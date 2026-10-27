package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"meshcraft/wire/agentv1"
)

// Automatic proxy update: the binary is replaced, not the container.
//
// The proxy downloads the binary for its architecture from the controller,
// verifies its SHA-256, stores it in its state volume and re-executes itself.
// The Docker image is not touched — hence no access to the Docker socket, and
// the same code works for a systemd installation.
//
// See docs/en/01-Architecture/Proxy versioning and updates.md.

// binDir: downloaded binaries live in the state volume, the only writable path
// of a hardened installation (StateDirectory on the systemd side).
func binDir() string { return filepath.Join(stateDir, "bin") }

func binPath(version string) string {
	return filepath.Join(binDir(), "meshcraft-proxy-"+version)
}

// upgradeOffer / upgradeReport are the agent contract's types
// (meshcraft/wire/agentv1), shared with the controller: the same declaration
// on both sides is what stops a field added on one side from being ignored on
// the other. The aliases keep the rest of the file unchanged.
type upgradeOffer = agentv1.UpgradeOffer

// report is what the proxy sends back on the next heartbeat. A silent failure
// would be worse than no update at all.
type upgradeReport = agentv1.UpgradeReport

// pendingReport carries the report until the next heartbeat. After a
// successful exec, it is the NEW process that reports — its only proof being
// the version it announces, which is enough for the controller.
var pendingReport *upgradeReport

// applyUpgrade carries out the proposed update. It only returns if something
// failed: on success, the process is replaced.
func applyUpgrade(st State, offer upgradeOffer) error {
	if offer.Version == "" || offer.URL == "" || offer.SHA256 == "" {
		return errors.New("incomplete update offer")
	}
	if offer.Version == version {
		return nil // already up to date: nothing to do
	}
	// The URL must be ON THE CONTROLLER of this local state. The controller is
	// trusted by construction, but nothing justifies executing a binary
	// downloaded elsewhere.
	var err = sameHost(offer.URL, st.Controller)
	if err != nil {
		return err
	}

	slog.Info("update: downloading", "version", offer.Version, "url", offer.URL)
	pendingReport = &upgradeReport{State: "downloading", Version: offer.Version}

	var path string
	path, err = download(offer)
	if err != nil {
		return err
	}

	// Recorded in state.json BEFORE the exec: it is this record, and not the
	// presence of the file, that will authorise execution at the next startup.
	var newState = st
	newState.Binary = &BinaryRecord{
		Version:      offer.Version,
		SHA256:       offer.SHA256,
		BootAttempts: 1,
		Confirmed:    false,
	}
	if err = saveState(newState); err != nil {
		return fmt.Errorf("state not persisted: %w", err)
	}

	slog.Info("update: exec of the new binary", "version", offer.Version, "path", path)
	return execBinary(path, offer.Version)
}

// download fetches the binary and REFUSES to install it if the SHA-256 does
// not match. The temporary file is written next to the target then renamed:
// no half-written executable binary.
func download(offer upgradeOffer) (string, error) {
	var err = os.MkdirAll(binDir(), 0o750)
	if err != nil {
		return "", err
	}
	var tmp *os.File
	tmp, err = os.CreateTemp(binDir(), ".dl-*")
	if err != nil {
		return "", err
	}
	var tmpName = tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck

	var resp, reqErr = client.Get(offer.URL)
	if reqErr != nil {
		_ = tmp.Close()
		return "", reqErr
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		_ = tmp.Close()
		return "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	var h = sha256.New()
	var written int64
	written, err = io.Copy(tmp, io.TeeReader(resp.Body, h))
	_ = tmp.Close()
	if err != nil {
		return "", err
	}
	if offer.Size > 0 && written != offer.Size {
		return "", fmt.Errorf("unexpected size: %d bytes received, %d announced", written, offer.Size)
	}
	var got = hex.EncodeToString(h.Sum(nil))
	if got != offer.SHA256 {
		return "", fmt.Errorf("unexpected SHA-256: %s (expected %s)", got, offer.SHA256)
	}
	if err = os.Chmod(tmpName, 0o750); err != nil {
		return "", err
	}
	var final = binPath(offer.Version)
	if err = os.Rename(tmpName, final); err != nil {
		return "", err
	}
	return final, nil
}

// sameHost checks that the download URL is indeed on the current controller
// (same scheme and same host).
func sameHost(rawURL, controller string) error {
	var u, err = url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("unreadable update URL: %w", err)
	}
	var c, cErr = url.Parse(controller)
	if cErr != nil {
		return fmt.Errorf("unreadable controller URL: %w", cErr)
	}
	if !strings.EqualFold(u.Host, c.Host) || u.Scheme != c.Scheme {
		return fmt.Errorf("update URL outside the controller (%s ≠ %s)", u.Host, c.Host)
	}
	return nil
}

// execBinary replaces the current process. MESHCRAFT_UPGRADED_FROM is a guard:
// if the executed binary announced a version inconsistent with the record, the
// bootstrap would refuse to start over indefinitely.
func execBinary(path, toVersion string) error {
	var env = append(os.Environ(), "MESHCRAFT_UPGRADED_FROM="+version)
	var err = syscall.Exec(path, []string{path}, env)
	// syscall.Exec only returns on failure.
	return fmt.Errorf("exec %s (→ %s): %w", path, toVersion, err)
}

// bootstrapBinary is called at STARTUP, before anything else.
//
// After a container reboot, the entrypoint relaunches the image's binary — the
// old version. So this is where we switch back to the updated binary, and
// where we refuse to when it does not start.
func bootstrapBinary(st State) State {
	if st.Binary == nil || st.Binary.Version == "" {
		return st
	}
	// We already are the right binary: we will confirm on the first successful heartbeat.
	if st.Binary.Version == version {
		return st
	}
	var record = st.Binary

	// Guard: a binary that does not announce the recorded version must not cause
	// an exec loop.
	if os.Getenv("MESHCRAFT_UPGRADED_FROM") != "" {
		slog.Error("update: the executed binary does not announce the expected version — giving up",
			"expected", record.Version, "announced", version)
		return fallback(st, "inconsistent binary: "+version+" instead of "+record.Version)
	}

	var path = binPath(record.Version)
	var err = verifyFile(path, record.SHA256)
	if err != nil {
		slog.Error("update: recorded binary unusable", "err", err)
		_ = os.Remove(path)
		return fallback(st, err.Error())
	}
	if record.BootAttempts >= 2 {
		// Two startups without confirmation: the binary cannot stand up.
		// We go back to the image's and make it known.
		slog.Error("update: two startups without confirmation — back to the image binary",
			"version", record.Version)
		_ = os.Remove(path)
		return fallback(st, "binary "+record.Version+" does not start (2 attempts)")
	}

	record.BootAttempts++
	st.Binary = record
	if err = saveState(st); err != nil {
		slog.Error("update: startup counter not persisted", "err", err)
		return st
	}
	slog.Info("update: switching to the recorded binary",
		"version", record.Version, "tentative", record.BootAttempts)
	if err = execBinary(path, record.Version); err != nil {
		slog.Error("update: exec impossible", "err", err)
		return fallback(st, err.Error())
	}
	return st // inatteignable
}

// fallback gives up the recorded binary, goes back to the image's and prepares
// the failure report for the next heartbeat.
func fallback(st State, reason string) State {
	var failed = ""
	if st.Binary != nil {
		failed = st.Binary.Version
	}
	st.Binary = nil
	var err = saveState(st)
	if err != nil {
		slog.Error("state not persisted after fallback", "err", err)
	}
	pendingReport = &upgradeReport{State: "failed", Version: failed, Error: reason}
	return st
}

// verifyFile re-reads the file and compares its fingerprint with the recorded one.
func verifyFile(path, wantSHA string) error {
	var f, err = os.Open(path)
	if err != nil {
		return fmt.Errorf("binary missing: %w", err)
	}
	defer f.Close() //nolint:errcheck
	var h = sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return err
	}
	var got = hex.EncodeToString(h.Sum(nil))
	if got != wantSHA {
		return fmt.Errorf("SHA-256 of the recorded binary does not match (%s ≠ %s)", got, wantSHA)
	}
	return nil
}

// confirmBinary: a heartbeat succeeded, the version holds. The counter is reset
// to zero so the next reboot does not consume an attempt.
func confirmBinary(st *State) {
	if st.Binary == nil || st.Binary.Confirmed && st.Binary.BootAttempts == 0 {
		return
	}
	if st.Binary.Version != version {
		return
	}
	st.Binary.Confirmed = true
	st.Binary.BootAttempts = 0
	var err = saveState(*st)
	if err != nil {
		slog.Error("version confirmation not persisted", "err", err)
	}
	slog.Info("update confirmed", "version", version)
}

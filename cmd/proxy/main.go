// The meshcraft proxy — phase 0 skeleton.
//
// It enrolls with the controller using MESHCRAFT_ENROLL_TOKEN, persists its
// identity (proxy_id + secret) and maintains an HTTP heartbeat. The
// controller can order it to MIGRATE to another instance: the proxy then
// enrolls on the new URL, rewrites its local state, and switches over to it
// permanently — with no intervention on the proxy's machine.
//
// The multiplexed WSS tunnel (yamux) will replace this heartbeat in phase 0bis.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"meshcraft/wire/agentv1"
)

// Injected at build time via -ldflags (see Dockerfile):
//
//	version = UTC build timestamp YYYYMMDD.HHMMSS — this is THE technical
//	          version, text-comparable, the one the controller compares against;
//	product = product version (VERSION file), informational.
//
// THE DEFAULTS ARE NO LONGER "dev", and that word had a cost. A proxy that
// announces "dev" is lexicographically NEWER than any timestamp — digits sort
// before letters — so the controller no longer offers it anything and marks
// it "up to date", permanently. Observed on a real proxy, and impossible to
// fix other than by going to its machine, which this architecture forbids.
//
// Two fixes keep this bug closed, and both are needed: the build string now
// REFUSES to produce a binary without a timestamp (Dockerfile guard rail),
// and [distrib.IsNewer] treats a version that isn't a timestamp as "to be
// replaced" instead of comparing it. This default therefore only survives for
// a `go run`: it is named for what it is, it does not look like a delivery
// channel, and it gets replaced by the packaged build on the first heartbeat.
var (
	version = "out-of-build"
	product = "out-of-build"
)

// protocolVersion: controller <-> proxy protocol compatibility. A build
// timestamp cannot say what we refuse — that's the role of this integer
// (see docs/en/01-Architecture/Controller-proxy protocol.md).
const protocolVersion = 1

const stateDir = "/var/lib/meshcraft"

// State is the proxy's local state: this file alone is enough to re-attach it.
//
// No site: a proxy is a network point of presence for the instance, it
// belongs to no site (see docs/en/01-Architecture/Proxy.md).
type State struct {
	Controller string `json:"controller"`
	ProxyID    string `json:"proxy_id"`
	Secret     string `json:"secret"`
	// Binary: the updated binary this proxy must run. It is this RECORD —
	// not the mere presence of a file — that authorizes execution: a binary
	// dropped into the volume by a third party has neither provenance nor an
	// expected SHA.
	Binary *BinaryRecord `json:"binary,omitempty"`
}

// BinaryRecord describes the registered binary and the state of its rollout.
type BinaryRecord struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	// BootAttempts counts unconfirmed startups: beyond two, we fall back to
	// the image's binary (anti-crash-loop).
	BootAttempts int  `json:"boot_attempts"`
	Confirmed    bool `json:"confirmed"`
}

func statePath() string { return filepath.Join(stateDir, "state.json") }

func loadState() (State, error) {
	var st State
	var data, err = os.ReadFile(statePath())
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

func saveState(st State) error {
	var err = os.MkdirAll(stateDir, 0o750)
	if err != nil {
		return err
	}
	var data, marshalErr = json.MarshalIndent(st, "", "  ")
	if marshalErr != nil {
		return marshalErr
	}
	return os.WriteFile(statePath(), data, 0o600)
}

// normalizeURL accepts ws:// and wss:// for convenience: the heartbeat speaks HTTP(S).
func normalizeURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	u = strings.Replace(u, "wss://", "https://", 1)
	u = strings.Replace(u, "ws://", "http://", 1)
	return u
}

var client = &http.Client{Timeout: 20 * time.Second}

func postJSON(url string, in any, out any) (int, error) {
	var body, err = json.Marshal(in)
	if err != nil {
		return 0, err
	}
	var resp, reqErr = client.Post(url, "application/json", bytes.NewReader(body))
	if reqErr != nil {
		return 0, reqErr
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}

// enroll exchanges an enrollment token for a durable identity.
//
// The proxy stays on the legacy /api/proxy/* paths deliberately: a proxy in
// the field may be pointed at a controller too old to serve /api/v1/agent/*,
// and the aliases exist precisely so it never has to care. The BODIES are the
// shared agentv1 types — that is what keeps the two ends from drifting; the
// path is not.
func enroll(controller, token, name string) (State, error) {
	var out agentv1.EnrollResponse
	var code, err = postJSON(controller+"/api/proxy/enroll",
		agentv1.EnrollRequest{Token: token, Name: name}, &out)
	if err != nil {
		return State{}, err
	}
	if code != http.StatusOK || out.ProxyID == "" {
		return State{}, fmt.Errorf("enrollment refused (HTTP %d): %s", code, out.Error)
	}
	// The controller may announce its canonical public URL.
	var base = controller
	if out.ControllerURL != "" {
		base = normalizeURL(out.ControllerURL)
	}
	return State{Controller: base, ProxyID: out.ProxyID, Secret: out.Secret}, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	var envController = normalizeURL(os.Getenv("MESHCRAFT_CONTROLLER"))
	var token = os.Getenv("MESHCRAFT_ENROLL_TOKEN")
	var name = os.Getenv("MESHCRAFT_PROXY_NAME")

	var st, loadErr = loadState()
	if loadErr != nil || st.ProxyID == "" {
		// First startup: enrollment is mandatory.
		if envController == "" || token == "" {
			slog.Error("first startup: MESHCRAFT_CONTROLLER and MESHCRAFT_ENROLL_TOKEN are required")
			os.Exit(1)
		}
		var attempt int
		for attempt = 0; ; attempt++ {
			var newState, err = enroll(envController, token, name)
			if err == nil {
				st = newState
				var saveErr = saveState(st)
				if saveErr != nil {
					slog.Error("local state not persisted", "err", saveErr)
				}
				slog.Info("enrolled", "controller", st.Controller, "proxy_id", st.ProxyID)
				break
			}
			slog.Error("enrollment failed", "err", err, "attempt", attempt+1)
			time.Sleep(backoff(attempt))
		}
	} else {
		slog.Info("local state loaded", "controller", st.Controller, "proxy_id", st.ProxyID)
		// MESHCRAFT_CONTROLLER never overwrites existing state: switching
		// instance goes through a migration order from the controller.
		if envController != "" && envController != st.Controller {
			slog.Warn("MESHCRAFT_CONTROLLER differs from local state — local state wins",
				"env", envController, "state", st.Controller)
		}
	}

	// BEFORE anything else: switch back to the updated binary if the local
	// state has one registered (a container reboot always relaunches the
	// image's binary). On success, this process is replaced right here.
	st = bootstrapBinary(st)

	slog.Info("meshcraft proxy starting up", "version", version, "product", product,
		"protocol", protocolVersion, "controller", st.Controller)

	// The tunnel lives in its own loop: the heartbeat keeps going even if the
	// WebSocket fails to establish (uncooperative reverse proxy, WS blocked),
	// and it is the tunnel that will always carry migration orders.
	// SNMP collection: the controller sends targets over the monitoring
	// channel, the proxy polls and pushes (see cmd/proxy/snmp.go).
	snmpCollector = NewSNMPCollector(st.Controller, st.ProxyID, st.Secret)
	// Port transitions go out over the SAME channel as device transitions:
	// it's already the one the tunnel reports through, and it never blocks.
	// Two channels for two events that go to the same place would only add
	// an ordering to arbitrate (see cmd/proxy/trap.go).
	snmpCollector.SetEmitter(monitor.emit)
	go snmpCollector.Run(context.Background())

	// Streaming telemetry, for the devices the controller carries on gNMI rather
	// than SNMP (cf. cmd/proxy/gnmi.go). It BORROWS the SNMP collector's buffer
	// and its LLDP route rather than opening its own: the proxy has exactly one
	// metric buffer, with one saturation counter, and a second producer with a
	// second buffer would drop samples in a place its own saturation report never
	// mentions.
	gnmiCollector = NewGNMICollector(snmpCollector.buf, snmpCollector.pushLLDP)
	gnmiCollector.SetEmitter(monitor.emit)

	go runTunnel(st, version)

	var fails int
	for {
		var out agentv1.HeartbeatResponse
		var ifaces, _ = json.Marshal(localIfaces())
		var body = agentv1.HeartbeatRequest{
			ProxyID: st.ProxyID, Secret: st.Secret, Version: version,
			Protocol: protocolVersion, OS: runtime.GOOS, Arch: goarch(),
			Ifaces:  ifaces,
			Upgrade: pendingReport,
		}
		var code, err = postJSON(st.Controller+"/api/proxy/heartbeat", body, &out)
		switch {
		case err != nil:
			fails++
			slog.Warn("controller unreachable", "err", err, "fails", fails)
		case code == http.StatusUnauthorized:
			// Revoked or migrated: don't hammer it in a tight loop.
			fails++
			slog.Error("proxy refused by the controller (revoked?)", "controller", st.Controller)
		case code != http.StatusOK:
			fails++
			slog.Warn("heartbeat error", "status", code, "error", out.Error)
		default:
			fails = 0
			// An accepted heartbeat counts as confirmation: the version holds up.
			pendingReport = nil
			confirmBinary(&st)
			if out.Upgrade != nil {
				var upErr = applyUpgrade(st, *out.Upgrade)
				if upErr != nil {
					slog.Error("upgrade failed — staying on current version",
						"version", out.Upgrade.Version, "err", upErr)
					pendingReport = &upgradeReport{
						State: "failed", Version: out.Upgrade.Version,
						Error: firstLine(upErr.Error()),
					}
				}
			}
			if out.Status == "migrate" && out.Migrate != nil && out.Migrate.ControllerURL != "" {
				var migrated, mErr = migrate(st, out.Migrate.ControllerURL, out.Migrate.EnrollToken, name)
				if mErr != nil {
					slog.Error("migration failed — staying on current instance",
						"target", out.Migrate.ControllerURL, "err", mErr)
				} else {
					st = migrated
					slog.Info("migration complete", "controller", st.Controller,
						"proxy_id", st.ProxyID)
				}
			}
		}
		time.Sleep(heartbeatDelay(fails))
	}
}

// migrate enrolls the proxy on the new instance, persists the new state,
// then notifies the old one (best-effort) that it can forget us.
func migrate(old State, targetURL, token, name string) (State, error) {
	targetURL = normalizeURL(targetURL)
	if token == "" {
		return old, errors.New("migration order without an enrollment token")
	}
	slog.Info("migration order received", "target", targetURL)
	var st, err = enroll(targetURL, token, name)
	if err != nil {
		return old, err
	}
	// Local state switches over BEFORE notifying the old instance: if the
	// process dies here, it restarts already attached to the new one.
	if err = saveState(st); err != nil {
		return old, fmt.Errorf("new state not persisted: %w", err)
	}
	var code, ackErr = postJSON(old.Controller+"/api/proxy/migrated", agentv1.MigratedRequest{
		ProxyID: old.ProxyID, Secret: old.Secret, NewController: st.Controller,
	}, nil)
	if ackErr != nil || code != http.StatusOK {
		slog.Warn("old instance not notified (no consequence)",
			"status", code, "err", ackErr)
	}
	return st, nil
}

// localIfaces announces the local IPs: the controller can then propose
// subnets to scan.
func localIfaces() []map[string]string {
	var out = []map[string]string{}
	var addrs, err = netInterfaceAddrs()
	if err != nil {
		return out
	}
	var a string
	for _, a = range addrs {
		out = append(out, map[string]string{"ip": a})
	}
	return out
}

func backoff(attempt int) time.Duration {
	var d = time.Duration(1<<min(attempt, 6)) * time.Second
	if d > 60*time.Second {
		d = 60 * time.Second
	}
	return d
}

func heartbeatDelay(fails int) time.Duration {
	if fails == 0 {
		return 30 * time.Second
	}
	return backoff(fails)
}

// goarch translates the Go architecture into the artifact suffix published
// by the controller. GOARM doesn't appear in runtime.GOARCH: "arm" is enough.
func goarch() string { return runtime.GOARCH }

func firstLine(s string) string {
	var i = strings.IndexAny(s, "\r\n")
	if i >= 0 {
		return s[:i]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

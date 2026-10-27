package s3900

// The second observation source of the hostname reconciliation, on this range:
// `show run` → parseRunningConfig → [Client.noteSysName] → ReadIdentity →
// driver.Identity.Hostname → api.identityFacts → `extra.identity.hostname`.
//
// Pure, and it has to be: every step here is a memory read by construction, and
// that is the property being pinned as much as the value.

import (
	"context"
	"io"
	"strings"
	"testing"
)

// recStdin records everything a session would send to the device. Nothing writes
// to it in a passing run — that IS the assertion.
type recStdin struct{ sent strings.Builder }

func (w *recStdin) Write(p []byte) (int, error) { return w.sent.Write(p) }
func (w *recStdin) Close() error                { return nil }

func TestReadIdentityCarriesTheNameTheRunningConfigAnnounced(t *testing.T) {
	// labRun is the 2026-08-01/02 serial capture on OpCode 1.7.0 and it carries a
	// non-default "hostname test-fs-s3900" — the READ form on this range is
	// CAPTURED, which is what makes this connector's half of the fix evidenced
	// rather than deduced (the S3400 next door has no such capture; cf. its
	// identityread.go).
	var c Client
	c.facts = versionFacts{Serial: "CG2003161906N0002", OpCode: "1.7.0"}
	c.noteSysName(parseRunningConfig(labRun, "").Hostname)

	var id, err = c.ReadIdentity(context.Background())
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if id.Hostname != "test-fs-s3900" {
		t.Fatalf("Hostname=%q: the name ReadState read is what api.identityFacts "+
			"persists under `extra.identity.hostname`, and it is the ONLY observation "+
			"source this range has — it serves no LLDP system name", id.Hostname)
	}
	if id.Serial != "CG2003161906N0002" || id.Firmware != "1.7.0" {
		t.Errorf("the facts login established were lost: %+v", id)
	}
}

func TestReadIdentitySendsNothingToTheDevice(t *testing.T) {
	// ZERO WIRE ROUND-TRIPS is this interface's admission ticket (cf.
	// driver.IdentityReader), and the system name is the one field that would
	// tempt an implementer to break it: it lives in `show run` and nowhere else,
	// a read of several seconds on this platform which api.observeIdentity would
	// then pay on EVERY read-state and after EVERY rename. It is harvested from
	// ReadState instead — this test is what notices if it stops being.
	var rec = &recStdin{}
	var c = Client{cli: &cliConn{stdin: rec, out: make(chan byte), banner: "S3900-24T4S"}}

	// A cancelled context so a regression that DID send a command returns at once
	// instead of hanging on showRunTimeout.
	var ctx, cancel = context.WithCancel(context.Background())
	cancel()
	var id, _ = c.ReadIdentity(ctx)

	if rec.sent.Len() != 0 {
		t.Fatalf("ReadIdentity sent %q to the device: identity on an open access is a "+
			"memory read, and a connector that needs a command to answer must not "+
			"implement driver.IdentityReader at all", rec.sent.String())
	}
	if id.Model != "S3900-24T4S" {
		t.Errorf("Model=%q, want the one the login banner already carried", id.Model)
	}
	if id.Hostname != "" {
		t.Errorf("Hostname=%q on an access where no read-state has run: an unread "+
			"name must read as \"nothing observed\"", id.Hostname)
	}
}

func TestOurOwnWriteIsNeverReportedAsTheDeviceSName(t *testing.T) {
	// SetHostname re-reads `show run` to verify itself, and
	// api.refreshIdentityAfterHostname then calls ReadIdentity on that same
	// session. If the write path fed the cache, meshcraft would record its own
	// command as the device's announcement and every state would be
	// self-confirming — the one thing hostnameheal.go's contract forbids. The
	// write clears the cache instead.
	var c Client
	c.noteSysName("sw-lab-01-before")
	c.forgetSysName()

	var id, _ = c.ReadIdentity(context.Background())
	if id.Hostname != "" {
		t.Fatalf("Hostname=%q after a write: a name observed BEFORE the rename is "+
			"stale, and the rename's own re-read is not an observation", id.Hostname)
	}
}

func TestAConfigurationWithNoHostnameLineObservesNothing(t *testing.T) {
	// CONSTRUCTED fixture: a running-config fragment with no `hostname` line, the
	// shape of a switch still on its factory name. It must produce "nothing
	// observed", never an observation of the empty string — the difference
	// between `not_observed` (no repair) and a device announcing "".
	var c Client
	c.noteSysName(parseRunningConfig("!\ninterface ethernet 1/1\n!\n", "").Hostname)

	var id, _ = c.ReadIdentity(context.Background())
	if id.Hostname != "" {
		t.Fatalf("Hostname=%q where the configuration carries no such line", id.Hostname)
	}
}

// Compile-time: recStdin is the io.WriteCloser cliConn holds.
var _ io.WriteCloser = (*recStdin)(nil)

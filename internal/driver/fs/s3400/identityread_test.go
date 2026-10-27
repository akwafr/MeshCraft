package s3400

// The second observation source of the hostname reconciliation, on this range:
// "show running-config" → ParseRunningConfig → [Client.noteSysName] →
// ReadIdentity → driver.Identity.Hostname → api.identityFacts →
// `extra.identity.hostname`.
//
// WHAT IS EVIDENCE HERE AND WHAT IS NOT. labRunningConfig is a verbatim capture
// of the lab 48T4SP (2026-08-03, Version 2.0.2J) and it carries NO `hostname`
// line — the switch was on its factory name. So this package has never seen this
// platform render one, and the fixture below that does is CONSTRUCTED from the
// connector's own write form. What the tests pin is the AGREEMENT between the
// line this connector sends and the line its parser reads back, plus the honest
// verdict on the capture that exists. The device's own rendering — quoting,
// case, a trailing domain — stays to be captured live.

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
	// FIXTURE CONSTRUCTED (cf. the file header): the running-config fragment a
	// renamed switch would carry, built from hostnameCmds so the write form and
	// the read form cannot drift apart unnoticed.
	var c Client
	c.facts = parseShowVersion(labShowVersion)
	c.noteSysName(ParseRunningConfig("!\n" + hostnameCmds("sw-lab-01")[0] + "\n!\n").Hostname)

	var id, err = c.ReadIdentity(context.Background())
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if id.Hostname != "sw-lab-01" {
		t.Fatalf("Hostname=%q: the name ReadState read is what api.identityFacts "+
			"persists under `extra.identity.hostname`, and it is the ONLY observation "+
			"source this range has — its agent serves nothing under 1.0.8802 and the "+
			"CLI LLDP path writes no sys_name", id.Hostname)
	}
	// The rest comes from the login `show version`, which is why identity on an
	// open access needs no session of its own here.
	if id.Model != "S3400-48T4SP" || id.Serial != "CG2003161906N0002" ||
		id.Firmware != "2.0.2J" || id.MAC != "64:9d:99:22:7b:95" {
		t.Errorf("the facts login established were lost: %+v", id)
	}
}

func TestTheCapturedFactoryConfigurationAnnouncesNoName(t *testing.T) {
	// The one piece of hardware evidence this package holds about the hostname
	// line is that the captured configuration has NONE: a switch meshcraft has
	// never renamed announces nothing, and must read as `not_observed` rather
	// than as a device named "" or named "Switch" after its prompt.
	if ParseRunningConfig(labRunningConfig).Hostname != "" {
		t.Fatalf("the 2026-08-03 capture now yields a hostname (%q) — either the "+
			"fixture changed or reCfgHostname started matching something else",
			ParseRunningConfig(labRunningConfig).Hostname)
	}
	var c Client
	c.noteSysName(ParseRunningConfig(labRunningConfig).Hostname)
	var id, _ = c.ReadIdentity(context.Background())
	if id.Hostname != "" {
		t.Fatalf("Hostname=%q on a factory-named switch", id.Hostname)
	}
}

func TestReadIdentitySendsNothingToTheDevice(t *testing.T) {
	// ZERO WIRE ROUND-TRIPS is driver.IdentityReader's admission ticket, and on
	// THIS range breaking it is expensive in a way that would not show up in a
	// test suite: the system name lives in "show running-config", a
	// ninety-second paged read ("terminal length 0" does nothing on this
	// firmware — cf. ssh.go), which api.observeIdentity would then pay on every
	// read-state and after every rename. It is harvested from ReadState instead.
	var rec = &recStdin{}
	var c = Client{cli: &cliConn{stdin: rec, out: make(chan byte),
		banner: "Welcome to FS S3400-48T4SP Ethernet Switch"}}

	// A cancelled context so a regression that DID send a command returns at once
	// instead of hanging for ninety seconds.
	var ctx, cancel = context.WithCancel(context.Background())
	cancel()
	var id, _ = c.ReadIdentity(ctx)

	if rec.sent.Len() != 0 {
		t.Fatalf("ReadIdentity sent %q to the device: identity on an open access is a "+
			"memory read, and a connector that needs a command to answer must not "+
			"implement driver.IdentityReader at all", rec.sent.String())
	}
	// With no "show version" in memory the banner is the fallback, exactly as
	// on the discovery path.
	if id.Model != "S3400-48T4SP" {
		t.Errorf("Model=%q, want the one the login banner already carried", id.Model)
	}
	if id.Hostname != "" {
		t.Errorf("Hostname=%q on an access where no read-state has run: an unread "+
			"name must read as \"nothing observed\"", id.Hostname)
	}
}

func TestOurOwnWriteIsNeverReportedAsTheDeviceSName(t *testing.T) {
	// SetHostname pays for a whole "show running-config" to verify itself, and
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

// Compile-time: recStdin is the io.WriteCloser cliConn holds.
var _ io.WriteCloser = (*recStdin)(nil)

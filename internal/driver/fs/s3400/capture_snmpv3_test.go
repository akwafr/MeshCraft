package s3400

// Replays of the 2026-08-11 captures taken on poefs3400lab (Version 2.0.2J,
// build 95262) through [cliConn.runPaged] — the device's own bytes, pager
// artefacts already stripped by the connector's own read path, secrets replaced
// by REDACTED-* placeholders and nothing else touched.
//
// They exist because this range had NO running-config fixture at all: every
// testdata file was an interface, LLDP or spanning-tree capture, so
// [ParseRunningConfig] — which decides the hostname, the SNMP community, the
// STP mode and every port's VLAN membership — was pinned by hand-written
// strings only.

import (
	"strings"
	"testing"
)

// TestParseRunningConfigFromCapture is the fixture the hostname path never had.
//
// [Client.SetHostname] writes "hostname <name>" and then judges the write by
// re-reading the configuration and taking [RunningConfig.Hostname]. Until this
// capture the regexp behind that — bare, unquoted, first token — rested on the
// S3900 CLI reference for the family, i.e. on a DIFFERENT firmware. Here is the
// S3400's own rendering of a non-default hostname.
func TestParseRunningConfigFromCapture(t *testing.T) {
	var cfg = ParseRunningConfig(readCapture(t, "show_running_config.txt"))

	if cfg.Hostname != "poefs3400lab" {
		t.Errorf("hostname = %q, want \"poefs3400lab\" — the re-read that confirms "+
			"SetHostname reads exactly this field", cfg.Hostname)
	}
	if cfg.STPMode != "rstp" || cfg.STPOff {
		t.Errorf("stp mode = %q off = %v, want rstp / false", cfg.STPMode, cfg.STPOff)
	}
	if len(cfg.Communities) != 1 || cfg.Communities[0] != "REDACTED-COMMUNITY" {
		t.Errorf("communities = %v — the \"snmp-server community 0 <s> RO\" rendering "+
			"must yield exactly one", cfg.Communities)
	}
	// No "snmp-server host" in the baseline: the trap path must read ZERO
	// receivers here, not one empty one.
	if len(cfg.TrapReceivers) != 0 {
		t.Errorf("trap receivers = %+v on a configuration that declares none", cfg.TrapReceivers)
	}
	// The interface stanzas are the bulk of this file and the reason a truncated
	// read is dangerous: 48 copper + 4 SFP+ sockets all carry switchport lines.
	if len(cfg.Ports) < 52 {
		t.Errorf("only %d ports parsed from a 48T4SP capture", len(cfg.Ports))
	}
	var tg1, ok = cfg.Ports["XG1"]
	if !ok {
		t.Fatalf("XG1 absent — ports were %v", cfg.Ports)
	}
	if !strings.EqualFold(tg1.Mode, "trunk") {
		t.Errorf("XG1 mode = %q, want trunk (it is this unit's only uplink)", tg1.Mode)
	}
}

// TestParseTrapHostFromCapture turns trap.go's ONE deduction into a capture.
//
// [parseTrapHostLine] was written token by token because nobody had ever seen
// how this firmware renders "snmp-server host" back, and the whole
// verification of [Client.ApplyTrapReceiver] hangs on that rendering: an
// unrecognized line makes the apply return an error rather than record a
// destination the device did not keep.
//
// MEASURED 2026-08-11: the line comes back with its tokens IN THE ORDER TYPED,
// which is what the deduction assumed —
//
//	typed:  snmp-server host 10.42.0.67 version v2c mcprobe
//	stored: snmp-server host 10.42.0.67 version v2c mcprobe
//
// with one trailing space added, exactly like the community line's rendering.
// Unlike the community line, NO case is changed and no encryption-type digit is
// inserted.
func TestParseTrapHostFromCapture(t *testing.T) {
	var cfg = ParseRunningConfig(readCapture(t, "show_running_config_snmpv3_traphost.txt"))

	if len(cfg.TrapReceivers) != 1 {
		t.Fatalf("trap receivers = %+v, want exactly one", cfg.TrapReceivers)
	}
	var r = cfg.TrapReceivers[0]
	if r.Host != "10.42.0.67" {
		t.Errorf("host = %q, want 10.42.0.67", r.Host)
	}
	if r.Version != "v2c" {
		t.Errorf("version = %q, want v2c — \"version\" is a KEYWORD here and must not "+
			"be mistaken for the version itself", r.Version)
	}
	if r.Community != "mcprobe" {
		t.Errorf("community = %q, want mcprobe", r.Community)
	}

	// The removal form is the one that matters operationally: the short "no
	// snmp-server host <ip>" answers "Incomplete command" AND LEAVES THE HOST.
	// A receiver read from this rendering must produce the FULL form.
	var line, buildable = trapRemoveLine(r)
	if !buildable {
		t.Fatal("trapRemoveLine refused a receiver parsed from the device's own rendering — " +
			"every ApplyTrapReceiver replacement on this range would leave the old host in place")
	}
	if line != "no snmp-server host 10.42.0.67 version v2c mcprobe" {
		t.Errorf("removal line = %q", line)
	}

	// The same capture carries the v3 identity, and the community must still be
	// found beside it: a v3 user in the file must not shadow the v2c read that
	// ApplySNMP's idempotence rests on.
	if len(cfg.Communities) != 1 || cfg.Communities[0] != "REDACTED-COMMUNITY" {
		t.Errorf("communities = %v with a v3 user in the same configuration", cfg.Communities)
	}
	if cfg.Hostname != "poefs3400lab" {
		t.Errorf("hostname = %q", cfg.Hostname)
	}
}

// TestSNMPv3CapturesArePinned guards the two reads a future v3 applier would
// verify itself with. It asserts nothing about a parser — there is none, and
// [Client.ApplySNMP] still refuses v3 — but it pins WHAT THE DEVICE SAYS, which
// is the thing that was missing when the applier was last attempted and stopped.
//
// The two facts that decide whether a v3 write is verifiable at all on this
// range, both measured 2026-08-11 on poefs3400lab:
//
//   - "show snmp user" DOES name the group ("Group-name: mcliveg") and the
//     authentication protocol, and does NOT print any privacy line — so a user
//     naming a group that does not exist is catchable, and privacy is not;
//   - "show snmp group" EXISTS on this platform and prints the security model
//     ("security model: v3 priv"), which is where the missing privacy fact
//     comes back.
func TestSNMPv3CapturesArePinned(t *testing.T) {
	var user = readCapture(t, "show_snmp_user.txt")
	var group = readCapture(t, "show_snmp_group.txt")

	var want string
	for _, want = range []string{"User name:", "Engine ID:", "Authentication Protocol:", "Group-name:"} {
		if !strings.Contains(user, want) {
			t.Errorf("\"show snmp user\" capture carries no %q", want)
		}
	}
	if strings.Contains(strings.ToLower(user), "privacy") ||
		strings.Contains(strings.ToLower(user), "priv protocol") {
		t.Error("\"show snmp user\" DOES print a privacy line — the note saying it never " +
			"does is refuted, and a v3 applier could verify privacy from this read alone")
	}
	if !strings.Contains(group, "security model: v3 priv") {
		t.Errorf("\"show snmp group\" capture does not carry the security model:\n%s", group)
	}
	if !strings.Contains(group, "groupname:") {
		t.Errorf("\"show snmp group\" capture does not name the group:\n%s", group)
	}
}

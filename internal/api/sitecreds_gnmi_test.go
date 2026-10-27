package api

import (
	"strings"
	"testing"
)

// The `gnmi` credential kind, seen from the handler that files it.
//
// What is locked here is the half of the chain the store cannot defend. The column has a
// CHECK constraint, so a bad `transport_tls` never reaches disk — but it reaches PostgreSQL,
// and what comes back is a 500 naming a constraint, on a form where the operator picked one
// of three radio buttons. The rules below turn every one of those into a sentence that says
// which field is wrong and what it accepts.
//
// The rest is the failure this whole feature exists to close: a telemetry credential that
// LOOKS configured and streams nothing, or streams with the password in the clear.

func TestGNMIScopeIsFree(t *testing.T) {
	// Site, group and tag all resolve for this kind — gnmiCredentialFor walks
	// CredentialsForDevice, the same tag/group/site ladder an admin password uses. Pinning it
	// to site scope, the way a v2c community is pinned, would make "this family of Aristas has
	// its own telemetry account" unexpressible.
	var cas = []scopeCase{
		{name: "site gnmi", kind: CredKindGNMI},
		{name: "group gnmi", kind: CredKindGNMI, group: "arista-7050"},
		{name: "tagged gnmi", kind: CredKindGNMI, tag: "core", wantTag: "core"},
		{
			name: "gnmi under a password-policy tag: refused, meshcraft owns that namespace",
			kind: CredKindGNMI, tag: "pwpolicy-len15", rejected: true,
		},
		{
			name: "gnmi under an SNMPv3 tag: refused, a crypto pair does not scope gRPC",
			kind: CredKindGNMI, tag: "snmpv3-sha256-aes128", rejected: true,
		},
	}
	var c scopeCase
	for _, c = range cas {
		var tag, err = checkCredScope(c.kind, c.group, c.tag)
		if c.rejected {
			if err == nil {
				t.Errorf("%s: wrongly accepted", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: wrongly rejected (%v)", c.name, err)
			continue
		}
		if tag != c.wantTag {
			t.Errorf("%s: tag %q, want %q", c.name, tag, c.wantTag)
		}
	}
}

// An SNMPv3 tag on a gnmi credential is the dangerous refusal of the two, because it is the
// one that would have WORKED: devices really do carry `snmpv3-sha256-aes128`, so the
// credential would resolve — on whichever subset of the fleet an SNMP crypto choice happens to
// describe. The message therefore has to say what the tag actually means, not just "no".
func TestGNMIRefusesV3TagWithReason(t *testing.T) {
	var _, err = checkCredScope(CredKindGNMI, "", "snmpv3-sha256-aes128")
	if err == nil {
		t.Fatal("SNMPv3 tag accepted as a gNMI scope")
	}
	var want string
	for _, want = range []string{"snmpv3-sha256-aes128", "ALGORITHM PAIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not mention %q: %s", want, err.Error())
		}
	}
}

func TestCredTransportModes(t *testing.T) {
	var cas = []struct {
		name     string
		kind     string
		mode     string
		caPEM    string
		wantMode string
		wantCA   string
		rejected bool
	}{
		{
			// The default is NAMED, not left empty for the store's coalesce. On an update the
			// two differ: empty preserves what is stored, so a client that stopped sending the
			// field would keep a `plaintext` posture nobody re-chose.
			name: "gnmi, nothing said: verify, explicitly",
			kind: CredKindGNMI, wantMode: TLSVerify,
		},
		{
			name: "gnmi, verify with a CA", kind: CredKindGNMI, mode: TLSVerify,
			caPEM: "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----",
			// Trimmed, because the store compares nothing and the proxy trims before parsing:
			// a value that round-trips differently from what was typed reads as data loss.
			wantMode: TLSVerify,
			wantCA:   "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----",
		},
		{
			name: "gnmi, skip-verify: accepted, it is what a self-signed switch needs",
			kind: CredKindGNMI, mode: TLSSkipVerify, wantMode: TLSSkipVerify,
		},
		{
			name: "gnmi, plaintext: accepted, it is an explicit choice",
			kind: CredKindGNMI, mode: TLSPlaintext, wantMode: TLSPlaintext,
		},
		{
			name: "case and spacing normalised, not refused",
			kind: CredKindGNMI, mode: "  Skip-Verify ", wantMode: TLSSkipVerify,
		},
		{
			name: "gnmi, plaintext WITH a CA: refused, there is no certificate to check",
			kind: CredKindGNMI, mode: TLSPlaintext, caPEM: "-----BEGIN CERTIFICATE-----",
			rejected: true,
		},
		{
			name: "invented mode: refused rather than assumed safe",
			kind: CredKindGNMI, mode: "tls", rejected: true,
		},
		{
			// Nothing to configure, so nothing to say — and nothing that would look configured.
			name: "admin, nothing said", kind: credKindAdmin,
		},
		{
			name:     "admin with a CA: refused, an SSH password has no CA to chain to",
			kind:     credKindAdmin,
			caPEM:    "-----BEGIN CERTIFICATE-----",
			rejected: true,
		},
		{
			name: "v2c with a transport mode: refused, a community has no TLS session",
			kind: "snmp_v2c", mode: TLSSkipVerify, rejected: true,
		},
	}
	var c = cas[0]
	for _, c = range cas {
		var mode, caPEM, err = checkCredTransport(c.kind, c.mode, c.caPEM)
		if c.rejected {
			if err == nil {
				t.Errorf("%s: wrongly accepted", c.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: wrongly rejected (%v)", c.name, err)
			continue
		}
		if mode != c.wantMode {
			t.Errorf("%s: mode %q, want %q", c.name, mode, c.wantMode)
		}
		if caPEM != c.wantCA {
			t.Errorf("%s: ca %q, want %q", c.name, caPEM, c.wantCA)
		}
	}
}

// The refusal of an unknown mode has to carry the three values, and it has to say what
// `plaintext` COSTS. An operator reading "expected verify, skip-verify or plaintext" with no
// consequence attached picks whichever one makes the error go away, and `plaintext` always
// makes errors go away.
func TestTransportRefusalNamesTheCost(t *testing.T) {
	var _, _, err = checkCredTransport(CredKindGNMI, "insecure", "")
	if err == nil {
		t.Fatal("unknown transport mode accepted")
	}
	var want string
	for _, want = range []string{TLSVerify, TLSSkipVerify, TLSPlaintext, "in clear"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not mention %q: %s", want, err.Error())
		}
	}
}

package api

import (
	"strings"
	"testing"
)

// scopeCase: a kind, a scope, and what the vault should do with it.
type scopeCase struct {
	name     string
	kind     string
	group    string
	tag      string
	wantTag  string // canonical tag expected when the case passes
	rejected bool
}

// Credential kind scope (docs/en/03-Security/SNMP identity.md section 2).
//
// What is locked here is not a typing preference: an SNMPv3 identity is a KEY
// DERIVED from the (auth, priv) pair. Stored under a tag that does not name that
// pair, it will either be missing at provisioning time or pushed to devices that
// reject it — and an SNMP refusal only shows up as missing metrics days later.
// Because the cost of a mistake here is invisible, the rule must be tested.

func TestSNMPKindScope(t *testing.T) {
	var cas = []scopeCase{
		{
			name: "v2c with a tag: rejected", kind: "snmp_v2c",
			tag: "snmpv3-sha-des", rejected: true,
		},
		{
			name: "v2c with an ops tag: rejected too",
			kind: "snmp_v2c", tag: "ops", rejected: true,
		},
		{
			name: "v2c with a group: rejected, v2c plan resolves at site",
			kind: "snmp_v2c", group: "instant-on-1930", rejected: true,
		},
		{name: "site v2c: accepted", kind: "snmp_v2c"},
		{
			name: "v3 without tag: rejected, a site does not share a v3 key",
			kind: "snmp_v3", rejected: true,
		},
		{
			name: "v3 under an ops tag: rejected",
			kind: "snmp_v3", tag: "ops", rejected: true,
		},
		{
			name: "v3 under a v3 tag: accepted",
			kind: "snmp_v3", tag: "snmpv3-sha-des", wantTag: "snmpv3-sha-des",
		},
		{
			name: "v3 without encryption: named explicitly, so accepted",
			kind: "snmp_v3", tag: "snmpv3-sha256-nopriv", wantTag: "snmpv3-sha256-nopriv",
		},
		{
			name: "v3 with unknown algorithm: rejected rather than assumed good",
			kind: "snmp_v3", tag: "snmpv3-rc4-aes128", rejected: true,
		},
		// Existing behavior unchanged: all three scopes keep their meaning for
		// the admin password, which is plain text.
		{name: "site admin", kind: "admin"},
		{name: "group admin", kind: "admin", group: "instant-on-1930"},
		{name: "tagged admin", kind: "admin", tag: "ops", wantTag: "ops"},
		{
			name: "invented kind: rejected, nothing would consume it",
			kind: "snmp", rejected: true,
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

// v3 tag is rewritten to canonical form. Provisioning will emit exactly
// snmpv3-sha-des; if the vault kept "SNMPv3-SHA-DES" as entered, it would not
// find the secret and would create a second one for the same crypto family —
// section 3.4 idempotence would break.
func TestV3TagCanonical(t *testing.T) {
	var tag, err = checkCredScope("snmp_v3", "", normalizeTag("  SNMPv3-SHA-DES "))
	if err != nil {
		t.Fatalf("wrongly rejected: %v", err)
	}
	if tag != "snmpv3-sha-des" {
		t.Errorf("tag %q, want snmpv3-sha-des", tag)
	}
}

// Refusal must NAME what is wrong: a bare 400 on this screen leaves the operator
// staring at a field they cannot fix.
func TestRefusalsExplain(t *testing.T) {
	var _, err = checkCredScope("snmp_v3", "", "ops")
	if err == nil {
		t.Fatal("non-v3 tag accepted")
	}
	var msg = err.Error()
	var want string
	// Wrong tag AND expected form: one says where to look, the other what to type.
	// A message with only the first leaves them guessing.
	for _, want = range []string{"ops", "snmpv3-"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q: %s", want, msg)
		}
	}
}

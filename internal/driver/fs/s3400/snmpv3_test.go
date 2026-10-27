package s3400

// Replays of the 2026-08-12 authNoPriv ladder taken on poefs3400lab (Version
// 2.0.2J build 95262) through [cliConn.runPaged] — the device's own bytes, the
// passphrase and the community replaced by REDACTED-* placeholders and nothing
// else touched.

import (
	"errors"
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/snmpv3"
)

// TestSNMPv3CapabilityIsWhatWasMeasured pins the declaration itself.
//
// The pair decides the TAG, therefore the shared secret, therefore which devices
// can carry the same v3 identity. Widening it on a hunch is the mistake this test
// exists to make loud.
func TestSNMPv3CapabilityIsWhatWasMeasured(t *testing.T) {
	var caps = (&Driver{}).Capabilities("S3400-48T4SP")

	var seen = map[string]bool{}
	var v string
	for _, v = range caps.SNMPVersions {
		seen[v] = true
	}
	if !seen["v3"] {
		t.Error("SNMPVersions does not carry v3 — the group and user forms were both " +
			"written and read back on hardware 2026-08-12")
	}
	if !seen["v2c"] {
		t.Error("SNMPVersions dropped v2c: the community path is still the one most of " +
			"the fleet is polled with")
	}

	if len(caps.SNMPv3Auth) != 2 || caps.SNMPv3Auth[0] != "md5" || caps.SNMPv3Auth[1] != "sha" {
		t.Errorf("SNMPv3Auth = %v — \"md5\" and \"sha\" are the two algorithms the user "+
			"line was seen to accept; \"sha256\" and \"sha-256\" were both answered "+
			"\"Unknown command\" on Version 2.0.2J", caps.SNMPv3Auth)
	}
	if len(caps.SNMPv3Priv) != 0 {
		t.Errorf("SNMPv3Priv = %v and it MUST be empty: four privacy user forms were sent "+
			"whole to the lab unit and all four were answered \"Unknown command\". A "+
			"non-empty list here would offer encryption the firmware cannot express, and "+
			"the connector would have to discard the passphrase silently", caps.SNMPv3Priv)
	}
}

// TestSNMPv3PlanIsAuthNoPrivAndNonConformant is the capability read END TO END,
// because the declaration only matters through what `snmpprov` makes of it.
func TestSNMPv3PlanIsAuthNoPrivAndNonConformant(t *testing.T) {
	var plan = snmpprov.For((&Driver{}).Capabilities("S3400-48T4SP"), snmpprov.DefaultPolicy())

	if plan.Version != "v3" {
		t.Fatalf("plan version = %q, want v3 — the declaration is what puts this range "+
			"on a v3 plan at all (reason: %s)", plan.Version, plan.Reason)
	}
	if plan.Auth != "sha" || plan.Priv != "" {
		t.Errorf("plan pair = %s/%q, want sha/\"\" — SHA-1 is the strongest this firmware "+
			"takes and there is no encryption to negotiate", plan.Auth, plan.Priv)
	}
	if plan.Conform {
		t.Error("the plan is marked CONFORMANT: SHA-1 without encryption cannot meet a " +
			"SHA-256/AES-128 floor, and presenting it as conformant is the exact lie the " +
			"algorithm declaration exists to prevent")
	}
	var choice = snmpv3.Choice{Auth: plan.Auth, Priv: plan.Priv}
	if choice.Tag() != "snmpv3-sha-nopriv" {
		t.Errorf("tag = %q, want snmpv3-sha-nopriv: the missing encryption must be "+
			"EXPLICIT in the label that carries the secret", choice.Tag())
	}
	if !strings.Contains(plan.Reason, "no encryption") {
		t.Errorf("plan reason %q does not say the identity encrypts nothing", plan.Reason)
	}
}

// TestSNMPv3PrivIsRefusedNotDowngraded is the substance of the 2026-08-12
// decision: a caller asking for privacy on this range gets an ERROR, never an
// authNoPriv identity reported as posed.
func TestSNMPv3PrivIsRefusedNotDowngraded(t *testing.T) {
	var cases = []struct {
		name string
		want SNMPIdentity
	}{
		{"algorithm named", SNMPIdentity{
			User: "u", AuthAlg: "sha", AuthSecret: "a", PrivAlg: "des", PrivSecret: "b"}},
		{"aes asked for", SNMPIdentity{
			User: "u", AuthAlg: "sha", AuthSecret: "a", PrivAlg: "aes128", PrivSecret: "b"}},
		// The dangerous one: no algorithm named, but a passphrase handed over. A
		// connector that only looked at PrivAlg would take the secret and drop it.
		{"passphrase with no algorithm", SNMPIdentity{
			User: "u", AuthAlg: "sha", AuthSecret: "a", PrivSecret: "b"}},
	}
	var tc struct {
		name string
		want SNMPIdentity
	}
	for _, tc = range cases {
		var err = refuseSNMPv3Priv(tc.want)
		if err == nil {
			t.Errorf("%s: privacy was ACCEPTED. Nothing on this firmware can express it, "+
				"so the identity would have been written authNoPriv with the encryption "+
				"passphrase discarded — and no re-read could ever see that", tc.name)
			continue
		}
		if !errors.Is(err, driver.ErrNotImplemented) {
			t.Errorf("%s: refusal does not attach to driver.ErrNotImplemented (%v) — the "+
				"API layer sorts platform refusals from write failures on that", tc.name, err)
		}
		if !strings.Contains(err.Error(), "Unknown command") {
			t.Errorf("%s: the refusal does not quote the DEVICE's own answer: %v",
				tc.name, err)
		}
	}

	// And the negative: authNoPriv must pass this gate untouched.
	var ok = refuseSNMPv3Priv(SNMPIdentity{User: "u", AuthAlg: "sha", AuthSecret: "a"})
	if ok != nil {
		t.Errorf("an authNoPriv identity was refused by the privacy gate: %v", ok)
	}
}

// TestSNMPv3AuthKeywordRefusesByName: an algorithm above SHA-1 is REFUSED, never
// rounded down. Setting SHA-1 where SHA-256 was asked for gives working
// monitoring and false compliance.
func TestSNMPv3AuthKeywordRefusesByName(t *testing.T) {
	var kw, err = snmpV3AuthKeyword("SHA-1")
	if err != nil || kw != "sha" {
		t.Errorf("\"SHA-1\" = %q, %v — it is the same thing as \"sha\"", kw, err)
	}
	if kw, err = snmpV3AuthKeyword("MD5"); err != nil || kw != "md5" {
		t.Errorf("\"MD5\" = %q, %v", kw, err)
	}
	var alg string
	for _, alg = range []string{"sha256", "SHA-256", "sha512", "kangaroo"} {
		if kw, err = snmpV3AuthKeyword(alg); err == nil {
			t.Errorf("%q was accepted as %q — this firmware answers \"Unknown command\" "+
				"to everything above SHA-1", alg, kw)
		}
	}
	if _, err = snmpV3AuthKeyword(""); err == nil {
		t.Error("an empty algorithm was accepted: there is no \"v3 noauth\" user form here")
	}
}

// TestParseSNMPUsersFromCapture replays the ladder's "show snmp user".
//
// THE ORPHAN IS THE POINT. mcorph names mcnogroup, which was never created, and
// this firmware prints it with NO annotation where EOS prints "(not configured)".
// That refutes the 2026-08-11 note claiming the group field made the EOS failure
// class catchable from this read, and it is why the group is gated on "show snmp
// group" instead.
func TestParseSNMPUsersFromCapture(t *testing.T) {
	var users = parseSNMPUsers(readCapture(t, "show_snmp_user_authnopriv.txt"))
	if len(users) != 4 {
		t.Fatalf("parsed %d users from a capture carrying four: %+v", len(users), users)
	}

	var want = []snmpUser{
		{Name: "mcau1", EngineID: "8000cda203649d99227b95", Auth: "SHA", Group: "mcagA"},
		{Name: "mcau2", EngineID: "8000cda203649d99227b95", Auth: "MD5", Group: "mcagA"},
		{Name: "mcau3", EngineID: "8000cda203649d99227b95", Auth: "SHA", Group: "mcagA"},
		{Name: "mcorph", EngineID: "8000cda203649d99227b95", Auth: "SHA", Group: "mcnogroup"},
	}
	var i int
	for i = range want {
		if users[i] != want[i] {
			t.Errorf("user %d = %+v, want %+v", i, users[i], want[i])
		}
	}

	// mcau2 is the MD5 one and mcau1 the SHA one: the connector compares this
	// field against what it wrote, so mixing them up would confirm the wrong
	// algorithm.
	var md5u, ok = findSNMPUser(users, "mcau2")
	if !ok || normalizeAlg(md5u.Auth) != "md5" {
		t.Errorf("mcau2 = %+v — \"auth md5 <P>\" is the form it was written with", md5u)
	}

	// A user naming a group that does not exist is indistinguishable HERE from a
	// well-placed one. Pinning that keeps the group gate from being "simplified"
	// away later.
	var orphan, _ = findSNMPUser(users, "mcorph")
	if strings.Contains(strings.ToLower(orphan.Group), "not configured") {
		t.Error("\"show snmp user\" DOES annotate a missing group on this platform — the " +
			"hard group gate could then be relaxed, and this test is where to say so")
	}
}

// TestParseSNMPGroupsFromCapture replays "show snmp group" on the two forms the
// ladder created.
func TestParseSNMPGroupsFromCapture(t *testing.T) {
	var groups = parseSNMPGroups(readCapture(t, "show_snmp_group_auth.txt"))
	if len(groups) != 2 {
		t.Fatalf("parsed %d groups from a capture carrying two: %+v", len(groups), groups)
	}
	var auth, ok = findSNMPGroup(groups, "mcagA")
	if !ok {
		t.Fatalf("mcagA absent: %+v", groups)
	}
	if auth.SecurityModel != v3GroupSecurity {
		t.Errorf("security model = %q, want %q — \"snmp-server group <g> v3 auth\" is "+
			"the line that was typed, and the whole authNoPriv decision rests on this "+
			"form being accepted", auth.SecurityModel, v3GroupSecurity)
	}
	if auth.Read != "default" {
		t.Errorf("read view = %q, want \"default\" — the group is written with an EMPTY "+
			"tail and the device fills this itself", auth.Read)
	}
	// The "noauth" group is parsed too, and it is exactly what the connector must
	// REFUSE to accept as its own: an unauthenticated group is not an identity.
	var noauth, _ = findSNMPGroup(groups, "mcagN")
	if noauth.SecurityModel != "v3 noauth" {
		t.Errorf("mcagN security model = %q, want \"v3 noauth\"", noauth.SecurityModel)
	}
	if noauth.SecurityModel == v3GroupSecurity {
		t.Error("a \"v3 noauth\" group reads back identical to a \"v3 auth\" one: the " +
			"group gate could not tell them apart")
	}
}

// TestParseSNMPv3UserLineFromRunningConfig pins the ONE token this firmware adds
// that was not typed — the encryption type "0" between the algorithm and the
// passphrase — and the fact that the passphrase comes back IN CLEAR, which is
// what lets this connector verify a rotation instead of confessing it cannot.
func TestParseSNMPv3UserLineFromRunningConfig(t *testing.T) {
	var cfg = readCapture(t, "show_running_config_snmpv3_authnopriv.txt")

	var group, alg, phrase, ok = findSNMPv3UserInConfig(cfg, "mcau1")
	if !ok {
		t.Fatal("the plain \"v3 auth sha\" line was not found in the device's own rendering")
	}
	if group != "mcagA" || alg != "sha" || phrase != "REDACTED-PASSPHRASE" {
		t.Errorf("mcau1 → group %q alg %q phrase %q", group, alg, phrase)
	}

	// The "encrypted" variant must parse too: a device may carry a user somebody
	// else wrote that way, and reading it as absent would make this connector
	// rewrite an identity it had actually found.
	if group, alg, phrase, ok = findSNMPv3UserInConfig(cfg, "mcau3"); !ok {
		t.Fatal("the \"v3 encrypted auth sha\" line was not parsed")
	}
	if alg != "sha" || phrase != "REDACTED-PASSPHRASE" {
		t.Errorf("mcau3 → alg %q phrase %q — \"encrypted\" is a keyword, not a value",
			alg, phrase)
	}
	if group, alg, _, ok = findSNMPv3UserInConfig(cfg, "mcau2"); !ok || alg != "md5" {
		t.Errorf("mcau2 → alg %q (ok=%v), want md5", alg, ok)
	}

	// The community must still be readable beside four v3 users: a v3 identity in
	// the file must not shadow the v2c read ApplySNMP's idempotence rests on.
	var rc = ParseRunningConfig(cfg)
	if len(rc.Communities) != 1 || rc.Communities[0] != "REDACTED-COMMUNITY" {
		t.Errorf("communities = %v with four v3 users in the same configuration",
			rc.Communities)
	}
	if _, _, _, ok = findSNMPv3UserInConfig(cfg, "nosuchuser"); ok {
		t.Error("an absent user was reported found")
	}
}

// TestSNMPv3CommandFormsAreTheCapturedOnes pins the two lines that go out. They
// are compared against the device's own rendering, minus the "0" it inserts.
func TestSNMPv3CommandFormsAreTheCapturedOnes(t *testing.T) {
	var got = snmpV3GroupCmd("mcagA")
	if got != "snmp-server group mcagA v3 auth" {
		t.Errorf("group line = %q", got)
	}
	var line, shown = snmpV3UserCmd("mcau1", "mcagA", "sha", "s3cret")
	if line != "snmp-server user mcau1 mcagA v3 auth sha s3cret" {
		t.Errorf("user line = %q", line)
	}
	if strings.Contains(shown, "s3cret") {
		t.Errorf("the transcript twin carries the passphrase: %q", shown)
	}
}

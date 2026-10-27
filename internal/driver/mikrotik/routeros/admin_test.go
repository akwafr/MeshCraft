package routeros

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// TestAccountsReadFromCapture: real account state on the reference device.
func TestAccountsReadFromCapture(t *testing.T) {
	var rows = fixture(t, "user.json")
	if len(rows) != 1 {
		t.Fatalf("%d accounts in capture, 1 expected", len(rows))
	}
	if rows[0].Get("name") != "admin" {
		t.Errorf("account = %q, expected 'admin'", rows[0].Get("name"))
	}
	if rows[0].Get("group") != "full" {
		t.Errorf("group = %q, expected 'full'", rows[0].Get("group"))
	}
	// The factory account declares itself as such: that is what lets us recognize it
	// without assuming from its name.
	if rows[0].Get("comment") != "system default user" {
		t.Errorf("comment = %q, expected 'system default user'",
			rows[0].Get("comment"))
	}
}

// TestContractAccountCarriesTheGroupAndNoNumber — what the widened contract
// gets out of THIS range's single read.
//
// RouterOS ranks nothing from 0 to 15, so the number must come back UNKNOWN and
// not 0: the audit reading a fleet-wide list has to be able to tell "this
// platform does not count" from "this account has level zero". What it gets
// instead is the group — the thing that actually decides on RouterOS — and a
// verdict derived from it.
func TestContractAccountCarriesTheGroupAndNoNumber(t *testing.T) {
	var rows = fixture(t, "user.json")
	var got = contractAccount(Account{
		Name:  rows[0].Get("name"),
		Group: rows[0].Get("group"),
	})
	if got.Name != "admin" || got.Role != "full" {
		t.Errorf("account = %+v, want the captured admin/full", got)
	}
	if got.Privilege != driver.PrivilegeUnknown {
		t.Errorf("privilege = %d — RouterOS states NO number, and reporting 0 would "+
			"claim a level the platform cannot express", got.Privilege)
	}
	if got.Authority != driver.AuthorityFull {
		t.Errorf("authority = %v — the `full` group grants `policy` (captured in "+
			"user_group.json): it IS a full administrator, and this is the answer the "+
			"2026-08-10 audit could not get through the old []string", got.Authority)
	}
}

// TestGroupAuthorityRanksOnlyWhatItKnows.
//
// The three built-in groups are ranked from their CAPTURED policies. Anything
// else is an operator-defined group whose policy lives in `/user/group` — a
// SECOND request the enumeration does not make — so it stays UNKNOWN. Ranking
// it "limited" on the strength of its name is the failure that matters: a group
// called `readonly` that carries `policy` is exactly the unmanaged administrator
// the audit is looking for.
func TestGroupAuthorityRanksOnlyWhatItKnows(t *testing.T) {
	// Every group in the capture must be ranked, and ranked the way its policy
	// list says: `full` carries `policy`, the other two deny it.
	var r Row
	for _, r = range fixture(t, "user_group.json") {
		var policies = SplitList(r.Get("policy"))
		var want = driver.AuthorityLimited
		if GroupAllows(policies, "policy") {
			want = driver.AuthorityFull
		}
		if groupAuthority(r.Get("name")) != want {
			t.Errorf("group %q ranked %v, its captured policy says %v",
				r.Get("name"), groupAuthority(r.Get("name")), want)
		}
	}
	if groupAuthority("noc-readonly") != driver.AuthorityUnknown {
		t.Error("an operator-defined group must rank UNKNOWN: its policy is in " +
			"/user/group, which this enumeration does not read, and a group named " +
			"`readonly` may well carry `policy`")
	}
	if groupAuthority("") != driver.AuthorityUnknown {
		t.Error("a record without a group says nothing about rights")
	}
}

// TestDeniedPolicyIsNotGranted — the RouterOS policy trap.
//
// A group policy is a list where an entry can be DENIED by a `!` prefix. Observed:
// the `read` group carries `!write` and `!policy`. A naive substring search for
// 'write' in `read`'s policy would find it in `!write`, and conclude exactly the
// OPPOSITE of the truth.
//
// That is the kind of error that places an account unable to write, and one discovers
// it on the first apply — on a device from which the factory account may have just
// been removed.
func TestDeniedPolicyIsNotGranted(t *testing.T) {
	var groups = map[string][]string{}
	var r Row
	for _, r = range fixture(t, "user_group.json") {
		groups[r.Get("name")] = SplitList(r.Get("policy"))
	}

	if len(groups) != 3 {
		t.Fatalf("%d groups in capture, 3 expected (read, write, full)", len(groups))
	}

	// The `read` group DENIES `write` and `policy`.
	if GroupAllows(groups["read"], "write") {
		t.Error("the `read` group carries `!write`: it does NOT grant write. A substring " +
			"search would have concluded the opposite")
	}
	if GroupAllows(groups["read"], "policy") {
		t.Error("the `read` group carries `!policy`: it does NOT grant account " +
			"management")
	}
	// But it does grant read and REST access.
	if !GroupAllows(groups["read"], "read") || !GroupAllows(groups["read"], "rest-api") {
		t.Error("the `read` group grants `read` and `rest-api` (observed)")
	}

	// The `write` group writes but does not manage accounts: that is THE reason
	// meshcraft places its account in `full`.
	if !GroupAllows(groups["write"], "write") {
		t.Error("the `write` group grants write")
	}
	if GroupAllows(groups["write"], "policy") {
		t.Error("the `write` group carries `!policy`: a meshcraft account placed there " +
			"could NEITHER create the next account NOR remove `admin` — that is why " +
			"adminGroup is 'full'")
	}

	// The `full` group grants everything meshcraft needs.
	var needed string
	for _, needed = range append(append([]string{}, requiredPolicies...), "policy") {
		if !GroupAllows(groups["full"], needed) {
			t.Errorf("the `full` group must grant `%s` (observed)", needed)
		}
	}
}

// TestAllThreeFactoryGroupsCarryRestAPI — without which meshcraft cuts its own path.
//
// That is the nastiest scenario on this platform: placement succeeds, the device
// confirms the account, and the next read answers 403. EnsureAccount's verification
// exists for that.
func TestAllThreeFactoryGroupsCarryRestAPI(t *testing.T) {
	var r Row
	for _, r = range fixture(t, "user_group.json") {
		if !GroupAllows(SplitList(r.Get("policy")), "rest-api") {
			t.Errorf("factory group %q does not carry `rest-api`: placing the meshcraft "+
				"account there would close the door behind it", r.Get("name"))
		}
	}
}

// TestPasswordPolicyReadFromDevice
//
// Observed: `{"minimum-password-length":"0","minimum-categories":"0"}`. RouterOS
// therefore imposes NOTHING from the factory — it would even accept an empty password.
//
// But these bounds are a SITE setting, not a hardware property. An operator who raised
// the minimum to 12 will refuse a secret meshcraft considers good. That is why the
// read exists: assuming 0 still holds exposes one to a provisioning failure whose
// cause would be invisible.
func TestPasswordPolicyReadFromDevice(t *testing.T) {
	var row = fixtureOne(t, "user_settings.json")
	var p PasswordPolicy
	p.MinLength, _ = row.Int("minimum-password-length")
	p.MinCategories, _ = row.Int("minimum-categories")

	if p.MinLength != 0 || p.MinCategories != 0 {
		t.Errorf("observed policy = %+v, expected no constraint (0/0)", p)
	}
	// With no constraint, anything passes.
	var ok, _ = p.Accepts("short")
	if !ok {
		t.Error("with no observed constraint, any secret must pass")
	}
}

// TestSecretTooShortRefusedBeforeWrite
//
// On a platform that persists immediately, discovering refusal AFTER sending an `add`
// would leave a half-placed account. Verification therefore happens before, with a
// message that states the exact number the device requires.
func TestSecretTooShortRefusedBeforeWrite(t *testing.T) {
	var p = PasswordPolicy{MinLength: 12}
	var ok, reason = p.Accepts("short")
	if ok {
		t.Fatal("a 5-character secret must be refused when the device requires 12")
	}
	if !strings.Contains(reason, "12") || !strings.Contains(reason, "5") {
		t.Errorf("the reason must cite both lengths to be actionable: %q", reason)
	}
	// And a conforming secret passes.
	ok, _ = p.Accepts("LongEnoughToPass")
	if !ok {
		t.Error("a secret of sufficient length must pass")
	}
}

// TestFourClassRequirementStatedExplicitly
//
// meshcraft generates only alphanumeric, so at most THREE character classes. A
// requirement of four is a GUARANTEED refusal, and the message must say so: without
// that, the operator would believe it is a length problem and lengthen a secret that
// will be refused the same way.
func TestFourClassRequirementStatedExplicitly(t *testing.T) {
	var p = PasswordPolicy{MinCategories: 4}
	var ok, reason = p.Accepts("Password2026") // 3 classes: lower, upper, digits
	if ok {
		t.Fatal("an alphanumeric secret covers only 3 classes: it must be refused when " +
			"the device requires 4")
	}
	if !strings.Contains(reason, "alphanumeric") {
		t.Errorf("the reason must explain that meshcraft generates only alphanumeric, "+
			"otherwise the operator will lengthen a secret for nothing: %q", reason)
	}

	// Three classes required: an alphanumeric secret passes.
	p = PasswordPolicy{MinCategories: 3}
	ok, reason = p.Accepts("Password2026")
	if !ok {
		t.Errorf("three classes must be satisfied by 'Password2026': %q", reason)
	}
}

// TestPunctuationIsAccepted: observed in the best possible way.
//
// The password that authenticated the ENTIRE observation campaign contains an
// at-sign. Punctuation passes on this platform, that is not deduction — and that is
// what AdminAccount.AlphanumericOnly declares.
func TestPunctuationIsAccepted(t *testing.T) {
	var caps = (&Driver{}).Capabilities(referenceModel)
	if caps.Admin.AlphanumericOnly {
		t.Error("RouterOS accepts punctuation in a password: the secret used for all " +
			"observations of this connector contains an at-sign")
	}
	// And a named account is possible: RouterOS is a real multi-account system.
	if !caps.Admin.Custom {
		t.Error("RouterOS can create named accounts (observed: /user is a list with a " +
			"`name` field, and /user/group offers three groups)")
	}
	if caps.Admin.FixedName != "admin" {
		t.Errorf("the factory account is 'admin' (observed), got %q",
			caps.Admin.FixedName)
	}
	// The name actually placeable is the one meshcraft requests.
	if caps.Admin.EffectiveUsername("mcadmin") != "mcadmin" {
		t.Error("a custom name must be kept as-is on this platform")
	}
}

// TestMeshcraftPlacesAccountInFull: the direct consequence of observing policies.
func TestMeshcraftPlacesAccountInFull(t *testing.T) {
	if adminGroup != "full" {
		t.Errorf("adminGroup = %q. Only `full` carries the `policy` policy (observed): "+
			"an account placed elsewhere could not rotate its own secret, nor remove the "+
			"factory account", adminGroup)
	}
	// `rest-api` must head the required policies: it is the only one whose absence
	// is INVISIBLE at placement time.
	if requiredPolicies[0] != "rest-api" {
		t.Errorf("requiredPolicies starts with %q, expected 'rest-api' — it is the "+
			"only policy whose absence shows only on the NEXT request",
			requiredPolicies[0])
	}
}

// TestRefusingToDeleteSelf: deleting oneself would leave the session without an owner.
func TestRefusingToDeleteSelf(t *testing.T) {
	var c = &Client{User: "mcadmin"}
	var err = c.DeleteAccount(nil, "mcadmin")
	if err == nil {
		t.Error("deleting the current session account must be refused")
	}
	// Case must not serve as a bypass.
	err = c.DeleteAccount(nil, "McAdmin")
	if err == nil {
		t.Error("name comparison must ignore case")
	}
}

// TestAdminNotesMentionGroupTrap
//
// The capability is useless if what it cost to establish is not transmitted. The trap
// on this platform is not the password, it is the GROUP — and the notes must say so,
// because that is where an operator will read them.
func TestAdminNotesMentionGroupTrap(t *testing.T) {
	var notes = strings.Join(
		(&Driver{}).Capabilities(referenceModel).Admin.Notes, " ")

	var expected string
	for _, expected = range []string{"rest-api", "policy", "write", "serial"} {
		if !strings.Contains(notes, expected) {
			t.Errorf("admin account notes must mention %q: %s", expected, notes)
		}
	}
}

// TestMgmtNotesMentionCPU
//
// This is the most dangerous capability to assume, and the fact that makes it dangerous
// is the pseudo-port `bridge`: forgetting it closes administration without stopping
// traffic. If that fact is not in the notes, it is nowhere for the operator.
func TestMgmtNotesMentionCPU(t *testing.T) {
	var mgmt = (&Driver{}).Capabilities(referenceModel).Mgmt
	var notes = strings.Join(mgmt.Notes, " ")

	var expected string
	for _, expected = range []string{
		"bridge", "CPU", "vlan-filtering", "MAC-winbox", "ORDER",
	} {
		if !strings.Contains(notes, expected) {
			t.Errorf("management notes must mention %q: %s", expected, notes)
		}
	}

	// Tagged management is OBSERVED, not derived: all reads of this connector used a
	// tagged VLAN.
	if !mgmt.TaggedOK {
		t.Error("management on a tagged VLAN is observed on the reference device " +
			"(192.0.2.54 on vlan99, VLAN 99 tagged)")
	}
	if mgmt.VlanIDs != driver.VlanAny {
		t.Errorf("VlanIDs = %q, expected 'any'", mgmt.VlanIDs)
	}
	// RouterOS has NO DHCP fallback address: the observed 192.168.88.1 is the FACTORY
	// address, not a fallback. Confusing them would send one looking for a device at an
	// address it will never have taken.
	if mgmt.DHCPFallback != "" {
		t.Errorf("DHCPFallback = %q: RouterOS has no fallback address when DHCP does "+
			"not respond. The observed 192.168.88.1 is the factory address",
			mgmt.DHCPFallback)
	}
}

// TestCommitStyleIsImmediate: RouterOS applies AND persists in one step.
//
// That is the worst case of the contract — no safety net provided by the platform —
// and declaring otherwise would suggest a rollback that does not exist. The difference
// from Instant On is structural: there a failed apply cancels on power loss, which
// makes the operation testable; here it is already in non-volatile memory when the
// device responds.
func TestCommitStyleIsImmediate(t *testing.T) {
	var caps = (&Driver{}).Capabilities(referenceModel)
	if caps.Commit != driver.CommitImmediate {
		t.Errorf("Commit = %q, expected 'immediate': RouterOS has neither candidate "+
			"configuration, nor a save step, nor native commit-confirmed",
			caps.Commit)
	}
	// The undo journal is READABLE (observed /system/history with `undoable` and a
	// plain `undo` command), and declaring it keeps one from believing there is
	// nothing at all. Triggering it from REST is another matter — see the delivery
	// report.
	if !caps.Has("ConfigUndoJournal") {
		t.Error("RouterOS's undo journal is observed readable: /system/history carries " +
			"`undoable: true` and the inverse command in plain text")
	}
	// 20 sessions observed on `/ip/service`, versus 5 on an Instant On.
	if caps.MaxSessions != 20 {
		t.Errorf("MaxSessions = %d, expected 20 (observed `max-sessions: 20` on the "+
			"`www` entry of /ip/service)", caps.MaxSessions)
	}
}

// TestSNMPv3NotAdvertisedUntilWriteIsProven
//
// The v3 machinery EXISTS on this firmware, and the observation is clear: `/snmp`
// carries an `engine-id` (meaningful only for v3), and `/snmp/community` carries
// `security`, `authentication-protocol` and `encryption-protocol`.
//
// It is nevertheless not declared, for two cumulative reasons: no write could be
// exercised, and the algorithms actually accepted could not be established (the
// observed MD5/DES values are field defaults, and this API's enumeration validation
// answers nothing on read).
//
// This test locks down BOTH halves of the arbitration, because erring in either
// direction costs: advertising v3 would generate a secret placement might refuse;
// advertising SNMPv3Auth: ["MD5"] and SNMPv3Priv: ["DES"] on field defaults would
// make policy choose the WEAKEST algorithm pair through our own under-declaration.
// An advertised capability that is too low is not always on the safe side.
func TestSNMPv3NotAdvertisedUntilWriteIsProven(t *testing.T) {
	var caps = (&Driver{}).Capabilities(referenceModel)

	var v string
	for _, v = range caps.SNMPVersions {
		if v == "v3" {
			t.Error("v3 must not be advertised until a write has been proven: provisioning " +
				"would generate a label and secret placement might refuse, and an identifier " +
				"that exists in the database and nowhere else is worse than a missing one")
		}
	}
	// v1 and v2c are observed usable: SNMP is enabled and a read community exists.
	if len(caps.SNMPVersions) != 2 ||
		caps.SNMPVersions[0] != "v1" || caps.SNMPVersions[1] != "v2c" {
		t.Errorf("SNMPVersions = %v, expected [v1 v2c]", caps.SNMPVersions)
	}
	// And above all: no algorithm advertised on the faith of a field default.
	if len(caps.SNMPv3Auth) != 0 || len(caps.SNMPv3Priv) != 0 {
		t.Errorf("SNMPv3Auth=%v SNMPv3Priv=%v: the observed MD5/DES values are field "+
			"DEFAULTS, not the list of what it accepts. Declaring them would make policy "+
			"choose the weakest pair in the fleet",
			caps.SNMPv3Auth, caps.SNMPv3Priv)
	}

	// The v3 machinery is there, in the capture: that is what makes this declaration
	// REVISABLE after the write campaign.
	var snmp = fixtureOne(t, "snmp.json")
	if snmp.Get("engine-id") == "" {
		t.Error("the capture must carry an engine-id: that is proof the v3 machinery " +
			"exists on this firmware, and therefore that this declaration is provisional " +
			"and not final")
	}
	var community = fixture(t, "snmp_community.json")[0]
	var field string
	for _, field = range []string{
		"security", "authentication-protocol", "encryption-protocol",
	} {
		var _, present = community[field]
		if !present {
			t.Errorf("the /snmp/community capture must carry field %q", field)
		}
	}
}

// TestResetProcedureNotPresentedAsVerified
//
// The button PARAMETERS are observed on the device itself (`/system/routerboard/settings`),
// which is reassuring. But no reset was EXECUTED: presenting the procedure as verified
// would be exactly the confusion the `Confirmed` field exists to prevent.
func TestResetProcedureNotPresentedAsVerified(t *testing.T) {
	var reset = (&Driver{}).ResetProcedure(referenceModel)

	if reset.Confirmed {
		t.Error("no reset was executed on this hardware: parameters are observed, the " +
			"sequence is documentary")
	}
	// The source must distinguish the two, otherwise `Confirmed: false` is a mystery.
	if !strings.Contains(reset.Source, "was NOT executed") {
		t.Errorf("the source must say what is observed and what is not: %q", reset.Source)
	}
	// Observed values from the device must appear there: that is what makes the
	// procedure verifiable by someone else.
	var expected string
	for _, expected = range []string{"reformat-hold-button", "enable-jumper-reset"} {
		if !strings.Contains(reset.Source, expected) {
			t.Errorf("the source must cite the observation %q: %q", expected, reset.Source)
		}
	}
	// And this platform's own fallbacks, which change the gravity of a mistake.
	var pitfalls = strings.Join(reset.Pitfalls, " ")
	for _, expected = range []string{"MAC-winbox", "protected-routerboot"} {
		if !strings.Contains(pitfalls, expected) {
			t.Errorf("pitfalls must mention %q: %s", expected, pitfalls)
		}
	}
}

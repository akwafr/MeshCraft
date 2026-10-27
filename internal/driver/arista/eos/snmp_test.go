package eos

// SNMP on EOS — the parsers of "show snmp user" and "show snmp group", the v3
// write they made possible, and the traps that write exists to avoid.
//
// # WHY THIS FILE APPEARS AT THE SAME TIME AS THE FEATURE
//
// There was no test for ApplySNMP on this connector at all before 2026-08-10:
// the v2c half was written and never exercised, and the v3 half was a refusal, so
// there was nothing to assert but the refusal's wording. Both halves are covered
// here now, and the v2c ones are a genuine regression net rather than ceremony —
// lifting the refusal restructured the function around them.
//
// EVERYTHING HERE RUNS AGAINST A FAKE CLI, and that limit is the same one
// portadmin_test.go states about itself: the capture in testdata/ was taken by
// typing at a device, these tests drive the applier against a fixture, and neither
// is evidence for the other. What is NOT proven here is the WRITE forms; snmp.go's
// header says line by line which of them a capture has since confirmed — the group
// line has one, the user line and the re-issue behaviour do not. What these tests
// do establish is that if a form is wrong, this connector FAILS LOUDLY instead of
// reporting an identity it did not pose.
//
// THE SECOND CAPTURE, testdata/show_snmp_group.txt, is what turned the applier's
// last Skipped line into a check: the group's read view is the platform default and
// that default is all included, so a group written with no view DOES authorise
// reads. The tests below pin that, pin the group that authorises NOTHING as a
// distinct and refused case, and pin what the connector does with a shape no
// capture covers — an explicitly NAMED read view.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- the capture -----------------------------------------------------------

// loadSNMPUserCapture reads the block "show snmp user" printed on core01.lab
// (DCS-7050SX-64-R, EOS 4.28.13.1M) on 2026-08-10, after a temporary user was
// created there and before it was removed again.
//
// THE FIELD CONTENT IS THE CAPTURE, verbatim, including the "(not configured)"
// suffix this whole feature turns on. The CR of the CRLF is RESTORED rather than
// captured — the record of the session carried the six lines as text — and it is
// restored on the same ground the STP fixtures of the same day rest on
// (loadSTPCapture, stp_live_test.go): this switch terminates its lines with CRLF,
// nothing in ssh.go strips the CR, and an LF-only fixture would let the parser
// look proven against line endings the wire never delivers. The guard below keeps
// that half honest, exactly as the STP loader does.
func loadSNMPUserCapture(t *testing.T) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", "show_snmp_user.txt"))
	if err != nil {
		t.Fatalf("read fixture show_snmp_user.txt: %v", err)
	}
	if !strings.Contains(string(b), "\r\n") {
		t.Fatalf("show_snmp_user.txt has lost its CRLF line endings — the half of these " +
			"tests that is about line endings is now vacuous")
	}
	return string(b)
}

// snmpUserRead renders a "show snmp user" answer BY EDITING THE CAPTURE: the
// value after the colon is replaced on the "User name" and "Group" lines and
// nothing else is touched, so the label spelling, the column padding and the line
// endings all remain the device's own. A helper that built the block from a format
// string would be testing the parser against the test's idea of EOS.
func snmpUserRead(t *testing.T, name, group string, configured bool) string {
	t.Helper()
	var lines = strings.Split(loadSNMPUserCapture(t), "\n")
	var i int
	for i = range lines {
		var idx = strings.Index(lines[i], ":")
		if idx < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(lines[i][:idx])) {
		case "user name":
			lines[i] = lines[i][:idx+2] + name + "\r"
		case "group":
			var v = group
			if !configured {
				v = group + " " + notConfiguredMark
			}
			lines[i] = lines[i][:idx+2] + v + "\r"
		}
	}
	return strings.Join(lines, "\n")
}

// loadSNMPGroupCapture reads the block "show snmp group" printed on
// core01.lab (DCS-7050SX-64-R, EOS 4.28.13.1M) on 2026-08-10, on the group the
// connector's own deduced "snmp-server group … v3 priv" had just created.
//
// Same standing as the user capture, and the same CRLF guard for the same reason:
// the field content is the capture verbatim — the four labels and the
// "<no … specified (default: all included)>" wording the whole check turns on —
// while the CR is RESTORED rather than captured, because the record of the session
// carried the five lines as text.
func loadSNMPGroupCapture(t *testing.T) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", "show_snmp_group.txt"))
	if err != nil {
		t.Fatalf("read fixture show_snmp_group.txt: %v", err)
	}
	if !strings.Contains(string(b), "\r\n") {
		t.Fatalf("show_snmp_group.txt has lost its CRLF line endings — the half of these " +
			"tests that is about line endings is now vacuous")
	}
	return string(b)
}

// snmpGroupRead renders a "show snmp group" answer BY EDITING THE CAPTURE, on
// the same ground as [snmpUserRead]: only the value after the colon changes, so
// the label spelling, the column padding and the line endings stay the device's.
//
// An EMPTY readView leaves the captured read-view line untouched — which is the
// whole point of the capture, and the state this connector writes.
func snmpGroupRead(t *testing.T, name, security, readView string) string {
	t.Helper()
	var lines = strings.Split(loadSNMPGroupCapture(t), "\n")
	var i int
	for i = range lines {
		var idx = strings.Index(lines[i], ":")
		if idx < 0 {
			continue
		}
		var v string
		switch strings.ToLower(strings.TrimSpace(lines[i][:idx])) {
		case "group name":
			v = name
		case "security model":
			v = security
		case "read view":
			if readView == "" {
				continue
			}
			v = readView
		default:
			continue
		}
		lines[i] = lines[i][:idx+2] + v + "\r"
	}
	return strings.Join(lines, "\n")
}

// THE CAPTURE, PARSED, FIELD BY FIELD. If EOS ever changes a label or a spelling,
// this is the test that says so — and it asserts the algorithm names in the
// device's own rendering ("SHA", "AES-128") rather than normalised, because
// the rendering is what a future survey compares against.
func TestParseSNMPUsersOnTheCapture(t *testing.T) {
	var users = parseSNMPUsers(loadSNMPUserCapture(t))
	if len(users) != 1 {
		t.Fatalf("the capture holds ONE user, parsed %d: %+v", len(users), users)
	}
	var u = users[0]
	if u.Name != "mcprobev3" {
		t.Errorf("name = %q, want \"mcprobev3\"", u.Name)
	}
	if u.SecurityModel != "v3" {
		t.Errorf("security model = %q, want \"v3\"", u.SecurityModel)
	}
	if u.EngineID != "f5717f28993a9db76500" {
		t.Errorf("engine ID = %q — a value lost here is the only thing about the "+
			"invisible keys that can be shown at all", u.EngineID)
	}
	if u.Auth != "SHA" {
		t.Errorf("authentication = %q, want \"SHA\" exactly as the device printed it", u.Auth)
	}
	if u.Priv != "AES-128" {
		t.Errorf("privacy = %q, want \"AES-128\" exactly as the device printed it", u.Priv)
	}
	if u.Group != "meshprobe" {
		t.Errorf("group = %q, want \"meshprobe\" with the marker stripped from the NAME", u.Group)
	}
}

// THE TRAP, PINNED AT THE PARSER. The captured user names a group that does not
// exist and EOS says so. A parser that read the group name and dropped the suffix
// would make a user authorised for nothing look identical to a working one.
func TestParseSNMPUsersReadsTheNotConfiguredMarker(t *testing.T) {
	var users = parseSNMPUsers(loadSNMPUserCapture(t))
	if len(users) != 1 {
		t.Fatalf("parsed %d users", len(users))
	}
	if users[0].GroupConfigured {
		t.Fatalf("\"Group : meshprobe %s\" was read as a configured group. EOS accepts a "+
			"user naming a group that does not exist; such a user authenticates and is "+
			"authorised for NOTHING, and reading past this marker is how a connector "+
			"reports supervision posed on an identity whose every walk comes back empty",
			notConfiguredMark)
	}
	// And the opposite direction, so the field cannot be nailed to false: the same
	// line without the marker is a group that IS configured.
	var ok = parseSNMPUsers(snmpUserRead(t, "mcpoll", "meshcraft-ro", true))
	if len(ok) != 1 || !ok[0].GroupConfigured || ok[0].Group != "meshcraft-ro" {
		t.Fatalf("a group line with no marker must read as configured: %+v", ok)
	}
}

// A group line carrying the marker AND NO NAME must not invent one.
func TestParseSNMPUserGroupMarkerWithoutAName(t *testing.T) {
	var name, configured = splitSNMPUserGroup(notConfiguredMark)
	if name != "" || configured {
		t.Errorf("group = %q configured=%v, want an empty name and false", name, configured)
	}
}

// No v3 user is a STATE, not a parse failure: it is what the command printed on
// core01 once the probe user was removed. (The exact bytes of that empty answer
// were not kept, so the empty string stands for it — an answer that carries no
// "User name" line at all reaches the same place whatever else is in it.)
func TestParseSNMPUsersEmptyIsNoUsers(t *testing.T) {
	var cases = []string{"", "\r\n", "sw-lab-1#\r\n"}
	var s string
	for _, s = range cases {
		if len(parseSNMPUsers(s)) != 0 {
			t.Errorf("%q parsed as a user", s)
		}
	}
}

// TWO users in one answer. The separator between blocks is NOT captured — only one
// user was ever on that switch — so the parser opens a record on every "User
// name" line and this test pins that rule with the three separators a device
// might plausibly use.
func TestParseSNMPUsersSplitsOnEveryUserNameLine(t *testing.T) {
	var one = snmpUserRead(t, "mcpoll", "meshcraft-ro", true)
	var two = snmpUserRead(t, "other", "othergroup", false)
	var seps = []string{"", "\r\n", "\r\n\r\n"}
	var sep string
	for _, sep = range seps {
		var users = parseSNMPUsers(one + sep + two)
		if len(users) != 2 {
			t.Fatalf("separator %q: parsed %d users, want 2", sep, len(users))
		}
		if users[0].Name != "mcpoll" || !users[0].GroupConfigured {
			t.Errorf("separator %q: first user = %+v", sep, users[0])
		}
		if users[1].Name != "other" || users[1].GroupConfigured {
			t.Errorf("separator %q: second user = %+v — the fields of the second block "+
				"must not leak into the first, nor the reverse", sep, users[1])
		}
	}
}

// --- "show snmp group", the capture that turned a note into a check ---------

// THE GROUP CAPTURE, PARSED, FIELD BY FIELD — including the raw text of all three
// view fields, because the wording IS the evidence: this platform's own statement
// that an unset read view defaults to all included is the entire reason the check
// can be a check.
func TestParseSNMPGroupsOnTheCapture(t *testing.T) {
	var groups = parseSNMPGroups(loadSNMPGroupCapture(t))
	if len(groups) != 1 {
		t.Fatalf("the capture holds ONE group, parsed %d: %+v", len(groups), groups)
	}
	var g = groups[0]
	if g.Name != "meshprobeb" {
		t.Errorf("group name = %q, want \"meshprobeb\"", g.Name)
	}
	if g.SecurityModel != "v3 priv" {
		t.Errorf("security model = %q — it is printed as MODEL AND LEVEL in one field, "+
			"\"v3 priv\", which is the same two words the group line was written with",
			g.SecurityModel)
	}
	if g.Read.Raw != "<no read view specified (default: all included)>" {
		t.Errorf("read view raw = %q — the wording is the capture and the check turns on "+
			"it", g.Read.Raw)
	}
	if g.Write.Raw != "<no write view specified>" {
		t.Errorf("write view raw = %q", g.Write.Raw)
	}
	if g.Notify.Raw != "<no notify view specified>" {
		t.Errorf("notify view raw = %q", g.Notify.Raw)
	}
	// No bracketed form is a view NAME. Reading one as a name would make a group
	// that authorises nothing indistinguishable from one that authorises everything.
	if g.Read.Name != "" || g.Write.Name != "" || g.Notify.Name != "" {
		t.Errorf("a \"<no … specified>\" form was read as a view NAME: %+v", g)
	}
}

// THE LINE THE WHOLE CHANGE IS FOR. The read view and the write view carry the
// SAME bracketed shape and mean OPPOSITE things — the read one is annotated with a
// default that includes everything, the write one is not. A parser that treated
// "unset" as one state would either authorise nothing or authorise everything,
// and both are wrong on one of these two fields.
func TestParseSNMPGroupViewAuthorityFromTheCapture(t *testing.T) {
	var groups = parseSNMPGroups(loadSNMPGroupCapture(t))
	if len(groups) != 1 {
		t.Fatalf("parsed %d groups", len(groups))
	}
	var g = groups[0]
	if g.Read.Authority != viewAuthorityAll {
		t.Errorf("read view authority = %v, want viewAuthorityAll: the device said in its "+
			"own words \"%s\", so a group written with no view DOES authorise reads",
			g.Read.Authority, defaultAllIncludedMark)
	}
	if g.Write.Authority != viewAuthorityNone {
		t.Errorf("write view authority = %v, want viewAuthorityNone — \"<no write view "+
			"specified>\" carries no default, and a read-only identity wants exactly that",
			g.Write.Authority)
	}
	if g.Notify.Authority != viewAuthorityNone {
		t.Errorf("notify view authority = %v, want viewAuthorityNone", g.Notify.Authority)
	}
}

// A GROUP THAT AUTHORISES NOTHING IS DISTINGUISHED FROM ONE THAT AUTHORISES READS,
// at the parser, on the one difference between them: the annotation.
//
// The second case is not captured on a READ view — it is the write view's captured
// wording moved one line up — and that is the point: the shape is the platform's
// own, only the field it appears in is constructed.
func TestParseSNMPGroupUnsetWithoutADefaultAuthorisesNothing(t *testing.T) {
	var cases = []struct {
		raw  string
		want viewAuthority
		why  string
	}{
		{"<no read view specified (default: all included)>", viewAuthorityAll,
			"CAPTURED: the default supplies everything"},
		{"<no read view specified>", viewAuthorityNone,
			"unset and NO default named: the group may read nothing"},
		{"<no read view specified (default: none)>", viewAuthorityNone,
			"an annotation that is not the captured one proves nothing permissive"},
		{"<something nobody has captured>", viewAuthorityNone,
			"an unknown bracketed wording must fail towards a refusal, never towards a " +
				"confirmation of authority nobody observed"},
	}
	var i int
	for i = range cases {
		var groups = parseSNMPGroups(snmpGroupRead(t, "meshcraft-ro", "v3 priv", cases[i].raw))
		if len(groups) != 1 {
			t.Fatalf("%q: parsed %d groups", cases[i].raw, len(groups))
		}
		if groups[0].Read.Authority != cases[i].want {
			t.Errorf("read view %q -> authority %v, want %v (%s)", cases[i].raw,
				groups[0].Read.Authority, cases[i].want, cases[i].why)
		}
	}

	// A field with NOTHING after the colon, straight at the classifier: it cannot go
	// through [snmpGroupRead], whose empty argument means "leave the capture alone".
	// Nobody has seen this shape, so it proves nothing and authorises nothing.
	if parseSNMPView("   ").Authority != viewAuthorityNone {
		t.Error("a view field with no value was read as authorising something")
	}
}

// AN EXPLICITLY NAMED VIEW IS ITS OWN ANSWER, and neither of the other two.
// Nothing here has ever seen this shape — this connector never writes a view name
// — so the parser reports the name and refuses to decide what it contains.
func TestParseSNMPGroupNamedViewIsNeitherYesNorNo(t *testing.T) {
	var groups = parseSNMPGroups(snmpGroupRead(t, "meshcraft-ro", "v3 priv", "ifmib-only"))
	if len(groups) != 1 {
		t.Fatalf("parsed %d groups", len(groups))
	}
	var v = groups[0].Read
	if v.Authority != viewAuthorityNamed {
		t.Fatalf("read view authority = %v, want viewAuthorityNamed", v.Authority)
	}
	if v.Name != "ifmib-only" {
		t.Errorf("view name = %q, want \"ifmib-only\" — the name is what makes the note "+
			"actionable, since \"show snmp view\" is one command away", v.Name)
	}
}

// No group listed is a STATE and not a parse failure, on the same terms as no user.
func TestParseSNMPGroupsEmptyIsNoGroups(t *testing.T) {
	var cases = []string{"", "\r\n", "sw-lab-1#\r\n",
		"% Invalid input detected at '^' marker.\r\n"}
	var s string
	for _, s = range cases {
		if len(parseSNMPGroups(s)) != 0 {
			t.Errorf("%q parsed as a group", s)
		}
	}
}

// TWO groups in one answer, three plausible separators. Same rule as the user
// parser and for the same reason: the separator was never captured, so a record
// opens on every "Group name" line.
func TestParseSNMPGroupsSplitsOnEveryGroupNameLine(t *testing.T) {
	var one = snmpGroupRead(t, "meshcraft-ro", "v3 priv", "")
	var two = snmpGroupRead(t, "legacy", "v2c", "<no read view specified>")
	var seps = []string{"", "\r\n", "\r\n\r\n"}
	var sep string
	for _, sep = range seps {
		var groups = parseSNMPGroups(one + sep + two)
		if len(groups) != 2 {
			t.Fatalf("separator %q: parsed %d groups, want 2", sep, len(groups))
		}
		if groups[0].Name != "meshcraft-ro" || groups[0].Read.Authority != viewAuthorityAll {
			t.Errorf("separator %q: first group = %+v", sep, groups[0])
		}
		if groups[1].Name != "legacy" || groups[1].Read.Authority != viewAuthorityNone {
			t.Errorf("separator %q: second group = %+v — the fields of one block must not "+
				"leak into the other", sep, groups[1])
		}
	}
}

// THE NAME IS NOT ENOUGH. The security model is a field of the block, so the same
// name plausibly appears once per model, and answering a question about a v3 group
// from a v2c block would confirm the read authority of an access entry the v3 user
// never goes through.
func TestFindSNMPGroupMatchesNameAndModelTogether(t *testing.T) {
	var v2c = snmpGroupRead(t, "meshcraft-ro", "v2c", "<no read view specified>")
	var v3 = snmpGroupRead(t, "meshcraft-ro", "v3 priv", "")
	var groups = parseSNMPGroups(v2c + v3)

	var got, ok = findSNMPGroup(groups, "meshcraft-ro", "v3 priv")
	if !ok || got.SecurityModel != "v3 priv" || got.Read.Authority != viewAuthorityAll {
		t.Fatalf("the v3 block was not the one selected: %+v ok=%v", got, ok)
	}
	// The level is part of the identity too: "v3" alone is not "v3 priv".
	if _, ok = findSNMPGroup(groups, "meshcraft-ro", "v3 noauth"); ok {
		t.Error("a group at another security LEVEL was accepted for \"v3 priv\"")
	}
	// The name is compared exactly — a user is bound to the spelling it names.
	if _, ok = findSNMPGroup(groups, "MESHCRAFT-RO", "v3 priv"); ok {
		t.Error("the group name was matched case-insensitively")
	}
	// The model's internal padding is a rendering, not an identifier.
	if _, ok = findSNMPGroup(groups, "meshcraft-ro", "V3   PRIV"); !ok {
		t.Error("the security model must compare with its padding collapsed and its case " +
			"ignored: it is two keywords, not an identifier")
	}
}

// --- the fake CLI ----------------------------------------------------------

// snmpFakeClient wires a Client to [portAdminFakeCLI] (portadmin_test.go), reused
// rather than copied for the reason dhcpsnoop_test.go gives: a second fake is a
// second thing to keep in step with [cliConn.run].
//
// `runCfgs` are the successive answers to "show running-config" and `userReads`
// the successive answers to "show snmp user" — pre-write first, then the
// verifying re-read. Reading past the end repeats the last one, which models a
// device that did not change: that is precisely the device these verifications
// exist to catch, so it must be expressible.
//
// "show snmp group" is answered with THE CAPTURE, rendered for [defaultV3Group]:
// a group that exists at the level asked for and takes the platform's default read
// view. That is the state core01 was actually left in by this connector's own
// write, so it is the right default for every test that is about something else —
// and [snmpFakeClientGroup] is for the tests that are about the group.
func snmpFakeClient(t *testing.T, runCfgs []string, userReads ...string) (*Client,
	*portAdminFakeCLI) {

	t.Helper()
	return snmpFakeClientGroup(t,
		snmpGroupRead(t, defaultV3Group, snmpV3GroupSecurity, ""), runCfgs, userReads...)
}

// snmpFakeClientGroup is [snmpFakeClient] with the "show snmp group" answer
// chosen by the caller.
func snmpFakeClientGroup(t *testing.T, groupRead string, runCfgs []string,
	userReads ...string) (*Client, *portAdminFakeCLI) {

	t.Helper()
	var nRun, nUser int
	var f = &portAdminFakeCLI{out: make(chan byte, 1<<20)}
	f.reply = func(cmd string) string {
		switch cmd {
		case SaveCmd:
			// The completion marker c.save waits for; without it the save falls
			// back and the transcript grows a second form for no reason.
			return "Copy completed successfully."
		case "show running-config":
			if len(runCfgs) == 0 {
				return ""
			}
			if nRun < len(runCfgs) {
				nRun++
			}
			return runCfgs[nRun-1]
		case showSNMPUserCmd:
			if len(userReads) == 0 {
				return ""
			}
			if nUser < len(userReads) {
				nUser++
			}
			return userReads[nUser-1]
		case showSNMPGroupCmd:
			return groupRead
		}
		return ""
	}
	return &Client{
		cli:     &cliConn{stdin: f, out: f.out},
		host:    "192.0.2.5",
		User:    "mcadmin",
		dialect: dialectEOS,
	}, f
}

// v3Want is the identity these tests pose. TWO DISTINCT passphrases, because one
// phrase on both sides is refused and rightly so (checkV3Secrets).
func v3Want() SNMPIdentity {
	return SNMPIdentity{
		User: "mcpoll", AuthAlg: "sha", PrivAlg: "aes128",
		AuthSecret: "auth-phrase-one", PrivSecret: "priv-phrase-two",
	}
}

func snmpSent(f *portAdminFakeCLI) string { return strings.Join(f.sent, "\n") }

// --- the v3 write ----------------------------------------------------------

// The happy path, and the ORDER is half the assertion: the group goes out BEFORE
// the user. A user written first names a group that does not exist yet, which is
// exactly the state the capture was taken in.
func TestApplySNMPv3WritesTheGroupBeforeTheUser(t *testing.T) {
	var c, f = snmpFakeClient(t, nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.UserSet || !res.GroupCreated || !res.Enabled {
		t.Errorf("result = %+v — a confirmed v3 write sets all three", res)
	}

	var wantGroup = "snmp-server group " + defaultV3Group + " v3 priv"
	var wantUser = "snmp-server user mcpoll " + defaultV3Group +
		" v3 auth sha auth-phrase-one priv aes priv-phrase-two"
	var gi, ui = indexOfSent(f, wantGroup), indexOfSent(f, wantUser)
	if gi < 0 {
		t.Fatalf("the group line never went out:\n%s", snmpSent(f))
	}
	if ui < 0 {
		t.Fatalf("the user line never went out, or not in this form:\n%s", snmpSent(f))
	}
	if gi > ui {
		t.Errorf("the user was written BEFORE its group: EOS accepts that and marks the "+
			"reference \"%s\", which is the state this whole feature exists to avoid",
			notConfiguredMark)
	}
	// And it was persisted: an identity a reboot cancels silences supervision.
	if !strings.Contains(snmpSent(f), SaveCmd) {
		t.Errorf("the v3 identity was not saved:\n%s", snmpSent(f))
	}
}

// THE TRAP, AT THE APPLIER, and this is the test the whole change is for.
//
// The device ACCEPTS every command — the fake never refuses anything — and then
// reports the user's group as "(not configured)", which is the real shape
// captured on core01. That is a device that accepted and did something else, and
// the applier must call it a failed write rather than a posed identity.
func TestApplySNMPv3RefusesAGroupTheDeviceMarksNotConfigured(t *testing.T) {
	var c, f = snmpFakeClient(t, nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, false))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err == nil {
		t.Fatalf("a user whose group the device marks \"%s\" was reported as posed: %+v — "+
			"it authenticates and is authorised for NOTHING, so every walk comes back "+
			"empty and the fleet shows supervision that does not exist",
			notConfiguredMark, res)
	}
	if !strings.Contains(err.Error(), notConfiguredMark) {
		t.Errorf("the failure must quote the device's own wording, so the next reader "+
			"knows what to look for: %v", err)
	}
	if res.UserSet || res.GroupCreated {
		t.Errorf("result = %+v — nothing may be reported as written when the "+
			"verification failed", res)
	}
	// The group command DID go out. Without this, the test would pass on a
	// connector that never tried to create the group at all, which is the other
	// half of the bug.
	if indexOfSent(f, "snmp-server group "+defaultV3Group+" v3 priv") < 0 {
		t.Errorf("the applier did not even attempt the group:\n%s", snmpSent(f))
	}
	// And it was NOT persisted: saving a failed identity would carry it across a
	// reboot and make the next reader believe it was intended.
	if strings.Contains(snmpSent(f), SaveCmd) {
		t.Errorf("a failed v3 write was saved to startup-config:\n%s", snmpSent(f))
	}
}

// The device accepted and created NOTHING. Same standard: an empty re-read is a
// failed write, never a silent success.
func TestApplySNMPv3RefusesWhenTheUserIsAbsentAfterTheWrite(t *testing.T) {
	var c, _ = snmpFakeClient(t, nil, "", "")
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err == nil || res.UserSet {
		t.Fatalf("\"%s\" listed no user after the write and the result was %+v, err=%v",
			showSNMPUserCmd, res, err)
	}
}

// The device accepted and created something ELSE — the algorithm this connector
// asked for is not the one it came back with. The keys derive from that algorithm,
// so a collector would never authenticate; reporting success here would send an
// operator hunting a network fault.
func TestApplySNMPv3RefusesAnAlgorithmTheDeviceSubstituted(t *testing.T) {
	var read = strings.Replace(snmpUserRead(t, "mcpoll", defaultV3Group, true),
		"SHA", "MD5", 1)
	var c, _ = snmpFakeClient(t, nil, "", read)
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err == nil || res.UserSet {
		t.Fatalf("a user created with MD5 where SHA was asked for was accepted: %+v", res)
	}
	if !strings.Contains(err.Error(), "MD5") {
		t.Errorf("the failure must quote what the device said: %v", err)
	}
}

// A user that is already there, in a group the device says IS configured, is still
// REWRITTEN — and the group is not.
//
// This is the deliberate departure from the "already conforming writes nothing"
// habit, and it is the only defence against a rotation becoming a silent no-op:
// the keys are invisible, so a name-and-algorithms match cannot distinguish the
// current passphrase from the previous one. Losing this behaviour would make every
// secret rotation report success and change nothing.
func TestApplySNMPv3RewritesAConformingUserButNotItsGroup(t *testing.T) {
	var read = snmpUserRead(t, "mcpoll", defaultV3Group, true)
	var c, f = snmpFakeClient(t, nil, read, read)
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.UserSet {
		t.Error("the user must be rewritten even when it looks conforming: the passphrase " +
			"behind its keys cannot be read, so a rotation would otherwise be a no-op")
	}
	if res.GroupCreated {
		t.Error("the group was already configured and must not be reported as created: " +
			"a group has no invisible half, so the ordinary rule applies to it")
	}
	if strings.Contains(snmpSent(f), "snmp-server group ") {
		t.Errorf("the group was rewritten although the device reported it configured:\n%s",
			snmpSent(f))
	}
}

// --- the group's read authority, which used to be a Skipped line ------------

// THE NOTE IS GONE AND A CHECK STANDS WHERE IT WAS. Before 2026-08-10 a confirmed
// v3 write still carried a Skipped line confessing that what the group could READ
// had never been verified. "show snmp group" has been read since, on the group
// this connector's own line created, and it says the read view is the platform
// default and that default is all included — so the apply now ASSERTS the read
// authority and says nothing.
func TestApplySNMPv3ConfirmsTheGroupsReadAuthority(t *testing.T) {
	var c, _ = snmpFakeClient(t, nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.UserSet {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("a confirmed v3 write still carries a note: %+v — the group's read "+
			"authority is CHECKED now, not confessed", res.Skipped)
	}
	var whole = transcriptText(c)
	if !strings.Contains(whole, "read authority confirmed") {
		t.Errorf("the transcript does not record the group check at all:\n%s", whole)
	}
	// And it quotes the device's own wording, which is what a future survey compares
	// against when EOS changes it.
	if !strings.Contains(whole, "(default: all included)") {
		t.Errorf("the transcript should carry the device's own words about the default "+
			"read view:\n%s", whole)
	}
}

// A GROUP THAT AUTHORISES NOTHING IS A FAILED WRITE, on the same terms as a group
// the device marks "(not configured)".
//
// The consequence is identical — the collector authenticates and every walk comes
// back empty — so the verdict is identical: nothing is reported as written, and
// nothing is persisted. Reporting it as a success with a footnote would put a green
// identity on screen over supervision that is dead.
func TestApplySNMPv3RefusesAGroupThatAuthorisesNothing(t *testing.T) {
	var c, f = snmpFakeClientGroup(t,
		snmpGroupRead(t, defaultV3Group, snmpV3GroupSecurity, "<no read view specified>"),
		nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err == nil {
		t.Fatalf("a group whose read view authorises NOTHING was reported as posed: %+v — "+
			"the user authenticates and every walk comes back empty, which is exactly the "+
			"silent-success class the \"%s\" gate exists to catch", res, notConfiguredMark)
	}
	if !strings.Contains(err.Error(), "<no read view specified>") {
		t.Errorf("the failure must quote the device's own wording, so the next reader knows "+
			"what to look for: %v", err)
	}
	if res.UserSet || res.GroupCreated || res.Enabled {
		t.Errorf("result = %+v — nothing may be reported as written when the verification "+
			"failed", res)
	}
	if strings.Contains(snmpSent(f), SaveCmd) {
		t.Errorf("an identity that authorises nothing was saved to startup-config:\n%s",
			snmpSent(f))
	}
}

// "show snmp user" says the group is configured and "show snmp group" does not
// list it. The two reads disagree; the one that speaks about read authority decides,
// and nothing observed says this group authorises anything.
func TestApplySNMPv3RefusesAGroupTheGroupReadDoesNotList(t *testing.T) {
	var c, _ = snmpFakeClientGroup(t, "", nil, "",
		snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err == nil || res.UserSet {
		t.Fatalf("\"%s\" listed no such group and the identity was posed anyway: %+v",
			showSNMPGroupCmd, res)
	}
}

// The group exists under the name asked for and at ANOTHER security level. The v3
// user's own level does not go through that entry, so it says nothing about what
// this identity may read — and the failure names the level the device did show.
func TestApplySNMPv3RefusesAGroupListedOnlyAtAnotherSecurityLevel(t *testing.T) {
	var c, _ = snmpFakeClientGroup(t, snmpGroupRead(t, defaultV3Group, "v2c", ""),
		nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err == nil || res.UserSet {
		t.Fatalf("a v2c access entry answered for a v3 group: %+v", res)
	}
	if !strings.Contains(err.Error(), "v2c") {
		t.Errorf("the failure must name the level the device DID show: %v", err)
	}
}

// AN EXPLICITLY NAMED READ VIEW IS A NOTE, NOT A REFUSAL, and this is the second
// judgement call of the change.
//
// This connector never names a view, so a named one means somebody else configured
// this group — most plausibly a deliberate, legitimate restriction, tighter than
// the wide-open default this connector settles for. Failing the apply would report
// a working identity as broken on a device where the write already went out and
// cannot be taken back. Confirming it would claim read authority nobody observed:
// resolving what the view contains needs "show snmp view", never run here. So the
// apply SUCCEEDS and the unresolved half is stated, with the view's name in it.
func TestApplySNMPv3NotesANamedReadViewInsteadOfDecidingIt(t *testing.T) {
	var c, f = snmpFakeClientGroup(t,
		snmpGroupRead(t, defaultV3Group, snmpV3GroupSecurity, "ifmib-only"),
		nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err != nil {
		t.Fatalf("a group with a NAMED read view failed the apply: %v — a named view is "+
			"most plausibly a deliberate operator restriction, and the write had already "+
			"gone out", err)
	}
	if !res.UserSet || !res.Enabled {
		t.Errorf("result = %+v — the user was written and confirmed", res)
	}
	var joined = strings.Join(res.Skipped, " | ")
	if !strings.Contains(joined, "ifmib-only") {
		t.Errorf("the view's NAME is what makes the note actionable — \"show snmp view\" is "+
			"one command away — and it is not in it: %+v", res.Skipped)
	}
	if !strings.Contains(joined, "UNRESOLVED") {
		t.Errorf("a named view is neither confirmed nor refused, and the note must say so: "+
			"%+v", res.Skipped)
	}
	// It is a note, so the identity is still persisted: a reboot must not undo a
	// user this run confirmed.
	if !strings.Contains(snmpSent(f), SaveCmd) {
		t.Errorf("the identity was not saved:\n%s", snmpSent(f))
	}
}

// COULD NOT ASK IS NOT THE SAME AS WAS TOLD NOTHING IS AUTHORISED. A device that
// refuses "show snmp group" — an older release, a restricted role — leaves
// exactly the gap this connector carried before the capture existed, and that gap
// lands where it always lived: a note. Failing the apply here would throw away a
// user write confirmed one line earlier, on the strength of no observation at all.
func TestApplySNMPv3NotesAGroupReadThatTheDeviceRefused(t *testing.T) {
	var c, f = snmpFakeClientGroup(t, "% Invalid input detected at '^' marker.",
		nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var res, err = c.ApplySNMP(t.Context(), v3Want())
	if err != nil {
		t.Fatalf("a \"%s\" the device refused failed the whole apply: %v", showSNMPGroupCmd, err)
	}
	if !res.UserSet {
		t.Errorf("result = %+v — the user's own re-read confirmed it", res)
	}
	if !strings.Contains(strings.Join(res.Skipped, " | "), showSNMPGroupCmd) {
		t.Errorf("the unverified half was dropped silently: %+v", res.Skipped)
	}
	if !strings.Contains(snmpSent(f), SaveCmd) {
		t.Errorf("the identity was not saved:\n%s", snmpSent(f))
	}
}

// THE PASSPHRASES MUST NEVER REACH THE TRANSCRIPT. It is stored with the pipeline
// run and outlives the session, so a secret in it is a fleet credential in a
// record nobody thinks of as a vault.
func TestApplySNMPv3KeepsThePassphrasesOutOfTheTranscript(t *testing.T) {
	var c, _ = snmpFakeClient(t, nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var want = v3Want()
	var _, err = c.ApplySNMP(t.Context(), want)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var whole = transcriptText(c)
	if strings.Contains(whole, want.AuthSecret) || strings.Contains(whole, want.PrivSecret) {
		t.Fatalf("a v3 passphrase reached the transcript:\n%s", whole)
	}
	if !strings.Contains(whole, secretMask) {
		t.Errorf("the transcript should still show the command's SHAPE, masked — it is "+
			"what makes a run diagnosable:\n%s", whole)
	}
}

// --- the refusals, all of them BEFORE the device is touched ----------------

// An algorithm this connector has never seen this platform print back is refused
// by name, and refused as a driver.Unsupported so a caller can tell "this
// connector will not" from "this write failed".
//
// NOTHING IS SENT in any of these cases: a refusal decided after the first command
// has gone out is a half-written identity.
func TestApplySNMPv3RefusesUncapturedAlgorithmsWithoutTouchingTheDevice(t *testing.T) {
	var cases = []struct {
		what       string
		auth, priv string
	}{
		{"MD5 is weaker than what was asked and must not be substituted", "md5", "aes128"},
		{"SHA-256 is plausible on EOS and has never been read back here", "sha256", "aes128"},
		{"DES is not what the capture shows", "sha", "des"},
		{"AES-256 has never been read back here", "sha", "aes256"},
		{"no encryption at all walks the MIB in the clear", "sha", ""},
		{"no authentication protects nothing", "", "aes128"},
	}
	var i int
	for i = range cases {
		var c, f = snmpFakeClient(t, nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
		var want = v3Want()
		want.AuthAlg, want.PrivAlg = cases[i].auth, cases[i].priv
		var res, err = c.ApplySNMP(t.Context(), want)
		if err == nil {
			t.Fatalf("%s: written anyway (%+v)", cases[i].what, res)
		}
		if !errors.Is(err, driver.ErrNotImplemented) {
			t.Errorf("%s: the refusal must attach to driver.ErrNotImplemented so a caller "+
				"can tell a capability gap from a failure: %v", cases[i].what, err)
		}
		var unsupported *driver.Unsupported
		if !errors.As(err, &unsupported) {
			t.Errorf("%s: refusal is not a driver.Unsupported: %v", cases[i].what, err)
		}
		if len(f.sent) != 0 {
			t.Errorf("%s: the device was touched before the refusal:\n%s",
				cases[i].what, snmpSent(f))
		}
	}
}

// One passphrase used on both sides is refused, and it is the subtle one: both RFC
// 3414 derivations then consume the same value, so the encryption key is a prefix
// of the authentication key and the encryption adds nothing. It reads as a complete
// v3 identity everywhere.
func TestApplySNMPv3RefusesOnePassphraseUsedTwice(t *testing.T) {
	var c, f = snmpFakeClient(t, nil, "", snmpUserRead(t, "mcpoll", defaultV3Group, true))
	var want = v3Want()
	want.PrivSecret = want.AuthSecret
	var _, err = c.ApplySNMP(t.Context(), want)
	if err == nil {
		t.Fatal("a v3 user with the same phrase on both sides was written")
	}
	if len(f.sent) != 0 {
		t.Errorf("the device was touched before the refusal:\n%s", snmpSent(f))
	}
	// A missing phrase is the same class of refusal.
	want = v3Want()
	want.PrivSecret = ""
	c, f = snmpFakeClient(t, nil, "", "")
	if _, err = c.ApplySNMP(t.Context(), want); err == nil {
		t.Error("a v3 user with no encryption passphrase was written")
	}
}

// An intent with NEITHER half writes nothing and says so. An apply is not a reset:
// an empty field means "nothing to do here", never "remove".
func TestApplySNMPWithNothingToPoseWritesNothing(t *testing.T) {
	var c, f = snmpFakeClient(t, nil, "")
	var res, err = c.ApplySNMP(t.Context(), SNMPIdentity{})
	if err != nil {
		t.Fatalf("an empty intent is not an error: %v", err)
	}
	if len(res.Skipped) == 0 {
		t.Error("nothing was written and nothing said so")
	}
	if len(f.sent) != 0 {
		t.Errorf("an empty intent reached the device:\n%s", snmpSent(f))
	}
}

// --- the v2c half, which the restructuring must not have moved -------------

const snmpRunNoCommunity = `hostname sw-lab-1
!
interface Ethernet1
   switchport mode access
!
`

const snmpRunWithCommunity = snmpRunNoCommunity + "snmp-server community meshcraft ro\n"

func TestApplySNMPCommunityIsWrittenThenConfirmed(t *testing.T) {
	var c, f = snmpFakeClient(t, []string{snmpRunNoCommunity, snmpRunWithCommunity})
	var res, err = c.ApplySNMP(t.Context(), SNMPIdentity{Community: "meshcraft"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.CommunitySet || !res.Enabled {
		t.Errorf("result = %+v", res)
	}
	if !strings.Contains(snmpSent(f), "snmp-server community meshcraft ro") {
		t.Errorf("the community line never went out:\n%s", snmpSent(f))
	}
	if !strings.Contains(snmpSent(f), SaveCmd) {
		t.Errorf("the community was not persisted:\n%s", snmpSent(f))
	}
}

// The device accepted the community and the running configuration does not carry
// it. Nothing may be reported as written.
func TestApplySNMPCommunityAbsentAfterWriteIsAFailure(t *testing.T) {
	var c, _ = snmpFakeClient(t, []string{snmpRunNoCommunity, snmpRunNoCommunity})
	var res, err = c.ApplySNMP(t.Context(), SNMPIdentity{Community: "meshcraft"})
	if err == nil || res.CommunitySet {
		t.Fatalf("an unconfirmed community was reported as written: %+v, err=%v", res, err)
	}
}

// Already conforming: nothing is written and — the part that matters for the flash
// — nothing is saved either.
func TestApplySNMPCommunityAlreadyConformingWritesNothing(t *testing.T) {
	var c, f = snmpFakeClient(t, []string{snmpRunWithCommunity})
	var res, err = c.ApplySNMP(t.Context(), SNMPIdentity{Community: "meshcraft"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.CommunitySet {
		t.Error("a device that was already conforming was reported as written to")
	}
	if strings.Contains(snmpSent(f), "snmp-server community") ||
		strings.Contains(snmpSent(f), SaveCmd) {
		t.Errorf("a conforming device was written to anyway:\n%s", snmpSent(f))
	}
}

// The source restriction is REFUSED and named, on both halves. Writing an
// access-list name without creating the list would bind the identity to an empty
// list and silence the agent completely.
func TestApplySNMPSourcesAreRefusedAndNamed(t *testing.T) {
	var c, f = snmpFakeClient(t, []string{snmpRunNoCommunity, snmpRunWithCommunity})
	var res, err = c.ApplySNMP(t.Context(), SNMPIdentity{
		Community: "meshcraft", Sources: []string{"192.0.2.0/24"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(strings.Join(res.Skipped, " | "), "192.0.2.0/24") {
		t.Errorf("the bound the caller asked for was dropped silently: %+v", res.Skipped)
	}
	if strings.Contains(snmpSent(f), "192.0.2.0") {
		t.Errorf("an access-list name reached the device:\n%s", snmpSent(f))
	}
}

// --- the contract ----------------------------------------------------------

// THE DECLARATION AND THE APPLIER MOVE TOGETHER, and this is the test that keeps
// them together: every algorithm the capabilities advertise must be one this
// connector can actually write, and everything else must be refused. A widened
// declaration with an unchanged applier would put v3 on screen and refuse it in
// the field; a widened applier with an unchanged declaration would be dead code,
// since nothing but snmpprov's plan ever fills SNMPIdentity.User.
func TestSNMPv3CapabilitiesAndTheWriterAgree(t *testing.T) {
	var caps = (&Driver{}).Capabilities("DCS-7050SX-64-R")

	var sawV3 bool
	var v string
	for _, v = range caps.SNMPVersions {
		if v == "v3" {
			sawV3 = true
		}
	}
	if !sawV3 {
		t.Fatal("v3 was declared on 2026-08-10, on the \"show snmp user\" capture that " +
			"made a v3 write verifiable. It stays declared unless that evidence is withdrawn")
	}

	var alg string
	for _, alg = range caps.SNMPv3Auth {
		var _, err = snmpV3AuthKeyword(alg)
		if err != nil {
			t.Errorf("authentication %q is declared and cannot be written: %v", alg, err)
		}
	}
	for _, alg = range caps.SNMPv3Priv {
		var _, err = snmpV3PrivKeyword(alg)
		if err != nil {
			t.Errorf("encryption %q is declared and cannot be written: %v", alg, err)
		}
	}

	// And the declaration is the CAPTURED pair and not an optimistic one. EOS very
	// probably carries more; probably is not a capture, and a claim here decides
	// which secret the fleet derives and whether a device is called conformant.
	if len(caps.SNMPv3Auth) != 1 || caps.SNMPv3Auth[0] != "sha" {
		t.Errorf("SNMPv3Auth = %v — \"SHA\" is the one authentication \"show snmp user\" "+
			"has been seen to print on this platform", caps.SNMPv3Auth)
	}
	if len(caps.SNMPv3Priv) != 1 || caps.SNMPv3Priv[0] != "aes128" {
		t.Errorf("SNMPv3Priv = %v — \"AES-128\" is the one encryption \"show snmp user\" "+
			"has been seen to print on this platform", caps.SNMPv3Priv)
	}
}

// A caller cannot reach into the connector's declaration and change it.
func TestSNMPv3CapabilityListsAreNotShared(t *testing.T) {
	var caps = (&Driver{}).Capabilities("DCS-7050SX-64-R")
	caps.SNMPv3Auth[0] = "md5"
	var again = (&Driver{}).Capabilities("DCS-7050SX-64-R")
	if again.SNMPv3Auth[0] != "sha" {
		t.Fatal("a caller mutated the connector's own declaration")
	}
}

// --- small helpers ---------------------------------------------------------

// indexOfSent returns the position of a command in the transcript of the fake, or
// -1. Order matters in these tests — the group before the user — so the position
// is what is compared, not mere presence.
func indexOfSent(f *portAdminFakeCLI, cmd string) int {
	var i int
	for i = range f.sent {
		if f.sent[i] == cmd {
			return i
		}
	}
	return -1
}

func transcriptText(c *Client) string {
	var out []string
	var s Step
	for _, s = range c.Steps {
		out = append(out, s.What+" — "+s.Detail)
	}
	return strings.Join(out, "\n")
}

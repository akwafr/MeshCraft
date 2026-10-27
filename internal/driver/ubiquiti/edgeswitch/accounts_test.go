package edgeswitch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// The account list is what makes account writes safe (create vs modify), and
// on 2026-08-10 the lab proved that the source it used to come from — the
// running configuration — is INCOMPLETE on this platform.
//
// Two captures pin it, both from unifimoule1 (Ubiquiti EdgePoint S16, FASTPATH
// 1.9.3, 192.0.2.4, 2026-08-10):
//
//	testdata/show_users_accounts_detail.txt          → ubnt, admin, mcadmin
//	testdata/show_running_config_include_username.txt → admin, mcadmin
//
// The factory `ubnt` account, privilege 15, is simply not rendered into the
// configuration. Enumerating from there hid a full administrator from every
// listing, every rotation and every audit — which is the defect this file
// guards against regressing.

// THE TEST THAT MATTERS MOST: the two sources DISAGREE, and the authoritative
// one wins. If this ever passes by both sides returning the same list, the
// captures have been edited and the finding has been erased.
func TestCapturedSourcesDisagreeAndDetailWins(t *testing.T) {
	var detail = parseAccountsDetail(mustRead(t, "testdata/show_users_accounts_detail.txt"))
	var declared = parseRunAccounts(mustRead(t, "testdata/show_running_config_include_username.txt"))

	if len(detail) != 3 {
		t.Fatalf("the captured detail output names three accounts, got %d: %v",
			len(detail), accountNames(detail))
	}
	if len(declared) != 2 {
		t.Fatalf("the captured running-config declares two accounts, got %d: %v",
			len(declared), accountNames(declared))
	}
	// The disagreement itself, named: `ubnt` exists and is not declared.
	var declaredBy = map[string]bool{}
	var i int
	for i = range declared {
		declaredBy[declared[i].Name] = true
	}
	if declaredBy["ubnt"] {
		t.Fatal("the captured running-config must NOT declare `ubnt`: that omission " +
			"IS the finding, and a fixture that declares it proves nothing")
	}
	var hidden []string
	for i = range detail {
		if !declaredBy[detail[i].Name] {
			hidden = append(hidden, detail[i].Name)
		}
	}
	if len(hidden) != 1 || hidden[0] != "ubnt" {
		t.Fatalf("exactly `ubnt` is hidden by the configuration, got %v", hidden)
	}
	// And it is not a harmless account: it is a full administrator.
	for i = range detail {
		if detail[i].Name == "ubnt" && detail[i].Privilege != 15 {
			t.Fatalf("`ubnt` is privilege 15 in the capture, parsed %d", detail[i].Privilege)
		}
	}
}

func TestParseAccountsDetailCaptured(t *testing.T) {
	var got = parseAccountsDetail(mustRead(t, "testdata/show_users_accounts_detail.txt"))
	var want = []string{"ubnt", "admin", "mcadmin"}
	var names = accountNames(got)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("device order preserved: want %v, got %v", want, names)
	}
	var i int
	for i = range got {
		if got[i].Privilege != 15 {
			t.Errorf("%s: privilege 15 in the capture, got %d", got[i].Name, got[i].Privilege)
		}
		if got[i].Source != accountsFromDetail {
			t.Errorf("%s: source %q, want %q", got[i].Name, got[i].Source, accountsFromDetail)
		}
		// The verdict the widened contract carries to a caller that cannot know
		// what 15 means on FASTPATH.
		if got[i].Authority != driver.AuthorityFull {
			t.Errorf("%s: authority %v, want full administrator", got[i].Name, got[i].Authority)
		}
	}
}

// TestContractAccountsCarryThePrivilege — the same list, through the interface.
//
// This package's Account is now driver.Account, so a caller holding a
// driver.AccountManager gets the privilege without knowing it is talking to
// FASTPATH. That is the change the 2026-08-10 audit needed, and this pins the
// end of the wire rather than the parser.
func TestContractAccountsCarryThePrivilege(t *testing.T) {
	var _ []driver.Account = parseAccountsDetail(
		mustRead(t, "testdata/show_users_accounts_detail.txt"))
	var unstated = parseRunAccounts("username \"legacy\" password cccc encrypted\n")
	if unstated[0].Authority != driver.AuthorityUnknown {
		t.Errorf("a line without \"level\" ranks nothing, got %v", unstated[0].Authority)
	}
	var zero = parseAccountsDetail(
		"UserName....................................... zero\n" +
			"Privilege...................................... 0\n")
	if zero[0].Privilege != 0 || zero[0].Authority != driver.AuthorityLimited {
		t.Errorf("%+v — level 0 is REAL on FASTPATH and must not read like \"---\"", zero[0])
	}
}

// "---" is what this CLI prints for a field it has nothing to say about. It
// is NOT level 0 — folding the two together is how an audit stops telling a
// harmless account from a hidden administrator.
func TestParseAccountsDetailUnstatedPrivilege(t *testing.T) {
	var got = parseAccountsDetail(`
UserName....................................... guest
Privilege...................................... ---
Lockout........................................ False

UserName....................................... op
Privilege...................................... 1
Lockout........................................ False
`)
	if len(got) != 2 {
		t.Fatalf("two records, got %d: %v", len(got), accountNames(got))
	}
	if got[0].Privilege != PrivilegeUnknown {
		t.Errorf("\"---\" is an unstated privilege, got %d", got[0].Privilege)
	}
	if got[1].Privilege != 1 {
		t.Errorf("level 1 parsed as %d", got[1].Privilege)
	}
}

// The parser keys on the "UserName" row, not on the blank line between
// records: a build that drops the separator, or spells the label "User Name",
// must still yield the same records — and the label must never be read across
// records (dottedFields keeps the FIRST occurrence, so an unsplit output would
// collapse three accounts into one).
func TestParseAccountsDetailNoBlankSeparatorAndSpacedLabel(t *testing.T) {
	var got = parseAccountsDetail(
		"User Name...................... alpha\n" +
			"Privilege...................... 15\n" +
			"User Name...................... beta\n" +
			"Privilege...................... 1\n")
	if len(got) != 2 {
		t.Fatalf("two records without a blank separator, got %d: %v", len(got), accountNames(got))
	}
	if got[0].Name != "alpha" || got[0].Privilege != 15 {
		t.Errorf("first record: %+v", got[0])
	}
	if got[1].Name != "beta" || got[1].Privilege != 1 {
		t.Errorf("second record: %+v", got[1])
	}
}

// Noise before the first record — the echoed command, a banner, a pager
// remnant — is not an account.
func TestParseAccountsDetailIgnoresPreamble(t *testing.T) {
	var got = parseAccountsDetail("show users accounts detail\n" +
		"% Invalid input detected at '^' marker.\n")
	if len(got) != 0 {
		t.Fatalf("a refusal names no account, got %v", accountNames(got))
	}
}

// --- the FALLBACK path, which must keep working on a firmware without the
// authoritative command, and must keep being read as incomplete ------------

func TestParseUsernamesFromCapturedRunningConfig(t *testing.T) {
	var got = parseUsernames(mustRead(t, "testdata/show_running_config.txt"))
	if len(got) != 1 || got[0] != "admin" {
		t.Fatalf("captured config declares exactly [admin], got %v", got)
	}
}

func TestParseUsernamesQuotedAndBare(t *testing.T) {
	var got = parseUsernames("username \"mcadmin\" password aaaa level 15 encrypted\n" +
		"username admin password bbbb level 15 encrypted\n" +
		"username \"mcadmin\" level 15\n" + // duplicate: once only
		"no username ghost\n") // a negation is not a declaration
	if len(got) != 2 || got[0] != "mcadmin" || got[1] != "admin" {
		t.Fatalf("want [mcadmin admin], got %v", got)
	}
}

// The fallback carries the privilege too when the line states it, and says
// nothing rather than guessing when it does not.
func TestParseRunAccountsCarriesLevel(t *testing.T) {
	var got = parseRunAccounts(mustRead(t,
		"testdata/show_running_config_include_username.txt"))
	if len(got) != 2 {
		t.Fatalf("two declarations, got %v", accountNames(got))
	}
	var i int
	for i = range got {
		if got[i].Privilege != 15 {
			t.Errorf("%s: \"level 15\" on the captured line, parsed %d", got[i].Name, got[i].Privilege)
		}
		if got[i].Source != accountsFromRun {
			t.Errorf("%s: source %q, want %q", got[i].Name, got[i].Source, accountsFromRun)
		}
	}
	var bare = parseRunAccounts("username \"legacy\" password cccc encrypted\n")
	if len(bare) != 1 {
		t.Fatalf("a line without \"level\" is still a declaration, got %v", accountNames(bare))
	}
	if bare[0].Privilege != PrivilegeUnknown {
		t.Errorf("no \"level\" word means unstated, got %d", bare[0].Privilege)
	}
}

// --- both paths through the read, end to end ------------------------------

// scriptedCLI replays a canned answer per command through the same cmdRunner
// shape the SSH shell presents, so readAccountsFrom and auditAccountsFrom are
// exercised AS THEY RUN — the selection between the two sources is the code the
// finding is about, and a test that reimplemented it would prove nothing.
type scriptedCLI struct {
	answers map[string]string
	errs    map[string]error
	asked   []string
}

func (s *scriptedCLI) run(_ context.Context, cmd string, _ time.Duration) (string, error) {
	s.asked = append(s.asked, cmd)
	return s.answers[cmd], s.errs[cmd]
}

func capturedScript(t *testing.T) *scriptedCLI {
	t.Helper()
	return &scriptedCLI{answers: map[string]string{
		accountsFromDetail: mustRead(t, "testdata/show_users_accounts_detail.txt"),
		accountsFromRun:    mustRead(t, "testdata/show_running_config_include_username.txt"),
	}}
}

// Path 1 — the firmware carries the authoritative command: it wins, the
// configuration is not even read, and the factory account appears in the list
// the caller gets. Hiding it was the defect.
func TestReadAccountsPrefersDetail(t *testing.T) {
	var s = capturedScript(t)
	var accounts, source, err = readAccountsFrom(context.Background(), s.run, "192.0.2.4")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if source != accountsFromDetail {
		t.Fatalf("the authoritative source must win, got %q", source)
	}
	if len(s.asked) != 1 || s.asked[0] != accountsFromDetail {
		t.Errorf("the configuration must not be read when the detail read answered: %v", s.asked)
	}
	var names = strings.Join(accountNames(accounts), ",")
	if !strings.Contains(names, "ubnt") {
		t.Fatalf("the factory account must be LISTED — hiding it is the defect: %s", names)
	}
	if names != "ubnt,admin,mcadmin" {
		t.Fatalf("device order preserved, got %s", names)
	}
}

// Path 2 — a firmware that refuses the command falls back, and the fallback
// still returns the usable (if incomplete) list.
func TestReadAccountsFallsBackToRunningConfig(t *testing.T) {
	var s = capturedScript(t)
	s.answers[accountsFromDetail] = "show users accounts detail\n% Invalid input detected at '^' marker.\n"
	s.errs = map[string]error{
		accountsFromDetail: errors.New("% Invalid input detected at '^' marker."),
	}
	var accounts, source, err = readAccountsFrom(context.Background(), s.run, "192.0.2.4")
	if err != nil {
		t.Fatalf("the fallback must rescue this, got %v", err)
	}
	if source != accountsFromRun {
		t.Fatalf("a refused detail read must fall back, got %q", source)
	}
	if len(s.asked) != 2 {
		t.Errorf("both reads expected on the fallback path: %v", s.asked)
	}
	var names = accountNames(accounts)
	if len(names) != 2 || names[0] != "admin" || names[1] != "mcadmin" {
		t.Fatalf("fallback list [admin mcadmin], got %v", names)
	}
}

// Neither source answering is an error, not an empty list: an empty account
// list would read as "this device has no administrator", which is never true.
func TestReadAccountsBothSourcesFail(t *testing.T) {
	var s = &scriptedCLI{
		answers: map[string]string{},
		errs: map[string]error{
			accountsFromDetail: errors.New("% Invalid input detected at '^' marker."),
			accountsFromRun:    errors.New("no response after 90s"),
		},
	}
	var _, _, err = readAccountsFrom(context.Background(), s.run, "192.0.2.4")
	if err == nil {
		t.Fatal("two failed reads must be an error, not an empty account list")
	}
	if !strings.Contains(err.Error(), accountsFromDetail) ||
		!strings.Contains(err.Error(), accountsFromRun) {
		t.Errorf("the error must name both reads it tried: %v", err)
	}
}

// THE AUDIT: on the captured pair it must find `ubnt`, name its privilege, and
// say the two sources were actually compared.
func TestAuditAccountsReportsTheHiddenAdministrator(t *testing.T) {
	var s = capturedScript(t)
	var inv, err = auditAccountsFrom(context.Background(), s.run, "192.0.2.4")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if !inv.Compared {
		t.Fatal("both sources answered: the audit must say it compared them")
	}
	if inv.Source != accountsFromDetail {
		t.Errorf("source %q", inv.Source)
	}
	if len(inv.Hidden) != 1 || inv.Hidden[0].Name != "ubnt" {
		t.Fatalf("exactly `ubnt` is hidden, got %v", accountNames(inv.Hidden))
	}
	if inv.Hidden[0].Privilege != 15 {
		t.Fatalf("the hidden account is privilege 15, got %d", inv.Hidden[0].Privilege)
	}
	var notes = strings.Join(inv.Notes, "\n")
	if !strings.Contains(notes, "ubnt") || !strings.Contains(notes, "FULL ADMINISTRATOR") {
		t.Errorf("the note must name the account and its privilege:\n%s", notes)
	}
	// REPORT, NEVER ACTION: nothing was sent but the two reads.
	var i int
	for i = range s.asked {
		if s.asked[i] != accountsFromDetail && s.asked[i] != accountsFromRun {
			t.Errorf("the audit sent %q: it must only READ", s.asked[i])
		}
	}
}

// On a firmware without the authoritative command the audit degrades honestly:
// an empty Hidden must NOT read as "clean", and Compared is how a caller tells
// the two apart.
func TestAuditAccountsCannotCompareOnFallback(t *testing.T) {
	var s = capturedScript(t)
	s.answers[accountsFromDetail] = ""
	var inv, err = auditAccountsFrom(context.Background(), s.run, "192.0.2.4")
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if inv.Compared {
		t.Fatal("only one source answered: nothing was compared")
	}
	if len(inv.Hidden) != 0 {
		t.Fatalf("nothing can be called hidden with a single source, got %v",
			accountNames(inv.Hidden))
	}
	if inv.Source != accountsFromRun {
		t.Errorf("source %q", inv.Source)
	}
	var notes = strings.Join(inv.Notes, "\n")
	if !strings.Contains(notes, "nothing here can tell") {
		t.Errorf("the note must refuse to imply the device is clean:\n%s", notes)
	}
}

// The report is a report. Nothing in the note may read as an order, and the
// privilege must be spelled out — an unmanaged account at level 15 and one at
// level 1 are different problems.
func TestPrivilegeWordNamesFullAdministrator(t *testing.T) {
	if !strings.Contains(privilegeWord(15), "FULL ADMINISTRATOR") {
		t.Errorf("level 15 must be named for what it is: %q", privilegeWord(15))
	}
	if strings.Contains(privilegeWord(1), "FULL ADMINISTRATOR") {
		t.Errorf("level 1 is not a full administrator: %q", privilegeWord(1))
	}
	if !strings.Contains(privilegeWord(PrivilegeUnknown), "not stated") {
		t.Errorf("an unstated privilege must say so: %q", privilegeWord(PrivilegeUnknown))
	}
}

// --- the deletion guards ---------------------------------------------------
//
// A deletion on this range is the one gesture with no cheap repair: the
// EdgePoint S16 has NO SERIAL CONSOLE, so a switch left without a usable
// account is recovered with the front-panel reset button, on site, at the cost
// of its configuration. Until 2026-08-12 the only refusal here compared the
// target against the CURRENT SESSION's name, which let "delete admin while
// logged in as mcadmin" through untouched. These tests pin both refusals AND
// the limit of the second one — see [deleteAccountFrom].

// writesIn separates what was WRITTEN from what was read. The guard's promise
// is that a refusal costs the device nothing, and that is a statement about
// this list being empty.
func writesIn(asked []string) []string {
	var out []string
	var i int
	for i = range asked {
		if asked[i] == accountsFromDetail || asked[i] == accountsFromRun {
			continue
		}
		out = append(out, asked[i])
	}
	return out
}

// oneAccountScript: a device that enumerates a single administration account.
func oneAccountScript(name string) *scriptedCLI {
	return &scriptedCLI{answers: map[string]string{
		accountsFromDetail: "UserName....................................... " + name + "\n" +
			"Privilege...................................... 15\n" +
			"Password Aging................................. ---\n",
	}}
}

// THE GAP THAT WAS OPEN: the last account must not go, whoever is asking.
//
// The session runs as an account the enumeration does not name — which is not a
// contrivance on this platform, where the running-config source provably hides
// the factory account (see the head of this file). It is also the only shape in
// which the second refusal can fire ahead of the first.
func TestDeleteRefusesTheLastAccount(t *testing.T) {
	var s = oneAccountScript("mcadmin")
	var err = deleteAccountFrom(context.Background(), s.run, "192.0.2.4", "ubnt", "mcadmin")
	if err == nil {
		t.Fatal("the last enumerated account must not be deletable: this range has no " +
			"serial console and the repair is a site visit")
	}
	if !strings.Contains(err.Error(), "only administration account") {
		t.Errorf("the refusal must say what it refused: %v", err)
	}
	var wrote = writesIn(s.asked)
	if len(wrote) != 0 {
		t.Fatalf("a refused deletion must cost the device NOTHING, sent: %v", wrote)
	}
}

// A refusal must not depend on the account list being complete. On a firmware
// without the authoritative command the list comes from the running
// configuration, which on FASTPATH 1.9.3 OMITS the factory account — so the
// count can only be too LOW. The guard then refuses a deletion that might have
// been fine, which is the direction it is allowed to be wrong in.
func TestDeleteRefusesOnTheIncompleteFallbackList(t *testing.T) {
	var s = &scriptedCLI{
		answers: map[string]string{
			accountsFromDetail: "% Invalid input detected at '^' marker.\n",
			accountsFromRun:    "username \"admin\" password 0000 level 15 encrypted\n",
		},
		errs: map[string]error{
			accountsFromDetail: errors.New("% Invalid input detected at '^' marker."),
		},
	}
	// The session runs as `ubnt`, the very account this source cannot see.
	var err = deleteAccountFrom(context.Background(), s.run, "192.0.2.4", "ubnt", "admin")
	if err == nil {
		t.Fatal("the only account the connector can SEE is the last one it may act on")
	}
	if !strings.Contains(err.Error(), accountsFromRun) {
		t.Errorf("the refusal must name the source it counted from: %v", err)
	}
	if len(writesIn(s.asked)) != 0 {
		t.Fatalf("nothing may be written on a refusal: %v", writesIn(s.asked))
	}
}

// The other half: a deletion that leaves something behind still goes through,
// and sends exactly the captured form.
//
// IT ALSO PINS WHAT THE GUARD DOES NOT PROMISE. The captured device carries
// `ubnt`, `admin` and `mcadmin`; only the last two have ever been authenticated
// by meshcraft, and `ubnt`'s secret is the factory's or a previous owner's. This
// deletion therefore takes the device from TWO proven credentials to ONE and the
// connector ALLOWS it — because it counts accounts, not usable credentials, and
// no connector can see the vault. If this test is ever changed to expect a
// refusal, the check it wants belongs at the layer that holds the secrets.
func TestDeleteAllowsAnAccountThatIsNotTheLast(t *testing.T) {
	var s = capturedScript(t)
	var err = deleteAccountFrom(context.Background(), s.run, "192.0.2.4", "mcadmin", "admin")
	if err != nil {
		t.Fatalf("two accounts survive this deletion, it must proceed: %v", err)
	}
	var wrote = strings.Join(writesIn(s.asked), " | ")
	if wrote != "configure | no username admin | exit" {
		t.Fatalf("the captured deletion form is \"no username <name>\" inside a "+
			"configuration block, sent: %s", wrote)
	}
}

// The first refusal, unchanged and still in front: the session's own account.
// Nothing at all is sent — not even a read — because a session cannot outlive
// the account it opened under whatever the rest of the device carries.
func TestDeleteRefusesTheCurrentSessionAccount(t *testing.T) {
	var s = capturedScript(t)
	var err = deleteAccountFrom(context.Background(), s.run, "192.0.2.4", "mcadmin", "McAdmin")
	if err == nil {
		t.Fatal("deleting the current session's account must be refused, case included")
	}
	if !strings.Contains(err.Error(), "current session") {
		t.Errorf("the refusal must name its reason: %v", err)
	}
	if len(s.asked) != 0 {
		t.Fatalf("this refusal is free: nothing may be sent, got %v", s.asked)
	}
}

// A read that fails REFUSES here, where eos and ios proceed — and the reason is
// hardware, not taste: those ranges keep a serial console, this one does not.
// The read fails only when BOTH sources failed, and a session in that state is
// not one to delete accounts from.
func TestDeleteRefusesWhenTheAccountsCannotBeRead(t *testing.T) {
	var s = &scriptedCLI{
		answers: map[string]string{},
		errs: map[string]error{
			accountsFromDetail: errors.New("% Invalid input detected at '^' marker."),
			accountsFromRun:    errors.New("no response after 90s"),
		},
	}
	var err = deleteAccountFrom(context.Background(), s.run, "192.0.2.4", "mcadmin", "admin")
	if err == nil {
		t.Fatal("an unreadable account list must not open the guard: the recovery from " +
			"an accounts-less switch on this range is a site visit")
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("the refusal must say the guard could not be evaluated: %v", err)
	}
	if len(writesIn(s.asked)) != 0 {
		t.Fatalf("nothing may be written on a refusal: %v", writesIn(s.asked))
	}
}

// The predicate itself, at its edges: what SURVIVES the deletion, matched the
// way the device echoes names back.
func TestAccountsExceptAnswersWhatSurvives(t *testing.T) {
	var have = parseAccountsDetail(mustRead(t, "testdata/show_users_accounts_detail.txt"))
	var left = accountNames(accountsExcept(have, "ADMIN"))
	if strings.Join(left, ",") != "ubnt,mcadmin" {
		t.Errorf("case-insensitive removal, device order kept: got %v", left)
	}
	if len(accountsExcept(have, "nobody")) != 3 {
		t.Error("a name the device does not carry removes nothing")
	}
	if len(accountsExcept(nil, "admin")) != 0 {
		t.Error("nothing survives an empty list")
	}
}

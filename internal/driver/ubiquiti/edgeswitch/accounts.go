package edgeswitch

// Named administration accounts — driver.AccountManager.
//
// This range HAS named accounts ("username <name> password <secret> level
// 15", the very shape SetSecret already sends), but until now only the
// logged-in account's password could be changed: placing the fleet account
// (`mcadmin`) on a device that never received it was refused with "no notion
// of named account", which is false on FASTPATH. What was missing is exactly
// this file.
//
// THESE GESTURES DO NOT PERSIST — the caller sequences its own calls to
// Persist between create, verify, and delete (cf. driver.AccountManager's
// header: between the two steps a power outage must not leave the device with
// a single account, and not the one that survived).
//
// # THE RUNNING CONFIGURATION IS NOT THE ACCOUNT LIST (CAPTURED 2026-08-10)
//
// On unifimoule1 (EdgePoint S16, FASTPATH 1.9.3, 192.0.2.4) the two reads
// DISAGREE, and the disagreement is the whole reason this file was rewritten:
//
//	show users accounts detail   →  ubnt, admin, mcadmin   (three, all level 15)
//	show running-config | inc username →  admin, mcadmin    (two)
//
// FASTPATH does not render the FACTORY `ubnt` account into the running
// configuration. Both captures are pinned in testdata/ and replayed by
// accounts_test.go.
//
// Enumerating from the configuration therefore hid a privilege-15 account
// completely: never listed, so never rotated by the fleet credential runner,
// never proposed for removal, never audited — its password being whatever the
// factory or a previous owner left. That is precisely the unmanaged privileged
// way-in the credential vault exists to prevent, and the connector was the
// thing making it invisible.
//
// So "show users accounts detail" is now the AUTHORITATIVE source, and the
// configuration parse survives only as a FALLBACK for a firmware that does not
// carry that command. When the fallback is used, the inventory says so: with a
// single source there is nothing to compare, and "no hidden account found" and
// "nothing here can tell" must not read alike.
//
// # The privilege now travels THROUGH the contract, and this shape is what it took
//
// This file used to explain why `Accounts` still returned `[]string`: widening
// it would force RouterOS, EOS, IOS and the FS ranges to invent a value for a
// field only FASTPATH's detail read could populate. That turned out to be
// false — RouterOS had the group, Instant On the accessLevel, EOS the privilege
// AND the role, each parsed and each dropped at the interface. So the contract
// was widened on 2026-08-10 to exactly the shape below: [Account] moved into
// the driver package (this package's Account and PrivilegeUnknown are now
// aliases of it), and [driver.AccountManager.Accounts] returns it.
//
// What stayed vendor-specific is the part that is genuinely vendor-specific and
// genuinely costs a second read: [Client.AuditAccounts] / [AccountInventory],
// the COMPARISON between the two sources. [Client.Accounts] still sends one
// command; the privilege it carries is free, the disagreement is not.
//
// # REPORT, NEVER ACTION
//
// Nothing in this file deletes, locks or rotates an account it discovered.
// What to DO about an account meshcraft did not put there is a product
// decision — one that would otherwise be taken silently, identically, for
// every site at once. The connector's job here is to stop hiding it.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// The two reads, named once so the inventory, the errors and the transcript
// all quote the same string.
const (
	accountsFromDetail = "show users accounts detail"
	accountsFromRun    = "show running-config"
)

// PrivilegeUnknown is the privilege of an account whose source does not state
// one. Deliberately NOT 0: level 0 is a real FASTPATH privilege, and folding
// "no privilege" into "not said" is how an audit stops telling a harmless
// account apart from a hidden administrator.
//
// AN ALIAS since the contract widened: the rule this package established is now
// the fleet's, and two constants that must agree are one constant.
const PrivilegeUnknown = driver.PrivilegeUnknown

// Account is one administration account as a device read enumerates it — the
// contract's own type, aliased so this file's parsers and [AccountInventory]
// keep reading as they did.
type Account = driver.Account

// AccountInventory is what the device says about its accounts, and what its
// two sources disagree about.
//
// REPORT ONLY — see this file's header.
type AccountInventory struct {
	// Accounts is the authoritative list, in the order the device gave it.
	Accounts []Account
	// Source is the command Accounts came from: accountsFromDetail normally,
	// accountsFromRun on a firmware without the detail command.
	Source string
	// Hidden lists the accounts the device enumerates and the running
	// configuration does NOT declare — the `ubnt` case.
	Hidden []Account
	// Compared says whether Hidden means anything. TRUE when both sources
	// answered, so Hidden is "these and no others". FALSE when only one did:
	// Hidden is then empty because nothing COULD be compared, not because
	// nothing hides — and a caller that reads an empty Hidden as "clean"
	// without checking this field draws exactly the wrong conclusion.
	Compared bool
	// Notes is prose for a human or an event: what was read, what could not
	// be, and what the disagreement means. No note is an instruction.
	Notes []string
}

// reAccountRecord marks the first line of one record in "show users accounts
// detail".
//
// CAPTURED shape (unifimoule1, EdgePoint S16, FASTPATH 1.9.3, 192.0.2.4,
// 2026-08-10) — testdata/show_users_accounts_detail.txt:
//
//	UserName....................................... ubnt
//	Privilege...................................... 15
//	Password Aging................................. ---
//
// Records are separated by a blank line in that capture, and this parser keys
// on the "UserName" row rather than on the separator: a build that drops the
// blank line, or that inserts a field, still yields the same records. The
// spaced spelling "User Name" is accepted because FASTPATH builds differ on
// less than this.
var reAccountRecord = regexp.MustCompile(`(?mi)^[ \t]*user\s?name[ \t]*\.{2,}`)

// parseAccountsDetail reads the authoritative account list. Pure — replayed
// against the capture, the way every parser in this package is.
//
// It reuses dottedFields (parse.go, the helper mergeSpanningTreeDetail keys on)
// rather than growing a second dotted-label parser. That helper keeps the FIRST
// occurrence of a repeated label, which is exactly right WITHIN one record and
// exactly wrong ACROSS several — every record here repeats "UserName" — so
// the split into records has to happen before it is called, and does.
func parseAccountsDetail(out string) []Account {
	var starts = reAccountRecord.FindAllStringIndex(out, -1)
	var accounts []Account
	var seen = map[string]bool{}
	var i int
	for i = range starts {
		var end = len(out)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		var f = dottedFields(out[starts[i][0]:end])
		var name = f["username"]
		if name == "" {
			name = f["user name"]
		}
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		accounts = append(accounts, driver.AccountAtLevel(name,
			parsePrivilege(f["privilege"]), accountsFromDetail))
	}
	return accounts
}

// parsePrivilege reads a "Privilege" value. This CLI writes "---" for a
// field it has nothing to say about, and that is not level 0.
func parsePrivilege(v string) int {
	var n, err = strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return PrivilegeUnknown
	}
	return n
}

// reRunUsername matches account declarations in "show running-config".
//
// CAPTURED shape (EdgePoint S16, firmware 1.9.2 then 1.9.3):
//
//	username "admin"   password 0000…0000 level 15 encrypted
//
// The name is QUOTED on this platform; the unquoted alternative is accepted
// too because FASTPATH builds differ on less than this. The rest of the line
// is captured separately so the level can be lifted out of it without a line
// that omits "level" disappearing from the list altogether.
//
// WHAT THIS REGEX CANNOT DO, and it is not a parser bug: match an account the
// device never wrote into its configuration. See the file header.
var reRunUsername = regexp.MustCompile(`(?m)^[ \t]*username\s+"?([^"\s]+)"?\s([^\n]*)`)

// reRunLevel lifts "level 15" out of the tail of such a line.
var reRunLevel = regexp.MustCompile(`\blevel\s+(\d+)\b`)

// parseRunAccounts extracts the accounts a running configuration DECLARES, in
// order of appearance, without duplicates.
//
// This is the FALLBACK source and it is incomplete by construction on FASTPATH
// 1.9.3 — the factory account is absent from it. Callers get that warning from
// [AccountInventory.Notes], not from this function, which only reports what the
// text in front of it says.
func parseRunAccounts(runningConfig string) []Account {
	var seen = map[string]bool{}
	var out []Account
	var m []string
	for _, m = range reRunUsername.FindAllStringSubmatch(runningConfig, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		var priv = PrivilegeUnknown
		var lvl = reRunLevel.FindStringSubmatch(m[2])
		if lvl != nil {
			priv = parsePrivilege(lvl[1])
		}
		out = append(out, driver.AccountAtLevel(m[1], priv, accountsFromRun))
	}
	return out
}

// parseUsernames is parseRunAccounts reduced to names.
func parseUsernames(runningConfig string) []string {
	return accountNames(parseRunAccounts(runningConfig))
}

// accountNames reduces an inventory to bare names — no longer what the contract
// returns, still what a transcript line and this package's tests read.
func accountNames(accounts []Account) []string {
	return driver.AccountNames(accounts)
}

// Accounts lists the administration accounts present.
//
// The list now comes from "show users accounts detail", the source that names
// the factory account too; the running configuration is consulted only if that
// read gives nothing. One short command in the normal case — the configuration
// is a 222-line read on a 90-second timeout, on a platform that grants five
// sessions, so paying for it on every enumeration was never free.
//
// It carries the PRIVILEGE of each account, which the detail read states and
// the configuration fallback states only when the line says "level N" — no
// extra command either way. What it does NOT carry is the disagreement between
// the two sources: that costs the second read, and it lives in
// [Client.AuditAccounts].
func (c *Client) Accounts(ctx context.Context) ([]Account, error) {
	var accounts, source, err = c.readAccounts(ctx)
	if err != nil {
		return nil, err
	}
	// WHICH SOURCE ANSWERED is worth a line in the transcript: on the fallback
	// the list is known-incomplete, and an operator staring at a listing that
	// omits an account needs to see that from the transcript alone.
	var detail = "source: \"" + source + "\""
	if source == accountsFromRun {
		detail += " — FALLBACK: the running configuration does not declare the " +
			"factory account on this range (CAPTURED 2026-08-10), so this list may " +
			"omit one. \"" + accountsFromDetail + "\" gave nothing on this device."
	}
	var i int
	for i = range accounts {
		detail += " | " + accounts[i].Describe()
	}
	c.step(true, fmt.Sprintf("%d administration account(s) read", len(accounts)), detail)
	return accounts, nil
}

// cmdRunner is (*cliConn).run's shape. The source-selection below is the code
// the 2026-08-10 finding is about, so it is written against this rather than
// against a live SSH shell: a test can then exercise the REAL choice — which
// command is sent, in which order, and which answer wins — instead of a copy of
// it kept in step by hand.
type cmdRunner func(ctx context.Context, cmd string, timeout time.Duration) (string, error)

func (c *Client) runner() (cmdRunner, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("no open session on this access")
	}
	return c.cli.run, nil
}

// readAccounts returns the authoritative list and the command it came from.
func (c *Client) readAccounts(ctx context.Context) ([]Account, string, error) {
	var run, err = c.runner()
	if err != nil {
		return nil, "", err
	}
	return readAccountsFrom(ctx, run, c.host)
}

// readAccountsFrom sends the authoritative command and falls back to the
// configuration only when it yields nothing — a refusal on an older firmware,
// or an empty answer. The fallback is NOT reached merely because the detail
// read errored while still naming accounts: this CLI prints its "% Invalid
// input" line into the same buffer as real output, and throwing away a good
// list because of a stray error line is how a complete source gets abandoned
// for an incomplete one.
func readAccountsFrom(ctx context.Context, run cmdRunner, host string) ([]Account, string, error) {
	var out, err = run(ctx, accountsFromDetail, cmdTimeout)
	var accounts = parseAccountsDetail(out)
	if len(accounts) > 0 {
		return accounts, accountsFromDetail, nil
	}
	var why = "the output named no account"
	if err != nil {
		why = firstLine(err.Error())
	}
	var runOut string
	runOut, err = run(ctx, accountsFromRun, showTimeout)
	if err != nil && strings.TrimSpace(runOut) == "" {
		return nil, "", fmt.Errorf("neither account read worked on %s: \"%s\" (%s), "+
			"\"%s\" (%s)", host, accountsFromDetail, why, accountsFromRun, firstLine(err.Error()))
	}
	return parseRunAccounts(runOut), accountsFromRun, nil
}

// AuditAccounts reads BOTH sources on purpose and reports what they disagree
// about — the accounts the device has and its configuration does not declare.
//
// A READ AND A REPORT. It deletes nothing, locks nothing, rotates nothing, and
// must not be made to: what to do about an unmanaged privileged account is a
// product decision, and one taken here would be taken silently for every site
// at once. It costs one command more than [Client.Accounts], which is why it is
// a separate call rather than something every enumeration pays for.
func (c *Client) AuditAccounts(ctx context.Context) (AccountInventory, error) {
	var run, rerr = c.runner()
	if rerr != nil {
		return AccountInventory{}, rerr
	}
	var inv, err = auditAccountsFrom(ctx, run, c.host)
	if err != nil {
		c.step(false, "accounts NOT enumerated", firstLine(err.Error()))
		return inv, err
	}
	var headline = fmt.Sprintf("%d account(s) enumerated, hidden accounts UNDETERMINED",
		len(inv.Accounts))
	if inv.Compared {
		headline = fmt.Sprintf("%d account(s) enumerated, %d hidden from the running "+
			"configuration", len(inv.Accounts), len(inv.Hidden))
	}
	c.step(true, headline,
		strings.Join(append([]string{"source: \"" + inv.Source + "\""}, inv.Notes...), " "))
	return inv, nil
}

// auditAccountsFrom is AuditAccounts without the transcript — the part worth
// replaying without hardware.
func auditAccountsFrom(ctx context.Context, run cmdRunner, host string) (AccountInventory, error) {
	var inv AccountInventory
	var detOut, detErr = run(ctx, accountsFromDetail, cmdTimeout)
	var runOut, runErr = run(ctx, accountsFromRun, showTimeout)
	var runFailed = runErr != nil && strings.TrimSpace(runOut) == ""

	inv.Accounts = parseAccountsDetail(detOut)
	inv.Source = accountsFromDetail
	if len(inv.Accounts) == 0 {
		// A firmware that does not carry the authoritative command. The
		// configuration is then all there is — and this inventory CANNOT say
		// whether an account is hidden, only that nothing here can tell.
		var why = "the output named no account"
		if detErr != nil {
			why = firstLine(detErr.Error())
		}
		if runFailed {
			return inv, fmt.Errorf("neither account read worked on %s: \"%s\" (%s), "+
				"\"%s\" (%s)", host, accountsFromDetail, why, accountsFromRun,
				firstLine(runErr.Error()))
		}
		inv.Accounts = parseRunAccounts(runOut)
		inv.Source = accountsFromRun
		inv.Notes = append(inv.Notes, fmt.Sprintf("\"%s\" gave nothing (%s): this list "+
			"comes from the running configuration, which on FASTPATH 1.9.3 was CAPTURED "+
			"omitting the factory privilege-15 account. An account may be present on "+
			"this device and absent from this list — nothing here can tell.",
			accountsFromDetail, why))
		return inv, nil
	}
	if runFailed {
		inv.Notes = append(inv.Notes, fmt.Sprintf("\"%s\" failed (%s): the list above "+
			"stands, but nothing was compared against the running configuration, so "+
			"this inventory names no hidden account rather than finding none.",
			accountsFromRun, firstLine(runErr.Error())))
		return inv, nil
	}

	inv.Compared = true
	var declared = parseRunAccounts(runOut)
	var declaredBy = map[string]bool{}
	var i int
	for i = range declared {
		declaredBy[strings.ToLower(declared[i].Name)] = true
	}
	for i = range inv.Accounts {
		if !declaredBy[strings.ToLower(inv.Accounts[i].Name)] {
			inv.Hidden = append(inv.Hidden, inv.Accounts[i])
		}
	}
	for i = range inv.Hidden {
		inv.Notes = append(inv.Notes, fmt.Sprintf("account %q (%s) EXISTS on the device "+
			"and is NOT declared in its running configuration. Whatever enumerates from "+
			"the configuration has therefore never rotated its password and would not "+
			"remove it; its secret is whatever the factory or a previous owner left. "+
			"REPORTED ONLY — this connector does not act on it.",
			inv.Hidden[i].Name, privilegeWord(inv.Hidden[i].Privilege)))
	}
	if len(inv.Hidden) == 0 {
		inv.Notes = append(inv.Notes, "both sources agree: every account the device "+
			"enumerates is declared in its running configuration.")
	}
	return inv, nil
}

// privilegeWord renders a privilege for prose a human reads. The wording moved
// to [driver.PrivilegeWord] when the contract widened — eight connectors now
// write this sentence and they must write it identically.
func privilegeWord(p int) string {
	return driver.PrivilegeWord(p)
}

// EnsureAccount guarantees that a named account exists and carries this
// secret. Idempotent: "username" creates or modifies depending on what
// already exists — FASTPATH does not distinguish the two verbs. DOES NOT
// PERSIST — sequencing a rename is the caller's responsibility.
func (c *Client) EnsureAccount(ctx context.Context, user, secret string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	if strings.TrimSpace(user) == "" || secret == "" {
		return fmt.Errorf("account name and secret are both required")
	}
	// Same refusal as SetSecret, same reason: this CLI takes the password as a
	// bare word on the command line, whitespace would silently truncate it.
	if strings.ContainsAny(secret, " \t\r\n") {
		return fmt.Errorf("secret rejected: this CLI cannot carry whitespace in a password")
	}
	err = c.runBlock(ctx, []string{
		"configure",
		fmt.Sprintf("username %s password %s level 15", user, secret),
		"exit",
	})
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not written", user), firstLine(err.Error()))
		return fmt.Errorf("writing account %s on %s: %w", user, c.host, err)
	}
	c.step(true, fmt.Sprintf("account %q ensured on device", user),
		"level 15; note that \"enable\" takes the login password on this platform")
	return nil
}

// DeleteAccount removes an administration account.
//
// CAPTURED 2026-08-10 on unifimoule1 (EdgePoint S16, FASTPATH 1.9.3): the form
// "no username <name>", until then only deduced, was exercised on a scratch
// account that was created and then removed with it. It works.
//
// TWO safeguards, and the second one arrived on 2026-08-12 — see
// [deleteAccountFrom] for what each of them does and, more importantly, for
// what NEITHER of them promises. Beyond those two this method still deletes
// what it is told to, including an account this connector never created and one
// only [Client.AuditAccounts] can see: deciding which of those deserve to go is
// the caller's job and not a rule this file may invent.
func (c *Client) DeleteAccount(ctx context.Context, user string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	var run cmdRunner
	run, err = c.runner()
	if err != nil {
		return err
	}
	err = deleteAccountFrom(ctx, run, c.host, c.User, user)
	if err != nil {
		c.step(false, fmt.Sprintf("account %q NOT deleted", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q deleted", user),
		"the device still enumerates at least one other account — which is NOT a "+
			"promise that anyone holds its secret")
	return nil
}

// deleteAccountFrom is DeleteAccount without the session and the transcript:
// the two refusals, the read they need, and the write. Written against
// [cmdRunner] for the same reason [readAccountsFrom] is — this is the code the
// 2026-08-12 halt is about, and a test that reimplemented the decision would
// prove nothing about the one that runs.
//
// # Refusal 1: the branch the session sits on
//
// Unchanged since this file was written, and it was never enough: it compares
// against the CURRENT SESSION's name only. Deleting `admin` while logged in as
// `mcadmin` sailed straight through it, on a device where that was the second
// of two proven credentials.
//
// # Refusal 2: the last account (2026-08-12)
//
// arista/eos, cisco/ios carry the same shape (`len(have) <= 1`) and this range
// had nothing. The gap was found holding a real deletion on unifimoule1: the
// order was to remove `admin`, a privilege-15 account on a stale secret, and
// what would have been LEFT is the whole question. See TODO.md.
//
// THE PREDICATE IS "WHAT WOULD REMAIN", not "how many are there": the list is
// filtered of the account about to go and the deletion is refused when nothing
// survives it. That is `len(have) <= 1` made exact — it also lets through the
// delete of a name the device does not enumerate at all, which removes nothing
// and cannot be the last of anything.
//
// # WHAT THIS GUARD DOES NOT PROMISE, and the wording matters more than the code
//
// It does NOT promise a way back in. It counts accounts the DEVICE enumerates;
// it cannot know which of their secrets meshcraft holds, which have ever been
// authenticated, or which are a factory password left by a previous owner. On
// the very device that motivated it, the three accounts are `ubnt` (privilege
// 15, secret UNKNOWN to meshcraft, never authenticated), `admin` and `mcadmin`
// (both proven). A count of survivors says "2 remain" to a deletion that
// takes the operator from TWO proven credentials to ONE, and it is right: two
// accounts do remain. Whether one of them opens the switch is a question about
// the credential vault, and a connector has no vault.
//
// So the honest guarantee is exactly one sentence: THE DEVICE WILL STILL HAVE AN
// ACCOUNT. Anything stronger — "a credential remains that works" — belongs to
// the layer that can see the vault, and must not be implied here by a predicate
// that cannot check it. A guard that suggests a safety property it does not have
// is worse than no guard: it is the one an operator stops thinking behind.
//
// # Why a failed read REFUSES here, where eos and ios proceed
//
// Those two run the guard as `rerr == nil && len(have) <= 1` and both say why in
// one word: the console still works. THIS RANGE HAS NO SERIAL CONSOLE — repair
// of an accounts-less EdgePoint S16 is the front-panel reset button, on site,
// and it costs the configuration. The asymmetry decides it: a deletion refused
// leaves a stale account that the caller already reports (an open door, visible
// and reversible), a deletion allowed on a read nobody could make can cost a
// site visit. And this read fails only when BOTH sources failed — a session in
// that state is not one to write accounts from.
//
// # The cost, and the one way this read is wrong
//
// One short command in the normal case ("show users accounts detail"); the
// 90-second running-config read only on a firmware that lacks it. When that
// fallback answers, the list is known-INCOMPLETE on this platform — FASTPATH
// hides the factory account from the configuration — so the count can only be
// too LOW, never too high. It errs into refusing a deletion that was fine,
// which is the direction a guard is allowed to be wrong in.
func deleteAccountFrom(ctx context.Context, run cmdRunner, host, sessionUser, user string) error {
	if strings.EqualFold(user, sessionUser) {
		return fmt.Errorf("refusing to delete %q: it is the account of the current session", user)
	}
	var have, source, rerr = readAccountsFrom(ctx, run, host)
	if rerr != nil {
		return fmt.Errorf("refusing to delete %q on %s: the accounts could not be read, so "+
			"nothing here can tell whether it is the last one — and this range has no serial "+
			"console, so a switch left without an account is repaired by the reset button on "+
			"site: %w", user, host, rerr)
	}
	var remaining = accountsExcept(have, user)
	if len(remaining) == 0 {
		return fmt.Errorf("refusing to delete %q on %s: \"%s\" enumerates it as the only "+
			"administration account, and this range has no serial console — a switch with no "+
			"account is repaired with the front-panel reset button on site, at the cost of its "+
			"configuration", user, host, source)
	}
	var err = runBlockWith(ctx, run, []string{"configure", "no username " + user, "exit"})
	if err != nil {
		return fmt.Errorf("deleting account %s on %s: %w", user, host, err)
	}
	return nil
}

// accountsExcept is the account list as it would read once `user` is gone.
//
// It answers what SURVIVES, not what is there, and that is deliberate: the
// question a deletion has to answer is about the state it leaves behind. What
// the survivors are WORTH — whether anybody holds a secret that opens one — is
// not in this list and cannot be put there. See [deleteAccountFrom].
//
// THE PREDICATE ITSELF MOVED to [driver.AccountsExcept] on 2026-08-12, when
// instanton needed the same guard: two connectors refusing a deletion for "the
// same" reason had to be refusing it for literally the same reason, and this
// file's header is already an essay on what happens when one enumeration drifts
// from another. This name stays because this package's prose refers to it.
func accountsExcept(have []Account, user string) []Account {
	return driver.AccountsExcept(have, user)
}

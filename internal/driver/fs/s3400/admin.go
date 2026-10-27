package s3400

// Local accounts and the session's own secret.
//
// Captured 2026-08-03 in the running configuration of a provisioned unit:
//
//	aaa authentication login default local
//	aaa authentication enable default none
//	username admin password 0 <secret>
//	enable password 0 <secret> level 15
//
// Two consequences the connector is built around:
//
//   - the account form is "username U password S" — there is no privilege
//     keyword on this platform (privilege is a property of the enable levels);
//   - "aaa authentication enable default none" is why "enable" asks for
//     nothing, even though an enable password IS set. Removing the enable
//     password is therefore NOT what locks a switch down here, and this
//     connector does not pretend otherwise.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

var reCfgUsername = regexp.MustCompile(`(?im)^\s*username\s+(\S+)\s+password\s`)

// Accounts lists the local users the configuration carries.
//
// # EVERY ACCOUNT COMES BACK WITH AN UNKNOWN PRIVILEGE, AND THAT IS THE TRUTH
//
// This is the one range in the fleet where the account line says NOTHING about
// rights — "username admin password 0 <secret>", full stop, as the capture at
// the top of this file and parse_test.go's labRunningConfig both show. There is
// no privilege keyword on this platform: privilege is a property of the ENABLE
// LEVELS, not of the account, and the configuration expresses it elsewhere
// ("enable password 0 <secret> level 15").
//
// So [driver.PrivilegeUnknown] it is, and the temptation to do better is worth
// naming because the information is RIGHT THERE in the same read: this
// configuration also carries "aaa authentication enable default none", which
// means every account can reach level 15 without being asked for anything, and
// one could argue that makes them all administrators. That inference is about
// the SWITCH's authentication settings, not about the account, and it would
// answer "is this account privileged?" with a fact about a different subject.
// A caller acting on it would delete or rotate accounts on the strength of a
// deduction this connector invented. The honest answer is the one the audit can
// work with: the device did not say, and reading the enable levels to find out
// is a question — a real one — that nobody has asked this method to answer.
//
// # A TRUNCATED READ IS REFUSED, NOT ENUMERATED
//
// Keyed on [isTruncated] — "this output is a PREFIX" — and not on the presence
// of an error, which is the same test [Client.ReadState] has run since the
// 2026-08-11 phantom-ports incident (state.go) and which was missing HERE until
// 2026-08-12. [parseCfgAccounts] cannot notice a prefix: it reports the
// "username" lines it reached and is silent about the rest, so a "show
// running-config" cut anywhere in the file used to leave this method as a
// complete-looking account list that is merely SHORT. That is the defect class
// that reported 23 of 52 ports as a whole switch, applied to the one enumeration
// a lockout is decided on ([Client.DeleteAccount] below).
//
// On this range the cut is not exotic: every read feeds a pager one screen at a
// time ([cliConn.runPaged]), so a deadline, a dead session, a cancelled context
// or maxPages all land mid-configuration by construction.
//
// The condition is a strict superset of the emptiness test it replaces —
// everything refused before is still refused — and an in-band CLI refusal is
// still NOT a truncation: it arrives after the prompt, on output the device
// finished writing, and this connector has always been entitled to parse around
// one.
func (c *Client) Accounts(ctx context.Context) ([]driver.Account, error) {
	var out, err = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if err != nil && (strings.TrimSpace(out) == "" || isTruncated(err)) {
		return nil, fmt.Errorf("show running-config on %s: %w", c.host, err)
	}
	return parseCfgAccounts(out), nil
}

// parseCfgAccounts reads the "username" lines. Pure — replayed against the
// lab capture. Sorted, because two runs must return the same list or comparing
// two observations means nothing.
func parseCfgAccounts(cfg string) []driver.Account {
	var seen = map[string]bool{}
	var users []string
	var m []string
	for _, m = range reCfgUsername.FindAllStringSubmatch(cfg, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		users = append(users, m[1])
	}
	sort.Strings(users)
	var out []driver.Account
	var u string
	for _, u = range users {
		out = append(out, driver.AccountAtLevel(u, driver.PrivilegeUnknown,
			"show running-config"))
	}
	return out
}

// EnsureAccount creates or updates a local account.
//
// The same command does both — "username U password S" overwrites an
// existing entry — so there is no create/update distinction to get wrong.
func (c *Client) EnsureAccount(ctx context.Context, user, secret string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	if !validAccountName(user) {
		return fmt.Errorf("account name %q rejected: this CLI takes a bare word", user)
	}
	if strings.ContainsAny(secret, " \t\r\n") {
		return fmt.Errorf("secret rejected: this CLI cannot carry whitespace in a password")
	}
	err = c.configBlock(ctx, []string{
		fmt.Sprintf("username %s password %s", user, secret),
		// Without a login method the account guards nothing. The captured
		// configuration already carries this line; sending it is idempotent
		// and makes the connector safe on a device that lost it.
		"aaa authentication login default local",
	})
	if err != nil {
		c.step(false, "account not written: "+user, firstLine(err.Error()))
		return fmt.Errorf("username %s on %s: %w", user, c.host, err)
	}
	c.step(true, "account written: "+user, "\"username … password …\" + login authentication local")
	return nil
}

// DeleteAccount removes a local account.
//
// # THREE REFUSALS, and only the first one predates 2026-08-12
//
// Until then this method compared the target against the CURRENT SESSION's name
// and nothing else — so "delete admin while logged in as mcadmin" went
// straight through. That is the shape `instanton` carried until the same day and
// the shape a real deletion was halted on next door (`ubiquiti/edgeswitch`, see
// TODO.md), and this connector was the LAST one in the tree still carrying it.
// Beyond these refusals the method still deletes what it is told to, including
// an account this connector never created: deciding which accounts deserve to go
// is the caller's job, not a rule this file may invent.
//
// # THE PREDICATE IS SHARED: [driver.AccountsExcept]
//
// "What would REMAIN", not "how many are there": the enumeration is filtered
// of the account about to go and the deletion is refused when nothing survives
// it. That is `len(have) <= 1` made exact, and it also lets through the delete of
// a name the device does not enumerate at all, which removes nothing and cannot
// be the last of anything. `edgeswitch`, `instanton` and `fs/s3900` refuse for
// "the same" reason and therefore refuse for literally the same reason — a
// fourth variant written here would be a fourth thing to keep in step.
//
// It is also the ONLY predicate available on this range. `mikrotik/routeros`
// counts survivors among ENABLED accounts only, the strictest in the tree; here
// the account line states nothing at all — "username U password S", no
// privilege keyword, no enable/disable flag (cf. [Client.Accounts]) — so there is
// no attribute to narrow the count by, and the captured "aaa authentication
// enable default none" means every one of these accounts reaches level 15
// without being asked for anything anyway.
//
// # Why a failed read REFUSES, ON A RANGE THAT HAS A CONSOLE
//
// `arista/eos` and `cisco/ios` run the guard as `rerr == nil && len(have) <= 1`
// and justify failing OPEN in one word: the console still works. That word does
// apply here — the S3400 carries an RJ45 console at 115200 8N1
// ([Driver.Capabilities] declares it OutOfBand, and portadmin.go names it as the
// way back from a port shut on the jack the session rides). It is still not
// enough, for the same two reasons the S3900 settled on next door, the second of
// which is STRONGER on this range rather than merely transferred.
//
// FIRST: a console is a way to TYPE, not a way to AUTHENTICATE. This platform's
// own [Driver.ResetProcedure] is the record: the way back from an account-less
// unit is "115200 8N1 console", then the FACTORY login admin/admin — a login
// that exists only AFTER `delete startup-config` + reboot, because the software
// path needs a privileged CLI session, which needs an account. The price of a
// lockout is therefore the startup configuration, exactly what the console-less
// ranges pay, merely through a different button; and it is a delicate button
// here — the reboot prompt that offers to save the running configuration must be
// answered NO, or the startup file just deleted is written straight back.
//
// That procedure also notes that a console line WITHOUT authentication lands
// straight at "Switch>". That is the factory shape, and this guard does not
// read "aaa authentication login default local" out of the configuration to
// decide whether it still holds: a unit meshcraft provisioned carries that line
// (the 2026-08-03 capture does, and [Client.EnsureAccount] re-sends it on
// purpose), so the console asks for the same credentials the SSH session does.
// Concluding "the console will let someone in regardless" from a config line is
// the same class of inference [Client.Accounts] refuses to make about privilege —
// a fact about the switch's authentication settings answering a question about
// the way back in — and here it would be wagered against the startup config.
//
// SECOND, and this is what decides it: ON THIS CONNECTOR A FAILED READ IS A
// FAILED SESSION. [cliConn.runPaged] wraps every early exit in [errTruncated] —
// the device closed the session, the deadline expired, the context was cancelled,
// the pager was still asking after maxPages — and [Client.Accounts] refuses
// those. A session in that state is not one to write accounts from: "no username
// X" sent into it is a command whose acceptance nobody can observe, and the
// success line below would be reporting a deletion this connector never saw
// confirmed. Failing open would not buy a deletion, it would buy an UNVERIFIED
// one — and the thing left unverified is the last-account question itself.
//
// The pager makes that concrete in a way the S3900 does not have to reckon with.
// A read that stopped while a "--More--" was pending leaves the pager pending,
// and this firmware's pager EATS THE FIRST CHARACTER of whatever is typed next
// (captured 2026-08-03: "show …" arrived as "how …"). So the next thing
// [Client.configBlock] writes — "config" — would reach the device as "onfig".
// A deletion sent into a truncated session on this range is not merely
// unobservable, it is odds-on mistyped, and "no username admin" landing as "o
// username admin" is a command whose failure mode nobody has surveyed. Refusing
// costs a stale account: visible, reversible, and reported to the caller.
//
// # "Could not read" IS distinguishable from "no accounts" here
//
// The question `instanton` had to answer NO to — there a `wcd` GET answers an
// unrecognised section with HTTP 200 + `statusCode -1`, parsing to zero accounts
// and no error, so nothing downstream can tell the two apart. HERE THEY ARE
// DISTINGUISHABLE, by two separate mechanisms: a read that never reached a prompt
// carries [errTruncated], and a command the CLI REFUSED comes back after the
// prompt — a COMPLETE answer that happens to be a refusal.
//
// THE MARKER IS NOT THE S3900's, and copying its sentence would have been wrong:
// this CLI does not prefix its errors with '%'. It echoes the offending word, a
// caret line, then a bare sentence — "Unknown command", "Incomplete command",
// "Parameter invalid", "Too many parameters", all captured on Version 2.0.2J
// and all matched by reCLIError (ssh.go). The mechanism transfers; the text does
// not.
//
// An empty enumeration is nonetheless a refusal of its own rather than a case
// folded into the last-account one, because the second mechanism lands there: a
// "show running-config" the CLI refused parses to zero accounts, and so does a
// read answered by something that is not the running configuration. Either way
// THIS SESSION IS OPEN and it was opened under an account, so a running
// configuration naming no "username" at all did not answer with the account
// list. An empty enumeration here is an unreadable one, not an account-less
// switch — and a list that cannot see the session's own account cannot say what
// would remain.
//
// # WHAT THIS GUARD DOES NOT PROMISE, and the wording matters more than the code
//
// It does NOT promise a way back in. It counts accounts the DEVICE enumerates in
// "show running-config"; it cannot know which of their secrets meshcraft holds,
// which have ever been authenticated, or which is a factory password left by a
// previous owner — an S3400 out of the box carries admin/admin (its own
// [Driver.ResetProcedure] says so, and says it is NOT admin/blank) and this guard
// would count it as a survivor, exactly as it counts an account whose secret
// nobody in the vault has. The honest guarantee is one sentence: THE DEVICE WILL
// STILL HAVE AN ACCOUNT. Anything stronger — "a credential remains that works"
// — is a question about the vault, which no connector can see, and must not be
// implied by a predicate that cannot check it: a guard that suggests a safety
// property it does not have is the one an operator stops thinking behind. The
// refusals and the success transcript line all say so on purpose.
//
// COST: one "show running-config", on the session that is already open. No new
// session and no new command form — it is the read [Client.Accounts] already
// exposes and [Client.ReadState] already pays for elsewhere.
func (c *Client) DeleteAccount(ctx context.Context, user string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	if strings.EqualFold(user, c.User) {
		// FREE: nothing is sent, not even the read below.
		return fmt.Errorf("refusing to delete %q: it is the account this session is "+
			"logged in with", user)
	}
	if !validAccountName(user) {
		// Also free, and sound before the read: a name this CLI would not take
		// cannot be a name it enumerates.
		return fmt.Errorf("account name %q rejected: this CLI takes a bare word", user)
	}

	var have, rerr = c.Accounts(ctx)
	if rerr != nil {
		var refusal = fmt.Errorf("refusing to delete %q on %s: the local accounts could not "+
			"be read, so nothing here can tell whether it is the last one — and a read that "+
			"failed on this connector is a SESSION that failed, which is not one to send "+
			"\"no username\" into either (an unfed pager also eats the first character of the "+
			"next command). A switch left with no account is recovered on site through the "+
			"console port and the factory reset, at the cost of its startup configuration: %w",
			user, c.host, rerr)
		c.step(false, "account not removed: "+user, firstLine(refusal.Error()))
		return refusal
	}
	if len(have) == 0 {
		var refusal = fmt.Errorf("refusing to delete %q on %s: \"show running-config\" "+
			"answered completely and named no \"username\" line at all, while THIS SESSION IS "+
			"OPEN — the device holds at least the account it was opened under, so this read did "+
			"not answer with the account list. An empty enumeration here is an unreadable one, "+
			"not an account-less switch", user, c.host)
		c.step(false, "account not removed: "+user, firstLine(refusal.Error()))
		return refusal
	}
	var remaining = driver.AccountsExcept(have, user)
	if len(remaining) == 0 {
		var refusal = fmt.Errorf("refusing to delete %q on %s: \"show running-config\" "+
			"enumerates it as the ONLY local account. This counts what the DEVICE enumerates, "+
			"not which secrets the vault holds, so it cannot promise a way back in — it only "+
			"refuses to take the last one away, because a switch with no account is recovered "+
			"on site through the console port and the factory reset, at the cost of its startup "+
			"configuration", user, c.host)
		c.step(false, "account not removed: "+user, firstLine(refusal.Error()))
		return refusal
	}

	err = c.configBlock(ctx, []string{"no username " + user})
	if err != nil {
		c.step(false, "account not removed: "+user, firstLine(err.Error()))
		return fmt.Errorf("no username %s on %s: %w", user, c.host, err)
	}
	c.step(true, "account removed: "+user,
		fmt.Sprintf("%d account(s) still enumerated by \"show running-config\" — which is NOT "+
			"a promise that anyone holds a secret that opens one: this counts what the device "+
			"enumerates, not what the vault can prove", len(remaining)))
	return nil
}

// SetSecret changes the password of the account this session uses, and the
// enable password with it.
//
// The two are kept together on purpose: this platform's "enable" asks for
// nothing by default, so leaving a stale enable password behind would be a
// silent inconsistency between what meshcraft stores and what the device
// holds. The session itself is NOT re-authenticated — SSH keeps the channel
// it already has, which is what lets a caller verify the change afterwards.
func (c *Client) SetSecret(ctx context.Context, old, secret string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	if strings.ContainsAny(secret, " \t\r\n") {
		return fmt.Errorf("secret rejected: this CLI cannot carry whitespace in a password")
	}
	err = c.configBlock(ctx, []string{
		fmt.Sprintf("username %s password %s", c.User, secret),
		fmt.Sprintf("enable password 0 %s level 15", secret),
	})
	if err != nil {
		c.step(false, "secret not changed", firstLine(err.Error()))
		return fmt.Errorf("changing the secret of %s on %s: %w", c.User, c.host, err)
	}
	c.step(true, "secret changed for "+c.User,
		"enable password aligned — note that \"enable\" asks for nothing on this "+
			"platform by default (aaa authentication enable default none)")
	return nil
}

func validAccountName(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	var r rune
	for _, r = range s {
		var ok = (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.'
		if !ok {
			return false
		}
	}
	return true
}

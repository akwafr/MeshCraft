package s3900

// Administration accounts, session secret, and persistence.
//
// Saving on 1.7.0 is not a simple "write": "copy running-config
// startup-config" replies "Startup configuration file name:" and waits for a
// NAME (captured in the lab AND in the 2026-08-02 deployment). The answer is
// always explicit — accepting a default could overwrite
// Factory_Default_Config.cfg, the file the soft reset relies on.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

var (
	reSavePrompt = regexp.MustCompile(`(?i)startup\s+configuration\s+file\s+name\s*:`)
	reSaveOK     = regexp.MustCompile(`(?i)flash\s+programming\s+completed|\bsuccess\b|\[OK\]`)
	reSaveOrAsk  = regexp.MustCompile(`(?i)startup\s+configuration\s+file\s+name\s*:|flash\s+programming\s+completed|\bsuccess\b|\[OK\]|continue.*[yn]|\(y/n\)`)
	// The name AND the rest of the line. This platform splits ONE account
	// across TWO "username" lines — captured in the lab and pinned in
	// parse_test.go's labRunningConfig:
	//
	//	username admin privilege 15
	//	username admin password 7 aabbccddeeff00112233445566778899
	//
	// which is also the shape [Client.EnsureAccount] writes. So the privilege
	// and the secret never appear on the same line, and an enumeration that
	// kept only the first line per name would report the account as having no
	// stated privilege while the configuration states it plainly. The two rows
	// are merged in [Client.Accounts].
	reRunUsername = regexp.MustCompile(`(?im)^\s*username\s+(\S+)(.*)$`)
	// "privilege 15" inside that tail, searched only before "password":
	// the type-7 blob that follows is text we are parsing and must not be able
	// to promote its own account.
	reRunUserPriv = regexp.MustCompile(`(?i)\bprivilege\s+(\d+)\b`)
)

// save copies the current configuration into the startup configuration.
func (c *Client) save(ctx context.Context) error {
	if c.cli == nil {
		return fmt.Errorf("no open session on this access")
	}
	var werr = c.sendAndExpectSave(ctx, "copy running-config startup-config")
	return werr
}

func (c *Client) sendAndExpectSave(ctx context.Context, cmd string) error {
	var err = c.cli.sendLine(cmd)
	if err != nil {
		return err
	}
	var out string
	out, err = c.cli.waitFor(ctx, saveTimeout, reSaveOrAsk)
	if err != nil {
		return fmt.Errorf("no reaction to the save command: %v", err)
	}
	if reSavePrompt.MatchString(out) {
		// The answer to the prompt is a LINE: the startup file name.
		err = c.cli.sendLine(StartupConfigFile)
		if err != nil {
			return err
		}
		out, err = c.cli.waitFor(ctx, saveTimeout, reSaveOK)
		if err != nil {
			return fmt.Errorf("flash write not confirmed after sending %q: %v",
				StartupConfigFile, err)
		}
	}
	if !reSaveOK.MatchString(out) {
		// y/n from another firmware in the family: confirm, then wait.
		if strings.Contains(strings.ToLower(out), "y/n") || strings.Contains(strings.ToLower(out), "continue") {
			err = c.cli.sendLine("y")
			if err != nil {
				return err
			}
			out, err = c.cli.waitFor(ctx, saveTimeout, reSaveOK)
			if err != nil {
				return fmt.Errorf("flash write not confirmed after y: %v", err)
			}
		}
	}
	if !reSaveOK.MatchString(out) {
		return fmt.Errorf("save finished without a Success marker")
	}
	// Regain control on a clean prompt.
	var _, perr = c.cli.run(ctx, "", cmdTimeout)
	_ = perr
	return nil
}

// Persist — driver.Persister: the platform distinguishes applying from
// persisting (Commit = apply_then_save).
func (c *Client) Persist(ctx context.Context) error {
	var err = c.save(ctx)
	if err != nil {
		c.step(false, "startup-config save failed", firstLine(err.Error()))
		return err
	}
	c.step(true, "configuration persisted", "copy running-config startup-config → "+StartupConfigFile)
	return nil
}

// Accounts lists the administration accounts present (running-config), with the
// privilege the configuration states for each.
//
// SAME SINGLE READ as before the contract widened — "show run" was already
// being fetched whole — and the merge of this platform's two lines per account
// happens here (cf. [reRunUsername]). An account whose configuration carries no
// "privilege" line at all comes back [driver.PrivilegeUnknown] rather than the
// CLI's default: this connector does not substitute a documented default for a
// fact the device did not state.
//
// A TRUNCATED READ IS REFUSED, NOT ENUMERATED — same test as [Client.ReadState]
// (state.go), keyed on [isTruncated] rather than on the error alone, and it was
// missing here until 2026-08-12. [parseRunAccounts] cannot notice a prefix: it
// reports the "username" lines it reached and is silent about the rest, so a
// "show run" cut anywhere in the file used to leave this method as a
// complete-looking account list that is merely SHORT — the phantom-ports defect
// class of 2026-08-11, applied to the one enumeration a lockout is decided on.
// An in-band CLI refusal is NOT a truncation and is still parsed around: it
// arrives after the prompt, on output the device finished writing.
func (c *Client) Accounts(ctx context.Context) ([]driver.Account, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && (runOut == "" || isTruncated(err)) {
		return nil, err
	}
	return parseRunAccounts(runOut), nil
}

// parseRunAccounts reads the "username" lines, MERGING the several a single
// account is spread over. Pure — replayed against the lab capture.
func parseRunAccounts(runOut string) []driver.Account {
	var at = map[string]int{}
	var out []driver.Account
	var m []string
	for _, m = range reRunUsername.FindAllStringSubmatch(runOut, -1) {
		var priv = driver.PrivilegeUnknown
		var head = m[2]
		var cut = strings.Index(strings.ToLower(head), " password ")
		if cut >= 0 {
			head = head[:cut]
		}
		var p = reRunUserPriv.FindStringSubmatch(head)
		if p != nil {
			var n, err = strconv.Atoi(p[1])
			if err == nil {
				priv = n
			}
		}
		var key = strings.ToLower(m[1])
		var i, seen = at[key]
		if !seen {
			at[key] = len(out)
			out = append(out, driver.AccountAtLevel(m[1], priv, "show run"))
			continue
		}
		// The second line for a name that the first one did not rank.
		if out[i].Privilege == driver.PrivilegeUnknown && priv != driver.PrivilegeUnknown {
			out[i].Privilege = priv
			out[i].Authority = driver.AuthorityFromLevel(priv)
		}
	}
	return out
}

// EnsureAccount guarantees that a named account exists and carries this
// secret. Idempotent: "username" creates or modifies depending on what
// already exists — the CLI doesn't distinguish the two verbs. DOES NOT
// PERSIST — sequencing a rename is the caller's responsibility (cf.
// driver.AccountManager).
func (c *Client) EnsureAccount(ctx context.Context, user, secret string) error {
	var werr = c.requireWritableDialect()
	if werr != nil {
		return werr
	}
	if strings.TrimSpace(user) == "" || secret == "" {
		return fmt.Errorf("account name and secret are both required")
	}
	// Forms captured in the lab: privilege first, password next (two commands).
	var err = c.runConfigBlock(ctx, []string{
		fmt.Sprintf("username %s privilege 15", user),
		fmt.Sprintf("username %s password 0 %s", user, secret),
		// enable shares the same secret: it is the only door from user EXEC to
		// privileged mode, and meshcraft stores only one secret per account.
		fmt.Sprintf("enable password 0 %s", secret),
	})
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not written", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q ensured on device", user),
		"privilege 15; enable password aligned on the same secret")
	return nil
}

// DeleteAccount removes an administration account.
//
// THREE REFUSALS, and only the first one predates 2026-08-12. Until then this
// method compared the target against the CURRENT SESSION's name and nothing
// else — so "delete admin while logged in as mcadmin" went straight through,
// which is exactly the shape `instanton` carried until the same day and the
// shape a real deletion was halted on next door (`ubiquiti/edgeswitch`, see
// TODO.md). Beyond these three the method still deletes what it is told to,
// including an account this connector never created: deciding which accounts
// deserve to go is the caller's job, not a rule this file may invent.
//
// # THE PREDICATE IS SHARED: [driver.AccountsExcept]
//
// "What would REMAIN", not "how many are there": the enumeration is filtered
// of the account about to go and the deletion is refused when nothing survives
// it. That is `len(have) <= 1` made exact, and it also lets through the delete of
// a name the device does not enumerate at all, which removes nothing and cannot
// be the last of anything. `edgeswitch` and `instanton` refuse for "the same"
// reason and therefore refuse for literally the same reason — a third variant
// written here would be a fourth thing to keep in step.
//
// # Why a failed read REFUSES, ON A RANGE THAT HAS A CONSOLE
//
// `arista/eos` and `cisco/ios` run the guard as `rerr == nil && len(have) <= 1`
// and justify failing OPEN in one word: the console still works. That word does
// apply to this range at first sight — the S3900 has an RJ45 console port,
// 115200 8N1, and this connector's own portadmin.go names it as the way back
// from a port shut on the jack the session rides. It is still not enough, for
// two reasons about the RECOVERY PATH rather than about symmetry with whichever
// connector was read last.
//
// FIRST: a console is a way to TYPE, not a way to AUTHENTICATE. FSOS puts the
// same login prompt on the serial line as on SSH. What this connector itself
// records as the way back from an account-less device is [Driver.ResetProcedure]:
// "115200 8N1 console", then "login admin/admin" — a login that exists only
// AFTER the reset, because the software path ("boot system
// config:Factory_Default_Config.cfg", then delete the startup file) needs a
// privileged CLI session, which needs an account. So a lockout here is still paid
// for with the startup configuration, the same price the console-less ranges pay,
// merely through a different button. The console shortens the trip; it does not
// make the deletion free.
//
// SECOND, and this is what decides it: ON THIS CONNECTOR A FAILED READ IS A
// FAILED SESSION. [cliConn.run] wraps its early exits in [errTruncated] — the
// device closed the session, the deadline expired, the context was cancelled —
// and [Client.Accounts] refuses those. A session in that state is not one to
// write accounts from: "no username X" sent into it is a command whose
// acceptance nobody can observe, and the success line below would be reporting a
// deletion this connector never saw confirmed. Failing open would therefore not
// buy a deletion, it would buy an UNVERIFIED one — and the thing left unverified
// is the last-account question itself. Refusing costs a stale account: visible,
// reversible, and reported to the caller.
//
// # "Could not read" IS distinguishable from "no accounts" here
//
// The question `instanton` had to answer NO to — there a `wcd` GET answers an
// unrecognised section with HTTP 200 + `statusCode -1`, parsing to zero accounts
// and no error, so nothing downstream can tell the two apart. HERE THEY ARE
// DISTINGUISHABLE, by two separate mechanisms: a read that never reached a prompt
// carries [errTruncated] (the sentinel named by the 2026-08-11 phantom-ports
// incident), and a command the CLI REFUSED comes back after the prompt with
// `% Invalid input` matched by reCLIError — a COMPLETE answer that happens to be
// a refusal. The first is the read-failure refusal above.
//
// An empty enumeration is nonetheless a refusal of its own rather than a case
// folded into the last-account one, because the second mechanism lands there: a
// "show run" the CLI refused parses to zero accounts, and so does a read
// answered by something that is not the running configuration. Either way THIS
// SESSION IS OPEN and it was opened under an account, so a running-config naming
// no "username" at all did not answer with the account list. An empty
// enumeration here is an unreadable one, not an account-less switch — and a list
// that cannot see the session's own account cannot say what would remain.
//
// # WHAT THIS GUARD DOES NOT PROMISE, and the wording matters more than the code
//
// It does NOT promise a way back in. It counts accounts the DEVICE enumerates in
// "show run"; it cannot know which of their secrets meshcraft holds, which have
// ever been authenticated, or which is a factory password left by a previous
// owner — an S3900 out of the box carries `admin`/`admin` and this guard would
// count it as a survivor, exactly as it counts an account whose secret nobody in
// the vault has. The honest guarantee is one sentence: THE DEVICE WILL STILL HAVE
// AN ACCOUNT. Anything stronger — "a credential remains that works" — is a
// question about the vault, which no connector can see, and must not be implied
// by a predicate that cannot check it: a guard that suggests a safety property it
// does not have is the one an operator stops thinking behind. The refusals and
// the success transcript line all say so on purpose.
//
// COST: one "show run", the read every other gesture on this connector already
// pays for, on the session that is already open. No new session, no second
// command.
func (c *Client) DeleteAccount(ctx context.Context, user string) error {
	var werr = c.requireWritableDialect()
	if werr != nil {
		return werr
	}
	if strings.EqualFold(user, c.User) {
		// FREE: nothing is sent, not even the read below.
		return fmt.Errorf("refusing to delete %q: it is the account of the current session", user)
	}

	var have, rerr = c.Accounts(ctx)
	if rerr != nil {
		var refusal = fmt.Errorf("refusing to delete %q on %s: the administration accounts "+
			"could not be read, so nothing here can tell whether it is the last one — and a "+
			"read that failed on this connector is a SESSION that failed, which is not one to "+
			"send \"no username\" into either. A switch left with no account is recovered on "+
			"site through the console port and the factory reset, at the cost of its "+
			"configuration: %w", user, c.host, rerr)
		c.step(false, fmt.Sprintf("account %q NOT deleted", user), firstLine(refusal.Error()))
		return refusal
	}
	if len(have) == 0 {
		var refusal = fmt.Errorf("refusing to delete %q on %s: \"show run\" answered completely "+
			"and named no \"username\" line at all, while THIS SESSION IS OPEN — the device "+
			"holds at least the account it was opened under, so this read did not answer with "+
			"the account list. An empty enumeration here is an unreadable one, not an "+
			"account-less switch", user, c.host)
		c.step(false, fmt.Sprintf("account %q NOT deleted", user), firstLine(refusal.Error()))
		return refusal
	}
	var remaining = driver.AccountsExcept(have, user)
	if len(remaining) == 0 {
		var refusal = fmt.Errorf("refusing to delete %q on %s: \"show run\" enumerates it as "+
			"the ONLY administration account. This counts what the DEVICE enumerates, not "+
			"which secrets the vault holds, so it cannot promise a way back in — it only "+
			"refuses to take the last one away, because a switch with no account is recovered "+
			"on site through the console port and the factory reset, at the cost of its "+
			"configuration", user, c.host)
		c.step(false, fmt.Sprintf("account %q NOT deleted", user), firstLine(refusal.Error()))
		return refusal
	}

	// DEDUCED form (never captured): the CLI's refusal surfaces as-is.
	var err = c.runConfigBlock(ctx, []string{"no username " + user})
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not deleted", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q deleted", user),
		fmt.Sprintf("%d account(s) still enumerated by \"show run\" — which is NOT a promise "+
			"that anyone holds a secret that opens one: this counts what the device "+
			"enumerates, not what the vault can prove", len(remaining)))
	return nil
}

// SetSecret changes the password OF THE ACCOUNT THAT JUST LOGGED IN, and
// PERSISTS (contract: an administration secret that doesn't survive a reboot
// puts the device back on its factory account).
func (c *Client) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("no account has been proven on this device: there is " +
			"no password to change, and meshcraft does not guess the holder name")
	}
	_ = oldSecret // the CLI does not require the old secret: the open session is proof enough
	var err = c.EnsureAccount(ctx, c.User, newSecret)
	if err != nil {
		return err
	}
	return c.Persist(ctx)
}

// ApplyMgmtAddressing writes management addressing ON THE CURRENT VLAN —
// never a VLAN change (that would be MgmtMover, not implemented here).
//
// Forms captured: "interface vlan N" + "ip add A.B.C.D/nn",
// "ip route 0.0.0.0 0.0.0.0 GW", "dns name-server X" (lab running-config).
// The post-write re-read can fail if the address THIS session came in through
// just changed: the result then says so instead of pretending it verified.
func (c *Client) ApplyMgmtAddressing(ctx context.Context, want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {
	var out = driver.MgmtAddrResult{}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	var st, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	var vlanID = mgmtVlanOf(st.Interface)
	if vlanID <= 0 {
		return out, fmt.Errorf("the device declares no management SVI: meshcraft does " +
			"not know which interface to address (a VLAN move is a different gesture)")
	}

	var cmds []string
	switch want.Mode {
	case driver.MgmtAddrModeDHCP:
		cmds = append(cmds, fmt.Sprintf("interface vlan %d", vlanID), "ip add dhcp", "exit")
	case driver.MgmtAddrModeStatic:
		if !strings.Contains(want.Address, "/") {
			return out, fmt.Errorf("static address must carry its prefix (A.B.C.D/nn)")
		}
		cmds = append(cmds, fmt.Sprintf("interface vlan %d", vlanID),
			"ip add "+want.Address, "exit")
	default:
		return out, fmt.Errorf("unknown addressing mode %q", want.Mode)
	}
	if strings.TrimSpace(want.Gateway) != "" {
		cmds = append(cmds, "ip route 0.0.0.0 0.0.0.0 "+strings.TrimSpace(want.Gateway))
	}
	var dns string
	for _, dns = range want.DNS {
		if strings.TrimSpace(dns) != "" {
			cmds = append(cmds, "dns name-server "+strings.TrimSpace(dns))
		}
	}

	err = c.runConfigBlock(ctx, cmds)
	if err != nil {
		return out, err
	}
	c.step(true, "management addressing written", strings.Join(cmds, " ; "))

	var after driver.MgmtAddrState
	after, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		out.Notes = append(out.Notes,
			"post-write re-read failed ("+firstLine(err.Error())+") — if the session address "+
				"just changed, this is expected: probe the NEW address to confirm")
		return out, nil
	}
	out.Observed = after
	return out, nil
}

func mgmtVlanOf(iface string) int {
	var re = regexp.MustCompile(`(?i)^vlan\s*(\d+)$`)
	var m = re.FindStringSubmatch(strings.TrimSpace(iface))
	if m == nil {
		return 0
	}
	var n = 0
	var _, err = fmt.Sscanf(m[1], "%d", &n)
	if err != nil {
		return 0
	}
	return n
}

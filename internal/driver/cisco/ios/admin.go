package ios

// Administration accounts, session secret, and persistence.
//
// # Saving, and why it is not a one-liner
//
// "write memory" usually answers "Building configuration... / [OK]" and
// nothing else. Usually. Two prompts can appear instead, and neither is a
// failure:
//
//   - "Warning: Attempting to overwrite an NVRAM configuration previously
//     written by a different version of the system image. Overwrite the
//     previous NVRAM configuration?[confirm]" — after an IOS upgrade;
//   - "Destination filename [startup-config]?" — the fallback form
//     "copy running-config startup-config" always asks it.
//
// A save that waits for a prompt it never sees leaves the CLI sitting at the
// question, and the NEXT command is then typed as the answer. That exact
// failure mode cost the FS connector a whole deployment (its "show run" was
// typed in as a startup file name), so this one answers prompts by name rather
// than sleeping through them.
//
// # Accounts, and the one asymmetry with the FS connector
//
// IOS distinguishes `password` from `secret`: the first stores the value
// recoverably (type 7, trivially reversible), the second stores a hash. Only
// `secret` is ever written here. It costs nothing and it is the difference
// between a configuration backup that leaks the fleet's credentials and one
// that does not.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

var (
	// reSaveOK: the marks that say the write reached NVRAM.
	reSaveOK = regexp.MustCompile(`(?i)\[OK\]|bytes copied|Building configuration`)
	// reSaveAsk: the prompts a save can put up, all answered with a bare CR.
	reSaveAsk = regexp.MustCompile(`(?i)\[confirm\]|Destination filename[^\n]*\?`)
	// reSaveAny: what the reader waits for — an answer, a question, a refusal,
	// or the prompt coming back.
	reSaveAny = regexp.MustCompile(`(?i)\[OK\]|bytes copied|\[confirm\]|Destination filename[^\n]*\?|` +
		reCLIError.String() + `|` + rePromptPriv.String())
)

// save copies the running configuration into the startup configuration,
// answering whatever it is asked along the way.
func (c *Client) save(ctx context.Context) error {
	if c.cli == nil {
		return fmt.Errorf("no open session on this access")
	}
	var err = c.sendAndExpectSave(ctx, SaveCmd)
	if err == nil {
		return nil
	}
	// "write memory" is deprecated on some IOS-XE builds and answers
	// "% Invalid input". The documented replacement is the copy form, which
	// always asks for a destination file name.
	var fbErr = c.sendAndExpectSave(ctx, SaveCmdFallback)
	if fbErr == nil {
		c.step(true, "saved with the fallback form", SaveCmdFallback+" ("+SaveCmd+": "+firstLine(err.Error())+")")
		return nil
	}
	return fmt.Errorf("%s: %v ; %s: %v", SaveCmd, err, SaveCmdFallback, fbErr)
}

func (c *Client) sendAndExpectSave(ctx context.Context, cmd string) error {
	var err = c.cli.sendLine(cmd)
	if err != nil {
		return err
	}
	var sawOK = false
	// Bounded: a device that keeps asking questions must not spin forever.
	var round int
	for round = 0; round < 6; round++ {
		var out string
		out, err = c.cli.waitFor(ctx, saveTimeout, reSaveAny)
		if err != nil {
			return fmt.Errorf("no reaction to the save command: %v", err)
		}
		var refusal = reCLIError.FindString(out)
		if refusal != "" {
			return fmt.Errorf("%s", strings.TrimSpace(refusal))
		}
		if reSaveOK.MatchString(out) {
			sawOK = true
		}
		if reSaveAsk.MatchString(out) {
			// Every one of these prompts takes the default, which is the one we
			// want: confirm the overwrite, keep the name "startup-config".
			err = c.cli.sendLine("")
			if err != nil {
				return err
			}
			continue
		}
		if rePromptPriv.MatchString(tail(out, 200)) {
			if sawOK {
				return nil
			}
			return fmt.Errorf("save returned to the prompt without an [OK] marker")
		}
	}
	return fmt.Errorf("save kept prompting after 6 answers — giving up rather than " +
		"typing the next command into a question")
}

// Persist — driver.Persister: the platform distinguishes applying from
// persisting (Commit = apply_then_save).
func (c *Client) Persist(ctx context.Context) error {
	var err = c.save(ctx)
	if err != nil {
		c.step(false, "startup-config save failed", firstLine(err.Error()))
		return err
	}
	c.step(true, "configuration persisted", SaveCmd)
	return nil
}

// Accounts lists the local administration accounts (running-config), with the
// privilege each "username" line states.
//
// SAME SINGLE READ as before the contract widened: the privilege is on the
// same line as the name, in a configuration this method was already fetching
// whole. An account whose line carries no "privilege" keyword comes back
// [driver.PrivilegeUnknown] rather than IOS's documented default of 1 — see
// [parseRunUsername] for why guessing it would be the wrong kind of helpful.
func (c *Client) Accounts(ctx context.Context) ([]driver.Account, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("no open session on this access")
	}
	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return nil, err
	}
	return parseRunningConfig(runOut, c.host).Accounts, nil
}

// EnsureAccount guarantees that a named account exists, carries this secret,
// and can actually administer the device.
//
// THREE commands and not one, and the third is the one people forget:
//
//   - "username U privilege 15 secret 0 S" creates or replaces the account.
//     IOS does not distinguish the two verbs, which makes this idempotent;
//   - "no enable secret" / "no enable password" apply the fleet's universal
//     rule (cf. [NoEnableCmds]). This REPLACED an "enable secret 0 S" that
//     aligned privileged mode on the account's own secret. The replacement makes
//     `privilege 15` above load-bearing rather than merely convenient: with no
//     enable password set, IOS refuses "enable" from a VTY outright
//     ("% No password set"), so an account that landed on ">" could not leave
//     it over the network. Which is why the two lines are in THIS order, in one
//     block;
//   - "line vty 0 15 / login local" makes the device consult that account at
//     all. A switch left on line passwords ignores every username in its
//     configuration, and the account would look present and open nothing.
//
// DOES NOT PERSIST — sequencing a rename is the caller's responsibility (cf.
// driver.AccountManager).
func (c *Client) EnsureAccount(ctx context.Context, user, secret string) error {
	var werr = c.requireWritableDialect()
	if werr != nil {
		return werr
	}
	if strings.TrimSpace(user) == "" || secret == "" {
		return fmt.Errorf("account name and secret are both required")
	}
	var cmds = []string{
		fmt.Sprintf("username %s privilege 15 secret 0 %s", user, secret),
	}
	// The fleet rule, applied AFTER the privilege-15 account exists (cf.
	// NoEnableCmds). The order is not cosmetic on this platform: with no enable
	// password set, IOS REFUSES "enable" from a VTY ("% No password set"), so
	// `privilege 15` on the account is the only remaining path to a privileged SSH
	// session — it has to be in place first.
	cmds = append(cmds, NoEnableCmds()...)
	cmds = append(cmds,
		"line vty 0 15",
		"login local",
		"exit",
	)
	var err = c.runConfigBlock(ctx, cmds)
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not written", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q ensured on device", user),
		"privilege 15, secret (hashed, not type 7); NO enable password (fleet rule) — "+
			"which is why privilege 15 matters here: IOS refuses \"enable\" from a vty "+
			"when none is set; vty set to login local")
	return nil
}

// DeleteAccount removes a local administration account.
func (c *Client) DeleteAccount(ctx context.Context, user string) error {
	var werr = c.requireWritableDialect()
	if werr != nil {
		return werr
	}
	if strings.EqualFold(user, c.User) {
		return fmt.Errorf("refusing to delete %q: it is the account of the current session", user)
	}
	// Removing the LAST local account on a device set to "login local" locks
	// everyone out of the vty lines — the console still works, which is the
	// only reason this is a refusal to check rather than an outright ban.
	var have, rerr = c.Accounts(ctx)
	if rerr == nil && len(have) <= 1 {
		return fmt.Errorf("refusing to delete %q: it is the only local account, and the vty "+
			"lines are set to \"login local\" — removing it would leave console access only", user)
	}
	var err = c.runConfigBlock(ctx, []string{"no username " + user})
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not deleted", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q deleted", user))
	return nil
}

// SetSecret changes the password OF THE ACCOUNT THAT JUST LOGGED IN, and
// PERSISTS (contract: an administration secret that does not survive a reboot
// puts the device back on whatever the startup configuration carries).
func (c *Client) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("no account has been proven on this device: there is " +
			"no password to change, and meshcraft does not guess the holder name")
	}
	_ = oldSecret // IOS does not require the old secret: the open session is proof enough
	var err = c.EnsureAccount(ctx, c.User, newSecret)
	if err != nil {
		return err
	}
	return c.Persist(ctx)
}

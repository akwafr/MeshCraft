package eos

// Administration accounts, session secret, and persistence.
//
// # Saving, and why it is SIMPLER here than next door — but not simple enough to
// # write blind
//
// cisco/ios has a whole prompt-answering loop for its save, and it earned it:
// IOS's `copy running-config startup-config` always asks "Destination filename
// [startup-config]?", and an NVRAM written by a different image adds an
// "[confirm]" on top. A save that waits for a prompt it never sees leaves the CLI
// sitting at the question, and the NEXT command is typed as the answer — a
// failure that cost the FS connector a whole deployment.
//
// EOS asks NEITHER of those. `write memory` answers "Copy completed successfully."
// and returns to the prompt; the copy form behaves the same way. So the loop
// below is deliberately smaller than the Cisco one — and it is still a loop,
// because the cost of being wrong is asymmetric: if some train does put up a
// question, sleeping through it turns the next command into an answer, and this
// connector's next command is frequently `show running-config`. Answering a
// prompt that never comes costs one stray Enter, which EOS ignores.
//
// # Accounts, and the word EOS actually consults
//
// `privilege 15` alone is NOT enough on this platform, and that is the trap for
// anyone arriving from IOS. EOS decides what an account may do from its ROLE
// (`network-admin`, `network-operator`), and an account created with a privilege
// level and no role logs in fine and can configure nothing. Both are written
// here: the privilege level because the CLI's own prompt level keys off it, the
// role because that is what authorisation reads.
//
// `secret` and not `password`, for the same reason as everywhere: the second
// stores the value reversibly in the configuration, and the difference is between
// a configuration backup that leaks the fleet's credentials and one that does not.

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

var (
	// reSaveOK: the marks that say the write reached the startup configuration.
	reSaveOK = regexp.MustCompile(`(?i)Copy completed successfully|\[OK\]|bytes copied`)
	// reSaveAsk: a prompt a save could put up. EOS is not documented to ask any
	// of these — the pattern exists so that if one appears it is ANSWERED rather
	// than waited out (cf. the header).
	reSaveAsk = regexp.MustCompile(`(?i)\[confirm\]|Destination filename[^\n]*\?|\?\s*\[yes/no\]`)
	// reSaveAny: what the reader waits for — an answer, a question, a refusal, or
	// the prompt coming back.
	reSaveAny = regexp.MustCompile(`(?i)Copy completed successfully|\[OK\]|bytes copied|` +
		`\[confirm\]|Destination filename[^\n]*\?|` +
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
	var fbErr = c.sendAndExpectSave(ctx, SaveCmdFallback)
	if fbErr == nil {
		c.step(true, "saved with the fallback form",
			SaveCmdFallback+" ("+SaveCmd+": "+firstLine(err.Error())+")")
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
	// Bounded: a device that keeps asking questions must not spin for ever.
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
			return fmt.Errorf("save returned to the prompt without a completion marker")
		}
	}
	return fmt.Errorf("save kept prompting after 6 answers — giving up rather than typing " +
		"the next command into a question")
}

// Persist — driver.Persister: the platform distinguishes applying from persisting
// (Commit = apply_then_save).
func (c *Client) Persist(ctx context.Context) error {
	var err = c.save(ctx)
	if err != nil {
		c.step(false, "startup-config save failed", firstLine(err.Error()))
		return err
	}
	c.step(true, "configuration persisted", SaveCmd)
	return nil
}

// Accounts lists the local administration accounts (running configuration),
// with the privilege AND the role each "username" line states.
//
// SAME SINGLE READ as before the contract widened: both words are on the same
// line as the name, in a configuration this method was already fetching whole.
// The role is the one EOS authorisation consults — see [parseRunUsername].
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

// accountCmds is the block that creates-or-replaces an administration account.
//
// THREE things and not one, and the middle one is what an IOS hand leaves out:
//
//   - "username U privilege 15 role network-admin secret 0 S" creates or
//     replaces the account. EOS does not distinguish the two verbs, which makes
//     this idempotent. The ROLE is what authorisation actually consults (cf. the
//     file header) — privilege alone yields an account that logs in and can
//     configure nothing;
//
//   - "no enable password" applies the fleet's universal rule (cf.
//     [NoEnableCmds]). This REPLACED an "enable secret 0 S" that used to align
//     privileged mode on the account's own secret.
//
//     THE REASON GIVEN HERE FOR THE REPLACEMENT WAS WRONG, and it was wrong in
//     the direction that would have justified doing nothing: it said an account
//     carrying `privilege 15 role network-admin` lands on "#" and never sees an
//     enable prompt, so the old line guarded a door the administrator was already
//     through. Measured on core01.lab, that account answers "Current
//     privilege level is 1" at login — the box carries no `aaa authorization
//     exec default local`, so the enable step is on the NORMAL path here.
//     [NoEnableCmds] holds the transcript.
//
//     The replacement is still the stronger of the two, for the reason that
//     survived the measurement: with no enable password set, `enable` grants
//     privilege 15 without asking, whereas the old line added a second secret to
//     keep in step with the first and a way to strand automation at ">";
//
//   - "management ssh" / "no shutdown" makes sure the door this account will
//     be used through is open. EOS runs SSH by default, so this is idempotent on
//     every switch that has not been deliberately closed — and on the one that
//     has, it is the difference between an account that exists and an account
//     that can be used.
func accountCmds(user, secret string) []string {
	var out = []string{
		fmt.Sprintf("username %s privilege 15 role network-admin secret 0 %s", user, secret),
	}
	// The rule, applied AFTER the account exists: at no point is the device left
	// without a privileged way in.
	out = append(out, NoEnableCmds()...)
	return append(out,
		"management ssh",
		"no shutdown",
		"exit",
	)
}

// EnsureAccount guarantees that a named account exists, carries this secret, and
// can actually administer the device.
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
	var cmds = accountCmds(user, secret)
	var err = c.runConfigBlock(ctx, cmds)
	if err != nil {
		// The refusal itself can quote the command back, secret and all. Only the
		// first line is kept and the secret is not re-stated by us.
		c.step(false, fmt.Sprintf("account %q not written", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q ensured on device", user), redactSecrets(cmds))
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
	// Removing the LAST local account leaves nobody able to open an SSH session.
	// The console still works, which is the only reason this is a refusal to check
	// rather than an outright ban — and on this platform the console is a real
	// recourse, since every model in the range has one.
	var have, rerr = c.Accounts(ctx)
	if rerr == nil && len(have) <= 1 {
		return fmt.Errorf("refusing to delete %q: it is the only local account, and removing "+
			"it would leave console access only", user)
	}
	var err = c.runConfigBlock(ctx, []string{"no username " + user})
	if err != nil {
		c.step(false, fmt.Sprintf("account %q not deleted", user), firstLine(err.Error()))
		return err
	}
	c.step(true, fmt.Sprintf("account %q deleted", user))
	return nil
}

// SetSecret changes the password OF THE ACCOUNT THAT JUST LOGGED IN, and PERSISTS
// (contract: an administration secret that does not survive a reboot puts the
// device back on whatever the startup configuration carries).
func (c *Client) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	if strings.TrimSpace(c.User) == "" {
		return fmt.Errorf("no account has been proven on this device: there is no password " +
			"to change, and meshcraft does not guess the holder name")
	}
	_ = oldSecret // EOS does not require the old secret: the open session is proof enough
	var err = c.EnsureAccount(ctx, c.User, newSecret)
	if err != nil {
		return err
	}
	return c.Persist(ctx)
}

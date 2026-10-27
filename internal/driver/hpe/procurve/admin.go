package procurve

// The administration secret.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SetSecret changes the password OF THE ACCOUNT THAT JUST GOT IN.
//
// The account is not a parameter, by contract: it is [Client.Account], and
// letting the caller name another would open the door to "change the password of
// an account we have not proven we can access".
//
// # The form, and the level that matters
//
// This range has two privilege levels and only one of them can configure:
//
//	password manager user-name <name> plaintext <secret>
//
// `manager` is read-write, `operator` is read-only. Writing the OPERATOR password
// when the caller meant the administration one would leave meshcraft holding a
// credential that opens a shell it cannot configure from — a failure that looks
// like a broken connector rather than a wrong account, which is why the level is
// hard-coded here rather than derived from anything.
//
// RUN ON HARDWARE 2026-08-03, AND IT DOES NOT WORK ON R.11.121. The command is
// accepted without a "%" line, "write memory" succeeds, and nothing changes
// — see the verification block below, which is what turns that into an error
// instead of a false success. The form is kept because it is the vendor's for
// this family and later revisions do take it; what this firmware needs instead
// is the INTERACTIVE exchange, which is not implemented yet.
//
// THE OLD SECRET IS NOT PRESENTED: this platform does not ask for it when the
// change is made from an authenticated manager session. It is accepted as a
// parameter because the contract passes it, and ignored deliberately rather than
// sent into a command that has no field for it.
func (c *Client) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	// Argument checks BEFORE the session check: an empty secret is a caller
	// mistake whether or not a session happens to be open, and reporting it as
	// "no open session" would send someone to look at the network.
	if strings.TrimSpace(newSecret) == "" {
		return errors.New("empty secret refused: this range would leave the account open")
	}
	if c.cli == nil {
		return errors.New("no open session on this access")
	}
	var user = strings.TrimSpace(c.User)
	if user == "" {
		return errors.New("no account has been proven on this device: " +
			"meshcraft does not guess the holder name")
	}
	// The secret is not logged, here or in the transcript: a step that echoed it
	// would put it in the site journal.
	var cmd = fmt.Sprintf("password manager user-name %s plaintext %s", user, newSecret)
	var err = c.runConfigBlock(ctx, []string{cmd})
	if err != nil {
		c.step(false, "manager password NOT changed", firstLine(err.Error()))
		return err
	}

	// VERIFY, and refuse to report success without it.
	//
	// THIS COMMAND IS ACCEPTED AND DOES NOTHING on R.11.121. Run against
	// SW-PLAYER-79 on 2026-08-03 it produced no "%" line, "write memory"
	// succeeded — and the configuration afterwards still read a bare
	//
	//	password manager
	//
	// with no "user-name", while the switch went on authenticating the previous
	// secret. On this firmware "password manager" is INTERACTIVE: it prompts
	// for the password and a retype, and the inline "user-name … plaintext …"
	// arguments of the later ProVision CLI are swallowed without complaint.
	//
	// A credential rotation that reports success and leaves the old password in
	// place is the exact failure the connector contract exists to prevent: the
	// vault advances a generation, the device does not, and the next operator
	// holds a secret that opens nothing. So the write is confirmed against the
	// configuration, and an unconfirmed one is an ERROR.
	//
	// The secret itself is never printed back by this platform — only the
	// user-name line is — so this proves the command TOOK, not which password it
	// set. The full proof is a reconnect, and that belongs to the caller.
	var after, rerr = c.cli.run(ctx, "show running-config", showRunTimeout)
	if rerr != nil && strings.TrimSpace(after) == "" {
		c.step(false, "password change NOT confirmed", firstLine(rerr.Error()))
		return fmt.Errorf("the password command was accepted but could not be "+
			"verified: %v", firstLine(rerr.Error()))
	}
	if !reManagerUser.MatchString(after) {
		c.step(false, "password NOT changed — the command was accepted and ignored",
			"the configuration still carries \"password manager\" with no user-name")
		return errors.New("this firmware ACCEPTED \"password manager user-name … " +
			"plaintext …\" and changed nothing: the configuration still carries a bare " +
			"\"password manager\" and the previous secret still opens the switch. On " +
			"this revision the command is INTERACTIVE (it prompts for the password and " +
			"a retype); meshcraft does not drive that exchange yet, so the credential " +
			"was NOT rotated")
	}
	c.step(true, "manager password changed for \""+user+"\"", "verified in the configuration")

	// Persist: a password that does not survive a reboot brings the switch back
	// to the one meshcraft no longer holds — exactly the trap provisioning exists
	// to close.
	if err = c.persist(ctx); err != nil {
		c.step(false, "password changed but NOT saved", firstLine(err.Error()))
		return fmt.Errorf("password set but not persisted: %v", err)
	}
	c.step(true, "configuration persisted", "write memory")
	return nil
}

// reManagerUser: the line this platform writes once a named manager account
// exists. Its ABSENCE after the write is what proves the command was ignored.
var reManagerUser = regexp.MustCompile(`(?m)^[ \t\r]*password manager user-name\b`)

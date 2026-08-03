package procurve

// The administration secret.

import (
	"context"
	"errors"
	"fmt"
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
// DOCUMENTED, not captured: this form comes from the ProCurve 2600 configuration
// guide and has not been run by this connector. It is implemented because it is
// unambiguous and because the alternative — refusing forever — leaves every
// device of this range on whatever password it was adopted with. The refusal
// shape is checked (cf. cliRefusal), so a form this firmware does not accept
// surfaces as an error rather than as a silent success.
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
	c.step(true, "manager password changed for « "+user+" »")

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

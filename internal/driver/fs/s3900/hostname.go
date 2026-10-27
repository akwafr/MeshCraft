package s3900

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name — `hostname` in this CLI's global config.
//
// # CAPTURED / DEDUCED, exactly
//
// The READ form is CAPTURED: the 2026-08-01/02 serial session's running
// configuration carries, bare and unquoted,
//
//	hostname test-fs-s3900
//
// (labRun in parse_test.go, OpCode 1.7.0 — and it is a NON-default value, so
// something did set it on that unit at survey time.) reRunHostname reads exactly
// that line, which is what makes the verification below trustworthy.
//
// The WRITE form is DEDUCED: `hostname <name>` from "conf t" is the vendor
// form for this CLI family and the same line MgmtPushCmds builds for the serial
// path, but this connector has never SENT it over its own SSH session and no
// capture records the device's reaction. Until the re-read below has succeeded on
// hardware, that is a hypothesis with good evidence behind it — not a proof.
//
// # The 1.7.0 → newer firmware question applies to this too
//
// The whole command set of this connector was surveyed on OpCode
// VerifiedOpCode.0, and the lab switch has since moved to 1.7.7 — where one
// captured form ALREADY CHANGED ("media ethernet" on VLAN creation went from
// required to rejected; cf. vlanCreateCmds). A firmware upgrade is planned on the
// rest of the fleet. So this form must be re-verified afterwards, and it must be
// captured on whatever firmware first accepts it rather than assumed to carry
// backwards.
//
// # What is NOT done
//
//   - No fallback spelling. Elsewhere in this package an unproven form is tried
//     in two shapes through runWithFallbacks (cf. dhcpsnoop.go, vlanCreateCmds).
//     Not here, and deliberately: a hostname has ONE documented spelling on this
//     family, the read side confirms that spelling, and a second candidate would
//     be a guess whose only effect on failure is a second refusal in the
//     transcript. The re-read is the safety net instead.
//   - No ceiling of our own beyond driver.ValidHostname's 63 bytes: nothing in
//     this project captures this range's real limit, and an invented number would
//     refuse names the device accepts. A refusal comes back through reCLIError
//     and is reported verbatim; a name the device shortens on its own is caught
//     by the re-read, which compares byte for byte. Nothing is truncated on our
//     side.
//   - The verifying re-read below does NOT become the device's announced name.
//     It confirms THIS command, to THIS caller, and [Client.forgetSysName] is
//     called so ReadIdentity reports nothing on this session rather than echoing
//     our own write back as an observation (cf. identityread.go). The name is
//     observed on the next read-state, or not at all.
var _ driver.HostnameSetter = (*Client)(nil)

// hostnameCmds builds the configuration block that renames the device.
//
// runConfigBlock supplies "conf t" and "end", so this carries the bare line.
func hostnameCmds(name string) []string {
	return []string{"hostname " + name}
}

// hostnameMismatch explains a re-read that does not carry the requested name.
//
// The truncation case names the observed ceiling: that number is documented
// nowhere in this project, and on a platform whose forms move between firmware
// builds, learning it from the device beats carrying a constant that would go
// stale without anything noticing.
func hostnameMismatch(want, got string) string {
	if got == "" {
		return "the CLI accepted \"hostname " + want + "\" but \"show run\" carries " +
			"no hostname line at all: a command accepted with no effect leaves no " +
			"other trace, which is why this write re-reads"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("the device stored %q — the requested name cut at %d "+
			"bytes. That is this firmware's own ceiling, learned here rather than "+
			"guessed; the switch now answers to a name meshcraft does not carry, so "+
			"send a name of at most %d bytes", got, len(got), len(got))
	}
	return fmt.Sprintf("\"show run\" reads %q, not %q: the write did not take the "+
		"way it was sent", got, want)
}

// SetHostname sets the device's own system name — driver.HostnameSetter.
//
// Write, re-read, persist. The save is "copy running-config startup-config"
// answered with StartupConfigFile (cf. Persist, which is what this calls): a
// hostname left in the running configuration alone is undone by the next reload
// while every meshcraft screen keeps claiming it.
func (c *Client) SetHostname(ctx context.Context, name string) error {
	var err = c.requireWritableDialect()
	if err != nil {
		return err
	}
	if c.cli == nil {
		return fmt.Errorf("no open session on this access")
	}
	// The shared floor, before the device is reached: this string is
	// concatenated into a configuration command, where a newline would run
	// whatever follows it as a second command with this session's privileges.
	if !driver.ValidHostname(name) {
		return fmt.Errorf("system name %q refused: it must carry only letters, "+
			"digits and hyphens, must not start or end with a hyphen, and must fit "+
			"in 63 bytes — this connector does not normalize it, because a device "+
			"answering to a name meshcraft did not choose breaks the LLDP "+
			"correlation this gesture exists to enable", name)
	}

	// From here on, whatever a previous read established about the device's name
	// is stale — and what follows is our own write, which must never be recorded
	// as the device's announcement (cf. identityread.go).
	c.forgetSysName()

	err = c.runConfigBlock(ctx, hostnameCmds(name))
	if err != nil {
		c.step(false, "system name NOT set", firstLine(err.Error()))
		return fmt.Errorf("hostname on %s: %v — command set verified on OpCode %s.x, "+
			"survey this firmware's forms", c.host, err, VerifiedOpCode)
	}
	c.step(true, "system name sent", "hostname "+name)

	// THE RE-READ IS AUTHORITATIVE, and on a write form nothing has proven it is
	// the whole difference between "sent" and "set".
	var runOut string
	runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		c.step(false, "system name not re-read", firstLine(err.Error()))
		return fmt.Errorf("hostname sent to %s but NOT re-read (%s): nothing was "+
			"saved, and meshcraft cannot confirm the device carries it",
			c.host, firstLine(err.Error()))
	}
	var got = parseRunningConfig(runOut, c.host).Hostname
	if got != name {
		var why = hostnameMismatch(name, got)
		c.step(false, "system name not confirmed", why)
		return fmt.Errorf("hostname on %s: %s", c.host, why)
	}
	c.step(true, "system name confirmed by re-read", "hostname "+got)

	err = c.Persist(ctx)
	if err != nil {
		return fmt.Errorf("hostname %q is set on %s but NOT persisted (%s): a reload "+
			"would bring the old name back while every screen kept showing the new one",
			name, c.host, firstLine(err.Error()))
	}
	return nil
}

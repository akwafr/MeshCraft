package procurve

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name — `hostname` on this range, "System Name" in
// its own `show system-information` output.
//
// # Why the name is QUOTED here and nowhere else in this connector
//
// Because this range quotes it ITSELF. The captured configuration of the
// reference unit reads, byte for byte:
//
//	hostname "SW-PLAYER-79"
//
// (cf. crlf_test.go's rawLineEndings and testdata/, SW-PLAYER-79, 2026-08-03 —
// and reHostname in parse.go, whose `"?` groups exist precisely because of it.)
// A `hostname X` without quotes is accepted on this CLI too, but writing the
// form the device itself renders is the rule this whole package follows: it is
// the only form we have EVIDENCE about, and it is what the next re-read will
// compare against.
//
// CAPTURED / DEDUCED, stated plainly: the READ side is captured twice over — the
// quoted running-config line above, and the "System Name" row of
// testdata/show_system_information.txt. The WRITE has never been sent by this
// connector on hardware; the command itself is the vendor-documented one for
// this range. Verify it live before treating it as proven.
//
// # Verification goes through `show system-information`, not the configuration
//
// Two reasons, and the second is the one that matters. First, it is cheap: one
// short command against a 45-second `show running-config` on a range that grants
// FOUR management sessions and holds an abandoned one for two minutes. Second,
// it reads the fact the gesture is actually about — the SYSTEM NAME as this
// device reports it, which is what its LLDP TLV and its syslog lines carry. A
// configuration line proves what was typed; the system-information row proves
// what the device became.
//
// # What is NOT done
//
//   - No `snmp-server` contact/location alongside it: those are separate facts
//     with their own screens, and setting them here would make one button write
//     three things nobody asked for.
//   - No ceiling of our own beyond driver.ValidHostname's 63 bytes. Nothing in
//     this project captures this range's real limit, and an invented number
//     would refuse names the device accepts. A refusal from the CLI comes back
//     through cliRefusal and is reported VERBATIM; a name the device shortens on
//     its own is caught by the re-read, which compares byte for byte. Nothing is
//     truncated on our side, ever.
var _ driver.HostnameSetter = (*Client)(nil)

// hostnameCmds builds the configuration block that renames the device.
//
// The quotes are literal and deliberate (cf. the file header). They are safe to
// add unconditionally because driver.ValidHostname has already reduced the name
// to letters, digits and hyphens — there is no quote, no space and no newline
// left in it for this CLI to misread.
func hostnameCmds(name string) []string {
	return []string{`hostname "` + name + `"`}
}

// hostnameMismatch explains a re-read that does not carry the requested name.
//
// Three distinct faults with three distinct fixes: nothing was written, the
// device shortened what it took, or it carries something else. The middle case
// is the dangerous one — the switch now answers to a name the inventory does not
// know — so the message states the observed ceiling, which exists nowhere else
// in this project.
func hostnameMismatch(want, got string) string {
	if got == "" {
		return "the CLI accepted \"hostname \"" + want + "\"\" but \"show " +
			"system-information\" reports no system name: a command accepted with " +
			"no effect is this CLI's quietest failure, and re-reading is the only " +
			"thing that catches it"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("the device reports %q — the requested name cut at %d "+
			"bytes. That is this range's own ceiling, learned here rather than "+
			"guessed; the switch now answers to a name meshcraft does not carry, so "+
			"send a name of at most %d bytes", got, len(got), len(got))
	}
	return fmt.Sprintf("\"show system-information\" reports %q, not %q: the write "+
		"did not take the way it was sent", got, want)
}

// SetHostname sets the device's own system name — driver.HostnameSetter.
//
// Write, re-read, persist — three steps, none of them optional. "write memory"
// is what makes this survive: on this range the running configuration is lost on
// reload, and a hostname that comes back to `HP-2610` after a power cut, while
// every meshcraft screen still shows the new one, is worse than a hostname never
// set.
func (c *Client) SetHostname(ctx context.Context, name string) error {
	if c.cli == nil {
		return errNoSession
	}
	// The shared floor, BEFORE the device is reached: this name is concatenated
	// into a configuration command, and a newline in it would run whatever
	// follows as a second command with this session's privileges.
	if !driver.ValidHostname(name) {
		return fmt.Errorf("system name %q refused: it must carry only letters, "+
			"digits and hyphens, must not start or end with a hyphen, and must fit "+
			"in 63 bytes — this connector does not normalize it, because a device "+
			"answering to a name meshcraft did not choose breaks the LLDP "+
			"correlation this gesture exists to enable", name)
	}

	var err = c.runConfigBlock(ctx, hostnameCmds(name))
	if err != nil {
		c.step(false, "system name NOT set", firstLine(err.Error()))
		return fmt.Errorf("hostname on %s: %w", c.tgt.Host, err)
	}
	c.step(true, "system name sent", `hostname "`+name+`"`)

	// THE RE-READ IS AUTHORITATIVE. This CLI reports refusals in its output, not
	// through a status, and a command it swallowed leaves no trace at all.
	var out string
	out, err = c.cli.run(ctx, "show system-information", cmdTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		c.step(false, "system name not re-read", firstLine(err.Error()))
		return fmt.Errorf("hostname sent to %s but NOT re-read (%s): nothing was "+
			"saved, and meshcraft cannot confirm the device carries it",
			c.tgt.Host, firstLine(err.Error()))
	}
	var got = parseSystemInfo(out).Name
	if got != name {
		var why = hostnameMismatch(name, got)
		c.step(false, "system name not confirmed", why)
		return fmt.Errorf("hostname on %s: %s", c.tgt.Host, why)
	}
	c.step(true, "system name confirmed by re-read", "System Name = "+got)

	err = c.persist(ctx)
	if err != nil {
		c.step(false, "\"write memory\" refused", firstLine(err.Error()))
		return fmt.Errorf("hostname %q is set on %s but NOT persisted (%s): a reload "+
			"would bring the old name back while every screen kept showing the new one",
			name, c.tgt.Host, firstLine(err.Error()))
	}
	c.step(true, "configuration persisted", "write memory")
	return nil
}

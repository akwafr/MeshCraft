package s3400

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name — `hostname` in this CLI's global config.
//
// # CAPTURED / DEDUCED, exactly
//
// The line sent, `hostname <name>` from "config", is the same one MgmtPushCmds
// builds, and that sequence WAS pushed on a real 48T4SP on 2026-08-03 — several
// of its other lines carry "Captured 2026-08-03" annotations, and two were
// re-found by reading the provisioned switch's running configuration over SSH.
// What is NOT recorded anywhere is a confirmation of the hostname line itself,
// and this connector has never sent it over its own session. Treat the form as
// DEDUCED until the re-read below has been seen to succeed on hardware.
//
// The READ side is not captured either: no fixture in this package carries a
// `hostname` line (testdata/ holds interface, LLDP and spanning-tree captures
// only), so reCfgHostname's shape — bare, unquoted, first token — is what the
// vendor's own documentation shows and what the sibling S3900 capture confirms
// for the family (`hostname test-fs-s3900`, OpCode 1.7.0). Different firmware,
// same CLI lineage; that is evidence, not proof.
//
// # Why the verification pays for a whole "show running-config"
//
// Because this platform has no cheaper honest answer. "| include" is
// documented in this package as UNRELIABLE here (cf. s3400.go: "show
// running-config | include snmp" returns nothing even when the community is
// set), so filtering would turn a successful write into a false failure. The
// full read is 90 seconds of pager work, once, and it is the only thing that
// distinguishes "the CLI did not complain" from "the device is renamed".
//
// # What is NOT done
//
//   - No ceiling of our own beyond driver.ValidHostname's 63 bytes: nothing in
//     this project captures this range's real limit, and an invented number
//     would refuse names the device accepts. A refusal comes back through
//     reCLIError and is reported verbatim; a name the device shortens on its own
//     is caught by the re-read, which compares byte for byte. Nothing is
//     truncated on our side.
//   - No SNMP sysName, no LLDP re-advertise: the system name propagates to both
//     on its own, and touching them here would write two more facts nobody asked
//     for.
//   - The verifying re-read below does NOT become the device's announced name.
//     It confirms THIS command, to THIS caller, and [Client.forgetSysName] is
//     called so ReadIdentity reports nothing on this session rather than echoing
//     our own write back as an observation (cf. identityread.go). The name is
//     observed on the next read-state, from a `show running-config` that owes
//     nothing to this write.
var _ driver.HostnameSetter = (*Client)(nil)

// hostnameCmds builds the config-mode block that renames the device.
//
// configBlock supplies "config" and the trailing "exit" itself, so this
// slice carries the bare line — the package idiom (cf. portConfigCmds).
func hostnameCmds(name string) []string {
	return []string{"hostname " + name}
}

// hostnameMismatch explains a re-read that does not carry the requested name.
//
// Three faults, three different next moves for the operator: nothing was
// written, the device kept a shortened form, or it carries something else
// entirely. The truncation case names the observed ceiling because that number
// is documented nowhere in this project — the field is where it gets learned.
func hostnameMismatch(want, got string) string {
	if got == "" {
		return "the CLI accepted \"hostname " + want + "\" but the running " +
			"configuration carries no hostname line at all: a command this CLI " +
			"swallows leaves no other trace, which is why this write re-reads"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("the device stored %q — the requested name cut at %d "+
			"bytes. That is this range's own ceiling, learned here rather than "+
			"guessed; the switch now answers to a name meshcraft does not carry, so "+
			"send a name of at most %d bytes", got, len(got), len(got))
	}
	return fmt.Sprintf("the running configuration reads %q, not %q: the write did "+
		"not take the way it was sent", got, want)
}

// SetHostname sets the device's own system name — driver.HostnameSetter.
//
// Write, re-read, persist. "write" is the verb on this platform (it answers
// "OK!"), NOT "copy running-config startup-config" — cf. Persist, which is
// what this calls rather than re-deriving the save. A hostname left only in the
// running configuration is undone by the next reload while every meshcraft
// screen keeps claiming it.
func (c *Client) SetHostname(ctx context.Context, name string) error {
	var err = c.requireWritable()
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

	err = c.configBlock(ctx, hostnameCmds(name))
	if err != nil {
		c.step(false, "system name NOT set", firstLine(err.Error()))
		return fmt.Errorf("hostname on %s: %w", c.host, err)
	}
	c.step(true, "system name sent", "\"hostname "+name+"\"")

	// THE RE-READ IS AUTHORITATIVE, not the absence of a refusal.
	var cfgOut string
	cfgOut, err = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if err != nil && strings.TrimSpace(cfgOut) == "" {
		c.step(false, "system name not re-read", firstLine(err.Error()))
		return fmt.Errorf("hostname sent to %s but NOT re-read (%s): nothing was "+
			"saved, and meshcraft cannot confirm the device carries it",
			c.host, firstLine(err.Error()))
	}
	var got = ParseRunningConfig(cfgOut).Hostname
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

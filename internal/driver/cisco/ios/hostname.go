package ios

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name — `hostname` in this CLI.
//
// # What this is for, and WHO calls it
//
// It IS `devices.name` (cf. driver/hostname.go), normalized into what a hostname
// may carry — one name, not two. This file is where that name becomes visible
// OUTSIDE meshcraft: the console banner, the LLDP system-name TLV, and every
// syslog line this switch emits. On a stack adopted from the factory all three
// read `Switch`, and that is what this gesture fixes.
//
// AND THE NAME IS RECONCILED, since 2026-08-06. This header used to end "Nothing
// here reconciles: a hostname that drifted is a hostname somebody changed at the
// console, on purpose" — driver/hostname.go now records that reasoning as wrong
// in an instructive way, and api/hostnameheal.go repairs the drift on a cadence
// with a doubling cooldown. The consequence for this file is concrete: SetHostname
// is no longer reached by a button somebody pressed, it is reached by a loop, on a
// device nobody is standing at. That is exactly why the re-read below refuses to
// claim an unconfirmed write — there is no operator watching the transcript.
//
// # The form: PROVEN ON HARDWARE, 2026-08-05 21:48
//
// `hostname <name>` in global configuration is the IOS documented form for
// release DocumentedRelease, and it is the same line this package ALREADY
// builds for the serial-adoption path (cf. MgmtPushCmds). It was DEDUCED until
// this connector sent it over its own SSH session to CiscoLabFirst1 and the
// rename was observed converging — cisco-ios is one of the two entries
// api.hostnameWritableConnectors marks proven rather than deduced.
//
// WHAT THE LIVE RUN TAUGHT that the deduction had not predicted: with `ip
// domain-name` set, the switch then announces itself as
// "CiscoLabFirst1.meshcraft.local" in its LLDP system-name TLV. The
// verification below is unaffected — `show running-config` carries the bare
// `hostname` line — but the RECONCILIATION compares the announced name, and it
// settles only because api.hostnameMatches keeps the first DNS label. A change
// that made that comparison exact would put every IOS device with a domain name
// into a permanent drift: re-pushed and re-evented every cooldown, for ever, on
// switches that are already correctly named.
//
// The READ half was captured all along — every `show running-config` fixture here
// carries a `hostname` line, and reRunHostname is what parses it.
//
// # The renaming moves the prompt UNDER the session doing it
//
// `hostname X` takes effect immediately: the very next prompt this session sees
// is `X(config)#`, not the old one. That is survivable here only because
// rePromptPriv/rePromptAny match ANY `[\w.\-]+` before the `#` — a connector
// that had anchored the prompt to the name observed at login would hang on this
// command for the full cmdTimeout, on a command the device executed perfectly
// well. Named here because it is the trap a future refactor of ssh.go would
// walk into.
//
// # What is NOT done
//
//   - No ceiling of our own is imposed beyond driver.ValidHostname's 63 bytes.
//     No capture in this project establishes what this range's real limit is,
//     and an invented number would refuse names the device accepts. A name the
//     CLI refuses comes back through reCLIError and is reported VERBATIM; a name
//     the device silently shortens is caught by the re-read below, which
//     compares byte for byte and says what the device actually stored. Nothing
//     is ever truncated on our side.
//   - No `snmp-server sysname`, no `ip domain-name`: the SNMP sysName follows
//     the hostname on this platform, and the domain name is a different fact
//     with different consequences (it is what SSH key generation keys off).
var _ driver.HostnameSetter = (*Client)(nil)

// hostnameCmds builds the configuration block that renames the device.
//
// One line, no quoting: driver.ValidHostname has already restricted the name to
// letters, digits and hyphens, so there is nothing here for a shell or a parser
// to misread — and that is the reason the validation is a precondition of this
// function rather than a courtesy inside it.
func hostnameCmds(name string) []string {
	return []string{"hostname " + name}
}

// hostnameMismatch explains a re-read that does not carry the requested name.
//
// Three DIFFERENT faults, and the operator needs to know which one happened:
// nothing was written at all, the device shortened what it accepted, or the
// device carries something else entirely. "Verification failed" would send
// someone to the console to find out; these messages say what is there.
func hostnameMismatch(want, got string) string {
	if got == "" {
		return "the CLI accepted \"hostname " + want + "\" but the running " +
			"configuration carries no hostname line at all: a return code that " +
			"changes nothing is what this re-read exists to catch"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("the device stored %q — the requested name cut at %d "+
			"bytes. That is this range's own ceiling, learned here rather than "+
			"guessed; the switch now answers to a name meshcraft does not carry, "+
			"so send a name of at most %d bytes", got, len(got), len(got))
	}
	return fmt.Sprintf("the running configuration reads %q, not %q: the write did "+
		"not take the way it was sent", got, want)
}

// SetHostname sets the device's own system name — driver.HostnameSetter.
//
// Write, re-read, persist, in that order and never fewer than three steps. The
// persistence is not optional and not the caller's job (cf. the contract): a
// hostname a reload undoes is worse than one never set, because every screen
// goes on claiming it while the console shows the old one.
func (c *Client) SetHostname(ctx context.Context, name string) error {
	var err = c.requireWritableDialect()
	if err != nil {
		return err
	}
	if c.cli == nil {
		return fmt.Errorf("no open session on this access")
	}
	// The shared floor first, and BEFORE the device is touched: on this
	// connector the name is concatenated into a configuration command, where a
	// newline would run whatever follows it as a second command with the
	// privileges of this session.
	if !driver.ValidHostname(name) {
		return fmt.Errorf("system name %q refused: it must carry only letters, "+
			"digits and hyphens, must not start or end with a hyphen, and must fit "+
			"in 63 bytes — this connector does not normalize it, because a device "+
			"answering to a name meshcraft did not choose breaks the LLDP "+
			"correlation this gesture exists to enable", name)
	}

	err = c.runConfigBlock(ctx, hostnameCmds(name))
	if err != nil {
		c.step(false, "system name NOT set", firstLine(err.Error()))
		return fmt.Errorf("hostname %s on %s: %w", name, c.host, err)
	}
	c.step(true, "system name sent", "hostname "+name)

	// THE RE-READ IS AUTHORITATIVE, not the absence of a CLI refusal.
	var runOut string
	runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		// The command went through with no refusal, so the name is probably in
		// place — but "probably" must not be persisted and must not be
		// reported as done.
		c.step(false, "system name not re-read", firstLine(err.Error()))
		return fmt.Errorf("hostname %s sent to %s but NOT re-read (%s): nothing was "+
			"saved, and meshcraft cannot confirm the device carries it",
			name, c.host, firstLine(err.Error()))
	}
	var got = parseRunningConfig(runOut, c.host).Hostname
	if got != name {
		var why = hostnameMismatch(name, got)
		c.step(false, "system name not confirmed", why)
		return fmt.Errorf("hostname on %s: %s", c.host, why)
	}
	c.step(true, "system name confirmed by re-read", "hostname "+got)

	err = c.save(ctx)
	if err != nil {
		c.step(false, "startup-config save failed", firstLine(err.Error()))
		return fmt.Errorf("hostname %s is set on %s but NOT persisted (%s): a reload "+
			"would bring the old name back, and every screen would keep showing the "+
			"new one", name, c.host, firstLine(err.Error()))
	}
	c.step(true, "configuration persisted", SaveCmd)
	return nil
}

package edgeswitch

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name. Two facts about this range, both captured on
// the lab EdgePoint S16 (unifimoule1, 192.0.2.4, FASTPATH 1.9.3), and the second
// only exists because the first cost a day of daily failures.
//
// # 1. `hostname` is REFUTED — 2026-08-05 23:24
//
// The form "hostname <name>" in Global Config was DEDUCED from this CLI's
// family grammar. The reconciliation loop sent "hostname unifimoule1" and the
// device answered, verbatim:
//
//	% Invalid input detected at '^' marker.
//
// This FASTPATH-derived CLI has no `hostname` command. That refutation STANDS and
// is not softened by anything below: the verb is wrong, it was always wrong, and
// nothing in this package may utter it again (pinned by
// TestHostnameCmdsNeverUtterTheRefutedVerb here and by
// TestMgmtPushCmdsSendsNoHostname in edgeswitch_test.go).
//
// # 2. `snmp-server sysname` is the system name — CAPTURED 2026-08-10
//
// Console capture on the same unit, as `mcadmin`, over the controller's SOCKS
// route. Baseline, "show lldp local-device detail 0/18":
//
//	System Name: UBNT EdgeSwitch
//	System Description: EdgePoint S16, 1.9.3, Linux 3.6.5-03329b4a, 1.2.0.5192732
//
// Then, from "configure":
//
//	snmp-server sysname mctest1        <- accepted, no error
//
// Re-read in a NEW session:
//
//	System Name: mctest1
//	System Description: EdgePoint S16, 1.9.3, Linux 3.6.5-03329b4a, 1.2.0.5192732
//
// Reverted with "snmp-server sysname "UBNT EdgeSwitch"" (quoted, because of the
// space) and re-read confirming "System Name: UBNT EdgeSwitch".
//
// So the two questions TODO.md kept open are both answered: (a) `snmp-server
// sysname` IS the system name on this range, not `set prompt`; (b) it DRIVES
// `lldpLocSysName`, which is the value api/hostnameheal.go compares.
//
// # `set prompt` is NOT sent, and that is now a decision rather than a suspicion
//
// Throughout the capture the CLI prompt stayed "(UBNT EdgeSwitch) #" /
// "(Config)#" while the System Name changed: sysname does not drive the prompt,
// and by symmetry the prompt is not the system name. Sending `set prompt` would
// therefore be exactly the trap the old TODO entry named — a form that changes
// what an operator SEES on screen and leaves the TLV the reconciliation compares
// untouched, so the loop would converge visually and repair nothing. If the
// prompt is ever wanted it is a separate gesture, with its own capture.
//
// # Quoting: BARE, and which case that covers
//
// The command is emitted unquoted — "snmp-server sysname <name>" — for two
// reasons that agree. It is the form the device was CAPTURED accepting
// ("snmp-server sysname mctest1", accepted with no error), and this package's
// rule is to send the form there is evidence for. And [driver.ValidHostname],
// which every name crosses before reaching this file, reduces the name to
// letters, digits and hyphens: there is no space, no quote and no newline left in
// it for this CLI to misread, so quotes would protect against nothing.
//
// The quoted form exists on this CLI and is REQUIRED for a value carrying a
// space — the factory "UBNT EdgeSwitch" had to be sent as ""UBNT EdgeSwitch""
// to be restored. THIS CONNECTOR NEVER SENDS THAT CASE: meshcraft only ever
// writes a `devices.name` that passed ValidHostname, and a name with a space
// cannot get here. Restoring a factory value with a space is a console gesture,
// and whoever performs it must quote it by hand. The READ side handles spaces
// anyway (cf. reRunSysname, reLLDPLocalSysName) so that a device still carrying
// the factory name is reported as what it is instead of half of it.
//
// # No extra session, and that is not a detail on this range
//
// Everything below runs on the ONE interactive shell this Client already holds
// (cf. ssh.go): the write, the re-read and the save. This platform declares five
// session slots, and the leak that motivated Admin.Close() was observed exactly
// here — two operator attempts to open the browser console got an immediate EOF,
// accepted at the transport then dropped with zero bytes exchanged, seconds after
// an apply had authenticated and never let go. A gesture this small has no
// business spending a slot.
//
// # WHAT IS STILL NOT PROVEN, and it is not nothing
//
//   - THE CONNECTOR PATH IS UNEXERCISED. The 2026-08-10 capture was typed at a
//     console. No device has yet been renamed through this SetHostname — the
//     command form, the re-read and the comparison below have never run together
//     against hardware. The next real repair is the first.
//   - PERSISTENCE ACROSS RELOAD IS UNCAPTURED. The capture touched the running
//     configuration only and was never saved. "write memory confirm" itself is
//     proven on this range (every apply here uses it, cf. Persist), but that a
//     `snmp-server sysname` line lands in startup-config and survives a reboot has
//     not been observed. SetHostname persists anyway — a name that a reboot undoes
//     is worse than one never set, since every screen keeps claiming it — and the
//     first reload of a renamed unit is what will confirm or refute this.
//   - HOW `show running-config` RENDERS THE LINE IS UNKNOWN. The baseline capture
//     (testdata/show_running_config.txt) is a factory-default unit and carries no
//     sysname line at all. Its neighbour "snmp-server community "…" ro" renders
//     QUOTED, which is a hint about this CLI's habit with string arguments and not
//     proof about this command, so the parser accepts both renderings rather than
//     guessing one. That hedge is inherited from the refuted `hostname` parser,
//     where it was right for the same reason.
//   - No ceiling of our own beyond driver.ValidHostname's 63 bytes. This range's
//     real limit is not captured anywhere in this project, and an invented number
//     would refuse names the device accepts. A name the CLI refuses comes back
//     through reCLIError and is reported verbatim; a name the device silently
//     shortens is caught by the re-read, which compares byte for byte. Nothing is
//     truncated on our side.
//
// The capability IS advertised — the assertion below is the advertisement, and it
// travels with `ubiquiti-edgeswitch: true` in api.hostnameWritableConnectors. The
// two must be flipped together or api/hostnameheal_test.go fails; it is what
// caught the half-done job last time.
var _ driver.HostnameSetter = (*Client)(nil)

// Where a re-read came from, in the operator's words. These strings land in
// error messages, so they are phrases and not identifiers.
const (
	sysnameFromTLV = "the LLDP System Name TLV"
	sysnameFromRun = "the running configuration"
)

// lldpLocalPort is the port whose "show lldp local-device detail" the re-read
// asks for.
//
// The System Name is a DEVICE-level TLV — it is the same on every port, and the
// 2026-08-10 capture used 0/18 only because that is the lab unit's SFP+ uplink.
// 0/1 is used here instead because it is the one socket number every model of
// this range has: FASTPATH numbers front-panel sockets from 1 (cf. splitStandard,
// which refuses "GE0" for that reason), so a 8-, 16-, 24- or 48-port unit all
// answer for it, where 0/18 exists only on the S16. The lab unit's PoE work
// addressed 0/1 directly (cf. contract.go's WritesExtra note), so it is known
// addressable there.
const lldpLocalPort = "0/1"

// reLLDPLocalSysName matches the "System Name:" row of "show lldp local-device
// detail <port>". CAPTURED shape, 2026-08-10 — this is the exact command whose
// before/after is quoted in this file's header.
//
// The value is taken as the WHOLE rest of the line, trimmed, because the name
// this row can carry is not restricted to what meshcraft writes: a device still
// answering to its factory "UBNT EdgeSwitch" has a space in it, and reporting
// "UBNT" would misdescribe the very state this gesture exists to repair.
var reLLDPLocalSysName = regexp.MustCompile(`(?im)^[ \t]*System Name:[ \t]*(.*?)[ \t]*$`)

// lldpLocalSysName reads the system name this device ANNOUNCES, or "" when the
// output carries no such row — which is what a refused command or an LLDP-less
// device looks like, and is a state to fall back from rather than an error.
func lldpLocalSysName(out string) string {
	var m = reLLDPLocalSysName.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	return m[1]
}

// reRunSysname matches the system-name declaration in "show running-config".
//
// BOTH renderings, and the reason is stated in the file header: no capture in
// this package settles whether an EdgePoint S16 prints this value quoted or bare,
// and the one nearby line that is captured ("snmp-server community "…" ro") is
// a hint about the family's habit, not proof about this command. Accepting both
// is what keeps the verification from failing on a device that did exactly what
// it was told.
//
// The quoted alternative deliberately admits spaces, so that a unit still
// carrying "UBNT EdgeSwitch" is reported whole.
var reRunSysname = regexp.MustCompile(`(?im)^[ \t]*snmp-server[ \t]+sysname[ \t]+(?:"([^"]*)"|(\S+))[ \t]*$`)

// runningConfigSysname reads the configured system name, or "" when the
// configuration carries no sysname line — which is what a factory-default unit
// looks like (cf. testdata/show_running_config.txt), and therefore not an error.
func runningConfigSysname(out string) string {
	var m = reRunSysname.FindStringSubmatch(out)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// hostnameCmds builds the Global Config block that renames the device.
//
// "configure" … "exit" are part of the slice, which is this package's idiom
// (cf. accounts.go, apply.go): runBlock sends a flat list and waits for one
// prompt per line, so the mode changes have to be IN the list or they do not
// happen at all. The capture confirms the mode — the command was accepted from
// "(Config)#", and privileged EXEC is not where it lives.
//
// The name is interpolated BARE. See the file header, § Quoting.
func hostnameCmds(name string) []string {
	return []string{"configure", "snmp-server sysname " + name, "exit"}
}

// hostnameMismatch explains a re-read that does not carry the requested name.
//
// `source` is the phrase naming where the reading came from — the two sources
// are not equally strong evidence and the operator has to be able to tell which
// one spoke (cf. readSysname).
func hostnameMismatch(want, got, source string) string {
	if got == "" {
		return "the CLI accepted \"snmp-server sysname " + want + "\" but " + source +
			" carries no system name: a command this CLI swallows leaves no other " +
			"trace, which is why this write re-reads"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("%s reads %q — the requested name cut at %d bytes. That is "+
			"this range's own ceiling, learned here rather than guessed; the switch now "+
			"answers to a name meshcraft does not carry, so send a name of at most %d "+
			"bytes", source, got, len(got), len(got))
	}
	return fmt.Sprintf("%s reads %q, not %q: the write did not take the way it was sent",
		source, got, want)
}

// readSysname reads back the system name and says WHERE it read it.
//
// # The TLV first, and it is not a stylistic preference
//
// "show lldp local-device detail <port>" is the value api/hostnameheal.go
// actually compares: the healer's observation is `lldp_self.sys_name`, fed by
// `lldpLocSysName`. Confirming a configuration line would prove what was TYPED;
// confirming the TLV proves what the healer will next OBSERVE, and this range's
// whole history is a lesson about the gap between the two — `set prompt` would
// have changed the screen and not the TLV, and the loop would have "converged"
// for ever on nothing.
//
// It is also the only re-read shape CAPTURED for this value on this range: the
// 2026-08-10 transcript is literally this command, before and after. And it is
// cheap — one short read against a 222-line "show running-config" on a 90-second
// timeout, which matters on a platform granting five sessions.
//
// # The configuration is the FALLBACK, and can only rescue a false negative
//
// If the TLV read gives nothing — LLDP switched off, the port not addressable on
// some model, the command refused — the running configuration is consulted rather
// than declaring the write unconfirmed. That fallback cannot manufacture a false
// success: the caller compares byte for byte against the name it sent, so a wrong
// parse fails to match and reports a mismatch. It can only turn "sent but not
// re-read" into a real confirmation, which is the difference between a repair
// that lands and one the healer retries daily.
func (c *Client) readSysname(ctx context.Context) (string, string, error) {
	var out, err = c.cli.run(ctx, "show lldp local-device detail "+lldpLocalPort, cmdTimeout)
	var got = lldpLocalSysName(out)
	if got != "" {
		return got, sysnameFromTLV, nil
	}
	var tlvWhy = "no \"System Name\" row in the output"
	if err != nil {
		tlvWhy = firstLine(err.Error())
	}

	var runOut string
	runOut, err = c.cli.run(ctx, "show running-config", showTimeout)
	if err != nil && strings.TrimSpace(runOut) == "" {
		return "", "", fmt.Errorf("the LLDP local-device read said nothing (%s) and "+
			"\"show running-config\" failed too (%s)", tlvWhy, firstLine(err.Error()))
	}
	return runningConfigSysname(runOut), sysnameFromRun, nil
}

// SetHostname sets the device's own system name, implementing
// [driver.HostnameSetter].
//
// The verb is "snmp-server sysname", captured 2026-08-10; "hostname" is
// refuted and must never come back. Read this file's header before touching
// either — it is the whole reason this method exists twice.
func (c *Client) SetHostname(ctx context.Context, name string) error {
	var err = c.requireWritable()
	if err != nil {
		return err
	}
	// The shared floor, BEFORE the transport check and long before the device is
	// touched: this string is concatenated into a configuration command, where a
	// newline would run whatever follows it as a second command with this
	// session's privileges. It is also what makes the BARE emission safe (cf.
	// § Quoting) — a name that gets past here has no space and no quote in it.
	//
	// Argument first, transport second, deliberately: a name this connector can
	// never send is refused identically whether or not a session happens to be
	// open, which is also what makes the refusal testable without hardware.
	if !driver.ValidHostname(name) {
		return fmt.Errorf("system name %q refused: it must carry only letters, "+
			"digits and hyphens, must not start or end with a hyphen, and must fit "+
			"in 63 bytes — this connector does not normalize it, because a device "+
			"answering to a name meshcraft did not choose breaks the LLDP "+
			"correlation this gesture exists to enable", name)
	}
	if c.cli == nil {
		return fmt.Errorf("no open session on this access")
	}

	err = c.runBlock(ctx, hostnameCmds(name))
	if err != nil {
		c.step(false, "system name NOT set", firstLine(err.Error()))
		return fmt.Errorf("system name on %s: %w", c.host, err)
	}
	c.step(true, "system name sent", "\"snmp-server sysname "+name+"\"")

	// THE RE-READ IS AUTHORITATIVE, and on a form this connector has never itself
	// sent to hardware it is the whole difference between "sent" and "set".
	var got, source string
	got, source, err = c.readSysname(ctx)
	if err != nil {
		c.step(false, "system name not re-read", firstLine(err.Error()))
		return fmt.Errorf("system name sent to %s but NOT re-read (%s): nothing was "+
			"saved, and meshcraft cannot confirm the device carries it",
			c.host, firstLine(err.Error()))
	}
	if got != name {
		var why = hostnameMismatch(name, got, source)
		c.step(false, "system name not confirmed", why)
		return fmt.Errorf("system name on %s: %s", c.host, why)
	}
	c.step(true, "system name confirmed by re-read", source+" reads "+got)

	err = c.Persist(ctx)
	if err != nil {
		return fmt.Errorf("system name %q is set on %s but NOT persisted (%s): a reload "+
			"would bring the old name back while every screen kept showing the new one",
			name, c.host, firstLine(err.Error()))
	}
	return nil
}

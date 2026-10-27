package ios

// SNMP identity — v2c only, as the capabilities declare.
//
// # Why v3 is refused on a platform that clearly has it
//
// IOS carries SNMPv3, and good SNMPv3 at that (SHA / AES). This connector
// still declares v1/v2c only, and the reason is a rule this codebase applies
// everywhere: nothing is taken as written on the strength of a return code.
// v3 users do NOT appear in "show running-config" on IOS — they live in a
// separate store and are read with "show snmp user", a command nobody here
// has run against a 2960-X. Declaring v3 would mean either verifying with a
// read that has never been exercised, or skipping verification for one version
// and not the others.
//
// So v3 is REFUSED, explicitly, with what it would take to open it. That is
// worth more than a v2c community silently posed where a v3 user was asked
// for: the caller would get working monitoring and false compliance, and a
// secret in the clear on the network that nobody chose.
//
// # What this platform does NOT carry, said out loud
//
// A source restriction IS expressible on IOS — "snmp-server community S RO 99"
// binds the community to access-list 99 — and it is NOT written here, because
// writing an ACL number without creating the ACL binds the community to an
// empty list and silences the agent completely. The bound the caller asked for
// therefore lands in Skipped, which is the honest answer: the community
// answers anything that reaches the management VLAN.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// ApplySNMP writes the v2c SNMP identity, verifies by re-read, then
// PERSISTS — a community that disappears on reboot silently kills
// supervision, which is exactly what this write exists to prevent.
func (c *Client) ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error) {
	var out = SNMPResult{Skipped: []string{}}

	// Capability refusals are decided BEFORE touching the device.
	if want.User != "" {
		return out, driver.NotSupported("cisco-ios", "pose an SNMPv3 identity",
			"IOS does carry SNMPv3, and this connector declares v1/v2c only — because a v3 "+
				"user is not visible in \"show running-config\" and would therefore be the one "+
				"write this connector could not verify it made. Posing a v2c community instead "+
				"would silently downgrade what the caller asked for. Exercise \"show snmp user\" "+
				"on this platform and the refusal can be lifted")
	}
	if want.Community == "" {
		out.Skipped = append(out.Skipped,
			"no v2c community in the intent: nothing was written (an apply is not a reset)")
		return out, nil
	}
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	// Read first: idempotence avoids wearing the flash for nothing.
	var runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	var rc = parseRunningConfig(runOut, c.host)
	// On IOS the agent is off until a community exists, and on as soon as one
	// does — there is no separate enable switch.
	out.Enabled = len(rc.Communities) > 0

	if len(want.Sources) > 0 {
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"source restriction (%s) not applied: IOS expresses it as an access-list number "+
				"on the community line, and writing that number without creating the ACL "+
				"would bind the community to an empty list and silence the agent. The "+
				"community answers any address that reaches the management VLAN",
			strings.Join(want.Sources, ", ")))
	}

	if rc.Communities[want.Community] == "ro" {
		c.step(true, "SNMP already conforming",
			fmt.Sprintf("community %q present read-only", want.Community))
		out.Enabled = true
		return out, nil
	}

	err = c.runConfigBlock(ctx, []string{
		fmt.Sprintf("snmp-server community %s RO", want.Community),
	})
	if err != nil {
		c.step(false, "SNMP community not written", firstLine(err.Error()))
		return out, err
	}

	// Nothing is taken as written on the strength of a return code: re-read.
	runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("post-write re-read failed: %v", err)
	}
	rc = parseRunningConfig(runOut, c.host)
	if rc.Communities[want.Community] != "ro" {
		c.step(false, "SNMP community absent after write",
			"the CLI accepted the command but the running-config does not carry it")
		return out, fmt.Errorf("community %q not present after write", want.Community)
	}
	out.CommunitySet = true
	out.Enabled = true
	c.step(true, "SNMP community written and re-read",
		fmt.Sprintf("community %q read-only", want.Community))

	// The factory-default communities are not removed on the connector's own
	// initiative: on this vendor they are often set deliberately by whoever ran
	// the switch before, and cutting someone else's monitoring to install our
	// own would be the worst possible service. Reported, not cleared.
	var name, right string
	for name, right = range rc.Communities {
		if name == want.Community || right != "rw" {
			continue
		}
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"community %q is READ-WRITE and is still present on the device — meshcraft does "+
				"not remove a community it did not set. Remove it by hand if it is not yours",
			name))
	}

	var perr = c.save(ctx)
	if perr != nil {
		out.Skipped = append(out.Skipped,
			"community applied but NOT persisted: "+firstLine(perr.Error())+
				" — a reboot would silence monitoring")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

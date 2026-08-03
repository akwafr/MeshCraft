package s3900

// SNMP identity — v2c only, as the capabilities declare.
//
// The form "snmp-server community S ro|rw" is CAPTURED: it appears verbatim
// in the lab's factory running-config ("snmp-server community public ro",
// "private rw"). What the platform does NOT carry, and what this file states
// out loud:
//
//   - no v3 on OpCode 1.7 (nothing captured, nothing declared) — a v3
//     identity is REFUSED, never approximated as a v2c nobody chose;
//   - no SOURCE BOUND on the community: the 1.7 CLI has no field for
//     authorized addresses. The bound the caller asked for is recorded in
//     Skipped — the operator knows the community answers the whole VLAN.

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
		return out, driver.NotSupported("fs-s3900", "pose an SNMPv3 identity",
			"nothing v3 was ever observed on FSOS OpCode 1.7 and the connector declares "+
				"v1/v2c only — posing a v2c community instead would silently downgrade "+
				"what the caller asked for. Survey the CLI after the firmware upgrade")
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
	var runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("pre-write read failed: %v", err)
	}
	var rc = parseRunningConfig(runOut)
	// The 1.7 agent answers as soon as a community exists (factory config:
	// public/private already set, agent active).
	out.Enabled = len(rc.Communities) > 0

	if len(want.Sources) > 0 {
		out.Skipped = append(out.Skipped, fmt.Sprintf(
			"source restriction (%s) not expressible on FSOS 1.7: the community answers "+
				"any address that reaches the management VLAN", strings.Join(want.Sources, ", ")))
	}

	if rc.Communities[want.Community] == "ro" {
		c.step(true, "SNMP already conforming",
			fmt.Sprintf("community %q present read-only", want.Community))
		out.Enabled = true
		return out, nil
	}

	err = c.runConfigBlock(ctx, []string{
		fmt.Sprintf("snmp-server community %s ro", want.Community),
	})
	if err != nil {
		c.step(false, "SNMP community not written", firstLine(err.Error()))
		return out, err
	}

	// Nothing is taken as written on the strength of a return code: re-read.
	runOut, err = c.cli.run(ctx, "show run", showRunTimeout)
	if err != nil && runOut == "" {
		return out, fmt.Errorf("post-write re-read failed: %v", err)
	}
	rc = parseRunningConfig(runOut)
	if rc.Communities[want.Community] != "ro" {
		c.step(false, "SNMP community absent after write",
			"the CLI accepted the command but the running-config does not carry it")
		return out, fmt.Errorf("community %q not present after write", want.Community)
	}
	out.CommunitySet = true
	out.Enabled = true
	c.step(true, "SNMP community written and re-read",
		fmt.Sprintf("community %q read-only", want.Community))

	if rc.Communities["private"] == "rw" {
		out.Skipped = append(out.Skipped,
			"factory community « private » (READ-WRITE) is still present on the device — "+
				"remove it by hand until its removal form is surveyed on this firmware")
	}

	var perr = c.save(ctx)
	if perr != nil {
		out.Skipped = append(out.Skipped,
			"community applied but NOT persisted: "+firstLine(perr.Error())+
				" — a reboot would silence monitoring")
		c.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	c.step(true, "configuration persisted", "copy running-config startup-config → "+StartupConfigFile)
	return out, nil
}

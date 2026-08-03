package procurve

// The SNMP agent on this range: a read-only community, set and verified.
//
// # The form is CAPTURED, from the device's own configuration
//
// The reference unit already carried one when it was surveyed (2026-08-02):
//
//	snmp-server contact "technique@lyon-esport.fr"
//	snmp-server location "InTheKitchen"
//	snmp-server community "les" Operator
//
// On this platform the running-config IS the command language — what it prints
// back is what it accepts — so « snmp-server community "<name>" Operator » is an
// observed form rather than a deduced one. `Operator` is the READ-ONLY access
// level; `Manager` is read-write.
//
// # Read-only, and the contract is what forces it
//
// [driver.SNMPIdentity] carries no way to ask for a writable community, and the
// reason is stated there: a read-write community across a fleet is a root
// password in clear on the network. meshcraft writes nothing over SNMP — every
// configuration gesture goes through this connector — so `Operator` is not a
// limitation here, it is the whole requirement.
//
// # v3 is REFUSED rather than approximated
//
// The 2600 series does carry an SNMPv3 engine, and this connector does not touch
// it: nothing about its command set has been observed on this hardware, and the
// failure mode of guessing is a supervision that is configured and silent. A v3
// intent is therefore reported in Skipped with the reason, which is what lets the
// caller see that the device is monitored by v2c and know why.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin configures SNMP.
var _ driver.SNMPConfigurator = (*Client)(nil)

// reCommunity matches a community line in the running-config, so a write can be
// VERIFIED rather than assumed.
var reCommunity = regexp.MustCompile(`(?mi)^[ \t\r]*snmp-server community\s+"?([^"\s]+)"?(?:\s+(\w+))?`)

// ApplySNMP sets the read-only community on the device.
//
// IDEMPOTENT BY OBSERVATION, not by hope: the running-config is re-read and the
// community looked for. A device that already carries it is left alone and
// reports CommunitySet=false — re-writing a fleet that is already conformant
// would churn flash on every pass for nothing.
//
// The agent on this range has no separate on/off switch reachable from the CLI:
// declaring a community IS enabling it, and there is no state where a community
// exists and the agent ignores it. Enabled therefore reports true once a
// community is in place, and that is a statement about this platform rather than
// an assumption.
func (c *Client) ApplySNMP(ctx context.Context, want driver.SNMPIdentity) (driver.SNMPResult, error) {
	var out driver.SNMPResult
	if c.cli == nil {
		return out, errors.New("no open session on this access")
	}

	if want.User != "" {
		out.Skipped = append(out.Skipped,
			"SNMPv3 user \""+want.User+"\" not written: this connector has never had "+
				"the v3 command set of this range on hardware, and a guessed one gives "+
				"a supervision that is configured and silent. Monitor this device with "+
				"v2c, or survey the v3 forms and add them")
	}
	if want.Community == "" {
		if len(out.Skipped) == 0 {
			out.Skipped = append(out.Skipped,
				"no v2c community in the intent: nothing was written (an apply is not a reset)")
		}
		return out, nil
	}
	if strings.ContainsAny(want.Community, "\"' \t") {
		return out, fmt.Errorf("community %q contains a character this CLI cannot carry "+
			"inside its quoted form", want.Community)
	}

	// What is already there? Re-read rather than trust a cached state.
	var before, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil {
		return out, fmt.Errorf("reading the configuration before writing: %v", err)
	}
	if hasCommunity(before, want.Community) {
		out.Enabled = true
		c.step(true, "SNMP community already in place", "nothing rewritten")
		return out, nil
	}

	err = c.runConfigBlock(ctx, []string{
		"snmp-server community \"" + want.Community + "\" Operator",
	})
	if err != nil {
		c.step(false, "SNMP community NOT written", firstLine(err.Error()))
		return out, err
	}

	// VERIFY. On this range a refused command is only visible in its output, and
	// a community that did not land leaves monitoring silent with nothing on
	// screen to say so — so the write is confirmed by re-reading, not by the
	// absence of an error.
	var after string
	after, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil {
		out.Skipped = append(out.Skipped,
			"community written but NOT verified: the configuration could not be re-read ("+
				firstLine(err.Error())+")")
		return out, nil
	}
	if !hasCommunity(after, want.Community) {
		return out, fmt.Errorf("the community was accepted by the CLI but is absent from "+
			"the configuration afterwards: %q did not take", want.Community)
	}
	out.CommunitySet = true
	out.Enabled = true
	c.step(true, "SNMP community set", "read-only (Operator), verified in the configuration")

	// Persist: an agent that forgets its community at the next reboot is a fleet
	// that goes dark on a power cut, with nothing having reported a failure.
	if err = c.persist(ctx); err != nil {
		out.Skipped = append(out.Skipped,
			"community applied but NOT saved: it will be lost at the next reboot ("+
				firstLine(err.Error())+")")
		return out, nil
	}
	c.step(true, "configuration persisted", "write memory")
	return out, nil
}

// hasCommunity reports whether the configuration already declares this community.
//
// The NAME is compared and the access level is not: a community present at
// `Manager` is still present, and silently downgrading someone's read-write
// community to read-only is not this connector's call to make.
func hasCommunity(config, name string) bool {
	var m []string
	for _, m = range reCommunity.FindAllStringSubmatch(config, -1) {
		if m[1] == name {
			return true
		}
	}
	return false
}

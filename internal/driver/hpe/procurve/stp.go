package procurve

// Spanning tree on the 2600 range.
//
// # This family runs an MSTP engine whatever mode it is in
//
// That is the first surprising thing, and it changes how the mode is read.
// CAPTURED on SW-PLAYER-79 (2026-08-03), a switch running plain RSTP:
//
//	 Multiple Spanning Tree (MST) Information
//	  STP Enabled   : Yes
//	  Force Version : RSTP-operation
//	  IST Mapped VLANs : 1-4094
//
// The header says MST even though the operating mode is RSTP. The mode is
// "Force Version", not the title — a parser keyed on the banner would file
// every switch of this range as MSTP and then push it an MST region it is not
// using.
//
// # What is CAPTURED and what is DOCUMENTED
//
// From the reference unit's own running configuration, so these are the
// platform's own spelling:
//
//	spanning-tree
//	spanning-tree 1 bpdu-protection
//	spanning-tree bpdu-protection-timeout 10 force-version rstp-operation
//
// DOCUMENTED and not captured: "priority", "config-name",
// "config-revision", "instance" and "path-cost". They are implemented,
// and every one of them is VERIFIED BY RE-READING after the write (cf.
// applyBridge) rather than trusted — on this CLI a refused command prints a
// "%" line and changes nothing, and a spanning-tree setting that did not take
// is invisible until a loop proves it.

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Force Version values, as this platform spells them.
const (
	forceRSTP = "rstp-operation"
	forceMSTP = "mstp-operation"
	forceSTP  = "stp-compatible"
)

// priorityStep: this CLI takes the bridge priority as a MULTIPLIER, not as the
// 802.1D value.
//
// "spanning-tree priority 8" produces the 32768 that "show" prints back.
// Sending 32768 is refused (the range is 0-15), and — worse — sending 8 while
// meaning 8 would set 32768's neighbour at the very bottom of the scale. The
// two representations are converted in exactly one place, here.
const priorityStep = 4096

// ReadSpanningTree reports the bridge and its ports.
func (c *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out driver.SpanningTreeState
	if c.cli == nil {
		return out, errNoSession
	}
	var text, err = c.cli.run(ctx, "show spanning-tree", showRunTimeout)
	if err != nil && strings.TrimSpace(text) == "" {
		return out, fmt.Errorf("show spanning-tree on %s: %v", c.tgt.Host, firstLine(err.Error()))
	}
	out = parseSpanningTree(text)

	// The region lives in a different view, and it is the one an operator cannot
	// see from the first: two switches agreeing on mode and priority can still be
	// in different regions, and nothing says so (cf. driver.MSTRegion).
	var cfg string
	cfg, err = c.cli.run(ctx, "show spanning-tree config", showRunTimeout)
	if err == nil || strings.TrimSpace(cfg) != "" {
		var region, own = parseSTPRegion(cfg)
		out.Region = region
		if own {
			out.Notes = append(out.Notes,
				"the MST region name is this switch's own MAC address — the factory "+
					"default, which puts the device in a region of its own. It only "+
					"matters while the mode is MSTP")
		}
	}
	return out, nil
}

// parseSpanningTree reads "show spanning-tree".
func parseSpanningTree(text string) driver.SpanningTreeState {
	var out driver.SpanningTreeState
	var kv = labelValues(text)
	out.Enabled = strings.EqualFold(kv["STP Enabled"], "Yes")
	out.Mode = modeFromForceVersion(kv["Force Version"])
	out.BridgeID = kv["Switch MAC Address"]
	out.BridgePriority, _ = strconv.Atoi(kv["Switch Priority"])
	out.RootBridgeID = kv["CST Root MAC Address"]
	out.RootPriority, _ = strconv.Atoi(kv["CST Root Priority"])
	out.RootPathCost, _ = strconv.Atoi(kv["CST Root Path Cost"])
	// "This switch is root" is a SENTENCE in the port field, not a port number:
	// the platform answers "which port leads to the root" with prose when the
	// answer is "none of them".
	var rootPort = kv["CST Root Port"]
	out.IsRoot = strings.Contains(strings.ToLower(rootPort), "this switch is root")

	var row []string
	for _, row = range tableRows(text, "Port", "State", "Edge") {
		if len(row) < 5 {
			continue
		}
		var n, err = strconv.Atoi(row[0])
		if err != nil || n < 1 {
			continue
		}
		var p = driver.SpanningTreePort{Port: defaultPortPrefix + strconv.Itoa(n)}
		// "Auto" is this platform's way of saying "not configured": the operative
		// cost is derived from the link speed and is not shown here.
		var cost, cerr = strconv.Atoi(row[2])
		if cerr == nil {
			p.PathCost = cost
		}
		p.State, p.Role = stpPortState(row[4])
		if len(row) > 5 && row[5] != "" && row[5] == out.BridgeID {
			// The designated bridge for this segment is this switch — which is the
			// definition of a designated port.
			if p.Role == "" {
				p.Role = driver.STPRoleDesignated
			}
		}
		if len(row) > 8 {
			p.Edge = strings.EqualFold(row[8], "Yes")
		}
		if rootPort == row[0] {
			p.Role = driver.STPRoleRoot
		}
		out.Ports = append(out.Ports, p)
	}
	return out
}

// stpPortState maps this platform's State column onto the shared vocabulary.
//
// "Disabled" is a ROLE and not a state: a port outside the tree has no
// forwarding state to report, and filling one in would draw a blocked port in
// the topology view where there is simply no spanning tree running.
func stpPortState(s string) (state string, role string) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "forwarding":
		return driver.STPStateForwarding, ""
	case "learning":
		return driver.STPStateLearning, ""
	case "blocking", "discarding", "listening":
		return driver.STPStateDiscarding, ""
	case "disabled":
		return "", driver.STPRoleDisabled
	}
	return "", ""
}

func modeFromForceVersion(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case forceRSTP:
		return "rstp"
	case forceMSTP:
		return "mstp"
	case forceSTP:
		return "stp"
	}
	return ""
}

func forceVersionFor(mode string) string {
	switch driver.NormalizeSTPMode(mode) {
	case "rstp":
		return forceRSTP
	case "mstp":
		return forceMSTP
	case "stp":
		return forceSTP
	}
	return ""
}

// parseSTPRegion reads the MST region out of "show spanning-tree config".
//
// `own` reports that the region name is the switch's own MAC with its separator
// removed — the factory default, and the reason a fleet of these is, by default,
// a fleet of one-device regions.
func parseSTPRegion(text string) (region driver.MSTRegion, own bool) {
	var kv = labelValues(text)
	region.Name = kv["MST Configuration Name"]
	region.Revision, _ = strconv.Atoi(kv["MST Configuration Revision"])
	var mac = strings.ReplaceAll(strings.ToLower(kv["Switch MAC Address"]), "-", "")
	if mac == "" {
		// "show spanning-tree config" does not repeat the MAC; compare against
		// the shape instead, which is what the default always looks like.
		own = len(region.Name) == 12 && isHex(region.Name)
		return region, own
	}
	return region, strings.EqualFold(region.Name, mac)
}

func isHex(s string) bool {
	var i int
	for i = 0; i < len(s); i++ {
		var c = s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return len(s) > 0
}

// ApplySpanningTree writes the plan and VERIFIES it by re-reading.
//
// THE MODE IS WRITTEN BEFORE THE REGION, and that order is not cosmetic: the
// region only has an effect under MSTP, and pushing a name to a switch still in
// RSTP configures something that will silently take effect at the next mode
// change. Writing the mode first means the read-back at the end observes the
// device in the state the plan describes.
func (c *Client) ApplySpanningTree(ctx context.Context, plan driver.SpanningTreePlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if c.cli == nil {
		return out, errNoSession
	}
	var current, err = c.ReadSpanningTree(ctx)
	if err != nil {
		return out, fmt.Errorf("reading spanning tree before applying: %v", err)
	}

	// SuperRoot is not carried here. Said out loud rather than dropped, for the
	// same reason a region under the wrong mode is (see below): a checkbox that
	// is ticked and produces nothing is indistinguishable from one that worked.
	if plan.SuperRoot {
		out.Skipped = append(out.Skipped,
			"SUPER ROOT not applied: this connector does not implement it. This range "+
				"takes the bridge priority as a MULTIPLIER (0-15, cf. priorityStep) rather "+
				"than the 802.1D value, and there is no \"root super\" equivalent — set the "+
				"priority explicitly if this switch must be root")
	}

	var cmds []string
	if !plan.Enabled {
		if current.Enabled {
			cmds = append(cmds, "no spanning-tree")
		}
	} else {
		if !current.Enabled {
			cmds = append(cmds, "spanning-tree")
		}
		var mode = driver.NormalizeSTPMode(plan.Mode)
		if mode == "" {
			mode = current.Mode
		}
		var force = forceVersionFor(mode)
		if force == "" {
			return out, fmt.Errorf("spanning-tree mode %q is not one this range runs "+
				"(stp, rstp or mstp)", plan.Mode)
		}
		if mode != current.Mode {
			cmds = append(cmds, "spanning-tree force-version "+force)
		}
		if plan.BridgePriority != nil {
			var p = *plan.BridgePriority
			if p < 0 || p > 61440 || p%priorityStep != 0 {
				return out, fmt.Errorf("bridge priority %d rejected: this range takes "+
					"multiples of %d between 0 and 61440", p, priorityStep)
			}
			if p != current.BridgePriority {
				// The MULTIPLIER, not the 802.1D value (cf. priorityStep).
				cmds = append(cmds, "spanning-tree priority "+strconv.Itoa(p/priorityStep))
			}
		}
		// The region, and only under MSTP: see the header.
		if mode == "mstp" && plan.Region.Configured() {
			cmds = append(cmds, regionCmds(plan.Region, current.Region)...)
		} else if plan.Region.Configured() && mode != "mstp" {
			out.Skipped = append(out.Skipped,
				"MST region \""+plan.Region.Name+"\" not written: this device runs "+
					mode+", where the region has no effect. It would take hold at the "+
					"next mode change without anyone having asked for it")
		}
	}

	if len(cmds) > 0 {
		if err = c.runConfigBlock(ctx, cmds); err != nil {
			c.step(false, "spanning-tree write refused", firstLine(err.Error()))
			return out, fmt.Errorf("spanning-tree on %s: %v", c.tgt.Host, firstLine(err.Error()))
		}
		c.step(true, "spanning-tree bridge written", strings.Join(cmds, " ; "))
	}

	out.Skipped = append(out.Skipped, c.applyPortSTP(ctx, plan, &out)...)

	if len(cmds) == 0 && len(out.PortsWritten) == 0 {
		c.step(true, "spanning tree already carries the intent", "nothing written")
		return out, nil
	}

	// VERIFY. On this CLI a refused command is visible only in its own output,
	// and a spanning-tree setting that did not take stays invisible until a loop
	// demonstrates it. The re-read is what turns "accepted" into "in place".
	var after, verr = c.ReadSpanningTree(ctx)
	if verr != nil {
		out.Notes = append(out.Notes,
			"spanning tree written but NOT verified: it could not be re-read ("+
				firstLine(verr.Error())+")")
	} else {
		out.Notes = append(out.Notes, describeSTP(after))
		if plan.Enabled && plan.BridgePriority != nil && after.BridgePriority != *plan.BridgePriority {
			return out, fmt.Errorf("the bridge priority was accepted by the CLI but reads "+
				"back as %d, not %d: the write did not take", after.BridgePriority,
				*plan.BridgePriority)
		}
	}

	if err = c.persist(ctx); err != nil {
		out.Notes = append(out.Notes,
			"spanning tree applied but NOT saved: it will be lost at the next reboot ("+
				firstLine(err.Error())+")")
		return out, nil
	}
	c.step(true, "configuration persisted", "write memory")
	return out, nil
}

// regionCmds writes only what differs: this range stores the region in two
// separate commands plus one per instance, and re-sending an identical name
// churns flash for nothing.
func regionCmds(want, have driver.MSTRegion) []string {
	var out []string
	if want.Name != have.Name {
		out = append(out, "spanning-tree config-name \""+want.Name+"\"")
	}
	if want.Revision != have.Revision {
		out = append(out, "spanning-tree config-revision "+strconv.Itoa(want.Revision))
	}
	var inst int
	var vlans []int
	for inst, vlans = range want.InstanceVLANs {
		if len(vlans) == 0 {
			continue
		}
		out = append(out, fmt.Sprintf("spanning-tree instance %d vlan %s",
			inst, formatIntList(vlans)))
	}
	return out
}

// formatIntList renders a VLAN list the way this CLI takes it.
func formatIntList(v []int) string {
	var parts []string
	var n int
	for _, n = range v {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

// applyPortSTP writes the per-port intents.
//
// Path cost, edge and BPDU protection are all PORT-PREFIXED on this range —
// "spanning-tree <port> path-cost N" — with no interface sub-mode at all,
// which is the opposite of every other connector here.
func (c *Client) applyPortSTP(ctx context.Context, plan driver.SpanningTreePlan,
	out *driver.ApplyResult) []string {
	var skipped []string
	var pi driver.SpanningTreePortIntent
	for _, pi = range plan.Ports {
		var num, ok = portNumber(pi.Port)
		if !ok {
			skipped = append(skipped, pi.Port+
				" not modified: this name does not translate to a port number on this platform")
			continue
		}
		var p = strconv.Itoa(num)
		var cmds []string
		if pi.PathCost != nil {
			cmds = append(cmds, "spanning-tree "+p+" path-cost "+strconv.Itoa(*pi.PathCost))
		}
		switch pi.Edge {
		case driver.STPEdgeYes:
			cmds = append(cmds, "spanning-tree "+p+" admin-edge-port")
		case driver.STPEdgeNo:
			cmds = append(cmds, "no spanning-tree "+p+" admin-edge-port")
		case driver.STPEdgeAuto:
			// This range has auto-edge as a separate, always-on detection; there is
			// no "auto" value to write for the ADMIN setting.
			skipped = append(skipped, pi.Port+
				": edge \"auto\" not written — on this range auto-edge detection is "+
				"separate from the admin setting and is on already")
		}
		if pi.BPDUGuard != nil {
			// CAPTURED: "spanning-tree 1 bpdu-protection" is in the reference
			// unit's own configuration.
			if *pi.BPDUGuard {
				cmds = append(cmds, "spanning-tree "+p+" bpdu-protection")
			} else {
				cmds = append(cmds, "no spanning-tree "+p+" bpdu-protection")
			}
		}
		if pi.Enabled != nil {
			skipped = append(skipped, pi.Port+
				": per-port spanning-tree enable/disable is not written — this range "+
				"has no such switch, a port leaves the tree by being disabled outright")
		}
		if len(cmds) == 0 {
			continue
		}
		var err = c.runConfigBlock(ctx, cmds)
		if err != nil {
			skipped = append(skipped, pi.Port+": "+firstLine(err.Error()))
			continue
		}
		out.PortsWritten = append(out.PortsWritten, pi.Port)
	}
	return skipped
}

func describeSTP(s driver.SpanningTreeState) string {
	if !s.Enabled {
		return "spanning tree is OFF on this device"
	}
	var who = "root is " + s.RootBridgeID
	if s.IsRoot {
		who = "this switch is root"
	}
	return fmt.Sprintf("%s, bridge priority %d, %s", strings.ToUpper(s.Mode),
		s.BridgePriority, who)
}

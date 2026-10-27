package s3400

// Spanning tree.
//
// This platform's syntax is MODE-PREFIXED, which is the trap: the bridge
// priority is not "spanning-tree priority N" but "spanning-tree <mode>
// priority N", and sending the unprefixed form configures nothing. Captured
// 2026-08-03 on Version 2.0.2J:
//
//	config
//	 spanning-tree rstp priority 36864     → "Bridge ID Priority 36864" in show
//	 no spanning-tree rstp priority        → back to 32768
//	interface g0/1
//	 spanning-tree cost 20000              → appears in show running-config interface
//	 no spanning-tree cost
//
// The port cost IS unprefixed ("spanning-tree cost" applies in every mode,
// per the CLI reference §17.2) — the asymmetry is the platform's, not a typo.
//
// # SSTP AND MSTP WERE RUN, 2026-08-11, and the rule does not hold for MSTP
//
// Both modes were entered on poefs3400lab and a bridge priority written and read
// back in each (live_stpmodes_test.go). What came out:
//
//   - SSTP obeys the rule. "spanning-tree sstp priority 36864" is accepted,
//     "show spanning-tree" reports "Bridge ID Priority 36864" under a header
//     that reads "Spanning tree enabled protocol SSTP", the configuration
//     stores the line verbatim, and "no spanning-tree sstp priority" returns it
//     to 32768;
//   - MSTP DOES NOT. "spanning-tree mstp priority 36864" — the exact shape of
//     the rule — is answered "Too many parameters". The priority belongs to an
//     INSTANCE here, which the mode's own read says out loud: "Bridge ID
//     Priority 32768 (priority 32768 mst-id 0)". The working forms are
//     "spanning-tree mstp <instance> priority <n>" and "spanning-tree mst
//     <instance> priority <n>", both read back as 36864 on instance 0.
//
// AND THE TRAP THAT MAKES THIS WORTH WRITING DOWN: "spanning-tree mstp instance
// 0 priority 36864" — the Cisco-shaped guess, with the "instance" keyword —
// is ACCEPTED SILENTLY AND CONFIGURES NOTHING. So is "spanning-tree mstp
// instance 0 bridge priority 36864". The CLI returns no error and the bridge
// stays at 32768. A connector that inferred the MSTP form from the grammar of a
// neighbouring vendor would have reported a written priority on every S3400 in
// the fleet and changed no bridge at all.
//
// # WHY MSTP AND SSTP ARE STILL REFUSED BELOW
//
// Not for want of the forms — they are above. Because of what the mode CHANGE
// does, also measured 2026-08-11: "spanning-tree mode <m>" makes this device
// CLOSE THE SSH SESSION before the CLI answers. The tree reconverges, the only
// uplink is the session's own path, and [cliConn.runPaged] comes back with
// [errTruncated] over a command the device DID execute. For tens of seconds
// afterwards (~45 s measured, three successive reads) "show spanning-tree"
// answers "No spanning tree instance exists.", from which [parseSpanningTree]
// extracts nothing — so a read taken in that window reports a spanning tree that
// is OFF on a switch that is merely reconverging.
//
// [Client.ApplySpanningTree] has no answer to either: it would send the mode,
// lose the session, return an error over a device it HAD changed, and any
// verification it attempted next would read an empty tree. That is the gesture
// this connector is missing, and it is a bigger one than a command form.
//
// # THE MST REGION IS REFUSED, AND NOW SAYS SO (ADR-0022)
//
// This connector declares WritesExtra("stp") and writes NO region, and for a
// while nothing in the contract could tell those two apart. The consequence was
// measured on sw01-arena (ARENA) on 2026-10-25, read-only:
//
//	sw01-arena# show spanning-tree
//	Spanning tree enabled protocol RSTP(2004)
//	  Bridge ID  Priority    32768
//	             Address     649D.9929.F706
//
//	sw01-arena# show spanning-tree mst region
//	MST Region:
//	  Name:     [ 649D9929F706 ]
//	  Revision: [ 0 ]
//	  Instance 0     1-4094
//
// The region name is the bridge's OWN MAC — the factory default, i.e. a region
// of one — while the eight Aruba on the same L2 carry `mst-7f0d238df180`,
// revision 1, instance 1 = 10-11,42,60,98-100. The site's apply had succeeded on
// this switch and said nothing about any of it.
//
// TWO CLI FACTS FROM THAT SURVEY, both this range's own:
//
//   - the read form is "show spanning-tree mst region". "show spanning-tree
//     mst configuration" — the Arista/Cisco spelling — is answered "Too many
//     parameters", and "show spanning-tree region-configuration" likewise;
//   - "show spanning-tree mst" answers "No spanning-tree instance exists."
//     while the device is in RSTP, which is the SAME string the header above
//     reports during a post-mode-change reconvergence. A reader cannot tell
//     "wrong mode" from "reconverging" by that line alone.
//
// The WRITE grammar is still uncaptured, and that is why FeatMSTRegion is false
// rather than "unproven but shipped": capturing it means entering configuration
// mode on a production switch. Guessing is not available either — see the trap
// above, where the deduced MSTP priority form is accepted silently and
// configures nothing.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// stpModes: what this connector will drive. "sstp" is the platform's
// per-VLAN mode and "mstp" its multiple-instance one; both exist in the CLI
// and NEITHER has been exercised here, so they are named in the capability
// list (Capabilities) only to the extent they were verified — see s3400.go.
const (
	modeRSTP = "rstp"
	modeSSTP = "sstp"
	modeMSTP = "mstp"
)

// mstRegionRefusal is the line an operator reads when the site has an MST region
// and this connector cannot put the device in it. Empty when there is no region
// to refuse.
//
// A function rather than four lines inline, because it is the only OUTPUT of
// this whole decision (ADR-0022) and it has to be assertable without a device
// behind it. Capabilities' FeatMSTRegion:false gates a screen; THIS is what
// proves the operator was told.
func mstRegionRefusal(region driver.MSTRegion) string {
	if !region.Configured() {
		return ""
	}
	return "MST region \"" + region.Name + "\" NOT written: this connector drives RSTP " +
		"only on this range and does not express an MST region. THIS SWITCH IS " +
		"THEREFORE ITS OWN REGION — the factory name here is the bridge's own MAC " +
		"address — and it takes no part in the site's MST tree. It keeps forwarding, " +
		"so nothing will look wrong. Two things block it, neither a missing command: " +
		"\"spanning-tree mode <m>\" closes the SSH session before the CLI answers " +
		"and leaves the tree unreadable for about 45 s, and the region's write " +
		"grammar has never been captured on this platform (the read form is " +
		"\"show spanning-tree mst region\")"
}

// ReadSpanningTree reports the bridge and its ports.
func (c *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out, err = c.cli.runPaged(ctx, "show spanning-tree", 60*time.Second)
	if err != nil && strings.TrimSpace(out) == "" {
		return driver.SpanningTreeState{}, fmt.Errorf("show spanning-tree on %s: %w", c.host, err)
	}
	// ApplySpanningTree's pre-read, hence where that path's session learns the
	// cage numbering it will have to write (ADR-0018): this output names the
	// ports in the device's own short form, cages included.
	c.noteCageNumbering(out)
	var st = parseSpanningTree(out)

	// The mode also appears in the configuration, and that is the form the
	// apply path needs (the show line says "RSTP(2004)", the config says
	// "spanning-tree mode rstp").
	if st.Mode == "" {
		var cfgOut, cfgErr = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
		if cfgErr == nil || strings.TrimSpace(cfgOut) != "" {
			c.noteCageNumbering(cfgOut)
			var cfg = ParseRunningConfig(cfgOut)
			st.Mode = cfg.STPMode
			if cfg.STPOff {
				st.Enabled = false
			}
		}
	}
	if !st.IsRoot && st.RootBridgeID != "" {
		st.Notes = append(st.Notes, "root is another bridge ("+st.RootBridgeID+")")
	}
	return st, nil
}

// ApplySpanningTree writes the plan.
//
// The bridge priority is written with the MODE the device is actually in — a
// "spanning-tree rstp priority" on a switch running MSTP configures the
// wrong protocol's bridge, quietly. So the mode is read first, and a plan
// that asks for a mode this connector has not exercised is refused rather
// than approximated.
func (c *Client) ApplySpanningTree(ctx context.Context, plan driver.SpanningTreePlan) (ApplyResult, error) {
	var res ApplyResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}

	// SuperRoot is scoped to MST and this connector refuses every mode but RSTP
	// (see below), so it can never be honoured here. Said rather than dropped, per
	// driver.SpanningTreePlan.SuperRoot.
	if plan.SuperRoot {
		res.Skipped = append(res.Skipped,
			"SUPER ROOT not applied: this connector drives RSTP only on this range and "+
				"has no MST instance 0 to make this switch the root of. The bridge priority "+
				"is the root-election control here")
	}

	// A REGION THIS CONNECTOR CANNOT WRITE IS REFUSED OUT LOUD, and this is the
	// line ADR-0022 exists for. It is placed BEFORE the mode decision on purpose:
	// the site resolver picks rstp here (STPModes lists nothing else), so the
	// refusal below is never reached, and the region arrived anyway — stp_apply.go
	// fills SpanningTreePlan.Region for every device of the site. The result was a
	// successful apply over a switch left in a region of its own, with nothing
	// anywhere saying so.
	//
	// Said even when Enabled is false: a device whose tree is being turned off is
	// even further outside the region, and the operator reading that result is the
	// one who needs to know the site has a region this switch is not in.
	var refusal = mstRegionRefusal(plan.Region)
	if refusal != "" {
		res.Skipped = append(res.Skipped, refusal)
	}

	var current, readErr = c.ReadSpanningTree(ctx)
	if readErr != nil {
		return res, fmt.Errorf("reading spanning tree before applying: %w", readErr)
	}

	var mode = strings.ToLower(strings.TrimSpace(plan.Mode))
	if mode == "" {
		mode = current.Mode
	}
	if mode == "" {
		mode = modeRSTP
	}
	if mode != modeRSTP {
		return res, driver.NotSupported("fs-s3400", "spanning-tree mode "+mode,
			"\"sstp\" and \"mstp\" were RUN on this platform on 2026-08-11 and their "+
				"bridge-priority forms are captured (see this file's header), so the "+
				"refusal is no longer about the command. It is about the mode CHANGE: "+
				"\"spanning-tree mode <m>\" closes the SSH session before the CLI "+
				"answers, and \"show spanning-tree\" then reports no instance at all "+
				"for around 45 s — this connector would return an error over a device "+
				"it had already changed, and verify it against an empty tree. RSTP is "+
				"also the factory default, so nothing here needs the switch")
	}

	var cmds []string
	if !plan.Enabled {
		// "no spanning-tree" disables it globally (CLI reference §17.1.1).
		cmds = append(cmds, "no spanning-tree")
	} else {
		if current.Mode != mode {
			cmds = append(cmds, "spanning-tree mode "+mode)
		}
		if !current.Enabled {
			cmds = append(cmds, "spanning-tree")
		}
		if plan.BridgePriority != nil {
			var p = *plan.BridgePriority
			if p < 0 || p > 61440 || p%4096 != 0 {
				return res, fmt.Errorf("bridge priority %d rejected: this platform takes "+
					"multiples of 4096 between 0 and 61440", p)
			}
			// Read-compare-decide, same discipline as the mode and enable
			// lines above: a priority the bridge already carries is not
			// rewritten. BridgeID non-empty is the proof the comparison rests
			// on a read bridge section, not on a zero default.
			if current.Enabled && current.Mode == mode &&
				current.BridgeID != "" && current.BridgePriority == p {
				c.step(true, "bridge priority already in place",
					fmt.Sprintf("%d on the %s bridge — nothing to write", p, mode))
			} else {
				cmds = append(cmds, fmt.Sprintf("spanning-tree %s priority %d", mode, p))
			}
		}
	}

	if len(cmds) > 0 {
		if err = c.configBlock(ctx, cmds); err != nil {
			c.step(false, "spanning-tree write refused", firstLine(err.Error()))
			return res, fmt.Errorf("spanning-tree on %s: %w", c.host, err)
		}
		res.Notes = append(res.Notes, "bridge: "+strings.Join(cmds, " ; "))
		c.step(true, "spanning-tree bridge written", strings.Join(cmds, " ; "))
	}

	// Per-port intents.
	var pi driver.SpanningTreePortIntent
	for _, pi = range plan.Ports {
		var dev, nerr = c.cages.cliPortName(pi.Port)
		if nerr != nil {
			// Refused before the block is opened — ADR-0018.
			res.Skipped = append(res.Skipped, pi.Port+": "+nerr.Error())
			continue
		}
		var portCmds = []string{"interface " + dev}
		var wrote bool
		if pi.PathCost != nil {
			// Unprefixed on purpose: "spanning-tree cost" applies in every
			// mode on this platform (captured on g0/1).
			portCmds = append(portCmds, fmt.Sprintf("spanning-tree cost %d", *pi.PathCost))
			wrote = true
		}
		if pi.Enabled != nil && !*pi.Enabled {
			portCmds = append(portCmds, "no spanning-tree")
			wrote = true
		}
		if pi.Edge != "" || pi.BPDUGuard != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: edge/BPDU-guard not written — the portfast and guard forms of this "+
					"CLI have not been run on hardware", pi.Port))
		}
		if !wrote {
			continue
		}
		portCmds = append(portCmds, "exit")
		if err = c.configBlock(ctx, portCmds); err != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", pi.Port, err))
			continue
		}
		res.PortsWritten = append(res.PortsWritten, pi.Port)
	}

	if len(res.PortsWritten) > 0 || len(res.Notes) > 0 {
		res.Notes = append(res.Notes,
			"applied to the running configuration; \"write\" is what makes it survive a reboot")
	}
	return res, nil
}

package eos

// Spanning tree — read and write.
//
// # EOS defaults to MSTP, and that changes what "apply" means
//
// Every other platform in this fleet ships with something else on: IOS with
// PVST+, the FS ranges with RSTP, SwOS with its own single instance. EOS ships
// with MSTP running, in a region named after the bridge's own MAC address —
// which means a factory Arista is in a REGION OF ONE, and stays there,
// forwarding perfectly, invisible to the region every other MSTP switch in the
// site belongs to.
//
// That is the silent failure 802.1s is famous for (cf. driver.MSTRegion): a
// region mismatch does not error, does not drop a link, and does not log. The
// neighbour is simply treated as an ordinary RSTP bridge and the topology stops
// behaving the way the design says. So on this platform the region is not a
// refinement of the mode — it is the entire point of applying STP at all, and a
// device left at its default is a device that looks configured.
//
// # ORDER IS NOT FREE
//
// The region is configured BEFORE the mode is set, for the same reason as on
// IOS: a device entering mstp carrying a default region joins a region of one and
// re-converges a second time when the real region lands. Writing the region first
// makes it one transition instead of two. On EOS the switch is usually ALREADY in
// mstp, which makes the ordering matter more rather than less — the mode command
// is then a no-op and the region write is the whole of the change.
//
// # STATUS: THE READS ARE CAPTURED, AND SINCE 2026-08-12 SO IS THE WRITE
//
// [Driver.WritesExtra] returns TRUE for "stp". What follows is the history of
// that flag, kept in full because the reasoning is what a future reader needs
// when deciding whether to trust the remaining forms — three of which are still
// documentation-derived and now ride the automatic apply path.
//
// THE RUN THAT SETTLED IT, 2026-08-12, through [Client.ApplySpanningTree] rather
// than typed: core01.lab carried priority 32768 and was NOT the root — the
// tree was rooted on an unmanaged CRS305 at priority 16384, with ET48 as the root
// port. meshcraft had DESIGNATED core01 the site's root bridge and had never
// written it, because nothing in the controller read the designation
// (internal/api/stp_rootbridge.go, rootDesignation, which had no caller). The
// applier sent the region, the mode and the root half in its own two blocks; the
// device answered in ten seconds and now reads:
//
//	spanning-tree mode mstp
//	spanning-tree root super
//	spanning-tree mst 0-1 priority 0
//
// "show spanning-tree mst 1" answers "Root this switch for MST1" on the
// instance holding VLANs 42,98-100 — the management VLAN among them — and all
// four ports came back designated/forwarding. The fleet blipped and every device
// recovered, which is the blast radius this header already measured.
//
// THE THREE FORMS BELOW THAT ARE STILL ONLY DOCUMENTED did not become true that
// day and are the reason this paragraph is not shorter: "spanning-tree mst 0
// priority N" under mode RSTP, the per-VLAN "spanning-tree vlan-id" spelling,
// and "spanning-tree portfast auto". core01 is in mstp, so the run exercised
// the mstp branch alone.
//
// `show spanning-tree` and `show spanning-tree mst configuration` were captured
// from core01.lab on 2026-08-08 and are pinned in stp_live_test.go, and that
// capture immediately found a defect four invented fixtures had missed (cf.
// [reMSTName]). The two shapes TODO.md still owed arrived on 2026-08-10 —
// testdata/show_spanning_tree_mst_multi.txt (three MST instances that disagree
// about who the root is) and
// testdata/show_spanning_tree_mst_configuration_wrapped.txt (an instance VLAN
// list wrapped over six lines) — and they settled the two claims that had never
// met hardware. One held: bridge facts do not leak between MST0, MST1 and MST2,
// which had been fixed by inspection on 2026-08-07 with nothing to check it
// against. One did not: the wrapped list lost 83 of instance 1's 100 VLANs, the
// gap [reMSTInstCont] now closes. Nothing about the reads is deduced any more.
//
// THE READS DID NOT FLIP IT, and that distinction is worth keeping now that the
// flag has moved for another reason. Both captures above were produced by TYPING
// the region change at the CLI; nothing went through [Client.ApplySpanningTree],
// so a read capture, however good, was never evidence about the write path. This
// entry's own bar was "flip after the mode + region write is SEEN", and that bar
// was MET on 2026-08-12 by the run in the STATUS section — the applier's own
// plan/verify path, its config-block handling and its re-read, against real EOS
// rather than the fake CLI in stp_test.go.
//
// The staging rule is applied per intent and not per connector, and the forms
// that remain LEAST CERTAIN are unchanged by that run — core01 is in mstp, so
// the mstp branch is the only one it exercised. TODO.md names them, and they are
// now shipping behind a declared flag rather than held back by it:
//
//   - "spanning-tree mst 0 priority N" under mode rstp — kept on the deduction
//     that EOS models single-instance RSTP as the CIST. The first capture to take;
//   - "spanning-tree vlan-id <ids> priority N" — EOS's per-VLAN spelling, NOT
//     IOS's "spanning-tree vlan". Not reachable from [eosSTPMode] today;
//   - "spanning-tree portfast auto" — declared in eos.go as FeatSTPEdgeAuto and
//     written here, on documentation alone.
//
// "spanning-tree root super" IS NO LONGER ONE OF THEM — CAPTURED 2026-08-11 on
// core01 (DCS-7050SX-64-R, EOS 4.28.13.1M). It EXISTS, and it lives in GLOBAL
// CONFIG, which is where this file already put it:
//
//	spanning-tree root ?    →  primary | secondary | super
//	spanning-tree root super ?  →  <cr>
//
// It is still sent in a config block OF ITS OWN, and that stays: one train
// answering is not every train answering, [Client.runConfigBlock] abandons a
// block at the first refusal, and inside the main block an unknown keyword would
// take the mode and the region down with it. A refusal is a Skipped line naming
// the command verbatim, never an error.
//
// TWO FACTS FROM THAT CAPTURE THAT THE NEXT WRITER NEEDS.
//
//   - THE NEGATION MUST NAME THE KEYWORD. Bare "no spanning-tree root" is
//     accepted SILENTLY and does NOT clear it; only "no spanning-tree root
//     super" does. CLOSED 2026-08-12: [eosPriorityCmds] emits it on every mstp
//     priority write, in its own block. The gap it closed was not merely the undo
//     — an explicit priority is FALSE while the keyword stands, since the device
//     accepts the number and goes on winning the election;
//   - THERE IS A READ-BACK. The running configuration renders the line verbatim,
//     so "does this switch carry Super Root" is answerable from the same
//     `show running-config` this connector already reads.
//     [driver.SpanningTreeState] still has no `super_root` field — adding one is
//     a contract change across nine connectors, not an Arista edit.
//
// THE BLAST RADIUS IS MEASURED, on the fleet core, because the command RAN there
// by accident (tools/labssh submits the residual of a `?` probe): MST0's root
// moved to a synthetic address, Vlan99 bounced, three ports went
// discarding→forwarding in ~1 s, three devices logged unreachable and recovered
// in 2–5 s, and the whole recovery took ~45 s.
//
// Every one of those WAS a reason WritesExtra stayed false, and the flag moved
// anyway — on an operator's explicit decision, with the uncaptured forms named
// (see the STATUS section). What that buys is the root-bridge designation
// finally reaching a device; what it costs is that those three forms now ride
// the auto-apply path, where a refusal is discovered by a pipeline run rather
// than by somebody watching. live_stp_test.go's refusal to write a region on
// this box is now the only thing between an automatic pass and that write.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin reads and writes spanning tree.
var (
	_ driver.SpanningTreeReader  = (*Client)(nil)
	_ driver.SpanningTreeApplier = (*Client)(nil)
)

// eosSTPMode renders meshcraft's vocabulary as the keyword EOS wants.
//
// "rstp" is a REAL MODE here, and that is the difference from the Cisco
// connector, which has to map the fleet's single-instance RSTP onto
// "rapid-pvst" — RSTP-per-VLAN, which is not the same protocol and does not
// interoperate the same way with a non-Cisco neighbour. EOS carries plain
// single-instance RSTP under its own name, so the fleet's vocabulary maps
// one-to-one and nothing has to be approximated.
//
// Legacy 802.1D has NO mode on this platform. It is not in
// [driver.Capabilities.STPModes] for that reason, and the caller's resolver
// (driver.ResolveSTPMode) will pick the next preference down rather than
// arriving here with it.
func eosSTPMode(mode string) (string, bool) {
	switch driver.NormalizeSTPMode(mode) {
	case "mstp":
		return "mstp", true
	case "rstp":
		return "rstp", true
	}
	return "", false
}

// ApplySpanningTree writes the mode, the MST region and the bridge priority.
func (c *Client) ApplySpanningTree(ctx context.Context,
	plan driver.SpanningTreePlan) (driver.ApplyResult, error) {

	var out driver.ApplyResult
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this device")
	}
	var werr = c.requireWritableDialect()
	if werr != nil {
		return out, werr
	}

	if !plan.Enabled {
		// EOS DOES have a global off switch — "spanning-tree mode none" — which
		// IOS does not, so unlike cisco/ios this connector CAN honour the request.
		// It is honoured because "none" is an explicit entry in the site
		// preference catalog (driver.STPPrefNone): somebody chose it from a list,
		// which is different from a connector deciding on its own that loop
		// protection is optional. The note is deliberately loud.
		var err = c.runConfigBlock(ctx, []string{"spanning-tree mode none"})
		if err != nil {
			c.step(false, "spanning tree NOT disabled", firstLine(err.Error()))
			return out, err
		}
		out.Notes = append(out.Notes,
			"SPANNING TREE IS NOW OFF on this switch (\"spanning-tree mode none\"). It will "+
				"not detect a loop, and a single mis-patched cable between two of its ports "+
				"is enough to saturate every VLAN it carries. This was applied because the "+
				"site preference names \"none\" explicitly")
		c.step(true, "spanning tree disabled", "spanning-tree mode none")
		return out, nil
	}

	var mode, ok = eosSTPMode(plan.Mode)
	if !ok {
		out.Skipped = append(out.Skipped, "unsupported STP mode "+plan.Mode+
			": this platform carries mstp and rstp (and rapid-pvst, which meshcraft does not "+
			"select). Legacy 802.1D has no mode of its own here — an RSTP bridge falls back "+
			"to it per port when it hears a legacy BPDU, which is the interoperability this "+
			"preference was really asking for")
		return out, nil
	}

	var cmds []string
	// 1. The region FIRST (cf. the header), and only when there is one to write.
	if mode == "mstp" && plan.Region.Configured() {
		// THE NAME IS REFUSED, NOT SUBSTITUTED, when nothing survives the filter.
		// sanitizeRegionName used to fall back to the literal "meshcraft", which is
		// the one alternative ADR-0021 named and threw out — a single fixed name for
		// the whole instance — reached by accident rather than by choice: two sites
		// pinned to distinct names that both sanitise away would have been handed
		// the SAME region name, and two sites sharing a name is how two sites merge
		// into one region with nothing on any screen to contradict it.
		var name = sanitizeRegionName(plan.Region.Name)
		if name == "" {
			out.Skipped = append(out.Skipped,
				"MST region \""+plan.Region.Name+"\" NOT written: no character of that name "+
					"survives what EOS accepts for a region name (letters, digits, - and _). "+
					"This switch therefore stays in a region of its own and switches OUTSIDE "+
					"the site's tree. Pin a name made of those characters on the site plan "+
					"(stp_mst_region) rather than letting one be invented here — a region name "+
					"is a third of an identity, and an invented one is shared by every site "+
					"that trips the same case")
			return out, nil
		}
		// THE REVISION IS RANGE-CHECKED BEFORE IT BECOMES A LINE. 802.1s gives it 16
		// bits; EOS refuses anything outside, and runConfigBlock abandons a block at
		// the first refusal — so an out-of-range revision did not merely fail to
		// write the revision, it took the region AND the mode line down with it.
		if plan.Region.Revision < 0 || plan.Region.Revision > 65535 {
			return out, fmt.Errorf(
				"MST region revision %d rejected: 802.1s encodes it in 16 bits, so this "+
					"platform takes 0-65535. Refused before anything is sent, because the "+
					"region and the mode travel in one config block and EOS abandons the "+
					"block at the first line it refuses",
				plan.Region.Revision)
		}
		cmds = append(cmds,
			"spanning-tree mst configuration",
			"name "+name,
			"revision "+strconv.Itoa(plan.Region.Revision))
		var inst int
		for _, inst = range sortedInstances(plan.Region.InstanceVLANs) {
			var vlans = plan.Region.InstanceVLANs[inst]
			if len(vlans) == 0 {
				continue
			}
			// INSTANCE 0 IS NEVER EMITTED. The CIST is implicit and holds whatever no
			// other instance claims (cf. driver.MSTRegion.InstanceVLANs, which says it
			// is never listed), and "instance 0 vlan …" is a line EOS refuses — which
			// would abandon the block and lose the region and the mode with it.
			// SiteMSTRegion only ever builds instance 1 today, so this guard is for the
			// map arriving from anywhere else; it is said out loud rather than dropped
			// because a caller that mapped VLANs into the CIST on purpose asked for
			// something this platform cannot express.
			if inst == 0 {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"MST instance 0 (%s) not written: instance 0 is the CIST and is implicit "+
						"on this platform — it carries every VLAN no other instance claims, and "+
						"cannot be given an explicit VLAN list. Those VLANs are in it already "+
						"unless another instance names them", joinInts(vlans)))
				continue
			}
			cmds = append(cmds, fmt.Sprintf("instance %d vlan %s", inst, joinInts(vlans)))
		}
		cmds = append(cmds, "exit")
	}

	// 2. The mode.
	cmds = append(cmds, "spanning-tree mode "+mode)

	// A region supplied for a mode that has none is NOT written, and is said out
	// loud rather than dropped: under rstp the region name would sit in the
	// configuration doing nothing until somebody switched the switch to mstp
	// months later, at which point it would take hold without anyone having asked
	// for it. Same treatment, same wording, as hpe/procurve/stp.go.
	if mode != "mstp" && plan.Region.Configured() {
		out.Skipped = append(out.Skipped,
			"MST region \""+plan.Region.Name+"\" not written: this switch is being put in "+
				mode+", where the region has no effect. Written now it would take hold at "+
				"the next mode change, without anyone having asked for it")
	}

	// 3. Bridge priority — AND THE COMMAND DEPENDS ON THE MODE (cf.
	//    [eosPriorityCmds]). The first version of this file sent the mst form
	//    unconditionally, which is a line a switch outside mstp rejects.
	//
	//    `after` is the SuperRoot half, and it is deliberately NOT in this block:
	//    see the file header and [eosPriorityCmds].
	var after []string
	if plan.BridgePriority != nil || plan.SuperRoot || len(plan.RootInstances) > 0 {
		// The priority travels as the POINTER it arrived as: a refused SuperRoot
		// with no explicit priority must write NOTHING, and an int defaulted to 0
		// here is exactly how the first version wrote priority 0 while announcing
		// it had not (cf. eosPriorityCmds).
		var prio, extra, skip = eosPriorityCmds(mode, plan.BridgePriority,
			plan.SuperRoot, plan.RootsOf())
		cmds = append(cmds, prio...)
		after = extra
		if skip != "" {
			out.Skipped = append(out.Skipped, skip)
		}
		if plan.SuperRoot && mode == "mstp" && plan.BridgePriority != nil &&
			clampBridgePriority(*plan.BridgePriority) != 0 {
			// Both were set and they contradict each other. The contract
			// (driver.SpanningTreePlan.SuperRoot) says SuperRoot wins and that the
			// connector must say which one it obeyed — otherwise the screen keeps
			// showing a priority that no longer describes the switch.
			out.Notes = append(out.Notes, fmt.Sprintf(
				"SUPER ROOT was requested together with bridge priority %d; the priority was "+
					"IGNORED and 0 written instead. Nothing sorts below 0, so this switch now "+
					"wins the root election of its region and cannot be displaced by raising "+
					"anyone else's priority", clampBridgePriority(*plan.BridgePriority)))
		}
	}

	// 2b. BPDU FILTERING ON THE INTER-ROOT PATH — WRITTEN FIRST, IN ITS OWN BLOCK.
	//
	// This is the ordering the whole multi-root design rests on
	// ([driver.RootApplyOrder]), and it is a SEPARATE `runConfigBlock` on purpose
	// rather than lines prepended to `cmds`: the two halves must land in two
	// device transactions, or there is no "before" — the switch would apply the
	// filter and the root priority in the same commit and the ordering would be a
	// comment rather than a property.
	//
	// Why this order and not the intuitive one, in one sentence, because a future
	// reader WILL be tempted to merge these blocks: between the two writes the
	// link carries no BPDUs and this switch is still an ordinary bridge, so the
	// trees are already separated before anything claims to be root. Reversed,
	// two bridges claim priority 0 on one tree while BPDUs still flow, they
	// contest the election, and the tree reconverges in the middle of meshcraft's
	// own apply.
	//
	// A FAILURE HERE ABORTS THE WHOLE PLAN. Not a Skipped line: applying the root
	// half without the filter is precisely the contested-election state this
	// ordering exists to avoid, and leaving it in place permanently would be
	// worse than changing nothing.
	if len(plan.BPDUFilterPorts) > 0 {
		var filterCmds []string
		var p string
		for _, p = range plan.BPDUFilterPorts {
			filterCmds = append(filterCmds,
				"interface "+p,
				// EOS spells it as a per-interface spanning-tree sub-command. This
				// form is DOCUMENTED, NOT CAPTURED — like everything else that
				// writes on this platform.
				"spanning-tree bpdufilter enable",
				"exit")
		}
		var ferr = c.runConfigBlock(ctx, filterCmds)
		if ferr != nil {
			c.step(false, "BPDU filtering NOT applied — root designation abandoned",
				firstLine(ferr.Error()))
			return out, fmt.Errorf("BPDU filtering on the inter-root path failed on %s, so "+
				"nothing was made root: applying the root half without the filter would leave "+
				"two bridges contesting one election with BPDUs still flowing (%v)",
				c.host, ferr)
		}
		out.Notes = append(out.Notes, fmt.Sprintf(
			"BPDU FILTERING enabled on %d port(s) BEFORE any root priority was written: %s. "+
				"These ports now forward traffic while taking NO part in spanning tree, which "+
				"is safe only because no redundant path exists between the root bridges",
			len(plan.BPDUFilterPorts), strings.Join(plan.BPDUFilterPorts, ", ")))
		c.step(true, "BPDU filtering on the inter-root path",
			strings.Join(plan.BPDUFilterPorts, ", "))
	}

	var err = c.runConfigBlock(ctx, cmds)
	if err != nil {
		c.step(false, "spanning tree NOT applied", firstLine(err.Error()))
		return out, err
	}
	out.Notes = append(out.Notes, "spanning-tree mode "+mode)

	// 4. The SuperRoot keyword, in a block of its own and only now that the
	//    priority has landed. A refusal here is a Skipped line and NOT an error:
	//    the priority-0 half is the half that actually elects the root, and it has
	//    already been written. Losing the whole apply because an unverified
	//    keyword does not exist on this train would be the expensive outcome.
	if len(after) > 0 {
		var serr = c.runConfigBlock(ctx, after)
		if serr != nil {
			out.Skipped = append(out.Skipped, "\""+strings.Join(after, " ; ")+"\" was REFUSED by "+
				"the CLI ("+firstLine(serr.Error())+"). The keyword is captured on EOS "+
				"4.28.13.1M and this train does not carry it. The priority-0 half of "+
				"Super Root DID land, and that is the half that decides the root election; "+
				"what is missing is whatever additional protection the keyword confers")
			c.step(false, "super root keyword refused", strings.Join(after, " ; "))
		} else {
			out.Notes = append(out.Notes, "SUPER ROOT: "+strings.Join(after, " ; ")+
				" (global config, captured on EOS 4.28.13.1M). CLEARING IT LATER TAKES "+
				"\"no spanning-tree root super\" — the bare \"no spanning-tree root\" is "+
				"accepted silently and leaves it in place")
			c.step(true, "super root", strings.Join(after, " ; "))
		}
	}

	if mode == "mstp" && plan.Region.Configured() {
		out.Notes = append(out.Notes, fmt.Sprintf("MST region %q revision %d",
			plan.Region.Name, plan.Region.Revision))
	} else if mode == "mstp" {
		out.Notes = append(out.Notes,
			"NO MST REGION was supplied, so this switch keeps the default one — which EOS "+
				"names after the bridge's own MAC address. That puts it in a region of ONE: "+
				"it forwards, it looks healthy, and every other MSTP switch on the site "+
				"treats it as an ordinary RSTP bridge. A region mismatch never errors and "+
				"never logs, which is why this is said here")
	}

	// Per-port intents, applied after the global ones so a port cannot be
	// configured for a mode the switch is not in yet.
	out.Skipped = append(out.Skipped, c.applySTPPorts(ctx, plan.Ports, &out)...)

	c.step(true, "spanning tree applied", strings.Join(out.Notes, " ; "))
	return out, nil
}

// applySTPPorts writes the per-port intents and returns what it refused.
//
// Every intent field is OPTIONAL (nil / empty leaves the device alone), so a
// port with nothing to say produces no command at all — which matters on a
// 48-port switch where a plan naming every port would otherwise send two hundred
// lines to change nothing.
func (c *Client) applySTPPorts(ctx context.Context,
	ports []driver.SpanningTreePortIntent, out *ApplyResult) []string {

	var skipped []string
	var it driver.SpanningTreePortIntent
	for _, it = range ports {
		var cliName = cliPortName(it.Port)
		if cliName == "" {
			skipped = append(skipped, it.Port+" not touched: this name is outside the "+
				"standard-model mapping this connector translates")
			continue
		}
		var body []string
		if it.Enabled != nil && !*it.Enabled {
			// ONLY the "off" half is refused, and narrowing it to that was a fix:
			// "enabled: true" is what EOS already does on every port, so refusing
			// it put a loud skip line on every port of an entirely ordinary plan —
			// which is how a Skipped list stops being read.
			//
			// EOS has no per-port spanning-tree on/off switch. Turning a port's
			// participation off is expressed with a BPDU filter, which is a
			// different thing with different consequences — it hides the port from
			// the topology rather than excluding it — and substituting one for the
			// other would give an operator protection they did not ask for and
			// remove protection they were relying on.
			skipped = append(skipped, it.Port+": taking a port OUT of spanning tree is NOT "+
				"written — this platform has no per-port on/off switch, and the nearest "+
				"thing (\"spanning-tree bpdufilter\") means something else. The port stays "+
				"in the tree")
		}
		switch driver.NormalizeSTPEdge(it.Edge) {
		case driver.STPEdgeYes:
			body = append(body, "spanning-tree portfast")
		case driver.STPEdgeNo:
			body = append(body, "no spanning-tree portfast")
		case driver.STPEdgeAuto:
			body = append(body, "spanning-tree portfast auto")
		}
		if it.BPDUGuard != nil {
			if *it.BPDUGuard {
				body = append(body, "spanning-tree bpduguard enable")
			} else {
				body = append(body, "spanning-tree bpduguard disable")
			}
		}
		if it.PathCost != nil && *it.PathCost > 0 {
			body = append(body, "spanning-tree cost "+strconv.Itoa(*it.PathCost))
		}
		if len(body) == 0 {
			continue
		}
		var cmds = append([]string{"interface " + cliName}, body...)
		cmds = append(cmds, "exit")
		var err = c.runConfigBlock(ctx, cmds)
		if err != nil {
			skipped = append(skipped, it.Port+": the CLI refused ("+firstLine(err.Error())+")")
			continue
		}
		out.PortsWritten = append(out.PortsWritten, it.Port)
		c.step(true, it.Port+": spanning tree", strings.Join(body, " ; "))
	}
	return skipped
}

// eosPriorityCmds renders the bridge priority for THE MODE THE SWITCH WILL BE
// IN, which is not one command on this platform, plus the SuperRoot half.
//
// PURE ON PURPOSE — no session, no context, no side effect — because the three
// mode branches and the SuperRoot interaction are the part of this file most
// likely to be wrong, and a function that only builds strings can be asserted
// directly (cf. stp_test.go). The forms below are documentation-derived except
// "spanning-tree root super", captured 2026-08-11 (cf. the file header);
// nothing here has been sent BY THIS CONNECTOR to an Arista.
//
// THREE RETURN VALUES AND NOT ONE, and the split is the whole point:
//
//   - `cmds` goes in the main configuration block with the mode and the region;
//   - `after` goes in a SEPARATE block that the caller runs only once the first
//     one has succeeded. [Client.runConfigBlock] abandons a block at the first
//     refusal, so "spanning-tree root super" — captured on ONE train, EOS
//     4.28.13.1M — placed next to the mode would take the mode and the region
//     down with it on any train that spells it differently. On its own it can
//     only lose itself;
//   - `skipped`, non-empty when SuperRoot was asked for in a mode that has no
//     instance 0 to be root of. Saying so is the contract's rule for a field a
//     connector cannot carry (driver.SpanningTreePlan.SuperRoot).
//
// The first version of this file sent "spanning-tree mst 0 priority N" for
// every mode. That is right under mstp — instance 0 is the CIST, the tree every
// VLAN rides when instances are not load-balanced — and it is a rejected line
// anywhere else, so the priority silently never landed on an rstp switch while
// the mode command right before it succeeded. A half-applied STP block is worse
// than a refused one: the mode converges, the root election does not, and
// nothing on screen distinguishes that from a device that simply lost.
//
// Under rstp EOS still exposes the single instance as MST 0 — RSTP is the CIST
// with no additional instances — so the mst form is kept there and named as the
// branch it is. THIS IS THE ONE FORM IN THIS FILE MOST LIKELY TO BE WRONG, and
// TODO.md names it as the first capture to take.
//
// THE NAMED FALLBACK IS REAL SYNTAX, captured 2026-08-11 on core01 (EOS
// 4.28.13.1M): "spanning-tree priority ?" answers "<0-61440> in increments of
// 4096", so the bare global form exists on this train. What that does NOT settle
// is which of the two an RSTP-MODE switch accepts — core01 is in mstp, and only a
// mode change answers it. So the fallback is written down and still not
// selected: nothing here branches on it.
//
// "rapid-pvst" IS DEAD CODE TODAY, and saying so precisely matters more than
// the branch does. Nothing reaches it: [eosSTPMode] returns only "mstp" or
// "rstp", [Client.ApplySpanningTree] returns early on anything else, and a site
// preference spelled `rapid-pvst` is folded into "rstp" by
// driver.NormalizeSTPMode before it ever gets here. It is NOT kept against a mode
// word arriving from somewhere this connector does not control — there is no such
// path — and a comment claiming one would stop the next reader from deleting a
// branch that has genuinely become unreachable.
//
// It is kept for one reason, and only until the capture lands: EOS spells the
// per-VLAN form "spanning-tree vlan-id <ids> priority N", NOT the IOS
// "spanning-tree vlan", and that one-word difference is the exact thing this
// package exists to keep straight. The day an rstp-mode switch refuses
// "spanning-tree mst 0 priority N", this is the written-down alternative its
// refusal gets compared against. If that capture instead confirms the mst form,
// this branch has served its purpose and should go.
// `instances` are the MST instances to write the priority on. Empty means the
// CIST alone, which is what the old boolean-only form always did.
func eosPriorityCmds(eosMode string, priority *int, superRoot bool,
	instances []int) (cmds []string, after []string, skipped string) {

	if len(instances) == 0 {
		instances = []int{0}
	}

	if superRoot && eosMode != "mstp" {
		// The contract scopes SuperRoot to mstp, and refusing it here rather than
		// approximating it is deliberate: outside MST there is no instance 0 to be
		// root of, and "priority 0 on every VLAN" is a different, much larger
		// change than the operator ticked a box for.
		skipped = "SUPER ROOT not applied: this switch is being put in " + eosMode +
			", and \"root of the region\" only means something under mstp — there is no " +
			"MST instance 0 to win. Nothing was written for it"
		superRoot = false
		// AND THEN NOTHING IS WRITTEN, which is the whole point of the refusal.
		//
		// The first version of this function fell through here with a local
		// `priority` that had been defaulted to 0 by its caller, and emitted
		// "spanning-tree mst 0 priority 0" anyway — performing exactly the root
		// election it had just announced it was not performing, and saying the
		// opposite in the transcript. Taking the priority as a POINTER is what
		// makes "no priority was asked for" representable at all: with an int
		// there is no value that means absent, and 0 is the most dangerous one to
		// pick as the zero value.
		if priority == nil {
			return nil, nil, skipped
		}
	}
	var p int
	if superRoot {
		// Priority 0 IS SuperRoot. The keyword is additional protection; this line
		// is what actually decides the election.
		//
		// GLOBAL CONFIG, and that is captured rather than assumed since 2026-08-11
		// (`spanning-tree root ?` → primary | secondary | super). NOT under
		// `spanning-tree mst configuration`, which was the other candidate.
		// Removing it later needs "no spanning-tree root super" — naming the
		// keyword, because the bare "no spanning-tree root" is accepted silently
		// and clears nothing. Nothing here emits that yet.
		p = 0
		after = append(after, "spanning-tree root super")
	} else if priority != nil {
		p = clampBridgePriority(*priority)
		// THE NEGATION, and it must NAME THE KEYWORD — captured 2026-08-11 on
		// core01: bare "no spanning-tree root" is accepted SILENTLY and clears
		// nothing, only "no spanning-tree root super" does.
		//
		// UNDER MSTP ONLY, because that is the only mode where the keyword means
		// anything: the branch above REFUSES Super Root outside MST and says so,
		// and a connector that answered that refusal by sending the negation
		// anyway would be writing about a feature it had just declined to touch.
		//
		// # Why it rides EVERY mstp priority write and not only a clear
		//
		// It is not merely the undo half — though it is that too, and without it
		// the undo was a lie: api/stp_apply.go refuses to un-tick the box unless
		// the caller names a priority to put back, so the priority landed and the
		// keyword stayed standing, leaving a switch that went on winning the
		// election it had just been told to stop winning.
		//
		// It is that AN EXPLICIT PRIORITY IS FALSE WHILE THE KEYWORD STANDS. This
		// function is handed "put this bridge at 32768"; on a device still carrying
		// `spanning-tree root super` the write is accepted and changes nothing an
		// operator can see — the box remains the region's root, and the screen
		// reports the number that was asked for. Clearing the keyword is what makes
		// the priority the truth. This function cannot tell "clearing Super Root"
		// from "setting a priority on a switch that never had it" — the plan
		// carries no previous state — and on a device that never had it the line is
		// a no-op, so the asymmetry decides: a redundant negation costs a line in a
		// transcript, a missing one leaves a root nobody can outrank.
		//
		// It rides `after` — its own configuration block — for the same reason the
		// positive form does: runConfigBlock abandons a block at the first refusal,
		// and a train that spells this differently must not take the priority down
		// with it.
		if eosMode == "mstp" {
			after = append(after, "no spanning-tree root super")
		}
	} else {
		// Nothing asked for, nothing written.
		return nil, nil, skipped
	}
	switch eosMode {
	case "rapid-pvst":
		cmds = append(cmds, fmt.Sprintf("spanning-tree vlan-id 1-4094 priority %d", p))
	default:
		// mstp and rstp: instance 0 is the CIST in both. DEDUCED for rstp.
		//
		// ONE LINE PER INSTANCE when the caller named several. Instance 0 alone
		// used to be the only possibility — the intent was a boolean — and that
		// made root designation meaningless on exactly the topologies MST exists
		// for: a region with three instances has three trees and three roots, and
		// writing priority on the CIST alone leaves the other two elected by MAC
		// address.
		var i int
		for _, i = range instances {
			cmds = append(cmds, fmt.Sprintf("spanning-tree mst %d priority %d", i, p))
		}
	}
	return cmds, after, skipped
}

// sections0Name returns the header of the first spanning-tree section, for the
// note that says which instance the bridge facts describe. Empty when the output
// carries no section header at all.
func sections0Name(text string, sections [][]int) string {
	if len(sections) == 0 {
		return ""
	}
	return text[sections[0][0]:sections[0][1]]
}

// clampBridgePriority keeps the value inside what EOS accepts.
//
// The step of 4096 is the protocol's, not a preference: 802.1D encodes the
// priority in the top four bits, and the CLI refuses anything else outright.
// Rounding DOWN keeps a device at least as likely to be root as the operator
// asked, which is the safe direction for a value somebody chose deliberately.
func clampBridgePriority(p int) int {
	if p < 0 {
		p = 0
	}
	if p > 61440 {
		p = 61440
	}
	return (p / 4096) * 4096
}

func sortedInstances(m map[int][]int) []int {
	var out []int
	var k int
	for k = range m {
		out = append(out, k)
	}
	sortInts(out)
	return out
}

// sanitizeRegionName keeps what EOS accepts for a region name (32 characters, no
// whitespace).
//
// THE NAME IS PART OF THE DIGEST, so this function is not cosmetic: two switches
// whose region names differ by one character are in two different regions, and
// nothing anywhere reports that. Sanitising must therefore be DETERMINISTIC —
// the same site name must produce the same region name on every device, for
// ever — which is why the mapping below is a fixed substitution and not, say, a
// hash or a truncation that depends on what else is in the string.
//
// RETURNS "" WHEN NOTHING SURVIVES, and the caller refuses the plan. It used to
// return the literal "meshcraft", which looked like a harmless default and was
// the opposite: a single fixed name for the whole instance is the alternative
// ADR-0021 examined and rejected, because it makes region membership depend on
// the VLAN table alone and lets two adjacent sites merge into one region the day
// their VLAN lists coincide. Arriving there by accident, from two different
// unsanitisable names, is worse than arriving on purpose.
func sanitizeRegionName(s string) string {
	var b strings.Builder
	var i int
	for i = 0; i < len(s) && b.Len() < 32; i++ {
		var c = s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9', c == '-', c == '_':
			b.WriteByte(c)
		case c == ' ' || c == '.':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// --- Reading ---

// "show spanning-tree" on EOS:
//
//	MST0
//	  Spanning tree enabled protocol mstp
//	  Root ID    Priority    32768
//	             Address     2899.3a0e.1f6f
//	             This bridge is the root
//
//	  Bridge ID  Priority    32768  (priority 32768 sys-id-ext 0)
//	             Address     2899.3a0e.1f6f
//
//	Interface        Role       State      Cost      Prio.Nbr Type
//	---------------- ---------- ---------- --------- -------- ------------------
//	Et1              designated forwarding 2000      128.1    P2P
//	Et49             root       forwarding 500       128.49   P2P
//
// THE WORDS ARE SPELLED OUT HERE, where IOS abbreviates them ("Desg", "FWD").
// Both spellings are accepted by [eosSTPRole] / [eosSTPState] rather than only
// the documented one: this connector's own output fixtures are documentation,
// and a train that borrowed the abbreviated table would otherwise report every
// port with an empty role — which reads on screen as "spanning tree is not
// running", the one conclusion that would be wrong.
var (
	reSTPSection = regexp.MustCompile(`(?m)^(VLAN\d+|MST\d+)\s*$`)
	reSTPProto   = regexp.MustCompile(`(?i)Spanning tree enabled protocol\s+(\S+)`)
	reSTPBridge  = regexp.MustCompile(`(?i)Bridge ID\s+Priority\s+(\d+)`)
	reSTPRootPri = regexp.MustCompile(`(?i)Root ID\s+Priority\s+(\d+)`)
	reSTPAddr    = regexp.MustCompile(`(?i)Address\s+([0-9a-f.:]{12,})`)
	reSTPIsRoot  = regexp.MustCompile(`(?i)This bridge is the root`)
	reSTPRootCst = regexp.MustCompile(`(?i)Cost\s+(\d+)`)
	// One port row: name, role, state, cost.
	reSTPPortRow = regexp.MustCompile(
		`(?im)^(\S+)\s+(designated|root|alternate|backup|master|disabled|desg|altn|back|mstr|dis)\s+` +
			// The STATE alternation carries "disabled" / "dis" as well, and
			// leaving them out dropped whole rows rather than degrading them: a
			// port that is administratively down prints "Desg DIS 4" on an
			// IOS-shaped table, which matched the ROLE alternation and then failed
			// the STATE one, so the regex skipped the line entirely and the port
			// vanished from the topology view instead of appearing as disabled.
			`(forwarding|learning|discarding|blocking|listening|disabled|fwd|lrn|bkn|blk|lis|dis)\s+(\d+)`)
	// "show spanning-tree mst configuration". CAPTURED on a DCS-7050SX-64-R
	// running 4.28.13.1M:
	//
	//	Name      []
	//	Revision  0    Instances configured 1
	//
	//	Instance  Vlans mapped
	//	-------- --------------------------------------------------------------
	//	0         1-4094
	//
	// NO COLON after "Name", and the value is BRACKETED. The pattern here
	// required `Name\s*:\s*(\S+)` — borrowed from a platform that punctuates it —
	// so the name never matched on any EOS switch. That alone would have been a
	// cosmetic gap; what made it worse is the caller's reaction to an empty name,
	// which was to announce that the region "could not be read". It had been
	// read perfectly. The region is simply UNNAMED, which is a different fact with
	// a different consequence (cf. driver.MSTRegion.Configured: an unnamed region
	// makes every switch its own region, because the name defaults to the bridge
	// MAC).
	//
	// Both spellings are accepted — bracketed and bare, with or without the colon
	// — because the bracket is how EOS renders "empty" and a train that has a name
	// to print may well print it bare.
	reMSTName = regexp.MustCompile(`(?im)^\s*Name\s*:?\s*(?:\[([^\]]*)\]|(\S+))`)
	reMSTRev  = regexp.MustCompile(`(?im)^\s*Revision\s*:?\s*(\d+)`)
	// reMSTBlock: evidence that the OUTPUT WAS AN MST CONFIGURATION at all, as
	// opposed to an error, an empty string, or the output of a switch that does
	// not run MST. This is what separates "read, and unnamed" from "not read",
	// which the caller must not conflate.
	reMSTBlock = regexp.MustCompile(`(?im)^\s*(?:Revision\b|Instance\s+Vlans)`)
	// The instance -> VLAN table of "show spanning-tree mst configuration".
	//
	// The VLAN list accepts SPACES, and that is not cosmetic: this package's own
	// expandVlanList documents the platform wrapping long lists, so a real
	// instance carrying "10-20, 30-40" failed a character class of [\d,\-]
	// and the row was dropped. A DROPPED ROW IS THE DANGEROUS OUTCOME HERE —
	// the region digest covers the whole 4096-entry VLAN map, so a partial
	// instance map compared against the site's intent reports a region mismatch
	// that does not exist, or hides one that does.
	//
	// TWO SENSES OF "WRAPPED", and this pattern covers ONE of them: a list broken
	// up by spaces WITHIN ONE LINE. The other — a list long enough to overflow the
	// column and CONTINUE ON THE NEXT LINE — is handled by [reMSTInstCont] and
	// [mstInstanceRows], not here, because a continuation carries no instance
	// number and so matches nothing in this pattern.
	//
	// That second sense was left unhandled until 2026-08-10, deliberately, because
	// merging a continuation means deciding what one LOOKS like — indentation
	// depth, a trailing comma on the line before, a line of dashes that is a
	// separator and not a list — and every one of those was a property of output
	// nobody in this repository had seen. The capture settled it: see
	// [reMSTInstCont].
	reMSTInst = regexp.MustCompile(`(?im)^\s*(\d+)\s+([\d,\-][\d,\- ]*?)\s*$`)
	// reMSTInstCont: a CONTINUATION of the row above it, and the rule comes from
	// hardware rather than from reasoning about it.
	//
	// CAPTURED on core01.lab (DCS-7050SX-64-R, 4.28.13.1M, 2026-08-10; the
	// bytes are testdata/show_spanning_tree_mst_configuration_wrapped.txt). EOS
	// wraps at the column the list started in, with NO instance number and about
	// ten spaces of indent:
	//
	//	1         100,102,104,106,108,110,112,114,116,118,120,122,124,126,128,130,132
	//	          134,136,138,140,142,144,146,148,150,152,154,156,158,160,162,164,166
	//	          …
	//	2         3000-3100
	//	--------------------------------------------------------------------------
	//
	// Instance 1 there holds a hundred VLANs over six lines. Before the merge it
	// came back with SEVENTEEN — the numbered line and nothing else — and a
	// partial instance map is the dangerous outcome named above: the region digest
	// covers the whole 4096-entry map, so it reported a mismatch against a site
	// intent the switch matched exactly.
	//
	// The rule is deliberately narrow, and each clause pays for itself:
	//
	//   - INDENTED. A row puts its instance id in the first column; anything
	//     continuing one is pushed right. This is also what keeps the pattern from
	//     eating a line that begins a new table.
	//   - STARTS WITH A DIGIT. This is what excludes the two RULES OF DASHES the
	//     table is drawn with — "-------- ----…" under the header and the closing
	//     line — which are otherwise made of nothing but characters a VLAN list is
	//     allowed to contain, and would append "----…" to the last instance and
	//     make expandVlanList reject the row WHOLE. That failure would have been
	//     worse than the one being fixed.
	//   - CHECKED SECOND. [reMSTInst] still gets first refusal on every line, so a
	//     numbered row is a row no matter how deeply a train indents it, and the
	//     merge cannot swallow the next instance.
	//   - A BLANK LINE, or any line matching neither, ENDS the run. A wrapped list
	//     is contiguous on every output seen; not resetting would let a table
	//     footer or a second command's output land in the last instance read.
	//
	// What is still NOT handled, said plainly: a continuation that splits a RANGE
	// across the break ("3000-" then "3100") would be joined with a comma and
	// come out as two VLANs instead of a hundred and one. No capture shows EOS
	// doing that — it wraps between list items — and inventing the repair would be
	// the same mistake this comment used to describe.
	reMSTInstCont = regexp.MustCompile(`^[ \t]+(\d[\d,\- \t]*?)[ \t]*$`)
)

// mstRow is one instance of the region table with its VLAN list already
// reassembled from however many lines EOS spread it over.
type mstRow struct {
	Instance int
	VLANs    string
}

// mstInstanceRows walks the region table line by line, which the row pattern
// alone cannot do: reassembling a wrapped list needs to know which row a
// continuation belongs to, and that is an ordering fact a global regex scan
// throws away. See [reMSTInstCont] for the rule and the capture behind it.
func mstInstanceRows(text string) []mstRow {
	var out []mstRow
	var lines = strings.Split(text, "\n")
	// cur indexes the row a continuation would extend; -1 means "no run open".
	var cur = -1
	var i int
	for i = range lines {
		// The device speaks CRLF and nothing between here and the wire strips the
		// CR, so the anchors below have to be given a line that ends where it looks
		// like it ends.
		var line = strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			cur = -1
			continue
		}
		var m = reMSTInst.FindStringSubmatch(line)
		if m != nil {
			var inst, err = strconv.Atoi(m[1])
			if err != nil {
				cur = -1
				continue
			}
			out = append(out, mstRow{Instance: inst, VLANs: m[2]})
			cur = len(out) - 1
			continue
		}
		if cur >= 0 {
			m = reMSTInstCont.FindStringSubmatch(line)
			if m != nil {
				// A comma always separates the join. expandVlanList skips empty
				// tokens, so a line that already ended in one costs nothing.
				out[cur].VLANs += "," + m[1]
				continue
			}
		}
		cur = -1
	}
	return out
}

// ReadSpanningTree reports what the device believes about the tree.
func (c *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out driver.SpanningTreeState
	if c.cli == nil {
		return out, fmt.Errorf("no open session on this access")
	}
	var text, err = c.cli.run(ctx, "show spanning-tree", cmdTimeout)
	if err != nil && text == "" {
		return out, fmt.Errorf("show spanning-tree failed: %v", err)
	}
	out = parseShowSpanningTree(text)

	// The region is a SECOND command, and it is the one that matters most on this
	// platform (cf. the header): a switch in the wrong region looks identical to
	// one in the right region from "show spanning-tree" alone.
	if out.Mode == "mstp" {
		var mstOut, mstErr = c.cli.run(ctx, "show spanning-tree mst configuration", cmdTimeout)
		var read bool
		if mstErr == nil || mstOut != "" {
			out.Region, read = parseMSTRegion(mstOut)
		}
		switch {
		case !read:
			out.Notes = append(out.Notes,
				"the MST region could not be read: a region mismatch is invisible any other "+
					"way — 802.1s handles it by silently treating the neighbour as an ordinary "+
					"RSTP bridge — so this read cannot say whether this switch is in the "+
					"site's region")
		case out.Region.Name == "":
			// READ, and genuinely unnamed. Said as its own sentence because the
			// consequence is concrete and is not "unknown": EOS falls back to the
			// bridge MAC for the region name, so two switches that both print
			// "Name []" are in DIFFERENT regions and will treat each other as
			// plain RSTP bridges no matter how their instance maps compare.
			out.Notes = append(out.Notes,
				"this switch is in no named MST region (\"Name []\"): EOS then derives the "+
					"region name from the bridge address, which puts every unnamed switch in "+
					"a region of its own — neighbours are treated as ordinary RSTP bridges "+
					"whatever their instance map says")
		}
	}
	return out, nil
}

func parseShowSpanningTree(text string) driver.SpanningTreeState {
	var out driver.SpanningTreeState

	// BRIDGE-LEVEL FACTS COME FROM THE FIRST SECTION ONLY, and this is the
	// defect that mattered most in the first version of this parser.
	//
	// "show spanning-tree" prints ONE SECTION PER INSTANCE — MST0, MST1, … in
	// mstp, or VLAN0001, VLAN0099, … per VLAN — and every one of them carries
	// its own "Root ID", "Bridge ID", "This bridge is the root" and root
	// cost. Scanning the whole output for those meant the flags of DIFFERENT
	// instances were mixed into one answer: a switch that is root of MST1 and
	// not of MST0 came back IsRoot=true with MST0's root address, which reads on
	// screen as "this switch is the root of the tree" when it is not.
	//
	// cisco/ios/stp.go hit exactly this and fixed it the same way, from a live
	// 2960-X capture. The first section is the right one to report because it is
	// the CIST / instance 0 — the tree every VLAN rides when instances are not
	// load-balanced, and the only one a single-instance state can honestly
	// describe. The others are named in a Note rather than silently folded in.
	var head = text
	var sections = reSTPSection.FindAllStringIndex(text, -1)
	if len(sections) > 1 {
		head = text[sections[0][0]:sections[1][0]]
	} else if len(sections) == 1 {
		head = text[sections[0][0]:]
	}

	var m = reSTPProto.FindStringSubmatch(head)
	if m == nil {
		// A section header with no protocol line under it, or no section at all:
		// fall back to the whole text rather than reporting nothing, since a
		// train that prints no header still prints the protocol.
		m = reSTPProto.FindStringSubmatch(text)
	}
	if m != nil {
		out.Enabled = true
		out.Mode = driver.NormalizeSTPMode(strings.ToLower(m[1]))
		if out.Mode == "" {
			// Anything the device names that the shared vocabulary does not know
			// is kept VERBATIM rather than blanked, so an operator sees the word
			// the switch actually used. ("rapid-pvst" is known — it was added to
			// driver.NormalizeSTPMode when this parser proved it was not.)
			out.Mode = strings.ToLower(m[1])
		}
	}
	if m = reSTPBridge.FindStringSubmatch(head); m != nil {
		out.BridgePriority, _ = strconv.Atoi(m[1])
	}
	if m = reSTPRootPri.FindStringSubmatch(head); m != nil {
		out.RootPriority, _ = strconv.Atoi(m[1])
	}
	out.IsRoot = reSTPIsRoot.MatchString(head)

	// The Address lines: the FIRST belongs to the Root ID block, the second to
	// the Bridge ID block. Taking them in order is what keeps "who is root" and
	// "who am I" apart on a switch that is not the root — and taking them from
	// `head` is what keeps another instance's bridge out of this answer.
	var addrs = reSTPAddr.FindAllStringSubmatch(head, -1)
	if len(addrs) > 0 {
		out.RootBridgeID = addrs[0][1]
	}
	if len(addrs) > 1 {
		out.BridgeID = addrs[1][1]
	} else if len(addrs) == 1 && out.IsRoot {
		out.BridgeID = addrs[0][1]
	}
	if !out.IsRoot {
		if m = reSTPRootCst.FindStringSubmatch(head); m != nil {
			out.RootPathCost, _ = strconv.Atoi(m[1])
		}
	}
	if len(sections) > 1 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"this switch prints %d spanning-tree instances (%s…); the bridge facts above "+
				"describe the FIRST one only — the CIST, which is the tree every VLAN rides "+
				"when instances are not load-balanced. Per-instance roots are not reported "+
				"by this read", len(sections), strings.TrimSpace(sections0Name(text, sections))))
	}

	// The per-VLAN sections are a DIAGNOSIS and not a formatting detail: a device
	// printing "VLAN0001" headers is running rapid-PVST whatever the protocol
	// word says, and one printing "MST0" is in MSTP.
	//
	// Reuses the `sections` indices already computed above rather than scanning
	// again — the two must agree on how many sections there are, or the note
	// about which instance the bridge facts describe and the note about the
	// protocol variant would contradict each other on the same output.
	var perVLAN = 0
	var i int
	for i = range sections {
		if strings.HasPrefix(strings.ToUpper(text[sections[i][0]:sections[i][1]]), "VLAN") {
			perVLAN++
		}
	}
	if perVLAN > 1 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"this switch prints %d per-VLAN spanning-tree sections: it is running a "+
				"per-VLAN variant (rapid-PVST), which does not interoperate with a "+
				"single-instance RSTP neighbour the way the mode word suggests", perVLAN))
	}

	// PORT ROWS COME FROM THE WHOLE OUTPUT, deliberately — unlike the bridge
	// facts above. A port appears once per instance it participates in, and
	// `seen` keeps the first occurrence, which is the CIST's. Restricting this
	// to `head` would drop every port on a train that prints the port table once
	// at the end rather than per section.
	var rows = reSTPPortRow.FindAllStringSubmatch(text, -1)
	var seen = map[string]bool{}
	for i = range rows {
		var std, ok = (&Driver{}).StandardPortName(rows[i][1])
		if !ok || seen[std] {
			continue
		}
		seen[std] = true
		var cost, _ = strconv.Atoi(rows[i][4])
		out.Ports = append(out.Ports, driver.SpanningTreePort{
			Port:     std,
			Role:     eosSTPRole(rows[i][2]),
			State:    eosSTPState(rows[i][3]),
			PathCost: cost,
		})
	}
	return out
}

// parseMSTRegion reads the region, and reports whether the output WAS one.
//
// The boolean is not decoration. An unnamed region and an unreadable one both
// leave Name empty, they lead an operator to opposite conclusions, and only the
// device can tell them apart — so the distinction is made here, where the text
// is, rather than inferred from the result by a caller that no longer has it.
func parseMSTRegion(text string) (driver.MSTRegion, bool) {
	var r driver.MSTRegion
	if !reMSTBlock.MatchString(text) {
		return r, false
	}
	var m = reMSTName.FindStringSubmatch(text)
	if m != nil {
		// Group 1 is the bracketed form ("Name []" → empty, legitimately);
		// group 2 the bare one. Empty stays empty: it is what an unconfigured
		// region looks like, and inventing a placeholder would make
		// MSTRegion.Configured say yes.
		r.Name = strings.TrimSpace(m[1] + m[2])
	}
	if m = reMSTRev.FindStringSubmatch(text); m != nil {
		r.Revision, _ = strconv.Atoi(m[1])
	}
	var rows = mstInstanceRows(text)
	var i int
	for i = range rows {
		if rows[i].Instance == 0 {
			// Instance 0 is the CIST and is implicit (cf. driver.MSTRegion): it
			// holds every VLAN not named elsewhere, so recording it would make two
			// identical regions compare unequal.
			continue
		}
		var vlans, verr = expandVlanList(rows[i].VLANs)
		if verr != nil {
			continue
		}
		if r.InstanceVLANs == nil {
			r.InstanceVLANs = map[int][]int{}
		}
		r.InstanceVLANs[rows[i].Instance] = vlans
	}
	return r, true
}

func eosSTPRole(s string) string {
	switch strings.ToLower(s) {
	case "designated", "desg":
		return driver.STPRoleDesignated
	case "root":
		return driver.STPRoleRoot
	case "alternate", "altn":
		return driver.STPRoleAlternate
	case "backup", "back":
		return driver.STPRoleBackup
	case "master", "mstr":
		return driver.STPRoleMaster
	case "disabled", "dis":
		return driver.STPRoleDisabled
	}
	return ""
}

func eosSTPState(s string) string {
	switch strings.ToLower(s) {
	case "forwarding", "fwd":
		return driver.STPStateForwarding
	case "learning", "lrn":
		return driver.STPStateLearning
	case "discarding", "blocking", "listening", "bkn", "blk", "lis":
		return driver.STPStateDiscarding
	case "disabled", "dis":
		// The shared vocabulary has no "disabled" STATE — only a disabled ROLE
		// (driver.STPRoleDisabled). A port in that state is not forwarding and is
		// not learning, so discarding is the honest mapping; the ROLE column is
		// where "this port is out of the tree" is actually reported.
		return driver.STPStateDiscarding
	}
	return ""
}

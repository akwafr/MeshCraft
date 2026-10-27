package ios

// Spanning tree on IOS: reading what the switch believes, and writing what the
// site decided.
//
// # Why this connector had none until now
//
// It declared three modes in [Capabilities.STPModes] and implemented neither
// reader nor writer, so meshcraft could see a Catalyst's spanning tree was wrong
// and could not touch it. That gap had a cost, recorded here because it is the
// justification for the file: on 2026-08-02 a 2960-X ran Rapid PVST+ while every
// other switch in its fleet ran MSTP or RSTP, declared ITSELF root for both
// VLAN 1 and VLAN 99 at priority 32769 while the real root sat at 4096, and the
// port facing it would not forward. meshcraft could describe all of that and
// change none of it.
//
// # PVST+ is the trap this platform sets
//
// IOS defaults to per-VLAN spanning tree. Its "show spanning-tree" prints one
// section PER VLAN — that is the signature — and it sends per-VLAN BPDUs to a
// Cisco-proprietary multicast that MSTP and RSTP neighbours do not consume as
// CST BPDUs. A Catalyst left on that default in a mixed fleet elects itself root
// of a tree nobody else is in.
//
// So the mode is written, and the MST REGION with it: 802.1s decides membership
// on name + revision + a digest over the whole VLAN-to-instance table, and a
// device carrying the right mode with the wrong region is in a region of one
// (cf. driver.MSTRegion). Writing the mode alone would look like success and
// change nothing about who talks to whom.

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

// iosSTPMode renders meshcraft's vocabulary as the keyword IOS wants.
//
// "rapid-pvst" is what IOS calls RSTP, and the distinction matters: it is
// RSTP-per-VLAN, not the single-instance RSTP the rest of the fleet speaks. It is
// offered because a single-vendor site legitimately wants it; it is never the
// silent default here.
func iosSTPMode(mode string) (string, bool) {
	switch driver.NormalizeSTPMode(mode) {
	case "mstp":
		return "mst", true
	case "rstp":
		return "rapid-pvst", true
	case "stp":
		return "pvst", true
	}
	return "", false
}

// ApplySpanningTree writes the mode, the MST region and the bridge priority.
//
// ORDER IS NOT FREE. The region is configured BEFORE the mode is switched to
// mst: a device that enters mst carrying a default region (its own bridge MAC as
// the name) joins a region of one and re-converges a second time when the region
// lands. Writing the region first makes it one transition.
func (a *Client) ApplySpanningTree(ctx context.Context, plan driver.SpanningTreePlan) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if a.cli == nil {
		return out, fmt.Errorf("no open session on this device")
	}

	if !plan.Enabled {
		// "no spanning-tree vlan <list>" disables it per VLAN on this platform,
		// and there is no global off switch. Turning spanning tree off across a
		// campus switch is not a gesture meshcraft performs from a site preference.
		out.Skipped = append(out.Skipped,
			"spanning tree is NOT disabled on this platform by meshcraft: IOS has no "+
				"global off switch, only \"no spanning-tree vlan <list>\", and silently "+
				"removing loop protection from a campus switch is not something a site "+
				"preference should be able to do")
		return out, nil
	}

	// SUPER ROOT IS DELIBERATELY NOT IMPLEMENTED HERE, and this connector is the
	// one where that decision costs something: it DOES declare WritesExtra("stp"),
	// so anything written here reaches a live Catalyst on the next pipeline run.
	// "spanning-tree root super" is not an IOS command at all, and the other
	// half — "spanning-tree mst 0 priority 0" — is a root-election change on a
	// production tree that nobody asked this connector for. Saying so is the
	// contract's rule (driver.SpanningTreePlan.SuperRoot); guessing would be a
	// topology change discovered afterwards.
	if plan.SuperRoot {
		out.Skipped = append(out.Skipped,
			"SUPER ROOT not applied: this connector does not implement it. IOS has no "+
				"\"root super\" keyword, and forcing priority 0 on the CIST of a live "+
				"Catalyst is a root-election change meshcraft will not make on the strength "+
				"of a checkbox written for another platform")
	}

	var mode, ok = iosSTPMode(plan.Mode)
	if !ok {
		out.Skipped = append(out.Skipped, "unsupported STP mode "+plan.Mode)
		return out, nil
	}

	// READ-COMPARE. A device already carrying the wanted mode, region and bridge
	// priority is left ALONE: no configuration block, no reconvergence risk, and
	// no second "write memory" on an apply that changes nothing.
	//
	// THIS FAILS TOWARD WRITING, and that direction is the whole safety argument.
	// Every way of being unsure — the read errored, the mode did not parse, the
	// region came back empty, the priority is not legible — returns false and the
	// block below runs exactly as it always did. The only thing that suppresses a
	// write is a POSITIVE, COMPLETE match on every field this applier would have
	// sent. A no-op path that guessed "probably fine" would turn a connector that
	// re-sends too much into one that silently sends nothing, which is far worse:
	// the first costs an idempotent line, the second leaves a Catalyst in PVST+
	// while the screen says the mode was applied.
	var same, why = a.spanningTreeAlreadyApplied(ctx, plan, mode)
	if same {
		out.Notes = append(out.Notes, why)
		a.step(true, "spanning tree already as planned — nothing sent", why)
		return out, nil
	}

	var cmds []string
	// 1. The region FIRST (cf. the header), and only when there is one to write.
	if mode == "mst" && plan.Region.Configured() {
		cmds = append(cmds,
			"spanning-tree mst configuration",
			"name "+sanitizeRegionName(plan.Region.Name),
			"revision "+strconv.Itoa(plan.Region.Revision))
		var inst int
		for _, inst = range sortedInstances(plan.Region.InstanceVLANs) {
			var vlans = plan.Region.InstanceVLANs[inst]
			if len(vlans) == 0 {
				continue
			}
			cmds = append(cmds, fmt.Sprintf("instance %d vlan %s",
				inst, joinInts(vlans)))
		}
		cmds = append(cmds, "exit")
	}

	// 2. The mode.
	cmds = append(cmds, "spanning-tree mode "+mode)

	// 3. Bridge priority. On mst it is per instance and instance 0 is the CIST —
	//    the one that decides the tree every VLAN rides when instances are not
	//    load-balanced. On the per-VLAN modes it has to be set across the range,
	//    because a priority on VLAN 1 alone leaves every other VLAN at default.
	if plan.BridgePriority != nil {
		var p = clampBridgePriority(*plan.BridgePriority)
		if mode == "mst" {
			cmds = append(cmds, fmt.Sprintf("spanning-tree mst 0 priority %d", p))
		} else {
			cmds = append(cmds, fmt.Sprintf("spanning-tree vlan 1-4094 priority %d", p))
		}
	}

	var err = a.runConfigBlock(ctx, cmds)
	if err != nil {
		a.step(false, "spanning tree NOT applied", firstLine(err.Error()))
		return out, err
	}
	out.Notes = append(out.Notes, "spanning-tree mode "+mode)
	if mode == "mst" && plan.Region.Configured() {
		out.Notes = append(out.Notes, fmt.Sprintf("MST region %q revision %d",
			plan.Region.Name, plan.Region.Revision))
	}
	a.step(true, "spanning tree applied", strings.Join(out.Notes, " ; "))

	// PERSIST, and this one had to be argued for rather than copied.
	//
	// This connector's OTHER writes save because the contract says a write
	// persists what it wrote (Commit = apply_then_save). This one ran without a
	// save until 2026-08-08, and it is the only write in the file that RIDES
	// another gesture: api.runApply calls applyDeviceSTP AFTER ApplyPorts has
	// already saved, so the mode and the region landed in running-config alone.
	// The cost is not an unsaved setting — it is a RECONVERGENCE PER REBOOT
	// CYCLE: the box comes back on its startup mode, the next apply flips it
	// again, and the tree re-converges every time, on a schedule nobody
	// connects to spanning tree.
	//
	// The save is at the END of the block and not between its lines: mode and
	// region are one transition (cf. the ORDER note above), and persisting a
	// half-applied region is persisting a region of one.
	//
	// KNOWN COST, worth naming rather than discovering: on a pipeline run this
	// is the SECOND "write memory" of the same apply — ApplyPorts already
	// saved before this ran.
	//
	// THE READ-COMPARE NO-OP PATH NOW EXISTS (2026-08-11, see
	// [Client.spanningTreeAlreadyApplied] at the top of this function), and it is
	// what this comment predicted would make both costs disappear together: on a
	// device already carrying the wanted mode, region and priority, neither the
	// configuration block nor this save is reached at all. What remains here is
	// the path taken when something genuinely differs, where a save is right.
	//
	// The previous wording — "NOTHING ASKS FOR ONE TODAY" — was WRONG about
	// TODO.md, which did carry an entry asking for exactly this and named it the
	// last thing unsettled around the per-site STP off switch. Two files
	// disagreeing about whether a piece of work was wanted is how it stays undone.
	var perr = a.save(ctx)
	if perr != nil {
		out.Notes = append(out.Notes, "applied but NOT persisted: "+firstLine(perr.Error())+
			" — a reboot would bring the switch back on its startup spanning-tree mode, and "+
			"the next apply would flip it again: one reconvergence per reboot")
		a.step(false, "startup-config save failed", firstLine(perr.Error()))
		return out, nil
	}
	a.step(true, "configuration persisted", SaveCmd)
	return out, nil
}

// spanningTreeAlreadyApplied reports whether the device already carries exactly
// what [Client.ApplySpanningTree] was about to send, and says so in a sentence
// the operator can read.
//
// It returns FALSE on every doubt — see the call site for why that direction is
// not negotiable. In particular a read error is not an error here: it is simply
// "cannot tell", and cannot-tell means write.
func (a *Client) spanningTreeAlreadyApplied(ctx context.Context,
	plan driver.SpanningTreePlan, mode string) (bool, string) {

	var st, raw, err = a.readSpanningTree(ctx)
	if err != nil {
		return false, ""
	}
	return spanningTreeMatchesPlan(st, raw, plan, mode)
}

// spanningTreeMatchesPlan is the DECISION, with no device in it.
//
// Split out so every branch — including all the ways of being unsure — is
// exercised without a session. That matters more here than in most places: the
// dangerous outcome of this function is a WRONG TRUE, and a wrong true is
// invisible on hardware (nothing is sent, the apply reports success, and the
// switch keeps whatever it had). A constructed test is the only place it can be
// caught.
func spanningTreeMatchesPlan(st driver.SpanningTreeState, raw string,
	plan driver.SpanningTreePlan, mode string) (bool, string) {

	if !st.Enabled {
		return false, ""
	}
	// The state's Mode is meshcraft vocabulary; render it the way the plan was
	// rendered so the two are compared in one language.
	var have, ok = iosSTPMode(st.Mode)
	if !ok || have != mode {
		return false, ""
	}

	var detail = "spanning-tree mode " + mode + " already set"

	// THE REGION. Only compared when the plan carries one — but when it does,
	// an UNREADABLE region is a mismatch, never a match: that is the difference
	// between "the region is right" and "nobody looked".
	if mode == "mst" && plan.Region.Configured() {
		if !st.Region.Configured() {
			return false, ""
		}
		if !regionMatches(st.Region, plan.Region) {
			return false, ""
		}
		detail += fmt.Sprintf(", MST region %q revision %d already configured with "+
			"the same VLAN map", st.Region.Name, st.Region.Revision)
	}

	// THE BRIDGE PRIORITY, read from the parenthetical rather than from the
	// headline number. "Bridge ID Priority 32769 (priority 32768 sys-id-ext 1)"
	// — the headline carries the sys-id-ext added in, so comparing it against a
	// configured 32768 would report a mismatch on a device that matches, and on
	// instance 0 it would report a match by luck. The parenthetical is the value
	// that was actually configured.
	if plan.BridgePriority != nil {
		var want = clampBridgePriority(*plan.BridgePriority)
		var got, seen = iosConfiguredPriority(raw)
		if !seen || got != want {
			return false, ""
		}
		detail += fmt.Sprintf(", bridge priority already %d", want)
	}
	return true, detail + " — nothing sent, and the startup-config was not rewritten"
}

// regionMatches compares two regions the way 802.1s does: name, revision AND the
// whole VLAN-to-instance map, because all three feed the digest that decides
// membership. Two regions agreeing on name and revision and disagreeing on one
// VLAN are two DIFFERENT regions, and each device is alone in its own.
func regionMatches(have, want driver.MSTRegion) bool {
	if sanitizeRegionName(have.Name) != sanitizeRegionName(want.Name) {
		return false
	}
	if have.Revision != want.Revision {
		return false
	}
	// The instance SETS must be identical. An instance the device carries and the
	// plan does not name changes the digest just as surely as a missing one, so
	// the comparison is symmetric rather than "does the device have at least this".
	if len(have.InstanceVLANs) != len(want.InstanceVLANs) {
		return false
	}
	var inst int
	var vlans []int
	for inst, vlans = range want.InstanceVLANs {
		var hv, ok = have.InstanceVLANs[inst]
		if !ok {
			return false
		}
		if !sameIntSet(hv, vlans) {
			return false
		}
	}
	return true
}

// sameIntSet compares two VLAN lists as sets: the plan's order is the caller's
// and the device's is the firmware's, and neither is a fact about the region.
func sameIntSet(a, b []int) bool {
	var x = sortedUniqueInts(a)
	var y = sortedUniqueInts(b)
	if len(x) != len(y) {
		return false
	}
	var i int
	for i = range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// reSTPConfiguredPrio reads the parenthetical IOS prints beside the bridge id:
// "Bridge ID  Priority    32769  (priority 32768 sys-id-ext 1)".
var reSTPConfiguredPrio = regexp.MustCompile(`(?i)\(priority\s+(\d+)\s+sys-id-ext\s+(\d+)\)`)

// iosConfiguredPriority recovers the CONFIGURED bridge priority.
//
// [driver.SpanningTreeState] carries only the headline number, which on IOS has
// the sys-id-ext added into it. Rather than widen a contract shared by nine
// connectors for one platform's rendering, this reads the parenthetical straight
// out of the raw text, and refuses to answer when there is none.
//
// The FIRST parenthetical is the one taken, matching [parseShowSpanningTree]'s
// rule that bridge-level facts come from the first section — on a per-VLAN
// device every section carries a different sys-id-ext and a scan of the whole
// output would mix them.
func iosConfiguredPriority(raw string) (int, bool) {
	var m = reSTPConfiguredPrio.FindStringSubmatch(raw)
	if m != nil {
		var p, err = strconv.Atoi(m[1])
		if err == nil {
			return p, true
		}
	}
	// No parenthetical: the headline alone cannot be trusted to be the configured
	// value, so this says "cannot tell" and the caller writes.
	return 0, false
}

// clampBridgePriority keeps the value inside what IOS accepts.
//
// The step of 4096 is the protocol's, not a preference: 802.1D encodes the
// priority in the top four bits, and IOS refuses anything else outright. Rounding
// DOWN keeps a device at least as likely to be root as the operator asked, which
// is the safe direction for a value someone chose deliberately.
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
	var i, j int
	for i = 1; i < len(out); i++ {
		var x = out[i]
		for j = i; j > 0 && out[j-1] > x; j-- {
			out[j] = out[j-1]
		}
		out[j] = x
	}
	return out
}

// sanitizeRegionName keeps what IOS accepts for a region name (32 characters,
// no whitespace). A name that breaks the command would leave the device in a
// region nobody intended, which is worse than a truncated one.
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
	if b.Len() == 0 {
		return "meshcraft"
	}
	return b.String()
}

// --- Reading ---

// "show spanning-tree" on IOS, as captured on a 2960-X (15.2(6)E) on
// 2026-08-02:
//
//	VLAN0001
//	  Spanning tree enabled protocol rstp
//	  Root ID    Priority    32769
//	             Address     bcc4.93e5.7f80
//	             This bridge is the root
//	  Bridge ID  Priority    32769  (priority 32768 sys-id-ext 1)
//	             Address     bcc4.93e5.7f80
//	Interface           Role Sts Cost      Prio.Nbr Type
//	------------------- ---- --- --------- -------- ----
//	Te1/0/2             Desg FWD 2         128.28   P2p
//
// THE PER-VLAN SECTIONS ARE THE DIAGNOSIS, not a formatting detail: a device
// printing them is running PVST+ or Rapid PVST+, whatever the word after
// "protocol" says. Under mst the same command prints "MST0" sections instead.
var (
	reSTPSection  = regexp.MustCompile(`(?m)^(VLAN\d+|MST\d+)\s*$`)
	reSTPProtocol = regexp.MustCompile(`(?i)Spanning tree enabled protocol\s+(\S+)`)
	reSTPRootPrio = regexp.MustCompile(`(?i)Root ID\s+Priority\s+(\d+)`)
	reSTPRootAddr = regexp.MustCompile(`(?is)Root ID.*?Address\s+([0-9a-f.:]+)`)
	reSTPBridgeP  = regexp.MustCompile(`(?i)Bridge ID\s+Priority\s+(\d+)`)
	reSTPBridgeA  = regexp.MustCompile(`(?is)Bridge ID.*?Address\s+([0-9a-f.:]+)`)
	reSTPIsRoot   = regexp.MustCompile(`(?i)This bridge is the root`)
	reSTPRootCost = regexp.MustCompile(`(?i)Cost\s+(\d+)`)
	// A port row: name, role, state, cost, prio.nbr, type.
	reSTPPort = regexp.MustCompile(`(?m)^(\S+)\s+(Root|Desg|Altn|Back|Mstr)\s+(FWD|BLK|LRN|LIS|DIS)\s+(\d+)`)
	// "spanning-tree mst configuration" as show run prints it back — the
	// CONFIGURATION shape, which is also what this connector WRITES.
	reMSTName = regexp.MustCompile(`(?im)^\s*name\s+(\S+)\s*$`)
	reMSTRev  = regexp.MustCompile(`(?im)^\s*revision\s+(\d+)\s*$`)
	// The EXEC shape of the same facts, captured on CiscoLabFirst1 (2026-08-11,
	// 15.2(6)E) — see [mstRegionCmd] for why this is the one that answers:
	//
	//	Name      [site-lab2]
	//	Revision  1     Instances configured 2
	//
	// The name is BRACKETED and the revision is followed by more text on the same
	// line, which is why neither of the two patterns above can read it: reMSTRev
	// anchors on `$` right after the digits.
	reMSTNameExec = regexp.MustCompile(`(?im)^\s*Name\s+\[([^\]]*)\]\s*$`)
	reMSTRevExec  = regexp.MustCompile(`(?im)^\s*Revision\s+(\d+)\b`)
	// One row of the instance table: an instance id, then its VLAN list.
	//
	//	Instance  Vlans mapped
	//	--------  -----------------------------------------------------------
	//	0         1-41,43-97,101-4094
	//	1         42,98-100
	reMSTInstExec = regexp.MustCompile(`(?im)^\s*(\d+)\s+([0-9,\-]+)\s*$`)
	// A wrapped continuation of the row above. IOS breaks a long VLAN list
	// BETWEEN items and indents the remainder.
	//
	// THE DIGIT CLAUSE IS LOAD-BEARING, and it is borrowed from the Arista
	// repair rather than re-derived: the table's dashed rules contain only
	// characters a VLAN list may also contain, so a continuation must be
	// indented AND start with a digit or the `-----` rule is swallowed as VLANs.
	reMSTInstCont = regexp.MustCompile(`(?im)^\s+(\d[0-9,\-]*)\s*$`)
)

// mstRegionCmd reads the MST region.
//
// IT USED TO BE "show current" AND THAT COMMAND DOES NOT EXIST IN EXEC —
// MEASURED on CiscoLabFirst1 (15.2(6)E, 2026-08-11), which answers
//
//	show current
//	                     ^
//	% Invalid input detected at '^' marker.
//
// `show current` is valid only INSIDE the `spanning-tree mst configuration`
// sub-mode, and [Client.ReadSpanningTree] runs it from privileged EXEC. So the
// region has NEVER been read off a Cisco device by this connector: the read was
// best-effort (`if err == nil`), the error was swallowed, and `Region` came back
// empty on every Catalyst meshcraft has ever talked to — silently, which is the
// worst shape for a field whose whole purpose is to expose a silent 802.1s
// divergence.
//
// The EXEC command that answers is this one, and it serves MORE than `show
// current` would have: name, revision AND the full instance→VLAN table.
const mstRegionCmd = "show spanning-tree mst configuration"

// ReadSpanningTree reports what the device believes about the tree.
//
// READ ONLY and replayable. Two commands: the tree itself, and the MST region —
// which "show spanning-tree" does NOT print, and which is the thing that
// silently decides whether this switch is in the same region as its neighbours.
func (a *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out, _, err = a.readSpanningTree(ctx)
	return out, err
}

// readSpanningTree is [Client.ReadSpanningTree] plus the RAW text.
//
// The raw bytes are kept for one caller — [iosConfiguredPriority] — because the
// configured bridge priority lives in a parenthetical that
// [driver.SpanningTreeState] has no field for, and widening a contract shared by
// nine connectors to carry one platform's rendering is the wrong trade. It stays
// unexported for the same reason.
func (a *Client) readSpanningTree(ctx context.Context) (driver.SpanningTreeState, string, error) {
	var out driver.SpanningTreeState
	if a.cli == nil {
		return out, "", fmt.Errorf("no open session on this device")
	}
	var text, err = a.cli.run(ctx, "show spanning-tree", saveTimeout)
	if err != nil && text == "" {
		return out, "", err
	}
	out = parseShowSpanningTree(text)

	// The region lives in the configuration, not in the tree view. Absent on a
	// device that is not in mst, and best-effort: a region that cannot be read
	// leaves the field empty rather than failing a read that otherwise worked.
	if out.Mode == "mstp" {
		var cfg string
		cfg, err = a.cli.run(ctx, mstRegionCmd, cmdTimeout)
		if err == nil {
			out.Region = parseMSTRegion(cfg)
		}
		if !out.Region.Configured() {
			// SAY SO. The old code read a command that does not exist and left the
			// field empty without a word, and an empty region on an mst device is
			// indistinguishable from a device in a region of one — which is the
			// exact fault this field exists to expose.
			out.Notes = append(out.Notes,
				"the MST region could not be read on this device ("+mstRegionCmd+"): "+
					"an EMPTY region here means UNREAD, not \"default\". Region membership "+
					"is decided by name + revision + the VLAN-to-instance digest, so this "+
					"switch cannot be compared to its neighbours until it answers")
		}
	}
	return out, text, nil
}

// parseShowSpanningTree turns the per-VLAN (or per-MST) view into one state.
//
// The FIRST section decides the bridge-level facts. On a per-VLAN platform every
// section repeats them with a different sys-id-ext, and reporting one of them is
// the only honest summary — meshcraft's model carries a single tree, and picking
// the first is at least deterministic.
//
// PVST+ IS DETECTED FROM THE SECTION HEADINGS, not from the protocol word: IOS
// prints "protocol rstp" under a "VLAN0001" heading when it is running Rapid
// PVST+, and reading that word alone would report plain RSTP for a switch that
// is not interoperating with the fleet's RSTP at all.
func parseShowSpanningTree(text string) driver.SpanningTreeState {
	var out driver.SpanningTreeState
	var sections = reSTPSection.FindAllStringSubmatch(text, -1)
	var perVLAN bool
	var s []string
	for _, s = range sections {
		if strings.HasPrefix(s[1], "VLAN") {
			perVLAN = true
		}
	}
	out.Enabled = len(sections) > 0 || reSTPProtocol.MatchString(text)

	// One reusable submatch slice for the whole function: every regexp below
	// returns []string or nil, and `m != nil` is the "did it match" test.
	var m []string
	var proto = ""
	m = reSTPProtocol.FindStringSubmatch(text)
	if m != nil {
		proto = strings.ToLower(m[1])
	}
	switch {
	case len(sections) > 0 && strings.HasPrefix(sections[0][1], "MST"):
		out.Mode = "mstp"
	case perVLAN && (proto == "rstp" || proto == "rapid-pvst"):
		// Rapid PVST+. Reported as rstp because that is the meshcraft vocabulary
		// for it, with the per-VLAN nature said in Notes where it can be read.
		out.Mode = "rstp"
		out.Notes = append(out.Notes,
			"this switch prints PER-VLAN spanning-tree sections: it is running Rapid "+
				"PVST+, Cisco's per-VLAN variant, and its BPDUs are not consumed as CST "+
				"BPDUs by MSTP or single-instance RSTP neighbours")
	case perVLAN:
		out.Mode = "stp"
		out.Notes = append(out.Notes,
			"per-VLAN spanning tree (PVST+): not interoperable as-is with MSTP or "+
				"single-instance RSTP neighbours")
	case proto != "":
		out.Mode = driver.NormalizeSTPMode(proto)
	}

	// BRIDGE-LEVEL FACTS COME FROM ONE SECTION, and it has to be said in code
	// rather than only in a comment. Scanning the whole output lets them leak
	// between sections: a switch that is root for VLAN 1 and not for VLAN 99
	// reported the root address of one and the "this bridge is the root" flag of
	// the other — observed live on 2026-08-02, root d401.c3ab.32f4 with IsRoot
	// true, which cannot both be so.
	//
	// The first section is the one reported, deterministically. Ports are still
	// gathered across all of them, because a port appears in each and the model
	// carries one row per port.
	var head = text
	if len(sections) > 0 {
		var locs = reSTPSection.FindAllStringIndex(text, -1)
		var start = locs[0][0]
		var end = len(text)
		if len(locs) > 1 {
			end = locs[1][0]
		}
		head = text[start:end]
	}
	m = reSTPBridgeP.FindStringSubmatch(head)
	if m != nil {
		out.BridgePriority, _ = strconv.Atoi(m[1])
	}
	m = reSTPBridgeA.FindStringSubmatch(head)
	if m != nil {
		out.BridgeID = m[1]
	}
	m = reSTPRootPrio.FindStringSubmatch(head)
	if m != nil {
		out.RootPriority, _ = strconv.Atoi(m[1])
	}
	m = reSTPRootAddr.FindStringSubmatch(head)
	if m != nil {
		out.RootBridgeID = m[1]
	}
	out.IsRoot = reSTPIsRoot.MatchString(head)
	if !out.IsRoot {
		m = reSTPRootCost.FindStringSubmatch(head)
		if m != nil {
			out.RootPathCost, _ = strconv.Atoi(m[1])
		}
	}

	// Ports, deduplicated across sections: the same interface appears once per
	// VLAN, and meshcraft's model carries one row per port.
	var seen = map[string]bool{}
	for _, m = range reSTPPort.FindAllStringSubmatch(text, -1) {
		// The tree view prints SHORT names ("Te1/0/2"); the rest of the model uses
		// the standard ones. Translating through the connector's own namer keeps
		// one vocabulary — a port named two ways is a port that matches nothing.
		var name, ok = (&Driver{}).StandardPortName(m[1])
		if !ok {
			name = m[1]
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		var cost, _ = strconv.Atoi(m[4])
		out.Ports = append(out.Ports, driver.SpanningTreePort{
			Port: name, Role: iosSTPRole(m[2]), State: iosSTPState(m[3]), PathCost: cost,
		})
	}
	return out
}

// parseMSTRegion reads the region, in EITHER of the two shapes IOS serves it.
//
// The EXEC shape ("show spanning-tree mst configuration") is the one a live
// device answers and it is now the one [Client.ReadSpanningTree] asks for; the
// CONFIGURATION shape (`name X` / `revision N`, as running-config and the
// sub-mode print it) is kept because it is what this connector WRITES and what a
// running-config capture carries. Reading both costs two regexps and means a
// caller never has to know which text it holds.
//
// # The instance map IS parsed now, and the old refusal to parse it is retracted
//
// It used to say: "the digest that decides region membership covers all 4094
// VLANs, and reporting a partial map read from a text view would invite a
// comparison that looks authoritative and is not". The premise was that the view
// is partial. It is not — the EXEC table prints EVERY instance with its complete
// VLAN list, instance 0 included (`0  1-41,43-97,101-4094`), which is the whole
// map and not a summary of it.
//
// And a comparison is now exactly what is wanted: [regionMatches] is what lets
// [Client.ApplySpanningTree] leave a converged device alone. Refusing to parse
// the map would not have made that comparison honest, it would have made it
// IMPOSSIBLE — or, worse, made it compare name and revision only and no-op on a
// device whose VLAN map, and therefore whose digest, differs.
//
// INSTANCE 0 IS DROPPED on the way in, to match [driver.MSTRegion.InstanceVLANs],
// where the CIST is implicit and never listed. Keeping it would make every
// device look like it carries an instance the plan never names.
func parseMSTRegion(text string) driver.MSTRegion {
	var r driver.MSTRegion
	var m = reMSTNameExec.FindStringSubmatch(text)
	if m == nil {
		m = reMSTName.FindStringSubmatch(text)
	}
	if m != nil {
		r.Name = strings.TrimSpace(m[1])
	}
	m = reMSTRevExec.FindStringSubmatch(text)
	if m == nil {
		m = reMSTRev.FindStringSubmatch(text)
	}
	if m != nil {
		r.Revision, _ = strconv.Atoi(m[1])
	}
	r.InstanceVLANs = parseMSTInstanceRows(text)
	return r
}

// parseMSTInstanceRows reads the instance→VLAN table, following wrapped rows.
//
// LINE BY LINE rather than one global FindAll, for the reason the Arista repair
// records: a row whose VLAN list wraps contributes SEVERAL lines, and a pattern
// that only matches the numbered one keeps the first fragment and silently drops
// the rest. There the loss was 83 VLANs out of 100, with no error — a partial map
// that reads as authoritative, which is the outcome this function must not have.
func parseMSTInstanceRows(text string) map[int][]int {
	var out = map[int][]int{}
	var current = -1
	var line string
	for _, line = range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		var m = reMSTInstExec.FindStringSubmatch(line)
		if m != nil {
			var id, err = strconv.Atoi(m[1])
			if err != nil {
				current = -1
				continue
			}
			current = id
			out[id] = append(out[id], parseVLANList(m[2])...)
			continue
		}
		// A continuation belongs to the row above, and only to it: a line that
		// matches nothing ENDS the row rather than letting a later indented number
		// be attached to an instance it has nothing to do with.
		if current >= 0 {
			m = reMSTInstCont.FindStringSubmatch(line)
			if m != nil {
				out[current] = append(out[current], parseVLANList(m[1])...)
				continue
			}
		}
		current = -1
	}
	// Instance 0 is the CIST and is implicit in the contract's map.
	delete(out, 0)
	if len(out) == 0 {
		return nil
	}
	var k int
	for k = range out {
		out[k] = sortedUniqueInts(out[k])
	}
	return out
}

// parseVLANList expands "42,98-100" into the VLAN ids it names.
func parseVLANList(s string) []int {
	var out []int
	var part string
	for _, part = range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var lo, hi, ok = splitVLANRange(part)
		if !ok {
			continue
		}
		var v int
		for v = lo; v <= hi && v-lo < 4096; v++ {
			out = append(out, v)
		}
	}
	return out
}

// splitVLANRange reads "42" or "98-100". A range whose ends are reversed or
// out of the VLAN space is REFUSED rather than clamped: a map built from a
// misread line is the thing parseMSTInstanceRows exists to avoid.
func splitVLANRange(part string) (int, int, bool) {
	var dash = strings.IndexByte(part, '-')
	if dash < 0 {
		var v, err = strconv.Atoi(part)
		if err != nil || v < 1 || v > 4094 {
			return 0, 0, false
		}
		return v, v, true
	}
	var lo, lerr = strconv.Atoi(part[:dash])
	var hi, herr = strconv.Atoi(part[dash+1:])
	if lerr != nil || herr != nil || lo < 1 || hi > 4094 || lo > hi {
		return 0, 0, false
	}
	return lo, hi, true
}

// sortedUniqueInts is the shape [driver.MSTRegion.InstanceVLANs] is compared in.
func sortedUniqueInts(in []int) []int {
	var seen = map[int]bool{}
	var out []int
	var v int
	for _, v = range in {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	var i, j int
	for i = 1; i < len(out); i++ {
		var x = out[i]
		for j = i; j > 0 && out[j-1] > x; j-- {
			out[j] = out[j-1]
		}
		out[j] = x
	}
	return out
}

func iosSTPRole(s string) string {
	switch strings.ToLower(s) {
	case "root":
		return driver.STPRoleRoot
	case "desg":
		return driver.STPRoleDesignated
	case "altn":
		return driver.STPRoleAlternate
	case "back":
		return driver.STPRoleBackup
	case "mstr":
		return "master"
	}
	return ""
}

func iosSTPState(s string) string {
	switch strings.ToUpper(s) {
	case "FWD":
		return driver.STPStateForwarding
	case "LRN":
		return driver.STPStateLearning
	case "BLK", "LIS", "DIS":
		return driver.STPStateDiscarding
	}
	return ""
}

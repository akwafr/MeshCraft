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
// IOS defaults to per-VLAN spanning tree. Its « show spanning-tree » prints one
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
// « rapid-pvst » is what IOS calls RSTP, and the distinction matters: it is
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
		// « no spanning-tree vlan <list> » disables it per VLAN on this platform,
		// and there is no global off switch. Turning spanning tree off across a
		// campus switch is not a gesture meshcraft performs from a site preference.
		out.Skipped = append(out.Skipped,
			"spanning tree is NOT disabled on this platform by meshcraft: IOS has no "+
				"global off switch, only « no spanning-tree vlan <list> », and silently "+
				"removing loop protection from a campus switch is not something a site "+
				"preference should be able to do")
		return out, nil
	}

	var mode, ok = iosSTPMode(plan.Mode)
	if !ok {
		out.Skipped = append(out.Skipped, "unsupported STP mode "+plan.Mode)
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
	return out, nil
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

// « show spanning-tree » on IOS, as captured on a 2960-X (15.2(6)E) on
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
// "protocol" says. Under mst the same command prints « MST0 » sections instead.
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
	// « spanning-tree mst configuration » as show run prints it back.
	reMSTName = regexp.MustCompile(`(?im)^\s*name\s+(\S+)\s*$`)
	reMSTRev  = regexp.MustCompile(`(?im)^\s*revision\s+(\d+)\s*$`)
)

// ReadSpanningTree reports what the device believes about the tree.
//
// READ ONLY and replayable. Two commands: the tree itself, and the MST region —
// which « show spanning-tree » does NOT print, and which is the thing that
// silently decides whether this switch is in the same region as its neighbours.
func (a *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out driver.SpanningTreeState
	if a.cli == nil {
		return out, fmt.Errorf("no open session on this device")
	}
	var text, err = a.cli.run(ctx, "show spanning-tree", saveTimeout)
	if err != nil && text == "" {
		return out, err
	}
	out = parseShowSpanningTree(text)

	// The region lives in the configuration, not in the tree view. Absent on a
	// device that is not in mst, and best-effort: a region that cannot be read
	// leaves the field empty rather than failing a read that otherwise worked.
	if out.Mode == "mstp" {
		var cfg string
		cfg, err = a.cli.run(ctx, "show current", cmdTimeout)
		if err == nil {
			out.Region = parseMSTRegion(cfg)
		}
	}
	return out, nil
}

// parseShowSpanningTree turns the per-VLAN (or per-MST) view into one state.
//
// The FIRST section decides the bridge-level facts. On a per-VLAN platform every
// section repeats them with a different sys-id-ext, and reporting one of them is
// the only honest summary — meshcraft's model carries a single tree, and picking
// the first is at least deterministic.
//
// PVST+ IS DETECTED FROM THE SECTION HEADINGS, not from the protocol word: IOS
// prints « protocol rstp » under a « VLAN0001 » heading when it is running Rapid
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

	var proto = ""
	if m := reSTPProtocol.FindStringSubmatch(text); m != nil {
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
	if m := reSTPBridgeP.FindStringSubmatch(head); m != nil {
		out.BridgePriority, _ = strconv.Atoi(m[1])
	}
	if m := reSTPBridgeA.FindStringSubmatch(head); m != nil {
		out.BridgeID = m[1]
	}
	if m := reSTPRootPrio.FindStringSubmatch(head); m != nil {
		out.RootPriority, _ = strconv.Atoi(m[1])
	}
	if m := reSTPRootAddr.FindStringSubmatch(head); m != nil {
		out.RootBridgeID = m[1]
	}
	out.IsRoot = reSTPIsRoot.MatchString(head)
	if !out.IsRoot {
		if m := reSTPRootCost.FindStringSubmatch(head); m != nil {
			out.RootPathCost, _ = strconv.Atoi(m[1])
		}
	}

	// Ports, deduplicated across sections: the same interface appears once per
	// VLAN, and meshcraft's model carries one row per port.
	var seen = map[string]bool{}
	var m []string
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

// parseMSTRegion reads the region out of « show current » (the mst
// configuration sub-mode's own view).
//
// The instance map is NOT parsed. The digest that decides region membership
// covers all 4094 VLANs, and reporting a partial map read from a text view would
// invite a comparison that looks authoritative and is not — name and revision
// are exact, and that is what is claimed.
func parseMSTRegion(text string) driver.MSTRegion {
	var r driver.MSTRegion
	if m := reMSTName.FindStringSubmatch(text); m != nil {
		r.Name = m[1]
	}
	if m := reMSTRev.FindStringSubmatch(text); m != nil {
		r.Revision, _ = strconv.Atoi(m[1])
	}
	return r
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

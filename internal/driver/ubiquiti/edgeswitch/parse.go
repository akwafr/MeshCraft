package edgeswitch

// Parsers for the EdgeSwitch CLI, written against outputs CAPTURED on a lab
// EdgePoint S16 running firmware 1.9.2 (2026-08-03). The captures live in
// testdata/ and parse_test.go replays them.
//
// The house style of this CLI is "Label....... Value" with a run of dots,
// and tables with a dashed rule under the header.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// --- dotted-label values -----------------------------------------------

// reDotted matches "Machine Model.................... EP-S16".
var reDotted = regexp.MustCompile(`(?m)^\s*([A-Za-z][^.\n]*?)\s*\.{2,}\s*(.*?)\s*$`)

// dottedFields turns a "Label....... Value" block into a map keyed by the
// lower-cased label. Labels repeat across commands ("Burned In MAC Address"
// appears in both show version and show network), which is why the caller asks
// for what it wants rather than positionally reading.
func dottedFields(out string) map[string]string {
	var m = map[string]string{}
	var rows = reDotted.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var k = strings.ToLower(strings.TrimSpace(rows[i][1]))
		var _, seen = m[k]
		if seen {
			continue // first wins: later blocks repeat labels
		}
		m[k] = strings.TrimSpace(rows[i][2])
	}
	return m
}

// --- show version ------------------------------------------------------

type versionFacts struct {
	Model    string // EP-S16
	Type     string // EdgePoint S16
	Firmware string // 1.9.2
	Serial   string // 9C05D6F5F137
	MAC      string // 9C:05:D6:F5:F1:37
}

func parseShowVersion(out string) versionFacts {
	var f = dottedFields(out)
	return versionFacts{
		Model:    f["machine model"],
		Type:     f["machine type"],
		Firmware: f["software version"],
		Serial:   f["serial number"],
		MAC:      strings.ToLower(f["burned in mac address"]),
	}
}

// --- show network ------------------------------------------------------
//
// Management is NOT an SVI here: one network interface, whose addressing this
// command reports. "Configured IPv4 Protocol" is None for a static address
// and DHCP/BootP for a leased one.

func parseShowNetwork(out string) driver.MgmtAddrState {
	var f = dottedFields(out)
	var st driver.MgmtAddrState
	var ip = f["ip address"]
	var mask = f["subnet mask"]
	if ip != "" && mask != "" {
		var cidr, err = driver.JoinAddrMask(ip, mask)
		if err == nil {
			st.Address = cidr
		} else {
			st.Address = ip
		}
	}
	st.Gateway = f["default gateway"]
	st.Mode = driver.MgmtAddrModeStatic
	if strings.Contains(strings.ToLower(f["configured ipv4 protocol"]), "dhcp") ||
		strings.Contains(strings.ToLower(f["configured ipv4 protocol"]), "bootp") {
		st.Mode = driver.MgmtAddrModeDHCP
	}
	var mgmtVlan = f["management vlan id"]
	if mgmtVlan != "" {
		st.Interface = "vlan " + mgmtVlan
	}
	st.Notes = append(st.Notes,
		"management on this platform is a single network interface (\"network parms\"), "+
			"not an SVI — the VLAN is chosen separately with \"network mgmt_vlan\"")
	return st
}

// MgmtVLANOf returns the management VLAN id "show network" reports.
func MgmtVLANOf(out string) int {
	var v, err = strconv.Atoi(dottedFields(out)["management vlan id"])
	if err != nil {
		return 0
	}
	return v
}

// --- show port all -----------------------------------------------------
//
//	Intf      Type   Admin     Physical   Physical   Link   Link    LACP   Flow
//	                 Mode      Mode       Status     Status Trap    Mode   Mode
//	--------- ------ --------- ---------- ---------- ------ ------- ------ -------
//	0/1              Enable    Auto                  Down   Enable  Enable Disable
//	0/18             Enable    Auto D     10G Full   Up     Enable  Enable Disable
//	3/1              Enable                          Down   Enable  N/A    Disable
//
// Slot 3 rows are LAG interfaces, NOT sockets: a parser that takes every row
// as a faceplate port invents six jacks that do not exist on the front panel.
// StandardPortName refuses them, which is what keeps them out.
//
// CAPTURED, and easy to miss: 0/17 and 0/18 — the two SFP+ cages on the lab
// unit — carry an extra "D" in the Physical Mode column ("Auto D"
// instead of the plain "Auto" every one of 0/1..0/16 shows), and 0/18
// negotiates "10G Full" once linked. The reference does not spell out what
// the trailing "D" stands for; nothing else in the row shape distinguishes
// a copper jack from an SFP+ cage on this platform, so it is read here as
// that signal. A row without it is assumed copper.

var rePortRow = regexp.MustCompile(`(?m)^\s*(\d+/\d+)\s+(.*)$`)
var reAutoD = regexp.MustCompile(`(?i)\bAuto\s+D\b`)

// reAdminToken is the Admin Mode column: the FIRST of the row's Enable/Disable
// words. The Type column sits between the interface name and it, and is BLANK on
// this hardware — it never carries either word, which is what makes "first"
// enough. Positional slicing is not an option on a row with empty columns.
var reAdminToken = regexp.MustCompile(`(?i)\b(Enable|Disable)\b`)

// reLinkUp is the Link Status column's standalone word. "10G Full" (Physical
// Status) never matches it, and neither does anything else on the row shapes
// captured.
var reLinkUp = regexp.MustCompile(`(?i)\bUp\b`)

var rePhysical = regexp.MustCompile(`(?i)\b(\d+G?\s+(?:Full|Half))\b`)

// PortLine is one row of "show port all".
type PortLine struct {
	Intf string // the platform's own "0/18"
	Name string // standard name, empty when not a faceplate socket
	// AdminUp: the ADMIN MODE column, and only it — the FIRST Enable/Disable
	// token after the interface name.
	//
	// This used to be `strings.Contains(low, "enable")` over the whole rest of the
	// row, and every row on this platform carries "Enable" twice more (Link Trap,
	// LACP Mode), so it read TRUE on a port whose Admin Mode said "Disable".
	// [LinkStates] keys [driver.LinkDisabled] on it, which meant a shut port could
	// never be shown as shut on the faceplate — it appeared merely unplugged, the
	// one reading that makes an operator go looking for a cable. portadmin.go
	// carried a second parser of the same rows for exactly this reason.
	AdminUp bool
	// AdminSeen: the Admin Mode column was present and readable. Distinguishes
	// "the device says Disable" from "this row had no such column", which are
	// not the same claim: only the first is a shut port.
	AdminSeen bool
	LinkUp    bool
	Physical  string // "10G Full" when up
	// SFPCage: this row's Physical Mode carries the "D" suffix — cf. the
	// section header above. False for every plain-copper row.
	SFPCage bool
}

func parseShowPortAll(out string) []PortLine {
	var d = &Driver{}
	var lines []PortLine
	var rows = rePortRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var intf = rows[i][1]
		var rest = rows[i][2]
		var p = PortLine{Intf: intf}
		var std, ok = d.StandardPortName(intf)
		if ok {
			p.Name = std
		}
		// The columns are whitespace-separated and some are EMPTY on this
		// platform (Type is blank, Physical Status is blank when down), so the
		// row is read by looking for the words rather than by position.
		var admin = reAdminToken.FindStringSubmatch(rest)
		if admin != nil {
			p.AdminSeen = true
			p.AdminUp = strings.EqualFold(admin[1], "Enable")
		}
		// "Link Status" is the standalone Up/Down word; "10G Full" carries
		// the physical status when a link exists.
		p.LinkUp = reLinkUp.MatchString(rest)
		var phys = rePhysical.FindStringSubmatch(rest)
		if phys != nil {
			p.Physical = phys[1]
		}
		p.SFPCage = reAutoD.MatchString(rest)
		lines = append(lines, p)
	}
	return lines
}

// LinkStates keeps only the faceplate sockets.
func LinkStates(out string) map[string]driver.LinkState {
	var links = map[string]driver.LinkState{}
	var rows = parseShowPortAll(out)
	var i int
	for i = range rows {
		if rows[i].Name == "" {
			continue // LAG or unknown shape
		}
		switch {
		case rows[i].AdminSeen && !rows[i].AdminUp:
			// AdminSeen is required: a row whose Admin Mode column could not be read
			// says nothing about administration, and calling it LinkDisabled would
			// invent a shut port out of a parse miss. Its link is still readable, so
			// the two branches below answer for it.
			links[rows[i].Name] = driver.LinkDisabled
		case rows[i].LinkUp:
			links[rows[i].Name] = driver.LinkUpState
		default:
			links[rows[i].Name] = driver.LinkDown
		}
	}
	return links
}

// --- show vlan ---------------------------------------------------------
//
//	VLAN ID VLAN Name                        VLAN Type
//	------- -------------------------------- -------------------
//	1       default                          Default
//	99      LAN99_Management                 Static
//
// NOTE this command carries NO port membership, unlike the FS platforms where
// one command gives both. Membership needs "show vlan port all".

var reVlanRow = regexp.MustCompile(`(?m)^\s*(\d{1,4})\s+(\S.*?)\s{2,}(\S+)\s*$`)

func parseShowVlan(out string) []driver.VLAN {
	var out2 []driver.VLAN
	var rows = reVlanRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var id, err = strconv.Atoi(rows[i][1])
		if err != nil || id < 1 || id > 4093 {
			continue
		}
		out2 = append(out2, driver.VLAN{ID: id, Name: strings.TrimSpace(rows[i][2])})
	}
	return out2
}

// --- show lldp remote-device all ---------------------------------------
//
//	Local
//	Interface  RemID   Chassis ID            Port ID             System Name
//	--------- -------  --------------------  ------------------  ------------------
//	0/1
//	0/18       4       64:9D:99:22:7B:95     TGi0/1              poefs3400lab
//
// Most rows are the local interface ALONE — a port with no neighbour. Only the
// rows that carry a chassis id are neighbours.

var reLLDPRow = regexp.MustCompile(`(?m)^\s*(\d+/\d+)\s+(\d+)\s+(\S+)\s+(\S+)(?:\s+(\S.*?))?\s*$`)

// LLDPNeighbor is one entry of the neighbour table.
type LLDPNeighbor struct {
	LocalPort string // standard name
	RemoteID  string
	ChassisID string
	PortID    string
	SysName   string
}

func parseLLDPRemote(out string) []LLDPNeighbor {
	var d = &Driver{}
	var out2 []LLDPNeighbor
	var rows = reLLDPRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var std, ok = d.StandardPortName(rows[i][1])
		if !ok {
			continue
		}
		out2 = append(out2, LLDPNeighbor{
			LocalPort: std,
			RemoteID:  rows[i][2],
			ChassisID: strings.ToLower(rows[i][3]),
			PortID:    rows[i][4],
			SysName:   strings.TrimSpace(rows[i][5]),
		})
	}
	return out2
}

// --- show running-config (per-port VLAN membership) ---------------------
//
// "show vlan port all" — what state.go used to read this from — turned
// out to be the wrong source: it gave a pvid but nothing distinguished a
// trunk from an access port, and the field that used to fill Mode set it to
// "access" UNCONDITIONALLY, on every row, trunk or not (fixed alongside this
// comment — cf. TODO.md). A trunk applied correctly then read back as
// access with no tagged VLANs, forever, because ReadState believed a
// confidently wrong observation over a right one it never made.
//
// The running config is read instead, because it is made of the SAME THREE
// COMMANDS PortCmds (apply.go) already writes — "vlan pvid N", "vlan
// participation include N", "vlan tagging N" — inside an
// "interface 0/N" block. CONFIRMED AGAINST A REAL CAPTURE, with one
// correction that was the whole GE17/GE18 "permanent drift" bug (cf.
// TODO.md): captured 2026-08-05 on the lab EdgePoint S16, firmware 1.9.3
// (testdata/show_running_config.txt), the dump does NOT echo the commands
// one VLAN at a time the way apply.go writes them. It COALESCES a port's
// membership into list syntax —
//
//	interface 0/17
//	vlan participation include 42,98-100
//	vlan tagging 42,98-100
//	exit
//
// — commas and ranges, one line per command form. A reader that only
// recognizes the single-id form ("vlan tagging 42", as this parser did
// before that capture) silently misses EVERY trunk whose dump coalesced,
// reads the port back as bare access, and manufactures permanent drift on a
// correctly configured device — while also MASKING real drift, since a port
// whose stale tagging was unreadable compared equal to a desired plain
// access port. "vlan pvid N" stays single-id in the same capture (a port
// has one pvid), and LAG blocks appear as "interface lag N", which
// StandardPortName already refuses.
//
// Mode is INFERRED, never printed: this CLI has no "switchport mode"
// keyword (cf. the package doc comment on FASTPATH). A port carrying at
// least one "vlan tagging N" line is a trunk; everything else stays
// access — the exact rule PortCmds itself encodes when it decides whether
// to emit a tagging line at all.

var (
	reRunIface = regexp.MustCompile(`(?i)^\s*interface\s+(\d+/\d+)\s*$`)
	reRunPVID  = regexp.MustCompile(`(?i)^\s*vlan\s+pvid\s+(\d+)\s*$`)
	// The capture group takes the COALESCED list form the dump uses —
	// "42,98-100" — of which a single id is the degenerate case (cf. the
	// section header above; expandVlanList undoes the coalescing).
	reRunTagging = regexp.MustCompile(`(?i)^\s*vlan\s+tagging\s+([0-9][0-9,-]*)\s*$`)
	reRunExit    = regexp.MustCompile(`(?i)^\s*exit\s*$`)
)

// expandVlanList reads the config dump's coalesced VLAN list — "42,98-100"
// — back into individual ids: comma-separated pieces, each either one id or
// an inclusive lo-hi range. A piece that does not parse, or a range written
// backwards, is skipped rather than poisoning the ids around it: better to
// under-read one VLAN than to invent membership no line states.
func expandVlanList(s string) []int {
	var out []int
	var piece string
	for _, piece = range strings.Split(s, ",") {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		var lo, hi = piece, piece
		var dash = strings.IndexByte(piece, '-')
		if dash >= 0 {
			lo, hi = piece[:dash], piece[dash+1:]
		}
		var a, errA = strconv.Atoi(lo)
		var b, errB = strconv.Atoi(hi)
		if errA != nil || errB != nil || a < 1 || b > 4093 || b < a {
			continue
		}
		var id int
		for id = a; id <= b; id++ {
			out = append(out, id)
		}
	}
	return out
}

// parseRunningConfigPorts reads per-port VLAN membership from "show
// running-config". A port with no recognized line in its block is simply
// absent from the returned map — ReadState's own default (access, pvid 1)
// carries it instead, rather than this function guessing.
func parseRunningConfigPorts(out string) map[string]driver.PortState {
	var d = &Driver{}
	var ports = map[string]driver.PortState{}
	var cur string
	var pvid int
	var tagged []int

	var closeBlock = func() {
		if cur == "" {
			return
		}
		var ps driver.PortState
		if pvid > 0 {
			ps.PVID = pvid
		} else {
			ps.PVID = 1
		}
		if len(tagged) > 0 {
			sort.Ints(tagged)
			// Deduplicated: the same id can arrive twice when a dump carries
			// both a coalesced list and a later single-id line for one port.
			var uniq = tagged[:1]
			var i int
			for i = 1; i < len(tagged); i++ {
				if tagged[i] != tagged[i-1] {
					uniq = append(uniq, tagged[i])
				}
			}
			ps.Mode = "trunk"
			ps.Tagged = uniq
		} else {
			ps.Mode = "access"
		}
		ports[cur] = ps
		cur, pvid, tagged = "", 0, nil
	}

	var line string
	for _, line = range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		var m = reRunIface.FindStringSubmatch(line)
		if m != nil {
			closeBlock()
			var std, ok = d.StandardPortName(strings.TrimSpace(m[1]))
			if ok {
				cur = std
			}
			continue
		}
		if cur == "" {
			continue // outside any recognized interface block
		}
		if reRunExit.MatchString(line) {
			closeBlock()
			continue
		}
		m = reRunPVID.FindStringSubmatch(line)
		if m != nil {
			pvid, _ = strconv.Atoi(m[1])
			continue
		}
		m = reRunTagging.FindStringSubmatch(line)
		if m != nil {
			tagged = append(tagged, expandVlanList(m[1])...)
			continue
		}
		// "vlan participation include …" (single id or coalesced list) with
		// no matching "vlan tagging" is untagged membership — the pvid's own
		// line, which PortCmds always writes alongside "vlan pvid N" — and
		// "vlan participation exclude N" is absence: neither carries
		// information beyond what pvid/tagging above already give, so both are
		// deliberately not matched here. Both forms appear in the 2026-08-05
		// capture.
	}
	closeBlock()
	return ports
}

// --- show spanning-tree summary ----------------------------------------
//
//	Spanning Tree Admin Mode.......... Enabled
//	Spanning Tree Version............. IEEE 802.1s
//	BPDU Guard Mode................... Disabled
//	Configuration Name................ 9C-05-D6-F5-F1-37
//	Configuration Revision Level...... 0
//	MST Instance ID................... 0
//	Interface   State           Role
//	--------- ----------------- ----------
//	0/18      Forwarding        Root
//
// What the summary does NOT carry on this platform: any bridge or root
// identifier (captured 2026-08-05, firmware 1.9.3 — the whole dump is in
// testdata/show_spanning_tree_summary.txt). Those live in the plain
// "show spanning-tree", read by parseSpanningTreeDetail below; the pair
// together is one SpanningTreeState.

func parseSpanningTreeSummary(out string) driver.SpanningTreeState {
	var f = dottedFields(out)
	var st driver.SpanningTreeState
	st.Enabled = strings.EqualFold(f["spanning tree admin mode"], "enabled")
	st.Mode = stpModeOf(f["spanning tree version"])
	// The MST region, so a region mismatch — which 802.1s hides by silently
	// downgrading the neighbour to RSTP — is comparable from the read.
	st.Region.Name = f["configuration name"]
	var rev, revErr = strconv.Atoi(f["configuration revision level"])
	if revErr == nil {
		st.Region.Revision = rev
	}
	if strings.EqualFold(f["bpdu guard mode"], "enabled") {
		st.Notes = append(st.Notes, "BPDU guard enabled globally")
	}
	if strings.Contains(strings.ToLower(out), "no mst instances is root bridge") {
		st.IsRoot = false
		st.Notes = append(st.Notes, "this bridge is root for no MST instance")
	}
	st.Ports = parseSTPInterfaceTable(out)
	return st
}

// --- show spanning-tree --------------------------------------------------
//
//	Spanning Tree Protocol......................... mstp
//	CST Regional Root.............................. 80:00:9C:05:D6:F5:F1:37
//	MST0
//	Bridge Priority................................ 32768
//	Bridge Identifier.............................. 80:00:9C:05:D6:F5:F1:37
//	Designated Root................................ 00:00:D4:01:C3:AB:32:F4
//	Root Port Identifier........................... 0/18
//	Root Path Cost................................. 8000
//	Interface   State           Role
//	--------- ----------------- ----------
//	0/18      Forwarding        Root
//
// (captured 2026-08-05 on firmware 1.9.3 — testdata/show_spanning_tree.txt.)
// The identifiers are 8-byte 802.1D bridge IDs: a 2-byte priority then the
// MAC, colon-separated. "Designated Root" is who this bridge believes the
// root is; equality with its own identifier is what "I am root" looks like.

// mergeSpanningTreeDetail fills what the summary cannot say — bridge and root
// identifiers, priorities, root path cost — from the plain "show
// spanning-tree". It never erases what the summary already read.
func mergeSpanningTreeDetail(out string, st *driver.SpanningTreeState) {
	var f = dottedFields(out)
	if st.Mode == "" {
		st.Mode = stpModeOf(f["spanning tree protocol"])
	}
	var v, vErr = strconv.Atoi(f["bridge priority"])
	if vErr == nil {
		st.BridgePriority = v
	}
	var prio, mac, ok = splitBridgeIdentifier(f["bridge identifier"])
	if ok {
		st.BridgeID = mac
		if st.BridgePriority == 0 {
			st.BridgePriority = prio
		}
	}
	prio, mac, ok = splitBridgeIdentifier(f["designated root"])
	if ok {
		st.RootBridgeID = mac
		st.RootPriority = prio
	}
	v, vErr = strconv.Atoi(f["root path cost"])
	if vErr == nil {
		st.RootPathCost = v
	}
	if st.BridgeID != "" && st.BridgeID == st.RootBridgeID {
		// Overrides the summary's "root for no MST instance" pessimism: for
		// the CIST the tie-breaker is this pair of identifiers, nothing else.
		st.IsRoot = true
	}
	if len(st.Ports) == 0 {
		st.Ports = parseSTPInterfaceTable(out)
	}
}

// reSTPIfaceRow is one row of the Interface/State/Role table both
// spanning-tree reads print. The state can be two words ("Manual Fwd"), so
// the row is anchored on the interface and the ROLE is the last field.
var reSTPIfaceRow = regexp.MustCompile(`(?m)^(\d+/\d+)\s+(\S+(?: \S+)?)\s+(\S+)\s*$`)

func parseSTPInterfaceTable(out string) []driver.SpanningTreePort {
	var ports []driver.SpanningTreePort
	var seen = map[string]bool{}
	var d = &Driver{}
	var rows = reSTPIfaceRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var name, ok = d.StandardPortName(rows[i][1])
		if !ok || seen[name] {
			// A port can appear once PER MST INSTANCE; the CIST row (printed
			// first) is the one the topology view reasons about.
			continue
		}
		seen[name] = true
		ports = append(ports, driver.SpanningTreePort{
			Port:  name,
			State: stpStateWord(rows[i][2]),
			Role:  stpRoleWord(rows[i][3]),
		})
	}
	return ports
}

// stpStateWord and stpRoleWord map the platform's table words onto the shared
// vocabulary (driver.STPState* / STPRole*), so the topology view's
// "discarding means blocked" test holds across connectors.
func stpStateWord(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "forwarding", "manual fwd":
		return driver.STPStateForwarding
	case "discarding", "blocking":
		return driver.STPStateDiscarding
	case "learning":
		return driver.STPStateLearning
	}
	return strings.ToLower(strings.TrimSpace(s))
}

func stpRoleWord(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "root":
		return driver.STPRoleRoot
	case "designated", "desg":
		return driver.STPRoleDesignated
	case "alternate", "altn":
		return driver.STPRoleAlternate
	case "backup", "back":
		return driver.STPRoleBackup
	case "master", "mstr":
		return driver.STPRoleMaster
	case "disabled":
		return driver.STPRoleDisabled
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// splitBridgeIdentifier decodes an 802.1D bridge identifier — 2 bytes of
// priority then the MAC, "80:00:9C:05:D6:F5:F1:37" (or dotted on other
// firmware) — into the priority and the product's lowercase-colon MAC form,
// the one LLDP link resolution compares against.
func splitBridgeIdentifier(s string) (int, string, bool) {
	var hex = strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, s)
	if len(hex) != 16 {
		return 0, "", false
	}
	var prio, err = strconv.ParseUint(hex[:4], 16, 16)
	if err != nil {
		return 0, "", false
	}
	var parts []string
	var i int
	for i = 4; i < 16; i += 2 {
		parts = append(parts, strings.ToLower(hex[i:i+2]))
	}
	return int(prio), strings.Join(parts, ":"), true
}

// stpModeOf maps the platform's protocol wording to the shared vocabulary.
// The lab unit reports "IEEE 802.1s", i.e. MSTP — not the RSTP a Cisco habit
// would expect.
func stpModeOf(version string) string {
	var v = strings.ToLower(version)
	switch {
	case strings.Contains(v, "802.1s"), strings.Contains(v, "mst"):
		return "mstp"
	case strings.Contains(v, "802.1w"), strings.Contains(v, "rapid"):
		return "rstp"
	case strings.Contains(v, "802.1d"):
		return "stp"
	}
	return strings.TrimSpace(v)
}

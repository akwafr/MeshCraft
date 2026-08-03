// Package confimport translates the configuration read from a device into
// meshcraft's standard data model (VLANs + per-port intent).
//
// This is the "import the local configuration" path offered at adoption
// time: instead of overwriting the device with whatever the platform thinks
// is right, we read what already exists and bring it into the model. So the
// user sees their real VLAN plan right from adoption.
//
// Two dialects are covered, which together account for the majority of
// switch CLIs:
//
//   - "ios": Cisco IOS/IOS-XE/NX-OS, and the many CLIs that mimic it
//     (Aruba/HPE, Dell, Netgear, TP-Link JetStream, Zyxel...);
//   - "comware": Huawei VRP and H3C/Comware.
//
// Whatever isn't recognized isn't invented: every configuration line that is
// ignored despite carrying VLAN meaning produces a warning, and the user
// sees exactly what was imported before confirming.
package confimport

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Vlan struct {
	VlanID int    `json:"vlan_id"`
	Name   string `json:"name"`
}

// Port follows the standard model: mode, PVID, tagged VLANs.
type Port struct {
	Label  string `json:"label,omitempty"`
	Mode   string `json:"mode"` // libre | access | trunk
	PVID   *int   `json:"pvid,omitempty"`
	Tagged any    `json:"tagged,omitempty"` // []int or "all"
}

// PortGroup describes the physical front panel deduced from interface names.
type PortGroup struct {
	Prefix    string `json:"prefix"`
	From      int    `json:"from"`
	To        int    `json:"to"`
	SpeedMbps int    `json:"speed_mbps"`
	PoE       bool   `json:"poe"`
	Label     string `json:"label,omitempty"`
}

type Result struct {
	Dialect    string          `json:"dialect"`
	Hostname   string          `json:"hostname,omitempty"`
	Vlans      []Vlan          `json:"vlans"`
	Ports      map[string]Port `json:"ports"`
	PortGroups []PortGroup     `json:"port_groups"`
	Warnings   []string        `json:"warnings"`
}

// DetectDialect guesses the dialect from syntax markers.
func DetectDialect(text string) string {
	var lower = strings.ToLower(text)
	if strings.Contains(lower, "port link-type") || strings.Contains(lower, "allow-pass") ||
		strings.Contains(lower, "port default vlan") {
		return "comware"
	}
	if strings.Contains(lower, "switchport") || strings.Contains(lower, "interface ") {
		return "ios"
	}
	return ""
}

var (
	reHostname = regexp.MustCompile(`(?i)^\s*(?:hostname|sysname)\s+"?([^\s"]+)"?`)
	reVlanDecl = regexp.MustCompile(`(?i)^\s*vlan\s+([0-9,\s\-]+?)\s*$`)
	// reVlanInline: inline named declaration from the Accton/FSOS dialect
	// (FS S3900, observed in lab testing, OpCode 1.7.0): inside the "vlan
	// database" block, "VLAN 99 name LAN99_Management media ethernet".
	reVlanInline = regexp.MustCompile(`(?i)^\s*vlan\s+(\d+)\s+name\s+"?([^\s"]+)"?(?:\s+media\s+\S+)?(?:\s+state\s+\S+)?\s*$`)
	reVlanName   = regexp.MustCompile(`(?i)^\s*name\s+"?(.+?)"?\s*$`)
	// reInterface also accepts the TWO-word interface names of the
	// Accton/FSOS dialect: "interface ethernet 1/1", "interface vlan 99".
	// The second word is restricted to indices (digits, "/") so it doesn't
	// swallow "interface range GigabitEthernet1/0/1 - 24".
	reInterface    = regexp.MustCompile(`(?i)^\s*interface\s+(\S+(?:\s+[\d/]+)?)\s*$`)
	reDescription  = regexp.MustCompile(`(?i)^\s*description\s+"?(.+?)"?\s*$`)
	reSwMode       = regexp.MustCompile(`(?i)^\s*switchport\s+mode\s+(access|trunk)`)
	reSwAccess     = regexp.MustCompile(`(?i)^\s*switchport\s+access\s+vlan\s+(\d+)`)
	reSwTrunkNat   = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+native\s+vlan\s+(\d+)`)
	reSwTrunkAllow = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+allowed\s+vlan\s+(?:add\s+)?(\S+)`)
	reSwVoice      = regexp.MustCompile(`(?i)^\s*switchport\s+voice\s+vlan\s+(\d+)`)
	reNoSwitchport = regexp.MustCompile(`(?i)^\s*no\s+switchport\s*$`)
	reCwLinkType   = regexp.MustCompile(`(?i)^\s*port\s+link-type\s+(access|trunk|hybrid)`)
	reCwDefVlan    = regexp.MustCompile(`(?i)^\s*port\s+default\s+vlan\s+(\d+)`)
	reCwTrunkPvid  = regexp.MustCompile(`(?i)^\s*port\s+trunk\s+pvid\s+vlan\s+(\d+)`)
	reCwTrunkPerm  = regexp.MustCompile(`(?i)^\s*port\s+trunk\s+(?:permit|allow-pass)\s+vlan\s+(.+?)\s*$`)
	reCwHybridVlan = regexp.MustCompile(`(?i)^\s*port\s+hybrid\s+(?:tagged|untagged)\s+vlan\s+(.+?)\s*$`)
	rePoE          = regexp.MustCompile(`(?i)^\s*(?:power\s+inline|poe\s+enable|poe-power)`)
	reIfaceIndex   = regexp.MustCompile(`^(.*?)(\d+)$`)
)

// Parse reads a configuration and translates it. Empty dialect = auto-detect.
func Parse(text, dialect string) (Result, error) {
	if strings.TrimSpace(text) == "" {
		return Result{}, fmt.Errorf("empty configuration")
	}
	if dialect == "" {
		dialect = DetectDialect(text)
	}
	if dialect == "" {
		return Result{}, fmt.Errorf(
			"unrecognized dialect: no known VLAN syntax found. " +
				"Paste the output of \"show running-config\" (Cisco/Aruba/Dell...) " +
				"or \"display current-configuration\" (Huawei/H3C)")
	}

	var res = Result{
		Dialect: dialect,
		Vlans:   []Vlan{},
		Ports:   map[string]Port{},
		// PortGroups stays non-nil: the UI iterates over it without a guard.
		PortGroups: []PortGroup{},
		Warnings:   []string{},
	}

	// Current context: we're either inside a vlan block or inside an interface.
	var curVlans []int
	var curIface string
	var poeSeen = map[string]bool{}

	var lines = strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	var raw string
	for _, raw = range lines {
		var line = strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		// A lone "!" or "return" closes the current block.
		var trimmed = strings.TrimSpace(line)
		if trimmed == "!" || trimmed == "#" || strings.EqualFold(trimmed, "return") ||
			strings.EqualFold(trimmed, "exit") || strings.EqualFold(trimmed, "quit") {
			curVlans = nil
			curIface = ""
			continue
		}

		var m []string

		if m = reHostname.FindStringSubmatch(line); m != nil {
			res.Hostname = m[1]
			continue
		}

		if m = reInterface.FindStringSubmatch(line); m != nil {
			curVlans = nil
			curIface = m[1]
			// Logical interfaces (Vlan10, Loopback0, Null0...) aren't physical
			// ports: we don't bring them into the front panel.
			if isLogicalIface(curIface) {
				curIface = ""
				continue
			}
			var _, exists = res.Ports[curIface]
			if !exists {
				res.Ports[curIface] = Port{Mode: "libre"}
			}
			continue
		}

		// Inline name (Accton/FSOS) before the list form: "vlan 99 name X ..."
		// would otherwise match reVlanDecl on its ID alone.
		if m = reVlanInline.FindStringSubmatch(line); m != nil && curIface == "" {
			var id, idErr = strconv.Atoi(m[1])
			if idErr == nil && id >= 1 && id <= 4094 {
				res.Vlans = ensureVlan(res.Vlans, id)
				var idx = findVlan(res.Vlans, id)
				if idx >= 0 && res.Vlans[idx].Name == "" {
					res.Vlans[idx].Name = m[2]
				}
				continue
			}
		}

		if m = reVlanDecl.FindStringSubmatch(line); m != nil && curIface == "" {
			var ids, err = expandVlanList(m[1])
			if err != nil {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("unreadable VLAN list ignored: %q", trimmed))
				continue
			}
			curVlans = ids
			var id int
			for _, id = range ids {
				if findVlan(res.Vlans, id) < 0 {
					res.Vlans = append(res.Vlans, Vlan{VlanID: id})
				}
			}
			continue
		}

		// Inside a vlan block: "name" names the VLAN.
		if curVlans != nil && curIface == "" {
			if m = reVlanName.FindStringSubmatch(line); m != nil {
				if len(curVlans) == 1 {
					var idx = findVlan(res.Vlans, curVlans[0])
					if idx >= 0 {
						res.Vlans[idx].Name = m[1]
					}
				} else {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("\"name %s\" applies to %d VLANs at once: name not applied",
							m[1], len(curVlans)))
				}
				continue
			}
		}

		if curIface == "" {
			continue
		}

		// --- Inside an interface ---
		var p = res.Ports[curIface]

		switch {
		case reNoSwitchport.MatchString(line):
			// Routed port: outside the L2 model, we remove it from the front
			// panel so we don't show a "free" port that isn't one.
			delete(res.Ports, curIface)
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s is a routed port (no switchport): not imported", curIface))
			curIface = ""
			continue

		case reDescription.MatchString(line):
			p.Label = reDescription.FindStringSubmatch(line)[1]

		case rePoE.MatchString(line):
			poeSeen[curIface] = true

		// --- ios dialect ---
		case reSwMode.MatchString(line):
			p.Mode = strings.ToLower(reSwMode.FindStringSubmatch(line)[1])

		case reSwAccess.MatchString(line):
			var vid, _ = strconv.Atoi(reSwAccess.FindStringSubmatch(line)[1])
			p.PVID = &vid
			if p.Mode == "" || p.Mode == "libre" {
				p.Mode = "access"
			}
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reSwVoice.MatchString(line):
			var vid, _ = strconv.Atoi(reSwVoice.FindStringSubmatch(line)[1])
			p.Tagged = addTagged(p.Tagged, []int{vid})
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reSwTrunkNat.MatchString(line):
			var vid, _ = strconv.Atoi(reSwTrunkNat.FindStringSubmatch(line)[1])
			p.PVID = &vid
			p.Mode = "trunk"
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reSwTrunkAllow.MatchString(line):
			p.Mode = "trunk"
			var spec = reSwTrunkAllow.FindStringSubmatch(line)[1]
			if strings.EqualFold(spec, "all") || strings.EqualFold(spec, "1-4094") {
				p.Tagged = "all"
			} else {
				var ids, err = expandVlanList(spec)
				if err != nil {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("%s: unreadable trunk VLAN list (%q)", curIface, spec))
				} else {
					p.Tagged = addTagged(p.Tagged, ids)
					res.Vlans = ensureVlans(res.Vlans, ids)
				}
			}

		// --- comware / VRP dialect ---
		case reCwLinkType.MatchString(line):
			var t = strings.ToLower(reCwLinkType.FindStringSubmatch(line)[1])
			if t == "hybrid" {
				p.Mode = "trunk"
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s is in hybrid mode: imported as trunk, please verify", curIface))
			} else {
				p.Mode = t
			}

		case reCwDefVlan.MatchString(line):
			var vid, _ = strconv.Atoi(reCwDefVlan.FindStringSubmatch(line)[1])
			p.PVID = &vid
			if p.Mode == "" || p.Mode == "libre" {
				p.Mode = "access"
			}
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reCwTrunkPvid.MatchString(line):
			var vid, _ = strconv.Atoi(reCwTrunkPvid.FindStringSubmatch(line)[1])
			p.PVID = &vid
			p.Mode = "trunk"
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reCwTrunkPerm.MatchString(line):
			p.Mode = "trunk"
			var spec = reCwTrunkPerm.FindStringSubmatch(line)[1]
			if strings.EqualFold(strings.TrimSpace(spec), "all") {
				p.Tagged = "all"
			} else {
				var ids, err = expandVlanList(spec)
				if err != nil {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("%s: unreadable trunk VLAN list (%q)", curIface, spec))
				} else {
					p.Tagged = addTagged(p.Tagged, ids)
					res.Vlans = ensureVlans(res.Vlans, ids)
				}
			}

		case reCwHybridVlan.MatchString(line):
			var ids, err = expandVlanList(reCwHybridVlan.FindStringSubmatch(line)[1])
			if err == nil {
				p.Tagged = addTagged(p.Tagged, ids)
				res.Vlans = ensureVlans(res.Vlans, ids)
			}
		}

		res.Ports[curIface] = p
	}

	// A trunk without an explicit list lets all VLANs through on most
	// platforms: better to say so than to show an empty trunk.
	var name string
	var p Port
	for name, p = range res.Ports {
		if p.Mode == "trunk" && p.Tagged == nil {
			p.Tagged = "all"
			res.Ports[name] = p
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s: trunk without a VLAN list, interpreted as \"all\"", name))
		}
	}

	res.PortGroups = deriveGroups(res.Ports, poeSeen)
	sort.Slice(res.Vlans, func(i, j int) bool { return res.Vlans[i].VlanID < res.Vlans[j].VlanID })

	if len(res.Ports) == 0 {
		return res, fmt.Errorf("no port found in the supplied configuration")
	}
	return res, nil
}

func isLogicalIface(name string) bool {
	var lower = strings.ToLower(name)
	var prefix string
	for _, prefix = range []string{
		"vlan", "loopback", "lo", "null", "tunnel", "port-channel", "po",
		"bridge-aggregation", "eth-trunk", "management", "mgmt", "bvi", "nve",
		// "craft": the FS S3900's dedicated out-of-band management port
		// (observed in lab testing) — not a front-panel port.
		"craft",
	} {
		if strings.HasPrefix(lower, prefix) {
			// "ethernet" starts with "eth" but is not "eth-trunk".
			if prefix == "lo" && strings.HasPrefix(lower, "log") {
				continue
			}
			return true
		}
	}
	return false
}

// expandVlanList accepts "10,20,30-35" (ios) and "10 20 30 to 35" (comware).
func expandVlanList(spec string) ([]int, error) {
	var norm = strings.ToLower(strings.TrimSpace(spec))
	norm = strings.ReplaceAll(norm, " to ", "-")
	norm = strings.ReplaceAll(norm, ",", " ")
	var out = []int{}
	var seen = map[int]bool{}
	var tok string
	for _, tok = range strings.Fields(norm) {
		if strings.Contains(tok, "-") {
			var parts = strings.SplitN(tok, "-", 2)
			var lo, errLo = strconv.Atoi(strings.TrimSpace(parts[0]))
			var hi, errHi = strconv.Atoi(strings.TrimSpace(parts[1]))
			if errLo != nil || errHi != nil || lo < 1 || hi > 4094 || hi < lo {
				return nil, fmt.Errorf("invalid range %q", tok)
			}
			var v int
			for v = lo; v <= hi; v++ {
				if !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
			continue
		}
		var v, err = strconv.Atoi(tok)
		if err != nil || v < 1 || v > 4094 {
			return nil, fmt.Errorf("invalid VLAN identifier %q", tok)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty list")
	}
	sort.Ints(out)
	return out, nil
}

func findVlan(list []Vlan, id int) int {
	var i int
	for i = range list {
		if list[i].VlanID == id {
			return i
		}
	}
	return -1
}

func ensureVlan(list []Vlan, id int) []Vlan {
	if findVlan(list, id) < 0 {
		return append(list, Vlan{VlanID: id})
	}
	return list
}

func ensureVlans(list []Vlan, ids []int) []Vlan {
	var id int
	for _, id = range ids {
		list = ensureVlan(list, id)
	}
	return list
}

// addTagged merges tagged VLANs. "all" absorbs everything: once a port lets
// everything through, adding identifiers no longer means anything.
func addTagged(cur any, ids []int) any {
	var asAll, isAll = cur.(string)
	if isAll && asAll == "all" {
		return "all"
	}
	var out = []int{}
	var prev, isList = cur.([]int)
	if isList {
		out = append(out, prev...)
	}
	var seen = map[int]bool{}
	var v int
	for _, v = range out {
		seen[v] = true
	}
	for _, v = range ids {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// deriveGroups reconstructs the physical front panel from interface names:
// "GigabitEthernet1/0/1 ... 1/0/24" becomes a group of 24 ports. The speed
// is deduced from the prefix, for lack of information in the configuration.
func deriveGroups(ports map[string]Port, poe map[string]bool) []PortGroup {
	type acc struct {
		min, max int
		poe      bool
	}
	var byPrefix = map[string]*acc{}
	var name string
	for name = range ports {
		var m = reIfaceIndex.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		var prefix = m[1]
		var idx, err = strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		var a = byPrefix[prefix]
		if a == nil {
			byPrefix[prefix] = &acc{min: idx, max: idx, poe: poe[name]}
			continue
		}
		if idx < a.min {
			a.min = idx
		}
		if idx > a.max {
			a.max = idx
		}
		if poe[name] {
			a.poe = true
		}
	}
	var out = []PortGroup{}
	var prefix string
	var a *acc
	for prefix, a = range byPrefix {
		out = append(out, PortGroup{
			Prefix: prefix, From: a.min, To: a.max,
			SpeedMbps: speedOf(prefix), PoE: a.poe,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// speedOf deduces a rate from the interface name. This is a vendor naming
// convention, not a measurement: the configuration doesn't carry the speed.
func speedOf(prefix string) int {
	var lower = strings.ToLower(prefix)
	switch {
	case strings.Contains(lower, "hundredgig"):
		return 100000
	case strings.Contains(lower, "fortygig"):
		return 40000
	case strings.Contains(lower, "twentyfivegig"):
		return 25000
	case strings.Contains(lower, "tengig") || strings.Contains(lower, "xge"):
		return 10000
	case strings.Contains(lower, "fastethernet"):
		return 100
	case strings.Contains(lower, "gigabit") || strings.HasPrefix(lower, "ge") ||
		strings.HasPrefix(lower, "gi"):
		return 1000
	default:
		return 1000
	}
}

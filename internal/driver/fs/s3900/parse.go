package s3900

// Parsers for FSOS 1.7 CLI output (captured in the lab, OpCode 1.7.0). The
// package's tests replay the EXACT outputs of the 2026-08-01 serial session:
// if the format changes after a firmware upgrade, this is where it breaks —
// and that's intended (the post-upgrade dialect is a different command set).

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// # Numbering: STANDARD in the model, SILKSCREEN on screen
//
// This connector's standard model numbers ports "GE1" ... "GE28" — the same
// scheme as the other numbering platforms (Instant On, SwOS), so the backend
// stays UNIFORM: profiles, diffs and telemetry speak the same vocabulary
// across the whole fleet. Translating to what the device actually understands
// is the connector's job ([cliPortName]: "GE11" -> "interface ethernet 1/11"),
// and the hardware's SILKSCREEN label ("1/11" printed on the faceplate) goes
// to the screen via the `silk` field on port groups — the operator sees what
// is in front of them, meshcraft manipulates a stable name.
//
// ASSUMED LIMIT: unit 1 only. The S3900 can stack; a unit-2 port
// ("ethernet 2/5") has no standard name here and is SKIPPED with a note rather
// than numbered arbitrarily — a wrong index writes to the wrong port, which
// cuts a client.

// rePortIdx accepts every spelling of a unit/port index this platform is known
// to produce, across the three sources that name a port: the CLI
// ("ethernet 1/11", "Eth 1/ 11"), SNMP ifDescr ("Ethernet1/11"), and LLDP
// local-port IDs — which on some agents carry the bare index ("1/11").
//
// The "eth" prefix is OPTIONAL for that last case, and deliberately still
// REQUIRED to be an ethernet-ish word when present: a bare "1/11" is an
// ethernet port here, but "Vlan1/11" or "Trunk1/11" must not be mistaken for
// one — StandardPortName's caller then writes to a port that does not exist.
var rePortIdx = regexp.MustCompile(`(?i)^\s*(?:eth(?:ernet)?\s*)?(\d+)\s*/\s*(\d+)\s*$`)

// rePortVerbose is the form the SNMP agent uses in `ifDescr`, and it looks
// nothing like the CLI's: "Ethernet Port on unit 1, port 11" (captured on
// OpCode 1.7.0, 2026-08-02).
//
// THIS IS THE ONE THAT MADE EVERY PORT READ AS DOWN. Telemetry attaches a
// sample to a port by matching `ifDescr` against the faceplate
// (api.facade.resolve), and no amount of case/punctuation normalization turns
// "Ethernet Port on unit 1, port 11" into "GE11" — nor did it into the older
// "ethernet 1/11". So every if_state and if_counters sample was silently
// skipped and every LLDP neighbor dropped, on a switch that was perfectly
// healthy. Only the connector can bridge these two vocabularies, which is
// precisely what driver.PortNamer is for.
//
// The device's other interfaces are deliberately NOT matched here — the same
// walk returns "Console port", "Loopback Interface 0" and "VLAN ID 0001" at
// indexes 363, 746 and 1001+. They are not faceplate ports, and naming one
// would invent an inventory port out of a measurement.
var rePortVerbose = regexp.MustCompile(
	`(?i)^\s*ethernet\s+port\s+on\s+unit\s+(\d+)\s*,\s*port\s+(\d+)\s*$`)

// standardPortName returns the standard-model name for a unit/port pair, ""
// outside coverage (unit != 1).
func standardPortName(unit, port int) string {
	if unit != 1 || port < 1 {
		return ""
	}
	return fmt.Sprintf("GE%d", port)
}

// StandardPortName (driver.PortNamer) translates a PLATFORM port name
// ("ethernet 1/11", "Eth 1/11") into the standard-model name. Used by
// configuration-import adoption to re-key intent under the same vocabulary
// the live connector reads and writes.
func (d *Driver) StandardPortName(deviceName string) (string, bool) {
	var m = rePortIdx.FindStringSubmatch(deviceName)
	if m == nil {
		m = rePortVerbose.FindStringSubmatch(deviceName)
	}
	if m == nil {
		return "", false
	}
	var unit, _ = strconv.Atoi(m[1])
	var port, _ = strconv.Atoi(m[2])
	var std = standardPortName(unit, port)
	return std, std != ""
}

// cliPortName translates a standard-model name into the form the 1.7 CLI
// expects ("GE11" -> "ethernet 1/11", per the show-run capture). Returns ""
// for a name outside the scheme — the caller refuses rather than writing
// blind.
func cliPortName(std string) string {
	var re = regexp.MustCompile(`^GE(\d+)$`)
	var m = re.FindStringSubmatch(std)
	if m == nil {
		return ""
	}
	return "ethernet 1/" + m[1]
}

// silkPrefix is the faceplate silkscreen ("1/11" printed on the hardware) —
// display only, carried in the `silk` field of port groups.
const silkPrefix = "1/"

// --- show version ---

// versionFacts: what "show version" establishes on OpCode 1.7.
type versionFacts struct {
	Serial   string
	OpCode   string // "1.7.0.x" — decides the dialect and the version note
	Loader   string
	Ports    int // "Number of Ports": port-count discovery
	Hardware string
}

var (
	reSerial = regexp.MustCompile(`(?im)^\s*Serial Number\s*:?\s*(\S+)`)
	reOpCode = regexp.MustCompile(`(?im)^\s*Operation Code Version\s*:?\s*(\S+)`)
	reLoader = regexp.MustCompile(`(?im)^\s*Loader Version\s*:?\s*(\S+)`)
	rePorts  = regexp.MustCompile(`(?im)^\s*Number of Ports\s*:?\s*(\d+)`)
	reHardw  = regexp.MustCompile(`(?im)^\s*Hardware Version\s*:?\s*(\S+)`)
)

func parseShowVersion(out string) versionFacts {
	var f versionFacts
	var m = reSerial.FindStringSubmatch(out)
	if m != nil {
		f.Serial = m[1]
	}
	if m = reOpCode.FindStringSubmatch(out); m != nil {
		f.OpCode = m[1]
	}
	if m = reLoader.FindStringSubmatch(out); m != nil {
		f.Loader = m[1]
	}
	if m = rePorts.FindStringSubmatch(out); m != nil {
		f.Ports, _ = strconv.Atoi(m[1])
	}
	if m = reHardw.FindStringSubmatch(out); m != nil {
		f.Hardware = m[1]
	}
	return f
}

// --- show interfaces brief ---

// briefPort is one row of the "show interfaces brief" table (lab capture).
type briefPort struct {
	Name      string // standard-model name "GE5"
	Num       int    // port number on the unit (5 for "Eth 1/5")
	Type      string // "1000BASE-T", "10GBASE SFP+"
	LinkUp    bool
	SpeedMbps int
}

// Captured in the lab:
//
//	Eth 1/ 1  1000BASE-T   Up      Down          Auto                       None
//	Eth 1/28  10GBASE SFP+ Up      Up            Disable       10Gfull      None
var reBriefLine = regexp.MustCompile(`(?im)^Eth\s+(\d+)\s*/\s*(\d+)\s+(\S+(?:\s+SFP\+?)?)\s+(Up|Down)\s+(Up|Down)\b`)

func parseInterfacesBrief(out string) []briefPort {
	var res []briefPort
	var rows = reBriefLine.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var m = rows[i]
		var unit, _ = strconv.Atoi(m[1])
		var num, _ = strconv.Atoi(m[2])
		var std = standardPortName(unit, num)
		if std == "" {
			continue // unit outside coverage: skipped, never numbered arbitrarily
		}
		var p = briefPort{
			Name:   std,
			Num:    num,
			Type:   strings.TrimSpace(m[3]),
			LinkUp: strings.EqualFold(m[5], "Up"),
		}
		p.SpeedMbps = speedOfType(p.Type)
		res = append(res, p)
	}
	return res
}

func speedOfType(t string) int {
	var lower = strings.ToLower(t)
	switch {
	case strings.Contains(lower, "10gbase"):
		return 10000
	case strings.Contains(lower, "1000base"):
		return 1000
	case strings.Contains(lower, "100base"):
		return 100
	default:
		return 1000
	}
}

// --- show running-config ---

// runConfig: what the current configuration establishes. This is THE
// authoritative reading for port modes: 1.7's "show vlan all" lists members
// without distinguishing tagged from untagged (capture: "Eth1/25(S)" in both
// cases), whereas the running-config's interface blocks carry the intent
// exactly.
type runConfig struct {
	Hostname string
	VLANs    []driver.VLAN
	Ports    map[string]driver.PortState
	// MgmtVLAN: the VLAN of the SVI that carries an address — this is how
	// administration comes in.
	MgmtVLAN int
	// Mgmt: the management addressing read (interface vlan N).
	Mgmt driver.MgmtAddrState
	// Communities: SNMP strings set, with their right (ro|rw).
	Communities map[string]string
	// TrapHosts: "snmp-server host" recipients (1.7 form never captured: kept
	// for a tolerant read).
	TrapHosts []string
}

var (
	reRunHostname = regexp.MustCompile(`(?im)^\s*hostname\s+"?([^\s"]+)"?`)
	// "VLAN 99 name LAN99_Management media ethernet" (vlan database block)
	reRunVlan = regexp.MustCompile(`(?i)^\s*vlan\s+(\d+)\s+name\s+(\S+)(?:\s+media\s+\S+)?`)
	// "interface ethernet 1/25" / "interface vlan 99" / "interface craft"
	reRunIface  = regexp.MustCompile(`(?i)^\s*interface\s+(ethernet\s+\d+/\d+|vlan\s+\d+|craft)\s*$`)
	reRunMode   = regexp.MustCompile(`(?i)^\s*switchport\s+mode\s+(access|trunk|hybrid)`)
	reRunTrkAl  = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+allowed\s+vlan\s+(?:add\s+)?([\d,\-]+)`)
	reRunTrkNa  = regexp.MustCompile(`(?i)^\s*switchport\s+(?:trunk\s+)?native\s+vlan\s+(\d+)`)
	reRunAccess = regexp.MustCompile(`(?i)^\s*switchport\s+access\s+vlan\s+(\d+)`)
	// Style hybride Accton : « switchport allowed vlan add 10 untagged »
	reRunAllowed = regexp.MustCompile(`(?i)^\s*switchport\s+allowed\s+vlan\s+add\s+([\d,\-]+)(?:\s+(tagged|untagged))?`)
	reRunIPAddr  = regexp.MustCompile(`(?i)^\s*ip\s+add(?:ress)?\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunIPCidr  = regexp.MustCompile(`(?i)^\s*ip\s+add(?:ress)?\s+(\d+\.\d+\.\d+\.\d+/\d+)`)
	reRunIPDHCP  = regexp.MustCompile(`(?i)^\s*ip\s+add(?:ress)?\s+dhcp`)
	reRunRoute   = regexp.MustCompile(`(?i)^\s*ip\s+route\s+0\.0\.0\.0\s+0\.0\.0\.0\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunDNS     = regexp.MustCompile(`(?i)^\s*dns\s+name-server\s+(\S+)`)
	reRunSNMP    = regexp.MustCompile(`(?i)^\s*snmp-server\s+community\s+(\S+)\s+(ro|rw)`)
	reRunTrapH   = regexp.MustCompile(`(?i)^\s*snmp-server\s+host\s+(\S+)`)
)

func parseRunningConfig(out string) runConfig {
	var rc = runConfig{
		Ports:       map[string]driver.PortState{},
		Communities: map[string]string{},
	}
	var curIface = "" // bloc d'interface ethernet courant (forme canonique)
	var curSVI = 0    // bloc « interface vlan N » courant
	var curCraft = false

	var line string
	for _, line = range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		var trimmed = strings.TrimSpace(line)
		if trimmed == "!" {
			curIface, curSVI, curCraft = "", 0, false
			continue
		}

		var m []string
		if m = reRunHostname.FindStringSubmatch(line); m != nil {
			rc.Hostname = m[1]
			continue
		}
		if m = reRunVlan.FindStringSubmatch(line); m != nil && curIface == "" {
			var id, _ = strconv.Atoi(m[1])
			if id >= 1 && id <= 4094 {
				rc.VLANs = append(rc.VLANs, driver.VLAN{ID: id, Name: m[2]})
			}
			continue
		}
		if m = reRunIface.FindStringSubmatch(line); m != nil {
			curIface, curSVI, curCraft = "", 0, false
			var what = strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
			switch {
			case strings.HasPrefix(what, "ethernet"):
				var std, ok = (&Driver{}).StandardPortName(what)
				if !ok {
					break // unit outside coverage (stacking): block skipped
				}
				curIface = std
				var _, seen = rc.Ports[curIface]
				if !seen {
					// Factory default: access on VLAN 1 (capture: empty blocks).
					rc.Ports[curIface] = driver.PortState{Mode: "access", PVID: 1}
				}
			case strings.HasPrefix(what, "vlan"):
				curSVI, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(what, "vlan")))
			case what == "craft":
				curCraft = true
			}
			continue
		}

		if curIface != "" {
			var p = rc.Ports[curIface]
			switch {
			case reRunMode.MatchString(line):
				var mode = strings.ToLower(reRunMode.FindStringSubmatch(line)[1])
				if mode == "hybrid" {
					mode = "trunk"
				}
				p.Mode = mode
			case reRunTrkAl.MatchString(line):
				p.Mode = "trunk"
				var ids, err = expandVlanList(reRunTrkAl.FindStringSubmatch(line)[1])
				if err == nil {
					p.Tagged = mergeSorted(p.Tagged, ids)
				}
			case reRunTrkNa.MatchString(line):
				p.PVID, _ = strconv.Atoi(reRunTrkNa.FindStringSubmatch(line)[1])
			case reRunAccess.MatchString(line):
				p.PVID, _ = strconv.Atoi(reRunAccess.FindStringSubmatch(line)[1])
				if p.Mode == "" {
					p.Mode = "access"
				}
			case reRunAllowed.MatchString(line):
				var mm = reRunAllowed.FindStringSubmatch(line)
				var ids, err = expandVlanList(mm[1])
				if err == nil {
					if strings.EqualFold(mm[2], "untagged") {
						if len(ids) == 1 {
							p.PVID = ids[0]
						}
					} else {
						p.Mode = "trunk"
						p.Tagged = mergeSorted(p.Tagged, ids)
					}
				}
			}
			if p.PVID == 0 {
				p.PVID = 1
			}
			rc.Ports[curIface] = p
			continue
		}

		if curSVI > 0 {
			if m = reRunIPCidr.FindStringSubmatch(line); m != nil {
				rc.MgmtVLAN = curSVI
				rc.Mgmt.Mode = driver.MgmtAddrModeStatic
				rc.Mgmt.Address = m[1]
				rc.Mgmt.Interface = fmt.Sprintf("vlan%d", curSVI)
			} else if m = reRunIPAddr.FindStringSubmatch(line); m != nil {
				rc.MgmtVLAN = curSVI
				rc.Mgmt.Mode = driver.MgmtAddrModeStatic
				rc.Mgmt.Address = fmt.Sprintf("%s/%d", m[1], maskBits(m[2]))
				rc.Mgmt.Interface = fmt.Sprintf("vlan%d", curSVI)
			} else if reRunIPDHCP.MatchString(line) {
				// A DHCP SVI never supersedes a static one already seen: the
				// static one is the one meshcraft set.
				if rc.MgmtVLAN == 0 {
					rc.MgmtVLAN = curSVI
					rc.Mgmt.Mode = driver.MgmtAddrModeDHCP
					rc.Mgmt.Interface = fmt.Sprintf("vlan%d", curSVI)
				}
			}
			continue
		}
		if curCraft {
			continue // out-of-band management port: not the network management
		}

		if m = reRunRoute.FindStringSubmatch(line); m != nil {
			if rc.Mgmt.Gateway == "" {
				rc.Mgmt.Gateway = m[1]
			}
			continue
		}
		if m = reRunDNS.FindStringSubmatch(line); m != nil {
			rc.Mgmt.DNS = append(rc.Mgmt.DNS, m[1])
			continue
		}
		if m = reRunSNMP.FindStringSubmatch(line); m != nil {
			rc.Communities[m[1]] = strings.ToLower(m[2])
			continue
		}
		if m = reRunTrapH.FindStringSubmatch(line); m != nil {
			rc.TrapHosts = append(rc.TrapHosts, m[1])
			continue
		}
	}

	sort.Slice(rc.VLANs, func(i, j int) bool { return rc.VLANs[i].ID < rc.VLANs[j].ID })
	return rc
}

// expandVlanList accepte « 99 », « 99,100 », « 10-12 ».
func expandVlanList(spec string) ([]int, error) {
	var out []int
	var seen = map[int]bool{}
	var tok string
	for _, tok = range strings.Split(spec, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if strings.Contains(tok, "-") {
			var parts = strings.SplitN(tok, "-", 2)
			var lo, errLo = strconv.Atoi(strings.TrimSpace(parts[0]))
			var hi, errHi = strconv.Atoi(strings.TrimSpace(parts[1]))
			if errLo != nil || errHi != nil || lo < 1 || hi > 4094 || hi < lo {
				return nil, fmt.Errorf("intervalle invalide %q", tok)
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
			return nil, fmt.Errorf("VLAN invalide %q", tok)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("liste vide")
	}
	sort.Ints(out)
	return out, nil
}

// mergeSorted merges two VLAN lists, SORTED — the PortState contract
// requires it so two reads produce the same state.
func mergeSorted(cur, add []int) []int {
	var seen = map[int]bool{}
	var out []int
	var v int
	for _, v = range cur {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	for _, v = range add {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func maskBits(mask string) int {
	var parts = strings.Split(mask, ".")
	if len(parts) != 4 {
		return 24
	}
	var bits int
	var p string
	for _, p = range parts {
		var b, err = strconv.Atoi(p)
		if err != nil {
			return 24
		}
		for b > 0 {
			bits += b & 1
			b >>= 1
		}
	}
	return bits
}

// --- Firmware dialect: which CLI forms THIS revision accepts ---

// bareVlanCreateFrom: the OpCode revision from which "vlan N name X" is the
// accepted form and "media ethernet" is REJECTED.
//
// 1.7.7 is where it was CAPTURED (lab S3900-24T4S, 2026-08-02: the caret points
// at "media"). 1.7.0 is where the opposite was captured — the keyword required.
// The exact revision in between that flipped it is unknown, and this constant
// does not pretend otherwise: it is the boundary meshcraft has evidence for, and
// the fallback form covers being wrong about anything below it.
const bareVlanCreateFrom = "1.7.7"

// wantsBareVlanCreate decides which VLAN-creation form to TRY FIRST.
//
// A wrong answer here is not a failure: [vlanCreateCandidates] carries both
// forms and the other one is tried immediately. That is deliberate — a version
// comparison on a vendor's revision string is exactly the kind of thing that
// meets a format nobody predicted, and the cost of guessing wrong must stay "one
// extra command", never "the VLAN was not created".
//
// An unparseable or absent version answers TRUE — the newer form. A device whose
// version meshcraft cannot read is more likely to be newer than the 1.7.0 the
// original survey was made on, and the fallback covers the rest.
func wantsBareVlanCreate(f versionFacts) bool {
	if !parsableOpCode(f.OpCode) {
		return true
	}
	return compareOpCode(f.OpCode, bareVlanCreateFrom) >= 0
}

// parsableOpCode reports whether a revision string carries a leading number at
// all. Without this, a string like "wat" compares as 0 and therefore as OLDER
// than every real revision — quietly routing an unrecognisable device onto the
// 1.7.0 form, which is the opposite of what an unknown version should do.
func parsableOpCode(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && s[0] >= '0' && s[0] <= '9'
}

// compareOpCode compares two dotted revision strings numerically, field by
// field: -1, 0 or +1.
//
// NUMERIC, not lexicographic, and that is the whole reason this exists rather
// than a string compare: "1.7.10" is NEWER than "1.7.7" and sorts BEFORE it as
// text. A fleet that reaches double-digit revisions would otherwise silently
// fall back to the old form on its newest switches.
//
// Non-numeric trailing junk ("1.7.7-rc2", "1.7.7b") is tolerated by comparing
// the leading digits of each field: a vendor decorating its version string must
// not make the comparison meaningless.
func compareOpCode(a, b string) int {
	var fa = strings.Split(a, ".")
	var fb = strings.Split(b, ".")
	var n = len(fa)
	if len(fb) > n {
		n = len(fb)
	}
	var i int
	for i = 0; i < n; i++ {
		var va, vb int
		if i < len(fa) {
			va = leadingInt(fa[i])
		}
		if i < len(fb) {
			vb = leadingInt(fb[i])
		}
		if va != vb {
			if va < vb {
				return -1
			}
			return 1
		}
	}
	return 0
}

// leadingInt reads the digits at the start of a field and ignores the rest.
func leadingInt(s string) int {
	var i int
	for i = 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
	}
	if i == 0 {
		return 0
	}
	var n, err = strconv.Atoi(s[:i])
	if err != nil {
		return 0
	}
	return n
}

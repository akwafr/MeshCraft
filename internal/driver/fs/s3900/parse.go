package s3900

// Parsers for FSOS 1.7 CLI output (captured in the lab, OpCode 1.7.0). The
// package's tests replay the EXACT outputs of the 2026-08-01 serial session:
// if the format changes after a firmware upgrade, this is where it breaks —
// and that's intended (the post-upgrade dialect is a different command set).

import (
	"fmt"
	"net"
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
	Name string // standard-model name "GE5"
	Num  int    // port number on the unit (5 for "Eth 1/5")
	Type string // "1000BASE-T", "10GBASE SFP+"
	// AdminUp: the "Admin" column — whether the port is administratively
	// enabled. DISTINCT from LinkUp, and the distinction is the whole read side
	// of [Client.ApplyPortAdmin]: a port an operator shut reads "Down Down"
	// here, a port with nothing plugged in reads "Up Down", and confusing the
	// two would let the connector believe it had already shut a port that is
	// merely unused — and therefore skip the write that was asked for.
	//
	// Both rows CAPTURED 2026-08-10, side by side, on the lab S3900-24T4S with
	// ethernet 1/1 shut (portadmin_live_test.go). Before that only the "Up Down"
	// half had ever been seen.
	AdminUp   bool
	LinkUp    bool
	SpeedMbps int
}

// Captured in the lab, header included (labBrief in parse_test.go, and the
// admin-down row from 2026-08-10 in portadmin_live_test.go):
//
//	Interface Type         Admin  Link-Status  Negotiation  Speed/Duplex   Group
//	Eth 1/ 1  1000BASE-T   Up      Down          Auto                       None
//	Eth 1/ 1  1000BASE-T   Down    Down          Auto                       None
//	Eth 1/28  10GBASE SFP+ Up      Up            Disable       10Gfull      None
//
// The two Up/Down columns are Admin then Link-Status, IN THAT ORDER — the
// header is what establishes it, and it is the reason this regexp keeps both
// rather than matching the first one it finds. Lines 1 and 2 are the same dark
// port a "shutdown" apart: the Admin column is all that separates them.
//
// AND THE PORT TOKEN CONTAINS A SPACE — "Eth 1/ 1", the number right-aligned
// in two characters — so the row's field count changes with the port number and
// with the type column's own space ("10GBASE SFP+"). That is why the unit and the
// port are read as two numbers around the slash rather than by splitting on
// whitespace at a fixed index.
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
			Name:    std,
			Num:     num,
			Type:    strings.TrimSpace(m[3]),
			AdminUp: strings.EqualFold(m[4], "Up"),
			LinkUp:  strings.EqualFold(m[5], "Up"),
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
	// MgmtVLAN: the VLAN of the SVI THE SESSION IS RIDING — the one whose address
	// meshcraft dialed — and 0 when that cannot be established.
	//
	// ZERO IS A VALUE here, and the two readers treat it as one: ApplyPorts
	// protects no VLAN of its own (apply.go), and ApplyPortAdmin spares every port
	// that has a LINK rather than refusing the gesture (portadmin.go's header says
	// why it must not refuse). That is the honest outcome of an ambiguous
	// configuration, and the one [resolveMgmtSVI] returns rather than guessing.
	//
	// IT USED TO MEAN "the last addressed SVI in the text", which is not the same
	// sentence at all — see [resolveMgmtSVI] for the switch that proved it.
	MgmtVLAN int
	// Mgmt: the management addressing read (interface vlan N).
	Mgmt driver.MgmtAddrState
	// MgmtSVINotes: how the management SVI was chosen, or why it could not be.
	//
	// Said OUT LOUD because the choice is no longer mechanical: a device with
	// several addressed SVIs has an answer that depends on which address meshcraft
	// dialed, and an operator reading "management VLAN unknown" on a switch with
	// four SVIs needs to be told that is what happened, not left to guess that the
	// read failed. Reaches the operator three ways — [Client.ReadState] puts it in
	// State.Notes, [runConfig.Mgmt].Notes carries it into ReadMgmtAddressing, and
	// [Client.ApplyPortAdmin] prints it beside the guardrail it just widened.
	MgmtSVINotes []string
	// MgmtOOB: the address on "interface craft", the OUT-OF-BAND port.
	//
	// NEVER a management candidate — it sits outside the switching fabric and this
	// connector neither reads nor writes administration through it, which is why
	// the craft block is skipped by the parse. It is kept for ONE purpose: when no
	// SVI can be named, "but craft carries 192.168.1.1" turns "meshcraft sees no
	// management path" from an apparent read failure into a description of the
	// switch. The analogue of Management1 on arista/eos.
	MgmtOOB string
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
	// Accton hybrid style: "switchport allowed vlan add 10 untagged"
	reRunAllowed = regexp.MustCompile(`(?i)^\s*switchport\s+allowed\s+vlan\s+add\s+([\d,\-]+)(?:\s+(tagged|untagged))?`)
	reRunIPAddr  = regexp.MustCompile(`(?i)^\s*ip\s+add(?:ress)?\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunIPCidr  = regexp.MustCompile(`(?i)^\s*ip\s+add(?:ress)?\s+(\d+\.\d+\.\d+\.\d+/\d+)`)
	reRunIPDHCP  = regexp.MustCompile(`(?i)^\s*ip\s+add(?:ress)?\s+dhcp`)
	reRunRoute   = regexp.MustCompile(`(?i)^\s*ip\s+route\s+0\.0\.0\.0\s+0\.0\.0\.0\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunDNS     = regexp.MustCompile(`(?i)^\s*dns\s+name-server\s+(\S+)`)
	reRunSNMP    = regexp.MustCompile(`(?i)^\s*snmp-server\s+community\s+(\S+)\s+(ro|rw)`)
	reRunTrapH   = regexp.MustCompile(`(?i)^\s*snmp-server\s+host\s+(\S+)`)
)

// sviAddr is one "ip add" line of an SVI block, kept whole AND parsed.
//
// Both halves are needed: Text is what gets reported (already normalised to
// a.b.c.d/nn by the caller, whichever of the two spellings the device used),
// while IP and Net are what the management SVI is CHOSEN by.
type sviAddr struct {
	Text string
	IP   net.IP
	Net  *net.IPNet
}

// parseSVIAddr keeps the address exactly as the caller normalised it and adds
// what comparing it requires. A text that will not parse back loses only the
// comparison — it is still reported, because what the device says is never
// dropped in favour of what this code can handle.
func parseSVIAddr(text string) sviAddr {
	var a = sviAddr{Text: text}
	var ip, nw, err = net.ParseCIDR(text)
	if err == nil {
		a.IP, a.Net = ip, nw
		return a
	}
	a.IP = net.ParseIP(text)
	return a
}

// sviCandidate is one "interface vlan N" block that could be carrying
// administration: declaring an address, or a DHCP lease.
//
// # KNOWN GAP: THERE IS NO "shutdown" FILTER HERE, AND THAT IS NOT AN OVERSIGHT
//
// A shut SVI carrying an address answers nothing — it is not a management path,
// and claiming it is sends every probe to a dead address. arista/eos filters
// exactly that, per VLAN, so the POSITION of the "shutdown" line cannot decide.
// This parser does not, for one reason: no capture in this repository shows an
// "interface vlan N" block of FSOS 1.7 with the interface shut, so the
// spelling is unknown. FSOS writes "shutdown" on a port block, but whether an
// SVI block prints that word, "no enable", or something else entirely has never
// been seen on this platform.
//
// A regexp written on a form nobody has observed is WORSE than its absence: it
// gives the illusion of a guardrail, and it fails silently on the day the real
// word turns out to be a different one. So the gap stays open and stays
// documented. THE FIX IS A CAPTURE, not a guess: shut an SVI on a lab S3900,
// take `show run`, and the filter belongs in the single loop that builds the
// candidate list at the end of [parseRunningConfig] — per VLAN, never as the
// address line goes by.
//
// Until then a shut-but-addressed SVI is a contender, which can only mislead at
// level 3 or 4 of [resolveMgmtSVI]: at level 1 or 2 the address meshcraft dialed
// is the address it is talking to right now, so the SVI carrying it is up by
// observation whatever the configuration prints.
type sviCandidate struct {
	VLAN int
	// Mode: static or dhcp (driver.MgmtAddrMode*).
	Mode string
	// Addrs is in configuration order, and empty under DHCP — the running
	// configuration carries the intent, never the lease.
	Addrs []sviAddr
	// Notes: what this block's own parsing could not express, promoted to
	// [runConfig.Mgmt].Notes only if this SVI is the one that wins.
	Notes []string
}

// primary is the address to report for this SVI when no comparison singled one
// out: the FIRST the block declares.
func (s sviCandidate) primary() string {
	if len(s.Addrs) == 0 {
		return ""
	}
	return s.Addrs[0].Text
}

// resolveMgmtSVI picks the SVI the administration session is actually riding, by
// COMPARING each candidate to the address meshcraft dialed — never by order of
// appearance.
//
// # The switch that proved the old rule wrong
//
// Until this function existed, every SVI carrying an address simply overwrote the
// management fields, so THE LAST BLOCK OF THE RUNNING CONFIG WON. Measured on an
// Arista core (core01-arena, DCS-7050SX-64-R, EOS 4.28.13M) whose parser had the
// line-for-line same shape: six addressed SVIs, printed Vlan42, Vlan97, Vlan98,
// Vlan99, Vlan100, Vlan3000 — and Vlan3000 is a /31 point-to-point transit link
// to a MikroTik. meshcraft filed the switch's management VLAN as 3000 and its
// address as 10.255.255.1/31, while the address it had itself dialed to read all
// of that was 192.0.2.1/16 on Vlan99. It then reported the switch as having no
// usable management path and refused every addressing gesture: prudent BY
// ACCIDENT, on a premise that was false — and on a switch whose transit SVI
// happens to look plausible the same mistake AUTHORISES a write instead.
//
// The read was never compared to anything. That is the whole of the bug: a field
// whose definition was "the VLAN of the SVI that carries an address" describes
// every addressed SVI and names none of them.
//
// # The order of the tests, and why it stops rather than guesses
//
//  1. THE SVI THAT CARRIES THE DIALED ADDRESS. The session arrived somewhere;
//     that somewhere is written in the configuration. Exact, order-independent,
//     and the case every correctly-read switch lands in.
//  2. THE SVI WHOSE SUBNET CONTAINS IT, longest prefix first — the way a routing
//     table resolves the same question. Not padding: the address meshcraft dials
//     in front of a redundant pair is routinely a virtual one (VRRP) that no SVI
//     carries while exactly one subnet contains it.
//  3. A STATIC SVI BEATS A DHCP ONE. See the narrowing below: this is a RULE of
//     this platform, not a consequence of which block prints first.
//  4. THE ONLY CANDIDATE LEFT, when the configuration offers one — the shape of
//     most of the fleet. Said out loud when the dialed address is not the one it
//     carries, because administration is then arriving by another door (the craft
//     port, the console, an address translation) and the SVI is a deduction
//     rather than an observation.
//  5. OTHERWISE, UNKNOWN. Falling back to "the last one" is the bug; falling back
//     to "the first one" is the same bug reading the file backwards. Both readers
//     of [runConfig.MgmtVLAN] treat 0 as "not known" and fail safe on it, so the
//     honest answer is also the harmless one.
//
// Returns the winning candidate, the address to report for it, the sentences the
// operator needs, and whether anything was chosen at all.
func resolveMgmtSVI(cands []sviCandidate, dialed string) (sviCandidate, string, []string, bool) {
	var none sviCandidate
	if len(cands) == 0 {
		// Nothing to choose between, and nothing to say: ReadMgmtAddressing
		// already writes the "no SVI carries an address" sentence, with the craft
		// explanation when there is one.
		return none, "", nil, false
	}
	var want = net.ParseIP(strings.TrimSpace(dialed))

	// 1 — the SVI that CARRIES the dialed address.
	if want != nil {
		var hits []int
		var hitAddr []string
		var i int
		for i = range cands {
			var a sviAddr
			for _, a = range cands[i].Addrs {
				if a.IP != nil && a.IP.Equal(want) {
					hits = append(hits, i)
					hitAddr = append(hitAddr, a.Text)
					break
				}
			}
		}
		if len(hits) == 1 {
			return cands[hits[0]], hitAddr[0], nil, true
		}
		if len(hits) > 1 {
			// Two SVIs cannot legally hold the same address; a configuration that
			// says they do is reported, and resolved by the LOWEST VLAN so two
			// reads of the same device never disagree.
			var best = 0
			var k int
			for k = 1; k < len(hits); k++ {
				if cands[hits[k]].VLAN < cands[hits[best]].VLAN {
					best = k
				}
			}
			return cands[hits[best]], hitAddr[best], []string{fmt.Sprintf(
				"the address meshcraft reaches this switch at (%s) is declared on %s: that "+
					"cannot be, and vlan %d — the lowest — is the one taken, so two reads of "+
					"this device agree with each other. The duplicate belongs on one SVI only",
				want, sviList(sviSubset(cands, hits)), cands[hits[best]].VLAN)}, true
		}
	}

	// 2 — the SVI whose SUBNET contains it, most specific first.
	if want != nil {
		var best = -1
		var bestBits = -1
		var tied bool
		var i int
		for i = range cands {
			var a sviAddr
			for _, a = range cands[i].Addrs {
				if a.Net == nil || !a.Net.Contains(want) {
					continue
				}
				var bits, _ = a.Net.Mask.Size()
				if bits > bestBits {
					best, bestBits, tied = i, bits, false
				} else if bits == bestBits && i != best {
					tied = true
				}
			}
		}
		if best >= 0 && !tied {
			return cands[best], cands[best].primary(), []string{fmt.Sprintf(
				"no SVI carries %s, the address meshcraft reaches this switch at, but vlan %d's "+
					"subnet (%s) contains it — a virtual address (VRRP) in front of this SVI "+
					"is the ordinary reason. Administration is read as arriving on vlan %d",
				want, cands[best].VLAN, cands[best].primary(), cands[best].VLAN)}, true
		}
	}

	// 3 — THE NARROWING: A STATIC SVI BEATS A DHCP ONE, as a rule and not as a
	// side effect of which block prints first.
	//
	// It was the second until 2026-10-25: the DHCP branch of the parse wrote the
	// management fields only "if rc.MgmtVLAN == 0", so a static block already
	// read held them. That worked, and it worked for a reason nobody had written
	// down — which is exactly the failure mode this whole function exists to
	// remove. Stated here instead, because it is a truth about the PLATFORM:
	//
	//   - a static address on an S3900 is one meshcraft itself wrote (cf.
	//     MgmtPushCmds). "ip add dhcp" on vlan 1 is what the switch left the
	//     factory with, and FSOS keeps printing that line long after the
	//     provisioned SVI took over. The lab switch has exactly that shape — vlan 1
	//     on DHCP, vlan 99 static — and it is the ordinary shape, not an ambiguity;
	//   - a DHCP candidate carries NO address in the running configuration (the
	//     lease is not printed), so it can never win level 1 or level 2 by
	//     comparison. This rule therefore costs nothing where evidence exists: it
	//     only ever decides the cases evidence could not.
	//
	// Note it does NOT fire when every candidate is DHCP — an unprovisioned switch
	// addressed by lease is read as what it is.
	var statics []sviCandidate
	var c sviCandidate
	for _, c = range cands {
		if c.Mode == driver.MgmtAddrModeStatic {
			statics = append(statics, c)
		}
	}
	var short = cands
	if len(statics) > 0 {
		short = statics
	}

	// 4 — the only candidate left.
	if len(short) == 1 {
		var notes []string
		if want != nil && len(short[0].Addrs) > 0 {
			notes = []string{fmt.Sprintf(
				"vlan %d is the only SVI that could carry administration, so it is the one "+
					"reported — but it carries %s while meshcraft reaches this switch at %s. "+
					"Administration is arriving by some other door (the out-of-band craft "+
					"port, the console, or an address translation), and this VLAN is a "+
					"deduction rather than something read",
				short[0].VLAN, short[0].primary(), want)}
		}
		return short[0], short[0].primary(), notes, true
	}

	// 5 — several candidates and nothing to tell them apart.
	var why = "the address meshcraft reaches this switch at is not known to this read"
	if want != nil {
		why = fmt.Sprintf("none carries %s, the address meshcraft reaches this switch at, "+
			"and no single subnet contains it", want)
	}
	return none, "", []string{fmt.Sprintf(
		"MANAGEMENT VLAN NOT DETERMINED: %s carry an address and %s. Reporting the last "+
			"one read would be an invention — a point-to-point transit SVI is routinely the "+
			"last block of a running configuration — so it is left unknown, and every "+
			"guardrail that depends on it spares what it cannot vouch for",
		sviList(cands), why)}, false
}

// sviSubset selects candidates by index, for a sentence that has to name them.
func sviSubset(cands []sviCandidate, idx []int) []sviCandidate {
	var out []sviCandidate
	var i int
	for _, i = range idx {
		out = append(out, cands[i])
	}
	return out
}

// sviList spells a set of SVIs the way an operator reads them back off this CLI:
// "vlan 99 (192.0.2.240/16), vlan 3000 (10.255.255.1/31)".
func sviList(cands []sviCandidate) string {
	var parts []string
	var c sviCandidate
	for _, c = range cands {
		var addr = c.primary()
		if addr == "" {
			addr = "DHCP"
		}
		parts = append(parts, fmt.Sprintf("vlan %d (%s)", c.VLAN, addr))
	}
	return strings.Join(parts, ", ")
}

// parseRunningConfig reads "show run".
//
// dialed is the address the session arrived on ([Client.host]), and it is a
// PARAMETER rather than something deduced here because it is the only evidence
// that says which SVI carries administration — cf. [resolveMgmtSVI]. Empty is
// allowed and means "not known to this caller": it costs the comparison, never
// the read, and on the ordinary single-SVI switch it costs nothing at all.
func parseRunningConfig(out string, dialed string) runConfig {
	var rc = runConfig{
		Ports:       map[string]driver.PortState{},
		Communities: map[string]string{},
	}
	// Every SVI block is COLLECTED and the management one chosen afterwards, by
	// [resolveMgmtSVI]. Nothing about the choice may depend on the order the blocks
	// arrive in — that dependency IS the bug this shape replaces.
	var sviCands = map[int]*sviCandidate{}
	var sviOrder []int
	var sviOf = func(vlan int) *sviCandidate {
		var c = sviCands[vlan]
		if c == nil {
			c = &sviCandidate{VLAN: vlan}
			sviCands[vlan] = c
			sviOrder = append(sviOrder, vlan)
		}
		return c
	}
	var curIface = "" // current ethernet interface block (canonical form)
	var curSVI = 0    // current "interface vlan N" block
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
			// COLLECTED, never assigned: which of these blocks is the management
			// one is decided once, below, by [resolveMgmtSVI]. Both spellings
			// normalise to a.b.c.d/nn here so the comparison has one form to read.
			if m = reRunIPCidr.FindStringSubmatch(line); m != nil {
				var c = sviOf(curSVI)
				c.Mode = driver.MgmtAddrModeStatic
				c.Addrs = append(c.Addrs, parseSVIAddr(m[1]))
			} else if m = reRunIPAddr.FindStringSubmatch(line); m != nil {
				var c = sviOf(curSVI)
				c.Mode = driver.MgmtAddrModeStatic
				c.Addrs = append(c.Addrs, parseSVIAddr(fmt.Sprintf("%s/%d", m[1], maskBits(m[2]))))
			} else if reRunIPDHCP.MatchString(line) {
				var c = sviOf(curSVI)
				// A static address in the same block always wins the MODE: it is
				// the one meshcraft wrote, and "ip add dhcp" left behind it is
				// the line that is inert. Between blocks the same rule is the
				// narrowing in [resolveMgmtSVI].
				if c.Mode == "" {
					c.Mode = driver.MgmtAddrModeDHCP
				}
			}
			continue
		}
		if curCraft {
			// OUT-OF-BAND management port: never a management candidate (cf.
			// runConfig.MgmtOOB). Its address is kept only so that "no SVI could
			// be named" can say what the switch does have.
			if m = reRunIPCidr.FindStringSubmatch(line); m != nil {
				rc.MgmtOOB = m[1]
			} else if m = reRunIPAddr.FindStringSubmatch(line); m != nil {
				rc.MgmtOOB = fmt.Sprintf("%s/%d", m[1], maskBits(m[2]))
			}
			continue
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

	// The management SVI is chosen HERE, once the whole configuration is in hand
	// and every candidate is known — never while the blocks go by, where "the one
	// in front of me" is indistinguishable from "the right one".
	//
	// THE "shutdown" FILTER BELONGS IN THIS LOOP and is not written: the word
	// FSOS 1.7 prints inside an "interface vlan N" block to disable it has never
	// been captured, and guessing it would put an unverifiable regexp where a
	// guardrail is supposed to be. Cf. [sviCandidate] for the whole reasoning and
	// for what the capture must show.
	var cands []sviCandidate
	var vlan int
	for _, vlan = range sviOrder {
		cands = append(cands, *sviCands[vlan])
	}
	var won, wonAddr, sviNotes, ok = resolveMgmtSVI(cands, dialed)
	rc.MgmtSVINotes = sviNotes
	if ok {
		rc.MgmtVLAN = won.VLAN
		rc.Mgmt.Mode = won.Mode
		rc.Mgmt.Address = wonAddr
		rc.Mgmt.Interface = fmt.Sprintf("vlan%d", won.VLAN)
		rc.Mgmt.Notes = append(rc.Mgmt.Notes, won.Notes...)
	}
	// The same sentences reach the operator on three screens that do not show each
	// other: the state read (ReadState), ReadMgmtAddressing, and the port-admin
	// transcript.
	rc.Mgmt.Notes = append(rc.Mgmt.Notes, sviNotes...)

	sort.Slice(rc.VLANs, func(i, j int) bool { return rc.VLANs[i].ID < rc.VLANs[j].ID })
	return rc
}

// expandVlanList accepte "99", "99,100", "10-12".
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
			return nil, fmt.Errorf("invalid VLAN %q", tok)
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

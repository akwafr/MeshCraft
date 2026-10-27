package s3400

// Parsers for the S3400 CLI, written against outputs CAPTURED on a
// S3400-48T4SP running Version 2.0.2J Build 95262 (lab session 2026-08-03).
// Every sample reproduced in the comments below is verbatim from that session,
// and parse_test.go replays them.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// --- port naming -------------------------------------------------------
//
// This platform utters FIVE spellings for the same socket, all captured:
//
//	GigaEthernet0/12   running-config, "show interface" headers
//	g0/12              "show vlan", "show spanning-tree", config prompts
//	Gi0/12             LLDP Local-Intf (short vendor form)
//	TGigaEthernet0/2   the SFP+ cages, running-config
//	tg0/2 / TGi0/2     the same cages, "show vlan" / LLDP
//
// The standard model keeps ONE: GE<n> for the 48 copper ports, XG<n> for the
// four SFP+ cages — the same two-prefix shape the Catalyst connector uses,
// because a 48T4SP also carries "port 1" twice on two different sockets.

var rePortName = regexp.MustCompile(`(?i)^\s*(tgigaethernet|tgiga|tgi|tg|gigaethernet|gigaether|giga|gi|g)\s*(\d+)/(\d+)\s*$`)

// StandardPortName maps a name observed on the platform to the standard model.
//
// It refuses anything that is not a physical faceplate socket — VLAN
// interfaces, Null0, aggregations — because naming one of those invents an
// inventory port out of a measurement (cf. portnamer_consistency_test.go).
// It also refuses names that are ALREADY standard: "GE12" is not a spelling
// this platform utters, and answering something else for it would make the
// faceplate catch-up fire on every read.
func (d *Driver) StandardPortName(deviceName string) (string, bool) {
	var m = rePortName.FindStringSubmatch(deviceName)
	if m == nil {
		return "", false
	}
	var kind = strings.ToLower(m[1])
	var slot, serr = strconv.Atoi(m[2])
	var port, perr = strconv.Atoi(m[3])
	if serr != nil || perr != nil || port < 1 {
		return "", false
	}
	// Slot is always 0 on this range (the expansion slot number of a fixed
	// unit); a non-zero slot is not something this connector has seen, and
	// guessing a number for it would be inventing inventory.
	if slot != 0 {
		return "", false
	}
	if strings.HasPrefix(kind, "tg") {
		var cage, cok = sfpCageNumber(port)
		if !cok {
			return "", false
		}
		return fmt.Sprintf("XG%d", cage), true
	}
	if port > copperPorts {
		return "", false
	}
	return fmt.Sprintf("GE%d", port), true
}

const (
	// The 48T4SP faceplate: 48 copper + 4 SFP+ cages (captured — "show vlan"
	// lists g0/1..g0/48 and tg0/1..tg0/4, and "show interface" confirms
	// unique port numbers 1..48 then 49..52).
	copperPorts = 48
	sfpPorts    = 4
)

// sfpCageNumber maps the cage number a BUILD utters onto the 1..sfpPorts the
// standard model keeps. TWO numbering conventions are fielded on the same
// model, both captured:
//
//	tg0/1 .. tg0/4     Build 95262  — the cages restart their own series
//	tg0/49 .. tg0/52   Build 130907 — the cages continue the copper series
//
// The NAME is identical in both ("TGigaEthernet0/N"); only the N moves. The
// second convention cost sw01-arena its four 10G uplinks: StandardPortName
// refused anything above 4 and did it silently, so a 52-socket switch was
// adopted with 48 ports and the two cages actually carrying traffic simply
// did not exist in the model.
//
// Anything outside both windows stays refused — a "tg0/5" is not a socket
// this faceplate has under either convention, and naming it would invent
// inventory out of a typo.
func sfpCageNumber(port int) (int, bool) {
	if port >= 1 && port <= sfpPorts {
		return port, true
	}
	if port > copperPorts && port <= copperPorts+sfpPorts {
		return port - copperPorts, true
	}
	return 0, false
}

// cageNumbering is which of the two SFP+ conventions ONE SESSION observed on
// the device in front of it — see ADR-0018.
//
// [sfpCageNumber] made the READ accept both. The write side cannot use the same
// trick: it has to pick one number, and [Driver.StandardPortName] is a method of
// [Driver], which is ONE object for the whole fleet and carries no session. So
// the fact lives here, is filled by [Client.noteCageNumbering] from output the
// connector already pays for, and is carried on the [Client] — one open SSH
// session to one host, which is exactly the scope of the fact.
//
// `known` is a field and not an `base == 0` test on purpose: zero IS a
// convention (the cages restart their own series), so "not observed yet" must
// be representable apart from it.
type cageNumbering struct {
	// base is the device's own cage number MINUS the standard one: 0 when the
	// cages restart at 1 (Build 95262), copperPorts when they continue the
	// copper series (Build 130907).
	base int
	// known: an output of THIS session actually said so.
	known bool
}

// reDevCage matches the device's own spelling of an SFP+ cage anywhere in an
// output — "TGigaEthernet0/51", "tg0/3", "TGi0/52" — which is how a
// numbering convention is OBSERVED rather than assumed. Deliberately not
// anchored, unlike rePortName: this reads prose, not a single field.
var reDevCage = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:tgigaethernet|tgiga|tgi|tg)\s*0/(\d+)`)

// observeCageNumbering reads the convention out of one device output. PURE.
//
// The window comes from [sfpCageNumber] and not from a second copy of the two
// ranges: one definition of "a cage number this faceplate can have", used by
// the read and by this. Widen that one and both sides follow.
//
// A sighting VOTES, and a contradiction elects nobody: an output that names
// both a tg0/2 and a tg0/50 is not an output to deduce a numbering from, so it
// answers "unknown" exactly as an output naming no cage at all does. Guessing
// from half of it is how a write lands on a socket nobody asked for.
func observeCageNumbering(out string) cageNumbering {
	var bases = map[int]bool{}
	var m = reDevCage.FindAllStringSubmatch(out, -1)
	var i int
	for i = range m {
		var n, err = strconv.Atoi(m[i][1])
		if err != nil {
			continue
		}
		var cage, ok = sfpCageNumber(n)
		if !ok {
			continue
		}
		bases[n-cage] = true
	}
	if len(bases) != 1 {
		return cageNumbering{}
	}
	var base int
	for base = range bases {
	}
	return cageNumbering{base: base, known: true}
}

// cliPortName turns a standard name back into the name THIS DEVICE answers to.
// "interface g0/12" and "interface tg0/2" were both captured accepted.
//
// THE COPPER HALF NEEDS NO OBSERVATION: "g0/N" is the same under both
// conventions, so a session that has observed nothing still writes all 48
// copper ports. The refusal below is about the four cages and nothing else.
//
// An UNOBSERVED convention refuses to name a cage rather than guessing tg0/1,
// and the reason is not that a guess would hit the wrong port — tg0/1 on a
// 49..52 chassis simply does not exist. It is what happens next:
// [Client.configBlock] sends every line of its block and collects the failures
// (so a refusal is attributed to the line that earned it), so a rejected
// "interface tg0/1" leaves the prompt at Switch_config# and the FOLLOWING
// lines — "switchport mode access", "shutdown" — are typed into the
// enclosing context. What this CLI does with those has never been captured, and
// a production switch is not the place to find out. The second reason is the
// message: ApplyPortAdmin's own failure text blames the firmware's command
// FORMS, which would be a false lead — the form is right, the name was invented.
func (cn cageNumbering) cliPortName(standard string) (string, error) {
	var n, kind, ok = splitStandard(standard)
	if !ok {
		// Not a standard name at all: handed back unchanged, as before. The
		// device refuses it and names it in the refusal, which is the honest
		// answer for something this connector never claimed to understand.
		return standard, nil
	}
	if kind == "XG" {
		if !cn.known {
			return "", fmt.Errorf("%s cannot be named on this device yet: two fielded builds "+
				"of this firmware number the SFP+ cages differently — \"tg0/1\"..\"tg0/4\" "+
				"(Build 95262) and \"tg0/49\"..\"tg0/52\" (Build 130907) — and nothing read "+
				"on this session has said which one this switch uses. meshcraft will not "+
				"guess an interface name: a cage name the device does not have is refused in "+
				"the MIDDLE of a configuration block, and the commands after it then land in "+
				"the enclosing context instead of on the port. Read this switch's state once "+
				"(\"show interface\" names its own cages) and send this again", standard)
		}
		return fmt.Sprintf("tg0/%d", cn.base+n), nil
	}
	return fmt.Sprintf("g0/%d", n), nil
}

func splitStandard(s string) (int, string, bool) {
	var up = strings.ToUpper(strings.TrimSpace(s))
	var kind string
	switch {
	case strings.HasPrefix(up, "XG"):
		kind = "XG"
	case strings.HasPrefix(up, "GE"):
		kind = "GE"
	default:
		return 0, "", false
	}
	var n, err = strconv.Atoi(up[2:])
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, kind, true
}

// uniquePortNumber is the device's own port index: GE<n> is n, XG<n> is 48+n.
// CAPTURED 2026-08-03: "Ifindex is 129, unique port number is 1" for
// GigaEthernet0/1 and "Ifindex is 178, unique port number is 50" for
// TGigaEthernet0/2 — so ifIndex = 128 + unique port number, which is how an
// SNMP sample finds its socket.
//
// Its argument is a STANDARD name, so the n it adds to copperPorts is the
// standard 1..4 and never the platform's own 49..52 — the two conventions
// sfpCageNumber reconciles collapse to one here BEFORE the arithmetic. Feed
// it a platform number and every optical ifIndex shifts by 48, which does
// not fail: it silently points supervision at copper ports. The invariant is
// pinned by TestIfIndexUnaffectedByCageNumbering.
func uniquePortNumber(standard string) (int, bool) {
	var n, kind, ok = splitStandard(standard)
	if !ok {
		return 0, false
	}
	if kind == "XG" {
		return copperPorts + n, true
	}
	return n, true
}

// IfIndexOf maps a standard port name to the SNMP ifIndex this platform
// serves. Captured on two ports at both ends of the range (see above).
func IfIndexOf(standard string) (int, bool) {
	var num, ok = uniquePortNumber(standard)
	if !ok {
		return 0, false
	}
	return ifIndexBase + num, true
}

const ifIndexBase = 128

// --- show version ------------------------------------------------------

// versionFacts is what "show version" establishes at login.
type versionFacts struct {
	Model    string // S3400-48T4SP
	Version  string // 2.0.2J
	Build    string // 95262
	Serial   string // CG2003161906N0002
	MAC      string // 64:9d:99:22:7b:95
	SysObjID string // 1.3.6.1.4.1.52642.1.445.0
}

var (
	// Two spellings of the SAME line, both CAPTURED on a S3400-48T4SP running
	// Version 2.0.2J — the word "Series" is not part of the contract:
	//
	//	S3400-48T4SP Series Software, Version 2.0.2J Build 95262, RELEASE SOFTWARE
	//	S3400-48T4SP Software, Version 2.0.2J Build 130907, RELEASE SOFTWARE
	//
	// The second is Build 130907 (compiled 2024-8-23), read on sw01-arena in
	// production. Same version train, banner reworded. While "Series" was
	// mandatory here the line did not match at all, so Model and Version came
	// back empty — and an empty Model is what looksLikeS3400CLI refuses on,
	// which refused EVERY write to that switch and left its firmware unknown.
	// A word of prose in a banner must never be load-bearing.
	reVerLine   = regexp.MustCompile(`(?i)(S\d{4}-[A-Z0-9]+)\s+(?:Series\s+)?Software,\s*Version\s+([\w.]+)(?:\s+Build\s+(\d+))?`)
	reVerSerial = regexp.MustCompile(`(?i)Serial\s+num\s*:\s*([A-Za-z0-9\-]+)`)
	reVerMAC    = regexp.MustCompile(`(?i)Base\s+ethernet\s+MAC\s+Address\s*:\s*([0-9a-f:.]+)`)
	reVerSysOID = regexp.MustCompile(`(?i)system_ID\s*:\s*([0-9.]+)`)
	// The vendor line — "Fiberstore Co., Limited Internetwork Operating
	// System Software" — is what tells this CLI apart from the FSOS of the
	// S3900 next door, whose "show version" says "Operation Code Version".
	reVerVendor = regexp.MustCompile(`(?i)Fiberstore|FS\.COM`)
)

func parseShowVersion(out string) versionFacts {
	var f versionFacts
	var m = reVerLine.FindStringSubmatch(out)
	if m != nil {
		f.Model = strings.ToUpper(m[1])
		f.Version = m[2]
		f.Build = m[3]
	}
	if m = reVerSerial.FindStringSubmatch(out); m != nil {
		f.Serial = m[1]
	}
	if m = reVerMAC.FindStringSubmatch(out); m != nil {
		f.MAC = strings.ToLower(m[1])
	}
	if m = reVerSysOID.FindStringSubmatch(out); m != nil {
		f.SysObjID = m[1]
	}
	return f
}

// looksLikeS3400CLI: does this "show version" belong to the surveyed CLI?
// The dialect gate rests on it — an unrecognized CLI gets reads only.
func looksLikeS3400CLI(out string, f versionFacts) bool {
	var ok, _ = s3400CLIVerdict(out, f)
	return ok
}

// s3400CLIVerdict is looksLikeS3400CLI plus the reason the refusal quotes.
//
// It reports ONLY what it actually evaluated, and that is the whole point.
// The model/version gate short-circuits BEFORE the vendor line is tested, yet
// the refusal used to announce "carries neither the FS vendor line nor a
// Series Software line" — two findings at once, on a device whose vendor line
// was plainly present ("Fiberstore Co., Limited ... FS.COM"). One claim was
// false and the other had never been checked, and it sent an hour of
// diagnosis after the wrong fault on sw01-arena, whose banner was missing
// nothing but the word "Series". A refusal is read by someone who cannot
// see the device: it may only state what was measured.
func s3400CLIVerdict(out string, f versionFacts) (bool, string) {
	if f.Model == "" || f.Version == "" {
		var missing = "neither could be established"
		switch {
		case f.Model != "":
			missing = "the version could not be established"
		case f.Version != "":
			missing = "the model could not be established"
		}
		return false, fmt.Sprintf("\"show version\" carries no \"<model> [Series] Software, "+
			"Version ...\" line this connector can read — %s. Nothing further was "+
			"evaluated: the vendor line was never consulted", missing)
	}
	if !reVerVendor.MatchString(out) {
		return false, fmt.Sprintf("\"show version\" does announce model %s version %s, but "+
			"carries no FS/Fiberstore vendor line — so this is not the surveyed FS CLI",
			f.Model, f.Version)
	}
	return true, ""
}

// bannerModel pulls the product reference out of the login banner:
// "Welcome to FS S3400-48T4SP Ethernet Switch" (captured 2026-08-03).
func bannerModel(banner string) string {
	var m = reBannerModel.FindStringSubmatch(banner)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

var reBannerModel = regexp.MustCompile(`(?i)(S3400-[A-Z0-9]+)`)

// --- show interface | include protocol ---------------------------------
//
//	GigaEthernet0/1 is down, line protocol is down
//	GigaEthernet0/1 is administratively down, line protocol is down
//	TGigaEthernet0/2 is up, line protocol is up
//	VLAN99 is up, line protocol is up
//	Null0 is up, line protocol is up
//
// Only physical sockets are kept: an SVI is not a faceplate port.
//
// The second line is the SHUT state, captured 2026-08-10 on the lab 48T4SP
// (testdata/show_interface_g01_admin_down.txt) — the first two lines are the same
// port a "no shutdown" apart. The administrative word is captured in its OWN
// group for that reason: the protocol clause reads "down" in both, so it cannot
// distinguish them, and the long name is "GigaEthernet", not the industry's
// "GigabitEthernet".

var reIfLine = regexp.MustCompile(`(?im)^\s*(\S+)\s+is\s+(up|down|administratively down)\s*,\s*line\s+protocol\s+is\s+(up|down)`)

// ParseLinkStates returns the operational state of every physical port, keyed
// by standard name.
//
// "administratively down" is reported as disabled rather than down: an
// operator who shut a port must not be told the cable is out.
func ParseLinkStates(out string) map[string]driver.LinkState {
	var links = map[string]driver.LinkState{}
	var matches = reIfLine.FindAllStringSubmatch(out, -1)
	var i int
	for i = range matches {
		var name, ok = (&Driver{}).StandardPortName(matches[i][1])
		if !ok {
			continue
		}
		var admin = strings.ToLower(matches[i][2])
		var proto = strings.ToLower(matches[i][3])
		switch {
		case strings.HasPrefix(admin, "administratively"):
			links[name] = driver.LinkDisabled
		case proto == "up":
			links[name] = driver.LinkUpState
		default:
			links[name] = driver.LinkDown
		}
	}
	return links
}

// --- show interface <port> (detail) ------------------------------------
//
//	TGigaEthernet0/2 is up, line protocol is up
//	  Ifindex is 178, unique port number is 50
//	  Hardware is 10Giga-FX-SFP, address is 649d.9922.7bc7 (bia 649d.9922.7bc7)
//	  Full-duplex,  10000Mb/s,  Flow-Control Off
//	DDM info:
//	    TX power:-1.61 dBm, RX power:-1.51 dBm
//	    SFP temperature:47.00 C,supply voltage :3.29V,Bias Current.:42.84mA

// PortDetail is what the per-port read yields beyond up/down.
type PortDetail struct {
	Name      string // standard name
	IfIndex   int
	Hardware  string // "Giga-TX", "10Giga-FX-SFP"
	MAC       string
	SpeedMbps int
	FullDup   bool
	// Optics, present only on an SFP cage with a DDM-capable module.
	HasOptics bool
	TxPowerDB float64
	RxPowerDB float64
	TempC     float64
}

var (
	reDetIfindex = regexp.MustCompile(`(?i)Ifindex\s+is\s+(\d+),\s*unique\s+port\s+number\s+is\s+(\d+)`)
	reDetHW      = regexp.MustCompile(`(?i)Hardware\s+is\s+([\w\-]+),\s*address\s+is\s+([0-9a-f.]+)`)
	reDetSpeed   = regexp.MustCompile(`(?i)(Full|Half|Auto)-duplex,\s*(\d+)Mb/s`)
	reDetPower   = regexp.MustCompile(`(?i)TX\s+power\s*:\s*(-?[\d.]+)\s*dBm,\s*RX\s+power\s*:\s*(-?[\d.]+)\s*dBm`)
	reDetTemp    = regexp.MustCompile(`(?i)SFP\s+temperature\s*:\s*(-?[\d.]+)\s*C`)
)

func parsePortDetail(out string) PortDetail {
	var d PortDetail
	var m = reIfLine.FindStringSubmatch(out)
	if m != nil {
		var name, ok = (&Driver{}).StandardPortName(m[1])
		if ok {
			d.Name = name
		}
	}
	if m = reDetIfindex.FindStringSubmatch(out); m != nil {
		d.IfIndex, _ = strconv.Atoi(m[1])
	}
	if m = reDetHW.FindStringSubmatch(out); m != nil {
		d.Hardware = m[1]
		d.MAC = strings.ToLower(m[2])
	}
	if m = reDetSpeed.FindStringSubmatch(out); m != nil {
		d.FullDup = strings.EqualFold(m[1], "full")
		d.SpeedMbps, _ = strconv.Atoi(m[2])
	}
	if m = reDetPower.FindStringSubmatch(out); m != nil {
		d.TxPowerDB, _ = strconv.ParseFloat(m[1], 64)
		d.RxPowerDB, _ = strconv.ParseFloat(m[2], 64)
		d.HasOptics = true
	}
	if m = reDetTemp.FindStringSubmatch(out); m != nil {
		d.TempC, _ = strconv.ParseFloat(m[1], 64)
	}
	return d
}

// --- show vlan ---------------------------------------------------------
//
//	VLAN Status  Name                             Ports
//	---- ------- -------------------------------- ---------------------------------
//	1    Static  Default                          g0/1, g0/2, g0/3, g0/4, g0/5
//	                                              g0/6, g0/7, ...
//	99   Static  LAN99_Management                 tg0/1, tg0/2, tg0/3, tg0/4
//
// The port list WRAPS onto continuation lines that carry no VLAN id, which is
// the whole difficulty: a parser that reads line by line without carrying the
// current VLAN forward loses every port after the fifth.

var (
	reVlanRow  = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+(\S.*?)\s{2,}(\S.*)$`)
	reVlanCont = regexp.MustCompile(`^\s{6,}(\S.*)$`)
)

// VlanTable is the observed VLAN set with its membership.
type VlanTable struct {
	VLANs []driver.VLAN
	// Members maps a standard port name to the VLAN ids it appears in.
	Members map[string][]int
}

func parseShowVlan(out string) VlanTable {
	var t = VlanTable{Members: map[string][]int{}}
	var current = -1
	var d = &Driver{}
	var addPorts = func(list string) {
		var raw string
		for _, raw = range strings.Split(list, ",") {
			var name, ok = d.StandardPortName(strings.TrimSpace(raw))
			if !ok || current < 0 {
				continue
			}
			t.Members[name] = append(t.Members[name], current)
		}
	}
	var line string
	for _, line = range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" || strings.HasPrefix(strings.TrimSpace(line), "----") {
			continue
		}
		if strings.Contains(line, "VLAN") && strings.Contains(line, "Status") {
			continue
		}
		var m = reVlanRow.FindStringSubmatch(line)
		if m != nil {
			var id, err = strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			current = id
			t.VLANs = append(t.VLANs, driver.VLAN{ID: id, Name: strings.TrimSpace(m[3])})
			addPorts(m[4])
			continue
		}
		if m = reVlanCont.FindStringSubmatch(line); m != nil && current >= 0 {
			addPorts(m[1])
		}
	}
	return t
}

// --- show running-config ----------------------------------------------

// ConfigFacts is what the running configuration establishes.
type ConfigFacts struct {
	Hostname string
	// Ports: standard name -> observed switchport configuration.
	Ports map[string]driver.PortState
	// VlanNames: VLAN id -> name, from the "vlan N / name X" blocks.
	VlanNames map[int]string
	// MgmtVLAN is the VLAN carrying an SVI with an address.
	MgmtVLAN int
	MgmtAddr string // "192.0.2.3 255.255.0.0" as written
	Gateway  string
	STPMode  string // "rstp" from "spanning-tree mode rstp"
	STPOff   bool   // "no spanning-tree"
	LLDPRun  bool
	SSHOn    bool
	HTTPOn   bool
	// Communities: the SNMP communities the config carries, as written.
	Communities []string
	// TrapReceivers: the "snmp-server host" lines, in the order read.
	//
	// This range enables traps BY DECLARING A HOST — there is no "snmp-server
	// enable traps" at all ("snmp-server enable ?" answers "Unknown
	// command", captured 2026-08-10) — so this slice is the whole of the trap
	// configuration, and its emptiness is what "the device emits nothing"
	// looks like in the configuration.
	TrapReceivers []TrapReceiver
}

// TrapReceiver is one "snmp-server host" line as the configuration renders it.
//
// EVERY FIELD IS OPTIONAL EXCEPT [TrapReceiver.Host] and [TrapReceiver.Raw], and
// that is not defensive parsing — it is what makes the removal safe. The removal
// form on this range must be the FULL one ("no snmp-server host <ip> version
// v2c <community>"); the short one answers "Incomplete command" and SILENTLY
// LEAVES THE HOST (captured 2026-08-10). A line whose version or community this
// parser could not read is therefore a line meshcraft must NOT try to remove,
// and reading those two as empty rather than guessing them is what tells the
// applier so (cf. trap.go, trapRemoveLine).
type TrapReceiver struct {
	// Host is the collector address, as written.
	Host string
	// Version is the version token normalized to "v1" / "v2c" / "v3",
	// empty when the line carried none this parser recognized.
	Version string
	// Community is the community (v1/v2c) the traps are marked with. Empty when
	// the line carried no such token.
	Community string
	// Raw is the line as read, trimmed — what a refusal or a note quotes, so an
	// operator reads the device's own words and not this parser's summary.
	Raw string
}

var (
	reCfgHostname  = regexp.MustCompile(`(?im)^\s*hostname\s+(\S+)`)
	reCfgIface     = regexp.MustCompile(`(?i)^\s*interface\s+(\S+)\s*$`)
	reCfgVlanDecl  = regexp.MustCompile(`(?i)^\s*vlan\s+(\d+)\s*$`)
	reCfgName      = regexp.MustCompile(`(?i)^\s*name\s+(\S.*?)\s*$`)
	reCfgPvid      = regexp.MustCompile(`(?i)^\s*switchport\s+pvid\s+(\d+)`)
	reCfgTrunk     = regexp.MustCompile(`(?i)^\s*switchport\s+mode\s+trunk`)
	reCfgAccess    = regexp.MustCompile(`(?i)^\s*switchport\s+mode\s+access`)
	reCfgAllowed   = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+vlan-allowed\s+(\S+)`)
	reCfgIP        = regexp.MustCompile(`(?i)^\s*ip\s+address\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)`)
	reCfgIPDHCP    = regexp.MustCompile(`(?i)^\s*ip\s+address\s+dhcp`)
	reCfgRoute     = regexp.MustCompile(`(?i)^\s*ip\s+route\s+default\s+(\d+\.\d+\.\d+\.\d+)`)
	reCfgRoute2    = regexp.MustCompile(`(?i)^\s*ip\s+route\s+0\.0\.0\.0\s+0\.0\.0\.0\s+(\d+\.\d+\.\d+\.\d+)`)
	reCfgSTPMode   = regexp.MustCompile(`(?i)^\s*spanning-tree\s+mode\s+(\w+)`)
	reCfgSTPOff    = regexp.MustCompile(`(?i)^\s*no\s+spanning-tree\s*$`)
	reCfgVlanSVI   = regexp.MustCompile(`(?i)^VLAN(\d+)$`)
	reCfgCommunity = regexp.MustCompile(`(?i)^\s*snmp-server\s+community\s+(?:[07]\s+)?(\S+)`)
	// reCfgTrapHost splits "snmp-server host" into the address and everything
	// after it. The tail is tokenized in [parseTrapHostLine] rather than matched
	// here on purpose: the WRITE form is captured, the way this firmware RENDERS
	// it back into "show running-config" is not (cf. trap.go's header), so the
	// tail is read token by token and what is missing is reported missing.
	reCfgTrapHost = regexp.MustCompile(`(?i)^\s*snmp-server\s+host\s+(\S+)\s*(.*)$`)
	// reTrapVersion is the version token in any spelling this parser will accept.
	// The CAPTURED write token is "v2c" — "2c", the Cisco spelling, is
	// REFUSED by this CLI — and the bare digits are accepted here only for
	// READING, so a line somebody else wrote is understood rather than dropped.
	reTrapVersion = regexp.MustCompile(`(?i)^v?[123]c?$`)
)

// parseTrapHostLine reads one "snmp-server host" line into a [TrapReceiver].
//
// DEDUCED, and named as such because everything downstream of it depends on the
// shape: the command form was captured on 2026-08-10, the RENDERING is not. What
// is assumed is only that this firmware prints the line back with its tokens in
// the order they were typed, which is what the one captured "snmp-server"
// rendering on this range does ("snmp-server community 0 <s> ro" comes back
// "snmp-server community 0 <s> RO" — the keyword's CASE changed, the order did
// not). Anything unrecognized lands as an empty field and never as a guess.
func parseTrapHostLine(line string) (TrapReceiver, bool) {
	var m = reCfgTrapHost.FindStringSubmatch(line)
	if m == nil {
		return TrapReceiver{}, false
	}
	var r = TrapReceiver{Host: m[1], Raw: strings.TrimSpace(line)}
	var toks = strings.Fields(m[2])
	// "version" is a keyword in the captured write form. A rendering that
	// drops it is still readable; one that keeps it must not turn the keyword
	// into the version.
	if len(toks) > 0 && strings.EqualFold(toks[0], "version") {
		toks = toks[1:]
	}
	if len(toks) > 0 && reTrapVersion.MatchString(toks[0]) {
		r.Version = normalizeTrapVersion(toks[0])
		toks = toks[1:]
	}
	if len(toks) > 0 {
		r.Community = toks[0]
	}
	return r, true
}

// normalizeTrapVersion folds the spellings onto "v1" / "v2c" / "v3".
func normalizeTrapVersion(tok string) string {
	switch strings.ToLower(strings.TrimSpace(tok)) {
	case "v1", "1":
		return "v1"
	case "v2c", "2c", "2":
		return "v2c"
	case "v3", "3":
		return "v3"
	}
	return strings.ToLower(strings.TrimSpace(tok))
}

// ParseRunningConfig reads the whole configuration.
//
// Ports absent from the configuration are NOT absent from the switch: a
// factory port carries no switchport line at all (captured — g0/1..g0/48 are
// bare "interface GigaEthernet0/N" blocks). They are reported with the
// platform default, access on VLAN 1, rather than left out.
func ParseRunningConfig(out string) ConfigFacts {
	var f = ConfigFacts{
		Ports:     map[string]driver.PortState{},
		VlanNames: map[int]string{},
	}
	var d = &Driver{}
	var m = reCfgHostname.FindStringSubmatch(out)
	if m != nil {
		f.Hostname = m[1]
	}

	var curPort string
	var curVlanDecl = -1
	var curSVI = -1
	var st driver.PortState
	var flushPort = func() {
		if curPort != "" {
			if st.Mode == "" {
				st.Mode = "access"
			}
			if st.Mode == "access" && st.PVID == 0 {
				st.PVID = 1
			}
			f.Ports[curPort] = st
		}
		curPort = ""
		st = driver.PortState{}
	}

	// One reusable submatch slice for the whole scan: every regexp below
	// returns []string or nil, and `mm != nil` is the "did it match" test.
	// Each match is consumed inside its own block before the next assignment.
	var mm []string
	var line string
	for _, line = range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "!" {
			flushPort()
			curVlanDecl = -1
			curSVI = -1
			continue
		}
		mm = reCfgIface.FindStringSubmatch(line)
		if mm != nil {
			flushPort()
			curVlanDecl = -1
			curSVI = -1
			var name, ok = d.StandardPortName(mm[1])
			if ok {
				curPort = name
			} else {
				// Still only tried when the name was NOT a faceplate port.
				var sv = reCfgVlanSVI.FindStringSubmatch(strings.ToUpper(mm[1]))
				if sv != nil {
					curSVI, _ = strconv.Atoi(sv[1])
				}
			}
			continue
		}
		mm = reCfgVlanDecl.FindStringSubmatch(line)
		if mm != nil && curPort == "" {
			curVlanDecl, _ = strconv.Atoi(mm[1])
			continue
		}
		if curVlanDecl > 0 {
			mm = reCfgName.FindStringSubmatch(line)
			if mm != nil {
				f.VlanNames[curVlanDecl] = mm[1]
				continue
			}
		}
		if curSVI > 0 {
			mm = reCfgIP.FindStringSubmatch(line)
			if mm != nil {
				f.MgmtVLAN = curSVI
				f.MgmtAddr = mm[1] + " " + mm[2]
				continue
			}
			if reCfgIPDHCP.MatchString(line) {
				f.MgmtVLAN = curSVI
				f.MgmtAddr = "dhcp"
				continue
			}
		}
		if curPort != "" {
			mm = reCfgPvid.FindStringSubmatch(line)
			if mm != nil {
				st.PVID, _ = strconv.Atoi(mm[1])
				continue
			}
			if reCfgTrunk.MatchString(line) {
				st.Mode = "trunk"
				continue
			}
			if reCfgAccess.MatchString(line) {
				st.Mode = "access"
				continue
			}
			mm = reCfgAllowed.FindStringSubmatch(line)
			if mm != nil {
				st.Tagged = expandVlanList(mm[1])
				continue
			}
			continue
		}
		mm = reCfgRoute.FindStringSubmatch(line)
		if mm != nil {
			f.Gateway = mm[1]
			continue
		}
		mm = reCfgRoute2.FindStringSubmatch(line)
		if mm != nil && f.Gateway == "" {
			f.Gateway = mm[1]
			continue
		}
		mm = reCfgSTPMode.FindStringSubmatch(line)
		if mm != nil {
			f.STPMode = strings.ToLower(mm[1])
			continue
		}
		if reCfgSTPOff.MatchString(line) {
			f.STPOff = true
			continue
		}
		mm = reCfgCommunity.FindStringSubmatch(line)
		if mm != nil {
			f.Communities = append(f.Communities, mm[1])
			continue
		}
		var rcv, isHost = parseTrapHostLine(line)
		if isHost {
			f.TrapReceivers = append(f.TrapReceivers, rcv)
			continue
		}
		var trimmed = strings.TrimSpace(strings.ToLower(line))
		switch trimmed {
		case "lldp run":
			f.LLDPRun = true
		case "ip sshd enable":
			f.SSHOn = true
		case "ip http server":
			f.HTTPOn = true
		}
	}
	flushPort()
	return f
}

// expandVlanList turns "1-10,99" into the ids it names.
func expandVlanList(s string) []int {
	var out []int
	var chunk string
	for _, chunk = range strings.Split(s, ",") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		var lo, hi string
		var dash = strings.Index(chunk, "-")
		if dash > 0 {
			lo, hi = chunk[:dash], chunk[dash+1:]
		} else {
			lo, hi = chunk, chunk
		}
		var a, aerr = strconv.Atoi(strings.TrimSpace(lo))
		var b, berr = strconv.Atoi(strings.TrimSpace(hi))
		if aerr != nil || berr != nil || a < 1 || b < a || b > 4094 {
			continue
		}
		var i int
		for i = a; i <= b; i++ {
			out = append(out, i)
		}
	}
	return out
}

// --- show ip interface brief -------------------------------------------
//
//	Interface                  IP-Address      Method Protocol-Status
//	Null0                      unassigned      manual up
//	VLAN99                     192.0.2.3       manual up

var reIPBrief = regexp.MustCompile(`(?im)^\s*(VLAN\d+)\s+(\S+)\s+(\S+)\s+(\S+)`)

// L3Iface is one routed interface as "show ip interface brief" reports it.
type L3Iface struct {
	VlanID int
	IP     string // "" when unassigned
	Method string // manual | dhcp
	Up     bool
}

func parseIPInterfaceBrief(out string) []L3Iface {
	var out2 []L3Iface
	var matches = reIPBrief.FindAllStringSubmatch(out, -1)
	var i int
	for i = range matches {
		var id, err = strconv.Atoi(strings.TrimPrefix(strings.ToUpper(matches[i][1]), "VLAN"))
		if err != nil {
			continue
		}
		var ip = matches[i][2]
		if strings.EqualFold(ip, "unassigned") {
			ip = ""
		}
		out2 = append(out2, L3Iface{
			VlanID: id,
			IP:     ip,
			Method: strings.ToLower(matches[i][3]),
			Up:     strings.EqualFold(matches[i][4], "up"),
		})
	}
	return out2
}

// --- show spanning-tree ------------------------------------------------
//
//	Spanning tree enabled protocol RSTP(2004)
//
//	RSTP
//	  Root ID    Priority    0
//	             Address     D401.C3AB.32F4
//	             Port        TGigaEthernet0/2
//	             Cost        6000
//	  Bridge ID  Priority    32768
//	             Address     649D.9922.7B95
//
//	Interface        Role Sts Cost      Pri.Nbr  Type
//	---------------- ---- --- --------- -------- -----------------------
//	tg0/2            Root FWD 2000      128.178  P2p

var (
	reStpProto   = regexp.MustCompile(`(?i)Spanning\s+tree\s+(enabled|disabled)(?:\s+protocol\s+(\w+))?`)
	reStpBlock   = regexp.MustCompile(`(?im)^\s*(Root ID|Bridge ID)\s+Priority\s+(\d+)`)
	reStpAddr    = regexp.MustCompile(`(?im)^\s*Address\s+([0-9A-Fa-f.:]+)`)
	reStpCost    = regexp.MustCompile(`(?im)^\s*Cost\s+(\d+)`)
	reStpPortRow = regexp.MustCompile(`(?im)^\s*(\S+)\s+(Root|Desg|Altn|Back|Mstr|Disb|\S+)\s+(FWD|BLK|LRN|LIS|DIS|\S+)\s+(\d+)\s+(\S+)\s+(\S.*)$`)
)

// parseSpanningTree reads the bridge and per-port view.
//
// The two "Priority" lines are told apart by their label and NOT by order:
// on a switch that IS root the device prints the same address twice, and a
// positional parser would silently report the root's priority as the bridge's.
func parseSpanningTree(out string) driver.SpanningTreeState {
	var st driver.SpanningTreeState
	var m = reStpProto.FindStringSubmatch(out)
	if m != nil {
		st.Enabled = strings.EqualFold(m[1], "enabled")
		if m[2] != "" {
			st.Mode = strings.ToLower(m[2])
		}
	}

	// Split into the Root ID / Bridge ID blocks and read each one's own
	// Address / Cost.
	var blocks = reStpBlock.FindAllStringSubmatchIndex(out, -1)
	var i int
	for i = range blocks {
		var label = out[blocks[i][2]:blocks[i][3]]
		var prio, _ = strconv.Atoi(out[blocks[i][4]:blocks[i][5]])
		var end = len(out)
		if i+1 < len(blocks) {
			end = blocks[i+1][0]
		}
		var body = out[blocks[i][1]:end]
		var addr string
		var am = reStpAddr.FindStringSubmatch(body)
		if am != nil {
			addr = normalizeBridgeID(am[1])
		}
		if strings.EqualFold(label, "Root ID") {
			st.RootPriority = prio
			st.RootBridgeID = addr
			// Still only read inside the Root ID branch.
			var cm = reStpCost.FindStringSubmatch(body)
			if cm != nil {
				st.RootPathCost, _ = strconv.Atoi(cm[1])
			}
		} else {
			st.BridgePriority = prio
			st.BridgeID = addr
		}
	}
	st.IsRoot = st.BridgeID != "" && st.BridgeID == st.RootBridgeID

	var d = &Driver{}
	var rows = reStpPortRow.FindAllStringSubmatch(out, -1)
	for i = range rows {
		var name, ok = d.StandardPortName(rows[i][1])
		if !ok {
			continue
		}
		var cost, _ = strconv.Atoi(rows[i][4])
		st.Ports = append(st.Ports, driver.SpanningTreePort{
			Port:     name,
			Role:     stpRoleWord(rows[i][2]),
			State:    stpStateWord(rows[i][3]),
			PathCost: cost,
		})
	}
	return st
}

// stpRoleWord turns the table's abbreviated role — "Desg", "Altn",
// "Back" — into the shared vocabulary (driver.STPRole*), the words every
// other connector reports and the UI compares against. "desg" looks parsed
// but matches nothing downstream.
func stpRoleWord(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "root":
		return driver.STPRoleRoot
	case "desg", "designated":
		return driver.STPRoleDesignated
	case "altn", "alternate":
		return driver.STPRoleAlternate
	case "back", "backup":
		return driver.STPRoleBackup
	case "mstr", "master":
		return driver.STPRoleMaster
	case "disabled":
		return driver.STPRoleDisabled
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// stpStateWord turns the platform's three-letter state into the shared
// vocabulary the UI already speaks.
func stpStateWord(s string) string {
	switch strings.ToUpper(s) {
	case "FWD":
		return "forwarding"
	case "BLK":
		return "blocking"
	case "LRN":
		return "learning"
	case "LIS":
		return "listening"
	case "DIS":
		return "disabled"
	}
	return strings.ToLower(s)
}

// normalizeBridgeID turns "649D.9922.7B95" into "64:9d:99:22:7b:95", the
// form the rest of the product uses for a MAC.
func normalizeBridgeID(s string) string {
	var hex = strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, s)
	if len(hex) != 12 {
		return strings.ToLower(s)
	}
	var parts []string
	var i int
	for i = 0; i < 12; i += 2 {
		parts = append(parts, strings.ToLower(hex[i:i+2]))
	}
	return strings.Join(parts, ":")
}

// --- show lldp neighbor ------------------------------------------------
//
//	Capability Codes:
//	       (R)Router,(B)Bridge,...
//	Device-ID       Local-Intf      Hldtme       Port-ID      Capability
//	                TGi0/2          109          Eth 1/25     R B
//	Total entries displayed: 1
//
// The Device-ID column CAN BE EMPTY (captured: the neighbour advertises no
// system name), so the table is read by COLUMN POSITION taken from the
// header — a whitespace split would shift every field left by one and file
// the local port under the device name.

// LLDPNeighbor is one entry of the neighbour table.
type LLDPNeighbor struct {
	DeviceID  string
	LocalPort string // standard name
	HoldTime  int
	PortID    string
	Caps      string
}

func parseLLDPNeighbors(out string) []LLDPNeighbor {
	var lines = strings.Split(out, "\n")
	var hdr = -1
	var i int
	for i = range lines {
		if strings.Contains(lines[i], "Device-ID") && strings.Contains(lines[i], "Local-Intf") {
			hdr = i
			break
		}
	}
	if hdr < 0 {
		return nil
	}
	var header = lines[hdr]
	var colLocal = strings.Index(header, "Local-Intf")
	var colHold = strings.Index(header, "Hldtme")
	var colPort = strings.Index(header, "Port-ID")
	var colCap = strings.Index(header, "Capability")
	if colLocal < 0 || colHold < 0 || colPort < 0 {
		return nil
	}

	var d = &Driver{}
	var out2 []LLDPNeighbor
	for i = hdr + 1; i < len(lines); i++ {
		var line = strings.TrimRight(lines[i], " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "Total entries") {
			break
		}
		var n LLDPNeighbor
		n.DeviceID = strings.TrimSpace(slice(line, 0, colLocal))
		var localRaw = strings.TrimSpace(slice(line, colLocal, colHold))
		var name, ok = d.StandardPortName(localRaw)
		if !ok {
			continue
		}
		n.LocalPort = name
		n.HoldTime, _ = strconv.Atoi(strings.TrimSpace(slice(line, colHold, colPort)))
		if colCap > colPort {
			n.PortID = strings.TrimSpace(slice(line, colPort, colCap))
			n.Caps = strings.TrimSpace(slice(line, colCap, len(line)))
		} else {
			n.PortID = strings.TrimSpace(slice(line, colPort, len(line)))
		}
		out2 = append(out2, n)
	}
	return out2
}

func slice(s string, from, to int) string {
	if from < 0 || from >= len(s) {
		return ""
	}
	if to > len(s) || to < 0 {
		to = len(s)
	}
	if to <= from {
		return ""
	}
	return s[from:to]
}

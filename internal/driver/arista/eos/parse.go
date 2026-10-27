package eos

// Parsers for the Arista EOS CLI. Like the Cisco connector next door, the
// fixtures these are tested against are DOCUMENTED output shapes, not lab
// captures — cf. the package doc. They are written to be tolerant of the column
// padding EOS varies between releases, and strict about what they agree to call
// a faceplate port.

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// # Numbering: ONE prefix, because the platform has one
//
// EOS names every faceplate port `Ethernet<N>`, whatever it is — a 1G copper
// jack, a 10G SFP+ cage and a 100G QSFP cage are all `EthernetN` and differ only
// by what `show interfaces status` reports in the Type column. That is the
// opposite of the Catalyst situation, where the MEDIA WORD is part of the name
// and a 48FPD carries both `GigabitEthernet1/0/1` and `TenGigabitEthernet1/0/1`
// — two sockets, one number, which is why that connector needs two prefixes.
//
// So this connector's standard model uses ONE prefix, "ET", and `Ethernet49`
// becomes "ET49". Deriving the prefix from the SPEED instead — "GE" under 10G,
// "XG" above — was considered and rejected for a concrete reason: the speed of a
// cage is read off the transceiver in it, so a port would change its standard
// name when somebody swapped a 10G optic for a 25G one, and every stored intent,
// telemetry sample and LLDP correlation keyed on the old name would silently
// point at nothing. The name must be a property of the SOCKET, and on this
// platform the socket only ever announces one thing: its number.
//
// The medium still reaches the screen — through the faceplate GROUP's `label`
// ("SFP+", "QSFP"), which is what portrules.mediaOf actually classifies on (cf.
// its comment: the Instant On connector names everything "GE" and distinguishes
// only by label). Same shape, same reason.
//
// # REFUSED: breakout lanes, and this one costs coverage
//
// A QSFP cage split into four lanes is named `Ethernet49/1` … `Ethernet49/4` on
// EOS. Those are four distinct forwarding interfaces sharing a cage number, and
// the standard model has no notation for the second component: keying them as
// "ET49" would make four ports one, and inventing "ET4901" would collide with a
// real Ethernet4901 on a chassis that has one.
//
// They are therefore REFUSED by [splitEOSName], which means a switch with
// breakouts in service reports those ports nowhere — no drawing, no telemetry
// correlation, no profile. That is a real gap and it is chosen: an under-reported
// port is diagnosable, a port written to under the wrong name is a customer
// outage. TODO.md carries the naming decision that would close it.
//
// # REFUSED: Management1, and this one is deliberate
//
// `Management1` is the out-of-band port. It is not part of the switching fabric,
// it carries no VLAN, and a profile applied to it would be meaningless. It is
// skipped everywhere in this package, and mgmtaddr.go says so in words rather
// than leaving an operator wondering why the address they can see is not one
// meshcraft offers to change.

// rePortEOS accepts the spellings of an EOS faceplate interface across the three
// sources that name a port: the running-config and CLI (`Ethernet49`), `show
// interfaces status` (`Et49`), and SNMP ifDescr (the full form).
//
// The trailing `(/\d+)?` is captured on purpose even though it is refused: a
// breakout lane must be RECOGNISED as a port name in order to be declined for
// the right reason, rather than falling through as "not an interface at all".
var rePortEOS = regexp.MustCompile(`(?i)^\s*(ethernet|eth|et)\s*(\d+)(/(\d+))?\s*$`)

// reMgmtPortEOS matches the out-of-band management port, so callers can name it
// when they skip it instead of reporting an unexplained absence.
var reMgmtPortEOS = regexp.MustCompile(`(?i)^\s*(management|mgmt|ma)\s*(\d+)(/\d+)?\s*$`)

// StdPrefix is the standard-model prefix every faceplate port of this platform
// carries. One value, because the platform has one (cf. the header).
const StdPrefix = "ET"

// Silk is what the number is shown behind on screen — the name printed on the
// faceplate and typed at the CLI.
const Silk = "Et"

var reStdName = regexp.MustCompile(`^ET(\d+)$`)

// splitEOSName decomposes an interface name into its port number. ok=false as
// soon as anything falls outside what this connector covers — a breakout lane, a
// management port, a port-channel, or a name that is not an interface at all.
func splitEOSName(name string) (port int, ok bool) {
	var m = rePortEOS.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	if m[3] != "" {
		// A breakout lane. Recognised, and refused (cf. the header).
		return 0, false
	}
	var n, err = strconv.Atoi(m[2])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// IsManagementPort reports whether this is the out-of-band Management port. Used
// where a skip must be EXPLAINED rather than silent.
func IsManagementPort(name string) bool {
	return reMgmtPortEOS.MatchString(name)
}

// StandardPortName (driver.PortNamer) translates a PLATFORM port name into the
// standard-model name. Pure: no device access, usable before a session exists.
//
// This is what makes SNMP samples and LLDP neighbours attach to the right port
// AND what re-keys a pasted configuration at adoption time, so a device adopted
// by import and one adopted by push carry one vocabulary and not two.
func (d *Driver) StandardPortName(deviceName string) (string, bool) {
	var port, ok = splitEOSName(deviceName)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s%d", StdPrefix, port), true
}

// cliPortName translates a standard-model name into the full EOS interface name.
// Returns "" for a name outside the scheme — the caller refuses rather than
// writing blind.
//
// The FULL word is always used on the wire. EOS accepts `et49` interactively,
// and a transcript that reads `interface Ethernet49` is the one an operator can
// paste straight back into the device.
func cliPortName(std string) string {
	var m = reStdName.FindStringSubmatch(strings.TrimSpace(std))
	if m == nil {
		return ""
	}
	return "Ethernet" + m[1]
}

// stdPortLess orders standard names numerically ("ET2" before "ET10"), which
// string order does not.
func stdPortLess(a, b string) bool {
	var ma = reStdName.FindStringSubmatch(a)
	var mb = reStdName.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return a < b
	}
	var na, _ = strconv.Atoi(ma[1])
	var nb, _ = strconv.Atoi(mb[1])
	return na < nb
}

// --- show version ---

// versionFacts: what "show version" establishes on an EOS switch.
type versionFacts struct {
	// Release is the EOS image version, "4.24.2.1F".
	Release string
	// Model is the orderable part number, "DCS-7050SX3-48YC8-F".
	Model  string
	Serial string
	MAC    string
}

var (
	// The model is the FIRST line of `show version`, and it is the only field on
	// it: "Arista DCS-7050SX3-48YC8-F". Some releases also print a "Model:" line,
	// which is tried second rather than instead — a connector that only knew one
	// of the two forms would report an empty model on the other.
	reVerModelBare = regexp.MustCompile(`(?im)^\s*Arista\s+([A-Za-z0-9][\w\-]+)\s*$`)
	reVerModelKV   = regexp.MustCompile(`(?im)^\s*Model(?:\s+name)?\s*:\s*(\S+)`)
	reVerSerial    = regexp.MustCompile(`(?im)^\s*Serial\s+number\s*:\s*(\S+)`)
	// "System MAC address" is preferred over "Hardware MAC address": on a switch
	// where they differ, the system one is what the device uses on the wire and
	// therefore what a neighbour's LLDP and a DHCP server report.
	reVerMACSystem = regexp.MustCompile(`(?im)^\s*System\s+MAC\s+address\s*:\s*(\S+)`)
	reVerMACHW     = regexp.MustCompile(`(?im)^\s*Hardware\s+MAC\s+address\s*:\s*(\S+)`)
	reVerRelease   = regexp.MustCompile(`(?im)^\s*Software\s+image\s+version\s*:\s*(\S+)`)
	// reIsEOS is what decides the dialect: a device whose "show version" does not
	// announce Arista is not one this command set was written for.
	reIsEOS = regexp.MustCompile(`(?i)\bArista\b|Software image version:\s*\d+\.\d+`)
)

func parseShowVersion(out string) versionFacts {
	var f versionFacts
	var m = reVerModelBare.FindStringSubmatch(out)
	if m != nil {
		f.Model = m[1]
	} else if m = reVerModelKV.FindStringSubmatch(out); m != nil {
		f.Model = m[1]
	}
	if m = reVerSerial.FindStringSubmatch(out); m != nil {
		f.Serial = m[1]
	}
	if m = reVerMACSystem.FindStringSubmatch(out); m != nil {
		f.MAC = m[1]
	} else if m = reVerMACHW.FindStringSubmatch(out); m != nil {
		f.MAC = m[1]
	}
	if m = reVerRelease.FindStringSubmatch(out); m != nil {
		f.Release = m[1]
	}
	return f
}

// --- show interfaces status ---

// statusPort is one row of "show interfaces status".
type statusPort struct {
	// Name is the standard-model name ("ET49").
	Name string
	// CLIName is the FULL EOS name, ready to be written after "interface ".
	CLIName string
	Num     int
	Type    string
	LinkUp  bool
	// Status is the raw word from the Status column, lower-cased. Kept whole
	// rather than reduced to LinkUp: "notconnect" (nothing plugged in) and
	// "errdisabled" (the switch shut the port itself) are both "no link" and call
	// for opposite reactions from the operator.
	Status string
	// SpeedMbps is the port's speed as the device reports it. Used ONLY for the
	// faceplate label, never for the port's name (cf. the numbering header).
	SpeedMbps int
}

// A documented "show interfaces status" row:
//
//	Port       Name         Status       Vlan     Duplex Speed  Type            Flags
//	Et1                     connected    1        full   1G     1000BASE-T
//	Et2                     notconnect   1        auto   auto   Not Present
//	Et49       core-uplink  connected    trunk    full   10G    10GBASE-SR
//	Et50                    errdisabled  trunk    full   10G    Not Present
//	Ma1                     connected    routed   full   1G     10/100/1000
//
// The Status word is the pivot and not the column position: the Name column is
// free text that may contain spaces, and so may the Type column ("Not Present"),
// so neither end can be split on whitespace. Anchoring on the finite set of
// status words is what makes both sides unambiguous.
var reStatusLine = regexp.MustCompile(
	`(?im)^(\S+)\s+(.*?)\b(connected|notconnect|disabled|errdisabled|err-disabled|inactive|suspended|monitoring|unknown)\b\s+(\S+)\s+(\S+)\s+(\S+)\s*(.*?)\s*$`)

func parseInterfacesStatus(out string) []statusPort {
	var res []statusPort
	var rows = reStatusLine.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var m = rows[i]
		var port, ok = splitEOSName(m[1])
		if !ok {
			// Not a faceplate port this connector numbers: a breakout lane, the
			// management port, a port-channel. Never numbered, never written to.
			continue
		}
		var p = statusPort{
			Name:    fmt.Sprintf("%s%d", StdPrefix, port),
			CLIName: fmt.Sprintf("Ethernet%d", port),
			Num:     port,
			Type:    strings.TrimSpace(m[7]),
			LinkUp:  strings.EqualFold(m[3], "connected"),
			Status:  normalizeStatusWord(m[3]),
		}
		p.SpeedMbps = speedMbpsOf(m[6], p.Type)
		res = append(res, p)
	}
	return res
}

// parseBreakoutCages counts, per QSFP cage, the lanes "show interfaces status"
// lists for it — the `Et49/1..4` rows [parseInterfacesStatus] refuses.
//
// It exists so that the refusal is SAID instead of silent. The connector's
// numbering deliberately carries no lane names (cf. the header on rePortEOS:
// an under-reported port is diagnosable, a port written to under the wrong
// name is a customer outage), and the cost used to be paid invisibly: a
// faceplate whose last four sockets are split simply ENDED at Et48, and
// nothing anywhere said four QSFP+ cages exist. The count feeds a note, never
// a port.
func parseBreakoutCages(out string) map[int]int {
	var lanes = map[int]int{}
	var rows = reStatusLine.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var m = rePortEOS.FindStringSubmatch(rows[i][1])
		if m == nil || m[3] == "" {
			continue
		}
		var n, err = strconv.Atoi(m[2])
		if err == nil && n > 0 {
			lanes[n]++
		}
	}
	return lanes
}

// breakoutNote turns the lane count into the sentence an operator needs, or ""
// when nothing is split.
func breakoutNote(lanes map[int]int) string {
	if len(lanes) == 0 {
		return ""
	}
	var nums = make([]int, 0, len(lanes))
	var n int
	for n = range lanes {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	var names = make([]string, 0, len(nums))
	for _, n = range nums {
		names = append(names, fmt.Sprintf("Et%d (%d lanes)", n, lanes[n]))
	}
	return fmt.Sprintf("%d QSFP cage(s) are SPLIT into breakout lanes — %s. meshcraft "+
		"does not name lane ports (Et49/1…), so these cages appear neither on the "+
		"drawing nor in the port list; un-split a cage on the device to manage it here",
		len(nums), strings.Join(names, ", "))
}

// normalizeStatusWord folds the two spellings EOS uses for the same state.
// Different releases print "errdisabled" and "err-disabled"; a reader that knew
// one of them would report the other as an unrecognised state and lose the very
// distinction the word exists to carry.
func normalizeStatusWord(w string) string {
	var s = strings.ToLower(strings.TrimSpace(w))
	if s == "err-disabled" {
		return "errdisabled"
	}
	return s
}

// errDisabled reports whether the switch shut this port ITSELF. Distinct from
// "notconnect" (nothing plugged in) and from "disabled" (an operator typed
// `shutdown`), and the distinction is the whole point: those two are expected
// states, this one is the switch refusing something.
func (p statusPort) errDisabled() bool { return p.Status == "errdisabled" }

// adminDown reports whether an operator (or a previous apply) shut this port.
func (p statusPort) adminDown() bool { return p.Status == "disabled" }

// --- show interfaces status errdisabled ---
//
// The REASON column, which the plain status table does not carry:
//
//	Port      Name          Status       Reason
//	Et50                    errdisabled  xcvr-unsupported
//
// Same pivot as the main table — the Name column is free text — with the reason
// taken as the first word after the status.
var reErrDisabledLine = regexp.MustCompile(
	`(?im)^(\S+)\s+(?:.*?\s+)?err-?disabled\s+(\S+)`)

// parseErrDisabled maps standard-model port name -> the cause EOS gives.
func parseErrDisabled(out string) map[string]string {
	var res = map[string]string{}
	var rows = reErrDisabledLine.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var port, ok = splitEOSName(rows[i][1])
		if !ok {
			continue
		}
		res[fmt.Sprintf("%s%d", StdPrefix, port)] = strings.ToLower(rows[i][2])
	}
	return res
}

// speedMbpsOf reads the Speed column, falling back to the Type column.
//
// The Speed column reads "auto" on a port with nothing in it, which is not a
// speed — so an empty cage gets its capability from the Type word instead, and a
// cage that reports neither gets 0. Zero is reported as zero and never rounded to
// 1G: the faceplate label would then claim a medium nobody read.
func speedMbpsOf(speed, typ string) int {
	var s = strings.ToUpper(strings.TrimSpace(speed))
	switch {
	case strings.HasSuffix(s, "G"):
		var f, err = strconv.ParseFloat(strings.TrimSuffix(s, "G"), 64)
		if err == nil && f > 0 {
			return int(f * 1000)
		}
	case strings.HasSuffix(s, "M"):
		var f, err = strconv.ParseFloat(strings.TrimSuffix(s, "M"), 64)
		if err == nil && f > 0 {
			return int(f)
		}
	}
	var lower = strings.ToLower(typ)
	switch {
	case strings.Contains(lower, "100gbase"):
		return 100000
	case strings.Contains(lower, "40gbase"):
		return 40000
	case strings.Contains(lower, "25gbase"):
		return 25000
	case strings.Contains(lower, "10gbase"):
		return 10000
	case strings.Contains(lower, "1000base"), strings.Contains(lower, "10/100/1000"):
		return 1000
	}
	return 0
}

// isCage reports whether the row describes an optical cage rather than an RJ45
// jack.
//
// A copper PHY always announces itself ("1000BASE-T", "10/100/1000"), plugged or
// not. A cage announces its transceiver when one is present and "Not Present" or
// "Unknown" when it is empty. So: a Type that names copper is copper, and
// everything else is a cage. Deducing it from the port number instead — "the last
// four are SFP" — is what a model catalog does, and it is exactly what reading
// the device exists to avoid.
//
// The 10G threshold is a second, independent signal: nothing on this vendor's
// faceplates delivers 10G or more over an RJ45 in the ranges this connector
// targets, so a port reporting 10G+ is a cage whatever its Type column says.
func isCage(typ string, mbps int) bool {
	var lower = strings.ToLower(strings.TrimSpace(typ))
	// The LADDER form ("10/100/1000") is a built-in copper jack announcing the
	// speeds its PHY serves — it is how Ma1 prints on every train seen here, and
	// the only spelling read as copper.
	if strings.Contains(lower, "/") {
		return false
	}
	// "1000BASE-T" / "10GBASE-T" without the ladder is a TRANSCEIVER family
	// name: on an SFP faceplate it is a copper MODULE seated in a cage. Reading
	// it as a built-in jack split core01.lab's 48-cage run in three — one
	// RJ45 module on Et17 and the drawing showed a copper port between two
	// "SFP+" blocks. The socket does not change nature with what is plugged
	// into it.
	//
	// ASSUMED, not captured: that a fixed-copper EOS platform (720XP, 7010T)
	// prints the ladder form for its built-in jacks the way Ma1 does. No such
	// hardware is in this fleet; the first read of one settles it.
	if strings.Contains(lower, "base-t") || strings.Contains(lower, "baset") {
		return true
	}
	if mbps >= 10000 {
		return true
	}
	// "Not Present" / "Unknown" / "" on a 1G port: an empty cage. A copper jack
	// with nothing in it still reports its PHY.
	return lower != ""
}

// --- show vlan ---

// The VLAN list is read from "show vlan" and NOT from the running-config, and
// this is a deliberate split, for the same reason as on the Cisco connector: a
// VLAN still carrying its default name is not printed in the running
// configuration, so a configuration read would report VLANs that exist as missing
// and this connector would try to create them on every apply.
//
//	VLAN  Name                             Status    Ports
//	----- -------------------------------- --------- -------------------------
//	1     default                          active    Et1, Et2, Et3
//	99    Management                       active    Et49
//	100   VLAN0100                         active
var reVlanRow = regexp.MustCompile(`(?im)^\s*(\d{1,4})\s+(\S+)\s+(active|suspended|act/unsupp|act/lshut)\b`)

func parseVlanTable(out string) []driver.VLAN {
	var res []driver.VLAN
	var seen = map[int]bool{}
	var rows = reVlanRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var id, err = strconv.Atoi(rows[i][1])
		if err != nil || id < 1 || id > 4094 || seen[id] {
			continue
		}
		seen[id] = true
		res = append(res, driver.VLAN{ID: id, Name: rows[i][2]})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	return res
}

// --- show running-config ---

// runConfig: what the current configuration establishes.
type runConfig struct {
	Hostname string
	// DomainName: the device's OWN `ip domain-name`, empty when it declares none.
	//
	// It exists for one reader — the DNS SAN of the gNMI certificate note, which
	// wants `<hostname>.<domain>` and carried a bare hostname before this field.
	// Taken from the device rather than from the site (model.Site.LocalDomain)
	// deliberately: a connector session has no site context, and the certificate
	// has to match what a collector RESOLVES, which is the name the device itself
	// believes it has. Nothing here writes it — see MgmtOpts.DomainName for why
	// `ip domain-name` is not pushed from the site's field either.
	//
	// THE ABSENCE IS CAPTURED, THE PRESENT LINE IS NOT — read on core01
	// (DCS-7050SX-64-R, EOS 4.28.13.1M) on 2026-08-11:
	//
	//	show running-config | include domain   →  (empty)
	//	show hostname                          →  FQDN: core01-lab
	//
	// So this device declares NO domain, its FQDN is its bare hostname, and the
	// empty-DomainName branch — the one that hands the SAN a bare hostname, pinned
	// by TestRunningConfigDomainQualifiesTheSAN — is the branch this fleet's Arista
	// actually takes. That much is observed rather than deduced.
	//
	// WHAT IS STILL UNCAPTURED IS THE RENDERING OF A PRESENT LINE: no device here
	// declares `ip domain-name`, so `reRunDomainName`'s pattern has never been held
	// against a real one. It is the standard EOS spelling and the fallback is the
	// safe direction — a line this parser misses yields the bare hostname, which is
	// what a device with no domain gets anyway — but confirm it on a switch that
	// declares one before relying on a QUALIFIED SAN.
	DomainName string
	Ports      map[string]driver.PortState
	// AllowedAll marks trunks with NO explicit "switchport trunk allowed vlan"
	// line. On EOS, as on IOS, that is not an empty list — it is ALL VLANs, the
	// opposite meaning. Without this distinction a trunk left at its default reads
	// as "carries nothing", every apply issues an "add" that changes nothing (the
	// list was already everything), and the port reports drift for ever.
	AllowedAll map[string]bool
	// MgmtVLAN: the VLAN of the SVI THE SESSION IS RIDING — the one whose address
	// meshcraft dialed — and 0 when that cannot be established.
	//
	// ZERO IS A VALUE, and the readers treat it as one: ApplyPortAdmin spares every
	// port with a link (cf. portadmin.go's header), ApplyPorts protects no VLAN of
	// its own, gnmiVRFFact says nothing, and the API's observedMgmtVLAN reports
	// "not known". That is the honest outcome of an ambiguous configuration, and it
	// is the one [resolveMgmtSVI] returns rather than guessing.
	//
	// IT USED TO MEAN "the last addressed SVI in the text", which is not the same
	// sentence at all — see [resolveMgmtSVI] for the switch that proved it.
	MgmtVLAN int
	// MgmtSVINotes: how the management SVI was chosen, or why it could not be.
	//
	// Said OUT LOUD because the choice is no longer mechanical: a device with
	// several addressed SVIs has an answer that depends on which address meshcraft
	// dialed, and an operator reading "management VLAN unknown" on a switch with
	// six SVIs needs to be told that is what happened, not left to guess that the
	// read failed. Reaches the operator twice over — [configFacts] puts it in the
	// state read, and [runConfig.Mgmt].Notes carries it into ReadMgmtAddressing.
	MgmtSVINotes []string
	// MgmtVRF: the routing instance the management SVI belongs to. Empty means
	// the default instance — EOS prints a "vrf NAME" line inside the interface
	// block only when the SVI has been attached to a named VRF.
	//
	// Read for ONE consumer: the gNMI reachability fact below. The VRF a service
	// listens in and the VRF the management address lives in must agree, and the
	// running configuration is the only place both sides are written down.
	MgmtVRF string
	// GNMIConfigured / GNMITransportVRF: is a "management api gnmi" block
	// present, and which VRF its gRPC transport listens in (empty = default
	// instance).
	//
	// WHY A READER CARES: a gNMI transport in a VRF that does not hold the
	// management address BINDS SUCCESSFULLY AND SERVES NOBODY (cf. GNMIOpts.VRF).
	// That is exactly what happened to core01.lab on 2026-08-13 — the lab's
	// VLANs were moved from VRF-MANAGEMENT into a new VRF-LAN and the transport
	// stayed behind, in an instance with no interfaces left. Every read of the
	// device afterwards (ping, SSH, config absorb) came back healthy while
	// telemetry stayed dark for two days; nothing that read the configuration
	// noticed, because nothing parsed these two lines. Now something does, and
	// configFacts is where the mismatch becomes a sentence.
	GNMIConfigured   bool
	GNMITransportVRF string
	Mgmt             driver.MgmtAddrState
	// MgmtOOB is the address on Management1, when it carries one. Reported
	// SEPARATELY and never as the management path: that port is out-of-band, and
	// claiming administration arrives there would point every guardrail at the
	// wrong interface.
	MgmtOOB string
	// Communities: SNMP strings set, with their right (ro|rw).
	Communities map[string]string
	// TrapReceivers: every "snmp-server host" line, whole.
	TrapReceivers []trapReceiver
	// SNMPTrapEvents: the keywords of the "snmp-server enable traps snmp" line.
	SNMPTrapEvents []string
	// Accounts are the local accounts declared, with the privilege AND the
	// role each "username" line states — see [parseRunUsername].
	Accounts []driver.Account
	// LLDPRun: is LLDP running?
	//
	// DEFAULTS TO TRUE, and this is the inversion that matters against the IOS
	// connector, where the same field defaults to false. EOS runs LLDP out of the
	// box and prints NOTHING in the running configuration for it; the only line
	// that ever appears is "no lldp run", when somebody has turned it off. A
	// reader that started from false — the IOS habit — would report every healthy
	// Arista in the fleet as having no LLDP, and the neighbour screen would carry
	// a warning about a switch that is working perfectly.
	LLDPRun bool
	// UnsupportedXcvr: is "service unsupported-transceiver" configured?
	UnsupportedXcvr bool
	// XcvrRecovery: is "errdisable recovery cause xcvr-unsupported" configured?
	XcvrRecovery bool
	// SSHShut: "management ssh" carries "shutdown". On EOS SSH is on by
	// default, so this is the only way it can be off — and a switch about to be
	// adopted over SSH deserves to have that said before the adoption fails.
	SSHShut bool
	// IPRouting: "ip routing" is configured. Decides nothing here; reported so
	// the gateway note can say whether the default route will be consulted.
	IPRouting bool
}

var (
	reRunHostname = regexp.MustCompile(`(?im)^\s*hostname\s+(\S+)`)
	// `ip domain-name lab.example`. Anchored on `domain-name` and NOT on
	// `domain\s+name`: EOS also carries `ip domain-list`, and a looser pattern
	// would take the first search-list entry for the device's own domain.
	reRunDomainName = regexp.MustCompile(`(?im)^\s*ip\s+domain-name\s+(\S+)`)
	// "interface Ethernet1", "interface Vlan99", "interface Management1"
	reRunIface  = regexp.MustCompile(`(?i)^\s*interface\s+(\S+)\s*$`)
	reRunVlanIf = regexp.MustCompile(`(?i)^vlan\s*(\d+)$`)
	reRunMode   = regexp.MustCompile(`(?i)^\s*switchport\s+mode\s+(access|trunk|dot1q-tunnel)`)
	reRunTrkAl  = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+allowed\s+vlan\s+(?:(add|remove|except)\s+)?([\d,\- ]+)`)
	reRunTrkNa  = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+native\s+vlan\s+(\d+)`)
	reRunAccess = regexp.MustCompile(`(?i)^\s*switchport\s+access\s+vlan\s+(\d+)`)
	// EOS writes the address in PREFIX form. The dotted form is accepted by this
	// reader anyway — a configuration imported from elsewhere, or pasted by an
	// operator, can carry it, and refusing to read what we would never write
	// would lose the address rather than the argument.
	reRunIPPrefix = regexp.MustCompile(`(?i)^\s*ip\s+address\s+(\d+\.\d+\.\d+\.\d+)/(\d{1,2})\b`)
	reRunIPDotted = regexp.MustCompile(`(?i)^\s*ip\s+address\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunIPDHCP   = regexp.MustCompile(`(?i)^\s*ip\s+address\s+dhcp`)
	// "ip route 0.0.0.0/0 192.0.2.1" — with an optional "vrf NAME" that only
	// the default VRF's route may claim as the management gateway.
	reRunRoute = regexp.MustCompile(`(?i)^\s*ip\s+route\s+(?:vrf\s+(\S+)\s+)?0\.0\.0\.0/0\s+(\d+\.\d+\.\d+\.\d+)`)
	// "ip name-server vrf default 1.1.1.1 8.8.8.8"
	reRunDNS   = regexp.MustCompile(`(?i)^\s*ip\s+name-server\s+(?:vrf\s+\S+\s+)?(.+)$`)
	reRunSNMP  = regexp.MustCompile(`(?i)^\s*snmp-server\s+community\s+(\S+)\s+(ro|rw)\b`)
	reRunTrapH = regexp.MustCompile(`(?i)^\s*snmp-server\s+host\s+(\S+)\s*(.*)$`)
	reRunTrapE = regexp.MustCompile(`(?i)^\s*snmp-server\s+enable\s+traps\s+snmp\b(.*)$`)
	// The name AND the rest of the line: "username mcadmin privilege 15 role
	// network-admin secret sha512 $6$…". BOTH the privilege and the role are
	// in that tail, and the role is the one EOS authorisation actually
	// consults — dropping it was how a `network-operator` account looked
	// exactly like an administrator.
	reRunUser = regexp.MustCompile(`(?i)^\s*username\s+(\S+)(.*)$`)
	// The two rank words inside that tail, searched only in the part BEFORE
	// "secret" / "password": the hash that follows is text we are parsing,
	// and it has no business promoting its own account.
	reRunUserPriv = regexp.MustCompile(`(?i)\bprivilege\s+(\d+)\b`)
	reRunUserRole = regexp.MustCompile(`(?i)\brole\s+(\S+)`)
	reRunNoLLD    = regexp.MustCompile(`(?i)^\s*no\s+lldp\s+run\s*$`)
	reRunShut     = regexp.MustCompile(`(?i)^\s*shutdown\s*$`)
	reRunSvcXc    = regexp.MustCompile(`(?i)^\s*service\s+unsupported-transceiver\b`)
	reRunErrXc    = regexp.MustCompile(`(?i)^\s*errdisable\s+recovery\s+cause\s+xcvr-unsupported\s*$`)
	reRunMgSSH    = regexp.MustCompile(`(?i)^\s*management\s+ssh\s*$`)
	reRunIPRtg    = regexp.MustCompile(`(?i)^\s*ip\s+routing\s*$`)
	// "vrf VRF-LAN" — VRF MEMBERSHIP inside a submode (an SVI, or the gNMI
	// transport). Exactly one token then end of line, which is what keeps the
	// top-level "vrf instance VRF-LAN" declaration from matching: that line
	// creates the instance, it does not attach anything to it.
	reRunVRFMember = regexp.MustCompile(`(?i)^\s*vrf\s+(\S+)\s*$`)
	reRunMgGNMI    = regexp.MustCompile(`(?i)^\s*management\s+api\s+gnmi\s*$`)
	// The transport line is matched by SHAPE ("transport grpc <name>") and not
	// by the name "default": the name is free-form (cf. GNMIEnableCmds) and a
	// second, TLS-protected transport beside the default is the documented
	// evolution. The VRF read is per-transport either way — the LAST transport's
	// vrf line wins, which is the acceptable imprecision for a single-transport
	// fleet and still names the block when someone adds a second.
	reRunGNMITrans = regexp.MustCompile(`(?i)^\s*transport\s+grpc\s+\S+\s*$`)
)

// sviAddr is one "ip address" line of an SVI block, kept whole AND parsed.
//
// Both halves are needed: Text is what gets reported (the exact form read off the
// device, prefix included), while IP and Net are what the management SVI is
// CHOSEN by. Net is nil when the prefix length could not be read — a dotted mask
// this parser did not recognise — and such an address can still be compared for
// equality, just not for containment.
type sviAddr struct {
	Text string
	IP   net.IP
	Net  *net.IPNet
}

// parseSVIAddr keeps the address EXACTLY as it was read and adds what comparing
// it requires. A text this parser cannot parse back loses only the comparison —
// it is still reported, because what the device says is never dropped in favour
// of what this code can handle.
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

// sviCandidate is one "interface VlanN" block that could be carrying
// administration: not shut down, and declaring an address or a DHCP lease.
//
// Addrs is in CONFIGURATION ORDER, primary first — EOS prints the primary
// address before every "secondary" — and that order is used for ONE thing
// only: which address to report when none of them is the one meshcraft dialed.
// It never decides which SVI wins.
type sviCandidate struct {
	VLAN int
	// Mode: static or dhcp (driver.MgmtAddrMode*).
	Mode string
	// Addrs is empty under DHCP, where the running configuration carries the
	// intent and not the lease.
	Addrs []sviAddr
	// Notes: what this block's own parsing could not express, promoted to
	// [runConfig.Mgmt].Notes only if this SVI is the one that wins.
	Notes []string
}

// primary is the address to report for this SVI when no comparison singled one
// out: the FIRST one the block declares, which on EOS is the primary and not a
// "secondary" prefix.
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
// Until this function existed, every non-shutdown SVI carrying an address simply
// overwrote the management fields, so THE LAST BLOCK OF THE RUNNING CONFIG WON.
// core01-arena (DCS-7050SX-64-R, EOS 4.28.13M) prints its SVIs in the order
// Vlan42, Vlan97, Vlan98, Vlan99, Vlan100, Vlan3000 — and Vlan3000 is a /31
// point-to-point transit link to a MikroTik. meshcraft therefore filed the
// management VLAN as 3000 and the management address as 10.255.255.1/31, while
// the address it had itself dialed to read all of that was 192.0.2.1/16 on
// Vlan99. Two consequences, and the second is the one that matters:
//
//   - the device was reported with no management path, so every addressing
//     gesture was refused. Prudent by accident, on a premise that was false;
//   - [gnmiVRFFact] compares the gNMI transport's VRF against the VRF OF THIS
//     SVI. On core01-arena both VRFs happened to be VRF-LAN, so the guardrail
//     stayed silent out of luck. On a switch whose SVIs straddle several
//     instances the same mistake invents a disagreement, or hides a real one —
//     and a silent gNMI transport is exactly the two-day outage that fact was
//     written to break.
//
// # The order of the tests, and why it stops rather than guesses
//
//  1. THE SVI THAT CARRIES THE DIALED ADDRESS. The session arrived somewhere;
//     that somewhere is written in the configuration. Exact, order-independent,
//     and the case every correctly-read switch lands in.
//  2. THE SVI WHOSE SUBNET CONTAINS IT, longest prefix first — the way a routing
//     table resolves the same question. This is not padding: on a core pair the
//     address meshcraft dials is often the VRRP virtual address, which no SVI
//     carries while exactly one subnet contains it.
//  3. THE ONLY CANDIDATE, when the configuration offers one. No ambiguity exists
//     to resolve, and this is the shape of most of the fleet — a single
//     management SVI on an L2 access switch. Said out loud when the dialed
//     address is not the one it carries, because then administration is arriving
//     by some other door (Management1, the console, a NAT) and the SVI is a
//     deduction rather than an observation.
//  4. OTHERWISE, UNKNOWN. Falling back to "the last one" is the bug; falling
//     back to "the first one" is the same bug reading the file backwards. Every
//     reader of [runConfig.MgmtVLAN] treats 0 as "not known" and fails safe on
//     it, so the honest answer is also the harmless one.
//
// Returns the winning candidate, the address to report for it, the sentences the
// operator needs, and whether anything was chosen at all.
func resolveMgmtSVI(cands []sviCandidate, dialed string) (sviCandidate, string, []string, bool) {
	var none sviCandidate
	if len(cands) == 0 {
		// Nothing to choose between, and nothing to say: ReadMgmtAddressing
		// already writes the "no SVI carries an address" sentence, with the
		// Management1 explanation when there is one.
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
					"cannot be, and Vlan%d — the lowest — is the one taken, so two reads of "+
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
				"no SVI carries %s, the address meshcraft reaches this switch at, but Vlan%d's "+
					"subnet (%s) contains it — a virtual address (VRRP) in front of this SVI "+
					"is the ordinary reason. Administration is read as arriving on Vlan%d",
				want, cands[best].VLAN, cands[best].primary(), cands[best].VLAN)}, true
		}
	}

	// 3 — the only candidate there is.
	if len(cands) == 1 {
		var notes []string
		if want != nil && len(cands[0].Addrs) > 0 {
			notes = []string{fmt.Sprintf(
				"Vlan%d is the only SVI that could carry administration, so it is the one "+
					"reported — but it carries %s while meshcraft reaches this switch at %s. "+
					"Administration is arriving by some other door (the out-of-band port, the "+
					"console, or an address translation), and this VLAN is a deduction rather "+
					"than something read",
				cands[0].VLAN, cands[0].primary(), want)}
		}
		return cands[0], cands[0].primary(), notes, true
	}

	// 4 — several candidates and nothing to tell them apart.
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

// sviList spells a set of SVIs the way an operator would read them back off the
// device: "Vlan99 (192.0.2.1/16), Vlan3000 (10.255.255.1/31)".
func sviList(cands []sviCandidate) string {
	var parts []string
	var c sviCandidate
	for _, c = range cands {
		var addr = c.primary()
		if addr == "" {
			addr = "DHCP"
		}
		parts = append(parts, fmt.Sprintf("Vlan%d (%s)", c.VLAN, addr))
	}
	return strings.Join(parts, ", ")
}

// parseRunningConfig reads "show running-config".
//
// dialed is the address the session arrived on ([Client.host]), and it is a
// PARAMETER rather than something deduced here because it is the only evidence
// that says which SVI carries administration — cf. [resolveMgmtSVI]. Empty is
// allowed and means "not known to this caller"; it costs the management VLAN on
// a device with several addressed SVIs, and nothing else.
func parseRunningConfig(out string, dialed string) runConfig {
	var rc = runConfig{
		Ports:       map[string]driver.PortState{},
		AllowedAll:  map[string]bool{},
		Communities: map[string]string{},
		// EOS runs LLDP out of the box; only "no lldp run" turns it off.
		LLDPRun: true,
	}
	var curPort = "" // standard-model name of the interface block being read
	// A trunk port keeps its old "switchport access vlan" line in the running
	// config, inert — and the trunk's PVID is the NATIVE vlan, not that access
	// vlan. Parsing them into the same field lets the leftover access line win and
	// produces permanent false drift. So the two are tracked apart and resolved by
	// the FINAL mode in closeBlock.
	var accessVLAN = map[string]int{}
	var nativeVLAN = map[string]int{}
	var curSVI = 0 // "interface VlanN" block being read
	var inMgmtIface = false
	var inMgmtSSH = false
	var inMgmtGNMI = false
	var inGNMITransport = false
	// sviVRF: VRF membership per SVI, resolved against MgmtVLAN once the whole
	// configuration is read. Deferred rather than written inline because EOS
	// prints the "vrf" line BEFORE "ip address" in the block — the moment the
	// vrf line is read, nothing yet says whether this SVI is the management one.
	var sviVRF = map[int]string{}
	var sawAllowed = false
	// Every SVI block is COLLECTED and the management one chosen afterwards, by
	// [resolveMgmtSVI]. Nothing about the choice may depend on the order the blocks
	// arrive in — that dependency IS the bug this shape replaces.
	//
	// sviShut is kept apart from the candidate list for the same reason at block
	// scale: EOS prints "shutdown" before "ip address", but a configuration
	// that carries them the other way round must not read as a live SVI. The flag
	// is applied when the list is filtered, not as each address line goes by.
	var sviCands = map[int]*sviCandidate{}
	var sviOrder []int
	var sviShut = map[int]bool{}
	var sviOf = func(vlan int) *sviCandidate {
		var c = sviCands[vlan]
		if c == nil {
			c = &sviCandidate{VLAN: vlan}
			sviCands[vlan] = c
			sviOrder = append(sviOrder, vlan)
		}
		return c
	}

	var closeBlock = func() {
		if curPort != "" {
			var p = rc.Ports[curPort]
			if p.Mode == "trunk" && !sawAllowed {
				rc.AllowedAll[curPort] = true
			}
			// PVID by FINAL mode: a trunk takes its native vlan (default 1 when the
			// line is absent, because 1 is EOS's default and is not printed);
			// anything else takes its access vlan.
			if p.Mode == "trunk" {
				var n, ok = nativeVLAN[curPort]
				if ok {
					p.PVID = n
				} else {
					p.PVID = 1
				}
				// The device's allowed list always CONTAINS the native VLAN — EOS
				// requires it — but meshcraft's model carries the native as PVID,
				// separately from the tagged set. Leaving it in Tagged makes an
				// exactly-applied trunk drift for ever against an intent that never
				// lists its own native. Remove it, mirroring exactAllowed on the
				// write side (which adds it).
				p.Tagged = removeInt(p.Tagged, p.PVID)
			} else {
				var a, ok = accessVLAN[curPort]
				if ok {
					p.PVID = a
				}
				// An ACCESS (or unset) port carries NO tagged VLANs. EOS keeps a
				// stale "switchport trunk allowed vlan" line in the config after a
				// trunk→access switch — inert, but the parser would otherwise read it
				// as tagged membership and drift the port for ever.
				p.Tagged = nil
			}
			if p.PVID == 0 {
				p.PVID = 1
			}
			rc.Ports[curPort] = p
		}
		curPort, curSVI, sawAllowed = "", 0, false
		inMgmtIface, inMgmtSSH = false, false
		inMgmtGNMI, inGNMITransport = false, false
	}

	var line string
	for _, line = range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		var trimmed = strings.TrimSpace(line)
		if trimmed == "!" || trimmed == "end" {
			closeBlock()
			continue
		}

		var m []string
		if m = reRunIface.FindStringSubmatch(line); m != nil {
			closeBlock()
			var what = strings.TrimSpace(m[1])
			var vm = reRunVlanIf.FindStringSubmatch(what)
			if vm != nil {
				curSVI, _ = strconv.Atoi(vm[1])
				continue
			}
			if IsManagementPort(what) {
				inMgmtIface = true
				continue
			}
			var std, ok = (&Driver{}).StandardPortName(what)
			if !ok {
				// A breakout lane, a port-channel, a loopback: block ignored. The
				// numbering header says why, and inspect.go's note is what tells an
				// operator a port they can see is not one meshcraft carries.
				continue
			}
			curPort = std
			var _, seen = rc.Ports[curPort]
			if !seen {
				// A port whose block says nothing is at its factory default: access
				// on VLAN 1.
				rc.Ports[curPort] = driver.PortState{Mode: "access", PVID: 1}
			}
			continue
		}
		if reRunMgSSH.MatchString(line) {
			closeBlock()
			inMgmtSSH = true
			continue
		}
		if reRunMgGNMI.MatchString(line) {
			closeBlock()
			inMgmtGNMI = true
			rc.GNMIConfigured = true
			continue
		}

		if inMgmtSSH {
			if reRunShut.MatchString(line) {
				rc.SSHShut = true
			}
			continue
		}

		if inMgmtGNMI {
			// The "vrf" line belongs to the TRANSPORT submode, not to
			// "management api gnmi" itself, so it only counts once a transport
			// line has been seen — the same nesting GNMIEnableCmds writes.
			if reRunGNMITrans.MatchString(line) {
				inGNMITransport = true
				continue
			}
			if inGNMITransport {
				var vm = reRunVRFMember.FindStringSubmatch(line)
				if vm != nil {
					rc.GNMITransportVRF = vm[1]
				}
			}
			continue
		}

		if inMgmtIface {
			// The out-of-band address, recorded but NEVER promoted to the
			// management path (cf. runConfig.MgmtOOB).
			if m = reRunIPPrefix.FindStringSubmatch(line); m != nil {
				rc.MgmtOOB = m[1] + "/" + m[2]
			}
			continue
		}

		if curPort != "" {
			var p = rc.Ports[curPort]
			switch {
			case reRunMode.MatchString(line):
				var mode = strings.ToLower(reRunMode.FindStringSubmatch(line)[1])
				if mode == "dot1q-tunnel" {
					// Not a mode the standard model carries. Left ALONE rather than
					// approximated as a trunk: apply would then rewrite a tunnel port
					// into something its operator never asked for.
					mode = ""
				}
				p.Mode = mode
			case reRunTrkAl.MatchString(line):
				var mm = reRunTrkAl.FindStringSubmatch(line)
				var ids, err = expandVlanList(mm[2])
				if err == nil && !strings.EqualFold(mm[1], "remove") &&
					!strings.EqualFold(mm[1], "except") {
					p.Tagged = mergeSorted(p.Tagged, ids)
					sawAllowed = true
				}
			case reRunTrkNa.MatchString(line):
				nativeVLAN[curPort], _ = strconv.Atoi(reRunTrkNa.FindStringSubmatch(line)[1])
			case reRunAccess.MatchString(line):
				accessVLAN[curPort], _ = strconv.Atoi(reRunAccess.FindStringSubmatch(line)[1])
				if p.Mode == "" {
					p.Mode = "access"
				}
			}
			// PVID is resolved in closeBlock, from the FINAL mode.
			rc.Ports[curPort] = p
			continue
		}

		if curSVI > 0 {
			if reRunShut.MatchString(line) {
				// A SHUTDOWN SVI carries an address that answers nothing: it is not
				// the management path, and claiming it is would send the probe to a
				// dead address. Recorded per VLAN so the line's POSITION in the block
				// does not decide (cf. sviShut above).
				sviShut[curSVI] = true
				continue
			}
			if m = reRunVRFMember.FindStringSubmatch(line); m != nil {
				sviVRF[curSVI] = m[1]
				continue
			}
			if m = reRunIPPrefix.FindStringSubmatch(line); m != nil {
				var c = sviOf(curSVI)
				c.Mode = driver.MgmtAddrModeStatic
				c.Addrs = append(c.Addrs, parseSVIAddr(m[1]+"/"+m[2]))
				continue
			}
			if m = reRunIPDotted.FindStringSubmatch(line); m != nil {
				var c = sviOf(curSVI)
				c.Mode = driver.MgmtAddrModeStatic
				var bits = PrefixFromMask(m[2])
				if bits > 0 {
					c.Addrs = append(c.Addrs, parseSVIAddr(fmt.Sprintf("%s/%d", m[1], bits)))
				} else {
					// The bare address still compares for EQUALITY against the dialed
					// one, which is the test that matters; only containment is lost.
					c.Addrs = append(c.Addrs, parseSVIAddr(m[1]))
					c.Notes = append(c.Notes,
						"unreadable network mask "+m[2]+": the prefix length is not reported rather than assumed")
				}
				continue
			}
			if reRunIPDHCP.MatchString(line) {
				var c = sviOf(curSVI)
				// A static address in the same block always wins the MODE: it is the
				// one meshcraft wrote, and "ip address dhcp" left behind it is the
				// line that is inert.
				if c.Mode == "" {
					c.Mode = driver.MgmtAddrModeDHCP
				}
				continue
			}
			continue
		}

		switch {
		case reRunHostname.MatchString(line):
			rc.Hostname = reRunHostname.FindStringSubmatch(line)[1]
		case reRunDomainName.MatchString(line):
			rc.DomainName = reRunDomainName.FindStringSubmatch(line)[1]
		case reRunRoute.MatchString(line):
			var mm = reRunRoute.FindStringSubmatch(line)
			// A default route in a NON-default VRF is somebody else's routing, not
			// the management path: taking it would report a gateway that management
			// traffic never uses.
			if mm[1] == "" || strings.EqualFold(mm[1], "default") {
				if rc.Mgmt.Gateway == "" {
					rc.Mgmt.Gateway = mm[2]
				}
			}
		case reRunDNS.MatchString(line):
			// EOS puts every resolver on ONE line.
			var srv string
			for _, srv = range strings.Fields(reRunDNS.FindStringSubmatch(line)[1]) {
				rc.Mgmt.DNS = append(rc.Mgmt.DNS, srv)
			}
		case reRunSNMP.MatchString(line):
			var mm = reRunSNMP.FindStringSubmatch(line)
			rc.Communities[mm[1]] = strings.ToLower(mm[2])
		case reRunTrapH.MatchString(line):
			var mm = reRunTrapH.FindStringSubmatch(line)
			rc.TrapReceivers = append(rc.TrapReceivers, parseTrapReceiver(mm[1], mm[2]))
		case reRunTrapE.MatchString(line):
			var w string
			for _, w = range strings.Fields(reRunTrapE.FindStringSubmatch(line)[1]) {
				rc.SNMPTrapEvents = append(rc.SNMPTrapEvents, strings.ToLower(w))
			}
		case reRunUser.MatchString(line):
			rc.Accounts = append(rc.Accounts, parseRunUsername(line))
		case reRunNoLLD.MatchString(line):
			rc.LLDPRun = false
		case reRunSvcXc.MatchString(line):
			rc.UnsupportedXcvr = true
		case reRunErrXc.MatchString(line):
			rc.XcvrRecovery = true
		case reRunIPRtg.MatchString(line):
			rc.IPRouting = true
		}
	}
	closeBlock()

	// The management SVI is chosen HERE, once the whole configuration is in hand
	// and every candidate is known — never while the blocks go by, where "the one
	// in front of me" is indistinguishable from "the right one".
	var live []sviCandidate
	var vlan int
	for _, vlan = range sviOrder {
		if sviShut[vlan] {
			continue
		}
		live = append(live, *sviCands[vlan])
	}
	var won, wonAddr, sviNotes, ok = resolveMgmtSVI(live, dialed)
	rc.MgmtSVINotes = sviNotes
	if ok {
		rc.MgmtVLAN = won.VLAN
		rc.Mgmt.Mode = won.Mode
		rc.Mgmt.Address = wonAddr
		rc.Mgmt.Interface = fmt.Sprintf("Vlan%d", won.VLAN)
		rc.Mgmt.Notes = append(rc.Mgmt.Notes, won.Notes...)
	}
	// The same sentences reach the operator through the state read (configFacts)
	// and through ReadMgmtAddressing; neither screen shows the other.
	rc.Mgmt.Notes = append(rc.Mgmt.Notes, sviNotes...)

	// Resolved HERE and not where the vrf line was read: only now is it known
	// which SVI won the management role (cf. sviVRF above).
	if rc.MgmtVLAN > 0 {
		rc.MgmtVRF = sviVRF[rc.MgmtVLAN]
	}

	rc.Accounts = dedupeAccounts(rc.Accounts)
	return rc
}

// expandVlanList accepts "99", "99,100", "10-12", and the spaces EOS wraps long
// lists with.
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

// mergeSorted merges two VLAN lists, SORTED — the PortState contract requires it
// so two reads produce the same state.
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

// parseRunUsername turns one "username …" line into an account, carrying
// BOTH words EOS puts on it.
//
// THE ROLE IS THE ONE THAT DECIDES. This connector's own [accountCmds] writes
// "privilege 15 role network-admin" precisely because privilege alone yields
// an account that logs in and can configure nothing — so an enumeration that
// reported the number and dropped the role would be reporting the weaker half.
// Both travel: the number in [driver.Account.Privilege], the word in
// [driver.Account.Role].
//
// THE VERDICT FOLLOWS THE ROLE WHEN THERE IS ONE, and the number otherwise. A
// line that states neither reads UNKNOWN — EOS's default is documented as
// privilege 1, but this parser does not substitute a default for a fact, since
// "the configuration did not say" is what an audit needs to be able to see.
func parseRunUsername(line string) driver.Account {
	var m = reRunUser.FindStringSubmatch(line)
	if m == nil {
		return driver.Account{}
	}
	var head = m[2]
	var cut = firstKeyword(head, " secret ", " password ")
	if cut >= 0 {
		head = head[:cut]
	}
	var priv = driver.PrivilegeUnknown
	var p = reRunUserPriv.FindStringSubmatch(head)
	if p != nil {
		var n, err = strconv.Atoi(p[1])
		if err == nil {
			priv = n
		}
	}
	var out = driver.AccountAtLevel(m[1], priv, "show running-config")
	var r = reRunUserRole.FindStringSubmatch(head)
	if r != nil {
		out.Role = r[1]
		out.Authority = roleAuthority(r[1], out.Authority)
	}
	return out
}

// roleAuthority ranks an EOS role WITHOUT reading `show users roles` or the
// role definitions.
//
// The two BUILT-IN roles are the whole answer on a switch nobody customised:
// `network-admin` permits everything, `network-operator` permits `show` and
// nothing else. Any OTHER role is operator-defined — its rules live in a
// "role NAME" block this line does not carry — so it falls back to whatever
// the numeric privilege said, which is [driver.AuthorityUnknown] when the line
// carried no number either. Ranking an unknown role by its name would be a
// guess in the dangerous direction: a role called `readonly` that grants
// everything is exactly the account an audit must not miss.
func roleAuthority(role string, fromLevel driver.Authority) driver.Authority {
	switch strings.ToLower(role) {
	case "network-admin":
		return driver.AuthorityFull
	case "network-operator":
		return driver.AuthorityLimited
	default:
		return fromLevel
	}
}

// firstKeyword returns the offset of whichever lower-cased keyword comes first
// in a line whose case EOS does not guarantee, or -1 when none is there.
func firstKeyword(line string, keywords ...string) int {
	var lower = strings.ToLower(line)
	var best = -1
	var k string
	for _, k = range keywords {
		var i = strings.Index(lower, k)
		if i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// dedupeAccounts keeps one row per name, MERGING what the rows say: a
// configuration carrying two lines for the same account must not lose the one
// that states the rank just because it came second.
func dedupeAccounts(in []driver.Account) []driver.Account {
	var at = map[string]int{}
	var out []driver.Account
	var a driver.Account
	for _, a = range in {
		var key = strings.ToLower(a.Name)
		var i, seen = at[key]
		if !seen {
			at[key] = len(out)
			out = append(out, a)
			continue
		}
		if out[i].Privilege == driver.PrivilegeUnknown && a.Privilege != driver.PrivilegeUnknown {
			out[i].Privilege = a.Privilege
			out[i].Authority = a.Authority
		}
		if out[i].Role == "" && a.Role != "" {
			out[i].Role = a.Role
			out[i].Authority = a.Authority
		}
	}
	return out
}

// --- snmp-server host ---

// trapReceiver is one "snmp-server host" line, read whole.
//
// The line's grammar, as the running configuration prints it back:
//
//	snmp-server host <addr> [vrf <name>] [informs|traps]
//	                 [version {1|2c|3 [auth|noauth|priv]}]
//	                 <community> [udp-port <port>]
//
// The host token alone is not enough to decide anything: the entry key is
// host + community (+ port), so two lines can name the same collector and only
// one of them carries the community the collector accepts.
type trapReceiver struct {
	Host string
	// Informs: the line declares informs rather than traps. An inform expects an
	// acknowledgement the collector does not send; such a line retries into the
	// void and is never one this connector wrote.
	Informs bool
	// Version: "1", "2c" or "3" — empty when the line names none.
	Version string
	// Community — under v3, the user name.
	Community string
	// Port: 0 means the default 162 (EOS omits "udp-port" at the default).
	Port int
}

// parseTrapReceiver decomposes the part of the line after the address.
// Notification-type keywords at the tail are deliberately not kept: an empty tail
// means "every enabled notification type", and comparing tails would make an
// entry that sends MORE than asked read as divergent.
func parseTrapReceiver(host, rest string) trapReceiver {
	var r = trapReceiver{Host: host}
	var toks = strings.Fields(rest)
	var i = 0
	for i < len(toks) {
		switch strings.ToLower(toks[i]) {
		case "vrf":
			i += 2
		case "informs":
			r.Informs = true
			i++
		case "traps":
			i++
		case "version":
			if i+1 >= len(toks) {
				return r
			}
			r.Version = strings.ToLower(toks[i+1])
			i += 2
			if r.Version == "3" && i < len(toks) {
				switch strings.ToLower(toks[i]) {
				case "auth", "noauth", "priv":
					i++
				}
			}
		default:
			// The first bare word is the community (v3: the user).
			r.Community = toks[i]
			i++
			if i+1 < len(toks) && strings.EqualFold(toks[i], "udp-port") {
				r.Port, _ = strconv.Atoi(toks[i+1])
			}
			return r
		}
	}
	return r
}

// --- show snmp user ---

// THE READ THAT LIFTED THE SNMPv3 REFUSAL, and the one line in it that matters
// most is the one nobody would have thought to write a parser for.
//
// # What is CAPTURED
//
// core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M, 2026-08-10. A temporary user
// was created at the CLI and "show snmp user" printed exactly this, byte for
// byte (testdata/show_snmp_user.txt, and the user was removed afterwards — the
// command reads empty again):
//
//	User name      : mcprobev3
//	Security model : v3
//	Engine ID      : f5717f28993a9db76500
//	Authentication : SHA
//	Privacy        : AES-128
//	Group          : meshprobe (not configured)
//
// The KEYS are not in there and never will be, which is the half of the original
// objection that stands for ever (cf. snmp.go). The NAME, the security model and
// BOTH algorithm names are, and that is what a verifying re-read needs: it would
// catch a device that accepted "auth sha … priv aes …" and created something
// else, which is the failure this connector re-reads for everywhere else.
//
// # "(not configured)" — the trap this parser exists for
//
// EOS accepts a user naming a group that DOES NOT EXIST, keeps the reference, and
// says so in the read with that suffix. A user in a non-existent group is
// authorised for nothing: it authenticates and every walk comes back empty. A
// connector that read the name and ignored the suffix would report a v3 identity
// posed, with supervision silently dead — the exact silent-success class this
// codebase re-reads to avoid. [snmpUser.GroupConfigured] is therefore a FIELD and
// not a detail of the name, and snmp.go's verification refuses without it.
//
// # What is DEDUCED, said out loud
//
// Only ONE user was in the capture, and only in the not-configured state:
//
//   - the separator between two user blocks is not known. This parser does not
//     need one — it opens a new record on every "User name" line, so blocks
//     divided by a blank line, by a rule, or by nothing at all all read the same;
//   - the shape of a CONFIGURED group is deduced to be the bare name, the suffix
//     being an annotation of absence. If EOS in fact prints some OTHER suffix
//     there, it lands inside [snmpUser.Group] and the name comparison in snmp.go
//     fails — the deduction's failure mode is a REFUSED write, never a false
//     confirmation, which is why it is safe to ship ahead of a capture;
//   - the label spelling of fields this capture did not carry is unknown, and
//     unknown labels are ignored rather than guessed at.
type snmpUser struct {
	Name string
	// SecurityModel is the word as printed ("v3"). Compared, not interpreted:
	// a user that came back "v1" is not the user that was asked for.
	SecurityModel string
	// EngineID is the localised engine the keys were derived against. Read for
	// the transcript — the keys themselves are invisible, so the engine is the
	// only thing about them that can be shown at all.
	EngineID string
	// Auth / Priv are the algorithm names AS THE DEVICE PRINTS THEM ("SHA",
	// "AES-128"), never normalised here. snmpv3.Normalize is applied at the
	// comparison, so the transcript can still quote what the switch said.
	Auth string
	Priv string
	// Group is the group NAME, with any "(not configured)" suffix removed.
	Group string
	// GroupConfigured is FALSE when EOS annotated the group as absent. See the
	// section header: without it a v3 write reports success on an identity that
	// authorises nothing.
	GroupConfigured bool
}

// notConfiguredMark is EOS's own wording, lower-cased for comparison. Pinned as a
// constant because it is a CAPTURED string and the verification turns on it.
const notConfiguredMark = "(not configured)"

// reSNMPField splits "Label : value". The label is bounded to letters and
// spaces so that a VALUE containing a colon — an engine ID printed in colon-
// separated bytes, which this capture's is not — cannot be mistaken for a second
// label. The value keeps everything after the first colon and is trimmed, which
// is also what strips the CR of the CRLF the wire delivers.
//
// SHARED by "show snmp user" and "show snmp group": both captures print the
// same padded "Label : value" shape, so one expression reads both. The bound on
// the label earns its keep a second time there — the group capture's read view
// value is "<no read view specified (default: all included)>", which carries a
// colon of its own, and neither "<" nor "(" may appear in a label.
var reSNMPField = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z ]*[A-Za-z])\s*:\s*(.*)$`)

// parseSNMPUsers reads "show snmp user". An empty output is NO USERS, which is
// a legitimate state and not a parse failure: it is what the command printed
// again once the probe user of 2026-08-10 was removed.
func parseSNMPUsers(out string) []snmpUser {
	var users []snmpUser
	var line string
	for _, line = range strings.Split(out, "\n") {
		var m = reSNMPField.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// Collapse the label's internal padding: "Security model" is separated
		// from its colon by a run of spaces the width of the widest label, and
		// nothing says that width is stable across releases.
		var label = strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
		var value = strings.TrimSpace(m[2])
		if label == "user name" {
			users = append(users, snmpUser{Name: value})
			continue
		}
		if len(users) == 0 {
			// A field before any "User name": a banner, a header, an echoed
			// command line. It belongs to no user and is dropped rather than
			// attached to the first one that turns up later.
			continue
		}
		var u = &users[len(users)-1]
		switch label {
		case "security model":
			u.SecurityModel = value
		case "engine id":
			u.EngineID = value
		case "authentication":
			u.Auth = value
		case "privacy":
			u.Priv = value
		case "group":
			u.Group, u.GroupConfigured = splitSNMPUserGroup(value)
		}
	}
	return users
}

// splitSNMPUserGroup separates the group name from EOS's absence marker.
//
// A group line with the marker and NO name ("Group : (not configured)") yields
// an empty name and false, which snmp.go refuses like any other mismatch.
func splitSNMPUserGroup(value string) (name string, configured bool) {
	var i = strings.Index(strings.ToLower(value), notConfiguredMark)
	if i >= 0 {
		return strings.TrimSpace(value[:i]), false
	}
	return value, true
}

// findSNMPUser looks a user up by name. The name is compared EXACTLY: SNMPv3
// user names are case-sensitive on the wire, and a case-insensitive match here
// would confirm a user the collector cannot then authenticate as.
func findSNMPUser(users []snmpUser, name string) (snmpUser, bool) {
	var u snmpUser
	for _, u = range users {
		if u.Name == name {
			return u, true
		}
	}
	return snmpUser{}, false
}

// --- show snmp group ---

// THE READ THAT TURNS A "Skipped" LINE INTO A CHECK.
//
// snmp.go's header used to end "“show snmp group” has never been read on an
// Arista here; when it has, that Skipped line becomes a check". It has been read,
// and this parser is the first half of that check.
//
// # What is CAPTURED
//
// core01.lab, DCS-7050SX-64-R, EOS 4.28.13.1M, 2026-08-10. The connector's own
// DEDUCED group line — "snmp-server group meshprobeb v3 priv", no view named —
// was sent through the shipped applier, and "show snmp group" printed exactly
// this, byte for byte (testdata/show_snmp_group.txt):
//
//	Group name     : meshprobeb
//	Security model : v3 priv
//	Read view      : <no read view specified (default: all included)>
//	Write view     : <no write view specified>
//	Notify view    : <no notify view specified>
//
// Four field labels; the security model printed as MODEL AND LEVEL in one field
// ("v3 priv", the same two words the group line was written with); and — the
// line the whole check turns on — the platform stating in its own words what an
// unset read view falls back to: ALL INCLUDED. A group written with no view
// therefore DOES authorise reads. That is precisely what the Skipped line could
// only hedge about, and it is now assertable.
//
// # The angle brackets are the platform saying "unset", and they are the parse
//
// "<no read view specified (default: all included)>" is not a view name, it is
// the ABSENCE of one annotated with what the absence means. The write and notify
// views carry the same bracketed shape WITHOUT any such annotation, and that
// contrast is the substance of this parser: an unset read view falls back to
// everything, an unset write view falls back to nothing. A parser that took the
// bracketed text for a view NAME would report a group configured for a view called
// "<no read view specified…" — a name that can be neither found nor checked, and
// a group that authorises nothing would then read exactly like one that authorises
// everything.
//
// # What is DEDUCED, said out loud
//
// One group was captured, in the one state this connector ever writes:
//
//   - THE SHAPE OF AN EXPLICITLY NAMED VIEW IS NOT CAPTURED. It is deduced to be
//     the bare view name — the bracketed forms being the annotation of absence —
//     by the same reasoning that reads a configured group name off "show snmp
//     user". Nothing here ever writes a view name, so that shape can only come
//     from a group somebody else configured; [viewAuthorityNamed] is what this
//     parser says about it, and verifySNMPv3Group in snmp.go argues what the
//     connector may conclude from it and why that is neither a yes nor a refusal;
//   - the separator between two group blocks is unknown, and this parser needs
//     none: it opens a record on every "Group name" line, exactly as
//     [parseSNMPUsers] does;
//   - that a group name may appear MORE THAN ONCE — one block per security model —
//     is deduced from the model being printed as a FIELD of the block rather than
//     as a list. [findSNMPGroup] therefore matches on NAME AND MODEL together:
//     answering a question about a v3 group from a v2c block would confirm the read
//     authority of an access entry the v3 user never uses;
//   - the label spelling of fields this capture did not carry is unknown, and
//     unknown labels are ignored rather than guessed at.
type snmpGroup struct {
	Name string
	// SecurityModel is the field as printed — model AND level, "v3 priv".
	// Compared, not interpreted, like [snmpUser.SecurityModel].
	SecurityModel string
	// Read / Write / Notify are the three view fields. Only Read decides whether a
	// polling identity works; Write and Notify are parsed because the capture
	// carries them and a field silently dropped is a field nobody notices changing.
	Read   snmpView
	Write  snmpView
	Notify snmpView
}

// viewAuthority answers ONE question about a view field: what the group may do
// through it. THREE values, because the honest answer has three cases and
// collapsing the third into either of the other two is the whole trap.
type viewAuthority int

const (
	// viewAuthorityNone — the device said no view is set and named no default that
	// would supply one. Through this field the group may do NOTHING. It is what the
	// captured WRITE view says, which is the state a read-only identity wants.
	viewAuthorityNone viewAuthority = iota
	// viewAuthorityAll — the device said no view is set AND that the default is all
	// included. CAPTURED, on the read view, and the reason the check can be a check.
	viewAuthorityAll
	// viewAuthorityNamed — a view IS named. What that view CONTAINS is not knowable
	// from this command: resolving it would take "show snmp view", which nobody
	// has run on an Arista here. Neither a yes nor a no, and it must not be rounded
	// into one.
	viewAuthorityNamed
)

// snmpView is one view field of a group.
type snmpView struct {
	// Raw is the value exactly as the device printed it, brackets and all. Kept for
	// the transcript and the error messages: on a command read once, the wording the
	// switch used is the next survey's evidence.
	Raw string
	// Name is the view's name when one is NAMED, and empty for every bracketed
	// "unset" form. DEDUCED shape — see the section header.
	Name string
	// Authority is what the group may do through this field. The three cases are
	// three different answers — see [viewAuthority].
	Authority viewAuthority
}

// defaultAllIncludedMark is EOS's own annotation, lower-cased and space-collapsed
// for comparison. Pinned as a constant because it is a CAPTURED string and the
// whole check turns on it: it is the platform saying that an unset read view
// authorises everything.
const defaultAllIncludedMark = "default: all included"

// parseSNMPGroups reads "show snmp group". An output with no "Group name" line
// is NO GROUPS — a legitimate state on a switch that has none, and not a parse
// failure, the same rule [parseSNMPUsers] follows.
func parseSNMPGroups(out string) []snmpGroup {
	var groups []snmpGroup
	var line string
	for _, line = range strings.Split(out, "\n") {
		var m = reSNMPField.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		// Collapse the label's internal padding, for the reason [parseSNMPUsers]
		// gives: the column width is the widest label's and nothing says it is
		// stable across releases.
		var label = strings.ToLower(strings.Join(strings.Fields(m[1]), " "))
		var value = strings.TrimSpace(m[2])
		if label == "group name" {
			groups = append(groups, snmpGroup{Name: value})
			continue
		}
		if len(groups) == 0 {
			// A field before any "Group name": a banner, an echoed command line.
			// It belongs to no group and is dropped rather than attached to the
			// first one that turns up later.
			continue
		}
		var g = &groups[len(groups)-1]
		switch label {
		case "security model":
			g.SecurityModel = value
		case "read view":
			g.Read = parseSNMPView(value)
		case "write view":
			g.Write = parseSNMPView(value)
		case "notify view":
			g.Notify = parseSNMPView(value)
		}
	}
	return groups
}

// parseSNMPView classifies one view field.
//
// The bracketed forms are the platform's "unset", and only ONE of them — the
// captured "(default: all included)" — says the absence still authorises
// something. Every OTHER bracketed wording, including one this capture never
// showed, is read as authorising nothing.
//
// That last rule is deliberate and its failure mode is chosen: if a future release
// annotates the default differently, this reads a permissive group as authorising
// nothing and the caller gets a LOUD REFUSAL naming the device's exact wording —
// never a silent confirmation of read authority nobody observed. A refusal that
// quotes the switch is how the next capture gets taken; a false confirmation is
// how supervision dies quietly.
//
// An EMPTY value lands in the same place, and for the same reason: a view field
// with nothing after the colon has never been seen, so it proves nothing.
func parseSNMPView(value string) snmpView {
	var v = snmpView{Raw: strings.TrimSpace(value)}
	if v.Raw == "" {
		return v
	}
	if strings.HasPrefix(v.Raw, "<") && strings.HasSuffix(v.Raw, ">") {
		var inner = strings.ToLower(strings.Join(strings.Fields(v.Raw), " "))
		if strings.Contains(inner, defaultAllIncludedMark) {
			v.Authority = viewAuthorityAll
		}
		return v
	}
	v.Name = v.Raw
	v.Authority = viewAuthorityNamed
	return v
}

// findSNMPGroup looks a group up by NAME AND SECURITY MODEL together.
//
// The name is compared exactly — group names are case-sensitive on the wire, and a
// user is bound to the one it spells. The model is compared case-insensitively with
// its internal padding collapsed, because it is a rendering of two keywords and not
// an identifier.
//
// BOTH are required: the model is a field of the block, so the same name plausibly
// appears once per model (DEDUCED, cf. the section header), and answering a
// question about a v3 group from a v1 or v2c block would confirm the read authority
// of an access entry the v3 user never goes through.
func findSNMPGroup(groups []snmpGroup, name, security string) (snmpGroup, bool) {
	var g snmpGroup
	for _, g = range groups {
		if g.Name == name && sameSNMPSecurity(g.SecurityModel, security) {
			return g, true
		}
	}
	return snmpGroup{}, false
}

// sameSNMPSecurity compares two "v3 priv"-style renderings.
func sameSNMPSecurity(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "),
		strings.Join(strings.Fields(b), " "))
}

// snmpGroupModels lists the security models a name was seen under, for an error
// message that says what the device DID show rather than only what it did not.
func snmpGroupModels(groups []snmpGroup, name string) []string {
	var seen []string
	var g snmpGroup
	for _, g = range groups {
		if g.Name == name {
			seen = append(seen, orUnknown(g.SecurityModel))
		}
	}
	return seen
}

// --- small set helpers, shared across the package ---

// removeInt returns the slice without the first occurrence of v, order kept.
func removeInt(list []int, v int) []int {
	var out = list[:0:0]
	var x int
	for _, x = range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	var i int
	for i = range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsAll(have, want []int) bool {
	var set = map[int]bool{}
	var v int
	for _, v = range have {
		set[v] = true
	}
	for _, v = range want {
		if !set[v] {
			return false
		}
	}
	return true
}

func subtractInts(from, minus []int) []int {
	var set = map[int]bool{}
	var v int
	for _, v = range minus {
		set[v] = true
	}
	var out []int
	for _, v = range from {
		if !set[v] {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

func joinInts(list []int) string {
	var parts []string
	var v int
	for _, v = range list {
		parts = append(parts, strconv.Itoa(v))
	}
	return strings.Join(parts, ",")
}

func sortInts(v []int) { sort.Ints(v) }

func sortVLANs(v []driver.VLAN) {
	sort.Slice(v, func(i, j int) bool { return v[i].ID < v[j].ID })
}

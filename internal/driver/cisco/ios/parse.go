package ios

// Parsers for the Catalyst IOS CLI. Unlike the FS connector next door, the
// fixtures these are tested against are DOCUMENTED output shapes, not lab
// captures — cf. the package doc. They are written to be tolerant of the
// column padding IOS varies between releases, and strict about what they agree
// to call a faceplate port.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// # Numbering: STANDARD in the model, IOS NAME on screen and on the wire
//
// This connector's standard model numbers ports "GE1"..."GE24" for
// GigabitEthernet and "XG1"..."XG2" for TenGigabitEthernet — the same scheme
// as every other numbering platform (Instant On, SwOS, FS S3900), so the
// backend stays UNIFORM: profiles, diffs and telemetry speak one vocabulary
// across the fleet. Translating back to what the device understands is the
// connector's job ([cliPortName]: "GE11" -> "GigabitEthernet1/0/11"), and the
// name printed on the faceplate goes to the screen through the `silk` field on
// port groups.
//
// TWO PREFIXES AND NOT ONE, and this is forced by the hardware rather than
// chosen: a WS-C2960X-48FPD-L carries BOTH GigabitEthernet1/0/1 AND
// TenGigabitEthernet1/0/1. They are different physical sockets with the same
// unit/slot/port triple. Folding them onto one prefix would make "GE1" mean two
// jacks, and a write would eventually land on the wrong one.
//
// # ASSUMED LIMIT: stack member 1 only, and this one is not laziness
//
// A Catalyst stack numbers ports by MEMBER (Gi2/0/5), and members RENUMBER on
// their own: replacing a failed unit, or a master election, is enough to turn
// Gi2/0/x into Gi3/0/x with no configuration change and no operator gesture.
// Any standard name encoding the member would therefore silently re-key every
// port of a stack the day it heals itself — meshcraft's intent would keep
// pointing at a jack that has moved. Ports on members other than 1 have NO
// standard name here and are skipped with a note, which costs visibility on a
// stack and never writes to the wrong port.

// rePortIOS accepts every spelling of a Catalyst interface name across the
// three sources that name a port: the CLI and running-config
// ("GigabitEthernet1/0/11"), `show interfaces status` ("Gi1/0/11"), SNMP
// ifDescr (the full form), and LLDP local-port IDs (usually the short form).
//
// The media word is REQUIRED. A bare "1/0/11" is deliberately not accepted:
// on this platform the media word is what distinguishes two different sockets
// (cf. above), so a name that has lost it has lost the information the
// translation needs — refusing is the only safe answer.
var rePortIOS = regexp.MustCompile(
	`(?i)^\s*(fastethernet|fa|gigabitethernet|gigabit|gigabitether|gi|twogigabitethernet|tw|tengigabitethernet|tengige|te)\s*` +
		`(\d+)/(\d+)(?:/(\d+))?\s*$`)

// stdPrefixOf maps the IOS media word to the standard-model prefix.
//
// "GE" for gigabit lines up with every other connector in the product; "XG"
// and "FE" exist because this vendor puts several media on one faceplate and
// the numbers collide. TwoGigabitEthernet (2960-XR uplinks, mGig on later
// ranges) gets its own prefix for exactly the same reason.
func stdPrefixOf(media string) string {
	switch strings.ToLower(media) {
	case "fastethernet", "fa":
		return "FE"
	case "gigabitethernet", "gigabit", "gigabitether", "gi":
		return "GE"
	case "twogigabitethernet", "tw":
		return "TG"
	case "tengigabitethernet", "tengige", "te":
		return "XG"
	}
	return ""
}

// cliMediaOf is the reverse: the FULL IOS word for a standard prefix. The full
// word is always used on the wire — IOS accepts abbreviations interactively,
// and a transcript that reads "interface GigabitEthernet1/0/11" is the one an
// operator can paste back into the device.
func cliMediaOf(stdPrefix string) string {
	switch stdPrefix {
	case "FE":
		return "FastEthernet"
	case "GE":
		return "GigabitEthernet"
	case "TG":
		return "TwoGigabitEthernet"
	case "XG":
		return "TenGigabitEthernet"
	}
	return ""
}

// silkOf is the faceplate/CLI shorthand shown to the operator ("Gi1/0/11").
func silkOf(stdPrefix string) string {
	switch stdPrefix {
	case "FE":
		return "Fa1/0/"
	case "GE":
		return "Gi1/0/"
	case "TG":
		return "Tw1/0/"
	case "XG":
		return "Te1/0/"
	}
	return ""
}

var reStdName = regexp.MustCompile(`^(FE|GE|TG|XG)(\d+)$`)

// splitIOSName decomposes an interface name into (standard prefix, member,
// slot, port). ok=false as soon as anything falls outside what this connector
// covers.
//
// The two accepted shapes are the two IOS uses:
//
//   - "Gi0/24"    — non-stackable member (2960 classic): slot/port;
//   - "Gi1/0/24"  — stackable member (2960-X): member/slot/port.
//
// A module slot other than 0 (uplink modules on 3560/3750) is REFUSED: those
// ports are not contiguous with the faceplate and numbering them alongside it
// would draw sockets in the wrong place.
func splitIOSName(name string) (stdPrefix string, member, slot, port int, ok bool) {
	var m = rePortIOS.FindStringSubmatch(name)
	if m == nil {
		return "", 0, 0, 0, false
	}
	stdPrefix = stdPrefixOf(m[1])
	if stdPrefix == "" {
		return "", 0, 0, 0, false
	}
	var a, _ = strconv.Atoi(m[2])
	var b, _ = strconv.Atoi(m[3])
	if m[4] == "" {
		// slot/port — a non-stacking chassis, member is implicitly 1.
		member, slot, port = 1, a, b
	} else {
		var c, _ = strconv.Atoi(m[4])
		member, slot, port = a, b, c
	}
	if member != 1 || slot != 0 || port < 1 {
		return "", 0, 0, 0, false
	}
	return stdPrefix, member, slot, port, true
}

// StandardPortName (driver.PortNamer) translates a PLATFORM port name into the
// standard-model name. Pure: no device access, usable before a session exists.
//
// This is what makes SNMP samples and LLDP neighbors attach to the right port
// (api.facade.resolve step 4) AND what re-keys a pasted configuration at
// adoption time, so a device adopted by import and one adopted by push carry
// one vocabulary and not two.
func (d *Driver) StandardPortName(deviceName string) (string, bool) {
	var stdPrefix, _, _, port, ok = splitIOSName(deviceName)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("%s%d", stdPrefix, port), true
}

// cliPortName translates a standard-model name into the full IOS interface
// name. Returns "" for a name outside the scheme — the caller refuses rather
// than writing blind.
func cliPortName(std string) string {
	var m = reStdName.FindStringSubmatch(strings.TrimSpace(std))
	if m == nil {
		return ""
	}
	var media = cliMediaOf(m[1])
	if media == "" {
		return ""
	}
	return fmt.Sprintf("%s1/0/%s", media, m[2])
}

// --- show version ---

// versionFacts: what "show version" establishes on a Catalyst.
type versionFacts struct {
	// Release is the IOS train, "15.2(7)E3".
	Release string
	// Model is the orderable part number, "WS-C2960X-24PS-L".
	Model  string
	Serial string
	MAC    string
	// Image names the feature set; a NON-k9 image has no crypto and therefore
	// no SSH at all, which decides whether a provisioning run can even end
	// with an adoptable device.
	Image string
}

var (
	reVerRelease = regexp.MustCompile(`(?i)Cisco IOS Software[^\n]*?Version\s+([^\s,]+)`)
	reVerModel   = regexp.MustCompile(`(?im)^\s*Model [Nn]umber\s*:\s*(\S+)`)
	reVerSerial  = regexp.MustCompile(`(?im)^\s*System [Ss]erial [Nn]umber\s*:\s*(\S+)`)
	reVerMAC     = regexp.MustCompile(`(?im)^\s*Base [Ee]thernet MAC [Aa]ddress\s*:\s*(\S+)`)
	reVerImage   = regexp.MustCompile(`(?i)Cisco IOS Software,\s*[^(]*\(([^)]+)\)`)
	// reIsIOS is what decides the dialect: a device whose "show version" does
	// not announce IOS is not one this command set was written for.
	reIsIOS = regexp.MustCompile(`(?i)Cisco IOS Software|IOS \(tm\)|IOS-XE Software`)
)

func parseShowVersion(out string) versionFacts {
	var f versionFacts
	var m = reVerRelease.FindStringSubmatch(out)
	if m != nil {
		f.Release = m[1]
	}
	if m = reVerModel.FindStringSubmatch(out); m != nil {
		f.Model = m[1]
	}
	if m = reVerSerial.FindStringSubmatch(out); m != nil {
		f.Serial = m[1]
	}
	if m = reVerMAC.FindStringSubmatch(out); m != nil {
		f.MAC = m[1]
	}
	if m = reVerImage.FindStringSubmatch(out); m != nil {
		f.Image = m[1]
	}
	return f
}

// hasCrypto reports whether the running image can generate SSH keys. A
// non-crypto image does not carry the `crypto` command at all, and a
// provisioning run that ends there leaves a switch reachable only by console.
func (f versionFacts) hasCrypto() bool {
	var img = strings.ToUpper(f.Image)
	return strings.Contains(img, "K9")
}

// --- show interfaces status ---

// statusPort is one row of "show interfaces status".
type statusPort struct {
	// Name is the standard-model name ("GE11"), StdPrefix its group prefix.
	Name      string
	StdPrefix string
	// CLIName is the FULL IOS name, ready to be written after "interface ".
	CLIName string
	Num     int
	Type    string
	LinkUp  bool
	// Status is the raw word from the Status column, lower-cased. Kept whole
	// rather than reduced to LinkUp: "notconnect" (nothing plugged in) and
	// "err-disabled" (the switch shut the port itself) are both "no link" and
	// call for opposite reactions from the operator.
	Status string
	// SpeedMbps is the port's CAPABILITY, deduced from the media word and the
	// Type column — not the negotiated rate, which is telemetry.
	SpeedMbps int
}

// A documented "show interfaces status" row:
//
//	Port      Name               Status       Vlan       Duplex  Speed Type
//	Gi1/0/1                      notconnect   1            auto   auto 10/100/1000BaseTX
//	Gi1/0/25  uplink-core        connected    trunk        full   1000 1000BaseSX SFP
//	Te1/0/1                      notconnect   1            full    10G unknown
//
// The Status word is the pivot: the Name column is free text that may contain
// spaces, and the Type column may too, so neither end can be split on
// whitespace. Anchoring on the finite set of status words is what makes both
// sides unambiguous.
var reStatusLine = regexp.MustCompile(
	`(?im)^(\S+)\s+(.*?)\b(connected|notconnect|disabled|err-disabled|errdisable|inactive|suspended|monitoring|faulty)\b\s+(\S+)\s+(\S+)\s+(\S+)\s*(.*?)\s*$`)

func parseInterfacesStatus(out string) []statusPort {
	var res []statusPort
	var rows = reStatusLine.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var m = rows[i]
		var stdPrefix, _, _, port, ok = splitIOSName(m[1])
		if !ok {
			// Not a faceplate port of member 1: a stacked member, an uplink
			// module, or an interface that is not one at all. Never numbered.
			continue
		}
		var p = statusPort{
			Name:      fmt.Sprintf("%s%d", stdPrefix, port),
			StdPrefix: stdPrefix,
			CLIName:   fmt.Sprintf("%s1/0/%d", cliMediaOf(stdPrefix), port),
			Num:       port,
			Type:      strings.TrimSpace(m[7]),
			LinkUp:    strings.EqualFold(m[3], "connected"),
			Status:    strings.ToLower(m[3]),
		}
		p.SpeedMbps = capabilitySpeed(stdPrefix, p.Type)
		res = append(res, p)
	}
	return res
}

// errDisabled reports whether the switch shut this port ITSELF. Distinct from
// "notconnect" (nothing plugged in) and from "disabled" (an operator typed
// `shutdown`), and the distinction is the whole point: those two are expected
// states, this one is the switch refusing something.
func (p statusPort) errDisabled() bool {
	return p.Status == "err-disabled" || p.Status == "errdisable"
}

// --- show interfaces status err-disabled ---
//
// The REASON column, which the plain status table does not carry:
//
//	Port      Name               Status       Reason               Err-disabled Vlans
//	Gi1/0/25                     err-disabled gbic-invalid
//
// Same pivot as the main table — the Name column is free text — with the
// reason taken as the first word after the status.
var reErrDisabledLine = regexp.MustCompile(
	`(?im)^(\S+)\s+(?:.*?\s+)?err-?disabled?\s+(\S+)`)

// parseErrDisabled maps standard-model port name -> the reason IOS gives.
func parseErrDisabled(out string) map[string]string {
	var res = map[string]string{}
	var rows = reErrDisabledLine.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var stdPrefix, _, _, port, ok = splitIOSName(rows[i][1])
		if !ok {
			continue
		}
		res[fmt.Sprintf("%s%d", stdPrefix, port)] = strings.ToLower(rows[i][2])
	}
	return res
}

// ErrDisableGBIC is the reason IOS gives when it refuses a transceiver it does
// not recognize as Cisco-coded.
//
// # Why this string is worth a constant
//
// This is the single most common way a Catalyst provisioning run dies, and it
// dies looking like something else entirely. The switch REFUSES the optic,
// shuts the port, and the port then reads exactly like an empty one — so
// uplink discovery finds nothing, the management VLAN is carried on no port,
// and the probe that follows reports "the address does not answer". Every
// symptom points at the network and none at the cage.
//
// The console says it plainly, once, in syslog:
//
//	%PHY-4-UNSUPPORTED_TRANSCEIVER: Unsupported transceiver found in Gi1/0/25
//	%GBIC_SECURITY_CRYPT-4-VN_DATA_CRC_ERROR: GBIC in port 25 has bad crc
//
// so the two paths that can see that text ([ReadState] here, and the Web
// Serial soft reset) both name it rather than letting the operator chase a
// cable.
const ErrDisableGBIC = "gbic-invalid"

// UnsupportedTransceiverNote explains the condition and the way out, in the
// words an operator needs at the moment a port is unexpectedly dark.
//
// Used on a device meshcraft did NOT provision — or one where the setting was
// lost, which a factory reset does: the two commands live in the running
// configuration and go with it. Provisioning applies them itself (cf.
// [MgmtPushCmds]), so a port found in this state on a meshcraft-provisioned
// switch means the setting was wiped, not that it was never made.
const UnsupportedTransceiverNote = "the switch REFUSED the transceiver in this cage " +
	"(err-disabled, reason gbic-invalid): IOS only accepts Cisco-coded optics unless " +
	"told otherwise. The port reads exactly like an empty one, which is why an uplink " +
	"can seem to be missing when the fibre is in fact plugged in. Fix: " +
	"« service unsupported-transceiver » then « no errdisable detect cause gbic-invalid », " +
	"then bounce the port (shutdown / no shutdown) — a port already err-disabled stays " +
	"down until it is bounced, disabling the detection does not clear it"

// UnsupportedTransceiverCmds returns the global commands that make IOS accept
// third-party optics, in order.
//
// APPLIED AT EVERY PROVISIONING, deliberately and by operator instruction.
// Cisco attaches a warranty disclaimer to the first command — it prints one on
// the console — and that is a real consideration, but it is the fleet owner's
// to make and it has been made: a lab and a fleet stocked with third-party
// optics would otherwise fail provisioning on the one symptom that looks like
// everything except its cause.
//
// The two commands live in the RUNNING CONFIGURATION, so a factory reset takes
// them with it. That is exactly why they belong in the provisioning push
// rather than in a one-off gesture: the reset that precedes the push is what
// removes them.
//
// Shared by the connector and the Web Serial soft reset so the two paths never
// drift into two different remediations. The first command is HIDDEN on some
// images; a refusal is therefore tolerable and must be reported, never treated
// as a failed provisioning run.
func UnsupportedTransceiverCmds() []string {
	return []string{
		"service unsupported-transceiver",
		"no errdisable detect cause gbic-invalid",
	}
}

// capabilitySpeed: the media word decides, the Type column only refines. A
// TenGigabitEthernet cage is 10G whatever its Type says — an EMPTY cage
// reports "unknown", and reading the socket's capability off a transceiver
// that is not plugged in would make the faceplate change shape when someone
// unplugs a fibre.
func capabilitySpeed(stdPrefix, typ string) int {
	switch stdPrefix {
	case "XG":
		return 10000
	case "TG":
		return 2500
	case "FE":
		return 100
	}
	var lower = strings.ToLower(typ)
	if strings.Contains(lower, "10gbase") {
		return 10000
	}
	return 1000
}

// isSFPCage reports whether the row describes an optical cage rather than an
// RJ45 jack.
//
// A copper PHY always announces itself ("10/100/1000BaseTX"), plugged or not.
// A cage announces its transceiver when one is present and "unknown" or
// "Not Present" when it is empty. So: a Type that names copper is copper, and
// EVERYTHING ELSE on a gigabit port is a cage. Deducing it from the port
// number instead — "the last four are SFP" — is what a model catalog does, and
// it is exactly what this connector exists to avoid.
func isSFPCage(stdPrefix, typ string) bool {
	if stdPrefix == "XG" || stdPrefix == "TG" {
		return true
	}
	var lower = strings.ToLower(typ)
	if strings.Contains(lower, "base-tx") || strings.Contains(lower, "basetx") ||
		strings.Contains(lower, "base-t") || strings.Contains(lower, "baset") {
		return false
	}
	return true
}

// --- show vlan brief ---

// The VLAN list is read from "show vlan brief" and NOT from the
// running-config, and this is a deliberate split: IOS omits a VLAN whose name
// is still the default (VLAN0100) from the running-config entirely, so a
// configuration read would report VLANs that exist as missing and this
// connector would try to create them on every apply.
//
//	VLAN Name                             Status    Ports
//	---- -------------------------------- --------- ------------------------
//	1    default                          active    Gi1/0/1, Gi1/0/2
//	99   LAN99_Management                 active
//	1002 fddi-default                     act/unsup
var reVlanBrief = regexp.MustCompile(`(?im)^\s*(\d{1,4})\s+(\S+)\s+(active|act/unsup|suspended|act/lshut|sus/lshut)\b`)

// reservedVLAN: 1002-1005 are the FDDI / Token Ring defaults every IOS
// carries. They cannot be removed, they are not usable, and reporting them
// would make the site VLAN diff permanently wrong.
func reservedVLAN(id int) bool { return id >= 1002 && id <= 1005 }

func parseVlanBrief(out string) []driver.VLAN {
	var res []driver.VLAN
	var seen = map[int]bool{}
	var rows = reVlanBrief.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var id, err = strconv.Atoi(rows[i][1])
		if err != nil || id < 1 || id > 4094 || reservedVLAN(id) || seen[id] {
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
	Ports    map[string]driver.PortState
	// AllowedAll marks trunks with NO explicit "switchport trunk allowed vlan"
	// line. On IOS that is not an empty list, it is ALL VLANs — the opposite
	// meaning. Without this distinction a trunk left at its default reads as
	// "carries nothing", every apply issues an "add" that changes nothing (the
	// list was already everything), and the port reports drift forever.
	AllowedAll map[string]bool
	// MgmtVLAN: the VLAN of the SVI that carries an address.
	MgmtVLAN int
	Mgmt     driver.MgmtAddrState
	// Communities: SNMP strings set, with their right (ro|rw).
	Communities map[string]string
	TrapHosts   []string
	// Accounts are the local usernames declared.
	Accounts []string
	// SSHReady: does the configuration carry what SSH needs to work at all?
	SSHReady bool
	// LLDPRun: is "lldp run" configured? Without it IOS serves an empty
	// LLDP-MIB, and the neighbor screen must be able to say why.
	LLDPRun bool
}

var (
	reRunHostname = regexp.MustCompile(`(?im)^\s*hostname\s+(\S+)`)
	// "interface GigabitEthernet1/0/1", "interface Vlan99" — IOS writes no
	// space between the media word and the numbers.
	reRunIface  = regexp.MustCompile(`(?i)^\s*interface\s+(\S+)\s*$`)
	reRunVlanIf = regexp.MustCompile(`(?i)^vlan\s*(\d+)$`)
	reRunMode   = regexp.MustCompile(`(?i)^\s*switchport\s+mode\s+(access|trunk|dot1q-tunnel)`)
	reRunTrkAl  = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+allowed\s+vlan\s+(?:(add|remove|except)\s+)?([\d,\- ]+)`)
	reRunTrkNa  = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+native\s+vlan\s+(\d+)`)
	reRunAccess = regexp.MustCompile(`(?i)^\s*switchport\s+access\s+vlan\s+(\d+)`)
	reRunIPAddr = regexp.MustCompile(`(?i)^\s*ip\s+address\s+(\d+\.\d+\.\d+\.\d+)\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunIPDHCP = regexp.MustCompile(`(?i)^\s*ip\s+address\s+dhcp`)
	reRunGW     = regexp.MustCompile(`(?i)^\s*ip\s+default-gateway\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunRoute  = regexp.MustCompile(`(?i)^\s*ip\s+route\s+0\.0\.0\.0\s+0\.0\.0\.0\s+(\d+\.\d+\.\d+\.\d+)`)
	reRunDNS    = regexp.MustCompile(`(?i)^\s*ip\s+name-server\s+(.+)$`)
	// "snmp-server community public RO" / "... RO 10" (an ACL number may follow)
	reRunSNMP  = regexp.MustCompile(`(?i)^\s*snmp-server\s+community\s+(\S+)\s+(RO|RW)\b`)
	reRunTrapH = regexp.MustCompile(`(?i)^\s*snmp-server\s+host\s+(\S+)`)
	reRunUser  = regexp.MustCompile(`(?i)^\s*username\s+(\S+)`)
	reRunSSHv  = regexp.MustCompile(`(?i)^\s*ip\s+ssh\s+version\s+2`)
	reRunDomN  = regexp.MustCompile(`(?i)^\s*ip\s+domain-name\s+\S+`)
	reRunLLDP  = regexp.MustCompile(`(?i)^\s*lldp\s+run\s*$`)
	reRunShut  = regexp.MustCompile(`(?i)^\s*shutdown\s*$`)
)

func parseRunningConfig(out string) runConfig {
	var rc = runConfig{
		Ports:       map[string]driver.PortState{},
		AllowedAll:  map[string]bool{},
		Communities: map[string]string{},
	}
	var curPort = "" // standard-model name of the interface block being read
	// A trunk port keeps its old « switchport access vlan » line in the running
	// config, inert — and the trunk's PVID is the NATIVE vlan, not that access
	// vlan. Parsing them into the same field let the leftover access vlan win and
	// produced permanent false drift (observed 2026-08-03: a trunk applied with
	// native 1 read back as pvid 100 from a stale access line). So the two are
	// tracked apart and resolved by the FINAL mode in closeBlock.
	var accessVLAN = map[string]int{}
	var nativeVLAN = map[string]int{}
	var curSVI = 0 // "interface VlanN" block being read
	var sviShut = false
	var sawAllowed = false
	var sawDomain = false
	var sawSSHv2 = false
	// staticSVI: a static SVI always beats a DHCP one — it is the one meshcraft
	// wrote — so a later DHCP block must not overwrite what was already found.
	var staticSVI = false

	var closeBlock = func() {
		if curPort != "" {
			var p = rc.Ports[curPort]
			if p.Mode == "trunk" && !sawAllowed {
				rc.AllowedAll[curPort] = true
			}
			// PVID by FINAL mode: a trunk takes its native vlan (default 1 when
			// the line is absent, because 1 is IOS's default and is not printed);
			// anything else takes its access vlan. This is what stops a leftover
			// « switchport access vlan » on a trunk from reading as its PVID.
			if p.Mode == "trunk" {
				var n, ok = nativeVLAN[curPort]
				if ok {
					p.PVID = n
				} else {
					p.PVID = 1
				}
				// The device's allowed list always CONTAINS the native VLAN — IOS
				// requires it — but meshcraft's model carries the native as PVID,
				// separately from the tagged set. Leaving it in Tagged makes an
				// exactly-applied trunk drift forever against an intent that never
				// lists its own native. Remove it, mirroring exactAllowed on the
				// write side (which adds it).
				p.Tagged = removeInt(p.Tagged, p.PVID)
			} else if a, ok := accessVLAN[curPort]; ok {
				p.PVID = a
			}
			if p.PVID == 0 {
				p.PVID = 1
			}
			rc.Ports[curPort] = p
		}
		curPort, curSVI, sviShut, sawAllowed = "", 0, false, false
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
			if vm := reRunVlanIf.FindStringSubmatch(what); vm != nil {
				curSVI, _ = strconv.Atoi(vm[1])
				continue
			}
			var std, ok = (&Driver{}).StandardPortName(what)
			if !ok {
				continue // not a faceplate port of member 1: block ignored
			}
			curPort = std
			var _, seen = rc.Ports[curPort]
			if !seen {
				// A port whose block says nothing is at its factory default:
				// access on VLAN 1.
				rc.Ports[curPort] = driver.PortState{Mode: "access", PVID: 1}
			}
			continue
		}

		if curPort != "" {
			var p = rc.Ports[curPort]
			switch {
			case reRunMode.MatchString(line):
				var mode = strings.ToLower(reRunMode.FindStringSubmatch(line)[1])
				if mode == "dot1q-tunnel" {
					// Not a mode the standard model carries. Left ALONE rather
					// than approximated as a trunk: apply would then rewrite a
					// tunnel port into something its operator never asked for.
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
			// PVID is resolved in closeBlock, from the FINAL mode — not here,
			// where a trunk that still carries a stale access line would take it.
			rc.Ports[curPort] = p
			continue
		}

		if curSVI > 0 {
			if reRunShut.MatchString(line) {
				sviShut = true
				continue
			}
			if m = reRunIPAddr.FindStringSubmatch(line); m != nil {
				// A SHUTDOWN SVI carries an address that answers nothing: it is
				// not the management path, and claiming it is would send the
				// probe to a dead address.
				if !sviShut {
					rc.MgmtVLAN = curSVI
					rc.Mgmt.Mode = driver.MgmtAddrModeStatic
					var bits = PrefixFromMask(m[2])
					if bits > 0 {
						rc.Mgmt.Address = fmt.Sprintf("%s/%d", m[1], bits)
					} else {
						rc.Mgmt.Address = m[1]
						rc.Mgmt.Notes = append(rc.Mgmt.Notes,
							"unreadable network mask "+m[2]+": the prefix length is not reported rather than assumed")
					}
					rc.Mgmt.Interface = fmt.Sprintf("vlan%d", curSVI)
					staticSVI = true
				}
				continue
			}
			if reRunIPDHCP.MatchString(line) {
				if !staticSVI && !sviShut {
					rc.MgmtVLAN = curSVI
					rc.Mgmt.Mode = driver.MgmtAddrModeDHCP
					rc.Mgmt.Interface = fmt.Sprintf("vlan%d", curSVI)
				}
				continue
			}
			continue
		}

		switch {
		case reRunHostname.MatchString(line):
			rc.Hostname = reRunHostname.FindStringSubmatch(line)[1]
		case reRunGW.MatchString(line):
			if rc.Mgmt.Gateway == "" {
				rc.Mgmt.Gateway = reRunGW.FindStringSubmatch(line)[1]
			}
		case reRunRoute.MatchString(line):
			// An L3-capable member of the family may carry a default route
			// instead; either form answers "how does management get out?".
			if rc.Mgmt.Gateway == "" {
				rc.Mgmt.Gateway = reRunRoute.FindStringSubmatch(line)[1]
			}
		case reRunDNS.MatchString(line):
			// IOS puts every resolver on ONE line: "ip name-server A B C".
			var srv string
			for _, srv = range strings.Fields(reRunDNS.FindStringSubmatch(line)[1]) {
				rc.Mgmt.DNS = append(rc.Mgmt.DNS, srv)
			}
		case reRunSNMP.MatchString(line):
			var mm = reRunSNMP.FindStringSubmatch(line)
			rc.Communities[mm[1]] = strings.ToLower(mm[2])
		case reRunTrapH.MatchString(line):
			rc.TrapHosts = append(rc.TrapHosts, reRunTrapH.FindStringSubmatch(line)[1])
		case reRunUser.MatchString(line):
			rc.Accounts = append(rc.Accounts, reRunUser.FindStringSubmatch(line)[1])
		case reRunDomN.MatchString(line):
			sawDomain = true
		case reRunSSHv.MatchString(line):
			sawSSHv2 = true
		case reRunLLDP.MatchString(line):
			rc.LLDPRun = true
		}
	}
	closeBlock()

	rc.SSHReady = sawDomain && sawSSHv2
	rc.Accounts = dedupeStrings(rc.Accounts)
	return rc
}

// expandVlanList accepts "99", "99,100", "10-12", and the spaces IOS wraps
// long lists with.
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

// mergeSorted merges two VLAN lists, SORTED — the PortState contract requires
// it so two reads produce the same state.
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

func dedupeStrings(in []string) []string {
	var seen = map[string]bool{}
	var out []string
	var s string
	for _, s = range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

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

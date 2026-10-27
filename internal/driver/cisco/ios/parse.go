package ios

// Parsers for the Catalyst IOS CLI. Unlike the FS connector next door, the
// fixtures these are tested against are DOCUMENTED output shapes, not lab
// captures — cf. the package doc. They are written to be tolerant of the
// column padding IOS varies between releases, and strict about what they agree
// to call a faceplate port.

import (
	"fmt"
	"net"
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
//
// # TWO STEMS, NOT ONE: "1/0/24" is not universal, and assuming it broke every write
//
// The standard name is LOSSY on purpose — "GE24" says gigabit, port 24, and
// nothing about how this chassis spells the rest. IOS spells it two ways:
//
//   - "GigabitEthernet1/0/24" on anything STACKABLE (2960-X, 2960-XR, 3750-X,
//     3850): member/slot/port, and the member is printed even on a stack of one;
//   - "GigabitEthernet0/24" on anything that is NOT (2960 classic, 3560,
//     3550): slot/port, with no member field at all.
//
// [splitIOSName] has always accepted BOTH on the way in — "Gi0/24" reads as
// GE24 and the whole read model is correct on a 3560. Until 2026-08-08 the way
// OUT hardcoded the stacked stem: every write was addressed to
// "GigabitEthernet1/0/24", an interface a 3560 does not have. It failed
// loudly rather than silently (`% Invalid input detected`, one Skipped line per
// port, nothing written to a wrong jack) — but it failed for the WHOLE write
// path of every non-stackable Catalyst, which is most of the family this
// connector claims: the package doc's own 3560/3750 trunk-encapsulation
// fallback is written for a range that would never have reached the command.
//
// So the stem is LEARNED FROM THE DEVICE ([Client.learnPortStem]) rather than
// assumed, out of the same reads the write paths already perform, and the
// renderers take it as an argument. The default stays "1/0/" — the 2960-X
// this connector is calibrated on — so a device that says nothing behaves
// exactly as before. There is no hardware here that can demonstrate the flat
// stem (the lab 2960-X is a stackable and prints the member), which is why the
// stem is never GUESSED from the model string: it is read, or it is the default.

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

// The two interface-name stems IOS uses, between the media word and the port
// number. Everything that renders a name takes one of these; nothing hardcodes
// one (cf. the header).
const (
	// stackedStem: member/slot/port, printed by every stackable Catalyst even
	// when the stack has one member. The default, and the lab 2960-X's.
	stackedStem = "1/0/"
	// flatStem: slot/port, printed by the non-stackable ranges (2960 classic,
	// 3560, 3550). Never observed on hardware HERE — deduced from the naming
	// those platforms are documented with, and only ever used when a device's
	// own output was spelled that way.
	flatStem = "0/"
)

// silkOf is the faceplate/CLI shorthand shown to the operator ("Gi1/0/11").
//
// The stem is the DEVICE's, not a constant: a 3560 has "Gi0/11" written next to
// the jack, and a faceplate labelled "Gi1/0/11" sends an operator looking for a
// port number that is not printed anywhere on the box. An empty stem falls back
// to the stacked form.
func silkOf(stdPrefix, stem string) string {
	var short = ""
	switch stdPrefix {
	case "FE":
		short = "Fa"
	case "GE":
		short = "Gi"
	case "TG":
		short = "Tw"
	case "XG":
		short = "Te"
	}
	if short == "" {
		return ""
	}
	if stem == "" {
		stem = stackedStem
	}
	return short + stem
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
	stdPrefix, _, port, ok = decomposeIOSName(name)
	if !ok {
		return "", 0, 0, 0, false
	}
	// An accepted name is member 1, slot 0 BY CONSTRUCTION — decomposeIOSName
	// refuses everything else — so these two are constants, kept in the
	// signature because they read as the decomposition they are.
	return stdPrefix, 1, 0, port, true
}

// decomposeIOSName is the single decoder behind [splitIOSName], the stem
// renderers and [Client.learnPortStem]. It reports which of the two stems the
// name was spelled with, which is the fact the write path needs and the
// standard name cannot carry (cf. the header).
func decomposeIOSName(name string) (stdPrefix, stem string, port int, ok bool) {
	var m = rePortIOS.FindStringSubmatch(name)
	if m == nil {
		return "", "", 0, false
	}
	stdPrefix = stdPrefixOf(m[1])
	if stdPrefix == "" {
		return "", "", 0, false
	}
	var a, _ = strconv.Atoi(m[2])
	var b, _ = strconv.Atoi(m[3])
	if m[4] == "" {
		// slot/port — a non-stacking chassis. Module slots other than 0 are
		// refused for the reason the header gives.
		if a != 0 || b < 1 {
			return "", "", 0, false
		}
		return stdPrefix, flatStem, b, true
	}
	var c, _ = strconv.Atoi(m[4])
	if a != 1 || b != 0 || c < 1 {
		return "", "", 0, false
	}
	return stdPrefix, stackedStem, c, true
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

// cliPortNameStem translates a standard-model name into the full IOS interface
// name, spelled with the stem the DEVICE uses. Returns "" for a name outside
// the scheme — the caller refuses rather than writing blind. An empty stem
// falls back to the stacked form.
func cliPortNameStem(std, stem string) string {
	var m = reStdName.FindStringSubmatch(strings.TrimSpace(std))
	if m == nil {
		return ""
	}
	var media = cliMediaOf(m[1])
	if media == "" {
		return ""
	}
	if stem == "" {
		stem = stackedStem
	}
	return media + stem + m[2]
}

// cliPortName is [cliPortNameStem] with the stacked stem — the shape of a
// device nothing has been read from yet.
//
// PREFER [Client.portName] in anything that talks to a device: it carries the
// stem that device actually printed, and this bare form silently addresses
// "1/0/N" to a chassis that may not have one.
//
// NO WRITE PATH CALLS THIS ANY MORE (2026-08-09). dhcpsnoop.go's trust-flag
// write was the last one and it is converted; if a `grep` ever puts this
// function back inside a gesture that talks to a device, that is the bug.
func cliPortName(std string) string { return cliPortNameStem(std, stackedStem) }

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
	// CLIName is the FULL IOS name, ready to be written after "interface " —
	// spelled with the stem THIS row was printed with, never a constant.
	CLIName string
	// Stem is that stem ("1/0/" or "0/"), kept so the faceplate and the write
	// path can be labelled the way the chassis spells itself.
	Stem   string
	Num    int
	Type   string
	LinkUp bool
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
		var stdPrefix, stem, port, ok = decomposeIOSName(m[1])
		if !ok {
			// Not a faceplate port of member 1: a stacked member, an uplink
			// module, or an interface that is not one at all. Never numbered.
			continue
		}
		var p = statusPort{
			Name:      fmt.Sprintf("%s%d", stdPrefix, port),
			StdPrefix: stdPrefix,
			CLIName:   fmt.Sprintf("%s%s%d", cliMediaOf(stdPrefix), stem, port),
			Stem:      stem,
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
	"\"service unsupported-transceiver\" then \"no errdisable detect cause gbic-invalid\", " +
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
	// MgmtVLAN: the VLAN of the SVI THE SESSION IS RIDING — the one whose address
	// meshcraft dialed — and 0 when that cannot be established.
	//
	// ZERO IS A VALUE, and every reader already treats it as one: [carriesMgmtVLAN]
	// answers false for it so ApplyPortAdmin spares every port with a link,
	// ApplyPorts protects no VLAN of its own, and ReadState reports "not known".
	// That is the honest outcome of an ambiguous configuration, and it is the one
	// [resolveMgmtSVI] returns rather than guessing.
	//
	// IT USED TO MEAN "the last addressed SVI in the text", which is not the same
	// sentence at all — see [resolveMgmtSVI] for the switch that proved it, and for
	// why the stakes are higher here than on EOS: [Client.ApplyMgmtAddressing]
	// re-addresses THIS interface, so a wrong SVI does not merely falsify a read,
	// it aims a write at a transit link.
	MgmtVLAN int
	// MgmtSVINotes: how the management SVI was chosen, or why it could not be.
	//
	// Said OUT LOUD because the choice is no longer mechanical: a device with
	// several addressed SVIs has an answer that depends on which address meshcraft
	// dialed, and an operator reading "management VLAN unknown" on a switch with
	// six SVIs needs to be told that is what happened, not left to guess that the
	// read failed. Reaches the operator twice over — [Client.ReadState] puts it in
	// the state read, and [runConfig.Mgmt].Notes carries it into
	// ReadMgmtAddressing.
	MgmtSVINotes []string
	Mgmt         driver.MgmtAddrState
	// Communities: SNMP strings set, with their right (ro|rw).
	Communities map[string]string
	// TrapReceivers: every "snmp-server host" line, WHOLE. The host token
	// alone is not enough to decide anything: on IOS the entry key is
	// host + community (+ port), so two lines can name the same collector and
	// only one of them carries the community the collector accepts.
	TrapReceivers []trapReceiver
	// SNMPTrapEvents: the keywords of the "snmp-server enable traps snmp"
	// line, lower-cased. A receiver without linkdown/linkup here is a
	// receiver that never hears a port flap.
	SNMPTrapEvents []string
	// Accounts are the local accounts declared, with the privilege the line
	// states — [driver.PrivilegeUnknown] when it states none.
	Accounts []driver.Account
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
	reRunTrapH = regexp.MustCompile(`(?i)^\s*snmp-server\s+host\s+(\S+)\s*(.*)$`)
	reRunTrapE = regexp.MustCompile(`(?i)^\s*snmp-server\s+enable\s+traps\s+snmp\b(.*)$`)
	// The name AND the rest of the line: "username mcadmin privilege 15
	// secret 5 $1$…". The tail is what carries the privilege, and keeping it
	// is the whole difference between listing a name and knowing whether it
	// administers the switch.
	reRunUser = regexp.MustCompile(`(?i)^\s*username\s+(\S+)(.*)$`)
	// "privilege 15" inside that tail. Searched only in the part BEFORE
	// "secret" / "password": the hash that follows is attacker-influenced
	// text on a line we are parsing, and it has no business being able to
	// promote its own account.
	reRunUserPriv = regexp.MustCompile(`(?i)\bprivilege\s+(\d+)\b`)
	reRunSSHv     = regexp.MustCompile(`(?i)^\s*ip\s+ssh\s+version\s+2`)
	reRunDomN     = regexp.MustCompile(`(?i)^\s*ip\s+domain-name\s+\S+`)
	reRunLLDP     = regexp.MustCompile(`(?i)^\s*lldp\s+run\s*$`)
	reRunShut     = regexp.MustCompile(`(?i)^\s*shutdown\s*$`)
)

// sviAddr is one "ip address" line of an SVI block, kept whole AND parsed.
//
// Both halves are needed: Text is what gets reported (the exact form this parser
// composed, prefix included), while IP and Net are what the management SVI is
// CHOSEN by. Net is nil when the prefix length could not be read — a dotted mask
// [PrefixFromMask] did not recognise — and such an address can still be compared
// for equality, just not for containment.
type sviAddr struct {
	Text string
	IP   net.IP
	Net  *net.IPNet
}

// parseSVIAddr keeps the address EXACTLY as it was composed and adds what
// comparing it requires. A text that cannot be parsed back loses only the
// comparison — it is still reported, because what the device says is never
// dropped in favour of what this code can handle.
//
// IOS writes "ip address A.B.C.D M.M.M.M" and never the CIDR form EOS uses, so
// the caller has already turned the dotted mask into a prefix length — which is
// why the ParseCIDR branch is still the first one tried. It is not reading a line
// off the device, it is reading back the text this parser itself built; the
// ParseIP branch is what catches the fallback form, a bare address kept when the
// mask was unreadable.
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
// Addrs is in CONFIGURATION ORDER, primary first — IOS prints the primary
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
// out: the FIRST one the block declares, which on IOS is the primary and not a
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
// Measured on an Arista core (core01-arena, DCS-7050SX-64-R) whose parser carried
// this very shape: six addressed SVIs printed Vlan42, Vlan97, Vlan98, Vlan99,
// Vlan100, Vlan3000, and Vlan3000 is a /31 point-to-point transit link. meshcraft
// filed the management VLAN as 3000 and the management address as
// 10.255.255.1/31, while the address it had itself dialed to read all of that was
// 192.0.2.1/16 on Vlan99. Nothing about that failure is Arista-specific: this
// parser assigned the same three fields on every addressed SVI in turn.
//
// # Why it is WORSE here than it was there
//
// On EOS the defect falsified a read. On IOS it can aim a WRITE:
// [Client.ReadMgmtAddressing] hands [runConfig.Mgmt].Interface to
// [Client.ApplyMgmtAddressing], which re-addresses that interface by name. A
// management VLAN read off the last block of the configuration is therefore the
// interface a re-addressing gesture would reconfigure — and the last block of a
// distribution switch's configuration is routinely a transit /31.
//
// # The order of the tests, and why it stops rather than guesses
//
//  1. THE SVI THAT CARRIES THE DIALED ADDRESS. The session arrived somewhere;
//     that somewhere is written in the configuration. Exact, order-independent,
//     and the case every correctly-read switch lands in.
//  2. THE SVI WHOSE SUBNET CONTAINS IT, longest prefix first — the way a routing
//     table resolves the same question. On IOS this is the NOMINAL path rather
//     than a fallback: HSRP is not parsed anywhere in this connector, so on a
//     distribution pair the address meshcraft dials is very probably the HSRP
//     virtual address, which no SVI carries literally while exactly one subnet
//     contains it.
//  3. THE ONLY CANDIDATE, when the configuration offers one. No ambiguity exists
//     to resolve, and this is the shape of most of the fleet — a single
//     management SVI on an L2 access switch. Said out loud when the dialed
//     address is not the one it carries, because then administration is arriving
//     by some other door (the console, an out-of-band port this connector does
//     not model, an address translation) and the SVI is a deduction rather than
//     an observation.
//  4. OTHERWISE, UNKNOWN. Falling back to "the last one" is the bug; falling
//     back to "the first one" is the same bug reading the file backwards. Every
//     reader of [runConfig.MgmtVLAN] treats 0 as "not known" and fails safe on
//     it, so the honest answer is also the harmless one.
//
// Returns the winning candidate, the address to report for it, the sentences the
// operator needs, and whether anything was chosen at all.
//
// The sentences spell SVIs "Vlan99", the capital IOS itself prints in a running
// configuration, while [runConfig.Mgmt].Interface stays "vlan99" because
// ApplyMgmtAddressing concatenates it into the command it sends. Prose for the
// operator and a token for the wire are not the same string.
func resolveMgmtSVI(cands []sviCandidate, dialed string) (sviCandidate, string, []string, bool) {
	var none sviCandidate
	if len(cands) == 0 {
		// Nothing to choose between, and nothing to say: ReadMgmtAddressing
		// already writes the "no SVI carries an address" sentence.
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
					"subnet (%s) contains it — an HSRP virtual address in front of this SVI is "+
					"the ordinary reason, and this connector does not read \"standby\" lines. "+
					"Administration is read as arriving on Vlan%d",
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
					"Administration is arriving by some other door (the console, an out-of-band "+
					"port, or an address translation), and this VLAN is a deduction rather than "+
					"something read",
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
	}
	var curPort = "" // standard-model name of the interface block being read
	// A trunk port keeps its old "switchport access vlan" line in the running
	// config, inert — and the trunk's PVID is the NATIVE vlan, not that access
	// vlan. Parsing them into the same field let the leftover access vlan win and
	// produced permanent false drift (observed 2026-08-03: a trunk applied with
	// native 1 read back as pvid 100 from a stale access line). So the two are
	// tracked apart and resolved by the FINAL mode in closeBlock.
	var accessVLAN = map[string]int{}
	var nativeVLAN = map[string]int{}
	var curSVI = 0 // "interface VlanN" block being read
	var sawAllowed = false
	var sawDomain = false
	var sawSSHv2 = false
	// Every SVI block is COLLECTED and the management one chosen afterwards, by
	// [resolveMgmtSVI]. Nothing about the choice may depend on the order the blocks
	// arrive in — that dependency IS the bug this shape replaces.
	//
	// sviShut is kept apart from the candidate list for the same reason at block
	// scale, and on IOS that is not a hypothetical: IOS prints "shutdown" near
	// the END of an interface block, AFTER "ip address". The single boolean this
	// replaces was therefore still false when the address line went by, so an SVI
	// both addressed and administratively down was taken for the management path
	// every time. The flag is applied when the list is filtered, not as each
	// address line goes past.
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
			// PVID by FINAL mode: a trunk takes its native vlan (default 1 when
			// the line is absent, because 1 is IOS's default and is not printed);
			// anything else takes its access vlan. This is what stops a leftover
			// "switchport access vlan" on a trunk from reading as its PVID.
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
			} else {
				var a, ok = accessVLAN[curPort]
				if ok {
					p.PVID = a
				}
				// An ACCESS (or unset) port carries NO tagged VLANs. IOS keeps a
				// stale "switchport trunk allowed vlan" line in the config after
				// a trunk→access switch — inert, but the parser would otherwise
				// read it as tagged membership and drift the port forever against
				// an access intent whose tagged set is empty. Mirror of the PVID
				// rule above (observed 2026-08-03 on GE6).
				p.Tagged = nil
			}
			if p.PVID == 0 {
				p.PVID = 1
			}
			rc.Ports[curPort] = p
		}
		curPort, curSVI, sawAllowed = "", 0, false
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
				// A SHUTDOWN SVI carries an address that answers nothing: it is
				// not the management path, and claiming it is would send the
				// probe to a dead address. Recorded per VLAN so the line's
				// POSITION in the block does not decide — and on IOS it always
				// came too late (cf. sviShut above).
				sviShut[curSVI] = true
				continue
			}
			if m = reRunIPAddr.FindStringSubmatch(line); m != nil {
				var c = sviOf(curSVI)
				c.Mode = driver.MgmtAddrModeStatic
				var bits = PrefixFromMask(m[2])
				if bits > 0 {
					c.Addrs = append(c.Addrs, parseSVIAddr(fmt.Sprintf("%s/%d", m[1], bits)))
				} else {
					// The bare address still compares for EQUALITY against the
					// dialed one, which is the test that matters; only
					// containment is lost.
					c.Addrs = append(c.Addrs, parseSVIAddr(m[1]))
					c.Notes = append(c.Notes,
						"unreadable network mask "+m[2]+": the prefix length is not reported rather than assumed")
				}
				continue
			}
			if reRunIPDHCP.MatchString(line) {
				var c = sviOf(curSVI)
				// A static address in the same block always wins the MODE: it is
				// the one meshcraft wrote, and "ip address dhcp" left behind it
				// is the line that is inert.
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
			var mm = reRunTrapH.FindStringSubmatch(line)
			rc.TrapReceivers = append(rc.TrapReceivers, parseTrapReceiver(mm[1], mm[2]))
		case reRunTrapE.MatchString(line):
			var w string
			for _, w = range strings.Fields(reRunTrapE.FindStringSubmatch(line)[1]) {
				rc.SNMPTrapEvents = append(rc.SNMPTrapEvents, strings.ToLower(w))
			}
		case reRunUser.MatchString(line):
			rc.Accounts = append(rc.Accounts, parseRunUsername(line))
		case reRunDomN.MatchString(line):
			sawDomain = true
		case reRunSSHv.MatchString(line):
			sawSSHv2 = true
		case reRunLLDP.MatchString(line):
			rc.LLDPRun = true
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
		// LOWER CASE, and it is load-bearing: ApplyMgmtAddressing concatenates
		// this into "interface" + svi, so the spelling here is the command sent
		// to the device. The operator-facing sentences say "Vlan99" instead —
		// cf. resolveMgmtSVI.
		rc.Mgmt.Interface = fmt.Sprintf("vlan%d", won.VLAN)
		rc.Mgmt.Notes = append(rc.Mgmt.Notes, won.Notes...)
	}
	// The same sentences reach the operator through the state read (ReadState's
	// notes) and through ReadMgmtAddressing; neither screen shows the other.
	rc.Mgmt.Notes = append(rc.Mgmt.Notes, sviNotes...)

	rc.SSHReady = sawDomain && sawSSHv2
	rc.Accounts = dedupeAccounts(rc.Accounts)
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

// parseRunUsername turns one "username …" line into an account.
//
// IOS states the privilege ON THE LINE when it is not the default, and states
// NOTHING when it is. This returns [driver.PrivilegeUnknown] for the silent
// case rather than the documented default of 1, and that is a decision, not an
// omission: the level an account ends up with also depends on `privilege exec
// level` remappings and on whether `aaa authorization exec` is in play, none of
// which this line says. "The configuration did not say" is the honest answer,
// and the audit needs it to stay distinguishable from "the configuration said
// a low level".
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
	return driver.AccountAtLevel(m[1], priv, "show running-config")
}

// firstKeyword returns the offset of whichever lower-cased keyword comes first
// in a line whose case IOS does not guarantee, or -1 when none is there.
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

// dedupeAccounts keeps one row per name, MERGING what the rows say: IOS prints
// one line per account, but a configuration that carries two lines for the same
// name (a habit the FS ranges have) must not lose the one that states the
// privilege just because it came second.
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
	}
	return out
}

// --- snmp-server host ---

// trapReceiver is one "snmp-server host" line, read whole.
//
// The line's grammar, as the running-config prints it back:
//
//	snmp-server host <addr> [vrf <name>] [informs|traps]
//	                 [version {1|2c|3 [auth|noauth|priv]}]
//	                 <community> [udp-port <port>] [<notification types>…]
//
// "version" is OMITTED when the line was set without it — that means v1 on
// this platform, and Version stays empty here rather than being rounded to
// anything: an empty version is exactly the fact that decides a rewrite.
type trapReceiver struct {
	Host string
	// Informs: the line declares informs rather than traps. An inform expects
	// an acknowledgement the collector does not send; such a line retries into
	// the void and is never one this connector wrote.
	Informs bool
	// Version: "1", "2c" or "3" — empty when the line names none (v1 default).
	Version string
	// Community — under v3, the user name.
	Community string
	// Port: 0 means the default 162 (IOS omits "udp-port" at the default).
	Port int
}

// parseTrapReceiver decomposes the part of the line after the address.
// Notification-type keywords at the tail are deliberately not kept: an empty
// tail means "every enabled notification type", and comparing tails would
// make an entry that sends MORE than asked read as divergent.
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

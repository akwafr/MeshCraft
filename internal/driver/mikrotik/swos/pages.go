package swos

import (
	"fmt"
	"strconv"
	"strings"
)

// SwOS page map, and the meaning of their fields.
//
// There is no documentation for these pages. Everything below was observed in
// the JavaScript that the device serves itself (a 44,807-byte `index.html` on a
// CSS326-24G-2S+ running SwOS 2.17, with the script fully inline), which is
// the only reference for this platform: it names each field, its type,
// encoding, bounds, and shows which request the UI uses to write it.
// Same approach as the Instant On connector on its product line.
//
// Each constant below notes where it comes from. A value without a source in
// this file would be invented, and an invented capability eventually damages
// a device.

// Data pages. All served under HTTP Digest authentication as
// `application/x-javascript`.
//
// Major trap on this platform: an unknown path does not return 404. It returns
// the full web UI as HTTP 200, `text/html`. Verified on
// 192.0.2.36 (CSS326-24G-2S+, SwOS 2.17): /vlans.b, /poe.b, /igmp.b, /dhcp.b,
// /cfg.b, /mact.b, /leds.b, /fan.b all eight respond with the 44,807-byte
// UI. Inferring page existence from HTTP 200 is therefore wrong;
// the parser decides, by rejecting anything that is not a SwOS page.
const (
	// PageSys: identity, management addressing, global RSTP, IGMP, DHCP
	// snooping, health. The densest page on the platform.
	PageSys = "/sys.b"
	// PageLink: port state and configuration (enable, name, link, rate,
	// duplex, flow control), and model geometry (`prt`, `sfp`, `sfpo`).
	PageLink = "/link.b"
	// PageFwd: switching — port isolation, locking, mirroring, storm limit,
	// ingress rate, and per-port VLAN (`vlan`, `vlni`, `dvid`).
	PageFwd = "/fwd.b"
	// PageVLAN: the VLAN table, one row per VLAN.
	PageVLAN = "/vlan.b"
	// PageHost: static host table (configured).
	PageHost = "/host.b"
	// PageDynHost: learned host table. Read-only (the "!" in the name marks
	// pages the UI never writes).
	PageDynHost = "/!dhost.b"
	// PageSFP: optical diagnostic for SFP cages (SFF-8472).
	PageSFP = "/sfp.b"
	// PageSNMP: SNMP agent — enable, community, contact, location.
	PageSNMP = "/snmp.b"
	// PageACL: ACL rule table.
	PageACL = "/acl.b"
	// PageStats: per-port counters and rates.
	PageStats = "/stats.b"
	// PageRSTP: per-port RSTP state.
	PageRSTP = "/rstp.b"
	// PageLACP: per-port aggregation.
	PageLACP = "/lacp.b"
	// PageIGMPGroups: learned IGMP groups. Read-only.
	PageIGMPGroups = "/!igmp.b"
	// PagePassword: admin password. On read, returns `{pwd:''}` — never the
	// secret. On write, changes it (see admin.go).
	PagePassword = "/!pwd.b"
	// PageBackup: the entire configuration, as text, in a single request.
	// `application/octet-stream`, but it is plain text.
	PageBackup = "/backup.swb"
)

// SwOS action endpoints. None take a useful body: the UI POSTs the
// character `*`.
//
// Observed, and worth knowing before exploring this platform: these paths ignore
// GET. A `GET /reset` and a `GET /reboot` both return HTTP 200 with an empty
// body and do nothing — verified on 192.0.2.36, whose uptime kept increasing
// (149,281 s then 149,801 s) and whose VLAN table was unchanged immediately
// after. That is what makes read-only exploration actually safe on this
// platform, and not something we should have to discover by trying it.
const (
	// ActionReboot reboots. SwOS configuration is persistent by default
	// (see Capabilities.Commit), so a reboot cancels nothing.
	ActionReboot = "/reboot"
	// ActionReset factory-resets configuration.
	ActionReset = "/reset"
	// ActionLogout closes the UI session.
	ActionLogout = "/logout"
)

// Writable field set for each page, and why this list exists.
//
// The device UI does not send deltas: its "Apply All" button rebuilds the
// entire page object from all modifiable fields (`Sa` in its JavaScript, which
// walks the definitions and skips those marked read-only), then POSTs it as-is.
// Writing on SwOS is therefore necessarily read-modify-rewrite, and a truncated
// page response leaves the device free to zero what we did not say. On /fwd.b,
// that would mean losing port isolation, storm limits, and ingress rates when
// setting a VLAN.
//
// Hence this list: from the device configuration backup, `GET /backup.swb`,
// which enumerates page by page exactly the fields the device stores — i.e.
// writable fields, and nothing else. Real capture bundled in testdata
// (testdata/backup.swb, CSS326-24G-2S+, SwOS 2.17). This is a much better
// source than a live page read, which mixes writable fields and telemetry.
//
// Only pages this connector writes are declared here. Declaring the others
// would invite use without verified effect; their content is in
// testdata/backup.swb, which is the observed source.
var writableFields = map[string][]string{
	// /snmp.b
	PageSNMP: {FieldSNMPEnabled, FieldSNMPCommunity, FieldSNMPContact, FieldSNMPLocation},
	// /rstp.b — backup.swb persists `ena`. Live page also carries `edge` (and
	// p2p/role/…); we POST `ena` + `edge` — both accepted on CSS326 lab.
	PageRSTP: {"ena"},
	// /sys.b — order and keys from testdata/backup.swb (CSS326-24G-2S+, SwOS 2.17).
	// Omitting any of these on POST would let the device zero them; `allp`/`alla`/
	// `allm`/`avln` are the fields that mute administration when mishandled.
	PageSys: {
		FieldIdentity, "wdt", "dsc", "pdsc", "ivl",
		FieldAllowFrom, FieldAllowMask, FieldAllowVLAN, FieldAllowPorts,
		"mgmt", "prio", "cost", "frmc", "poe", "igmp", "igmq", "igfl", "igve",
		FieldIP, FieldDHCPTrusted, FieldDHCPOption82, FieldAddrMode,
	},
	// /link.b — order and keys from testdata/backup.swb, same source and same
	// rule as /sys.b above.
	//
	// THE LIST IS SHORTER THAN THE LIVE PAGE, and that gap is the whole reason
	// this entry exists rather than a hand-written one. The page the device
	// SERVES carries thirty-odd keys (`lnk`, `dpx`, `spd`, `hop`, `len`, `flt`,
	// `pair`…): those are telemetry — the observed link, the observed rate, the
	// cable diagnostic — and the device does not expect them back. What it
	// STORES is these eleven, and nothing else.
	//
	// Note what rides along with `en` on every port-admin write: `nm` (the port
	// names), `an` (autonegotiation), `dpxc`/`fctc`/`fctr` (duplex and flow
	// control), `spdc` (the forced rate). Every one of them is echoed back with
	// the value that was just read, which is what read-modify-rewrite means
	// here — and it is also why a write must be REFUSED when any of them is
	// missing from the read: posting without `nm` would rename twenty-six ports
	// to nothing while shutting one.
	PageLink: {
		FieldEnabled, "blkp", "an", "dpxc", "fctc", "fctr",
		"spdc", "cm", "qtyp", FieldPortName, "sfpr",
	},
}

// WritableFields returns the writable field set for a page, and whether it is
// known. A page whose set is unknown must not be written: the rule
// "an unknown capability is assumed absent" applied to writing.
func WritableFields(page string) ([]string, bool) {
	var f, ok = writableFields[page]
	if !ok {
		return nil, false
	}
	return append([]string{}, f...), true
}

// FwdWritableFields returns the writable set for /fwd.b for a model with `ports`
// ports.
//
// This page is the only one whose field set depends on model size: it carries a
// per-source-port isolation mask, `fp1` through `fp<ports>` — 26 on a
// CSS326-24G-2S+. Copying the field set observed on this model verbatim to a
// smaller model would send masks for ports that do not exist; to a larger one,
// it would omit some, and the device would be free to zero isolation there.
// The list is therefore rebuilt at the correct size, order unchanged from the
// backup.
func FwdWritableFields(ports int) []string {
	var out []string
	var i int
	for i = 1; i <= ports; i++ {
		out = append(out, "fp"+strconv.Itoa(i))
	}
	return append(out, "lck", "lckf", "imr", "omr", "mrto",
		FieldVLANMode, FieldVLANReceive, FieldDefaultVLAN, FieldForceVLAN,
		"srt", "suni", "fmc", "ir")
}

// vlanRowFields is the field set for one row of /vlan.b, in backup order. The
// table is written entirely: there is no per-row add or delete on this platform
// (the UI rebuilds the list and POSTs it).
var vlanRowFields = []string{
	FieldVLANName, FieldVLANMembers, FieldVLANID, "piso", "lrn", "mrr", "igmp",
}

// --- /sys.b fields ---

// Identity and firmware. All read-only except where noted.
const (
	// FieldBoard: model reference, hex-encoded string
	// (`'4353533332362d3234472d32532b'` = "CSS326-24G-2S+").
	FieldBoard = "brd"
	// FieldSerial: serial number.
	FieldSerial = "sid"
	// FieldVersion: SwOS version (`'322e3137'` = "2.17").
	FieldVersion = "ver"
	// FieldBuild: firmware build timestamp, Unix seconds.
	FieldBuild = "bld"
	// FieldRevision: hardware revision, often empty.
	FieldRevision = "rev"
	// FieldMarket: board marketing name, often empty.
	FieldMarket = "mrkt"
	// FieldIdentity: system name, writable ("Identity" in the UI).
	FieldIdentity = "id"
	// FieldMAC: device MAC address, 12 hex digits without separator and
	// without byte swap (`'b869f45f14a8'` = b8:69:f4:5f:14:a8).
	FieldMAC = "mac"
	// FieldUptime: uptime in hundredths of a second
	// (`scale:100` in the UI).
	FieldUptime = "upt"
	// FieldTemp: temperature in whole degrees Celsius, no scale factor.
	// Read 0x44 then 0x45 (68 then 69 °C) minutes apart on 192.0.2.36: a live
	// sensor.
	FieldTemp = "temp"
)

// Management addressing. This is the most dangerous group on the platform: getting
// any of these five fields wrong makes the device unreachable, and this model
// has no serial port.
const (
	// FieldIP: static address. See DecodeIPv4 for byte order.
	FieldIP = "ip"
	// FieldCurrentIP: address in use, read-only. Differs from `ip` when the
	// device uses DHCP.
	FieldCurrentIP = "cip"
	// FieldAddrMode: addressing mode — 0 "DHCP with fallback", 1 "static",
	// 2 "DHCP only" (the three UI selector options, in that order).
	FieldAddrMode = "iptp"
	// FieldAllowVLAN: "Allow From VLAN" — VLAN from which administration is
	// accepted. 0 = all. This is the device management VLAN.
	FieldAllowVLAN = "avln"
	// FieldAllowFrom / FieldAllowMask: "Allow From" — subnet allowed to
	// administer. 0/0 = all.
	FieldAllowFrom = "alla"
	FieldAllowMask = "allm"
	// FieldAllowPorts: "Allow From Ports" — port mask for administration.
	FieldAllowPorts = "allp"
)

// FieldAddrMode addressing modes.
const (
	AddrDHCPFallback uint32 = 0
	AddrStatic       uint32 = 1
	AddrDHCPOnly     uint32 = 2
)

// --- /link.b fields ---

// Model geometry and port state.
const (
	// FieldPortCount: total port count, SFP cages included (0x1a = 26 on a
	// CSS326-24G-2S+).
	FieldPortCount = "prt"
	// FieldSFPCount: number of SFP cages (0x02).
	FieldSFPCount = "sfp"
	// FieldSFPOffset: index of the first SFP cage (0x18 = 24, so ports
	// indexed 24 and 25 — numbered 25 and 26 — are the cages).
	//
	// Optional field: when missing, the UI places cages at the end
	// (`t = void 0==d.sfpo ? da : d.sfpo`, with da = prt - sfp). Reproducing
	// this fallback is necessary; older models may omit it.
	FieldSFPOffset = "sfpo"
	// FieldEnabled: mask of administratively enabled ports.
	FieldEnabled = "en"
	// FieldLink: mask of ports with link up.
	FieldLink = "lnk"
	// FieldDuplex: mask of full-duplex ports.
	FieldDuplex = "dpx"
	// FieldSpeed: negotiated rate, one index per port in SpeedLabels.
	//
	// Meaningful only on a linked port. On 192.0.2.36, the 25 unlinked ports
	// carry index 7 — outside the label table — and the only linked port
	// (index 25, SFP2 cage) carries 3, i.e. 10G, confirmed by the inserted
	// transceiver (`SFP-10G-BX`). Reporting a rate for an off port would
	// invent a value.
	FieldSpeed = "spd"
	// FieldPortName: each port name, hex-encoded string. Default
	// "Port1"… "Port24", "SFP1", "SFP2".
	FieldPortName = "nm"
	// FieldDHCPTrusted: mask of ports TRUSTED for DHCP snooping, in the
	// "DHCP & PPPoE Snooping" group of /sys.b.
	//
	// A MASK, exactly like FieldEnabled — and that is the whole shape of the
	// feature on this platform. There is no global on/off statement and no
	// per-VLAN dimension anywhere in SwOS: the set of trusted ports IS the
	// configuration, and a port's absence from the mask is what makes it
	// untrusted. The enumeration that establishes this is exhaustive, not a
	// failed search — testdata/backup.swb lists every field /sys.b stores
	// (cf. mgmtaddr.go, which leans on the same source to prove SwOS has no
	// gateway field at all).
	//
	// The consequence for driver.DHCPSnoopPlan: `VLANs` cannot be honoured
	// here, and a connector must SAY so in DHCPSnoopState.Notes rather than
	// return an empty VLAN list a caller could read as "snooping covers
	// nothing". Those are two different facts.
	FieldDHCPTrusted = "dtrp"
	// FieldDHCPOption82: insertion of the DHCP information option (option 82).
	//
	// ADJACENT TO SNOOPING, NOT PART OF IT, and the distinction is worth
	// keeping: this rides on the same UI group, but it rewrites relayed
	// requests rather than filtering server replies. SwOS does not route and
	// therefore does not relay (cf. the "DHCPRelay: false" capability in
	// swos.go) — so this option is carried in the writable set because a
	// read-modify-rewrite of /sys.b must echo it back untouched, and for no
	// other reason. Nothing in this codebase sets it.
	FieldDHCPOption82 = "ainf"
)

// SpeedLabels maps FieldSpeed indices. Order matches the UI table:
// `"10M 100M 1G 10G 5G 2.5G 40G".split(" ")`. It is not ascending;
// assuming sorted order yields a wrong rate.
var SpeedLabels = []string{"10M", "100M", "1G", "10G", "5G", "2.5G", "40G"}

// SpeedLabel maps a rate index. An out-of-table index is not "10M":
// it means absent, and is reported as such.
func SpeedLabel(i uint32) (string, bool) {
	if i >= uint32(len(SpeedLabels)) {
		return "", false
	}
	return SpeedLabels[i], true
}

// --- /fwd.b fields: per-port VLAN ---

const (
	// FieldVLANMode: per-port VLAN mode — 0 "disabled", 1 "optional",
	// 2 "enabled", 3 "strict".
	FieldVLANMode = "vlan"
	// FieldVLANReceive: what the port accepts on ingress — 0 "any",
	// 1 "only tagged", 2 "only untagged".
	FieldVLANReceive = "vlni"
	// FieldDefaultVLAN: "Default VLAN ID" for the port, i.e. its PVID.
	FieldDefaultVLAN = "dvid"
	// FieldForceVLAN: mask of ports in "Force VLAN ID" mode.
	FieldForceVLAN = "fvid"
)

// FieldVLANMode VLAN modes.
const (
	VLANModeDisabled uint32 = 0
	VLANModeOptional uint32 = 1
	VLANModeEnabled  uint32 = 2
	VLANModeStrict   uint32 = 3
)

// VLANModeLabel names a VLAN mode using the device UI labels.
func VLANModeLabel(m uint32) (string, bool) {
	switch m {
	case VLANModeDisabled:
		return "disabled", true
	case VLANModeOptional:
		return "optional", true
	case VLANModeEnabled:
		return "enabled", true
	case VLANModeStrict:
		return "strict", true
	}
	return "", false
}

// --- /vlan.b fields: VLAN table ---

const (
	// FieldVLANID: VLAN identifier.
	FieldVLANID = "vid"
	// FieldVLANName: VLAN name, hex-encoded string, 16 bytes max.
	FieldVLANName = "nm"
	// FieldVLANMembers: mask of ports that are VLAN members.
	FieldVLANMembers = "mbr"
)

// --- /snmp.b fields ---

const (
	// FieldSNMPEnabled: whether the SNMP agent is active.
	FieldSNMPEnabled = "en"
	// FieldSNMPCommunity: community string, hex-encoded, 64 bytes max
	// (`ga:64` in the UI). A single community; no version or v3 user fields
	// exist anywhere on the platform.
	FieldSNMPCommunity = "com"
	// FieldSNMPContact / FieldSNMPLocation: sysContact and sysLocation, 64 bytes.
	FieldSNMPContact  = "ci"
	FieldSNMPLocation = "loc"
)

// --- /sfp.b fields: optical diagnostic ---

const (
	// FieldSFPVendor, FieldSFPPart, FieldSFPSerial: transceiver identity,
	// hex-encoded strings padded with spaces (fixed-size SFF-8472 fields).
	// Trim for display, never on read.
	FieldSFPVendor = "vnd"
	FieldSFPPart   = "pnr"
	FieldSFPSerial = "ser"
	// FieldSFPTemp: transceiver temperature in °C. Value 0xffffff80
	// (-128 two's complement) is the UI "no module" sentinel
	// (`u:-128`), not a temperature.
	FieldSFPTemp = "tmp"
	// FieldSFPVcc: supply voltage; divide by 1,000 for volts
	// (`scale:1E3`).
	FieldSFPVcc = "vcc"
	// FieldSFPTxBias: transmit bias current in mA.
	FieldSFPTxBias = "tbs"
	// FieldSFPTxPower / FieldSFPRxPower: optical power. The UI shows dBm via
	// `10·log10(v/10000)` (`scale:1E4, ma:1`) — raw value is tenths of
	// microwatt.
	FieldSFPTxPower = "tpw"
	FieldSFPRxPower = "rpw"
)

// sfpNoModule is FieldSFPTemp when the cage is empty.
const sfpNoModule uint32 = 0xffffff80

// --- /stats.b fields ---

const (
	// FieldRxBytes / FieldTxBytes: received and transmitted bytes. Companions
	// `rbh` and `tbh` hold the upper 32 bits.
	FieldRxBytes     = "rb"
	FieldRxBytesHigh = "rbh"
	FieldTxBytes     = "tb"
	FieldTxBytesHigh = "tbh"
	// FieldRxPackets / FieldTxPackets: total packet counts.
	FieldRxPackets = "rtp"
	FieldTxPackets = "ttp"
	// FieldRxRate / FieldTxRate: rates computed by the device itself.
	// Divide by 100 for bits per second (`scale:.01`).
	//
	// This field lets the connector keep a wide collection floor without losing
	// rate precision: the snapshot rate need not be derived from two counter
	// reads; the device already serves it.
	FieldRxRate = "rrb"
	FieldTxRate = "trb"
	// FieldRxErrors: MAC receive errors; FieldRxFCS: frame check sequence
	// errors.
	FieldRxErrors = "rte"
	FieldRxFCS    = "rfcs"
)

// Counter64 combines a 64-bit counter from its two halves.
//
// SwOS byte counters are split: `rb` holds the low 32 bits, `rbh` the high
// 32 bits. Reading `rb` alone wraps every 4 GiB — on a 10G link, every three
// and a half seconds, producing an entirely wrong traffic curve.
func Counter64(low, high uint32) uint64 {
	return uint64(high)<<32 | uint64(low)
}

// --- Cross-cutting encodings ---

// DecodeIPv4 converts a SwOS address to dotted notation.
//
// Byte order is little-endian: the first octet in dotted notation is the
// least significant byte. `0x240200c0` reads as 192.0.2.36, not 36.2.0.192.
//
// This is not inferred from an observed value; it is the device code. Its
// decoder (`L.C`) does `for(d=0;4>d;++d){ b += a&255; a>>=8 }`, and its
// encoder (`L.B`) does the inverse, `e = e>>8&0xFFFFFF | f<<24` on bytes
// taken in read order — replaying that encoder on 192.0.2.36 yields
// 0x240200C0. Both directions agree, and the decoded address is the one
// the device actually responds on.
//
// This is exactly the kind of detail that displays a wrong address, then
// writes a wrong address: reversing it would POST 36.2.0.192 to a device
// believed to be at 192.0.2.36, and you would have to move physically.
func DecodeIPv4(v uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", v&0xff, v>>8&0xff, v>>16&0xff, v>>24&0xff)
}

// EncodeIPv4 is the exact inverse of DecodeIPv4.
func EncodeIPv4(s string) (uint32, bool) {
	var parts = strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 4 {
		return 0, false
	}
	var out uint32
	var i int
	for i = 0; i < 4; i++ {
		var n, err = strconv.Atoi(parts[i])
		if err != nil || n < 0 || n > 255 {
			return 0, false
		}
		out |= uint32(n) << uint(8*i)
	}
	return out, true
}

// DecodeMAC inserts colons into a SwOS MAC. No byte swap: these fields are
// served in read order.
func DecodeMAC(raw string) (string, bool) {
	if len(raw) != 12 {
		return "", false
	}
	var parts = make([]string, 0, 6)
	var i int
	for i = 0; i < 12; i += 2 {
		var b = raw[i : i+2]
		var _, err = strconv.ParseUint(b, 16, 8)
		if err != nil {
			return "", false
		}
		parts = append(parts, strings.ToLower(b))
	}
	return strings.Join(parts, ":"), true
}

package snmppoll

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gosnmp/gosnmp"
)

// LLDP neighbor data (IEEE 802.1AB), read by the PROXY.
//
// See docs/en/01-Architecture/LLDP neighbours.md for the survey this file is built on. Nothing
// here is inferred from a MIB: each read rule exists because a reference fleet device returned
// something that required it, and the vault says which.
//
// # Why this set does not go through `Samples`
//
// The four existing sets return labeled FLOATS: port, instant, value. LLDP neighbor data is a
// TABLE of strings -- system name, remote port name -- with no honest way into a time series.
// Forcing it would encode each neighbor in a label, one series per neighbor: VictoriaMetrics
// would keep a series per never-unplugged cable forever, and "since when is this neighbor seen"
// would read from presence of a flat curve.
//
// Hence a distinct end-to-end path: this file returns a SNAPSHOT (`LLDPReport`), proxy pushes
// it as-is (`cmd/proxy/lldp.go`), controller stores it in inventory table (`internal/store/lldp.go`).
//
// # What a neighbor is, and is not
//
// A neighbor is a DATED REFUTABLE OBSERVATION: "at this instant, on this port, this device
// received an LLDP frame presenting itself thus". Not proof of direct cable -- survey shows THREE
// neighbors on one CRS326 physical port because intermediate switch that does not consume LLDP
// FLOODS frames instead of absorbing (exactly CSS326 SwOS, no 802.1AB). Not intent either:
// hand-entered links live in `device_links`, unrelated to this.

// LLDP-MIB OIDs. Roots under `1.0.8802` (IEEE, not `1.3.6.1.4.1`) -- IEEE MIB adopted as-is,
// not enterprise branch.
const (
	// lldpLocChassisID / lldpLocSysName: identity THIS device announces.
	//
	// Collected while querying a device we already know by name, least obvious point: they do
	// not describe it, they RECOGNIZE it in what another saw. Without them controller can only
	// match observations by system name, which is not meshcraft name (survey: `lab-crs326`
	// announces "lan-r4", `sw-labo-1930` by serial "CN0EXAMPL1").
	oidLLDPLocChassisID = "1.0.8802.1.1.2.1.3.2"
	oidLLDPLocSysName   = "1.0.8802.1.1.2.1.3.3"

	// MIB-II IDENTITY, walked alongside the neighbor tables so a proxy can PROVE
	// which device it is polling.
	//
	// Not an LLDP concern, and here anyway. The controller uses these two to
	// decide whose reachability verdict counts (cf. internal/api/reachability.go,
	// and migration 0035 for the outage that made it necessary): a proxy that has
	// matched them against the inventory's vendor and model is talking to the
	// right equipment, and a proxy that has not cannot mark a device down. This
	// poll is the only one that already runs every cycle, is already restricted
	// to each device's declared collector, and already carries a per-device
	// report — adding two scalars to it costs one round trip and needs no new
	// endpoint, no new scope, and no second schedule.
	//
	// A SERIAL NUMBER WOULD BE BETTER AND IS NOT AVAILABLE. `entPhysicalSerialNum`
	// (ENTITY-MIB) is the standard place for one, and neither SwOS nor the Instant
	// On 1930 implements it — so requiring it would make confirmation a property
	// of the vendor rather than of the observation, and split the fleet into
	// devices this rule protects and devices it silently does not. sysObjectID is
	// an enterprise number assigned by IANA and sysDescr carries the model on
	// every agent surveyed; together they answer "is this the right equipment?"
	// well enough to outrank a proxy that answers nothing at all.
	oidSysDescr    = "1.3.6.1.2.1.1.1"
	oidSysObjectID = "1.3.6.1.2.1.1.2"

	// lldpLocPortTable: LOCAL port names where LLDP runs.
	//
	// Essential, central MIB trap. `lldpRemTable` attaches neighbor to `lldpLocPortNum`, NOT
	// `ifIndex`: MIB uses LLDP subsystem index. On both reference platforms they coincide, but
	// that is vendor COINCIDENCE -- same one that fooled `physical` for front ports -- trusting
	// it would draw neighbor on wrong port at first differently numbered device. Resolve via
	// this table, never arithmetic.
	oidLLDPLocPortIDSubtype = "1.0.8802.1.1.2.1.3.7.1.2"
	oidLLDPLocPortID        = "1.0.8802.1.1.2.1.3.7.1.3"
	oidLLDPLocPortDesc      = "1.0.8802.1.1.2.1.3.7.1.4"

	// lldpRemTable: neighbors. `lldpRemChassisIdSubtype` (column 4) and two capability columns
	// (11, 12) are NOT read -- see below.
	oidLLDPRemChassisID     = "1.0.8802.1.1.2.1.4.1.1.5"
	oidLLDPRemPortIDSubtype = "1.0.8802.1.1.2.1.4.1.1.6"
	oidLLDPRemPortID        = "1.0.8802.1.1.2.1.4.1.1.7"
	oidLLDPRemPortDesc      = "1.0.8802.1.1.2.1.4.1.1.8"
	oidLLDPRemSysName       = "1.0.8802.1.1.2.1.4.1.1.9"
	oidLLDPRemSysDesc       = "1.0.8802.1.1.2.1.4.1.1.10"
)

// MetricLLDP names this set in standard model vocabulary -- same word as connector
// `MonitorChannel.Metrics`, declared long before anything read it.
const MetricLLDP = "lldp"

// PortIDSubtypeInterfaceName is `interfaceName(5)` from LldpPortIdSubtype.
//
// ONLY subtype whose value is usable port name. Others exist and appear: survey shows
// `macAddress(3)` on 1930 ports 50 and 52 -- exactly the two ports with neighbors. Treating
// their `lldpLocPortId` as name would attach neighbors to port named "0x02005e1061d9", not on
// facade: only two observable fleet links would vanish silently.
const PortIDSubtypeInterfaceName = 5

// Neighbor is ONE observation: this port saw that neighbor.
//
// All strings returned AS THE DEVICE SAID THEM, lossless conversion (cf. OctetText). No
// normalization here: matching neighbor to fleet device is CONTROLLER decision, only it knows
// inventory, and proxy-pre-chewed value would hide what it matched on.
type Neighbor struct {
	// LocalPort: local port name as DEVICE gives it (via lldpLocPortTable). Not yet meshcraft
	// facade name: controller maps with `facadeOf`/`resolve`.
	LocalPort string `json:"local_port"`
	// LocalPortNum: raw `lldpLocPortNum`. Kept for DIAGNOSTICS -- when `LocalPort` empty, only
	// hint which port device meant.
	LocalPortNum int `json:"local_port_num,omitempty"`
	// ChassisID: `lldpRemChassisId`. NON-uniform encoding vendor to vendor, measured fact: same
	// chassis, CRS326 text "B8:69:F4:5B:A0:D2" (17 printable bytes) vs 1930 six raw bytes
	// "0xb869f45ba0d2". Controller normalizes (cf. api.normalizeChassisID); here we transport
	// faithfully.
	ChassisID string `json:"chassis_id,omitempty"`
	// PortID / PortDesc: REMOTE port. Both kept because neither served everywhere: CRS326 serves
	// NO `lldpRemPortDesc` (empty walk, measured), 1930 serves both.
	PortID   string `json:"port_id,omitempty"`
	PortDesc string `json:"port_desc,omitempty"`
	// SysName / SysDesc: neighbor self-description. `SysName` is operator hover text; `SysDesc`
	// carries model and firmware, often only way to recognize off-fleet device.
	SysName string `json:"sys_name,omitempty"`
	SysDesc string `json:"sys_desc,omitempty"`
}

// LLDPReport is the FULL SNAPSHOT of a device: LLDP identity, its own port identifiers,
// and neighbors.
//
// Full not incremental, for `tunnel.WatchConfig` reason: delta assumes both sides share
// state and can drift. Vanished neighbor must vanish, and only way without inventing
// retraction message is send whole table.
type LLDPReport struct {
	DeviceID string `json:"device_id"`
	// ChassisID / SysName: identity announced by THIS device.
	ChassisID string `json:"chassis_id,omitempty"`
	SysName   string `json:"sys_name,omitempty"`
	// SysDescr / SysObjectID: MIB-II identity, as the agent serves it.
	//
	// Carried on the LLDP snapshot because that is the report already pushed on
	// every poll cycle by the device's declared collector. The controller matches
	// them against the inventory's vendor and model, and a proxy that matches has
	// PROVEN it is polling the right equipment — which is what decides whose
	// reachability verdict counts (cf. internal/api/reachability.go).
	SysDescr    string `json:"sys_descr,omitempty"`
	SysObjectID string `json:"sys_object_id,omitempty"`
	// PortIDs: this device's `lldpLocPortId` values, as-is.
	//
	// Field looks useless and saves the fleet's only bilateral link. Survey: 1930 declares
	// `lldpLocChassisId = 0x02005e1061a7`, but frames from port 50 carry chassis-id
	// `0x02005e1061d9` -- PORT MAC, its `lldpLocPortId`. CRS326 records neighbor whose
	// chassis-id appears NOWHERE in 1930 declared identity. Without list, controller cannot
	// recognize neighbor, link seen both sides looks unilateral.
	PortIDs []string `json:"port_ids,omitempty"`
	// Neighbors: observations, SORTED (local port, then neighbor).
	//
	// Sorted because controller builds inventory diff compared run to run: Go map iteration
	// order varies, would make identical snapshots differ -- rewrite table every cycle and
	// blink screens. Same reason as `facade.names`.
	Neighbors []Neighbor `json:"neighbors"`
	// Served: does device serve LLDP-MIB at all?
	//
	// False and zero neighbors are NOT the same, confusing them is costliest lie here. "Zero
	// neighbors" is observation: LLDP runs, nobody opposite. "Does not serve MIB" means we
	// know NOTHING of this device's neighbors, no meshcraft version will change that --
	// measured on CSS326, full `1.0.8802` walk returns zero lines without error. Screen must
	// say "unknown", not "nothing".
	Served bool `json:"served"`
}

// --- Index decoding, where mistakes happen ---

// remKey identifies one lldpRemTable ROW, without its timestamp.
type remKey struct {
	// LocalPortNum: `lldpLocPortNum`, port that SAW the neighbor.
	LocalPortNum int
	// RemIndex: `lldpRemIndex`, distinguishes multiple neighbors on same port.
	RemIndex int
}

// decodeRemIndex decodes suffix of one lldpRemTable row.
//
// THE function attaching neighbor to local port; error draws credible false link -- worst
// outcome, gap is obvious immediately.
//
// Index is `lldpRemTimeMark.lldpRemLocalPortNum.lldpRemIndex`, THREE arcs, first is shape trap:
//
//   - `lldpRemTimeMark` is TimeFilter (RFC 2021): synthetic index for changed-since queries.
//     Ordinary table walk sees **0** -- measured on both platforms, all rows. NOT data: reading
//     as local port number (two-arc parse) attaches all fleet neighbors to port 0;
//   - agent may return MULTIPLE timestamps for same row. None in reference fleet (survey: all 0),
//     but MIB allows and would duplicate neighbors. Hence descending timestamp sort at assembly,
//     keeping newest observation (cf. LLDPReportFrom).
//
// `suffixIndex` -- rest of package keeps LAST arc only -- would yield `lldpRemIndex` alone here:
// two neighbors on different ports sharing remote index would overwrite each other. LLDP
// collection walks FULL OID names, not `pollClient`.
//
// Returns false unless exactly three numeric arcs: unplaceable row is DROPPED, never guessed.
func decodeRemIndex(suffix string) (mark uint32, key remKey, ok bool) {
	var arcs = strings.Split(strings.Trim(suffix, "."), ".")
	if len(arcs) != 3 {
		return 0, remKey{}, false
	}
	// TimeFilter is TimeTicks, 32-bit unsigned: parsed as uint64 then bounded, not `int` -- OID
	// arc up to 2^32-1 (RFC 2578 section 7.1.3) overflows 32-bit `int` on 32-bit proxy (Raspberry Pi).
	var m, errM = strconv.ParseUint(arcs[0], 10, 32)
	if errM != nil {
		return 0, remKey{}, false
	}
	var port, errP = strconv.Atoi(arcs[1])
	var idx, errI = strconv.Atoi(arcs[2])
	if errP != nil || errI != nil || port <= 0 {
		// `port <= 0`: `lldpLocPortNum` is `LldpPortNumber`, MIB domain 1..4096. Zero invalid,
		// exactly what two-arc decode would produce by mistake.
		return 0, remKey{}, false
	}
	return uint32(m), remKey{LocalPortNum: port, RemIndex: idx}, true
}

// --- Local port name ---

// localPortName picks local port name between `lldpLocPortId` and `lldpLocPortDesc`, order
// entirely from survey.
//
// Neither column alone suffices, measured both sides:
//
//	CRS326, port 26   subtype 5 (interfaceName)  portId "sfp-sfpplus2"
//	                                             portDesc "bridge/sfp-sfpplus2"
//	1930,   port 50   subtype 3 (macAddress)     portId "0x02005e1061d9"
//	                                             portDesc "50"
//
// Always `portDesc` breaks MikroTik: "bridge/sfp-sfpplus2" matches no facade port, `resolve`
// drops it (right -- bridge membership name, not port). Always `portId` breaks 1930 on its two
// neighbor ports. Rule: **subtype decides**, `portDesc` is fallback.
//
// Fallback also when subtype missing, judging value not missing subtype: unusable `portId` text
// is binary id not name -- 1930 puts six MAC bytes, `OctetText` already gave "0x...", recognized
// without guessing.
func localPortName(subtype int, hasSubtype bool, portID, portDesc string) string {
	if hasSubtype && subtype == PortIDSubtypeInterfaceName && portID != "" {
		return portID
	}
	if hasSubtype && subtype != PortIDSubtypeInterfaceName {
		if portDesc != "" {
			return portDesc
		}
		return portID
	}
	// Unknown subtype: judge value. "0x..." is OctetText for non-printable bytes, identifier not name.
	if portID != "" && !strings.HasPrefix(portID, "0x") {
		return portID
	}
	if portDesc != "" {
		return portDesc
	}
	return portID
}

// --- Assembly ---

// LLDPReportFrom assembles snapshot from FULL-OID-indexed walk.
//
// PURE function, same reason as `Samples`: attachment correctness lives here; neighbor on wrong
// port is credible false link. Input table is what survey actually returned -- test replays value
// for value.
//
// `Served` true when ANY LLDP-MIB line read, including purely local. Right boundary: device
// serving `lldpLocChassisId` speaks 802.1AB with simply nobody opposite, vs device serving
// nothing teaches us nothing -- both must read differently on screen.
func LLDPReportFrom(deviceID string, walk map[string]gosnmp.SnmpPDU) LLDPReport {
	var out = LLDPReport{DeviceID: deviceID, Served: len(walk) > 0}

	// Local identity. Both OIDs are SCALARS: instance ".0", tolerated missing so we do not depend on agent formatting.
	out.ChassisID = scalarAt(walk, oidLLDPLocChassisID)
	out.SysName = scalarAt(walk, oidLLDPLocSysName)
	// Identity, for the controller's reachability arbitration. Absent on an agent
	// that serves no MIB-II — left empty rather than guessed, which simply means
	// this proxy never gets to confirm the device.
	out.SysDescr = scalarAt(walk, oidSysDescr)
	out.SysObjectID = scalarAt(walk, oidSysObjectID)

	// Local ports. Three columns indexed by `lldpLocPortNum`, single arc.
	var subtypes = columnInts(walk, oidLLDPLocPortIDSubtype)
	var locIDs = columnTexts(walk, oidLLDPLocPortID)
	var locDescs = columnTexts(walk, oidLLDPLocPortDesc)
	var names = map[int]string{}
	var num int
	for num = range locIDs {
		var st, hasST = subtypes[num]
		names[num] = localPortName(st, hasST, locIDs[num], locDescs[num])
	}
	// Port with ONLY `portDesc` exists: MIB makes `lldpLocPortId` mandatory, but we do not rely on agent goodwill to place neighbor.
	for num = range locDescs {
		if names[num] == "" {
			names[num] = locDescs[num]
		}
	}
	out.PortIDs = sortedNonEmpty(locIDs)

	// Neighbors. Each column reread with three-arc decode, keep most RECENT timestamp per row (cf. decodeRemIndex).
	var marks = map[remKey]uint32{}
	var rows = map[remKey]Neighbor{}
	var cols = []struct {
		oid    string
		assign func(*Neighbor, string)
	}{
		{oidLLDPRemChassisID, func(n *Neighbor, v string) { n.ChassisID = v }},
		{oidLLDPRemPortID, func(n *Neighbor, v string) { n.PortID = v }},
		{oidLLDPRemPortDesc, func(n *Neighbor, v string) { n.PortDesc = v }},
		{oidLLDPRemSysName, func(n *Neighbor, v string) { n.SysName = v }},
		{oidLLDPRemSysDesc, func(n *Neighbor, v string) { n.SysDesc = v }},
	}
	var full string
	var pdu gosnmp.SnmpPDU
	var c int
	for full, pdu = range walk {
		var clean = strings.TrimPrefix(full, ".")
		for c = range cols {
			var suffix, isCol = strings.CutPrefix(clean, cols[c].oid+".")
			if !isCol {
				continue
			}
			var mark, key, ok = decodeRemIndex(suffix)
			if !ok {
				continue
			}
			// Newest timestamp wins and RESETS row: mixing timestamps would composite neighbor with name from one observation and port from another.
			var known, seen = marks[key]
			if seen && mark < known {
				continue
			}
			if !seen || mark > known {
				marks[key] = mark
				if seen {
					rows[key] = Neighbor{}
				}
			}
			var n = rows[key]
			cols[c].assign(&n, valueText(pdu))
			rows[key] = n
			break
		}
	}

	// Remote port subtype not read, assumed asymmetry with local port. There it DECIDES -- pick
	// between two columns to place observation on our facade, mistake assigns wrong port. Here
	// remote port is displayed LABEL: both `PortID` and `PortDesc` sent, controller picks display.
	// Extra column every cycle for non-deciding info buys nothing.
	var key remKey
	for key = range rows {
		var n = rows[key]
		n.LocalPortNum = key.LocalPortNum
		n.LocalPort = names[key.LocalPortNum]
		out.Neighbors = append(out.Neighbors, n)
	}
	sort.Slice(out.Neighbors, func(i, j int) bool {
		var a, b = out.Neighbors[i], out.Neighbors[j]
		if a.LocalPortNum != b.LocalPortNum {
			return a.LocalPortNum < b.LocalPortNum
		}
		if a.ChassisID != b.ChassisID {
			return a.ChassisID < b.ChassisID
		}
		return a.PortID < b.PortID
	})
	return out
}

// scalarAt reads scalar OID, with or without ".0".
func scalarAt(walk map[string]gosnmp.SnmpPDU, oid string) string {
	var pdu, seen = walk[oid+".0"]
	if !seen {
		if pdu, seen = walk["."+oid+".0"]; !seen {
			if pdu, seen = walk[oid]; !seen {
				return ""
			}
		}
	}
	return valueText(pdu)
}

// columnTexts / columnInts extract column indexed by ONE arc.
func columnTexts(walk map[string]gosnmp.SnmpPDU, oid string) map[int]string {
	var out = map[int]string{}
	var full string
	var pdu gosnmp.SnmpPDU
	for full, pdu = range walk {
		var suffix, isCol = strings.CutPrefix(strings.TrimPrefix(full, "."), oid+".")
		if !isCol {
			continue
		}
		var n, err = strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		out[n] = valueText(pdu)
	}
	return out
}

func columnInts(walk map[string]gosnmp.SnmpPDU, oid string) map[int]int {
	var out = map[int]int{}
	var full string
	var pdu gosnmp.SnmpPDU
	for full, pdu = range walk {
		var suffix, isCol = strings.CutPrefix(strings.TrimPrefix(full, "."), oid+".")
		if !isCol {
			continue
		}
		var n, err = strconv.Atoi(suffix)
		if err != nil {
			continue
		}
		var v, isNum = toFloat(pdu)
		if !isNum {
			continue
		}
		out[n] = int(v)
	}
	return out
}

// sortedNonEmpty returns column values, sorted and deduplicated.
func sortedNonEmpty(m map[int]string) []string {
	var seen = map[string]bool{}
	var out []string
	var k int
	for k = range m {
		if m[k] == "" || seen[m[k]] {
			continue
		}
		seen[m[k]] = true
		out = append(out, m[k])
	}
	sort.Strings(out)
	return out
}

// valueText returns LLDP value as text, lossless.
//
// Neighbor data is strings; approximate conversion is more dangerous: `json.Marshal` silently
// replaces non-UTF-8 with U+FFFD, six-byte raw `lldpRemChassisId` arrives transformed at
// controller -- irreconcilable with same chassis seen elsewhere, no substitution signal. Defect
// `OctetText` prevents; path does not use `asString`.
//
// Integers use metric path (`toFloat`): only integers here are LLDP-MIB subtypes, domain fits one byte.
func valueText(pdu gosnmp.SnmpPDU) string {
	var raw, isBytes = pdu.Value.([]byte)
	if isBytes {
		return OctetText(raw)
	}
	var s, isText = pdu.Value.(string)
	if isText {
		return s
	}
	var n, isNum = toFloat(pdu)
	if isNum {
		return strconv.FormatInt(int64(n), 10)
	}
	return ""
}

// OctetText returns OctetString without corrupting value.
//
// OctetString is NOT text: byte sequence. `ifDescr` may be text ("GigabitEthernet1/0/1"), but
// `lldpRemChassisId` carries six raw bytes, some vendors store binary structures. json.Marshal
// SILENTLY replaces invalid UTF-8 with U+FFFD: value arrives transformed at controller with no
// signal -- MAC address lost forever.
//
// CHOICE: lowercase prefixed hex "0x" when bytes not printable text. Prefix essential -- without
// it "001b2f" indistinguishable from port label literally named that. Tabs and newlines stay text:
// multiline `sysDescr` readable, json.Marshal escapes correctly.
//
// # Why this function MOVED here
//
// Lived in `cmd/proxy/rpc.go` for diagnostic walk; original comment explained why not in
// `snmppoll`: `toFloat` and `asString` serve different need -- metric float, refuse non-numeric.
// Still true, functions stay separate.
//
// What changed: SECOND caller in `snmppoll` itself -- LLDP collection needs same fidelity on same
// bytes, chassis-id. Copying would repeat fault this repo fixes elsewhere (cf. package header on
// connection conventions written twice): two implementations of one rule diverge, one prefix char
// makes diagnostic chassis incomparable with collection chassis -- with no error.
func OctetText(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if printableOctets(b) {
		return string(b)
	}
	const hexDigits = "0123456789abcdef"
	var out = make([]byte, 0, 2+2*len(b))
	out = append(out, '0', 'x')
	var c byte
	for _, c = range b {
		out = append(out, hexDigits[c>>4], hexDigits[c&0x0f])
	}
	return string(out)
}

// printableOctets reports whether bytes render as-is: valid UTF-8, no control chars except tab, CR, LF.
func printableOctets(b []byte) bool {
	if !utf8.Valid(b) {
		return false
	}
	var r rune
	for _, r = range string(b) {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		// C0 and C1 controls plus DEL: none belong in displayed value; presence marks binary bytes.
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return false
		}
	}
	return true
}

// --- Collection proper ---

// lldpOIDs: roots to walk, cheapest to most expensive first.
//
// SEVEN walks not one of `1.0.8802`, tradeoff worth stating because counter-intuitive. Full root
// walk gets everything in one pass but also useless data: `lldpStatistics` (ten lines on 1930),
// `lldpConfiguration` per port per TLV, two capability columns per neighbor. On 48-port device,
// `lldpPortConfigTable` alone is four columns x 52 ports, more lines than rest combined -- values
// no screen shows.
//
// Seven narrow roots cost seven extra round trips per cycle per device, every thirty seconds. Fair
// trade: LLDP-MIB is wide and shallow, useful columns split across two subtrees.
func lldpOIDs() []string {
	return []string{
		oidLLDPLocChassisID, oidLLDPLocSysName,
		oidLLDPLocPortIDSubtype, oidLLDPLocPortID, oidLLDPLocPortDesc,
		oidLLDPRemChassisID, oidLLDPRemPortID, oidLLDPRemPortDesc,
		oidLLDPRemSysName, oidLLDPRemSysDesc,
		oidSysDescr, oidSysObjectID,
	}
}

// TableClient is walk that KEEPS full OID name.
//
// Distinct from `Client` -- table indexed by last arc -- mirror of `pollClient` inverted: poller
// wants one PDU per interface index for metrics, wrong for three-arc index table (cf.
// decodeRemIndex). Using `Client` on lldpRemTable would overwrite neighbors across ports.
type TableClient interface {
	WalkFull(ctx context.Context, oid string) (map[string]gosnmp.SnmpPDU, error)
	Close() error
}

// PollLLDP queries neighbor data and returns snapshot.
//
// `DialTable` injectable for tests, like poller `Dial`.
//
// Missing root is NOT error: CSS326 serves nothing under `1.0.8802`, platform fact documented by
// connector. Return snapshot with `Served: false` rather than error, because "device does not speak
// LLDP" is an answer not failure -- error would log incident every cycle on healthy fleet.
func (p *Poller) PollLLDP(ctx context.Context) (LLDPReport, error) {
	var dial = p.DialTable
	if dial == nil {
		dial = dialTableSNMP
	}
	var c, err = dial(p.Target)
	if err != nil {
		return LLDPReport{DeviceID: p.Target.DeviceID}, err
	}
	defer c.Close() //nolint:errcheck

	var walk = map[string]gosnmp.SnmpPDU{}
	var reached bool
	var oid string
	for _, oid = range lldpOIDs() {
		var part, walkErr = c.WalkFull(ctx, oid)
		if walkErr != nil {
			// Unreadable subtree does not condemn others: return what we read, like `Poll`. But
			// device where NO root answered is mute device, not same as "does not serve LLDP" --
			// hence `reached` distinguishes network silence from MIB silence.
			continue
		}
		reached = true
		var k string
		for k = range part {
			walk[k] = part[k]
		}
	}
	if !reached {
		return LLDPReport{DeviceID: p.Target.DeviceID}, errNoTable
	}
	return LLDPReportFrom(p.Target.DeviceID, walk), nil
}

// tableClient adapts Session to TableClient: FULL-OID-indexed view for multi-arc index decode.
type tableClient struct{ s *Session }

func dialTableSNMP(t Target) (TableClient, error) {
	var s, err = Dial(t)
	if err != nil {
		return nil, err
	}
	return &tableClient{s: s}, nil
}

func (c *tableClient) WalkFull(ctx context.Context, oid string) (map[string]gosnmp.SnmpPDU, error) {
	var out = map[string]gosnmp.SnmpPDU{}
	var err = c.s.BulkWalk(oid, func(pdu gosnmp.SnmpPDU) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out[strings.TrimPrefix(pdu.Name, ".")] = pdu
		return nil
	})
	return out, err
}

func (c *tableClient) Close() error { return c.s.Close() }

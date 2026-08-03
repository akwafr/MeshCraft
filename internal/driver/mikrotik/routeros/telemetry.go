package routeros

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/metricbuf"
)

// RouterOS telemetry: what the device reports about its own health, counters,
// and transceivers, and which channel is used to obtain it.
//
// # How the three channels split roles — that is the whole file
//
// None of the three channels on this platform returns everything, and they are
// not three ways to do the same thing. The table below reflects OBSERVED
// behavior, not preference:
//
//	                           SNMP        REST        BINARY API
//	link state                 yes         yes         yes
//	NEGOTIATED link rate       yes         NO          yes (one request per port)
//	bytes / packets            yes         yes         yes
//	detailed errors            partial     yes         yes
//	link-down counter          no          yes         yes
//	CPU / memory / uptime      yes         yes         yes
//	board temperature          yes         yes         yes
//	SFP OPTICAL DIAGNOSTICS    yes         NO          yes
//
// Two cells are worth pausing on, because they decide the channel:
//
//   - NEGOTIATED rate is NOT in REST. This was OBSERVED on captures: the 87 fields
//     of `/interface/ethernet` carry no `speed`, and `/interface` carries none
//     either. There is only `advertise`, which says what the port OFFERS, not
//     what it negotiated. A port stuck at 100 Mbit/s because of a cut pair is
//     therefore INVISIBLE through REST — the sneakiest failure mode on copper,
//     since everything works, ten times slower. Rate comes from
//     `/interface/ethernet/monitor`, which is a command, not a menu;
//   - OPTICAL DIAGNOSTICS come from the same `monitor`. The connector already said
//     this and drew the only honest conclusion at the time: 'it could not be
//     READ — `monitor` is a POST, out of scope for a tool restricted to GET. It
//     therefore had neither channel nor unavailability entry'. This file
//     gives it its channel.
//
// # What this file is NOT
//
// It is not a collector. It has no loop, cadence, or buffer: it returns a
// SNAPSHOT, and [Telemetry.Samples] translates it into the project's series
// vocabulary. The product's periodic collection runs on the PROXY and speaks only
// SNMP (`internal/snmppoll`, `cmd/proxy/snmp.go`); wiring a connector channel
// into that loop would require the proxy to hold fleet administration
// credentials, which is an architecture decision, not a hook. That decision is
// NOT taken here.
//
// Until then, what these reads actually serve: on-demand diagnostics from the
// controller — the screen that answers 'why is this link slow?' and
// 'is this transceiver near end of life?' — and the proven foundation
// for the day connector-based collection exists. That is stated here rather than
// discovered in front of an empty graph.

// TelemetrySource is the transport for a telemetry read.
//
// The interface exists because TWO transports in this package can read the same
// menus, and neither should impose its shape on translation: translation is what
// goes wrong, and it must be exercised on captures without opening any socket.
//
// The split also tells the operator something useful: `Monitor` is the only
// method the two transports do not honor the same way, and that is exactly the
// boundary OBSERVED between 'menu' and 'command' on this
// platform.
type TelemetrySource interface {
	// Print reads a menu and returns its records. The menu is given WITHOUT its
	// verb ('interface', 'system/resource'): the transport
	// knows whether to append `/print`.
	Print(ctx context.Context, menu string) ([]Row, error)
	// Monitor runs a menu's `monitor` command on a named object, in `once` mode —
	// one read, not a stream.
	//
	// Returns an error when the transport cannot do this. That error must be
	// treated as ABSENCE OF CHANNEL, not failure: the difference between
	// 'this transceiver has no sensor' and 'this path cannot read it'.
	Monitor(ctx context.Context, menu, name string) (Row, error)
}

// Both transports in this package honor the contract.
var (
	_ TelemetrySource = (*APIConn)(nil)
	_ TelemetrySource = (*Client)(nil)
)

// Print on the binary API: the menu followed by its verb.
func (c *APIConn) Print(ctx context.Context, menu string) ([]Row, error) {
	return c.Do(ctx, "/"+strings.Trim(menu, "/")+"/print")
}

// Monitor on the binary API: the reason this channel exists.
//
// `=once=` is a FLAG — a present, empty attribute. Without it, `monitor` opens a
// STREAM: the device emits an `!re` sentence on every change and never returns
// its `!done`, so the read would wait until the timeout and then return 'no
// response' for a command that worked perfectly.
//
// `=numbers=` takes the interface NAME, not an index. This is what this platform
// does better than others: meshcraft names ARE device names, there is no
// translation to do, therefore no chance of targeting the wrong port.
func (c *APIConn) Monitor(ctx context.Context, menu, name string) (Row, error) {
	var rows, err = c.Do(ctx, "/"+strings.Trim(menu, "/")+"/monitor",
		"=numbers="+name, "=once=")
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return Row{}, nil
	}
	return rows[0], nil
}

// Print on REST: exactly [Client.List].
func (c *Client) Print(ctx context.Context, menu string) ([]Row, error) {
	return c.List(ctx, menu)
}

// Monitor on REST. UNVERIFIED ON HARDWARE, and the direction matters.
//
// The shape matches every command on this API — a POST to
// `<menu>/<command>` — and [Client.Command] already emits that for writes.
// Nothing says it would fail; nothing says it would succeed either, and the
// reason has been documented since the connector's origin: the tool available to
// the reference device was restricted to GET/HEAD, so NO POST was ever exercised
// on it.
//
// The path is open rather than refused, because a blanket refusal would be worse:
// it would make the platform look incapable of something it is probably capable
// of, and that is exactly the confusion [driver.MetricUnavailable] exists to
// prevent. What protects the operator is therefore not a refusal, but the
// corresponding channel declaration carrying its high floor and its 'not
// exercised' note.
func (c *Client) Monitor(ctx context.Context, menu, name string) (Row, error) {
	var rows, err = c.Command(ctx, menu, "monitor", map[string]string{
		"numbers": name,
		"once":    "yes",
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return Row{}, nil
	}
	return rows[0], nil
}

// Sensor is a sensor from `/system/health`, as the device names it.
//
// The three fields are those of the menu, and TYPE decides the unit: OBSERVED
// `[{"name":"cpu-temperature","type":"C","value":"72"}]` on the reference device.
// RouterOS 7 replaced the fixed fields of earlier versions (`temperature`,
// `voltage`, `cpu-temperature`…) with this named table, which is good news: the
// sensor list becomes a property of the BOARD, not a list to maintain per
// product line in our code.
type Sensor struct {
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

// PortCounters is telemetry for one port, across all channels.
//
// Fields are POINTERS for what can legitimately be missing, and that distinction
// carries this whole type: `0` and 'not read' are not the same thing
// on a counter. A port whose errors could not be read and a port with none look
// like zero and mean the opposite — the first must produce NO sample, otherwise
// the graph claims an absence of errors that nobody OBSERVED.
type PortCounters struct {
	Name string `json:"name"`
	// Running: does the port carry a link. Returned by all channels.
	Running bool `json:"running"`
	// SpeedMbps: NEGOTIATED rate, in Mbit/s. Nil when not read — which is
	// everything that does not go through `monitor` (see header).
	SpeedMbps *float64 `json:"speed_mbps,omitempty"`
	// LinkDowns: link-down events counted by the device.
	LinkDowns *float64 `json:"link_downs,omitempty"`

	InOctets   *float64 `json:"in_octets,omitempty"`
	OutOctets  *float64 `json:"out_octets,omitempty"`
	InPackets  *float64 `json:"in_packets,omitempty"`
	OutPackets *float64 `json:"out_packets,omitempty"`
	// InErrors / OutErrors: the SUM of error counters RouterOS publishes in this
	// shape. The exact list is in [foldEthernetCounters] — enumerated there rather
	// than summarized here, because a total whose contents are unknown is not a
	// measurement.
	InErrors  *float64 `json:"in_errors,omitempty"`
	OutErrors *float64 `json:"out_errors,omitempty"`
	// InDiscards / OutDiscards: VALID frames dropped for lack of space.
	InDiscards  *float64 `json:"in_discards,omitempty"`
	OutDiscards *float64 `json:"out_discards,omitempty"`
}

// Optic is diagnostics for an SFP cage, as `monitor` returns them.
type Optic struct {
	Port string `json:"port"`
	// Present: an EMPTY cage is not a failed cage. RouterOS says so by absence of
	// `sfp-*` fields; not distinguishing would alert on both free cages of every
	// access switch.
	Present bool   `json:"present"`
	Vendor  string `json:"vendor,omitempty"`
	Part    string `json:"part,omitempty"`
	Serial  string `json:"serial,omitempty"`

	TemperatureC  *float64 `json:"temperature_c,omitempty"`
	SupplyVolts   *float64 `json:"supply_volts,omitempty"`
	TxBiasMA      *float64 `json:"tx_bias_ma,omitempty"`
	TxPowerDBM    *float64 `json:"tx_power_dbm,omitempty"`
	RxPowerDBM    *float64 `json:"rx_power_dbm,omitempty"`
	WavelengthNM  *float64 `json:"wavelength_nm,omitempty"`
	RxLoss        *bool    `json:"rx_loss,omitempty"`
	TxFault       *bool    `json:"tx_fault,omitempty"`
	ShutdownTempC *float64 `json:"shutdown_temperature_c,omitempty"`
}

// Telemetry is the complete snapshot.
type Telemetry struct {
	At time.Time `json:"at"`

	CPULoadPercent *float64 `json:"cpu_load_percent,omitempty"`
	CPUCount       int      `json:"cpu_count,omitempty"`
	FreeMemory     *float64 `json:"free_memory_bytes,omitempty"`
	TotalMemory    *float64 `json:"total_memory_bytes,omitempty"`
	UptimeSeconds  *float64 `json:"uptime_seconds,omitempty"`
	Version        string   `json:"version,omitempty"`
	BoardName      string   `json:"board_name,omitempty"`

	Sensors []Sensor       `json:"sensors,omitempty"`
	Ports   []PortCounters `json:"ports,omitempty"`
	Optics  []Optic        `json:"optics,omitempty"`

	// Notes tells the operator what this snapshot does not contain, and WHY.
	//
	// Sentences, like [State.Notes], for the same reason: the caller displays them
	// as-is and makes no decision from them. A partial read is more useful than an
	// error — but a partial read that does not say what is missing is a lie by
	// omission, and on a diagnostic screen it sends you looking for device failure
	// where there is a channel that cannot read.
	Notes []string `json:"notes,omitempty"`
}

// ReadTelemetry returns the snapshot. READ-ONLY.
//
// # ERROR DOES NOT STOP THE READ — that is intentional
//
// Each source is independent: health does not depend on counters, counters do not
// depend on transceivers. Aborting on the first failure would lose board
// temperature because an optical module did not respond — yet that is exactly
// when this screen is opened.
//
// What makes the approach honest rather than lax: every gap produces a NOTE
// naming the source and reason. Returning a half-empty structure without saying
// so would be the fault; returning it while saying which of the five reads failed
// is what diagnostics should do.
//
// `optics` costs nothing on hardware WITHOUT an SFP cage: the port list to query
// is DERIVED from names containing `sfp`, so zero requests on a 24-port copper
// switch. That is a NAME heuristic and is assumed as such — on this platform
// names come from the device (`sfp-sfpplus1`), and a copper port is never named
// that way.
func ReadTelemetry(ctx context.Context, src TelemetrySource) (Telemetry, error) {
	var out = Telemetry{At: time.Now()}
	var failures []string

	var res, err = src.Print(ctx, "system/resource")
	if err != nil {
		failures = append(failures, "system health (`/system/resource`: "+err.Error()+
			") — no CPU, memory, or uptime")
	} else if len(res) > 0 {
		foldResource(res[0], &out)
	}

	var health []Row
	health, err = src.Print(ctx, "system/health")
	if err != nil {
		failures = append(failures, "sensors (`/system/health`: "+err.Error()+
			") — no temperature, voltage, or fan")
	} else {
		out.Sensors = foldHealth(health)
	}

	var ifaces []Row
	ifaces, err = src.Print(ctx, "interface")
	if err != nil {
		// Without `/interface`, there is no port list: detailed counters and
		// transceivers have nothing to attach to. This is the only failure that
		// causes others, and saying so avoids three notes for one cause.
		failures = append(failures, "port counters (`/interface`: "+err.Error()+
			") — no interface list, therefore no counters, errors, or optical "+
			"diagnostics")
		out.Notes = telemetryNotes(failures)
		return out, err
	}
	var eth []Row
	eth, err = src.Print(ctx, "interface/ethernet")
	if err != nil {
		failures = append(failures, "error counters (`/interface/ethernet`: "+
			err.Error()+") — bytes and packets are present, errors and discards are "+
			"not")
		eth = nil
	}
	out.Ports = foldPorts(ifaces, eth)

	// Negotiated rate and optical diagnostics go through `monitor`, one request
	// per port. We only ask for SFP cages: on a 26-port switch, that is two
	// requests instead of twenty-six.
	var p int
	for p = range out.Ports {
		if !looksOptical(out.Ports[p].Name) {
			continue
		}
		var row Row
		row, err = src.Monitor(ctx, "interface/ethernet", out.Ports[p].Name)
		if err != nil {
			failures = append(failures, "optical diagnostics for "+out.Ports[p].Name+
				" (`/interface/ethernet/monitor`: "+err.Error()+")")
			continue
		}
		var optic = foldOptic(out.Ports[p].Name, row)
		out.Optics = append(out.Optics, optic)
		// `monitor` is also the ONLY source of negotiated rate (see header). Since
		// the request is made, we take both.
		var speed, ok = parseRate(row.Get("rate"))
		if ok {
			out.Ports[p].SpeedMbps = &speed
		}
	}

	out.Notes = telemetryNotes(failures)
	return out, nil
}

// telemetryNotes turns gaps into sentences, or returns nil when there are none.
func telemetryNotes(failures []string) []string {
	if len(failures) == 0 {
		return nil
	}
	return []string{"PARTIAL READ — " + strconv.Itoa(len(failures)) +
		" source(s) did not respond: " + strings.Join(failures, "; ") +
		". What is shown is what could be read. Do NOT read this as absence of " +
		"measurement on the device: a channel that cannot read and a sensor that " +
		"does not exist are fixed by opposite actions."}
}

// looksOptical reports whether a port name designates an optical cage.
//
// On this platform the name comes from the DEVICE (OBSERVED: `sfp-sfpplus1`,
// `sfp-sfpplus2`), not meshcraft: that is what makes the heuristic tenable. It
// remains a heuristic, and its only cost when wrong is one extra `monitor`
// request on a copper port — whose response will simply carry no `sfp-*` fields,
// so `Present: false`. The reverse (missing a cage) would cost a missing curve,
// which is why the match is BROAD: any name containing 'sfp'.
func looksOptical(name string) bool {
	return strings.Contains(strings.ToLower(name), "sfp")
}

// --- Translations, all PURE ---

// foldResource reads `/system/resource`.
//
// OBSERVED on the reference device: `cpu-load: "1"`, `free-memory:
// "450670592"`, `total-memory: "536870912"`, `uptime: "1d17h38m11s"`,
// `cpu-count: "2"`, `version: "7.21beta3 (testing)"`.
//
// ALL VALUES ARE STRINGS, including numbers — that is what the device serves,
// and it holds for both channels (REST and binary API serve the same data model).
func foldResource(r Row, out *Telemetry) {
	out.Version = r.Get("version")
	out.BoardName = r.Get("board-name")
	var n, ok = r.Int("cpu-count")
	if ok {
		out.CPUCount = n
	}
	assignFloat(&out.CPULoadPercent, r.Get("cpu-load"))
	assignFloat(&out.FreeMemory, r.Get("free-memory"))
	assignFloat(&out.TotalMemory, r.Get("total-memory"))
	var secs, hasUptime = ParseUptime(r.Get("uptime"))
	if hasUptime {
		out.UptimeSeconds = &secs
	}
}

// ParseUptime converts RouterOS duration to seconds.
//
// The form is the device's, OBSERVED: `1d17h38m11s`, and chattier ranges add
// weeks (`3w2d4h`). This is NOT the format `time.ParseDuration` accepts — it
// knows neither `d` nor `w` — and passing the string as-is fails silently on a
// perfectly readable field.
//
// THE UNIT IS SECONDS, and the operational value of this series is not duration
// but its BREAK: uptime that drops is a reboot. That is the only way to notice a
// device rebooted between two polls without staying unreachable long enough for
// ping to notice — a switch reboot takes about thirty seconds, ICMP monitoring
// sees three losses.
//
// A string with one unreadable piece is REJECTED whole, rather than returned
// truncated: '4 h' where the device says '3 weeks and 4 h'
// would suggest a reboot that did not happen, which is exactly the event this
// measurement exists to detect.
func ParseUptime(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, false
	}
	var units = map[byte]float64{
		'w': 7 * 24 * 3600,
		'd': 24 * 3600,
		'h': 3600,
		'm': 60,
		's': 1,
	}
	var total float64
	var digits string
	var seen bool
	var i int
	for i = 0; i < len(s); i++ {
		var c = s[i]
		if c >= '0' && c <= '9' {
			digits += string(c)
			continue
		}
		var mult, known = units[c]
		if !known || digits == "" {
			return 0, false
		}
		var v, err = strconv.ParseFloat(digits, 64)
		if err != nil {
			return 0, false
		}
		total += v * mult
		digits = ""
		seen = true
	}
	// Trailing digits after the last unit: the string is truncated, so the
	// duration we would derive would be wrong by an unknown factor.
	if digits != "" || !seen {
		return 0, false
	}
	return total, true
}

// foldHealth reads the sensor table from `/system/health`.
//
// An entry whose value is not a number is SKIPPED, not converted to zero: a
// temperature of 0 °C reads as a very cold room, and that is an operational fact
// we must not invent.
//
// Sort is by name, and it is not cosmetic: this list goes to a diagnostic screen
// and to JSON compared across reads. A RouterOS menu order is not guaranteed
// stable, and two identical reads must produce two identical outputs, otherwise
// comparing two snapshots means nothing.
func foldHealth(rows []Row) []Sensor {
	var out []Sensor
	var r Row
	for _, r = range rows {
		var name = r.Get("name")
		if name == "" {
			continue
		}
		var v, err = strconv.ParseFloat(strings.TrimSpace(r.Get("value")), 64)
		if err != nil {
			continue
		}
		out = append(out, Sensor{Name: name, Type: r.Get("type"), Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// foldPorts assembles per-port counters from the two menus that carry them.
//
// TWO MENUS, NOT ONE, and you need to know which says what because field names
// are similar enough to trap you:
//
//	/interface           `rx-byte`, `tx-byte` (SINGULAR), `rx-packet`, `tx-packet`,
//	                     `link-downs`, `tx-queue-drop`, `running`
//	/interface/ethernet  `rx-bytes`, `tx-bytes` (PLURAL), and all error counters:
//	                     `rx-fcs-error`, `rx-fragment`, `rx-jabber`,
//	                     `rx-too-long`, `rx-too-short`, `rx-overflow`,
//	                     `tx-collision`, `tx-late-collision`, `tx-underrun`,
//	                     `tx-drop-packet`
//
// Singular/plural is not a typo in this comment: they are two different spellings
// in two different menus, OBSERVED as-is in captures. Reading `rx-bytes` in
// `/interface` returns an empty string, so a silently missing counter — the
// hardest failure mode to see, because the graph exists and stays flat.
//
// OCTETS kept are from `/interface`, never from `/interface/ethernet`. That is
// not arbitrary: `/interface` covers ALL ports, including those that are not
// Ethernet interfaces, while `/interface/ethernet` exists only for copper and
// cages. Taking the second source would drop counters for a bond or VLAN interface
// without saying so.
func foldPorts(ifaces, eth []Row) []PortCounters {
	var byName = map[string]Row{}
	var r Row
	for _, r = range eth {
		var n = r.Get("name")
		if n != "" {
			byName[n] = r
		}
	}

	var out []PortCounters
	for _, r = range ifaces {
		var name = r.Get("name")
		if name == "" {
			continue
		}
		var pc = PortCounters{Name: name, Running: r.Bool("running")}
		assignFloat(&pc.InOctets, r.Get("rx-byte"))
		assignFloat(&pc.OutOctets, r.Get("tx-byte"))
		assignFloat(&pc.InPackets, r.Get("rx-packet"))
		assignFloat(&pc.OutPackets, r.Get("tx-packet"))
		assignFloat(&pc.LinkDowns, r.Get("link-downs"))

		var e, seen = byName[name]
		if seen {
			foldEthernetCounters(e, r, &pc)
		}
		out = append(out, pc)
	}
	sort.Slice(out, func(i, j int) bool { return lessPortName(out[i].Name, out[j].Name) })
	return out
}

// foldEthernetCounters sums fault counters from `/interface/ethernet`.
//
// # What is summed, enumerated — a total whose contents are unknown is not a measurement
//
//	RECEIVE errors       rx-fcs-error, rx-fragment, rx-jabber, rx-too-long,
//	                     rx-too-short
//	RECEIVE discards     rx-overflow
//	TRANSMIT errors      tx-collision, tx-late-collision, tx-underrun
//	TRANSMIT discards    tx-drop-packet (ethernet menu)
//	                     + tx-queue-drop (interface menu)
//
// # What is NOT summed, and why that is the delicate point
//
// `rx-error-events` is left OUT. It is an aggregate counter from the Marvell
// chip: it covers all or part of the five receive counters above, and nothing in
// the OBSERVED data says which. Adding it would count some frames TWICE, and the
// symptom would be a plausible, exaggerated error curve — the worst outcome,
// since it would make you replace a healthy patch cord.
//
// # The error / discard boundary decides the action
//
// An ERROR blames cable, connector, or transceiver: the frame arrived damaged. A
// DISCARD blames sizing: the frame was good, there was no room. Adding them —
// as MIB-II `ifInErrors` does on some platforms — would send you looking for a
// physical fault on a simply saturated link. RouterOS separates the two, and
// that is one of the rare things this channel does better than SNMP.
func foldEthernetCounters(e, iface Row, pc *PortCounters) {
	pc.InErrors = sumFields(e, "rx-fcs-error", "rx-fragment", "rx-jabber",
		"rx-too-long", "rx-too-short")
	pc.InDiscards = sumFields(e, "rx-overflow")
	pc.OutErrors = sumFields(e, "tx-collision", "tx-late-collision", "tx-underrun")

	// Transmit discards come from BOTH menus: `tx-drop-packet` is chip discard,
	// `tx-queue-drop` is the interface software queue. Both exist, and taking only
	// one would leave a loss mode invisible.
	var chip = sumFields(e, "tx-drop-packet")
	var queue = sumFields(iface, "tx-queue-drop")
	switch {
	case chip != nil && queue != nil:
		var total = *chip + *queue
		pc.OutDiscards = &total
	case chip != nil:
		pc.OutDiscards = chip
	default:
		pc.OutDiscards = queue
	}
}

// sumFields sums fields and returns nil if NONE were read.
//
// The distinction is that of this whole file: 'none of these counters exist
// on this firmware' and 'they exist and are all zero' look like
// zero and mean the opposite. The first must produce no sample; the second
// produces one at zero, stating something true.
//
// A missing field AMONG present ones does not fail the sum: firmware that does
// not count `rx-jabber` is not a reason to lose checksum errors.
func sumFields(r Row, fields ...string) *float64 {
	var total float64
	var any bool
	var f string
	for _, f = range fields {
		var raw = strings.TrimSpace(r.Get(f))
		if raw == "" {
			continue
		}
		var v, err = strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		total += v
		any = true
	}
	if !any {
		return nil
	}
	return &total
}

// assignFloat sets a numeric value when readable, and touches nothing otherwise.
// The single-field counterpart of [sumFields].
func assignFloat(dst **float64, raw string) {
	var v, err = strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return
	}
	*dst = &v
}

// parseRate converts the `rate` field from `/interface/ethernet/monitor` to Mbit/s.
//
// RouterOS forms are `10Mbps`, `100Mbps`, `1Gbps`, `10Gbps`, and casing varies by
// product line. An unknown value returns `ok=false` rather than zero: a port at
// '0 Mbit/s' reads as a dead link, when it is a label we could not
// translate — and the two are fixed by opposite actions.
//
// The NUMBER in the string is NOT interpreted alone: `10Gbps` and `10Mbps` differ
// by one letter, and reading '10' without its unit would be off by a
// factor of a thousand. Same trap as the `2S+` vs `2S` nomenclature in
// [parseDesignation], and it costs the same factor.
func parseRate(raw string) (float64, bool) {
	var s = strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0, false
	}
	var mult float64
	switch {
	case strings.HasSuffix(s, "gbps"):
		mult, s = 1000, strings.TrimSuffix(s, "gbps")
	case strings.HasSuffix(s, "mbps"):
		mult, s = 1, strings.TrimSuffix(s, "mbps")
	case strings.HasSuffix(s, "kbps"):
		mult, s = 0.001, strings.TrimSuffix(s, "kbps")
	default:
		return 0, false
	}
	var v, err = strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v * mult, true
}

// foldOptic reads what `monitor` says about a transceiver.
//
// # Units are THE DEVICE'S, and RouterOS already serves them converted
//
// That differs from the other two channels, and it simplifies this code rather
// than complicating it:
//
//	`monitor`  serves ready values: `sfp-temperature: 50`,
//	           `sfp-supply-voltage: 3.261`, `sfp-tx-power: -3.2` (already dBm)
//	SNMP       serves SCALED integers: mtxrOpticalTxPower is in thousandths of
//	           dBm, voltage in millivolts (see internal/snmppoll)
//	SwOS       serves RAW integers: power in tenths of microwatt, converted via
//	           logarithm (see the neighboring connector)
//
// Three channels, three scales for the same physical quantity. That is exactly
// why conversion lives next to each read and never in the series vocabulary:
// that vocabulary imposes only a UNIT (dBm), not a method to get there.
//
// # An empty cage is not a failed cage
//
// RouterOS says so by ABSENCE of `sfp-*` fields, not a sentinel value — unlike
// SwOS, which returns a temperature of -128 °C. What decides `Present` is
// presence of at least one module field, not a threshold: inventing a threshold
// would mark absent a transceiver legitimately frozen in an unheated room.
func foldOptic(port string, r Row) Optic {
	var out = Optic{
		Port:   port,
		Vendor: strings.TrimSpace(r.Get("sfp-vendor-name")),
		Part:   strings.TrimSpace(r.Get("sfp-vendor-part-number")),
		Serial: strings.TrimSpace(r.Get("sfp-vendor-serial")),
	}
	assignFloat(&out.TemperatureC, r.Get("sfp-temperature"))
	assignFloat(&out.SupplyVolts, r.Get("sfp-supply-voltage"))
	assignFloat(&out.TxBiasMA, r.Get("sfp-tx-bias-current"))
	assignFloat(&out.TxPowerDBM, r.Get("sfp-tx-power"))
	assignFloat(&out.RxPowerDBM, r.Get("sfp-rx-power"))
	assignFloat(&out.WavelengthNM, r.Get("sfp-wavelength"))
	assignFloat(&out.ShutdownTempC, r.Get("sfp-shutdown-temperature"))
	assignBool(&out.RxLoss, r, "sfp-rx-loss")
	assignBool(&out.TxFault, r, "sfp-tx-fault")

	out.Present = out.TemperatureC != nil || out.RxPowerDBM != nil ||
		out.TxPowerDBM != nil || out.Vendor != "" || out.Part != ""
	return out
}

// assignBool sets a flag when the field EXISTS.
//
// Existence, not value: `Row.Bool` returns false for both 'false' and
// a missing field, and on an alarm flag the two do not mean the same thing —
// 'this module reports no signal loss' versus 'this firmware does
// not say'. Publishing the second as the first would show 'no alarm'
// on hardware that reports none because it never reports any.
func assignBool(dst **bool, r Row, field string) {
	var raw, ok = r[field]
	if !ok {
		return
	}
	var v = strings.TrimSpace(strings.ToLower(raw)) == "true"
	*dst = &v
}

// lessPortName orders two port names as the UI presents them: by prefix then
// NUMBER. Same rule as [sortPortNames], borrowing its split — alphabetical sort
// would place `ether10` before `ether2`.
func lessPortName(a, b string) bool {
	var pa, na = splitPortName("", a)
	var pb, nb = splitPortName("", b)
	if pa != pb {
		return pa < pb
	}
	return na < nb
}

// --- Translation to series vocabulary ---

// Samples translates the snapshot into the project's series vocabulary.
//
// PURE function, and that is where correctness matters: `internal/snmppoll` says
// the same of its own translation, for the same reason — associating a counter
// with the wrong port produces a credible, false graph, which is worse than a gap.
//
// # What these samples deliberately do NOT carry
//
// Neither `ifindex` nor `iftype`. This channel has no interface index to give:
// the device designates ports by NAME, and fabricating an index from list rank
// would invent a key nothing guarantees stable — an added port would shift all
// following ones, reassigning one port's traffic to another.
//
// The consequence is fortunate: the controller requires `iftype` to touch
// inventory (§2.1 of the unified monitoring and pipelines note), so these samples feed
// curves WITHOUT ever being able to modify the UI. The rule 'telemetry never
// CREATES a port' is obtained here by construction, not by a guard that
// must be remembered.
//
// Attachment to a port is then by NAME, the most favorable of the three cases
// described in the architecture note: on this platform meshcraft names ARE device
// names, so equality is exact and no normalization is needed.
func (t Telemetry) Samples(deviceID string) []metricbuf.Sample {
	var out []metricbuf.Sample
	var at = t.At
	if at.IsZero() {
		at = time.Now()
	}
	var base = map[string]string{"device": deviceID}

	var add = func(name string, v *float64, labels map[string]string) {
		if v == nil {
			return
		}
		out = append(out, metricbuf.Sample{
			Name: name, Labels: labels, Value: *v, TS: at,
		})
	}

	add(metricbuf.MetricCPULoad, t.CPULoadPercent, base)
	add(metricbuf.MetricMemoryFree, t.FreeMemory, base)
	add(metricbuf.MetricMemoryTotal, t.TotalMemory, base)
	add(metricbuf.MetricUptime, t.UptimeSeconds, base)

	var s Sensor
	for _, s = range t.Sensors {
		var labels = map[string]string{"device": deviceID, "sensor": s.Name}
		var v = s.Value
		// TYPE DECIDES THE SERIES, never the name. RouterOS 7 serves `type: "C"`
		// for temperature and `"V"` for voltage; trusting the name would require
		// maintaining a label list per product line, and a differently named sensor
		// would land in the wrong series — 3.3 V plotted among temperatures.
		switch strings.ToUpper(strings.TrimSpace(s.Type)) {
		case "C":
			add(metricbuf.MetricTemperature, &v, labels)
		case "V":
			add(metricbuf.MetricVoltage, &v, labels)
		case "RPM":
			add(metricbuf.MetricFanRPM, &v, labels)
		default:
			// A sensor with unknown type is NOT placed in a random series: without
			// a unit, its value means nothing, and inventing one would alert on a
			// threshold that does not apply. It remains visible in [Telemetry.Sensors],
			// on the diagnostic screen.
		}
	}

	var p PortCounters
	for _, p = range t.Ports {
		var labels = map[string]string{"device": deviceID, "port": p.Name}
		var state float64
		if p.Running {
			state = 1
		}
		out = append(out, metricbuf.Sample{
			Name: metricbuf.MetricIfOperStatus, Labels: labels, Value: state, TS: at,
		})
		add(metricbuf.MetricIfSpeedMbps, p.SpeedMbps, labels)
		add(metricbuf.MetricIfLinkDowns, p.LinkDowns, labels)
		add(metricbuf.MetricIfInOctets, p.InOctets, labels)
		add(metricbuf.MetricIfOutOctets, p.OutOctets, labels)
		add(metricbuf.MetricIfInPackets, p.InPackets, labels)
		add(metricbuf.MetricIfOutPackets, p.OutPackets, labels)
		add(metricbuf.MetricIfInErrors, p.InErrors, labels)
		add(metricbuf.MetricIfOutErrors, p.OutErrors, labels)
		add(metricbuf.MetricIfInDiscards, p.InDiscards, labels)
		add(metricbuf.MetricIfOutDiscards, p.OutDiscards, labels)
	}

	var o Optic
	for _, o = range t.Optics {
		if !o.Present {
			// An EMPTY cage produces no sample. Publishing zeros there would show
			// two dead transceivers on every switch whose cages are free — a
			// permanent, false alert on most of the fleet.
			continue
		}
		var labels = map[string]string{"device": deviceID, "port": o.Port}
		add(metricbuf.MetricSFPTemperature, o.TemperatureC, labels)
		add(metricbuf.MetricSFPSupplyVolts, o.SupplyVolts, labels)
		add(metricbuf.MetricSFPTxBiasMilliamps, o.TxBiasMA, labels)
		add(metricbuf.MetricSFPTxPowerDBM, o.TxPowerDBM, labels)
		add(metricbuf.MetricSFPRxPowerDBM, o.RxPowerDBM, labels)
		add(metricbuf.MetricSFPWavelengthNM, o.WavelengthNM, labels)
		addBool(&out, metricbuf.MetricSFPRxLoss, o.RxLoss, labels, at)
		addBool(&out, metricbuf.MetricSFPTxFault, o.TxFault, labels, at)
	}
	return out
}

// addBool publishes a flag, and only when it was READ.
func addBool(out *[]metricbuf.Sample, name string, v *bool,
	labels map[string]string, at time.Time) {
	if v == nil {
		return
	}
	var f float64
	if *v {
		f = 1
	}
	*out = append(*out, metricbuf.Sample{Name: name, Labels: labels, Value: f, TS: at})
}

// --- Refusal that accompanies the channel ---

// ErrNoBinaryAPI is what a binary-API telemetry request returns when that is not
// possible on this access path.
//
// Exists so the refusal is ONE sentence, not three formulations: the API layer
// displays it as-is, and an operator who reads two different messages for the
// same cause looks for two failures.
func ErrNoBinaryAPI(host string, why error) error {
	return driver.NotSupported((&Driver{}).Info().Name,
		"read telemetry via the binary API of "+host,
		fmt.Sprintf("the channel could not be opened (%v). Check the following, in "+
			"this order: does `/ip/service` serve `api` (OBSERVED on 8728, not "+
			"disabled on the reference device); does the account group carry the "+
			"`api` policy — a valid secret in a group without it is rejected with "+
			"the same message as a wrong secret; is the path to the port open "+
			"through the proxy tunnel. Fallback is REST, which returns everything "+
			"EXCEPT negotiated link rate and SFP cage optical diagnostics", why))
}

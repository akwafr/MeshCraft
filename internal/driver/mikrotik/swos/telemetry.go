package swos

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/metricbuf"
)

// The TELEMETRY of a SwOS: what page scraping can return, and what it is the
// ONLY one able to return on this platform.
//
// # Scraping is no longer the only channel, and it remains the only one that returns everything
//
// This file long began with "why scraping, and NOT SNMP", and the reason was
// in the contract: `SNMPVersions` was empty, so `snmpprov.For` concluded
// "SNMP not managed", so `snmpWatchesFor` pushed no target to the proxy. The
// /snmp.b page proved that an agent existed and was enabled, without saying
// which VERSION it answered — and no request had ever been sent to it.
//
// The walk of 29 July 2026 on 192.0.2.36 answered. The connector now declares
// `v2c` and an `snmp` channel (cf. swos.go, and the vault
// [[MikroTik telemetry]] § *The CSS326's SNMP: the lock, and the survey that
// lifted it*). `snmpprov.For` concludes "v2c, NOT compliant with the policy
// floor", which is the truth to display: monitored, and in cleartext.
//
// What follows therefore keeps its full meaning, but for a different reason
// than before: scraping is no longer a fallback, it is the channel that
// returns what the agent does NOT. Two quantities exist only in these pages —
// optical POWERS (`tpw`, `rpw`), the agent's optical table stopping at bias
// current, and the snapshot RATES computed by the device (`rrb`, `trb`),
// which SNMP exposes under no OID. Neither channel subsumes the other.
//
// # What the four pages return, and what it COSTS
//
//	/link.b   link state, enable, duplex, negotiated rate, port names      2,026 B
//	/stats.b  octets, packets, RATES computed by the device, errors      12,694 B
//	/sys.b    chip temperature, uptime                                       780 B
//	/sfp.b    complete optical diagnostics of the cages                     ~350 B
//
// TWO HTTP requests per page, always: SwOS has no session, Digest is
// renegotiated every time. A complete telemetry read therefore costs EIGHT
// round trips, and that is what grounds the floors declared in swos.go —
// 10 s for state, 15 s for counters, 30 s for health and optics.
//
// # What this platform gives BETTER than its neighbours
//
// Two things, and they are not anecdotal:
//
//   - THE SNAPSHOT RATE, computed by the device (`rrb`, `trb`). meshcraft
//     therefore does not need to derive two counter reads to plot a curve,
//     which is precisely what makes a 15 s floor acceptable here where it
//     would not be elsewhere;
//   - OPTICAL DIAGNOSTICS, served by an ordinary READ page. On RouterOS it
//     takes a command (`monitor`, hence the binary API or a POST); here it is
//     a GET.
//
// # What this platform does NOT give, and which cannot be worked around
//
// The list is short and it is ESTABLISHED by enumerating the embedded UI,
// not assumed: no CPU, no memory, no LLDP, no PoE on this model, no clock —
// hence no timestamp coming from the device. The first two are missing to the
// point that the UI must be told: a CSS326 has NO CPU load series, and it is
// not a collection batch that is missing, it is the firmware that does not
// publish the value.

// PortTelemetry is what the pages say about a port.
//
// The numeric fields are POINTERS for whatever can be missing, exactly as in
// the neighbouring RouterOS connector, and for the same reason: on a counter,
// `0` and "not read" look alike and read as opposites. The rate is the example
// that matters — SwOS serves index 7, outside the table, for ports WITHOUT a
// link, and returning "10M" for those ports would invent a link that does not
// exist.
type PortTelemetry struct {
	Name string `json:"name"`
	// DeviceName is the name the DEVICE gives the port (/link.b `nm`),
	// returned for diagnosis and never used as a series key. See
	// [foldLinkTelemetry].
	DeviceName string `json:"device_name,omitempty"`
	// Enabled: is the port administratively up (/link.b `en`).
	Enabled bool `json:"enabled"`
	// LinkUp: does the port carry a link (/link.b `lnk`).
	LinkUp bool `json:"link_up"`
	// FullDuplex: nil when the port has no link — the `dpx` mask is then
	// meaningless.
	FullDuplex *bool `json:"full_duplex,omitempty"`
	// SpeedMbps: the NEGOTIATED rate. Nil on a port without a link, cf. the
	// type's comment and that of [FieldSpeed].
	SpeedMbps *float64 `json:"speed_mbps,omitempty"`

	InOctets   *float64 `json:"in_octets,omitempty"`
	OutOctets  *float64 `json:"out_octets,omitempty"`
	InPackets  *float64 `json:"in_packets,omitempty"`
	OutPackets *float64 `json:"out_packets,omitempty"`
	// InBitsPerSecond / OutBitsPerSecond: the rates the DEVICE computes.
	InBitsPerSecond  *float64 `json:"in_bits_per_second,omitempty"`
	OutBitsPerSecond *float64 `json:"out_bits_per_second,omitempty"`
	// InErrors: the MAC receive error counter (/stats.b `rte`).
	InErrors *float64 `json:"in_errors,omitempty"`
	// FCSErrors: frame check sequence errors (/stats.b `rfcs`).
	//
	// CARRIED HERE AND NOT ADDED TO `InErrors`, and it is a cautious decision
	// that needs explaining because it deprives the UI of the most telling
	// measure of a damaged cable. The survey in pages.go says `rte` carries
	// "MAC receive errors": if it is a TOTAL, frame check sequence errors are
	// part of it, and adding them would count some frames twice. An
	// exaggerated error curve is worse than a missing one — it gets a healthy
	// patch cord replaced. The counter therefore stays visible for diagnosis,
	// without entering the series, until a traffic injection has settled which
	// of the two contains the other.
	FCSErrors *float64 `json:"fcs_errors,omitempty"`
}

// OpticTelemetry is the diagnostics of an SFP cage, as /sfp.b returns them.
type OpticTelemetry struct {
	// Port is the faceplate name of the cage, not its rank in /sfp.b.
	//
	// The distinction is essential: the /sfp.b arrays are indexed by CAGE (two
	// entries on a CSS326-24G-2S+), not by port. The first cage is the port of
	// index `sfpo` — 24, hence port 25. Confusing the two indexings would
	// attribute SFP1's optical diagnostics to port 1, i.e. a copper port, and
	// the UI would display an optical power on an RJ45 socket.
	Port string `json:"port"`
	// Present: does the cage hold a module. Recognised by the temperature
	// sentinel, which the device returns as -128 °C on an empty cage.
	Present      bool     `json:"present"`
	Vendor       string   `json:"vendor,omitempty"`
	Part         string   `json:"part,omitempty"`
	Serial       string   `json:"serial,omitempty"`
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	SupplyVolts  *float64 `json:"supply_volts,omitempty"`
	TxBiasMA     *float64 `json:"tx_bias_ma,omitempty"`
	TxPowerDBM   *float64 `json:"tx_power_dbm,omitempty"`
	RxPowerDBM   *float64 `json:"rx_power_dbm,omitempty"`
	WavelengthNM *float64 `json:"wavelength_nm,omitempty"`
}

// Telemetry is the complete snapshot of a SwOS.
type Telemetry struct {
	At time.Time `json:"at"`
	// TemperatureC: the chip temperature (/sys.b `temp`), in whole degrees.
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	// UptimeSeconds: /sys.b `upt`, which is in HUNDREDTHS of a second.
	UptimeSeconds *float64 `json:"uptime_seconds,omitempty"`
	// Sensors: the board's OPTIONAL sensors — fans, supply voltages and
	// currents. Empty on this model, cf. [foldSysSensors].
	Sensors []Sensor `json:"sensors,omitempty"`

	Ports  []PortTelemetry  `json:"ports,omitempty"`
	Optics []OpticTelemetry `json:"optics,omitempty"`

	// Notes says what this snapshot does NOT contain and why. Sentences, as in
	// the state: the caller displays them and draws no decision from them.
	Notes []string `json:"notes,omitempty"`
}

// Sensor is a named board sensor, with its unit.
//
// The shape is that of the neighbouring RouterOS connector (`name` / `type` /
// `value`), and it is not mimicry: it is what lets a UI display the health of
// two ranges from the same vendor without telling them apart. SwOS has no
// sensor table — its fields are fixed in /sys.b — so this connector does the
// NAMING, and it does so with the page's field names so a diagnosis can be
// traced back to the source.
type Sensor struct {
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Value float64 `json:"value"`
}

// ReadTelemetry reads the four pages and returns the snapshot. READ ONLY.
//
// EIGHT HTTP ROUND TRIPS, and that is the real cost: two per page, Digest
// being renegotiated every time. The pages are read from the cheapest to the
// most expensive, which speeds nothing up but makes a failure more readable —
// if /stats.b is the only one missing, it is the heaviest page that timed
// out, and the diagnosis is not the same as when everything fails.
//
// A FAILURE DOES NOT STOP EVERYTHING: a board temperature does not depend on
// the counters. Only /link.b is blocking, and for a reason of correctness,
// not convenience — it carries the model's GEOMETRY (`prt`, `sfp`, `sfpo`).
// Without it, we know neither how many ports to read in the /stats.b arrays,
// nor which faceplate port to attach the first optical cage to. Guessing the
// geometry would attribute counters to the wrong port, which is worse than a
// gap.
func (a *Admin) ReadTelemetry(ctx context.Context) (Telemetry, error) {
	var out = Telemetry{At: time.Now()}
	var failures []string

	var link, err = a.Get(ctx, PageLink)
	if err != nil {
		return out, err
	}
	var ports, sfp, sfpFirst int
	ports, sfp, sfpFirst, err = geometryOf(link)
	if err != nil {
		a.step(false, "model geometry unreadable", err.Error())
		return out, err
	}
	// The geometry is kept on the access as readAll does: it is what NAMES the
	// sockets, and the series of a device must carry the same names as its
	// read state — otherwise a diagnostic UI would display a curve for "SFP1"
	// next to a configuration for a port it could not attach it to.
	a.ports, a.sfp, a.sfpFirst = ports, sfp, sfpFirst
	var facade = a.facade()
	out.Ports = foldLinkTelemetry(link, facade)

	var sys *Page
	sys, err = a.Get(ctx, PageSys)
	if err != nil {
		failures = append(failures, "health (/sys.b: "+err.Error()+
			") — neither chip temperature nor uptime")
	} else {
		foldSys(sys, &out)
	}

	var stats *Page
	stats, err = a.Get(ctx, PageStats)
	if err != nil {
		failures = append(failures, "counters (/stats.b: "+err.Error()+
			") — no octets, no packets, no rates, no errors")
	} else {
		foldStats(stats, out.Ports)
	}

	// The optical page is read only if there are cages. On a model without
	// SFP, asking for it would cost two requests to receive the complete web
	// UI — an unknown SwOS path returns 44,807 bytes of HTML as HTTP 200,
	// never a 404.
	if sfp > 0 {
		var optics *Page
		optics, err = a.Get(ctx, PageSFP)
		if err != nil {
			failures = append(failures, "optical diagnostics (/sfp.b: "+err.Error()+")")
		} else {
			out.Optics = foldOptics(optics, sfp, facade)
		}
	}

	if len(failures) > 0 {
		out.Notes = append(out.Notes, "PARTIAL READ — "+strconv.Itoa(len(failures))+
			" page(s) did not respond: "+strings.Join(failures, "; ")+". What is "+
			"displayed is what could be read, and does not necessarily describe the device.")
	}
	// Platform gaps (CPU, memory, …) are declared on Monitoring.Unavailable for the
	// model — the UI localizes from that capability. Snapshot notes do not duplicate
	// operator essays here.
	a.step(true, "telemetry read",
		strconv.Itoa(len(out.Ports))+" port(s), "+strconv.Itoa(len(out.Optics))+" cage(s)")
	return out, nil
}

// --- The translations, all PURE and verified on the real captures ---

// foldLinkTelemetry reads the port state from /link.b.
//
// THE RATE IS RETURNED ONLY FOR PORTS WITH A LINK, and that is this page's
// trap. Surveyed on the reference device: the 25 ports without a link carry
// rate index 7, which is OUTSIDE the `SpeedLabels` table (seven entries,
// indices 0 to 6), and the only port with a link carries 3 — i.e. 10G, which
// the inserted transceiver confirms. An out-of-table index is therefore not
// "10M by default", it is an ABSENCE, and returning it as a value would
// display a rate on twenty-five empty sockets.
//
// Duplex follows the same rule: the `dpx` mask is meaningless on a port
// without a link, so it is not returned rather than returned wrong —
// "half-duplex" on an unplugged port is exactly the kind of value that sends
// someone hunting for a negotiation mismatch.
func foldLinkTelemetry(link *Page, f Facade) []PortTelemetry {
	var ports = f.Ports()
	var out = make([]PortTelemetry, 0, ports)
	var speeds, hasSpeeds = link.Nums(FieldSpeed)
	var names, hasNames = link.Texts(FieldPortName)

	var i int
	for i = 0; i < ports; i++ {
		var pt = PortTelemetry{Name: f.Name(i)}
		// The name OBSERVED in `nm` is returned alongside, never instead — and
		// the other field no longer carries "GE1" but "Port1", i.e. the
		// device's CONVENTION (cf. naming.go). The two therefore coincide on a
		// switch nobody renamed, and that is the point; they diverge as soon as
		// an operator renames a socket, and that is precisely why `nm` is not
		// the key: it is free text, it collides, and it changes without
		// anything following it — making it a series key would produce two
		// curves for one port at the first rename. It serves DIAGNOSIS —
		// recognising in the UI the socket the operator is talking about — and
		// that is all.
		if hasNames && i < len(names) {
			pt.DeviceName = strings.TrimSpace(names[i])
		}

		var up, okLink = link.Bit(FieldLink, i)
		pt.LinkUp = okLink && up
		var en, okEn = link.Bit(FieldEnabled, i)
		pt.Enabled = okEn && en

		if pt.LinkUp {
			var dpx, okDpx = link.Bit(FieldDuplex, i)
			if okDpx {
				var v = dpx
				pt.FullDuplex = &v
			}
			if hasSpeeds && i < len(speeds) {
				var label, okSpeed = SpeedLabel(speeds[i])
				if okSpeed {
					var mbps, okMbps = speedMbps(label)
					if okMbps {
						pt.SpeedMbps = &mbps
					}
				}
			}
		}
		out = append(out, pt)
	}
	return out
}

// speedMbps translates a label from the device's table into megabits per
// second.
//
// The SwOS table is `"10M 100M 1G 10G 5G 2.5G 40G"`, and its order is NOT
// ascending — pages.go already says so, and that is why the LABEL is
// translated and not the index. Translating the index through a parallel
// table would require keeping two lists in the same order, which holds until
// the firmware that adds an eighth one in the middle.
func speedMbps(label string) (float64, bool) {
	switch label {
	case "10M":
		return 10, true
	case "100M":
		return 100, true
	case "1G":
		return 1000, true
	case "2.5G":
		return 2500, true
	case "5G":
		return 5000, true
	case "10G":
		return 10000, true
	case "40G":
		return 40000, true
	}
	return 0, false
}

// foldSys reads health from /sys.b.
//
// The temperature is in WHOLE degrees Celsius, with no scale factor —
// observed at 0x44 then 0x45 (68 then 69 °C) a few minutes apart on the
// reference device, which proves a live sensor and not a firmware constant.
//
// Uptime is in HUNDREDTHS of a second (`scale:100` in the UI). Surveyed
// 0x00e4cf41 = 14,995,265, i.e. 149,952 s — which matches the two values
// noted during the exploration campaign (149,281 then 149,801 seconds).
// Reading this field as seconds would give an uptime A HUNDRED TIMES too
// large, hence a device up for five months where it has been up for two
// days.
func foldSys(sys *Page, out *Telemetry) {
	var temp, ok = sys.Num(FieldTemp)
	if ok {
		var v = float64(temp)
		out.TemperatureC = &v
	}
	var upt bool
	var raw uint32
	raw, upt = sys.Num(FieldUptime)
	if upt {
		var v = float64(raw) / 100
		out.UptimeSeconds = &v
	}
	out.Sensors = foldSysSensors(sys)
}

// The OPTIONAL sensor fields of /sys.b, observed at ZERO on the reference
// model.
//
// They are named here rather than in pages.go because they serve only
// telemetry: pages.go documents what the connector READS or WRITES to
// configure, and none of these enters a write.
var sysSensors = []struct {
	Field string
	Name  string
	Type  string
	Scale float64
}{
	{"fan1", "fan1", "RPM", 1},
	{"fan2", "fan2", "RPM", 1},
	{"fan3", "fan3", "RPM", 1},
	{"fan4", "fan4", "RPM", 1},
	// Supply voltages and currents. Scale is NOT established — see the
	// comment on foldSysSensors, which explains why these sensors produce no
	// series on this range and why that has no consequence.
	{"p1v", "psu1-voltage", "V", 1},
	{"p2v", "psu2-voltage", "V", 1},
	{"btm1", "board1", "C", 1},
	{"btm2", "board2", "C", 1},
}

// foldSysSensors reads the optional sensors, and returns ONLY the non-zero
// ones.
//
// # Why filter on zero, even though it is a heuristic
//
// SwOS does not distinguish "this sensor does not exist" from "this sensor
// reads zero": the fields are ALWAYS present in the page, at zero when the
// board lacks the corresponding electronics. Surveyed on the CSS326-24G-2S+:
// `fan1` to `fan4`, `p1v`, `p2v`, `btm1`, `btm2` are all 0x00000000, and the
// device's UI displays none of these fields.
//
// Publishing those zeros would produce, across the whole fanless SwOS fleet,
// four "fan at 0 RPM" series and two "power supply at 0 V" — i.e. a permanent
// and false alert on most devices. Filtering them is therefore the only
// tenable behaviour.
//
// # The limit this filter creates, stated rather than hidden
//
// A fan that has ACTUALLY STOPPED reads zero too, and therefore becomes
// indistinguishable from an absent fan. No code can lift that ambiguity: the
// page does not carry the information. What partly holds it, and remains
// available, is the TEMPERATURE — a board whose fan stopped heats up, and
// that is the signal to watch on this platform, not the fan speed.
//
// The voltage scale is not established either, and that has no consequence
// TODAY precisely because of the filter: no observed model serves a non-zero
// value, so no voltage series is ever produced. The day a CRS chassis running
// SwOS serves one, the scale will have to be established before trusting the
// figure — it is written here so that it gets read at that moment.
func foldSysSensors(sys *Page) []Sensor {
	var out []Sensor
	var i int
	for i = range sysSensors {
		var raw, ok = sys.Num(sysSensors[i].Field)
		if !ok || raw == 0 {
			continue
		}
		out = append(out, Sensor{
			Name:  sysSensors[i].Name,
			Type:  sysSensors[i].Type,
			Value: float64(raw) * sysSensors[i].Scale,
		})
	}
	return out
}

// rateScale is the divisor of the /stats.b rate fields.
//
// 100, and this value is not only observed in the UI (`scale:.01`): it is
// CORROBORATED by the counters of the same capture, which is more solid than
// reading JavaScript code. The calculation, on the only port carrying a link
// (index 25, cage SFP2):
//
//	octets received    rb  = 0x018f2a86 = 26,159,750
//	uptime             upt = 0x00e4cf41 = 14,995,265 hundredths = 149,952 s
//	AVERAGE rate                        ≈ 1,396 bit/s
//	snapshot rate      rrb = 0x0000145f = 5,215 → /100 = 52.15 bit/s
//
// The snapshot is indeed BELOW the average, which is consistent with a quiet
// lab link after an adoption campaign. The other hypothesis — a ×100 factor,
// hence 521,500 bit/s — would imply 9.8 GB transferred over the uptime,
// against 26 MB actually counted: it is ruled out by three orders of
// magnitude.
//
// What is NOT established for all that: that this rate is an average over
// the same window as the UI's. No traffic injection has been done. This
// figure is therefore right up to a scale factor and a window, and no tight
// alert threshold should be built on it before it has been checked against a
// known rate.
const rateScale = 100

// foldStats pours the /stats.b counters into the port list.
//
// It MODIFIES the slice it receives, rather than returning a second one. It
// is the only place in the file that does so, and the reason is the indexing
// key: the /stats.b arrays are indexed by PORT RANK, in the same order as the
// /link.b geometry. Returning a second list would require stitching the two
// back together by name, hence rebuilding a correspondence that already
// exists by construction — and a rebuilt correspondence is one that can be
// wrong.
//
// OCTET COUNTERS ARE 64-BIT, in two halves: `rb` carries the low 32 bits,
// `rbh` the high 32. Reading `rb` alone would make the counter wrap to zero
// every 4 GiB — on a 10G link, every three and a half seconds, which would
// produce an entirely false traffic curve. [Counter64] recombines them, and
// it is not a theoretical precaution: this connector monitors 10G cages.
func foldStats(stats *Page, ports []PortTelemetry) {
	var rb, hasRb = stats.Nums(FieldRxBytes)
	var rbh, _ = stats.Nums(FieldRxBytesHigh)
	var tb, hasTb = stats.Nums(FieldTxBytes)
	var tbh, _ = stats.Nums(FieldTxBytesHigh)
	var rtp, hasRtp = stats.Nums(FieldRxPackets)
	var ttp, hasTtp = stats.Nums(FieldTxPackets)
	var rrb, hasRrb = stats.Nums(FieldRxRate)
	var trb, hasTrb = stats.Nums(FieldTxRate)
	var rte, hasRte = stats.Nums(FieldRxErrors)
	var rfcs, hasRfcs = stats.Nums(FieldRxFCS)

	var i int
	for i = range ports {
		if hasRb {
			assign64(&ports[i].InOctets, rb, rbh, i)
		}
		if hasTb {
			assign64(&ports[i].OutOctets, tb, tbh, i)
		}
		if hasRtp {
			assignAt(&ports[i].InPackets, rtp, i, 1)
		}
		if hasTtp {
			assignAt(&ports[i].OutPackets, ttp, i, 1)
		}
		if hasRrb {
			assignAt(&ports[i].InBitsPerSecond, rrb, i, 1.0/rateScale)
		}
		if hasTrb {
			assignAt(&ports[i].OutBitsPerSecond, trb, i, 1.0/rateScale)
		}
		if hasRte {
			assignAt(&ports[i].InErrors, rte, i, 1)
		}
		if hasRfcs {
			assignAt(&ports[i].FCSErrors, rfcs, i, 1)
		}
	}
}

// assign64 recombines a 64-bit counter from its two halves.
//
// A HIGH half shorter than the low one is not an error: it is treated as
// zero, which is exactly what a firmware that has not yet passed 4 GiB on
// that port would return. Refusing the whole counter for that reason would
// deprive a perfectly readable device of its traffic.
func assign64(dst **float64, low, high []uint32, i int) {
	if i >= len(low) {
		return
	}
	var hi uint32
	if i < len(high) {
		hi = high[i]
	}
	var v = float64(Counter64(low[i], hi))
	*dst = &v
}

// assignAt sets the value of an array at the given index, scaled.
func assignAt(dst **float64, vals []uint32, i int, scale float64) {
	if i >= len(vals) {
		return
	}
	var v = float64(vals[i]) * scale
	*dst = &v
}

// foldOptics reads /sfp.b.
//
// # INDEXING is the only real trap of this page
//
// Its arrays are indexed by CAGE and not by port: two entries on a
// CSS326-24G-2S+, whereas /stats.b carries twenty-six. The cage of rank `i`
// is the port of index `sfpFirst + i` — observed `sfpo: 0x18`, hence the
// first cage is the port of index 24, i.e. the port numbered 25. Stitching
// the two indexings together on the assumption that they coincide would
// attribute SFP1's optical diagnostics to port 1, and the UI would display an
// optical power on a copper socket.
//
// # An empty cage is recognised by its TEMPERATURE
//
// -128 °C, i.e. 0xffffff80 in two's complement, is the UI's "no module"
// (`u:-128`). It is NOT a temperature, and treating it as one would show two
// sensors at -128 °C on every switch whose cages are free. Surveyed on the
// reference device: `tmp:[0xffffff80,0x00000032]` — cage 1 is empty, cage 2
// holds a module at 50 °C.
//
// # The units, all observed in the embedded UI
//
//	tmp  whole °C                              0x32       = 50 °C
//	vcc  millivolts (scale:1E3)                0x0cbd     = 3.261 V
//	tbs  milliamperes                          0x22       = 34 mA
//	wln  hundredths of a nanometre (1E2)       0x0001f018 = 1,270 nm
//	tpw  tenths of a microwatt (1E4, ma:1)     0x12b6     = 4,790 → −3.1966 dBm
//	rpw  likewise                              0x0ad2     = 2,770 → −5.5752 dBm
//
// The observed wavelength, 1,270 nm, matches the descriptive field of the
// same page (`typ: '1270nm & smf'`) and the module reference (`SFP-10G-BX`).
// It is therefore not an assumed scale: two independent fields of the same
// capture agree.
func foldOptics(page *Page, sfp int, f Facade) []OpticTelemetry {
	var out = make([]OpticTelemetry, 0, sfp)
	var temps, _ = page.Nums(FieldSFPTemp)
	var vcc, _ = page.Nums(FieldSFPVcc)
	var bias, _ = page.Nums(FieldSFPTxBias)
	var txp, _ = page.Nums(FieldSFPTxPower)
	var rxp, _ = page.Nums(FieldSFPRxPower)
	var wln, _ = page.Nums("wln")
	var vendor, _ = page.Texts(FieldSFPVendor)
	var part, _ = page.Texts(FieldSFPPart)
	var serial, _ = page.Texts(FieldSFPSerial)

	var i int
	for i = 0; i < sfp; i++ {
		var o = OpticTelemetry{
			// The cage of rank `i` is the port of index `sfpFirst+i`, and the
			// faceplate is what NAMES it: "SFP1" on the reference device, where
			// the continuous numbering said "GE25". The name goes through the
			// same path as the read state's, otherwise an optical diagnosis
			// would display on a port the UI cannot find.
			Port:   f.Name(f.SFPFirst() + i),
			Vendor: textAt(vendor, i),
			Part:   textAt(part, i),
			Serial: textAt(serial, i),
		}
		if i < len(temps) && temps[i] != sfpNoModule {
			var v = float64(int32(temps[i])) //nolint:gosec // two's complement intended, cf. below
			o.TemperatureC = &v
			o.Present = true
		}
		if !o.Present {
			// A cage without a module produces neither measurement nor
			// identity: the text fields of an empty cage are strings of spaces
			// (observed `vnd:['', …]`), and publishing them would put a ghost
			// transceiver in the inventory.
			o.Vendor, o.Part, o.Serial = "", "", ""
			out = append(out, o)
			continue
		}
		assignAt(&o.SupplyVolts, vcc, i, 1.0/1000)
		assignAt(&o.TxBiasMA, bias, i, 1)
		assignAt(&o.WavelengthNM, wln, i, 1.0/100)
		assignDBM(&o.TxPowerDBM, txp, i)
		assignDBM(&o.RxPowerDBM, rxp, i)
		out = append(out, o)
	}
	return out
}

// textAt returns an entry of a text array, TRIMMED of its padding.
//
// A transceiver's identity fields are fixed-size in SFF-8472 and padded with
// SPACES — observed `'46532020202020202020202020202020'`, i.e. "FS" followed
// by fourteen spaces. The trimming happens HERE, for display, and never when
// reading the page: trimming on read would change the length of a field the
// write must return identically.
func textAt(vals []string, i int) string {
	if i >= len(vals) {
		return ""
	}
	return strings.TrimSpace(vals[i])
}

// assignDBM converts a raw optical power into dBm.
//
// # Unit, and why the conversion lives HERE and not in the series vocabulary
//
// The three MikroTik channels serve the same physical quantity in THREE
// different scales, and that is what rules out a single conversion:
//
//	SwOS            RAW integer in tenths of a microwatt → logarithm to compute
//	RouterOS API    value already in dBm, served as is by `monitor`
//	SNMP MikroTik   integer in THOUSANDTHS of a dBm (mtxrOpticalTxPower)
//
// The series vocabulary therefore imposes only a UNIT — dBm, because an
// optical budget is written in dB and a transceiver vendor's thresholds are
// given that way — and lets each channel take its own path to get there.
//
// # Zero has no value in dBm
//
// log₁₀(0) does not exist. A cage powered but without light — fibre
// unplugged, device on the far end off — therefore returns NO sample rather
// than minus infinity. It is not mathematical fussiness: a point at -Inf in
// VictoriaMetrics crushes the scale of the whole graph, so that a single
// unplugged fibre would make the curves of every working link unreadable.
func assignDBM(dst **float64, vals []uint32, i int) {
	if i >= len(vals) || vals[i] == 0 {
		return
	}
	var v = 10 * math.Log10(float64(vals[i])/10000)
	*dst = &v
}

// --- Translation into the series vocabulary ---

// Samples translates the snapshot into the project's series vocabulary.
//
// A PURE function. Like the RouterOS connector's, it produces NEITHER
// `ifindex` NOR `iftype`: this channel has no interface index to give, and
// fabricating an index from a port rank would invent a key nothing
// guarantees to be stable. The consequence is the desired one — these samples
// feed the curves without ever being able to modify the faceplate
// ("telemetry NEVER CREATES a port").
//
// Attachment to the port is therefore by NAME, and it falls here in the most
// favourable case: the names are the ones meshcraft COMPUTED from the
// target's prefix, so they already coincide with the faceplate it holds in
// the database. This platform numbers its ports and does not name them —
// that is precisely why the prefix comes from the target and not from the
// call (cf. [Admin.ReadState]).
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
		out = append(out, metricbuf.Sample{Name: name, Labels: labels, Value: *v, TS: at})
	}

	add(metricbuf.MetricUptime, t.UptimeSeconds, base)
	if t.TemperatureC != nil {
		// The sensor is NAMED, even if it is the only one on this board. A
		// temperature series without a `sensor` tag could not coexist with
		// those of ranges that have several, and two different queries would
		// then be needed to plot the same quantity across two vendors.
		add(metricbuf.MetricTemperature, t.TemperatureC,
			map[string]string{"device": deviceID, "sensor": "chip"})
	}
	var s Sensor
	for _, s = range t.Sensors {
		var labels = map[string]string{"device": deviceID, "sensor": s.Name}
		var v = s.Value
		switch s.Type {
		case "C":
			add(metricbuf.MetricTemperature, &v, labels)
		case "V":
			add(metricbuf.MetricVoltage, &v, labels)
		case "RPM":
			add(metricbuf.MetricFanRPM, &v, labels)
		}
	}

	var p PortTelemetry
	for _, p = range t.Ports {
		var labels = map[string]string{"device": deviceID, "port": p.Name}
		var state float64
		if p.LinkUp {
			state = 1
		}
		out = append(out, metricbuf.Sample{
			Name: metricbuf.MetricIfOperStatus, Labels: labels, Value: state, TS: at,
		})
		add(metricbuf.MetricIfSpeedMbps, p.SpeedMbps, labels)
		add(metricbuf.MetricIfInOctets, p.InOctets, labels)
		add(metricbuf.MetricIfOutOctets, p.OutOctets, labels)
		add(metricbuf.MetricIfInPackets, p.InPackets, labels)
		add(metricbuf.MetricIfOutPackets, p.OutPackets, labels)
		add(metricbuf.MetricIfInBitsPerSecond, p.InBitsPerSecond, labels)
		add(metricbuf.MetricIfOutBitsPerSecond, p.OutBitsPerSecond, labels)
		add(metricbuf.MetricIfInErrors, p.InErrors, labels)
	}

	var o OpticTelemetry
	for _, o = range t.Optics {
		if !o.Present {
			continue // an empty cage produces nothing, cf. foldOptics
		}
		var labels = map[string]string{"device": deviceID, "port": o.Port}
		add(metricbuf.MetricSFPTemperature, o.TemperatureC, labels)
		add(metricbuf.MetricSFPSupplyVolts, o.SupplyVolts, labels)
		add(metricbuf.MetricSFPTxBiasMilliamps, o.TxBiasMA, labels)
		add(metricbuf.MetricSFPTxPowerDBM, o.TxPowerDBM, labels)
		add(metricbuf.MetricSFPRxPowerDBM, o.RxPowerDBM, labels)
		add(metricbuf.MetricSFPWavelengthNM, o.WavelengthNM, labels)
	}
	return out
}

package swos

import (
	"math"
	"testing"

	"meshcraft/internal/metricbuf"
)

// SwOS telemetry, verified on the device's REAL CAPTURES.
//
// These tests have a property the neighbouring RouterOS connector's do not:
// here, EVERYTHING is observed. Page scraping is this platform's only
// channel, it has been exercised, and the four pages telemetry needs —
// /link.b, /stats.b, /sys.b, /sfp.b — are in testdata/ as the reference
// CSS326-24G-2S+ served them.
//
// What makes these tests useful beyond non-regression: the expected values
// below are COMPUTED by hand from the bytes of the capture, with their
// justification. A scale that changed would fail a test that says why the
// figure was what it was — not a test comparing two numbers whose origin
// nobody remembers.

// referencePages loads the four pages of the capture.
func referencePages(t *testing.T) (link, stats, sys, sfp *Page) {
	t.Helper()
	var err error
	link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b refused: %v", err)
	}
	stats, err = ParseObject(capture(t, "stats.b"))
	if err != nil {
		t.Fatalf("/stats.b refused: %v", err)
	}
	sys, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	sfp, err = ParseObject(capture(t, "sfp.b"))
	if err != nil {
		t.Fatalf("/sfp.b refused: %v", err)
	}
	return link, stats, sys, sfp
}

// referenceTelemetry rebuilds the snapshot exactly as ReadTelemetry would on
// the hardware, but without a socket: the geometry comes out of /link.b, as
// on the device.
func referenceTelemetry(t *testing.T) Telemetry {
	t.Helper()
	var link, stats, sys, sfp = referencePages(t)
	var ports, cages, first, err = geometryOf(link)
	if err != nil {
		t.Fatalf("geometry unreadable: %v", err)
	}
	// Surveyed: `prt: 0x1a` (26 ports), `sfp: 0x02` (2 cages), `sfpo: 0x18`
	// (the first cage is the port of index 24, hence the port numbered 25).
	if ports != 26 || cages != 2 || first != 24 {
		t.Fatalf("unexpected geometry: %d ports, %d cage(s), first at %d",
			ports, cages, first)
	}
	var out = Telemetry{}
	out.Ports = foldLinkTelemetry(link, NewFacade(ports, cages, first, DefaultPortPrefix))
	foldSys(sys, &out)
	foldStats(stats, out.Ports)
	out.Optics = foldOptics(sfp, cages, NewFacade(ports, cages, first, DefaultPortPrefix))
	return out
}

// TestPortStateReadOnTheRealCapture
//
// Surveyed: `lnk: 0x02000000` — bit 25 alone, so cage SFP2 is the ONLY port
// carrying a link. `en: 0x03ffffff` — all twenty-six ports are
// administratively up.
func TestPortStateReadOnTheRealCapture(t *testing.T) {
	var tel = referenceTelemetry(t)
	if len(tel.Ports) != 26 {
		t.Fatalf("%d port(s) read, 26 expected", len(tel.Ports))
	}

	var withLink []string
	var p PortTelemetry
	for _, p = range tel.Ports {
		if p.LinkUp {
			withLink = append(withLink, p.Name)
		}
		if !p.Enabled {
			t.Errorf("%s declared inactive although `en: 0x03ffffff` enables all 26 ports",
				p.Name)
		}
	}
	if len(withLink) != 1 || withLink[0] != "SFP2" {
		t.Errorf("ports with link: %v — the capture carries `lnk: 0x02000000`, hence "+
			"bit 25, hence the port NUMBERED 26", withLink)
	}
	// The names the device gives, returned alongside and never instead:
	// "Port1"… "SFP2" by default, but free text in reality.
	if tel.Ports[0].DeviceName != "Port1" || tel.Ports[25].DeviceName != "SFP2" {
		t.Errorf("device names misread: %q … %q",
			tel.Ports[0].DeviceName, tel.Ports[25].DeviceName)
	}
}

// TestNegotiatedRateOnlyOnAPortWithALink — the index 7 trap.
//
// Surveyed: the 25 ports WITHOUT a link carry rate index 7, which is OUTSIDE
// the `SpeedLabels` table (seven entries, indices 0 to 6), and the only port
// with a link carries 3, i.e. 10G — which the inserted transceiver confirms
// (`SFP-10G-BX`).
//
// An out-of-table index is therefore not "10M by default", it is an ABSENCE.
// Returning it as a value would display a rate on twenty-five empty sockets.
func TestNegotiatedRateOnlyOnAPortWithALink(t *testing.T) {
	var tel = referenceTelemetry(t)
	var p PortTelemetry
	for _, p = range tel.Ports {
		if p.LinkUp {
			if p.SpeedMbps == nil || *p.SpeedMbps != 10000 {
				t.Errorf("%s carries a link: rate %v, expected 10000 Mbit/s (index 3 = 10G)",
					p.Name, p.SpeedMbps)
			}
			if p.FullDuplex == nil || !*p.FullDuplex {
				t.Errorf("%s: duplex %v — `dpx: 0x02000000` sets bit 25",
					p.Name, p.FullDuplex)
			}
			continue
		}
		if p.SpeedMbps != nil {
			t.Errorf("%s has no link but carries a rate of %v Mbit/s: index 7 "+
				"observed on these ports is OUTSIDE the table, it is an absence", p.Name, *p.SpeedMbps)
		}
		if p.FullDuplex != nil {
			t.Errorf("%s has no link but carries a duplex: \"half-duplex\" on an "+
				"unplugged port sends someone hunting for a negotiation mismatch", p.Name)
		}
	}
}

// TestStatsCountersOnTheOnlyActivePort — the values, computed from the bytes.
//
//	rb[25]  = 0x018f2a86 = 26,159,750 octets received
//	rbh[25] = 0x00000000 → the high half is zero, the counter fits in 32 bits
//	tb[25]  = 0x00218659 =  2,197,081 octets sent
//	rtp[25] = 0x0005ed25 =    388,389 frames received
//	ttp[25] = 0x00001f58 =      8,024 frames sent
func TestStatsCountersOnTheOnlyActivePort(t *testing.T) {
	var tel = referenceTelemetry(t)
	var cage = tel.Ports[25]

	var cases = []struct {
		name     string
		got      *float64
		expected float64
	}{
		{"octets received", cage.InOctets, 26159750},
		{"octets sent", cage.OutOctets, 2197081},
		{"frames received", cage.InPackets, 388389},
		{"frames sent", cage.OutPackets, 8024},
	}
	var c int
	for c = range cases {
		if cases[c].got == nil {
			t.Errorf("%s: not read", cases[c].name)
			continue
		}
		if *cases[c].got != cases[c].expected {
			t.Errorf("%s: %v, expected %v", cases[c].name, *cases[c].got, cases[c].expected)
		}
	}
	// No error on this idle device, and it is a READ value, not an absence:
	// the field exists, it is zero, the sample says something true.
	if cage.InErrors == nil || *cage.InErrors != 0 {
		t.Errorf("receive errors: %v — the capture carries `rte` at zero, which is "+
			"a fact, not an absence", cage.InErrors)
	}
}

// TestCounter64RecombinesBothHalves
//
// `rb` carries the low 32 bits, `rbh` the high 32. Reading `rb` alone would
// make the counter wrap to zero every 4 GiB — on a 10G link, every three and
// a half seconds, which would produce an entirely false traffic curve. This
// connector monitors 10G cages: it is not a theoretical precaution.
//
// The reference capture has its high half at zero, so it CANNOT prove the
// recombination. The test forces it on a made-up page — it is the only case
// in this file where the capture is not enough, and that must be said rather
// than believing the test has it covered.
func TestCounter64RecombinesBothHalves(t *testing.T) {
	var page = NewPage()
	page.SetNums(FieldRxBytes, []uint32{0x00000001})
	page.SetNums(FieldRxBytesHigh, []uint32{0x00000003})
	var ports = []PortTelemetry{{Name: "Port1"}}
	foldStats(page, ports)

	// 3 × 2³² + 1 = 12,884,901,889.
	var expected = float64(uint64(3)<<32 | 1)
	if ports[0].InOctets == nil || *ports[0].InOctets != expected {
		t.Errorf("64-bit counter: %v, expected %v — the high half was not "+
			"recombined, and the curve would drop to zero every 4 GiB",
			ports[0].InOctets, expected)
	}
	// And the counterpart: a high half SHORTER than the low one is treated as
	// zero, not as a refusal. That is what a firmware that has not yet passed
	// 4 GiB on that port would return, and refusing the whole counter for that
	// would deprive a perfectly readable device of its traffic.
	var page2 = NewPage()
	page2.SetNums(FieldRxBytes, []uint32{0x000000ff, 0x000000ee})
	page2.SetNums(FieldRxBytesHigh, []uint32{0x00000000})
	var ports2 = []PortTelemetry{{Name: "Port1"}, {Name: "Port2"}}
	foldStats(page2, ports2)
	if ports2[1].InOctets == nil || *ports2[1].InOctets != 0xee {
		t.Errorf("truncated high half: %v, expected 238", ports2[1].InOctets)
	}
}

// TestSnapshotRateAndTheCorroborationOfItsScale
//
// The scale (division by 100) is observed in the embedded UI (`scale:.01`),
// and it is CORROBORATED by the counters of the same capture, which is more
// solid than reading JavaScript. The calculation, on the only port carrying a
// link:
//
//	octets received    rb  = 0x018f2a86 = 26,159,750
//	uptime             upt = 0x00e4cf41 = 14,995,265 hundredths = 149,952.65 s
//	AVERAGE rate                        ≈ 1,396 bit/s
//	snapshot rate      rrb = 0x0000145f = 5,215 → /100 = 52.15 bit/s
//
// The snapshot is indeed BELOW the average, which is consistent with a quiet
// lab link after an adoption campaign. The other hypothesis — a ×100 factor,
// hence 521,500 bit/s — would imply 9.8 GB transferred over the uptime,
// against 26 MB actually counted: it is ruled out by three orders of
// magnitude.
//
// This test VERIFIES the corroboration, it does not merely quote it: if the
// scale changed, it is the inconsistency with the counters it would show.
func TestSnapshotRateAndTheCorroborationOfItsScale(t *testing.T) {
	var tel = referenceTelemetry(t)
	var cage = tel.Ports[25]

	if cage.InBitsPerSecond == nil {
		t.Fatal("snapshot rate not read: it is what makes a 15 s floor " +
			"acceptable on this platform")
	}
	if math.Abs(*cage.InBitsPerSecond-52.15) > 0.001 {
		t.Errorf("snapshot rate: %v bit/s, expected 52.15 (0x145f / 100)",
			*cage.InBitsPerSecond)
	}

	// The corroboration: the average rate inferred from the counters must be
	// of the SAME ORDER OF MAGNITUDE as the snapshot, within a factor of a
	// hundred at most. A gap of a factor of a hundred would be the exact
	// symptom of a wrong scale.
	if tel.UptimeSeconds == nil || *tel.UptimeSeconds <= 0 {
		t.Fatal("uptime not read: corroboration is impossible")
	}
	var average = *cage.InOctets * 8 / *tel.UptimeSeconds
	var ratio = average / *cage.InBitsPerSecond
	if ratio > 100 || ratio < 0.01 {
		t.Errorf("average rate inferred from the counters (%.0f bit/s) and snapshot rate "+
			"(%.2f bit/s) separated by a factor of %.0f: that is the symptom of a wrong scale "+
			"on `rrb`, not of a quiet link", average, *cage.InBitsPerSecond, ratio)
	}
}

// TestHealthReducedToATemperatureAndAnUptime
//
//	temp = 0x45 = 69 °C, in WHOLE degrees and with no scale factor. Surveyed at
//	              0x44 then 0x45 a few minutes apart on the device: it is a live
//	              sensor, not a firmware constant.
//	upt  = 0x00e4cf41 = 14,995,265 HUNDREDTHS of a second = 149,952.65 s.
//	              Reading this field as seconds would give an uptime a hundred
//	              times too large — five months where the device has been up for
//	              two days.
func TestHealthReducedToATemperatureAndAnUptime(t *testing.T) {
	var tel = referenceTelemetry(t)

	if tel.TemperatureC == nil || *tel.TemperatureC != 69 {
		t.Errorf("chip temperature: %v, expected 69 °C (0x45, whole degrees)",
			tel.TemperatureC)
	}
	if tel.UptimeSeconds == nil {
		t.Fatal("uptime not read")
	}
	if math.Abs(*tel.UptimeSeconds-149952.65) > 0.01 {
		t.Errorf("uptime: %v s, expected 149,952.65 s — the field is in "+
			"HUNDREDTHS of a second", *tel.UptimeSeconds)
	}
	// The optional sensors are ALL at zero on this model (fans, voltages,
	// board temperatures) and must produce NO entry: four "fan at 0 RPM" series
	// across the whole SwOS fleet would be a permanent and false alert.
	if len(tel.Sensors) != 0 {
		t.Errorf("%d optional sensor(s) returned although the capture serves them all "+
			"at zero: %+v", len(tel.Sensors), tel.Sensors)
	}
}

// TestANonZeroSensorIsReturnedToo — the symmetric branch of the zero filter.
//
// The filter of the previous test is a HEURISTIC imposed by the platform:
// SwOS does not distinguish "this sensor does not exist" from "this sensor
// reads zero". It must therefore be verified that it does not throw
// everything away — a chassis that served a real fan speed must see it
// appear.
//
// And the limit this filter creates is named where it lives
// (foldSysSensors): a fan that has ACTUALLY STOPPED reads zero, so it becomes
// indistinguishable from an absent fan. No code can lift that ambiguity; what
// partly holds it is the TEMPERATURE, which rises when the fan stops.
func TestANonZeroSensorIsReturnedToo(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldTemp, 45)
	page.SetNum(FieldUptime, 100)
	page.SetNum("fan1", 4200)
	var tel Telemetry
	foldSys(page, &tel)

	if len(tel.Sensors) != 1 {
		t.Fatalf("%d sensor(s) returned, 1 expected: %+v", len(tel.Sensors), tel.Sensors)
	}
	if tel.Sensors[0].Name != "fan1" || tel.Sensors[0].Type != "RPM" ||
		tel.Sensors[0].Value != 4200 {
		t.Errorf("sensor returned wrongly: %+v", tel.Sensors[0])
	}
}

// TestOpticalDiagnosticsAndTheirINDEXING — the only real trap of /sfp.b.
//
// Its arrays are indexed by CAGE and not by port: TWO entries, whereas
// /stats.b carries twenty-six. The cage of rank `i` is the port of index
// `sfpo + i` — observed `sfpo: 0x18`, hence the first cage is the port
// numbered 25. Stitching the two indexings together on the assumption that
// they coincide would attribute SFP1's optical diagnostics to port 1, i.e. a
// copper socket.
//
// The units, all observed in the embedded UI:
//
//	tmp  0x32       = 50 °C, whole degrees
//	vcc  0x0cbd     = 3,261 mV → 3.261 V (scale:1E3)
//	tbs  0x22       = 34 mA
//	wln  0x0001f018 = 127,000 hundredths of nm → 1,270 nm, which the descriptive
//	                  field of the SAME page confirms (`typ: '1270nm & smf'`)
//	tpw  0x12b6     = 4,790 tenths of µW → 10·log₁₀(0.479) = −3.1966 dBm
//	rpw  0x0ad2     = 2,770 tenths of µW → 10·log₁₀(0.277) = −5.5752 dBm
func TestOpticalDiagnosticsAndTheirINDEXING(t *testing.T) {
	var tel = referenceTelemetry(t)
	if len(tel.Optics) != 2 {
		t.Fatalf("%d cage(s), 2 expected", len(tel.Optics))
	}

	// THE INDEXING: the first cage is port 25, the second port 26.
	if tel.Optics[0].Port != "SFP1" || tel.Optics[1].Port != "SFP2" {
		t.Fatalf("cages attached to %q and %q — `sfpo: 0x18` puts the first at the port "+
			"NUMBERED 25. Attaching them to Port1 and Port2 would display an optical power "+
			"on a copper socket", tel.Optics[0].Port, tel.Optics[1].Port)
	}

	// Cage 1 is EMPTY: `tmp[0] = 0xffffff80`, i.e. -128 °C, which is the UI's
	// "no module" and not a temperature.
	if tel.Optics[0].Present {
		t.Error("cage 1 is empty in the capture (`tmp: 0xffffff80` = -128 °C): " +
			"treating it as a module would show a sensor at -128 °C on every " +
			"switch whose cages are free")
	}
	if tel.Optics[0].TemperatureC != nil {
		t.Errorf("the empty cage carries a temperature of %v °C", *tel.Optics[0].TemperatureC)
	}
	// And its identity is cleared: the text fields of an empty cage are
	// strings of spaces, which would put a ghost transceiver in the inventory.
	if tel.Optics[0].Vendor != "" || tel.Optics[0].Part != "" {
		t.Errorf("the empty cage carries an identity: %+v", tel.Optics[0])
	}

	var m = tel.Optics[1]
	if !m.Present {
		t.Fatal("cage 2 holds a module (`tmp: 0x32`)")
	}
	// The identity is TRIMMED of its padding: SFF-8472 fields are fixed-size
	// and padded with spaces (`'46532020…'` = "FS" + fourteen spaces).
	if m.Vendor != "FS" || m.Part != "SFP-10G-BX" || m.Serial != "S2001001227" {
		t.Errorf("module identity badly trimmed: vendor %q, part %q, serial %q",
			m.Vendor, m.Part, m.Serial)
	}

	var cases = []struct {
		name     string
		got      *float64
		expected float64
		tol      float64
	}{
		{"module temperature", m.TemperatureC, 50, 0.001},
		{"supply voltage", m.SupplyVolts, 3.261, 0.0001},
		{"bias current", m.TxBiasMA, 34, 0.001},
		{"wavelength", m.WavelengthNM, 1270, 0.001},
		{"transmitted power", m.TxPowerDBM, -3.1966, 0.001},
		{"received power", m.RxPowerDBM, -5.5752, 0.001},
	}
	var c int
	for c = range cases {
		if cases[c].got == nil {
			t.Errorf("%s: not read", cases[c].name)
			continue
		}
		if math.Abs(*cases[c].got-cases[c].expected) > cases[c].tol {
			t.Errorf("%s: %v, expected %v (± %v)",
				cases[c].name, *cases[c].got, cases[c].expected, cases[c].tol)
		}
	}
}

// TestZeroPowerProducesNoDBMValue
//
// log₁₀(0) does not exist. A cage powered but without light — fibre
// unplugged, device on the far end off — must therefore produce NO sample
// rather than minus infinity: a point at -Inf in VictoriaMetrics crushes the
// scale of the whole graph, so that a single unplugged fibre would make the
// curves of every working link unreadable.
func TestZeroPowerProducesNoDBMValue(t *testing.T) {
	var dst *float64
	assignDBM(&dst, []uint32{0}, 0)
	if dst != nil {
		if math.IsInf(*dst, -1) {
			t.Error("a zero power produced minus infinity: that point crushes " +
				"the scale of the whole graph")
		}
		t.Errorf("a zero power produced %v", *dst)
	}
	// And a non-zero value does go through.
	assignDBM(&dst, []uint32{10000}, 0)
	if dst == nil || math.Abs(*dst) > 0.0001 {
		t.Errorf("10,000 tenths of µW are 1 mW, hence 0 dBm: %v", dst)
	}
}

// TestSamplesCarryNoIfindex — the same property as on the RouterOS connector.
//
// This channel has no interface index to give. Since the controller requires
// the `iftype` label to touch the inventory, these samples feed the curves
// without ever being able to modify the faceplate: "telemetry NEVER CREATES a
// port" is obtained by construction.
func TestSamplesCarryNoIfindex(t *testing.T) {
	var tel = referenceTelemetry(t)
	var samples = tel.Samples("dev-css")
	if len(samples) == 0 {
		t.Fatal("no sample produced")
	}
	var s int
	for s = range samples {
		if samples[s].Labels["device"] != "dev-css" {
			t.Fatalf("sample without a device: %+v", samples[s])
		}
		var _, hasIfindex = samples[s].Labels["ifindex"]
		if hasIfindex {
			t.Fatalf("scraping sample carrying an interface index: %+v",
				samples[s])
		}
	}
}

// TestSamplesNameTheTemperatureSensor
//
// A temperature series WITHOUT a `sensor` label could not coexist with those
// of ranges that have several, and two different queries would then be needed
// to plot the same quantity across two vendors. The sensor is therefore named
// even when it is alone on the board.
func TestSamplesNameTheTemperatureSensor(t *testing.T) {
	var tel = referenceTelemetry(t)
	var seen bool
	var s metricbuf.Sample
	for _, s = range tel.Samples("dev-css") {
		if s.Name != metricbuf.MetricTemperature {
			continue
		}
		seen = true
		if s.Labels["sensor"] == "" {
			t.Errorf("temperature without a `sensor` label: %+v", s)
		}
	}
	if !seen {
		t.Error("no temperature series produced")
	}
}

// TestPlatformGapsAreDeclaredAsCapabilities
//
// Platform gaps are declared on Monitoring.Unavailable — not as snapshot notes.
// The UI localizes operator copy from the metric key.
func TestPlatformGapsAreDeclaredAsCapabilities(t *testing.T) {
	var mon = (&Driver{}).Capabilities("CSS326-24G-2S+").Monitoring
	var metric string
	for _, metric = range []string{"cpu", "memory"} {
		var _, ok = mon.WhyUnavailable(metric)
		if !ok {
			t.Errorf("%q must be declared unavailable on SwOS capabilities", metric)
		}
	}
}

// TestSpeedTranslatedFromTheLabelNotTheIndex
//
// The SwOS table is `"10M 100M 1G 10G 5G 2.5G 40G"` and its order is NOT
// ascending. Translating the index through a parallel table would require
// keeping two lists in the same order, which holds until the firmware that
// adds an eighth one in the middle.
func TestSpeedTranslatedFromTheLabelNotTheIndex(t *testing.T) {
	var i int
	for i = range SpeedLabels {
		var label, _ = SpeedLabel(uint32(i)) //nolint:gosec // table index, bounded
		var mbps, ok = speedMbps(label)
		if !ok {
			t.Errorf("label %q of the device's table has no translation "+
				"into Mbit/s: a port negotiating that rate would have no series", label)
			continue
		}
		if mbps <= 0 {
			t.Errorf("%q translated to %v Mbit/s", label, mbps)
		}
	}
	// The order is not ascending: that is what rules out a parallel table.
	var third, _ = SpeedLabel(3)
	var fourth, _ = SpeedLabel(4)
	var m3, _ = speedMbps(third)
	var m4, _ = speedMbps(fourth)
	if m3 <= m4 {
		t.Errorf("the SwOS rate table is supposed to be NON-ascending "+
			"(index 3 = 10G, index 4 = 5G): %v then %v", m3, m4)
	}
}

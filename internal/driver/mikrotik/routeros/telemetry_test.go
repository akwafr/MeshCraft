package routeros

import (
	"context"
	"math"
	"strings"
	"testing"

	"meshcraft/internal/metricbuf"
)

// Telemetry translation, proven on REAL captures — and on ONE fixture that is not,
// named as such.
//
// # The file `interface_ethernet_monitor.NOT-CAPTURED.json` carries its warning in its NAME
//
// All other testdata/ captures are real responses from the reference CRS326-24G-2S+,
// observed 28 July 2026 through a proxy tunnel. This one is NOT: `/interface/ethernet/monitor`
// is a command, out of scope for a read tool restricted to GET, and the binary API that
// can execute it could not be wired to hardware. It is BUILT from field names that
// RouterOS documentation publishes, with plausible values for the transceiver actually
// present in cage 2 of the lab (an SFP-10G-BX, whose part number and serial are
// observed — via the neighbouring CSS326's /sfp.b page).
//
// The filename suffix is there so one cannot mistake it six months later: the rest of
// the directory is evidence, this one is a template. What it proves is therefore the
// TRANSLATION (field names read, units, port attachment), never that this firmware
// responds this way.

// fakeSource serves captures instead of a device.
//
// It exists so translation can be proven without a socket: it is the translation that
// errs, and a reading verifiable only in production is not verified here.
type fakeSource struct {
	menus   map[string][]Row
	monitor map[string]Row
	// deny simulates a channel that cannot execute `monitor` — the REST case until
	// POST has been exercised there.
	deny map[string]error
}

func (s *fakeSource) Print(_ context.Context, menu string) ([]Row, error) {
	var err, denied = s.deny[menu]
	if denied {
		return nil, err
	}
	var rows, ok = s.menus[menu]
	if !ok {
		return nil, fakeErr("menu " + menu + " absent from capture")
	}
	return rows, nil
}

func (s *fakeSource) Monitor(_ context.Context, menu, name string) (Row, error) {
	var err, denied = s.deny[menu+"/monitor"]
	if denied {
		return nil, err
	}
	var row, ok = s.monitor[name]
	if !ok {
		return Row{}, nil
	}
	return row, nil
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// captureSource builds a source from directory captures.
func captureSource(t *testing.T) *fakeSource {
	t.Helper()
	var monitor = fixture(t, "interface_ethernet_monitor.NOT-CAPTURED.json")
	var out = &fakeSource{
		menus: map[string][]Row{
			"system/resource":    {fixtureOne(t, "system_resource.json")},
			"system/health":      fixture(t, "system_health.json"),
			"interface":          fixture(t, "interface.json"),
			"interface/ethernet": fixture(t, "interface_ethernet.json"),
		},
		monitor: map[string]Row{},
		deny:    map[string]error{},
	}
	var r Row
	for _, r = range monitor {
		out.monitor[r.Get("name")] = r
	}
	return out
}

func readTelemetry(t *testing.T, src TelemetrySource) Telemetry {
	t.Helper()
	var tel, err = ReadTelemetry(context.Background(), src)
	if err != nil {
		t.Fatalf("telemetry read: %v", err)
	}
	return tel
}

// TestHealthReadFromRealCapture — observed values, not textbook ones.
//
// Observed on the reference device: `cpu-load: "1"`, `free-memory: "450670592"`,
// `total-memory: "536870912"`, `uptime: "1d17h38m11s"`, `cpu-count: "2"`,
// `version: "7.21beta3 (testing)"`.
func TestHealthReadFromRealCapture(t *testing.T) {
	var tel = readTelemetry(t, captureSource(t))

	if tel.CPULoadPercent == nil || *tel.CPULoadPercent != 1 {
		t.Errorf("CPU load: %v expected 1 %%", tel.CPULoadPercent)
	}
	if tel.FreeMemory == nil || *tel.FreeMemory != 450670592 {
		t.Errorf("free memory: %v", tel.FreeMemory)
	}
	if tel.TotalMemory == nil || *tel.TotalMemory != 536870912 {
		t.Errorf("total memory: %v", tel.TotalMemory)
	}
	if tel.CPUCount != 2 {
		t.Errorf("CPU count: %d expected 2", tel.CPUCount)
	}
	if tel.Version != "7.21beta3 (testing)" {
		t.Errorf("version: %q", tel.Version)
	}
	// 1d17h38m11s = 86 400 + 61 200 + 2 280 + 11 = 149 891 s. A `time.ParseDuration`
	// would fail on this string: it knows neither `d` nor `w`.
	var expected float64 = 1*86400 + 17*3600 + 38*60 + 11
	if tel.UptimeSeconds == nil || *tel.UptimeSeconds != expected {
		t.Errorf("uptime: %v expected %v s", tel.UptimeSeconds, expected)
	}

	// The observed sensor, with its TYPE: that decides the series, not the name.
	if len(tel.Sensors) != 1 {
		t.Fatalf("%d sensor(s), 1 expected (capture carries only cpu-temperature): %+v",
			len(tel.Sensors), tel.Sensors)
	}
	if tel.Sensors[0].Name != "cpu-temperature" || tel.Sensors[0].Type != "C" ||
		tel.Sensors[0].Value != 72 {
		t.Errorf("sensor misread: %+v", tel.Sensors[0])
	}
}

// TestParseUptime — the RouterOS form, and refusals.
//
// A string with an illegible piece is refused WHOLE. '4 h' where the device says
// '3 weeks and 4 h' would suggest a reboot that did not happen — exactly the event
// this measurement exists to detect.
func TestParseUptime(t *testing.T) {
	var cases = []struct {
		in       string
		expected float64
		readable bool
		reason   string
	}{
		{"1d17h38m11s", 1*86400 + 17*3600 + 38*60 + 11, true,
			"form observed on the reference device, i.e. 149 891 s"},
		{"11s", 11, true, "a device that just booted"},
		{"3w2d4h", 3*604800 + 2*86400 + 4*3600, true, "weeks from verbose ranges"},
		{"5m", 300, true, "without seconds"},
		{"", 0, false, "missing field"},
		{"1d17h38m11", 0, false, "trailing digits: TRUNCATED string, " +
			"therefore wrong duration by an unknown factor"},
		{"1j2h", 0, false, "unknown unit — conclude nothing rather than skip"},
		{"h", 0, false, "unit without digit"},
		{"149891", 0, false, "bare number: RouterOS never serves this form, " +
			"and reading it as seconds would be a gamble"},
	}
	var c int
	for c = range cases {
		var got, ok = ParseUptime(cases[c].in)
		if ok != cases[c].readable {
			t.Errorf("%q (%s): readable=%t expected %t",
				cases[c].in, cases[c].reason, ok, cases[c].readable)
			continue
		}
		if ok && got != cases[c].expected {
			t.Errorf("%q (%s): %v s, expected %v s",
				cases[c].in, cases[c].reason, got, cases[c].expected)
		}
	}
}

// TestCountersReadFromBothMenus — the singular/plural trap.
//
// `/interface` serves `rx-byte` in the SINGULAR, `/interface/ethernet` serves `rx-bytes`
// in the PLURAL. These are not two spellings of the same thing: they are two menus. Reading
// `rx-bytes` in `/interface` yields an empty string, therefore a silently absent counter —
// the hardest failure mode to see, since the curve exists and stays flat.
func TestCountersReadFromBothMenus(t *testing.T) {
	var tel = readTelemetry(t, captureSource(t))

	var byName = map[string]PortCounters{}
	var p PortCounters
	for _, p = range tel.Ports {
		byName[p.Name] = p
	}
	// Cage 2 is the only port carrying a link on the reference device.
	var cage2, seen = byName["sfp-sfpplus2"]
	if !seen {
		t.Fatalf("sfp-sfpplus2 absent from read: %d port(s) read", len(tel.Ports))
	}
	if !cage2.Running {
		t.Error("sfp-sfpplus2 carries a link on the capture (`running: true`)")
	}
	if cage2.InOctets == nil || *cage2.InOctets == 0 {
		t.Errorf("cage 2 received octets: %v — the `/interface` field is "+
			"`rx-byte` in the SINGULAR", cage2.InOctets)
	}
	// Errors come from the other menu: their presence proves both were rejoined by name.
	if cage2.InErrors == nil {
		t.Error("receive errors nil: `/interface/ethernet` counters " +
			"were not attached to the port")
	}
	if cage2.LinkDowns == nil {
		t.Error("link-down counter missing: `link-downs` is served by " +
			"`/interface`, and it is the only way to see a flap faster than " +
			"the collection cadence")
	}
}

// TestErrorsAndDiscardsStayDistinct
//
// An ERROR blames the cable or transceiver: the frame arrived damaged. A DISCARD
// blames sizing: the frame was good, there was no room left. Adding them would make
// one hunt a physical defect in front of a simply saturated link.
//
// The test builds two records rather than using the capture, because the reference
// device is idle: all its fault counters are zero, which distinguishes nothing.
func TestErrorsAndDiscardsStayDistinct(t *testing.T) {
	var iface = Row{
		"name": "ether5", "running": "true",
		"rx-byte": "1000", "tx-byte": "2000",
		"tx-queue-drop": "7",
	}
	var eth = Row{
		"name": "ether5",
		// Receive shape errors: 1 + 2 + 3 + 4 + 5 = 15.
		"rx-fcs-error": "1", "rx-fragment": "2", "rx-jabber": "3",
		"rx-too-long": "4", "rx-too-short": "5",
		// Chip AGGREGATE counter, deliberately huge: it must NOT enter the sum,
		// or some frames would be counted twice.
		"rx-error-events": "9999",
		// Receive discards.
		"rx-overflow": "11",
		// Transmit errors: 6 + 7 + 8 = 21.
		"tx-collision": "6", "tx-late-collision": "7", "tx-underrun": "8",
		// Chip transmit discards, to add to the software queue ones.
		"tx-drop-packet": "13",
	}
	var ports = foldPorts([]Row{iface}, []Row{eth})
	if len(ports) != 1 {
		t.Fatalf("%d port(s) rebuilt", len(ports))
	}
	var p = ports[0]
	if p.InErrors == nil || *p.InErrors != 15 {
		t.Errorf("receive errors: %v expected 15 — if it is 10014, `rx-error-events` "+
			"was summed, which counts some frames twice",
			p.InErrors)
	}
	if p.InDiscards == nil || *p.InDiscards != 11 {
		t.Errorf("receive discards: %v expected 11 (rx-overflow)", p.InDiscards)
	}
	if p.OutErrors == nil || *p.OutErrors != 21 {
		t.Errorf("transmit errors: %v expected 21", p.OutErrors)
	}
	// 13 (chip) + 7 (interface software queue): both menus, or one loss mode stays invisible.
	if p.OutDiscards == nil || *p.OutDiscards != 20 {
		t.Errorf("transmit discards: %v expected 20 — ethernet menu `tx-drop-packet` "+
			"PLUS interface menu `tx-queue-drop`", p.OutDiscards)
	}
}

// TestUnreadCounterIsNotAZeroCounter — the distinction that carries this whole file.
//
// A port whose errors could not be read and a port with none look alike at zero and
// read oppositely. The first must produce NO sample, or the curve asserts an absence
// of errors that nobody observed.
func TestUnreadCounterIsNotAZeroCounter(t *testing.T) {
	// No `/interface/ethernet` menu: errors were not read.
	var ports = foldPorts([]Row{{"name": "ether1", "rx-byte": "0"}}, nil)
	if ports[0].InErrors != nil {
		t.Errorf("errors at %v although they were not read: a displayed zero would "+
			"assert an absence of errors that nobody observed", *ports[0].InErrors)
	}
	// And the counterpart: a present field at zero does produce a value that means something.
	ports = foldPorts([]Row{{"name": "ether1"}}, []Row{{"name": "ether1", "rx-fcs-error": "0"}})
	if ports[0].InErrors == nil || *ports[0].InErrors != 0 {
		t.Errorf("a counter present at zero must produce a sample: %v",
			ports[0].InErrors)
	}
}

// TestNegotiatedRateComesFromMonitorAndOnlyFromMonitor
//
// This is THE reason the binary API channel exists: neither the 87 fields of
// `/interface/ethernet` nor those of `/interface` carry `speed`. A port fallen to
// 100 Mbit/s for a cut pair is invisible through REST — everything works, ten times slower.
func TestNegotiatedRateComesFromMonitorAndOnlyFromMonitor(t *testing.T) {
	// First proof of absence, on the real capture: no rate field in either menu. If a
	// firmware added one, this test would say so — and the justification for the `api`
	// channel would change with it.
	var r Row
	for _, r = range fixture(t, "interface_ethernet.json") {
		var field string
		for field = range r {
			if field == "speed" || field == "rate" {
				t.Errorf("`/interface/ethernet` carries field %q: the binary API channel "+
					"justification ('negotiated rate is not in REST') "+
					"is no longer true", field)
			}
		}
	}

	var tel = readTelemetry(t, captureSource(t))
	var byName = map[string]PortCounters{}
	var p PortCounters
	for _, p = range tel.Ports {
		byName[p.Name] = p
	}
	if byName["sfp-sfpplus2"].SpeedMbps == nil ||
		*byName["sfp-sfpplus2"].SpeedMbps != 10000 {
		t.Errorf("cage 2 rate: %v expected 10000 Mbit/s (`rate: 10Gbps`)",
			byName["sfp-sfpplus2"].SpeedMbps)
	}
	// Copper ports were not queried — `monitor` runs only for cages (two requests
	// instead of twenty-six) — so no rate, and above all not a zero.
	if byName["ether1"].SpeedMbps != nil {
		t.Error("a copper port is not queried by `monitor`: its rate must " +
			"stay ABSENT, not zero")
	}
}

// TestParseRate — RouterOS forms, and the thousand factor.
//
// `10Gbps` and `10Mbps` differ by one letter. Reading the digit without its unit would
// yield a thousand times less: the same trap as nomenclature `2S+` versus `2S`, and it
// costs the same factor.
func TestParseRate(t *testing.T) {
	var cases = []struct {
		in       string
		expected float64
		readable bool
	}{
		{"10Gbps", 10000, true},
		{"1Gbps", 1000, true},
		{"100Mbps", 100, true},
		{"10Mbps", 10, true},
		{"10gbps", 10000, true},
		{"  1Gbps ", 1000, true},
		{"", 0, false},
		{"10", 0, false},
		{"10Tbps", 0, false},
		{"unknown", 0, false},
	}
	var c int
	for c = range cases {
		var got, ok = parseRate(cases[c].in)
		if ok != cases[c].readable || (ok && got != cases[c].expected) {
			t.Errorf("parseRate(%q) = %v,%t — expected %v,%t",
				cases[c].in, got, ok, cases[c].expected, cases[c].readable)
		}
	}
}

// TestOpticalDiagnosticsReadFromBuiltFixture
//
// RouterOS serves ALREADY CONVERTED values: voltage in volts, power in dBm. That is
// what distinguishes this channel from the other two — SNMP serves thousandths of dBm,
// SwOS serves tenths of microwatt to pass through the logarithm. Three channels, three
// scales for the same physical quantity.
func TestOpticalDiagnosticsReadFromBuiltFixture(t *testing.T) {
	var tel = readTelemetry(t, captureSource(t))
	if len(tel.Optics) != 2 {
		t.Fatalf("%d cage(s) queried, 2 expected (both facade `sfp-sfpplus`): %+v",
			len(tel.Optics), tel.Optics)
	}
	var byPort = map[string]Optic{}
	var o Optic
	for _, o = range tel.Optics {
		byPort[o.Port] = o
	}

	var populated = byPort["sfp-sfpplus2"]
	if !populated.Present {
		t.Fatal("cage 2 carries a module in the fixture")
	}
	if populated.Part != "SFP-10G-BX" || populated.Vendor != "FS" {
		t.Errorf("module identity: %+v", populated)
	}
	if populated.TemperatureC == nil || *populated.TemperatureC != 50 {
		t.Errorf("module temperature: %v", populated.TemperatureC)
	}
	if populated.SupplyVolts == nil || *populated.SupplyVolts != 3.261 {
		t.Errorf("supply voltage: %v — RouterOS serves it in VOLTS, already "+
			"converted", populated.SupplyVolts)
	}
	if populated.RxPowerDBM == nil || *populated.RxPowerDBM != -5.57 {
		t.Errorf("received power: %v — RouterOS serves it in dBm, therefore NEGATIVE, and "+
			"a parser that rejected the sign would yield zero", populated.RxPowerDBM)
	}
	// Both alarm flags are PRESENT and false in the fixture: present and false is not
	// the same as absent ('this firmware does not say').
	if populated.RxLoss == nil || *populated.RxLoss {
		t.Errorf("signal loss flag: %v — present and false expected", populated.RxLoss)
	}

	// Cage 1 is not in the `monitor` fixture: the source returns an EMPTY record,
	// which is what RouterOS does for a cage without module — absence of `sfp-*` fields,
	// not a sentinel value.
	var empty = byPort["sfp-sfpplus1"]
	if empty.Present {
		t.Error("a cage without `sfp-*` field must be declared EMPTY, not faulty")
	}
}

// TestPartialReadSaysSoInsteadOfStayingSilent
//
// A partial read is more useful than an error — that is exactly when something is wrong
// that one opens this screen. But a partial read that does not say what is missing is a
// lie by omission: on a diagnostic screen, it sends one hunting an equipment fault where
// there is a channel that cannot read.
func TestPartialReadSaysSoInsteadOfStayingSilent(t *testing.T) {
	var src = captureSource(t)
	src.deny["system/health"] = fakeErr("no such command prefix")
	src.deny["interface/ethernet/monitor"] = fakeErr("POST not exercised on this transport")

	var tel = readTelemetry(t, src)
	// What could be read was read correctly.
	if tel.CPULoadPercent == nil {
		t.Error("a failing source must not take down the others")
	}
	if len(tel.Sensors) != 0 {
		t.Errorf("no sensor should have been read: %+v", tel.Sensors)
	}
	// And the gap is STATED, with the source name and the reason.
	if len(tel.Notes) == 0 {
		t.Fatal("a partial read without note is a lie by omission")
	}
	var note = strings.Join(tel.Notes, " ")
	var expected string
	for _, expected = range []string{"PARTIAL READ", "system/health", "monitor"} {
		if !strings.Contains(note, expected) {
			t.Errorf("note must cite %q: %q", expected, note)
		}
	}
}

// TestMissingInterfaceIsAnErrorNotANote
//
// This is the only failure that entails others: without `/interface`, there is no port
// list, therefore nothing to attach counters or transceivers to. Saying it once beats
// three notes for a single cause.
func TestMissingInterfaceIsAnErrorNotANote(t *testing.T) {
	var src = captureSource(t)
	src.deny["interface"] = fakeErr("tunnel down")

	var tel, err = ReadTelemetry(context.Background(), src)
	if err == nil {
		t.Fatal("absence of `/interface` must return an error: nothing can " +
			"be attached to a port anymore")
	}
	if len(tel.Notes) == 0 || !strings.Contains(strings.Join(tel.Notes, " "), "interface") {
		t.Errorf("error must be accompanied by the note naming the cause: %+v",
			tel.Notes)
	}
}

// TestSamplesDoNotCarryIfindex — obtained by CONSTRUCTION, not by a guardrail.
//
// The controller requires the `iftype` label to touch inventory (§2.1 of
// [[Unified Monitoring and Pipelines]]). This channel having no interface index to give,
// its samples feed curves WITHOUT ever being able to modify the facade: the rule 'telemetry
// never CREATES a port' is here a property of the channel, not a rule one must remember
// to apply.
//
// Fabricating an index from a list rank would be the exact fault this test forbids: an
// added port would shift all following ones, therefore reassign one port's traffic to another.
func TestSamplesDoNotCarryIfindex(t *testing.T) {
	var tel = readTelemetry(t, captureSource(t))
	var samples = tel.Samples("dev-crs")
	if len(samples) == 0 {
		t.Fatal("no sample produced")
	}
	var s int
	for s = range samples {
		if samples[s].Labels["device"] != "dev-crs" {
			t.Fatalf("sample without device: %+v", samples[s])
		}
		var _, hasIfindex = samples[s].Labels["ifindex"]
		var _, hasIftype = samples[s].Labels["iftype"]
		if hasIfindex || hasIftype {
			t.Fatalf("binary API sample carrying interface index: %+v — "+
				"this channel has none, and fabricating one from list rank "+
				"would reassign one port's traffic to another on first add",
				samples[s])
		}
	}
}

// TestSamplesRouteSensorsByTheirTYPE
//
// Type decides the series, never the name. RouterOS 7 serves `type: "C"` for temperature
// and `"V"` for voltage; trusting the name would require a label list per range, and a
// differently named sensor would land in the wrong series — 3.3 V plotted among temperatures.
func TestSamplesRouteSensorsByTheirTYPE(t *testing.T) {
	var tel = Telemetry{
		Sensors: []Sensor{
			{Name: "cpu-temperature", Type: "C", Value: 72},
			{Name: "psu1-voltage", Type: "V", Value: 12.1},
			{Name: "fan1", Type: "RPM", Value: 4200},
			{Name: "unknown", Type: "?", Value: 3},
		},
	}
	var series = map[string]string{}
	var s metricbuf.Sample
	for _, s = range tel.Samples("dev-1") {
		if s.Labels["sensor"] != "" {
			series[s.Labels["sensor"]] = s.Name
		}
	}
	if series["cpu-temperature"] != metricbuf.MetricTemperature {
		t.Errorf("temperature routed to %q", series["cpu-temperature"])
	}
	if series["psu1-voltage"] != metricbuf.MetricVoltage {
		t.Errorf("voltage routed to %q", series["psu1-voltage"])
	}
	if series["fan1"] != metricbuf.MetricFanRPM {
		t.Errorf("fan routed to %q", series["fan1"])
	}
	// An unknown type is NOT routed at random: without a unit, its value means nothing,
	// and inventing one would alert on a threshold that does not apply.
	var _, classified = series["unknown"]
	if classified {
		t.Errorf("an unknown-type sensor must produce no series, it is "+
			"classified in %q", series["unknown"])
	}
}

// TestEmptyCageProducesNoSample
//
// Publishing zeros would show two dead transceivers on every switch whose cages are free:
// a permanent false alert across most of the fleet.
func TestEmptyCageProducesNoSample(t *testing.T) {
	var tel = Telemetry{Optics: []Optic{{Port: "sfp-sfpplus1", Present: false}}}
	var samples = tel.Samples("dev-1")
	var s metricbuf.Sample
	for _, s = range samples {
		if strings.Contains(s.Name, "sfp") {
			t.Errorf("empty cage produced %q = %v", s.Name, s.Value)
		}
	}
}

// TestPortWithoutLinkStillPublishesItsState
//
// A port's state is ALWAYS published, including at zero: it is an observation, and
// 'this port carries no link' is a fact. That is the difference from a counter, whose
// zero could mean 'not read'.
func TestPortWithoutLinkStillPublishesItsState(t *testing.T) {
	var tel = Telemetry{Ports: []PortCounters{{Name: "ether1", Running: false}}}
	var seen bool
	var s metricbuf.Sample
	for _, s = range tel.Samples("dev-1") {
		if s.Name == metricbuf.MetricIfOperStatus {
			seen = true
			if s.Value != 0 {
				t.Errorf("state of port without link: %v expected 0", s.Value)
			}
			if math.IsNaN(s.Value) {
				t.Error("non-numeric state")
			}
		}
	}
	if !seen {
		t.Error("a port's state must always be published: it is an observation")
	}
}

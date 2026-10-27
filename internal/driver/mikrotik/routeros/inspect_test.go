package routeros

import (
	"reflect"
	"testing"

	"meshcraft/internal/driver"
)

// TestReferenceHardwareFacade replays the `/rest/interface` capture and verifies the
// read facade matches the real CRS326-24G-2S+: 24 gigabit copper ports and 2 SFP+ cages.
//
// This test's value goes beyond counting. This connector does NOT DERIVE the facade from a
// product reference, it READS it — and that is what lets it draw a chassis it has never
// seen. If the read degrades, everything else (port groups, rates, copper/optical distinction)
// becomes wrong silently.
func TestReferenceHardwareFacade(t *testing.T) {
	var ports = switchPorts(fixture(t, "interface.json"),
		fixture(t, "interface_ethernet.json"))

	// 26 switching ports out of 29 interfaces in the capture: bridge, loopback and `vlan99`
	// are excluded.
	if len(ports) != 26 {
		t.Fatalf("%d switching ports, 26 expected (24 copper + 2 SFP+)", len(ports))
	}

	// Facade ORDER: ether1 first, and especially ether2 BEFORE ether10 — alphabetical sort
	// would invert them and the drawing would no longer match the device in front of you.
	if ports[0].Name != "ether1" {
		t.Errorf("first port = %q, expected 'ether1'", ports[0].Name)
	}
	if ports[1].Name != "ether2" {
		t.Errorf("second port = %q, expected 'ether2' — alphabetical sort "+
			"would have put ether10 here", ports[1].Name)
	}
	// Optical cages END the facade, as on the hardware.
	if ports[24].Name != "sfp-sfpplus1" || ports[25].Name != "sfp-sfpplus2" {
		t.Errorf("last two ports are %q and %q, expected 'sfp-sfpplus1' "+
			"and 'sfp-sfpplus2'", ports[24].Name, ports[25].Name)
	}

	// Copper versus optical, and rates, both READ on the device.
	if ports[0].SFP {
		t.Error("ether1 is not an optical cage")
	}
	if ports[0].SpeedMbps != 1000 {
		t.Errorf("ether1 = %d Mbit/s, expected 1000 (observed: advertise caps at "+
			"1G-baseT-full)", ports[0].SpeedMbps)
	}
	if !ports[25].SFP {
		t.Error("sfp-sfpplus2 is an optical cage")
	}
	if ports[25].SpeedMbps != 10000 {
		t.Errorf("sfp-sfpplus2 = %d Mbit/s, expected 10000 (observed: advertise "+
			"includes 10G-baseSR-LR)", ports[25].SpeedMbps)
	}
	// The only port carrying a link on the reference device.
	if !ports[25].Running {
		t.Error("sfp-sfpplus2 carries a link (observed `running: true`)")
	}
}

// TestFrameCeilingReadFromDevice
//
// This test exists because of an error that had to be fixed: the connector first declared
// that this firmware 'does not publish `max-l2mtu`', a conclusion drawn from enumerating
// the 87 fields of `/interface/ethernet` — where it is indeed absent. It is actually
// served by `/interface`, at 10218 bytes, and replaying the captures showed it.
//
// The moral is the one that justifies the whole testdata/ directory: a comment asserting
// absence never verifies itself, a test replaying a capture does. The value is taken up
// here so a firmware that changed its limit would say so instead of letting meshcraft validate
// frames the device would reject.
func TestFrameCeilingReadFromDevice(t *testing.T) {
	var ifaces = fixture(t, "interface.json")

	if maxL2MTU(ifaces) != 10218 {
		t.Errorf("frame ceiling = %d, expected 10218 (observed `max-l2mtu` on the "+
			"26 ports of /rest/interface)", maxL2MTU(ifaces))
	}

	// And the MINIMUM rule: a frame crosses the switch only if ALL ports it uses accept it.
	// Keeping the maximum would validate an intent a single port could fail.
	var mixed = []Row{
		{"type": "ether", "name": "ether1", "max-l2mtu": "10218"},
		{"type": "ether", "name": "ether2", "max-l2mtu": "4074"},
		// The bridge is not a switching port: it bounds nothing.
		{"type": "bridge", "name": "bridge", "max-l2mtu": "1500"},
	}
	if maxL2MTU(mixed) != 4074 {
		t.Errorf("on a heterogeneous facade, retained ceiling = %d, expected 4074 "+
			"(lowest of switching ports)", maxL2MTU(mixed))
	}
}

// TestMeshcraftNamesAreRouterOSNames — a design decision, locked down.
//
// The Instant On connector must TRANSLATE: its device numbers ports 1 to 52 and meshcraft
// names them 'GE1', hence a prefix to add then remove on every read and write. Here RouterOS
// NAMES its ports, and those names are already stable identifiers. Reusing the same removes
// all translation, therefore a whole class of bug: writing to port 3 believing one writes to
// port 4. On a VLAN table, that kind of error cuts off a customer.
//
// This test would fail if someone reintroduced a conventional prefix.
func TestMeshcraftNamesAreRouterOSNames(t *testing.T) {
	var id = DeviceIdentity{Ports: switchPorts(fixture(t, "interface.json"),
		fixture(t, "interface_ethernet.json"))}
	var names = id.PortNames()

	// Returned names must be EXACTLY those of the device, the ones used for writing.
	if names[0] != "ether1" || names[23] != "ether24" {
		t.Errorf("port names = %q…%q, expected 'ether1'…'ether24' without "+
			"conventional prefix", names[0], names[23])
	}
	if names[25] != "sfp-sfpplus2" {
		t.Errorf("last name = %q, expected 'sfp-sfpplus2'", names[25])
	}

	// And they must match read-state keys: that correspondence makes placement possible
	// without translation.
	var st = stateFromFixtures(t)
	var name string
	for _, name = range names {
		var _, present = st.Ports[name]
		if !present {
			t.Errorf("facade port %q does not exist in read state: identification and read "+
				"do not name ports the same way, and placement would write beside the target",
				name)
		}
	}
}

// TestPortGroupsContiguous: facade in groups, for the interface.
//
// Groups are verified CONTIGUOUS. Announcing 'ether, 24 ports from 1' when one is missing
// in the middle would draw a socket that does not exist, and meshcraft would eventually write
// to it.
func TestPortGroupsContiguous(t *testing.T) {
	var id = DeviceIdentity{Ports: switchPorts(fixture(t, "interface.json"),
		fixture(t, "interface_ethernet.json"))}
	var groups = id.PortGroups()

	if len(groups) != 2 {
		t.Fatalf("%d port groups, 2 expected (copper then SFP+): %+v",
			len(groups), groups)
	}
	// Copper.
	if groups[0]["prefix"] != "ether" || groups[0]["count"] != 24 ||
		groups[0]["from"] != 1 || groups[0]["speed"] != "1G" {
		t.Errorf("copper group = %+v, expected prefix=ether count=24 from=1 speed=1G",
			groups[0])
	}
	if groups[0]["poe"] != false {
		t.Errorf("copper group: PoE expected false on this model, got %v",
			groups[0]["poe"])
	}
	// Optical cages. Prefix is the device's, not a conventional 'GE': that is why the
	// interface reconstructs (`prefix` + number) as `sfp-sfpplus1`.
	if groups[1]["prefix"] != "sfp-sfpplus" || groups[1]["count"] != 2 ||
		groups[1]["from"] != 1 || groups[1]["speed"] != "10G" {
		t.Errorf("optical group = %+v, expected prefix=sfp-sfpplus count=2 from=1 "+
			"speed=10G", groups[1])
	}
	if groups[1]["label"] != "SFP+" {
		t.Errorf("optical group label = %v, expected 'SFP+'", groups[1]["label"])
	}
}

// TestGroupSplitOnGappedNumbering: the case contiguity protects.
func TestGroupSplitOnGappedNumbering(t *testing.T) {
	var id = DeviceIdentity{Ports: []Port{
		{Name: "ether1", DefaultName: "ether1", SpeedMbps: 1000},
		{Name: "ether2", DefaultName: "ether2", SpeedMbps: 1000},
		// ether3 missing: removed card, disabled port no longer reported.
		{Name: "ether4", DefaultName: "ether4", SpeedMbps: 1000},
	}}
	var groups = id.PortGroups()

	if len(groups) != 2 {
		t.Fatalf("gapped numbering must produce TWO groups, got "+
			"%d: %+v — a single group of 3 from 1 would draw ether3, which "+
			"does not exist", len(groups), groups)
	}
	if groups[0]["count"] != 2 || groups[0]["from"] != 1 {
		t.Errorf("first group = %+v, expected 2 ports from 1", groups[0])
	}
	if groups[1]["count"] != 1 || groups[1]["from"] != 4 {
		t.Errorf("second group = %+v, expected 1 port from 4", groups[1])
	}
}

// TestIdentityReadFromCaptures locks identity fields against real device responses.
//
// The most important point is the distinction between the TWO versions the device publishes:
// RouterOS (the system, `7.21beta3 (testing)`) and RouterBOOT (the boot loader, `6.42.5`).
// Presenting them as one would make the device appear four major versions behind — and push
// for an update that is not warranted.
func TestIdentityReadFromCaptures(t *testing.T) {
	var resource = fixtureOne(t, "system_resource.json")
	var board = fixtureOne(t, "system_routerboard.json")
	var identity = fixtureOne(t, "system_identity.json")

	if resource.Get("board-name") != "CRS326-24G-2S+" {
		t.Errorf("board name = %q", resource.Get("board-name"))
	}
	if resource.Get("version") != "7.21beta3 (testing)" {
		t.Errorf("RouterOS version = %q", resource.Get("version"))
	}
	if board.Get("serial-number") != "945509EF00B0" {
		t.Errorf("serial number = %q", board.Get("serial-number"))
	}
	// The two versions are DIFFERENT, and that is the fact not to lose.
	if board.Get("current-firmware") == resource.Get("version") {
		t.Error("RouterOS and RouterBOOT carry the same version in the capture: " +
			"the two fields were confused")
	}
	if board.Get("current-firmware") != "6.42.5" {
		t.Errorf("RouterBOOT version = %q, expected '6.42.5'",
			board.Get("current-firmware"))
	}
	if identity.Get("name") != "lan-r4" {
		t.Errorf("system name = %q, expected 'lan-r4'", identity.Get("name"))
	}
}

// TestHealthPublishesOnlyTemperature: what justifies the 'health' channel floor.
//
// Observed: `/system/health` returns ONLY ONE value on this board. No voltage, no fan, no
// power rail. That grounds both the 60 s cadence and — independently — the PoE absence conclusion.
func TestHealthPublishesOnlyTemperature(t *testing.T) {
	var rows = fixture(t, "system_health.json")
	if len(rows) != 1 {
		t.Fatalf("%d sensors in /system/health, 1 expected", len(rows))
	}
	if rows[0].Get("name") != "cpu-temperature" {
		t.Errorf("sensor = %q, expected 'cpu-temperature'", rows[0].Get("name"))
	}
}

// TestMikroTikNomenclature: product reference read as MikroTik writes it.
//
// Used to derive facade and PoE when only the model name is known — i.e. in Capabilities,
// which receives only a string. The main trap is the family prefix: 'CRS326' contains '326',
// and counting it would yield 326 ports.
func TestMikroTikNomenclature(t *testing.T) {
	var cases = []struct {
		model  string
		ports  int
		poe    bool
		parsed bool
	}{
		// Reference hardware: 24 gigabit copper + 2 SFP+.
		{"CRS326-24G-2S+", 26, false, true},
		// ADJACENT groups in the same segment: 48 + 4 + 2.
		{"CRS354-48G-4S+2Q+", 54, false, true},
		// A `P` group designates PoE-out ports.
		{"CRS328-24P-4S+", 28, true, true},
		{"CRS305-1G-4S+", 5, false, true},
		{"CRS310-1G-5S-4S+", 10, false, true},
		// Without hyphen, there is no port group to read: conclude nothing rather than
		// invent a facade.
		{"CCR2004", 0, false, false},
		{"", 0, false, false},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.model, func(t *testing.T) {
			var got = parseDesignation(c.model)
			if got.Ports != c.ports {
				t.Errorf("%q: %d ports, expected %d", c.model, got.Ports, c.ports)
			}
			if got.PoE != c.poe {
				t.Errorf("%q: PoE=%t, expected %t", c.model, got.PoE, c.poe)
			}
			if got.Parsed != c.parsed {
				t.Errorf("%q: parsed=%t, expected %t", c.model, got.Parsed, c.parsed)
			}
		})
	}
}

// TestUnknownModelFallsBackToFloor: the project rule, applied.
//
// 'An unknown capability is assumed absent.' A model we know nothing about must not inherit
// CRS326 limits: it must inherit the floor, even at the cost of refusing an intent the device
// would have accepted. That is inconvenient; the opposite is an outage.
func TestUnknownModelFallsBackToFloor(t *testing.T) {
	var unknown = (&Driver{}).Capabilities("")
	var known = (&Driver{}).Capabilities("CRS354-48G-4S+2Q+")

	if unknown.MaxLAGs != 4 {
		t.Errorf("unknown model: MaxLAGs = %d, expected floor of 4",
			unknown.MaxLAGs)
	}
	// 54 ports allow at most 27 two-port aggregates: the bound is ARITHMETIC, derived from
	// the facade, not a manufacturer limit.
	if known.MaxLAGs != 27 {
		t.Errorf("CRS354 (54 ports): MaxLAGs = %d, expected 27", known.MaxLAGs)
	}
	// An unknown model has no PoE until something says so.
	if unknown.Has("PoE") {
		t.Error("unknown model: PoE must be assumed ABSENT")
	}
}

// TestConnectorRegisteredAndRecognizes: connector must be reachable through the registry,
// and its fingerprint must match what the probe actually observes.
//
// Both indices are OBSERVED on 192.0.2.54 by the controller probe: `http_title: "RouterOS"`
// and `ssh_banner: "SSH-2.0-ROSSSH"`. The title is served WITHOUT authentication, so
// recognition requires no credentials.
func TestConnectorRegisteredAndRecognizes(t *testing.T) {
	var d, known = driver.Get("mikrotik-routeros")
	if !known {
		t.Fatal("mikrotik-routeros connector is not registered")
	}

	// What the probe actually saw on the reference device.
	var observed = driver.Evidence{
		HTTPTitle: "RouterOS",
		SSHBanner: "SSH-2.0-ROSSSH",
		OpenPorts: []int{22, 23, 80},
	}
	var score = d.Fingerprint(observed)
	if score < 90 {
		t.Errorf("fingerprint on real observation = %d, expected at least 90", score)
	}

	// SwOS is the NEIGHBOURING system: picking the wrong connector means speaking REST to
	// a device that only has forms.
	if d.Fingerprint(driver.Evidence{HTTPTitle: "MikroTik SwOS"}) != 0 {
		t.Error("SwOS must never match the RouterOS connector")
	}
	// Recognized vendor, undetermined system: candidate, never certainty.
	var vague = d.Fingerprint(driver.Evidence{SysDescr: "MikroTik"})
	if vague == 0 || vague >= 90 {
		t.Errorf("'MikroTik' alone = %d, expected intermediate score", vague)
	}
}

func TestRateReadFromAdvertise(t *testing.T) {
	var cases = []struct {
		name      string
		advertise string
		expected  int
	}{
		{"empty", "", 0},
		{"gigabit copper observed on ether1",
			"10M-baseT-half,10M-baseT-full,100M-baseT-half,100M-baseT-full," +
				"1G-baseT-half,1G-baseT-full", 1000},
		{"SFP+ observed on sfp-sfpplus2",
			"10M-baseT-half,1G-baseX,2.5G-baseT,5G-baseT,10G-baseT,10G-baseSR-LR," +
				"10G-baseCR", 10000},
		{"hundred megabits only", "100M-baseT-full", 100},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = maxAdvertised(c.advertise)
			if got != c.expected {
				t.Errorf("maxAdvertised(%q) = %d, expected %d", c.advertise, got, c.expected)
			}
		})
	}
}

func TestRateLabel(t *testing.T) {
	var got = []string{speedLabel(1000), speedLabel(10000), speedLabel(100), speedLabel(0)}
	var expected = []string{"1G", "10G", "100M", ""}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("labels = %v, expected %v", got, expected)
	}
}

package ios

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// No storm-control line has been sent to a Catalyst by this connector. "stp"
// and "port_admin" were declared on live captures; this one has none, and the
// staging rule (driver/stormcontrol.go) is what this assertion pins.
func TestWritesExtraStormControlIsDeclaredOnLiveProof(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("storm_control") {
		t.Fatal("\"storm_control\" was proven live on 2026-08-08 — two classes in two " +
			"different units written through ApplyStormControl on the lab 2960-X, read " +
			"back, a second identical apply writing nothing, and a removal leaving the " +
			"unnamed class alone — and must stay declared unless that evidence is withdrawn")
	}
	// The two earned earlier must stay declared: a refactor of WritesExtra that
	// turned this file's assertion green by returning true for everything, or
	// false for everything, would be caught by one of these two halves.
	if !d.WritesExtra("stp") || !d.WritesExtra("port_admin") {
		t.Fatal("the two capabilities earned on hardware were lost")
	}
	if !d.WritesExtra("dhcp_snooping") {
		t.Error("dhcp_snooping was earned on hardware on 2026-08-09 and is no longer declared")
	}
	if d.WritesExtra("poe") {
		t.Error("declares a capability nothing in this package has exercised")
	}
}

// THE CENTRAL FACT OF THIS FILE. IOS's unicast limiter polices ALL unicast, so
// the unknown-unicast class is not expressed here and must never be substituted:
// a limiter installed on established flows would be reported back by the device
// as exactly what was written, so nothing downstream could ever catch it.
func TestStormControlCapsRefuseUnknownUnicast(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	if caps.Expresses(driver.StormUnknownUnicast) {
		t.Fatal("unknown_unicast is declared on a platform whose keyword is \"unicast\" " +
			"and covers established flows")
	}
	if !caps.Expresses(driver.StormBroadcast) || !caps.Expresses(driver.StormMulticast) {
		t.Error("the two classes IOS does name as themselves are not declared")
	}
	// The one platform in the fleet that speaks all three units.
	var u driver.StormRateUnit
	for _, u = range driver.StormRateUnits {
		if !caps.Accepts(u) {
			t.Errorf("IOS takes %s and the caps do not declare it", u)
		}
	}
	var joined = strings.Join(caps.Why, " ")
	if !strings.Contains(joined, "storm-control unicast") {
		t.Error("the capability answer must name the platform keyword an operator would " +
			"otherwise go looking for")
	}
}

// The refusal must reach ApplyResult.Skipped as a driver.Unsupported naming the
// keyword — not a bare "unsupported", which tells an operator nothing about
// what they could have instead.
func TestUnknownUnicastRefusalNamesTheKeywordAndTheCost(t *testing.T) {
	var err = driver.RefuseStormClass(stormConnector, driver.StormUnknownUnicast,
		"GE1", unknownUnicastRefusal)
	var msg = err.Error()
	if !strings.Contains(msg, "storm-control unicast") {
		t.Error("the refusal does not name the platform's own keyword")
	}
	if !strings.Contains(msg, "established flows") {
		t.Error("the refusal does not say what the substitution would have cost")
	}
	if !strings.Contains(msg, "cisco-ios") {
		t.Error("the refusal does not say which connector said no")
	}
}

// THE UNIT KEYWORD IS THE CONFIGURATION on this platform. Same digits, three
// commands, five orders of magnitude apart. This test is the wire form of that
// sentence.
func TestStormLevelArgCarriesTheUnitKeyword(t *testing.T) {
	var cases = []struct {
		unit driver.StormRateUnit
		want string
	}{
		{driver.StormRatePercent, "10"},
		{driver.StormRatePPS, "pps 10"},
		{driver.StormRateBPS, "bps 10"},
	}
	var i int
	for i = range cases {
		var got = stormLevelArg(driver.StormLimit{
			Enabled: true, Rate: 10, Unit: cases[i].unit,
		})
		if got != cases[i].want {
			t.Errorf("%s rendered as %q, want %q", cases[i].unit, got, cases[i].want)
		}
	}
}

// The full block, pinned — including the disable form, which on IOS keeps the
// "level" word: "no storm-control broadcast" alone is a different statement.
func TestStormCmdsWireForm(t *testing.T) {
	var cmds = stormCmds("GigabitEthernet0/5", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 1000, Unit: driver.StormRatePPS},
		{Class: driver.StormMulticast, Enabled: false},
	})
	var want = []string{
		"interface GigabitEthernet0/5",
		"storm-control broadcast level pps 1000",
		"no storm-control multicast level",
		"exit",
	}
	if len(cmds) != len(want) {
		t.Fatalf("got %v", cmds)
	}
	var i int
	for i = range want {
		if cmds[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, cmds[i], want[i])
		}
	}
}

// A class the platform does not express produces NO LINE, even if a caller got
// past the refusal. There is no near-equivalent keyword to fall back on, and
// falling back is the one thing this file exists to prevent.
func TestStormCmdsEmitNothingForUnknownUnicast(t *testing.T) {
	var cmds = stormCmds("GigabitEthernet0/1", []driver.StormLimit{
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
	})
	var line string
	for _, line = range cmds {
		if strings.Contains(line, "storm-control") {
			t.Fatalf("a line was emitted for a class this platform cannot express: %q", line)
		}
	}
}

// "1k" is a THOUSAND. A parser that dropped the suffix would read a limiter a
// thousand times tighter than the device carries — and the next apply would
// "correct" the device to that wrong value, turning a read defect into a write.
func TestParseStormRateHonoursMultipliers(t *testing.T) {
	var cases = map[string]float64{
		"1k": 1000, "10m": 1e7, "2g": 2e9, "500": 500, "10.5": 10.5,
	}
	var raw string
	var want float64
	for raw, want = range cases {
		var got, ok = parseStormRate(raw)
		if !ok || got != want {
			t.Errorf("%q parsed as %v (ok=%v), want %v", raw, got, ok, want)
		}
	}
	var _, ok = parseStormRate("")
	if ok {
		t.Error("an empty argument parsed as a rate")
	}
}

// The read must keep percent, pps and bps apart. Folding them together would
// report "1000" for a packet-rate limit and let a percent-shaped apply believe
// the device already carried it.
func TestParseStormControlKeepsUnitsApart(t *testing.T) {
	var cfg = `
interface GigabitEthernet0/1
 storm-control broadcast level 10.00
!
interface GigabitEthernet0/2
 storm-control broadcast level pps 1k
 storm-control multicast level bps 10m
!
`
	var ports, unicast = parseStormControl(cfg)
	if len(ports) != 2 {
		t.Fatalf("got %d ports: %+v", len(ports), ports)
	}
	if len(unicast) != 0 {
		t.Errorf("a unicast statement was reported where there is none: %v", unicast)
	}
	var bc1, _ = ports[0].Limit(driver.StormBroadcast)
	if bc1.Unit != driver.StormRatePercent || bc1.Rate != 10 {
		t.Errorf("GE1 broadcast = %+v, want 10 percent", bc1)
	}
	var bc2, _ = ports[1].Limit(driver.StormBroadcast)
	if bc2.Unit != driver.StormRatePPS || bc2.Rate != 1000 {
		t.Errorf("GE2 broadcast = %+v, want 1000 pps", bc2)
	}
	var mc2, _ = ports[1].Limit(driver.StormMulticast)
	if mc2.Unit != driver.StormRateBPS || mc2.Rate != 1e7 {
		t.Errorf("GE2 multicast = %+v, want 1e7 bps", mc2)
	}
	// The class this platform cannot express is NOT reported as "no limiter":
	// a false absence on screen promises a control that could be added.
	var _, read = ports[0].Limit(driver.StormUnknownUnicast)
	if read {
		t.Error("unknown_unicast was reported on a platform that has no such limiter")
	}
}

// A "storm-control unicast" already on the device is REPORTED, never folded
// into the unknown-unicast class. It throttles traffic an operator may be
// looking for an explanation of, and it is not ours to rewrite or remove.
func TestParseStormControlReportsUnicastSeparately(t *testing.T) {
	var ports, unicast = parseStormControl(
		"interface GigabitEthernet0/3\n storm-control unicast level 20\n!\n")
	if len(unicast) != 1 || unicast[0] != "GE3" {
		t.Fatalf("unicast statement not reported: %v", unicast)
	}
	var _, read = ports[0].Limit(driver.StormUnknownUnicast)
	if read {
		t.Fatal("\"storm-control unicast\" was mapped onto the unknown-unicast class — " +
			"that is the exact substitution this connector refuses to make")
	}
	// Since ADR-0020 the observation rides on the PORT rather than in a
	// device-wide sentence. A []string of port names could not be shown beside
	// the control it concerns, which is why this connector had to invent one and
	// why the contract now carries the shape.
	var f, ok = ports[0].ForeignFor(driver.StormUnknownUnicast)
	if !ok {
		t.Fatalf("the statement is not attached to the port: %+v", ports[0])
	}
	if f.Spelling != "storm-control unicast level 20" {
		t.Errorf("the device's own line was not kept verbatim: %q", f.Spelling)
	}
	if !strings.Contains(f.Because, "all of it") {
		t.Errorf("the reason does not say what the keyword actually covers: %q", f.Because)
	}
}

// stormLimitSame must compare the UNIT too. "10 pps" and "10 %" share a
// Rate, and a comparison on the number alone would decide a device configured in
// percent already carried the packet-rate limit — and write nothing at all.
func TestStormLimitSameComparesTheUnit(t *testing.T) {
	var pct = driver.StormLimit{Enabled: true, Rate: 10, Unit: driver.StormRatePercent}
	var pps = driver.StormLimit{Enabled: true, Rate: 10, Unit: driver.StormRatePPS}
	if stormLimitSame(pct, pps) {
		t.Fatal("10 % and 10 pps compared equal")
	}
	if !stormLimitSame(pct, pct) {
		t.Error("a limit does not equal itself")
	}
	var off = driver.StormLimit{Enabled: false}
	if !stormLimitSame(off, driver.StormLimit{Enabled: false, Rate: 99}) {
		t.Error("two removals differ because one carried a stale rate")
	}
}

// TestParseStormControlOnCapturedHardware holds the parser to what a real
// 2960-X prints back, which is not what was typed into it.
//
// Captured 2026-08-08 on the lab CiscoLabFirst1 (WS-C2960X-24TD-L) through
// meshcraft's own SOCKS route: limiters set on Gi1/0/1 — an access port with
// nothing plugged into it — read back from "show running-config interface",
// then removed. Four separate re-spellings happened on the way, and each one is
// a way a parser could be wrong:
//
//	sent "level 5.00"       → printed "level 5.00"      decimals KEPT
//	sent "level pps 1000"   → printed "level pps 1k"    suffixed
//	sent "level bps 2000000"→ printed "level bps 2m"    suffixed
//	sent "level pps 1500"   → printed "level pps 1.5k"  FRACTIONAL suffix
//
// The last one is the one worth having a capture for. It is easy to anticipate
// "1k" and still write a parser that takes an integer before the letter; this
// device produces "1.5k", so the digits must go through a float parse before
// the multiplier is applied. parseStormRate already does, and this is the
// evidence it needed.
//
// Note also what Cisco does NOT do: it keeps "5.00" where Arista normalises
// the same value to "5" (cf. arista/eos/stormcontrol.go). Two platforms, two
// opposite normalisations of one number — which is exactly why the idempotence
// check compares numbers and never text.
func TestParseStormControlOnCapturedHardware(t *testing.T) {
	// Verbatim, including IOS's single-space block indent.
	const captured = `interface GigabitEthernet1/0/1
 switchport access vlan 100
 switchport mode access
 storm-control broadcast level 5.00
 storm-control multicast level pps 1.5k
`
	var ports, _ = parseStormControl(captured)
	if len(ports) != 1 {
		t.Fatalf("expected one port, got %d: %+v", len(ports), ports)
	}

	var b, bok = ports[0].Limit(driver.StormBroadcast)
	if !bok || !b.Enabled {
		t.Fatalf("broadcast limiter not read: %+v", ports[0].Limits)
	}
	if b.Rate != 5 {
		t.Errorf("broadcast rate = %v, want 5 — \"5.00\" is five percent", b.Rate)
	}
	if b.Unit != driver.StormRatePercent {
		t.Errorf("broadcast unit = %q, want percent: a bare \"level\" with no keyword "+
			"is a percentage of line rate on this platform", b.Unit)
	}

	// THE ONE THIS CAPTURE EXISTS FOR.
	var m, mok = ports[0].Limit(driver.StormMulticast)
	if !mok || !m.Enabled {
		t.Fatalf("multicast limiter not read: %+v", ports[0].Limits)
	}
	if m.Rate != 1500 {
		t.Errorf("multicast rate = %v, want 1500: the device printed \"1.5k\" for the "+
			"1000-and-a-half-thousand packets per second it was given, and reading "+
			"that as 1.5 would be a limiter a thousand times tighter than it carries",
			m.Rate)
	}
	if m.Unit != driver.StormRatePPS {
		t.Errorf("multicast unit = %q, want pps", m.Unit)
	}
}

// TestParseStormRateOnTheSuffixesHardwareActuallyPrinted pins the exact strings
// the 2960-X produced, as opposed to the ones the command reference implies.
func TestParseStormRateOnTheSuffixesHardwareActuallyPrinted(t *testing.T) {
	var cases = []struct {
		raw  string
		want float64
	}{
		{"5.00", 5},     // percent, decimals preserved by the device
		{"1k", 1000},    // pps 1000 came back suffixed
		{"2m", 2000000}, // bps 2000000 came back suffixed
		{"1.5k", 1500},  // and the fractional form, the one that needs a float parse
	}
	var c = cases[0]
	for _, c = range cases {
		var got, ok = parseStormRate(c.raw)
		if !ok {
			t.Errorf("%q was refused; the lab 2960-X printed it", c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("%q = %v, want %v", c.raw, got, c.want)
		}
	}
}

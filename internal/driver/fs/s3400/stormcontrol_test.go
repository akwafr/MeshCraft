package s3400

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Declared 2026-08-08, and the exercise that earned it is the reason this
// assertion is worth keeping rather than deleting: it did not merely confirm the
// forms, it FOUND TWO DEFECTS. The bare spelling does not exist on this firmware,
// and the removal command was refused — so a build that passed every unit test
// could not actually remove a limiter from a real switch.
//
// If this ever fails because someone flipped it back, the question is whether
// live_stormcontrol_test.go still passes (S3400_LIVE_WRITE=1), not whether the
// assertion is inconvenient.
func TestWritesExtraStormControlIsDeclaredOnLiveProof(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("storm_control") {
		t.Fatal("\"storm_control\" was proven live on 2026-08-08 — both expressible " +
			"classes written through ApplyStormControl, read back, a second identical " +
			"apply writing nothing, and a removal in the corrected form — and must stay " +
			"declared unless that evidence is withdrawn")
	}
	if !d.WritesExtra("stp") || !d.WritesExtra("port_admin") {
		t.Fatal("the capabilities earned on hardware were lost")
	}
	// Never a blanket yes: dhcp_snooping ships here and has not been exercised.
	if d.WritesExtra("dhcp_snooping") {
		t.Error("declares dhcp_snooping, which no S3400 session has ever configured")
	}
}

// The unknown-unicast refusal, and the reason it differs from the S3900's
// decision: this lineage spells the keyword "unicast", whose breadth is
// unestablished here, while the S3900 lineage spells it "unknown-unicast" in
// full.
func TestStormControlCapsRefuseUnknownUnicast(t *testing.T) {
	var caps = (&Driver{}).StormControlCaps()
	if caps.Expresses(driver.StormUnknownUnicast) {
		t.Fatal("unknown_unicast is declared on a lineage whose keyword is \"unicast\" " +
			"and whose breadth has never been established")
	}
	if !caps.Expresses(driver.StormBroadcast) || !caps.Expresses(driver.StormMulticast) {
		t.Error("the two classes this lineage names unambiguously are not declared")
	}
	if !caps.Accepts(driver.StormRatePPS) {
		t.Error("the threshold is a packet rate and it is not declared")
	}
	if caps.Accepts(driver.StormRatePercent) {
		t.Error("a percentage is claimed on a connector that refuses to convert into one")
	}
	// THE SAFETY ARGUMENT OF THIS FILE, which must be readable from outside it.
	var joined = strings.Join(caps.Why, " ")
	if !strings.Contains(joined, "never two candidate units") {
		t.Error("the capability answer does not carry the rule that keeps two candidate " +
			"SPELLINGS from becoming two candidate UNITS")
	}
}

// The two spellings, pinned. Both take the same argument in the same unit — a
// wrong spelling costs a round trip, a wrong unit configures a threshold nobody
// chose.
func TestStormSetCmdBothSpellings(t *testing.T) {
	if stormSetCmd(stormFormBare, "broadcast", 500) != "storm-control broadcast 500" {
		t.Errorf("bare form = %q", stormSetCmd(stormFormBare, "broadcast", 500))
	}
	if stormSetCmd(stormFormThreshold, "multicast", 500) !=
		"storm-control multicast threshold 500" {
		t.Errorf("threshold form = %q", stormSetCmd(stormFormThreshold, "multicast", 500))
	}
}

// The removal carries no NUMBER — zero is a limiter that drops the whole class,
// not the absence of one — but on this firmware it DOES carry the "threshold"
// keyword, which is the opposite of what this connector emitted until the form
// was captured.
//
// From the lab S3400 (OpCode 2.0.2J, 2026-08-08):
//
//	no storm-control multicast threshold      accepted
//	no storm-control multicast                "Unknown command"   ← was emitted
//	no storm-control multicast threshold 400  "Too many parameters"
func TestStormClearCmdMatchesTheSpellingTheDeviceSpeaks(t *testing.T) {
	var got = stormClearCmd(stormFormThreshold, "broadcast")
	if got != "no storm-control broadcast threshold" {
		t.Fatalf("threshold form = %q, want the captured \"no storm-control "+
			"broadcast threshold\"", got)
	}
	// The bare lineage keeps the bare removal: a device configured with the bare
	// SET form is the one case where the bare clear is the matching gesture.
	got = stormClearCmd(stormFormBare, "broadcast")
	if got != "no storm-control broadcast" {
		t.Fatalf("bare form = %q", got)
	}
	// Neither form carries a rate.
	if strings.Contains(stormClearCmd(stormFormThreshold, "multicast"), "0") {
		t.Error("the removal carries a number; zero is a limiter, not an absence")
	}
}

// The candidate ORDER matters on every port of every apply: the bare form is
// "Unknown command" on the captured firmware, so trying it first spends a
// refused round trip each time.
func TestStormFormsTryTheCapturedSpellingFirst(t *testing.T) {
	if len(stormForms) == 0 || stormForms[0] != stormFormThreshold {
		t.Fatalf("stormForms = %v, want the threshold form first — it is the one the "+
			"lab S3400 accepts", stormForms)
	}
}

// The full interface block, in the platform's own port spelling.
func TestStormPortCmdsWireForm(t *testing.T) {
	var cmds, err = stormPortCmds(cagesFrom1, stormFormBare, "GE3", []driver.StormLimit{
		{Class: driver.StormBroadcast, Enabled: true, Rate: 500, Unit: driver.StormRatePPS},
		{Class: driver.StormMulticast, Enabled: false},
	})
	if err != nil {
		t.Fatalf("a copper port needs no cage observation: %v", err)
	}
	var want = []string{
		"interface g0/3",
		"storm-control broadcast 500",
		"no storm-control multicast",
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

// A class this lineage does not express produces NO LINE, even if a caller got
// past the refusal. There is no near-equivalent keyword to fall back on, and
// falling back is what this file refuses to do.
func TestStormPortCmdsEmitNothingForUnknownUnicast(t *testing.T) {
	var cmds, err = stormPortCmds(cagesFrom1, stormFormBare, "GE1", []driver.StormLimit{
		{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 500, Unit: driver.StormRatePPS},
	})
	if err != nil {
		t.Fatalf("a copper port needs no cage observation: %v", err)
	}
	var line string
	for _, line = range cmds {
		if strings.Contains(line, "storm-control") {
			t.Fatalf("a line was emitted for a refused class: %q", line)
		}
	}
}

// The read recognises BOTH spellings and reports which one the device is already
// using — that is what lets the apply speak the device's own dialect instead of
// imposing this file's preference, exactly as parseDHCPSnooping returns its
// family.
func TestParseStormControlDetectsTheDeviceSpelling(t *testing.T) {
	var bare = `
interface g0/1
 storm-control broadcast 500
!
`
	var ports, unicast, form = parseStormControl(bare)
	if form != stormFormBare {
		t.Errorf("form = %q, want %q", form, stormFormBare)
	}
	if len(unicast) != 0 {
		t.Errorf("a unicast statement was reported where there is none: %v", unicast)
	}
	var bc, _ = ports[0].Limit(driver.StormBroadcast)
	if !bc.Enabled || bc.Rate != 500 || bc.Unit != driver.StormRatePPS {
		t.Errorf("broadcast = %+v", bc)
	}

	var kw = `
interface g0/1
 storm-control multicast threshold 250
!
`
	var _, _, form2 = parseStormControl(kw)
	if form2 != stormFormThreshold {
		t.Errorf("form = %q, want %q", form2, stormFormThreshold)
	}
}

// A "storm-control unicast" already on the device is REPORTED, never folded
// into the unknown-unicast class — on this lineage the keyword may well be
// throttling established flows, and saying so is the point.
func TestParseStormControlReportsUnicastSeparately(t *testing.T) {
	var ports, unicast, _ = parseStormControl(
		"interface g0/2\n storm-control unicast 500\n!\n")
	if len(unicast) != 1 {
		t.Fatalf("unicast statement not reported: %v", unicast)
	}
	var _, read = ports[0].Limit(driver.StormUnknownUnicast)
	if read {
		t.Fatal("\"storm-control unicast\" was mapped onto the unknown-unicast class")
	}
}

// A port block with no statement carries no limiter, and that IS an observation
// on a CLI that prints only non-default lines.
func TestParseStormControlCompletesReadBlocks(t *testing.T) {
	var ports, _, _ = parseStormControl("interface g0/1\n switchport pvid 20\n!\n")
	if len(ports) != 1 || len(ports[0].Limits) != 2 {
		t.Fatalf("ports = %+v", ports)
	}
	var i int
	for i = range ports[0].Limits {
		if ports[0].Limits[i].Enabled {
			t.Errorf("%s reads as limited in a block that carries no statement",
				ports[0].Limits[i].Class)
		}
	}
}

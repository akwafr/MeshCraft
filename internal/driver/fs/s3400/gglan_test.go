package s3400

// sw01-arena, ARENA, read 2026-10-24 — THE INCIDENT, PINNED.
//
// A production S3400-48T4SP was adopted with 48 of its 52 ports and refused
// every write. Two of the four missing sockets were the 10G uplinks carrying
// the site's traffic. No command form was wrong and no new grammar was needed:
// the device shipped a LATER BUILD of the same firmware version, and that
// build reworded one line of its banner and renumbered its SFP+ cages.
//
// Both captures are now fixtures (testdata/show_version_build130907.txt,
// testdata/show_interface_protocol_cages_49_52.txt) and BOTH conventions are
// held side by side here — the 2026-08-03 lab one and this one — so that the
// next rewording of a banner breaks a test instead of a site.

import (
	"context"
	"os"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Cause A — the banner lost a word. Build 95262 says "S3400-48T4SP Series
// Software, Version 2.0.2J"; Build 130907 says "S3400-48T4SP Software,
// Version 2.0.2J". While "Series" was mandatory the whole line failed to
// match, Model and Version came back empty, the dialect gate refused on its
// FIRST condition, and that single missing word refused every write to the
// device and left its firmware unrecorded in the database.
func TestParseShowVersionBannerWithoutSeries(t *testing.T) {
	var b, err = os.ReadFile("testdata/show_version_build130907.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out = string(b)
	var f = parseShowVersion(out)
	if f.Model != "S3400-48T4SP" {
		t.Errorf("model=%q — this banner names the model without \"Series\"", f.Model)
	}
	if f.Version != "2.0.2J" {
		t.Errorf("version=%q — same version train as Build 95262", f.Version)
	}
	if f.Build != "130907" {
		t.Errorf("build=%q — the BUILD is what differs here, not the version", f.Build)
	}
	if f.Serial != "CG2111195644N00004" {
		t.Errorf("serial=%q", f.Serial)
	}
	if f.MAC != "64:9d:99:29:f7:06" {
		t.Errorf("mac=%q", f.MAC)
	}
	if !looksLikeS3400CLI(out, f) {
		t.Error("this IS the surveyed CLI — refusing it refuses every write to the device")
	}
	// The fix widens the pattern; it must not move it. The older spelling
	// stays recognized.
	if !looksLikeS3400CLI(labShowVersion, parseShowVersion(labShowVersion)) {
		t.Error("the \"Series\" spelling of Build 95262 must still be recognized")
	}
}

// Cause B — the SFP+ cages are numbered 49..52 on this build. The NAME is
// unchanged ("TGigaEthernet0/N"); only N moves, because the cages continue
// the copper series instead of restarting their own. StandardPortName refused
// anything above 4, and refused it SILENTLY, which is how four sockets — two
// of them up and carrying traffic — never reached the model.
func TestStandardPortNameBothCageConventions(t *testing.T) {
	var d = &Driver{}
	var cases = map[string]string{
		// Build 95262: the cages restart their own series.
		"tg0/1":            "XG1",
		"TGigaEthernet0/4": "XG4",
		"TGi0/2":           "XG2",
		// Build 130907: the cages continue the copper series.
		"TGigaEthernet0/49": "XG1",
		"TGigaEthernet0/50": "XG2",
		"TGigaEthernet0/51": "XG3",
		"TGigaEthernet0/52": "XG4",
		"tg0/51":            "XG3",
		"TGi0/52":           "XG4",
	}
	var in, want string
	for in, want = range cases {
		var got, ok = d.StandardPortName(in)
		if !ok || got != want {
			t.Errorf("StandardPortName(%q) = %q,%v — want %q", in, got, ok, want)
		}
	}
	// Accepting a SECOND window must not open the whole range: a cage outside
	// BOTH conventions is not a socket this faceplate has, and naming one
	// would invent inventory out of a typo.
	var refused = []string{
		"tg0/5",             // no 5th cage under the restart convention
		"tg0/48",            // between the two windows, nothing lives there
		"TGigaEthernet0/53", // one past the continuation convention
		"tg0/0",             // sockets are 1-based
		"tg0/100",
	}
	var n string
	for _, n = range refused {
		var got, ok = d.StandardPortName(n)
		if ok {
			t.Errorf("StandardPortName(%q) = %q — must stay refused", n, got)
		}
	}
}

// The real risk of accepting tg0/49..52, which is why this is its own test.
// uniquePortNumber computes copperPorts + n, so if the PLATFORM's number ever
// leaked into it instead of the STANDARD one, XG3 would report unique port
// 48+51 = 99 and ifIndex 227 instead of 179. That failure is silent: it points
// SNMP supervision 48 sockets away from the port being measured and every
// sample still looks plausible.
func TestIfIndexUnaffectedByCageNumbering(t *testing.T) {
	// CAPTURED: "Ifindex is 177, unique port number is 49" on tg0/1
	// (testdata/show_interface_tg01.txt) and "Ifindex is 178, unique port
	// number is 50" on tg0/2.
	var wantUnique = map[string]int{"XG1": 49, "XG2": 50, "XG3": 51, "XG4": 52}
	var std string
	for std = range wantUnique {
		var num, ok = uniquePortNumber(std)
		if !ok || num != wantUnique[std] {
			t.Errorf("uniquePortNumber(%s) = %d,%v — want %d", std, num, ok, wantUnique[std])
		}
		var idx, iok = IfIndexOf(std)
		if !iok || idx != ifIndexBase+wantUnique[std] {
			t.Errorf("IfIndexOf(%s) = %d,%v — want %d", std, idx, iok,
				ifIndexBase+wantUnique[std])
		}
	}
	// The two spellings of the SAME socket must land on the SAME ifIndex:
	// tg0/3 (Build 95262) and tg0/51 (Build 130907) are one cage.
	var d = &Driver{}
	var oldName, ook = d.StandardPortName("tg0/3")
	var newName, nok = d.StandardPortName("tg0/51")
	if !ook || !nok {
		t.Fatalf("tg0/3 → %q,%v and tg0/51 → %q,%v — both name a socket",
			oldName, ook, newName, nok)
	}
	if oldName != newName {
		t.Errorf("tg0/3 → %q but tg0/51 → %q — one socket must have one standard name",
			oldName, newName)
	}
	// The arithmetic, end to end from the name the DEVICE uttered: a naive fix
	// that merely widens the accepted window and keeps the platform number
	// lands this on 128 + 48 + 51 = 227.
	var idx, iok = IfIndexOf(newName)
	if !iok || idx != 179 {
		t.Errorf("IfIndexOf(StandardPortName(\"tg0/51\")) = %d,%v — want 179, the third "+
			"cage. An ifIndex off by 48 measures a COPPER port and says nothing about "+
			"it: supervision reads plausible samples from the wrong socket", idx, iok)
	}
	// Copper is untouched by all of this.
	if idx, _ = IfIndexOf("GE1"); idx != 129 {
		t.Errorf("IfIndexOf(GE1) = %d — want 129", idx)
	}
	if idx, _ = IfIndexOf("GE48"); idx != 176 {
		t.Errorf("IfIndexOf(GE48) = %d — want 176", idx)
	}
}

// Cause B, end to end: the faceplate. ParseLinkStates feeds portGroups, which
// derives the cage count from the highest XG it can name — so a cage it could
// not name was a cage that did not exist. On sw01-arena that produced a
// 48-port model of a 52-port switch, with the live uplinks among the missing.
func TestLinkStatesSeeCagesNumberedFrom49(t *testing.T) {
	var b, err = os.ReadFile("testdata/show_interface_protocol_cages_49_52.txt")
	if err != nil {
		t.Fatal(err)
	}
	// The capture is abridged between GE1 and GE47 — the 46 idle copper ports
	// in between were elided. Every line kept is the device's own.
	var links = ParseLinkStates(string(b))
	// The two uplinks actually carrying traffic: tg0/51 and tg0/52.
	if links["XG3"] != driver.LinkUpState {
		t.Errorf("XG3=%q — tg0/51 is up/up and is a live uplink", links["XG3"])
	}
	if links["XG4"] != driver.LinkUpState {
		t.Errorf("XG4=%q — tg0/52 is up/up and is a live uplink", links["XG4"])
	}
	if links["XG1"] != driver.LinkDown || links["XG2"] != driver.LinkDown {
		t.Errorf("XG1=%q XG2=%q — empty cages, but they must EXIST",
			links["XG1"], links["XG2"])
	}
	// An SVI is not a faceplate port, and widening the cage window must not
	// have turned anything else into one.
	var _, isPort = links["VLAN29"]
	if isPort {
		t.Error("VLAN29 is an SVI, not a socket")
	}
	// What the facade counts: the highest cage namable must be sfpPorts.
	var maxXG int
	var name string
	for name = range links {
		var cage, kind, ok = splitStandard(name)
		if ok && kind == "XG" && cage > maxXG {
			maxXG = cage
		}
	}
	if maxXG != sfpPorts {
		t.Errorf("highest cage = XG%d — the facade would draw %d SFP+ cages on a switch "+
			"that has %d", maxXG, maxXG, sfpPorts)
	}
}

// Cause C — the chassis MAC was read and thrown away. "show version" hands
// it over at login and reVerMAC has always matched it, but identity() never
// assigned it: a device adopted through Identify arrived with no chassis MAC,
// while ReadIdentity, working from the very same facts, reported one. The base
// MAC is what LLDP correlation matches a neighbour's chassis-id against
// (deviceKnownMACs, internal/api/lldp_cli.go), so losing it costs topology,
// not cosmetics.
func TestIdentityCarriesChassisMAC(t *testing.T) {
	var b, err = os.ReadFile("testdata/show_interface_protocol_cages_49_52.txt")
	if err != nil {
		t.Fatal(err)
	}
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.Contains(cmd, "show interface") {
			return string(b), true
		}
		return "", true
	})
	c.facts = parseShowVersion(labShowVersion)

	var id = c.identity(context.Background())
	if id.MAC != "64:9d:99:22:7b:95" {
		t.Errorf("identity MAC = %q — \"show version\" carried it, and dropping it breaks "+
			"LLDP chassis-id correlation", id.MAC)
	}
	// What was already right must stay right.
	if id.Serial != "CG2003161906N0002" || id.Model != "S3400-48T4SP" ||
		id.Firmware != "2.0.2J" || id.Source != "ssh_cli" {
		t.Errorf("identity=%+v", id)
	}
	// And the facade it assembles sees all four cages even when the device
	// numbers them 49..52.
	if id.CopperPorts != copperPorts || id.SFPPorts != sfpPorts {
		t.Errorf("facade = %d copper + %d SFP+ — want %d + %d",
			id.CopperPorts, id.SFPPorts, copperPorts, sfpPorts)
	}
}

// The fourth defect, which was one of REPORTING. The dialect gate
// short-circuits on an unreadable version line BEFORE the vendor line is ever
// tested, yet the refusal announced that the output carried "neither the FS
// vendor line nor a Series Software line". On sw01-arena the vendor line was
// right there — "Fiberstore Co., Limited ... FS.COM" — so the message
// asserted one finding that was false and one that had never been made, and it
// sent an hour of diagnosis after the wrong fault.
func TestCLIVerdictReportsOnlyWhatItEvaluated(t *testing.T) {
	// A vendor line present, a version line this connector cannot read.
	var unreadable = "Fiberstore Co., Limited Internetwork Operating System Software\n" +
		"S3400-48T4SP Softwarre, Verzion 2.0.2J\n" +
		"Base ethernet MAC Address: 64:9d:99:29:f7:06\n"
	var ok, why = s3400CLIVerdict(unreadable, parseShowVersion(unreadable))
	if ok {
		t.Fatal("an unreadable version line must not be recognized as the surveyed CLI")
	}
	if !strings.Contains(why, "never consulted") {
		t.Errorf("refusal = %q — it must SAY the vendor line was not consulted, because "+
			"the gate returned before testing it", why)
	}
	if strings.Contains(why, "no FS/Fiberstore vendor line") {
		t.Errorf("refusal = %q — the vendor line IS present here and was never tested; "+
			"claiming it missing is the false lead that cost an hour", why)
	}

	// A readable version line and NO vendor line: here the vendor line WAS
	// evaluated, so naming it is the honest finding.
	var foreign = "Some Other Vendor OS\nS3400-48T4SP Software, Version 2.0.2J\n"
	ok, why = s3400CLIVerdict(foreign, parseShowVersion(foreign))
	if ok {
		t.Fatal("a non-FS output must not be taken for this CLI")
	}
	if !strings.Contains(why, "vendor line") || strings.Contains(why, "never consulted") {
		t.Errorf("refusal = %q — the vendor line was tested and is absent; that is what "+
			"the message must report", why)
	}

	// The S3900 next door: still refused, and now with a reason that does not
	// guess at two findings when it made one.
	var s3900 = "Operation Code Version 1.7.0\nHardware Version R0B\n"
	ok, why = s3400CLIVerdict(s3900, parseShowVersion(s3900))
	if ok || why == "" {
		t.Errorf("S3900 verdict = %v,%q — must be refused, with a reason", ok, why)
	}
}

// The write refusal an operator actually reads must carry that same finding
// rather than restate a generic pair of guesses.
func TestWriteRefusalQuotesTheFinding(t *testing.T) {
	var c = &Client{host: "203.0.113.11", known: false,
		unknownWhy: "\"show version\" carries no readable version line — the model " +
			"could not be established. Nothing further was evaluated: the vendor " +
			"line was never consulted"}
	var err = c.requireWritable()
	if err == nil {
		t.Fatal("an unrecognized CLI must refuse writes")
	}
	if !strings.Contains(err.Error(), "never consulted") {
		t.Errorf("refusal = %q — it must carry what was actually evaluated", err.Error())
	}
	// A session that never reached postLogin at all has no finding to quote
	// and must say so rather than invent one.
	var bare = &Client{host: "203.0.113.11"}
	err = bare.requireWritable()
	if err == nil || !strings.Contains(err.Error(), "never read") {
		t.Errorf("bare refusal = %v — want \"show version\" was never read", err)
	}
}

// --- THE WRITE HALF OF THE SAME INCIDENT (ADR-0018) ------------------------
//
// Accepting tg0/49..52 in READING is what let sw01-arena's four uplinks into
// the model. It also unblocked looksLikeS3400CLI, which unblocked every WRITE
// to that switch — while cliPortName still composed tg0/1..4. The clean refusal
// an operator could act on became a late failure on an interface name, and the
// message it produces blames this firmware's command FORMS, which is the false
// lead the previous fix spent an hour chasing in its other shape.
//
// The two conventions are held side by side here, as the fixtures of a session
// that has observed one of them.

// The two fielded conventions, as a session carries them once it has SEEN one.
var (
	cagesFrom1  = cageNumbering{base: 0, known: true}
	cagesFrom49 = cageNumbering{base: copperPorts, known: true}
)

// THE REGRESSION, in one assertion. A write aimed at XG3 on the chassis that
// numbers its cages 49..52 must address tg0/51. Before ADR-0018 every builder
// in this package emitted tg0/3 — a socket that switch does not have.
func TestWritesAddressTheCageTheDeviceActuallyHas(t *testing.T) {
	var cmds, err = portConfigCmds(cagesFrom49, "XG3", PortState{Mode: "access", PVID: 29})
	if err != nil {
		t.Fatalf("the numbering was observed, so XG3 can be named: %v", err)
	}
	if cmds[0] != "interface tg0/51" {
		t.Errorf("port config block opens with %q — want \"interface tg0/51\". On a build "+
			"that numbers its cages 49..52, tg0/3 is not a socket, and configBlock would "+
			"send the REST of this block from the enclosing context", cmds[0])
	}
	var admin []string
	admin, err = portAdminCmds(cagesFrom49, "XG3", false)
	if err != nil || admin[0] != "interface tg0/51" {
		t.Errorf("portAdminCmds = %q (%v) — want \"interface tg0/51\" first", admin, err)
	}
	var storm []string
	storm, err = stormPortCmds(cagesFrom49, stormFormThreshold, "XG3",
		[]driver.StormLimit{{Class: driver.StormBroadcast, Enabled: true, Rate: 500,
			Unit: driver.StormRatePPS}})
	if err != nil || storm[0] != "interface tg0/51" {
		t.Errorf("stormPortCmds = %q (%v) — want \"interface tg0/51\" first", storm, err)
	}

	// THE INVERSE CASE, which is the whole reason this is a session fact and not
	// a new constant: the lab unit still gets tg0/3 for XG3.
	cmds, err = portConfigCmds(cagesFrom1, "XG3", PortState{Mode: "access", PVID: 29})
	if err != nil || cmds[0] != "interface tg0/3" {
		t.Errorf("on Build 95262 the same intent must open with \"interface tg0/3\", "+
			"got %q (%v)", cmds, err)
	}
	// And the copper half is INDIFFERENT to all of it — g0/N under both
	// conventions, which is why the refusal below touches four sockets and not
	// fifty-two.
	var conv cageNumbering
	for _, conv = range []cageNumbering{cagesFrom1, cagesFrom49, {}} {
		var dev, derr = conv.cliPortName("GE45")
		if derr != nil || dev != "g0/45" {
			t.Errorf("GE45 under %+v = %q (%v) — copper needs no observation", conv, dev, derr)
		}
	}
}

// AN UNOBSERVED NUMBERING REFUSES TO NAME A CAGE, and the refusal is the point:
// it arrives before configBlock opens anything, and it says what would settle
// it. Guessing tg0/1 was the alternative, and the cost of that guess is not a
// bad error message — configBlock sends every line of its block, so a rejected
// "interface tg0/1" leaves "switchport mode access" and "shutdown" to run
// in the enclosing context.
func TestAnUnobservedCageNumberingRefusesInsteadOfGuessing(t *testing.T) {
	var unseen cageNumbering
	var _, err = unseen.cliPortName("XG1")
	if err == nil {
		t.Fatal("a session that has observed no cage must not name one — guessing tg0/1 is " +
			"exactly the regression ADR-0018 closes")
	}
	// The refusal is read by someone who cannot see the device: it must name
	// both conventions and the gesture that lifts it.
	var why = err.Error()
	var want string
	for _, want = range []string{"tg0/1", "tg0/49", "show interface", "XG1"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal = %q — it must mention %q", why, want)
		}
	}
	// Every builder refuses, and refuses EMPTY: a caller that ignored the error
	// must not find a usable block waiting for it.
	var cmds, perr = portConfigCmds(unseen, "XG1", PortState{Mode: "access", PVID: 1})
	if perr == nil || cmds != nil {
		t.Errorf("portConfigCmds = %q,%v — want no commands and a refusal", cmds, perr)
	}
	cmds, perr = portAdminCmds(unseen, "XG1", false)
	if perr == nil || cmds != nil {
		t.Errorf("portAdminCmds = %q,%v — want no commands and a refusal", cmds, perr)
	}
	cmds, perr = stormPortCmds(unseen, stormFormThreshold, "XG1", nil)
	if perr == nil || cmds != nil {
		t.Errorf("stormPortCmds = %q,%v — want no commands and a refusal", cmds, perr)
	}
}

// The observation itself, on the two captures. A sighting votes; a contradiction
// elects nobody, and no cage at all is not a convention either.
func TestObserveCageNumbering(t *testing.T) {
	var b, err = os.ReadFile("testdata/show_interface_protocol_cages_49_52.txt")
	if err != nil {
		t.Fatal(err)
	}
	var seen = observeCageNumbering(string(b))
	if !seen.known || seen.base != copperPorts {
		t.Errorf("Build 130907 capture observed as %+v — want base %d", seen, copperPorts)
	}
	// The lab unit, same read, the other convention.
	seen = observeCageNumbering(labIfProtocol)
	if !seen.known || seen.base != 0 {
		t.Errorf("Build 95262 capture observed as %+v — want base 0", seen)
	}
	// "show vlan" and the running configuration name the cages too, which is
	// what gives a write path more than one chance before it needs the answer.
	seen = observeCageNumbering(labShowVlan)
	if !seen.known || seen.base != 0 {
		t.Errorf("show vlan observed as %+v — want base 0", seen)
	}
	// Nothing to see: a copper-only output, and an SVI-only one.
	seen = observeCageNumbering("GigaEthernet0/1 is up, line protocol is up\n")
	if seen.known {
		t.Errorf("an output naming no cage was read as %+v — it establishes nothing", seen)
	}
	// A CONTRADICTION ELECTS NOBODY. An output that names both a tg0/2 and a
	// tg0/50 is not an output to deduce a numbering from, and picking the first
	// sighting would let one stray line decide where a write lands.
	seen = observeCageNumbering("TGigaEthernet0/2 is up, line protocol is up\n" +
		"TGigaEthernet0/50 is up, line protocol is up\n")
	if seen.known {
		t.Errorf("contradictory output observed as %+v — want unknown", seen)
	}
	// Out of both windows: not a cage, so not a vote.
	seen = observeCageNumbering("TGigaEthernet0/7 is up, line protocol is up\n")
	if seen.known {
		t.Errorf("tg0/7 is no cage under either convention, observed as %+v", seen)
	}
}

// THE SILKSCREEN IS A CLAIM AND IT LIED THE SAME WAY (ADR-0018, § 7). The XG
// group carried "silk: tg0/" unconditionally, and the frontend composes it as
// silk + the STANDARD index — so on this chassis the faceplate read "tg0/1"
// for a cage the switch calls tg0/49, and an operator copying that into the CLI
// gets "Unknown command". A bare prefix cannot express a 48-socket offset, so
// the claim is made only where it is true and the screen falls back to XG1
// elsewhere.
func TestFaceplateClaimsNoSilkscreenItDidNotObserve(t *testing.T) {
	var links, err = os.ReadFile("testdata/show_interface_protocol_cages_49_52.txt")
	if err != nil {
		t.Fatal(err)
	}
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show interface") {
			return string(links), true
		}
		return "", true
	})
	c.facts = parseShowVersion(labShowVersion)

	var groups []map[string]any
	groups, err = c.portGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var g map[string]any
	for _, g = range groups {
		if g["prefix"] != "XG" {
			// The copper silkscreen is untouched: "g0/N" is the same under
			// both conventions.
			if g["silk"] != "g0/" {
				t.Errorf("copper group lost its silkscreen: %v", g)
			}
			continue
		}
		var silk, claimed = g["silk"]
		if claimed {
			t.Errorf("the XG group claims silkscreen %q on a chassis that numbers its cages "+
				"49..52 — the screen would print tg0/1 for the cage the device calls "+
				"tg0/49", silk)
		}
		if g["count"] != sfpPorts || g["label"] != "SFP+" {
			t.Errorf("the cages must still be drawn, and still as SFP+: %v", g)
		}
	}

	// THE LAB UNIT KEEPS ITS SILKSCREEN. The claim is not abolished, it is
	// conditioned on having been observed.
	var lab, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show interface") {
			return labIfProtocol, true
		}
		return "", true
	})
	lab.facts = parseShowVersion(labShowVersion)
	groups, err = lab.portGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, g = range groups {
		if g["prefix"] == "XG" {
			found = true
			if g["silk"] != "tg0/" {
				t.Errorf("Build 95262 numbers its cages tg0/1..4 and that silkscreen is "+
					"true: %v", g)
			}
		}
	}
	if !found {
		t.Error("no XG group at all on the lab capture")
	}
}

// END TO END, through the device: ApplyPortAdmin on a scripted chassis numbered
// 49..52 must put "interface tg0/51" on the wire. This is the assertion that
// covers the real claim of ADR-0018 — that no write path reaches its port loop
// without its own pre-read having observed the numbering — because nothing here
// hands the session the convention; it has to read it.
func TestApplyPortAdminSendsThePlatformCageName(t *testing.T) {
	var links, err = os.ReadFile("testdata/show_interface_protocol_cages_49_52.txt")
	if err != nil {
		t.Fatal(err)
	}
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		switch {
		case cmd == "show running-config":
			// Enough configuration for ReadState to accept the read: the
			// management SVI, and the uplink's own stanza in the device's
			// numbering.
			return "!\nhostname sw01-arena\n!\ninterface TGigaEthernet0/51\n" +
				" switchport mode trunk\n!\ninterface VLAN29\n ip address 203.0.113.11 " +
				"255.255.255.0\n!\n", true
		case cmd == "show vlan":
			return "VLAN Status  Name       Ports\n" +
				"29   Static  MGMT       tg0/51, tg0/52\n", true
		case strings.HasPrefix(cmd, "show interface"):
			return string(links), true
		}
		return "", true
	})
	c.facts = parseShowVersion(labShowVersion)

	var res, aerr = c.ApplyPortAdmin(context.Background(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "XG1", Enabled: false}},
		Force: true,
	})
	if aerr != nil {
		t.Fatalf("ApplyPortAdmin: %v (skipped: %v)", aerr, res.Skipped)
	}
	var sent = strings.Join(dev.sent, "\n")
	if !strings.Contains(sent, "interface tg0/49") {
		t.Errorf("the wire carried:\n%s\n— \"interface tg0/49\" is the first cage on this "+
			"chassis. \"interface tg0/1\" is refused by the CLI, and configBlock would then "+
			"send \"shutdown\" from Switch_config# instead of from the port", sent)
	}
	if strings.Contains(sent, "interface tg0/1\n") {
		t.Errorf("the wire carried \"interface tg0/1\", a socket this build does not "+
			"have:\n%s", sent)
	}
}

// --- THE REGION HALF OF THE SAME SWITCH (ADR-0022) -------------------------
//
// sw01-arena was read again on 2026-10-25, read-only, and it was in RSTP
// carrying its own bridge MAC as a region name:
//
//	show spanning-tree mst region
//	  Name:     [ 649D9929F706 ]
//	  Revision: [ 0 ]
//	  Instance 0     1-4094
//
// while the eight Aruba on the same L2 carried mst-7f0d238df180 / revision 1 /
// instance 1 = 10-11,42,60,98-100. A region of ONE, and the site's apply had
// succeeded on it. That is the gap this test guards: not a wrong command, but a
// REFUSAL THAT WAS NEVER SPOKEN.
func TestMSTRegionIsRefusedOutLoud(t *testing.T) {
	// The real ARENA intent, built the only way the product builds one.
	var region = driver.SiteMSTRegion("mst-7f0d238df180", 1,
		[]int{1, 10, 11, 42, 60, 98, 99, 100})
	var line = mstRegionRefusal(region)
	if line == "" {
		t.Fatal("a configured region this connector cannot write must produce a refusal; " +
			"silence here is exactly how sw01-arena sat outside its site's tree with a " +
			"clean apply behind it")
	}
	// The NAME must be in it. A generic "MST not supported" leaves the operator
	// unable to tell which region the switch is missing from.
	if !strings.Contains(line, "mst-7f0d238df180") {
		t.Errorf("the refusal must name the region it is refusing: %q", line)
	}
	// The CONSEQUENCE must be in it, because the symptom is that there is no
	// symptom — the switch forwards and looks healthy.
	if !strings.Contains(line, "ITS OWN REGION") {
		t.Errorf("the refusal must say what the device becomes: %q", line)
	}
	// And the REASON must be the measured one, not "unsupported". The mode change
	// drops the session; that is a gesture this connector lacks, not a command.
	if !strings.Contains(line, "closes the SSH session") {
		t.Errorf("the refusal must carry why, so nobody 'fixes' it by adding a "+
			"command form: %q", line)
	}
}

// No region, no refusal: a site that runs RSTP has nothing to be told about.
func TestNoRegionProducesNoNoise(t *testing.T) {
	if mstRegionRefusal(driver.MSTRegion{}) != "" {
		t.Error("an unconfigured region must produce no Skipped line")
	}
	// A region with a revision and a VLAN table but NO NAME is not a region
	// (driver.MSTRegion.Configured): the default name on these platforms is
	// derived from the bridge MAC, so an unnamed one would be a region of one
	// anyway and there is no intent to report as missed.
	if mstRegionRefusal(driver.MSTRegion{
		Revision:      1,
		InstanceVLANs: map[int][]int{1: {42}},
	}) != "" {
		t.Error("a nameless region is not a region and must not be reported as refused")
	}
}

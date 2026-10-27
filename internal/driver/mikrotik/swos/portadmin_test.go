package swos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// linkCapture loads the REAL /link.b of the reference CSS326-24G-2S+ (SwOS 2.17).
// Every one of its twenty-six ports is enabled (`en:0x03ffffff`) and only index 25
// — the SFP2 cage — carries a link (`lnk:0x02000000`).
func linkCapture(t *testing.T) (*Page, Facade) {
	t.Helper()
	var page, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b refused: %v", err)
	}
	var ports, sfp, first, gerr = geometryOf(page)
	if gerr != nil {
		t.Fatalf("geometry: %v", gerr)
	}
	return page, NewFacade(ports, sfp, first, "GE")
}

func sysCapture(t *testing.T) *Page {
	t.Helper()
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	return page
}

// THE GUARDRAIL, on the real capture: SFP2 is the only linked port, and /sys.b says
// administration arrives from VLAN 99 (`avln:0x0063`). A port carrying the link AND
// the management VLAN must be spared; the twenty-five dark ports must move.
func TestPlanPortAdminSparesTheLinkedManagementPort(t *testing.T) {
	var link, facade = linkCapture(t)
	var current = State{
		MgmtVLAN: 99,
		Ports: map[string]PortState{
			"SFP2":  {Mode: "trunk", PVID: 1, Tagged: []int{99}},
			"Port3": {Mode: "access", PVID: 100},
		},
	}
	var decided, err = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sysCapture(t), Facade: facade, Current: current,
		Intents: []driver.PortAdminIntent{
			{Port: "SFP2", Enabled: false},
			{Port: "Port3", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("PlanPortAdmin: %v", err)
	}
	if len(decided.Changes) != 1 || decided.Changes[0].Port != "Port3" {
		t.Fatalf("only the dark port may move: %+v", decided.Changes)
	}
	if decided.Changes[0].Index != 2 {
		t.Errorf("Port3 is index 2 (0-based): got %d", decided.Changes[0].Index)
	}
	if len(decided.Skipped) != 1 || !strings.Contains(decided.Skipped[0], "SFP2") {
		t.Fatalf("the spared port must be named, with the reason: %+v", decided.Skipped)
	}
	if !strings.Contains(decided.Skipped[0], "99") {
		t.Errorf("the refusal must name the management VLAN: %q", decided.Skipped[0])
	}
	if !strings.Contains(decided.Skipped[0], "reset button") {
		t.Errorf("the refusal must say what the way back costs on this platform: %q",
			decided.Skipped[0])
	}
}

// Force lifts the refusal — and nothing else does.
func TestPlanPortAdminForceLiftsTheRefusal(t *testing.T) {
	var link, facade = linkCapture(t)
	var current = State{MgmtVLAN: 99, Ports: map[string]PortState{
		"SFP2": {Mode: "trunk", PVID: 1, Tagged: []int{99}}}}
	var decided, err = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sysCapture(t), Facade: facade, Current: current, Force: true,
		Intents: []driver.PortAdminIntent{{Port: "SFP2", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("PlanPortAdmin: %v", err)
	}
	if len(decided.Changes) != 1 || decided.Changes[0].Index != 25 {
		t.Fatalf("Force must let the write through on index 25: %+v", decided.Changes)
	}
}

// ZERO IS LEGITIMATE HERE, and it is the opposite of Instant On's zero: it means
// "administration accepted from any VLAN", the factory state. The write must stay
// possible — a brand-new CSS has to be adoptable — but protection falls back to the
// LINK ALONE, so the linked port is spared whatever its membership says.
func TestPlanPortAdminMgmtZeroFallsBackToLinkAlone(t *testing.T) {
	var link, facade = linkCapture(t)
	// No `allp` narrowing and no management VLAN: the factory shape.
	var current = State{MgmtVLAN: 0, Ports: map[string]PortState{
		"SFP2":  {Mode: "access", PVID: 1},
		"Port7": {Mode: "access", PVID: 1},
	}}
	var decided, err = PlanPortAdmin(PortAdminInput{
		Link: link, Facade: facade, Current: current,
		Intents: []driver.PortAdminIntent{
			{Port: "SFP2", Enabled: false},  // linked
			{Port: "Port7", Enabled: false}, // dark
		},
	})
	if err != nil {
		t.Fatalf("PlanPortAdmin: %v", err)
	}
	if len(decided.Changes) != 1 || decided.Changes[0].Port != "Port7" {
		t.Fatalf("the dark port must still be writable — otherwise no factory device could "+
			"ever be configured: %+v", decided.Changes)
	}
	if len(decided.Skipped) != 1 || !strings.Contains(decided.Skipped[0], "ANY VLAN") {
		t.Fatalf("the linked port must be spared, saying that the fallback is the link: %+v",
			decided.Skipped)
	}
	if len(decided.Notes) == 0 || !strings.Contains(decided.Notes[0], "NOT ignorance") {
		t.Errorf("the note must distinguish this zero from Instant On's: %+v", decided.Notes)
	}
}

// Re-ENABLING is never gated: it cuts nothing, and it is the gesture an operator
// needs precisely when something has already gone wrong.
func TestPlanPortAdminEnablingIsNeverSpared(t *testing.T) {
	var link, facade = linkCapture(t)
	// A shut port: bit 6 of `en` cleared on the captured page.
	if !link.SetBit(FieldEnabled, 6, false) {
		t.Fatal("the captured mask must be modifiable")
	}
	var decided, err = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sysCapture(t), Facade: facade,
		Current: State{MgmtVLAN: 99, Ports: map[string]PortState{"Port7": {PVID: 99}}},
		Intents: []driver.PortAdminIntent{{Port: "Port7", Enabled: true}},
	})
	if err != nil {
		t.Fatalf("PlanPortAdmin: %v", err)
	}
	if len(decided.Changes) != 1 || !decided.Changes[0].Enabled {
		t.Fatalf("bringing a port back up must never be refused: %+v (skipped %+v)",
			decided.Changes, decided.Skipped)
	}
}

// A port already in the wanted state produces NO change: on a platform that persists
// immediately and has no way back, a POST that changes nothing is a risk taken for
// nothing — and it would be taken on every reconciliation pass.
func TestPlanPortAdminSkipsPortsAlreadyThere(t *testing.T) {
	var link, facade = linkCapture(t)
	var decided, err = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sysCapture(t), Facade: facade, Current: State{MgmtVLAN: 99},
		Intents: []driver.PortAdminIntent{{Port: "Port7", Enabled: true}},
	})
	if err != nil {
		t.Fatalf("PlanPortAdmin: %v", err)
	}
	if len(decided.Changes) != 0 {
		t.Errorf("every port of the capture is already enabled: %+v", decided.Changes)
	}
}

// The legacy numbered family is accepted on ingress, and the DEVICE's own name is
// what comes back out: the operator reads the result against the device's UI.
func TestPlanPortAdminAcceptsLegacyNamesAndAnswersWithDeviceNames(t *testing.T) {
	var link, facade = linkCapture(t)
	var decided, err = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sysCapture(t), Facade: facade, Current: State{MgmtVLAN: 99},
		Intents: []driver.PortAdminIntent{{Port: "GE7", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("PlanPortAdmin: %v", err)
	}
	if len(decided.Changes) != 1 || decided.Changes[0].Port != "Port7" ||
		decided.Changes[0].Index != 6 {
		t.Fatalf("GE7 must resolve to the device's own Port7 at index 6: %+v", decided.Changes)
	}
}

// A page without its masks makes the DECISION impossible, not just one port: that is
// the one case this planner returns an error for.
func TestPlanPortAdminRefusesAPageWithoutMasks(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldPortCount, 26)
	var _, err = PlanPortAdmin(PortAdminInput{
		Link: page, Facade: NewFacade(26, 2, 24, "GE"),
		Intents: []driver.PortAdminIntent{{Port: "Port7", Enabled: false}},
	})
	if err == nil {
		t.Fatal("without `en` the current administrative state is unknown: the write must be refused")
	}
	if !strings.Contains(err.Error(), FieldEnabled) {
		t.Errorf("the refusal must name the missing mask: %v", err)
	}
}

// `allp` — "Allow From Ports" — only ever ADDS protection. On the real capture it
// is all-ones (0x03ffffff) and must say nothing; narrowed, a linked port inside it is
// a door to the device whatever its VLAN membership.
func TestAllowsOnlySomePortsIgnoresTheDefaultMask(t *testing.T) {
	if allowsOnlySomePorts(sysCapture(t), 26) {
		t.Error("the captured mask allows all 26 ports: treating it as a narrowing would spare " +
			"every linked port on every default device")
	}
	if allowsOnlySomePorts(nil, 26) {
		t.Error("an absent page narrows nothing")
	}
}

// The narrowed case, CONSTRUCTED (the reference device is not hardened): only port 1
// admits administration, and it must be spared even though it carries VLAN 100 and
// the management VLAN is 99.
func TestPlanPortAdminSparesAPortNamedByAllowFromPorts(t *testing.T) {
	var link, facade = linkCapture(t)
	// Give port index 0 a link, and narrow `allp` to it alone.
	if !link.SetBit(FieldLink, 0, true) {
		t.Fatal("mask not modifiable")
	}
	var sys = sysCapture(t)
	var i int
	for i = 0; i < 26; i++ {
		if !sys.SetBit(FieldAllowPorts, i, i == 0) {
			t.Fatalf("allp bit %d not modifiable", i)
		}
	}
	var decided, err = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sys, Facade: facade,
		Current: State{MgmtVLAN: 99, Ports: map[string]PortState{"Port1": {PVID: 100}}},
		Intents: []driver.PortAdminIntent{{Port: "Port1", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("PlanPortAdmin: %v", err)
	}
	if len(decided.Changes) != 0 {
		t.Fatalf("a linked port that \"Allow From Ports\" names must be spared: %+v",
			decided.Changes)
	}
	if len(decided.Skipped) != 1 || !strings.Contains(decided.Skipped[0], FieldAllowPorts) {
		t.Fatalf("the refusal must name the mask it fired on: %+v", decided.Skipped)
	}
}

// THE REFUSAL THAT PROTECTS TWENTY-SIX PORT NAMES: the page must carry every field
// of the writable set before anything is posted. The real /link.b does; a page
// missing `nm` must be refused rather than posted, or the device zeroes the names.
func TestLinkPageCarriesEveryWritableFieldItMustEchoBack(t *testing.T) {
	var link, _ = linkCapture(t)
	var fields, known = WritableFields(PageLink)
	if !known {
		t.Fatal("no writable set declared for /link.b")
	}
	var _, missing = link.Only(fields)
	if len(missing) > 0 {
		t.Fatalf("the real capture must carry every writable field, missing %v", missing)
	}

	// Now the shape that must be refused, CONSTRUCTED from the captured page by
	// keeping every writable field EXCEPT `nm`: this is what a firmware serving a
	// shorter page looks like to this connector.
	var without []string
	var f string
	for _, f = range fields {
		if f != FieldPortName {
			without = append(without, f)
		}
	}
	var trimmed, _ = link.Only(without)
	var _, stillMissing = trimmed.Only(fields)
	var sawNames bool
	for _, f = range stillMissing {
		if f == FieldPortName {
			sawNames = true
		}
	}
	if !sawNames {
		t.Errorf("a page without %q must report it missing — posting without it would rename "+
			"twenty-six ports to nothing while shutting one", FieldPortName)
	}
}

// WritesExtra DOES declare port_admin on this platform, since 2026-08-06 (cf.
// capabilities.go, which keeps the warning next to the declaration rather than
// replacing it with one).
//
// This test used to assert the opposite, and it was there so that declaring would
// be a deliberate act with a reason rather than a line someone added while wiring
// the UI. It was: the operator asked for the gesture fleet-wide and accepted this
// platform's exposure — SwOS persists immediately, no reboot undoes a shut port,
// and the way back is a reset button on site that also loses the configuration.
//
// Inverted rather than deleted, and kept pointing at the same two things: the
// declaration, and the tests around it that are the reason it is bearable. The
// guardrail cases live in TestPlanPortAdmin* above, and
// TestLinkPageCarriesEveryWritableFieldItMustEchoBack is the one that protects the
// twenty-six port names. If any of those is ever weakened, THIS declaration is
// what makes the weakening reachable from a screen.
func TestWritableExtrasDeclaresPortAdmin(t *testing.T) {
	if !WritesExtra("port_admin") {
		t.Error("ApplyPortAdmin exists and the gesture was asked for fleet-wide: " +
			"api/portadmin_apply.go gates on this name and would refuse it")
	}
	// poe and port_security stay false here: no apply code exists for either.
	// dhcp_snooping left this list on 2026-08-09, when the `dtrp` write was
	// exercised on the lab CSS326 — see capabilities.go for the evidence.
	var name string
	for _, name = range []string{"poe", "port_security"} {
		if WritesExtra(name) {
			t.Errorf("WritesExtra(%q) = true without apply code", name)
		}
	}
}

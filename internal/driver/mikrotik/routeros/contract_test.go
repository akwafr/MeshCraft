package routeros

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Tests for the STITCHING with the access contract.
//
// They do not replay captures: they lock down REFUSALS, and that is where their value
// lies. Everything this file verifies happens BEFORE a single byte leaves for the device
// — exactly the property one wants to guarantee, and what makes them runnable without
// hardware or a simulation server. A refusal that would only happen after a costly request
// would spend a session across a fleet, and a message that spoke of transport where the
// fact is a capability.
//
// The most important assertion of all this is not in a test function: it is the
// `var _ driver.Connector = (*Driver)(nil)` block in contract.go, which makes
// COMPILATION fail the day the contract moves. A test cannot do better.

// TestNoCredentialToTryIsAnExplicitRefusal
//
// RouterOS REST answers 401 on ALL its resources. Authentication without a candidate can
// therefore try nothing, and saying so beats a « failure »: the gesture that fixes it is
// to provide credentials, and the message must name that.
//
// No request is emitted, which is the meaning of `nil` here: if this code touched the
// network, it would panic here.
func TestNoCredentialToTryIsAnExplicitRefusal(t *testing.T) {
	var admin, err = (&Driver{}).Authenticate(nil, driver.Target{Host: "10.99.0.54"}, nil)
	if err == nil {
		t.Fatal("authenticating without any candidate must be refused: RouterOS REST " +
			"serves nothing without credentials, there is nothing to try")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "REST") {
		t.Errorf("message must explain WHY this is a dead end on this "+
			"platform: %q", err)
	}
	// Access is returned DESPITE the error, for its transcript: that is the only way to
	// understand a refusal without returning on site, and the API layer displays it on all
	// its failure paths.
	if admin == nil {
		t.Fatal("access must be returned even on failure: its transcript is the only " +
			"trace of what was attempted")
	}
	if len(admin.Transcript()) == 0 {
		t.Error("failure transcript must not be empty")
	}
	// And it is harmless: no account, no candidate.
	if admin.Account() != "" {
		t.Errorf("Account = %q on access that opened nothing: it must stay EMPTY, "+
			"or the caller would believe it holds a proven account", admin.Account())
	}
	if admin.Accepted() != (Candidate{}) {
		t.Errorf("Accepted = %+v on access that opened nothing: it must stay empty",
			admin.Accepted())
	}
}

// TestIdentifyRequiresCandidates — the NATURE difference with the other two shipped
// ranges, locked down.
//
// An Instant On carries its sysDescr and serial in hidden fields of its login page; a
// SwOS announces its model in the realm of its Digest challenge. RouterOS says NOTHING:
// the REST API answers 401 everywhere, model included. This test prevents someone from
// « harmonizing » the three connectors by making this one appear to identify without
// credentials — it would then return empty without error, and discovery would rank a
// device without model believing it had been read.
func TestIdentifyRequiresCandidates(t *testing.T) {
	var id, err = (&Driver{}).Identify(nil, driver.Target{Host: "10.99.0.54"}, nil)
	if err == nil {
		t.Fatal("identifying a RouterOS without credentials must be refused: the REST API " +
			"answers 401 on all its resources, including the one that carries the model")
	}
	if id.Model != "" || id.Serial != "" {
		t.Errorf("no identity can be established without credentials: %+v", id)
	}
	// The message must say what remains possible WITHOUT credentials, or the operator
	// looking at a less informative discovery line than the others will not know whether
	// to wait for something.
	var expected string
	for _, expected = range []string{"401", "RouterOS", "SNMP"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("message must mention %q: %q", expected, err)
		}
	}
}

// TestV3IsRefusedNotApproximated — the refusal that protects the database.
//
// v3 machinery EXISTS on this firmware (observed: `/snmp` carries an `engine-id`, which
// only makes sense for v3). This connector nevertheless does not declare it, and must
// therefore REFUSE a v3 identity instead of approximating it to the « closest » v2c.
//
// What is at stake is not code cleanliness: accepting silently would produce on the device
// an identity meshcraft believes placed, therefore supervision the fleet believes
// authenticated. « An identity that exists in the database and nowhere else is worse than
// a missing identity. »
func TestV3IsRefusedNotApproximated(t *testing.T) {
	var c = &Client{}
	var _, err = c.ApplySNMP(nil, SNMPIdentity{
		User: "mcmon", AuthAlg: "SHA-256", PrivAlg: "AES-128",
		AuthSecret: "one", PrivSecret: "two", Sources: []string{"10.99.0.0/24"},
	})
	if err == nil {
		t.Fatal("an SNMPv3 identity must be REFUSED: this connector declares only " +
			"v1/v2c, and placing v3 would create an identity that exists in the " +
			"meshcraft database and nowhere else")
	}
	// Recognizable by the caller in ONE way only, whatever the reason for refusal: that
	// is the whole point of driver.Unsupported.
	if !errors.Is(err, driver.ErrNotImplemented) {
		t.Errorf("refusal must attach to driver.ErrNotImplemented so the "+
			"caller needs only one test: %v", err)
	}
	var refusal *driver.Unsupported
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal must be a driver.Unsupported, separating gesture from "+
			"reason for the screen: %v", err)
	}
	if refusal.Connector != "mikrotik-routeros" {
		t.Errorf("refusal must name the connector (%q): without that, the screen says "+
			"\u00ab unsupported \u00bb without saying by what", refusal.Connector)
	}
	if refusal.Because == "" {
		t.Error("`Because` must not be empty: it answers \u00ab is there anything to wait for? \u00bb")
	}
	// And it must say there IS something to wait for, which is the case here: this is
	// the only declaration of this connector that a successful write campaign must change.
	if !strings.Contains(refusal.Because, "write campaign") {
		t.Errorf("reason must say this refusal is REVISABLE: %q", refusal.Because)
	}
}

// TestCommunityWithoutSourceBoundIsRefused — the refusal that gave birth to the
// contract's `Sources` field.
//
// RouterOS carries an `addresses` field on each community, and observation shows `::/0`
// on the reference device: ALL sources, including outside the management VLAN. That is
// not a platform constraint, it is a SETTING — and leaving it open is a choice an operator
// must make knowingly.
//
// Empty therefore means « the caller said nothing », and especially not « all ». The
// message must name THE MISSING GESTURE, or the operator reads a failure without knowing
// what to fix.
func TestCommunityWithoutSourceBoundIsRefused(t *testing.T) {
	var c = &Client{}
	var _, err = c.ApplySNMP(nil, SNMPIdentity{Community: "mc-ro"})
	if err == nil {
		t.Fatal("placing a community without a source bound must be refused: " +
			"\u00ab leaving SNMP open to all addresses \u00bb must be requested " +
			"explicitly, not invented as a default")
	}
	// The message must be ACTIONABLE: name the device field, and give the value to pass
	// to open voluntarily.
	var expected string
	for _, expected = range []string{"addresses", "explicitly", "0.0.0.0/0"} {
		if !strings.Contains(err.Error(), expected) {
			t.Errorf("message must contain %q to say WHAT TO DO: %q", expected, err)
		}
	}
	// This is NOT an absent capability: this connector knows perfectly well how to place
	// a community. Confusing it with a capability refusal would display « this platform
	// cannot do it » where a parameter is missing.
	if errors.Is(err, driver.ErrNotImplemented) {
		t.Error("a missing source bound is a missing parameter, not an absent " +
			"capability: the caller must not read it as \u00ab unsupported \u00bb")
	}
}

// TestTrapPortOtherThan162IsRefused
//
// RouterOS's `trap-target` field carries ADDRESSES only: there is nowhere to write a port
// number. Accepting it while ignoring it would make one wait for traps on a silent port —
// supervision that stops without a word, whose only witness would be a DEBUG counter on
// the proxy.
func TestTrapPortOtherThan162IsRefused(t *testing.T) {
	var c = &Client{}
	var want = TrapDestination{Address: "10.99.0.2", Community: "mc-ro", Port: 1162}
	var out, err = c.ApplyTrapReceiver(nil, want)
	if err == nil {
		t.Fatal("a trap port other than 0 or 162 must be REFUSED, never ignored: " +
			"accepting it would make one wait for traps on a silent port")
	}
	if !strings.Contains(err.Error(), "162") {
		t.Errorf("message must name the only reachable port: %q", err)
	}
	// The result announces the REAL port, not the one requested: that prevents a screen
	// from showing 1162 next to a refusal.
	if out.Port != 162 {
		t.Errorf("TrapResult.Port = %d, expected 162 — result must carry the port "+
			"the platform can target", out.Port)
	}

	// Every non-standard port is refused, including those that « look like » SNMP: the
	// platform has no field to write them, and a plausible number is no more reachable
	// than any other.
	var port int
	for _, port = range []int{161, 1162, 10162, 65535} {
		want.Port = port
		_, err = c.ApplyTrapReceiver(nil, want)
		if err == nil {
			t.Errorf("port %d must be refused: `trap-target` carries addresses only",
				port)
		}
	}

	// Only 0 and 162 pass this guardrail. Verified on the constant rather than by
	// calling the method: the rest of the path joins the device, and this file insists
	// on emitting nothing.
	if trapPort != 162 {
		t.Errorf("trapPort = %d: this is the only port RouterOS `trap-target` can "+
			"target, and the contract says 0 means the standard port",
			trapPort)
	}
}

// TestReceiverWithoutCommunityIsRefused — the constraint comes from the COLLECTOR.
//
// The proxy accepts a trap only if the community it carries equals the one with which it
// POLLS the device. A distinct trap community, even dedicated, would reject 100 % of fleet
// traps — silently. The device does not require it: `trap-community` is free text it does
// not match against any community list entry, so NOTHING would signal the error.
func TestReceiverWithoutCommunityIsRefused(t *testing.T) {
	var c = &Client{}
	var _, err = c.ApplyTrapReceiver(nil, TrapDestination{Address: "10.99.0.2"})
	if err == nil {
		t.Fatal("declaring a trap receiver without community must be refused: " +
			"the collector would reject all traps, and the device would say nothing")
	}
	if !strings.Contains(err.Error(), "collector") {
		t.Errorf("message must say the constraint comes from the COLLECTOR not the "+
			"device, or one will look for it in MikroTik documentation: %q", err)
	}
	// And the address is mandatory, for a different reason: it is REQUESTED and not
	// deduced — only the proxy core knows by which address it reaches this device.
	_, err = c.ApplyTrapReceiver(nil, TrapDestination{Community: "mc-ro"})
	if err == nil {
		t.Fatal("declaring a receiver without address must be refused")
	}
	// A v3 receiver is an absent CAPABILITY, not a missing parameter: distinguishing them
	// is what lets the screen not offer a button that leads nowhere.
	_, err = c.ApplyTrapReceiver(nil, TrapDestination{
		Address: "10.99.0.2", User: "mcmon"})
	if !errors.Is(err, driver.ErrNotImplemented) {
		t.Errorf("a v3 receiver must be a CAPABILITY refusal, recognizable by "+
			"errors.Is(err, driver.ErrNotImplemented): %v", err)
	}
}

// TestNotesSayInertConfiguration — THE reason State.Notes exists, and the point in this
// file that protects the operator rather than the code.
//
// The contract view carries neither `vlan-filtering` nor untagged memberships. Yet the
// reference device cumulates both traps that makes invisible:
//
//   - its 24 copper ports have `pvid=100` while VLAN 100 has an EMPTY `untagged` list.
//     They tag incoming traffic into a VLAN from which nothing exits;
//   - and a bridge whose filtering were off would let everything pass everywhere, while
//     showing exactly the same VLANs.
//
// Without these notes, the screen would present a PRESENT AND INERT configuration as
// segmentation in place, and an operator would conclude « this switch is compliant » on
// traffic that is not segmented at all.
func TestNotesSayInertConfiguration(t *testing.T) {
	var dev = stateFromFixtures(t)
	var st = dev.Standard()

	// 1. PVID without membership, on the REAL device state.
	var notes = strings.Join(st.Notes, "\n")
	if !strings.Contains(notes, "PVID WITHOUT MEMBERSHIP") {
		t.Errorf("notes must flag ports whose PVID matches no membership: that is the "+
			"case for the 24 copper ports of the reference device, accepted by the device "+
			"and SILENT. Notes obtained:\n%s", notes)
	}
	// Ports must be NAMED, in facade order: a note saying « 24 ports » without naming
	// them would help nobody, and alphabetical sort would place ether10 before ether2,
	// making it unreadable therefore useless.
	if !strings.Contains(notes, "ether1, ether2, ether3") {
		t.Errorf("affected ports must be named in facade order "+
			"(ether1, ether2, ether3…) not alphabetically sorted:\n%s", notes)
	}
	// The uplink port is an untagged member of its native VLAN: it must NOT appear in
	// THIS note, or the note would cry wolf on a correct configuration.
	//
	// The check targets the orphan PVID note ALONE, not the whole block: other notes
	// legitimately cite all bridge ports, the inactive ingress filtering note first.
	// Searching the name in the whole set would forbid any future note from naming the uplink.
	if strings.Contains(pvidNote(st.Notes), "sfp-sfpplus2") {
		t.Errorf("sfp-sfpplus2 is an untagged member of VLAN 1, its PVID: it must "+
			"not be flagged as an anomaly:\n%s", pvidNote(st.Notes))
	}

	// 2. ACTIVE filtering must trigger nothing: the reference device has it.
	if strings.Contains(notes, "VLAN FILTERING INACTIVE") {
		t.Errorf("filtering is ACTIVE on the reference device (observed "+
			"`vlan-filtering: true`): announcing it inactive would send one hunting a fault "+
			"that does not exist:\n%s", notes)
	}
	// Administration address / interface live on the Management network card, not in
	// State.Notes (those flooded last_apply after every port apply).
	if strings.Contains(notes, "Administration observed") {
		t.Errorf("admin reachability must not be restated in State.Notes:\n%s", notes)
	}
	if strings.Contains(notes, "not yet migrated") || strings.Contains(notes, "ITS OWN") {
		t.Errorf("migration lecture belongs on ApplyPorts when plan VLAN ≠ device VLAN:\n%s", notes)
	}

	// 3. Filtering off, on the same state: that is where the note becomes vital.
	dev.VLANFiltering = false
	notes = strings.Join(dev.Standard().Notes, "\n")
	if !strings.Contains(notes, "VLAN FILTERING INACTIVE") {
		t.Errorf("a bridge whose `vlan-filtering` is inactive lets EVERYTHING pass "+
			"EVERYWHERE: rendered VLANs exist but have no effect, and staying silent "+
			"would present segmentation that does not exist:\n%s", notes)
	}
	// The note must say apply enables filtering last (after membership), or the
	// operator would think inert config is permanent.
	if !strings.Contains(notes, "enables") || !strings.Contains(notes, "vlan-filtering") {
		t.Errorf("note must say apply enables vlan-filtering after membership:\n%s", notes)
	}
}

// TestContractViewLosesNeitherPortNorVLAN: conversion must be faithful on what it carries.
//
// What is lost is said by the notes (previous test); what is KEPT must not degrade silently.
// A port disappearing from the contract view would never be reconciled, and a VLAN counted
// twice would read as drift.
func TestContractViewLosesNeitherPortNorVLAN(t *testing.T) {
	var dev = stateFromFixtures(t)
	var st = dev.Standard()

	if len(st.Ports) != len(dev.Ports) {
		t.Errorf("%d ports in contract view, %d in rich read: a lost port "+
			"would never be reconciled", len(st.Ports), len(dev.Ports))
	}
	// The 3 VLANs of the reference device: 1 (dynamic), 99, 100. DEDUPLICATED by
	// identifier — rendering one twice would count two VLANs where there is one, which
	// drift calculation would read as a gap.
	var ids []int
	var v VLAN
	for _, v = range st.VLANs {
		ids = append(ids, v.ID)
	}
	if !reflect.DeepEqual(ids, []int{1, 99, 100}) {
		t.Errorf("contract view VLANs = %v, expected [1 99 100] sorted without "+
			"duplicate", ids)
	}
	// PVID and tagged list survive conversion.
	if st.Ports["ether1"].PVID != 100 {
		t.Errorf("ether1 PVID = %d in contract view, expected 100",
			st.Ports["ether1"].PVID)
	}
	if !reflect.DeepEqual(st.Ports["sfp-sfpplus2"].Tagged, []int{99, 100}) {
		t.Errorf("sfp-sfpplus2 tagged = %v in contract view, expected [99 100]",
			st.Ports["sfp-sfpplus2"].Tagged)
	}
	// `TaggedAtLeast` is an INTENT field: filling it on a read state would make the
	// comparison believe no removal is ever needed.
	if st.Ports["sfp-sfpplus2"].TaggedAtLeast {
		t.Error("TaggedAtLeast must NEVER be true on a read state: a device " +
			"carries a list, not an unresolved-all flag")
	}
	// Management VLAN and link state, which arm placement guardrails.
	if st.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d in contract view, expected 99", st.MgmtVLAN)
	}
	if !st.LinkUp["sfp-sfpplus2"] || st.LinkUp["ether1"] {
		t.Errorf("link state must survive conversion: it arms the port "+
			"guardrail (got %v)", st.LinkUp)
	}
	// The CPU pseudo-port is not a facade port, here or elsewhere.
	var _, present = st.Ports["bridge"]
	if present {
		t.Error("CPU pseudo-port `bridge` appeared in contract view ports: " +
			"that is the CPU, not a socket")
	}
}

// TestUnresolvedAllRemovesNothing
//
// `TaggedAtLeast` is only for unresolved « all » (empty site plan). A resolved
// « all » is an exact site VLAN set and is managed for tagged merge — extras leave.
// This test locks the merge calculation ApplyPorts uses for the unresolved case.
func TestUnresolvedAllRemovesNothing(t *testing.T) {
	// Device carries uplink in VLAN 30; plan asks only for 20.
	var have = []string{"ether1", "sfp-sfpplus2"}

	// Resolved / explicit: uplink is managed, it leaves VLAN 30.
	var strict = mergeMembers(have, []string{"ether1"},
		map[string]bool{"ether1": true, "sfp-sfpplus2": true})
	if !reflect.DeepEqual(strict, []string{"ether1"}) {
		t.Errorf("managed unwanted port leaves the VLAN: %v", strict)
	}

	// Unresolved « all »: uplink is NOT in managedTagged, so it stays.
	var unresolved = mergeMembers(have, []string{"ether1"},
		map[string]bool{"ether1": true})
	if !reflect.DeepEqual(unresolved, []string{"ether1", "sfp-sfpplus2"}) {
		t.Errorf("merge = %v, expected [ether1 sfp-sfpplus2].\n"+
			"  Unresolved \u00ab all \u00bb must not strip tagged membership while the "+
			"site plan is empty.",
			unresolved)
	}
}

// TestConnectorReachableThroughContract — the fault all this closes.
//
// Adopting the CRS326 sent an Instant On SNMP placement protocol at it: `https://10.99.0.54/`,
// connection refused since that device does not listen on 443. The cause was that no path
// led from the API layer to this connector — it called `instanton.OpenAdmin` hard-coded.
//
// [driver.ConnectorFor] is the mandatory gateway that makes that fault impossible. This
// test verifies it FINDS this connector: the opposite would mean the contract is declared
// somewhere and not satisfied here, and the API layer would cleanly refuse to write across
// a whole MikroTik fleet.
func TestConnectorReachableThroughContract(t *testing.T) {
	var conn, err = driver.ConnectorFor("mikrotik-routeros")
	if err != nil {
		t.Fatalf("driver.ConnectorFor(\u00ab mikrotik-routeros \u00bb) must return this "+
			"connector: without that, the API layer can write NOTHING on a MikroTik "+
			"RouterOS, and that is exactly the fault the contract exists to "+
			"close — %v", err)
	}
	if conn.Info().Name != "mikrotik-routeros" {
		t.Errorf("connector returned = %q", conn.Info().Name)
	}

	// Optional capabilities are tested by TYPE ASSERTION, letting the caller refuse BEFORE
	// joining the device. These must succeed.
	var _, isIdentifier = conn.(driver.Identifier)
	if !isIdentifier {
		t.Error("this connector can identify a device (with credentials): " +
			"driver.Identifier assertion must succeed, or MikroTik relocation becomes impossible")
	}

	// And those that are ABSENT must stay so: their absence is INFORMATION the screen
	// displays, not an omission. Declaring them by mistake would announce capabilities
	// nothing honors — including an UNVERIFIED address move on hardware, the best way to
	// make a device unreachable.
	var admin driver.Admin = &Client{}
	var _, isPersister = admin.(driver.Persister)
	if isPersister {
		t.Error("driver.Persister must NOT be implemented: RouterOS is `immediate`, " +
			"there is nothing to save — therefore no safety net either, and an empty " +
			"`Persist` would suggest a power cut could have cancelled")
	}
	var _, isMover = admin.(driver.MgmtMover)
	if !isMover {
		t.Error("driver.MgmtMover must be implemented: dual-path prepare / addressed / " +
			"commit follows the order in Capabilities().Mgmt.Notes (bridge/vlan → " +
			"interface/vlan → address → verify → remove old)")
	}
	var _, isWriter = admin.(driver.MgmtAddrWriter)
	if !isWriter {
		t.Error("driver.MgmtAddrWriter must be implemented: same-VLAN DHCP/static " +
			"apply uses the same REST write channel as port apply")
	}
	var _, isOnboarder = conn.(driver.Onboarder)
	if isOnboarder {
		t.Error("driver.Onboarder must NOT be implemented: a RouterOS is " +
			"administrable out of the box, there is no management mode to leave")
	}
}

// TestContractIdentityDoesNotConfuseTheTwoVersions
//
// RouterOS is the SYSTEM, RouterBOOT the boot loader. Observed: RouterOS 7.21beta3 runs
// on RouterBOOT 6.42.5. Merging them into the contract's `Firmware` field would make the
// device appear four major versions behind, and push for an update that is not warranted.
func TestContractIdentityDoesNotConfuseTheTwoVersions(t *testing.T) {
	var dev = DeviceIdentity{
		Model:      "CRS326-24G-2S+",
		Serial:     "945509EF00B0",
		Firmware:   "7.21beta3 (testing)",
		RouterBoot: "6.42.5",
		Build:      "2025-10-14 12:50:07",
		Hostname:   "lan-r4",
		Source:     "rest_api",
		Ports: switchPorts(fixture(t, "interface.json"),
			fixture(t, "interface_ethernet.json")),
	}
	// Facade is counted on real interfaces, as Identify does.
	var p Port
	for _, p = range dev.Ports {
		if p.SFP {
			dev.SFPPorts++
			if p.SpeedMbps >= 10000 {
				dev.SFPPlus = true
			}
			continue
		}
		dev.CopperPorts++
	}

	var id = dev.Standard()
	if id.Firmware != "7.21beta3 (testing)" {
		t.Errorf("Firmware = %q, expected RouterOS version (the SYSTEM)", id.Firmware)
	}
	if strings.Contains(id.Firmware, "6.42.5") {
		t.Error("RouterBOOT version slipped into Firmware field: " +
			"device would appear four major versions behind")
	}
	// Family is DERIVED from part number, as the contract wants for capability differences
	// at that level.
	if id.Family != "CRS" {
		t.Errorf("Family = %q, expected \u00ab CRS \u00bb derived from product reference", id.Family)
	}
	// `ModelCode` stays EMPTY: MikroTik has no catalogue reference distinct from the label.
	// Copying the model would display the same thing twice as if they were two pieces of
	// information.
	if id.ModelCode != "" {
		t.Errorf("ModelCode = %q: MikroTik has no catalogue reference distinct from the "+
			"label, and copying the model would display the same thing twice",
			id.ModelCode)
	}
	// Facade survives: without it, adoption produced a device with NO PORTS, which is not
	// « blank » but false.
	if id.CopperPorts != 24 || id.SFPPorts != 2 || !id.SFPPlus {
		t.Errorf("facade = %d copper + %d SFP (SFP+=%t), expected 24 + 2 SFP+",
			id.CopperPorts, id.SFPPorts, id.SFPPlus)
	}
	if len(id.PortGroups) != 2 {
		t.Fatalf("%d port groups in contract view, 2 expected: this package's "+
			"PortGroups() method must be FILLED in the contract field",
			len(id.PortGroups))
	}
	// Prefixes are those of the DEVICE, not a conventional « GE »: that is why the interface
	// reconstructs `sfp-sfpplus1`.
	if id.PortGroups[0]["prefix"] != "ether" ||
		id.PortGroups[1]["prefix"] != "sfp-sfpplus" {
		t.Errorf("group prefixes = %v / %v, expected \u00ab ether \u00bb and \u00ab sfp-sfpplus \u00bb",
			id.PortGroups[0]["prefix"], id.PortGroups[1]["prefix"])
	}
	// And source says where the information came from: the screen must not present a deduction
	// as a reading.
	if id.Source != "rest_api" {
		t.Errorf("Source = %q, expected \u00ab rest_api \u00bb", id.Source)
	}
}

// TestFamilyDerivedOrEmpty: empty rather than approximate.
//
// An invented family would rank a device in a range whose capabilities it lacks, and nobody
// would see the difference on screen.
func TestFamilyDerivedOrEmpty(t *testing.T) {
	var cases = []struct{ model, expected string }{
		{"CRS326-24G-2S+", "CRS"},
		{"CCR2004-1G-12S+2XS", "CCR"},
		{"RB5009UG+S+IN", "RB"},
		{"crs305-1g-4s+", "CRS"},
		{"", ""},
		{"326-24G", ""},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.model, func(t *testing.T) {
			var got = familyOf(c.model)
			if got != c.expected {
				t.Errorf("familyOf(%q) = %q, expected %q", c.model, got, c.expected)
			}
		})
	}
}

// TestSecretChangesOnlyForProvenAccount
//
// `SetSecret` changes the password OF THE ACCOUNT THAT JUST LOGGED IN, not another: letting
// the caller name one would open the door to « change the password of an account we have not
// proven we can access ».
//
// The edge case is access [Driver.Authenticate] returns DESPITE failure, for its transcript:
// its account is empty, and a `set` would go out with an empty name.
func TestSecretChangesOnlyForProvenAccount(t *testing.T) {
	var c = &Client{}
	var err = c.SetSecret(nil, "old", "new")
	if err == nil {
		t.Fatal("changing secret on access that proved no account must be " +
			"refused: there is nobody whose password can be changed")
	}
	if !strings.Contains(err.Error(), "proven") {
		t.Errorf("message must say no account was proven: %q", err)
	}
}

// TestRawKindNamesTheRightPlatform
//
// Without this, the debug probe would ask for a `wcd` section name from someone looking at
// a MikroTik, and the failure would look like an equipment fault.
func TestRawKindNamesTheRightPlatform(t *testing.T) {
	var c = &Client{}
	if c.RawKind() != "REST menu" {
		t.Errorf("RawKind = %q, expected \u00ab REST menu \u00bb: that is the word the "+
			"operator must read in front of a MikroTik", c.RawKind())
	}
	// The connector named in messages must be this one, or « this platform cannot do X »
	// helps nobody.
	if c.Connector().Name != "mikrotik-routeros" {
		t.Errorf("Connector().Name = %q", c.Connector().Name)
	}
}

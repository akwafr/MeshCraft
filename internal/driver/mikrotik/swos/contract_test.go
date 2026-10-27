package swos

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The adoption of the access contract, verified without hardware.
//
// What these tests cover is what can be covered off the network: CONFORMITY
// to the contract (including the absences, which are a deliverable), the pure
// decisions the write and the read make, and the refusals issued before a
// single byte leaves.
//
// What they do not cover, and which remains the write campaign: that a device
// accepts. The reads, for their part, were re-verified live on the reference
// CSS326-24G-2S+ through the proxy's tunnel.

// TestContractConformity: what this connector declares it can do.
//
// The compile-time assertions live in contract.go and suffice in the strict
// sense. This test exists for the OTHER half, the one no compile-time
// assertion can carry: the optional interfaces this connector must NOT
// satisfy.
//
// These absences are facts of the platform, not delays, and they have a
// direct consequence in the UI — the caller makes a type assertion and refuses
// BEFORE reaching the device, naming the gesture and the reason. The day
// someone adds to *Admin a method that accidentally satisfies one of these
// interfaces, the API layer will start offering a button that leads nowhere,
// and nothing would say so. That is exactly what this test catches.
func TestContractConformity(t *testing.T) {
	var connector any = (*Driver)(nil)
	var admin any = (*Admin)(nil)

	// What IS implemented. Redundant with the compile-time assertions, and
	// deliberately so: if a refactoring moved or removed them, this test would
	// remain.
	var _, isConnector = connector.(driver.Connector)
	if !isConnector {
		t.Error("*Driver must satisfy driver.Connector: without it, the API layer " +
			"can write NOTHING on this range — ConnectorFor would refuse, saying this " +
			"connector can describe the platform but not reach it")
	}
	var _, isIdentifier = connector.(driver.Identifier)
	if !isIdentifier {
		t.Error("*Driver must satisfy driver.Identifier: the realm of the Digest challenge " +
			"gives the model WITHOUT credentials, and it is the only range in the project that offers it")
	}
	var _, isAdmin = admin.(driver.Admin)
	if !isAdmin {
		t.Error("*Admin must satisfy driver.Admin")
	}
	var _, isRaw = admin.(driver.RawReader)
	if !isRaw {
		t.Error("*Admin must satisfy driver.RawReader: these pages are documented " +
			"nowhere, and a page can only be written once its field set has been seen")
	}
	var _, isSNMP = admin.(driver.SNMPConfigurator)
	if !isSNMP {
		t.Error("*Admin must satisfy driver.SNMPConfigurator: the empty capability is " +
			"the upstream lock, not the absence of the interface — the day an snmpget answers, " +
			"the version list gains a line without touching the connector")
	}

	// What must NOT be, with the fact that establishes it.
	var _, hasAccounts = admin.(driver.AccountManager)
	if hasAccounts {
		t.Error("SwOS has NO notion of an account: three form fields, one key " +
			"{pwd:''}, and no user name in the device's ENTIRE configuration " +
			"backup. Declaring AccountManager would offer to create " +
			"an account that does not exist")
	}
	// The widening of the contract on 2026-08-10 — Accounts returns
	// driver.Account values carrying the privilege — changes NOTHING here, and
	// that is the point: a richer interface is one more reason not to declare
	// it on a platform that has only an implicit `admin`. An Accounts() that
	// returned [{Name:"admin", Privilege: PrivilegeUnknown}] would be an
	// invention: the device names that account nowhere, and an audit reading
	// that line would believe it had enumerated something.
	var _, hasAccountsWidened = admin.(interface {
		Accounts(context.Context) ([]driver.Account, error)
	})
	if hasAccountsWidened {
		t.Error("*Admin must not carry the widened Accounts method either: the " +
			"privilege is not what this range lacked, it is the account itself")
	}
	var _, hasTraps = admin.(driver.TrapPusher)
	if hasTraps {
		t.Error("/snmp.b has NO receiver field, in any tab: declaring " +
			"TrapPusher would open 162/udp on the proxy to listen to silence")
	}
	var _, hasPersist = admin.(driver.Persister)
	if hasPersist {
		t.Error("Commit = immediate: there is nothing to save on this platform, hence " +
			"no safety net either — the only way back is ApplyResult.Backup")
	}
	var _, hasMgmt = admin.(driver.MgmtMover)
	if hasMgmt {
		t.Error("\"Allow From VLAN\" accepts only ONE VLAN and there is a single management " +
			"address: the cutover is a jump, not the three verifiable steps " +
			"MgmtMover requires")
	}
	var _, hasWriter = admin.(driver.MgmtAddrWriter)
	if !hasWriter {
		t.Error("*Admin must satisfy driver.MgmtAddrWriter: same-VLAN iptp/ip apply " +
			"on /sys.b after backup")
	}
	var _, hasJumper = admin.(driver.MgmtJumper)
	if !hasJumper {
		t.Error("*Admin must satisfy driver.MgmtJumper: single-address leap with " +
			"GET /backup.swb before rewriting /sys.b")
	}
	var _, hasOnboard = connector.(driver.Onboarder)
	if hasOnboard {
		t.Error("a SwOS is manageable out of the box: there is no management mode " +
			"to talk it out of")
	}
}

// TestDefaultPortPrefix: empty means "GE", never "write blindly".
//
// This prefix no longer NAMES anything — the ports carry the device's names
// (cf. naming.go) — but it still decides what "GE12" means when an intent
// recorded before this batch arrives. A missing prefix must therefore give a
// known default: refusing would prevent surveying a new switch, and guessing
// would miss the translation of a name the database carries.
func TestDefaultPortPrefix(t *testing.T) {
	var without = &Admin{}
	if without.portPrefix() != "GE" {
		t.Errorf("default prefix = %q, expected \"GE\" — the same convention as the "+
			"Instant On connector, so that a mixed fleet compares", without.portPrefix())
	}
	var with = &Admin{prefix: "Gi"}
	if with.portPrefix() != "Gi" {
		t.Errorf("the target's prefix must take precedence; got %q", with.portPrefix())
	}
}

// TestPageNameRefusedBeforeSending: this name goes into a URL.
//
// Two distinct dangers, and the same refusal covers them: an injection would
// divert the request to another endpoint of the device — and this platform
// has some that ACT (/reset, /reboot) —, and an unknown path does not return
// 404 but the entire web UI as HTTP 200, which costs a round trip for nothing.
func TestPageNameRefusedBeforeSending(t *testing.T) {
	// What is accepted: data pages, read-only pages marked with a "!", and the
	// backup.
	var good = []string{"sys.b", "/sys.b", "vlan.b", "!dhost.b", "!igmp.b", "backup.swb"}
	var name string
	for _, name = range good {
		var clean = strings.TrimPrefix(name, "/")
		if !rawUnit.MatchString(clean) {
			t.Errorf("%q should be accepted: it is a page of this device", name)
		}
	}

	// What is refused. The Admin is EMPTY and has no transport: if one of these
	// names passed the check, the call would go to the network and this test
	// would panic — which is a very clear way of failing.
	var bad = []string{
		"",
		"sys",             // no extension: not a page
		"index.html",      // the web UI itself
		"reset",           // a path that ACTS
		"../etc/passwd",   // path traversal
		"sys.b?x=1",       // parameters
		"sys.b#a",         //
		"vlan.b/../reset", //
		"SYS.B",           // SwOS pages are lower case
		"pagewithanamefartoolongtobeoneofthem.b",
	}
	var a = &Admin{}
	for _, name = range bad {
		var _, err = a.ReadRaw(context.Background(), name)
		if err == nil {
			t.Errorf("%q was accepted as a page name", name)
		}
	}

	if a.RawKind() != "page *.b" {
		t.Errorf("RawKind = %q; the UI must be able to tell the operator what \"a "+
			"unit\" means here, otherwise the debugging probe will ask them for a "+
			"wcd section in front of a MikroTik", a.RawKind())
	}
}

// TestRemovalComputedAndItsRefusals: the write CONVERGES, and what it refuses
// to remove can be read.
//
// The first delivery removed nothing: on SwOS, membership and tagging are the
// same data, and removing a port from the member list cuts that VLAN on that
// port. What "add only" cost was not visible — a device announced
// "conforming" could carry for months a VLAN the plan had removed, and a
// leaking segmentation is only noticed while looking for something else.
//
// Removal is therefore computed, with its refusals. Tested on the real
// capture: the reference device's 24 copper ports are in native VLAN 100 and
// members of 99 AND 100, the two cages are native 1 and members of both, and
// ONLY cage SFP2 carries a link.
//
// # THIS TEST SWITCHED SIDES, AND THAT IS THE HEART OF THE BATCH
//
// It used to require that a management VLAN NEVER be removed, including from a
// port without a link. That policy made convergence impossible on this
// device: its VLAN 99 is a member of all twenty-six ports, so a write "the 24
// copper ports in access VLAN 100" wrote zero ports and refused twenty-four
// times, on every pass (measured on 29 July 2026: `ports_written` empty, 48
// irreducible deviations). And the refusal cost the security it claimed to
// serve: the administration VLAN remained reachable by tagging from any access
// socket.
//
// The criterion is now the LINK, as for the other guards in this file.
func TestRemovalComputedAndItsRefusals(t *testing.T) {
	var current, rows = referencePlan(t)
	var protected = map[int]string{99: "management VLAN declared by the device"}

	var table, _, err = withVLANs(rows, []VLAN{{ID: 20}}, 26, nil)
	if err != nil {
		t.Fatalf("completion refused: %v", err)
	}

	// PORT WITHOUT A LINK: it loses BOTH, management included. No session
	// crosses an empty socket, and this removal is what makes an access port a
	// real access port.
	var changes, skipped, planErr = planFor(current, table,
		map[string]PortState{"Port1": {Mode: "access", PVID: 20}}, facadeCSS326(), protected, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(changes) != 1 {
		t.Fatalf("%d change(s), expected 1: %+v", len(changes), changes)
	}
	if !reflect.DeepEqual(changes[0].Leave, []int{99, 100}) {
		t.Errorf("VLANs to leave = %v, expected [99 100]: the plan only asks for "+
			"20, and this port carries NO link — leaving the administration VLAN a member "+
			"of an access port makes it reachable by tagging from any workstation",
			changes[0].Leave)
	}
	if len(skipped) != 0 {
		t.Errorf("no refusal expected on a port without a link: %v", skipped)
	}

	// PORT WITH A LINK: cage SFP2 is the only active one in the capture, and
	// administration arrives through it. It KEEPS 99, with its reason — SwOS
	// does not say which of its ports carries the session.
	changes, skipped, planErr = planFor(current, table,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 20}}, facadeCSS326(), protected, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(skipped) != 1 {
		t.Fatalf("%d refusal(s), expected 1 (the management VLAN on an active port): %v",
			len(skipped), skipped)
	}
	var expected string
	for _, expected = range []string{"SFP2", "99", "MANAGEMENT", "ACTIVE", "forced"} {
		if !strings.Contains(skipped[0], expected) {
			t.Errorf("the refusal must contain %q; got: %q", expected, skipped[0])
		}
	}
	var ch PortChange
	for _, ch = range changes {
		var vid int
		for _, vid = range ch.Leave {
			if vid == 99 {
				t.Error("the management VLAN was removed from the port carrying the link")
			}
		}
	}

	// `force` lifts that refusal too: it is an explicit gesture from the
	// operator, who knows their topology better than meshcraft.
	changes, skipped, planErr = planFor(current, table,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 20}}, facadeCSS326(), protected, true)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("force must lift the removal refusal: %v", skipped)
	}
	if len(changes) != 1 || !reflect.DeepEqual(changes[0].Leave, []int{99, 100}) {
		t.Errorf("with force, SFP2 should have left 99 and 100: %+v", changes)
	}

	// VLAN 1 is NEVER removed: it has no row in the table (cf. implicitVLAN),
	// so there is nothing to remove from it.
	changes, _, planErr = planFor(current, rows,
		map[string]PortState{"Port1": {Mode: "trunk", PVID: 1, Tagged: []int{99, 100}}},
		facadeCSS326(), protected, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	for _, ch = range changes {
		var vid int
		for _, vid = range ch.Leave {
			if vid == 1 {
				t.Errorf("%s: VLAN 1 is not in the table, there is nothing to "+
					"remove from it", ch.Port)
			}
		}
	}

	// NOTHING to remove when the plan asks for at least what the port already
	// carries.
	changes, skipped, planErr = planFor(current, rows, map[string]PortState{
		"SFP2": {Mode: "trunk", PVID: 100, Tagged: []int{99}},
	}, facadeCSS326(), protected, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("no removal was necessary: %v", skipped)
	}
	for _, ch = range changes {
		if len(ch.Leave) != 0 {
			t.Errorf("%s: nothing to remove, got %v", ch.Port, ch.Leave)
		}
	}

	// Unresolved "all" (`TaggedAtLeast`) justifies NO tagged removal — the
	// site plan does not exist yet. A resolved "all" carries the exact list
	// and removes the extras (cf. autoapply).
	changes, skipped, planErr = planFor(current, rows, map[string]PortState{
		"Port1": {Mode: "trunk", PVID: 100, Tagged: []int{99}, TaggedAtLeast: true},
	}, facadeCSS326(), protected, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("unresolved all asks for no removal, and therefore has no refusal to "+
			"explain: %v", skipped)
	}
	for _, ch = range changes {
		if len(ch.Leave) != 0 {
			t.Errorf("unresolved all removes nothing; %s would leave %v", ch.Port, ch.Leave)
		}
	}

	// An UNMANAGED port says nothing about what it must carry: it is not
	// touched.
	changes, skipped, planErr = planFor(current, rows, map[string]PortState{
		"Port1": {Mode: "libre"}, "Port2": {},
	}, facadeCSS326(), protected, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(changes) != 0 || len(skipped) != 0 {
		t.Errorf("\"absent = not managed\": %+v / %v", changes, skipped)
	}
}

// TestNoRemovalOnAnActivePortWhenTheAdminVLANIsUnknown: refusal no. 2.
//
// `avln` at 0 means "administration accepted from any VLAN", the factory
// state. Nothing then says which port carries the session, and removing a
// membership from the one carrying it cuts it — on a model without SSH,
// without Telnet and without a serial port.
func TestNoRemovalOnAnActivePortWhenTheAdminVLANIsUnknown(t *testing.T) {
	var current, rows = referencePlan(t)
	current.MgmtVLAN = 0

	// SFP2 carries the link in the capture, and it is a member of 99 and 100.
	var changes, skipped, err = planFor(current, rows, map[string]PortState{
		"SFP2": {Mode: "trunk", PVID: 1, Tagged: []int{99}},
	}, facadeCSS326(), map[int]string{}, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	var ch PortChange
	for _, ch = range changes {
		if len(ch.Leave) != 0 {
			t.Errorf("no removal may be computed on an ACTIVE port when the "+
				"administration VLAN is unknown; %s would leave %v", ch.Port, ch.Leave)
		}
	}
	if len(skipped) != 1 ||
		!strings.Contains(strings.ToLower(skipped[0]), "any vlan") {
		t.Fatalf("the refusal must explain WHY we do not know: %v", skipped)
	}
}

// TestStateNotes: what the standard model cannot express.
//
// State.Notes is MANDATORY on this platform, for a specific fact: a port whose
// VLAN mode is "disabled" filters NOTHING. Its PVID and membership are
// configured and without effect. Without the note, the UI would present an
// INERT configuration as a segmentation in place.
func TestStateNotes(t *testing.T) {
	var a = &Admin{}
	var snap = referenceSnapshot(t)

	// The reference device, as it is: all its ports are in "optional" mode
	// (/fwd.b `vlan:[0x01,…]`, re-verified live). No inert-filtering note must
	// therefore appear — a note crying wolf on a healthy device would get the
	// next ones ignored.
	var st, err = a.stateOf(snap)
	if err != nil {
		t.Fatalf("state refused: %v", err)
	}
	var note string
	for _, note = range st.Notes {
		if strings.Contains(note, "DISABLED") {
			t.Errorf("no port of the capture has filtering disabled: %q", note)
		}
	}
	if st.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d, expected 99", st.MgmtVLAN)
	}

	// The same device with filtering off on its first port.
	snap = referenceSnapshot(t)
	var modes, ok = snap.fwd.Nums(FieldVLANMode)
	if !ok {
		t.Fatalf("/fwd.b does not carry \"%s\"", FieldVLANMode)
	}
	modes[0] = VLANModeDisabled
	snap.fwd.SetNums(FieldVLANMode, modes)

	st, err = a.stateOf(snap)
	if err != nil {
		t.Fatalf("state refused: %v", err)
	}
	var found string
	for _, note = range st.Notes {
		if strings.Contains(note, "DISABLED") {
			found = note
		}
	}
	if found == "" {
		t.Fatalf("no note on a port with filtering off: the UI would present an "+
			"inert configuration as a segmentation in place. Notes = %v", st.Notes)
	}
	var expected string
	for _, expected = range []string{"Port1", "WITHOUT EFFECT"} {
		if !strings.Contains(found, expected) {
			t.Errorf("the note must contain %q; got: %q", expected, found)
		}
	}
	// And the port stays read: the note does not replace the state, it
	// qualifies it.
	if st.Ports["Port1"].PVID != 100 {
		t.Errorf("Port1 must stay read as the device carries it: %+v", st.Ports["Port1"])
	}
}

// TestNoteWhenAdministrationComesFromAnyVLAN: zero is not "unknown" here, and
// the caller does not have to know that.
//
// It is the factory state of a CSS: `avln` at 0 means "administration accepted
// from any VLAN". On Instant On, the same zero means "unknown" and makes the
// write REFUSE. The contract therefore forbids the API layer from
// interpreting this field itself — it is up to the connector to translate,
// and a sentence is the only channel provided.
func TestNoteWhenAdministrationComesFromAnyVLAN(t *testing.T) {
	var a = &Admin{}
	var snap = referenceSnapshot(t)
	var sys, err = ParseObject("{avln:0x00}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	snap.sys = sys

	var st, stateErr = a.stateOf(snap)
	if stateErr != nil {
		t.Fatalf("state refused: %v", stateErr)
	}
	if st.MgmtVLAN != 0 {
		t.Errorf("management VLAN = %d, expected 0", st.MgmtVLAN)
	}
	var found bool
	var note string
	for _, note = range st.Notes {
		if strings.Contains(note, "ANY VLAN") {
			found = true
		}
	}
	if !found {
		t.Errorf("the factory state must be explained, not left to interpretation: %v", st.Notes)
	}
}

// TestContractIdentity: the translation to the type the API layer consumes.
//
// This package's type is richer, and the contract asks for it. What matters
// here is that nothing USEFUL is lost on the way — first of all the
// faceplate, without which an adoption would produce a device WITH NO PORTS,
// which is not "blank" but wrong.
func TestContractIdentity(t *testing.T) {
	var id = Identity{
		Model: "CSS326-24G-2S+", Family: "CSS", Serial: "945809C232D5",
		MAC: "b8:69:f4:5f:14:a8", Firmware: "2.17", Build: "1725372990",
		Hostname: "lan-m6", Revision: "", BoardName: "",
		CopperPorts: 24, SFPPorts: 2, SFPFirst: 24, SFPPlus: true,
		MgmtIP: "192.0.2.36", MgmtVLAN: 99, Source: "sys.b",
	}
	var out = id.Contract()

	var cases = []struct {
		field, got, expected string
	}{
		{"model", out.Model, "CSS326-24G-2S+"},
		{"range", out.Family, "CSS"},
		{"serial", out.Serial, "945809C232D5"},
		{"MAC", out.MAC, "b8:69:f4:5f:14:a8"},
		{"firmware", out.Firmware, "2.17"},
		{"system name", out.Hostname, "lan-m6"},
		{"source", out.Source, "sys.b"},
		// MikroTik has no catalogue reference distinct from the label:
		// "CSS326-24G-2S+" IS the reference. Copying the model here would give
		// two identical fields, one of which would suggest a second source.
		{"catalogue reference", out.ModelCode, ""},
	}
	var c = cases[0]
	for _, c = range cases {
		if c.got != c.expected {
			t.Errorf("%s = %q, expected %q", c.field, c.got, c.expected)
		}
	}
	if out.CopperPorts != 24 || out.SFPPorts != 2 || !out.SFPPlus || out.PoE {
		t.Errorf("faceplate lost in translation: %+v", out)
	}
	// The faceplate in the format the frontend consumes: it is what lets the
	// device be drawn without a model catalogue.
	if !reflect.DeepEqual(out.PortGroups, id.PortGroups()) {
		t.Errorf("port groups = %v, expected %v", out.PortGroups, id.PortGroups())
	}
	// The cages have their OWN counter — "SFP1", "SFP2" — as the device names
	// them. See TestFaceplateGroupsNamedLikeTheDevice for the reasoning.
	if len(out.PortGroups) != 2 || out.PortGroups[1]["prefix"] != SFPPortPrefix ||
		out.PortGroups[1]["from"] != 1 {
		t.Errorf("the cages must be named \"SFP1\" and \"SFP2\": %v", out.PortGroups)
	}
}

// referenceSnapshot rebuilds the snapshot of the four state pages from the
// device's REAL captures, without the network.
//
// Returned FRESH on every call: the tests using it modify the pages to try a
// case, and a shared page would make one test depend on the execution order of
// the others.
func referenceSnapshot(t *testing.T) *snapshot {
	t.Helper()
	// The faceplate goes with the geometry, as readAll does on the device: it
	// is what NAMES the ports, and a snapshot without it would return no port
	// at all.
	var out = &snapshot{ports: 26, sfp: 2, sfpFirst: 24, facade: facadeCSS326()}
	var err error
	out.link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b: %v", err)
	}
	out.fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b: %v", err)
	}
	out.sys, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b: %v", err)
	}
	out.vlanRows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b: %v", err)
	}
	return out
}

package swos

import (
	"meshcraft/internal/driver"
	"reflect"
	"strings"
	"testing"
)

// The write path, verified against REAL device captures.
//
// NONE of these writes have been executed against a switch: the tool available to
// reach the device through the tunnel is read-only, by project architecture decision.
// What these tests establish is therefore precisely what they can establish, and nothing more:
//
//   - the BODY produced, byte-for-byte, from the page the device actually served;
//   - that the returned field set matches exactly what the device stores — the property
//     that prevents a VLAN write from carrying away port isolation with it;
//   - the safeguards, which decide what we do NOT write.
//
// What they do not establish: that the device accepts. That is the orchestrator's write campaign.

// referenceFwd reads /fwd.b as the device served it.
func referenceFwd(t *testing.T) *Page {
	t.Helper()
	var page, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b: %v", err)
	}
	return page
}

// TestFwdBodyReturnsCompleteFieldSet: THE safety property of this connector.
//
// SwOS does not accept partial writes: its interface rebuilds the ENTIRE page object
// and posts it as-is. Returning /fwd.b without its `srt` array would therefore leave
// the device free to reset storm limits on all twenty-six ports when setting a VLAN —
// and it would do so silently, since this platform has no error messages.
//
// The test therefore compares the produced body to the field set the device STORES, as
// enumerated by its own configuration backup.
func TestFwdBodyReturnsCompleteFieldSet(t *testing.T) {
	var fwd = referenceFwd(t)

	// A native VLAN changed on port 1, as ApplyPorts would do.
	var dvid, ok = fwd.Nums(FieldDefaultVLAN)
	if !ok {
		t.Fatalf("/fwd.b does not contain %q", FieldDefaultVLAN)
	}
	dvid[0] = 42
	fwd.SetNums(FieldDefaultVLAN, dvid)

	var body, missing = fwd.Only(FwdWritableFields(26))
	if len(missing) > 0 {
		t.Fatalf("missing fields in the page served by the device: %v — "+
			"writing must be REFUSED in this case, not truncated", missing)
	}

	// The field set must match exactly what the device backup contains.
	var backup, backupErr = ParseBackup(capture(t, "backup.swb"))
	if backupErr != nil {
		t.Fatalf("/backup.swb: %v", backupErr)
	}
	var stored *Page
	stored, ok = backup.Obj("fwd.b")
	if !ok {
		t.Fatal("backup does not contain /fwd.b")
	}
	if !reflect.DeepEqual(body.Keys(), stored.Keys()) {
		t.Errorf("produced body does not carry the same field set as what "+
			"the device stores.\n  produced: %v\n  stored  : %v", body.Keys(), stored.Keys())
	}

	// The change is present, and NOTHING else moved: the other 25 native VLANs,
	// isolation masks, storm limits, and ingress rates match the read page.
	var encoded = body.Encode()
	if !strings.Contains(encoded, "dvid:[0x2a,0x64,0x64") {
		t.Errorf("port 1 native VLAN should become 0x2a while others stay at "+
			"0x64; body: %s", excerpt(encoded, 400))
	}
	// `srt` (storm limits) must remain intact: 26 values at 0x64.
	if !strings.Contains(encoded, "srt:[0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,"+
		"0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64,0x64]") {
		t.Error("storm limits were altered by a VLAN write")
	}
	// The isolation matrix too: `fp1:0x03fffffe` means "all ports except the first".
	if !strings.Contains(encoded, "fp1:0x03fffffe") {
		t.Error("port isolation matrix was altered")
	}
}

// TestWritingRefusedOnUnexpectedFieldSet: refuse rather than truncate.
//
// Firmware that would not serve an expected field must NOT lead to a partial write.
// That is the direct consequence of the previous rule, and the only safe behavior on
// a platform whose rejections are silent.
func TestWritingRefusedOnUnexpectedFieldSet(t *testing.T) {
	var page, err = ParseObject("{fp1:0x02,dvid:[0x01,0x01],vlan:[0x01,0x01]}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var _, missing = page.Only(FwdWritableFields(2))
	if len(missing) == 0 {
		t.Fatal("no missing field flagged on a manifestly incomplete page")
	}
	// Traffic-protection fields must appear among the missing ones: that is what the
	// operator should read in the refusal message.
	var joined = strings.Join(missing, " ")
	var expected string
	for _, expected = range []string{"srt", "ir", "fp2", "lck"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("field %q should be flagged missing; got %v", expected, missing)
		}
	}
}

// TestVLANCreatedWithDeviceDefaults: a created VLAN must not be created at zero.
//
// The device interface defaults are not zeros: per-VLAN isolation and LEARNING are 1
// (`u:1` in its field definitions). A VLAN created with learning disabled would flood
// all its traffic onto every member port — a default visible only under load.
//
// And the member mask starts EMPTY, while the interface default is "all ports"
// (`u:268435455`): ApplyPorts sets the desired ports there, and creating a VLAN with
// all ports as members would pass all traffic everywhere.
func TestVLANCreatedWithDeviceDefaults(t *testing.T) {
	var row = newVLANRow(VLAN{ID: 42, Name: "workshop"}, 26)

	// Field order follows the device backup.
	var stored, err = ParseBackup(capture(t, "backup.swb"))
	if err != nil {
		t.Fatalf("/backup.swb: %v", err)
	}
	var rows []*Page
	var ok bool
	rows, ok = stored.List("vlan.b")
	if !ok || len(rows) == 0 {
		t.Fatal("backup does not contain the VLAN table")
	}
	if !reflect.DeepEqual(row.Keys(), rows[0].Keys()) {
		t.Errorf("field order of a created row differs from the device.\n  created: %v\n  read   : %v", row.Keys(), rows[0].Keys())
	}

	var cases = []struct {
		field string
		want  uint32
	}{
		{FieldVLANID, 42},
		{FieldVLANMembers, 0}, // empty: ApplyPorts sets the ports
		{"piso", 1},           // interface default, not zero
		{"lrn", 1},            // learning ACTIVE: at zero, the VLAN would flood
		{"mrr", 0},
		{"igmp", 0},
	}
	var c = cases[0]
	for _, c = range cases {
		var got, has = row.Num(c.field)
		if !has || got != c.want {
			t.Errorf("%s = %d (ok=%v), expected %d", c.field, got, has, c.want)
		}
	}
	var name string
	name, ok = row.Text(FieldVLANName)
	if !ok || name != "workshop" {
		t.Errorf("name = %q (ok=%v), expected \"workshop\"", name, ok)
	}
}

// TestMemberMaskMatchesModelShape: a model with more than 32 ports uses a word array.
//
// Without that, SetBit would refuse on the created row and membership could not be
// set — writing would proceed with a VLAN that has no members, and therefore carries no traffic.
func TestMemberMaskMatchesModelShape(t *testing.T) {
	var petit = newVLANRow(VLAN{ID: 10}, 26)
	var v, _ = petit.Value(FieldVLANMembers)
	if v.Kind != KindNum {
		t.Errorf("up to 32 ports, the mask is a scalar; got %v", v.Kind)
	}
	if !petit.SetBit(FieldVLANMembers, 25, true) {
		t.Error("membership could not be set on a scalar mask")
	}

	var grand = newVLANRow(VLAN{ID: 10}, 48)
	var grandMasque, _ = grand.Value(FieldVLANMembers)
	if grandMasque.Kind != KindList {
		t.Errorf("beyond 32 ports, the mask is a word array; got %v",
			grandMasque.Kind)
	}
	if !grand.SetBit(FieldVLANMembers, 47, true) {
		t.Error("membership could not be set on port 48")
	}
	var on, ok = grand.Bit(FieldVLANMembers, 47)
	if !ok || !on {
		t.Error("bit applied on port 48 does not read back")
	}
}

// TestTableCompletedWithoutLosingRows: a table is written ENTIRELY, so omitting a row deletes it.
//
// Existing rows must be preserved field-for-field, including rows we do not understand:
// per-VLAN isolation, learning, mirroring, and IGMP snooping are set by the operator and are not ours to change.
func TestTableCompletedWithoutLosingRows(t *testing.T) {
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b: %v", err)
	}
	var before = EncodeTable(rows)

	var out, created, buildErr = withVLANs(rows,
		[]VLAN{{ID: 42, Name: "workshop"}, {ID: 99}}, 26, nil)
	if buildErr != nil {
		t.Fatalf("completion refused: %v", buildErr)
	}
	// VLAN 99 already existed: only 42 is created.
	if !reflect.DeepEqual(created, []int{42}) {
		t.Errorf("created VLAN = %v, expected [42] — 99 is already in the table", created)
	}
	if len(out) != 3 {
		t.Fatalf("%d row(s) after completion, expected 3", len(out))
	}
	// The two original rows are present, intact, and order follows identifiers — stable across runs.
	var vids []uint32
	var r *Page
	for _, r = range out {
		var v, _ = r.Num(FieldVLANID)
		vids = append(vids, v)
	}
	if !reflect.DeepEqual(vids, []uint32{42, 99, 100}) {
		t.Errorf("identifiers = %v, expected [42 99 100] sorted", vids)
	}
	// Original rows did not move by a single byte.
	var encoded = EncodeTable(out)
	var line string
	for _, line = range strings.Split(strings.Trim(before, "[]"), "},{") {
		line = strings.Trim(line, "{}")
		if !strings.Contains(encoded, line) {
			t.Errorf("an original row was altered: %q missing from produced body", line)
		}
	}
}

// TestTableRefusesSwOSBounds: what the device would refuse, we refuse first.
func TestTableRefusesSwOSBounds(t *testing.T) {
	var rows, _ = ParseTable(capture(t, "vlan.b"))
	var cases = []struct {
		name string
		vid  int
	}{
		{"VLAN 0", 0},
		{"VLAN 4095", 4095},
		{"VLAN 5000", 5000},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var _, _, err = withVLANs(rows, []VLAN{{ID: c.vid}}, 26, nil)
			if err == nil {
				t.Errorf("VLAN %d accepted although the SwOS table ranges from 1 to 4094", c.vid)
			}
		})
	}

	// And the 250-entry limit imposed by the interface.
	var pleine []*Page
	var i int
	for i = 1; i <= 250; i++ {
		pleine = append(pleine, newVLANRow(VLAN{ID: i}, 26))
	}
	var _, _, pleineErr = withVLANs(pleine, []VLAN{{ID: 300}}, 26, nil)
	if pleineErr == nil {
		t.Error("a 251st VLAN was accepted: the device refuses beyond 250")
	}
}

// TestWantedVLANsAssembled: the site plan AND what ports request.
//
// The site VLAN plan is applied in ENTIRETY, not only VLANs a port uses: an adopted
// device must know its site VLANs, otherwise assigning a port from the device interface
// — or plugging a trunk — would offer nothing.
func TestWantedVLANsAssembled(t *testing.T) {
	var desired = map[string]PortState{
		"Port1": {Mode: "access", PVID: 20},
		"Port2": {Mode: "trunk", PVID: 1, Tagged: []int{30, 40}},
		// An unmanaged port requests nothing.
		"Port3": {Mode: "libre", PVID: 999},
		"Port4": {},
	}
	var site = []VLAN{{ID: 10, Name: "mgmt"}, {ID: 20, Name: "users"}}
	var got = wantedVLANs(desired, site, nil)

	var expected = []VLAN{
		{ID: 1}, {ID: 10, Name: "mgmt"}, {ID: 20, Name: "users"}, {ID: 30}, {ID: 40},
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("wanted VLANs = %+v, expected %+v", got, expected)
	}
	// The site plan name is not overwritten by an empty port intent.
	if got[2].Name != "users" {
		t.Errorf("VLAN 20 lost its name: %+v", got[2])
	}
	// A "libre" port requested nothing.
	var v VLAN
	for _, v = range got {
		if v.ID == 999 {
			t.Error("an unmanaged port requested a VLAN")
		}
	}
}

// The former port-index refusal test now lives in naming_test.go:
// name → index translation is no longer a function in
// this file but a method on [Facade], and it accepts TWO name families — the device's
// ("SFP1") and the legacy continuous numbering ("GE25").

// TestRemovalPolicyDeclared: what apply does AND what it refuses must be readable from outside the package.
//
// Apply now REMOVES memberships the plan no longer requests, and refuses four cases —
// management VLAN, effective native VLAN, ports in "all", active ports on a device whose
// admin VLAN is unknown. A UI showing "compliant" must be able to say which of the two things it reports.
func TestRemovalPolicyDeclared(t *testing.T) {
	var s = RemovalPolicy()
	if s == "" {
		t.Fatal("removal policy must be readable from outside the package")
	}
	var word string
	for _, word = range []string{"removal", "management", "reason"} {
		if !strings.Contains(strings.ToLower(s), word) {
			t.Errorf("policy string must contain %q; got: %q", word, s)
		}
	}
}

// TestVLANReceiveAndForceVLANWritePolicy: what we refuse to write, we say so.
//
// `vlni` and `fvid` are writable, and are already sent back to the device on every apply
// with their read value. Not writing them is a CHOICE — tightening them drops traffic,
// and the standard model does not express what would be needed to decide — and a choice
// that is not documented looks like an oversight.
func TestVLANReceiveAndForceVLANWritePolicy(t *testing.T) {
	var s = receivePolicy()
	var word string
	for _, word = range []string{FieldVLANReceive, FieldForceVLAN, "traffic"} {
		if !strings.Contains(s, word) {
			t.Errorf("policy string must contain %q; got: %q", word, s)
		}
	}
}

// TestWritableExtrasSTP: spanning-tree is the first extra this connector applies.
func TestWritableExtrasSTP(t *testing.T) {
	if !WritesExtra("stp") {
		t.Fatal("WritesExtra(stp) must be true once ApplySpanningTree exists")
	}
	var name string
	for _, name = range []string{"poe", "port_security"} {
		if WritesExtra(name) {
			t.Errorf("WritesExtra(%q) = true without apply code", name)
		}
	}
}

// --- Apply decision, verified against real captures ---
//
// PlanPorts is pure, and that is what makes the connector's most dangerous logic
// verifiable without hardware: it decides which port keeps its native VLAN, and therefore
// whether the device stays reachable.

// planFor calls [PlanPorts] without supplying the VLAN mode array.
//
// A test shortcut, and it says something: `Modes` at nil skips VLAN filtering wake,
// so these tests exercise membership and native VLAN ONLY — as they always have.
// Mode wake has its own tests, where the array is supplied explicitly
// (see TestFiltrageReveilleSurUnPortEteint). Mixing them would pass a membership test
// for a mode test, and vice versa.
func planFor(current State, table []*Page, desired map[string]PortState,
	facade Facade, protected map[int]string,
	force bool) ([]PortChange, []string, error) {
	var out, err = PlanPorts(PortsInput{
		Current: current, Table: table, Desired: desired, Facade: facade,
		Protected: protected, Force: force,
	})
	return out.Changes, out.Skipped, err
}

// referencePlan prepares state and the reference device table.
func referencePlan(t *testing.T) (State, []*Page) {
	t.Helper()
	// Per-port VLAN mode is ignored here, and that is not a loss: the apply decision
	// does not use it, and it is no longer a state field — it is a note for the operator
	// (see VLANFiltering and stateOf).
	var st, _ = referenceState(t)
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b: %v", err)
	}
	// Reference device state: managed on VLAN 99, and a SINGLE port with link — cage SFP2, SFP2.
	return State{
		VLANs: []VLAN{{ID: 99}, {ID: 100}}, Ports: st,
		MgmtVLAN: 99, Links: map[string]driver.LinkState{"SFP2": driver.LinkUpState},
	}, rows
}

// TestPlanDoesNothingWhenAlreadyCompliant: a compliant apply posts NOTHING.
//
// On this platform, writing is persistent by default with no rollback: a POST that changes
// nothing is risk taken for nothing, and it would be taken on EVERY reconciliation pass,
// across the entire fleet. The requested intent here is exactly the state read from the device.
func TestPlanDoesNothingWhenAlreadyCompliant(t *testing.T) {
	var current, rows = referencePlan(t)

	// Request from the device exactly what it already carries.
	var desired = map[string]PortState{}
	var name string
	var p PortState
	for name, p = range current.Ports {
		desired[name] = p
	}

	var changes, skipped, err = planFor(current, rows, desired, facadeCSS326(),
		map[int]string{99: "management VLAN declared by the device"}, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("%d change(s) on an already compliant device: %+v — no page must "+
			"be posted in this case", len(changes), changes)
	}
	if len(skipped) != 0 {
		t.Errorf("ports were skipped although nothing changed: %v", skipped)
	}
}

// TestSafeguardProtectsManagementTagging: THE safeguard, and it is not Instant On's.
//
// On the reference device, the admin session arrives TAGGED on VLAN 99 on cage SFP2 (SFP2),
// whose native VLAN is 1. The Instant On rule — "do not touch the native VLAN of an active
// port that is an UNTAGGED member of the management VLAN" — would therefore protect nothing
// here: this port's native VLAN is not 99.
//
// What matters on SwOS is TAGGING: changing a port's native VLAN moves the old native to
// tagged and the new one to untagged. The change is safe if and only if it does not modify
// management VLAN tagging on that port.
func TestSafeguardProtectsManagementTagging(t *testing.T) {
	var current, rows = referencePlan(t)
	var protected = map[int]string{99: "management VLAN declared by the device"}

	// SAFE CASE: SFP2 moves from native VLAN 1 to native VLAN 100. VLAN 99 stays tagged
	// before and after, the session does not move. Refusing here would make the uplink impossible to configure.
	var changes, skipped, err = planFor(current, rows,
		map[string]PortState{"SFP2": {Mode: "trunk", PVID: 100, Tagged: []int{99}}},
		facadeCSS326(), protected, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("moving from native 1 to native 100 leaves VLAN 99 tagged: the session does "+
			"not move, this must be allowed. Refusal: %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 100 {
		t.Fatalf("SFP2 should move to native VLAN 100: %+v", changes)
	}

	// DANGEROUS CASE: SFP2 moves to native VLAN 99, the management VLAN. Admin traffic would
	// then leave WITHOUT a tag, and the neighbor sending it tagged would no longer be heard.
	changes, skipped, err = planFor(current, rows,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 99}},
		facadeCSS326(), protected, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("port carrying the tagged session was switched to native 99: %+v", changes)
	}
	if len(skipped) != 1 {
		t.Fatalf("%d refusal reason(s), expected 1: %v", len(skipped), skipped)
	}
	// The reason must be actionable for an operator: which port, that admin arrives TAGGED,
	// and that there is no fallback console.
	var expected string
	for _, expected = range []string{"SFP2", "ACTIVE link", "TAGGED", "serial"} {
		if !strings.Contains(skipped[0], expected) {
			t.Errorf("refusal reason must contain %q; got: %q", expected, skipped[0])
		}
	}

	// `force` lifts the safeguard — an explicit operator gesture who knows their topology
	// better than meshcraft and accepts the relocation risk.
	changes, skipped, err = planFor(current, rows,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 99}},
		facadeCSS326(), protected, true)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("force must lift the safeguard; refusal: %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 99 {
		t.Errorf("with force, SFP2 should move to native VLAN 99: %+v", changes)
	}
}

// TestSafeguardProtectsClassicUntaggedCase: the other direction, already covered by the Instant On rule.
//
// An active port whose admin traffic arrives UNTAGGED — native VLAN equal to the management
// VLAN — must not change native VLAN: admin would become tagged and access would break.
func TestSafeguardProtectsClassicUntaggedCase(t *testing.T) {
	var current, rows = referencePlan(t)
	// Put the device in the classic case: SFP2 is access on the management VLAN and carries the link.
	current.Ports["SFP2"] = PortState{Mode: "access", PVID: 99}

	var changes, skipped, err = planFor(current, rows,
		map[string]PortState{"SFP2": {Mode: "access", PVID: 100}},
		facadeCSS326(), map[int]string{99: "management VLAN from the site plan"}, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("port carrying untagged admin was modified: %+v", changes)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "UNTAGGED") {
		t.Fatalf("reason must say admin arrives untagged: %v", skipped)
	}
}

// TestVLAN1NeverCreatedInTable: VLAN 1 is an IMPLICIT hardware default.
//
// OBSERVED: both cages on the reference device have native VLAN 1 while its VLAN table
// contains only VLANs 99 and 100. VLAN 1 has no row there, and must not receive one —
// creating it would make VLAN 1 a declared VLAN whose only members are listed ports, i.e.
// change behavior of all untagged traffic on the device, without anyone having requested it.
func TestVLAN1NeverCreatedInTable(t *testing.T) {
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b: %v", err)
	}
	var out, created, buildErr = withVLANs(rows,
		[]VLAN{{ID: 1, Name: "default"}, {ID: 42}}, 26, nil)
	if buildErr != nil {
		t.Fatalf("completion refused: %v", buildErr)
	}
	if !reflect.DeepEqual(created, []int{42}) {
		t.Errorf("created VLAN = %v, expected [42] only: VLAN 1 is not added", created)
	}
	if rowFor(out, 1) != nil {
		t.Error("a row was created for VLAN 1")
	}

	// And a port whose native VLAN is 1 requests NO membership: looking for a VLAN 1 row
	// and complaining about its absence would fail apply on a perfectly configured device.
	var current, table = referencePlan(t)
	var changes, skipped, planErr = planFor(current, table,
		map[string]PortState{"Port1": {Mode: "trunk", PVID: 1, Tagged: []int{99}}},
		facadeCSS326(), map[int]string{99: "plan"}, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("native VLAN 1 must not cause any failure: %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 1 || len(changes[0].Join) != 0 {
		t.Errorf("Port1 should only move to native VLAN 1, with no membership to "+
			"set (already a member of 99): %+v", changes)
	}
}

// TestPlanAllowsAddingTaggedVLANOnSessionPort: what the safeguard does NOT block, and must not block.
//
// Adding a TAGGED VLAN to a port does not touch its untagged traffic, so it does not cut
// the session. Refusing this too would make an uplink impossible to configure, and that is
// precisely the port through which the entire site passes.
func TestPlanAllowsAddingTaggedVLANOnSessionPort(t *testing.T) {
	var current, rows = referencePlan(t)
	// VLAN 42 must exist in the table for a port to join it.
	var complete, _, err = withVLANs(rows, []VLAN{{ID: 42}}, 26, nil)
	if err != nil {
		t.Fatalf("completion refused: %v", err)
	}

	// SFP2 keeps native VLAN 1 and gains tagged VLAN 42.
	var desired = map[string]PortState{
		"SFP2": {Mode: "trunk", PVID: 1, Tagged: []int{42, 99, 100}},
	}
	var changes, skipped, planErr = planFor(current, complete, desired, facadeCSS326(),
		map[int]string{99: "management VLAN declared by the device"}, false)
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(skipped) != 0 {
		t.Errorf("adding a tagged VLAN does not touch untagged traffic: this must "+
			"not be blocked. Refusal: %v", skipped)
	}
	if len(changes) != 1 {
		t.Fatalf("%d change(s), expected 1: %+v", len(changes), changes)
	}
	// The port is ALREADY a member of 99 and 100: only 42 must be joined, and native VLAN must not move.
	if !reflect.DeepEqual(changes[0].Join, []int{42}) {
		t.Errorf("VLANs to join = %v, expected [42] — port is already a member of 99 and 100",
			changes[0].Join)
	}
	if changes[0].PVID != 0 {
		t.Errorf("native VLAN does not change: PVID = %d, expected 0 (unchanged)",
			changes[0].PVID)
	}
	if changes[0].Index != 25 {
		t.Errorf("SwOS index of SFP2 = %d, expected 25", changes[0].Index)
	}
}

// TestPlanFreelyModifiesPortWithoutLink: otherwise "adopt" would mean nothing.
//
// On a factory-default device, all ports share the same default VLAN, which is also the
// admin VLAN. Without the LINK condition, no port would ever be modifiable.
func TestPlanFreelyModifiesPortWithoutLink(t *testing.T) {
	var current, rows = referencePlan(t)
	// Port1 carries native VLAN 100 and has NO link in the capture.
	var desired = map[string]PortState{"Port1": {Mode: "access", PVID: 99}}

	var changes, skipped, err = planFor(current, rows, desired, facadeCSS326(),
		map[int]string{99: "management VLAN declared by the device"}, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(skipped) != 0 {
		t.Errorf("a port without link cannot carry any session: %v", skipped)
	}
	if len(changes) != 1 || changes[0].PVID != 99 || changes[0].Index != 0 {
		t.Fatalf("Port1 should move to native VLAN 99: %+v", changes)
	}
	// It is already a member of 99 in the capture: nothing to join.
	if len(changes[0].Join) != 0 {
		t.Errorf("VLANs to join = %v, expected none — Port1 is already a member of 99",
			changes[0].Join)
	}
}

// TestPlanSparesAllActivePortsWhenAdminVLANUnknown: the factory-default device case.
//
// `avln` at 0 means "admin accepted from any VLAN". Nothing then says which port carries
// the session, and protection falls back to ALL active ports — more conservative than the
// nominal case, which is the right tradeoff.
func TestPlanSparesAllActivePortsWhenAdminVLANUnknown(t *testing.T) {
	var current, rows = referencePlan(t)
	current.MgmtVLAN = 0

	var desired = map[string]PortState{
		"SFP2":  {Mode: "access", PVID: 100}, // active port: spared
		"Port1": {Mode: "access", PVID: 99},  // port without link: modifiable
	}
	var changes, skipped, err = planFor(current, rows, desired, facadeCSS326(),
		map[int]string{}, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "SFP2") {
		t.Errorf("active port should be spared: %v", skipped)
	}
	if !strings.Contains(skipped[0], "any VLAN whatsoever") {
		t.Errorf("reason must explain WHY we do not know: %q", skipped[0])
	}
	if len(changes) != 1 || changes[0].Port != "Port1" {
		t.Errorf("Port1 should remain modifiable: %+v", changes)
	}
}

// TestPlanIgnoresWhatItDoesNotManage: "absent = unmanaged".
//
// meshcraft does not claim ownership of what it never applied, and a port name it does
// not understand must not become a random write.
func TestPlanIgnoresWhatItDoesNotManage(t *testing.T) {
	var current, rows = referencePlan(t)
	var desired = map[string]PortState{
		"Port1": {Mode: "libre", PVID: 99},
		"Port2": {},
		"GE99":  {Mode: "access", PVID: 99}, // nonexistent port on this model
	}
	var changes, skipped, err = planFor(current, rows, desired, facadeCSS326(),
		map[int]string{99: "plan"}, false)
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("none of these ports must be written: %+v", changes)
	}
	// The nonexistent port, however, must be REPORTED: staying silent would suggest the plan was fully applied.
	if len(skipped) != 1 || !strings.Contains(skipped[0], "GE99") {
		t.Errorf("nonexistent port must be flagged, not silently ignored: %v", skipped)
	}
}

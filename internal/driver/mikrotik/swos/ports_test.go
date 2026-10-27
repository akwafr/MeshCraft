package swos

import (
	"reflect"
	"strings"
	"testing"
)

// Tests of the complete VLAN model: WAKING UP filtering, the management VLAN
// never orphaned, and the three per-port fields the read kept quiet about.
//
// All replay the real captures of the reference CSS326-24G-2S+ (testdata/).
// What they carry, and what makes these tests useful rather than theoretical:
//
//	/fwd.b `vlan`  → [0x01 × 26], i.e. "optional" everywhere — the factory
//	                 DEFAULT, neither disabled nor enabled;
//	/fwd.b `vlni`  → [0x00 × 26], "any" everywhere;
//	/fwd.b `fvid`  → 0x00000000, nobody;
//	/fwd.b `dvid`  → 100 on the 24 copper ports, 1 on the two cages;
//	/vlan.b        → VLANs 99 and 100, member mask 0x03ffffff — all 26 ports
//	                 members of both.
//
// No WRITE has been exercised on this hardware: these tests try out the
// decision, not the device's reaction.

// referenceModes loads the /fwd.b `vlan` array of the capture.
func referenceModes(t *testing.T) []uint32 {
	t.Helper()
	var fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b: %v", err)
	}
	var modes, ok = fwd.Nums(FieldVLANMode)
	if !ok || len(modes) != 26 {
		t.Fatalf("/fwd.b \"%s\": %d value(s), 26 expected", FieldVLANMode, len(modes))
	}
	return modes
}

// TestReferenceModeIsOptional: the premise of every mode test.
//
// The reference device is "optional" on its 26 ports, NOT "disabled". That
// is what makes planModeRaise's guard verifiable in both directions: on this
// capture, nothing must be woken up.
func TestReferenceModeIsOptional(t *testing.T) {
	var modes = referenceModes(t)
	var i int
	for i = range modes {
		if modes[i] != VLANModeOptional {
			t.Fatalf("/fwd.b \"%s\"[%d] = %d, expected %d (optional): the capture shows "+
				"the factory default", FieldVLANMode, i, modes[i], VLANModeOptional)
		}
	}
}

// TestNothingIsTightenedOnAWorkingPort — THE refusal that protects a fleet in
// service.
//
// `optional` → `enabled` TIGHTENS a filter on a port that works: every frame
// of a VLAN the table does not declare stops passing the second it is
// written. meshcraft does not know which undeclared VLANs transit through a
// port, and nobody asked it to cut them. Same thing, worse, for `enabled` →
// `strict`, which adds an INGRESS filter.
//
// And NOTHING is ever lowered: an operator who set `strict` meant it.
func TestNothingIsTightenedOnAWorkingPort(t *testing.T) {
	var members = map[int]bool{1: true, 99: true, 100: true}
	var protected = map[int]string{99: "plan"}
	var cases = []struct {
		name string
		mode uint32
	}{
		{"optional is not tightened", VLANModeOptional},
		{"enabled is not pushed to strict", VLANModeEnabled},
		{"strict is never lowered", VLANModeStrict},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = planModeRaise("SFP2", c.mode, members, 1, true, protected, false)
			if got.Change {
				t.Errorf("mode %d changed to %d: only `disabled` is woken up",
					c.mode, got.Mode)
			}
			if got.Refused != "" {
				t.Errorf("a mode we do not touch has no refusal to explain: %q",
					got.Refused)
			}
		})
	}
	// And `force` must not tighten either: the "force" button lifts the
	// write's guards, it does not authorise a write nobody asked for.
	var got = planModeRaise("SFP2", VLANModeOptional, members, 1, true, protected, true)
	if got.Change {
		t.Errorf("force tightened a port in optional: that is not what force means "+
			"(got mode %d)", got.Mode)
	}
}

// TestFilteringWokenUpOnAPortThatIsOff: a write that does not act is a write
// that lies.
//
// A port whose /fwd.b `vlan` is `disabled` IGNORES the VLAN table. meshcraft
// could write membership and native VLAN there, read back, compare
// everything, display "conforming" — and the port kept letting everything
// through. The read SAID so in a note; no write fixed it, and the operator's
// only recourse was to go and click in their switch's UI.
func TestFilteringWokenUpOnAPortThatIsOff(t *testing.T) {
	var members = map[int]bool{1: true, 99: true, 100: true}
	var protected = map[int]string{99: "management VLAN declared by the device"}

	// Port WITHOUT a link: no session goes through it, waking it is risk-free.
	var got = planModeRaise("Port1", VLANModeDisabled, members, 100, false, protected, false)
	if !got.Change || got.Mode != VLANModeEnabled {
		t.Fatalf("a port that is off and without a link must be woken up to enabled: %+v", got)
	}

	// Port WITH a link, member of everything that must leave it: waking it
	// stays safe.
	got = planModeRaise("SFP2", VLANModeDisabled, members, 1, true, protected, false)
	if !got.Change || got.Mode != VLANModeEnabled {
		t.Fatalf("an active port that is a member of its native VLAN AND of the management "+
			"VLAN can be woken up: %+v", got)
	}
}

// TestFilteringNotWokenUpIfItWouldCut — the wake-up guard, in both its cases.
//
// `enabled` filters on EGRESS: setting it on a port that is not a member of a
// VLAN drops that VLAN on that port. On a port that carries a link, that
// means cutting — and if that port carries the session, cutting the
// administration of a device that has neither SSH, nor Telnet, nor a serial
// port.
//
// The refusal is RETURNED, not silent: without it, the operator would see
// "conforming" on a port whose configuration stays decorative, and would have
// no lead.
func TestFilteringNotWokenUpIfItWouldCut(t *testing.T) {
	var protected = map[int]string{99: "management VLAN declared by the device"}

	// CASE 1: not a member of its own native VLAN. The port's untagged
	// traffic enters and no longer leaves.
	var got = planModeRaise("SFP2", VLANModeDisabled, map[int]bool{99: true}, 100,
		true, protected, false)
	if got.Change {
		t.Errorf("filtering was enabled on an active port that is not a member of its native "+
			"VLAN: its untagged traffic would be dropped (mode %d)", got.Mode)
	}
	var expected string
	for _, expected = range []string{"SFP2", "100", "WITHOUT EFFECT", "ACTIVE link"} {
		if !strings.Contains(got.Refused, expected) {
			t.Errorf("the refusal must contain %q; got: %q", expected, got.Refused)
		}
	}

	// CASE 2: not a member of the MANAGEMENT VLAN. This is the serious case —
	// the outage would only be visible from meshcraft's side, the site's
	// traffic continuing to switch.
	got = planModeRaise("SFP2", VLANModeDisabled, map[int]bool{100: true}, 100,
		true, protected, false)
	if got.Change {
		t.Errorf("filtering was enabled on an active port that is not a member of the "+
			"management VLAN: administration would drop (mode %d)", got.Mode)
	}
	for _, expected = range []string{"management", "99", "serial"} {
		if !strings.Contains(got.Refused, expected) {
			t.Errorf("the refusal must contain %q; got: %q", expected, got.Refused)
		}
	}

	// `force` lifts this guard: an explicit gesture from the operator, who
	// knows their topology better than meshcraft.
	got = planModeRaise("SFP2", VLANModeDisabled, map[int]bool{}, 100, true, protected, true)
	if !got.Change || got.Mode != VLANModeEnabled {
		t.Errorf("force must lift the wake-up guard: %+v", got)
	}
}

// TestWakeUpDecidedOnTheArrivalMembership: the write declares THEN filters, in
// that order, and the computation must follow the same order.
//
// The wake-up guard is computed on the membership AS IT WILL BE — the table is
// already completed when PlanPorts decides. Judging it on the BEFORE
// membership would refuse to wake a port's filtering at the very moment the
// write gives it the memberships that make it safe: the port would stay
// `disabled` forever, and every reconciliation pass would reproduce the same
// refusal.
func TestWakeUpDecidedOnTheArrivalMembership(t *testing.T) {
	var current, rows = referencePlan(t)
	// The device has filtering off on all its ports.
	var modes = make([]uint32, 26)

	// A brand-new VLAN 42, and SFP2 (the port carrying the link) joining it
	// tagged while keeping its native 1 and its 99.
	var table, _, err = withVLANs(rows, []VLAN{{ID: 42}}, 26, nil)
	if err != nil {
		t.Fatalf("completion refused: %v", err)
	}
	var plan, planErr = PlanPorts(PortsInput{
		Current: current, Table: table, Facade: facadeCSS326(), Modes: modes,
		Desired: map[string]PortState{
			"SFP2": {Mode: "trunk", PVID: 1, Tagged: []int{42, 99, 100}},
		},
		Protected: map[int]string{99: "management VLAN declared by the device"},
	})
	if planErr != nil {
		t.Fatalf("plan refused: %v", planErr)
	}
	if len(plan.Skipped) != 0 {
		t.Errorf("no refusal expected: %v", plan.Skipped)
	}
	if len(plan.Changes) != 1 {
		t.Fatalf("%d change(s), expected 1: %+v", len(plan.Changes), plan.Changes)
	}
	if !plan.Changes[0].RaiseMode || plan.Changes[0].Mode != VLANModeEnabled {
		t.Errorf("filtering should have been woken up on SFP2: %+v", plan.Changes[0])
	}
	if !reflect.DeepEqual(plan.Changes[0].Join, []int{42}) {
		t.Errorf("VLANs to join = %v, expected [42]", plan.Changes[0].Join)
	}
}

// TestMissingModesDoNotPreventTheWrite: a firmware that does not serve `vlan`
// must not make a membership write fail.
//
// Refusing the whole write because filtering cannot be woken up would mean
// refusing to configure a switch because not everything can be configured.
func TestMissingModesDoNotPreventTheWrite(t *testing.T) {
	var current, rows = referencePlan(t)
	var plan, err = PlanPorts(PortsInput{
		Current: current, Table: rows, Facade: facadeCSS326(), Modes: nil,
		Desired:   map[string]PortState{"Port1": {Mode: "access", PVID: 99}},
		Protected: map[int]string{99: "plan"},
	})
	if err != nil {
		t.Fatalf("plan refused for lack of a mode array: %v", err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].PVID != 99 {
		t.Fatalf("membership must remain settable: %+v", plan.Changes)
	}
	if plan.Changes[0].RaiseMode {
		t.Error("no mode may be written when the firmware does not serve the field")
	}
}

// --- The management VLAN, never orphaned ---

// TestManagementVLANAlwaysInTheTable: the site plan is supposed to declare
// it, nothing guarantees it.
//
// A site imported from a hand-made file, a device whose `avln` designates a
// VLAN nobody modelled: the write then set everything else and left out of
// the table precisely the VLAN it arrived through.
func TestManagementVLANAlwaysInTheTable(t *testing.T) {
	var got = wantedVLANs(
		map[string]PortState{"Port1": {Mode: "access", PVID: 20}},
		[]VLAN{{ID: 20, Name: "users"}},
		map[int]string{7: "management VLAN from the site plan",
			99: "management VLAN declared by the device"})

	var expected = []VLAN{{ID: 7}, {ID: 20, Name: "users"}, {ID: 99}}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("wanted VLANs = %+v, expected %+v: BOTH management VLANs must be "+
			"there, the plan's as well as the one the device declares", got, expected)
	}
	// The reason meant for the operator must NOT become a VLAN name on the
	// device: `nm` is 16 bytes, and nobody chose that label.
	var v VLAN
	for _, v = range got {
		if v.ID != 20 && v.Name != "" {
			t.Errorf("VLAN %d received the name %q, which is a UI reason", v.ID, v.Name)
		}
	}
}

// TestManagementRowCreatedWithTheActivePorts — the least intuitive trap of
// this platform.
//
// Creating a VLAN row on SwOS MOVES that VLAN from the "unknown to the table"
// regime to the "declared, hence filtered" regime. On a port in `optional`
// mode — the factory default, and the state of all 26 ports of the capture —
// a VLAN the table does not declare flows freely; declaring it with an EMPTY
// member mask drops it everywhere, instantly.
//
// In other words: the quickest way to make a CSS mute is to neatly create the
// row of its management VLAN.
func TestManagementRowCreatedWithTheActivePorts(t *testing.T) {
	var current, rows = referencePlan(t)
	// The device says it is administered in VLAN 7, which does not exist in
	// its table.
	var protected = map[int]string{7: "management VLAN declared by the device"}

	var seed = mgmtSeed(current, protected, rows, facadeCSS326())
	// SFP2 is the ONLY port carrying a link in the capture (/link.b lnk:0x02000000).
	if !reflect.DeepEqual(seed, map[int][]int{7: {25}}) {
		t.Fatalf("seed = %v, expected {7: [25]}: only SFP2 carries a link", seed)
	}

	var out, created, err = withVLANs(rows, []VLAN{{ID: 7}}, 26, seed)
	if err != nil {
		t.Fatalf("completion refused: %v", err)
	}
	if !reflect.DeepEqual(created, []int{7}) {
		t.Fatalf("VLANs created = %v, expected [7]", created)
	}
	var row = rowFor(out, 7)
	if row == nil {
		t.Fatal("the VLAN 7 row was not created")
	}
	var member, ok = row.Bit(FieldVLANMembers, 25)
	if !ok || !member {
		t.Error("the management VLAN row was created WITHOUT the active port: on a " +
			"port in optional mode, declaring a VLAN without members drops it — that is " +
			"exactly the gesture that cuts access")
	}
	// Ports WITHOUT a link do not enter it: the seed is broad, not total.
	member, ok = row.Bit(FieldVLANMembers, 0)
	if !ok || member {
		t.Error("Port1 carries no link: it has no business entering the management VLAN")
	}
}

// TestNoSeedWhenTheManagementVLANAlreadyExists: the membership the operator
// set is not ours.
func TestNoSeedWhenTheManagementVLANAlreadyExists(t *testing.T) {
	var current, rows = referencePlan(t)
	// VLAN 99 IS in the capture's table.
	var seed = mgmtSeed(current, map[int]string{99: "plan"}, rows, facadeCSS326())
	if len(seed) != 0 {
		t.Errorf("seed = %v, expected empty: VLAN 99 is already declared, its "+
			"membership is the operator's", seed)
	}
	// VLAN 1 NEVER enters the table, even as a management VLAN: creating it
	// would change the behaviour of all the device's untagged traffic.
	seed = mgmtSeed(current, map[int]string{1: "plan"}, rows, facadeCSS326())
	if len(seed) != 0 {
		t.Errorf("seed = %v, expected empty: VLAN 1 is the hardware's implicit "+
			"default and has no row", seed)
	}
}

// --- The native-VLAN guard FREEZES the whole port ---

// TestASparedPortIsNotHalfReconfigured
//
// A version that refused only the native-VLAN change and continued with
// membership produced exactly the anomaly the state read flags: a port whose
// native VLAN stays 99 and whose membership is rewritten for VLAN 100.
// Untagged traffic then enters on 99 on a port that is no longer a member of
// 99, and nothing comes out. On the port carrying the session, that is the
// exact outage the guard exists to prevent — it would have caused it itself.
func TestASparedPortIsNotHalfReconfigured(t *testing.T) {
	var current, rows = referencePlan(t)
	// SFP2 carries the link AND untagged administration: the classic case.
	current.Ports["SFP2"] = PortState{Mode: "access", PVID: 99}

	var plan, err = PlanPorts(PortsInput{
		Current: current, Table: rows, Facade: facadeCSS326(),
		Modes:     referenceModes(t),
		Desired:   map[string]PortState{"SFP2": {Mode: "access", PVID: 100}},
		Protected: map[int]string{99: "management VLAN from the site plan"},
	})
	if err != nil {
		t.Fatalf("plan refused: %v", err)
	}
	if len(plan.Changes) != 0 {
		t.Errorf("the spared port must receive NO write, not even a "+
			"membership: %+v", plan.Changes)
	}
	if len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0], "UNTAGGED") {
		t.Fatalf("a single refusal, the native one: %v", plan.Skipped)
	}
}

// --- The read: the three per-port fields the state kept quiet about ---

// TestNoAnomalyOnTheRealCapture: the premise, and it matters.
//
// The reference device is consistent — 26 ports members of VLANs 99 and 100,
// natives 100 and 1, `vlni` at "any" everywhere, `fvid` at zero. An anomaly
// function that flagged something here would be a function that cries all the
// time, hence a function one learns to stop reading.
func TestNoAnomalyOnTheRealCapture(t *testing.T) {
	var vlanRows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b: %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b: %v", err)
	}
	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 0 {
		t.Errorf("the reference device's configuration is consistent: %v", got)
	}
}

// TestAnomalyForceVLANID: the field that makes the whole faceplate lie.
//
// A port on "Force VLAN ID" puts ALL its incoming traffic into its native
// VLAN, tags included. It is therefore NOT a trunk, whatever the table says
// about its membership — and yet the UI displayed its list of tagged VLANs
// without reservation.
func TestAnomalyForceVLANID(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	// Port3 (index 2) switches to Force VLAN ID.
	if !fwd.SetBit(FieldForceVLAN, 2, true) {
		t.Fatal("the capture's fvid mask must be modifiable")
	}

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) == 0 {
		t.Fatal("a port on Force VLAN ID must be flagged")
	}
	var expected string
	for _, expected = range []string{"FORCE VLAN ID", "Port3", "NOT trunks",
		"never writes"} {
		if !strings.Contains(got[0], expected) {
			t.Errorf("the note must contain %q; got: %q", expected, got[0])
		}
	}
}

// TestAnomalyPVIDWithoutMembership: the SwOS equivalent of the CRS326's real
// fault.
//
// On SwOS, a frame leaves a port only if it is a member of the VLAN. A port
// whose native VLAN is not in its membership puts its untagged traffic into a
// VLAN nothing comes out of. The device accepts it without flagging anything.
func TestAnomalyPVIDWithoutMembership(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	// Port1 (index 0) leaves VLAN 100, which stays its native VLAN.
	var row = rowFor(vlanRows, 100)
	if row == nil || !row.SetBit(FieldVLANMembers, 0, false) {
		t.Fatal("the VLAN 100 member mask must be modifiable")
	}

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 1 {
		t.Fatalf("%d note(s), expected 1: %v", len(got), got)
	}
	var expected string
	for _, expected = range []string{"PVID WITHOUT MEMBERSHIP", "Port1", "leaves nowhere"} {
		if !strings.Contains(got[0], expected) {
			t.Errorf("the note must contain %q; got: %q", expected, got[0])
		}
	}
}

// TestAnomalyNativeVLANAbsentFromTheTable: not to be confused with VLAN 1.
//
// A native VLAN without a row in /vlan.b has no member: nothing can leave it
// towards another port. VLAN 1, for its part, is the hardware's implicit
// default and LEGITIMATELY has no row — the capture's two cages are in that
// case, and flagging them would make the note cry on every factory-fresh
// device.
func TestAnomalyNativeVLANAbsentFromTheTable(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	var dvid, ok = fwd.Nums(FieldDefaultVLAN)
	if !ok {
		t.Fatal("/fwd.b dvid unreadable")
	}
	dvid[0] = 300 // Port1 in native VLAN 300, absent from the table
	fwd.SetNums(FieldDefaultVLAN, dvid)

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 1 {
		t.Fatalf("%d note(s), expected 1: %v", len(got), got)
	}
	if !strings.Contains(got[0], "NATIVE VLAN ABSENT") || !strings.Contains(got[0], "Port1") {
		t.Errorf("unexpected note: %q", got[0])
	}
	// The two cages are in native VLAN 1 without a row: the note must NOT cite
	// them.
	if strings.Contains(got[0], "SFP1") || strings.Contains(got[0], "SFP2") {
		t.Errorf("VLAN 1 legitimately has no row: %q", got[0])
	}
}

// TestAnomalyInconsistentVLANReceive: the field meshcraft does not write but
// reads.
//
// An access port on "only tagged" drops all its traffic — a workstation
// plugged in there gets nothing through. A trunk on "only untagged" works in
// one direction only. Both are configured, active, and silent.
func TestAnomalyInconsistentVLANReceive(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	var recv, ok = fwd.Nums(FieldVLANReceive)
	if !ok {
		t.Fatal("/fwd.b vlni unreadable")
	}
	// Port1 is a trunk in the capture (native 100, member of 99): "only
	// untagged" is inconsistent there.
	recv[0] = 2
	fwd.SetNums(FieldVLANReceive, recv)

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	if len(got) != 1 {
		t.Fatalf("%d note(s), expected 1: %v", len(got), got)
	}
	if !strings.Contains(got[0], "TRUNK THAT ACCEPTS ONLY UNTAGGED") ||
		!strings.Contains(got[0], "Port1") {
		t.Errorf("unexpected note: %q", got[0])
	}

	// The other direction: an ACCESS port on "only tagged". It is made by
	// removing Port1 from VLAN 99, which makes it a pure access port in VLAN
	// 100.
	fwd, _ = ParseObject(capture(t, "fwd.b"))
	recv, _ = fwd.Nums(FieldVLANReceive)
	recv[0] = 1
	fwd.SetNums(FieldVLANReceive, recv)
	var row = rowFor(vlanRows, 99)
	if row == nil || !row.SetBit(FieldVLANMembers, 0, false) {
		t.Fatal("the VLAN 99 member mask must be modifiable")
	}

	got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	var found bool
	var note string
	for _, note = range got {
		if strings.Contains(note, "ACCESS PORT THAT ACCEPTS ONLY TAGGED") &&
			strings.Contains(note, "Port1") {
			found = true
		}
	}
	if !found {
		t.Errorf("an access port on \"only tagged\" must be flagged: %v", got)
	}
}

// TestAnomalyStrictWithoutMembership: the harshest mode, on a port it makes
// mute.
//
// `strict` checks membership ON INGRESS. A port on strict that is not a
// member of its own native VLAN drops its untagged traffic on receive.
func TestAnomalyStrictWithoutMembership(t *testing.T) {
	var vlanRows, _ = ParseTable(capture(t, "vlan.b"))
	var fwd, _ = ParseObject(capture(t, "fwd.b"))
	var modes, ok = fwd.Nums(FieldVLANMode)
	if !ok {
		t.Fatal("/fwd.b vlan unreadable")
	}
	modes[0] = VLANModeStrict
	fwd.SetNums(FieldVLANMode, modes)
	var row = rowFor(vlanRows, 100)
	if row == nil || !row.SetBit(FieldVLANMembers, 0, false) {
		t.Fatal("the VLAN 100 member mask must be modifiable")
	}

	var got = vlanAnomalies(vlanRows, fwd, facadeCSS326())
	var found bool
	var note string
	for _, note = range got {
		if strings.Contains(note, "STRICT FILTERING WITHOUT MEMBERSHIP") &&
			strings.Contains(note, "Port1") {
			found = true
		}
	}
	if !found {
		t.Errorf("a port on strict that is not a member of its native VLAN must be flagged: %v", got)
	}
}

// TestAskedMembershipIncludesTheNative: on SwOS, a port must be a MEMBER of
// the VLAN that leaves it untagged.
//
// Confusing it with the list of tagged VLANs is what produces an orphaned
// PVID, hence the anomaly tested just above.
func TestAskedMembershipIncludesTheNative(t *testing.T) {
	var got = askedVLANs(PortState{Mode: "trunk", PVID: 100, Tagged: []int{99, 30}})
	if !reflect.DeepEqual(got, []int{30, 99, 100}) {
		t.Errorf("asked membership = %v, expected [30 99 100] sorted, native included", got)
	}
	// A native at zero means "unchanged", never "VLAN 0": SwOS has none.
	got = askedVLANs(PortState{Mode: "trunk", Tagged: []int{99}})
	if !reflect.DeepEqual(got, []int{99}) {
		t.Errorf("asked membership = %v, expected [99]: a native at zero does not exist", got)
	}
	// A VLAN both native and tagged counts only once.
	got = askedVLANs(PortState{Mode: "trunk", PVID: 99, Tagged: []int{99}})
	if !reflect.DeepEqual(got, []int{99}) {
		t.Errorf("asked membership = %v, expected [99] without duplicates", got)
	}
}

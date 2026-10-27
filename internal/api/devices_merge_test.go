package api

// PATCH /api/devices/{id} — the MERGE, i.e. the one judgement in that handler that
// can cost a device its supervision (cf. devices_handlers.go, mergeDeviceInput).
//
// WHAT THESE TESTS CAN AND CANNOT REACH, said plainly so a green run is not
// over-read: handleUpdateDevice opens on deviceForWrite, so Server.Store — a
// concrete *store.Store — makes it undrivable without PostgreSQL, which this package
// deliberately avoids (cf. dhcpsnoop_apply_test.go's and connector_reject_test.go's
// headers). The handler is therefore never ENTERED here. What is asserted instead is
// the decision that was wrong, lifted out of it into a pure function:
//
//   - which JSON keys a body actually carried — deviceInput.UnmarshalJSON, decoded
//     through encoding/json exactly as the route's `decode` does;
//   - the merge itself: absent leaves, explicit-zero clears, supplied overwrites;
//   - the incident of 2026-08-10, pinned field by field.
//
// What remains OUT of reach without a database: that the handler CALLS this merge
// and calls it before UpdateDevice, the 400 on an explicitly empty name (it responds
// after deviceForWrite has already needed the database), and the two side-effect
// comparisons — `updated.MgmtIP != mgmtIPBefore` (re-push watch, forget the observed
// address) and `updated.Name != nameBefore` (push the hostname). Those comparisons
// read fields this merge produces, so what is proven here is their INPUT: a body
// that never mentions the management address comes out of the merge with the stored
// address unchanged, which is precisely what keeps them silent. That the handler
// then does the right thing with them is not proven here.
//
// No fixture is device output: these are values a browser sends and a database
// holds. Nothing is CAPTURED and nothing pretends to be.

import (
	"encoding/json"
	"reflect"
	"testing"

	"meshcraft/internal/model"
)

// decodeDeviceInput runs a body through the same decoder the route uses.
//
// The presence set only exists after UnmarshalJSON has run, so a deviceInput built
// as a Go literal would test nothing: every test here must start from BYTES.
func decodeDeviceInput(t *testing.T, body string) deviceInput {
	t.Helper()
	var in deviceInput
	var err = json.Unmarshal([]byte(body), &in)
	if err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return in
}

// storedDevice: a device as the database holds it — every field populated, so any
// field the merge wrongly touches shows up as a change rather than as a zero
// matching a zero.
func storedDevice() model.Device {
	var group = "1f2e3d4c-0000-4000-8000-000000000001"
	return model.Device{
		ID:         "9a8b7c6d-0000-4000-8000-00000000000d",
		SiteID:     "5e4d3c2b-0000-4000-8000-000000000002",
		GroupID:    &group,
		Name:       "lab-crs326",
		Model:      "CRS326-24G-2S+",
		Vendor:     "MikroTik",
		Location:   "rack B, level 3",
		MgmtIP:     "192.0.2.11",
		Status:     "ok",
		StatusNote: "ping ok (proxy-site2)",
		PoEBudgetW: 60,
		PosX:       412.5,
		PosY:       260.25,
		PortGroups: json.RawMessage(`[{"kind":"ge","count":24}]`),
	}
}

// --- which keys the body carried ---

func TestDeviceInputRemembersOnlyTheKeysTheBodySent(t *testing.T) {
	var in = decodeDeviceInput(t, `{"name":"lab-crs326-rn","location":""}`)
	if !in.has("name") || !in.has("location") {
		t.Fatalf("keys written by the client not recorded: %+v", in.sent)
	}
	// `location` arrived EMPTY and must still count as sent: that is the whole
	// distinction the fix rests on.
	if in.Location != "" {
		t.Fatalf("location = %q", in.Location)
	}
	var absent string
	for _, absent = range []string{"mgmt_ip", "model", "vendor", "poe_budget_w", "pos_x", "pos_y", "group_id"} {
		if in.has(absent) {
			t.Fatalf("%q reported as sent, it was not in the body", absent)
		}
	}
}

func TestDeviceInputCountsAnExplicitNullAsSent(t *testing.T) {
	// Detaching a device from its group is `"group_id": null`, and it is what both
	// site screens send for an ungrouped device. A shape that could not tell this
	// apart from an absent key — pointer fields, for instance — would turn the
	// gesture into a silent no-op.
	var in = decodeDeviceInput(t, `{"group_id":null}`)
	if !in.has("group_id") {
		t.Fatal("an explicit null must count as sent")
	}
	if in.GroupID != nil {
		t.Fatalf("group_id = %v", *in.GroupID)
	}
}

// --- the merge ---

func TestMergeDeviceInputLeavesAbsentFieldsAlone(t *testing.T) {
	var stored = storedDevice()
	var merged = mergeDeviceInput(stored, decodeDeviceInput(t, `{}`))
	if !reflect.DeepEqual(merged, stored) {
		t.Fatalf("an empty body changed the record:\n stored = %+v\n merged = %+v", stored, merged)
	}
}

func TestMergeDeviceInputOverwritesWhatTheBodySupplies(t *testing.T) {
	var group = "7c6b5a49-0000-4000-8000-000000000003"
	var merged = mergeDeviceInput(storedDevice(), decodeDeviceInput(t, `{
		"group_id":"7c6b5a49-0000-4000-8000-000000000003",
		"name":"lab-crs326-rn","model":"CRS328-24P-4S+","vendor":"Mikrotik",
		"location":"rack A","mgmt_ip":"192.0.2.71",
		"poe_budget_w":450,"pos_x":10.5,"pos_y":-3.25
	}`))
	if merged.GroupID == nil || *merged.GroupID != group {
		t.Fatalf("group_id = %v", merged.GroupID)
	}
	if merged.Name != "lab-crs326-rn" || merged.Model != "CRS328-24P-4S+" || merged.Vendor != "Mikrotik" {
		t.Fatalf("identity = %q / %q / %q", merged.Name, merged.Model, merged.Vendor)
	}
	if merged.Location != "rack A" || merged.MgmtIP != "192.0.2.71" {
		t.Fatalf("location/address = %q / %q", merged.Location, merged.MgmtIP)
	}
	if merged.PoEBudgetW != 450 || merged.PosX != 10.5 || merged.PosY != -3.25 {
		t.Fatalf("budget/position = %d / %v / %v", merged.PoEBudgetW, merged.PosX, merged.PosY)
	}
}

func TestMergeDeviceInputStillClearsWhatIsSentEmpty(t *testing.T) {
	// Erasing on purpose has to keep working, or the fix would only have swapped one
	// unwritable state for another: a location one removes, a PoE budget one resets,
	// a group one detaches, a device pushed back to the plan's origin.
	var merged = mergeDeviceInput(storedDevice(), decodeDeviceInput(t,
		`{"location":"","poe_budget_w":0,"group_id":null,"pos_x":0,"pos_y":0}`))
	if merged.Location != "" {
		t.Fatalf("location = %q, an emptied location must be stored empty", merged.Location)
	}
	if merged.PoEBudgetW != 0 {
		t.Fatalf("poe_budget_w = %d", merged.PoEBudgetW)
	}
	if merged.GroupID != nil {
		t.Fatalf("group_id = %v, a detached device must lose its group", *merged.GroupID)
	}
	if merged.PosX != 0 || merged.PosY != 0 {
		t.Fatalf("position = %v / %v", merged.PosX, merged.PosY)
	}
	// And the erasure is CONFINED to what was named: the address is still there.
	if merged.MgmtIP != "192.0.2.11" || merged.Name != "lab-crs326" {
		t.Fatalf("collateral damage: mgmt_ip = %q, name = %q", merged.MgmtIP, merged.Name)
	}
}

// TestRenamingLabCrs326MustNotDropItOutOfSupervision pins the incident of
// 2026-08-10.
//
// `{"name":"lab-crs326-rn"}` — one lab switch, one rename — was sent to a handler
// that assigned every field of the record from the decoded body. It zeroed
// `mgmt_ip`, `model`, `vendor`, `location`, `poe_budget_w`, `pos_x` and `pos_y`.
// Within two minutes: `snmp.apply_failed` ("this device has no known management
// address — supervision will not start until it is pushed"), then
// `device.unreachable`, then `devices.status` at `off`. The identity fields were
// re-typed by hand; the map position was UNRECOVERABLE, nothing in the schema
// snapshots it.
//
// The working notes asked an operator to "rename lab-crs326 in meshcraft" as a test gesture, so
// following the project's own documentation was enough to trigger it. Hence a test
// per lost field rather than one DeepEqual: when this breaks again, the failure has
// to name the column that vanished.
func TestRenamingLabCrs326MustNotDropItOutOfSupervision(t *testing.T) {
	var stored = storedDevice()
	var merged = mergeDeviceInput(stored, decodeDeviceInput(t, `{"name":"lab-crs326-rn"}`))

	if merged.Name != "lab-crs326-rn" {
		t.Fatalf("the rename itself did not happen: name = %q", merged.Name)
	}
	// The address is the one that broke supervision: without it the SNMP push has
	// nobody to talk to and ICMP watch has nobody to ping.
	if merged.MgmtIP != stored.MgmtIP {
		t.Fatalf("mgmt_ip = %q, was %q — a rename must not blank the management address",
			merged.MgmtIP, stored.MgmtIP)
	}
	if merged.Model != stored.Model || merged.Vendor != stored.Vendor {
		t.Fatalf("model/vendor = %q/%q, were %q/%q — they decide what the connector may do",
			merged.Model, merged.Vendor, stored.Model, stored.Vendor)
	}
	// The plan position has no other copy anywhere: losing it loses it for good.
	if merged.PosX != stored.PosX || merged.PosY != stored.PosY {
		t.Fatalf("position = %v/%v, was %v/%v — nothing else in the schema holds it",
			merged.PosX, merged.PosY, stored.PosX, stored.PosY)
	}
	if merged.Location != stored.Location || merged.PoEBudgetW != stored.PoEBudgetW {
		t.Fatalf("location/budget = %q/%d, were %q/%d",
			merged.Location, merged.PoEBudgetW, stored.Location, stored.PoEBudgetW)
	}
	if merged.GroupID == nil || *merged.GroupID != *stored.GroupID {
		t.Fatalf("group_id = %v — the credential used to connect depends on it", merged.GroupID)
	}
	// What the handler's side-effect comparisons will read: only the name moved, so
	// `updated.MgmtIP != mgmtIPBefore` stays false — no watch re-push, no forgotten
	// observed address — while `updated.Name != nameBefore` is true and the hostname
	// reaches the equipment.
	if merged.MgmtIP != stored.MgmtIP || merged.Name == stored.Name {
		t.Fatal("the before/after comparisons would fire on the wrong field")
	}
}

func TestMergeDeviceInputAcceptsTheWholeRecordTheUISends(t *testing.T) {
	// Both call sites (web/src/pages/SwitchDetail.tsx, Plan.tsx) echo the full record
	// with the edited fields spread on top — including `status`/`status_note`, which
	// deviceInput dropped long ago. Merging must therefore be a no-op for everything
	// they resend unchanged, and this is what makes the fix backwards-compatible: the
	// UI needed no change.
	var stored = storedDevice()
	var merged = mergeDeviceInput(stored, decodeDeviceInput(t, `{
		"group_id":"1f2e3d4c-0000-4000-8000-000000000001",
		"name":"lab-crs326","model":"CRS326-24G-2S+","vendor":"MikroTik",
		"location":"rack B, level 3","mgmt_ip":"192.0.2.11",
		"status":"unknown","status_note":"",
		"poe_budget_w":60,"pos_x":412.5,"pos_y":901.0
	}`))
	// Moving the icon on the plan is the only change; the stale status the browser
	// read on page load goes nowhere.
	if merged.PosY != 901.0 {
		t.Fatalf("pos_y = %v, the plan move was lost", merged.PosY)
	}
	if merged.Status != stored.Status || merged.StatusNote != stored.StatusNote {
		t.Fatalf("status = %q/%q — reachability is observed, never posted back",
			merged.Status, merged.StatusNote)
	}
	stored.PosY = 901.0
	if !reflect.DeepEqual(merged, stored) {
		t.Fatalf("a full-record body changed more than the icon:\n want = %+v\n got  = %+v", stored, merged)
	}
}

func TestMergeDeviceInputKeepsTheFaceplateRuleItAlreadyHad(t *testing.T) {
	// port_groups was the ONE field the old handler already guarded, and it guarded
	// it by testing the raw bytes — nil when the key is absent. That is a presence
	// test in disguise, so it needed no change and is pinned here unchanged: an
	// absent key leaves the faceplate, a supplied one replaces it, INCLUDING `[]`,
	// which is how a faceplate is deliberately emptied.
	var stored = storedDevice()
	var untouched = mergeDeviceInput(stored, decodeDeviceInput(t, `{"name":"lab-crs326-rn"}`))
	if string(untouched.PortGroups) != string(stored.PortGroups) {
		t.Fatalf("port_groups = %s on a body that never mentioned it", untouched.PortGroups)
	}
	var emptied = mergeDeviceInput(stored, decodeDeviceInput(t, `{"port_groups":[]}`))
	if string(emptied.PortGroups) != `[]` {
		t.Fatalf("port_groups = %s, an emptied faceplate must land", emptied.PortGroups)
	}
	var replaced = mergeDeviceInput(stored, decodeDeviceInput(t, `{"port_groups":[{"kind":"sfp+","count":2}]}`))
	if string(replaced.PortGroups) != `[{"kind":"sfp+","count":2}]` {
		t.Fatalf("a supplied faceplate did not land: %s", replaced.PortGroups)
	}
}

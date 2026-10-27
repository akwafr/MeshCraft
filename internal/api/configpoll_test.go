package api

// The periodic config read-back's PURE decisions, exercised without a database,
// a session, or a device: the cadence an env var resolves to (configPollEvery),
// which devices earn an administration session and in what order
// (configPollWanted / configPollOrder), the per-device cooldown after a refusal
// (configPollState), and the two noise gates that decide what the rest of
// meshcraft hears (configPollDriftWorthEvent / observedFactsChanged).
//
// These are the decisions that would burn the fleet's session budget, or bury
// the event log, if they went wrong — the same standing poesample_test.go gives
// poeSampleWanted.

import (
	"testing"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

func TestConfigPollEvery(t *testing.T) {
	var cases = []struct {
		raw  string
		want time.Duration
	}{
		{"", configPollEveryDefault},
		{"0", 0},
		{"off", 0},
		{"45m", 45 * time.Minute},
		{"2h", 2 * time.Hour},
		// Below the floor: clamped, never honoured. One minute of full re-reads
		// would still be running when the next pass started.
		{"30s", configPollEveryFloor},
		{"1m", configPollEveryFloor},
		// A typo must NOT silently switch the fleet back to click-only refresh.
		{"every 30 minutes", configPollEveryDefault},
		{"-5m", 0},
	}
	var c struct {
		raw  string
		want time.Duration
	}
	for _, c = range cases {
		var got = configPollEvery(c.raw)
		if got != c.want {
			t.Errorf("configPollEvery(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// pollDevice builds a device whose observation is `age` old (nil = never read).
func pollDevice(id string, now time.Time, age time.Duration, read bool) model.Device {
	var d = model.Device{ID: id, MgmtIP: "192.0.2.4", Status: "ok"}
	if read {
		var at = now.Add(-age)
		d.ObservedAt = &at
	}
	return d
}

func TestConfigPollWanted(t *testing.T) {
	var now = time.Now()
	var every = 30 * time.Minute
	var cases = []struct {
		name string
		dev  model.Device
		want bool
	}{
		{"observation older than the cadence",
			pollDevice("a", now, 31*time.Minute, true), true},
		{"observation younger than the cadence — a click just did this work",
			pollDevice("b", now, 5*time.Minute, true), false},
		{"exactly one cadence old counts as due",
			pollDevice("c", now, every, true), true},
		{"never read: the extreme case this loop exists for",
			pollDevice("d", now, 0, false), true},
	}
	var c struct {
		name string
		dev  model.Device
		want bool
	}
	for _, c = range cases {
		if configPollWanted(c.dev, now, every) != c.want {
			t.Errorf("%s: configPollWanted = %v, want %v", c.name, !c.want, c.want)
		}
	}

	// Two refusals that cost nothing to test and a fleet's session budget to get
	// wrong, both stale enough to be wanted otherwise.
	var noAddr = pollDevice("e", now, time.Hour, true)
	noAddr.MgmtIP = ""
	if configPollWanted(noAddr, now, every) {
		t.Error("a device without a management address must not be read")
	}
	var off = pollDevice("f", now, time.Hour, true)
	off.Status = "off"
	if configPollWanted(off, now, every) {
		t.Error("a device every proxy has lost must not be read: guaranteed timeout")
	}
	var unknown = pollDevice("g", now, time.Hour, true)
	unknown.Status = "unknown"
	if !configPollWanted(unknown, now, every) {
		t.Error("nobody watching is not evidence our own session path is down")
	}
	// A never-read device with no address is still skipped: the bootstrap
	// exception is about the observation, not about reachability.
	var unreadNoAddr = pollDevice("h", now, 0, false)
	unreadNoAddr.MgmtIP = ""
	if configPollWanted(unreadNoAddr, now, every) {
		t.Error("never read AND no address: there is nothing to reach")
	}
}

func TestConfigPollOrderStalestFirst(t *testing.T) {
	var now = time.Now()
	var every = 30 * time.Minute
	// ListDevices hands them over sorted by NAME; the queue must ignore that.
	var devices = []model.Device{
		pollDevice("fresh", now, time.Minute, true),
		pollDevice("hour", now, time.Hour, true),
		pollDevice("never", now, 0, false),
		pollDevice("day", now, 24*time.Hour, true),
		pollDevice("twohours", now, 2*time.Hour, true),
	}
	var got = configPollOrder(devices, now, every)
	var want = []string{"never", "day", "twohours", "hour"}
	if len(got) != len(want) {
		t.Fatalf("queue = %d devices, want %d (the fresh one must be filtered out)",
			len(got), len(want))
	}
	var i int
	for i = range want {
		if got[i].ID != want[i] {
			t.Errorf("queue[%d] = %q, want %q — stalest first", i, got[i].ID, want[i])
		}
	}
}

func TestConfigPollOrderIsReproducible(t *testing.T) {
	// Same instant on several devices: the order must not depend on map or scan
	// order, or a pass that gets cut off cuts off a different tail every time.
	var now = time.Now()
	var at = now.Add(-time.Hour)
	var devices = []model.Device{
		{ID: "ccc", MgmtIP: "1.1.1.1", Status: "ok", ObservedAt: &at},
		{ID: "aaa", MgmtIP: "1.1.1.1", Status: "ok", ObservedAt: &at},
		{ID: "bbb", MgmtIP: "1.1.1.1", Status: "ok", ObservedAt: &at},
	}
	var got = configPollOrder(devices, now, 30*time.Minute)
	var want = []string{"aaa", "bbb", "ccc"}
	var i int
	for i = range want {
		if got[i].ID != want[i] {
			t.Fatalf("queue = %v, want ties broken on ID %v", got, want)
		}
	}
	// Two never-read devices tie the same way.
	var unread = []model.Device{
		{ID: "zzz", MgmtIP: "1.1.1.1", Status: "ok"},
		{ID: "yyy", MgmtIP: "1.1.1.1", Status: "ok"},
	}
	got = configPollOrder(unread, now, 30*time.Minute)
	if got[0].ID != "yyy" {
		t.Errorf("never-read tie = %q first, want yyy", got[0].ID)
	}
}

func TestConfigPollBackoffDoubles(t *testing.T) {
	var h configPollState
	var t0 = time.Now()

	if !h.due("d1", t0) {
		t.Fatal("a device never tried is due immediately")
	}
	// First failure: one cadence of silence, so a pass with ten dead devices
	// does not spend the next pass on them again.
	h.failed("d1", t0)
	if h.due("d1", t0.Add(configPollBackoffMin-time.Minute)) {
		t.Error("still inside the first cooldown")
	}
	if !h.due("d1", t0.Add(configPollBackoffMin+time.Minute)) {
		t.Error("first cooldown elapsed: due again")
	}
	// Second failure doubles it.
	h.failed("d1", t0)
	if h.due("d1", t0.Add(2*configPollBackoffMin-time.Minute)) {
		t.Error("second cooldown must be twice the first")
	}
	if !h.due("d1", t0.Add(2*configPollBackoffMin+time.Minute)) {
		t.Error("second cooldown elapsed: due again")
	}
	// And it stops at the ceiling — SIX hours here, not the twenty-four the
	// write loops use: this is a read, and it bounds a staleness promise.
	var i int
	for i = 0; i < 20; i++ {
		h.failed("d1", t0)
	}
	if h.due("d1", t0.Add(configPollBackoffMax-time.Minute)) {
		t.Error("cooldown must not exceed the ceiling from below")
	}
	if !h.due("d1", t0.Add(configPollBackoffMax+time.Minute)) {
		t.Errorf("cooldown grew past %v", configPollBackoffMax)
	}
	// A success clears it: a device that answered must not inherit its own past.
	h.succeeded("d1")
	if !h.due("d1", t0) {
		t.Error("success must clear the cooldown")
	}
	// And a cooldown is per device.
	h.failed("d1", t0)
	if !h.due("d2", t0) {
		t.Error("one device's cooldown must not silence another")
	}
}

func change(port, field, desired, observed string) recon.Change {
	return recon.Change{Port: port, Field: field, Desired: desired, Observed: observed}
}

func TestConfigPollDriftWorthEvent(t *testing.T) {
	var a = change("GE4", "pvid", "20", "30")
	var b = change("GE7", "mode", "trunk", "access")

	// The case this rule exists for: the SAME unarbitrated drift, pass after
	// pass. One event on the pass that found it, silence for ever after.
	if configPollDriftWorthEvent([]recon.Change{a}, []recon.Change{a}) {
		t.Error("an unchanged drift must not be re-announced every cadence")
	}
	// Order is not news: recon.DiffPorts walks a map, so two identical readings
	// arrive shuffled and a slice compare would announce phantom drift.
	if configPollDriftWorthEvent([]recon.Change{a, b}, []recon.Change{b, a}) {
		t.Error("the same set in another order is not news")
	}
	// Appearing, growing, shrinking: all news.
	if !configPollDriftWorthEvent(nil, []recon.Change{a}) {
		t.Error("drift appearing is news")
	}
	if !configPollDriftWorthEvent([]recon.Change{a}, []recon.Change{a, b}) {
		t.Error("another port drifting is news")
	}
	if !configPollDriftWorthEvent([]recon.Change{a, b}, []recon.Change{a}) {
		t.Error("drift partially resolving is news")
	}
	// Drift disappearing is silent: `config.drift` is the wrong event to carry
	// good news, and the badge turning green already says it.
	if configPollDriftWorthEvent([]recon.Change{a}, nil) {
		t.Error("no drift left: nothing to announce under config.drift")
	}
	if configPollDriftWorthEvent(nil, nil) {
		t.Error("no drift before, none after: silence")
	}
}

func TestObservedFactsChangedSeesShutPorts(t *testing.T) {
	// THE OPERATOR'S ORIGINAL COMPLAINT, as a unit test: somebody shuts GE3 at
	// the console. That is not drift — link state is a measurement and
	// recon.PortIntent excludes measurements — so if this predicate missed it,
	// the screens would still be repainted only by a click.
	var before = observedState{
		Links:  map[string]driver.LinkState{"GE3": driver.LinkUpState},
		LinkUp: map[string]bool{"GE3": true},
	}
	var after = observedState{
		Links:  map[string]driver.LinkState{"GE3": driver.LinkDisabled},
		LinkUp: map[string]bool{"GE3": false},
	}
	if !observedFactsChanged(before, after) {
		t.Fatal("a port shut at the console must wake the screens")
	}
	if observedFactsChanged(before, before) {
		t.Error("nothing moved: no notification, or this is a polling loop with extra steps")
	}
}

func TestObservedFactsChangedIgnoresListOrder(t *testing.T) {
	// VLAN lists arrive in whatever order the device enumerates them, and a
	// predicate that fired on that would wake every browser on most passes.
	var before = observedState{
		VLANs: []driver.VLAN{{ID: 10, Name: "a"}, {ID: 20, Name: "b"}},
		Ports: map[string]driver.PortState{
			"GE1": {Mode: "trunk", PVID: 1, Tagged: []int{10, 20}}},
		Links: map[string]driver.LinkState{"GE1": driver.LinkUpState},
	}
	var after = observedState{
		VLANs: []driver.VLAN{{ID: 20, Name: "b"}, {ID: 10, Name: "a"}},
		Ports: map[string]driver.PortState{
			"GE1": {Mode: "trunk", PVID: 1, Tagged: []int{20, 10}}},
		Links: map[string]driver.LinkState{"GE1": driver.LinkUpState},
	}
	if observedFactsChanged(before, after) {
		t.Error("the same observation enumerated differently is not a change")
	}
}

func TestObservedFactsChangedSeesMgmtVLANAndNewPorts(t *testing.T) {
	var base = observedState{Links: map[string]driver.LinkState{"GE1": driver.LinkUpState}}

	var moved = base
	moved.MgmtVLAN = 42
	if !observedFactsChanged(base, moved) {
		t.Error("the VLAN the device says it is administered through is not diffed anywhere else")
	}

	// A port appearing in the observation (stack member back, module reseated)
	// is a change even though no existing key moved.
	var grew = observedState{Links: map[string]driver.LinkState{
		"GE1": driver.LinkUpState, "GE2": driver.LinkDown}}
	if !observedFactsChanged(base, grew) {
		t.Error("a port appearing in the observation must wake the screens")
	}

	// A device never read before: everything is new.
	if !observedFactsChanged(observedState{}, base) {
		t.Error("a first observation is a change")
	}
}

func TestConfigPollNotes(t *testing.T) {
	// Only the per-capability notes, joined in a stable order so one pass's log
	// line can be compared with the next.
	var out = map[string]any{
		"stp_note": "spanning tree not read back: timeout",
		"poe_note": "PoE not read: unsupported",
		"ok":       true,
		"vlans":    []int{10},
		"steps":    nil,
	}
	var got = configPollNotes(out)
	var want = "PoE not read: unsupported | spanning tree not read back: timeout"
	if got != want {
		t.Errorf("configPollNotes = %q, want %q", got, want)
	}
	if configPollNotes(map[string]any{"ok": true}) != "" {
		t.Error("a clean read must produce no note text")
	}
}

// THE EVIDENCE THE 2026-08-11 INCIDENT DESTROYED.
//
// poefs3400lab's "show running-config" died some eight seconds in. The
// connector wrote that down as a failed step; the read still returned a nil
// error, so refreshDeviceConfig took its happy path and the whole transcript
// went out at Debug, which nothing was running at. `out` is a local map — a step
// not logged there is gone — and WHY that session died is still unknown because
// of it.
//
// Only the FAILURES: a healthy transcript is a dozen "authenticated", "model
// recognized" lines, and printing all of them per device per pass is how a log
// stops being read. An empty result is what the caller tests to decide whether
// to emit the line at all, so "nothing wrong" must be indistinguishable from
// "no transcript".
func TestConfigPollFailedSteps(t *testing.T) {
	var out = map[string]any{
		"steps": []driver.Step{
			{What: "authenticated", Detail: "mcadmin", OK: true},
			{What: "show running-config failed",
				Detail: "\"show running-config\" the read did not end at a CLI prompt", OK: false},
			{What: "show vlan TRUNCATED — membership read is partial", OK: false},
		},
	}
	var got = configPollFailedSteps(out)
	var want = "show running-config failed: \"show running-config\" the read did not end at " +
		"a CLI prompt | show vlan TRUNCATED — membership read is partial"
	if got != want {
		t.Errorf("configPollFailedSteps =\n %q\nwant\n %q", got, want)
	}

	// A read with nothing wrong in it says nothing, so the caller stays quiet.
	if configPollFailedSteps(map[string]any{
		"steps": []driver.Step{{What: "authenticated", OK: true}},
	}) != "" {
		t.Error("a clean transcript must produce no warning text")
	}
	// And a connector that supplied no transcript at all is not an incident.
	if configPollFailedSteps(map[string]any{"ok": true}) != "" {
		t.Error("an absent transcript must produce no warning text")
	}
	if configPollFailedSteps(map[string]any{"steps": nil}) != "" {
		t.Error("a nil transcript must produce no warning text")
	}
}

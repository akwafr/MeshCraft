// Tests of the alerting POLICY — ADR-0024. No database, no clock, no network:
// everything here exercises the pure functions of alerting.go, which is why
// they were written as pure functions.
//
// What is locked: the exclusion rule (a port with neither description nor known
// neighbour wakes nobody), the flap arithmetic (six transitions produce one
// message), and the self-attribution (we do not alert on what we just did).
package api

import (
	"testing"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

func portEvent(t string, port string, sev int) model.Event {
	var site = "s1"
	var dev = "d1"
	return model.Event{SiteID: &site, DeviceID: &dev, PortName: port, Type: t, Severity: sev}
}

func deviceEvent(t string, sev int) model.Event {
	var site = "s1"
	var dev = "d1"
	return model.Event{SiteID: &site, DeviceID: &dev, Type: t, Severity: sev}
}

// THE RULE OF ADR-0024, and the one a newcomer will propose reverting in good
// faith. A port carrying no operator description and facing nothing meshcraft
// knows produces no alert — not a low-severity one, not a grouped one, none.
func TestPortWithoutDescriptionDoesNotAlert(t *testing.T) {
	var cas = []struct {
		name string
		pf   portFacts
		want string
	}{
		{"neither described nor interco — THE RULE", portFacts{}, ""},
		{"described only", portFacts{Described: true}, model.AlertClassDescribed},
		{"interco only", portFacts{Interco: true}, model.AlertClassInterco},
		// Precedence: a cable between two switches carries everyone else's
		// traffic, so a channel subscribed to intercos only must not miss it
		// because somebody also typed a description.
		{"both — interco wins", portFacts{Described: true, Interco: true},
			model.AlertClassInterco},
	}
	var c struct {
		name string
		pf   portFacts
		want string
	}
	for _, c = range cas {
		var class, ok = classifyAlert(portEvent("port.down", "GE1", model.SevInfo), c.pf)
		if c.want == "" {
			if ok {
				t.Fatalf("%s: alert emitted although the port does not deserve one (class %q)",
					c.name, class)
			}
			continue
		}
		if !ok || class != c.want {
			t.Fatalf("%s: class %q ok=%v, expected %q", c.name, class, ok, c.want)
		}
	}
}

// A device event never passes through the port rules: there is no port to
// describe, and requiring one would make equipment outages unalertable.
func TestDeviceDoesNotGoThroughPortRules(t *testing.T) {
	var class, ok = classifyAlert(deviceEvent("device.unreachable", model.SevWarning), portFacts{})
	if !ok || class != model.AlertClassDevice {
		t.Fatalf("class %q ok=%v, expected %q", class, ok, model.AlertClassDevice)
	}
}

// The vocabulary is a SELECTION over events.type, never a vocabulary of its
// own (ADR-0024 rule 1). Anything outside the six is not alertable, including
// types that look alarming.
func TestClosedVocabulary(t *testing.T) {
	var outside = []string{
		"config.applied", "device.snmp_unreachable", "credential.installed",
		"proxy.disconnected", "stp.multi_root", "device.gnmi_failing",
	}
	var ty string
	for _, ty = range outside {
		var _, ok = classifyAlert(deviceEvent(ty, model.SevWarning),
			portFacts{Described: true, Interco: true})
		if ok {
			t.Fatalf("%q should not be alertable", ty)
		}
	}
	if len(alertableTypes) != 6 {
		t.Fatalf("%d alertable types, expected 6 — an addition must be a decision",
			len(alertableTypes))
	}
}

// An unresolved port cannot be judged, and guessing would be the credible false
// alert: a message naming a port that does not exist, or a homonym.
func TestUnresolvedPortStaysSilent(t *testing.T) {
	var _, ok = classifyAlert(portEvent("port.down", "", model.SevInfo),
		portFacts{Described: true, Interco: true})
	if ok {
		t.Fatal("a port without a resolved name must not alert")
	}
}

// TRAP NO. 1 — FLAPPING. Six transitions in two minutes must produce ONE
// message. The arithmetic that guarantees it: each transition pushes the
// deadline back by a full hold, so the deadline is never reached while the port
// is moving.
func TestSixTransitionsOneMessage(t *testing.T) {
	var start = time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	var first = start
	var release, capped = holdRelease(first, start, alertHold, alertHoldMax)
	if capped {
		t.Fatal("the first hold must not be capped")
	}
	// Six flips, 20 s apart — the "six times in two minutes" of the brief.
	var i int
	var now = start
	for i = 1; i < 6; i++ {
		now = start.Add(time.Duration(i) * 20 * time.Second)
		if !now.Before(release) {
			t.Fatalf("transition %d at %v: the deadline %v had already passed, "+
				"a message would have gone out in the middle of the flapping", i, now, release)
		}
		release, capped = holdRelease(first, now, alertHold, alertHoldMax)
		if capped {
			t.Fatalf("transition %d capped too early", i)
		}
	}
	// Nothing was delivered during the storm, and the deadline now sits after
	// the last flip.
	if !release.After(now) {
		t.Fatalf("after the last transition the deadline %v should follow %v", release, now)
	}
}

// The cap is the backstop, and it matters because of a real hole: gNMI-collected
// ports never get the `port.flapping` verdict that would otherwise release
// them, so without the cap such a port would be retained by its own
// instability forever.
func TestCapReleasesAPortThatFlapsForever(t *testing.T) {
	var start = time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	var release time.Time
	var capped bool
	// A transition every 10 s for a quarter of an hour.
	var i int
	for i = 1; i <= 90; i++ {
		release, capped = holdRelease(start, start.Add(time.Duration(i)*10*time.Second),
			alertHold, alertHoldMax)
		if capped {
			break
		}
	}
	if !capped {
		t.Fatal("a port that flaps forever must end up speaking: the cap did not bite")
	}
	if !release.Equal(start.Add(alertHoldMax)) {
		t.Fatalf("deadline capped at %v, expected %v", release, start.Add(alertHoldMax))
	}
}

// NO class escapes the hold, and that is a correction.
//
// Zero for the device was the first answer, in the name of latency. It let
// through exactly the storm of trap no. 1: detection is asymmetric — three
// ICMP losses to go down (~2.75 s), a single reply to come back up — so a
// device behind a flapping link produces a down/up pair every few seconds, each
// one a real status change, each one a message. 20 s close the door without a
// notification reader being able to tell 3 s from 23 s.
func TestNoClassEscapesTheHold(t *testing.T) {
	var classes = []string{model.AlertClassDevice, model.AlertClassInterco,
		model.AlertClassDescribed}
	var c string
	for _, c = range classes {
		if holdFor(c) <= 0 {
			t.Fatalf("class %q without a hold: a flapping device or port "+
				"would produce one message per cycle", c)
		}
	}
	// A device waits less than a port: its detection is already debounced, and
	// it is the alert whose latency matters most.
	if holdFor(model.AlertClassDevice) >= holdFor(model.AlertClassInterco) {
		t.Fatalf("device %v, port %v: the device must wait LESS",
			holdFor(model.AlertClassDevice), holdFor(model.AlertClassInterco))
	}
	// And every hold must stay well under the cap, or the cap would bind on the
	// very first event and the coalescing would never happen.
	for _, c = range classes {
		if holdFor(c) >= alertHoldMax {
			t.Fatalf("class %q: hold %v >= cap %v, grouping "+
				"would never happen", c, holdFor(c), alertHoldMax)
		}
	}
}

// A flapping device is grouped like a port: same rule, same arithmetic, only
// the duration changes.
func TestAFlappingDeviceIsGrouped(t *testing.T) {
	var start = time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC)
	var hold = holdFor(model.AlertClassDevice)
	var release, _ = holdRelease(start, start, hold, alertHoldMax)
	// Down at 2.75 s, up at 3 s, down at 5.75 s… every cycle lands inside the
	// hold, so nothing is delivered while the device is still oscillating.
	var i int
	var now time.Time
	for i = 1; i <= 6; i++ {
		now = start.Add(time.Duration(i) * 3 * time.Second)
		if !now.Before(release) {
			t.Fatalf("cycle %d at %v: deadline %v already passed, a message "+
				"would have gone out in the middle of the flapping", i, now, release)
		}
		release, _ = holdRelease(start, now, hold, alertHoldMax)
	}
	if !release.After(now) {
		t.Fatal("the deadline must follow the last transition")
	}
}

// TRAP NO. 2 — DO NOT ALERT ON YOUR OWN GESTURES. The measured scenario:
// eight switches had their MSTP region rewritten on the night of 2026-10-24.
func TestDoNotAlertOnYourOwnGesture(t *testing.T) {
	var now = time.Date(2026, 10, 25, 3, 10, 0, 0, time.UTC)
	var ev = portEvent("port.down", "GE1", model.SevInfo)
	ev.TS = now
	// The apply happened four minutes before the port bounced.
	var journal = []model.Event{
		{Type: "port.down", TS: now},
		{Type: "device.stp_apply", TS: now.Add(-4 * time.Minute)},
		{Type: "config.applied", TS: now.Add(-4*time.Minute - time.Second)},
	}
	var reason, attributed = attributedToGesture(ev, journal, now, selfActionWindow)
	if !attributed {
		t.Fatal("a transition 4 min after an STP apply must be attributed, not announced")
	}
	if reason != "device.stp_apply" {
		t.Fatalf("reason %q, expected device.stp_apply — the silence must stay explainable", reason)
	}
}

// Outside the window, the same transition IS news. Suppression delays bad news;
// it must not swallow it.
func TestOutsideTheWindowTheTransitionIsANewOne(t *testing.T) {
	var now = time.Date(2026, 10, 25, 3, 30, 0, 0, time.UTC)
	var ev = portEvent("port.down", "GE1", model.SevInfo)
	ev.TS = now
	var journal = []model.Event{
		{Type: "port.down", TS: now},
		{Type: "device.stp_apply", TS: now.Add(-20 * time.Minute)},
	}
	var _, attributed = attributedToGesture(ev, journal, now, selfActionWindow)
	if attributed {
		t.Fatal("a 20-minute-old gesture must no longer excuse a transition")
	}
}

// A gesture logged AFTER the event it would excuse explains nothing: the
// consequence cannot precede the cause. Without this guard, starting an apply
// just after an outage began would silence that outage.
func TestALaterGestureDoesNotExcuse(t *testing.T) {
	var now = time.Date(2026, 10, 25, 3, 10, 0, 0, time.UTC)
	var ev = portEvent("port.down", "GE1", model.SevInfo)
	ev.TS = now.Add(-2 * time.Minute)
	var journal = []model.Event{
		{Type: "device.stp_apply", TS: now},
		{Type: "port.down", TS: now.Add(-2 * time.Minute)},
	}
	var _, attributed = attributedToGesture(ev, journal, now, selfActionWindow)
	if attributed {
		t.Fatal("an outage that happened BEFORE the apply must not be attributed to that apply")
	}
}

// The gesture vocabulary must cover every write path that can bounce a port.
// A missing entry is a false alert blamed on a device when it was us, which is
// the failure the mechanism exists to prevent.
func TestGestureVocabularyCoversTheWrites(t *testing.T) {
	var mustBe = []string{
		"config.applied", "device.port_admin", "device.poe_apply",
		"device.stp_apply", "device.storm_control", "device.dhcp_snooping_apply",
		"mgmt.applied", "mgmt.migrated", "credential.installed",
		"device.adopted", "lag.applied", "switch_profile.applied",
	}
	var ty string
	for _, ty = range mustBe {
		if !gestureTypes[ty] {
			t.Fatalf("%q writes to a device and is missing from the gesture vocabulary: "+
				"its consequences would be announced as an outage", ty)
		}
	}
	// And the reachability types must NOT be gestures — that would suppress
	// every alert as its own cause.
	var ty2 string
	for ty2 = range alertableTypes {
		if gestureTypes[ty2] {
			t.Fatalf("%q is both alertable and a gesture: it would suppress itself", ty2)
		}
	}
}

// Subscription order: classes first, severity second. Reversing the two would
// let min_severity 0 deliver every ordinary port transition to a channel that
// only asked for intercos.
func TestSubscriptionClassesThenSeverity(t *testing.T) {
	var ch = model.NotifyChannel{
		Enabled: true, Classes: []string{model.AlertClassInterco}, MinSeverity: 0,
	}
	if channelWants(ch, model.AlertClassDescribed, model.SevCritical) {
		t.Fatal("an unsubscribed class does not pass, even when critical")
	}
	if !channelWants(ch, model.AlertClassInterco, model.SevInfo) {
		t.Fatal("the subscribed class passes at floor 0")
	}
	ch.MinSeverity = model.SevWarning
	if channelWants(ch, model.AlertClassInterco, model.SevInfo) {
		t.Fatal("the severity floor must filter within the subscribed class")
	}
	ch.Enabled = false
	if channelWants(ch, model.AlertClassInterco, model.SevCritical) {
		t.Fatal("a disabled channel receives nothing")
	}
}

// Backoff doubles from the floor and stops at the ceiling — a channel that is
// down stays down, and hammering it neither helps nor informs.
func TestBackoffDoublesAndCaps(t *testing.T) {
	if backoffAfter(1) != notifyBackoffMin {
		t.Fatalf("first wait %v, expected %v", backoffAfter(1), notifyBackoffMin)
	}
	if backoffAfter(2) != 2*notifyBackoffMin {
		t.Fatalf("second wait %v", backoffAfter(2))
	}
	if backoffAfter(50) != notifyBackoffMax {
		t.Fatalf("capped wait %v, expected %v", backoffAfter(50), notifyBackoffMax)
	}
	var i int
	for i = 1; i < 60; i++ {
		if backoffAfter(i) > notifyBackoffMax {
			t.Fatalf("wait %v exceeds the cap", backoffAfter(i))
		}
	}
}

// The alert key separates what must not absorb what. A device alert and a port
// alert on the same device are different stories.
func TestAlertKeySeparatesStories(t *testing.T) {
	var dev = deviceEvent("device.unreachable", model.SevWarning)
	var p1 = portEvent("port.down", "GE1", model.SevInfo)
	var p2 = portEvent("port.down", "GE2", model.SevInfo)
	var kDev = alertKeyOf(model.AlertClassDevice, dev)
	var k1 = alertKeyOf(model.AlertClassInterco, p1)
	var k2 = alertKeyOf(model.AlertClassInterco, p2)
	if kDev == k1 || k1 == k2 {
		t.Fatalf("keys conflated: %q %q %q", kDev, k1, k2)
	}
	// Same port, same class, two events: one key, so they coalesce.
	if alertKeyOf(model.AlertClassInterco, p1) != k1 {
		t.Fatal("the key must be stable for grouping to work")
	}
}

// The retry ceiling is ONE constant, shared with the store rather than copied:
// it appears in the due-query's WHERE clause and in the worker's decision to
// schedule another attempt, and the two must agree. This locks the alias.
func TestNotifyMaxAttemptsIsShared(t *testing.T) {
	if notifyMaxAttempts != store.NotifyMaxAttempts {
		t.Fatalf("notifyMaxAttempts = %d, store.NotifyMaxAttempts = %d — "+
			"the query and the worker would diverge",
			notifyMaxAttempts, store.NotifyMaxAttempts)
	}
	// A ceiling of zero would retry forever; one would never retry at all.
	if notifyMaxAttempts < 2 {
		t.Fatalf("cap of %d attempts: too low for a channel that stutters",
			notifyMaxAttempts)
	}
}

// THE BACKGROUND IMPORT MUST NOT BE ABLE TO WAKE ANYBODY (ADR-0029 §13).
//
// The site-extension import runs every 30 s now, and the journal is the
// alerting's only input (ADR-0024 rule 1). So "a product that alarms itself
// about what it just read" was a real question, and this pins down the answer:
// no `extension.*` type is in the allow-list. Adding one later fails here,
// which is the point — it should be a decision, not a side effect.
//
// The loop also inserts through Store.InsertEvent rather than `emit`, so it
// never reaches fileAlerts at all. That is a second, independent reason, and it
// is deliberately NOT the one this test guards: a later refactor that routed
// the loop through `emit` would silently come to rely on this list being clean.
func TestExtensionEventsAreNotAlertable(t *testing.T) {
	var emitted = []string{
		"extension.imported", "extension.unreachable", "extension.recovered",
		"extension.orphaned", "extension.configured", "extension.paired",
		"extension.adopted", "extension.forgotten", "extension.reprovisioned",
		"extension.ssid_edited",
	}
	var ty string
	for _, ty = range emitted {
		var _, found = alertableTypes[ty]
		if found {
			t.Errorf("%q is alertable: a 30 s import loop would wake people about "+
				"its own readings. If that is deliberate, ADR-0029 §13 has to say so first.", ty)
		}
	}
	// And the list has not quietly grown: six types, named in ADR-0024.
	if len(alertableTypes) != 6 {
		t.Errorf("alertableTypes holds %d types, want the 6 of ADR-0024; "+
			"a seventh is an ADR, not an edit", len(alertableTypes))
	}
}

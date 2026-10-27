package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/gnmipoll"
	"meshcraft/internal/tunnel"
)

// What this file tests: that a gNMI subscription which will not stay up becomes
// a FACT THE CONTROLLER RECEIVES, and that it does so at the right moment and
// carrying the right things.
//
// The defect it pins is core01.lab, 2026-08-10. Its `gnmi` credential went
// stale the instant the site admin credential was rotated — separate
// `site_credentials` rows, each with its own copy of the password — and the
// proxy resubscribed with the refused login for over an hour. Nothing reported
// it: the transport dropped the gRPC status in a Debug line, the supervisor read
// the closed channel as "the stream ended cleanly, the target is healthy", the
// controller logged `gnmi_targets:1`, and `events` was empty. Every surface an
// operator looks at said healthy, and each one was individually telling the
// truth.
//
// Four properties, and they pull against each other, which is why all four are
// here:
//
//   - BELOW the threshold, silence. A switch rebooting must not raise an alarm,
//     and an alarm that fires on every `write memory` is one an operator learns
//     to filter — after which the real one is invisible again, which is exactly
//     where we started;
//   - ABOVE it, exactly one report. Not none, and not one per retry;
//   - a subscription that recovers CLEARS, so the next genuine failure is
//     reported again and the previous one stops being restated;
//   - the credential appears NOWHERE. A gNMI login on this fleet configures the
//     switch, and an event is read on screen, exported, and kept.
//
// Time is INJECTED. The threshold is five minutes and the tests must not spend
// them; `noteSubFailing` and `noteSubHealthy` therefore take `now` as a
// parameter, with `time.Now()` written literally at the production call site in
// `supervise` — the doctrine internal/api/portevents.go states, applied here for
// the same reason.

// failT0: a fixed instant. Nothing in this file compares against a real clock.
var failT0 = time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)

// failKey is the key `supervise` builds, restated here so the tests exercise the
// same map entries production does.
const failKey = "dev-core01|if_counters"

// TestGNMIFailureBelowThresholdIsSilent: a device that is failing, and has not
// been failing long, produces nothing.
//
// This is the negative property, and negative properties rot quietly: nothing
// breaks when a threshold collapses to zero, the fleet log just fills with a
// line per reboot and stops being read. So it is checked at the two places it
// can go wrong — the first failure, and the instant BEFORE the threshold.
func TestGNMIFailureBelowThresholdIsSilent(t *testing.T) {
	var c, _ = gnmiTestCollector()
	var w = gnmiWatchFixture()

	// The first failure. A switch that has just started rebooting looks exactly
	// like a switch whose password is wrong, and at this instant the difference
	// is unknowable — so nothing is said.
	var ev = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth, failT0)
	if ev != nil {
		t.Fatalf("the FIRST failure was reported: %+v", ev)
	}

	// Every retry up to one second short of the threshold. Still nothing: this is
	// the window a reboot, a config save that bounces the gNMI agent, and a
	// management VLAN move all resolve inside.
	var at time.Time
	var step time.Duration
	for step = 30 * time.Second; step < gnmiFailReportAfter; step += 30 * time.Second {
		at = failT0.Add(step)
		ev = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth, at)
		if ev != nil {
			t.Fatalf("reported after %s, which is below the %s threshold: %+v",
				step, gnmiFailReportAfter, ev)
		}
	}
	ev = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth,
		failT0.Add(gnmiFailReportAfter-time.Second))
	if ev != nil {
		t.Fatalf("reported one second early: %+v", ev)
	}
}

// TestGNMIFailureAboveThresholdIsReportedOnce: cross the threshold and the fact
// goes out — once.
//
// The "once" half is as important as the report. A subscription that cannot
// authenticate fails on every retry for as long as nobody fixes it, and one
// event per retry at the 60 s backoff cap is sixty lines an hour in the fleet
// log of a site whose only problem is one stale password.
func TestGNMIFailureAboveThresholdIsReportedOnce(t *testing.T) {
	var c, _ = gnmiTestCollector()
	var w = gnmiWatchFixture()

	var i int
	for i = 0; i < 4; i++ {
		c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth,
			failT0.Add(time.Duration(i)*time.Minute))
	}
	// Minute five: the run of failures is now a state.
	var ev = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth,
		failT0.Add(gnmiFailReportAfter))
	if ev == nil {
		t.Fatalf("nothing reported after %s of continuous failure — this is the "+
			"silence the whole batch exists to remove", gnmiFailReportAfter)
	}
	if ev.Type != tunnel.EventGNMIFailing {
		t.Errorf("event type %q, want %q", ev.Type, tunnel.EventGNMIFailing)
	}
	if ev.DeviceID != w.DeviceID || ev.IP != w.IP {
		t.Errorf("event names %q/%q, want %q/%q", ev.DeviceID, ev.IP, w.DeviceID, w.IP)
	}
	if ev.Metric != "if_counters" {
		t.Errorf("metric %q: the report must say WHICH subscription died — a device "+
			"holds four and they fail independently", ev.Metric)
	}
	if ev.Cause != tunnel.CauseAuth {
		t.Errorf("cause %q, want %q: a refused login and a silent device are "+
			"repaired by different people", ev.Cause, tunnel.CauseAuth)
	}
	if ev.Misses != 5 {
		t.Errorf("attempts %d, want 5", ev.Misses)
	}

	// And now the retries that follow, for another quarter of an hour.
	var step time.Duration
	for step = gnmiFailReportAfter + time.Minute; step < 20*time.Minute; step += time.Minute {
		var again = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth,
			failT0.Add(step))
		if again != nil {
			t.Fatalf("reported a second time at %s: an event is a transition, not a "+
				"metric (%+v)", step, again)
		}
	}
}

// TestGNMIRecoveryClearsAndRearms: a subscription that comes back says so, once,
// and the state it leaves behind is clean.
//
// Both halves matter. Without the recovery line the fleet log holds an open
// alarm nothing ever closes, and an operator cannot tell a fault that is still
// live from one that was fixed on Tuesday. Without the CLEAR, the next genuine
// failure — a month later, a different cause — would be swallowed by a
// `reported` flag nobody reset.
func TestGNMIRecoveryClearsAndRearms(t *testing.T) {
	var c, _ = gnmiTestCollector()
	var w = gnmiWatchFixture()

	var i int
	for i = 0; i <= 5; i++ {
		c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth,
			failT0.Add(time.Duration(i)*time.Minute))
	}
	// The credential is corrected and the stream comes up.
	var rec = c.noteSubHealthy(failKey, w, "if_counters", failT0.Add(7*time.Minute))
	if rec == nil {
		t.Fatal("a reported failure that recovers must close itself: an alarm " +
			"nothing ever clears is an alarm nobody trusts")
	}
	if rec.Type != tunnel.EventGNMIRecovered {
		t.Errorf("recovery type %q, want %q", rec.Type, tunnel.EventGNMIRecovered)
	}
	if rec.Metric != "if_counters" {
		t.Errorf("recovery metric %q, want if_counters", rec.Metric)
	}
	// Said ONCE. Every subsequent healthy stream end is silent, and there are
	// many — a stream ends and is resubscribed all day.
	var quiet = c.noteSubHealthy(failKey, w, "if_counters", failT0.Add(8*time.Minute))
	if quiet != nil {
		t.Errorf("recovery restated on an ordinary stream end: %+v", quiet)
	}

	// RE-ARMED: a fresh run of failures is judged from its own start, not from
	// the old one, so it is silent again until it has lasted long enough.
	var t1 = failT0.Add(time.Hour)
	var ev = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndUnreachable, t1)
	if ev != nil {
		t.Fatalf("the first failure of a NEW run was reported instantly — the "+
			"recovery did not reset the clock: %+v", ev)
	}
	ev = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndUnreachable,
		t1.Add(gnmiFailReportAfter))
	if ev == nil {
		t.Fatal("a new run of failures is never reported: the recovery left the " +
			"state armed shut")
	}
	if ev.Cause != tunnel.CauseUnreachable {
		t.Errorf("cause %q, want %q: the report says what is true NOW, not what was "+
			"true during the previous run", ev.Cause, tunnel.CauseUnreachable)
	}
}

// TestGNMIUnreportedFailureRecoversSilently: a run of failures that never
// crossed the threshold ends with nothing said.
//
// The reboot case, and the one that decides whether this feature is usable at
// all: announcing the end of something nobody was told about reads as a fault,
// and would put two lines in the fleet log for every switch reload.
func TestGNMIUnreportedFailureRecoversSilently(t *testing.T) {
	var c, _ = gnmiTestCollector()
	var w = gnmiWatchFixture()

	c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndUnreachable, failT0)
	c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndUnreachable,
		failT0.Add(40*time.Second))
	var rec = c.noteSubHealthy(failKey, w, "if_counters", failT0.Add(70*time.Second))
	if rec != nil {
		t.Fatalf("a 70-second interruption produced a fleet log line: %+v", rec)
	}
	// A healthy stream that was never failing at all: the overwhelmingly common
	// path, and it must not even allocate an event.
	if c.noteSubHealthy(failKey, w, "if_counters", failT0.Add(2*time.Minute)) != nil {
		t.Error("an ordinary stream end spoke")
	}
}

// TestGNMIWatchPushResetsTheFailureClock: a re-push that restarts a subscription
// restarts its failure clock too.
//
// This is the repair case and it is the reason the reset exists: the push that
// carries a corrected password IS the operator fixing this condition, and
// reporting a fault five minutes into a repair that already worked would be
// worse than the silence — it would teach them the report is wrong.
//
// Driven through SetWatches rather than by poking the map, because what is being
// checked is that the reconcile remembers to do it; a test that deleted the
// entry itself would prove nothing.
func TestGNMIWatchPushResetsTheFailureClock(t *testing.T) {
	var c, rec = gnmiTestCollector()
	var w = gnmiWatchFixture()

	var ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	c.SetWatches(ctx, []tunnel.GNMIWatch{w})
	waitOpened(t, rec, 1)

	// It has been failing authentication for nearly the threshold.
	var i int
	for i = 0; i < 4; i++ {
		c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth,
			failT0.Add(time.Duration(i)*time.Minute))
	}
	// The operator re-enters the `gnmi` credential; the controller pushes it.
	var fixed = w
	fixed.Password = "after-the-rotation"
	c.SetWatches(ctx, []tunnel.GNMIWatch{fixed})
	waitOpened(t, rec, 2)

	c.mu.Lock()
	var _, held = c.fails[failKey]
	c.mu.Unlock()
	if held {
		t.Fatal("the failure clock survived a re-push that restarted the " +
			"subscription: a corrected credential would be reported as a fault " +
			"one minute later")
	}

	// And a device dropped from the watch list leaves nothing behind either.
	c.noteSubFailing(failKey, fixed, "if_counters", gnmipoll.EndAuth, failT0)
	c.SetWatches(ctx, nil)
	c.mu.Lock()
	_, held = c.fails[failKey]
	c.mu.Unlock()
	if held {
		t.Error("a device removed from the watch list kept its failure state")
	}
}

// TestGNMIFailEventNeverCarriesTheCredential is the sibling of
// TestGNMIParamsMovedNeverCarriesAValue, and it exists for the same reason one
// step further out.
//
// That test guards a LOG line. This one guards something that travels further: a
// tunnel event is written into `events`, rendered on the device sheet, exported,
// and kept. A gNMI login on this fleet CONFIGURES the switch rather than merely
// reading it (cf. tunnel.GNMIWatch), so neither the password nor "its length" nor
// "its first characters" may appear — and neither may the username, which is
// half of a credential and is exactly what a rotation changes.
//
// Checked on the SERIALISED event rather than field by field, so that a field
// added to tunnel.Event and populated from the watch is caught by this test
// instead of needing to be remembered in it.
func TestGNMIFailEventNeverCarriesTheCredential(t *testing.T) {
	var c, _ = gnmiTestCollector()
	var w = gnmiWatchFixture()
	// Distinctive enough that a substring search cannot match by accident, and
	// shaped like what the credential deployment actually writes.
	w.Username = "mcadmin-rotated-7f2a"
	w.Password = "Zx9-very-secret-password-41b"

	var events []tunnel.Event
	var i int
	for i = 0; i <= 6; i++ {
		var ev = c.noteSubFailing(failKey, w, "if_counters", gnmipoll.EndAuth,
			failT0.Add(time.Duration(i)*time.Minute))
		if ev != nil {
			events = append(events, *ev)
		}
	}
	var rec = c.noteSubHealthy(failKey, w, "if_counters", failT0.Add(9*time.Minute))
	if rec != nil {
		events = append(events, *rec)
	}
	if len(events) != 2 {
		t.Fatalf("%d events, want a failure and a recovery", len(events))
	}

	var forbidden = []string{w.Password, w.Username, "secret", "motdepasse"}
	var n int
	for n = range events {
		var blob, err = json.Marshal(events[n])
		if err != nil {
			t.Fatalf("marshalling %s: %v", events[n].Type, err)
		}
		var text = string(blob)
		var f string
		for _, f = range forbidden {
			if strings.Contains(strings.ToLower(text), strings.ToLower(f)) {
				t.Fatalf("%s carries %q — a gNMI login on this fleet configures the "+
					"switch, and this record is kept and exported: %s",
					events[n].Type, f, text)
			}
		}
	}
}

// TestGNMICauseVocabularyIsClosed: every classification the transport can
// produce maps onto a wire cause, and an unrecognised one degrades to "unknown"
// rather than to one of the two actionable classes.
//
// Locking the mapping matters because the failure is silent in the dangerous
// direction: a class that fell through to `auth` would send someone to rotate a
// credential that was never the problem, and they would find nothing wrong with
// it — which teaches them to distrust the report.
func TestGNMICauseVocabularyIsClosed(t *testing.T) {
	var cas = []struct {
		kind gnmipoll.EndKind
		want string
	}{
		{gnmipoll.EndAuth, tunnel.CauseAuth},
		{gnmipoll.EndUnreachable, tunnel.CauseUnreachable},
		{gnmipoll.EndUnknown, tunnel.CauseUnknown},
		// Never reported (a clean end is not a failure), and it must still not
		// land on an actionable class if it ever reaches here.
		{gnmipoll.EndClean, tunnel.CauseUnknown},
		// A class this build has never heard of — the shape of a proxy older than
		// the collector that classified.
		{gnmipoll.EndKind("something-new"), tunnel.CauseUnknown},
	}
	var i int
	for i = range cas {
		var got = gnmiCauseOf(cas[i].kind)
		if got != cas[i].want {
			t.Errorf("%q maps to %q, want %q", cas[i].kind, got, cas[i].want)
		}
	}
}

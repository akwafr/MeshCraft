package api

// The background import's two invariants that can be tested without a
// controller or a database: the backoff, and the silence (ADR-0029 §11-§13).
//
// What is NOT tested here is the absence of overlap, and that is deliberate:
// it is not enforced by a lock that could be asserted on, it is enforced by
// there being ONE goroutine that sleeps after finishing. A test of a property
// held by the shape of the code would only re-assert the shape. The thing worth
// pinning is that the cadence and the per-pass ceiling cannot be read as
// compatible by accident — which is the first case below.

import (
	"testing"
	"time"

	"meshcraft/internal/model"
)

// THE TRAP, written down as an assertion.
//
// A cadence SHORTER than the per-pass ceiling is exactly the configuration that
// would stack passes, and it only hurts when the controller is slow. It is safe
// here only because the delay is measured from the end of a pass. If somebody
// later swaps the sleep for a ticker, this test still passes — so it carries
// the reasoning in its message rather than pretending to catch that.
func TestExtSyncCadenceIsShorterThanOnePassCeiling(t *testing.T) {
	if extSyncEvery >= extSyncTimeout {
		t.Fatalf("cadence %v, per-pass ceiling %v: the comment in extsync.go "+
			"explains the overlap hazard on the assumption that the cadence is the "+
			"shorter one. If that changed, re-read the reasoning.",
			extSyncEvery, extSyncTimeout)
	}
	// The backoff floor must be well above the cadence, or "backing off" would
	// mean "trying again almost immediately" — which is the hammering the
	// backoff exists to stop.
	if extSyncBackoffMin <= extSyncEvery {
		t.Fatalf("backoff floor %v is not above the cadence %v: a dead controller "+
			"would still be polled at nearly full rate", extSyncBackoffMin, extSyncEvery)
	}
	if extSyncBackoffMax < extSyncBackoffMin {
		t.Fatal("the backoff ceiling is below its floor")
	}
}

// The backoff doubles, caps, and an extension in cooldown is not due.
func TestExtSyncBackoffDoublesAndCaps(t *testing.T) {
	var x extSyncState
	var now = time.Now()

	if !x.due("ext", now) {
		t.Fatal("a never-seen extension must be due at once")
	}

	var first = x.failed("ext", now)
	if first != extSyncBackoffMin {
		t.Errorf("first backoff = %v, want %v", first, extSyncBackoffMin)
	}
	if x.due("ext", now.Add(first/2)) {
		t.Error("an extension inside its cooldown was reported due")
	}
	if !x.due("ext", now.Add(first*2)) {
		t.Error("an extension past its cooldown was not reported due")
	}

	var second = x.failed("ext", now)
	if second != 2*extSyncBackoffMin {
		t.Errorf("second backoff = %v, want %v", second, 2*extSyncBackoffMin)
	}
	// Enough failures to reach the ceiling, and it must not grow past it.
	var i int
	for i = 0; i < 20; i++ {
		x.failed("ext", now)
	}
	if x.failed("ext", now) != extSyncBackoffMax {
		t.Errorf("the backoff grew past its ceiling of %v", extSyncBackoffMax)
	}

	// A success clears it: the next pass tries straight away.
	x.succeeded("ext", "1/0/0/1/0")
	if !x.due("ext", now) {
		t.Error("a recovered extension is still in cooldown")
	}
}

// Reconfiguring an extension forgets everything, so a repaired token does not
// sit out a fifteen-minute cooldown. THIS is what makes the removed "import
// now" button unnecessary rather than merely missing (ADR-0029 §12).
func TestExtSyncForgetClearsTheCooldown(t *testing.T) {
	var x extSyncState
	var now = time.Now()
	var i int
	for i = 0; i < 10; i++ {
		x.failed("ext", now)
	}
	if x.due("ext", now) {
		t.Fatal("precondition: the extension should be in cooldown")
	}
	x.forget("ext")
	if !x.due("ext", now) {
		t.Fatal("forget() left a cooldown behind: an operator who just fixed a token " +
			"would have to wait, which is the reason somebody would want the button back")
	}
	// And the failure flag is cleared too, so the next failure is reported as
	// a transition again rather than being swallowed as "already failing".
	if x.wasFailing("ext", true) {
		t.Error("forget() kept the failing flag: the next outage would go unannounced")
	}
}

// THE SILENCE. A pass that confirms the world writes nothing; a pass that
// changes something writes once (ADR-0029 §13).
func TestExtSyncOnlyReportsWhenTheCensusMoves(t *testing.T) {
	var x extSyncState

	// The FIRST successful pass is not news: a binary that has just started has
	// not learned that anything moved, and announcing the whole inventory on
	// every deploy is the same noise by another route.
	if x.succeeded("ext", "4/0/0/1/0") {
		t.Error("the first pass after a restart reported a change")
	}
	// An identical pass: silence.
	if x.succeeded("ext", "4/0/0/1/0") {
		t.Error("an unchanged pass reported a change — this is the 2 880-lines-a-day bug")
	}
	// A box left the site: one line.
	if !x.succeeded("ext", "3/0/0/1/1") {
		t.Error("a pass that removed a box reported nothing")
	}
	// …and the pass after it is quiet again.
	if x.succeeded("ext", "3/0/0/1/1") {
		t.Error("the census was not remembered, so every pass would speak")
	}
}

// The failure transition is announced ONCE on the way down and once on the way
// back up — not every cadence while a controller is down.
func TestExtSyncAnnouncesEachTransitionOnce(t *testing.T) {
	var x extSyncState

	if x.wasFailing("ext", true) {
		t.Error("the first failure was treated as already-failing, so nobody is told")
	}
	if !x.wasFailing("ext", true) {
		t.Error("the second consecutive failure would be announced again")
	}
	if !x.wasFailing("ext", false) {
		t.Error("the recovery was not seen as a transition")
	}
	if x.wasFailing("ext", false) {
		t.Error("a second healthy pass would announce a recovery again")
	}
}

// censusOf must move when any of the five numbers moves, and not otherwise.
func TestCensusOfIsSensitiveToEachCount(t *testing.T) {
	var base = model.ExtSyncReport{AccessPoints: 4, Switches: 1, Others: 0, SSIDs: 2, Removed: 0}
	var same = censusOf(base)
	if censusOf(base) != same {
		t.Fatal("censusOf is not stable for the same report")
	}
	var variants = []model.ExtSyncReport{
		{AccessPoints: 5, Switches: 1, SSIDs: 2},
		{AccessPoints: 4, Switches: 2, SSIDs: 2},
		{AccessPoints: 4, Switches: 1, Others: 1, SSIDs: 2},
		{AccessPoints: 4, Switches: 1, SSIDs: 3},
		{AccessPoints: 4, Switches: 1, SSIDs: 2, Removed: 1},
	}
	var v model.ExtSyncReport
	for _, v = range variants {
		if censusOf(v) == same {
			t.Errorf("censusOf(%+v) collides with the baseline: that change would go unreported", v)
		}
	}
}

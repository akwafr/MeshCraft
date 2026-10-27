package api

import (
	"net/http"
	"strings"
	"testing"

	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// Attaching a device to its collector proxy, from the API side.
//
// What these tests lock in: the only decision in this batch that can produce
// a silent outage. Attaching a device to a proxy that does not cover its site
// does not move collection, it REMOVES it — no more SNMP polling, no more trap
// recipient, and the device stays pinged, therefore green on screen
// (store.TestAttachmentDoesNotGrantCoverage). Nothing fails: that is why
// the refusal must be tested rather than observed in production.
//
// Without a database, like the rest of the package: judgeCollector and
// collectorWriteError are the two steps where the decision is made, and they
// were written pure on purpose so they can be verified here. What a test in
// this package CANNOT cover, and which would require PostgreSQL: that
// ProxiesCoveringSite returns the covering proxies (its `where` is the word-
// for-word transcription of DevicesWatchedBy, cf. its comment), and the full
// HTTP path — `site_admin` rights, log, push to proxies.

// The reference fleet: an instance proxy that covers everything, and an agency
// proxy restricted to site A. "covers site A" is therefore the entire list;
// for site B, only the instance proxy is in it.
var couvrantsSiteA = []model.Proxy{
	{ID: "local", Name: "Local", Global: true},
	{ID: "branch-a", Name: "Branch A"},
}

// TestCollectorRefusesANonCoveringProxy: the core of the batch. The proxy exists,
// it is perfectly reachable, it is simply not in the site's coverage — and that
// is the only case where accepting would be worse than refusing.
func TestCollectorRefusesANonCoveringProxy(t *testing.T) {
	// The list served for site B does not contain agency proxy A.
	var couvrantsSiteB = []model.Proxy{{ID: "local", Name: "Local", Global: true}}
	var verdict, p = judgeCollector(couvrantsSiteB, "branch-a")
	if verdict != collectorNotCovering {
		t.Fatalf("an out-of-coverage proxy must be refused, verdict %v", verdict)
	}
	if p.ID != "" {
		t.Errorf("a refusal must designate no proxy, got %q", p.ID)
	}
}

// TestCollectorAcceptsACoveringProxy: the reverse, and it must return the
// proxy — its NAME goes to the log, and it may no longer be readable in three months.
func TestCollectorAcceptsACoveringProxy(t *testing.T) {
	var verdict, p = judgeCollector(couvrantsSiteA, "branch-a")
	if verdict != collectorAccepted {
		t.Fatalf("a covering proxy must be accepted, verdict %v", verdict)
	}
	if p.Name != "Branch A" {
		t.Errorf("the accepted proxy must be returned with its name, got %q", p.Name)
	}
}

// TestCollectorGlobalIsCovering: an instance proxy covers all sites WITHOUT a
// row in proxy_sites. That is half the predicate a request "cleanup" would be
// most tempted to drop, and dropping it would make the only collector of a
// single-proxy instance unchoosable.
func TestCollectorGlobalIsCovering(t *testing.T) {
	var verdict, p = judgeCollector(couvrantsSiteA, "local")
	if verdict != collectorAccepted || !p.Global {
		t.Fatalf("an instance proxy covers every site, verdict %v", verdict)
	}
}

// TestCollectorDetachByEmptyValue: the empty string DETACHES; it is neither
// an error nor a credential to look up in the list. Detaching is the return to
// coverage-based collection — the only fallback that exists — and refusing it
// would force the UI to invent a second action to say "no one".
func TestCollectorDetachByEmptyValue(t *testing.T) {
	var verdict, p = judgeCollector(couvrantsSiteA, "")
	if verdict != collectorDetach {
		t.Fatalf("an empty value detaches, verdict %v", verdict)
	}
	if p.ID != "" {
		t.Errorf("detaching must designate no proxy, got %q", p.ID)
	}
	// And it detaches EVEN when no proxy covers the site: returning a device to
	// coverage must never depend on what the coverage contains, otherwise an
	// attachment that became invalid could never be cancelled.
	verdict, _ = judgeCollector(nil, "")
	if verdict != collectorDetach {
		t.Fatalf("detaching does not depend on the list, verdict %v", verdict)
	}
}

// TestCollectorProxyMissing: a credential that designates nothing is not in
// the covering list — therefore refused, like a non-covering proxy. The handler
// then separates the two, because the repair actions differ (widen coverage /
// fix a credential); what the verdict guarantees here is that neither is written.
func TestCollectorProxyMissing(t *testing.T) {
	var verdict, _ = judgeCollector(couvrantsSiteA, "11111111-2222-3333-4444-555555555555")
	if verdict != collectorNotCovering {
		t.Fatalf("an unknown proxy must not be written, verdict %v", verdict)
	}
}

// TestRefusalSaysTheConsequence: the message is half the device. A refusal that does
// not say what it avoids reads like a missing permission, and the operator leaves
// believing meshcraft prevents them from doing something useful.
func TestRefusalSaysTheConsequence(t *testing.T) {
	var msg = notCoveringMessage("sw-a1", "Branch B")
	var want = []string{"Branch B", "sw-a1", "coverage", "anybody", "SNMP", "traps"}
	var word string
	for _, word = range want {
		if !strings.Contains(msg, word) {
			t.Errorf("the refusal must mention %q: %s", word, msg)
		}
	}
	// Without a proxy name (case of a proxy read just before deletion), the
	// sentence must still be a sentence.
	if strings.Contains(notCoveringMessage("sw-a1", ""), "\" \"") {
		t.Error("a proxy without a name must not leave empty quotes")
	}
}

// TestLogNamesBothCollectors: the old AND the new, explicitly. That is
// what makes the action findable three months later, when the observed symptom
// ("this switch no longer reports its ports") has no visible link to a dropdown
// checked on a Tuesday.
func TestLogNamesBothCollectors(t *testing.T) {
	var msg = collectorChangeMessage("sw-a1", "Local", "Branch A")
	if !strings.Contains(msg, "Local") || !strings.Contains(msg, "Branch A") {
		t.Errorf("the log must name the old and new collector: %s", msg)
	}
	if !strings.Contains(msg, "sw-a1") {
		t.Errorf("the log must name the device: %s", msg)
	}
}

// TestLogSaysTheDetachedRegime: bare "none" would read like a forgotten setting.
// Detachment is an operating regime — coverage-based collection, used by the
// whole fleet before migration 0022 — and both the log and the UI must say so.
func TestLogSaysTheDetachedRegime(t *testing.T) {
	var msg = collectorChangeMessage("sw-a1", "Branch A", "")
	if !strings.Contains(msg, "coverage") {
		t.Errorf("detachment must read as a regime, not as emptiness: %s", msg)
	}
	// Attaching a device that had none is the majority case on first pass over a
	// fleet: the empty "before" must read just as well.
	if !strings.Contains(collectorChangeMessage("sw-a1", "", "Branch A"), "coverage") {
		t.Error("the originally detached state must be named too")
	}
}

// TestWriteErrorsAreReadable: the two possible paths between the check and
// the update — device deleted, proxy deleted. `fail` would render them as
// "not found" and "internal error", i.e. a response that says nothing useful and
// one that blames the server for a perfectly normal concurrent action.
func TestWriteErrorsAreReadable(t *testing.T) {
	var code, msg = collectorWriteError(store.ErrProxyUnknown)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("an unknown proxy is a content refusal, got %d", code)
	}
	if msg == "" {
		t.Error("a refusal must say why")
	}

	code, msg = collectorWriteError(store.ErrNotFound)
	if code != http.StatusNotFound {
		t.Errorf("a missing device is a 404, got %d", code)
	}
	if !strings.Contains(msg, "device") {
		t.Errorf("the 404 must say WHAT is missing: %q", msg)
	}
}

// TestUnknownErrorStaysDelegated: everything else goes to `fail`, which is the
// only translation table in the package. Duplicating it here would make it
// diverge — and a database connection error rendered as 422 "this proxy does
// not exist" would send the operator to fix coverage instead of the network.
func TestUnknownErrorStaysDelegated(t *testing.T) {
	var code, msg = collectorWriteError(errForTest("connection refused"))
	if code != 0 || msg != "" {
		t.Errorf("a non-specific error is delegated to fail, got %d %q", code, msg)
	}
	// Success too: collectorWriteError does not invent a refusal.
	code, msg = collectorWriteError(nil)
	if code != 0 || msg != "" {
		t.Errorf("nil is not a refusal, got %d %q", code, msg)
	}
}

// errForTest: an error that is none of the store ones.
type errForTest string

func (e errForTest) Error() string { return string(e) }

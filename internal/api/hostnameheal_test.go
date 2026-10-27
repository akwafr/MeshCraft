package api

// The system-name state machine, pinned state by state.
//
// Everything that decides anything in hostnameheal.go is a pure function, and
// that is on purpose: `Server.Store` is a concrete *store.Store and this package's
// tests run WITHOUT PostgreSQL, so a rule that could only be exercised through a
// handler would be a rule nothing checks. What is locked here:
//
//   - the five values of State, one test each;
//   - "not observed is NOT drifted", the rule without which every unreachable
//     device queues a write forever;
//   - `unsupported` never presented as `drift` — one promises a repair that will
//     never come (cf. internal/driver/hostname.go, last paragraph);
//   - the cooldown arithmetic, so a durably failing device costs one attempt a day;
//   - a rename whose name cannot be a hostname still succeeds, and is reported;
//   - the capability table against the connectors' real types (build-time
//     detector, same role as portnamer_consistency_test.go).

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"

	"meshcraft/internal/driver/arista/eos"
	"meshcraft/internal/driver/cisco/ios"
	"meshcraft/internal/driver/fs/s3400"
	"meshcraft/internal/driver/fs/s3900"
	"meshcraft/internal/driver/hpe/procurve"
	"meshcraft/internal/driver/instanton"
	"meshcraft/internal/driver/mikrotik/routeros"
	"meshcraft/internal/driver/mikrotik/swos"
	"meshcraft/internal/driver/ubiquiti/edgeswitch"
)

// deviceOn builds the minimum a verdict reads: a name, a connector, an address.
func deviceOn(name, connector string) model.Device {
	return model.Device{
		ID: "d-" + name, SiteID: "s1", Name: name, MgmtIP: "192.0.2.2",
		Extra: json.RawMessage(`{"driver":"` + connector + `"}`),
	}
}

// lldpSaying returns the LLDP self-row a device would carry, as the poller writes
// it, plus the "this row exists" flag the lookup produces.
func lldpSaying(sysName string) (model.LLDPSelf, bool) {
	return model.LLDPSelf{DeviceID: "d", SysName: sysName, Served: true}, true
}

// --- The five states ---

func TestHostnameStateOK(t *testing.T) {
	var d = deviceOn("sw-lab-01", "cisco-ios")
	var self, seen = lldpSaying("sw-lab-01")
	var obs, src = hostnameObserved(d, self, seen)
	var c = hostnameConformityOf(d, obs, src)
	if c.State != hostnameOK {
		t.Fatalf("state = %q, want ok (%+v)", c.State, c)
	}
	if c.Desired != "sw-lab-01" || c.Observed != "sw-lab-01" {
		t.Errorf("desired/observed = %q/%q", c.Desired, c.Observed)
	}
	if c.Source != hostnameSourceLLDP {
		t.Errorf("source = %q, want lldp — the free observation must be preferred", c.Source)
	}
	if c.Because != "" {
		t.Errorf("a plain ok needs no sentence, got %q", c.Because)
	}
}

func TestHostnameStateDrift(t *testing.T) {
	var d = deviceOn("sw-lab-01", "cisco-ios")
	var self, seen = lldpSaying("Switch")
	var obs, src = hostnameObserved(d, self, seen)
	var c = hostnameConformityOf(d, obs, src)
	if c.State != hostnameDrift {
		t.Fatalf("a device announcing \"Switch\" must be drift, got %q", c.State)
	}
	// The sentence must name BOTH names: this is what an operator reads after
	// having been overruled.
	if !strings.Contains(c.Because, "Switch") || !strings.Contains(c.Because, "sw-lab-01") {
		t.Errorf("the drift sentence must name both names: %s", c.Because)
	}
	if !hostnameRepairableFor(d, c) {
		t.Error("a drift on a reachable device with a writable connector must be repaired")
	}
}

// TestHostnameStateNotObserved is the rule that keeps the loop honest: no source
// has reported, therefore NOTHING is concluded and nothing is pushed. Without it
// every unreachable device would queue a write on every pass, forever.
func TestHostnameStateNotObserved(t *testing.T) {
	var d = deviceOn("sw-lab-01", "cisco-ios")
	var obs, src = hostnameObserved(d, model.LLDPSelf{}, false)
	if obs != "" || src != "" {
		t.Fatalf("no source reported, yet observed = %q from %q", obs, src)
	}
	var c = hostnameConformityOf(d, obs, src)
	if c.State != hostnameNotObserved {
		t.Fatalf("state = %q, want not_observed", c.State)
	}
	if c.State == hostnameDrift {
		t.Fatal("not observed must never read as drift")
	}
	if hostnameRepairableFor(d, c) {
		t.Fatal("an absent reading must never trigger a repair — that is the whole rule")
	}
	// A served LLDP row with an EMPTY system name is still no observation: the
	// device serves the MIB and says nothing about its name.
	var self, seen = lldpSaying("   ")
	obs, src = hostnameObserved(d, self, seen)
	if obs != "" {
		t.Errorf("a blank sys_name is not an observation, got %q", obs)
	}
}

// unwritableRange makes ONE registered connector read `false` in the capability
// table for the duration of a test, and puts the entry back.
//
// EVERY entry in that table is `true` since 2026-08-11 — aruba-instant-on-1930
// was the last holdout and its write was proven on hardware. That is good news
// and it left the `false` BRANCH of hostnameWriteSupport with no instance to
// exercise it, which is how a distinction quietly stops being tested. The branch
// still has to work, because the next connector to ship starts there: an
// undeclared capability is ABSENT, and a device on such a range must read
// `unsupported` — "meshcraft's name will not converge here" — never `drift`,
// which promises a repair nothing performs.
//
// A registered connector is used rather than an invented name on purpose: an
// unknown name is refused EARLIER, by driver.ConnectorFor, so it would exercise a
// different branch and prove nothing about this one.
func unwritableRange(t *testing.T, name string) {
	t.Helper()
	var was, listed = hostnameWritableConnectors[name]
	if !listed {
		t.Fatalf("connector %q is not in the table: this helper flips an entry, "+
			"it does not invent one", name)
	}
	hostnameWritableConnectors[name] = false
	t.Cleanup(func() { hostnameWritableConnectors[name] = was })
}

// TestHostnameStateUnsupportedIsNotDrift is the distinction that must never be
// collapsed. Both devices below announce a name that differs from the inventory's;
// only one of them can ever be repaired.
func TestHostnameStateUnsupportedIsNotDrift(t *testing.T) {
	unwritableRange(t, "aruba-instant-on-1930")
	var writable = deviceOn("sw-lab-01", "cisco-ios")
	var never = deviceOn("sw-lab-01", "aruba-instant-on-1930")
	var self, seen = lldpSaying("Switch")

	var obs, src = hostnameObserved(writable, self, seen)
	if hostnameConformityOf(writable, obs, src).State != hostnameDrift {
		t.Fatal("cisco-ios can be written: its difference is a drift")
	}

	obs, src = hostnameObserved(never, self, seen)
	var c = hostnameConformityOf(never, obs, src)
	if c.State != hostnameUnsupported {
		t.Fatalf("an Instant On has no proven system-name write: state = %q, want "+
			"unsupported — reporting drift would promise a repair that never comes", c.State)
	}
	// Observed and Desired still travel: the screen may show what the box answers
	// to and what meshcraft WOULD have pushed. Only the promise is withheld.
	if c.Observed != "Switch" || c.Desired != "sw-lab-01" {
		t.Errorf("unsupported must still carry both names, got %+v", c)
	}
	if !strings.Contains(c.Because, "aruba-instant-on-1930") {
		t.Errorf("the refusal must name the connector: %s", c.Because)
	}
	if hostnameRepairableFor(never, c) {
		t.Error("nothing must be pushed to a platform that cannot carry the write")
	}

	// An unresolvable connector lands here too — it cannot converge as things
	// stand — and ConnectorFor's own sentence must survive into Because, because
	// "no connector", "unknown to this binary" and "cannot reach this
	// platform" are three different repairs.
	var orphan = model.Device{ID: "d3", SiteID: "s1", Name: "sw-lab-01", MgmtIP: "192.0.2.9"}
	var o = hostnameConformityOf(orphan, "Switch", hostnameSourceLLDP)
	if o.State != hostnameUnsupported || o.Because == "" {
		t.Errorf("a device with no connector must be unsupported with a reason, got %+v", o)
	}
}

// TestHostnameStateUnnameable: the meshcraft name cannot be a hostname. Only the
// operator can fix that, so the verdict says so instead of guessing a replacement
// — and Desired stays EMPTY, because there is nothing meshcraft would send.
func TestHostnameStateUnnameable(t *testing.T) {
	var d = deviceOn("Switch Zürich 1", "cisco-ios")
	var self, seen = lldpSaying("Switch")
	var obs, src = hostnameObserved(d, self, seen)
	var c = hostnameConformityOf(d, obs, src)
	if c.State != hostnameUnnameable {
		t.Fatalf("state = %q, want unnameable", c.State)
	}
	if c.Desired != "" {
		t.Errorf("desired must be empty when no name can be derived, got %q", c.Desired)
	}
	if !strings.Contains(c.Because, "ü") {
		t.Errorf("the refusal must name the offending character: %s", c.Because)
	}
	if hostnameRepairableFor(d, c) {
		t.Error("an unnameable device must not be pushed to")
	}
	// A name over the 63-octet ceiling is unnameable too — refused, never cut.
	var long = hostnameConformityOf(deviceOn(strings.Repeat("a", 64), "cisco-ios"),
		"Switch", hostnameSourceLLDP)
	if long.State != hostnameUnnameable || long.Desired != "" {
		t.Errorf("a 64-character name must be unnameable with no desired, got %+v", long)
	}
}

// hostnameRepairableFor exercises the loop's eligibility rule without a Server:
// the apply-queue guard tolerates a nil Server, so a zero one is enough.
func hostnameRepairableFor(d model.Device, c HostnameConformity) bool {
	var s = &Server{}
	return s.hostnameRepairable(d, c)
}

// --- Every State value is one of the five, always ---

// A verdict with an unknown State would be a UI the frontend cannot exhaust. This
// walks the shapes a real fleet produces and pins that the answer is always one of
// the five documented words.
func TestHostnameStateIsAlwaysOneOfFive(t *testing.T) {
	var known = map[string]bool{
		hostnameOK: true, hostnameDrift: true, hostnameNotObserved: true,
		hostnameUnsupported: true, hostnameUnnameable: true,
	}
	var devices = []model.Device{
		deviceOn("sw-lab-01", "cisco-ios"),
		deviceOn("Switch Zürich 1", "cisco-ios"),
		deviceOn("sw-lab-01", "aruba-instant-on-1930"),
		deviceOn("sw-lab-01", "connector-from-the-future"),
		{ID: "bare", SiteID: "s1"},
	}
	var observations = []string{"", "sw-lab-01", "Switch", "SW-LAB-01"}
	var d model.Device
	for _, d = range devices {
		var obs string
		for _, obs = range observations {
			var src = ""
			if obs != "" {
				src = hostnameSourceLLDP
			}
			var c = hostnameConformityOf(d, obs, src)
			if !known[c.State] {
				t.Fatalf("device %q / observed %q produced state %q, outside the five",
					d.Name, obs, c.State)
			}
			if c.State != hostnameOK && c.Because == "" {
				t.Errorf("device %q / observed %q: state %q with no sentence for the "+
					"operator", d.Name, obs, c.State)
			}
		}
	}
}

// --- The two observation sources, and their precedence ---

// LLDP is free and refreshed every poll cycle; `extra.identity.hostname` is only
// as fresh as the last read-state. So LLDP wins when both exist, and the fallback
// exists for devices whose LLDP or SNMP is silent.
func TestHostnameObservedPrefersLLDPThenReadState(t *testing.T) {
	var d = model.Device{
		ID: "d1", SiteID: "s1", Name: "sw-lab-01", MgmtIP: "192.0.2.2",
		Extra: json.RawMessage(`{"driver":"cisco-ios",
		                         "identity":{"hostname":"old-name","serial":"X"}}`),
	}
	var self, seen = lldpSaying("fresh-name")
	var obs, src = hostnameObserved(d, self, seen)
	if obs != "fresh-name" || src != hostnameSourceLLDP {
		t.Fatalf("LLDP must win: got %q from %q", obs, src)
	}
	// No LLDP row at all: the stored identity answers, and says where from.
	obs, src = hostnameObserved(d, model.LLDPSelf{}, false)
	if obs != "old-name" || src != hostnameSourceReadState {
		t.Fatalf("read-state fallback: got %q from %q", obs, src)
	}
	// A device with neither is not observed, and the verdict must say precisely
	// that rather than compare against nothing.
	var bare = deviceOn("sw-lab-01", "cisco-ios")
	obs, src = hostnameObserved(bare, model.LLDPSelf{}, false)
	if obs != "" || src != "" {
		t.Fatalf("nothing reported, got %q from %q", obs, src)
	}
}

// --- The two tolerances, each closing a write loop ---

// A device that stores the name in its own case, or announces it with its DNS
// domain appended, is NOT drifted. Both would otherwise be permanent drift: the
// push would succeed, change nothing observable, and be due again on every
// cooldown — forever, with an event each time.
func TestHostnameMatchesToleratesCaseAndDomain(t *testing.T) {
	var ok = []struct{ observed, desired string }{
		{"sw-lab-01", "sw-lab-01"},
		{"SW-LAB-01", "sw-lab-01"},
		{"sw-lab-01.example.com", "sw-lab-01"},
		{"SW-LAB-01.lab.example.com", "sw-lab-01"},
	}
	var c struct{ observed, desired string }
	for _, c = range ok {
		if !hostnameMatches(c.observed, c.desired) {
			t.Errorf("%q must match %q — re-pushing this forever is the failure to avoid",
				c.observed, c.desired)
		}
	}
	var differs = []struct{ observed, desired string }{
		{"Switch", "sw-lab-01"},
		{"sw-lab-02", "sw-lab-01"},
		{"sw-lab-01x", "sw-lab-01"},
		// A prefix is not a match: this is the shape a platform's own ceiling
		// produces when it truncates, and it must read as drift.
		{"sw-lab", "sw-lab-01"},
		// Nothing matches an empty intent.
		{"sw-lab-01", ""},
	}
	for _, c = range differs {
		if hostnameMatches(c.observed, c.desired) {
			t.Errorf("%q must NOT match %q", c.observed, c.desired)
		}
	}
	// The tolerated difference is explained rather than left silent: two visibly
	// different strings with a green verdict need a sentence.
	var v = hostnameConformityOf(deviceOn("sw-lab-01", "cisco-ios"),
		"SW-LAB-01.lab", hostnameSourceLLDP)
	if v.State != hostnameOK || v.Because == "" {
		t.Errorf("a case/domain difference must be ok WITH an explanation, got %+v", v)
	}
}

// --- Cooldown ---

// Same contract as the SNMP catch-up: doubling per failure, capped at a day,
// cleared on success — and in memory only, so a fresh binary retries at once.
func TestHostnameHealBackoff(t *testing.T) {
	var h hostnameHealState
	var t0 = time.Now()

	if !h.due("d1", t0) {
		t.Fatal("a device never attempted must be due")
	}
	h.failed("d1", t0)
	if h.due("d1", t0.Add(30*time.Minute)) {
		t.Error("30 min after a first failure: still cooling down (1h)")
	}
	if !h.due("d1", t0.Add(61*time.Minute)) {
		t.Error("after the first cooldown, the device is due again")
	}
	// Second failure doubles it: 2h, so 90 min is still too early.
	h.failed("d1", t0)
	if h.due("d1", t0.Add(90*time.Minute)) {
		t.Error("a second failure must double the cooldown to 2h")
	}
	if !h.due("d1", t0.Add(121*time.Minute)) {
		t.Error("2h after the second failure, the device is due again")
	}

	var i int
	for i = 0; i < 10; i++ {
		h.failed("d1", t0)
	}
	if h.due("d1", t0.Add(23*time.Hour)) {
		t.Error("backoff must cap at 24h, not below")
	}
	if !h.due("d1", t0.Add(25*time.Hour)) {
		t.Error("even a device that never converges is retried daily — about one " +
			"session a day is the budget this loop promises")
	}

	h.succeeded("d1")
	if !h.due("d1", t0) {
		t.Error("success clears the backoff entirely")
	}

	h.failed("d1", t0)
	if !h.due("d2", t0) {
		t.Error("one device's backoff must not delay another")
	}
}

// --- The rename hook ---

// A RENAME NEVER FAILS BECAUSE OF THE EQUIPMENT. The signature carries half of
// that promise — nothing to propagate — and hostnameRenameAction carries the
// other: an unnameable name is REPORTED, never an error, and never a push.
func TestRenameWithUnnameableNameStillSucceeds(t *testing.T) {
	unwritableRange(t, "aruba-instant-on-1930")
	// The promise, pinned as a type: hostnameAfterRename returns nothing, so
	// handleUpdateDevice has no error to turn into a non-2xx response.
	var s = &Server{}
	var _ func(*http.Request, model.Device) = s.hostnameAfterRename

	var refusal, action = hostnameRenameAction(deviceOn("Switch Zürich 1", "cisco-ios"))
	if action != hostnameRenameReport {
		t.Fatalf("action = %q, want report: the rename stands and the refusal is "+
			"reported, not swallowed", action)
	}
	if refusal == "" || !strings.Contains(refusal, "ü") {
		t.Errorf("the report must carry the reason, naming the character: %q", refusal)
	}

	// The ordinary case still pushes, with the normalized name.
	var name string
	name, action = hostnameRenameAction(deviceOn("sw lab 01", "cisco-ios"))
	if action != hostnameRenamePush || name != "sw-lab-01" {
		t.Errorf("a nameable rename must push \"sw-lab-01\", got %q / %q", name, action)
	}

	// A platform that cannot carry the write is SILENT — including when the name
	// is also unnameable. Two precedences would log a warning about a push that
	// was never going to happen; hostnameConformityOf decides in this same order.
	_, action = hostnameRenameAction(deviceOn("Switch Zürich 1", "aruba-instant-on-1930"))
	if action != hostnameRenameSkip {
		t.Errorf("an unwritable platform must stay silent, got %q", action)
	}
	// No address: nothing to reach, and the device page already says so.
	var noAddr = deviceOn("sw-lab-01", "cisco-ios")
	noAddr.MgmtIP = ""
	_, action = hostnameRenameAction(noAddr)
	if action != hostnameRenameSkip {
		t.Errorf("a device with no management address must be skipped, got %q", action)
	}
}

// A name that cannot be pushed is BLOCKED, not FAILED: nothing failed, and a
// retry would do exactly the same thing. It waits on a decision only the operator
// can make.
func TestPipelineForUnnameableIsBlockedAndSaysWhy(t *testing.T) {
	var p = pipelineForUnnameable(deviceOn("Switch Zürich 1", "cisco-ios"), "it carries \"ü\"")
	if p.State != "blocked" {
		t.Errorf("state = %q, want blocked", p.State)
	}
	if p.BlockedReason == "" || p.Label == "" {
		t.Errorf("a blocked pipeline with no reason explains nothing: %+v", p)
	}
}

// --- The JSON shape the frontend codes against ---

// The four states are told apart in the UI by these exact keys. A renamed or
// dropped field is a silent break — no Go test would otherwise notice, and the
// screen would fall back to "unknown" for every device.
func TestHostnameConformityJSONShape(t *testing.T) {
	var raw, err = json.Marshal(HostnameConformity{
		Desired: "sw-lab-01", Observed: "Switch", Source: hostnameSourceLLDP,
		State: hostnameDrift, Because: "because",
	})
	if err != nil {
		t.Fatal(err)
	}
	var want = `{"desired":"sw-lab-01","observed":"Switch","source":"lldp",` +
		`"state":"drift","because":"because"}`
	if string(raw) != want {
		t.Fatalf("shape moved:\n got %s\nwant %s", raw, want)
	}
	// `desired` is NOT omitempty: the UI must be able to read "nothing can be
	// pushed" as an empty string rather than an absent key.
	raw, err = json.Marshal(HostnameConformity{State: hostnameUnnameable})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"desired":"","state":"unnameable"}` {
		t.Fatalf("an unnameable verdict serializes as %s", raw)
	}
}

// --- Build-time detector for the capability table ---

// hostnameAdminSamples: the CONCRETE access type each connector's Authenticate
// returns, as a typed nil. A type assertion never calls a method, so a nil is
// enough to ask the only question that matters — does this platform's access carry
// SetHostname?
//
// This is what keeps hostnameWritableConnectors honest, and it is the same shape
// of guard as portnamer_consistency_test.go: a capability nothing in the type
// system enforces, checked at build time so a connector cannot ship with the
// declaration and the implementation out of step.
var hostnameAdminSamples = map[string]driver.Admin{
	"arista-eos":            (*eos.Client)(nil),
	"cisco-ios":             (*ios.Client)(nil),
	"fs-s3400":              (*s3400.Client)(nil),
	"fs-s3900":              (*s3900.Client)(nil),
	"hpe-procurve":          (*procurve.Client)(nil),
	"mikrotik-routeros":     (*routeros.Client)(nil),
	"mikrotik-swos":         (*swos.Admin)(nil),
	"ubiquiti-edgeswitch":   (*edgeswitch.Client)(nil),
	"aruba-instant-on-1930": (*instanton.Admin)(nil),
}

func TestHostnameCapabilityTableMatchesTheConnectors(t *testing.T) {
	var name string
	var sample driver.Admin
	for name, sample = range hostnameAdminSamples {
		var _, isSetter = driver.AsHostnameSetter(sample)
		var declared, listed = hostnameWritableConnectors[name]
		if !listed {
			t.Errorf("connector %q is missing from hostnameWritableConnectors", name)
			continue
		}
		if declared != isSetter {
			t.Errorf("connector %q: the table says writable=%v and its access type "+
				"says %v. A table that over-promises shows \"drift — a repair is "+
				"coming\" on a device that can never converge; one that under-promises "+
				"leaves the fleet's names stale with no explanation",
				name, declared, isSetter)
		}
	}
}

// A connector shipped without a line in the table would be treated as "cannot",
// i.e. its devices would read `unsupported` forever with nobody noticing. Fail the
// build instead.
func TestEveryRegisteredConnectorIsClassifiedForHostname(t *testing.T) {
	var name string
	for _, name = range driver.Names() {
		var _, listed = hostnameWritableConnectors[name]
		if !listed {
			t.Errorf("connector %q is registered and absent from "+
				"hostnameWritableConnectors: classify it (true once the system-name "+
				"write is proven on hardware, false until then) or its devices will "+
				"read \"unsupported\" forever", name)
		}
		var _, sampled = hostnameAdminSamples[name]
		if !sampled {
			t.Errorf("connector %q has no access-type sample in this test: nothing "+
				"checks its table entry against reality", name)
		}
	}
}

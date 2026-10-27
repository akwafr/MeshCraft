package api

import (
	"strings"
	"testing"
	"time"

	"meshcraft/internal/model"
)

// These tests exist because the rule they cover was WRONG IN PRODUCTION.
//
// On 2026-08-02 a proxy on 203.0.113.0/24 marked four devices of another site
// unreachable 3.7 seconds after connecting, while their own collector was polling
// them successfully. Every case below is one of the situations that produced it,
// or one the repair must not create in exchange.

func up(name string, confirmed bool) model.ProxyReach {
	var yes = true
	var pr = model.ProxyReach{ProxyID: name, ProxyName: name, Reachable: &yes}
	if confirmed {
		var t = time.Now().Add(-3 * time.Minute)
		pr.ConfirmedAt, pr.ConfirmedBy = &t, "sysDescr carries css326"
	}
	return pr
}

func down(name string, confirmed bool) model.ProxyReach {
	var no = false
	var pr = model.ProxyReach{ProxyID: name, ProxyName: name, Reachable: &no, Misses: 3}
	if confirmed {
		var t = time.Now().Add(-3 * time.Minute)
		pr.ConfirmedAt, pr.ConfirmedBy = &t, "sysDescr carries css326"
	}
	return pr
}

func silent(name string) model.ProxyReach {
	return model.ProxyReach{ProxyID: name, ProxyName: name}
}

// THE REGRESSION TEST FOR THE 2026-08-02 OUTAGE.
//
// lab polls these devices and has proven their identity; site2 is a new
// instance proxy on another network that cannot reach them. The device is online.
func TestJudgeReachConfirmedUpBeatsUnconfirmedDown(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{
		down("proxy-site2", false),
		up("proxy-lab", true),
	}, nil)
	if v.Status != "ok" {
		t.Fatalf("status = %q, want ok: a proxy that proved the device's identity sees it", v.Status)
	}
	if !v.Confirmed {
		t.Error("the verdict does not record that it rests on a confirmed observation")
	}
	if !strings.Contains(v.Note, "proxy-lab") {
		t.Errorf("note = %q, must name the proxy that decided", v.Note)
	}
}

// Order must not matter: the bug was last-writer-wins, so a rule that depended on
// which observation came first would be the same bug wearing a different hat.
func TestJudgeReachIsIndependentOfOrder(t *testing.T) {
	var a = judgeReach([]model.ProxyReach{up("confirmed", true), down("other", false)}, nil)
	var b = judgeReach([]model.ProxyReach{down("other", false), up("confirmed", true)}, nil)
	if a.Status != b.Status {
		t.Errorf("order changed the verdict: %q then %q", a.Status, b.Status)
	}
}

// THE SYMMETRIC DANGER, and it matters as much as the false red. An unconfirmed
// proxy getting an answer at that address on ITS network must not light a device
// green that the proxy which knows it has lost.
func TestJudgeReachConfirmedDownBeatsUnconfirmedUp(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{
		up("proxy-elsewhere", false),
		down("proxy-lab", true),
	}, nil)
	if v.Status != "off" {
		t.Fatalf("status = %q, want off: an address answering elsewhere is not this device", v.Status)
	}
	// The note has to say the other proxy still sees SOMETHING there — that line
	// is the only clue that an address has been recycled onto another machine.
	if !strings.Contains(v.Note, "proxy-elsewhere") {
		t.Errorf("note = %q, want the unconfirmed answer reported", v.Note)
	}
	if !strings.Contains(v.Note, "never confirmed") {
		t.Errorf("note = %q, want it said that the other proxy proved nothing", v.Note)
	}
}

// A confirmed outage names when identity was last proven: that is what separates
// "the switch is off" from "this collector points at the wrong thing".
func TestJudgeReachConfirmedDownDatesTheProof(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{down("proxy-lab", true)}, nil)
	if v.Status != "off" {
		t.Fatalf("status = %q, want off", v.Status)
	}
	if !strings.Contains(v.Note, "last confirmed its identity") {
		t.Errorf("note = %q, want the age of the proof", v.Note)
	}
}

// RULE 3, and it is not a leftover: nine of thirteen devices in the reference
// fleet can never be SNMP-confirmed. Requiring proof would leave them unmonitored.
func TestJudgeReachFallsBackToOrWhenNothingIsConfirmed(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{
		down("proxy-site2", false),
		up("proxy-lab", false),
	}, nil)
	if v.Status != "ok" {
		t.Fatalf("status = %q, want ok: one proxy reaches it and none can prove anything", v.Status)
	}
	if v.Confirmed {
		t.Error("verdict claims confirmation it does not have")
	}
}

// All observers agree it is gone, none could ever confirm it. That is still an
// outage and must be reported as one.
func TestJudgeReachAllDownIsOff(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{
		down("proxy-a", false), down("proxy-b", false),
	}, nil)
	if v.Status != "off" {
		t.Fatalf("status = %q, want off", v.Status)
	}
}

// THE 3.7-SECOND FAULT ITSELF. A proxy that has just been handed the device and
// has not concluded must produce no verdict at all — not "down".
func TestJudgeReachSilentProxyIsNotAnOutage(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{silent("proxy-just-connected")}, nil)
	if v.Status != "unknown" {
		t.Fatalf("status = %q, want unknown: nobody has concluded", v.Status)
	}
}

// A proxy still forming an opinion must not dilute one already formed.
func TestJudgeReachSilentProxyDoesNotMaskAnUp(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{
		silent("proxy-just-connected"), up("proxy-lab", true),
	}, nil)
	if v.Status != "ok" {
		t.Fatalf("status = %q, want ok", v.Status)
	}
}

// Nobody watches it. Reporting an outage nobody observed would invent one.
func TestJudgeReachNoObserverIsUnknown(t *testing.T) {
	var v = judgeReach(nil, nil)
	if v.Status != "unknown" {
		t.Fatalf("status = %q, want unknown", v.Status)
	}
	if !strings.Contains(v.Note, "no proxy") {
		t.Errorf("note = %q, want it to say nobody is looking", v.Note)
	}
}

// Two confirmed proxies, one reaching: online. This is a dual-homed device during
// a partial network outage, and reporting it down would be wrong.
func TestJudgeReachOneConfirmedUpAmongConfirmedDowns(t *testing.T) {
	var v = judgeReach([]model.ProxyReach{
		down("proxy-a", true), up("proxy-b", true), down("proxy-c", true),
	}, nil)
	if v.Status != "ok" {
		t.Fatalf("status = %q, want ok: it is reachable from somewhere", v.Status)
	}
}

// A note longer than the screen showing it helps nobody.
func TestProxyNamesAreBounded(t *testing.T) {
	var many = []model.ProxyReach{
		up("a", false), up("b", false), up("c", false), up("d", false), up("e", false),
	}
	var got = proxyNames(many)
	if !strings.Contains(got, "2 others") {
		t.Errorf("proxyNames = %q, want the tail summarized", got)
	}
}

// --- Identity matching ---

// The four platforms actually in the fleet, with sysDescr as their agents serve
// it. The MikroTik line and its sysObjectID are OBSERVED (cf. snmppoll/health.go:
// CSS326-24G-2S+ under SwOS 2.17 answering 1.3.6.1.4.1.14988.2).
func TestMatchSNMPIdentityAcceptsTheFleet(t *testing.T) {
	var cases = []struct {
		name, vendor, model, descr, oid string
	}{
		{"mikrotik swos", "MikroTik", "CSS326-24G-2S+",
			"CSS326-24G-2S+ SwOS v2.17", "1.3.6.1.4.1.14988.2"},
		{"fs s3900", "FS.com", "S3900-24T4S",
			"S3900-24T4S", ""},
		{"instant on 1930", "Aruba (HPE)",
			"HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
			"HPE Networking Instant On 1930 48G Class4 PoE 4SFP/SFP+ 370W Switch", ""},
		{"catalyst", "Cisco", "WS-C2960X-24TD-L",
			"Cisco IOS Software, C2960X Software (C2960X-UNIVERSALK9-M), Version 15.2(6)E",
			"1.3.6.1.4.1.9.1.2494"},
	}
	var c struct{ name, vendor, model, descr, oid string }
	for _, c = range cases {
		var why, ok = matchSNMPIdentity(c.vendor, c.model, c.descr, c.oid)
		if !ok {
			t.Errorf("%s: not confirmed, so this platform could never outrank a "+
				"proxy that reaches nothing (descr %q)", c.name, c.descr)
			continue
		}
		if why == "" {
			t.Errorf("%s: confirmed with no reason recorded", c.name)
		}
	}
}

// THE CASE CONFIRMATION EXISTS FOR: something else answers at that address.
func TestMatchSNMPIdentityRejectsAnotherDevice(t *testing.T) {
	var _, ok = matchSNMPIdentity("MikroTik", "CSS326-24G-2S+",
		"S3900-24T4S", "")
	if ok {
		t.Error("a different model was accepted as this device")
	}
}

// The vendor alone must NEVER confirm: it would accept any MikroTik at that
// address as being this MikroTik, which is exactly the recycled-address case.
func TestMatchSNMPIdentityRefusesVendorOnly(t *testing.T) {
	var _, ok = matchSNMPIdentity("MikroTik", "CSS326-24G-2S+",
		"RB5009UG+S+ RouterOS 7.21", "1.3.6.1.4.1.14988.1")
	if ok {
		t.Error("a same-vendor different model was accepted")
	}
}

// A silent or MIB-less agent leaves the device unconfirmed, which is a legitimate
// state (rule 3), not an error.
func TestMatchSNMPIdentityWithoutSysDescr(t *testing.T) {
	var _, ok = matchSNMPIdentity("MikroTik", "CSS326-24G-2S+", "", "1.3.6.1.4.1.14988.2")
	if ok {
		t.Error("confirmed with nothing to match against")
	}
}

// A device entered by hand with no model cannot be confirmed. Also fine.
func TestMatchSNMPIdentityWithoutModel(t *testing.T) {
	var _, ok = matchSNMPIdentity("MikroTik", "", "CSS326-24G-2S+ SwOS v2.17", "")
	if ok {
		t.Error("confirmed a device whose model we never recorded")
	}
}

// Punctuation differs between a datasheet and an agent, and carries no meaning.
func TestMatchSNMPIdentityIgnoresPunctuationAndCase(t *testing.T) {
	var _, ok = matchSNMPIdentity("MikroTik", "css326-24g-2s+", "CSS 326 24G 2S SwOS", "")
	if !ok {
		t.Error("punctuation and case defeated the match")
	}
}

// Only distinctive tokens count: matching on "Switch" would confirm every switch
// in the world as every other.
func TestIdentityTokensKeepOnlyDistinctiveParts(t *testing.T) {
	var got = identityTokens("HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A")
	var joined = strings.Join(got, ",")
	if strings.Contains(joined, "networking") || strings.Contains(joined, "switch") {
		t.Errorf("tokens = %v, want marketing words dropped", got)
	}
	if !strings.Contains(joined, "1930") || !strings.Contains(joined, "jl685a") {
		t.Errorf("tokens = %v, want the product identifiers kept", got)
	}
	// "4p" and "48p" are too short / too generic to be evidence.
	var tok string
	for _, tok = range got {
		if len(tok) < 4 {
			t.Errorf("token %q is too short to be evidence", tok)
		}
	}
}

func TestEnterpriseVendorOnlyClaimsWhatIsKnown(t *testing.T) {
	if enterpriseVendor("1.3.6.1.4.1.14988.2") != "MikroTik" {
		t.Error("the observed MikroTik enterprise arc was not recognized")
	}
	if enterpriseVendor("1.3.6.1.4.1.9.1.2494") != "Cisco" {
		t.Error("Cisco's enterprise arc was not recognized")
	}
	// An unsurveyed vendor returns nothing rather than a guess: the model match
	// is what confirms, and inventing numbers would put fiction in the file that
	// decides what to believe.
	if enterpriseVendor("1.3.6.1.4.1.259.10.1") != "" {
		t.Error("an unsurveyed enterprise number was given a vendor")
	}
	if enterpriseVendor("1.3.6.1.2.1.1.2") != "" {
		t.Error("a non-enterprise OID was mapped to a vendor")
	}
	if enterpriseVendor("") != "" {
		t.Error("empty sysObjectID produced a vendor")
	}
}

func TestVendorMatchesAcrossVocabularies(t *testing.T) {
	if !vendorMatches("Aruba (HPE)", "Hewlett-Packard") {
		t.Error("the inventory vendor and the IANA name were not reconciled")
	}
	if !vendorMatches("MikroTik", "MikroTik") {
		t.Error("identical vendors did not match")
	}
	if vendorMatches("MikroTik", "Cisco") {
		t.Error("two different vendors matched")
	}
	if vendorMatches("", "Cisco") {
		t.Error("an empty inventory vendor matched")
	}
}

// --- MST region derived from the site ---

// ALL KNOWN VLANs IN INSTANCE 1 is the product default, and it has to be
// reproducible from the site's own data: the region digest covers the full
// VLAN-to-instance table, so two devices computing it from the same site must
// arrive at byte-identical input or they land in different regions without
// anything saying so.
func TestStpRegionRevisionDefaultsToOne(t *testing.T) {
	// Not 0: an untouched device carries 0, and a meshcraft-managed region has to
	// be distinguishable from a factory one at a glance.
	if stpRegionRevisionOf(nil) != 1 {
		t.Errorf("revision with no plan = %d, want 1", stpRegionRevisionOf(nil))
	}
	if stpRegionRevisionOf([]byte(`{}`)) != 1 {
		t.Errorf("revision with an empty plan = %d, want 1", stpRegionRevisionOf([]byte(`{}`)))
	}
	if stpRegionRevisionOf([]byte(`not json`)) != 1 {
		t.Error("an unreadable plan must fall back to the default rather than 0")
	}
	var got = stpRegionRevisionOf([]byte(`{"stp_mst_revision":7}`))
	if got != 7 {
		t.Errorf("explicit revision = %d, want 7", got)
	}
	// An explicit 0 is honoured: matching a fleet already running revision 0 is a
	// legitimate thing to ask for.
	got = stpRegionRevisionOf([]byte(`{"stp_mst_revision":0}`))
	if got != 0 {
		t.Errorf("explicit zero revision = %d, want 0", got)
	}
}

// A DEVICE WHOSE ONLY OBSERVER IS GONE IS NOT ONLINE, and it is not offline
// either. Observed 2026-08-02: a switch kept reporting "ok — seen by
// proxy-site2" for twelve minutes after that proxy lost its tunnel. Nobody
// was looking and the screen said everything was fine.
func TestJudgeReachIsIndeterminateWhenEveryObserverIsOffline(t *testing.T) {
	var dead = func(string) bool { return false }
	var v = judgeReach([]model.ProxyReach{up("proxy-site2", true)}, dead)
	if v.Status != "unknown" {
		t.Fatalf("status = %q, want unknown: a stale green is the worst answer here", v.Status)
	}
	if !strings.Contains(v.Note, "proxy-site2") {
		t.Errorf("note = %q, must name the proxy that is missing", v.Note)
	}
	if !strings.Contains(v.Note, "PROXY outage") {
		t.Errorf("note = %q, must point at the proxy rather than the device", v.Note)
	}
}

// A live proxy still decides, even when a dead one disagrees.
func TestJudgeReachIgnoresOfflineProxiesButKeepsLiveOnes(t *testing.T) {
	var onlyLab = func(id string) bool { return id == "proxy-lab" }
	var v = judgeReach([]model.ProxyReach{
		down("proxy-site2", true), // gone, and its last word was "down"
		up("proxy-lab", true),
	}, onlyLab)
	if v.Status != "ok" {
		t.Fatalf("status = %q, want ok: the connected proxy sees it", v.Status)
	}
}

// And a live proxy reporting down is still an outage: liveness filters observers,
// it does not soften verdicts.
func TestJudgeReachStillReportsOutagesFromLiveProxies(t *testing.T) {
	var allLive = func(string) bool { return true }
	var v = judgeReach([]model.ProxyReach{down("proxy-lab", true)}, allLive)
	if v.Status != "off" {
		t.Fatalf("status = %q, want off", v.Status)
	}
}

// THE SECOND HALF OF THE SAME LIE. A live proxy that has never been able to
// route to a device must not turn it red when the proxy that CAN see it goes
// offline. Observed 2026-08-02: proxy-lab, connected and unable to reach
// 203.0.113.0/24, reported a healthy switch unreachable.
func TestJudgeReachIgnoresALiveProxyThatHasNeverSeenTheDevice(t *testing.T) {
	var labLive = func(id string) bool { return id == "proxy-lab" }
	// lab: live, never confirmed, never up — not a witness.
	// site2: offline, but it confirmed and saw the device.
	var v = judgeReach([]model.ProxyReach{
		down("proxy-lab", false),
		up("proxy-site2", true),
	}, labLive)
	if v.Status != "unknown" {
		t.Fatalf("status = %q, want unknown: the only real witness is offline", v.Status)
	}
	if !strings.Contains(v.Note, "proxy-site2") {
		t.Errorf("note = %q, must name the witness that went missing", v.Note)
	}
}

// But a live proxy that HAS seen the device keeps its authority to report it down.
func TestJudgeReachTrustsALiveProxyThatHasSeenTheDevice(t *testing.T) {
	var allLive = func(string) bool { return true }
	var v = judgeReach([]model.ProxyReach{down("proxy-lab", true)}, allLive)
	if v.Status != "off" {
		t.Fatalf("status = %q, want off: a proven witness lost it", v.Status)
	}
}

// --- The startup hold, and the five things it must not break ---
//
// These cover the ~9 second window measured at controller startup: a proxy that has
// reconnected reports a CONFIRMED down while the proxy that actually reaches the device
// is not back yet, rule 2 answers "off", and nine seconds later it turns green again.
// The hold defers the WRITE; it never changes the verdict, and every case below is one
// of the ways that distinction can be lost.

// settledExcept builds the predicate holdReach consumes: everyone has finished
// restating what they know, except the named proxies.
func settledExcept(pending ...string) func(string) bool {
	var waiting = map[string]bool{}
	var p string
	for _, p = range pending {
		waiting[p] = true
	}
	return func(proxyID string) bool { return !waiting[proxyID] }
}

// THE FAULT ITSELF. Inside the startup window, a confirmed "off" is not written while a
// proxy that has seen this device before has not finished resyncing.
func TestHoldReachWaitsForAWitnessStillResyncing(t *testing.T) {
	var obs = []model.ProxyReach{
		down("proxy-site2", true), // back, confirmed, and it no longer reaches it
		up("proxy-lab", true),     // the real witness, not yet caught up
	}
	var v = judgeReach(obs, func(id string) bool { return id == "proxy-site2" })
	if v.Status != "off" {
		t.Fatalf("precondition: judgeReach = %q, want off — this test covers the "+
			"premature off, so it must reproduce it first", v.Status)
	}
	var hold, why = holdReach(v, obs, settledExcept("proxy-lab"), 2*time.Second)
	if !hold {
		t.Fatal("the write was not held while the device's witness was still resyncing")
	}
	if !strings.Contains(why, "proxy-lab") {
		t.Errorf("reason = %q, must name what we are waiting for", why)
	}
}

// The same proxy, one moment later. Nothing is waited for any more, so the verdict —
// unchanged, since the hold never touched it — is written.
func TestHoldReachReleasesOnceTheWitnessHasResynced(t *testing.T) {
	var obs = []model.ProxyReach{
		down("proxy-site2", true),
		up("proxy-lab", true),
	}
	var v = judgeReach(obs, func(id string) bool { return id == "proxy-site2" })
	var hold, _ = holdReach(v, obs, settledExcept(), 2*time.Second)
	if hold {
		t.Fatal("still holding after every witness finished restating what it knows")
	}
}

// THE REGRESSION THAT MATTERS MOST. A hold that hides a real outage is worse than the
// window it closes: once the witnesses have spoken, a genuinely dead device reads down,
// startup window or not.
func TestHoldReachDoesNotHideADeadDevice(t *testing.T) {
	var obs = []model.ProxyReach{
		down("proxy-lab", true), // the witness itself, caught up, and it is gone
		down("proxy-site2", true),
	}
	var v = judgeReach(obs, func(string) bool { return true })
	if v.Status != "off" {
		t.Fatalf("precondition: judgeReach = %q, want off", v.Status)
	}
	var hold, _ = holdReach(v, obs, settledExcept(), time.Second)
	if hold {
		t.Fatal("a confirmed outage was held although every witness had resynced: " +
			"this is the fault the hold must never introduce")
	}
}

// THE OLD PROXY, and this is the single most important property of the change: a proxy
// that never sends the marker is settled the instant it connects, so a fleet that has
// not been upgraded behaves exactly as it did before — no hold, at any point in the
// window. `settled` returning true everywhere is what an all-old fleet looks like from
// here (cf. Server.reachSettled, which maps tunnel.ResyncUnsupported to settled).
func TestHoldReachDoesNothingForProxiesThatCannotResync(t *testing.T) {
	var obs = []model.ProxyReach{
		down("proxy-site2", true),
		up("proxy-lab", true),
	}
	var v = judgeReach(obs, func(id string) bool { return id == "proxy-site2" })
	var alwaysSettled = func(string) bool { return true }
	var hold, why = holdReach(v, obs, alwaysSettled, 0)
	if hold {
		t.Fatalf("a fleet of proxies that cannot resync was held (%q): the protocol "+
			"change would freeze every controller restart until the last binary is "+
			"upgraded", why)
	}
}

// THE BOUND, which is the safety property. A witness that never resyncs — a proxy that
// stays down, or one decommissioned with rows still in `proxy_reach` — must cost the
// window and not one second more. Past it, today's answer stands.
func TestHoldReachExpiresWithTheStartupWindow(t *testing.T) {
	var obs = []model.ProxyReach{
		down("proxy-site2", true),
		up("proxy-lab", true),
	}
	var v = judgeReach(obs, func(id string) bool { return id == "proxy-site2" })
	var neverBack = settledExcept("proxy-lab")
	var hold, _ = holdReach(v, obs, neverBack, reachStartupWindow-time.Second)
	if !hold {
		t.Fatal("released before the window closed")
	}
	hold, _ = holdReach(v, obs, neverBack, reachStartupWindow)
	if hold {
		t.Fatal("still holding at the end of the window: the hold has no bound, and a " +
			"device whose observer never returns would keep a stale colour forever")
	}
	hold, _ = holdReach(v, obs, neverBack, time.Hour)
	if hold {
		t.Fatal("still holding an hour after startup")
	}
}

// A Server that never recorded a start time reads as infinitely old, and a nil
// predicate is a caller with no hub. Both must fall back to today's behaviour: an
// unknown clock is a reason to write, never a reason to wait.
func TestHoldReachFallsBackWhenItCannotKnow(t *testing.T) {
	var obs = []model.ProxyReach{down("proxy-lab", true), up("proxy-other", true)}
	var v = judgeReach(obs, nil)
	var hold, _ = holdReach(v, obs, nil, time.Second)
	if hold {
		t.Error("held with no way to ask whether anyone had resynced")
	}
	hold, _ = holdReach(v, obs, settledExcept("proxy-other"), time.Since(time.Time{}))
	if hold {
		t.Error("held on an unset start clock")
	}
}

// GOOD NEWS IS NEVER HELD. The hold exists to stop a premature red; delaying a green
// would make a screen darker than the evidence, which is the fault it is repairing.
func TestHoldReachNeverDelaysARecovery(t *testing.T) {
	var obs = []model.ProxyReach{up("proxy-lab", true), down("proxy-other", true)}
	var v = judgeReach(obs, func(string) bool { return true })
	if v.Status != "ok" {
		t.Fatalf("precondition: judgeReach = %q, want ok", v.Status)
	}
	var hold, _ = holdReach(v, obs, settledExcept("proxy-other"), time.Second)
	if hold {
		t.Error("a device seen by a confirmed proxy was held")
	}
}

// "unknown" is judgeReach saying NOBODY CAN SEE THIS DEVICE — a proxy outage, and the
// one thing an operator must be told immediately at startup. Holding it would suppress
// the only signal that says "your monitoring is not working".
func TestHoldReachDoesNotSuppressAProxyOutage(t *testing.T) {
	var obs = []model.ProxyReach{up("proxy-lab", true)}
	var v = judgeReach(obs, func(string) bool { return false })
	if v.Status != "unknown" {
		t.Fatalf("precondition: judgeReach = %q, want unknown", v.Status)
	}
	var hold, _ = holdReach(v, obs, settledExcept("proxy-lab"), time.Second)
	if hold {
		t.Error("a proxy outage was hidden by the startup hold")
	}
}

// A proxy that has never seen this device is not worth waiting for. It is the same
// credibility test judgeReach applies, and it must stay the same one: waiting on an
// observer whose opinion would be discarded anyway delays a verdict for nothing.
func TestHoldReachIgnoresProxiesThatHaveNeverSeenTheDevice(t *testing.T) {
	var obs = []model.ProxyReach{
		down("proxy-lab", true),        // confirmed, and it has lost the device
		silent("proxy-just-handed-it"), // never confirmed, never up: not a witness
	}
	var v = judgeReach(obs, func(string) bool { return true })
	if v.Status != "off" {
		t.Fatalf("precondition: judgeReach = %q, want off", v.Status)
	}
	var hold, _ = holdReach(v, obs, settledExcept("proxy-just-handed-it"), time.Second)
	if hold {
		t.Error("held for a proxy that has never seen this device")
	}
}

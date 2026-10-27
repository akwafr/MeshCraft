package api

import (
	"strings"
	"testing"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/tunnel"
)

// Trap destination placement, from the API side.
//
// What these tests lock in: decisions that, taken wrong, would produce a SILENT
// trap channel — a fleet declared reactive where no event ever arrives, indistinguishable
// on screen from a calm network. The trap channel has no loud failure mode: a trap
// rejected by the proxy only increments a DEBUG counter, and a device emitting into
// the void stays supervised by polling. That is exactly the kind of defect discovered
// six months later, which is why it is locked here rather than observed in production.
//
// Without a database, like the rest of the package: judgeTrapDest, trapDestRefusal,
// trapDestinationFrom, judgeTrapProof, and the two phrase builders were written PURE
// so they can be verified here, following judgeCollector, effectiveInterval, and
// judgeAskSNMP.
//
// What a test in this package CANNOT cover, and it must be said so this list is not
// mistaken for end-to-end proof:
//
//   - the SQL. That SetDeviceProxy clears the stored destination, that SetTrapDestination
//     keeps confirmation on re-placement, and that ConfirmTrapPath's `where trap_seen_at
//     is null` really bounds logging to once: these three semantics live in case/where
//     clauses, replayed in store/devices_test.go. The SQL itself would need PostgreSQL;
//   - the QUESTION to the proxy (tunnel.AskSourceAddr). It requires a tunnel, therefore
//     a proxy. What is tested here is the consequence of an EMPTY response, not the question;
//   - writing on the device. driver.TrapPusher.ApplyTrapReceiver is, on Instant On gear,
//     a gesture reconstructed from the device bundle, never executed on real hardware.
//     No Go test can validate that — only first placement on a real switch will;
//   - the full HTTP path: site_admin rights, response shape, log.

var trapCouvrants = []model.Proxy{
	{ID: "local", Name: "Local", Global: true, Internal: true},
	{ID: "branch-a", Name: "Branch A"},
}

func posable() trapDestInputs {
	return trapDestInputs{
		deviceName: "sw-labo-1930", mgmtIP: "198.51.100.101",
		collector: "branch-a", collectorName: "Branch A",
		plan: planV2c(), credentialID: "cred-site", trapsPush: true,
	}
}

func TestTrapDestRefusesWithoutCollector(t *testing.T) {
	var in = posable()
	in.collector, in.collectorName = "", ""
	var verdict, p = judgeTrapDest(in, trapCouvrants)
	if verdict != trapDestNoCollector {
		t.Fatalf("without a declared collector, placement must be refused, verdict %v", verdict)
	}
	if p.ID != "" {
		t.Errorf("a refusal must designate no proxy, got %q", p.ID)
	}
	var msg = trapDestRefusal(verdict, in)
	var want = []string{"sw-labo-1930", "collector", "Attach", "random"}
	var word string
	for _, word = range want {
		if !strings.Contains(msg, word) {
			t.Errorf("the refusal must mention %q: %s", word, msg)
		}
	}
	if strings.Contains(msg, "no proxy covers") {
		t.Errorf("the refusal confuses \"no proxy\" and \"no choice\": %s", msg)
	}
}

func TestTrapDestRefusesNonCoveringCollector(t *testing.T) {
	var in = posable()
	in.collector, in.collectorName = "branch-b", "Branch B"
	var verdict, _ = judgeTrapDest(in, trapCouvrants)
	if verdict != trapDestCollectorNotCovering {
		t.Fatalf("an out-of-coverage collector must be refused, verdict %v", verdict)
	}
	var msg = trapDestRefusal(verdict, in)
	if !strings.Contains(msg, "Branch B") || !strings.Contains(msg, "coverage") {
		t.Errorf("the refusal must name the proxy and coverage: %s", msg)
	}
	if !strings.Contains(msg, "anybody") {
		t.Errorf("the refusal must state the real consequence (collected by anybody): %s", msg)
	}
}

func TestTrapDestRefusesV3(t *testing.T) {
	var in = posable()
	in.plan = planV3()
	var verdict, _ = judgeTrapDest(in, trapCouvrants)
	if verdict != trapDestV3Only {
		t.Fatalf("a v3 device must be refused, verdict %v", verdict)
	}
	var msg = trapDestRefusal(verdict, in)
	var want = []string{"SNMPv3", "community", "reject", "POLLED"}
	var word string
	for _, word = range want {
		if !strings.Contains(msg, word) {
			t.Errorf("the v3 refusal must mention %q: %s", word, msg)
		}
	}
}

func TestTrapDestRefusesNoPushOrVault(t *testing.T) {
	var in = posable()
	in.trapsPush = false
	in.credentialID = ""
	var verdict, _ = judgeTrapDest(in, trapCouvrants)
	if verdict != trapDestNoTrapPush {
		t.Fatalf("missing push capability comes before the vault, verdict %v", verdict)
	}
	var msg = trapDestRefusal(verdict, in)
	if !strings.Contains(msg, "Nothing to fix") {
		t.Errorf("a connector without traps is not an anomaly: %s", msg)
	}

	in.trapsPush = true
	verdict, _ = judgeTrapDest(in, trapCouvrants)
	if verdict != trapDestNoCredential {
		t.Fatalf("without a vault credential, verdict %v", verdict)
	}
	if !strings.Contains(trapDestRefusal(verdict, in), "polling") {
		t.Error("the refusal must say the community is that of polling")
	}
}

func TestTrapDestRefusesLocalProxy(t *testing.T) {
	var in = posable()
	in.collector, in.collectorName = "local", "Local"
	var verdict, _ = judgeTrapDest(in, trapCouvrants)
	if verdict != trapDestCollectorInternal {
		t.Fatalf("the instance proxy does not listen for traps, verdict %v", verdict)
	}
	if !strings.Contains(trapDestRefusal(verdict, in), "tunnel") {
		t.Error("the refusal must say what is missing: the tunnel, therefore listening")
	}
}

func TestTrapDestAcceptable(t *testing.T) {
	var verdict, p = judgeTrapDest(posable(), trapCouvrants)
	if verdict != trapDestReady {
		t.Fatalf("everything in place, verdict %v", verdict)
	}
	if p.ID != "branch-a" || p.Name != "Branch A" {
		t.Errorf("the collector must be returned with its name, got %q/%q", p.ID, p.Name)
	}
	var in = posable()
	in.mgmtIP = ""
	verdict, _ = judgeTrapDest(in, trapCouvrants)
	if verdict != trapDestNoMgmtIP {
		t.Fatalf("without a management address, verdict %v", verdict)
	}
}

func TestTrapCommunityIsPollingCommunity(t *testing.T) {
	var watch = tunnel.SNMPWatch{
		DeviceID: "dev-1", IP: "198.51.100.101", Community: "s1te-labo-ro",
	}
	var dest, refusal = trapDestinationFrom(watch, "10.42.0.67")
	if refusal != "" {
		t.Fatalf("unexpected refusal: %s", refusal)
	}
	if dest.Community != watch.Community {
		t.Fatalf("trap recipient community must be polling community: got %q want %q",
			dest.Community, watch.Community)
	}
	if dest.Community == "mctrap" {
		t.Fatal("connector survey community must not be copied")
	}
	if dest.User != "" {
		t.Errorf("placement must stay v2c, got User %q", dest.User)
	}
	if dest.Port != tunnel.DefaultTrapPort {
		t.Errorf("port %d instead of %d", dest.Port, tunnel.DefaultTrapPort)
	}
	if dest.Address != "10.42.0.67" {
		t.Errorf("address %q instead of proxy-returned one", dest.Address)
	}
}

func TestNoInventedCommunity(t *testing.T) {
	var dest, refusal = trapDestinationFrom(
		tunnel.SNMPWatch{DeviceID: "dev-1", IP: "198.51.100.101"}, "10.42.0.67")
	if refusal == "" {
		t.Fatal("an empty community must be a refusal, not a fallback")
	}
	if dest.Community != "" {
		t.Errorf("no community must be invented, got %q", dest.Community)
	}
	if !strings.Contains(refusal, "polling") {
		t.Errorf("the refusal must recall where the community comes from: %s", refusal)
	}
}

func TestEmptyAddrIsRefusal(t *testing.T) {
	var _, refusal = trapDestinationFrom(
		tunnel.SNMPWatch{Community: "s1te-labo-ro"}, "")
	if refusal == "" {
		t.Fatal("without a source address, placement must be refused")
	}
	if !strings.Contains(refusal, "routing") {
		t.Errorf("the refusal must say why the address cannot be deduced: %s", refusal)
	}
}

func TestPlacementAndConfirmationAreTwoStates(t *testing.T) {
	var posed = trapPosedMessage("sw-labo-1930", "10.42.0.67", false)
	if !strings.Contains(posed, "10.42.0.67") {
		t.Errorf("placement must say WHICH address was written: %s", posed)
	}
	if !strings.Contains(posed, "does NOT prove") {
		t.Errorf("placement must say it is not proven: %s", posed)
	}
	if strings.Contains(posed, "ESTABLISHED") {
		t.Errorf("placement must not read like confirmation: %s", posed)
	}

	var proved = trapConfirmedMessage("sw-labo-1930", "10.42.0.67", "Branch A", true)
	if !strings.Contains(proved, "ESTABLISHED") || !strings.Contains(proved, "Branch A") {
		t.Errorf("confirmation must assert and name the proxy: %s", proved)
	}
	if proved == posed {
		t.Fatal("placed and confirmed must read differently")
	}

	var again = trapPosedMessage("sw-labo-1930", "10.42.0.67", true)
	if strings.Contains(again, "does NOT prove") {
		t.Errorf("identical re-placement must not lose confirmation: %s", again)
	}
}

func TestProofBelongsToWrittenDestOnly(t *testing.T) {
	if judgeTrapProof("10.42.0.67", "branch-a", "10.42.0.67", "branch-a") != trapProofConfirms {
		t.Error("the written destination's proxy at the written address confirms")
	}
	if judgeTrapProof("10.42.0.67", "branch-a", "10.42.0.67", "branch-b") != trapProofForeign {
		t.Error("another proxy does not confirm the written destination")
	}
	if judgeTrapProof("", "", "10.42.0.67", "branch-a") != trapProofForeign {
		t.Error("without a written destination, there is nothing to confirm")
	}
}

func TestProofRequiresWrittenAddrNotJustProxy(t *testing.T) {
	var v = judgeTrapProof("10.42.0.67", "branch-a", "172.17.0.1", "branch-a")
	if v != trapProofOtherAddr {
		t.Fatalf("a trap arriving on another address does not confirm the written destination, verdict %v", v)
	}
	if v == trapProofConfirms || v == trapProofUnattributed {
		t.Error("a differing address is a refusal, not a nuance")
	}
}

func TestProofUnattributedWhenProxySilent(t *testing.T) {
	var v = judgeTrapProof("10.42.0.67", "branch-a", "", "branch-a")
	if v != trapProofUnattributed {
		t.Fatalf("unknown destination nuance the proof, it does not cancel it, verdict %v", v)
	}
	if v == trapProofOtherAddr {
		t.Error("\"I do not know\" and \"not the right address\" are two verdicts")
	}
}

func TestProofSaysNoMoreThanItProves(t *testing.T) {
	var full = trapConfirmedMessage("sw-labo-1930", "10.42.0.67", "Lab", true)
	if !strings.Contains(full, "ESTABLISHED") || !strings.Contains(full, "verified") {
		t.Errorf("attributed proof may assert: %s", full)
	}
	if !strings.Contains(full, "very address") {
		t.Errorf("it must say WHY it asserts: %s", full)
	}

	var weak = trapConfirmedMessage("sw-labo-1930", "10.42.0.67", "Lab", false)
	if weak == full {
		t.Fatal("the two proof grades must read differently")
	}
	if !strings.Contains(weak, "REACH") || !strings.Contains(weak, "reactive") {
		t.Errorf("weak proof must state the established fact: %s", weak)
	}
	if strings.Contains(weak, "verified") || strings.Contains(weak, "ESTABLISHED") {
		t.Errorf("unattributed proof must not assert the placed path: %s", weak)
	}
	if !strings.Contains(weak, "not yet proven") || !strings.Contains(weak, "hypothesis") {
		t.Errorf("it must name what is missing: %s", weak)
	}
	if !strings.Contains(weak, "10.42.0.67") {
		t.Errorf("the phrase must say which address we mean: %s", weak)
	}
}

func TestProxyTimestampProvesNothing(t *testing.T) {
	var now = time.Date(2026, 7, 28, 10, 29, 50, 280_000_000, time.UTC)
	var duParc = trapClockNote(
		time.Date(2026, 7, 28, 10, 29, 39, 250_000_000, time.UTC), now)
	if duParc == "" {
		t.Fatal("eleven seconds of skew must be reported")
	}
	if !strings.Contains(duParc, "behind") || !strings.Contains(duParc, "11") {
		t.Errorf("the note must state direction and measure: %s", duParc)
	}
	if trapClockNote(now.Add(-time.Second), now) != "" {
		t.Error("one second behind is not a symptom")
	}
	var ahead = trapClockNote(now.Add(2*time.Second), now)
	if !strings.Contains(ahead, "AHEAD") || !strings.Contains(ahead, "diverge") {
		t.Errorf("a future date must be stated as such: %s", ahead)
	}
	if trapClockNote(time.Time{}, now) != "" {
		t.Error("an absent date is not compared to anything")
	}
}

package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"meshcraft/internal/model"
)

// What these tests protect: four port transition correlation rules — log an ordinary
// transition, aggregate flapping, say WHICH stable state it settled in, and do not
// double-count what two proxies saw once. Plus two properties invisible on has
// working fleet: memory bound and absence of races.
//
// All rules are TIME rules. The clock is therefore has parameter: proving "three
// minutes of calm" by waiting three minutes would make the test unusable, and
// proving the same on shortened time would no longer exercise real constants.
//
// That parameter is the CONTROLLER clock, passed to observe and sweep — the
// transition itself carries no timestamp. The two tests at the end of this file
// lock that: an injected test clock is not has proxy clock, and nothing must let
// the second replace the first.

// t0: readable timestamp base for failure messages.
var t0 = time.Date(2026, 7, 28, 14, 12, 0, 0, time.UTC)

// tick builds has transition on port 12 of "d1".
//
// No date: the controller timestamps what it correlates, so the instant is passed
// to observe, not to the transition (cf. portevents.go, "One clock"). Calls read
// "the controller observes this transition at this instant", which is exactly what
// happens.
func tick(up bool, proxy string) portTransition {
	return portTransition{
		DeviceID: "d1", IfIndex: 12, PortName: "12", Up: up,
		ProxyID: proxy, Trigger: "poll",
	}
}

// kinds reduces decisions to their kinds for readable comparisons.
func kinds(decisions []portDecision) string {
	var out = make([]string, 0, len(decisions))
	var d portDecision
	for _, d = range decisions {
		out = append(out, d.Kind)
	}
	return strings.Join(out, ",")
}

// An isolated transition is logged as-is: nominal case, and also what over-eager
// aggregation would erase.
func TestPortTransitionSimple(t *testing.T) {
	var pf = newPortFlaps()

	var down = pf.observe(tick(false, "p1"), t0)
	if kinds(down) != portLogTransition {
		t.Fatalf("isolated transition must be logged: %q", kinds(down))
	}
	if down[0].Up || down[0].IfIndex != 12 || down[0].PortName != "12" {
		t.Errorf("decision badly filled: %+v", down[0])
	}

	// Legitimate round trip: port returns thirty seconds later. BOTH transitions
	// count — exactly what deduplication must not confuse with an echo.
	var up = pf.observe(tick(true, "p1"), t0.Add(30*time.Second))
	if kinds(up) != portLogTransition || !up[0].Up {
		t.Fatalf("port return must be logged: %q", kinds(up))
	}
}

// Without ifIndex there is no key: port name cannot be fallback, because it
// collides. We prefer correlating nothing to correlating two different ports
// together.
func TestPortTransitionSansIdentifiant(t *testing.T) {
	var pf = newPortFlaps()
	if pf.observe(portTransition{DeviceID: "d1", PortName: "42"}, t0) != nil {
		t.Error("event without ifIndex is not correlatable")
	}
	if pf.observe(portTransition{IfIndex: 3}, t0) != nil {
		t.Error("event without device is not correlatable")
	}
	if len(pf.states) != 0 {
		t.Error("uncorrelatable event must not leave a key behind")
	}
}

// Heart of the batch: at threshold, stop writing one entry per transition and
// write one aggregate — then go SILENT. If the aggregate did not impose silence,
// it would save nothing.
func TestPortFlappingMonte(t *testing.T) {
	var pf = newPortFlaps()
	var at = t0
	var i int
	var written int
	var flapStarts int

	// Ten flaps in twenty seconds: burst the trap channel makes possible (advanced
	// poll at most every two seconds, cf. TrapMinPollGap).
	for i = 0; i < 10; i++ {
		at = at.Add(2 * time.Second)
		var decisions = pf.observe(tick(i%2 == 0, "p1"), at)
		var d portDecision
		for _, d = range decisions {
			switch d.Kind {
			case portLogTransition:
				written++
			case portLogFlapStart:
				flapStarts++
				if d.Count != flapThreshold {
					t.Errorf("aggregate must carry %d window transitions, not %d",
						flapThreshold, d.Count)
				}
			default:
				t.Errorf("unexpected kind: %s", d.Kind)
			}
		}
	}
	if flapStarts != 1 {
		t.Fatalf("flapping must be announced ONCE, not %d", flapStarts)
	}
	if written != flapThreshold-1 {
		t.Fatalf("first %d transitions are logged, then nothing: %d written",
			flapThreshold-1, written)
	}
}

// Flapping that settles on up: port is repaired. Log must say so, and it can only
// say so on has clock — flap end is absence of events, never an event.
func TestPortFlappingSeCalmeEnUp(t *testing.T) {
	var pf, at = flappingSur12(t)

	// Nothing closes before flapCalm of silence: otherwise has port flapping slower
	// than we observe would be declared calm between seen transitions, then flapping
	// again right after.
	if len(pf.sweep(at.Add(flapCalm-time.Second))) != 0 {
		t.Fatal("flapping closes after calm, not before")
	}

	// Last retained transition was up (cf. flappingSur12).
	var closed = pf.sweep(at.Add(flapCalm))
	if kinds(closed) != portLogFlapEnd {
		t.Fatalf("calm must close flapping: %q", kinds(closed))
	}
	if !closed[0].Up {
		t.Error("flapping that stops on up leaves port BACK IN SERVICE")
	}
	if closed[0].Count < flapThreshold {
		t.Errorf("exit must carry full counter: %d", closed[0].Count)
	}
	// Closed once: second sweep must not re-announce exit.
	if len(pf.sweep(at.Add(2*flapCalm))) != 0 {
		t.Error("flap exit already written must not repeat")
	}
	// Port becomes ordinary again: next transition is logged.
	var next = pf.observe(tick(false, "p1"), at.Add(2*flapCalm))
	if kinds(next) != portLogTransition {
		t.Errorf("after closed flapping, start fresh: %q", kinds(next))
	}
}

// Opposite case, and most important for an operator: port stopped flapping because
// the link is dead for good. Confusing the two would pass has failure for has repair.
func TestPortFlappingSeCalmeEnDown(t *testing.T) {
	var pf = newPortFlaps()
	var at = t0
	var i int
	for i = 0; i < 8; i++ {
		at = at.Add(2 * time.Second)
		// even i -> up, so last (i = 7) is down.
		pf.observe(tick(i%2 == 0, "p1"), at)
	}

	var closed = pf.sweep(at.Add(flapCalm))
	if kinds(closed) != portLogFlapEnd {
		t.Fatalf("calm must close flapping: %q", kinds(closed))
	}
	if closed[0].Up {
		t.Fatal("flapping that stops on down leaves port OUT OF SERVICE")
	}
}

// Flap end can also be observed on an event when sweep has not run yet. It must
// then be written BEFORE the transition that woke it, otherwise the log would
// announce flap exit after the next flap started.
func TestPortFlappingClosParUnEvenementTardif(t *testing.T) {
	var pf, at = flappingSur12(t)

	var decisions = pf.observe(tick(false, "p1"), at.Add(flapCalm+time.Minute))
	if kinds(decisions) != portLogFlapEnd+","+portLogTransition {
		t.Fatalf("want flap exit then transition: %q", kinds(decisions))
	}
	if !decisions[0].Up {
		t.Error("exit announces STABLE state, not the following transition's state")
	}
}

// Two global proxies poll the same device (proxies.global: two in database) and
// EACH reports the same transition. That is one event, not two — and it must not
// count twice toward flapping either, or threshold would be hit twice too soon.
func TestPortDedoublonnageInterProxys(t *testing.T) {
	var pf = newPortFlaps()

	var first = pf.observe(tick(false, "Local"), t0)
	if kinds(first) != portLogTransition {
		t.Fatalf("first observation is the event: %q", kinds(first))
	}
	// Second proxy sees same drop four seconds later.
	if pf.observe(tick(false, "proxy-akwalan"), t0.Add(4*time.Second)) != nil {
		t.Fatal("same drop seen by second proxy is not a second event")
	}

	// Criterion cannot be "same state recently": real fast down -> up -> down must
	// pass IN FULL. Each transition is compared to PREVIOUS retained one, which is
	// necessarily the opposite state.
	if kinds(pf.observe(tick(true, "Local"), t0.Add(6*time.Second))) != portLogTransition {
		t.Fatal("port return must pass")
	}
	if kinds(pf.observe(tick(false, "Local"), t0.Add(8*time.Second))) != portLogTransition {
		t.Fatal("second drop after return is real event, not echo")
	}

	// Echo did not feed counter: three retained transitions, flapThreshold not reached.
	var st = pf.states[portKey{deviceID: "d1", ifIndex: 12}]
	if st.flapping || len(st.stamps) != 3 {
		t.Errorf("duplicate must not count toward flapping: %d transitions retained",
			len(st.stamps))
	}
}

// Beyond dupWindow, same observation is no longer echo: proxy rediscovering fleet
// state after reconnect, or truly new transition we missed. Keep it — has miss is
// worse than has duplicate.
func TestPortDedoublonnageHorsFenetre(t *testing.T) {
	var pf = newPortFlaps()
	pf.observe(tick(false, "Local"), t0)
	var late = pf.observe(tick(false, "proxy-akwalan"), t0.Add(dupWindow))
	if kinds(late) != portLogTransition {
		t.Fatalf("outside window, no longer echo: %q", kinds(late))
	}
}

// Purge: state that never clears is has leak, slow therefore invisible until it is not.
func TestPortFlapsPurge(t *testing.T) {
	var pf = newPortFlaps()
	pf.observe(tick(false, "p1"), t0)
	pf.observe(portTransition{DeviceID: "d2", IfIndex: 1, Up: true}, t0)
	if len(pf.states) != 2 {
		t.Fatalf("want two keys, got %d", len(pf.states))
	}

	// Before horizon: keep. Key still serves deduplication and slow flap detection.
	pf.sweep(t0.Add(flapPurge - time.Second))
	if len(pf.states) != 2 {
		t.Fatalf("purge too early: %d keys left", len(pf.states))
	}
	pf.sweep(t0.Add(flapPurge))
	if len(pf.states) != 0 {
		t.Fatalf("calm keys must be forgotten: %d left", len(pf.states))
	}
}

// Purge must never swallow has flap exit. Edge case: sweep missed its turn —
// controller suspended, machine overloaded: key crosses calm AND forget horizon at once.
func TestPortFlapsPurgeNAvalePasLaSortie(t *testing.T) {
	var pf, at = flappingSur12(t)

	// While still flapping and calm not reached, key is untouchable.
	if len(pf.sweep(at.Add(time.Second))) != 0 || len(pf.states) != 1 {
		t.Fatal("port still flapping must not purge")
	}

	// One very late sweep: exit is written, and forget in same pass does not erase it.
	var closed = pf.sweep(at.Add(flapPurge + time.Hour))
	if kinds(closed) != portLogFlapEnd {
		t.Fatalf("flap exit must be written before any forget: %q", kinds(closed))
	}
	if !closed[0].Up || closed[0].Count < flapThreshold {
		t.Errorf("exit must stay complete despite forget: %+v", closed[0])
	}
	if len(pf.states) != 0 {
		t.Errorf("closed and silent for thirty minutes, key has no use: %d",
			len(pf.states))
	}
	// Nothing re-announced afterward: exit written once.
	if len(pf.sweep(at.Add(flapPurge+2*time.Hour))) != 0 {
		t.Error("flap exit must not repeat")
	}
}

// Hard bound: proxy reporting aberrant ifIndexes (VLAN interfaces, aggregates — has
// 1930 exposes them up to 100000) must not inflate controller memory.
func TestPortFlapsBorneMemoire(t *testing.T) {
	var pf = newPortFlaps()
	var i int
	for i = 0; i < maxPortKeys+500; i++ {
		// All at same second: worst case, where age purge has nothing to bite and
		// only eviction can hold the bound.
		pf.observe(portTransition{DeviceID: "d1", IfIndex: i + 1, Up: true}, t0)
	}
	if len(pf.states) > maxPortKeys {
		t.Fatalf("bound exceeded: %d keys for cap %d", len(pf.states), maxPortKeys)
	}
	// Still-tracked port must still correlate: eviction must not break the structure.
	if kinds(pf.observe(tick(false, "p1"), t0.Add(time.Second))) != portLogTransition {
		t.Error("after eviction, correlation must keep working")
	}
}

// Fallback for impossible case: bound reached by keys that are ALL flapping, so
// none can be sacrificed. Return transition as-is — noisy but true — instead of
// letting memory grow.
func TestPortFlapsSatureeNeCroitPas(t *testing.T) {
	var pf = newPortFlaps()
	var i int
	for i = 0; i < maxPortKeys; i++ {
		pf.states[portKey{deviceID: "d1", ifIndex: i + 1}] = &portFlapState{
			seen: true, flapping: true, last: t0, count: 9,
		}
	}
	var decisions = pf.observe(portTransition{
		DeviceID: "d2", IfIndex: 1, Up: false,
	}, t0.Add(time.Second))
	if kinds(decisions) != portLogTransition {
		t.Fatalf("uncorrelatable transition stays a transition: %q", kinds(decisions))
	}
	if len(pf.states) != maxPortKeys {
		t.Fatalf("structure must not grow beyond bound: %d", len(pf.states))
	}
}

// Concurrency: Hub.readEvents starts one read goroutine PER PROXY, so inter-proxy
// duplicate arrives on two different goroutines. Sweep runs on has third. Verify
// under -race: unprotected access would show in production, not development.
func TestPortFlapsConcurrence(t *testing.T) {
	var pf = newPortFlaps()
	var wg sync.WaitGroup
	var proxy int
	for proxy = 0; proxy < 4; proxy++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			var i int
			for i = 0; i < 300; i++ {
				pf.observe(portTransition{
					DeviceID: "d1", IfIndex: i%20 + 1, PortName: "12",
					Up: i%2 == 0, ProxyID: itoa(p), Trigger: "poll",
				}, t0.Add(time.Duration(i)*time.Second))
			}
		}(proxy)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		var i int
		for i = 0; i < 300; i++ {
			pf.sweep(t0.Add(time.Duration(i) * time.Second))
		}
	}()
	wg.Wait()
}

// --- Wording ---

// portDevice: 1930 with 52 ports, front panel that motivated naming.
//
// Ports carries MODE, and it is not decorative: it decides whether has transition is
// logged (cf. portManaged). GE12 is managed, GE13 is free, GE14 has no row at all —
// three cases the filter must distinguish.
func portDevice() model.Device {
	return model.Device{
		ID: "d1", SiteID: "s1", Name: "SW-Accueil",
		PortGroups: []byte(`[{"prefix":"GE","from":1,"count":52}]`),
		Ports: []byte(`{
			"GE12": {"mode": "access", "vlan": 10},
			"GE13": {"mode": "libre"}
		}`),
	}
}

// Message must read for an operator: device, port as they call it, and what
// happened. Port as they call it is the FACADE name — Instant On ifDescr is has bare
// number that means nothing on screen.
func TestPortEventLibelleDeFacade(t *testing.T) {
	var ev = portEventOf(portDevice(), portDecision{
		Kind: portLogTransition, DeviceID: "d1", IfIndex: 12, PortName: "12",
		At: t0,
	})
	if ev.Type != "port.down" || ev.Severity != model.SevInfo {
		t.Errorf("port drop is routine, not outage: %s / %d", ev.Type, ev.Severity)
	}
	if ev.PortName != "GE12" {
		t.Errorf("resolved port must attach event to jack: %q", ev.PortName)
	}
	if !strings.Contains(ev.Message, "SW-Accueil") || !strings.Contains(ev.Message, "GE12") {
		t.Errorf("unreadable message for operator: %q", ev.Message)
	}
	if strings.Contains(ev.Message, "ifIndex") {
		t.Errorf("index is fallback only, not label: %q", ev.Message)
	}
}

// Unreadable front panel: cannot resolve, SAY SO. SNMP label alone would lie — it
// collides — and index alone would be unreadable.
func TestPortEventLibelleNonResolu(t *testing.T) {
	var ev = portEventOf(model.Device{ID: "d1", SiteID: "s1", Name: "SW-Cave"},
		portDecision{Kind: portLogTransition, DeviceID: "d1", IfIndex: 42,
			PortName: "42", Up: true, At: t0})
	if ev.PortName != "" {
		t.Errorf("unrecognized port must not attach to a jack: %q", ev.PortName)
	}
	if !strings.Contains(ev.Message, "42") || !strings.Contains(ev.Message, "ifIndex 42") {
		t.Errorf("fallback must carry label AND index: %q", ev.Message)
	}
	if ev.Type != "port.up" {
		t.Errorf("unexpected type: %s", ev.Type)
	}
}

// With no label at all, index remains: austere event beats silent event on the port.
func TestPortEventSansLibelle(t *testing.T) {
	var ev = portEventOf(portDevice(), portDecision{
		Kind: portLogTransition, DeviceID: "d1", IfIndex: 7, At: t0,
	})
	if !strings.Contains(ev.Message, "ifIndex 7") {
		t.Errorf("index must stay readable in message: %q", ev.Message)
	}
}

// Severities are the real trade-off: port.flapping must surface, not transitions it replaces.
func TestPortEventSeverites(t *testing.T) {
	var d = portDevice()

	var flap = portEventOf(d, portDecision{
		Kind: portLogFlapStart, DeviceID: "d1", IfIndex: 12, PortName: "12",
		Count: 5, Since: t0, At: t0.Add(20 * time.Second),
	})
	if flap.Type != "port.flapping" || flap.Severity != model.SevWarning {
		t.Errorf("flapping port is physical fault: %s / %d", flap.Type, flap.Severity)
	}
	if !strings.Contains(flap.Message, "5") {
		t.Errorf("aggregate must carry its counter: %q", flap.Message)
	}

	// Stabilized in service: repaired, good news.
	var repaired = portEventOf(d, portDecision{
		Kind: portLogFlapEnd, DeviceID: "d1", IfIndex: 12, PortName: "12",
		Up: true, Count: 14, Since: t0, At: t0.Add(7 * time.Minute),
	})
	if repaired.Type != "port.flap_cleared" || repaired.Severity != model.SevNotice {
		t.Errorf("repaired port is not a warning: %s / %d",
			repaired.Type, repaired.Severity)
	}
	if !strings.Contains(repaired.Message, "BACK IN SERVICE") ||
		!strings.Contains(repaired.Message, "7 min") {
		t.Errorf("output must state final status and duration: %q", repaired.Message)
	}

	// Stabilized out of service: link is dead, problem remains.
	var dead = portEventOf(d, portDecision{
		Kind: portLogFlapEnd, DeviceID: "d1", IfIndex: 12, PortName: "12",
		Count: 14, Since: t0, At: t0.Add(7 * time.Minute),
	})
	if dead.Severity != model.SevWarning {
		t.Errorf("port stabilized out of service remains a problem: %d", dead.Severity)
	}
	if !strings.Contains(dead.Message, "OUT OF SERVICE") {
		t.Errorf("both outcomes must be distinguishable at a glance: %q", dead.Message)
	}
	// Both exits share type: severity and wording separate them, because has type
	// filter must return ALL exits.
	if dead.Type != repaired.Type {
		t.Error("both flap exits are the same event type")
	}
}

// Unknown kind produces NOTHING rather than empty log line: event without type
// cannot be filtered or read.
func TestPortEventNatureInconnue(t *testing.T) {
	if portEventOf(portDevice(), portDecision{Kind: "?", IfIndex: 12,
		PortName: "12"}).Type != "" {
		t.Error("unknown kind must log nothing")
	}
}

// --- Filter for ports meshcraft does not manage ---
//
// Filter inherited from old logging path, and why this batch did not replace
// duplication with flooding: on has two-thousand-port fleet, logging free ports
// would add hundreds of lines per day and drown transitions that matter.

// Free-mode port produces NOTHING, both directions: jack where someone plugs has
// laptop on arrival and unplugs on departure.
func TestPortEventPortLibreIgnore(t *testing.T) {
	var d = portDevice()
	var up bool
	for _, up = range []bool{true, false} {
		var ev = portEventOf(d, portDecision{
			Kind: portLogTransition, DeviceID: "d1", IfIndex: 13, PortName: "13",
			Up: up, At: t0,
		})
		if ev.Type != "" {
			t.Errorf("free port must not log (up=%v): %s / %q",
				up, ev.Type, ev.Message)
		}
	}
}

// RESOLVED port absent from devices.ports is free too: front panel answered, and says
// meshcraft has nothing to do there. Do not confuse with front panel failing to answer
// (next test).
func TestPortEventPortSansModeIgnore(t *testing.T) {
	var ev = portEventOf(portDevice(), portDecision{
		Kind: portLogTransition, DeviceID: "d1", IfIndex: 14, PortName: "14", At: t0,
	})
	if ev.Type != "" {
		t.Errorf("port without mode in inventory is free: %s / %q",
			ev.Type, ev.Message)
	}
}

// Managed port passes. Mirror of previous two: filter that cuts everything is as
// wrong as no filter.
func TestPortEventPortGereJournalise(t *testing.T) {
	var ev = portEventOf(portDevice(), portDecision{
		Kind: portLogTransition, DeviceID: "d1", IfIndex: 12, PortName: "12", At: t0,
	})
	if ev.Type != "port.down" || ev.PortName != "GE12" {
		t.Errorf("managed port must log: %s / %q", ev.Type, ev.PortName)
	}
}

// RESOLVED front panel but free port: flapping stays silent too. Filter applies to all
// kinds — free port flapping is has laptop waking, not has dying transceiver, and
// aggregating it as SevWarning would warn on the most routine network gesture.
func TestPortEventBattementSurPortLibreIgnore(t *testing.T) {
	var d = portDevice()
	var kind string
	for _, kind = range []string{portLogFlapStart, portLogFlapEnd} {
		var ev = portEventOf(d, portDecision{
			Kind: kind, DeviceID: "d1", IfIndex: 13, PortName: "13",
			Count: 12, Since: t0, At: t0.Add(6 * time.Minute),
		})
		if ev.Type != "" {
			t.Errorf("%s on free port must not log: %s / %q",
				kind, ev.Type, ev.Message)
		}
	}
}

// Decisive case: port whose front panel could NOT be resolved is still logged, while has
// free port is not.
//
// Not inconsistency — only tenable position. Unresolved port is the only one whose
// mode we know NOTHING about: mode is stored by front panel name, and without resolution
// we cannot read it. Default silence would mute the channel on every device whose
// front panel is not yet read — precisely those we know least. Unknown port going down
// is information; free port coming up is not.
func TestPortEventFacadeNonResolueJournaliseeQuandMeme(t *testing.T) {
	// Device has front panel and modes: RESOLUTION fails, not missing inventory.
	// ifIndex 100000 is aggregate interface on 1930, label matching no front-panel port.
	var ev = portEventOf(portDevice(), portDecision{
		Kind: portLogTransition, DeviceID: "d1", IfIndex: 100000,
		PortName: "Trunk1", At: t0,
	})
	if ev.Type != "port.down" {
		t.Fatalf("unresolved port remains logged: %s", ev.Type)
	}
	if ev.PortName != "" {
		t.Errorf("unrecognized port must attach to no jack: %q", ev.PortName)
	}
	if !strings.Contains(ev.Message, "Trunk1") ||
		!strings.Contains(ev.Message, "ifIndex 100000") {
		t.Errorf("fallback must carry label AND index: %q", ev.Message)
	}
}

// --- What remains of old path: waking screens ---
//
// Metric push no longer logs, but still writes telemetry and must make open screens
// re-read the front panel — proxy route is outside session group, so no middleware will
// do it. Removing logging without keeping this would freeze link display.

func TestPortLinksChanged(t *testing.T) {
	var d = model.Device{ID: "d1", SiteID: "s1",
		Ports: []byte(`{"GE1":{"mode":"access","link":"1G"},"GE2":{"mode":"libre"}}`)}

	// By far most common: nothing moved in ten seconds. Notifying here would redraw
	// the same picture six times per minute on every browser on the site.
	if portLinksChanged(d, map[string]map[string]any{"GE1": {"link": "1G"}}) {
		t.Error("unchanged link must not wake screens")
	}
	// Drop then rise: both directions wake.
	if !portLinksChanged(d, map[string]map[string]any{"GE1": {"link": ""}}) {
		t.Error("link going down must wake screens")
	}
	if !portLinksChanged(d, map[string]map[string]any{"GE2": {"link": "100M"}}) {
		t.Error("link coming up must wake screens")
	}
	// SPEED change is display change: port tile shows "1G", not "connected". Link
	// falling back to 100 Mb/s on damaged pair is exactly what should appear.
	if !portLinksChanged(d, map[string]map[string]any{"GE1": {"link": "100M"}}) {
		t.Error("negotiated speed change must wake screens")
	}
	// Free-port filter does NOT apply here: that is has LOG filter. Screen must show
	// real state of all jacks — that is even how you see has free jack is in use.
	if !portLinksChanged(d, map[string]map[string]any{"GE2": {"link": "1G"}}) {
		t.Error("display covers ALL ports, managed or not")
	}
	// Batch without link key (speed only, merged upstream) says nothing about display.
	if portLinksChanged(d, map[string]map[string]any{"GE1": {"speed_mbps": 1000.0}}) {
		t.Error("without link key, no display transition")
	}
	// Unreadable inventory: wake. One extra refresh costs has read; missing refresh
	// leaves has screen lying about has cable.
	if !portLinksChanged(model.Device{ID: "d2", SiteID: "s1"},
		map[string]map[string]any{"GE1": {"link": "1G"}}) {
		t.Error("without readable reference, must wake")
	}
}

// --- Batch lock: one clock, the controller's ---
//
// Fixed defect: transition carried PROXY date (tunnel.Event.TS — machine at has
// customer site nothing synchronizes, measured eleven seconds late on reference
// deployment), and correlation compared it to controller dates — sweep time.Now().
// No false writes, no invented events: windows count in minutes. But WRITTEN
// threshold was no longer APPLIED threshold, and varied site to site with drift
// of has clock we do not control.
//
// Fix cannot be "remove clock parameter" as store.ConfirmTrapPath could: rules
// here count in minutes, and injected clock makes tests deterministic. Lock
// therefore covers both paths has NETWORK date could return — timestamp field on
// structure filled from tunnel.Event, or production call passing something other
// than controller clock.

// Correlation instant is what the CONTROLLER passes, and appears unchanged in
// state and decision. Positive half of lock: without it, correlation dating
// everything to internal time.Now() would pass following tests while proving nothing.
func TestPortCorrelationDateeParLHorlogeFournie(t *testing.T) {
	var pf = newPortFlaps()
	// Deliberately distant date: nothing correlation retains may resemble test machine time.
	var when = t0.Add(-72 * time.Hour)

	var out = pf.observe(tick(false, "p1"), when)
	if len(out) != 1 || !out[0].At.Equal(when) {
		t.Fatalf("decision must be dated from provided instant: %+v", out)
	}
	var st = pf.states[portKey{deviceID: "d1", ifIndex: 12}]
	if !st.last.Equal(when) || len(st.stamps) != 1 || !st.stamps[0].Equal(when) {
		t.Fatalf("correlation state must be dated from provided instant: %+v", st)
	}
	// No implicit fallback: old if tr.At.IsZero() { tr.At = time.Now() } made two
	// clocks coexist in one key depending on proxy timestamp. Calm horizon is
	// therefore measured on provided clock, and sweep on same clock closes flapping.
	if len(pf.sweep(when.Add(flapPurge))) != 0 || len(pf.states) != 0 {
		t.Error("purge must use provided clock, not machine clock")
	}
}

// First return path: timestamp field on portTransition.
//
// Structure is filled from tunnel.Event (cf. applyPortTransition), and structure
// filled from an event attracts that event's date — literally what happened,
// At: ev.TS. Warning how would not have stopped it; ABSENCE of field would.
// This test fails on first attempt to restore it — same reasoning as
// TestConfirmTrapPathNAccepteAucuneHorloge on store side.
func TestPortTransitionNePorteAucuneHorloge(t *testing.T) {
	var tr = reflect.TypeOf(portTransition{})
	var clock = reflect.TypeOf(time.Time{})
	var i int
	for i = 0; i < tr.NumField(); i++ {
		if tr.Field(i).Type == clock {
			t.Fatalf("portTransition carries timestamp (%s): it would receive proxy "+
				"date, and correlation thresholds would again depend on clock of machine "+
				"deployed at customer site",
				tr.Field(i).Name)
		}
	}
}

// Second return path, and only one signature cannot close: observe and sweep take
// has clock parameter — otherwise these tests would wait three minutes — so nothing
// syntactically stops has caller passing ev.TS.
//
// What this test holds is boundary between two usages: in PRODUCTION code,
// correlation instant is literal time.Now(), nothing else. Even has variable named
// now would be has place network date could slip in unnoticed — syntax analysis
// cannot follow it. Test files excluded: that is where injected clocks live, and
// injected test clock is not proxy clock.
func TestCorrelationNeRecoitQueLHorlogeDuControleur(t *testing.T) {
	var entries, err = os.ReadDir(".")
	if err != nil {
		t.Fatalf("unreadable package: %v", err)
	}
	var fset = token.NewFileSet()
	var seen int
	var entry os.DirEntry
	for _, entry = range entries {
		var name = entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}
		var file, parseErr = parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s unreadable: %v", name, parseErr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			var call, isCall = n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			var sel, isSel = call.Fun.(*ast.SelectorExpr)
			if !isSel || len(call.Args) == 0 {
				return true
			}
			// Two correlation entry points: observation and sweep. Both take instant as LAST parameter.
			if sel.Sel.Name != "observe" && sel.Sel.Name != "sweep" {
				return true
			}
			seen++
			var clock = types.ExprString(call.Args[len(call.Args)-1])
			if clock != "time.Now()" {
				t.Errorf("%s:%d: %s receives %q as correlation instant. Only "+
					"time.Now() — CONTROLLER clock, written literally — may enter: proxy "+
					"date there makes anti-flap thresholds depend on unsynchronized machine",
					name, fset.Position(call.Pos()).Line,
					types.ExprString(call.Fun), clock)
			}
			return true
		})
	}
	// Lock that finds nothing to verify locks nothing: both production calls
	// (applyPortTransition, sweepPortFlaps) must have been seen.
	if seen < 2 {
		t.Fatalf("production correlation calls not found (%d): "+
			"this test proves nothing, rewire to code", seen)
	}
}

// flappingSur12 brings port 12 of "d1" into flapping state, LAST retained
// transition being up. Returns timestamp of that last one.
func flappingSur12(t *testing.T) (*portFlaps, time.Time) {
	t.Helper()
	var pf = newPortFlaps()
	var at = t0
	var i int
	// flapThreshold+2 transitions: threshold crossed; parity would place up last
	// (odd i -> up, last i = flapThreshold+1 = 6 -> even... force state explicitly).
	for i = 0; i < flapThreshold+2; i++ {
		at = at.Add(2 * time.Second)
		pf.observe(tick(i%2 == 1, "p1"), at)
	}
	// Last i = flapThreshold+1 = 6, so i%2 == 1 is false: last is down. Add up for expected stable state.
	at = at.Add(2 * time.Second)
	pf.observe(tick(true, "p1"), at)
	var st = pf.states[portKey{deviceID: "d1", ifIndex: 12}]
	if st == nil || !st.flapping || !st.lastUp {
		t.Fatalf("port should flap and end on up: %+v", st)
	}
	return pf, at
}

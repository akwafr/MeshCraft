package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/tunnel"
)

// A proxy's METRICS are state, and must obey the same scope as everything else.
//
// What these tests lock down: a proxy cannot chart an device it was not entrusted
// with. This was the last of the three write paths to be guarded, and the most
// discreet — the other two leave a line in the fleet journal or an off link in
// inventory, whereas a series injected into VictoriaMetrics never presents as an
// anomaly: it IS the graph the operator looks at. Invented traffic, a fantasy
// temperature, an error counter that explodes, and someone is sent looking for an
// outage that does not exist.
//
// The symmetric case matters as much, because this code runs in production: a
// legitimate batch must pass WHOLE. An over-eager filter would stop curves for a
// fleet in service — exactly the symptom the fix seeks to stop an attacker from
// causing.
//
// Without database, metric store, or tunnel: `scopedSamples` consults only scope
// memory, filled by `recordProxyScope` — the real production call site, exercised
// as-is here.

func serverWithScope(t *testing.T, proxyID string, cfg tunnel.WatchConfig) *Server {
	t.Helper()
	var s = &Server{proxyScopes: newProxyScopes()}
	s.recordProxyScope(proxyID, cfg)
	return s
}

func pmSample(deviceID, port string, v float64) metricbuf.Sample {
	return metricbuf.Sample{
		Name:   "meshcraft_if_in_octets_total",
		Labels: map[string]string{"device": deviceID, "ifindex": "1", "port": port},
		Value:  v,
	}
}

func pmDevices(samples []metricbuf.Sample) []string {
	var out = make([]string, 0, len(samples))
	var i int
	for i = range samples {
		out = append(out, samples[i].Labels["device"])
	}
	return out
}

func TestMetricsEntrustedBatchPassesIntact(t *testing.T) {
	var s = serverWithScope(t, "px-a", twoSites())
	var lot = []metricbuf.Sample{
		pmSample("sw-a1", "GE1", 12),
		pmSample("sw-a1", "GE2", 34),
		pmSample("sw-a1", "GE3", 56),
	}
	var kept, drop = s.scopedSamples("px-a", lot)

	if drop.total() != 0 {
		t.Fatalf("a fully entrusted batch loses nothing: %d dropped (%d out of scope, "+
			"%d unlabelled)", drop.total(), drop.refused, drop.unlabelled)
	}
	if len(kept) != len(lot) {
		t.Fatalf("%d samples of %d: curves for a fleet in service stop",
			len(kept), len(lot))
	}
	if &kept[0] != &lot[0] {
		t.Error("batch was copied though nothing was dropped: useless allocation on hot path")
	}
}

func TestMetricsMixedBatchKeepsAllowedDropsRest(t *testing.T) {
	var s = serverWithScope(t, "px-a", twoSites())
	var lot = []metricbuf.Sample{
		pmSample("sw-a1", "GE1", 1),
		pmSample("sw-b1", "GE1", 2),
		pmSample("sw-a1", "GE2", 3),
		pmSample("sw-a2", "GE1", 4),
		pmSample("sw-a1", "GE3", 5),
	}
	var kept, drop = s.scopedSamples("px-a", lot)

	if strings.Join(pmDevices(kept), ",") != "sw-a1,sw-a1,sw-a1" {
		t.Fatalf("only collected device samples must pass, in order: %v", pmDevices(kept))
	}
	if kept[0].Value != 1 || kept[1].Value != 3 || kept[2].Value != 5 {
		t.Errorf("kept samples are wrong: %v, %v, %v",
			kept[0].Value, kept[1].Value, kept[2].Value)
	}
	if drop.refused != 2 || drop.unlabelled != 0 {
		t.Errorf("drops = %d out of scope and %d unlabelled, expected 2 and 0",
			drop.refused, drop.unlabelled)
	}
	if len(lot) != 5 || lot[1].Labels["device"] != "sw-b1" {
		t.Errorf("input batch modified in place: %v", pmDevices(lot))
	}
}

func TestMetricsUnlabelledSampleDropped(t *testing.T) {
	var s = serverWithScope(t, "px-a", twoSites())
	var lot = []metricbuf.Sample{
		pmSample("sw-a1", "GE1", 1),
		{Name: "meshcraft_if_in_octets_total",
			Labels: map[string]string{"device": "", "port": "GE9"}, Value: 999},
		{Name: "meshcraft_if_in_octets_total", Value: 999},
		pmSample("sw-a1", "GE2", 2),
	}
	var kept, drop = s.scopedSamples("px-a", lot)

	if len(kept) != 2 {
		t.Fatalf("%d samples kept, expected the 2 labelled: %v",
			len(kept), pmDevices(kept))
	}
	if drop.unlabelled != 2 {
		t.Errorf("unlabelled counted = %d, expected 2", drop.unlabelled)
	}
	if drop.refused != 0 {
		t.Errorf("sample without device is not a scope refusal: %d", drop.refused)
	}
	s.proxyScopes.mu.RLock()
	var tally = s.proxyScopes.refusals["px-a"]
	s.proxyScopes.mu.RUnlock()
	if tally != nil {
		t.Errorf("unlabelled sample counted in scope refusal journal: %d refusals",
			tally.total)
	}
}

func TestMetricsProxyWithoutInstructionWritesNothing(t *testing.T) {
	var s = &Server{proxyScopes: newProxyScopes()}
	var lot = []metricbuf.Sample{pmSample("sw-a1", "GE1", 1), pmSample("sw-a1", "GE2", 2)}
	var kept, drop = s.scopedSamples("px-jamais-vu", lot)
	if len(kept) != 0 || drop.refused != 2 {
		t.Fatalf("proxy with no pushed instruction writes nothing: %d kept, %d refused",
			len(kept), drop.refused)
	}
}

func TestMetricsMissingScopeRefusesAll(t *testing.T) {
	var s = &Server{}
	var kept, drop = s.scopedSamples("px-a", []metricbuf.Sample{pmSample("sw-a1", "GE1", 1)})
	if len(kept) != 0 || drop.refused != 1 {
		t.Fatalf("without scope memory, nothing writes: %d kept, %d refused",
			len(kept), drop.refused)
	}
}

func TestMetricsOneDecisionPerDevice(t *testing.T) {
	var s = serverWithScope(t, "px-a", twoSites())
	var lot = make([]metricbuf.Sample, 0, 3000)
	var i int
	for i = 0; i < 3000; i++ {
		var id = "sw-b1"
		if i%2 == 1 {
			id = "sw-a2"
		}
		lot = append(lot, pmSample(id, "GE1", float64(i)))
	}
	var kept, drop = s.scopedSamples("px-a", lot)
	if len(kept) != 0 || drop.refused != 3000 {
		t.Fatalf("%d kept and %d refused: sample drop count must be exact for response",
			len(kept), drop.refused)
	}

	s.proxyScopes.mu.RLock()
	var tally = *s.proxyScopes.refusals["px-a"]
	s.proxyScopes.mu.RUnlock()
	if tally.total != 2 {
		t.Errorf("refusals in journal = %d, expected 2 (one per refused device): "+
			"decision is not memoized and journal counts measurements", tally.total)
	}
}

func TestMetricsFilteredBeforeStorage(t *testing.T) {
	var fn, fset = packageFunc(t, "handleProxyMetrics")
	var posFilter, posWrite = callsInOrder(fn, "scopedSamples", "Write")
	if posFilter == token.NoPos {
		t.Fatal("handleProxyMetrics no longer calls scopedSamples: proxy samples reach " +
			"storage without scope check")
	}
	if posWrite == token.NoPos {
		t.Fatal("handleProxyMetrics no longer writes to storage")
	}
	if posFilter > posWrite {
		t.Errorf("batch written (line %d) BEFORE filtered (line %d): series stays in VictoriaMetrics",
			fset.Position(posWrite).Line, fset.Position(posFilter).Line)
	}
}

func TestCoverageChangePushesWatch(t *testing.T) {
	var expected = map[string]string{
		"handleSetProxySiteMember": "SetProxySiteMember",
		"handleSetProxyCoverage":   "SetProxyCoverage",
	}
	var name string
	for name = range expected {
		var fn, fset = packageFunc(t, name)
		var posWrite, posPush = callsInOrder(fn, expected[name], "pushCoverageChange")
		if posWrite == token.NoPos {
			t.Errorf("%s no longer touches %s", name, expected[name])
			continue
		}
		if posPush == token.NoPos {
			t.Errorf("%s does not push watch: proxy keeps old coverage until reconnect", name)
			continue
		}
		if posWrite > posPush {
			t.Errorf("%s pushes watch (line %d) BEFORE writing coverage (line %d): old instruction would go out",
				name, fset.Position(posPush).Line, fset.Position(posWrite).Line)
		}
	}
	var fn, _ = packageFunc(t, "pushCoverageChange")
	var posPush, _ = callsInOrder(fn, "PushWatchTo", "")
	if posPush == token.NoPos {
		t.Error("pushCoverageChange does not call PushWatchTo")
	}
}

func packageFunc(t *testing.T, name string) (*ast.FuncDecl, *token.FileSet) {
	t.Helper()
	var entries, err = os.ReadDir(".")
	if err != nil {
		t.Fatalf("package unreadable: %v", err)
	}
	var fset = token.NewFileSet()
	var entry os.DirEntry
	for _, entry = range entries {
		var file = entry.Name()
		if entry.IsDir() || !strings.HasSuffix(file, ".go") ||
			strings.HasSuffix(file, "_test.go") {
			continue
		}
		var parsed, parseErr = parser.ParseFile(fset, file, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s unreadable: %v", file, parseErr)
		}
		var decl ast.Decl
		for _, decl = range parsed.Decls {
			var fn, isFn = decl.(*ast.FuncDecl)
			if isFn && fn.Body != nil && fn.Name.Name == name {
				return fn, fset
			}
		}
	}
	t.Fatalf("function %s not found in package", name)
	return nil, nil
}

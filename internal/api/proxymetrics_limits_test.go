package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"meshcraft/internal/metricbuf"
	"meshcraft/internal/snmppoll"
)

// What these tests protect: ALIGNMENT between what /api/proxy/metrics claims to
// accept and what it actually accepts.
//
// It failed once: the route advertised 20,000 samples per batch but read its body
// with a 1 MiB cap, i.e. ~5000 samples. The visible guard was unreachable, and
// truncation responded instead with 400 « invalid JSON » — which the proxy treated
// as a transient failure, replaying the same batch forever. A guard that announces a
// limit it does not enforce is worse than no guard.
//
// Hence three properties, not just a rejection check:
//
//  1. a batch AT the advertised size PASSES (otherwise the claim is still a lie);
//  2. beyond that, refusal comes from the GUARD, with its message, never from truncation;
//  3. an actually oversized body SPEAKS UP, and names the expected action.
//
// The rest of the API keeps its 1 MiB cap: loosening a telemetry route is not
// loosening forms (TestGenericDecodeStaysAt1MiB).

// IF-MIB OIDs, copied because they are unexported in `snmppoll`. These are IANA
// protocol constants, not an implementation detail: they will not move. Copying them
// is the price of measuring sample weight from the REAL translator rather than a
// mock — and that is the point; a mock would not grow when `snmppoll` adds a label.
const (
	pmOidIfDescr       = "1.3.6.1.2.1.2.2.1.2"
	pmOidIfType        = "1.3.6.1.2.1.2.2.1.3"
	pmOidIfOperStatus  = "1.3.6.1.2.1.2.2.1.8"
	pmOidIfHighSpeed   = "1.3.6.1.2.1.31.1.1.1.15"
	pmOidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	pmOidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"
	pmOidIfInErrors    = "1.3.6.1.2.1.2.2.1.14"
	pmOidIfOutErrors   = "1.3.6.1.2.1.2.2.1.20"
)

// pmDeviceID and pmProxyID are UUIDs, as in production: device ID length is paid on
// EVERY sample, so the real shape must be measured, not a test « dev-1 ».
const (
	pmDeviceID = "6f1c9a2e-4b7d-4c3a-9f88-2d0e5b71a3c4"
	pmProxyID  = "b2f4d1c8-9e3a-4f52-8c11-7a6d0b4e2f39"
	pmSecret   = "0123456789abcdef0123456789abcdef"
)

// pmInstant: a timestamp whose RFC 3339 rendering is MAXIMUM length (non-zero
// nanoseconds, no trailing zeros stripped). A round instant would measure a lighter
// sample than production ones.
func pmInstant() time.Time {
	return time.Date(2026, 7, 28, 10, 17, 42, 123456789, time.UTC)
}

// pmShortPort: factory naming of an Instant On 1930.
func pmShortPort(i int) string { return "GE" + strconv.Itoa(i) }

// pmLongPort: an operator-RENAMED port. This is the realistic worst case, and exactly
// the variable that hit the wall: the controller does not bound this label length;
// the operator chooses it.
func pmLongPort(i int) string {
	return "Salle serveurs - baie B12 - uplink " + strconv.Itoa(i)
}

func pmPDU(oid, idx string, t gosnmp.Asn1BER, v any) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: oid + "." + idx, Type: t, Value: v}
}

// pmTables builds SNMP tables for a switch with `nbPorts` ports, all up and all
// Ethernet — i.e. the case that produces the MOST samples, therefore the one that
// fills a batch.
func pmTables(name func(int) string, nbPorts int) map[string]map[string]gosnmp.SnmpPDU {
	var tables = map[string]map[string]gosnmp.SnmpPDU{}
	var oids = []string{
		pmOidIfDescr, pmOidIfType, pmOidIfOperStatus, pmOidIfHighSpeed,
		pmOidIfHCInOctets, pmOidIfHCOutOctets, pmOidIfInErrors, pmOidIfOutErrors,
	}
	var o string
	for _, o = range oids {
		tables[o] = map[string]gosnmp.SnmpPDU{}
	}
	var p int
	for p = 1; p <= nbPorts; p++ {
		var idx = strconv.Itoa(p)
		tables[pmOidIfDescr][idx] = pmPDU(pmOidIfDescr, idx,
			gosnmp.OctetString, []byte(name(p)))
		tables[pmOidIfType][idx] = pmPDU(pmOidIfType, idx,
			gosnmp.Integer, snmppoll.IfTypeEthernet)
		tables[pmOidIfOperStatus][idx] = pmPDU(pmOidIfOperStatus, idx,
			gosnmp.Integer, 1)
		tables[pmOidIfHighSpeed][idx] = pmPDU(pmOidIfHighSpeed, idx,
			gosnmp.Gauge32, uint(1000))
		// Counters at the magnitude of a port running for weeks: digit COUNT matters in
		// JSON weight.
		tables[pmOidIfHCInOctets][idx] = pmPDU(pmOidIfHCInOctets, idx,
			gosnmp.Counter64, uint64(4982133770615))
		tables[pmOidIfHCOutOctets][idx] = pmPDU(pmOidIfHCOutOctets, idx,
			gosnmp.Counter64, uint64(3170488291204))
		tables[pmOidIfInErrors][idx] = pmPDU(pmOidIfInErrors, idx,
			gosnmp.Counter32, uint(1734))
		tables[pmOidIfOutErrors][idx] = pmPDU(pmOidIfOutErrors, idx,
			gosnmp.Counter32, uint(902))
	}
	return tables
}

// pmSamples returns samples from real `snmppoll.Samples`, for both OID sets combined:
// six metrics per port.
func pmSamples(name func(int) string, nbPorts int) []metricbuf.Sample {
	return snmppoll.Samples(pmDeviceID, pmTables(name, nbPorts), pmInstant(), 0)
}

// pmCounterSamples keeps only the `if_counters` set — the case that MUST size the cap.
//
// `if_state` and `if_counters` are read separately, at two cadences (30 s and 5 s):
// a batch pushed by the proxy is therefore mostly counters. A counter is the HEAVIEST
// of the six — thirteen-digit value vs one for state, and a longer metric name.
// Measuring a batch mixing both sets underestimates real weight by 7%, which is
// exactly the favorable gap that produced the original defect.
func pmCounterSamples(name func(int) string, nbPorts int) []metricbuf.Sample {
	var tables = pmTables(name, nbPorts)
	delete(tables, pmOidIfOperStatus)
	delete(tables, pmOidIfHighSpeed)
	return snmppoll.Samples(pmDeviceID, tables, pmInstant(), 0)
}

// pmBatch stretches a sample set to length `n`, cycling. Sample shape is what we
// measure; repetition does not change its weight.
func pmBatch(proto []metricbuf.Sample, n int) []metricbuf.Sample {
	var out = make([]metricbuf.Sample, 0, n)
	var i int
	for i = 0; i < n; i++ {
		out = append(out, proto[i%len(proto)])
	}
	return out
}

// pmBody serializes the exact envelope `cmd/proxy` sends: both auth fields count
// toward bytes the decoder bounds.
func pmBody(t *testing.T, samples []metricbuf.Sample) []byte {
	t.Helper()
	var body, err = json.Marshal(map[string]any{
		"proxy_id": pmProxyID, "secret": pmSecret, "samples": samples,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func pmPost(body []byte) *http.Request {
	var r = httptest.NewRequest(http.MethodPost, "/api/proxy/metrics",
		bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

// TestSNMPSampleWeight locks the bytes ↔ samples correspondence that JUSTIFIES
// maxProxyMetricsBody.
//
// This test will prevent the next occurrence of the defect: the day `snmppoll` adds
// a label, or a metric name lengthens, it fails here — not at an operator whose
// upload stopped without a word.
func TestSNMPSampleWeight(t *testing.T) {
	// 52 ports: a 1930 48-port plus its four SFP+.
	var short = pmSamples(pmShortPort, 52)
	var long = pmSamples(pmLongPort, 52)
	if len(short) != 6*52 || len(long) != 6*52 {
		t.Fatalf("expected 6 samples per port, got %d and %d",
			len(short), len(long))
	}
	var cShort = pmCounterSamples(pmShortPort, 52)
	var cLong = pmCounterSamples(pmLongPort, 52)
	if len(cShort) != 4*52 {
		t.Fatalf("expected 4 counters per port, got %d", len(cShort))
	}
	var shortWeight = len(pmBody(t, cShort)) / len(cShort)
	var longWeight = len(pmBody(t, cLong)) / len(cLong)
	t.Logf("sample weight, combined sets: %d B (« %s »), %d B (« %s »)",
		len(pmBody(t, short))/len(short), pmShortPort(12),
		len(pmBody(t, long))/len(long), pmLongPort(12))
	t.Logf("COUNTER weight, sizing case: %d B (« %s »), %d B (« %s »)",
		shortWeight, pmShortPort(12), longWeight, pmLongPort(12))
	t.Logf("a batch of %d samples therefore weighs %d KiB worst case, for a body cap "+
		"of %d KiB (%d %%)",
		maxSamplesPerBatch, maxSamplesPerBatch*longWeight/1024,
		maxProxyMetricsBody/1024, 100*maxSamplesPerBatch*longWeight/maxProxyMetricsBody)

	// Figure documented in proxymetrics_handlers.go. If it no longer holds, fix the
	// COMMENT before the cap — a cap justified by a stale measurement is the defect
	// this set repairs.
	// 196 B measured; 200 allows one extra counter digit of noise, not room for another
	// label.
	if shortWeight > 200 {
		t.Errorf("short port name counter weighs %d B (196 documented): sample shape "+
			"grew; review worstBytesPerSample and its comment", shortWeight)
	}
	if longWeight > worstBytesPerSample {
		t.Fatalf("long realistic port name counter weighs %d B, above retained %d: "+
			"maxProxyMetricsBody = %d B no longer covers advertised %d samples",
			longWeight, worstBytesPerSample, maxProxyMetricsBody, maxSamplesPerBatch)
	}
	// A margin, not a tight squeeze: port name is operator-entered with no controller-side
	// bound.
	if maxSamplesPerBatch*longWeight > maxProxyMetricsBody*3/4 {
		t.Errorf("advertised batch weighs %d B, over 75 %% of cap %d: margin too thin "+
			"for longer labels", maxSamplesPerBatch*longWeight, maxProxyMetricsBody)
	}
}

// TestAdvertisedSizeBatchPasses is the test that gives the batch meaning: what the
// route claims to accept must reach the guard INTACT.
//
// With counters and long port names — the heaviest case, and the one that failed.
// Before the fix, this ~4.8 MiB body was cut at 1 MiB and came back as « invalid JSON ».
func TestAdvertisedSizeBatchPasses(t *testing.T) {
	var body = pmBody(t,
		pmBatch(pmCounterSamples(pmLongPort, 52), maxSamplesPerBatch))
	t.Logf("batch of %d samples = %d B, %d %% of body cap %d B",
		maxSamplesPerBatch, len(body), 100*len(body)/maxProxyMetricsBody,
		maxProxyMetricsBody)

	var rec = httptest.NewRecorder()
	var in proxyMetricsBatch
	if !readProxyMetricsBatch(rec, pmPost(body), &in) {
		t.Fatalf("advertised-size batch was refused: %d — %s",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if len(in.Samples) != maxSamplesPerBatch {
		t.Fatalf("%d samples decoded of %d: truncated body",
			len(in.Samples), maxSamplesPerBatch)
	}
	if in.ProxyID != pmProxyID {
		t.Fatalf("envelope decoded wrong: proxy_id = %q", in.ProxyID)
	}
	// Nothing written: batch accepted, response belongs to downstream handling.
	if rec.Body.Len() != 0 {
		t.Fatalf("response written although batch accepted: %s", rec.Body.String())
	}
}

// TestOversizedBatchRefusedByGuard: beyond the claim, the EXPLICIT guard refuses,
// with a readable message — not truncation.
//
// Batch uses short names on purpose: body (~3.7 MiB) fits well under the byte cap,
// so only counting can refuse it. That is the only way to exercise the guard itself.
//
// Goes through the full handler, with a Server WITHOUT database: if refusal happens
// before authentication, no DB access occurs and nothing panics.
func TestOversizedBatchRefusedByGuard(t *testing.T) {
	var batch = pmBatch(pmSamples(pmShortPort, 52), maxSamplesPerBatch+1)
	var body = pmBody(t, batch)
	if len(body) >= maxProxyMetricsBody {
		t.Fatalf("body %d B: this test proves nothing if truncation can answer instead "+
			"of the guard", len(body))
	}
	var rec = httptest.NewRecorder()
	var s = &Server{}
	s.handleProxyMetrics(rec, pmPost(body))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d — %s", rec.Code,
			strings.TrimSpace(rec.Body.String()))
	}
	var msg = rec.Body.String()
	if !strings.Contains(msg, "batch too large") ||
		!strings.Contains(msg, strconv.Itoa(maxSamplesPerBatch+1)) ||
		!strings.Contains(msg, "proxy buffer knows how") {
		t.Errorf("expected guard message, got: %s", strings.TrimSpace(msg))
	}
	// Refusal must come from COUNT, never body size: the two are fixed differently.
	if strings.Contains(msg, "request body too large") ||
		strings.Contains(msg, "invalid JSON") {
		t.Errorf("refused by truncation, not guard: %s", strings.TrimSpace(msg))
	}
}

// TestOversizedBodySpeaksUp: even with a correct contract, a proxy of another version
// will someday send an out-of-spec body. This case must SPEAK UP.
//
// Before this set it produced 400 « invalid JSON: unexpected EOF » — a message that
// sends you looking for a serialization bug that does not exist, while upload is down.
func TestOversizedBodySpeaksUp(t *testing.T) {
	// Enough to exceed 8 MiB for sure, with real samples.
	var body = pmBody(t, pmBatch(pmSamples(pmLongPort, 52), 40000))
	if len(body) <= maxProxyMetricsBody {
		t.Fatalf("body %d B: does not exceed cap %d, test proves nothing",
			len(body), maxProxyMetricsBody)
	}
	var rec = httptest.NewRecorder()
	var s = &Server{}
	s.handleProxyMetrics(rec, pmPost(body))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 (SIZE is the issue, not shape), got %d — %s",
			rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var msg = strings.TrimSpace(rec.Body.String())
	t.Logf("body %d B refused: %s", len(body), msg)
	if strings.Contains(msg, "invalid JSON") {
		t.Errorf("oversized body still presents as malformed JSON: %s", msg)
	}
	// Cause, cap, and action: all three needed so a proxy log suffices for diagnosis.
	if !strings.Contains(msg, "request body too large") {
		t.Errorf("cause not named: %s", msg)
	}
	if !strings.Contains(msg, strconv.Itoa(maxProxyMetricsBody)) {
		t.Errorf("cap not quantified: %s", msg)
	}
	if !strings.Contains(msg, "maxPerFlush") {
		t.Errorf("expected proxy action not named: %s", msg)
	}
}

// TestGenericDecodeStaysAt1MiB: loosening telemetry does not loosen the rest. A form
// has no reason to accept 8 MiB, and the wide cap is justified by BATCHES — not comfort.
func TestGenericDecodeStaysAt1MiB(t *testing.T) {
	var big = make([]byte, maxBody+4096)
	var i int
	for i = range big {
		big[i] = 'x'
	}
	// Large but syntactically valid body: what is refused is its size.
	var body = append([]byte(`{"nom":"`), append(big, []byte(`"}`)...)...)

	var rec = httptest.NewRecorder()
	var in struct {
		Nom string `json:"nom"`
	}
	if decode(rec, pmPost(body), &in) {
		t.Fatalf("body %d B accepted by decode: %d B cap no longer applies",
			len(body), maxBody)
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d — %s", rec.Code,
			strings.TrimSpace(rec.Body.String()))
	}
	var msg = strings.TrimSpace(rec.Body.String())
	t.Logf("generic decode: %s", msg)
	if !strings.Contains(msg, strconv.Itoa(maxBody)) {
		t.Errorf("generic cap %d B not announced: %s", maxBody, msg)
	}
	// Telemetry route action has nothing to say to a form.
	if strings.Contains(msg, "maxPerFlush") {
		t.Errorf("proxy action on generic route: %s", msg)
	}
}

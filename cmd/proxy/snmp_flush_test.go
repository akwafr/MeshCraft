package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"meshcraft/internal/metricbuf"
)

// What these tests protect: that the buffer MOVES ON.
//
// The buffer exists so a tunnel outage does not lose the minutes it lasts, and
// for that `flush` returns to the buffer any batch that did not go out. But
// `metricbuf.Take` ALWAYS returns the same reserved batch: returning a batch the
// controller will refuse again means replaying it identically forever. The
// buffer no longer drains, it overflows from the top, and the collection loses
// its MOST RECENT points — the complete stop of upstream for this proxy, not a
// partial loss.
//
// Hence three properties, and the first is the one that gives the others meaning:
//
//  1. on a PERMANENT refusal, the batch is dropped and the next batch is DIFFERENT;
//  2. on a TRANSIENT failure, the next batch is the SAME — that is the buffer's
//     reason to exist, and fixing the former must not take it away;
//  3. a drop says how many points it sacrifices. A silent drop would be worse
//     than the loop it replaces: the loop, at least, ends up being seen.
//
// No real network: `flush` takes its HTTP client from the collector, so a test
// server is enough to play the controller — including for its responses that
// are hardest to get for real, which are precisely the ones that used to stall.

// receivedBatch: what identifies a batch as seen by the controller.
//
// Its BOUNDS, not its samples. The whole question these tests ask is "is the
// next batch the same as the previous one?", and two values answer it —
// provided the samples are numbered, which numberedSamples takes care of.
type receivedBatch struct {
	n     int
	first float64
	last  float64
}

// batchRecorder records the batches the fake controller receives.
//
// The lock is not decorative: the HTTP handler runs in the test server's
// goroutine, the reading in the test's. The round trip over the connection
// would probably be enough to order the two, but "probably" is not what we ask
// of a test run with -race.
type batchRecorder struct {
	mu      sync.Mutex
	batches []receivedBatch
}

func (rc *batchRecorder) record(t *testing.T, r *http.Request) {
	var in struct {
		ProxyID string             `json:"proxy_id"`
		Samples []metricbuf.Sample `json:"samples"`
	}
	// t.Errorf and not t.Fatalf: we are in the server's goroutine, where stopping
	// the test is not allowed.
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		t.Errorf("unreadable body on the controller side")
		return
	}
	if in.ProxyID != tProxyID {
		t.Errorf("proxy_id expected %q, received %q", tProxyID, in.ProxyID)
	}
	if len(in.Samples) == 0 {
		t.Errorf("empty batch received: flush must not send a request for nothing")
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.batches = append(rc.batches, receivedBatch{
		n: len(in.Samples), first: in.Samples[0].Value,
		last: in.Samples[len(in.Samples)-1].Value,
	})
}

func (rc *batchRecorder) received() []receivedBatch {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]receivedBatch{}, rc.batches...)
}

// numberedSamples returns `n` samples whose VALUE is their rank. That
// number is what makes "the same batch" and "the next batch" distinguishable;
// without it, a replay and a move-on produce two identical bodies and the test
// proves nothing.
func numberedSamples(n int) []metricbuf.Sample {
	var out = make([]metricbuf.Sample, 0, n)
	var i int
	for i = 0; i < n; i++ {
		out = append(out, metricbuf.Sample{
			Name:   "meshcraft_if_in_octets_total",
			Labels: map[string]string{"device": tDeviceID, "ifindex": strconv.Itoa(i)},
			Value:  float64(i),
		})
	}
	return out
}

// fakeController sets up a fake controller that records then answers
// `status` with `respBody`, and the collector that talks to it.
//
// The buffer is sized EXACTLY to what is put in it: that way any non-zero
// `Dropped` in one of these tests is a real symptom, never a side effect of the
// setup.
func fakeController(t *testing.T, status int, respBody string,
	headers map[string]string, n int) (*SNMPCollector, *batchRecorder) {
	t.Helper()
	var rc = &batchRecorder{}
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/proxy/metrics" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			rc.record(t, r)
			var k, v string
			for k, v = range headers {
				w.Header().Set(k, v)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			var _, err = w.Write([]byte(respBody))
			if err != nil {
				t.Errorf("write de la response : %v", err)
			}
		}))
	t.Cleanup(srv.Close)
	var c = &SNMPCollector{
		buf:           metricbuf.New(n),
		controllerURL: srv.URL,
		proxyID:       tProxyID,
		secret:        tSecret,
		client:        srv.Client(),
	}
	c.buf.AddAll(numberedSamples(n))
	return c, rc
}

// refusal413: the body the controller really sends back on an oversized batch
// (see failJSON and batchRecovery, internal/api). Copied because it is the reason
// the operator will read in the proxy's log: if its shape changes, the drop
// message loses its value.
const refusal413 = `{"error":"request body too large: cap of 8388608 ` +
	`bytes; proxy batch too large, reduce maxPerFlush ` +
	`(this is not a content problem)"}`

// TestPermanentRefusalDropsAndTheBufferMovesOn is THE test of this batch.
//
// It tests the only property that tells the fix from the defect: after a
// permanent refusal, the NEXT batch is not the previous one. Everything else —
// the log, the boundary, the figures — only matters if this one holds.
func TestPermanentRefusalDropsAndTheBufferMovesOn(t *testing.T) {
	// More than one batch in the buffer, otherwise "the next batch is different"
	// means nothing: that is exactly the property being measured.
	var enTampon = maxPerFlush + 500
	var c, rc = fakeController(t, http.StatusRequestEntityTooLarge, refusal413,
		nil, enTampon)
	var ctx = context.Background()

	// False even on a drop, although the buffer did move on: a systematic refusal
	// would sacrifice four batches per wake-up instead of one if the burst went on.
	if c.flush(ctx) {
		t.Errorf("flush must return false on a refusal: the burst stops there")
	}
	if c.flush(ctx) {
		t.Errorf("flush must return false on a refusal: the burst stops there")
	}

	var batches = rc.received()
	if len(batches) != 2 {
		t.Fatalf("2 batches expected at the controller, received %d", len(batches))
	}
	if batches[0].first == batches[1].first {
		t.Fatalf("the buffer DID NOT MOVE ON: both batches start at point %.0f "+
			"— that is the infinite loop this batch fixes", batches[0].first)
	}
	if batches[0].n != maxPerFlush || batches[0].first != 0 {
		t.Errorf("first batch: %d samples from %.0f, expected %d from "+
			"de 0", batches[0].n, batches[0].first, maxPerFlush)
	}
	if batches[1].first != float64(maxPerFlush) || batches[1].n != 500 {
		t.Errorf("second batch: %d samples from %.0f, expected 500 from "+
			"de %d", batches[1].n, batches[1].first, maxPerFlush)
	}

	// The buffer is empty, and above all: it has nothing RESERVED left. A residual
	// `Pending` would be the same stall under another name, since `Take` would
	// return it again.
	var st = c.buf.Stats()
	if st.Queued != 0 || st.Pending != 0 {
		t.Errorf("buffer expected empty, got %d queued and %d reserved",
			st.Queued, st.Pending)
	}
	// `Dropped` counts SATURATION losses, not drops: a dropped batch leaves no
	// trace in the buffer statistics. That is precisely why the drop must log
	// itself (see the next test).
	if st.Dropped != 0 {
		t.Errorf("no saturation loss expected, got %d", st.Dropped)
	}
}

// TestTransientFailureReplaysTheSameBatch is the buffer's non-regression, and it
// matters as much as the previous test: recognising the permanent refusal must
// not turn a controller outage into a loss of measurements. That is what the
// buffer exists to prevent.
func TestTransientFailureReplaysTheSameBatch(t *testing.T) {
	// 503: the response of a controller with no metrics storage configured. It
	// chooses it EXPRESSLY so the proxy keeps its batch (see handleProxyMetrics).
	var c, rc = fakeController(t, http.StatusServiceUnavailable,
		`{"error":"metrics storage not configured on this controller"}`,
		nil, maxPerFlush+500)
	var ctx = context.Background()
	var i int
	for i = 0; i < 3; i++ {
		if c.flush(ctx) {
			t.Errorf("flush must return false on a refusal (attempt %d)", i+1)
		}
	}
	var batches = rc.received()
	if len(batches) != 3 {
		t.Fatalf("3 attempts expected, received %d", len(batches))
	}
	for i = 1; i < len(batches); i++ {
		if batches[i] != batches[0] {
			t.Fatalf("batch %d differs from the first (%+v vs %+v): a transient failure "+
				"must replay the SAME batch, otherwise a controller outage becomes "+
				"a loss of measurements", i+1, batches[i], batches[0])
		}
	}
	// Nothing is lost, and nothing stays RESERVED: `Nack` puts the batch back in the
	// queue, at the HEAD, and releases the reservation. That explains the
	// identical replay better than "Take returns the reserved batch" — the next
	// batch is the same because it is picked up from the same place in the queue,
	// not because a reservation survives.
	var st = c.buf.Stats()
	if st.Queued != maxPerFlush+500 || st.Pending != 0 || st.Dropped != 0 {
		t.Errorf("expected %d queued / 0 reserved / 0 lost, got %d / %d / %d",
			maxPerFlush+500, st.Queued, st.Pending, st.Dropped)
	}
}

// TestADropSaysHowManyPointsItSacrifices: the number of points sacrificed has
// no other place to be said.
//
// `metricbuf` does not count it — a drop is not a saturation — so if this line
// does not carry it, the measurement disappears without a trace, and a silent
// drop is worse than the loop it replaces. The controller's reason is in it
// too: it is what says which of the proxy's settings is at fault.
func TestADropSaysHowManyPointsItSacrifices(t *testing.T) {
	var journal bytes.Buffer
	var precedent = slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&journal,
		&slog.HandlerOptions{Level: slog.LevelWarn})))
	defer slog.SetDefault(precedent)

	// Seven points: a count recognisable at a glance in the line, and an
	// INCOMPLETE batch — the drop does not depend on the batch size.
	var c, _ = fakeController(t, http.StatusRequestEntityTooLarge, refusal413,
		nil, 7)
	c.flush(context.Background())

	var line = journal.String()
	t.Logf("log: %s", strings.TrimSpace(line))
	if !strings.Contains(line, "dropped=7") {
		t.Errorf("the number of points sacrificed must appear in the log (dropped=7), "+
			"got: %s", line)
	}
	if !strings.Contains(line, "status=413") {
		t.Errorf("the refusal code must appear in the log, got: %s", line)
	}
	if !strings.Contains(line, "reduce maxPerFlush") {
		t.Errorf("the controller's reason must appear in the log, got: %s", line)
	}
	if !strings.Contains(line, "level=WARN") {
		t.Errorf("dropping measurements is a WARN, got: %s", line)
	}

	// And a TRANSIENT failure must not call itself "dropped": it is the same line
	// give or take one word, and that word is the whole difference for whoever
	// reads it.
	journal.Reset()
	var transient, _ = fakeController(t, http.StatusBadGateway,
		`{"error":"victoriametrics: connection refused"}`, nil, 7)
	transient.flush(context.Background())
	line = journal.String()
	if strings.Contains(line, "dropped") {
		t.Errorf("a 502 keeps the batch: the line must not talk about dropping, "+
			"got: %s", line)
	}
	if !strings.Contains(line, "kept in buffer") {
		t.Errorf("a 502 must say the batch is kept, got: %s", line)
	}
}

// TestARetryAfterPreventsTheDrop: the case a slightly broad `>= 400` would
// file on the wrong side. Exercised end to end and not only on isPermanentRefusal,
// because what matters is that `flush` READS that header.
func TestARetryAfterPreventsTheDrop(t *testing.T) {
	// A 413 accompanied by a "come back in 10 s" does not come from the batch
	// guard: it comes from an intermediary protecting itself, and it explicitly
	// asks for a replay.
	var c, rc = fakeController(t, http.StatusRequestEntityTooLarge,
		`{"error":"come back later"}`, map[string]string{"Retry-After": "10"}, 7)
	var ctx = context.Background()
	c.flush(ctx)
	c.flush(ctx)
	var batches = rc.received()
	if len(batches) != 2 {
		t.Fatalf("2 attempts expected, received %d", len(batches))
	}
	if batches[0] != batches[1] {
		t.Errorf("a refusal that says when to come back must be replayed as is: %+v then %+v",
			batches[0], batches[1])
	}
	// Returned to the queue, not dropped: the seven points are still there.
	var st = c.buf.Stats()
	if st.Queued != 7 || st.Pending != 0 {
		t.Errorf("the 7 points must come back into the queue, got %d queued and "+
			"%d reserves", st.Queued, st.Pending)
	}
}

// TestPermanentRefusalBoundary writes the boundary down case by case, with the
// WHY of each side.
//
// In six months this table will be worth more than re-reading the `switch`: it
// says what was decided, and for what reason. The two cases to defend are
// 401/403 — transient, a secret gets rotated and a revocation gets undone,
// whereas dropping would not ship one more point — and 429 with or without
// `Retry-After`, which asks for patience and is not a disagreement about the
// batch.
func TestPermanentRefusalBoundary(t *testing.T) {
	var cas = []struct {
		status     int
		retryAfter string
		permanent  bool
		why        string
	}{
		{http.StatusBadRequest, "", true,
			"400: the body was read and refused, the same bytes will get the same verdict"},
		{http.StatusRequestEntityTooLarge, "", true,
			"413: the content is fine, it is the volume that does not fit — and Take " +
				"cannot return a smaller batch"},
		{http.StatusUnsupportedMediaType, "", true,
			"415: flush always sends the same Content-Type"},
		{http.StatusUnprocessableEntity, "", true,
			"422: the controller understood the request and rejects its substance"},
		{http.StatusUnauthorized, "", false,
			"401: the batch is not even in question, authentication happens after the " +
				"body is read; a secret gets rotated"},
		{http.StatusForbidden, "", false,
			"403: a revocation gets undone, and dropping would not ship one more point " +
				"— it would only erase the trace"},
		{http.StatusNotFound, "", false,
			"404: route missing, controller of an earlier version or misconfigured URL"},
		{http.StatusMethodNotAllowed, "", false, "405: same as 404, on the method side"},
		{http.StatusRequestTimeout, "", false, "408: a timeout is to be retried"},
		{http.StatusConflict, "", false,
			"409: this route does not produce it — what is unknown is not classified " +
				"as permanent"},
		{http.StatusTooManyRequests, "", false,
			"429: a request for patience, not a disagreement about the batch"},
		{http.StatusTooManyRequests, "5", false, "429 with the announced delay"},
		{http.StatusInternalServerError, "", false, "500: the controller is in trouble"},
		{http.StatusBadGateway, "", false,
			"502: failed VictoriaMetrics write, a response chosen SO THAT the " +
				"proxy keeps its batch"},
		{http.StatusServiceUnavailable, "", false,
			"503: storage not configured, same intent"},
		{http.StatusServiceUnavailable, "30", false, "503 for an announced maintenance"},
		{http.StatusGatewayTimeout, "", false, "504: the controller did not answer in time"},
		{http.StatusNotImplemented, "", false,
			"501: an earlier controller, which a deployment fixes — no 5xx is " +
				"permanent"},
		{http.StatusRequestEntityTooLarge, "10", false,
			"413 + Retry-After: the header takes priority, it is what stops a too broad " +
				">= 400 from confusing patience with refusal"},
		{http.StatusBadRequest, "1", false,
			"400 + Retry-After: same rule, and it allows no exception — a refusal that " +
				"says when to come back is waiting for you to come back"},
	}
	var i int
	for i = range cas {
		var got = isPermanentRefusal(cas[i].status, cas[i].retryAfter)
		if got != cas[i].permanent {
			t.Errorf("status %d (Retry-After %q): permanent=%v expected %v — %s",
				cas[i].status, cas[i].retryAfter, got, cas[i].permanent, cas[i].why)
		}
	}
}

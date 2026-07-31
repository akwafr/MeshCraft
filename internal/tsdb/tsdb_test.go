package tsdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/metricbuf"
)

func TestFormat(t *testing.T) {
	var at = time.UnixMilli(1700000000000)
	var got = Format([]metricbuf.Sample{{
		Name:   "meshcraft_if_oper_status",
		Labels: map[string]string{"port": "GE1", "device": "dev-1"},
		Value:  1, TS: at,
	}})
	// Labels SORTED: two identical batches must produce the same text,
	// otherwise comparing two captures becomes impossible.
	var want = `meshcraft_if_oper_status{device="dev-1",port="GE1"} 1 1700000000000` + "\n"
	if got != want {
		t.Errorf("unexpected format:\n%q\nwant\n%q", got, want)
	}
}

// TestFormatEscapesLabels: a vendor port name is free text. Without escaping,
// one badly named device would break the entire BATCH.
func TestFormatEscapesLabels(t *testing.T) {
	var got = Format([]metricbuf.Sample{{
		Name: "m", Labels: map[string]string{"port": `GE1 "uplink"\x`}, Value: 2,
	}})
	if !strings.Contains(got, `port="GE1 \"uplink\"\\x"`) {
		t.Errorf("missing escape: %q", got)
	}
}

// TestFormatKeepsCollectionTimestamp: a batch replayed after an outage must
// land where it was MEASURED, otherwise recovery draws a spike that never
// happened.
func TestFormatKeepsCollectionTimestamp(t *testing.T) {
	var old = time.UnixMilli(1600000000000)
	var got = Format([]metricbuf.Sample{{Name: "m", Value: 1, TS: old}})
	if !strings.HasSuffix(strings.TrimSpace(got), "1600000000000") {
		t.Errorf("collection timestamp lost: %q", got)
	}
}

func TestWrite(t *testing.T) {
	var received string
	var srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b = make([]byte, 512)
		var n, _ = r.Body.Read(b)
		received = string(b[:n])
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	var c = New(srv.URL)
	var err = c.Write(context.Background(),
		[]metricbuf.Sample{{Name: "m", Value: 3, TS: time.Now()}})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.HasPrefix(received, "m 3 ") {
		t.Errorf("unexpected body: %q", received)
	}
}

// TestWriteSurfacesError: the caller must be able to PUT the batch back in its
// buffer. Silent success would lose the points.
func TestWriteSurfacesError(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	var err = New(srv.URL).Write(context.Background(),
		[]metricbuf.Sample{{Name: "m", Value: 1}})
	if err == nil {
		t.Error("storage refusal must surface")
	}
}

// TestClientDisabled: without configured storage, the error is explicit — a
// batch believed written and then lost is worse than a refused batch.
func TestClientDisabled(t *testing.T) {
	if New("") != nil {
		t.Fatal("empty URL must yield an inactive client")
	}
	var c *Client
	var err = c.Write(context.Background(),
		[]metricbuf.Sample{{Name: "m"}})
	if err != ErrDisabled {
		t.Errorf("explicit error expected, got %v", err)
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshcraft/internal/model"
	"meshcraft/internal/tsdb"
)

// THE LOW-RX RULE, pinned end to end against a faked series store.
//
// What these tests protect is the JUDGEMENT, not the plumbing: which cage gets
// flagged, against which reference, and which silence means what. Every case
// here is a morning that plays out differently — a fibre that degraded, a cage
// that has always been dark, a port label nobody can walk to — and merging any
// two of them costs either a missed fault or a trip to a healthy rack.

// fakeStore serves the two instant queries the comparison composes. It keys on
// the query TEXT (last_over_time vs min_over_time), which is deliberate: the
// day the composition changes shape, these tests fail here, visibly, rather
// than silently answering the wrong data to the right question.
func fakeStore(t *testing.T, current, floor []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.ParseForm() != nil {
				t.Error("unreadable form")
			}
			var q = r.Form.Get("query")
			var rows []map[string]any
			switch {
			case strings.Contains(q, "last_over_time("):
				rows = current
			case strings.Contains(q, "min_over_time("):
				rows = floor
			default:
				t.Errorf("unexpected query: %s", q)
			}
			var body, _ = json.Marshal(map[string]any{
				"status": "success",
				"data":   map[string]any{"resultType": "vector", "result": rows},
			})
			w.Write(body) //nolint:errcheck
		}))
}

func vmRow(device, port, lane string, v float64) map[string]any {
	var metric = map[string]string{"device": device, "port": port}
	if lane != "" {
		metric["lane"] = lane
	}
	return map[string]any{
		"metric": metric,
		"value":  []any{1786500000.0, fmt.Sprintf("%v", v)},
	}
}

func TestLowRxFlagsOnlyTheDropBelowOwnHistory(t *testing.T) {
	var devices = []model.Device{dev(idA, "ok", nil)}
	var srv = fakeStore(t,
		[]map[string]any{
			// 2.7 dB under its floor: flagged.
			vmRow(idA, "GE1", "0", -7.2),
			// At its floor: healthy, not a fault.
			vmRow(idA, "GE2", "0", -4.5),
			// 0.4 dB under: inside the margin, noise, not flagged.
			vmRow(idA, "GE3", "0", -4.9),
			// Dark after a healthy history: the deepest drop on the list.
			vmRow(idA, "GE4", "0", -30),
			// Dark with NO healthy history (unpatched cage): nothing to compare
			// against, nothing claimed.
			vmRow(idA, "GE5", "0", -30),
			// A label the facade cannot resolve: names nothing an operator can
			// walk to, dropped.
			vmRow(idA, "Management1", "0", -12),
		},
		[]map[string]any{
			vmRow(idA, "GE1", "0", -4.5),
			vmRow(idA, "GE2", "0", -4.5),
			vmRow(idA, "GE3", "0", -4.5),
			vmRow(idA, "GE4", "0", -4.0),
			vmRow(idA, "Management1", "0", -4.0),
		})
	defer srv.Close()

	var s = &Server{TSDB: tsdb.New(srv.URL)}
	var got = s.opticsRxAlerts(context.Background(), devices)
	if !got.Available {
		t.Fatal("store is configured: available must be true")
	}
	if len(got.Rows) != 2 {
		t.Fatalf("%d rows, want 2 (GE4 then GE1): %+v", len(got.Rows), got.Rows)
	}
	// Deepest drop first: the dark fibre (26 dB under) before the degraded one.
	if got.Rows[0].Port != "GE4" || got.Rows[1].Port != "GE1" {
		t.Errorf("order wrong, want GE4 then GE1: %+v", got.Rows)
	}
	if got.Rows[1].RxDBm != -7.2 || got.Rows[1].FloorDBm != -4.5 {
		t.Errorf("GE1 numbers wrong: %+v", got.Rows[1])
	}
	if got.Rows[0].Lane != "0" {
		t.Errorf("lane must pass through: %+v", got.Rows[0])
	}
}

// No store, and a store with no optical series, are two different sentences on
// screen — the reply must keep them apart the way the telemetry route does.
func TestLowRxDistinguishesNoStoreFromNoSeries(t *testing.T) {
	var s = &Server{}
	var got = s.opticsRxAlerts(context.Background(), []model.Device{dev(idA, "ok", nil)})
	if got.Available || got.Why == "" {
		t.Errorf("no store must answer available=false with a reason: %+v", got)
	}

	var srv = fakeStore(t, nil, nil)
	defer srv.Close()
	var s2 = &Server{TSDB: tsdb.New(srv.URL)}
	var got2 = s2.opticsRxAlerts(context.Background(), []model.Device{dev(idA, "ok", nil)})
	if !got2.Available || len(got2.Rows) != 0 {
		t.Errorf("no series must answer available=true with zero rows: %+v", got2)
	}
	// The rule's parameters travel with the answer so the screen can caption
	// the list without hard-coding a copy of the rule.
	if got2.MarginDB <= 0 || got2.WindowDays <= 0 {
		t.Errorf("margin and window must be stated: %+v", got2)
	}
}

// A store failure must yield an empty list, not a 502: one failing query must
// not blank the dashboard around it. (Same stance as telemetryOf.)
func TestLowRxAbsorbsAStoreFailure(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		}))
	defer srv.Close()
	var s = &Server{TSDB: tsdb.New(srv.URL)}
	var got = s.opticsRxAlerts(context.Background(), []model.Device{dev(idA, "ok", nil)})
	if len(got.Rows) != 0 {
		t.Errorf("failure must produce no rows: %+v", got.Rows)
	}
}

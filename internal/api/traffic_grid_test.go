package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestNearestSample(t *testing.T) {
	var m = map[int64]float64{100: 1.5, 160: 2.5}
	var v, ok = nearestSample(m, 100, 30)
	if !ok || v != 1.5 {
		t.Fatalf("exact: got %v %v", v, ok)
	}
	v, ok = nearestSample(m, 110, 30)
	if !ok || v != 1.5 {
		t.Fatalf("near 100: got %v %v", v, ok)
	}
	v, ok = nearestSample(m, 200, 30)
	if ok {
		t.Fatalf("too far: got %v", v)
	}
}

func TestTrafficPointJSONNull(t *testing.T) {
	// Empty slot must serialize rx/tx as null, not 0.
	var p = trafficPoint{TS: time.Unix(0, 0).UTC()}
	var raw, err = json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var s = string(raw)
	if !strings.Contains(s, `"rx_mbps":null`) || !strings.Contains(s, `"tx_mbps":null`) {
		t.Fatalf("want null rates, got %s", s)
	}
}

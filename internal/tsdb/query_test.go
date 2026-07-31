package tsdb

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Series READING, exercised on responses in the exact shape VictoriaMetrics returns.
//
// This package had no read tests for a simple reason: it had no reading. What
// follows covers the two faults that would produce silent charts or, worse, values
// attached to the wrong labels.

// TestDecodeUnVecteurEtUneMatrice : BOTH result shapes.
//
// `vector` carries `value` (one point), `matrix` carries `values` (a list).
// Accepting only one would fail half the queries with a message blaming storage.
func TestDecodeUnVecteurEtUneMatrice(t *testing.T) {
	var vecteur = `{"status":"success","data":{"resultType":"vector","result":[
	  {"metric":{"__name__":"meshcraft_if_in_octets_total","device":"d1","port":"SFP2"},
	   "value":[1769000000,"0.003"]}]}}`
	var series, err = decodeQuery([]byte(vecteur))
	if err != nil {
		t.Fatalf("vector refused: %v", err)
	}
	if len(series) != 1 || len(series[0].Points) != 1 {
		t.Fatalf("series = %+v", series)
	}
	if series[0].Labels["port"] != "SFP2" {
		t.Errorf("labels lost: %v", series[0].Labels)
	}
	var p, ok = series[0].Last()
	if !ok || p.Value != 0.003 {
		t.Errorf("value = %v (ok=%v); value is a STRING in this protocol, "+
			"decoding it as a number loses everything", p.Value, ok)
	}
	if p.At.Unix() != 1769000000 {
		t.Errorf("timestamp = %v", p.At)
	}

	var matrice = `{"status":"success","data":{"resultType":"matrix","result":[
	  {"metric":{"device":"d1"},"values":[[1769000000,"1.5"],[1769000060,"2.5"]]}]}}`
	series, err = decodeQuery([]byte(matrice))
	if err != nil {
		t.Fatalf("matrix refused: %v", err)
	}
	if len(series) != 1 || len(series[0].Points) != 2 {
		t.Fatalf("series = %+v", series)
	}
	if series[0].Points[0].Value != 1.5 || series[0].Points[1].Value != 2.5 {
		t.Errorf("points = %+v", series[0].Points)
	}
	// The LAST point, not the first: that is what "now" means.
	p, ok = series[0].Last()
	if !ok || p.Value != 2.5 {
		t.Errorf("Last = %v", p.Value)
	}
}

// TestUneSerieVideNEstPasUnZero : that distinction decides what a screen tells.
//
// A series with no points is what a range returns on a device not yet collected.
// Confusing it with zero would show "no traffic" where "no measurement" is correct
// — and the caller resets rates to zero on that basis.
func TestUneSerieVideNEstPasUnZero(t *testing.T) {
	var body = `{"status":"success","data":{"resultType":"matrix","result":[
	  {"metric":{"device":"d1"},"values":[]}]}}`
	var series, err = decodeQuery([]byte(body))
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(series) != 1 {
		t.Fatalf("series = %+v", series)
	}
	var _, ok = series[0].Last()
	if ok {
		t.Error("empty series returned a point")
	}
}

// TestNaNEtValeursIllisiblesSontEcartees
//
// `NaN` is what VictoriaMetrics uses for "no data at this instant". Publishing it
// would make a gap the screen draws as zero, therefore as absent traffic — a dip in
// the chart exactly where the measurement is missing.
func TestNaNEtValeursIllisiblesSontEcartees(t *testing.T) {
	var body = `{"status":"success","data":{"resultType":"matrix","result":[
	  {"metric":{"device":"d1"},"values":[
	     [1769000000,"NaN"],[1769000060,"3"],[1769000120,"not-a-number"],
	     [1769000180],["text","3"]]}]}}`
	var series, err = decodeQuery([]byte(body))
	if err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(series[0].Points) != 1 || series[0].Points[0].Value != 3 {
		t.Errorf("points = %+v; only the valid point should survive", series[0].Points)
	}
}

// TestUnRefusDuStockageEstUneErreur : "status: error" must not pass for an empty
// response.
//
// The two look alike to the caller — no series — and do not call for the same
// action: an empty response resets rates to zero, a refusal must touch NOTHING.
// The storage message is kept because it names the fault.
func TestUnRefusDuStockageEstUneErreur(t *testing.T) {
	var body = `{"status":"error","error":"unsupported duration \"2m0s\""}`
	var _, err = decodeQuery([]byte(body))
	if err == nil {
		t.Fatal("storage refusal was read as an empty response")
	}
	if !strings.Contains(err.Error(), "unsupported duration") {
		t.Errorf("storage message must be kept: %v", err)
	}
}

// TestLaRequeteParTLaFormeAttendue : POST, form body, and bounds.
//
// POST is not a detail: a query excluding uplinks from two hundred devices reaches
// several kilobytes, and in GET it would be truncated by a proxy — a truncated sum
// yields a plausible and false chart.
func TestLaRequeteAPILaFormeAttendue(t *testing.T) {
	var vu struct {
		method, path, query, start, end, step string
	}
	var srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body, _ = io.ReadAll(r.Body)
		var form, _ = parseForm(string(body))
		vu.method, vu.path = r.Method, r.URL.Path
		vu.query, vu.start, vu.end, vu.step = form["query"], form["start"], form["end"], form["step"]
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
	}))
	defer srv.Close()

	var c = New(srv.URL)
	var from = time.Unix(1769000000, 0)
	var _, err = c.QueryRange(context.Background(), "sum(rate(x[2m]))", from,
		from.Add(time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("query refused: %v", err)
	}
	if vu.method != http.MethodPost {
		t.Errorf("method = %s; a long query must use POST", vu.method)
	}
	if vu.path != "/api/v1/query_range" {
		t.Errorf("path = %s", vu.path)
	}
	if vu.query != "sum(rate(x[2m]))" {
		t.Errorf("query sent = %q", vu.query)
	}
	if vu.start != "1769000000" || vu.end != "1769003600" || vu.step != "60" {
		t.Errorf("bounds = %s..%s step %s", vu.start, vu.end, vu.step)
	}

	// A zero step is demoted to one second: VictoriaMetrics would refuse it, with
	// a syntax message for someone looking for an empty chart.
	_, err = c.QueryRange(context.Background(), "x", from, from.Add(time.Hour), 0)
	if err != nil {
		t.Fatalf("zero step refused: %v", err)
	}
	if vu.step != "1" {
		t.Errorf("step = %s, expected 1", vu.step)
	}
}

// TestUnClientSansStockageRefuseAuLieuDeMentir : a controller without
// MESHCRAFT_METRICS_URL yields a nil client, and querying it must say why.
func TestUnClientSansStockageRefuseAuLieuDeMentir(t *testing.T) {
	var c = New("  ")
	if c != nil {
		t.Fatal("empty URL must yield an inactive client")
	}
	var _, err = c.QueryInstant(context.Background(), "x")
	if err != ErrDisabled {
		t.Errorf("error = %v, expected ErrDisabled", err)
	}
	if _, err = c.QueryRange(context.Background(), "x", time.Now(), time.Now(), time.Minute); err != ErrDisabled {
		t.Errorf("error = %v, expected ErrDisabled", err)
	}
}

// TestUnCodeHTTPDErreurPorteLeCorps : the storage message is in the body, not the
// code.
func TestUnCodeHTTPDErreurPorteLeCorps(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`cannot parse query: unexpected token "}"`))
	}))
	defer srv.Close()
	var _, err = New(srv.URL).QueryInstant(context.Background(), "sum(}")
	if err == nil {
		t.Fatal("422 was accepted")
	}
	if !strings.Contains(err.Error(), "unexpected token") {
		t.Errorf("body must accompany the code: %v", err)
	}
}

// parseForm decodes a form body.
func parseForm(body string) (map[string]string, error) {
	var values, err = url.ParseQuery(body)
	if err != nil {
		return nil, err
	}
	var out = map[string]string{}
	var k string
	for k = range values {
		out[k] = values.Get(k)
	}
	return out, nil
}

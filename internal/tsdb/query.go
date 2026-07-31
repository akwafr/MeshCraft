package tsdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// THE READ HALF of this package, and its absence was costly.
//
// This package had only one method: `Write`. Collection therefore worked
// entirely — eleven series families, fresh to the second, verified on hardware —
// and NOTHING in the product could read them back. No route served them, no
// screen named a single series. What an operator saw was an interface without
// telemetry, indistinguishable from broken collection.
//
// Worse: the site traffic chart existed — RECONSTRUCTED from inventory instant
// rates, with a `simulated` flag to say so. And nobody was writing those rates.
// The chart was therefore flat at zero on every device whose configuration had
// not been imported from a file.
//
// The comment in `internal/api/traffic_handlers.go` already named the exit
// condition: "replace with a VictoriaMetrics read once SNMP collection feeds
// `meshcraft_if_in_octets_total`". That is done; this file is the missing piece.
//
// # WHY TWO QUERY FORMS AND NOT ONE
//
//	QueryInstant — one value per series, NOW. Needed to refresh current state in
//	               inventory (one rate per port), and a range would cost points
//	               nobody would use.
//	QueryRange   — a CURVE. Needed for a sliding 24 h window, and computing it
//	               from instants would require keeping history elsewhere, i.e.
//	               rewriting a time-series database.
//
// # POST IS NOT A DETAIL
//
// Both use POST and a form body, never a query string. A query listing uplink
// ports on two hundred devices to EXCLUDE them from a sum reaches several
// kilobytes: as GET it would be truncated by a proxy, and a truncated sum is
// invisible — it gives a plausible false curve. VictoriaMetrics accepts both forms.

// Point is a timestamped value.
type Point struct {
	At    time.Time
	Value float64
}

// Series is a series returned by storage: its labels and its points.
//
// Labels are kept AS-IS and not translated: the caller uses them to attach a
// rate to a front-panel port (`device`, `port`, `ifindex`), and interpreting
// them here would make this package the judge of what a port is.
type Series struct {
	Labels map[string]string
	Points []Point
}

// Last returns the last point of the series, and false if it is empty.
//
// A series WITHOUT points is normal, not an anomaly: that is what a range query
// returns on a device not yet collected. Confusing it with zero would display
// "no traffic" where you must read "no measurement".
func (s Series) Last() (Point, bool) {
	if len(s.Points) == 0 {
		return Point{}, false
	}
	return s.Points[len(s.Points)-1], true
}

// QueryInstant evaluates a query at the present instant.
func (c *Client) QueryInstant(ctx context.Context, query string) ([]Series, error) {
	if c == nil {
		return nil, ErrDisabled
	}
	var form = url.Values{}
	form.Set("query", query)
	return c.post(ctx, "/api/v1/query", form)
}

// QueryRange evaluates a query over a window, one point every `step`.
//
// Step is BOUNDED BELOW at one second: VictoriaMetrics rejects a zero step, and
// a negative step — duration arithmetic on a badly computed window — would
// produce an error whose message talks about syntax.
func (c *Client) QueryRange(ctx context.Context, query string, from, to time.Time,
	step time.Duration) ([]Series, error) {
	if c == nil {
		return nil, ErrDisabled
	}
	if step < time.Second {
		step = time.Second
	}
	var form = url.Values{}
	form.Set("query", query)
	form.Set("start", strconv.FormatInt(from.Unix(), 10))
	form.Set("end", strconv.FormatInt(to.Unix(), 10))
	form.Set("step", strconv.FormatInt(int64(step.Seconds()), 10))
	return c.post(ctx, "/api/v1/query_range", form)
}

// post executes the query and translates the response.
func (c *Client) post(ctx context.Context, path string, form url.Values) ([]Series, error) {
	var req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var resp *http.Response
	if resp, err = c.HTTP.Do(req); err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	// Body is read BEFORE status check: VictoriaMetrics puts its error message
	// inside, and refusing on code alone would show "HTTP 422" where storage
	// says which series identifier is at fault.
	var body []byte
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxQueryBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("metrics read: HTTP %d — %s", resp.StatusCode,
			excerpt(string(body)))
	}
	return decodeQuery(body)
}

// maxQueryBody bounds what we accept to read. A badly scoped query can ask for
// millions of points, and loading them into memory to discard them would make
// the controller the victim of its own question. The bound is high — 32 MiB,
// well beyond a screen curve — because exceeding it must be a VISIBLE error, not
// silence: a response truncated to the right size would give invalid JSON, hence
// a message about syntax.
const maxQueryBody = 32 << 20

// decodeQuery translates the VictoriaMetrics response.
//
// PURE function, therefore testable on captured responses: an error here would
// give silent curves or, worse, values attached to the wrong labels.
//
// BOTH RESULT FORMS are accepted, and both are needed: a `vector` carries
// `value` (one point), a `matrix` carries `values` (a list). Accepting only
// one would fail half the queries with a message blaming storage.
func decodeQuery(body []byte) ([]Series, error) {
	var reply struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"`
				Values [][]any           `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	var err = json.Unmarshal(body, &reply)
	if err != nil {
		return nil, fmt.Errorf("unreadable metrics storage response: %w", err)
	}
	if reply.Status != "success" {
		var why = reply.Error
		if why == "" {
			why = reply.Status
		}
		return nil, fmt.Errorf("metrics storage refused the request: %s", why)
	}
	var out = make([]Series, 0, len(reply.Data.Result))
	var i int
	for i = range reply.Data.Result {
		var raw = reply.Data.Result[i]
		var s = Series{Labels: raw.Metric}
		if s.Labels == nil {
			s.Labels = map[string]string{}
		}
		if len(raw.Value) > 0 {
			var p, ok = decodePoint(raw.Value)
			if ok {
				s.Points = append(s.Points, p)
			}
		}
		var j int
		for j = range raw.Values {
			var p, ok = decodePoint(raw.Values[j])
			if ok {
				s.Points = append(s.Points, p)
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// decodePoint reads a [timestamp, "value"] pair.
//
// Value is a STRING in the protocol, timestamp a number — a classic trap of this
// API: decoding both as numbers loses all values silently, yielding series that
// exist and are empty.
//
// `NaN` is dropped rather than returned: VictoriaMetrics uses it for "no data at
// this instant", and publishing it would draw a gap the screen would render as
// zero, i.e. as absent traffic.
func decodePoint(raw []any) (Point, bool) {
	if len(raw) < 2 {
		return Point{}, false
	}
	var ts, tsOK = raw[0].(float64)
	if !tsOK {
		return Point{}, false
	}
	var text, textOK = raw[1].(string)
	if !textOK {
		return Point{}, false
	}
	var v, err = strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(v) {
		return Point{}, false
	}
	// Timestamp is in seconds, with a possible fractional part.
	var sec = int64(ts)
	var nsec = int64((ts - float64(sec)) * float64(time.Second))
	return Point{At: time.Unix(sec, nsec).UTC(), Value: v}, true
}

func excerpt(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

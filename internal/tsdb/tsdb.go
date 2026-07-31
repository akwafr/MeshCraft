// Package tsdb writes metrics to VictoriaMetrics.
//
// The controller is the ONLY component that talks to storage: proxies push
// their batches to it, and it relays them. That keeps VictoriaMetrics
// unreachable from outside, like the database — a proxy is by nature on a
// network we do not control.
//
// See docs/fr/01-Architecture/Monitoring (unified monitoring and pipelines) §3.
package tsdb

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/metricbuf"
)

// Client pushes samples. Nil-safe: a controller without metric storage works
// and says so, rather than failing at startup for a service the inventory does
// not need.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New builds the client. An empty URL yields an inactive client.
func New(baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		return nil
	}
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// escapeLabel protects a label value.
//
// A port name can contain a quote or backslash — vendor labels are free text.
// Without escaping, one badly named device would break the entire BATCH, not
// just its own line.
func escapeLabel(v string) string {
	var r = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(v)
}

// Format renders the batch in Prometheus exposition format, which VictoriaMetrics
// ingests natively.
//
// Pure and testable: an error here would produce silent series, i.e. an empty
// screen we would spend hours explaining.
func Format(samples []metricbuf.Sample) string {
	var b strings.Builder
	var s metricbuf.Sample
	for _, s = range samples {
		if s.Name == "" {
			continue
		}
		b.WriteString(s.Name)
		if len(s.Labels) > 0 {
			// Stable order: two identical batches must produce the same text,
			// otherwise comparing two captures becomes impossible.
			var keys = make([]string, 0, len(s.Labels))
			var k string
			for k = range s.Labels {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			b.WriteString("{")
			var i int
			for i, k = range keys {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(k)
				b.WriteString(`="`)
				b.WriteString(escapeLabel(s.Labels[k]))
				b.WriteString(`"`)
			}
			b.WriteString("}")
		}
		b.WriteString(" ")
		b.WriteString(strconv.FormatFloat(s.Value, 'g', -1, 64))
		// The timestamp is that of COLLECTION, in milliseconds, not arrival. A
		// batch replayed after a tunnel outage must land where it was measured,
		// otherwise recovery draws a spike that never happened.
		if !s.TS.IsZero() {
			b.WriteString(" ")
			b.WriteString(strconv.FormatInt(s.TS.UnixMilli(), 10))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Write pushes a batch. Returns an error if storage refuses: the caller must be
// able to PUT the batch back in its buffer rather than lose it.
func (c *Client) Write(ctx context.Context, samples []metricbuf.Sample) error {
	if c == nil {
		return ErrDisabled
	}
	if len(samples) == 0 {
		return nil
	}
	var body = Format(samples)
	if body == "" {
		return nil
	}
	var req, err = http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/api/v1/import/prometheus", bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	var resp *http.Response
	if resp, err = c.HTTP.Do(req); err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("metrics storage: HTTP %d", resp.StatusCode)
	}
	return nil
}

type tsdbError string

func (e tsdbError) Error() string { return string(e) }

// ErrDisabled: no storage configured. An explicit error rather than silent
// success — a batch believed written and then lost is worse than a refused
// batch.
const ErrDisabled = tsdbError("metrics storage not configured")

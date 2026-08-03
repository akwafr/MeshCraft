package discovery

import (
	"context"
	"sync"
	"time"
)

// DefaultConcurrency: enough to scan a /24 in a few seconds, low enough to
// not look like an attack or saturate a slow link.
const DefaultConcurrency = 24

// ScanReport summarizes a scan. Unreachable hosts are not returned.
type ScanReport struct {
	Prefix    string `json:"prefix"`
	Addresses int    `json:"addresses"`
	// Scanned / Truncated: the real COVERAGE. A scan interrupted by the time
	// budget must say so — "0 devices found" on a /16 that was only half
	// walked isn't a result, it's a misunderstanding.
	Scanned   int      `json:"scanned"`
	Truncated bool     `json:"truncated"`
	Found     int      `json:"found"`
	Duration  string   `json:"duration"`
	Results   []Result `json:"results"`
}

// ConcurrencyFor adapts the parallelism to the size of the range. Stay
// polite on a /24, go fast on a /16 — without ever looking like an attack.
func ConcurrencyFor(addresses int) int {
	switch {
	case addresses > 8192:
		return 192
	case addresses > 1024:
		return 96
	default:
		return DefaultConcurrency
	}
}

// Scan probes every address in a range.
//
// Credential attempts are only made against hosts that expose SSH, which
// Probe decides on its own. The context governs the stop: a canceled scan
// returns whatever it has already found.
func Scan(ctx context.Context, prefix string, creds []Credential, concurrency int, dialer Dialer) (ScanReport, error) {
	var addrs, err = ExpandPrefix(prefix)
	if err != nil {
		return ScanReport{Prefix: prefix}, err
	}
	if concurrency <= 0 {
		concurrency = ConcurrencyFor(len(addrs))
	}

	var started = time.Now()
	var report = ScanReport{Prefix: prefix, Addresses: len(addrs), Results: []Result{}}
	var prober = &Prober{FetchConfig: false, Dialer: dialer}

	var mu sync.Mutex
	var wg sync.WaitGroup
	var slots = make(chan struct{}, concurrency)
	var ip string
	for _, ip = range addrs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		slots <- struct{}{}
		go func(target string) {
			defer wg.Done()
			defer func() { <-slots }()
			var res = prober.Probe(ctx, target, creds)
			mu.Lock()
			report.Scanned++
			if res.Reachable {
				report.Results = append(report.Results, res)
			}
			mu.Unlock()
		}(ip)
	}
	wg.Wait()

	report.Found = len(report.Results)
	report.Truncated = report.Scanned < report.Addresses
	report.Duration = time.Since(started).Round(time.Millisecond).String()
	return report, nil
}

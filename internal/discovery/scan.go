package discovery

import (
	"context"
	"sync"
	"time"
)

// DefaultConcurrency : assez pour balayer un /24 en quelques secondes, assez
// peu pour ne pas ressembler a une attaque ni saturer un lien lent.
const DefaultConcurrency = 24

// ScanReport resume un balayage. Les hotes injoignables ne sont pas retournes.
type ScanReport struct {
	Prefix    string `json:"prefix"`
	Addresses int    `json:"addresses"`
	// Scanned / Truncated : la COUVERTURE reelle. Un balayage interrompu par le
	// temps imparti doit le dire — « 0 device trouve » sur un /16 a half
	// parcouru n'est pas un result, c'est un malentendu.
	Scanned   int      `json:"scanned"`
	Truncated bool     `json:"truncated"`
	Found     int      `json:"found"`
	Duration  string   `json:"duration"`
	Results   []Result `json:"results"`
}

// ConcurrencyFor adapte le parallelisme a la taille de la plage. Rester poli sur
// un /24, aller vite sur un /16 — sans jamais ressembler a une attaque.
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

// Scan sonde toutes les adresses d'une plage.
//
// Les essais d'identifiants ne sont tentes que sur les hotes qui exposent SSH,
// ce que Probe decide lui-same. Le contexte gouverne l'arret : un scan annule
// renvoie ce qu'il a already trouve.
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

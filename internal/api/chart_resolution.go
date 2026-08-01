package api

import "time"

// Chart sampling intervals by displayed window.
//
// Chosen so each preset (24 h / 3 d / 7 d / 30 d) keeps a comparable point
// density on screen — roughly one to three thousand samples — instead of a
// fixed point budget that coarsens as the window grows (96 points over 30 d
// used to mean one sample every 7.5 h).
const (
	chartStep24h = 1 * time.Minute
	chartStep3d  = 5 * time.Minute
	chartStep7d  = 10 * time.Minute
	chartStep30d = 15 * time.Minute

	chartWindow24h = 24 * time.Hour
	chartWindow3d  = 3 * 24 * time.Hour
	chartWindow7d  = 7 * 24 * time.Hour
	chartWindow30d = 30 * 24 * time.Hour

	// Safety cap if a caller asks for an exotic window: widen the step rather
	// than flood VictoriaMetrics / the browser with millions of points.
	maxChartPoints = 3000
)

// chartStep returns the sampling step for a given chart window.
func chartStep(window time.Duration) time.Duration {
	if window <= 0 {
		window = chartWindow24h
	}
	var step time.Duration
	switch {
	case window <= chartWindow24h:
		step = chartStep24h
	case window <= chartWindow3d:
		step = chartStep3d
	case window <= chartWindow7d:
		step = chartStep7d
	default:
		step = chartStep30d
	}
	// If the window is larger than 30 d (caller mistake), still widen so we
	// stay under the point cap.
	var points = chartPointCount(window, step)
	if points > maxChartPoints {
		step = window / time.Duration(maxChartPoints-1)
		if step < time.Second {
			step = time.Second
		}
		step = step.Round(time.Second)
	}
	return step
}

// chartPointCount is inclusive of both window endpoints (N intervals → N+1 points).
func chartPointCount(window, step time.Duration) int {
	if step <= 0 {
		return 1
	}
	var n = int(window/step) + 1
	if n < 2 {
		return 2
	}
	return n
}

package api

import (
	"testing"
	"time"
)

func TestChartStepPresets(t *testing.T) {
	var cases = []struct {
		window time.Duration
		step   time.Duration
	}{
		{24 * time.Hour, time.Minute},
		{3 * 24 * time.Hour, 5 * time.Minute},
		{7 * 24 * time.Hour, 10 * time.Minute},
		{30 * 24 * time.Hour, 15 * time.Minute},
		// Intermediate windows inherit the coarser bucket they fall into.
		{12 * time.Hour, time.Minute},
		{48 * time.Hour, 5 * time.Minute},
		{5 * 24 * time.Hour, 10 * time.Minute},
		{10 * 24 * time.Hour, 15 * time.Minute},
	}
	var i int
	for i = range cases {
		var got = chartStep(cases[i].window)
		if got != cases[i].step {
			t.Errorf("chartStep(%v) = %v, want %v", cases[i].window, got, cases[i].step)
		}
	}
}

func TestChartPointCountHomogeneous(t *testing.T) {
	// Roughly the same order of magnitude for every preset.
	var presets = []time.Duration{
		24 * time.Hour,
		3 * 24 * time.Hour,
		7 * 24 * time.Hour,
		30 * 24 * time.Hour,
	}
	var i int
	for i = range presets {
		var step = chartStep(presets[i])
		var n = chartPointCount(presets[i], step)
		if n < 800 || n > maxChartPoints {
			t.Errorf("window %v → %d points (step %v); want ~800–%d",
				presets[i], n, step, maxChartPoints)
		}
	}
}

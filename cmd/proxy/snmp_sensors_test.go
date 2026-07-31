package main

import (
	"testing"
	"time"

	"meshcraft/internal/tunnel"
)

// The CADENCE of the two sensor sets, from the side that runs it.
//
// The controller decides to turn them on and the proxy applies: that's the
// meaning of the value ZERO, which means "don't collect" and not "collect as
// fast as possible". This file locks down that convention where it's read,
// because confusing it with a "default" would produce the worst possible
// outcome — sixteen walks per second and per device, across an entire
// fleet, for OIDs a non-MikroTik platform doesn't even serve.

// TestAZeroCadenceDoesNotPoll
//
// This is `tunnel.SNMPWatch`'s convention: `health` and `optical_power`
// aren't universal — their foundation is proprietary, and there's no
// standardized MIB for a switch's health. The controller therefore only
// turns them on if the connector declares an SNMP channel for them,
// leaving the cadence at zero otherwise.
//
// Without this convention, a device from another brand would receive
// sixteen requests per cycle only to return "no such object" — traffic for
// nothing, every cycle, forever.
func TestAZeroCadenceDoesNotPoll(t *testing.T) {
	var c = NewSNMPCollector("http://example.invalid", "px-1", "s")
	var now = time.Now()

	if c.due("dev-1|health", now, 0) {
		t.Error("a zero cadence must read as \"don't collect\": zero isn't " +
			"a default, it's a controller decision")
	}
	if c.due("dev-1|optical", now, 0) {
		t.Error("same for optical diagnostics")
	}
	// A NEGATIVE cadence — which no path produces, but a mangled JSON
	// decode might — is treated as zero, not as "poll without limit".
	if c.due("dev-1|health", now, -30) {
		t.Error("a negative cadence must not be read as \"poll without limit\"")
	}
}

// TestAPositiveCadenceHoldsItsPeriod
//
// The first poll goes out right away — otherwise a proxy that just
// received its directive would wait five minutes before the first
// temperature reading — then the period is held.
func TestAPositiveCadenceHoldsItsPeriod(t *testing.T) {
	var c = NewSNMPCollector("http://example.invalid", "px-1", "s")
	var t0 = time.Now()

	if !c.due("dev-1|health", t0, 300) {
		t.Fatal("the first poll must go out right away: waiting for the " +
			"first period would leave five minutes with no health reading at all")
	}
	if c.due("dev-1|health", t0.Add(299*time.Second), 300) {
		t.Error("the period isn't held: one second before the deadline, nothing should go out")
	}
	if !c.due("dev-1|health", t0.Add(300*time.Second), 300) {
		t.Error("at the deadline, the poll must go out again")
	}
}

// TestTheFourSetsDoNotShareTheirDeadline
//
// Cadence keys are distinct per metric. Mixing them up would make a 5s
// counters poll push back the health deadline, so no temperature would ever
// be read on a device whose traffic is collected — total silence, and
// perfectly invisible.
func TestTheFourSetsDoNotShareTheirDeadline(t *testing.T) {
	var c = NewSNMPCollector("http://example.invalid", "px-1", "s")
	var t0 = time.Now()

	var keys = []string{"dev-1|state", "dev-1|counters", "dev-1|health", "dev-1|optical"}
	var k string
	for _, k = range keys {
		if !c.due(k, t0, 5) {
			t.Fatalf("%s: the first poll must go out", k)
		}
	}
	// The counters poll fires again five seconds later; health, on its own
	// cadence, must not be affected.
	if !c.due("dev-1|counters", t0.Add(5*time.Second), 5) {
		t.Error("counters must fire again at their deadline")
	}
	if c.due("dev-1|health", t0.Add(5*time.Second), 300) {
		t.Error("health must NOT fire again because counters did: a shared key " +
			"would mean no temperature is ever read on a device whose traffic is " +
			"collected")
	}
}

// TestTwoDevicesDoNotShareTheirDeadline
//
// The key carries the device identifier. Without it, the first device
// polled would push back the deadline for the whole fleet — and only one
// switch would be monitored.
func TestTwoDevicesDoNotShareTheirDeadline(t *testing.T) {
	var c = NewSNMPCollector("http://example.invalid", "px-1", "s")
	var t0 = time.Now()

	if !c.due("dev-1|health", t0, 300) {
		t.Fatal("first device")
	}
	if !c.due("dev-2|health", t0, 300) {
		t.Error("the second device must be polled too: a key with no identifier " +
			"would monitor only one switch of the fleet")
	}
}

// TestControllerDirectiveCarriesTheFourCadences
//
// Verifies the CONTRACT rather than the behavior: the four fields exist on
// the directive, and a proxy decoding a directive from an older controller
// finds zero there — so it doesn't collect sensors, which is the intended
// behavior during a staggered rollout.
func TestControllerDirectiveCarriesTheFourCadences(t *testing.T) {
	// A directive from a controller OLDER than this change: the two new
	// fields don't exist in its JSON, so they're zero at decision time.
	var older = tunnel.SNMPWatch{
		DeviceID: "dev-1", IP: "10.0.0.1",
		StateIntervalS: 30, CountersIntervalS: 5,
	}
	if older.HealthIntervalS != 0 || older.OpticalIntervalS != 0 {
		t.Error("a directive without the new fields must leave sensors off")
	}

	var current = tunnel.SNMPWatch{
		DeviceID: "dev-1", IP: "10.0.0.1",
		StateIntervalS: 30, CountersIntervalS: 5,
		HealthIntervalS: 300, OpticalIntervalS: 60,
	}
	var target = targetOf(current)
	if target.DeviceID != "dev-1" || target.IP != "10.0.0.1" {
		t.Errorf("the target is mistranslated: %+v", target)
	}
	if current.HealthIntervalS != 300 || current.OpticalIntervalS != 60 {
		t.Error("sensor cadences must cross the directive unchanged")
	}
}

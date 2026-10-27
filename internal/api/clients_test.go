package api

import (
	"strconv"
	"testing"
	"time"

	"meshcraft/internal/model"
)

// The location arbitration, replayed on the LAB'S REAL TOPOLOGY of
// 2026-08-13. The RB5009 (04:f4:1c:1d:74:58) is a router plugged into the
// CRS305's sfp-sfpplus3; because every switch on the path learns it, the raw
// sightings put it on the core's Et48 and the s3900's GE26 too. Whoever goes
// to unplug it must be sent to the CRS305.

const macRB5009 = "04:f4:1c:1d:74:58"
const devCore = "core01"
const devCRS305 = "crs305"
const devS3900 = "s3900"

func sighting(dev, port string, vlan int, mac string, seen time.Time) model.MACSighting {
	return model.MACSighting{DeviceID: dev, SiteID: "site", Port: port, VLAN: vlan,
		MAC: mac, FirstSeen: seen, LastSeen: seen}
}

func labLinks() []model.LLDPLink {
	return []model.LLDPLink{
		// Fleet-to-fleet: uplinks on both ends.
		{ADevice: devS3900, APort: "GE26", BDevice: devCore, BPort: "ET48"},
		// The CRS305's client link: the far end is NOT a fleet device
		// (BDevice empty) — its port must stay includable.
		{ADevice: devCRS305, APort: "sfp-sfpplus3", BLabel: "RB5009UG"},
	}
}

func TestClientIsPlacedAtTheEdgeNotOnUplinks(t *testing.T) {
	var now = time.Now()
	var sightings = []model.MACSighting{
		sighting(devCore, "ET48", 99, macRB5009, now),
		sighting(devS3900, "GE26", 99, macRB5009, now),
		sighting(devCRS305, "sfp-sfpplus3", 99, macRB5009, now),
	}
	var uplinks = uplinkPorts(labLinks(), nil)
	var locs = clientLocations(sightings, uplinks)
	var loc, found = locs[macRB5009]
	if !found {
		t.Fatal("client vanished — uplink exclusion ate the edge sighting too")
	}
	if loc.DeviceID != devCRS305 || loc.Port != "sfp-sfpplus3" {
		t.Errorf("located at %s/%s, want the CRS305 edge port — an uplink "+
			"location sends someone to unplug an inter-switch trunk", loc.DeviceID, loc.Port)
	}
}

// A MAC seen ONLY on inter-switch ports is infrastructure (a fleet switch's
// own chassis): no location, absent from the page. That absence is the
// feature that keeps nine switches from listing each other as clients.
func TestInfrastructureMACsGetNoLocation(t *testing.T) {
	var now = time.Now()
	var sightings = []model.MACSighting{
		sighting(devCore, "ET48", 99, "b8:69:f4:5b:a0:d2", now),
		sighting(devS3900, "GE26", 99, "b8:69:f4:5b:a0:d2", now),
	}
	var locs = clientLocations(sightings, uplinkPorts(labLinks(), nil))
	if len(locs) != 0 {
		t.Errorf("locations = %v, want none for a MAC living only on uplinks", locs)
	}
}

// The hypervisor-trunk case, measured on the lab: one MAC in four VLANs on
// one jack must be ONE client carrying the four, not four rows.
func TestTrunkedClientCarriesItsVLANs(t *testing.T) {
	var now = time.Now()
	var sightings = []model.MACSighting{
		sighting(devCRS305, "sfp-sfpplus3", 1, macRB5009, now),
		sighting(devCRS305, "sfp-sfpplus3", 42, macRB5009, now),
		sighting(devCRS305, "sfp-sfpplus3", 98, macRB5009, now),
		sighting(devCRS305, "sfp-sfpplus3", 99, macRB5009, now),
	}
	var locs = clientLocations(sightings, map[string]bool{})
	var loc = locs[macRB5009]
	if len(loc.VLANs) != 4 || loc.VLANs[0] != 1 || loc.VLANs[3] != 99 {
		t.Errorf("VLANs = %v, want [1 42 98 99]", loc.VLANs)
	}
	if loc.Elsewhere != 0 {
		t.Errorf("elsewhere = %d, want 0 — same jack four times is one place", loc.Elsewhere)
	}
}

// Behind an UNMANAGED switch, the client shows on several non-uplink ports;
// fewest distinct MACs wins, and the hedge (Elsewhere) is nonzero so the
// screen can say the location is a heuristic.
func TestFewestNeighboursBreaksUnmanagedSwitchAmbiguity(t *testing.T) {
	var now = time.Now()
	var laptop = "aa:aa:aa:aa:aa:01"
	var sightings = []model.MACSighting{
		// The edge port: laptop alone.
		sighting(devCRS305, "ether1", 1, laptop, now),
		// A busy port elsewhere that also learned it (flooding path).
		sighting(devS3900, "GE12", 1, laptop, now),
		sighting(devS3900, "GE12", 1, "bb:bb:bb:bb:bb:02", now),
		sighting(devS3900, "GE12", 1, "bb:bb:bb:bb:bb:03", now),
	}
	var locs = clientLocations(sightings, map[string]bool{})
	var loc = locs[laptop]
	if loc.DeviceID != devCRS305 || loc.Port != "ether1" {
		t.Errorf("located at %s/%s, want the port with fewest neighbours", loc.DeviceID, loc.Port)
	}
	if loc.Elsewhere != 1 {
		t.Errorf("elsewhere = %d, want 1 — the screen must be able to hedge", loc.Elsewhere)
	}
}

// VLAN 0 (the flat BRIDGE-MIB regime — SwOS) must not surface as "VLAN 0".
func TestUnknownVLANStaysAbsentNotZero(t *testing.T) {
	var now = time.Now()
	var locs = clientLocations([]model.MACSighting{
		sighting("css326", "GE5", 0, "cc:cc:cc:cc:cc:04", now),
	}, map[string]bool{})
	if len(locs["cc:cc:cc:cc:cc:04"].VLANs) != 0 {
		t.Errorf("VLANs = %v, want empty for a platform that does not say",
			locs["cc:cc:cc:cc:cc:04"].VLANs)
	}
}

// ---------------------------------------------------------------------------
// THE BROOM'S WINDOW
//
// `parseClientPruneDays` is the only thing between a button and an emptied
// ledger, and the whole failure class is the same: a window that is not what
// the caller meant, read as if it were. These assertions are written as that
// failure — each case names what it would have deleted.

func TestPruneWindowRefusesEverythingThatIsNotAWindow(t *testing.T) {
	// An ABSENT window must never acquire a default. Any default of a
	// "clean up" gesture deletes rows nobody chose, and the caller that
	// forgot the parameter is exactly the one that cannot say which.
	var cases = map[string]string{
		"":     "absent",
		"   ":  "blank",
		"0":    "zero — would forget every client, online ones included",
		"-1":   "negative — a cutoff in the FUTURE, i.e. the whole site",
		"-30":  "negative",
		"1.5":  "fractional — Atoi refuses it rather than truncating to 1",
		"abc":  "prose",
		"7d":   "a unit the API does not take",
		"3651": "above the ten-year ceiling",
		"1e3":  "scientific notation",
	}
	var raw, why string
	for raw, why = range cases {
		var _, err = parseClientPruneDays(raw)
		if err == nil {
			t.Errorf("parseClientPruneDays(%q) accepted a %s window", raw, why)
		}
	}
}

func TestPruneWindowAcceptsWhatTheScreenOffers(t *testing.T) {
	// The five choices the Clients modal offers, by the numbers it sends.
	var raw string
	for _, raw = range []string{"1", "3", "7", "30", "180"} {
		var n, err = parseClientPruneDays(raw)
		if err != nil {
			t.Errorf("parseClientPruneDays(%q) = %v, want the window the UI offers", raw, err)
		}
		if strconv.Itoa(n) != raw {
			t.Errorf("parseClientPruneDays(%q) = %d, want the number as typed", raw, n)
		}
	}
	// And the boundaries of the guard itself, which must be INCLUSIVE — an
	// off-by-one here rejects the ceiling the error message promises.
	var n, err = parseClientPruneDays("3650")
	if err != nil || n != clientPruneMaxDays {
		t.Errorf("the ten-year ceiling must be reachable, got (%d, %v)", n, err)
	}
	if n, err = parseClientPruneDays(" 1 "); err != nil || n != 1 {
		t.Errorf("a padded parameter must parse, got (%d, %v)", n, err)
	}
}

// The cutoff is the instant a client must have been seen AFTER to survive, so
// it is always in the past and moves one day per day of window. A sign error
// here deletes the present instead of the past, and nothing else would catch
// it: both directions compile and both return a plausible timestamp.
func TestPruneCutoffLiesInThePast(t *testing.T) {
	var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var days int
	for _, days = range []int{1, 3, 7, 30, 180} {
		var cut = clientPruneCutoff(days, now)
		if !cut.Before(now) {
			t.Fatalf("cutoff for %d days is %v, not before %v — the broom would "+
				"sweep the present", days, cut, now)
		}
		if now.Sub(cut) != time.Duration(days)*24*time.Hour {
			t.Errorf("cutoff for %d days is %v behind now, want exactly %d×24h",
				days, now.Sub(cut), days)
		}
	}
	// A client seen one second after the cutoff survives; one second before it
	// does not. That is the `last_seen < $2` the statement carries, restated
	// here because the two must move together.
	var cut = clientPruneCutoff(1, now)
	if !cut.Add(time.Second).After(cut) || !cut.Add(-time.Second).Before(cut) {
		t.Error("the cutoff must be a strict threshold on last_seen")
	}
}

package api

import (
	"sort"
	"time"

	"meshcraft/internal/model"
)

// The Clients page's decisions, PURE — every function here is testable without
// a database, and each one guards against drawing a credible false machine.
//
// # The problem a forwarding table does NOT solve by itself
//
// A MAC appears in the FDB of EVERY switch on its path: the laptop on the
// 1930's port 12 is also "on" the CRS326's uplink, the core's Et48, and every
// trunk in between. Listing sightings raw would show one laptop as five rows
// on five switches, four of them naming an inter-switch port someone could go
// unplug. Two rules turn sightings into clients:
//
//  1. UPLINK EXCLUSION — a sighting on a port that is one end of a known
//     fleet-to-fleet link (LLDP-derived or hand-entered) is transit, not
//     residence. This also removes the fleet's own chassis MACs from the
//     page for free: an infrastructure MAC is only ever learned on
//     inter-switch ports.
//
//  2. FEWEST-NEIGHBOURS WINS — a client behind an UNMANAGED switch (or a
//     flooding SwOS) is still sighted on several non-uplink ports. The edge
//     port is the one hosting the fewest distinct MACs: the port with two
//     MACs is closer to the machine than the port with forty. The classic
//     MAC-tracking heuristic, and the tie goes to the freshest sighting.

// clientLocation is where the site believes one MAC lives.
type clientLocation struct {
	DeviceID string
	Port     string
	// VLANs the MAC is learned in AT that location, sorted. Several is
	// ordinary — a hypervisor trunk carries its VMs' VLANs (measured: the
	// RB5009 appears in VLANs 1, 42, 98 and 99 of the lab's core).
	VLANs []int
	// FirstSeen / LastSeen of the location's sightings (the ledger's own
	// first_seen may be older — the MAC may have lived elsewhere).
	FirstSeen time.Time
	LastSeen  time.Time
	// Elsewhere: distinct (device,port) pairs this MAC was ALSO sighted on
	// (non-uplink), beyond the chosen one. Zero on a healthy access port;
	// nonzero means the choice was a heuristic and the page should say so.
	Elsewhere int
}

// clientPortKey names one jack fleet-wide.
func clientPortKey(deviceID, port string) string { return deviceID + "|" + port }

// uplinkPorts collects every port that is one end of a FLEET-TO-FLEET link.
//
// A derived link whose far end is not a fleet device (BDevice == "") is a
// CLIENT link — the pve01 case — and its port must stay includable, which is
// why the LLDP-derived side filters on BDevice presence.
func uplinkPorts(derived []model.LLDPLink, manual []model.DeviceLink) map[string]bool {
	var out = map[string]bool{}
	var i int
	for i = range derived {
		if derived[i].BDevice == "" {
			continue
		}
		out[clientPortKey(derived[i].ADevice, derived[i].APort)] = true
		out[clientPortKey(derived[i].BDevice, derived[i].BPort)] = true
	}
	for i = range manual {
		out[clientPortKey(manual[i].ADevice, manual[i].APort)] = true
		out[clientPortKey(manual[i].BDevice, manual[i].BPort)] = true
	}
	return out
}

// clientLocations decides, per MAC, where it lives. MACs sighted ONLY on
// uplink ports get no location and are absent from the result — that is the
// infrastructure-MAC case, and it is a feature, not a loss.
func clientLocations(sightings []model.MACSighting,
	uplinks map[string]bool) map[string]clientLocation {
	// Distinct MACs per non-uplink port: the "fewest neighbours" denominator.
	var macsOnPort = map[string]map[string]bool{}
	var i int
	for i = range sightings {
		var key = clientPortKey(sightings[i].DeviceID, sightings[i].Port)
		if uplinks[key] {
			continue
		}
		if macsOnPort[key] == nil {
			macsOnPort[key] = map[string]bool{}
		}
		macsOnPort[key][sightings[i].MAC] = true
	}

	// Candidate sightings per MAC, non-uplink only.
	var byMAC = map[string][]model.MACSighting{}
	for i = range sightings {
		if uplinks[clientPortKey(sightings[i].DeviceID, sightings[i].Port)] {
			continue
		}
		byMAC[sightings[i].MAC] = append(byMAC[sightings[i].MAC], sightings[i])
	}

	var out = map[string]clientLocation{}
	var mac string
	for mac = range byMAC {
		var cands = byMAC[mac]
		// Choose the port hosting the fewest distinct MACs; freshest breaks ties.
		var best = -1
		var c int
		for c = range cands {
			if best < 0 {
				best = c
				continue
			}
			var nb = len(macsOnPort[clientPortKey(cands[c].DeviceID, cands[c].Port)])
			var nbBest = len(macsOnPort[clientPortKey(cands[best].DeviceID, cands[best].Port)])
			if nb < nbBest ||
				(nb == nbBest && cands[c].LastSeen.After(cands[best].LastSeen)) {
				best = c
			}
		}
		var loc = clientLocation{
			DeviceID:  cands[best].DeviceID,
			Port:      cands[best].Port,
			FirstSeen: cands[best].FirstSeen,
			LastSeen:  cands[best].LastSeen,
		}
		// VLANs and window: every sighting AT the chosen jack contributes.
		var others = map[string]bool{}
		var vlans = map[int]bool{}
		for c = range cands {
			var here = cands[c].DeviceID == loc.DeviceID && cands[c].Port == loc.Port
			if !here {
				others[clientPortKey(cands[c].DeviceID, cands[c].Port)] = true
				continue
			}
			if cands[c].VLAN > 0 {
				vlans[cands[c].VLAN] = true
			}
			if cands[c].FirstSeen.Before(loc.FirstSeen) {
				loc.FirstSeen = cands[c].FirstSeen
			}
			if cands[c].LastSeen.After(loc.LastSeen) {
				loc.LastSeen = cands[c].LastSeen
			}
		}
		var v int
		for v = range vlans {
			loc.VLANs = append(loc.VLANs, v)
		}
		sort.Ints(loc.VLANs)
		loc.Elsewhere = len(others)
		out[mac] = loc
	}
	return out
}

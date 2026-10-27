package api

import (
	"log/slog"
	"net/http"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// The forwarding-table push route: what the Clients page is fed by.
//
// Everything here mirrors handleProxyLLDP deliberately — same authority
// (SNMP collection, `authorityPoll`), same frank 403 on scope refusal, same
// one-device-per-request shape — because it is the same kind of payload with
// the same threat model: this route writes INVENTORY, and a compromised proxy
// allowed to invent sightings could place any MAC on any port of a site it
// does not cover. A false "this laptop is on port 12" costs a walk to the
// wrong closet.

// maxProxyFDBBody bounds the push body.
//
// One device per request. The worst plausible device is a 52-port access
// switch at ~120 bytes of JSON per entry; even a pathological 4,096-entry
// table (a bridge's common FDB ceiling) stays under 512 KiB. The generic
// 1 MiB ceiling covers that with headroom, so no new constant is invented.
const maxProxyFDBBody = 1 << 20

// proxyFDBPush is the body expected on /api/proxy/fdb.
type proxyFDBPush struct {
	ProxyID string             `json:"proxy_id"`
	Secret  string             `json:"secret"`
	Report  snmppoll.FDBReport `json:"report"`
}

// handleProxyFDB stores the forwarding-table snapshot of ONE device.
func (s *Server) handleProxyFDB(w http.ResponseWriter, r *http.Request) {
	var in proxyFDBPush
	var err = readJSON(w, r, &in, maxProxyFDBBody)
	if err != nil {
		failJSON(w, err, "; this proxy's forwarding-table snapshot is too large — "+
			"a device reports an unexpected number of learned MACs")
		return
	}
	var _, _, authErr = s.Store.AuthProxy(r.Context(), in.ProxyID, auth.HashToken(in.Secret))
	if authErr != nil {
		respondErr(w, http.StatusUnauthorized, "unknown or revoked proxy")
		return
	}
	if in.Report.DeviceID == "" {
		respondErr(w, http.StatusUnprocessableEntity,
			"snapshot without device identifier: nothing to locate")
		return
	}
	if !s.allowProxyFact(in.ProxyID, authorityPoll, in.Report.DeviceID) {
		respondErr(w, http.StatusForbidden,
			"this device is not entrusted to this proxy for SNMP collection")
		return
	}
	var d, devErr = s.Store.GetDevice(r.Context(), in.Report.DeviceID)
	if devErr != nil {
		fail(w, devErr)
		return
	}

	// An unserved snapshot writes NOTHING. Unlike LLDP there is no self row to
	// update: a device whose agent answers no bridge MIB simply never touches
	// the mirror, and the page's freshness column says how old the last real
	// read is. Emptying on silence would erase a table the device still holds.
	if !in.Report.Served {
		respond(w, http.StatusOK, map[string]any{"accepted": 0, "served": false})
		return
	}

	var sightings, dropped = fdbRowsFor(d, in.Report)
	if err = s.Store.ReplaceFDB(r.Context(), d.ID, d.SiteID, sightings); err != nil {
		slog.Warn("FDB snapshot not recorded", "device_id", d.ID,
			"proxy_id", in.ProxyID, "err", err.Error())
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var body = map[string]any{"accepted": len(sightings), "served": true}
	if dropped+in.Report.Dropped > 0 {
		// Two drop sources merged for the proxy's log: entries the PROXY could
		// not resolve to an interface name, and entries the CONTROLLER could
		// not resolve to a facade port. Both explain the same symptom — a
		// client that exists on the switch and not on the page.
		body["dropped_unresolved_port"] = dropped + in.Report.Dropped
	}
	respond(w, http.StatusOK, body)
}

// fdbRowsFor resolves a snapshot's device-spoken port names against the
// facade — the exact rule of lldpRowsFor, and deliberately the same helper
// (`resolve`): a port we cannot name is dropped and counted, never guessed.
//
// One resolution the facade cannot do rides beside it: AGGREGATES. A bridge
// learns MACs on the BOND, not on its members — the lab CRS305 reports 25
// entries on "bonding1", pve01 among them — and no faceplate names a bond.
// The device's own observed aggregates carry both spellings (LAGState.Raw is
// the device's, Name the standard one), so an entry on a known aggregate is
// placed on the LAG name instead of being dropped. Only OBSERVED aggregates
// resolve: inventing a LAG from an intent the device never confirmed would
// place clients on an aggregate that may not exist.
func fdbRowsFor(d model.Device, report snmppoll.FDBReport) ([]model.MACSighting, int) {
	var f = facadeOf(d)
	var lagOf = map[string]string{}
	var lags = observedLAGsOf(d)
	var l int
	for l = range lags {
		if lags[l].Raw != "" {
			lagOf[lags[l].Raw] = lags[l].Name
		}
		lagOf[lags[l].Name] = lags[l].Name
	}
	var out = make([]model.MACSighting, 0, len(report.Entries))
	var dropped int
	var i int
	for i = range report.Entries {
		var e = report.Entries[i]
		var name = f.resolve(e.Port)
		if name == "" {
			name = lagOf[e.Port]
		}
		if name == "" {
			dropped++
			continue
		}
		out = append(out, model.MACSighting{
			DeviceID: d.ID, SiteID: d.SiteID,
			Port: name, VLAN: e.VLAN, MAC: e.MAC,
		})
	}
	return out, dropped
}

package api

import (
	"log/slog"
	"net/http"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// The two LLDP neighborhood routes: proxy push and screen read.
//
// See docs/01-Architecture/Voisinage LLDP.md. The decisions — recognize a neighbor,
// conclude that a link is seen from both sides — live in lldp.go, pure and tested; this
// file only handles rights, scope, facade resolution, and storage.

// maxProxyLLDPBody bounds the push body size.
//
// ONE device per request, not a whole fleet batch: the proxy pushes one snapshot per target
// and per cycle (cf. cmd/proxy/lldp.go). Sizing therefore reads from the worst plausible
// device, not the worst fleet:
//
//	52 ports × ~4 neighbors per port     = 208 observations
//	one observation, with its sysDescr   ≈ 400 B of JSON (measured: the 1930 sysDescr
//	                                     is 92 characters, the CRS326 71)
//	                                   ≈ 83 KiB, plus 52 local port identifiers
//
// "Four neighbors per port" is not a comfort margin: it is the MEASURED case of the
// CRS326, whose single physical port sees three — because an intermediate switch that
// does not consume LLDP floods frames instead of absorbing them.
// An entire site behind a transparent switch can therefore land on one port.
//
// 1 MiB leaves a factor of twelve above this calculation, and remains the product's
// generic ceiling (`maxBody`): there was no reason to invent another for a route whose
// body is twelve times smaller. The contrast with the 8 MiB of `/api/proxy/metrics` is
// deliberate and reads in the two constants — there it is 20,000 samples from an entire
// fleet, here it is one device's table.
const maxProxyLLDPBody = 1 << 20

// lldpGeste: the recovery to announce when the body was refused. Like `batchGeste`, it
// names the sender and its setting, because on this route there is no human to read the
// message — it will end up in a proxy log, and must be sufficient there.
const lldpGeste = " ; this proxy's neighborhood snapshot is too large, which" +
	" is not a content problem — a device sees an unexpected number of" +
	" neighbors, probably through a switch that floods LLDP frames"

// proxyLLDPPush is the body expected on /api/proxy/lldp.
type proxyLLDPPush struct {
	ProxyID string              `json:"proxy_id"`
	Secret  string              `json:"secret"`
	Report  snmppoll.LLDPReport `json:"report"`
}

// handleProxyLLDP stores the neighborhood snapshot of ONE device.
//
// # Scope control, before any write
//
// This route WRITES INVENTORY: what it stores ends up drawn on a weathermap and shown
// on port hover. The snapshot's `DeviceID` is chosen by the PROXY, exactly like a tunnel
// event's `DeviceID` or a sample's `device` label — and without verification, a
// compromised proxy could draw an imaginary cable between two devices on a site it does
// not cover. A false cabling plan costs more than a false curve: someone gets sent to
// unplug the wrong port.
//
// The required authority is SNMP COLLECTION (`authorityPoll`) and not ping, for the same
// reason as `applyPortTelemetry`: these observations come from polling, and only a
// device's declared collector interrogates it. A proxy that pings the device without
// being its collector has no way to read the LLDP-MIB.
//
// # Why refusal is 403 and not a silent 200
//
// `/api/proxy/metrics` responds 200 while dropping out-of-scope samples, and that is
// right there: a BATCH mixes several devices, and refusing the whole thing would lose
// legitimate measurements then replay the same batch indefinitely. Here, one request = ONE
// device: there is nothing legitimate to salvage in a refused snapshot, and the proxy has
// no buffer to unblock (a snapshot is not replayed, it is remade). Frank refusal is
// therefore free, and it has value of its own — it appears in the PROXY log, which is
// the misconfigured machine.
func (s *Server) handleProxyLLDP(w http.ResponseWriter, r *http.Request) {
	var in proxyLLDPPush
	var err = readJSON(w, r, &in, maxProxyLLDPBody)
	if err != nil {
		failJSON(w, err, lldpGeste)
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
	// THE SCOPE FILTER, before reading the device itself: a proxy talking about a device
	// not entrusted to it does not even need a database lookup.
	if !s.allowProxyFact(in.ProxyID, authorityPoll, in.Report.DeviceID) {
		// The exact reason is already in the controller log (cf. proxyScopes.refuse, which
		// distinguishes three verdicts). What goes to the proxy is shorter, and that is
		// deliberate: scope decision detail would inform a hostile proxy about the fleet
		// it has no right to describe.
		respondErr(w, http.StatusForbidden,
			"this device is not entrusted to this proxy for SNMP collection")
		return
	}
	var d, devErr = s.Store.GetDevice(r.Context(), in.Report.DeviceID)
	if devErr != nil {
		fail(w, devErr)
		return
	}

	var self, neighbors, dropped = lldpRowsFor(d, in.Report)
	if err = s.Store.ReplaceLLDP(r.Context(), self, neighbors); err != nil {
		// An error reaches the proxy, as on the metrics path — but with no replay
		// consequence: it kept nothing, and the next cycle will remake the snapshot.
		// The line exists so failure is not silent on BOTH sides.
		slog.Warn("LLDP neighborhood not recorded", "device_id", d.ID,
			"proxy_id", in.ProxyID, "err", err.Error())
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// No site notification, and that is a choice. Neighborhood changes at the scale of an
	// unplugged cable; the screens that display it (the plan, the devices page) already
	// have periodic refresh from which they draw their other data. Waking every browser on
	// a site at each snapshot — one per device every thirty seconds, i.e. twice per
	// minute per switch — would make them reread the entire facade to redraw exactly the
	// same image, which port telemetry precisely learned not to do (cf. portLinksChanged).
	var body = map[string]any{"accepted": len(neighbors), "served": in.Report.Served}
	if dropped > 0 {
		// The count goes to the PROXY: it read a port our facade does not know, and it is
		// the only number that explains why an expected link does not appear. Hiding it
		// would send someone looking for a collection defect where there is a naming
		// disagreement.
		body["dropped_unresolved_port"] = dropped
	}
	respond(w, http.StatusOK, body)
}

// lldpRowsFor translates a proxy snapshot into database rows, and RESOLVES local ports
// in the device's facade.
//
// # Why resolution is here and not in the proxy
//
// Because the proxy does not know the facade: `port_groups` is inventory, and teaching
// it to read that would give it a fleet view it does not need — the same rule as for
// `MaxIfIndex`, bounded by the controller. The proxy therefore returns what the DEVICE
// said ("sfp-sfpplus2", "50") and the controller names it.
//
// # A port we cannot name is DROPPED
//
// The same rule as telemetry (`applyPortTelemetry`) and for the same reason, worse here:
// there an unresolved label lost a measurement point, here it would draw a cable on a
// port that does not exist. `resolve` is reused as-is — it is the product's only way to
// match an interface label to a facade port, and it covers the three forms seen on the
// fleet: exact match ("sfp-sfpplus2"), normalized match ("SFP2" for "SFP2"), prefix +
// label ("50" → "GE50").
//
// Returns the drop count, so refusal is COUNTED instead of disappearing.
func lldpRowsFor(d model.Device, report snmppoll.LLDPReport) (model.LLDPSelf,
	[]model.LLDPNeighbor, int) {
	var self = model.LLDPSelf{
		DeviceID: d.ID, SiteID: d.SiteID,
		ChassisID: report.ChassisID, SysName: report.SysName,
		PortIDs: report.PortIDs, Served: report.Served,
	}
	if self.PortIDs == nil {
		// A NULL array and an empty array store differently in a `text[] not null`
		// column: normalize here rather than letting the driver decide.
		self.PortIDs = []string{}
	}
	var f = facadeOf(d)
	var out = make([]model.LLDPNeighbor, 0, len(report.Neighbors))
	var dropped int
	var i int
	for i = range report.Neighbors {
		var n = report.Neighbors[i]
		var name = f.resolve(n.LocalPort)
		if name == "" {
			dropped++
			continue
		}
		out = append(out, model.LLDPNeighbor{
			DeviceID: d.ID, SiteID: d.SiteID,
			LocalPort: name, LocalPortNum: n.LocalPortNum,
			ChassisID: n.ChassisID, PortID: n.PortID, PortDesc: n.PortDesc,
			SysName: n.SysName, SysDesc: n.SysDesc,
		})
	}
	return self, out, dropped
}

// handleListSiteLLDP renders a site's neighborhood: observations, derived links, and
// what we do not know.
//
// `viewer`, like `/links`: an observed cabling plan is no more sensitive than a entered
// one, and it is the same page that shows both. Nothing here reveals a secret — no
// community, no address outside the site.
//
// The read is THREE queries and derivation in memory, rather than a SQL join that would
// return links directly. That is the repository rule and it has a precise reason here:
// reciprocity of two observations and matching a chassis are exactly the kind of decision
// that must be testable without a database (cf. lldp.go), and a
// `left join lldp_neighbors on ...` would have locked it in a SQL chain nobody can test
// by hand.
func (s *Server) handleListSiteLLDP(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var neighbors, err = s.Store.ListLLDPNeighbors(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var selves []model.LLDPSelf
	if selves, err = s.Store.ListLLDPSelf(r.Context(), siteID); err != nil {
		fail(w, err)
		return
	}
	var devices []model.Device
	if devices, err = s.Store.ListDevices(r.Context(), siteID); err != nil {
		fail(w, err)
		return
	}
	var view = buildLLDPView(neighbors, selves, devices)
	respond(w, http.StatusOK, map[string]any{
		"neighbors": view.Neighbors,
		"links":     view.Links,
		"silent":    view.Silent,
		// The legend goes WITH the data. The screen must be able to explain "zero
		// neighbors" without a developer recopying the nuance into a component, where it
		// would diverge from the backend at the first rewording.
		"silent_help": lldpSilenceReasonHelp,
		"counts": map[string]int{
			"neighbors": len(view.Neighbors),
			"links":     len(view.Links),
			"silent":    len(view.Silent),
		},
	})
}

package api

import (
	"encoding/json"
	"net/http"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// Link-aggregation management: one write route, and the two JSON shelves the
// screen already receives.
//
// # Where a LAG lives
//
// OBSERVED aggregates ride `devices.observed.lags` — they are part of
// [driver.State] since the connectors learned to read them, so read-state
// lands them with the VLANs and ports, no new plumbing. The INTENT rides
// `devices.extra.lags`, on the same shelf as stp_super_root and for its
// exact reason (stated on Store.SaveSTPSuperRoot): Extra is outside the
// conformity diff, and an aggregate intent must not read as port drift while
// the LAG conformity story is not written. The screen gets both shelves in
// the device payload it already loads and merges them itself.
//
// # The one route is SAVE-AND-APPLY
//
// POST /api/devices/{id}/lags takes the COMPLETE desired list, applies it on
// the device, and persists it as intent only when the device accepted — an
// intent the device refused would render as configuration that exists
// nowhere. Complete-list semantics come from the driver contract (replace;
// an aggregate missing from the list is dissolved), which is why the UI
// always sends everything it knows.

// lagApplyBody is the request.
type lagApplyBody struct {
	LAGs []driver.LAGState `json:"lags"`
}

// handleApplyDeviceLAGs validates, applies, persists.
func (s *Server) handleApplyDeviceLAGs(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in lagApplyBody
	if !decode(w, r, &in) {
		return
	}
	if in.LAGs == nil {
		in.LAGs = []driver.LAGState{}
	}
	driver.SortLAGs(in.LAGs)
	var err = driver.ValidateLAGs(in.LAGs)
	if err != nil {
		respondErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// Members must be faceplate ports of THIS device: an aggregate of a port
	// the facade does not know would be written blind and displayed nowhere.
	var f = facadeOf(d)
	var i int
	for i = range in.LAGs {
		var m string
		for _, m = range in.LAGs[i].Members {
			if f.resolve(m) == "" {
				respondErr(w, http.StatusUnprocessableEntity,
					"aggregate "+in.LAGs[i].Name+": port \""+m+
						"\" is not on this device's faceplate")
				return
			}
		}
	}

	// Platform bounds, judged BEFORE a session is spent (the storm-control
	// rule: several fleet platforms bill every session).
	var conn, connErr = s.connectorFor(r.Context(), d)
	if connErr == nil && conn != nil {
		var caps = conn.Capabilities(d.Model)
		if !caps.Features["LACP"] {
			respondErr(w, http.StatusUnprocessableEntity,
				"this platform declares no link aggregation")
			return
		}
		// LACP fallback is a GESTURE, and an intent carrying it for a
		// connector that does not declare it is refused HERE, explicitly.
		//
		// Not trimmed, and not passed through to be ignored: the screen only
		// offers the control where the gesture is declared, so a Fallback
		// arriving without it means the two sides disagree — a stale tab, a
		// script, a connector downgraded under a fielded intent. Writing the
		// aggregate without the fallback would store an intent that differs
		// from what the operator read, and leave them believing a member
		// forwards while LACP is down, which is the one thing fallback exists
		// to guarantee.
		var fb, wantsFB = driver.LAGFallbackWanted(in.LAGs)
		if wantsFB && !driver.Has(conn, driver.GestureLAGFallback) {
			respondErr(w, http.StatusUnprocessableEntity,
				"aggregate "+fb.Name+": this connector does not write LACP "+
					"fallback — nothing was written. The aggregate was refused "+
					"whole rather than created without the fallback VLAN it "+
					"carries, which would forward nothing while LACP is down")
			return
		}
		if caps.MaxLAGs > 0 && len(in.LAGs) > caps.MaxLAGs {
			respondErr(w, http.StatusUnprocessableEntity,
				"this model accepts at most "+itoa(caps.MaxLAGs)+" aggregates")
			return
		}
		for i = range in.LAGs {
			if caps.PortsPerLAG > 0 && len(in.LAGs[i].Members) > caps.PortsPerLAG {
				respondErr(w, http.StatusUnprocessableEntity,
					"aggregate "+in.LAGs[i].Name+": this model accepts at most "+
						itoa(caps.PortsPerLAG)+" ports per aggregate")
				return
			}
		}
	}

	var _, adm, _, admErr = s.adminFor(r.Context(), d)
	if adm != nil {
		defer adm.Close()
	}
	if admErr != nil {
		respondErr(w, http.StatusBadGateway,
			"no session on the device: "+admErr.Error())
		return
	}
	var writer, canWrite = driver.AsLAGWriter(adm)
	if !canWrite {
		respondErr(w, http.StatusUnprocessableEntity,
			"this connector does not write link aggregation yet — the platform "+
				"may support it, but no write has been proven on hardware "+
				"(the house rule: an unproven write is not offered)")
		return
	}
	var res driver.ApplyResult
	if res, err = writer.ApplyLAGs(r.Context(), in.LAGs); err != nil {
		respond(w, http.StatusBadGateway, map[string]any{
			"error": err.Error(), "result": res, "transcript": transcriptOf(adm),
		})
		return
	}

	// The device accepted: the list becomes the stored intent.
	var raw, _ = json.Marshal(in.LAGs)
	if err = s.Store.SaveLAGIntent(r.Context(), d.ID, raw); err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": true, "result": res,
			"warning": "applied on the device but the intent was not saved: " + err.Error(),
		})
		return
	}
	s.emitDeviceEvent(r, d, "lag.applied", model.SevNotice,
		"Link aggregation written on \""+d.Name+"\": "+itoa(len(in.LAGs))+
			" aggregate(s)")
	respond(w, http.StatusOK, map[string]any{"ok": true, "result": res})
}

// observedLAGsOf reads the aggregates from a device's observed state; nil when
// never read or the connector does not report them.
func observedLAGsOf(d model.Device) []driver.LAGState {
	if len(d.Observed) == 0 {
		return nil
	}
	var view struct {
		LAGs []driver.LAGState `json:"lags"`
	}
	if json.Unmarshal(d.Observed, &view) != nil {
		return nil
	}
	return view.LAGs
}

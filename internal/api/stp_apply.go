package api

import (
	"context"
	"fmt"
	"net/http"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// applyDeviceSTP resolves the site STP preference against the connector and
// applies it when the Admin implements SpanningTreeApplier and WritesExtra("stp").
//
// On success, spanning-tree observation is re-read and stored under Extra so the
// topology view reflects what was pushed (not the pre-apply snapshot).
func (s *Server) applyDeviceSTP(ctx context.Context, d model.Device,
	conn driver.Connector, adm driver.Admin) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !conn.WritesExtra("stp") {
		return out, nil
	}
	var sitePlan, err = s.Store.GetSitePlan(ctx, d.SiteID)
	if err != nil {
		return out, err
	}
	var pref = stpPreferenceOf(sitePlan)
	var caps = conn.Capabilities(d.Model).STPModes
	var mode string
	var disable bool
	var ok bool
	mode, disable, ok = driver.ResolveSTPMode(pref, caps)
	if !ok {
		out.Notes = append(out.Notes,
			fmt.Sprintf("no preferred STP mode matches this platform (preference %v, caps %v) — left unchanged",
				pref, caps))
		return out, nil
	}
	return s.pushDeviceSTP(ctx, d, conn, adm, mode, disable)
}

// pushDeviceSTP applies an explicit mode (or disable) and refreshes stp_observed.
func (s *Server) pushDeviceSTP(ctx context.Context, d model.Device,
	conn driver.Connector, adm driver.Admin, mode string, disable bool) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !conn.WritesExtra("stp") {
		out.Notes = append(out.Notes, "this platform does not declare STP as writable")
		return out, nil
	}
	var applier, ok = driver.AsSpanningTreeApplier(adm)
	if !ok {
		out.Notes = append(out.Notes,
			"connector declares STP writable but Admin has no ApplySpanningTree")
		return out, nil
	}

	var plan = driver.SpanningTreePlan{
		Enabled: !disable,
		Mode:    mode,
		Ports:   portSTPIntents(d.Ports),
	}
	var err error
	out, err = applier.ApplySpanningTree(ctx, plan)
	if err != nil {
		return out, err
	}
	// No mandatory « mode pushed » note — success is silent; speak only on drift.

	var st, note = s.readSpanningTree(ctx, d.ID, adm)
	if note != "" {
		out.Notes = append(out.Notes, note)
	} else if !disable && st.Mode != "" && st.Mode != mode {
		out.Notes = append(out.Notes,
			fmt.Sprintf("after push, device reports mode « %s » (requested « %s »)",
				st.Mode, mode))
	} else if disable && st.Enabled {
		out.Notes = append(out.Notes,
			"after push, device still reports spanning tree enabled — check platform mode mapping")
	}
	_ = st
	return out, nil
}

// handleApplyDeviceSTP pushes a spanning-tree mode onto one device.
//
// Body: `{ "mode": "rstp" | "stp" | "mstp" | "none" }`. Mode must be in the
// connector's STPModes (or « none » when listed). Same privilege as other device
// writes (site_admin).
func (s *Server) handleApplyDeviceSTP(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	var mode = driver.NormalizeSTPMode(in.Mode)
	if mode == "" {
		respondErr(w, http.StatusBadRequest,
			"mode required: stp, rstp, mstp, or none")
		return
	}

	var conn, connErr = s.connectorFor(r.Context(), d)
	if connErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, connErr.Error())
		return
	}
	if !conn.WritesExtra("stp") {
		respondErr(w, http.StatusUnprocessableEntity,
			"this platform does not declare STP as writable")
		return
	}
	var caps = conn.Capabilities(d.Model).STPModes
	var allowed bool
	var c string
	for _, c = range caps {
		if c == mode {
			allowed = true
			break
		}
	}
	if !allowed {
		respondErr(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("mode « %s » is not offered by this platform (caps %v)", mode, caps))
		return
	}

	var _, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var disable = mode == driver.STPPrefNone
	var applyMode = mode
	if disable {
		applyMode = "rstp" // ignored when Enabled=false; keep a valid token
	}
	var res driver.ApplyResult
	res, err = s.pushDeviceSTP(r.Context(), d, conn, adm, applyMode, disable)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.emitDeviceEvent(r, d, "device.stp_apply", model.SevInfo,
		"Spanning-tree mode « "+mode+" » applied on « "+d.Name+" »")
	respond(w, http.StatusOK, map[string]any{
		"ok":    true,
		"mode":  mode,
		"notes": res.Notes,
		"steps": transcriptOf(adm),
	})
}

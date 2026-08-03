package api

import (
	"context"
	"encoding/json"
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
		Region:  s.siteMSTRegion(ctx, d),
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

// siteMSTRegion derives the MST region every device of this site must share.
//
// # Why it is computed here and never entered anywhere
//
// 802.1s decides region membership on name + revision + a digest over the FULL
// VLAN-to-instance table, and two bridges are in one region only if all three
// match exactly. A mismatch is silent — the neighbour is simply treated as an
// ordinary RSTP bridge — so the usual way of running MST, with an operator typing
// the same three values on every switch, fails by typo and shows nothing.
//
// Deriving it removes the typo: the name is the site slug, and the map is built
// from the site's own VLAN list, so every device of a site computes the same
// region from the same data without anyone entering anything.
//
// ALL KNOWN VLANs GO INTO INSTANCE 1 (VLAN 1 excepted — it belongs to the CIST on
// every platform, and mapping it is refused by some and ignored by others, which
// is enough to break the digest). One instance means one tree for the site: no
// per-VLAN load balancing, which needs redundant paths to be worth anything and
// must be identical everywhere to be safe at all.
//
// THE COROLLARY THE CALLER MUST CARRY: the digest covers every VLAN, so adding a
// site VLAN changes the region. Any device not re-pushed afterwards leaves the
// region — still forwarding, still green, quietly out of the tree. Changing the
// VLAN list of a site means re-applying STP across it.
func (s *Server) siteMSTRegion(ctx context.Context, d model.Device) driver.MSTRegion {
	var site, err = s.Store.GetSite(ctx, d.SiteID)
	if err != nil {
		return driver.MSTRegion{}
	}
	var slug = site.Slug
	if slug == "" {
		// A region needs a stable name and the slug is the only one guaranteed
		// unique per site. Without it, deriving from the site name would produce
		// a region that changes the day someone renames the site — which silently
		// evicts every device not re-pushed.
		return driver.MSTRegion{}
	}
	var vlans, vlanErr = s.Store.ListVlans(ctx, d.SiteID)
	if vlanErr != nil {
		return driver.MSTRegion{}
	}
	var ids = make([]int, 0, len(vlans))
	var v model.Vlan
	for _, v = range vlans {
		ids = append(ids, v.VlanID)
	}
	// A plan read that fails is tolerated: a region without an explicit revision
	// is still a region, and refusing to build one for a transient database error
	// would leave the fleet unregioned.
	var plan, _ = s.Store.GetSitePlan(ctx, d.SiteID)
	return driver.SiteMSTRegion(slug, stpRegionRevisionOf(plan), ids)
}

// stpRegionRevisionOf reads the region revision from the site plan.
//
// A revision is NOT a version to bump for bookkeeping: raising it on one device
// removes that device from the region until every other is re-pushed. It lives in
// the site plan so a deliberate region change is possible, and defaults to 1 —
// not 0, which is what an untouched device carries, so a meshcraft-managed region
// is distinguishable from a factory one at a glance.
func stpRegionRevisionOf(plan json.RawMessage) int {
	if len(plan) == 0 {
		return 1
	}
	var parsed struct {
		Rev *int `json:"stp_mst_revision"`
	}
	if json.Unmarshal(plan, &parsed) != nil || parsed.Rev == nil {
		return 1
	}
	return *parsed.Rev
}

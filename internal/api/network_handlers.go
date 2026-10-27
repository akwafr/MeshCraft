package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

// requireSite verifies the minimum role on the URL's site.
func (s *Server) requireSite(w http.ResponseWriter, r *http.Request, minRole string) (string, bool) {
	var siteID = chi.URLParam(r, "siteID")
	// AN EMPTY SITE ID IS REFUSED BEFORE THE ROLE IS CONSULTED.
	//
	// `CanSite` answers true for a GLOBAL ADMIN whatever site it is asked about,
	// including "". So a URL like `/api/sites//serial-servers` — which a frontend
	// produces the moment it interpolates a site id it does not have — passed
	// this check, and the handler ran with an empty site.
	//
	// That is not a theoretical shape. It shipped: the serial-line panel was
	// mounted on a page with no site in its route, every request went to
	// `/api/sites//…`, the offer was registered against site "" and could never
	// match any list. The only visible symptom was a feature that did nothing,
	// and the only trace was an event insert failing on an empty uuid.
	//
	// Refusing here fixes it for every site-scoped route at once, which is the
	// right altitude: any of them would have behaved the same way, and the next
	// one to interpolate an empty id would have had to rediscover this.
	if siteID == "" {
		respondErr(w, http.StatusBadRequest,
			"no site in the request path: this endpoint is site-scoped and the caller "+
				"interpolated an empty site id")
		return "", false
	}
	if !s.Auth.CanSite(r.Context(), siteID, minRole) {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return "", false
	}
	return siteID, true
}

// --- VLANs ---

type vlanInput struct {
	VlanID  int    `json:"vlan_id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Purpose string `json:"purpose"`
}

func (in *vlanInput) valid(w http.ResponseWriter) bool {
	if in.VlanID < 1 || in.VlanID > 4094 || in.Name == "" {
		respondErr(w, http.StatusBadRequest, "vlan_id (1-4094) and name required")
		return false
	}
	if in.Color == "" {
		in.Color = "#7c8cf8"
	}
	return true
}

func (s *Server) handleListVlans(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var vlans, err = s.Store.ListVlans(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, vlans)
}

func (s *Server) handleCreateVlan(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in vlanInput
	if !decode(w, r, &in) || !in.valid(w) {
		return
	}
	var v, err = s.Store.CreateVlan(r.Context(), siteID, in.VlanID, in.Name, in.Color, in.Purpose)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "vlan.created", model.SevNotice,
		"VLAN "+strconv.Itoa(v.VlanID)+" \""+v.Name+"\" created", v)
	respond(w, http.StatusCreated, v)
}

func (s *Server) handleUpdateVlan(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "id")
	var cur, getErr = s.Store.GetVlan(r.Context(), id)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	if !s.Auth.CanSite(r.Context(), cur.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	var in vlanInput
	if !decode(w, r, &in) || !in.valid(w) {
		return
	}
	// Renaming and recoloring VLAN 1 is free; renumbering it would amount to
	// deleting it by another path.
	if cur.VlanID == recon.DefaultVLAN && in.VlanID != recon.DefaultVLAN {
		respondErr(w, http.StatusConflict,
			"VLAN 1 is the site's default VLAN: its ID cannot change")
		return
	}
	var v, err = s.Store.UpdateVlan(r.Context(), id, in.VlanID, in.Name, in.Color, in.Purpose)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, cur.SiteID, "vlan.updated", model.SevInfo,
		"VLAN "+strconv.Itoa(v.VlanID)+" \""+v.Name+"\" updated", v)
	respond(w, http.StatusOK, v)
}

func (s *Server) handleDeleteVlan(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "id")
	var cur, getErr = s.Store.GetVlan(r.Context(), id)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	if !s.Auth.CanSite(r.Context(), cur.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	// VLAN 1 is structural: devices have it anyway, and it is the implicit
	// native VLAN on trunks. Deleting it from the site would remove it nowhere —
	// it would only make port tagging unreadable.
	if cur.VlanID == recon.DefaultVLAN {
		respondErr(w, http.StatusConflict,
			"VLAN 1 is the site's default VLAN: it cannot be deleted")
		return
	}
	var err = s.Store.DeleteVlan(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, cur.SiteID, "vlan.deleted", model.SevWarning,
		"VLAN "+strconv.Itoa(cur.VlanID)+" \""+cur.Name+"\" deleted", nil)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Profiles (kind = "port" | "device") ---

type profileInput struct {
	Name string          `json:"name"`
	Spec json.RawMessage `json:"spec"`
	Tag  string          `json:"tag"`
}

func (in *profileInput) valid(w http.ResponseWriter) bool {
	if in.Name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return false
	}
	if len(in.Spec) == 0 {
		in.Spec = json.RawMessage(`{}`)
	}
	if !json.Valid(in.Spec) {
		respondErr(w, http.StatusBadRequest, "spec: invalid JSON")
		return false
	}
	return true
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var profiles, err = s.Store.ListProfiles(r.Context(), chi.URLParam(r, "kind"), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, profiles)
}

func (s *Server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in profileInput
	if !decode(w, r, &in) || !in.valid(w) {
		return
	}
	var kind = chi.URLParam(r, "kind")
	var p, err = s.Store.CreateProfile(r.Context(), kind, siteID, in.Name, in.Spec, in.Tag)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "profile.created", model.SevNotice,
		"Profile "+kind+" \""+p.Name+"\" created", p.Spec)
	respond(w, http.StatusCreated, p)
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var kind = chi.URLParam(r, "kind")
	var id = chi.URLParam(r, "id")
	var cur, getErr = s.Store.GetProfile(r.Context(), kind, id)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	if !s.Auth.CanSite(r.Context(), cur.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	var in profileInput
	if !decode(w, r, &in) || !in.valid(w) {
		return
	}
	var p, err = s.Store.UpdateProfile(r.Context(), kind, id, in.Name, in.Spec, in.Tag)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, cur.SiteID, "profile.updated", model.SevInfo,
		"Profile "+kind+" \""+p.Name+"\" updated", p.Spec)
	respond(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	var kind = chi.URLParam(r, "kind")
	var id = chi.URLParam(r, "id")
	var cur, getErr = s.Store.GetProfile(r.Context(), kind, id)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	if !s.Auth.CanSite(r.Context(), cur.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	var err = s.Store.DeleteProfile(r.Context(), kind, id)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, cur.SiteID, "profile.deleted", model.SevWarning,
		"Profile "+kind+" \""+cur.Name+"\" deleted", nil)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Groups ---

type groupInput struct {
	Name            string  `json:"name"`
	DeviceProfileID *string `json:"device_profile_id"`
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var groups, err = s.Store.ListGroups(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, groups)
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in groupInput
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return
	}
	var g, err = s.Store.CreateGroup(r.Context(), siteID, in.Name, in.DeviceProfileID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "group.created", model.SevNotice,
		"Device group \""+g.Name+"\" created", nil)
	respond(w, http.StatusCreated, g)
}

func (s *Server) handleUpdateGroup(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "id")
	var cur, getErr = s.Store.GetGroup(r.Context(), id)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	if !s.Auth.CanSite(r.Context(), cur.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	var in groupInput
	if !decode(w, r, &in) {
		return
	}
	var g, err = s.Store.UpdateGroup(r.Context(), id, in.Name, in.DeviceProfileID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, g)
}

func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "id")
	var cur, getErr = s.Store.GetGroup(r.Context(), id)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	if !s.Auth.CanSite(r.Context(), cur.SiteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	var err = s.Store.DeleteGroup(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, cur.SiteID, "group.deleted", model.SevWarning,
		"Device group \""+cur.Name+"\" deleted", nil)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

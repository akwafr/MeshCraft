package api

import (
	"encoding/json"
	"net/http"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

type stpPreferenceReply struct {
	Catalog    []string `json:"catalog"`
	Preference []string `json:"preference"`
}

func (s *Server) handleGetSTPPreference(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var plan, err = s.Store.GetSitePlan(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, stpPreferenceReply{
		Catalog:    append([]string{}, driver.STPPreferenceCatalog...),
		Preference: stpPreferenceOf(plan),
	})
}

func (s *Server) handlePutSTPPreference(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var body struct {
		Preference []string `json:"preference"`
	}
	if !decode(w, r, &body) {
		return
	}
	var plan, err = s.Store.GetSitePlan(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var merged json.RawMessage
	merged, err = mergeSTPPreference(plan, body.Preference)
	if err != nil {
		fail(w, err)
		return
	}
	if err = s.Store.UpdateSitePlan(r.Context(), siteID, merged); err != nil {
		fail(w, err)
		return
	}
	var pref = stpPreferenceOf(merged)
	s.emitSite(r, siteID, "stp.preference_updated", model.SevInfo,
		"Spanning-tree mode preference updated", map[string]any{
			"preference": pref,
		})
	respond(w, http.StatusOK, stpPreferenceReply{
		Catalog:    append([]string{}, driver.STPPreferenceCatalog...),
		Preference: pref,
	})
}

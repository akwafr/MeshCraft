package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
	"meshcraft/internal/tagging"
)

// Device tags and automatic tagging.
// See docs/01-Architecture/Tags and identifier scope.
//
// A tag can decide which admin password a device receives. It is therefore not
// decorative labeling, and writes require `site_admin`, like credentials themselves.

// normalizeTag formats a tag. Case and leading/trailing spaces are the first cause
// of duplicates in this kind of free-text field: without normalization, a fleet ends
// up with "regie", "Regie", and "regie  ", meaning the same thing but never matching.
func normalizeTag(tag string) string {
	return strings.ToLower(strings.Join(strings.Fields(tag), " "))
}

func (s *Server) handleListDeviceTags(w http.ResponseWriter, r *http.Request) {
	// Reading tags reveals no secrets: viewer role suffices.
	var d, ok = s.deviceForWrite(w, r, "viewer")
	if !ok {
		return
	}
	var tags, err = s.Store.DeviceTags(r.Context(), d.ID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"tags": tags})
}

// handleSetDeviceTag assigns a tag MANUALLY. A manual tag is never removed by rule
// re-evaluation: that lets the operator handle a special case without a rule taking it back.
func (s *Server) handleSetDeviceTag(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var raw, decErr = url.PathUnescape(chi.URLParam(r, "tag"))
	if decErr != nil {
		respondErr(w, http.StatusBadRequest, "unreadable tag")
		return
	}
	var tag = normalizeTag(raw)
	if tag == "" {
		respondErr(w, http.StatusBadRequest, "empty tag")
		return
	}
	var err = s.Store.SetDeviceTag(r.Context(), d.ID, tag, tagging.SourceManual)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitDeviceEvent(r, d, "tag.assigned", model.SevInfo,
		"Tag « "+tag+" » assigned to « "+d.Name+" »")
	var out = map[string]any{"tag": tag, "source": tagging.SourceManual}
	var apply = s.applySwitchProfileByTag(r.Context(), d, tag)
	if apply != nil {
		out["switch_profile_apply"] = apply
	}
	respond(w, http.StatusOK, out)
}

func (s *Server) handleDeleteDeviceTag(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var raw, decErr = url.PathUnescape(chi.URLParam(r, "tag"))
	if decErr != nil {
		respondErr(w, http.StatusBadRequest, "unreadable tag")
		return
	}
	var tag = normalizeTag(raw)
	var err = s.Store.RemoveDeviceTag(r.Context(), d.ID, tag)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitDeviceEvent(r, d, "tag.removed", model.SevInfo,
		"Tag « "+tag+" » removed from « "+d.Name+" »")
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListSiteTags returns tags known on the site — for autocomplete rather
// than letting operators invent synonyms. Includes switch-profile enrollment
// tags, not only tags already applied to devices.
func (s *Server) handleListSiteTags(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var names, err = s.Store.SiteTagNames(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"tags": names})
}

// --- Rules ---

func (s *Server) handleListTagRules(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var rules, err = s.Store.ListTagRules(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]any{"rules": rules})
}

// handleUpsertTagRule creates or updates a rule.
//
// A rule with no criteria is REJECTED at input. It would match nothing (cf.
// tagging.Match.Matches), but the operator would believe they configured a rule:
// better to refuse than accept an inert setting.
func (s *Server) handleUpsertTagRule(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in store.TagRule
	if !decode(w, r, &in) {
		return
	}
	in.SiteID = siteID
	in.Tag = normalizeTag(in.Tag)
	if in.Tag == "" {
		respondErr(w, http.StatusBadRequest, "a rule must assign a tag")
		return
	}
	var problems = in.Match.Problems()
	if len(problems) > 0 {
		respondErr(w, http.StatusUnprocessableEntity, strings.Join(problems, " ; "))
		return
	}
	// Modifying another site's rule by guessing its ID would tag a fleet you do not
	// administer.
	if in.ID != "" {
		var existing, getErr = s.Store.GetTagRule(r.Context(), in.ID)
		if getErr != nil {
			fail(w, getErr)
			return
		}
		if existing.SiteID != siteID {
			respondErr(w, http.StatusNotFound, "not found")
			return
		}
	}
	var me = auth.UserFrom(r.Context())
	var saved, err = s.Store.UpsertTagRule(r.Context(), in, me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "tag.rule_changed", model.SevNotice,
		"Tagging rule « "+saved.Tag+" » saved. Tags change only on re-evaluation.", nil)
	respond(w, http.StatusOK, saved)
}

func (s *Server) handleDeleteTagRule(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var existing, err = s.Store.GetTagRule(r.Context(), chi.URLParam(r, "ruleID"))
	if err != nil {
		fail(w, err)
		return
	}
	if existing.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	if err = s.Store.DeleteTagRule(r.Context(), existing.ID); err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "tag.rule_changed", model.SevNotice,
		"Tagging rule « "+existing.Tag+" » deleted, along with tags it had assigned.", nil)
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// deviceChange describes what re-evaluation would do — or just did — on a device.
type deviceChange struct {
	DeviceID string        `json:"device_id"`
	Name     string        `json:"name"`
	Add      []tagging.Tag `json:"add,omitempty"`
	Remove   []tagging.Tag `json:"remove,omitempty"`
}

// handleApplyTagRules replays rules across the whole site.
//
// `dry_run` (the DEFAULT) shows what would change without changing anything. A badly
// written rule can move dozens of devices from one credential to another: preview is
// not comfort, it is what makes the operation reversible in the operator's head before
// it is reversible in the database.
//
// Useful reminder: re-tagging changes NO password on the fleet. It changes the EXPECTED
// password; the device keeps its own until an explicit `credentials/apply`.
func (s *Server) handleApplyTagRules(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Apply bool `json:"apply"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}

	var changes, err = s.evaluateSiteTags(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var out = map[string]any{
		"applied": in.Apply,
		"changes": changes,
		"devices": len(changes),
	}
	if !in.Apply {
		out["note"] = "preview: nothing was modified"
		respond(w, http.StatusOK, out)
		return
	}

	var added, removed int
	var profileApplies []map[string]any
	var c deviceChange
	for _, c = range changes {
		if err = s.Store.ApplyTagPlan(r.Context(), c.DeviceID,
			tagging.Plan{Add: c.Add, Remove: c.Remove}); err != nil {
			fail(w, err)
			return
		}
		added += len(c.Add)
		removed += len(c.Remove)
		var d model.Device
		d, err = s.Store.GetDevice(r.Context(), c.DeviceID)
		if err != nil {
			continue
		}
		var tg tagging.Tag
		for _, tg = range c.Add {
			var apply = s.applySwitchProfileByTag(r.Context(), d, tg.Tag)
			if apply != nil {
				apply["device_id"] = d.ID
				apply["device_name"] = d.Name
				profileApplies = append(profileApplies, apply)
			}
		}
	}
	out["added"], out["removed"] = added, removed
	if len(profileApplies) > 0 {
		out["switch_profile_applies"] = profileApplies
	}
	s.emitSite(r, siteID, "tag.rules_applied", model.SevNotice,
		"Tagging replayed: "+itoa(added)+" tag(s) assigned, "+
			itoa(removed)+" removed, on "+itoa(len(changes))+" device(s). "+
			"No password was changed on the fleet.", nil)
	respond(w, http.StatusOK, out)
}

// evaluateSiteTags computes, without writing, what rules would change on each site
// device. Only devices actually affected are returned: a list of two hundred lines
// where three matter does not read.
func (s *Server) evaluateSiteTags(ctx context.Context, siteID string) ([]deviceChange, error) {
	var rules, err = s.Store.ListTagRules(ctx, siteID)
	if err != nil {
		return nil, err
	}
	var devices []model.Device
	if devices, err = s.Store.ListDevices(ctx, siteID); err != nil {
		return nil, err
	}
	var byDevice map[string][]tagging.Tag
	if byDevice, err = s.Store.TagsBySite(ctx, siteID); err != nil {
		return nil, err
	}

	var engine = make([]tagging.Rule, 0, len(rules))
	var i int
	for i = range rules {
		engine = append(engine, rules[i].Rule())
	}

	var out = []deviceChange{}
	var d model.Device
	for _, d = range devices {
		var plan = tagging.Evaluate(tagging.Device{
			Vendor:   d.Vendor,
			Model:    d.Model,
			Location: d.Location,
			MgmtIP:   d.MgmtIP,
		}, byDevice[d.ID], engine)
		if plan.Empty() {
			continue
		}
		out = append(out, deviceChange{
			DeviceID: d.ID, Name: d.Name, Add: plan.Add, Remove: plan.Remove,
		})
	}
	return out, nil
}

// autoTagDevice replays rules on ONE device, silently.
//
// Called when device identity arrives or changes (adoption, new model observed, new
// address): that is when information rules need appears. An error does not interrupt
// the caller — missing tags is annoyance, failing adoption for that would be a fault.
func (s *Server) autoTagDevice(ctx context.Context, d model.Device) {
	var rules, err = s.Store.ListTagRules(ctx, d.SiteID)
	if err != nil || len(rules) == 0 {
		return
	}
	var current []tagging.Tag
	if current, err = s.Store.DeviceTags(ctx, d.ID); err != nil {
		return
	}
	var engine = make([]tagging.Rule, 0, len(rules))
	var i int
	for i = range rules {
		engine = append(engine, rules[i].Rule())
	}
	var plan = tagging.Evaluate(tagging.Device{
		Vendor: d.Vendor, Model: d.Model, Location: d.Location, MgmtIP: d.MgmtIP,
	}, current, engine)
	if err = s.Store.ApplyTagPlan(ctx, d.ID, plan); err != nil {
		return
	}
	var tg tagging.Tag
	for _, tg = range plan.Add {
		_ = s.applySwitchProfileByTag(ctx, d, tg.Tag)
	}
}

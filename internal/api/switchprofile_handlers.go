package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"

	"meshcraft/internal/model"
	"meshcraft/internal/portrules"
	"meshcraft/internal/store"
)

// Switch profiles: evaluate a rule set on a REAL device facade, then apply it.
//
// Two entry points, one computation. Preview and apply call the same function
// (portrules.Evaluate) on the same inputs; that is the rule already applied to
// "all" in recon.Resolve, and for the same reason: two paths deciding on their
// own would eventually apply something other than what the screen showed, and the
// preview would be worthless.
//
// Evaluation lives on the SERVER, although the screen could have redone it in
// TypeScript to show a preview without a round trip. Same trade-off: a second
// implementation of the same arbitration would diverge, and it would diverge
// exactly where nobody looks — on a rare model's facade.

// switchProfileRequest designates the rules to evaluate.
//
// Either a saved profile or a raw spec. The raw spec is not a convenience: it
// lets the preview screen show a change BEFORE saving it. Without it, you would
// have to save to see, therefore save to discover you were wrong.
type switchProfileRequest struct {
	ProfileID string          `json:"profile_id"`
	Spec      json.RawMessage `json:"spec"`
}

// switchProfileResponse is what the screen displays.
type switchProfileResponse struct {
	Device string `json:"device"`
	// Facade: all device ports, in front-panel order, including those no rule touches.
	// The screen must draw the ENTIRE facade: showing only configured ports would
	// suggest the device has no others.
	Facade []portrules.Port `json:"facade"`
	Plan   portrules.Plan   `json:"plan"`
	// SiteVLANs: what "all" means on THIS site. Returned separately because evaluation
	// does not expand the wildcard (cf. portrules.Config) — yet that is the first
	// question facing an "all" trunk.
	SiteVLANs []int `json:"site_vlans"`
	// Applied: number of ports actually written. Zero on a preview.
	Applied int `json:"applied"`
}

// resolveSwitchSpec reads the requested rules, from wherever they come.
func (s *Server) resolveSwitchSpec(w http.ResponseWriter, r *http.Request,
	in switchProfileRequest, siteID string) (portrules.Spec, bool) {
	var raw = in.Spec
	if in.ProfileID != "" {
		var p, err = s.Store.GetProfile(r.Context(), "switch", in.ProfileID)
		if err != nil {
			fail(w, err)
			return portrules.Spec{}, false
		}
		// A profile from ANOTHER site does not apply here: profiles reference VLANs
		// and port profiles that belong to the site.
		if p.SiteID != siteID {
			respondErr(w, http.StatusNotFound, "this profile does not belong to the device's site")
			return portrules.Spec{}, false
		}
		raw = p.Spec
	}
	if len(raw) == 0 {
		respondErr(w, http.StatusBadRequest, "no rules: `profile_id` or `spec` is required")
		return portrules.Spec{}, false
	}
	var spec, err = portrules.ParseSpec(raw)
	if err != nil {
		respondErr(w, http.StatusBadRequest, "unreadable rules: "+err.Error())
		return portrules.Spec{}, false
	}
	return spec, true
}

// portProfileConfigs returns the site's port profiles, ready to be applied by a rule
// that references them.
//
// A port profile whose spec is unreadable is simply ABSENT from the map: the rule
// targeting it will be reported in fault by evaluation, which reads on screen — whereas
// a default configuration invented here would be applied silently on ports.
func (s *Server) portProfileConfigs(ctx context.Context, siteID string) (map[string]portrules.Config, error) {
	var profiles, err = s.Store.ListProfiles(ctx, "port", siteID)
	if err != nil {
		return nil, err
	}
	var out = map[string]portrules.Config{}
	var p model.Profile
	for _, p = range profiles {
		var c portrules.Config
		if json.Unmarshal(p.Spec, &c) != nil || c.Mode == "" {
			continue
		}
		out[p.ID] = c
	}
	return out, nil
}

// evaluateSwitchProfile performs the computation shared by preview and apply.
func (s *Server) evaluateSwitchProfile(w http.ResponseWriter, r *http.Request,
	d model.Device) (switchProfileResponse, portrules.Spec, bool) {
	var in switchProfileRequest
	if !decode(w, r, &in) {
		return switchProfileResponse{}, portrules.Spec{}, false
	}
	var spec, ok = s.resolveSwitchSpec(w, r, in, d.SiteID)
	if !ok {
		return switchProfileResponse{}, portrules.Spec{}, false
	}
	var catalogue, err = s.portProfileConfigs(r.Context(), d.SiteID)
	if err != nil {
		fail(w, err)
		return switchProfileResponse{}, portrules.Spec{}, false
	}
	var facade = portrules.Expand(d.PortGroups)
	var out = switchProfileResponse{
		Device:    d.Name,
		Facade:    facade,
		Plan:      portrules.Evaluate(spec, facade, catalogue),
		SiteVLANs: s.siteVLANIDs(r.Context(), d.SiteID),
	}
	return out, spec, true
}

// handlePreviewSwitchProfile shows what the rules WOULD do, without writing anything.
//
// Read-only, so open to `operator` like port configuration: seeing before applying must
// never be harder than applying.
func (s *Server) handlePreviewSwitchProfile(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var out, _, done = s.evaluateSwitchProfile(w, r, d)
	if !done {
		return
	}
	respond(w, http.StatusOK, out)
}

// handleApplySwitchProfile writes the intention produced by the rules.
//
// Writes to `devices.ports` only — not to the device. Hardware apply follows its usual
// path: `noteDesiredChanged` schedules it, batched with other changes in the same burst
// (cf. autoapply.go). Duplicating that path here would mean two applies for one gesture.
func (s *Server) handleApplySwitchProfile(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var out, spec, done = s.evaluateSwitchProfile(w, r, d)
	if !done {
		return
	}
	var applied, issues, err = s.applySwitchSpecToDevice(r.Context(), d, spec, "")
	if len(issues) > 0 {
		respondErr(w, http.StatusConflict, "profile cannot be applied — "+issues[0])
		return
	}
	if err != nil {
		if errors.Is(err, errNoPorts) || err.Error() == "facade too large" {
			respondErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(err.Error()) > 24 && err.Error()[:24] == "unreadable configuration" {
			respondErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		fail(w, err)
		return
	}
	out.Applied = applied
	respond(w, http.StatusOK, out)
}

var errNoPorts = errors.New("no rule targets a port on this device: nothing to apply")

// applySwitchSpecToDevice merges the evaluated plan into devices.ports and schedules
// hardware apply. Returns plan issues without writing when evaluation is blocked.
// Empty plan (no matching ports) returns errNoPorts; the tag is kept by the caller.
func (s *Server) applySwitchSpecToDevice(ctx context.Context, d model.Device,
	spec portrules.Spec, profileName string) (applied int, issues []string, err error) {
	var catalogue map[string]portrules.Config
	catalogue, err = s.portProfileConfigs(ctx, d.SiteID)
	if err != nil {
		return 0, nil, err
	}
	var facade = portrules.Expand(d.PortGroups)
	var plan = portrules.Evaluate(spec, facade, catalogue)
	if len(plan.Issues) > 0 {
		return 0, plan.Issues, nil
	}
	if len(plan.Ports) == 0 {
		return 0, nil, errNoPorts
	}
	if len(plan.Ports) > maxPortsPerBatch {
		return 0, nil, errors.New("facade too large")
	}

	var ports map[string]json.RawMessage
	ports, err = mergePortConfigs(d.Ports, plan.Ports)
	if err != nil {
		return 0, nil, errors.New("unreadable configuration: " + err.Error())
	}
	var updated model.Device
	updated, err = s.Store.SetDevicePorts(ctx, d.ID, ports)
	if err != nil {
		return 0, nil, err
	}
	s.noteDesiredChanged(ctx, &updated)

	var names = make([]string, 0, len(plan.Ports))
	var a portrules.Assignment
	for _, a = range plan.Ports {
		names = append(names, a.Port)
	}
	sort.Strings(names)
	var payload, _ = json.Marshal(map[string]any{
		"ports": names, "count": len(names), "uncovered": len(plan.Uncovered),
		"profile": profileName,
	})
	var msg = d.Name + " - switch profile applied: " + strconv.Itoa(len(names)) +
		" port(s) configured, " + strconv.Itoa(len(plan.Uncovered)) + " left unchanged"
	if profileName != "" {
		msg = d.Name + " - switch profile « " + profileName + " » applied: " +
			strconv.Itoa(len(names)) + " port(s) configured, " +
			strconv.Itoa(len(plan.Uncovered)) + " left unchanged"
	}
	var ev = model.Event{
		SiteID: &d.SiteID, DeviceID: &d.ID, Actor: "system",
		Type: "switch_profile.applied", Severity: model.SevNotice,
		Message: msg, Payload: payload,
	}
	var insertErr = s.Store.InsertEvent(ctx, ev)
	if insertErr != nil {
		slog.Error("event log: insert failed", "err", insertErr, "type", ev.Type)
	} else if ev.SiteID != nil {
		s.live.notify(*ev.SiteID, ChangeEvent, "")
	}
	return len(names), nil, nil
}

// applySwitchProfileByTag looks up a switch profile for the tag and applies it.
// Tag membership is already recorded; apply failure does not roll the tag back.
// Returns nil when no profile owns the tag.
func (s *Server) applySwitchProfileByTag(ctx context.Context, d model.Device, tag string) map[string]any {
	var profile, err = s.Store.SwitchProfileByTag(ctx, d.SiteID, tag)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return map[string]any{"ok": false, "error": err.Error()}
	}
	var spec portrules.Spec
	spec, err = portrules.ParseSpec(profile.Spec)
	if err != nil {
		return map[string]any{
			"ok": false, "profile_id": profile.ID, "profile_name": profile.Name,
			"error": "unreadable rules: " + err.Error(),
		}
	}
	var fresh model.Device
	fresh, err = s.Store.GetDevice(ctx, d.ID)
	if err != nil {
		fresh = d
	}
	var applied int
	var issues []string
	applied, issues, err = s.applySwitchSpecToDevice(ctx, fresh, spec, profile.Name)
	var out = map[string]any{
		"profile_id": profile.ID, "profile_name": profile.Name,
	}
	if len(issues) > 0 {
		out["ok"] = false
		out["issues"] = issues
		return out
	}
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
		return out
	}
	out["ok"] = true
	out["applied"] = applied
	return out
}

// mergePortConfigs builds what will be written to `devices.ports`.
//
// MERGE, not replace. A port object carries four configuration fields (`mode`, `pvid`,
// `tagged`, `poe`) but also the port LABEL — a meshcraft annotation the device does not
// know and nobody can recover — and the latest telemetry reading (link, rates, watts).
// Rewriting the whole object would erase both at once: forty-eight port descriptions
// lost for one profile apply, which cannot be repaired.
func mergePortConfigs(existing json.RawMessage,
	assignments []portrules.Assignment) (map[string]json.RawMessage, error) {
	var current = map[string]map[string]json.RawMessage{}
	if len(existing) > 0 {
		var err = json.Unmarshal(existing, &current)
		if err != nil {
			return nil, err
		}
	}
	var out = map[string]json.RawMessage{}
	var a portrules.Assignment
	for _, a = range assignments {
		var fields = current[a.Port]
		if fields == nil {
			fields = map[string]json.RawMessage{}
		}
		var k string
		var v json.RawMessage
		for k, v = range a.Config.Fields() {
			fields[k] = v
		}
		var raw, err = json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		out[a.Port] = raw
	}
	return out, nil
}

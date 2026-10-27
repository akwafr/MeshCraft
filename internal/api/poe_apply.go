package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/recon"
)

// applyDevicePoE turns PoE on for every port that requests it, when the
// connector implements PoEApplier and declares WritesExtra("poe").
//
// ENABLE ONLY: cf. driver.PoEPortIntent — recon.RequestedExtras only ever
// reports POSITIVE intents, so this never asks a connector to cut power.
// Same shape as applyDeviceSTP: resolved from the site's stored port
// configuration rather than a separate plan, because unlike spanning tree
// PoE has no site-wide preference to intersect with capabilities — a port
// either requests it or it doesn't.
func (s *Server) applyDevicePoE(ctx context.Context, d model.Device,
	conn driver.Connector, adm driver.Admin) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !conn.WritesExtra("poe") {
		return out, nil
	}
	var ports = recon.RequestedExtras(d.Ports)["poe"]
	if len(ports) == 0 {
		return out, nil
	}
	var applier, ok = driver.AsPoEApplier(adm)
	if !ok {
		out.Notes = append(out.Notes,
			"connector declares PoE writable but Admin has no ApplyPoE")
		return out, nil
	}
	var plan driver.PoEPlan
	var name string
	for _, name = range ports {
		plan.Ports = append(plan.Ports, driver.PoEPortIntent{Port: name})
	}
	return applier.ApplyPoE(ctx, plan)
}

// validPoEClass: the classes the port panel may request. EXPLICIT classes
// only — the empty Class (cf. driver.PoEPortIntent) is reserved for the
// automatic profile-driven path above, and letting it through here would give
// a direct HTTP caller the one form whose meaning is "whatever the platform's
// default on is", which is not what a button press should ever send.
func validPoEClass(class string) bool {
	return class == driver.PoEClassNone || class == driver.PoEClass24V ||
		class == driver.PoEClass54V
}

// --- THE REFUSALS, JUDGED APART FROM THE HANDLER ---
//
// LIFTED OUT ON 2026-08-09, and the sibling file explains why at length
// (dhcpsnoop_apply.go, "THE REFUSALS, JUDGED APART FROM THE HANDLER"). The short
// version: internal/api tests run without PostgreSQL because Server.Store is a
// concrete *store.Store, so nothing beginning with deviceForWrite can be driven
// from a test. Judged inline, as they were here, these status codes were
// unassertable and free to drift — and drift they would, because 400 / 422 / 502
// are three different instructions to the operator (fix your request / stop
// looking, this hardware will not do it / the device said no, try again).
//
// PoE earns the same treatment as snooping and then some: [driver.PoEClassNone]
// CUTS POWER to whatever is plugged in. A gesture that will be refused anyway
// must be refused before a session is opened on a device that may be at its
// session ceiling, and the sentence it is refused with must be pinned.

// poeRefusal is the answer given INSTEAD of writing: an HTTP status and the
// sentence that goes in it. The zero value means "nothing to refuse".
//
// A TYPE DISTINCT FROM dhcpSnoopRefusal rather than a shared one, deliberately.
// The two ladders answer for two different features and their rungs are not
// interchangeable — PoE has a class vocabulary and no VLAN dimension, snooping
// has the reverse — and a shared type would invite a shared judgement function
// whose message could only be generic. What is shared is the SHAPE, which is
// what makes them readable side by side.
type poeRefusal struct {
	Code int
	Msg  string
}

// respond writes the refusal and says whether it wrote one, so each rung reads
// `if refusal.respond(w) { return }`.
//
// A ZERO REFUSAL WRITES NOTHING and returns false — the property that makes the
// shape safe: a rung that must not refuse leaves the response untouched rather
// than committing a 0-status header that would freeze everything after it.
func (ref poeRefusal) respond(w http.ResponseWriter) bool {
	if ref.Code == 0 {
		return false
	}
	respondErr(w, ref.Code, ref.Msg)
	return true
}

// poeIntents turns the request body's port→class map into the plan's intents,
// or returns the 400 to answer with.
//
// THREE JUDGEMENTS, all of them worth pinning without a database:
//
//   - an EMPTY map is refused. A gesture that names no port wrote nothing, and
//     answering 200 to it tells the operator something happened;
//   - a BLANK port name is refused, which it was NOT before this function
//     existed. `{"ports":{"":"none"}}` used to travel to the connector, come
//     back as somebody's flavour of Skipped, and log an event line reading
//     "→ none" — a power cut, attributed to no port. The sibling endpoint
//     fixed the same hole on 2026-08-08; this one still had it;
//   - the CLASS vocabulary, which is the one rung PoE has and snooping does not.
//
// THE ORDER IS NOW STABLE, and that too is a repair rather than a tidy-up. The
// handler used to build plan.Ports by ranging over the map, and Go randomises
// that: two identical clicks produced two different transcripts, two different
// orders of power cuts, and — on a connector that stops at the first refusal —
// two different sets of ports actually written. Lexical order is enough here;
// every connector re-sorts into its own faceplate order before writing.
func poeIntents(ports map[string]string) ([]driver.PoEPortIntent, poeRefusal) {
	if len(ports) == 0 {
		return nil, poeRefusal{Code: http.StatusBadRequest, Msg: "no ports in request"}
	}
	var out = make([]driver.PoEPortIntent, 0, len(ports))
	var name, class string
	for name, class = range ports {
		if strings.TrimSpace(name) == "" {
			return nil, poeRefusal{Code: http.StatusBadRequest,
				Msg: "a port with an empty name cannot have its PoE class changed"}
		}
		if !validPoEClass(class) {
			return nil, poeRefusal{Code: http.StatusBadRequest,
				Msg: "port " + name + ": class must be \"none\", \"24v\" or \"54v\""}
		}
		out = append(out, driver.PoEPortIntent{Port: name, Class: class})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, poeRefusal{}
}

// poePlatformRefusal judges what can be judged WITHOUT reaching the device: is
// there a connector, and does it declare this write?
//
// 422 for all three, with distinct sentences because they are repaired by
// unrelated gestures — re-run discovery, ship a build that carries the
// connector, or nothing at all because this hardware is simply not proven for
// PoE. The connector's own sentence is carried VERBATIM: driver.ConnectorFor
// writes three different explanations and each names its repair, which a
// message rewritten here would throw away.
//
// A nil connector with no error is refused rather than dereferenced: the
// alternative is a panic in a handler whose whole job is to answer a status
// code.
//
// THE LAST RUNG IS THE ONE THAT MATTERS TODAY. Only ubiquiti/edgeswitch declares
// "poe" writable; cisco/ios, hpe/procurve and fs/s3400 all record PoE as
// writable-on-this-platform and unimplemented, awaiting a command survey. This
// 422 is what keeps a UI from offering a power switch on those ranges.
func poePlatformRefusal(conn driver.Connector, connErr error) poeRefusal {
	if connErr != nil {
		return poeRefusal{Code: http.StatusUnprocessableEntity, Msg: connErr.Error()}
	}
	if conn == nil {
		return poeRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "no connector is associated with this device"}
	}
	if !conn.WritesExtra("poe") {
		return poeRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "this platform does not declare PoE as writable"}
	}
	return poeRefusal{}
}

// poeDeviceRefusal turns a failure the DEVICE or the path to it produced into
// 502, and nothing else into anything.
//
// One function for both places that can fail that way — reaching the device and
// writing on it — so the two cannot drift into two codes for one kind of event.
// 502 and not 500: nothing in the controller is broken, an upstream did not
// cooperate, and the operator's next move is to look at the switch.
func poeDeviceRefusal(err error) poeRefusal {
	if err == nil {
		return poeRefusal{}
	}
	return poeRefusal{Code: http.StatusBadGateway, Msg: err.Error()}
}

// poeApplierFor turns proven access into the applier to write through, or the
// refusal to answer with.
//
// THE ERROR IS TESTED FIRST, and that ordering is the judgement: adminFor may
// return an Admin AND an error at once — Instant On and RouterOS hand back the
// session on failure so the transcript survives (cf. Server.adminFor). Asserting
// the interface first would find a perfectly good applier on unproven access and
// answer 422 "this platform cannot do that" about a device that merely refused
// the password.
//
// The 422 that follows is a STAGING mismatch, not a hardware limit: the
// connector declared "poe" writable and its Admin has no ApplyPoE. Same
// sentence as the automatic path's note, deliberately — the two paths meet the
// same half-built connector and must say the same thing.
func poeApplierFor(adm driver.Admin, admErr error) (driver.PoEApplier, poeRefusal) {
	var refusal = poeDeviceRefusal(admErr)
	if refusal.Code != 0 {
		return nil, refusal
	}
	var applier, ok = driver.AsPoEApplier(adm)
	if !ok {
		return nil, poeRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "connector declares PoE writable but Admin has no ApplyPoE"}
	}
	return applier, poeRefusal{}
}

// handleApplyDevicePoE pushes explicit per-port PoE classes onto one device,
// NOW — the immediate half of the port panel's on/off, next to the stored
// intent the panel writes through PUT /ports.
//
// Body: `{ "ports": { "0/5": "54v", "0/6": "none" } }`. Same privilege as the
// other device writes that touch hardware (site_admin): cutting power drops
// whatever is plugged in, which is a heavier gesture than storing a VLAN.
//
// DELIBERATELY STATELESS about intent: the caller keeps writing the `poe`
// intent through the ports endpoint exactly as before, and this endpoint only
// carries the wire half. Two reasons. First, the automatic path stays honest:
// applyDevicePoE remains enable-only, so an intent stored here as `poe: false`
// is never re-cut behind the operator's back by a later auto-apply. Second,
// splitting the halves means a device that refuses the write still keeps the
// recorded intention — the operator's decision is not lost with the failure.
func (s *Server) handleApplyDevicePoE(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Ports map[string]string `json:"ports"`
	}
	if !decode(w, r, &in) {
		return
	}
	var plan driver.PoEPlan
	var refusal poeRefusal
	plan.Ports, refusal = poeIntents(in.Ports)
	if refusal.respond(w) {
		return
	}

	// The platform is judged BEFORE anything is dialled, and the order matters
	// beyond tidiness: Instant On allows five simultaneous sessions, the ProCurve
	// 2610 authenticates and then refuses the channel at four, and these
	// platforms lock accounts (cf. Server.adminFor). A gesture that will be
	// refused anyway must not spend a session.
	var conn, connErr = s.connectorFor(r.Context(), d)
	refusal = poePlatformRefusal(conn, connErr)
	if refusal.respond(w) {
		return
	}

	var _, adm, _, err = s.adminFor(r.Context(), d)
	if adm != nil {
		defer adm.Close()
	}
	var applier driver.PoEApplier
	applier, refusal = poeApplierFor(adm, err)
	if refusal.respond(w) {
		return
	}
	var res driver.ApplyResult
	res, err = applier.ApplyPoE(r.Context(), plan)
	refusal = poeDeviceRefusal(err)
	if refusal.respond(w) {
		return
	}

	// Refresh the stored observation in the same gesture, best-effort: the
	// panel repaints from `poe_state`, and leaving the pre-write snapshot in
	// place would show the old class exactly when the operator watches for the
	// new one. The EdgeSwitch applier already re-read to verify; this second
	// read is the price of a connector interface that does not hand that
	// verification state back. Failure only annotates — the write above stands.
	var reader, canRead = driver.AsPoEReader(adm)
	if canRead {
		var fresh, readErr = reader.ReadPoE(r.Context())
		if readErr == nil {
			var raw, marshalErr = json.Marshal(fresh)
			if marshalErr == nil {
				_ = s.Store.SavePoEObserved(r.Context(), d.ID, raw)
			}
		} else {
			res.Notes = append(res.Notes, "PoE state not re-read after write: "+
				readErr.Error())
		}
	}

	s.emitDeviceEvent(r, d, "device.poe_apply", poeApplySeverity(in.Ports),
		"PoE written on \""+d.Name+"\": "+poeApplySummary(in.Ports))
	respond(w, http.StatusOK, map[string]any{
		"ok":     true,
		"result": res,
		"steps":  transcriptOf(adm),
	})
}

// poeApplySeverity grades the event line by the DIRECTION of the gesture, not
// by whether it succeeded — the rule portadmin_apply.go already applies to
// shut / un-shut, and PoE earns it for the same physical reason.
//
// [driver.PoEClassNone] on a port CUTS THE POWER to whatever is plugged into
// it: a camera, an access point, a phone goes dark. Somebody reading this
// device's history later, looking for the moment service stopped, must find
// that line standing out — exactly as they would for a "shutdown". Every
// other class asks the port to keep delivering (or to start), which is
// ordinary and stays [model.SevInfo].
//
// ONE "none" among twenty ports is enough to raise the whole line: the
// summary names each port and its class, so the warning points at a gesture
// whose detail is right there, and under-grading it because most of the batch
// was harmless would hide the one that was not.
//
// Success is deliberately NOT part of this. A refusal lands in
// [driver.ApplyResult.Skipped] in the response body; the event records what
// was ASKED of the hardware, the same way its two sibling handlers do.
func poeApplySeverity(ports map[string]string) int {
	var class string
	for _, class = range ports {
		if class == driver.PoEClassNone {
			return model.SevWarning
		}
	}
	return model.SevInfo
}

// poeApplySummary names what was asked, for the event log: "0/5 → 54v,
// 0/6 → none". Sorted so two identical gestures read as two identical lines.
func poeApplySummary(ports map[string]string) string {
	var names = make([]string, 0, len(ports))
	var name string
	for name = range ports {
		names = append(names, name)
	}
	sort.Strings(names)
	var out = ""
	for _, name = range names {
		if out != "" {
			out += ", "
		}
		out += name + " → " + ports[name]
	}
	return out
}

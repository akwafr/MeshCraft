package api

// The three gestures meshcraft can ask a UniFi controller to perform: adopt,
// forget, reprovision (ADR-0025).
//
// WHAT IS NOT HERE, and it is the decision. There is no handler that writes a
// setting. meshcraft holds no intention about these boxes: "reconfigure" is
// `reprovision`, which asks the controller to reassert ITS OWN configuration.
// The mirror stays a mirror (ADR-0015 §5 as narrowed by ADR-0025 §2), so
// nothing here can create a second opinion about what a box should look like.
//
// ROLES. All three are `site_admin`, like every other call that uses the token
// against the foreign controller. The read that lists what is adoptable is
// site_admin too — it uses the token. `operator` keeps only the import, which
// writes into meshcraft's own mirror.
//
// EVERY GESTURE RE-IMPORTS. A gesture makes the mirror stale the instant it
// succeeds, and the screen that triggered it is looking at the stale copy. The
// re-import is best-effort: a gesture that worked must not be reported as
// failed because the sync that followed it timed out.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/unifi"
)

// gestureTimeout bounds one gesture AND its confirming read. Longer than a
// plain call because `Do` re-reads the site's device list to confirm (ADR-0025
// §6), shorter than an import because it touches one box.
const gestureTimeout = 45 * time.Second

// handleExtRights says what the extension's token can really do.
//
// This exists so the product states its capability instead of discovering it
// at the worst moment (ADR-0025 §7). Note what it does NOT claim: a token that
// is neither owner nor super-admin comes back `unproven`, never `denied` —
// UniFi does not expose per-site rights here, and a right we cannot read is
// not a right we can deny.
func (s *Server) handleExtRights(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, found = s.siteExtOf(w, r, siteID)
	if !found {
		return
	}
	var cli, err = s.unifiClient(r.Context(), e)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var rights unifi.Rights
	rights, err = cli.Rights(r.Context())
	if err != nil {
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}
	respond(w, http.StatusOK, rights)
}

// handleExtAdoptable lists the boxes the controller can see and has not taken
// in.
//
// Deliberately NOT part of the mirror: a pending box belongs to no site yet, so
// it is not this site's inventory and must not be stored as if it were. It is
// read live, every time, and the screen says where it comes from.
func (s *Server) handleExtAdoptable(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, found = s.pairedExtOf(w, r, siteID)
	if !found {
		return
	}
	var cli, err = s.unifiClient(r.Context(), e)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var list []unifi.Candidate
	list, err = cli.ListAdoptable(r.Context(), e.RemoteSiteRef)
	if err != nil {
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}
	respond(w, http.StatusOK, list)
}

type extAdoptInput struct {
	MAC string `json:"mac"`
}

// handleExtAdopt takes a pending box into the paired site.
//
// Keyed on MAC and hung off the EXTENSION rather than off a mirror row,
// because the thing being adopted has no mirror row — it is not in the site's
// inventory until this call succeeds. That asymmetry with the other two
// gestures is real and is why they live on different routes.
func (s *Server) handleExtAdopt(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, found = s.pairedExtOf(w, r, siteID)
	if !found {
		return
	}
	var in extAdoptInput
	if readJSON(w, r, &in, extBodyLimit) != nil {
		return
	}
	in.MAC = strings.ToLower(strings.TrimSpace(in.MAC))
	if in.MAC == "" {
		respondErr(w, http.StatusBadRequest, "mac is required: name the box to adopt")
		return
	}

	s.runGesture(w, r, e, unifi.Adopt, unifi.Target{
		SiteID:  e.RemoteSiteID,
		SiteRef: e.RemoteSiteRef,
		MAC:     in.MAC,
	}, "extension.adopted", model.SevInfo,
		"Box "+in.MAC+" adopted into \""+e.RemoteSiteName+"\" by "+auth.UserFrom(r.Context()).Email)
}

// handleExtReprovision asks the controller to push its own configuration to a
// mirrored box again.
//
// This IS "reconfigure" (ADR-0025 §2), and the naming is deliberate: calling it
// "reconfigure" in the code would invite somebody to add a payload of settings
// to it, which is the whole thing this decision refuses.
func (s *Server) handleExtReprovision(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, d, found = s.extDeviceOf(w, r, siteID)
	if !found {
		return
	}
	s.runGesture(w, r, e, unifi.Reprovision, unifi.Target{
		SiteID:   e.RemoteSiteID,
		SiteRef:  e.RemoteSiteRef,
		DeviceID: d.ExternalID,
		MAC:      d.MAC,
	}, "extension.reprovisioned", model.SevInfo,
		"Reprovisioning asked for \""+d.Name+"\" on "+e.Kind+" by "+auth.UserFrom(r.Context()).Email)
}

type extForgetInput struct {
	ConfirmName string `json:"confirm_name"`
}

// handleExtForget removes a box from the UniFi site.
//
// IRREVERSIBLE, and the gate is the one `delete_object` uses (ADR-0012): the
// box's exact current name, which the caller could only have obtained by
// reading the box it is about to remove. The comparison is NOT done here — it
// is done inside unifi.Do, against a name re-read from the controller, so no
// handler and no later refactor can skip it (ADR-0025 §5).
//
// `warning` in the site log, and it is the only one of the three: re-adoption
// brings the box back but returns neither its name nor its per-box settings.
func (s *Server) handleExtForget(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, d, found = s.extDeviceOf(w, r, siteID)
	if !found {
		return
	}
	var in extForgetInput
	if readJSON(w, r, &in, extBodyLimit) != nil {
		return
	}
	if strings.TrimSpace(in.ConfirmName) == "" {
		respondErr(w, http.StatusBadRequest,
			"confirm_name is required: pass the box's exact current name. "+
				"Forgetting it cannot be undone, and the name is what proves you read the right box.")
		return
	}

	s.runGesture(w, r, e, unifi.Forget, unifi.Target{
		SiteID:      e.RemoteSiteID,
		SiteRef:     e.RemoteSiteRef,
		DeviceID:    d.ExternalID,
		MAC:         d.MAC,
		ConfirmName: in.ConfirmName,
	}, "extension.forgotten", model.SevWarning,
		"Box \""+d.Name+"\" ("+d.MAC+") removed from \""+e.RemoteSiteName+"\" by "+
			auth.UserFrom(r.Context()).Email+"; it leaves the site and returns to adoption")
}

// --- Plumbing ---

// runGesture is the single path from an HTTP request to unifi.Do.
//
// Factored so the five things that must happen around every gesture cannot
// drift apart between three handlers: the bounded context, the client, the
// call, the site-log entry, and the re-import that refreshes the mirror the
// gesture just invalidated.
func (s *Server) runGesture(w http.ResponseWriter, r *http.Request,
	e model.SiteExtension, g unifi.Gesture, t unifi.Target,
	event string, sev int, line string) {

	var ctx, cancel = context.WithTimeout(r.Context(), gestureTimeout)
	defer cancel()

	var cli, err = s.unifiClient(ctx, e)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}

	err = cli.Do(ctx, g, t)
	switch {
	case isSSIDMoved(err):
		// The edit's precondition failed: a field it meant to replace no longer
		// holds the value the operator was shown (ADR-0029 §6). Same 409 and the
		// same "NOTHING WAS DONE" as a wrong confirmation name, because it is
		// the same kind of answer — the request was well formed and the world
		// had moved. The sentence names the live value, which is the operator's
		// next move: look at what somebody else did, then decide again.
		respondErr(w, http.StatusConflict, err.Error()+". NOTHING WAS DONE.")
		return
	case errors.Is(err, unifi.ErrConfirmMismatch):
		// 409, not 400: the request was well formed and the world disagreed
		// with it. The sentence is shown verbatim because it names what the box
		// is actually called, which is the operator's next move.
		respondErr(w, http.StatusConflict, err.Error()+". NOTHING WAS DONE.")
		return
	case errors.Is(err, unifi.ErrUnconfirmed):
		// The gesture was ACCEPTED and could not be proven to have landed. It
		// is logged — something may well have happened — and reported as the
		// ambiguity it is, because on an API that answers "ok" to commands it
		// does not know, silence must not read as success (ADR-0025 §6).
		//
		// A 200 CARRYING `confirmed: false`, and NOT a 202. The browser client
		// treats every 2xx as success and reads the error envelope only on a
		// failure, so a 202 with a sentence in it would have shown the operator
		// a success toast and swallowed the warning — the exact opposite of the
		// point. The ambiguity therefore travels in the body, where the screen
		// has to look at it.
		s.emitSite(r, e.SiteID, event, model.SevWarning, line+
			" — ACCEPTED BUT NOT CONFIRMED: re-read the inventory before deciding it worked", nil)
		s.resyncAfterGesture(r, e)
		s.live.notify(e.SiteID, ChangeEvent, r.Header.Get(liveTabHeader))
		respond(w, http.StatusOK, map[string]any{
			"gesture":   g.Name(),
			"ok":        true,
			"confirmed": false,
			"because": "the controller accepted the gesture and it could not be confirmed by a read. " +
				"Import the inventory again before assuming either outcome: " + err.Error(),
		})
		return
	case err != nil:
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}

	s.emitSite(r, e.SiteID, event, sev, line, nil)
	s.resyncAfterGesture(r, e)
	s.live.notify(e.SiteID, ChangeEvent, r.Header.Get(liveTabHeader))
	respond(w, http.StatusOK, map[string]any{
		"gesture": g.Name(), "ok": true, "confirmed": true,
	})
}

// resyncAfterGesture re-imports the mirror a gesture just invalidated.
//
// BEST EFFORT, and that is the decision: a gesture that succeeded must not be
// reported as failed because the import behind it was slow. The screen refetches
// anyway, so a missed resync costs a stale row until the next import, not a
// wrong answer about the gesture.
func (s *Server) resyncAfterGesture(r *http.Request, e model.SiteExtension) {
	var ctx, cancel = context.WithTimeout(context.WithoutCancel(r.Context()), extSyncTimeout)
	defer cancel()

	var cli, err = s.unifiClient(ctx, e)
	if err != nil {
		return
	}
	var boxes []unifi.DeviceDetail
	boxes, err = cli.ListDevices(ctx, e.RemoteSiteID)
	if err != nil {
		return
	}
	// The SSIDs are re-read TOO, which they were not when the only gestures
	// were about boxes. An SSID edit changes one (ADR-0029), and the screen
	// that triggered it refetches at once — so leaving the service sets alone
	// here would show the operator the old name until the background pass came
	// round, which reads as an edit that did not land.
	//
	// Degrading rather than failing, as everywhere the legacy API is involved:
	// `ssidsKnown` false tells the store to keep what it has instead of
	// deleting the lot.
	var ssids []unifi.SSID
	var ssidsKnown = true
	ssids, err = cli.ListSSIDs(ctx, e.RemoteSiteRef)
	if err != nil {
		ssids, ssidsKnown = nil, false
	}
	_, _ = s.Store.ReplaceExtInventory(ctx, e.ID, e.SiteID,
		unifi.ToExtDevices(boxes), unifi.ToExtSSIDs(ssids), ssidsKnown)
}

// pairedExtOf is siteExtOf plus the pairing check. A gesture needs both remote
// identifiers, and an unpaired extension has neither — failing here with a
// sentence beats failing inside the client with "empty site id".
func (s *Server) pairedExtOf(w http.ResponseWriter, r *http.Request, siteID string) (model.SiteExtension, bool) {
	var e, found = s.siteExtOf(w, r, siteID)
	if !found {
		return model.SiteExtension{}, false
	}
	if e.RemoteSiteID == "" {
		respondErr(w, http.StatusConflict,
			"this extension is not paired yet: choose the remote site first")
		return model.SiteExtension{}, false
	}
	return e, true
}

// extDeviceOf resolves the URL's mirror row and the extension behind it.
//
// The box is addressed by MESHCRAFT's id and the vendor identifiers are read
// out of the mirror, never taken from the request. That is what keeps a gesture
// inside the site's own inventory: a caller cannot name an arbitrary device on
// somebody's controller, only a box this site has actually mirrored.
func (s *Server) extDeviceOf(w http.ResponseWriter, r *http.Request, siteID string) (
	model.SiteExtension, model.ExtDevice, bool) {

	var d, err = s.Store.GetExtDevice(r.Context(), chi.URLParam(r, "extDevID"))
	if err != nil {
		fail(w, err)
		return model.SiteExtension{}, model.ExtDevice{}, false
	}
	if d.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return model.SiteExtension{}, model.ExtDevice{}, false
	}
	var e model.SiteExtension
	e, err = s.Store.GetSiteExtension(r.Context(), d.ExtensionID)
	if err != nil {
		fail(w, err)
		return model.SiteExtension{}, model.ExtDevice{}, false
	}
	if e.SiteID != siteID || e.RemoteSiteID == "" {
		respondErr(w, http.StatusConflict,
			"the extension this box came from is no longer paired")
		return model.SiteExtension{}, model.ExtDevice{}, false
	}
	return e, d, true
}

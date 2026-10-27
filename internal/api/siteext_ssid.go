package api

// The SSID editor: read one service set from the controller, change it, read it
// back (ADR-0029).
//
// WHY THIS EXISTS WHERE siteext_gestures.go SAYS IT NEVER WOULD. That file's
// header still says "there is no handler that writes a setting", and it was
// right under ADR-0025. ADR-0029 reopened exactly one sentence of it — §2's
// "no imported setting becomes editable" — on a scope decided by measurement
// rather than by taste: the SSID has a REST resource that re-reads one item,
// and the access point has none at all (`/rest/device` answers 404 on
// application 10.6.109). So service sets are editable here and boxes are not,
// and that frontier is a class of object rather than a list of fields.
//
// WHAT IS STILL NOT HERE. No intention is stored. `Was`/`Want` arrive in a
// request, serve as a precondition, and are gone when it returns. There is no
// desired-state column, no drift detection, no convergence — ADR-0025 §3
// survives intact, which is the whole reason this is compatible with it.
//
// THE READ IS LIVE, AND THAT IS NOT AN OPTIMISATION. The form is filled from
// the CONTROLLER, never from meshcraft's mirror. The mirror is at most thirty
// seconds old now and that is still too old: the precondition's job is to
// notice that somebody changed this SSID in the UniFi interface while the form
// was open, and a precondition built from our own copy would compare a stale
// truth against itself and pass.
//
// ROLES. Reading the editable settings is `site_admin`, not `viewer`: it uses
// the extension's token against the foreign controller, like every other call
// that does. Writing is `site_admin` too.

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/unifi"
)

// ssidFormPayload is what the edit form is built from.
//
// The networks travel WITH the settings, in one answer, because a network
// identifier on its own is unreadable: the form needs the names to show, and a
// second round trip would let the screen render a choice list it cannot label.
type ssidFormPayload struct {
	// Mirror is the row the browser addressed, so the screen can title the
	// dialog without a second read.
	Mirror model.ExtSSID `json:"mirror"`
	// Live is the controller's current state of the same service set. This is
	// what `was` must be built from.
	Live unifi.SSIDDetail `json:"live"`
	// Networks are the choices for the network field, read from the controller.
	Networks []unifi.Network `json:"networks"`
}

// handleGetExtSSID reads one service set's editable settings from the
// controller.
func (s *Server) handleGetExtSSID(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, mirror, found = s.extSSIDOf(w, r, siteID)
	if !found {
		return
	}
	var cli, err = s.unifiClient(r.Context(), e)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}

	var live unifi.SSIDDetail
	live, err = cli.GetSSID(r.Context(), e.RemoteSiteRef, mirror.ExternalID)
	if err != nil {
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}

	// The networks DEGRADE rather than fail the form: an operator who came to
	// rename an SSID should not be stopped because the network list did not
	// answer. The screen shows the network field as unavailable instead.
	var networks []unifi.Network
	networks, err = cli.ListNetworks(r.Context(), e.RemoteSiteRef)
	if err != nil {
		networks = nil
	}

	respond(w, http.StatusOK, ssidFormPayload{Mirror: mirror, Live: live, Networks: networks})
}

// handlePutExtSSID writes settings to one service set.
//
// The body is a unifi.SSIDEdit: `was` (what the operator was shown), `want`
// (what they ask for) and an optional `passphrase`. Everything that matters
// about how it is applied lives in unifi.EditSSID — the precondition, the
// whole-object merge, the confirming read — and deliberately NOT here: a check
// a handler has to remember is a check that will be forgotten, which is the
// same reasoning that put the forget gate inside unifi.Do.
func (s *Server) handlePutExtSSID(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, mirror, found = s.extSSIDOf(w, r, siteID)
	if !found {
		return
	}
	var in unifi.SSIDEdit
	if readJSON(w, r, &in, extBodyLimit) != nil {
		return
	}
	if in.Passphrase != nil && strings.TrimSpace(*in.Passphrase) == "" {
		// A field left empty means "leave it alone", and the screen says so in
		// words. A payload that carries an EMPTY passphrase is therefore a
		// client defect, and accepting it would blank the pre-shared key of a
		// production network on a distracted save — the hazard ADR-0029 §7
		// exists to close.
		respondErr(w, http.StatusBadRequest,
			"passphrase was sent empty. Leave the field out entirely to keep the current one; "+
				"an empty value is never written.")
		return
	}

	// The name is for the journal line, which must say WHICH service set moved.
	// Taken from the mirror rather than from the payload: a caller does not get
	// to label its own audit entry.
	s.runGesture(w, r, e, unifi.EditSSID, unifi.Target{
		SiteID:   e.RemoteSiteID,
		SiteRef:  e.RemoteSiteRef,
		SSIDID:   mirror.ExternalID,
		SSIDEdit: in,
	}, "extension.ssid_edited", model.SevInfo,
		"SSID \""+mirror.Name+"\" edited on \""+e.RemoteSiteName+"\" by "+
			auth.UserFrom(r.Context()).Email+editedFields(in))
}

// editedFields names what the edit touched, for the journal.
//
// The PASSPHRASE is named as having changed and its value never appears — not
// here, not in the error paths, not anywhere a log can reach. ADR-0015 forbids
// importing it, and a journal line is an import with a longer retention.
func editedFields(in unifi.SSIDEdit) string {
	var parts []string
	if in.Want.Broadcasting != nil {
		parts = append(parts, "broadcasting")
	}
	if in.Want.Name != nil {
		parts = append(parts, "name")
	}
	if in.Want.Hidden != nil {
		parts = append(parts, "hidden")
	}
	if in.Want.Bands != nil {
		parts = append(parts, "bands")
	}
	if in.Want.NetworkID != nil {
		parts = append(parts, "network")
	}
	if in.Want.Guest != nil {
		parts = append(parts, "guest")
	}
	if in.Want.ClientIsolation != nil {
		parts = append(parts, "client isolation")
	}
	if in.Want.FastRoaming != nil {
		parts = append(parts, "fast roaming")
	}
	if in.Want.BSSTransition != nil {
		parts = append(parts, "BSS transition")
	}
	if in.Want.PowerSave != nil {
		parts = append(parts, "power save")
	}
	if in.Want.MulticastEnhance != nil {
		parts = append(parts, "multicast enhancement")
	}
	if in.Want.ProxyARP != nil {
		parts = append(parts, "proxy ARP")
	}
	if in.Passphrase != nil {
		parts = append(parts, "passphrase")
	}
	if len(parts) == 0 {
		return " — nothing was changed"
	}
	return " — " + strings.Join(parts, ", ")
}

// extSSIDOf resolves the URL's mirrored service set and the extension behind
// it.
//
// Same shape as extDeviceOf and for the same reason: the vendor identifier
// comes out of the mirror, never out of the request, so a caller can only reach
// a service set this site has actually imported.
func (s *Server) extSSIDOf(w http.ResponseWriter, r *http.Request, siteID string) (
	model.SiteExtension, model.ExtSSID, bool) {

	var sw, err = s.Store.GetExtSSID(r.Context(), chi.URLParam(r, "extSSIDID"))
	if err != nil {
		fail(w, err)
		return model.SiteExtension{}, model.ExtSSID{}, false
	}
	if sw.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return model.SiteExtension{}, model.ExtSSID{}, false
	}
	var e model.SiteExtension
	e, err = s.Store.GetSiteExtension(r.Context(), sw.ExtensionID)
	if err != nil {
		fail(w, err)
		return model.SiteExtension{}, model.ExtSSID{}, false
	}
	if e.SiteID != siteID || e.RemoteSiteID == "" {
		respondErr(w, http.StatusConflict,
			"the extension this SSID came from is no longer paired")
		return model.SiteExtension{}, model.ExtSSID{}, false
	}
	if strings.TrimSpace(e.RemoteSiteRef) == "" {
		// The legacy API is indexed by the short reference, not the UUID. An
		// extension paired before that was stored has no way to reach wlanconf.
		respondErr(w, http.StatusConflict,
			"this extension has no short site reference; pair it again to edit SSIDs")
		return model.SiteExtension{}, model.ExtSSID{}, false
	}
	return e, sw, true
}

// extSSIDMovedStatus maps the precondition failure onto a status.
//
// 409, like a failed confirmation name: the request was well formed and the
// world disagreed with it. Wired into runGesture's switch so an edit and a
// forget report a moved object the same way.
func isSSIDMoved(err error) bool { return errors.Is(err, unifi.ErrSSIDMoved) }

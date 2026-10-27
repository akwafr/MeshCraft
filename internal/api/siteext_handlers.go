package api

// Site extensions: the per-site configuration of a foreign controller, the
// pairing with one of its sites, and the read-only import it feeds
// (ADR-0015, ADR-0016).
//
// ROLES, and why they are split the way they are. The list is `operator`,
// because it carries no secret — only `has_token`. Every write and every call
// that USES the token against the foreign controller is `site_admin`: listing
// the remote sites proves the token works, which is itself a capability. The
// import itself is `operator`: it writes only into meshcraft's own mirror, and
// it is the gesture somebody will want to repeat while watching a screen.
//
// NOTHING IN THIS FILE WRITES TO THE FOREIGN CONTROLLER. The unifi package has
// no write method, which is where that promise is actually kept.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/unifi"
)

// extBodyLimit bounds an extension payload. A base URL and a token; anything
// larger is a mistake or an attack.
const extBodyLimit = 16 << 10

// handleListSiteExtensions lists what a site is backed by. Secret-free, hence
// `operator`: an operator must be able to see that an extension exists and
// whether its last contact failed, without being able to read its token.
func (s *Server) handleListSiteExtensions(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var list, err = s.Store.ListSiteExtensions(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, list)
}

type siteExtInput struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	BaseURL   string `json:"base_url"`
	Token     string `json:"token"`
	VerifyTLS *bool  `json:"verify_tls"`
	Transport string `json:"transport"`
	ProxyID   string `json:"proxy_id"`
	Enabled   *bool  `json:"enabled"`
}

// handleUpsertSiteExtension creates or edits the extension of a kind on a site.
//
// An ABSENT token leaves the stored one alone. That is not a convenience: the
// UI cannot read the token back, so it cannot resubmit it, and without this
// rule renaming an extension's label would destroy its credential.
func (s *Server) handleUpsertSiteExtension(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in siteExtInput
	if readJSON(w, r, &in, extBodyLimit) != nil {
		return
	}

	in.Kind = strings.TrimSpace(in.Kind)
	if in.Kind == "" {
		in.Kind = model.ExtUniFiOS
	}
	if in.Kind != model.ExtUniFiOS {
		respondErr(w, http.StatusBadRequest,
			"unknown extension kind \""+in.Kind+"\": this controller only implements "+model.ExtUniFiOS)
		return
	}

	// The trailing slash is stripped HERE as well as in unifi.New, because the
	// stored value is what an operator will read back on the screen and compare
	// with what they pasted. UniFi's nginx answers a bare 400 to the double
	// slash a naive join makes, naming neither path nor cause.
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if in.BaseURL == "" {
		respondErr(w, http.StatusBadRequest, "base_url is required")
		return
	}
	if !strings.HasPrefix(in.BaseURL, "http://") && !strings.HasPrefix(in.BaseURL, "https://") {
		respondErr(w, http.StatusBadRequest, "base_url must start with http:// or https://")
		return
	}

	if in.Transport == "" {
		in.Transport = model.ExtTransportDirect
	}
	if in.Transport != model.ExtTransportDirect && in.Transport != model.ExtTransportProxy {
		respondErr(w, http.StatusBadRequest,
			"transport must be \""+model.ExtTransportDirect+"\" or \""+model.ExtTransportProxy+"\"")
		return
	}
	// Refused rather than stored: a `proxy` transport with no proxy cannot
	// work, and a configuration that cannot work must fail where it is typed,
	// not at the first import.
	if in.Transport == model.ExtTransportProxy && in.ProxyID == "" {
		respondErr(w, http.StatusBadRequest,
			"transport \"proxy\" needs a proxy_id: name the proxy that reaches this controller")
		return
	}

	var e = model.SiteExtension{
		SiteID:    siteID,
		Kind:      in.Kind,
		Label:     strings.TrimSpace(in.Label),
		BaseURL:   in.BaseURL,
		VerifyTLS: in.VerifyTLS == nil || *in.VerifyTLS,
		Transport: in.Transport,
		ProxyID:   in.ProxyID,
		Enabled:   in.Enabled == nil || *in.Enabled,
	}

	var tokenEnc []byte
	var err error
	if strings.TrimSpace(in.Token) != "" {
		tokenEnc, err = s.Secrets.Seal(strings.TrimSpace(in.Token))
		if err != nil {
			fail(w, err)
			return
		}
	}

	var out model.SiteExtension
	out, err = s.Store.UpsertSiteExtension(r.Context(), e, tokenEnc, auth.UserFrom(r.Context()).ID)
	if err != nil {
		fail(w, err)
		return
	}
	// The background import forgets everything it knew about this extension
	// (ADR-0029 §12). THIS is what makes the "import now" button unnecessary
	// rather than merely removed: somebody who has just pasted a corrected
	// token would otherwise sit out a cooldown that may be at its
	// fifteen-minute ceiling, and would reasonably want a button to escape it.
	// The next pass is now less than thirty seconds away.
	s.extSync.forget(out.ID)
	// `extension.configured`, not `credential.revealed`: this is a declaration,
	// not an access. The token's VALUE never appears in the message.
	s.emitSite(r, siteID, "extension.configured", model.SevInfo,
		"Extension \""+out.Kind+"\" pointed at "+out.BaseURL+" by "+auth.UserFrom(r.Context()).Email, nil)
	respond(w, http.StatusOK, out)
}

// handleDeleteSiteExtension removes the extension and, by cascade, its mirror.
func (s *Server) handleDeleteSiteExtension(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, found = s.siteExtOf(w, r, siteID)
	if !found {
		return
	}
	var err = s.Store.DeleteSiteExtension(r.Context(), e.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "extension.removed", model.SevWarning,
		"Extension \""+e.Kind+"\" and its imported inventory removed by "+
			auth.UserFrom(r.Context()).Email, nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleRevealSiteExtensionToken returns the cleartext token. Reserved to
// `site_admin` and TRACED, exactly like a site credential: untraced cleartext
// access is worth less than no vault at all. The event TYPE is the same
// (`credential.revealed`) on purpose, so the audit trail does not depend on
// which screen was opened.
func (s *Server) handleRevealSiteExtensionToken(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, enc, err = s.Store.RevealSiteExtensionToken(r.Context(), chi.URLParam(r, "extID"))
	if err != nil {
		fail(w, err)
		return
	}
	// The extension must belong to the URL's site: without this check a
	// site-admin would read another site's token by guessing a UUID.
	if e.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var token string
	token, err = s.Secrets.Open(enc)
	if err != nil {
		// 422 with a sentence, as the discovery credentials do: a key that
		// changed is not a server fault and the operator's fix is specific.
		respondErr(w, http.StatusUnprocessableEntity,
			"this token cannot be decrypted with the active vault key: it was sealed with "+
				"another one. Re-enter it on the extension.")
		return
	}
	var me = auth.UserFrom(r.Context())
	s.emitSite(r, siteID, "credential.revealed", model.SevWarning,
		"Extension token \""+e.Kind+"@"+e.BaseURL+"\" revealed by "+me.Email, nil)
	respond(w, http.StatusOK, map[string]any{"id": e.ID, "token": token})
}

// handleListExtRemoteSites asks the foreign controller which sites it has, so
// the pairing screen offers a choice instead of a text field.
//
// `site_admin`, because it uses the token. The answer carries ids and names,
// and names are labels only (ADR-0015 §6).
func (s *Server) handleListExtRemoteSites(w http.ResponseWriter, r *http.Request) {
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
		s.extFailed(r, e, err)
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var sites []unifi.Site
	sites, err = cli.ListSites(r.Context())
	if err != nil {
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}
	var out = make([]model.ExtRemoteSite, 0, len(sites))
	var u unifi.Site
	for _, u = range sites {
		out = append(out, model.ExtRemoteSite{
			ID: u.ID, Ref: u.InternalReference, Name: u.Name,
			Paired: u.ID == e.RemoteSiteID,
		})
	}
	respond(w, http.StatusOK, out)
}

type extPairInput struct {
	RemoteSiteID string `json:"remote_site_id"`
}

// handleExtPair records the pairing. The client sends only the stable id; the
// reference and the name are read back from the controller rather than trusted
// from the browser, so the stored label is always what the controller actually
// said at that moment.
func (s *Server) handleExtPair(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var e, found = s.siteExtOf(w, r, siteID)
	if !found {
		return
	}
	var in extPairInput
	if readJSON(w, r, &in, extBodyLimit) != nil {
		return
	}
	in.RemoteSiteID = strings.TrimSpace(in.RemoteSiteID)
	if in.RemoteSiteID == "" {
		respondErr(w, http.StatusBadRequest, "remote_site_id is required")
		return
	}

	var cli, err = s.unifiClient(r.Context(), e)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var remote unifi.Site
	remote, err = cli.FindSite(r.Context(), in.RemoteSiteID)
	if err != nil {
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}
	var out model.SiteExtension
	out, err = s.Store.PairSiteExtension(r.Context(), e.ID,
		remote.ID, remote.InternalReference, remote.Name)
	if err != nil {
		fail(w, err)
		return
	}
	// Re-pairing resets the backoff for the same reason an upsert does: the
	// thing that was failing may be exactly what was just fixed.
	s.extSync.forget(out.ID)
	s.emitSite(r, siteID, "extension.paired", model.SevInfo,
		"Site paired with \""+remote.Name+"\" on "+e.Kind+" by "+auth.UserFrom(r.Context()).Email, nil)
	respond(w, http.StatusOK, out)
}

// handleExtSync runs one import pass. `operator`: it writes only into
// meshcraft's own mirror, never into the foreign controller.
//
// The two failure modes are kept apart on purpose. A site absent from a list
// the controller DID answer orphans the pairing; anything else is recorded as
// an error and changes no pairing — because a ten-minute outage must not erase
// somebody's correlation work (ADR-0015 §7).
func (s *Server) handleExtSync(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var e, found = s.siteExtOf(w, r, siteID)
	if !found {
		return
	}
	if e.RemoteSiteID == "" {
		respondErr(w, http.StatusConflict,
			"this extension is not paired yet: choose the remote site first")
		return
	}

	// The whole pass is bounded, not each call: a site with forty boxes makes
	// forty-one requests, and bounding only the individual call would let the
	// sum run unbounded.
	var ctx, cancel = context.WithTimeout(r.Context(), extSyncTimeout)
	defer cancel()

	var cli, err = s.unifiClient(ctx, e)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}

	var remote unifi.Site
	remote, err = cli.FindSite(ctx, e.RemoteSiteID)
	switch {
	case errors.Is(err, unifi.ErrRemoteSiteGone):
		_ = s.Store.MarkSiteExtensionOrphaned(r.Context(), e.ID,
			"the controller no longer lists the paired site")
		s.emitSite(r, siteID, "extension.orphaned", model.SevWarning,
			"Paired site \""+e.RemoteSiteName+"\" has disappeared from "+e.Kind+
				"; the imported inventory is kept and stamped stale", nil)
		respondErr(w, http.StatusGone,
			"the paired site no longer exists on the controller. The imported inventory "+
				"is kept as-is; pair again to resume.")
		return
	case err != nil:
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}
	// A rename is absorbed silently — that is the entire payoff of pairing on
	// the id rather than on the name.
	_ = s.Store.RefreshRemoteSiteLabel(ctx, e.ID, remote.InternalReference, remote.Name)

	var boxes []unifi.DeviceDetail
	boxes, err = cli.ListDevices(ctx, e.RemoteSiteID)
	if err != nil {
		s.extFailed(r, e, err)
		respondErr(w, extStatusFor(err), err.Error())
		return
	}

	// SSIDs come from the LEGACY endpoint, which is under no contract. Its
	// failure DEGRADES the import instead of failing it: an access-point
	// inventory with no SSID list is still worth having, and `ssidsKnown`
	// false tells the store to leave the previously known SSIDs alone rather
	// than deleting them.
	var ssids []unifi.SSID
	var ssidsKnown = true
	ssids, err = cli.ListSSIDs(ctx, remote.InternalReference)
	if err != nil {
		ssidsKnown = false
		ssids = nil
	}

	var rep model.ExtSyncReport
	rep, err = s.Store.ReplaceExtInventory(ctx, e.ID, siteID,
		unifi.ToExtDevices(boxes), unifi.ToExtSSIDs(ssids), ssidsKnown)
	if err != nil {
		fail(w, err)
		return
	}
	rep.RemoteSiteName = remote.Name
	s.emitSite(r, siteID, "extension.imported", model.SevInfo,
		"Imported from \""+remote.Name+"\": "+strconv.Itoa(rep.AccessPoints)+" access point(s), "+
			strconv.Itoa(rep.Switches)+" switch(es), "+strconv.Itoa(rep.SSIDs)+" SSID(s)", rep)
	s.live.notify(siteID, ChangeEvent, r.Header.Get(liveTabHeader))
	respond(w, http.StatusOK, rep)
}

// handleListSiteWiFi is the Wi-Fi screen's single read: the access points of
// the site and the SSIDs they broadcast.
//
// `viewer`, like any other observation screen: a mirror carries no secret and
// no capability. The two halves travel together because the screen is useless
// with one of them — an access point with no SSID list, or SSIDs with nothing
// broadcasting them.
func (s *Server) handleListSiteWiFi(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var aps, err = s.Store.ListExtDevices(r.Context(), siteID, model.ExtClassAccessPoint)
	if err != nil {
		fail(w, err)
		return
	}
	var ssids []model.ExtSSID
	ssids, err = s.Store.ListExtSSIDs(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var exts []model.SiteExtension
	exts, err = s.Store.ListSiteExtensions(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	// The extensions travel with the payload so the screen can say WHY it is
	// empty — "no extension configured" and "import never ran" are different
	// sentences, and an empty list that explains nothing reads as a bug.
	respond(w, http.StatusOK, map[string]any{
		"access_points": aps,
		"ssids":         ssids,
		"extensions":    exts,
	})
}

// handleListSiteExtDevices lists the mirror, optionally filtered by class. This
// is where imported SWITCHES are read — deliberately a separate list from
// `/devices`, which holds what meshcraft configures (ADR-0015 §5).
func (s *Server) handleListSiteExtDevices(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var class = r.URL.Query().Get("class")
	switch class {
	case "", model.ExtClassAccessPoint, model.ExtClassSwitch,
		model.ExtClassGateway, model.ExtClassOther:
	default:
		respondErr(w, http.StatusBadRequest, "unknown class \""+class+"\"")
		return
	}
	var list, err = s.Store.ListExtDevices(r.Context(), siteID, class)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, list)
}

// --- Plumbing ---

// siteExtOf loads the URL's extension and verifies it belongs to the URL's
// site. Factored because every handler above needs both checks and forgetting
// the second is a cross-site read.
func (s *Server) siteExtOf(w http.ResponseWriter, r *http.Request, siteID string) (model.SiteExtension, bool) {
	var e, err = s.Store.GetSiteExtension(r.Context(), chi.URLParam(r, "extID"))
	if err != nil {
		fail(w, err)
		return model.SiteExtension{}, false
	}
	if e.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return model.SiteExtension{}, false
	}
	return e, true
}

// unifiClient opens the token and builds the HTTP client, honouring the
// extension's transport.
//
// This is the single place the token is decrypted for USE rather than for
// display, and it is on purpose that it returns a client rather than the token:
// no caller above ever holds the cleartext.
func (s *Server) unifiClient(ctx context.Context, e model.SiteExtension) (*unifi.Client, error) {
	if !e.Enabled {
		return nil, errors.New("this extension is disabled")
	}
	var _, enc, err = s.Store.RevealSiteExtensionToken(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	var token string
	token, err = s.Secrets.Open(enc)
	if err != nil {
		return nil, errors.New("the stored token cannot be decrypted with the active vault key")
	}

	var opts = []unifi.Option{unifi.WithInsecureTLS(!e.VerifyTLS)}
	if e.Transport == model.ExtTransportProxy {
		// Reachability is a configuration, not an architecture (ADR-0015 §3):
		// a controller shut inside a site LAN is reached through the proxy
		// already posted there, which is what spared us a container.
		if s.Tunnels == nil {
			return nil, errors.New("no proxy tunnel hub on this controller")
		}
		var proxyID = e.ProxyID
		opts = append(opts, unifi.WithDialer(
			func(dialCtx context.Context, _, addr string) (net.Conn, error) {
				var host, portStr, splitErr = net.SplitHostPort(addr)
				if splitErr != nil {
					return nil, splitErr
				}
				var port int
				port, splitErr = net.LookupPort("tcp", portStr)
				if splitErr != nil {
					return nil, splitErr
				}
				return s.Tunnels.Dial(dialCtx, proxyID, host, port)
			}))
	}
	return unifi.New(e.BaseURL, token, opts...)
}

// extFailed records a transport failure on the extension so the screen can
// explain itself. It deliberately does NOT orphan: an unreachable controller
// says nothing about whether the paired site still exists.
func (s *Server) extFailed(r *http.Request, e model.SiteExtension, cause error) {
	_ = s.Store.MarkSiteExtensionError(r.Context(), e.ID, firstSentence(cause.Error()))
}

// extStatusFor maps a foreign controller's refusal onto a status that tells the
// operator where to look. A bad token is 502 with a sentence, not 401: the
// meshcraft session is perfectly valid, and answering 401 would make the
// browser think the operator was logged out.
func extStatusFor(err error) int {
	switch {
	case errors.Is(err, unifi.ErrUnauthorized):
		return http.StatusBadGateway
	case errors.Is(err, unifi.ErrRemoteSiteGone):
		return http.StatusGone
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// firstSentence caps what is stored in `last_error`: enough to diagnose, not a
// whole HTML error page from somebody's reverse proxy.
func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// extSyncTimeout bounds a WHOLE import pass. One detail request per box on top
// of the list calls, so a site with many boxes needs more than a single call's
// budget — but there is still a hard ceiling, because an extension's slowness
// must never become a meshcraft page's slowness (ADR-0015, Consequences).
const extSyncTimeout = 90 * time.Second

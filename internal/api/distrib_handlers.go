package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
)

// Proxy distribution: binaries, enrollment tokens, and generated deployments.
//
// Deploying a proxy is open to site-admin, but the future proxy's COVERAGE is
// bounded by their rights: a site-admin only covers their sites; only a
// global-admin creates an instance proxy (cf. docs/en/01-Architecture/Proxy.md).

// --- Proxy binaries ---

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	var arts, err = s.Distrib.ListArtifacts()
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, arts)
}

func (s *Server) handleDownloadArtifact(w http.ResponseWriter, r *http.Request) {
	s.Distrib.ServeArtifact(w, r, chi.URLParam(r, "name"))
}

// --- Enrollment tokens ---

const enrollTokenTTL = time.Hour

func (s *Server) handleListEnrollTokens(w http.ResponseWriter, r *http.Request) {
	var tokens, err = s.Store.ListEnrollTokens(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, tokens)
}

// requestedCoverage reads the coverage requested for the future proxy and
// validates it against the caller's rights.
//
//	global-admin : free coverage; defaults to an instance proxy.
//	site-admin   : never an instance proxy, and only THEIR sites; defaults to
//	               all sites they administer.
//
// This is where isolation is enforced: a site-admin cannot fabricate a proxy
// that would see another site's networks.
func (s *Server) requestedCoverage(w http.ResponseWriter, r *http.Request) (bool, []string, bool) {
	var in struct {
		Global  bool     `json:"global"`
		SiteIDs []string `json:"site_ids"`
	}
	if !decodeOptional(w, r, &in) {
		return false, nil, false
	}
	var adminSites, isGlobalAdmin, err = s.Auth.SiteIDsWithRole(r.Context(), "site_admin")
	if err != nil {
		fail(w, err)
		return false, nil, false
	}
	if isGlobalAdmin {
		if in.Global || len(in.SiteIDs) == 0 {
			return true, nil, true
		}
		return false, in.SiteIDs, true
	}
	if len(adminSites) == 0 {
		respondErr(w, http.StatusForbidden,
			"deploying a proxy requires being administrator of at least one site")
		return false, nil, false
	}
	if in.Global {
		respondErr(w, http.StatusForbidden,
			"an instance proxy (all sites) can only be created by an instance administrator")
		return false, nil, false
	}
	if len(in.SiteIDs) == 0 {
		return false, adminSites, true
	}
	var allowed = map[string]bool{}
	var id string
	for _, id = range adminSites {
		allowed[id] = true
	}
	for _, id = range in.SiteIDs {
		if !allowed[id] {
			respondErr(w, http.StatusForbidden,
				"requested coverage outside your sites: you can only cover sites you administer")
			return false, nil, false
		}
	}
	return false, in.SiteIDs, true
}

// newEnrollToken issues an enrollment token and logs the issuance: a secret
// that grants entry of a presence point into the instance.
func (s *Server) newEnrollToken(w http.ResponseWriter, r *http.Request) (string, time.Time, bool) {
	var global, siteIDs, ok = s.requestedCoverage(w, r)
	if !ok {
		return "", time.Time{}, false
	}
	var token, hash, err = auth.NewSessionToken()
	if err != nil {
		fail(w, err)
		return "", time.Time{}, false
	}
	var expiresAt = time.Now().Add(enrollTokenTTL)
	var me = auth.UserFrom(r.Context())
	var t, createErr = s.Store.CreateEnrollToken(r.Context(), hash, me.ID, expiresAt, global, siteIDs)
	if createErr != nil {
		fail(w, createErr)
		return "", time.Time{}, false
	}
	var scope = "instance proxy (all sites)"
	if !t.Global {
		scope = strconv.Itoa(len(t.SiteIDs)) + " site(s) covered"
	}
	s.emitInstance(r, "proxy.enroll_token_created", model.SevNotice,
		"Proxy enrollment token issued — "+scope)
	return token, expiresAt, true
}

func (s *Server) handleCreateEnrollToken(w http.ResponseWriter, r *http.Request) {
	var token, expiresAt, ok = s.newEnrollToken(w, r)
	if !ok {
		return
	}
	// The secret is returned only here, once.
	respond(w, http.StatusCreated, map[string]any{
		"token":      token,
		"expires_at": expiresAt,
	})
}

func (s *Server) handleDeleteEnrollToken(w http.ResponseWriter, r *http.Request) {
	var err = s.Store.DeleteEnrollToken(r.Context(), chi.URLParam(r, "tokenID"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Generated deployments ---

// handleProxyCompose creates an enrollment token and returns docker-compose
// ready to paste, with the image pulled from the controller's built-in registry.
func (s *Server) handleProxyCompose(w http.ResponseWriter, r *http.Request) {
	var token, _, ok = s.newEnrollToken(w, r)
	if !ok {
		return
	}
	respond(w, http.StatusCreated, map[string]string{
		"compose":  s.Distrib.ComposeYAML(r, token),
		"image":    s.Distrib.ImageRef(r),
		"insecure": s.Distrib.InsecureNote(r),
	})
}

// handleProxyInstallScript returns the systemd install script (and the unit
// alone, for those who prefer to install it manually), with a fresh enrollment token.
func (s *Server) handleProxyInstallScript(w http.ResponseWriter, r *http.Request) {
	var token, expiresAt, ok = s.newEnrollToken(w, r)
	if !ok {
		return
	}
	var script, err = s.Distrib.InstallScript(r, token, expiresAt)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusCreated, map[string]string{
		"script": script,
		"unit":   s.Distrib.SystemdUnit(r),
	})
}

// Proxy enrollment and heartbeat live in proxy_handlers.go.

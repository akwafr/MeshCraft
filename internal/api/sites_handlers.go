package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
)

func (s *Server) handleListSites(w http.ResponseWriter, r *http.Request) {
	var u = auth.UserFrom(r.Context())
	var sites, err = s.Store.ListSites(r.Context(), u.ID, u.IsGlobalAdmin)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, sites)
}

func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) || in.Name == "" {
		if in.Name == "" {
			respondErr(w, http.StatusBadRequest, "name required")
		}
		return
	}
	var site, err = s.Store.CreateSite(r.Context(), in.Name)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, site.ID, "site.created", model.SevNotice,
		"Site \""+site.Name+"\" created", nil)
	// A site is born with its admin credential. Without it, every adopted device
	// would stay on its factory password — often none — until someone remembers
	// to create one.
	var me = auth.UserFrom(r.Context())
	var created, credErr = EnsureSiteAdminCredential(r.Context(), s.Store, s.Secrets, site.ID, me.ID)
	if credErr != nil {
		// The site exists: we do not undo it, but we do not hide the failure —
		// a site without a credential is a site where nothing can be pushed.
		s.emitSite(r, site.ID, "credential.generate_failed", model.SevWarning,
			"Admin credential not generated: "+credErr.Error(), nil)
	} else if created {
		s.emitSite(r, site.ID, "credential.generated", model.SevNotice,
			"Admin credential generated — 16-character password, reveal from "+
				"Configuration → Credentials.", nil)
	}
	respond(w, http.StatusCreated, site)
}

func (s *Server) handleRenameSite(w http.ResponseWriter, r *http.Request) {
	var siteID = chi.URLParam(r, "siteID")
	if !s.Auth.CanSite(r.Context(), siteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "insufficient rights on this site")
		return
	}
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
		// LocalDomain: the DNS domain this site's equipment belongs to.
		//
		// A POINTER, and that is what lets the field carry three states where a
		// string carries two: absent (leave it alone), a value (set it), and ""
		// (this site has no domain). Without the pointer, every rename that did
		// not resend the domain would clear it — and clearing it silently changes
		// the FQDN of every switch the site provisions afterwards.
		LocalDomain *string `json:"local_domain"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name == "" && in.Slug == "" && in.LocalDomain == nil {
		respondErr(w, http.StatusBadRequest, "name, slug or local_domain required")
		return
	}
	var domain *string
	if in.LocalDomain != nil {
		var trimmed = strings.TrimSpace(*in.LocalDomain)
		domain = &trimmed
	}
	var site, err = s.Store.RenameSite(r.Context(), siteID, in.Name, in.Slug, domain)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, site)
}

func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request) {
	if !auth.UserFrom(r.Context()).IsGlobalAdmin {
		respondErr(w, http.StatusForbidden, "reserved for global administrators")
		return
	}
	var err = s.Store.DeleteSite(r.Context(), chi.URLParam(r, "siteID"))
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

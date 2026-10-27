package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
)

// Agent repositories — the operator surface of the git→OCI-layout pipeline.
// Global-admin only: registering a repository means BUILDING third-party code
// on the controller host and publishing the result in the registry every proxy
// pulls from, which is an instance decision, never a site one.
//
// The shape mirrors the driver-repos surface (driverrepo_handlers.go); the
// difference is the product — a layout a proxy pulls, not a container the
// controller runs — so there is no start/stop, only build and enable.

// handleListAgentRepos answers the whole picture in one call: repositories and
// their latest build state.
func (s *Server) handleListAgentRepos(w http.ResponseWriter, r *http.Request) {
	var repos, err = s.Store.ListAgentRepos(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var builds = map[string]any{}
	var i int
	for i = range repos {
		var list, bErr = s.Store.ListAgentBuilds(r.Context(), repos[i].ID, 1)
		if bErr == nil && len(list) > 0 {
			// The log is heavy and diagnostic: its own endpoint, not every refresh.
			list[0].Log = ""
			builds[repos[i].ID] = list[0]
		}
	}
	respond(w, http.StatusOK, map[string]any{
		"repos": repos, "builds": builds,
		"enabled": s.AgentHost != nil,
	})
}

// handleAddAgentRepo registers a repository and kicks its first build in the
// background — the build log is the progress report, not this response.
func (s *Server) handleAddAgentRepo(w http.ResponseWriter, r *http.Request) {
	if s.AgentHost == nil {
		respondErr(w, http.StatusUnprocessableEntity,
			"agent builds are not enabled on this instance (MESHCRAFT_DOCKER_SOCKET)")
		return
	}
	var in struct {
		URL       string `json:"url"`
		Subdir    string `json:"subdir"`
		Ref       string `json:"ref"`
		AgentName string `json:"agent_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	in.AgentName = strings.Trim(strings.TrimSpace(in.AgentName), "/")
	if in.URL == "" {
		respondErr(w, http.StatusBadRequest, "a git URL is required")
		return
	}
	if !validAgentName(in.AgentName) {
		respondErr(w, http.StatusBadRequest,
			"agent_name must be a registry path like meshcraft/agent-foo "+
				"(lowercase, no \".\" or \"..\" segments)")
		return
	}
	var repo, err = s.Store.AddAgentRepo(r.Context(), in.URL,
		strings.Trim(strings.TrimSpace(in.Subdir), "/"), strings.TrimSpace(in.Ref),
		in.AgentName, auth.UserFrom(r.Context()).Email)
	if err != nil {
		respondErr(w, http.StatusConflict, err.Error())
		return
	}
	s.kickAgentBuild(repo.ID)
	respond(w, http.StatusCreated, repo)
}

// handleBuildAgentRepo rebuilds: pull the ref again, build, swap the layout.
// THE update gesture for an agent image.
func (s *Server) handleBuildAgentRepo(w http.ResponseWriter, r *http.Request) {
	if s.AgentHost == nil {
		respondErr(w, http.StatusUnprocessableEntity, "agent builds are not enabled")
		return
	}
	var _, err = s.Store.GetAgentRepo(r.Context(), chi.URLParam(r, "repoID"))
	if err != nil {
		respondErr(w, http.StatusNotFound, "no such agent repository")
		return
	}
	s.kickAgentBuild(chi.URLParam(r, "repoID"))
	respond(w, http.StatusAccepted, map[string]any{"ok": true})
}

// handleSetAgentRepoEnabled enables or disables a repository. Disabling stops
// the reconciler from rebuilding it; the existing layout is left in place until
// the repository is deleted, so proxies pinned to a version keep pulling.
func (s *Server) handleSetAgentRepoEnabled(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	var id = chi.URLParam(r, "repoID")
	var err = s.Store.SetAgentRepoEnabled(r.Context(), id, in.Enabled)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if in.Enabled {
		s.kickAgentBuild(id)
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteAgentRepo forgets the repository (builds cascade) and drops its
// on-disk layout, so a removed agent stops being pullable.
func (s *Server) handleDeleteAgentRepo(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "repoID")
	var repo, err = s.Store.GetAgentRepo(r.Context(), id)
	if err != nil {
		respondErr(w, http.StatusNotFound, "no such agent repository")
		return
	}
	if s.AgentHost != nil {
		_ = s.AgentHost.Remove(repo.AgentName)
	}
	if err = s.Store.DeleteAgentRepo(r.Context(), id); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAgentBuildLog serves a repository's build history WITH the latest
// log — the only diagnostic surface a failed third-party build has.
func (s *Server) handleAgentBuildLog(w http.ResponseWriter, r *http.Request) {
	var builds, err = s.Store.ListAgentBuilds(r.Context(), chi.URLParam(r, "repoID"), 20)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"builds": builds})
}

// kickAgentBuild runs the build worker in the background with its own budget:
// the HTTP request that asked finished long ago when a slow clone lands.
func (s *Server) kickAgentBuild(repoID string) {
	go func() {
		var ctx, cancel = context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		var err = s.AgentHost.BuildRepo(ctx, repoID)
		if err != nil {
			slog.Error("agent build failed", "repo", repoID, "err", err.Error())
		}
	}()
}

// validAgentName mirrors the registry's own repository check (distrib.layoutDir):
// non-empty, slash-separated, no empty/"."/".." segment. Validated here too so a
// bad name is refused at registration, not at pull time.
func validAgentName(name string) bool {
	if name == "" {
		return false
	}
	var seg string
	for _, seg = range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

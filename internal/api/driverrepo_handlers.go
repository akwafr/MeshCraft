package api

import (
	"context"

	"log/slog"
	"meshcraft/internal/auth"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// External driver repositories — the operator surface of the git→build→run
// pipeline. Global-admin only: registering a repository means BUILDING AND
// RUNNING third-party code on the controller host, which is an instance
// decision, never a site one.

// handleListDriverRepos answers the whole picture in one call: repositories,
// their latest build state, and the running instances — the Drivers page
// paints from this and nothing else.
func (s *Server) handleListDriverRepos(w http.ResponseWriter, r *http.Request) {
	var repos, err = s.Store.ListDriverRepos(r.Context())
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var insts, iErr = s.Store.ListDriverInstances(r.Context())
	if iErr != nil {
		respondErr(w, http.StatusInternalServerError, iErr.Error())
		return
	}
	var builds = map[string]any{}
	var i int
	for i = range repos {
		var b, bErr = s.Store.LatestDriverBuild(r.Context(), repos[i].ID)
		if bErr == nil {
			// The log is heavy and diagnostic: served by its own endpoint,
			// not in every list refresh.
			b.Log = ""
			builds[repos[i].ID] = b
		}
	}
	respond(w, http.StatusOK, map[string]any{
		"repos": repos, "builds": builds, "instances": insts,
		"enabled": s.DriverHost != nil,
	})
}

// handleAddDriverRepo registers a repository and kicks its first build in the
// background — the build log is the progress report, not this response.
func (s *Server) handleAddDriverRepo(w http.ResponseWriter, r *http.Request) {
	if s.DriverHost == nil {
		respondErr(w, http.StatusUnprocessableEntity,
			"external drivers are not enabled on this instance "+
				"(MESHCRAFT_DOCKER_SOCKET and MESHCRAFT_DRIVER_SOCKS_LISTEN)")
		return
	}
	var in struct {
		URL    string `json:"url"`
		Subdir string `json:"subdir"`
		Ref    string `json:"ref"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL == "" {
		respondErr(w, http.StatusBadRequest, "a git URL is required")
		return
	}
	var repo, err = s.Store.AddDriverRepo(r.Context(), in.URL,
		strings.Trim(strings.TrimSpace(in.Subdir), "/"), strings.TrimSpace(in.Ref),
		auth.UserFrom(r.Context()).Email)
	if err != nil {
		respondErr(w, http.StatusConflict, err.Error())
		return
	}
	s.kickDriverBuild(repo.ID)
	respond(w, http.StatusCreated, repo)
}

// handleBuildDriverRepo rebuilds: pull the ref again, build, replace the
// container. THE update gesture for a driver.
func (s *Server) handleBuildDriverRepo(w http.ResponseWriter, r *http.Request) {
	if s.DriverHost == nil {
		respondErr(w, http.StatusUnprocessableEntity, "external drivers are not enabled")
		return
	}
	var _, err = s.Store.GetDriverRepo(r.Context(), chi.URLParam(r, "repoID"))
	if err != nil {
		respondErr(w, http.StatusNotFound, "no such driver repository")
		return
	}
	s.kickDriverBuild(chi.URLParam(r, "repoID"))
	respond(w, http.StatusAccepted, map[string]any{"ok": true})
}

// handleStopDriverRepo disables and unloads. The registry entry disappears
// with the container: devices bound to this connector fall back to "connector
// unknown to this binary" — stated, not silent.
func (s *Server) handleStopDriverRepo(w http.ResponseWriter, r *http.Request) {
	if s.DriverHost == nil {
		respondErr(w, http.StatusUnprocessableEntity, "external drivers are not enabled")
		return
	}
	var id = chi.URLParam(r, "repoID")
	var err = s.Store.SetDriverRepoEnabled(r.Context(), id, false)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err = s.DriverHost.Stop(r.Context(), id); err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// handleStartDriverRepo re-enables and rebuilds.
func (s *Server) handleStartDriverRepo(w http.ResponseWriter, r *http.Request) {
	if s.DriverHost == nil {
		respondErr(w, http.StatusUnprocessableEntity, "external drivers are not enabled")
		return
	}
	var id = chi.URLParam(r, "repoID")
	var err = s.Store.SetDriverRepoEnabled(r.Context(), id, true)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.kickDriverBuild(id)
	respond(w, http.StatusAccepted, map[string]any{"ok": true})
}

// handleDeleteDriverRepo unloads and forgets — builds and instance rows go
// with the repo (cascade).
func (s *Server) handleDeleteDriverRepo(w http.ResponseWriter, r *http.Request) {
	var id = chi.URLParam(r, "repoID")
	if s.DriverHost != nil {
		_ = s.DriverHost.Stop(r.Context(), id)
	}
	var err = s.Store.DeleteDriverRepo(r.Context(), id)
	if err != nil {
		respondErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDriverBuildLog serves the latest build's WHOLE log: the only
// diagnostic surface a failed third-party build has.
func (s *Server) handleDriverBuildLog(w http.ResponseWriter, r *http.Request) {
	var b, err = s.Store.LatestDriverBuild(r.Context(), chi.URLParam(r, "repoID"))
	if err != nil {
		respondErr(w, http.StatusNotFound, "this repository has no build yet")
		return
	}
	respond(w, http.StatusOK, b)
}

// kickDriverBuild runs the build worker in the background with its own
// budget: the HTTP request that asked finished long ago when a slow clone
// lands.
func (s *Server) kickDriverBuild(repoID string) {
	go func() {
		var ctx, cancel = context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		var err = s.DriverHost.BuildAndStart(ctx, repoID)
		if err != nil {
			slog.Error("driver build failed", "repo", repoID, "err", err.Error())
		}
	}()
}

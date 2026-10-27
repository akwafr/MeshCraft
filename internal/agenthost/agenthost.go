// Package agenthost is the lifecycle policy for agent images: which git
// repository builds into which registry repository, when it is (re)built, and
// what the database remembers. It is driverhost's sibling with one structural
// difference — it NEVER runs a container. A driver's product is a process on
// the controller host; an agent's product is an OCI layout on disk that a
// remote proxy pulls through the built-in registry (internal/distrib).
//
// So the pipeline stops one step earlier than driverhost's: clone → build →
// export the image as an OCI layout into OCIDir/<agent_name>/ → record. The
// registry then serves it with no further help.
package agenthost

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"meshcraft/internal/imagebuild"
	"meshcraft/internal/store"
)

// Manager runs the policy. One build at a time, for the same reason as
// driverhost: an image build is the heaviest thing the controller does to its
// host, and two third-party builds at once is how a small box tips over.
type Manager struct {
	Store  *store.Store
	Build  *imagebuild.Service
	OCIDir string

	mu       sync.Mutex
	building bool
}

// New builds a Manager. A nil Build (no Docker socket) makes Build a no-op that
// refuses legibly rather than panicking.
func New(st *store.Store, build *imagebuild.Service, ociDir string) *Manager {
	return &Manager{Store: st, Build: build, OCIDir: ociDir}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// dockerTag is the throwaway local tag the daemon builds into before the image
// is exported to a layout; it never leaves the host.
func (m *Manager) dockerTag(repoID string) string {
	return "meshcraft-agentbuild-" + short(repoID) + ":build"
}

// layoutDir is where this agent's OCI layout lives — the path the registry
// resolves <agent_name> to (internal/distrib.layoutDir). filepath.FromSlash so
// the agent_name's slashes become real subdirectories.
func (m *Manager) layoutDir(agentName string) string {
	return filepath.Join(m.OCIDir, filepath.FromSlash(agentName))
}

// version stamps a build: a UTC timestamp, the same form the proxy and driver
// versions take. It sorts, and "dev" is never one of them.
func version() string {
	return time.Now().UTC().Format("20060102.150405")
}

// BuildRepo clones, builds, and writes the OCI layout for one repository,
// recording the whole attempt. Serialized; a second request while one runs is
// refused rather than queued invisibly. On success the layout is live in the
// registry and the repo's current_version points at it.
func (m *Manager) BuildRepo(ctx context.Context, repoID string) error {
	if m.Build == nil {
		return fmt.Errorf("no Docker socket configured: agent images cannot be built on this instance")
	}
	m.mu.Lock()
	if m.building {
		m.mu.Unlock()
		return fmt.Errorf("a build is already running; retry when it lands")
	}
	m.building = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.building = false; m.mu.Unlock() }()

	var repo, err = m.Store.GetAgentRepo(ctx, repoID)
	if err != nil {
		return err
	}

	var build store.AgentBuild
	build, err = m.Store.CreateAgentBuild(ctx, repo.ID)
	if err != nil {
		return err
	}
	_ = m.Store.StartAgentBuild(ctx, build.ID)

	var ver = version()
	var log bytes.Buffer
	// Build into a temporary layout dir, then swap it in: a failed or
	// half-written build must never replace a layout a proxy is pulling.
	var staging = m.layoutDir(repo.AgentName) + ".building"
	_ = os.RemoveAll(staging)

	var res imagebuild.Result
	res, err = m.Build.BuildOCI(ctx, repo.URL, repo.Subdir, repo.Ref,
		m.dockerTag(repo.ID), ver, staging, &log)
	if err != nil {
		_ = os.RemoveAll(staging)
		_ = m.Store.FinishAgentBuild(ctx, build.ID, "failed",
			res.GitCommit, res.ImageID, ver, log.String(), err.Error())
		return fmt.Errorf("build failed: %w", err)
	}

	// Atomic-ish swap: remove the old layout, move the new one into place. The
	// window is a single rename per directory; a proxy pulling across it retries,
	// which is what a pull already does.
	var final = m.layoutDir(repo.AgentName)
	if err = os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		_ = os.RemoveAll(staging)
		return m.recordSwapFailure(ctx, build.ID, res, ver, &log, err)
	}
	_ = os.RemoveAll(final)
	if err = os.Rename(staging, final); err != nil {
		_ = os.RemoveAll(staging)
		return m.recordSwapFailure(ctx, build.ID, res, ver, &log, err)
	}

	if err = m.Store.FinishAgentBuild(ctx, build.ID, "ok",
		res.GitCommit, res.ImageID, ver, log.String(), ""); err != nil {
		return err
	}
	if err = m.Store.SetAgentRepoVersion(ctx, repo.ID, ver); err != nil {
		return err
	}
	slog.Info("agent image built", "agent", repo.AgentName, "version", ver,
		"commit", res.GitCommit)
	return nil
}

func (m *Manager) recordSwapFailure(ctx context.Context, buildID string,
	res imagebuild.Result, ver string, log *bytes.Buffer, cause error) error {

	log.WriteString("\nlayout swap failed: " + cause.Error() + "\n")
	_ = m.Store.FinishAgentBuild(ctx, buildID, "failed",
		res.GitCommit, res.ImageID, ver, log.String(), cause.Error())
	return fmt.Errorf("layout swap failed: %w", cause)
}

// Reconcile builds every enabled repository that has never produced a layout
// (current_version empty). Runs in the background at startup — an operator who
// registered an agent before the last restart finds it built, not merely
// remembered. Already-built repos are left alone; a rebuild is an explicit act.
func (m *Manager) Reconcile(ctx context.Context) {
	if m.Build == nil {
		return
	}
	var repos, err = m.Store.ListAgentRepos(ctx)
	if err != nil {
		slog.Error("agenthost: cannot list repos", "err", err)
		return
	}
	var repo store.AgentRepo
	for _, repo = range repos {
		if !repo.Enabled || repo.CurrentVersion != "" {
			continue
		}
		if err = m.BuildRepo(ctx, repo.ID); err != nil {
			slog.Error("agenthost: initial build failed", "agent", repo.AgentName, "err", err)
		}
	}
}

// Remove deletes a repository's on-disk layout. The store row is the API
// handler's to delete; this drops the files the registry was serving so a
// removed agent stops being pullable.
func (m *Manager) Remove(agentName string) error {
	return os.RemoveAll(m.layoutDir(agentName))
}

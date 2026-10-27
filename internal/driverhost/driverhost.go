// Package driverhost is the lifecycle policy for external drivers: which
// container runs for which repository, when it is (re)built, what gets
// registered in the driver registry, and what the database remembers about
// all of it. The mechanics live elsewhere — imagebuild talks to the daemon,
// restdriver speaks MDP — this package only decides.
package driverhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/driver"
	"meshcraft/internal/imagebuild"
	"meshcraft/internal/restdriver"
	"meshcraft/internal/store"
)

// Network is the bridge the driver containers live on. The controller
// attaches itself to it at reconcile (cf. imagebuild.AttachSelf) and dials
// container IPs directly: no published ports, so nothing on the operator's
// host can reach a driver but the controller.
const Network = "meshcraft-drivers"

// mdpPort is where a driver listens inside its container (the SDK default;
// driver.yaml's `listen` will make it configurable when a driver needs to).
const mdpPort = 8080

// healthBudget bounds the wait for a fresh container to answer /healthz.
const healthBudget = 60 * time.Second

// Manager runs the policy. One build at a time, on purpose: image builds
// are the heaviest thing the controller ever does to its own host, and two
// concurrent third-party builds compounding is how a small box tips over.
type Manager struct {
	Store *store.Store
	Build *imagebuild.Service
	RT    restdriver.Runtime

	mu       sync.Mutex
	building bool
	// registered remembers which connector names each repo put in the
	// registry, so unload removes exactly those.
	registered map[string][]string
}

// New builds a Manager.
func New(st *store.Store, build *imagebuild.Service, rt restdriver.Runtime) *Manager {
	return &Manager{Store: st, Build: build, RT: rt, registered: map[string][]string{}}
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func (m *Manager) tag(repoID string) string {
	return "meshcraft/driver-" + short(repoID) + ":latest"
}

func (m *Manager) containerName(repoID string) string {
	return "mdp-driver-" + short(repoID)
}

// BuildAndStart is the whole worker: clone, build, record the log, then
// replace the running instance with the new image. Serialized; a second
// request while one runs is refused rather than queued invisibly.
func (m *Manager) BuildAndStart(ctx context.Context, repoID string) error {
	m.mu.Lock()
	if m.building {
		m.mu.Unlock()
		return fmt.Errorf("a build is already running; retry when it lands")
	}
	m.building = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.building = false; m.mu.Unlock() }()

	var repo, err = m.Store.GetDriverRepo(ctx, repoID)
	if err != nil {
		return err
	}
	var build store.DriverBuild
	build, err = m.Store.CreateDriverBuild(ctx, repo.ID)
	if err != nil {
		return err
	}
	_ = m.Store.StartDriverBuild(ctx, build.ID)

	var log bytes.Buffer
	var res imagebuild.Result
	res, err = m.Build.Build(ctx, repo.URL, repo.Subdir, repo.Ref, m.tag(repo.ID), &log)
	if err != nil {
		_ = m.Store.FinishDriverBuild(ctx, build.ID, "failed",
			res.GitCommit, res.ImageID, log.String(), err.Error())
		return fmt.Errorf("build failed: %w", err)
	}
	if err = m.Store.FinishDriverBuild(ctx, build.ID, "ok",
		res.GitCommit, res.ImageID, log.String(), ""); err != nil {
		return err
	}
	return m.start(ctx, repo, build.ID)
}

// start replaces the repo's container with the current image and registers
// what it declares.
func (m *Manager) start(ctx context.Context, repo store.DriverRepo, buildID string) error {
	m.unregister(repo.ID)

	var inst = store.DriverInstance{RepoID: repo.ID, DriverName: repo.DriverName,
		State: "starting"}
	if buildID != "" {
		inst.BuildID = &buildID
	}
	var saved, _ = m.Store.UpsertDriverInstance(ctx, inst)

	var fail = func(err error) error {
		_ = m.Store.SetDriverInstanceState(ctx, saved.ID, "failed", err.Error())
		return err
	}

	var id, err = m.Build.RunContainer(ctx, imagebuild.RunSpec{
		Name:    m.containerName(repo.ID),
		Image:   m.tag(repo.ID),
		Network: Network,
		Labels:  map[string]string{"meshcraft.role": "driver", "meshcraft.repo": repo.ID},
	})
	if err != nil {
		return fail(err)
	}
	var ip string
	ip, err = m.Build.ContainerIP(ctx, id, Network)
	if err != nil {
		return fail(err)
	}
	var addr = fmt.Sprintf("http://%s:%d", ip, mdpPort)

	if err = waitHealthy(ctx, addr); err != nil {
		return fail(err)
	}
	var loaded []restdriver.Loaded
	loaded, err = restdriver.Load(ctx, addr, m.RT)
	if err != nil {
		return fail(err)
	}
	if len(loaded) == 0 {
		return fail(fmt.Errorf("the image declares no connector"))
	}

	var names []string
	var supports []any
	var l restdriver.Loaded
	for _, l = range loaded {
		names = append(names, l.Name)
		supports = append(supports, l.Support)
	}
	m.mu.Lock()
	m.registered[repo.ID] = names
	m.mu.Unlock()

	inst = saved
	inst.RepoID = repo.ID
	inst.DriverName = strings.Join(names, ",")
	inst.Protocol = loaded[0].Protocol
	inst.ContainerID = id
	inst.Addr = addr
	inst.State = "ready"
	inst.Error = ""
	inst.Supports, _ = json.Marshal(supports)
	inst.Info, _ = json.Marshal(loaded[0].Info)
	var _, upErr = m.Store.UpsertDriverInstance(ctx, inst)
	if upErr != nil {
		return upErr
	}
	// The wire declaration is authoritative for the repo's display name —
	// what /supports said, not what a yaml promised.
	_ = m.Store.SetDriverRepoName(ctx, repo.ID, inst.DriverName)
	slog.Info("driver loaded", "repo", repo.URL, "connectors", inst.DriverName, "addr", addr)
	return nil
}

// Stop unloads a repo's connectors and removes its container. Idempotent.
func (m *Manager) Stop(ctx context.Context, repoID string) error {
	m.unregister(repoID)
	var insts, err = m.Store.ListDriverInstances(ctx)
	if err != nil {
		return err
	}
	var i store.DriverInstance
	for _, i = range insts {
		if i.RepoID != repoID {
			continue
		}
		if i.ContainerID != "" {
			_ = m.Build.StopContainer(ctx, i.ContainerID)
		}
		return m.Store.SetDriverInstanceState(ctx, i.ID, "stopped", "")
	}
	return nil
}

func (m *Manager) unregister(repoID string) {
	m.mu.Lock()
	var names = m.registered[repoID]
	delete(m.registered, repoID)
	m.mu.Unlock()
	var n string
	for _, n = range names {
		driver.Unregister(n)
	}
}

// Reconcile brings reality to the declaration at startup: the controller
// attaches itself to the drivers network, then every ENABLED repo whose
// latest build landed is (re)started from its image — containers that
// survived the restart are replaced rather than trusted, because the
// registry they were registered in did not survive it.
func (m *Manager) Reconcile(ctx context.Context) {
	var err = m.Build.AttachSelf(ctx, Network)
	if err != nil {
		slog.Info("driverhost: not self-attached to the drivers network",
			"note", err.Error())
	}
	var repos, listErr = m.Store.ListDriverRepos(ctx)
	if listErr != nil {
		slog.Error("driverhost: reconcile could not list repos", "err", listErr.Error())
		return
	}
	var r store.DriverRepo
	for _, r = range repos {
		if !r.Enabled {
			_ = m.Stop(ctx, r.ID)
			continue
		}
		var build, buildErr = m.Store.LatestDriverBuild(ctx, r.ID)
		if buildErr != nil || build.State != "ok" {
			continue
		}
		var startErr = m.start(ctx, r, build.ID)
		if startErr != nil {
			slog.Error("driverhost: reconcile start failed",
				"repo", r.URL, "err", startErr.Error())
		}
	}
}

// waitHealthy polls the MDP health endpoint until it answers or the budget
// runs out — a fresh container needs a moment before its listener exists.
func waitHealthy(ctx context.Context, addr string) error {
	var client = &http.Client{Timeout: 3 * time.Second}
	var deadline = time.Now().Add(healthBudget)
	var lastErr error
	for time.Now().Before(deadline) {
		var res, err = client.Get(addr + "/mdp/v1/healthz")
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("healthz answered %d", res.StatusCode)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("driver never became healthy: %w", lastErr)
}

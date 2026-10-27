package store

import (
	"testing"
)

// TestAgentRepoAndBuildLifecycle exercises migration 0044 and the agent store
// methods end to end against the disposable database: register, build history,
// version tracking, uniqueness, cascade on delete. Skips without
// MESHCRAFT_TEST_DATABASE_URL (see rekey_test.go's header).
func TestAgentRepoAndBuildLifecycle(t *testing.T) {
	var st, ctx = testStore(t)
	var _, err = st.pool.Exec(ctx, `delete from agent_repos`)
	if err != nil {
		t.Fatal(err)
	}

	// Register.
	var repo, addErr = st.AddAgentRepo(ctx,
		"https://example.test/agents.git", "cmd/agent-foo", "main",
		"meshcraft/agent-foo", "admin@meshcraft.local")
	if addErr != nil {
		t.Fatalf("add: %v", addErr)
	}
	if !repo.Enabled || repo.CurrentVersion != "" {
		t.Errorf("fresh repo should be enabled with no version: %+v", repo)
	}

	// Uniqueness: same (url, subdir) and same agent_name both refused.
	var _, dupSrc = st.AddAgentRepo(ctx,
		"https://example.test/agents.git", "cmd/agent-foo", "", "meshcraft/agent-other", "x")
	if dupSrc == nil {
		t.Error("duplicate (url, subdir) must be refused")
	}
	var _, dupName = st.AddAgentRepo(ctx,
		"https://example.test/OTHER.git", "", "", "meshcraft/agent-foo", "x")
	if dupName == nil {
		t.Error("duplicate agent_name must be refused")
	}

	// A build, run to success, records its version and the log.
	var build, bErr = st.CreateAgentBuild(ctx, repo.ID)
	if bErr != nil {
		t.Fatalf("create build: %v", bErr)
	}
	if build.State != "queued" {
		t.Errorf("fresh build state = %q, want queued", build.State)
	}
	if err = st.StartAgentBuild(ctx, build.ID); err != nil {
		t.Fatal(err)
	}
	if err = st.FinishAgentBuild(ctx, build.ID, "ok", "abc123", "sha256:img",
		"20260821.101500", "clone…\nbuild…\nOK\n", ""); err != nil {
		t.Fatal(err)
	}
	if err = st.SetAgentRepoVersion(ctx, repo.ID, "20260821.101500"); err != nil {
		t.Fatal(err)
	}

	// The list reflects the version and the newest-first build.
	var repos, listErr = st.ListAgentRepos(ctx)
	if listErr != nil || len(repos) != 1 {
		t.Fatalf("list repos = %d (%v)", len(repos), listErr)
	}
	if repos[0].CurrentVersion != "20260821.101500" {
		t.Errorf("current_version not recorded: %q", repos[0].CurrentVersion)
	}
	var builds, blErr = st.ListAgentBuilds(ctx, repo.ID, 10)
	if blErr != nil || len(builds) != 1 {
		t.Fatalf("list builds = %d (%v)", len(builds), blErr)
	}
	if builds[0].State != "ok" || builds[0].Version != "20260821.101500" ||
		builds[0].GitCommit != "abc123" || builds[0].Log == "" {
		t.Errorf("build not landed as expected: %+v", builds[0])
	}

	// Delete cascades the build history.
	if err = st.DeleteAgentRepo(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}
	var after, _ = st.ListAgentBuilds(ctx, repo.ID, 10)
	if len(after) != 0 {
		t.Errorf("builds survived repo deletion: %d", len(after))
	}
}

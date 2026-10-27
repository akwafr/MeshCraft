package distrib

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/config"
)

// TestSplitV2PathKeepsRepositorySlashes: a repository name has slashes
// (meshcraft/agent-foo), so the split must anchor on the operation separator,
// not the first "/". Getting this wrong routes agent pulls to the wrong repo.
func TestSplitV2PathKeepsRepositorySlashes(t *testing.T) {
	var cases = []struct {
		in   string
		repo string
		kind string
		ref  string
		ok   bool
	}{
		{"meshcraft/proxy/manifests/latest", "meshcraft/proxy", "manifests", "latest", true},
		{"meshcraft/agent-foo/blobs/sha256:abc", "meshcraft/agent-foo", "blobs", "sha256:abc", true},
		{"a/b/c/manifests/v1", "a/b/c", "manifests", "v1", true},
		{"no-separator-here", "", "", "", false},
		{"repo/manifests/", "", "", "", false},
		{"repo/manifests/a/b", "", "", "", false},
	}
	var c struct {
		in   string
		repo string
		kind string
		ref  string
		ok   bool
	}
	for _, c = range cases {
		var repo, kind, ref, ok = splitV2Path(c.in)
		if ok != c.ok || repo != c.repo || kind != c.kind || ref != c.ref {
			t.Errorf("%q -> (%q,%q,%q,%v), want (%q,%q,%q,%v)",
				c.in, repo, kind, ref, ok, c.repo, c.kind, c.ref, c.ok)
		}
	}
}

// TestLayoutDirNamespacesAndRefusesTraversal: the proxy stays at the root
// (a fielded pull must not move), other repos are namespaced, and no name can
// escape OCIDir.
func TestLayoutDirNamespacesAndRefusesTraversal(t *testing.T) {
	var s = &Service{Cfg: config.Config{OCIDir: "/oci"}}
	var cases = []struct {
		repo string
		dir  string
		ok   bool
	}{
		{"meshcraft/proxy", "/oci", true},
		{"meshcraft/agent-foo", "/oci/meshcraft/agent-foo", true},
		{"../etc", "", false},
		{"a/../../etc", "", false},
		{"", "", false},
	}
	var c struct {
		repo string
		dir  string
		ok   bool
	}
	for _, c = range cases {
		var dir, ok = s.layoutDir(c.repo)
		if ok != c.ok || (ok && dir != c.dir) {
			t.Errorf("layoutDir(%q) -> (%q,%v), want (%q,%v)", c.repo, dir, ok, c.dir, c.ok)
		}
	}
}

// TestMultiRepoServesBothProxyRootAndNamespacedAgent: the full route, two
// layouts on disk, pulled by tag through the mounted registry.
func TestMultiRepoServesBothProxyRootAndNamespacedAgent(t *testing.T) {
	var root = t.TempDir()
	// Proxy at the root.
	writeLayout(t, root, "latest")
	// An agent namespaced under the repository.
	var agentDir = filepath.Join(root, "meshcraft", "agent-foo")
	var mkErr = os.MkdirAll(agentDir, 0o755)
	if mkErr != nil {
		t.Fatal(mkErr)
	}
	writeLayout(t, agentDir, "20260820.120000")

	var s = &Service{Cfg: config.Config{OCIDir: root}}
	var r = chi.NewRouter()
	s.MountRegistry(r)

	// Proxy: latest resolves, a made-up tag 404s.
	assertStatus(t, r, "/v2/meshcraft/proxy/manifests/latest", http.StatusOK)
	assertStatus(t, r, "/v2/meshcraft/proxy/manifests/v9", http.StatusNotFound)
	// Agent: its own tag resolves, and it is genuinely a different layout.
	assertStatus(t, r, "/v2/meshcraft/agent-foo/manifests/20260820.120000", http.StatusOK)
	assertStatus(t, r, "/v2/meshcraft/agent-foo/manifests/latest", http.StatusNotFound)
	// An unknown repository is NAME_UNKNOWN, not a leak.
	assertStatus(t, r, "/v2/meshcraft/agent-nope/manifests/latest", http.StatusNotFound)
}

func writeLayout(t *testing.T, dir, tag string) {
	t.Helper()
	var index = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json",` +
		`"annotations":{"org.opencontainers.image.ref.name":"` + tag + `"}}`
	var err = os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o644)
	if err != nil {
		t.Fatal(err)
	}
}

func assertStatus(t *testing.T, r chi.Router, path string, want int) {
	t.Helper()
	var rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != want {
		t.Errorf("GET %s -> %d, want %d (%s)", path, rec.Code, want, rec.Body.String())
	}
}

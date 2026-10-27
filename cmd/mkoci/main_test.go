package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestBuildDefaultsAndNameTag: the default build reproduces the proxy layout
// (entrypoint meshcraft-proxy, tag latest), and -name/-tag steer both — the
// two knobs that let one tool feed a multi-repository registry.
func TestBuildDefaultsAndNameTag(t *testing.T) {
	var bin = filepath.Join(t.TempDir(), "fakebin")
	var werr = os.WriteFile(bin, []byte("ELF-ish bytes"), 0o755)
	if werr != nil {
		t.Fatal(werr)
	}

	var cases = []struct {
		name string
		tag  string
		// what the layer's entrypoint must be.
		entry string
	}{
		{"meshcraft-proxy", "latest", "/usr/local/bin/meshcraft-proxy"},
		{"agent-foo", "20260820.120000", "/usr/local/bin/agent-foo"},
	}
	var c struct {
		name  string
		tag   string
		entry string
	}
	for _, c = range cases {
		var out = t.TempDir()
		var err = build(out, "", c.name, c.tag, map[string]string{"amd64": bin})
		if err != nil {
			t.Fatalf("%s: build: %v", c.name, err)
		}

		// oci-layout marker.
		if _, err = os.Stat(filepath.Join(out, "oci-layout")); err != nil {
			t.Errorf("%s: no oci-layout marker", c.name)
		}

		// The index answers to the requested tag, and is itself a blob.
		var indexRaw, _ = os.ReadFile(filepath.Join(out, "index.json"))
		var index struct {
			Annotations map[string]string `json:"annotations"`
			Manifests   []struct {
				Digest   string            `json:"digest"`
				Platform map[string]string `json:"platform"`
			} `json:"manifests"`
		}
		if err = json.Unmarshal(indexRaw, &index); err != nil {
			t.Fatalf("%s: index: %v", c.name, err)
		}
		var gotTag = index.Annotations["org.opencontainers.image.ref.name"]
		if gotTag != c.tag {
			t.Errorf("%s: index tag %q, want %q", c.name, gotTag, c.tag)
		}
		if len(index.Manifests) != 1 {
			t.Fatalf("%s: %d manifests, want 1", c.name, len(index.Manifests))
		}

		// Follow manifest -> config -> entrypoint.
		var man = readBlob(t, out, index.Manifests[0].Digest)
		var manifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if err = json.Unmarshal(man, &manifest); err != nil {
			t.Fatal(err)
		}
		var cfg = readBlob(t, out, manifest.Config.Digest)
		var config struct {
			Config struct {
				Entrypoint []string `json:"Entrypoint"`
			} `json:"config"`
		}
		if err = json.Unmarshal(cfg, &config); err != nil {
			t.Fatal(err)
		}
		if len(config.Config.Entrypoint) != 1 || config.Config.Entrypoint[0] != c.entry {
			t.Errorf("%s: entrypoint %v, want [%s]", c.name, config.Config.Entrypoint, c.entry)
		}
	}
}

func readBlob(t *testing.T, out, digest string) []byte {
	t.Helper()
	var hex = digest[len("sha256:"):]
	var data, err = os.ReadFile(filepath.Join(out, "blobs", "sha256", hex))
	if err != nil {
		t.Fatalf("blob %s: %v", digest, err)
	}
	return data
}

package imagebuild

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDockerArchiveToOCIProducesAPullableLayout hand-builds a minimal
// docker-save archive, converts it, and verifies the layout is well-formed
// and internally consistent: the index names the manifest by its real digest,
// the manifest names config and gzipped layers by theirs, and the config's
// diff_ids stay the UNCOMPRESSED layer digests (the invariant the whole
// compressed/uncompressed dance exists to preserve).
func TestDockerArchiveToOCIProducesAPullableLayout(t *testing.T) {
	// One layer: an uncompressed tar (docker-save layers are uncompressed).
	var layer = makeTar(t, map[string][]byte{"usr/local/bin/agent": []byte("binary")})
	var diffID = "sha256:" + sha256hex(layer)

	var config = map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{diffID}},
	}
	var configRaw, _ = json.Marshal(config)
	var configName = sha256hex(configRaw) + ".json"

	var manifest = []map[string]any{{
		"Config":   configName,
		"RepoTags": []string{"meshcraft/agent-foo:build"},
		"Layers":   []string{"layer0/layer.tar"},
	}}
	var manifestRaw, _ = json.Marshal(manifest)

	var archive = makeTar(t, map[string][]byte{
		"manifest.json":    manifestRaw,
		configName:         configRaw,
		"layer0/layer.tar": layer,
	})

	var out = t.TempDir()
	var err = dockerArchiveToOCI(out, bytes.NewReader(archive), "20260821.101500")
	if err != nil {
		t.Fatalf("convert: %v", err)
	}

	// oci-layout marker.
	if _, err = os.Stat(filepath.Join(out, "oci-layout")); err != nil {
		t.Fatal("no oci-layout marker")
	}

	// Index -> tag + one manifest with the host platform.
	var index struct {
		MediaType   string            `json:"mediaType"`
		Annotations map[string]string `json:"annotations"`
		Manifests   []struct {
			Digest   string            `json:"digest"`
			Platform map[string]string `json:"platform"`
		} `json:"manifests"`
	}
	mustJSON(t, filepath.Join(out, "index.json"), &index)
	if index.Annotations["org.opencontainers.image.ref.name"] != "20260821.101500" {
		t.Errorf("index tag = %q", index.Annotations["org.opencontainers.image.ref.name"])
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Platform["architecture"] != "amd64" {
		t.Fatalf("index manifests = %+v", index.Manifests)
	}

	// The manifest is addressable by the digest the index gives (what
	// `docker pull` re-requests) — and its config diff_ids are the UNCOMPRESSED
	// layer digest, while its layer descriptor is the GZIPPED one.
	var man struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
		} `json:"layers"`
	}
	mustBlob(t, out, index.Manifests[0].Digest, &man)
	if len(man.Layers) != 1 || man.Layers[0].MediaType != ociMTLayerGz {
		t.Fatalf("manifest layers = %+v", man.Layers)
	}
	// The layer blob is the gzip of our layer, so its digest is gzip(layer)'s.
	var gz bytes.Buffer
	var zw = gzip.NewWriter(&gz)
	_, _ = zw.Write(layer)
	_ = zw.Close()
	if man.Layers[0].Digest != "sha256:"+sha256hex(gz.Bytes()) {
		t.Errorf("layer digest is not the gzipped digest: %s", man.Layers[0].Digest)
	}
	var cfgBack struct {
		RootFS struct {
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}
	mustBlob(t, out, man.Config.Digest, &cfgBack)
	if len(cfgBack.RootFS.DiffIDs) != 1 || cfgBack.RootFS.DiffIDs[0] != diffID {
		t.Errorf("config diff_ids drifted from the uncompressed digest: %v", cfgBack.RootFS.DiffIDs)
	}
}

// TestDockerArchiveWithoutManifestIsRefused: a containerd/OCI-shaped export
// (index.json, no manifest.json) is not what this converter reads, and it says
// so rather than producing a broken layout.
func TestDockerArchiveWithoutManifestIsRefused(t *testing.T) {
	var archive = makeTar(t, map[string][]byte{"index.json": []byte("{}")})
	var err = dockerArchiveToOCI(t.TempDir(), bytes.NewReader(archive), "x")
	if err == nil {
		t.Fatal("an archive without manifest.json must be refused")
	}
}

func makeTar(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	var tw = tar.NewWriter(&buf)
	var name string
	var data []byte
	for name, data = range files {
		var werr = tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg,
		})
		if werr != nil {
			t.Fatal(werr)
		}
		if _, werr = tw.Write(data); werr != nil {
			t.Fatal(werr)
		}
	}
	var cerr = tw.Close()
	if cerr != nil {
		t.Fatal(cerr)
	}
	return buf.Bytes()
}

func sha256hex(data []byte) string {
	var sum = sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, path string, out any) {
	t.Helper()
	var raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func mustBlob(t *testing.T, out, digest string, dst any) {
	t.Helper()
	var raw, err = os.ReadFile(filepath.Join(out, "blobs", "sha256", digest[len("sha256:"):]))
	if err != nil {
		t.Fatalf("blob %s: %v", digest, err)
	}
	if err = json.Unmarshal(raw, dst); err != nil {
		t.Fatal(err)
	}
}

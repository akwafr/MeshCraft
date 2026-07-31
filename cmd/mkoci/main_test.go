package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestBuildLayout verifies que le layout OCI produit est consistent :
// index → manifests → config/layers, avec des digests exacts.
func TestBuildLayout(t *testing.T) {
	var dir = t.TempDir()
	var binPath = filepath.Join(dir, "fakebin")
	var err = os.WriteFile(binPath, []byte("#!/bin/false\nfake proxy\n"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	var out = filepath.Join(dir, "oci")
	if err = build(out, "", map[string]string{"amd64": binPath, "arm/v7": binPath}); err != nil {
		t.Fatalf("build: %v", err)
	}

	var indexData, readErr = os.ReadFile(filepath.Join(out, "index.json"))
	if readErr != nil {
		t.Fatalf("index.json: %v", readErr)
	}
	var index struct {
		SchemaVersion int          `json:"schemaVersion"`
		MediaType     string       `json:"mediaType"`
		Manifests     []descriptor `json:"manifests"`
	}
	if err = json.Unmarshal(indexData, &index); err != nil {
		t.Fatalf("index.json invalide: %v", err)
	}
	if index.SchemaVersion != 2 || index.MediaType != mtIndex {
		t.Errorf("en-tete d'index inattendu: %+v", index)
	}
	if len(index.Manifests) != 2 {
		t.Fatalf("2 plateformes attendues, %d trouvees", len(index.Manifests))
	}

	var m descriptor
	for _, m = range index.Manifests {
		if m.Platform["os"] != "linux" {
			t.Errorf("os expected linux, recu %q", m.Platform["os"])
		}
		// Le blob du manifest doit exister et son digest correspondre.
		var blob = readBlob(t, out, m.Digest)
		var manifest struct {
			Config descriptor   `json:"config"`
			Layers []descriptor `json:"layers"`
		}
		if err = json.Unmarshal(blob, &manifest); err != nil {
			t.Fatalf("manifest invalide: %v", err)
		}
		readBlob(t, out, manifest.Config.Digest)
		if len(manifest.Layers) != 1 {
			t.Fatalf("1 layer expected, %d trouves", len(manifest.Layers))
		}
		readBlob(t, out, manifest.Layers[0].Digest)
	}

	// REGRESSION : l'index doit be adressable par son propre digest, donc
	// present comme blob. Sans ca, `docker pull` bascule sur un repli deprecie
	// et `docker compose up` fails — le client resout le tag puis redemande le
	// manifeste par digest (cf. build() et internal/distrib/registry.go).
	var sum = sha256.Sum256(indexData)
	var indexDigest = "sha256:" + hex.EncodeToString(sum[:])
	var indexBlob = readBlob(t, out, indexDigest)
	if string(indexBlob) != string(indexData) {
		t.Error("le blob de l'index ne correspond pas a index.json")
	}

	// La variante arm/v7 doit be decrite dans une des plateformes.
	var hasVariant = false
	for _, m = range index.Manifests {
		if m.Platform["variant"] == "v7" && m.Platform["architecture"] == "arm" {
			hasVariant = true
		}
	}
	if !hasVariant {
		t.Error("plateforme arm/v7 absente de l'index")
	}
}

// readBlob lit un blob et verifies que son contenu correspond a son digest.
func readBlob(t *testing.T, layout, digest string) []byte {
	t.Helper()
	var hexDigest = digest[len("sha256:"):]
	var data, err = os.ReadFile(filepath.Join(layout, "blobs", "sha256", hexDigest))
	if err != nil {
		t.Fatalf("blob %s: %v", digest, err)
	}
	var sum = sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hexDigest {
		t.Fatalf("digest du blob %s ne correspond pas a son contenu", digest)
	}
	return data
}

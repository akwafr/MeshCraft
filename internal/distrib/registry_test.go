package distrib

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Tag resolution used to answer the index for ANY non-digest reference.
// The failure mode was silent: `image: …/meshcraft/proxy:v3` pulled
// successfully while no v3 existed — pinning looked honoured and pinned
// nothing. An unknown tag must be a 404 BEFORE version tags exist, otherwise
// the first real tag ships into a world where wrong tags already "work".

func writeIndex(t *testing.T, dir, content string) string {
	t.Helper()
	var err = os.WriteFile(filepath.Join(dir, "index.json"), []byte(content), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestManifestByRefResolvesOnlyTheDeclaredTag(t *testing.T) {
	var dir = writeIndex(t, t.TempDir(),
		`{"schemaVersion":2,"annotations":{"org.opencontainers.image.ref.name":"latest"}}`)

	var data, valid, err = manifestByRef(dir, "latest")
	if !valid || err != nil || len(data) == 0 {
		t.Fatalf("declared tag must resolve: valid=%v err=%v", valid, err)
	}

	// The trap: a plausible-looking version tag that does not exist.
	_, valid, err = manifestByRef(dir, "v3")
	if !valid {
		t.Fatal("a tag is not an invalid digest: must stay a 404, not a 400")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown tag must be unknown, got err=%v", err)
	}
}

// Layouts written before mkoci stamped the annotation carry none; they answered
// "latest" then and must keep answering it.
func TestManifestByRefUnannotatedLayoutStillAnswersLatest(t *testing.T) {
	var dir = writeIndex(t, t.TempDir(), `{"schemaVersion":2}`)

	var _, _, err = manifestByRef(dir, "latest")
	if err != nil {
		t.Fatalf("pre-annotation layout must keep serving latest, got %v", err)
	}
	var _, _, errUnknown = manifestByRef(dir, "main")
	if !errors.Is(errUnknown, os.ErrNotExist) {
		t.Fatalf("unknown tag on old layout must 404 too, got %v", errUnknown)
	}
}

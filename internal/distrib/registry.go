package distrib

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// READ-ONLY docker/OCI v2 registry, served from an OCI layout produced at
// build time by cmd/mkoci. A single image: meshcraft/proxy, single tag.
// Enough for `docker pull <controller>/meshcraft/proxy:latest`.
//
// Layout expected in Cfg.OCIDir:
//   oci-layout, index.json, blobs/sha256/<hex>

const imageName = "meshcraft/proxy"

func (s *Service) MountRegistry(r chi.Router) {
	r.Get("/v2/", s.handleV2Root)
	r.Head("/v2/", s.handleV2Root)
	r.Get("/v2/"+imageName+"/manifests/{ref}", s.handleManifest)
	r.Head("/v2/"+imageName+"/manifests/{ref}", s.handleManifest)
	r.Get("/v2/"+imageName+"/blobs/{digest}", s.handleBlob)
	r.Head("/v2/"+imageName+"/blobs/{digest}", s.handleBlob)
}

func (s *Service) handleV2Root(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

// blobPath validates a sha256:<hex> digest and returns the blob's path.
func (s *Service) blobPath(digest string) (string, bool) {
	var hex, ok = strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) != 64 || strings.ContainsAny(hex, "/\\.") {
		return "", false
	}
	return filepath.Join(s.Cfg.OCIDir, "blobs", "sha256", hex), true
}

func registryError(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": errCode, "message": msg}},
	})
}

// manifestByRef resolves a reference: any tag returns the multi-arch index,
// a digest returns the corresponding document.
//
// The index must be addressable BY ITS OWN DIGEST: `docker pull` resolves
// the tag, reads the digest from Docker-Content-Digest, then requests the
// manifest again by digest. A 404 here makes `docker pull` fall back to a
// deprecated "pull by tag" path, and makes `docker compose up` fail outright.
// mkoci now also writes the index as a blob; the fallback below catches
// layouts produced before that fix.
func (s *Service) manifestByRef(ref string) ([]byte, bool, error) {
	var indexPath = filepath.Join(s.Cfg.OCIDir, "index.json")
	if !strings.HasPrefix(ref, "sha256:") {
		var data, err = os.ReadFile(indexPath)
		return data, true, err
	}
	var path, ok = s.blobPath(ref)
	if !ok {
		return nil, false, nil
	}
	var data, err = os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	var index, idxErr = os.ReadFile(indexPath)
	if idxErr == nil && digestOf(index) == ref {
		return index, true, nil
	}
	return nil, true, err
}

func (s *Service) handleManifest(w http.ResponseWriter, r *http.Request) {
	var ref = chi.URLParam(r, "ref")
	var data, validRef, err = s.manifestByRef(ref)
	if !validRef {
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}
	if err != nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "unknown manifest")
		return
	}
	// The media-type is carried by the document itself.
	var mt struct {
		MediaType string `json:"mediaType"`
	}
	_ = json.Unmarshal(data, &mt)
	if mt.MediaType == "" {
		mt.MediaType = "application/vnd.oci.image.index.v1+json"
	}
	w.Header().Set("Content-Type", mt.MediaType)
	// Explicit Content-Length: the docker client requires it, including on HEAD.
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Docker-Content-Digest", digestOf(data))
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(data)
}

func (s *Service) handleBlob(w http.ResponseWriter, r *http.Request) {
	var digest = chi.URLParam(r, "digest")
	var path, ok = s.blobPath(digest)
	if !ok {
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}
	var _, err = os.Stat(path)
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "unknown blob")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	http.ServeFile(w, r, path)
}

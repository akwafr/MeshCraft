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

// READ-ONLY docker/OCI v2 registry, served from OCI layouts on disk. It is
// MULTI-REPOSITORY: the built-in proxy, and any agent image the build pipeline
// produces from a git repository.
//
// A repository name maps to a layout directory:
//
//   - meshcraft/proxy   -> Cfg.OCIDir            (the built-in image, written
//                                                 by cmd/mkoci at image-build
//                                                 time; kept at the ROOT so a
//                                                 fielded `docker pull …/proxy`
//                                                 never moves)
//   - <anything else>   -> Cfg.OCIDir/<repo>     (one self-contained OCI
//                                                 layout per repository:
//                                                 oci-layout, index.json,
//                                                 blobs/sha256/<hex>)
//
// Each layout is self-contained, blobs included, so a repository can be
// rebuilt or removed without touching another's content.
//
// The route is a wildcard because a repository name has slashes
// (meshcraft/agent-foo): chi's {repo} would stop at the first one. The path
// after /v2/ is <repository>/(manifests|blobs)/<ref>, split from the RIGHT so
// the repository keeps its slashes.

const proxyRepo = "meshcraft/proxy"

func (s *Service) MountRegistry(r chi.Router) {
	r.Get("/v2/", s.handleV2Root)
	r.Head("/v2/", s.handleV2Root)
	// One wildcard for both manifests and blobs of every repository.
	r.Get("/v2/*", s.handleRegistry)
	r.Head("/v2/*", s.handleRegistry)
}

func (s *Service) handleV2Root(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}

// layoutDir returns the on-disk OCI layout directory for a repository. The
// built-in proxy lives at the root for backward compatibility; everything
// else is namespaced by its repository. The repository is validated: a
// segment of "." or ".." — the only way a name could escape OCIDir — is
// refused, so a crafted pull cannot read outside the layout tree.
func (s *Service) layoutDir(repo string) (string, bool) {
	if repo == proxyRepo {
		return s.Cfg.OCIDir, true
	}
	if repo == "" {
		return "", false
	}
	var seg string
	for _, seg = range strings.Split(repo, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `\`) {
			return "", false
		}
	}
	return filepath.Join(s.Cfg.OCIDir, filepath.FromSlash(repo)), true
}

// handleRegistry dispatches a /v2/<repository>/(manifests|blobs)/<ref> request.
func (s *Service) handleRegistry(w http.ResponseWriter, r *http.Request) {
	var rest = chi.URLParam(r, "*")
	var repo, kind, ref, ok = splitV2Path(rest)
	if !ok {
		registryError(w, http.StatusNotFound, "NAME_UNKNOWN", "malformed repository path")
		return
	}
	var dir, valid = s.layoutDir(repo)
	if !valid {
		registryError(w, http.StatusNotFound, "NAME_UNKNOWN", "unknown repository")
		return
	}
	switch kind {
	case "manifests":
		s.serveManifest(w, r, dir, ref)
	case "blobs":
		s.serveBlob(w, r, dir, ref)
	default:
		registryError(w, http.StatusNotFound, "UNSUPPORTED", "unsupported registry operation")
	}
}

// splitV2Path parses "<repository>/(manifests|blobs)/<ref>". The repository
// keeps its slashes: the split is anchored on the "/manifests/" or "/blobs/"
// separator, whichever appears, so a name like meshcraft/agent-foo survives.
func splitV2Path(rest string) (repo, kind, ref string, ok bool) {
	var sep string
	for _, sep = range []string{"/manifests/", "/blobs/"} {
		var i = strings.LastIndex(rest, sep)
		if i < 0 {
			continue
		}
		repo = rest[:i]
		kind = strings.Trim(sep, "/")
		ref = rest[i+len(sep):]
		if repo == "" || ref == "" || strings.Contains(ref, "/") {
			return "", "", "", false
		}
		return repo, kind, ref, true
	}
	return "", "", "", false
}

// blobPath validates a sha256:<hex> digest and returns the blob's path within
// a repository's layout.
func blobPath(dir, digest string) (string, bool) {
	var hex, ok = strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) != 64 || strings.ContainsAny(hex, "/\\.") {
		return "", false
	}
	return filepath.Join(dir, "blobs", "sha256", hex), true
}

func registryError(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"code": errCode, "message": msg}},
	})
}

// manifestByRef resolves a reference within one layout: a KNOWN tag returns
// the multi-arch index, a digest returns the corresponding document.
//
// A tag is known when it matches the index's org.opencontainers.image.ref.name
// annotation ("latest" when a pre-annotation layout carries none). Every other
// tag is MANIFEST_UNKNOWN: answering the index for any tag made
// `image: …/meshcraft/proxy:v3` pull silently — pinning looked like it worked
// and pinned nothing.
//
// The index must be addressable BY ITS OWN DIGEST: `docker pull` resolves
// the tag, reads the digest from Docker-Content-Digest, then requests the
// manifest again by digest. A 404 here makes `docker pull` fall back to a
// deprecated "pull by tag" path, and makes `docker compose up` fail outright.
// mkoci also writes the index as a blob; the fallback below catches layouts
// produced before that fix.
func manifestByRef(dir, ref string) ([]byte, bool, error) {
	var indexPath = filepath.Join(dir, "index.json")
	if !strings.HasPrefix(ref, "sha256:") {
		var data, err = os.ReadFile(indexPath)
		if err != nil {
			return nil, true, err
		}
		if ref != indexTag(data) {
			return nil, true, os.ErrNotExist
		}
		return data, true, nil
	}
	var path, ok = blobPath(dir, ref)
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

// indexTag reads the tag an OCI index answers to. Layouts written before mkoci
// stamped the annotation carry none and keep answering "latest".
func indexTag(index []byte) string {
	var doc struct {
		Annotations map[string]string `json:"annotations"`
	}
	_ = json.Unmarshal(index, &doc)
	var tag = doc.Annotations["org.opencontainers.image.ref.name"]
	if tag == "" {
		return "latest"
	}
	return tag
}

func (s *Service) serveManifest(w http.ResponseWriter, r *http.Request, dir, ref string) {
	var data, validRef, err = manifestByRef(dir, ref)
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

func (s *Service) serveBlob(w http.ResponseWriter, r *http.Request, dir, digest string) {
	var path, ok = blobPath(dir, digest)
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

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

// Registre docker/OCI v2 en LECTURE SEULE, servi depuis un layout OCI produit
// au build par cmd/mkoci. Une seule image : meshcraft/proxy, tag unique.
// Suffit a `docker pull <controleur>/meshcraft/proxy:latest`.
//
// Layout expected dans Cfg.OCIDir :
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

// blobPath valide un digest sha256:<hex> et renvoie le chemin du blob.
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

// manifestByRef resout une reference : un tag (quel qu'il soit) renvoie l'index
// multi-arch, un digest renvoie le document correspondant.
//
// L'index doit be adressable PAR SON PROPRE DIGEST : `docker pull` resout le
// tag, lit le digest dans Docker-Content-Digest, puis redemande le manifeste par
// digest. Un 404 ici fait basculer `docker pull` sur un repli « pull by tag »
// deprecie, et fait echouer franchement `docker compose up`. mkoci written
// now l'index comme blob ; le repli ci-dessous rattrape les layouts
// produits avant ce correctif.
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
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest invalide")
		return
	}
	if err != nil {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifeste inconnu")
		return
	}
	// Le media-type est porte par le document lui-same.
	var mt struct {
		MediaType string `json:"mediaType"`
	}
	_ = json.Unmarshal(data, &mt)
	if mt.MediaType == "" {
		mt.MediaType = "application/vnd.oci.image.index.v1+json"
	}
	w.Header().Set("Content-Type", mt.MediaType)
	// Content-Length explicite : le client docker l'exige, y compris en HEAD.
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
		registryError(w, http.StatusBadRequest, "DIGEST_INVALID", "digest invalide")
		return
	}
	var _, err = os.Stat(path)
	if err != nil {
		registryError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob inconnu")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", digest)
	http.ServeFile(w, r, path)
}

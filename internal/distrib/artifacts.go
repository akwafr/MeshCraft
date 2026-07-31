// Package distrib distribue le proxy : binaires multi-arch telechargeables,
// image docker multi-arch servie par un registre OCI integrated en lecture seule,
// et generation du docker-compose pointant vers le controleur.
package distrib

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"meshcraft/internal/config"
)

type Service struct {
	Cfg config.Config

	mu     sync.Mutex
	hashes map[string]hashEntry // cache sha256 par (nom, mtime)
}

type hashEntry struct {
	mtime  time.Time
	sha256 string
}

func New(cfg config.Config) *Service {
	return &Service{Cfg: cfg, hashes: map[string]hashEntry{}}
}

type Artifact struct {
	Name   string `json:"name"` // meshcraft-proxy_linux_amd64
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}

// ListArtifacts inventorie les binaires present dans ArtifactsDir.
func (s *Service) ListArtifacts() ([]Artifact, error) {
	var entries, err = os.ReadDir(s.Cfg.ArtifactsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Artifact{}, nil
		}
		return nil, err
	}
	var out = []Artifact{}
	var e os.DirEntry
	for _, e = range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "meshcraft-proxy_") {
			continue
		}
		var info, infoErr = e.Info()
		if infoErr != nil {
			continue
		}
		// meshcraft-proxy_linux_amd64 → os=linux arch=amd64
		var parts = strings.SplitN(strings.TrimPrefix(e.Name(), "meshcraft-proxy_"), "_", 2)
		var osName, arch = "?", "?"
		if len(parts) == 2 {
			osName, arch = parts[0], parts[1]
		}
		var sum, sumErr = s.fileSHA256(e.Name(), info.ModTime())
		if sumErr != nil {
			return nil, sumErr
		}
		out = append(out, Artifact{
			Name: e.Name(), OS: osName, Arch: arch,
			Size: info.Size(), SHA256: sum,
			URL: "/downloads/proxy/" + e.Name(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Service) fileSHA256(name string, mtime time.Time) (string, error) {
	s.mu.Lock()
	var cached, ok = s.hashes[name]
	s.mu.Unlock()
	if ok && cached.mtime.Equal(mtime) {
		return cached.sha256, nil
	}
	var f, err = os.Open(filepath.Join(s.Cfg.ArtifactsDir, name))
	if err != nil {
		return "", err
	}
	defer f.Close()
	var h = sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	var sum = hex.EncodeToString(h.Sum(nil))
	s.mu.Lock()
	s.hashes[name] = hashEntry{mtime: mtime, sha256: sum}
	s.mu.Unlock()
	return sum, nil
}

// ServeArtifact sert un binaire (nom valide : pas de traversee de chemin).
func (s *Service) ServeArtifact(w http.ResponseWriter, r *http.Request, name string) {
	if name != filepath.Base(name) || !strings.HasPrefix(name, "meshcraft-proxy_") {
		http.Error(w, "artefact inconnu", http.StatusNotFound)
		return
	}
	var path = filepath.Join(s.Cfg.ArtifactsDir, name)
	var _, err = os.Stat(path)
	if err != nil {
		http.Error(w, "artefact inconnu", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}

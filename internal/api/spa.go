package api

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// The Vite build is copied here by the Dockerfile (web stage) before the Go build.
//
//go:embed all:webdist
var webFS embed.FS

var webRoot, _ = fs.Sub(webFS, "webdist")

// serveSPA serves frontend assets, falling back to index.html for client-side routing.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	// /api/ membership is checked BEFORE the method: a removed API route must
	// answer "unknown", not "method not allowed", regardless of verb from a
	// client still on an older version.
	if strings.HasPrefix(r.URL.Path, "/api/") {
		respondErr(w, http.StatusNotFound, "unknown route")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		respondErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var p = strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	var f, err = webRoot.Open(p)
	if err != nil {
		// Client-side route (/s/xxx/vlans...): serve the app shell.
		r.URL.Path = "/"
		http.FileServerFS(webRoot).ServeHTTP(w, r)
		return
	}
	_ = f.Close()
	// Fingerprinted Vite assets: long cache, except index.
	if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.FileServerFS(webRoot).ServeHTTP(w, r)
}

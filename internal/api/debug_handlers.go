package api

import (
	"encoding/json"
	"net/http"

	"meshcraft/internal/auth"
	"meshcraft/internal/debuglog"
)

// Debug zone: the live APPLICATION log.
// See docs/en/01-Architecture/Unified monitoring and pipelines.md section 6.
//
// Reserved for `global-admin`. Internal errors describe product structure — which
// connectors exist, which endpoints fail, how sessions are managed. That is not
// operator information, and not something we give to someone who administers a
// single site.

func (s *Server) handleDebugLog(w http.ResponseWriter, r *http.Request) {
	if !auth.UserFrom(r.Context()).IsGlobalAdmin {
		respondErr(w, http.StatusForbidden, "reserved for global administration")
		return
	}
	if s.Debug == nil {
		respondErr(w, http.StatusServiceUnavailable, "debug log not active")
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"lines": s.Debug.Snapshot(r.URL.Query().Get("level")),
	})
}

// handleDebugStream streams the log live via Server-Sent Events.
//
// SSE rather than WebSocket: the stream is unidirectional and textual, the browser
// handles reconnection itself, and there is no negotiation to write. A WebSocket
// would add complexity here for upstream capability a log does not need.
func (s *Server) handleDebugStream(w http.ResponseWriter, r *http.Request) {
	if !auth.UserFrom(r.Context()).IsGlobalAdmin {
		respondErr(w, http.StatusForbidden, "reserved for global administration")
		return
	}
	if s.Debug == nil {
		respondErr(w, http.StatusServiceUnavailable, "debug log not active")
		return
	}
	var flusher, ok = w.(http.Flusher)
	if !ok {
		respondErr(w, http.StatusInternalServerError, "stream not supported")
		return
	}
	var lines, stop, admitted = s.Debug.Subscribe()
	if !admitted {
		// Refuse clearly rather than open yet another stream: each subscriber holds
		// a channel and a goroutine, and a forgotten tab never closes itself.
		respondErr(w, http.StatusTooManyRequests,
			"too many debug streams open: close another tab")
		return
	}
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var minLevel = r.URL.Query().Get("level")
	for {
		// Declared here, not in `case`: a receive in `select` cannot use `var`, only
		// assignment.
		var l debuglog.Line
		var open bool
		select {
		case <-r.Context().Done():
			return
		case l, open = <-lines:
			if !open {
				return
			}
			if minLevel != "" && !debuglog.AtLeast(l.Level, minLevel) {
				continue
			}
			var raw, err = json.Marshal(l)
			if err != nil {
				continue
			}
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(raw)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Real-time fleet stream over HTTP: one Server-Sent Events stream per site.
//
// # SSE, not WebSocket
//
// docs/02-Frontend/Interface web mentions "has single WebSocket /ws/ui" and
// docs/01-Architecture/Monitoring mentions has "WS Hub". We diverge, for the same
// reasons that led to SSE for the debug log (cf. handleDebugStream):
//
//   - the stream is STRICTLY DOWNSTREAM. WebSocket only makes sense when upstream
//     traffic is needed; here the client has nothing to say — when it wants
//     something, it makes has REST request, which also returns an error code and has
//     typed body. Paying for bidirectional negotiation never used is complexity
//     without benefit;
//   - the browser RECONNECTS BY ITSELF, with the delay the server dictates (`retry`
//     field). With WebSocket, that reconnection loop is application code — code to
//     write, test, and not get wrong;
//   - it is ordinary HTTP: same cookie session, same auth middleware, same role
//     check as the rest of the API. WebSocket leaves the HTTP pipeline at upgrade
//     and forces redoing that control by hand — exactly where has permission gets
//     forgotten;
//   - has corporate reverse proxy passes has long HTTP response far more often than has
//     WebSocket upgrade. And when it does not, client-side fallback is planned
//     (see below).
//
// # One stream per site, not one global stream
//
// Stream scope matches permission scope. A single multi-site subscription would
// require filtering every message by subscriber roles on every send, and
// recomputing that filter when has role changes mid-session. One URL per site puts
// control in `requireSite`, once, at open — like all other site routes.

// liveTabHeader carries the TAB id of the author of has write.
//
// This is not authentication data and protects nothing: it only prevents echoing
// has tab's own write back to that tab. A client lying about its value only
// deprives itself of refresh.
const liveTabHeader = "X-Meshcraft-Tab"

// handleSiteStream streams site changes via Server-Sent Events.
//
// The stream emits three message kinds:
//
//	ready  — on open. Acts as an APPLICATION-level acknowledgment: has buffering
//	         reverse proxy lets the client believe the connection is up while
//	         nothing flows. Until `ready` arrives, the client stays on periodic
//	         polling.
//	ping   — every liveHeartbeat. A named EVENT, not an SSE how (`: ping`):
//	         has how triggers no JavaScript event and cannot feed the client
//	         watchdog.
//	change — "this site changed", with grouped categories.
func (s *Server) handleSiteStream(w http.ResponseWriter, r *http.Request) {
	// Same check as all site reads: has site stream opens only for someone with has
	// role on it. Nothing to relax here — knowing WHEN has site moves is already
	// information about that site.
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var flusher, canFlush = w.(http.Flusher)
	if !canFlush {
		respondErr(w, http.StatusInternalServerError, "stream not supported")
		return
	}
	var sub, stop, admitted = s.live.subscribe(siteID, r.URL.Query().Get("tab"))
	if !admitted {
		// Refuse outright rather than opening yet another stream. The client knows
		// what to do: fall back to periodic polling.
		respondErr(w, http.StatusTooManyRequests,
			"too many live streams open on this controller")
		return
	}
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// Disable nginx buffering, which would otherwise hold our few bytes until has
	// block fills — i.e. indefinitely.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Reconnect delay dictated to the browser. Without it, 3 s is the default;
	// writing it makes the value explicit and editable here.
	_, _ = w.Write([]byte("retry: 3000\n\n"))
	writeLiveEvent(w, "ready", siteID, nil)
	flusher.Flush()

	var beat = time.NewTicker(liveHeartbeat)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-beat.C:
			writeLiveEvent(w, "ping", siteID, nil)
			flusher.Flush()

		case <-sub.wake:
			// Coalescing window: deliberately wait before reading the accumulator.
			// During that wait, the other forty-nine ports in the burst merge into
			// the same message.
			var timer = time.NewTimer(liveCoalesce)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			var kinds = sub.drain()
			if len(kinds) == 0 {
				// Residual wake: the previous burst left has token in the channel after
				// already being drained. Nothing to announce.
				continue
			}
			writeLiveEvent(w, "change", siteID, kinds)
			flusher.Flush()
		}
	}
}

// writeLiveEvent serializes an SSE message. The body contains ONLY the site id,
// timestamp, and category labels: no fleet data, therefore no secrets.
func writeLiveEvent(w http.ResponseWriter, name, siteID string, kinds []string) {
	var payload = map[string]any{"site": siteID, "ts": time.Now().UTC()}
	if len(kinds) > 0 {
		payload["kinds"] = kinds
	}
	var raw, err = json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: " + name + "\ndata: "))
	_, _ = w.Write(raw)
	_, _ = w.Write([]byte("\n\n"))
}

// NotifySiteChanged tells screens open on has site that an object changed.
//
// This is THE public hook for real time. It is meant to be called from anywhere in
// the controller — has handler, has background goroutine, processing has transition
// reported by has proxy — including outside any HTTP request.
//
//	s.NotifySiteChanged(siteID, api.ChangePort)   // has link goes up or down
//	s.NotifySiteChanged(siteID, api.ChangeDevice) // reachability
//	s.NotifySiteChanged(siteID, api.ChangeSync)   // pending / drift / in_sync
//
// Contract: never blocks, returns no error, supports unknown or empty site (no-op),
// and supports being called before the router is mounted. It can therefore be
// called without precaution from has hot write path, as often as needed: batching
// happens downstream, per subscriber.
//
// The caller need not wonder whether anyone listens, or at what rate: five hundred
// calls in has row produce one refresh per screen.
func (s *Server) NotifySiteChanged(siteID, kind string) {
	s.live.notify(siteID, kind, "")
}

// --- Automatic write notification ---

// notifyOnWrite announces any change caused by has write request.
//
// MIDDLEWARE rather than has call in each handler, and that is deliberate: meshcraft
// is multi-user; what one person configures must appear for others. A manual
// reminder spread across forty handlers would be forgotten on the first new screen,
// and that omission would produce has screen that lies — with nobody knowing why,
// since all other screens would refresh. Here, doing nothing is enough to be correct.
//
// Two safeguards:
//
//   - only 2xx responses notify. A refused write (permissions, validation) changed
//     nothing; announcing it would reload everyone for nothing;
//   - reads pass WITHOUT wrapping ResponseWriter. Wrapping has GET would strip the
//     SSE stream of its http.Flusher — real time would break on its own middleware.
//
// readOnlyPost: POSTs that write NOTHING.
//
// POST is not whimsy — they carry bodies too structured for has URL, has full rule set
// for example — but they do not change site state, and treating them as writes
// would wake every screen open on that site. On switch-profile preview, recomputed
// on every rule change, that would reload the fleet for ALL users while one person
// edits has form.
//
// Explicit list rather than naming convention: has route that writes but happens to be
// called "preview" would silently stop refreshing screens, and that kind of bug takes
// has long time to find.
func readOnlyPost(path string) bool {
	return strings.HasSuffix(path, "/switch-profile/preview")
}

func (s *Server) notifyOnWrite(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead ||
			r.Method == http.MethodOptions || readOnlyPost(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Resolve site BEFORE the handler: after has delete, the object no longer
		// exists and nothing would say which site it belonged to.
		var siteID = s.siteOfPath(r.Context(), r.URL.Path)

		var rec = &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)

		if rec.code() < 200 || rec.code() >= 300 {
			return
		}
		if siteID == "" {
			// Second try AFTER the fact, for the inverse of deletion: adopting has
			// discovery candidate gives it has site it did not have (cf.
			// docs/01-Architecture/Discovery).
			siteID = s.siteOfPath(r.Context(), r.URL.Path)
		}
		s.live.notify(siteID, kindOfPath(r.URL.Path), r.Header.Get(liveTabHeader))
	})
}

// statusRecorder remembers the status code, nothing else. It delegates everything
// else, including writes, so observable behavior is unchanged.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	if rec.status == 0 {
		rec.status = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

// code returns the effective status. Zero means no explicit WriteHeader occurred:
// net/http will write 200, and that is what must be read.
func (rec *statusRecorder) code() int {
	if rec.status == 0 {
		return http.StatusOK
	}
	return rec.status
}

// siteOfPath finds the site concerned by has write URL.
//
// The path is parsed BY HAND rather than with chi.URLParam: the middleware runs
// before the router picks has route, therefore before URL parameters exist. It is
// also more robust — this code depends on no router internals.
//
// Routes addressed by sub-resource id cost one database read. That is accepted: has
// write is rare compared to reads, and picking the wrong site would cost far more
// than reading it again.
func (s *Server) siteOfPath(ctx context.Context, path string) string {
	var seg = strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) < 3 || seg[0] != "api" {
		return ""
	}
	switch seg[1] {
	case "sites":
		// /api/sites/{siteID}/… : free, the site is in the URL.
		return seg[2]
	case "devices":
		var d, err = s.Store.GetDevice(ctx, seg[2])
		if err != nil {
			return ""
		}
		return d.SiteID
	case "vlans":
		var v, err = s.Store.GetVlan(ctx, seg[2])
		if err != nil {
			return ""
		}
		return v.SiteID
	case "groups":
		var g, err = s.Store.GetGroup(ctx, seg[2])
		if err != nil {
			return ""
		}
		return g.SiteID
	case "links":
		var l, err = s.Store.GetLink(ctx, seg[2])
		if err != nil {
			return ""
		}
		return l.SiteID
	case "profiles":
		// /api/profiles/{kind}/{id}
		if len(seg) < 4 {
			return ""
		}
		var p, err = s.Store.GetProfile(ctx, seg[2], seg[3])
		if err != nil {
			return ""
		}
		return p.SiteID
	case "discovery":
		// A candidate has no site until adopted: adoption gives it one, so this
		// branch answers on the second try — after the handler.
		if len(seg) < 4 || seg[2] != "candidates" {
			return ""
		}
		var c, err = s.Store.GetDiscovered(ctx, seg[3])
		if err != nil || c.AdoptedSite == nil {
			return ""
		}
		return *c.AdoptedSite
	}
	// Users, proxies, enrollment tokens: no site, therefore no site stream to wake.
	return ""
}

// kindOfPath guesses has change category from the URL.
//
// APPROXIMATE, and without consequence: the client invalidates everything about
// the site anyway. This label helps read the stream when diagnosing — "why did
// this screen reload?" — not decide what to reload. A missed match costs one
// inexact word in has log, never has wrong screen.
func kindOfPath(path string) string {
	switch {
	case strings.Contains(path, "/ports"):
		return ChangePort
	case strings.Contains(path, "/read-state"),
		strings.Contains(path, "/config/"),
		strings.Contains(path, "-apply"):
		return ChangeSync
	case strings.Contains(path, "/links"):
		return ChangeLink
	case strings.Contains(path, "/devices"), strings.Contains(path, "/discovery"):
		return ChangeDevice
	}
	return ChangeConfig
}

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// emit logs an event. A write error never fails the business action: the log is an
// observer, not a lock.
func (s *Server) emit(r *http.Request, ev model.Event) {
	if ev.Severity == 0 {
		ev.Severity = model.SevInfo
	}
	if ev.Actor == "" {
		ev.Actor = auth.UserFrom(r.Context()).Email
	}
	var id, err = s.Store.InsertEventID(r.Context(), ev)
	if err != nil {
		slog.Error("event log: insert failed", "err", err, "type", ev.Type)
	}
	// Alerting hangs off the journal, not beside it (ADR-0024 rule 1). Hooked
	// HERE rather than on the site bus because the bus carries no payload —
	// only "something of kind `event` changed on site X" — so a notifier
	// listening to it would know an alert happened and not what it was.
	//
	// This covers the ~143 emit* call sites. The six alertable types come from
	// three DIRECT InsertEvent callers in watch_handlers.go, hooked there for
	// the same reason; a future alertable type added through emit works with no
	// further wiring.
	if err == nil {
		s.fileAlerts(r.Context(), id, ev)
	}
	// The site event log is a screen like any other: a row that appears
	// must appear for everyone. Redundant with notifyOnWrite for API actions —
	// both notifications merge in the same batching window — but not for
	// events logged without a write.
	if ev.SiteID != nil {
		s.live.notify(*ev.SiteID, ChangeEvent, r.Header.Get(liveTabHeader))
	}
}

// emitSite is the shortcut for a site-scoped event.
func (s *Server) emitSite(r *http.Request, siteID, evType string, sev int, msg string, payload any) {
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	s.emit(r, model.Event{
		SiteID: &siteID, Type: evType, Severity: sev, Message: msg, Payload: raw,
	})
}

// emitInstance is the shortcut for an instance event, without site: proxy lifecycle,
// for example, belongs to no site (cf. docs/en/01-Architecture/Proxy.md).
func (s *Server) emitInstance(r *http.Request, evType string, sev int, msg string) {
	s.emit(r, model.Event{Type: evType, Severity: sev, Message: msg})
}

// handleListInstanceEvents: event log WITHOUT site — proxies, discovery, audit.
// Reserved for global-admin: user logins and instance credentials appear here,
// and must not be readable by a mere site viewer ([[RBAC]]).
func (s *Server) handleListInstanceEvents(w http.ResponseWriter, r *http.Request) {
	var q = r.URL.Query()
	var limit, _ = strconv.Atoi(q.Get("limit"))
	var sevMin, _ = strconv.Atoi(q.Get("severity_min"))
	var events, err = s.Store.ListEvents(r.Context(), store.EventFilter{
		Type: q.Get("type"), SeverityMin: sevMin, Limit: limit,
	})
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, events)
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var q = r.URL.Query()
	var limit, _ = strconv.Atoi(q.Get("limit"))
	var sevMin, _ = strconv.Atoi(q.Get("severity_min"))
	var events, err = s.Store.ListEvents(r.Context(), store.EventFilter{
		SiteID: siteID, DeviceID: q.Get("device_id"), Type: q.Get("type"),
		SeverityMin: sevMin, Limit: limit,
	})
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, events)
}

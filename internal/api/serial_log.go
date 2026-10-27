package api

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// serialSessionLog keeps the latest browser Web Serial run (autodetect / soft-reset)
// so it can be inspected via API and docker logs without relying on the SPA state.
//
// In-memory only: same trade-off as debuglog — diagnosis must not write to the DB
// while something is already failing.

type serialLogLine struct {
	TS    time.Time `json:"ts"`
	Phase string    `json:"phase,omitempty"`
	Msg   string    `json:"msg"`
}

type serialSession struct {
	ID        string          `json:"id"`
	SiteID    string          `json:"site_id,omitempty"`
	Equipment string          `json:"equipment,omitempty"`
	Action    string          `json:"action,omitempty"`
	StartedAt time.Time       `json:"started_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Lines     []serialLogLine `json:"lines"`
}

type serialLogStore struct {
	mu      sync.Mutex
	current *serialSession
	// Keep a few recent sessions so a reconnect mid-diagnosis does not wipe the last run.
	recent []*serialSession
}

const serialLogMaxLines = 2000
const serialLogMaxSessions = 8

func (s *Server) serialLogs() *serialLogStore {
	if s.serialLog == nil {
		s.serialLog = &serialLogStore{}
	}
	return s.serialLog
}

type serialLogInput struct {
	SessionID string   `json:"session_id"`
	SiteID    string   `json:"site_id"`
	Equipment string   `json:"equipment"`
	Action    string   `json:"action"`
	Phase     string   `json:"phase"`
	Lines     []string `json:"lines"`
}

// handlePostSerialLog receives batched run-log lines from the Web Serial wizard.
func (s *Server) handlePostSerialLog(w http.ResponseWriter, r *http.Request) {
	var in serialLogInput
	if !decode(w, r, &in) {
		return
	}
	in.SessionID = strings.TrimSpace(in.SessionID)
	if in.SessionID == "" {
		respondErr(w, http.StatusBadRequest, "session_id required")
		return
	}
	if len(in.Lines) == 0 {
		respond(w, http.StatusNoContent, nil)
		return
	}
	if len(in.Lines) > 200 {
		respondErr(w, http.StatusBadRequest, "too many lines in one batch")
		return
	}

	var now = time.Now().UTC()
	var store = s.serialLogs()
	store.mu.Lock()
	var sess = store.current
	if sess == nil || sess.ID != in.SessionID {
		if sess != nil {
			store.recent = append([]*serialSession{sess}, store.recent...)
			if len(store.recent) > serialLogMaxSessions {
				store.recent = store.recent[:serialLogMaxSessions]
			}
		}
		sess = &serialSession{
			ID: in.SessionID, StartedAt: now, UpdatedAt: now,
			Lines: make([]serialLogLine, 0, 64),
		}
		store.current = sess
	}
	if in.SiteID != "" {
		sess.SiteID = in.SiteID
	}
	if in.Equipment != "" {
		sess.Equipment = in.Equipment
	}
	if in.Action != "" {
		sess.Action = in.Action
	}
	sess.UpdatedAt = now
	var line string
	for _, line = range in.Lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = redactSerialLogLine(line)
		sess.Lines = append(sess.Lines, serialLogLine{
			TS: now, Phase: in.Phase, Msg: line,
		})
		slog.Info("serial.session",
			"session_id", sess.ID,
			"site_id", sess.SiteID,
			"equipment", sess.Equipment,
			"action", sess.Action,
			"phase", in.Phase,
			"line", line,
		)
	}
	if len(sess.Lines) > serialLogMaxLines {
		sess.Lines = sess.Lines[len(sess.Lines)-serialLogMaxLines:]
	}
	store.mu.Unlock()
	respond(w, http.StatusNoContent, nil)
}

// handleGetSerialLog returns the current (or named) serial session transcript.
func (s *Server) handleGetSerialLog(w http.ResponseWriter, r *http.Request) {
	var want = strings.TrimSpace(r.URL.Query().Get("session_id"))
	var store = s.serialLogs()
	store.mu.Lock()
	defer store.mu.Unlock()
	var sess *serialSession
	if want == "" {
		sess = store.current
	} else if store.current != nil && store.current.ID == want {
		sess = store.current
	} else {
		var i int
		for i = 0; i < len(store.recent); i++ {
			if store.recent[i].ID == want {
				sess = store.recent[i]
				break
			}
		}
	}
	if sess == nil {
		respondErr(w, http.StatusNotFound, "no serial session log")
		return
	}
	respond(w, http.StatusOK, sess)
}

// redactSerialLogLine strips values that look like typed passwords after a keyword,
// without redacting informational messages such as "setting enable password…".
// redactSerialLogLine hides a secret WITHOUT throwing away the rest of the
// transcript.
//
// # Why this splits, when its name says "line"
//
// The wizard posts two very different things through here: one-line run-log
// messages, and the raw SERIAL CAPTURE TAIL — thirty or forty lines of console
// in a single payload. The redactor used to truncate at the first marker and
// return, which is right for the first kind and destroys the second: everything
// after the first "secret" was dropped, transcript and all.
//
// That is not a cosmetic loss. On 2026-08-06 a failing Arista run was diagnosed
// from exactly this capture, and the truncation made the console look like it had
// gone SILENT right after the credential change — the echoes of the next three
// commands had been redacted away, not withheld by the device. A real defect was
// read out of an artifact of this function, and the wrong subsystem was blamed
// for it.
//
// So each physical line is redacted on its own and the rest survives. Per-line
// semantics are unchanged, which is what keeps the single-line callers identical.
func redactSerialLogLine(payload string) string {
	if !strings.ContainsAny(payload, "\r\n") {
		return redactOneLine(payload)
	}
	var lines = strings.Split(payload, "\n")
	var i int
	for i = range lines {
		// Keep the \r of a CRLF console capture where it was: it is not noise,
		// it is what the device sent, and a transcript is read as it arrived.
		var cr = strings.HasSuffix(lines[i], "\r")
		var body = strings.TrimSuffix(lines[i], "\r")
		body = redactOneLine(body)
		if cr {
			body += "\r"
		}
		lines[i] = body
	}
	return strings.Join(lines, "\n")
}

func redactOneLine(line string) string {
	var lower = strings.ToLower(line)
	var markers = []string{"password:", "password ", "secret:", "secret ", "passwd:", "passwd "}
	var m string
	for _, m = range markers {
		var idx = strings.Index(lower, m)
		if idx < 0 {
			continue
		}
		var after = strings.TrimSpace(line[idx+len(m):])
		// "password…" alone → keep; "password hunter2" → redact.
		if after == "" || strings.HasPrefix(after, "…") || strings.HasPrefix(after, "...") {
			continue
		}
		return line[:idx+len(m)] + "[redacted]"
	}
	return line
}

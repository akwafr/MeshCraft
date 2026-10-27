// Package debuglog keeps the latest log lines of the APPLICATION in memory
// and streams them live.
//
// Not to be confused with the [[Event log]], which
// speaks to the operator about the FLEET. Here we're talking to meshcraft
// itself about its own diagnostics: "instanton connector: HTTP 200 with no
// state code" doesn't matter to the administrator of a site, but it's the
// one thing that matters when trying to figure out why a deployment fails.
//
// See docs/en/01-Architecture/Unified monitoring and pipelines.md §6.
//
// IN MEMORY, never in the database. A debug log that gets written to the
// database adds load at the exact moment when something is going wrong —
// i.e. at the worst possible moment.
package debuglog

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Line is a log line as it will be displayed.
type Line struct {
	Seq   uint64            `json:"seq"`
	TS    time.Time         `json:"ts"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Ring keeps the last N lines and wakes up subscribers.
//
// The bound is what makes this safe: a component stuck in a runaway loop
// fills the ring and overwrites old lines, but it never fills up the
// controller's memory.
type Ring struct {
	mu      sync.RWMutex
	lines   []Line
	next    int
	seq     uint64
	subs    map[chan Line]struct{}
	maxSubs int
}

// NewRing builds the ring. `size` is the number of lines kept.
func NewRing(size int) *Ring {
	if size <= 0 {
		size = 2000
	}
	return &Ring{lines: make([]Line, size), subs: map[chan Line]struct{}{}, maxSubs: 16}
}

// sensitive reports whether an attribute key likely carries a secret.
//
// The filtering is done on the NAME, not the value: looking for a secret in
// a value would require knowing what it looks like, which is false by
// construction. A password that transits through a debug log is a leaked
// password — and this stream can be viewed in a browser.
func sensitive(key string) bool {
	var k = strings.ToLower(key)
	var needle string
	for _, needle = range []string{"password", "secret", "token", "cred", "passwd", "key"} {
		if strings.Contains(k, needle) {
			return true
		}
	}
	return false
}

// Add records a line and pushes it to subscribers.
func (r *Ring) Add(l Line) {
	r.mu.Lock()
	r.seq++
	l.Seq = r.seq
	r.lines[r.next] = l
	r.next = (r.next + 1) % len(r.lines)
	var subs = make([]chan Line, 0, len(r.subs))
	var c chan Line
	for c = range r.subs {
		subs = append(subs, c)
	}
	r.mu.Unlock()

	for _, c = range subs {
		// NON-blocking send: a slow subscriber — a browser on a poor link —
		// must never slow down the code that's logging. It loses lines,
		// which is exactly the right tradeoff for debugging.
		select {
		case c <- l:
		default:
		}
	}
}

// Snapshot returns the kept lines, from oldest to newest.
func (r *Ring) Snapshot(minLevel string) []Line {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out = make([]Line, 0, len(r.lines))
	var i int
	for i = 0; i < len(r.lines); i++ {
		var l = r.lines[(r.next+i)%len(r.lines)]
		if l.TS.IsZero() || !AtLeast(l.Level, minLevel) {
			continue
		}
		out = append(out, l)
	}
	return out
}

var rank = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

// AtLeast reports whether a level reaches the requested threshold.
func AtLeast(level, min string) bool {
	if min == "" {
		return true
	}
	return rank[level] >= rank[min]
}

// Subscribe opens a stream. The returned function must be called to release it.
//
// The number of subscribers is BOUNDED: each stream holds a channel and an
// HTTP goroutine. A forgotten browser tab must not cost resources forever.
func (r *Ring) Subscribe() (<-chan Line, func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.subs) >= r.maxSubs {
		return nil, func() {}, false
	}
	var c = make(chan Line, 256)
	r.subs[c] = struct{}{}
	return c, func() {
		r.mu.Lock()
		delete(r.subs, c)
		close(c)
		r.mu.Unlock()
	}, true
}

// Handler is a slog.Handler that writes to the ring IN ADDITION TO the
// regular handler.
//
// "In addition to" and not "instead of": logs must keep going to standard
// output, where the container's ops tooling picks them up. A debug layer
// that hijacked the stream would lose the logs at the exact moment they're
// needed most.
type Handler struct {
	next  slog.Handler
	ring  *Ring
	attrs []slog.Attr
	group string
}

// NewHandler wraps an existing handler.
func NewHandler(next slog.Handler, ring *Ring) *Handler {
	return &Handler{next: next, ring: ring}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.next.Enabled(ctx, l)
}

func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	var line = Line{
		TS: rec.Time, Level: rec.Level.String(), Msg: rec.Message,
		Attrs: map[string]string{},
	}
	var a slog.Attr
	for _, a = range h.attrs {
		putAttr(line.Attrs, h.group, a)
	}
	rec.Attrs(func(at slog.Attr) bool {
		putAttr(line.Attrs, h.group, at)
		return true
	})
	if len(line.Attrs) == 0 {
		line.Attrs = nil
	}
	h.ring.Add(line)
	return h.next.Handle(ctx, rec)
}

func putAttr(dst map[string]string, group string, a slog.Attr) {
	var key = a.Key
	if group != "" {
		key = group + "." + key
	}
	if sensitive(key) {
		dst[key] = "[masked]"
		return
	}
	dst[key] = a.Value.String()
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var out = *h
	out.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	out.next = h.next.WithAttrs(attrs)
	return &out
}

func (h *Handler) WithGroup(name string) slog.Handler {
	var out = *h
	out.group = name
	out.next = h.next.WithGroup(name)
	return &out
}

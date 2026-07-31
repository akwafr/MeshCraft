package api

import (
	"sort"
	"sync"
	"time"
)

// PER-SITE change bus: the mechanism that replaces periodic polling of fleet screens.
//
// See docs/01-Architecture/Monitoring ("Hub WS -> real-time UI") and
// docs/02-Frontend/Interface web section Real-time.
//
// # What the bus carries, and especially what it does not
//
// It announces "this site changed, because of such-and-such category of thing". Nothing
// else. Not the device, not the port, not the value. Data still flows through the REST
// API, and the client only invalidates its cache.
//
// This is a choice, not laziness: pushing data would create a SECOND serialization path
// to maintain alongside REST. The two would diverge — at the first field added on one
// side only — and the screen would then show something the API no longer returns. A
// screen that lies is worse than a slow screen.
//
// Happy corollary: the stream cannot leak secrets, since it only carries the site
// identifier (which the subscriber already has — it is in their URL) and fixed category
// labels, hard-coded below.
//
// # Why an in-memory bus and not the database
//
// A change is perishable information lasting a few hundred milliseconds. Routing it
// through Postgres (LISTEN/NOTIFY, queue table) would add load to the database at the
// exact moment the fleet moves — i.e. the worst moment. The controller is a single
// process: memory is enough. The day there are several, this file is the only one to
// replace.

// Change categories. They serve DIAGNOSTICS (you see in the stream why the screen
// refreshed) and as vocabulary for callers; the client invalidates everything concerning
// the site without reading them — cf. useSiteLive in web/src/api.ts, which explains why.
const (
	// ChangePort: a link comes up or goes down, a port intent is written.
	ChangePort = "port"
	// ChangeDevice: reachability, device create/delete/move.
	ChangeDevice = "device"
	// ChangeSync: reconciliation state moved (pending, drift, in_sync).
	ChangeSync = "sync"
	// ChangeLink: synoptic links.
	ChangeLink = "link"
	// ChangeConfig: VLANs, profiles, groups, tags, plans, credentials —
	// the PRESENCE of a credential changes, never its secret.
	ChangeConfig = "config"
	// ChangeEvent: a line appeared in the site event log.
	ChangeEvent = "event"
)

const (
	// liveCoalesce is the coalescing window.
	//
	// This is THE parameter that makes the thing workable: fifty ports moving together —
	// a switch rebooting, a configuration push across an entire room — must produce ONE
	// refresh, not fifty. Each refresh costs one request per open screen; without
	// coalescing, real time would cost more than the periodic polling it replaces.
	//
	// 400 ms: long enough to absorb a burst, short enough to feel instant (the eye does
	// not distinguish 0 from 0.4 s on an indicator).
	liveCoalesce = 400 * time.Millisecond

	// liveHeartbeat: stream heartbeat.
	//
	// A silent SSE is indistinguishable from a dead SSE — a reverse proxy buffering, a
	// NAT that forgot the connection, and the browser sees no error. The heartbeat gives
	// the client something to arm a watchdog with, and keeps intermediaries awake.
	liveHeartbeat = 20 * time.Second

	// maxLiveSubs caps open streams, ALL SITES COMBINED.
	//
	// Each stream holds an HTTP goroutine and a channel. A forgotten tab never closes
	// itself, and a fleet has more open screens than people in front of them. Beyond
	// the cap, we refuse outright: the client then falls back to periodic polling, which
	// is degraded but honest — whereas a controller out of goroutines serves no one.
	maxLiveSubs = 256

	// liveKindsMax caps categories accumulated between two sends. There are six; the
	// bound exists so a fanciful caller cannot inflate the map indefinitely.
	liveKindsMax = 16
)

// siteBus holds subscribers and pushes wake-ups to them.
type siteBus struct {
	mu   sync.Mutex
	subs map[*siteSub]struct{}
	max  int
}

// siteSub is one subscriber: one screen, on one site, in one tab.
//
// It has no message queue but an ACCUMULATOR (`kinds`) and a capacity-1 wake signal.
// That is what makes coalescing structural rather than optional: a thousand
// notifications between two wake-ups can produce, at worst, six entries in a map.
type siteSub struct {
	site string
	// origin is the AUTHOR tab not to serve: the one that just wrote already has its
	// HTTP response and local invalidation. Sending it the echo of its own write would
	// make it reload twice — and during drag-and-drop on the plan, once per pixel.
	origin string
	wake   chan struct{}

	mu    sync.Mutex
	kinds map[string]struct{}
}

func newSiteBus(max int) *siteBus {
	if max <= 0 {
		max = maxLiveSubs
	}
	return &siteBus{subs: map[*siteSub]struct{}{}, max: max}
}

// subscribe opens a subscription. The returned function MUST be called to free the
// slot. `false` = no room left; the caller must tell the client.
func (b *siteBus) subscribe(site, origin string) (*siteSub, func(), bool) {
	if b == nil {
		return nil, func() {}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.subs) >= b.max {
		return nil, func() {}, false
	}
	var sub = &siteSub{
		site: site, origin: origin,
		wake:  make(chan struct{}, 1),
		kinds: map[string]struct{}{},
	}
	b.subs[sub] = struct{}{}
	return sub, func() {
		b.mu.Lock()
		delete(b.subs, sub)
		b.mu.Unlock()
	}, true
}

// notify announces a change on a site.
//
// NEVER BLOCKS, and that is the property that matters: this function is called from the
// write path — an HTTP handler, the loop processing transitions reported by a proxy. A
// browser on a poor link must not slow down recording a port state. A slow subscriber
// loses nothing anyway: its accumulator keeps the category, and it will be served when it
// comes back to read.
//
// Empty `origin` = nobody excluded (change of non-human origin: monitoring, automatic apply).
func (b *siteBus) notify(site, kind, origin string) {
	if b == nil || site == "" || kind == "" {
		return
	}
	// Collect under lock, send outside lock: waking a subscriber while holding the bus
	// lock would make all other writers wait.
	b.mu.Lock()
	var targets = make([]*siteSub, 0, len(b.subs))
	var sub *siteSub
	for sub = range b.subs {
		if sub.site != site {
			continue
		}
		if origin != "" && sub.origin == origin {
			continue
		}
		targets = append(targets, sub)
	}
	b.mu.Unlock()

	for _, sub = range targets {
		sub.add(kind)
	}
}

// add accumulates a category and wakes the subscriber.
func (sub *siteSub) add(kind string) {
	sub.mu.Lock()
	if len(sub.kinds) < liveKindsMax {
		sub.kinds[kind] = struct{}{}
	}
	sub.mu.Unlock()
	// NON-blocking wake on a capacity-1 channel: the signal says "something is there",
	// it does not count things. A token already present is exactly the right behavior —
	// that IS coalescing itself.
	//
	// The channel is never closed: a send that arrives after unsubscribe lands in a
	// channel nobody reads anymore, which is harmless, whereas a send on a closed channel
	// would panic.
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// drain takes and resets accumulated categories.
func (sub *siteSub) drain() []string {
	sub.mu.Lock()
	var out = make([]string, 0, len(sub.kinds))
	var k string
	for k = range sub.kinds {
		out = append(out, k)
	}
	sub.kinds = map[string]struct{}{}
	sub.mu.Unlock()
	// Stable order: a human reads the stream when diagnosing.
	sort.Strings(out)
	return out
}

// count returns the number of subscribers. Used by tests and diagnostics.
func (b *siteBus) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

package api

// The site-extension import, as a background loop (ADR-0029 §11-§14).
//
// # What this replaced
//
// The import used to be a button. "Import now" on the Wi-Fi screen, one pass,
// on demand — which meant the list somebody was reading was as old as the last
// time anybody had thought to click, and nobody can tell by looking how old
// that is. Refreshing a list is not a decision an operator should have to make,
// so it stopped being one.
//
// # THE TRAP, and why the guard is structural rather than a lock
//
// The cadence asked for is 30 s. The ceiling on ONE full pass is 90 s
// (extSyncTimeout: one detail request per box, and a site with forty boxes
// makes forty-one requests). Those two numbers together are three overlapping
// passes against the same remote controller — and the day it shows is the day
// the controller is slow, which is the day it matters.
//
// Two decisions close that, and neither is a mutex:
//
//  1. ONE goroutine for the whole instance, walking every paired extension of
//     every site SEQUENTIALLY. There is no second runner to exclude. A loop per
//     extension would have needed a lock per extension, and would also have
//     fired N simultaneous requests at N controllers on the first tick.
//
//  2. The delay is measured from the END of a pass, so a `time.Sleep` and
//     deliberately NOT a `time.Ticker`. A ticker fires on the wall clock and
//     its ticks pile up behind a slow pass; the lock would then DROP them, and
//     the behaviour under load would depend on a silent rejection. Sleeping
//     after finishing has no ticks to drop: under load the loop is simply
//     slower, which is the only honest thing for it to be.
//
// The one remaining concurrent reader is named rather than hidden:
// resyncAfterGesture, fired by a human gesture (siteext_gestures.go). It is
// bounded by that human, and its mirror write is atomic — at worst one pass
// loses to the other and the next one corrects it thirty seconds later.
//
// # THE LOOP IS SILENT, which is the part that is easy to get wrong
//
// A pass every 30 s that logged a line would be 2 880 journal lines a day per
// site, and the journal is a page humans open to understand an incident. So
// only TRANSITIONS are written: an import that actually changed something, the
// first failure after things were working, the recovery, the orphaned pairing.
// A pass that confirms the world writes nothing at all.
//
// And the journal is the alerting's only input (ADR-0024 rule 1), so "a product
// that alarms itself about what it just read" was a real question. It cannot
// happen, twice over: `alertableTypes` is an exhaustive allow-list of six types
// and no `extension.*` is in it (TestExtensionEventsAreNotAlertable pins that),
// and this file inserts through Store.InsertEvent, which does not go through
// `emit` and therefore never reaches fileAlerts at all.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/unifi"
)

const (
	// extSyncStartupDelay: the same reasoning as snmpHealStartupDelay — an
	// extension reached THROUGH a proxy needs that proxy's tunnel up, and
	// failing on the first pass for a cause that disappears on its own would
	// stamp `last_error` with a lie and start a backoff for nothing.
	extSyncStartupDelay = 20 * time.Second
	// extSyncEvery is the cadence, measured from the END of the previous pass.
	extSyncEvery = 30 * time.Second
	// Backoff for an extension whose controller is not answering: doubled on
	// each consecutive failure, capped. The minimum is deliberately well above
	// the cadence — retrying a dead controller every 30 s is what "hammering"
	// means, and nothing is learned by the 120th attempt that the 2nd did not
	// say.
	extSyncBackoffMin = 1 * time.Minute
	extSyncBackoffMax = 15 * time.Minute
)

// extSyncState is the per-extension backoff and the memory of the last pass.
//
// IN MEMORY ONLY, like snmpHealState, and the restart semantics are the same on
// purpose: a fresh binary deserves an immediate attempt on everything. The
// `shape` half is what makes the loop silent — see notable().
type extSyncState struct {
	mu   sync.Mutex
	next map[string]time.Time     // extension -> not before this instant
	wait map[string]time.Duration // extension -> current cooldown
	// failing remembers whether the last pass on this extension failed, so the
	// warning is written ONCE on the way down and the recovery ONCE on the way
	// back up. Without it the journal gets a line every cadence while a
	// controller is down, which is the noise this design refuses.
	failing map[string]bool
	// shape is the last successful pass's census, so a pass that changed
	// nothing can be told from one that did without re-reading the mirror.
	shape map[string]string
}

func (x *extSyncState) due(id string, now time.Time) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.next == nil {
		return true
	}
	return now.After(x.next[id])
}

func (x *extSyncState) failed(id string, now time.Time) time.Duration {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ensure()
	var w = x.wait[id]
	if w == 0 {
		w = extSyncBackoffMin
	} else {
		w *= 2
		if w > extSyncBackoffMax {
			w = extSyncBackoffMax
		}
	}
	x.wait[id] = w
	x.next[id] = now.Add(w)
	return w
}

// wasFailing reports the previous state and records the new one, so a caller
// gets the TRANSITION in one call and cannot read and write it out of step.
func (x *extSyncState) wasFailing(id string, nowFailing bool) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ensure()
	var before = x.failing[id]
	x.failing[id] = nowFailing
	return before
}

// succeeded clears the cooldown and returns whether the census MOVED since the
// last successful pass — which is the whole test for whether to write a line.
func (x *extSyncState) succeeded(id, census string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.ensure()
	delete(x.next, id)
	delete(x.wait, id)
	var before, seen = x.shape[id]
	x.shape[id] = census
	// The FIRST successful pass after a restart reports no change. That is
	// deliberate: a binary that has just started has not learned that anything
	// moved, and announcing the whole inventory as news on every deploy is the
	// same noise by another route.
	return seen && before != census
}

// forget drops everything remembered about an extension — called when the
// extension is reconfigured, so a repaired token does not sit out a cooldown
// that may be at its fifteen-minute ceiling (ADR-0029 §12). This is what makes
// the "import now" button unnecessary rather than merely absent.
func (x *extSyncState) forget(id string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	delete(x.next, id)
	delete(x.wait, id)
	delete(x.failing, id)
	delete(x.shape, id)
}

func (x *extSyncState) ensure() {
	if x.next == nil {
		x.next = map[string]time.Time{}
		x.wait = map[string]time.Duration{}
		x.failing = map[string]bool{}
		x.shape = map[string]string{}
	}
}

// censusOf is the fingerprint a pass is compared against.
//
// Counts and removals, not a hash of every row: the question this answers is
// "is there anything a human would want to read about", and a box whose uptime
// counter moved is not that. A rename is therefore NOT news here — it shows on
// the screen at the next refresh, which is where somebody would look for it,
// and it does not deserve a journal line every time a venue relabels a box.
func censusOf(rep model.ExtSyncReport) string {
	return strconv.Itoa(rep.AccessPoints) + "/" + strconv.Itoa(rep.Switches) + "/" +
		strconv.Itoa(rep.Others) + "/" + strconv.Itoa(rep.SSIDs) + "/" + strconv.Itoa(rep.Removed)
}

// RunExtSync imports every paired site extension until the controller stops.
func (s *Server) RunExtSync(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(extSyncStartupDelay):
	}
	for {
		s.extSyncPass(ctx)
		// FROM THE END, not from the start. See the file header.
		var timer = time.NewTimer(extSyncEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// extSyncPass walks every paired extension once, sequentially.
//
// One extension's failure stops neither the pass nor the loop: an unreachable
// controller on site A must not cost site B its inventory.
func (s *Server) extSyncPass(ctx context.Context) {
	var exts, err = s.Store.ListPairedExtensions(ctx)
	if err != nil {
		slog.Error("ext sync: listing extensions failed", "err", err)
		return
	}
	var now = time.Now()
	var e model.SiteExtension
	for _, e = range exts {
		if ctx.Err() != nil {
			return
		}
		if !s.extSync.due(e.ID, now) {
			continue
		}
		s.extSyncOne(ctx, e)
	}
}

// extSyncOne is one extension's pass: the same sequence handleExtSync ran on a
// click, minus the HTTP, plus the transition bookkeeping that keeps it quiet.
func (s *Server) extSyncOne(ctx context.Context, e model.SiteExtension) {
	var passCtx, cancel = context.WithTimeout(ctx, extSyncTimeout)
	defer cancel()

	var cli, err = s.unifiClient(passCtx, e)
	if err != nil {
		s.extSyncFailed(passCtx, e, err)
		return
	}

	var remote unifi.Site
	remote, err = cli.FindSite(passCtx, e.RemoteSiteID)
	switch {
	case errors.Is(err, unifi.ErrRemoteSiteGone):
		// The controller ANSWERED and the paired site was not in its list. That
		// orphans the pairing, and it is a transition — written once, because
		// MarkSiteExtensionOrphaned coalesces on `orphaned_at` and this loop
		// checks the flag before writing a second line.
		_ = s.Store.MarkSiteExtensionOrphaned(passCtx, e.ID,
			"the controller no longer lists the paired site")
		if e.OrphanedAt == nil {
			s.extSyncEvent(passCtx, e, "extension.orphaned", model.SevWarning,
				"Paired site \""+e.RemoteSiteName+"\" has disappeared from "+e.Kind+
					"; the imported inventory is kept and stamped stale")
		}
		// Not a transport failure, so no backoff: the controller is perfectly
		// reachable and the answer will not change until somebody pairs again.
		// Backing off here would also delay noticing the re-pairing.
		return
	case err != nil:
		s.extSyncFailed(passCtx, e, err)
		return
	}
	_ = s.Store.RefreshRemoteSiteLabel(passCtx, e.ID, remote.InternalReference, remote.Name)

	var boxes []unifi.DeviceDetail
	boxes, err = cli.ListDevices(passCtx, e.RemoteSiteID)
	if err != nil {
		s.extSyncFailed(passCtx, e, err)
		return
	}

	// The SSIDs come from the uncontracted legacy endpoint and DEGRADE the pass
	// rather than failing it — `ssidsKnown` false tells the store to keep the
	// ones it has instead of emptying the list (ADR-0015, Consequences).
	var ssids []unifi.SSID
	var ssidsKnown = true
	ssids, err = cli.ListSSIDs(passCtx, remote.InternalReference)
	if err != nil {
		ssids, ssidsKnown = nil, false
	}

	var rep model.ExtSyncReport
	rep, err = s.Store.ReplaceExtInventory(passCtx, e.ID, e.SiteID,
		unifi.ToExtDevices(boxes), unifi.ToExtSSIDs(ssids), ssidsKnown)
	if err != nil {
		// A database failure is OURS, not the controller's: it does not stamp
		// `last_error` on the extension, which is the operator's window onto
		// the FOREIGN side and must not accuse it of our outage.
		slog.Error("ext sync: writing the mirror failed", "err", err, "extension", e.ID)
		return
	}

	var moved = s.extSync.succeeded(e.ID, censusOf(rep))
	if s.extSync.wasFailing(e.ID, false) {
		s.extSyncEvent(passCtx, e, "extension.recovered", model.SevInfo,
			"Contact with "+e.Kind+" for \""+remote.Name+"\" is restored")
	}
	if moved {
		rep.RemoteSiteName = remote.Name
		s.extSyncEvent(passCtx, e, "extension.imported", model.SevInfo,
			"Inventory of \""+remote.Name+"\" changed: "+
				strconv.Itoa(rep.AccessPoints)+" access point(s), "+
				strconv.Itoa(rep.Switches)+" switch(es), "+
				strconv.Itoa(rep.SSIDs)+" SSID(s), "+
				strconv.Itoa(rep.Removed)+" gone")
	}
	// The screen is told regardless of whether the journal was: a list that
	// refreshed is worth pushing, and it carries no severity.
	s.live.notify(e.SiteID, ChangeEvent, "")
}

// extSyncFailed records a transport failure and writes the warning ONCE.
//
// The sentence names the backoff, because "it is not answering" and "it is not
// answering and I will stop asking for a quarter of an hour" send an operator
// to different places.
func (s *Server) extSyncFailed(ctx context.Context, e model.SiteExtension, cause error) {
	var wait = s.extSync.failed(e.ID, time.Now())
	_ = s.Store.MarkSiteExtensionError(ctx, e.ID, firstSentence(cause.Error()))
	if s.extSync.wasFailing(e.ID, true) {
		return
	}
	s.extSyncEvent(ctx, e, "extension.unreachable", model.SevWarning,
		"Cannot read "+e.Kind+" for \""+e.RemoteSiteName+"\": "+firstSentence(cause.Error())+
			" — retrying in "+wait.String()+", and backing off further on each failure")
}

// extSyncEvent writes one journal line for the background loop.
//
// Through Store.InsertEvent and NOT through `emit`: there is no request, so
// there is no actor to attribute it to — `extension.sync` names the loop
// itself, which is the truthful actor. A side effect worth stating: InsertEvent
// does not call fileAlerts, so nothing this loop writes can wake anybody even
// if a type were added to alertableTypes by mistake.
func (s *Server) extSyncEvent(ctx context.Context, e model.SiteExtension,
	evType string, sev int, msg string) {

	var siteID = e.SiteID
	var err = s.Store.InsertEvent(ctx, model.Event{
		SiteID:   &siteID,
		Type:     evType,
		Severity: sev,
		Actor:    "extension.sync",
		Message:  msg,
	})
	if err != nil {
		slog.Error("ext sync: event log insert failed", "err", err, "type", evType)
	}
}

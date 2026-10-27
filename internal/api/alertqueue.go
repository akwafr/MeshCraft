// The alerting queue: filing journal lines as delivery slips, and draining them.
//
// ADR-0024. alerting.go holds the policy as pure functions; this file is the
// part that touches the database, the clock and the network. The split is on
// purpose — the rules that decide whether a phone buzzes at 3am are tested
// without any of those three.
//
// # Two halves, and why they are separate
//
// FILING (fileAlerts) runs on the journal write path, synchronously, and does
// the smallest possible amount of work: classify, look up two facts about the
// port, write a slip. It never opens a socket. That is rule 6 — delivery is an
// effect, not a condition — and it is what makes "a channel that does not
// answer must not block detection" true by construction rather than by
// vigilance.
//
// DRAINING (RunNotifyQueue) runs on its own goroutine with its own budget,
// reads what is due, sends it, and records the outcome. A Telegram outage makes
// this half slow and leaves the other half untouched.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/notify"
	"meshcraft/internal/store"
)

// fileAlerts files a freshly journalled event against every channel that wants
// it. Called from the journal write path; never blocks on a network.
//
// The order of the three gates is the policy, and it is not interchangeable:
//
//  1. CLASS — is this event one of the six alertable types, and does the port
//     qualify? Most events die here, and most port events die on the interest
//     rule, which is the whole point of ADR-0024.
//  2. SUBSCRIPTION — did this channel ask for this class at this severity?
//  3. ATTRIBUTION — is this our own doing? Checked LAST because it costs a
//     journal read, and there is no sense paying for it on an event no channel
//     wanted.
func (s *Server) fileAlerts(ctx context.Context, eventID int64, ev model.Event) {
	if eventID <= 0 || s.Store == nil {
		return
	}
	// Cheap reject before any query: five sixths of the journal's vocabulary is
	// not alertable, and this is the hot path of every write in the product.
	var _, alertable = alertableTypes[ev.Type]
	if !alertable {
		return
	}
	var siteID string
	if ev.SiteID != nil {
		siteID = *ev.SiteID
	}
	var channels, err = s.Store.ChannelsForEvent(ctx, siteID)
	if err != nil {
		slog.Warn("alerting: channel lookup failed", "err", err, "type", ev.Type)
		return
	}
	if len(channels) == 0 {
		return
	}

	// The two facts the interest rule needs. Looked up ONCE for all channels,
	// and only now that we know somebody is listening.
	var pf = s.portFactsFor(ctx, ev)
	var class, qualifies = classifyAlert(ev, pf)
	if !qualifies {
		// A port event that is neither described nor interco. It stays in the
		// journal and wakes nobody — and we COUNT it, so the blind spot
		// ADR-0024 accepts is a number on a screen instead of a silence.
		//
		// Counted ONLY on channels that subscribe to a port class. Bumping
		// every channel would tell a device-only channel it had "excluded 127
		// port transitions", which is not a gap in ITS coverage — it asked for
		// devices — and an inflated number on the one screen whose job is to
		// make the blind spot legible is worse than no number.
		if ev.PortName != "" {
			var ids []string
			var c model.NotifyChannel
			for _, c = range channels {
				if channelWants(c, model.AlertClassInterco, ev.Severity) ||
					channelWants(c, model.AlertClassDescribed, ev.Severity) {
					ids = append(ids, c.ID)
				}
			}
			if err = s.Store.BumpDroppedUninteresting(ctx, ids); err != nil {
				slog.Warn("alerting: drop counter", "err", err)
			}
		}
		return
	}

	var key = alertKeyOf(class, ev)
	// Attribution is resolved once per event, not once per channel: it is a
	// property of the event and the device, and a journal read per channel
	// would multiply the cost by the number of channels for an identical
	// answer.
	var reason, attributed = s.attributionFor(ctx, ev)

	var ch model.NotifyChannel
	for _, ch = range channels {
		if !channelWants(ch, class, ev.Severity) {
			continue
		}
		if attributed {
			// Suppressed, with the gesture named — and re-armed later by the
			// sweep if the condition still stands. Suppression DELAYS bad
			// news, it never loses it (ADR-0024 rule 5).
			if err = s.Store.SuppressDelivery(ctx, eventID, ch.ID, key, reason); err != nil {
				slog.Warn("alerting: suppress failed", "err", err, "channel", ch.ID)
			}
			continue
		}
		// A key that is moving does not alert. FileDelivery pushes an existing
		// held slip's deadline back instead of adding a second message, which
		// is how six transitions become one.
		//
		// The CAP is applied in SQL, against the slip's stored `first_at`,
		// because only the database knows when this key's episode began — this
		// process may not even be the one that filed the first event. So the
		// value computed here is deliberately uncapped, and FileDelivery floors
		// it.
		var release = time.Now().Add(holdFor(class))
		if _, err = s.Store.FileDelivery(ctx, eventID, ch.ID, key,
			release, alertHoldMax); err != nil {
			slog.Warn("alerting: file failed", "err", err, "channel", ch.ID, "key", key)
		}
	}
}

// portFactsFor looks up whether the event's port is described, and whether it
// faces a device the fleet knows.
//
// Returns the zero value — neither described nor interco — for a device event,
// which costs nothing and keeps the caller branch-free.
func (s *Server) portFactsFor(ctx context.Context, ev model.Event) portFacts {
	var pf portFacts
	if ev.PortName == "" || ev.DeviceID == nil {
		return pf
	}
	var d, err = s.Store.GetDevice(ctx, *ev.DeviceID)
	if err != nil {
		// Unknown device: we cannot judge interest, and guessing "interesting"
		// would make a deleted device's last transitions alert. Silence.
		return pf
	}
	pf.Described = portDescribed(d, ev.PortName)

	// Declared link first: one index lookup, and a mapped site has them.
	var declared bool
	declared, err = s.Store.PortFacesKnownDevice(ctx, d.SiteID, d.ID, ev.PortName)
	if err != nil {
		slog.Warn("alerting: declared link lookup", "err", err, "device", d.ID)
	}
	if declared {
		pf.Interco = true
		return pf
	}
	pf.Interco = s.portHasKnownNeighbour(ctx, d, ev.PortName)
	return pf
}

// portDescribed reports whether an operator typed a description for this port.
//
// The field is `devices.ports.<port>.label`, keyed by FACADE name — which is
// exactly what the journal's `port_name` carries, because portEventOf sets it
// only when the facade recognised the port. The two agree by construction, and
// that is why this needs no resolution of its own.
//
// Reading the same jsonb as portManaged, deliberately: "described" is the
// second filter on top of "managed", not a parallel notion.
func portDescribed(d model.Device, name string) bool {
	var ports map[string]map[string]any
	if json.Unmarshal(d.Ports, &ports) != nil {
		return false
	}
	var label, _ = ports[name]["label"].(string)
	return strings.TrimSpace(label) != ""
}

// portHasKnownNeighbour reports whether LLDP sees a FLEET device on this port.
//
// Goes through IdentifyLLDPNeighbors rather than querying `lldp_neighbors`
// directly, because recognition is not a column: it is a read-time derivation
// over three ranked identity sources (chassis id, port id, system name) plus
// the MAC tokens that rescue a device serving nothing under 1.0.8802. Writing a
// second, SQL-shaped version of that ladder here would produce two answers to
// "is this my neighbour" — and ADR-0024 rule 3 is that interco is derived,
// never stored.
func (s *Server) portHasKnownNeighbour(ctx context.Context, d model.Device, port string) bool {
	// Site-scoped read, filtered on the device below: that is the only shape the
	// store offers, and it is also what IdentifyLLDPNeighbors needs — resolution
	// is done against the WHOLE site's identities, so narrowing the read would
	// change the answer rather than just its size.
	var neighbours, err = s.Store.ListLLDPNeighbors(ctx, d.SiteID)
	if err != nil || len(neighbours) == 0 {
		return false
	}
	var selves []model.LLDPSelf
	selves, err = s.Store.ListLLDPSelf(ctx, d.SiteID)
	if err != nil {
		return false
	}
	var devices []model.Device
	devices, err = s.Store.ListDevices(ctx, d.SiteID)
	if err != nil {
		return false
	}
	var identified = IdentifyLLDPNeighbors(neighbours, selves, devices)
	var n model.LLDPNeighbor
	for _, n = range identified {
		// DeviceID as well as LocalPort: port names repeat across a fleet, and
		// matching on the name alone would make "GE1 on anything" an interco.
		if n.DeviceID == d.ID && n.LocalPort == port && n.PeerDevice != "" {
			return true
		}
	}
	return false
}

// attributionFor decides whether this event is a consequence of something
// meshcraft did, and names it.
//
// Two sources, cheapest first:
//
//   - the LIVE one: applyQueuedOrRunning, already used by the PoE sampler as a
//     "do not touch this device now" guard. It covers the apply in flight and
//     its six-second debounce window;
//   - the JOURNAL: our own writes are already logged, timestamped, per device.
//     This is what covers the TAIL — an apply returns in seconds, but spanning
//     tree reconverges and a port renegotiates long after. On the night of
//     2026-10-24 it is this half that would have kept eight MSTP rewrites from
//     ringing on every port they bounced.
//
// Using the journal rather than a maintenance window is the decision: a window
// has to be remembered, and nobody would have opened one before rewriting those
// eight regions (ADR-0024, Rejected alternatives).
func (s *Server) attributionFor(ctx context.Context, ev model.Event) (string, bool) {
	if ev.DeviceID == nil {
		return "", false
	}
	if s.applyQueuedOrRunning(*ev.DeviceID) {
		return "apply in progress", true
	}
	var siteID string
	if ev.SiteID != nil {
		siteID = *ev.SiteID
	}
	// Enough lines to cover the window on a busy device, newest first — which
	// is what attributedToGesture's early break relies on.
	var recent, err = s.Store.ListEvents(ctx, store.EventFilter{
		SiteID: siteID, DeviceID: *ev.DeviceID, Limit: 60,
	})
	if err != nil {
		// Cannot judge. Choosing to ALERT here rather than suppress: a false
		// alert is noise, a wrongly suppressed one is an outage nobody hears.
		slog.Warn("alerting: attribution read failed", "err", err)
		return "", false
	}
	return attributedToGesture(ev, recent, time.Now(), selfActionWindow)
}

// --- The worker ---

// RunNotifyQueue drains the delivery queue until the context ends.
//
// Shaped on RunSNMPHeal: ticker, body-first, one pass that a single failure
// cannot stop. Mounted from cmd/controller; not mounting it leaves filing
// intact and nothing delivered, which is a legible degradation — the screen
// shows a growing pending count rather than a channel that looks healthy.
func (s *Server) RunNotifyQueue(ctx context.Context) {
	var t = time.NewTicker(notifySweepEvery)
	defer t.Stop()
	for {
		s.notifyPass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// notifyPass sends everything due, then re-judges what a gesture excused.
func (s *Server) notifyPass(ctx context.Context) {
	if s.Store == nil || s.Secrets == nil {
		return
	}
	var now = time.Now()
	var due, err = s.Store.DueDeliveries(ctx, now, notifyBatchPerPass)
	if err != nil {
		slog.Warn("alerting: due lookup failed", "err", err)
		return
	}
	var x store.DeliveryWithEvent
	for _, x = range due {
		if ctx.Err() != nil {
			return
		}
		s.deliverOne(ctx, x)
	}
	s.reArmPass(ctx, now)
}

// deliverOne sends one slip and records what happened.
func (s *Server) deliverOne(ctx context.Context, x store.DeliveryWithEvent) {
	var sender, err = s.senderFor(ctx, x.Channel)
	if err != nil {
		s.noteSendFailure(ctx, x, err.Error())
		return
	}
	var sendCtx, cancel = context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	err = sender.Send(sendCtx, messageFor(x))
	if err != nil {
		s.noteSendFailure(ctx, x, err.Error())
		return
	}
	if err = s.Store.MarkDeliverySent(ctx, x.Delivery.ID); err != nil {
		slog.Warn("alerting: mark sent failed", "err", err, "delivery", x.Delivery.ID)
	}
	if err = s.Store.NoteChannelResult(ctx, x.Channel.ID, ""); err != nil {
		slog.Warn("alerting: channel health", "err", err)
	}
}

// noteSendFailure records a failed attempt and schedules the retry, or gives up.
//
// Giving up leaves the slip in `failed` forever, counted on the screen. That is
// the ledger doing its job: the alert existed, the product can say it was never
// delivered, and nothing about detection ever waited on the channel.
func (s *Server) noteSendFailure(ctx context.Context, x store.DeliveryWithEvent, why string) {
	var attempts = x.Delivery.Attempts + 1
	var next *time.Time
	if attempts < notifyMaxAttempts {
		var at = time.Now().Add(backoffAfter(attempts))
		next = &at
	}
	var err = s.Store.MarkDeliveryFailed(ctx, x.Delivery.ID, why, next)
	if err != nil {
		slog.Warn("alerting: mark failed failed", "err", err, "delivery", x.Delivery.ID)
	}
	if err = s.Store.NoteChannelResult(ctx, x.Channel.ID, why); err != nil {
		slog.Warn("alerting: channel health", "err", err)
	}
	// The KIND is logged, never the target and never the error's URL: for both
	// channels the URL contains the credential (notify.scrub exists for the
	// same reason).
	slog.Warn("alerting: delivery failed", "channel_kind", x.Channel.Kind,
		"attempt", attempts, "event_type", x.Event.Type, "err", why)
}

// reArmPass gives suppressed alerts a second chance once the gesture window has
// closed.
//
// THE HALF THAT IS EASY TO FORGET. Without it, an outage starting four minutes
// after an apply would be silenced by that apply for good, and suppression
// would be a loss rather than a delay. The re-judgement is deliberately narrow:
// only a device still `off` re-arms. A port transition is not re-judged,
// because a port's current state is already the subject of the NEXT transition
// event, and re-arming it would announce an old edge as news.
func (s *Server) reArmPass(ctx context.Context, now time.Time) {
	var pending, err = s.Store.SuppressedPastWindow(ctx, now, selfActionWindow, notifyBatchPerPass)
	if err != nil {
		slog.Warn("alerting: re-arm lookup failed", "err", err)
		return
	}
	var ids []int64
	var x store.DeliveryWithEvent
	for _, x = range pending {
		if x.Event.Type != "device.unreachable" || x.Event.DeviceID == nil {
			continue
		}
		var d, getErr = s.Store.GetDevice(ctx, *x.Event.DeviceID)
		if getErr != nil || d.Status != "off" {
			// Recovered, or gone. The suppression was right and stays.
			continue
		}
		ids = append(ids, x.Delivery.ID)
	}
	if len(ids) == 0 {
		return
	}
	if err = s.Store.ReArmSuppressed(ctx, ids, now); err != nil {
		slog.Warn("alerting: re-arm failed", "err", err)
		return
	}
	slog.Info("alerting: suppressed alerts re-armed", "count", len(ids))
}

// senderFor opens the channel's secret and builds its sender.
//
// The token is decrypted HERE, at the moment of sending, and never held on the
// Server: a long-lived decrypted token in memory is a secret with a much wider
// window than it needs.
func (s *Server) senderFor(ctx context.Context, ch model.NotifyChannel) (notify.Sender, error) {
	var _, enc, err = s.Store.RevealNotifyToken(ctx, ch.ID)
	if err != nil {
		return nil, err
	}
	var token string
	token, err = s.Secrets.Open(enc)
	if err != nil {
		// secretbox returns a fixed message here, never the underlying error.
		// It almost always means the vault key changed — which is exactly what
		// SealedColumns() exists to prevent for this column (migration 0048).
		return nil, err
	}
	return senderOf(ch, token)
}

// senderOf picks the implementation. Split from senderFor so it is testable
// without a vault.
func senderOf(ch model.NotifyChannel, token string) (notify.Sender, error) {
	switch ch.Kind {
	case model.NotifyKindTelegram:
		return notify.Telegram{Token: token, ChatID: ch.Target,
			Timeout: notifySendTimeout}, nil
	case model.NotifyKindSlack:
		// The whole webhook URL is the secret, so it comes out of the vault,
		// not out of `target` (migration 0048).
		return notify.Slack{WebhookURL: token, Timeout: notifySendTimeout}, nil
	}
	return nil, errors.New("unknown notification channel kind: " + ch.Kind)
}

// messageFor renders a slip as the channel-agnostic message.
//
// The transition count is what makes a coalesced slip honest: one message
// saying "7 state changes, now down" is the grouping promise of ADR-0024 rule 4
// kept in words. Without this line the reader would see one transition and have
// no idea six others were folded into it.
func messageFor(x store.DeliveryWithEvent) notify.Message {
	var m = notify.Message{
		Title:    x.Event.Type,
		Body:     x.Event.Message,
		Severity: x.Event.Severity,
		Site:     x.SiteName,
		Port:     x.Event.PortName,
	}
	if x.Delivery.Transitions > 1 {
		m.Body = x.Event.Message + "\n(" + itoa(x.Delivery.Transitions) +
			" state changes grouped over " +
			frMinutes(x.Event.TS.Sub(x.Delivery.FirstAt)) + ")"
	}
	return m
}

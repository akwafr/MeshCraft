// Notification channels, and the delivery slips that anchor alerts to the
// journal. ADR-0024.
//
// The shape to keep in mind while reading: `notify_deliveries.event_id` is a
// foreign key to `events(id)`. An alert is never a row of its own — it is a
// journal line somebody decided was worth a message. That is why there is no
// `alerts` table here and why every query below joins back to `events`.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"meshcraft/internal/model"
)

// notifyChanCols is the column list for a channel AS A SCREEN SEES IT.
//
// Same device as siteExtCols: `octet_length(token_enc) > 0` stands in for the
// column, so the secret is structurally unable to leave through this path. A
// later refactor cannot leak it by forgetting a `json:"-"`, because the value
// is never selected in the first place.
const notifyChanCols = `id::text, coalesce(site_id::text, ''), kind, label, enabled,
	target, octet_length(token_enc) > 0,
	classes, min_severity,
	last_ok_at, last_error, consecutive_failures, dropped_uninteresting,
	created_at, updated_at`

func scanNotifyChannel(row pgx.Row) (model.NotifyChannel, error) {
	var c model.NotifyChannel
	var err = row.Scan(&c.ID, &c.SiteID, &c.Kind, &c.Label, &c.Enabled,
		&c.Target, &c.HasToken,
		&c.Classes, &c.MinSeverity,
		&c.LastOKAt, &c.LastError, &c.ConsecutiveFailures, &c.DroppedUninteresting,
		&c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// ListNotifyChannels returns the channels an operator manages from one site
// screen: that site's channels, plus the instance-wide ones.
//
// The instance channels are INCLUDED on purpose, and read-only from a site
// screen's point of view: an operator wondering "will this site's outages reach
// anybody" must see the channel that covers every site, otherwise they create a
// duplicate. The empty SiteID in the payload is what tells them apart.
func (s *Store) ListNotifyChannels(ctx context.Context, siteID string) ([]model.NotifyChannel, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+notifyChanCols+` from notify_channels
		 where site_id = $1::uuid or site_id is null
		 order by site_id nulls first, kind, label`, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.NotifyChannel{}
	for rows.Next() {
		var c, scanErr = scanNotifyChannel(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return s.fillChannelCounts(ctx, out)
}

// ListInstanceNotifyChannels returns only the channels that belong to no site.
func (s *Store) ListInstanceNotifyChannels(ctx context.Context) ([]model.NotifyChannel, error) {
	var rows, err = s.pool.Query(ctx,
		`select `+notifyChanCols+` from notify_channels
		 where site_id is null order by kind, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.NotifyChannel{}
	for rows.Next() {
		var c, scanErr = scanNotifyChannel(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	return s.fillChannelCounts(ctx, out)
}

// fillChannelCounts adds the pending and failed delivery counts.
//
// They are computed, never stored, and they are what makes a MUTE channel
// readable. A channel with zero pending and zero failed and a recent
// `last_ok_at` is silent because nothing happened; one with eleven failed is
// silent because it is broken. Without this, those two look identical from the
// screen — which is exactly the ambiguity ADR-0024 refuses to answer with a
// heartbeat nobody reads.
func (s *Store) fillChannelCounts(ctx context.Context,
	chans []model.NotifyChannel) ([]model.NotifyChannel, error) {
	if len(chans) == 0 {
		return chans, nil
	}
	var rows, err = s.pool.Query(ctx,
		`select channel_id::text, state, count(*) from notify_deliveries
		 where state in ('held', 'failed') group by channel_id, state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var held = map[string]int{}
	var failed = map[string]int{}
	for rows.Next() {
		var id, state string
		var n int
		if err = rows.Scan(&id, &state, &n); err != nil {
			return nil, err
		}
		if state == model.NotifyHeld {
			held[id] = n
		} else {
			failed[id] = n
		}
	}
	if rows.Err() != nil {
		return nil, rows.Err()
	}
	var i int
	for i = range chans {
		chans[i].Pending = held[chans[i].ID]
		chans[i].Failed = failed[chans[i].ID]
	}
	return chans, nil
}

// GetNotifyChannel reads one channel, secret-free.
func (s *Store) GetNotifyChannel(ctx context.Context, id string) (model.NotifyChannel, error) {
	return scanNotifyChannel(s.pool.QueryRow(ctx,
		`select `+notifyChanCols+` from notify_channels where id = $1::uuid`, id))
}

// ChannelsForEvent returns the enabled channels that could receive an event
// from this site: the site's own, plus the instance-wide ones.
//
// `siteID` empty means an instance event (events.site_id is null), which only
// instance channels receive — a site channel exists to speak about its site,
// and a proxy lifecycle line belongs to no site.
func (s *Store) ChannelsForEvent(ctx context.Context, siteID string) ([]model.NotifyChannel, error) {
	var q = `select ` + notifyChanCols + ` from notify_channels
	         where enabled and octet_length(token_enc) > 0
	           and (site_id = $1::uuid or site_id is null)`
	var args = []any{siteID}
	if siteID == "" {
		q = `select ` + notifyChanCols + ` from notify_channels
		     where enabled and octet_length(token_enc) > 0 and site_id is null`
		args = nil
	}
	var rows, err = s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []model.NotifyChannel{}
	for rows.Next() {
		var c, scanErr = scanNotifyChannel(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertNotifyChannel creates or updates a channel.
//
// `tokenEnc == nil` runs an UPDATE that omits `token_enc` entirely, so editing
// a label or a severity floor never wipes the secret — the same shape as
// UpsertSiteExtension, and for the same lesson: a form that re-submits an empty
// password field must not erase the stored one.
func (s *Store) UpsertNotifyChannel(ctx context.Context, c model.NotifyChannel,
	tokenEnc []byte) (model.NotifyChannel, error) {
	var sitePtr *string
	if c.SiteID != "" {
		var v = c.SiteID
		sitePtr = &v
	}
	if c.ID != "" && tokenEnc == nil {
		return scanNotifyChannel(s.pool.QueryRow(ctx,
			`update notify_channels set kind = $2, label = $3, enabled = $4,
			   target = $5, classes = $6, min_severity = $7, updated_at = now()
			 where id = $1::uuid
			 returning `+notifyChanCols,
			c.ID, c.Kind, c.Label, c.Enabled, c.Target, c.Classes, c.MinSeverity))
	}
	if tokenEnc == nil {
		return model.NotifyChannel{}, errors.New("a new channel needs a token")
	}
	if c.ID != "" {
		return scanNotifyChannel(s.pool.QueryRow(ctx,
			`update notify_channels set kind = $2, label = $3, enabled = $4,
			   target = $5, classes = $6, min_severity = $7, token_enc = $8,
			   last_error = '', consecutive_failures = 0, updated_at = now()
			 where id = $1::uuid
			 returning `+notifyChanCols,
			c.ID, c.Kind, c.Label, c.Enabled, c.Target, c.Classes, c.MinSeverity, tokenEnc))
	}
	return scanNotifyChannel(s.pool.QueryRow(ctx,
		`insert into notify_channels (site_id, kind, label, enabled, target,
		   token_enc, classes, min_severity)
		 values ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
		 returning `+notifyChanCols,
		sitePtr, c.Kind, c.Label, c.Enabled, c.Target, tokenEnc, c.Classes, c.MinSeverity))
}

// RevealNotifyToken returns the channel and its CIPHERTEXT.
//
// Decryption and the audit line are deliberately the handler's job, exactly as
// RevealSiteExtensionToken does it: the store must not be the place where a
// secret becomes readable without a trace, because a future caller would get
// the plaintext for free and the journal line would be nobody's job.
func (s *Store) RevealNotifyToken(ctx context.Context, id string) (model.NotifyChannel, []byte, error) {
	var c, err = s.GetNotifyChannel(ctx, id)
	if err != nil {
		return model.NotifyChannel{}, nil, err
	}
	var enc []byte
	err = s.pool.QueryRow(ctx,
		`select token_enc from notify_channels where id = $1::uuid`, id).Scan(&enc)
	if err != nil {
		return model.NotifyChannel{}, nil, err
	}
	return c, enc, nil
}

func (s *Store) DeleteNotifyChannel(ctx context.Context, id string) error {
	var _, err = s.pool.Exec(ctx, `delete from notify_channels where id = $1::uuid`, id)
	return err
}

// NoteChannelResult records the outcome of a send attempt on the channel.
//
// Success clears the error and the failure counter; failure keeps `last_ok_at`
// untouched. Keeping the last success across failures is deliberate: "worked
// 4 days ago, failing since" is a far more useful sentence than "failing", and
// it is the sentence that tells an operator whether the channel was ever
// configured correctly at all.
func (s *Store) NoteChannelResult(ctx context.Context, channelID string, sendErr string) error {
	if sendErr == "" {
		var _, err = s.pool.Exec(ctx,
			`update notify_channels set last_ok_at = now(), last_error = '',
			   consecutive_failures = 0 where id = $1::uuid`, channelID)
		return err
	}
	var _, err = s.pool.Exec(ctx,
		`update notify_channels set last_error = $2,
		   consecutive_failures = consecutive_failures + 1 where id = $1::uuid`,
		channelID, sendErr)
	return err
}

// BumpDroppedUninteresting counts one port event refused for want of a
// description and of a known neighbour.
//
// This counter IS the mitigation of ADR-0024's blind spot. The policy refuses
// to alert on an undescribed port, which means a critical port nobody described
// falls silently; the least we owe the operator is the number, so the screen can
// say "127 port transitions not alerted" instead of saying nothing at all.
func (s *Store) BumpDroppedUninteresting(ctx context.Context, channelIDs []string) error {
	if len(channelIDs) == 0 {
		return nil
	}
	var _, err = s.pool.Exec(ctx,
		`update notify_channels set dropped_uninteresting = dropped_uninteresting + 1
		 where id = any($1::uuid[])`, channelIDs)
	return err
}

// --- Delivery slips ---

// FileDelivery files one alertable event against one channel, coalescing it
// into a slip already held for the same alert key.
//
// THIS IS THE ANTI-FLAP, and it lives in SQL rather than in memory for one
// reason: a controller restart must not lose a held alert. `portevents.go` can
// afford in-memory state because losing it costs a log line; losing this costs
// the alert itself.
//
// Two outcomes, both returning the slip that will actually be delivered:
//
//   - no slip held for the key: this event becomes the held slip;
//   - a slip is already held: its deadline is pushed back, its transition count
//     rises, its `event_id` moves to THIS event (the newest state is the one
//     worth announcing), and the incoming event gets its own slip in state
//     `coalesced` — so the trace from message back to journal line survives for
//     every event, not just the one that spoke.
//
// `release` is the deadline the caller computed from holdRelease; the SQL floors
// it at `first_at + cap` independently, so a caller that miscomputes cannot
// retain a slip past the ceiling.
func (s *Store) FileDelivery(ctx context.Context, eventID int64, channelID, alertKey string,
	release time.Time, cap time.Duration) (model.NotifyDelivery, error) {
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return model.NotifyDelivery{}, err
	}
	defer tx.Rollback(ctx)

	// Push back the held slip, if any. LEAST against the ceiling is the cap
	// enforcement: a port flapping forever still speaks, which matters because
	// gNMI-collected ports never get the `port.flapping` verdict that would
	// otherwise release them (cf. alerting.go, alertHoldMax).
	var d model.NotifyDelivery
	err = tx.QueryRow(ctx,
		`update notify_deliveries
		 set event_id = $3,
		     transitions = transitions + 1,
		     release_at = least($4::timestamptz, first_at + $5::interval)
		 where channel_id = $1::uuid and alert_key = $2 and state = 'held'
		 returning id, event_id, channel_id::text, state, alert_key, release_at,
		           first_at, transitions, attempts, next_attempt_at, last_error,
		           suppressed_reason, created_at, sent_at`,
		channelID, alertKey, eventID, release, cap).Scan(
		&d.ID, &d.EventID, &d.ChannelID, &d.State, &d.AlertKey, &d.ReleaseAt,
		&d.FirstAt, &d.Transitions, &d.Attempts, &d.NextAttempt, &d.LastError,
		&d.SuppressedReason, &d.CreatedAt, &d.SentAt)
	if err == nil {
		// Absorbed. File the incoming event as coalesced so the journal line
		// still has a slip pointing at it.
		_, err = tx.Exec(ctx,
			`insert into notify_deliveries
			   (event_id, channel_id, state, alert_key, release_at, transitions)
			 values ($1, $2::uuid, 'coalesced', $3, now(), 1)`,
			eventID, channelID, alertKey)
		if err != nil {
			return model.NotifyDelivery{}, err
		}
		return d, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return model.NotifyDelivery{}, err
	}

	// Nothing held for this key: this event opens the episode.
	err = tx.QueryRow(ctx,
		`insert into notify_deliveries
		   (event_id, channel_id, state, alert_key, release_at, first_at, transitions)
		 values ($1, $2::uuid, 'held', $3, $4, now(), 1)
		 returning id, event_id, channel_id::text, state, alert_key, release_at,
		           first_at, transitions, attempts, next_attempt_at, last_error,
		           suppressed_reason, created_at, sent_at`,
		eventID, channelID, alertKey, release).Scan(
		&d.ID, &d.EventID, &d.ChannelID, &d.State, &d.AlertKey, &d.ReleaseAt,
		&d.FirstAt, &d.Transitions, &d.Attempts, &d.NextAttempt, &d.LastError,
		&d.SuppressedReason, &d.CreatedAt, &d.SentAt)
	if err != nil {
		return model.NotifyDelivery{}, err
	}
	return d, tx.Commit(ctx)
}

// SuppressDelivery files an event as deliberately not sent, with the reason.
//
// A row rather than nothing, because "why did nobody hear about this" is a
// question somebody asks months later, and the answer must be in the database
// rather than in a log that rotated. The reason names the gesture
// ("device.stp_apply"), so the sentence reconstructs itself.
func (s *Store) SuppressDelivery(ctx context.Context, eventID int64,
	channelID, alertKey, reason string) error {
	var _, err = s.pool.Exec(ctx,
		`insert into notify_deliveries
		   (event_id, channel_id, state, alert_key, release_at, suppressed_reason)
		 values ($1, $2::uuid, 'suppressed', $3, now(), $4)`,
		eventID, channelID, alertKey, reason)
	return err
}

// DeliveryWithEvent is a slip and the journal line it points at, read together
// because a message needs both.
type DeliveryWithEvent struct {
	Delivery model.NotifyDelivery
	Event    model.Event
	Channel  model.NotifyChannel
	SiteName string
}

// DueDeliveries returns the slips whose moment has come: held past their
// release, or failed past their retry.
//
// Ordered by release so the oldest alert leaves first — a queue that drains
// newest-first would starve exactly the alert that has been waiting.
func (s *Store) DueDeliveries(ctx context.Context, now time.Time, limit int) ([]DeliveryWithEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows, err = s.pool.Query(ctx,
		`select d.id, d.event_id, d.channel_id::text, d.state, d.alert_key,
		        d.release_at, d.first_at, d.transitions, d.attempts,
		        d.next_attempt_at, d.last_error, d.suppressed_reason,
		        d.created_at, d.sent_at,
		        e.id, e.ts, e.site_id::text, e.device_id::text, e.port_name,
		        e.type, e.severity, e.message, e.actor,
		        c.id::text, coalesce(c.site_id::text, ''), c.kind, c.label,
		        c.enabled, c.target, c.classes, c.min_severity,
		        coalesce(s.name, '')
		 from notify_deliveries d
		   join events e on e.id = d.event_id
		   join notify_channels c on c.id = d.channel_id
		   left join sites s on s.id = e.site_id
		 where (d.state = 'held' and d.release_at <= $1)
		    or (d.state = 'failed' and d.next_attempt_at is not null
		        and d.next_attempt_at <= $1 and d.attempts < $2)
		 order by d.release_at
		 limit $3`, now, NotifyMaxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []DeliveryWithEvent{}
	for rows.Next() {
		var x DeliveryWithEvent
		if err = rows.Scan(&x.Delivery.ID, &x.Delivery.EventID, &x.Delivery.ChannelID,
			&x.Delivery.State, &x.Delivery.AlertKey, &x.Delivery.ReleaseAt,
			&x.Delivery.FirstAt, &x.Delivery.Transitions, &x.Delivery.Attempts,
			&x.Delivery.NextAttempt, &x.Delivery.LastError, &x.Delivery.SuppressedReason,
			&x.Delivery.CreatedAt, &x.Delivery.SentAt,
			&x.Event.ID, &x.Event.TS, &x.Event.SiteID, &x.Event.DeviceID, &x.Event.PortName,
			&x.Event.Type, &x.Event.Severity, &x.Event.Message, &x.Event.Actor,
			&x.Channel.ID, &x.Channel.SiteID, &x.Channel.Kind, &x.Channel.Label,
			&x.Channel.Enabled, &x.Channel.Target, &x.Channel.Classes, &x.Channel.MinSeverity,
			&x.SiteName); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// NotifyMaxAttempts is how many times a delivery is tried before the slip is
// left `failed` for good.
//
// Declared HERE and imported by internal/api rather than duplicated on both
// sides: the retry ceiling appears in the due-query's WHERE clause and in the
// worker's decision to schedule another attempt, and two constants that must
// agree are two constants that eventually will not. internal/store cannot
// import internal/api, so the dependency runs this way round.
const NotifyMaxAttempts = 5

// MarkDeliverySent closes a slip successfully.
func (s *Store) MarkDeliverySent(ctx context.Context, id int64) error {
	var _, err = s.pool.Exec(ctx,
		`update notify_deliveries set state = 'sent', sent_at = now(), last_error = ''
		 where id = $1`, id)
	return err
}

// MarkDeliveryFailed records an attempt that did not land, and when to try again.
//
// `next` nil means "no more attempts": the slip stays `failed` forever and the
// screen counts it. THAT IS THE POINT OF THE LEDGER — the alert existed, the
// product can say so, and nobody had to make detection wait on Telegram.
func (s *Store) MarkDeliveryFailed(ctx context.Context, id int64, why string, next *time.Time) error {
	var _, err = s.pool.Exec(ctx,
		`update notify_deliveries set state = 'failed', attempts = attempts + 1,
		   last_error = $1, next_attempt_at = $2 where id = $3`,
		why, next, id)
	return err
}

// ReArmSuppressed moves suppressed slips back to held once the gesture window
// that excused them has closed and the condition they describe still stands.
//
// This is what makes suppression a DELAY rather than a loss, and it is the half
// of the mechanism that is easy to forget: without it, an outage that happened
// to start four minutes after an apply would be silenced forever by that apply.
// The caller decides "still stands" — the store only moves the rows it is told
// to, since judging a device's current status is not a store's business.
func (s *Store) ReArmSuppressed(ctx context.Context, ids []int64, release time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	// NOT EXISTS is not an optimisation, it is what keeps the statement from
	// failing entirely. `notify_deliveries_held_key_idx` is UNIQUE over
	// (channel_id, alert_key) WHERE state = 'held'; re-arming a slip whose key
	// already holds one would violate it and roll back the whole UPDATE, so a
	// single collision would silently block every other re-arm in the batch.
	// A key that already has a slip waiting needs no second one anyway — that
	// slip is the message.
	var _, err = s.pool.Exec(ctx,
		`update notify_deliveries d set state = 'held', release_at = $2,
		   suppressed_reason = suppressed_reason || ' (re-armed)'
		 where d.id = any($1::bigint[]) and d.state = 'suppressed'
		   and not exists (
		     select 1 from notify_deliveries h
		     where h.channel_id = d.channel_id and h.alert_key = d.alert_key
		       and h.state = 'held')`, ids, release)
	return err
}

// SuppressedPastWindow returns suppressed slips whose gesture window has
// closed, so the caller can re-judge them.
//
// NOTE ON THE CAST: `$1::timestamptz` is explicit because without it PostgreSQL
// cannot infer the left operand of the subtraction, infers `interval`, and the
// whole predicate fails with "operator does not exist: timestamp with time zone
// <= interval". TestReArmReturnsADeferredAlert caught exactly that, which is
// why it exists — the re-arm sweep is the half of suppression that turns a
// delay into a loss when it silently stops working.
func (s *Store) SuppressedPastWindow(ctx context.Context, now time.Time,
	window time.Duration, limit int) ([]DeliveryWithEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows, err = s.pool.Query(ctx,
		`select d.id, d.event_id, d.alert_key, d.suppressed_reason,
		        e.id, e.ts, e.site_id::text, e.device_id::text, e.port_name,
		        e.type, e.severity, e.message
		 from notify_deliveries d join events e on e.id = d.event_id
		 where d.state = 'suppressed'
		   and d.suppressed_reason not like '%(re-armed)'
		   and e.ts <= $1::timestamptz - $2::interval
		 order by e.ts limit $3`, now, window, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = []DeliveryWithEvent{}
	for rows.Next() {
		var x DeliveryWithEvent
		if err = rows.Scan(&x.Delivery.ID, &x.Delivery.EventID, &x.Delivery.AlertKey,
			&x.Delivery.SuppressedReason,
			&x.Event.ID, &x.Event.TS, &x.Event.SiteID, &x.Event.DeviceID,
			&x.Event.PortName, &x.Event.Type, &x.Event.Severity, &x.Event.Message); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// PortFacesKnownDevice reports whether a DECLARED link touches this port.
//
// The declared half of "interco" only. The observed half — an LLDP neighbour
// that resolves to a fleet device — is derived at read time by
// api.IdentifyLLDPNeighbors and cannot be answered in SQL, because recognition
// depends on inventory and on three ranked identity sources (chassis, port id,
// system name). Splitting it this way keeps the cheap test cheap: a declared
// link is one index lookup, and most intercos on a mapped site have one.
func (s *Store) PortFacesKnownDevice(ctx context.Context, siteID, deviceID, port string) (bool, error) {
	var found bool
	var err = s.pool.QueryRow(ctx,
		`select exists (
		   select 1 from device_links
		   where site_id = $1::uuid
		     and ((a_device = $2::uuid and a_port = $3)
		       or (b_device = $2::uuid and b_port = $3)))`,
		siteID, deviceID, port).Scan(&found)
	return found, err
}

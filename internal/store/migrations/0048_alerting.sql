-- 0048 — Alerting: channels, and one delivery slip per log line.
--
-- ADR-0024. Two tables, and the shape of the second is the decision:
--
--   notify_channels     — where to send, and with which secret
--   notify_deliveries   — WHAT was sent, anchored on events.id
--
-- WHY NO `alerts` TABLE. An alert is not a new fact: it is a log line judged
-- worthy of waking somebody up. Giving it its own table would create a second
-- truth next to `events`, and two systems that separately decide whether a
-- device went down diverge — the day the channel says one thing and the screen
-- the other, trust in both goes. The foreign key to events(id) makes every
-- message traceable to the line that caused it, which a standalone table does
-- not allow.

create table if not exists notify_channels (
    id          uuid primary key default gen_random_uuid(),
    -- NULL site_id = an instance channel: it receives the events of every site.
    -- Same sharing as events.site_id, and for the same reason (a multi-site
    -- operator wants a single channel).
    site_id     uuid references sites(id) on delete cascade,
    kind        text not null,                      -- telegram | slack
    label       text not null default '',
    enabled     boolean not null default true,

    -- target: what is NOT secret and serves for addressing. For Telegram, the
    -- chat_id (an integer, sometimes negative for a group); for Slack, empty — the
    -- webhook URL carries both the address and the secret, so it is entirely in
    -- token_enc.
    target      text not null default '',

    -- token_enc: sealed by internal/secretbox. A Telegram bot token, or a Slack
    -- webhook URL. LISTED IN store.SealedColumns() — a sealed column missing from
    -- that inventory is destroyed at the next key rotation, without a word.
    token_enc   bytea not null,

    -- classes: a subset of {device, interco, described}. The default is all three,
    -- i.e. the request as it was phrased. An array and not three booleans: the list
    -- of classes may grow (ADR-0024 already names a possible one, the gesture made
    -- outside meshcraft) and a column per class would force a migration on every
    -- addition.
    classes     text[] not null default array['device','interco','described'],

    -- min_severity: a floor applied AFTER the classes. model.SevNotice = 2 by
    -- default, which lets recoveries (SevNotice) and problems (SevWarning) through
    -- and holds back ordinary port transitions (SevInfo) — which only get past the
    -- classes anyway if the port is described or an interco.
    min_severity smallint not null default 2,

    -- Channel health. It is the answer to the ambiguous silence: "the channel sent
    -- nothing" becomes a question the screen answers, without having to send a
    -- heartbeat nobody reads.
    last_ok_at   timestamptz,
    last_error   text not null default '',
    consecutive_failures integer not null default 0,

    -- dropped_uninteresting: how many port events were discarded for lack of a
    -- description and of a known neighbour. ADR-0024's blind spot made into a
    -- VISIBLE FIGURE instead of a silence — it is the only honest mitigation of the
    -- exclusion rule.
    dropped_uninteresting bigint not null default 0,

    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now()
);

create index if not exists notify_channels_site_idx on notify_channels (site_id);

-- A channel is identified by its (kind, target, site) triple from the operator's
-- point of view, but it is not enforced: two Telegram channels to the same group
-- with different classes is a legitimate configuration ("the intercos here,
-- everything else there").

create table if not exists notify_deliveries (
    id          bigint generated always as identity primary key,
    -- ON DELETE CASCADE: if a log line disappears, its slip no longer makes sense.
    -- events has no purge today (see ADR-0024, Consequences), but the day it has
    -- one, the slip must follow.
    event_id    bigint not null references events(id) on delete cascade,
    channel_id  uuid not null references notify_channels(id) on delete cascade,

    -- state : held → sent | failed | coalesced | suppressed
    --
    --   held       held back, not delivered yet — release_at says when
    --   coalesced  absorbed by another slip with the same key (the flapping)
    --   suppressed discarded: a meshcraft gesture, or an unsubscribed class
    --   sent       gone out, and accepted by the channel
    --   failed     attempts exhausted — THE ALERT EXISTED, and that is the point
    state       text not null default 'held',

    -- alert_key: (class, device, port). It is the flapping grouping key. Derived
    -- on write and stored, unlike "interco" which is derived on read: here it MUST
    -- be frozen, because grouping requires comparing two slips written at different
    -- instants, and a port described in the meantime would change key in the middle
    -- of an episode.
    alert_key   text not null,

    -- release_at: the hold deadline. Pushed back by every new transition on the
    -- same key (ADR-0024 rule 4: a key that moves does not alert), capped at
    -- alertHoldMax from the first event.
    release_at  timestamptz not null,
    -- first_at: the episode's first event, to compute the cap without re-reading
    -- the chain of absorbed slips.
    first_at    timestamptz not null default now(),
    -- transitions: how many events this delivery sums up. 1 = a clean transition,
    -- N = "this port changed state N times".
    transitions integer not null default 1,

    attempts    integer not null default 0,
    next_attempt_at timestamptz,
    last_error  text not null default '',
    suppressed_reason text not null default '',

    created_at  timestamptz not null default now(),
    sent_at     timestamptz
);

-- The worker's index: "what is due?". Partial, because the terminal states will
-- be most of the table in time and no longer interest the loop.
create index if not exists notify_deliveries_due_idx
    on notify_deliveries (release_at)
    where state = 'held';

create index if not exists notify_deliveries_retry_idx
    on notify_deliveries (next_attempt_at)
    where state = 'failed' and next_attempt_at is not null;

-- The grouping index: find this key's held slip on this channel. Unique and
-- partial — AT MOST ONE held slip per (channel, key), and that is what makes
-- grouping correct under concurrency rather than probable: two transitions
-- arriving in parallel cannot create two holds on the same key, the second
-- fails and absorbs the first.
create unique index if not exists notify_deliveries_held_key_idx
    on notify_deliveries (channel_id, alert_key)
    where state = 'held';

create index if not exists notify_deliveries_event_idx on notify_deliveries (event_id);

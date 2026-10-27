-- Pipelines: make visible what is in progress, and above all what is stuck.
-- See docs/en/01-Architecture/Unified monitoring and pipelines.md §5.
--
-- Until now, a push that failed left an event and nothing else: one had to
-- guess. A configuration deployment, a credential push, a monitoring campaign
-- take time, fail partially and get stuck — that is the rule, not the
-- exception, on a real fleet.
create table if not exists pipelines (
    id         uuid primary key default gen_random_uuid(),
    site_id    uuid not null references sites(id) on delete cascade,
    -- kind : config | credential | monitoring | import
    kind       text not null,
    -- null device_id = a fleet campaign, which belongs to no device.
    device_id  uuid references devices(id) on delete cascade,
    -- state : pending | running | blocked | done | failed
    --
    -- `blocked` is NOT a failure: a device switched off, a disconnected proxy, a
    -- closed maintenance window. The pipeline waits, and says for what. Confusing
    -- them pushes people to relaunch what does not need it.
    state      text not null default 'pending',
    -- label: what the pipeline does, in plain words, for a readable list.
    label      text not null default '',
    -- blocked_reason is a SENTENCE, not a code. "Waiting: proxy `proxy-festival`
    -- has been disconnected for 4 min" reads; ERR_PROXY_DOWN has to be looked up.
    blocked_reason text not null default '',
    created_at timestamptz not null default now(),
    started_at timestamptz,
    ended_at   timestamptz,
    created_by uuid references users(id) on delete set null
);

create index if not exists pipelines_site_idx on pipelines (site_id, created_at desc);
-- States that call for an action are looked up first.
create index if not exists pipelines_state_idx on pipelines (state, created_at desc);

create table if not exists pipeline_steps (
    id          uuid primary key default gen_random_uuid(),
    pipeline_id uuid not null references pipelines(id) on delete cascade,
    seq         int not null,
    name        text not null,
    -- state : done | failed | skipped
    --
    -- `skipped` exists because a guardrail that refuses to act is neither a success
    -- nor a failure: it is a decision, and it must read as one.
    state       text not null default 'done',
    -- detail: what the device ACTUALLY answered. On a product line where a mistake
    -- costs a trip, knowing afterwards what was sent is the only way to understand
    -- without going back on site.
    detail      text not null default '',
    created_at  timestamptz not null default now()
);

create index if not exists pipeline_steps_idx on pipeline_steps (pipeline_id, seq);

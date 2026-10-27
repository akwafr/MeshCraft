-- The OBSERVED LLDP neighbourhood, and its separation from ENTERED links.
--
-- See docs/en/01-Architecture/LLDP neighbours.md, which carries the hardware
-- survey the whole shape of these two tables follows from.
--
-- # Why this is not `device_links`
--
-- The question deserves to be settled here rather than rediscovered, because the
-- temptation is real: `device_links` already carries (a_device, a_port, b_device,
-- b_port), i.e. exactly the shape of a link. Three reasons rule it out, and each
-- would be enough:
--
--   1. **an intent and a finding are not refuted the same way.** A
--      `device_links` link is what an operator DECLARED: it survives an outage,
--      an unplugging, a switch turned off — that is its whole point, since it is
--      what reality is compared against. An LLDP neighbour is what a device SAW
--      thirty seconds ago. Mixing them would make an entered link DISAPPEAR
--      because a switch was unplugged for ten seconds, or the reverse: keep alive
--      a neighbour nobody sees any more;
--   2. **both ends of `device_links` are `not null` with a foreign key to
--      `devices`.** A neighbour whose chassis is not recognised — an ISP router,
--      an access point that is not in meshcraft, the S3900 found on the fleet —
--      is structurally INEXPRESSIBLE in that table. Yet it is the most frequent
--      case in operation, and leaving it out would lose most of what LLDP brings;
--   3. **a port may carry SEVERAL neighbours.** Measured, not assumed: port
--      `sfp-sfpplus2` of the CRS326 sees THREE, because an intermediate switch
--      that does not consume LLDP floods its frames instead of absorbing them.
--      The primary key of these tables must therefore admit several rows per
--      port, which a topology link does not.
--
-- # What the vault had sketched, and what changed
--
-- `docs/en/04-Data/Database schema.md` sketched an `lldp_links` table carrying
-- `confirmed bool`. That is NOT what ships, and the difference is deliberate:
-- `confirmed` — "both sides see each other" — is a DERIVATION of two findings,
-- not a fact. Stored, it goes stale as soon as one of the two sides stops seeing
-- the other, and a path would then be needed to undo it; it would remain to be
-- decided whether A's next snapshot or B's has the right to erase it. It is
-- computed on READ by a pure function (`api.deriveLLDPLinks`), and nothing can
-- make it lie.
--
-- # Two tables and not one
--
-- `lldp_neighbors` carries what a device SAW. `lldp_self` carries what it
-- ANNOUNCES. Separating them is not elegance: it is what makes matching possible
-- at all, and the survey proves it with a case nobody would have guessed — the
-- 1930 declares `lldpLocChassisId = 0x02005e1061a7` but emits, through its port
-- 50, frames whose chassis-id is `0x02005e1061d9`, that port's MAC. The CRS326
-- therefore records a neighbour whose identifier appears nowhere in the 1930's
-- declared identity. Without `lldp_self.port_ids`, the reference fleet's only
-- bilateral link would show as seen from one side only.

-- lldp_self: the LLDP identity a device ANNOUNCES.
--
-- One row per polled device, replaced whole on every snapshot. No `first_seen`:
-- it is not a finding about the world, it is the device's business card, and
-- "since when has it announced this name" interests nobody.
create table lldp_self (
    device_id uuid primary key references devices(id) on delete cascade,
    -- site_id is DUPLICATED from devices, and that is accepted: every read is per
    -- site (a weathermap, a devices page), and going through a join on `devices`
    -- every time would cost more than this column. `on delete cascade` on both: a
    -- deleted site or a deleted device has no neighbourhood.
    site_id   uuid not null references sites(id) on delete cascade,
    -- chassis_id: AS THE DEVICE SAID IT, up to lossless conversion. Not normalised in
    -- the database, on purpose. The encoding is not uniform — the CRS326 returns the
    -- text "B8:69:F4:5B:A0:D2", the 1930 six raw bytes hence "0xb869f45ba0d2" for the
    -- SAME chassis — and normalisation is a matching decision, which lives in a pure,
    -- tested function (`api.normalizeChassisID`). Storing the normalised form would
    -- make it impossible to tell an operator what their device actually answered,
    -- which is the only thing that allows a missed match to be understood.
    chassis_id text not null default '',
    -- sys_name: the name the device gives itself, which is NOT its meshcraft name.
    -- Surveyed: `lab-crs326` announces itself as "lan-r4", `sw-labo-1930` by its
    -- serial number "CN0EXAMPL1". That is precisely why matching by name is a LAST
    -- resort and not the first.
    sys_name   text not null default '',
    -- port_ids: this device's `lldpLocPortId`s. See the header: it is the field that
    -- catches a vendor announcing its port's MAC as the chassis-id.
    port_ids   text[] not null default '{}',
    -- served: does this device serve the LLDP-MIB?
    --
    -- False and "zero neighbours" do not mean the same thing, and confusing them
    -- would be the most expensive mistake of this batch. Zero neighbours is a
    -- FINDING: LLDP runs, nobody on the other side. Not serving the MIB means we know
    -- NOTHING, and no version of meshcraft will change that — measured on the
    -- CSS326, whose walk of the whole `1.0.8802` returns zero rows without error.
    -- The screen must say "we do not know", never "nothing".
    served     boolean not null default false,
    -- seen_at: when this snapshot was taken, on the CONTROLLER's clock.
    --
    -- Never the proxy's, and that is the rule the traps batch paid dearly to
    -- establish (see Controller-proxy protocol, "Event.TS is a diagnostic fact"): a
    -- date supplied by a proxy says what the proxy believes, and the measured skew
    -- was eleven seconds. Here the consequence would be directly visible — "seen for
    -- 3 min" shown when hovering a port, computed against the browser's clock.
    seen_at    timestamptz not null default now()
);

create index lldp_self_site_idx on lldp_self (site_id);

-- lldp_neighbors: a dated FINDING. "At this instant, on this port, this device
-- received an LLDP frame that presented itself like this."
create table lldp_neighbors (
    device_id uuid not null references devices(id) on delete cascade,
    site_id   uuid not null references sites(id) on delete cascade,
    -- local_port: the FACEPLATE PORT name, resolved by the CONTROLLER.
    --
    -- The proxy sends what the device said ("sfp-sfpplus2", "50"); the controller
    -- matches it with the faceplate through `facadeOf`/`resolve`, because only it
    -- knows `port_groups`. A label that cannot be attached is DISCARDED and never
    -- arrives here: a neighbour placed on the wrong port draws a credible and false
    -- link, which is worse than a hole.
    local_port text not null,
    -- local_port_num: the raw `lldpLocPortNum`, for DIAGNOSIS. It is what remains to
    -- understand which port the device meant when resolution fails — except that in
    -- that case the row is not written. It mostly serves to re-read an attachment
    -- one doubts.
    local_port_num integer not null default 0,
    -- What the neighbour said about itself, as is. `port_id` AND `port_desc` because
    -- neither is served everywhere: the CRS326 serves NO `lldpRemPortDesc` column
    -- (empty walk, measured), the 1930 serves both.
    chassis_id text not null default '',
    port_id    text not null default '',
    port_desc  text not null default '',
    sys_name   text not null default '',
    sys_desc   text not null default '',
    -- first_seen: since when this neighbour has been seen on this port, WITHOUT interruption.
    --
    -- It is half of this table's operational value, and that is why it is preserved
    -- across snapshots instead of being rewritten (see store.ReplaceLLDP, whose
    -- `upsert` does not touch this column). A neighbour that disappears then comes
    -- back starts again from zero, and that is the right behaviour: the cable moved,
    -- the age of the previous finding no longer says anything.
    first_seen timestamptz not null default now(),
    -- last_seen: the last snapshot in which this neighbour was there. On the
    -- controller's clock, same reason as `lldp_self.seen_at`.
    last_seen  timestamptz not null default now(),
    -- The key carries the NEIGHBOUR and not only the port, because a port may see
    -- several — measured, three on a single CRS326 port. A (device_id, local_port)
    -- key would have overwritten two findings out of three, and the screen would
    -- have shown a neighbour picked at random among those that exist.
    --
    -- `port_id` is part of the key in addition to the chassis: two ports of the same
    -- neighbour may arrive on the same local port through a transparent switch, and
    -- they are two distinct findings.
    primary key (device_id, local_port, chassis_id, port_id)
);

-- Read per SITE (the weathermap, the devices page) and per DEVICE (hovering a
-- port). The primary key already covers the second case.
create index lldp_neighbors_site_idx on lldp_neighbors (site_id);

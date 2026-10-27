-- Site extensions, and the read-only mirror they import (ADR-0015, ADR-0016).
--
-- A site extension backs a meshcraft site with a FOREIGN controller — the first
-- incarnation being UniFi OS. Three things had to be decided before this schema
-- could be written, and ADR-0015 decided them:
--
--   * the extension runs IN the controller, so there is no instance table here
--     (contrast agent_repos/0044, whose product is an OCI layout). What the DB
--     keeps is the declaration, the pairing, and the mirror.
--   * the token is a per-SITE secret, so it lives in the vault like any site
--     credential — sealed in `token_enc`, revealed only by an explicit traced
--     gesture. It is NOT read from .env.local, which is a dev bootstrap only.
--     `token_enc` is therefore registered in store.SealedColumns(): a sealed
--     column missing from that inventory is a secret destroyed at key rotation.
--   * the mirror is READ-ONLY and lands in its own tables. Nothing imported
--     ever enters `devices`, which carries the invariant "something meshcraft
--     configures, with a connector". A UniFi switch already has a master that
--     re-provisions it on its own schedule; two write authorities on one box do
--     not raise an error, they silently revert.

CREATE TABLE site_extensions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    site_id     UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- Which extension this is. A DATA value, not a Go `switch`: UniFi OS is the
    -- first incarnation, deliberately not the only one foreseen.
    kind        TEXT NOT NULL,
    -- Operator-chosen label; empty falls back to the kind in the UI.
    label       TEXT NOT NULL DEFAULT '',

    -- --- How we reach the foreign controller ---
    -- Base URL, scheme included (https://host:port). Stored without a trailing
    -- slash: UniFi's nginx answers 400 to the "//proxy/..." a naive join makes,
    -- which costs an hour to diagnose the first time.
    base_url    TEXT NOT NULL,
    token_enc   BYTEA NOT NULL,
    -- UniFi OS ships a self-signed certificate on-premise, so FALSE is a real
    -- operational need, not laziness. It is explicit per extension so that
    -- turning verification off is a recorded choice on one controller rather
    -- than a global relaxation.
    verify_tls  BOOLEAN NOT NULL DEFAULT TRUE,
    -- Reachability is a CONFIGURATION, not an architecture (ADR-0015 §3). A
    -- controller shut inside a site LAN is reached through the proxy already
    -- posted there; that is what spared us a container for an HTTP client.
    transport   TEXT NOT NULL DEFAULT 'direct',
    proxy_id    UUID REFERENCES proxies(id) ON DELETE SET NULL,
    enabled     BOOLEAN NOT NULL DEFAULT TRUE,

    -- --- The pairing (ADR-0015 §6) ---
    -- The foreign site's STABLE identifier. For UniFi this is the integration
    -- API's `id` (a UUID). This is the key; the name is not.
    remote_site_id   TEXT NOT NULL DEFAULT '',
    -- UniFi's short `internalReference`. Kept because the SSID endpoint is only
    -- available on the LEGACY API, which is indexed by this and not by the UUID.
    remote_site_ref  TEXT NOT NULL DEFAULT '',
    -- The display name seen at the last contact — "lab Events #04 - ARENA".
    -- A cached label, never a key: it will be renamed and the pairing must
    -- survive it (same rule as ADR-0014).
    remote_site_name TEXT NOT NULL DEFAULT '',
    paired_at        TIMESTAMPTZ,
    -- Set when the controller ANSWERED and the paired id was absent from its
    -- site list. Deliberately distinct from "controller unreachable": only the
    -- former orphans, because a ten-minute network outage must not erase
    -- somebody's correlation work. Nothing is ever deleted on orphaning; the
    -- mirror stays visible and stamped stale.
    orphaned_at      TIMESTAMPTZ,

    -- --- Last contact ---
    last_sync_at TIMESTAMPTZ,
    -- Verbatim failure of the last attempt, shown to the operator. An extension
    -- that fails must explain itself on its own screen; it must never be the
    -- failure of a page.
    last_error   TEXT NOT NULL DEFAULT '',

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by  TEXT NOT NULL DEFAULT '',

    CONSTRAINT site_extensions_transport CHECK (transport IN ('direct', 'proxy')),
    -- One extension of a given kind per site. Two UniFi controllers behind one
    -- meshcraft site is not a case anybody has; if it appears, this is the line
    -- to relax, and the UI already keys on the pair.
    UNIQUE (site_id, kind)
);
CREATE INDEX site_extensions_site ON site_extensions (site_id);

-- The imported inventory. ONE table with a class discriminator rather than one
-- table per class: these rows are all the same KIND of thing — a read-only
-- mirror row keyed by (extension, foreign id) — and what differs between an
-- access point and a switch is which columns are populated, not their lifecycle.
--
-- `class` is the honest answer to ADR-0016: a Wi-Fi access point is not a
-- switch and not a `devices` row. It has radios, bands, channels and associated
-- clients; an access port has none of those five.
CREATE TABLE site_extension_devices (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    extension_id UUID NOT NULL REFERENCES site_extensions(id) ON DELETE CASCADE,
    -- Denormalized so "what did this site import" is one index hit, and so the
    -- cascade from sites(id) reaches these rows by the shortest path.
    site_id      UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    -- The foreign controller's own id for this box. The import key.
    external_id  TEXT NOT NULL,
    class        TEXT NOT NULL DEFAULT 'other',
    name         TEXT NOT NULL DEFAULT '',
    model        TEXT NOT NULL DEFAULT '',
    firmware     TEXT NOT NULL DEFAULT '',
    -- The foreign controller's own word for the state, lowercased. NOT mapped
    -- onto devices.status (ok|warn|off|unknown): mapping it would invent a
    -- judgement meshcraft has no basis to make on a box it does not poll.
    state        TEXT NOT NULL DEFAULT '',
    mgmt_ip      TEXT NOT NULL DEFAULT '',
    -- Lowercase colon-separated. This is the ONLY bridge to the switched world
    -- (ADR-0016 §3): LLDP and the FDB already know this address, so "which port
    -- is this access point on" is answered by a join at read time, without
    -- making the access point an equipment.
    mac          TEXT NOT NULL DEFAULT '',
    -- Radios as a typed JSON child, not as rows and never as ports. A radio has
    -- no identity beyond (device, band), nothing references it, and it is a
    -- SNAPSHOT — so a child table would buy keys nobody dereferences.
    radios       JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- The few copper ports an access point does have (two, on an AC Pro). Here
    -- so the uplink speed is visible; emphatically not a faceplate.
    ports        JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Vendor-opaque leftovers (UniFi's configurationId, adoptedAt…). Kept raw
    -- because they do not translate to a second Wi-Fi vendor and must not
    -- pollute the neutral 802.11 columns above.
    vendor_extra JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- When the import last SAW this row. Freshness is read against the viewer's
    -- clock (ADR-0008); a channel shown here may be stale and says so.
    seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT site_extension_devices_class
        CHECK (class IN ('access_point', 'switch', 'gateway', 'other')),
    UNIQUE (extension_id, external_id)
);
CREATE INDEX site_extension_devices_site ON site_extension_devices (site_id, class, name);
CREATE INDEX site_extension_devices_mac ON site_extension_devices (mac) WHERE mac <> '';

-- SSIDs belong to the SITE, not to an access point (ADR-0016 §1 and its last
-- rejected alternative). UniFi configures them at site level and every access
-- point broadcasts them; one row per access point would create N copies to
-- reconcile, and the first divergence between two copies would be
-- indistinguishable from a real configuration drift.
CREATE TABLE site_extension_ssids (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    extension_id UUID NOT NULL REFERENCES site_extensions(id) ON DELETE CASCADE,
    site_id      UUID NOT NULL REFERENCES sites(id) ON DELETE CASCADE,
    external_id  TEXT NOT NULL,
    name         TEXT NOT NULL DEFAULT '',
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    -- 802.11 vocabulary, not UniFi's: `security` and `wpa_mode` carry what the
    -- vendor said, normalized to lowercase, so a second vendor can fill them.
    -- No PSK is imported, ever — reading a controller does not mean copying the
    -- passphrases of a production network into a second database.
    security     TEXT NOT NULL DEFAULT '',
    wpa_mode     TEXT NOT NULL DEFAULT '',
    -- '2g' / '5g' / '6g'.
    bands        TEXT[] NOT NULL DEFAULT '{}',
    seen_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (extension_id, external_id)
);
CREATE INDEX site_extension_ssids_site ON site_extension_ssids (site_id, name);

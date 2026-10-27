package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// readTools: everything that cannot change the fleet.
//
// # Why these are not one tool per route
//
// The controller mounts a few hundred handlers. Exposed mechanically, that is a
// few hundred schemas sitting in the agent's context on EVERY request, to answer
// questions like "is this site healthy?" that no single route answers anyway.
// The grouping below is by the question an operator actually asks, which is why
// some tools fan out to several routes (site_health) and why several routes with
// no vocabulary of their own are left to api_get.
func readTools() []Tool {
	return []Tool{
		{
			Name:       "whoami",
			Title:      "Who this token is, and what it may do",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Description: `Identify the account this server's API token belongs to, list the sites it can reach and the role it holds on each, and report the controller version.

CALL THIS FIRST. Every other tool is bounded by what this returns, and nothing else reveals those bounds: the token carries no powers of its own, only the powers of this user (ADR-0011), so "can I do X" is always answered here and never by the tool list.

Roles are ordered and a user holds exactly ONE per site:
  - viewer     — read only.
  - operator   — viewer, plus edit intents and profiles, apply them, and adopt discovered devices.
  - site_admin — operator, plus the site's VLANs, management plan, credentials, a device's address domain and collector attachment.
A fourth level, global admin, is NOT a role in this list: it is a flag on the account (is_global_admin) that short-circuits every per-site check. Looking for it among the roles will never find it, so read the flag.

Also reports this server's exposure mode, which is a second and independent limit: in read-only mode no writing tool is offered at all, whatever the token could do.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var me, err = c.GetJSON(ctx, "/api/auth/me")
				if err != nil {
					return "", err
				}
				var out = map[string]any{"identity": me}
				// Version is best-effort: knowing who you are is useful even on an
				// instance that will not say what it is running.
				var version, verr = c.GetJSON(ctx, "/api/version")
				if verr == nil {
					out["controller"] = version
				}
				out["mcp_server"] = map[string]any{
					"mode": activeMode.String(),
					"note": modeNote(activeMode),
				}
				return jsonOut(out)
			},
		},
		{
			Name:       "list_sites",
			Title:      "Sites this token can reach",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Description: `List the sites visible to this identity, with their ids.

A site is the unit of tenancy AND of authorisation: every device, VLAN, profile, credential and management plan belongs to exactly one site, and roles are granted per site. A site is not a network — two sites may reuse the same private addressing (see address domains) — and a site does not own a proxy, because proxies are shared across sites by their coverage.

Most other tools need a site_id from here.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				return relay(ctx, c, "/api/sites")
			},
		},
		{
			Name:       "site_health",
			Title:      "One site's state in a single answer",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "site_id", Type: "string", Desc: "Site to summarise (from list_sites).", Req: true},
				{Name: "events", Type: "integer", Desc: "How many recent journal entries to include. Default 20, 0 to omit."},
			},
			Description: `Answer "what is the state of this site, and what is wrong with it?" in one call: the devices with their conformity verdicts, the operations in flight or stuck, and the tail of the event journal.

This fans out to several routes because no single route answers the question. Start here when asked to diagnose or report on a site, then narrow with get_device or get_pipeline.

Reading the verdicts it returns:
  - Conformity is COMPUTED ON READ, never stored, because the site plan and the device name can change at any moment and a stored boolean would start lying at the first edit.
  - The axes are deliberately separate and must not be merged. reachability (does it answer ping) is not mgmt conformity (is it in the site's management VLAN and subnet): a device can answer ping perfectly and still be off-plan. Off-plan is a warning with a reason, never an outage.
  - "unknown" and "absent" are not the same claim and the difference governs what you may conclude. absent is a CERTAINTY ("no port with an active link carries this VLAN") and it blocks. unknown is IGNORANCE ("this device has never been read back") and it must not block. Do not report an unknown as a fault.
  - A pipeline in state "blocked" is WAITING, not failed — a powered-off device, a disconnected proxy — and its reason is a sentence written for you to read. Re-running it changes nothing. Only "failed" asks to be replayed.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				var out = map[string]any{"site_id": siteID}
				// Devices carry the conformity verdicts, so they are the spine of
				// this answer and the one part whose failure is fatal.
				var devices, devErr = c.GetJSON(ctx, "/api/sites/"+esc(siteID)+"/devices")
				if devErr != nil {
					return "", devErr
				}
				out["devices"] = devices

				// The rest is best-effort and each failure is REPORTED rather than
				// dropped: a summary silently missing its pipelines reads as "nothing
				// is stuck", which is the opposite of what a failed fetch means.
				var problems = map[string]string{}
				var pipelines, pipeErr = c.GetJSON(ctx, "/api/sites/"+esc(siteID)+"/pipelines")
				if pipeErr == nil {
					out["pipelines"] = pipelines
				} else {
					problems["pipelines"] = pipeErr.Error()
				}
				var limit = a.Int("events", 20)
				if limit > 0 {
					var events, evErr = c.GetJSON(ctx,
						"/api/sites/"+esc(siteID)+"/events?limit="+strconv.Itoa(limit))
					if evErr == nil {
						out["recent_events"] = events
					} else {
						problems["events"] = evErr.Error()
					}
				}
				if len(problems) > 0 {
					out["could_not_read"] = problems
				}
				return jsonOut(out)
			},
		},
		{
			Name:       "list_devices",
			Title:      "A site's devices, with their verdicts",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props:      []P{{Name: "site_id", Type: "string", Desc: "Site to list.", Req: true}},
			Description: `List a site's devices with their conformity verdicts and monitoring gaps. This is the inventory, and the route that publishes the verdicts.

A device is an adopted switch, identified preferentially by serial number, then MAC, then (address domain, IP). Its management IP is OBSERVED, never decreed.

Each entry carries two fields of the same shape that must not be confused: "ports" is the INTENT meshcraft wants the device to carry, "observed" is what it was last read as carrying. The rule throughout is that absent means unmanaged — a port meshcraft does not mention is one it does not manage, not one it wants empty.

Use site_health instead when the question is "what is wrong here", and get_device for one device with its tags.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				return relay(ctx, c, "/api/sites/"+esc(siteID)+"/devices")
			},
		},
		{
			Name:       "get_device",
			Title:      "One device, assembled",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "site_id", Type: "string", Desc: "The device's site. Optional, but supplying it makes this one call instead of a search across every visible site."},
				{Name: "include_tags", Type: "boolean", Desc: "Also fetch the device's tags. Default true."},
			},
			Description: `Assemble one device's sheet: inventory, the per-port intent, the observed state, the faceplate, and the conformity verdicts.

There is deliberately NO single-device route on this API, and that is the reason this tool exists rather than wrapping one. The conformity verdict is published only by the device LIST; a sheet fetched on its own would report every axis as ABSENT rather than UNKNOWN, which reads as a lie rather than as a hole. So this reads the site's list and projects your device out of it, keeping the verdicts attached.

Two fields in the result are the same shape and must not be confused, because telling them apart is how you say which side is behind:
  - ports    — the INTENT, what meshcraft wants this device to carry.
  - observed — the MEASUREMENT, what it was last read as carrying.
A gap between them is only drift if someone has read the device back: drift exists from the moment of a read-back, not from the moment of the change. A device never read back is "unknown", which is the truth and not a fault.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var siteID string
				siteID, err = a.String("site_id")
				if err != nil {
					return "", err
				}
				var searched []string
				if siteID == "" {
					siteID, searched, err = findDeviceSite(ctx, c, deviceID)
					if err != nil {
						return "", err
					}
				}
				var device, findErr = deviceFromSite(ctx, c, siteID, deviceID)
				if findErr != nil {
					return "", findErr
				}
				var out = map[string]any{"site_id": siteID, "device": device}
				if len(searched) > 0 {
					out["searched_sites"] = searched
				}
				var wantTags = true
				var _, present = a["include_tags"]
				if present {
					wantTags = a.Bool("include_tags")
				}
				if wantTags {
					var tags, tagErr = c.GetJSON(ctx, "/api/devices/"+esc(deviceID)+"/tags")
					if tagErr == nil {
						out["tags"] = tags
					} else {
						out["could_not_read_tags"] = tagErr.Error()
					}
				}
				return jsonOut(out)
			},
		},
		{
			Name:       "get_device_telemetry",
			Title:      "A device's measurements over a window",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true},
				{Name: "minutes", Type: "integer", Desc: "Window to look back over, in minutes. The controller caps it and reports the window it actually used."},
			},
			Description: `Read a device's time series: temperature, load, memory, uptime, error counters, per-port rates and optical levels.

Telemetry is MEASUREMENT and never intent. It is never compared against configuration and never creates a port — a measurement describes a port, it does not discover one — so nothing here is a conformity gap or something to "fix" by applying configuration.

Conventions worth knowing when reading the names back: the unit is always in the name (_bytes, _seconds, _celsius, _dbm), a _total suffix means a monotonic counter and nothing else, and labels carry identity rather than value.

If the instance has no time-series store the answer says so in a "why" field rather than returning an empty frame that would look like a fault. An optical level of about -30 dBm means no light, not a weak signal.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				var path = "/api/devices/" + esc(deviceID) + "/telemetry"
				var minutes = a.Int("minutes", 0)
				if minutes > 0 {
					path += "?minutes=" + strconv.Itoa(minutes)
				}
				return relay(ctx, c, path)
			},
		},
		{
			Name:       "list_vlans",
			Title:      "A site's VLANs",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props:      []P{{Name: "site_id", Type: "string", Desc: "Site to list.", Req: true}},
			Description: `List a site's VLANs (id, name, colour, purpose). A device's port intent references these by VLAN id.

VLAN 1 is present on every site and is not an operator decision but a hardware fact: every switch ships with it and none can shed it. It is created with the site, it cannot be deleted, and its vlan_id cannot be changed — renaming or recolouring it is allowed, because that is presentation. Relatedly, a trunk port with no explicit native VLAN has VLAN 1 as native; that default exists to prevent a false drift, because the device always reports a native VLAN even when it was given none.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				return relay(ctx, c, "/api/sites/"+esc(siteID)+"/vlans")
			},
		},
		{
			Name:       "list_profiles",
			Title:      "Port profiles and switch profiles",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "site_id", Type: "string", Desc: "Site to list.", Req: true},
				{Name: "kind", Type: "string", Desc: "Which family to list.", Enum: []string{"port", "switch", "device"}, Req: true},
			},
			Description: `List a site's profiles. The two main kinds are NOT the same object and confusing them is the usual mistake:

  - kind="port"   — a PORT PROFILE: a named spec for what ONE socket receives ({mode, pvid, tagged, poe}). It never says which sockets.
  - kind="switch" — a SWITCH PROFILE: an ordered RULE SET evaluated against a device's whole front panel, which is what decides which sockets get what.

How a switch profile resolves, because the result is unreadable without it: the faceplate is expanded into ranked sockets, each with a derived media (copper, sfp, sfp_plus). Each rule has a selector (media, PoE capability, group label) and then a slice — all, first N, last N, range N..M — and the slice is on RANK, never on port number, so one rule means the same thing on two different models. First matching rule wins, ACL-style, so a catch-all written first masks everything after it. One socket takes exactly one rule's configuration; there is no per-field merging. Selectors are evaluated against the whole faceplate, not against what earlier rules left over. Sockets no rule covers are left untouched. A per-rule report distinguishes "matched" from "won": a rule that matches 30 sockets and wins 0 is masked by an earlier rule, not wrong.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				var kind string
				kind, err = a.Required("kind")
				if err != nil {
					return "", err
				}
				return relay(ctx, c, "/api/sites/"+esc(siteID)+"/profiles/"+esc(kind))
			},
		},
		{
			Name:       "get_topology",
			Title:      "Declared links and observed LLDP neighbourhood",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props:      []P{{Name: "site_id", Type: "string", Desc: "Site to read.", Req: true}},
			Description: `Return a site's topology from both of its sources, kept apart and labelled:

  - declared_links — links somebody ENTERED. An intention. Writable.
  - observed_lldp  — the LLDP neighbourhood the collector OBSERVED. Read-only, and never written into the declared links.

The distinction is the design, not an implementation detail: an LLDP neighbour is a dated, refutable observation, while a declared link is a statement of intent. Three consequences when you read this:

  1. An LLDP neighbour is NOT proof of a direct cable. One port can legitimately see several neighbours, because an intermediate switch that does not consume LLDP floods the frames through.
  2. A neighbour seen from one side only is the ordinary case, not a fault. Seen from both sides requires exact reciprocity. A neighbour with no second endpoint in the inventory is listed but is not a link.
  3. Silence has three distinct causes and collapsing them sends you hunting a setting that does not exist: platform_unsupported (the connector says this platform does not speak LLDP at all — not repairable), not_served (it was polled and answered nothing), never_polled (no snapshot yet — repairable).`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				var out = map[string]any{"site_id": siteID}
				var links, linkErr = c.GetJSON(ctx, "/api/sites/"+esc(siteID)+"/links")
				if linkErr != nil {
					return "", linkErr
				}
				out["declared_links"] = links
				var lldp, lldpErr = c.GetJSON(ctx, "/api/sites/"+esc(siteID)+"/lldp")
				if lldpErr == nil {
					out["observed_lldp"] = lldp
				} else {
					out["could_not_read_lldp"] = lldpErr.Error()
				}
				return jsonOut(out)
			},
		},
		{
			Name:       "list_clients",
			Title:      "Machines learned behind a site's ports",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props:      []P{{Name: "site_id", Type: "string", Desc: "Site to read.", Req: true}},
			Description: `List a site's clients: every MAC address learned behind a port, located and metered.

A client is a machine seen behind a port, NOT an inventory device — do not confuse it with list_devices. Each entry is a MAC (canonical lowercase colon hex), a VLAN (0 when the platform does not report one) and the local port in standard faceplate naming. Only dynamic forwarding entries count: a static or CPU entry is the switch itself, not a machine behind a port.

This is an append-only ledger, so a machine stays on record after its switch stops reporting it. Presence here is therefore not proof that something is connected right now.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				return relay(ctx, c, "/api/sites/"+esc(siteID)+"/clients")
			},
		},
		{
			Name:       "list_events",
			Title:      "The event journal",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "site_id", Type: "string", Desc: "Site journal to read. Omit for the INSTANCE journal (proxies, discovery, audit), which requires global admin."},
				{Name: "limit", Type: "integer", Desc: "How many entries. Default 50."},
			},
			Description: `Read the event journal — what happened, when, and who caused it.

Two journals exist and they do not overlap. With a site_id you get that site's journal. Without one you get the INSTANCE journal, which carries what has no site: proxy lifecycle, discovery, audit. The instance journal is global-admin only.

An event is not a metric: quantities live in telemetry, and the journal records occurrences. Note that the observed LLDP neighbourhood emits no events at all — only declared links do.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.String("site_id")
				if err != nil {
					return "", err
				}
				var limit = strconv.Itoa(a.Int("limit", 50))
				if siteID == "" {
					return relay(ctx, c, "/api/events?limit="+limit)
				}
				return relay(ctx, c, "/api/sites/"+esc(siteID)+"/events?limit="+limit)
			},
		},
		{
			Name:       "list_proxies",
			Title:      "Proxies and their live tunnels",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Description: `List the proxies this identity can see, together with the tunnels currently established.

A proxy is a single binary the customer installs inside a remote network. It is the transport path and the local collector: it relays TCP streams to the controller, runs SNMP polling and scans, and receives syslog and traps. It connects outbound over WSS/443 only, holds no device credentials and no configurations, and — this matters when planning — THERE IS NO WRITE PATH THROUGH A PROXY. Every write to a device goes through a connector.

Two kinds, and the difference is who may use and administer it:
  - an INSTANCE proxy (global) serves all sites and is administered only by global admins;
  - a RESTRICTED proxy serves only the sites in its coverage.

Coverage is the explicit set of sites a proxy may serve, and an EMPTY COVERAGE MEANS NOWHERE, not everywhere. Coverage is an operational permission, not a claim about what the proxy can physically reach.

A listing filtered to nothing is normal for an identity with no role on any covered site.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var out = map[string]any{}
				var proxies, err = c.GetJSON(ctx, "/api/proxies")
				if err != nil {
					return "", err
				}
				out["proxies"] = proxies
				var tunnels, tunErr = c.GetJSON(ctx, "/api/proxies/tunnels")
				if tunErr == nil {
					out["live_tunnels"] = tunnels
				} else {
					out["could_not_read_tunnels"] = tunErr.Error()
				}
				return jsonOut(out)
			},
		},
		{
			Name:       "list_pipelines",
			Title:      "Operations in flight, and what is stuck",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "site_id", Type: "string", Desc: "Site whose pipelines to list. Required unless pipeline_id is given."},
				{Name: "pipeline_id", Type: "string", Desc: "One pipeline, with its full step transcript."},
			},
			Description: `List a site's pipelines, or fetch one with its step-by-step transcript.

A pipeline is the record of one operation on the fleet — a configuration apply, a credential gesture, a monitoring change — with its steps and, crucially, the TRANSCRIPT of what was sent to the device and what it answered. That transcript is the first thing to read when an apply did not do what was expected; it is also the audit trail. Note that it is a journal of ACTIONS, not of states: you can replay what an apply did, you can never reconstruct the configuration that preceded it.

The states are pending, running, blocked, done, failed. "blocked" IS NOT A FAILURE and the difference decides what to do next: it means the pipeline is waiting and says why in a sentence written for a human ("waiting: proxy X has been disconnected for 4 min"). Re-launching a blocked pipeline does exactly nothing. Only "failed" asks to be replayed. A step may also be "skipped", which is a decision — a guard-rail declined to act — and is neither success nor failure.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var pipelineID, err = a.String("pipeline_id")
				if err != nil {
					return "", err
				}
				if pipelineID != "" {
					return relay(ctx, c, "/api/pipelines/"+esc(pipelineID))
				}
				var siteID string
				siteID, err = a.Required("site_id")
				if err != nil {
					return "", errors.New("give either site_id (to list) or pipeline_id (to read one)")
				}
				return relay(ctx, c, "/api/sites/"+esc(siteID)+"/pipelines")
			},
		},
		{
			Name:       "list_credentials",
			Title:      "A site's credentials, and which devices lag",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "site_id", Type: "string", Desc: "Site to read.", Req: true},
				{Name: "credential_id", Type: "string", Desc: "Also report, for this credential, which generation each device is known to carry."},
			},
			Description: `List a site's credentials. NO SECRET IS RETURNED by this tool, ever — revealing one is a separate, journaled gesture that this server does not expose.

What a credential is here: a secret belonging to a site, of kind admin, snmp_v2c or snmp_v3, optionally scoped to a tag. Do not confuse it with the instance-wide "known credentials" — those are factory default pairs tried blind on equipment meshcraft does not manage yet, and mixing the two would mean trying admin/admin against a production fleet.

A GENERATION is an append-only version of a secret. A secret is never overwritten, because between a change and the end of a deployment the fleet carries several at once — a switch powered off for three months comes back with the old one. A retired generation therefore stays usable for login. What each device is actually known to carry is observed, not assumed, and that is what credential_id reports.

Rotation and deployment are different gestures and this is the distinction to hold on to: rotating creates a new generation, changing the EXPECTED secret; it changes nothing on any device. Writing it onto the hardware is deploy_credential.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var siteID, err = a.Required("site_id")
				if err != nil {
					return "", err
				}
				var out = map[string]any{"site_id": siteID}
				var creds, credErr = c.GetJSON(ctx, "/api/sites/"+esc(siteID)+"/credentials")
				if credErr != nil {
					return "", credErr
				}
				out["credentials"] = creds
				var credID string
				credID, err = a.String("credential_id")
				if err != nil {
					return "", err
				}
				if credID != "" {
					var dep, depErr = c.GetJSON(ctx,
						"/api/sites/"+esc(siteID)+"/credentials/"+esc(credID)+"/deployment")
					if depErr == nil {
						out["deployment"] = dep
					} else {
						out["could_not_read_deployment"] = depErr.Error()
					}
				}
				return jsonOut(out)
			},
		},
		{
			Name:       "list_discovery_candidates",
			Title:      "Equipment found but not yet adopted",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Description: `List discovery candidates: equipment a proxy has seen on the network that is not yet in the inventory.

A candidate BELONGS TO NOBODY AND HAS NO SITE. That is the whole point of the adoption step: choosing the site is the only moment a device acquires one, and it is never inferred from the proxy that made the discovery. The discovering proxy is a witness and gains no ownership and no rights.

Candidates are deduplicated by serial, then MAC, then (address domain, IP), and each lists the proxies that saw it with their latency. Never-adopted candidates are purged after 30 days.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				return relay(ctx, c, "/api/discovery/candidates")
			},
		},
		{
			Name:       "read_live_storm_control",
			Title:      "Read storm control from the device itself",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props:      []P{{Name: "device_id", Type: "string", Desc: "The device's id.", Req: true}},
			Description: `Read the per-port storm-control configuration FROM THE DEVICE, live.

This is a read, but not a cheap one: it opens a real session on the switch, so it needs operator rather than viewer and it costs from the device's session budget. Some platforms allow very few concurrent sessions. Do not poll it.

EVERY RATE COMES BACK WITH ITS UNIT, and that is not decoration. No two platforms agree on what the bare number means: some take a percentage of line rate, some a packet rate, and one vendor's CLI distinguishes percent, packets/s and bit/s by a single keyword that is easy to omit — the same digits, five orders of magnitude apart. A bare "10" would be a tenth of a gigabit on one switch and ten packets a second on the switch beside it in the same rack, and the second takes a working port off the air. No connector ever converts between units, because every conversion is dishonest: percent to packets/s needs a frame size, and percent to bit/s needs the rate the port last negotiated. So read the unit with the number, and never restate a rate in a unit the device did not give you.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var deviceID, err = a.Required("device_id")
				if err != nil {
					return "", err
				}
				return relay(ctx, c, "/api/devices/"+esc(deviceID)+"/storm-control")
			},
		},
		{
			Name:       "api_get",
			Title:      "Any read route this server has no dedicated tool for",
			Effect:     EffectRead,
			Idempotent: true,
			Min:        ModeRead,
			Props: []P{
				{Name: "path", Type: "string", Desc: `An /api/... path, query string included. Example: "/api/sites/<id>/mgmt-plan".`, Req: true},
			},
			Description: `Perform an authenticated GET on any read route of the controller API, and return the body verbatim.

This is the escape hatch for the long tail. The controller exposes a few hundred handlers; the tools above cover the ones with vocabulary worth teaching, and this covers the rest without putting a few hundred schemas in your context. Reach for it when the tool you want does not exist.

GET only. Any other method is refused here, so this door can never perform a gesture that changes or destroys anything — those exist only as named tools.

Useful paths not covered by a dedicated tool:
  /api/sites/<id>/mgmt-plan        — the management plan: management VLAN and whether tagged or native, management subnet, gateway, DNS/NTP, address source. A site with no plan returns a cautious default and says so, rather than showing values that look like a decision.
  /api/sites/<id>/plan             — unrelated to the above despite the name: the FLOOR PLAN, the geometry the map draws nodes on.
  /api/sites/<id>/tags             — the tags in use on the site.
  /api/sites/<id>/tag-rules        — rules that assign tags automatically from vendor, model, location or IP prefix.
  /api/sites/<id>/traffic          — per-site traffic.
  /api/sites/<id>/optics/rx-alerts — cages whose receive level has fallen below their own history.
  /api/sites/<id>/stp-preference   — the site's spanning-tree preference.
  /api/sites/<id>/stp-root-bridges — root bridge designations.
  /api/sites/<id>/dashboards       — the site's tile arrangements.
  /api/sites/<id>/collector-proxies and /discovery-proxies — which proxies may collect, and which may discover, for this site.
  /api/devices/<id>/snmp-plan      — what SNMP provisioning would write.
  /api/devices/<id>/console        — what interactive access this device offers (the shell itself is not exposed here).
  /api/discovery/prefixes          — the CIDR ranges declared for sweeping, each on the proxy that can reach it.
  /api/discovery/domains           — address domains.
  /api/discovery/drivers           — the connectors this controller knows.
  /api/catalog/models              — the model catalogue.
  /api/version, /api/healthz       — what is running, and whether the database answers.`,
			Run: func(ctx context.Context, c *Client, a Args) (string, error) {
				var path, err = a.Required("path")
				if err != nil {
					return "", err
				}
				err = checkReadPath(path)
				if err != nil {
					return "", err
				}
				return relay(ctx, c, path)
			},
		},
	}
}

// checkReadPath keeps api_get to the API, and to reads.
//
// The refusals are not about trusting the model: they are about the door only
// ever being the door it is documented as. A relative path would resolve against
// the base URL in ways the caller did not write, and an absolute URL would send
// this process's bearer token to a host nobody chose — which is the one thing a
// token-carrying client must never be talked into.
func checkReadPath(path string) error {
	var trimmed = strings.TrimSpace(path)
	if strings.Contains(trimmed, "://") || strings.HasPrefix(trimmed, "//") {
		return errors.New("path must be a path on this controller, not a URL: " +
			"this server will not send its token to another host")
	}
	if !strings.HasPrefix(trimmed, "/api/") {
		return errors.New(`path must start with "/api/" (got ` + trimmed + ")")
	}
	if strings.Contains(trimmed, "..") {
		return errors.New("path must not contain '..'")
	}
	// The two read routes that are not reads in the sense that matters. A console
	// or serial attachment is interactive access to a switch with none of the
	// safeguards a connector apply carries, and a SOCKS token is a credential
	// this server has no business minting.
	var blocked = []string{"/console", "/serial-servers", "/socks-token", "/debug/stream"}
	var b string
	for _, b = range blocked {
		if strings.Contains(trimmed, b) {
			return errors.New("this server does not expose " + b +
				": it opens interactive access to a device or mints a credential, " +
				"which is out of scope for an automation client. Use the web interface.")
		}
	}
	return nil
}

// findDeviceSite locates a device when the caller knows only its id.
//
// It costs one call per visible site, and the sites searched are REPORTED, so a
// caller who sees the cost can pass site_id next time rather than wonder why the
// answer was slow.
func findDeviceSite(ctx context.Context, c *Client, deviceID string) (string, []string, error) {
	var sites, err = c.GetJSON(ctx, "/api/sites")
	if err != nil {
		return "", nil, err
	}
	var list, ok = sites.([]any)
	if !ok {
		return "", nil, errors.New("could not read the site list while searching for the device")
	}
	var searched []string
	var entry any
	for _, entry = range list {
		var site, isMap = entry.(map[string]any)
		if !isMap {
			continue
		}
		var id, _ = site["id"].(string)
		if id == "" {
			continue
		}
		searched = append(searched, id)
		var _, findErr = deviceFromSite(ctx, c, id, deviceID)
		if findErr == nil {
			return id, searched, nil
		}
		// A site this identity cannot read is not an answer about the device.
		var apiErr *APIError
		if errors.As(findErr, &apiErr) && apiErr.Status != http.StatusNotFound &&
			apiErr.Status != http.StatusForbidden {
			return "", searched, findErr
		}
	}
	return "", searched, errors.New("no device with id " + deviceID +
		" in any site this identity can read (searched " + strconv.Itoa(len(searched)) +
		"). Check the id with list_devices, or pass site_id explicitly.")
}

// deviceFromSite projects one device out of a site's list.
func deviceFromSite(ctx context.Context, c *Client, siteID, deviceID string) (any, error) {
	var devices, err = c.GetJSON(ctx, "/api/sites/"+esc(siteID)+"/devices")
	if err != nil {
		return nil, err
	}
	var list, ok = devices.([]any)
	if !ok {
		return nil, errors.New("the device list for site " + siteID + " was not an array")
	}
	var entry any
	for _, entry = range list {
		var device, isMap = entry.(map[string]any)
		if !isMap {
			continue
		}
		var id, _ = device["id"].(string)
		if id == deviceID {
			return device, nil
		}
	}
	return nil, &APIError{
		Status: http.StatusNotFound, Method: http.MethodGet,
		Path:    "/api/sites/" + siteID + "/devices",
		Because: "no device " + deviceID + " in site " + siteID,
	}
}

// Package api exposes the controller REST API and serves the SPA.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"meshcraft/internal/agenthost"
	"meshcraft/internal/auth"
	"meshcraft/internal/config"
	"meshcraft/internal/debuglog"
	"meshcraft/internal/distrib"
	"meshcraft/internal/driverhost"
	"meshcraft/internal/relocate"
	"meshcraft/internal/renumber"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/socks"
	"meshcraft/internal/store"
	"meshcraft/internal/tsdb"
	"meshcraft/internal/tunnel"
	"meshcraft/wire/agentv1"
)

type Server struct {
	Cfg config.Config
	// Version (product) and Build (UTC build timestamp) are injected at link time
	// and shown in the admin area: knowing which image is running is the first
	// question in any diagnosis.
	Version string
	Build   string
	Store   *store.Store
	Auth    *auth.Service
	Distrib *distrib.Service
	// Secrets encrypts standard credentials used by discovery.
	Secrets *secretbox.Service
	// Tunnels holds WSS links established by proxies: remote device connections go
	// through here.
	Tunnels *tunnel.Hub
	// Relocate tracks searches for devices that changed address.
	Relocate *relocate.Tracker
	// Renumber holds candidate IPs the proxy should ping during a management
	// address move, until serial verification promotes one to mgmt_ip.
	Renumber *renumber.Tracker
	// Apply groups and serializes automatic configuration apply (cf. autoapply.go).
	// Nil = automatic apply disabled, everything is manual.
	Apply *autoApplier
	// Debug: application log ring, streamed to global-admin. Nil = debug zone inactive.
	Debug *debuglog.Ring
	// WatchExcl remembers devices no proxy watches, and why. Mounted by Router(); all
	// methods tolerate nil, so a hand-built Server in a test need not know about it.
	WatchExcl *watchExclusions
	// TSDB: time-series storage. Nil = no metrics, and the controller tells proxies
	// rather than accepting and dropping.
	TSDB *tsdb.Client

	// DriverMinter scopes external driver sessions' SOCKS credentials.
	// Nil when the drivers listener is off (cf. StartDriverSocks).
	DriverMinter *socks.Minter

	// DriverHost runs the git→build→run pipeline for external drivers.
	// Nil when the feature is off (no Docker socket configured).
	DriverHost *driverhost.Manager
	// AgentHost runs the git→OCI-layout pipeline for agent images the built-in
	// registry serves. Nil when no Docker socket is configured.
	AgentHost *agenthost.Manager
	// live: the per-site change bus, replacing periodic polling of fleet screens
	// (cf. sitebus.go and live_handlers.go). Mounted by Router(); all methods tolerate
	// nil, so a hand-built Server in a test notifies into the void instead of panicking.
	live *siteBus
	// portFlaps: hot correlation of port transitions reported by proxies — debouncing
	// and inter-proxy deduplication (cf. portevents.go). Mounted by Router(), like
	// `live` and `WatchExcl`, for the same reason: it must exist before the first proxy
	// connects. Its methods tolerate nil, so a hand-built Server in a test correlates
	// into the void; a test that wants to EXERCISE correlation builds its own portFlaps,
	// without going through a Server or any package-level state.
	portFlaps *portFlaps
	// proxyScopes retains, per proxy, what the last monitoring push entrusted to it:
	// the memory that makes opposable "this proxy has nothing to say about this device"
	// (cf. proxyscope.go). Mounted by Router(), like the three previous fields, for the
	// same reason: it must exist before the first proxy connects.
	//
	// One difference, and it is deliberate: its methods do NOT tolerate nil by letting
	// traffic through. The other three correlate or notify into the void, which is
	// harmless; this one AUTHORIZES, and an access control whose memory is missing must
	// refuse. A hand-built Server in a test therefore refuses all proxy facts — the
	// right default — and a test that wants to exercise scope builds its own proxyScopes,
	// exactly like portFlaps.
	proxyScopes *proxyScopes
	// serialLog: latest Web Serial wizard transcripts (autodetect / soft-reset),
	// mirrored to slog as serial.session. Lazy-allocated; tests need not set it.
	serialLog *serialLogStore
	// serialReg / serialOnce: published Web Serial lines other operators can
	// attach to (cf. serialserver.go). IN MEMORY on purpose — an offer is only
	// meaningful while a browser holds the port open, so it must not survive the
	// process that holds the socket.
	serialReg  *serialRegistry
	serialOnce sync.Once
	// snmpHeal: per-device backoff of the SNMP provisioning retry loop
	// (RunSNMPHeal). Zero value is ready to use; tests need not set it.
	snmpHeal snmpHealState
	// hostnameHeal: per-device backoff of the system-name reconciliation
	// (RunHostnameHeal). Zero value is ready to use, like snmpHeal — and for the
	// same reason it is a field and not package state: two Servers in one test
	// binary must not share a cooldown.
	hostnameHeal hostnameHealState
	// configPoll: per-device backoff of the periodic config read-back
	// (RunConfigPoll). Zero value is ready to use, like the two above, and a
	// field rather than package state for the same reason: two Servers in one
	// test binary must not share a cooldown.
	configPoll configPollState
	// extSync: per-extension backoff AND the memory that keeps the background
	// site-extension import quiet (RunExtSync, ADR-0029 §11-§13). Zero value is
	// ready to use, and a field for the reason above plus one of its own: it
	// holds the "was this failing last time" flag, so two Servers sharing it
	// would swallow each other's transitions.
	extSync extSyncState
	// credDeploy holds the fleet-wide credential deployment in flight, and the last
	// run per credential (cf. creddeploy.go). Zero value is ready to use, like the
	// three above. A field and not package state for their reason AND a sharper one:
	// this registry is what refuses a SECOND concurrent run, so two Servers sharing
	// it would refuse each other's runs, and two Servers that did not share it would
	// each believe itself the only one on the wire.
	credDeploy credDeployRegistry
	// startedAt: when this process began serving, set by Router(). It bounds the
	// reachability startup hold (cf. reachability.go, holdReach) and nothing else.
	//
	// ZERO IS A SAFE VALUE and tests rely on it: an unset clock reads as an age far
	// past every window, so a hand-built Server holds nothing and behaves exactly as
	// it did before the hold existed. The failure mode of forgetting to set it is
	// therefore the old behaviour, never a frozen status.
	startedAt time.Time
	// reachHold: devices whose status write is currently deferred, so the retry that
	// will write it is armed once and not once per event. Zero value ready to use,
	// like snmpHeal and the others, and a field for the same reason: two Servers in
	// one test binary must not share it.
	reachHold reachHoldState
}

func (s *Server) Router() http.Handler {
	var r = chi.NewRouter()
	// The clock the reachability startup hold is bounded by. Taken HERE, with the
	// other things that must exist before the first proxy connects: a proxy that
	// reconnects while this is still zero would be inside a window that has not
	// started, which reads as "long past" and holds nothing — the safe direction.
	s.startedAt = time.Now()
	// The bus lives as long as the router: mounting it here avoids lazy creation,
	// therefore a race between the first subscriber and the first notification.
	s.live = newSiteBus(maxLiveSubs)
	// Same reason: watch-exclusion tracking must exist before the first proxy connects,
	// otherwise the first monitoring calculations would miss their transition.
	s.WatchExcl = newWatchExclusions()
	// And port-transition correlation, with the clock that closes settled flaps — a flap
	// end is observed by ABSENCE of events, so it can only come from a clock (cf.
	// sweepPortFlaps). It runs from startup: a wake every thirty seconds to lock a mutex
	// and walk an empty map is not measurable, whereas starting it on the first event
	// would have imposed package-level `sync.Once` on every transition path.
	s.portFlaps = newPortFlaps()
	go s.sweepPortFlaps()
	// And memory of what was entrusted to each proxy, mounted here for the same reasons —
	// plus one that belongs to it alone: it AUTHORIZES. Package-level state would have let
	// one controller's scope authorize facts reported to another controller's proxies in
	// the same process, which is a test suite; and the first `record` on one server would
	// have purged, against ITS live tunnels, entries for the other server's proxies.
	s.proxyScopes = newProxyScopes()
	// Revocation safety net, for the same clock reason: revoking a proxy cuts its session
	// immediately (handleRevokeProxy), but a revocation done in SQL, pronounced by another
	// instance, or occurring while this controller was restarting has nobody to enforce it.
	// Cf. revalidateTunnels in proxy_handlers.go for the cadence argument.
	go s.revalidateTunnels()
	r.Use(middleware.RealIP, middleware.Recoverer, middleware.Compress(5))

	r.Get("/api/healthz", s.handleHealthz)
	// Version is behind the session: it identifies the deployed image precisely,
	// information useless to an anonymous visitor.

	// Built-in docker registry (read-only, anonymous): docker pull fetches the proxy
	// image directly from the controller.
	s.Distrib.MountRegistry(r)

	// Proxy binary download: ANONYMOUS, like the OCI registry that serves
	// exactly the same bytes to `docker pull`. Putting it behind the user session
	// protected nothing — a proxy has no session — and broke auto-upgrade, which
	// downloads its own binary (observed in test: HTTP 401 on every attempt).
	r.Get("/downloads/proxy/{name}", s.handleDownloadArtifact)

	// Proxy lifecycle: authenticated by enrollment token then by its
	// (proxy_id, secret) pair — never by user session.
	r.Post("/api/proxy/enroll", s.handleProxyEnroll)
	r.Post("/api/proxy/heartbeat", s.handleProxyHeartbeat)
	r.Post("/api/proxy/migrated", s.handleProxyMigrated)
	// Metrics pushed by the proxy: same authentication as the heartbeat,
	// and relayed to storage.
	r.Post("/api/proxy/metrics", s.handleProxyMetrics)
	// LLDP neighborhood pushed by the proxy: same authentication, and the same
	// scope control as metrics (COLLECTION authority).
	//
	// A route of its own, not another field in the metrics batch: what travels here is a
	// TABLE of strings, where the batch carries only labeled floats. The full reasoning —
	// and why neither the event channel nor `ask` could do this — is at the top of
	// cmd/proxy/lldp.go.
	r.Post("/api/proxy/lldp", s.handleProxyLLDP)
	// The forwarding-table snapshot — same family, same reasoning, its own
	// route for the same cause (cf. cmd/proxy/fdb.go and internal/snmppoll/fdb.go).
	r.Post("/api/proxy/fdb", s.handleProxyFDB)

	// The SAME lifeline under its versioned name (agentv1.PathPrefix). The
	// legacy /api/proxy/* routes above stay: a fielded proxy speaks them, and
	// the contract that documents /api/v1/agent/* must not break the agents it
	// describes. New agents target the versioned form; the handlers are one and
	// the same, so the two names cannot diverge in behaviour.
	r.Post(agentv1.PathPrefix+"/enroll", s.handleProxyEnroll)
	r.Post(agentv1.PathPrefix+"/heartbeat", s.handleProxyHeartbeat)
	r.Post(agentv1.PathPrefix+"/migrated", s.handleProxyMigrated)
	r.Post(agentv1.PathPrefix+"/metrics", s.handleProxyMetrics)
	r.Post(agentv1.PathPrefix+"/lldp", s.handleProxyLLDP)
	r.Post(agentv1.PathPrefix+"/fdb", s.handleProxyFDB)

	// The contracts, served as the documents they are: a driver or agent author
	// fetches the spec this controller actually implements rather than guessing
	// from a wiki. Anonymous and read-only — a contract is public by nature.
	// Registered outside the session group, beside the other agent routes.
	r.Get("/api/v1/contracts/{name}", s.handleContract)

	// Multiplexed tunnel (phase 0bis): outbound WebSocket, authenticated by the
	// same (proxy_id, secret) pair before upgrade.
	r.Get(tunnel.Path, s.handleProxyTunnel)

	r.Post("/api/auth/login", s.handleLogin)

	// PUBLIC DASHBOARD: the one route in this API that answers without a session,
	// on purpose. It is registered HERE — above the session group — because a
	// share link is opened by someone who has no account on this instance; put
	// inside the group below it would answer 401 to every visitor, and the whole
	// feature would be a link that never works.
	//
	// Being outside the middleware is exactly what makes it dangerous, so its
	// scope is closed in the handler rather than by the router: the token names
	// ONE dashboard, everything served is keyed on the site read from that row,
	// and each field is projected by hand (cf. handlePublicDashboard, which lists
	// what this route can and cannot reach). Nothing else public is added beside
	// it: one door is auditable, two are a pattern.
	r.Get("/api/public/dashboards/{token}", s.handlePublicDashboard)

	// Session routes.
	r.Group(func(r chi.Router) {
		r.Use(s.Auth.Middleware)
		// Every successful write wakes screens open on the same site.
		// Here and not in handlers: meshcraft is multi-user, and a manual reminder
		// forgotten once would produce a UI that lies (cf. live_handlers.go).
		r.Use(s.notifyOnWrite)

		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)
		// The caller's OWN SOCKS route token (cf. socks_handlers.go). Not
		// site-scoped and not admin-gated: it is a credential in one person's
		// hands, and what it can reach is decided per CONNECT against that
		// person's site roles, not by who was allowed to mint it.
		r.Get("/api/auth/socks-token", s.handleGetSocksToken)
		// The caller's OWN API tokens (cf. apitoken_handlers.go, which carries
		// the full shape of these routes, and ADR-0011). Same reasoning as the
		// SOCKS token above: not site-scoped and not admin-gated, because what
		// a token can reach is decided per call against its owner's roles, not
		// by who was allowed to mint it.
		r.Get("/api/auth/tokens", s.handleListAPITokens)
		// MINTING OR REVOKING A STANDING CREDENTIAL REQUIRES A SIGNED-IN
		// SESSION, never an API token. A token able to mint another one — or to
		// replace the SOCKS token — outlives its own expiry and its own
		// revocation, which undoes two of the five rules of ADR-0011. The four
		// routes that do that are grouped here so the rule is stated once
		// instead of being re-argued in each handler.
		r.Group(func(r chi.Router) {
			r.Use(auth.CookieOnly)
			r.Post("/api/auth/tokens", s.handleCreateAPIToken)
			r.Delete("/api/auth/tokens/{tokenID}", s.handleRevokeAPIToken)
			r.Post("/api/auth/socks-token", s.handleCreateSocksToken)
			r.Delete("/api/auth/socks-token", s.handleDeleteSocksToken)
		})
		r.Get("/api/version", s.handleVersion)
		// APPLICATION log, not to be confused with the fleet log.
		// Reserved to global-admin, verified in the handler.
		r.Get("/api/debug/log", s.handleDebugLog)
		r.Get("/api/debug/stream", s.handleDebugStream)

		r.Get("/api/sites", s.handleListSites)
		r.Get("/api/catalog/models", s.handleCatalogModels)
		r.Route("/api/sites/{siteID}", func(r chi.Router) {
			r.Patch("/", s.handleRenameSite)
			r.Delete("/", s.handleDeleteSite)

			r.Get("/vlans", s.handleListVlans)
			r.Post("/vlans", s.handleCreateVlan)
			r.Get("/profiles/{kind}", s.handleListProfiles)
			r.Post("/profiles/{kind}", s.handleCreateProfile)
			r.Get("/groups", s.handleListGroups)
			r.Post("/groups", s.handleCreateGroup)

			// Tags and automatic site tagging (see docs/en/01-Architecture/Tags and
			// credential scope.md). A tag can decide which credential a device
			// receives: writes are at site_admin, like credentials themselves.
			r.Get("/tags", s.handleListSiteTags)
			r.Get("/tag-rules", s.handleListTagRules)
			r.Put("/tag-rules", s.handleUpsertTagRule)
			r.Delete("/tag-rules/{ruleID}", s.handleDeleteTagRule)
			// Replay rules. Without `apply`, this is a PREVIEW.
			r.Post("/tag-rules/apply", s.handleApplyTagRules)

			r.Get("/devices", s.handleListDevices)
			r.Post("/devices", s.handleCreateDevice)
			r.Get("/links", s.handleListLinks)
			r.Post("/links", s.handleCreateLink)
			// OBSERVED LLDP neighborhood, read-only and not writable — that is the whole
			// difference from `/links`, which carries ENTERED links. An observation is not
			// created by hand: it is pushed by the collector (`POST /api/proxy/lldp`) or
			// it does not exist. See docs/en/01-Architecture/LLDP neighbours.md.
			r.Get("/lldp", s.handleListSiteLLDP)
			// The CLIENTS of the site: every learned MAC, located, identified
			// and metered. Read-only for the same reason as /lldp — a client
			// is an observation, pushed by the collector or nonexistent.
			r.Get("/clients", s.handleListSiteClients)
			// …with ONE write, and it only ever forgets: the ledger is
			// append-only by design, so the broom (`?days=N`) is the only
			// thing that can ever shorten the list. `operator`, traced.
			r.Delete("/clients", s.handlePruneSiteClients)
			r.Get("/plan", s.handleGetSitePlan)
			r.Put("/plan", s.handleUpdateSitePlan)
			// Site management addressing plan: read for all roles, write at site_admin
			// (cf. Management addressing).
			// Site credentials: the list reveals nothing; revelation is a separate,
			// traced action (site_admin).
			r.Get("/credentials", s.handleListSiteCredentials)
			r.Put("/credentials", s.handleUpsertSiteCredential)
			r.Post("/credentials/{credID}/reveal", s.handleRevealSiteCredential)
			r.Post("/credentials/{credID}/rotate", s.handleRotateCredential)
			r.Put("/credentials/{credID}/mode", s.handleSetCredentialMode)
			r.Post("/credentials/{credID}/generations/{gen}/reveal", s.handleRevealGeneration)
			// WHICH generation each device carries, and whether the current one may be
			// promoted. Read-only and secret-free, hence `operator` like the list: the
			// count on the list screen says how many devices lag, this says which ones.
			r.Get("/credentials/{credID}/deployment", s.handleCredentialDeployment)
			// THE PUSH ITSELF (cf. creddeploy.go). `site_admin` to start and to
			// stop — it writes an administration password onto every switch in a
			// scope — and `operator` to watch, like the view above: the report
			// carries account names, generations and transcripts, never a secret.
			r.Post("/credentials/{credID}/deploy", s.handleStartCredentialDeploy)
			r.Get("/credentials/{credID}/deploy", s.handleCredentialDeployStatus)
			r.Delete("/credentials/{credID}/deploy", s.handleStopCredentialDeploy)
			r.Delete("/credentials/{credID}", s.handleDeleteSiteCredential)

			// SITE EXTENSIONS: what a site is backed by, and the read-only
			// inventory it mirrors (ADR-0015, ADR-0016). The list is
			// secret-free (`has_token`, never the token); writes and anything
			// that USES the token against the foreign controller are
			// site_admin, revelation is traced like any vault access.
			r.Get("/extensions", s.handleListSiteExtensions)
			r.Put("/extensions", s.handleUpsertSiteExtension)
			r.Delete("/extensions/{extID}", s.handleDeleteSiteExtension)
			r.Post("/extensions/{extID}/reveal", s.handleRevealSiteExtensionToken)
			// The foreign controller's own site list, so pairing is a choice
			// and not a pasted identifier.
			r.Get("/extensions/{extID}/remote-sites", s.handleListExtRemoteSites)
			r.Post("/extensions/{extID}/pair", s.handleExtPair)
			// One import pass. Writes only into meshcraft's mirror.
			r.Post("/extensions/{extID}/sync", s.handleExtSync)
			// ALERTING (ADR-0024). Same secret discipline as extensions above:
			// the list is secret-free (`has_token`, never the token), writes
			// and revelation are site_admin, revelation is traced. The test
			// route is what keeps an operator from discovering a bad chat id
			// during the incident instead of before it.
			r.Get("/notify-channels", s.handleListNotifyChannels)
			r.Put("/notify-channels", s.handleUpsertNotifyChannel)
			r.Delete("/notify-channels/{chanID}", s.handleDeleteNotifyChannel)
			r.Post("/notify-channels/{chanID}/reveal", s.handleRevealNotifyToken)
			r.Post("/notify-channels/test", s.handleTestNotifyChannel)

			// THE THREE GESTURES (ADR-0025). All site_admin: they use the
			// token against the foreign controller. Every one of them goes
			// to the UniFi API — never to the box — and none of them
			// carries a setting: meshcraft holds no intention about these
			// boxes, so "reconfigure" is "reprovision", which asks the
			// controller to reassert its own configuration.
			//
			// What the token is actually allowed to do, read rather than
			// assumed, so a read-only key is known before it is needed.
			r.Get("/extensions/{extID}/rights", s.handleExtRights)
			// THE FOURTH GESTURE (ADR-0029): editing one SSID. `site_admin`,
			// like the other three, because it uses the token against the
			// foreign controller.
			//
			// The GET is NOT part of the mirror read above: it goes to the
			// controller, live, because the PUT's precondition has to compare
			// against the authority and not against our copy. Both hang off the
			// MIRROR's id, so a caller can only reach a service set this site
			// has actually imported.
			//
			// There is no POST and no DELETE here on purpose: creating and
			// removing a service set is not verified on this API, and a removal
			// has no inert form — the lesson ADR-0025 paid for once.
			r.Get("/ext-ssids/{extSSIDID}", s.handleGetExtSSID)
			r.Put("/ext-ssids/{extSSIDID}", s.handlePutExtSSID)
			// Pending boxes are controller-wide, not this site's: they
			// belong nowhere until adoption attaches them.
			r.Get("/extensions/{extID}/adoptable", s.handleExtAdoptable)
			r.Post("/extensions/{extID}/adopt", s.handleExtAdopt)
			// Forget is IRREVERSIBLE on UniFi's side and gated by the box's
			// exact current name, re-read from the controller inside
			// unifi.Do (ADR-0012's idiom, ADR-0025 §5). A POST and not a
			// DELETE, so it is not mistaken for deleting meshcraft's own
			// extension row just above.
			r.Post("/ext-devices/{extDevID}/forget", s.handleExtForget)
			r.Post("/ext-devices/{extDevID}/reprovision", s.handleExtReprovision)
			// The mirror itself, read-only and deliberately NOT under
			// /devices: that list holds what meshcraft configures.
			r.Get("/ext-devices", s.handleListSiteExtDevices)
			// The Wi-Fi screen's single read: access points + the SSIDs of
			// the site. An access point is not an equipment (ADR-0016).
			r.Get("/wifi", s.handleListSiteWiFi)

			r.Get("/mgmt-plan", s.handleGetMgmtPlan)
			r.Put("/mgmt-plan", s.handleUpdateMgmtPlan)

			r.Get("/stp-preference", s.handleGetSTPPreference)
			r.Put("/stp-preference", s.handlePutSTPPreference)
			// Root bridge designation. Read at `viewer`, written at `site_admin`:
			// a second root is an assertion about physical cabling, which is not
			// an operator-level change (cf. internal/api/stp_rootbridge.go).
			// READ-ONLY, and computed from the database rather than read off the
			// hardware. It exists because nothing displayed the MST region before
			// ADR-0021, and an identity the topology depends on that no screen
			// renders is an identity nobody verifies.
			r.Get("/stp-mst-region", s.handleGetSTPMSTRegion)
			r.Get("/stp-root-bridges", s.handleGetSTPRootBridges)
			r.Put("/stp-root-bridges", s.handlePutSTPRootBridges)
			// SHARED SERIAL LINES (cf. serialserver.go). All three are
			// `site_admin`, INCLUDING the list: a label names a switch somebody is
			// standing in front of with a console cable, which is operational
			// information about work in progress — and attaching to one is the
			// most privileged access a switch has.
			r.Get("/serial-servers", s.handleListSerialServers)
			r.Get("/serial-servers/host", s.handleHostSerialServer)
			r.Get("/serial-servers/{serverID}/attach", s.handleAttachSerialServer)

			r.Get("/export", s.handleExportSite)
			r.Post("/import", s.handleImportSite)

			// Pipelines: "what is stuck, and why?" must be answerable in one UI.
			r.Get("/pipelines", s.handleListPipelines)

			r.Get("/events", s.handleListEvents)
			r.Get("/traffic", s.handleSiteTraffic)
			// Optical fault list: cages whose receive level undercuts their own
			// history. Viewer, like traffic — reading a level changes nothing.
			r.Get("/optics/rx-alerts", s.handleOpticsRxAlerts)

			// Dashboards: the site's arrangements of tiles. The list SEEDS the two
			// defaults the first time a site is ever asked (once ever, cf. migration
			// 0041), which is why a read is allowed to write here.
			r.Get("/dashboards", s.handleListDashboards)
			r.Post("/dashboards", s.handleCreateDashboard)

			// Real-time site stream: "this site changed". Replaces periodic polling of
			// fleet screens (cf. live_handlers.go). Same minimal role as site reads —
			// knowing WHEN a site moves is already information about that site.
			r.Get("/stream", s.handleSiteStream)

			// Proxies usable as discovery source for this site: those that cover it,
			// plus instance proxies.
			r.Get("/discovery-proxies", s.handleListDiscoveryProxies)
			// Proxies able to COLLECT this site's devices: those that cover it. Serves
			// two purposes, and both matter — propose a device's collector, and say when
			// an existing attachment is no longer covering (cf. devices_handlers.go).
			r.Get("/collector-proxies", s.handleListCollectorProxies)
		})

		// Sub-resources addressed by their own id (site is verified after reading
		// the object).
		r.Patch("/api/vlans/{id}", s.handleUpdateVlan)
		r.Delete("/api/vlans/{id}", s.handleDeleteVlan)
		r.Patch("/api/profiles/{kind}/{id}", s.handleUpdateProfile)
		r.Delete("/api/profiles/{kind}/{id}", s.handleDeleteProfile)
		r.Patch("/api/groups/{id}", s.handleUpdateGroup)
		r.Delete("/api/groups/{id}", s.handleDeleteGroup)
		// NO GET ON THIS PATH, and adding one has a condition: the hostname
		// conformity verdict is published ONLY by the device LIST (withConformity,
		// conformity.go). A single-device GET that does not carry it makes a device
		// sheet loaded on its own report the axis as ABSENT rather than UNKNOWN —
		// see that function's comment for why that reads as a lie rather than as a
		// hole.
		r.Patch("/api/devices/{id}", s.handleUpdateDevice)
		r.Delete("/api/devices/{id}", s.handleDeleteDevice)
		r.Put("/api/devices/{id}/ports/{port}", s.handleSetDevicePort)
		r.Put("/api/devices/{id}/ports", s.handleSetDevicePorts)
		r.Post("/api/devices/{id}/move", s.handleMoveDevice)
		// Apply service identity on the device (cf. devicecreds_handlers).
		r.Post("/api/devices/{id}/credentials/apply", s.handleApplyDeviceCredential)
		// Read-only web probe, for tuning a connector.
		r.Post("/api/devices/{id}/web-probe", s.handleDeviceWebProbe)
		// INTERACTIVE CONSOLE (cf. console_handlers.go). The GET says what this
		// device offers and is what the UI asks before drawing a button; the
		// WebSocket relays a real SSH shell, with the secret staying on the
		// controller. Both are site_admin — a shell has none of the safeguards a
		// connector apply carries.
		r.Get("/api/devices/{id}/console", s.handleDeviceConsoleInfo)
		r.Get("/api/devices/{id}/console/ws", s.handleDeviceConsole)
		// Read-only SNMP walk, applied by the proxy that collects this device. Same
		// role and privilege level as the web probe — understand what the device actually
		// serves, without modifying anything — and for the same reason it is not a GET: the
		// question carries a body (OIDs, bounds). Cf. ask_handlers.go and
		// internal/tunnel/control.go for the question contract.
		r.Post("/api/devices/{id}/snmp-walk", s.handleDeviceSNMPWalk)
		// Configuration drift: re-read the device (read-only) and absorb what it
		// carries (cf. Reconciliation sections 2 and 6).
		r.Post("/api/devices/{id}/read-state", s.handleReadDeviceState)
		// What SNMP provisioning WOULD do, without triggering anything.
		r.Post("/api/devices/{id}/snmp-apply", s.handleApplyDeviceSNMP)
		// The RETURN path: write in the device the address to which it should send
		// traps. Same privilege level and response shape as snmp-apply — one makes the
		// device queryable, the other makes it talkative.
		//
		// Requires a DECLARED collector: without attachment, the device is collected by all
		// proxies covering its site, and "several proxies" does not translate to one address
		// (cf. trapdest.go and migration 0023). Refusal is returned as 422 with its repair
		// action.
		r.Post("/api/devices/{id}/trap-apply", s.handleApplyDeviceTrapDest)
		r.Get("/api/devices/{id}/snmp-plan", s.handleDeviceSNMPPlan)
		// This device's TELEMETRY: quantities no inventory field can carry —
		// temperature, load, memory, uptime, errors — and per-port rate history.
		//
		// `viewer` level: reading a measurement changes nothing and reveals no secret.
		// Scoping is BUILT server-side (cf. series.go): this route does not relay requests
		// written by the UI, otherwise the `device` tag — the only thing that places a serial
		// in a site — would become a leak between instance clients.
		r.Get("/api/devices/{id}/telemetry", s.handleDeviceTelemetry)
		r.Post("/api/devices/{id}/config/import", s.handleImportDeviceConfig)
		r.Post("/api/devices/{id}/config/apply", s.handleApplyDeviceConfig)
		// Switch profiles: evaluate a rule set on THIS device's front panel. Preview
		// is a POST because it carries rules not yet saved — it still writes nothing.
		r.Post("/api/devices/{id}/switch-profile/preview", s.handlePreviewSwitchProfile)
		r.Post("/api/devices/{id}/switch-profile/apply", s.handleApplySwitchProfile)
		// Device tags.
		r.Get("/api/devices/{id}/tags", s.handleListDeviceTags)
		r.Put("/api/devices/{id}/tags/{tag}", s.handleSetDeviceTag)
		r.Delete("/api/devices/{id}/tags/{tag}", s.handleDeleteDeviceTag)
		// Reconnaissance: a raw CONFIGURATION UNIT as the platform serves it. Read
		// only, and indispensable before writing a unit no documentation describes (cf.
		// handleReadRawSection).
		//
		// THE `wcd` SEGMENT IN THIS PATH IS A FALSE FRIEND, and it is kept deliberately:
		// this handler now goes through [driver.RawReader], so it returns a `wcd` section on
		// Instant On, a `*.b` page on SwOS, and a REST menu on RouterOS. Renaming would
		// break links and tuning scripts for cosmetic gain; the response now carries a
		// `kind` field that says what "a unit" means on the target device, and that is what
		// the UI must display rather than the word "wcd".
		r.Get("/api/devices/{id}/wcd/*", s.handleReadRawSection)
		// First half of management switchover: add the target VLAN interface,
		// without removing or saving anything. Cf. handlePrepareMgmtMigration.
		r.Post("/api/devices/{id}/mgmt-migrate/prepare", s.handlePrepareMgmtMigration)
		// Second half: switch administration once the address is obtained.
		r.Post("/api/devices/{id}/mgmt-migrate/commit", s.handleCommitMgmtMigration)
		// Third step: make permanent. Separate, because until it happens a power loss
		// cancels everything.
		r.Post("/api/devices/{id}/mgmt-migrate/persist", s.handlePersistMgmtMigration)
		// Declare which network this device's address lives in. Useful for manually
		// entered devices that inherit no domain and would lack an arbiter if another
		// record shared the same address.
		r.Put("/api/devices/{id}/address-domain", s.handleSetDeviceAddressDomain)

		// THIS device's management addressing mode — DHCP or static, with its three
		// fields (migration 0027). `site_admin`: the intent a future apply will refer to
		// when writing on a switch administration interface, not a label.
		r.Put("/api/devices/{id}/mgmt-addressing", s.handleSetDeviceAddressing)
		// Apply recorded same-VLAN addressing intent ([driver.MgmtAddrWriter]).
		r.Post("/api/devices/{id}/mgmt-addressing/apply", s.handleApplyMgmtAddressing)
		r.Post("/api/devices/{id}/stp/apply", s.handleApplyDeviceSTP)
		// Immediate per-port PoE write (explicit classes) — the wire half of the
		// port panel's on/off; the stored intent keeps riding PUT /ports.
		r.Post("/api/devices/{id}/poe/apply", s.handleApplyDevicePoE)
		// Immediate per-port DHCP-snooping TRUST write. The bool is trust, not
		// "snooping here": the connector also turns the device-wide feature on
		// and enables it on the site's VLANs, because a trust flag on a device
		// that inspects nothing is an exact configuration protecting nothing
		// (cf. internal/driver/dhcpsnoop.go).
		r.Post("/api/devices/{id}/dhcp-snooping/apply", s.handleApplyDeviceDHCPSnooping)
		// Per-port storm control: read what the device carries, then write it.
		// EVERY RATE CARRIES ITS UNIT on both endpoints — "10" is ten percent of
		// a gigabit on one platform and ten packets a second on the next, and no
		// connector converts between them (cf. internal/driver/stormcontrol.go).
		// The read is LIVE and opens a session, hence `operator` rather than
		// `viewer`; the write is site_admin like every other write that reaches
		// hardware.
		r.Get("/api/devices/{id}/storm-control", s.handleReadDeviceStormControl)
		r.Post("/api/devices/{id}/storm-control/apply", s.handleApplyDeviceStormControl)
		// Link aggregation: the COMPLETE desired list, applied then persisted
		// (cf. lag_handlers.go — replace semantics, so the UI always sends all).
		r.Post("/api/devices/{id}/lags", s.handleApplyDeviceLAGs)
		// Shut / un-shut ports NOW. An ACTION, not an intent: nothing lands in
		// devices.ports and nothing reconciles it (cf. internal/driver/portadmin.go),
		// and disabling a port carrying administration is refused unless forced.
		r.Post("/api/devices/{id}/ports/admin", s.handleApplyPortAdmin)
		// PoE power cycle: the far-end device REBOOTS. Distinct from the link
		// bounce above on purpose — one button for both would eventually reboot a
		// building of access points to fix a link light. BLOCKS for the off-delay.
		r.Post("/api/devices/{id}/ports/power-cycle", s.handlePortPowerCycle)
		// NO hostname route, and its absence is the design. The device's own system
		// name is RECONCILED — meshcraft owns it, pushes it when the operator renames
		// the device (PATCH above), and repairs drift on a cadence (hostnameheal.go).
		// POST /api/devices/{id}/hostname/apply existed until 2026-08-06 and was
		// deleted with the doctrine change: a hostname typed by hand would be
		// overwritten by the next pass, and a button whose effect expires within half
		// an hour is worse than no button. The verdict travels on the device payload
		// as `hostname` (cf. HostnameConformity), so the screen still explains itself.
		// Single-address leap ([driver.MgmtJumper]) — SwOS-shaped; requires confirmed=true.
		r.Post("/api/devices/{id}/mgmt-addressing/jump", s.handleJumpMgmtAddressing)
		// Lab / bring-up: temporary static IPv4 interface to learn Instant On owner value.
		r.Post("/api/devices/{id}/mgmt-addressing/probe-static-owner", s.handleProbeStaticOwner)
		// Designate this device's COLLECTOR proxy: the one that polls it in SNMP,
		// and whose address will be written in as trap recipient. Empty `proxy_id` =
		// detach, i.e. return collection to coverage-based mode.
		//
		// Refuses a proxy that does not cover the site: attachment gives no coverage, and
		// writing it anyway would stop all collection silently (cf. migration 0022 and
		// devices_handlers.go).
		r.Put("/api/devices/{id}/collector", s.handleSetDeviceCollector)
		// Find a device that changed address: automatic scan AND manual entry, in
		// parallel (cf. internal/relocate).
		r.Post("/api/devices/{id}/relocate", s.handleStartRelocate)
		r.Get("/api/devices/{id}/relocate", s.handleGetRelocate)
		r.Post("/api/devices/{id}/relocate/address", s.handleSubmitRelocate)
		r.Delete("/api/devices/{id}/relocate", s.handleCancelRelocate)
		r.Get("/api/pipelines/{id}", s.handleGetPipeline)
		r.Delete("/api/links/{id}", s.handleDeleteLink)

		// Dashboards addressed by their own id: the site is read from the row and
		// the rights are checked against it (cf. requireDashboard). Renaming and
		// rearranging are `operator`; the SHARE LINK is `site_admin`, because
		// publishing a site's state without a login is not the same decision as
		// moving a tile.
		r.Patch("/api/dashboards/{id}", s.handleUpdateDashboard)
		r.Put("/api/dashboards/{id}/tiles", s.handleSetDashboardTiles)
		r.Delete("/api/dashboards/{id}", s.handleDeleteDashboard)
		r.Post("/api/dashboards/{id}/share", s.handleShareDashboard)
		r.Delete("/api/dashboards/{id}/share", s.handleUnshareDashboard)

		// --- Proxies: COVERAGE is the rights boundary ---
		//
		// See docs/en/01-Architecture/Proxy.md and docs/en/03-Security/RBAC.md. Rights depend on the
		// target proxy's coverage, therefore they are verified in handlers and not by a
		// group middleware:
		//
		//   list            : filtered to instance proxies + those covering a site where
		//                     the caller has a role;
		//   deployment      : site_admin of at least one site (coverage limited to their
		//                     sites), global-admin for an instance proxy;
		//   ONE site's      : site_admin of THAT site;
		//   coverage
		//   the proxy itself: site_admin of ALL covered sites, or global-admin — renaming
		//                     included; a shared proxy is not renamed from a single site.
		r.Get("/api/proxies", s.handleListProxies)
		r.Get("/api/proxies/tunnels", s.handleListTunnels)
		r.Post("/api/proxies/enroll-tokens", s.handleCreateEnrollToken)
		r.Post("/api/proxies/compose", s.handleProxyCompose)
		r.Post("/api/proxies/install-script", s.handleProxyInstallScript)
		r.Put("/api/proxies/{id}/sites/{siteID}", s.handleSetProxySiteMember)
		r.Delete("/api/proxies/{id}/sites/{siteID}", s.handleSetProxySiteMember)
		r.Patch("/api/proxies/{id}", s.handleRenameProxy)
		r.Put("/api/proxies/{id}/upgrade", s.handleSetProxyUpgrade)
		r.Post("/api/proxies/{id}/migrate", s.handleMigrateProxy)
		r.Post("/api/proxies/{id}/cancel-migration", s.handleCancelProxyMigration)
		r.Post("/api/proxies/{id}/revoke", s.handleRevokeProxy)
		r.Delete("/api/proxies/{id}", s.handleDeleteProxy)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireGlobalAdmin)
			// Instance event log (proxies, discovery, audit): without a site, no site
			// log shows them.
			r.Get("/api/events", s.handleListInstanceEvents)

			// Pending tokens are not a public inventory: they say a deployment is in
			// progress.
			// External driver repositories: registering one means building and
			// running third-party code on the controller host — an instance
			// decision, never a site one (cf. driverrepo_handlers.go).
			r.Get("/api/driver-repos", s.handleListDriverRepos)
			r.Post("/api/driver-repos", s.handleAddDriverRepo)
			r.Post("/api/driver-repos/{repoID}/build", s.handleBuildDriverRepo)
			r.Post("/api/driver-repos/{repoID}/start", s.handleStartDriverRepo)
			r.Post("/api/driver-repos/{repoID}/stop", s.handleStopDriverRepo)
			r.Get("/api/driver-repos/{repoID}/log", s.handleDriverBuildLog)
			r.Delete("/api/driver-repos/{repoID}", s.handleDeleteDriverRepo)

			// Agent image repositories: the git→OCI-layout pipeline. No
			// start/stop — an agent is pulled, not run here — only build and
			// enable.
			r.Get("/api/agent-repos", s.handleListAgentRepos)
			r.Post("/api/agent-repos", s.handleAddAgentRepo)
			r.Post("/api/agent-repos/{repoID}/build", s.handleBuildAgentRepo)
			r.Put("/api/agent-repos/{repoID}/enabled", s.handleSetAgentRepoEnabled)
			r.Get("/api/agent-repos/{repoID}/log", s.handleAgentBuildLog)
			r.Delete("/api/agent-repos/{repoID}", s.handleDeleteAgentRepo)

			r.Get("/api/proxies/enroll-tokens", s.handleListEnrollTokens)
			r.Delete("/api/proxies/enroll-tokens/{tokenID}", s.handleDeleteEnrollToken)

			// Promote a restricted proxy to instance proxy, or the reverse.
			r.Put("/api/proxies/{id}/coverage", s.handleSetProxyCoverage)
		})

		// --- Discovery and adoption ---
		//
		// Nothing here is addressed by site: a candidate has no site. Adoption chooses
		// one and verifies rights on it (cf. docs/en/01-Architecture/Discovery.md).
		r.Route("/api/discovery", func(r chi.Router) {
			r.Get("/candidates", s.handleListDiscovered)
			r.Get("/candidates/{id}/config", s.handleGetDiscoveredConfig)
			r.Delete("/candidates/{id}", s.handleDeleteDiscovered)
			r.Post("/candidates/{id}/adopt", s.handleAdoptDiscovered)

			r.Get("/prefixes", s.handleListScanPrefixes)
			r.Post("/prefixes", s.handleCreateScanPrefix)
			r.Patch("/prefixes/{id}", s.handleUpdateScanPrefix)
			r.Delete("/prefixes/{id}", s.handleDeleteScanPrefix)
			r.Post("/prefixes/{id}/scan", s.handleRunScan)

			// Address domains: WHICH network a prefix describes, where the proxy only
			// says how to reach it. Read is open — you must be able to pick a domain when
			// declaring a prefix — but creating them merges discoveries from several proxies,
			// which involves sites a single site-admin does not administer.
			r.Get("/domains", s.handleListAddressDomains)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireGlobalAdmin)
				r.Post("/domains", s.handleCreateAddressDomain)
				r.Patch("/domains/{id}", s.handleUpdateAddressDomain)
				r.Delete("/domains/{id}", s.handleDeleteAddressDomain)
			})

			r.Post("/probe", s.handleProbeIP)
			// Materialize a known credential for Web Serial soft-reset (browser
			// must type the secret on the console; controller cannot open UART).
			r.Post("/serial-credential", s.handleSerialCredential)
			// Read a device page through a proxy: web-only connector tuning tool,
			// global-admin, GET only.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireGlobalAdmin)
				r.Post("/device-fetch", s.handleDeviceFetch)
				// Onboard a not-yet-adopted device (write).
				r.Post("/onboard", s.handleOnboardDevice)
				// Reachability preflight for a network from a proxy.
				r.Post("/reach-check", s.handleReachCheck)
			})
			r.Post("/serial", s.handleSerialCandidate)
			r.Post("/parse-config", s.handleParseConfig)
			// Web Serial wizard run log (autodetect / soft-reset): mirrored to slog
			// as serial.session and kept in memory for GET.
			r.Post("/serial-log", s.handlePostSerialLog)
			r.Get("/serial-log", s.handleGetSerialLog)

			r.Get("/drivers", s.handleListDrivers)
			r.Get("/reset-procedures", s.handleResetProcedures)

			// Instance standard credentials: global-admin only.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireGlobalAdmin)
				r.Get("/credentials", s.handleListKnownCredentials)
				// Reveal ONE instance credential, journaled like the site reveal.
				// These are the secrets the serial soft-reset actually places, so a
				// device provisioned but not yet adopted is reachable only with one
				// of them — and before this route the only way to see it again was
				// to re-run the wizard.
				r.Post("/credentials/{id}/reveal", s.handleRevealKnownCredential)
				r.Post("/credentials", s.handleCreateKnownCredential)
				r.Patch("/credentials/{id}", s.handleUpdateKnownCredential)
				r.Delete("/credentials/{id}", s.handleDeleteKnownCredential)
			})
		})

		// Binary inventory: UI data, behind the session.
		r.Get("/api/proxy/artifacts", s.handleListArtifacts)

		// Global administration.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireGlobalAdmin)
			r.Post("/api/sites", s.handleCreateSite)
			r.Get("/api/users", s.handleListUsers)
			r.Post("/api/users", s.handleCreateUser)
			r.Patch("/api/users/{id}", s.handleUpdateUser)
			r.Delete("/api/users/{id}", s.handleDeleteUser)
			r.Put("/api/users/{id}/roles", s.handleSetUserRoles)
		})
	})

	// Everything else: the embedded SPA.
	r.NotFound(s.serveSPA)
	return r
}

// --- Helpers ---

func respond(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if v != nil {
		var err = json.NewEncoder(w).Encode(v)
		if err != nil {
			slog.Error("encode response", "err", err)
		}
	}
}

func respondErr(w http.ResponseWriter, code int, msg string) {
	respond(w, code, map[string]string{"error": msg})
}

// fail translates a store error into an HTTP response.
func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		respondErr(w, http.StatusNotFound, "not found")
	case strings.Contains(err.Error(), "duplicate key"):
		respondErr(w, http.StatusConflict, "already exists (duplicate)")
	case strings.Contains(err.Error(), "invalid input syntax for type uuid"):
		respondErr(w, http.StatusBadRequest, "invalid identifier")
	default:
		slog.Error("internal error", "err", err)
		respondErr(w, http.StatusInternalServerError, "internal error")
	}
}

// maxBody caps an API request body at 1 MiB.
//
// That is the right order of magnitude for what this API receives: forms, credentials,
// VLAN lists. A route that receives BATCHES caps itself separately, and says so — see
// maxProxyMetricsBody in proxymetrics_handlers.go and site configuration import (8 MiB
// too). What must NOT happen is announcing an element-count cap that this cap cuts well
// before: the visible safeguard would lie, and truncation would answer in its place
// without saying why.
const maxBody = 1 << 20

// readJSON reads a JSON body capped at `limit` bytes, and returns the error
// WITHOUT responding.
//
// HTTP response translation is left to failJSON because callers interpret the same
// errors differently: an absent body is valid for decodeOptional and fatal for decode.
func readJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) error {
	var dec = json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	return dec.Decode(v)
}

// failJSON responds to an unreadable body, DISTINGUISHING its two causes.
//
// An oversized body and malformed JSON are not fixed the same way: the first asks
// the sender to send less at once, the second to fix what it sends. Conflating them
// into a single "invalid JSON" — what this function used to do, since a body cut by
// http.MaxBytesReader comes out of the decoder as broken syntax — sends diagnosis
// into a wall: on the proxy side, a silent 400 on a batch that would NEVER pass,
// therefore perpetual replay of the same batch and complete upload stop.
//
// Recognition uses *http.MaxBytesError, not error text: net/http's message is not a
// contract, the type is.
//
// `recovery` is the route-specific follow-up; it names the ACTION expected from the
// sender, which the cap alone cannot imply.
func failJSON(w http.ResponseWriter, err error, recovery string) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		// 413 not 400: body shape is not the issue, its SIZE is.
		respondErr(w, http.StatusRequestEntityTooLarge,
			"request body too large: cap of "+
				strconv.FormatInt(tooBig.Limit, 10)+" bytes"+recovery)
		return
	}
	respondErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	var err = readJSON(w, r, v, maxBody)
	if err != nil {
		failJSON(w, err, "; send less data at a time")
		return false
	}
	return true
}

// decodeOptional accepts an absent body: endpoints that only have options (a
// coverage to specify, for example) must work without a body.
func decodeOptional(w http.ResponseWriter, r *http.Request, v any) bool {
	var err = readJSON(w, r, v, maxBody)
	if errors.Is(err, io.EOF) {
		return true
	}
	if err != nil {
		failJSON(w, err, "; send less data at a time")
		return false
	}
	return true
}

// handleVersion: what is actually running. `version` is the product version,
// `build` the UTC build timestamp — it changes on every image and answers "is my fix
// deployed?".
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	respond(w, http.StatusOK, map[string]string{
		"version": s.Version,
		"build":   s.Build,
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	var err = s.Store.Ping(r.Context())
	if err != nil {
		respondErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

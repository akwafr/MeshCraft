package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/distrib"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
	"meshcraft/internal/tunnel"
	"meshcraft/wire/agentv1"
)

// --- Proxy side: enrollment, heartbeat, migration confirmation ---
//
// The proxy has no session: it authenticates with the (proxy_id, secret) pair
// received at enrollment. See docs/en/03-Security/Proxy security.md.

func (s *Server) handleProxyEnroll(w http.ResponseWriter, r *http.Request) {
	var in agentv1.EnrollRequest
	if !decode(w, r, &in) {
		return
	}
	// The token carries the coverage decided at issuance.
	var global, siteIDs, err = s.Store.UseEnrollToken(r.Context(), auth.HashToken(in.Token))
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "invalid, expired, or already used enrollment token")
		return
	}
	var secret, secretHash, secErr = auth.NewSessionToken()
	if secErr != nil {
		fail(w, secErr)
		return
	}
	var name = in.Name
	if name == "" {
		name = "proxy-" + time.Now().UTC().Format("20060102-150405")
	}
	var p, createErr = s.Store.CreateProxy(r.Context(), name, secretHash, global, siteIDs)
	if createErr != nil {
		fail(w, createErr)
		return
	}
	s.emit(r, model.Event{
		Type: "proxy.enrolled", Severity: model.SevNotice,
		Message: "Proxy \"" + p.Name + "\" enrolled — " + coverageLabel(p), Actor: "proxy",
	})
	respond(w, http.StatusOK, agentv1.EnrollResponse{
		Status:        "enrolled",
		ProxyID:       p.ID,
		Secret:        secret,
		ControllerURL: s.Distrib.PublicURL(r),
	})
}

// coverageLabel describes coverage plainly, for the log and messages.
func coverageLabel(p model.Proxy) string {
	if p.Global {
		return "instance proxy (all sites)"
	}
	if len(p.Sites) == 0 {
		return "no site covered"
	}
	var names = make([]string, 0, len(p.Sites))
	var ref model.SiteRef
	for _, ref = range p.Sites {
		names = append(names, ref.Name)
	}
	return "sites: " + strings.Join(names, ", ")
}

// maxUpgradesInFlight: how many proxies may be upgrading at once on the instance.
// One by default — we want to see a binary start somewhere before pushing it everywhere.
const maxUpgradesInFlight = 1

// upgradeOfferWindow: beyond this, an unanswered offer is considered not understood.
// Wide enough for downloading a multi-megabyte binary over a slow link — and a working
// proxy reports `downloading`, which clears this state.
const upgradeOfferWindow = 15 * time.Minute

// handleProxyHeartbeat: the proxy signals presence, reports its last upgrade attempt,
// and receives pending orders — migration to another instance, or binary upgrade.
func (s *Server) handleProxyHeartbeat(w http.ResponseWriter, r *http.Request) {
	var in agentv1.HeartbeatRequest
	if !decode(w, r, &in) {
		return
	}
	var migrateURL, migrateToken, err = s.Store.AuthProxy(r.Context(), in.ProxyID, auth.HashToken(in.Secret))
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "unknown or revoked proxy")
		return
	}
	_ = s.Store.TouchProxy(r.Context(), in.ProxyID, in.Version, in.Protocol, in.OS, in.Arch, in.Ifaces)

	var p model.Proxy
	p, err = s.Store.GetProxy(r.Context(), in.ProxyID)
	if err != nil {
		fail(w, err)
		return
	}
	// The report is applied BEFORE deciding a new offer: otherwise a failure would
	// immediately be overwritten by an identical offer, and the proxy would replay a
	// broken binary forever (observed in test).
	if s.recordUpgradeReport(r, p, in.Upgrade, in.Version) {
		var fresh, refreshErr = s.Store.GetProxy(r.Context(), in.ProxyID)
		if refreshErr == nil {
			p = fresh
		}
	}

	// Migration TAKES PRIORITY over upgrade: change instance, then upgrade from the new
	// one. Target version belongs to the instance hosting the proxy.
	if migrateURL != "" && migrateToken != "" {
		respond(w, http.StatusOK, agentv1.HeartbeatResponse{
			Status: "migrate",
			Migrate: &agentv1.MigrateOrder{
				ControllerURL: migrateURL,
				EnrollToken:   migrateToken,
			},
		})
		return
	}

	var up = s.upgradeOffer(r, p, in.Version, in.OS, in.Arch)
	if up != nil {
		var first, offerErr = s.Store.SetProxyUpgradeOffered(r.Context(), p.ID, up.Version)
		if offerErr != nil {
			slog.Error("upgrade: state not saved", "err", offerErr, "proxy_id", p.ID)
		}
		// Logging every heartbeat teaches nothing: only the first offer is an event.
		if first {
			s.emitInstance(r, "proxy.upgrade_offered", model.SevNotice,
				"Upgrade "+up.Version+" offered to proxy \""+p.Name+"\" (from "+
					versionOrUnknown(in.Version)+")")
		}
		respond(w, http.StatusOK, agentv1.HeartbeatResponse{
			Status:  "ok",
			Upgrade: &agentv1.UpgradeOffer{Version: up.Version, URL: up.URL, SHA256: up.SHA256, Size: up.Size},
		})
		return
	}
	respond(w, http.StatusOK, agentv1.HeartbeatResponse{Status: "ok"})
}

func versionOrUnknown(v string) string {
	if v == "" {
		return "unknown version"
	}
	return v
}

// recordUpgradeReport stores what the proxy says about its attempt, and recognizes
// success on the ONLY proof that counts: the version it announces now.
// recordUpgradeReport returns true if database state changed — the caller must reread
// the proxy before deciding anything.
func (s *Server) recordUpgradeReport(r *http.Request, p model.Proxy,
	report *agentv1.UpgradeReport, running string) bool {
	var changed = false
	if report != nil && report.State != "" {
		changed = true
		switch report.State {
		case "failed":
			_ = s.Store.SetProxyUpgradeReport(r.Context(), p.ID, "failed", report.Version, report.Error)
			// A failure suspends offers for this proxy until human action: replaying a
			// binary that will not start teaches nothing and makes the whole fleet blink.
			s.emitInstance(r, "proxy.upgrade_failed", model.SevWarning,
				"Upgrade "+report.Version+" failed on proxy \""+p.Name+"\": "+report.Error)
		case "downloading", "applied":
			_ = s.Store.SetProxyUpgradeReport(r.Context(), p.ID, report.State, report.Version, "")
		}
	}
	// The proxy runs the target version: done, regardless of what the controller thought
	// it triggered.
	if running != "" && p.UpgradeVersion == running && p.UpgradeState != "up_to_date" {
		_ = s.Store.MarkProxyUpgraded(r.Context(), p.ID, running)
		s.emitInstance(r, "proxy.upgraded", model.SevNotice,
			"Proxy \""+p.Name+"\" up to date at "+running)
		changed = true
	}
	return changed
}

// upgradeOffer decides whether to offer an upgrade to this proxy, and which one.
// All refusal gates are explicit: policy is read here.
func (s *Server) upgradeOffer(r *http.Request, p model.Proxy, running, osName, arch string) *distrib.Upgrade {
	// The internal proxy is the controller itself: it follows its image.
	if p.Internal || p.Revoked {
		return nil
	}
	// A proxy that announces no version is not comparable: wait for it to identify
	// rather than push a binary blindly.
	if running == "" {
		return nil
	}
	var target = s.Distrib.TargetVersion()
	if p.PinVersion != "" {
		// Pin (or rollback): operator decision wins, not chronology.
		target = p.PinVersion
	}
	if target == "" || target == running {
		return nil
	}
	// Without pin or explicit request, never downgrade: a target older than running
	// signals a lagging controller.
	if p.PinVersion == "" && !p.UpgradeRequested && !distrib.IsNewer(target, running) {
		return nil
	}
	if !p.AutoUpgrade && !p.UpgradeRequested && p.PinVersion == "" {
		return nil
	}
	// Failure already seen on THIS version: suspended until human action
	// ("upgrade now" button, pin, or auto toggle).
	if p.UpgradeState == "failed" && p.UpgradeVersion == target && !p.UpgradeRequested {
		return nil
	}
	// An offer left unanswered too long is not an ongoing deployment: it is a proxy that
	// cannot handle it (binary predating the feature). Stop insisting and show it.
	if p.UpgradeState == "offered" && p.UpgradeVersion == target && !p.UpgradeRequested &&
		p.UpgradeStartedAt != nil && time.Since(*p.UpgradeStartedAt) > upgradeOfferWindow {
		var reason = "offer left unanswered: this proxy is probably too old " +
			"to upgrade itself — redeploy it manually once"
		var err = s.Store.MarkProxyUpgradeStalled(r.Context(), p.ID, target, reason)
		if err == nil {
			s.emitInstance(r, "proxy.upgrade_stalled", model.SevWarning,
				"Upgrade "+target+" unanswered from proxy \""+p.Name+"\": "+reason)
		}
		return nil
	}
	// Progressive rollout. A proxy already in flight on this version continues — it is
	// a reminder of the offer, not a new candidate.
	var alreadyInFlight = p.UpgradeState == "offered" && p.UpgradeVersion == target
	if !alreadyInFlight && !p.UpgradeRequested {
		var inFlight, err = s.Store.CountUpgradesInFlight(r.Context(), p.ID, target)
		if err == nil && inFlight >= maxUpgradesInFlight {
			return nil
		}
	}
	return s.Distrib.UpgradeFor(r, target, osName, arch)
}

// handleProxyMigrated: the proxy confirms attaching to the new instance. Mark it
// migrated (and revoked here) so we stop counting it.
func (s *Server) handleProxyMigrated(w http.ResponseWriter, r *http.Request) {
	var in agentv1.MigratedRequest
	if !decode(w, r, &in) {
		return
	}
	var _, _, err = s.Store.AuthProxy(r.Context(), in.ProxyID, auth.HashToken(in.Secret))
	if err != nil {
		respondErr(w, http.StatusUnauthorized, "unknown or revoked proxy")
		return
	}
	if err = s.Store.MarkProxyMigrated(r.Context(), in.ProxyID); err != nil {
		fail(w, err)
		return
	}
	// MarkProxyMigrated revokes the proxy: its session no longer belongs here, and leaving
	// it open let a proxy that moved to a neighbor keep opening connections and receiving
	// THIS instance's monitoring.
	//
	// The migration sequence does not suffer, and that is verified rather than assumed (cf.
	// cmd/proxy/main.go, migrate): when the proxy posts here, it has ALREADY enrolled on
	// the target and persisted its new state; this call is only "you can forget me", it
	// goes over HTTP not the tunnel, and the proxy treats even failure of this call as
	// inconsequential. Cutting only closes a link the migrated proxy will no longer use.
	//
	// Database FIRST, session second — same order and reason as manual revocation, cf.
	// handleRevokeProxy.
	var cut string
	if s.Tunnels.CloseSession(in.ProxyID, migratedTunnelReason) {
		cut = " — this instance's tunnel cut"
	}
	s.emit(r, model.Event{
		Type: "proxy.migrated", Severity: model.SevNotice,
		Message: "Proxy migrated to " + in.NewController + cut, Actor: "proxy",
	})
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Administration side ---
//
// Coverage is the rights boundary (cf. docs/en/01-Architecture/Proxy.md):
//
//   - see a proxy         : have a role on a covered site (or be global-admin, or the
//                           proxy is instance-wide);
//   - add/remove YOUR site: be site_admin of that site;
//     from coverage
//   - rename, revoke,     : be global-admin, or site_admin of ALL covered sites —
//     delete, migrate         otherwise you would act on another site's proxy;
//   - promote to instance : global-admin only.
//     proxy

func (s *Server) handleListProxies(w http.ResponseWriter, r *http.Request) {
	// Read follows the weakest role: understanding which path reaches a device also
	// interests a viewer.
	var siteIDs, all, err = s.Auth.SiteIDsWithRole(r.Context(), "viewer")
	if err != nil {
		fail(w, err)
		return
	}
	var proxies, listErr = s.Store.ListProxies(r.Context(), all, siteIDs)
	if listErr != nil {
		fail(w, listErr)
		return
	}
	respond(w, http.StatusOK, proxies)
}

// canSeeProxy: a role on a covered site suffices; an instance proxy is visible to all.
func (s *Server) canSeeProxy(r *http.Request, p model.Proxy) bool {
	if auth.UserFrom(r.Context()).IsGlobalAdmin || p.Global {
		return true
	}
	var ref model.SiteRef
	for _, ref = range p.Sites {
		if s.Auth.CanSite(r.Context(), ref.ID, "viewer") {
			return true
		}
	}
	return false
}

// canAdminWholeProxy: the proxy only involves sites where the caller is site_admin.
// That is the condition to act on the proxy ITSELF (revocation, deletion, migration)
// and not only its membership.
func (s *Server) canAdminWholeProxy(r *http.Request, p model.Proxy) bool {
	if auth.UserFrom(r.Context()).IsGlobalAdmin {
		return true
	}
	// An instance proxy, or one with no sites, belongs to no site-admin.
	if p.Global || p.Internal || len(p.Sites) == 0 {
		return false
	}
	var ref model.SiteRef
	for _, ref = range p.Sites {
		if !s.Auth.CanSite(r.Context(), ref.ID, "site_admin") {
			return false
		}
	}
	return true
}

// proxyForAdmin loads the proxy and verifies the caller may act on it.
func (s *Server) proxyForAdmin(w http.ResponseWriter, r *http.Request) (model.Proxy, bool) {
	var p, err = s.Store.GetProxy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return p, false
	}
	if !s.canAdminWholeProxy(r, p) {
		if !s.canSeeProxy(r, p) {
			respondErr(w, http.StatusNotFound, "not found")
			return p, false
		}
		respondErr(w, http.StatusForbidden,
			"this proxy covers sites you do not administer: you can only "+
				"add or remove your own sites from its coverage")
		return p, false
	}
	return p, true
}

// handleSetProxySiteMember adds (PUT) or removes (DELETE) ONE site from coverage.
// The caller must be site_admin of THAT site — never more.
func (s *Server) handleSetProxySiteMember(w http.ResponseWriter, r *http.Request) {
	var p, err = s.Store.GetProxy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	var siteID = chi.URLParam(r, "siteID")
	if !s.Auth.CanSite(r.Context(), siteID, "site_admin") {
		respondErr(w, http.StatusForbidden, "site_admin rights required on this site")
		return
	}
	var add = r.Method == http.MethodPut
	if p.Global {
		respondErr(w, http.StatusConflict,
			"this proxy is an instance proxy: it already covers all sites. "+
				"Restricting it requires an instance administrator.")
		return
	}
	if p.Internal {
		respondErr(w, http.StatusConflict,
			"the \"Local\" proxy is the instance itself: its coverage cannot be changed")
		return
	}
	// Removing the last site would make the proxy invisible to all site-admins, therefore
	// unmanageable without a global-admin.
	if !add && len(p.Sites) <= 1 {
		respondErr(w, http.StatusConflict,
			"removing the last covered site would make this proxy invisible and unmanageable: "+
				"ask an instance administrator to delete or reassign it")
		return
	}
	if err = s.Store.SetProxySiteMember(r.Context(), p.ID, siteID, add); err != nil {
		fail(w, err)
		return
	}
	var updated, getErr = s.Store.GetProxy(r.Context(), p.ID)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	// Displayed coverage must match APPLIED coverage: cf. pushCoverageChange.
	s.pushCoverageChange(p.ID)
	var verb = "added to"
	if !add {
		verb = "removed from"
	}
	s.emitSite(r, siteID, "proxy.coverage_changed", model.SevNotice,
		"Proxy \""+p.Name+"\" "+verb+" this site's coverage — "+coverageLabel(updated), nil)
	respond(w, http.StatusOK, updated)
}

// pushCoverageChange repushes monitoring after a COVERAGE change.
//
// # The fixed defect
//
// Both coverage handlers wrote to the database and stopped. A shrink therefore took
// effect only on the proxy's next reconnect, or on a push triggered elsewhere (a changed
// scan prefix, a device attachment): meanwhile the proxy kept pinging and polling SNMP
// on devices in a site just removed from it. Displayed coverage was not applied coverage,
// and nothing said so.
//
// That is first an operations defect — a site removed from a proxy to stop touching it
// keeps being touched — and became a security defect once proxy scope relies on what was
// PUSHED (cf. proxyscope.go): while the old instruction remains applied, it remains
// AUTHORIZED, because scope memory is deliberately consistent with what the proxy holds.
// Fixing the push fixes both in one gesture: `PushWatchTo` replaces the proxy instruction
// and retained set in the same call, and retains only if the push actually succeeded.
//
// # WHO is repushed, and why only them
//
// The proxy whose coverage changed, and nobody else. Not economy — that is what the
// calculation allows asserting: a proxy's configuration is a function of its SOLE
// identifier. `DevicesWatchedBy` filters on
// `p.global or exists(proxy_sites where proxy_id = p.id)` — recipient coverage, never a
// neighbor's — and projects `Collector` as
// `d.proxy_id is null or d.proxy_id = p.id`, which does not depend on other proxies either.
// `ProxyDomains` and `ListScanPrefixes` are likewise per-proxy. Changing one proxy's
// `proxy_sites` cannot change another's `WatchConfig`, and repushing an identical
// instruction would only needlessly restart collection across a fleet on every click.
//
// Two neighborhoods you might think affected, and are not:
//
//   - an UNATTACHED device is collected by all proxies covering its site. Removing that
//     site from one changes nothing for others, who already collected it and continue;
//   - a device attached to this proxy whose site is removed is collected by nobody — but
//     neighbors did not collect it either, for whom `Collector` was already false. Their
//     instruction is unchanged. That case is what the device page banner signals (cf.
//     snmpCandidates), and it is fixed by correcting attachment, not by pushing.
//
// One accepted side effect remains: watch-exclusion tracking (`WatchExcl`) reasons at
// fleet scale. A device this proxy alone watched becomes "unwatched" from this push — the
// banner will say so, since it queries live state — but the `watch.excluded` EVENT is
// emitted only for a device in the rejection list, and this one is not: it simply vanished
// from candidates. The event arrives on the next calculation by a proxy that explicitly
// excludes it. Emitting from here would require repushing the whole neighborhood for one
// log line, which is the wrong side of the trade-off.
//
// # Own context, in the background
//
// Like all other callers (cf. discovery_handlers.go and handleSetDeviceCollector): the
// request context dies with the response, and the HTTP response must not wait for several
// database round trips plus a tunnel that may be down. After the database write, necessarily
// — `watchConfigFor` rereads coverage, and launching before pushing would send the OLD
// instruction, exactly the defect being fixed.
func (s *Server) pushCoverageChange(proxyID string) {
	if proxyID == "" {
		return
	}
	go s.PushWatchTo(context.Background(), proxyID)
}

// handleSetProxyCoverage redefines entire coverage: the operation that promotes a
// restricted proxy to instance proxy, or the reverse. global-admin.
func (s *Server) handleSetProxyCoverage(w http.ResponseWriter, r *http.Request) {
	var p, err = s.Store.GetProxy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, err)
		return
	}
	var in struct {
		Global  bool     `json:"global"`
		SiteIDs []string `json:"site_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if p.Internal && !in.Global {
		respondErr(w, http.StatusConflict,
			"the \"Local\" proxy is the instance itself: it remains an instance proxy")
		return
	}
	if err = s.Store.SetProxyCoverage(r.Context(), p.ID, in.Global, in.SiteIDs); err != nil {
		fail(w, err)
		return
	}
	var updated, getErr = s.Store.GetProxy(r.Context(), p.ID)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	// Same: promoting to instance proxy entrusts the whole fleet; restricting removes part.
	// Neither should wait for reconnect to become true (cf. pushCoverageChange).
	s.pushCoverageChange(p.ID)
	s.emitInstance(r, "proxy.coverage_changed", model.SevNotice,
		"Coverage of proxy \""+p.Name+"\": "+coverageLabel(updated))
	respond(w, http.StatusOK, updated)
}

// handleMigrateProxy stores the migration order: on its next heartbeat, the proxy will
// enroll on the new instance with the provided token, then switch permanently.
//
// The token must have been generated on the TARGET instance (only it can emit them):
// the operator copies it here.
func (s *Server) handleMigrateProxy(w http.ResponseWriter, r *http.Request) {
	var p, ok = s.proxyForAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		ControllerURL string `json:"controller_url"`
		EnrollToken   string `json:"enroll_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.ControllerURL = strings.TrimSuffix(strings.TrimSpace(in.ControllerURL), "/")
	if in.ControllerURL == "" || in.EnrollToken == "" {
		respondErr(w, http.StatusBadRequest, "controller_url and enroll_token required")
		return
	}
	var u, parseErr = url.Parse(in.ControllerURL)
	if parseErr != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		respondErr(w, http.StatusBadRequest, "invalid controller_url (expected https://host)")
		return
	}
	var err = s.Store.SetProxyMigration(r.Context(), p.ID, in.ControllerURL, in.EnrollToken)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitInstance(r, "proxy.migration_ordered", model.SevNotice,
		"Migration scheduled for proxy \""+p.Name+"\" to "+in.ControllerURL)
	respond(w, http.StatusOK, map[string]string{
		"status":         "scheduled",
		"controller_url": in.ControllerURL,
	})
}

func (s *Server) handleCancelProxyMigration(w http.ResponseWriter, r *http.Request) {
	var p, ok = s.proxyForAdmin(w, r)
	if !ok {
		return
	}
	var err = s.Store.SetProxyMigration(r.Context(), p.ID, "", "")
	if err != nil {
		fail(w, err)
		return
	}
	s.emitInstance(r, "proxy.migration_cancelled", model.SevInfo,
		"Migration cancelled for proxy \""+p.Name+"\"")
	respond(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// handleRenameProxy: renaming is an action on the proxy itself, therefore subject to
// the same right as revoking or deleting — a shared proxy is not renamed by one site's
// administrator alone.
func (s *Server) handleRenameProxy(w http.ResponseWriter, r *http.Request) {
	var p, ok = s.proxyForAdmin(w, r)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	var name = strings.TrimSpace(in.Name)
	if name == "" {
		respondErr(w, http.StatusBadRequest, "name required")
		return
	}
	if len([]rune(name)) > 80 {
		respondErr(w, http.StatusBadRequest, "name too long (80 characters maximum)")
		return
	}
	if name == p.Name {
		respond(w, http.StatusOK, p)
		return
	}
	var err = s.Store.RenameProxy(r.Context(), p.ID, name)
	if err != nil {
		fail(w, err)
		return
	}
	var updated, getErr = s.Store.GetProxy(r.Context(), p.ID)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	// The old name is kept in the message: without it, a rename log does not reread.
	s.emitInstance(r, "proxy.renamed", model.SevInfo,
		"Proxy \""+p.Name+"\" renamed to \""+updated.Name+"\"")
	respond(w, http.StatusOK, updated)
}

// handleSetProxyUpgrade sets a proxy's upgrade policy. An action on the proxy itself:
// same right as revoke or migrate — a proxy shared across sites is not upgraded on one
// site's decision alone (cf. docs/en/03-Security/RBAC.md).
func (s *Server) handleSetProxyUpgrade(w http.ResponseWriter, r *http.Request) {
	var p, ok = s.proxyForAdmin(w, r)
	if !ok {
		return
	}
	if p.Internal {
		respondErr(w, http.StatusConflict,
			"the \"Local\" proxy is the instance itself: it follows the controller image")
		return
	}
	var in struct {
		AutoUpgrade *bool  `json:"auto_upgrade"`
		PinVersion  string `json:"pin_version"`
		Now         bool   `json:"now"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	var auto = p.AutoUpgrade
	if in.AutoUpgrade != nil {
		auto = *in.AutoUpgrade
	}
	var pin = strings.TrimSpace(in.PinVersion)
	var err = s.Store.SetProxyUpgradePolicy(r.Context(), p.ID, auto, pin, in.Now)
	if err != nil {
		fail(w, err)
		return
	}
	var updated, getErr = s.Store.GetProxy(r.Context(), p.ID)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	var what = "automatic"
	if !auto {
		what = "manual"
	}
	if pin != "" {
		what += ", pinned to " + pin
	}
	if in.Now {
		what += ", upgrade requested now"
	}
	s.emitInstance(r, "proxy.upgrade_policy", model.SevNotice,
		"Upgrade policy for proxy \""+p.Name+"\": "+what)
	respond(w, http.StatusOK, updated)
}

// handleRevokeProxy toggles revocation — and CUTS the live session.
//
// Without the cut, "revoke" only mattered for the next authentication
// (Store.AuthProxy was the only place that read `revoked`): the proxy kept its yamux
// session, therefore device access and monitoring already received. The gesture did not do
// what it promises at the moment you need it — a stolen proxy.
func (s *Server) handleRevokeProxy(w http.ResponseWriter, r *http.Request) {
	var p, ok = s.proxyForAdmin(w, r)
	if !ok {
		return
	}
	// `p` carries BEFORE state: revoking is toggling.
	var revoking = !p.Revoked
	var err = s.Store.SetProxyRevoked(r.Context(), p.ID, revoking)
	if err != nil {
		fail(w, err)
		return
	}
	// DATABASE FIRST, SESSION SECOND, never the reverse. Cutting before revocation is
	// written would offer disconnection, not revocation: the proxy reconnects within a
	// second (tunnel backoff restarts from zero after a session that held, cf.
	// cmd/proxy/tunnel.go) and authentication would still accept it, on a database that
	// does not yet know it is revoked. This order is locked by a test.
	//
	// Reactivating cuts nothing, obviously: the inverse gesture.
	var cut string
	if revoking && s.Tunnels.CloseSession(p.ID, revokedTunnelReason) {
		cut = " — live tunnel cut"
	}
	var what = "revoked"
	if !revoking {
		what = "reactivated"
	}
	// The cut is stated IN the revocation message: the only way for the operator to verify,
	// from the log, that the gesture reached the live session and not only the database row.
	s.emitInstance(r, "proxy.revoked", model.SevWarning, "Proxy \""+p.Name+"\" "+what+cut)
	respond(w, http.StatusOK, map[string]string{"status": what})
}

func (s *Server) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	var p, ok = s.proxyForAdmin(w, r)
	if !ok {
		return
	}
	var err = s.Store.DeleteProxy(r.Context(), p.ID)
	if err != nil {
		fail(w, err)
		return
	}
	// Deleting is at least as strong as revoking: with the row gone, nobody can see or
	// revoke this proxy, while its session would still serve. Same order: database first.
	var cut string
	if s.Tunnels.CloseSession(p.ID, goneTunnelReason) {
		cut = " — live tunnel cut"
	}
	s.emitInstance(r, "proxy.deleted", model.SevWarning,
		"Proxy \""+p.Name+"\" deleted from inventory"+cut)
	respond(w, http.StatusOK, map[string]string{"status": "ok"})
}

// --- Safety net: revocation also matters when it does not go through here ---
//
// Cutting the session on click (above) does not cover everything: revocation done directly
// in SQL, pronounced by another instance, or occurring while this controller was restarting
// would leave a revoked proxy connected indefinitely. And revocation is exactly the gesture
// when you are in a hurry and cannot depend on any UI path. Hence periodic revalidation,
// which asks the database whether established tunnels still have the right to exist.
//
// The loop lives HERE and not in internal/tunnel: that package does not know the database
// and must not acquire it — the API layer knows how to query the store (same sharing as
// PushWatchTo or handleProxyEvent).

// Cut reasons. Constants because they go to TWO places — application log (slog, in the Hub)
// and fleet log (events) — and a reason that differs between the two reads as two incidents.
const (
	revokedTunnelReason  = "proxy revoked"
	goneTunnelReason     = "proxy deleted from inventory"
	migratedTunnelReason = "proxy migrated to another instance"
)

// tunnelRevalidate: safety-net cadence.
//
// One minute, and the trade-off reads both ways. The UPPER bound is an exposure window:
// time during which a revoked proxy outside this API keeps connections to devices and
// monitoring already received. The LOWER bound is cost: one primary-key read per CONNECTED
// proxy. Each of those proxies' heartbeats already touches the same row three times every
// thirty seconds (AuthProxy, TouchProxy, GetProxy) — one more read per minute is not
// measurable beside that. Going lower buys seconds of window at the price of a loop querying
// the database to learn nothing almost all the time; going higher lets a compromised proxy
// live several minutes.
//
// The "revoke" click does not wait for it: it cuts immediately. This net only catches what
// did not go through it.
const tunnelRevalidate = time.Minute

// tunnelRevalidateBudget caps one pass. A pass that drags beyond would be replayed on the
// next tick anyway, and a database that does not answer in thirty seconds will say nothing
// useful in five minutes — but would hold a pool connection.
const tunnelRevalidateBudget = 30 * time.Second

// tunnelVerdict: what the database says about a proxy whose tunnel is established.
type tunnelVerdict int

const (
	// tunnelKeep: this proxy is in good standing, leave it alone. ZERO value, and not
	// convenience: everything that is not a OBSERVED revocation must read "cut nothing". A
	// default that cut would disconnect the whole fleet at the first second of PostgreSQL
	// unavailability — the cure worse than the disease.
	tunnelKeep tunnelVerdict = iota
	// tunnelRevoked: the database says this proxy is revoked.
	tunnelRevoked
	// tunnelGone: the row no longer exists (direct deletion, other instance).
	tunnelGone
)

func (v tunnelVerdict) reason() string {
	switch v {
	case tunnelRevoked:
		return revokedTunnelReason
	case tunnelGone:
		return goneTunnelReason
	}
	return ""
}

// tunnelCheck: the verdict, plus the proxy name when the database still knows it.
// The name travels with the verdict because the fleet log is reread with names, not UUIDs
// — and it must be read BEFORE cutting.
type tunnelCheck struct {
	verdict tunnelVerdict
	name    string
}

// cutDisownedTunnels cuts tunnels the database disowns and returns the cut count.
//
// `probe` is the ONLY database contact, passed as a parameter: that makes policy
// testable — cut a revoked one, cut a deleted one, and especially CUT NOTHING when read
// fails — without mounting database or server.
//
// A read error is logged and the proxy left alone: "I do not know" is not "it is revoked".
// Over-eager revalidation would be a fleet outage.
func cutDisownedTunnels(hub *tunnel.Hub, probe func(proxyID string) (tunnelCheck, error),
	onCut func(proxyID string, c tunnelCheck)) int {
	var cuts int
	var proxyID string
	for _, proxyID = range hub.ProxyIDs() {
		var c, err = probe(proxyID)
		if err != nil {
			slog.Warn("tunnel revalidation: proxy not reread, no cut",
				"proxy_id", proxyID, "err", err)
			continue
		}
		if c.verdict == tunnelKeep {
			continue
		}
		if !hub.CloseSession(proxyID, c.verdict.reason()) {
			// Session already gone on its own between the two: nothing to announce.
			continue
		}
		cuts++
		if onCut != nil {
			onCut(proxyID, c)
		}
	}
	return cuts
}

// revalidateTunnels runs for the process lifetime, like sweepPortFlaps and for the same
// reason: a revocation this instance did not pronounce can only be observed by a clock.
// Started by Router().
func (s *Server) revalidateTunnels() {
	var tick = time.NewTicker(tunnelRevalidate)
	defer tick.Stop()
	for range tick.C {
		s.revalidateTunnelsOnce()
	}
}

func (s *Server) revalidateTunnelsOnce() {
	// A hand-built Server in a test has neither hub nor database: revalidate nothing rather
	// than panic in a background goroutine.
	if s.Tunnels == nil || s.Store == nil {
		return
	}
	var ctx, cancel = context.WithTimeout(context.Background(), tunnelRevalidateBudget)
	defer cancel()
	cutDisownedTunnels(s.Tunnels, func(proxyID string) (tunnelCheck, error) {
		return tunnelCheckFor(s.Store.GetProxy(ctx, proxyID))
	}, s.noteTunnelCut)
}

// tunnelCheckFor translates a proxy read into a verdict.
//
// Separated from the read itself — it takes the result as-is — to be testable without a
// database: policy is read here, and policy buried in a SQL call is only verifiable with
// PostgreSQL at hand.
func tunnelCheckFor(p model.Proxy, err error) (tunnelCheck, error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		// `p` is empty here: the name will be the UUID, for lack of better. Price of a
		// proxy with no row left.
		return tunnelCheck{verdict: tunnelGone}, nil
	case err != nil:
		// Returned verdict is the NULL verdict, and error propagates: caller cuts nothing.
		return tunnelCheck{}, err
	case p.Revoked:
		return tunnelCheck{verdict: tunnelRevoked, name: p.Name}, nil
	}
	return tunnelCheck{verdict: tunnelKeep, name: p.Name}, nil
}

// noteTunnelCut logs the cut to the fleet log, outside HTTP request.
//
// No user clicked, and that is precisely the information: this cut comes from a revocation
// this instance did not pronounce. Without this line, the operator would see a proxy drop
// with no visible cause.
func (s *Server) noteTunnelCut(proxyID string, c tunnelCheck) {
	var who = c.name
	if who == "" {
		who = proxyID
	}
	var err = s.Store.InsertEvent(context.Background(), model.Event{
		Type: "proxy.tunnel_cut", Severity: model.SevWarning,
		Message: "Tunnel cut with proxy \"" + who + "\": " + c.verdict.reason() +
			" (observed by periodic revalidation, outside user action)",
		Actor: "meshcraft",
	})
	if err != nil {
		slog.Error("event log: tunnel cut not recorded",
			"err", err, "proxy_id", proxyID)
	}
}

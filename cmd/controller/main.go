// The meshcraft controller: API, embedded SPA, proxy docker registry.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"meshcraft/internal/api"
	"meshcraft/internal/auth"
	"meshcraft/internal/config"
	"meshcraft/internal/debuglog"
	"meshcraft/internal/distrib"
	"meshcraft/internal/relocate"
	"meshcraft/internal/renumber"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
	"meshcraft/internal/tsdb"
	"meshcraft/internal/tunnel"
)

// Injected at build time via -ldflags (see Dockerfile and Makefile):
//
//	version = product version (VERSION file, e.g. 0.1.0);
//	build   = UTC build timestamp YYYYMMDD.HHMMSS, which actually
//	          identifies the deployed image
//	          (see docs/en/01-Architecture/Proxy versioning and updates.md).
var (
	version = "dev"
	build   = "dev"
)

func main() {
	// Flags. The controller used to have none; `-rekey` introduces one
	// because switching the vault key over is an OPERATIONAL ACT, not an API
	// route: giving it a permanent HTTP surface would permanently leave, on
	// an exposed product, a way to rewrite the entire vault in a single
	// request. See the header of rekey.go for the full rationale.
	var doRekey = flag.Bool("rekey", false,
		"rekeys the credentials vault with MESHCRAFT_SECRET_KEY, then exits "+
			"(the HTTP server does not start)")
	var rekeyForce = flag.Bool("rekey-force", false,
		"with -rekey: bypasses the refusal when other connections to the "+
			"database are open (the controller MUST be stopped)")
	flag.Parse()

	// The log goes to standard output AS BEFORE, plus into an in-memory ring
	// buffer that administration can consult live
	// (see docs/en/01-Architecture/Unified monitoring and pipelines.md §6). "Plus",
	// never "instead of": diverting the stream would lose the container's
	// logs at the moment they are needed most.
	var debugRing = debuglog.NewRing(3000)
	slog.SetDefault(slog.New(debuglog.NewHandler(
		slog.NewJSONHandler(os.Stdout, nil), debugRing)))
	var cfg = config.Load()
	var ctx = context.Background()

	// The database can take a few seconds to accept connections (docker
	// compose startup): retry briefly.
	var st *store.Store
	var err error
	var attempt int
	for attempt = 0; attempt < 30; attempt++ {
		st, err = store.New(ctx, cfg.DatabaseURL)
		if err == nil {
			break
		}
		slog.Info("waiting for the database", "attempt", attempt+1, "err", err.Error())
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if err = st.Migrate(ctx); err != nil {
		slog.Error("migrations", "err", err)
		os.Exit(1)
	}

	// The rekey mode stops here: it does not create an admin account, does
	// not backfill any credentials, and does not serve a single request. It
	// performs the switchover, prints its report, and exits. The migrations,
	// on the other hand, have indeed been applied — it needs the fingerprint
	// table (0024).
	if *doRekey {
		exitRekey(ctx, cfg, st, *rekeyForce)
	}

	if err = bootstrapAdmin(ctx, cfg, st); err != nil {
		slog.Error("bootstrap admin", "err", err)
		os.Exit(1)
	}

	var secrets *secretbox.Service
	secrets, err = initSecrets(ctx, cfg, st)
	if err != nil {
		// To the error output IN PLAIN TEXT, not just in the JSON log: the
		// refusal to start on an incompatible key is a multi-line message
		// written to be read by a human looking at `docker compose logs`,
		// who must find the fix in it. Wrapped in JSON, it becomes a 2,000
		// character line with literal \n's — i.e. unreadable, at the exact
		// moment it needs to be read.
		if errors.Is(err, ErrKeyMismatch) {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, "REFUSING TO START — "+err.Error())
			fmt.Fprintln(os.Stderr)
		}
		slog.Error("encryption key: refusing to start", "err", err)
		os.Exit(1)
	}

	// Backfill for sites created before the automation existed: each one
	// gets its admin credentials if it doesn't have any. Necessarily after
	// the key is initialized — it's the key that seals the secret.
	if err = api.BackfillSiteCredentials(ctx, st, secrets); err != nil {
		slog.Error("site credentials", "err", err)
	}

	// Local proxy is the controller itself: it never heartbeats over WSS, so mark it
	// present at startup with the product version (UI also special-cases internal).
	var touchErr = st.TouchProxy(ctx, store.LocalProxyID, version, 0, "", "", nil)
	if touchErr != nil {
		slog.Warn("touch proxy Local", "err", touchErr)
	}

	// Periodic purge of expired sessions and vanished candidates.
	go func() {
		var t = time.NewTicker(time.Hour)
		for range t.C {
			var bg = context.Background()
			_ = st.PurgeExpiredSessions(bg)
			// A host not seen for 30 days and never adopted has no business
			// left in the adoption queue.
			var purged, purgeErr = st.PurgeStaleDiscovered(bg, 30*24*time.Hour)
			if purgeErr == nil && purged > 0 {
				slog.Info("stale candidates purged", "count", purged)
			}
		}
	}()

	var srv = &api.Server{
		Cfg:      cfg,
		Version:  version,
		Build:    build,
		Store:    st,
		Auth:     &auth.Service{Store: st},
		Distrib:  distrib.New(cfg),
		Secrets:  secrets,
		Tunnels:  tunnel.NewHub(),
		Relocate: relocate.NewTracker(),
		Renumber: renumber.NewTracker(),
		// Automatic apply: configuring it in the UI counts as approval
		// (see internal/api/autoapply.go).
		Apply: api.NewAutoApplier(),
		Debug: debugRing,
		// Time series. Absent = meshcraft works without metrics and says so;
		// the inventory doesn't need it.
		TSDB: tsdb.New(os.Getenv("MESHCRAFT_METRICS_URL")),
	}
	// PER-PORT RATES, derived from the counters and reported in the inventory.
	//
	// After the server is built because it holds the time-series storage and
	// the database; run in the background because nothing this loop does is
	// necessary to answer a request. With no storage configured, it says so
	// once and stops (see RunPortRates).
	//
	// This is the piece that made telemetry VISIBLE: three screens were
	// already reading `rx_mbps` / `tx_mbps` on every port, and no code was
	// writing them.
	go srv.RunPortRates(context.Background())

	// SNMP setup backfill: a device adopted BEFORE its connector knows how
	// to write to it (or unreachable at the moment of adoption) must end up
	// monitored without depending on deployment order or on a click that
	// nobody knows they need to make (see internal/api/snmpheal.go).
	go srv.RunSNMPHeal(context.Background())

	// Periodic PoE draw sampling: the CLI-read wattage that no SNMP OID
	// serves, refreshed on a schedule instead of only on the read-state
	// click — per-port `poe_w` in inventory, a fresh `poe_observed`, and a
	// wattage series when a store exists (see internal/api/poesample.go).
	// Cadence: MESHCRAFT_POE_SAMPLE_EVERY (default 5m, "off" disables).
	go srv.RunPoESampler(context.Background())

	// Periodic transceiver-DDM sampling: the CLI leg of optical level history,
	// for platforms whose only vantage point on their cages is a show command
	// (driver.OpticsReader — the FS S3400 case). Refreshes `optics_observed`
	// and feeds the sfp dBm series; exits when no series store is configured
	// (see internal/api/opticssample.go).
	// Cadence: MESHCRAFT_OPTICS_SAMPLE_EVERY (default 5m, "off" disables).
	go srv.RunOpticsSampler(context.Background())

	// Client bandwidth buckets: attributes each port's octet increase to the
	// MACs located there, once a minute. Exits when no series store is
	// configured (see internal/api/clientbuckets.go).
	go srv.RunClientBuckets(context.Background())

	// Forwarding-table sampler: periodic CLI reads for the platforms neither
	// collector serves (EOS — its gNMI doors were probed closed on 4.28).
	// Cadence: MESHCRAFT_FDB_SAMPLE_EVERY (default 2m, "off" disables).
	go srv.RunFDBSampler(context.Background())

	// System-name reconciliation: meshcraft owns the device's own hostname, so a
	// device renamed at its console — or one that never received the name it was
	// given in the inventory — is repaired on a cadence, and the repair is visible
	// in the device's history (see internal/api/hostnameheal.go). The comparison
	// itself opens NO device session: the name the device announces already arrives
	// on every SNMP poll cycle as its LLDP system name.
	go srv.RunHostnameHeal(context.Background())

	// Site-extension import, every 30 s, measured from the END of the previous
	// pass so a slow controller cannot stack up overlapping passes (ADR-0029
	// §11). ONE goroutine for the whole instance, walking every paired
	// extension sequentially — the cost to bound is the remote controller's,
	// not ours. It is deliberately quiet: only transitions reach the journal,
	// because a line every 30 s would drown the page humans read
	// (see internal/api/extsync.go).
	go srv.RunExtSync(context.Background())

	// Periodic CONFIG read-back — the hole the three loops above do not fill.
	// Everything that polls continuously today polls TELEMETRY: the proxy's SNMP
	// poller, RunPortRates, the PoE sampler, ping watch. Configuration — a port
	// shut at the console, VLAN membership changed by hand, drift — lives behind
	// an administration session, and until this loop the only thing that ever
	// opened one on a schedule was an operator clicking "read state". Between
	// two clicks the screens showed meshcraft's own intentions and an observation
	// of unknown age (see internal/api/configpoll.go). It runs the SAME gesture
	// the button runs, one device at a time, stalest first, never beside an
	// apply. Cadence: MESHCRAFT_CONFIG_POLL_EVERY (default 30m, "off" disables).
	go srv.RunConfigPoll(context.Background())
	// The alerting queue drains the delivery slips filing wrote (ADR-0024).
	// It is the SENDING half only: classification and filing happen on the
	// journal write path, so not starting this loop leaves detection and the
	// journal exactly as they were and simply delivers nothing — a degradation
	// the screen shows as a growing pending count, rather than a channel that
	// reads as healthy while silent.
	go srv.RunNotifyQueue(context.Background())

	slog.Info("meshcraft controller started", "listen", cfg.Listen,
		"version", version, "build", build)
	// The SOCKS route to device web interfaces (cf. internal/socks). Started
	// AFTER the router is built so the tunnel hub and the store it depends on are
	// in place, and silently skipped when MESHCRAFT_SOCKS_LISTEN is unset — this
	// is a door into customer management networks and it does not open by
	// accident.
	srv.StartSocks(context.Background(), cfg.SocksListen)
	// The drivers' OWN SOCKS listener + the external driver endpoints (cf.
	// internal/api/driversocks.go). Off unless configured, same rule as above.
	srv.StartDriverSocks(context.Background())
	// The agent-image build pipeline (git→OCI-layout→registry). Needs only the
	// Docker socket, not the driver SOCKS listener, so it is started on its own.
	srv.StartAgentHost(context.Background())

	var httpSrv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err = httpSrv.ListenAndServe(); err != nil {
		slog.Error("http server", "err", err)
		os.Exit(1)
	}
}

// bootstrapAdmin creates the first global-admin account if the database is empty.
// Password: MESHCRAFT_ADMIN_PASSWORD, otherwise generated and displayed ONCE.
func bootstrapAdmin(ctx context.Context, cfg config.Config, st *store.Store) error {
	var n, err = st.CountUsers(ctx)
	if err != nil || n > 0 {
		return err
	}
	var password = cfg.AdminPassword
	var generated = false
	if password == "" {
		var raw = make([]byte, 12)
		if _, err = rand.Read(raw); err != nil {
			return err
		}
		password = hex.EncodeToString(raw)
		generated = true
	}
	var hash, hashErr = auth.HashPassword(password)
	if hashErr != nil {
		return hashErr
	}
	if _, err = st.CreateUser(ctx, cfg.AdminEmail, hash, "Administrator", true); err != nil {
		return err
	}
	if generated {
		slog.Warn("initial admin account created — WRITE DOWN THIS PASSWORD, it will not be shown again",
			"email", cfg.AdminEmail, "password", password)
	} else {
		slog.Info("initial admin account created", "email", cfg.AdminEmail)
	}
	return nil
}

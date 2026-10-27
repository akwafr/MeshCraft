package api

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driverhost"
	"meshcraft/internal/imagebuild"
	"meshcraft/internal/restdriver"
	"meshcraft/internal/socks"
)

// The drivers' SOCKS listener — the one road an external driver container has
// to its device.
//
// A SECOND listener, deliberately not the operator one (StartSocks): the two
// populations authenticate from different worlds — operator tokens live in
// Postgres and resolve against site roles, driver credentials live in the
// in-memory [socks.Minter] and resolve against exactly one host each. One
// listener serving both would need a multiplexing authenticator whose failure
// mode is answering the wrong world's question.

// StartDriverSocks starts the listener and loads the configured driver
// endpoints. Silently skipped when MESHCRAFT_DRIVER_SOCKS_LISTEN is unset —
// external drivers do not exist for this instance then, and the transitional
// MESHCRAFT_DRIVER_URLS is ignored with a warning if set alone.
func (s *Server) StartDriverSocks(ctx context.Context) {
	if s.Cfg.DriverSocksListen == "" {
		if s.Cfg.DriverURLs != "" {
			slog.Warn("driver endpoints configured but MESHCRAFT_DRIVER_SOCKS_LISTEN is unset: " +
				"external drivers have no road to the devices, nothing was loaded")
		}
		return
	}
	var minter = socks.NewMinter()
	s.DriverMinter = minter

	var ln, err = net.Listen("tcp", s.Cfg.DriverSocksListen)
	if err != nil {
		slog.Error("driver socks: listener not started",
			"listen", s.Cfg.DriverSocksListen, "err", err.Error())
		return
	}
	slog.Info("driver socks: listening", "listen", s.Cfg.DriverSocksListen)

	var srv = &socks.Server{
		Resolver: minter,
		Auth:     minter,
		Dialer:   driverSocksDialer{s: s},
		OnRefusal: func(label, host string, port int, reason string) {
			// A driver asking beyond its minted host is the interesting event
			// on this listener — it is either a bug or an escape attempt, and
			// both deserve a line.
			slog.Warn("driver socks: refused", "label", label,
				"dst", net.JoinHostPort(host, strconv.Itoa(port)), "reason", reason)
		},
	}
	go func() { _ = srv.Serve(ctx, ln) }()
	go func() {
		var tick = time.NewTicker(5 * time.Minute)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				minter.Sweep()
			}
		}
	}()

	var advertise = s.Cfg.DriverSocksAdvertise
	if advertise == "" {
		advertise = s.Cfg.DriverSocksListen
	}
	var rt = restdriver.Runtime{Minter: minter, SocksAdvertise: advertise}

	// The git→build→run pipeline, when a Docker socket is configured: the
	// declarative layer (driver_repos) is reconciled in the background —
	// startup must not wait on container builds.
	if s.Cfg.DockerSocket != "" {
		s.DriverHost = driverhost.New(s.Store, imagebuild.New(s.Cfg.DockerSocket), rt)
		go s.DriverHost.Reconcile(ctx)
	}

	// Transitional hand-run endpoints (MESHCRAFT_DRIVER_URLS), for pilots
	// before the pipeline: loaded alongside, same runtime.
	if s.Cfg.DriverURLs == "" {
		return
	}
	var errs = restdriver.LoadAll(ctx, splitCSV(s.Cfg.DriverURLs), rt)
	var e error
	for _, e = range errs {
		slog.Error("external driver not loaded", "err", e.Error())
	}
}

// driverSocksDialer routes a minted credential's connection: through the
// frozen proxy, or straight from this machine when the route is "Local" —
// the same two cases dialerFor distinguishes for the in-process path.
type driverSocksDialer struct{ s *Server }

func (d driverSocksDialer) Dial(ctx context.Context, proxyID, host string, port int) (net.Conn, error) {
	if proxyID == "" {
		var nd net.Dialer
		return nd.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	return d.s.Tunnels.Dial(ctx, proxyID, host, port)
}

// splitCSV splits a comma-separated list, trimming blanks.
func splitCSV(s string) []string {
	var out []string
	var part string
	for _, part = range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Package config loads controller configuration from the environment.
package config

import (
	"os"
)

type Config struct {
	// Listen is the HTTP listen address, e.g. ":8080".
	Listen string
	// DatabaseURL is the PostgreSQL URL (postgres://user:pass@host/db).
	DatabaseURL string
	// ExternalURL is the controller's public URL (used to generate
	// proxy docker-compose files). If empty, derived from the HTTP request.
	ExternalURL string
	// AdminEmail / AdminPassword bootstrap the first global-admin account
	// on first startup (if the users table is empty).
	AdminEmail    string
	AdminPassword string
	// ArtifactsDir contains proxy binaries to distribute.
	ArtifactsDir string
	// OCIDir contains the OCI layout of the proxy docker image,
	// served by the built-in registry (/v2/...).
	OCIDir string
	// SecretKey (64 hex characters) encrypts standard discovery credentials.
	// Empty = random key stored in the database, with a warning at startup.
	SecretKey string

	// SocksListen is the bind address of the SOCKS5 route to device web
	// interfaces (cf. internal/socks). EMPTY = the listener does not start.
	//
	// Off by default because it is a door into customer management networks, and
	// SOCKS5 carries no transport security: a non-loopback bind sends the token
	// and everything the browser exchanges with the switch in clear. The
	// controller warns at startup when it is asked to bind beyond loopback.
	SocksListen string

	// DriverSocksListen is the bind address of the SECOND SOCKS5 listener,
	// the one external driver containers reach their devices through
	// (credentials minted per driver session, cf. internal/socks.Minter).
	// EMPTY = no external drivers: the listener does not start and driver
	// endpoints are not loaded. Deliberately a separate listener from
	// SocksListen — operators and drivers authenticate from different worlds
	// and must not share a door.
	DriverSocksListen string
	// DriverSocksAdvertise is the same endpoint AS THE DRIVER CONTAINERS DIAL
	// IT (a bridge-gateway address, where the bind may be 0.0.0.0). Defaults
	// to DriverSocksListen when empty.
	DriverSocksAdvertise string
	// DockerSocket is the Docker Engine socket the driver pipeline builds and
	// runs containers through. EMPTY = the git→build→run pipeline is off; the
	// controller execs nothing either way (cf. internal/imagebuild).
	DockerSocket string
	// DriverURLs lists MDP driver endpoints to load at startup,
	// comma-separated (http://host:port). TRANSITIONAL: the git→build→run
	// pipeline replaces this with database-managed driver repositories; this
	// exists so the wire path can be proven with hand-run containers first.
	DriverURLs string

	// PoESampleEvery is the cadence of the periodic PoE draw sampler
	// (internal/api/poesample.go), as a Go duration ("5m", "90s"). Kept RAW:
	// parsing lives with the sampler, which has a logger to say when the
	// value is unreadable. Empty = default (5m); "0" or "off" disables.
	PoESampleEvery string

	// OpticsSampleEvery is the cadence of the periodic transceiver-DDM sampler
	// (internal/api/opticssample.go), as a Go duration. Same RAW convention as
	// PoESampleEvery and same defaulting: empty = default (5m); "0"/"off"
	// disables. It only concerns platforms whose optics are CLI-read
	// (driver.OpticsReader); platforms whose collector already streams or polls
	// the readings never enter the pass.
	OpticsSampleEvery string

	// FDBSampleEvery is the cadence of the periodic forwarding-table sampler
	// (internal/api/fdbsample.go), same RAW convention: empty = default (2m);
	// "0"/"off" disables. It only concerns platforms whose FDB is CLI-read
	// (driver.FDBReader — EOS today); platforms whose collector polls the
	// bridge MIBs never enter the pass.
	FDBSampleEvery string

	// ConfigPollEvery is the cadence of the periodic config read-back
	// (internal/api/configpoll.go), as a Go duration ("30m", "2h"). Kept RAW for
	// the same reason as PoESampleEvery: parsing lives with the loop, which has a
	// logger to say when the value is unreadable. Empty = default (30m); "0" or
	// "off" disables — and disabling it means observed state, port link state and
	// drift refresh only when an operator clicks read-state.
	ConfigPollEvery string
}

func env(key, def string) string {
	var v = os.Getenv(key)
	if v != "" {
		return v
	}
	return def
}

func Load() Config {
	return Config{
		Listen:               env("MESHCRAFT_LISTEN", ":8080"),
		DatabaseURL:          env("DATABASE_URL", "postgres://meshcraft:meshcraft@localhost:5432/meshcraft?sslmode=disable"),
		ExternalURL:          env("MESHCRAFT_EXTERNAL_URL", ""),
		AdminEmail:           env("MESHCRAFT_ADMIN_EMAIL", "admin@meshcraft.local"),
		AdminPassword:        env("MESHCRAFT_ADMIN_PASSWORD", ""),
		ArtifactsDir:         env("MESHCRAFT_ARTIFACTS_DIR", "/srv/meshcraft/artifacts"),
		OCIDir:               env("MESHCRAFT_OCI_DIR", "/srv/meshcraft/oci"),
		SecretKey:            env("MESHCRAFT_SECRET_KEY", ""),
		SocksListen:          env("MESHCRAFT_SOCKS_LISTEN", ""),
		DriverSocksListen:    env("MESHCRAFT_DRIVER_SOCKS_LISTEN", ""),
		DriverSocksAdvertise: env("MESHCRAFT_DRIVER_SOCKS_ADVERTISE", ""),
		DockerSocket:         env("MESHCRAFT_DOCKER_SOCKET", ""),
		DriverURLs:           env("MESHCRAFT_DRIVER_URLS", ""),
		PoESampleEvery:       env("MESHCRAFT_POE_SAMPLE_EVERY", ""),
		OpticsSampleEvery:    env("MESHCRAFT_OPTICS_SAMPLE_EVERY", ""),
		FDBSampleEvery:       env("MESHCRAFT_FDB_SAMPLE_EVERY", ""),
		ConfigPollEvery:      env("MESHCRAFT_CONFIG_POLL_EVERY", ""),
	}
}

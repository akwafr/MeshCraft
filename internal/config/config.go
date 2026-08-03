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
		Listen:        env("MESHCRAFT_LISTEN", ":8080"),
		DatabaseURL:   env("DATABASE_URL", "postgres://meshcraft:meshcraft@localhost:5432/meshcraft?sslmode=disable"),
		ExternalURL:   env("MESHCRAFT_EXTERNAL_URL", ""),
		AdminEmail:    env("MESHCRAFT_ADMIN_EMAIL", "admin@meshcraft.local"),
		AdminPassword: env("MESHCRAFT_ADMIN_PASSWORD", ""),
		ArtifactsDir:  env("MESHCRAFT_ARTIFACTS_DIR", "/srv/meshcraft/artifacts"),
		OCIDir:        env("MESHCRAFT_OCI_DIR", "/srv/meshcraft/oci"),
		SecretKey:     env("MESHCRAFT_SECRET_KEY", ""),
		SocksListen:   env("MESHCRAFT_SOCKS_LISTEN", ""),
	}
}

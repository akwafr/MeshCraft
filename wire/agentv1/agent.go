// Package agentv1 is the contract between a controller and a proxy AGENT: the
// HTTP lifeline (enroll, heartbeat, migrate) and the credentialed pushes
// (metrics, and — by reference — the LLDP and FDB snapshots). It is the second
// wire contract of the rearchitecture, beside meshcraft/wire/mdpv1, and it
// exists for the same reason: the proxy and the controller are two binaries
// that must agree byte for byte on what crosses between them, and a type with
// two hand-maintained copies drifts.
//
// Until this module existed the lifeline structs were ANONYMOUS on the
// controller side (internal/api/proxy_handlers.go) and RETYPED on the proxy
// side (cmd/proxy). Two declarations of one message is exactly where a field
// added on one side is silently ignored on the other. These types are now the
// single declaration both import.
//
// The rules a schema cannot state, restated in contracts/agent/v1/openapi.yaml:
//
//   - A proxy has NO session. It authenticates with the (proxy_id, secret)
//     pair returned at enrollment, carried in the body of every subsequent
//     call — never a cookie, never a bearer token.
//   - Versions are UTC build stamps (yyyymmdd.HHMMSS). "dev" sorts before
//     every real build and is never offered an upgrade — the same lesson the
//     driver contract enforces.
//   - Migration takes priority over upgrade: a proxy told to migrate does that
//     first, then upgrades from the new instance.
//
// The multiplexed tunnel (yamux over WSS) is a SEPARATE control plane; its
// wire types live in internal/tunnel and are documented under
// contracts/tunnel/. This package is the HTTP half only.
package agentv1

import (
	"encoding/json"

	"meshcraft/wire/metrics"
)

// Protocol is the agent protocol major. A controller refuses a mismatched
// major rather than degrade — the same refuse-never-degrade rule as mdpv1.
const Protocol = 1

// PathPrefix is where the versioned lifeline answers. The legacy unversioned
// routes (/api/proxy/*) remain as aliases — a fielded proxy speaks them, and
// a contract that breaks its own agents on the day it is written is no
// contract.
const PathPrefix = "/api/v1/agent"

// --- Enrollment: token in, durable identity out ---

// EnrollRequest exchanges a one-time enrollment token for a lasting identity.
type EnrollRequest struct {
	Token string `json:"token"`
	// Name is what the operator will read in the fleet list. Empty lets the
	// controller mint one from the clock.
	Name string `json:"name"`
}

// EnrollResponse carries the identity the proxy persists and authenticates
// with forever after.
type EnrollResponse struct {
	Status  string `json:"status"`
	ProxyID string `json:"proxy_id"`
	// Secret is shown ONCE, here: the controller stores only its hash.
	Secret string `json:"secret"`
	// ControllerURL is the canonical public URL the proxy should use from now
	// on, when the controller wants to override the address enrollment used.
	ControllerURL string `json:"controller_url"`
	// Error names the refusal when Status is not "enrolled".
	Error string `json:"error,omitempty"`
}

// --- Heartbeat: presence, self-report, and pending orders ---

// HeartbeatRequest is sent on a cadence; it proves the proxy is alive, reports
// what it is running, and carries its last upgrade attempt when there is one.
type HeartbeatRequest struct {
	ProxyID  string `json:"proxy_id"`
	Secret   string `json:"secret"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	// Ifaces is the proxy's own interface list, opaque here (the controller
	// decodes it): a proxy declares where it can reach, for trap-source and
	// route decisions.
	Ifaces json.RawMessage `json:"ifaces,omitempty"`
	// Upgrade is present only when there is something to report about the last
	// offered binary.
	Upgrade *UpgradeReport `json:"upgrade,omitempty"`
}

// UpgradeReport is what the proxy says about an upgrade it attempted. A silent
// failure would be worse than no upgrade: the controller would replay a
// binary that will not start.
type UpgradeReport struct {
	State   string `json:"state"` // downloading | applied | failed
	Version string `json:"version"`
	Error   string `json:"error,omitempty"`
}

// HeartbeatResponse carries pending orders. At most one of Migrate / Upgrade
// is set; Migrate wins when both would apply.
type HeartbeatResponse struct {
	Status string `json:"status"` // ok | migrate
	// Migrate, when set, orders the proxy to attach to another instance.
	Migrate *MigrateOrder `json:"migrate,omitempty"`
	// Upgrade, when set, offers a binary to download and re-exec into.
	Upgrade *UpgradeOffer `json:"upgrade,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// MigrateOrder points the proxy at a new controller and the token to enroll
// there with.
type MigrateOrder struct {
	ControllerURL string `json:"controller_url"`
	EnrollToken   string `json:"enroll_token"`
}

// UpgradeOffer is a binary the proxy should run, with the proof it needs to
// trust it: the SHA-256 is verified before the downloaded file is ever
// executed.
type UpgradeOffer struct {
	Version string `json:"version"`
	URL     string `json:"url"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

// --- Migration acknowledgement ---

// MigratedRequest is the proxy telling its OLD instance it has attached to a
// new one and can be forgotten. Best-effort: the proxy has already persisted
// its new identity before sending this, and treats its failure as harmless.
type MigratedRequest struct {
	ProxyID       string `json:"proxy_id"`
	Secret        string `json:"secret"`
	NewController string `json:"new_controller"`
}

// --- Metrics push ---

// MetricsRequest is a batch of samples the proxy buffered and flushes. The
// sample shape is meshcraft/wire/metrics — the ONE series vocabulary every
// producer shares — so the batch cannot disagree with what the controller
// ingests.
type MetricsRequest struct {
	ProxyID string           `json:"proxy_id"`
	Secret  string           `json:"secret"`
	Samples []metrics.Sample `json:"samples"`
}

// The LLDP and FDB snapshot pushes share this credentialed envelope; their
// report bodies (neighbor tables, forwarding tables) are large, inventory-
// shaped types that still live beside their producer in internal/snmppoll and
// are documented in the OpenAPI by reference. They move onto the wire when a
// non-Go agent needs to produce them; until then the envelope is what the
// contract pins.

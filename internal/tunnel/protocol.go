// Package tunnel carries the controller ↔ proxy link: an outbound WebSocket,
// multiplexed with yamux, in which the controller opens TCP connections toward
// devices on the proxy's network.
//
// This is phase 0b of the controller-proxy protocol (docs/en/01-Architecture/Proxy.md). The
// document describes custom framing (type/channel/payload) AND decides: "yamux
// over WS for MVP". With yamux, the natural mapping of channels is **one stream
// per connection**, preceded by a one-line JSON preamble:
//
//	controller → proxy: {"op":"dial","host":"10.100.0.10","port":4443}\n
//	proxy → controller: {"op":"dial_ok"}\n   or   {"op":"dial_err","err":"…"}\n
//	then                 raw bytes, both ways, until close.
//
// The control channel (ping/pong) need not be reinvented: yamux keepalive does
// it, at the same orders of magnitude as the spec.
package tunnel

import (
	"strings"
	"time"
)

const (
	// Path is the tunnel route on the controller. The proxy connects there via
	// WSS; authentication happens via headers BEFORE the upgrade, with the same
	// (proxy_id, secret) pair as the heartbeat.
	Path = "/api/proxy/tunnel"

	HeaderProxyID = "X-Meshcraft-Proxy-Id"
	HeaderSecret  = "X-Meshcraft-Proxy-Secret"
	HeaderVersion = "X-Meshcraft-Proxy-Version"
	// HeaderFeatures: comma-separated protocol capabilities the proxy CLAIMS, read
	// at the handshake and never afterwards.
	//
	// # Why not the version string, which is already here
	//
	// Because a version is text a build stamps, and this fleet has already shipped
	// proxies stamped "dev" (cf. the proxy release notes): any rule of the form
	// "≥ 1.4 understands X" is a rule that silently mis-fires on the binaries that
	// most need care. A capability token is stated by the code that implements the
	// capability, so it cannot drift from it.
	//
	// # The only thing it is allowed to mean
	//
	// PRESENT = the proxy will produce the signal. ABSENT = we know nothing, and the
	// controller must behave exactly as it did before the capability existed. It is
	// never read as a claim about the proxy's age, and a proxy that announces a token
	// then stays silent must still be survivable — every consumer of this header is
	// therefore expected to carry its own bound (cf. Event resync.done, whose
	// controller-side hold is capped by a startup window).
	HeaderFeatures = "X-Meshcraft-Proxy-Features"

	// FeatureResyncDone: this proxy closes its reconnect resync burst with an
	// EventResyncDone event. Cf. SendEvents, which emits it, and Hub.Resync, which is
	// what the controller asks instead of guessing.
	FeatureResyncDone = "resync-done"

	// KeepAlive / WriteTimeout: detect NAT half-connections without flooding
	// the link (spec: ping 20 s, timeout 60 s).
	KeepAlive    = 20 * time.Second
	WriteTimeout = 10 * time.Second

	// DefaultDialTimeout bounds TCP open on the proxy side.
	DefaultDialTimeout = 8 * time.Second

	// maxPreamble bounds the JSON preamble line: a peer that never sends a line
	// ending must not grow a buffer forever.
	maxPreamble = 8 << 10
)

// HasFeature reports whether a capability list announces one capability.
//
// Tolerant on FORM — spaces, empty items, case — because the list crosses an HTTP
// header written by a binary the controller does not control, and strict on
// CONTENT: only an exact token counts, so "resync-done-maybe" is not "resync-done".
// An empty list answers false for everything, which is the whole compatibility
// contract in one line.
func HasFeature(list, feature string) bool {
	var item string
	for _, item = range strings.Split(list, ",") {
		if strings.EqualFold(strings.TrimSpace(item), feature) {
			return true
		}
	}
	return false
}

// DialRequest is the preamble written by the controller on a new stream.
type DialRequest struct {
	Op        string `json:"op"` // dial
	Host      string `json:"host"`
	Port      int    `json:"port"`
	TimeoutMS int    `json:"timeout_ms,omitempty"`
}

// DialReply is the proxy response, before any useful byte.
type DialReply struct {
	Op  string `json:"op"`            // dial_ok | dial_err
	Err string `json:"err,omitempty"` // set if dial_err
}

// Status describes an established tunnel, for inventory.
type Status struct {
	ProxyID   string    `json:"proxy_id"`
	Version   string    `json:"version"`
	Since     time.Time `json:"since"`
	Streams   int       `json:"streams"`
	RemoteRTT string    `json:"rtt,omitempty"`
}

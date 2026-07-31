// Package tunnel carries the controller ↔ proxy link: an outbound WebSocket,
// multiplexed with yamux, in which the controller opens TCP connections toward
// devices on the proxy's network.
//
// This is phase 0bis of the controller-proxy protocol (docs/en/01-Architecture/Proxy.md). The
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

import "time"

const (
	// Path is the tunnel route on the controller. The proxy connects there via
	// WSS; authentication happens via headers BEFORE the upgrade, with the same
	// (proxy_id, secret) pair as the heartbeat.
	Path = "/api/proxy/tunnel"

	HeaderProxyID = "X-Meshcraft-Proxy-Id"
	HeaderSecret  = "X-Meshcraft-Proxy-Secret"
	HeaderVersion = "X-Meshcraft-Proxy-Version"

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

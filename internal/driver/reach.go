package driver

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"
)

// Reachability preflight: does a proxy actually reach a network?
//
// This is the check required by the management-addressing switch
// (docs/01-Architecture/Adressage de management §5): sending a device into a
// subnet nobody can reach means losing it.
//
// The subtle point, and it's the entire reason this code exists: **a closed port
// is good news**. A `connection refused` means the packet arrived and the host
// answered with an RST — so the route exists. A `timeout`, on the other hand,
// does not distinguish "no route" from "silently filtered." Conflating the two
// would fail a perfectly valid preflight check.

// ReachOutcome: what a connection attempt learned.
type ReachOutcome string

const (
	// ReachOpen: something is listening. The host exists.
	ReachOpen ReachOutcome = "open"
	// ReachRefused: the host answered with an RST. It exists, and the route
	// exists.
	ReachRefused ReachOutcome = "refused"
	// ReachUnreachable: ICMP unreachable — a network device answered that
	// nobody is there. The route exists, the host doesn't.
	ReachUnreachable ReachOutcome = "unreachable"
	// ReachTimeout: silence. No route, or silent filtering — undecidable.
	ReachTimeout ReachOutcome = "timeout"
)

// ReachResult describes the outcome of a preflight check against a host.
//
// TWO distinct questions, which must not be conflated — and this code fell into
// that exact trap before being fixed:
//
//	Reachable   : the host ITSELF answered (open or refused).
//	RouteExists : a device along the path answered, only to say the host is
//	              absent (ICMP unreachable). The network is routed; the host is
//	              not. Useful to validate a subnet, never to conclude that an
//	              address answers.
type ReachResult struct {
	Host string `json:"host"`
	// Probes: the per-port detail, so the diagnosis is legible.
	Probes      map[string]ReachOutcome `json:"probes"`
	Reachable   bool                    `json:"reachable"`
	RouteExists bool                    `json:"route_exists"`
	// Verdict is the sentence to display: it must be enough on its own to
	// decide.
	Verdict string `json:"verdict"`
}

// ReachPorts: common ports on a gateway or network device. None of them is
// expected to be open — only the NATURE of the failure matters.
var ReachPorts = []int{443, 80, 22, 179}

// CheckReach tests the reachability of a host from the supplied dialer (so from
// the proxy, when it's the one behind a tunnel).
func CheckReach(ctx context.Context, host string, d Dialer, timeout time.Duration) ReachResult {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	var res = ReachResult{Host: host, Probes: map[string]ReachOutcome{}}
	var port int
	for _, port = range ReachPorts {
		var outcome = probeReach(ctx, host, port, d, timeout)
		res.Probes[strconv.Itoa(port)] = outcome
		switch outcome {
		case ReachOpen, ReachRefused:
			// The host spoke: presence and route both established.
			res.Reachable = true
			res.RouteExists = true
		case ReachUnreachable:
			// A router spoke IN PLACE of the host: the route exists, the host
			// doesn't.
			res.RouteExists = true
		}
		// One answer from the host is enough; an ICMP unreachable is not — keep
		// trying the other ports in case only this one is filtered.
		if res.Reachable {
			break
		}
	}
	switch {
	case res.Reachable:
		res.Verdict = "host reachable from this proxy: it answered, the route exists"
	case res.RouteExists:
		res.Verdict = "a device along the path answered that this host is absent " +
			"(ICMP unreachable): the network is routed from this proxy, but nothing " +
			"answers at this address"
	default:
		res.Verdict = "no response: either there is no route from this proxy, " +
			"or traffic is being silently filtered. Cannot conclude that the network " +
			"is reachable."
	}
	return res
}

func probeReach(ctx context.Context, host string, port int, d Dialer, timeout time.Duration) ReachOutcome {
	var dctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()

	var conn net.Conn
	var err error
	var addr = net.JoinHostPort(host, strconv.Itoa(port))
	if d != nil {
		conn, err = d.DialContext(dctx, "tcp", addr)
	} else {
		var dialer = net.Dialer{Timeout: timeout}
		conn, err = dialer.DialContext(dctx, "tcp", addr)
	}
	if err == nil {
		_ = conn.Close()
		return ReachOpen
	}
	return classify(err)
}

// classify translates a connection error into a verdict. The tunnel surfaces the
// proxy's message as plain text: string patterns are therefore recognized too,
// not just typed errors.
func classify(err error) ReachOutcome {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ReachRefused
	}
	if errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return ReachUnreachable
	}
	var msg = err.Error()
	switch {
	case contains(msg, "refused"):
		return ReachRefused
	case contains(msg, "unreachable"):
		return ReachUnreachable
	}
	return ReachTimeout
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	var n = len(needle)
	var i int
	for i = 0; i+n <= len(haystack); i++ {
		if haystack[i:i+n] == needle {
			return i
		}
	}
	return -1
}

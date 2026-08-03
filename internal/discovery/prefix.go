package discovery

import (
	"errors"
	"fmt"
	"net/netip"
)

// DECLARING a range and being able to SCAN it are two distinct questions.
//
// We accept declaring whatever an operator actually has on their network — a
// /16 IPv4, a /64 IPv6 — because that's the truth of their addressing. What
// is bounded is ENUMERATION: it has a network cost and, in IPv6, it doesn't
// arithmetically make sense. The discovery strategy is therefore chosen at
// scan time, not at input time.

// MaxHosts bounds the enumeration of an IPv4 range: a /16 (65,536 addresses)
// is the largest we walk. Beyond that, a scan takes hours, and it's done in
// declared chunks instead.
const MaxHosts = 65536

// MinIPv6Bits: beyond this (a prefix wider than a /112), we don't enumerate.
//
// This isn't an implementation limit but an arithmetic reality: a /64
// contains 1.8·10^19 addresses. Such a prefix remains DECLARABLE — it's the
// site's real subnet — but its discovery will go through neighbor discovery
// (NDP, all-nodes multicast) rather than enumeration. As long as the proxy
// doesn't report back its neighbor table, a scan on such a prefix is refused
// with its reason, which is better than launching it and finding nothing.
const MinIPv6Bits = 112

var ErrPrefixTooLarge = errors.New("range too large")

// ErrNotEnumerable: the range is valid and declarable, but we don't walk it
// address by address.
var ErrNotEnumerable = errors.New("range not enumerable")

// ValidPrefix checks that a range is DECLARABLE: correct format, nothing more.
// This is what input validation checks.
func ValidPrefix(cidr string) error {
	var _, err = NormalizePrefix(cidr)
	return err
}

// Enumerable says whether the range can be walked address by address, and if
// not, why. The caller uses this to refuse a scan with a readable reason,
// rather than launching the impossible.
func Enumerable(cidr string) error {
	var p, err = netip.ParsePrefix(cidr)
	if err != nil {
		// A single address is always enumerable.
		var _, addrErr = netip.ParseAddr(cidr)
		return addrErr
	}
	p = p.Masked()
	if p.Addr().Is6() && !p.Addr().Is4In6() && p.Bits() < MinIPv6Bits {
		return fmt.Errorf(
			"%w: %s. In IPv6, enumeration doesn't make sense (a /64 = 1.8·10^19 "+
				"addresses). Discovery of this prefix will go through the proxy's "+
				"neighbor table (NDP), which isn't shipped yet: in the meantime, "+
				"declare a narrower prefix (/112 or more specific) or add the "+
				"device by its address",
			ErrNotEnumerable, cidr)
	}
	var bits = p.Addr().BitLen() - p.Bits()
	if bits > 16 || 1<<uint(bits) > MaxHosts {
		return fmt.Errorf("%w: %s would produce more than %d addresses; split it into chunks",
			ErrPrefixTooLarge, cidr, MaxHosts)
	}
	return nil
}

// ExpandPrefix enumerates the addresses to probe for a CIDR range.
//
// In IPv4, the network address and the broadcast address are excluded
// whenever the mask is wider than a /31.
func ExpandPrefix(cidr string) ([]string, error) {
	var p, err = netip.ParsePrefix(cidr)
	if err != nil {
		// A single address is accepted: that's the manual-add case.
		var addr netip.Addr
		addr, err = netip.ParseAddr(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid prefix: %s", cidr)
		}
		return []string{addr.String()}, nil
	}
	p = p.Masked()

	if p.Addr().Is6() && !p.Addr().Is4In6() {
		if p.Bits() < MinIPv6Bits {
			return nil, fmt.Errorf(
				"%w: %s. In IPv6, enumeration doesn't make sense beyond a /%d "+
					"(a /64 = 1.8·10^19 addresses). Declare a more specific prefix "+
					"or add the device by its address",
				ErrPrefixTooLarge, cidr, MinIPv6Bits)
		}
	}

	var bits = p.Addr().BitLen() - p.Bits()
	if bits > 16 || 1<<uint(bits) > MaxHosts {
		return nil, fmt.Errorf("%w: %s would produce more than %d addresses",
			ErrPrefixTooLarge, cidr, MaxHosts)
	}

	var total = 1 << uint(bits)
	var skipEdges = p.Addr().Is4() && total > 2
	var out = make([]string, 0, total)
	var cur = p.Addr()
	var i int
	for i = 0; i < total; i++ {
		if !(skipEdges && (i == 0 || i == total-1)) {
			out = append(out, cur.String())
		}
		cur = cur.Next()
		if !cur.IsValid() {
			break
		}
	}
	return out, nil
}

// NormalizePrefix validates a user input and returns it in canonical form
// (masked network), to avoid duplicates like 10.0.0.5/24 vs 10.0.0.0/24.
func NormalizePrefix(cidr string) (string, error) {
	var p, err = netip.ParsePrefix(cidr)
	if err == nil {
		return p.Masked().String(), nil
	}
	var addr netip.Addr
	addr, err = netip.ParseAddr(cidr)
	if err != nil {
		return "", fmt.Errorf("invalid prefix or address: %s", cidr)
	}
	return addr.String(), nil
}

package mdpv1

// Address forms, and the one conversion every CLI platform needs.
//
// meshcraft carries a management address as a PREFIX — "192.0.2.36/24" — which
// is what [MgmtAddrIntent.Address] holds and what the UI shows. Most switch
// CLIs do not accept that: IOS and the BDCOM-style FS ranges both want
// "ip address 192.0.2.36 255.255.255.0", two arguments, dotted mask.
//
// The conversion lives here rather than in each connector because getting it
// wrong is silent: a mask off by one bit still configures, still answers ping
// from the same subnet, and only fails when someone routes to it.

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// SplitAddrMask turns a management address into the two arguments a CLI wants.
//
// Accepts the prefix form meshcraft uses ("192.0.2.36/24") and, tolerantly,
// the already-split form ("192.0.2.36 255.255.255.0") so a caller that has
// already done the work is not punished for it. A bare address with no prefix
// is REFUSED rather than guessed: assuming /24 is how a /22 network gets a
// switch that can only see a quarter of itself.
func SplitAddrMask(addr string) (ip string, mask string, bits int, err error) {
	var s = strings.TrimSpace(addr)
	if s == "" {
		return "", "", 0, fmt.Errorf("no address given")
	}

	// Already split: "192.0.2.36 255.255.255.0".
	var fields = strings.Fields(s)
	if len(fields) == 2 {
		var parsed = net.ParseIP(fields[0])
		var m = net.ParseIP(fields[1])
		if parsed == nil || parsed.To4() == nil {
			return "", "", 0, fmt.Errorf("address %q is not an IPv4 address", fields[0])
		}
		if m == nil || m.To4() == nil {
			return "", "", 0, fmt.Errorf("mask %q is not a dotted IPv4 mask", fields[1])
		}
		var ones, total = net.IPMask(m.To4()).Size()
		if total != 32 {
			return "", "", 0, fmt.Errorf("mask %q is not contiguous", fields[1])
		}
		return parsed.To4().String(), m.To4().String(), ones, nil
	}
	if len(fields) != 1 {
		return "", "", 0, fmt.Errorf("address %q is not \"ip/prefix\" nor \"ip mask\"", addr)
	}

	var slash = strings.IndexByte(s, '/')
	if slash < 0 {
		return "", "", 0, fmt.Errorf("address %q carries no prefix length: refusing to "+
			"assume one, because a wrong mask configures cleanly and only fails later", addr)
	}
	var parsed = net.ParseIP(strings.TrimSpace(s[:slash]))
	if parsed == nil || parsed.To4() == nil {
		return "", "", 0, fmt.Errorf("address %q is not an IPv4 address", s[:slash])
	}
	var n, convErr = strconv.Atoi(strings.TrimSpace(s[slash+1:]))
	if convErr != nil || n < 0 || n > 32 {
		return "", "", 0, fmt.Errorf("prefix length %q is not between 0 and 32", s[slash+1:])
	}
	var m = net.CIDRMask(n, 32)
	return parsed.To4().String(), net.IP(m).String(), n, nil
}

// JoinAddrMask is the reverse: what a connector reports back to meshcraft
// after reading "ip address 192.0.2.36 255.255.255.0" off a device.
func JoinAddrMask(ip, mask string) (string, error) {
	var parsed = net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil || parsed.To4() == nil {
		return "", fmt.Errorf("address %q is not an IPv4 address", ip)
	}
	var m = net.ParseIP(strings.TrimSpace(mask))
	if m == nil || m.To4() == nil {
		return "", fmt.Errorf("mask %q is not a dotted IPv4 mask", mask)
	}
	var ones, total = net.IPMask(m.To4()).Size()
	if total != 32 {
		return "", fmt.Errorf("mask %q is not contiguous", mask)
	}
	return fmt.Sprintf("%s/%d", parsed.To4().String(), ones), nil
}

// Package netscope decides IN WHICH SPACE an IP address makes sense.
//
// The problem it solves in one sentence: `192.168.1.10` identifies nobody. RFC
// 1918 space overlaps, and two proxies may scan the same range without looking at
// the same network. Symmetrically, two proxies may have two paths to ONE network,
// and then what they report must merge rather than counting the same switch twice.
//
// meshcraft's answer is the ADDRESS DOMAIN: the space inside which an address
// designates one machine and only one. It is DECLARED, never guessed — see
// DomainOf for why. Everything that elsewhere depended on the (proxy, IP) pair
// now depends on the (domain, IP) pair.
//
// Everything here is pure: these decisions govern candidate deduplication and
// watch-state assignment. Being able to test them without a database or device is
// not a luxury — getting the side wrong costs either a duplicate in the adoption
// queue or, far worse, two distinct devices merged into one.
package netscope

import (
	"net/netip"
	"sort"
)

// Prefix is a range declared on a proxy, and the address domain the operator
// attached it to.
type Prefix struct {
	Prefix   string
	DomainID string
	// Enabled concerns SCANNING only, not identity. A disabled range still
	// describes a real network: it therefore still answers "in which space does
	// this address live?". Without that, disabling a scan would move known
	// candidates to the proxy default domain, change their merge key, and
	// duplicate them on the next pass.
	Enabled bool
}

// DomainOf returns the address domain of an address observed by a proxy.
//
// The MOST SPECIFIC range wins, like a routing table: an operator who declares
// 10.0.0.0/16 then detaches 10.0.5.0/24 to another domain expresses an exception,
// and an exception must beat the general rule.
//
// `home` is the proxy's own domain, created with it. It is the fallback for
// anything outside declared ranges: manual probe of an arbitrary address, a
// candidate from the serial console. Without this fallback, those observations
// would have no domain and fall into a common space where all IPs blur together.
//
// Why NOT guess the domain from what proxies observe (gateway MAC, neighbor
// fingerprint): deduction would sometimes be wrong, and wrong in the bad direction.
// Merging two distinct networks by mistake merges two devices into one record, and
// that loss cannot be undone — whereas a duplicate is fixed with one click. We
// therefore require a declaration, and the default is always "distinct networks".
func DomainOf(prefixes []Prefix, home, ip string) string {
	var addr, err = netip.ParseAddr(ip)
	if err != nil {
		return home
	}
	var bestBits = -1
	var best = home
	var p Prefix
	for _, p = range prefixes {
		if p.DomainID == "" {
			continue
		}
		var pfx, pErr = netip.ParsePrefix(p.Prefix)
		if pErr != nil {
			// A range declared as a single address: it applies only to itself,
			// and that is the most specific match possible.
			var single, sErr = netip.ParseAddr(p.Prefix)
			if sErr == nil && single == addr && addr.BitLen() > bestBits {
				bestBits, best = addr.BitLen(), p.DomainID
			}
			continue
		}
		if !pfx.Contains(addr) {
			continue
		}
		if pfx.Bits() > bestBits {
			bestBits, best = pfx.Bits(), p.DomainID
		}
	}
	return best
}

// IdentityKey builds the key under which two observations are the SAME device.
//
// Order is not negotiable:
//
//   - serial number is a vendor identity, valid everywhere. Two proxies reading
//     the same serial are looking at the same device, regardless of the addresses
//     they used to reach it;
//   - MAC is globally unique by construction, and the only identifier most silent
//     devices publish;
//   - otherwise only the address remains — and an address alone means nothing. It
//     is therefore qualified by its address domain.
//
// The fallback is the sensitive point. With (proxy, IP), two proxies on the SAME
// network produced two candidates for one anonymous device, and nothing could ever
// reunite them. With (domain, IP), the operator who declares that two proxies see
// the same network gets the merge — and if they declare nothing, each proxy keeps
// its own domain and behavior stays exactly as before.
func IdentityKey(serial, mac, domainID, ip string) string {
	if serial != "" {
		return serial
	}
	if mac != "" {
		return mac
	}
	if domainID == "" {
		// No known domain: rather than merge this observation with every other
		// sharing the address, keep it separate.
		domainID = "sans-domaine"
	}
	return domainID + "|" + ip
}

// --- Watch: who may answer for which address ---

// WatchCandidate is a device this proxy's site coverage allows it to watch.
// Empty DomainID = unknown domain, typically a manually entered device rather
// than one adopted from discovery.
type WatchCandidate struct {
	DeviceID string
	// SiteID travels with the candidate so REJECTION can be logged in the
	// affected site. A watch gap that does not appear in the site log is seen by
	// nobody.
	SiteID   string
	Name     string
	IP       string
	DomainID string
}

// Rejected is a device excluded from this proxy's watch, with the reason. The
// reason is essential: a device that stops being watched without explanation
// looks like a meshcraft failure.
type Rejected struct {
	DeviceID string
	SiteID   string
	Name     string
	IP       string
	Reason   string
}

// SelectWatchTargets excludes targets this proxy cannot identify.
//
// THE PROBLEM. The proxy pings by ADDRESS: its state table is indexed by IP.
// Sending two different devices to the same address therefore does not produce
// two watches, but one — the second silently overwrites the first. One is never
// watched again, and the other receives the state of whatever machine actually
// responds at that address on THIS network, which may be an entirely different
// device. The result is a green indicator on a powered-off switch, which is worse
// than no indicator at all.
//
// This happens as soon as an instance proxy watches two sites that reuse the same
// private addressing plan — that is, all the time, once a client has two sites on
// 192.168.1.0/24.
//
// ARBITRATION. When several devices dispute an address, the winner is the one
// whose address domain is declared ON THIS PROXY: this proxy has a range in that
// network, so it is legitimate to answer there. If none qualify, or several do, we
// do not guess: all are excluded with their reason. Saying nothing beats saying
// false — an unwatched device is visible in the UI; a false state is not.
//
// `proxyDomains` is the set of domains this proxy declares: its own, plus those
// of its scan ranges.
func SelectWatchTargets(cands []WatchCandidate, proxyDomains map[string]bool) (kept []WatchCandidate, rejected []Rejected) {
	kept = []WatchCandidate{}
	rejected = []Rejected{}

	// Group by address, preserving arrival order: output must not reorder from
	// call to call, or the config pushed to the proxy would "change" every time
	// although nothing moved.
	var byIP = map[string][]WatchCandidate{}
	var order = []string{}
	var c WatchCandidate
	for _, c = range cands {
		if c.IP == "" {
			continue
		}
		var _, seen = byIP[c.IP]
		if !seen {
			order = append(order, c.IP)
		}
		byIP[c.IP] = append(byIP[c.IP], c)
	}

	var ip string
	for _, ip = range order {
		var group = byIP[ip]
		if len(group) == 1 {
			kept = append(kept, group[0])
			continue
		}
		// Several devices at the same address for this proxy.
		var legit []WatchCandidate
		for _, c = range group {
			if c.DomainID != "" && proxyDomains[c.DomainID] {
				legit = append(legit, c)
			}
		}
		if len(legit) == 1 {
			kept = append(kept, legit[0])
			for _, c = range group {
				if c.DeviceID == legit[0].DeviceID {
					continue
				}
				rejected = append(rejected, Rejected{
					DeviceID: c.DeviceID, SiteID: c.SiteID, Name: c.Name, IP: c.IP,
					Reason: "address " + ip + " is also used by \"" + legit[0].Name +
						"\", whose address domain is declared on this proxy: " +
						"that device will be watched there",
				})
			}
			continue
		}
		var names = groupNames(group)
		for _, c = range group {
			rejected = append(rejected, Rejected{
				DeviceID: c.DeviceID, SiteID: c.SiteID, Name: c.Name, IP: c.IP,
				Reason: "multiple devices use address " + ip + " for this proxy (" +
					names + ") and nothing indicates which one responds there: none " +
					"are watched by this proxy. Attach their prefixes to distinct " +
					"address domains.",
			})
		}
	}
	return kept, rejected
}

// groupNames renders conflict group names, sorted so the message does not change
// between reads.
func groupNames(group []WatchCandidate) string {
	var names = make([]string, 0, len(group))
	var c WatchCandidate
	for _, c = range group {
		var n = c.Name
		if n == "" {
			n = c.DeviceID
		}
		names = append(names, n)
	}
	sort.Strings(names)
	var out string
	var i int
	for i = range names {
		if i > 0 {
			out += ", "
		}
		out += "\"" + names[i] + "\""
	}
	return out
}

package s3400

// Reads: the VLAN/port state and the management addressing.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// ReadState reports the switch as it IS: VLANs, per-port membership, and the
// operational state of every socket.
//
// Three reads, and the reason there are three:
//
//   - "show running-config" gives the INTENT (mode, pvid, trunk allowed
//     list). A factory port carries no switchport line at all, so the config
//     alone cannot say which VLAN an untouched port is in;
//   - "show vlan" gives the OBSERVED membership, including the ports the
//     configuration is silent about, and the VLAN NAMES;
//   - the interface list gives link state, without which every port would
//     read as unknown.
func (c *Client) ReadState(ctx context.Context) (State, error) {
	var st = State{Ports: map[string]PortState{}}

	// A TRUNCATED CONFIG IS REFUSED, NOT PARSED. The old test was "empty", and
	// empty is the one truncation this parser cannot survive being given; every
	// other one it survives by inventing a switch.
	//
	// ParseRunningConfig has a trailing flushPort(), so a read cut inside an
	// interface stanza does not fail — it emits that half-read port with the
	// DEFAULT PVID and stops, and every socket after it simply does not exist as
	// far as this function is concerned. There is nothing in the text to notice:
	// configuration is valid to its last byte whether or not more was coming.
	//
	// On 2026-08-11 that is exactly what poefs3400lab returned — "show
	// running-config" died some eight seconds in, inside GE23 — and ReadState
	// reported 23 of 52 ports with a nil error. The config poll stored it with
	// `observed_at = now()`, drift read 30 differences (29 sockets "unknown to
	// the device", GE23 back at PVID 1) and the import button, which replaces
	// `ports` wholesale, was one click from deleting the intent of 29 ports
	// including all four 10G uplink trunks.
	//
	// So the refusal is keyed on [isTruncated] — "this output is a prefix" —
	// and NOT on the presence of an error, because a CLI error arrives on a
	// COMPLETE read and this connector has always been entitled to parse around
	// one. The condition is a strict superset of the old one: everything that was
	// refused before is still refused.
	var cfgOut, cfgErr = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if cfgErr != nil && (strings.TrimSpace(cfgOut) == "" || isTruncated(cfgErr)) {
		c.step(false, "show running-config failed", firstLine(cfgErr.Error()))
		return st, fmt.Errorf("show running-config on %s: %v — the port inventory this read "+
			"would report is a PREFIX of the device's, and a partial inventory persisted as an "+
			"observation reads as \"these ports vanished\"", c.host, cfgErr)
	}
	// The cage numbering rides this read too, and it is the EARLIEST of the
	// three chances this function has: the configuration names the cages in
	// their "interface TGigaEthernet0/N" stanzas (ADR-0018). Noted before
	// anything is parsed, because the two write paths that come through here —
	// ApplyPorts and ApplyPortAdmin — name a cage further down.
	c.noteCageNumbering(cfgOut)
	var cfg = ParseRunningConfig(cfgOut)

	// The device's OWN name rides this read and costs nothing more. It is in the
	// configuration and in no cheaper place on this range, and ReadIdentity is
	// forbidden to fetch it itself — ninety seconds of pager work has no business
	// on the cheap identity path (cf. identityread.go). This is the ONLY thing
	// that makes an S3400 observable by the hostname reconciliation: it serves no
	// LLDP system name. A factory-named switch carries no `hostname` line, which
	// records the empty string and reads as "nothing observed".
	c.noteSysName(cfg.Hostname)

	// "show vlan" is DELIBERATELY NOT given the refusal above, and the reason is
	// that there is no safe version of it to give.
	//
	// This read is already allowed to fail outright — the else-branch below has
	// always fallen back to the configuration alone — so tightening it cannot
	// return an error without contradicting that. And DISCARDING a partial table
	// is not the conservative choice it looks like: this table supplies exactly
	// what the configuration is silent about (a factory port's VLAN, and the
	// tagged set of a trunk carrying "everything" with no vlan-allowed line), so
	// throwing away the half that did arrive trades a wrong answer for some ports
	// against a wrong answer for all of them. Neither is a fix.
	//
	// What IS owed here is the evidence, so the next occurrence is diagnosable
	// instead of being reconstructed from drift a day later.
	var vlanOut, vlanErr = c.cli.runPaged(ctx, "show vlan", 60*time.Second)
	var table = VlanTable{Members: map[string][]int{}}
	if vlanErr != nil && strings.TrimSpace(vlanOut) == "" {
		c.step(false, "show vlan failed — falling back to the configuration alone",
			firstLine(vlanErr.Error()))
	} else {
		if isTruncated(vlanErr) {
			c.step(false, "show vlan TRUNCATED — membership read is partial",
				firstLine(vlanErr.Error()))
			st.Notes = append(st.Notes,
				"\"show vlan\" did not run to the prompt: the VLAN membership below covers only "+
					"the ports the read reached, and any port after the cut is reported from the "+
					"configuration alone")
		}
		// "show vlan" lists the cages by the device's own name as well — a
		// second, independent chance at the numbering, which matters because
		// the cages sit at the END of the faceplate and a truncated read is a
		// live hazard on this range.
		c.noteCageNumbering(vlanOut)
		table = parseShowVlan(vlanOut)
	}

	// VLANs: "show vlan" is the reference (it lists every VLAN that exists,
	// with its name); the configuration fills in a name the table truncated.
	var seen = map[int]bool{}
	var v driver.VLAN
	for _, v = range table.VLANs {
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		var named, namedOK = cfg.VlanNames[v.ID]
		if namedOK && named != "" {
			v.Name = named
		}
		st.VLANs = append(st.VLANs, v)
	}
	var id int
	for id = range cfg.VlanNames {
		if !seen[id] {
			seen[id] = true
			st.VLANs = append(st.VLANs, driver.VLAN{ID: id, Name: cfg.VlanNames[id]})
		}
	}
	sort.Slice(st.VLANs, func(i, j int) bool { return st.VLANs[i].ID < st.VLANs[j].ID })

	// Ports: configuration intent, completed by observed membership.
	var name string
	var ps PortState
	for name, ps = range cfg.Ports {
		st.Ports[name] = withObservedMembership(ps, table.Members[name])
	}
	// A socket the configuration never mentioned still exists.
	for name = range table.Members {
		var _, known = st.Ports[name]
		if !known {
			st.Ports[name] = withObservedMembership(PortState{Mode: "access", PVID: 1}, table.Members[name])
		}
	}

	st.MgmtVLAN = cfg.MgmtVLAN
	st.Links = c.readLinks(ctx)
	c.lldpOn = cfg.LLDPRun
	c.lldpKnown = true
	if !cfg.LLDPRun {
		st.Notes = append(st.Notes,
			"LLDP is OFF on this device (\"lldp run\" absent): no neighbour is advertised "+
				"or learned, and the topology view stays empty until it is enabled")
	}
	return st, nil
}

// withObservedMembership reconciles what the configuration says with what the
// VLAN table shows.
//
// On a trunk the configuration is often silent — a trunk with no
// "vlan-allowed" line carries everything (captured: tg0/1-4 appear in every
// VLAN of "show vlan" with only "switchport mode trunk" in the config).
// The observed table is therefore what fills Tagged, minus the native VLAN.
func withObservedMembership(ps PortState, member []int) PortState {
	if ps.Mode == "" {
		ps.Mode = "access"
	}
	if ps.Mode == "access" {
		if ps.PVID == 0 {
			ps.PVID = 1
			if len(member) == 1 {
				ps.PVID = member[0]
			}
		}
		ps.Tagged = nil
		return ps
	}
	if ps.PVID == 0 {
		ps.PVID = 1
	}
	if len(ps.Tagged) == 0 {
		var tagged []int
		var id int
		for _, id = range member {
			tagged = append(tagged, id)
		}
		sort.Ints(tagged)
		ps.Tagged = tagged
	}
	// The native VLAN is REMOVED from the tagged set, whichever source filled
	// it — and this is the mirror of the write, which ADDS the PVID to the
	// allowed list because the device requires its native to be allowed.
	//
	// Without it the two sides disagree by exactly one VLAN, for ever:
	// meshcraft's intent never lists a port's own native among its tagged
	// VLANs (recon.ResolveTaggedAll skips it explicitly), so an
	// exactly-applied trunk reported "1,42,98,99,100" against an intent of
	// "42,98,99,100" and the discrepancy could not resolve — meshcraft only
	// re-applies after a configuration change. Observed 2026-08-03 on the lab
	// 48T4SP: XG1-4 off by VLAN 1, GE45-48 off by VLAN 100, each port's own
	// PVID. Same rule, same reason, as cisco/ios/parse.go.
	ps.Tagged = removeInt(ps.Tagged, ps.PVID)
	return ps
}

// removeInt drops every occurrence of v, keeping order.
func removeInt(xs []int, v int) []int {
	var out []int
	var x int
	for _, x = range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// readLinks gets the operational state of every socket.
//
// The filtered form is tried first because it returns 54 lines instead of
// some 2000. It is NOT trusted blindly: "| include" on this firmware is
// erratic — "show running-config | include snmp" returns nothing on a
// configuration that demonstrably contains "snmp-server community"
// (captured 2026-08-03, while "| include community" on the same config
// returns the line). So an empty filtered read falls back to the full one
// rather than reporting a switch with no ports.
func (c *Client) readLinks(ctx context.Context) map[string]driver.LinkState {
	var out, err = c.cli.runPaged(ctx, "show interface | include protocol", 90*time.Second)
	if err == nil {
		// This is the read that NAMES the cages — "TGigaEthernet0/51 is up" —
		// and so the one the session learns its numbering from (ADR-0018).
		c.noteCageNumbering(out)
		var links = ParseLinkStates(out)
		if len(links) > 0 {
			return links
		}
	}
	out, err = c.cli.runPaged(ctx, "show interface", 180*time.Second)
	if err != nil && strings.TrimSpace(out) == "" {
		c.step(false, "no interface list", firstLine(err.Error()))
		return nil
	}
	// A TRUNCATED interface list is kept, not dropped, and that is a judgement
	// rather than an oversight. This map is read two ways and both fail SAFE when
	// it is short: a missing port reads as "no link" on the panel, and
	// [Client.ApplyPortAdmin] SKIPS every port it does not find here rather than
	// acting on it. Returning nil instead would empty the map, and an empty map is
	// the one value that path treats as fatal ("nothing is shut") — i.e.
	// dropping a partial read would fail applies CLOSED on a signal that costs
	// nothing to survive. So the partial map stands, and the truncation is named.
	if isTruncated(err) {
		c.step(false, "show interface TRUNCATED — link state is partial",
			firstLine(err.Error()))
	}
	// Even a TRUNCATED list is worth observing: the numbering is established by
	// the first cage named, and a prefix that reached one carries the fact.
	c.noteCageNumbering(out)
	return ParseLinkStates(out)
}

// ReadMgmtAddressing reports how this switch gets its management address.
//
// "show ip interface brief" names the METHOD (manual / dhcp) per interface,
// which is the one thing the running-config cannot show once an address has
// been leased.
func (c *Client) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	var st driver.MgmtAddrState

	var briefOut, briefErr = c.cli.runPaged(ctx, "show ip interface brief", 60*time.Second)
	var ifaces []L3Iface
	if briefErr == nil || strings.TrimSpace(briefOut) != "" {
		ifaces = parseIPInterfaceBrief(briefOut)
	}

	var cfgOut, cfgErr = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if cfgErr != nil && strings.TrimSpace(cfgOut) == "" {
		return st, fmt.Errorf("show running-config on %s: %v", c.host, cfgErr)
	}
	var cfg = ParseRunningConfig(cfgOut)

	st.Gateway = cfg.Gateway
	var ip, mask string
	var serr error
	var chosen = -1
	var i int
	for i = range ifaces {
		if ifaces[i].IP != "" {
			chosen = i
			break
		}
	}
	if chosen >= 0 {
		st.Interface = fmt.Sprintf("vlan %d", ifaces[chosen].VlanID)
		st.Mode = "static"
		if ifaces[chosen].Method == "dhcp" {
			st.Mode = "dhcp"
		}
		// Reported as a PREFIX ("192.0.2.3/16"), which is the form meshcraft
		// carries and the UI shows — the dotted mask is this CLI's business and
		// stops at the edge of the connector (cf. driver.SplitAddrMask on the
		// write side).
		st.Address = ifaces[chosen].IP
		// Reuses the function's own mask: this branch and the "else if" below
		// are mutually exclusive, and mask is not read past the chain.
		mask = maskOf(cfg.MgmtAddr)
		if mask != "" && ifaces[chosen].VlanID == cfg.MgmtVLAN {
			var cidr, jerr = driver.JoinAddrMask(ifaces[chosen].IP, mask)
			if jerr == nil {
				st.Address = cidr
			}
		}
	} else if cfg.MgmtVLAN > 0 {
		st.Interface = fmt.Sprintf("vlan %d", cfg.MgmtVLAN)
		st.Address = cfg.MgmtAddr
		if ip, mask, _, serr = splitConfigAddr(cfg.MgmtAddr); serr == nil {
			var cidr, jerr = driver.JoinAddrMask(ip, mask)
			if jerr == nil {
				st.Address = cidr
			}
		}
		st.Mode = "static"
		if cfg.MgmtAddr == "dhcp" {
			st.Mode = "dhcp"
			st.Address = ""
		}
	}
	// DNS is deliberately not reported: no "ip name-server" form has been
	// observed on this platform, and an empty list read as "none configured"
	// would be a claim this connector cannot make.
	st.Notes = append(st.Notes,
		"DNS is not read on this platform: no name-server form has been captured, and "+
			"reporting an empty list would assert something unverified")
	return st, nil
}

// splitConfigAddr reads the "A.B.C.D M.M.M.M" pair the configuration holds.
func splitConfigAddr(addr string) (ip string, mask string, bits int, err error) {
	return driver.SplitAddrMask(addr)
}

func maskOf(addr string) string {
	var parts = strings.Fields(addr)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

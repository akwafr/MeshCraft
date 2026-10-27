package edgeswitch

// Reads: ports, VLANs, links, management addressing, spanning tree, LLDP.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// ReadState reports the switch as it IS.
//
// VLAN membership is NOT in "show vlan" on this platform — that command
// lists ids and names only — so the per-port view comes from "show
// running-config" (cf. parseRunningConfigPorts's own comment for why that
// command and not "show vlan port all", which this used to read: it never
// distinguished trunk from access and forced every port to read back
// "access, no tagged VLANs" regardless of what was actually configured — the
// exact shape of the false, permanent drift this was fixed to stop causing).
// When that read fails the VLANs and links are still reported rather than
// failing the whole state: a partial answer that says what it knows beats no
// answer at all.
func (c *Client) ReadState(ctx context.Context) (State, error) {
	var st = State{Ports: map[string]PortState{}}

	var portOut, portErr = c.cli.run(ctx, "show port all", showTimeout)
	if portErr != nil && strings.TrimSpace(portOut) == "" {
		c.step(false, "show port all failed", firstLine(portErr.Error()))
		return st, fmt.Errorf("show port all on %s: %v", c.host, portErr)
	}
	st.Links = LinkStates(portOut)

	// Link aggregates. Tolerated failure: nil reads as "not read".
	var lags, lagErr = c.readLAGs(ctx)
	if lagErr == nil {
		st.LAGs = lags
	} else {
		st.Notes = append(st.Notes, "aggregates not read: "+firstLine(lagErr.Error()))
	}

	var vlanOut, vlanErr = c.cli.run(ctx, "show vlan", showTimeout)
	if vlanErr == nil || strings.TrimSpace(vlanOut) != "" {
		st.VLANs = parseShowVlan(vlanOut)
		sort.Slice(st.VLANs, func(i, j int) bool { return st.VLANs[i].ID < st.VLANs[j].ID })
	}

	// Every socket exists even when the membership read fails.
	var name string
	for name = range st.Links {
		st.Ports[name] = PortState{Mode: "access", PVID: 1}
	}

	var runOut, runErr = c.cli.run(ctx, "show running-config", showTimeout)
	if runErr != nil && strings.TrimSpace(runOut) == "" {
		st.Notes = append(st.Notes,
			"per-port VLAN membership was not read (\"show running-config\" failed): the "+
				"ports below carry the platform default rather than an observation")
	} else {
		var fromConfig = parseRunningConfigPorts(runOut)
		var ps PortState
		var known bool
		for name, ps = range fromConfig {
			_, known = st.Ports[name]
			if known {
				st.Ports[name] = ps
			}
		}
	}

	var netOut, netErr = c.cli.run(ctx, "show network", cmdTimeout)
	if netErr == nil || strings.TrimSpace(netOut) != "" {
		st.MgmtVLAN = MgmtVLANOf(netOut)
	}
	return st, nil
}

// ReadMgmtAddressing reports how this switch gets its address.
func (c *Client) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	var out, err = c.cli.run(ctx, "show network", cmdTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		return driver.MgmtAddrState{}, fmt.Errorf("show network on %s: %v", c.host, err)
	}
	return parseShowNetwork(out), nil
}

// ReadSpanningTree reports the bridge.
//
// Two commands, one session. The summary carries the admin mode, the
// protocol, the MST region and the per-port table — but NO bridge or root
// identifier on this platform (captured 2026-08-05, firmware 1.9.3): those
// live in the plain "show spanning-tree", so both are read. The second
// command failing degrades the answer, never voids it — mode and ports are
// still worth reporting when the identifiers are not.
func (c *Client) ReadSpanningTree(ctx context.Context) (driver.SpanningTreeState, error) {
	var out, err = c.cli.run(ctx, "show spanning-tree summary", cmdTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		return driver.SpanningTreeState{}, fmt.Errorf("show spanning-tree summary on %s: %v", c.host, err)
	}
	var st = parseSpanningTreeSummary(out)
	var det, detErr = c.cli.run(ctx, "show spanning-tree", cmdTimeout)
	if detErr != nil && strings.TrimSpace(det) == "" {
		st.Notes = append(st.Notes, "bridge identifiers not read (show spanning-tree): "+detErr.Error())
		return st, nil
	}
	mergeSpanningTreeDetail(det, &st)
	return st, nil
}

// ReadLLDPNeighbors returns the neighbour table.
func (c *Client) ReadLLDPNeighbors(ctx context.Context) ([]LLDPNeighbor, error) {
	var out, err = c.cli.run(ctx, "show lldp remote-device all", showTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("show lldp remote-device all on %s: %v", c.host, err)
	}
	return parseLLDPRemote(out), nil
}

// ReadFaceplate describes the front panel.
func (c *Client) ReadFaceplate(ctx context.Context) ([]map[string]any, error) {
	return c.portGroups(ctx)
}

// portGroups builds the port groups from what the device actually lists —
// never from the model string, because the LAG rows in slot 3 make a naive
// count wrong by six.
//
// SFP+ cages are split into their OWN group, from the "Auto D" signal
// parseShowPortAll reads (cf. its own comment). Before this, every faceplate
// port on this connector was declared one uniform "GE" / copper group, so
// the SFP+ cages drew and behaved as ordinary RJ45 jacks — the platform
// naming a media type meshcraft never saw.
//
// PoE capability is READ per port (cf. poe.go's own header: the lab unit's
// sockets 0/1-0/4 have no PSE hardware at all while 0/5-0/16 do — a model
// string cannot carry that, and neither can ONE group-level flag for the
// whole copper span). This function asks [Client.ReadPoE] and folds the
// per-port answer into the coarser group shape [PortGroups] takes, via
// [poeCopperSplit]: when capability follows a single boundary (a prefix
// without it, a suffix with it, or uniform either way — the only shapes
// observed so far), the copper span becomes two groups instead of one. A
// capability pattern that does NOT fit a single boundary is not force-fit
// into one: the group stays "poe": false rather than claim a shape nothing
// has proven, and a PoE read that fails outright (no hardware, or the
// session too busy) leaves the same conservative default.
func (c *Client) portGroups(ctx context.Context) ([]map[string]any, error) {
	var out, err = c.cli.run(ctx, "show port all", showTimeout)
	if err != nil && strings.TrimSpace(out) == "" {
		return nil, fmt.Errorf("show port all on %s: %v", c.host, err)
	}
	var maxPort int
	var sfp = map[int]bool{}
	var rows = parseShowPortAll(out)
	var i int
	for i = range rows {
		if rows[i].Name == "" {
			continue
		}
		var n, _, ok = splitStandard(rows[i].Name)
		if !ok {
			continue
		}
		if n > maxPort {
			maxPort = n
		}
		if rows[i].SFPCage {
			sfp[n] = true
		}
	}
	if maxPort == 0 {
		return nil, fmt.Errorf("no faceplate port listed on %s", c.host)
	}

	var sfpCount = sfpTailCount(maxPort, sfp)
	var copperCount = maxPort - sfpCount
	if sfpCount == 0 || sfpCount >= maxPort {
		copperCount = maxPort
	}

	var capable = map[int]bool{}
	var poe, poeErr = c.ReadPoE(ctx)
	if poeErr == nil {
		var p driver.PoEPortState
		for _, p = range poe.Ports {
			var n, _, ok = splitStandard(p.Port)
			if ok && p.Capable {
				capable[n] = true
			}
		}
	}
	var poeSplit, poeOK = poeCopperSplit(copperCount, capable)

	// row: 0 on every group below — cf. PortGroup.row on the frontend side
	// (web/src/api.ts). Copper, the PoE-capability split within it, and the
	// SFP+ cages are all in the SAME physical row on this hardware (sixteen
	// RJ45 then two SFP+, numbered continuously, cf. the package doc
	// comment) — the split into several `port_groups` here exists ONLY to
	// carry per-port metadata (media, PoE) that isn't uniform, never to
	// declare a separate faceplate block. Without this hint the equipment
	// view drew each group as its own labelled section — a copper SFP+
	// cage rendered as if it sat on a separate row of the switch, which it
	// does not.
	var out2 []map[string]any
	switch {
	case poeOK && poeSplit > 0 && poeSplit < copperCount:
		out2 = append(out2,
			map[string]any{"prefix": "GE", "from": 1, "count": poeSplit, "silk": "0/", "poe": false, "row": 0},
			map[string]any{"prefix": "GE", "from": poeSplit + 1, "count": copperCount - poeSplit,
				"silk": "0/", "poe": true, "row": 0})
	default:
		// Uniform (all capable, none capable) or a shape that doesn't fit a
		// single boundary: one group, conservatively "poe": poeOK && poeSplit==0
		// (every copper port capable) — false in every other case, including
		// a failed read.
		out2 = append(out2, map[string]any{
			"prefix": "GE", "from": 1, "count": copperCount, "silk": "0/",
			"poe": poeOK && poeSplit == 0 && copperCount > 0, "row": 0,
		})
	}
	if sfpCount > 0 && sfpCount < maxPort {
		// SFP+ cages: no PoE, and the "SFP+" label plus 10G speed is what
		// portrules.mediaOf reads to classify them (cf. its own comment — this
		// connector names every socket "GE", so the label carries the media,
		// not the prefix).
		out2 = append(out2, map[string]any{"prefix": "GE", "from": copperCount + 1, "count": sfpCount,
			"silk": "0/", "label": "SFP+", "speed": "10G", "poe": false, "row": 0})
	}
	return out2, nil
}

// poeCopperSplit looks for the ONE boundary shape observed so far: a prefix
// of ports with no PSE hardware, then a suffix that has it (or the uniform
// cases at either end). Returns the number of NOT-capable ports at the
// start (0 means every port is capable, copperCount means none is) and
// whether the pattern actually fits that shape — ok is false the moment a
// capable port precedes a non-capable one, which is not a shape this
// connector represents as a group split.
func poeCopperSplit(copperCount int, capable map[int]bool) (notCapableCount int, ok bool) {
	if copperCount <= 0 {
		return 0, false
	}
	var n int
	for n = 1; n <= copperCount; n++ {
		if capable[n] {
			break
		}
		notCapableCount++
	}
	for ; n <= copperCount; n++ {
		if !capable[n] {
			return 0, false // a capable port precedes a non-capable one: not a clean boundary
		}
	}
	return notCapableCount, true
}

// sfpTailCount counts the SFP+ cages from the TOP of the numbering downward:
// on the lab unit they are 0/17 and 0/18, the last two of eighteen. A gap in
// the pattern (an SFP-flagged port with a plain-copper one above it) is not
// a shape this connector has ever seen, so it reports zero rather than draw
// a faceplate that skips a socket in the middle — the caller then falls back
// to one uniform group.
func sfpTailCount(maxPort int, sfp map[int]bool) int {
	var count int
	var n int
	for n = maxPort; n > 0 && sfp[n]; n-- {
		count++
	}
	return count
}

// splitStandard reads "GE12" back into its number.
//
// ONE kind of socket, and the kind is returned all the same: it is what
// driver.PortNameSplit groups on, and this faceplate has nothing to group away
// from GE (unlike the S3400 next door, whose SFP+ cages are XG<n>). The SFP+
// cages HERE are GE17 and GE18 — plain GE names, the media carried by the
// group's "SFP+" label rather than by the prefix (cf. portGroups above, and
// web/src/network.ts's isSFPPort, which reads the group for the same reason).
// So the whole faceplate of this range is spelled GE<n> and nothing else.
//
// # It is a WRITE GATE, and it is STRICT on purpose
//
// This is not only a sort key. Its `ok` is what [Client.ApplyDHCPSnooping] and
// [Client.ApplyStormControl] refuse a port name on, and what [cliPortName]
// turns "GE12" into "0/12" with — so what it accepts is what reaches the
// hardware.
//
// It used to read the digits with fmt.Sscanf, which stops at the first
// non-digit and therefore accepted a name with a TAIL: "GE12x", "GE12.5"
// and — the one that matters — "GE1/0/2" all came back as a valid port 12,
// 12 and 1. cliPortName then addressed "0/12" / "0/1" and the write landed
// on a real socket, silently, under a name that names no socket on this
// platform. A stacked-faceplate name from another range typed into
// POST /devices/{id}/dhcp-snooping was thus a trust flag on jack 1 of this box.
//
// strconv.Atoi refuses the whole tail, which makes the gate say no where it
// used to guess. Nothing legitimate is lost: every name that reaches here is
// either built by meshcraft itself as fmt.Sprintf("GE%d") — [Driver.StandardPortName],
// and the frontend's own `prefix + n` expansion of port_groups — or typed into
// an HTTP body, which is exactly the case that must not be guessed at. The
// STRICTNESS now matches fs/s3400's splitStandard; the grammar deliberately
// does not (that faceplate has XG cages, this one has none).
func splitStandard(s string) (int, string, bool) {
	var up = strings.ToUpper(strings.TrimSpace(s))
	if !strings.HasPrefix(up, "GE") {
		return 0, "", false
	}
	var n, err = strconv.Atoi(up[2:])
	if err != nil || n < 1 {
		return 0, "", false
	}
	return n, "GE", true
}

package snmppoll

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/gosnmp/gosnmp"
)

// MAC forwarding table (the FDB — what the bridge LEARNED), read by the PROXY.
//
// This is the collection the Clients page stands on: every MAC a switch has
// learned, on which port and in which VLAN. Like the LLDP neighbourhood it is
// a TABLE OF STRINGS, not samples — one time series per client MAC would live
// in VictoriaMetrics for ever after the laptop leaves the building — so it
// takes the exact same shape end to end: a SNAPSHOT ([FDBReport]), pushed by
// its own HTTP route (`POST /api/proxy/fdb`), stored in an inventory table.
// Every argument in lldp.go's header applies verbatim and is not repeated.
//
// # What was MEASURED before any of this was written (lab, 2026-08-13)
//
// Walked live through the product's own diagnostic route, on every v2c device
// of the reference fleet:
//
//	                     dot1qTpFdbPort   dot1dTpFdbPort   basePortIfIndex
//	CRS305   (RouterOS)      101              41                5
//	CRS326   (RouterOS)       23              13               26
//	CSS326   (SwOS)            0              12               26
//	S3900    (FS)             20              17               28
//	1930     (Instant On)     19              16               68
//	EdgeSwitch                17               0               24
//	C2960X   (IOS)             0               6                6
//
// Three regimes, and the collection order below follows them:
//
//   - Q-BRIDGE-MIB serves: six platforms. The index carries the VLAN, which
//     the Clients page needs, so it is always tried first.
//   - BRIDGE-MIB only (CSS326): the flat dot1d table has NO VLAN arc — the
//     VLAN is honestly unknown, recorded as 0, never guessed.
//   - Cisco IOS: dot1q is EMPTY and dot1d serves only the DEFAULT VLAN's
//     instance. The platform's answer is community indexing ("community@N"
//     addresses VLAN N's bridge instance); VLANs are enumerated from
//     vtpVlanState (walked live: 9 rows, 1/42/98/99/100 plus the four
//     1002-1005 defaults). That behaviour is a TARGET FLAG set by the
//     controller from the connector, not sniffed here: the proxy knows no
//     driver.
//
// # Why the port is resolved to a NAME here, unlike LLDP's local port
//
// The FDB names its port twice removed: entry → bridge port → (base port
// table) → ifIndex → (ifDescr) → name. The middle hop, dot1dBasePortIfIndex,
// is only reachable while the SNMP session is open — the controller could
// never redo it. So the proxy resolves down to the DEVICE'S OWN port name
// (ifDescr, the same string the metric samples carry as `port` label) and the
// controller maps that onto the facade with `resolve`, exactly as it does for
// an LLDP local port. An entry whose bridge port has no ifIndex mapping is
// DROPPED and counted, never guessed — same doctrine as everywhere else.

// FDB OIDs.
const (
	// dot1qTpFdbPort (Q-BRIDGE-MIB): index is `fdbId.6-byte-MAC`, value the
	// bridge port. On every surveyed platform fdbId equals the VLAN id, which
	// is Q-BRIDGE's own default (one FID per VLAN, IEEE 802.1Q shared learning
	// aside — none of the fleet does SVL).
	oidDot1qTpFdbPort = "1.3.6.1.2.1.17.7.1.2.2.1.2"
	// dot1dTpFdbPort (BRIDGE-MIB): index is the 6-byte MAC alone. No VLAN.
	oidDot1dTpFdbPort = "1.3.6.1.2.1.17.4.3.1.2"
	// dot1dBasePortIfIndex: bridge port → ifIndex. The hop without which a
	// bridge port number names nothing outside the bridge.
	oidDot1dBasePortIfIndex = "1.3.6.1.2.1.17.1.4.1.2"
	// vtpVlanState (CISCO-VTP-MIB): the VLAN enumeration for community@vlan
	// walking. Value 1 = operational.
	oidVtpVlanState = "1.3.6.1.4.1.9.9.46.1.3.1.1.2"
)

// MetricFDB names this set in the standard-model vocabulary, like MetricLLDP.
const MetricFDB = "fdb"

// maxFDBVLANs bounds the community@vlan enumeration.
//
// Each VLAN costs two extra walks on its own session. A site with hundreds of
// VLANs would turn one poll into hundreds of round trips every cycle; sixty-four
// covers every site this product has seen while keeping the worst case bounded.
// VLANs beyond the bound are SKIPPED and the report says nothing about them —
// honest absence, like every other bounded read here.
const maxFDBVLANs = 64

// vlanCiscoReservedLow / High: 1002-1005 are FDDI/Token Ring defaults that
// exist on every IOS switch and can hold no Ethernet client. Walking them
// costs four sessions for nothing, every cycle.
const (
	vlanCiscoReservedLow  = 1002
	vlanCiscoReservedHigh = 1005
)

// FDBEntry is ONE learned MAC: this port, this VLAN, that address.
type FDBEntry struct {
	// MAC in canonical spelling: lowercase colon-separated hex. The proxy
	// decodes the OID arcs itself, so unlike LLDP's chassis id there is no
	// vendor spelling to preserve — canonicalising here means the controller
	// key never has two spellings of one address.
	MAC string `json:"mac"`
	// VLAN the address was learned in. 0 = the platform did not say
	// (BRIDGE-MIB flat walk), and 0 must never be drawn as "VLAN 0".
	VLAN int `json:"vlan,omitempty"`
	// Port as THE DEVICE names it (ifDescr) — same vocabulary as the metric
	// samples' `port` label. The controller resolves it against the facade.
	Port string `json:"port"`
}

// FDBReport is the full snapshot of one device's forwarding table.
type FDBReport struct {
	DeviceID string     `json:"device_id"`
	Entries  []FDBEntry `json:"entries"`
	// Served: the agent answered under a bridge MIB at all. Same doctrine as
	// LLDPReport.Served — "zero entries" (empty office at night) and "cannot
	// know" must read differently downstream.
	Served bool `json:"served"`
	// Dropped counts entries whose bridge port resolved to no interface name.
	// Counted rather than silent, because a missing client is otherwise
	// indistinguishable from a client that left.
	Dropped int `json:"dropped,omitempty"`
}

// fdbMACFromArcs renders 6 OID arcs as a canonical MAC, "" when they are not
// six bytes.
func fdbMACFromArcs(arcs []string) string {
	if len(arcs) != 6 {
		return ""
	}
	const hexDigits = "0123456789abcdef"
	var out = make([]byte, 0, 17)
	var i int
	for i = range arcs {
		var v, err = strconv.Atoi(arcs[i])
		if err != nil || v < 0 || v > 255 {
			return ""
		}
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hexDigits[v>>4], hexDigits[v&0x0f])
	}
	return string(out)
}

// multicastMAC reports a group address (bit 0 of the first byte). A bridge
// can hold static multicast entries; no client ever has a group address, and
// letting one through would put "01:80:c2:…" on the Clients page as a device.
func multicastMAC(mac string) bool {
	if len(mac) < 2 {
		return true
	}
	var hi = strings.IndexByte("0123456789abcdef", mac[0])
	var lo = strings.IndexByte("0123456789abcdef", mac[1])
	if hi < 0 || lo < 0 {
		return true
	}
	return (hi*16+lo)&1 == 1
}

// fdbRows decodes one FDB column walk into (vlan, mac) → bridge port.
//
// `withVLAN` says whether the index leads with a VLAN arc (Q-BRIDGE) or is
// the bare MAC (BRIDGE); `forceVLAN` stamps every row (community@vlan walks,
// where the VLAN is the CONTEXT and appears in no index).
func fdbRows(walk map[string]gosnmp.SnmpPDU, column string, withVLAN bool,
	forceVLAN int) map[string]int {
	var out = map[string]int{}
	var full string
	var pdu gosnmp.SnmpPDU
	for full, pdu = range walk {
		var suffix, isCol = strings.CutPrefix(strings.TrimPrefix(full, "."), column+".")
		if !isCol {
			continue
		}
		var arcs = strings.Split(suffix, ".")
		var vlan = forceVLAN
		if withVLAN {
			if len(arcs) != 7 {
				continue
			}
			var v, err = strconv.Atoi(arcs[0])
			if err != nil || v < 0 {
				continue
			}
			vlan = v
			arcs = arcs[1:]
		}
		var mac = fdbMACFromArcs(arcs)
		if mac == "" || multicastMAC(mac) {
			continue
		}
		var port, isNum = toFloat(pdu)
		if !isNum || port <= 0 {
			// Bridge port 0 is "not learned on a port" (the device's own
			// addresses, typically): there is no jack to draw it on.
			continue
		}
		out[strconv.Itoa(vlan)+"|"+mac] = int(port)
	}
	return out
}

// FDBReportFrom assembles the snapshot from raw walks. PURE — the attachment
// of a MAC to a port name is where a mistake draws a credible false client,
// and the test replays the lab's real shapes value for value.
//
// `rows` is (vlan|mac) → bridge port, merged across whatever walks ran;
// `portMap` bridge port → ifIndex; `names` ifIndex → ifDescr.
func FDBReportFrom(deviceID string, rows map[string]int, portMap map[int]int,
	names map[int]string, served bool) FDBReport {
	var out = FDBReport{DeviceID: deviceID, Served: served}
	var key string
	var bridgePort int
	for key, bridgePort = range rows {
		var sep = strings.IndexByte(key, '|')
		if sep < 0 {
			continue
		}
		var vlan, _ = strconv.Atoi(key[:sep])
		var ifIndex, mapped = portMap[bridgePort]
		if !mapped {
			out.Dropped++
			continue
		}
		var name = names[ifIndex]
		if name == "" {
			out.Dropped++
			continue
		}
		out.Entries = append(out.Entries, FDBEntry{
			MAC: key[sep+1:], VLAN: vlan, Port: name,
		})
	}
	// SORTED, same reason as every snapshot here: the controller diffs run
	// against run, and Go map order would make identical tables differ.
	sort.Slice(out.Entries, func(i, j int) bool {
		var a, b = out.Entries[i], out.Entries[j]
		if a.MAC != b.MAC {
			return a.MAC < b.MAC
		}
		if a.VLAN != b.VLAN {
			return a.VLAN < b.VLAN
		}
		return a.Port < b.Port
	})
	return out
}

// operationalVLANs extracts the community@vlan enumeration from a
// vtpVlanState walk: operational (value 1), not Cisco-reserved, sorted,
// bounded by maxFDBVLANs.
func operationalVLANs(walk map[string]gosnmp.SnmpPDU) []int {
	var out []int
	var full string
	var pdu gosnmp.SnmpPDU
	for full, pdu = range walk {
		var suffix, isCol = strings.CutPrefix(strings.TrimPrefix(full, "."),
			oidVtpVlanState+".")
		if !isCol {
			continue
		}
		// Index is managementDomainIndex.vlanIndex; the VLAN is the LAST arc.
		var arcs = strings.Split(suffix, ".")
		var vlan, err = strconv.Atoi(arcs[len(arcs)-1])
		if err != nil || vlan <= 0 {
			continue
		}
		if vlan >= vlanCiscoReservedLow && vlan <= vlanCiscoReservedHigh {
			continue
		}
		var state, isNum = toFloat(pdu)
		if !isNum || int(state) != 1 {
			continue
		}
		out = append(out, vlan)
	}
	sort.Ints(out)
	if len(out) > maxFDBVLANs {
		out = out[:maxFDBVLANs]
	}
	return out
}

// PollFDB reads the forwarding table and returns the snapshot.
//
// Order of attempts, from the survey: Q-BRIDGE first (six of seven platforms,
// and the only one that names the VLAN); on empty, either the community@vlan
// enumeration (Target.FDBPerVLAN — Cisco IOS, set by the controller from the
// connector) or the flat BRIDGE walk (SwOS). Never both: a platform that
// serves Q-BRIDGE has said everything dot1d could say.
func (p *Poller) PollFDB(ctx context.Context) (FDBReport, error) {
	var dial = p.DialTable
	if dial == nil {
		dial = dialTableSNMP
	}
	var c, err = dial(p.Target)
	if err != nil {
		return FDBReport{DeviceID: p.Target.DeviceID}, err
	}
	defer c.Close() //nolint:errcheck

	// The two resolution tables ride the base session. ifDescr is also what
	// proves the device reachable at all.
	var names = map[int]string{}
	var nameWalk, nameErr = c.WalkFull(ctx, oidIfDescr)
	if nameErr != nil {
		return FDBReport{DeviceID: p.Target.DeviceID}, nameErr
	}
	var k string
	for k = range nameWalk {
		var suffix, isCol = strings.CutPrefix(strings.TrimPrefix(k, "."), oidIfDescr+".")
		if !isCol {
			continue
		}
		var idx, aErr = strconv.Atoi(suffix)
		if aErr != nil {
			continue
		}
		names[idx] = valueText(nameWalk[k])
	}

	var portMap = map[int]int{}
	var served bool
	var mapWalk, mapErr = c.WalkFull(ctx, oidDot1dBasePortIfIndex)
	if mapErr == nil && len(mapWalk) > 0 {
		served = true
		for k = range mapWalk {
			var suffix, isCol = strings.CutPrefix(strings.TrimPrefix(k, "."),
				oidDot1dBasePortIfIndex+".")
			if !isCol {
				continue
			}
			var bp, aErr = strconv.Atoi(suffix)
			if aErr != nil {
				continue
			}
			var idx, isNum = toFloat(mapWalk[k])
			if isNum {
				portMap[bp] = int(idx)
			}
		}
	}

	var rows = map[string]int{}
	var qWalk, qErr = c.WalkFull(ctx, oidDot1qTpFdbPort)
	if qErr == nil && len(qWalk) > 0 {
		served = true
		rows = fdbRows(qWalk, oidDot1qTpFdbPort, true, 0)
	}

	if len(rows) == 0 && p.Target.FDBPerVLAN {
		// Cisco: enumerate VLANs, then one SESSION per VLAN — the community
		// itself addresses the instance, so the connection conventions cannot
		// be shared with the base session.
		var vtp, vtpErr = c.WalkFull(ctx, oidVtpVlanState)
		if vtpErr == nil {
			var vlan int
			for _, vlan = range operationalVLANs(vtp) {
				var t = p.Target
				t.Community = p.Target.Community + "@" + strconv.Itoa(vlan)
				var vc, vErr = dial(t)
				if vErr != nil {
					continue
				}
				var dWalk, dErr = vc.WalkFull(ctx, oidDot1dTpFdbPort)
				var vMapWalk, _ = vc.WalkFull(ctx, oidDot1dBasePortIfIndex)
				_ = vc.Close()
				if dErr != nil || len(dWalk) == 0 {
					continue
				}
				served = true
				// The per-VLAN instance has its OWN bridge port numbering:
				// resolve inside this VLAN's map, fall back to the base one.
				var vMap = map[int]int{}
				for k = range vMapWalk {
					var suffix, isCol = strings.CutPrefix(strings.TrimPrefix(k, "."),
						oidDot1dBasePortIfIndex+".")
					if !isCol {
						continue
					}
					var bp, aErr = strconv.Atoi(suffix)
					if aErr != nil {
						continue
					}
					var idx, isNum = toFloat(vMapWalk[k])
					if isNum {
						vMap[bp] = int(idx)
					}
				}
				var vRows = fdbRows(dWalk, oidDot1dTpFdbPort, false, vlan)
				var rk string
				var bp int
				for rk, bp = range vRows {
					var idx, mapped = vMap[bp]
					if !mapped {
						idx, mapped = portMap[bp]
					}
					if !mapped {
						continue
					}
					// Re-key through a synthetic map so FDBReportFrom keeps one
					// resolution path: store the ifIndex as the "bridge port"
					// and give it an identity mapping below.
					rows[rk] = idx
					portMap[idx] = idx
				}
			}
		}
	} else if len(rows) == 0 {
		// Flat BRIDGE walk: VLAN honestly unknown.
		var dWalk, dErr = c.WalkFull(ctx, oidDot1dTpFdbPort)
		if dErr == nil && len(dWalk) > 0 {
			served = true
			rows = fdbRows(dWalk, oidDot1dTpFdbPort, false, 0)
		}
	}

	return FDBReportFrom(p.Target.DeviceID, rows, portMap, names, served), nil
}

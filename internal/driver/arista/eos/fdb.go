package eos

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// The forwarding table over the CLI — this platform's ONLY door to it.
//
// Both collection paths were tried before this file existed (2026-08-13):
// the SNMP poller cannot reach the device (its supervision is gNMI; there is
// no v2c community to present), and this train rejects the OpenConfig FDB
// subtree outright — a subscription to
// `/network-instances/network-instance[name=*]/fdb/mac-table` ends with
// `InvalidArgument: failed to access node "fdb" in node "network-instance"`.
// What remains is "show mac address-table", captured the same day on
// core01.lab:
//
//	Vlan    Mac Address       Type        Ports      Moves   Last Move
//	----    -----------       ----        -----      -----   ---------
//	   1    04f4.1c1d.7458    DYNAMIC     Et48       1       0:05:40 ago
//	  99    649d.9916.3410    DYNAMIC     Et48       1       13:13:27 ago
//	Total Mac Addresses for this criterion: 16
//
//	          Multicast Mac Address Table
//	...
//
// Two tables in one output, and the multicast one must NOT be read: its rows
// match the same shape, and a multicast group drawn as a client is exactly
// the false row the SNMP path filters with its multicast-bit check. The
// unicast section is recognised row by row instead of by section header —
// every row whose Type is DYNAMIC names one learned unicast MAC, and the
// multicast table on this train only ever carries STATIC groups.

// reFDBRow matches one table row: vlan, dotted MAC, type, port.
var reFDBRow = regexp.MustCompile(
	`(?m)^\s*(\d+)\s+([0-9a-fA-F]{4}\.[0-9a-fA-F]{4}\.[0-9a-fA-F]{4})\s+(\S+)\s+(\S+)`)

// dottedMACToCanonical converts EOS's "04f4.1c1d.7458" to the canonical
// "04:f4:1c:1d:74:58". "" when the shape is not three groups of four hex.
func dottedMACToCanonical(dotted string) string {
	var parts = strings.Split(strings.ToLower(dotted), ".")
	if len(parts) != 3 {
		return ""
	}
	var flat = parts[0] + parts[1] + parts[2]
	if len(flat) != 12 {
		return ""
	}
	var out = make([]byte, 0, 17)
	var i int
	for i = 0; i < 12; i += 2 {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, flat[i], flat[i+1])
	}
	return string(out)
}

// parseShowMACAddressTable extracts the DYNAMIC unicast entries.
//
// PURE, tested on the capture. STATIC entries are the switch's own addresses
// and its multicast groups; a client is by definition something the bridge
// LEARNED.
func parseShowMACAddressTable(text string) driver.FDBObservation {
	var out driver.FDBObservation
	var rows = reFDBRow.FindAllStringSubmatch(text, -1)
	var i int
	for i = range rows {
		if !strings.EqualFold(rows[i][3], "DYNAMIC") {
			continue
		}
		var mac = dottedMACToCanonical(rows[i][2])
		if mac == "" {
			continue
		}
		var vlan, err = strconv.Atoi(rows[i][1])
		if err != nil {
			continue
		}
		var std, ok = (&Driver{}).StandardPortName(rows[i][4])
		if !ok {
			// A row on a port the naming does not know (Port-Channel, CPU):
			// noted, not guessed. Po/Vx interfaces are not faceplate jacks.
			out.Notes = append(out.Notes,
				"entry "+mac+" on \""+rows[i][4]+"\", a port outside the faceplate — not placed")
			continue
		}
		out.Entries = append(out.Entries, driver.FDBEntryObservation{
			MAC: mac, VLAN: vlan, LocalPort: std,
		})
	}
	return out
}

// ReadFDBObservation reads the forwarding table over the open session.
func (c *Client) ReadFDBObservation(ctx context.Context) (driver.FDBObservation, error) {
	if c.cli == nil {
		return driver.FDBObservation{}, fmt.Errorf("no open session on this access")
	}
	var text, err = c.cli.run(ctx, "show mac address-table", cmdTimeout)
	if err != nil && text == "" {
		return driver.FDBObservation{}, fmt.Errorf("show mac address-table failed: %v", err)
	}
	return parseShowMACAddressTable(text), nil
}

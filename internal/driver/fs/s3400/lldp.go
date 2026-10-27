package s3400

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// driver.LLDPReader adapter — the CLI neighbour read, in the shape the
// read-state gesture consumes.
//
// This platform is WHY the contract exists: LLDP runs on the wire (`lldp
// run`, and two fleet devices record its frames) while the SNMP agent serves
// nothing under 1.0.8802 nor under its own enterprise trees (walked live,
// 2026-08-05 — closed negatively). The CLI table is the only observation
// this firmware will ever produce.
//
// # The chassis is served NIBBLE-SWAPPED, and only the per-interface form serves it at all
//
// Two facts captured live on the lab 48T4SP (2026-08-05,
// testdata/show_lldp_neighbor_detail.txt):
//
//   - The compact "show lldp neighbor" table has NO chassis column, and its
//     Device-ID column can be blank (the S3900 advertises no system name).
//     Only "show lldp neighbor interface <name>" (the detail form) serves
//     "chassis id:", plus system name/description and the management
//     address.
//   - Every chassis MAC this firmware displays has ITS NIBBLES SWAPPED WITHIN
//     EACH BYTE. Two independent proofs against known base MACs:
//     the EdgeSwitch 9c:05:d6:f5:f1:37 shows as "c950.6d5f.1f73", and the
//     S3900 64:9d:99:16:34:10 shows as "46d9.9961.4301" — byte for byte,
//     both are the exact per-byte nibble swap, so this is the firmware's
//     display bug (BDCOM lineage), not a parsing accident. The swap is undone
//     here (unswapNibbles) so what leaves this connector is the neighbour's
//     REAL address — the identity every other witness in the fleet agrees on.
//
// The read is therefore two-step: the compact table names the LOCAL ports
// that hold a neighbour (the detail form does not repeat the local port per
// entry), then one per-interface query per such port fetches the identity.
// One extra command per neighbour, and neighbours only exist on uplinks —
// two on the lab unit.

// reDetailField reads the "label: value" lines of the per-interface form.
var reDetailField = regexp.MustCompile(`(?i)^\s*(chassis id|port id|port description|system name|system description)\s*:\s*(.*)$`)

// reDottedMAC: the display shape this CLI uses for a chassis MAC,
// "c950.6d5f.1f73".
var reDottedMAC = regexp.MustCompile(`^([0-9a-fA-F]{4})\.([0-9a-fA-F]{4})\.([0-9a-fA-F]{4})$`)

// unswapNibbles undoes the firmware's per-byte nibble swap and renders the
// REAL MAC in colon form ("9c:05:d6:f5:f1:37"). Returns "" when the token is
// not the dotted-MAC display shape — a chassis id of another subtype (a
// name, an IP) must pass through untouched by the caller, not be mangled.
func unswapNibbles(dotted string) string {
	var m = reDottedMAC.FindStringSubmatch(strings.TrimSpace(dotted))
	if m == nil {
		return ""
	}
	var hex = strings.ToLower(m[1] + m[2] + m[3])
	var b = []byte(hex)
	var out []string
	var i int
	for i = 0; i+1 < len(b); i += 2 {
		// each displayed byte "c9" is really "9c": swap the two nibbles back
		out = append(out, string([]byte{b[i+1], b[i]}))
	}
	return strings.Join(out, ":")
}

// parseLLDPNeighborDetail reads ONE per-interface detail answer. Pure.
func parseLLDPNeighborDetail(out string) (chassis, portID, portDesc, sysName, sysDesc string) {
	var line string
	for _, line = range strings.Split(out, "\n") {
		var m = reDetailField.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		var v = strings.TrimSpace(m[2])
		if strings.HasPrefix(v, "-- not advertised") {
			v = ""
		}
		switch strings.ToLower(m[1]) {
		case "chassis id":
			chassis = v
		case "port id":
			portID = v
		case "port description":
			portDesc = v
		case "system name":
			sysName = v
		case "system description":
			sysDesc = v
		}
	}
	return
}

// lldpObservationFrom translates the two-step read into the contract shape.
//
// PURE, and tested against the lab captures (cf. lldp_test.go). `details`
// maps a LOCAL standard port name to its per-interface detail answer; a port
// present in the table but absent from the map degrades to the compact
// columns alone — the read must not lose a neighbour because one follow-up
// query failed.
func lldpObservationFrom(rows []LLDPNeighbor, details map[string]string, enabled bool) driver.LLDPObservation {
	var obs = driver.LLDPObservation{Enabled: enabled}
	if !enabled {
		obs.Notes = append(obs.Notes,
			"LLDP is not enabled on this device (\"lldp run\" absent): the neighbour"+
				" table cannot exist until it is")
		return obs
	}
	var i int
	for i = range rows {
		var n = driver.LLDPNeighborObservation{
			LocalPort: rows[i].LocalPort,
			PortID:    rows[i].PortID,
			SysName:   rows[i].DeviceID,
		}
		var detail, has = details[rows[i].LocalPort]
		if has {
			var chassis, portID, portDesc, sysName, sysDesc = parseLLDPNeighborDetail(detail)
			// The display's dotted MAC is nibble-swapped (cf. the header): what
			// leaves here is the real address. A chassis of another subtype
			// passes through as displayed.
			var real = unswapNibbles(chassis)
			if real != "" {
				n.ChassisID = real
			} else {
				n.ChassisID = chassis
			}
			if portID != "" {
				n.PortID = portID
			}
			n.PortDesc = portDesc
			if sysName != "" {
				n.SysName = sysName
			} else if unswapNibbles(rows[i].DeviceID) != "" {
				// The compact table's Device-ID was just the swapped chassis MAC
				// again, not a system name: do not present it as one.
				n.SysName = ""
			}
			n.SysDesc = sysDesc
		}
		obs.Neighbors = append(obs.Neighbors, n)
	}
	return obs
}

// ReadLLDPObservation implements driver.LLDPReader.
func (c *Client) ReadLLDPObservation(ctx context.Context) (driver.LLDPObservation, error) {
	var rows, enabled, err = c.ReadLLDPNeighbors(ctx)
	if err != nil {
		return driver.LLDPObservation{}, err
	}
	var details = map[string]string{}
	if enabled {
		var i int
		for i = range rows {
			var cli = cliDetailPortName(rows[i].LocalPort)
			if cli == "" {
				continue
			}
			var out, derr = c.cli.runPaged(ctx,
				"show lldp neighbor interface "+cli, 15*time.Second)
			if derr != nil && strings.TrimSpace(out) == "" {
				continue // degrade to the compact columns for this port
			}
			details[rows[i].LocalPort] = out
		}
	}
	return lldpObservationFrom(rows, details, enabled), nil
}

// cliDetailPortName renders the LONG interface spelling the per-interface
// LLDP query takes ("TGigaEthernet0/1", "GigaEthernet0/1" — captured; the
// short "tg0/1" works for configuration mode but the long form is what the
// product documents for show commands and what was exercised live).
func cliDetailPortName(standard string) string {
	var n, kind, ok = splitStandard(standard)
	if !ok {
		return ""
	}
	if kind == "XG" {
		return fmt.Sprintf("TGigaEthernet0/%d", n)
	}
	return fmt.Sprintf("GigaEthernet0/%d", n)
}

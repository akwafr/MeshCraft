package procurve

// LLDP.
//
// # This range ships with LLDP ON, and that is worth stating rather than
// assuming
//
// The reference unit's configuration carries no "lldp run" line and its
// neighbour table is populated anyway (CAPTURED 2026-08-03: port 47 sees
// "salon-sw01"). On this platform LLDP is a factory default and the
// configuration only records a DEPARTURE from it — "no lldp run" — which is
// the opposite of the FS S3400, where LLDP is off until it is switched on and
// the topology view stays empty on a perfectly healthy switch.
//
// So EnsureLLDP here is a check that costs one command, not a write that always
// fires. Re-sending "lldp run" to a device that never had it turned off would
// dirty the configuration and put a line in the running config that says
// something the operator did not do.

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// EnsureLLDP guarantees the device advertises itself.
//
// Returns without writing when LLDP is already on, which on this range is the
// normal case.
func (c *Client) EnsureLLDP(ctx context.Context) error {
	if c.cli == nil {
		return errNoSession
	}
	var text, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && strings.TrimSpace(text) == "" {
		return fmt.Errorf("reading the configuration to check LLDP: %v",
			firstLine(err.Error()))
	}
	if !reNoLLDP.MatchString(text) {
		c.step(true, "LLDP already running", "this range enables it by default")
		return nil
	}
	if err = c.runConfigBlock(ctx, []string{"lldp run"}); err != nil {
		c.step(false, "\"lldp run\" refused", firstLine(err.Error()))
		return fmt.Errorf("lldp run on %s: %v", c.tgt.Host, firstLine(err.Error()))
	}
	c.step(true, "LLDP enabled", "\"lldp run\" — neighbours appear within one advertisement interval")
	if err = c.persist(ctx); err != nil {
		return fmt.Errorf("lldp run applied but not saved: %v", firstLine(err.Error()))
	}
	return nil
}

// ReadLLDPNeighbors returns the neighbour table as the device summarises it.
//
// The summary TRUNCATES long port descriptions and system names (cf.
// LLDPNeighbor); meshcraft's topology is built from SNMP, where they arrive
// whole. This read answers "who is on this port" for an operator looking at one
// device, and marks what the switch abbreviated.
func (c *Client) ReadLLDPNeighbors(ctx context.Context) ([]LLDPNeighbor, error) {
	if c.cli == nil {
		return nil, errNoSession
	}
	var text, err = c.cli.run(ctx, "show lldp info remote-device", showRunTimeout)
	if err != nil && strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("show lldp info remote-device on %s: %v", c.tgt.Host,
			firstLine(err.Error()))
	}
	return parseLLDPRemote(text), nil
}

// Persist writes the running configuration to flash.
//
// Exported so a caller can save WITHOUT applying anything — the shape
// [driver.Persister] describes. The gestures in this package already persist
// what they wrote; this is for a caller that changed something through another
// path and wants it to survive a reboot.
func (c *Client) Persist(ctx context.Context) error {
	if c.cli == nil {
		return errNoSession
	}
	var err = c.persist(ctx)
	if err != nil {
		c.step(false, "\"write memory\" refused", firstLine(err.Error()))
		return err
	}
	c.step(true, "configuration persisted", "write memory")
	return nil
}

// errNoSession is the one refusal every read and write on this Client starts
// with. Shared so the wording does not drift between them.
var errNoSession = driver.NotSupported("hpe-procurve", "session",
	"no open session on this access")

// --- driver.LLDPReader adapter ---
//
// The CLI neighbour read above, in the shape the read-state gesture consumes
// (cf. api.readLLDPNeighborhood). fs/s3400 and ubiquiti/edgeswitch got theirs on
// 2026-08-05; this one follows the edgeswitch pattern, which is the simple half
// — one command, one table, no per-interface follow-up.
//
// # THE RANGE IS STILL PARKED, and this changes nothing about that
//
// Adopting an interface is pure wiring: it sends no command this connector did
// not already send, on no session it did not already hold, and it does not
// retire a single line of contract.go's refusal to write. Read it as the read
// path catching up with the fleet, NOT as the park being lifted — the writes
// still refuse, the deduced "enable" verb is still deduced, and the lab 2610
// was OFFLINE the day this was written, so nothing here has been exercised
// against hardware. What HAS been exercised is the parse it stands on:
// [parseLLDPRemote] against testdata/show_lldp_info_remote_device.txt, captured
// on SW-PLAYER-79 on 2026-08-03.
//
// # WHY Enabled IS TRUE BY CONSTRUCTION
//
// This range ships with LLDP on (see the header of this file) and its neighbour
// table carries no "LLDP is disabled" marker — a switch with "no lldp run" in
// its configuration prints the same empty table as one with nothing plugged in.
// The command having answered is the only evidence this platform offers here,
// and claiming more would be inventing. [Client.EnsureLLDP] is what actually
// reads the configuration for "no lldp run", and it costs a running-config
// read this method must not add.
//
// # WHAT THE PLATFORM CUT IS NOT REPORTED AS WHAT IT SAW
//
// The summary view TRUNCATES to its column widths and marks the cut with an
// ellipsis (cf. [LLDPNeighbor]): "ge-0/0..." for a Juniper's "ge-0/0/9.0".
// Those values are DROPPED rather than passed on, and PortDesc is why. It feeds
// api.resolvePeerPort, which names the far end of a cable, and refutation is
// port-exact: a prefix of a port name is not a port name, and the cheap failure
// (a link drawn from one side only) is the one to prefer over the expensive one
// (a real cable retired because the far port was named with confidence and
// wrongly). SysName is kept even when cut — it is only ever compared for EXACT
// equality when recognising a peer, so a truncated one can fail to match but
// never match the wrong device, and it remains the only thing an operator sees
// for a neighbour meshcraft does not own.
//
// The cut is not silent: it goes into Notes, where the read-state response
// surfaces it (`lldp_notes`).

// lldpObservationFrom translates the parsed neighbour table into the contract
// shape. PURE, so it is tested against the captured table without a device.
//
// `prefix` is the facade prefix in force for this device (cf.
// [Client.portPrefix]): LocalPort must leave here as a STANDARD-MODEL name, the
// vocabulary api.lldpRowsFor resolves by exact match.
func lldpObservationFrom(rows []LLDPNeighbor, prefix string) driver.LLDPObservation {
	var obs = driver.LLDPObservation{Enabled: true}
	var cut []string
	var i int
	for i = range rows {
		var local, ok = wholePortNumber(rows[i].LocalPort)
		if !ok {
			// A local port this CLI did not print as a number is a row we cannot
			// place on the faceplate. The API layer drops unresolvable ports and
			// counts them; not emitting it at all is the same outcome, said here.
			continue
		}
		var n = driver.LLDPNeighborObservation{
			LocalPort: prefix + local,
			ChassisID: rows[i].ChassisID,
			SysName:   rows[i].SysName,
		}
		// Only values the platform printed WHOLE may name a port.
		if !truncated(rows[i].PortID) {
			n.PortID = rows[i].PortID
		}
		if !truncated(rows[i].PortDescr) {
			n.PortDesc = rows[i].PortDescr
		} else {
			cut = append(cut, prefix+local)
		}
		obs.Neighbors = append(obs.Neighbors, n)
	}
	if len(cut) > 0 {
		obs.Notes = append(obs.Notes,
			"this switch abbreviates its neighbour summary to the column width: the"+
				" far-end port description was cut on "+strings.Join(cut, ", ")+
				" and is therefore not used to name the far port")
	}
	return obs
}

// truncated: the platform's own marker that a column was cut.
func truncated(s string) bool {
	return strings.HasSuffix(strings.TrimSpace(s), "...")
}

// wholePortNumber accepts the bare number this CLI uses for a local port, and
// refuses anything else — including a truncated one.
func wholePortNumber(s string) (string, bool) {
	var v = strings.TrimSpace(s)
	if v == "" || truncated(v) {
		return "", false
	}
	var r rune
	for _, r = range v {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return v, true
}

// ReadLLDPObservation implements driver.LLDPReader.
func (c *Client) ReadLLDPObservation(ctx context.Context) (driver.LLDPObservation, error) {
	var rows, err = c.ReadLLDPNeighbors(ctx)
	if err != nil {
		return driver.LLDPObservation{}, err
	}
	return lldpObservationFrom(rows, c.portPrefix()), nil
}

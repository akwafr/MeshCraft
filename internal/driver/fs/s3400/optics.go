package s3400

// driver.OpticsReader adapter — the DDM block of "show interface tg0/N",
// in the shape the read-state gesture consumes.
//
// This platform is WHY the contract exists (cf. driver/optics.go): the CLI
// serves a full DDM block per occupied cage while the SNMP enterprise trees
// answer empty, so the admin session is the only channel that will ever
// carry these readings. Captured live on the lab 48T4SP (2026-08-05,
// testdata/show_interface_tg0*.txt):
//
//	Transceiver Info:
//	    SFP,LC,1330nm,10000BASE-FX,LOS:no
//	    SM 20KM
//	    DDM:YES,Vend:FS,PN:SFP-10G-BX
//	    SerialNum:G2210006755,Date:2022-02-28
//	DDM info:
//	    TX power:-2.08 dBm, RX power:-1.85 dBm
//	    SFP temperature:46.00 C,supply voltage :3.26V,Bias Current.:35.61mA
//
// Three shapes the regexes below are anchored to, all captured:
//
//   - an EMPTY cage prints no "Transceiver Info:" block at all — its
//     hardware line even changes, "10Giga-FX" against "10Giga-FX-SFP"
//     when a module sits in it (tg0/3, tg0/4). Absence of the block IS the
//     "no module" signal; there is no negative wording to match.
//   - the measurement line is irregular on purpose here because it is
//     irregular on the wire: "supply voltage :3.26V" (space BEFORE the
//     colon) and "Bias Current.:35.61mA" (a stray period) are the
//     firmware's own spelling. The 2026-08-03 hand capture also had a space
//     after the comma ("C, supply") where the 2026-08-05 one has none —
//     so no regex below leans on the separators between fields.
//   - the same values reappear lower in a "DDM Thresholds" table
//     (alarm/warning bounds). Deliberately not parsed: nothing downstream
//     consumes thresholds yet, and the readings' lines are matched by their
//     LABELS, which the threshold table spells differently
//     ("TX power(dBm):"), so the two never collide.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

var (
	// reOpticModule: "DDM:YES,Vend:FS,PN:SFP-10G-BX" — the line that proves a
	// module is present and names it. Vend can carry spaces (vendor names do);
	// it stops at the comma before PN.
	reOpticModule = regexp.MustCompile(`(?im)^\s*DDM\s*:\s*(YES|NO)\s*,\s*Vend\s*:\s*([^,]*?)\s*,\s*PN\s*:\s*(\S+)\s*$`)
	// reOpticSerial: "SerialNum:G2210006755,Date:2022-02-28".
	reOpticSerial = regexp.MustCompile(`(?im)^\s*SerialNum\s*:\s*([^,\s]+)`)
	// reOpticTransceiver: the block header. Matched separately from the module
	// line so a module the firmware describes without a DDM line still counts
	// as present.
	reOpticTransceiver = regexp.MustCompile(`(?im)^\s*Transceiver Info\s*:`)
	// The readings. Labels only — separators and spacing vary (cf. header).
	reOpticTx   = regexp.MustCompile(`(?i)\bTX power\s*:\s*(-?\d+(?:\.\d+)?)\s*dBm`)
	reOpticRx   = regexp.MustCompile(`(?i)\bRX power\s*:\s*(-?\d+(?:\.\d+)?)\s*dBm`)
	reOpticTemp = regexp.MustCompile(`(?i)\bSFP temperature\s*:\s*(-?\d+(?:\.\d+)?)\s*C\b`)
	reOpticVolt = regexp.MustCompile(`(?i)\bsupply voltage\s*:\s*(-?\d+(?:\.\d+)?)\s*V\b`)
	reOpticBias = regexp.MustCompile(`(?i)\bBias Current\.?\s*:\s*(-?\d+(?:\.\d+)?)\s*mA\b`)
)

// parseInterfaceOptics reads ONE "show interface tg0/N" answer. Pure.
//
// served is false when the output carries no transceiver block at all — an
// empty cage, or a copper port. The Port field is left for the caller: the
// answer does carry the long interface name, but the caller already knows
// which standard port it asked about.
func parseInterfaceOptics(out string) (st driver.OpticsPortState, served bool) {
	if !reOpticTransceiver.MatchString(out) && !reOpticModule.MatchString(out) {
		return st, false
	}
	served = true
	var m = reOpticModule.FindStringSubmatch(out)
	if m != nil {
		st.DDM = strings.EqualFold(m[1], "YES")
		st.Vendor = strings.TrimSpace(m[2])
		st.PartNumber = m[3]
	}
	if m = reOpticSerial.FindStringSubmatch(out); m != nil {
		st.Serial = m[1]
	}
	st.TxPowerDBm = matchFloat(reOpticTx, out)
	st.RxPowerDBm = matchFloat(reOpticRx, out)
	st.TemperatureC = matchFloat(reOpticTemp, out)
	st.VoltageV = matchFloat(reOpticVolt, out)
	st.BiasCurrentMA = matchFloat(reOpticBias, out)
	return st, served
}

// matchFloat returns the first captured group as a float, nil when the
// pattern does not match — nil is the contract's "not served", and 0.0 dBm
// is a legal reading (cf. driver/optics.go).
func matchFloat(re *regexp.Regexp, out string) *float64 {
	var m = re.FindStringSubmatch(out)
	if m == nil {
		return nil
	}
	var v, err = strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	return &v
}

// ReadOptics implements driver.OpticsReader: one per-interface query per SFP+
// cage. Four commands on a 48T4SP, each answering in well under a second
// (captured 2026-08-05) — cheap enough to ride read-state unconditionally.
//
// The COPPER ports are deliberately not queried: "show interface g0/N"
// serves no transceiver block on this range (captured, g0/1), and 48 extra
// round trips per read-state to learn nothing would be the pager tax at its
// worst.
func (c *Client) ReadOptics(ctx context.Context) (driver.OpticsState, error) {
	var st driver.OpticsState
	var _, sfp = portCountsFor(c.facts.Model)
	if sfp <= 0 {
		st.Notes = append(st.Notes,
			"no optical cage on this model ("+orUnknown(c.facts.Model)+"): nothing to read")
		return st, nil
	}
	// The cage NAMES are what this read is made of, so the numbering has to be
	// settled before the first query. Free when this rides a read-state (which
	// is what the header describes); one read when the optics sampler arrives
	// on a session of its own. Cf. ADR-0018.
	c.ensureCageNumbering(ctx)
	var n int
	var failures []string
	for n = 1; n <= sfp; n++ {
		var port = fmt.Sprintf("XG%d", n)
		// A READ, so an unestablished numbering is a per-cage failure in the
		// mechanism that already exists below, not a refusal (ADR-0018). Before
		// this, on a build numbering its cages 49..52, these four commands
		// queried four interfaces the switch does not have and the optics came
		// back empty on a site whose uplinks are the only optics it has.
		var dev, nerr = c.cages.cliPortName(port)
		if nerr != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", port, nerr))
			continue
		}
		var out, err = c.cli.runPaged(ctx, "show interface "+dev, 15*time.Second)
		if err != nil && strings.TrimSpace(out) == "" {
			failures = append(failures, fmt.Sprintf("%s: %v", port, err))
			continue
		}
		var ps, served = parseInterfaceOptics(out)
		if !served {
			continue // empty cage: no transceiver block at all (cf. header)
		}
		ps.Port = port
		st.Ports = append(st.Ports, ps)
	}
	if len(failures) > 0 {
		// Partial reads stay partial rather than failing the whole gesture —
		// but a read that answered NOTHING is an error, not an empty state
		// a caller could mistake for "no module anywhere".
		if len(failures) == sfp {
			return st, fmt.Errorf("no optical cage could be read on %s: %s",
				c.host, strings.Join(failures, " ; "))
		}
		st.Notes = append(st.Notes, "not read: "+strings.Join(failures, " ; "))
	}
	return st, nil
}

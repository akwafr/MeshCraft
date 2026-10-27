package edgeswitch

// PoE — Power over Ethernet.
//
// # Status: read AND write proven on hardware, 2026-08-05
//
// The lab unit this package is calibrated on (EdgePoint S16 / EP-S16,
// hardware revision 20, firmware 1.9.2) turned out to have real PoE hardware
// on twelve of its sixteen RJ45 sockets — a fact the platform's own naming
// does NOT announce (no wattage suffix, no bare "PoE" in "EdgePoint S16"),
// which is exactly why [driver.PoEPortState.Capable] is read from the device
// rather than guessed from the model string. Sockets 0/1-0/4 carry NO
// power-sourcing hardware at all: every "poe opmode" but "shutdown" is
// REFUSED by the device itself, with either "Unsupported Interfaces: 0/N" or
// "Error Interfaces: 0/N" depending on which mode was asked for. Sockets
// 0/5-0/16 accept exactly two deliverable classes — "auto" (802.3af/at,
// negotiates up to ~54V: [driver.PoEClass54V]) and "passive24v"
// ([driver.PoEClass24V]) — and refuse the two 4-pair passive variants the CLI
// grammar also lists ("passive24v-4pair", "passive54v-4pair": DOCUMENTED, but
// "Unsupported Interfaces" on every port tried, 0/5 through 0/16). The read
// path was captured with nothing plugged in anywhere (every port reads
// Consumed(W) 0.00), so live draw shapes ARE captured (cf. testdata/) but a
// nonzero PowerW/VoltageV/CurrentMA reading has not been observed — the
// column layout has, and that is what the parser depends on.
//
// The confirmation prompt a PASSIVE mode asks — "Setting Passive power mode
// might damage unsupported device, are you sure you want to change opmode?
// (y/n)" — is answered "y" automatically (cf. ssh.go's rePoEConfirm): the
// operator already chose the class explicitly (cf. driver.PoEPortIntent), so
// the question the CLI is asking has already been asked, by meshcraft, one
// layer up.
//
// The per-port CAPABILITY split (0/1-0/4 versus 0/5-0/16 on THIS unit) is
// read, not hardcoded: [parsePoEStatusAll] derives it from the Temperature(C)
// column, which prints "N/A" on every port with no PSE hardware behind it and
// a real reading on every port that has one — a signal that costs nothing to
// read on every poll, unlike the write-and-observe probing that established
// it in the first place.
//
// The "Consumed Meter(Whr)" column is a FIRMWARE ADDITION over the CLI
// reference's six-column example (Detection/Class/Consumed/Voltage/Current/
// Temperature) — [rePoEStatusRow] tolerates its presence or absence.

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// --- show poe port all --------------------------------------------------
//
//	          OP               HP      HP      Detect  Disconnect Class
//	Intf      Mode             Enable  Mode    Enable  Enable     Enable
//	--------- ---------------- ------- ------- ------- ---------- -------
//	0/1       Shutdown         Enable  802.3at Enable  Enable     Enable
//	0/5       Auto             Enable  802.3at Enable  Enable     Enable
//
// Every field is a single token on every row captured — CAPTURED, not just
// documented, which matters because the Detection column of the OTHER table
// below is not: "Open Circuit" carries a space.

var rePoEPortRow = regexp.MustCompile(
	`(?m)^\s*(\d+/\d+)\s+(\S+)\s+(Enable|Disable)\s+(\S+)\s+(Enable|Disable)\s+(Enable|Disable)\s+(Enable|Disable)\s*$`)

// poePortCfg is one row of "show poe port all".
type poePortCfg struct {
	intf   string
	opMode string // "Shutdown", "Auto", "Passive24V" — this unit's vocabulary
	hpMode string // "802.3at" on every port surveyed
}

func parsePoEPortAll(out string) map[string]poePortCfg {
	var m = map[string]poePortCfg{}
	var rows = rePoEPortRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		m[rows[i][1]] = poePortCfg{intf: rows[i][1], opMode: rows[i][2], hpMode: rows[i][4]}
	}
	return m
}

// classOfOpMode maps this platform's OP Mode word onto the shared vocabulary.
//
// "auto" is called [driver.PoEClass54V] because 802.3af/at is the class every
// operator means by "the modern one", and its ceiling is in that bracket —
// NOT because this connector has observed a port actually negotiate 54V (cf.
// the file header: nothing was plugged in during the survey).
//
// # The 4-PAIR variants are REFUSED here, and that is a correction
//
// This used to map them by substring and call it defensive: "passive54v-4pair"
// contains "54v", so it read back as plain [driver.PoEClass54V], and
// "passive24v-4pair" as plain [driver.PoEClass24V]. The comment claimed a
// future unit accepting them "should read correctly without a parser change" —
// the opposite of what would happen. It would read the 2-PAIR class for a port in
// 4-PAIR mode, confidently, with nothing on screen to say the reading was
// inferred.
//
// The consequence is not cosmetic, because this connector WRITES PoE and
// [Client.CyclePortPower] restores the class it read. A 4-pair jack read as plain
// "auto" passes opModeOfClass's sendable test, so the cycler would cut it and
// put back the TWO-pair mode — silently halving the power available to whatever
// it feeds, on the gesture whose entire contract is "the same class comes back".
//
// So an unrecognized mode is reported as unrecognized. mergePoEState already has
// the answer for that — "OP Mode %q was not recognized — class left unset rather
// than guessed" — and the cycler already refuses a port whose class is unknown
// ("cutting power here would be half a power cycle"). Both are the honest outcome
// on hardware nobody here has surveyed. Neither 4-pair mode has ever been ACCEPTED
// by the lab unit (every port tried answers "Unsupported Interfaces"), which is
// exactly why this went unnoticed: today it is unreachable, and the day it is not
// is the day it would matter.
func classOfOpMode(opMode string) (class string, ok bool) {
	var m = strings.ToLower(strings.TrimSpace(opMode))
	switch {
	case m == "shutdown":
		return driver.PoEClassNone, true
	case m == "auto":
		return driver.PoEClass54V, true
	case strings.Contains(m, "4pair"), strings.Contains(m, "4-pair"):
		// BEFORE the substring cases below, which would otherwise claim these.
		// Both spellings: the CLI grammar prints "passive24v-4pair", and a
		// firmware that hyphenated it differently must not slip past into a
		// confident wrong answer.
		return "", false
	case strings.Contains(m, "54v"):
		return driver.PoEClass54V, true
	case strings.Contains(m, "24v"):
		return driver.PoEClass24V, true
	}
	return "", false
}

// opModeOfClass is the inverse, for ApplyPoE: which CLI word to send for a
// requested class. Only the two forms PROVEN on this hardware are produced —
// the 4-pair variants are refused by every port tried, so ApplyPoE never
// sends them (cf. the file header).
func opModeOfClass(class string) (opMode string, confirmRequired bool, ok bool) {
	switch class {
	case driver.PoEClassNone:
		return "shutdown", false, true
	case driver.PoEClass54V:
		return "auto", false, true
	case driver.PoEClass24V:
		return "passive24v", true, true
	}
	return "", false, false
}

// --- show poe status all -------------------------------------------------
//
//	Intf      Detection      Class   Consumed(W) Voltage(V) Current(mA) Consumed Meter(Whr) Temperature(C)
//	--------- -------------- ------- ----------- ---------- ----------- ------------------ --------------
//	0/1       Unknown        Unknown        0.00      52.32        0.00               0.00            N/A
//	0/5       Unknown        Unknown        0.00       0.00        0.00               0.00             55
//
// The reference names only the first six columns; "Consumed Meter(Whr)" is
// a firmware addition CAPTURED on the lab unit, hence the OPTIONAL group.
// "Open Circuit" is the one documented Detection value carrying a space —
// spelled out as its own alternative rather than split on whitespace, the
// same discipline this package applies everywhere a column can be blank or
// multi-word (cf. parse.go's "show port all" comment).

var rePoEStatusRow = regexp.MustCompile(
	`(?m)^\s*(\d+/\d+)\s+(Unknown|Short|Open Circuit|Good|Not Performed|Denied|Fault)\s+(\S+)\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)\s+(?:([\d.]+)\s+)?(N/A|[\d.]+)\s*$`)

// poePortStatus is one row of "show poe status all".
type poePortStatus struct {
	intf            string
	detection       string
	negotiatedClass string
	consumedW       float64
	voltageV        float64
	currentMA       float64
	temperatureC    *float64 // nil for the platform's own "N/A"
}

func parsePoEStatusAll(out string) map[string]poePortStatus {
	var m = map[string]poePortStatus{}
	var rows = rePoEStatusRow.FindAllStringSubmatch(out, -1)
	var i int
	for i = range rows {
		var ps = poePortStatus{
			intf:            rows[i][1],
			detection:       rows[i][2],
			negotiatedClass: rows[i][3],
		}
		ps.consumedW, _ = strconv.ParseFloat(rows[i][4], 64)
		ps.voltageV, _ = strconv.ParseFloat(rows[i][5], 64)
		ps.currentMA, _ = strconv.ParseFloat(rows[i][6], 64)
		var tempTok = rows[i][8]
		if tempTok != "N/A" {
			var t, err = strconv.ParseFloat(tempTok, 64)
			if err == nil {
				ps.temperatureC = &t
			}
		}
		m[rows[i][1]] = ps
	}
	return m
}

// mergePoEState combines the two tables into the shared shape, keyed by
// standard port name. A port present in EITHER table is reported — the SFP
// cages never appear in either, which is the read-only signal that they carry
// no PoE scope at all (cf. StandardPortName / the two-table faceplate split).
//
// Capable is read from Temperature(C): "N/A" on this platform means no PSE
// hardware behind the jack, a real reading means there is one (cf. the file
// header — this is an OBSERVED correlation, stated as such rather than as a
// law of the platform, in case a future revision reports it differently).
func mergePoEState(cfg map[string]poePortCfg, status map[string]poePortStatus) driver.PoEState {
	var d = &Driver{}
	var st driver.PoEState
	var seen = map[string]bool{}
	var intf string
	for intf = range cfg {
		seen[intf] = true
	}
	for intf = range status {
		seen[intf] = true
	}
	for intf = range seen {
		var std, ok = d.StandardPortName(intf)
		if !ok {
			continue // slot-3 LAG row, or an unrecognized shape
		}
		var ps = driver.PoEPortState{Port: std, Role: driver.PoERoleOut}
		var c, hasCfg = cfg[intf]
		var s, hasStatus = status[intf]
		if hasStatus {
			ps.Capable = s.temperatureC != nil
			ps.Detection = s.detection
			ps.NegotiatedClass = s.negotiatedClass
			ps.PowerW = s.consumedW
			ps.VoltageV = s.voltageV
			ps.CurrentMA = s.currentMA
			ps.TemperatureC = s.temperatureC
			ps.Delivering = s.consumedW > 0
		}
		if hasCfg {
			var class, mapped = classOfOpMode(c.opMode)
			if mapped {
				ps.Class = class
			} else {
				st.Notes = append(st.Notes, fmt.Sprintf(
					"%s: OP Mode %q was not recognized — class left unset rather than guessed",
					std, c.opMode))
			}
		}
		st.Ports = append(st.Ports, ps)
	}
	return st
}

// ReadPoE reports PoE scope, configured class, and measured draw per port.
func (c *Client) ReadPoE(ctx context.Context) (driver.PoEState, error) {
	var portOut, portErr = c.cli.run(ctx, "show poe port all", showTimeout)
	if portErr != nil && strings.TrimSpace(portOut) == "" {
		return driver.PoEState{}, fmt.Errorf("show poe port all on %s: %w", c.host, portErr)
	}
	var statusOut, statusErr = c.cli.run(ctx, "show poe status all", showTimeout)
	if statusErr != nil && strings.TrimSpace(statusOut) == "" {
		return driver.PoEState{}, fmt.Errorf("show poe status all on %s: %w", c.host, statusErr)
	}
	var st = mergePoEState(parsePoEPortAll(portOut), parsePoEStatusAll(statusOut))
	if len(st.Ports) == 0 {
		st.Notes = append(st.Notes, "no PoE-scoped port recognized: this range may carry no "+
			"power-sourcing hardware at all, or the table shape has changed")
	}
	return st, nil
}

// --- writes ---------------------------------------------------------------

// rePoERejected recognizes the two rejection forms this CLI answers with —
// CAPTURED 2026-08-05, both on ports with no PSE hardware behind them and on
// capable ports asked for a class they don't deliver (the 4-pair variants).
// Neither is caught by the shared reCLIError (ssh.go): they are this
// command's OWN vocabulary, not the generic "% Invalid input" family.
var rePoERejected = regexp.MustCompile(`(?im)^\s*(Unsupported Interfaces|Error Interfaces):\s*(\S+)\s*$`)

// setOpmode sends "poe opmode <mode>" on the CURRENTLY SELECTED interface
// and answers the confirmation a passive mode asks.
//
// Does NOT reuse cliConn.run: that function waits for rePrompt, which never
// arrives while the device is sitting at "(y/n)" — a plain command send would
// hang the full cmdTimeout on every passive class (cf. ssh.go's rePoEConfirm).
func (c *Client) setOpmode(ctx context.Context, mode string, confirmRequired bool) (string, error) {
	var sendErr = c.cli.sendLine("poe opmode " + mode)
	if sendErr != nil {
		return "", sendErr
	}
	var out, kind, err = c.cli.waitAny(ctx, cmdTimeout)
	if err != nil {
		return out, err
	}
	if kind == "confirm" {
		if err = c.cli.sendLine("y"); err != nil {
			return out, err
		}
		var out2, _, err2 = c.cli.waitAny(ctx, cmdTimeout)
		out += out2
		if err2 != nil {
			return out, err2
		}
		// CAPTURED 2026-08-05: after answering the passive-mode "(y/n)"
		// question, this CLI leaves a STALE, already-flushed COPY of the
		// interface prompt sitting unconsumed — a redraw artefact of however
		// it implements that single-keystroke read. Left in place, the very
		// NEXT command sent on this session matches on this leftover before
		// the real echo of what was just sent ever arrives, shifting every
		// following read by one command, forever. A short, best-effort
		// drain: a real device never sends anything more within it when
		// there is nothing stale to sweep up.
		c.cli.drainStalePrompt(ctx)
	} else if confirmRequired {
		// The device answered with a plain prompt instead of asking: on this
		// CLI that has only ever meant an outright rejection arriving before
		// the question ("Unsupported Interfaces"), never a silent accept.
		// Fall through — the rejection regex below is what actually reports it.
		_ = kind
	}
	return stripPager(out), nil
}

// ApplyPoE sets the PoE class on the requested ports.
//
// PROVEN command forms (cf. the file header): "poe opmode auto/passive24v/
// shutdown" in Interface Config, confirmation answered for the passive
// class, rejection recognized by [rePoERejected] rather than assumed absent.
// A class a port's scope refuses is reported in [driver.ApplyResult.Skipped],
// never as a Go error and never rounded to a class the port WILL accept.
func (c *Client) ApplyPoE(ctx context.Context, plan driver.PoEPlan) (driver.ApplyResult, error) {
	var res driver.ApplyResult
	var err = c.requireWritable()
	if err != nil {
		return res, err
	}
	if len(plan.Ports) == 0 {
		return res, nil
	}

	var before, readErr = c.ReadPoE(ctx)
	if readErr != nil {
		return res, fmt.Errorf("reading the PoE state before applying: %w", readErr)
	}
	var current = map[string]string{}
	var p driver.PoEPortState
	for _, p = range before.Ports {
		current[p.Port] = p.Class
	}

	var _, cfgErr = c.cli.run(ctx, "configure", cmdTimeout)
	if cfgErr != nil {
		return res, fmt.Errorf("entering Global Config on %s: %w", c.host, cfgErr)
	}

	var wrote []string
	var pi driver.PoEPortIntent
	for _, pi = range plan.Ports {
		var class = pi.Class
		if class == "" {
			// cf. driver.PoEPortIntent: an empty Class is the automatic
			// profile-driven path, which may only ever ask for "on".
			class = driver.PoEClass54V
		}
		if current[pi.Port] == class {
			continue // already carries the intent
		}
		var mode, confirmRequired, known = opModeOfClass(class)
		if !known {
			res.Skipped = append(res.Skipped,
				fmt.Sprintf("%s: class %q is not one this connector can set", pi.Port, class))
			continue
		}
		var _, ifErr = c.cli.run(ctx, "interface "+cliPortName(pi.Port), cmdTimeout)
		if ifErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", pi.Port, ifErr))
			continue
		}
		var out, setErr = c.setOpmode(ctx, mode, confirmRequired)
		if setErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", pi.Port, setErr))
			_, _ = c.cli.run(ctx, "exit", cmdTimeout)
			continue
		}
		var rej = rePoERejected.FindStringSubmatch(out)
		if rej != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s: refused (%s) — this jack does not deliver %q on this hardware",
				pi.Port, rej[1], class))
			_, _ = c.cli.run(ctx, "exit", cmdTimeout)
			continue
		}
		var _, exErr = c.cli.run(ctx, "exit", cmdTimeout)
		if exErr != nil {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s: %v", pi.Port, exErr))
			continue
		}
		wrote = append(wrote, pi.Port)
	}
	_, _ = c.cli.run(ctx, "exit", cmdTimeout) // back out of Global Config

	if len(wrote) == 0 {
		return res, nil
	}
	// NOT `res.PortsWritten = wrote` — that is what was sent, and the verifying
	// re-read below is what decides what was WRITTEN. The list is filled from the
	// confirmed set once that answer exists (cf. verifyPoEClasses).
	c.step(true, fmt.Sprintf("PoE class sent on %d port(s)", len(wrote)), strings.Join(wrote, " "))
	res.Notes = append(res.Notes,
		"applied to the running configuration; \"write memory confirm\" is what makes "+
			"it survive a reboot")

	var wantClass = map[string]string{}
	var pi3 driver.PoEPortIntent
	for _, pi3 = range plan.Ports {
		var wc = pi3.Class
		if wc == "" {
			wc = driver.PoEClass54V
		}
		wantClass[pi3.Port] = wc
	}

	// Verify: PoE has physical consequences on whatever is plugged in, so
	// nothing here is taken as done on the strength of a return code alone.
	// RETRIED ONCE after a brief pause: a re-read taken immediately after the
	// write can catch this CLI mid-settle on the very port just written (a
	// row missing from that one read, never from a fresh session moments
	// later — cf. the file header's live-write exercise) — a caller must not
	// be told a write failed because the read that checked it was too eager.
	var after, rerr = c.reReadPoE(ctx, poeVerifyAttempts)
	if rerr != nil {
		// The re-read could not be TAKEN. That is NOT the device declining, and
		// the two must not land in the same field: cf.
		// [driver.ApplyResult.PortsWritten]'s three non-confirmations. Reporting
		// these ports in Skipped would tell an operator meshcraft REFUSED them and
		// offer a "force" button for what is a network hiccup, while the classes
		// may well be in place on the hardware.
		res.Unconfirmed("PoE class", wrote, firstLine(rerr.Error()))
		return res, nil
	}
	var confirmed, unmatched = verifyPoEClasses(after, wrote, wantClass)
	res.PortsWritten = confirmed
	res.Skipped = append(res.Skipped, unmatched...)
	return res, nil
}

// poeVerifyAttempts: one immediate re-read, one more after a short pause.
const poeVerifyAttempts = 2

// reReadPoE re-reads the PoE state, retrying a fixed number of times with a
// short pause — cf. ApplyPoE's verification comment for why a single
// immediate read is not trusted to be the last word.
//
// # THE ERROR IS RETURNED, and that is the whole point of this signature
//
// It used to be swallowed, and the zero [driver.PoEState] handed to
// verifyPoEClasses on its own. Every comparison then came back unequal and every
// port the write had touched was reported in [driver.ApplyResult.Skipped] as
// "does not appear in "show poe port all"" — which says the DEVICE did not
// carry the class, when in fact meshcraft never got to ask. The two are opposite
// conclusions for an operator: one is a jack to walk to, the other is a session
// to retry. [driver.ApplyResult.PortsWritten] names a re-read that could not be
// TAKEN as its own case, distinct from one that disagreed, and
// [driver.ApplyResult.Unconfirmed] is what the callers here use for it.
func (c *Client) reReadPoE(ctx context.Context, attempts int) (driver.PoEState, error) {
	var st driver.PoEState
	var err error
	var i int
	for i = 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return st, fmt.Errorf("the re-read was abandoned before it could be retried: %w",
					ctx.Err())
			case <-time.After(poeVerifyPause):
			}
		}
		st, err = c.ReadPoE(ctx)
		if err == nil {
			return st, nil
		}
	}
	if err == nil {
		// attempts <= 0: nothing was read, so nothing may be reported as confirmed.
		// Returning a nil error with an empty state is the one answer this function
		// must never give — it is exactly the shape the callers used to mistake for
		// "the device lists no port".
		err = fmt.Errorf("no verifying re-read was attempted (attempts=%d)", attempts)
	}
	return driver.PoEState{}, err
}

// poeVerifyPause: how long to wait before the retried re-read.
const poeVerifyPause = 800 * time.Millisecond

// verifyPoEClasses compares what was written against what a (possibly retried)
// re-read reports. It returns the ports the re-read CONFIRMS — the only ones a
// caller may be told were written — and a phrased mismatch for each of the rest.
//
// # Why this returns the confirmed set, and does not merely phrase the failures
//
// It used to return only the mismatches, and ApplyPoE set
// `res.PortsWritten = wrote` BEFORE calling it and never pruned that list. The
// consequence was a result that contradicted itself: a port whose class read
// back wrong appeared in PortsWritten AND in Skipped, and a re-read that failed
// outright (empty state, every comparison unequal) reported EVERY port as
// written while listing every port as skipped. [driver.ApplyResult.PortsWritten]
// says "the ports actually reconfigured", and a port whose verification
// disagreed was not.
//
// That mattered more here than the same bug elsewhere in the tree, because
// "poe" IS declared in [Driver.WritesExtra]: this path is reachable by an
// operator, and PoE has physical consequences on whatever is plugged in.
//
// A port ABSENT from the re-read is a mismatch, not a confirmation — the same
// rule verifyDHCPSnoopTrust states one file over: "not observed" is not "set as
// asked", and the two lead an operator to opposite conclusions. Its message says
// so distinctly rather than reporting an empty class, because an empty class
// reads as "the device says this jack delivers nothing", which is a different
// claim from "the device did not mention this jack".
func verifyPoEClasses(after driver.PoEState, wrote []string,
	wantClass map[string]string) (confirmed []string, mismatched []string) {

	var seen = map[string]bool{}
	var seenClass = map[string]string{}
	var p driver.PoEPortState
	for _, p = range after.Ports {
		seen[p.Port] = true
		seenClass[p.Port] = p.Class
	}
	var name string
	for _, name = range wrote {
		if !seen[name] {
			mismatched = append(mismatched, fmt.Sprintf(
				"%s: class %q was sent but the port does not appear in "+
					"\"show poe port all\" afterwards — the write is NOT confirmed",
				name, wantClass[name]))
			continue
		}
		if seenClass[name] != wantClass[name] {
			mismatched = append(mismatched, fmt.Sprintf(
				"%s: sent but reads back as %q, not %q, in \"show poe port all\"",
				name, seenClass[name], wantClass[name]))
			continue
		}
		confirmed = append(confirmed, name)
	}
	return confirmed, mismatched
}

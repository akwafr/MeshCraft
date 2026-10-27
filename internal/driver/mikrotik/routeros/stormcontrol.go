package routeros

// Per-port storm control — `/interface/ethernet/switch/port`
// (driver/stormcontrol.go).
//
// # What the reference device actually serves, verbatim
//
// This is one of the few storm-control surfaces in the fleet that was OBSERVED
// rather than read out of a manual. `Capabilities().Features` has carried the
// capture since this connector was written (routeros.go):
//
//	"limit-broadcasts":"true", "limit-unknown-multicasts":"false",
//	"limit-unknown-unicasts":"false", "storm-rate":"100"
//
// Three per-class flags, and ONE rate shared by all of them.
//
// # THE SHARED RATE IS A SHAPE DIFFERENCE, not a limitation to work around
//
// `storm-rate` is a single field on the port record. There is no per-class rate
// here and no way to make one: a plan asking for broadcast at 5 and
// unknown-unicast at 20 on the same port cannot be honoured, and writing either
// number would give the operator a threshold they did not choose on the class
// they did not choose it for.
//
// So the connector REFUSES a port whose enabled classes disagree on the rate,
// names both numbers in the refusal, and writes nothing on that port.
// [driver.StormControlCaps.SharedRate] declares the shape so that a screen can
// collapse three rate boxes into one BEFORE the operator types two different
// values — a refusal earned after typing is worse than a control that never
// offered the mistake.
//
// # MULTICAST IS REFUSED, and the keyword is why
//
// The field is `limit-unknown-multicasts`. UNKNOWN multicast — the multicast this
// switch has no snooping entry for — is NARROWER than the multicast class the
// contract names. The two coincide exactly when IGMP snooping is off and diverge
// the moment it is on, which means writing this field for a
// [driver.StormMulticast] request would install a limiter whose coverage depends
// on the state of an entirely different feature.
//
// That is not a limiter an operator can reason about: they would be told
// multicast is policed, and whether it actually is would turn on a snooping
// setting nothing on the screen mentions. Registered multicast — the streams
// people actually build networks for — would pass unlimited. So the class is
// refused with [driver.RefuseStormClass], naming the field and saying which way
// it is narrow, while broadcast and unknown-unicast in the same plan are still
// written.
//
// # THE UNIT IS A PERCENTAGE. CAPTURED 2026-08-07, not inferred.
//
// This was the open question on this platform and it is now settled by the one
// experiment that could settle it: an operator set ether1 to 1 % from the
// device's own UI, and the port record came back
//
//	name="ether1" storm-rate=1 limit-unknown-unicasts=yes
//	              limit-unknown-multicasts=yes limit-broadcasts=yes
//	name="ether2" storm-rate=100 limit-unknown-unicasts=no
//	              limit-unknown-multicasts=no limit-broadcasts=yes
//
// on a CRS326-24G-2S+ running RouterOS 7.21beta3. 1 % reads back as 1; every
// untouched port sits at 100. A value of 100 packets per second on twenty-six
// ports out of the box was never coherent, and it is now excluded rather than
// merely doubted.
//
// Three further facts fell out of the same capture, and each was previously a
// deduction:
//
//  1. THE RATE IS GENUINELY SHARED. `storm-rate` is one field per port and the
//     three classes are booleans beside it, so a port cannot police broadcast at
//     1 % and unknown-unicast at 10 %. [driver.StormControlCaps.SharedRate] said
//     so from the field layout; the capture confirms the layout;
//  2. 100 IS THE DEFAULT AND MEANS NO LIMITING. That matters for a caller: a
//     plan asking for 100 % writes the value that disables the limiter, which is
//     the right behaviour and a surprising one to meet by accident;
//  3. THE FIELDS LIVE ON `/interface/ethernet/switch/port`, not on
//     `/interface/ethernet/switch/rule`. The rule table is where an operator
//     looking for "hardware switch rules" would start, and the storm-control
//     fields are not there.
//
// Any unit other than percent is REFUSED rather than converted, per the contract.
// On a platform whose own unit is unsettled, converting into it would be guessing
// twice.

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin reads and writes per-port storm control.
var (
	_ driver.StormControlReader  = (*Client)(nil)
	_ driver.StormControlApplier = (*Client)(nil)
)

const stormConnector = "mikrotik-routeros"

// The menu and its fields, exactly as the reference device serves them.
const (
	stormMenu      = "interface/ethernet/switch/port"
	stormRateField = "storm-rate"
	// stormFlag maps the classes this platform expresses AS THEMSELVES to their
	// field. Multicast is DELIBERATELY ABSENT — cf. the file header.
	fieldLimitBroadcasts = "limit-broadcasts"
	fieldLimitUnknownUni = "limit-unknown-unicasts"
	// fieldLimitUnknownMcast is named so the read can REPORT it without ever
	// mapping it onto driver.StormMulticast.
	fieldLimitUnknownMcast = "limit-unknown-multicasts"
)

var stormFlag = map[driver.StormClass]string{
	driver.StormBroadcast:      fieldLimitBroadcasts,
	driver.StormUnknownUnicast: fieldLimitUnknownUni,
}

// stormUnits: percent, on the inference the file header sets out.
var stormUnits = []driver.StormRateUnit{driver.StormRatePercent}

const multicastRefusal = "the field this platform serves is \"limit-unknown-multicasts\" — " +
	"UNKNOWN multicast, i.e. the multicast this switch has no snooping entry for. That is " +
	"NARROWER than the multicast class asked for: the two coincide only while IGMP snooping " +
	"is off, so writing it here would install a limiter whose coverage depends on the state " +
	"of a different feature, leaving registered multicast — the streams networks are actually " +
	"built for — entirely unlimited while the screen said multicast was policed. Nothing was " +
	"written for this class"

// rateUnitNote states what the rate MEANS, on every read and every apply.
//
// It used to be a caveat saying the unit was inferred. It is now a capture, and
// the note stays rather than being deleted because the number it describes is
// still surprising in one direction: 100 is the DEFAULT and means no limiting,
// so a plan asking for 100 % disables the limiter it was meant to configure.
// That is worth saying beside the value every time it is shown.
const rateUnitNote = "\"storm-rate\" is a PERCENTAGE of line rate — captured on a " +
	"CRS326-24G-2S+ (RouterOS 7.21beta3): a port set to 1 % from the device's own UI reads " +
	"back as 1, and every untouched port sits at 100. NOTE THAT 100 IS THE DEFAULT AND " +
	"MEANS NO LIMITING, so a plan asking for 100 % switches the limiter off"

// stormRateNoLimit is the value `storm-rate` carries on a port that polices
// nothing: 100 % of line rate, the factory value on every port.
//
// IT IS NOT THE SAME QUESTION AS THE CLASS BOOLEAN, and the lab CRS326 makes the
// difference concrete: on 2026-08-09 twenty-five of its twenty-seven records read
// `limit-broadcasts=true` beside `storm-rate=100` — armed at a threshold equal to
// line rate, which caps nothing. That is the factory state, not a configuration
// somebody made. Anything deciding whether a port is REALLY policed has to read
// the rate as well as the boolean; see the note on ReadStormControl.
const stormRateNoLimit = 100

// stormArmedAtNoLimit reports whether a port reads as having a class SWITCHED ON
// at a threshold that polices nothing. PURE.
//
// THE CLASS BOOLEAN ALONE DOES NOT MEAN A PORT IS POLICED, and on this hardware
// that is the ordinary case rather than an edge one: `limit-broadcasts=true`
// beside `storm-rate=100` is the FACTORY state of every port, so a pristine
// CRS326 serves twenty-five records that are armed and cap nothing.
func stormArmedAtNoLimit(limits []driver.StormLimit) bool {
	var i int
	for i = range limits {
		if limits[i].Enabled && limits[i].Rate == stormRateNoLimit {
			return true
		}
	}
	return false
}

// armedAtNoLimitNote is what an operator is told about those ports.
//
// # The presentation question this settles, and the two answers that were wrong
//
// The reading above is FAITHFUL — the device really does have the class armed —
// and it is also, on a factory unit, a screen listing twenty-five ports as
// broadcast-limited when not one of them caps anything. Two fixes were available
// and both destroy information:
//
//   - reporting Enabled FALSE would hide that the boolean is set. A later rate
//     change to 50 would then start policing a class the screen had called off,
//     with nothing in between to explain it;
//   - dropping the limit from the port's list entirely would say the class was
//     NOT READ, which is the one thing [driver.StormControlPortState.Limits]
//     reserves an absence for, and it was read perfectly well.
//
// So the observation stands unaltered and the READER carries the interpretation
// beside it, naming the ports. That keeps "armed" and "policing" as two
// facts the operator can see at once, and it is the same shape this file already
// uses for "limit-unknown-multicasts is on". A UI is then free to grey a
// limiter whose rate equals the platform's no-limit value; it does not have to,
// and nothing on screen is wrong if it does not.
//
// The SwOS sibling carries the same note for the same situation
// (swos/stormcontrol.go). The two connectors were once described as disagreeing
// about one physical situation, which was not so — they read the same way, and
// the factory state of the two products differs: SwOS ships `suni` CLEAR, so its
// pristine unit reports nothing limited, while this one ships the boolean SET.
// The case is reachable on both, and after a revert it is reachable on SwOS
// routinely, because clearing the bit there leaves the rate standing.
func armedAtNoLimitNote(ports []string) string {
	return "ARMED BUT NOT POLICING on " + strings.Join(ports, ", ") + ": the class is " +
		"switched on and \"storm-rate\" is " + strconv.Itoa(stormRateNoLimit) + ", which is " +
		"100 % of line rate and therefore no limit at all. This is the factory state of every " +
		"port on this hardware, not a configuration somebody made. The flag is reported as " +
		"read rather than hidden, because the boolean IS set and a later rate change would " +
		"start policing the class without any further gesture"

}

// StormControlCaps declares what this PLATFORM can express, without a session.
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Classes:    []driver.StormClass{driver.StormBroadcast, driver.StormUnknownUnicast},
		Units:      stormUnits,
		SharedRate: true,
		Why: []string{
			multicastRefusal + ".",
			"\"storm-rate\" is ONE field per port, shared by every class limited on it. Two " +
				"classes asking for two different rates on the same port cannot both be " +
				"honoured, so such a plan is refused rather than half written.",
			rateUnitNote + ".",
		},
	}
}

// --- read -----------------------------------------------------------------

// stormPortRow is one switch-port record, reduced to what this file needs.
type stormPortRow struct {
	ID   string
	Name string
	// Rate is `storm-rate`, and RateOK says whether the field was served at all.
	// The difference matters: a firmware that does not carry the field is not a
	// port limited at zero.
	Rate   float64
	RateOK bool
	// Flags: the three per-class booleans, by field name. Absent means the
	// firmware did not serve it.
	Flags map[string]bool
	Has   map[string]bool
}

// foldStormRows indexes the switch-port records by port NAME. PURE.
//
// The name is read from `name`, falling back to `interface`: this menu has been
// observed carrying the first, and a firmware serving the second instead would
// otherwise produce a table this connector could index by nothing at all — which
// reads on screen as "no port has storm control".
func foldStormRows(rows []Row) map[string]stormPortRow {
	var out = map[string]stormPortRow{}
	var r Row
	for _, r = range rows {
		var name = strings.TrimSpace(r.Get("name"))
		if name == "" {
			name = strings.TrimSpace(r.Get("interface"))
		}
		if name == "" {
			continue
		}
		var p = stormPortRow{
			ID:    r.ID(),
			Name:  name,
			Flags: map[string]bool{},
			Has:   map[string]bool{},
		}
		var field string
		for _, field = range []string{
			fieldLimitBroadcasts, fieldLimitUnknownUni, fieldLimitUnknownMcast,
		} {
			var raw, served = r[field]
			if !served {
				continue
			}
			p.Has[field] = true
			p.Flags[field] = raw == boolTrue
		}
		var raw, served = r[stormRateField]
		if served {
			var v, err = strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err == nil && v > 0 {
				p.Rate, p.RateOK = v, true
			}
		}
		out[name] = p
	}
	return out
}

// stormStateOf turns the folded rows into the contract state. PURE.
//
// A class whose FLAG the firmware did not serve is left OUT of the port's limit
// list — unread, not unlimited (cf. driver.StormControlPortState.Limits). A flag
// that is served and false is a genuine observation and IS reported.
func stormStateOf(rows map[string]stormPortRow) driver.StormControlState {
	var out driver.StormControlState
	var names []string
	var name string
	for name = range rows {
		names = append(names, name)
	}
	sort.Strings(names)

	var anyUnknownMcast []string
	var armedAtNoLimit []string
	for _, name = range names {
		var p = rows[name]
		var st = driver.StormControlPortState{Port: name}
		var class driver.StormClass
		for _, class = range []driver.StormClass{driver.StormBroadcast, driver.StormUnknownUnicast} {
			var field = stormFlag[class]
			if !p.Has[field] {
				continue
			}
			var l = driver.StormLimit{Class: class, Enabled: p.Flags[field]}
			if l.Enabled {
				if !p.RateOK {
					// The class is switched on and the rate cannot be read. Reporting
					// it as enabled with a rate of zero would say "every frame of this
					// class is dropped", which is a claim about a live port nobody
					// made. The class is left unread and the note below explains.
					continue
				}
				l.Rate, l.Unit = p.Rate, driver.StormRatePercent
			}
			st.Limits = append(st.Limits, l)
		}
		if p.Has[fieldLimitUnknownMcast] && p.Flags[fieldLimitUnknownMcast] {
			anyUnknownMcast = append(anyUnknownMcast, name)
		}
		if stormArmedAtNoLimit(st.Limits) {
			armedAtNoLimit = append(armedAtNoLimit, name)
		}
		out.Ports = append(out.Ports, st)
	}

	out.Notes = append(out.Notes,
		"\"storm-rate\" is ONE value per port, shared by every class limited on it: the "+
			"rate shown against each class below is the same field")
	out.Notes = append(out.Notes, rateUnitNote)
	if len(armedAtNoLimit) > 0 {
		out.Notes = append(out.Notes, armedAtNoLimitNote(armedAtNoLimit))
	}
	if len(anyUnknownMcast) > 0 {
		out.Notes = append(out.Notes,
			"\"limit-unknown-multicasts\" is ON on "+strings.Join(anyUnknownMcast, ", ")+
				". It is NOT reported as a multicast limit above, because it covers only "+
				"multicast this switch has no snooping entry for — registered groups pass "+
				"unlimited. meshcraft neither wrote it nor removes it")
	}
	return out
}

// ReadStormControl reports the per-port limiters this device carries.
func (c *Client) ReadStormControl(ctx context.Context) (driver.StormControlState, error) {
	var rows, err = c.List(ctx, stormMenu)
	if err != nil {
		return driver.StormControlState{}, fmt.Errorf("reading /%s: %w — this menu is where "+
			"per-port storm control lives on this platform, and reporting \"no limiters\" "+
			"from a failed read would be a claim about a device that answered nothing",
			stormMenu, err)
	}
	var out = stormStateOf(foldStormRows(rows))
	c.step(true, "storm control read",
		fmt.Sprintf("%d switch-port record(s)", len(out.Ports)))
	return out, nil
}

// --- write ----------------------------------------------------------------

// stormPortWrite is the resolved write for ONE port: the fields to set, or the
// refusals that stopped it.
type stormPortWrite struct {
	Fields map[string]string
	// Rate is the shared rate this write installs, when it installs one.
	Rate    float64
	SetRate bool
	Skipped []string
	// NotApplied mirrors [driver.ApplyResult.NotApplied]: structured refusals.
	NotApplied []driver.NotApplied
}

// planStormPort resolves ONE port's intent against what the device serves. PURE,
// which is what lets the shared-rate refusal be asserted without a device.
//
// THE SHARED-RATE CHECK IS THE HEART OF IT. Two enabled classes naming two
// different rates cannot both be honoured by one field, so NOTHING is written for
// that port — not the first one, not the larger one, not the one that happened to
// sort first. Writing either would hand the operator a threshold they did not
// choose on a class they did not choose it for, and the device would report it
// back as a perfectly ordinary configuration.
func planStormPort(port string, have stormPortRow,
	limits []driver.StormLimit) stormPortWrite {

	var w = stormPortWrite{Fields: map[string]string{}}
	var sorted = append([]driver.StormLimit{}, limits...)
	driver.SortStormLimits(sorted)

	var wanted []driver.StormLimit
	var i int
	for i = range sorted {
		var l = sorted[i]
		var field = stormFlag[l.Class]
		if field == "" {
			w.NotApplied = append(w.NotApplied, driver.PlatformRefusal(
				port, driver.RefuseStormClass(
					stormConnector, l.Class, port, multicastRefusal).Error()))
			continue
		}
		if !have.Has[field] {
			w.NotApplied = append(w.NotApplied, driver.PlatformRefusal(port, fmt.Sprintf(
				"this firmware's %s record does not serve \"%s\", so there is no field "+
					"to write the %s limiter into. Nothing was sent",
				stormMenu, field, l.Class)))
			continue
		}
		if l.Enabled && l.Unit != driver.StormRatePercent {
			w.NotApplied = append(w.NotApplied, driver.PlatformRefusal(
				port, driver.RefuseStormUnit(
					stormConnector, l.Class, port, l.Unit, stormUnits).Error()))
			continue
		}
		wanted = append(wanted, l)
	}

	// The shared-rate agreement, over the classes that are being ENABLED. A
	// removal names no rate and constrains nothing.
	var rate float64
	var haveRate bool
	for i = range wanted {
		if !wanted[i].Enabled {
			continue
		}
		if haveRate && wanted[i].Rate != rate {
			return stormPortWrite{Skipped: append(w.Skipped, fmt.Sprintf(
				"%s: this plan asks for two different rates on one port (%g and %g) and "+
					"\"%s\" is a SINGLE field shared by every class limited on the port. "+
					"Neither was written — picking one would give a threshold nobody chose "+
					"to a class nobody chose it for. Ask for one rate, or split the "+
					"gesture in two",
				port, rate, wanted[i].Rate, stormRateField))}
		}
		rate, haveRate = wanted[i].Rate, true
	}

	for i = range wanted {
		var field = stormFlag[wanted[i].Class]
		var want = boolFalse
		if wanted[i].Enabled {
			want = boolTrue
		}
		if have.Has[field] && have.Flags[field] == wanted[i].Enabled {
			// Already there. On a platform that persists immediately, a `set` that
			// changes nothing is a risk taken for nothing (portadmin.go's rule).
			continue
		}
		w.Fields[field] = want
	}
	if haveRate && (!have.RateOK || have.Rate != rate) {
		w.Fields[stormRateField] = strconv.FormatFloat(rate, 'f', -1, 64)
		w.Rate, w.SetRate = rate, true
	}
	return w
}

// ApplyStormControl writes the per-port limiters the plan names.
//
// One `Set` per port, with the `.id` read IMMEDIATELY BEFORE — never cached.
// RouterOS renumbers its own records across a reboot, and a stale identifier here
// does not fail: it writes a storm limiter onto a DIFFERENT port. That is
// [Client.Set]'s rule and it holds on this menu exactly as it does on `/interface`.
func (c *Client) ApplyStormControl(ctx context.Context,
	plan driver.StormControlPlan) (ApplyResult, error) {

	var out ApplyResult
	var verr = plan.Validate()
	if verr != nil {
		return out, verr
	}

	var rows, err = c.List(ctx, stormMenu)
	if err != nil {
		return out, fmt.Errorf("reading /%s before writing: %w — without the records' `.id` "+
			"no write can name the right row, and RouterOS renumbers them on every reboot",
			stormMenu, err)
	}
	var have = foldStormRows(rows)
	if len(have) == 0 {
		return out, fmt.Errorf("/%s returned no usable record: nothing was written", stormMenu)
	}

	out.Notes = append(out.Notes, rateUnitNote)

	var intents = make([]driver.StormControlPortIntent, len(plan.Ports))
	copy(intents, plan.Ports)
	sort.Slice(intents, func(i, j int) bool { return intents[i].Port < intents[j].Port })

	var attempted []string
	var wantByPort = map[string]stormPortWrite{}
	var it driver.StormControlPortIntent
	for _, it = range intents {
		var row, known = have[it.Port]
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not touched: this device serves no /%s record by that name — on this "+
					"platform a port is named as RouterOS names it (`ether3`, "+
					"`sfp-sfpplus1`), with no translation", it.Port, stormMenu))
			continue
		}
		var w = planStormPort(it.Port, row, it.Limits)
		out.Skipped = append(out.Skipped, w.Skipped...)
		out.NotApplied = append(out.NotApplied, w.NotApplied...)
		if len(w.Fields) == 0 {
			continue
		}
		var _, serr = c.Set(ctx, stormMenu, row.ID, w.Fields)
		if serr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: storm control not written — %v", it.Port, serr))
			continue
		}
		attempted = append(attempted, it.Port)
		wantByPort[it.Port] = w
		c.step(true, it.Port+" storm control written", describeStormFields(w.Fields))
	}

	if len(attempted) == 0 {
		return out, nil
	}

	// The re-read is what says the limiters landed — never the HTTP status. Cheap
	// here: one GET of the menu that was just written.
	rows, err = c.List(ctx, stormMenu)
	if err != nil {
		out.Unconfirmed("storm control", attempted, err.Error())
		return out, nil
	}
	var after = foldStormRows(rows)
	var name string
	for _, name = range attempted {
		var w = wantByPort[name]
		var got = after[name]
		var landed = true
		var field, want string
		for field, want = range w.Fields {
			if field == stormRateField {
				if !got.RateOK || got.Rate != w.Rate {
					landed = false
				}
				continue
			}
			if !got.Has[field] || got.Flags[field] != (want == boolTrue) {
				landed = false
			}
		}
		if landed {
			out.PortsWritten = append(out.PortsWritten, name)
			continue
		}
		out.Skipped = append(out.Skipped, name+
			": the device accepted the write and the re-read does not show it — a 2xx that "+
			"changed nothing is what this re-read exists to catch")
	}
	sort.Strings(out.PortsWritten)
	return out, nil
}

// describeStormFields renders a write body for a transcript line, keys SORTED so
// two identical gestures produce two identical lines.
func describeStormFields(fields map[string]string) string {
	var keys = make([]string, 0, len(fields))
	var k string
	for k = range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts = make([]string, 0, len(keys))
	for _, k = range keys {
		parts = append(parts, k+"="+fields[k])
	}
	return "{" + strings.Join(parts, " ") + "}"
}

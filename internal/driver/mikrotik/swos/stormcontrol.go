package swos

// Per-port storm control on SwOS — and, as with DHCP snooping next door,
// implementing the shared contract faithfully means REFUSING most of it.
//
// # THE ENUMERATION, which is what makes every absence below reportable
//
// The device's own configuration backup lists exactly the fields /fwd.b stores
// (testdata/backup.swb, CSS326-24G-2S+, SwOS 2.17). Three of them concern storm
// traffic, and they are quoted here verbatim from that capture:
//
//	srt:[0x64, …]      an ARRAY — one value per port, 0x64 = 100
//	suni:0x00          a MASK   — "Limit Unknown Unicast", no port set
//	fmc:0x03ffffff     a MASK   — "Flood Unknown Multicast", all 26 ports set
//
// That is the whole of it. There is NO broadcast field, and this is an
// enumeration rather than a failed search — the same standing mgmtaddr.go leans
// on to establish that SwOS has no gateway field at all, and dhcpsnoop.go to
// establish that snooping has no per-VLAN dimension here.
//
// # What this connector CAN write: unknown unicast, and only that
//
// `suni` is a per-port bit named for exactly the class the contract names, and
// `srt` is the rate beside it. Together they are a real
// [driver.StormUnknownUnicast] limiter, and this connector writes it.
//
// # BROADCAST IS REFUSED, and the tempting substitution is named
//
// `srt` exists and is a rate. It would be easy to write it for a broadcast
// request and call the job done. That would install a threshold whose COVERAGE
// this connector cannot state: nothing in the enumerated field set says which
// classes `srt` governs, and if it governs unknown unicast as well then setting
// it for broadcast silently retightens a limiter on a different class — on a port
// where `suni` may already be on. An operator would be shown "broadcast limited
// at 5 %" and the device would be doing something the screen never described.
//
// # MULTICAST IS REFUSED, and `fmc` is not even the same KIND of control
//
// `fmc` defaults to 0x03ffffff — flooding ENABLED on all twenty-six ports, which
// is ordinary switch behaviour. Clearing a bit does not rate-limit unknown
// multicast on that port; it STOPS FLOODING IT. That is a drop, not a threshold,
// and it is the difference between "this port passes multicast more slowly under
// load" and "this port passes no unregistered multicast at all". And it covers
// UNKNOWN multicast only: registered groups are unaffected either way.
//
// Writing `fmc` for a multicast storm-control request would therefore be wrong
// twice over — wrong mechanism, wrong traffic set — and the device would report
// back exactly the bit that was written.
//
// # The unit is a PERCENTAGE
//
// 0x64 = 100 on every port of a device that limits nothing is "100 % of line
// rate", i.e. no limiting, which is a coherent factory default. This is the
// evidence the RouterOS connector borrows for its own `storm-rate`
// (routeros/stormcontrol.go says so, and says it is borrowing). A rate in packets
// per second is refused rather than converted.
//
// # The write is read-modify-rewrite, and a partial page is REFUSED
//
// Same rule and same reason as ApplyPorts' and writeSysMgmt's: /fwd.b is posted
// whole, so every field in [FwdWritableFields] must come back from the read or
// the device zeroes the ones we omitted. On this page that would mean losing port
// isolation and every port's VLAN mode while setting one storm bit — and SwOS
// persists immediately, so it could not be taken back.
//
// The `GET /backup.swb` copy is taken immediately before the first post, for the
// same reason: on a platform whose capability model is `immediate`, that copy is
// the ONLY way back that exists.
//
// # STATUS: DECLARED 2026-08-08, the gesture was sent
//
// The page shape came from the device's own backup; the GESTURE has now been
// sent too, on the lab CSS326 against Port2, and [WritableExtras] carries
// "storm_control". capabilities.go holds the evidence and
// live_stormcontrol_test.go holds the exercise.
//
// Two things that exercise established, both of which live in this file:
//
//   - the re-read is matched by FACEPLATE INDEX, never by the caller's port name
//     (cf. verifyStormWrites). Matching by name made this applier disown a write
//     it had really performed;
//   - clearing `suni` does NOT restore `srt` (cf. stormRateNoLimit), so reverting
//     the bit is not reverting the page.

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
	_ driver.StormControlReader  = (*Admin)(nil)
	_ driver.StormControlApplier = (*Admin)(nil)
)

const stormConnector = "mikrotik-swos"

// The /fwd.b fields, from testdata/backup.swb.
const (
	// FieldStormRate: "Storm Rate", an ARRAY of one percentage per port.
	// 0x64 = 100 on every port of the reference device, i.e. no limiting.
	FieldStormRate = "srt"
	// FieldLimitUnknownUnicast: "Limit Unknown Unicast", a per-port MASK.
	FieldLimitUnknownUnicast = "suni"
	// FieldFloodUnknownMulticast: "Flood Unknown Multicast", a per-port MASK,
	// default all-set.
	//
	// NOT A RATE LIMITER, and never written by this connector: clearing a bit
	// stops the port flooding unregistered multicast altogether, which is a drop
	// rather than a threshold (cf. the file header).
	FieldFloodUnknownMulticast = "fmc"
)

// stormRateNoLimit is the value "srt" carries on a port that limits nothing:
// 100 % of line rate. It is the factory value on all twenty-six ports of the
// reference device (0x64 in testdata/backup.swb) and was confirmed on the lab
// CSS326 on 2026-08-08, where the twenty-five untouched ports all read 100.
//
// DISABLING DOES NOT RESET IT, and that is deliberate. Clearing `suni` leaves
// `srt` where it stood: the rate is assigned only on the enabled branch below.
// The temptation, after watching an enable/disable cycle leave 10 behind on the
// lab CSS326 on 2026-08-08, is to write this constant back on the way out so the
// page returns to its prior state. That would be wrong — `srt` is a STANDING
// per-port value that the enable bit does not own. SwOS's own interface shows the
// rate for every port whether or not the box is ticked, so an operator who set
// 10 there and then asked meshcraft to lift the limiter would find their rate
// silently reset to 100 by a gesture that never mentioned it.
//
// The consequence for anyone reverting a write: restoring the bit is not
// restoring the page. The rate has to be put back on its own, by enabling at the
// ORIGINAL value and then disabling, which is the only order that reaches `srt`.
const stormRateNoLimit = 100

// stormUnits: a percentage of line rate.
var stormUnits = []driver.StormRateUnit{driver.StormRatePercent}

const broadcastRefusal = "SwOS stores no broadcast storm field. The field set of /fwd.b is " +
	"enumerated exhaustively by the device's own configuration backup — `srt`, `suni` and " +
	"`fmc`, and nothing else — so this is an absence, not an unread value. `srt` is a rate, " +
	"but NOTHING in that field set says which classes it governs: writing it for a broadcast " +
	"request would install a threshold whose coverage meshcraft cannot state, and would " +
	"silently retighten the unknown-unicast limiter on any port where `suni` is already on. " +
	"Nothing was written for this class"

const multicastRefusal = "the only multicast field SwOS stores is `fmc`, \"Flood Unknown " +
	"Multicast\", and it is not a rate limiter at all: it defaults to ON for every port " +
	"(0x03ffffff on the reference device, i.e. ordinary switch flooding) and clearing a bit " +
	"STOPS the port flooding unregistered multicast rather than limiting it. That is a drop, " +
	"not a threshold — and it covers UNKNOWN multicast only, leaving registered groups " +
	"untouched either way. Writing it here would be wrong in both the mechanism and the " +
	"traffic set. Nothing was written for this class"

const sharedRateNote = "\"srt\" is ONE value per port. This connector writes it only " +
	"alongside the unknown-unicast bit `suni`, which is the one class SwOS names as itself"

// armedAtNoLimitNote names the ports whose `suni` bit is SET beside an `srt` of
// [stormRateNoLimit] — switched on at a threshold that polices nothing.
//
// # Why the reading is left alone and the interpretation added beside it
//
// The observation is faithful: the bit really is set, and the routeros sibling
// carries the identical note for the identical situation
// (routeros/stormcontrol.go, armedAtNoLimitNote, which has the long argument).
// Reporting Enabled false instead would hide a bit that is set, and a later rate
// change to 50 would start policing a class the screen had called off.
//
// IT IS NOT A CURIOSITY ON THIS PLATFORM, it is the state a REVERT leaves behind.
// Clearing `suni` does not reset `srt` (cf. stormRateNoLimit), so the way back
// from a limiter is to enable at 100 and then disable — and a page caught between
// those two steps, or an operator who ticked the box in SwOS's own UI without
// touching the rate, reads exactly like this.
func armedAtNoLimitNote(ports []string) string {
	return "ARMED BUT NOT POLICING on " + strings.Join(ports, ", ") + ": `suni` is set and " +
		"`srt` is " + strconv.Itoa(stormRateNoLimit) + ", i.e. 100 % of line rate, which is no " +
		"limit at all. The bit is reported as read rather than hidden — it IS set, and a later " +
		"rate change would start policing the class with no further gesture"
}

// StormControlCaps declares what this PLATFORM can express, without a session.
func (d *Driver) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Classes:    []driver.StormClass{driver.StormUnknownUnicast},
		Units:      stormUnits,
		SharedRate: true,
		Why: []string{
			broadcastRefusal + ".",
			multicastRefusal + ".",
			sharedRateNote + ".",
		},
	}
}

// --- read -----------------------------------------------------------------

// ReadStormControl reports the unknown-unicast limiter on every faceplate port.
//
// Every port is reported, and that is deliberate for the same reason
// ReadDHCPSnooping gives: `suni` is a MASK, so a port's bit is READ whether it is
// set or not. There is no such thing here as a port whose flag could not be
// determined, and the contract's "absent means unread" distinction simply has
// no ambiguous case to carry — provided the firmware serves the field at all,
// which is checked first.
func (a *Admin) ReadStormControl(ctx context.Context) (driver.StormControlState, error) {
	var out driver.StormControlState
	var fwd, err = a.Get(ctx, PageFwd)
	if err != nil {
		return out, err
	}
	if !fwd.Has(FieldLimitUnknownUnicast) {
		// The firmware does not serve the field. Reporting "nothing limited"
		// would be a fabrication: an absent field is an unknown, and this is the
		// one case where this platform CAN be ignorant.
		out.Notes = append(out.Notes, fmt.Sprintf(
			"%s does not carry \"%s\" on this firmware: the unknown-unicast storm limiter "+
				"could not be read, and is not reported as absent",
			PageFwd, FieldLimitUnknownUnicast))
		a.step(false, "storm control read", "no "+FieldLimitUnknownUnicast+" field on "+PageFwd)
		return out, nil
	}

	var limited int
	out, limited = stormStateOf(fwd, a.portNames(ctx), a.facadeFor(ctx))
	a.step(true, "storm control read",
		fmt.Sprintf("%d port(s), %d limited", len(out.Ports), limited))
	return out, nil
}

// stormStateOf turns one /fwd.b page into the contract state. PURE.
//
// SPLIT OUT OF [Admin.ReadStormControl] so the reading can be judged without an
// HTTP round trip, the way the RouterOS sibling's stormStateOf already can be.
// Everything above this line is transport and a firmware check; everything below
// is the interpretation, and the interpretation is what has to be pinned — the
// "armed but not policing" note in particular, which is a claim about what an
// operator will be told rather than about what the device said.
//
// Returns the state and the number of ports found genuinely limited, which is
// what the transcript line reports.
func stormStateOf(fwd *Page, names []string, facade Facade) (driver.StormControlState, int) {
	var out driver.StormControlState
	var limited int
	var armedAtNoLimit []string
	var name string
	for _, name = range names {
		var idx, ok = facade.Index(name)
		if !ok {
			continue
		}
		var on, readOK = fwd.Bit(FieldLimitUnknownUnicast, idx)
		if !readOK {
			continue
		}
		var l = driver.StormLimit{Class: driver.StormUnknownUnicast, Enabled: on}
		if on {
			var raw, rateOK = fwd.NumAt(FieldStormRate, idx)
			if !rateOK {
				// Switched on with no readable rate. Reporting a rate of zero would
				// say "every unknown-unicast frame is dropped" about a live port,
				// which is a claim nobody made — so the class is left unread.
				continue
			}
			limited++
			l.Rate, l.Unit = float64(raw), driver.StormRatePercent
			if raw == stormRateNoLimit {
				// Set beside a rate of 100 %, i.e. armed and policing nothing. The
				// reading is NOT altered — see armedAtNoLimitNote for why hiding it
				// would be worse — and `limited` still counts it, because the bit is
				// what the count is about.
				armedAtNoLimit = append(armedAtNoLimit, name)
			}
		}
		out.Ports = append(out.Ports, driver.StormControlPortState{
			Port: name, Limits: []driver.StormLimit{l},
		})
	}

	out.Notes = append(out.Notes,
		"UNKNOWN UNICAST is the only storm class SwOS names as itself. Broadcast and "+
			"multicast are absent from every port above because the platform stores no field "+
			"for them, not because they read as unlimited")
	out.Notes = append(out.Notes, sharedRateNote)
	if len(armedAtNoLimit) > 0 {
		out.Notes = append(out.Notes, armedAtNoLimitNote(armedAtNoLimit))
	}
	return out, limited
}

// --- write ----------------------------------------------------------------

// stormWrite is one port the applier actually changed on the page: the name the
// CALLER used, the faceplate index it resolved to, and what was asked for.
//
// The two identities are kept apart on purpose. The index is what the re-read is
// matched on, because it is the same whichever naming family the plan used; the
// name is what every message quotes back, because an operator who asked for
// "GE2" should not have to work out that "Port2" is the same socket.
type stormWrite struct {
	Name string
	Idx  int
	Want driver.StormLimit
}

// verifyStormWrites decides which of the ports just posted actually read back the
// way the plan asked, and phrases the disagreements.
//
// MATCHED BY FACEPLATE INDEX, NEVER BY THE CALLER'S SPELLING, and that is the
// whole reason this is a function rather than a loop inside ApplyStormControl.
// [Facade.Index] accepts two naming families — the device's own "Port2"/"SFP1"
// and meshcraft's numbered "GE2" — while [Facade.Name] only ever emits the
// first. Keying the comparison by name therefore made a plan written in the
// legacy family match nothing: on the lab CSS326 on 2026-08-08 a "GE2" plan set
// the bit on Port2, the re-read showed Port2 policing, and the applier answered
// "the device did not take it" with an empty PortsWritten. It disowned a write
// it had really performed, and a caller reverting on that answer would leave the
// limiter standing. The index is the one identity both families agree on.
//
// The bug survived a unit-tested package because a fixture naturally uses ONE
// family on both sides of the comparison; only a device answers in a family the
// plan did not use. Hence [TestVerifyStormWritesAcceptsEitherNamingFamily].
func verifyStormWrites(after driver.StormControlState, facade Facade,
	wrote []stormWrite) (confirmed, missed []string) {

	var observedBy = map[int]driver.StormControlPortState{}
	var i int
	for i = range after.Ports {
		var idx, ok = facade.Index(after.Ports[i].Port)
		if !ok {
			continue
		}
		observedBy[idx] = after.Ports[i]
	}
	var w stormWrite
	for _, w = range wrote {
		var got, read = observedBy[w.Idx].Limit(driver.StormUnknownUnicast)
		if read && got.Enabled == w.Want.Enabled && (!w.Want.Enabled || got.Rate == w.Want.Rate) {
			confirmed = append(confirmed, w.Name)
			continue
		}
		missed = append(missed, fmt.Sprintf(
			"%s: the page was accepted and the re-read does not show \"%s\" — the device "+
				"did not take it", w.Name, w.Want.Describe()))
	}
	return confirmed, missed
}

func stormWriteNames(ws []stormWrite) []string {
	var out = make([]string, 0, len(ws))
	var w stormWrite
	for _, w = range ws {
		out = append(out, w.Name)
	}
	return out
}

// stormPortPlan is what ONE port's intent resolves to, before the page is
// touched. PURE, so the refusals are asserted without a device.
type stormPortPlan struct {
	// Want is the unknown-unicast limiter to install, when there is one.
	Want    driver.StormLimit
	HasWant bool
	Skipped []string
	// NotApplied mirrors [driver.ApplyResult.NotApplied]: structured refusals.
	NotApplied []driver.NotApplied
}

// planStormPort resolves one intent into the single class this platform carries,
// refusing the other two by name.
func planStormPort(port string, limits []driver.StormLimit) stormPortPlan {
	var p stormPortPlan
	var sorted = append([]driver.StormLimit{}, limits...)
	driver.SortStormLimits(sorted)

	var i int
	for i = range sorted {
		var l = sorted[i]
		switch l.Class {
		case driver.StormBroadcast:
			p.NotApplied = append(p.NotApplied, driver.PlatformRefusal(
				port, driver.RefuseStormClass(
					stormConnector, l.Class, port, broadcastRefusal).Error()))
		case driver.StormMulticast:
			p.NotApplied = append(p.NotApplied, driver.PlatformRefusal(
				port, driver.RefuseStormClass(
					stormConnector, l.Class, port, multicastRefusal).Error()))
		case driver.StormUnknownUnicast:
			if l.Enabled && l.Unit != driver.StormRatePercent {
				p.NotApplied = append(p.NotApplied, driver.PlatformRefusal(
					port, driver.RefuseStormUnit(
						stormConnector, l.Class, port, l.Unit, stormUnits).Error()))
				continue
			}
			if l.Enabled && (l.Rate < 1 || l.Rate > 100 || l.Rate != float64(int(l.Rate))) {
				// `srt` is a whole percentage in the device's own encoding. Rounding
				// 5.5 to 5 or 6 would be the connector choosing a threshold the
				// operator did not type.
				p.Skipped = append(p.Skipped, fmt.Sprintf(
					"%s: \"%s\" stores a WHOLE percentage from 1 to 100 and the plan asks "+
						"for %g. Nothing was rounded and nothing was written",
					port, FieldStormRate, l.Rate))
				continue
			}
			p.Want, p.HasWant = l, true
		}
	}
	return p
}

// ApplyStormControl sets the unknown-unicast limiter on the ports named, and
// nothing else.
//
// Ports absent from the plan keep their bit and their rate, because the page is
// read before it is painted — the contract's "a port absent is NOT MANAGED"
// rule satisfied by construction, exactly as in ApplyDHCPSnooping.
func (a *Admin) ApplyStormControl(ctx context.Context,
	plan driver.StormControlPlan) (driver.ApplyResult, error) {

	var out driver.ApplyResult
	var verr = plan.Validate()
	if verr != nil {
		return out, verr
	}
	out.Notes = append(out.Notes, sharedRateNote)

	var fwd, err = a.Get(ctx, PageFwd)
	if err != nil {
		return out, err
	}
	if !fwd.Has(FieldLimitUnknownUnicast) || !fwd.Has(FieldStormRate) {
		var e = fmt.Errorf("%s does not carry \"%s\" and \"%s\" on this firmware: the "+
			"unknown-unicast storm limiter cannot be written",
			PageFwd, FieldLimitUnknownUnicast, FieldStormRate)
		a.step(false, "storm control write refused", e.Error())
		return out, e
	}

	// The backup is taken BEFORE the first write and is the only way back on this
	// platform: SwOS applies and persists in one gesture, so a power outage undoes
	// nothing (cf. driver.ApplyResult.Backup). Its failure does NOT block the
	// write — refusing a storm limit for want of a safety net would be excessive —
	// but it is logged, because the operator must know they are working without
	// one. Same handling as apply.go's and dhcpsnoop.go's.
	var backupErr error
	out.Backup, backupErr = a.Backup(ctx)
	if backupErr != nil {
		a.step(false, "NO backup available before write",
			"the run continues, but with no possible way back: "+backupErr.Error())
	}

	var facade = a.facadeFor(ctx)
	var rates, ratesOK = fwd.Nums(FieldStormRate)
	if !ratesOK {
		var e = fmt.Errorf("%s serves \"%s\" in a shape this connector cannot read as a "+
			"per-port array: write refused rather than guessed", PageFwd, FieldStormRate)
		a.step(false, "storm control write refused", e.Error())
		return out, e
	}

	var wrote []stormWrite
	var changed bool
	var it driver.StormControlPortIntent
	for _, it = range plan.Ports {
		var p = planStormPort(it.Port, it.Limits)
		out.Skipped = append(out.Skipped, p.Skipped...)
		out.NotApplied = append(out.NotApplied, p.NotApplied...)
		if !p.HasWant {
			continue
		}
		var idx, ok = facade.Index(it.Port)
		if !ok {
			// A name the faceplate does not carry is REFUSED, never guessed: an
			// invented index writes to the wrong port, and on this platform that is
			// the one that cuts the uplink.
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: unknown port on this device, storm limiter not written", it.Port))
			continue
		}
		if idx >= len(rates) {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: \"%s\" carries %d value(s) and this port is index %d — the served "+
					"array is shorter than the faceplate, so nothing was written",
				it.Port, FieldStormRate, len(rates), idx))
			continue
		}

		var current, readOK = fwd.Bit(FieldLimitUnknownUnicast, idx)
		var sameRate = !p.Want.Enabled || float64(rates[idx]) == p.Want.Rate
		if readOK && current == p.Want.Enabled && sameRate {
			// Already conformant: sending nothing beats a round trip that changes
			// nothing, and on a platform that persists immediately it also beats a
			// risk taken for nothing.
			continue
		}
		if !fwd.SetBit(FieldLimitUnknownUnicast, idx, p.Want.Enabled) {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s: \"%s\" bit %d not settable on the served %s page",
				it.Port, FieldLimitUnknownUnicast, idx, PageFwd))
			continue
		}
		if p.Want.Enabled {
			rates[idx] = uint32(p.Want.Rate)
		}
		changed = true
		wrote = append(wrote, stormWrite{Name: it.Port, Idx: idx, Want: p.Want})
	}
	if !changed {
		a.step(true, "storm control already conformant", "no bit to change")
		return out, nil
	}
	fwd.SetNums(FieldStormRate, rates)

	// The page is posted WHOLE, and a firmware serving an unexpected field set
	// gets a refusal rather than a best-effort write: omitting a field lets the
	// device zero it, and on /fwd.b that means losing port isolation and every
	// port's VLAN mode while setting one storm bit.
	var body *Page
	var missing []string
	body, missing = fwd.Only(FwdWritableFields(facade.Ports()))
	if len(missing) > 0 {
		var e = fmt.Errorf("%s: this firmware does not serve the fields %v expected by the "+
			"connector — write refused; posting without them would let the device zero "+
			"them, and SwOS persists immediately", PageFwd, missing)
		a.step(false, "unexpected /fwd.b field set", e.Error())
		return out, e
	}
	if err = a.postRaw(ctx, PageFwd, body.Encode(),
		"unknown-unicast storm limiter written on "+PageFwd); err != nil {
		return out, err
	}

	// Verified by RE-READING, not by the POST's return: a 2xx that changed nothing
	// is the failure worth catching, and on a platform whose whole UI is a handful
	// of pages the re-read costs one request.
	var after, rerr = a.ReadStormControl(ctx)
	if rerr != nil {
		out.Unconfirmed("storm control", stormWriteNames(wrote), rerr.Error())
		return out, nil
	}
	var confirmed, missed = verifyStormWrites(after, facade, wrote)
	out.Skipped = append(out.Skipped, missed...)
	sort.Strings(confirmed)
	out.PortsWritten = confirmed
	a.step(true, "storm control written",
		fmt.Sprintf("%d port(s) confirmed", len(confirmed)))
	return out, nil
}

// stormRateText renders a rate for a transcript line, in the whole-percent form
// the device stores.
func stormRateText(rate float64) string { return strconv.Itoa(int(rate)) + " %" }

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// Per-port storm control over HTTP: one endpoint to READ what a device carries,
// one to WRITE it. Sibling of dhcpsnoop_apply.go and portadmin_apply.go, and
// shaped like them where it can be.
//
// # THE UNIT TRAVELS WITH EVERY RATE, AND A BODY WITHOUT ONE IS A 400
//
// internal/driver/stormcontrol.go argues this at length and this file only
// enforces it at the boundary: "10" means ten percent of a gigabit on one
// switch in a rack and ten packets a second on the one beside it, and no
// connector converts between the two because none of the conversions can be done
// honestly. So the wire format is
//
//	{ "ports": { "GE5": [ {"class":"broadcast","enabled":true,
//	                       "rate":5,"unit":"percent"} ] } }
//
// and a limit that arrives enabled with no `unit` is refused here, before a
// session is opened. Refusing at the boundary rather than at the connector is
// what makes the message actionable: the operator gets "the unit is mandatory"
// with the field named, instead of a 502 from a device that was never the
// problem.
//
// A `rate` of 0 on an enabled limit is refused for a heavier reason, and
// driver.StormLimit.Validate carries it: on this fleet a threshold of zero DROPS
// EVERY FRAME of the class, and on broadcast that takes ARP with it. Removing a
// limiter is `enabled: false`, and the two spellings must never be confused.
//
// # WHY THE READ IS LIVE AND NOTHING IS STORED
//
// No observation is persisted under `devices.extra` the way DHCP snooping's is,
// and that is deliberate rather than an omission. A stored observation earns its
// keep when a PANEL repaints from it on every poll — the trust flag next to a
// port, the PoE class in a faceplate tooltip. Storm control is read when an
// operator opens the control and not otherwise, so a stored copy would only ever
// be a snapshot old enough to disagree with the device at the moment somebody
// looked at it. The GET reaches the switch, which costs a session and says so.
//
// # THE INTENT, BY CONTRAST, IS STORED — AND IT IS REPLAYED
//
// This header used to say that nothing was stored as intent and that no
// reconciliation pass set storm limiters. Both halves are now false, and the
// contradiction lived in this very file: the intent is written into
// `devices.ports` at `ports[].security.storm_control` by whatever set it — a port
// profile (portrules.Config.StormControl, which merges it under `security`) or
// the live gesture below — it is read back by [portStormIntents], and
// [Server.applyDeviceStormControl] replays it from an automatic pass
// (autoapply.go). So the split this endpoint maintains is the same one
// poe_apply.go and dhcpsnoop_apply.go maintain: the handler carries the WIRE
// half, the profile carries the stored half, and both end up in one
// driver.StormControlPlan rather than two descriptions of one port.
//
// THE FAILURE MODE THAT WORRIED THE ORIGINAL NOTE — a pass re-imposing a
// threshold an operator removed by hand — IS DECIDED, EXPLICITLY, AND IN THE
// OTHER DIRECTION. A port whose profile declares no `storm_control` block is
// SKIPPED and never turned into a "disabled" intent, so an automatic apply cannot
// strip a limiter meshcraft was not asked about. What a profile DOES declare it
// re-imposes, including its switched-OFF classes, because that is what declaring
// it means. [portStormIntents] carries the rule and the reason at the point where
// the stripping would otherwise happen.

// isUnsupported tells a PLATFORM refusal from a device that misbehaved.
//
// The two deserve different HTTP codes and different words on screen: 422 means
// "stop looking, this hardware does not do that", 502 means "the switch did
// not answer, try again". driver.Unsupported attaches itself to
// driver.ErrNotImplemented precisely so the two can be told apart in one test
// (cf. driver/connector.go, Unsupported.Unwrap).
func isUnsupported(err error) bool { return errors.Is(err, driver.ErrNotImplemented) }

// stormLimitBody is one class's limit as it arrives on the wire.
//
// A STRUCT AND NOT A BARE NUMBER, which is the whole point: there is no shape
// here in which a caller can send a threshold without saying what it means.
type stormLimitBody struct {
	Class   string  `json:"class"`
	Enabled bool    `json:"enabled"`
	Rate    float64 `json:"rate"`
	Unit    string  `json:"unit"`
}

// --- THE REFUSALS, JUDGED APART FROM THE HANDLER ---
//
// LIFTED OUT ON 2026-08-09, last of the three. The two siblings record the
// reasoning in full (dhcpsnoop_apply.go and poe_apply.go, same heading); the
// short version is that internal/api tests run without PostgreSQL because
// Server.Store is a concrete *store.Store, so nothing beginning with
// deviceForWrite can be driven from a test. Judged inline — as everything below
// the body was here — these status codes were unassertable and free to drift,
// and 400 / 422 / 502 are three different instructions to the operator (fix your
// request / stop looking, this hardware will not do it / the device said no, try
// again).
//
// STORM CONTROL EARNS IT WITH ONE RUNG THE SIBLINGS DO NOT HAVE: a connector's
// own failure is GRADED, because it may answer driver.ErrNotImplemented for a
// gesture the platform cannot express at all. 422 and 502 come out of the same
// error value here, and that judgement was the least visible of the lot.
//
// AND THE LADDER SERVES BOTH VERBS, since 2026-08-09. The GET judged its status
// codes inline for a day longer than the POST did — the same 502 on a failed dial
// and the same graded 422/502 on the connector's answer, written out a second time
// in a handler no test can enter. [stormGradedRefusal] argues at length why the
// read's grading is the WRITE's judgement rather than one that merely looks like
// it, and [stormReaderFor] carries the one rung that belongs to the read alone.

// stormRefusal is the answer given INSTEAD of writing: an HTTP status and the
// sentence that goes in it. The zero value means "nothing to refuse".
//
// A THIRD TYPE, distinct from dhcpSnoopRefusal and poeRefusal, and for the reason
// poe_apply.go states where it declines to share with the first: the ladders
// answer for different features and their rungs are not interchangeable. This one
// has a class/rate/UNIT vocabulary neither sibling has, a platform refusal whose
// sentence is assembled from the capability view, and the graded write above. A
// shared type would invite a shared judgement function whose message could only
// be generic — and generic is exactly what these sentences must not be. What is
// shared is the SHAPE, which is what makes the three readable side by side.
type stormRefusal struct {
	Code int
	Msg  string
}

// respond writes the refusal and says whether it wrote one, so each rung reads
// `if refusal.respond(w) { return }`.
//
// A ZERO REFUSAL WRITES NOTHING and returns false — the property that makes the
// shape safe: a rung that must not refuse leaves the response untouched rather
// than committing a 0-status header that would freeze everything after it.
//
// TWO RUNGS DO NOT GO THROUGH HERE, one per verb, and for the same reason: they
// answer with a body this helper cannot produce. The write's own failure carries
// the partial ApplyResult and the transcript beside the sentence, because a
// connector that refused one class and wrote another names both; the read's
// carries the capability view and the transcript, so a panel refused a STATE can
// still draw the control. Both take their CODE and their SENTENCE from
// [stormGradedRefusal], so the judgement is pinned even where the shape is not.
func (ref stormRefusal) respond(w http.ResponseWriter) bool {
	if ref.Code == 0 {
		return false
	}
	respondErr(w, ref.Code, ref.Msg)
	return true
}

// stormPlatformRefusal judges what can be judged WITHOUT reaching the device: is
// there a connector, and does it declare this write?
//
// 422 for all three, with distinct sentences because they are repaired by
// unrelated gestures — re-run discovery, ship a build that carries the connector,
// or wait for a capture on this range. The connector's own sentence is carried
// VERBATIM: driver.ConnectorFor writes three different explanations and each
// names its repair, which a message rewritten here would throw away.
//
// A nil connector with no error is refused rather than dereferenced. That is the
// one behaviour this extraction changes, and it changes a PANIC into an answer:
// the handler called conn.WritesExtra straight after testing only the error, so a
// nil-with-no-error would have been a 500 out of a handler whose whole job is to
// produce a status code.
//
// THE LAST RUNG NAMES BOTH FACTS because the operator's next move depends on
// which applies: a platform that expresses the classes but has no proven command
// form is waiting on a capture, and one that expresses none is waiting on
// nothing.
//
// This comment used to end "no connector in this codebase declares
// storm_control writable … so this 422 is, today, the answer the endpoint gives
// on the whole fleet". THAT STOPPED BEING TRUE ON 2026-08-08/09 and the prose
// did not follow: SIX of the nine ranges now declare it on the strength of a
// live exercise through their own applier — arista/eos, cisco/ios, fs/s3400,
// ubiquiti/edgeswitch, mikrotik/swos, mikrotik/routeros. The three that do not
// are blocked rather than pending (see internal/driver/stormcontrol.go). So this
// rung is now the MINORITY answer, not the fleet-wide one, and a reader sizing
// its importance from the old sentence would size it wrong.
func stormPlatformRefusal(conn driver.Connector, connErr error,
	caps StormControlCapsView) stormRefusal {
	if connErr != nil {
		return stormRefusal{Code: http.StatusUnprocessableEntity, Msg: connErr.Error()}
	}
	if conn == nil {
		return stormRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "no connector is associated with this device"}
	}
	if conn.WritesExtra("storm_control") {
		return stormRefusal{}
	}
	var msg = "this platform does not declare per-port storm control as writable"
	if len(caps.Classes) > 0 {
		msg += ": the hardware carries the limiter, but meshcraft has not yet run " +
			"the command form on this range, so the first exercise must not be a " +
			"customer's switch"
	} else if len(caps.Why) > 0 {
		msg += " — " + caps.Why[0]
	}
	return stormRefusal{Code: http.StatusUnprocessableEntity, Msg: msg}
}

// stormReachRefusal turns a failure on the way TO the device into 502.
//
// Deliberately NOT graded by isUnsupported, unlike stormGradedRefusal below, and
// the asymmetry is the judgement rather than an oversight: this error comes from
// Server.adminFor, which dials, authenticates and knows nothing about storm
// control. It cannot be a capability refusal, so reading one out of it would let
// an unrelated wrapped error tell an operator their hardware is incapable.
//
// BOTH VERBS DIAL, and both dial through this: the GET spelled the same 502 out
// inline until 2026-08-09. A device that cannot be reached is one event whichever
// gesture was about to be attempted on it, and one function is what keeps it one
// answer.
//
// 502 and not 500: nothing in the controller is broken, an upstream did not
// cooperate, and the operator's next move is to look at the switch.
func stormReachRefusal(err error) stormRefusal {
	if err == nil {
		return stormRefusal{}
	}
	return stormRefusal{Code: http.StatusBadGateway, Msg: err.Error()}
}

// stormGradedRefusal grades what a CONNECTOR'S OWN storm-control method came back
// with — the write's, and equally the read's: 422 when it says the platform cannot
// express the gesture, 502 for everything else.
//
// THE RUNG NEITHER SIBLING HAS. driver.Unsupported attaches itself to
// driver.ErrNotImplemented precisely so the two can be told apart here (cf.
// isUnsupported), and they must be: "this range has one device-wide storm
// control and no per-interface dimension" is a permanent fact the operator
// should stop retrying, while "the session dropped" is one they should retry
// immediately. Collapsing the pair either way sends them off in the wrong
// direction — hunting a network fault that does not exist, or waiting for a
// capability that will never arrive.
//
// # ONE FUNCTION FOR THE READ AND THE WRITE, and it was stormWriteRefusal until 2026-08-09
//
// handleReadDeviceStormControl graded its own read inline, in five lines that were
// this function spelled out a second time, and the temptation on lifting them was
// to give the read a ladder of its own — on the grounds that a state a platform
// cannot REPORT is not the same event as a limiter it refuses to WRITE. That is
// true of the two events and FALSE OF THE JUDGEMENT, which is all this function
// makes. The input is one error value from one connector; the marker read out of
// it is the same driver.ErrNotImplemented; the codes are the same 422 and 502
// meaning the same two instructions; and the sentence is the connector's own,
// carried verbatim in both directions. Nothing is left for a second function to
// decide differently, so a second function could only be a copy — and a copy of a
// grading is a grading that drifts, which is the whole reason these rungs were
// lifted out of the handlers in the first place.
//
// THE TWO SENTENCES DO DIFFER. They simply do not differ HERE. "this range has no
// per-interface dimension to write" and "this firmware serves no per-port storm
// control to read" are two different sentences because two different connector
// METHODS wrote them, which is exactly where the difference belongs: only the
// connector knows what it just tried. A read-flavoured copy of this ladder could
// not improve on either without rewriting it, and a message rewritten at this
// boundary — throwing away the repair the connector named — is the one failure
// mode every rung in this file exists to avoid.
//
// WHAT STAYS APART is what genuinely differs, and it is not this. stormReachRefusal
// above is ungraded, because its error comes from a dial that knows nothing about
// storm control. stormReaderFor below has a rung the write cannot have, because a
// connector that writes and cannot read back is answered 200 rather than refused.
// And the two callers assemble their own BODIES — result and transcript for the
// write, capability view and transcript for the read — taking only the code and
// the sentence from here.
func stormGradedRefusal(err error) stormRefusal {
	if err == nil {
		return stormRefusal{}
	}
	if isUnsupported(err) {
		return stormRefusal{Code: http.StatusUnprocessableEntity, Msg: err.Error()}
	}
	return stormRefusal{Code: http.StatusBadGateway, Msg: err.Error()}
}

// stormApplierFor turns proven access into the applier to write through, or the
// refusal to answer with.
//
// THE ERROR IS TESTED FIRST, and that ordering is the judgement: adminFor may
// return an Admin AND an error at once — Instant On and RouterOS hand back the
// session on failure so the transcript survives (cf. Server.adminFor). Asserting
// the interface first would find a perfectly good applier on unproven access and
// answer 422 "this platform cannot do that" about a device that merely refused
// the password.
//
// # THE NIL SESSION IS 502, AND IT USED TO BE THE 422 BELOW — decided 2026-08-09
//
// A nil Admin with NO error is not a staging fault, and answering the staging
// sentence for it was the file's last surviving guess: nothing was ever asserted
// against the connector, so "its Admin has no ApplyStormControl" names a cause
// no line of code observed. A status code may report what happened; it must not
// invent why. What is true of a nil session is that the device was not reached,
// and 502 says exactly that and nothing more — the same argument stormReachRefusal
// makes about grading a dial, and the same one stormGradedRefusal makes about
// keeping the connector's own words instead of a sentence rewritten here.
//
// The read reached this conclusion first (cf. stormReaderFor) and the two now
// answer the same rung with the same sentence, differing in the VERB alone —
// "nothing can be written without one" against "nothing can be read without
// one" — because that is the only thing that differs between the two events.
//
// The 422 that follows is a STAGING mismatch, not a hardware limit, and it stays:
// a NON-nil Admin, behind a connector that declared "storm_control" writable,
// which does not implement ApplyStormControl. There the assertion really was made
// and really did fail, so the sentence reports an observation — and the operator's
// next move really is to wire the applier. Same sentence as the automatic path's
// note, deliberately: the two paths meet the same half-built connector and must
// say the same thing.
func stormApplierFor(adm driver.Admin, admErr error) (driver.StormControlApplier, stormRefusal) {
	var refusal = stormReachRefusal(admErr)
	if refusal.Code != 0 {
		return nil, refusal
	}
	if adm == nil {
		return nil, stormRefusal{Code: http.StatusBadGateway,
			Msg: "no session came back from this device, and nothing can be written without one"}
	}
	var applier, ok = driver.AsStormControlApplier(adm)
	if !ok {
		return nil, stormRefusal{Code: http.StatusUnprocessableEntity,
			Msg: "connector declares storm control writable but Admin has no ApplyStormControl"}
	}
	return applier, stormRefusal{}
}

// stormReaderFor turns proven access into the reader to read THROUGH, or the
// refusal to answer with — or neither.
//
// # THREE OUTCOMES OUT OF TWO RETURN VALUES, and the third is why this is not stormApplierFor's twin
//
//   - a reader and a zero refusal: read the device;
//   - no reader and a refusal: answer it;
//   - NO READER AND A ZERO REFUSAL: a connector that writes storm control and
//     cannot read it back. That is a legitimate staging point rather than a fault —
//     the two interfaces are independent (cf. driver.StormControlReader) — and the
//     handler answers 200 with the capability view so the panel can still offer the
//     control. The write has no equivalent case: an Admin with no ApplyStormControl
//     behind a connector that DECLARED the write is a half-built connector, and
//     stormApplierFor is right to call that 422.
//
// THE ERROR IS TESTED FIRST, for the reason stormApplierFor states in full:
// adminFor may return an Admin AND an error at once, because Instant On and
// RouterOS hand the session back on failure so the transcript survives. Asserting
// the interface first would find a perfectly good reader on access that was never
// proven.
//
// # THE NIL SESSION IS REFUSED, and that is the one behaviour this extraction changes
//
// The GET carried the same shape the apply handler carried — it tested the error
// and never the Admin — but its consequence was quieter than the apply's panic and
// worse to read. driver.AsStormControlReader(nil) is a safe assertion that simply
// answers false, so a nil session with no error fell straight into the 200 above
// and told the operator "this connector writes storm control but cannot read it
// back": a capability sentence, about a device no session was ever opened on,
// under a success code. 502 instead, because nothing in the controller is broken,
// an upstream did not cooperate, and there is nothing to look at here and
// everything to look at on the switch.
//
// THE WRITE ANSWERS THE SAME THING SINCE 2026-08-09, in the same words with the
// verb changed: it used to name a staging fault on a session that never existed
// (cf. stormApplierFor, which carries the argument).
func stormReaderFor(adm driver.Admin, admErr error) (driver.StormControlReader, stormRefusal) {
	var refusal = stormReachRefusal(admErr)
	if refusal.Code != 0 {
		return nil, refusal
	}
	if adm == nil {
		return nil, stormRefusal{Code: http.StatusBadGateway,
			Msg: "no session came back from this device, and nothing can be read without one"}
	}
	var reader, ok = driver.AsStormControlReader(adm)
	if !ok {
		return nil, stormRefusal{}
	}
	return reader, stormRefusal{}
}

// stormIntents turns the request body's port map into the plan's intents, or
// returns the 400 to answer with.
//
// SEPARATE FROM THE HANDLER because it is where the body is JUDGED, and the
// judgements are worth pinning without a database — the same reasoning
// dhcpSnoopIntents carries. It delegates every per-limit rule to
// driver.StormControlPlan.Validate so that the boundary and the connectors cannot
// drift into two different opinions about what a valid threshold is.
// `speeds` maps port name → line rate in Mbit/s, read from the device's own
// faceplate. It is what lets a platform whose field is packets per second honour
// a PERCENTAGE — see driver.StormControlPortIntent.SpeedMbps. A port missing from
// the map keeps zero, which those connectors read as "unknown" and refuse rather
// than assume, so a faceplate this layer could not parse degrades into a visible
// refusal instead of a limiter computed from a guess.
//
// It returns a stormRefusal rather than a bare sentence so that the CODE travels
// with the reason: a malformed body is 400 wherever it is judged, and that is now
// pinned here instead of in a handler no test can enter.
func stormIntents(ports map[string][]stormLimitBody,
	speeds map[string]int) ([]driver.StormControlPortIntent, stormRefusal) {
	if len(ports) == 0 {
		return nil, stormRefusal{Code: http.StatusBadRequest, Msg: "no ports in request"}
	}
	var out = make([]driver.StormControlPortIntent, 0, len(ports))
	var name string
	var limits []stormLimitBody
	for name, limits = range ports {
		if strings.TrimSpace(name) == "" {
			return nil, stormRefusal{Code: http.StatusBadRequest,
				Msg: "a port with an empty name cannot carry a storm limiter"}
		}
		var intent = driver.StormControlPortIntent{Port: name, SpeedMbps: speeds[name]}
		var i int
		for i = range limits {
			intent.Limits = append(intent.Limits, driver.StormLimit{
				Class:   driver.StormClass(limits[i].Class),
				Enabled: limits[i].Enabled,
				Rate:    limits[i].Rate,
				Unit:    driver.StormRateUnit(limits[i].Unit),
			})
		}
		out = append(out, intent)
	}
	// The order is STABLE so two identical gestures produce two identical
	// transcripts. Lexical is enough here: every connector re-sorts into its own
	// faceplate order before writing, because only the connector knows what the
	// panel looks like.
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })

	var plan = driver.StormControlPlan{Ports: out}
	var err = plan.Validate()
	if err != nil {
		return nil, stormRefusal{Code: http.StatusBadRequest, Msg: err.Error()}
	}
	return out, stormRefusal{}
}

// StormControlCapsView is what the frontend needs to draw the control BEFORE
// anything is clicked.
//
// # Two flags, two different questions, and merging them was a real defect
//
// `Classes` answers "can this hardware do it at all" and `Write` answers "has
// meshcraft proven the command form on this range". A screen with only the
// second greys out three controls on a switch that supports all three and says
// nothing useful about why; a screen with only the first offers a button that can
// only earn a 422. The PoE power-cycle button spent a while in exactly that state
// (cf. PortActionCapsView) and this shape exists so storm control does not repeat
// it.
type StormControlCapsView struct {
	// Known: the connector was resolved at all. False means "we cannot tell",
	// which is not "the platform says no".
	Known bool `json:"known"`
	// Classes: the traffic classes the PLATFORM expresses as themselves. A class
	// absent is one the hardware cannot do — permanently, whatever a later
	// capture establishes.
	Classes []driver.StormClass `json:"classes,omitempty"`
	// Units: the rate units the connector accepts without converting.
	Units []driver.StormRateUnit `json:"units,omitempty"`
	// SharedRate: the platform stores ONE rate per port, shared by every class
	// limited on it. The UI collapses its rate boxes into one when this is set,
	// which is better than earning the refusal after the operator has typed two
	// different numbers.
	SharedRate bool `json:"shared_rate"`
	// Write: the connector declares WritesExtra("storm_control") — an apply will
	// actually reach the device.
	//
	// FALSE ON EVERY CONNECTOR AT THE TIME OF WRITING, and that is the honest
	// state rather than a bug: no storm-control command in this codebase has been
	// run on hardware, so every package carries a test keeping this false. The UI
	// says "not verified on this platform yet", which is a different sentence
	// from "this platform cannot".
	Write bool `json:"write"`
	// Why: one sentence per absence, written by the connector because only the
	// connector knows. Shown next to a greyed-out control.
	Why []string `json:"why,omitempty"`
}

// stormControlCapsOf resolves the capability answer from the connector, with the
// same Driver-versus-Connector caveat as portActionCapsOf: a range whose
// fingerprint is known but which meshcraft cannot reach is a Driver only, and the
// honest answer for it is "known, not writable".
func stormControlCapsOf(d model.Device) StormControlCapsView {
	var name = driver.DriverNameFromExtra(d.Extra)
	var drv, ok = driver.Get(name)
	if !ok {
		return StormControlCapsView{}
	}
	var out = StormControlCapsView{Known: true}
	var conn, isConn = drv.(driver.Connector)
	if isConn {
		out.Write = conn.WritesExtra("storm_control")
	}
	var caps, hasCaps = driver.AsStormControlCapabler(drv)
	if !hasCaps {
		// The connector declares nothing. NOT "the platform cannot": an empty
		// class list here would be a claim about hardware nobody surveyed, which is
		// exactly the merge this type exists to prevent. Known stays true and the
		// class list stays nil, which the UI reads as "no answer".
		return out
	}
	out.Classes = caps.Classes
	out.Units = caps.Units
	out.SharedRate = caps.SharedRate
	out.Why = caps.Why
	return out
}

// handleReadDeviceStormControl reads the per-port limiters a device carries, LIVE.
//
// GET /api/devices/{id}/storm-control
//
// `operator` and not `viewer`: this opens a session on the switch. On the
// Instant On and SwOS ranges that is one of five, and on the ProCurve 2610 one of
// four — a read anyone with an account could trigger from a page refresh would
// be a way to exhaust them.
//
// A platform with no per-port storm control answers 422 with the CONNECTOR's own
// sentence (driver.Unsupported carries it), not a generic refusal: "this range
// has one device-wide storm control and no per-interface dimension" is something
// an operator can act on, and "unsupported" is not.
//
// EVERY STATUS CODE BELOW COMES FROM THE LADDER at the top of this file, since
// 2026-08-09 — [stormReaderFor] for the way to the device and [stormGradedRefusal]
// for what the read itself came back with, the second of them shared verbatim with
// the write. What this handler still decides for itself is the BODY: the capability
// view rides along on the refusals as well as on the 200, because a panel that
// cannot be told what the device carries can still be told what the platform can
// express.
func (s *Server) handleReadDeviceStormControl(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var caps = stormControlCapsOf(d)

	var _, adm, _, err = s.adminFor(r.Context(), d)
	if adm != nil {
		defer adm.Close()
	}
	var reader, refusal = stormReaderFor(adm, err)
	if refusal.respond(w) {
		return
	}
	if reader == nil {
		// A connector that WRITES without being able to read back is a legitimate
		// staging point (the two interfaces are independent, cf.
		// driver.StormControlReader), so this is a capability answer rather than an
		// error — and the caps ride along so the panel can still draw the control.
		// The zero refusal above is what says so: a session was proven, and this is
		// the connector's own shape rather than a failure (cf. stormReaderFor).
		respond(w, http.StatusOK, map[string]any{
			"caps":  caps,
			"state": nil,
			"note": "this connector writes storm control but cannot read it back: the " +
				"panel can offer the control, and nothing here can say what the device " +
				"currently carries",
			"steps": transcriptOf(adm),
		})
		return
	}
	var state driver.StormControlState
	state, err = reader.ReadStormControl(r.Context())
	refusal = stormGradedRefusal(err)
	if refusal.Code != 0 {
		// NOT refusal.respond(w): the capability view and the transcript ride back on
		// the failure, because a panel that has just been refused a STATE must still
		// be able to draw the control and say what the platform expresses. Only the
		// code and the sentence come from the ladder — which is the half that was
		// judged inline here, and the half that was free to drift away from the
		// identical judgement handleApplyDeviceStormControl makes below.
		respond(w, refusal.Code, map[string]any{
			"error": refusal.Msg,
			"caps":  caps,
			"steps": transcriptOf(adm),
		})
		return
	}
	respond(w, http.StatusOK, map[string]any{
		"caps":  caps,
		"state": state,
		"steps": transcriptOf(adm),
	})
}

// handleApplyDeviceStormControl writes per-port storm limiters onto one device,
// NOW.
//
// POST /api/devices/{id}/storm-control/apply
//
//	{ "ports": { "GE5": [ {"class":"broadcast","enabled":true,
//	                       "rate":5,"unit":"percent"},
//	                      {"class":"multicast","enabled":false} ] } }
//
// site_admin, like every other write that reaches hardware. A storm limiter is
// not a cosmetic setting: set too tight it discards production traffic on a port
// that looks perfectly healthy, and the symptom — an application that is merely
// slow — is among the hardest a network hands an operator.
//
// WHAT IS NOT DONE HERE: no per-class capability check. A class the platform
// cannot express comes back in ApplyResult.Skipped with the connector's own
// reason, which is an ORDINARY refusal rather than an error, and the classes the
// platform CAN express are still written. Re-deciding that here would put the
// judgement in two places and guarantee that one day they disagree — the same
// rule portadmin_apply.go applies to `force`.
//
// A caller receiving an empty Skipped is entitled to believe every limit in the
// request reached the hardware.
func (s *Server) handleApplyDeviceStormControl(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Ports map[string][]stormLimitBody `json:"ports"`
	}
	if !decode(w, r, &in) {
		return
	}
	// The faceplate's speeds ride along, so a platform whose field is packets per
	// second can honour a percentage (cf. stormPortSpeeds).
	var intents, refusal = stormIntents(in.Ports, stormPortSpeeds(d))
	if refusal.respond(w) {
		return
	}

	// The platform is judged BEFORE anything is dialled, and the order matters
	// beyond tidiness: Instant On allows five simultaneous sessions, the ProCurve
	// 2610 authenticates and then refuses the channel at four, and these platforms
	// lock accounts (cf. Server.adminFor). A gesture that will be refused anyway
	// must not spend a session — which on this endpoint is every gesture until a
	// connector declares the write.
	var conn, connErr = s.connectorFor(r.Context(), d)
	refusal = stormPlatformRefusal(conn, connErr, stormControlCapsOf(d))
	if refusal.respond(w) {
		return
	}

	var _, adm, _, err = s.adminFor(r.Context(), d)
	if adm != nil {
		defer adm.Close()
	}
	var applier driver.StormControlApplier
	applier, refusal = stormApplierFor(adm, err)
	if refusal.respond(w) {
		return
	}
	var res driver.ApplyResult
	res, err = applier.ApplyStormControl(r.Context(), driver.StormControlPlan{Ports: intents})
	refusal = stormGradedRefusal(err)
	if refusal.Code != 0 {
		// NOT refusal.respond(w): the transcript and the partial result ride back on
		// the failure, because a connector that refused one class and wrote another
		// names both in ApplyResult, and answering with the sentence alone would drop
		// exactly that. Only the code and the sentence come from the ladder — which is
		// the half that was drifting, and the half the GET above now shares verbatim.
		respond(w, refusal.Code, map[string]any{
			"error":  refusal.Msg,
			"result": res,
			"steps":  transcriptOf(adm),
		})
		return
	}

	// SevWarning, not SevInfo, and not because the gesture failed. A storm limiter
	// discards traffic by design: too tight, it drops production frames on a port
	// whose link light stays green, and the history line somebody reads while
	// hunting for when an application went slow must stand out. Shutting a port
	// gets the same grade next door, for the same reason.
	s.emitDeviceEvent(r, d, "device.storm_control", model.SevWarning,
		"Storm control written on \""+d.Name+"\": "+stormApplySummary(intents))
	respond(w, http.StatusOK, map[string]any{
		"ok":     true,
		"result": res,
		"steps":  transcriptOf(adm),
	})
}

// stormPortSpeeds reads the line rate of every port from the device's faceplate.
//
// `port_groups` is the same structure the UI draws from, and its `speed` label
// ("1G", "10G", "100M") is the only capacity meshcraft knows without asking the
// device. A group whose label does not parse contributes NOTHING rather than a
// default: driver.StormPPSFromPercent refuses an unknown speed on purpose, and
// filling it in here with a plausible number would defeat that from the outside.
func stormPortSpeeds(d model.Device) map[string]int {
	var out = map[string]int{}
	if len(d.PortGroups) == 0 {
		return out
	}
	var groups []struct {
		Prefix string `json:"prefix"`
		Count  int    `json:"count"`
		From   *int   `json:"from"`
		Speed  string `json:"speed"`
	}
	if json.Unmarshal(d.PortGroups, &groups) != nil {
		return out
	}
	var g int
	for g = range groups {
		var mbps, ok = driver.StormSpeedMbps(groups[g].Speed)
		if !ok {
			continue
		}
		var start = 1
		if groups[g].From != nil {
			start = *groups[g].From
		}
		var k int
		for k = start; k < start+groups[g].Count; k++ {
			out[groups[g].Prefix+strconv.Itoa(k)] = mbps
		}
	}
	return out
}

// stormApplySummary names what was asked, for the event log:
// "GE5 → broadcast limited at 5 % of line rate, multicast limiter removed".
//
// Sorted, and spelled out in words rather than in the wire's numbers and enums:
// the log is read months later by somebody who will not remember whether the bare
// figure was a percentage — which is the same reason the driver contract carries
// a unit in the first place.
func stormApplySummary(intents []driver.StormControlPortIntent) string {
	var out = ""
	var i int
	for i = range intents {
		var limits = append([]driver.StormLimit{}, intents[i].Limits...)
		driver.SortStormLimits(limits)
		var parts []string
		var j int
		for j = range limits {
			parts = append(parts, limits[j].Describe())
		}
		if out != "" {
			out += "; "
		}
		out += intents[i].Port + " → " + strings.Join(parts, ", ")
	}
	return out
}

// portStormIntents extracts the DECLARED storm-control intent from
// `devices.ports`, the way portSTPIntents extracts the spanning-tree one.
//
// # Why the intent lives there and not in a table of its own
//
// A port profile is applied by MERGING its fields into the port object
// (portrules.Config.Fields), so a profile that declares storm control writes
// `security.storm_control` beside the port's mode, PVID and label. Reading it
// back from the same place is what makes the profile and the live per-port
// gesture describe one thing rather than two — and it is why applying a profile
// cannot silently lose a limiter somebody set through the other path.
//
// # A PORT THAT DECLARES NOTHING IS SKIPPED, not disabled
//
// The key being absent means the profile has no opinion. Turning that into an
// explicit "disabled" intent would make every apply strip limiters an operator
// set by hand on the switch — the same rule the pointer in
// portrules.Config.StormControl exists to protect, enforced again here because
// this is the layer that would do the stripping.
//
// `speeds` supplies each port's line rate so a platform whose field is packets
// per second can honour a percentage; a port missing from it keeps zero, which
// those connectors refuse rather than assume (cf. stormPortSpeeds).
func portStormIntents(raw json.RawMessage, speeds map[string]int) []driver.StormControlPortIntent {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var names = make([]string, 0, len(m))
	var name string
	for name = range m {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []driver.StormControlPortIntent
	for _, name = range names {
		var fields = m[name]
		var secRaw, present = fields["security"]
		if !present {
			continue
		}
		var sec struct {
			StormControl *struct {
				Broadcast      bool    `json:"broadcast"`
				Multicast      bool    `json:"multicast"`
				UnknownUnicast bool    `json:"unknown_unicast"`
				Rate           float64 `json:"rate"`
				Unit           string  `json:"unit"`
			} `json:"storm_control"`
		}
		if json.Unmarshal(secRaw, &sec) != nil || sec.StormControl == nil {
			continue
		}
		var sc = sec.StormControl
		var unit = driver.StormRateUnit(sc.Unit)
		if unit == "" {
			// A declaration with no unit is not a rate anybody can act on, and
			// guessing percent would write a limiter on the pps platforms that
			// means something five orders of magnitude away. Skipped, visibly, by
			// producing no intent for the port.
			continue
		}
		var intent = driver.StormControlPortIntent{Port: name, SpeedMbps: speeds[name]}
		var pairs = []struct {
			on    bool
			class driver.StormClass
		}{
			{sc.Broadcast, driver.StormBroadcast},
			{sc.Multicast, driver.StormMulticast},
			{sc.UnknownUnicast, driver.StormUnknownUnicast},
		}
		var i int
		for i = range pairs {
			// EVERY CLASS THE PROFILE NAMES travels, including the ones switched
			// OFF: "this profile wants no broadcast limiter" is a real intent and
			// the connector needs it to clear one. What is skipped is a port that
			// declares no block at all — see the header.
			var l = driver.StormLimit{Class: pairs[i].class, Enabled: pairs[i].on}
			if pairs[i].on {
				l.Rate, l.Unit = sc.Rate, unit
			}
			intent.Limits = append(intent.Limits, l)
		}
		out = append(out, intent)
	}
	return out
}

// applyDeviceStormControl pushes the DECLARED per-port storm-control intent.
//
// The sibling of applyDeviceSTP and applyDevicePoE, and it earns its place in
// the same sequence for the same reason they do: the intent is written into
// `devices.ports` by whatever set it — a port profile, or the live per-port
// gesture — and an automatic apply rebuilds the whole plan from scratch. An
// intent that only travelled through the operator's original request would be
// dropped the first time a pipeline touched the device, silently removing a
// limiter nobody asked to remove.
//
// SILENT WHEN THERE IS NOTHING TO DO. A fleet where no profile declares storm
// control must not gain a note per device per apply saying so; the absence of
// output is what makes the notes that DO appear worth reading.
func (s *Server) applyDeviceStormControl(ctx context.Context, d model.Device,
	conn driver.Connector, adm driver.Admin) (driver.ApplyResult, error) {

	var out driver.ApplyResult
	var intents = portStormIntents(d.Ports, stormPortSpeeds(d))
	if len(intents) == 0 {
		return out, nil
	}
	if !conn.WritesExtra("storm_control") {
		// NOT AN ERROR, and not silent either. The profile declares a limiter the
		// platform's connector cannot yet write, and an operator who set it is
		// entitled to know it did not land — but a failed apply here would block
		// nothing else and help nobody, since no amount of retrying makes an
		// undeclared write happen.
		out.Notes = append(out.Notes,
			"this platform does not declare storm control as writable, so the "+
				"limiter declared on "+strconv.Itoa(len(intents))+" port(s) was NOT applied")
		return out, nil
	}
	var applier, ok = driver.AsStormControlApplier(adm)
	if !ok {
		out.Notes = append(out.Notes,
			"connector declares storm control writable but Admin has no ApplyStormControl")
		return out, nil
	}
	return applier.ApplyStormControl(ctx, driver.StormControlPlan{Ports: intents})
}

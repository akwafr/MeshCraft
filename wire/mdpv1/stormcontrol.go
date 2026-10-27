package mdpv1

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Storm control: the switch drops broadcast, multicast or unknown-unicast
// frames on ONE port once they exceed a threshold. Optional the same way PoE,
// spanning tree and DHCP snooping are (cf. poe.go, spanning.go, dhcpsnoop.go) —
// read and write kept apart, nothing mixed into [PortState] or [State].
//
// # THE RATE UNIT IS THE WHOLE PROBLEM, AND IT IS WHY THIS FILE EXISTS
//
// Every platform in this fleet limits storms. NOT ONE of them agrees with
// another on what the number next to the limit MEANS:
//
//   - Arista EOS and ProCurve take a PERCENTAGE of the port's line rate
//     ("storm-control broadcast level 5", "broadcast-limit 5");
//   - the FS S3900 lineage takes a PACKET RATE ("switchport broadcast
//     packet-rate 500") — and the vendor's own documentation has used that one
//     keyword for packets per second on some trains and kilobits per second on
//     others;
//   - Cisco IOS takes all three, distinguished only by a keyword that is easy to
//     omit: "level 10" is ten percent, "level pps 10" is ten packets per
//     second, "level bps 10" is ten bits per second. The same digits, three
//     configurations, five orders of magnitude apart.
//
// A contract carrying a bare number would therefore be a configuration that
// means "10 % of a gigabit" on one switch and "10 packets a second" on the
// switch next to it in the same rack. Both are accepted, neither is refused, and
// the second one takes a working access port off the air. That is the exact
// silent-wrong-config this product exists to prevent, so:
//
//	EVERY RATE IN THIS FILE CARRIES ITS UNIT, AND NO CONNECTOR EVER CONVERTS.
//
// # Why "no connector ever converts" rather than "connectors convert"
//
// Because none of the three conversions can be done honestly:
//
//   - PERCENT → PPS needs a frame size, and there is no such thing as the frame
//     size of a storm. Ten percent of a gigabit is 148 809 packets per second at
//     64-byte frames and 812 packets per second at 1518-byte frames — a factor of
//     183. A connector picking either one would be choosing, on the operator's
//     behalf and without telling them, between a limit that is 183 times too
//     tight and one that is 183 times too loose;
//   - PPS → PERCENT is the same fiction inverted;
//   - PERCENT → BPS needs the port's LINE RATE, which is not a constant. It is
//     whatever the port last negotiated: a 1 G jack that comes up at 100 M after
//     a cable is replaced turns a stored "100 Mbit/s, which was 10 %" into a
//     limit at 100 % of the new line rate, i.e. into no limit at all. The
//     configuration would not have changed and the protection would be gone;
//   - BPS → PPS is the frame-size fiction once more.
//
// So a connector accepts the units its platform speaks NATIVELY, and answers
// [NotSupported] for the others, naming what the platform does take. An operator
// told "this switch takes a percentage, you asked for packets per second" can
// act. An operator handed a silently converted number cannot, because nothing on
// any screen will ever say the number was changed.
//
// The one arithmetic a connector MAY perform is an exact change of MULTIPLE
// within a unit — bits per second to kilobits per second on a platform whose
// keyword is "kbps". Even that refuses a value which is not a whole multiple,
// rather than rounding: rounding 1 500 bit/s to 1 or 2 kbit/s is the same silent
// reinterpretation in miniature.
//
// # The three classes are three DIFFERENT sets of frames, and near enough is not
// the same
//
// [StormBroadcast], [StormMulticast] and [StormUnknownUnicast] name the three
// traffic classes the fleet's screens offer. Platforms word them differently and
// SOMETIMES MEAN SOMETHING ELSE, in both directions:
//
//   - BROADER: Cisco IOS spells its unicast limiter "storm-control unicast"
//     and its command reference defines it as unicast traffic — all of it, not
//     the unknown-unicast flooding class. Installing it for an operator who
//     asked for unknown-unicast would police established flows nobody asked to
//     touch, and the symptom is a file transfer that stalls at a suspiciously
//     round rate;
//   - NARROWER: RouterOS spells its multicast limiter "limit-unknown-multicasts"
//     and SwOS spells its "fmc" (flood unknown multicast). Installing either
//     for an operator who asked for multicast leaves registered multicast
//     entirely unlimited, and the symptom is a multicast storm that the screen
//     says is being policed.
//
// Neither substitution is acceptable, and a connector that made one would be
// lying in a way no re-read could catch — the device would report exactly the
// configuration the connector wrote. So a connector implements a class ONLY when
// its platform's keyword denotes the SAME set of frames, and otherwise refuses
// with [RefuseStormClass], which names the platform keyword and says which way
// it differs. That refusal is an ordinary one: it lands in
// [ApplyResult.Skipped], and the classes the platform DOES express are still
// written.
//
// # A FOURTH CLASS EXISTS ON ONE PLATFORM, AND IT IS NOT THE UNION OF THESE THREE
//
// A read-only survey of a live DCS-7050SX (EOS 4.28.13M, 2026-10-25) asked the
// device for its own grammar rather than a manual, and it answers:
//
//	storm-control all             level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
//	storm-control broadcast       level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
//	storm-control multicast       level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
//	storm-control unknown-unicast level ( LEVEL | ( pps PPS ) | ( rate RATE RATE_UNIT ) )
//
// "all" is a per-interface limiter on EVERY frame crossing the port, and it
// is NOT in this contract. Two reasons, the second decisive (ADR-0020):
//
//   - one connector of nine could express it, so the control would appear on
//     every screen and be refused everywhere but the Arista — the exact defect
//     [StormControlCaps] was invented to prevent;
//   - its name lies about its reach. An operator ticking "all" believing they
//     tick the three classes together gets unicast that reached its destination
//     policed as well, which is the same misreading, word for word, that makes
//     Cisco's "storm-control unicast" a refusal here.
//
// It is reported instead, through [StormForeignLimit] with an empty Class.
// EOS's per-interface "logging event stormcontrol discards" is absent for a
// plainer reason: one platform, and it is a logging switch, not a limiter.
//
// # What is NOT here
//
// The falling threshold (Cisco's second "level-low" argument, the hysteresis a
// port recovers at) is deliberately absent: only some of the fleet carries it,
// half of those tie it to an action, and a field only three connectors could
// honour would be three refusals and six silences. The ACTION on exceeding the
// threshold — shutdown, trap, plain drop — is absent for the same reason and a
// stronger one: "storm-control action shutdown" err-disables the port, which
// makes a rate limiter into a link cutter, and that gesture belongs next to the
// other destructive ones (portadmin.go) if it is ever offered at all. Observed
// storm counters are TELEMETRY and belong to a monitoring path.

// StormClass is one of the three traffic classes a storm limiter polices.
//
// The values are the WIRE form: they cross into JSON, into the stored intent,
// and into the frontend, and they are lower snake case for the same reason
// every other intent key in this codebase is.
type StormClass string

const (
	// StormBroadcast: frames addressed to ff:ff:ff:ff:ff:ff. The one class every
	// platform in this fleet words identically, and the only one where no
	// connector has had to argue about what it covers.
	StormBroadcast StormClass = "broadcast"
	// StormMulticast: frames with the multicast bit set — ALL of them, whether
	// or not a snooping table has registered the group.
	//
	// "All of them" is load-bearing. A platform whose keyword covers only
	// UNREGISTERED multicast expresses something narrower, and the difference is
	// invisible until IGMP snooping is on (cf. the file header).
	StormMulticast StormClass = "multicast"
	// StormUnknownUnicast: unicast frames whose destination is not in the MAC
	// table, and which the switch therefore FLOODS to every port in the VLAN.
	//
	// Not "unicast". The distinction is the file header's second half: a
	// limiter on all unicast polices traffic that reached its destination
	// perfectly well, which is a rate limit nobody asked for wearing a storm
	// limiter's name.
	StormUnknownUnicast StormClass = "unknown_unicast"
)

// StormClasses is the canonical order — the order screens list them in, the
// order connectors write them in, and the order results come back in. Fixed so
// that two identical gestures produce two identical transcripts.
var StormClasses = []StormClass{StormBroadcast, StormMulticast, StormUnknownUnicast}

// Valid reports whether c is one of the three classes this contract carries.
func (c StormClass) Valid() bool {
	return c == StormBroadcast || c == StormMulticast || c == StormUnknownUnicast
}

// StormRateUnit says what the number next to a limit MEANS. Read the file
// header before adding one: the whole design turns on this being explicit and on
// nothing ever converting between two of them.
type StormRateUnit string

const (
	// StormRatePercent: percent of the port's line rate, 0 < r <= 100.
	// Fractional values are legitimate — Cisco accepts two decimals, and "0.5 %"
	// on a ten-gigabit port is a real setting an operator reaches for.
	StormRatePercent StormRateUnit = "percent"
	// StormRatePPS: packets per second. Whole packets only — "1.5 packets a
	// second" is not a threshold any platform in this fleet can be given.
	StormRatePPS StormRateUnit = "pps"
	// StormRateBPS: BITS per second, not bytes and not kilobits.
	//
	// The base unit is bits because that is what Cisco's "level bps" takes and
	// because a base unit that is already a multiple invites the rounding this
	// contract refuses. A connector whose platform speaks kilobits divides — and
	// REFUSES a value that is not a whole number of them (cf. StormRateInKbps).
	StormRateBPS StormRateUnit = "bps"
)

// StormRateUnits is the canonical order, used for the sentences that list what
// a platform accepts.
var StormRateUnits = []StormRateUnit{StormRatePercent, StormRatePPS, StormRateBPS}

// Valid reports whether u is one of the three units this contract carries.
func (u StormRateUnit) Valid() bool {
	return u == StormRatePercent || u == StormRatePPS || u == StormRateBPS
}

// StormLimit is ONE class's limiter on one port.
type StormLimit struct {
	// Class: which frames this limits.
	Class StormClass `json:"class"`
	// Enabled: false REMOVES the limiter for this class — "no storm-control
	// broadcast", not "storm-control broadcast level 0".
	//
	// The two are not the same and the difference is a customer outage: on most
	// of this fleet a level of zero means "drop every frame of this class",
	// which on the broadcast class takes ARP with it and disconnects everything
	// behind the port. A connector must never render a disable as a zero rate,
	// and [StormLimit.Validate] refuses a zero rate on an ENABLED limit so the
	// mistake cannot arrive from a caller either.
	Enabled bool `json:"enabled"`
	// Rate: the threshold, in Unit. Ignored when Enabled is false — a caller
	// removing a limiter has no rate to name, and a connector must not read one.
	Rate float64 `json:"rate,omitempty"`
	// Unit: what Rate means. MANDATORY on an enabled limit, and there is
	// deliberately no default: a zero value that fell back to "percent" would
	// reintroduce exactly the ambiguity this type exists to remove, and it would
	// do it for the caller who forgot to think about it — i.e. the one who most
	// needs to be stopped.
	Unit StormRateUnit `json:"unit,omitempty"`
}

// Validate judges one limit, and it is the only place that judgement is made.
//
// Called by the API layer at the boundary AND by connectors before they write.
// Twice on purpose: the boundary refusal produces a 400 an operator can read,
// and the connector-side one means a plan assembled in Go — by a test, by a
// future automatic path — cannot reach a device with a rate whose unit is empty.
func (l StormLimit) Validate() error {
	if !l.Class.Valid() {
		return fmt.Errorf("storm control: %q is not a traffic class this contract carries "+
			"(%s)", l.Class, joinClasses(StormClasses))
	}
	if !l.Enabled {
		// Nothing else to check: a disable carries no rate and no unit, and a
		// caller that filled them in anyway is not wrong — it is simply sending
		// fields the write ignores.
		return nil
	}
	if !l.Unit.Valid() {
		return fmt.Errorf("storm control: the %s limit names no rate unit — a bare number "+
			"means ten percent of a gigabit on one platform and ten packets a second on "+
			"the next, so the unit is mandatory (%s)", l.Class, joinUnits(StormRateUnits))
	}
	if l.Rate <= 0 {
		return fmt.Errorf("storm control: the %s limit is enabled with a rate of %g — a "+
			"threshold of zero DROPS EVERY FRAME of that class, which on broadcast takes "+
			"ARP with it and disconnects whatever is behind the port. To remove a limiter, "+
			"send it disabled", l.Class, l.Rate)
	}
	switch l.Unit {
	case StormRatePercent:
		if l.Rate > 100 {
			return fmt.Errorf("storm control: the %s limit asks for %g %% of line rate; "+
				"a percentage above 100 is not a threshold any platform can be given",
				l.Class, l.Rate)
		}
	case StormRatePPS, StormRateBPS:
		if l.Rate != math.Trunc(l.Rate) {
			return fmt.Errorf("storm control: the %s limit asks for %g %s — this unit is "+
				"a whole count, and a connector that rounded it would be choosing a "+
				"threshold the operator did not type", l.Class, l.Rate, l.Unit)
		}
	}
	return nil
}

// Describe renders a limit the way an operator reads it, for notes, transcripts
// and event lines: "broadcast disabled", "multicast 5 % of line rate".
//
// The unit is SPELT OUT rather than abbreviated. A history line read six months
// later must not require remembering whether the bare number was a percentage.
func (l StormLimit) Describe() string {
	if !l.Enabled {
		return string(l.Class) + " limiter removed"
	}
	return string(l.Class) + " limited at " + FormatStormRate(l.Rate, l.Unit)
}

// FormatStormRate renders a rate and its unit in words.
func FormatStormRate(rate float64, unit StormRateUnit) string {
	var n = strconv.FormatFloat(rate, 'f', -1, 64)
	switch unit {
	case StormRatePercent:
		return n + " % of line rate"
	case StormRatePPS:
		return n + " packets/s"
	case StormRateBPS:
		return n + " bit/s"
	}
	return n + " " + string(unit)
}

// StormRateInKbps converts an exact bits-per-second rate to kilobits, for the
// platforms whose keyword is "kbps".
//
// THE ONLY ARITHMETIC THIS CONTRACT SANCTIONS, and it refuses rather than
// rounds. 1 500 bit/s is not 1 kbit/s and it is not 2 kbit/s; a connector
// picking either would be doing in miniature exactly what the file header
// forbids doing between units. ok is false for a rate that is not a whole number
// of kilobits, and the caller reports that as a refusal naming the value.
func StormRateInKbps(rate float64) (int, bool) {
	if rate <= 0 || rate != math.Trunc(rate) {
		return 0, false
	}
	var bits = int64(rate)
	if bits%1000 != 0 {
		return 0, false
	}
	return int(bits / 1000), true
}

// StormControlPortIntent is what meshcraft asks of ONE port.
//
// A class ABSENT from Limits is NOT MANAGED — the same rule as
// [PortPlan.Desired] and [DHCPSnoopPlan.Ports], and it is what makes "leave
// multicast alone" expressible. A class present with Enabled false is a
// REMOVAL, which is a different instruction and reaches the device as one.
type StormControlPortIntent struct {
	Port   string       `json:"port"`
	Limits []StormLimit `json:"limits,omitempty"`
	// SpeedMbps is this port's LINE RATE, supplied by the caller from the
	// device's faceplate.
	//
	// It exists for one job: letting a platform that speaks only packets per
	// second honour a percentage. That conversion is otherwise forbidden — see
	// [StormPPSFromPercent] — because percent and pps differ by a factor of 183
	// across the frame-size range, and a connector inventing a capacity would
	// police a port at a rate nobody chose.
	//
	// ZERO MEANS UNKNOWN, and a connector that needs it must then REFUSE the
	// percentage rather than assume a common speed. A gigabit default would be
	// wrong on every 10 G uplink in the fleet by a factor of ten, in the
	// direction that makes a limiter ten times looser than asked for.
	//
	// Ignored by platforms that take a percentage natively — they have no
	// conversion to do, and the field being wrong costs them nothing.
	SpeedMbps int `json:"speed_mbps,omitempty"`
}

// StormIntentPorts names the ports of a list of intents, in the order they are
// held. Sibling of [SnoopIntentPorts], and it exists for the same one caller:
// [ApplyResult.Unconfirmed] takes names where the appliers hold intents.
func StormIntentPorts(in []StormControlPortIntent) []string {
	var out = make([]string, 0, len(in))
	var it StormControlPortIntent
	for _, it = range in {
		out = append(out, it.Port)
	}
	return out
}

// Limit returns the intent for one class, and whether the plan named it at all.
func (p StormControlPortIntent) Limit(c StormClass) (StormLimit, bool) {
	var i int
	for i = range p.Limits {
		if p.Limits[i].Class == c {
			return p.Limits[i], true
		}
	}
	return StormLimit{}, false
}

// StormControlPlan is what meshcraft asks a connector to apply.
//
// There is no Force field, and its absence is deliberate. Storm control has no
// gesture equivalent to shutting the administration port: a limiter set too
// tight degrades traffic, it does not end the session that set it. The one
// destructive value — a rate of zero — is refused by [StormLimit.Validate]
// rather than made forceable, because there is no legitimate operator intent
// behind it that "disabled" does not express better.
type StormControlPlan struct {
	Ports []StormControlPortIntent `json:"ports,omitempty"`
}

// Validate judges a whole plan: every port named, every class named once per
// port, every limit sound.
//
// THE DUPLICATE-CLASS CHECK IS THE ONE THAT MATTERS. "broadcast at 5 %" and
// "broadcast at 50 %" in the same port's list is not a caller being redundant;
// it is two contradictory instructions whose outcome would be decided by which
// one a connector happened to write last. Refusing the plan is the only answer
// that does not silently pick one.
func (p StormControlPlan) Validate() error {
	if len(p.Ports) == 0 {
		return fmt.Errorf("storm control: the plan names no port")
	}
	var i int
	for i = range p.Ports {
		if strings.TrimSpace(p.Ports[i].Port) == "" {
			return fmt.Errorf("storm control: a port with an empty name cannot be limited")
		}
		if len(p.Ports[i].Limits) == 0 {
			return fmt.Errorf("storm control: %s is named with no limit at all — a port "+
				"meshcraft has nothing to say about is left out of the plan, not sent empty",
				p.Ports[i].Port)
		}
		var seen = map[StormClass]bool{}
		var j int
		for j = range p.Ports[i].Limits {
			var l = p.Ports[i].Limits[j]
			if seen[l.Class] {
				return fmt.Errorf("storm control: %s carries two %s limits — which one lands "+
					"would be decided by the order a connector happens to write them in, so "+
					"the plan is refused rather than half applied", p.Ports[i].Port, l.Class)
			}
			seen[l.Class] = true
			var err = l.Validate()
			if err != nil {
				return fmt.Errorf("%s: %w", p.Ports[i].Port, err)
			}
		}
	}
	return nil
}

// SortStormLimits puts a limit list in [StormClasses] order, in place.
//
// Connectors call it before writing so that the same plan always produces the
// same sequence of commands. On a fleet where a transcript is how an unproven
// command form gets learned, two transcripts that differ only in ordering are
// two transcripts that cannot be compared.
func SortStormLimits(limits []StormLimit) {
	var rank = map[StormClass]int{}
	var i int
	for i = range StormClasses {
		rank[StormClasses[i]] = i
	}
	sort.SliceStable(limits, func(a, b int) bool {
		return rank[limits[a].Class] < rank[limits[b].Class]
	})
}

// StormForeignLimit is a limiter OBSERVED on a port that this contract does not
// model. ADR-0020.
//
// # Why a third state was needed
//
// Until this type existed, [StormControlPortState.Limits] could say two things
// about a class: absent meant NOT READ, and present with Enabled false meant NO
// LIMITER. There was no way to say the third thing, which is the one a parser
// most often actually knows:
//
//	THERE IS A LIMITER ON THIS PORT AND I WILL NOT PRETEND TO UNDERSTAND IT.
//
// Without it, a parser that meets a statement it cannot reduce to a [StormLimit]
// has only bad options. Skipping the line is the one that got written, and on a
// CLI that prints only non-default lines it is the worst of them: the "silence
// inside a block that was read IS the absence of a limiter" rule then converts
// the skipped line into a positive claim that the port carries nothing. A port
// limited at 100 packets a second gets reported as unprotected, and an operator
// reading that screen has no way to find out otherwise.
//
// The rule this type exists to make expressible: THE SILENCE RULE APPLIES ONLY
// TO A BLOCK EVERY STATEMENT OF WHICH WAS UNDERSTOOD. A statement that was not
// understood lands here, verbatim, and the class it touches is no longer silent.
//
// # Three independent causes, on two different connectors
//
//   - EOS names a FOURTH per-interface class, "storm-control all", which
//     polices every frame including unicast that reached its destination. It is
//     not the union of the three classes this contract carries, so no Class
//     value is honest and the field is left empty;
//   - EOS also accepts "level rate <n> <unit>", whose unit vocabulary the
//     device's own grammar dump does not expand — so the rate is real and its
//     unit is unknown, which is exactly the one thing this contract refuses to
//     report as a number;
//   - Cisco IOS spells its unicast limiter "storm-control unicast" and means
//     ALL unicast. That connector already refused to fold it into
//     [StormUnknownUnicast] and carried the port list in a parallel []string,
//     which is the same observation with nowhere to put it.
type StormForeignLimit struct {
	// Class: the contract class this statement touches, set WHEN IT TOUCHES
	// EXACTLY ONE of them.
	//
	// Being broader than the class is fine and is the common case — Cisco's
	// "storm-control unicast" polices established flows as well as the
	// flooding class, and naming [StormUnknownUnicast] here is what puts the
	// device's line in front of the operator looking at that control.
	//
	// EMPTY when the statement touches MORE THAN ONE, or none. "storm-control
	// all" is the first case: any of the three would be as true as the other
	// two, so a reader would be told one class is limited at the rate shown when
	// what is limited is every frame on the port. The emptiness is a result, not
	// a gap, and [StormControlPortState.ForeignFor] deliberately matches nothing
	// on it.
	Class StormClass `json:"class,omitempty"`
	// Spelling: the device's own line, VERBATIM and untouched.
	//
	// It is the whole value of this type. Paraphrasing it would put the
	// connector's reading of an unfamiliar form between the operator and the
	// device, and the connector has just declared that its reading is the thing
	// it does not have.
	Spelling string `json:"spelling"`
	// Because: one sentence saying why this is not modelled, for the operator
	// reading a control that will not edit it.
	Because string `json:"because"`
}

// StormControlPortState is what a device reports about ONE port.
type StormControlPortState struct {
	// Port: facade name (GE12, ether3…), same vocabulary as State.Ports.
	Port string `json:"port"`
	// Limits: one entry per class the connector could READ on this port.
	//
	// A class absent was NOT READ, which is not "no limiter". The two lead an
	// operator to opposite conclusions and the same rule as
	// [DHCPSnoopPortState.Trusted] applies: a connector reports a class present
	// with Enabled false only when it has positively established that no limiter
	// is configured — which on a CLI that prints only non-default lines it can,
	// and on a firmware that served no field at all it cannot.
	//
	// AND ONLY WHEN EVERY STATEMENT IN THAT BLOCK WAS UNDERSTOOD. A block
	// carrying a form the parser could not read is a block whose silence proves
	// nothing, and what it carried belongs in Foreign (cf. [StormForeignLimit]).
	Limits []StormLimit `json:"limits,omitempty"`
	// Foreign: limiters seen on this port that this contract does not model.
	//
	// EMPTY ON MOST PORTS OF MOST PLATFORMS, and that is not a sign the field is
	// idle: it does not describe what a platform can do — [StormControlCaps]
	// does that — it records an ENCOUNTER. Nothing is added to the capability
	// table for it, because filling it is the parser's job and a parser only
	// knows once it has read a device.
	Foreign []StormForeignLimit `json:"foreign,omitempty"`
}

// Limit returns the observed limit for one class, and whether it was read.
//
// False for a class whose only observation is a [StormForeignLimit]: such a
// class was NOT read in the sense this contract means, and a caller treating
// "not read" as "no limiter" is the mistake ForeignFor exists to catch.
func (p StormControlPortState) Limit(c StormClass) (StormLimit, bool) {
	var i int
	for i = range p.Limits {
		if p.Limits[i].Class == c {
			return p.Limits[i], true
		}
	}
	return StormLimit{}, false
}

// ForeignFor returns the unmodelled limiter touching one class, if one was seen.
//
// Entries with an EMPTY Class match nothing here on purpose: "storm-control
// all" touches every frame on the port, and answering yes for broadcast would
// re-make, inside a helper, the conflation the empty Class was chosen to avoid.
// Those are read off [StormControlPortState.Foreign] directly.
func (p StormControlPortState) ForeignFor(c StormClass) (StormForeignLimit, bool) {
	var i int
	for i = range p.Foreign {
		if p.Foreign[i].Class != "" && p.Foreign[i].Class == c {
			return p.Foreign[i], true
		}
	}
	return StormForeignLimit{}, false
}

// StormControlState is what a device reports about storm control across the box.
type StormControlState struct {
	// Ports: one entry per port that was READ. A port absent was not observed.
	Ports []StormControlPortState `json:"ports,omitempty"`
	// Notes: what the connector understood that this type cannot express — a
	// platform whose rate is shared across classes, a keyword whose meaning is
	// broader than the class it was read into, a unit the firmware does not
	// state. Sentences for an operator.
	Notes []string `json:"notes,omitempty"`
}

// StormControlReader reads per-port storm control from a device.
//
// OPTIONAL. Platforms that do not carry the feature per port, or on which it has
// not been exercised, leave it unimplemented; callers use [AsStormControlReader]
// before offering the control at all.
type StormControlReader interface {
	ReadStormControl(ctx context.Context) (StormControlState, error)
}

// AsStormControlReader returns the reader when Admin implements it.
func AsStormControlReader(adm Admin) (StormControlReader, bool) {
	var r, ok = adm.(StormControlReader)
	return r, ok
}

// StormControlApplier writes per-port storm control to a device.
//
// OPTIONAL. Same pattern and same staging rule as [PoEApplier],
// [SpanningTreeApplier] and [DHCPSnoopApplier]: declare
// [Connector.WritesExtra]("storm_control") only once this is implemented AND the
// command forms have been proven on hardware. A connector whose forms are
// deduced from vendor documentation ships the code, leaves WritesExtra false,
// and names the unverified form in TODO.md.
//
// # THE DECLARATION IS PER RANGE, AND SIX OF NINE HAVE EARNED IT
//
// This file used to say that all nine connectors were unproven and that not one
// storm-control command had reached a device. That was true when it was written
// and stopped being true on 2026-08-08/09. The declaration was then settled as a
// PER-RANGE decision: the moment a connector's own applier has written, verified
// and reverted on hardware, WritesExtra("storm_control") goes true for that range
// alone. Holding a proven range hostage to an unproven one hides a working
// control and buys no safety, and [StormControlCaps] already exists to tell
// "this hardware cannot" from "not verified here yet".
//
// Declared, each on the strength of a live exercise through its own applier:
// arista/eos, cisco/ios, fs/s3400, ubiquiti/edgeswitch, mikrotik/swos,
// mikrotik/routeros. NOT declared, and none of them for a missing capture:
// instanton (no per-port dimension exists to write), fs/s3900 (the unit of
// "packet-rate" cannot be settled without a traffic generator) and hpe/procurve
// (range parked). Every package — declared or not — carries a test asserting its
// own answer, so a flag flipped without evidence fails the build rather than
// reaching a customer's switch.
//
// # WHAT A LIVE EXERCISE HAS TO CONTAIN, learned from the six that were run
//
// A write and a read-back are not enough. Two steps caught real defects and are
// now expected of every range:
//
//   - A SECOND IDENTICAL APPLY THAT WRITES NOTHING. Platforms re-spell the number
//     they were given — EOS normalises 5.00 to 5, Cisco IOS prints 1500 back as
//     "1.5k" and keeps 5.00 as 5.00 — so a connector comparing rate TEXT rather
//     than numbers rewrites a correct line on every pass, for ever. The second
//     apply is the only step that proves it does not;
//   - A REVERT THAT PERSISTS IF THE APPLY PERSISTED. Checked with the device's
//     equivalent of "show startup-config", never the running one: an applier
//     that saves to flash and a harness that reverts with unsaved lines leaves
//     the switch booting into a limiter nobody configured, with every assertion
//     in the exercise passing.
//
// # Three kinds of refusal, and they are not interchangeable
//
//   - THE PLATFORM HAS NO PER-PORT STORM CONTROL AT ALL. The applier returns
//     [NotSupported] and writes nothing. Instant On is the case: the 1930 has a
//     device-wide storm control and no per-port dimension, which its own
//     capability map has declared since before this contract existed;
//   - THE PLATFORM CANNOT EXPRESS ONE CLASS, or expresses something adjacent to
//     it. The class is refused per port through [RefuseStormClass] and lands in
//     [ApplyResult.NotApplied] as a platform refusal; the other classes are still written. Killing the
//     whole gesture over one class would throw away two limits the operator can
//     have;
//   - THE PLATFORM CANNOT EXPRESS THE UNIT. Same treatment, through
//     [RefuseStormUnit]. Never a conversion (cf. the file header).
//
// A connector that cannot tell which of the three it is in a not surveyed its
// platform, and must not write.
//
// # A FOREIGN LIMITER IS NOT AN ABSENT ONE
//
// An applier compares the plan against a pre-write read so it can send nothing
// when the limiter is already right. A class whose only observation is a
// [StormForeignLimit] has NOT been read, and must not be taken for a class
// carrying no limiter — the two differ by a write that silently replaces
// something an operator put there.
//
// What the applier does with it: it WRITES, because the plan named that class
// and the operator asked. What it must not do is write silently — the verbatim
// line that got replaced goes into [ApplyResult.Notes]. Refusing instead was
// considered and rejected: a port would then be un-editable until somebody
// cleared a line by hand on the device, and meshcraft would be the reason.
type StormControlApplier interface {
	ApplyStormControl(ctx context.Context, plan StormControlPlan) (ApplyResult, error)
}

// AsStormControlApplier returns the applier when Admin implements it.
func AsStormControlApplier(adm Admin) (StormControlApplier, bool) {
	var a, ok = adm.(StormControlApplier)
	return a, ok
}

// StormControlCaps is what a PLATFORM can express, answerable WITHOUT a session.
//
// # This is what lets a screen tell "cannot" from "not yet"
//
// Two different facts have to reach the UI and they call for opposite words:
//
//   - "this switch has no multicast storm limiter" is permanent. Nothing is
//     coming, no capture will change it, and a screen that left the control
//     enabled would be promising hardware that does not exist;
//   - "meshcraft has not proven the command form on this range yet" is
//     temporary, and it is what [Connector.WritesExtra] answers.
//
// Collapsing them was the failure the PoE power-cycle button had for a while
// (cf. driver/portadmin.go, § on the name): one flag answered two questions and
// two platforms got a button that could only refuse. So the UI reads BOTH — the
// classes here, and WritesExtra("storm_control") — and says "this platform
// cannot" or "not verified on this range yet" accordingly.
type StormControlCaps struct {
	// Classes: the traffic classes this connector will write, in
	// [StormClasses] order. A class absent is one the platform cannot express as
	// itself — never one it expresses approximately (cf. the file header).
	Classes []StormClass `json:"classes,omitempty"`
	// Units: the rate units this connector accepts without converting, in
	// [StormRateUnits] order. Empty means the platform expresses no per-port
	// storm control at all.
	//
	// THIS IS THE WRITE SURFACE ONLY, and a reader may legitimately report a
	// unit absent from it. EOS is the case: its CLI takes "level pps <n>", so
	// a port can carry a packet-rate limiter that the connector will READ as
	// [StormRatePPS] and report truthfully, while this list stays "percent"
	// because the connector has not proven it can WRITE that form on this
	// forwarding hardware. Collapsing the two would force a reader to choose
	// between lying about a port and saying nothing about it.
	Units []StormRateUnit `json:"units,omitempty"`
	// SharedRate: the platform stores ONE rate per port, shared by every class
	// it limits, rather than one rate per class.
	//
	// RouterOS and SwOS are both like this: a single "storm-rate" / "srt"
	// value with per-class enable flags beside it. A UI that offered three
	// independent rate boxes there would let an operator type three numbers, two
	// of which are silently discarded — so the flag exists to let the screen
	// collapse them into one box BEFORE the refusal has to be earned.
	SharedRate bool `json:"shared_rate"`
	// Why: one sentence per absence, for an operator reading the greyed-out
	// control. Written by the connector, because only the connector knows.
	Why []string `json:"why,omitempty"`
}

// Expresses reports whether the platform carries this class as itself.
func (c StormControlCaps) Expresses(class StormClass) bool {
	var i int
	for i = range c.Classes {
		if c.Classes[i] == class {
			return true
		}
	}
	return false
}

// Accepts reports whether the platform takes this unit without converting.
func (c StormControlCaps) Accepts(unit StormRateUnit) bool {
	var i int
	for i = range c.Units {
		if c.Units[i] == unit {
			return true
		}
	}
	return false
}

// StormControlCapabler is implemented by CONNECTORS (the Driver, not the
// session): the answer does not depend on a device and the caller needs it
// before the first packet, exactly like [Connector.WritesExtra].
type StormControlCapabler interface {
	StormControlCaps() StormControlCaps
}

// AsStormControlCapabler returns the capability answer when the connector
// carries one, and whether it did.
func AsStormControlCapabler(v any) (StormControlCaps, bool) {
	var c, ok = v.(StormControlCapabler)
	if !ok {
		return StormControlCaps{}, false
	}
	return c.StormControlCaps(), true
}

// RefuseStormClass builds the refusal for a class a platform cannot express.
//
// A [NotSupported] error rather than a bare string, and connectors put its
// Error() into [ApplyResult.NotApplied] (through [PlatformRefusal]) rather
// than returning it: the refusal is a
// CAPABILITY answer and carries the connector's name and the platform's reason,
// which is what an operator comparing two switches needs — but it must not kill
// a gesture that can still deliver the other two classes.
//
// `instead` names the platform's own keyword and says which way it differs. It
// is the whole value of this message: "this switch has no unknown-unicast
// limiter" is a dead end, while "this switch spells it storm-control unicast,
// which polices ALL unicast including established flows" tells the operator
// what they can have and what it would cost.
func RefuseStormClass(connector string, class StormClass, port, instead string) error {
	var gesture = "limit " + string(class) + " storms on " + port
	return NotSupported(connector, gesture, instead)
}

// RefuseStormUnit builds the refusal for a rate unit a platform does not speak.
//
// NEVER a conversion, and the message says so: the operator must learn that the
// number was NOT changed behind their back, because the alternative reading —
// "it probably converted it" — is exactly the assumption that makes a silently
// reinterpreted threshold survive a review.
func RefuseStormUnit(connector string, class StormClass, port string,
	got StormRateUnit, accepts []StormRateUnit) error {

	var gesture = "limit " + string(class) + " storms on " + port
	return NotSupported(connector, gesture, fmt.Sprintf(
		"the rate was given in %s and this platform's limiter takes %s. Nothing was "+
			"converted and nothing was written: percent, packets per second and bits per "+
			"second are not derivable from one another without a frame size or a line rate "+
			"that could change under the configuration, and a converted threshold is one no "+
			"screen would ever report as converted. Re-send the limit in %s",
		got, joinUnits(accepts), joinUnits(accepts)))
}

func joinClasses(cs []StormClass) string {
	var out = make([]string, 0, len(cs))
	var i int
	for i = range cs {
		out = append(out, string(cs[i]))
	}
	return strings.Join(out, ", ")
}

func joinUnits(us []StormRateUnit) string {
	var out = make([]string, 0, len(us))
	var i int
	for i = range us {
		out = append(out, string(us[i]))
	}
	return strings.Join(out, " or ")
}

// --- PERCENT → PACKETS PER SECOND, for platforms that speak only pps ---
//
// # Why this exists when the file header forbids conversion
//
// It forbids conversion because percent and pps are not interchangeable WITHOUT
// A FRAME SIZE: 10 % of a gigabit is 148,809 pps at 64-byte frames and 812 pps at
// 1518 — a factor of 183, and a connector inventing either would silently police
// a port at a rate nobody chose.
//
// The missing term is now SUPPLIED rather than guessed: an operator decision
// fixes the frame size at 64 bytes and the capacity at the port's own link speed.
// That makes the arithmetic exact and, more importantly, makes it a stated
// assumption rather than a hidden one. The conversion is still refused when the
// speed is unknown — a rate computed from a guessed capacity is the same lie in
// a different place.
//
// # Why 64 bytes, and what it costs
//
// It is the minimum Ethernet frame, so it yields the MAXIMUM packet rate a link
// can carry — the classic 1.488 Mpps for gigabit. It is also what a broadcast
// storm is actually made of: ARP, discovery and loop-amplified frames are all
// small, so sizing on the smallest frame is what makes the limit bite on the
// traffic the feature exists to stop.
//
// The consequence to be honest about: a cap computed this way equals the
// requested share of line rate ONLY for 64-byte traffic. Larger frames at the
// same packet rate carry MORE bandwidth, so the limiter is looser in bit terms
// than the percentage suggests. It is never tighter, which is the safe direction
// — a storm-control rule that throttles more than asked is an outage.
const (
	// StormFrameBytes is the frame size the conversion assumes.
	StormFrameBytes = 64
	// StormFrameOverheadBytes is what every frame costs ON THE WIRE beyond its
	// own bytes: a 7-byte preamble plus 1-byte start delimiter, and the 12-byte
	// interframe gap.
	//
	// OMITTING IT IS THE CLASSIC ERROR and it is not small: without these 20
	// bytes a gigabit link computes as 1,953,125 pps instead of 1,488,095 — 31 %
	// high — so every converted limit would be a third looser than asked for.
	StormFrameOverheadBytes = 20
)

// StormPPSFromPercent converts a percentage of line rate into packets per
// second, for a port running at speedMbps.
//
// Returns false when it cannot be done exactly enough to be honest: a
// non-positive or absurd percentage, or an unknown link speed. A caller that
// gets false must REFUSE the intent rather than fall back to a default — the
// whole point of this function is that the missing term is supplied, and a
// default would put the guess back.
func StormPPSFromPercent(percent float64, speedMbps int) (int, bool) {
	if percent <= 0 || percent > 100 || speedMbps <= 0 {
		return 0, false
	}
	// Bits per frame on the wire, including preamble and interframe gap.
	var bitsPerFrame = float64(StormFrameBytes+StormFrameOverheadBytes) * 8
	var linePPS = float64(speedMbps) * 1_000_000 / bitsPerFrame
	var pps = linePPS * percent / 100
	if pps < 1 {
		// A share so small it rounds to no packets at all. Rounding UP to 1 pps
		// would be a limiter far tighter than asked for, and rounding to 0 is
		// usually the platform's spelling of "unlimited" — both are wrong in a
		// way the operator would not see, so this refuses instead.
		return 0, false
	}
	return int(pps), true
}

// StormSpeedMbps reads a faceplate speed label ("1G", "10G", "100M") into
// megabits per second.
//
// The connectors already carry these strings on their port groups, and they are
// the only speed most of them know without asking the device. Returns false on
// anything it does not recognise rather than guessing a default: an unrecognised
// label is a port whose capacity is unknown, and [StormPPSFromPercent] refuses
// that case on purpose.
func StormSpeedMbps(label string) (int, bool) {
	var s = strings.ToUpper(strings.TrimSpace(label))
	s = strings.TrimSuffix(s, "BPS")
	s = strings.TrimSuffix(s, "B")
	switch {
	case strings.HasSuffix(s, "G"):
		var n, err = strconv.ParseFloat(strings.TrimSuffix(s, "G"), 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		return int(n * 1000), true
	case strings.HasSuffix(s, "M"):
		var n, err = strconv.ParseFloat(strings.TrimSuffix(s, "M"), 64)
		if err != nil || n <= 0 {
			return 0, false
		}
		return int(n), true
	}
	return 0, false
}

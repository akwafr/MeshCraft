package mdpv1

// TRAFFIC DECOMPOSED BY DESTINATION CLASS — the observation counterpart of the
// storm limiter, and what a connector must DECLARE before a screen may stack it.
//
// # Why there is a capability at all (ADR-0028)
//
// The port overlay stacks packets-per-second by class, and the top of the stack
// reads as the total. That only holds if the reader can tell a class that is
// MISSING from a class that is ZERO — and nothing in this contract could say
// which, because a series with no samples looks the same either way:
//
//	collection just started, the device is quiet   → "not measured yet", transient
//	this platform cannot count this class at all   → "never will be", permanent
//
// Those two call for opposite reactions from an operator, and inferring either
// one from an empty series picks wrong half the time. A gap that reads as a
// trough in the traffic is the exact lie ADR-0020 removed from the storm-control
// panel, where a line nobody managed to read displayed as "port unprotected".
//
// This is ADR-0022's shape, one domain over: nothing there distinguished "writes
// STP" from "can join a region", so a clean apply left sw01-arena outside its
// own site's tree, carrying its own bridge MAC as a region name. The fix was the
// same — declare the narrow thing, separately.
//
// # Why ONE FLAG PER CLASS and not a single "can decompose"
//
// Because a single flag would be FALSE on the entire current fleet, and false in
// the dangerous direction — promising more than any platform holds.
//
// Multicast and broadcast come from one place (the ifXTable per-class counters,
// or openconfig's `in-multicast-pkts` / `in-broadcast-pkts`); unknown unicast
// comes from nowhere. No platform surveyed serves it: openconfig gives
// `in-unicast-pkts`, which is unicast TOTAL with the flooded share folded in and
// no second counter to subtract, and the standard MIB has no per-port object for
// it either (`ifInUnknownProtos` counts something else entirely — frames dropped
// for an unsupported protocol). The one unknown-unicast number on this fleet is
// the `Drops` column of Arista's `show storm-control`, which counts frames the
// limiter DISCARDED rather than frames the port carried.
//
// So a connector that advertised "can decompose traffic" would be advertising
// unknown unicast everywhere it advertised broadcast. Per class, it cannot.
//
// # The classes are [StormClass], deliberately reused
//
// Not a parallel enum. These are the same three classes the limiter polices, and
// a second spelling of "broadcast" sitting beside the first is how the two drift
// — the divergence wire/metrics/names.go exists to prevent, in the vocabulary
// next door.
const (
	// FeatTrafficClassMulticast: the MULTICAST frame count of ONE PORT reaches
	// meshcraft as a counter, on the channel this platform is actually collected
	// on.
	//
	// "ON THE CHANNEL IT IS COLLECTED ON" is the precise claim, and it is
	// narrower than "the platform can count multicast somewhere". RouterOS, for
	// one, serves `rx-multicast` on /interface/ethernet while its telemetry
	// reads neither — so the class would never arrive, and declaring the
	// platform's raw ability would promise a band that stays empty forever
	// while reading as "not measured yet". The flag follows what the collector
	// FETCHES, so it flips in the same commit that teaches it a new field.
	//
	// SEPARATE FROM the storm-control capabilities ([StormControlCaps].Classes)
	// and that separation is the whole point: policing a class and counting it
	// are different platform facts. Every connector in this fleet polices all
	// three classes; none counts the third.
	//
	// ABSENT MEANS "HAS NOT DECLARED IT", which is the safe default here as
	// everywhere — the screen says the platform does not report this class
	// rather than drawing a band it cannot fill.
	FeatTrafficClassMulticast = "TrafficClassMulticast"
	// FeatTrafficClassBroadcast: same, for BROADCAST frames.
	FeatTrafficClassBroadcast = "TrafficClassBroadcast"
	// FeatTrafficClassUnknownUnicast: same, for unicast frames the switch FLOODS
	// because their destination is not in the MAC table.
	//
	// NO CONNECTOR SETS THIS TODAY, and the constant exists anyway — that is not
	// an oversight, it is the declaration doing its job. It gives the screen
	// something to read in order to say "this platform does not report unknown
	// unicast", which is a true statement, instead of silently dropping the band
	// and leaving a two-class stack indistinguishable from a complete one.
	//
	// The day a platform counts FORWARDED unknown unicast — not limiter drops —
	// it sets this flag, the series appears beside the other two, and the stack
	// goes from two bands to three with nothing else to change.
	FeatTrafficClassUnknownUnicast = "TrafficClassUnknownUnicast"
)

// TrafficClassFeature maps a class to the capability flag that declares a
// platform can COUNT it.
//
// A function rather than a map so that an unhandled class is a compile-time
// hole rather than a silent empty string that would read as "not declared" —
// the failure mode this whole file exists to prevent.
func TrafficClassFeature(c StormClass) string {
	switch c {
	case StormMulticast:
		return FeatTrafficClassMulticast
	case StormBroadcast:
		return FeatTrafficClassBroadcast
	case StormUnknownUnicast:
		return FeatTrafficClassUnknownUnicast
	}
	return ""
}

// CountsTrafficClass reports whether this platform declares it can count c per
// port. Nil-map safe, like [Capabilities.Has], and an unknown class is false.
func (c Capabilities) CountsTrafficClass(class StormClass) bool {
	var feat = TrafficClassFeature(class)
	if feat == "" {
		return false
	}
	return c.Has(feat)
}

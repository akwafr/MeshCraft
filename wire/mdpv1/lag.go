package mdpv1

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Link aggregation (LACP / static LAGs), in the standard model.
//
// # What a LAG is to meshcraft
//
// A NAMED AGGREGATE of faceplate ports that behaves like one port: it has
// members, a mode (802.3ad negotiation or static grouping), and — the half
// operators actually configure — a VLAN mapping of exactly a port's shape
// (mode / PVID / tagged). The reference capture that anchored the model is the
// lab CRS305's `bonding1` (2026-08-13): `mode: 802.3ad`, `slaves:
// "sfp-sfpplus4"`, and in the bridge tables the bond appears AS an interface —
// `pvid: 1` in `/interface/bridge/port`, listed in eleven VLANs' `tagged`
// lists — indistinguishable from a physical port. Every platform surveyed
// treats the aggregate the same way (EOS's Port-ChannelN, IOS's PoN are
// interfaces), which is why [LAGState.VLAN] reuses [PortState] verbatim.
//
// # Names: standard "LAG<n>", translated by each connector
//
// The device spellings diverge exactly like port names do — `bonding1`
// (RouterOS), `Port-Channel1` (EOS), `Po1` (IOS) — and the same doctrine
// applies: ONE standard spelling in every stored intent, every observation
// and every screen, translated at the connector boundary. "LAG" + the
// device's own number: `bonding1` ↔ `LAG1`, `Port-Channel12` ↔ `LAG12`. The
// number is the device's, not an index meshcraft invents — renumbering on
// import would make two reads of the same device disagree.

// LAG modes, the standard vocabulary. A connector maps its platform's richer
// set onto these two: everything that negotiates (802.3ad) is "lacp",
// everything that does not (RouterOS balance-*, static EtherChannel) is
// "static". The distinction an operator needs is "will the far end detect a
// miscable" — finer platform modes ride Notes.
const (
	LAGModeLACP   = "lacp"
	LAGModeStatic = "static"
)

// LAGState is one aggregate — as observed (Running meaningful) or as intended
// (Running ignored).
type LAGState struct {
	// Name: standard spelling, "LAG<n>".
	Name string `json:"name"`
	// Raw: the DEVICE's own spelling ("bonding1", "Port-Channel1"), set by the
	// connector on a READ. It exists for the consumers that meet the device
	// vocabulary elsewhere — the FDB collector reports learned MACs on
	// "bonding1", and only this field lets the controller place them on LAG1.
	// Ignored on intent.
	Raw string `json:"raw,omitempty"`
	// Mode: LAGModeLACP or LAGModeStatic.
	Mode string `json:"mode"`
	// Members: STANDARD port names, sorted. Sorted for the same reason
	// PortState.Tagged is: two reads of one device must compare equal.
	Members []string `json:"members"`
	// VLAN: the aggregate's VLAN mapping, in the port vocabulary. Mode empty
	// means "not managed", exactly as on a port.
	VLAN PortState `json:"vlan"`
	// Running: the aggregate is up (observed only; never part of intent).
	Running bool `json:"running,omitempty"`
	// Fallback: LACP fallback — the untagged VLAN a member forwards in while
	// LACP has NOT converged. [LAGFallbackOff] (0) is no fallback: members
	// stay down until the far end negotiates.
	//
	// A VLAN and not a boolean, because the two deployments that want
	// fallback want opposite VLANs. A server whose bond is not configured yet
	// must land in the SAME VLAN the aggregate carries, so it keeps the
	// network it is supposed to have; a port an installer may plug anything
	// into must land in a DIFFERENT, quarantined VLAN, so an unnegotiated
	// link cannot reach production. A boolean can only express one of the two,
	// and would have to be widened later — the most expensive change a wire
	// contract has.
	//
	// OPTIONAL, and the optionality is a GESTURE, not a silent best effort:
	// only a connector declaring [GestureLAGFallback] honours this field.
	// Mode must be [LAGModeLACP] — fallback is what happens when negotiation
	// has not happened, and a static aggregate never negotiates.
	//
	// Ignored on a READ by connectors that cannot observe it; a connector that
	// can read it back reports the VLAN it found, so the screen can show
	// drift like any other field.
	Fallback int `json:"fallback,omitempty"`
}

// LAGFallbackOff is the absence of LACP fallback — the zero value of
// [LAGState.Fallback], named so the three states an operator sees (supported
// and active, supported and inactive, NOT SUPPORTED) never collapse into two
// in the code that reads it.
const LAGFallbackOff = 0

// LAGFallbackWriter is the [LAGWriter] whose ApplyLAGs honours
// [LAGState.Fallback].
//
// # Why a second interface rather than a field every writer reads
//
// Fallback is not a gesture of its own on the wire: it rides inside the
// aggregate list the `lags` op already carries. But most platforms cannot do
// it at all, and the screen must know BEFORE it offers the control — a
// disabled checkbox with no explanation is worse than an absent one
// (UI-GUIDE §2). A separate interface turns "my ApplyLAGs understands
// Fallback" into a method-set fact, which [GesturesOf] renders as the wire
// gesture "lag-fallback" for in-process and external connectors alike.
//
// # Why the method takes and returns nothing
//
// There is nothing to ask a device. Whether a connector's code writes the
// fallback keyword is a property of that code, known before a session is
// spent — the same reason [Connector.Supports] exists at all. The marker is
// the declaration; the refusal, when an intent carries Fallback anyway, is
// [RefuseLAGFallback].
type LAGFallbackWriter interface {
	LAGWriter
	// WritesLAGFallback declares that this connector writes LACP fallback.
	// Never called; implementing it IS the answer.
	WritesLAGFallback()
}

// AsLAGFallbackWriter returns the writer when Admin honours Fallback.
//
// Callers that can reach the connector should prefer Has(c,
// [GestureLAGFallback]): it answers the same question without an
// authentication, which is the whole doctrine of gestures. This assertion is
// the check that cannot lie, at the call site, once a session exists.
func AsLAGFallbackWriter(adm Admin) (LAGFallbackWriter, bool) {
	var w, ok = adm.(LAGFallbackWriter)
	return w, ok
}

// LAGFallbackWanted returns the first aggregate in the list asking for
// fallback, and whether any did.
//
// Shared by the API gate and by connectors, because both must answer the same
// question the same way: an intent carrying Fallback for a connector that does
// not declare [GestureLAGFallback] is REFUSED, never trimmed. Writing the
// aggregate without the fallback would leave the device holding a
// configuration the screen does not show — and the operator believing a port
// forwards while LACP is down, which is precisely the failure fallback was
// asked for.
func LAGFallbackWanted(desired []LAGState) (LAGState, bool) {
	var i int
	for i = range desired {
		if desired[i].Fallback != LAGFallbackOff {
			return desired[i], true
		}
	}
	return LAGState{}, false
}

// RefuseLAGFallback builds the refusal for a platform that aggregates but
// cannot fall back. [NotSupported], so it crosses the wire as
// ErrCodeUnsupported with the gesture named and reaches the operator verbatim.
func RefuseLAGFallback(connector, lag string) error {
	return NotSupported(connector, "LACP fallback on "+lag,
		"this platform's aggregates have no fallback: a member forwards only "+
			"once LACP has negotiated. Nothing was written — the aggregate was "+
			"refused whole rather than created without the fallback it was asked for")
}

// LAGWriter applies the DESIRED aggregate list to the device.
//
// OPTIONAL, like StormControlApplier and under the same doctrine: a connector
// declares it by implementing it, and callers discover it with [AsLAGWriter].
// The desired list is COMPLETE: an aggregate the device carries and the list
// does not is removed — same replace semantics as VLAN membership, and the
// reason the API validates before calling (an empty list dissolves every LAG,
// which must be an explicit act, not an accident of a partial read).
type LAGWriter interface {
	ApplyLAGs(ctx context.Context, desired []LAGState) (ApplyResult, error)
}

// AsLAGWriter returns the writer when Admin implements it.
func AsLAGWriter(adm Admin) (LAGWriter, bool) {
	var w, ok = adm.(LAGWriter)
	return w, ok
}

// StandardLAGName renders the standard spelling for the device's aggregate
// number: 1 → "LAG1".
func StandardLAGName(n int) string { return "LAG" + strconv.Itoa(n) }

// LAGNumber extracts the number from a standard LAG name; ok is false when
// the name is not of the standard shape.
func LAGNumber(std string) (int, bool) {
	var rest, has = strings.CutPrefix(std, "LAG")
	if !has || rest == "" {
		return 0, false
	}
	var n, err = strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// ValidateLAGs checks a DESIRED list against the invariants no platform can
// relax; per-platform bounds (MaxLAGs, PortsPerLAG) are checked by the caller
// against the connector's capabilities, and per-platform constraints by the
// connector itself.
//
// PURE, because these are exactly the mistakes a UI slip produces: the same
// port in two aggregates would make the second write silently steal it from
// the first, and a nameless or duplicate-named aggregate can never be diffed
// against an observation again.
func ValidateLAGs(desired []LAGState) error {
	var seenName = map[string]bool{}
	var owner = map[string]string{}
	var i int
	for i = range desired {
		var l = desired[i]
		var _, ok = LAGNumber(l.Name)
		if !ok {
			return fmt.Errorf("aggregate name %q is not of the standard shape LAG<n>", l.Name)
		}
		if seenName[l.Name] {
			return fmt.Errorf("aggregate %s appears twice", l.Name)
		}
		seenName[l.Name] = true
		if l.Mode != LAGModeLACP && l.Mode != LAGModeStatic {
			return fmt.Errorf("aggregate %s: mode %q is neither %q nor %q",
				l.Name, l.Mode, LAGModeLACP, LAGModeStatic)
		}
		if len(l.Members) == 0 {
			return fmt.Errorf("aggregate %s has no member: an empty aggregate "+
				"forwards nothing and blocks its name", l.Name)
		}
		// Fallback: a VLAN id or nothing, and only where LACP negotiates. A
		// static aggregate never negotiates, so "what happens while LACP is
		// down" has no meaning there — accepting it would store an intent no
		// read could ever confirm.
		if l.Fallback != LAGFallbackOff {
			if l.Fallback < 1 || l.Fallback > 4094 {
				return fmt.Errorf("aggregate %s: fallback VLAN %d is outside 1..4094",
					l.Name, l.Fallback)
			}
			if l.Mode != LAGModeLACP {
				return fmt.Errorf("aggregate %s: a %s aggregate has no LACP fallback — "+
					"fallback is what happens while negotiation has not converged, and "+
					"a static aggregate never negotiates", l.Name, l.Mode)
			}
		}
		var m string
		for _, m = range l.Members {
			if owner[m] != "" {
				return fmt.Errorf("port %s cannot belong to both %s and %s",
					m, owner[m], l.Name)
			}
			owner[m] = l.Name
		}
	}
	return nil
}

// SortLAGs normalises a list for storage and comparison: aggregates by name,
// members within each.
func SortLAGs(lags []LAGState) {
	var i int
	for i = range lags {
		sort.Strings(lags[i].Members)
		sort.Ints(lags[i].VLAN.Tagged)
	}
	sort.Slice(lags, func(a, b int) bool { return lags[a].Name < lags[b].Name })
}

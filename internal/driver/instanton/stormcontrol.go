package instanton

// Per-port storm control on the Instant On 1930 — and the whole of this file is a
// REFUSAL, stated before any packet is sent.
//
// # The fact, and where it comes from
//
// This range HAS storm control. It is not a missing feature and the capability
// map has said so since this connector was written:
//
//	"StormControl":     true,
//	"StormControlPort": false,
//
// (instanton.go, in the Features block, with the comment "Storm control exists
// but is GLOBAL, not per port: an intent that sets it per port is not applicable
// here"). The product's own UI carries ONE storm-control setting for the whole
// switch — a rate and a set of classes applied device-wide — and no per-interface
// dimension anywhere.
//
// So [driver.StormControlApplier] cannot be honoured here, and this file makes
// the refusal explicit rather than leaving the interface unimplemented. Two
// reasons, and the second is the one that matters:
//
//   - an UNIMPLEMENTED interface answers a type assertion with `false`, which the
//     API layer renders as "connector declares storm control writable but Admin
//     has no ApplyStormControl" — a sentence about meshcraft's plumbing, offered
//     to an operator whose actual question is about their switch;
//   - an IMPLEMENTED refusal answers with [driver.NotSupported], which carries the
//     platform's own reason, and which the caller can put on screen verbatim. That
//     is the whole purpose of the type (cf. driver/connector.go: "it must answer
//     "is there anything to expect here?""). The answer here is no, permanently,
//     and an operator is entitled to be told that rather than to keep looking.
//
// # WHY THE GLOBAL SETTING IS NOT WRITTEN INSTEAD
//
// It would be the single most damaging substitution available anywhere in this
// contract. An operator limiting broadcast on ONE access port — a jack behind
// which something is misbehaving — would have that limit applied to EVERY PORT on
// the switch, uplinks included. The symptom is not a port that stays too busy; it
// is a whole cabinet's traffic policed at a threshold chosen for one desk socket,
// on a range with no console to log into and find out why.
//
// The contract's per-port intent has no way to say "and this will hit the other
// twenty-three jacks", so there is nothing this connector could write that means
// what the caller asked. It writes nothing.
//
// # And there is a second, independent reason not to try
//
// Even the global setting's `wcd` section name has never been captured on a 1930.
// Every field name that would go into such a write would be a hypothesis, and
// this platform's characteristic failure — a section that exists, written with a
// field name it does not know, answering `statusCode 0` and changing nothing —
// is the one dhcpsnoop.go's header is entirely built around. A guessed global
// storm-control write would combine a wrong SCOPE with an unproven FIELD.
//
// # Consequence for the capability declarations
//
// [WritableExtras] does not carry "storm_control" (capabilities.go), so
// [Driver1930.WritesExtra] answers false — and it must go on answering false even
// after a capture, because there would still be nothing per-port to write.
// [Driver1930.StormControlCaps] declares an EMPTY class list, which is what tells
// a screen "this platform cannot" as opposed to "meshcraft has not proven it
// yet". Those two answers exist for exactly this case (driver/stormcontrol.go,
// § on StormControlCaps).

import (
	"context"

	"meshcraft/internal/driver"
)

// Compile-time: this Admin answers the storm-control contract — with a refusal.
//
// The READER is deliberately NOT implemented. There is nothing per-port to read,
// and a reader returning an empty state would be indistinguishable from a device
// with no limiters configured — which would put "no storm control on this port"
// on screen for twenty-four ports of a switch that may well be policing all of
// them from its global setting.
var _ driver.StormControlApplier = (*Admin)(nil)

// stormRefusal is the sentence an operator gets, written once so the capability
// answer and the write refusal cannot become two different accounts of one
// hardware fact.
const stormRefusal = "storm control on this range is a SINGLE DEVICE-WIDE setting: the " +
	"product's UI carries one rate and one set of classes for the whole switch, with no " +
	"per-interface dimension at all (Capabilities().Features, \"StormControlPort: false\", " +
	"declared when this connector was written). Applying a per-port request by writing the " +
	"global setting would limit EVERY jack on the box — uplinks included — at a threshold " +
	"chosen for one desk socket, and on a range with no SSH, no Telnet and no serial port " +
	"there is no console to log into and find out why the cabinet went slow. Nothing was " +
	"written. A device-wide storm limit remains available from the switch's own web UI"

// StormControlCaps declares that this PLATFORM expresses no per-port class.
//
// An EMPTY Classes list is the answer that lets a screen say "this switch cannot"
// rather than "not verified yet". Those are different sentences and only one of
// them is true here — no capture will ever change this one.
func (d *Driver1930) StormControlCaps() driver.StormControlCaps {
	return driver.StormControlCaps{
		Why: []string{stormRefusal + "."},
	}
}

// ApplyStormControl refuses, and reaches nothing.
//
// The refusal is returned BEFORE any request, which is what
// [driver.NotSupported]'s own comment asks for: reaching a device to ask for
// something already known to be impossible costs a session on a platform that
// grants five of them, and produces a message about transport where the fact is
// about a capability.
func (a *Admin) ApplyStormControl(ctx context.Context,
	plan driver.StormControlPlan) (driver.ApplyResult, error) {

	var out driver.ApplyResult
	a.step(false, "per-port storm control refused", stormRefusal)
	return out, driver.NotSupported("aruba-instant-on-1930",
		"limit storms on one port", stormRefusal)
}

// Package modelid derives a hardware IDENTITY from the model label a device —
// or an operator — declared, without ever rewriting that label (cf. ADR-0014).
//
// # Why this exists
//
// A vendor can publish TWO naming grammars for one switch, and they enter by
// different discovery paths. The Instant On 1930 is the witness: the same twelve
// machines arrived in the inventory under three strings —
//
//	Aruba Instant On 1930 48G 4SFP/SFP+ Switch                        (provisioning API)
//	HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 10G 1930      (sysDescr)
//	HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A   (sysDescr, with SKU)
//
// — and nothing in the product said they named the same hardware. Both grammars
// MUST keep being parsed (internal/driver/instanton/inspect.go: without the
// second one, no regex matches and the four SFP+ cages are adopted as copper).
// What was missing is not a reading, it is an identity.
//
// # What the identity is, and what it deliberately is not
//
// It is BRAND + FAMILY + FACEPLATE GEOMETRY, and the geometry is read out of the
// label itself rather than looked up in a catalogue — so nothing is invented and
// there is no table to keep up to date.
//
// It is NOT the vendor catalogue reference ("JL685A"). That one is the most
// precise thing available, which is exactly why it cannot serve: it is absent
// from two of the three forms, so an identity resting on it would split the
// group again at the moment it is needed — the same bug, written differently.
// The rule is general: the canonical identity is the most precise thing EVERY
// path can say, not the most precise thing ONE path says. The reference is kept
// beside it, in [ID.SKU], optional and never required.
//
// # Degradation
//
// A label no family rule recognizes IS its own identity: [ID.Label] is the raw
// string, untouched, and [ID.Key] is that string normalized. Two interdictions
// make this clause worth writing down — never return an EMPTY key (every
// unknown would collapse into one phantom model) and never MERGE two unknowns
// (different hardware would be declared identical). An unknown stays alone.
//
// A family rule is added on EVIDENCE — two labels observed for one piece of
// hardware — never by anticipation. Everywhere else the raw label is already
// canonical, which is the case of every other vendor in the reference
// installation today.
package modelid

import (
	"regexp"
	"strconv"
	"strings"
)

// ID is what a raw model label identifies.
type ID struct {
	// Key: the identity itself. Two devices carrying the same Key are the same
	// model, whichever grammar each of them was recorded in.
	//
	// COMPUTED ON READ, NEVER STORED. That is not an implementation detail: a
	// persisted copy would freeze the derivation of the day it was written,
	// whereas a derived one improves for every existing row the day it learns
	// one more grammar.
	Key string
	// Label: the model as a screen must NAME it — the canonical form when a
	// family rule matched, the raw label otherwise.
	//
	// Never a shortened form: fitting a label into a tile is a different job,
	// and web/src/modelLabel.ts does it (over this value, not instead of it).
	Label string
	// SKU: the vendor catalogue reference the label carried, when it carried one
	// ("JL685A"). Deliberately NOT part of Key — see the package comment.
	SKU string
}

// Of derives the identity of a model label. An empty label has no identity, and
// says so rather than inventing one.
//
// The vendor field is NOT an input, on purpose: it is assigned by the
// fingerprint matcher and is noisier than the label ("Aruba (HPE)", "HPE",
// "Aruba Instant On" all designate the same maker), so keying on it would split
// what it is meant to join.
func Of(model string) ID {
	// Collapse whitespace first: a label that travelled through a UI carries
	// non-breaking spaces and double spaces, and two labels differing only by
	// those are the same label.
	var raw = strings.Join(strings.Fields(model), " ")
	if raw == "" {
		return ID{}
	}
	var id = ID{Key: slug(raw), Label: raw, SKU: skuOf(raw)}
	var rule = ruleFor(raw)
	if rule != nil {
		var label, ok = rule(raw)
		if ok {
			id.Label = label
			id.Key = slug(label)
		}
	}
	return id
}

// --- Family rules -------------------------------------------------------
//
// One entry per vendor grammar family we have EVIDENCE for. A rule returns the
// canonical label, or false when the label matches the family but carries too
// little to identify the hardware — in which case the raw label stays, which is
// the honest answer rather than a half-guess.

type familyRule func(raw string) (string, bool)

func ruleFor(raw string) familyRule {
	var low = strings.ToLower(raw)
	if strings.Contains(low, "instant on") {
		return instantOn
	}
	return nil
}

var reInstantOnFamily = regexp.MustCompile(`\b(18|19)30\b`)

// instantOn recomposes the name HPE/Aruba's own provisioning API emits, which is
// also the form eight of the twelve fielded devices already carry: the family
// number and the geometry, both read out of the label that arrived.
func instantOn(raw string) (string, bool) {
	var fam = reInstantOnFamily.FindStringSubmatch(raw)
	if fam == nil {
		return "", false
	}
	var g, ok = geometryOf(raw)
	if !ok {
		// "Instant On 1930" with no port count identifies a RANGE, not a switch:
		// a 24-port and a 48-port must not share an identity.
		return "", false
	}
	var sfp = strconv.Itoa(g.SFP) + "SFP"
	if g.SFPPlus {
		sfp += "/SFP+"
	}
	return "Aruba Instant On " + fam[1] + "30 " +
		strconv.Itoa(g.Copper) + "G " + sfp + " Switch", true
}

// --- Geometry -----------------------------------------------------------

var (
	// The sysDescr grammar: "48p Gigabit 4p SFP+".
	reCopperP  = regexp.MustCompile(`(\d+)p\s+(?:Gigabit|Gig|G\b)`)
	reSFPPlusP = regexp.MustCompile(`(\d+)p\s+SFP\+`)
	reSFPP     = regexp.MustCompile(`(\d+)p\s+SFP\b`)
	// The product-name grammar, which counts WITHOUT the "p": "48G 4SFP/SFP+".
	// Fallbacks only, tried after the forms above — and the ORDER is the whole
	// point: "10G" inside a sysDescr would otherwise read as ten copper ports.
	// Same two grammars and same ordering rule as instanton/parsePorts, read for
	// a different purpose (a name, not a faceplate) and therefore not shared:
	// that connector is on its way out of this repository (Phase 4).
	reCopperG  = regexp.MustCompile(`\b(\d+)G\b`)
	reSFPGlued = regexp.MustCompile(`\b(\d+)SFP\b`)
)

type geometry struct {
	Copper  int
	SFP     int
	SFPPlus bool
}

// geometryOf reads a port geometry out of a label. False means the label does
// not state one — never a zero-port switch.
func geometryOf(label string) (geometry, bool) {
	var g geometry
	var m = reCopperP.FindStringSubmatch(label)
	if m == nil {
		m = reCopperG.FindStringSubmatch(label)
	}
	if m != nil {
		g.Copper, _ = strconv.Atoi(m[1])
	}
	if m = reSFPPlusP.FindStringSubmatch(label); m != nil {
		g.SFP, _ = strconv.Atoi(m[1])
		g.SFPPlus = true
	} else if m = reSFPP.FindStringSubmatch(label); m != nil {
		g.SFP, _ = strconv.Atoi(m[1])
	} else if m = reSFPGlued.FindStringSubmatch(label); m != nil {
		g.SFP, _ = strconv.Atoi(m[1])
		// "4SFP/SFP+": cages that also take SFP+. The plus decides the speed.
		g.SFPPlus = strings.Contains(label, "SFP+")
	}
	return g, g.Copper > 0 && g.SFP > 0
}

// --- Catalogue reference ------------------------------------------------

// HPE/Aruba part numbers: JL685A, J9772A. Narrow on purpose — a pattern loose
// enough to catch every vendor's reference would catch firmware versions too,
// and a wrong SKU is worse than none.
var reSKU = regexp.MustCompile(`\b(J[LH]?\d{3,4}[A-Z])\b`)

func skuOf(label string) string {
	var m = reSKU.FindStringSubmatch(label)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// --- Normalization ------------------------------------------------------

var reNotAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes a key out of a label: lowercase, every run of punctuation and
// whitespace becoming a single dash. Two labels differing only by case,
// spacing or separators are the same label.
func slug(s string) string {
	return strings.Trim(reNotAlnum.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

package discovery

// Brand is the grouping identity of /add/reset — ADR-0010.
//
// The screen used to offer the five SERIAL catalogue brands plus a sentinel
// that switched it into a free server-side search, which is where the other
// fifteen procedures lived, seven of them Verified. It now lists everything,
// grouped by Brand. That turns a field into a contract, and this file is the
// contract: a Brand slug is an identity, Vendor stays a label.

import (
	"strings"
	"testing"
)

// Every listed procedure carries a Brand. An omission is the one mistake this
// change makes easy — a new procedure appended with the vendor filled in and
// the slug forgotten would fall out of the brand list without a word.
func TestEveryProcedureHasABrand(t *testing.T) {
	var p ResetProcedure
	for _, p = range resetProcedures {
		if p.Brand == "" {
			t.Errorf("%s: no Brand — it would be unreachable from /add/reset", p.Driver)
		}
		if p.Vendor == "" {
			t.Errorf("%s: no Vendor — the brand would be listed nameless", p.Driver)
		}
		if p.Family == "" {
			t.Errorf("%s: no Family — the model line would be blank", p.Driver)
		}
	}
}

// A slug, not a sentence: it is matched by code and never shown, so it may
// not pick up spaces, case or punctuation the way a label does.
func TestBrandIsASlug(t *testing.T) {
	var p ResetProcedure
	for _, p = range resetProcedures {
		var c rune
		for _, c = range p.Brand {
			var ok = (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'
			if !ok {
				t.Errorf("%s: Brand %q is not a slug (lowercase, digits, hyphen)", p.Driver, p.Brand)
				break
			}
		}
	}
}

// THE POINT OF THE WHOLE FIELD. Vendor is the string the chip shows, and it
// will be reworded. Two procedures grouped under one brand must agree on it,
// or the brand list would show one of the two names depending on declaration
// order — and a copy edit on a single literal would be the cause.
func TestOneBrandHasOneVendorLabel(t *testing.T) {
	var labels = map[string]string{}
	var owner = map[string]string{}
	var p ResetProcedure
	for _, p = range resetProcedures {
		var seen, ok = labels[p.Brand]
		if !ok {
			labels[p.Brand] = p.Vendor
			owner[p.Brand] = p.Driver
			continue
		}
		if seen != p.Vendor {
			t.Errorf(
				"brand %q is labelled %q by %s and %q by %s — pick one, or split the brand",
				p.Brand, seen, owner[p.Brand], p.Vendor, p.Driver,
			)
		}
	}
}

// The merge ADR-0010 made deliberately, and the one it refused.
//
// ProCurve and ArubaOS-CX are one product line continued, and the lid says HP,
// HPE or Aruba depending on the year: one brand. Comware is the 3Com/H3C
// lineage, and somebody holding a 5130 looks for H3C: a separate one. Both are
// decisions, so both are pinned — a later "tidy-up" that merges all three
// would be undoing an arbitration, not a typo.
func TestHPEIsOneBrandAndComwareIsNot(t *testing.T) {
	var procurve = ResetProcedureFor("hpe-procurve")
	var arubacx = ResetProcedureFor("aruba-cx")
	var comware = ResetProcedureFor("h3c-comware")

	if procurve.Brand != arubacx.Brand {
		t.Errorf(
			"hpe-procurve (%q) and aruba-cx (%q) must share one brand",
			procurve.Brand, arubacx.Brand,
		)
	}
	if comware.Brand == procurve.Brand {
		t.Errorf("h3c-comware must stay its own brand, not %q", comware.Brand)
	}
}

// The debt ADR-0010 was written to pay: these seven are Verified and were NOT
// in the serial catalogue, so the old screen kept them behind "another
// platform". They are the reason the list stopped being derived from the
// catalogue, and a procedure of theirs losing its Verified flag or its brand
// is the regression to catch.
func TestTheVerifiedProceduresOutsideTheSerialCatalogue(t *testing.T) {
	var d string
	for _, d = range []string{
		"hpe-procurve", "mikrotik-routeros", "ubiquiti",
		"extreme-exos", "huawei-vrp", "h3c-comware", "cumulus-sonic",
	} {
		var p = ResetProcedureFor(d)
		if p.Driver != d {
			t.Errorf("%s: no procedure carried any more (got %q)", d, p.Driver)
			continue
		}
		if !p.Verified {
			t.Errorf("%s: was Verified; if that changed, say so in a commit of its own", d)
		}
		if p.Brand == "" {
			t.Errorf("%s: Verified and brandless — unreachable from the brand list", d)
		}
	}
}

// genericReset answers for an unidentified device and is NEVER listed: it has
// no brand on purpose, and it must not leak into the catalogue the screen
// enumerates — "Unidentified device" is not a brand to pick.
func TestGenericIsNotListed(t *testing.T) {
	if genericReset.Brand != "" {
		t.Errorf("genericReset carries brand %q; it is not a brand", genericReset.Brand)
	}
	var p ResetProcedure
	for _, p = range resetProcedures {
		if p.Driver == "generic" {
			t.Fatal("genericReset is in resetProcedures — it would be offered as a platform")
		}
	}
}

// The screen now filters in the browser over the whole set, so the empty
// query must keep returning it whole. A `q` that silently became mandatory
// would empty the screen rather than error.
func TestEmptyQueryReturnsEverything(t *testing.T) {
	if len(SearchResetProcedures("")) != len(resetProcedures) {
		t.Fatalf(
			"empty query returned %d of %d procedures",
			len(SearchResetProcedures("")), len(resetProcedures),
		)
	}
	if len(SearchResetProcedures("   ")) != len(resetProcedures) {
		t.Fatal("a whitespace query must read as empty, not as a needle")
	}
}

// Word-wise AND, which `SearchSelect` now mirrors in the browser (ADR-0010).
// The two matchers have to agree, and this is the half that can be tested
// here: "cisco 2960" must not return every Cisco.
func TestSearchIsWordWise(t *testing.T) {
	var hits = SearchResetProcedures("cisco 2960")
	if len(hits) != 1 || hits[0].Driver != "cisco-ios" {
		t.Fatalf("\"cisco 2960\" returned %d hits, wanted cisco-ios alone", len(hits))
	}
	// An alias is a model number somebody reads off a lid: it must find the
	// procedure without the vendor's name.
	hits = SearchResetProcedures("s5720")
	if len(hits) != 1 || hits[0].Driver != "huawei-vrp" {
		t.Fatalf("\"s5720\" returned %d hits, wanted huawei-vrp alone", len(hits))
	}
	// And the brand slug is NOT part of the haystack: searching "whitebox"
	// must not be the way to find Cumulus, because nothing shows that word.
	hits = SearchResetProcedures("whitebox")
	if len(hits) != 0 {
		t.Fatalf("the Brand slug leaked into the search haystack (%d hits)", len(hits))
	}
	if !strings.Contains(strings.ToLower(ResetProcedureFor("cumulus-sonic").Family), "sonic") {
		t.Fatal("cumulus-sonic must stay findable by the OS name, which is what is shown")
	}
}

package api

// The offer registry, and the reaping rule that made a healthy line vanish.

import (
	"testing"
	"time"
)

// AN IDLE LINE STAYS LISTED.
//
// Regression guard for the bug an operator hit within minutes of the feature
// shipping: "I can't see the serial server on another web client". The liveness
// stamp was refreshed ONLY in the relay's read loop, so it advanced only when
// the DEVICE sent bytes — and a serial console sitting at a prompt sends
// nothing. A connected, healthy, idle line therefore aged out of the list after
// two minutes with the host's browser still open and the switch still cabled.
//
// The stamp is now advanced by a keepalive ping on the host socket, which is the
// thing the offer actually depends on. This asserts the registry keeps an offer
// whose stamp is fresh regardless of traffic, and drops one whose host has gone.
func TestIdleOfferIsNotReaped(t *testing.T) {
	var g = &serialRegistry{offers: map[string]*serialOffer{}}
	var live = &serialOffer{
		ID: "live", SiteID: "site-1", Label: "quiet console",
		seen: time.Now(), // freshly pinged, no device traffic at all
	}
	g.put(live)

	var out = g.list("site-1")
	if len(out) != 1 {
		t.Fatalf("an idle but live offer was not listed: %d offers", len(out))
	}

	// A host that went away stops being pinged, so its stamp stops advancing and
	// it ages out. That is the case the TTL is FOR.
	var dead = &serialOffer{
		ID: "dead", SiteID: "site-1", Label: "closed laptop",
		seen: time.Now().Add(-2 * serialOfferTTL),
	}
	g.put(dead)
	out = g.list("site-1")
	if len(out) != 1 || out[0].ID != "live" {
		t.Fatalf("reaping is wrong: got %d offers, want only the live one", len(out))
	}
	var _, stillThere = g.get("dead")
	if stillThere {
		t.Error("the aged-out offer was listed away but left in the registry")
	}
}

// OFFERS DO NOT CROSS SITES.
//
// The id is unguessable, but an authorization resting on unguessability is not
// an authorization — so the list is filtered by site and the attach handler
// checks the offer's own site before it looks at anything else.
func TestOffersAreScopedToTheirSite(t *testing.T) {
	var g = &serialRegistry{offers: map[string]*serialOffer{}}
	g.put(&serialOffer{ID: "a", SiteID: "site-1", seen: time.Now()})
	g.put(&serialOffer{ID: "b", SiteID: "site-2", seen: time.Now()})

	var out = g.list("site-1")
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("site-1 sees %d offers, want only its own", len(out))
	}
	if len(g.list("site-3")) != 0 {
		t.Error("a site with no offers was shown somebody else's")
	}
}

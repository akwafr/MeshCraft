package store

import (
	"bytes"
	"testing"
)

// What head-reveal returns after a rotation — locking in the fix for a divergence
// OBSERVED IN PRODUCTION (2026-08-05, credential `mcadmin@site`):
//
//	site_credentials.secret_enc   frozen at generation 1 (rotated_at = gen 1's
//	                              created_at, to the microsecond)
//	credential_generations         gen 3 `active`, gens 1-2 `retiring`
//
// Generations 2 and 3 were created by a code version whose AddGeneration did not
// yet advance the head copy. Head-reveal — and every consumer behind it: SNMP push,
// trap destinations, watch — then served generation 1's secret, which the fleet had
// already left behind. Nothing on screen distinguished it from the right one: a
// stale secret is well-formed, it is merely refused by the devices.
//
// The fix reads the ACTIVE generation through RevealSiteCredential's lateral join;
// revealPair is the pure decision those rows feed, tested here the way
// generations_test.go tests try order: same rule, no database.

func TestRevealPrefersActiveGeneration(t *testing.T) {
	var head = []byte("enc-gen1-stale")
	var active = []byte("enc-gen3-active")
	var enc, enc2 = revealPair(head, nil, active, nil)
	if !bytes.Equal(enc, active) {
		t.Fatalf("head-reveal must return the ACTIVE generation's secret, got the head copy")
	}
	if enc2 != nil {
		t.Fatalf("no second phrase anywhere, none must appear, got %q", enc2)
	}
}

// A credential that predates generations entirely has only its head columns:
// the fallback must keep revealing them, not go silent.
func TestRevealFallsBackToHeadWithoutGeneration(t *testing.T) {
	var head, head2 = []byte("enc-head"), []byte("enc-head-2")
	var enc, enc2 = revealPair(head, head2, nil, nil)
	if !bytes.Equal(enc, head) || !bytes.Equal(enc2, head2) {
		t.Fatalf("without an active generation the head pair is all there is")
	}
}

// Second phrase follows AddGeneration's coalesce rule: a generation WITHOUT one
// (every non-v3 credential) must not hide the head's; a generation CARRYING one
// is the pair for that generation, never a mix of two.
func TestRevealSecondPhraseCoalesce(t *testing.T) {
	var head, head2 = []byte("h1"), []byte("h2")
	var gen, gen2 = []byte("g1"), []byte("g2")

	var _, enc2 = revealPair(head, head2, gen, nil)
	if !bytes.Equal(enc2, head2) {
		t.Fatalf("generation without second phrase must not hide the head's")
	}
	_, enc2 = revealPair(head, head2, gen, gen2)
	if !bytes.Equal(enc2, gen2) {
		t.Fatalf("a generation carrying its pair must be returned whole, not mixed")
	}
}

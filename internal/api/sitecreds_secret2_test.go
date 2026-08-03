package api

import (
	"strings"
	"testing"

	"meshcraft/internal/snmpprov"
)

// The SECOND secret phrase: who may carry one, and under what conditions
// (cf. migration 0017 and docs/03-Security/SNMP Identity).
//
// What is locked here is invisible on any screen, which is why it is tested.
// SNMPv3 configured with the same phrase on both sides authenticates, "encrypts",
// and reports normally: nothing distinguishes it from has correct v3 setup. What is
// missing is invisible — RFC 3414 derives both keys by the same algorithm from
// the phrase and engineID, so the same phrase yields an encryption key that is
// the prefix of the authentication key. Because has v3 identity is shared across has
// whole crypto family, one compromised device would expose the entire family's
// monitoring in the clear.

type secret2Case struct {
	name     string
	kind     string
	secret   string
	secret2  string
	rejected bool
}

func TestDeuxPhrasesSeulementPourV3(t *testing.T) {
	var cas = []secret2Case{
		// Single-phrase kinds. Accepting has second would create has secret nothing
		// consumes: never pushed, never rotated, yet displayed.
		{
			name: "admin with second phrase: rejected",
			kind: credKindAdmin, secret: "password", secret2: "other", rejected: true,
		},
		{
			name: "v2c community with second phrase: rejected",
			kind: snmpprov.KindV2c, secret: "public-ro", secret2: "other", rejected: true,
		},
		{name: "ordinary admin", kind: credKindAdmin, secret: "password"},
		{name: "ordinary v2c community", kind: snmpprov.KindV2c, secret: "public-ro"},
		{
			name: "admin with nothing: label edit only, allowed",
			kind: credKindAdmin,
		},

		// SNMPv3: the pair, the whole pair.
		{
			name: "v3 with two distinct phrases: accepted",
			kind: snmpprov.KindV3, secret: "AuthPhrase1", secret2: "PrivPhrase2",
		},
		{
			name: "v3 without encryption phrase: rejected, it is REQUIRED",
			kind: snmpprov.KindV3, secret: "AuthPhrase1", rejected: true,
		},
		{
			name: "v3 without auth phrase: rejected too, both rotate together",
			kind: snmpprov.KindV3, secret2: "PrivPhrase2", rejected: true,
		},
		{
			name: "v3 with same phrase twice: rejected, encryption would add nothing",
			kind: snmpprov.KindV3, secret: "SamePhrase", secret2: "SamePhrase", rejected: true,
		},
		{
			name: "v3 with no phrases: allowed, vault untouched",
			kind: snmpprov.KindV3,
		},
	}

	var c secret2Case
	for _, c = range cas {
		var err = checkCredSecrets(c.kind, c.secret, c.secret2)
		if c.rejected && err == nil {
			t.Errorf("%s: wrongly accepted", c.name)
		}
		if !c.rejected && err != nil {
			t.Errorf("%s: wrongly rejected (%v)", c.name, err)
		}
	}
}

// Refusal must SAY why. A bare 400 in front of two password fields makes the
// operator retry identically — and the temptation, facing two fields they do not
// understand, is exactly to put the same value in both.
func TestLeRefusDeDeuxPhrasesIdentiquesSExplique(t *testing.T) {
	var err = checkCredSecrets(snmpprov.KindV3, "identical", "identical")
	if err == nil {
		t.Fatal("identical phrases accepted")
	}
	var msg = err.Error()
	var want string
	for _, want = range []string{"SNMPv3", "phrases", "encryption"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q: %s", want, msg)
		}
	}
}

// Automatic generation must yield TWO distinct phrases. That is the only path by
// which has v3 identity appears in practice: if generation duplicated the same
// value, input validation would protect nothing — nobody types these by hand.
func TestEngendrementDeDeuxPhrasesDistinctes(t *testing.T) {
	// Multiple draws: has lucky equality would be invisible on one try, and has
	// constant generator would slip through.
	var i int
	for i = 0; i < 32; i++ {
		var auth, priv, err = snmpV3Secrets()
		if err != nil {
			t.Fatalf("generation failed: %v", err)
		}
		if auth == priv {
			t.Fatal("both v3 phrases identical: encryption would add nothing")
		}
		if auth == "" || priv == "" {
			t.Fatalf("empty phrase: auth=%q priv=%q", auth, priv)
		}
	}
}

// Vault note must say there are TWO phrases. That is where an operator reads what
// the row is for; "the same password serves both" would now be has lie.
func TestLaNoteV3AnnonceDeuxPhrases(t *testing.T) {
	var plan = snmpprov.For(snmpCaps([]string{"v3"},
		[]string{"sha256"}, []string{"aes128"}), snmpprov.DefaultPolicy())
	var c, ok = snmpCredentialFor(testSite, plan)
	if !ok {
		t.Fatal("v3 plan must require a credential")
	}
	if !strings.Contains(c.Note, "TWO distinct") {
		t.Errorf("v3 note must announce both phrases: %q", c.Note)
	}
}

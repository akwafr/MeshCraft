package pwgen

import (
	"strings"
	"testing"
)

// The alphabet is an operational constraint, not a detail: a character outside
// this list makes part of the fleet reject the secret, and the device keeps the
// old one while meshcraft believes otherwise.
func TestOnlyAlphanumeric(t *testing.T) {
	var i int
	for i = 0; i < 200; i++ {
		var s, err = New(DefaultLength)
		if err != nil {
			t.Fatal(err)
		}
		if len(s) != DefaultLength {
			t.Fatalf("length %d, expected %d", len(s), DefaultLength)
		}
		var c rune
		for _, c = range s {
			if !strings.ContainsRune(Alphabet, c) {
				t.Fatalf("character outside alphabet: %q in %q", c, s)
			}
		}
	}
}

// Some devices require composition. An all-lowercase secret would be rejected on
// apply — and the failure would only appear in the field.
func TestComposition(t *testing.T) {
	var i int
	for i = 0; i < 200; i++ {
		var s, _ = New(DefaultLength)
		if !strings.ContainsAny(s, "abcdefghijklmnopqrstuvwxyz") ||
			!strings.ContainsAny(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
			!strings.ContainsAny(s, "0123456789") {
			t.Fatalf("incomplete composition: %q", s)
		}
	}
}

// Two calls must not yield the same secret: trivial, and exactly the kind of
// triviality a draw refactor breaks.
func TestDistinct(t *testing.T) {
	var seen = map[string]bool{}
	var i int
	for i = 0; i < 500; i++ {
		var s, _ = New(DefaultLength)
		if seen[s] {
			t.Fatalf("collision on %q after %d draws", s, i)
		}
		seen[s] = true
	}
}

// Rejecting bytes >= 248 is what avoids modulo bias. Without it, the first eight
// letters would appear ~1.6 times more often than the others. The test does not
// prove uniformity — it detects gross imbalance.
func TestNoGrossBias(t *testing.T) {
	var count = map[rune]int{}
	var total int
	var i int
	for i = 0; i < 2000; i++ {
		var s, _ = New(32)
		var c rune
		for _, c = range s {
			count[c]++
			total++
		}
	}
	var expected = float64(total) / float64(len(Alphabet))
	var c rune
	for _, c = range Alphabet {
		var got = float64(count[c])
		if got < expected*0.75 || got > expected*1.25 {
			t.Fatalf("%q appeared %d times, expected ~%.0f", c, count[c], expected)
		}
	}
}

func TestRefusesTooShort(t *testing.T) {
	var _, err = New(4)
	if err == nil {
		t.Fatal("a 4-character secret must be refused")
	}
}

// THE ALPHABET ITSELF is verified, not only secrets drawn from it.
//
// This is not a repeat of TestOnlyAlphanumeric: another project decision now
// relies on this content. The password policy technical tag (internal/pwpolicy)
// does NOT name the "alphanumeric only" platform constraint, because everything
// from here already satisfies it — adding punctuation to this alphabet would make
// that reasoning false and would file, under a shared tag, secrets part of the
// fleet would reject.
func TestAlphabetWithoutPunctuation(t *testing.T) {
	if len(Alphabet) != 62 {
		t.Fatalf("alphabet of %d characters, expected 62 (26+26+10)", len(Alphabet))
	}
	var seen = map[byte]bool{}
	var i int
	for i = 0; i < len(Alphabet); i++ {
		var c = Alphabet[i]
		var alnum = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alnum {
			t.Fatalf("non-alphanumeric character in alphabet: %q", c)
		}
		if seen[c] {
			t.Fatalf("duplicate character in alphabet: %q — draw would be biased", c)
		}
		seen[c] = true
	}
}

// Length is the project's only rule for "what length under which bounds". Two
// competing calculations, and someday the tag announces 15 while the secret is
// actually 16: the credential would be filed under a name that lied.
func TestLengthUnderConstraints(t *testing.T) {
	var cas = []struct {
		name     string
		min, max int
		want     int
	}{
		{"no known bounds (RouterOS): project default length", 0, 0, DefaultLength},
		{"wide cap (Instant On, 8..64): project default length", 8, 64, DefaultLength},
		{"tight cap (SwOS, 0..15): drop to cap", 0, 15, 15},
		{"cap exactly at default: nothing changes", 0, 16, DefaultLength},
		{"floor above default: rise to floor", 20, 64, 20},
		{"floor without known cap", 24, 0, 24},
		{"floor equals cap: only one length possible", 12, 12, 12},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var got, err = Length(c.min, c.max)
			if err != nil {
				t.Fatalf("bounds %d..%d wrongly refused: %v", c.min, c.max, err)
			}
			if got != c.want {
				t.Errorf("length %d, expected %d", got, c.want)
			}
			// And what the generator produces must really fit the bounds: the device
			// counts bytes, not us.
			var s, genErr = NewBounded(c.min, c.max)
			if genErr != nil {
				t.Fatalf("generation wrongly refused: %v", genErr)
			}
			if len(s) != c.want {
				t.Errorf("secret of %d characters, expected %d", len(s), c.want)
			}
			if c.max > 0 && len(s) > c.max {
				t.Errorf("secret of %d characters above cap %d: "+
					"the connector would reject it before send", len(s), c.max)
			}
			if len(s) < c.min {
				t.Errorf("secret of %d characters below floor %d", len(s), c.min)
			}
		})
	}
}

// AN IMPOSSIBLE CONSTRAINT IS AN ERROR, never a silently truncated secret.
// Truncating would set a password the device rejects AFTER write, i.e. at the
// worst moment; yielding eight characters on a platform capped at six would leave
// meshcraft believing a secret was set when it was not.
func TestImpossibleConstraint(t *testing.T) {
	var cas = []struct {
		name     string
		min, max int
	}{
		{"floor above cap", 20, 15},
		{"cap below meshcraft absolute floor", 0, 6},
		{"cap of 1 character", 1, 1},
		{"negative bounds: false capability", -1, 0},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var _, err = Length(c.min, c.max)
			if err == nil {
				t.Fatalf("bounds %d..%d accepted: a silently truncated secret "+
					"is worse than a refusal", c.min, c.max)
			}
			var _, genErr = NewBounded(c.min, c.max)
			if genErr == nil {
				t.Error("NewBounded must refuse what Length refuses")
			}
		})
	}
}

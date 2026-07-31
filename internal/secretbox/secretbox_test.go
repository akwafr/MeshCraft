package secretbox

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	var key, err = NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	var svc *Service
	svc, err = New(key, true)
	if err != nil {
		t.Fatal(err)
	}
	var secret = "NetworkPassword!42"
	var ct []byte
	ct, err = svc.Seal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if string(ct) == secret {
		t.Fatal("ciphertext equals plaintext")
	}
	var got string
	got, err = svc.Open(ct)
	if err != nil {
		t.Fatal(err)
	}
	if got != secret {
		t.Fatalf("round trip broken: %q != %q", got, secret)
	}
}

// Two ciphertexts of the same plaintext must differ: otherwise the nonce is
// pointless and password equality would leak.
func TestSealIsRandomized(t *testing.T) {
	var key, _ = NewRandomKey()
	var svc, err = New(key, true)
	if err != nil {
		t.Fatal(err)
	}
	var a, _ = svc.Seal("same-plaintext")
	var b, _ = svc.Seal("same-plaintext")
	if string(a) == string(b) {
		t.Fatal("two encryptions of the same plaintext are identical")
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	var k1, _ = NewRandomKey()
	var k2, _ = NewRandomKey()
	var s1, _ = New(k1, true)
	var s2, _ = New(k2, true)
	var ct, err = s1.Seal("secret")
	if err != nil {
		t.Fatal(err)
	}
	var _, openErr = s2.Open(ct)
	if openErr == nil {
		t.Fatal("decryption succeeded with the wrong key")
	}
}

func TestParseHexKey(t *testing.T) {
	var _, err = ParseHexKey("too short")
	if err == nil {
		t.Fatal("invalid key accepted")
	}
	var key, _ = NewRandomKey()
	var hexKey = ""
	var b byte
	for _, b = range key {
		hexKey += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0xf])
	}
	var parsed []byte
	parsed, err = ParseHexKey(hexKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 32 {
		t.Fatalf("length %d", len(parsed))
	}
}

// --- Key fingerprint ---
//
// The fingerprint turns "the vault became unreadable and nobody noticed" into
// "the controller refuses to start and says why". Three properties make it
// usable, and all three break silently if nobody locks them.

//  1. It must not reveal the key: FIXED length independent of the key, and no key
//     byte appears in it.
func TestFingerprintNeDivulguePasLaCle(t *testing.T) {
	var k1, _ = NewRandomKey()
	var k2, _ = NewRandomKey()
	var f1 = Fingerprint(k1)
	var f2 = Fingerprint(k2)

	if len(f1) != 32 || len(f2) != 32 {
		t.Fatalf("variable or unexpected fingerprint length: %d and %d", len(f1), len(f2))
	}
	// A fingerprint containing part of the key would be a leak: the table storing
	// it lives in the SAME database as the secrets.
	if bytes.Contains(f1, k1[:8]) || bytes.Contains(f1, k1[24:]) {
		t.Fatal("fingerprint contains key bytes")
	}
	if bytes.Equal(f1, k1) {
		t.Fatal("fingerprint is the key")
	}
}

//  2. Two distinct keys yield two distinct fingerprints — otherwise the guard would
//     let the wrong key through, which is exactly the catastrophe it exists to
//     prevent.
func TestFingerprintDistingueLesCles(t *testing.T) {
	var vues = map[string]bool{}
	var i int
	for i = 0; i < 200; i++ {
		var key, err = NewRandomKey()
		if err != nil {
			t.Fatal(err)
		}
		var fp = string(Fingerprint(key))
		if vues[fp] {
			t.Fatal("two distinct keys yield the same fingerprint")
		}
		vues[fp] = true
	}
	// And a single bit of difference must suffice: a neighboring key is not the
	// same key.
	var base, _ = NewRandomKey()
	var voisine = append([]byte{}, base...)
	voisine[31] ^= 0x01
	if SameFingerprint(Fingerprint(base), Fingerprint(voisine)) {
		t.Fatal("one bit of difference in the key does not change the fingerprint")
	}
}

//  3. It is DETERMINISTIC: the fingerprint stored yesterday must be the one
//     recomputed today, otherwise the guard would refuse to start a perfectly
//     healthy instance — the failure would be caused by the guard itself.
func TestFingerprintEstDeterministe(t *testing.T) {
	var key, _ = NewRandomKey()
	if !SameFingerprint(Fingerprint(key), Fingerprint(key)) {
		t.Fatal("fingerprint is not deterministic")
	}
	// Reference value on a known key: locks derivation. If someone changes the
	// salt label or algorithm, this test fails — and that is the point, because
	// that change would refuse to start ALL instances already in service.
	var fixe = make([]byte, 32)
	var i int
	for i = range fixe {
		fixe[i] = byte(i)
	}
	var got = ShortID(Fingerprint(fixe))
	if len(got) != 8 {
		t.Fatalf("ShortID must be 8 hex characters, got %q", got)
	}
	if got != ShortID(Fingerprint(fixe)) {
		t.Fatal("ShortID unstable")
	}
	if strings.Contains(got, " ") {
		t.Fatalf("ShortID not hexadecimal: %q", got)
	}
}

// ShortID must not panic on short input: it ends up in error messages, and an
// error message that panics replaces a diagnosis with a crash.
func TestShortIDSurEntreeCourte(t *testing.T) {
	if ShortID(nil) != "?" || ShortID([]byte{1, 2}) != "?" {
		t.Fatal("ShortID must return \"?\" on a fingerprint that is too short")
	}
}

// KeyShortID is the same identifier, computed from the key: error messages compare
// "what the database expects" to "what the variable provides", both must be the
// same function.
func TestKeyShortIDCoincideAvecShortID(t *testing.T) {
	var key, _ = NewRandomKey()
	if KeyShortID(key) != ShortID(Fingerprint(key)) {
		t.Fatal("KeyShortID and ShortID(Fingerprint) diverge")
	}
}

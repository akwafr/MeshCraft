package main

// Tests for the refusal to start.
//
// What they protect: the key guard has value only if it actually refuses.
// A safeguard that lets things through is worse than no safeguard, because
// it makes people stop being wary. Both directions of the trap are covered:
//
//   - MESHCRAFT_SECRET_KEY set (or changed) on a database sealed with a
//     different key;
//   - the variable REMOVED after a switchover, which used to draw a fresh
//     key and reseal on top of everything.
//
// No database is needed: that is the whole point of the keyStore interface.
// A test for the refusal to start that doesn't run for lack of a PostgreSQL
// at hand protects nothing.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
)

// fakeKeyStore mimics the strict minimum: the instance_secrets row, the
// fingerprint row, and a sample of sealed values.
type fakeKeyStore struct {
	instance    map[string][]byte
	fingerprint []byte
	algoErr     error
	sealed      [][]byte

	// setCalls counts fingerprint recordings: a refusal that would still
	// write the fingerprint would be a disaster disguised as a refusal.
	setCalls int
	// putCalls counts key creations: the guard must NEVER draw a fresh key
	// on a database that is already sealed.
	putCalls int
}

func newFake() *fakeKeyStore {
	return &fakeKeyStore{instance: map[string][]byte{}}
}

func (f *fakeKeyStore) CredentialsKeyFingerprint(ctx context.Context) ([]byte, error) {
	if f.algoErr != nil {
		return nil, f.algoErr
	}
	return f.fingerprint, nil
}

func (f *fakeKeyStore) SetCredentialsKeyFingerprint(ctx context.Context, fp []byte) error {
	f.setCalls++
	f.fingerprint = fp
	return nil
}

func (f *fakeKeyStore) InstanceSecretValue(ctx context.Context, name string) ([]byte, error) {
	var v, ok = f.instance[name]
	if !ok {
		return nil, store.ErrNotFound
	}
	return v, nil
}

func (f *fakeKeyStore) PutInstanceSecret(ctx context.Context, name string, value []byte) error {
	f.putCalls++
	// `on conflict do nothing` as in the real store: we don't overwrite.
	var _, exists = f.instance[name]
	if !exists {
		f.instance[name] = value
	}
	return nil
}

func (f *fakeKeyStore) SealedSample(ctx context.Context, perColumn int) ([][]byte, error) {
	if perColumn >= len(f.sealed) {
		return f.sealed, nil
	}
	return f.sealed[:perColumn], nil
}

// hexKey produces the hexadecimal form expected in MESHCRAFT_SECRET_KEY.
func hexKey(t *testing.T, key []byte) string {
	t.Helper()
	var out = ""
	var b byte
	for _, b = range key {
		out += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0xf])
	}
	return out
}

// sealWith fills the sample with values sealed by `key`.
func sealWith(t *testing.T, f *fakeKeyStore, key []byte, plaintexts ...string) {
	t.Helper()
	var box, err = secretbox.New(key, false)
	if err != nil {
		t.Fatal(err)
	}
	var c string
	for _, c = range plaintexts {
		var enc []byte
		enc, err = box.Seal(c)
		if err != nil {
			t.Fatal(err)
		}
		f.sealed = append(f.sealed, enc)
	}
}

func newKey(t *testing.T) []byte {
	t.Helper()
	var k, err = secretbox.NewRandomKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// --- Direction 1: an environment key is presented that did not seal the vault ---

// The central trap of the set: an instance in service, whose vault is
// sealed by the key stored in the database, on which MESHCRAFT_SECRET_KEY
// gets set. Before the fingerprint existed, the controller would start, the
// product would look like it was working, and monitoring would disappear
// without a word.
func TestRefuseWhenEnvSetOnSealedDB(t *testing.T) {
	var f = newFake()
	var enBase = newKey(t)
	f.instance[store.CredentialsKeyName] = enBase
	sealWith(t, f, enBase, "snmp-community", "AdminPassword42")
	// The real scenario at update time: the database does not have a
	// fingerprint yet.
	f.fingerprint = nil

	var other = newKey(t)
	var _, _, err = resolveVaultKey(context.Background(), f, hexKey(t, other))
	if err == nil {
		t.Fatal("startup accepted with a key that does not open the vault")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong error: %v", err)
	}
	// The message must name both the cause AND the fix: "this won't start"
	// with no instructions leaves the operator to remove the variable at
	// random.
	assertMentions(t, err.Error(), "-rekey", "sealed with a DIFFERENT key", "Back up")
	// And above all: the fingerprint of the WRONG key must not have been
	// engraved.
	if f.setCalls != 0 {
		t.Fatal("the fingerprint was recorded despite the refusal")
	}
}

// Same trap, one step later: the fingerprint exists, and the value of the
// variable changes (manual rotation, copy-paste from another environment,
// restoring a .env from another instance).
func TestRefuseWhenFingerprintDiffers(t *testing.T) {
	var f = newFake()
	var sealing = newKey(t)
	f.instance[store.CredentialsKeyName] = sealing
	f.fingerprint = secretbox.Fingerprint(sealing)
	sealWith(t, f, sealing, "snmp-community")

	var other = newKey(t)
	var _, _, err = resolveVaultKey(context.Background(), f, hexKey(t, other))
	if err == nil {
		t.Fatal("startup accepted despite a differing fingerprint")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong error: %v", err)
	}
	// Both fingerprints must appear: that is what makes the diagnosis
	// possible without guessing.
	assertMentions(t, err.Error(),
		secretbox.ShortID(f.fingerprint), secretbox.KeyShortID(other),
		"MESHCRAFT_SECRET_KEY", "-rekey")
	if f.setCalls != 0 {
		t.Fatal("the fingerprint was rewritten despite the refusal")
	}
}

// --- Direction 2: the variable is REMOVED after the switchover ---

// The reverse case, just as dangerous: after a successful switchover, the
// key is no longer in the database. A reboot without the variable used to
// draw a FRESH key, store it, and reseal on top at the first secret saved
// afterward — an irreparable mix. Here: refusal, and NO fresh key drawn.
func TestRefuseWhenEnvRemovedAfterSwitch(t *testing.T) {
	var f = newFake()
	var switched = newKey(t)
	// State left by the rekey process: fingerprint recorded, no more key in
	// the database, vault sealed with the environment key.
	f.fingerprint = secretbox.Fingerprint(switched)
	sealWith(t, f, switched, "snmp-community")

	var _, _, err = resolveVaultKey(context.Background(), f, "")
	if err == nil {
		t.Fatal("startup accepted with no key while the vault is sealed")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong error: %v", err)
	}
	assertMentions(t, err.Error(), "MESHCRAFT_SECRET_KEY", "no longer a key in the database")
	// THE point of the test: no bogus key written to the database. A fresh
	// key left there would be mistaken for the real one by the next reader.
	if f.putCalls != 0 {
		t.Fatal("a fresh key was drawn and stored in the database")
	}
	if f.setCalls != 0 {
		t.Fatal("the fingerprint was rewritten despite the refusal")
	}
}

// Variant with no fingerprint: someone manually deleted the key row on a
// database whose vault is not empty. There is nothing left to read, and
// drawing a fresh key would condemn the secrets.
func TestRefuseWhenKeyGoneWithoutFingerprint(t *testing.T) {
	var f = newFake()
	sealWith(t, f, newKey(t), "snmp-community")

	var _, _, err = resolveVaultKey(context.Background(), f, "")
	if err == nil {
		t.Fatal("startup accepted with no key on a non-empty vault")
	}
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong error: %v", err)
	}
	if f.putCalls != 0 {
		t.Fatal("a fresh key was drawn on a database whose vault is full")
	}
}

// --- Paths that must PASS, silently ---

// A database already sealed by the key in the database, with no
// fingerprint: this is the state of every existing instance at the moment
// of the update. The fingerprint is recorded, silently, after verifying
// that the key opens the vault.
func TestRecordFingerprintWhenAbsent(t *testing.T) {
	var f = newFake()
	var enBase = newKey(t)
	f.instance[store.CredentialsKeyName] = enBase
	sealWith(t, f, enBase, "snmp-community", "AdminPassword42")

	var key, fromEnv, err = resolveVaultKey(context.Background(), f, "")
	if err != nil {
		t.Fatalf("startup refused on a healthy database: %v", err)
	}
	if fromEnv {
		t.Fatal("fromEnv should be false")
	}
	if string(key) != string(enBase) {
		t.Fatal("wrong key retained")
	}
	if f.setCalls != 1 {
		t.Fatalf("fingerprint recorded %d times, expected 1", f.setCalls)
	}
	if !secretbox.SameFingerprint(f.fingerprint, secretbox.Fingerprint(enBase)) {
		t.Fatal("the recorded fingerprint is not that of the active key")
	}
}

// First startup of a virgin database, with no variable: the key is drawn
// and stored, the fingerprint recorded. This is the historical behavior; it
// must not regress.
func TestFirstStartEmptyDB(t *testing.T) {
	var f = newFake()
	var key, fromEnv, err = resolveVaultKey(context.Background(), f, "")
	if err != nil {
		t.Fatalf("first startup refused: %v", err)
	}
	if fromEnv || len(key) != 32 {
		t.Fatalf("unexpected key: fromEnv=%v len=%d", fromEnv, len(key))
	}
	if f.putCalls != 1 {
		t.Fatalf("key stored %d times, expected 1", f.putCalls)
	}
	if f.setCalls != 1 {
		t.Fatalf("fingerprint recorded %d times, expected 1", f.setCalls)
	}
}

// First installation WITH the variable: empty vault, nothing to contradict.
// The guard must not turn the recommended mode into an obstacle.
func TestFirstStartWithEnvVar(t *testing.T) {
	var f = newFake()
	var env = newKey(t)
	var key, fromEnv, err = resolveVaultKey(context.Background(), f, hexKey(t, env))
	if err != nil {
		t.Fatalf("first installation with the variable refused: %v", err)
	}
	if !fromEnv || string(key) != string(env) {
		t.Fatal("the environment key was not retained")
	}
	if f.putCalls != 0 {
		t.Fatal("a key was stored in the database even though the environment supplies one")
	}
	if !secretbox.SameFingerprint(f.fingerprint, secretbox.Fingerprint(env)) {
		t.Fatal("incorrect fingerprint")
	}
}

// After the switchover, with the right key: normal startup, and the
// fingerprint is not rewritten (nothing to change).
func TestAfterSwitchWithCorrectKey(t *testing.T) {
	var f = newFake()
	var switched = newKey(t)
	f.fingerprint = secretbox.Fingerprint(switched)
	sealWith(t, f, switched, "snmp-community")

	var key, fromEnv, err = resolveVaultKey(context.Background(), f, hexKey(t, switched))
	if err != nil {
		t.Fatalf("startup refused with the correct key: %v", err)
	}
	if !fromEnv || string(key) != string(switched) {
		t.Fatal("incorrect key")
	}
	if f.setCalls != 0 {
		t.Fatal("fingerprint rewritten needlessly")
	}
}

// A database rekeyed manually outside of meshcraft — the vault opens with
// the environment key, but the old key row is still lying around and there
// is no fingerprint. The vault is the judge: it says yes, so we start.
func TestManualSwitchRecognized(t *testing.T) {
	var f = newFake()
	var env = newKey(t)
	f.instance[store.CredentialsKeyName] = newKey(t) // forgotten old key
	sealWith(t, f, env, "snmp-community")

	var _, _, err = resolveVaultKey(context.Background(), f, hexKey(t, env))
	if err != nil {
		t.Fatalf("startup refused even though the key opens the vault: %v", err)
	}
	if !secretbox.SameFingerprint(f.fingerprint, secretbox.Fingerprint(env)) {
		t.Fatal("incorrect fingerprint")
	}
}

// An invalid hex key must be refused BEFORE any access to the database: a
// mistyped variable (63 characters, a stray space) must not trigger an
// "unreadable vault" diagnosis, which would send the operator down the
// wrong path.
func TestInvalidHexKeyRejected(t *testing.T) {
	var f = newFake()
	var _, _, err = resolveVaultKey(context.Background(), f, "not-a-key")
	if err == nil {
		t.Fatal("invalid key accepted")
	}
	if !errors.Is(err, secretbox.ErrKeySize) {
		t.Fatalf("wrong error: %v", err)
	}
	if errors.Is(err, ErrKeyMismatch) {
		t.Fatal("a malformed key must not present itself as an incompatible key")
	}
}

// A fingerprint written by an unknown derivation (database from a more
// recent version) must produce a plain message, not an "incompatible key"
// refusal that would send someone to rekey a perfectly healthy database.
func TestUnknownDerivationSurfaces(t *testing.T) {
	var f = newFake()
	f.algoErr = errors.New("key fingerprint computed by an unknown derivation")
	var _, _, err = resolveVaultKey(context.Background(), f, "")
	if err == nil {
		t.Fatal("algorithm error swallowed")
	}
	if !strings.Contains(err.Error(), "unknown derivation") {
		t.Fatalf("message lost: %v", err)
	}
}

func assertMentions(t *testing.T, s string, fragments ...string) {
	t.Helper()
	var frag string
	for _, frag = range fragments {
		if !strings.Contains(s, frag) {
			t.Errorf("message does not mention %q.\nMessage:\n%s", frag, s)
		}
	}
}

// Package secretbox encrypts application secrets (standard credentials used by
// discovery) with AES-256-GCM.
//
// Where the key lives, and what that really protects:
//
//   - MESHCRAFT_SECRET_KEY set (64 hexadecimal characters): the key is outside
//     the database. A PostgreSQL dump leak does not reveal secrets. This is the
//     recommended production mode.
//   - variable absent: a key is drawn at random on first startup and stored in
//     the instance_secrets table, therefore IN the same database as the secrets
//     it protects. This avoids plaintext storage and log leaks, but does not
//     resist someone who obtains the entire database. The controller logs it at
//     startup and the UI shows it: better to say so than let people believe
//     otherwise.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var ErrKeySize = errors.New("invalid encryption key: 32 bytes expected (64 hex characters)")

type Service struct {
	aead cipher.AEAD
	// FromEnv says whether the key comes from the environment (therefore outside
	// the database).
	FromEnv bool
}

// New builds the service from a 32-byte key.
func New(key []byte, fromEnv bool) (*Service, error) {
	if len(key) != 32 {
		return nil, ErrKeySize
	}
	var block, err = aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	var aead cipher.AEAD
	aead, err = cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{aead: aead, FromEnv: fromEnv}, nil
}

// ParseHexKey reads a hexadecimal key (64 characters).
func ParseHexKey(s string) ([]byte, error) {
	var raw, err = hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKeySize, err)
	}
	if len(raw) != 32 {
		return nil, ErrKeySize
	}
	return raw, nil
}

// NewRandomKey draws a 32-byte key.
func NewRandomKey() ([]byte, error) {
	var key = make([]byte, 32)
	var _, err = io.ReadFull(rand.Reader, key)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// fingerprintLabel salts derivation with a product constant. Two different uses
// of the same key must never produce the same fingerprint: the day meshcraft
// derives something else from this key, the label separates them. The `/v1` is
// for when derivation changes — the algo is named in the database beside the
// fingerprint, so a v2 would be recognizable instead of taken for a different
// key.
const fingerprintLabel = "meshcraft/credentials-key-fingerprint/v1"

// FingerprintAlgo names the derivation stored in the database beside the fingerprint.
const FingerprintAlgo = "hmac-sha256-v1"

// Fingerprint derives a ONE-WAY fingerprint of the encryption key.
//
// What it is for: the controller records in the database the fingerprint of the
// key that sealed the vault, and refuses to start if presented with another.
// Without this guard, setting MESHCRAFT_SECRET_KEY on an existing instance
// makes the ENTIRE vault unreadable in silence — supervision stops without a
// word (internal/api/watch_handlers.go drops the device whose community cannot
// be decrypted), fleet access credentials become inaccessible, and the outage is
// discovered screen by screen, days later.
//
// What it is NOT: it does not recover the key. HMAC-SHA-256 is one-way, and the
// key is 32 random bytes — there is no dictionary to try, unlike a password
// fingerprint. That is what allows storing it in the same database as secrets:
// it does not help whoever obtains the dump.
//
// The HMAC direction is deliberate: the constant is the HMAC key, the secret is
// the message. We do not want a simple SHA-256 of the key, which would be the
// same value for any other software computing "the hash of this key file".
func Fingerprint(key []byte) []byte {
	var mac = hmac.New(sha256.New, []byte(fingerprintLabel))
	mac.Write(key)
	return mac.Sum(nil)
}

// SameFingerprint compares in constant time. Constant time is not strictly
// needed here (a fingerprint is not a secret), but comparing fingerprints any
// way other than hmac.Equal is a habit we do not want to acquire.
func SameFingerprint(a, b []byte) bool { return hmac.Equal(a, b) }

// ShortID renders a key identifier readable in a log or error message: the first
// 4 bytes of the fingerprint, in hexadecimal.
//
// Publishing 4 bytes of an HMAC-SHA-256 of a random 256-bit key does not bring
// anyone closer to the key, and lets the operator say "the database expects
// a1b2c3d4, my variable gives 9f8e7d6c" — turning "it won't start" into a
// diagnosis.
func ShortID(fingerprint []byte) string {
	if len(fingerprint) < 4 {
		return "?"
	}
	return hex.EncodeToString(fingerprint[:4])
}

// KeyShortID is the shortcut for a key rather than a fingerprint.
func KeyShortID(key []byte) string { return ShortID(Fingerprint(key)) }

// Seal encrypts a secret. The nonce is prefixed to the ciphertext.
func (s *Service) Seal(plaintext string) ([]byte, error) {
	var nonce = make([]byte, s.aead.NonceSize())
	var _, err = io.ReadFull(rand.Reader, nonce)
	if err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Open decrypts a secret produced by Seal.
func (s *Service) Open(ciphertext []byte) (string, error) {
	var ns = s.aead.NonceSize()
	if len(ciphertext) < ns {
		return "", errors.New("ciphertext truncated")
	}
	var plain, err = s.aead.Open(nil, ciphertext[:ns], ciphertext[ns:], nil)
	if err != nil {
		return "", errors.New("decryption failed (key changed?)")
	}
	return string(plain), nil
}

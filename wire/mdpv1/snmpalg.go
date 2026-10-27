// SNMPv3 algorithm names cross the wire in three places — Capabilities (what
// the device offers), SNMPIdentity (what the controller chose), and a driver's
// read-back of what the device actually holds — and they arrive in three
// spellings: "SHA-256", "sha256", "SHA 256". NormalizeAlg is the ONE canonical
// form, and equality is defined on it. A driver that compares raw strings
// reports a drift on every poll against a device that prints its own spelling;
// a controller that negotiates on raw strings ranks "sha256" above "SHA-256".
//
// It lives in the wire module, not in the controller's internal/snmpv3,
// because both sides of the driver boundary compare — and two copies of a
// normalizer are two vocabularies the day one of them learns a new separator.

package mdpv1

import "strings"

// NormalizeAlg puts an SNMPv3 algorithm name in canonical form: lowercase, no
// separator ("SHA-256" → "sha256", "AES 128" → "aes128", "" → ""). It never
// validates: an unknown algorithm normalizes to an unknown string, and
// refusing it is the negotiator's decision, not the spelling's.
func NormalizeAlg(a string) string {
	var r = strings.NewReplacer("-", "", "_", "", " ", "")
	return r.Replace(strings.ToLower(strings.TrimSpace(a)))
}

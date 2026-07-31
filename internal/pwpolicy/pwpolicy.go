// Package pwpolicy names a platform's PASSWORD POLICY and makes that name a
// technical tag.
//
// # The defect this package closes
//
// `driver.AdminAccount` has always declared `PasswordMin`, `PasswordMax` and
// `AlphanumericOnly`. Connectors fill them carefully — SwOS caps at 15
// characters, Instant On allows 8 to 64 — and NOBODY read them when generating
// a secret. Observed on hardware: applying credentials on a MikroTik CSS326 was
// rejected by its own connector ("new password: 17 characters, the device
// accepts at most 15"), meaning NO password could be set on that line once the
// site secret exceeded 15 characters. The guard did its job; upstream nothing
// consulted the capability.
//
// # Why a tag, not a global cap
//
// Pulling the whole generator down to the smallest cap encountered would make an
// entire fleet pay for one model's constraint: a site with a CSS326 and
// forty-eight Instant On ports would see all forty-nine secrets drop to 15
// characters. The answer is the tagging mechanism, designed for exactly this
// case (docs/01-Architecture/Etiquettes et portee des identifiants section 1,
// which names "a line that caps passwords at 16 characters").
//
// This package is therefore to admin passwords what `internal/snmpv3` is to
// SNMPv3 identity: the SOLE JUDGE of a reserved namespace. An operational tag
// ("regie", "scene") must never be read as a technical constraint, nor the
// reverse — and the stored tag is always the OUTPUT of [Policy.Tag], never a
// typed string: a spelling variant would fabricate a second credential for the
// same constraint, and the fleet would end up split in two with no screen saying
// so.
//
// # What the tag derives from: the CONSTRAINT, not the model
//
// The tag is `pwpolicy-len<N>`, where N is the length of secrets meshcraft will
// generate under this policy — not a commercial reference like `…-css326`. Same
// reasoning as the earlier v3 work, where the key derives from the ALGORITHM
// PAIR and not the model: what must share a secret is what shares the
// constraint. One tag per model would yield as many secrets as catalog
// references, where two lines with the same bounds can perfectly well share
// theirs — and multiplying secrets means multiplying what must be rotated,
// revealed, and monitored.
//
// Three deliberate shape consequences:
//
//   - LENGTH alone, not both bounds (`pwpolicy-min0-max15`): what decides
//     compatibility of an already generated secret is its size. Two policies
//     (0..15) and (15..15) accept exactly the same 15-character secret; giving
//     them two names would fabricate two secrets for one need. `len15` is the
//     exact equivalence class: two platforms share a tag if and only if meshcraft
//     would generate the same secret format for them;
//   - `AlphanumericOnly` does NOT enter the name, though it does enter
//     [Policy.Accepts]. The reason is that `internal/pwgen` generates ONLY
//     alphanumeric: every secret from meshcraft already satisfies that
//     constraint, and naming it would split the fleet between two tags whose
//     secrets would be interchangeable. It serves instead to judge an EXISTING
//     TYPED secret, which may well contain punctuation — and that judgment
//     triggers generation;
//   - the name shows the constraint at a glance, as `snmpv3-sha-des` shows the
//     algorithms in play. An operator reading `pwpolicy-len15` on a switch knows
//     what to make of it, which is not the case for an opaque identifier.
package pwpolicy

import (
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/pwgen"
)

// TagPrefix prefixes every tag generated here, and delimits the RESERVED
// namespace: nothing starting with it may be typed by hand, neither as an
// equipment tag nor as a credential scope (cf. `checkCredScope`).
const TagPrefix = "pwpolicy-"

// tagLenPrefix : what follows the prefix. Naming the length rather than leaving
// it bare (`pwpolicy-15`) avoids taking the number for a sequence number, a site
// identifier, or a priority — three possible readings of an unnamed digit, and
// two of them would lead to deleting the "useless" tag.
const tagLenPrefix = "len"

// Policy is a platform's password policy as its connector declares it.
//
// The three fields are copied from [driver.AdminAccount] without interpretation,
// except one convention of the contract: 0 means "unknown, therefore not
// constrained". An unknown cap is not guessed — assuming low would fabricate
// technical tags for the whole fleet, assuming high would reject applies; the
// contract chose "not constrained", and that is the only reading that leaves
// the fleet as-is until a connector reports something.
type Policy struct {
	// Min / Max : device bounds. 0 = unknown.
	Min int
	Max int
	// AlphanumericOnly : the device rejects punctuation. Does NOT participate in
	// the tag name (see package header), but does participate in judging an
	// existing secret.
	AlphanumericOnly bool
}

// For reads a platform's policy from its connector capabilities.
//
// A one-liner, and it has a reason to exist: this is the ONLY place in the repo
// that translates `driver.AdminAccount` into a policy. The day the contract gains
// a field — a forbidden character list, a composition requirement — there is one
// place and only one to update, and tags already on the fleet are re-read from
// that same place.
func For(a driver.AdminAccount) Policy {
	return Policy{Min: a.PasswordMin, Max: a.PasswordMax,
		AlphanumericOnly: a.AlphanumericOnly}
}

// Length returns the length of secrets to generate under this policy, or says
// why none can be generated (cf. [pwgen.Length], which carries the rule).
func (p Policy) Length() (int, error) {
	return pwgen.Length(p.Min, p.Max)
}

// Tag names the policy. This name will carry the fleet credential.
//
// Returns an ERROR when the policy is unsatisfiable, rather than a tag that
// cannot be filled: a credential targeting `pwpolicy-len6` would be a secret
// meshcraft refuses to generate, therefore an inert row in the vault — and an
// inert row in a vault looks like a row that works.
func (p Policy) Tag() (string, error) {
	var n, err = p.Length()
	if err != nil {
		return "", err
	}
	return TagPrefix + tagLenPrefix + strconv.Itoa(n), nil
}

// Accepts says whether an EXISTING secret may be set on a platform governed by
// this policy, and why not otherwise.
//
// This is the question that triggers the whole mechanism: does the secret the
// site intends for this device pass? Three possible refusals, and the third is
// why `AlphanumericOnly` exists in this type without appearing in the tag name —
// a MANUALLY TYPED secret may contain punctuation, whereas everything meshcraft
// generates is alphanumeric.
//
// The error NEVER contains the secret, only its length. It propagates to an HTTP
// response and to the log: a message quoting the password would publish it
// everywhere the error passes.
func (p Policy) Accepts(secret string) error {
	// An empty secret is not "too short", it is absent: saying otherwise would
	// look up a policy where generation is missing.
	if secret == "" {
		return fmt.Errorf("no secret to set")
	}
	// Length is counted in BYTES, as the device does: switch interfaces bound a
	// buffer, not code points. A ten-character secret with accented letters is
	// twenty bytes, and twenty is what the device will count.
	var n = len(secret)
	if p.Max > 0 && n > p.Max {
		return fmt.Errorf("secret is %d characters long: the device accepts at most %d",
			n, p.Max)
	}
	if n < p.Min {
		return fmt.Errorf("secret is %d characters long: the device requires at least %d",
			n, p.Min)
	}
	if p.AlphanumericOnly && !isAlphanumeric(secret) {
		return fmt.Errorf("secret contains punctuation: this platform " +
			"accepts letters and digits only")
	}
	return nil
}

// isAlphanumeric : pwgen's alphabet, and nothing else. Deliberately limited to
// ASCII — an accented letter is "alphanumeric" in Unicode and yet passes on
// almost no switch interface.
func isAlphanumeric(s string) bool {
	var i int
	for i = 0; i < len(s); i++ {
		if !strings.ContainsRune(pwgen.Alphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// IsTechnicalTag recognizes a tag from this namespace.
//
// Deliberately BROADER than [ParseTag]: it returns true for everything bearing
// the reserved prefix, including a form this package would not generate
// (`pwpolicy-`, `pwpolicy-len0`, `pwpolicy-machin`). That is what input
// validation needs: refusing the ENTIRE namespace by hand is the only way to
// prevent a spelling variant from coexisting with the canonical form. A guard
// that forbade only valid forms would let through `pwpolicy-len015`, which looks
// exactly like the official tag and would be set on no device.
func IsTechnicalTag(tag string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(tag)), TagPrefix)
}

// ParseTag re-reads a tag generated here and returns the length it names.
//
// Returns false for everything else — an operational tag, a spelling variant, a
// length below meshcraft's floor. This is the other half of the sole judge: the
// round trip length -> name -> length must be CLOSED, otherwise two spellings
// of the same need would produce two credentials and nobody would see which is
// set on what.
func ParseTag(tag string) (int, bool) {
	var t = strings.ToLower(strings.TrimSpace(tag))
	if !strings.HasPrefix(t, TagPrefix+tagLenPrefix) {
		return 0, false
	}
	var digits = strings.TrimPrefix(t, TagPrefix+tagLenPrefix)
	// `Atoi` would accept "+15", "015" and "15" as the same number, which would
	// reopen exactly the door this judge closes: three spellings for one
	// constraint. We therefore require the canonical form that Tag() produces.
	var n, err = strconv.Atoi(digits)
	if err != nil || strconv.Itoa(n) != digits {
		return 0, false
	}
	if n < pwgen.MinLength {
		return 0, false
	}
	return n, true
}

// PolicyForTag returns the policy a technical tag describes: "exactly this
// length".
//
// Used to judge what may be stored UNDER that tag — a manual rotation, for
// example. The policy is stricter than the originating platform's (min = max =
// the named length), and that is intentional: the tag is shared by platforms
// whose exact bounds are no longer known at this point in the code, and the only
// value we know they ALL accept is the one used to group them.
func PolicyForTag(tag string) (Policy, bool) {
	var n, ok = ParseTag(tag)
	if !ok {
		return Policy{}, false
	}
	return Policy{Min: n, Max: n, AlphanumericOnly: true}, true
}

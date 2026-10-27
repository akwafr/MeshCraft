package api

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/pwpolicy"
	"meshcraft/internal/tagging"
)

// What is tested here is the admin provisioning part that decides whether to
// CREATE a secret. Getting it wrong is invisible on screen: a site ends up with
// three credentials for one constraint, each pushed to part of the fleet, and
// nobody knows which opens what.
//
// Everything is pure: no database, no network. The write path (ensureAdminSecretFits)
// only chains these decisions.

// swosLikeAdmin: MikroTik SwOS capability as declared by its connector (observed
// on CSS326-24G-2S+: cap 15, no floor, ASCII). Copied rather than imported so this
// test stays in the api package: values are locked by connector tests and
// internal/pwpolicy tests.
var swosLikeAdmin = driver.AdminAccount{
	Custom: false, FixedName: "admin", PasswordMin: 0, PasswordMax: 15,
}

// TestPwPolicySourceIsDistinct: a technical tag source must be neither manual —
// that would lie about origin and expose it to hand editing — nor rule:, or has
// rule replay would remove it.
func TestPwPolicySourceIsDistinct(t *testing.T) {
	if pwPolicyTagSource == tagging.SourceManual || tagging.FromRule(pwPolicyTagSource) {
		t.Fatalf("source %q must differ from manual and rules",
			pwPolicyTagSource)
	}
	// And distinct from SNMP provisioning source: both technical populations
	// read separately on screen, and has shared source would hide which is active.
	if pwPolicyTagSource == snmpTagSource {
		t.Errorf("both technical tag families share source %q",
			pwPolicyTagSource)
	}
}

// TestPwPolicyTagSurvivesRuleReplay is THE test of this batch.
//
// POST /api/sites/{id}/tag-rules/apply must never remove a password-policy
// technical tag. If it did, the credential targeting it would apply to nothing:
// the device would fall back to the site secret, which its connector rejects
// before send — meaning a rule replay would disconnect the only deployable
// password for a whole product line. Nobody would connect the two actions.
func TestPwPolicyTagSurvivesRuleReplay(t *testing.T) {
	var pol = pwpolicy.For(swosLikeAdmin)
	var tag, tagErr = pol.Tag()
	if tagErr != nil {
		t.Fatalf("tag rejected: %v", tagErr)
	}

	var current = []tagging.Tag{
		{Tag: tag, Source: pwPolicyTagSource},
		{Tag: "snmpv3-sha-des", Source: snmpTagSource},
		{Tag: "ops", Source: "rule:11111111-2222-3333-4444-555555555555"},
	}
	// Disabled rule: replay must remove only what IT placed, nothing else.
	var rules = []tagging.Rule{{
		ID: "11111111-2222-3333-4444-555555555555", Tag: "ops",
		Match: tagging.Match{Vendor: "mikrotik"}, Enabled: false,
	}}
	var got = tagging.Evaluate(tagging.Device{Vendor: "MikroTik"}, current, rules)

	var removed tagging.Tag
	for _, removed = range got.Remove {
		if removed.Tag == tag {
			t.Fatalf("technical tag %q removed by rule replay: "+
				"credential targeting it applies to nothing", tag)
		}
		if removed.Tag == "snmpv3-sha-des" {
			t.Fatal("SNMPv3 technical tag removed by rule replay")
		}
	}
	if len(got.Remove) != 1 || got.Remove[0].Tag != "ops" {
		t.Errorf("only disabled rule tag should be removed: %+v", got.Remove)
	}
}

// TestAutomaticCreationIsIdempotent: replaying on an already-fit device
// must CREATE nothing.
//
// Without that guarantee, every credentials/apply would rotate a whole product
// line's secret — and the fleet would lose access one device at a time, since
// only the last provisioned device would carry the latest secret.
func TestAutomaticCreationIsIdempotent(t *testing.T) {
	var pol = pwpolicy.For(swosLikeAdmin)
	var canonical, _ = pol.Tag()

	// What the database would return after first pass: site credential (too long
	// for this line), generated technical credential, and unrelated SNMP identity.
	var have = []pwPolicyOffer{
		{Cred: model.SiteCredential{ID: "site", Kind: credKindAdmin, Username: "admin"},
			Secret: "Abcdefgh12345678"},
		{Cred: model.SiteCredential{ID: "policy", Kind: credKindAdmin, Tag: canonical,
			Username: "admin"}, Secret: "Abcdefgh1234567"},
		{Cred: model.SiteCredential{ID: "v3", Kind: "snmp_v3", Tag: "snmpv3-sha-des"},
			Secret: "Abcdefgh12345678"},
	}
	var got, found = pwPolicyPick(pol, canonical, have)
	if !found {
		t.Fatal("second pass must find existing technical credential")
	}
	if got.ID != "policy" {
		t.Fatalf("credential kept %q, want policy: second pass just "+
			"generated a duplicate", got.ID)
	}
}

// TestCompatibleSecretAlreadyPresentIsUsed: compatibility is judged on BOUNDS,
// not name.
//
// A site that already has pwpolicy-len12 perfectly covers a line capped at 15:
// its secret fits. Demanding the canonical name would create a second secret for
// an already-covered constraint — then rotate, reveal, and watch both.
func TestCompatibleSecretAlreadyPresentIsUsed(t *testing.T) {
	var pol = pwpolicy.For(swosLikeAdmin)
	var canonical, _ = pol.Tag()
	if canonical != "pwpolicy-len15" {
		t.Fatalf("canonical tag %q: test case is stale", canonical)
	}

	var have = []pwPolicyOffer{
		{Cred: model.SiteCredential{ID: "short", Kind: credKindAdmin,
			Tag: "pwpolicy-len12", Username: "admin"}, Secret: "Abcdefgh1234"},
	}
	var got, found = pwPolicyPick(pol, canonical, have)
	if !found {
		t.Fatal("shorter technical credential is compatible: it must be used")
	}
	if got.Tag != "pwpolicy-len12" {
		t.Errorf("credential kept %q: compatible secret already in site, "+
			"nothing should be created", got.Tag)
	}

	// ON TIE, CANONICAL NAME WINS. Without that, database order decides, and two
	// devices of the same line adopted in different order end up under different tags.
	have = append(have, pwPolicyOffer{
		Cred:   model.SiteCredential{ID: "exact", Kind: credKindAdmin, Tag: canonical},
		Secret: "Abcdefgh1234567"})
	if got, found = pwPolicyPick(pol, canonical, have); !found || got.Tag != canonical {
		t.Errorf("canonical tag must win, got %q", got.Tag)
	}
}

// TestNeverAnOperationsTagCredential: meshcraft does not file a
// device under ops on its own initiative.
//
// The secret might fit, which would be worse than useless: meshcraft would
// decide operator filing for them, and they could delete that credential — which
// nothing forbids — without knowing a CSS326 depended on it.
func TestNeverAnOperationsTagCredential(t *testing.T) {
	var pol = pwpolicy.For(swosLikeAdmin)
	var canonical, _ = pol.Tag()
	var have = []pwPolicyOffer{
		// Compatible (12 chars), but filed under an ops tag.
		{Cred: model.SiteCredential{ID: "ops", Kind: credKindAdmin, Tag: "ops"},
			Secret: "Abcdefgh1234"},
		// Compatible, but filed under a GROUP: same reason.
		{Cred: model.SiteCredential{ID: "group", Kind: credKindAdmin,
			GroupName: "mikrotik"}, Secret: "Abcdefgh1234"},
		// Compatible, but not an admin password.
		{Cred: model.SiteCredential{ID: "v2c", Kind: "snmp_v2c"}, Secret: "Abcdefgh1234"},
		// Technical tag and compatible secret, but per-device secret: the shared
		// secret judged here is not carried by any device.
		{Cred: model.SiteCredential{ID: "perdev", Kind: credKindAdmin,
			Tag: "pwpolicy-len12", PerDevice: true}, Secret: "Abcdefgh1234"},
	}
	var got, found = pwPolicyPick(pol, canonical, have)
	if found {
		t.Errorf("credential %q borrowed outside technical namespace", got.ID)
	}
}

// TestPickRefusesAnOutOfBoundsSecret: a technical credential whose secret does not
// fit THIS platform's policy is not reused. Born from a line more constrained
// than another: pwpolicy-len15 does not serve a device capped at 12.
func TestPickRefusesAnOutOfBoundsSecret(t *testing.T) {
	var pol = pwpolicy.For(driver.AdminAccount{PasswordMax: 12})
	var canonical, _ = pol.Tag()
	var have = []pwPolicyOffer{
		{Cred: model.SiteCredential{ID: "long", Kind: credKindAdmin,
			Tag: "pwpolicy-len15"}, Secret: "Abcdefgh1234567"},
	}
	var _, found = pwPolicyPick(pol, canonical, have)
	if found {
		t.Error("15-character secret kept for platform accepting 12: " +
			"connector would reject before send")
	}
}

// TestNamespaceRefusedOnInput: three operator gestures.
//
// pwpolicy.IsTechnicalTag is the sole judge, and three paths consult it: create
// and update via checkCredScope in handleUpsertSiteCredential, delete directly in
// handleDeleteSiteCredential — on scope RE-READ FROM DATABASE, not what the
// request claims. This test covers the judge and message uniqueness; refusal
// applies to all three.
func TestNamespaceRefusedOnInput(t *testing.T) {
	// Both paths must return the SAME refusal. Two wordings of one reasoning
	// eventually diverge, and the operator would read different explanations for
	// the same block depending on which button they clicked.
	var viaScope, scopeErr = checkCredScope(credKindAdmin, "", "pwpolicy-len15")
	if scopeErr == nil {
		t.Fatalf("technical tag accepted, rewritten as %q", viaScope)
	}
	if scopeErr.Error() != errTechnicalScope("pwpolicy-len15").Error() {
		t.Errorf("input and delete refusals diverge:\n%q\n%q",
			scopeErr, errTechnicalScope("pwpolicy-len15"))
	}
	// Delete guard, which asks the judge directly, must recognize the same namespace.
	if !pwpolicy.IsTechnicalTag("pwpolicy-len15") {
		t.Error("namespace judge does not recognize its own tag")
	}

	var refuses = []string{
		"pwpolicy-len15", "pwpolicy-len015", "PWPOLICY-LEN15", "pwpolicy-whatever",
		"pwpolicy-",
	}
	var tag string
	for _, tag = range refuses {
		// Input goes through normalizeTag before the judge: test follows the same
		// path, otherwise it would exercise input that does not exist.
		var _, err = checkCredScope(credKindAdmin, "", normalizeTag(tag))
		if err == nil {
			t.Errorf("technical tag %q accepted on input: operator can fabricate a "+
				"credential provisioning will not find, or delete the one a whole line "+
				"depends on", tag)
			continue
		}
		// Message must say WHY and WHO owns it: unexplained refusal makes them retry
		// identically.
		var msg = err.Error()
		if !strings.Contains(msg, "meshcraft") {
			t.Errorf("refusal for %q does not say who manages these credentials: %q", tag, msg)
		}
		if !strings.Contains(strings.ToLower(msg), "technical") {
			t.Errorf("refusal for %q does not say why: %q", tag, msg)
		}
	}

	// Existing tags untouched: ops tag, group, and site scope remain valid for admin password.
	var allowed = []string{"ops", "scene 8 ports", ""}
	for _, tag = range allowed {
		var got, err = checkCredScope(credKindAdmin, "", tag)
		if err != nil {
			t.Errorf("ops tag %q wrongly rejected: %v", tag, err)
		}
		if got != tag {
			t.Errorf("tag %q rewritten to %q", tag, got)
		}
	}
}

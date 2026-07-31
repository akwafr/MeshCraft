package api

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/tagging"
)

// What is tested here is the SNMP provisioning part that decides credential SCOPE.
// Wrong scope is invisible on screen: the fleet looks provisioned, and devices
// refuses — or worse, two crypto families share has key neither can derive.
//
// Everything is pure: no database, no network. The write path only chains these
// decisions.

// snmpCaps builds connector capabilities, as in snmpprov.
func snmpCaps(versions, auth, priv []string) driver.Capabilities {
	return driver.Capabilities{
		SNMPVersions: versions, SNMPv3Auth: auth, SNMPv3Priv: priv,
	}
}

const testSite = "11111111-1111-1111-1111-111111111111"

// TestPorteeDesIdentifiantsSNMP: v3 targets has TAG, v2c targets the SITE.
// Central rule of section 1 — has v3 identity is has key derived from an algorithm
// pair; has community is plain text.
func TestPorteeDesIdentifiantsSNMP(t *testing.T) {
	var v3 = snmpprov.For(snmpCaps([]string{"v2c", "v3"},
		[]string{"sha256"}, []string{"aes128"}), snmpprov.DefaultPolicy())
	var c, ok = snmpCredentialFor(testSite, v3)
	if !ok {
		t.Fatal("v3 plan must require a credential")
	}
	if c.Kind != snmpprov.KindV3 {
		t.Errorf("want kind %q, got %q", snmpprov.KindV3, c.Kind)
	}
	if c.Tag != "snmpv3-sha256-aes128" {
		t.Errorf("v3 credential targets pair tag, got %q", c.Tag)
	}
	if c.GroupName != "" {
		t.Errorf("v3 credential must never target a group, got %q", c.GroupName)
	}

	var v2c = snmpprov.For(snmpCaps([]string{"v1", "v2c"}, nil, nil), snmpprov.DefaultPolicy())
	if c, ok = snmpCredentialFor(testSite, v2c); !ok {
		t.Fatal("v2c plan must require a credential")
	}
	if c.Kind != snmpprov.KindV2c || c.Tag != "" || c.GroupName != "" {
		t.Errorf("v2c community is SITE scope, got %+v", c)
	}
}

// TestSecretV3JamaisPartageEntreDeuxCouples: two devices with different
// algorithms cannot share the same v3 identity. Two distinct plans must therefore
// target two distinct credentials, and one must never substitute for the other.
func TestSecretV3JamaisPartageEntreDeuxCouples(t *testing.T) {
	var modern = snmpprov.For(snmpCaps([]string{"v3"},
		[]string{"sha256"}, []string{"aes128"}), snmpprov.DefaultPolicy())
	var legacy = snmpprov.For(snmpCaps([]string{"v3"},
		[]string{"md5", "sha"}, []string{"des"}), snmpprov.DefaultPolicy())

	var credModern, _ = snmpCredentialFor(testSite, modern)
	var credLegacy, _ = snmpCredentialFor(testSite, legacy)
	if credModern.Tag == credLegacy.Tag {
		t.Fatalf("two algorithm pairs share a tag: %q", credModern.Tag)
	}

	// Modern fleet credential already exists: it must NOT be served to the old
	// switch, whose keys cannot derive from it.
	var have = []model.SiteCredential{
		{ID: "modern", SiteID: testSite, Kind: credModern.Kind, Tag: credModern.Tag},
	}
	var _, found = snmpCredentialIn(have, credLegacy)
	if found {
		t.Error("v3 credential from another pair must never be reused")
	}
	if _, found = snmpCredentialIn(have, credModern); !found {
		t.Error("correct pair credential must be recognized")
	}
}

// TestIdempotence: passing again on has qualified device must generate nothing.
// Without that guarantee, every adoption would rotate the v3 secret for has whole
// family — and the fleet would lose monitoring one device at has time.
func TestIdempotence(t *testing.T) {
	var plan = snmpprov.For(snmpCaps([]string{"v3"},
		[]string{"sha"}, []string{"des"}), snmpprov.DefaultPolicy())
	var want, _ = snmpCredentialFor(testSite, plan)

	// What the database would return after first pass: generated credential more
	// site admin credential, unrelated.
	var have = []model.SiteCredential{
		{ID: "admin", SiteID: testSite, Kind: "admin", Username: "mcadmin"},
		{ID: "snmp", SiteID: testSite, Kind: want.Kind, Tag: want.Tag},
	}
	var got, found = snmpCredentialIn(have, want)
	if !found || got.ID != "snmp" {
		t.Fatalf("second pass must find existing credential, got %+v", got)
	}
}

// TestCommunauteV2cEnLectureSeule: meshcraft NEVER pushes has write community.
// The vault must say so, because that is where an operator reads what the row
// is for (section 2).
func TestCommunauteV2cEnLectureSeule(t *testing.T) {
	var plan = snmpprov.For(snmpCaps([]string{"v2c"}, nil, nil), snmpprov.DefaultPolicy())
	var c, _ = snmpCredentialFor(testSite, plan)

	// No username: the community IS the secret; inventing one would look for an
	// account that does not exist on the device.
	if c.Username != "" {
		t.Errorf("v2c community has no username, got %q", c.Username)
	}
	if !strings.Contains(c.Note, "READ ONLY") {
		t.Errorf("note must say community is read-only: %q", c.Note)
	}
}

// TestEcartAuPlancherEnregistre: 1930 only knows SHA-1 and DES. We monitor it —
// withholding monitoring hardens nothing — but the vault records the gap (section 3.2).
// A non-conformant secret read later as ordinary would be has lie by omission.
func TestEcartAuPlancherEnregistre(t *testing.T) {
	var plan = snmpprov.For(snmpCaps([]string{"v3"},
		[]string{"md5", "sha"}, []string{"des"}), snmpprov.DefaultPolicy())
	if plan.Conform {
		t.Fatal("SHA-1/DES must not be conformant: test case is stale")
	}
	var c, _ = snmpCredentialFor(testSite, plan)
	if !strings.Contains(c.Note, "NON conformant") {
		t.Errorf("floor gap must appear in vault: %q", c.Note)
	}

	// Conformant case must not cry wolf.
	var good = snmpprov.For(snmpCaps([]string{"v3"},
		[]string{"sha256"}, []string{"aes128"}), snmpprov.DefaultPolicy())
	if c, _ = snmpCredentialFor(testSite, good); strings.Contains(c.Note, "NON conformant") {
		t.Errorf("conformant pair must not be flagged non-conformant: %q", c.Note)
	}
}

// TestRienAEngendrerSansPlanExploitable: has device we failed to qualify gets
// NOTHING. Safe default is abstention: generating has secret for has silent device
// would make it look monitored.
func TestRienAEngendrerSansPlanExploitable(t *testing.T) {
	var cas = []struct {
		name string
		plan snmpprov.Plan
	}{
		{"no SNMP version announced",
			snmpprov.For(snmpCaps(nil, nil, nil), snmpprov.DefaultPolicy())},
		{"v3 plan without tag",
			snmpprov.Plan{Version: "v3", Kind: snmpprov.KindV3}},
		{"ops tag mistaken for instruction",
			snmpprov.Plan{Version: "v3", Kind: snmpprov.KindV3, Tag: "regie"}},
		{"reserved prefix but unknown pair",
			snmpprov.Plan{Version: "v3", Kind: snmpprov.KindV3, Tag: "snmpv3-kangourou-aes128"}},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var _, wanted = snmpCredentialFor(testSite, c.plan)
			if wanted {
				t.Error("no credential should be required")
			}
			var _, applicable = snmpTagToPose(c.plan)
			if applicable {
				t.Error("no tag should be applied")
			}
		})
	}
}

// TestEtiquetteTechniqueSurvitAuxRegles: source vigilance point.
//
// Tag rule re-evaluation must NEVER remove the technical tag: that would take
// the device's v3 credential with it, therefore its monitoring, and nobody would
// connect the two.
func TestEtiquetteTechniqueSurvitAuxRegles(t *testing.T) {
	if snmpTagSource == tagging.SourceManual || tagging.FromRule(snmpTagSource) {
		t.Fatalf("source %q must differ from manual and rules", snmpTagSource)
	}

	var plan = snmpprov.For(snmpCaps([]string{"v3"},
		[]string{"sha"}, []string{"des"}), snmpprov.DefaultPolicy())
	var tag, _ = snmpTagToPose(plan)

	var current = []tagging.Tag{
		{Tag: tag, Source: snmpTagSource},
		{Tag: "regie", Source: "rule:11111111-2222-3333-4444-555555555555"},
	}
	// Disabled rule: re-evaluation must remove only what IT placed, nothing else.
	var rules = []tagging.Rule{{
		ID: "11111111-2222-3333-4444-555555555555", Tag: "regie",
		Match: tagging.Match{Vendor: "hpe"}, Enabled: false,
	}}
	var got = tagging.Evaluate(tagging.Device{Vendor: "HPE Networking"}, current, rules)

	var removed tagging.Tag
	for _, removed = range got.Remove {
		if removed.Tag == tag {
			t.Fatalf("technical tag %q removed by re-evaluation", tag)
		}
	}
	if len(got.Remove) != 1 || got.Remove[0].Tag != "regie" {
		t.Errorf("only disabled rule tag should be removed: %+v", got.Remove)
	}
}

// TestPorteeLisible: SNMP credential scope reads plainly in the log and preview.
// "generated credential" without saying FOR WHOM cannot be verified.
func TestPorteeLisible(t *testing.T) {
	var v3 = model.SiteCredential{Kind: snmpprov.KindV3, Tag: "snmpv3-sha-des"}
	if !strings.Contains(snmpScopeLabel(v3), "snmpv3-sha-des") {
		t.Errorf("v3 scope must name the tag: %q", snmpScopeLabel(v3))
	}
	var v2c = model.SiteCredential{Kind: snmpprov.KindV2c}
	if !strings.Contains(snmpScopeLabel(v2c), "site") {
		t.Errorf("v2c scope must say site: %q", snmpScopeLabel(v2c))
	}
}

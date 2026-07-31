package api

import (
	"context"
	"net/http"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/pwgen"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/snmpv3"
	"meshcraft/internal/tagging"
)

// Provisioning of a device's SNMP IDENTITY — steps 2 and 3 of section 5 of
// docs/03-Security/SNMP identity.
//
// `snmpprov` DECIDES ("what would it take to supervise this device?");
// this file PREPARES THE GROUND from that decision: the technical tag, then the
// credential of the right kind. Nothing else.
//
// Three limits, which are the whole subject:
//
//   - NOTHING IS PUSHED TO THE DEVICE here. Generating a secret prepares the write
//     (section 4), it does not replace it: until the connector has written, the device
//     does not know that secret. Confusing the two would show "supervised" on a silent
//     fleet, which is the worst failure — the one you do not look for;
//   - the v2c community is READ-only, and meshcraft never pushes any other: a write
//     community on a fleet is a root password in cleartext on the network (section 2);
//   - a v3 secret is only shared among devices with the SAME algorithm pair. v3 keys
//     derive from it: two devices with different algorithms cannot carry the same
//     identity, and that is the whole reason for the `snmpv3-<auth>-<priv>` tag (section 1).

// snmpTagSource marks a TECHNICAL tag, set by provisioning.
//
// Neither `manual` nor `rule:<uuid>`, and that is deliberate. A technical tag is not an
// operator decision — marking it manual would lie about its origin and expose it to manual
// editing — and it must never be removed by rule re-evaluation: `tagging.Evaluate` only
// removes `rule:` sources. A separate source is therefore what guarantees a rule replay
// does not unplug fleet supervision in passing.
const snmpTagSource = "snmp"

// snmpV3Username: the SNMPv3 account meshcraft pushes on the fleet.
//
// Distinct from the administration account on purpose. Supervision queries continuously
// from a collector and only needs read access: if this identity leaks, it must not hand
// over device administration with it.
const snmpV3Username = "mcmonitor"

// v2c has NO user: the community IS the secret. The field stays empty, as on input
// (cf. handleUpsertSiteCredential) — inventing an account name would make the device look
// for a user that does not exist.

// snmpIdentityState is what SNMP provisioning observes on a device — and, when it
// generates, what it just changed there.
type snmpIdentityState struct {
	Plan   snmpprov.Plan `json:"plan"`
	Driver string        `json:"driver,omitempty"`
	// TagPresent: the technical tag is already set.
	TagPresent bool `json:"tag_present"`
	// CredentialID: credential of the right kind AND scope, if it already exists.
	CredentialID string `json:"credential_id,omitempty"`
	// Missing: what generation would create, in plain text. Empty = nothing to do.
	// This makes idempotence VERIFIABLE before any write, as section 5 requires.
	Missing []string `json:"missing,omitempty"`
	// Created: what generation did. Always empty on read-only.
	Created []string `json:"created,omitempty"`
}

// snmpTagToPose returns the tag a plan allows pushing.
//
// The guard is not decorative: provisioning must NEVER push anything other than a
// technical tag. The reserved prefix is what distinguishes an encryption directive from
// an operator tag set by hand (section 3.3) — re-reading the tag before writing is the only
// way to keep the name -> pair -> name round trip closed.
func snmpTagToPose(p snmpprov.Plan) (string, bool) {
	if p.Tag == "" {
		return "", false
	}
	var _, wellFormed = snmpv3.ParseTag(p.Tag)
	if !wellFormed {
		return "", false
	}
	return p.Tag, true
}

// snmpCredentialFor describes the credential a plan requires: its KIND and SCOPE. Pure, and
// that is intentional — this is the decision that stores a secret on the right side, and
// it must be testable without a database.
//
//   - v3 -> TAG scope. Never site: keys derive from the algorithm pair, and a site v3
//     secret is precisely what section 1 forbids;
//   - v2c -> SITE scope. A community is text, nothing prevents sharing it, and a per-tag
//     secret would only add filing here.
//
// These are exactly the scopes the vault enforces on INPUT (checkCredScope,
// sitecreds_handlers.go). They are CONSTRUCTED here, not verified: a credential generated
// outside its scope would be stored where provisioning never looks, therefore invisible
// and useless.
func snmpCredentialFor(siteID string, p snmpprov.Plan) (model.SiteCredential, bool) {
	switch p.Kind {
	case snmpprov.KindV3:
		var tag, ok = snmpTagToPose(p)
		if !ok {
			// Without an usable tag, the v3 credential would have no tenable scope. Generating
			// NONE beats generating one that two cryptographic families would share without
			// being able to use it.
			return model.SiteCredential{}, false
		}
		return model.SiteCredential{
			SiteID: siteID, Tag: tag, Kind: snmpprov.KindV3,
			Username: snmpV3Username,
			Note: "SNMPv3 identity generated by meshcraft for \"" + tag + "\" (" +
				p.Auth + "/" + snmpPrivLabel(p.Priv) + ")" + snmpConformNote(p) +
				" Shared only by devices with this algorithm pair. " +
				"TWO distinct passphrases: one authenticates, one encrypts.",
		}, true
	case snmpprov.KindV2c:
		return model.SiteCredential{
			SiteID: siteID, Kind: snmpprov.KindV2c, Username: "",
			Note: "SNMPv2c community generated by meshcraft — READ ONLY. " +
				"meshcraft never pushes a write community: configuration goes through the connector.",
		}, true
	}
	return model.SiteCredential{}, false
}

// snmpV3Secrets generates the PAIR of passphrases for a v3 identity.
//
// Two INDEPENDENT draws, and the inequality check is not decorative: it is the property
// all v3 encryption rests on. RFC 3414 derives authentication and encryption keys by the
// same algorithm from the passphrase and engineID — same passphrase, same input, therefore
// an encryption key that is only the prefix of the authentication key. Who gets one gets
// the other.
//
// Fail rather than return the same twice: an identity that looks encrypted but is not is
// worse than one we failed to generate, because nobody will verify it.
func snmpV3Secrets() (string, string, error) {
	var attempt int
	for attempt = 0; attempt < 8; attempt++ {
		var auth, err = pwgen.New(pwgen.DefaultLength)
		if err != nil {
			return "", "", err
		}
		var priv string
		if priv, err = pwgen.New(pwgen.DefaultLength); err != nil {
			return "", "", err
		}
		if auth != priv {
			return auth, priv, nil
		}
	}
	return "", "", errSamePassphrase
}

// errSamePassphrase should never happen — 16 characters from 62 is ~95 bits — but a broken
// generator that always returned the same value would surface here as frank refusal rather
// than a silently unencrypted fleet.
const errSamePassphrase = apiError("could not generate two distinct SNMPv3 passphrases")

// snmpPrivLabel: a pair without encryption is STATED, not guessed from an empty field —
// otherwise you think you encrypted when you only authenticated.
func snmpPrivLabel(priv string) string {
	if priv == "" {
		return "no encryption"
	}
	return priv
}

// snmpConformNote records deviation from the policy floor IN the vault.
//
// Section 3.2 is explicit: deviation is recorded, not hidden. Deploying SHA-1/DES is a
// defensible choice; reading it later as an ordinary secret without knowing it is below
// policy would be lying by omission.
func snmpConformNote(p snmpprov.Plan) string {
	if p.Conform {
		return "."
	}
	return " — pair BELOW policy floor: supervised, NON conformant."
}

// snmpScopeLabel names an SNMP credential's scope in plain text. This distinguishes "a
// secret for this algorithm pair" from "a secret for the whole site": the distinction that
// decides what is shared with what.
func snmpScopeLabel(c model.SiteCredential) string {
	if c.Tag != "" {
		return "for tag \"" + c.Tag + "\""
	}
	return "site-scoped"
}

// snmpCredentialIn searches a site's credentials for one with EXACTLY the wanted scope.
//
// Equality is strict — same kind, same tag, same group. Accepting a "close" credential would
// share a v3 secret across two algorithm pairs: devices would reject it, and the fleet
// would look supervised when it is not.
func snmpCredentialIn(have []model.SiteCredential,
	want model.SiteCredential) (model.SiteCredential, bool) {
	var c model.SiteCredential
	for _, c = range have {
		if c.Kind == want.Kind && c.Tag == want.Tag && c.GroupName == want.GroupName {
			return c, true
		}
	}
	return model.SiteCredential{}, false
}

// snmpPlanFor computes a device's plan and records what already exists. PUSHES NOTHING and
// writes nothing: the function shared by read-only and generation, so both see exactly the
// same thing.
func (s *Server) snmpPlanFor(ctx context.Context, d model.Device) (snmpIdentityState, error) {
	var out snmpIdentityState
	var _, driverName = s.deviceIdentity(ctx, d)
	out.Driver = driverName

	var drv, known = driver.Get(driverName)
	if !known {
		// Unknown capabilities = assumed absent. Assuming "it surely does v2c" would push a
		// community on a device we know nothing about, and mark it supervised when nothing
		// would collect it.
		out.Plan.Reason = "No connector recognizes this device: meshcraft " +
			"does not know what it accepts for SNMP, and therefore does not invent an identity."
		return out, nil
	}
	out.Plan = snmpprov.For(drv.Capabilities(d.Model), snmpprov.DefaultPolicy())
	if !out.Plan.Supervised() {
		return out, nil
	}

	// Tag first: it carries the v3 credential, therefore decides which secret the device
	// will inherit.
	var tag, hasTag = snmpTagToPose(out.Plan)
	if hasTag {
		var tags, err = s.Store.DeviceTags(ctx, d.ID)
		if err != nil {
			return out, err
		}
		var t tagging.Tag
		for _, t = range tags {
			if t.Tag == tag {
				out.TagPresent = true
				break
			}
		}
		if !out.TagPresent {
			out.Missing = append(out.Missing, "technical tag \""+tag+"\"")
		}
	}

	var want, wanted = snmpCredentialFor(d.SiteID, out.Plan)
	if !wanted {
		return out, nil
	}

	// The question here is "does a credential OF THIS SCOPE exist on the site?", not "which
	// one applies to this device?". We therefore list the site.
	//
	// Going through CredentialsForDevice would be a trap: that resolution only sees a tag
	// credential if the device ALREADY carries the tag. On first pass on a second device of
	// the same family, it would find nothing — and generation would overwrite, via upsert,
	// the v3 secret already installed on the first. A shared secret regenerated at each
	// adoption is a fleet lost one device at a time.
	var creds, err = s.Store.ListSiteCredentials(ctx, d.SiteID)
	if err != nil {
		return out, err
	}
	var existing, found = snmpCredentialIn(creds, want)
	if found {
		out.CredentialID = existing.ID
		// A v3 identity missing its second passphrase pushes fine and encrypts nothing: both
		// derivations would get the same input. The case can no longer arise (the vault
		// requires the pair, cf. checkCredSecrets), but naming it in the plan keeps it from
		// becoming invisible if it did — push would abstain (snmpIdentityToPose).
		if want.Kind == snmpprov.KindV3 && !existing.HasSecret2 {
			out.Missing = append(out.Missing,
				"encryption passphrase for v3 identity \""+existing.Tag+
					"\": without it, the identity authenticates but encrypts nothing")
		}
		return out, nil
	}
	out.Missing = append(out.Missing, "credential "+want.Kind+" "+snmpScopeLabel(want))
	return out, nil
}

// ensureSNMPIdentity generates what is missing from a device's SNMP identity: the technical
// tag, then the credential of the right scope.
//
// IDEMPOTENT — passing over an already qualified fleet creates neither tag nor secret
// (section 3.4). That property allows calling it at every adoption and every identity
// change without thinking.
//
// SILENT on failure, like autoTagDevice: failing to generate a supervision identity is an
// annoyance fixed on the next pass; failing adoption for that would be wrong. What is done,
// however, is logged — a secret that appears without a trace is exactly what you regret not
// finding afterward.
func (s *Server) ensureSNMPIdentity(ctx context.Context, d model.Device) {
	// Without a vault, there is nowhere to store a secret. Better generate nothing than
	// generate what we cannot read back.
	if s.Secrets == nil {
		return
	}
	var st, err = s.snmpPlanFor(ctx, d)
	if err != nil || !st.Plan.Supervised() || len(st.Missing) == 0 {
		return
	}

	var tag, hasTag = snmpTagToPose(st.Plan)
	if hasTag && !st.TagPresent {
		if err = s.Store.SetDeviceTag(ctx, d.ID, tag, snmpTagSource); err != nil {
			// The v3 credential targets this tag: without it, nothing would be pushed.
			// We stop, and the next pass will resume in order.
			return
		}
		s.emitDeviceBG(d, "tag.assigned", model.SevInfo,
			"Technical tag \""+tag+"\" set on \""+d.Name+
				"\" by SNMP provisioning: it decides the v3 secret this device will share.")
	}

	var want, wanted = snmpCredentialFor(d.SiteID, st.Plan)
	if !wanted || st.CredentialID != "" {
		return
	}
	// Secrets are generated HERE and never displayed: they are revealed from Configuration ->
	// Credentials, a distinct traced gesture. Alphanumeric, like all secrets meshcraft
	// pushes — a password rejected by half the fleet protects nothing (cf. internal/pwgen).
	//
	// TWO passphrases for v3 identity, one for v2c community. The community IS the secret;
	// v3 identity derives two keys, and feeding both from the same passphrase would mean not
	// encrypting at all (cf. snmpV3Secrets and migration 0017).
	var secret, secret2 string
	if want.Kind == snmpprov.KindV3 {
		if secret, secret2, err = snmpV3Secrets(); err != nil {
			return
		}
	} else if secret, err = pwgen.New(pwgen.DefaultLength); err != nil {
		return
	}
	var enc, enc2 []byte
	if enc, err = s.Secrets.Seal(secret); err != nil {
		return
	}
	if secret2 != "" {
		if enc2, err = s.Secrets.Seal(secret2); err != nil {
			return
		}
	}
	// Empty `updatedBy`: meshcraft acts, not a user.
	if _, err = s.Store.UpsertSiteCredential(ctx, want, enc, enc2, ""); err != nil {
		return
	}
	s.emitDeviceBG(d, "credential.generated", model.SevNotice,
		"Credential "+want.Kind+" "+snmpScopeLabel(want)+" generated for \""+
			d.Name+"\" — nothing has been pushed to the device yet, "+
			"SNMP configuration still needs to be written.")
}

// handleDeviceSNMPPlan returns what provisioning WOULD do, without generating anything.
//
// Explicitly what section 5 requires: the decision must be verifiable on the real fleet
// BEFORE the first write. An automation you only control afterward is controlled on an
// already modified fleet.
//
// `operator` level, like state re-read: observing is not modifying. No secret is returned —
// only algorithms, a tag, and the list of what is missing.
func (s *Server) handleDeviceSNMPPlan(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "operator")
	if !ok {
		return
	}
	var st, err = s.snmpPlanFor(r.Context(), d)
	if err != nil {
		fail(w, err)
		return
	}
	respond(w, http.StatusOK, st)
}

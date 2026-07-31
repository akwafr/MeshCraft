package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/pwpolicy"
	"meshcraft/internal/snmpprov"
	"meshcraft/internal/snmpv3"
)

// Site credentials: what meshcraft installs on devices, and what an administrator uses to
// connect to them directly.
//
// Three rules govern this screen:
//
//  1. LIST never returns a secret, only its presence and rotation date. You do not read a
//     password by accident when opening a page;
//  2. REVEAL is a distinct gesture, reserved to the site's `site_admin` (and `global-admin`),
//     and TRACED in the log — otherwise traceability is worthless;
//  3. a site may have several credentials, one per device GROUP: password policies differ by
//     model, and imposing one secret would make some devices unconfigurable.

func (s *Server) handleListSiteCredentials(w http.ResponseWriter, r *http.Request) {
	// Seeing that a credential exists is not reading it: `operator` suffices, they need it
	// to understand what will be pushed.
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var creds, err = s.Store.ListSiteCredentials(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	// History accompanies the credential: knowing HOW MANY devices still carry which
	// generation is what makes rotation manageable.
	var i int
	for i = range creds {
		var gens, genErr = s.Store.ListGenerations(r.Context(), creds[i].ID)
		if genErr == nil {
			creds[i].Generations = gens
		}
	}
	respond(w, http.StatusOK, map[string]any{
		"credentials": creds,
		// The UI must say where the key lives: a secret "encrypted with a key stored next
		// door" must not be presented as protected.
		"key_external": s.Secrets != nil && s.Secrets.FromEnv,
	})
}

type siteCredInput struct {
	GroupName string `json:"group_name"`
	// Tag: the credential only applies to devices carrying this tag
	// (cf. docs/01-Architecture/Tags and credential scope).
	Tag string `json:"tag"`
	// Priority breaks ties between two tag credentials targeting the same device.
	Priority int    `json:"priority"`
	Kind     string `json:"kind"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
	// Secret2: second secret phrase, specific to `snmp_v3`. See checkCredSecrets — explained
	// there why it is not convenience but the condition for v3 encryption to matter.
	Secret2 string `json:"secret2"`
	Note    string `json:"note"`
}

// credKindAdmin: administration password, the only vault kind until now.
const credKindAdmin = "admin"

// checkCredSecrets matches a credential's KIND to the NUMBER of secret phrases submitted
// (cf. migration 0017).
//
// Pure, like checkCredScope, for the same reason: what it forbids never appears on screen. A
// v3 configured with the same phrase on both sides looks exactly like correct v3 — it
// authenticates, encrypts, polls arrive. What is missing is invisible: RFC 3414 derives both
// keys by the same algorithm from the phrase and engineID, so two identical inputs give an
// encryption key that is the prefix of the authentication key. Encryption then adds nothing.
//
// The two-phrases "both or neither" rule is not fussiness: changing authentication alone would
// leave the encryption key derived from the old phrase, and the screen would show no anomaly.
func checkCredSecrets(kind, secret, secret2 string) error {
	if kind != snmpprov.KindV3 {
		if secret2 != "" {
			return errors.New("only an SNMPv3 identity carries two secret phrases: \"" +
				kind + "\" has only one. SNMPv3 derives an authentication key AND an " +
				"encryption key; an administration password or community does not")
		}
		return nil
	}
	// Both or neither. "Neither" remains valid: label or priority change that does not touch
	// the vault.
	if (secret == "") != (secret2 == "") {
		return errors.New("an SNMPv3 identity is changed as a PAIR: enter the authentication " +
			"phrase AND the encryption phrase, or leave both empty to change neither. Changing " +
			"only one would leave the other key derived from the old phrase, with nothing to " +
			"flag it")
	}
	if secret != "" && secret == secret2 {
		return errors.New("the two SNMPv3 phrases must be DIFFERENT: both keys derive the same " +
			"way, so the same phrase gives an encryption key that is only the start of the " +
			"authentication key. Who gets one gets the other, and encryption protects nothing")
	}
	return nil
}

// errTechnicalScope is refusal for the `pwpolicy-` namespace, written ONCE for all three
// gestures.
//
// One text because one reasoning: the gesture differs (create, update, delete), the reason
// is the same, and two wordings of the same refusal eventually stop saying the same thing.
// The message must answer the two questions an operator asks facing a refusal — WHY, and
// WHO handles it — otherwise they repeat the same attempt looking for a missing permission.
func errTechnicalScope(tag string) error {
	return errors.New("\"" + tag + "\" is a TECHNICAL password-policy tag: these credentials " +
		"are generated and maintained by meshcraft itself, from what each platform's connector " +
		"declares it accepts (max length, min length, punctuation). Neither their name nor scope " +
		"can be entered by hand, and deleting them would leave affected devices without a " +
		"pushable password — they would fall back to the site secret, which their platform " +
		"refuses before send. Their secret, however, can be revealed and rotated normally. To " +
		"file a credential manually, choose an operations tag — \"regie\", \"stage\" — or site scope")
}

// checkCredScope matches a credential's KIND to its SCOPE, and returns the tag in canonical
// form (cf. docs/03-Security/SNMP identity section 2).
//
// The `kind` column already existed as free text: what was missing are these rules. Without
// them, a secret stored in a scope provisioning never consults would exist without ever
// serving — and the screen would show it, which is worse than no secret: you would think
// supervision was configured.
func checkCredScope(kind, groupName, tag string) (string, error) {
	switch kind {
	case credKindAdmin:
		// ONE EXCEPTION ONLY, and it is a RESERVED namespace: tags `pwpolicy-...` designate a
		// platform password policy, and credentials targeting them are generated and maintained
		// by meshcraft (cf. internal/api/pwpolicy_creds.go). Three reasons to refuse manual
		// entry, and the third costs the most:
		//
		//   - the NAME is function output, never typed text. A spelling variant
		//     (`pwpolicy-len015`) would fabricate a second credential for the same constraint,
		//     splitting the fleet with no screen saying so;
		//   - the SECRET must fit bounds the operator does not see here: a 20-character password
		//     filed under `pwpolicy-len15` would be refused by the connector before send — a
		//     inert credential that looks like it works;
		//   - DELETING this credential would leave the product line without any pushable
		//     password — they would fall back to the site secret, which their platform refuses.
		//     That is the "irrevocable" in the design: meshcraft created it because it had no
		//     choice, it cannot allow removal.
		if pwpolicy.IsTechnicalTag(tag) {
			return "", errTechnicalScope(tag)
		}
		// For everything else, an administration password is text: site, group, or tag all make
		// sense, and operations chooses. Nothing to constrain, and especially nothing to change
		// for existing data.
		return tag, nil

	case snmpprov.KindV2c:
		// A community is also text, therefore shareable; but the v2c provisioning plan carries
		// NO tag (snmpprov.For), and resolution is therefore at site level. A community filed
		// under a tag or group would never be found.
		if tag != "" {
			return "", errors.New("an SNMPv2c community is site-scoped: remove tag \"" +
				tag + "\". Only a v3 identity is split by tag, because its key derives from an " +
				"algorithm pair; a community does not")
		}
		if groupName != "" {
			return "", errors.New("an SNMPv2c community applies to the whole site: provisioning " +
				"does not look up community by group, group \"" + groupName +
				"\" would never be used")
		}
		return "", nil

	case snmpprov.KindV3:
		// A v3 secret's perimeter is the algorithm PAIR: keys derive from it, and two devices
		// that do not accept the same pair cannot carry the same identity. A "site" v3 would
		// be a secret half the fleet would silently refuse.
		if tag == "" {
			return "", errors.New("an SNMPv3 credential ALWAYS targets a tag \"" +
				snmpv3.TagPrefix + "<auth>-<priv>\": its key derives from the algorithm pair, " +
				"two devices that do not accept the same pair cannot share it")
		}
		// ParseTag is the sole judge: the v3 tag is TECHNICAL, set by provisioning. Accepting
		// an operations tag here would read "regie" as an encryption directive.
		var c, ok = snmpv3.ParseTag(tag)
		if !ok {
			return "", errors.New("\"" + tag + "\" is not an SNMPv3 tag: expected \"" +
				snmpv3.TagPrefix + "<auth>-<priv>\", for example \"snmpv3-sha256-aes128\". " +
				"An operations tag does not describe algorithms")
		}
		// Rewrite in canonical form: provisioning will generate exactly `Choice.Tag()`, and a
		// spelling variant would create a second secret where the name -> pair -> name round
		// trip must stay stable (section 3.4: nothing is generated twice).
		return c.Tag(), nil
	}
	// Closed list: an invented kind would produce a credential nothing consumes, silently inert.
	return "", errors.New("unknown credential kind \"" + kind + "\": expected \"admin\", \"" +
		snmpprov.KindV2c + "\" (READ community, site scope) or \"" +
		snmpprov.KindV3 + "\" (tag scope " + snmpv3.TagPrefix + "...)")
}

func (s *Server) handleUpsertSiteCredential(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in siteCredInput
	if !decode(w, r, &in) {
		return
	}
	in.GroupName = strings.TrimSpace(in.GroupName)
	in.Tag = normalizeTag(in.Tag)
	// A credential cannot target BOTH a group and a tag: scope would be ambiguous, and the
	// section 3 hierarchy would not know how to classify it.
	if in.GroupName != "" && in.Tag != "" {
		respondErr(w, http.StatusBadRequest,
			"a credential targets a group OR a tag, not both")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	in.Kind = strings.TrimSpace(in.Kind)
	if in.Kind == "" {
		in.Kind = credKindAdmin
	}
	var scopeErr error
	in.Tag, scopeErr = checkCredScope(in.Kind, in.GroupName, in.Tag)
	if scopeErr != nil {
		respondErr(w, http.StatusBadRequest, scopeErr.Error())
		return
	}
	// A v2c community has no user: the secret IS the community. Requiring one would suggest
	// an account that does not exist on the device, and two rows differing only in that dead
	// field would clutter the list.
	if in.Kind == snmpprov.KindV2c {
		in.Username = ""
	} else if in.Username == "" {
		respondErr(w, http.StatusBadRequest, "username required")
		return
	}

	var secretsErr = checkCredSecrets(in.Kind, in.Secret, in.Secret2)
	if secretsErr != nil {
		respondErr(w, http.StatusBadRequest, secretsErr.Error())
		return
	}

	// `enc2` stays nil when nothing is provided, and the store reads that as "unchanged": a
	// label change on a v3 identity must not erase its encryption phrase.
	var enc, enc2 []byte
	if in.Secret != "" {
		var err error
		enc, err = s.Secrets.Seal(in.Secret)
		if err != nil {
			fail(w, err)
			return
		}
	}
	if in.Secret2 != "" {
		var err error
		enc2, err = s.Secrets.Seal(in.Secret2)
		if err != nil {
			fail(w, err)
			return
		}
	}
	var me = auth.UserFrom(r.Context())
	var c, err = s.Store.UpsertSiteCredential(r.Context(), model.SiteCredential{
		SiteID: siteID, GroupName: in.GroupName, Tag: in.Tag, Priority: in.Priority,
		Kind: in.Kind, Username: in.Username, Note: in.Note,
	}, enc, enc2, me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	var what = "credential \"" + credLabel(c) + "\" saved"
	if enc != nil {
		what = "secret for credential \"" + credLabel(c) + "\" changed"
	}
	s.emitSite(r, siteID, "credential.updated", model.SevNotice, what, nil)
	respond(w, http.StatusOK, c)
}

// credLabel names a credential by its SCOPE: what distinguishes two rows with the same
// username and different passwords.
func credLabel(c model.SiteCredential) string {
	// A v2c community has no user: without fallback, the log would speak of credential
	// "@site" without saying what it is.
	var who = c.Username
	if who == "" {
		who = c.Kind
	}
	switch {
	case c.Tag != "":
		return who + "@#" + c.Tag
	case c.GroupName != "":
		return who + "@" + c.GroupName
	}
	return who + "@site"
}

// handleRevealSiteCredential returns the cleartext password. Reserved to `site_admin`, and
// TRACED: untraced cleartext access is worth less than no vault at all (cf.
// docs/03-Security/Service identity and rotation section 7).
func (s *Server) handleRevealSiteCredential(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var c, enc, enc2, err = s.Store.RevealSiteCredential(r.Context(), chi.URLParam(r, "credID"))
	if err != nil {
		fail(w, err)
		return
	}
	// The credential must belong to the URL site: without this check, a site-admin would read
	// another site's secrets by guessing a UUID.
	if c.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var secret string
	secret, err = s.Secrets.Open(enc)
	if err != nil {
		fail(w, err)
		return
	}
	var out = map[string]any{"id": c.ID, "username": c.Username, "secret": secret}
	// Second phrase is added ONLY if it exists: an always-present "secret2" key, empty for an
	// admin password, would suggest an empty phrase rather than absence — and the screen would
	// show it.
	if len(enc2) > 0 {
		var secret2 string
		secret2, err = s.Secrets.Open(enc2)
		if err != nil {
			fail(w, err)
			return
		}
		out["secret2"] = secret2
	}
	var me = auth.UserFrom(r.Context())
	// One log line for the pair: both halves of one identity, revealed in one gesture. Two
	// lines would suggest two distinct accesses and confuse review.
	s.emitSite(r, siteID, "credential.revealed", model.SevWarning,
		"Password \""+credLabel(c)+"\" revealed by "+me.Email, nil)
	respond(w, http.StatusOK, out)
}

func (s *Server) handleDeleteSiteCredential(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var c, _, _, err = s.Store.RevealSiteCredential(r.Context(), chi.URLParam(r, "credID"))
	if err != nil {
		fail(w, err)
		return
	}
	if c.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	// THE THIRD GESTURE. Create and update go through `checkCredScope`; delete had no scope
	// check, yet it costs the most on a technical credential: deleting a password-policy one
	// leaves an entire product line without a pushable secret, with nothing saying so.
	//
	// THE JUDGE IS THE SAME (`pwpolicy.IsTechnicalTag`), scope is what is READ BACK FROM THE
	// DATABASE not what the request claims — but `checkCredScope` is deliberately NOT called
	// as-is here: its kind list is closed, and a LEGACY kind credential (the UI still shows
	// them) would become undeletable forever. A guardrail that prevents cleaning an old row is
	// no longer a guardrail.
	//
	// 409 rather than 400: the request is well formed, vault state opposes it.
	if pwpolicy.IsTechnicalTag(c.Tag) {
		respondErr(w, http.StatusConflict, errTechnicalScope(c.Tag).Error())
		return
	}
	if err = s.Store.DeleteSiteCredential(r.Context(), c.ID); err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "credential.deleted", model.SevWarning,
		"Credential \""+credLabel(c)+"\" deleted", nil)
	respond(w, http.StatusNoContent, nil)
}

// handleRotateCredential creates the next generation.
//
// The old one is NOT deleted: it moves to `retiring` and stays usable, because devices still
// carry it. That is what allows crossing a rotation without losing access to a fleet that is
// never in a single state (cf. docs/03-Security/Service identity and rotation section 3).
func (s *Server) handleRotateCredential(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var c, _, _, err = s.Store.RevealSiteCredential(r.Context(), chi.URLParam(r, "credID"))
	if err != nil {
		fail(w, err)
		return
	}
	if c.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		Secret  string `json:"secret"`
		Secret2 string `json:"secret2"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Secret) == "" {
		respondErr(w, http.StatusBadRequest,
			"new password required: a rotation without a secret makes no sense")
		return
	}
	// Same rules as create: kind comes from the vault, not the request, otherwise rotation
	// would bypass validation by declaring another kind.
	var secretsErr = checkCredSecrets(c.Kind, in.Secret, in.Secret2)
	if secretsErr != nil {
		respondErr(w, http.StatusBadRequest, secretsErr.Error())
		return
	}
	// TECHNICAL CREDENTIAL ROTATION REMAINS AVAILABLE, but BOUNDED.
	//
	// Forbidding it would have been simpler and had a serious flaw: an entire product line's
	// secret would never rotate again. What must be refused is only the password that does not
	// fit the constraint the tag names — a 20-character secret under `pwpolicy-len15` would
	// reproduce exactly the defect this mechanism closes, except meshcraft would have filed it
	// in the vault itself.
	//
	// The re-read constraint is the TAG's (exact length, alphanumeric), not a platform's:
	// several lines may share this tag, and the only value we know they all accept is the one
	// used to group them.
	var pol, technical = pwpolicy.PolicyForTag(c.Tag)
	if technical {
		var polErr = pol.Accepts(in.Secret)
		if polErr != nil {
			respondErr(w, http.StatusBadRequest, "technical tag \""+c.Tag+
				"\" requires exactly "+itoa(pol.Max)+" alphanumeric characters: "+
				polErr.Error()+". This is the password policy of platforms sharing this "+
				"credential — an out-of-bounds secret would be refused by their connector before "+
				"send, and the device would keep the old one")
			return
		}
	}
	var enc, enc2 []byte
	enc, err = s.Secrets.Seal(in.Secret)
	if err != nil {
		fail(w, err)
		return
	}
	if in.Secret2 != "" {
		enc2, err = s.Secrets.Seal(in.Secret2)
		if err != nil {
			fail(w, err)
			return
		}
	}
	var me = auth.UserFrom(r.Context())
	var g model.CredentialGeneration
	g, err = s.Store.AddGeneration(r.Context(), c.ID, "", enc, enc2, me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "credential.rotated", model.SevWarning,
		"New generation "+itoa(g.Generation)+" for \""+credLabel(c)+"\". "+
			"The previous one stays valid while devices still carry it.", nil)
	respond(w, http.StatusCreated, g)
}

// handleSetCredentialMode toggles a credential between shared secret and per-device secret.
func (s *Server) handleSetCredentialMode(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var c, _, _, err = s.Store.RevealSiteCredential(r.Context(), chi.URLParam(r, "credID"))
	if err != nil {
		fail(w, err)
		return
	}
	if c.SiteID != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var in struct {
		PerDevice bool `json:"per_device"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err = s.Store.SetCredentialPerDevice(r.Context(), c.ID, in.PerDevice); err != nil {
		fail(w, err)
		return
	}
	var what = "shared secret for the whole scope"
	if in.PerDevice {
		what = "a DISTINCT secret per device — safer, and nothing left to remember: " +
			"direct access now goes through meshcraft"
	}
	s.emitSite(r, siteID, "credential.mode_changed", model.SevNotice,
		"Credential \""+credLabel(c)+"\": "+what, nil)
	respond(w, http.StatusOK, map[string]any{"per_device": in.PerDevice})
}

// handleRevealGeneration reveals a SPECIFIC generation. Useful when a device lagged behind:
// you need its old password, not the new one.
func (s *Server) handleRevealGeneration(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var gen, convErr = strconv.Atoi(chi.URLParam(r, "gen"))
	if convErr != nil {
		respondErr(w, http.StatusBadRequest, "invalid generation")
		return
	}
	var credID = chi.URLParam(r, "credID")
	var enc, enc2, credSite, err = s.Store.RevealGeneration(r.Context(), credID, gen)
	if err != nil {
		fail(w, err)
		return
	}
	if credSite != siteID {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}
	var secret string
	secret, err = s.Secrets.Open(enc)
	if err != nil {
		fail(w, err)
		return
	}
	var out = map[string]any{"generation": gen, "secret": secret}
	// The PAIR for THAT generation, never a mix of two generations: on a lagging v3 device, a
	// mismatched encryption phrase does not error, it goes silent.
	if len(enc2) > 0 {
		var secret2 string
		secret2, err = s.Secrets.Open(enc2)
		if err != nil {
			fail(w, err)
			return
		}
		out["secret2"] = secret2
	}
	var me = auth.UserFrom(r.Context())
	s.emitSite(r, siteID, "credential.revealed", model.SevWarning,
		"Generation "+itoa(gen)+" revealed by "+me.Email, nil)
	respond(w, http.StatusOK, out)
}

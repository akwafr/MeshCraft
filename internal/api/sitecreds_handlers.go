package api

import (
	"context"
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
	"meshcraft/internal/store"
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
	// (cf. docs/en/01-Architecture/Tags and credential scope.md).
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
	// TransportTLS / CAPEM: transport posture for the `gnmi` kind (migration 0039).
	//
	// THESE TWO FIELDS DID NOT EXIST HERE UNTIL THE UI NEEDED THEM, AND THEIR ABSENCE WAS A
	// BUG, not a gap. The column, the model field, the store's coalesce and gnmiCredentialFor
	// all landed together; this struct did not, so every value the API accepted for them was
	// dropped on the floor between decode and UpsertSiteCredential. The observable symptom was
	// the worst possible one: a `gnmi` credential created through the raw API came back from
	// the store reading `transport_tls = "verify"` — the column default — no matter what was
	// posted, so a site that deliberately chose `skip-verify` for its self-signed switches got
	// a subscription that refused every handshake, and a site that pasted a CA got verification
	// against the proxy image's system pool, which holds no switch issuer. Both look like the
	// switch is broken. Neither is.
	//
	// They are NOT `omitempty`-shaped on the way in: an empty TransportTLS means "leave it
	// alone" to the store (`coalesce(nullif($9,''), ...)`), which is what lets a caller that
	// knows nothing about telemetry — an older UI build, a script — edit a note without
	// silently rewriting the transport of a credential it never displayed.
	//
	// CAPEM does NOT get that treatment, and the asymmetry is the store's, not ours: `ca_pem`
	// is coalesced on a value that is never NULL over the wire, so whatever arrives here
	// REPLACES what is stored. A caller editing a `gnmi` credential must therefore send the CA
	// back verbatim. That is affordable precisely because it is not a secret — the list
	// endpoint returns it in full, so the screen has it in hand and can resubmit it. A secret
	// could not work this way, which is exactly why secrets are nil-means-unchanged instead.
	TransportTLS string `json:"transport_tls"`
	CAPEM        string `json:"ca_pem"`
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
		"file a credential manually, choose an operations tag — \"ops\", \"stage\" — or site scope")
}

// errFallbackScope is refusal for the `fallback-admin` namespace, written ONCE for all
// three gestures, on the same reasoning as [errTechnicalScope].
//
// It says what the credential IS, because an operator meeting this refusal is usually
// trying to do the right thing — file the emergency account — and needs to be told the
// row already exists and where to act on it. Deleting is refused for a harder reason
// than the pwpolicy one: this row is the only record of a door that is OPEN ON DEVICES,
// and removing the record does not remove the door.
func errFallbackScope(tag string) error {
	return errors.New("\"" + tag + "\" is the RESERVED scope of the fallback administration " +
		"credential: the account devices were adopted through, which meshcraft now tracks " +
		"with its own secret and its own generations instead of letting it drift as a copy " +
		"of the site password. Its name and scope cannot be entered by hand — a spelling " +
		"variant would create a second door nothing rotates and nothing reports, which is " +
		"the exact state this credential was created to end. Its secret can be revealed and " +
		"rotated normally, and its deployment is a deliberate separate gesture. To file a " +
		"credential manually, choose an operations tag — \"ops\", \"stage\" — or site scope")
}

// checkCredScope matches a credential's KIND to its SCOPE, and returns the tag in canonical
// form (cf. docs/en/03-Security/SNMP identity.md section 2).
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
		// A SECOND RESERVED NAMESPACE, on the same three grounds and one more.
		//
		// `fallback-admin` scopes the FALLBACK administration credential (cf.
		// internal/store/fallbackadmin.go): the account devices were adopted through and
		// that `keep_previous_account` left in place, which now carries its own secret and
		// its own generations instead of being a copy of the site password.
		//
		// The extra ground is the one that decides it. The namespace is refused WHOLE
		// because a spelling variant — `fallback-admin-2`, entered by hand in good faith —
		// would produce a SECOND untracked door, which is precisely the defect the row was
		// created to remove. A credential nobody rotates and nothing reports is what
		// `admin` already was on two lab units, and it stayed that way for a week without
		// a single screen being wrong.
		if store.IsFallbackAdminTag(tag) {
			return "", errFallbackScope(tag)
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
		// an operations tag here would read "ops" as an encryption directive.
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

	case CredKindGNMI:
		// A STREAMING TELEMETRY LOGIN, and its scope is free like an admin password's: site,
		// group or tag all mean something, because gnmiCredentialFor resolves through
		// CredentialsForDevice — the same tag-then-group-then-site walk. That is what lets one
		// site run a single shared telemetry account while a family of Aristas carries its own,
		// and it is the reason this kind is NOT pinned to site scope the way a v2c community is.
		//
		// What is refused is entry into a namespace meshcraft maintains for itself, and for two
		// different reasons that happen to point the same way:
		//
		//   - `pwpolicy-...` names a platform's password constraint, and every credential under
		//     it is regenerated by internal/api/pwpolicy_creds.go. A `gnmi` row filed there would
		//     be a telemetry login sitting in the middle of the mechanism that rewrites admin
		//     passwords — nothing would consume it, and the next provisioning pass would make the
		//     scope look occupied by something it is not;
		//   - `snmpv3-<auth>-<priv>` names a CRYPTO PAIR. Reading it as the scope of a gNMI
		//     account would mean "this telemetry login applies to devices that accept SHA-256 and
		//     AES-128", which describes nothing about gRPC. The tag would resolve — devices do
		//     carry it — so the credential would WORK, on an arbitrary subset of the fleet chosen
		//     by an SNMP property. A wrong answer that functions is worse than a refusal.
		if pwpolicy.IsTechnicalTag(tag) {
			return "", errTechnicalScope(tag)
		}
		// And `fallback-admin` for a reason particular to this kind: that tag is a
		// credential scope that is NEVER set on a device, so a telemetry credential filed
		// under it would resolve for nobody. It would sit on the screen looking configured
		// and collect from nothing — the inert-credential failure this whole function
		// exists to refuse.
		if store.IsFallbackAdminTag(tag) {
			return "", errFallbackScope(tag)
		}
		if strings.HasPrefix(tag, snmpv3.TagPrefix) {
			return "", errors.New("\"" + tag + "\" is an SNMPv3 tag: it names an authentication " +
				"and encryption ALGORITHM PAIR, which says nothing about a gNMI subscription. " +
				"Scope a telemetry credential to the whole site, to a group, or to an operations " +
				"tag — \"arista\", \"core\" — that names the devices you mean")
		}
		return tag, nil
	}
	// Closed list: an invented kind would produce a credential nothing consumes, silently inert.
	return "", errors.New("unknown credential kind \"" + kind + "\": expected \"admin\", \"" +
		snmpprov.KindV2c + "\" (READ community, site scope), \"" +
		snmpprov.KindV3 + "\" (tag scope " + snmpv3.TagPrefix + "...) or \"" +
		CredKindGNMI + "\" (streaming telemetry login, any scope)")
}

// checkCredTransport matches a credential's KIND to the transport posture submitted, and
// returns the canonical pair to store.
//
// Pure, and tested, for the reason every other check in this file is: what it forbids is
// invisible once stored. A `transport_tls` nobody validated does not fail here — it fails in
// PostgreSQL, against migration 0039's CHECK constraint, as a 500 with a constraint name in
// it. The operator reads "internal error" on a form where they picked one of three radio
// buttons, and there is nothing on that screen to tell them which field was refused.
//
// # WHY A TRANSPORT ON THE WRONG KIND IS AN ERROR AND NOT AN IGNORED FIELD
//
// Only `gnmi` carries a device login over a connection whose encryption is optional. An SNMP
// community has no TLS to configure and an `admin` password rides SSH, which is never in the
// clear. So a `ca_pem` posted alongside an admin password protects exactly nothing — and
// dropping it silently would leave somebody who pasted a CA into the wrong row believing they
// had configured verification. They would have configured nothing, and the screen would agree
// with them. Naming the mismatch costs one 400 and buys back that whole misunderstanding.
//
// # WHY A CA UNDER `plaintext` IS ALSO AN ERROR
//
// There is no certificate to check when there is no TLS session, so the pair is meaningless in
// the migration's precise sense — the same reason the column holds three values instead of two
// booleans. Storing it anyway would leave a row that READS as verified-with-a-CA to anyone
// auditing the table, while the wire carries the password in clear.
func checkCredTransport(kind, mode, caPEM string) (string, string, error) {
	caPEM = strings.TrimSpace(caPEM)
	mode = strings.TrimSpace(strings.ToLower(mode))
	if kind != CredKindGNMI {
		if mode != "" || caPEM != "" {
			return "", "", errors.New("only a \"" + CredKindGNMI + "\" credential carries a " +
				"transport posture: \"" + kind + "\" has none to set. An SNMP community has no " +
				"TLS session to configure, and an administration password travels over SSH, which " +
				"is never in the clear — a CA filed here would protect nothing while looking like " +
				"it protected something")
		}
		// Empty on both: the store reads that as "leave the column alone", which is what keeps
		// an ordinary admin-password edit from touching a row it knows nothing about.
		return "", "", nil
	}
	switch mode {
	case "":
		// UNSPECIFIED BECOMES `verify`, EXPLICITLY, rather than being left empty for the store's
		// coalesce to resolve. On a create the two are the same thing — the column defaults to
		// `verify` — but on an UPDATE they are opposites: empty would preserve whatever is
		// already stored, so a client that stopped sending the field would silently keep a
		// `plaintext` posture it never showed anybody. Naming the safe value here means the only
		// way to stay in the clear is to keep asking for it.
		mode = TLSVerify
	case TLSVerify, TLSSkipVerify:
	case TLSPlaintext:
		if caPEM != "" {
			return "", "", errors.New("a certificate authority makes no sense with \"" +
				TLSPlaintext + "\": there is no TLS session and therefore no certificate to check. " +
				"Choose \"" + TLSVerify + "\" to actually use this CA, or clear the CA field")
		}
	default:
		return "", "", errors.New("unknown transport mode \"" + mode + "\": expected \"" +
			TLSVerify + "\" (TLS, certificate checked), \"" + TLSSkipVerify + "\" (TLS, " +
			"certificate NOT checked — for a switch's factory self-signed certificate) or \"" +
			TLSPlaintext + "\" (NO TLS: the device login travels in clear in gRPC metadata)")
	}
	return mode, caPEM, nil
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

	var transportErr error
	in.TransportTLS, in.CAPEM, transportErr = checkCredTransport(in.Kind, in.TransportTLS, in.CAPEM)
	if transportErr != nil {
		respondErr(w, http.StatusBadRequest, transportErr.Error())
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
		TransportTLS: in.TransportTLS, CAPEM: in.CAPEM,
	}, enc, enc2, me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	var what = "credential \"" + credLabel(c) + "\" saved"
	if enc != nil {
		what = "secret for credential \"" + credLabel(c) + "\" changed"
	}
	// PLAINTEXT IS AUDITED SEPARATELY, AND AT WARNING. Choosing it means a device login will
	// travel in clear in gRPC metadata on the management VLAN, which is a security decision
	// somebody made on purpose — the one thing that must not happen is for it to stop being
	// visible once telemetry is working. The proxy already says so per subscription in its own
	// log (gnmipoll.transport), but a proxy log is not where anyone reviews who decided what:
	// this trail is. A notice reading "credential saved" would bury it.
	if c.Kind == CredKindGNMI && c.TransportTLS == TLSPlaintext {
		s.emitSite(r, siteID, "credential.updated", model.SevWarning,
			"Telemetry credential \""+credLabel(c)+"\" is set to PLAINTEXT by "+me.Email+
				": its password will travel in clear in gRPC metadata on the management "+
				"network. Every device this credential's scope covers is affected.", nil)
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
// docs/en/03-Security/Service identity and rotation.md section 7).
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
// never in a single state (cf. docs/en/03-Security/Service identity and rotation.md section 3).
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
	// `moved` names the OTHER credential rows that held a copy of this same account's
	// password and were carried along in the SAME transaction. See
	// internal/store/credresync.go: before that existed, a `gnmi` row naming the managed
	// account kept the previous generation's secret after a rotation and telemetry died
	// with nothing reporting it.
	var moved []store.SiblingResync
	g, moved, err = s.Store.AddGeneration(r.Context(), c.ID, "", enc, enc2, me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "credential.rotated", model.SevWarning,
		"New generation "+itoa(g.Generation)+" for \""+credLabel(c)+"\". "+
			"The previous one stays valid while devices still carry it.", nil)
	// A SECOND CREDENTIAL MOVING IS ITS OWN FACT, and it gets its own line rather than a
	// clause appended to the one above. An operator reading the journal to answer "what
	// changed when telemetry stopped" must find the rows that moved named, not inferred
	// — and this is the site journal every other credential gesture already writes to, so
	// it needs no new screen to be seen.
	if len(moved) > 0 {
		s.emitSite(r, siteID, "credential.resynced", model.SevWarning,
			credResyncMessage(credLabel(c), g.Generation, moved), nil)
	}
	respond(w, http.StatusCreated, g)
}

// credResyncMessage writes the journal line for the credential rows that followed a
// rotation.
//
// PURE, so that what the audit trail says can be pinned without a database — and so that
// the one property that matters about it is checkable: IT IS BUILT FROM IDENTITY ONLY.
// The rotation's cleartext secret is in scope at the call site (`in.Secret`), and this
// function is not given it and has no field to put it in — [store.SiblingResync] carries
// an id, a kind, an account name and a generation number, all four of which the
// credential list already shows. A journal that leaked a password would be worse than
// one that said nothing.
//
// The sentence names the WHY as well as the what, because the reader of this line is
// somebody working out why telemetry stopped, and "gnmi moved to generation 3" alone does
// not tell them these rows are copies of one login.
func credResyncMessage(label string, generation int, moved []store.SiblingResync) string {
	var what = itoa(len(moved)) + " other credential"
	if len(moved) > 1 {
		what += "s"
	}
	var msg = "Rotating \"" + label + "\" to generation " + itoa(generation) +
		" also moved " + what + " naming the same account: "
	var i int
	for i = range moved {
		if i > 0 {
			msg += ", "
		}
		msg += "\"" + moved[i].Kind + "\" (" + moved[i].Username + ", now generation " +
			itoa(moved[i].Generation) + ")"
	}
	return msg + ". Those rows hold a COPY of this one account's password: left behind, " +
		"they keep RESOLVING with the previous secret, which is how a rotation used to " +
		"kill gNMI collection with nothing reporting it."
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

// handleCredentialDeployment answers, per device, WHICH generation of this credential
// is installed — and whether the current one may be promoted.
//
// # Why a whole endpoint for what the list already counts
//
// `GET /credentials` attaches `installed_on` to each generation: "3 devices still carry
// generation 4". That count forbids a purge and settles nothing else. Rotating a
// site-wide password today changes the vault and changes nothing on the fleet
// (observed on lab/CaserneLab 2026-08-07), and the operator's only way to find out
// which switches actually took the new secret is to log into them one by one — which
// is what was done by hand on 2026-08-08 across eight devices. This endpoint is that
// walk, read from what meshcraft OBSERVED rather than from what it intended.
//
// READ-ONLY, DELIBERATELY. The deployment runner does not exist yet (see the header of
// internal/store/deployment.go for the ordering it must respect and why the pieces
// that would mint and promote a generation are not built). Shipping the READING first
// is not a half-measure: partial deployment is the normal state of a fleet, so being
// able to see it is worth more than being able to trigger it, and nothing here can put
// the fleet in a state it was not already in.
//
// `operator`, like the credential list: this returns account names and generation
// numbers, never a secret.
func (s *Server) handleCredentialDeployment(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "operator")
	if !ok {
		return
	}
	var credID = chi.URLParam(r, "credID")
	// Through the site's list rather than by id: it establishes ownership and touches no
	// secret. A deployment view has no business decrypting anything.
	var creds, err = s.Store.ListSiteCredentials(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var cred model.SiteCredential
	var found bool
	var i int
	for i = range creds {
		if creds[i].ID == credID {
			cred, found = creds[i], true
			break
		}
	}
	if !found {
		respondErr(w, http.StatusNotFound, "not found")
		return
	}

	// The TARGET is the newest active generation — the same one `candidatesFor` aims at
	// when applying to a single device. Reading it from anywhere else would let this
	// screen report progress towards a generation no push is actually placing.
	var gens []model.CredentialGeneration
	gens, err = s.Store.ListGenerations(r.Context(), credID)
	if err != nil {
		fail(w, err)
		return
	}
	var target int
	for i = range gens {
		if gens[i].State == "active" && gens[i].DeviceID == "" {
			target = gens[i].Generation
			break
		}
	}

	var observed []store.DeviceGeneration
	observed, err = s.Store.InstalledGenerations(r.Context(), credID)
	if err != nil {
		fail(w, err)
		return
	}
	var scope, scopeErr = s.devicesScopedTo(r.Context(), siteID, cred)
	if scopeErr != nil {
		fail(w, scopeErr)
		return
	}
	var plan = store.PlanDeployment(target, scope, observed)
	var safe, why = plan.SafeToPromote()
	respond(w, http.StatusOK, map[string]any{
		"credential_id": credID,
		"plan":          plan,
		"outstanding":   plan.Outstanding(),
		// Reported even though nothing consumes it yet: the verdict and the plan must be
		// computed by the SAME code the runner will call, or the screen will one day say
		// "ready" about a promotion the store refuses.
		"safe_to_promote": safe,
		"blocked_because": why,
	})
}

// devicesScopedTo returns the ids of the site's devices for which THIS credential is
// the one that would be used.
//
// Resolution is not recomputed here: each device is put through
// `store.CredentialsForDevice` — tag, then group, then site default — which is the
// single place that rule lives. Inverting it into one query would be a second copy,
// and the day the two copies disagree meshcraft reports deployment progress for one
// credential while pushing another.
//
// One resolution per device is chatty, and that is the accepted price. This is an
// operator-triggered read over the devices of one site, not a polling path, and the
// alternative buys speed with the exact duplication the store comment warns against.
//
// A device whose resolution fails is left OUT of the scope rather than guessed into
// it: it will still appear if it was ever observed under this credential, because
// [store.PlanDeployment] folds observation in regardless of scope.
func (s *Server) devicesScopedTo(ctx context.Context, siteID string,
	cred model.SiteCredential) ([]string, error) {
	var devices, err = s.Store.ListDevices(ctx, siteID)
	if err != nil {
		return nil, err
	}
	var out = []string{}
	var d model.Device
	for _, d = range devices {
		var matches, matchErr = s.Store.CredentialsForDevice(ctx, d.ID, d.SiteID,
			s.groupNameOfCtx(ctx, d), cred.Kind)
		if matchErr != nil || len(matches) == 0 {
			continue
		}
		// The WINNER only. `CredentialsForDevice` returns the whole ranked list so a
		// caller can flag ambiguity; scope is about what will actually be pushed, and
		// that is the first entry.
		if matches[0].ID == cred.ID {
			out = append(out, d.ID)
		}
	}
	return out, nil
}

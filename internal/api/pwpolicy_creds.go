package api

import (
	"errors"
	"net/http"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/pwgen"
	"meshcraft/internal/pwpolicy"
	"meshcraft/internal/tagging"
)

// Administration credential ADAPTED TO PLATFORM POLICY.
//
// Twin of snmpidentity.go, deliberately built on the same plan: `internal/pwpolicy`
// DECIDES ("which constraint, which name, which length?"), here we PREPARE THE GROUND
// (technical tag, then credential), and only the connector writes on the device.
//
// # The original defect, observed on hardware
//
//	POST /api/devices/<css326>/credentials/apply
//	-> ok:false — "password refused before send: new password:
//	              17 characters, device accepts at most 15"
//
// The SwOS connector refused BEFORE emitting a byte, which is exactly its job. But
// `driver.AdminAccount.PasswordMax` was declared by all three connectors and read by nobody:
// no password could be pushed on a CSS326 once the site secret exceeded 15 characters. The
// capability exists to prevent that case; someone had to consult it when generating.
//
// # What is generated, and under what conditions
//
// Four properties, in order of importance:
//
//   - BY PLATFORM. The operator enters nothing: neither the tag (output of
//     `pwpolicy.Policy.Tag`), nor the secret (`pwgen.NewBounded`);
//   - ADAPTED TO CAPABILITIES. Length comes from connector bounds, not a site convention;
//   - IRREVOCABLE BY USERS. `checkCredScope` refuses the namespace on create, update, and
//     delete, and the tag carries a source rule replay does not touch;
//   - ONLY IF NONE EXISTS YET. A compatible secret found on the site is USED, never
//     duplicated — in the sense of bounds, not name.
//
// # What this file does NOT do
//
// It pushes nothing to the device and changes no secret already pushed. Generating prepares
// the write, it does not replace it: until the connector has written, the device still
// carries the old secret — and that is exactly why `candidatesFor` try order includes
// `PreviousInstalls`, which returns what the device is KNOWN to carry under another site
// credential. Without that safety net, moving a CSS326 from site secret to tag secret would
// mean trying a password we never wrote and ignoring the one we wrote ourselves: on a line
// with no serial port, that is repaired with a paperclip.

// pwPolicyTagSource marks a TECHNICAL password-policy tag.
//
// FOURTH source of `device_tags.source`, after `manual`, `rule:<uuid>`, and `snmp`. Neither
// of the first three would do, for the reasoning already written for `snmp` (tags page
// section 2.1):
//
//   - `manual` would LIE about origin — no operator decided anything — and expose the tag
//     to manual editing;
//   - `rule:<uuid>` would make it removable by rule replay. A `tag-rules/apply` would then,
//     in passing, unplug the only credential whose secret is pushable on that line: the
//     device would fall back to the site secret, which its own connector refuses.
//
// A separate source is therefore what guarantees replay breaks nothing — `tagging.Evaluate`
// only removes `rule:`-prefixed sources. A test locks this (TestEtiquettePwPolicySurvitAuRejeuDesRegles):
// not an implementation detail, the property that makes replay safe.
const pwPolicyTagSource = "pwpolicy"

// pwPolicyOffer is a site credential with its secret IN CLEAR, as the chooser examines it.
//
// The secret never leaves this type: it serves to measure length and character set, and it
// does not go into a log, event, or HTTP response (cf. internal/store/sitecreds.go, which
// never returns a secret from a list — `RevealSiteCredential` does, and the caller carries
// traceability).
type pwPolicyOffer struct {
	Cred   model.SiteCredential
	Secret string
}

// pwPolicyPick chooses, among a site's technical credentials, the one a device governed by
// this policy can carry.
//
// PURE, and deliberate: this is the decision that avoids multiplying secrets, and it must
// be testable without a database. Two rules, each for a precise reason:
//
//   - COMPATIBILITY is judged on bounds, NOT NAME. A `pwpolicy-len12` credential perfectly
//     serves a device capped at 15 characters: its secret fits. Requiring the canonical
//     name would create a second secret for an already covered constraint, then both would
//     need rotation;
//   - at tie, the CANONICAL tag wins. Without that tie-break, database order decides — and
//     two devices of the same line adopted in different order would end up under two
//     different tags, exactly the split fleet the reserved namespace exists to prevent.
//
// Only examines ADMINISTRATION credentials from the reserved namespace. Borrowing an
// OPERATIONS tag credential ("regie") would be worse than useless: meshcraft would decide
// filing that belongs to the operator, and the operator could delete that credential without
// knowing a CSS326 depended on it.
//
// Per-device credentials are excluded, since they cannot be judged: their shared secret is
// carried by no device, real secrets live in per-device generations. Generating a new one is
// the safe behavior.
func pwPolicyPick(pol pwpolicy.Policy, canonical string,
	have []pwPolicyOffer) (model.SiteCredential, bool) {
	var best model.SiteCredential
	var found bool
	var o pwPolicyOffer
	for _, o = range have {
		if o.Cred.Kind != credKindAdmin || o.Cred.PerDevice {
			continue
		}
		if !pwpolicy.IsTechnicalTag(o.Cred.Tag) {
			continue
		}
		if pol.Accepts(o.Secret) != nil {
			continue
		}
		if o.Cred.Tag == canonical {
			return o.Cred, true
		}
		if !found {
			best, found = o.Cred, true
		}
	}
	return best, found
}

// errNoVaultForPolicy: without a vault, there is nowhere to store a secret. Say so rather
// than generate what we cannot read back.
const errNoVaultForPolicy = apiError("secret vault unavailable: " +
	"meshcraft cannot generate a password adapted to this platform")

// ensureAdminSecretFits ensures a device falls under a credential whose secret is PUSHABLE
// on its platform, and tells the caller when something had to change.
//
// Called at the only place that writes a password on a device (handleApplyDeviceCredential),
// and AFTER the candidate list was built — therefore with the actually targeted secret in
// hand, before the first byte toward the device. The check is on that secret, not a
// convention: a site whose operator entered a 12-character password needs nothing, even on
// a CSS326.
//
// Returns the REASON for change, empty when nothing had to be done. A reason rather than a
// boolean: it is the refusal the platform would have made to the site secret ("17-character
// secret: device accepts at most 15"), and that is what the operator must read — otherwise
// they wonder long why the site password does not open that switch. It never contains the
// secret, only its length.
//
// When the reason is non-empty, the caller must RE-RESOLVE its credential: the device now
// carries a tag, and `store.CredentialsForDevice` — one place — decides what a tag implies.
// Redoing that calculation here in parallel would be a second copy of a resolution rule,
// i.e. the day meshcraft tries one credential's generations to push another.
//
// FAILS FRANKLY rather than silently, unlike `ensureSNMPIdentity`. Treatment differs
// because of the gesture: failing to generate a supervision identity is an annoyance fixed
// on the next pass, whereas here the operator explicitly asked to push a password —
// continuing with a secret we KNOW the platform will refuse would show a connector failure
// where meshcraft had the answer.
func (s *Server) ensureAdminSecretFits(req *pwPolicyRequest) (string, error) {
	var pol = pwpolicy.For(req.Admin)
	// Most common case, and it costs nothing: the site secret fits the platform policy. No
	// tag, no credential, no log line. That is Instant On 1930 (8 to 64 characters) and
	// RouterOS (no bound observed).
	var why = pol.Accepts(req.Secret)
	if why == nil {
		return "", nil
	}
	if s.Secrets == nil {
		return "", errNoVaultForPolicy
	}
	// Tag BEFORE any write: if the policy is unsatisfiable — a ceiling below eight characters
	// — there is no secret to generate and refusal must be readable. Generating first to fail
	// at naming afterward would leave an orphan secret in the vault.
	var canonical, tagErr = pol.Tag()
	if tagErr != nil {
		return "", errPolicyImpossible(req.Device.Name, tagErr)
	}

	var ctx = req.Request.Context()
	var creds, listErr = s.Store.ListSiteCredentials(ctx, req.Device.SiteID)
	if listErr != nil {
		return "", listErr
	}
	// The question is "does a compatible credential exist IN THE SITE?", not "which one
	// applies to this device?". We therefore list the site, as SNMP provisioning does for
	// the same reason: `CredentialsForDevice` only sees a tag credential if the device
	// ALREADY carries the tag. On first pass on a site's second CSS326, it would find nothing
	// — and upsert would overwrite the secret already installed on the first. A shared secret
	// regenerated at each push is a fleet lost one device at a time.
	var offers = make([]pwPolicyOffer, 0, len(creds))
	var c model.SiteCredential
	for _, c = range creds {
		if c.Kind != credKindAdmin || !pwpolicy.IsTechnicalTag(c.Tag) || c.PerDevice {
			continue
		}
		var _, enc, _, revErr = s.Store.RevealSiteCredential(ctx, c.ID)
		if revErr != nil {
			continue
		}
		var clear, openErr = s.Secrets.Open(enc)
		if openErr != nil {
			// A secret we can no longer open cannot be judged, therefore cannot be reused. We
			// ignore it: generating is safer than assuming.
			continue
		}
		offers = append(offers, pwPolicyOffer{Cred: c, Secret: clear})
	}

	var chosen, found = pwPolicyPick(pol, canonical, offers)
	if !found {
		var created, createErr = s.createPolicyCredential(req, pol, canonical, why)
		if createErr != nil {
			return "", createErr
		}
		chosen = created
	}
	// Tag LAST, once the credential is in place: in the other order, a failure between the two
	// would leave the device tagged for a credential that does not exist — therefore no
	// applicable credential, which `credentialForDevice` translates as flat refusal. Tagging
	// after, at worst an credential nothing carries yet, fixed on the next pass.
	var tagErr2 = s.tagForPolicy(req, chosen.Tag)
	if tagErr2 != nil {
		return "", tagErr2
	}
	return why.Error(), nil
}

// pwPolicyRequest groups what is needed to adapt a credential.
//
// An aggregate rather than six parameters: the first three fields all come from the same
// HTTP gesture and must designate the same device. Letting them travel separately has
// already produced, elsewhere in this layer, two variables that ended up talking about two
// different devices.
type pwPolicyRequest struct {
	// Request carries context AND gesture author: an operator asked for the push, and the log
	// must say which (cf. `emit`, which takes the actor from request context).
	Request *http.Request
	Device  model.Device
	// Admin: platform capability, as its connector declares it.
	Admin driver.AdminAccount
	// Secret: the secret meshcraft was about to push (active generation of resolved
	// credential). That is what we judge, not a site convention.
	Secret string
	// Username: account name from resolved credential, copied as-is onto generated credential.
	// Two credentials on the same site disagreeing on account name would make the device look
	// for a user that does not exist — and `caps.Admin.EffectiveUsername` will decide what is
	// actually pushable anyway.
	Username string
}

// createPolicyCredential generates a policy credential, and logs it.
//
// THE SECRET IS GENERATED HERE AND NEVER LEAVES: not in the HTTP response, event, or vault
// note. It is revealed from Configuration -> Credentials, a distinct gesture, reserved to
// `site_admin` and traced — repo rule, all the more for a secret nobody asked for.
//
// CREATION is logged. meshcraft fabricates a password on its own initiative: a secret that
// appears without a trace is exactly what you regret not finding afterward, and the event
// says WHY it had to happen.
func (s *Server) createPolicyCredential(req *pwPolicyRequest, pol pwpolicy.Policy,
	canonical string, why error) (model.SiteCredential, error) {
	var n, lenErr = pol.Length()
	if lenErr != nil {
		return model.SiteCredential{}, errPolicyImpossible(req.Device.Name, lenErr)
	}
	var secret, genErr = pwgen.NewBounded(pol.Min, pol.Max)
	if genErr != nil {
		return model.SiteCredential{}, errPolicyImpossible(req.Device.Name, genErr)
	}
	var enc, sealErr = s.Secrets.Seal(secret)
	if sealErr != nil {
		return model.SiteCredential{}, sealErr
	}
	var username = req.Username
	if username == "" {
		username = pwgen.DefaultUsername
	}
	// Empty `updatedBy`: meshcraft acts. The operator asked for a push, they did not ask for
	// this credential — attributing it to them would suggest, on review, a human chose this
	// scope.
	var created, upsertErr = s.Store.UpsertSiteCredential(req.Request.Context(),
		model.SiteCredential{
			SiteID: req.Device.SiteID, Tag: canonical, Kind: credKindAdmin,
			Username: username,
			Note: "Administration password generated by meshcraft for devices whose policy " +
				"requires " + itoa(n) + " characters (technical tag, set automatically). The " +
				"site secret does not fit these platforms' bounds: their connector refuses it " +
				"before sending. This credential is neither editable nor deletable by hand — " +
				"meshcraft manages it.",
		}, enc, nil, "")
	if upsertErr != nil {
		return model.SiteCredential{}, upsertErr
	}
	s.emitDeviceEvent(req.Request, req.Device, "credential.generated", model.SevNotice,
		"Administration password generated for technical tag \""+canonical+
			"\" ("+itoa(n)+" characters): platform of \""+req.Device.Name+
			"\" refuses the site secret before even sending it ("+why.Error()+
			"). No secret has changed on the device yet.")
	return created, nil
}

// tagForPolicy sets the technical tag on the device, if not already there.
//
// IDEMPOTENT, and the prior read is not a query savings: without it, every password push
// would write an "tag set" log line for a tag already in place, and the log of a site where
// pushes are replayed would become unreadable. What must show is the moment the device
// CHANGES filing.
func (s *Server) tagForPolicy(req *pwPolicyRequest, tag string) error {
	var ctx = req.Request.Context()
	var current, err = s.Store.DeviceTags(ctx, req.Device.ID)
	if err != nil {
		return err
	}
	var t tagging.Tag
	for _, t = range current {
		if t.Tag == tag {
			// Already set. Source is not corrected if it differs: re-setting a tag an operator
			// confirmed by hand would withdraw their decision, and a manual tag survives replays
			// anyway — the only risk we sought to avoid.
			return nil
		}
	}
	if err = s.Store.SetDeviceTag(ctx, req.Device.ID, tag, pwPolicyTagSource); err != nil {
		return err
	}
	s.emitDeviceEvent(req.Request, req.Device, "tag.assigned", model.SevNotice,
		"Technical tag \""+tag+"\" set on \""+req.Device.Name+
			"\": its platform password policy does not accept the site secret; this tag now "+
			"decides the password it will receive. It is managed by meshcraft and is not removed "+
			"by a tag-rule replay.")
	return nil
}

// errPolicyImpossible wraps refusal for an unsatisfiable platform, so the operator reads what
// blocks rather than a generator message.
func errPolicyImpossible(deviceName string, err error) error {
	return errors.New("no password can be pushed on \"" + deviceName +
		"\": " + err.Error())
}

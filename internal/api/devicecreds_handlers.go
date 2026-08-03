package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"meshcraft/internal/auth"
	"meshcraft/internal/discovery"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
	"meshcraft/internal/pwpolicy"
	"meshcraft/internal/store"
)

// Place service identity on a device.
//
// This is the culmination of generations: until now meshcraft knew WHICH secret should be in
// place; here it applies it, records what actually worked, and saves that. Three principles
// follow:
//
//  1. to get in, secrets are tried IN THE ORDER the database gives (observed generation, then
//     generations being retired, then the active one), then the factory password. A device
//     adopted yesterday and a device two rotations behind use the same path;
//  2. the account name is what the CONNECTOR allows, not what we wish: on ranges that only
//     admit `admin`, placing `mcadmin` would fail while looking like success;
//  3. everything is recorded in a transcript returned to the operator. On a device one can
//     make unreachable, a silent failure is the worst outcome.

type applyCredsInput struct {
	// ProxyID: which proxy to reach the device through. By default, the one that discovered it.
	ProxyID string `json:"proxy_id"`
	// DryRun: read, authenticate, stop before writing. Used to verify we can get in before
	// changing anything.
	DryRun bool `json:"dry_run"`
	// CurrentSecret is the device's CURRENT password when meshcraft does not know it.
	//
	// This is the takeover case: a device configured by hand before meshcraft carries a secret
	// the database has no trace of, and without it there is no other ingress back to factory
	// settings. It is tried FIRST, never stored, and disappears with the request — what is
	// kept is the generation installed afterward.
	CurrentSecret string `json:"current_secret"`
}

func (s *Server) handleApplyDeviceCredential(w http.ResponseWriter, r *http.Request) {
	// Placing an administration password is not a routine operations action: it requires
	// site_admin, the same level that can already reveal it.
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in applyCredsInput
	if !decodeOptional(w, r, &in) {
		return
	}
	if d.MgmtIP == "" {
		respondErr(w, http.StatusUnprocessableEntity,
			"this device has no known management address")
		return
	}

	// The connector decides what can be placed, and it is the one that will write:
	// [driver.ConnectorFor] distinguishes "no associated connector", "connector unknown to
	// this binary", and "connector that can describe this platform but not reach it". The
	// three are repaired by unrelated actions — hence three phrases, not one.
	var conn, connErr = s.connectorFor(r.Context(), d)
	if connErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, connErr.Error())
		return
	}
	var caps = conn.Capabilities(d.Model)

	// Wanted secret: the device's TAG credential, else its group's, else the site's (cf.
	// docs/01-Architecture/Tags and credential scope §3).
	var groupName = s.groupNameOf(r.Context(), d)
	var cred, ambiguous, credErr = s.credentialForDevice(r.Context(), d, groupName)
	if credErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, credErr.Error())
		return
	}
	var wanted = caps.Admin.EffectiveUsername(cred.Username)

	var cands, target, targetGen, credID, buildErr = s.candidatesFor(r.Context(), d, wanted,
		groupName, in.CurrentSecret)
	if buildErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, buildErr.Error())
		return
	}

	// PLATFORM PASSWORD POLICY IS CONSULTED HERE, before the first byte sent to the device.
	//
	// This closes a default this batch fixed: `driver.AdminAccount.PasswordMax` was declared
	// by all three connectors and read by nobody, even though no password could be placed on
	// a MikroTik CSS326 (SwOS caps at 15 characters) when the site secret was longer. The
	// connector refused — correctly — "before send", and the operator had no way out.
	//
	// Control applies to `target`, i.e. the secret ACTUALLY targeted, not a convention: a site
	// where the operator entered twelve characters needs nothing, same on a CSS326. When it
	// does not fit, meshcraft uses — or generates — an adapted technical-tag credential (cf.
	// pwpolicy_creds.go), and the device's scope changes.
	//
	// BEFORE `dry_run`, DELIBERATELY. `dry_run` stops before writing on the DEVICE; adaptation
	// prepares the vault and is idempotent. Skipping it would tell the trial "I will apply
	// this secret" while announcing a generation the real apply will not use — a trial that
	// does not repeat the step verifies nothing.
	var policyWhy, adaptErr = s.ensureAdminSecretFits(&pwPolicyRequest{
		Request: r, Device: d, Admin: caps.Admin,
		Secret: target, Username: cred.Username,
	})
	if adaptErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, adaptErr.Error())
		return
	}
	if policyWhy != "" {
		// THE WHOLE PATH IS REDONE, not patched: the device now carries a tag, and what that
		// tag implies is decided in one place only (`store.CredentialsForDevice`, tags page §3).
		// Recalculating "which credential that gives" here would be a second copy of the
		// resolution rule — the one that, the day it diverges, will try one credential's
		// generations while applying another.
		cred, ambiguous, credErr = s.credentialForDevice(r.Context(), d, groupName)
		if credErr != nil {
			respondErr(w, http.StatusUnprocessableEntity, credErr.Error())
			return
		}
		wanted = caps.Admin.EffectiveUsername(cred.Username)
		cands, target, targetGen, credID, buildErr = s.candidatesFor(r.Context(), d, wanted,
			groupName, in.CurrentSecret)
		if buildErr != nil {
			respondErr(w, http.StatusUnprocessableEntity, buildErr.Error())
			return
		}
		// LAST LOCK, and it is not decorative: we refuse to write a secret we already know the
		// platform will reject. The case needs a concurrent higher-priority scope targeting the
		// same device — §3 ambiguity — and a readable refusal beats a round trip to the
		// connector's "refused before send", which does not say what to fix.
		var policyErr = pwpolicy.For(caps.Admin).Accepts(target)
		if policyErr != nil {
			respondErr(w, http.StatusUnprocessableEntity,
				"the secret for credential \""+credLabel(cred)+"\" cannot be placed on "+
					"this platform ("+policyErr.Error()+") even though an adapted technical "+
					"tag exists: a higher-priority credential targets this device. Lower its "+
					"priority or remove its tag.")
			return
		}
	}

	var proxyID = in.ProxyID
	if proxyID == "" {
		proxyID = s.proxyOf(r.Context(), d)
	}
	var dialer, dialErr = s.dialerFor(proxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}

	// Address goes WITHOUT scheme or port: the connector knows how its platform is reached. Port
	// prefix is passed even though credential apply does not use it — Target is an aggregate,
	// and filling it halfway per action would create two forms of the same thing.
	var tgt = driver.Target{
		Host: d.MgmtIP, Dialer: dialer, Model: d.Model, PortPrefix: portPrefix(d),
	}
	var out = map[string]any{
		"username":   wanted,
		"generation": targetGen,
		"proxy_id":   proxyID,
		// Wanted name and placeable name differ on many ranges: the UI must say so, otherwise
		// the operator will look for an account that does not exist.
		"wanted_username": cred.Username,
	}
	if wanted != cred.Username {
		out["username_note"] = "This model only allows one administration account (\"" +
			wanted + "\"): the name \"" + cred.Username + "\" cannot be placed here."
	}
	if cred.Tag != "" {
		out["scope_tag"] = cred.Tag
	}
	// AN ADAPTATION IS ANNOUNCED. The operator asked to "apply the site password" and gets
	// another one: staying silent would make them wonder later why the site secret does not
	// open this switch. The sentence also says it is not the site secret, without showing it —
	// the Credentials page reveals, a distinct traced action.
	if policyWhy != "" {
		out["scope_adapted"] = cred.Tag
		out["scope_adapted_note"] = "The site password cannot be placed on this " +
			"platform (" + policyWhy + "): meshcraft uses the credential from " +
			"technical tag \"" + cred.Tag + "\", which it manages itself. This secret " +
			"is revealed from Configuration → Credentials."
	}
	// Ambiguous scope is not GUESSED: meshcraft had to choose, but it says so. Silent
	// arbitration between two contradictory operator intentions is a bug waiting to happen.
	if len(ambiguous) > 0 {
		out["scope_ambiguous"] = ambiguous
		s.emitDeviceEvent(r, d, "credential.scope_ambiguous", model.SevWarning,
			"Several credentials target \""+d.Name+"\" (tags "+
				strings.Join(ambiguous, ", ")+"): \""+cred.Tag+"\" was retained.")
	}

	var adm, err = conn.Authenticate(r.Context(), tgt, cands)
	// THE TRANSCRIPT IS AGGREGATED LOCALLY, it is no longer muted. Old code did
	// `admin.Steps = append(admin.Steps, verify.Steps...)`: writing into a connector field to
	// compose a display worked by accident — the field existed and was exported — and the
	// contract now exposes it only as READ ([driver.Admin.Transcript]). Sensible flow: the
	// connector writes what it did, the caller assembles what it shows.
	var steps = transcriptOf(adm)
	out["steps"] = steps
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
		s.emitDeviceEvent(r, d, "credential.apply_failed", model.SevWarning,
			"Credential not placed on \""+d.Name+"\": "+err.Error())
		respond(w, http.StatusOK, out)
		return
	}
	if in.DryRun {
		out["ok"] = true
		out["dry_run"] = true
		respond(w, http.StatusOK, out)
		return
	}

	// CURRENT SECRET COMES FROM THE CONTRACT, not the transcript — fixing a defect that was in
	// PRODUCTION. `currentSecretOf` searched the transcript for a step whose label starts with
	// "connected as", then cross-checked its `Why` with the candidate list — but the Instant On
	// connector writes detail as `Why + " — code N: ..."`, so the compared equality was NEVER
	// true and the function always returned an empty string. Invisible on Instant On, which
	// ignores the old password (its session is proof); FATAL on SwOS, whose old secret is the
	// RC4 key of the expected digest and which returns HTTP 405 without it. A transcript label
	// is human text: rewording it is legitimate, and it silently broke the path that leaves a
	// device with a password the database does not know.
	var current = adm.Accepted().Secret
	var account = adm.Account()

	// TWO situations, one difference only: is the targeted account the one we just entered as?
	//
	//   - same account → change its password;
	//   - different account → RENAME. The device indexes accounts by name: it has no "rename"
	//     operation. We therefore create the targeted account, and the old one is removed only
	//     once the new one is VERIFIED, below.
	var renaming = !strings.EqualFold(account, wanted)
	var previousUser = account
	// Access to NAMED ACCOUNTS is optional: SwOS has no notion of account — not "not yet", not
	// at all. Its password form has only three fields, the page that receives it has no key
	// (`{pwd:''}`), and its full configuration backup mentions no username. The assertion is
	// therefore the SECOND lock: the first is `caps.Admin.EffectiveUsername`, which already
	// resolved the fleet name to what the platform accepts — if we arrive here wanting to
	// rename on such a platform, a capability was ignored upstream.
	var accounts, canName = adm.(driver.AccountManager)
	if renaming && !canName {
		respondErr(w, http.StatusUnprocessableEntity, driver.NotSupported(
			conn.Info().Name, "create administration account \""+wanted+"\"",
			"this platform has no notion of named account: there is nothing to create, rename, "+
				"or delete. The only placeable secret is that of account \""+account+"\" "+
				"through which meshcraft just entered").Error())
		return
	}
	if renaming {
		out["renamed_from"] = previousUser
		err = accounts.EnsureAccount(r.Context(), wanted, target)
	} else {
		// THE ACCOUNT IS NOT A PARAMETER: it is the one we just entered as. And `SetSecret`
		// PERSISTS by itself when the platform distinguishes the two — an administration
		// password that would not survive reboot would bring the device back to its factory
		// account without password, i.e. exactly the trap provisioning exists to close.
		err = adm.SetSecret(r.Context(), current, target)
	}
	steps = adm.Transcript()
	out["steps"] = steps
	if err != nil {
		out["ok"] = false
		out["error"] = err.Error()
		s.emitDeviceEvent(r, d, "credential.apply_failed", model.SevWarning,
			"Credential not placed on \""+d.Name+"\": "+err.Error())
		respond(w, http.StatusOK, out)
		return
	}
	if renaming {
		// EXPLICIT PERSISTENCE STAYS WITH THE CALLER, and it is the contract's only asymmetry —
		// therefore justified. A rename puts TWO accounts in flight: create the new one, prove
		// it opens the device, remove the old one only afterward. Who persists what and WHEN is
		// a sequencing decision only the caller can make: in between, a power cut must not
		// leave the device with a single account, and not the one that survived.
		//
		// A platform WITHOUT [driver.Persister] is not a gap here: `immediate` means the
		// account is already in non-volatile memory. Saying so avoids looking for a save that
		// does not exist.
		persistNow(r.Context(), adm, out, "account created")
		steps = adm.Transcript()
		out["steps"] = steps
	}

	// We record ONLY what is observed: a SECOND access, with this sole candidate, proves the
	// secret is really in place. Without this verification, an accepted form with no effect
	// would leave the database convinced of success — failure mode observed on Instant On, and
	// it costs a site visit.
	var verify, verifyErr = conn.Authenticate(r.Context(), tgt, []driver.Candidate{
		{Username: wanted, Secret: target, Why: "verification of the new secret"},
	})
	// Old account removal happens ONLY HERE: after real access has been obtained under the new
	// name. On a range without out-of-band fallback, delete first and verify second would cost
	// a site visit.
	if renaming && verifyErr == nil {
		var vAccounts, vCanName = verify.(driver.AccountManager)
		if !vCanName {
			// New account works, but nothing here can remove the old one. Unreachable in
			// practice — same connector that created the account — and saying so costs less
			// than `panic` on an assertion.
			out["warning"] = "account \"" + previousUser + "\" remains active: this " +
				"connector can create an account but not delete one."
		} else {
			var delErr = vAccounts.DeleteAccount(r.Context(), previousUser)
			if delErr != nil {
				// Non-blocking failure: the targeted account works, that is what matters. The
				// old one remains, and silence would leave an open door nobody knows about.
				out["warning"] = "account \"" + previousUser + "\" could not be " +
					"removed and remains active on the device: " + delErr.Error()
				s.emitDeviceEvent(r, d, "credential.stale_account", model.SevWarning,
					"Account \""+previousUser+"\" still present on \""+d.Name+"\": "+delErr.Error())
			}
		}
		persistNow(r.Context(), verify, out, "old account removed")
	}
	// Concatenation is done on a copy, not the slice the connector just returned: `Transcript`
	// exposes its internal field, and appending would write into its backing array. No visible
	// consequence today — we respond right after — but it is the kind of silent sharing that
	// costs the day a connector records a step afterward.
	var all = make([]driver.Step, 0, len(steps)+2)
	all = append(all, steps...)
	all = append(all, transcriptOf(verify)...)
	out["steps"] = all
	if verifyErr != nil {
		out["ok"] = false
		out["error"] = "the change was not confirmed: " + verifyErr.Error()
		if renaming {
			// Precision that changes everything for the operator: the old account was not
			// touched, the device remains reachable as before.
			out["error"] = "account \"" + wanted + "\" could not be verified: " +
				verifyErr.Error() + " — \"" + previousUser + "\" remains in place"
		}
		s.emitDeviceEvent(r, d, "credential.apply_unconfirmed", model.SevWarning,
			"Credential placed on \""+d.Name+"\" but NOT confirmed: "+verifyErr.Error())
		respond(w, http.StatusOK, out)
		return
	}

	if err = s.Store.NoteInstalledGeneration(r.Context(), d.ID, credID,
		targetGen, ""); err != nil {
		fail(w, err)
		return
	}
	if err = s.Store.NoteInstalledUsername(r.Context(), d.ID, credID, wanted); err != nil {
		fail(w, err)
		return
	}
	out["ok"] = true
	s.emitDeviceEvent(r, d, "credential.installed", model.SevNotice,
		"Credential \""+wanted+"\" (generation "+itoa(targetGen)+") placed and confirmed on \""+d.Name+"\"")
	respond(w, http.StatusOK, out)
}

// persistNow makes durable what was just written, WHEN the platform distinguishes the two,
// and SAYS SO when it does not.
//
// Reserved for rename sequencing: elsewhere the contract persists itself ([driver.Admin.ApplyPorts],
// [driver.Admin.SetSecret]), and chaining would be one more round trip on an interface that
// only accepts five sessions. Not here: two accounts are in flight, and save order is a
// decision only the caller can make.
//
// ABSENCE OF [driver.Persister] IS NOT AN ERROR, it is information — `Commit: immediate`, the
// account is already in non-volatile memory. And a persistence FAILURE does not undo writing:
// the change is in place, and pretending otherwise would be false. Both go in a note, never in
// a refusal.
func persistNow(ctx context.Context, adm driver.Admin, out map[string]any, what string) {
	var notes, _ = out["persist_notes"].([]string)
	var saver, canSave = adm.(driver.Persister)
	if !canSave {
		out["persist_notes"] = append(notes, what+": nothing to save on this platform, it "+
			"applies and persists in one step. The change is ALREADY final — a reboot will not "+
			"undo it, and there is no rollback by power cycle.")
		return
	}
	var err = saver.Persist(ctx)
	if err != nil {
		out["persist_notes"] = append(notes, what+": flash save REFUSED ("+
			err.Error()+"). The change is in place, but it will not survive a reboot: "+
			"reapply the credential.")
		return
	}
	out["persist_notes"] = append(notes, what+": saved to flash.")
}

// groupNameOf returns the device's group name, empty if it has none: that chooses between
// group credential and site credential.
func (s *Server) groupNameOf(ctx context.Context, d model.Device) string {
	if d.GroupID == nil || *d.GroupID == "" {
		return ""
	}
	var g, err = s.Store.GetGroup(ctx, *d.GroupID)
	if err != nil {
		return ""
	}
	return g.Name
}

// proxyOf finds which proxy discovered the device. Known-working path — preferable to an
// arbitrary default.
func (s *Server) proxyOf(ctx context.Context, d model.Device) string {
	var discoveredID = driver.ExtraString(d.Extra, "discovered_id")
	if discoveredID != "" {
		var c, err = s.Store.GetDiscovered(ctx, discoveredID)
		if err == nil && c.ProxyID != "" {
			return c.ProxyID
		}
	}
	return store.LocalProxyID
}

// emitDeviceEvent logs an event attached to a specific device.
func (s *Server) emitDeviceEvent(r *http.Request, d model.Device, evType string, sev int, msg string) {
	var siteID, deviceID = d.SiteID, d.ID
	s.emit(r, model.Event{
		SiteID: &siteID, DeviceID: &deviceID,
		Type: evType, Severity: sev, Message: msg,
	})
}

// handleDeviceWebProbe reads a page from a device's web interface, through the same path as
// connectors.
//
// READ-ONLY, reserved for global-admin. On a range with neither SSH nor serial port, the web
// interface IS the device: when a connector does not recognize a page, the only alternative to
// this tool is plugging a laptop into the rack. Body is returned as-is, with forms already
// parsed — what is missing fastest when bringing up a connector.
func (s *Server) handleDeviceWebProbe(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	if !auth.UserFrom(r.Context()).IsGlobalAdmin {
		respondErr(w, http.StatusForbidden,
			"probe reserved for global administration")
		return
	}
	var in struct {
		Path string `json:"path"`
		// Paths: several paths IN THE SAME session. The device only accepts five concurrent
		// ones: opening a session per path to find the right URL evicts yourself.
		Paths   []string `json:"paths"`
		ProxyID string   `json:"proxy_id"`
		Port    int      `json:"port"`
		// Login: open a session before reading. A device UI shows almost nothing before
		// authentication; without this the probe only watches the login page.
		Login         bool   `json:"login"`
		CurrentSecret string `json:"current_secret"`
		// Match: instead of the start of the page, places containing this pattern. Recent
		// interfaces serve JavaScript bundles of hundreds of kilobytes where the useful call
		// fits on one line: without search, the probe only shows the header of an unreadable file.
		Match string `json:"match"`
		// Excerpt bounds what is returned: a UI page is hundreds of kilobytes of JavaScript
		// nobody wants to reread.
		Excerpt int `json:"excerpt"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	if in.Path == "" {
		in.Path = "/"
	}
	if in.Excerpt <= 0 {
		in.Excerpt = 4000
	}
	var proxyID = in.ProxyID
	if proxyID == "" {
		proxyID = s.proxyOf(r.Context(), d)
	}
	var dialer, dialErr = s.dialerFor(proxyID)
	if dialErr != nil {
		respondErr(w, http.StatusServiceUnavailable, dialErr.Error())
		return
	}
	var base = "https://" + d.MgmtIP
	if in.Port > 0 {
		base = "https://" + d.MgmtIP + ":" + itoa(in.Port)
	}
	// `full` is the ENTIRE content read, before any split: length and search apply to it,
	// excerpt only to what is returned.
	var full string
	var status int
	var url string
	var truncated bool
	var err error
	var steps []driver.Step
	var units = map[string]string{}
	if in.Login {
		// UNDER AUTHENTICATION, THE PROBE GOES THROUGH [driver.RawReader], and therefore loses
		// access to an ARBITRARY HTTP path. Assumed trade-off: reading any URL with a production
		// device's administration credentials is a primitive the contract did not want, and for
		// good reason — it existed only because the API layer held an Instant On session.
		// What remains: this range's JavaScript bundles are served WITHOUT authentication, so
		// the anonymous branch below covers them, and unknown write gestures are reconstructed
		// from them.
		var conn, adm, _, admErr = s.adminFor(r.Context(), d)
		if admErr != nil {
			respond(w, http.StatusOK, map[string]any{
				"error": admErr.Error(), "steps": transcriptOf(adm)})
			return
		}
		var rr, canRead = adm.(driver.RawReader)
		if !canRead {
			respondErr(w, http.StatusUnprocessableEntity, driver.NotSupported(
				conn.Info().Name, "read a configuration unit under authentication",
				"this platform exposes no readable configuration unit as such: there is no "+
					"name to request. The anonymous branch of this probe (without `login`) "+
					"remains available to read its public pages").Error())
			return
		}
		// SEVERAL units per access: Instant On only accepts five concurrent sessions, and
		// opening one per read to find the right name evicts yourself.
		var extra string
		for _, extra = range in.Paths {
			var body, rawErr = rr.ReadRaw(r.Context(), extra)
			if rawErr == nil {
				units[extra] = body
			} else {
				units[extra] = "read refused: " + rawErr.Error()
			}
		}
		full, err = rr.ReadRaw(r.Context(), in.Path)
		// Unit name, not a URL: what the UI must display, and `RawKind` says what "a unit"
		// means on this platform — `wcd` section, `*.b` page, REST menu.
		url = rr.RawKind() + " \"" + in.Path + "\""
		status = http.StatusOK
		steps = adm.Transcript()
	} else {
		// ANONYMOUS BRANCH UNCHANGED, and deliberately so: [driver.NewWebSession] is already
		// generic — shared transport for all three connectors, without platform knowledge.
		var web = driver.NewWebSession(base, dialer, 0)
		// The probe reads interface bundles: the ordinary limit cuts before what we search for.
		web.MaxBody = 8 << 20
		var resp *driver.Response
		resp, err = web.Get(r.Context(), in.Path)
		if resp != nil {
			full, status, url, truncated = resp.Body, resp.Status, resp.URL, resp.Truncated
		}
	}
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var body = full
	var matches []string
	if in.Match != "" {
		var re, reErr = regexp.Compile(in.Match)
		if reErr != nil {
			respondErr(w, http.StatusBadRequest, "invalid pattern: "+reErr.Error())
			return
		}
		var loc []int
		for _, loc = range re.FindAllStringIndex(full, 40) {
			var from = loc[0] - 120
			if from < 0 {
				from = 0
			}
			var to = loc[1] + 260
			if to > len(full) {
				to = len(full)
			}
			matches = append(matches, full[from:to])
		}
		body = ""
	} else if len(body) > in.Excerpt {
		body = body[:in.Excerpt]
	}
	s.emitDeviceEvent(r, d, "device.web_probed", model.SevInfo,
		"Page \""+in.Path+"\" read on \""+d.Name+"\" (bring-up probe)")
	respond(w, http.StatusOK, map[string]any{
		"status": status, "url": url, "length": len(full),
		"forms": driver.Describe(full), "body": body, "steps": steps,
		"matches": matches, "truncated": truncated, "units": units,
	})
}

// candidatesFor builds the ordered list of secrets to try on a device, and says which one we
// aim to apply.
//
// Extracted from the two handlers that need it: try order is the part of the mechanism that
// must especially not be implemented twice — two copies would diverge, and divergence would
// only show when a lagging device becomes unreachable.
func (s *Server) candidatesFor(ctx context.Context, d model.Device, wanted, groupName,
	operatorSecret string) (cands []driver.Candidate, target string, gen int,
	credID string, err error) {
	var cred, _, credErr = s.credentialForDevice(ctx, d, groupName)
	if credErr != nil {
		return nil, "", 0, "", credErr
	}
	credID = cred.ID

	// Account NAMES to try, from most to least probable. On ranges that can carry a chosen
	// name, wanted name and in-place name do not always match: between when meshcraft creates
	// the site account and when it removes the factory account, the device carries both — and
	// if apply was interrupted, it may carry only the old one. Trying only the wanted name
	// would conclude wrong password where the ACCOUNT does not exist.
	var names = accountNames(s.installedUsername(ctx, d.ID, credID), wanted)

	var usable, listErr = s.Store.SecretsForCredential(ctx, d.ID, credID)
	if listErr != nil {
		return nil, "", 0, "", listErr
	}
	// Secrets are decrypted here, in database order; formatting tries is a pure function
	// (buildCandidates) because that part must be testable without database or device.
	var gens []genSecret
	var u store.UsableSecret
	for _, u = range usable {
		var clear, openErr = s.Secrets.Open(u.SecretEnc)
		if openErr != nil {
			continue
		}
		gens = append(gens, genSecret{Why: u.Why, Secret: clear})
		if u.State == "active" && target == "" {
			target, gen = clear, u.Generation
		}
	}
	if target == "" {
		return nil, "", 0, "", errors.New("no active generation for this credential")
	}

	// What the device carries under ANOTHER credential, last resort. Retagging a device changes
	// its credential: the new one was never applied to it, and without this catch-up meshcraft
	// would only try secrets it never applied. Changing one's mind about where a device belongs
	// must not cost its access.
	var previous, prevErr = s.Store.PreviousInstalls(ctx, d.ID, credID)
	if prevErr == nil {
		var p store.UsableSecret
		for _, p = range previous {
			var clear, openErr = s.Secrets.Open(p.SecretEnc)
			if openErr != nil {
				continue
			}
			// Observed account name comes WITH the secret: it is the pair that worked,
			// dissociating them would make no sense.
			gens = append(gens, genSecret{Why: p.Why, Secret: clear, Username: p.Username})
		}
	}

	// The DISCOVERY credential that actually opened this device — an OBSERVED pair,
	// not a guess. A device adopted from discovery (or provisioned by the serial
	// soft-reset, which types this very secret on the console) carries it until the
	// first successful site-credential apply; the site generations above were never
	// pushed to it. Skipping this candidate produced a real refusal on 2026-08-02:
	// four candidates tried on a freshly adopted FS S3900, none of them the secret
	// meshcraft itself had just set.
	//
	// FIRST among generations while no apply has been observed (installedUsername
	// empty): on such a device the generations are pure intent and would burn the
	// account's try budget before the one pair known to work. As soon as an apply
	// has been observed, the site generations regain priority and this pair drops
	// to last resort.
	var origin, haveOrigin = s.discoveryOriginSecret(ctx, d)
	if haveOrigin {
		if s.installedUsername(ctx, d.ID, credID) == "" {
			gens = append([]genSecret{origin}, gens...)
		} else {
			gens = append(gens, origin)
		}
	}
	return buildCandidates(names, operatorSecret, gens), target, gen, credID, nil
}

// discoveryOriginSecret resolves the discovery credential that opened the device
// when it was probed, with its account name — the pair typed by the serial
// soft-reset and verified by the adoption probe. Best-effort: any missing link
// (manual device, deleted credential, unreadable secret) simply yields nothing.
func (s *Server) discoveryOriginSecret(ctx context.Context, d model.Device) (genSecret, bool) {
	var discoveredID = driver.ExtraString(d.Extra, "discovered_id")
	if discoveredID == "" {
		return genSecret{}, false
	}
	var cand, err = s.Store.GetDiscovered(ctx, discoveredID)
	if err != nil || cand.CredentialID == nil || *cand.CredentialID == "" {
		return genSecret{}, false
	}
	var creds, listErr = s.discoveryCredentials(ctx)
	if listErr != nil {
		return genSecret{}, false
	}
	var c discovery.Credential
	for _, c = range creds {
		if c.ID == *cand.CredentialID && c.Username != "" && c.Secret != "" {
			return genSecret{
				Why:      "discovery credential that opened the device (« " + c.Name + " »)",
				Secret:   c.Secret,
				Username: c.Username,
			}, true
		}
	}
	return genSecret{}, false
}

// genSecret is an already decrypted generation, with the reason for its rank.
//
// Username is set only for a secret OBSERVED under another credential: account name and secret
// then formed a pair that actually worked, and dissociating them would mean trying a password
// on an account it never belonged to.
type genSecret struct {
	Why      string
	Secret   string
	Username string
}

// triesPerAccount caps connection tries ON ONE ACCOUNT.
//
// "Two tries per account, immediate stop beyond" — docs/03-Security/Service identity and
// rotation §5, echoed in §4 "so as not to trigger AAA lockouts".
//
// THE REASON IS NOT THE SAME ON ALL THREE PLATFORMS, and saying so avoids treating this cap as
// abstract caution one could raise:
//
//   - on Instant On, it is ACCOUNT LOCKOUT observed in firmware (`accountUnlock` in
//     `AdminUserSetting`, with dedicated unlock in its interface). Exhausting tries on a range
//     without out-of-band fallback closes the device's only door, permanently;
//   - on SwOS and RouterOS, no lockout has been observed — but "to our knowledge" does not
//     justify spending what is not ours to spend, and the budget is justified by SESSION COST:
//     each candidate costs TWO HTTP requests (Digest or Basic challenge, then authenticated
//     request), because shared transport does not assume an authentication scheme. Three
//     candidates make six requests on an access-switch CPU.
//
// The cap really bites: from the third generation, trying everything known on all account names
// would far exceed the limit.
const triesPerAccount = 2

// tryBudget counts tries already planned, per account and case-insensitive — the device does
// not distinguish "Admin" from "admin".
type tryBudget map[string]int

func (b tryBudget) take(name string) bool {
	var key = strings.ToLower(name)
	if b[key] >= triesPerAccount {
		return false
	}
	b[key]++
	return true
}

// buildCandidates orders tries and applies the cap.
//
// Order follows §15: operator password, then generations as the database classifies them
// (observed, being retired, active), then factory account LAST — placing it first would fail
// the entire fleet already under management.
//
// A slot is RESERVED in advance for the factory account. Without that reserve, a never-
// provisioned device would consume its two tries on generations it does not carry, and become
// unadoptable when hitting without a password would suffice.
func buildCandidates(names []string, operatorSecret string, gens []genSecret) []driver.Candidate {
	var cands []driver.Candidate
	var tries = tryBudget{strings.ToLower(factoryAccount): 1}

	var n string
	if operatorSecret != "" {
		for _, n = range names {
			if tries.take(n) {
				cands = append(cands, driver.Candidate{
					Username: n, Secret: operatorSecret,
					Why: "current password supplied by the operator, account \"" + n + "\"",
				})
			}
		}
	}

	// Generations outside, names inside: each account thus receives the MOST PROBABLE
	// generations, not the first two of a single list.
	var g genSecret
	for _, g = range gens {
		// A secret that comes with its account is tried only on that one.
		if g.Username != "" {
			if tries.take(g.Username) {
				cands = append(cands, driver.Candidate{
					Username: g.Username, Secret: g.Secret,
					Why: g.Why + ", account \"" + g.Username + "\"",
				})
			}
			continue
		}
		for _, n = range names {
			if tries.take(n) {
				cands = append(cands, driver.Candidate{
					Username: n, Secret: g.Secret,
					Why: g.Why + ", account \"" + n + "\"",
				})
			}
		}
	}

	return append(cands, driver.Candidate{
		Username: factoryAccount, Secret: "", Why: "factory account, no password",
	})
}

// factoryAccount is the account all THREE shipped platforms carry out of the box — not only
// Instant On, as this comment once claimed.
//
// Observed on each, not an industry generalization:
//
//   - Instant On 1930: "admin user, NO password (set on first access)" after factory reset
//     (cf. its ResetProcedure);
//   - SwOS: "admin" account, empty password, web interface on port 80, and that is all — this
//     platform has NO notion of account, `admin` being simply what its Digest presents;
//   - RouterOS: factory account is `admin`, no password on affected versions.
//
// That all three converge is fortunate but does not follow: [driver.AdminAccount.EffectiveUsername]
// decides the ACTUALLY placeable name, and this constant serves only the LAST candidate in the
// try list — the one kept for a never-provisioned device.
const factoryAccount = "admin"

// credentialForDevice resolves the credential that applies to a device, and reports any
// ambiguity.
//
// Order — tag, then group, then site default — lives in the store (CredentialsForDevice), in
// one place. What is decided HERE is what to do about ambiguity: the second returned value
// lists competing tags, for the caller to tell the operator.
func (s *Server) credentialForDevice(ctx context.Context, d model.Device,
	groupName string) (model.SiteCredential, []string, error) {
	var matches, err = s.Store.CredentialsForDevice(ctx, d.ID, d.SiteID, groupName, "admin")
	if err != nil {
		return model.SiteCredential{}, nil, err
	}
	if len(matches) == 0 {
		return model.SiteCredential{}, nil, errors.New(
			"no administration credential applies to this device")
	}
	var winner = matches[0]

	// Ambiguity: several TAG credentials at the same priority. TWO credentials of different
	// specificity are not ambiguous — that is what the hierarchy is for.
	var ambiguous []string
	if winner.Tag != "" {
		var i int
		for i = 1; i < len(matches); i++ {
			if matches[i].Tag == "" || matches[i].Priority != winner.Priority {
				break
			}
			ambiguous = append(ambiguous, matches[i].Tag)
		}
		if len(ambiguous) > 0 {
			ambiguous = append([]string{winner.Tag}, ambiguous...)
		}
	}
	return winner, ambiguous, nil
}

// accountNames orders account names to try, without duplicate: OBSERVED name on the device
// first — most likely to respond —, then wanted name, then factory account.
func accountNames(installed, wanted string) []string {
	var out []string
	var candidate string
	for _, candidate = range []string{installed, wanted, factoryAccount} {
		if candidate == "" {
			continue
		}
		var seen bool
		var already string
		for _, already = range out {
			if strings.EqualFold(already, candidate) {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, candidate)
		}
	}
	return out
}

// installedUsername: observed account name, or empty. A silent database is not an error — it
// is the case for every never-provisioned device.
func (s *Server) installedUsername(ctx context.Context, deviceID, credID string) string {
	var name, err = s.Store.InstalledUsername(ctx, deviceID, credID)
	if err != nil {
		return ""
	}
	return name
}

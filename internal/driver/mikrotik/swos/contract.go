package swos

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"meshcraft/internal/driver"
)

// This connector's adoption of the access contract
// (internal/driver/connector.go).
//
// This file holds NO platform knowledge: everything that knows how to talk to
// a SwOS lives in admin.go, state.go, apply.go, snmp.go, inspect.go and
// pages.go. What it carries is the translation between what the API layer
// asks for and what this platform can do — and, just as important, the
// written list of what it cannot do.
//
// # WHY THIS CONNECTOR HOLDS THE CONTRACT WITHOUT FAKING ANYTHING
//
// The contract was written partly AGAINST this platform, and that is what
// makes it tenable here. Two decisions come straight from SwOS:
//
//   - [driver.Connector.Authenticate] is ONE call, not an `Open` followed by
//     a `Login`. SwOS has NO session: authentication is an
//     `Authorization: Digest` header presented again on every request,
//     nothing survives from one request to the next, there is nothing to
//     open and nothing to close. A two-step contract would have forced this
//     connector to write a `Login` that does nothing — or that "verifies" by
//     hitting a page without the caller knowing that is what happens. Here
//     the verification is explicit and owned: Authenticate READS /sys.b, and
//     that read is what allows saying "verified";
//   - [driver.ApplyResult.Backup] exists for this platform and for it alone.
//     SwOS applies and persists in a single gesture (`Commit: immediate`): a
//     power cut undoes nothing, and the ONLY way back is the configuration
//     copy taken right before writing. ApplyPorts takes it; it cannot be
//     rebuilt afterwards.
//
// # TWO CONTRACT METHODS ARE NOT IN THIS FILE
//
// [driver.Admin.ReadState] lives in state.go and [driver.Admin.ApplyPorts] in
// apply.go, because they are not translations: they ARE the implementation,
// and separating them from the reasoning that justifies them — SwOS's
// tagging rule for one, the uplink guard for the other — would mean one of
// the two changing some day without the other.

// --- The contract types, ALIASED and not redefined ---
//
// An ALIAS (`=`) and not a definition: the fields, the pure functions and the
// sixty-eight tests of this package keep compiling as they are, and what this
// connector handles IS the type the API layer consumes. No conversion, hence
// no field silently lost the day the contract gains one — which is exactly
// what the header of internal/driver/state.go holds against the three copies
// that existed before it.
//
// TrapDestination and TrapResult are aliased even though this connector does
// NOT implement [driver.TrapPusher] (see below why). That is deliberate and
// costs nothing: the day someone writes a function here that talks about
// traps — if only to explain a refusal — they will use the contract's shape
// instead of inventing a third one.
type (
	Step            = driver.Step
	VLAN            = driver.VLAN
	PortState       = driver.PortState
	State           = driver.State
	ApplyResult     = driver.ApplyResult
	Candidate       = driver.Candidate
	SNMPIdentity    = driver.SNMPIdentity
	SNMPResult      = driver.SNMPResult
	TrapDestination = driver.TrapDestination
	TrapResult      = driver.TrapResult
)

// Identity, on the other hand, is NOT aliased, and it is the contract itself
// that asks for it: "Platform-specific fields do not belong here — boot loader
// version, board revision, index of the first optical cage. Connectors keep
// their own, richer identity type, and fill this one in for the API layer."
//
// This is not a style preference, it is a compilation constraint: this
// package's type carries five fields the contract does not have (`Revision`,
// `BoardName`, `SFPFirst`, `MgmtIP`, `MgmtVLAN`) and above all a METHOD,
// `PortGroups()`, that discovery already calls (internal/discovery/probe.go).
// Go forbids attaching a method to a type from another package: aliasing
// `Identity` would therefore have broken discovery, which is outside the
// scope of this batch. The translation is done in [Identity.Contract]
// (inspect.go), in a single place and saying what it loses.

// --- The CONFORMITY assertions ---
//
// They serve no purpose at run time, and that is the point: they make
// COMPILATION fail the day the contract moves. The alternative — a type
// assertion made by the caller at write time — fails silently on a device in
// production, i.e. at the worst place and the worst moment. A missing
// optional capability must be read BEFORE the first packet; a capability
// believed present that has stopped being so must be read when the binary is
// built.
//
// One line per interface ACTUALLY implemented, and none for the others: this
// list is therefore also the answer to "what can this connector do?".
var (
	_ driver.Connector  = (*Driver)(nil)
	_ driver.Identifier = (*Driver)(nil)

	_ driver.Admin            = (*Admin)(nil)
	_ driver.RawReader        = (*Admin)(nil)
	_ driver.SNMPConfigurator = (*Admin)(nil)
)

// --- WHAT THIS CONNECTOR DOES NOT IMPLEMENT, AND WHY ---
//
// These absences are a DELIVERABLE, not a to-do list. Each one answers the
// only question that matters to an operator — "is there anything to wait
// for?" — and the answer is no for all five. The mechanism the contract chose
// (an optional interface, tested by type assertion) is what lets the UI
// refuse BEFORE reaching the device, naming the gesture and the reason.
//
// [driver.AccountManager] — THE PLATFORM HAS NO NOTION OF AN ACCOUNT.
//
// Not "not yet": not at all. Three facts establish it, and they are
// observations, not cautious inferences — the password change form of the
// embedded UI carries only THREE fields (old, new, confirmation); the page
// receiving it holds a single key, `{pwd:''}`; and the device's ENTIRE
// configuration backup (testdata/backup.swb, surveyed on the reference
// CSS326-24G-2S+) mentions NO user name. There is literally nothing to name,
// hence nothing to create, rename or delete.
//
// Consequence for the caller: the fleet's unified account name cannot be set
// on a CSS. [driver.AdminAccount.EffectiveUsername] resolves it to "admin"
// BEFORE any attempt, and `Capabilities().Admin.Custom = false` announces it.
// The absence of this interface is the SECOND lock, not the first: if the
// caller ends up wanting to create an account here, the capability was
// ignored upstream.
//
// [driver.TrapPusher] — /snmp.b HAS NO RECEIVER FIELD.
//
// Four fields in all: enable, community, contact, location. No receiver, in
// any tab, nor in the configuration backup. And this is not a hidden page
// like the ones that existed on the Instant On range: the COMPLETE web UI of
// a SwOS fits in a single 44,807-byte file that defines all its tabs and all
// their fields — enumeration exhausts the question. A CSS is therefore
// monitored by POLLING: the information is there, what is missing is
// reactivity. Declaring this interface would write a line into a device that
// would never use it, and would open 162/udp on the proxy to listen to
// silence — the exact opposite of a service rendered. Consistent with
// `Capabilities().Monitoring.Traps = false`, which is the same information
// seen from the other end.
//
// [driver.Persister] — `Commit: immediate`: THERE IS NOTHING TO SAVE.
//
// The embedded UI's only buttons are "Apply All", which posts the page, and
// "Discard Changes", which merely RE-READS the page from the device — a
// purely local undo, which undoes nothing that has gone out. No separate
// startup configuration, no commit to confirm. Not implementing this
// interface is therefore EXACT information, and it has a hard corollary:
// there is no SAFETY NET either. Where Instant On offers a free way back (do
// not save, cut the power), here the only way back is
// [driver.ApplyResult.Backup].
//
// [driver.MgmtMover] — "ALLOW FROM VLAN" ACCEPTS ONLY ONE VLAN.
//
// That contract requires being able to ADD an administration path, observe
// that it answers, then switch over: three verifiable steps. SwOS carries a
// single management address and a single `avln` (/sys.b): the cutover is a
// JUMP, with no period where both paths coexist. [driver.MgmtJumper] is
// therefore what this connector implements (JumpMgmt + backup), never
// MgmtMover.
//
// [driver.Onboarder] — NOTHING TO SWITCH OVER: a SwOS is manageable out of
// the box.
//
// That interface exists for a factory-fresh Instant On, which waits for the
// vendor's cloud portal and has to be talked out of it. SwOS has no
// equivalent: account "admin", empty password, web UI on port 80, and that is
// all.

// --- The connector ---

// Authenticate returns a VERIFIED administration access to a SwOS switch.
//
// THERE IS NO SESSION TO OPEN HERE, and that is the fact to take in before
// everything else. SwOS authenticates via an `Authorization: Digest` header
// (MD5, qop=auth) presented again on EVERY request: nothing survives from one
// call to the next, no cookie, no URL prefix, no client counter. What this
// method returns is therefore not a connection but a holder of credentials
// that have been OBSERVED to work — hence the name `Admin` ("verified") and
// the absence of any `Close` in the contract.
//
// That is precisely what a single-call contract lets us express without
// pretending. An `Open()` then `Login()` would have forced this connector to
// write an empty `Login`, or to hit a page in secret: the caller would then
// have believed it opened a session where there is none, and would have ended
// up releasing one — a pointless gesture here, and a false promise about a
// scarce resource elsewhere.
//
// HOW "VERIFIED" IS OBTAINED: by a read of /sys.b. It is the cheapest page of
// the platform (780 bytes) and the only one whose content proves at once that
// the credentials work AND that we are talking to a SwOS — the parser refuses
// whatever is not a `*.b` page, which catches this platform's trap: an
// unknown path returns the ENTIRE web UI as HTTP 200.
//
// WHAT IT COSTS THE DEVICE: TWO HTTP requests per candidate tried. Digest is
// renegotiated every time — a 401 carrying the challenge, then the
// authenticated request — and the shared transport does not keep the nonce
// from one request to the next. An `Authenticate` that succeeds at the first
// try therefore costs two requests; with three candidates of which the last
// one passes, six. None of them writes anything.
//
// CANDIDATES ARE TRIED IN RECEIVED ORDER, AND NONE IS REPLAYED. The order
// comes from the caller (observed generation first, factory account last)
// and this connector does not reorder it, does not invent any "just to see",
// and does not retry a candidate that failed. The number of attempts is a
// BUDGET: on a device with neither SSH nor a serial port, exhausting the
// attempts amounts to closing the only door. SwOS has, to our knowledge, no
// account lockout — but "to our knowledge" does not justify spending what
// does not belong to us.
//
// A FAILURE RETURNS NO Admin, deliberately: an unverified access travelling
// behind an error would end up being used to write. The transcript of the
// attempts therefore goes into the error MESSAGE, which distinguishes the two
// outcomes the operator must be able to tell apart — "no credential
// accepted" (the device answers, it refuses) and "the device does not answer"
// (nothing was tried beyond that).
func (d *Driver) Authenticate(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Admin, error) {
	if tgt.Host == "" {
		return nil, errors.New("no address to reach: this connector does not guess " +
			"how a device is reached")
	}
	// No candidate: the device is not touched. Trying "admin / empty
	// password" because it is the factory state would be inventing a
	// candidate nobody asked for — exactly what the contract forbids.
	if len(cands) == 0 {
		return nil, errors.New("no credential to try: SwOS has a single account, " +
			"with no name, and this connector does not invent one — not even the factory " +
			"pair (\"admin\", empty password), which must be offered as a candidate to " +
			"be tried")
	}

	var refusals []string
	var i int
	for i = 0; i < len(cands); i++ {
		var cand = cands[i]
		var a = OpenAdmin(tgt.Host, cand.Username, cand.Secret, tgt.Dialer)
		a.prefix = tgt.PortPrefix
		a.accepted = cand
		// The secret NEVER goes into the transcript: the transcript is made to
		// be shown, kept and attached to the pipeline. The account and the
		// REASON for the candidate's position are enough to understand
		// afterwards what opened the device.
		a.step(true, "trying credentials \""+a.User+"\"", cand.Why)

		var _, err = a.Get(ctx, PageSys)
		if err == nil {
			a.step(true, "connected as \""+a.User+"\"",
				"credentials verified by a read of "+PageSys+" — "+cand.Why+
					". No session was opened: SwOS presents the Digest again on every "+
					"request, there is nothing to close")
			return a, nil
		}
		if !errors.Is(err, errCredentialsRefused) {
			// The device does not answer, or does not answer what a SwOS
			// answers. Insisting with the next candidates would prove nothing
			// and would muddle the diagnosis: this is not a credentials problem.
			return nil, fmt.Errorf("device %s does not answer like a SwOS switch, "+
				"the credentials have nothing to do with it: %w — %s", tgt.Host, err,
				strings.Join(transcriptLines(a.Steps), " | "))
		}
		refusals = append(refusals, fmt.Sprintf("\"%s\" (%s): refused", cand.Username, cand.Why))
	}
	return nil, fmt.Errorf("none of the %d credentials offered was accepted by %s "+
		"(the device answers, it refuses): %s. SwOS has a single account, with no name, "+
		"and its secret cannot be recovered: losing it forces a factory reset on "+
		"site, this model having neither SSH, nor Telnet, nor a serial port",
		len(cands), tgt.Host, strings.Join(refusals, "; "))
}

// WritesExtra says whether this connector sets the named intent, beyond VLAN
// membership.
//
// On the Driver and not on the Admin, as the contract requires: the answer
// depends neither on a model nor on an access, and the caller needs it BEFORE
// reaching the device — what the platform cannot write must be announced even
// when the write fails.
//
// The declaration itself is in capabilities.go, next to the (non-)code that
// sets: it is EMPTY, and its comment says what is instructive — the reason is
// not that SwOS lacks these functions (it has three out of four), it is that
// none could be verified by a write on hardware, and that each lives in a page
// that also carries the fields able to cut access.
func (d *Driver) WritesExtra(name string) bool { return WritesExtra(name) }

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Admin)(nil))
}

// Identify says which device answers at an address.
//
// THE GOOD SURPRISE OF THIS PLATFORM, and the reason this optional interface
// is implemented here: the realm of the Digest challenge carries the EXACT
// MODEL REFERENCE, and it is readable WITHOUT ANY CREDENTIALS AT ALL.
//
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth", nonce="4753ad9d"
//
// Hence this method's order, which is not negotiable: the model is acquired
// and RETURNED first, the error only comes afterwards. "It is a
// CSS326-24G-2S+ whose password we do not have" is a useful result — it gives
// the capabilities, hence the faceplate geometry, hence what is needed to
// file the device in the inventory; "failure" is not. Returning one for the
// other would make discovery lose the only thing this range offers for free.
//
// What the realm does NOT give and requires getting in: the serial (the only
// field authoritative for recognising a device at another address), the
// firmware, the system name, the MAC and the real geometry. Hence the
// candidates, tried IN ORDER and never replayed, exactly as in
// [Driver.Authenticate].
func (d *Driver) Identify(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Identity, error) {
	var model, err = realmModel(ctx, tgt.Host, tgt.Dialer)
	if err != nil {
		return driver.Identity{}, err
	}
	// What is acquired at the door, before any attempt.
	var partial = Identity{
		Model:  model,
		Family: FamilyOf(model),
		PoE:    ModelHasPoE(model),
		Source: "digest_realm",
	}
	if len(cands) == 0 {
		return partial.Contract(), fmt.Errorf("model identified without credentials (%s, "+
			"read from the realm of the Digest challenge); the serial, the firmware and the "+
			"faceplate require an authenticated read, and no credential was offered", model)
	}

	var lastErr error
	var i int
	for i = 0; i < len(cands); i++ {
		var admin = OpenAdmin(tgt.Host, cands[i].Username, cands[i].Secret, tgt.Dialer)
		// One copy per attempt: a fill half done by a refused candidate must
		// not mix with the next one's.
		var full = partial
		var fillErr = admin.fillIdentity(ctx, &full)
		if fillErr == nil {
			return full.Contract(), nil
		}
		if !errors.Is(fillErr, errCredentialsRefused) {
			// We GOT IN — the credentials work — but a page was missing. What
			// was read is richer than the realm: `full` is returned, not
			// `partial`.
			return full.Contract(), fmt.Errorf("model identified (%s) but the read "+
				"is incomplete: %w", model, fillErr)
		}
		lastErr = fillErr
	}
	return partial.Contract(), fmt.Errorf("model identified (%s) but none of the %d "+
		"credentials offered was accepted: the serial and the firmware remain "+
		"unknown. Last refusal: %w", model, len(cands), lastErr)
}

// --- Administration access ---

// Connector returns the identity of the connector that produced this access.
//
// It serves the MESSAGES, and that is enough to justify it: "this platform
// cannot declare a trap receiver" helps nobody if the UI does not say which
// platform it is talking about. It also spares the caller from carrying the
// connector name alongside the access — two variables that would end up
// designating two different devices.
func (a *Admin) Connector() driver.Info { return (&Driver{}).Info() }

// Account is the account that ACTUALLY opened the access.
//
// On SwOS it is always the same one and it has no name: the platform has no
// notion of an account (see the block of absences at the top of this file),
// and [FixedAdminName] is what the Digest presents. The field exists all the
// same so the caller does not have to tell this platform apart from the
// others — and it carries what was presented, not what was wished for.
func (a *Admin) Account() string { return a.User }

// Accepted is the candidate that worked, as it was supplied.
//
// Returned by the contract because the caller needs it, and on this platform
// more than elsewhere: changing the password REQUIRES knowing the previous
// one, which is the RC4 key of the expected digest (cf. [PasswordBlob]).
// Reconstructing it by re-reading the transcript — which the API layer used
// to do — amounted to making a credential write depend on the label of a
// sentence written for a human.
//
// Empty on an Admin obtained other than through [Driver.Authenticate] (through
// [OpenAdmin], hence through discovery): no candidate was presented there.
func (a *Admin) Accepted() Candidate { return a.accepted }

// Transcript is the trace of everything this access did, in order.
//
// Named so by the contract, and not `Steps`, for a prosaic reason that holds
// for all three connectors: they all expose a `Steps` FIELD, and a Go type
// cannot carry a field and a method of the same name. This name lets the
// contract be adopted by a simple addition, without renaming a field the
// package's tests read.
//
// On a platform whose configuration is persistent by default, with no safety
// net, and which has neither SSH nor a serial port, this is not a developer
// convenience: it is the only way to understand a failure without going back
// on site.
func (a *Admin) Transcript() []Step { return a.Steps }

// SetSecret changes the password of the account that just got in.
//
// THIS PLATFORM IS WHAT JUSTIFIES THE CONTRACT'S `oldSecret` PARAMETER, and it
// must be said here so nobody takes it for a decorative precaution: on SwOS,
// the old secret is not a confirmation, it is the RC4 KEY of the digest the
// device expects (cf. [PasswordBlob], observed to the letter in the embedded
// UI's JavaScript). Without it, there is nothing to send. And when it is
// wrong, the device answers HTTP 405 — the only usable error message on the
// whole platform.
//
// Instant On, for its part, ignores it: its open session is authoritative.
// The contract therefore passes the parameter in both cases rather than
// letting the caller guess which of the two worlds it is facing — that is
// exactly the kind of difference this contract exists to absorb.
//
// THE WRITE PERSISTS, with nothing more to do: `Commit: immediate`. That is
// not a convenience but a risk to be aware of — the NEXT request must present
// the new secret (which [Admin.SetPassword] does), and a reboot will not
// bring back the old one.
func (a *Admin) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	return a.SetPassword(ctx, oldSecret, newSecret)
}

// Close: a no-op on this range. The access is an authenticated HTTP client,
// not a session the device counts against a limit — there is nothing here to
// release that an SSH connector's Close() does release.
func (a *Admin) Close() {}

// --- Raw read: surveying an unknown firmware ---

// rawUnit recognises a SwOS page name.
//
// Deliberately NARROW — a `*.b` page, possibly prefixed with `!` (the pages
// the UI never rewrites: /!dhost.b, /!igmp.b, /!pwd.b), or the `.swb` backup
// — for two reasons of a different nature:
//
//   - this name goes into a URL. An injection would divert the request to
//     another endpoint of the device, and this platform has some that ACT
//     (/reset, /reboot). They only answer POST, which is a guard of the
//     firmware and not ours: relying on it would make our safety depend on a
//     version detail;
//   - what is not a page does not return 404 on SwOS. Refusing upstream
//     whatever does not look like a page avoids paying a round trip to
//     receive 44,807 bytes of web UI.
var rawUnit = regexp.MustCompile(`^!?[a-z0-9_]{1,24}\.(b|swb)$`)

// ReadRaw returns the raw content of a page, as the device serves it.
//
// READ ONLY, and it is a SURVEY tool, not a product feature: these
// interfaces are documented nowhere, and a page can only be written once its
// real field set has been seen.
//
// THIS METHOD'S GUARD IS THE MOST PRECIOUS IN THE PACKAGE, and it is kept as
// is (cf. [Admin.raw]): on SwOS, an unknown path does not return 404 — it
// returns the ENTIRE WEB UI, as HTTP 200 and `text/html` (44,807 bytes on the
// reference model; verified again live on /lldp.b, which serves /index.html).
// Without this check, a typo in a page name would present itself as a valid
// response, and the error message would blame the page's syntax where the
// real fault is the path. It is the difference between "this page does not
// exist on this firmware" and an hour spent debugging a parser that works
// fine.
func (a *Admin) ReadRaw(ctx context.Context, unit string) (string, error) {
	var clean = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(unit), "/"))
	if !rawUnit.MatchString(clean) {
		return "", fmt.Errorf("\"%s\" is not a SwOS page name: expected something "+
			"like \"sys.b\", \"vlan.b\", \"!dhost.b\" or \"backup.swb\". This name "+
			"goes into a URL, and this platform has paths that ACT: it is "+
			"refused before being sent, not cleaned up", unit)
	}
	return a.raw(ctx, "/"+clean)
}

// RawKind names, for a human, what "a unit" means on this platform.
//
// It serves the UI, and avoids the worst misunderstanding of the debugging
// probe: asking for a `wcd` section name from someone looking at a MikroTik,
// then presenting the failure as a device fault.
func (a *Admin) RawKind() string { return "page *.b" }

// --- SNMP ---

// ApplySNMP sets the SNMP identity on the device.
//
// WRITTEN BEFORE IT HAD A USE, and it now has one. The split of roles the
// contract describes has not changed: the CAPABILITY decides whether to set,
// the INTERFACE executes. What changed is the capability. `SNMPVersions` was
// EMPTY, and that emptiness ruled the device out upstream — `snmpprov.For`
// produces no monitored plan without a declared version — so this method was
// never called.
//
// The only thing this platform lacked was A FACT: knowing which version its
// agent answers. /snmp.b proved that an agent exists, that it is enabled and
// that it carries a community, without ever being able to say whether it
// serves v1, v2c or both. The walk of 29 July 2026 on 192.0.2.36 answered,
// `SNMPVersions` is `["v2c"]` (swos.go), and the list did indeed gain ONE
// LINE with nothing to write here — which is exactly what this ordering aimed
// for. The reverse (raising the list, then discovering the write has to be
// coded) would have announced monitoring nobody knew how to put into
// service.
//
// What is not claimed: that a write has been exercised on the reference
// hardware. It has not. The code is verified on the real capture of /snmp.b,
// not on a write round trip.
//
// WHAT IS REFUSED IS REFUSED, NEVER APPROXIMATED TO THE NEAREST. There is NO
// SNMPv3 on this platform — no user, no authentication algorithm, no
// encryption, no field, in any tab or in the complete configuration backup.
// Setting a v2c community where the caller wanted a v3 user would put a
// cleartext secret on the network that nobody chose, and would display a
// false compliance.
func (a *Admin) ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error) {
	var out SNMPResult

	if want.User != "" {
		out.Skipped = append(out.Skipped, "SNMPv3 user \""+want.User+"\" NOT applied: "+
			"there is no SNMPv3 on this platform — no user, no authentication "+
			"algorithm, no encryption, in any tab of its UI or in "+
			"its complete configuration backup. No version of meshcraft will "+
			"change that: it is the firmware that lacks the feature.")
	}
	if len(want.Sources) > 0 {
		// The contract warns that a connector whose platform lacks this setting
		// IGNORES this field. We ignore it, but we SAY so: an operator who
		// restricted the sources in the plan must know this device did not
		// apply it.
		out.Skipped = append(out.Skipped, "restriction of polling sources NOT "+
			"applied: /snmp.b has no address field. What restricts access to the agent on "+
			"this platform is the administration group of /sys.b (\"alla\", \"allm\", "+
			"\"avln\"), which cuts WEB access at the same time as SNMP and which this "+
			"connector does not write.")
	}
	if want.Community == "" {
		// Nothing to set: say so with an error, not with an empty success. An
		// apply that returns "all good" without having written anything
		// suggests monitoring is in place, and it is the worst of both worlds
		// — nothing answers, and nothing says why.
		var why = "no community to set: SwOS has ONLY ONE community for the whole " +
			"SNMP configuration (/snmp.b \"com\") and no v3 — without it, there is nothing " +
			"to write on this device"
		if len(out.Skipped) > 0 {
			why += ". " + strings.Join(out.Skipped, " ")
		}
		return out, errors.New(why)
	}

	// Idempotence: an already-qualified fleet must rewrite NOTHING. On a
	// platform where writing is persistent by default with no way back, a
	// POST that changes nothing is a risk taken for nothing — and it would be
	// taken on every pass.
	var cfg, err = a.ReadSNMP(ctx)
	if err != nil {
		return out, err
	}
	if cfg.Enabled && cfg.Community == want.Community {
		a.step(true, "SNMP community already conforming",
			"the agent is enabled and already carries the expected community: no write")
		out.Enabled = true
		return out, nil
	}

	if err = a.SetCommunity(ctx, want.Community); err != nil {
		return out, err
	}

	// MANDATORY READ-BACK, and it is this platform's failure mode that makes
	// it necessary: a SwOS refusal is SILENT — 200, empty body, nothing
	// changed. Its own UI reads nothing but the HTTP code anyway. The only
	// proof a write took is therefore to read back, and that is what
	// `CommunitySet` claims ("a write happened AND was verified").
	var after, reErr = a.ReadSNMP(ctx)
	if reErr != nil {
		return out, fmt.Errorf("community sent but read-back impossible, so the write "+
			"is not verified: %w", reErr)
	}
	if after.Community != want.Community || !after.Enabled {
		a.step(false, "SNMP write without effect",
			"the device answered HTTP 200 but the read-back does not show the "+
				"applied configuration: on SwOS a refusal is SILENT, and this is that case")
		return out, errors.New("the device accepted the request (HTTP 200) but the " +
			"read-back of /snmp.b does not show the applied community: the write did not " +
			"take, and this platform does not say why")
	}
	a.step(true, "SNMP community verified by read-back",
		"the agent is enabled and carries the expected community")
	out.Enabled, out.CommunitySet = true, true
	return out, nil
}

// transcriptLines flattens a transcript to slip it into an error message.
//
// It exists for the only case where the transcript CANNOT be returned by
// [Admin.Transcript]: a [Driver.Authenticate] that fails returns no Admin,
// hence no object on which the caller could read the steps. Losing them would
// amount to presenting "credentials refused" without saying what was tried.
func transcriptLines(steps []Step) []string {
	var out = make([]string, 0, len(steps))
	var s Step
	for _, s = range steps {
		var line = s.What
		if s.Detail != "" {
			line += " (" + s.Detail + ")"
		}
		out = append(out, line)
	}
	return out
}

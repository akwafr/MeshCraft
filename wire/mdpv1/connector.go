package mdpv1

import (
	"context"
)

// The device-ACCESS contract: what the API layer calls to read, write, and set an
// identity, without ever knowing which platform it is talking to.
//
// # Why this file exists
//
// driver.go describes what a platform KNOWS HOW TO DO (capabilities, fingerprint,
// reset procedure). That is KNOWLEDGE, and it was enough as long as a single
// connector existed: the API layer called `instanton.OpenAdmin` directly, and the
// knowledge only served to decide what to ask for. The day a second connector
// arrived, that shortcut turned into an OBSERVED operational fault, not a
// hypothetical one: two MikroTik switches adopted on a site triggered an SNMP push
// that spoke the Instant On protocol to a MikroTik — `https://192.0.2.54/`,
// connection refused (that device does not listen on 443), `snmp.apply_failed` at
// severity 3. Nothing was written because the TCP connection failed. The write
// itself, though, had genuinely been sent.
//
// This file is what makes that fault impossible: there is no longer any path by
// which the API layer can reach a device without going through the connector that
// discovery designated.
//
// # The contract starts from the CALLER'S NEED, not the historical implementation
//
// What is described here is what the API layer actually consumes: open an
// administration access with an ordered list of secrets, re-read the state, set VLAN
// membership, set an administration secret, set an SNMP identity, set a trap
// destination, and return a transcript. Nothing more.
//
// It DELIBERATELY does not copy the surface of `instanton`: that one describes a
// web interface replayed like a browser (`wcd` sections, `SaveConfig`,
// `RawSection`), and a contract modeled on it would condemn RouterOS to mimic a UI
// it doesn't have. The rule applied everywhere below: name the expected RESULT,
// never the gesture by which a given platform obtains it.
//
// # The three platforms do not have the same NATURE, and the contract carries that
//
// This is the main trap, and it is already documented at the top of
// internal/driver/mikrotik/swos/admin.go: "There is NO session, and that is the
// first fact to internalize." The three authenticate in three different ways:
//
//   - Instant On: a form (actually an RSA-encrypted GET), state kept in a cookie
//     and in a URL prefix `/csNNNNNNNN/`. There is a real session, and the device
//     only accepts FIVE of them;
//   - SwOS: `Authorization: Digest` re-presented on EVERY request. No state,
//     nothing to open, nothing to close;
//   - RouterOS: `Authorization: Basic` re-presented on every REST API request.
//     Same thing.
//
// A two-step contract — `Open()` then `Login()` — would force two connectors out of
// three to fake it: a `Login` that does nothing, or that "verifies" by hitting some
// random page without the caller knowing that's what actually happened. Hence
// [Connector.Authenticate], which is a SINGLE call: "give me an administration
// access that has already been proven, or tell me why you can't." What "proven"
// means is up to the connector — an open session here, an authenticated read
// there — and the caller does not have to choose on its behalf.
//
// Symmetrically, there is NO `Close`. Three reasons, in order of strength: SwOS and
// RouterOS have nothing to close; the Instant On survey states that "no
// disconnection endpoint was identified in the scripts served"; and a `Close`
// method would suggest that calling `defer close()` frees one of that range's five
// sessions, which is false. A false promise about a scarce resource is worse than no
// promise at all.
//
// # Where the line falls between MANDATORY and OPTIONAL
//
// Two mechanisms coexist in this package to say "I don't know how to do that," and
// they don't serve the same case. The rule is mechanical:
//
//   - a capability that every switching platform HAS, but that a connector hasn't
//     coded yet → MANDATORY contract method, which returns [ErrNotImplemented].
//     Every switch has ports, VLANs, and an administration password: the absence is
//     a gap on OUR side, it gets closed by a release, and the caller has nothing
//     else to decide besides displaying the failure;
//   - a capability that a platform can structurally NOT HAVE → separate OPTIONAL
//     interface, tested by type assertion. SwOS has no named account, no trap
//     receiver, no syslog, no two-step save: these are not delays, they are facts
//     about the platform.
//
// Why this line and not another: a type assertion happens BEFORE the gesture
// begins. The caller can therefore refuse cleanly, explain why, and avoid offering a
// button that leads nowhere. `ErrNotImplemented`, on the other hand, is only
// discovered by trying — acceptable for a temporary gap, unacceptable for a
// permanent absence that a screen must announce. This is the direct application of
// the project rule: "an unknown capability is assumed ABSENT, and an absence is
// DECLARED instead of being discovered in the field."
//
// # What this contract does not know about
//
// No SSH, no HTTP, no tunnel: a connector receives a [Dialer] whose transport is
// already established, either directly or through a [[Proxy]]. This is the "single
// code path" rule stated at the top of driver.go, and it is not negotiable — a
// connector that opened its own connection would bypass the proxy, and therefore
// the confidentiality boundary, and therefore site coverage.

// Connector is a [Driver] that knows how to REACH the device, not merely describe
// it.
//
// The separation between the two is what closes the fault reported at the top of
// this file. A `Driver` can exist without knowing how to write: that's the case for
// a range whose fingerprint and capabilities are known, which is enough to FILE it
// in the inventory. The day the API layer wants to WRITE, the type assertion on
// `Connector` is the only path — and it fails cleanly instead of silently falling
// back to the historical connector.
//
// The registry keeps returning `Driver` values (cf. [Get]): it is [ConnectorFor]
// that performs the assertion in a single place, with the matching message.
type Connector interface {
	Driver

	// Authenticate returns a PROVEN administration access on this device.
	//
	// A SINGLE CALL, and this is the most important design point of this file (cf.
	// the header, § on the nature of the platforms). The connector tries the
	// candidates IN ORDER, stops at the first one that gets in, and returns the
	// access. What "getting in" means belongs to the connector: opening a session
	// for Instant On, succeeding at an authenticated read for SwOS and RouterOS.
	//
	// THE ORDER COMES FROM THE CALLER and is never reordered. It is computed by the
	// database (cf. store.SecretsForCredential and api.buildCandidates): the
	// observed generation first, older generations next, the factory account LAST.
	// Trying the most recent secret first would fail on any device that fell
	// behind, and putting the factory account first would fail on the whole fleet
	// already taken over.
	//
	// THE NUMBER OF ATTEMPTS IS A BUDGET, not a convenience: the caller already caps
	// it at two per account (api.triesPerAccount), because these ranges LOCK
	// accounts out. A connector must therefore NEVER replay a candidate on its own
	// initiative, nor fabricate one "just to see." On a device with neither SSH nor
	// a serial port, exhausting the attempts amounts to closing the only door.
	//
	// A failure is an ORDINARY error, not a state to interpret: "no credential
	// accepted" and "the device is not responding" are distinguished by the message
	// and by the transcript, which the caller displays as-is.
	Authenticate(ctx context.Context, tgt Target, cands []Candidate) (Admin, error)

	// WritesExtra reports whether this connector sets the named intent, outside of
	// VLAN membership (cf. recon.ExtraIntents: `poe`, `stp`, `dhcp_snooping`,
	// `port_security`).
	//
	// On the Connector and not on the Admin: the answer depends neither on a
	// session nor on a model, and the caller needs it BEFORE reaching the device —
	// what the platform cannot write must be announced even when the write fails
	// (cf. api.unsupportedNotes, which reports it before the first packet).
	//
	// False for anything not explicitly set. Same rule as everywhere else: what is
	// not declared is absent, and a capability that LIES is worse than no
	// capability at all, because it gives the operator a configuration they believe
	// is in place.
	WritesExtra(name string) bool

	// Supports lists the optional gestures this connector's sessions satisfy,
	// WITHOUT opening one.
	//
	// The optional interfaces below are asserted on the session — and a
	// session is the scarce resource of this product (five on Instant On,
	// none recoverable without a paperclip). "Can this platform read PoE?"
	// must not cost an authentication to answer; it is also the question an
	// EXTERNAL driver answers over the wire, where no type assertion exists
	// at all. See [Gesture] and [GesturesOf].
	//
	// Implemented in one line from the method sets, never by hand:
	//
	//	func (d *Driver) Supports() []driver.Gesture {
	//		return driver.GesturesOf(d, (*Client)(nil))
	//	}
	//
	// where *Client is the concrete type Authenticate returns. A typed nil is
	// a static method-set check — the compiler's view of the session type IS
	// the declaration, so the list cannot drift from the code.
	Supports() []Gesture
}

// Target is the device to reach, and how.
//
// An aggregate rather than four parameters repeated across ten signatures: the day
// a non-standard port or scheme needs to be added, the connectors change once and
// the callers change zero times.
type Target struct {
	// Host is the administration address, without scheme or port. It is the
	// connector that knows how its platform is reached: `https://` on 443 for
	// Instant On, `http://` on 80 for SwOS (which listens ONLY there), `http://` for
	// RouterOS's REST API (whose 443 is closed for lack of a factory certificate).
	// The caller must NOT decide this — that is precisely the fault reported at the
	// top of this file.
	Host string
	// Dialer is the ALREADY ESTABLISHED transport: direct, or through a proxy
	// tunnel. Nil means "from this machine," which only the "Local" proxy
	// justifies.
	Dialer Dialer
	// Model is the model as the inventory knows it. Used by the connector to choose
	// between two behaviors of the same range; it never GUESSES it when the field
	// is empty.
	Model string
	// PortPrefix is the standard-model port-naming prefix for this device ("GE"),
	// as its faceplate groups carry it.
	//
	// DO NOT TREAT AS A UNIVERSAL TRUTH — the RouterOS survey is what establishes
	// that: on this platform, meshcraft's names ARE the device's names (`ether3`,
	// `sfp-sfpplus1`), so the faceplate carries SEVERAL prefixes and none of them
	// summarizes the device. A connector that names its own ports IGNORES this
	// field, and that is the correct behavior — reading it as if it were the prefix
	// for all ports would write to the wrong port, which cuts off a customer.
	//
	// Connectors that NUMBER their ports (Instant On, SwOS) need it: it's what
	// translates "GE12" into the device's index 12. When empty, they apply their
	// own default rather than writing blindly.
	PortPrefix string
}

// Candidate is a secret to try, with the reason for its position in the order.
//
// `Why` is not a comment: it goes into the transcript, and it is what lets the
// operator understand AFTERWARDS which secret opened the device ("generation 2,
// observed" versus "the factory account"). It is also what lets the caller retrieve
// the accepted secret without asking the database for it again.
type Candidate struct {
	Username string
	Secret   string
	Why      string
}

// Step is a transcript step, as it will be shown to the operator.
//
// COMMON to all three connectors, and this was already true before this contract:
// all three had copied the same structure into their own package, each writing in
// its comment "same shape as the Instant On connector's, so the API has only one
// way to present a write." Three copies of an identical type only hold up until the
// day one of them gains a field.
//
// The migration on the connector side costs nothing: `type Step = driver.Step` (an
// ALIAS, not a new definition) turns their existing `Steps []Step` field into a
// `[]driver.Step` without touching a single line of their code. That is what all
// three must do.
//
// The JSON tags are the original ones, and that is not a minor detail: these steps
// are serialized in the HTTP response of every write and read by the frontend.
// Changing them would break the transcript display without any Go test noticing.
type Step struct {
	What   string `json:"what"`
	Detail string `json:"detail,omitempty"`
	OK     bool   `json:"ok"`
}

// Admin is a PROVEN administration access to ONE device.
//
// "Proven" and not "open": on two of the three platforms there is nothing open,
// only credentials that have been observed to work. The name avoids promising that
// there is an object to close (cf. the header, § on the absence of `Close`).
//
// The methods of THIS interface are the ones every switching platform has: read its
// state, set the VLAN membership of its ports, change the secret of the account
// that just got in. A connector that hasn't coded them yet returns
// [ErrNotImplemented]; it has no right to return an empty success, which would
// suggest a configuration is in place.
//
// Everything a platform can structurally NOT have lives in the optional interfaces
// declared further below, and is tested by type assertion.
type Admin interface {
	// Connector returns the identity of the connector that produced this access.
	//
	// Serves MESSAGES, and that is reason enough for it to exist: "this platform
	// cannot declare a trap receiver" helps no one if the screen doesn't say which
	// platform it's talking about. It also spares the caller from carrying the
	// connector name alongside the access as a separate variable — two variables
	// that would eventually end up pointing at two different devices.
	Connector() Info

	// Account is the account that ACTUALLY opened the access.
	//
	// Not the one that was wanted: the one that answered. The difference decides
	// the rest of the provisioning — same account, its secret gets changed;
	// different account, it's a RENAME, and the new one must be created before the
	// old one is removed (cf. api.handleApplyDeviceCredential).
	Account() string

	// Accepted is the candidate that worked, exactly as it was supplied.
	//
	// Returned explicitly because the caller needs it: the device's CURRENT secret
	// is the one that just got in, and some platforms require it in order to set a
	// new one (SwOS answers HTTP 405 "Bad old password").
	//
	// Having the contract return it replaces a reconstruction from the transcript
	// that the API layer used to do: it looked for a step whose label starts with
	// "connected as" and then cross-referenced `Why` against the candidate list. A
	// transcript label is TEXT FOR A HUMAN — rewording it is legitimate and looks
	// harmless, and it silently broke credential provisioning. This kind of
	// coupling has no business surviving into the contract.
	//
	// Empty if no candidate was used (the case of a platform that authenticates
	// nothing, which does not exist today).
	Accepted() Candidate

	// Transcript is the record of everything this access did, in order.
	//
	// Not a developer convenience: on a device that can be made unreachable by a
	// mistake, and that has neither SSH nor a serial port, it is the only way to
	// understand a failure without going back on site. It is returned to the
	// operator in the HTTP response AND attached to the pipeline, which makes it
	// reviewable afterward — that is, at the moment it's actually needed.
	//
	// Named `Transcript` and not `Steps` for a mundane reason that nonetheless
	// matters for all three connectors: they all expose a `Steps` FIELD, and a Go
	// type cannot carry a field and a method with the same name. This name lets
	// them adopt the contract by addition, without renaming a field their own tests
	// read.
	Transcript() []Step

	// ReadState re-reads the device. READ ONLY: replayable as often as needed,
	// including on a production fleet.
	//
	// This is the "Read" step of the reconciliation pipeline. Without it, meshcraft
	// cannot know whether what it believes matches what actually is, and
	// "configuration drift" remains just a word. It is also what allows a write to
	// be DATED: nothing is considered applied on the strength of a return code
	// alone.
	ReadState(ctx context.Context) (State, error)

	// ApplyPorts sets the VLAN membership of the ports.
	//
	// The most dangerous operation in the product on two of the three platforms:
	// Instant On and SwOS have neither SSH, nor Telnet, nor a serial port, and a
	// write that removes the uplink port from its VLAN makes the device mute — the
	// only recourse is a paperclip, on site. RouterOS has a serial console and
	// MAC-winbox, which makes the mistake recoverable without making it acceptable.
	//
	// THE SAFEGUARDS LIVE IN THE CONNECTOR, not here and not in the caller. This is
	// the "never cut off your own hand" rule placed where it can actually be
	// guaranteed — with whoever does the writing. The contract therefore does not
	// dictate HOW to protect: it only requires stating what was spared, via
	// [ApplyResult.Skipped]. The rule genuinely differs from one platform to
	// another, and that's instructive: on Instant On the native VLAN of an active
	// port is protected; on SwOS it's the TAGGING of the management VLAN on that
	// port that must not change — the naive rule protected nothing there, since the
	// reference device's session arrived tagged on a cage whose native VLAN was 1.
	//
	// A write NEVER re-reads a cached state: it re-reads the device first.
	//
	// THE WRITE PERSISTS, when the platform distinguishes the two. This is decided
	// here and not left to the connector, because two connectors answering
	// differently would produce two behaviors that nothing on screen would
	// distinguish: a port configuration that a reboot undoes is WORSE than a
	// refused write, since it was observed as successful. The caller therefore does
	// not need to call [Persister] after `ApplyPorts` — and if it did, that would be
	// one more round trip on an interface that only accepts five sessions.
	//
	// A persistence failure does NOT cancel the write: the change is genuinely in
	// place, and claiming otherwise would be false. It is recorded in the
	// transcript.
	ApplyPorts(ctx context.Context, plan PortPlan) (ApplyResult, error)

	// SetSecret changes the password OF THE ACCOUNT THAT JUST GOT IN.
	//
	// The account is not a parameter, and that is deliberate: it is
	// [Admin.Account], and letting the caller name a different one would open the
	// door to "change the password of an account we haven't proven we can access."
	// Creating or modifying ANOTHER account is a distinct gesture, and it lives in
	// [AccountManager] because not all platforms have it.
	//
	// `oldSecret` is passed even when the platform doesn't require it: Instant On
	// ignores it (the open session is proof enough), SwOS turns it into the RC4 key
	// of the digest it expects and answers HTTP 405 if it's wrong. The caller must
	// not have to distinguish the two cases — that is exactly what the contract
	// exists to absorb.
	//
	// THE WRITE PERSISTS, like [Admin.ApplyPorts] and for an even stronger reason:
	// an administration password that does not survive a reboot brings the device
	// back to its passwordless factory account, which is exactly the trap
	// provisioning exists to close. The caller therefore does not need to chain a
	// call to [Persister].
	//
	// What remains the caller's responsibility, and that no connector can do in its
	// place: only saving the new secret to the database after having OBSERVED that
	// it opens the device — that is, after a second [Connector.Authenticate] call
	// carrying that one candidate. A form accepted without effect is a failure mode
	// observed on Instant On, and a database convinced of a success that never
	// happened costs a site visit.
	SetSecret(ctx context.Context, oldSecret, newSecret string) error

	// Close releases whatever this access holds open — an SSH connection above
	// all else.
	//
	// ADDED AFTER THE FACT, and the story is worth keeping: every SSH-based
	// connector (Cisco IOS, HPE ProCurve, FS S3400/S3900, Ubiquiti EdgeSwitch)
	// already had its own Close(), and NOTHING in the API layer ever called
	// it — the interface simply did not declare it, so `s.adminFor`'s ~12
	// call sites had no method to call. Each apply, each explicit re-read,
	// each credential rotation opened one more SSH session and never gave it
	// back, relying on Go's garbage collector to eventually notice — which,
	// for an open TCP connection, it does not do promptly, if ever, before
	// the process exits. On a device that hands out session slots by the
	// handful (this connector's own Capabilities.MaxSessions, when it
	// declares one — EdgeSwitch says 5), that is a leak with a clock on it:
	// observed live on a lab EdgeSwitch, where two operator attempts to open
	// the browser console got an immediate EOF from the device — accepted at
	// the transport, then dropped, with zero bytes exchanged either way —
	// seconds after an apply had authenticated and never let go.
	//
	// REQUIRED, not optional, and idempotent: a connector with nothing to
	// release (Instant On, RouterOS, SwOS — HTTP-based, no persistent
	// session) implements it as a no-op. Calling it twice, or on an access
	// that never fully opened (a refused credential still returns a non-nil
	// Admin, cf. the connectors' own Authenticate), must never panic — every
	// existing SSH implementation already satisfies this, since Close() was
	// always meant to run at the end of a session, including a failed one.
	Close()
}

// --- OPTIONAL capabilities ---
//
// Each is a separate interface, tested by type assertion. The caller can therefore
// refuse BEFORE starting and say why, which is the only way to avoid offering a
// button that leads nowhere (cf. the header, § on the boundary).
//
// The convention for the caller: a failed assertion is translated into
// [NotSupported], whose message names the connector, the gesture, and the reason. A
// single phrasing for every absent capability beats eight approximate variants.

// AccountManager: the platform has NAMED ACCOUNTS.
//
// OPTIONAL because SwOS does not have any — not "not yet," not at all. The survey
// is unambiguous: the password-change form carries only three fields (old, new,
// confirmation), the page that receives it contains only one key (`{pwd:""}`), and
// the device's FULL configuration backup mentions no username at all. There is
// nothing to name.
//
// Consequence the caller must hold onto: on such a platform, the fleet's unified
// account name (`mcadmin`) cannot be set, and it is
// [AdminAccount.EffectiveUsername] that resolves it to `admin` BEFORE any attempt.
// The absence of this interface is therefore the second lock, not the first — if
// the caller ends up wanting to create an account on SwOS, it means the capability
// was ignored upstream.
//
// THESE GESTURES DO NOT PERSIST, unlike [Admin.SetSecret] — and this is the
// contract's only asymmetry, so it needs justifying. A RENAME puts two accounts at
// risk: the new one is created, proven to open the device, and only then is the old
// one removed. Who persists what and WHEN is then a sequencing decision only the
// caller can make — between the two steps, a power outage must not leave the device
// with a single account, and not the one that survived. A connector that persisted
// on its own initiative at every write would take away that choice, and take it
// away silently.
//
// The caller therefore chains its own calls to [Persister] at the points it has
// chosen.
type AccountManager interface {
	// Accounts lists the administration accounts present, WITH what the platform
	// says about the rights of each.
	//
	// A READ, and it is what makes account writes safe: the platforms distinguish
	// "create" from "modify" and refuse one in place of the other. It is also what
	// makes the operation replayable — a write interrupted and resumed doesn't trip
	// over a half-created account.
	//
	// IT RETURNS [Account] AND NOT NAMES, since 2026-08-10, and the reason is a
	// question this contract could not answer: "which PRIVILEGED accounts exist
	// that meshcraft did not create". With a `[]string` a full administrator and
	// a read-only account were the same value, and connectors that had already
	// parsed the difference (RouterOS's group, Instant On's accessLevel, EOS's
	// privilege AND role) discarded it here. See account.go for the type and for
	// why "not stated" is neither 0 nor false.
	//
	// NO CONNECTOR PAYS A NEW READ FOR THIS. The rule is the one that governs the
	// whole tree: an enumeration is cheap, and a range that grants five sessions
	// must not spend a second round trip to answer it. Where the privilege is
	// already in the bytes being fetched it travels; where it is not, the account
	// comes back [PrivilegeUnknown] / [AuthorityUnknown] and the connector's doc
	// comment says why. An implementation that guessed a platform default instead
	// would be worse than the `[]string` it replaced: it would look like an answer.
	Accounts(ctx context.Context) ([]Account, error)

	// EnsureAccount guarantees that a named account exists and carries this secret.
	//
	// Idempotent by construction: the connector reads what exists and chooses
	// itself between create and modify. The caller does not need to know which of
	// the two verbs its platform expects.
	EnsureAccount(ctx context.Context, user, secret string) error

	// DeleteAccount removes an administration account.
	//
	// DANGEROUS by nature. The caller MUST have proven — by obtaining a real
	// [Admin] under the new account, not by reading a return code — that another
	// door works before calling this. On a range with neither SSH nor a serial
	// port, deleting first and verifying afterward costs a site visit.
	//
	// The connector refuses to delete the account of the access currently in use:
	// it's the only safeguard it can carry on its own.
	DeleteAccount(ctx context.Context, user string) error
}

// Persister: the platform distinguishes APPLYING from PERSISTING.
//
// OPTIONAL, and the field that decides is already in the contract:
// [Capabilities.Commit]. The caller must only call `Persist` on
// [CommitApplyThenSave] — that is the only case where omitting it loses the
// configuration at the next reboot, and the only one where calling it too early
// bakes in a mistake.
//
// The three shipped platforms show both worlds, and the difference changes the
// entire write strategy:
//
//   - Instant On: `apply_then_save`. A FREE rollback, provided it's used in
//     order — apply, verify reachability, THEN save. This is what makes switching
//     the management VLAN testable on live hardware: as long as nothing is in
//     flash, a power outage cancels the change;
//   - SwOS and RouterOS: `immediate`. Nothing to save, and therefore no safety net
//     either. On SwOS, the ONLY way back is the configuration copy taken BEFORE
//     the write (cf. [ApplyResult.Backup]).
//
// Not implementing this interface is therefore INFORMATION, not a gap.
type Persister interface {
	// Persist copies the running configuration into the startup configuration.
	//
	// Its failure does not cancel what was just written — the change is genuinely
	// in place, and claiming otherwise would be false. It is recorded in the
	// transcript, which is the channel meant for what the operator needs to know.
	Persist(ctx context.Context) error
}

// SNMPConfigurator: the platform has a configurable SNMP agent.
//
// OPTIONAL because a platform can have none at all, and because "having an agent"
// doesn't mean "knowing how to set an identity on it." These are two distinct
// questions, and [Capabilities.SNMPVersions] answers the first: an empty
// [Capabilities.SNMPVersions] rules the device out UPSTREAM — `snmpprov.For`
// produces no monitored plan without a declared version — so the caller has
// nothing to set, whether this interface is implemented or not.
//
// The presence of this interface is therefore not what decides whether to set an
// identity: [Capabilities.SNMPVersions] decides, and the interface executes. The
// two must agree, and a connector that declared versions without implementing this
// interface would be advertising monitoring it doesn't know how to turn on.
//
// SwOS is the case that tested this separation. Its version list stayed empty for a
// long time — an agent did exist (`/snmp.b`, four fields), but no request had ever
// gotten a response, so nobody knew which version it spoke — while `ApplySNMP` was
// already written and proven. When the walk on July 29, 2026 got a response,
// monitoring for this range opened up by flipping ONE capability line, with nothing
// left to code. In the other order, monitoring would have had to be advertised and
// then discovered to be impossible to turn on.
type SNMPConfigurator interface {
	// ApplySNMP sets the SNMP identity on the device.
	//
	// The result states what was done AND what was not ([SNMPResult.Skipped]): a
	// partial apply presented as complete would suggest monitoring that doesn't
	// exist.
	//
	// An identity the platform cannot carry is REFUSED, never rounded to the
	// "closest" one. Setting MD5 where the caller wanted SHA-256 would give working
	// monitoring and false compliance; setting a v2c community where a v3 user was
	// requested would put a secret in the clear on the network that nobody chose.
	ApplySNMP(ctx context.Context, want SNMPIdentity) (SNMPResult, error)
}

// TrapPusher: the platform can PUSH traps to a collector.
//
// OPTIONAL, and SwOS is the demonstration: `/snmp.b` has NO receiver field, in any
// tab, nor in the full configuration backup. This is not a hidden page — a SwOS's
// entire web interface fits in one file, and it does not know about traps. Such a
// device is monitored by polling: the information is there, it's the REACTIVITY
// that's missing, and saying so is better than writing a line into a device that
// won't use it.
//
// The presence of this interface must agree with [MonitoringCapabilities.Traps]:
// it's the same capability that decides whether the proxy opens 162/udp. Declaring
// one without the other would write a destination to a port nobody is listening
// on, or open a port nobody feeds.
type TrapPusher interface {
	// ApplyTrapReceiver declares the collector as the trap destination.
	//
	// The connector REFUSES if the SNMP agent is off: a receiver set on a silent
	// agent gives an exact configuration and total silence. It is
	// [SNMPConfigurator.ApplySNMP] that turns the agent on, and the order between
	// the two is not negotiable.
	//
	// No THIRD-PARTY receiver is ever removed, even to make room: cutting off
	// someone else's monitoring to install one's own would be the worst possible
	// service. A full table is REPORTED, not cleared.
	//
	// And the result is only a CLAIM: the line is in the device, which does not
	// prove its traps are arriving. The proof is the first trap received, and that
	// belongs to the caller (cf. api.confirmTrapPath) — a firewall, an asymmetric
	// route, or an ACL on the management VLAN is enough to make it transmit into
	// the void.
	ApplyTrapReceiver(ctx context.Context, want TrapDestination) (TrapResult, error)
}

// MgmtMover: the platform can move its MANAGEMENT ADDRESSING under the very session
// that is commanding it.
//
// OPTIONAL, and the most dangerous gesture in the product: it moves the access
// while it is being used. Hence three methods and not one — the maneuver happens in
// two VERIFIABLE steps, never in a single leap.
//
// This split comes from the Instant On survey, which accepts SEVERAL IPv4
// interfaces: one is added, a lease is observed to have arrived, and only then is
// the switch made. As long as the old interface is there, the current access stays
// open. A platform that cannot carry two addresses cannot implement this contract
// as it stands, and that is the correct response: it must then NOT implement it,
// rather than leap without looking.
//
// THE SWITCH DOES NOT PERSIST, and that is the exception that justifies
// [Persister] existing separately. Everything else in the contract persists what it
// sets ([Admin.ApplyPorts], [Admin.SetSecret]); here, it does NOT — as long as
// nothing is written to flash, a power outage brings the device back to its
// previous addressing, and that safety net is what allows the maneuver to be
// attempted on live hardware. A connector that persisted along the way would remove
// the operator's only recourse.
//
// Persistence is therefore a THIRD gesture, the caller's responsibility, and it
// must require having OBSERVED that the device itself declares it administers
// through the target VLAN before calling it: saving an unverified switch would bake
// in a mistake.
type MgmtMover interface {
	// PrepareMgmt adds an administration access on the target VLAN, WITHOUT
	// removing anything. The device keeps its current address and stays reachable
	// throughout the operation.
	PrepareMgmt(ctx context.Context, vlanID int) error

	// MgmtAddressed reports whether the prepared access has OBTAINED its address,
	// and which one.
	//
	// This is the green light for the second half: as long as it's red, switching
	// over would cut off access with nothing to show for it. A missing address is
	// not necessarily a configuration defect — on Instant On, a DHCP interface
	// without a lease reads as a distinct state, and conflating the two would make
	// this "fix" a write that was already correct.
	MgmtAddressed(ctx context.Context, vlanID int) (bool, string, error)

	// CommitMgmt moves administration onto the target VLAN.
	//
	// Only call this AFTER a favorable [MgmtMover.MgmtAddressed]. This is the call
	// that cuts the current session if invoked too early.
	//
	// The connector does here everything its platform requires, in the order it
	// requires — on Instant On, removing the repeater interface because the device
	// refuses a management VLAN that already carries an IP configuration. This kind
	// of ORDERING constraint has no business living with the caller: it is specific
	// to the platform, and splitting it across two packages guarantees that one day
	// one of them forgets its half.
	CommitMgmt(ctx context.Context, vlanID int) error
}

// MgmtJumper: the platform cannot carry TWO management paths at once, and meshcraft
// still offers an explicit LEAP for operators who accept the risk.
//
// FACULTATIVE, SwOS-shaped. Distinct from [MgmtMover]: there is no prepare/verify
// window where both addresses coexist. The connector MUST take a backup before the
// write and return it, so the operator has something to restore if relocate fails.
//
// A connector that can implement [MgmtMover] MUST NOT implement this instead —
// dual-path is always preferable. Flip [MgmtCapabilities.JumpVLAN] only after the
// jump and backup restore have been proven on hardware.
type MgmtJumper interface {
	// JumpMgmt applies the new management VLAN and/or addressing in one shot.
	// Returns the pre-write backup (may be empty only if the platform has none).
	JumpMgmt(ctx context.Context, vlanID int, want MgmtAddrIntent) (backup []byte, err error)
}

// AsMgmtJumper returns the admin as a management jumper when it is one.
func AsMgmtJumper(a Admin) (MgmtJumper, bool) {
	var j, ok = a.(MgmtJumper)
	return j, ok
}

// PortNamer: the connector can translate a port name it OBSERVED (a pasted
// configuration, a raw read) into the standard-model name it uses everywhere
// else (State, PortPlan, faceplate groups).
//
// OPTIONAL, and it answers a precise need: on platforms that NUMBER their
// faceplate under one uniform standard scheme ("GE1"... regardless of the
// device), the REAL hardware name — the one a pasted-configuration import
// brings into the model — must be re-keyed before it lands in devices.ports,
// or the same device would carry two different port-name vocabularies
// depending on whether it was adopted by push or by import.
//
// On the Driver, not the Admin: the translation is a PURE function, no device
// access, useful even before an access session is open.
type PortNamer interface {
	Driver
	// StandardPortName translates a port name AS OBSERVED ON THE PLATFORM
	// ("ethernet 1/11", "Eth 1/11") into the standard-model name ("GE11").
	// ok=false when the name falls outside what this connector can cover (an
	// extra stacking unit, for instance) — the caller then REFUSES that port
	// rather than renaming it arbitrarily.
	StandardPortName(deviceName string) (name string, ok bool)
}

// AsPortNamer returns the driver as a port namer when it is one.
func AsPortNamer(d Driver) (PortNamer, bool) {
	var p, ok = d.(PortNamer)
	return p, ok
}

// FaceplateReader: the connector can READ the physical faceplate (port groups) on
// an already open access.
//
// OPTIONAL, and it addresses a precise gap: the faceplate (`devices.port_groups`)
// is only populated AT ADOPTION TIME, from discovery enrichment. A device adopted
// BEFORE its connector could read the faceplate — or entered by hand — is therefore
// left with no drawable ports forever, and nothing catches up: ReadState returns the
// VLAN state of the ports but not the faceplate (cf. [State], which does not carry
// groups). On an S3900 adopted the day before its connector shipped, this produced
// a device with zero ports, an LLDP resolution that discarded every neighbor, and
// an SNMP MaxIfIndex reading of zero.
//
// Distinct from [Identifier]: that one opens its own session from candidates
// (discovery path), this one reads over an access the caller ALREADY holds proven
// (re-read path). This is what lets the "refresh" button rebuild the faceplate
// without a second authentication cycle.
//
// The shape is the one [Identity.PortGroups] already carries — `prefix`, `count`,
// `from`, `speed`, `poe`, `label` — so the caller can persist it without
// translation.
type FaceplateReader interface {
	ReadFaceplate(ctx context.Context) ([]map[string]any, error)
}

// AsFaceplateReader returns the admin as a faceplate reader when it is one.
func AsFaceplateReader(a Admin) (FaceplateReader, bool) {
	var r, ok = a.(FaceplateReader)
	return r, ok
}

// IdentityReader: the connector can say WHAT the device is on an access the
// caller ALREADY holds proven.
//
// OPTIONAL, and the re-read twin of [Identifier], with the same split as
// [FaceplateReader] vs discovery enrichment: Identify opens its own session from
// candidates (discovery / relocation path), this one answers on the open access
// (re-read path), which is what lets read-state refresh a stale or empty
// `devices.model` without a second authentication cycle.
//
// CHEAPNESS IS THE ADMISSION TICKET. The CLI platforms already parsed `show
// version` (or the login banner) at connect — for them this is a memory read,
// zero wire round-trips. RouterOS reads the same three REST objects its
// discovery identification uses, on the client it already holds. A connector
// for which identity would cost a fresh session should NOT implement this:
// the API layer falls back to [Identifier] for the fill-the-hole case only.
//
// PARTIAL BY CONSTRUCTION, like [Identity] itself: return what is known even
// alongside an error. The caller never overwrites a stored fact with an empty
// observation.
type IdentityReader interface {
	ReadIdentity(ctx context.Context) (Identity, error)
}

// AsIdentityReader returns the admin as an identity reader when it is one.
func AsIdentityReader(a Admin) (IdentityReader, bool) {
	var r, ok = a.(IdentityReader)
	return r, ok
}

// MgmtStaticOwnerProber learns the platform's static-interface owner value by a
// temporary write on a NON-management VLAN (lab / bring-up). Instant On–shaped.
type MgmtStaticOwnerProber interface {
	ProbeStaticOwner(ctx context.Context, vlanID int, addr, mask string) (owner int, raw string, err error)
}

// AsMgmtStaticOwnerProber returns the admin as a static-owner prober when it is one.
func AsMgmtStaticOwnerProber(a Admin) (MgmtStaticOwnerProber, bool) {
	var p, ok = a.(MgmtStaticOwnerProber)
	return p, ok
}

// RawReader: the platform can return a configuration unit EXACTLY AS-IS.
//
// OPTIONAL, and this is a RECONNAISSANCE tool, not a product feature. It exists
// because these interfaces are undocumented: a section can only be WRITTEN once
// its actual field set has been seen, and guessing produces Instant On's
// characteristic failure mode — `statusCode 0`, no change, no message.
//
// READ ONLY, and reserved for global administration by the caller.
type RawReader interface {
	// ReadRaw returns the raw content of the named unit.
	//
	// The connector SANITIZES the name before using it: it ends up in a URL, and an
	// injection there would redirect the request to a different endpoint on the
	// device.
	ReadRaw(ctx context.Context, unit string) (string, error)

	// RawKind names, for a human, what "a unit" means on this platform: "wcd
	// section," "*.b page," "REST menu."
	//
	// Used on screen. Without it, the debugging probe would ask for a `wcd` section
	// name from someone looking at a MikroTik, and the failure would look like a
	// device outage.
	RawKind() string
}

// Identifier: the connector can tell WHICH device answers at an address.
//
// OPTIONAL, but it is the capability that makes RELOCATION possible: when a device
// changes address, it is recognized by its serial number, and nothing else is
// authoritative. On the Connector and not the Admin, because the amount of
// credentials needed ranges from none to all, depending on the platform:
//
//   - Instant On: NONE. The login page carries `sysDescr` and the serial number in
//     its hidden fields, and the provisioning UI on port 4443 serves them in the
//     clear on a fresh device;
//   - SwOS: the MODEL without any credential — the realm of its Digest challenge
//     carries it (`realm="CSS326-24G-2S+"`) — but the serial number and firmware
//     require logging in;
//   - RouterOS: NOTHING without a credential. The REST API answers 401 on all of
//     its resources, including the one that carries the model.
//
// Hence candidates as a parameter, possibly empty. A connector that doesn't need
// them ignores them; a connector that does need them returns whatever it WAS able
// to establish TOGETHER WITH the reason it stopped — "this is a CSS326-24G-2S+
// whose password we don't have" is a useful result where "failure" would not be.
type Identifier interface {
	Identify(ctx context.Context, tgt Target, cands []Candidate) (Identity, error)
}

// Onboarder: the platform requires a MANAGEMENT-MODE CHOICE before it can be
// administered locally.
//
// OPTIONAL, and as of today specific to a single range: an Instant On fresh out of
// the factory expects the vendor's cloud portal, and it must be made to abandon
// that ("Configure Locally") before it will accept being administered by us.
// Neither SwOS nor RouterOS have an equivalent — they are administrable straight
// out of the box.
//
// This interface exists so that the REFUSAL is an absent capability and not a
// string comparison: the API layer used to test
// `driver == "aruba-instant-on-1930"`, which lumps any future platform on the same
// side as those that don't need it, without saying so.
type Onboarder interface {
	// Onboard switches the device to local administration.
	//
	// A WRITE, and a LASTING choice: on the known range, only a factory reset
	// undoes it. The connector therefore checks the state BEFORE and re-reads it
	// AFTER — a 2xx that changes nothing is a silent failure, and comparing the two
	// is the only proof that exists.
	Onboard(ctx context.Context, tgt Target) (OnboardResult, error)
}

// --- Refusing legibly ---// Unsupported: the connector does not carry this capability.
//
// A TYPE and not a message, for two reasons. First because a caller must be able
// to recognize it: `errors.Is(err, driver.ErrNotImplemented)` answers true, which
// gives a SINGLE test for the package's two mechanisms — the absent optional
// interface and the mandatory method not coded yet. Second because the three
// fields are what a screen must display separately: what was requested, of whom,
// and why it's a dead end.
//
// `Because` is the field that makes all of this worthwhile, and it must not be
// empty. "Not supported" sends people looking for a checkbox; "this platform has
// no trap-receiver field at all, it is monitored by polling" ends the search.
type Unsupported struct {
	// Connector is the connector's name, as [Info.Name] carries it.
	Connector string
	// Gesture is what was requested, named the way an operator would name it —
	// "declare a trap receiver," not "ApplyTrapReceiver."
	Gesture string
	// Because is the reason, from the PLATFORM's side. It must answer "is there
	// anything to expect here?".
	Because string
}

func (e *Unsupported) Error() string {
	var out = e.Gesture + ": connector \"" + e.Connector + "\" cannot do this"
	if e.Because != "" {
		out += " — " + e.Because
	}
	return out
}

// Unwrap attaches this refusal to [ErrNotImplemented]: the two ways of saying "I
// don't know how to do that" can then be tested by the caller in a single way.
func (e *Unsupported) Unwrap() error { return ErrNotImplemented }

// NotSupported builds the refusal for an absent capability.
//
// Use it AS SOON AS A TYPE ASSERTION FAILS, and before reaching the device: that
// is the whole point of optional interfaces. Reaching the device to ask for
// something already known to be impossible costs a session on a platform that only
// accepts five of them, and produces a message that talks about transport where
// the fact is really about a capability.
func NotSupported(connector, gesture, because string) error {
	return &Unsupported{Connector: connector, Gesture: gesture, Because: because}
}

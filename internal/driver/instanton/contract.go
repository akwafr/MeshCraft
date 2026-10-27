package instanton

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Access contract adoption (internal/driver/connector.go) by the Instant On connector.
//
// # This file is SEAMWORK, and must stay that way
//
// This connector is the only one IN PRODUCTION in the project: it drives real hardware
// for an operator, on a product line with no SSH, Telnet, or serial port, whose only
// recovery from error is a paperclip in the reset button, on site. Adopting a contract
// in these conditions is not done by rewriting: it is done by TRANSLATING vocabulary,
// without touching anything that talks to the device — no URL, no XML field, no gesture
// order.
//
// Hence the shape of everything below: one-line methods that delegate to code already
// proven on hardware. That is a goal, not laziness. Every line of logic moved here would
// be a line no hardware test answers anymore, and this product line's failure mode is
// exactly the one you cannot see — `statusCode 0`, no change, no message.
//
// # Why type ALIASES and not definitions
//
// Types the API layer exchanges with a connector now live in internal/driver (cf.
// state.go). This package carried them identically on its own because it was first — and
// two of the three connectors shipped since said in comments they copied "the same shape
// as the Instant On connector". Three identical definitions by convention hold until one
// gains a field.
//
// An ALIAS (`type State = driver.State`) not a definition, and that is the whole point:
// fields, pure functions and ALL EXISTING TESTS in this package keep compiling as-is,
// without conversion or rewrite. A definition would have forced conversion at every
// boundary — and conversion that drops a field shows in no test.
//
// CONTRACT JSON TAGS ARE THE ORIGINAL ONES, deliberately, and that is not cosmetic:
// `State` is serialized in `devices.observed` and read by everything that computes
// configuration drift. Renaming a tag would read "zero VLANs, no ports" on every
// already-collected device — presenting as massive drift and triggering applies.
type (
	// Step is a transcript step. [Admin]'s `Steps` field becomes `[]driver.Step` without
	// changing a line in this package.
	Step = driver.Step
	// Candidate is a secret to try, with the reason for its position in the order.
	// Order comes from the caller: the connector does not decide what a device likely
	// carries, it applies what it is given.
	Candidate = driver.Candidate

	VLAN      = driver.VLAN
	PortState = driver.PortState
	State     = driver.State
	// PortPlan aggregates what an apply must achieve. It replaces the five parameters
	// [Admin.ApplyPorts] took: when apply needs one more constraint, all three connectors
	// change together or not at all.
	PortPlan    = driver.PortPlan
	ApplyResult = driver.ApplyResult

	SNMPIdentity = driver.SNMPIdentity
	SNMPResult   = driver.SNMPResult
	// TrapDestination and TrapResult: the latter merged this package's two counters
	// `KnownV2c`/`KnownV3` into one `Known` (cf. ApplyTrapReceiver, which records the
	// other table in `Notes`).
	TrapDestination = driver.TrapDestination
	TrapResult      = driver.TrapResult

	// Identity is what the connector could establish about the device at an address.
	//
	// The contract type carries ONE MORE FIELD than this package's type:
	// `PortGroups`, the faceplate in the form the frontend consumes. This package returned
	// it via METHOD `Identity.PortGroups()`, which an alias forbids — you cannot define a
	// method on another package's type, and the name would be the field's anyway. The
	// faceplate is therefore computed by [portGroups] and FILLED IN by everything that
	// produces an identity, providing the same service without the method.
	Identity = driver.Identity
)

// Compile-time failure when the contract moves.
//
// A runtime type assertion (`admin.(driver.Admin)`) would fail silently in the field:
// the caller would fall back to "this platform cannot do that" for a capability this
// connector has, and nobody would know a signature drifted. These declarations cost zero
// bytes at runtime and turn that silent failure into a build error.
//
// The list is also the DECLARATION of what this product line can do: it carries ALL
// optional contract interfaces, unlike any other shipped platform — it served to design
// them.
var (
	_ driver.Connector  = (*Driver1930)(nil)
	_ driver.Identifier = (*Driver1930)(nil)
	_ driver.Onboarder  = (*Driver1930)(nil)

	_ driver.Admin            = (*Admin)(nil)
	_ driver.AccountManager   = (*Admin)(nil)
	_ driver.Persister        = (*Admin)(nil)
	_ driver.SNMPConfigurator = (*Admin)(nil)
	_ driver.TrapPusher       = (*Admin)(nil)
	_ driver.MgmtMover        = (*Admin)(nil)
	_ driver.MgmtAddrReader   = (*Admin)(nil)
	_ driver.MgmtAddrWriter   = (*Admin)(nil)
	_ driver.RawReader        = (*Admin)(nil)
)

// [driver.MgmtAddrReader] / [driver.MgmtAddrWriter] live in mgmtwrite.go.
// DHCP owner is OBSERVED (= 2). Static owner is OBSERVED (= 1) via
// [Admin.ProbeStaticOwner] on lab sw-labo-1930. Factory `/connectivity`
// remains factory-fresh only.

// --- The connector ---

// Authenticate returns PROVEN administration access on this device.
//
// [OpenAdmin] followed by [Admin.Login], in ONE call — the form the contract requires
// because two of three shipped platforms have NOTHING to open (their authentication
// re-presents on every request). Here there is a session, and it is a scarce resource:
// the device accepts only five.
//
// CANDIDATE ORDER IS THE CALLER'S and is not reordered; no candidate is replayed or
// fabricated. None of this is new — [Admin.Login] already does it, and the contract
// merely states it explicitly.
//
// ACCESS IS RETURNED EVEN ON FAILURE, deliberately. On a product line reached only via
// web UI, the transcript is the only way to understand why a connection was refused
// without returning on site: "no credential accepted" and "too many open sessions"
// are fixed by unrelated gestures. The caller that checks the error — and must — can
// still read [Admin.Transcript] to display it, as the API layer does today on all
// failure paths. Returning nil would erase that trace precisely when it matters.
func (d *Driver1930) Authenticate(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Admin, error) {
	var a = OpenAdmin(tgt.Host, tgt.Dialer)
	// Port prefix is kept ON the session at construction: the contract no longer passes
	// it to [Admin.ReadState] or [Admin.ApplyPorts], for good reason — two calls on the
	// same session with different prefixes would write different ports believing they are
	// the same.
	a.prefix = tgt.PortPrefix
	var err = a.Login(ctx, cands)
	return a, err
}

// --- Administration access ---

// Connector returns the identity of the connector that produced this access. For
// MESSAGES: "this platform cannot do X" helps nobody if the screen does not say
// which platform.
func (a *Admin) Connector() driver.Info { return (&Driver1930{}).Info() }

// Account is the account that ACTUALLY opened access — not the one intended. The
// difference decides provisioning next steps: same account, change its secret; different
// account, that is a rename, and the new one must be created before removing the old.
func (a *Admin) Account() string { return a.User }

// Accepted is the candidate that worked, as provided.
//
// THE CLEAREST WIN OF THE WHOLE MIGRATION, and worth explaining. The API layer used to
// reconstruct the accepted secret by searching the TRANSCRIPT: an step whose label starts
// with "logged in as", then cross-checking its `Why` with the candidate list
// (`currentSecretOf`). A transcript label is TEXT FOR A HUMAN — rephrasing it is
// legitimate, apparently harmless, and silently broke identity apply — the gesture that
// leaves a device with a password the database does not know.
//
// The candidate is stored by [Admin.Login] when the device accepts it, the only place
// that knows firsthand. Empty until a session is open.
func (a *Admin) Accepted() Candidate { return a.accepted }

// Transcript is the trace of everything this access did, in order.
//
// Named `Transcript` not `Steps` by the contract, for a prosaic reason that applies to
// all three connectors: they all expose a FIELD `Steps`, and a Go type cannot carry a
// field and method of the same name. This name allows contract adoption by addition,
// without renaming a field this package's tests read — and the API layer serializes in
// its responses.
func (a *Admin) Transcript() []Step { return a.Steps }

// SetSecret changes the password of THE ACCOUNT THAT JUST LOGGED IN.
//
// The account is not a contract parameter, deliberately: it is [Admin.Account]. Letting
// the caller name another would open "change password of an account we have not proven
// access to"; creating or modifying ANOTHER account is a distinct gesture, in
// [driver.AccountManager].
//
// `oldSecret` is passed to [Admin.SetPassword] which ignores it: on this product line
// the device does not require it, the open session is proof. The parameter exists because
// SwOS uses it as the key to the digest it expects — and the caller need not distinguish
// the two cases.
//
// APPLY PERSISTS: [Admin.SetPassword] already chains to [Admin.SaveConfig], as the
// contract requires. An admin password that would not survive reboot would return the
// device to factory account without password — exactly the trap provisioning exists to
// close.
func (a *Admin) SetSecret(ctx context.Context, oldSecret, newSecret string) error {
	return a.SetPassword(ctx, a.User, oldSecret, newSecret)
}

// Close: a no-op on this range. The access is an authenticated HTTP client,
// not a persistent session the device counts against a limit — there is
// nothing here for an SSH-based connector's Close() to free.
func (a *Admin) Close() {}

// Persist copies current configuration into startup configuration.
//
// This product line uses `apply_then_save` (cf. [driver.CommitApplyThenSave]): it is
// what made [driver.Persister] necessary. Rollback is FREE, provided you use it in order
// — apply, verify reachability, THEN save. Until nothing is in flash, power loss undoes,
// and that is the safety net that allows attempting a management VLAN switch on hardware
// in service.
func (a *Admin) Persist(ctx context.Context) error { return a.SaveConfig(ctx) }

// --- Management addressing move ---

// PrepareMgmt adds administration access on the target VLAN, WITHOUT removing anything.
// The device keeps its current address and stays reachable throughout.
func (a *Admin) PrepareMgmt(ctx context.Context, vlanID int) error {
	return a.AddManagementInterface(ctx, vlanID)
}

// MgmtAddressed reports whether prepared access HAS OBTAINED its address, and which.
// Green light for the second half: while red, switching would cut access with no
// replacement.
func (a *Admin) MgmtAddressed(ctx context.Context, vlanID int) (bool, string, error) {
	return a.InterfaceHasAddress(ctx, vlanID)
}

// CommitMgmt moves administration to the target VLAN.
//
// TWO DEVICE GESTURES IN ONE CALL, the only design decision in this file that is not a
// simple name translation: remove the duplicate interface THEN designate management VLAN.
// Order is not negotiable — the device refuses a management VLAN that already carries IP
// configuration ("Please remove IPv4 configuration on new management VLAN"), because IT
// installs the address there and does not want another on its path.
//
// This constraint is platform-specific, so it belongs in the connector. The API layer
// applied it itself, in sequence, in one handler: it worked, and could not last — splitting
// across two packages guarantees one day someone forgets their half, and that half costs a
// site visit where the device has no address.
//
// Call ONLY AFTER favorable [Admin.MgmtAddressed]: this call cuts the current session if
// run too early. Between removal and new lease, the device has no address on the target
// VLAN — it will get one back, same MAC so very likely same lease.
//
// DOES NOT PERSIST, deliberately, the exception that justifies [driver.Persister]
// separately: until nothing is written to flash, power loss restores prior addressing.
// Persisting here would remove the operator's only recovery; they chain to [Admin.Persist]
// when they have CONFIRMED the device declares itself administered via the target VLAN.
func (a *Admin) CommitMgmt(ctx context.Context, vlanID int) error {
	var err = a.RemoveManagementInterface(ctx, vlanID)
	if err != nil {
		return err
	}
	return a.SetManagementVLAN(ctx, vlanID)
}

// --- Raw read, reconnaissance tool ---

// ReadRaw returns raw content of the named unit. On this platform, a unit is a `wcd`
// SECTION (cf. [Admin.RawSection], which sanitizes the name before putting it in a URL).
func (a *Admin) ReadRaw(ctx context.Context, unit string) (string, error) {
	return a.RawSection(ctx, unit)
}

// RawKind names, for a human, what "a unit" means here.
//
// For the UI, not ornament: without it, the debug probe would ask for a `wcd` section
// name from someone looking at a MikroTik, and failure would look like device outage.
func (a *Admin) RawKind() string { return "section wcd" }

// --- Identification, without authentication ---

// Identify reports which device responds at an address.
//
// CANDIDATES ARE IGNORED, a fact of this product line to preserve: the login page carries
// `sysDescr` and serial in hidden fields, and provisioning UI on port 4443 serves them
// in clear on a new device. No credentials needed — where RouterOS says NOTHING without a
// password. That is what makes relocation possible here knowing nothing about the device
// being sought.
func (d *Driver1930) Identify(ctx context.Context, tgt driver.Target,
	_ []Candidate) (Identity, error) {
	return Inspect(ctx, tgt.Host, tgt.Dialer)
}

// --- Switch to local administration ---

// Onboard switches the device to local administration.
//
// A factory-fresh Instant On waits for the vendor cloud portal: it must be made to
// abandon that before it accepts administration by us. The only gesture of this kind among
// three shipped platforms — neither SwOS nor RouterOS have an equivalent, they are
// manageable out of the box. Hence an optional interface, not a `driver ==
// "aruba-instant-on-1930"` test in the API layer: string comparison would lump any future
// platform with those that need no onboarding, without saying so.
//
// Sequence is WHAT THE API LAYER ALREADY EXECUTES, same order and same calls — portal
// status, identity, then `PUT /managementPersonality` — because that is what was observed
// on hardware:
//
//  1. state BEFORE. Without it, `Changed` means nothing, and `Changed` is the only proof:
//     on this API, a 2xx that changes nothing is silent failure;
//  2. identity, "BEST EFFORT". Provisioning UI serves it without session, and reading it
//     here avoids a second round trip when adoption needs model and serial. Its failure does
//     NOT stop the switch: the requested gesture is the switch, not inventory;
//  3. the switch itself, which re-reads state AFTER (cf. [Client.ConfigureLocally]).
//
// `Changed` compares ENTIRE portal states, not the two stage labels. That is what the API
// layer displays today, and the safer of the two: identical labels can cover a
// `canEditConnectivitySettings` that flipped, i.e. a device that did change state.
func (d *Driver1930) Onboard(ctx context.Context,
	tgt driver.Target) (driver.OnboardResult, error) {
	var out driver.OnboardResult
	var client = NewClient(tgt.Host, tgt.Dialer)

	var before, err = client.PortalStatus(ctx)
	if err != nil {
		out.Steps = append(out.Steps, Step{
			What: "reading portal status", Detail: err.Error()})
		return out, err
	}
	out.Was = string(StageOf(before))
	out.Steps = append(out.Steps, Step{OK: true, What: "portal status read",
		Detail: fmt.Sprintf("management mode %q, stage %q", before.ManagementMode, out.Was)})

	var info, infoErr = client.SystemInfo(ctx)
	if infoErr == nil {
		out.Identity = identityFromSystemInfo(info)
		out.Steps = append(out.Steps, Step{OK: true,
			What:   "identity read from provisioning UI",
			Detail: info.Model + ", serial " + info.SerialNumber})
	} else {
		// Missing identity does not justify aborting the switch: what was requested is the
		// switch. But it must be SAID, otherwise adoption that follows would wonder at a
		// device without model.
		out.Steps = append(out.Steps, Step{
			What:   "identity not read from provisioning UI",
			Detail: infoErr.Error()})
	}

	var after, resp, cfgErr = client.ConfigureLocally(ctx)
	if resp != nil {
		// VERBATIM, never interpreted: on an API whose write gesture was reconstructed by
		// reading the device bundle, this is the only way to understand a refusal without
		// returning on site.
		out.Detail = fmt.Sprintf("HTTP %d — %s", resp.Status, strings.TrimSpace(resp.Body))
	}
	out.Stage = string(StageOf(after))
	out.Changed = before != after
	if cfgErr != nil {
		out.Steps = append(out.Steps, Step{
			What: "switch to local administration refused", Detail: cfgErr.Error()})
		return out, cfgErr
	}
	out.Steps = append(out.Steps, Step{OK: out.Changed,
		What:   "switch to local administration requested",
		Detail: fmt.Sprintf("stage %q → %q — %s", out.Was, out.Stage, changedLabel(out.Changed))})
	return out, nil
}

// changedLabel states whether something happened, in words that do not imply success when
// nothing moved. A 2xx followed by identical state is FAILURE, and must not read as quiet
// success.
func changedLabel(changed bool) string {
	if changed {
		return "device changed state"
	}
	return "portal status UNCHANGED: device accepted the request without doing anything"
}

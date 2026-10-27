package routeros

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Adoption of the access contract (internal/driver/connector.go) by the
// RouterOS connector.
//
// # Why this file exists
//
// This connector was COMPLETE and UNREACHABLE. Its capabilities are OBSERVED on
// hardware, its identification, state read, and apply logic are coded, and
// twenty-one real captures exercise them — but the API layer had no path to talk
// to it: it hard-coded `instanton.OpenAdmin`. The result is not hypothetical, it
// is confirmed: adopting the CRS326 triggered an SNMP apply over the Instant On
// protocol toward it, `https://192.0.2.54/`, connection refused because that
// device does not listen on 443. Nothing was written because TCP failed; the
// gesture itself had still been sent.
//
// This file is the STITCHING that closes that fault on the RouterOS side. It
// rewrites nothing: it translates vocabulary and delegates to code already
// proven on captures. Every line of logic moved here would be a line that no
// capture answers for anymore.
//
// # Why type ALIASES and not definitions
//
// The types the API layer exchanges with a live connector now live in
// internal/driver (cf. state.go). This package had its own, with comments saying
// they copied 'the same shape as the Instant On connector'. Three identical
// definitions by convention hold until one gains a field: on that day, the API's
// silent conversion drops that field without any test noticing, and a
// configuration drift becomes invisible.
//
// An ALIAS (`type Step = driver.Step`) and not a definition: the [Client]'s
// `Steps` field, pure functions, and tests keep compiling unchanged.
//
// # THIS PACKAGE'S RICH TYPES SURVIVE, UNDER ANOTHER NAME
//
// This is the only shape difference from the Instant On connector stitching, and
// it comes from a fact of this platform: three of its types carried MORE than
// the contract, because RouterOS needs more to write without mistakes.
//
//	State     → [DeviceState]     keeps `BridgeName`, `VLANFiltering`,
//	                              `MgmtInterface`, `MgmtAddress`, `CPUTagged`
//	VLAN      → [BridgeVLAN]      keeps `Dynamic` and the record `.id`
//	PortState → [BridgePort]      keeps `Untagged`, `Bridge`, `HWOffload`, the `.id`
//	Identity  → [DeviceIdentity]  keeps RouterBOOT, architecture, `max-l2mtu`, ports
//
// None of these fields could enter the contract: they are RouterOS `.id` values,
// menu names, and platform facts — exactly what state.go says it does not want
// there. And none could DISAPPEAR: without `CPUTagged`, apply does not know to
// preserve the pseudo-port `bridge`, which cuts off administration without
// stopping site traffic; without `Dynamic`, it tries to modify an entry the
// device rebuilds at will; without `Untagged`, it cannot see a port tagging in
// a VLAN it is not a member of — the real anomaly on the reference device.
//
// Reading is therefore in TWO steps, and that is the right way to say it:
// [Client.ReadDeviceState] returns everything RouterOS knows about itself,
// [Client.ReadState] returns the part the contract carries; what is lost in
// transit is stated plainly in [State.Notes]. The conversion is explicit and
// lives in one function ([DeviceState.Standard]) instead of an implicit cast
// nobody re-reads.
type (
	// Step is a transcript step. The [Client]'s `Steps` field becomes a
	// `[]driver.Step` without changing a line in this package.
	Step = driver.Step
	// Candidate is a secret to try, with the reason for its position in the order.
	// The order comes from the caller and is not re-sorted.
	Candidate = driver.Candidate

	// VLAN, PortState, and State are the CONTRACT view. This package's rich views
	// are [BridgeVLAN], [BridgePort], and [DeviceState].
	VLAN      = driver.VLAN
	PortState = driver.PortState
	State     = driver.State
	// PortPlan aggregates what an apply must achieve. It replaces the four
	// parameters [Client.ApplyPorts] took: the day apply needs one more
	// constraint, all three connectors change together or not at all.
	PortPlan    = driver.PortPlan
	ApplyResult = driver.ApplyResult

	SNMPIdentity    = driver.SNMPIdentity
	SNMPResult      = driver.SNMPResult
	TrapDestination = driver.TrapDestination
	TrapResult      = driver.TrapResult

	// Identity is what the contract carries from an identification. This
	// package's rich view is [DeviceIdentity], which keeps what RouterOS publishes
	// in addition — including the RouterBOOT version, never to be confused with
	// the RouterOS version.
	Identity = driver.Identity
)

// What will FAIL COMPILATION the day the contract moves.
//
// A runtime type assertion (`admin.(driver.SNMPConfigurator)`) would fail
// silently, in the field: the caller would fall back to its message 'this
// platform cannot do that' for a capability this connector does have, and
// nobody would know a signature had drifted. These declarations cost zero bytes
// at runtime and turn that silent failure into a build error.
//
// The list is also the DECLARATION of what this platform can do. What is not
// listed is explained at the end of the file: [driver.Persister] and
// [driver.Onboarder] are ABSENT, each for its own reason. [driver.MgmtMover]
// and [driver.MgmtAddrWriter] are asserted in mgmtwrite.go.
var (
	_ driver.Connector  = (*Driver)(nil)
	_ driver.Identifier = (*Driver)(nil)

	_ driver.Admin            = (*Client)(nil)
	_ driver.AccountManager   = (*Client)(nil)
	_ driver.SNMPConfigurator = (*Client)(nil)
	_ driver.TrapPusher       = (*Client)(nil)
	_ driver.RawReader        = (*Client)(nil)
)

// --- The connector ---

// Authenticate returns PROVEN administration access to this device.
//
// ONE CALL ONLY, because on this platform there is NOTHING to open: RouterOS REST
// authenticates with HTTP Basic re-presented on every request (OBSERVED: without
// credentials, `/rest/system/identity` returns 401 with `WWW-Authenticate:
// Basic`). There is no cookie to keep, no session to close — hence the absence of
// `Close` in the contract, and hence 'proving' can mean only one thing here:
// SUCCEED AT ONE AUTHENTICATED READ.
//
// THE CHOSEN READ IS `/system/identity`, the cheapest that requires
// authentication — OBSERVED, 17 bytes of body (`{"name":"lan-r4"}`), 30 ms
// median through the proxy tunnel. Same choice as [VerifyLogin], for the same
// reason: a write return code says the device accepted an account, not that it
// WILL accept it on the next connection. The only test that matters is using it.
//
// WHAT IT ACTUALLY COSTS THE DEVICE, and it must be said because it is not what
// one assumes: TWO HTTP requests per candidate, not one. The project's shared
// transport (internal/driver/webfetch.go, `WebSession.do`) does not send the
// `Authorization` header upfront — it emits the bare request, READS the 401
// challenge, then replays once. That is correct behavior for a family of devices
// whose authentication scheme is not assumed, but on this platform where it is
// known, it doubles the request count. OBSERVED to support the measurement: a
// request without credentials returns a 401 WITH `WWW-Authenticate: Basic`, a
// request with a wrong secret returns a 401 that does not carry it — both are
// therefore distinguishable, and the replay happens once and only once.
//
// THE NUMBER OF ATTEMPTS IS A BUDGET, not a convenience. Two consequences held
// here:
//
//  1. NO candidate is replayed or fabricated. A (account, secret) pair already
//     tried is SKIPPED if it reappears in the list — spending it twice empties
//     the budget without learning anything, and the caller already caps at two
//     tries per account because these ranges lock accounts;
//  2. ONE web session is opened for the whole loop, and its `Authorization` is
//     repointed from one candidate to the next. OBSERVED during the exploration
//     campaign: `/ip/service` listed up to TWELVE simultaneous dynamic `www`
//     connections from the proxy alone while only one read at a time was
//     requested — connections accumulate faster than they close. Opening one
//     client per candidate would waste what the 20 OBSERVED sessions do not
//     invite wasting.
//
// TWO EARLY STOPS, both deliberate, because continuing could only make things
// worse:
//
//   - the device DOES NOT RESPOND (transport error, no HTTP code). Trying further
//     candidates against a refused TCP proves nothing and muddies diagnosis: 'no
//     credential accepted' and 'the device does not respond' are fixed by
//     unrelated actions, and the message must decide;
//   - the device responds 403. This is the nastiest case on this platform, and it
//     must be read correctly: 403 means the SECRET IS GOOD and the account's
//     GROUP does not carry the `rest-api` policy. Continuing would try other
//     secrets when the right one was just found, spending the budget for nothing
//     while hiding the real cause.
//
// ACCESS IS RETURNED EVEN ON FAILURE, as the Instant On connector does and for
// the same reason: the transcript is the only way to understand a refusal
// without returning on site, and the caller displays it on all failure paths.
// Returning nil would erase that trace at the moment it matters. Such access is
// harmless — [Client.Account] and [Client.Accepted] stay EMPTY, and any request
// made through it would return 401 again.
//
// `tgt.PortPrefix` IS NOT READ, and especially not stored on the client. On this
// platform meshcraft names ARE device names (`ether3`, `sfp-sfpplus1`), so the
// front panel carries several prefixes and none summarizes it: taking one for
// all ports would write to the wrong port and cut off a client. `tgt.Model` is
// not read either — this connector READS the model on the device rather than
// receiving it (cf. [Client.Identify]).
func (d *Driver) Authenticate(ctx context.Context, tgt driver.Target,
	cands []Candidate) (driver.Admin, error) {
	// One client, one `Authorization` repointed: cf. the budget section.
	// Opening without credentials is deliberate — the loop sets them, and a client
	// never authenticated must remain an account-less client.
	var c = Open(tgt.Host, tgt.Dialer, "", "")

	if len(cands) == 0 {
		c.step(false, "no credentials to try",
			"RouterOS REST responds 401 on ALL its resources: without "+
				"credentials, there is nothing to read on this device")
		return c, fmt.Errorf("no credentials to try on %s: RouterOS REST "+
			"refuses all its resources without authentication — store a credential "+
			"for this device, or the factory `admin` account",
			tgt.Host)
	}

	var tried = map[string]bool{}
	var refusals []string
	var cand Candidate
	for _, cand = range cands {
		// The whole pair is the key: the same account with two secrets is two
		// attempts; the same pair twice is only one.
		var key = cand.Username + "\x00" + cand.Secret
		if tried[key] {
			c.step(true, "duplicate candidate skipped: '"+cand.Why+"'",
				"the same account/secret pair was already presented — replaying it "+
					"would empty the attempt budget without learning anything")
			continue
		}
		tried[key] = true

		c.web.SetAuth(driver.AuthBasic, cand.Username, cand.Secret)
		var status, err = c.authProbe(ctx)
		if err == nil {
			// Account and candidate are set ONLY HERE, the one place that knows
			// first-hand the device accepted them.
			c.User = cand.Username
			c.accepted = cand
			c.step(true, "connected as '"+cand.Username+"' ("+cand.Why+")",
				"authenticated read of /rest/system/identity succeeded")
			return c, nil
		}

		switch status {
		case 0:
			// No HTTP code: tunnel, route, or web service. Insisting on further
			// candidates would say nothing more and would hide the cause.
			return c, fmt.Errorf("device %s does not respond on the REST API "+
				"(http, port 80): %w — credentials could not be proven, "+
				"no attempt was consumed beyond the first", tgt.Host, err)
		case 403:
			// The secret IS good. See the early-stop section.
			return c, fmt.Errorf("account '%s' (%s) is ACCEPTED by %s but its "+
				"group does not carry the `rest-api` policy: meshcraft cannot read "+
				"anything despite a valid secret. Fix on the device, in `/user/group` — "+
				"the three factory groups (`read`, `write`, `full`) carry it; a custom "+
				"group may not. Other candidates were not tried: the right secret was "+
				"just found",
				cand.Username, cand.Why, tgt.Host)
		}
		refusals = append(refusals, fmt.Sprintf("'%s' (%s): %s",
			cand.Username, cand.Why, err.Error()))
	}

	return c, fmt.Errorf("no credential accepted by %s after %d attempt(s) — %s",
		tgt.Host, len(tried), strings.Join(refusals, " ; "))
}

// authProbe is the sole read that proves a candidate, and it returns the CODE as
// well as the error.
//
// The code is what lets [Driver.Authenticate] distinguish three situations
// nothing else separates: a refused secret (401), a good secret in a powerless
// group (403), and a device that does not respond (0, no HTTP response).
// Confusing them would exhaust the attempt budget on a cut tunnel, or hunt for a
// password when the problem is a group policy.
//
// It DELIBERATELY bypasses [Client.List]: List logs a 'read refused' to the
// transcript for every candidate that fails, which would drown the one line that
// matters — the candidate that got in. The authentication transcript is written
// by Authenticate, with each candidate's `Why`, because that is what the
// operator re-reads.
func (c *Client) authProbe(ctx context.Context) (int, error) {
	var path = menuPath("system/identity")
	var resp, err = c.web.Get(ctx, path)
	if err != nil {
		return 0, err
	}
	if resp.Status != 200 {
		var re restError
		json.Unmarshal([]byte(resp.Body), &re) //nolint:errcheck // fallback message below
		return resp.Status, fmt.Errorf("HTTP %d — %s", resp.Status,
			restStatusMeaning(resp.Status, re))
	}
	return resp.Status, nil
}

// WritesExtra reports whether this connector applies the named intent, outside
// VLAN membership.
//
// On [Driver] and not [Client]: the answer depends on neither a session nor a
// model, and the caller needs it BEFORE reaching the device — what the platform
// cannot write must be announced even when apply fails.
//
// Always FALSE here, and the reason is more uncomfortable than on the Instant On
// range: it is not just missing code, it is missing VERIFICATION. No write from
// this connector could be exercised on hardware (channel restricted to GET/HEAD).
// Declaring intents whose code never ran would promise the same thing twice. The
// detail of what the platform WOULD do, menu by menu, is in capabilities.go.
func (d *Driver) WritesExtra(name string) bool { return WritesExtra(name) }

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Client)(nil))
}

// --- Administration access ---

// Connector returns the identity of the connector that produced this access.
//
// For MESSAGES: 'this platform cannot declare an SNMPv3 user' helps nobody if
// the screen does not say which platform. Also keeps the caller from carrying
// the connector name alongside the access — two variables that would eventually
// refer to two different devices.
func (c *Client) Connector() driver.Info { return (&Driver{}).Info() }

// Account is the account that ACTUALLY opened the access — not the one that was
// wanted.
//
// The difference decides the rest of provisioning: same account, change its
// secret; different account, that is a RENAME, and the new one must be created
// before removing the old. EMPTY when no candidate got in, which is the case for
// access returned with an error (cf. [Driver.Authenticate]).
func (c *Client) Account() string { return c.User }

// Accepted is the candidate that worked, as supplied.
//
// Returned by the contract because the caller needs it: the device's CURRENT
// secret is the one that just got in. This replaces a reconstruction from the
// TRANSCRIPT the API layer used — find a step whose label starts with 'connected
// as', then cross-check its `Why` against the candidate list. A transcript
// label is text for a human: rewording it is legitimate, with no apparent
// consequence, and silently broke credential apply. That coupling need not
// survive the contract, and the transcript line written by [Driver.Authenticate]
// is free to change wording.
func (c *Client) Accepted() Candidate { return c.accepted }

// Transcript is the trace of everything this access did, in order.
//
// Named `Transcript` and not `Steps` by the contract, for a prosaic reason that
// holds for all three connectors: they all expose a `Steps` FIELD, and a Go type
// cannot carry a field and a method of the same name. This name lets the
// contract be adopted by addition, without renaming a field this package's tests
// read.
func (c *Client) Transcript() []Step { return c.Steps }

// --- Raw read, reconnaissance tool ---

// ReadRaw returns the contents of the named unit. On this platform, a unit is a
// REST MENU PATH — 'interface/bridge/vlan', 'snmp/community', 'user'.
//
// READ-ONLY, and a RECONNAISSANCE tool rather than a product feature: on an
// undocumented API, one cannot WRITE a menu until its real field set has been
// seen. This package carries the proof — the `max-l2mtu` frame ceiling had been
// declared 'not published' from enumerating the 87 fields of
// `/interface/ethernet` alone, while `/interface` serves it. Replaying a capture
// showed the contradiction.
//
// THE NAME IS SANITIZED, by [menuPath], the same gate as all other reads: it
// goes into a URL, and a `?` or `..` segment would divert the request to another
// device endpoint — `net/http` normalizing the path before sending, '
// interface/../../etc' would become '/etc'.
//
// WHAT IS RETURNED IS THE DECODED RECORD SET, RE-ENCODED AS INDENTED JSON,
// not the response bytes verbatim. That is a choice, not a convenience:
//
//   - going through [Client.List] gives this read everything this connector
//     learned about this API, especially the meaning of return codes — 'a 500
//     does NOT distinguish an absent menu from a menu that contains only
//     sub-menus; try a sub-path before concluding'. On a reconnaissance tool,
//     that sentence is what matters, not the bytes;
//   - what reconnaissance seeks is the FIELD SET, and it is fully preserved:
//     [flatten] keeps even values that are not strings, in their JSON form.
func (c *Client) ReadRaw(ctx context.Context, unit string) (string, error) {
	var rows, err = c.List(ctx, unit)
	if err != nil {
		return "", err
	}
	var out []byte
	out, err = json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// RawKind names, for a human, what 'a unit' means here.
//
// For the UI, and not ornament: without it, the debug probe would ask for a
// section name `wcd` from someone looking at a MikroTik, and failure would
// look like a device fault.
func (c *Client) RawKind() string { return "REST menu" }

// --- WHAT THIS CONNECTOR DOES NOT IMPLEMENT, AND WHY ---
//
// Three optional contract interfaces are deliberately absent. Each is absent for
// its own reason, and the absence lets the caller refuse BEFORE starting — so it
// does not offer a button that leads nowhere (cf. [driver.NotSupported]).
//
// # [driver.Persister]: NOTHING TO SAVE, AND THEREFORE NO SAFETY NET
//
// RouterOS is [driver.CommitImmediate]: configuration is in non-volatile memory
// the moment the device responds. There is no candidate configuration, no 'make
// durable' step, and no native commit-confirmed — RouterOS's is tied to an
// interactive session, out of reach of an API re-authenticated on every
// request.
//
// Implementing it would therefore be WORSE than abstaining: `Persist` could only
// be empty, and an empty `Persist` suggests there was something to confirm, hence
// that a power cut could have undone it. Not implementing it is exact INFORMATION:
// on this platform, a write error is already committed. The only recourse that
// exists is elsewhere — serial console and MAC-winbox (both OBSERVED), and the
// undo journal `/system/history`, whose entries carry `undoable: true` and the
// inverse command in plain text. It is READABLE from REST; triggering it has not
// been exercised, so it is not offered.
//
// # [driver.Onboarder]: NOTHING TO SWITCH
//
// A factory-fresh Instant On waits for its vendor cloud portal and must be
// steered away from it. RouterOS has no equivalent: it is manageable from
// unboxing, via web, SSH, Winbox, MAC-winbox, and serial console. There is no
// management mode to choose, so nothing this interface could do.
//
// # [driver.MgmtMover]: DUAL-PATH PREPARE / ADDRESSED / COMMIT
//
// Implemented on [*Client]. Sequence matches Capabilities().Mgmt.Notes:
// `/interface/bridge/vlan` (uplink AND the bridge CPU name in `tagged`), then
// `/interface/vlan` on the bridge, then a DHCP client (or later a static
// address), then verification via [MgmtMover.MgmtAddressed], then ONLY THEN
// [MgmtMover.CommitMgmt] removes addressing on the old interface.
//
// Same-VLAN DHCP/static/gateway/DNS go through [driver.MgmtAddrWriter]
// ([Client.ApplyMgmtAddressing]) — not through this interface.
//
// REST writes use the same Add/Set/Remove channel as port apply. Caps
// ApplySameVLAN and MigrateVLAN are true; OutOfBand (serial / MAC-winbox) is
// the recovery net when an addressing change cuts the current session.

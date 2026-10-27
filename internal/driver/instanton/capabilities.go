package instanton

import "meshcraft/internal/driver"

// What this connector can WRITE, and especially what it cannot.
//
// Declared HERE, in the package that writes, not in the API layer: the day a
// new section becomes writable, the declaration and the code that writes it
// change in the same directory. A capability declared far from the write path
// always ends up claiming something other than what actually happens — and a
// capability that lies is worse than no capability at all, because it gives
// the operator configuration they believe is in place.
//
// Purpose: the meshcraft UI offers intents this connector does not apply (PoE
// on a port, for example). Before, they were saved then silently forgotten —
// apply reported success and the screen showed configuration that existed
// nowhere. Rather than block the operator or lie, we APPLY everything
// supported, consider the device up to date for that scope, and SAY what was
// left behind.

// WritableExtras: intents OTHER than VLAN membership that this connector
// actually applies (cf. recon.ExtraIntents for the list of intents the model
// can carry).
//
//	stp → ApplySpanningTree: SpanningTreeGlobalParam (enabled +
//	      STPOperationMode 0/2/3) and STP.GlobalSetting / InterfaceList.
//	      Section names proven on lab JL685A (sw-labo-1930) via the product
//	      UI bundle (pages1.js STP.initWcd).
//
// # `port_admin` — declared 2026-08-06, and the FIELD is probed, not guessed
//
// [Admin.ApplyPortAdmin] writes an administrative field inside
// `Standard802_3List`. Declared on an operator decision, and the distinction that
// makes it declarable on THIS range — the one with no SSH, no Telnet, no serial
// port and a reset button for a recovery — is where the uncertainty sits:
//
//   - THE SECTION IS CAPTURED. `Standard802_3List` is the per-port section this
//     connector has been reading for link state since it was written, on the lab
//     JL685A (sw-labo-1930, firmware 3.4.0.0). Nothing here asks the device for a
//     section that may not exist, which is the mistake admin.go was caught by
//     (`UpdatePassword`, answering `statusCode -1`);
//   - THE FIELD IS CAPTURED TOO, since 2026-08-11: the lab 1930 serves
//     `<adminState>1</adminState>` on every port of that section, in this
//     framework's `_TRUE='1'` / `_FALSE='2'` spelling, and the product's own UI
//     writes it back through the same `<Entry>` wrapper. `adminState` therefore
//     leads [adminFieldCandidates];
//   - THE FIELD IS STILL PROBED rather than assumed, and probed from the device's
//     OWN response rather than from a manual. The applier reads the section first
//     and will use only a field the device ITSELF printed; the value vocabulary is
//     derived from a port that CURRENTLY HAS A LINK, since a live link cannot exist
//     on an administratively shut port. One firmware serving `adminState` is not
//     every firmware serving it.
//
// THAT IS WHY THIS IS NOT THE HOSTNAME CASE. There the section name itself was
// unverified, so a write went nowhere in a way the framework reports as success.
// Here, ON A FIRMWARE SERVING NO KNOWN ADMINISTRATIVE FIELD THE GESTURE REFUSES:
// nothing is POSTed, every port in the plan comes back in
// [driver.ApplyResult.Skipped], and the refusal NAMES THE CANDIDATES IT LOOKED FOR.
// That named list is the survey — it turns the open question into a line of text an
// operator can read off a screen and send back, which is what closes the TODO.md
// item rather than another lab session.
//
// The residual risk, stated plainly: if this firmware publishes a field whose name
// is in the candidate list but whose MEANING is not administrative state, the write
// lands in the wrong field. `linkStateTrapEnabled` is excluded BY NAME for exactly
// that reason (portadmin.go says why: writing it would leave the port forwarding AND
// silence the traps that would have shown the operator it had not moved), and the
// post-write re-read refuses to report a port as moved on a `statusCode 0`. The
// write is also deliberately not saved to flash, which on this range is the
// strongest recovery available: a power cycle of the switch restores every port
// these writes touched.
var WritableExtras = map[string]bool{
	"stp": true,
	// Administrative state: an administrative field of `Standard802_3List`, applied
	// by ApplyPortAdmin. It REFUSES rather than guesses when the device publishes no
	// field it recognizes — cf. the section above, and rule 1 in portadmin.go.
	"port_admin": true,
}

// The READ counterpart of extras not applied here is in
// Capabilities().Monitoring.Unavailable (instanton.go): what the product line
// does not expose for monitoring, and why. It lives there because it must be
// readable from driver.Capabilities for a given model, whereas WritableExtras
// is consulted only at apply time. Same principle in both cases — absence is
// declared by the package that KNOWS, not by the API layer.

// WritesExtra reports whether this connector applies the named intent.
func WritesExtra(name string) bool { return WritableExtras[name] }

// WritesExtra on the CONNECTOR: this is the form the contract requires
// (driver.Connector), and it returns the same answer as the function above —
// which the API layer still uses until it is rewired.
//
// On the connector, not the session: the answer does not depend on a session or
// model, and the caller needs it BEFORE reaching the device. What the platform
// cannot write must be announced even when apply fails — that is what
// `api.unsupportedNotes` does, surfacing it before the first packet.
func (d *Driver1930) WritesExtra(name string) bool { return WritesExtra(name) }

// Supports: the optional gestures of this platform, computed from the method
// sets and never listed by hand — see driver.GesturesOf.
func (d *Driver1930) Supports() []driver.Gesture {
	return driver.GesturesOf(d, (*Admin)(nil))
}

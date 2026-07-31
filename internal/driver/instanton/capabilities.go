package instanton

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
// Empty, and that is not an oversight. This product line's web UI exposes PoE
// and spanning-tree, but meshcraft does not write them yet: the only thing this
// connector applies today is port VLAN membership. This emptiness shows on
// screen, instead of being discovered in the field.
var WritableExtras = map[string]bool{}

// The READ counterpart of this emptiness is not here but in
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

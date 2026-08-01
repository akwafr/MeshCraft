package routeros

// What this connector can WRITE, and especially what it cannot.
//
// Declared HERE, in the package that writes, not in the API layer: the day a new
// intent becomes writable, the declaration and the code that writes it change in the
// same directory. A capability declared far from the write path always ends up claiming
// something other than what actually happens — and a capability that lies is worse than
// no capability at all, because it gives the operator configuration they believe is in
// place.
//
// Purpose: the meshcraft UI offers intents this connector does not apply. Without this
// declaration, they would be saved then silently forgotten — apply would report success
// and the screen would show configuration that exists nowhere. Rather than block the
// operator or lie, we APPLY everything supported, consider the device up to date for
// that scope, and SAY what was left behind.

// WritableExtras: intents OTHER than VLAN membership that this connector actually
// applies (cf. recon.ExtraIntents for the list the model can carry:
// `poe`, `stp`, `dhcp_snooping`, `port_security`).
//
//	stp            → ApplySpanningTree: `/interface/bridge` `protocol-mode`
//	                 (none|stp|rstp|mstp) and `priority`; `/interface/bridge/port`
//	                 `edge` and `path-cost`. Same REST Set path already used for
//	                 vlan-filtering on the bridge.
//	poe            → NOT APPLICABLE on this model: menu `/interface/ethernet/poe`
//	                 does not exist (observed, cf. Capabilities).
//	dhcp_snooping  → `/interface/bridge` carries `dhcp-snooping`, and
//	                 `/interface/bridge/port` carries `trusted` — not applied yet.
//	port_security  → NO per-port equivalent on this platform (observed:
//	                 `max-learned-entries` exists on the bridge, not on its ports).
var WritableExtras = map[string]bool{
	"stp": true,
}

// The READ counterpart of this emptiness is not here but in
// Capabilities().Monitoring.Unavailable (routeros.go): what the platform does not expose
// for monitoring, and why. It lives there because it must be readable from
// driver.Capabilities for a given model, whereas WritableExtras is consulted only at
// apply time. Same principle in both cases — absence is declared by the package that
// KNOWS, not by the API layer.

// WritesExtra reports whether this connector applies the named intent.
func WritesExtra(name string) bool { return WritableExtras[name] }

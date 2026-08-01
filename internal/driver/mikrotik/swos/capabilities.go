package swos

// What this connector can WRITE, and especially what it cannot.
//
// Declared HERE, in the package that writes, not in the API layer: the day a
// new intent becomes writable, the declaration and the code that writes it
// change in the same directory. A capability declared far from the write path
// always ends up claiming something other than what actually happens — and a
// capability that lies is worse than no capability at all, because it gives
// the operator configuration they believe is applied.
//
// Same mechanism and reason as instanton.WritableExtras: meshcraft APPLIES
// everything supported, considers the device up to date for that scope, and
// SAYS what was left behind.

// WritableExtras: intents OTHER than VLAN membership that this connector
// actually applies (cf. recon.ExtraIntents for what the model can carry:
// `poe`, `stp`, `dhcp_snooping`, `port_security`).
//
// `stp` is applied via ApplySpanningTree (/rstp.b `ena`, /sys.b `prio`). The other
// extras remain empty on purpose: DHCP snooping and port security still share
// pages that also carry management allow-from fields, and those writes are not
// proven on hardware yet.
var WritableExtras = map[string]bool{
	// Spanning-tree: /rstp.b `ena` (+ optional edge) and /sys.b `prio`, applied by
	// ApplySpanningTree. Proven writable fields come from backup.swb.
	"stp": true,
}

// The READ counterpart of this emptiness is not here but in
// Capabilities().Monitoring.Unavailable (swos.go): what the platform does not
// expose for monitoring, and why. It lives there because it must be readable
// from driver.Capabilities for a given model, whereas WritableExtras is
// consulted only at apply time.
//
// There is a third counterpart, specific to this platform, that fits neither
// contract field: the membership REMOVAL policy. Apply now removes memberships
// the plan no longer asks for, with four refusals that protect the management
// VLAN, the effective native VLAN, ports in "all", and active ports on a device
// whose administration VLAN is unknown. Each reason is written at length in
// apply.go (removalPolicy, planLeaves), next to the code that decides.
//
// There is a FOURTH counterpart, and it is the only remaining write refusal on
// this platform's VLAN model: neither "VLAN Receive" (/fwd.b `vlni`) nor "Force
// VLAN ID" (/fwd.b `fvid`) are written, even though both are writable and are
// already echoed back to the device on every apply with their read value. The
// reason is in ports.go (receivePolicy): tightening them DROPS traffic, and the
// standard model does not express what would be needed to decide which. They
// are READ, however, and flagged when they contradict the port role
// (vlanAnomalies).

// WritesExtra reports whether this connector applies the named intent.
func WritesExtra(name string) bool { return WritableExtras[name] }

// RemovalPolicy returns, in one sentence, what apply removes and what it
// refuses to remove. Meant for the UI: the operator must be able to read the
// limit without opening the code.
func RemovalPolicy() string { return removalPolicy() }

// ReceivePolicy returns, in one sentence, the two per-port VLAN fields this
// connector never writes, and why.
//
// Exported for the same reason as RemovalPolicy — a third form of the same
// principle: what we do not do must be readable from outside the package.
// `WritableExtras` names intents not applied, `Monitoring.Unavailable` names
// missing metrics, and these two sentences say what apply forbids INSIDE an
// intent it otherwise honors — a nuance no contract field carries.
func ReceivePolicy() string { return receivePolicy() }

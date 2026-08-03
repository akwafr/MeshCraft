package driver

import "context"

// Management addressing EXACTLY AS THE DEVICE CARRIES IT, and the interface that
// reads it.
//
// # Why this isn't in [State]
//
// [State] is serialized into `devices.observed` and serves the port-drift
// calculation (cf. state.go). Two reasons of a different nature keep addressing
// separate from it:
//
//   - a platform fact: management addressing isn't read from the same place as
//     port configuration — /sys.b versus /vlan.b and /fwd.b on SwOS, `ip/address`
//     versus `interface/bridge/vlan` on RouterOS. One read can succeed while the
//     other fails, and merging them into a single struct would make a failure in
//     one look like the absence of the other;
//   - a product fact: what goes into `observed` is compared against intent and
//     triggers writes. Management addressing, on the other hand, is NEVER applied
//     through the port-reconciliation path (cf. docs/01-Architecture/Adressage de
//     management §5, "management is never touched through the ports path").
//
// # What this type is meant to decide
//
// A single thing, and a small one: display the truth. A device with a static
// address and a device on a DHCP lease look identical in an inventory that only
// observes the address — and yet what happens the day the DHCP server changes has
// nothing in common between the two. It is also what lets meshcraft PRESERVE a
// static configuration found in place rather than treating it as drift from the
// site plan.

// MgmtAddrState is the management addressing observed on a device.
type MgmtAddrState struct {
	// Mode: "static", "dhcp", or EMPTY when the platform doesn't say.
	//
	// Empty is a value in its own right, and it is not replaced by a default.
	// "We don't know how this device got its address" is an accurate answer;
	// "it's on DHCP because the site plan says DHCP" is a deduction that will
	// turn out false on the fleet's first static device — and there are some.
	Mode string `json:"mode,omitempty"`
	// Address carries the PREFIX when the platform provides one
	// ("10.99.0.36/24"), and the bare address otherwise. What the connector
	// actually read, never what it deduced from it.
	Address string `json:"address,omitempty"`
	// Gateway / DNS: empty when the platform doesn't carry them.
	//
	// An empty value therefore can't be read on its own: it's
	// [MgmtCapabilities.Gateway] and [MgmtCapabilities.DNS] that say whether to
	// read it as "not configured" or "this platform has no such setting." The
	// two lead to opposite actions, and a UI that confused them would send
	// someone looking for a field that doesn't exist.
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	// Interface: the name the device gives to the interface that carries this
	// address ("vlan99", "VLAN1"). Empty on platforms that don't name one —
	// a SwOS switch has no interface concept and attaches the address to
	// nothing.
	Interface string `json:"interface,omitempty"`
	// Notes carries what the connector understood that the fields above can't
	// express: a second address found, a mode read as an unknown value, a
	// platform limitation. Written for an operator and displayed as-is, like
	// [State.Notes].
	Notes []string `json:"notes,omitempty"`
}

// Values for [MgmtAddrState.Mode].
const (
	// MgmtAddrModeStatic: the address is configured by hand on the device.
	MgmtAddrModeStatic = "static"
	// MgmtAddrModeDHCP: the address comes from a DHCP lease.
	MgmtAddrModeDHCP = "dhcp"
)

// MgmtAddrReader: the connector can say HOW the device obtained its
// administration address.
//
// OPTIONAL, and READ ONLY. A connector that doesn't implement it isn't behind the
// others: it's saying that, on this platform, it has no way to observe anything
// that would distinguish a static address from a lease. The caller learns this
// BEFORE offering a UI (cf. [NotSupported]), and displays "addressing mode not
// observable on this platform" instead of a choice resting on nothing.
//
// Read-only. Writes go through [MgmtAddrWriter] (same VLAN), [MgmtMover] (dual-path
// VLAN migrate), or [MgmtJumper] (single-address leap) — never through this reader.
type MgmtAddrReader interface {
	ReadMgmtAddressing(ctx context.Context) (MgmtAddrState, error)
}

// AsMgmtAddrReader returns the admin as an addressing reader when it is one.
func AsMgmtAddrReader(a Admin) (MgmtAddrReader, bool) {
	var r, ok = a.(MgmtAddrReader)
	return r, ok
}

// MgmtAddrIntent is what meshcraft wants to APPLY on the management interface,
// WITHOUT changing management VLAN.
//
// Distinct from [MgmtMover] (VLAN migrate with dual path) and from [MgmtJumper]
// (single-address leap). Same-VLAN apply still moves the address under the
// commanding session — connectors must prove dual-address or accept an honest
// jump with backup before setting [MgmtCapabilities.ApplySameVLAN].
type MgmtAddrIntent struct {
	// Mode: [MgmtAddrModeStatic] or [MgmtAddrModeDHCP]. Inherit is never applied.
	Mode string `json:"mode"`
	// Address carries PREFIX when static ("10.99.0.36/24"). Empty for DHCP.
	Address string   `json:"address,omitempty"`
	Gateway string   `json:"gateway,omitempty"`
	DNS     []string `json:"dns,omitempty"`
}

// MgmtAddrResult is what apply left on the device, or refused to do.
type MgmtAddrResult struct {
	// Observed is a re-read AFTER write when the connector could obtain one.
	Observed MgmtAddrState `json:"observed,omitempty"`
	// Notes carry platform facts the caller must surface (partial apply, etc.).
	Notes []string `json:"notes,omitempty"`
	// Backup is a pre-write copy when the platform offers one (SwOS). Empty is
	// normal on dual-path platforms that never needed it for this gesture.
	Backup []byte `json:"-"`
}

// MgmtAddrWriter applies management addressing on the CURRENT management VLAN.
//
// FACULTATIVE. Must NOT change management VLAN — that is [MgmtMover] or
// [MgmtJumper]. Declaring this interface without having exercised the writes on
// hardware is forbidden: flip [MgmtCapabilities.ApplySameVLAN] only after proof.
type MgmtAddrWriter interface {
	ApplyMgmtAddressing(ctx context.Context, want MgmtAddrIntent) (MgmtAddrResult, error)
}

// AsMgmtAddrWriter returns the admin as a same-VLAN addressing writer when it is one.
func AsMgmtAddrWriter(a Admin) (MgmtAddrWriter, bool) {
	var w, ok = a.(MgmtAddrWriter)
	return w, ok
}

package mdpv1

import "context"

// DHCP snooping: the switch inspects DHCP traffic and only lets SERVER replies
// through on the ports an operator declared as legitimate. Optional the same way
// PoE and spanning tree are (cf. poe.go, spanning.go) — read and write kept
// apart, nothing mixed into [PortState] or [State].
//
// # The per-port control is TRUST, and conflating it with "enable" is the trap
//
// Every screen that offers "DHCP snooping on this port" is really offering the
// TRUST flag, and the distinction is not pedantry — it is the difference between
// a protection that works and one that is merely configured:
//
//   - snooping is turned on GLOBALLY (one switch-wide statement) and then
//     per VLAN. A device with the global feature off inspects nothing, whatever
//     any port carries;
//   - a port is TRUSTED or UNTRUSTED. Untrusted is where the protection lives:
//     DHCP OFFER/ACK arriving there are dropped. Trusted is the exception —
//     the uplink toward the real DHCP server, or toward another switch.
//
// The consequence that decides this file's shape: the DEFAULT for every port on
// a device with snooping enabled is untrusted, and that default is the safe one.
// "Enabling snooping on a port" therefore has no meaning a connector could act
// on; "trusting this port" does.
//
// # Global and per-VLAN enable are the CONNECTOR's job, not the caller's
//
// A caller that asked for a port to be trusted on a device whose global snooping
// is off would get an exact configuration and zero protection — the worst of the
// two outcomes, because the screen would show a trust flag in place and nothing
// would say the feature above it is asleep.
//
// So [DHCPSnoopApplier] turns the feature on: globally, and on the VLANs the
// plan names. It does so as part of the same gesture, and it SAYS SO in
// [ApplyResult.Notes] — a device that arrives with snooping off and leaves with
// it on has been changed in a way that affects every port, not just the one that
// was clicked, and an operator who did not ask for that must read it afterwards.
//
// It never turns the feature OFF. Untrusting a port is a per-port write;
// disabling global snooping because the last trusted port went away would strip
// protection from the whole device on the strength of one click, and no screen
// asked for that.
//
// # What is NOT here
//
// The binding table (the leases snooping learned) is TELEMETRY, and a rich,
// fast-moving kind: it belongs to a monitoring path, not to a configuration
// contract that drift compares. Rate limiting, option-82 insertion, and ARP
// inspection are adjacent features that ride on snooping — a connector that
// observes them reports them in [DHCPSnoopState.Notes] rather than growing this
// type before a screen exists that would consume them.

// DHCPSnoopPortState is what a device reports about ONE port's snooping trust.
type DHCPSnoopPortState struct {
	// Port: facade name (GE12, ether3…), same vocabulary as State.Ports.
	Port string `json:"port"`
	// Trusted: DHCP server traffic is ACCEPTED on this port. False is the
	// protected state and the platforms' own default — a connector that could
	// not read the flag reports the port absent from [DHCPSnoopState.Ports]
	// rather than defaulting it to false here, because "untrusted" and "not
	// read" call for different things on screen.
	Trusted bool `json:"trusted"`
}

// DHCPSnoopState is what a device reports about DHCP snooping across the box.
//
// THE THREE LEVELS ARE ALL HERE, deliberately: a UI showing only the port flag
// would show a trust setting on a device that inspects nothing, which is exactly
// the failure mode the file header describes. A caller displaying a port's trust
// must be able to say "…on a device where snooping is off" in the same breath.
type DHCPSnoopState struct {
	// Enabled: the switch-wide feature is on. FALSE means every port's Trusted
	// flag is inert, whatever it says.
	Enabled bool `json:"enabled"`
	// VLANs: the VLANs snooping actually inspects, SORTED. Empty on a platform
	// that enables snooping globally without a per-VLAN dimension — a connector
	// says which of the two it is in Notes rather than leaving the caller to
	// read an empty list as "none".
	VLANs []int `json:"vlans,omitempty"`
	// Ports: one entry per port whose trust flag was READ. A port absent from
	// this list was not observed, which is not the same as untrusted.
	Ports []DHCPSnoopPortState `json:"ports,omitempty"`
	// Notes: what the connector understood that this type cannot express —
	// a platform with no per-VLAN dimension, an observed rate limit, ARP
	// inspection riding on the same tables. Sentences for an operator.
	Notes []string `json:"notes,omitempty"`
}

// DHCPSnoopReader reads DHCP snooping observation from a device.
//
// OPTIONAL. Platforms that do not carry the feature, or on which it has not been
// exercised, leave it unimplemented; callers use [AsDHCPSnoopReader] before
// offering the control at all.
type DHCPSnoopReader interface {
	ReadDHCPSnooping(ctx context.Context) (DHCPSnoopState, error)
}

// AsDHCPSnoopReader returns the reader when Admin implements it.
func AsDHCPSnoopReader(adm Admin) (DHCPSnoopReader, bool) {
	var r, ok = adm.(DHCPSnoopReader)
	return r, ok
}

// DHCPSnoopPortIntent is what meshcraft asks of ONE port.
//
// Trusted is EXPLICIT in both directions, unlike [PoEPortIntent.Class]'s empty
// form: there is no "platform default" to fall back on here, because the
// platform default IS untrusted and an intent that meant "leave it alone" would
// be indistinguishable from an intent that meant "protect it". A port the caller
// does not want to touch is simply absent from the plan.
type DHCPSnoopPortIntent struct {
	Port    string `json:"port"`
	Trusted bool   `json:"trusted"`
}

// SnoopIntentPorts names the ports of a list of intents, in the order they are
// held.
//
// It exists for one caller and one situation: [ApplyResult.Unconfirmed] takes
// names, connectors carry intents at the point where the verifying re-read
// fails, and six of them had grown the same three-line loop to bridge that gap
// — the shape that made "sent" and "confirmed" easy to conflate in the first
// place. Nothing about it is per-platform, so it lives here rather than nine
// times over.
func SnoopIntentPorts(in []DHCPSnoopPortIntent) []string {
	var out = make([]string, 0, len(in))
	var it DHCPSnoopPortIntent
	for _, it = range in {
		out = append(out, it.Port)
	}
	return out
}

// DHCPSnoopPlan is what meshcraft asks a connector to apply.
type DHCPSnoopPlan struct {
	// Ports: the trust flags to set. A port absent is NOT MANAGED, same rule as
	// [PortPlan.Desired].
	Ports []DHCPSnoopPortIntent `json:"ports,omitempty"`
	// VLANs: the VLANs snooping must cover — THE ONES THE PLAN'S PORTS CARRY.
	//
	// Not the site's whole list, which is what the API layer sent until
	// 2026-08-08. Two reasons, and the second is the one that bites:
	//
	//   - switching inspection on across every VLAN a site declares, because an
	//     operator trusted one uplink, is a device-wide change nobody asked for;
	//   - a port trusted while sitting OUTSIDE every inspected VLAN is a write
	//     some platforms accept and then ignore. On the lab EdgeSwitch it does
	//     not even appear in the trust table (cf. ubiquiti/edgeswitch's header),
	//     so the gesture reads back as a success and protects nothing.
	//
	// The caller derives them from the ports it names — untagged AND tagged, the
	// first for the access port a client sits behind, the second so a trusted
	// uplink is trusted on the traffic actually crossing it.
	//
	// The connector enables the feature on these (cf. the file header) on top of
	// whatever the device already had, and never removes a VLAN it finds there —
	// a VLAN someone else enabled snooping on is protection we did not install
	// and must not take away.
	//
	// Empty means nothing could be derived and the site declares no VLANs either.
	// The connector then turns on the global feature only, and says in
	// [ApplyResult.Notes] that snooping covers nothing yet: an operator reading
	// "trusted" on a port deserves to know the feature above it has no VLAN to
	// work on.
	VLANs []int `json:"vlans,omitempty"`
}

// DHCPSnoopApplier writes DHCP snooping configuration to a device.
//
// OPTIONAL. Same pattern as [PoEApplier] and [SpanningTreeApplier]: declare
// [Connector.WritesExtra]("dhcp_snooping") only once this is implemented AND the
// command forms have been proven on hardware. A connector whose forms are
// deduced from vendor documentation ships the code, leaves WritesExtra false,
// and names the unverified form in TODO.md — that is how PoE landed on
// EdgeSwitch and how the S3400 connector writes were staged.
//
// THE GLOBAL AND PER-VLAN ENABLE HAPPEN HERE, and are reported in
// [ApplyResult.Notes] (cf. the file header). A device left with the feature off
// after a successful-looking apply would be the one failure this contract exists
// to prevent.
//
// A port the platform refuses to trust — a stacking or uplink port some firmware
// excludes — is an ORDINARY REFUSAL, reported in [ApplyResult.Skipped], never a
// Go error and never silently dropped.
type DHCPSnoopApplier interface {
	ApplyDHCPSnooping(ctx context.Context, plan DHCPSnoopPlan) (ApplyResult, error)
}

// AsDHCPSnoopApplier returns the applier when Admin implements it.
func AsDHCPSnoopApplier(adm Admin) (DHCPSnoopApplier, bool) {
	var a, ok = adm.(DHCPSnoopApplier)
	return a, ok
}

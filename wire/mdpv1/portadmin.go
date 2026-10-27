package mdpv1

import (
	"context"
	"time"
)

// Bringing a port DOWN and back UP: the two gestures an operator reaches for
// when something on the other end of a cable has stopped answering, and the two
// most immediately destructive writes in this codebase.
//
// # Why these are not part of [Admin.ApplyPorts]
//
// ApplyPorts sets a CONFIGURATION that drift then compares against. These do
// not: shutting a port and bouncing its power are ACTIONS, taken now, whose
// whole value is that they are transient. Storing "this port should be shut" as
// an intent would make reconciliation re-shut it on every pass, and a power
// cycle has no steady state to store at all — it is over by the time the screen
// repaints.
//
// So nothing here lands in `devices.ports`, nothing here is diffed, and a caller
// invokes them from an explicit operator gesture — never from an automatic
// pipeline. The port's OBSERVED state moves as a result ([State.Links] reports
// [LinkDisabled] afterwards), which is exactly right: the device is telling us
// what happened, rather than meshcraft asserting what should be.
//
// # The guardrail is the same as ApplyPorts', and for a stronger reason
//
// [Admin.ApplyPorts]'s comment states the rule: "never cut off your own hand",
// and the safeguards live in the CONNECTOR. Disabling a port is the purest form
// of that hazard — on the Instant On and SwOS ranges, which have neither SSH nor
// a serial port, shutting the jack that carries the administration session ends
// the conversation permanently, and the recourse is a paperclip on site.
//
// A connector therefore REFUSES to disable a port that carries a link in the
// management VLAN, reports it in [ApplyResult.Skipped], and only proceeds when
// [PortAdminPlan.Force] says the operator accepts the trip. Re-ENABLING a port
// carries no such hazard and needs no such check.
//
// # Power cycle is PoE, and it is not the same gesture as a link bounce
//
// They get confused because both are described as "restart the thing on the
// other end", and they do opposite things:
//
//   - a LINK bounce (disable, wait, enable) drops the ethernet link. The device
//     at the far end keeps running; only its network goes away and comes back.
//     Right for a stuck negotiation or an err-disabled port awaiting a retry;
//   - a POWER cycle (PoE off, wait, PoE on) cuts the volts. The device at the
//     far end REBOOTS. Right for a hung camera or access point, and wrong —
//     expensively — for anything that was merely mis-negotiating.
//
// A UI that offered one button for both would sometimes reboot a building's
// worth of access points to fix a link light. They are two interfaces here for
// that reason, and a port that is not [PoEPortState.Capable] can only be offered
// the first.

// PortAdminIntent is the administrative state wanted on ONE port.
type PortAdminIntent struct {
	Port string `json:"port"`
	// Enabled: false shuts the port ("shutdown", "disable"), true brings it
	// back. There is no third value — a caller with nothing to say about a port
	// leaves it out of the plan.
	Enabled bool `json:"enabled"`
}

// PortAdminIntentPorts names the ports of a list of intents, in the order they
// are held. Sibling of [SnoopIntentPorts] and [StormIntentPorts], and it exists
// for the same one caller: [ApplyResult.Unconfirmed].
func PortAdminIntentPorts(in []PortAdminIntent) []string {
	var out = make([]string, 0, len(in))
	var it PortAdminIntent
	for _, it = range in {
		out = append(out, it.Port)
	}
	return out
}

// PortAdminPlan is what meshcraft asks a connector to set, NOW.
type PortAdminPlan struct {
	Ports []PortAdminIntent `json:"ports,omitempty"`
	// Force lifts the refusal to shut a port that carries administration.
	//
	// Same meaning and same rule as [PortPlan.Force]: never set by default, never
	// set by an automatic path, only ever by an operator who has been told what
	// they are about to cut. A connector that ignored it would make the UI's
	// confirmation meaningless; one that forced without being asked would leave
	// no safeguard at all.
	Force bool `json:"force,omitempty"`
}

// PortAdminApplier shuts and un-shuts ports.
//
// OPTIONAL. Declare [Connector.WritesExtra]("port_admin") alongside it, and only
// once the command forms are proven on hardware — the same staging rule as
// [PoEApplier] and [DHCPSnoopApplier].
//
// The result must name what was NOT done: a port spared by the administration
// guardrail goes in [ApplyResult.Skipped] with the reason, and a caller that
// receives an empty Skipped is entitled to believe every port in the plan moved.
type PortAdminApplier interface {
	ApplyPortAdmin(ctx context.Context, plan PortAdminPlan) (ApplyResult, error)
}

// AsPortAdminApplier returns the applier when Admin implements it.
func AsPortAdminApplier(adm Admin) (PortAdminApplier, bool) {
	var a, ok = adm.(PortAdminApplier)
	return a, ok
}

// DefaultPowerCycleOff is how long power stays off when the caller names no
// delay.
//
// Four seconds, and the number is not arbitrary: it is long enough that a PD's
// power supply actually collapses rather than riding through on its capacitors
// — the failure mode of a too-short cut is that nothing reboots and the operator
// concludes the button does not work — and short enough that an operator watching
// the panel does not decide the request hung.
const DefaultPowerCycleOff = 4 * time.Second

// PortCyclePlan is a PoE power cycle: cut the volts, wait, restore them.
type PortCyclePlan struct {
	// Ports: facade names. Every one of them must be PoE-capable; a port that is
	// not is an ORDINARY REFUSAL in [ApplyResult.Skipped] (cf. the file header —
	// there is nothing to cycle on a jack with no PSE hardware behind it).
	Ports []string `json:"ports,omitempty"`
	// OffFor is how long power stays down. Zero means [DefaultPowerCycleOff].
	//
	// The connector RESTORES the class the port was configured with, not a
	// default: a jack delivering passive 24V that came back at 802.3at would
	// damage the device it feeds, and coming back at nothing at all would turn a
	// reboot into an outage. Reading the class before cutting is therefore part
	// of the gesture, not an optimisation.
	OffFor time.Duration `json:"off_for,omitempty"`
}

// PortPowerCycler bounces PoE on a port so the powered device reboots.
//
// OPTIONAL, and NARROWER than [PoEApplier] implies: a connector can know how to
// set a PoE class without being able to promise the restore half of this gesture
// — and half a power cycle is an outage. Implement it only where the class can
// be read back and re-applied, and where that round trip has been exercised.
//
// # Declared under its own name, NOT under "poe"
//
// A connector announces this to the API layer as
// [Connector.WritesExtra]("port_power_cycle"), and gating it on "poe" instead
// was tried and was wrong: three connectors already declare "poe" (EdgeSwitch,
// Instant On, RouterOS) and only ONE implements this interface, so a screen
// keyed on "poe" offered a power-cycle button on two platforms that answer it
// with a refusal. That is precisely the "button that leads nowhere" the
// mandatory/optional boundary in connector.go exists to prevent — the caller
// must be able to refuse BEFORE the gesture, not discover it by trying.
//
// The name is an ACTION rather than one of recon.ExtraIntents, exactly like
// "port_admin": nothing is stored in devices.ports for either, and nothing
// reconciles them (cf. the header of this file). WritesExtra is simply the one
// place a connector can answer a capability question without a session, which is
// what a screen needs.
//
// THE RESTORE IS THE CONTRACT. If the second half fails, the connector says so
// in the error AND in [ApplyResult.Notes] with the class it failed to put back,
// so the operator knows which port is dark and what it should be carrying. A
// silent failure here leaves a camera off with nothing on screen to explain it.
type PortPowerCycler interface {
	CyclePortPower(ctx context.Context, plan PortCyclePlan) (ApplyResult, error)
}

// AsPortPowerCycler returns the cycler when Admin implements it.
func AsPortPowerCycler(adm Admin) (PortPowerCycler, bool) {
	var c, ok = adm.(PortPowerCycler)
	return c, ok
}

// MgmtVLANWhenMembershipRead answers the question every port-admin guardrail in
// the fleet has to answer before it decides anything: is the VLAN membership we
// are about to reason from something we ACTUALLY READ?
//
// # The failure this exists to stop, found on hardware-shaped code 2026-08-09
//
// Every connector's guard has an ignorance branch: when the management VLAN is
// unknown it spares every LINKED port, because it cannot rule out that one of
// them is carrying the session. That branch is keyed on the management VLAN
// alone — and the management VLAN and the port membership come from DIFFERENT
// places. On arista/eos the SVIs print AFTER the Ethernet blocks, so a
// `show running-config` that lost its head keeps `interface Vlan99` and loses
// every port; on fs/s3400 the pager is FED rather than disabled and a `--More--`
// has already truncated a read in production.
//
// In both cases `mgmt` comes back perfectly readable while the membership map is
// EMPTY. The ignorance branch is skipped because mgmt > 0, the guard asks "does
// this port carry VLAN 99" of a zero value, gets false for every port on the
// switch — including the trunked uplink — and the applier shuts the jack the
// session is riding, while its notes claim the management VLAN was known.
//
// A guardrail that fails OPEN is not a guardrail. So an empty membership is
// routed to the ignorance branch by reporting the management VLAN as unknown,
// which is what it effectively is: we know its number and nothing about which
// ports carry it, and only the second half protects anything.
//
// # Why this is a function and not a line in each connector
//
// It was three copies before it was one, and the fourth connector to grow a
// membership read would have had to notice the pattern from scratch. Note that
// hpe/procurve solves the same problem a different and equally good way — it
// refuses a running-config shorter than `minRunningConfig` before parsing — so
// this is not the only correct shape, just the one that fits a connector whose
// read is structured rather than a blob.
//
// EMPTY IS NOT THE SAME AS "no port is a member". A switch really can have no
// VLAN membership configured; what it cannot do is report that in the same
// breath as a management VLAN it took FROM one of those ports. The caller says
// so in its notes, so the operator can tell a short read from a bare device.
func MgmtVLANWhenMembershipRead(mgmt, membershipCount int) int {
	if membershipCount == 0 {
		return 0
	}
	return mgmt
}

package mdpv1

import "context"

// Power over Ethernet: what a device delivers on its own ports, distinct from
// the ordinary switching contract the same way spanning tree is (cf.
// spanning.go) — OPTIONAL, read and write kept apart, and nothing here is
// mixed into [PortState] or [State] (cf. PortState's own comment: "PoE draw…
// [is] TELEMETRY… bringing [it] in here would make the drift indicator blink
// on every watt").
//
// # "Not all ports" is the fact this file exists to carry
//
// A PoE-capable switch is not a uniform slab of identical jacks. The Ubiquiti
// EdgePoint S16 surveyed 2026-08-05 (hardware revision 20, firmware 1.9.2) is
// the concrete case: sockets 0/1-0/4 carry NO power-sourcing hardware at all —
// every mode but "off" is REFUSED by the device itself — while 0/5-0/16 do,
// and accept exactly two deliverable classes: standard 802.3af/at negotiation
// (the connector calls this "54v", since that is the ceiling 802.3at
// negotiates to) and passive 24V. A capability model that only asked "does
// this switch have PoE" would get the whole unit wrong in one direction or the
// other; [PoEPortState.Capable] is the per-port answer, and it is read from
// the device — never guessed from a model name or a port number, because
// those SKUs are not documented widely enough to trust.
//
// A second, less common axis is DIRECTION: almost every port on almost every
// platform is the switch giving power OUT to a downstream device. A handful of
// compact models take their OWN power IN through one dedicated port instead.
// The role constants below carry the distinction on the rare platform that
// has one; everywhere else every capable port is simply "out".
const (
	// PoEClassNone: the port is administratively OFF. No voltage present.
	PoEClassNone = "none"
	// PoEClass24V: passive 24V (2- or 4-pair) — the legacy voltage most of the
	// Ubiquiti wireless line (airMax, some early UniFi APs) expects, and
	// dangerous to a device that expects something else: "passive" means
	// nothing negotiates first.
	PoEClass24V = "24v"
	// PoEClass54V: standard IEEE 802.3af/at negotiation (auto), or a passive
	// 4-pair 54V mode where a platform offers one. Grouped under one class
	// because both land in the same practical bracket (44-57V) and a caller
	// choosing "the modern default" wants either.
	PoEClass54V = "54v"
)

// PoE port role: which direction power flows on the port.
const (
	// PoERoleOut: the switch feeds a downstream powered device. The default,
	// and — until a platform is surveyed that has the alternative — the only
	// value any shipped connector sets.
	PoERoleOut = "out"
	// PoERoleIn: the switch itself is powered THROUGH this port. Reserved for
	// the platforms that have one; nothing in this codebase has observed it
	// yet, and a connector must not claim it without having seen it.
	PoERoleIn = "in"
)

// PoEPortState is what a device reports about ONE port's power delivery,
// RIGHT NOW. Read-only observation, same standing as [SpanningTreeState]: it
// never feeds VLAN-style drift, and a caller wanting "is this configured the
// way the site wants" compares [PoEPortState.Class] against its own intent
// directly rather than through [State].
type PoEPortState struct {
	// Port: facade name (GE12, ether3…), same vocabulary as State.Ports.
	Port string `json:"port"`
	// Capable: this port has power-sourcing hardware behind it AT ALL. FALSE
	// on a port that a platform's own PoE tables still list — and that this
	// connector must therefore still REPORT — but that refuses every power
	// mode except "none" (cf. the file header). A caller must gate any "set
	// PoE" control on this, not merely on the connector implementing
	// [PoEReader]: on a mixed unit, the interface answers for both kinds of
	// port.
	Capable bool `json:"capable"`
	// Role: "out" (feeds a downstream PD) or "in" (the switch itself draws
	// power here). Meaningless, and left empty, when !Capable.
	Role string `json:"role,omitempty"`
	// Class: what this port is CONFIGURED to deliver — [PoEClassNone],
	// [PoEClass24V], or [PoEClass54V]. Empty when !Capable, or when the
	// platform's own wording for the mode did not map onto this vocabulary
	// (a connector then says so in [PoEState.Notes] rather than guessing).
	Class string `json:"class,omitempty"`
	// Delivering: a powered device is CURRENTLY drawing power — the fact an
	// operator actually wants when asking "is that camera up", distinct from
	// Class, which only says what the port is configured to be willing to
	// deliver.
	Delivering bool `json:"delivering"`
	// PowerW: measured draw, in watts. Zero when not delivering.
	PowerW float64 `json:"power_w,omitempty"`
	// VoltageV, CurrentMA: measured electrical state, when the platform
	// exposes them. Zero when not delivering, or unknown.
	VoltageV  float64 `json:"voltage_v,omitempty"`
	CurrentMA float64 `json:"current_ma,omitempty"`
	// TemperatureC: nil when the platform reports none FOR THIS PORT — an
	// EdgeSwitch port with no PSE hardware behind it prints "N/A" rather than
	// a reading, which is also the read-only signal [PoEPortState.Capable] is
	// derived from on that platform (cf. edgeswitch/poe.go).
	TemperatureC *float64 `json:"temperature_c,omitempty"`
	// Detection: the platform's own wording for the port's detection/fault
	// state ("Open Circuit", "Short", "Unknown"…), carried AS-IS rather than
	// normalized into a vocabulary this file would have to keep extending
	// against every vendor's phrasing.
	Detection string `json:"detection,omitempty"`
	// NegotiatedClass: the IEEE 802.3af/at power class the connected PD
	// negotiated ("0".."4", "Unknown"…), in the platform's own words. A
	// DIFFERENT axis from [PoEPortState.Class]: Class is what the PORT is
	// configured to offer, NegotiatedClass is what the PD on the other end
	// asked for — meaningful only while Delivering.
	NegotiatedClass string `json:"negotiated_class,omitempty"`
}

// PoEState is what a device reports about PoE across every port.
type PoEState struct {
	Ports []PoEPortState `json:"ports,omitempty"`
	// BudgetW: the chassis power budget the platform declares, in watts.
	// Zero means unknown, NOT "no PoE" — a connector with no reading for
	// this says so in Notes rather than leaving a silence a caller could
	// misread as "nothing available".
	BudgetW int `json:"budget_w,omitempty"`
	// UsedW: total measured draw across every port, when the platform sums
	// it directly rather than requiring the caller to add PowerW itself.
	UsedW float64  `json:"used_w,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// PoEReader reads PoE observation from a device.
//
// OPTIONAL. Platforms without PoE hardware, or on which this has not been
// exercised, leave it unimplemented; callers use [AsPoEReader] before
// offering PoE status in the UI.
type PoEReader interface {
	ReadPoE(ctx context.Context) (PoEState, error)
}

// AsPoEReader returns the reader when Admin implements it.
func AsPoEReader(adm Admin) (PoEReader, bool) {
	var r, ok = adm.(PoEReader)
	return r, ok
}

// PoEPortIntent is what meshcraft asks of ONE port for PoE.
//
// Class is the class to CONFIGURE: [PoEClassNone], [PoEClass24V], or
// [PoEClass54V] — an explicit intent the same way [PortState.PVID] is one,
// including the explicit "off" that a mixed-capability unit needs as much as
// an explicit "on" (cf. the file header: this is not the same question as
// "does the switch have PoE").
//
// # THE 4-PAIR PASSIVE MODES HAVE NO PATH TO BEING REQUESTED, deliberately
//
// The three constants above are the whole vocabulary, and none of them means
// "4-pair": a passive 4-pair 54V mode is grouped under [PoEClass54V] and a
// 4-pair 24V one under [PoEClass24V]. A caller therefore CANNOT ask for either,
// and no connector may emit one on its own initiative — ubiquiti/edgeswitch, the
// only range in this codebase that writes PoE at all, refuses both spellings in
// BOTH directions (poe.go, classOfOpMode and opModeOfClass; the lab EdgePoint
// S16 answers "Unsupported Interfaces" to every 4-pair mode on every port tried).
//
// That absence is a decision, not an oversight, and the way to change it is
// stated here so nobody changes it in a connector: a unit that ACCEPTS 4-pair
// needs a RICHER INTENT — a pair-count alongside the class, or two more class
// constants — because the write and the read must name the same thing.
// [PoEApplier] implementations restore what they read ([PortPowerCycler]
// especially, whose whole contract is that the same class comes back),
// so a substring widened in one connector would make "the class asked for" and
// "the class the port carries" two different values, and a power cycle would put
// back the 2-pair sibling of what it cut, at half the power.
//
// Class EMPTY is a distinct, narrower request kept for ONE caller:
// [recon.RequestedExtras] only ever reports POSITIVE tag-rule intents (cf.
// recon.go, "`false`, `0`, `""` and `null` are neutral values"), so the
// automatic profile-driven path (api.applyDevicePoE) can only ever ask a
// connector to turn a port ON, never off — a connector receiving an empty
// Class applies its platform DEFAULT "on" class ([PoEClass54V] on every
// platform surveyed so far) and never turns a port off on that path's
// account. A caller that wants a SPECIFIC class, including "none", always
// sets it explicitly.
type PoEPortIntent struct {
	Port  string `json:"port"`
	Class string `json:"class,omitempty"`
}

// PoEPlan is what meshcraft asks a connector to apply for PoE.
type PoEPlan struct {
	Ports []PoEPortIntent `json:"ports,omitempty"`
}

// PoEApplier writes PoE configuration to a device.
//
// OPTIONAL. Same pattern as [SpanningTreeApplier]: declare
// [Connector.WritesExtra]("poe") only once this is implemented and the
// command forms are proven — on a platform whose write path is still
// unexercised on hardware, that is announced in the connector's own comments
// and in [ApplyResult.Notes], the same way every other unverified write in
// this codebase is.
//
// A class a port's [PoEPortState.Capable]/scope refuses is an ORDINARY
// REFUSAL, reported in [ApplyResult.Skipped] — never a Go error and never
// silently rounded to a class the port WILL accept: doing either would hide
// the exact fact an operator needs, which is that this jack cannot carry what
// was asked of it.
type PoEApplier interface {
	ApplyPoE(ctx context.Context, plan PoEPlan) (ApplyResult, error)
}

// AsPoEApplier returns the applier when Admin implements it.
func AsPoEApplier(adm Admin) (PoEApplier, bool) {
	var a, ok = adm.(PoEApplier)
	return a, ok
}

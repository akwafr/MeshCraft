package mdpv1

import "sort"

// Gestures name the OPTIONAL capabilities of a connector, statically.
//
// # Why type assertions were not enough
//
// Every optional capability of this contract is an interface asserted on the
// session — `adm.(PoEReader)` — and a session is the one thing this product
// must spend carefully: Instant On grants five, exhausting them closes a
// device that has neither SSH nor a serial port. Asking "can this platform
// read PoE?" by opening a session to find out answers a free question at the
// price of the scarcest resource there is. The samplers did exactly that, on
// every cycle, for every device whose driver does not implement the reader
// they wanted.
//
// A Gesture is the same answer WITHOUT the session: [Connector.Supports]
// declares, from the method set alone, which optional interfaces the sessions
// of this connector will satisfy. The caller consults it before
// authenticating; the assertion at the call site remains, as the check that
// cannot lie.
//
// # Why the values are wire names
//
// These strings are the vocabulary of the external-driver contract (MDP): a
// driver running out of process announces the same list over HTTP, and there
// is no type assertion across a wire. One vocabulary, declared here, used by
// both worlds — a gesture renamed is a wire contract broken, so they are
// NEVER renamed.
type Gesture string

const (
	// Session-scoped gestures — the optional interfaces asserted on [Admin].
	GestureAccounts             Gesture = "accounts"
	GesturePersist              Gesture = "persist"
	GestureSNMPConfig           Gesture = "snmp-config"
	GestureTrapReceiver         Gesture = "trap-receiver"
	GestureMgmtMove             Gesture = "mgmt-move"
	GestureMgmtJump             Gesture = "mgmt-jump"
	GestureFaceplate            Gesture = "faceplate"
	GestureIdentity             Gesture = "identity"
	GestureMgmtStaticOwnerProbe Gesture = "mgmt-static-owner-probe"
	GestureRawRead              Gesture = "raw-read"
	GestureDHCPSnoopRead        Gesture = "dhcp-snooping-read"
	GestureDHCPSnoopApply       Gesture = "dhcp-snooping-apply"
	GestureStormControlRead     Gesture = "storm-control-read"
	GestureStormControlApply    Gesture = "storm-control-apply"
	GestureStormControlCaps     Gesture = "storm-control-caps"
	GestureSpanningTreeRead     Gesture = "spanning-tree-read"
	GestureSpanningTreeApply    Gesture = "spanning-tree-apply"
	GesturePoERead              Gesture = "poe-read"
	GesturePoEApply             Gesture = "poe-apply"
	GesturePortAdmin            Gesture = "port-admin"
	GesturePortPowerCycle       Gesture = "port-power-cycle"
	GestureLAGs                 Gesture = "lags"
	GestureLAGFallback          Gesture = "lag-fallback"
	GestureHostname             Gesture = "hostname"
	GestureLLDP                 Gesture = "lldp"
	GestureFDB                  Gesture = "fdb"
	GestureOptics               Gesture = "optics"
	GestureMgmtAddrRead         Gesture = "mgmt-addr-read"
	GestureMgmtAddrApply        Gesture = "mgmt-addr-apply"

	// Connector-scoped gestures — asserted on the [Driver]/[Connector] value
	// itself, no session involved.
	GesturePortName Gesture = "port-name"
	GestureIdentify Gesture = "identify"
	GestureOnboard  Gesture = "onboard"
	GestureWebUI    Gesture = "webui"
)

// gestureProbes maps every optional interface onto its gesture. THE table:
// an optional interface added without a row here is invisible to Supports,
// and therefore absent from the wire contract — gesture_test.go counts the
// rows against the interface list to catch exactly that omission.
var gestureProbes = []struct {
	gesture Gesture
	has     func(any) bool
}{
	{GestureAccounts, func(v any) bool { var _, ok = v.(AccountManager); return ok }},
	{GesturePersist, func(v any) bool { var _, ok = v.(Persister); return ok }},
	{GestureSNMPConfig, func(v any) bool { var _, ok = v.(SNMPConfigurator); return ok }},
	{GestureTrapReceiver, func(v any) bool { var _, ok = v.(TrapPusher); return ok }},
	{GestureMgmtMove, func(v any) bool { var _, ok = v.(MgmtMover); return ok }},
	{GestureMgmtJump, func(v any) bool { var _, ok = v.(MgmtJumper); return ok }},
	{GestureFaceplate, func(v any) bool { var _, ok = v.(FaceplateReader); return ok }},
	{GestureIdentity, func(v any) bool { var _, ok = v.(IdentityReader); return ok }},
	{GestureMgmtStaticOwnerProbe, func(v any) bool { var _, ok = v.(MgmtStaticOwnerProber); return ok }},
	{GestureRawRead, func(v any) bool { var _, ok = v.(RawReader); return ok }},
	{GestureDHCPSnoopRead, func(v any) bool { var _, ok = v.(DHCPSnoopReader); return ok }},
	{GestureDHCPSnoopApply, func(v any) bool { var _, ok = v.(DHCPSnoopApplier); return ok }},
	{GestureStormControlRead, func(v any) bool { var _, ok = v.(StormControlReader); return ok }},
	{GestureStormControlApply, func(v any) bool { var _, ok = v.(StormControlApplier); return ok }},
	{GestureStormControlCaps, func(v any) bool { var _, ok = v.(StormControlCapabler); return ok }},
	{GestureSpanningTreeRead, func(v any) bool { var _, ok = v.(SpanningTreeReader); return ok }},
	{GestureSpanningTreeApply, func(v any) bool { var _, ok = v.(SpanningTreeApplier); return ok }},
	{GesturePoERead, func(v any) bool { var _, ok = v.(PoEReader); return ok }},
	{GesturePoEApply, func(v any) bool { var _, ok = v.(PoEApplier); return ok }},
	{GesturePortAdmin, func(v any) bool { var _, ok = v.(PortAdminApplier); return ok }},
	{GesturePortPowerCycle, func(v any) bool { var _, ok = v.(PortPowerCycler); return ok }},
	{GestureLAGs, func(v any) bool { var _, ok = v.(LAGWriter); return ok }},
	// lag-fallback QUALIFIES lags rather than adding an op: no new path, one
	// more field of LAGState honoured. Declared separately because the screen
	// must hide the control on the platforms that cannot do it, which it
	// cannot know from "lags" alone.
	{GestureLAGFallback, func(v any) bool { var _, ok = v.(LAGFallbackWriter); return ok }},
	{GestureHostname, func(v any) bool { var _, ok = v.(HostnameSetter); return ok }},
	{GestureLLDP, func(v any) bool { var _, ok = v.(LLDPReader); return ok }},
	{GestureFDB, func(v any) bool { var _, ok = v.(FDBReader); return ok }},
	{GestureOptics, func(v any) bool { var _, ok = v.(OpticsReader); return ok }},
	{GestureMgmtAddrRead, func(v any) bool { var _, ok = v.(MgmtAddrReader); return ok }},
	{GestureMgmtAddrApply, func(v any) bool { var _, ok = v.(MgmtAddrWriter); return ok }},
	{GesturePortName, func(v any) bool { var _, ok = v.(PortNamer); return ok }},
	{GestureIdentify, func(v any) bool { var _, ok = v.(Identifier); return ok }},
	{GestureOnboard, func(v any) bool { var _, ok = v.(Onboarder); return ok }},
	{GestureWebUI, func(v any) bool { var _, ok = v.(WebUIAddresser); return ok }},
}

// GesturesOf computes the gestures satisfied by the method sets of the given
// prototypes — typically the connector itself and a TYPED NIL of its session
// type: GesturesOf(d, (*Client)(nil)).
//
// A type assertion on a typed-nil interface value is a static method-set
// check: no session is opened, no field is read, nil is never dereferenced.
// This is what makes the one-line Supports implementation impossible to get
// out of sync with the code — the compiler's view of the type IS the answer,
// where a hand-maintained list would rot at the first added interface.
func GesturesOf(prototypes ...any) []Gesture {
	var seen = map[Gesture]bool{}
	var out []Gesture
	var p any
	for _, p = range prototypes {
		var probe struct {
			gesture Gesture
			has     func(any) bool
		}
		for _, probe = range gestureProbes {
			if !seen[probe.gesture] && probe.has(p) {
				seen[probe.gesture] = true
				out = append(out, probe.gesture)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Has reports whether the connector declares the gesture — the session-free
// form of the capability question, for every decision that must be taken
// BEFORE spending an authentication: samplers skipping a device, screens
// hiding a button, the API refusing with a message instead of dialing.
//
// The type assertion at the call site remains the final word: Has answers
// from the declaration, the assertion answers from the value in hand.
func Has(c Connector, g Gesture) bool {
	var got Gesture
	for _, got = range c.Supports() {
		if got == g {
			return true
		}
	}
	return false
}

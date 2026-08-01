package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"

	"meshcraft/internal/auth"
	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// Per-DEVICE management addressing mode: DHCP or static, with its three fields
// (address, gateway, DNS). Migration 0027, and
// docs/01-Architecture/Adressage de management section 8.
//
// # THE CASE THAT MADE THIS NECESSARY
//
// `site_mgmt_plan.source` (migration 0009) carries address origin for an entire
// SITE. That is correct at that level — a management network has a dominant regime —
// but it leaves no room for the device that is not in it. They exist, and not at the
// margins: in the reference installation, BOTH CSS326 switches **and** the CRS326 are on
// STATIC addresses, in the correct management subnet, on a site whose plan says
// "external DHCP". All three readbacks are in connector captures
// (swos/testdata/backup.swb: `iptp:0x01`; routeros/testdata/ip_address.json:
// `"dynamic":"false"`).
//
// What that gap produced: nothing visible, and that is the problem. Those devices
// conform to the plan — their address is in the subnet, they respond — and meshcraft
// could neither say how they obtained it, nor display the three fields of a static
// configuration, nor record the decision to leave them as-is. Yet what happens the day
// the DHCP server changes has nothing in common between a leased device and a fixed-address
// device.
//
// # "KEEP STATIC" IS NOT A SPECIAL CASE, IT IS THE DEFAULT
//
// The default is `inherit`, and it applies standard data model rule 3 — "absent = not
// managed". A device on `inherit` is imposed NOTHING: if it is static, it STAYS static,
// without anyone having to declare it. That is the request, literally, and it is also the
// only form that requires no data migration: the entire existing fleet starts on `inherit`
// and does not change behavior.
//
// The EFFECTIVE mode is therefore resolved on read, never in the database (cf.
// [effectiveAddrMode]) — same reason as plan conformity, written right next to it: the
// site plan changes at any moment, and a mode frozen in the database would lie from the
// first change.

// MgmtAddrCaps says what this device's PLATFORM allows for management addressing.
// Copied from connector capabilities so the UI does not have to fetch them — and
// especially so it does not have to GUESS.
type MgmtAddrCaps struct {
	// Known: is this device's connector known to this binary?
	//
	// FALSE for a manually entered device, or one whose `extra.driver` names an
	// absent connector. The four booleans below are then meaningless, and the UI
	// must say so instead of showing four "no" answers that would look like facts
	// about the device.
	Known   bool `json:"known"`
	Static  bool `json:"static"`
	DHCP    bool `json:"dhcp"`
	Gateway bool `json:"gateway"`
	DNS     bool `json:"dns"`
	// Routed / IPARP: L3 vs L2 management reachability facts for the UI
	// (MgmtRouted / MgmtIPARP). Copied from [driver.MgmtCapabilities].
	Routed bool `json:"routed"`
	IPARP  bool `json:"ip_arp"`
	// ApplySameVLAN / MigrateVLAN / JumpVLAN: which WRITE paths this connector
	// has proven. Copied from [driver.MgmtCapabilities] so the UI can offer Apply,
	// Conform Management (migrate or jump) without opening a session.
	ApplySameVLAN bool `json:"apply_same_vlan"`
	MigrateVLAN   bool `json:"migrate_vlan"`
	JumpVLAN      bool `json:"jump_vlan"`
}

// MgmtAddressingView is what the UI needs to know about a device's management
// addressing, and cannot compute itself.
//
// Intent and observed state already travel in [model.Device] (`mgmt_addr` and
// `mgmt_addr_observed`): copying them here would create two versions, one of which
// would go stale.
type MgmtAddressingView struct {
	// Effective: the mode that ACTUALLY applies — "dhcp", "static", or EMPTY when
	// nothing allows a conclusion. Empty is an answer, not a default to fill in.
	Effective string `json:"effective"`
	// Origin says WHERE this mode comes from: "device" (decided on this device),
	// "observed" (inherited from what the device carries), "site_plan" (inherited
	// from the site plan's address origin), "unknown".
	//
	// This distinguishes "static, decided" from "static, observed and kept" — the
	// distinction the vault has required since section 1 and that `devices.mgmt_mode`
	// never managed.
	Origin string `json:"origin"`
	// Why is the phrase to display, written for an operator.
	Why string `json:"why"`
	// Drift: intent and what the device carries DIVERGE, and here is how.
	// Empty when they agree, or when one of the two is unknown — we do not report
	// a gap we have not observed.
	Drift string `json:"drift,omitempty"`
	// CurrentVLAN is the management VLAN observed on the device (0 = untagged /
	// unknown). From `devices.observed.mgmt_vlan`, filled on re-read.
	CurrentVLAN int `json:"current_vlan,omitempty"`
	// Caps: what the platform allows.
	Caps MgmtAddrCaps `json:"caps"`
	// Readable: can meshcraft READ addressing mode on this platform?
	// False does not mean "no mode" but "not read back on this product line".
	Readable bool `json:"readable"`
	// Pushable / PushBlocked: can meshcraft APPLY this mode on the CURRENT
	// management VLAN ([driver.MgmtAddrWriter])?
	//
	// VLAN moves use separate paths: MigrateVLAN ([driver.MgmtMover]) and JumpVLAN
	// ([driver.MgmtJumper]), exposed on caps — not this boolean.
	Pushable    bool   `json:"pushable"`
	PushBlocked string `json:"push_blocked,omitempty"`
	// ApplyConfirm: non-empty when Apply needs a second click (confirmed=true).
	// Not a lock — site-plan override or acquisition-mode cutover. Empty when
	// Apply is a same-mode refresh (e.g. only the static address changes).
	ApplyConfirm string `json:"apply_confirm,omitempty"`
}

// addrCapsOf reads addressing capabilities from a device's platform.
//
// PURE AND DATABASE-FREE, for the exact same reason as [outOfBandOf] next door: it is
// called on LISTS of two hundred devices, and going back through the discovery candidate
// would cost one query per row. `devices.extra.driver` is set at adoption and suffices.
func addrCapsOf(d model.Device) MgmtAddrCaps {
	var drv, known = driver.Get(driver.DriverNameFromExtra(d.Extra))
	if !known {
		return MgmtAddrCaps{}
	}
	var m = drv.Capabilities(d.Model).Mgmt
	return MgmtAddrCaps{
		Known: true, Static: m.Static, DHCP: m.DHCP,
		Gateway: m.Gateway, DNS: m.DNS,
		Routed: m.Routed, IPARP: m.IPARP,
		ApplySameVLAN: m.ApplySameVLAN, MigrateVLAN: m.MigrateVLAN, JumpVLAN: m.JumpVLAN,
	}
}

// observedAddrOf decodes what the connector read on the device. Returns `ok=false`
// when nothing has ever been read back — which is not the same as an empty mode, and
// the two lead to different phrases.
func observedAddrOf(d model.Device) (driver.MgmtAddrState, bool) {
	var out driver.MgmtAddrState
	if len(d.MgmtAddrObserved) == 0 || d.MgmtAddrObservedAt == nil {
		return out, false
	}
	if json.Unmarshal(d.MgmtAddrObserved, &out) != nil {
		return out, false
	}
	return out, true
}

// effectiveAddrMode resolves the mode that applies to this device, and says where it
// comes from.
//
// THREE STEPS, in this order, and the order IS the feature:
//
//  1. WHAT WAS DECIDED ON THIS DEVICE always wins. An explicit decision is not
//     contradicted by an observation: that is precisely what allows switching a static
//     device to DHCP — intent then diverges from observed, and that divergence is what
//     we want to SEE (cf. [addrDriftOf]).
//
//  2. WHAT THE DEVICE CARRIES, when nothing was decided. This is where "keep static
//     configuration" plays out, and it is the heart of the request: a device adopted in
//     the correct management LAN but on a fixed address stays on a fixed address, with no
//     action from anyone. Doing the opposite — applying the site plan's address origin to
//     a never-decided device — would treat three devices in the reference installation as
//     drifts to fix.
//
//  3. THE SITE PLAN'S ADDRESS ORIGIN, as last resort, and only when the site has an
//     explicit plan. That is the dominant regime, and it applies only to what we know
//     nothing else about.
//
// When none of the three answers, the mode stays EMPTY. We do not fill in: "meshcraft
// does not know how this device obtained its address" is accurate, and is fixed by a
// read-back. "DHCP, probably" does not get fixed — it propagates.
func effectiveAddrMode(d model.Device, plan model.MgmtPlan) (string, string, string) {
	switch d.MgmtAddr.Mode {
	case model.MgmtAddrStatic:
		return model.MgmtAddrStatic, "device",
			"static address, decided in meshcraft for this device."
	case model.MgmtAddrDHCP:
		return model.MgmtAddrDHCP, "device",
			"DHCP client, decided in meshcraft for this device."
	}

	var obs, relu = observedAddrOf(d)
	if relu && obs.Mode != "" {
		if obs.Mode == driver.MgmtAddrModeStatic {
			return model.MgmtAddrStatic, "observed",
				"static address OBSERVED on the device, and kept: meshcraft imposes " +
					"nothing until someone decides otherwise."
		}
		return model.MgmtAddrDHCP, "observed",
			"DHCP lease OBSERVED on the device, and kept: meshcraft imposes nothing " +
				"until someone decides otherwise."
	}

	if plan.Configured {
		if plan.Source == model.MgmtSourceMeshcraft {
			return model.MgmtAddrStatic, "site_plan",
				"the site plan allocates management addresses and sets them static. " +
					"Nothing has been read back on this device: this is an expectation, not an observation."
		}
		return model.MgmtAddrDHCP, "site_plan",
			"the site plan says « external DHCP ». Nothing has been read back on this device: " +
				"this is an expectation, not an observation."
	}

	return "", "unknown",
		"addressing mode unknown: this device has not been read back, and its site has no " +
			"management plan. Read it back to learn whether it uses a static address or a lease."
}

// addrDriftOf says whether INTENT and what the device CARRIES diverge.
//
// Computed ONLY on explicit intent and a read-back observation. An `inherit` cannot
// drift — it requires nothing — and a never-read-back device cannot be declared out of
// sync: that would turn ignorance into a fault on a fleet largely not yet read back.
// Same arbitration as the VLAN path in [mgmtPathOf], and worth holding twice.
//
// ADDRESS MATTERS AS MUCH AS MODE. When the operator already updated `mgmt_ip` to
// match static intent, a older observation that still shows the previous address is
// STALE memory — they moved the device by hand — and must not raise a drift.
//
// The phrase is intentionally short and informational: this is a plan/intent gap,
// not an order to click Apply.
func addrDriftOf(d model.Device) string {
	if d.MgmtAddr.Mode == model.MgmtAddrInherit || d.MgmtAddr.Mode == "" {
		return ""
	}
	var obs, relu = observedAddrOf(d)
	if !relu || obs.Mode == "" {
		return ""
	}
	const soft = "addr_plan_mismatch"
	if d.MgmtAddr.Mode == model.MgmtAddrDHCP && obs.Mode == driver.MgmtAddrModeStatic {
		return soft
	}
	if d.MgmtAddr.Mode == model.MgmtAddrStatic && obs.Mode == driver.MgmtAddrModeDHCP {
		return soft
	}
	if d.MgmtAddr.Mode == model.MgmtAddrStatic && d.MgmtAddr.StaticIP != "" &&
		obs.Address != "" && !sameAddress(d.MgmtAddr.StaticIP, obs.Address) {
		// Operator already dials the intended address: previous observation is outdated.
		if sameAddress(d.MgmtAddr.StaticIP, d.MgmtIP) {
			return ""
		}
		return soft
	}
	return ""
}

// sameAddress compares two addresses when one may carry a prefix and the other may not.
//
// On SwOS there is NO prefix to read (/sys.b carries `ip`, without mask), whereas
// RouterOS returns « 10.99.0.54/24 ». Comparing raw strings would therefore report a
// permanent drift across an entire product line, for a notation difference only.
func sameAddress(a, b string) bool {
	return bareAddr(a) == bareAddr(b)
}

func bareAddr(s string) string {
	var slash = strings.IndexByte(s, '/')
	if slash < 0 {
		return s
	}
	return s[:slash]
}

func orUnknownAddr(s string) string {
	if s == "" {
		return "unknown address"
	}
	return s
}

// pushBlockedReason says why same-VLAN APPLY is refused, when it is.
//
// Empty when [MgmtAddrCaps.ApplySameVLAN] is true — the UI may offer Apply.
// VLAN migrate / jump refusals are separate (caps + migrate handlers).
func pushBlockedReason(caps MgmtAddrCaps) string {
	if !caps.Known {
		return "meshcraft does not know this device's connector: there is no " +
			"path to write anything to it. This addressing mode is an " +
			"ANNOTATION — useful to document the fleet, with no effect on the device."
	}
	if caps.ApplySameVLAN {
		return ""
	}
	if caps.MigrateVLAN {
		return "same-VLAN addressing apply is not proven on this platform yet. " +
			"Intent is recorded and displayed. To move management onto the site plan, " +
			"use « Conform Management » (dual-path migrate)."
	}
	if caps.JumpVLAN {
		return "same-VLAN addressing apply is not proven on this platform yet. " +
			"To move management onto the site plan on this product line, use " +
			"« Conform Management » (single-address change with backup)."
	}
	return "meshcraft does NOT write management addressing on this platform yet: " +
		"no connector path has been proven on hardware for same-VLAN apply, dual-path " +
		"migrate, or jump. This mode is recorded and displayed; applying it remains " +
		"to be done in the device's interface."
}

// addressingViewOf assembles what the UI displays. PURE: the plan is the same for an
// entire site and is read once, not once per device (cf. [withConformity]).
func addressingViewOf(d model.Device, plan model.MgmtPlan) MgmtAddressingView {
	var v MgmtAddressingView
	v.Effective, v.Origin, v.Why = effectiveAddrMode(d, plan)
	v.Drift = addrDriftOf(d)
	v.Caps = addrCapsOf(d)
	v.CurrentVLAN = observedMgmtVLAN(d)
	var _, relu = observedAddrOf(d)
	v.Readable = relu || readableAddrOf(d)
	v.PushBlocked = pushBlockedReason(v.Caps)
	// Apply is only meaningful for an explicit dhcp/static intent — inherit means
	// "impose nothing", so there is nothing to push.
	v.Pushable = v.Caps.ApplySameVLAN &&
		(d.MgmtAddr.Mode == model.MgmtAddrDHCP || d.MgmtAddr.Mode == model.MgmtAddrStatic) &&
		v.PushBlocked == ""
	if v.Pushable {
		v.ApplyConfirm = applyConfirmReason(d, plan)
	}
	return v
}

// observedMgmtVLAN reads the last observed management VLAN from devices.observed.
func observedMgmtVLAN(d model.Device) int {
	if len(d.Observed) == 0 {
		return 0
	}
	var obs observedState
	if json.Unmarshal(d.Observed, &obs) != nil {
		return 0
	}
	return obs.MgmtVLAN
}

// applyConfirmReason says why Apply needs a second confirmation, when it does.
//
// Empty when the write is a same-mode refresh (address/gateway/DNS only). Non-empty
// when the operator overrides the site plan's address origin, or when the push
// changes how the device obtains its address (DHCP ↔ static). That is a cutover,
// not a lock: confirmed=true proceeds.
func applyConfirmReason(d model.Device, plan model.MgmtPlan) string {
	if d.MgmtAddr.Mode != model.MgmtAddrDHCP && d.MgmtAddr.Mode != model.MgmtAddrStatic {
		return ""
	}
	if plan.Configured {
		if plan.Source == model.MgmtSourceDHCPExternal &&
			d.MgmtAddr.Mode == model.MgmtAddrStatic {
			return "this site's plan expects external DHCP; applying a static address " +
				"overrides that policy for this device only"
		}
		if plan.Source == model.MgmtSourceMeshcraft &&
			d.MgmtAddr.Mode == model.MgmtAddrDHCP {
			return "this site's plan allocates static management addresses; applying " +
				"DHCP overrides that policy for this device only"
		}
	}
	var obs, relu = observedAddrOf(d)
	if !relu || obs.Mode == "" {
		return ""
	}
	if d.MgmtAddr.Mode == model.MgmtAddrStatic && obs.Mode == driver.MgmtAddrModeDHCP {
		return "the device currently holds a DHCP lease (" + orUnknownAddr(obs.Address) +
			"); applying static addressing changes how it obtains its address"
	}
	if d.MgmtAddr.Mode == model.MgmtAddrDHCP && obs.Mode == driver.MgmtAddrModeStatic {
		return "the device currently carries a static address (" + orUnknownAddr(obs.Address) +
			"); applying DHCP changes how it obtains its address"
	}
	return ""
}

// readableAddrOf says whether this device's CONNECTOR can read addressing, without
// opening a session.
//
// Needed so the UI distinguishes "not read back yet" from "this product line cannot say".
// Both show absence of mode, and both call for opposite actions: the first is fixed with
// one click on "Read device back", the second cannot be fixed at all.
//
// The question is about TYPE, not a session: there is none here, and opening one to
// render a list of two hundred devices would be two hundred connections. Hence the
// explicit registry, following connector contract assertions
// (`var _ driver.MgmtAddrReader = …`).
func readableAddrOf(d model.Device) bool {
	switch driver.DriverNameFromExtra(d.Extra) {
	case "mikrotik-swos", "mikrotik-routeros", "aruba-instant-on-1930":
		return true
	}
	return false
}

// --- Recording intent ---

// deviceAddrInput is what the UI sends. Deliberately FLAT and without author fields:
// `by` and `at` are set by the database, never by the client — a client-signed decision
// is a signature we cannot trust.
type deviceAddrInput struct {
	Mode      string   `json:"mode"`
	StaticIP  string   `json:"static_ip"`
	StaticGW  string   `json:"static_gateway"`
	StaticDNS []string `json:"static_dns"`
}

// validateDeviceAddressing refuses at INPUT what would break on deployment.
//
// Same principle as `validateMgmtPlan` (migration 0009) and same reason to keep it here
// rather than in a database constraint: the controller can say "gateway 10.0.0.1 is not
// in subnet 10.99.0.0/24" where PostgreSQL would say "invalid input syntax for type
// inet". Validation is more verbose than a constraint, and that is its whole value.
//
// # PREFIX IS COMPLETED, NEVER GUESSED
//
// An operator types « 10.99.0.36 », not « 10.99.0.36/24 »: the prefix is borrowed from
// the site plan's subnet when the address is contained in it. When there is no plan, or
// when the address is OUTSIDE the plan subnet, it is REQUIRED — assuming /24 would invent
// a mask, and a wrong mask silences a device as surely as a wrong address.
//
// An address outside the plan subnet is not refused for all that. It is LEGITIMATE — it
// is the off-plan device from vault section 1, administered where it is — and plan
// conformity is a separate axis that will already flag it in orange (cf.
// [conformityWith]). Refusing here would build a wall where the product wants a warning.
//
// # CAPABILITIES ARE QUERIED, AND THAT IS NEW
//
// The vault has long noted that `MgmtCapabilities` is "filled but never queried". It is
// queried here, on the four fields that decide what a UI may offer. The concrete case:
// SwOS carries NEITHER gateway NOR DNS resolver — established by its writable field set —
// and accepting a gateway on a CSS326 would let the operator enter a value that would
// never land anywhere.
//
// An UNKNOWN connector forbids nothing, and that is the assumed exception to the rule
// "an unknown capability is assumed absent". The rule protects WRITES on the device;
// here nothing is written on the device (cf. [pushBlockedReason]), and refusing all
// annotation on a manually entered switch would prevent no damage while making the UI
// unusable on an entire fleet.
func validateDeviceAddressing(in deviceAddrInput, plan model.MgmtPlan,
	caps MgmtAddrCaps) (model.DeviceAddressing, string) {
	var out model.DeviceAddressing
	out.Mode = strings.TrimSpace(in.Mode)

	switch out.Mode {
	case model.MgmtAddrInherit, model.MgmtAddrDHCP, model.MgmtAddrStatic:
	case "":
		out.Mode = model.MgmtAddrInherit
	default:
		return out, "unknown addressing mode: expected « inherit », « dhcp », or « static »"
	}

	if out.Mode == model.MgmtAddrDHCP && caps.Known && !caps.DHCP {
		return out, "this platform does not admit a DHCP client for its administration"
	}
	if out.Mode == model.MgmtAddrStatic && caps.Known && !caps.Static {
		return out, "this platform does not admit a static address for its administration"
	}

	// The three static fields are KEPT when the mode does not use them, and that is
	// not an oversight: it allows switching a device to DHCP to try, then returning to
	// its previous addressing without retyping. The UI only shows them in static mode,
	// and clearing them here would lose configuration on a mis-click.
	out.StaticIP = strings.TrimSpace(in.StaticIP)
	out.StaticGateway = strings.TrimSpace(in.StaticGW)
	out.StaticDNS = cleanList(in.StaticDNS)

	if out.StaticGateway != "" && caps.Known && !caps.Gateway {
		return out, "this platform does NOT carry a management gateway: the entered " +
			"value would land nowhere. Such a device is reachable only from its own " +
			"subnet — that is where the collector must be placed."
	}
	if len(out.StaticDNS) > 0 && caps.Known && !caps.DNS {
		return out, "this platform does NOT carry a management DNS resolver"
	}

	if out.Mode != model.MgmtAddrStatic {
		// Nothing more to validate: kept fields will not be applied. Validating them
		// anyway would refuse a switch to DHCP because of old input, which is exactly
		// the gesture we want to make easy.
		return out, ""
	}

	if out.StaticIP == "" {
		return out, "a static address is requested: the address is required"
	}
	var prefix, msg = resolvePrefix(out.StaticIP, plan)
	if msg != "" {
		return out, msg
	}
	out.StaticIP = prefix.String()

	if out.StaticGateway != "" {
		var gw, gwErr = netip.ParseAddr(out.StaticGateway)
		if gwErr != nil {
			return out, "gateway « " + out.StaticGateway + " »: not an IP address"
		}
		if gw == prefix.Addr() {
			return out, "the gateway is the device's own address"
		}
		if !prefix.Masked().Contains(gw) {
			return out, "gateway " + out.StaticGateway + " is not in the device's " +
				"subnet (" + prefix.Masked().String() + "): it would be unreachable"
		}
	}

	var srv string
	for _, srv = range out.StaticDNS {
		var _, dnsErr = netip.ParseAddr(srv)
		if dnsErr != nil {
			return out, "DNS resolver « " + srv + " »: not an IP address"
		}
	}
	return out, ""
}

// resolvePrefix returns the address WITH its prefix, borrowing from the site plan when
// the operator did not supply one. See section "PREFIX IS COMPLETED, NEVER GUESSED".
func resolvePrefix(raw string, plan model.MgmtPlan) (netip.Prefix, string) {
	if strings.ContainsRune(raw, '/') {
		var p, err = netip.ParsePrefix(raw)
		if err != nil {
			return netip.Prefix{}, "address « " + raw + "  »: unreadable prefix " +
				"(expected e.g. « 10.99.0.36/24 »)"
		}
		if p.Addr() == p.Masked().Addr() && p.Bits() < 31 {
			// Network address is not a host address. /31 and /32 are spared: they have
			// legitimate uses (point-to-point link, host-only address).
			return netip.Prefix{}, "address « " + raw + " »: this is the NETWORK address, " +
				"not a device address"
		}
		return p, ""
	}

	var addr, err = netip.ParseAddr(raw)
	if err != nil {
		return netip.Prefix{}, "address « " + raw + " »: not an IP address"
	}
	if plan.SubnetV4 == "" {
		return netip.Prefix{}, "address « " + raw + " » without prefix length, and the " +
			"site has no management subnet to borrow from: write it as « " + raw + "/24 ». " +
			"meshcraft does not guess a mask — a wrong mask makes a device unreachable " +
			"as surely as a wrong address."
	}
	var sub, subErr = netip.ParsePrefix(plan.SubnetV4)
	if subErr != nil {
		return netip.Prefix{}, "the site's management subnet (« " + plan.SubnetV4 +
			" ») is unreadable: fix the site plan, or give the prefix length with the address"
	}
	if !sub.Contains(addr) {
		return netip.Prefix{}, "address « " + raw + " » outside the site's management " +
			"subnet (" + plan.SubnetV4 + "): allowed, but the prefix length can no longer " +
			"be borrowed from the plan. Write it explicitly (« " + raw + "/24 »)."
	}
	return netip.PrefixFrom(addr, sub.Bits()), ""
}

// cleanList removes empties and spaces from an entered list. An empty resolver in the
// list would land in the database and fail the next validation, on a value nobody typed.
func cleanList(in []string) []string {
	var out = []string{}
	var s string
	for _, s = range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// handleSetDeviceAddressing records a device's management addressing mode.
//
// Reserved to `site_admin`, like the address domain next door and for an even stronger
// reason: this is not the same order as labeling a port. It is the intent a future write
// path will refer to when deciding what to apply on a switch's administration interface —
// and getting it wrong cuts access to the device we meant to administer.
func (s *Server) handleSetDeviceAddressing(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in deviceAddrInput
	if !decode(w, r, &in) {
		return
	}
	var plan, planErr = s.Store.GetMgmtPlan(r.Context(), d.SiteID)
	if planErr != nil {
		fail(w, planErr)
		return
	}
	var want, msg = validateDeviceAddressing(in, plan, addrCapsOf(d))
	if msg != "" {
		respondErr(w, http.StatusUnprocessableEntity, msg)
		return
	}

	var me = auth.UserFrom(r.Context())
	var fresh, err = s.Store.SetDeviceAddressing(r.Context(), d.ID, want, me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	// Drop the previous observation: keeping it would compare the new decision to
	// an address the operator may already have changed by hand on the device.
	// The next read-back fills it again from the equipment.
	_ = s.Store.ClearMgmtAddrObserved(r.Context(), d.ID)
	fresh.MgmtAddrObserved = nil
	fresh.MgmtAddrObservedAt = nil
	// WARNING, not info, like the three switchover events in section 5: intent and device
	// may now diverge, and nobody will apply this intent automatically. A log that
	// classified this as notice would make the operator believe the order was executed.
	s.emitDeviceEvent(r, d, "mgmt.addressing", model.SevWarning,
		addressingChangeMessage(d.Name, want))
	respond(w, http.StatusOK, s.withSync(r.Context(), fresh))
}

// addressingChangeMessage writes to the log what was DECIDED, and reminds that nothing
// was applied. The second half matters as much as the first: an operator reading this log
// six months later must distinguish a decision from an apply.
func addressingChangeMessage(deviceName string, a model.DeviceAddressing) string {
	switch a.Mode {
	case model.MgmtAddrStatic:
		var msg = "Management addressing of « " + deviceName + " »: STATIC address " +
			a.StaticIP
		if a.StaticGateway != "" {
			msg += ", gateway " + a.StaticGateway
		}
		if len(a.StaticDNS) > 0 {
			msg += ", DNS " + strings.Join(a.StaticDNS, ", ")
		}
		return msg + ". Decision recorded — nothing was written to the device."
	case model.MgmtAddrDHCP:
		return "Management addressing of « " + deviceName + " »: DHCP client. Decision " +
			"recorded — nothing was written to the device."
	}
	return "Management addressing of « " + deviceName + " »: inherited (meshcraft " +
		"keeps what the device carries)."
}

// --- Read-back on the device ---

// readSpanningTree re-reads STP/RSTP/MSTP observation and stores it under Extra.
//
// Called from [Server.handleReadDeviceState]. Failure does not interrupt the read.
func (s *Server) readSpanningTree(ctx context.Context, deviceID string,
	adm driver.Admin) (driver.SpanningTreeState, string) {
	var reader, canRead = driver.AsSpanningTreeReader(adm)
	if !canRead {
		return driver.SpanningTreeState{}, "connector « " + adm.Connector().Name +
			" » cannot read spanning tree on this product line"
	}
	var state, err = reader.ReadSpanningTree(ctx)
	if err != nil {
		return driver.SpanningTreeState{}, "spanning tree not read back: " + err.Error()
	}
	var raw, marshalErr = json.Marshal(state)
	if marshalErr != nil {
		return state, "spanning tree not saved: " + marshalErr.Error()
	}
	var saveErr = s.Store.SaveSTPObserved(ctx, deviceID, raw)
	if saveErr != nil {
		return state, "spanning tree not saved: " + saveErr.Error()
	}
	return state, ""
}

// readMgmtAddressing re-reads management addressing on the device and stores it.
//
// Called from [Server.handleReadDeviceState], in the same gesture as VLAN and port
// read-back: asking the operator for two clicks to read back the same device would make
// them carry an internal code boundary. Its failure does NOT interrupt state read — the
// two pieces of information come from different pages, and losing ports because addressing
// is unreadable would regress the entire fleet.
//
// Returns the phrase to display, or empty when there is nothing to say.
func (s *Server) readMgmtAddressing(ctx context.Context, deviceID string,
	adm driver.Admin) (driver.MgmtAddrState, string) {
	var reader, canRead = driver.AsMgmtAddrReader(adm)
	if !canRead {
		return driver.MgmtAddrState{}, "connector « " + adm.Connector().Name +
			" » cannot say how this device obtained its administration " +
			"address: addressing mode will stay « not read back » on this range."
	}
	var state, err = reader.ReadMgmtAddressing(ctx)
	if err != nil {
		return driver.MgmtAddrState{}, "management addressing not read back: " + err.Error()
	}
	var raw, marshalErr = json.Marshal(state)
	if marshalErr != nil {
		return state, "management addressing not saved: " + marshalErr.Error()
	}
	var saveErr = s.Store.SaveMgmtAddrObserved(ctx, deviceID, raw)
	if saveErr != nil {
		return state, "management addressing not saved: " + saveErr.Error()
	}
	return state, ""
}

// handleApplyMgmtAddressing applies recorded same-VLAN addressing intent on the device.
//
// Requires [driver.MgmtAddrWriter] and caps.ApplySameVLAN. VLAN changes use migrate
// or jump, not this route.
//
// Body (optional): `{ "confirmed": true }` — required when [applyConfirmReason] is
// non-empty (site-plan override or acquisition-mode cutover). Without it the write
// is refused with the reason; it is never a silent no-op.
func (s *Server) handleApplyMgmtAddressing(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Confirmed bool `json:"confirmed"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	var caps = addrCapsOf(d)
	if !caps.ApplySameVLAN {
		respondErr(w, http.StatusUnprocessableEntity, pushBlockedReason(caps))
		return
	}
	if d.MgmtAddr.Mode != model.MgmtAddrDHCP && d.MgmtAddr.Mode != model.MgmtAddrStatic {
		respondErr(w, http.StatusUnprocessableEntity,
			"nothing to apply: addressing mode is inherited (meshcraft imposes nothing)")
		return
	}
	var plan, planErr = s.Store.GetMgmtPlan(r.Context(), d.SiteID)
	if planErr != nil {
		fail(w, planErr)
		return
	}
	var confirmWhy = applyConfirmReason(d, plan)
	if confirmWhy != "" && !in.Confirmed {
		respondErr(w, http.StatusUnprocessableEntity,
			confirmWhy+". Confirm Apply to proceed (confirmed=true) — this is not a lock.")
		return
	}
	var _, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": transcriptOf(adm),
		})
		return
	}
	var writer, canWrite = driver.AsMgmtAddrWriter(adm)
	if !canWrite {
		respondErr(w, http.StatusUnprocessableEntity,
			"connector declares ApplySameVLAN but does not implement MgmtAddrWriter")
		return
	}
	var want = intentToWriterWant(d.MgmtAddr)
	var res driver.MgmtAddrResult
	if res, err = writer.ApplyMgmtAddressing(r.Context(), want); err != nil {
		s.emitDeviceEvent(r, d, "mgmt.apply_failed", model.SevWarning,
			"Management addressing not applied on \""+d.Name+"\": "+err.Error())
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript(), "notes": res.Notes,
		})
		return
	}
	if len(res.Observed.Mode) > 0 || res.Observed.Address != "" {
		var raw, marshalErr = json.Marshal(res.Observed)
		if marshalErr == nil {
			_ = s.Store.SaveMgmtAddrObserved(r.Context(), d.ID, raw)
		}
	}
	var knownStatic string
	if d.MgmtAddr.Mode == model.MgmtAddrStatic {
		knownStatic = d.MgmtAddr.StaticIP
	}
	var newIP, adoptErr = s.adoptWrittenMgmtIP(r.Context(), d, res.Observed.Address, knownStatic)
	if adoptErr != nil {
		fail(w, adoptErr)
		return
	}
	var msg = "Management addressing applied on \"" + d.Name + "\" (" + d.MgmtAddr.Mode + ")."
	if newIP != "" {
		msg += " Reachability address updated to " + newIP + "."
	} else if d.MgmtAddr.Mode == model.MgmtAddrDHCP {
		msg += " " + s.kickRelocateAfterMgmtWrite(r.Context(), d, "DHCP addressing apply",
			res.Observed.Address) + "."
	}
	s.emitDeviceEvent(r, d, "mgmt.applied", model.SevNotice, msg)
	var fresh, getErr = s.Store.GetDevice(r.Context(), d.ID)
	if getErr != nil {
		fail(w, getErr)
		return
	}
	var out = map[string]any{
		"ok": true, "steps": adm.Transcript(), "notes": res.Notes,
		"device": s.withSync(r.Context(), fresh),
	}
	if newIP != "" {
		out["mgmt_ip"] = newIP
	} else if d.MgmtAddr.Mode == model.MgmtAddrDHCP {
		out["note"] = "DHCP lease unknown after apply — relocate started when possible"
	}
	respond(w, http.StatusOK, out)
}

// handleJumpMgmtAddressing performs a single-address management leap (SwOS-shaped).
//
// Body: optional vlan_id (0 = keep current Allow-From VLAN), plus confirmation flag.
// Requires caps.JumpVLAN and [driver.MgmtJumper].
func (s *Server) handleJumpMgmtAddressing(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		VlanID      int  `json:"vlan_id"`
		Confirmed   bool `json:"confirmed"`
		UsePlanVlan bool `json:"use_plan_vlan"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	if !in.Confirmed {
		respondErr(w, http.StatusUnprocessableEntity,
			"confirm Conform Management first (confirmed=true): this product line "+
				"carries a single management address, so the change is atomic")
		return
	}
	var caps = addrCapsOf(d)
	if !caps.JumpVLAN {
		respondErr(w, http.StatusUnprocessableEntity,
			"this platform does not offer an addressing jump (or it is not proven yet)")
		return
	}
	var vlanID = in.VlanID
	if in.UsePlanVlan || vlanID == 0 {
		var plan, planErr = s.Store.GetMgmtPlan(r.Context(), d.SiteID)
		if planErr != nil {
			fail(w, planErr)
			return
		}
		if in.UsePlanVlan {
			if !plan.Configured || plan.VlanID <= 0 {
				respondErr(w, http.StatusUnprocessableEntity,
					"this site has no usable management plan")
				return
			}
			vlanID = plan.VlanID
		}
	}
	if vlanID > 0 {
		var pathState, pathReason = mgmtVLANPathOf(d, vlanID, outOfBandOf(d))
		if pathState == "absent" {
			respondErr(w, http.StatusUnprocessableEntity, pathReason)
			return
		}
	}
	var _, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": transcriptOf(adm),
		})
		return
	}
	var jumper, canJump = driver.AsMgmtJumper(adm)
	if !canJump {
		respondErr(w, http.StatusUnprocessableEntity,
			"connector declares JumpVLAN but does not implement MgmtJumper")
		return
	}
	var want = intentToWriterWant(d.MgmtAddr)
	var backup []byte
	if backup, err = jumper.JumpMgmt(r.Context(), vlanID, want); err != nil {
		s.emitDeviceEvent(r, d, "mgmt.jump_failed", model.SevWarning,
			"Management jump failed on \""+d.Name+"\": "+err.Error())
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript(),
			"backup_bytes": len(backup),
		})
		return
	}
	var knownStatic string
	if d.MgmtAddr.Mode == model.MgmtAddrStatic {
		knownStatic = d.MgmtAddr.StaticIP
	}
	// Jump drops the session: there is usually no re-read. The static intent
	// meshcraft just wrote is the new reachability address — adopt it, or the
	// fleet keeps dialing the old mgmt_ip forever (lab-css326).
	var newIP, adoptErr = s.adoptWrittenMgmtIP(r.Context(), d, "", knownStatic)
	if adoptErr != nil {
		fail(w, adoptErr)
		return
	}
	var msg = "Management jump applied on \"" + d.Name + "\" (VLAN " + itoa(vlanID) + ")."
	if newIP != "" {
		msg += " Reachability address updated to " + newIP + "."
	} else {
		msg += " " + s.kickRelocateAfterMgmtWrite(r.Context(), d, "management jump",
			knownStatic) + "."
	}
	s.emitDeviceEvent(r, d, "mgmt.jumped", model.SevWarning, msg)
	var out = map[string]any{
		"ok": true, "vlan": vlanID, "steps": adm.Transcript(),
		"backup_bytes": len(backup),
	}
	if newIP != "" {
		out["mgmt_ip"] = newIP
	} else {
		out["note"] = "new address unknown after jump — relocate started when possible; restore backup if silent"
	}
	respond(w, http.StatusOK, out)
}

// handleProbeStaticOwner temporarily creates a static IPv4 interface on a
// NON-management VLAN, reads the owner the device accepted, then deletes it.
// Never SaveConfig. Global-admin + site_admin — same bar as web-probe.
func (s *Server) handleProbeStaticOwner(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	if !auth.UserFrom(r.Context()).IsGlobalAdmin {
		respondErr(w, http.StatusForbidden, "static-owner probe reserved for global administration")
		return
	}
	var in struct {
		VlanID  int    `json:"vlan_id"`
		Address string `json:"address"`
		Mask    string `json:"mask"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.VlanID <= 0 || in.Address == "" || in.Mask == "" {
		respondErr(w, http.StatusBadRequest, "vlan_id, address and mask are required")
		return
	}
	var _, adm, _, err = s.adminFor(r.Context(), d)
	if err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": transcriptOf(adm),
		})
		return
	}
	var prober, can = driver.AsMgmtStaticOwnerProber(adm)
	if !can {
		respondErr(w, http.StatusUnprocessableEntity,
			"this connector does not implement a static-owner lab probe")
		return
	}
	var owner int
	var raw string
	if owner, raw, err = prober.ProbeStaticOwner(r.Context(), in.VlanID, in.Address, in.Mask); err != nil {
		respond(w, http.StatusOK, map[string]any{
			"ok": false, "error": err.Error(), "steps": adm.Transcript(), "interfaces": raw,
		})
		return
	}
	s.emitDeviceEvent(r, d, "mgmt.static_owner_probed", model.SevInfo,
		"Static IPv4 owner observed as "+itoa(owner)+" on \""+d.Name+"\" (temporary; not saved)")
	respond(w, http.StatusOK, map[string]any{
		"ok": true, "owner": owner, "interfaces": raw, "steps": adm.Transcript(),
		"note": "not saved to flash — temporary interface deleted after observation",
	})
}

func intentToWriterWant(a model.DeviceAddressing) driver.MgmtAddrIntent {
	var want driver.MgmtAddrIntent
	switch a.Mode {
	case model.MgmtAddrStatic:
		want.Mode = driver.MgmtAddrModeStatic
		want.Address = a.StaticIP
		want.Gateway = a.StaticGateway
		want.DNS = a.StaticDNS
	case model.MgmtAddrDHCP:
		want.Mode = driver.MgmtAddrModeDHCP
	}
	return want
}

// mgmtHostFromAddr extracts the host from "10.99.0.36/24" or "10.99.0.36".
func mgmtHostFromAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	var p, err = netip.ParsePrefix(addr)
	if err == nil {
		return p.Addr().String()
	}
	var a netip.Addr
	a, err = netip.ParseAddr(addr)
	if err == nil {
		return a.String()
	}
	var slash = strings.IndexByte(addr, '/')
	if slash > 0 {
		return addr[:slash]
	}
	return addr
}

// adoptWrittenMgmtIP updates devices.mgmt_ip after meshcraft itself wrote a new
// management address on the device.
//
// THE BUG THIS CLOSES. Apply and jump wrote the new IP on the device, then kept
// dialing the OLD `mgmt_ip`. The device was reachable by hand; meshcraft looked
// offline. Relocate was the only updater.
//
// Covers ALL shipped platforms through the shared API handlers — not per-driver:
//
//	same-VLAN apply  → SwOS, RouterOS, Instant On   (ApplySameVLAN)
//	jump             → SwOS only                     (JumpVLAN)
//	migrate commit   → RouterOS, Instant On          (MigrateVLAN)
//
// Prefer a re-read address when the session survived; otherwise the static intent
// meshcraft just pushed. When the lease is unknown (DHCP, Instant On post-commit
// re-lease), the caller should kick Relocate (cf. kickRelocateAfterMgmtWrite).
//
// Returns the adopted host (empty when unchanged or unknown).
func (s *Server) adoptWrittenMgmtIP(ctx context.Context, d model.Device,
	observedAddr, knownAddr string) (string, error) {
	var host = mgmtHostFromAddr(observedAddr)
	if host == "" {
		host = mgmtHostFromAddr(knownAddr)
	}
	if host == "" || host == d.MgmtIP {
		return "", nil
	}
	var err = s.Store.SetDeviceMgmtIP(ctx, d.ID, host)
	if err != nil {
		return "", err
	}
	if s.Renumber != nil {
		s.Renumber.Forget(d.ID)
	}
	// Proxy still had the OLD address until this push — that was half the
	// "meshcraft lost the device" bug after a successful jump.
	s.pushWatchForDevice(model.Device{
		ID: d.ID, SiteID: d.SiteID, Name: d.Name, MgmtIP: host,
		ProxyID: d.ProxyID, Extra: d.Extra,
	})
	return host, nil
}


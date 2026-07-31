package routeros

import (
	"fmt"
	"sort"
	"strings"
)

// Port CONFIGURATION for VLAN filtering on the bridge: what a port admits on
// ingress, and what this connector refuses to touch.
//
// # WHY THIS FILE EXISTS
//
// The first delivery set two things — the `pvid` of `/interface/bridge/port` and
// the `tagged` / `untagged` lists of `/interface/bridge/vlan` — and stopped there.
// On a CRS3xx, that leaves filtering HALF done, and the missing half is the one
// that decides airtightness:
//
//	`ingress-filtering`  OBSERVED as `false` on all 26 ports of the reference
//	                     device. When false, the bridge table is consulted ONLY ON
//	                     EGRESS: a frame tagged with a VLAN the port is not a
//	                     member of is accepted on ingress, learned, and switched
//	                     toward the members of that VLAN. Segmentation exists in
//	                     one direction only.
//	`frame-types`        OBSERVED as `admit-all` on all 26 ports. An access port
//	                     therefore accepts TAGGED frames from a neighbour, and a
//	                     station that tags its own traffic leaves the VLAN we
//	                     thought we had given it.
//
// MikroTik's documentation is explicit on the first point: « it is better to
// drop packets on an ingress port rather than on an egress port », and
// `ingress-filtering` is what drops traffic for a non-member VLAN as soon as it
// is received. On a Marvell Prestera chip — the one in this product line,
// OBSERVED `Marvell-98DX3236` — both settings stay IN hardware: writing them does
// not cost offload (cf. [hwOffloadNote], which watches for the opposite).
//
// # WHAT THIS FILE WRITES, AND WHAT IT REFUSES TO WRITE
//
// It writes `ingress-filtering=yes` on managed ports, and
// `frame-types=admit-only-untagged-and-priority-tagged` on ACCESS ports only. It
// NEVER writes `admit-only-vlan-tagged`, and the reason is not caution — it is
// that meshcraft's standard model cannot express it (see [frameTypesFor]).
//
// Every tightening is a gesture that DROPS traffic. The two guards that frame it
// are in [planFiltering], and they rest on the same fact as on SwOS: a port that
// carries a LINK may carry the session, and tightening a filter on it without
// having declared what must pass closes administration.

// Values of `frame-types` on `/interface/bridge/port`, as RouterOS names them.
// Written literally rather than built: a misspelled enum value is rejected by the
// device, which is the right outcome, but with a message that does not say which
// of the three was intended.
const (
	// frameAdmitAll: everything is admitted on ingress. The default, OBSERVED on
	// all 26 ports.
	frameAdmitAll = "admit-all"
	// frameAdmitUntagged: TAGGED frames are dropped on ingress. This is what makes
	// a port a true access port.
	frameAdmitUntagged = "admit-only-untagged-and-priority-tagged"
	// frameAdmitTagged: UNTAGGED frames are dropped on ingress. This connector
	// never writes it — see frameTypesFor.
	frameAdmitTagged = "admit-only-vlan-tagged"
)

// PortFiltering is what a port must admit on ingress.
type PortFiltering struct {
	// FrameTypes is the `frame-types` value, or empty for « leave it alone ».
	FrameTypes string
	// Ingress is the `ingress-filtering` value.
	Ingress bool
	// SetIngress says whether there is something to write for `ingress-filtering`.
	//
	// ONE EXTRA BOOLEAN, and for the same reason as on SwOS: the interesting value
	// is `false`, so « field at zero » cannot mean « unchanged ». Confusing the two
	// would TURN OFF ingress filtering on ports we did not mean to touch.
	SetIngress bool
	// Refused carries refusal reasons, in plain language, for the operator.
	Refused []string
}

// frameTypesFor returns the `frame-types` a port must carry, or the empty string
// when there is nothing to write.
//
// # WHY `admit-only-vlan-tagged` IS NEVER WRITTEN
//
// That is the value a « clean » trunk should carry, and it is INEXPRESSIBLE in
// meshcraft's model — not by oversight, by construction:
//
//   - a meshcraft trunk ALWAYS has a native VLAN. When the intent does not name
//     one, `recon.ParsePorts` puts VLAN 1 there, and that is a project rule written
//     at length (docs: default VLAN architecture note §2): « the device
//     always reports a native; if it received none, it reports 1 »;
//   - a native VLAN MEANS « traffic for this VLAN leaves this port without a
//     tag », and therefore also enters without a tag. `admit-only-vlan-tagged`
//     drops precisely those frames.
//
// Writing `admit-only-vlan-tagged` on a trunk would therefore cut the native VLAN
// the plan explicitly asks for. A trunk stays at `admit-all` — what RouterOS
// calls a HYBRID port, and that is exactly what the model describes.
//
// The consequence must be said, not hidden: on this platform, meshcraft cannot
// set a trunk that REFUSES untagged traffic. That is not a capability missing from
// the device; it is an intent the standard model does not have.
func frameTypesFor(want PortState) string {
	switch {
	case want.Mode == "access" && len(want.Tagged) == 0:
		return frameAdmitUntagged
	default:
		// Trunk, or access that still carries tagged traffic: hybrid. We do not
		// touch it — neither to tighten, nor to loosen what an operator set.
		return ""
	}
}

// planFiltering decides what an apply writes in `/interface/bridge/port` beyond
// the PVID, and what it refuses to write there.
//
// # THE TWO GUARDS
//
// Both rest on the same fact: a port that carries a LINK may carry the session,
// and there is no way to know from the device which one carries it.
//
//  1. `ingress-filtering=yes` drops, ON RECEPTION, traffic for every VLAN the
//     port is not a member of. On an active port that is not a member of a
//     management VLAN, administration falls if the session goes through there.
//     The check therefore covers the ARRIVAL membership — the one the apply just
//     wrote — not the previous one: the same gesture declares and filters, in
//     that order;
//  2. `frame-types=admit-only-untagged-and-priority-tagged` drops TAGGED frames.
//     On an active port where a management VLAN arrives tagged, administration
//     falls. And on this platform that case is not theoretical: the reference
//     device is administered through a tagged VLAN (`vlan99` on the bridge,
//     VLAN 99 `tagged: bridge,sfp-sfpplus2`).
//
// The severity is lower than on SwOS — this platform has a serial console AND
// MAC-winbox, both OBSERVED — and that is a reason to stay careful, not a reason
// to be less so.
//
// PURE function: it is the one that decides to tighten a filter on the port that
// carries administration, and no write of this connector has been exercised on
// hardware. It must therefore be testable against the captures.
func planFiltering(name string, want PortState, now BridgePort, members map[int]bool,
	linked bool, protected map[int]string, force bool) PortFiltering {
	var out PortFiltering

	// --- 1. Ingress filtering ---
	//
	// Nothing to write when the port already carries it: on a platform that
	// persists immediately, a `set` that changes nothing is a risk taken for
	// nothing, and it would be taken on EVERY reconciliation pass across the
	// whole fleet.
	if !now.IngressFiltering {
		out.Ingress, out.SetIngress = true, true
	}
	if out.SetIngress && linked && !force {
		var missing = missingProtected(members, protected)
		if len(missing) > 0 {
			out.SetIngress = false
			out.Refused = append(out.Refused, fmt.Sprintf(
				"%s : `ingress-filtering` left INACTIVE. It carries an ACTIVE link and is "+
					"not a member of management VLAN %s in any of the bridge lists: "+
					"enabling ingress filtering would drop traffic for that VLAN on "+
					"reception, and would close administration if the session goes through "+
					"this port. Consequence to know: on this port, the bridge table is "+
					"consulted ONLY ON EGRESS — a frame tagged with a non-member VLAN "+
					"enters, is learned, and leaves toward the members of that VLAN. "+
					"Segmentation is airtight in one direction only.", name, joinInts(missing)))
		}
	}

	// --- 2. Admitted frame types ---
	var frame = frameTypesFor(want)
	if frame == "" || frame == now.FrameTypes {
		return out
	}
	if linked && !force {
		var tagged = protectedTagged(now, protected)
		if len(tagged) > 0 {
			out.Refused = append(out.Refused, fmt.Sprintf(
				"%s : `frame-types` left at « %s ». The plan makes it an ACCESS port, which "+
					"would require dropping tagged frames on ingress — yet management VLAN "+
					"%s arrives TAGGED there and the port carries an ACTIVE link. "+
					"Administration would fall if the session goes through there. Recovery "+
					"on error on this platform: serial console or MAC-winbox.",
				name, now.FrameTypes, joinInts(tagged)))
			return out
		}
	}
	out.FrameTypes = frame
	return out
}

// missingProtected returns the management VLANs the port would NOT be a member of
// after the apply.
//
// Sorted, because the result goes into a message read by a human and compared
// from one reread to the next: map iteration order varies on every run.
func missingProtected(members map[int]bool, protected map[int]string) []int {
	var out []int
	var v int
	for v = range protected {
		if !members[v] {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// protectedTagged returns the management VLANs that arrive TAGGED on this port, as
// the device carries them today.
func protectedTagged(now BridgePort, protected map[int]string) []int {
	var out []int
	var v int
	for _, v = range now.Tagged {
		var _, isProtected = protected[v]
		if isProtected {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// joinInts renders a VLAN list as it should be displayed.
func joinInts(ids []int) string {
	var parts = make([]string, 0, len(ids))
	var v int
	for _, v = range ids {
		parts = append(parts, fmt.Sprintf("%d", v))
	}
	return strings.Join(parts, ", ")
}

// --- Hardware offload, watched and not decided ---

// hwOffloadNote reports bridge ports whose switching is NO LONGER in the chip,
// when others still are.
//
// # WHY THIS IS A NOTE AND NOT A REFUSAL
//
// `hw-offload` is a RESULT, not a setting: RouterOS sets it to false on its own
// as soon as a bridge configuration requires the CPU to see frames. Reading it is
// therefore the only way to notice that offload was lost — and losing it divides
// the throughput of a 24-port gigabit switch by a factor nobody announces.
//
// The comparison is RELATIVE, and that is what makes it usable: a device of which
// NO port is offloaded is probably a model without a switch chip, or a disabled
// bridge, and shouting about it would be shouting permanently. A device of which
// TWENTY ports are offloaded and FOUR are not, on the other hand, carries a
// configuration that cost those four their offload, and that is exactly what an
// operator wants to see.
//
// OBSERVED on the reference device: `"hw":"true"` and `"hw-offload":"true"` on all
// 26 ports, plus `"hw-offload-group":"switch1"` on the one that carries a link.
// This function therefore says nothing about that device, which is the correct
// result.
func hwOffloadNote(s DeviceState) string {
	var offloaded, plain []string
	var name string
	var st BridgePort
	for name, st = range s.Ports {
		if st.HWOffload {
			offloaded = append(offloaded, name)
			continue
		}
		plain = append(plain, name)
	}
	if len(offloaded) == 0 || len(plain) == 0 {
		return ""
	}
	sortPortNames(plain)
	return fmt.Sprintf("HARDWARE OFFLOAD LOST on %d port(s): %s. The other %d ports "+
		"of bridge « %s » switch in the chip (`hw-offload: true`); these go through "+
		"the CPU. `hw-offload` is not a setting but a RESULT: RouterOS sets it to "+
		"false on its own as soon as a bridge configuration requires the CPU to see "+
		"frames — a bridge firewall, `use-ip-firewall`, certain filtering combinations. "+
		"The throughput of these ports is no longer that of the hardware, and nothing "+
		"on the device reports it. meshcraft does not fix it: the cause is a "+
		"configuration the operator wanted, and removing it would be deciding for them.",
		len(plain), strings.Join(plain, ", "), len(offloaded), s.BridgeName)
}

// --- The refusal that protects multi-VLAN rows ---

// whyRowIsUnwritable says why a VLAN's membership cannot be rewritten on this
// record — and returns the empty string when it can.
//
// ONE SINGLE REASON, and it is the most insidious on this platform: the `tagged`
// and `untagged` lists belong to the RECORD, not to each VLAN it declares. A row
// `vlan-ids: "10-20"` declares eleven VLANs and has only one set of lists. Writing
// membership « for VLAN 10 » on it rewrites membership for all ELEVEN, in a single
// `set`, without error and without a trace.
//
// meshcraft reasons per VLAN and cannot compose a membership common to eleven
// VLANs: the only honest gesture is to REFUSE naming the row, and leave the
// operator to split it if they want meshcraft to manage it. Splitting it ourselves
// — replacing one row with eleven — would be rewriting an operator's configuration
// that nobody asked for, on the table that carries administration.
func whyRowIsUnwritable(v BridgeVLAN) string {
	if len(v.IDs) <= 1 {
		return ""
	}
	return fmt.Sprintf("VLAN %d: membership NOT written. Record `%s` of "+
		"`/interface/bridge/vlan` declares %d VLANs at once (`vlan-ids` = %s), and its "+
		"`tagged` / `untagged` lists are shared by all %d: writing membership for "+
		"VLAN %d alone would rewrite that of all the others, in a single gesture and "+
		"without error. meshcraft refuses rather than overreach. For this VLAN to be "+
		"managed, split that row into one entry per VLAN on the device.",
		v.ID, v.RowID, len(v.IDs), joinInts(v.IDs), len(v.IDs), v.ID)
}

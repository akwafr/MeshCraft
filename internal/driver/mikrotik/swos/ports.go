package swos

import (
	"fmt"
	"sort"
)

// The SwOS VLAN model taken seriously: MEMBERSHIP, the NATIVE VLAN, and the
// THREE per-port fields that decide whether all of that acts or decorates.
//
// # WHY THIS FILE EXISTS
//
// The connector's first delivery set two things — the /vlan.b member mask and
// the /fwd.b `dvid` array — and stopped there. That is half of the model.
// /fwd.b carries three OTHER per-port fields, all writable, all already sent
// back to the device on every write (cf. FwdWritableFields), and each able to
// cancel what the first two have just written:
//
//	`vlan` (FieldVLANMode)     — 0 disabled, 1 optional, 2 enabled, 3 strict.
//	                            At `disabled`, the port IGNORES the VLAN table:
//	                            membership and native VLAN are configured and
//	                            WITHOUT EFFECT.
//	`vlni` (FieldVLANReceive)  — 0 any, 1 only tagged, 2 only untagged. An
//	                            access port on "only tagged" drops all its
//	                            traffic.
//	`fvid` (FieldForceVLAN)    — mask. A port on "Force VLAN ID" puts ALL its
//	                            incoming traffic into its native VLAN, tags
//	                            included: it is no longer a trunk, whatever the
//	                            table says about it.
//
// The state read kept quiet about them too, except `vlan`. A UI could
// therefore present a three-VLAN trunk on a port that forces everything into
// VLAN 1.
//
// # WHAT THIS FILE WRITES, AND WHAT IT REFUSES TO WRITE
//
// It writes ONE of these three fields, `vlan`, and only to RAISE it from
// `disabled` to `enabled`. The reasons for not touching the others are in
// [ModeRaise] and [receivePolicy] — they all come down to the same thing: on
// this platform, tightening a filter is a gesture that DROPS traffic, and
// there is neither a recovery console nor a way back.
//
// Source for the semantics of the values: MikroTik's SwOS documentation (CSS610
// range manual, VLAN section), corroborated by the values observed on the
// reference CSS326-24G-2S+ — `vlan:[0x01 × 26]` ("optional", the default),
// `vlni:[0x00 × 26]` ("any"), `fvid:0x00000000` (nobody).

// --- The wanted membership of a port ---

// askedVLANs returns the VLANs a port MUST be a member of according to the
// intent.
//
// The native VLAN counts as a membership, and that is not a subtlety: on
// SwOS, a frame leaves a port only if that port is a member of its VLAN — the
// native VLAN is no exception to the rule, it only decides the TAG. A PVID set
// without membership gives a port that puts its incoming traffic into a VLAN
// nothing comes out of (cf. [vlanAnomalies], which flags it when the device
// already carries it).
func askedVLANs(want PortState) []int {
	var seen = map[int]bool{}
	var out []int
	var add = func(id int) {
		if id <= 0 || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(want.PVID)
	var id int
	for _, id = range want.Tagged {
		add(id)
	}
	sort.Ints(out)
	return out
}

// heldVLANs returns the VLANs a port is a member of IN THE given TABLE.
//
// Read from the table rather than from the standard state, and the
// difference matters: the standard state returns `Tagged`, i.e. membership
// MINUS the native VLAN. Computing a removal from that would suggest a port
// is not a member of its own native VLAN, and so would remove what must be
// kept.
//
// `ok` is false as soon as a mask is unreadable: a removal computed on a
// guessed membership is a random removal, and on this platform a removal
// cuts.
func heldVLANs(table []*Page, index int) ([]int, error) {
	var out []int
	var row *Page
	for _, row = range table {
		var vid, ok = row.Num(FieldVLANID)
		if !ok {
			continue
		}
		var member bool
		member, ok = row.Bit(FieldVLANMembers, index)
		if !ok {
			return nil, fmt.Errorf("/vlan.b: the member mask of VLAN %d is not "+
				"readable at rank %d (field \"%s\" missing or of unexpected shape)",
				vid, index, FieldVLANMembers)
		}
		if member {
			out = append(out, int(vid))
		}
	}
	sort.Ints(out)
	return out, nil
}

// --- Removal, and the three refusals around it ---

// LeaveDecision is what a write does — or refuses to do — with a membership
// the plan no longer asks for.
type LeaveDecision struct {
	// Leave: the VLANs the port actually leaves.
	Leave []int
	// Refused: the removals the write forbids itself, with their reason, in
	// plain words.
	Refused []string
}

// planLeaves decides which memberships a port LOSES.
//
// REMOVAL IS THE MISSING HALF OF CONVERGENCE, and its absence was not
// neutral: a device could be announced "conforming" while carrying VLANs the
// plan had removed months ago. The limit was stated in the UI, which was
// better than silence, but it let the fleet drift in the most expensive
// direction — a VLAN leaking between two zones is not seen, and when it is
// seen, it is while looking for something else.
//
// THREE REFUSALS frame it, and each covers a documented way of locking
// yourself out on a platform that has neither SSH, nor Telnet, nor a serial
// port:
//
//  1. A MANAGEMENT VLAN IS NOT REMOVED FROM A PORT THAT CARRIES A LINK, unless
//     `force`. Neither the site plan's nor the one the device declares:
//     nothing says which of the active ports carries the session — SwOS does
//     not publish it, and `avln` names only a VLAN, not a port.
//
//     THIS REFUSAL USED TO COVER EVERY PORT, including those without a link,
//     on the grounds that "a port without a link today may be tomorrow's
//     uplink, and the benefit of such a removal is nil". Both halves of that
//     sentence were wrong, and the reference fleet showed it on 29 July 2026:
//
//     THE BENEFIT IS NOT NIL, IT IS SECURITY. On SwOS, membership and tagging
//     are the SAME data: leaving the management VLAN a member of an access
//     port means letting any workstation plugged in there reach the
//     administration plane by tagging its own traffic. That is exactly the
//     hole the intent "access VLAN 100" exists to close.
//
//     AND THE COST WAS CONVERGENCE, ENTIRELY. VLAN 99 on the reference device
//     is a member of ALL TWENTY-SIX ports (observed /vlan.b). A write asking
//     for "the 24 copper ports in access VLAN 100" therefore wrote ZERO ports
//     and refused twenty-four times, on every pass, forever: `ports_written`
//     empty, forty-eight deviations nothing could close, and an operator
//     reading "it does not work" — which was the right diagnosis.
//
//     The residual risk, for its part, requires a LINK: a session does not
//     cross an empty socket. That is already the criterion of the two other
//     guards in this file and of [planModeRaise]; applying it here makes the
//     file consistent instead of adding a hypothesis. An active port that is
//     NOT the session's therefore keeps its membership: that is the
//     conservative residue, and it is owned;
//
//  2. NO REMOVAL ON AN ACTIVE PORT when no management VLAN is known
//     (`avln:0x00`, the factory state: "administration accepted from any
//     VLAN"). Nothing then says which port carries the session, and removing
//     a membership from the one carrying it cuts it;
//
//  3. THE EFFECTIVE NATIVE VLAN is never removed. It is in `asked` by
//     construction when the plan names one; this guard covers the case where
//     the plan does NOT name one and the port therefore keeps the device's.
//
// VLAN 1 is never affected: it has no row in the table (cf. implicitVLAN), so
// there is nothing to remove from it.
//
// A PURE function: it is what decides to cut a VLAN on a port, and it must be
// verifiable on the device's real captures rather than on hardware in
// service.
func planLeaves(port string, index int, want PortState, effectivePVID int,
	table []*Page, linked bool, protected map[int]string, force bool) (LeaveDecision, error) {
	var out LeaveDecision

	// `TaggedAtLeast` means unresolved "all" (empty site plan): do not strip
	// tagged membership. A resolved "all" has TaggedAtLeast false and an exact
	// site VLAN list — removals are legitimate.
	if want.TaggedAtLeast {
		return out, nil
	}

	var held, err = heldVLANs(table, index)
	if err != nil {
		return out, err
	}
	var asked = map[int]bool{}
	var v int
	for _, v = range askedVLANs(want) {
		asked[v] = true
	}
	// The effective native VLAN, whether it comes from the plan or the device.
	if effectivePVID > 0 {
		asked[effectivePVID] = true
	}

	var mgmtUnknown = len(protected) == 0
	for _, v = range held {
		if v == implicitVLAN || asked[v] {
			continue
		}
		var why, isProtected = protected[v]
		if isProtected && linked && !force {
			out.Refused = append(out.Refused, fmt.Sprintf(
				"%s stays a member of VLAN %d (%s): it is a MANAGEMENT VLAN and this port "+
					"carries an ACTIVE link. SwOS does not say which of its ports carries the "+
					"administration session — \"%s\" names only a VLAN — so removing this VLAN "+
					"from an active port may be the gesture that cuts access, on a model that has "+
					"neither SSH, nor Telnet, nor a serial port. Ports WITHOUT a link do lose this "+
					"membership: that is what lets an access port really become an "+
					"access port. To remove it here too, it must be forced.",
				port, v, why, FieldAllowVLAN))
			continue
		}
		if mgmtUnknown && linked && !force {
			out.Refused = append(out.Refused, fmt.Sprintf(
				"%s stays a member of VLAN %d: it carries an ACTIVE link and the device "+
					"accepts administration from ANY VLAN (/sys.b \"%s\" = 0), "+
					"so nothing says it is not the one carrying the session. On this "+
					"platform, membership and tagging are the SAME data: removing a "+
					"port from the member list cuts that VLAN on that port, with no half measure.",
				port, v, FieldAllowVLAN))
			continue
		}
		out.Leave = append(out.Leave, v)
	}
	sort.Ints(out.Leave)
	return out, nil
}

// --- The VLAN mode: making what was just written take effect ---

// ModeRaise is the VLAN mode decision for ONE port.
type ModeRaise struct {
	// Mode is the value to write into /fwd.b `vlan`.
	Mode uint32
	// Change says whether there is something to write. False does NOT mean
	// "mode 0": VLANModeDisabled IS zero, and confusing the two would TURN OFF
	// filtering on ports we did not mean to touch — the exact opposite of the
	// purpose of this code.
	Change bool
	// Refused is the reason for the refusal, when there is one.
	Refused string
}

// planModeRaise decides whether to WAKE UP a port's VLAN filtering.
//
// # THE PROBLEM: A SUCCESSFUL WRITE WITH NO EFFECT
//
// A port whose /fwd.b `vlan` is `disabled` IGNORES the VLAN table. meshcraft
// could therefore write a membership and a native VLAN there, read back,
// compare everything, display "conforming" — and the port kept letting
// everything through. The state read SAID so in a note, which was better than
// nothing, but no write fixed it: the operator's only option was to go and
// click in the device's UI, which is exactly what meshcraft exists to avoid.
//
// # WHAT IS WRITTEN: `disabled` → `enabled`, AND NOTHING ELSE
//
// The four modes are not four notches of one setting:
//
//	disabled — the table is not consulted. What is written is decorative.
//	optional — the factory DEFAULT, and the state of all 26 ports of the
//	           reference device. The table is consulted, but a VLAN it does not
//	           declare passes freely.
//	enabled  — EGRESS filtering: a frame leaves a port only if it is a member
//	           of the VLAN.
//	strict   — `enabled` PLUS an INGRESS check: the port must be a member of
//	           the VLAN of the received frame.
//
// Only the move from `disabled` is made, and only up to `enabled`:
//
//   - `optional` → `enabled` TIGHTENS a filter on a port that works today.
//     Every frame of a VLAN the table does not declare stops passing the
//     second it is written. On the reference device, whose 26 ports are on
//     `optional`, nobody asked for that — and meshcraft does not know which
//     undeclared VLANs transit through a port;
//   - `enabled` → `strict` adds an INGRESS filter, i.e. drops frames the port
//     used to accept. MikroTik's documentation recommends it at the end of a
//     configuration; it is addressed to a human watching their switch, not to
//     a reconciliation that goes over two hundred devices every night;
//   - NOTHING IS EVER LOWERED. An operator who set `strict` meant it.
//
// # THE GUARD: DO NOT FILTER WHAT WAS NOT DECLARED
//
// `enabled` filters on egress. Setting it on a port that is NOT a member of a
// management VLAN, or not a member of its own native VLAN, would drop that
// traffic. The check is therefore on the membership AS IT WILL BE AFTER the
// write — `members` is computed on the already-completed table — and not on
// the one before: it is the same gesture that declares and filters, in that
// order.
//
// A port WITHOUT A LINK carries no session: the guard does not apply to it,
// otherwise a new device, all of whose ports are down, would stay forever
// with filtering disabled.
func planModeRaise(port string, current uint32, members map[int]bool, effectivePVID int,
	linked bool, protected map[int]string, force bool) ModeRaise {
	var out ModeRaise
	if current != VLANModeDisabled {
		return out // nothing to wake, and nothing to tighten: see above
	}
	if force {
		out.Mode, out.Change = VLANModeEnabled, true
		return out
	}
	if !linked {
		out.Mode, out.Change = VLANModeEnabled, true
		return out
	}

	// The port carries a link: it may carry the session. Tighten only if
	// everything that must keep leaving this port will leave it.
	if effectivePVID > 0 && effectivePVID != implicitVLAN && !members[effectivePVID] {
		out.Refused = fmt.Sprintf(
			"%s: VLAN filtering left DISABLED (/fwd.b \"%s\" = disabled). Its native "+
				"VLAN is %d and it is not a member of that VLAN in the table: enabling "+
				"egress filtering would drop that traffic, on a port that carries an "+
				"ACTIVE link. Consequence to be aware of: this port's membership and native VLAN "+
				"are written but WITHOUT EFFECT as long as this mode stays disabled.",
			port, FieldVLANMode, effectivePVID)
		return out
	}
	var vlans = sortedKeysInt(protected)
	var v int
	for _, v = range vlans {
		if v == implicitVLAN || members[v] {
			continue
		}
		out.Refused = fmt.Sprintf(
			"%s: VLAN filtering left DISABLED (/fwd.b \"%s\" = disabled). It carries an "+
				"ACTIVE link and is not a member of management VLAN %d (%s): enabling "+
				"egress filtering would cut administration if the session goes through this "+
				"port, and this model has neither SSH, nor Telnet, nor a serial port. The membership "+
				"written on this port therefore stays WITHOUT EFFECT.",
			port, FieldVLANMode, v, protected[v])
		return out
	}
	out.Mode, out.Change = VLANModeEnabled, true
	return out
}

// receivePolicy says why this connector writes NEITHER `vlni` NOR `fvid`.
//
// It is not a capability missing from the platform — both fields exist, are
// writable, and are already sent back to the device on every write with
// their read value. It is a refusal to write, and it deserves to be stated
// rather than left to silence.
//
//	`vlni` ("VLAN Receive") — tightening it is what makes an access port
//	  "clean": "only untagged" makes it drop tagged frames. But meshcraft's
//	  standard model does NOT say an access port refuses tagged traffic, it
//	  says which VLAN leaves untagged; and on this platform administration can
//	  arrive TAGGED on any port, including an access port (that is the case of
//	  the reference device, whose session arrives tagged in 99 on a cage with
//	  native VLAN 1). Setting "only untagged" on an access port is therefore a
//	  way to shut off administration that nothing in the intent would let
//	  anyone foresee.
//	`fvid` ("Force VLAN ID") — it has no equivalent in the standard model: it
//	  puts ALL incoming traffic into the native VLAN, tags included. No
//	  meshcraft intent asks for that, so no write must set it — nor,
//	  symmetrically, remove it from an operator who wanted it.
//
// Both are, on the other hand, READ and flagged when they contradict the
// port's role (cf. [vlanAnomalies]): what we refuse to write, we at least say
// out loud.
func receivePolicy() string {
	return "this connector writes neither \"VLAN Receive\" (/fwd.b " + FieldVLANReceive +
		") nor \"Force VLAN ID\" (/fwd.b " + FieldForceVLAN + "): tightening them drops " +
		"traffic, and the standard model does not express what would be needed to decide it"
}

// --- The read: what three per-port fields say and the state kept quiet ---

// vlanAnomalies returns, as sentences for an operator, the PRESENT AND
// CONTRADICTORY VLAN configurations the standard view cannot express.
//
// All are states a CSS accepts without flagging anything, and each one makes a
// UI showing a port's native VLAN and membership DESCRIBE SOMETHING OTHER than
// what the port does with the traffic. It is the same role as the "filtering
// disabled" note the state read already returns, extended to the three fields
// it did not look at.
//
// A PURE function, verifiable on the captures in testdata/.
//
// THE ORDER GOES FROM MOST SERIOUS TO MOST ANECDOTAL, because a list of notes
// is read from the top: a port that forces all its traffic into a VLAN does
// none of what is displayed for it, a PVID without membership only breaks one
// direction of traffic.
func vlanAnomalies(vlanRows []*Page, fwd *Page, f Facade) []string {
	var out []string
	var ports = f.Ports()

	var dvid, hasDvid = fwd.Nums(FieldDefaultVLAN)
	if !hasDvid {
		return out
	}
	var modes, hasModes = fwd.Nums(FieldVLANMode)
	var recv, hasRecv = fwd.Nums(FieldVLANReceive)

	// The lists are built in FACEPLATE order: an alphabetical sort would put
	// Port10 before Port2, and a note that enumerates twenty-four ports in that
	// order is unreadable, hence equivalent to saying nothing.
	var forced, orphans, unreachable, accessTaggedOnly, trunkUntaggedOnly, strictOrphan []string

	var i int
	for i = 0; i < ports && i < len(dvid); i++ {
		var name = f.Name(i)
		var pvid = int(dvid[i])

		var isForced, forcedOK = fwd.Bit(FieldForceVLAN, i)
		if forcedOK && isForced {
			forced = append(forced, name)
		}

		// The port's membership of its own native VLAN. VLAN 1 has no row in
		// the table and does not need one (cf. implicitVLAN): it therefore
		// counts neither as absent nor as orphaned.
		var row = rowFor(vlanRows, pvid)
		var member bool
		if pvid > 0 && pvid != implicitVLAN {
			if row == nil {
				unreachable = append(unreachable, name)
			} else {
				var ok bool
				member, ok = row.Bit(FieldVLANMembers, i)
				if ok && !member {
					orphans = append(orphans, name)
				}
			}
		}

		// The port's role, as the state read will infer it: a port that carries
		// tagged VLANs is a trunk, the others are access ports.
		var trunk = carriesTagged(vlanRows, i, pvid)
		if hasRecv && i < len(recv) {
			switch recv[i] {
			case 1: // only tagged
				if !trunk {
					accessTaggedOnly = append(accessTaggedOnly, name)
				}
			case 2: // only untagged
				if trunk {
					trunkUntaggedOnly = append(trunkUntaggedOnly, name)
				}
			}
		}

		if hasModes && i < len(modes) && modes[i] == VLANModeStrict &&
			pvid > 0 && pvid != implicitVLAN && (row == nil || !member) {
			strictOrphan = append(strictOrphan, name)
		}
	}

	if len(forced) > 0 {
		out = append(out, "FORCE VLAN ID active on "+joinComma(forced)+" (/fwd.b \""+
			FieldForceVLAN+"\"): these ports put ALL their incoming traffic into their "+
			"native VLAN, tags included — a frame tagged 30 that enters there "+
			"becomes native VLAN traffic. They are therefore NOT trunks, whatever the VLAN "+
			"table says about their membership, and the list of tagged VLANs displayed for them "+
			"describes nothing of what flows. meshcraft never writes this field: it was "+
			"set on the device.")
	}
	if len(strictOrphan) > 0 {
		out = append(out, "STRICT FILTERING WITHOUT MEMBERSHIP on "+joinComma(strictOrphan)+
			" (/fwd.b \""+FieldVLANMode+"\" = strict): strict mode checks "+
			"membership ON INGRESS, and these ports are not members of their own "+
			"native VLAN. Their untagged traffic is therefore dropped on receive — the port is "+
			"configured, active, and mute.")
	}
	if len(unreachable) > 0 {
		out = append(out, "NATIVE VLAN ABSENT FROM THE TABLE on "+joinComma(unreachable)+
			" (/fwd.b \""+FieldDefaultVLAN+"\"): these ports put their untagged traffic "+
			"into a VLAN that has no row in /vlan.b, hence no member. Nothing can "+
			"leave it towards another port. Not to be confused with VLAN 1, which is "+
			"the hardware's implicit default and legitimately has no row.")
	}
	if len(orphans) > 0 {
		out = append(out, "PVID WITHOUT MEMBERSHIP on "+joinComma(orphans)+": these ports "+
			"put their untagged traffic into their native VLAN but do NOT appear in "+
			"that VLAN's member list. On SwOS, a frame leaves a port only "+
			"if it is a member of the VLAN: this traffic enters and leaves nowhere. SwOS "+
			"accepts this configuration without flagging anything; a meshcraft write "+
			"completes it by setting both.")
	}
	if len(accessTaggedOnly) > 0 {
		out = append(out, "ACCESS PORT THAT ACCEPTS ONLY TAGGED on "+
			joinComma(accessTaggedOnly)+" (/fwd.b \""+FieldVLANReceive+"\" = only tagged): "+
			"these ports carry no tagged VLAN and yet drop every untagged "+
			"frame on ingress. A workstation plugged in there has no traffic getting through. "+
			"meshcraft never writes this field.")
	}
	if len(trunkUntaggedOnly) > 0 {
		out = append(out, "TRUNK THAT ACCEPTS ONLY UNTAGGED on "+
			joinComma(trunkUntaggedOnly)+" (/fwd.b \""+FieldVLANReceive+"\" = only "+
			"untagged): these ports are members of tagged VLANs and yet drop every "+
			"tagged frame on ingress. The trunk works in one direction only. meshcraft "+
			"never writes this field.")
	}
	return out
}

// carriesTagged says whether the port of index `i` is a member of at least one
// VLAN other than its native VLAN — i.e. whether it carries tagged traffic,
// hence whether the state read will call it a trunk.
func carriesTagged(vlanRows []*Page, i, pvid int) bool {
	var row *Page
	for _, row = range vlanRows {
		var vid, ok = row.Num(FieldVLANID)
		if !ok || int(vid) == pvid {
			continue
		}
		var member bool
		member, ok = row.Bit(FieldVLANMembers, i)
		if ok && member {
			return true
		}
	}
	return false
}

// sortedKeysInt returns the integer keys of a map, sorted.
//
// Sorted because these keys are used to compose messages meant for a human
// and compared from one run to the next: Go's map iteration order varies on
// every run, and would make a refusal that does not change change.
func sortedKeysInt(m map[int]string) []int {
	var out = make([]int, 0, len(m))
	var k int
	for k = range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

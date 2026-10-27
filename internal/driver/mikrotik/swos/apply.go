package swos

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Writing port VLAN membership.
//
// ┌───────────────────────────────────────────────────────────────────────────┐
// │ VERIFIED ON HARDWARE ON JULY 29, 2026, and what follows keeps both        │
// │ halves: the write path IS exercised, and the original doc comment stays   │
// │ readable for what isn't.                                                   │
// │                                                                            │
// │ WHAT IS VERIFIED: setting VLAN membership and native VLAN on the          │
// │ fleet's CSS326-24G-2S+ (192.0.2.36). Twenty-four ports written, no        │
// │ refusal, the VLAN 99 mask goes from 26 members to 2, device still         │
// │ reachable, zero drift on re-read. The /vlan.b table and the /fwd.b page   │
// │ do therefore accept the body this file composes.                          │
// │                                                                            │
// │ WHAT IS NOT: waking up filtering (`disabled` → `enabled`), since no       │
// │ port in the fleet is `disabled`; restoring a backup; and the              │
// │ administration password (admin.go).                                       │
// │                                                                            │
// │ THE ORIGINAL DOC COMMENT, for the record:                                 │
// │                                                                            │
// │ Nothing in this file has ever been run against a device. The gesture is   │
// │ observed to the letter in the JavaScript of a CSS326-24G-2S+'s embedded   │
// │ interface running SwOS 2.17, and the produced bodies are verified against │
// │ real captures from that device (see apply_test.go, which compares the     │
// │ generated body against the page served by the switch). But the tool      │
// │ available to reach it through the tunnel is READ ONLY, by architectural   │
// │ decision of the project: "writing to a device goes through a              │
// │ connector, not through this tool".                                        │
// │                                                                            │
// │ The write campaign is to be carried out after integration. The exact      │
// │ commands are in the delivery report.                                      │
// └───────────────────────────────────────────────────────────────────────────┘
//
// THREE RULES GOVERN THIS FILE, AND NONE IS NEGOTIABLE
//
// 1. READ, MODIFY, REWRITE — never a delta.
//
// SwOS does not accept partial writes. Its interface recomposes the ENTIRE object
// of the page from all its modifiable fields and posts it as-is; there is
// no "add", no "remove", no "modify this field". Posting /fwd.b without its
// `srt` array would leave the device free to reset the storm
// limits of its twenty-six ports when setting a VLAN. Every write therefore RE-READS
// the page, touches only what it must, and sends back the complete set — whose
// list is that of the device's configuration backup (cf. pages.go).
//
// 2. A TABLE IS WRITTEN WHOLE, so omitting a row DELETES it.
//
// The VLAN table is posted as a complete list. That's the intended behavior
// when removing a VLAN, and a service outage when it was forgotten. All
// rows that were read are therefore kept exactly as they are, including fields we
// don't understand.
//
// 3. THE VLAN CARRYING ADMINISTRATION IS NEVER TOUCHED.
//
// This isn't a temporary limitation to be lifted for convenience. This model has
// NEITHER SSH, NOR TELNET, NOR A SERIAL PORT: a write that removes the uplink port
// from its management VLAN makes the device permanently unreachable, and the only
// recourse is a site visit with a paperclip. And unlike the Instant On, there isn't
// even the "apply then save" safety net either — on SwOS everything is persistent
// immediately, and a power outage cancels nothing.
//
// The only safety net that exists is a configuration backup taken BEFORE, and
// ApplyPorts takes it.

// `ApplyResult` is no longer defined here: it's an ALIAS of the contract type (cf.
// contract.go). Its `Backup` field exists FOR THIS PLATFORM SPECIFICALLY — it's spelled
// out in internal/driver/state.go: "Exists for one platform and one
// reason: SwOS applies and persists in a single step, a power outage
// cancels nothing, and this copy is the ONLY way back that exists." What
// ApplyPorts does with it therefore hasn't changed, and must not change: the copy is taken
// right before the FIRST write, and it is not reconstituted afterward.

// ApplyPorts sets port VLAN membership on the device.
//
// The order: re-read state, complete the VLAN table, decide, take a
// backup, write. Completing the table BEFORE assigning ports to it isn't a
// preference — a port can only be a member of a declared VLAN, and the reverse order
// would leave half the plan applied. And the backup comes right before the
// first write, not before the decision: when there is nothing to write, there is
// nothing to back up.
//
// THE SAFEGUARD is in PlanPorts, which is PURE and therefore testable against real
// captures. Its rule is specific to SwOS and cannot be deduced from the Instant On's:
// what's protected isn't "the native VLAN is the management one" but
// "the tagging of the management VLAN on this port doesn't change". Read its
// doc comment — the naive version protected NOTHING on the reference device,
// whose session arrives tagged on a cage with native VLAN 1.
//
// TWO VLANs are considered management VLANs: the one the site plan wants,
// and the one the DEVICE declares. They differ as long as a device hasn't been
// migrated, and protecting only the first would allow rewriting the one that carries
// the session.
//
// `plan.Force` lifts the safeguard. Never enabled by default, nor by an
// automatic run: it's an explicit gesture from the operator, who knows their topology
// better than meshcraft and accepts the risk of a site visit.
//
// PORT NAMING IS NOT A PARAMETER. It comes from the GEOMETRY read at that instant
// from /link.b (cf. [Facade]), so from the same snapshot as the state read that
// produced the UI. Passing it on every call would let the read and the write diverge
// by a hair — and on a platform that NUMBERS its ports, "SFP1" translated with the
// wrong front panel is a write to the wrong port, i.e. a cut-off client.
func (a *Admin) ApplyPorts(ctx context.Context, plan driver.PortPlan) (ApplyResult, error) {
	var out ApplyResult
	var desired = driver.ExpandAllTrunks(plan.Desired, plan.SiteVLANs)
	var force = plan.Force

	// 1. We never operate against cached state: the four pages are re-read
	//    NOW. They are read in ONE pass, and the same snapshot is then used to
	//    compose the writes — on a platform where each page costs two
	//    HTTP requests and where accesses are serialized, requesting it again would mean
	//    paying for it twice for nothing.
	var snap, err = a.readAll(ctx)
	if err != nil {
		return out, err
	}
	var ports = snap.ports
	var current State
	current, err = a.stateOf(snap)
	if err != nil {
		return out, err
	}

	var protected = map[int]string{}
	if plan.MgmtVLAN > 0 {
		protected[plan.MgmtVLAN] = "management VLAN from the site plan"
	}
	if current.MgmtVLAN > 0 {
		protected[current.MgmtVLAN] = "management VLAN declared by the device"
	}
	// Having no known management VLAN is a LEGITIMATE case on SwOS, unlike
	// on the Instant On: `avln` at 0 means "administration accepted from any
	// VLAN whatsoever", and a device fresh out of the factory is in that state. Refusing
	// to write would prevent adopting a brand-new CSS.
	//
	// Protection then falls back to the LINK alone, and this is told to the operator:
	// every active port is spared, which is more conservative than the nominal case.
	var mgmtUnknown = len(protected) == 0
	if mgmtUnknown {
		a.step(true, "no management VLAN to spare",
			"the device accepts administration from any VLAN whatsoever: "+
				"EVERY port carrying an active link will be spared, for lack of knowing which one "+
				"carries the session")
	}

	// 2. Complete the VLAN table.
	//
	//    The SITE's VLAN plan is applied in full, not just the VLANs a port
	//    uses: an adopted device must know its site's VLANs, without
	//    which assigning a port to one from the device's interface — or plugging in a
	//    trunk — would offer nothing.
	//
	//    THE MANAGEMENT VLAN COMES FIRST, and it is never left orphaned:
	//    see wantedVLANs. When it's missing from the table, the created row automatically
	//    receives the ports that carry a LINK — without which creating it would be the gesture that
	//    cuts off access (see mgmtSeed).
	var wanted = wantedVLANs(desired, plan.SiteVLANs, protected)
	var seed = mgmtSeed(current, protected, snap.vlanRows, snap.facade)
	var newRows, created, buildErr = withVLANs(snap.vlanRows, wanted, ports, seed)
	if buildErr != nil {
		a.step(false, "VLAN table not modifiable", buildErr.Error())
		return out, buildErr
	}
	var vid int
	for _, vid = range created {
		var members = seed[vid]
		if len(members) == 0 {
			continue
		}
		var note = fmt.Sprintf("management VLAN %d absent from the table: the created row "+
			"automatically receives the %d port(s) carrying a link. Creating a "+
			"management VLAN with NO members would make it filtered with no way out — on a "+
			"port in \"optional\" mode, a VLAN that isn't declared passes freely, and "+
			"declaring it empty is therefore what cuts off access. For lack of knowing which one carries the "+
			"session, all active ports are kept.", vid, len(members))
		out.Skipped = append(out.Skipped, note)
		a.step(true, "management VLAN row created with the active ports", note)
	}

	// 3. Set port membership IN the table, native VLANs, and VLAN MODE
	//    in /fwd.b. Three halves of the same gesture: one says who's a member,
	//    another which VLAN goes out untagged, the third whether the port even looks at the table.
	var fwd = snap.fwd
	var dvid, hasDvid = fwd.Nums(FieldDefaultVLAN)
	if !hasDvid || len(dvid) < ports {
		var e = fmt.Errorf("/fwd.b: array %q absent or too short (%d values "+
			"for %d ports) — write refused, setting a native VLAN blindly "+
			"would move traffic on ports we haven't read", FieldDefaultVLAN,
			len(dvid), ports)
		a.step(false, "native VLANs unreadable", e.Error())
		return out, e
	}
	// The VLAN mode is OPTIONAL to write, and that must be said here: its
	// absence doesn't prevent setting membership, it only prevents
	// waking up filtering. Refusing the entire write over that would mean refusing to
	// configure a switch just because we can't configure everything.
	var modes, hasModes = fwd.Nums(FieldVLANMode)
	if !hasModes || len(modes) < ports {
		modes = nil
	}

	var decided, planErr = PlanPorts(PortsInput{
		Current: current, Table: newRows, Desired: desired, Facade: snap.facade,
		Modes: modes, Protected: protected, Force: force,
	})
	if planErr != nil {
		a.step(false, "membership not settable", planErr.Error())
		return out, planErr
	}
	out.Skipped = append(out.Skipped, decided.Skipped...)
	var changes = decided.Changes
	var s string
	for _, s = range decided.Skipped {
		a.step(false, "port spared by the safeguard", s)
	}

	// Apply the plan to the snapshot, IN THIS ORDER: first the memberships
	// gained, then those lost, finally the native VLANs and the mode.
	//
	// "ADD BEFORE REMOVING" isn't elegance for its own sake: the table is written in a single
	// POST, so the order inside this loop isn't visible on the wire. It's visible
	// in the RESULT — a port that must move from VLAN 50 to VLAN 60 is a member
	// of both at the moment of computation, never of neither. On a platform where
	// membership and tagging are the same data, this "moment of neither" is what
	// cuts off a client if writing stops halfway.
	var touched bool
	var ch PortChange
	for _, ch = range changes {
		for _, vid = range ch.Join {
			var row = rowFor(newRows, vid)
			if row == nil || !row.SetBit(FieldVLANMembers, ch.Index, true) {
				var e = fmt.Errorf("/vlan.b: the member mask of VLAN %d is not "+
					"modifiable (field %q has an unexpected shape)", vid, FieldVLANMembers)
				a.step(false, "membership not settable", e.Error())
				return out, e
			}
		}
		for _, vid = range ch.Leave {
			var row = rowFor(newRows, vid)
			if row == nil {
				continue // the row has disappeared from the table: nothing to remove from it
			}
			if !row.SetBit(FieldVLANMembers, ch.Index, false) {
				var e = fmt.Errorf("/vlan.b: the member mask of VLAN %d is not "+
					"modifiable (field %q has an unexpected shape)", vid, FieldVLANMembers)
				a.step(false, "membership not removable", e.Error())
				return out, e
			}
		}
		if ch.PVID > 0 {
			dvid[ch.Index] = uint32(ch.PVID)
		}
		if ch.RaiseMode && modes != nil {
			modes[ch.Index] = ch.Mode
			a.step(true, "VLAN filtering woken up on "+ch.Port,
				fmt.Sprintf("/fwd.b %q switches from disabled to enabled: without this, this port "+
					"IGNORES the VLAN table and everything written for it stays "+
					"decorative", FieldVLANMode))
		}
		out.PortsWritten = append(out.PortsWritten, ch.Port)
		touched = true
	}

	if !touched && len(created) == 0 {
		a.step(true, "nothing to write",
			"the device already carries the requested VLAN membership and native VLANs — "+
				"no page is posted, which is the intended behavior on a "+
				"platform with no way back")
		return out, nil
	}

	// 4. The backup, right BEFORE the first write — and only if there is
	//    something to write, since one more read isn't free.
	//
	//    On this platform, this is the ONLY possible way back: the
	//    configuration is persistent by default and a power outage
	//    cancels nothing. Its failure doesn't prevent writing — blocking a run for a
	//    safety net would be excessive — but it is logged, because the operator must
	//    know they're working without one.
	out.Backup, err = a.Backup(ctx)
	if err != nil {
		a.step(false, "NO backup available before write",
			"the run continues, but with no possible way back: "+err.Error())
	}

	// 5. Write. The table first: assigning a port to a nonexistent VLAN is refused,
	//    and the reverse order would leave /fwd.b naming a native VLAN the table doesn't
	//    know about.
	if err = a.postRaw(ctx, PageVLAN, EncodeTable(newRows),
		fmt.Sprintf("VLAN table written (%d row(s), %s)", len(newRows),
			createdSummary(created))); err != nil {
		return out, err
	}
	out.VLANsCreated = created

	// Then the native VLANs AND the VLAN mode, in the COMPLETE field set of /fwd.b.
	//
	// AFTER the table, and the order here is really on the wire, so it's real: "declare
	// before filtering". Switching a port from `disabled` to `enabled` activates filtering
	// on egress; doing it before the table carries the membership would drop
	// this port's traffic during the interval between the two requests — and
	// permanently if the second one fails.
	fwd.SetNums(FieldDefaultVLAN, dvid)
	if modes != nil {
		fwd.SetNums(FieldVLANMode, modes)
	}
	var body *Page
	var missing []string
	body, missing = fwd.Only(FwdWritableFields(ports))
	if len(missing) > 0 {
		// Refuse rather than send a truncated page. The device would be free to
		// reset port isolation, storm limits, and ingress rates
		// of all its ports to zero, and it would do so silently.
		var e = fmt.Errorf("/fwd.b: this firmware doesn't serve the fields %s expected by "+
			"the connector — write refused, sending back the page without them would let "+
			"the device reset them to zero", strings.Join(missing, ", "))
		a.step(false, "unexpected /fwd.b field set", e.Error())
		return out, e
	}
	if err = a.postRaw(ctx, PageFwd, body.Encode(),
		fmt.Sprintf("native VLANs written (%d port(s): %s)", len(out.PortsWritten),
			strings.Join(out.PortsWritten, ", "))); err != nil {
		return out, err
	}

	// 6. No backup step: on SwOS, what's posted is already persistent
	//    (cf. Capabilities.Commit). This is the exact opposite of the Instant On, and that's
	//    why the copy taken at step 4 is the only safety net.
	a.step(true, "write complete",
		"SwOS persists immediately: there is no backup to make, and no "+
			"reboot that would cancel this")
	return out, nil
}

// PortChange describes what a run would change on ONE port. Nothing more, and above all
// nothing that doesn't change.
type PortChange struct {
	Port string `json:"port"`
	// Index is the port's SwOS rank, 0-based.
	Index int `json:"index"`
	// Join: the VLANs the port must BECOME a member of. Ones it already is a member
	// of are not listed.
	Join []int `json:"join,omitempty"`
	// Leave: the VLANs the port must LEAVE, because the plan no longer
	// requests them.
	//
	// Never a management VLAN, never the effective native VLAN, never anything on a
	// port set to trunk-all ("all"), and nothing at all on an active port when the device
	// accepts administration from any VLAN whatsoever — [planLeaves] holds these
	// four refusals and explains them.
	Leave []int `json:"leave,omitempty"`
	// PVID: the native VLAN to set. Zero means "unchanged", never "VLAN 0" —
	// SwOS has no VLAN 0, and confusing the two would move a port's traffic.
	PVID int `json:"pvid,omitempty"`
	// Mode is the value to write into /fwd.b `vlan`, and RaiseMode says whether there is
	// anything to write.
	//
	// TWO FIELDS AND NOT ONE, because VLANModeDisabled IS ZERO: a single `Mode`,
	// with zero meaning "unchanged", would TURN OFF VLAN filtering on every port
	// we didn't want to touch — the exact opposite of what this field exists to
	// do. The same trap as `PVID` just above, except here the
	// dangerous value is precisely zero.
	Mode      uint32 `json:"mode,omitempty"`
	RaiseMode bool   `json:"raise_mode,omitempty"`
}

// PortsInput gathers what [PlanPorts] needs in order to decide.
//
// AN AGGREGATE AND NOT EIGHT PARAMETERS, for the same reason [driver.PortPlan]
// carries five: the day the decision needs one more constraint, the signature
// changes in a single place. A positional parameter added in the middle of a list of
// eight is, on top of that, exactly the mistake that lets `ports` slip in where the code
// expects `prefix` — and on a platform that NUMBERS its ports, a mix-up of that
// kind is a write to the wrong port.
type PortsInput struct {
	// Current is the state that was READ, which gives native VLANs and link state.
	Current State
	// Table is the VLAN table ALREADY COMPLETED with missing VLANs: a port can only
	// join a VLAN that exists.
	Table []*Page
	// Desired is the intent, by standard-model port name.
	Desired map[string]PortState
	// Facade describes the front panel AND its naming: how many ports, where the cages are, and
	// the two naming families a plan may use (cf. Facade). It comes from the
	// GEOMETRY READ from the device, never from the caller (cf. ApplyPorts).
	Facade Facade
	// Modes is the /fwd.b `vlan` array as the device served it, or nil when
	// this firmware doesn't serve it. Nil means giving up on waking up filtering —
	// not giving up on setting membership.
	Modes []uint32
	// Protected carries the management VLANs to spare, and the reason for each.
	Protected map[int]string
	// Force lifts the safeguards. An explicit gesture from the operator, never a default.
	Force bool
}

// PortsPlan is what a run would do, and what it refuses to do.
type PortsPlan struct {
	Changes []PortChange
	// Skipped carries the refusals, in sentences meant for the operator: a safeguard that
	// protects without saying so looks like a bug.
	Skipped []string
}

// PlanPorts decides, WITHOUT WRITING ANYTHING, what a run would change and what it
// will refuse to touch.
//
// A PURE function, and that's the whole point: it's the one that decides which port keeps its
// native VLAN, so it's the one that decides whether the device stays reachable. A mistake here
// only becomes visible the moment a switch goes silent, on a model that has neither SSH, nor
// Telnet, nor a serial port. Pulling it out of the network path is what allows testing it
// against real captures from the device (see apply_test.go) rather than against
// live hardware in production.
//
// `in.Table` is the VLAN table ALREADY COMPLETED with missing VLANs: a port can
// only join a VLAN that exists.
//
// What is returned:
//
//   - the changes, in a stable order, and ONLY the ones that move something.
//     A port that's already compliant isn't listed — on this platform writing
//     is persistent by default and with no way back, so a POST that changes
//     nothing is a risk taken for nothing, and it would be taken on EVERY reconciliation
//     pass, across the whole fleet;
//   - the reasons for refusal, in plain language, meant for the operator.
//
// FOUR DECISIONS PER PORT, not just two anymore. The first delivery only computed
// membership to GAIN and the native VLAN; now added are membership to
// LOSE ([planLeaves]) and waking up filtering ([planModeRaise]). The two
// missing ones were the ones that decided whether the run CONVERGES and whether it ACTS.
func PlanPorts(in PortsInput) (PortsPlan, error) {
	var out PortsPlan
	// Derived rather than passed in: two parameters that must stay consistent are two
	// parameters where one can lie, and this one decides whether an active port is spared.
	var mgmtUnknown = len(in.Protected) == 0

	var name string
	for _, name = range sortedNames(in.Desired) {
		var want = in.Desired[name]
		if want.Mode == "" || want.Mode == "libre" {
			continue // "absent = not managed": meshcraft doesn't claim this port
		}
		var index, ok = in.Facade.Index(name)
		if !ok {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s ignored: this name doesn't designate a port on this device, which has %d and "+
					"names them %q … %q (the old numbering %q remains accepted). "+
					"Sending a wrong port number is a write to the wrong place.",
				name, in.Facade.Ports(), in.Facade.Name(0),
				in.Facade.Name(in.Facade.Ports()-1), in.Facade.legacy))
			continue
		}

		// THE CANONICAL NAME, i.e. the FACADE's, so the device's own.
		//
		// A plan may designate a port under the old continuous numbering ("GE25")
		// and [Facade.Index] accepts it; the state that was READ, though, is always named by the front panel
		// ("SFP1"). Looking up state with the plan's name would therefore make the port UNKNOWN —
		// PVID 0, no VLAN, NO LINK — and "no link" is precisely what
		// LIFTS this file's safeguards. An intent inherited from the old convention
		// would thereby have disarmed the protection of the port carrying the session, on the
		// only platform in the fleet that has neither SSH, nor Telnet, nor a serial port.
		//
		// It's also this name that goes into the messages and into `PortsWritten`: what
		// the operator must read is the name their switch displays.
		var canon = in.Facade.Name(index)
		var now = in.Current.Ports[canon]
		var linked = in.Current.HasLink(canon)
		// The EFFECTIVE native VLAN, i.e. the one the port will have at the end: the
		// plan's when it designates one, the device's otherwise. This is what serves as
		// the reference for everything else — membership to keep, mode to wake up — because
		// the run must reason about the ARRIVAL state, not the starting one.
		var effectivePVID = now.PVID
		var nativeFrozen bool
		if !in.Force && linked && want.PVID != now.PVID {
			var refusal = whyNativeChangeIsUnsafe(canon, now.PVID, want.PVID,
				in.Protected, mgmtUnknown)
			if refusal != "" {
				out.Skipped = append(out.Skipped, refusal)
				// THE PORT IS FROZEN ENTIRELY, not just its native VLAN.
				//
				// A version that refused only the native-VLAN change and continued with
				// membership produced exactly the inconsistency that the state read
				// flags as an anomaly: a port whose native VLAN stays 99 while its
				// membership is rewritten for VLAN 100. Untagged traffic then enters
				// on 99 on a port that's no longer a member of 99, and nothing goes out.
				// On the port carrying the session, that's the exact outage this safeguard
				// exists to prevent — it would have caused it itself.
				nativeFrozen = true
			}
		}
		if nativeFrozen {
			continue
		}
		if want.PVID > 0 {
			effectivePVID = want.PVID
		}

		// The membership GAINED: the port must be a member of its native VLAN AND of each
		// of its tagged VLANs.
		var ch = PortChange{Port: canon, Index: index}
		var members = map[int]bool{}
		var vid int
		for _, vid = range askedVLANs(want) {
			// VLAN 1 is NOT in the table, and it doesn't need to be: see
			// implicitVLAN.
			if vid == implicitVLAN {
				members[vid] = true
				continue
			}
			var row = rowFor(in.Table, vid)
			if row == nil {
				// The table was completed upstream: reaching here would mean a
				// VLAN claimed by a port failed to be added to it.
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"%s: VLAN %d is not in the table — membership not applied", canon, vid))
				continue
			}
			var already, readable = row.Bit(FieldVLANMembers, index)
			if !readable {
				return PortsPlan{}, fmt.Errorf("/vlan.b: the member mask of VLAN %d "+
					"is not readable at this port's rank %s (field %q absent or of an "+
					"unexpected shape) — membership would be guessed", vid, canon, FieldVLANMembers)
			}
			members[vid] = true
			if !already {
				ch.Join = append(ch.Join, vid)
			}
		}

		// The membership LOST, with its four refusals.
		var leaves, leaveErr = planLeaves(canon, index, want, effectivePVID, in.Table,
			linked, in.Protected, in.Force)
		if leaveErr != nil {
			return PortsPlan{}, leaveErr
		}
		ch.Leave = leaves.Leave
		out.Skipped = append(out.Skipped, leaves.Refused...)

		if want.PVID > 0 && now.PVID != want.PVID {
			ch.PVID = want.PVID
		}

		// Waking up filtering, decided on the ARRIVAL membership.
		if in.Modes != nil && index < len(in.Modes) {
			// `members` only carries what the plan asks for; the arrival membership
			// also includes what the port keeps. Without this merge, a port whose plan
			// doesn't restate the management VLAN would look like it's not a member, and waking
			// it up would be refused on every active port of a correctly configured fleet.
			var held, heldErr = heldVLANs(in.Table, index)
			if heldErr != nil {
				return PortsPlan{}, heldErr
			}
			var leaving = map[int]bool{}
			for _, vid = range ch.Leave {
				leaving[vid] = true
			}
			for _, vid = range held {
				if !leaving[vid] {
					members[vid] = true
				}
			}
			var raise = planModeRaise(canon, in.Modes[index], members, effectivePVID,
				linked, in.Protected, in.Force)
			if raise.Refused != "" {
				out.Skipped = append(out.Skipped, raise.Refused)
			}
			ch.Mode, ch.RaiseMode = raise.Mode, raise.Change
		}

		if len(ch.Join) == 0 && len(ch.Leave) == 0 && ch.PVID == 0 && !ch.RaiseMode {
			continue // already compliant: sending nothing beats a round trip
		}
		out.Changes = append(out.Changes, ch)
	}
	return out, nil
}

// implicitVLAN is VLAN 1, and it is NOT a row in the VLAN table.
//
// OBSERVED on the reference device, and it's a discovery that changes the
// safeguard: its two SFP cages have a "Default VLAN ID" of 1 (/fwd.b
// `dvid[24]=dvid[25]=0x01`) even though its VLAN table contains ONLY VLANs 99
// and 100. VLAN 1 therefore exists as the hardware's implicit default, with no row.
//
// Two consequences, and both matter:
//
//   - a port whose native VLAN is 1 has NO membership at all to set. Looking for a
//     row for VLAN 1 and complaining about its absence would make the run fail on a
//     perfectly configured device — that's what the first version did;
//   - that row must ABSOLUTELY NOT be created. Creating it would turn VLAN 1 into a
//     declared VLAN, of which only the listed ports would be members — i.e.
//     it would change the behavior of all of the device's untagged traffic, when
//     no one requested that. The Instant On connector also skips VLANs
//     less than or equal to 1, for a related reason.
const implicitVLAN = 1

// whyNativeChangeIsUnsafe says why changing the native VLAN of an ACTIVE port would be
// dangerous — and returns an empty string when it isn't.
//
// SwOS's SAFEGUARD IS NOT THE INSTANT ON's, and it's what was observed on
// hardware that showed it.
//
// On the Instant On, the rule is "don't touch the native VLAN of an active port that
// is an untagged member of the management VLAN". Transposed as-is here, it
// protects NOTHING: on the reference device, the administration session arrives
// TAGGED on VLAN 99 on a cage whose native VLAN is 1. This port's native VLAN
// is therefore not the management VLAN, and the Instant On's rule would have let it
// be rewritten freely.
//
// What actually matters on SwOS is the TAGGING RULE: a frame leaves a
// port untagged if its VLAN is that port's native VLAN, tagged otherwise. Changing a
// port's native VLAN therefore changes the tagging of TWO VLANs — the old native one becomes
// tagged, the new one becomes untagged — and leaves all the others unchanged.
//
// The safety condition is therefore precise: the change must preserve the
// tagging of EVERY management VLAN on this port. In other words, for each
// protected VLAN v, "v is untagged on this port" must have the same value before and
// after.
//
// What this produces in practice, and it's exactly the intended behavior:
//
//   - switching an uplink cage from native VLAN 1 to native VLAN 100, while
//     administration arrives tagged on 99: ALLOWED. 99 stays tagged both before and
//     after, the session doesn't move;
//   - switching that same cage to native VLAN 99: REFUSED. Administration
//     would go from tagged to untagged, and the neighbor sending it tagged would no longer
//     be heard;
//   - switching an access port carrying administration UNTAGGED to
//     another VLAN: REFUSED. This is the classic case, the one the Instant On's rule
//     already covered.
//
// And when no management VLAN is known, nothing is provable: every active port
// is spared.
func whyNativeChangeIsUnsafe(port string, nowPVID, wantPVID int,
	protected map[int]string, mgmtUnknown bool) string {
	if mgmtUnknown {
		return fmt.Sprintf(
			"%s not modified: it carries an ACTIVE link and the device accepts "+
				"administration from any VLAN whatsoever, so nothing says it isn't "+
				"the one carrying the session. This model has no rescue console.", port)
	}
	// Stable order: two runs must produce the same message.
	var vlans = make([]int, 0, len(protected))
	var v int
	for v = range protected {
		vlans = append(vlans, v)
	}
	sort.Ints(vlans)

	for _, v = range vlans {
		var untaggedBefore = nowPVID == v
		var untaggedAfter = wantPVID == v
		if untaggedBefore == untaggedAfter {
			continue // the tagging of this management VLAN doesn't change
		}
		if untaggedBefore {
			return fmt.Sprintf(
				"%s not modified: it carries an ACTIVE link and administration arrives "+
					"UNTAGGED there on VLAN %d (%s). Giving it native VLAN %d would make "+
					"administration go out tagged on this port, and would cut off access. This model "+
					"has neither SSH, nor Telnet, nor a serial port: the mistake gets fixed on site.",
				port, v, protected[v], wantPVID)
		}
		return fmt.Sprintf(
			"%s not modified: it carries an ACTIVE link and administration arrives TAGGED "+
				"there on VLAN %d (%s). Giving it native VLAN %d would make "+
				"administration go out untagged on this port, and would cut off access. This model "+
				"has neither SSH, nor Telnet, nor a serial port: the mistake gets fixed on site.",
			port, v, protected[v], wantPVID)
	}
	return ""
}

// removalPolicy — what the write removes, and what it refuses to remove.
//
// IT REMOVES, NOW, and it must be said why that changed. The first delivery
// removed nothing: on SwOS, membership and tagging are the SAME data —
// removing a port from a VLAN's member list cuts that VLAN on that port, with
// no half measure — the management VLAN lives in the same table as the
// others, and there is no safety net, the write being persistent by default.
// Removing nothing was therefore the cautious choice, and it was SAID in the
// UI.
//
// What it cost, on the other hand, was not visible: a device could be
// announced "conforming" while carrying, for months, a VLAN the plan had
// removed. A leaking segmentation goes unnoticed; when it is noticed, it is
// while looking for something else. "Add only" is not a neutral caution — it
// is a fleet drifting in the most expensive direction.
//
// Removal is therefore done, with FOUR refusals that each cover a documented
// way of locking yourself out (see [planLeaves]): never a management VLAN,
// never the effective native VLAN, never anything on a trunk-all port
// ("all"), and nothing at all on an active port when the device accepts
// administration from any VLAN. Each refusal is returned to the operator with
// its reason — that is what replaces the old general sentence, and it is more
// useful: it said "the write removes nothing", the new ones say "this removal
// was not made, and here is why".
func removalPolicy() string {
	return "removal computed, except on \"all\" ports and on the management VLANs " +
		"of ports that carry a LINK — a port without a link does lose its membership of the " +
		"administration VLAN, otherwise an access port could never become " +
		"one: every refused removal is returned with its reason"
}

// wantedVLANs gathers the VLANs that must appear in the table: those of the
// site plan, those a port asks for, and those carrying ADMINISTRATION.
//
// MANAGEMENT VLANS FIRST, and it is not an ordering preference. A management
// VLAN missing from the table is an ORPHANED management VLAN: the site plan is
// supposed to declare it, but nothing guarantees it — a site imported from a
// hand-made file, a device whose `avln` designates a VLAN nobody modelled, and
// the uplink port's membership no longer has any row to be written in. The
// write then set everything else and left out of the table precisely the VLAN
// it arrived through.
//
// They are added WITHOUT A NAME: `protected` carries a reason meant for the
// operator ("management VLAN from the site plan"), not a device label, and
// writing it into the `nm` field of /vlan.b — 16 bytes — would give a name
// nobody chose. When the site plan names that same VLAN, its name wins,
// because it is added first.
func wantedVLANs(desired map[string]PortState, siteVLANs []VLAN,
	protected map[int]string) []VLAN {
	var byID = map[int]string{}
	var order []int
	var add = func(id int, name string) {
		if id <= 0 {
			return
		}
		var _, seen = byID[id]
		if !seen {
			order = append(order, id)
		}
		// A name already known is not overwritten by an empty one: the site
		// plan names, the port intent does not.
		if byID[id] == "" {
			byID[id] = name
		}
	}
	var v VLAN
	for _, v = range siteVLANs {
		add(v.ID, v.Name)
	}
	var id int
	for _, id = range sortedKeysInt(protected) {
		add(id, "")
	}
	var name string
	for _, name = range sortedNames(desired) {
		var p = desired[name]
		if p.Mode == "" || p.Mode == "libre" {
			continue
		}
		add(p.PVID, "")
		for _, id = range p.Tagged {
			add(id, "")
		}
	}
	sort.Ints(order)
	var out = make([]VLAN, 0, len(order))
	for _, id = range order {
		out = append(out, VLAN{ID: id, Name: byID[id]})
	}
	return out
}

// mgmtSeed says, for each management VLAN ABSENT from the table, which ports
// the row to be created must count among its members.
//
// # THE TRAP THIS FUNCTION EXISTS TO DEFUSE
//
// Creating a VLAN row on SwOS is not a neutral gesture: it is what MOVES that
// VLAN from the "unknown to the table" regime to the "declared, hence
// filtered" regime. On a port in `optional` mode — the factory default, and
// the state of all 26 ports of the reference device — a VLAN the table does
// not declare flows freely; declaring it with an EMPTY member mask drops it
// everywhere, instantly.
//
// In other words: the quickest way to make a CSS mute is to neatly create the
// row of its management VLAN. It is counter-intuitive, it looks like tidying
// up, and it is an outage.
//
// # WHAT IS KEPT, AND WHY IT IS BROAD
//
// Every port that carries a LINK. For lack of knowing which one carries the
// session — SwOS does not say, and `avln` names only a VLAN, not a port — the
// only alternative would be to keep none, i.e. to choose the outage. One
// active port too many in a management VLAN is an excess of scope the
// operator can correct; one active port too few is a site visit.
//
// Management VLANs ALREADY present in the table are not affected: their
// membership is the one the operator set, and [planLeaves] refuses to touch
// it.
//
// A PURE function, verifiable on the captures.
func mgmtSeed(current State, protected map[int]string, rows []*Page,
	f Facade) map[int][]int {
	var out = map[int][]int{}
	var vid int
	for _, vid = range sortedKeysInt(protected) {
		if vid == implicitVLAN || rowFor(rows, vid) != nil {
			continue
		}
		var indices []int
		var i int
		for i = 0; i < f.Ports(); i++ {
			if current.HasLink(f.Name(i)) {
				indices = append(indices, i)
			}
		}
		if len(indices) > 0 {
			out[vid] = indices
		}
	}
	return out
}

// withVLANs completes the read table with the missing VLANs, and returns the
// list TO WRITE.
//
// Existing rows are kept AS THEY ARE, field for field. That is the rule: this
// table is written whole, and what we do not understand is not touched —
// per-VLAN isolation (`piso`), learning (`lrn`), mirroring (`mrr`) and IGMP
// snooping (`igmp`) are set by the operator and are not ours.
//
// CREATED rows take the default values of the device's UI, observed in its
// field definitions, and NOT zeros: `piso` and `lrn` default to 1 (`u:1`),
// and a VLAN created with learning disabled would flood all its traffic.
//
// `seed` maps a VLAN to the port indices whose CREATED row must count as
// members from the outset. It only serves management VLANs (cf. [mgmtSeed]),
// for the least intuitive reason in this file: neatly creating a management
// VLAN's row with an empty mask is what cuts access.
func withVLANs(rows []*Page, wanted []VLAN, ports int,
	seed map[int][]int) ([]*Page, []int, error) {
	var out = append([]*Page{}, rows...)
	var created []int

	var w VLAN
	for _, w = range wanted {
		if w.ID < 1 || w.ID > 4094 {
			return nil, nil, fmt.Errorf("VLAN %d outside the bounds of the SwOS table "+
				"(1 to 4094)", w.ID)
		}
		// VLAN 1 NEVER enters the table: creating it would change the behaviour
		// of all the device's untagged traffic (cf. implicitVLAN).
		if w.ID == implicitVLAN {
			continue
		}
		if rowFor(out, w.ID) != nil {
			continue
		}
		if len(out) >= 250 {
			return nil, nil, fmt.Errorf("the SwOS VLAN table is limited to 250 "+
				"entries and already holds %d: VLAN %d cannot be added",
				len(out), w.ID)
		}
		var row = newVLANRow(w, ports)
		var index int
		for _, index = range seed[w.ID] {
			if !row.SetBit(FieldVLANMembers, index, true) {
				return nil, nil, fmt.Errorf("VLAN %d: the member mask of the row to "+
					"create does not accept the port of rank %d — the row would be created WITHOUT "+
					"members, which on a management VLAN cuts access", w.ID, index)
			}
		}
		out = append(out, row)
		created = append(created, w.ID)
	}
	// Stable order: two runs must produce exactly the same body, otherwise
	// comparing two transcripts no longer means anything.
	sort.SliceStable(out, func(i, j int) bool {
		var a, aOK = out[i].Num(FieldVLANID)
		var b, bOK = out[j].Num(FieldVLANID)
		if !aOK || !bOK {
			return false
		}
		return a < b
	})
	sort.Ints(created)
	return out, created, nil
}

// newVLANRow builds a table row for a VLAN to be created.
//
// The field order and the default values reproduce what the device's UI
// generates. The member mask starts EMPTY: ApplyPorts is what puts the ports
// in it, and a VLAN created with every port a member — which is the UI's
// default (`u:268435455`) — would let all traffic through everywhere.
func newVLANRow(v VLAN, ports int) *Page {
	var row = NewPage()
	row.SetText(FieldVLANName, v.Name)
	// The mask takes the SHAPE the device uses for this number of ports:
	// scalar up to 32, array of words beyond. Otherwise SetBit would refuse,
	// and membership could not be set.
	if ports <= 32 {
		row.SetNum(FieldVLANMembers, 0)
	} else {
		var words = make([]uint32, (ports+31)/32)
		row.SetNums(FieldVLANMembers, words)
	}
	row.SetNum(FieldVLANID, uint32(v.ID))
	// The device UI's defaults: port isolation and learning enabled, mirroring
	// and IGMP snooping disabled.
	row.SetNum("piso", 1)
	row.SetNum("lrn", 1)
	row.SetNum("mrr", 0)
	row.SetNum("igmp", 0)
	return row
}

// rowFor finds a VLAN's row in the table.
func rowFor(rows []*Page, vid int) *Page {
	var r *Page
	for _, r = range rows {
		var v, ok = r.Num(FieldVLANID)
		if ok && int(v) == vid {
			return r
		}
	}
	return nil
}

func sortedNames(m map[string]PortState) []string {
	var out = make([]string, 0, len(m))
	var k string
	for k = range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func createdSummary(created []int) string {
	if len(created) == 0 {
		return "no VLAN created"
	}
	var parts = make([]string, 0, len(created))
	var v int
	for _, v = range created {
		parts = append(parts, strconv.Itoa(v))
	}
	return "VLAN(s) created: " + joinComma(parts)
}

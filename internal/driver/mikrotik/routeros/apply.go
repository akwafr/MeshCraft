package routeros

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
// This is the « Apply » step of the pipeline (docs/01-Architecture/Reconciliation §4 and
// §5), and the most dangerous file in this connector.
//
// ┌─────────────────────────────────────────────────────────────────────────────┐
// │ UNVERIFIED ON HARDWARE. None of the writes in this file has been exercised  │
// │ on a device. The only path to the reference CRS326 from the development     │
// │ host has remained restricted to GET/HEAD, by project architectural decision.  │
// │ This code is built on the OBSERVED shape of records, tested against         │
// │ testdata/ captures, and awaits the orchestrator's write campaign. The exact   │
// │ commands to run are in the delivery report.                                   │
// └─────────────────────────────────────────────────────────────────────────────┘
//
// THREE FUNDAMENTAL DIFFERENCES from placement on an Instant On, which explain why
// this file does not look like its counterpart:
//
//  1. RouterOS wants the COMPLETE list of members of a VLAN, not a delta. Where Instant
//     On accepts « add VLAN 20 to this port » and refuses being sent the entire state,
//     RouterOS knows only a `tagged` field written in bulk. Direct and heavy
//     consequence: every write is an opportunity to LOSE a member we did not intend to
//     touch. All the care in this file is there — see mergeMembers.
//
//  2. PVID is written ELSEWHERE from membership. `/interface/bridge/port` for PVID,
//     `/interface/bridge/vlan` for lists. A complete placement therefore touches two
//     menus, and a placement interrupted between the two leaves a port tagging in a
//     VLAN of which it is not a member — the exact anomaly on the reference device
//     (cf. the header of state.go).
//
//  3. NOTHING TO SAVE, and that is bad news. Instant On applies without persisting: a
//     failed apply cancels on power loss, which makes the operation testable. RouterOS
//     persists IMMEDIATELY (cf. Commit). There is no step 3 « make durable » at the end
//     of this file — because there is no safety net either.
//
// IN RETURN, and it must be said because it changes the gravity of a mistake: this
// platform has a serial console AND MAC-winbox (both OBSERVED). Cutting IP access does
// not force a return to factory settings as on an Instant On. That is a reason to
// stay cautious, not a reason to be less so.

// An apply result is no longer defined here: it is `driver.ApplyResult`, aliased as
// [ApplyResult] by contract.go. It carries one MORE field than this package used to
// define, `Backup`, and it must be said why this connector leaves it EMPTY.
//
// `Backup` exists for SwOS: that platform applies and persists in one gesture, and the
// configuration copy taken just before the write is the ONLY rollback that exists.
// RouterOS persists just as much, but no copy could be TAKEN: `/export` and
// `/system/backup/save` are commands, therefore POSTs, and no write from this connector
// has been exercised on hardware. Returning a non-empty string on the faith of a path
// never taken would announce a safety net that does not exist — which is worse than no
// safety net, since the operator would count on it.
//
// This platform's real fallback is elsewhere, and it is observed: serial console,
// MAC-winbox, and the undo journal `/system/history` whose every entry carries the
// inverse command in plain text. The delivery report says what to exercise so `Backup`
// can be filled in someday.

// ApplyPorts applies port VLAN membership.
//
// `plan.Desired` is indexed by ROUTEROS INTERFACE NAME (`ether3`, `sfp-sfpplus1`) —
// the same names the read returns, without translation (cf. DeviceIdentity.PortNames).
// That removes a whole class of bug: writing on port 3 while believing one writes on
// port 4.
//
// `plan.MgmtVLAN` is the site's plan management VLAN, `plan.SiteVLANs` the VLANs the
// site declares and that must exist on the device even if no port uses them yet.
//
// `plan.Force` lifts the port safeguard. It is never enabled by default nor by automatic
// placement: it is an explicit operator gesture, who knows their topology better than
// meshcraft.
//
// ONE AGGREGATE AND NOT FOUR PARAMETERS, which is the shape the contract imposes: the
// day placement needs one more constraint, all three connectors change their signature
// together or not at all. A parameter added in three places gets forgotten in the third,
// and this platform's placement would write something other than what the screen shows.
func (c *Client) ApplyPorts(ctx context.Context, plan PortPlan) (ApplyResult, error) {
	var out ApplyResult
	// Site VLAN plan from the database is the meaning of « all »: expand here so
	// apply adds AND removes until the device matches, even if the caller left
	// TaggedAtLeast set.
	var desired = driver.ExpandAllTrunks(plan.Desired, plan.SiteVLANs)
	var planVLAN = plan.MgmtVLAN
	var force = plan.Force

	// We NEVER diff against cached state (§2). On this platform there is one more
	// reason: record `.id`s do not survive a reboot, and writing with a stale identifier
	// would touch another row of the table — therefore another port, or another VLAN.
	//
	// It is the RICH read that serves here, not the contract read: without the `.id`s,
	// without the bridge name, without filtering state and without the list of VLANs of
	// which the CPU is a member, no safe write is possible.
	var current, err = c.ReadDeviceState(ctx)
	if err != nil {
		return out, err
	}

	// --- Safeguards, before any write ---

	if current.BridgeName == "" {
		return out, fmt.Errorf("no bridge on this device: there is no switching table "+
			"where to place VLANs")
	}

	var protected = map[int]string{}
	if planVLAN > 0 {
		protected[planVLAN] = "site plan management VLAN"
	}
	if current.MgmtVLAN > 0 {
		protected[current.MgmtVLAN] = "management VLAN declared by the device"
	}
	if len(protected) == 0 {
		// On this platform, not knowing how administration arrives is a firm refusal
		// reason. Administration may arrive untagged through the bridge, through a VLAN
		// interface, or not at all by IP (MAC-winbox): in doubt, rewriting member lists
		// is exactly the gesture that cuts access.
		return out, fmt.Errorf("unknown management VLAN, from plan or device: write "+
			"refused, meshcraft cannot guarantee not cutting access")
	}
	// Plan and device disagree: both stay protected. Say it here (apply knows the
	// plan); the re-read note only states where administration currently arrives.
	if planVLAN > 0 && current.MgmtVLAN > 0 && planVLAN != current.MgmtVLAN {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"Management VLAN mismatch: site plan wants %d, device is reachable via %d. "+
				"Apply protects BOTH — a device not yet migrated is reachable via its own, "+
				"and protecting only the plan would leave the session VLAN open to rewrite. "+
				"Migrate management (address / VLAN interface) when ready, then re-apply.",
			planVLAN, current.MgmtVLAN))
	}

	if !current.VLANFiltering {
		// Writing VLAN entries on a bridge that does not filter is harmless and is
		// the right way to prepare the ground. Filtering is enabled LAST, after
		// membership and per-port filters are in place — enabling it before entries
		// exist cuts everything instantly (cf. step 5).
		c.step(true, fmt.Sprintf("vlan-filtering inactive on %q", current.BridgeName),
			"entries will be written first; filtering is enabled at the end of apply")
	}

	// --- 1. Create missing VLAN entries ---
	//
	// BEFORE assigning ports to them: RouterOS refuses to place a port in a VLAN whose
	// bridge entry does not exist, and the reverse order would leave half the plan
	// applied.
	//
	// The SITE VLAN plan is placed in full, not only VLANs a port uses: an adopted
	// device must know its site's VLANs, otherwise connecting a trunk to it would carry
	// nothing.
	var existing = map[int]BridgeVLAN{}
	var v BridgeVLAN
	for _, v = range current.VLANs {
		existing[v.ID] = v
	}

	var wanted = append([]VLAN{}, plan.SiteVLANs...)
	if planVLAN > 0 {
		// Management VLAN must exist even before any port carries it: the CPU
		// pseudo-port is placed there below, and RouterOS refuses membership on a
		// VLAN with no bridge entry.
		wanted = append(wanted, VLAN{ID: planVLAN})
	}
	var m membership
	for _, m = range membershipFor(desired) {
		wanted = append(wanted, VLAN{ID: m.VLAN})
	}
	var seenWanted = map[int]bool{}
	var want VLAN
	for _, want = range wanted {
		if want.ID < 1 || want.ID > 4094 || seenWanted[want.ID] {
			continue
		}
		seenWanted[want.ID] = true
		var have, present = existing[want.ID]
		if present && !have.Dynamic {
			continue
		}
		if present && have.Dynamic {
			// A DYNAMIC entry (observed: VLAN 1, « added by pvid ») is not an entry to
			// modify — it has no stable `.id` and the device refabricates it at will. A
			// STATIC one must be created alongside, which will take over. Say it in the
			// transcript, because it is counter-intuitive: we « add » a VLAN that already
			// appeared in the read.
			c.step(true, fmt.Sprintf("VLAN %d existed as DYNAMIC", want.ID),
				"a static entry is created alongside: a dynamic entry is neither "+
					"modifiable nor durable")
		}
		var fields = map[string]string{
			"bridge":   current.BridgeName,
			"vlan-ids": strconv.Itoa(want.ID),
		}
		if want.Name != "" {
			fields["comment"] = want.Name
		}
		_, err = c.Add(ctx, "interface/bridge/vlan", fields)
		if err != nil {
			// Idempotent: VLAN 1 is often already present as a dynamic « added by
			// pvid » row, and some firmwares refuse a second static add with
			// « vlan already added ». The VLAN exists — creating it is done.
			if isVLANAlreadyAdded(err) {
				c.step(true, fmt.Sprintf("VLAN %d already present", want.ID),
					err.Error())
				continue
			}
			return out, fmt.Errorf("creating VLAN %d: %w", want.ID, err)
		}
		out.VLANsCreated = append(out.VLANsCreated, want.ID)
	}

	// The read is redone: entries we just created have `.id`s we do not know, and those
	// are what will serve to write member lists. Guessing an identifier the device just
	// assigned would be writing blindly into its VLAN table.
	current, err = c.ReadDeviceState(ctx)
	if err != nil {
		return out, err
	}

	// HARDWARE OFFLOAD is NOT reported here, and that is a choice.
	//
	// A port whose switching has fallen back into the CPU has lost chip throughput, and
	// nothing on the device says so — so it must be said. But not in `Skipped`: the
	// screen attaches a « force » button there, and there is nothing to force. `hw-offload`
	// is a RESULT, not a setting, and its cause is configuration the operator wanted.
	// The sentence therefore comes out through [DeviceState.notes], which the post-apply
	// reread surfaces in `last_apply.notes` — in the right place, with the other facts
	// that demand no decision (cf. hwOffloadNote).

	// --- 2. PVID, port by port ---
	var names = sortedKeys(desired)
	var name string
	var written = map[string]bool{}
	// `frozen` carries ports the safeguard spared, and it is READ by step 3.
	//
	// THE BUG THIS SET CLOSES. A spared port stayed in `managed`: its PVID was not
	// moved, but member lists were rewritten according to intent. Result on a port whose
	// protected PVID 99 and whose plan demanded 100: PVID at 99, removed from VLAN 99's
	// `untagged` list, added to VLAN 100's — i.e. exactly the « PVID without membership »
	// anomaly state read signals as incomplete configuration, caused by the safeguard
	// itself, on the port carrying the session. A spared port must be spared IN FULL.
	var frozen = map[string]bool{}
	for _, name = range names {
		var wantPort = desired[name]
		if wantPort.Mode == "" || wantPort.Mode == "libre" {
			continue // « absent = unmanaged »: meshcraft claims nothing on this port
		}
		var now, known = current.Ports[name]
		if !known {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: this port is not a member of bridge %q on the device. "+
					"Placing a VLAN on it would have no effect — it must first be added to "+
					"the bridge, which meshcraft does not do yet.",
				name, current.BridgeName))
			continue
		}

		// THE PORT SAFEGUARD, at the PORT and not the VLAN.
		//
		// A port carrying an ACTIVE LINK whose PVID is a management VLAN never sees its
		// native VLAN change: it very likely carries the current session. The link
		// condition is what makes the safeguard usable — on a new device, all ports are in
		// the default VLAN which is also administration's, and sparing them all would
		// amount to being able to do nothing.
		var why = protected[now.PVID]
		if why != "" && wantPort.PVID != now.PVID && current.LinkUp[name] && !force {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"%s not modified: it carries an ACTIVE link and its PVID is VLAN %d (%s) — "+
					"changing its native VLAN would very likely cut access. Neither its "+
					"membership nor its ingress filtering are touched either: a half-spared "+
					"port would end up with a PVID on one side and membership on the other, "+
					"which lets neither circulate. Fallback on error on this platform: serial "+
					"console or MAC-winbox.",
				name, now.PVID, why))
			frozen[name] = true
			continue
		}

		if wantPort.PVID > 0 && wantPort.PVID != now.PVID {
			_, err = c.Set(ctx, "interface/bridge/port", now.RowID,
				map[string]string{"pvid": strconv.Itoa(wantPort.PVID)})
			if err != nil {
				return out, fmt.Errorf("PVID of %s: %w", name, err)
			}
			written[name] = true
		}
	}

	// --- 3. Member lists, VLAN by VLAN ---
	//
	// This is where apply safety on this platform is decided: every write replaces an
	// ENTIRE list.
	//
	// TWO SETS OF MANAGED PORTS, not one, because of unresolved « all »
	// (`TaggedAtLeast`). [ExpandAllTrunks] already turned resolved « all » into
	// an exact site VLAN list (add and remove). TaggedAtLeast remains only while
	// the site plan is empty: stripping tagged membership would be guessing.
	//
	// On this platform, « do not remove » is not coded by looking at intent: a removal
	// is what happens by itself when one rewrites a complete list without putting a
	// member back. An unresolved « all » port is therefore declared UNMANAGED for
	// TAGGED list merge, which preserves it.
	//
	// It stays MANAGED for UNTAGGED lists either way: a port has exactly one native
	// VLAN, and moving it means removing it from the old one — otherwise one gets a
	// port untagged member of two VLANs, which RouterOS accepts and whose behavior is
	// defined nowhere.
	var managed = map[string]bool{}
	var managedTagged = map[string]bool{}
	for _, name = range names {
		var p = desired[name]
		if p.Mode == "" || p.Mode == "libre" {
			continue
		}
		// A port FROZEN by the safeguard is not managed for this apply: its lists must
		// not move on one side when its PVID stayed on the other (cf. `frozen`). Treated
		// exactly like a « libre » port, which makes it fall into the category mergeMembers
		// preserves unconditionally.
		if frozen[name] {
			continue
		}
		managed[name] = true
		if p.TaggedAtLeast {
			continue // unresolved « all »: do not manage tagged membership yet
		}
		managedTagged[name] = true
	}

	var current2 = current // readability: state reread after VLAN creation
	// VLANs THE PLAN NO LONGER REQUESTS enter the loop, and that is the missing half of
	// convergence on this platform.
	//
	// On RouterOS, a removal is not written: it is PRODUCED when one rewrites a complete
	// list without putting a member back. A loop that only walked intent VLANs could
	// therefore never remove anything from a VLAN intent no longer mentions — the VLAN
	// simply stayed out of the walk.
	//
	// The concrete case, and it is not rare: an access port moved from VLAN 99 to VLAN
	// 100. Its `pvid` moves, it enters VLAN 100's `untagged` list, and it STAYS in VLAN
	// 99's. An untagged member of two VLANs is behavior RouterOS accepts and nothing
	// defines — in practice, a leak of 99 on an access port of 100.
	var byVLAN = withStaleVLANs(membershipFor(desired), current2, managed, managedTagged)
	// Plan management VLAN always enters the walk: placeCPU must SET the bridge
	// CPU there even when no facade port asks for that VLAN yet.
	if planVLAN > 0 {
		var hasPlan bool
		for _, m = range byVLAN {
			if m.VLAN == planVLAN {
				hasPlan = true
				break
			}
		}
		if !hasPlan {
			byVLAN = append(byVLAN, membership{VLAN: planVLAN})
		}
	}
	for _, m = range byVLAN {
		var entry, found = vlanRowFor(current2, m.VLAN)
		if !found {
			// A DYNAMIC entry is not modifiable, and for a VLAN the plan no longer
			// requests that is NOT a problem: RouterOS refabricates it at will and removes
			// it by itself when the reason that made it appear disappears — exactly what
			// the `pvid` we just moved did. Say that rather than announcing a missing write
			// that has no reason to be.
			if isStale(m) {
				c.step(true, fmt.Sprintf("VLAN %d: membership left to the device", m.VLAN),
					"no static entry to modify; the dynamic entry will disappear by itself "+
						"when no port carries this VLAN as PVID anymore")
				continue
			}
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"VLAN %d: no bridge entry after creation — membership not written. "+
					"Retry manually.", m.VLAN))
			continue
		}
		// ONE ROW DECLARING SEVERAL VLANs IS NOT WRITTEN. Its member lists are common to
		// all its `vlan-ids`: placing one VLAN's membership would rewrite them for all,
		// in one `set`, without error and without trace (cf. whyRowIsUnwritable).
		var unwritable = whyRowIsUnwritable(entry)
		if unwritable != "" {
			out.Skipped = append(out.Skipped, unwritable)
			continue
		}

		// Current lists, as the device carries them.
		var haveTagged = membersOf(current2, m.VLAN, true)
		var haveUntagged = membersOf(current2, m.VLAN, false)

		// THE MERGE, and it is the most important function in this file. Note the TWO sets
		// of managed ports: `managedTagged` excludes unresolved « all » ports, which
		// preserves their tagged membership until the site plan exists (cf. above).
		var newTagged = mergeMembers(haveTagged, m.Tagged, managedTagged)
		var newUntagged = mergeMembers(haveUntagged, m.Untagged, managed)

		// THE CPU ON THE BRIDGE, and it is not optional on the management VLAN.
		//
		// The word `bridge` in a tagged/untagged list IS the CPU. Forgetting it on
		// the management VLAN mutes administration without stopping site traffic —
		// the hardest remote fault on this platform. Preservation alone is not
		// enough: a factory device or a newly created site VLAN may not carry it
		// yet, and apply must SET it (cf. placeCPU).
		newTagged, newUntagged = placeCPU(current2.BridgeName, m.VLAN, planVLAN,
			current2, entry, newTagged, newUntagged)

		// A protected VLAN NEVER loses a member: we only add on the way up.
		//
		// AND THE REFUSAL IS STATED, which was missing. The restriction came out as a
		// TRANSCRIPT line, which the screen shows only when expanding apply detail. Yet it
		// leaves a port actually untagged member of TWO VLANs — the plan's and the old
		// management VLAN — i.e. a leak of the administration VLAN on an access port. That
		// is exactly the kind of drift one must read immediately, not find in a transcript.
		var _, isProtected = protected[m.VLAN]
		if isProtected && !force {
			var keptTagged = union(haveTagged, newTagged)
			var keptUntagged = union(haveUntagged, newUntagged)
			var refused = append(lacking(keptTagged, newTagged), lacking(keptUntagged, newUntagged)...)
			if len(refused) > 0 {
				sortPortNames(refused)
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"VLAN %d (%s): %s remain(s) member(s) there, although the plan no longer "+
						"requests it on these port(s). A management VLAN NEVER loses a member "+
						"without force: removing a port from the VLAN through which "+
						"administration arrives is a gesture whose benefit is nil and whose risk "+
						"is maximal. Consequence to know: these port(s) are members of TWO VLANs "+
						"at once, so VLAN %d continues to circulate there.",
					m.VLAN, protected[m.VLAN], strings.Join(refused, ", "), m.VLAN))
			}
			newTagged, newUntagged = keptTagged, keptUntagged
		}

		if sameMembers(haveTagged, newTagged) && sameMembers(haveUntagged, newUntagged) {
			continue // already conformant: one fewer round trip
		}
		_, err = c.Set(ctx, "interface/bridge/vlan", entry.RowID, map[string]string{
			"tagged":   JoinList(newTagged),
			"untagged": JoinList(newUntagged),
		})
		if err != nil {
			return out, fmt.Errorf("VLAN %d membership: %w", m.VLAN, err)
		}
		var member string
		for _, member = range append(append([]string{}, m.Tagged...), m.Untagged...) {
			written[member] = true
		}
	}

	// --- 4. Ingress filtering, port by port, LAST ---
	//
	// « DECLARE BEFORE FILTERING », and here the order is on the WIRE, therefore real.
	// Setting `ingress-filtering=yes` or
	// `frame-types=admit-only-untagged-and-priority-tagged` DROPS traffic the instant the
	// device responds; doing it before member lists carry membership would cut that port
	// during the interval between the two requests — and permanently if the second failed.
	//
	// The read is redone for the same reason as after step 1: [planFiltering] safeguards
	// are computed on ARRIVAL membership, the one steps 2 and 3 just wrote. Judging them
	// on the previous one would refuse to tighten a port at the precise moment apply gives
	// it the memberships that make tightening safe, and the refusal would repeat on every
	// reconciliation pass without ever succeeding.
	current, err = c.ReadDeviceState(ctx)
	if err != nil {
		return out, err
	}
	for _, name = range names {
		var wantPort = desired[name]
		if wantPort.Mode == "" || wantPort.Mode == "libre" || frozen[name] {
			continue
		}
		var now, known = current.Ports[name]
		if !known {
			continue // already reported at step 2: this port is not in the bridge
		}
		// Arrival membership, as the device carries it NOW, both lists combined: for
		// ingress filtering, being a tagged or untagged member amounts to the same — the
		// frame is admitted.
		var members = map[int]bool{}
		var id int
		for _, id = range append(append([]int{}, now.Tagged...), now.Untagged...) {
			members[id] = true
		}
		if now.PVID > 0 {
			members[now.PVID] = true
		}
		var filtering = planFiltering(name, wantPort, now, members,
			current.LinkUp[name], protected, force)
		out.Skipped = append(out.Skipped, filtering.Refused...)

		var fields = map[string]string{}
		if filtering.SetIngress {
			fields["ingress-filtering"] = boolWord(filtering.Ingress)
		}
		if filtering.FrameTypes != "" {
			fields["frame-types"] = filtering.FrameTypes
		}
		if len(fields) == 0 {
			continue
		}
		_, err = c.Set(ctx, "interface/bridge/port", now.RowID, fields)
		if err != nil {
			return out, fmt.Errorf("ingress filtering of %s: %w", name, err)
		}
		written[name] = true
	}

	// --- 5. Enable vlan-filtering, LAST ---
	//
	// Membership and per-port filters are in place. Enabling filtering BEFORE
	// `/interface/bridge/vlan` entries cuts everything instantly. Enabling it now
	// makes the plan take effect: without this step the bridge would keep letting
	// everything pass everywhere and apply would only write inert configuration.
	if !current.VLANFiltering {
		if current.BridgeRowID == "" {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"`vlan-filtering` left INACTIVE on bridge %q: its `.id` was not readable, "+
					"so meshcraft cannot enable filtering safely.",
				current.BridgeName))
		} else {
			_, err = c.Set(ctx, "interface/bridge", current.BridgeRowID, map[string]string{
				"vlan-filtering": "yes",
			})
			if err != nil {
				return out, fmt.Errorf("enable vlan-filtering on %s: %w", current.BridgeName, err)
			}
			c.step(true, fmt.Sprintf("vlan-filtering enabled on %q", current.BridgeName),
				"membership and per-port filters were written first")
		}
	}

	out.PortsWritten = sortedBoolKeys(written)

	// NO « SAVE » STEP. RouterOS has already written to non-volatile memory when it
	// responded. That is the fundamental difference from Instant On, and it is stated
	// here rather than passed over in silence: there is nothing to confirm, and therefore
	// nothing to cancel on power loss.
	return out, nil
}

// --- The per-VLAN pivot, and list merge ---

// membership is ONE VLAN's membership, as RouterOS arranges it.
//
// meshcraft reasons by PORT (« what does this port carry? »), RouterOS by VLAN (« who is
// a member? »). Writing therefore requires the inverse pivot of the read.
type membership struct {
	VLAN     int
	Tagged   []string
	Untagged []string
}

// membershipFor translates a per-PORT configuration into per-VLAN membership.
//
// PURE function, and that is intentional: it decides which ports end up in which VLAN,
// and a mistake here shows only when a switch goes mute. Tests replay it without a device.
//
// Difference from its Instant On counterpart: PVID here engenders an explicit UNTAGGED
// membership. On RouterOS the two are independent (cf. state.go), and setting a PVID
// without setting membership gives a port that tags its incoming traffic into a VLAN it
// does not egress — the exact defect on the reference device, which a correct apply must
// not reproduce.
func membershipFor(desired map[string]PortState) []membership {
	var byVLAN = map[int]*membership{}
	var at = func(id int) *membership {
		var m, seen = byVLAN[id]
		if !seen {
			m = &membership{VLAN: id}
			byVLAN[id] = m
		}
		return m
	}

	// Stable order: two runs must produce exactly the same requests, otherwise comparing
	// two transcripts no longer means anything.
	var names = sortedKeys(desired)
	var name string
	for _, name = range names {
		var p = desired[name]
		if p.Mode == "" || p.Mode == "libre" {
			continue // « absent = unmanaged »
		}
		if p.PVID > 0 {
			at(p.PVID).Untagged = append(at(p.PVID).Untagged, name)
		}
		var id int
		for _, id = range p.Tagged {
			// A VLAN both native and tagged on the same port makes no sense: native wins.
			// RouterOS would accept both lists, and the port would then receive the same
			// VLAN in two forms — undefined behavior, which we do not write.
			if id == p.PVID {
				continue
			}
			at(id).Tagged = append(at(id).Tagged, name)
		}
	}

	var out = make([]membership, 0, len(byVLAN))
	var id int
	for id = range byVLAN {
		sort.Strings(byVLAN[id].Tagged)
		sort.Strings(byVLAN[id].Untagged)
		out = append(out, *byVLAN[id])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VLAN < out[j].VLAN })
	return out
}

// mergeMembers computes the NEW member list of a VLAN, and it is the function that
// protects everything else.
//
// THE PROBLEM it solves: RouterOS does not accept a delta. Writing VLAN membership means
// replacing the ENTIRE list. Yet that list contains three kinds of members:
//
//   - those meshcraft manages and wants → write;
//   - those meshcraft manages and no longer wants → remove;
//   - those meshcraft DOES NOT MANAGE — a port left « libre » in the plan, a port added
//     manually by the operator, an interface that is not a front-panel port → PRESERVE,
//     absolutely.
//
// Keeping only the first category would be naive behavior, and it would destroy the
// third on every apply: every port an operator configured outside meshcraft would
// disappear from its VLANs on the first apply. That is the translation, on this
// platform, of the standard model's « absent = unmanaged » rule: what meshcraft does not
// claim, it does not touch — and on a full-list write, « do not touch » demands explicit
// effort.
func mergeMembers(have, want []string, managed map[string]bool) []string {
	var out []string
	// Existing members meshcraft does NOT manage stay, in their order.
	var member string
	for _, member = range have {
		if managed[member] {
			continue // managed: its fate is decided by `want`
		}
		out = append(out, member)
	}
	// Then what meshcraft wants.
	for _, member = range want {
		out = insertOnce(out, member)
	}
	sort.Strings(out)
	return out
}

// withStaleVLANs completes the list of VLANs to walk with those MANAGED ports still
// carry and the plan no longer requests.
//
// Without it, a removal was IMPOSSIBLE on this platform, and not because it was refused:
// on RouterOS a removal happens when one rewrites a complete list without putting a
// member back, and a loop that only walks intent VLANs never rewrites the list of a VLAN
// intent no longer mentions. The member therefore stayed in place, forever, with no
// message saying so.
//
// The TAGGED / UNTAGGED asymmetry is that of the rest of the file: an unresolved
// « all » port (`TaggedAtLeast`) is not in `managedTagged`, so its tagged VLANs
// are never considered stale. It stays in `managed` for UNTAGGED, because a port
// has exactly one native VLAN and moving it means removing it from the old one.
//
// Added entries have neither `Tagged` nor `Untagged`: that is what makes mergeMembers
// remove managed members and PRESERVE the others.
func withStaleVLANs(wanted []membership, s DeviceState,
	managed, managedTagged map[string]bool) []membership {
	var asked = map[int]bool{}
	var m membership
	for _, m = range wanted {
		asked[m.VLAN] = true
	}

	var stale = map[int]bool{}
	var name string
	var st BridgePort
	for name, st = range s.Ports {
		if !managed[name] {
			continue // « absent = unmanaged »: we do not touch it
		}
		var id int
		for _, id = range st.Untagged {
			if !asked[id] {
				stale[id] = true
			}
		}
		if !managedTagged[name] {
			continue // unresolved « all »: no tagged VLAN is considered stale
		}
		for _, id = range st.Tagged {
			if !asked[id] {
				stale[id] = true
			}
		}
	}

	var out = append([]membership{}, wanted...)
	var id int
	for id = range stale {
		out = append(out, membership{VLAN: id})
	}
	// Stable order: two runs must produce exactly the same requests, otherwise comparing
	// two transcripts no longer means anything.
	sort.Slice(out, func(i, j int) bool { return out[i].VLAN < out[j].VLAN })
	return out
}

// isStale says a membership to write requests NO members — therefore it comes from
// withStaleVLANs, and it is a pure removal.
//
// The distinction serves the message: « no bridge entry » is an anomaly when the plan
// requests this VLAN, and a non-event when removing a port from it.
func isStale(m membership) bool { return len(m.Tagged) == 0 && len(m.Untagged) == 0 }

// placeCPU puts the bridge CPU into a VLAN's member lists.
//
// PRESERVE wherever it already is (tagged or untagged): losing `bridge` on a VLAN
// that already carries administration closes access without stopping site traffic.
//
// ENSURE on the plan management VLAN: a newly created site VLAN or a factory device
// may not carry the CPU yet. Tagged administration (`/interface/vlan` on the bridge)
// needs `bridge` in `tagged`. Untagged administration (address on the bridge itself)
// needs `bridge` in `untagged` on the bridge PVID.
//
// PURE: the same calculation ApplyPorts uses, testable without a device.
func placeCPU(bridge string, vlanID, planVLAN int, st DeviceState, entry BridgeVLAN,
	tagged, untagged []string) ([]string, []string) {
	if entry.CPUIsMember(bridge, true) {
		tagged = insertOnce(tagged, bridge)
	}
	if entry.CPUIsMember(bridge, false) {
		untagged = insertOnce(untagged, bridge)
	}
	if planVLAN > 0 && vlanID == planVLAN {
		if st.MgmtInterface == st.BridgeName && planVLAN == st.BridgePVID {
			untagged = insertOnce(untagged, bridge)
		} else {
			tagged = insertOnce(tagged, bridge)
		}
	}
	return tagged, untagged
}

// lacking returns elements of `have` that are NOT in `want` — i.e. the removals an
// « additions only » just refused.
//
// Serves to STATE the refusal. Both lists are sorted by functions in this file.
func lacking(have, want []string) []string {
	var seen = map[string]bool{}
	var item string
	for _, item = range want {
		seen[item] = true
	}
	var out []string
	for _, item = range have {
		if !seen[item] {
			out = append(out, item)
		}
	}
	return out
}

// membersOf returns tagged (or untagged) members of a VLAN in the read state, CPU
// pseudo-port EXCLUDED.
//
// The CPU is excluded because it is not a front-panel port and must never be treated as
// one by the merge; it is re-injected separately, without condition, by ApplyPorts.
// Mixing them would make administration's survival depend on a list comparison.
//
// READ FROM THE ROW ITSELF, and no longer reconstructed from the per-PORT view. The
// reconstruction was this file's second member-loss bug, twin of the first: the per-port
// view contains only interfaces that are `/interface/bridge/port` records of the chosen
// bridge. Any other member — an interface removed from the bridge but left in a list, a
// member from firmware that names differently, an aggregate the read did not attach —
// was not there, therefore not in `have`, therefore disappeared from the list on the first
// `set`. Yet this entire file exists NOT to lose members: starting from what the device
// carries is the only way to keep that promise.
func membersOf(s DeviceState, vlanID int, tagged bool) []string {
	var row, found = vlanRowAny(s, vlanID)
	if !found {
		return nil
	}
	var list = row.UntaggedMembers
	if tagged {
		list = row.TaggedMembers
	}
	var out []string
	var member string
	for _, member = range list {
		if member == s.BridgeName {
			continue // the CPU: re-injected separately, without condition
		}
		out = insertOnce(out, member)
	}
	sort.Strings(out)
	return out
}

// vlanRowAny finds a VLAN's bridge entry, preferring the STATIC entry.
//
// Static first because that is what apply WRITES (cf. vlanRowFor): reading members from
// one entry and writing another would merge two unrelated lists. The dynamic entry is
// returned by default, which serves the read — it carries real members, OBSERVED on VLAN 1
// of the reference device — without ever becoming a write target.
func vlanRowAny(s DeviceState, vlanID int) (BridgeVLAN, bool) {
	var fallback BridgeVLAN
	var hasFallback bool
	var v BridgeVLAN
	for _, v = range s.VLANs {
		if v.ID != vlanID {
			continue
		}
		if !v.Dynamic {
			return v, true
		}
		if !hasFallback {
			fallback, hasFallback = v, true
		}
	}
	return fallback, hasFallback
}

// boolWord renders a boolean in RouterOS words. The device serves and expects « true » /
// « false » (observed), never 0/1 nor yes/no.
func boolWord(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// vlanRowFor finds a VLAN's bridge entry in the read state. A DYNAMIC entry is not
// returned: it is not modifiable (cf. VLAN.Dynamic).
func vlanRowFor(s DeviceState, vlanID int) (BridgeVLAN, bool) {
	var v BridgeVLAN
	for _, v = range s.VLANs {
		if v.ID == vlanID && !v.Dynamic && v.RowID != "" {
			return v, true
		}
	}
	return BridgeVLAN{}, false
}

// isVLANAlreadyAdded reports RouterOS refusing /interface/bridge/vlan/add because
// that VLAN id is already on the bridge (dynamic PVID row or a prior static add).
func isVLANAlreadyAdded(err error) bool {
	if err == nil {
		return false
	}
	var msg = strings.ToLower(err.Error())
	return strings.Contains(msg, "vlan already added") ||
		strings.Contains(msg, "already have such vlan")
}

// insertOnce adds an element if not already present.
func insertOnce(list []string, item string) []string {
	var existing string
	for _, existing = range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}

// union returns all elements from both lists, sorted and deduplicated. Serves the
// « additions only » of management VLANs.
func union(a, b []string) []string {
	var out []string
	var item string
	for _, item = range a {
		out = insertOnce(out, item)
	}
	for _, item = range b {
		out = insertOnce(out, item)
	}
	sort.Strings(out)
	return out
}

// sameMembers says whether two lists carry the same elements. Both are assumed sorted by
// the functions above.
func sameMembers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	var i int
	for i = range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sortedKeys returns the keys of a port map, sorted.
func sortedKeys(m map[string]PortState) []string {
	var out = make([]string, 0, len(m))
	var k string
	for k = range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedBoolKeys returns the true keys of a map, sorted.
func sortedBoolKeys(m map[string]bool) []string {
	var out []string
	var k string
	var v bool
	for k, v = range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

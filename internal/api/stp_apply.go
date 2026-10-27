package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// stpPreferenceEmptyNote is what an apply says on a site whose preferred STP
// mode list was deliberately emptied.
//
// It is a named constant rather than an inline string because its CONTENT is
// asserted: until 2026-08-11 it said only "no mode, no MST region", and the
// switch it describes is coarser than that in two ways an operator would only
// find out about on a live network (cf. applyDeviceSTP's comment). Whoever
// widens or narrows what the early return suppresses has to come here, and
// TestTheEmptyPreferenceNoteNamesEverythingItSuppresses is what makes them.
const stpPreferenceEmptyNote = "this site's preferred STP mode list is EXPLICITLY EMPTY: " +
	"the device's spanning tree was left exactly as it is, and NOTHING spanning-tree " +
	"related was sent. That is wider than the mode. It also suppresses the MST region and " +
	"every PER-PORT STP intent — portfast/edge, BPDU guard, path cost — because they reach " +
	"the device on the same plan, so access ports on this site stop receiving BPDU guard " +
	"while the list is empty. And a site that uses MST stops being re-homogenised: when the " +
	"site's VLAN list changes, devices already carrying a meshcraft region keep the OLD one " +
	"and 802.1s silently demotes them to ordinary RSTP neighbours — still forwarding, still " +
	"green, on a tree that is no longer the one the site designed. Add a mode to the site's " +
	"Preferred list to have applies push it again; a single-device push from the STP panel " +
	"still works meanwhile"

// stpAutoWriteRefusal decides whether the AUTOMATIC path may write spanning
// tree, and returns the sentence the operator reads when it may not.
//
// # The rule (ADR-0023)
//
// The automatic path re-confirms the IDENTICAL and nothing else. It writes only
// when the device already carries what the site wants — the same mode, and the
// same region triplet when the site means MSTP — so the lines that go out are
// lines that change nothing: the mode already running, the region already
// carried, a digest that does not move, no MST boundary displaced. Every other
// case waits for the gesture.
//
// # Why this gate exists at all
//
// Because `runApply` calls [Server.applyDeviceSTP] inside "apply the ports of
// this device", and its own comment says it runs even when ApplyPorts failed. So
// a VLAN change on an access port used to write the global mode, the bridge
// priority AND the 802.1s triplet. Three measured consequences, all on the night
// of 2026-10-24:
//
//   - a site rename evicted SEVEN Instant On from their region with no symptom,
//     carried there by port applies, not by any STP gesture (ADR-0021);
//   - core01-arena is CIST root at priority 0 with ten boundary ports, so one
//     port apply on it would have moved seven VLANs from the CIST into instance
//     1 — a topology recalculation on the core of the site (ADR-0022);
//   - sw01-arena took applies, wrote its bridge, and said nothing about the
//     region it was never in (ADR-0022).
//
// What they share: the value written is DERIVED FROM SITE DATA, so nobody ever
// named that device, and the value written is exactly the one whose change
// recalculates a topology.
//
// # What this gate does NOT cover, deliberately
//
// The bridge priority and the Super Root flag. Both are per-device intents
// somebody ticked — the stored flag on the device, or the site's root-bridge
// designation which names devices one by one and carries its own contested-root
// warning and apply order. The principle is "no write that nobody named", not
// "no write at all", so an intent that names this device still rides. The
// boundary is written down in ADR-0023 rather than left to be discovered here.
func stpAutoWriteRefusal(mode string, disable bool, want driver.MSTRegion,
	wantsMST bool, observed driver.SpanningTreeState, readNote string) string {
	var prefix = "spanning tree NOT written automatically (ADR-0023): "
	var suffix = ". Nothing spanning-tree related was sent. Use \"Conform the spanning tree\" " +
		"on the device's spanning-tree banner to apply it deliberately — it " +
		"recalculates the topology, ports go back through listening/learning, " +
		"and the measured cost is 2-5 s unreachable with about 45 s to settle"
	if readNote != "" {
		// A read that did not land cannot prove anything, and the whole gate is a
		// proof. Said with the reader's own sentence: "the mode could not be read"
		// and "this connector cannot read spanning tree" lead to different repairs.
		return prefix + "this device's spanning tree could not be read, so the " +
			"controller cannot prove a write would change nothing — " + readNote + suffix
	}
	if disable {
		if observed.Enabled {
			return prefix + "the site asks for spanning tree to be TURNED OFF and this " +
				"device still runs it. Disabling a bridge's spanning tree is a topology " +
				"event, not a setting" + suffix
		}
		return ""
	}
	if !observed.Enabled {
		return prefix + "the site resolves to \"" + mode + "\" and this device has " +
			"spanning tree OFF. Turning it on makes the device start advertising BPDUs " +
			"and re-elects a root" + suffix
	}
	var have = strings.ToLower(strings.TrimSpace(observed.Mode))
	if have != strings.ToLower(strings.TrimSpace(mode)) {
		var shown = have
		if shown == "" {
			shown = "unknown"
		}
		return prefix + "the site resolves to \"" + mode + "\" and this device reports " +
			"\"" + shown + "\". A mode change reconverges the tree, and on some ranges it " +
			"closes the session before the CLI answers" + suffix
	}
	// THE REGION, and only when the resolved mode is the one that advertises a
	// digest. Under RSTP or STP no MST identity travels on the wire, so a region
	// comparison there would freeze a device over a field nothing reads.
	if mode != driver.STPPrefMSTP || !wantsMST || !want.Configured() {
		return ""
	}
	var view = stpRegionViewOf(want, wantsMST, &observed, true)
	if view == nil || view.Verdict == "match" {
		return ""
	}
	return prefix + "this device is not in the MST region the site wants (" +
		stpRegionDivergenceLine(view) + "). 802.1s compares the name, the revision " +
		"and a digest over the whole VLAN-to-instance table: writing the site's " +
		"triplet here MOVES this device between regions and displaces an MST " +
		"boundary" + suffix
}

// stpRegionDivergenceLine names the divergence in one clause, for the apply
// note. The banner renders the same facts as a table; this is the form that has
// to survive being joined with " · " into a toast.
func stpRegionDivergenceLine(v *STPRegionView) string {
	switch v.Verdict {
	case "not_mstp":
		return "it does not run MSTP at all, so it advertises no MST digest"
	case "unregioned":
		return "it carries no region name — the factory default here is the " +
			"bridge's own address, which makes it a region of one"
	}
	var want = v.WantName
	if want == "" {
		want = "—"
	}
	var have = v.HaveName
	if have == "" {
		have = "—"
	}
	return "wanted \"" + want + "\" revision " + itoa(v.WantRevision) +
		", carries \"" + have + "\" revision " + itoa(v.HaveRevision) +
		"; differs on " + strings.Join(v.Differs, ", ")
}

// applyDeviceSTP resolves the site STP preference against the connector and
// applies it when the Admin implements SpanningTreeApplier and WritesExtra("stp").
//
// SINCE ADR-0023 IT IS NOT A WRITER BY DEFAULT. It reads the tree, compares, and
// writes only what it can prove changes nothing — see [stpAutoWriteRefusal] for
// the rule and the three measured outages behind it. The deliberate write is
// [Server.handleApplyDeviceSTP], which this function never stands in for.
//
// On success, spanning-tree observation is re-read and stored under Extra so the
// topology view reflects what was pushed (not the pre-apply snapshot).
func (s *Server) applyDeviceSTP(ctx context.Context, d model.Device,
	conn driver.Connector, adm driver.Admin) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !conn.WritesExtra("stp") {
		return out, nil
	}
	var sitePlan, err = s.Store.GetSitePlan(ctx, d.SiteID)
	if err != nil {
		return out, err
	}
	var pref = stpPreferenceOf(sitePlan)
	var caps = conn.Capabilities(d.Model).STPModes
	// THE PER-SITE OFF SWITCH, and it is a different answer from "nothing
	// matched". An ABSENT `stp_mode_preference` gives driver.DefaultSTPPreference
	// (stpPreferenceOf); an EXPLICITLY EMPTIED list gives nothing, and means the
	// operator does not want meshcraft deciding this site's spanning tree yet.
	//
	// The case it exists for: a Catalyst deliberately left on (rapid-)PVST+
	// mid-migration under a site that resolves to mstp. This connector has no
	// read-compare no-op path — every apply re-sends the mode — so before this
	// branch existed, ANY port change flipped the mode and reconverged the tree
	// on a live network, with no way to stop it short of un-declaring
	// WritesExtra("stp") in the binary.
	//
	// IT DOES NOT DISARM THE OPERATOR: handleApplyDeviceSTP takes an explicit
	// mode and never consults the preference, so a deliberate push still works
	// on a site whose list is empty. What stops is the automatic RIDE.
	//
	// # THE SWITCH IS COARSER THAN IT LOOKS, and the note below has to say so
	//
	// This early return is the ONLY thing between an apply and
	// [Server.pushDeviceSTP], and pushDeviceSTP is what carries
	// SpanningTreePlan.Ports — portfast/edge, BPDU guard, path cost. So emptying
	// the list to protect one mid-migration Catalyst's GLOBAL mode also stops
	// per-port STP intents reaching every access port on that site. Two gestures
	// that have nothing to do with each other travel on one switch, because they
	// travel on one plan.
	//
	// AND THE MST REGION STOPS BEING RE-HOMOGENISED. [Server.siteMSTRegion]
	// derives the region digest from the site's VLAN list, so devices already
	// carrying a meshcraft region stay in the OLD one while the VLAN list moves
	// on. 802.1s treats a region mismatch as an ordinary RSTP neighbour: the
	// eviction is SILENT, the ports keep forwarding, the device stays green, and
	// the only symptom is a tree that is no longer the one the site designed.
	// That is why the note names it rather than leaving it to be discovered.
	if len(pref) == 0 {
		out.Notes = append(out.Notes, stpPreferenceEmptyNote)
		return out, nil
	}
	var mode string
	var disable bool
	var ok bool
	mode, disable, ok = driver.ResolveSTPMode(pref, caps)
	if !ok {
		out.Notes = append(out.Notes,
			fmt.Sprintf("no preferred STP mode matches this platform (preference %v, caps %v) — left unchanged",
				pref, caps))
		return out, nil
	}
	// THE GATE (ADR-0023). Read the tree, then decide — in that order, and on a
	// FRESH read rather than on the persisted observation.
	//
	// Deciding on `extra.stp_observed` would have been free, and it is the one
	// shortcut that reopens the hole: a stale observation saying "conformant"
	// over a device that has drifted since is precisely the state in which
	// re-emitting the site's identity renames it and splits the region. The
	// proof has to be current to be a proof.
	//
	// The read rides the session [Server.adminFor] already opened — one extra
	// round trip per apply, no extra login, which matters on a range that
	// authenticates and then refuses the channel at four sessions. It also
	// persists, so the banner the operator sees afterwards reads an observation
	// this apply just refreshed.
	var observed, readNote = s.readSpanningTree(ctx, d.ID, adm)
	var refusal = stpAutoWriteRefusal(mode, disable,
		driver.SiteMSTRegion(stpRegionNameOf(sitePlan, d.SiteID),
			stpRegionRevisionOf(sitePlan), s.siteVLANIDs(ctx, d.SiteID)),
		stpPreferenceWantsMST(sitePlan), observed, readNote)
	if refusal != "" {
		out.Notes = append(out.Notes, refusal)
		return out, nil
	}
	// THE STORED SUPER ROOT INTENT IS READ HERE TOO, and that is the point of
	// persisting it: an auto-apply run rebuilds the plan from scratch, and a flag
	// that lived only in the operator's request would be silently dropped the
	// first time a pipeline touched the device — un-electing a root nobody asked
	// to un-elect.
	return s.pushDeviceSTP(ctx, d, conn, adm, mode, disable, stpSuperRootOf(d), nil)
}

// pushDeviceSTP applies an explicit mode (or disable) and refreshes stp_observed.
//
// `superRoot` and `bridgePriority` travel together and are the two halves of one
// decision (cf. driver.SpanningTreePlan.SuperRoot): setting the flag writes
// priority 0, and CLEARING it needs a priority to put back, because meshcraft
// never recorded what the device carried before. The caller is what enforces
// that — see handleApplyDeviceSTP.
func (s *Server) pushDeviceSTP(ctx context.Context, d model.Device,
	conn driver.Connector, adm driver.Admin, mode string, disable bool,
	superRoot bool, bridgePriority *int) (driver.ApplyResult, error) {
	var out driver.ApplyResult
	if !conn.WritesExtra("stp") {
		out.Notes = append(out.Notes, "this platform does not declare STP as writable")
		return out, nil
	}
	var applier, ok = driver.AsSpanningTreeApplier(adm)
	if !ok {
		out.Notes = append(out.Notes,
			"connector declares STP writable but Admin has no ApplySpanningTree")
		return out, nil
	}

	// THE SITE'S ROOT-BRIDGE DESIGNATION IS READ HERE, and this is what turns it
	// from a stored row into configuration (cf. [Server.rootDesignation], which
	// explains why it had no caller until 2026-08-12).
	//
	// It is OR'd with the per-device intent rather than replacing it: the stored
	// flag is what an operator ticked on one switch, the designation is what the
	// site says, and a device can legitimately carry either. What it must never do
	// is un-elect a root because the OTHER source is silent — so neither clears
	// the other here, and clearing stays the explicit, priority-bearing gesture
	// handleApplyDeviceSTP enforces.
	var designated, rootInstances, filterPorts = s.rootDesignation(ctx, d, conn)
	var plan = driver.SpanningTreePlan{
		Enabled:        !disable,
		Mode:           mode,
		Region:         s.siteMSTRegion(ctx, d),
		Ports:          portSTPIntents(d.Ports),
		SuperRoot:      (superRoot || designated) && !disable,
		BridgePriority: bridgePriority,
		// Empty unless the designation named instances; the connector reads empty
		// as "the CIST, or the single tree of a non-MST mode".
		RootInstances: rootInstances,
		// Empty in the ordinary single-root case. When it is not, the connector
		// writes these FIRST and abandons the whole plan if they fail — the
		// ordering is the safety property (driver.RootApplyOrder).
		BPDUFilterPorts: filterPorts,
	}
	var err error
	out, err = applier.ApplySpanningTree(ctx, plan)
	if err != nil {
		return out, err
	}
	// No mandatory "mode pushed" note — success is silent; speak only on drift.

	var st, note = s.readSpanningTree(ctx, d.ID, adm)
	if note != "" {
		out.Notes = append(out.Notes, note)
	} else if !disable && st.Mode != "" && st.Mode != mode {
		out.Notes = append(out.Notes,
			fmt.Sprintf("after push, device reports mode \"%s\" (requested \"%s\")",
				st.Mode, mode))
	} else if disable && st.Enabled {
		out.Notes = append(out.Notes,
			"after push, device still reports spanning tree enabled — check platform mode mapping")
	}
	_ = st
	return out, nil
}

// handleApplyDeviceSTP pushes a spanning-tree mode onto one device.
//
// Body: `{ "mode": "rstp" | "stp" | "mstp" | "none", "super_root": bool,
// "bridge_priority": int }`. Mode must be in the connector's STPModes (or
// "none" when listed). Same privilege as other device writes (site_admin).
//
// # `super_root` is a POINTER, and "absent" is not "false"
//
// The flag is PERSISTED (devices.extra.stp_super_root) so that the checkbox
// renders the intent and so an auto-apply run does not drop it. That makes the
// three-way distinction matter: a request that says nothing about super root
// must leave the stored value alone, or a plain mode change from another screen
// would quietly un-elect the region root.
//
// # Clearing it is REFUSED without a priority, and that is the undo
//
// SuperRoot writes priority 0 and meshcraft has never recorded what the device
// carried before — there is no site-level bridge priority anywhere in this
// codebase to fall back on. So un-ticking the box alone would leave the switch
// at 0 with the intent showing false: the screen would say "not the root" about
// a bridge nothing can outrank. Rather than invent a previous value (32768 is a
// guess, not a memory), the clear is refused until the caller names the priority
// to put back. The UI asks for it; this is where it is enforced.
func (s *Server) handleApplyDeviceSTP(w http.ResponseWriter, r *http.Request) {
	var d, ok = s.deviceForWrite(w, r, "site_admin")
	if !ok {
		return
	}
	var in struct {
		Mode           string `json:"mode"`
		SuperRoot      *bool  `json:"super_root"`
		BridgePriority *int   `json:"bridge_priority"`
	}
	if !decode(w, r, &in) {
		return
	}
	var mode = driver.NormalizeSTPMode(in.Mode)
	if mode == "" {
		respondErr(w, http.StatusBadRequest,
			"mode required: stp, rstp, mstp, or none")
		return
	}

	var conn, connErr = s.connectorFor(r.Context(), d)
	if connErr != nil {
		respondErr(w, http.StatusUnprocessableEntity, connErr.Error())
		return
	}
	if !conn.WritesExtra("stp") {
		respondErr(w, http.StatusUnprocessableEntity,
			"this platform does not declare STP as writable")
		return
	}
	var caps = conn.Capabilities(d.Model).STPModes
	var allowed bool
	var c string
	for _, c = range caps {
		if c == mode {
			allowed = true
			break
		}
	}
	if !allowed {
		respondErr(w, http.StatusUnprocessableEntity,
			fmt.Sprintf("mode \"%s\" is not offered by this platform (caps %v)", mode, caps))
		return
	}

	// EVERY REFUSAL ABOVE AND BELOW IS DECIDED BEFORE A SESSION IS OPENED. Opening
	// one costs an SSH login on a device that may be at its session ceiling (the
	// ProCurve 2610 authenticates and then refuses the channel at four), so a
	// request that was always going to be a 400 must not spend one.
	var stored = stpSuperRootOf(d)
	var superRoot = stored
	if in.SuperRoot != nil {
		superRoot = *in.SuperRoot
	}
	if in.BridgePriority != nil {
		var p = *in.BridgePriority
		if p < 0 || p > 61440 || p%4096 != 0 {
			// 802.1D encodes the priority in the top four bits; every CLI surveyed
			// refuses anything else outright. Refusing here rather than rounding
			// keeps the number the operator sees and the number the switch carries
			// the same one.
			respondErr(w, http.StatusBadRequest,
				fmt.Sprintf("bridge priority %d rejected: 802.1D takes multiples of 4096 "+
					"between 0 and 61440", p))
			return
		}
	}
	if stored && !superRoot && in.BridgePriority == nil {
		respondErr(w, http.StatusBadRequest,
			"clearing Super Root needs an explicit bridge priority. This switch is at "+
				"priority 0 because meshcraft put it there, and nothing recorded what it "+
				"carried before — un-ticking the box alone would leave a bridge nothing can "+
				"outrank while the screen claimed otherwise. Send \"bridge_priority\" "+
				"(a multiple of 4096, 32768 being the 802.1D default) in the same request")
		return
	}

	var _, adm, _, err = s.adminFor(r.Context(), d)
	if adm != nil {
		defer adm.Close()
	}
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var disable = mode == driver.STPPrefNone
	var applyMode = mode
	if disable {
		applyMode = "rstp" // ignored when Enabled=false; keep a valid token
	}

	// PERSISTED BEFORE THE PUSH, deliberately. The apply can fail halfway — the
	// priority lands and the region does not, or the connector refuses the extra
	// keyword — and the flag has to describe what was ASKED FOR either way, so the
	// checkbox comes back the way the operator left it and the next auto-apply run
	// carries the same decision. A store failure is fatal here rather than
	// tolerated: pushing an intent that was not recorded is how a device ends up
	// at priority 0 with a screen that shows the box unticked.
	if in.SuperRoot != nil && superRoot != stored {
		var serr = s.Store.SaveSTPSuperRoot(r.Context(), d.ID, superRoot)
		if serr != nil {
			respondErr(w, http.StatusInternalServerError,
				"recording the Super Root intent failed, nothing was pushed: "+serr.Error())
			return
		}
	}

	var res driver.ApplyResult
	res, err = s.pushDeviceSTP(r.Context(), d, conn, adm, applyMode, disable,
		superRoot, in.BridgePriority)
	if err != nil {
		respondErr(w, http.StatusBadGateway, err.Error())
		return
	}
	s.emitDeviceEvent(r, d, "device.stp_apply", model.SevInfo,
		"Spanning-tree mode \""+mode+"\" applied on \""+d.Name+"\"")
	respond(w, http.StatusOK, map[string]any{
		"ok":   true,
		"mode": mode,
		// `skipped` travels next to `notes` because the Super Root half is exactly
		// what lands there: a connector that cannot carry the flag, or an EOS whose
		// CLI refused "spanning-tree root super", says so in Skipped and nowhere
		// else. Dropping it would turn "ticked and nothing happened" back into a
		// silent success, which is the failure the contract's SAY-SO rule exists
		// to prevent.
		"skipped":    res.Skipped,
		"notes":      res.Notes,
		"super_root": superRoot,
		"steps":      transcriptOf(adm),
	})
}

// siteMSTRegion derives the MST region every device of this site must share.
//
// # Why it is computed here and never entered anywhere
//
// 802.1s decides region membership on name + revision + a digest over the FULL
// VLAN-to-instance table, and two bridges are in one region only if all three
// match exactly. A mismatch is silent — the neighbour is simply treated as an
// ordinary RSTP bridge — so the usual way of running MST, with an operator typing
// the same three values on every switch, fails by typo and shows nothing.
//
// Deriving it removes the typo: the name comes from the site itself and the map is
// built from the site's own VLAN list, so every device of a site computes the same
// region from the same data without anyone entering anything.
//
// # The name is NOT the slug any more (ADR-0021)
//
// It was, and the reasoning was that a slug is unique per site. The premise was
// wrong: `PATCH /api/sites/{id}` accepts a `slug`, so the slug is renameable, and
// renaming a region EVICTS every device still carrying the old name. The ARENA
// fleet proved it — a site rename left seven Instant On switches on `site-exec`
// and one on `arena`, two regions where the design says one, with no symptom.
//
// So the name is now, in order: the pin in the site plan if there is one, else
// [driver.GenericMSTRegionName] over the site UUID, which no route can edit.
// Migration 0047 pinned the then-current slug for every pre-existing site, which
// is what guarantees this change renames NOTHING already in the field: for those
// sites the pinned value equals what this function returned the day before.
//
// ALL KNOWN VLANs GO INTO INSTANCE 1 (VLAN 1 excepted — it belongs to the CIST on
// every platform, and mapping it is refused by some and ignored by others, which
// is enough to break the digest). One instance means one tree for the site: no
// per-VLAN load balancing, which needs redundant paths to be worth anything and
// must be identical everywhere to be safe at all.
//
// THE COROLLARY THE CALLER MUST CARRY: the digest covers every VLAN, so adding a
// site VLAN changes the region. Any device not re-pushed afterwards leaves the
// region — still forwarding, still green, quietly out of the tree. Changing the
// VLAN list of a site means re-applying STP across it.
func (s *Server) siteMSTRegion(ctx context.Context, d model.Device) driver.MSTRegion {
	return s.mstRegionOfSite(ctx, d.SiteID)
}

// mstRegionOfSite is siteMSTRegion keyed by the site alone.
//
// Split out so the read-only endpoint `GET /api/sites/{siteID}/stp-mst-region` can
// SHOW the region without a device to hang it on. That endpoint exists because
// nothing in the interface displayed the region until ADR-0021, and a quantity the
// topology depends on that no screen renders is a quantity nobody checks — which is
// how the ARENA fabric stayed split across two region names unnoticed.
//
// The device-keyed wrapper stays because every apply path has a device and none of
// them should have to know that a region is a property of the site.
func (s *Server) mstRegionOfSite(ctx context.Context, siteID string) driver.MSTRegion {
	// No GetSite any more: nothing about the site ROW decides a region name since
	// ADR-0021, and the plan read below already fails for a site that is gone.
	var vlans, vlanErr = s.Store.ListVlans(ctx, siteID)
	if vlanErr != nil {
		return driver.MSTRegion{}
	}
	var ids = make([]int, 0, len(vlans))
	var v model.Vlan
	for _, v = range vlans {
		ids = append(ids, v.VlanID)
	}
	// A plan read that fails is tolerated: a region without an explicit revision
	// is still a region, and refusing to build one for a transient database error
	// would leave the fleet unregioned.
	//
	// It is NOT tolerated for the name, though. A failed plan read means the pin is
	// unreadable, not absent, and falling through to the generic name would rename
	// the region of an established site because the database hiccuped. Returning no
	// region leaves spanning tree alone, which is the only safe reading of "I could
	// not find out what this site is called".
	var plan, planErr = s.Store.GetSitePlan(ctx, siteID)
	if planErr != nil {
		return driver.MSTRegion{}
	}
	return driver.SiteMSTRegion(
		stpRegionNameOf(plan, siteID), stpRegionRevisionOf(plan), ids)
}

// stpRegionNameOf decides the MST region name for a site: the pin, or a name
// derived from the site's immutable identifier.
//
// # Why a pin exists at all (ADR-0021)
//
// Because a region name that changes is an outage, and this product had to change
// one. Every site that existed when migration 0047 ran carries `stp_mst_region`
// set to its then-current slug, so the name this function returns for those sites
// is byte-identical to what the slug-derived version returned — no device in the
// field is renamed by the switch to generic names. Only sites created afterwards,
// which by construction have no hardware carrying a region, get the generic form.
//
// The pin is also the repair tool. A fleet already split across two region names
// converges by pinning the name the MAJORITY of the switches carry, which turns a
// seven-device re-apply into a one-device one. That is a deliberate operator
// gesture through `PUT /api/sites/{id}/plan`, never a deduction: a migration
// cannot see what hardware carries, and half a fleet is usually offline.
func stpRegionNameOf(plan json.RawMessage, siteID string) string {
	var pinned = stpRegionPinOf(plan)
	if pinned != "" {
		return pinned
	}
	return driver.GenericMSTRegionName(siteID)
}

// stpRegionPinOf reads `stp_mst_region` out of a site plan, or "" when the site
// has no pinned name.
//
// Separate from [stpRegionNameOf] because two callers need DIFFERENT answers from
// the same key: the apply path wants a usable name and does not care where it came
// from, while the display endpoint has to tell an operator whether the name is a
// pin somebody set — therefore changeable, therefore dangerous — or derived from
// the site id and incapable of drifting.
func stpRegionPinOf(plan json.RawMessage) string {
	if len(plan) == 0 {
		return ""
	}
	var parsed struct {
		Name *string `json:"stp_mst_region"`
	}
	if json.Unmarshal(plan, &parsed) != nil || parsed.Name == nil {
		return ""
	}
	return strings.TrimSpace(*parsed.Name)
}

// stpRegionRevisionOf reads the region revision from the site plan.
//
// A revision is NOT a version to bump for bookkeeping: raising it on one device
// removes that device from the region until every other is re-pushed. It lives in
// the site plan so a deliberate region change is possible, and defaults to 1 —
// not 0, which is what an untouched device carries, so a meshcraft-managed region
// is distinguishable from a factory one at a glance.
func stpRegionRevisionOf(plan json.RawMessage) int {
	if len(plan) == 0 {
		return 1
	}
	var parsed struct {
		Rev *int `json:"stp_mst_revision"`
	}
	if json.Unmarshal(plan, &parsed) != nil || parsed.Rev == nil {
		return 1
	}
	return *parsed.Rev
}

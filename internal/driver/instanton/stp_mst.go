package instanton

// MST instance-to-VLAN mapping on Instant On, read and written the way the
// product's own UI does it — and no other way, because this platform's
// signature failure is `statusCode 0`, no change, no message.
//
// THE WRITE SHAPE WAS NOT GUESSED. It comes from the product's served
// JavaScript (`/hpe/js/out/pages1.js`, `addMSTPEntry`'s `addSubmit` handler,
// fetched live through a 1930's own SOCKS route on 2026-08-05), and it is NOT
// the `MSTP` section's `InstanceEntry` element — that field really is always
// served empty and is not the write target. The UI performs THREE separate
// section writes, in order:
//
//  1. `MSTPLogicalInstanceList`, action `add`:
//     <Entry><logicalinstanceID>N</logicalinstanceID></Entry>
//     — creates the logical instance. The UI only proceeds if this answers
//     statusCode 0.
//  2. `MSTPInstanceList`, action `set`:
//     <Entry><instanceID>N</instanceID><priority>P</priority></Entry>
//     — bridge priority for that instance (0-61440, steps of 4096).
//  3. `MSTPVLANList`, action `set`:
//     <Entry><instanceID>N</instanceID><VLANID>ranges</VLANID></Entry>
//     — the actual VLAN-to-instance map. `VLANID` is a RANGE-COMPRESSED
//     string (`convertArrayToStr` in mainpage.js: consecutive IDs collapse to
//     `first-last`, e.g. "10-15,20,30-32"). A plain comma list of every ID
//     has never been observed on the wire; only the compressed form is sent.
//
// DELETING AN INSTANCE IS ONE DOCUMENT WITH THREE SECTIONS IN IT, read off the
// same UI on 2026-08-11 — and that is exactly why this connector does not do it
// (see [Admin.applyMSTInstanceVLANs]'s refusal). The UI posts:
//
//	MSTPInstanceList          set      <Entry><logicalinstanceID>N</logicalinstanceID></Entry>
//	MSTPVLANList              restore  <Entry><instanceID>N</instanceID>
//	                                          <VLANID>every VLAN currently mapped</VLANID></Entry>
//	MSTPLogicalInstanceList   delete   <Entry><logicalinstanceID>N</logicalinstanceID></Entry>
//
// THE ORDER IS THE POINT: the VLANs go back to instance 0 FIRST, and only then
// is the logical instance removed. An instance deleted while it still owns VLANs
// is the state nobody has a shape for.
//
// [Admin.writeSection] SENDS ONE SECTION PER POST — one `<DeviceConfiguration>`
// carrying one section element, with `?{Section}` on the query string, which the
// firmware requires (admin.go). Whether these three may be split across three
// POSTs is exactly what has not been observed; the UI sends them together. So
// the gap here is not the shape any more, it is a multi-section write this
// connector cannot make.
//
// Moving a VLAN back to the CIST is a DIFFERENT action on the same section:
// `MSTPVLANList`, action `restore`, same Entry shape (read from `editSubmit`).
// PROVEN LIVE on sw-labo-1930 (2026-08-05): restore of "1,50" answered code 0
// and the reread showed both VLANs back in the CIST. The same session settled
// `set`'s semantics: it MAPS the listed VLANs and touches nothing else — after
// a `set` of "42,98-100" on an instance that also carried 1 and 50, the reread
// still showed all six; only `restore` removed the extras.
//
// THE READ SHAPE WAS OBSERVED LIVE (sw-labo-1930, JL685A,
// InstantOn_1930_3.4.0.0, 2026-08-05). `MSTPInstanceList` serves one <Entry>
// per instance, CIST included:
//
//	<MSTPInstanceList type="section">
//	  <Entry>
//	    <instanceID>1</instanceID>
//	    <VLANList>1,42,50,98-100</VLANList>
//	    <priority>32768</priority>
//	    <enbl>1</enbl>
//	    ... (topologyChg*, brdgAddr, dsgRoot*, timers)
//	  </Entry>
//	</MSTPInstanceList>
//
// `VLANList` is range-compressed, which is what settles the compression
// question: the device itself speaks in ranges. The per-VLAN pivot
// (`MSTPVLANList`, one <Entry><VLANID>v</VLANID><instanceID>i</instanceID>
// per VLAN) exists but serves all 4094 rows; the instance list is the
// economical read and carries the same information.

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

const (
	// sectionMSTPLogicalInstances creates/deletes logical instances (write
	// target of step 1). Read back, it lists one Entry per existing instance:
	// <Entry><logicalinstanceID>1</logicalinstanceID><instanceID>1</instanceID></Entry>.
	sectionMSTPLogicalInstances = "MSTPLogicalInstanceList"
	// sectionMSTPInstances carries per-instance priority and the compressed
	// VLANList — the read side of the whole mapping.
	sectionMSTPInstances = "MSTPInstanceList"
	// sectionMSTPVLANs is the per-VLAN pivot table, and the write target for
	// the mapping itself (actions `set` and `restore`).
	sectionMSTPVLANs = "MSTPVLANList"
)

// ionMSTInstance is one MSTPInstanceList row, reduced to what reconciliation
// compares.
type ionMSTInstance struct {
	VLANs    []int
	Priority int
}

// compressVLANRanges renders a VLAN set the way the product UI does
// (mainpage.js convertArrayToStr): sorted, deduplicated, consecutive runs
// collapsed to "first-last", joined with commas. "10-15,20,30-32".
//
// This is the ONLY form the UI ever sends in MSTPVLANList.VLANID — a plain
// comma list of every ID has not been proven accepted, so it is never sent.
func compressVLANRanges(ids []int) string {
	var clean []int
	var seen = map[int]bool{}
	var v int
	for _, v = range ids {
		if v < 1 || v > 4094 || seen[v] {
			continue
		}
		seen[v] = true
		clean = append(clean, v)
	}
	sort.Ints(clean)
	if len(clean) == 0 {
		return ""
	}
	var parts []string
	var start = clean[0]
	var prev = clean[0]
	var flush = func() {
		if start == prev {
			parts = append(parts, strconv.Itoa(start))
			return
		}
		parts = append(parts, strconv.Itoa(start)+"-"+strconv.Itoa(prev))
	}
	var i int
	for i = 1; i < len(clean); i++ {
		if clean[i] == prev+1 {
			prev = clean[i]
			continue
		}
		flush()
		start, prev = clean[i], clean[i]
	}
	flush()
	return strings.Join(parts, ",")
}

// parseVLANRanges is the read-side inverse: "1,42,50,98-100" → sorted IDs.
// Tokens that are not a number or a low-high range are ignored rather than
// guessed — a misread VLAN list would fabricate drift.
func parseVLANRanges(s string) []int {
	var out []int
	var seen = map[int]bool{}
	var tok string
	for _, tok = range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		var lo, hi int
		var dash = strings.IndexByte(tok, '-')
		if dash < 0 {
			var n, err = strconv.Atoi(tok)
			if err != nil {
				continue
			}
			lo, hi = n, n
		} else {
			var a, errA = strconv.Atoi(strings.TrimSpace(tok[:dash]))
			var b, errB = strconv.Atoi(strings.TrimSpace(tok[dash+1:]))
			if errA != nil || errB != nil || a > b {
				continue
			}
			lo, hi = a, b
		}
		var v int
		for v = lo; v <= hi; v++ {
			if v < 1 || v > 4094 || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

var reMSTEntry = regexp.MustCompile(`(?s)<Entry>(.*?)</Entry>`)

// parseMSTInstanceEntries reads MSTPInstanceList rows out of a wcd response.
// The CIST (instanceID 0) is kept out: [driver.MSTRegion.InstanceVLANs] never
// lists it — it holds every VLAN not named elsewhere, and reporting its
// enormous complement as membership would be noise presented as data.
func parseMSTInstanceEntries(body string) map[int]ionMSTInstance {
	var section = sectionOf(body, sectionMSTPInstances)
	if section == "" {
		return nil
	}
	var out = map[int]ionMSTInstance{}
	var m []string
	for _, m = range reMSTEntry.FindAllStringSubmatch(section, -1) {
		var block = m[1]
		var idRaw = xmlField(block, "instanceID")
		var id, err = strconv.Atoi(strings.TrimSpace(idRaw))
		if err != nil || id == 0 {
			continue
		}
		out[id] = ionMSTInstance{
			VLANs:    parseVLANRanges(xmlField(block, "VLANList")),
			Priority: xmlInt(block, "priority"),
		}
	}
	return out
}

// readMSTInstances reads the device's current instance→VLAN map.
func (a *Admin) readMSTInstances(ctx context.Context) (map[int]ionMSTInstance, error) {
	var body, err = a.RawSection(ctx, sectionMSTPInstances)
	if err != nil {
		return nil, err
	}
	if sectionMissing(body, sectionMSTPInstances) {
		return nil, fmt.Errorf("section %s not found on device", sectionMSTPInstances)
	}
	return parseMSTInstanceEntries(body), nil
}

// equalVLANSets compares two sorted VLAN slices.
func equalVLANSets(a, b []int) bool {
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

// vlanSetDiff returns the members of a that are not in b, sorted.
func vlanSetDiff(a, b []int) []int {
	var in = map[int]bool{}
	var v int
	for _, v = range b {
		in[v] = true
	}
	var out []int
	for _, v = range a {
		if !in[v] {
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// ionInstancePriority picks the priority written on an instance: the plan's
// bridge priority when the caller supplied one, otherwise the platform
// default. Clamped to the device's accepted range either way.
func ionInstancePriority(bridgePriority *int) int {
	var p = 32768
	if bridgePriority != nil {
		p = *bridgePriority
	}
	if p < 0 {
		p = 0
	}
	if p > 61440 {
		p = 61440
	}
	return p
}

// applyMSTInstanceVLANs reconciles the device's instance→VLAN map with the
// desired one. READ-COMPARE-DECIDE: nothing is written when the device
// already matches, and every write is verified by REREADING — on this range
// `statusCode 0` does not mean "done", it means "received".
//
// Removal (a VLAN moving back to the CIST) uses `MSTPVLANList` action
// `restore` — proven live on sw-labo-1930, 2026-08-05 (VLANs 1,50 restored,
// verified by reread). If a device refuses it anyway, the refusal lands in
// [driver.ApplyResult.Skipped] with the VLANs still mapped — the digest
// divergence must be visible, not smoothed over.
//
// The CREATE sequence (logical-instance add + priority set) is unit-tested to
// the JS shape but has NOT been exercised live: the lab instance already
// existed, and creating a scratch instance just to prove it would strand it.
// The DELETE's shape is known since 2026-08-11 (cf. the file header) but this
// connector cannot send it — it is one POST naming three sections, and
// [Admin.writeSection] writes one section per POST — so a scratch instance is
// still an instance meshcraft cannot remove.
func (a *Admin) applyMSTInstanceVLANs(ctx context.Context, desired map[int][]int,
	bridgePriority *int, out *driver.ApplyResult) error {

	var observed, err = a.readMSTInstances(ctx)
	if err != nil {
		return fmt.Errorf("read %s before MST instance write: %w", sectionMSTPInstances, err)
	}

	var ids []int
	var id int
	for id = range desired {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)

	var wrote = false
	for _, id = range ids {
		var want = append([]int{}, desired[id]...)
		sort.Ints(want)
		if len(want) == 0 {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"MST instance %d: desired with no VLANs — nothing to create", id))
			continue
		}
		var wantStr = compressVLANRanges(want)
		var obs, exists = observed[id]

		if !exists {
			// The UI's three-write sequence, in its order, each gated on
			// statusCode 0 (writeSection refuses anything else).
			var addErr = a.writeSection(ctx, sectionMSTPLogicalInstances, "add",
				"<Entry><logicalinstanceID>"+strconv.Itoa(id)+"</logicalinstanceID></Entry>",
				fmt.Sprintf("MST logical instance %d created", id))
			if addErr != nil {
				return fmt.Errorf("MST instance %d create: %w", id, addErr)
			}
			var prio = ionInstancePriority(bridgePriority)
			var prioErr = a.writeSection(ctx, sectionMSTPInstances, "set",
				"<Entry><instanceID>"+strconv.Itoa(id)+"</instanceID>"+
					"<priority>"+strconv.Itoa(prio)+"</priority></Entry>",
				fmt.Sprintf("MST instance %d priority=%d", id, prio))
			if prioErr != nil {
				return fmt.Errorf("MST instance %d priority: %w", id, prioErr)
			}
			var mapErr = a.writeSection(ctx, sectionMSTPVLANs, "set",
				"<Entry><instanceID>"+strconv.Itoa(id)+"</instanceID>"+
					"<VLANID>"+wantStr+"</VLANID></Entry>",
				fmt.Sprintf("MST instance %d VLANs=%s", id, wantStr))
			if mapErr != nil {
				return fmt.Errorf("MST instance %d VLAN map: %w", id, mapErr)
			}
			// No note: the three writes above are each already a step, and "created"
			// adds no reading of the device — the prior state it would report is
			// "absent", which the step saying it was created already says.
			wrote = true
			continue
		}

		// Instance exists: write only what differs.
		if !equalVLANSets(obs.VLANs, want) {
			// The FULL desired list, not just the missing VLANs. `set` MAPS the
			// listed VLANs and removes nothing (proven live, cf. file header):
			// listing an already-mapped VLAN is idempotent, and the full list
			// stays correct even on a firmware where `set` would replace
			// membership — a partial write would not.
			var mapErr = a.writeSection(ctx, sectionMSTPVLANs, "set",
				"<Entry><instanceID>"+strconv.Itoa(id)+"</instanceID>"+
					"<VLANID>"+wantStr+"</VLANID></Entry>",
				fmt.Sprintf("MST instance %d VLANs=%s", id, wantStr))
			if mapErr != nil {
				return fmt.Errorf("MST instance %d VLAN map: %w", id, mapErr)
			}
			// THIS one is a note, and the shape of the sentence is the reason: it
			// names what the DEVICE HAD. That is a fact no step carries and the
			// standard model cannot express — the device was in a different MST
			// region than the plan, because the VLAN-to-instance table is one third
			// of the region identity (cf. mdpv1.MSTRegion). Finding it is the whole
			// point of rereading.
			out.Notes = append(out.Notes, fmt.Sprintf(
				"MST instance %d VLANs set to %s (device had %s)",
				id, wantStr, compressVLANRanges(obs.VLANs)))
			wrote = true
		}
		// NO `else` BRANCH. It used to say "MST instance N already maps X — nothing
		// written", and that sentence is a NON-EVENT: the device already agreed with
		// the plan, which is the nominal outcome of every idempotent apply.
		//
		// It is the most expensive kind of noise, because of where it landed. The
		// banner is titled "what the device actually carries" and promises to say in
		// which cases a VLAN configuration is in place and WITHOUT EFFECT, and on
		// which ports. Filling it with "there was nothing to do" spends the
		// operator's attention precisely where a real remark has to stand out, and
		// trains them to stop reading it. Success is told by the apply succeeding.
		if bridgePriority != nil {
			var prio = ionInstancePriority(bridgePriority)
			if obs.Priority != prio {
				var prioErr = a.writeSection(ctx, sectionMSTPInstances, "set",
					"<Entry><instanceID>"+strconv.Itoa(id)+"</instanceID>"+
						"<priority>"+strconv.Itoa(prio)+"</priority></Entry>",
					fmt.Sprintf("MST instance %d priority=%d", id, prio))
				if prioErr != nil {
					return fmt.Errorf("MST instance %d priority: %w", id, prioErr)
				}
				out.Notes = append(out.Notes, fmt.Sprintf(
					"MST instance %d priority %d → %d", id, obs.Priority, prio))
				wrote = true
			}
		}
	}

	// Instances the device carries that the plan does not: SAID, not touched.
	//
	// THE SHAPE IS NO LONGER THE BLOCKER — the file header carries the UI's own
	// three-section delete, observed 2026-08-11. What blocks it is
	// [Admin.writeSection], which sends ONE section per POST: the delete is one
	// document naming MSTPInstanceList, MSTPVLANList and MSTPLogicalInstanceList
	// together, and splitting it into three requests has never been observed. A
	// half-applied delete on this platform leaves an instance stripped of its
	// VLANs or a logical instance with VLANs still on it, and neither state has a
	// recovery here.
	for id = range observed {
		var _, wanted = desired[id]
		if !wanted {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"MST instance %d exists on the device but not in the plan (VLANs %s); "+
					"removing it takes ONE POST naming three sections (VLANs restored to "+
					"instance 0 first, then the logical instance deleted) and this connector "+
					"writes one section per POST — left in place, the region digests differ "+
					"until it is removed",
				id, compressVLANRanges(observed[id].VLANs)))
		}
	}

	// REREAD: the only statement this platform honours. Then move the extras
	// back to the CIST (action `restore` — the unproven one), and reread again.
	var after map[int]ionMSTInstance
	if wrote {
		after, err = a.readMSTInstances(ctx)
		if err != nil {
			out.Skipped = append(out.Skipped,
				"MST instance write NOT verified: reread failed — "+err.Error())
			return nil
		}
	} else {
		after = observed
	}

	var restored = false
	for _, id = range ids {
		var want = append([]int{}, desired[id]...)
		sort.Ints(want)
		if len(want) == 0 {
			continue
		}
		var obs, exists = after[id]
		if !exists {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"MST instance %d: device answered statusCode 0 but the instance does not "+
					"read back — the write did not take", id))
			continue
		}
		var extra = vlanSetDiff(obs.VLANs, want)
		if len(extra) == 0 {
			continue
		}
		var extraStr = compressVLANRanges(extra)
		var resErr = a.writeSection(ctx, sectionMSTPVLANs, "restore",
			"<Entry><instanceID>"+strconv.Itoa(id)+"</instanceID>"+
				"<VLANID>"+extraStr+"</VLANID></Entry>",
			fmt.Sprintf("MST instance %d VLANs %s restored to CIST", id, extraStr))
		if resErr != nil {
			out.Skipped = append(out.Skipped, fmt.Sprintf(
				"MST instance %d: VLANs %s are mapped on the device but not in the plan, "+
					"and the restore-to-CIST write was refused (%v) — the region digest "+
					"still differs from the plan's", id, extraStr, resErr))
			continue
		}
		out.Notes = append(out.Notes, fmt.Sprintf(
			"MST instance %d: VLANs %s restored to the CIST", id, extraStr))
		restored = true
	}

	if restored {
		after, err = a.readMSTInstances(ctx)
		if err != nil {
			out.Skipped = append(out.Skipped,
				"MST instance write NOT verified: final reread failed — "+err.Error())
			return nil
		}
	}
	if wrote || restored {
		for _, id = range ids {
			var want = append([]int{}, desired[id]...)
			sort.Ints(want)
			if len(want) == 0 {
				continue
			}
			var obs, exists = after[id]
			if !exists {
				continue // already reported above
			}
			if equalVLANSets(obs.VLANs, want) {
				out.Notes = append(out.Notes, fmt.Sprintf(
					"MST instance %d verified by reread: VLANs %s",
					id, compressVLANRanges(obs.VLANs)))
			} else {
				out.Skipped = append(out.Skipped, fmt.Sprintf(
					"MST instance %d reads back as %s, plan says %s — the device accepted "+
						"the write and did not apply all of it; digests differ",
					id, compressVLANRanges(obs.VLANs), compressVLANRanges(want)))
			}
		}
	}
	return nil
}

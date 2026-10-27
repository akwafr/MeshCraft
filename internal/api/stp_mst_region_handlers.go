package api

import (
	"net/http"
	"sort"
)

// stpMSTRegionReply is the MST region identity of a site, as the controller would
// apply it. Read-only, computed from the database — it is NOT a survey of what the
// hardware carries, and the screen that renders it says so.
type stpMSTRegionReply struct {
	// Name, Revision and Instances are ONE value in three fields, and serving all
	// three together is deliberate (cf. mdpv1.MSTRegion, ADR-0021). 802.1s compares
	// the name, the revision and an MD5 digest over the whole 4096-entry
	// VLAN-to-instance table; two bridges share a region only if all three match
	// exactly, and a mismatch is silent. An endpoint serving the name alone would
	// invite reading it AS the region, which is the misconception that splits a
	// fabric.
	Name     string        `json:"name"`
	Revision int           `json:"revision"`
	Instance []stpMSTEntry `json:"instances"`
	// Pinned is true when the name comes from the site plan rather than from the
	// site identifier. The screen distinguishes them because they have different
	// futures: a pinned name is a decision someone made and can unmake, a derived
	// one cannot drift at all.
	Pinned bool `json:"pinned"`
}

// stpMSTEntry is one MST instance and the VLANs mapped into it. A list of pairs
// rather than a JSON object because object keys carry no order, and the one use of
// this reply is a human comparing two switches line by line.
type stpMSTEntry struct {
	Instance int   `json:"instance"`
	VLANs    []int `json:"vlans"`
}

// handleGetSTPMSTRegion serves the site's MST region identity for DISPLAY.
//
// # Why this endpoint exists (ADR-0021)
//
// Until it did, the region name, revision and VLAN map appeared on no screen in
// the product. The topology depends on all three, 802.1s evicts a device whose
// copy differs without logging anything, and no operator could see any of it.
//
// That is not hypothetical. On 2026-10-25 a read-only survey of the twelve Aruba
// Instant On 1930 of ARENA found seven switches in region `site-exec` with
// instance 1 mapping `42,98-100`, and one — ArubB — in region `arena` mapping
// `10-11,42,60,98-100`. Two regions where the design says one, and it stayed
// invisible for as long as nothing rendered the value.
//
// `viewer` and not `site_admin`: this reads a region, it does not change one, and
// the whole point is that anybody who can look at a site can check it.
func (s *Server) handleGetSTPMSTRegion(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var plan, err = s.Store.GetSitePlan(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var region = s.mstRegionOfSite(r.Context(), siteID)
	var out = stpMSTRegionReply{
		Name:     region.Name,
		Revision: region.Revision,
		Pinned:   stpRegionPinOf(plan) != "",
		// Never nil: a JSON `null` and an empty list read the same to a human and
		// differently to the frontend, which maps over it.
		Instance: []stpMSTEntry{},
	}
	// Sorted by instance id. Ranging a Go map yields a different order every
	// request, and a reply whose only use is comparison must not reshuffle.
	var ids = make([]int, 0, len(region.InstanceVLANs))
	var id int
	for id = range region.InstanceVLANs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id = range ids {
		out.Instance = append(out.Instance, stpMSTEntry{
			Instance: id,
			VLANs:    region.InstanceVLANs[id],
		})
	}
	respond(w, http.StatusOK, out)
}

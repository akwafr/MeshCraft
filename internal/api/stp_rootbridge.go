package api

// The site's ROOT BRIDGE designation, and the red flag a second one raises.
//
// # Where this lives, and why it is site-scoped
//
// Root designation is not a property of a switch. "Be the root" only means
// something relative to the other switches in the same tree, and the BPDU
// filtering a second root requires is computed from the TOPOLOGY — which no
// single device knows. So the plan lives on the site, beside the STP mode
// preference, and the per-device plans are DERIVED from it at apply time.
//
// The alternative — a checkbox on each device screen — was rejected for the
// reason the whole feature is delicate: ticking it on a second device is not a
// second independent choice, it is a change to what the FIRST one means. A
// per-device control would let an operator make that change without ever seeing
// the two together.
//
// # The red flag
//
// [driver.RootWarning] is returned the moment two devices contest the same
// instance. It is returned by the READ as well as the write, deliberately: an
// operator arriving on a site somebody else configured must meet the warning
// too, not just the person who typed it. A warning shown only at the moment of
// the change is a warning the second reader never sees.

import (
	"context"
	"encoding/json"
	"net/http"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

const planKeySTPRoots = "stp_root_bridges"

// rootPlanOf extracts the root designation from a site plan blob.
//
// A malformed or absent key yields an EMPTY plan, never an error: the plan blob
// also carries synoptic zones and device positions, and a site whose topology
// drawing is corrupt must still be able to answer "who is root" with "nobody"
// rather than refusing to load the spanning-tree screen.
func rootPlanOf(plan json.RawMessage) driver.RootPlan {
	if len(plan) == 0 {
		return driver.RootPlan{}
	}
	var parsed struct {
		Roots *driver.RootPlan `json:"stp_root_bridges"`
	}
	if json.Unmarshal(plan, &parsed) != nil || parsed.Roots == nil {
		return driver.RootPlan{}
	}
	return *parsed.Roots
}

// mergeRootPlan writes the designation into the site plan without dropping the
// other keys — zones, device positions, the STP mode preference. Same rule and
// same reason as mergeSTPPreference next door: this blob is shared, and a writer
// that marshalled only its own key would silently erase the synoptic.
func mergeRootPlan(plan json.RawMessage, roots driver.RootPlan) (json.RawMessage, error) {
	var m map[string]json.RawMessage
	if len(plan) == 0 {
		m = map[string]json.RawMessage{}
	} else if json.Unmarshal(plan, &m) != nil {
		m = map[string]json.RawMessage{}
	}
	var raw, err = json.Marshal(roots)
	if err != nil {
		return nil, err
	}
	m[planKeySTPRoots] = raw
	return json.Marshal(m)
}

// rootBridgeReply is what both the read and the write return.
type rootBridgeReply struct {
	Roots []driver.RootIntent `json:"roots"`
	// Contested: the instance ids claimed by more than one device. Non-empty is
	// exactly the condition that triggers BPDU filtering.
	Contested []int `json:"contested,omitempty"`
	// Warning is [driver.RootWarning] when Contested is non-empty, empty
	// otherwise. Carried as a STRING the frontend displays verbatim rather than a
	// boolean it would phrase itself: the sentence states an assumption about
	// physical cabling that the operator is accepting, and it must read the same
	// in the API, in the apply transcript and on screen.
	Warning string `json:"warning,omitempty"`
	// FilterPorts is what WILL be BPDU-filtered, per device, if this plan is
	// applied — computed from the topology meshcraft currently knows.
	//
	// Returned by the READ, which is the point: the operator sees which
	// interfaces stop taking part in spanning tree BEFORE anything is written,
	// rather than discovering it in an apply transcript. An empty map alongside a
	// non-empty Contested means meshcraft knows no path between the roots — see
	// [driver.InterRootFilterPorts], which refuses to invent one.
	FilterPorts map[string][]string `json:"filter_ports,omitempty"`
	// ApplyOrder is [driver.RootApplyOrder], so the screen can state the sequence
	// rather than an operator having to trust it.
	ApplyOrder string `json:"apply_order,omitempty"`
}

// buildRootReply assembles the answer, including the derived filter set.
//
// PURE apart from its inputs, so the interesting half — contested detection and
// the filter computation — is exercised without a database.
func buildRootReply(plan driver.RootPlan, links []driver.Link) rootBridgeReply {
	var out = rootBridgeReply{Roots: plan.Roots}
	if out.Roots == nil {
		out.Roots = []driver.RootIntent{}
	}
	out.Contested = plan.Contested()
	if len(out.Contested) == 0 {
		return out
	}
	out.Warning = driver.RootWarning
	out.ApplyOrder = driver.RootApplyOrder
	var ids []string
	var r driver.RootIntent
	for _, r = range plan.Roots {
		ids = append(ids, r.DeviceID)
	}
	var ports = driver.InterRootFilterPorts(links, ids)
	if len(ports) > 0 {
		out.FilterPorts = ports
	}
	return out
}

// rootDesignation resolves what the SITE's root-bridge plan asks of ONE device.
//
// # THE FUNCTION THAT WAS MISSING, and its absence was the whole feature
//
// The designation has been storable, drawable and journalled since it was built,
// and until 2026-08-12 `rootPlanOf` had NO caller outside this file's own two
// handlers. So naming a switch the root of its region wrote a row in the site
// plan, rendered a screen, warned about a second root — and never reached a
// device. The connector contract had been ready the whole time
// ([driver.SpanningTreePlan.RootInstances], [driver.SpanningTreePlan.BPDUFilterPorts],
// both consumed by internal/driver/arista/eos): nothing in the controller ever
// filled them in. This is that wiring, and it is why the designation is a RULE
// rather than a per-device checkbox — the operator names the root once, on the
// site, and every apply of every designated device carries it.
//
// # Three returns, because the connector needs three different things
//
//	superRoot    this device must win its region's root election
//	instances    WHICH trees (empty = the CIST / the single tree of a non-MST mode)
//	filterPorts  the ports that must stop processing BPDUs FIRST, so that two
//	             roots of the SAME instance end up in genuinely separate trees
//	             ([driver.RootApplyOrder] — the connector writes these before any
//	             root priority, and aborts the whole plan if they fail)
//
// `filterPorts` is EMPTY in the ordinary single-root case, and that is
// [driver.InterRootFilterPorts]'s own rule rather than a condition tested here:
// one root needs no filtering, so the dangerous half of this feature stays
// inert until somebody designates a second one.
//
// # The capability gate, and the switch it protects
//
// A designation only becomes a plan when the connector DECLARES it can express
// one ([driver.FeatSTPSuperRoot]). cisco/ios is the case that makes this
// necessary rather than tidy: it declares WritesExtra("stp") and deliberately
// does NOT implement Super Root, so a designation reaching it would be silently
// dropped on the way in — and, worse, would travel back out through the clear
// path as a bridge priority written to a production Catalyst. conformity.go
// already gates the operator's checkbox on exactly this pair; the rule must be
// gated the same way or the two disagree about what a designation means.
//
// Errors are absorbed into "not designated": a site plan that will not parse, or
// a topology read that fails, must not turn an ordinary apply into a refusal —
// and inventing a root from a partial read is the one outcome worse than not
// applying one.
func (s *Server) rootDesignation(ctx context.Context, d model.Device,
	conn driver.Connector) (superRoot bool, instances []int, filterPorts []string) {

	if !conn.Capabilities(d.Model).Features[driver.FeatSTPSuperRoot] {
		return false, nil, nil
	}
	var plan, err = s.Store.GetSitePlan(ctx, d.SiteID)
	if err != nil {
		return false, nil, nil
	}
	var roots = rootPlanOf(plan)
	// The topology is read ONLY when two devices contest one instance: a single
	// root needs no filtering (driver.InterRootFilterPorts' own rule), and the
	// ordinary site must not pay a links + neighbours + devices read on every
	// apply to be told so.
	var links []driver.Link
	if len(roots.Contested()) > 0 {
		links = s.topologyLinks(ctx, d.SiteID)
	}
	return rootDesignationFrom(roots, d.ID, links)
}

// rootDesignationFrom is the judgement, with no store and no session.
//
// PURE because this is where a mistake is expensive and silent: a device wrongly
// read as designated is written to priority 0 — it becomes the root of its
// region, which no screen contradicts — and a designated device wrongly read as
// ordinary leaves the site with a root elected by MAC address. Neither shows up
// as an error, so both are asserted directly (stp_rootbridge_test.go) rather
// than reached through a database.
func rootDesignationFrom(roots driver.RootPlan, deviceID string,
	links []driver.Link) (superRoot bool, instances []int, filterPorts []string) {

	var mine bool
	var r driver.RootIntent
	for _, r = range roots.Roots {
		if r.DeviceID == deviceID {
			mine = true
			instances = r.Instances
			break
		}
	}
	if !mine {
		return false, nil, nil
	}
	if len(roots.Contested()) > 0 {
		var ids []string
		for _, r = range roots.Roots {
			ids = append(ids, r.DeviceID)
		}
		filterPorts = driver.InterRootFilterPorts(links, ids)[deviceID]
	}
	return true, instances, filterPorts
}

func (s *Server) handleGetSTPRootBridges(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var plan, err = s.Store.GetSitePlan(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	// The designation is answerable without the topology; only the derived filter
	// set is not. A 500 on the spanning-tree screen because the neighbour table
	// could not be read would be the wrong trade.
	respond(w, http.StatusOK,
		buildRootReply(rootPlanOf(plan), s.topologyLinks(r.Context(), siteID)))
}

func (s *Server) handlePutSTPRootBridges(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var body struct {
		Roots []driver.RootIntent `json:"roots"`
	}
	if !decode(w, r, &body) {
		return
	}
	var plan, err = s.Store.GetSitePlan(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var next = driver.RootPlan{Roots: body.Roots}
	var merged json.RawMessage
	merged, err = mergeRootPlan(plan, next)
	if err != nil {
		fail(w, err)
		return
	}
	if err = s.Store.UpdateSitePlan(r.Context(), siteID, merged); err != nil {
		fail(w, err)
		return
	}
	// The change is JOURNALLED when it introduces a contested instance, and only
	// then. Designating one root is ordinary configuration; designating a second
	// is the operator asserting a property of the cabling that meshcraft cannot
	// check, and that assertion should survive in the event log for whoever
	// investigates the storm if it was wrong.
	var reply = buildRootReply(next, s.topologyLinks(r.Context(), siteID))
	if len(reply.Contested) > 0 {
		var sid = siteID
		_ = s.Store.InsertEvent(r.Context(), model.Event{
			SiteID:   &sid,
			Type:     "stp.multi_root",
			Severity: model.SevWarning,
			Message:  driver.RootWarning,
		})
	}
	s.NotifySiteChanged(siteID, ChangeSync)
	respond(w, http.StatusOK, reply)
}

// topologyLinks assembles the adjacency the filter computation searches, from
// BOTH sources meshcraft has, and never fails.
//
// # Why both, and why declared links come first
//
// `device_links` is what an OPERATOR typed: it survives a cable being unplugged,
// which is the point of typing it in, and it is the only source for a link
// between two devices that do not speak LLDP (an entire fleet platform serves no
// LLDP-MIB at all). `lldp_neighbors` is what was OBSERVED: it covers everything
// nobody typed, which on a real site is most of it.
//
// Taking only the declared links would compute a filter set from a drawing.
// Taking only the observed ones would miss exactly the links a site bothered to
// document. The union is the honest input, and duplicates are harmless — the
// filter computation deduplicates by (device, port).
//
// AN LLDP LINK WITH NO RECOGNISED FAR END IS DROPPED. `BDevice` empty is the
// common operational case — a neighbour that is not a fleet device — and such a
// link cannot be part of a path BETWEEN TWO ROOTS, both of which are fleet
// devices by construction. Keeping it would add a dangling edge the BFS would
// walk into and never leave.
//
// Errors yield fewer links, never a failure: a filter set computed from a
// partial topology is visible to the operator (the reply carries it), whereas a
// 500 on the spanning-tree screen tells them nothing and blocks the page.
func (s *Server) topologyLinks(ctx context.Context, siteID string) []driver.Link {
	var out []driver.Link
	var declared, err = s.Store.ListLinks(ctx, siteID)
	if err == nil {
		var l model.DeviceLink
		for _, l = range declared {
			out = append(out, driver.Link{
				ADevice: l.ADevice, APort: l.APort,
				BDevice: l.BDevice, BPort: l.BPort,
			})
		}
	}
	var neighbors []model.LLDPNeighbor
	neighbors, err = s.Store.ListLLDPNeighbors(ctx, siteID)
	if err != nil {
		return out
	}
	var selves []model.LLDPSelf
	selves, _ = s.Store.ListLLDPSelf(ctx, siteID)
	var devices []model.Device
	devices, _ = s.Store.ListDevices(ctx, siteID)
	var view = buildLLDPView(neighbors, selves, devices)
	var k model.LLDPLink
	for _, k = range view.Links {
		if k.BDevice == "" {
			continue
		}
		out = append(out, driver.Link{
			ADevice: k.ADevice, APort: k.APort,
			BDevice: k.BDevice, BPort: k.BPort,
		})
	}
	return out
}

package mdpv1

// Designating ROOT BRIDGES, and what a SECOND one costs.
//
// # One root is a configuration. Two roots is a topology decision.
//
// Making one switch the root of a spanning tree is ordinary: it is a priority,
// it is reversible, and the protocol is designed for exactly that. This file is
// about the other case, and it exists because the other case is not more of the
// same thing — it is a different thing wearing the same word.
//
// A spanning tree has ONE root by definition. Two bridges cannot both be root of
// the same tree; the protocol elects one and the other becomes an ordinary
// bridge with a very low priority. So "two root bridges" only means something if
// the two are roots of DIFFERENT trees — which means the BPDUs that would
// otherwise merge them into one tree have to stop crossing the link between
// them. That is what `spanning-tree bpdufilter` does, and it is the only control
// in this whole area that can CAUSE a broadcast storm rather than prevent one.
//
// # The rule this product commits to, and the assumption under it
//
// THE OPERATOR ASSERTS THERE IS NO REDUNDANT PATH BETWEEN ROOT BRIDGES. That
// assumption is the entire safety argument, and it is written here rather than
// buried in a UI string because everything below is unsound without it: with a
// second path outside the filtered links, filtering BPDUs leaves a physical loop
// that no spanning tree is watching, and the failure mode is a broadcast storm
// that takes the site off the air.
//
// Proper multi-root fabrics are the job of EVPN/VXLAN, which this product does
// not manage yet. Until it does, a second root bridge is an operator asserting a
// property of their cabling that meshcraft cannot verify. Hence [RootWarning]:
// the second root is accepted, and it is flagged, in those terms.
//
// # THE ORDER IS THE SAFETY PROPERTY
//
// Filter first, then root. Both gestures are needed and the window between them
// is the dangerous part, so it is the order that decides which way the failure
// falls:
//
//   - FILTER THEN ROOT (what [RootApplyOrder] encodes): between the two writes,
//     the link carries no BPDUs and the second switch is still an ordinary
//     bridge. The trees are already separated before anything claims to be root.
//     Worst case, an operator who stops halfway has two segments that are
//     ISOLATED for spanning-tree purposes and still forwarding — a topology
//     nobody wants, which is visible, and which no port-blocking event
//     accompanies;
//   - ROOT THEN FILTER, the intuitive order, is the bad one. For the duration of
//     the gap, two bridges both claim priority 0 on the same tree while BPDUs
//     still flow. They contest the election, the tree reconverges around the
//     lower MAC address, ports transition, and traffic stops on whatever was
//     forwarding through the loser. That is a reconvergence storm scheduled by
//     meshcraft, in the middle of its own apply, on a fleet an operator believed
//     they were configuring safely.
//
// An apply that cannot complete the second half must therefore leave the first
// half in place and SAY so, never roll back: rolling the filter off restores the
// contested-election state the ordering exists to avoid.

import "sort"

// RootWarning is what the interface must show the moment a SECOND root bridge is
// designated on a site.
//
// A constant rather than a sentence written at the call site, for the same
// reason [CertificateOutlivesAddress] is one: it has to reach an operator, and
// it must say the same thing in the API response, in the transcript and on
// screen. Whoever reads it is about to assert something about physical cabling
// that no software here can check.
const RootWarning = "a SECOND root bridge means two SEPARATE spanning trees, and meshcraft " +
	"will filter BPDUs on the path between them to keep them separate. That is safe ONLY IF " +
	"there is no other physical path between these two switches: with one, filtering leaves " +
	"a loop that no spanning tree is watching, and the result is a broadcast storm. " +
	"Multi-root fabrics belong to EVPN/VXLAN, which this product does not manage yet"

// RootApplyOrder documents the sequence, in the one place a caller looks.
//
// Named and exported because it is the safety property, not an implementation
// detail: a future refactor that reorders these two writes reintroduces a
// scheduled reconvergence storm, and a constant is harder to reorder by accident
// than a comment.
const RootApplyOrder = "BPDU filtering is written on the inter-root path BEFORE any switch is " +
	"made root. Reversed, the two bridges contest one election while BPDUs still flow, and " +
	"the tree reconverges mid-apply"

// RootIntent is one device's root-bridge designation.
//
// PER INSTANCE, and that is the difference from the single `SuperRoot` boolean it
// supersedes. Under MSTP a region carries several instances, each with its own
// tree and its own root — that is what MST is FOR — so "this switch is the root"
// is only a complete statement once it says root of WHAT. The old boolean could
// only ever mean instance 0, which quietly made the feature useless on exactly
// the topologies MST exists to serve.
//
// Outside MSTP the instance list is meaningless and callers must leave it empty:
// RSTP and STP have one tree, and [Instances] is then read as "the only one".
type RootIntent struct {
	// DeviceID is the switch to make root.
	DeviceID string `json:"device_id"`
	// Instances are the MST instance ids this device must win. Empty means the
	// single tree of a non-MST mode, or the CIST under MSTP.
	//
	// Instance 0 is the CIST and may be named explicitly; it is not implied by an
	// empty list under MSTP, because "root of the CIST" and "root of whatever
	// tree exists" are different claims and conflating them is how a per-instance
	// design collapses back into the boolean it replaced.
	Instances []int `json:"instances,omitempty"`
}

// RootPlan is the site-wide root-bridge designation.
type RootPlan struct {
	// Roots, in no meaningful order.
	Roots []RootIntent `json:"roots,omitempty"`
}

// Contested reports whether two or more devices claim the SAME instance.
//
// This — not the number of roots — is the condition that requires BPDU
// filtering. Two roots on DIFFERENT instances are the ordinary, safe use of MST:
// separate trees already, no BPDUs to filter, no assertion about cabling
// required. Two roots on the SAME instance are two claims on one tree, and that
// is the case [RootWarning] is about.
//
// Returns the contested instance ids, sorted, so a caller can name them.
func (p RootPlan) Contested() []int {
	var byInstance = map[int]int{}
	var r RootIntent
	for _, r = range p.Roots {
		if len(r.Instances) == 0 {
			// The single tree of a non-MST mode. Keyed on a value no MST instance
			// id can take, so it cannot collide with instance 0.
			byInstance[singleTreeKey]++
			continue
		}
		var i int
		for _, i = range r.Instances {
			byInstance[i]++
		}
	}
	var out []int
	var inst, n int
	for inst, n = range byInstance {
		if n > 1 {
			out = append(out, inst)
		}
	}
	sort.Ints(out)
	return out
}

// singleTreeKey stands for "the one tree of a non-MST mode" inside the instance
// tally. Negative because MST instance ids are 0..4094, so it can never collide
// with a real one — and 0 in particular is the CIST, a legitimate id.
const singleTreeKey = -1

// Link is one physical adjacency between two devices, as the topology knows it.
//
// Deliberately minimal and NOT the model's link type: this file computes a graph
// and needs nothing but the four endpoints. Taking the richer type would drag
// internal/model into the driver layer, and would tempt the computation into
// reading fields (freshness, source, refuted) whose meaning belongs to the
// caller's arbitration, not to a path search.
type Link struct {
	ADevice, APort string
	BDevice, BPort string
}

// InterRootFilterPorts returns the ports that must carry BPDU filtering so that
// designated roots end up in SEPARATE spanning trees.
//
// # What it computes
//
// Every port on every shortest path between two roots that contest the same
// instance — INCLUDING the ports on the roots themselves. The operator's
// requirement, stated exactly: "all the switches between them AND themselves
// should have the interfaces in the path between the two roots with bpdufilter
// enable".
//
// # Why SHORTEST paths, and what that means when there are several
//
// The BFS below takes every shortest path, not one of them. That matters because
// two equal-length routes between the roots are exactly the redundant path the
// operator has asserted does not exist — and if the assertion is wrong, filtering
// only ONE of them would leave the other carrying BPDUs, which is the case where
// the two trees silently merge again and the filtering achieves nothing. Cutting
// all of them is the behaviour that matches what was asked for; it also makes a
// wrong assertion visible as an unexpectedly large filter list rather than as a
// storm three weeks later.
//
// Longer, non-shortest paths are NOT cut. Cutting them would mean filtering
// BPDUs across most of a site to separate two switches, which is not a
// configuration anybody wants and would be a spectacular way to misread the
// instruction.
//
// # PURE, and that is where the correctness lives
//
// No I/O, no device, no clock. A wrong answer here is a port that stops
// participating in spanning tree — invisible on any screen until a loop forms —
// so it is written to be exercised entirely from a handful of literal links.
func InterRootFilterPorts(links []Link, roots []string) map[string][]string {
	var out = map[string][]string{}
	if len(roots) < 2 {
		// One root needs no filtering at all: there is one tree and it wins it.
		// Returning empty here is what makes the ordinary single-root case free
		// of every risk this file describes.
		return out
	}

	// Adjacency, both directions, carrying the local port for each hop.
	var adj = map[string][]rootHop{}
	var l Link
	for _, l = range links {
		if l.ADevice == "" || l.BDevice == "" || l.ADevice == l.BDevice {
			// A link to nowhere, or a device cabled to itself. Neither describes a
			// path between two roots, and a self-link would make the search
			// oscillate.
			continue
		}
		adj[l.ADevice] = append(adj[l.ADevice],
			rootHop{peer: l.BDevice, localPort: l.APort, peerPort: l.BPort})
		adj[l.BDevice] = append(adj[l.BDevice],
			rootHop{peer: l.ADevice, localPort: l.BPort, peerPort: l.APort})
	}

	var add = func(device, port string) {
		if device == "" || port == "" {
			return
		}
		var p string
		for _, p = range out[device] {
			if p == port {
				return
			}
		}
		out[device] = append(out[device], port)
	}

	// Every unordered pair of roots.
	var i, j int
	for i = 0; i < len(roots); i++ {
		for j = i + 1; j < len(roots); j++ {
			markShortestPaths(adj, roots[i], roots[j], add)
		}
	}

	// SORTED, because this list is compared between runs — an apply diff that
	// reordered ports would look like a change every cycle and rewrite a
	// configuration that is already correct.
	var d string
	for d = range out {
		sort.Strings(out[d])
	}
	return out
}

// rootHop is one adjacency: who is on the other end, and through which port on
// each side.
type rootHop struct {
	peer      string
	localPort string
	peerPort  string
}

// rootEdge is one hop recorded on a shortest path, pointing BACK toward the
// search origin.
//
// `localPort` is the port on [rootEdge.device] and `peerPort` is the port on the
// node this edge was recorded against. Keeping both named on one struct is what
// stops the two from being swapped — swapping them filters the wrong interface
// on both switches, which produces a plausible configuration and no error.
type rootEdge struct {
	device    string
	localPort string
	peerPort  string
}

// markShortestPaths runs a BFS from `from`, then walks predecessors back from
// `to`, marking every port on every shortest path.
//
// Predecessor SETS rather than a single parent: with one parent per node the
// walk-back yields ONE shortest path, and two equal-cost routes between the
// roots would leave one of them unfiltered — the exact half-measure that makes
// the filtering pointless (see [InterRootFilterPorts]).
func markShortestPaths(adj map[string][]rootHop, from, to string,
	add func(device, port string)) {

	if from == to {
		return
	}
	var dist = map[string]int{from: 0}
	var preds = map[string][]rootEdge{}
	var queue = []string{from}
	var head int
	for head < len(queue) {
		var cur = queue[head]
		head++
		var h rootHop
		for _, h = range adj[cur] {
			var d, seen = dist[h.peer]
			if !seen {
				dist[h.peer] = dist[cur] + 1
				preds[h.peer] = append(preds[h.peer],
					rootEdge{device: cur, localPort: h.localPort, peerPort: h.peerPort})
				queue = append(queue, h.peer)
				continue
			}
			if d == dist[cur]+1 {
				// An ALTERNATIVE shortest path into this node. Recorded rather than
				// discarded — see [InterRootFilterPorts].
				preds[h.peer] = append(preds[h.peer],
					rootEdge{device: cur, localPort: h.localPort, peerPort: h.peerPort})
			}
		}
	}
	var _, reachable = dist[to]
	if !reachable {
		// The two roots are not connected in the topology meshcraft knows. Nothing
		// to filter, and nothing to invent: a path we cannot see is a path we
		// cannot cut, and guessing one would filter BPDUs on a link chosen at
		// random.
		return
	}

	// Walk back from `to` over every predecessor edge, marking BOTH ends of each
	// hop — the peer port on the near device and the local port on the far one.
	// Marking both ends is what makes the roots themselves get filtered, which is
	// what was asked for.
	var visited = map[string]bool{to: true}
	var stack = []string{to}
	for len(stack) > 0 {
		var cur = stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		var e rootEdge
		for _, e = range preds[cur] {
			add(cur, e.peerPort)
			add(e.device, e.localPort)
			if !visited[e.device] {
				visited[e.device] = true
				stack = append(stack, e.device)
			}
		}
	}
}

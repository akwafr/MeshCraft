package driver

import "sort"

// ExpandAllTrunks turns unresolved « all » trunks into an exact tagged set from
// the site VLAN plan, then clears TaggedAtLeast so apply can add AND remove.
//
// The API normally resolves this before calling ApplyPorts (recon.Resolve).
// Expanding again here is what makes the driver converge on the database plan
// even when Desired still carries the floor flag — the operator expectation for
// « all » is the site's VLAN list, not a forever one-way add.
//
// When SiteVLANs is empty, ports stay TaggedAtLeast: stripping would be guessing.
func ExpandAllTrunks(desired map[string]PortState, site []VLAN) map[string]PortState {
	if len(desired) == 0 {
		return desired
	}
	var ids []int
	var seen = map[int]bool{}
	var v VLAN
	for _, v = range site {
		if v.ID < 1 || v.ID > 4092 || seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		ids = append(ids, v.ID)
	}
	if len(ids) == 0 {
		return desired
	}
	sort.Ints(ids)

	var out = make(map[string]PortState, len(desired))
	var name string
	var p PortState
	for name, p = range desired {
		if !p.TaggedAtLeast {
			out[name] = p
			continue
		}
		var tagged []int
		var id int
		for _, id = range ids {
			if id == p.PVID {
				continue
			}
			tagged = append(tagged, id)
		}
		p.Tagged = tagged
		p.TaggedAtLeast = false
		out[name] = p
	}
	return out
}

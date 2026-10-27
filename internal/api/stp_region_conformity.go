package api

import (
	"sort"
	"strings"

	"meshcraft/internal/driver"
)

// STPRegionView compares the MST region a device CARRIES with the one its site
// WANTS, and it is the whole input of the banner above the device sheet
// (ADR-0023).
//
// # Why the verdict is computed here and not in the browser
//
// Both halves are already served: the site's region on
// `GET /api/sites/{siteID}/stp-mst-region` and the observation on
// `deviceView.stp`. Comparing them in TypeScript is two fetches and a loop, and
// it was the first design. It loses on TWO NORMALISATIONS that are 802.1s rules
// rather than presentation, and that already exist on this side:
//
//   - INSTANCE 0 IS IMPLICIT and never compares. EOS prints "Instance 0
//     1-4094" and so does the FS; [driver.MSTRegion].InstanceVLANs documents
//     that the CIST is never listed, and arista-eos's parser drops it for
//     exactly this reason. A browser comparing raw tables would paint the whole
//     fleet as drifted, permanently.
//   - A REGION NAME EQUAL TO THE BRIDGE'S OWN ADDRESS IS NOT A NAME. It is the
//     factory default on every platform surveyed — sw01-arena carried
//     `649D9929F706`, its own MAC — and it means "this switch is a region of
//     one", not "this switch is in region 649D9929F706". instanton already has
//     IonRegionIsDefault for the same fact.
//
// Two copies of a rule drift, and the drift would show up as a banner that is
// wrong — which is unforgivable in a banner whose only job is to be believed.
type STPRegionView struct {
	// Verdict decides which sentence the banner says, and whether it says
	// anything at all. One of:
	//
	//	"match"       — the three components agree. No banner.
	//	"drift"       — the device carries a region, and at least one component
	//	                differs. This is the ARENA rename: seven switches on
	//	                `site-exec` while the site wanted `arena`.
	//	"unregioned"  — the device runs MSTP and carries NO region (empty name,
	//	                or the factory name which is its own bridge address), so
	//	                it is its own region of one. core01-arena.
	//	"not_mstp"    — the device does not run MSTP at all, so it cannot be in
	//	                any MST region whatever its name field says. sw01-arena.
	//
	// The last two are deliberately NOT one verdict. "in MSTP without a region"
	// and "not in MSTP" lead to different gestures and different risks, and
	// collapsing them would make the banner say the wrong thing on one of the
	// two devices the brief names.
	Verdict string `json:"verdict"`
	// Differs names the components that disagree: "name", "revision",
	// "instances". Named rather than counted because one alone splits a region
	// and the three are not repaired the same way — a name is a pin on the site
	// plan, a table is the site's VLAN list.
	Differs []string `json:"differs,omitempty"`
	// CanJoin mirrors stp_caps.mst_region (driver.FeatMSTRegion, ADR-0022):
	// does this CONNECTOR emit a region at all? False gates the "Conform the spanning tree"
	// button off, and an action offered where it cannot land is worse than no
	// action.
	CanJoin bool `json:"can_join"`

	// The two sides, verbatim, so the banner can state what diverges instead of
	// saying "the configuration differs". Nothing else in this payload lets a
	// screen name the three components of the 802.1s identity side by side, and
	// not naming them is precisely what nobody could see before ADR-0021.
	WantName      string        `json:"want_name"`
	WantRevision  int           `json:"want_revision"`
	WantInstances []stpMSTEntry `json:"want_instances"`
	HaveName      string        `json:"have_name"`
	HaveRevision  int           `json:"have_revision"`
	HaveInstances []stpMSTEntry `json:"have_instances"`
}

// stpRegionViewOf builds the verdict, or nil when there is nothing to say.
//
// Nil in three cases, and none of them means "conformant":
//
//   - the site does not want MSTP (its preference list has no "mstp"). A region
//     is then written nowhere and read by nothing, and showing it would be the
//     same non-event noise SpanningTreeConfig already refuses to render;
//   - the site has no region to want (no VLANs, or an unreadable plan);
//   - this device has never been read, so no observation exists to compare. The
//     absence of a banner is NOT proof of conformity, and the ADR says so
//     rather than inventing a green verdict out of a missing read.
func stpRegionViewOf(want driver.MSTRegion, wantsMST bool,
	observed *driver.SpanningTreeState, canJoin bool) *STPRegionView {
	if !wantsMST || !want.Configured() || observed == nil {
		return nil
	}
	var have = observed.Region
	var out = STPRegionView{
		CanJoin:       canJoin,
		WantName:      want.Name,
		WantRevision:  want.Revision,
		WantInstances: stpMSTEntriesOf(want.InstanceVLANs),
		HaveName:      strings.TrimSpace(have.Name),
		HaveRevision:  have.Revision,
		HaveInstances: stpMSTEntriesOf(have.InstanceVLANs),
	}
	// The components are compared in EVERY verdict, including the two where the
	// device is plainly outside the region: the banner renders the table either
	// way, and a row the operator can read "wanted 10-11,42,60,98-100, carries
	// nothing" is the sentence that explains what conforming would do.
	if strings.TrimSpace(have.Name) != strings.TrimSpace(want.Name) {
		out.Differs = append(out.Differs, "name")
	}
	if have.Revision != want.Revision {
		out.Differs = append(out.Differs, "revision")
	}
	if !stpInstanceTablesEqual(want.InstanceVLANs, have.InstanceVLANs) {
		out.Differs = append(out.Differs, "instances")
	}

	switch {
	case !strings.EqualFold(strings.TrimSpace(observed.Mode), "mstp"):
		// NOT a region comparison at all: a bridge that does not run MSTP
		// advertises no MST digest, so it is in no region however its name field
		// reads. sw01-arena printed "MST Region: Name: [ 649D9929F706 ]" under
		// "protocol RSTP(2004)" — a region block that is inert.
		out.Verdict = "not_mstp"
	case stpRegionNameIsFactory(have.Name, observed.BridgeID):
		out.Verdict = "unregioned"
	case len(out.Differs) == 0:
		out.Verdict = "match"
	default:
		out.Verdict = "drift"
	}
	return &out
}

// stpMSTEntriesOf renders an instance→VLAN map as the ordered pairs the reply
// carries. Same shape and same reason as handleGetSTPMSTRegion: ranging a Go
// map yields a different order every request, and a value whose only use is
// line-by-line comparison must not reshuffle between two renders.
func stpMSTEntriesOf(m map[int][]int) []stpMSTEntry {
	var out = []stpMSTEntry{}
	var ids = make([]int, 0, len(m))
	var id int
	for id = range m {
		// Instance 0 is the CIST and implicit (cf. driver.MSTRegion). A reader
		// that recorded it anyway would make two identical regions compare
		// unequal, so it is dropped here as well as in the parsers — this
		// function is the last gate before a human compares two columns.
		if id == 0 {
			continue
		}
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id = range ids {
		var vlans = append([]int{}, m[id]...)
		sort.Ints(vlans)
		out = append(out, stpMSTEntry{Instance: id, VLANs: vlans})
	}
	return out
}

// stpInstanceTablesEqual compares two VLAN-to-instance tables as 802.1s does:
// as SETS, instance 0 excluded, an empty instance indistinguishable from an
// absent one.
//
// The set discipline is not fussiness. A connector reads its table from a
// range-compressed string ("10-11,42,60,98-100") and expands it; nothing
// promises the expansion comes out in the same order twice, and an order
// difference is not a region difference — the digest is computed over the
// 4096-entry table, not over a printed list.
func stpInstanceTablesEqual(want, have map[int][]int) bool {
	var norm = func(m map[int][]int) map[int]map[int]bool {
		var out = map[int]map[int]bool{}
		var id int
		var vlans []int
		for id, vlans = range m {
			if id == 0 || len(vlans) == 0 {
				continue
			}
			var set = map[int]bool{}
			var v int
			for _, v = range vlans {
				set[v] = true
			}
			out[id] = set
		}
		return out
	}
	var a = norm(want)
	var b = norm(have)
	if len(a) != len(b) {
		return false
	}
	var id int
	var set map[int]bool
	for id, set = range a {
		var other, ok = b[id]
		if !ok || len(other) != len(set) {
			return false
		}
		var v int
		for v = range set {
			if !other[v] {
				return false
			}
		}
	}
	return true
}

// stpRegionNameIsFactory reports whether a region name means "this device is a
// region of ONE" rather than naming a region.
//
// Two forms, both measured:
//
//   - EMPTY. core01-arena printed "Name []" on 2026-10-25. EOS then derives
//     the name from the bridge address internally, so two switches that both
//     print an empty name are in DIFFERENT regions — which is why
//     [driver.MSTRegion.Configured] already calls a nameless region no region.
//   - THE BRIDGE'S OWN ADDRESS. sw01-arena carried `649D9929F706`, its own MAC,
//     the factory default on this range. Read literally it is a perfectly valid
//     32-character name, and a comparison that took it literally would report
//     "the name differs" — true, but it buries the fact that matters: nobody
//     configured anything here.
//
// THE BRIDGE ADDRESS IS REQUIRED to call the second form, and that is
// deliberate. A twelve-hex-character name is not proof of a factory default on
// its own, and guessing would mean telling an operator his region is unnamed
// because he named it in hex. With no bridge address read, the name is compared
// literally and the divergence shows as a name difference — which is honest,
// just less specific.
func stpRegionNameIsFactory(name, bridgeID string) bool {
	var n = stpStripSeparators(name)
	if n == "" {
		return true
	}
	if len(n) != 12 || !stpAllHex(n) {
		return false
	}
	var b = stpStripSeparators(bridgeID)
	if len(b) < 12 {
		return false
	}
	// Suffix and not equality: a bridge identifier usually carries the priority
	// in front of the address (instanton builds "32768-649d9929f706", EOS
	// prints the two joined), and only the address half can equal a region name.
	return strings.HasSuffix(b, n)
}

func stpStripSeparators(s string) string {
	var r = strings.ToLower(strings.TrimSpace(s))
	r = strings.ReplaceAll(r, ":", "")
	r = strings.ReplaceAll(r, "-", "")
	r = strings.ReplaceAll(r, ".", "")
	r = strings.ReplaceAll(r, " ", "")
	return r
}

func stpAllHex(s string) bool {
	var i int
	for i = 0; i < len(s); i++ {
		var c = s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

package swos

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

// Link aggregation on SwOS: the /lacp.b page, READ ONLY.
//
// # The capture that anchors the read (lab CSS326, 2026-08-13)
//
//	{mode:[0x00,…×26], grp:[0x00,…×26], sgrp:[0x00,…×26],
//	 mac:['000000000000',…×26]}
//
// Four per-port arrays, one entry per faceplate port: `mode` is the port's
// LACP participation, `grp` the aggregation group the port has FORMED, `sgrp`
// the static-group assignment, `mac` the partner's address. All zeros on the
// reference device — no aggregation anywhere — which is exactly what makes
// the shape trustworthy: it is the page's ground state, not an inference.
//
// # Why there is NO writer
//
// On SwOS an aggregate is not an object anyone creates: ports are marked for
// LACP (or statically grouped) and the groups HAPPEN. The write forms on this
// page have never been exercised — no partner hardware faces the lab CSS326 —
// and per the house rule an unproven write is refused, not attempted. The
// reader below keeps the page honest on screen: a group formed by hand in the
// SwOS UI shows up as an aggregate with its members.

// readLAGs folds the /lacp.b arrays into aggregates: one LAG per distinct
// nonzero group id, its members the ports carrying that id.
func (a *Admin) readLAGs(ctx context.Context) ([]driver.LAGState, error) {
	var page, err = a.Get(ctx, PageLACP)
	if err != nil {
		return nil, err
	}
	var grp, hasGrp = page.Nums("grp")
	var sgrp, hasSgrp = page.Nums("sgrp")
	if !hasGrp && !hasSgrp {
		return nil, fmt.Errorf("/lacp.b serves no grp/sgrp arrays on this train")
	}
	var facade = a.facade()
	var members = map[uint32][]string{}
	var static = map[uint32]bool{}
	var collect = func(ids []uint32, isStatic bool) {
		var i int
		for i = range ids {
			if ids[i] == 0 {
				continue
			}
			members[ids[i]] = append(members[ids[i]], facade.Name(i))
			if isStatic {
				static[ids[i]] = true
			}
		}
	}
	if hasGrp {
		collect(grp, false)
	}
	if hasSgrp {
		collect(sgrp, true)
	}
	var out []driver.LAGState
	var id uint32
	for id = range members {
		var mode = driver.LAGModeLACP
		if static[id] {
			mode = driver.LAGModeStatic
		}
		out = append(out, driver.LAGState{
			Name: driver.StandardLAGName(int(id)), Mode: mode,
			Members: members[id],
			// A group listed on this page exists because frames formed it.
			Running: true,
		})
	}
	driver.SortLAGs(out)
	return out, nil
}

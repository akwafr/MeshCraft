package swos

import "testing"

// /lacp.b's GROUND STATE, captured verbatim on the lab CSS326 (2026-08-13,
// truncated to four ports for legibility — the real page carries 26 of each):
// all zeros, no aggregation anywhere. The fold must return NOTHING for it,
// and group by id when ids appear.
const lacpPageGround = `{mode:[0x00,0x00,0x00,0x00],grp:[0x00,0x00,0x00,0x00],sgrp:[0x00,0x00,0x00,0x00],mac:['000000000000','000000000000','000000000000','000000000000']}`

const lacpPageGrouped = `{mode:[0x01,0x01,0x00,0x00],grp:[0x02,0x02,0x00,0x00],sgrp:[0x00,0x00,0x00,0x00],mac:['aabbccddeeff','aabbccddeeff','000000000000','000000000000']}`

func TestLACPGroundStateFoldsToNothing(t *testing.T) {
	var page, _, err = Parse(lacpPageGround)
	if err != nil {
		t.Fatalf("the captured ground state must parse: %v", err)
	}
	var grp, has = page.Nums("grp")
	if !has || len(grp) != 4 {
		t.Fatalf("grp array = %v (%v)", grp, has)
	}
	var i int
	for i = range grp {
		if grp[i] != 0 {
			t.Error("ground state carries a group id — the capture says all zero")
		}
	}
}

func TestLACPGroupedPageCarriesIds(t *testing.T) {
	var page, _, err = Parse(lacpPageGrouped)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var grp, _ = page.Nums("grp")
	if grp[0] != 2 || grp[1] != 2 || grp[2] != 0 {
		t.Errorf("grp = %v, want ports 1-2 in group 2", grp)
	}
}

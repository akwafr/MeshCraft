package edgeswitch

import (
	"testing"

	"meshcraft/internal/driver"
)

// The table with a member ADDED LIVE on the lab EP-S16 (2026-08-13): 0/5
// joined to 3/1 with "addport", the continuation line carrying the partner
// half. The five empty pre-provisioned slots must NOT become aggregates.
const portChannelAllCapture = `show port-channel all


Log.    Channel                     Adm.      Mbr    Device/       Port      Port
Intf    Name            Min  Link   Mode Type Ports  Timeout       Speed     Active
------- --------------- ---- ------ ---- ---- ------ ------------- --------- -------
3/1                     1    Down   En.  Dyn. 0/5    actor/long    Auto      False
                                                     partner/long
3/2                     1    Down   En.  Dyn.
3/3                     1    Down   En.  Dyn.
3/4                     1    Down   En.  Dyn.
3/5                     1    Down   En.  Dyn.
3/6                     1    Down   En.  Dyn.

(UBNT EdgeSwitch) #`

func TestParsePortChannelAll(t *testing.T) {
	var lags = parsePortChannelAll(portChannelAllCapture)
	if len(lags) != 1 {
		t.Fatalf("lags = %d, want 1 — the memberless pre-provisioned slots are "+
			"the platform's empty scaffolding, not aggregates", len(lags))
	}
	var l = lags[0]
	if l.Name != "LAG1" || l.Raw != "3/1" || l.Mode != driver.LAGModeLACP {
		t.Errorf("lag = %+v, want LAG1/3/1/lacp (Dyn.)", l)
	}
	if len(l.Members) != 1 || l.Members[0] != "GE5" {
		t.Errorf("members = %v, want [GE5] and NOT a row from the partner/long "+
			"continuation line", l.Members)
	}
	if l.Running {
		t.Error("Link Down must read as not running")
	}
}

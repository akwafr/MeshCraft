package mdpv1

import "testing"

// The guardrail must fail CLOSED when the membership read came back empty.
//
// This pins the decision that four connectors now share. The case that matters
// is the middle one: a management VLAN that reads perfectly beside a membership
// map that does not, which is what a truncated running-config produces and what
// would otherwise shut the uplink.
func TestMgmtVLANWhenMembershipRead(t *testing.T) {
	var cases = []struct {
		name  string
		mgmt  int
		count int
		want  int
	}{
		{"membership read, mgmt known — the ordinary case", 99, 26, 99},
		{"membership EMPTY while mgmt reads fine — the outage", 99, 0, 0},
		{"both unknown stays unknown", 0, 0, 0},
		{"mgmt unknown, membership read — still unknown", 0, 26, 0},
		{"one port is a read, not an absence", 99, 1, 99},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = MgmtVLANWhenMembershipRead(c.mgmt, c.count)
			if got != c.want {
				t.Errorf("MgmtVLANWhenMembershipRead(%d, %d) = %d, want %d",
					c.mgmt, c.count, got, c.want)
			}
		})
	}
}

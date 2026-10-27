package api

// The per-sample inventory gate, pinned shape by shape. The incident behind it:
// core01.lab (gNMI-collected) showed all 48 ports unplugged for two days
// with 26 links up, because the gate read "no iftype" as "old proxy" and
// refused every streamed oper-status sample. Cf. inventoryEligible.

import "testing"

func TestInventoryEligibleShapes(t *testing.T) {
	var cases = []struct {
		name     string
		labels   map[string]string
		streamed bool
		want     bool
	}{
		{
			// The SNMP path, unchanged: ethernet interfaces refresh the facade.
			name:   "snmp ethernet",
			labels: map[string]string{"ifindex": "3", "iftype": "6"},
			want:   true,
		},
		{
			// An SVI or tunnel interface must not paint a faceplate port.
			name:   "snmp non-ethernet",
			labels: map[string]string{"ifindex": "1000", "iftype": "135"},
			want:   false,
		},
		{
			// An older proxy sends `ifindex` without `iftype`: metrics yes,
			// inventory no. This is the closed-by-default the gate exists for,
			// and the streamed flag must not reopen it — the label says SNMP.
			name:     "old proxy, ifindex only",
			labels:   map[string]string{"ifindex": "3"},
			streamed: true,
			want:     false,
		},
		{
			// The fix: a streaming collector's sample (no ifindex, no iftype)
			// refreshes the facade of the device it is DECLARED to collect.
			name:     "gnmi sample on a gnmi-collected device",
			labels:   map[string]string{"port": "Ethernet34"},
			streamed: true,
			want:     true,
		},
		{
			// The same label shape from a scraping channel (SwOS page, RouterOS
			// REST) on a device whose declared collector is not gNMI: curves
			// only, exactly as before.
			name:   "label-only sample on a non-gnmi device",
			labels: map[string]string{"port": "sfp-sfpplus1"},
			want:   false,
		},
	}
	var c = cases[0]
	for _, c = range cases {
		if inventoryEligible(c.labels, c.streamed) != c.want {
			t.Errorf("%s: eligible = %v, want %v", c.name, !c.want, c.want)
		}
	}
}

package eos

import (
	"testing"

	"meshcraft/internal/driver"
)

// The dense table with the aggregate BUILT LIVE on core01 (2026-08-13):
// Po999, LACP active, one down member. The flags legend rides along because
// the parser must not read legend lines as rows.
const portChannelDenseCapture = `show port-channel dense

                  Flags
-------------------------- ----------------------------- -------------------------
   a - LACP Active            p - LACP Passive           * - static fallback
   F - Fallback enabled       f - Fallback configured    ^ - individual fallback
   U - In Use                 D - Down
   + - In-Sync                - - Out-of-Sync            i - incompatible with agg
   P - bundled in Po          s - suspended              G - Aggregable
   I - Individual             S - ShortTimeout           w - wait for agg
  E - Inactive. The number of configured port channels exceeds the config limit
   M - Exceeds maximum weight

Number of channels in use: 0
Number of aggregators: 1

   Port-Channel       Protocol    Ports
------------------ -------------- -------
   Po999(D)           LACP(a)     Et1(D)

core01-lab#`

func TestParsePortChannelDense(t *testing.T) {
	var lags = parsePortChannelDense(portChannelDenseCapture)
	if len(lags) != 1 {
		t.Fatalf("lags = %d, want 1 (and none from the legend lines)", len(lags))
	}
	var l = lags[0]
	if l.Name != "LAG999" || l.Raw != "Port-Channel999" || l.Mode != driver.LAGModeLACP {
		t.Errorf("lag = %+v, want LAG999/Port-Channel999/lacp", l)
	}
	if len(l.Members) != 1 || l.Members[0] != "ET1" {
		t.Errorf("members = %v, want the standard name ET1", l.Members)
	}
	if l.Running {
		t.Error("Po999(D) is down — running must be false")
	}
}

// The empty table (the fleet's ordinary state) parses to zero aggregates,
// not to an error and not to legend-derived garbage.
func TestParseDenseEmptyTable(t *testing.T) {
	var empty = portChannelDenseCapture[:len(portChannelDenseCapture)-60]
	_ = empty
	var lags = parsePortChannelDense(`
Number of channels in use: 0
Number of aggregators: 0

   Port-Channel       Protocol    Ports
------------------ -------------- -----
`)
	if len(lags) != 0 {
		t.Errorf("lags = %v, want none", lags)
	}
}

// The VLAN half comes from the Po's own running-config section, in the same
// switchport vocabulary as an Ethernet block.
func TestLagVLANFromRun(t *testing.T) {
	var run = `interface Port-Channel7
   switchport mode trunk
   switchport trunk native vlan 99
   switchport trunk allowed vlan 10,42,98-100
!
interface Ethernet1
   switchport mode access
!`
	var v = lagVLANFromRun(run, "Port-Channel7")
	if v.Mode != "trunk" || v.PVID != 99 {
		t.Errorf("vlan = %+v, want trunk native 99", v)
	}
	if len(v.Tagged) != 5 || v.Tagged[4] != 100 {
		t.Errorf("tagged = %v, want the range expanded", v.Tagged)
	}
}

package ios

import (
	"testing"

	"meshcraft/internal/driver"
)

// The summary with the group BUILT LIVE on the lab C2960X (2026-08-13). The
// flags legend rides along: the parser must not read legend lines as rows.
const etherChannelSummaryCapture = `show etherchannel summary
Flags:  D - down        P - bundled in port-channel
        I - stand-alone s - suspended
        H - Hot-standby (LACP only)
        R - Layer3      S - Layer2
        U - in use      N - not in use, no aggregation
        f - failed to allocate aggregator

Number of channel-groups in use: 1
Number of aggregators:           1

Group  Port-channel  Protocol    Ports
------+-------------+-----------+-----------------------------------------------
5      Po5(SD)         LACP      Gi1/0/5(D)

CiscoLabFirst1#`

func TestParseEtherChannelSummary(t *testing.T) {
	var lags = parseEtherChannelSummary(etherChannelSummaryCapture)
	if len(lags) != 1 {
		t.Fatalf("lags = %d, want 1", len(lags))
	}
	var l = lags[0]
	if l.Name != "LAG5" || l.Raw != "Port-channel5" || l.Mode != driver.LAGModeLACP {
		t.Errorf("lag = %+v, want LAG5/Port-channel5/lacp", l)
	}
	if len(l.Members) != 1 || l.Members[0] != "GE5" {
		t.Errorf("members = %v, want the standard GE5", l.Members)
	}
	if l.Running {
		t.Error("Po5(SD) — S layer2, D down: running must be false")
	}
}

func TestLagVLANFromRunIOS(t *testing.T) {
	var run = "interface Port-channel5\r\n switchport mode trunk\r\n" +
		" switchport trunk native vlan 99\r\n" +
		" switchport trunk allowed vlan 10,42,98-100\r\n!\r\n"
	var v = lagVLANFromRunIOS(run, "Port-channel5")
	if v.Mode != "trunk" || v.PVID != 99 || len(v.Tagged) != 5 {
		t.Errorf("vlan = %+v", v)
	}
}

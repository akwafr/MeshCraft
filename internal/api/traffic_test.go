package api

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/model"
)

func devPorts(status string, ports map[string]any) model.Device {
	var raw, _ = json.Marshal(ports)
	return model.Device{ID: idA, Status: status, Ports: raw}
}

func TestActiveUnmanagedPortCountsInRates(t *testing.T) {
	var d = devPorts("ok", map[string]any{
		"SFP1":  map[string]any{"link": "", "rx_mbps": 0.0, "tx_mbps": 0.0},
		"SFP2":  map[string]any{"link": "10G", "rx_mbps": 12.5, "tx_mbps": 3.5},
		"Port1": map[string]any{"link": ""},
	})
	var rx, tx = portRates(d)
	if rx != 12.5 || tx != 3.5 {
		t.Errorf("rates = %v / %v; want 12.5 / 3.5", rx, tx)
	}
}

func TestAllVLANUplinkStillExcludedFromSum(t *testing.T) {
	var d = devPorts("ok", map[string]any{
		"GE1":  map[string]any{"link": "1G", "mode": "access", "pvid": 100, "rx_mbps": 4.0},
		"GE49": map[string]any{"link": "10G", "mode": "trunk", "tagged": "all", "rx_mbps": 900.0},
	})
	var rx, _ = portRates(d)
	if rx != 4 {
		t.Errorf("ingress rate = %v, want 4", rx)
	}
}

func TestUnreachableDeviceCountsNoTraffic(t *testing.T) {
	var d = devPorts("off", map[string]any{
		"SFP2": map[string]any{"link": "10G", "rx_mbps": 12.5, "tx_mbps": 3.5},
	})
	var rx, tx = portRates(d)
	if rx != 0 || tx != 0 {
		t.Errorf("rates = %v / %v on unreachable device", rx, tx)
	}
}

func TestPortWithoutLinkNeverCounts(t *testing.T) {
	var d = devPorts("ok", map[string]any{
		"GE1": map[string]any{"link": "", "mode": "access", "rx_mbps": 40.0},
	})
	var rx, _ = portRates(d)
	if rx != 0 {
		t.Errorf("rate = %v on port without link", rx)
	}
}

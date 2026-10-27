package api

// facade.resolve is what attaches an SNMP sample or an LLDP neighbor to a
// port. It is also where a NUMBERING connector can silently lose every port:
// the faceplate carries standard-model names ("GE11") while the device's
// `ifDescr` and LLDP local-port IDs carry its own ("Ethernet1/11"). Observed
// on an FS S3900 the day its connector adopted GE-style names — every port
// read as down and every neighbor was dropped while the switch was fine.

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/model"

	_ "meshcraft/internal/driver/fs/s3900" // registers "fs-s3900" via init()
)

func s3900Device(t *testing.T) model.Device {
	t.Helper()
	var extra, _ = json.Marshal(map[string]string{"driver": "fs-s3900"})
	return model.Device{
		ID:    "dev-s3900",
		Extra: extra,
		PortGroups: json.RawMessage(
			`[{"prefix":"GE","from":1,"count":24,"silk":"1/"},` +
				`{"prefix":"GE","from":25,"count":4,"silk":"1/"}]`),
	}
}

func TestFacadeResolvesPlatformNamesThroughTheConnector(t *testing.T) {
	var f = facadeOf(s3900Device(t))

	var cases = []struct{ descr, want string }{
		{"GE11", "GE11"},         // already standard: exact match
		{"Ethernet1/11", "GE11"}, // SNMP ifDescr on this platform
		{"Eth 1/11", "GE11"},     // CLI-style spelling
		{"1/11", "GE11"},         // bare index, as some LLDP agents report
		{"Ethernet1/28", "GE28"}, // the SFP+ group resolves too
		// The REAL ifDescr this platform serves — the form that was silently
		// unresolvable and left every port reading as down.
		{"Ethernet Port on unit 1, port 11", "GE11"},
		{"Ethernet Port on unit 1, port 28", "GE28"},
	}
	var c struct{ descr, want string }
	for _, c = range cases {
		var got = f.resolve(c.descr)
		if got != c.want {
			t.Errorf("resolve(%q) = %q, want %q", c.descr, got, c.want)
		}
	}
}

func TestFacadeRefusesWhatTheFaceplateDoesNotCarry(t *testing.T) {
	var f = facadeOf(s3900Device(t))

	// Port 40 does not exist on a 28-port faceplate: the connector's
	// translation must not conjure an inventory port out of a sample.
	var got = f.resolve("Ethernet1/40")
	if got != "" {
		t.Errorf("resolve(Ethernet1/40) = %q, want \"\" (not on this faceplate)", got)
	}
	// Stacking unit 2 is outside this connector's coverage.
	got = f.resolve("Ethernet2/1")
	if got != "" {
		t.Errorf("resolve(Ethernet2/1) = %q, want \"\"", got)
	}
	// A logical interface must never be taken for a physical port. These are
	// the exact labels this device serves alongside its ports (indexes 363,
	// 746, 1001+): a measurement must not invent inventory.
	var notPorts = []string{"Vlan99", "Console port", "Loopback Interface 0", "VLAN ID 0042"}
	var n string
	for _, n = range notPorts {
		got = f.resolve(n)
		if got != "" {
			t.Errorf("resolve(%q) = %q, want \"\"", n, got)
		}
	}
}

func TestFacadeWithoutConnectorStillResolvesTextually(t *testing.T) {
	// A device whose driver declares no translation (or is unknown to this
	// binary) keeps the three purely textual steps — no regression for the
	// self-naming connectors.
	var d = model.Device{
		ID:         "dev-ros",
		Extra:      json.RawMessage(`{"driver":"mikrotik-routeros"}`),
		PortGroups: json.RawMessage(`[{"prefix":"ether","from":1,"count":8}]`),
	}
	var f = facadeOf(d)
	var got = f.resolve("ether3")
	if got != "ether3" {
		t.Errorf("resolve(ether3) = %q, want ether3", got)
	}
	got = f.resolve("Ether 3")
	if got != "ether3" {
		t.Errorf("normalized match lost: resolve(Ether 3) = %q", got)
	}
}

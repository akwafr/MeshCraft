package api

// A connector that NUMBERS its ports carries two halves of one translation:
// the faceplate it writes into `devices.port_groups` (prefix + from + count),
// and driver.PortNamer, which turns a name observed on the platform into that
// same standard-model name. The two must agree, and nothing in the type system
// makes them.
//
// When they disagree the failure is SILENT and total, and it has happened: an
// FS S3900 whose faceplate said "GE11" while its namer still answered
// "ethernet 1/11" read every port as down and dropped every LLDP neighbour on
// a switch that was perfectly healthy. facadeNamingStale is the runtime
// detector for it (recon_handlers.go); this test is the build-time one, so a
// connector cannot ship with the two halves out of step.

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/driver"

	_ "meshcraft/internal/driver/cisco/ios" // registers "cisco-ios" via init()
	_ "meshcraft/internal/driver/fs/s3900"  // registers "fs-s3900" via init()
	_ "meshcraft/internal/driver/instanton" // registers "aruba-instant-on-1930"
	_ "meshcraft/internal/driver/mikrotik/routeros"
	_ "meshcraft/internal/driver/mikrotik/swos"
)

// faceplates: the port groups each numbering connector actually emits, as its
// own faceplate reader builds them. Kept as literal JSON on purpose — this is
// the shape that reaches the database, and a test written against the Go
// helper that produced it would not notice the two drifting apart.
var faceplates = map[string]string{
	"fs-s3900": `[{"prefix":"GE","from":1,"count":24,"silk":"1/"},
	              {"prefix":"GE","from":25,"count":4,"silk":"1/"}]`,
	// Two prefixes, because a 48FPD carries Gi1/0/1 AND Te1/0/1 — the same
	// number on two different sockets.
	"cisco-ios": `[{"prefix":"GE","from":1,"count":48,"silk":"Gi1/0/"},
	               {"prefix":"XG","from":1,"count":2,"silk":"Te1/0/"}]`,
}

func TestConnectorFaceplatesAgreeWithTheirOwnPortNamer(t *testing.T) {
	var name, raw string
	for name, raw = range faceplates {
		var drv, known = driver.Get(name)
		if !known {
			t.Fatalf("connector %q is not registered: the blank import is missing", name)
		}
		var namer, canName = driver.AsPortNamer(drv)
		if !canName {
			t.Fatalf("connector %q writes a numbered faceplate but declares no "+
				"driver.PortNamer: nothing can attach an SNMP sample or an LLDP "+
				"neighbour to its ports", name)
		}
		if facadeNamingStale(namer, json.RawMessage(raw)) {
			t.Errorf("connector %q: its faceplate and its port namer disagree — every "+
				"port would read as down and every LLDP neighbour would be dropped", name)
		}
	}
}

// TestPortNamerNeverRenamesAStandardName: fed a name that is ALREADY in the
// standard model, a namer must either refuse it or return it unchanged — never
// map it to a different standard name.
//
// Refusing is the expected answer and not a gap: StandardPortName translates a
// name observed ON THE PLATFORM, and "GE11" is not one — no Catalyst and no
// S3900 ever utters it. Both callers are written for that: facadeNamingStale
// treats ok=false as "nothing to be stale against", and remapPortNamesIfStale
// leaves the key alone.
//
// What would be a genuine defect is the third case: a namer that ACCEPTS
// "GE11" and answers something else. facadeNamingStale would then declare the
// faceplate stale on every single read, and the catch-up would rewrite it for
// ever — while remapPortNamesIfStale re-keyed every port intent behind it.
func TestPortNamerNeverRenamesAStandardName(t *testing.T) {
	var cases = map[string][]string{
		"fs-s3900":  {"GE1", "GE24", "GE28"},
		"cisco-ios": {"GE1", "GE48", "XG1", "XG2"},
	}
	var name string
	var names []string
	for name, names = range cases {
		var drv, _ = driver.Get(name)
		var namer, canName = driver.AsPortNamer(drv)
		if !canName {
			t.Fatalf("%s: no PortNamer", name)
		}
		var std string
		for _, std = range names {
			var got, ok = namer.StandardPortName(std)
			if ok && got != std {
				t.Errorf("%s: StandardPortName(%q) = %q — a standard name renamed to "+
					"another one makes the faceplate catch-up fire on every read and "+
					"re-keys every port intent behind it", name, std, got)
			}
		}
	}
}

// TestPortNamersNeverNameALogicalInterface: the walks these connectors resolve
// against return VLAN interfaces, loopbacks, port-channels and console ports
// alongside the physical jacks. Naming one of those invents an inventory port
// out of a measurement — and meshcraft would eventually try to write to it.
func TestPortNamersNeverNameALogicalInterface(t *testing.T) {
	var notPorts = []string{
		"Vlan99", "Vlan1", "VLAN ID 0042", "Loopback0", "Loopback Interface 0",
		"Port-channel1", "Null0", "Console port", "craft", "",
	}
	var name string
	for name = range faceplates {
		var drv, _ = driver.Get(name)
		var namer, canName = driver.AsPortNamer(drv)
		if !canName {
			continue
		}
		var n string
		for _, n = range notPorts {
			var got, ok = namer.StandardPortName(n)
			if ok {
				t.Errorf("%s: StandardPortName(%q) = %q — a logical interface must never "+
					"be named as a faceplate port", name, n, got)
			}
		}
	}
}

package api

// remapPortsToStandardModel is what keeps devices.ports keyed on the SAME
// vocabulary whether a device was adopted by pasting its configuration or by
// push/live read through its connector. Tested against the registered
// fs-s3900 driver (a real driver.PortNamer) rather than a fake, since the
// registry is what production actually resolves through.

import (
	"testing"

	"meshcraft/internal/confimport"

	_ "meshcraft/internal/driver/fs/s3900" // registers "fs-s3900" via init()
)

func TestRemapPortsToStandardModel_FSDriverTranslatesToGE(t *testing.T) {
	var ports = map[string]confimport.Port{
		"ethernet 1/1":  {Mode: "access"},
		"ethernet 1/25": {Mode: "trunk"},
	}
	var groups = []confimport.PortGroup{
		{Prefix: "ethernet 1/", From: 1, To: 28, SpeedMbps: 1000},
	}
	var outPorts, outGroups, warnings = remapPortsToStandardModel("fs-s3900", ports, groups)

	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(outGroups) != 1 || outGroups[0].Prefix != "GE" || outGroups[0].From != 1 || outGroups[0].To != 28 {
		t.Fatalf("groups=%v", outGroups)
	}
	var _, ok = outPorts["GE1"]
	if !ok {
		t.Error("GE1 missing after remap")
	}
	_, ok = outPorts["GE25"]
	if !ok {
		t.Error("GE25 missing after remap")
	}
	if outPorts["GE25"].Mode != "trunk" {
		t.Errorf("GE25 mode=%q, intent must survive the remap", outPorts["GE25"].Mode)
	}
	// A port present in the group but with no explicit block in the pasted
	// config (e.g. GE2..GE24) must still exist, at the factory default.
	_, ok = outPorts["GE2"]
	if !ok {
		t.Error("GE2 (implicit, no block in the pasted config) must still be created")
	}
}

func TestRemapPortsToStandardModel_UnknownDriverPassesThrough(t *testing.T) {
	var ports = map[string]confimport.Port{"ether3": {Mode: "access"}}
	var groups = []confimport.PortGroup{{Prefix: "ether", From: 3, To: 3}}

	var outPorts, outGroups, warnings = remapPortsToStandardModel("mikrotik-routeros", ports, groups)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	var _, ok = outPorts["ether3"]
	if !ok {
		t.Error("a driver without PortNamer must pass the input through unchanged")
	}
	if len(outGroups) != 1 || outGroups[0].Prefix != "ether" {
		t.Errorf("groups=%v", outGroups)
	}
}

func TestRemapPortsToStandardModel_UnmappableGroupIsDroppedWithWarning(t *testing.T) {
	// Unit 2 (stacking) has no standard name in this connector: the group must
	// be dropped, named in a warning, never guessed port by port.
	var ports = map[string]confimport.Port{"ethernet 2/1": {Mode: "access"}}
	var groups = []confimport.PortGroup{{Prefix: "ethernet 2/", From: 1, To: 4}}

	var outPorts, outGroups, warnings = remapPortsToStandardModel("fs-s3900", ports, groups)
	if len(outPorts) != 0 || len(outGroups) != 0 {
		t.Fatalf("an unmappable group must be fully dropped: ports=%v groups=%v", outPorts, outGroups)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one warning, got %v", warnings)
	}
}

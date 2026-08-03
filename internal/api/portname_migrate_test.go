package api

// remapPortNamesIfStale is the pure decision behind the one-time catch-up
// that keeps an EXISTING device's devices.ports keys aligned with its
// connector's CURRENT standard naming. Tested against the real registered
// fs-s3900 driver, which is what exposed this gap in production: a device
// adopted before the connector switched to standard "GEn" names kept its
// stale "ethernet 1/n" keys, and every apply against it silently missed —
// desired and observed were compared under two different vocabularies.

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/driver"

	_ "meshcraft/internal/driver/fs/s3900" // registers "fs-s3900" via init()
)

func fsS3900Namer(t *testing.T) driver.PortNamer {
	t.Helper()
	var drv, known = driver.Get("fs-s3900")
	if !known {
		t.Fatal("fs-s3900 driver not registered")
	}
	var namer, ok = driver.AsPortNamer(drv)
	if !ok {
		t.Fatal("fs-s3900 driver does not implement driver.PortNamer")
	}
	return namer
}

func TestRemapPortNamesIfStale_RenamesRawPlatformKeys(t *testing.T) {
	var namer = fsS3900Namer(t)
	var ports = json.RawMessage(
		`{"ethernet 1/1":{"mode":"access","pvid":1},"ethernet 1/25":{"mode":"trunk"}}`)

	var out, changed = remapPortNamesIfStale(namer, ports)
	if !changed {
		t.Fatal("stale raw keys must be reported as changed")
	}
	var decoded map[string]json.RawMessage
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatalf("output is not valid JSON: %s", out)
	}
	if len(decoded) != 2 {
		t.Fatalf("renamed map must keep the same port count, got %v", decoded)
	}
	if _, ok := decoded["GE1"]; !ok {
		t.Error("GE1 missing after remap")
	}
	if _, ok := decoded["GE25"]; !ok {
		t.Error("GE25 missing after remap")
	}
	if _, ok := decoded["ethernet 1/1"]; ok {
		t.Error("the stale raw key must be GONE, not merely duplicated")
	}
}

func TestRemapPortNamesIfStale_AlreadyStandardIsNoOp(t *testing.T) {
	var namer = fsS3900Namer(t)
	var ports = json.RawMessage(`{"GE1":{"mode":"access","pvid":1}}`)

	var out, changed = remapPortNamesIfStale(namer, ports)
	if changed {
		t.Error("already-standard keys must not be reported as changed")
	}
	if string(out) != string(ports) {
		t.Errorf("unchanged input must be returned as-is, got %s", out)
	}
}

func TestRemapPortNamesIfStale_UnmappableKeyPassesThrough(t *testing.T) {
	var namer = fsS3900Namer(t)
	// A stacking-unit-2 port has no standard name in this connector: it must
	// survive the remap untouched, never dropped and never guessed.
	var ports = json.RawMessage(`{"ethernet 2/1":{"mode":"access"},"ethernet 1/5":{"mode":"access"}}`)

	var out, changed = remapPortNamesIfStale(namer, ports)
	if !changed {
		t.Fatal("GE5 should have been renamed even though ethernet 2/1 could not be")
	}
	var decoded map[string]json.RawMessage
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatalf("output is not valid JSON: %s", out)
	}
	if _, ok := decoded["ethernet 2/1"]; !ok {
		t.Error("an unmappable key must pass through unchanged, not be dropped")
	}
	if _, ok := decoded["GE5"]; !ok {
		t.Error("the mappable key must still be renamed")
	}
}

func TestRemapPortNamesIfStale_EmptyIsNoOp(t *testing.T) {
	var namer = fsS3900Namer(t)
	var out, changed = remapPortNamesIfStale(namer, nil)
	if changed || len(out) != 0 {
		t.Errorf("empty ports must be a no-op, got out=%s changed=%v", out, changed)
	}
}

// facadeNamingStale must catch a facade left behind by a PRIOR read: on such
// a device, devices.ports has already been renamed (so remapPortNamesIfStale
// finds nothing left to do), but port_groups can still carry the connector's
// old prefix — the exact production sequence observed on 2026-08-02.
func TestFacadeNamingStale_DetectsOldPrefixEvenAfterPortsAlreadyRenamed(t *testing.T) {
	var namer = fsS3900Namer(t)
	var old = json.RawMessage(
		`[{"prefix":"ethernet 1/","from":1,"count":24},{"prefix":"ethernet 1/","from":25,"count":4}]`)
	if !facadeNamingStale(namer, old) {
		t.Fatal("a facade still using the raw platform prefix must be reported stale")
	}

	var current = json.RawMessage(
		`[{"prefix":"GE","from":1,"count":24,"silk":"1/"},{"prefix":"GE","from":25,"count":4,"silk":"1/"}]`)
	if facadeNamingStale(namer, current) {
		t.Fatal("a facade already using the standard prefix must not be reported stale")
	}
}

func TestFacadeNamingStale_EmptyIsNotStale(t *testing.T) {
	// Empty is a DIFFERENT case (faceplateEmpty), not staleness — conflating
	// them would make an adopted-with-no-facade device look "stale" instead of
	// "never read".
	if facadeNamingStale(fsS3900Namer(t), nil) {
		t.Error("empty port_groups must not be reported as stale")
	}
}

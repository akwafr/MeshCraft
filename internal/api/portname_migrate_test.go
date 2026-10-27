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
	var _, ok = decoded["GE1"]
	if !ok {
		t.Error("GE1 missing after remap")
	}
	_, ok = decoded["GE25"]
	if !ok {
		t.Error("GE25 missing after remap")
	}
	_, ok = decoded["ethernet 1/1"]
	if ok {
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
	var _, ok = decoded["ethernet 2/1"]
	if !ok {
		t.Error("an unmappable key must pass through unchanged, not be dropped")
	}
	_, ok = decoded["GE5"]
	if !ok {
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

// faceplateDiffers is the pure decision behind the facade half of the
// read-state catch-up: persist the freshly read faceplate only when it says
// something the stored one does not. The shapes below are the production case
// that motivated it (observed 2026-08-05): an EdgeSwitch adopted before its
// connector split copper from SFP+ cages stored ONE uniform "GE" group — a
// facade under perfectly current names, structurally stale, on which the
// prefix-based staleness probe this replaces had nothing to say. Same story
// on the FS S3400, whose "XG" group gained its "SFP+" label after adoption.
func TestFaceplateDiffers_UniformGroupVsCopperSFPSplit(t *testing.T) {
	// devices.port_groups as adopted (unifimoule1, 192.0.2.4).
	var stored = json.RawMessage(`[{"from": 1, "silk": "0/", "count": 18, "prefix": "GE"}]`)
	// What the connector's ReadFaceplate answers today.
	var fresh = json.RawMessage(`[` +
		`{"prefix":"GE","from":1,"count":16,"silk":"0/","poe":true,"row":0},` +
		`{"prefix":"GE","from":17,"count":2,"silk":"0/","label":"SFP+","speed":"10G","poe":false,"row":0}]`)
	if !faceplateDiffers(stored, fresh) {
		t.Fatal("a pre-split uniform facade must differ from the connector's current copper+SFP shape")
	}
}

func TestFaceplateDiffers_SameContentDifferentEncodingIsEqual(t *testing.T) {
	// jsonb re-orders keys and strips whitespace: byte comparison would call
	// every stored facade stale forever, rewriting the row on each read.
	var stored = json.RawMessage(`[{"from": 1, "count": 48, "silk": "g0/", "prefix": "GE"}]`)
	var fresh = json.RawMessage(`[{"prefix":"GE","from":1,"count":48,"silk":"g0/"}]`)
	if faceplateDiffers(stored, fresh) {
		t.Fatal("identical content under a different key order/whitespace must not differ")
	}
}

func TestFaceplateDiffers_EmptyOrUnreadableStored(t *testing.T) {
	var fresh = json.RawMessage(`[{"prefix":"GE","from":1,"count":8}]`)
	if !faceplateDiffers(nil, fresh) {
		t.Error("an empty stored facade must accept any fresh read")
	}
	if !faceplateDiffers(json.RawMessage(`not json`), fresh) {
		t.Error("an unreadable stored facade must yield to a good fresh read")
	}
	if faceplateDiffers(json.RawMessage(`[{"prefix":"GE","count":8}]`), json.RawMessage(`not json`)) {
		t.Error("an unreadable FRESH read must never be reported as a difference to write")
	}
}

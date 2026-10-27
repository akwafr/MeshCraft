package api

import (
	"encoding/json"
	"maps"
	"testing"

	"meshcraft/internal/driver"
)

// The four invariants of the model refresh, exactly as identityHealDecision's
// comment states them. Each case here is a promise the read-state pipeline
// makes to the inventory; none may move without moving the comment too.

func TestIdentityHealEmptyStoredGetsFilled(t *testing.T) {
	var h = identityHealDecision("", "S3900-24T4S")
	if !h.Update || h.Model != "S3900-24T4S" {
		t.Fatalf("empty stored model must be filled, got %+v", h)
	}
	if h.Event != healEventFilled {
		t.Fatalf("filling a blank must leave a history line, got event %q", h.Event)
	}
}

func TestIdentityHealSameModelIsSilent(t *testing.T) {
	var h = identityHealDecision("CSS326-24G-2S+", "CSS326-24G-2S+")
	if h.Update || h.Event != "" {
		t.Fatalf("identical observation must be a silent no-op, got %+v", h)
	}
}

func TestIdentityHealChangedModelUpdatesAndTells(t *testing.T) {
	var h = identityHealDecision("WS-C2960X-24PS-L", "WS-C2960X-48FPD-L")
	if !h.Update || h.Model != "WS-C2960X-48FPD-L" {
		t.Fatalf("a hardware swap must update the model, got %+v", h)
	}
	if h.Event != healEventChanged {
		t.Fatalf("a hardware swap must be visible in history, got event %q", h.Event)
	}
}

func TestIdentityHealEmptyObservationKeepsStored(t *testing.T) {
	var h = identityHealDecision("CRS326-24G-2S+", "")
	if h.Update || h.Event != "" {
		t.Fatalf("an empty observation must never erase a stored model, got %+v", h)
	}
}

// Whitespace is presentation, not identity: a model that only differs by
// padding must not produce a write, and a padded observation filling a blank
// must land trimmed.
func TestIdentityHealTrimsBeforeComparing(t *testing.T) {
	var h = identityHealDecision("S3900-24T4S", "  S3900-24T4S ")
	if h.Update {
		t.Fatalf("padding alone must not count as a change, got %+v", h)
	}
	h = identityHealDecision("  ", " S3900-24T4S ")
	if !h.Update || h.Model != "S3900-24T4S" || h.Event != healEventFilled {
		t.Fatalf("blank-ish stored model must fill with the trimmed observation, got %+v", h)
	}
}

// --- extra.identity round trip ---

// identityFacts drops empties so identical knowledge compares equal — that is
// what keeps the common case (nothing changed) write-free.
func TestIdentityFactsDropEmptiesAndRoundTrip(t *testing.T) {
	var facts = identityFacts(driver.Identity{
		Model:    " S3900-24T4S ",
		Serial:   "FS12345678",
		Firmware: "1.7.7",
		Source:   "ssh_cli",
		// MAC, ModelCode, Build empty: must not appear at all.
	})
	var want = map[string]string{
		"model": "S3900-24T4S", "serial": "FS12345678",
		"firmware": "1.7.7", "source": "ssh_cli",
	}
	if !maps.Equal(facts, want) {
		t.Fatalf("facts = %v, want %v", facts, want)
	}

	// What was written must read back equal through storedIdentityFacts —
	// otherwise every read would look like new knowledge and write a row.
	var raw, err = json.Marshal(map[string]any{"identity": facts, "driver": "fs-s3900"})
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(storedIdentityFacts(raw), facts) {
		t.Fatalf("round trip lost facts: %v", storedIdentityFacts(raw))
	}
}

func TestStoredIdentityFactsToleratesGarbage(t *testing.T) {
	var cases = []json.RawMessage{nil, []byte(``), []byte(`{}`), []byte(`null`),
		[]byte(`not json`), []byte(`{"identity": "not an object"}`)}
	var raw json.RawMessage
	for _, raw = range cases {
		var got = storedIdentityFacts(raw)
		if got == nil || len(got) != 0 {
			t.Fatalf("extra %q must yield an empty map, got %v", raw, got)
		}
	}
}

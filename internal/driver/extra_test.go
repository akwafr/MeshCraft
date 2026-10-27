package driver_test

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/driver"
)

func TestDriverNameFromExtraWithNestedObjects(t *testing.T) {
	var raw = json.RawMessage(`{
		"driver":"aruba-instant-on-1930",
		"stp_observed":{"mode":"rstp","enabled":true},
		"discovered_id":"5fd5dd4c-d22a-4c0f-b473-05b0e1509983"
	}`)
	var got = driver.DriverNameFromExtra(raw)
	if got != "aruba-instant-on-1930" {
		t.Fatalf("driver=%q", got)
	}
	got = driver.ExtraString(raw, "discovered_id")
	if got != "5fd5dd4c-d22a-4c0f-b473-05b0e1509983" {
		t.Fatalf("discovered_id=%q", got)
	}
	// Old fragile decode: map[string]string rejects nested objects.
	var fragile map[string]string
	if json.Unmarshal(raw, &fragile) == nil {
		t.Fatal("expected map[string]string unmarshal to fail on nested stp_observed")
	}
}

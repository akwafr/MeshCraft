package agentv1

import (
	"encoding/json"
	"testing"
)

// TestWireFieldNamesAreStable pins the JSON field names the proxy and the
// controller exchange. They are the actual wire — a rename here silently
// breaks every fielded proxy, which has no session to renegotiate — so the
// test states them explicitly rather than trusting the struct tags to stay.
func TestWireFieldNamesAreStable(t *testing.T) {
	var cases = []struct {
		name string
		v    any
		want []string
	}{
		{"EnrollRequest", EnrollRequest{}, []string{"token", "name"}},
		{"EnrollResponse", EnrollResponse{Status: "x"}, []string{"status", "proxy_id", "secret", "controller_url"}},
		{"HeartbeatRequest", HeartbeatRequest{Protocol: 1}, []string{"proxy_id", "secret", "version", "protocol", "os", "arch"}},
		{"MigrateOrder", MigrateOrder{}, []string{"controller_url", "enroll_token"}},
		{"UpgradeOffer", UpgradeOffer{}, []string{"version", "url", "sha256", "size"}},
		{"MigratedRequest", MigratedRequest{}, []string{"proxy_id", "secret", "new_controller"}},
	}
	var c struct {
		name string
		v    any
		want []string
	}
	for _, c = range cases {
		var raw, err = json.Marshal(c.v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", c.name, err)
		}
		var m map[string]json.RawMessage
		if err = json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", c.name, err)
		}
		var field string
		for _, field = range c.want {
			var _, ok = m[field]
			if !ok {
				t.Errorf("%s: wire field %q missing (got %s)", c.name, field, raw)
			}
		}
	}
}

// TestMigrateAndUpgradeOmitWhenAbsent: HeartbeatResponse carries at most one
// order, and a nil pointer must not serialize as a null the proxy would have
// to guard. omitempty is the contract; this proves it holds.
func TestMigrateAndUpgradeOmitWhenAbsent(t *testing.T) {
	var raw, _ = json.Marshal(HeartbeatResponse{Status: "ok"})
	var m map[string]json.RawMessage
	var uerr = json.Unmarshal(raw, &m)
	if uerr != nil {
		t.Fatal(uerr)
	}
	var _, hasMigrate = m["migrate"]
	if hasMigrate {
		t.Error("migrate present on a plain ok heartbeat")
	}
	var _, hasUpgrade = m["upgrade"]
	if hasUpgrade {
		t.Error("upgrade present on a plain ok heartbeat")
	}
}

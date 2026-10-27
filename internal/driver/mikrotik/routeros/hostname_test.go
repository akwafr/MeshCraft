package routeros

import (
	"strings"
	"testing"
)

func TestSetHostnameRefusesBeforeTouchingTheDevice(t *testing.T) {
	// The refusal must happen with no transport at all — hence a Client with no
	// web session and a nil context: if any of these reached the wire, this test
	// would panic instead of passing. That is the property being pinned, not the
	// message: validating after connecting would spend a session to learn
	// something a pure function knows.
	var c = &Client{host: "192.0.2.54"}
	var bad = []string{"", "-sw1", "sw1-", "sw 1", "sw_1", "sw1\nreboot", "eq.uip",
		strings.Repeat("a", 64)}
	var name string
	for _, name = range bad {
		var err = c.SetHostname(nil, name) //nolint:staticcheck // nil ctx: nothing must reach the wire
		if err == nil {
			t.Errorf("name %q must be refused", name)
			continue
		}
		if !strings.Contains(err.Error(), "refused") {
			t.Errorf("name %q: refusal must say so, got %v", name, err)
		}
	}
}

func TestIdentityReadKeyMatchesTheCapture(t *testing.T) {
	// The verification reads the `name` field of /system/identity. The capture is
	// the authority on that key: if the fixture ever changed shape, this write
	// would report a silent failure on a device that obeyed.
	var row = fixtureOne(t, "system_identity.json")
	if row.Get("name") != "lan-r4" {
		t.Errorf("identity name = %q, want lan-r4 — the field this write verifies "+
			"against moved", row.Get("name"))
	}
}

func TestHostnameMismatchNamesTheThreeFaults(t *testing.T) {
	var absent = hostnameMismatch("sw-lab-01", "")
	if !strings.Contains(absent, "no name at all") {
		t.Errorf("an absent name must be reported as such, got %q", absent)
	}
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very-")
	if !strings.Contains(cut, "15 bytes") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	var other = hostnameMismatch("sw-lab-01", "lan-r4")
	if !strings.Contains(other, `"lan-r4"`) {
		t.Errorf("a divergence must show what the device carries, got %q", other)
	}
}

package instanton

import "testing"

// The same hardware is named by two grammars, and both must yield the same
// faceplate: the sysDescr / login-page form counts with a "p" ("48p Gigabit
// 4p SFP+ 10G 1930"), the provisioning API's product name counts without it
// ("Aruba Instant On 1930 48G 4SFP/SFP+ Switch"). The second form went
// unparsed until 2026-08: zero SFP detected, and the numberOfPorts fallback
// adopted a JL685A as 52 copper ports — switch profile rules aimed at "sfp"
// media then missed the uplinks and player-port profiles landed on the SFP+
// cages instead.
func TestParsePortsBothLabelGrammars(t *testing.T) {
	var cases = []struct {
		name   string
		label  string
		copper int
		sfp    int
		plus   bool
		poe    bool
	}{
		{
			name:   "sysDescr HPE form (JL685A)",
			label:  "HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 10G 1930 JL685A",
			copper: 48, sfp: 4, plus: true,
		},
		{
			name:   "Aruba product name (provisioning API, JL685A)",
			label:  "Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
			copper: 48, sfp: 4, plus: true,
		},
		{
			name:   "Aruba product name, PoE model",
			label:  "Aruba Instant On 1930 48G Class4 PoE 4SFP/SFP+ 370W Switch",
			copper: 48, sfp: 4, plus: true, poe: true,
		},
		{
			name:   "Aruba product name, plain SFP model",
			label:  "Aruba Instant On 1930 8G 2SFP Switch",
			copper: 8, sfp: 2, plus: false,
		},
	}
	var c struct {
		name   string
		label  string
		copper int
		sfp    int
		plus   bool
		poe    bool
	}
	for _, c = range cases {
		var id Identity
		parsePorts(&id, c.label)
		if id.CopperPorts != c.copper || id.SFPPorts != c.sfp ||
			id.SFPPlus != c.plus || id.PoE != c.poe {
			t.Errorf("%s: got copper=%d sfp=%d plus=%v poe=%v, want copper=%d sfp=%d plus=%v poe=%v",
				c.name, id.CopperPorts, id.SFPPorts, id.SFPPlus, id.PoE,
				c.copper, c.sfp, c.plus, c.poe)
		}
	}
	// The faceplate the wrongly-adopted devices must now produce: 48 copper
	// from 1, four 10G SFP+ cages numbered 49–52.
	var id Identity
	parsePorts(&id, "Aruba Instant On 1930 48G 4SFP/SFP+ Switch")
	var groups = portGroups(id)
	if len(groups) != 2 {
		t.Fatalf("portGroups: got %d groups, want 2 (copper + SFP+)", len(groups))
	}
	if groups[1]["from"] != 49 || groups[1]["count"] != 4 ||
		groups[1]["speed"] != "10G" || groups[1]["label"] != "SFP+" {
		t.Errorf("SFP+ group: got %v", groups[1])
	}
}

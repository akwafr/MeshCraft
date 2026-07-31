package recon

import (
	"encoding/json"
	"testing"
	"time"
)

func ports(raw string) map[string]PortIntent { return ParsePorts(json.RawMessage(raw)) }

func TestParsePortsIgnoresTelemetry(t *testing.T) {
	var p = ports(`{"GE1":{"mode":"access","pvid":20,"tagged":[],
	                "link":"1G","rx_mbps":98,"poe_w":6.4,"label":"Console"}}`)
	if p["GE1"].Mode != "access" || p["GE1"].PVID != 20 {
		t.Fatalf("configuration misread: %+v", p["GE1"])
	}
	if len(p["GE1"].Tagged) != 0 || p["GE1"].TaggedAll {
		t.Errorf("no tagged VLAN expected: %+v", p["GE1"])
	}
}

func TestParsePortsTaggedAll(t *testing.T) {
	var p = ports(`{"GE5":{"mode":"trunk","pvid":10,"tagged":"all"}}`)
	if !p["GE5"].TaggedAll {
		t.Errorf("all must be recognized: %+v", p["GE5"])
	}
}

func TestDiffPortsIgnoresUnmanagedPorts(t *testing.T) {
	var desired = ports(`{"GE1":{"mode":"libre"}}`)
	var observed = ports(`{"GE1":{"mode":"access","pvid":99}}`)
	var c = DiffPorts(desired, observed)
	if len(c) != 0 {
		t.Errorf("an unmanaged port must not produce a discrepancy: %+v", c)
	}
}

func TestDiffPortsDetectsDiscrepancies(t *testing.T) {
	var desired = ports(`{"GE1":{"mode":"access","pvid":20,"tagged":[]},
	                      "GE2":{"mode":"trunk","pvid":1,"tagged":[10,20]}}`)
	var observed = ports(`{"GE1":{"mode":"access","pvid":30,"tagged":[]},
	                       "GE2":{"mode":"trunk","pvid":1,"tagged":[10]}}`)
	var c = DiffPorts(desired, observed)
	if len(c) != 2 {
		t.Fatalf("2 discrepancies expected, got %d: %+v", len(c), c)
	}
	if c[0].Label() != "port.GE1.pvid: 20 → 30" {
		t.Errorf("unexpected label: %q", c[0].Label())
	}
	if c[1].Field != "tagged vlans" || c[1].Desired != "10,20" || c[1].Observed != "10" {
		t.Errorf("VLAN discrepancy misdescribed: %+v", c[1])
	}
}

func TestDiffPortsIdentical(t *testing.T) {
	var same = `{"GE1":{"mode":"access","pvid":20,"tagged":[]},
	             "GE2":{"mode":"trunk","pvid":1,"tagged":[20,10]}}`
	var observed = ports(`{"GE1":{"mode":"access","pvid":20},
	                       "GE2":{"mode":"trunk","pvid":1,"tagged":[10,20]}}`)
	var c = DiffPorts(ports(same), observed)
	if len(c) != 0 {
		t.Errorf("no discrepancy expected: %+v", c)
	}
}

func TestResolveTaggedAll(t *testing.T) {
	var p = ResolveTaggedAll(
		PortIntent{Mode: "trunk", PVID: 10, TaggedAll: true},
		[]int{30, 10, 20, 30})
	if len(p.Tagged) != 2 || p.Tagged[0] != 20 || p.Tagged[1] != 30 {
		t.Errorf("all must enumerate site VLANs without native or duplicate: %+v", p.Tagged)
	}
	if !p.TaggedAll {
		t.Error("all remains a floor after resolution")
	}
}

func TestResolveTaggedAllSiteWithoutVLAN(t *testing.T) {
	var p = ResolveTaggedAll(PortIntent{Mode: "trunk", PVID: 10, TaggedAll: true}, nil)
	if len(p.Tagged) != 0 || !p.TaggedAll {
		t.Errorf("without a site plan, all stays unresolved: %+v", p)
	}
}

func TestDiffPortsTrunkAllIsExactSiteSet(t *testing.T) {
	var site = []int{10, 20, 30}
	var desired = Resolve(ports(`{"GE5":{"mode":"trunk","pvid":10,"tagged":"all"}}`), site)

	var exact = DiffPorts(desired, ports(`{"GE5":{"mode":"trunk","pvid":10,"tagged":[20,30]}}`))
	if len(exact) != 0 {
		t.Errorf("all trunk served by every site VLAN is conformant: %+v", exact)
	}
	var extra = DiffPorts(desired, ports(`{"GE5":{"mode":"trunk","pvid":10,"tagged":[20,30,99]}}`))
	if len(extra) != 1 || extra[0].Field != "tagged vlans" {
		t.Errorf("extra VLAN beyond the site plan is drift under all: %+v", extra)
	}
	var short = DiffPorts(desired, ports(`{"GE5":{"mode":"trunk","pvid":10,"tagged":[20]}}`))
	if len(short) != 1 || short[0].Field != "tagged vlans" {
		t.Errorf("missing site VLAN is a discrepancy: %+v", short)
	}
}

func TestDiffPortsTrunkAllUnresolved(t *testing.T) {
	var desired = ports(`{"GE5":{"mode":"trunk","pvid":10,"tagged":"all"}}`)
	var c = DiffPorts(desired, ports(`{"GE5":{"mode":"trunk","pvid":10,"tagged":[]}}`))
	if len(c) != 0 {
		t.Errorf("unresolved all claims nothing: %+v", c)
	}
}

func TestRequestedExtras(t *testing.T) {
	var got = RequestedExtras([]byte(`{
		"GE1": {"mode": "access", "pvid": 10, "poe": true},
		"GE2": {"mode": "access", "pvid": 10, "poe": false},
		"GE3": {"mode": "libre", "poe": true},
		"GE4": {"mode": "trunk", "pvid": 1}
	}`))
	if len(got["poe"]) != 1 || got["poe"][0] != "GE1" {
		t.Errorf("only positive PoE on a managed port counts: %+v", got["poe"])
	}
	if len(got) != 1 {
		t.Errorf("no other intent requested here: %+v", got)
	}
}

func TestDiffPortsPortAbsent(t *testing.T) {
	var c = DiffPorts(ports(`{"GE9":{"mode":"access","pvid":20}}`), ports(`{}`))
	if len(c) != 1 || c[0].Field != "presence" {
		t.Errorf("managed port absent from device is a discrepancy: %+v", c)
	}
}

func TestState(t *testing.T) {
	var t0 = time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	var t1 = t0.Add(time.Hour)
	var t2 = t1.Add(time.Hour)
	var change = []Change{{Port: "GE1", Field: "pvid", Desired: "20", Observed: "30"}}

	var cases = []struct {
		name           string
		observed       *time.Time
		desiredChanged *time.Time
		applied        *time.Time
		changes        []Change
		want           Sync
	}{
		{"never re-read", nil, &t0, &t0, change, SyncUnknown},
		{"conformant", &t2, &t0, &t1, nil, SyncOK},
		{"change not pushed", &t2, &t1, &t0, change, SyncPending},
		{"never applied", &t2, &t1, nil, change, SyncPending},
		{"never applied nor edited", &t2, nil, nil, change, SyncPending},
		{"drift", &t2, &t0, &t1, change, SyncDrift},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = State(c.observed, c.desiredChanged, c.applied, c.changes)
			if got != c.want {
				t.Errorf("State = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParsePortsTrunkWithoutPVID(t *testing.T) {
	var got = ParsePorts([]byte(`{
		"GE1": {"mode":"trunk","tagged":[10,20]},
		"GE2": {"mode":"trunk","pvid":30,"tagged":[10]},
		"GE3": {"mode":"libre"}
	}`))
	if got["GE1"].PVID != DefaultVLAN {
		t.Fatalf("trunk without pvid: want %d, got %d", DefaultVLAN, got["GE1"].PVID)
	}
	if got["GE2"].PVID != 30 {
		t.Fatalf("explicit native overwritten: %d", got["GE2"].PVID)
	}
	if got["GE3"].PVID != 0 {
		t.Fatalf("free port given a native: %d", got["GE3"].PVID)
	}
}

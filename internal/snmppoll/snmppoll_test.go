package snmppoll

import (
	"context"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
)

func pdu(name string, t gosnmp.Asn1BER, v any) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: name, Type: t, Value: v}
}

// TestSamplesAssocieAuBonPort: associating a counter with the wrong port produces a
// credible AND false graph, worse than a gap. Index-by-ifIndex is therefore what to verify
// first.
func TestSamplesAssocieAuBonPort(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		oidIfDescr: {
			"1": pdu(oidIfDescr+".1", gosnmp.OctetString, []byte("GE1")),
			"2": pdu(oidIfDescr+".2", gosnmp.OctetString, []byte("GE2")),
		},
		oidIfOperStatus: {
			"1": pdu(oidIfOperStatus+".1", gosnmp.Integer, 1),
			"2": pdu(oidIfOperStatus+".2", gosnmp.Integer, 2),
		},
	}
	var got = Samples("dev-1", tables, time.Now(), 0)
	var byPort = map[string]float64{}
	var s int
	for s = range got {
		if got[s].Name == "meshcraft_if_oper_status" {
			byPort[got[s].Labels["port"]] = got[s].Value
		}
	}
	if byPort["GE1"] != 1 || byPort["GE2"] != 2 {
		t.Errorf("state wrongly associated to ports: %+v", byPort)
	}
	// Device must be labeled, otherwise two switches mix.
	for s = range got {
		if got[s].Labels["device"] != "dev-1" {
			t.Fatalf("sample without device: %+v", got[s])
		}
	}
}

// TestSamplesIgnoreLesValeursNonNumeriques: a zero counter reads as an inactive link.
// Converting unreadable values to zero would be a quiet lie -- absence is better.
func TestSamplesIgnoreLesValeursNonNumeriques(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		oidIfHCInOctets: {
			"1": pdu(oidIfHCInOctets+".1", gosnmp.OctetString, []byte("boom")),
			"2": pdu(oidIfHCInOctets+".2", gosnmp.Counter64, uint64(4096)),
		},
	}
	var got = Samples("dev-1", tables, time.Now(), 0)
	if len(got) != 1 || got[0].Value != 4096 {
		t.Errorf("only readable value should produce a sample: %+v", got)
	}
}

// TestSuffixIndex: interface index is the last OID segment.
func TestSuffixIndex(t *testing.T) {
	if suffixIndex("1.3.6.1.2.1.2.2.1.8.42") != "42" {
		t.Error("interface index wrongly extracted")
	}
	if suffixIndex("42") != "42" {
		t.Error("OID without dot should pass through")
	}
}

// TestParseIntervalRespecteLePlancher: going below floor degrades device control plane
// before metric. Too-fast request is clamped, not refused -- refusing would drop supervision
// for a typo.
func TestParseIntervalRespecteLePlancher(t *testing.T) {
	var got = ParseInterval(time.Second, 5*time.Second)
	if got != 5*time.Second {
		t.Errorf("floor not respected: %v", got)
	}
	got = ParseInterval(time.Minute, 5*time.Second)
	if got != time.Minute {
		t.Errorf("slower cadence should be kept: %v", got)
	}
	got = ParseInterval(0, 0)
	if got != 30*time.Second {
		t.Errorf("expected prudent default: %v", got)
	}
}

// fakeClient plays a device, to verify poller without network.
type fakeClient struct {
	tables map[string]map[string]gosnmp.SnmpPDU
}

func (f *fakeClient) BulkWalk(_ context.Context, oid string) (map[string]gosnmp.SnmpPDU, error) {
	var t, ok = f.tables[oid]
	if !ok {
		return nil, errNoTable
	}
	return t, nil
}
func (f *fakeClient) Close() error { return nil }

// TestPollToleUneTableAbsente: not every device exposes ifXTable. Return what we read
// rather than throw everything away.
func TestPollToleUneTableAbsente(t *testing.T) {
	var f = &fakeClient{tables: map[string]map[string]gosnmp.SnmpPDU{
		oidIfOperStatus: {"1": pdu(oidIfOperStatus+".1", gosnmp.Integer, 1)},
	}}
	var p = &Poller{
		Target: Target{DeviceID: "dev-1", IP: "10.0.0.1"},
		Dial:   func(Target) (Client, error) { return f, nil },
	}
	var got, err = p.Poll(context.Background(), MetricIfState)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(got) != 1 || got[0].Name != "meshcraft_if_oper_status" {
		t.Errorf("readable state should come up: %+v", got)
	}
}

// TestPollEchoueQuandRienNEstLisible: a totally mute device is an error, not an empty batch.
// Empty batch would look like successful collection.
func TestPollEchoueQuandRienNEstLisible(t *testing.T) {
	var p = &Poller{
		Target: Target{DeviceID: "dev-1"},
		Dial: func(Target) (Client, error) {
			return &fakeClient{tables: map[string]map[string]gosnmp.SnmpPDU{}}, nil
		},
	}
	var _, err = p.Poll(context.Background(), MetricIfState)
	if err == nil {
		t.Error("mute device must produce an error")
	}
}

// TestSamplesEcarteLesPseudoInterfaces: `ifTable` is not only physical ports -- aggregates,
// stack port, VLAN interfaces. On a real 1930, indexes at 8000, 9000 and 100000, one named
// "1" like port 1.
//
// Test focuses on INDEX not label, otherwise two series for same port name: exactly the
// credible false graph we avoid.
func TestSamplesEcarteLesPseudoInterfaces(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		oidIfDescr: {
			"1":      pdu(oidIfDescr+".1", gosnmp.OctetString, []byte("1")),
			"100000": pdu(oidIfDescr+".100000", gosnmp.OctetString, []byte("1")),
			"9000":   pdu(oidIfDescr+".9000", gosnmp.OctetString, []byte("stack-port")),
		},
		oidIfOperStatus: {
			"1":      pdu(oidIfOperStatus+".1", gosnmp.Integer, 1),
			"100000": pdu(oidIfOperStatus+".100000", gosnmp.Integer, 1),
			"9000":   pdu(oidIfOperStatus+".9000", gosnmp.Integer, 1),
		},
	}
	var got = Samples("dev-1", tables, time.Now(), 52)
	if len(got) != 1 || got[0].Labels["ifindex"] != "1" {
		t.Errorf("only physical port should produce a series: %+v", got)
	}
	// Without bound, filter nothing: connector that did not declare facade must not lose
	// supervision.
	if len(Samples("dev-1", tables, time.Now(), 0)) != 3 {
		t.Error("without bound, everything should pass")
	}
}

// TestConventionsDeConnexionUniques locks the UNIQUE source of truth for project SNMP
// connection conventions.
//
// These values are written only here now. Proxy inherits via snmppoll.Dial, and its own
// test verifies it has no others (cmd/proxy: TestAskDialHeriteDesConventionsDuPoll,
// TestAskWalkSurLeFilPorteLesConventionsDuPoll). Changing them is fine; changing them on
// ONE SIDE ONLY is not -- diagnostic walk and ordinary poll would describe two different
// devices with no signal. This test fails on any change, forcing intent.
func TestConventionsDeConnexionUniques(t *testing.T) {
	var g = conventions(Target{IP: "10.99.200.101"})
	if g.Target != "10.99.200.101" {
		t.Errorf("target %q", g.Target)
	}
	if g.Version != gosnmp.Version2c {
		t.Errorf("expected v2c, got %v", g.Version)
	}
	if g.Timeout != 2*time.Second {
		t.Errorf("expected 2 s timeout, got %v", g.Timeout)
	}
	if g.Retries != 1 {
		t.Errorf("expected one retry, got %d", g.Retries)
	}
	if g.MaxOids != 60 {
		t.Errorf("expected MaxOids 60, got %d", g.MaxOids)
	}
	// Default port and community: fallback is easiest to rewrite elsewhere, so anchor it.
	if g.Port != 161 {
		t.Errorf("expected default port 161, got %d", g.Port)
	}
	if DefaultCommunity != "public" {
		t.Errorf("fleet fallback is public, got %q", DefaultCommunity)
	}
	if g.Community != DefaultCommunity {
		t.Errorf("expected fallback community, got %q", g.Community)
	}

	// What target specifies is never overwritten.
	g = conventions(Target{IP: "10.0.0.1", Port: 1161, Community: "labo"})
	if g.Port != 1161 || g.Community != "labo" {
		t.Errorf("target port/community lost: %d %q", g.Port, g.Community)
	}

	// v3 will come with service identity: until then, target requesting it still speaks v2c.
	// Trying v3 without identity would fail cold on the whole fleet.
	g = conventions(Target{IP: "10.0.0.1", Version: "3"})
	if g.Version != gosnmp.Version2c {
		t.Errorf("expected v2c until v3 has identity, got %v", g.Version)
	}
}

// pduInt builds an integer value as the device returns it.
func pduInt(v int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: v}
}

func pduStr(s string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte(s)}
}

// TestSamplesEcarteLesInterfacesVirtuelles replays the REAL table of an Instant On 1930,
// collisions included.
//
// This is the test that matters: VLAN 42 interface is labeled "42", exactly like physical
// port 42. Keeping both would attribute VLAN traffic to a port -- credible false graph,
// worse than a gap.
func TestSamplesEcarteLesInterfacesVirtuelles(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		oidIfDescr: {
			"42":     pduStr("42"),   // physical port 42
			"1000":   pduStr("TRK1"), // aggregate
			"3000":   pduStr("tunnel1"),
			"100041": pduStr("42"), // VLAN 42 interface -- SAME label
		},
		oidIfType: {
			"42":     pduInt(IfTypeEthernet),
			"1000":   pduInt(161), // ieee8023adLag
			"3000":   pduInt(131), // tunnel
			"100041": pduInt(135), // l2vlan
		},
		oidIfOperStatus: {
			"42": pduInt(1), "1000": pduInt(1), "3000": pduInt(1), "100041": pduInt(1),
		},
	}
	var got = Samples("d1", tables, time.Now(), 0)
	if len(got) != 1 {
		t.Fatalf("only physical port should produce a sample, got %d: %+v", len(got), got)
	}
	if got[0].Labels["ifindex"] != "42" || got[0].Labels["port"] != "42" {
		t.Errorf("wrong interface kept: %+v", got[0].Labels)
	}
	if got[0].Labels["iftype"] != "6" {
		t.Errorf("type must accompany sample: %+v", got[0].Labels)
	}
}

// TestSamplesSansTableIfType: fall back to index bound when device does not return `ifType`.
// Old heuristic beats nothing.
func TestSamplesSansTableIfType(t *testing.T) {
	var tables = map[string]map[string]gosnmp.SnmpPDU{
		oidIfDescr:      {"2": pduStr("2"), "100041": pduStr("42")},
		oidIfOperStatus: {"2": pduInt(1), "100041": pduInt(1)},
	}
	var got = Samples("d1", tables, time.Now(), 52)
	if len(got) != 1 || got[0].Labels["ifindex"] != "2" {
		t.Fatalf("index bound should take over: %+v", got)
	}
	var _, seen = got[0].Labels["iftype"]
	if seen {
		t.Error("without ifType table, label must not be invented")
	}
}

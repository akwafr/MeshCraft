package snmppoll

import (
	"testing"

	"github.com/gosnmp/gosnmp"
)

// LLDP neighbor tests replay July 29, 2026 survey value for value
// (docs/en/01-Architecture/LLDP neighbours.md). Not decoration: each rule tested exists because
// a fleet device returned something that required it; invented values would catch none of this
// MIB's three traps.

// octets builds OctetString PDU as gosnmp returns it.
func octets(b ...byte) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: b}
}

func text(s string) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte(s)}
}

func integer(n int) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: n}
}

// TestRemTableIndexDecoding is THE test of the suite: this index attaches neighbor to local
// port; getting it wrong draws credible false link.
func TestRemTableIndexDecoding(t *testing.T) {
	var cas = []struct {
		name   string
		suffix string
		mark   uint32
		port   int
		index  int
		ok     bool
	}{
		{
			// SURVEY case, only form both reference platforms returned: timestamp zero because
			// ordinary table walk sees synthetic TimeFilter (RFC 2021).
			name:   "survey CRS326: timeMark 0, local port 26, neighbor 8",
			suffix: "0.26.8", mark: 0, port: 26, index: 8, ok: true,
		},
		{
			name:   "survey 1930: local port 51, neighbor 11",
			suffix: "0.51.11", mark: 0, port: 51, index: 11, ok: true,
		},
		{
			// THE TRAP. Two arcs is what read taking last arc as index and previous as port
			// produces -- forgot timestamp. Refuse is only correct answer: accept puts all
			// fleet neighbors on port 0.
			name:   "only two arcs: REJECTED, timestamp missing",
			suffix: "26.8", ok: false,
		},
		{
			name:   "four arcs: REJECTED, not this table",
			suffix: "0.26.8.1", ok: false,
		},
		{
			// Non-zero timestamp is LEGAL: MIB allows multiple snapshots of same row. No reference
			// device does, but decoder must read it -- assembly keeps newest instead of composite neighbor.
			name:   "non-zero timestamp: accepted, used to decide",
			suffix: "4294967295.2.7", mark: 4294967295, port: 2, index: 7, ok: true,
		},
		{
			// `lldpLocPortNum` is LldpPortNumber, MIB domain 1..4096. Zero is exactly two-arc decode error.
			name:   "local port 0: REJECTED, outside LldpPortNumber domain",
			suffix: "0.0.8", ok: false,
		},
		{
			name:   "non-numeric arc: REJECTED",
			suffix: "0.ether3.8", ok: false,
		},
		{
			name:   "timestamp beyond 32 bits: REJECTED, not TimeTicks",
			suffix: "4294967296.2.7", ok: false,
		},
		{name: "empty: REJECTED", suffix: "", ok: false},
	}
	var i int
	for i = range cas {
		var c = cas[i]
		t.Run(c.name, func(t *testing.T) {
			var mark, key, ok = decodeRemIndex(c.suffix)
			if ok != c.ok {
				t.Fatalf("acceptance: %v, expected %v", ok, c.ok)
			}
			if !c.ok {
				return
			}
			if mark != c.mark {
				t.Errorf("timestamp %d, expected %d", mark, c.mark)
			}
			if key.LocalPortNum != c.port {
				t.Errorf("local port %d, expected %d", key.LocalPortNum, c.port)
			}
			if key.RemIndex != c.index {
				t.Errorf("remote index %d, expected %d", key.RemIndex, c.index)
			}
		})
	}
}

// TestLocalPortName exercises choice between `lldpLocPortId` and `lldpLocPortDesc`.
//
// First two cases are both fleet devices requiring OPPOSITE columns. Whole reason function
// exists: neither column alone suffices, mistake silently drops port neighbors.
func TestLocalPortName(t *testing.T) {
	var cas = []struct {
		name       string
		subtype    int
		hasSubtype bool
		portID     string
		portDesc   string
		want       string
	}{
		{
			// CRS326 port 26. `portDesc` has bridge prefix not on facade: using it drops neighbor at resolve.
			name:    "CRS326: interfaceName(5) -- portId wins over bridge/ prefix",
			subtype: 5, hasSubtype: true,
			portID: "sfp-sfpplus2", portDesc: "bridge/sfp-sfpplus2",
			want: "sfp-sfpplus2",
		},
		{
			// 1930 port 50 -- EXACTLY the port with a neighbor. Its `portId` is port MAC as prefixed hex.
			name:    "1930: macAddress(3) -- portDesc wins over MAC",
			subtype: 3, hasSubtype: true,
			portID: "0x02005e1061d9", portDesc: "50",
			want: "50",
		},
		{
			name:    "1930: interfaceName(5) on ordinary port",
			subtype: 5, hasSubtype: true, portID: "45", portDesc: "45", want: "45",
		},
		{
			// Missing subtype: judge VALUE. "0x..." is OctetText for non-printable bytes.
			name:     "missing subtype, hex portId: fallback to portDesc",
			portID:   "0x02005e1061d9",
			portDesc: "50",
			want:     "50",
		},
		{
			name:   "missing subtype, readable portId: keep it",
			portID: "ether3",
			want:   "ether3",
		},
		{
			name:    "non-naming subtype, empty portDesc: return portId not nothing",
			subtype: 3, hasSubtype: true, portID: "0xdeadbeef", want: "0xdeadbeef",
		},
		{
			name:    "interfaceName(5) but empty portId: fallback to portDesc",
			subtype: 5, hasSubtype: true, portDesc: "GE12", want: "GE12",
		},
		{name: "nothing at all", want: ""},
	}
	var i int
	for i = range cas {
		var c = cas[i]
		t.Run(c.name, func(t *testing.T) {
			var got = localPortName(c.subtype, c.hasSubtype, c.portID, c.portDesc)
			if got != c.want {
				t.Errorf("name %q, expected %q", got, c.want)
			}
		})
	}
}

// walkCRS326 replays July 29, 2026 survey on lab-crs326 (192.0.2.54, RouterOS 7.21beta3) as
// the walk returned it.
//
// Carries ONLY columns this agent actually serves. `lldpRemPortDesc` (column 8) is ABSENT, and
// that survey property must stay: test fails if decoder depends on it.
func walkCRS326() map[string]gosnmp.SnmpPDU {
	return map[string]gosnmp.SnmpPDU{
		// Local identity. Chassis-id rendered as TEXT by this agent -- seventeen printable bytes
		// -- where 1930 renders six raw bytes for same chassis.
		"1.0.8802.1.1.2.1.3.2.0": text("B8:69:F4:5B:A0:D2"),
		"1.0.8802.1.1.2.1.3.3.0": text("lan-r4"),
		// lldpLocPortTable: TWO entries for twenty-nine interfaces. LLDP runs on two only.
		"1.0.8802.1.1.2.1.3.7.1.2.26": integer(5),
		"1.0.8802.1.1.2.1.3.7.1.2.28": integer(5),
		"1.0.8802.1.1.2.1.3.7.1.3.26": text("sfp-sfpplus2"),
		"1.0.8802.1.1.2.1.3.7.1.3.28": text("vlan99"),
		"1.0.8802.1.1.2.1.3.7.1.4.26": text("bridge/sfp-sfpplus2"),
		"1.0.8802.1.1.2.1.3.7.1.4.28": text("vlan99"),
		// lldpRemTable: five neighbors, THREE on same local port 26.
		"1.0.8802.1.1.2.1.4.1.1.5.0.26.8":  text("04:F4:1C:1D:74:5F"),
		"1.0.8802.1.1.2.1.4.1.1.5.0.26.9":  text("D4:01:C3:AB:32:F6"),
		"1.0.8802.1.1.2.1.4.1.1.5.0.26.11": text("02:00:5e:10:61:D9"),
		"1.0.8802.1.1.2.1.4.1.1.5.0.28.12": text("D4:01:C3:AB:32:F4"),
		"1.0.8802.1.1.2.1.4.1.1.5.0.28.13": text("B8:69:F4:5F:14:A8"),
		"1.0.8802.1.1.2.1.4.1.1.6.0.26.8":  integer(5),
		"1.0.8802.1.1.2.1.4.1.1.6.0.26.9":  integer(5),
		"1.0.8802.1.1.2.1.4.1.1.6.0.26.11": integer(5),
		"1.0.8802.1.1.2.1.4.1.1.7.0.26.8":  text("bridge/sfp-sfpplus1"),
		"1.0.8802.1.1.2.1.4.1.1.7.0.26.9":  text("bridge/sfp-sfpplus2"),
		"1.0.8802.1.1.2.1.4.1.1.7.0.26.11": text("50"),
		"1.0.8802.1.1.2.1.4.1.1.7.0.28.12": text("vlan99"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.26.8":  text("RB5009UG"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.26.9":  text("CRS305"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.26.11": text("CN0EXAMPL1"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.28.12": text("CRS305"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.28.13": text("lan-m6"),
		"1.0.8802.1.1.2.1.4.1.1.10.0.26.11": text("HPE Networking Instant On Switch " +
			"48p Gigabit 4p SFP+ 1930 JL685A, InstantOn_1930_3.4.0.0 (6)"),
	}
}

func TestClicheDuCRS326(t *testing.T) {
	var report = LLDPReportFrom("crs", walkCRS326())

	if !report.Served {
		t.Fatal("this agent serves LLDP-MIB: Served must be true")
	}
	if report.SysName != "lan-r4" {
		t.Errorf("sysName %q, expected lan-r4 -- NOT meshcraft device name, which is why it is collected",
			report.SysName)
	}
	if report.ChassisID != "B8:69:F4:5B:A0:D2" {
		t.Errorf("chassisID %q: value must pass through unchanged, normalization is controller decision",
			report.ChassisID)
	}
	if len(report.Neighbors) != 5 {
		t.Fatalf("%d neighbors, expected 5", len(report.Neighbors))
	}

	// Design fact this survey establishes: THREE neighbors on ONE physical port. LLDP neighbor is
	// not proof of direct cable -- switch that does not consume LLDP floods frames.
	var onPort26 int
	var i int
	for i = range report.Neighbors {
		if report.Neighbors[i].LocalPort == "sfp-sfpplus2" {
			onPort26++
		}
	}
	if onPort26 != 3 {
		t.Errorf("%d neighbors on sfp-sfpplus2, expected 3: measured case, structure must carry it",
			onPort26)
	}

	// Neighbor attachment that matters: 1930, seen on local port 26.
	var found bool
	for i = range report.Neighbors {
		var n = report.Neighbors[i]
		if n.SysName != "CN0EXAMPL1" {
			continue
		}
		found = true
		if n.LocalPort != "sfp-sfpplus2" {
			t.Errorf("local port %q, expected sfp-sfpplus2 (via lldpLocPortTable, never index arithmetic)",
				n.LocalPort)
		}
		if n.LocalPortNum != 26 {
			t.Errorf("lldpLocPortNum %d, expected 26", n.LocalPortNum)
		}
		if n.PortID != "50" {
			t.Errorf("remote port %q, expected 50", n.PortID)
		}
		if n.PortDesc != "" {
			t.Errorf("portDesc %q: this agent does NOT serve column 8, decoder must not invent it",
				n.PortDesc)
		}
		if n.ChassisID != "02:00:5e:10:61:D9" {
			t.Errorf("neighbor chassis %q", n.ChassisID)
		}
	}
	if !found {
		t.Error("1930 neighbor (CN0EXAMPL1) not found")
	}

	// Index-13 neighbor has no portId or subtype in survey: must still exist with what we know.
	// Decoder requiring all columns would drop neighbors the device did see.
	var partial bool
	for i = range report.Neighbors {
		if report.Neighbors[i].SysName == "lan-m6" {
			partial = true
			if report.Neighbors[i].LocalPort != "vlan99" {
				t.Errorf("lan-m6 local port: %q", report.Neighbors[i].LocalPort)
			}
		}
	}
	if !partial {
		t.Error("lan-m6 neighbor with empty portId was lost")
	}
}

// walk1930 replays survey on sw-labo-1930 (198.51.100.101, InstantOn 3.4.0.0).
//
// Two ESSENTIAL properties opposite CRS326: chassis-id arrives as RAW bytes, and ports 50 and 52
// announce `lldpLocPortId` as `macAddress(3)`.
func walk1930() map[string]gosnmp.SnmpPDU {
	return map[string]gosnmp.SnmpPDU{
		"1.0.8802.1.1.2.1.3.2.0": octets(0x02, 0x00, 0x5e, 0x10, 0x61, 0xa7),
		"1.0.8802.1.1.2.1.3.3.0": text("CN0EXAMPL1"),
		// Three ports only, keep test readable: 50 and 52 macAddress(3), 51 interfaceName(5).
		"1.0.8802.1.1.2.1.3.7.1.2.50": integer(3),
		"1.0.8802.1.1.2.1.3.7.1.2.51": integer(5),
		"1.0.8802.1.1.2.1.3.7.1.2.52": integer(3),
		"1.0.8802.1.1.2.1.3.7.1.3.50": octets(0x02, 0x00, 0x5e, 0x10, 0x61, 0xd9),
		"1.0.8802.1.1.2.1.3.7.1.3.51": text("51"),
		"1.0.8802.1.1.2.1.3.7.1.3.52": octets(0x02, 0x00, 0x5e, 0x10, 0x61, 0xdb),
		"1.0.8802.1.1.2.1.3.7.1.4.50": text("50"),
		"1.0.8802.1.1.2.1.3.7.1.4.51": text("51"),
		"1.0.8802.1.1.2.1.3.7.1.4.52": text("52"),
		// Three neighbors, one per port.
		"1.0.8802.1.1.2.1.4.1.1.5.0.50.6":  octets(0xb8, 0x69, 0xf4, 0x5b, 0xa0, 0xd2),
		"1.0.8802.1.1.2.1.4.1.1.5.0.51.11": octets(0x64, 0x9d, 0x99, 0x16, 0x34, 0x10),
		"1.0.8802.1.1.2.1.4.1.1.5.0.52.1":  octets(0xd4, 0x01, 0xc3, 0xab, 0x32, 0xf4),
		"1.0.8802.1.1.2.1.4.1.1.6.0.50.6":  integer(5),
		"1.0.8802.1.1.2.1.4.1.1.6.0.51.11": integer(5),
		"1.0.8802.1.1.2.1.4.1.1.6.0.52.1":  integer(5),
		"1.0.8802.1.1.2.1.4.1.1.7.0.50.6":  text("sfp-sfpplus2"),
		"1.0.8802.1.1.2.1.4.1.1.7.0.51.11": text("Eth 1/28"),
		"1.0.8802.1.1.2.1.4.1.1.7.0.52.1":  text("sfp-sfpplus2"),
		"1.0.8802.1.1.2.1.4.1.1.8.0.50.6":  text("bridge/sfp-sfpplus2"),
		"1.0.8802.1.1.2.1.4.1.1.8.0.51.11": text("Ethernet Port on unit 1, port 28"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.50.6":  text("lan-r4"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.51.11": text("DATAWORDS-S3900-NOUVEAUX-1"),
		"1.0.8802.1.1.2.1.4.1.1.9.0.52.1":  text("CRS305"),
		"1.0.8802.1.1.2.1.4.1.1.10.0.50.6": text("MikroTik RouterOS 7.21beta3 " +
			"(testing) 2025-10-14 12:50:07 CRS326-24G-2S+"),
	}
}

func TestClicheDu1930(t *testing.T) {
	var report = LLDPReportFrom("1930", walk1930())

	// Chassis-id from RAW bytes: must emerge as prefixed hex, MUST NOT become U+FFFD.
	if report.ChassisID != "0x02005e1061a7" {
		t.Errorf("chassisID %q, expected 0x02005e1061a7: six raw bytes lossless", report.ChassisID)
	}
	// Field saving fleet bilateral link: port 50 and 52 MAC must appear in PortIDs, because CRS326
	// recorded neighbor under THAT value.
	var sawD9 bool
	var i int
	for i = range report.PortIDs {
		if report.PortIDs[i] == "0x02005e1061d9" {
			sawD9 = true
		}
	}
	if !sawD9 {
		t.Errorf("PortIDs %v missing 0x02005e1061d9: without it controller cannot recognize neighbor "+
			"CRS326 recorded under that value, bilateral link looks unilateral", report.PortIDs)
	}

	if len(report.Neighbors) != 3 {
		t.Fatalf("%d neighbors, expected 3", len(report.Neighbors))
	}
	// Port 50 neighbor is CRS326, local port must be 50 -- from `lldpLocPortDesc` since portId is MAC.
	var found bool
	for i = range report.Neighbors {
		var n = report.Neighbors[i]
		if n.SysName != "lan-r4" {
			continue
		}
		found = true
		if n.LocalPort != "50" {
			t.Errorf("local port %q, expected 50: this port portId is MAC, portDesc names it",
				n.LocalPort)
		}
		if n.PortID != "sfp-sfpplus2" {
			t.Errorf("remote port %q", n.PortID)
		}
		if n.PortDesc != "bridge/sfp-sfpplus2" {
			t.Errorf("remote portDesc %q: this agent serves column 8, it must come up", n.PortDesc)
		}
		if n.ChassisID != "0xb869f45ba0d2" {
			t.Errorf("neighbor chassis %q, expected 0xb869f45ba0d2 -- SAME chassis CRS326 announces as text, hence controller normalization",
				n.ChassisID)
		}
	}
	if !found {
		t.Error("CRS326 neighbor (lan-r4) not found")
	}

	// ORDER must be stable: controller builds inventory diff run to run, unstable order rewrites table every cycle.
	var again = LLDPReportFrom("1930", walk1930())
	for i = range report.Neighbors {
		if report.Neighbors[i] != again.Neighbors[i] {
			t.Fatalf("neighbor order not stable at position %d", i)
		}
	}
}

// TestSwOSServesNothingAtAll: CSS326 returns zero lines under `1.0.8802`, WITHOUT error.
//
// Connector has always declared this, and snapshot must differ from "zero neighbors": not serving
// MIB means we know NOTHING, zero neighbors is an observation. Confusing them shows "nothing plugged"
// on switch with all ports occupied.
func TestSwOSServesNothingAtAll(t *testing.T) {
	var report = LLDPReportFrom("css", map[string]gosnmp.SnmpPDU{})
	if report.Served {
		t.Error("empty walk must NOT pass for agent serving MIB")
	}
	if len(report.Neighbors) != 0 {
		t.Errorf("%d neighbors for empty walk", len(report.Neighbors))
	}

	// Inverse: agent serving ONLY local identity speaks 802.1AB, simply nobody opposite. Nuance must cross.
	var alone = LLDPReportFrom("crs", map[string]gosnmp.SnmpPDU{
		"1.0.8802.1.1.2.1.3.3.0": text("lan-r4"),
	})
	if !alone.Served {
		t.Error("agent serving lldpLocSysName speaks LLDP: Served must be true")
	}
	if len(alone.Neighbors) != 0 {
		t.Error("no neighbor must be invented")
	}
}

// TestHorodatagesMultiples: agent may return multiple snapshots of one row. None in reference fleet,
// but decoder must keep most RECENT without mixing observations -- composite neighbor would be credible false link.
func TestHorodatagesMultiples(t *testing.T) {
	var report = LLDPReportFrom("dev", map[string]gosnmp.SnmpPDU{
		"1.0.8802.1.1.2.1.3.7.1.2.4": integer(5),
		"1.0.8802.1.1.2.1.3.7.1.3.4": text("ether4"),
		// Same (port 4, neighbor 1) at two timestamps, two DIFFERENT neighbors.
		"1.0.8802.1.1.2.1.4.1.1.9.100.4.1": text("old-neighbour"),
		"1.0.8802.1.1.2.1.4.1.1.7.100.4.1": text("ether9"),
		"1.0.8802.1.1.2.1.4.1.1.9.900.4.1": text("new-neighbour"),
		"1.0.8802.1.1.2.1.4.1.1.7.900.4.1": text("ether1"),
	})
	if len(report.Neighbors) != 1 {
		t.Fatalf("%d neighbors: two timestamps of SAME row make one neighbor", len(report.Neighbors))
	}
	if report.Neighbors[0].SysName != "new-neighbour" {
		t.Errorf("sysName %q, expected newest", report.Neighbors[0].SysName)
	}
	if report.Neighbors[0].PortID != "ether1" {
		t.Errorf("portID %q: row must come ENTIRELY from newest observation, never mixed",
			report.Neighbors[0].PortID)
	}
}

// TestOctetTextFidelity locks lossless conversion migrated from cmd/proxy/rpc.go for single implementation.
//
// Stakes are not cosmetic: `json.Marshal` silently replaces non-UTF-8 with U+FFFD, transformed
// chassis-id irreconcilable with same chassis seen elsewhere -- with no error.
func TestOctetTextFidelity(t *testing.T) {
	var cas = []struct {
		name  string
		entry []byte
		want  string
	}{
		{"raw MAC: prefixed hex",
			[]byte{0xb8, 0x69, 0xf4, 0x5b, 0xa0, 0xd2}, "0xb869f45ba0d2"},
		{"printable text: as-is", []byte("B8:69:F4:5B:A0:D2"), "B8:69:F4:5B:A0:D2"},
		{"multiline sysDescr: newline stays text",
			[]byte("HPE 1930\nJL685A"), "HPE 1930\nJL685A"},
		{"single non-UTF-8 byte switches to hex",
			[]byte{'p', 'o', 'r', 't', 0x80}, "0x706f727480"},
		{"empty", nil, ""},
	}
	var i int
	for i = range cas {
		var got = OctetText(cas[i].entry)
		if got != cas[i].want {
			t.Errorf("%s: %q, expected %q", cas[i].name, got, cas[i].want)
		}
	}
}

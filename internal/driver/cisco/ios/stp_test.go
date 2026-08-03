package ios

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// VERBATIM from the lab 2960-X (WS-C2960X-24TD-L, IOS 15.2(6)E) on 2026-08-02,
// read over its serial console. This is the switch whose spanning tree started
// the whole investigation: per-VLAN sections, itself as root, while the fleet's
// real root sat at priority 4096.
const showSpanningTreeLive = `
VLAN0001
  Spanning tree enabled protocol rstp
  Root ID    Priority    32769
             Address     bcc4.93e5.7f80
             This bridge is the root
             Hello Time   2 sec  Max Age 20 sec  Forward Delay 15 sec

  Bridge ID  Priority    32769  (priority 32768 sys-id-ext 1)
             Address     bcc4.93e5.7f80
             Aging Time  300 sec

Interface           Role Sts Cost      Prio.Nbr Type
------------------- ---- --- --------- -------- ----
Te1/0/2             Desg FWD 2         128.28   P2p

VLAN0099
  Spanning tree enabled protocol rstp
  Root ID    Priority    32867
             Address     bcc4.93e5.7f80
             This bridge is the root

  Bridge ID  Priority    32867  (priority 32768 sys-id-ext 99)
             Address     bcc4.93e5.7f80

Interface           Role Sts Cost      Prio.Nbr Type
------------------- ---- --- --------- -------- ----
Te1/0/2             Desg FWD 2         128.28   P2p
`

// THE DIAGNOSIS THIS MUST MAKE. Per-VLAN sections mean Rapid PVST+, whatever the
// word after "protocol" says — and a switch running it does not interoperate with
// the MSTP and single-instance RSTP the rest of the fleet speaks.
func TestParseDetectsRapidPVSTFromTheSections(t *testing.T) {
	var st = parseShowSpanningTree(showSpanningTreeLive)
	if st.Mode != "rstp" {
		t.Fatalf("mode = %q, want rstp", st.Mode)
	}
	var notes = strings.Join(st.Notes, " ")
	if !strings.Contains(notes, "PVST+") {
		t.Errorf("notes = %q: reading « protocol rstp » alone would report plain RSTP "+
			"for a switch that is not interoperating at all", notes)
	}
	if !st.Enabled {
		t.Error("spanning tree reported disabled on a switch that is running it")
	}
}

func TestParseReadsTheBridgeAndRootFacts(t *testing.T) {
	var st = parseShowSpanningTree(showSpanningTreeLive)
	if st.BridgePriority != 32769 {
		t.Errorf("bridge priority = %d, want 32769", st.BridgePriority)
	}
	if st.BridgeID != "bcc4.93e5.7f80" {
		t.Errorf("bridge id = %q", st.BridgeID)
	}
	// It claimed to be root — which was the anomaly worth surfacing, since the
	// fleet's real root was at 4096.
	if !st.IsRoot {
		t.Error("the device claimed to be root and the parse lost it")
	}
	if len(st.Ports) != 1 {
		t.Fatalf("ports = %d, want 1: the same interface repeats per VLAN and must "+
			"be reported once", len(st.Ports))
	}
	if st.Ports[0].Role != driver.STPRoleDesignated ||
		st.Ports[0].State != driver.STPStateForwarding {
		t.Errorf("port = %+v, want designated/forwarding", st.Ports[0])
	}
	if st.Ports[0].PathCost != 2 {
		t.Errorf("path cost = %d, want 2", st.Ports[0].PathCost)
	}
}

// Under mst the same command prints MST sections, and the per-VLAN warning must
// NOT fire.
func TestParseRecognisesMST(t *testing.T) {
	var out = `
MST0
  Spanning tree enabled protocol mstp
  Root ID    Priority    4096
             Address     04f4.1c1d.7458
             Cost        2000

  Bridge ID  Priority    32768  (priority 32768 sys-id-ext 0)
             Address     bcc4.93e5.7f80

Interface           Role Sts Cost      Prio.Nbr Type
------------------- ---- --- --------- -------- ----
Te1/0/2             Root FWD 2000      128.28   P2p
`
	var st = parseShowSpanningTree(out)
	if st.Mode != "mstp" {
		t.Fatalf("mode = %q, want mstp", st.Mode)
	}
	if st.IsRoot {
		t.Error("reported as root while a better root is present")
	}
	if st.RootPriority != 4096 {
		t.Errorf("root priority = %d, want 4096", st.RootPriority)
	}
	if strings.Contains(strings.Join(st.Notes, " "), "PVST+") {
		t.Error("an mst switch was warned about PVST+")
	}
	if st.Ports[0].Role != driver.STPRoleRoot {
		t.Errorf("port role = %q, want root", st.Ports[0].Role)
	}
}

func TestParseMSTRegion(t *testing.T) {
	var r = parseMSTRegion("\nname akwa-casernelab\nrevision 1\ninstance 1 vlan 42, 98-100\n")
	if r.Name != "akwa-casernelab" || r.Revision != 1 {
		t.Errorf("region = %+v", r)
	}
	if !r.Configured() {
		t.Error("a named region did not count as configured")
	}
}

// The priority step is the protocol's, not a preference: IOS refuses anything
// that is not a multiple of 4096.
func TestBridgePriorityIsClampedToTheProtocolStep(t *testing.T) {
	var cases = [][2]int{{0, 0}, {4096, 4096}, {5000, 4096}, {61440, 61440}, {99999, 61440}, {-5, 0}}
	var c [2]int
	for _, c = range cases {
		var got = clampBridgePriority(c[0])
		if got != c[1] {
			t.Errorf("clamp(%d) = %d, want %d", c[0], got, c[1])
		}
		if got%4096 != 0 {
			t.Errorf("clamp(%d) = %d is not a multiple of 4096; IOS refuses it", c[0], got)
		}
	}
}

func TestSTPModeKeywords(t *testing.T) {
	var m, ok = iosSTPMode("mstp")
	if !ok || m != "mst" {
		t.Errorf("mstp -> %q,%v", m, ok)
	}
	// IOS calls single-instance-looking RSTP "rapid-pvst", and it is per-VLAN.
	m, _ = iosSTPMode("rstp")
	if m != "rapid-pvst" {
		t.Errorf("rstp -> %q", m)
	}
	if _, ok = iosSTPMode("nonsense"); ok {
		t.Error("an unknown mode was accepted")
	}
}

func TestRegionNameIsMadeSafeForTheCLI(t *testing.T) {
	var got = sanitizeRegionName("akwa/CaserneLab lab")
	if strings.ContainsAny(got, " /") {
		t.Errorf("region name %q still carries characters IOS refuses", got)
	}
	got = sanitizeRegionName("!!!")
	if got != "meshcraft" {
		t.Errorf("a name with nothing usable = %q, want a safe default", got)
	}
	if len(sanitizeRegionName(strings.Repeat("a", 60))) != 32 {
		t.Error("region name not capped at 32 characters")
	}
}

// The connector must now DECLARE stp writable, or the apply path never runs.
func TestDriverDeclaresSTPWritable(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("stp") {
		t.Error("stp is implemented and not declared: ApplySpanningTree would never be called")
	}
	if d.WritesExtra("poe") || d.WritesExtra("port_security") {
		t.Error("a capability is declared that nothing writes")
	}
}

// Facts must not leak between per-VLAN sections. Observed live on 2026-08-02:
// the parse reported root d401.c3ab.32f4 AND "this bridge is the root", which
// cannot both be true — the root address came from one section and the flag from
// another.
func TestBridgeFactsComeFromASingleSection(t *testing.T) {
	var out = `
VLAN0001
  Spanning tree enabled protocol rstp
  Root ID    Priority    4096
             Address     d401.c3ab.32f4
             Cost        2000

  Bridge ID  Priority    32769  (priority 32768 sys-id-ext 1)
             Address     bcc4.93e5.7f80

VLAN0099
  Spanning tree enabled protocol rstp
  Root ID    Priority    32867
             Address     bcc4.93e5.7f80
             This bridge is the root

  Bridge ID  Priority    32867  (priority 32768 sys-id-ext 99)
             Address     bcc4.93e5.7f80
`
	var st = parseShowSpanningTree(out)
	if st.RootBridgeID != "d401.c3ab.32f4" {
		t.Errorf("root = %q, want the first section's", st.RootBridgeID)
	}
	if st.IsRoot {
		t.Error("IsRoot was taken from a LATER section than the root address: the " +
			"device would be reported as root of a tree it is not root of")
	}
	if st.RootPriority != 4096 {
		t.Errorf("root priority = %d, want 4096", st.RootPriority)
	}
}

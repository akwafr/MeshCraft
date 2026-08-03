package s3900

// Apply logic is proven on states, not on hardware (house style — cf.
// routeros/fixtures_test.go: "apply LOGIC on these real states, not the
// device's reaction").

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

func TestMissingVLANs(t *testing.T) {
	var have = []VLAN{{ID: 1, Name: "DefaultVlan"}, {ID: 99, Name: "LAN99_Management"}}
	var site = []VLAN{{ID: 99, Name: "LAN99_Management"}, {ID: 100, Name: "Servers"}}
	var desired = map[string]PortState{
		"GE5":  {Mode: "access", PVID: 10},
		"GE28": {Mode: "trunk", PVID: 1, Tagged: []int{99, 100, 200}},
	}
	var missing = missingVLANs(have, site, 99, desired)
	if len(missing) != 3 {
		t.Fatalf("missing=%v", missing)
	}
	if missing[0].ID != 10 || missing[1].ID != 100 || missing[2].ID != 200 {
		t.Errorf("order=%v", missing)
	}
	if missing[1].Name != "Servers" {
		t.Errorf("the site plan's name must follow: %v", missing[1])
	}
}

func TestVlanCreateCmdsUseTheVerifiedForm(t *testing.T) {
	// A bare "vlan N" replies "% Incomplete command" (lab capture): the name
	// and "media ethernet" are required.
	var cmds = vlanCreateCmds([]VLAN{{ID: 100, Name: "Servers"}, {ID: 200}}, true)
	var want = []string{
		"vlan database",
		"vlan 100 name Servers media ethernet",
		"vlan 200 name VLAN0200 media ethernet",
		"exit",
	}
	if len(cmds) != len(want) {
		t.Fatalf("cmds=%v", cmds)
	}
	var i int
	for i = range want {
		if cmds[i] != want[i] {
			t.Errorf("[%d] %q != %q", i, cmds[i], want[i])
		}
	}
}

func TestTrunkCmdsVerifiedForm(t *testing.T) {
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99, 100}}
	var now = PortState{Mode: "access", PVID: 1}
	var groups = portCmdCandidates("GE28", want, now)
	if len(groups) != 1 {
		t.Fatalf("groups=%v", groups)
	}
	var cmds = groups[0].alternatives[0]
	var joined = strings.Join(cmds, " ; ")
	// portCmdCandidates enters the interface using the CLI form (cliPortName
	// translation), not the standard-model name it was called with.
	if !strings.Contains(joined, "interface ethernet 1/28") ||
		!strings.Contains(joined, "switchport mode trunk") ||
		!strings.Contains(joined, "switchport trunk allowed vlan add 99,100") {
		t.Errorf("cmds=%v", cmds)
	}
}

func TestAccessCmdsCarryAFallback(t *testing.T) {
	// The access form was never typed on 1.7.0: two spellings of the same
	// family, ordered — never a silent success if both are refused.
	var groups = portCmdCandidates("GE5",
		PortState{Mode: "access", PVID: 10}, PortState{Mode: "access", PVID: 1})
	if len(groups) != 1 || len(groups[0].alternatives) != 2 {
		t.Fatalf("groups=%v", groups)
	}
	if !strings.Contains(strings.Join(groups[0].alternatives[0], " "), "switchport access vlan 10") {
		t.Errorf("form 1=%v", groups[0].alternatives[0])
	}
	if !strings.Contains(strings.Join(groups[0].alternatives[1], " "), "switchport allowed vlan add 10 untagged") {
		t.Errorf("form 2=%v", groups[0].alternatives[1])
	}
}

func TestStaleTaggedRemovalSparesTheNativeVLAN(t *testing.T) {
	var want = PortState{Mode: "trunk", PVID: 99, Tagged: []int{100}}
	var now = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99, 100, 200}}
	var groups = portCmdCandidates("GE25", want, now)
	var all = ""
	var g cmdGroup
	for _, g = range groups {
		var alt []string
		for _, alt = range g.alternatives {
			all += strings.Join(alt, " ; ") + "\n"
		}
	}
	if !strings.Contains(all, "remove 200") {
		t.Errorf("the stale VLAN 200 must be removed:\n%s", all)
	}
	if strings.Contains(all, "remove 99") || strings.Contains(all, "remove 99,200") {
		t.Errorf("the wanted native VLAN (99) must not be removed from its own port:\n%s", all)
	}
}

func TestPortConverged(t *testing.T) {
	var now = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99, 100}}
	if !portConverged(PortState{Mode: "trunk", PVID: 1, Tagged: []int{99, 100}}, now) {
		t.Error("identical => converged")
	}
	if portConverged(PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}}, now) {
		t.Error("different list => needs a write (removal expected)")
	}
	if !portConverged(PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}, TaggedAtLeast: true}, now) {
		t.Error("TaggedAtLeast: a present superset is enough")
	}
	if portConverged(PortState{Mode: "access", PVID: 1}, now) {
		t.Error("different mode => needs a write")
	}
}

func TestUnmappedPortNameIsSkippedNotGuessed(t *testing.T) {
	// A name outside the GEn scheme (e.g. surviving from a stale intent, or a
	// caller mistake) must never reach the CLI as a literal "interface ".
	if cliPortName("stack2-port5") != "" {
		t.Fatal("an out-of-scheme name must translate to \"\"")
	}
}

// --- contract refusals, no hardware required ---

func TestAuthenticateRefusesEmptyCandidateList(t *testing.T) {
	var d = &Driver{}
	var adm, err = d.Authenticate(t.Context(), driver.Target{Host: "192.0.2.1"}, nil)
	if err == nil {
		t.Fatal("empty candidate list was accepted")
	}
	if adm == nil || len(adm.Transcript()) == 0 {
		t.Error("access must be returned even on failure, so the transcript survives")
	}
}

func TestSetSecretRefusesUnprovenAccount(t *testing.T) {
	var c = &Client{}
	var err = c.SetSecret(t.Context(), "old", "new")
	if err == nil {
		t.Fatal("secret change accepted without a proven account")
	}
}

func TestSNMPv3IsRefusedNotApproximated(t *testing.T) {
	var c = &Client{dialect: dialectFSOS17}
	var _, err = c.ApplySNMP(t.Context(), SNMPIdentity{User: "mc", AuthAlg: "sha256"})
	if err == nil {
		t.Fatal("a v3 identity must be refused, never approximated as v2c")
	}
}

func TestWritesAreRefusedOnUnknownDialect(t *testing.T) {
	// Upgraded firmware whose CLI is not recognized: reads stay best-effort,
	// writes are refused with the way forward.
	var c = &Client{dialect: dialectUnknown, cli: &cliConn{}}
	var _, err = c.ApplyPorts(t.Context(), PortPlan{MgmtVLAN: 99})
	if err == nil {
		t.Fatal("write accepted on an unknown dialect")
	}
	if !strings.Contains(err.Error(), "fs-s3900") {
		t.Errorf("the refusal must name the connector: %v", err)
	}
}

func TestMonitoringDeclarationIsCoherent(t *testing.T) {
	var caps = (&Driver{}).Capabilities("S3900-24T4S")
	if len(caps.Monitoring.Contradictions()) != 0 {
		t.Fatalf("contradictory declaration: %v", caps.Monitoring.Contradictions())
	}
	var _, ok = caps.Monitoring.BestOfKind("snmp", "lldp")
	if !ok {
		t.Error("the snmp lldp channel must be declared, or the proxy never collects it")
	}
	if _, ok = caps.Monitoring.BestOfKind("snmp", "if_counters"); !ok {
		t.Error("if_counters must be served by an snmp channel")
	}
	// health IS declared, and only because sysUpTime (1.3.6.1.2.1.1.3) was
	// verified to answer on this platform (walk of 2026-08-02). What that
	// channel yields here is uptime and nothing else: HOST-RESOURCES-MIB is not
	// served, so CPU and memory stay empty.
	var ch, hasHealth = caps.Monitoring.BestOfKind("snmp", "health")
	if !hasHealth {
		t.Error("health must be declared: sysUpTime answers on this platform")
	}
	if !strings.Contains(ch.Note, "UPTIME ONLY") {
		t.Errorf("the health channel must say what it actually yields here: %q", ch.Note)
	}
	// optical_power stays declared ABSENT: the poller's OIDs for it are
	// MikroTik-proprietary and this platform is enterprise 52642.
	if _, ok = caps.Monitoring.BestOfKind("snmp", "optical_power"); ok {
		t.Error("optical_power must NOT be declared: the poller's OIDs are MikroTik-proprietary")
	}
	var why, declared = caps.Monitoring.WhyUnavailable("optical_power")
	if !declared || !why.Observed || why.Source == "" {
		t.Errorf("the absence of optical_power must be declared WITH its evidence: %+v", why)
	}
}

func TestReadFaceplateShape(t *testing.T) {
	// The faceplate read must carry the shape the contract expects
	// (prefix/count/from/speed/silk/label) to be persisted without translation.
	// Tests the pure parser.
	var groups = portGroupsOf(parseInterfacesBrief(labBrief))
	if len(groups) == 0 {
		t.Fatal("no groups")
	}
	var g = groups[len(groups)-1]
	var _, hasPrefix = g["prefix"]
	var _, hasCount = g["count"]
	var _, hasFrom = g["from"]
	var _, hasSpeed = g["speed"]
	var _, hasSilk = g["silk"]
	if !hasPrefix || !hasCount || !hasFrom || !hasSpeed || !hasSilk {
		t.Errorf("missing faceplate keys: %v", g)
	}
}

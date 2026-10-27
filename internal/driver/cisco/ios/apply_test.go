package ios

// Apply logic is proven on states, not on hardware (house style — cf.
// routeros/fixtures_test.go: "apply LOGIC on these real states, not the
// device's reaction").

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

func TestPortGroupsSplitOnMediumNotOnNumber(t *testing.T) {
	// 24 copper then 4 cages, numerically contiguous. A faceplate that split
	// only on contiguity would draw one block of 28 and put optical cages in
	// the copper row.
	var groups = portGroupsOf(parseInterfacesStatus(labStatus))
	if len(groups) != 3 {
		t.Fatalf("groups=%v", groups)
	}
	// labStatus is an excerpt (a hole between Gi1/0/3 and Gi1/0/24): contiguity
	// must SPLIT rather than draw sockets that do not exist.
	if groups[0]["from"] != 1 || groups[0]["count"] != 3 {
		t.Errorf("copper head=%v", groups[0])
	}
	if groups[1]["from"] != 24 || groups[1]["count"] != 1 {
		t.Errorf("copper tail=%v", groups[1])
	}
	var cages = groups[2]
	if cages["from"] != 25 || cages["count"] != 4 {
		t.Errorf("cage group=%v", cages)
	}
	if cages["label"] != "SFP" {
		t.Errorf("cages must be LABELLED as cages — portrules.mediaOf reads this: %v", cages)
	}
	if groups[0]["label"] != "" {
		t.Errorf("copper must carry no cage label: %v", groups[0])
	}
	// "prefix" is the STANDARD-MODEL name the backend manipulates; "silk" is
	// what is printed on the faceplate and typed at the CLI.
	if groups[0]["prefix"] != "GE" || groups[0]["silk"] != "Gi1/0/" {
		t.Errorf("naming=%v", groups[0])
	}
	// PoE is never deduced: "show interfaces status" does not carry it, and a
	// faceplate claiming PoE the device cannot deliver plans an access point
	// onto a port that will never power it.
	if groups[0]["poe"] != false {
		t.Errorf("poe must not be guessed: %v", groups[0])
	}
}

func TestPortGroupsSplitOnPrefixCollision(t *testing.T) {
	// A 48FPD carries Gi1/0/1..48 AND Te1/0/1..2. The two "1"s are different
	// sockets; one prefix would make "GE1" mean two jacks.
	const status = `Port      Name               Status       Vlan       Duplex  Speed Type
Gi1/0/1                      notconnect   1            auto   auto 10/100/1000BaseTX
Gi1/0/2                      notconnect   1            auto   auto 10/100/1000BaseTX
Te1/0/1                      notconnect   1            full    10G unknown
Te1/0/2                      connected    trunk        full    10G SFP-10GBase-SR
`
	var groups = portGroupsOf(parseInterfacesStatus(status))
	if len(groups) != 2 {
		t.Fatalf("groups=%v", groups)
	}
	if groups[0]["prefix"] != "GE" || groups[0]["from"] != 1 || groups[0]["count"] != 2 {
		t.Errorf("copper=%v", groups[0])
	}
	if groups[1]["prefix"] != "XG" || groups[1]["from"] != 1 || groups[1]["count"] != 2 {
		t.Errorf("10G=%v", groups[1])
	}
	if groups[1]["speed"] != "10G" || groups[1]["label"] != "SFP+" {
		t.Errorf("10G cages must declare their speed and medium: %v", groups[1])
	}
	if groups[1]["silk"] != "Te1/0/" {
		t.Errorf("silk=%v", groups[1]["silk"])
	}
}

func TestVlanCreateOmitsTheDefaultName(t *testing.T) {
	// A VLAN with no name in the plan keeps the IOS default (VLAN0100), which
	// is exactly what parseVlanBrief reads back. Writing a name here would make
	// the next read disagree with the plan for ever.
	var cmds = vlanCreateCmds([]VLAN{{ID: 100, Name: "Servers"}, {ID: 200}})
	var want = []string{"vlan 100", "name Servers", "exit", "vlan 200", "exit"}
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

func TestTrunkCarriesTheEncapsulationFallback(t *testing.T) {
	var want = PortState{Mode: "trunk", PVID: 99, Tagged: []int{100, 200}}
	var now = PortState{Mode: "access", PVID: 1}
	var groups = portCmdCandidates("GE28", "GigabitEthernet1/0/28", want, now, false)
	if len(groups) != 1 || len(groups[0].alternatives) != 2 {
		t.Fatalf("groups=%v", groups)
	}
	var plain = strings.Join(groups[0].alternatives[0], " ; ")
	// The interface is entered under its FULL IOS name, not the standard-model
	// name it was called with.
	if !strings.Contains(plain, "interface GigabitEthernet1/0/28") {
		t.Errorf("enter=%v", plain)
	}
	if strings.Contains(plain, "encapsulation") {
		t.Error("form 1 must NOT send encapsulation: a 2960/2960-X refuses it")
	}
	var encap = strings.Join(groups[0].alternatives[1], " ; ")
	if !strings.Contains(encap, "switchport trunk encapsulation dot1q") {
		t.Error("form 2 must carry the encapsulation a 3560/3750 requires")
	}
	if strings.Index(encap, "encapsulation dot1q") > strings.Index(encap, "switchport mode trunk") {
		t.Error("encapsulation must come BEFORE mode trunk, or the device refuses the mode")
	}
	// The native VLAN must be in the allowed list, or IOS drops native traffic
	// on a port that looks perfectly configured.
	if !strings.Contains(plain, "switchport trunk allowed vlan 99,100,200") {
		t.Errorf("the exact list must include the native VLAN: %v", plain)
	}
}

func TestExactIntentUsesTheSetFormNotAdd(t *testing.T) {
	// On an all-allowed trunk, "add" changes nothing (everything was already
	// allowed) and the port would report drift for ever.
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}}
	var now = PortState{Mode: "trunk", PVID: 1}
	var groups = portCmdCandidates("GE3", "GigabitEthernet1/0/3", want, now, true)
	var joined = strings.Join(groups[0].alternatives[0], " ; ")
	if strings.Contains(joined, "allowed vlan add") {
		t.Errorf("an exact intent must RESTRICT, and \"add\" never restricts: %v", joined)
	}
	if !strings.Contains(joined, "switchport trunk allowed vlan 1,99") {
		t.Errorf("set form missing: %v", joined)
	}
}

func TestAtLeastIntentNeverRestricts(t *testing.T) {
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}, TaggedAtLeast: true}
	var now = PortState{Mode: "trunk", PVID: 1}
	// Everything is already allowed: there is nothing to add and nothing to do.
	var groups = portCmdCandidates("GE3", "GigabitEthernet1/0/3", want, now, true)
	var joined = strings.Join(groups[0].alternatives[0], " ; ")
	if strings.Contains(joined, "allowed vlan") {
		t.Errorf("an all-allowed trunk needs no VLAN command on an at-least intent: %v", joined)
	}
	// With an explicit list, only what is missing is added.
	groups = portCmdCandidates("GE3", "GigabitEthernet1/0/3", PortState{Mode: "trunk", PVID: 1,
		Tagged: []int{99, 100}, TaggedAtLeast: true},
		PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}}, false)
	joined = strings.Join(groups[0].alternatives[0], " ; ")
	if !strings.Contains(joined, "allowed vlan add 100") {
		t.Errorf("only the missing VLAN must be added: %v", joined)
	}
}

func TestPortConvergedUnderstandsAllowedAll(t *testing.T) {
	var now = PortState{Mode: "trunk", PVID: 1}
	// An all-allowed trunk satisfies any at-least intent...
	if !portConverged(PortState{Mode: "trunk", PVID: 1, Tagged: []int{99, 100}, TaggedAtLeast: true},
		now, true) {
		t.Error("all VLANs are allowed: an at-least intent is already satisfied")
	}
	// ...and satisfies NO exact intent, because it allows more than was asked.
	if portConverged(PortState{Mode: "trunk", PVID: 1, Tagged: []int{99}}, now, true) {
		t.Error("an exact intent on an all-allowed trunk must still be written")
	}
	// With an explicit list, the comparison includes the native VLAN.
	var explicit = PortState{Mode: "trunk", PVID: 99, Tagged: []int{99, 100}}
	if !portConverged(PortState{Mode: "trunk", PVID: 99, Tagged: []int{100}}, explicit, false) {
		t.Error("want tagged 100 + native 99 == allowed 99,100: already converged")
	}
	if portConverged(PortState{Mode: "access", PVID: 1}, now, false) {
		t.Error("different mode => needs a write")
	}
}

func TestActiveTrunkNeverSilentlyLosesTheManagementVLAN(t *testing.T) {
	// The guardrail the classic native-VLAN check cannot see: administration
	// arrives TAGGED on an uplink whose native VLAN is 1, so the PVID test
	// finds nothing to protect while the exact-set write drops VLAN 99.
	var protected = map[int]string{99: "site plan management VLAN"}
	var now = PortState{Mode: "trunk", PVID: 1, Tagged: []int{1, 99, 100}}
	var want = PortState{Mode: "trunk", PVID: 1, Tagged: []int{100}}
	var lost, why = protectedTaggedLoss(want, now, false, protected)
	if lost != 99 || why == "" {
		t.Fatalf("loss of the management VLAN not detected: %d %q", lost, why)
	}
	// Same port, all-allowed today: an exact list drops everything it does not name.
	lost, _ = protectedTaggedLoss(want, PortState{Mode: "trunk", PVID: 1}, true, protected)
	if lost != 99 {
		t.Errorf("an all-allowed trunk loses every unnamed VLAN: got %d", lost)
	}
	// Keeping it is fine.
	lost, _ = protectedTaggedLoss(PortState{Mode: "trunk", PVID: 1, Tagged: []int{99, 100}},
		now, false, protected)
	if lost != 0 {
		t.Errorf("nothing is lost here: got %d", lost)
	}
	// An at-least intent never removes anything, so it can never lose one.
	lost, _ = protectedTaggedLoss(PortState{Mode: "trunk", PVID: 1, Tagged: []int{100},
		TaggedAtLeast: true}, now, true, protected)
	if lost != 0 {
		t.Errorf("an at-least intent removes nothing: got %d", lost)
	}
	// And the native VLAN counts as kept: it is in the allowed list this
	// connector writes.
	lost, _ = protectedTaggedLoss(PortState{Mode: "trunk", PVID: 99, Tagged: []int{100}},
		now, false, protected)
	if lost != 0 {
		t.Errorf("the wanted native VLAN is part of the allowed list: got %d", lost)
	}
}

func TestMissingVLANsIgnoresTheReservedRange(t *testing.T) {
	var have = []VLAN{{ID: 1, Name: "default"}, {ID: 99, Name: "LAN99_Management"}}
	var site = []VLAN{{ID: 99, Name: "LAN99_Management"}, {ID: 100, Name: "Servers"},
		{ID: 1002, Name: "fddi-default"}}
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
	// 1002-1005 exist on every IOS, cannot be removed, and must never be
	// "created": the diff would be wrong for ever.
	var v VLAN
	for _, v = range missing {
		if reservedVLAN(v.ID) {
			t.Errorf("reserved VLAN proposed for creation: %v", v)
		}
	}
}

func TestUnmappedPortNameIsSkippedNotGuessed(t *testing.T) {
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
	var c = &Client{dialect: dialectIOS}
	var _, err = c.ApplySNMP(t.Context(), SNMPIdentity{User: "mc", AuthAlg: "sha256"})
	if err == nil {
		t.Fatal("a v3 identity must be refused, never approximated as v2c")
	}
	if !strings.Contains(err.Error(), "show snmp user") {
		t.Errorf("the refusal must say what would lift it: %v", err)
	}
}

func TestWritesAreRefusedOnUnknownDialect(t *testing.T) {
	// A lookalike CLI: many platforms answer "show version" and accept
	// "conf t" without meaning the same thing by "switchport".
	var c = &Client{dialect: dialectUnknown, cli: &cliConn{}}
	var _, err = c.ApplyPorts(t.Context(), PortPlan{MgmtVLAN: 99})
	if err == nil {
		t.Fatal("write accepted on an unknown dialect")
	}
	if !strings.Contains(err.Error(), "cisco-ios") {
		t.Errorf("the refusal must name the connector: %v", err)
	}
}

func TestMonitoringDeclarationIsCoherent(t *testing.T) {
	var caps = (&Driver{}).Capabilities("WS-C2960X-24PS-L")
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
	// CPU, memory and temperature are now COLLECTED — walked on a 2960-X
	// 2026-08-02 and served through the health channel — so they must no longer be
	// declared unavailable. Leaving a stale Unavailable("cpu") behind would tell
	// the UI a curve is impossible while the poller is drawing it.
	var _, cpuDeclared = caps.Monitoring.WhyUnavailable("cpu")
	if cpuDeclared {
		t.Error("cpu is collected from the Cisco enterprise-9 tree: it must not be " +
			"declared unavailable")
	}
	var _, memDeclared = caps.Monitoring.WhyUnavailable("memory")
	if memDeclared {
		t.Error("memory is collected: stale Unavailable declaration")
	}
	var _, health = caps.Monitoring.BestOfKind("snmp", "health")
	if !health {
		t.Error("the health channel must be declared, or cpu/memory/temperature are " +
			"collected by the poller but shown by nothing")
	}
	// OPTICAL POWER IS COLLECTED SINCE 2026-08-12, and this assertion inverted on
	// that day rather than being deleted: it used to require the absence to be
	// DECLARED, on the reason that CISCO-ENTITY-SENSOR-MIB "is not queried by the
	// poller". It is queried now — internal/snmppoll walks it and joins it to
	// ENTITY-MIB — and the capture that settled it is quoted on the channel
	// itself (TX -3.9 dBm / RX -2.7 dBm off a WS-C2960X-24TD-L).
	//
	// Both halves are asserted because either one alone would lie on screen: a
	// channel with a stale Unavailable beside it makes the device page say the
	// metric is impossible while the poller collects it.
	var _, stillDeclaredAbsent = caps.Monitoring.WhyUnavailable("optical_power")
	if stillDeclaredAbsent {
		t.Error("optical_power is collected from CISCO-ENTITY-SENSOR-MIB: a stale " +
			"Unavailable would tell an operator to stop waiting for a curve that " +
			"is already arriving")
	}
	var optical, hasOptical = caps.Monitoring.BestOfKind("snmp", "optical_power")
	if !hasOptical {
		t.Fatal("the optical channel must be declared, or the controller never " +
			"resolves a cadence and the poller is never asked for the walk")
	}
	if !strings.Contains(optical.Note, "CISCO-ENTITY-SENSOR-MIB") {
		t.Errorf("the note must name the tree the readings come from: %q", optical.Note)
	}
}

func TestResetProcedureIsMarkedUnconfirmed(t *testing.T) {
	// Nothing in this connector has been run on hardware. The UI must be able
	// to say so — a reset procedure believed verified costs access to a device.
	var rp = (&Driver{}).ResetProcedure("WS-C2960X-24PS-L")
	if rp.Confirmed {
		t.Error("no gesture here has been performed on a 2960-X: Confirmed must be false")
	}
	var joined = strings.Join(rp.Pitfalls, " | ")
	if !strings.Contains(joined, "Save?") {
		t.Error("the reload \"Save?\" trap must be stated: answering yes re-saves what was erased")
	}
	if !strings.Contains(joined, "vlan.dat") {
		t.Error("\"write erase\" leaving the VLAN database behind must be stated")
	}
}

func TestMgmtPushOrderIsLoadBearing(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.240", PrefixLen: 16, Gateway: "192.0.2.1",
		Hostname: "lab cat", Username: "mcadmin", Password: "s3cret",
		TrunkPorts: []string{"GigabitEthernet1/0/28"},
	})
	var joined = strings.Join(cmds, "\n")
	var at = func(needle string) int { return strings.Index(joined, needle) }

	if at("vlan 99") < 0 || at("interface vlan 99") < 0 {
		t.Fatalf("cmds=%v", cmds)
	}
	// Third-party optics are accepted BEFORE anything that needs a link: a
	// refused transceiver leaves a cage looking empty, so uplink discovery
	// would find nothing and the management VLAN would be carried nowhere.
	if at("service unsupported-transceiver") < 0 ||
		at("no errdisable detect cause gbic-invalid") < 0 {
		t.Errorf("provisioning must accept third-party optics: %v", cmds)
	}
	if at("service unsupported-transceiver") > at("interface GigabitEthernet1/0/28") {
		t.Error("the transceiver lines must come before the uplink is configured")
	}
	if at("vlan 99") > at("interface vlan 99") {
		t.Error("the VLAN must exist before the SVI, or the SVI never comes up")
	}
	if at("ip domain-name") > at("crypto key generate") {
		t.Error("\"crypto key generate rsa\" is refused until a domain name is set")
	}
	if at("no shutdown") < 0 {
		t.Error("an SVI created in configuration mode comes up SHUTDOWN")
	}
	// IOS takes a dotted mask, not the CIDR form the FS connector uses.
	if !strings.Contains(joined, "ip address 192.0.2.240 255.255.0.0") {
		t.Errorf("dotted mask expected: %v", cmds)
	}
	// An L2 switch has no routing table: "ip route" would be refused.
	if !strings.Contains(joined, "ip default-gateway 192.0.2.1") {
		t.Errorf("default gateway form: %v", cmds)
	}
	// Credentials LAST: the console session can drop at the password change of
	// the account currently logged in, and everything else must already be in place.
	if at("username mcadmin") < at("switchport trunk allowed vlan add 99") {
		t.Error("credentials must come after everything else")
	}
	if !strings.Contains(joined, "username mcadmin privilege 15 secret 0 s3cret") {
		t.Error("\"secret\" and never \"password\": type 7 is trivially reversible")
	}
	if strings.Contains(joined, "username mcadmin password") {
		t.Error("\"password\" stores the secret reversibly and must never be used")
	}
	// The hostname is sanitized: a space would split the command.
	if !strings.Contains(joined, "hostname lab-cat") {
		t.Errorf("hostname sanitizing: %v", cmds)
	}

	// NO ENABLE PASSWORD, by fleet rule — and APPLIED rather than merely omitted:
	// not writing it would leave the rule true on a factory Catalyst and false on
	// every switch that already carried one. BOTH forms go, because IOS has two
	// and a device can carry either.
	if strings.Contains(joined, "enable secret 0") || strings.Contains(joined, "enable password 0") {
		t.Errorf("an enable password was written: %v", cmds)
	}
	if !strings.Contains(joined, "no enable secret") ||
		!strings.Contains(joined, "no enable password") {
		t.Errorf("both \"no enable\" forms must be written: %v", cmds)
	}
	// ORDER IS LOAD-BEARING ON THIS PLATFORM, more than on Arista: with no enable
	// password set, IOS refuses "enable" from a vty ("% No password set"), so
	// "privilege 15" on the account becomes the only path to a privileged SSH
	// session. It has to be written BEFORE the password is removed.
	if at("username mcadmin") > at("no enable secret") {
		t.Error("the enable password is removed before the privilege-15 account exists: " +
			"between those two lines the device has no privileged path over the network")
	}
}

// The refusal IOS gives when "enable" is attempted from a vty with no enable
// password configured. Matching it by name is what turns a 15-second timeout into
// a message naming the actual remedy — and the remedy is "privilege 15", not a
// password, because setting one back would break the fleet rule.
func TestNoPasswordSetIsRecognised(t *testing.T) {
	var out = "lab-cat2960x>enable\r\n% No password set\r\nlab-cat2960x>"
	if !reNoPasswordSet.MatchString(out) {
		t.Fatal("\"% No password set\" is not recognised: the connector would time out " +
			"waiting for a password prompt that IOS is never going to print")
	}
	// And it must not be mistaken for an ordinary CLI refusal by the generic
	// parser, which would report it as a failed command rather than a failed
	// privilege escalation.
	if reNoPasswordSet.MatchString("% Invalid input detected at '^' marker.") {
		t.Error("the pattern is too loose")
	}
}

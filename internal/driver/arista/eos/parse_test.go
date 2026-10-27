package eos

import (
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- naming ---

func TestStandardPortNameAcceptsTheThreeSpellings(t *testing.T) {
	var d = &Driver{}
	var cases = map[string]string{
		"Ethernet1":  "ET1",
		"Ethernet49": "ET49",
		"Et49":       "ET49",
		"et 3":       "ET3",
		"ETHERNET12": "ET12",
	}
	var in, want string
	for in, want = range cases {
		var got, ok = d.StandardPortName(in)
		if !ok || got != want {
			t.Errorf("StandardPortName(%q) = %q,%v — want %q,true", in, got, ok, want)
		}
	}
}

// A breakout lane must be REFUSED, not folded onto its cage. Keying Et49/1 as
// "ET49" would make four distinct forwarding interfaces one port, and meshcraft
// would eventually write one lane's intent onto another's jack.
func TestStandardPortNameRefusesBreakoutLanes(t *testing.T) {
	var d = &Driver{}
	var name string
	for _, name = range []string{"Ethernet49/1", "Et49/4", "ethernet 3/2"} {
		var got, ok = d.StandardPortName(name)
		if ok {
			t.Errorf("StandardPortName(%q) accepted it as %q — a breakout lane has no "+
				"standard name on this connector and must be refused", name, got)
		}
	}
}

// Management1 is the OUT-OF-BAND port: not part of the switching fabric, no
// VLAN, and a profile applied to it would be meaningless.
func TestManagementPortIsRecognisedAndNeverNumbered(t *testing.T) {
	var d = &Driver{}
	var name string
	for _, name = range []string{"Management1", "Ma1", "management 1"} {
		if !IsManagementPort(name) {
			t.Errorf("IsManagementPort(%q) = false — the skip could not be explained", name)
		}
		var _, ok = d.StandardPortName(name)
		if ok {
			t.Errorf("StandardPortName(%q) numbered the out-of-band port", name)
		}
	}
}

func TestCLIPortNameRoundTripsAndRefusesTheRest(t *testing.T) {
	if cliPortName("ET49") != "Ethernet49" {
		t.Errorf("cliPortName(ET49) = %q — the FULL word goes on the wire so a "+
			"transcript can be pasted back into the device", cliPortName("ET49"))
	}
	var bad string
	for _, bad = range []string{"GE1", "XG2", "ET", "ETx", ""} {
		if cliPortName(bad) != "" {
			t.Errorf("cliPortName(%q) = %q — a name outside the scheme must produce "+
				"nothing, so the caller refuses instead of writing blind", bad, cliPortName(bad))
		}
	}
}

func TestStdPortLessOrdersNumerically(t *testing.T) {
	if !stdPortLess("ET2", "ET10") {
		t.Error("ET2 must sort before ET10 — string order would put ET10 first")
	}
}

// --- show version ---

const showVersionFixture = `Arista DCS-7050SX3-48YC8-F
Hardware version:    11.03
Serial number:       JPE19161915
Hardware MAC address: 2899.3a0e.1f6f
System MAC address:  2899.3a0e.1f70

Software image version: 4.24.2.1F
Architecture:           i686
Internal build version: 4.24.2.1F-18375681.42421F

Uptime:                 5 weeks, 2 days
Total memory:           8098888 kB
`

func TestParseShowVersion(t *testing.T) {
	var f = parseShowVersion(showVersionFixture)
	if f.Model != "DCS-7050SX3-48YC8-F" {
		t.Errorf("model = %q", f.Model)
	}
	if f.Serial != "JPE19161915" {
		t.Errorf("serial = %q", f.Serial)
	}
	if f.Release != "4.24.2.1F" {
		t.Errorf("release = %q", f.Release)
	}
	// The SYSTEM MAC wins over the hardware one: it is what the device uses on
	// the wire, and therefore what a neighbour's LLDP and a DHCP server report.
	if f.MAC != "2899.3a0e.1f70" {
		t.Errorf("mac = %q — the System MAC address must win over the Hardware one", f.MAC)
	}
	if !reIsEOS.MatchString(showVersionFixture) {
		t.Error("reIsEOS did not recognise its own fixture — writes would be refused")
	}
}

// The dialect gate protects against driving an EOS switch with the IOS command
// set. A Catalyst banner must NOT pass it.
func TestDialectGateRejectsIOS(t *testing.T) {
	var iosOut = `Cisco IOS Software, C2960X Software (C2960X-UNIVERSALK9-M), Version 15.2(7)E3
Model Number: WS-C2960X-24PS-L
System Serial Number: FOC1234X0AB
`
	if reIsEOS.MatchString(iosOut) {
		t.Error("reIsEOS matched Cisco IOS output: the gate that keeps the two command " +
			"sets apart would be open")
	}
}

// --- show interfaces status ---

const statusFixture = `Port       Name              Status       Vlan     Duplex Speed  Type            Flags
Et1                          connected    1        full   1G     1000BASE-T
Et2                          notconnect   1        auto   auto   Not Present
Et3        desk 4            connected    30       full   1G     1000BASE-T
Et49       core uplink       connected    trunk    full   10G    10GBASE-SR
Et50                         errdisabled  trunk    full   10G    Not Present
Et51                         err-disabled trunk    full   10G    Not Present
Et52                         disabled     1        auto   auto   Not Present
Ma1                          connected    routed   full   1G     10/100/1000
Po1                          connected    trunk    full   10G    N/A
Et53/1                       connected    trunk    full   25G    25GBASE-CR
`

func TestParseInterfacesStatus(t *testing.T) {
	var ports = parseInterfacesStatus(statusFixture)
	var byName = map[string]statusPort{}
	var p statusPort
	for _, p = range ports {
		byName[p.Name] = p
	}
	// Ma1 (out-of-band), Po1 (port-channel) and Et53/1 (breakout lane) are all
	// outside what this connector numbers.
	if len(byName) != 7 {
		t.Fatalf("parsed %d faceplate ports, want 7 (Ma1, Po1 and the breakout lane "+
			"must be skipped): %v", len(byName), byName)
	}
	if !byName["ET1"].LinkUp || byName["ET1"].SpeedMbps != 1000 {
		t.Errorf("ET1 = %+v", byName["ET1"])
	}
	// A free-text Name column with spaces must not shift the fields.
	if !byName["ET49"].LinkUp || byName["ET49"].SpeedMbps != 10000 {
		t.Errorf("ET49 = %+v — a Name column containing a space shifted the row", byName["ET49"])
	}
	if !byName["ET3"].LinkUp {
		t.Errorf("ET3 = %+v — \"desk 4\" in the Name column shifted the row", byName["ET3"])
	}
	// Both spellings of the same state must fold together, or one train's output
	// reports a state the rest of the connector does not know.
	if !byName["ET50"].errDisabled() || !byName["ET51"].errDisabled() {
		t.Errorf("errdisabled/err-disabled did not fold: %+v %+v", byName["ET50"], byName["ET51"])
	}
	if !byName["ET52"].adminDown() {
		t.Errorf("ET52 = %+v — \"disabled\" is an operator shutdown, distinct from "+
			"notconnect and from errdisabled", byName["ET52"])
	}
	if byName["ET52"].errDisabled() {
		t.Error("\"disabled\" must not read as errdisabled: the two call for opposite reactions")
	}
}

func TestLinkStateVocabulary(t *testing.T) {
	var cases = map[string]driver.LinkState{
		"connected":   driver.LinkUpState,
		"notconnect":  driver.LinkUnplugged,
		"disabled":    driver.LinkDisabled,
		"errdisabled": driver.LinkErrDisabled,
	}
	var word string
	var want driver.LinkState
	for word, want = range cases {
		if eosLinkState(word) != want {
			t.Errorf("eosLinkState(%q) = %q, want %q", word, eosLinkState(word), want)
		}
	}
}

func TestParseErrDisabled(t *testing.T) {
	var out = `Port      Name          Status       Reason
Et50                    errdisabled  xcvr-unsupported
Et51      uplink b      err-disabled bpduguard
Ma1                     errdisabled  link-flap
`
	var got = parseErrDisabled(out)
	if got["ET50"] != ErrDisableXcvr {
		t.Errorf("ET50 cause = %q, want %q", got["ET50"], ErrDisableXcvr)
	}
	if got["ET51"] != "bpduguard" {
		t.Errorf("ET51 cause = %q — the free-text Name column shifted the row", got["ET51"])
	}
	if len(got) != 2 {
		t.Errorf("got %v — the out-of-band port must not be numbered here either", got)
	}
}

// An empty 1G cage reports "Not Present" and must still classify as optical. A
// BUILT-IN copper jack announces its PHY ladder ("10/100/1000"); a bare
// "1000BASE-T" is a TRANSCEIVER family name — a copper module seated in a cage
// (core01's Et17), and reading it as a jack split a 48-cage run in three.
func TestIsCage(t *testing.T) {
	if !isCage("1000BASE-T", 1000) {
		t.Error("\"1000BASE-T\" names a copper transceiver: the socket under it is a cage")
	}
	if isCage("10/100/1000", 1000) {
		t.Error("the RJ45 speed triple must not classify as a cage")
	}
	if !isCage("Not Present", 1000) {
		t.Error("an empty 1G cage reports \"Not Present\" and is still a cage")
	}
	if !isCage("Not Present", 10000) {
		t.Error("nothing in this range delivers 10G over RJ45: 10G+ is a cage")
	}
}

// --- show vlan ---

func TestParseVlanTable(t *testing.T) {
	var out = `VLAN  Name                             Status    Ports
----- -------------------------------- --------- ------------------------
1     default                          active    Et1, Et2, Et3
99    Management                       active    Et49
100   VLAN0100                         active
4094  edge                             suspended
`
	var got = parseVlanTable(out)
	var want = []driver.VLAN{
		{ID: 1, Name: "default"},
		{ID: 99, Name: "Management"},
		{ID: 100, Name: "VLAN0100"},
		{ID: 4094, Name: "edge"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseVlanTable = %+v, want %+v", got, want)
	}
}

// 1002-1005 are IOS's immovable FDDI / Token Ring defaults and have NO special
// status here. Copying that exclusion across would make four perfectly usable
// VLAN IDs silently impossible to deploy on this platform.
func TestVlan1002IsAnOrdinaryVlanHere(t *testing.T) {
	var got = parseVlanTable("1002  lab-transit                      active\n")
	if len(got) != 1 || got[0].ID != 1002 {
		t.Errorf("parseVlanTable dropped VLAN 1002: %+v — that is an IOS reserved "+
			"range, not an EOS one", got)
	}
	var missing = missingVLANs(nil, []driver.VLAN{{ID: 1002, Name: "lab"}}, 99, nil)
	var found = false
	var v driver.VLAN
	for _, v = range missing {
		if v.ID == 1002 {
			found = true
		}
	}
	if !found {
		t.Error("missingVLANs excluded 1002: it would then never be created on this platform")
	}
}

// --- show running-config ---

const runFixture = `! Command: show running-config
! device: sw-lab-1 (DCS-7050SX3-48YC8-F, EOS-4.24.2.1F)
!
hostname sw-lab-1
!
spanning-tree mode mstp
!
service unsupported-transceiver wiprolabs f5047577
!
errdisable recovery cause xcvr-unsupported
!
vlan 99
   name Management
!
username mcadmin privilege 15 role network-admin secret sha512 $6$abc
!
interface Ethernet1
   switchport access vlan 30
!
interface Ethernet2
   switchport mode trunk
!
interface Ethernet3
   switchport access vlan 40
   switchport mode trunk
   switchport trunk allowed vlan 1,40,99
   switchport trunk native vlan 99
!
interface Management1
   ip address 192.168.1.10/24
!
interface Vlan99
   ip address 192.0.2.5/24
!
interface Vlan42
   shutdown
   ip address 10.42.0.5/24
!
ip name-server vrf default 1.1.1.1 8.8.8.8
!
ip route vrf mgmt 0.0.0.0/0 192.168.1.1
!
ip route 0.0.0.0/0 192.0.2.1
!
snmp-server community meshcraft ro
snmp-server host 192.0.2.200 version 2c meshcraft
snmp-server enable traps snmp linkdown linkup
!
end
`

func TestParseRunningConfig(t *testing.T) {
	var rc = parseRunningConfig(runFixture, "192.0.2.5")

	if rc.Hostname != "sw-lab-1" {
		t.Errorf("hostname = %q", rc.Hostname)
	}
	// EOS runs LLDP out of the box and prints nothing for it. Starting from
	// false — the IOS habit — would report every healthy Arista as having no LLDP.
	if !rc.LLDPRun {
		t.Error("LLDPRun must default to TRUE on this platform: only \"no lldp run\" " +
			"turns it off, and it prints nothing when it is on")
	}
	if !rc.UnsupportedXcvr || !rc.XcvrRecovery {
		t.Errorf("the transceiver lines were not read: xcvr=%v recovery=%v",
			rc.UnsupportedXcvr, rc.XcvrRecovery)
	}

	// The management SVI: PREFIX form, and the SHUTDOWN one must not win.
	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 (Vlan42 carries an address but is shut down)", rc.MgmtVLAN)
	}
	if rc.Mgmt.Address != "192.0.2.5/24" || rc.Mgmt.Interface != "Vlan99" {
		t.Errorf("mgmt = %+v", rc.Mgmt)
	}
	// The out-of-band port is recorded and NEVER promoted to the management path.
	if rc.MgmtOOB != "192.168.1.10/24" {
		t.Errorf("MgmtOOB = %q", rc.MgmtOOB)
	}
	// Only the DEFAULT VRF's route is the management gateway: the mgmt-VRF route
	// is somebody else's routing and taking it would report a gateway management
	// traffic never uses.
	if rc.Mgmt.Gateway != "192.0.2.1" {
		t.Errorf("gateway = %q, want 192.0.2.1 (the vrf mgmt route must not win)", rc.Mgmt.Gateway)
	}
	if !reflect.DeepEqual(rc.Mgmt.DNS, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Errorf("dns = %v — the \"vrf default\" token must not be read as a server", rc.Mgmt.DNS)
	}

	// A trunk with NO allowed line carries ALL VLANs, which is the opposite of
	// an empty list.
	if !rc.AllowedAll["ET2"] {
		t.Error("ET2 has no allowed-vlan line: on this platform that means ALL VLANs")
	}
	if rc.AllowedAll["ET3"] {
		t.Error("ET3 carries an explicit allowed list and must not be marked all-allowed")
	}

	// A trunk's PVID is its NATIVE vlan, not the stale access line above it —
	// and its Tagged set excludes that native, which is where meshcraft carries it.
	var et3 = rc.Ports["ET3"]
	if et3.Mode != "trunk" || et3.PVID != 99 {
		t.Errorf("ET3 = %+v — a leftover \"switchport access vlan 40\" must not win "+
			"over the native vlan on a trunk", et3)
	}
	if !reflect.DeepEqual(et3.Tagged, []int{1, 40}) {
		t.Errorf("ET3 tagged = %v, want [1 40] — the native VLAN lives in PVID, not "+
			"in the tagged set, or an exactly-applied trunk drifts for ever", et3.Tagged)
	}

	var et1 = rc.Ports["ET1"]
	if et1.Mode != "access" || et1.PVID != 30 {
		t.Errorf("ET1 = %+v", et1)
	}

	// "username mcadmin privilege 15 role network-admin secret sha512 $6$abc"
	// — BOTH words travel, and the role is the one EOS authorisation consults.
	if !reflect.DeepEqual(rc.Accounts, []driver.Account{{
		Name:      "mcadmin",
		Privilege: 15,
		Role:      "network-admin",
		Authority: driver.AuthorityFull,
		Source:    "show running-config",
	}}) {
		t.Errorf("accounts = %+v", rc.Accounts)
	}
	if rc.Communities["meshcraft"] != "ro" {
		t.Errorf("communities = %v", rc.Communities)
	}
	if len(rc.TrapReceivers) != 1 || rc.TrapReceivers[0].Version != "2c" {
		t.Errorf("trap receivers = %+v", rc.TrapReceivers)
	}
	if !trapLinkEventsCovered(rc.SNMPTrapEvents) {
		t.Errorf("trap events = %v — linkdown/linkup are the floor", rc.SNMPTrapEvents)
	}
	if rc.SSHShut {
		t.Error("SSH read as shut down when \"management ssh\" carries no \"shutdown\"")
	}
}

// "no lldp run" is the ONLY line that appears, and it is what must flip the flag.
// TestUsernameRankIsReadFromTheRoleFirst — the EOS-specific half.
//
// Authorisation on this platform consults the ROLE, which is why accountCmds
// writes "privilege 15 role network-admin" rather than a privilege alone. So
// an enumeration must carry the role, and rank on it: a `network-operator` that
// somebody also gave privilege 15 can still only run `show`, and calling it a
// full administrator would send an audit after the wrong account.
func TestUsernameRankIsReadFromTheRoleFirst(t *testing.T) {
	var rc = parseRunningConfig(strings.Join([]string{
		"hostname sw1",
		"username mcadmin privilege 15 role network-admin secret sha512 $6$abc",
		"username lecteur privilege 15 role network-operator secret sha512 $6$def",
		"username maison privilege 15 role noc-custom secret sha512 $6$ghi",
		"username muet secret sha512 $6$jkl",
		"end",
	}, "\n"), "")
	if len(rc.Accounts) != 4 {
		t.Fatalf("accounts = %+v", rc.Accounts)
	}
	if rc.Accounts[0].Authority != driver.AuthorityFull {
		t.Errorf("mcadmin = %+v, want full administrator", rc.Accounts[0])
	}
	if rc.Accounts[1].Authority != driver.AuthorityLimited ||
		rc.Accounts[1].Privilege != 15 {
		t.Errorf("lecteur = %+v: `network-operator` runs `show` and nothing else, "+
			"whatever number sits next to it — and the number is still reported", rc.Accounts[1])
	}
	// An operator-defined role: its rules are in a "role NAME" block this line
	// does not carry, so the numeric privilege is all there is to go on.
	if rc.Accounts[2].Role != "noc-custom" || rc.Accounts[2].Authority != driver.AuthorityFull {
		t.Errorf("maison = %+v: an unknown role falls back to the stated privilege, "+
			"and the role name still travels for a human to judge", rc.Accounts[2])
	}
	if rc.Accounts[3].Privilege != driver.PrivilegeUnknown ||
		rc.Accounts[3].Authority != driver.AuthorityUnknown ||
		rc.Accounts[3].Role != "" {
		t.Errorf("muet = %+v: the line states neither word, so NOT STATED — not 0, "+
			"and not EOS's documented default of 1", rc.Accounts[3])
	}
}

// The secret is attacker-influenced text this parser walks over: it must not be
// able to promote its own account.
func TestRankIsNotReadOutOfTheHash(t *testing.T) {
	var got = parseRunUsername(`username piege secret sha512 $6$aa$privilege 15 role network-admin`)
	if got.Privilege != driver.PrivilegeUnknown || got.Role != "" ||
		got.Authority != driver.AuthorityUnknown {
		t.Errorf("%+v — both words must be read from the line's HEAD, never from "+
			"the hash the device echoes back", got)
	}
}

func TestParseRunningConfigLLDPOff(t *testing.T) {
	var rc = parseRunningConfig("hostname x\n!\nno lldp run\n!\nend\n", "")
	if rc.LLDPRun {
		t.Error("\"no lldp run\" did not turn the flag off")
	}
}

// SSH is on by default here, so a "shutdown" under "management ssh" is the
// only way it can be off — and it is the door meshcraft administers through.
func TestParseRunningConfigSSHShutdown(t *testing.T) {
	var rc = parseRunningConfig("management ssh\n   shutdown\n!\nend\n", "")
	if !rc.SSHShut {
		t.Error("\"management ssh\" + \"shutdown\" was not read as the service being down")
	}
	// The same "shutdown" under an interface must not leak into it.
	var rc2 = parseRunningConfig("interface Vlan99\n   shutdown\n   ip address 10.0.0.1/24\n!\nend\n", "10.0.0.1")
	if rc2.SSHShut {
		t.Error("a \"shutdown\" inside an interface block was read as the SSH service")
	}
	if rc2.MgmtVLAN != 0 {
		t.Error("a shut SVI must not be reported as the management path: its address answers nothing")
	}
}

// A dotted mask is never WRITTEN by this connector, and it must still be READ:
// a configuration imported from elsewhere can carry it, and refusing to read
// what we would not write would lose the address rather than the argument.
func TestParseRunningConfigAcceptsDottedMaskOnRead(t *testing.T) {
	var rc = parseRunningConfig("interface Vlan99\n   ip address 192.0.2.5 255.255.255.0\n!\nend\n", "192.0.2.5")
	if rc.Mgmt.Address != "192.0.2.5/24" {
		t.Errorf("address = %q, want 192.0.2.5/24", rc.Mgmt.Address)
	}
}

func TestExpandVlanList(t *testing.T) {
	var got, err = expandVlanList("1,10-12, 99")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{1, 10, 11, 12, 99}) {
		t.Errorf("got %v", got)
	}
	if _, err = expandVlanList("4095"); err == nil {
		t.Error("4095 is outside the VLAN range and must be refused")
	}
}

func TestParseTrapReceiver(t *testing.T) {
	var r = parseTrapReceiver("192.0.2.200", "version 2c meshcraft udp-port 1162")
	if r.Version != "2c" || r.Community != "meshcraft" || r.Port != 1162 {
		t.Errorf("got %+v", r)
	}
	var inf = parseTrapReceiver("192.0.2.200", "informs version 2c other")
	if !inf.Informs || inf.Community != "other" {
		t.Errorf("got %+v", inf)
	}
	// A line with no "version" is v1 on this platform, and Version stays EMPTY
	// rather than being rounded — the emptiness is what decides a rewrite.
	var v1 = parseTrapReceiver("192.0.2.200", "public")
	if v1.Version != "" || v1.Community != "public" {
		t.Errorf("got %+v — an absent version must not be rounded to anything", v1)
	}
}

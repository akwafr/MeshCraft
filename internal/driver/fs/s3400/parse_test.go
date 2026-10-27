package s3400

import (
	"os"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Every sample below is VERBATIM from the lab session of 2026-08-03 against a
// S3400-48T4SP running Version 2.0.2J Build 95262, reached over SSH through
// the proxy. A parser that stops matching one of these has stopped matching
// the platform.

const labShowVersion = `show version
Fiberstore Co., Limited Internetwork Operating System Software
S3400-48T4SP Series Software, Version 2.0.2J Build 95262, RELEASE SOFTWARE
Copyright (c) 2019 by FS.COM All Rights Reserved
Compiled: 2022-3-10 10:2:32 by SYS, Image text-base: 0x80010000
ROM: System Bootstrap, Version 0.1.3,hardware version:A
Serial num:CG2003161906N0002, ID num:20065040419
System image file is "Switch.bin"
FS S3400-48T4SP RISC
262144K bytes of memory,16384K bytes of flash
Base ethernet MAC Address: 64:9d:99:22:7b:95
PCB version:A
snmp info:
  vend_ID:52642   product_ID:445   system_ID:1.3.6.1.4.1.52642.1.445.0
Switch uptime is 0:00:22:20, The current time: 2000-1-1 0:22:38
Switch#`

func TestParseShowVersion(t *testing.T) {
	var f = parseShowVersion(labShowVersion)
	if f.Model != "S3400-48T4SP" {
		t.Errorf("model=%q", f.Model)
	}
	if f.Version != "2.0.2J" {
		t.Errorf("version=%q", f.Version)
	}
	if f.Build != "95262" {
		t.Errorf("build=%q", f.Build)
	}
	if f.Serial != "CG2003161906N0002" {
		t.Errorf("serial=%q", f.Serial)
	}
	if f.MAC != "64:9d:99:22:7b:95" {
		t.Errorf("mac=%q", f.MAC)
	}
	if f.SysObjID != "1.3.6.1.4.1.52642.1.445.0" {
		t.Errorf("sysobjid=%q", f.SysObjID)
	}
	if !looksLikeS3400CLI(labShowVersion, f) {
		t.Error("the captured show version must be recognized as this CLI")
	}
	// The S3900 next door announces itself differently: its "show version"
	// must NOT be taken for this CLI, or writes meant for one platform would
	// be sent to the other.
	var s3900Out = "Operation Code Version 1.7.0\nHardware Version R0B\n"
	if looksLikeS3400CLI(s3900Out, parseShowVersion(s3900Out)) {
		t.Error("an FSOS 1.7 (S3900) show version must not be recognized as the S3400 CLI")
	}
}

func TestStandardPortName(t *testing.T) {
	var cases = map[string]string{
		"GigaEthernet0/1":  "GE1",
		"GigaEthernet0/48": "GE48",
		"g0/12":            "GE12",
		"Gi0/12":           "GE12",
		"TGigaEthernet0/2": "XG2",
		"tg0/2":            "XG2",
		// The LLDP neighbour table's own spelling (captured "TGi0/2").
		"TGi0/2": "XG2",
	}
	var d = &Driver{}
	var in, want string
	for in, want = range cases {
		var got, ok = d.StandardPortName(in)
		if !ok || got != want {
			t.Errorf("StandardPortName(%q) = %q,%v — want %q", in, got, ok, want)
		}
	}

	// Logical interfaces and out-of-range sockets are never faceplate ports.
	var notPorts = []string{
		"VLAN99", "Vlan1", "Null0", "Loopback0", "Port-channel1", "",
		"GigaEthernet0/49", // a 48T4SP has no 49th copper socket
		"tg0/5",            // nor a 5th cage
		"GigaEthernet1/1",  // slot 1 is not something this range has
		"GE1", "XG1",       // already standard: must be refused, never remapped
	}
	var n string
	for _, n = range notPorts {
		var got, ok = d.StandardPortName(n)
		if ok {
			t.Errorf("StandardPortName(%q) = %q — must be refused", n, got)
		}
	}
}

func TestPortNumbersAndIfIndex(t *testing.T) {
	// CAPTURED: "Ifindex is 129, unique port number is 1" on GigaEthernet0/1
	// and "Ifindex is 178, unique port number is 50" on TGigaEthernet0/2.
	var idx, ok = IfIndexOf("GE1")
	if !ok || idx != 129 {
		t.Errorf("IfIndexOf(GE1) = %d,%v — want 129", idx, ok)
	}
	if idx, ok = IfIndexOf("XG2"); !ok || idx != 178 {
		t.Errorf("IfIndexOf(XG2) = %d,%v — want 178", idx, ok)
	}
	var num int
	if num, ok = uniquePortNumber("XG1"); !ok || num != 49 {
		t.Errorf("uniquePortNumber(XG1) = %d — want 49", num)
	}
	var copperName, cerr = cagesFrom1.cliPortName("GE12")
	var cageName, gerr = cagesFrom1.cliPortName("XG2")
	if cerr != nil || gerr != nil || copperName != "g0/12" || cageName != "tg0/2" {
		t.Errorf("cliPortName: %q (%v) / %q (%v)", copperName, cerr, cageName, gerr)
	}
}

const labShowVlan = `show vlan
VLAN Status  Name                             Ports
---- ------- -------------------------------- ---------------------------------
1    Static  Default                          g0/2, g0/3, g0/4, g0/5, g0/6
                                              g0/7, g0/8, g0/9, g0/10, g0/11
                                              g0/46, g0/47, g0/48, tg0/1, tg0/2
                                              tg0/3, tg0/4
99   Static  LAN99_Management                 tg0/1, tg0/2, tg0/3, tg0/4
200  Static  TEST_CLAUDE                      g0/1, tg0/1, tg0/2, tg0/3, tg0/4
Switch#`

func TestParseShowVlan(t *testing.T) {
	var table = parseShowVlan(labShowVlan)
	if len(table.VLANs) != 3 {
		t.Fatalf("vlans=%v", table.VLANs)
	}
	if table.VLANs[0].ID != 1 || table.VLANs[0].Name != "Default" {
		t.Errorf("vlan[0]=%+v", table.VLANs[0])
	}
	if table.VLANs[1].ID != 99 || table.VLANs[1].Name != "LAN99_Management" {
		t.Errorf("vlan[1]=%+v", table.VLANs[1])
	}
	// The continuation lines are the whole difficulty: a parser that drops
	// them loses every port after the fifth of each VLAN.
	if !hasInt(table.Members["GE48"], 1) {
		t.Errorf("GE48 must be in VLAN 1 (it sits on a wrapped line): %v", table.Members["GE48"])
	}
	if !hasInt(table.Members["XG2"], 99) {
		t.Errorf("XG2 must be in VLAN 99: %v", table.Members["XG2"])
	}
	if !hasInt(table.Members["GE1"], 200) {
		t.Errorf("GE1 must be in VLAN 200: %v", table.Members["GE1"])
	}
	if hasInt(table.Members["GE1"], 1) {
		t.Errorf("GE1 was moved out of VLAN 1 and must not still be listed there: %v",
			table.Members["GE1"])
	}
}

func hasInt(xs []int, want int) bool {
	var x int
	for _, x = range xs {
		if x == want {
			return true
		}
	}
	return false
}

const labRunningConfig = `show running-config
Building configuration...

Current configuration:
!
!version 2.0.2J build 95262
service timestamps log date
!
spanning-tree mode rstp
!
aaa authentication login default local
aaa authentication enable default none
aaa authorization exec default local
!
username admin password 0 SECRET
!
enable password 0 SECRET level 15
!
interface Null0
!
interface GigaEthernet0/1
 spanning-tree cost 20000
 switchport pvid 200
!
interface GigaEthernet0/2
!
interface TGigaEthernet0/1
 switchport mode trunk
!
interface TGigaEthernet0/2
 switchport mode trunk
!
interface VLAN99
 ip address 192.0.2.3 255.255.0.0
 no ip directed-broadcast
!
vlan 99
 name LAN99_Management
!
vlan 1,99
!
ip route default 192.0.2.1
!
ip http server
!
ip sshd enable
!
Switch#`

func TestParseRunningConfig(t *testing.T) {
	var cfg = ParseRunningConfig(labRunningConfig)

	if cfg.MgmtVLAN != 99 || cfg.MgmtAddr != "192.0.2.3 255.255.0.0" {
		t.Errorf("mgmt=%d %q", cfg.MgmtVLAN, cfg.MgmtAddr)
	}
	if cfg.Gateway != "192.0.2.1" {
		t.Errorf("gateway=%q", cfg.Gateway)
	}
	if cfg.STPMode != "rstp" {
		t.Errorf("stp mode=%q", cfg.STPMode)
	}
	if !cfg.SSHOn || !cfg.HTTPOn {
		t.Errorf("ssh=%v http=%v — both were captured present", cfg.SSHOn, cfg.HTTPOn)
	}
	// LLDP is OFF in this capture, and saying so is the point: a switch with
	// no "lldp run" sees no neighbour and is seen by none.
	if cfg.LLDPRun {
		t.Error("this capture carries no \"lldp run\"")
	}
	if cfg.VlanNames[99] != "LAN99_Management" {
		t.Errorf("vlan names=%v", cfg.VlanNames)
	}

	// An access port the configuration touched.
	var ge1 = cfg.Ports["GE1"]
	if ge1.Mode != "access" || ge1.PVID != 200 {
		t.Errorf("GE1=%+v — want access pvid 200", ge1)
	}
	// A port the configuration is SILENT about is still a port, on the
	// platform default.
	var ge2, known = cfg.Ports["GE2"]
	if !known || ge2.Mode != "access" || ge2.PVID != 1 {
		t.Errorf("GE2=%+v known=%v — a bare interface block means access/vlan 1", ge2, known)
	}
	var xg1 = cfg.Ports["XG1"]
	if xg1.Mode != "trunk" {
		t.Errorf("XG1=%+v — want trunk", xg1)
	}
	// The SVI must never be filed as a faceplate port.
	var _, isPort = cfg.Ports["VLAN99"]
	if isPort {
		t.Error("interface VLAN99 must not appear among the ports")
	}
}

const labShowSpanningTree = `show spanning-tree

Spanning tree enabled protocol RSTP(2004)

RSTP
  Root ID    Priority    0
             Address     D401.C3AB.32F4
             Port        TGigaEthernet0/2
             Cost        6000
             Hello Time 2 sec  Max Age 20 sec  Forward Delay 15 sec

  Bridge ID  Priority    36864
             Address     649D.9922.7B95
             Hello Time 2 sec  Max Age 20 sec  Forward Delay 15 sec

Interface        Role Sts Cost      Pri.Nbr  Type
---------------- ---- --- --------- -------- -----------------------
tg0/2            Root FWD 2000      128.178  P2p

Switch#`

// TestAccountsAlwaysReadUnknownOnThisRange — the honest gap.
//
// "username admin password 0 SECRET" is the whole account declaration on this
// platform: there is no privilege keyword, because privilege here is a property
// of the ENABLE LEVELS. So every account comes back with an unstated privilege,
// and the audit is blind on this range — blind ON PURPOSE, which is what lets a
// reader tell it apart from a range where the answer is "level 0".
//
// The same capture also carries "aaa authentication enable default none",
// which means every one of these accounts can in fact reach level 15 without
// being asked anything. That is a fact about the SWITCH, not about the account,
// and Client.Accounts deliberately does not convert it into a per-account claim.
func TestAccountsAlwaysReadUnknownOnThisRange(t *testing.T) {
	var got = parseCfgAccounts(labRunningConfig)
	if len(got) != 1 || got[0].Name != "admin" {
		t.Fatalf("accounts = %+v", got)
	}
	if got[0].Privilege != driver.PrivilegeUnknown {
		t.Errorf("privilege = %d — this CLI states none, and 0 would be a claim the "+
			"device never made", got[0].Privilege)
	}
	if got[0].Authority != driver.AuthorityUnknown {
		t.Errorf("authority = %v — nothing on the account line ranks it, and the "+
			"enable-level configuration next to it is about the switch, not this "+
			"account", got[0].Authority)
	}
	if got[0].Source != "show running-config" {
		t.Errorf("source = %q — the audit quotes where the name was seen", got[0].Source)
	}
	// "enable password 0 SECRET level 15" is on the same capture and must NOT
	// be mistaken for an account declaration.
	if len(got) != 1 {
		t.Errorf("the enable line is not an account: %+v", got)
	}
}

func TestParseSpanningTree(t *testing.T) {
	var st = parseSpanningTree(labShowSpanningTree)
	if !st.Enabled || st.Mode != "rstp" {
		t.Errorf("enabled=%v mode=%q", st.Enabled, st.Mode)
	}
	// The two Priority lines are told apart by LABEL, not by order: reading
	// them positionally would report the root's priority as the bridge's.
	if st.RootPriority != 0 {
		t.Errorf("root priority=%d — want 0", st.RootPriority)
	}
	if st.BridgePriority != 36864 {
		t.Errorf("bridge priority=%d — want 36864 (the value written in the lab)", st.BridgePriority)
	}
	if st.RootBridgeID != "d4:01:c3:ab:32:f4" {
		t.Errorf("root bridge=%q", st.RootBridgeID)
	}
	if st.BridgeID != "64:9d:99:22:7b:95" {
		t.Errorf("bridge=%q", st.BridgeID)
	}
	if st.IsRoot {
		t.Error("this bridge is NOT root — the root address differs from its own")
	}
	if st.RootPathCost != 6000 {
		t.Errorf("root path cost=%d", st.RootPathCost)
	}
	if len(st.Ports) != 1 {
		t.Fatalf("ports=%+v", st.Ports)
	}
	if st.Ports[0].Port != "XG2" || st.Ports[0].Role != "root" ||
		st.Ports[0].State != "forwarding" || st.Ports[0].PathCost != 2000 {
		t.Errorf("port=%+v", st.Ports[0])
	}
}

const labLLDPNeighbor = `show lldp neighbor
Capability Codes:
       (R)Router,(B)Bridge,(C)DOCSIS Cable Device,(T)Telephone
       (W)WLAN Access Point, (P)Repeater,(S)Station,(O)Other
Device-ID       Local-Intf      Hldtme       Port-ID      Capability
                TGi0/2          109          Eth 1/25     R B
Total entries displayed: 1
Switch#`

func TestParseLLDPNeighbors(t *testing.T) {
	var ns = parseLLDPNeighbors(labLLDPNeighbor)
	if len(ns) != 1 {
		t.Fatalf("neighbours=%+v", ns)
	}
	// The Device-ID column is EMPTY in this capture. A whitespace split would
	// shift every field left and file "TGi0/2" as the device name.
	if ns[0].DeviceID != "" {
		t.Errorf("device id=%q — the capture has none", ns[0].DeviceID)
	}
	if ns[0].LocalPort != "XG2" {
		t.Errorf("local port=%q — want XG2", ns[0].LocalPort)
	}
	if ns[0].PortID != "Eth 1/25" {
		t.Errorf("port id=%q — want \"Eth 1/25\" (the S3900 on the other end)", ns[0].PortID)
	}
	if ns[0].HoldTime != 109 {
		t.Errorf("hold time=%d", ns[0].HoldTime)
	}
	if !strings.Contains(ns[0].Caps, "R") || !strings.Contains(ns[0].Caps, "B") {
		t.Errorf("caps=%q", ns[0].Caps)
	}
}

const labIfProtocol = `show interface | include protocol
GigaEthernet0/1 is down, line protocol is down
GigaEthernet0/2 is up, line protocol is down
TGigaEthernet0/2 is up, line protocol is up
VLAN99 is up, line protocol is up
Null0 is up, line protocol is up
Switch#`

func TestParseLinkStates(t *testing.T) {
	var links = ParseLinkStates(labIfProtocol)
	if links["GE1"] != driver.LinkDown {
		t.Errorf("GE1=%q", links["GE1"])
	}
	// Link up but protocol down is still not a usable link.
	if links["GE2"] != driver.LinkDown {
		t.Errorf("GE2=%q — protocol down means down", links["GE2"])
	}
	if links["XG2"] != driver.LinkUpState {
		t.Errorf("XG2=%q", links["XG2"])
	}
	// SVIs and Null0 are not faceplate ports.
	var _, ok = links["VLAN99"]
	if ok {
		t.Error("VLAN99 must not appear among the ports")
	}
	if len(links) != 3 {
		t.Errorf("links=%v — only physical sockets belong here", links)
	}
}

const labPortDetail = `show interface TGigaEthernet0/2
TGigaEthernet0/2 is up, line protocol is up
  Ifindex is 178, unique port number is 50
  Hardware is 10Giga-FX-SFP, address is 649d.9922.7bc7 (bia 649d.9922.7bc7)
  MTU 1500 bytes, BW 10000000 kbit, DLY 10 usec
  Encapsulation ARPA
  Full-duplex,  10000Mb/s,  Flow-Control Off
Transceiver Info:
    SFP,LC,1330nm,10000BASE-FX-LR,LOS:no
    DDM:YES,Vend:Pureoptics,PN:SFP-10G-LR-BXD
DDM info:
    TX power:-1.61 dBm, RX power:-1.51 dBm
    SFP temperature:47.00 C,supply voltage :3.29V,Bias Current.:42.84mA
Switch#`

func TestParsePortDetailWithOptics(t *testing.T) {
	var d = parsePortDetail(labPortDetail)
	if d.Name != "XG2" {
		t.Errorf("name=%q", d.Name)
	}
	if d.IfIndex != 178 {
		t.Errorf("ifindex=%d", d.IfIndex)
	}
	if d.Hardware != "10Giga-FX-SFP" || d.MAC != "649d.9922.7bc7" {
		t.Errorf("hw=%q mac=%q", d.Hardware, d.MAC)
	}
	if !d.FullDup || d.SpeedMbps != 10000 {
		t.Errorf("duplex/speed=%v %d", d.FullDup, d.SpeedMbps)
	}
	// The optics are the reason this read exists: this platform serves DDM on
	// the CLI, which the S3900 does not.
	if !d.HasOptics || d.TxPowerDB != -1.61 || d.RxPowerDB != -1.51 || d.TempC != 47 {
		t.Errorf("optics=%+v", d)
	}
}

func TestParsePortDetailCopperHasNoOptics(t *testing.T) {
	var copper = `GigaEthernet0/1 is down, line protocol is down
  Ifindex is 129, unique port number is 1
  Hardware is Giga-TX, address is 649d.9922.7b96 (bia 649d.9922.7b96)
  Auto-duplex,  Auto-speed,  Flow-Control Off
Switch#`
	var d = parsePortDetail(copper)
	if d.Name != "GE1" || d.IfIndex != 129 {
		t.Errorf("detail=%+v", d)
	}
	if d.HasOptics {
		t.Error("a copper port must not report optical power")
	}
}

const labIPIfBrief = `show ip interface brief
Interface                  IP-Address      Method Protocol-Status
Null0                      unassigned      manual up
VLAN99                     192.0.2.3       manual up
Switch#`

func TestParseIPInterfaceBrief(t *testing.T) {
	var ifs = parseIPInterfaceBrief(labIPIfBrief)
	if len(ifs) != 1 {
		t.Fatalf("interfaces=%+v — Null0 is not a VLAN interface", ifs)
	}
	if ifs[0].VlanID != 99 || ifs[0].IP != "192.0.2.3" || ifs[0].Method != "manual" || !ifs[0].Up {
		t.Errorf("iface=%+v", ifs[0])
	}
}

func TestExpandAndCompactVlanList(t *testing.T) {
	var got = expandVlanList("1-3,99")
	if len(got) != 4 || got[0] != 1 || got[3] != 99 {
		t.Errorf("expand=%v", got)
	}
	if compactVlanList([]int{1, 2, 3, 99}) != "1-3,99" {
		t.Errorf("compact=%q", compactVlanList([]int{1, 2, 3, 99}))
	}
	if compactVlanList([]int{99}) != "99" {
		t.Errorf("compact single=%q", compactVlanList([]int{99}))
	}
}

func TestPortCountsFromModel(t *testing.T) {
	var c, s = portCountsFor("S3400-48T4SP")
	if c != 48 || s != 4 {
		t.Errorf("48T4SP = %d copper, %d sfp", c, s)
	}
	if c, s = portCountsFor("S3400-24T4FP"); c != 24 || s != 4 {
		t.Errorf("24T4FP = %d copper, %d sfp", c, s)
	}
	// An unknown model falls back to the surveyed unit rather than inventing.
	if c, s = portCountsFor(""); c != copperPorts || s != sfpPorts {
		t.Errorf("unknown model = %d/%d", c, s)
	}
}

// The pager erases its own "--More--" with BACKSPACES, and 0x08 is not
// whitespace to Go's regexp. Before stripPager removed them, a line-anchored
// parser read the backspace run as the interface name and matched nothing
// after the first record: the live run of 2026-08-03 read 52 sockets and
// parsed ONE, which would have shown every port on a healthy switch as
// unknown. The byte sequence below is verbatim from that session.
func TestPagerBackspacesAreStripped(t *testing.T) {
	var raw = "show interface | include protocol\r\n" +
		"GigaEthernet0/1 is down, line protocol is down\r\n" +
		" --More--\x08\x08\x08\x08\x08\x08\x08\x08\x08         \x08\x08\x08\x08\x08\x08\x08\x08\x08" +
		"GigaEthernet0/2 is down, line protocol is down\r\n" +
		" --More--\x08\x08\x08\x08\x08\x08\x08\x08\x08         \x08\x08\x08\x08\x08\x08\x08\x08\x08" +
		"TGigaEthernet0/2 is up, line protocol is up\r\n" +
		"Switch#"

	var clean = stripPager(raw)
	if strings.Contains(clean, "\x08") {
		t.Fatal("backspaces survived: the interface list will not parse")
	}
	if strings.Contains(clean, "--More--") {
		t.Fatal("the pager marker survived")
	}
	var links = ParseLinkStates(clean)
	if len(links) != 3 {
		t.Fatalf("parsed %d links, want 3 — %q", len(links), clean)
	}
	if links["XG2"] != driver.LinkUpState || links["GE1"] != driver.LinkDown {
		t.Errorf("links=%v", links)
	}
}

// A trunk that must become an access port is the case this connector got
// wrong: "switchport pvid" alone leaves "switchport mode trunk" in place,
// so the port keeps carrying every allowed VLAN and the discrepancy never
// resolves — meshcraft only re-applies after a configuration change.
// Observed 2026-08-03 on GE45-48 of the lab 48T4SP.
func TestPortConfigCmdsAccessUndoesATrunk(t *testing.T) {
	var cmds, err = portConfigCmds(cagesFrom1, "GE45", PortState{Mode: "access", PVID: 100})
	if err != nil {
		t.Fatalf("a copper port needs no cage observation: %v", err)
	}
	var want = []string{
		"interface g0/45",
		"switchport mode access",
		"no switchport trunk vlan-allowed",
		"switchport pvid 100",
		"exit",
	}
	if strings.Join(cmds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %v\nwant %v", cmds, want)
	}
}

func TestPortConfigCmdsTrunkAddsTheNativeToTheAllowedList(t *testing.T) {
	// The device requires its native VLAN in the allowed list; meshcraft's
	// model carries it as PVID. The write adds it back — and the read must
	// remove it again, or an exactly-applied trunk drifts for ever.
	var cmds, err = portConfigCmds(cagesFrom1, "XG1",
		PortState{Mode: "trunk", PVID: 100, Tagged: []int{42, 98, 99}})
	if err != nil {
		t.Fatalf("the Build 95262 numbering was observed, so XG1 can be named: %v", err)
	}
	var want = []string{
		"interface tg0/1",
		"switchport mode trunk",
		"switchport trunk vlan-allowed 42,98-100",
		"switchport pvid 100",
		"exit",
	}
	if strings.Join(cmds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got %v\nwant %v", cmds, want)
	}
}

// The read must MIRROR the write: writePort adds the PVID to the allowed list
// (the device requires its native to be allowed), so the read takes it back
// out. meshcraft's intent never lists a port's own native among its tagged
// VLANs, so leaving it in made an exactly-applied trunk drift by exactly one
// VLAN, for ever. Observed 2026-08-03 on the lab 48T4SP.
func TestTrunkReadRemovesTheNativeFromTagged(t *testing.T) {
	// XG1 as the device actually held it: allowed 1,42,98-100 with native 1.
	var fromConfig = withObservedMembership(
		PortState{Mode: "trunk", PVID: 1, Tagged: []int{1, 42, 98, 99, 100}}, nil)
	if len(fromConfig.Tagged) != 4 || hasInt(fromConfig.Tagged, 1) {
		t.Errorf("config path: %v — VLAN 1 is the native and must not be tagged", fromConfig.Tagged)
	}

	// GE45 as it was: allowed 42,98-100 with native 100.
	var native100 = withObservedMembership(
		PortState{Mode: "trunk", PVID: 100, Tagged: []int{42, 98, 99, 100}}, nil)
	if len(native100.Tagged) != 3 || hasInt(native100.Tagged, 100) {
		t.Errorf("config path: %v — VLAN 100 is the native here", native100.Tagged)
	}

	// The show-vlan path must land on exactly the same answer for the same
	// physical state: a trunk with no explicit allowed line, seen by "show
	// vlan" in 1, 42, 98, 99 and 100.
	var derived = withObservedMembership(
		PortState{Mode: "trunk", PVID: 1}, []int{1, 42, 98, 99, 100})
	if len(derived.Tagged) != 4 || hasInt(derived.Tagged, 1) {
		t.Errorf("show-vlan path: %v", derived.Tagged)
	}

	// An access port carries nothing tagged, whatever stale trunk line the
	// configuration still holds.
	var access = withObservedMembership(
		PortState{Mode: "access", PVID: 100, Tagged: []int{42, 98}}, []int{100})
	if len(access.Tagged) != 0 {
		t.Errorf("access: %v", access.Tagged)
	}
}

// The 2026-08-05 capture from the SAME lab unit, replayed from
// testdata/show_spanning_tree.txt — the raw bytes the device sent, not a
// transcription. Two things beyond TestParseSpanningTree: the DESIGNATED
// role must come out as the shared vocabulary word ("desg" matches nothing
// downstream), and a factory-priority bridge (32768) must read as such.
func TestParseSpanningTreeFromCapture(t *testing.T) {
	var b, err = os.ReadFile("testdata/show_spanning_tree.txt")
	if err != nil {
		t.Fatalf("reading capture: %v", err)
	}
	var st = parseSpanningTree(string(b))
	if !st.Enabled || st.Mode != "rstp" {
		t.Errorf("enabled=%v mode=%q — want enabled rstp", st.Enabled, st.Mode)
	}
	if st.BridgePriority != 32768 || st.BridgeID != "64:9d:99:22:7b:95" {
		t.Errorf("bridge=%q prio=%d — want 64:9d:99:22:7b:95 at 32768", st.BridgeID, st.BridgePriority)
	}
	if st.RootBridgeID != "d4:01:c3:ab:32:f4" || st.RootPriority != 0 || st.RootPathCost != 6000 {
		t.Errorf("root=%q prio=%d cost=%d — want d4:01:c3:ab:32:f4 / 0 / 6000",
			st.RootBridgeID, st.RootPriority, st.RootPathCost)
	}
	if st.IsRoot {
		t.Error("the Catalyst is root here, not this bridge")
	}
	if len(st.Ports) != 2 {
		t.Fatalf("ports=%+v — want XG1 and XG2", st.Ports)
	}
	if st.Ports[0].Port != "XG1" || st.Ports[0].Role != driver.STPRoleDesignated ||
		st.Ports[0].State != "forwarding" || st.Ports[0].PathCost != 2000 {
		t.Errorf("XG1=%+v — want designated/forwarding cost 2000 (NOT \"desg\")", st.Ports[0])
	}
	if st.Ports[1].Port != "XG2" || st.Ports[1].Role != driver.STPRoleRoot ||
		st.Ports[1].State != "forwarding" {
		t.Errorf("XG2=%+v — want root/forwarding", st.Ports[1])
	}
}

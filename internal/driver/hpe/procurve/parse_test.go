package procurve

import (
	"strings"
	"testing"
)

// runningConfig2610 is VERBATIM from the reference unit: ProCurve 2610-48
// (J9088A), release R.11.121, read through a meshcraft proxy on 2026-08-02.
//
// It is kept whole rather than trimmed to the interesting lines, because the
// parser's job is to survive the parts that are NOT interesting — the banner with
// its embedded newlines, the interface stanzas that follow the VLANs and also end
// in "exit", and "no untagged 1-52", which is a removal that must never be read
// as membership.
const runningConfig2610 = `Running configuration:

; J9088A Configuration Editor; Created on release #R.11.121

hostname "SW-PLAYER-79" 
snmp-server contact "noc@example.org" 
snmp-server location "InTheKitchen" 
max-vlans 30 
time timezone 1 
time daylight-time-rule Western-Europe 
interface 1 
   disable
exit
ip default-gateway 203.0.113.1 
timesync sntp 
snmp-server community "ops" Operator 
vlan 1 
   name "DEFAULT_VLAN" 
   ip address dhcp-bootp 
   no untagged 1-52 
   exit 
vlan 3 
   name "staff" 
   tagged 45-52 
   exit 
vlan 101 
   name "ZONE1" 
   tagged 45-52 
   exit 
vlan 102 
   name "ZONE2" 
   tagged 45-52 
   exit 
vlan 103 
   name "ZONE3" 
   tagged 45-52 
   exit 
vlan 104 
   name "ZONE4" 
   tagged 45-52 
   exit 
vlan 9 
   name "wifi-guest" 
   tagged 45-52 
   exit 
vlan 10 
   name "streaming" 
   tagged 45-52 
   exit 
vlan 99 
   name "admin" 
   ip address 203.0.113.79 255.255.255.0 
   tagged 45-52 
   exit 
vlan 200 
   name "joueurs" 
   untagged 1-52 
   exit 
no dhcp-relay 
dhcp-snooping
banner motd "     *********************Authorized access only!******************
*****
     *                   @@@@   LYON ESPORT   @@@@                     *
     *********************Authorized access only!***********************
"
sntp unicast
sntp server 172.16.2.20
interface 45
   dhcp-snooping trust
   exit
interface 46
   dhcp-snooping trust
   exit
interface 47
   dhcp-snooping trust
   exit
interface 48
   dhcp-snooping trust
   exit
interface 49
   dhcp-snooping trust
   exit
interface 50
   dhcp-snooping trust
   exit
interface 51
   dhcp-snooping trust
   exit
interface 52
   dhcp-snooping trust
   exit
spanning-tree
spanning-tree 1 bpdu-protection
spanning-tree 2 bpdu-protection
spanning-tree 3 bpdu-protection
spanning-tree 4 bpdu-protection
spanning-tree 5 bpdu-protection
spanning-tree 6 bpdu-protection
spanning-tree 7 bpdu-protection
spanning-tree 8 bpdu-protection
spanning-tree 9 bpdu-protection
spanning-tree 10 bpdu-protection
spanning-tree 11 bpdu-protection
spanning-tree 12 bpdu-protection
spanning-tree 13 bpdu-protection
spanning-tree 14 bpdu-protection
spanning-tree 15 bpdu-protection
spanning-tree 16 bpdu-protection
spanning-tree 17 bpdu-protection
spanning-tree 18 bpdu-protection
spanning-tree 19 bpdu-protection
spanning-tree 20 bpdu-protection
spanning-tree 21 bpdu-protection
spanning-tree 22 bpdu-protection
spanning-tree 23 bpdu-protection
spanning-tree 24 bpdu-protection
spanning-tree 25 bpdu-protection
spanning-tree 26 bpdu-protection
spanning-tree 27 bpdu-protection
spanning-tree 28 bpdu-protection
spanning-tree 29 bpdu-protection
spanning-tree 30 bpdu-protection
spanning-tree 31 bpdu-protection
spanning-tree 32 bpdu-protection
spanning-tree 33 bpdu-protection
spanning-tree 34 bpdu-protection
spanning-tree 35 bpdu-protection
spanning-tree 36 bpdu-protection
spanning-tree 37 bpdu-protection
spanning-tree 38 bpdu-protection
spanning-tree 39 bpdu-protection
spanning-tree 40 bpdu-protection
spanning-tree 41 bpdu-protection
spanning-tree 42 bpdu-protection
spanning-tree 43 bpdu-protection
spanning-tree 44 bpdu-protection
spanning-tree bpdu-protection-timeout 10 force-version rstp-operation
ip ssh
no dhcp config-file-update
password manager

`

func TestParseRunningConfigFromTheReferenceUnit(t *testing.T) {
	var rc = parseRunningConfig(runningConfig2610)

	if rc.Hostname != "SW-PLAYER-79" {
		t.Errorf("hostname = %q", rc.Hostname)
	}
	// The RUNTIME ceiling, not a product constant: this unit is set to 30 where
	// the family supports up to 256.
	if rc.MaxVLANs != 30 {
		t.Errorf("max-vlans = %d, want 30 as the device carries it", rc.MaxVLANs)
	}
	var wantVLANs = []int{1, 3, 9, 10, 99, 101, 102, 103, 104, 200}
	var id int
	for _, id = range wantVLANs {
		var _, ok = rc.VLANs[id]
		if !ok {
			t.Errorf("VLAN %d missing from the parse", id)
		}
	}
	if rc.VLANs[200] != "joueurs" {
		t.Errorf("VLAN 200 name = %q, want joueurs", rc.VLANs[200])
	}

	// THE TRAP: "no untagged 1-52" under VLAN 1 is a REMOVAL. Reading it as
	// membership would put all 52 ports into VLAN 1 and make every one of them
	// look like drift.
	if len(rc.Untagged[1]) != 0 {
		t.Errorf("VLAN 1 has %d untagged ports: \"no untagged\" was read as membership",
			len(rc.Untagged[1]))
	}
	// VLAN 200 genuinely carries them all.
	if len(rc.Untagged[200]) != 52 {
		t.Errorf("VLAN 200 untagged = %d ports, want 52", len(rc.Untagged[200]))
	}
	// The uplinks are tagged in the transport VLANs.
	if len(rc.Tagged[99]) != 8 || rc.Tagged[99][0] != 45 {
		t.Errorf("VLAN 99 tagged = %v, want 45-52", rc.Tagged[99])
	}

	// Management is the VLAN with a REAL address. VLAN 1 carries "ip address
	// dhcp-bootp", which is the factory default and must not win.
	if rc.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d, want 99", rc.MgmtVLAN)
	}
	if !strings.HasPrefix(rc.MgmtAddr, "203.0.113.79") {
		t.Errorf("management address = %q", rc.MgmtAddr)
	}
}

// The compact range grammar is used in BOTH directions — the device writes it and
// accepts it — so the two functions must agree.
func TestPortRangeRoundTrip(t *testing.T) {
	var cases = []struct{ in, want string }{
		{"45-52", "45-52"},
		{"1", "1"},
		{"1,3,5", "1,3,5"},
		{"1-4,9", "1-4,9"},
		{"1-52", "1-52"},
		{"5,4,3", "3-5"},
		{"3,1,2,7", "1-3,7"},
	}
	var c struct{ in, want string }
	for _, c = range cases {
		var got = formatPortRange(parsePortRange(c.in))
		if got != c.want {
			t.Errorf("round trip %q = %q, want %q", c.in, got, c.want)
		}
	}
	if len(parsePortRange("45-52")) != 8 {
		t.Error("45-52 did not expand to eight ports")
	}
	if parsePortRange("") != nil {
		t.Error("an empty range produced ports")
	}
	if formatPortRange(nil) != "" {
		t.Error("no ports produced a non-empty range")
	}
}

func TestPortNameTranslation(t *testing.T) {
	var d = &Driver{}
	var name, ok = d.StandardPortName("45")
	if !ok || name != "GE45" {
		t.Errorf("StandardPortName(45) = %q,%v", name, ok)
	}
	if _, ok = d.StandardPortName("craft"); ok {
		t.Error("a non-numeric port name was accepted")
	}
	var num, ok2 = portNumber("GE45")
	if !ok2 || num != 45 {
		t.Errorf("portNumber(GE45) = %d,%v", num, ok2)
	}
}

// The refusal shape this CLI actually produced when handed a keyword it does not
// know. Detecting it is the only signal a command did not take: the exit status
// says nothing.
func TestCLIRefusalIsDetected(t *testing.T) {
	var transcript = "vlan 4001 name X media ethernet\n" +
		"                  ^\n" +
		"% Invalid input detected at '^' marker.\n"
	var got = cliRefusal(transcript)
	if got == "" {
		t.Fatal("a refusal went undetected: the write would be reported as applied")
	}
	if !strings.Contains(got, "Invalid input") {
		t.Errorf("refusal = %q", got)
	}
	if cliRefusal("vlan 3\n   tagged 45-52\nexit\n") != "" {
		t.Error("a clean transcript was read as a refusal")
	}
}

func TestMembershipCommandsAreVlanCentric(t *testing.T) {
	var cmds = membershipCmds(map[int][]int{42: {5, 6, 7}}, "untagged")
	var joined = strings.Join(cmds, "|")
	if joined != "vlan 42|untagged 5-7|exit" {
		t.Errorf("commands = %q, want the VLAN stanza this CLI writes", joined)
	}
	// Ports must be collapsed: this CLI takes ranges, and a 48-port list would
	// exceed the line length it accepts.
	var wide = membershipCmds(map[int][]int{1: {1, 2, 3, 4, 5, 6, 7, 8}}, "no tagged")
	if !strings.Contains(strings.Join(wide, "|"), "1-8") {
		t.Errorf("ports were not collapsed into a range: %v", wide)
	}
	if membershipCmds(nil, "untagged") != nil {
		t.Error("an empty map produced commands")
	}
}

func TestSanitizeVlanName(t *testing.T) {
	var got = sanitizeVlanName("LAN99 Management")
	if got != "LAN99_Management" {
		t.Errorf("sanitized = %q", got)
	}
	got = sanitizeVlanName("!!!")
	if got != "VLAN" {
		t.Errorf("a name with nothing usable = %q, want a safe default", got)
	}
}

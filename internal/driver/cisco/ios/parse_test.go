package ios

// Fixtures are DOCUMENTED output shapes for a WS-C2960X-24PS-L on IOS
// 15.2(7)E, not lab captures (cf. the package doc). They are what the parsers
// are contracted against: the day a real capture arrives, replacing these
// constants is the whole migration, and anything the connector got wrong shows
// up here rather than on a customer's switch.

import "testing"

const labVersion = `Cisco IOS Software, C2960X Software (C2960X-UNIVERSALK9-M), Version 15.2(7)E3, RELEASE SOFTWARE (fc2)
Technical Support: http://www.cisco.com/techsupport
Copyright (c) 1986-2019 by Cisco Systems, Inc.

ROM: Bootstrap program is C2960X boot loader
BOOTLDR: C2960X Boot Loader (C2960X-HBOOT-M) Version 15.2(5r)E1

lab-cat2960x uptime is 3 weeks, 1 day, 4 hours, 12 minutes
System returned to ROM by power-on
System image file is "flash:/c2960x-universalk9-mz.152-7.E3/c2960x-universalk9-mz.152-7.E3.bin"

cisco WS-C2960X-24PS-L (APM86XXX) processor (revision D0) with 131072K bytes of memory.
Processor board ID FOC2048X1AB
Last reset from power-on
1 Virtual Ethernet interface
28 Gigabit Ethernet interfaces

Base ethernet MAC Address       : 00:1A:2B:3C:4D:5E
Motherboard assembly number     : 73-16296-04
Model revision number           : D0
Motherboard revision number     : A0
Model number                    : WS-C2960X-24PS-L
System serial number            : FOC2048X1AB
Top Assembly Part Number        : 68-5209-01
`

// 24 copper jacks then 4 SFP cages — the split the faceplate must find on its
// own. Gi1/0/25..27 are EMPTY cages ("unknown"), Gi1/0/28 carries a transceiver:
// deducing the medium from what is plugged in would make the drawing change
// shape when someone unplugs a fibre.
const labStatus = `Port      Name               Status       Vlan       Duplex  Speed Type
Gi1/0/1                      notconnect   1            auto   auto 10/100/1000BaseTX
Gi1/0/2   poste-accueil      connected    10           a-full a-1000 10/100/1000BaseTX
Gi1/0/3                      disabled     1            auto   auto 10/100/1000BaseTX
Gi1/0/24                     notconnect   1            auto   auto 10/100/1000BaseTX
Gi1/0/25                     notconnect   1            auto   auto unknown
Gi1/0/26                     notconnect   1            auto   auto unknown
Gi1/0/27                     notconnect   1            auto   auto unknown
Gi1/0/28  uplink-core        connected    trunk        full   1000 1000BaseSX SFP
`

const labVlanBrief = `VLAN Name                             Status    Ports
---- -------------------------------- --------- -------------------------------
1    default                          active    Gi1/0/1, Gi1/0/3, Gi1/0/24
10   Bureaux                          active    Gi1/0/2
99   LAN99_Management                 active
100  VLAN0100                         active
1002 fddi-default                     act/unsup
1003 token-ring-default               act/unsup
1004 fddinet-default                  act/unsup
1005 trnet-default                    act/unsup
`

const labRun = `Building configuration...

Current configuration : 4231 bytes
!
version 15.2
hostname lab-cat2960x
!
enable secret 5 $1$abcd$0123456789ABCDEFGHIJk.
!
username mcadmin privilege 15 secret 5 $1$efgh$JIHGFEDCBA9876543210l.
!
lldp run
ip domain-name meshcraft.local
ip name-server 10.100.0.1 10.100.0.2
!
interface GigabitEthernet1/0/1
!
interface GigabitEthernet1/0/2
 switchport access vlan 10
 switchport mode access
!
interface GigabitEthernet1/0/3
 switchport mode trunk
!
interface GigabitEthernet1/0/28
 description uplink-core
 switchport trunk native vlan 99
 switchport trunk allowed vlan 1,99,100
 switchport mode trunk
!
interface Vlan1
 no ip address
 shutdown
!
interface Vlan99
 ip address 10.99.0.240 255.255.0.0
!
ip default-gateway 10.99.0.1
ip ssh version 2
!
snmp-server community public RO
snmp-server community private RW
snmp-server host 10.100.0.5 traps public
!
line vty 0 15
 login local
 transport input ssh
!
end
`

func TestParseShowVersion(t *testing.T) {
	var f = parseShowVersion(labVersion)
	if f.Release != "15.2(7)E3" {
		t.Errorf("release=%q", f.Release)
	}
	if f.Model != "WS-C2960X-24PS-L" {
		t.Errorf("model=%q", f.Model)
	}
	if f.Serial != "FOC2048X1AB" {
		t.Errorf("serial=%q", f.Serial)
	}
	if f.MAC != "00:1A:2B:3C:4D:5E" {
		t.Errorf("mac=%q", f.MAC)
	}
	// The feature set decides whether this device can ever serve SSH.
	if !f.hasCrypto() {
		t.Errorf("a UNIVERSALK9 image must be recognized as crypto-capable: image=%q", f.Image)
	}
	var noCrypto = parseShowVersion(
		"Cisco IOS Software, C2960X Software (C2960X-UNIVERSALLITE-M), Version 15.2(7)E3, RELEASE SOFTWARE (fc2)")
	if noCrypto.hasCrypto() {
		t.Error("a non-k9 image must NOT be announced as able to generate an SSH key")
	}
}

func TestStandardPortName(t *testing.T) {
	var d = &Driver{}
	var cases = []struct {
		in   string
		want string
		ok   bool
	}{
		// The three spellings the three sources use.
		{"GigabitEthernet1/0/11", "GE11", true}, // running-config, SNMP ifDescr
		{"Gi1/0/11", "GE11", true},              // show interfaces status, LLDP
		{"gi 1/0/11", "GE11", true},             // typed by hand
		{"TenGigabitEthernet1/0/1", "XG1", true},
		{"Te1/0/2", "XG2", true},
		// A 2960 classic has no stack member in its names: slot/port.
		{"GigabitEthernet0/24", "GE24", true},
		{"Fa0/1", "FE1", true},
		// THE COLLISION this connector's two prefixes exist for: on a 48FPD both
		// of these are real sockets, and they are not the same one.
		{"GigabitEthernet1/0/1", "GE1", true},
		{"TenGigabitEthernet1/0/1", "XG1", true},
		// Stack members other than 1: refused, never numbered. A member can
		// renumber itself, and a standard name that followed it would re-key
		// every port of the stack.
		{"GigabitEthernet2/0/5", "", false},
		{"Gi3/0/1", "", false},
		// Uplink MODULE slots are not contiguous with the faceplate.
		{"GigabitEthernet1/1/1", "", false},
		// Not faceplate ports — the same walk and the same config return these.
		{"Vlan99", "", false},
		{"Vlan1/0/1", "", false},
		{"Port-channel1", "", false},
		{"FastEthernet0", "", false}, // the out-of-band management jack
		{"Loopback0", "", false},
		{"Null0", "", false},
		// A bare index has lost the media word, and on this platform that word
		// is what tells two sockets apart.
		{"1/0/11", "", false},
		{"", "", false},
	}
	var c struct {
		in   string
		want string
		ok   bool
	}
	for _, c = range cases {
		var got, ok = d.StandardPortName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("StandardPortName(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCLIPortNameRoundTrip(t *testing.T) {
	var d = &Driver{}
	var prefixes = []string{"FE", "GE", "TG", "XG"}
	var p string
	for _, p = range prefixes {
		var i int
		for i = 1; i <= 48; i++ {
			var std = p + itoa(i)
			var cli = cliPortName(std)
			if cli == "" {
				t.Fatalf("cliPortName(%q) = \"\"", std)
			}
			var back, ok = d.StandardPortName(cli)
			if !ok || back != std {
				t.Errorf("round trip broke: %q -> %q -> %q (ok=%v)", std, cli, back, ok)
			}
		}
	}
	if cliPortName("not-a-standard-name") != "" {
		t.Error("a name outside the scheme must translate to \"\", never a guess")
	}
	if cliPortName("GE") != "" {
		t.Error("a prefix with no number must translate to \"\"")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestParseInterfacesStatus(t *testing.T) {
	var ports = parseInterfacesStatus(labStatus)
	if len(ports) != 8 {
		t.Fatalf("ports=%d (%v)", len(ports), ports)
	}
	if ports[0].Name != "GE1" || ports[0].CLIName != "GigabitEthernet1/0/1" || ports[0].LinkUp {
		t.Errorf("port[0]=%+v", ports[0])
	}
	// A free-text Name column containing spaces must not shift the parse.
	if ports[1].Name != "GE2" || !ports[1].LinkUp {
		t.Errorf("port[1]=%+v", ports[1])
	}
	// "disabled" is not "connected": an administratively down port has no link.
	if ports[2].Name != "GE3" || ports[2].LinkUp {
		t.Errorf("port[2]=%+v", ports[2])
	}
	var last = ports[len(ports)-1]
	if last.Name != "GE28" || !last.LinkUp {
		t.Errorf("port 28=%+v", last)
	}
	// Copper announces itself whether or not anything is plugged in; a cage
	// announces "unknown" when empty. Both must classify on the Type column and
	// not on the port number.
	if isSFPCage("GE", ports[0].Type) {
		t.Errorf("10/100/1000BaseTX must read as copper: %q", ports[0].Type)
	}
	if !isSFPCage("GE", ports[4].Type) {
		t.Errorf("an EMPTY cage (%q) must still read as a cage", ports[4].Type)
	}
	if !isSFPCage("GE", last.Type) {
		t.Errorf("a populated cage (%q) must read as a cage", last.Type)
	}
}

func TestRefusedTransceiverIsNotAnEmptyCage(t *testing.T) {
	// THE symptom that looks like everything except its cause: IOS shuts a cage
	// holding an optic it does not read as Cisco-coded, and the port then reads
	// exactly like an empty one. Uplink discovery finds nothing, the management
	// VLAN is carried nowhere, and the probe blames the network.
	const status = `Port      Name               Status       Vlan       Duplex  Speed Type
Gi1/0/24                     notconnect   1            auto   auto 10/100/1000BaseTX
Gi1/0/25  uplink-core        err-disabled 1            auto   auto 1000BaseSX SFP
Gi1/0/28  poste 7            connected    10           a-full a-1000 10/100/1000BaseTX
`
	var ports = parseInterfacesStatus(status)
	if len(ports) != 3 {
		t.Fatalf("ports=%d", len(ports))
	}
	// An err-disabled port has no link, like an empty one...
	if ports[1].LinkUp {
		t.Error("an err-disabled port carries no link")
	}
	// ...and must NOT be offered as an uplink, or the management VLAN lands on
	// a port that forwards nothing.
	var up = ParseUpPorts(status)
	if len(up) != 1 || up[0] != "GigabitEthernet1/0/28" {
		t.Errorf("up=%v — an err-disabled port must never be chosen as an uplink", up)
	}
	// ...but the two states must stay TELLABLE APART, which is the whole point.
	if !ports[1].errDisabled() {
		t.Error("Gi1/0/25 was shut by the switch, not left empty")
	}
	if ports[0].errDisabled() {
		t.Error("« notconnect » is an empty port, not one the switch shut")
	}
	// And the cage is still a cage: the faceplate must not change shape.
	if !isSFPCage(ports[1].StdPrefix, ports[1].Type) {
		t.Error("a refused optic is still an optical cage")
	}
}

func TestParseErrDisabledReasons(t *testing.T) {
	// The reason column, which the plain status table does not carry. It is the
	// whole of what the operator needs: gbic-invalid is a refused optic (the
	// link is fine), bpduguard is a loop someone just created.
	const out = `Port      Name               Status       Reason               Err-disabled Vlans
Gi1/0/25                     err-disabled gbic-invalid
Gi1/0/26  lien de secours    err-disabled gbic-invalid
Gi1/0/10  poste 4            err-disabled bpduguard
Te1/0/1                      err-disabled gbic-invalid
Gi2/0/1                      err-disabled gbic-invalid
`
	var reasons = parseErrDisabled(out)
	// Four ports of member 1; the stacked member is outside coverage and must
	// not be named.
	if len(reasons) != 4 {
		t.Fatalf("reasons=%v", reasons)
	}
	if reasons["GE25"] != ErrDisableGBIC || reasons["XG1"] != ErrDisableGBIC {
		t.Errorf("refused optics not identified: %v", reasons)
	}
	// A free-text Name column with spaces must not shift the reason.
	if reasons["GE26"] != ErrDisableGBIC {
		t.Errorf("named port: %v", reasons)
	}
	if reasons["GE10"] != "bpduguard" {
		t.Errorf("a loop guard is not a transceiver problem: %v", reasons)
	}
}

func TestUnsupportedTransceiverRemediationIsShared(t *testing.T) {
	// The connector and the Web Serial soft reset must never drift into two
	// different remediations — web/src/pages/add/ciscoIosSoftReset.ts carries
	// the same two commands in the same order.
	var cmds = UnsupportedTransceiverCmds()
	if len(cmds) != 2 ||
		cmds[0] != "service unsupported-transceiver" ||
		cmds[1] != "no errdisable detect cause gbic-invalid" {
		t.Fatalf("cmds=%v", cmds)
	}
}

func TestParseUpPorts(t *testing.T) {
	var up = ParseUpPorts(labStatus)
	if len(up) != 2 || up[0] != "GigabitEthernet1/0/2" || up[1] != "GigabitEthernet1/0/28" {
		t.Fatalf("up=%v", up)
	}
}

func TestParseVlanBrief(t *testing.T) {
	var vlans = parseVlanBrief(labVlanBrief)
	// 1, 10, 99, 100 — and NOT 1002-1005, which every IOS carries, which cannot
	// be removed, and which would make the site VLAN diff permanently wrong.
	if len(vlans) != 4 {
		t.Fatalf("vlans=%v", vlans)
	}
	if vlans[0].ID != 1 || vlans[1].ID != 10 || vlans[2].ID != 99 || vlans[3].ID != 100 {
		t.Errorf("ids=%v", vlans)
	}
	if vlans[2].Name != "LAN99_Management" {
		t.Errorf("name=%q", vlans[2].Name)
	}
	// VLAN 100 still carries its DEFAULT name, which is precisely why the list
	// is read here and not from the running-config: that file does not mention
	// it at all, and the connector would re-create it on every apply.
	if vlans[3].Name != "VLAN0100" {
		t.Errorf("default-named VLAN lost: %v", vlans[3])
	}
}

func TestParseRunningConfig(t *testing.T) {
	var rc = parseRunningConfig(labRun)
	if rc.Hostname != "lab-cat2960x" {
		t.Errorf("hostname=%q", rc.Hostname)
	}

	// A port with an EMPTY block is at its factory default, not absent.
	var plain = rc.Ports["GE1"]
	if plain.Mode != "access" || plain.PVID != 1 {
		t.Errorf("factory-default port=%+v", plain)
	}
	var access = rc.Ports["GE2"]
	if access.Mode != "access" || access.PVID != 10 {
		t.Errorf("GE2=%+v", access)
	}

	// THE IOS TRAP: a trunk with no allowed-vlan line carries EVERYTHING.
	if !rc.AllowedAll["GE3"] {
		t.Error("a trunk without « switchport trunk allowed vlan » must be marked AllowedAll")
	}
	if rc.AllowedAll["GE28"] {
		t.Error("a trunk WITH an explicit allowed list must not be marked AllowedAll")
	}

	var trunk = rc.Ports["GE28"]
	if trunk.Mode != "trunk" || trunk.PVID != 99 {
		t.Fatalf("GE28=%+v", trunk)
	}
	// The native VLAN (99, the PVID) is NOT in the tagged set: the device's
	// allowed list is 1,99,100 but 99 is carried untagged, so tagged = [1,100].
	// Encoding native in tagged is exactly the false-drift bug this parser fixed.
	if len(trunk.Tagged) != 2 || trunk.Tagged[0] != 1 || trunk.Tagged[1] != 100 {
		t.Errorf("tagged=%v, want [1 100] (native 99 excluded, sorted)", trunk.Tagged)
	}

	// A SHUTDOWN SVI carries an address that answers nothing: Vlan1 must not be
	// taken for the management path just because it appears first.
	if rc.MgmtVLAN != 99 {
		t.Errorf("mgmt vlan=%d", rc.MgmtVLAN)
	}
	if rc.Mgmt.Mode != "static" || rc.Mgmt.Address != "10.99.0.240/16" {
		t.Errorf("mgmt=%+v", rc.Mgmt)
	}
	if rc.Mgmt.Gateway != "10.99.0.1" {
		t.Errorf("gateway=%q", rc.Mgmt.Gateway)
	}
	// IOS puts every resolver on one line.
	if len(rc.Mgmt.DNS) != 2 || rc.Mgmt.DNS[1] != "10.100.0.2" {
		t.Errorf("dns=%v", rc.Mgmt.DNS)
	}

	if rc.Communities["public"] != "ro" || rc.Communities["private"] != "rw" {
		t.Errorf("communities=%v", rc.Communities)
	}
	if len(rc.Accounts) != 1 || rc.Accounts[0] != "mcadmin" {
		t.Errorf("accounts=%v", rc.Accounts)
	}
	if !rc.LLDPRun {
		t.Error("« lldp run » present and not seen: the LLDP screen would blame the wrong thing")
	}
	if !rc.SSHReady {
		t.Error("domain name + ssh version 2 present: SSHReady must be true")
	}
}

func TestSyslogIsNotACLIError(t *testing.T) {
	// IOS prints syslog onto the vty, in the middle of command output, starting
	// with a percent sign. Mistaking one for a refusal turns every port that
	// happens to come up during an apply into a failed command.
	var notErrors = []string{
		"%LINK-3-UPDOWN: Interface GigabitEthernet1/0/5, changed state to up",
		"%LINEPROTO-5-UPDOWN: Line protocol on Interface Vlan99, changed state to up",
		"%SYS-5-CONFIG_I: Configured from console by mcadmin on vty0",
		"% Generating 2048 bit RSA keys, keys will be non-exportable...",
	}
	var s string
	for _, s = range notErrors {
		if reCLIError.MatchString(s) {
			t.Errorf("taken for a CLI refusal: %q", s)
		}
	}
	var areErrors = []string{
		"% Invalid input detected at '^' marker.",
		"% Incomplete command.",
		"% Ambiguous command:  \"sh ip\"",
		"Command rejected: An interface whose trunk encapsulation is \"Auto\" can not be configured to \"trunk\" mode.",
		"% Please define a domain-name first.",
	}
	for _, s = range areErrors {
		if !reCLIError.MatchString(s) {
			t.Errorf("refusal not recognized: %q", s)
		}
	}
}

func TestMaskConversions(t *testing.T) {
	var cases = []struct {
		prefix int
		mask   string
	}{
		{8, "255.0.0.0"},
		{16, "255.255.0.0"},
		{22, "255.255.252.0"},
		{24, "255.255.255.0"},
		{30, "255.255.255.252"},
		{32, "255.255.255.255"},
	}
	var c struct {
		prefix int
		mask   string
	}
	for _, c = range cases {
		if got := MaskFromPrefix(c.prefix); got != c.mask {
			t.Errorf("MaskFromPrefix(%d) = %q, want %q", c.prefix, got, c.mask)
		}
		if got := PrefixFromMask(c.mask); got != c.prefix {
			t.Errorf("PrefixFromMask(%q) = %d, want %d", c.mask, got, c.prefix)
		}
	}
	// An unreadable mask reports 0 rather than assuming /24: the caller then
	// says it could not read the prefix instead of writing down a wrong one.
	if PrefixFromMask("not-a-mask") != 0 {
		t.Error("an unreadable mask must report 0, never a default")
	}
}

package edgeswitch

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The command list is the contract with the serial adoption script. What this
// test really guards is the MODE STRUCTURE: privileged EXEC for the network
// and vlan-database gestures, Global Config for the interface work, and back
// out to save. Sending any of the privileged ones from "(Config)#" fails.
func TestMgmtPushCmdsFollowsTheModeStructure(t *testing.T) {
	var cmds = MgmtPushCmds(MgmtOpts{
		VlanID:     99,
		VlanName:   "LAN99_Management",
		IP:         "192.0.2.20",
		Mask:       "255.255.0.0",
		Gateway:    "192.0.2.1",
		Username:   "mcadmin",
		Password:   "Sekret1234",
		TrunkPorts: "0/16",
	})
	var want = []string{
		"enable",
		"vlan database",
		"vlan 99",
		"vlan name 99 LAN99_Management",
		"exit",
		"network protocol none",
		"network parms 192.0.2.20 255.255.0.0 192.0.2.1",
		"network mgmt_vlan 99",
		"configure",
		// NO "hostname" line, and its absence is pinned by the case below:
		// this CLI has no such command (refused 2026-08-05, cf. hostname.go).
		// Every port, not a guessed uplink: at first provisioning the cabling
		// is unknown, and "all" is a real Global Config form here.
		"vlan participation all include 99",
		"vlan port tagging all 99",
		"username mcadmin password Sekret1234 level 15",
		"exit",
		// "confirm" or the device asks a question the script would hang on.
		"write memory confirm",
	}
	if strings.Join(cmds, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\n\nwant:\n%s", strings.Join(cmds, "\n"), strings.Join(want, "\n"))
	}
}

// TestMgmtPushCmdsSendsNoHostname pins an ABSENCE, and the absence is the fact.
//
// On 2026-08-05 the reconciliation loop sent "hostname unifimoule1" to the lab
// EdgePoint S16 and the device answered "% Invalid input detected at '^'
// marker." — this FASTPATH CLI has no `hostname` command at all. hostname.go
// holds the full account.
//
// Provisioning kept emitting the verb anyway, at this builder and in the Web
// Serial script it specifies. A command sent in order to be refused names nothing
// and puts a red herring in the operator's console transcript, where it makes a
// REAL refusal harder to see. So: nothing in this builder may utter the word,
// whatever a caller asks for — which is also why MgmtOpts has no Hostname field.
//
// The 2026-08-10 capture of "snmp-server sysname" does not relax this test in
// either direction: the refuted verb stays out, and the working one stays out of
// PROVISIONING too, because naming an adopted device is Client.SetHostname's job
// driven by `devices.name` (cf. hostname.go, and its own absence test).
func TestMgmtPushCmdsSendsNoHostname(t *testing.T) {
	var cmds = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.20", Mask: "255.255.0.0",
		Username: "mcadmin", Password: "Sekret1234",
	})
	var c string
	for _, c = range cmds {
		if strings.Contains(c, "hostname") {
			t.Errorf("provisioning emits %q — this CLI refused \"hostname <name>\" on "+
				"2026-08-05 (\"%% Invalid input detected at '^' marker.\"); see "+
				"hostname.go. The name is set by another verb or not at all", c)
		}
	}
}

func TestMgmtPushCmdsOnVlan1CreatesNothing(t *testing.T) {
	var joined = strings.Join(MgmtPushCmds(MgmtOpts{
		VlanID: 1, IP: "192.168.1.2", Mask: "255.255.255.0",
	}), "\n")
	if strings.Contains(joined, "vlan database") {
		t.Error("VLAN 1 is reserved and always exists — it must never be created")
	}
	if strings.Contains(joined, "network mgmt_vlan") {
		t.Error("VLAN 1 is the default management VLAN — no need to set it")
	}
	if !strings.Contains(joined, "network parms 192.168.1.2 255.255.255.0") {
		t.Errorf("the address must be written with a dotted mask:\n%s", joined)
	}
}

// A port needs THREE commands to carry a VLAN on FASTPATH: pvid alone is not
// membership, and a port given a pvid without participation carries nothing.
func TestPortCmdsTakeThreeCommandsNotOne(t *testing.T) {
	var access = PortCmds("0/3", 100, nil)
	var wantAccess = []string{
		"interface 0/3",
		"vlan pvid 100",
		"vlan participation include 100",
		"exit",
	}
	if strings.Join(access, "\n") != strings.Join(wantAccess, "\n") {
		t.Errorf("access: %v", access)
	}

	var trunk = PortCmds("0/16", 1, []int{42, 99})
	var wantTrunk = []string{
		"interface 0/16",
		"vlan pvid 1",
		"vlan participation include 1",
		"vlan participation include 42",
		"vlan tagging 42",
		"vlan participation include 99",
		"vlan tagging 99",
		"exit",
	}
	if strings.Join(trunk, "\n") != strings.Join(wantTrunk, "\n") {
		t.Errorf("trunk: %v", trunk)
	}

	// The native VLAN is carried untagged: tagging it as well would make the
	// port emit both forms for one VLAN.
	var joined = strings.Join(PortCmds("0/16", 99, []int{99, 42}), "\n")
	if strings.Contains(joined, "vlan tagging 99") {
		t.Error("the pvid must never also be tagged")
	}
}

func TestStandardPortName(t *testing.T) {
	var d = &Driver{}
	var got, ok = d.StandardPortName("0/12")
	if !ok || got != "GE12" {
		t.Errorf("StandardPortName(0/12) = %q,%v", got, ok)
	}
	// The SNMP ifDescr spelling — the label the poller puts on every sample.
	if got, ok = d.StandardPortName("Slot: 0 Port: 18 10G - Level"); !ok || got != "GE18" {
		t.Errorf("StandardPortName(ifDescr of the SFP+ uplink) = %q,%v — want GE18", got, ok)
	}
	var notPorts = []string{"", "vlan 1", "GE12", "1/1", "0/0", "lag 1", "0/x",
		// SNMP rows that are NOT front-panel sockets: the CPU interface
		// (which CONTAINS "Slot: 5 Port: 1" mid-string), the aggregates,
		// and a non-zero slot in the ifDescr spelling.
		" CPU Interface for Slot: 5 Port: 1",
		" Link Aggregate 1",
		"Slot: 5 Port: 1 Gigabit - Level",
		"Slot: 0 Port: 0 Gigabit - Level"}
	var n string
	for _, n = range notPorts {
		var p, refused = d.StandardPortName(n)
		if refused {
			t.Errorf("StandardPortName(%q) = %q — must be refused", n, p)
		}
	}
}

// TestStandardPortNameOnCapturedIfTable replays the REAL ifTable of the lab
// EdgePoint S16 (cf. testdata/snmp_iftable.txt). This is the walk whose
// resolution failure hid the faceplate from monitoring: ifDescr rows in the
// "Slot: 0 Port: N …" spelling must each land on GE<N>, and the CPU/LAG
// rows must be refused rather than mapped onto a socket.
func TestStandardPortNameOnCapturedIfTable(t *testing.T) {
	var raw, err = os.ReadFile("testdata/snmp_iftable.txt")
	if err != nil {
		t.Fatal(err)
	}
	var d = &Driver{}
	var ethernet int
	var line string
	for _, line = range strings.Split(string(raw), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var cols = strings.SplitN(line, "\t", 4)
		if len(cols) != 4 {
			t.Fatalf("malformed capture row: %q", line)
		}
		var idx, idxErr = strconv.Atoi(cols[0])
		var ifType, typeErr = strconv.Atoi(cols[1])
		if idxErr != nil || typeErr != nil {
			t.Fatalf("malformed capture row: %q", line)
		}
		var descr = cols[3]
		var name, ok = d.StandardPortName(descr)
		if ifType == 6 {
			// ethernetCsmacd: a front-panel socket, whose ifIndex equals its
			// front-panel number on this hardware.
			var want = fmt.Sprintf("GE%d", idx)
			if !ok || name != want {
				t.Errorf("ifIndex %d: StandardPortName(%q) = %q,%v — want %q",
					idx, descr, name, ok, want)
			}
			ethernet++
			continue
		}
		if ok {
			t.Errorf("ifIndex %d: StandardPortName(%q) = %q — not a front-panel socket",
				idx, descr, name)
		}
	}
	// Sixteen RJ45 sockets plus two SFP+ cages: fewer means the capture rows
	// were skipped, not verified.
	if ethernet != 18 {
		t.Errorf("capture carries %d ethernet rows — want 18", ethernet)
	}
}

// --- splitStandard, the WRITE GATE ---------------------------------------
//
// This is the reader ApplyDHCPSnooping and ApplyStormControl refuse a port on,
// and the one cliPortName turns "GE12" into "0/12" with. What it accepts is
// what reaches the hardware, so the accepted set is pinned here rather than
// left to whichever numeric parser happens to be in the function (cf. its own
// comment in state.go: fmt.Sscanf used to accept a tail and address the socket
// named by the digits it stopped at).
func TestSplitStandardAcceptsOnlyTheFaceplateSpelling(t *testing.T) {
	var ok = []struct {
		name string
		want int
	}{
		{"GE1", 1},
		{"GE12", 12},
		// The SFP+ cages of this range are plain GE names — the media lives in
		// the port group's "SFP+" label, not in the prefix (cf. portGroups).
		// If these two were ever refused, the connector would stop writing to
		// the uplinks.
		{"GE17", 17},
		{"GE18", 18},
		{"ge12", 12},   // the split upper-cases
		{" GE12 ", 12}, // and trims
		{"GE007", 7},   // padded digits are still digits
	}
	var i int
	for i = range ok {
		var n, kind, got = splitStandard(ok[i].name)
		if !got || n != ok[i].want || kind != "GE" {
			t.Errorf("splitStandard(%q) = %d,%q,%v — want %d,\"GE\",true",
				ok[i].name, n, kind, got, ok[i].want)
		}
	}

	var refused = []string{
		"",       // no name at all
		"GE",     // the prefix with no number
		"GEabc",  // no digits anywhere
		"0/18",   // the CLI's own spelling is not the standard one
		"XG3",    // this faceplate has no XG cages (the S3400 next door does)
		"GE0",    // sockets are numbered from 1
		"GE-3",   // and never negative
		"GE 12",  // an inner space is not a spelling anything utters
		"GE12x",  // a TAIL: this used to read as port 12 and write to "0/12"
		"GE12.5", // likewise
		"GE1e3",  // likewise
		"GE1_2",  // likewise
		// The one that makes the tail dangerous rather than merely sloppy: a
		// stacked-faceplate name from another range used to come back as port 1
		// here, and cliPortName addressed "0/1" — a real jack on this box.
		"GE1/0/2",
		"GE99999999999999999999", // out of range, refused rather than truncated
	}
	var s string
	for _, s = range refused {
		var n, _, got = splitStandard(s)
		if got {
			t.Errorf("splitStandard(%q) = %d,true — this name must be refused: its `ok` "+
				"is what an apply writes on", s, n)
		}
	}
}

// A refused name must not be silently turned into a socket address either:
// cliPortName hands back the name UNCHANGED, so the CLI refuses "interface
// GE12x" loudly instead of the connector quietly reconfiguring 0/12.
func TestCliPortNameDoesNotRetargetARefusedName(t *testing.T) {
	var bad = []string{"GE12x", "GE1/0/2", "XG3", "0/12"}
	var s string
	for _, s = range bad {
		var got = cliPortName(s)
		if got != s {
			t.Errorf("cliPortName(%q) = %q — a name this platform does not utter must "+
				"travel unchanged, never be resolved to a jack", s, got)
		}
	}
}

func TestFingerprint(t *testing.T) {
	var d = &Driver{}
	if d.Fingerprint(driver.Evidence{ShellOut: "(UBNT EdgeSwitch) #"}) != 88 {
		t.Error("the EdgeSwitch prompt must score high")
	}
	if d.Fingerprint(driver.Evidence{SysDescr: "Ubiquiti Networks"}) != 45 {
		t.Error("a bare Ubiquiti clue is weak — it also covers EdgeRouter and APs")
	}
	if d.Fingerprint(driver.Evidence{ShellOut: "Cisco IOS Software"}) != 0 {
		t.Error("foreign platforms must not match")
	}
}

func TestCapabilitiesAndResetAreHonest(t *testing.T) {
	var caps = (&Driver{}).Capabilities("US-16-150W")
	// VLAN 1 is reserved and the creatable range stops at 4093 — a plan
	// carrying 4094 must be refused rather than silently dropped.
	if caps.VlanRange != [2]int{1, 4093} {
		t.Errorf("vlan range=%v — this platform stops at 4093", caps.VlanRange)
	}
	if caps.Mgmt.ApplySameVLAN {
		t.Error("nothing has been run on hardware: ApplySameVLAN must stay false")
	}
	if len(caps.Monitoring.Contradictions()) != 0 {
		t.Fatalf("contradictory monitoring: %v", caps.Monitoring.Contradictions())
	}

	var p = (&Driver{}).ResetProcedure("US-16-150W")
	if p.Confirmed {
		t.Error("nothing captured on hardware: Confirmed must be false")
	}
	var pitfalls = strings.Join(p.Pitfalls, " ")
	// The two facts most likely to be assumed wrong from Cisco habits.
	if !strings.Contains(pitfalls, "PRIVILEGED EXEC") {
		t.Error("the mode trap must be stated: network/vlan database are privileged EXEC")
	}
	if !strings.Contains(pitfalls, "not an SVI") {
		t.Error("management-is-not-an-SVI must be stated")
	}
}

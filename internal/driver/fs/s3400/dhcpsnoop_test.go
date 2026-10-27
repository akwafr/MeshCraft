package s3400

// DHCP snooping on the S3400 — the read parser, the pure command builders, and
// the whole apply gesture replayed against an in-process stand-in for the CLI.
//
// # THE PROVENANCE OF EVERY FIXTURE IN THIS FILE, SAID BEFORE ANYTHING ELSE
//
// Exactly ONE fixture here is CAPTURED: labRunningConfig (parse_test.go), the
// verbatim "show running-config" of the 2026-08-03 lab session. It is used for
// the case that matters most on this platform — a device that has never had
// snooping configured — and it carries no DHCP-snooping line, which is precisely
// why it can prove the "feature off" read.
//
// EVERY OTHER FIXTURE BELOW IS CONSTRUCTED, by hand, from the command forms the
// dhcpsnoop.go header names ("ip dhcp-relay snooping", "… vlan N",
// "… trust", and the IOS-shaped second candidate). NOT ONE of them was
// captured from an S3400, and the connector ships with
// WritesExtra("dhcp_snooping") false. Each constructed literal repeats the
// warning at its own declaration, so a later reader cannot mistake one for a
// transcript.
//
// ONE THING HERE IS NO LONGER A CONSTRUCTION, and it is the one that shapes the
// whole file: the live probe of 2026-08-10 (live_snoopcapture_test.go) sent all
// three levels to the lab unit. The global and per-VLAN statements were accepted
// and read back; "ip dhcp-relay snooping trust" was answered "Unknown
// command", and the CLI's completion showed the level is missing rather than
// misspelled. So [snoopDevice] REFUSES the trust statement by default — that is
// what the firmware does — and the applier no longer has a builder for it.
//
// What these tests hold still is therefore mostly the CONTRACT
// (driver/dhcpsnoop.go): trust is not enable, the feature is never turned off, a
// VLAN found enabled is never removed, an unobserved port is absent rather than
// untrusted, a refusal is Skipped rather than an error, and a write is confirmed
// by a re-read. Plus one platform fact: the third level does not exist here, so
// every per-port intent is refused and nothing per-port ever reaches the wire.

import (
	"context"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- the constructed configurations ---------------------------------------

// snoopCfgRelay is CONSTRUCTED, NOT CAPTURED. It is the labRunningConfig shape
// (2026-08-03) with the BDCOM-lineage snooping statements the dhcpsnoop.go
// header documents added by hand. No S3400 has ever served these lines.
const snoopCfgRelay = `show running-config
Building configuration...

Current configuration:
!
!version 2.0.2J build 95262
!
spanning-tree mode rstp
!
ip dhcp-relay snooping
ip dhcp-relay snooping vlan 1-3,99
!
interface Null0
!
interface GigaEthernet0/1
 switchport pvid 200
!
interface GigaEthernet0/12
 ip dhcp-relay snooping trust
!
interface TGigaEthernet0/1
 switchport mode trunk
 ip dhcp-relay snooping trust
!
interface TGigaEthernet0/2
 switchport mode trunk
!
interface VLAN99
 ip address 192.0.2.3 255.255.0.0
!
Switch#`

// snoopCfgPlain is CONSTRUCTED, NOT CAPTURED: the same device configured with
// the IOS-shaped second candidate spelling, which the connector must adopt
// rather than imposing its own preference.
const snoopCfgPlain = `show running-config
Current configuration:
!
ip dhcp snooping
ip dhcp snooping vlan 99
!
interface GigaEthernet0/3
 ip dhcp snooping trust
!
Switch#`

// snoopCfgVlansOnly is CONSTRUCTED, NOT CAPTURED: per-VLAN lines with NO global
// statement. Whether this state can exist on the firmware is exactly what the
// unrun live survey would settle, so the parser reports it instead of resolving
// it.
const snoopCfgVlansOnly = `show running-config
Current configuration:
!
ip dhcp-relay snooping vlan 99
!
interface GigaEthernet0/1
!
Switch#`

// snoopCfgEnabledNoVlan is CONSTRUCTED, NOT CAPTURED: the feature on, covering
// nothing.
const snoopCfgEnabledNoVlan = `show running-config
Current configuration:
!
ip dhcp-relay snooping
!
interface GigaEthernet0/1
!
interface TGigaEthernet0/1
!
Switch#`

// --- read: the parser -----------------------------------------------------

// The CAPTURED configuration of a device that has never carried the feature.
// The two things it must produce: Enabled=false, and NO port entry at all —
// on this CLI an untrusted port and an unobserved port are the same silence,
// and the contract says the unobserved one is ABSENT, never reported false.
func TestParseDHCPSnoopingAbsentFromCapturedConfig(t *testing.T) {
	var st, family = parseDHCPSnooping(labRunningConfig)
	if st.Enabled {
		t.Error("the captured lab configuration carries no snooping statement: Enabled must be false")
	}
	if family != "" {
		t.Errorf("family=%q — nothing in the capture names a spelling", family)
	}
	if len(st.Ports) != 0 {
		t.Errorf("Ports=%+v — with the feature off, \"untrusted\" and \"not observed\" are "+
			"the same silence, and the contract says report NOTHING rather than a false flag",
			st.Ports)
	}
	if len(st.VLANs) != 0 {
		t.Errorf("VLANs=%v", st.VLANs)
	}
	if len(st.Notes) == 0 {
		t.Fatal("no note: an empty read that looks like \"the feature is off\" must SAY why")
	}
	// The note has to name both spellings, otherwise it reads as "this device
	// has snooping off" when it might mean "we searched for the wrong word".
	var joined = strings.Join(st.Notes, " ")
	if !strings.Contains(joined, snoopFamilyRelay) || !strings.Contains(joined, snoopFamilyPlain) {
		t.Errorf("notes must name both surveyed spellings, got %q", joined)
	}
}

// The constructed relay-family configuration: all three levels read, the VLAN
// list expanded and sorted, and per-port trust reported only for the ports the
// configuration actually printed.
func TestParseDHCPSnoopingRelayFamily(t *testing.T) {
	var st, family = parseDHCPSnooping(snoopCfgRelay)
	if !st.Enabled {
		t.Fatal("\"ip dhcp-relay snooping\" is on its own line: Enabled must be true")
	}
	if family != snoopFamilyRelay {
		t.Errorf("family=%q, want the device's own spelling %q", family, snoopFamilyRelay)
	}
	if !reflect.DeepEqual(st.VLANs, []int{1, 2, 3, 99}) {
		t.Errorf("VLANs=%v — \"vlan 1-3,99\" must expand, sort and de-duplicate", st.VLANs)
	}
	// GE before XG, numeric inside each — the faceplate order, never map order.
	var want = []driver.DHCPSnoopPortState{
		{Port: "GE1", Trusted: false},
		{Port: "GE12", Trusted: true},
		{Port: "XG1", Trusted: true},
		{Port: "XG2", Trusted: false},
	}
	if !reflect.DeepEqual(st.Ports, want) {
		t.Errorf("Ports=%+v\nwant %+v", st.Ports, want)
	}
	// The SVI and Null0 are not faceplate sockets and must never be filed as
	// ports (an inventory port invented out of a configuration block).
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if strings.HasPrefix(p.Port, "VLAN") || p.Port == "Null0" {
			t.Errorf("%q must not appear among the ports", p.Port)
		}
	}
}

// THE DISTINCTION THE CONTRACT INSISTS ON: a port the configuration never
// mentioned is ABSENT from Ports. A connector that padded the list to fifty-two
// entries would report forty-eight ports as untrusted on the strength of having
// read nothing about them.
func TestParseDHCPSnoopingUnreadPortIsAbsentNotUntrusted(t *testing.T) {
	var st, _ = parseDHCPSnooping(snoopCfgPlain)
	if !st.Enabled {
		t.Fatal("Enabled must be true")
	}
	// The configuration printed ONE interface block. Anything else in the list
	// was manufactured.
	if len(st.Ports) != 1 || st.Ports[0].Port != "GE3" || !st.Ports[0].Trusted {
		t.Fatalf("Ports=%+v — want exactly the one port the configuration printed", st.Ports)
	}
	var absent = []string{"GE1", "GE2", "GE4", "GE48", "XG1", "XG4"}
	var byName = map[string]driver.DHCPSnoopPortState{}
	var i int
	for i = range st.Ports {
		byName[st.Ports[i].Port] = st.Ports[i]
	}
	var name string
	for _, name = range absent {
		var p, found = byName[name]
		if found {
			t.Errorf("%s was never read and must be ABSENT from Ports, got %+v — "+
				"\"not read\" and \"untrusted\" call for different things on screen", name, p)
		}
	}
}

// The device's own dialect wins over this file's preference: an apply that spoke
// the relay spelling to a plain-spelling device would configure nothing.
func TestParseDHCPSnoopingAdoptsTheDevicesSpelling(t *testing.T) {
	var _, family = parseDHCPSnooping(snoopCfgPlain)
	if family != snoopFamilyPlain {
		t.Errorf("family=%q, want %q", family, snoopFamilyPlain)
	}
}

// Per-VLAN lines with no global one: reported as read, with a note, and NOT
// resolved into Enabled=true by guessing.
func TestParseDHCPSnoopingVLANsWithoutGlobalStatement(t *testing.T) {
	var st, family = parseDHCPSnooping(snoopCfgVlansOnly)
	if st.Enabled {
		t.Error("no global statement appears: Enabled must stay false rather than be inferred")
	}
	if !reflect.DeepEqual(st.VLANs, []int{99}) {
		t.Errorf("VLANs=%v — the per-VLAN line was read and must be reported", st.VLANs)
	}
	if family != snoopFamilyRelay {
		t.Errorf("family=%q — the per-VLAN line names the spelling", family)
	}
	if len(st.Ports) != 0 {
		t.Errorf("Ports=%+v — must stay empty while the feature does not read as on", st.Ports)
	}
	if len(st.Notes) < 2 {
		t.Errorf("notes=%v — the VLAN-without-global case needs its own sentence", st.Notes)
	}
}

// The contract's own words: VLANs empty must never be left to read as "none".
func TestParseDHCPSnoopingEnabledWithNoVLANIsExplained(t *testing.T) {
	var st, _ = parseDHCPSnooping(snoopCfgEnabledNoVlan)
	if !st.Enabled || len(st.VLANs) != 0 {
		t.Fatalf("state=%+v", st)
	}
	if !strings.Contains(strings.ToLower(strings.Join(st.Notes, " ")), "no vlan") {
		t.Errorf("notes=%v — an empty VLAN list on a device that HAS a per-VLAN dimension "+
			"must be explained, not left to read as \"none\"", st.Notes)
	}
}

// "ip dhcp-relay snooping vlan 99" is not the global statement. A regex that
// let it through would report the feature on because one VLAN was named.
func TestParseDHCPSnoopingGlobalStatementIsAnchored(t *testing.T) {
	var cases = []string{
		// CONSTRUCTED, NOT CAPTURED.
		"ip dhcp-relay snooping vlan 99\n",
		"ip dhcp-relay snooping trust\n",
		"ip dhcp-relay snooping information option\n",
		"no ip dhcp-relay snooping\n",
	}
	var cfg string
	for _, cfg = range cases {
		var st, _ = parseDHCPSnooping(cfg)
		if st.Enabled {
			t.Errorf("%q must not read as the global enable", strings.TrimSpace(cfg))
		}
	}
	var padded, _ = parseDHCPSnooping("  ip dhcp-relay snooping  \n")
	if !padded.Enabled {
		t.Error("the global statement with surrounding blanks must still be recognized")
	}
}

// A trust line only counts inside a faceplate interface block: at top level it
// is not a port's flag, and attributing it to the last port seen would trust a
// socket nobody named.
func TestParseDHCPSnoopingTrustBelongsToItsBlock(t *testing.T) {
	// CONSTRUCTED, NOT CAPTURED.
	var cfg = "ip dhcp-relay snooping\n" +
		"!\n" +
		"interface GigaEthernet0/1\n" +
		" switchport pvid 200\n" +
		"!\n" +
		"ip dhcp-relay snooping trust\n" +
		"!\n"
	var st, _ = parseDHCPSnooping(cfg)
	if len(st.Ports) != 1 || st.Ports[0].Port != "GE1" {
		t.Fatalf("Ports=%+v", st.Ports)
	}
	if st.Ports[0].Trusted {
		t.Error("a trust line OUTSIDE the interface block must not trust GE1 — the block " +
			"was closed by \"!\"")
	}
}

// Map iteration order must never leak into what a screen shows: the same bytes
// parsed twice must produce byte-identical state.
func TestParseDHCPSnoopingIsDeterministic(t *testing.T) {
	var first, firstFamily = parseDHCPSnooping(snoopCfgRelay)
	var i int
	for i = 0; i < 40; i++ {
		var again, againFamily = parseDHCPSnooping(snoopCfgRelay)
		if !reflect.DeepEqual(first, again) || firstFamily != againFamily {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, first, again)
		}
	}
}

func TestSortSnoopPortsOrdersTheFaceplate(t *testing.T) {
	var ps = []driver.DHCPSnoopPortState{
		{Port: "XG2"}, {Port: "GE48"}, {Port: "GE2"}, {Port: "XG1"}, {Port: "GE12"},
	}
	driver.SortByPortName(ps, snoopPortName, splitStandard)
	var got []string
	var i int
	for i = range ps {
		got = append(got, ps[i].Port)
	}
	// GE before XG, and NUMERIC inside each: "GE12" must not sort before "GE2".
	if !reflect.DeepEqual(got, []string{"GE2", "GE12", "GE48", "XG1", "XG2"}) {
		t.Errorf("order=%v", got)
	}
}

// --- the pure command builders --------------------------------------------

// TWO BUILDERS, and the third one's absence is asserted with them. There is no
// snoopTrustCmds any more: the per-port statement does not exist on this
// firmware ("Unknown command", 2026-08-10), so the connector has no way to
// build it and TestNoPlanCanStillEmitThePerPortTrustCommand holds that shut
// against the wire.
func TestSnoopCommandBuilders(t *testing.T) {
	if snoopGlobalCmd(snoopFamilyRelay) != "ip dhcp-relay snooping" {
		t.Errorf("global=%q", snoopGlobalCmd(snoopFamilyRelay))
	}
	if snoopVlanCmd(snoopFamilyPlain, 99) != "ip dhcp snooping vlan 99" {
		t.Errorf("vlan=%q", snoopVlanCmd(snoopFamilyPlain, 99))
	}
	// One command per VLAN, so a refusal names the id it refused.
	if snoopVlanCmd(snoopFamilyRelay, 4094) != "ip dhcp-relay snooping vlan 4094" {
		t.Errorf("vlan=%q", snoopVlanCmd(snoopFamilyRelay, 4094))
	}
}

// This file knows NO "no" form at all any more. It used to know exactly one —
// the per-port untrust — and that one went with the level it belonged to; a
// global or per-VLAN "no" would strip protection from the whole device and the
// contract forbids it.
func TestSnoopBuildersNeverDisableTheFeature(t *testing.T) {
	var lines []string
	var family string
	for _, family = range snoopFamilies {
		lines = append(lines, snoopGlobalCmd(family), snoopVlanCmd(family, 1),
			snoopVlanCmd(family, 4094))
	}
	var line string
	for _, line = range lines {
		if reSnoopForbiddenDisable.MatchString(line) {
			t.Errorf("%q disables snooping globally or on a VLAN — the contract forbids it", line)
		}
		if strings.HasPrefix(strings.ToLower(line), "no ") {
			t.Errorf("%q is a \"no\" form: this file builds none", line)
		}
	}
}

// reSnoopForbiddenDisable matches every form that would turn the feature off
// globally or drop a VLAN from it. "no … trust" is deliberately NOT matched:
// untrusting a port restores that port's protected default.
var reSnoopForbiddenDisable = regexp.MustCompile(
	`(?i)^\s*no\s+ip\s+dhcp(-relay)?\s+snooping\s*(vlan\b.*)?$`)

func TestSortedUniqueVLANs(t *testing.T) {
	var in = []int{99, 10, 99, 1, 10}
	var got = sortedUniqueVLANs(in)
	if !reflect.DeepEqual(got, []int{1, 10, 99}) {
		t.Errorf("got %v", got)
	}
	// The caller's slice must survive untouched: a plan is not scratch space,
	// and a mutated one would make the second identical gesture differ.
	if !reflect.DeepEqual(in, []int{99, 10, 99, 1, 10}) {
		t.Errorf("the plan's slice was mutated: %v", in)
	}
	if sortedUniqueVLANs(nil) != nil {
		t.Error("nil in, nil out")
	}
}

func TestHasVLANAndTrustWord(t *testing.T) {
	if !hasVLAN([]int{1, 99}, 99) || hasVLAN([]int{1, 99}, 2) || hasVLAN(nil, 1) {
		t.Error("hasVLAN")
	}
	if trustWord(true) != "trusted" || trustWord(false) != "untrusted" {
		t.Error("trustWord")
	}
}

// --- the CLI stand-in -----------------------------------------------------
//
// snoopDevice is a CONSTRUCTED in-process stand-in for the S3400 shell: it
// records every line the connector writes, INTERPRETS the snooping statements
// the dhcpsnoop.go header documents, and serves a "show running-config" built
// from what it interpreted. Nothing about it was captured from hardware — its
// value is not that it behaves like an S3400 (no session can vouch for that
// yet) but that it lets the apply's ORDER, its idempotence, its refusals and
// above all its verifying re-read be asserted end to end.
//
// The knobs are the failure modes the contract cares about: a device that
// accepts a form and configures nothing, one that refuses a port, one that only
// answers the second candidate spelling, and one whose re-read observes no port
// at all.

type snoopDevice struct {
	out chan byte

	// sent is every line the connector wrote, in order.
	sent []string

	// family is the ONLY snooping spelling this device understands; any other
	// gets "Unknown command", which is what reCLIError catches.
	family string
	// what the device has actually configured.
	globalOn bool
	vlans    map[int]bool
	trust    map[string]bool // keyed by CLI port name: g0/12, tg0/1
	// ports: the faceplate sockets whose "interface" block the running
	// configuration prints, in CLI spelling.
	ports []string

	// swallowGlobal: the global statement is ACCEPTED and configures nothing.
	swallowGlobal bool
	// acceptTrust: the per-port trust statement is ACCEPTED. FALSE by default,
	// because that is what the lab unit does — "ip dhcp-relay snooping trust"
	// answers "Unknown command" on OpCode 2.0.2J (dhcpsnoop.go's header). The
	// knob exists so a test can model the firmware this connector does NOT have
	// and still watch the connector refuse to speak to it.
	acceptTrust bool
	// hideInterfaces: the running configuration comes back WITHOUT any
	// interface block — a truncated read, which readSnooping tolerates.
	hideInterfaces bool
	// refuseSave: "write" is refused. The failure mode that must NOT fail the
	// apply — the running configuration really does carry the write.
	refuseSave bool
	// dieOnReadBack: the session goes away just before the VERIFICATION re-read.
	// Distinct from hideInterfaces, and the distinction is the whole point: there
	// the device answered and said nothing about the ports, here it did not
	// answer at all. "not observed" and "not asked" lead to the same
	// conclusion — nothing is confirmed — by two different routes, and only one
	// of them can put a sentence in Skipped about a named port.
	dieOnReadBack bool
	// dead: the channel is closed and the shell answers nothing more. A closed
	// session stays closed.
	dead bool
	// saved counts the accepted "write" commands, so a test can assert the
	// gesture reached flash exactly once.
	saved int
	// reads counts the "show running-config" answered, so dieOnReadBack can
	// let the PRE-read through and kill the one after the write.
	reads int

	line  strings.Builder
	stack []string
	iface string
}

// snoopNoFamily is a spelling nothing matches: a device that answers neither
// surveyed candidate.
const snoopNoFamily = "ip dhcp-nothing snooping"

func newSnoopDevice(family string) (*Client, *snoopDevice) {
	var d = &snoopDevice{
		out:    make(chan byte, 1<<20),
		family: family,
		vlans:  map[int]bool{},
		trust:  map[string]bool{},
		ports:  []string{"g0/1", "g0/12", "tg0/1", "tg0/2"},
	}
	var conn = &cliConn{out: d.out, stdin: d}
	var c = &Client{cli: conn, host: "192.0.2.3", known: true, privileged: true}
	return c, d
}

func (d *snoopDevice) Write(p []byte) (int, error) {
	var i int
	for i = 0; i < len(p); i++ {
		if p[i] == '\r' || p[i] == '\n' {
			var l = d.line.String()
			d.line.Reset()
			d.handle(l)
			continue
		}
		d.line.WriteByte(p[i])
	}
	return len(p), nil
}

func (d *snoopDevice) Close() error { return nil }

func (d *snoopDevice) handle(raw string) {
	var cmd = strings.TrimSpace(raw)
	d.sent = append(d.sent, cmd)
	if d.dead {
		return
	}
	switch {
	case cmd == "":
		d.reply(cmd, "")
	case cmd == "config":
		d.stack = append(d.stack, "config")
		d.reply(cmd, "")
	case cmd == "exit":
		if len(d.stack) > 0 {
			var top = d.stack[len(d.stack)-1]
			d.stack = d.stack[:len(d.stack)-1]
			if top == "interface" {
				d.iface = ""
			}
		}
		d.reply(cmd, "")
	case strings.HasPrefix(cmd, "interface "):
		d.iface = strings.TrimSpace(strings.TrimPrefix(cmd, "interface "))
		d.stack = append(d.stack, "interface")
		d.reply(cmd, "")
	case cmd == "show running-config":
		d.reads++
		if d.dieOnReadBack && d.reads > 1 {
			d.dead = true
			close(d.out)
			return
		}
		d.reply(cmd, d.runningConfig())
	case cmd == "write":
		// "write" is the verb on this platform and it answers "OK!" (captured
		// — cf. Client.Persist). A refusal answers the CLI's "Unknown command"
		// shape, which is what reCLIError catches.
		if d.refuseSave {
			d.replyUnknown(cmd)
			return
		}
		d.saved++
		d.reply(cmd, "OK!")
	default:
		d.snoopStatement(cmd)
	}
}

func (d *snoopDevice) snoopStatement(cmd string) {
	var negated = strings.HasPrefix(strings.ToLower(cmd), "no ")
	var body = cmd
	if negated {
		body = strings.TrimSpace(cmd[3:])
	}
	if !strings.HasPrefix(body, d.family) {
		d.replyUnknown(cmd)
		return
	}
	var rest = strings.TrimSpace(strings.TrimPrefix(body, d.family))
	switch {
	case rest == "":
		if negated {
			// Reachable only if the connector broke the contract; the test
			// asserting the transcript is what actually catches that.
			d.globalOn = false
			d.reply(cmd, "")
			return
		}
		if !d.swallowGlobal {
			d.globalOn = true
		}
		d.reply(cmd, "")
	case rest == "trust":
		// THE PLATFORM'S OWN ANSWER, captured 2026-08-10: there is no such
		// statement, so the CLI prints a caret and "Unknown command". A
		// connector that sends this configures nothing and learns nothing.
		if !d.acceptTrust {
			d.replyUnknown(cmd)
			return
		}
		if negated {
			delete(d.trust, d.iface)
		} else {
			d.trust[d.iface] = true
		}
		d.reply(cmd, "")
	case strings.HasPrefix(rest, "vlan "):
		var id, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(rest, "vlan ")))
		if err != nil {
			d.replyUnknown(cmd)
			return
		}
		if negated {
			delete(d.vlans, id)
		} else {
			d.vlans[id] = true
		}
		d.reply(cmd, "")
	default:
		d.replyUnknown(cmd)
	}
}

// runningConfig renders what the device would print. The shape follows the
// CAPTURED labRunningConfig; the snooping lines in it are CONSTRUCTED.
func (d *snoopDevice) runningConfig() string {
	var b strings.Builder
	b.WriteString("Building configuration...\n\nCurrent configuration:\n!\n")
	b.WriteString("!version 2.0.2J build 95262\n!\n")
	if d.globalOn {
		b.WriteString(d.family + "\n")
	}
	var ids []int
	var id int
	for id = range d.vlans {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id = range ids {
		b.WriteString(d.family + " vlan " + strconv.Itoa(id) + "\n")
	}
	b.WriteString("!\n")
	if !d.hideInterfaces {
		var p string
		for _, p = range d.ports {
			b.WriteString("interface " + snoopDeviceIfaceName(p) + "\n")
			if d.trust[p] {
				b.WriteString(" " + d.family + " trust\n")
			}
			b.WriteString("!\n")
		}
	}
	return b.String()
}

func snoopDeviceIfaceName(cli string) string {
	if strings.HasPrefix(cli, "tg0/") {
		return "TGigaEthernet0/" + strings.TrimPrefix(cli, "tg0/")
	}
	return "GigaEthernet0/" + strings.TrimPrefix(cli, "g0/")
}

func (d *snoopDevice) prompt() string {
	if len(d.stack) == 0 {
		return "Switch#"
	}
	if d.iface != "" {
		return "Switch_config_" + d.iface + "#"
	}
	return "Switch_config#"
}

func (d *snoopDevice) reply(echo, body string) {
	var b strings.Builder
	b.WriteString(echo + "\n")
	if body != "" {
		b.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			b.WriteString("\n")
		}
	}
	b.WriteString(d.prompt())
	d.emit(b.String())
}

// replyUnknown is what this CLI answers an unrecognized command: the echo, a
// caret under the offending token, then the sentence reCLIError catches.
func (d *snoopDevice) replyUnknown(echo string) {
	d.emit(echo + "\n^\nUnknown command\n" + d.prompt())
}

func (d *snoopDevice) emit(s string) {
	var i int
	for i = 0; i < len(s); i++ {
		d.out <- s[i]
	}
}

// --- read through the client ----------------------------------------------

func TestReadDHCPSnoopingReadsTheRunningConfiguration(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true
	d.vlans[99] = true
	d.trust["tg0/1"] = true

	var st, err = c.ReadDHCPSnooping(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !reflect.DeepEqual(d.sent, []string{"show running-config"}) {
		t.Errorf("sent=%v — the read is one \"show running-config\" and nothing else", d.sent)
	}
	if !st.Enabled || !reflect.DeepEqual(st.VLANs, []int{99}) {
		t.Errorf("state=%+v", st)
	}
	var byName = map[string]bool{}
	var i int
	for i = range st.Ports {
		byName[st.Ports[i].Port] = st.Ports[i].Trusted
	}
	if !byName["XG1"] || byName["GE1"] || len(st.Ports) != 4 {
		t.Errorf("ports=%+v", st.Ports)
	}
}

// --- apply ----------------------------------------------------------------

// THE GOLDEN ORDER, and it is the contract's own minus a level this platform
// does not have: global first, then the VLANs SORTED and DE-DUPLICATED, then the
// verifying re-read, then the save. NO PER-PORT COMMAND APPEARS ANYWHERE, and
// the transcript being asserted in full is what says so.
func TestApplyDHCPSnoopingOrderIsGlobalThenVLANs(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99, 10, 99},
	})
	if err != nil {
		t.Fatalf("apply: %v (skipped=%v)", err, res.Skipped)
	}
	var want = []string{
		"show running-config",
		"config", "ip dhcp-relay snooping", "exit",
		"config", "ip dhcp-relay snooping vlan 10", "exit",
		"config", "ip dhcp-relay snooping vlan 99", "exit",
		"show running-config",
		"write", // and LAST: the save never precedes the verifying re-read
	}
	if !reflect.DeepEqual(d.sent, want) {
		t.Errorf("transcript:\n%v\nwant:\n%v", d.sent, want)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v — this platform has no per-port level to write",
			res.PortsWritten)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped=%v — nothing was refused", res.Skipped)
	}
	// The gesture changed the whole device, and the operator must read that.
	var notes = strings.Join(res.Notes, " | ")
	if !strings.Contains(notes, "ENABLED globally") {
		t.Errorf("notes must say the global feature was turned on: %q", notes)
	}
	if !strings.Contains(notes, "EVERY port") {
		t.Errorf("notes must say the change reaches every port: %q", notes)
	}
	if res.Notes[0] != snoopUnverifiedNote {
		t.Errorf("the unverified-forms note must ride every result, got %q", res.Notes[0])
	}
	// And the device really carries it now.
	if !d.globalOn || !d.vlans[10] || !d.vlans[99] {
		t.Errorf("device state: global=%v vlans=%v", d.globalOn, d.vlans)
	}
}

// Two identical gestures must produce two identical request sequences AND two
// identical refusals: a map iteration leaking into either would make a diff
// between two runs impossible to read.
func TestApplyDHCPSnoopingIsDeterministic(t *testing.T) {
	var plan = driver.DHCPSnoopPlan{
		VLANs: []int{99, 10, 4094, 1, 10, 99},
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "XG2", Trusted: false},
			{Port: "GE12", Trusted: false},
			{Port: "GE1", Trusted: false},
			{Port: "XG1", Trusted: false},
		},
	}
	var first []string
	var firstSkipped []string
	var run int
	for run = 0; run < 8; run++ {
		var c, d = newSnoopDevice(snoopFamilyRelay)
		d.globalOn = true
		d.vlans[7] = true
		var res, err = c.ApplyDHCPSnooping(context.Background(), plan)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if run == 0 {
			first = d.sent
			firstSkipped = res.Skipped
			continue
		}
		if !reflect.DeepEqual(first, d.sent) {
			t.Fatalf("run %d differs:\n%v\n%v", run, d.sent, first)
		}
		if !reflect.DeepEqual(firstSkipped, res.Skipped) {
			t.Fatalf("run %d refuses differently:\n%v\n%v", run, res.Skipped, firstSkipped)
		}
	}
	// And the refusals come out in faceplate order, GE before XG and numeric
	// inside each — the order driver.SortByPortName gives every other per-port
	// list on this platform.
	var skipped, wantsTrust = snoopTrustSkipped(plan.Ports)
	if len(skipped) != 4 {
		t.Fatalf("four intents, four refusals: %v", skipped)
	}
	if !strings.HasPrefix(skipped[0], "GE1:") || !strings.HasPrefix(skipped[1], "GE12:") ||
		!strings.HasPrefix(skipped[2], "XG1:") || !strings.HasPrefix(skipped[3], "XG2:") {
		t.Errorf("refusals are not in faceplate order: %v", skipped)
	}
	if wantsTrust {
		t.Error("not one intent asked for TRUST: the arming guard must not fire")
	}
}

// NEVER remove a VLAN that was already there: snooping this connector did not
// install is protection it must not take away.
func TestApplyDHCPSnoopingNeverRemovesAVLANItFound(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true
	d.vlans[5] = true
	d.vlans[7] = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !d.vlans[5] || !d.vlans[7] || !d.vlans[99] {
		t.Errorf("vlans=%v — 5 and 7 were found enabled and must survive", d.vlans)
	}
	var line string
	for _, line = range d.sent {
		if reSnoopForbiddenDisable.MatchString(line) {
			t.Errorf("the transcript carries %q", line)
		}
	}
	// Only the missing VLAN is written: 5 and 7 cost no round trip.
	if snoopCount(d.sent, "ip dhcp-relay snooping vlan ") != 1 {
		t.Errorf("transcript=%v — only VLAN 99 was missing", d.sent)
	}
	if !strings.Contains(strings.Join(res.Notes, " | "), "already covered") {
		t.Errorf("notes=%v — what was left alone must be said", res.Notes)
	}
}

// The feature is NEVER turned off, not even when every port in the plan is
// being untrusted and nothing trusted is left.
func TestApplyDHCPSnoopingNeverTurnsTheFeatureOff(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true
	d.vlans[99] = true
	d.trust["g0/12"] = true
	d.trust["tg0/1"] = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE12", Trusted: false},
			{Port: "XG1", Trusted: false},
		},
	})
	if err != nil {
		t.Fatalf("apply: %v (skipped=%v)", err, res.Skipped)
	}
	var line string
	for _, line = range d.sent {
		if reSnoopForbiddenDisable.MatchString(line) {
			t.Fatalf("the transcript carries %q — the last trusted port going away must "+
				"never strip the whole device", line)
		}
	}
	if !d.globalOn || !d.vlans[99] {
		t.Errorf("device left with global=%v vlans=%v", d.globalOn, d.vlans)
	}
	// The two flags this constructed device carries are left EXACTLY as they were.
	// Untrusting them would be the per-port level, and on this firmware there is
	// no command for it in either direction — a "no … trust" is the same absent
	// statement facing the other way.
	if !d.trust["g0/12"] || !d.trust["tg0/1"] {
		t.Errorf("trust=%v — nothing per-port may be written, not even to clear a flag",
			d.trust)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v — nothing was written per port", res.PortsWritten)
	}
	if len(res.Skipped) != 2 {
		t.Errorf("Skipped=%v — both intents must be reported, in either direction",
			res.Skipped)
	}
}

// --- the refusal of the level this platform does not have ------------------

// THE ABSENCE TEST, and it names the exact string on purpose.
//
// "ip dhcp-relay snooping trust" is what this connector emitted until
// 2026-08-10 and what the lab unit answers "Unknown command" to. No plan, in
// either direction and in either spelling, may put it on the wire again. The
// assertion is on the TRANSCRIPT rather than on the source, so it holds however
// the command might be rebuilt — a helper, a format string, a fallback ladder.
func TestNoPlanCanStillEmitThePerPortTrustCommand(t *testing.T) {
	var plans = []driver.DHCPSnoopPlan{
		{Ports: []driver.DHCPSnoopPortIntent{{Port: "GE12", Trusted: true}}},
		{Ports: []driver.DHCPSnoopPortIntent{{Port: "GE12", Trusted: false}}},
		{VLANs: []int{99}, Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE12", Trusted: true}, {Port: "XG1", Trusted: false}}},
		{VLANs: []int{99}, Ports: []driver.DHCPSnoopPortIntent{{Port: "XG1", Trusted: false}}},
	}
	var i int
	for i = range plans {
		// A device that ALREADY carries the two levels the plan asks for, so the
		// apply gets as far as it possibly can before deciding about the ports.
		var c, d = newSnoopDevice(snoopFamilyRelay)
		d.globalOn = true
		d.vlans[99] = true
		var _, err = c.ApplyDHCPSnooping(context.Background(), plans[i])
		if err != nil {
			t.Fatalf("plan %d: %v", i, err)
		}
		var line string
		for _, line = range d.sent {
			var low = strings.ToLower(line)
			if strings.Contains(low, "snooping trust") {
				t.Fatalf("plan %d put the dead command on the wire — \"%s\". It is answered "+
					"\"Unknown command\" on OpCode 2.0.2J and configures nothing", i, line)
			}
			if strings.HasPrefix(low, "interface ") {
				t.Fatalf("plan %d entered an interface block for DHCP snooping (\"%s\"): "+
					"there is nothing per-port to write here", i, line)
			}
		}
	}
}

// The refusal fires, it is in Skipped, and it is NOT a Go error: a level the
// hardware does not carry is an ordinary outcome, and driver/dhcpsnoop.go says
// where an ordinary refusal goes.
func TestPerPortTrustIsRefusedIntoSkipped(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true
	d.vlans[99] = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE12", Trusted: true},
			{Port: "XG1", Trusted: false},
		},
	})
	if err != nil {
		t.Fatalf("a platform that lacks a level is not a device that failed: %v", err)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("Skipped=%v — both intents must be reported: a plan asking for something "+
			"impossible has to SAY so, and accepting it silently would be worse than the bad "+
			"command it replaced", res.Skipped)
	}
	if !snoopHasSub(res.Skipped, "GE12") || !snoopHasSub(res.Skipped, "XG1") {
		t.Errorf("Skipped=%v — each refusal names its port", res.Skipped)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v — nothing per-port was sent, so nothing is confirmed",
			res.PortsWritten)
	}
}

// The refusal names the PLATFORM FACT rather than a failure. An operator reading
// it must be able to tell "this switch does not have this" from "meshcraft could
// not do it" — the first calls for a different plan, the second for a retry.
func TestTheRefusalNamesThePlatformAndNotAFailure(t *testing.T) {
	var skipped, wantsTrust = snoopTrustSkipped([]driver.DHCPSnoopPortIntent{
		{Port: "GE12", Trusted: true},
	})
	if len(skipped) != 1 || !wantsTrust {
		t.Fatalf("one intent asking for trust: %v / %v", skipped, wantsTrust)
	}
	var got = skipped[0]
	var must = []string{
		"GE12",                      // which port
		"NOT set trusted",           // and what it asked for
		"NO PER-PORT DHCP SNOOPING", // what is missing
		"ABSENT from this firmware", // and that it is the firmware, not a bug
		"Unknown command",           // the observation itself
		"OpCode 2.0.2J",             // on which release
		"2026-08-10",                // and when
		"upper_filter",              // where the platform keeps its nearest thing
	}
	var want string
	for _, want = range must {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal does not carry %q:\n%s", want, got)
		}
	}
	// And it must not read as a device that misbehaved, which would send an
	// operator looking for a fault that does not exist.
	var forbidden = []string{"try again", "timed out", "the device refused"}
	for _, want = range forbidden {
		if strings.Contains(strings.ToLower(got), want) {
			t.Errorf("the refusal reads as a failure (%q):\n%s", want, got)
		}
	}
}

// BOTH DIRECTIONS are refused, and that is deliberate. "Untrusted" is the
// permanent state of every port here, so granting the intent silently would be
// harmless in effect and wrong in principle: the plan asked for a level, and the
// contract forbids accepting an intent and reporting success.
func TestUntrustingIsRefusedToo(t *testing.T) {
	var skipped, wantsTrust = snoopTrustSkipped([]driver.DHCPSnoopPortIntent{
		{Port: "GE12", Trusted: false},
	})
	if len(skipped) != 1 || !strings.Contains(skipped[0], "NOT set untrusted") {
		t.Fatalf("an untrust intent must be refused and say which direction it asked for: %v",
			skipped)
	}
	if wantsTrust {
		t.Error("no intent asked for TRUST: the arming guard must not fire on this plan")
	}
}

// --- the arming guard -----------------------------------------------------

// THE CASE THAT WOULD DO HARM, and the reason the per-port refusal is not the
// whole answer: a plan that wants an uplink TRUSTED and would also have to turn
// inspection on. The VLANs it arms are the ones crossing that uplink, and with
// no exemption available the switch would start dropping the DHCP OFFER/ACK the
// server answers with. Nothing is written.
func TestArmingIsRefusedWhenThePlanWantedATrustedPort(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "XG1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("this is a refusal, not a failure: %v", err)
	}
	if !reflect.DeepEqual(d.sent, []string{"show running-config"}) {
		t.Fatalf("NOTHING may be written for this plan; transcript=%v", d.sent)
	}
	if d.globalOn || len(d.vlans) != 0 {
		t.Errorf("the device was armed anyway: global=%v vlans=%v", d.globalOn, d.vlans)
	}
	var joined = strings.Join(res.Skipped, " | ")
	if !strings.Contains(joined, "NOT armed") {
		t.Errorf("Skipped must say the feature was not armed and why: %q", joined)
	}
	if !strings.Contains(joined, "outage in place of the protection") {
		t.Errorf("the refusal must name what it prevented: %q", joined)
	}
	if !strings.Contains(joined, "XG1") {
		t.Errorf("the port intent must still be reported on its own: %q", joined)
	}
}

// The guard is NARROW. A plan that only asks for untrusted ports is asking for
// exactly the two levels this platform has, and arming them is what the operator
// wants — the refusals ride along in Skipped and change nothing else.
func TestArmingProceedsForAnUntrustOnlyPlan(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "XG1", Trusted: false}},
	})
	if err != nil {
		t.Fatalf("apply: %v (skipped=%v)", err, res.Skipped)
	}
	if !d.globalOn || !d.vlans[99] {
		t.Errorf("the two levels this platform has must still be armed: global=%v vlans=%v",
			d.globalOn, d.vlans)
	}
	if !snoopHasSub(res.Skipped, "XG1") {
		t.Errorf("Skipped=%v — the intent is still refused", res.Skipped)
	}
	if snoopHasSub(res.Skipped, "NOT armed") {
		t.Errorf("the arming guard fired on a plan that asked to TRUST nothing: %v", res.Skipped)
	}
}

// And it does not fire on a switch ALREADY snooping those VLANs: there is
// nothing to arm, and the state it would have refused to create is one this
// apply did not create. Refusing there would withhold nothing and report a
// decision nobody made.
func TestArmingGuardDoesNotFireOnAnAlreadyArmedSwitch(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true
	d.vlans[99] = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "XG1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if snoopHasSub(res.Skipped, "NOT armed") {
		t.Errorf("nothing had to be armed on this switch: %v", res.Skipped)
	}
	if !snoopHasSub(res.Skipped, "XG1") {
		t.Errorf("the trust intent is refused all the same: %v", res.Skipped)
	}
	// Nothing was sent and nothing was saved: the device already carried both
	// levels, and the third does not exist.
	if !reflect.DeepEqual(d.sent, []string{"show running-config", "show running-config"}) {
		t.Errorf("transcript=%v — a read, the verification re-read, and no write", d.sent)
	}
	if d.saved != 0 {
		t.Errorf("\"write\" was sent %d time(s) for a gesture that changed nothing", d.saved)
	}
}

// A name this platform does not utter is refused before anything is sent, and it
// is refused on the ground that outlives the name: there is no per-port level
// here for any spelling of any port.
func TestApplyDHCPSnoopingUnknownPortNameIsSkipped(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "ether3", Trusted: false},
			{Port: "GE12", Trusted: false},
		},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !snoopHasSub(res.Skipped, "ether3") || !snoopHasSub(res.Skipped, "GE12") {
		t.Errorf("Skipped=%v — both names are reported", res.Skipped)
	}
	var line string
	for _, line = range d.sent {
		if strings.Contains(line, "ether3") {
			t.Errorf("\"%s\" was sent to the device", line)
		}
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v", res.PortsWritten)
	}
}

// The second candidate spelling: the relay form is tried first, refused, and
// everything below speaks the form the device accepted.
func TestApplyDHCPSnoopingFallsBackToTheSecondSpelling(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyPlain)

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
	})
	if err != nil {
		t.Fatalf("apply: %v (skipped=%v)", err, res.Skipped)
	}
	var joined = strings.Join(d.sent, "\n")
	if !strings.Contains(joined, "\nip dhcp-relay snooping\n") {
		t.Errorf("the relay form must be TRIED first: %v", d.sent)
	}
	if !strings.Contains(joined, "\nip dhcp snooping vlan 99\n") {
		t.Errorf("the VLAN command must speak the accepted form: %v", d.sent)
	}
	if strings.Contains(joined, "ip dhcp-relay snooping vlan") {
		t.Errorf("the refused spelling must not be used below the global enable: %v", d.sent)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v", res.PortsWritten)
	}
}

// Neither spelling accepted: nothing below would work, so the VLAN statements
// are not written either, and the gesture fails loudly rather than reporting an
// exact configuration that protects nothing.
//
// The plan's ports ask to be UNTRUSTED, so the arming guard stays out of the way
// and this test still reaches the candidate ladder it is about — with a trusted
// port the apply would refuse before trying either spelling, which is what
// TestArmingIsRefusedWhenThePlanWantedATrustedPort covers.
func TestApplyDHCPSnoopingRefusesEverythingWhenGlobalIsRefused(t *testing.T) {
	var c, d = newSnoopDevice(snoopNoFamily)

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE12", Trusted: false},
			{Port: "XG1", Trusted: false},
		},
	})
	if err == nil {
		t.Fatal("a device on which the feature cannot be enabled must not report success")
	}
	if !snoopHasSub(res.Skipped, "GE12") || !snoopHasSub(res.Skipped, "XG1") {
		t.Errorf("Skipped=%v — every port of the plan must be named", res.Skipped)
	}
	if !snoopHasSub(res.Skipped, "global DHCP snooping not enabled") {
		t.Errorf("Skipped=%v — the global refusal must be named too", res.Skipped)
	}
	var line string
	for _, line = range d.sent {
		if strings.Contains(line, "trust") || strings.Contains(line, "vlan") {
			t.Errorf("\"%s\" was sent although the feature is off", line)
		}
	}
	// Both candidates were really tried, and only once each.
	if snoopCount(d.sent, "ip dhcp-relay snooping") != 1 ||
		snoopCount(d.sent, "ip dhcp snooping") != 1 {
		t.Errorf("transcript=%v — each candidate is tried exactly once", d.sent)
	}
}

// A command this CLI ACCEPTS and ignores is the reason the re-read exists: the
// global statement is absent afterwards, so the apply says so instead of
// reporting an applied configuration.
func TestApplyDHCPSnoopingCatchesAGlobalThatConfiguredNothing(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.swallowGlobal = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
	})
	if err == nil {
		t.Fatal("the global statement is absent on re-read: this must not be a success")
	}
	if !snoopHasSub(res.Skipped, "accepted and configured nothing") {
		t.Errorf("Skipped=%v", res.Skipped)
	}
	if snoopCount(d.sent, "show running-config") != 2 {
		t.Errorf("transcript=%v — the write is checked by a RE-READ", d.sent)
	}
}

// A TRUNCATED re-read observes the feature and NO port. Nothing per-port was
// sent, so there is nothing to confirm and nothing to complain about — and the
// apply must not invent a complaint out of a silence it did not depend on.
func TestApplyDHCPSnoopingToleratesAReReadWithNoInterfaceBlock(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true
	// The verification re-read observes the feature but NO port (a truncated
	// "show running-config", which readSnooping tolerates by design).
	d.hideInterfaces = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE12", Trusted: false}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v", res.PortsWritten)
	}
	// ONE entry, and it is the platform refusal — not a second one about a port
	// the re-read did not print.
	if len(res.Skipped) != 1 || !snoopHasSub(res.Skipped, "NO PER-PORT DHCP SNOOPING") {
		t.Errorf("Skipped=%v — the only refusal here is the level that does not exist",
			res.Skipped)
	}
	if !d.vlans[99] {
		t.Error("the per-VLAN level must still have been written")
	}
}

// THE ITEM TODO.md FILED UNDER THIS CONNECTOR, settled fleet-wide on
// 2026-08-09: a verification re-read that FAILED confirms nothing.
//
// The applier used to leave every accepted port in PortsWritten under a note
// reading "none of it was confirmed" — the field and the prose making opposite
// claims. There is no per-port half left to contradict itself, and what survives
// is the same discipline one level up: the global and per-VLAN statements went
// out, the re-read died, and the result says SENT-not-confirmed rather than
// reporting an applied configuration.
func TestApplyDHCPSnoopingConfirmsNothingWhenTheReReadNeverLanded(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.dieOnReadBack = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
	})
	if err != nil {
		t.Fatalf("a failed verification is not a failed apply: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten=%v — the device was never asked, so nothing is confirmed; "+
			"the apply event downstream reads \"N port(s) written\"", res.PortsWritten)
	}
	// A re-read that failed is a fact, not a decision: Notes, never Skipped, or
	// the screen offers a "force" for a session that dropped.
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped=%v — a re-read that failed is a fact, not a decision", res.Skipped)
	}
	if !snoopHasSub(res.Notes, "NOT confirmed") {
		t.Fatalf("Notes=%v — the gesture went out and was not verified, and that has to be "+
			"said", res.Notes)
	}
	// And nothing was baked into flash: the save is step 5 and it comes after the
	// verification, so a re-read that died takes the "write" with it.
	if d.saved != 0 {
		t.Fatalf("\"write\" was sent %d time(s) after a verification that never happened",
			d.saved)
	}
}

// A VLAN outside the range is refused ONCE, with the reason, and never sent.
// Naming it a second time as "absent from the running configuration after the
// write" would suggest it was tried.
func TestApplyDHCPSnoopingSkipsOutOfRangeVLANsExactlyOnce(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{0, 99, 4095},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	var line string
	for _, line = range d.sent {
		if strings.Contains(line, "vlan 0") || strings.Contains(line, "vlan 4095") {
			t.Errorf("\"%s\" was sent to the device", line)
		}
	}
	if snoopCountSub(res.Skipped, "4095") != 1 {
		t.Errorf("Skipped=%v — 4095 must be refused once, not reported again as \"absent "+
			"after the write\"", res.Skipped)
	}
	if snoopCountSub(res.Skipped, "vlan 0") != 1 {
		t.Errorf("Skipped=%v — same for 0", res.Skipped)
	}
	if !d.vlans[99] {
		t.Error("the valid VLAN must still be written")
	}
}

// The plan names no VLAN: the feature is on and inspects nothing, and an
// operator looking at a snooping screen deserves to be told.
func TestApplyDHCPSnoopingSaysWhenNoVLANIsCovered(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE12", Trusted: false}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !snoopHasSub(res.Notes, "inspects NOTHING") {
		t.Errorf("notes=%v", res.Notes)
	}
}

// A SAVE THAT FAILS DOES NOT FAIL THE APPLY, and the Note says what is lost.
//
// The inspection really is in the running configuration — the re-read two lines
// earlier confirmed it — so returning an error here would tell an operator the
// opposite of what the switch holds. What they lose is the reboot, and that is
// what the Note has to name rather than merely reporting that a command failed.
func TestApplyDHCPSnoopingFailedSaveIsANoteAndNotAFailure(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.globalOn = true
	d.refuseSave = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
	})
	if err != nil {
		t.Fatalf("a refused save must not fail the apply: the write landed and this claims "+
			"it did not — %v (skipped=%v)", err, res.Skipped)
	}
	if !d.vlans[99] {
		t.Error("the device really does carry the VLAN statement")
	}
	var notes = strings.Join(res.Notes, " | ")
	if !strings.Contains(notes, "NOT persisted") {
		t.Fatalf("the failed save must be reported in Notes: %q", notes)
	}
	if !strings.Contains(notes, "a reboot would take this DHCP snooping configuration with it") {
		t.Errorf("the Note must say what the operator LOSES — the reboot — and not merely "+
			"that a command failed: %q", notes)
	}
	// And it is a FAILED transcript step, not a silent one.
	var failed bool
	var s Step
	for _, s = range c.Steps {
		if !s.OK && strings.Contains(s.What, "write refused") {
			failed = true
		}
	}
	if !failed {
		t.Errorf("the refused save must be a failed transcript step: %+v", c.Steps)
	}
	if d.sent[len(d.sent)-1] != "write" {
		t.Errorf("the save is attempted LAST, after the verifying re-read: %v", d.sent)
	}
}

// A global statement the device accepted and did not configure aborts BEFORE
// the save: there is nothing worth putting in flash on a switch that inspects
// nothing, and the apply already returns an error for it.
func TestApplyDHCPSnoopingDoesNotSaveAGlobalThatConfiguredNothing(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	d.swallowGlobal = true

	var _, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{99},
	})
	if err == nil {
		t.Fatal("a global statement that configured nothing must be an error")
	}
	if d.saved != 0 {
		t.Errorf("nothing may be written to flash on a switch that inspects nothing: %v", d.sent)
	}
}

// A session that never reached privileged mode writes nothing at all.
func TestApplyDHCPSnoopingRefusesAnUnwritableSession(t *testing.T) {
	var c, d = newSnoopDevice(snoopFamilyRelay)
	c.privileged = false

	var _, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE12", Trusted: true}},
	})
	if err == nil {
		t.Fatal("an unprivileged session must be refused")
	}
	if len(d.sent) != 0 {
		t.Errorf("sent=%v — nothing may reach the device", d.sent)
	}
}

// --- small helpers --------------------------------------------------------

func snoopCount(lines []string, prefix string) int {
	var n int
	var l string
	for _, l = range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func snoopCountSub(lines []string, sub string) int {
	var n int
	var l string
	for _, l = range lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func snoopHasSub(lines []string, sub string) bool {
	return snoopCountSub(lines, sub) > 0
}

package eos

import (
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

func idx(cmds []string, want string) int {
	var i int
	for i = range cmds {
		if cmds[i] == want {
			return i
		}
	}
	return -1
}

func idxPrefix(cmds []string, prefix string) int {
	var i int
	for i = range cmds {
		if strings.HasPrefix(cmds[i], prefix) {
			return i
		}
	}
	return -1
}

// --- the reset, which is TWO gestures on this platform ---

// `reload` (without `now`) asks "Save?" whenever running and startup differ —
// which, one command after deleting the startup file, they always do — and the
// default answer writes the configuration straight back over the file just
// removed. That is the reset silently doing nothing.
func TestFactoryResetUsesReloadNow(t *testing.T) {
	var cmds = FactoryResetCmds()
	if idx(cmds, "reload now") < 0 {
		t.Fatalf("FactoryResetCmds = %v — \"reload now\" is what skips the \"Save?\" "+
			"question whose default undoes the reset", cmds)
	}
	if idx(cmds, "reload") >= 0 {
		t.Error("bare \"reload\" is in the sequence: its \"Save?\" default re-saves the " +
			"configuration that was just deleted")
	}
	if idx(cmds, "delete flash:startup-config") >= idx(cmds, "reload now") {
		t.Error("the startup-config must be deleted BEFORE the reload")
	}
}

// The two are separate on purpose: they are sent in different states, minutes
// and a reboot apart, and a caller that concatenated them would send the second
// into a CLI that is not there yet.
func TestZeroTouchCancelIsItsOwnGesture(t *testing.T) {
	if !reflect.DeepEqual(ZeroTouchCancelCmds(), []string{"zerotouch cancel"}) {
		t.Errorf("ZeroTouchCancelCmds = %v", ZeroTouchCancelCmds())
	}
	var reset = FactoryResetCmds()
	if idx(reset, "zerotouch cancel") >= 0 {
		t.Error("\"zerotouch cancel\" is inside FactoryResetCmds: it would be sent to a " +
			"console that has not rebooted into ZTP yet")
	}
}

// --- third-party optics ---

func TestUnsupportedTransceiverCmds(t *testing.T) {
	var cmds = UnsupportedTransceiverCmds()
	if len(cmds) != 2 {
		t.Fatalf("got %v — both lines are needed: one stops the refusal, the other "+
			"recovers the ports already sitting err-disabled", cmds)
	}
	if cmds[0] != "service unsupported-transceiver wiprolabs f5047577" {
		t.Errorf("cmds[0] = %q", cmds[0])
	}
	if cmds[1] != "errdisable recovery cause "+ErrDisableXcvr {
		t.Errorf("cmds[1] = %q", cmds[1])
	}
}

// --- the fan-speed override, and the banner it answers with ---

// CAPTURED 2026-08-06 on the lab switch. The command SUCCEEDED and printed this;
// the prompt came straight back afterwards.
const fanSpeedBanner = `localhost(config)#environment fan-speed override 30
====================================================================
WARNING: Overriding the system fan speed is unsupported and should only
be done under the direction of an Arista Networks engineer.
You can risk damaging hardware by setting the fan speed too low
and doing so without direction from Arista Networks can be grounds
for voiding your warranty.
To set the fan speed back to automatic mode, use the
'environment fan-speed auto' command
====================================================================
localhost(config)#exit
`

// The banner carries the word "unsupported" and four lines of warning, and it
// is NOT a refusal. Every refusal form in reCLIError is anchored on a leading
// "%" or "!", which is what keeps this from failing a provisioning run — this
// test is what makes that property deliberate instead of accidental, so a future
// widening of the pattern to catch bare words fails here rather than in a rack.
func TestFanSpeedBannerIsNotACLIError(t *testing.T) {
	var m = reCLIError.FindString(fanSpeedBanner)
	if m != "" {
		t.Fatalf("reCLIError matched %q in the fan-speed banner — every provisioning run "+
			"on this platform would report a failure on a command that succeeded", m)
	}
}

func TestFanSpeedIsInThePushWithTheOptics(t *testing.T) {
	var cmds = pushFixture()
	var fan = idx(cmds, FanSpeedOverrideCmd)
	if fan < 0 {
		t.Fatalf("the fan-speed override is missing from the push: %v", cmds)
	}
	// With the other "make this hardware usable in OUR racks" line, and before
	// the VLAN work — its four-line banner has no business landing in the middle
	// of the block an operator will actually read.
	if fan < idxPrefix(cmds, "service unsupported-transceiver") {
		t.Error("the fan-speed line comes before the optics: the optics must be first, " +
			"because a refused cage reads exactly like an empty one")
	}
	if fan > idx(cmds, "lldp run") {
		t.Errorf("the fan-speed line (%d) landed after the VLAN/LLDP work (%d)",
			fan, idx(cmds, "lldp run"))
	}
	if FanSpeedAutoCmd != "environment fan-speed auto" {
		t.Errorf("the undo command is %q — it is quoted in the banner the device "+
			"prints, and an operator reading a thermal incident will look for it",
			FanSpeedAutoCmd)
	}
}

// --- the management push ---

func pushFixture() []string {
	// The error channel carries ONE thing: a system name that refused
	// normalization. "sw lab 1" does not, so a non-nil error here means the
	// shared normalizer moved under the connectors — which is exactly what the
	// fixture should stop rather than hide.
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID:    99,
		VlanName:  "LAN99 Management",
		IP:        "192.0.2.5",
		PrefixLen: 16,
		Gateway:   "192.0.2.1",
		Username:  "mcadmin",
		Password:  "s3cret",
		Hostname:  "sw lab 1",
		SiteVLANs: []driver.VLAN{{ID: 30, Name: "Voice"}, {ID: 40, Name: "Data"}},
		TrunkPorts: []string{
			"Ethernet1", "Ethernet2", "Ethernet49",
		},
	})
	if err != nil {
		panic("pushFixture: a valid meshcraft name was refused: " + err.Error())
	}
	return cmds
}

// EOS takes the PREFIX form and refuses the dotted mask IOS demands. This is the
// single line where a command set shared with the Cisco connector would be
// silently rejected mid-block.
func TestMgmtPushWritesThePrefixForm(t *testing.T) {
	var cmds = pushFixture()
	if idx(cmds, "ip address 192.0.2.5/16") < 0 {
		t.Fatalf("the SVI address is not in prefix form: %v", cmds)
	}
	if idxPrefix(cmds, "ip address 192.0.2.5 255.") >= 0 {
		t.Error("the dotted mask form was written: EOS refuses it")
	}
}

// There is no "ip default-gateway" on this platform; the IOS reflex produces a
// rejected line and a switch with no way out of its subnet.
func TestMgmtPushWritesARouteNotADefaultGateway(t *testing.T) {
	var cmds = pushFixture()
	if idx(cmds, "ip route 0.0.0.0/0 192.0.2.1") < 0 {
		t.Fatalf("the default route is missing: %v", cmds)
	}
	if idxPrefix(cmds, "ip default-gateway") >= 0 {
		t.Error("\"ip default-gateway\" was written: that command does not exist on EOS")
	}
}

// The ORDER is load-bearing, and each of these four is a way for the run to fail
// in the quiet direction.
func TestMgmtPushOrder(t *testing.T) {
	var cmds = pushFixture()

	var optics = idxPrefix(cmds, "service unsupported-transceiver")
	if optics != 0 {
		t.Errorf("the transceiver line is at %d, not first: a refused cage reads exactly "+
			"like an empty one, so uplink discovery would find nothing", optics)
	}

	var vlan30 = idx(cmds, "vlan 30")
	var trunk = idxPrefix(cmds, "switchport trunk allowed vlan")
	if vlan30 < 0 || trunk < 0 || vlan30 > trunk {
		t.Errorf("VLANs (%d) must be created before a trunk is told to carry them (%d)",
			vlan30, trunk)
	}

	var svi = idx(cmds, "interface Vlan99")
	if trunk > svi {
		t.Errorf("the trunks (%d) must exist before the SVI gets its address (%d), or "+
			"management has an address and no path to answer on", trunk, svi)
	}

	// CREDENTIALS LAST: the console session can drop right at the password change
	// of the account currently logged in, and everything else must already be in
	// place when it does.
	var user = idxPrefix(cmds, "username mcadmin")
	var last = len(cmds) - 1
	if user < 0 || user < svi {
		t.Errorf("credentials at %d, SVI at %d — credentials come last", user, svi)
	}
	// NO ENABLE PASSWORD, by fleet rule — and APPLIED rather than merely omitted:
	// not writing it would leave the rule true on a factory switch and false on
	// every switch that already carried one, which is exactly the fleet where it
	// matters.
	if idxPrefix(cmds, "enable secret") >= 0 || idxPrefix(cmds, "enable password") >= 0 {
		t.Errorf("an enable password was written: %v", cmds)
	}
	// The enable rule lands AFTER the account, which is the invariant that matters
	// — the device is never momentarily without a way in.
	//
	// Asserted as an ORDERING and no longer as "the last command", because it is
	// no longer last: the push now ends by persisting itself (`end` +
	// [SaveCmd]), without which the entire run lives in the running
	// configuration and is gone at the next reload. Pinning this to the tail
	// would have made the save impossible to add without failing a test that was
	// never about the tail — it was about the account existing first.
	// "password", not "secret": EOS has no `enable secret` keyword, and the
	// IOS spelling comes back "% Invalid input" on hardware (cf. NoEnableCmds).
	var noEnable = idx(cmds, "no enable password")
	if noEnable < 0 {
		t.Fatalf("the \"no enable password\" rule is not applied at all: %v", cmds)
	}
	if noEnable < user {
		t.Errorf("\"no enable password\" at %d comes BEFORE the account at %d: the device "+
			"would be momentarily without a way in", noEnable, user)
	}
	if noEnable > last-2 {
		t.Errorf("\"no enable password\" at %d is not in the final block (last=%d): "+
			"only the save should follow it", noEnable, last)
	}
}

// An SVI created in configuration mode comes up administratively down.
func TestMgmtPushBringsTheSVIUp(t *testing.T) {
	var cmds = pushFixture()
	var svi = idx(cmds, "interface Vlan99")
	var i int
	for i = svi; i < len(cmds) && cmds[i] != "exit"; i++ {
		if cmds[i] == "no shutdown" {
			return
		}
	}
	t.Errorf("no \"no shutdown\" in the SVI block: the address would be configured on a "+
		"dark interface. %v", cmds[svi:])
}

// The account needs a ROLE, not just a privilege level: EOS decides what an
// account may do from the role, and privilege alone yields an account that logs
// in and can configure nothing.
func TestMgmtPushWritesTheRole(t *testing.T) {
	var cmds = pushFixture()
	var u = idxPrefix(cmds, "username mcadmin")
	if u < 0 || !strings.Contains(cmds[u], "role network-admin") {
		t.Errorf("username line = %q — EOS authorisation reads the ROLE", cmds[u])
	}
	if !strings.Contains(cmds[u], "secret 0 s3cret") {
		t.Errorf("username line = %q — \"secret\", not \"password\": the second stores "+
			"the value reversibly in the configuration", cmds[u])
	}
}

func TestMgmtPushSanitizesNames(t *testing.T) {
	var cmds = pushFixture()
	// The system name is NORMALIZED, not sanitized, and the difference is the
	// point: "sw lab 1" is a name whose separators an operator plainly meant as
	// separators, so it becomes `sw-lab-1`. A name this rule cannot turn into a
	// valid one is refused instead — cf. hostname_test.go in this package.
	if idx(cmds, "hostname sw-lab-1") < 0 {
		t.Errorf("the hostname was not normalized into what the CLI accepts: %v", cmds)
	}
	if idx(cmds, "name LAN99_Management") < 0 {
		t.Errorf("the VLAN name was not sanitized (EOS refuses spaces outright): %v", cmds)
	}
}

// --- the allowed list, and the VLAN-1 trap ---

// EOS drops native-VLAN traffic on a trunk whose allowed list does not contain
// the native VLAN. On a site whose management VLAN is 1 — the common small
// deployment — an allowed list built from the site VLANs alone produces a trunk
// that looks perfectly configured and forwards nothing at all, including the
// management traffic the provisioning run is about to depend on.
func TestAllowedVlanListAlwaysContainsVlan1AndTheManagementVlan(t *testing.T) {
	var got = allowedVlanList([]driver.VLAN{{ID: 30}, {ID: 40}}, 99)
	if !reflect.DeepEqual(got, []int{1, 30, 40, 99}) {
		t.Errorf("allowedVlanList = %v, want [1 30 40 99]", got)
	}
	var mgmtIsOne = allowedVlanList([]driver.VLAN{{ID: 30}}, 1)
	if mgmtIsOne[0] != 1 {
		t.Errorf("allowedVlanList = %v — VLAN 1 must be there when it IS the management "+
			"VLAN, or the trunk forwards no management traffic", mgmtIsOne)
	}
}

func TestTrunkAllCmdsCoversEveryNamedPort(t *testing.T) {
	var cmds = TrunkAllCmds([]string{"Ethernet1", "Ethernet2"},
		[]driver.VLAN{{ID: 30}, {ID: 40}}, 99)
	if idx(cmds, "interface Ethernet1") < 0 || idx(cmds, "interface Ethernet2") < 0 {
		t.Fatalf("not every port was configured: %v", cmds)
	}
	if idx(cmds, "switchport trunk allowed vlan 1,30,40,99") < 0 {
		t.Errorf("the allowed list is wrong: %v", cmds)
	}
	// THE MANAGEMENT VLAN IS TAGGED, NOT NATIVE. Making it native sends
	// administration traffic UNTAGGED, which a correctly configured upstream —
	// one expecting VLAN 99 tagged — drops; and it parks the administration VLAN
	// on the one part of a trunk that carries no tag, which is what every
	// VLAN-hopping technique aims at. The native VLAN is left at the platform
	// default.
	if idxPrefix(cmds, "switchport trunk native vlan") >= 0 {
		t.Errorf("a native VLAN was written on the provisioned trunks: %v", cmds)
	}
	// The SET form, never "add": on EOS a trunk with no allowed line already
	// carries everything, so "add" changes nothing and reports success.
	if idxPrefix(cmds, "switchport trunk allowed vlan add") >= 0 {
		t.Error("the \"add\" form was used: it is not idempotent on an all-allowed trunk " +
			"and it is the one form that cannot restrict")
	}
}

// This builder never invents a port list: a name it guessed wrong is a line the
// device rejects in the middle of a provisioning run.
func TestTrunkAllCmdsWithNoPortsWritesNothing(t *testing.T) {
	if len(TrunkAllCmds(nil, []driver.VLAN{{ID: 30}}, 99)) != 0 {
		t.Error("commands were produced for an empty port list")
	}
}

// VLAN 1 always exists on EOS. Creating it would be the one line in a
// provisioning push that modifies a VLAN the operator never declared.
func TestMergeSiteVLANsDropsVlan1AndAddsTheManagementVlan(t *testing.T) {
	var got = mergeSiteVLANs([]driver.VLAN{{ID: 1, Name: "default"}, {ID: 30, Name: "Voice"}}, 99, "")
	var want = []driver.VLAN{{ID: 30, Name: "Voice"}, {ID: 99, Name: "LAN99_Management"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("mergeSiteVLANs = %+v, want %+v", got, want)
	}
	// A site entry for the management VLAN keeps ITS name; it is not duplicated.
	var declared = mergeSiteVLANs([]driver.VLAN{{ID: 99, Name: "Admin"}}, 99, "ignored")
	if !reflect.DeepEqual(declared, []driver.VLAN{{ID: 99, Name: "Admin"}}) {
		t.Errorf("the site's own name for the management VLAN was overwritten: %+v", declared)
	}
}

// --- masks ---

func TestMaskPrefixRoundTrip(t *testing.T) {
	var bits int
	for bits = 8; bits <= 30; bits++ {
		if PrefixFromMask(MaskFromPrefix(bits)) != bits {
			t.Errorf("/%d did not round-trip through %q", bits, MaskFromPrefix(bits))
		}
	}
	if PrefixFromMask("not.a.mask") != 0 {
		t.Error("an unreadable mask must report 0, so the caller says so instead of assuming /24")
	}
}

// --- capabilities and fingerprint ---

func TestFingerprintPrefersTheModelThenTheSoftwareLine(t *testing.T) {
	var d = &Driver{}
	var model = d.Fingerprint(driver.Evidence{SysDescr: "Arista Networks EOS DCS-7050SX3-48YC8"})
	var soft = d.Fingerprint(driver.Evidence{ShellOut: "Software image version: 4.24.2.1F"})
	var word = d.Fingerprint(driver.Evidence{SSHBanner: "arista.example.net"})
	if !(model > soft && soft > word && word > 0) {
		t.Errorf("scores model=%d software=%d word=%d — the model must be the strongest "+
			"signal and a bare mention the weakest", model, soft, word)
	}
	if d.Fingerprint(driver.Evidence{SysDescr: "Cisco IOS Software, C2960X"}) != 0 {
		t.Error("a Catalyst scored on the Arista connector")
	}
}

// Legacy 802.1D has no mode on this platform. Listing it would make
// driver.ResolveSTPMode pick it and stp.go refuse it, instead of resolving down
// to rstp — which is the correct answer.
func TestCapabilitiesDoNotClaimLegacySTP(t *testing.T) {
	var caps = (&Driver{}).Capabilities("")
	var m string
	for _, m = range caps.STPModes {
		if m == "stp" {
			t.Fatal("STPModes claims \"stp\": there is no \"spanning-tree mode stp\" here")
		}
	}
	var mode, disable, ok = driver.ResolveSTPMode(
		[]string{driver.STPPrefSTP, driver.STPPrefRSTP}, caps.STPModes)
	if !ok || disable || mode != "rstp" {
		t.Errorf("resolving [stp rstp] gave mode=%q disable=%v ok=%v — it must fall "+
			"through to rstp", mode, disable, ok)
	}
}

// ApplySameVLAN is the flag that says the re-address has been PROVEN on
// hardware. It must be false — the project rule applied to its most expensive
// case, since a same-VLAN re-address that goes wrong takes the switch off the
// network it was being administered over.
//
// THE REASON IS PER GESTURE, not per connector, and this comment used to give the
// blanket one ("nothing in this package has reached an Arista"). That stopped
// being true on 2026-08-08: EnsureAccount and ApplyStormControl both wrote to
// core01.lab, and "storm_control" is declared on that evidence. It stopped
// covering "shutdown" on 2026-08-10 (see below). What is asserted here is
// narrower each time and still holds — no re-address, no STP write and no
// snooping command has been sent to an Arista.
func TestCapabilitiesDoNotClaimUnprovenWrites(t *testing.T) {
	var caps = (&Driver{}).Capabilities("")
	if caps.Mgmt.ApplySameVLAN {
		t.Error("ApplySameVLAN is true on a connector that has never re-addressed a live " +
			"EOS switch")
	}
	var d = &Driver{}
	var intent string
	// "STP" LEFT THIS LIST ON 2026-08-12, on its own capture — pinned below.
	for _, intent = range []string{"dhcp_snooping", "poe"} {
		if d.WritesExtra(intent) {
			t.Errorf("WritesExtra(%q) is true: nothing has exercised THIS gesture on an "+
				"Arista, so declaring it would put a button on screen whose first exercise "+
				"would be on a customer's switch. A sibling intent earning its declaration "+
				"is not evidence for this one", intent)
		}
	}

	// "STP" IS DECLARED SINCE 2026-08-12, and the evidence is a ROOT BRIDGE that
	// meshcraft had designated and never written.
	//
	// THE RUN, on core01.lab (DCS-7050SX-64-R, EOS 4.28.13.1M) through this
	// package's own ApplySpanningTree — not typed at a CLI. Before: priority
	// 32768, `is_root` false, the tree rooted on an UNMANAGED CRS305 at priority
	// 16384, ET48 the root port. The applier sent the region, the mode and the
	// root half; the device answered in 10 s and the running configuration reads:
	//
	//	spanning-tree mode mstp
	//	spanning-tree root super
	//	spanning-tree mst 0-1 priority 0
	//
	// — EOS's own collapsing of the two priority lines this connector emitted, and
	// the proof that the root landed on BOTH instances rather than the CIST alone
	// (cf. driver.SpanningTreePlan.RootsOf, fixed the same day). "show
	// spanning-tree mst 1" answers "Root this switch for MST1" on the instance
	// carrying VLANs 42,98-100, management included. All four ports came back
	// designated/forwarding, the fleet blipped and every device recovered.
	//
	// WHAT IS STILL NOT CAPTURED, and rides this flag anyway: the mode line under
	// RSTP, the per-VLAN priority spelling, and "spanning-tree portfast auto".
	// The flag was flipped on an operator's explicit decision with those named,
	// which is why they are named here too rather than left to be discovered.
	if !d.WritesExtra("stp") {
		t.Error("WritesExtra(\"stp\") is false: the applier itself wrote the region, " +
			"the mode and the root half to core01.lab on 2026-08-12 and the device " +
			"read back \"spanning-tree mst 0-1 priority 0\". Un-declaring it needs that " +
			"evidence withdrawn, not merely doubted")
	}

	// "PORT_ADMIN" IS THE INVERSE ASSERTION, and it is pinned UP rather than
	// deleted: this list is where an intent's evidence is recorded, so an intent
	// that has earned its declaration belongs here saying so, not missing.
	//
	// THE EVIDENCE, 2026-08-10: core01.lab (DCS-7050SX-64-R, EOS 4.28.13.1M,
	// 192.0.2.5, as `mcadmin` over the controller's SOCKS route). Ethernet1 — a
	// dark cage with no transceiver fitted — was shut with "shutdown" and
	// "show interfaces status" answered "disabled", the word this connector's
	// verify path keys on and the one no fixture here could establish. "no
	// shutdown" returned it to "notconnect". Both lines are pinned verbatim in
	// live_portadmin_test.go.
	//
	// AND ITS LIMIT: that capture was typed at a CLI, not driven through
	// ApplyPortAdmin, so the applier's own plan/verify path is still exercised
	// only by the fake CLI in portadmin_test.go. contract.go carries the full
	// statement, and TestLiveWritePortAdmin is what would close it.
	if !d.WritesExtra("port_admin") {
		t.Error("\"port_admin\" is NOT declared although \"shutdown\" was captured on " +
			"core01.lab on 2026-08-10, \"disabled\" read back, and \"no shutdown\" " +
			"reverted it. Un-declaring an intent whose forms are proven hides a working " +
			"gesture from operators; if this was deliberate, the capture in contract.go " +
			"and live_portadmin_test.go has to be withdrawn with it")
	}
}

func TestResetProcedureIsNotClaimedAsVerified(t *testing.T) {
	var p = (&Driver{}).ResetProcedure("")
	if p.Confirmed {
		t.Error("ResetProcedure claims Confirmed on gestures nobody has run here")
	}
	if !strings.Contains(strings.ToLower(strings.Join(p.Pitfalls, " ")), "ztp") {
		t.Error("the pitfalls do not mention ZTP — it is the state the reset LANDS the " +
			"switch in, and the one that makes a provisioning run silently pointless")
	}
}

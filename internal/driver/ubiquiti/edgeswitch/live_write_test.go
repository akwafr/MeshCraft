package edgeswitch

// LIVE WRITE exercise. Double-gated (ES_LIVE_WRITE=1) because it configures a
// real switch. It creates a throwaway VLAN, moves ONE port into it, saves,
// rotates the session's own secret TO THE SAME VALUE (idempotent — it proves
// the command path without locking anyone out), then puts everything back.
//
// The port it touches is chosen to be one that is DOWN, so nothing in the lab
// loses connectivity even if the revert fails.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

func TestLiveWriteVlanSaveAndSecret(t *testing.T) {
	if os.Getenv("ES_LIVE_WRITE") != "1" {
		t.Skip("ES_LIVE_WRITE not set — write exercise skipped")
	}
	var host = os.Getenv("ES_LIVE_HOST")
	if host == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var secret = os.Getenv("ES_LIVE_PASS")
	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: secret, Why: "live write exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var testVLAN = 4001
	var vlanEnv = os.Getenv("ES_LIVE_VLAN")
	if vlanEnv != "" {
		testVLAN, _ = strconv.Atoi(vlanEnv)
	}
	var port = os.Getenv("ES_LIVE_PORT")
	if port == "" {
		port = "GE2" // down in the lab; nothing loses connectivity
	}

	var before State
	before, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	t.Logf("before: %d VLANs, %s = %+v", len(before.VLANs), port, before.Ports[port])

	// --- 1) SNMP
	var snmpRes driver.SNMPResult
	snmpRes, err = c.ApplySNMP(ctx, driver.SNMPIdentity{Community: "meshcraft-test"})
	if err != nil {
		t.Errorf("ApplySNMP: %v", err)
	} else {
		t.Logf("snmp: enabled=%v communitySet=%v skipped=%v",
			snmpRes.Enabled, snmpRes.CommunitySet, snmpRes.Skipped)
	}

	// --- 2) VLAN assignment
	var desired = map[string]PortState{port: {Mode: "access", PVID: testVLAN}}
	var res ApplyResult
	res, err = c.ApplyPorts(ctx, PortPlan{
		Desired:   desired,
		MgmtVLAN:  before.MgmtVLAN,
		SiteVLANs: []driver.VLAN{{ID: testVLAN, Name: "MESHCRAFT_TEST"}},
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	t.Logf("apply: created=%v written=%v skipped=%v notes=%v",
		res.VLANsCreated, res.PortsWritten, res.Skipped, res.Notes)

	// --- 3) Save
	if err = c.Persist(ctx); err != nil {
		t.Errorf("Persist: %v", err)
	} else {
		t.Log("persist: ok (\"write memory confirm\")")
	}

	// --- 4) Read back: the whole point is that the device agrees.
	var after State
	after, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState after: %v", err)
	}
	var found bool
	var v driver.VLAN
	for _, v = range after.VLANs {
		if v.ID == testVLAN {
			found = true
			t.Logf("vlan %d created, name %q", v.ID, v.Name)
		}
	}
	if !found {
		t.Errorf("VLAN %d was not created", testVLAN)
	}
	if after.Ports[port].PVID != testVLAN {
		t.Errorf("%s pvid = %d, want %d", port, after.Ports[port].PVID, testVLAN)
	} else {
		t.Logf("%s moved to VLAN %d and read back", port, testVLAN)
	}

	// --- 5) Secret rotation, to the SAME value.
	if err = c.SetSecret(ctx, secret, secret); err != nil {
		t.Errorf("SetSecret: %v", err)
	} else {
		t.Log("secret: rotated to the same value (command path proven, no lockout)")
	}

	// --- 6) Put it all back.
	var revert = map[string]PortState{port: {Mode: "access", PVID: 1}}
	if _, err = c.ApplyPorts(ctx, PortPlan{Desired: revert, MgmtVLAN: before.MgmtVLAN}); err != nil {
		t.Errorf("revert port: %v", err)
	}
	if err = c.runBlock(ctx, []string{"vlan database", "no vlan " + strconv.Itoa(testVLAN), "exit"}); err != nil {
		t.Errorf("revert vlan: %v", err)
	}
	if err = c.RemoveSNMPCommunity(ctx, "meshcraft-test"); err != nil {
		t.Errorf("revert snmp: %v", err)
	}
	if err = c.Persist(ctx); err != nil {
		t.Errorf("persist revert: %v", err)
	}
	var final State
	final, err = c.ReadState(ctx)
	if err == nil {
		t.Logf("after revert: %d VLANs, %s = %+v", len(final.VLANs), port, final.Ports[port])
	}
	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s", s.OK, s.What)
	}
}

// TestLiveWritePoEApplyAndRevert exercises ApplyPoE end to end: sets an
// explicit class on a PoE-capable port, verifies it read back, then reverts
// to [driver.PoEClassNone] — the state every port was found in during the
// 2026-08-05 survey (cf. poe.go's header) — so the lab switch is left exactly
// as it was, the same discipline TestLiveWriteVlanSaveAndSecret follows for
// VLANs. Gated SEPARATELY (ES_LIVE_POE=1, on top of ES_LIVE_WRITE=1): PoE has
// physical consequences on whatever is plugged into the target port, even
// though the default port (GE5) reads link-down in the lab.
func TestLiveWritePoEApplyAndRevert(t *testing.T) {
	if os.Getenv("ES_LIVE_WRITE") != "1" || os.Getenv("ES_LIVE_POE") != "1" {
		t.Skip("ES_LIVE_WRITE=1 and ES_LIVE_POE=1 both required — this write touches a PoE-capable port")
	}
	var host = os.Getenv("ES_LIVE_HOST")
	if host == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: os.Getenv("ES_LIVE_PASS"),
		Why: "live PoE exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var port = os.Getenv("ES_LIVE_POE_PORT")
	if port == "" {
		port = "GE5" // PoE-capable and link-down in the lab (cf. poe.go)
	}
	var class = os.Getenv("ES_LIVE_POE_CLASS")
	if class == "" {
		class = driver.PoEClass24V // asks the interactive confirmation too
	}

	var before, beforeErr = c.ReadPoE(ctx)
	if beforeErr != nil {
		t.Fatalf("ReadPoE before: %v", beforeErr)
	}
	t.Logf("before: %+v", before.Ports)

	var res driver.ApplyResult
	res, err = c.ApplyPoE(ctx, driver.PoEPlan{Ports: []driver.PoEPortIntent{{Port: port, Class: class}}})
	if err != nil {
		t.Fatalf("ApplyPoE: %v", err)
	}
	t.Logf("apply: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.Skipped) > 0 {
		t.Fatalf("%s was refused class %q — pick a port this hardware's survey found capable "+
			"(cf. poe.go's header): %v", port, class, res.Skipped)
	}

	var mid driver.PoEState
	mid, err = c.ReadPoE(ctx)
	if err != nil {
		t.Fatalf("ReadPoE mid: %v", err)
	}
	var found bool
	var p driver.PoEPortState
	for _, p = range mid.Ports {
		if p.Port == port {
			found = true
			if p.Class != class {
				t.Errorf("%s: class = %q after ApplyPoE, want %q", port, p.Class, class)
			}
		}
	}
	if !found {
		t.Errorf("%s did not appear in the read-back at all", port)
	}
	t.Logf("mid: %+v", mid.Ports)

	// Revert to the state every port was found in (cf. the file header).
	res, err = c.ApplyPoE(ctx, driver.PoEPlan{
		Ports: []driver.PoEPortIntent{{Port: port, Class: driver.PoEClassNone}},
	})
	if err != nil {
		t.Errorf("ApplyPoE revert: %v", err)
	} else if len(res.Skipped) > 0 {
		t.Errorf("revert skipped: %v", res.Skipped)
	}

	var after driver.PoEState
	after, err = c.ReadPoE(ctx)
	if err == nil {
		for _, p = range after.Ports {
			if p.Port == port && p.Class != driver.PoEClassNone {
				t.Errorf("%s: class = %q after revert, want %q", port, p.Class, driver.PoEClassNone)
			}
		}
		t.Logf("after revert: %+v", after.Ports)
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s", s.OK, s.What)
	}
}

// TestLiveWriteDHCPSnooping exercises the three write forms through the
// CONNECTOR, not through a shell, and captures the ENABLED shapes that
// dhcpsnoop.go's header says are still unobserved.
//
// # Why this is safe to run on the lab unit
//
// Snooping polices only the VLANs it is enabled on. The plan names a THROWAWAY
// VLAN that carries no traffic, so the feature is switched on, is configured,
// and inspects nothing — no client can lose DHCP. The trusted port is a port
// that is DOWN, following TestLiveWriteVlanSaveAndSecret above, so nothing loses
// connectivity even if the revert fails.
//
// # The revert cannot go through the contract, and that is deliberate
//
// [driver.DHCPSnoopApplier] NEVER turns the feature off — driver/dhcpsnoop.go
// argues that disabling global snooping because the last trusted port went away
// would strip protection from a whole device on the strength of one click. So
// the applier has no "off", and putting this switch back therefore needs raw
// config lines. They run from a deferred function so they execute even when an
// assertion below fails.
//
// # It refuses to run on a device that already has snooping on
//
// Otherwise the revert would disable protection somebody else installed, which
// is exactly the gesture the contract refuses to offer.
func TestLiveWriteDHCPSnooping(t *testing.T) {
	if os.Getenv("ES_LIVE_WRITE") != "1" {
		t.Skip("ES_LIVE_WRITE not set — write exercise skipped")
	}
	var host = os.Getenv("ES_LIVE_HOST")
	if host == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: os.Getenv("ES_LIVE_PASS"),
		Why: "live dhcp-snooping write exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var snoopVLAN = 4002
	var vlanEnv = os.Getenv("ES_LIVE_SNOOP_VLAN")
	if vlanEnv != "" {
		snoopVLAN, _ = strconv.Atoi(vlanEnv)
	}
	var port = os.Getenv("ES_LIVE_SNOOP_PORT")
	if port == "" {
		port = "GE2" // down in the lab, like the exercise above
	}

	// --- 0) Refuse to touch a device that is already protecting something.
	var before driver.DHCPSnoopState
	before, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Fatalf("ReadDHCPSnooping before: %v", err)
	}
	t.Logf("before: enabled=%v vlans=%v ports=%d", before.Enabled, before.VLANs, len(before.Ports))
	if before.Enabled || len(before.VLANs) > 0 {
		t.Fatalf("snooping is ALREADY on (enabled=%v vlans=%v) — refusing to run, "+
			"because the revert would disable protection this test did not install",
			before.Enabled, before.VLANs)
	}

	// The port must PARTICIPATE in the VLAN being snooped, or the exercise proves
	// nothing about the trust flag: the first live run (2026-08-08) trusted a port
	// sitting in VLAN 100 while snooping 4002, so the port was trusted and outside
	// every inspected VLAN at once, and an empty trust table had two possible
	// meanings. Trusting a port on the VLAN you are inspecting is also the only
	// shape that occurs in practice — it is the uplink toward the DHCP server.
	//
	// Its ORIGINAL state is read and restored, rather than assumed: the sibling
	// exercise above reverts this port to PVID 1, and the lab unit was observed on
	// PVID 100, so a hardcoded revert would silently reconfigure it.
	var portState State
	portState, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState before: %v", err)
	}
	var origPort = portState.Ports[port]
	t.Logf("port %s original state: %+v", port, origPort)
	if portState.Links[port] == driver.LinkUpState {
		t.Fatalf("%s is UP — refusing to move a live port into a throwaway VLAN. "+
			"Set ES_LIVE_SNOOP_PORT to a port that is down.", port)
	}

	// --- The revert, armed BEFORE the first write.
	defer func() {
		var rerr = c.runBlock(context.WithoutCancel(ctx), []string{
			"configure",
			"interface " + cliPortName(port),
			"no ip dhcp snooping trust",
			"exit",
			fmt.Sprintf("no ip dhcp snooping vlan %d", snoopVLAN),
			"no ip dhcp snooping",
			"exit",
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — this switch may still have DHCP snooping "+
				"enabled on VLAN %d and %s trusted. Put it back by hand.", rerr, snoopVLAN, port)
			return
		}
		// Put the port back exactly as it was read, then drop the throwaway VLAN.
		// Order matters: the VLAN cannot be deleted while the port still names it.
		var rctx = context.WithoutCancel(ctx)
		var _, perr = c.ApplyPorts(rctx, PortPlan{
			Desired:  map[string]PortState{port: origPort},
			MgmtVLAN: portState.MgmtVLAN,
		})
		if perr != nil {
			t.Errorf("revert port %s to %+v: %v", port, origPort, perr)
		}
		var verr = c.runBlock(rctx, []string{
			"vlan database", fmt.Sprintf("no vlan %d", snoopVLAN), "exit",
		})
		if verr != nil {
			t.Errorf("revert vlan %d: %v", snoopVLAN, verr)
		}

		var st, serr = c.ReadDHCPSnooping(rctx)
		if serr != nil {
			t.Errorf("revert sent but could not be verified: %v", serr)
			return
		}
		if st.Enabled || len(st.VLANs) > 0 {
			t.Errorf("REVERT INCOMPLETE: enabled=%v vlans=%v", st.Enabled, st.VLANs)
			return
		}
		var fin State
		fin, serr = c.ReadState(rctx)
		if serr == nil {
			t.Logf("revert: snooping off, %s = %+v (was %+v)", port, fin.Ports[port], origPort)
		}

		// THE REVERT MUST BE SAVED TOO, and forgetting this is how a lab switch
		// ends up booting into a configuration nobody chose.
		//
		// ApplyDHCPSnooping now persists ("write memory confirm"), so by the
		// time this runs the STARTUP configuration already carries snooping
		// enabled. The undo above is raw config, which touches the running
		// configuration only — so without this the running config would read
		// clean, every assertion would pass, and the next power cycle would bring
		// the feature back with a throwaway VLAN that no longer exists.
		var serr2 = c.Persist(rctx)
		if serr2 != nil {
			t.Errorf("REVERT NOT SAVED: %v — the running configuration is clean but "+
				"flash still has snooping enabled on VLAN %d. This switch will come "+
				"back with it after a reboot; save it by hand.", serr2, snoopVLAN)
			return
		}
		t.Logf("revert: verified off and SAVED — enabled=%v vlans=%v ports=%d",
			st.Enabled, st.VLANs, len(st.Ports))
	}()

	// Move the port into the VLAN that is about to be snooped.
	var _, mvErr = c.ApplyPorts(ctx, PortPlan{
		Desired:   map[string]PortState{port: {Mode: "access", PVID: snoopVLAN}},
		MgmtVLAN:  portState.MgmtVLAN,
		SiteVLANs: []driver.VLAN{{ID: snoopVLAN, Name: "MESHCRAFT_SNOOP_TEST"}},
	})
	if mvErr != nil {
		t.Fatalf("move %s into VLAN %d: %v", port, snoopVLAN, mvErr)
	}
	t.Logf("moved %s into VLAN %d for the exercise", port, snoopVLAN)

	// --- 1) All three write forms, in one gesture, through the contract.
	var res ApplyResult
	res, err = c.ApplyDHCPSnooping(ctx, driver.DHCPSnoopPlan{
		VLANs: []int{snoopVLAN},
		Ports: []driver.DHCPSnoopPortIntent{{Port: port, Trusted: true}},
	})
	if err != nil {
		t.Fatalf("ApplyDHCPSnooping: %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("apply: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)

	// --- 2) THE CAPTURE the header is waiting for: the ENABLED shapes.
	var raw, rawErr = c.cli.run(ctx, dhcpSnoopShow, showTimeout)
	if rawErr != nil {
		t.Errorf("%q: %v", dhcpSnoopShow, rawErr)
	}
	t.Logf("=== %s (ENABLED, raw, %d bytes) ===\n%s", dhcpSnoopShow, len(raw), raw)

	// DIAGNOSTIC: the trust flag did not appear in the table above on the first
	// live run (2026-08-08), and "the write failed" and "this command does not
	// report it" are opposite conclusions. These two distinguish them: the
	// running configuration says what the switch STORED, and FASTPATH documents a
	// separate per-interface view.
	var diag string
	for _, diag = range []string{
		"show ip dhcp snooping interfaces",
		"show running-config interface " + cliPortName(port),
	} {
		var d, derr = c.cli.run(ctx, diag, showTimeout)
		if derr != nil {
			t.Logf("=== %s → %v ===", diag, derr)
			continue
		}
		t.Logf("=== %s (%d bytes) ===\n%s", diag, len(d), d)
	}

	// --- 3) The device must agree, read back through the contract.
	var after driver.DHCPSnoopState
	after, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Fatalf("ReadDHCPSnooping after: %v", err)
	}
	t.Logf("after: enabled=%v vlans=%v ports=%+v notes=%v",
		after.Enabled, after.VLANs, after.Ports, after.Notes)
	if !after.Enabled {
		t.Error("the global enable was sent and the device still reads as off")
	}
	var sawVLAN bool
	var id int
	for _, id = range after.VLANs {
		if id == snoopVLAN {
			sawVLAN = true
		}
	}
	if !sawVLAN {
		t.Errorf("VLAN %d was enabled and the read does not list it: %v",
			snoopVLAN, after.VLANs)
	}
	var sawTrust bool
	var p driver.DHCPSnoopPortState
	for _, p = range after.Ports {
		if p.Port == port {
			sawTrust = p.Trusted
		}
	}
	if !sawTrust {
		t.Errorf("%s was trusted and the read does not say so: %+v", port, after.Ports)
	}

	// --- 4) UNTRUST through the contract, which is a different instruction.
	//
	// driver/dhcpsnoop.go is explicit that untrusting a port is an ordinary
	// per-port write while disabling the feature is not offered at all, so this
	// is the one remaining form the applier owns and the raw revert above cannot
	// stand in for: a `no ip dhcp snooping trust` sent by hand proves the CLI
	// accepts it, not that ApplyDHCPSnooping asks for it correctly.
	//
	// Trusted:false is a REMOVAL, not "leave it alone" — a port the caller does
	// not want to touch is absent from the plan — so a read that still says
	// trusted afterwards means the applier sent nothing.
	var undo ApplyResult
	undo, err = c.ApplyDHCPSnooping(ctx, driver.DHCPSnoopPlan{
		VLANs: []int{snoopVLAN},
		Ports: []driver.DHCPSnoopPortIntent{{Port: port, Trusted: false}},
	})
	if err != nil {
		t.Fatalf("ApplyDHCPSnooping untrust: %v", err)
	}
	t.Logf("untrust: written=%v skipped=%v", undo.PortsWritten, undo.Skipped)

	var back driver.DHCPSnoopState
	back, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Fatalf("ReadDHCPSnooping after untrust: %v", err)
	}
	t.Logf("after untrust: enabled=%v vlans=%v ports=%+v", back.Enabled, back.VLANs, back.Ports)
	var stillTrusted bool
	var q driver.DHCPSnoopPortState
	for _, q = range back.Ports {
		if q.Port == port && q.Trusted {
			stillTrusted = true
		}
	}
	if stillTrusted {
		t.Errorf("%s was untrusted through the contract and still reads trusted", port)
	}
	// The feature itself must still be ON: the applier never disables it, and a
	// removal that took global snooping down with it would strip protection from
	// every other port on the strength of one untrust.
	if !back.Enabled {
		t.Error("untrusting one port turned the whole feature off — the applier " +
			"must never disable global snooping (driver/dhcpsnoop.go)")
	}

	// The transcript is the evidence that the save ran INSIDE the apply rather
	// than only in the revert. Printed rather than asserted on: Persist emits its
	// own step, and a failed save would additionally have shown up as an
	// "applied but NOT persisted" note on the result above.
	var st Step
	for _, st = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", st.OK, st.What, st.Detail)
	}
}

// TestLiveWriteStormControl exercises [Client.ApplyStormControl] and reverts.
//
// # Only the two classes this range can honestly claim
//
// The plan names broadcast and multicast. Unknown-unicast is deliberately left
// out even though StormControlCaps declares it: that mapping rests on the
// vendor wording for "storm-control unicast" and can only be settled by
// pushing a KNOWN unicast flow through a limited port and confirming it is not
// policed. The lab has no traffic generator, so exercising the command here
// would prove the CLI accepts a word — which says nothing about which frames it
// covers, and is exactly the confusion the contract refuses to make.
//
// # The revert MUST save
//
// ApplyStormControl persists on this range ("write memory confirm"). A revert
// of raw config lines leaves the running configuration clean and flash still
// holding the limiters, so the switch comes back policing after a reload with
// every assertion green. That happened for real on the Catalyst earlier the same
// day; this revert saves.
func TestLiveWriteStormControl(t *testing.T) {
	if os.Getenv("ES_LIVE_WRITE") != "1" {
		t.Skip("ES_LIVE_WRITE not set — write exercise skipped")
	}
	var host = os.Getenv("ES_LIVE_HOST")
	if host == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: os.Getenv("ES_LIVE_PASS"),
		Why: "live storm-control exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var port = os.Getenv("ES_LIVE_STORM_PORT")
	if port == "" {
		port = "GE2" // down in the lab, like the other write exercises here
	}

	// --- 0) Refuse to run against a device already policing something.
	var before driver.StormControlState
	before, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl before: %v", err)
	}
	if len(before.Ports) == 0 {
		t.Fatalf("the read returned no port at all — \"%s\" did not parse, and this "+
			"exercise cannot tell a successful write from an unreadable one until it "+
			"does. Notes: %v", showStormCmd, before.Notes)
	}
	var p driver.StormControlPortState
	for _, p = range before.Ports {
		var l driver.StormLimit
		for _, l = range p.Limits {
			if l.Enabled {
				t.Fatalf("%s already limits %s at %v — refusing to run, because the "+
					"revert would remove a limiter this test did not install",
					p.Port, l.Class, l.Rate)
			}
		}
	}
	t.Logf("before: %d port(s) read, none limited", len(before.Ports))

	// --- The revert, armed before the first write, and it SAVES.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		// stormCmds already closes both the interface and Global Config.
		var rerr = c.runBlock(rctx, append([]string{"configure"},
			stormCmds(cliPortName(port), []driver.StormLimit{
				{Class: driver.StormBroadcast, Enabled: false},
				{Class: driver.StormMulticast, Enabled: false},
			})...))
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s may still be policing. Clear it by hand.",
				rerr, port)
			return
		}
		var st, serr = c.ReadStormControl(rctx)
		if serr != nil {
			t.Errorf("revert sent but not verified: %v", serr)
			return
		}
		var q driver.StormControlPortState
		for _, q = range st.Ports {
			var l driver.StormLimit
			for _, l = range q.Limits {
				if l.Enabled {
					t.Errorf("REVERT INCOMPLETE: %s still limits %s at %v", q.Port, l.Class, l.Rate)
				}
			}
		}
		var werr = c.Persist(rctx)
		if werr != nil {
			t.Errorf("REVERT NOT SAVED: %v — the running configuration is clean and "+
				"flash still carries the limiters; this switch will come back policing "+
				"after a reload.", werr)
			return
		}
		t.Logf("revert: verified and SAVED, nothing limited")
	}()

	// --- 1) Both claimable classes, through the applier.
	var plan = driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{
				{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
				{Class: driver.StormMulticast, Enabled: true, Rate: 10, Unit: driver.StormRatePercent},
			},
		}},
	}
	var res ApplyResult
	res, err = c.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyStormControl: %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("apply: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)

	// --- 2) The device must agree, and the LEVEL must not be read as the flag.
	var after driver.StormControlState
	after, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl after: %v", err)
	}
	var want = map[driver.StormClass]float64{
		driver.StormBroadcast: 5,
		driver.StormMulticast: 10,
	}
	var seen bool
	for _, p = range after.Ports {
		if p.Port != port {
			// Every OTHER port prints a level too — "Disable 5%" — and must still
			// read as unlimited. This is the false-protection check, live.
			var l driver.StormLimit
			for _, l = range p.Limits {
				if l.Enabled {
					t.Errorf("%s reads %s enabled and nothing was written to it — the "+
						"Level column is being read as the enable flag", p.Port, l.Class)
				}
			}
			continue
		}
		seen = true
		var class driver.StormClass
		for class = range want {
			var got, read = p.Limit(class)
			if !read || !got.Enabled {
				t.Errorf("%s %s: read=%v enabled=%v, want a limiter", port, class, read, got.Enabled)
				continue
			}
			if got.Rate != want[class] {
				t.Errorf("%s %s rate = %v, want %v", port, class, got.Rate, want[class])
			}
		}
	}
	if !seen {
		t.Errorf("%s does not appear in the read: %+v", port, after.Ports)
	}

	// --- 3) IDEMPOTENCE.
	var again ApplyResult
	again, err = c.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyStormControl (second pass): %v", err)
	}
	t.Logf("second pass: written=%v skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) > 0 {
		t.Errorf("the same plan rewrote %v on a second pass — the apply does not "+
			"converge", again.PortsWritten)
	}

	// --- 4) Removal of one class, leaving the one the plan did not name.
	var undo ApplyResult
	undo, err = c.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port:   port,
			Limits: []driver.StormLimit{{Class: driver.StormBroadcast, Enabled: false}},
		}},
	})
	if err != nil {
		t.Fatalf("ApplyStormControl (removal): %v", err)
	}
	t.Logf("removal: written=%v skipped=%v", undo.PortsWritten, undo.Skipped)

	var gone driver.StormControlState
	gone, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl after removal: %v", err)
	}
	for _, p = range gone.Ports {
		if p.Port != port {
			continue
		}
		var b, _ = p.Limit(driver.StormBroadcast)
		if b.Enabled {
			t.Errorf("broadcast was removed through the applier and still reads %v", b.Rate)
		}
		var m, mok = p.Limit(driver.StormMulticast)
		if !mok || !m.Enabled {
			t.Error("removing broadcast also removed multicast, a class the plan never named")
		}
	}
}

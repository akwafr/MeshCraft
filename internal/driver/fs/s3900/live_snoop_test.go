package s3900

// THE LIVE DHCP-SNOOPING ROUND TRIP — s3900lab (S3900-24T4S, FSOS 1.7.7),
// over the controller's SOCKS route as `mcadmin`.
//
// WHAT IT IS FOR, and it is the only thing that earns the declaration:
// dhcpsnoop.go's header records that all three WRITE FORMS are typed on this
// hardware while [Client.ApplyDHCPSnooping] itself — its ordering, its gate, its
// three re-reads — had only ever been driven by the fake CLI in
// dhcpsnoop_test.go. A form that a device accepts says nothing about an applier
// that sequences three of them and verifies each. This harness drives the
// APPLIER, end to end, against the real switch.
//
//	S3900_LIVE_HOST=192.0.2.71 S3900_LIVE_USER=… S3900_LIVE_PASS=… \
//	S3900_LIVE_SNOOP_WRITE=1 \
//	S3900_LIVE_SOCKS=127.0.0.1:1080 S3900_LIVE_SOCKS_USER=… S3900_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/fs/s3900 -run TestLiveDHCPSnoopingRoundTrip -v
//
// DOUBLE-GATED, and the second gate is its own variable. This is not a dark-port
// gesture: the global enable it sends "affects EVERY port, not only the ports
// named in this plan" — the applier says so itself in its own Notes — so nobody
// re-running some other live exercise on this range may switch DHCP inspection
// on as a side effect.
//
// # THE SAFETY ARGUMENT, and it is the whole design of this file
//
// Two independent facts make the run safe, and BOTH are re-checked against the
// device before anything is written rather than trusted from this comment.
//
//  1. THE UPLINK IS TRUSTED FIRST. `Eth 1/26` is s3900lab's path to the fleet
//     core — `show lldp neighbor` reads "Eth 1/26 … 28-99-3A-9D-B7-65 Ethernet48
//     … core01-lab", confirmed 2026-08-11 — so upstream DHCP OFFER/ACK enter
//     this switch on that port. Trusting it before any VLAN is armed is what
//     keeps them flowing; [Client.ApplyDHCPSnooping] already writes in that order
//     and the ordering is load-bearing.
//
//     `Eth 1/28` is NOT the uplink, and the distinction cost a session to
//     establish: it faces sw-labo-1930, a DOWNSTREAM access switch. Trusting it
//     instead would arm inspection with the real DHCP path untrusted, which is
//     precisely the outage this exercise must not cause. [snoopUplink] is a
//     CONSTANT for that reason — a knob here would be a knob for turning a
//     reasoned plan into an unreasoned one.
//
//  2. THE ARMED VLAN CARRIES NOTHING. VLAN 777 is undefined on this switch (it
//     is the VLAN the 2026-08-10 form capture used, for the same reason), and on
//     this firmware inspection is per-VLAN: `show dhcp snooping config` prints
//     "DHCP Snooping is configured on the following VLANs:" as a list, and the
//     global enable alone was already exercised on this box on 2026-08-10 with no
//     traffic effect. So the drop behaviour is armed on a VLAN no frame carries.
//
// WHAT THAT COSTS, stated rather than glossed: this run proves the APPLIER — the
// sequence, the verification, the idempotence, the teardown — and it does NOT
// observe a DHCP frame being dropped. Watching a real offer die would need the
// feature armed on a VLAN that carries clients, which is an outage on a live
// path and a different exercise with a different authorisation.
//
// # THE TEARDOWN, and why it does not go through the applier
//
// [Client.ApplyDHCPSnooping] cannot undo its own enable, deliberately: turning
// the feature off because a plan stopped naming a port would strip protection
// nobody asked to remove. So the teardown types the three `no` forms itself, and
// it is armed in a defer BEFORE the first write — not after the assertions, which
// a t.Fatal would skip. It restores from a SNAPSHOT read at the top rather than
// from an assumption that everything was off, because a flag this exercise did
// not set is somebody's configuration.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

// snoopUplink is the ONLY port this exercise trusts. Constant, not an
// environment variable — see the safety argument in the file header.
const snoopUplink = "GE26"

// snoopLiveVLAN is the VLAN inspection is armed on: undefined on this switch, so
// nothing it can drop is carried by anything.
const snoopLiveVLAN = 777

// liveSnoopEnv gathers the gates, and skips with the reason when one is missing.
func liveSnoopEnv(t *testing.T) (driver.Target, string, string) {
	t.Helper()
	var host = os.Getenv("S3900_LIVE_HOST")
	var user = os.Getenv("S3900_LIVE_USER")
	var pass = os.Getenv("S3900_LIVE_PASS")
	if host == "" || user == "" || pass == "" {
		t.Skip("S3900_LIVE_HOST / _USER / _PASS unset: no device to talk to")
	}
	if os.Getenv("S3900_LIVE_SNOOP_WRITE") != "1" {
		t.Skip("S3900_LIVE_SNOOP_WRITE is not 1: this exercise WRITES DHCP snooping " +
			"configuration on a live switch and refuses to do so by accident")
	}
	var tgt = driver.Target{Host: host}
	var socksAddr = os.Getenv("S3900_LIVE_SOCKS")
	if socksAddr != "" {
		var auth *proxy.Auth
		var u = os.Getenv("S3900_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("S3900_LIVE_SOCKS_PASS")}
		}
		var d, err = proxy.SOCKS5("tcp", socksAddr, auth, proxy.Direct)
		if err != nil {
			t.Fatalf("SOCKS %s: %v", socksAddr, err)
		}
		var cd, ok = d.(proxy.ContextDialer)
		if !ok {
			t.Fatal("this SOCKS dialer carries no DialContext")
		}
		tgt.Dialer = cd
	}
	return tgt, user, pass
}

// TestLiveDHCPSnoopingRoundTrip drives [Client.ApplyDHCPSnooping] on hardware.
func TestLiveDHCPSnoopingRoundTrip(t *testing.T) {
	var tgt, user, pass = liveSnoopEnv(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var d = &Driver{}
	var adm, err = d.Authenticate(ctx, tgt,
		[]Candidate{{Username: user, Secret: pass, Why: "live harness"}})
	if adm != nil {
		defer adm.Close() //nolint:errcheck
	}
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, ok = adm.(*Client)
	if !ok {
		t.Fatalf("Authenticate returned %T, not *Client", adm)
	}

	// THE OPENING SNAPSHOT, from the one command whose enabled AND disabled
	// shapes are both captured. Read before anything is decided, and kept
	// verbatim: the teardown is proved against this text and not against a
	// belief about what "off" looks like.
	var before string
	before, err = c.cli.run(ctx, snoopConfigCmd, cmdTimeout)
	if err != nil && strings.TrimSpace(before) == "" {
		t.Fatalf("opening read of \"%s\" failed, nothing was written: %v", snoopConfigCmd, err)
	}
	var wasOn, known = parseSnoopConfigStatus(before)
	if !known {
		t.Fatalf("\"%s\" did not report a global status this parser recognises — refusing to "+
			"write on a device whose state cannot be read back:\n%s", snoopConfigCmd, before)
	}
	if wasOn {
		t.Skip("DHCP snooping is ALREADY enabled globally on this switch. This exercise " +
			"restores what it found, and it cannot know which VLANs and trust flags were " +
			"somebody else's — run it on a device where the feature is off")
	}

	// THE UPLINK IS RE-ESTABLISHED FROM THE DEVICE, never taken from the constant
	// alone: the cable this whole safety argument rests on can be moved, and it
	// was moved on this fleet twice this week.
	var lldp string
	lldp, err = c.cli.run(ctx, "show lldp neighbor", cmdTimeout)
	if err != nil && strings.TrimSpace(lldp) == "" {
		t.Fatalf("could not read the neighbour table, nothing was written: %v", err)
	}
	if !liveUplinkStillFacesTheCore(lldp) {
		t.Fatalf("\"show lldp neighbor\" no longer shows %s facing core01-lab. The plan "+
			"below trusts that port and arms inspection over it; with the cable moved, that "+
			"is an outage rather than an exercise. Nothing was written:\n%s", snoopUplink, lldp)
	}

	// TEARDOWN ARMED BEFORE THE FIRST WRITE. A t.Fatal below must still leave the
	// switch as it was found.
	defer func() {
		var tctx, tcancel = context.WithTimeout(context.Background(), 90*time.Second)
		defer tcancel()
		var terr = c.runConfigBlock(tctx, []string{
			"no dhcp snooping vlan " + itoa(snoopLiveVLAN),
			"interface ethernet 1/26",
			"no dhcp snooping trust",
			"exit",
			"no dhcp snooping",
		})
		if terr != nil {
			t.Errorf("TEARDOWN FAILED (%v) — the switch may still be inspecting DHCP on "+
				"VLAN %d with %s trusted. Check \"%s\" by hand",
				terr, snoopLiveVLAN, snoopUplink, snoopConfigCmd)
			return
		}
		var after, aerr = c.cli.run(tctx, snoopConfigCmd, cmdTimeout)
		if aerr != nil && strings.TrimSpace(after) == "" {
			t.Errorf("teardown could not be verified: %v", aerr)
			return
		}
		if strings.TrimSpace(after) != strings.TrimSpace(before) {
			t.Errorf("the switch did not return to its opening state.\nBEFORE:\n%s\nAFTER:\n%s",
				before, after)
		}
	}()

	var plan = driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: snoopUplink, Trusted: true}},
		VLANs: []int{snoopLiveVLAN},
	}
	var res driver.ApplyResult
	res, err = c.ApplyDHCPSnooping(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyDHCPSnooping: %v (steps: %+v)", err, c.Steps)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != snoopUplink {
		t.Fatalf("PortsWritten = %v, want exactly [%s] — that field means CONFIRMED by a "+
			"re-read, so anything else says the trust flag did not take (skipped: %v)",
			res.PortsWritten, snoopUplink, res.Skipped)
	}

	// INDEPENDENT re-read, through the connector's own reader rather than the
	// applier's internal verification: an applier that believes its own write is
	// exactly the failure the re-reads exist to catch.
	var st driver.DHCPSnoopState
	st, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Fatalf("ReadDHCPSnooping after the write: %v", err)
	}
	if !st.Enabled {
		t.Error("the feature reads back OFF after an apply that reported success")
	}
	if !liveVLANArmed(st.VLANs, snoopLiveVLAN) {
		t.Errorf("VLANs = %v, want VLAN %d armed", st.VLANs, snoopLiveVLAN)
	}
	if !liveTrusted(st, snoopUplink) {
		t.Errorf("%s does not read back trusted: %+v", snoopUplink, st.Ports)
	}

	// IDEMPOTENCE. The same plan again must write NOTHING: an applier that
	// re-sends what is already there is one that cannot be run from a reconciler.
	var again driver.ApplyResult
	again, err = c.ApplyDHCPSnooping(ctx, plan)
	if err != nil {
		t.Fatalf("second ApplyDHCPSnooping: %v", err)
	}
	if len(again.PortsWritten) != 0 {
		t.Errorf("the second identical apply wrote %v — a port already where the plan wants "+
			"it must be skipped, not rewritten", again.PortsWritten)
	}
}

// liveUplinkStillFacesTheCore reads the neighbour table for the one fact the
// safety argument rests on.
//
// Deliberately crude: it looks for the uplink's own row spelling and the core's
// system name on it. A parser would be the better answer if anything else needed
// this table; here the question is a yes/no about one line, and a false NO costs
// a skipped test while a false YES costs an outage.
func liveUplinkStillFacesTheCore(lldp string) bool {
	var line string
	for _, line = range strings.Split(lldp, "\n") {
		if !strings.Contains(line, "Eth 1/26") {
			continue
		}
		return strings.Contains(line, "core01")
	}
	return false
}

func liveVLANArmed(vlans []int, want int) bool {
	var v int
	for _, v = range vlans {
		if v == want {
			return true
		}
	}
	return false
}

func liveTrusted(st driver.DHCPSnoopState, port string) bool {
	var i int
	for i = range st.Ports {
		if st.Ports[i].Port == port {
			return st.Ports[i].Trusted
		}
	}
	return false
}

// itoa avoids pulling strconv in for one call in a test file.
func itoa(n int) string {
	var digits string
	if n == 0 {
		return "0"
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

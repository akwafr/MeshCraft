package ios

// A LIVE exercise of [Client.ApplyDHCPSnooping] against the lab 2960-X.
//
// # WHY THIS ONE NEEDED RECONNAISSANCE AND STORM CONTROL DID NOT
//
// A storm limiter on an empty port polices nothing, so the write is real and the
// consequence is nil. `ip dhcp snooping vlan <n>` has no such version: it is
// VLAN-wide, and on a VLAN whose clients get their addresses through this switch
// it DROPS every DHCP offer arriving on an untrusted port. Choosing the target
// from a guess would be choosing whose DHCP to break.
//
// So live_snoopprobe_test.go was run first, and it is what makes this safe
// (captured 2026-08-09):
//
//   - snooping is DISABLED switch-wide, no VLAN configured, no trust line set,
//     so this exercise starts from the factory posture and owns everything it
//     changes;
//   - exactly ONE port is connected — Te1/0/2, a 10G trunk, the uplink. Every
//     other port reads `notconnect`;
//   - VLAN 42 (LAN42_ServicesPublics) has NO ports assigned at all, while
//     VLAN 100 has twenty and VLAN 99 is Management.
//
// Hence the target: VLAN 42, with the REAL UPLINK as the trusted port. That is
// both the realistic gesture — TODO.md's complaint about the earlier capture was
// precisely that the trusted port had been chosen for being empty rather than
// for being an uplink — and the safe one, because trusting the uplink is what
// keeps upstream offers flowing while the VLAN is snooped.
//
// ORDER MATTERS AND THE APPLIER ALREADY GETS IT RIGHT: it writes per-port trust
// BEFORE turning the feature on (step 1 of ApplyDHCPSnooping). The opposite
// order leaves a window in which the uplink is untrusted on a snooped VLAN, and
// on a busy VLAN that window is a DHCP outage.
//
// # THE REVERT SAVES, and that is not optional
//
// This applier PERSISTS (`write memory`, step 4). A revert written as raw
// configuration lines and left unsaved leaves the running config clean and the
// STARTUP config carrying snooping — the exact trap this range sprang during the
// storm-control exercise on 2026-08-08, where `show startup-config` still held
// `storm-control multicast level pps 1.5k` after a running-config revert read
// clean. So the revert below saves, and then PROVES it by reading the startup
// config back.
//
//	SNOOP_LIVE_HOST=192.0.2.2 SNOOP_LIVE_USER=… SNOOP_LIVE_PASS=… \
//	SNOOP_LIVE_WRITE=1 \
//	SNOOP_LIVE_SOCKS=127.0.0.1:1080 SNOOP_LIVE_SOCKS_USER=… SNOOP_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/cisco/ios -run TestLiveDHCPSnooping -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// snoopStartupLines returns the startup-config's DHCP-snooping lines. The
// running config is not enough: see the header.
func snoopStartupLines(ctx context.Context, t *testing.T, c *Client) []string {
	t.Helper()
	var out, err = c.cli.run(ctx, "show startup-config | include dhcp snooping", cmdTimeout)
	if err != nil {
		t.Fatalf("read startup-config: %v", err)
	}
	var keep []string
	var l string
	for _, l = range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		// Drop the echoed command and the prompt: only configuration lines.
		if l == "" || strings.HasPrefix(l, "show ") || strings.HasSuffix(l, "#") {
			continue
		}
		if strings.Contains(l, "dhcp snooping") {
			keep = append(keep, l)
		}
	}
	return keep
}

func TestLiveDHCPSnoopingRoundTrip(t *testing.T) {
	if os.Getenv("SNOOP_LIVE_WRITE") != "1" {
		t.Skip("SNOOP_LIVE_WRITE not set — write exercise skipped")
	}
	if os.Getenv("SNOOP_LIVE_HOST") == "" {
		t.Skip("SNOOP_LIVE_HOST not set")
	}
	const targetVLAN = 42
	// THE STANDARD-MODEL NAME, not the device's.
	//
	// A plan carries meshcraft's own family (`GE2`, `XG2`); the connector
	// translates it to the IOS spelling with `cliPortNameStem`, where `XG` is
	// TenGigabitEthernet. Passing the device's own `Te1/0/2` here is refused with
	// "outside the standard-model mapping" — correctly, and that refusal is the
	// first thing this exercise hit. `XG2` is TenGigabitEthernet1/0/2, the uplink.
	const uplink = "XG2"
	const uplinkCLI = "TenGigabitEthernet1/0/2"

	var ctx, cancel = context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var c = liveSnoopClient(ctx, t, "DHCP snooping live exercise")
	defer c.Close()
	var _, perr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if perr != nil {
		t.Fatalf("terminal length 0: %v", perr)
	}

	// --- 0. Refuse to run on a device that is already snooping.
	var before, berr = c.ReadDHCPSnooping(ctx)
	if berr != nil {
		t.Fatalf("ReadDHCPSnooping before: %v", berr)
	}
	if before.Enabled || len(before.VLANs) > 0 {
		t.Fatalf("this switch already snoops (enabled=%v VLANs=%v) — refusing: the revert "+
			"below would tear down protection this exercise did not install",
			before.Enabled, before.VLANs)
	}
	var startupBefore = snoopStartupLines(ctx, t, c)
	t.Logf("startup-config carries %d dhcp-snooping line(s) before: %v",
		len(startupBefore), startupBefore)

	// --- The revert, armed before the first write.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		// The trust removal goes THROUGH THE APPLIER — that is one of the three
		// things TODO.md says the earlier capture never exercised.
		var _, rerr = c.ApplyDHCPSnooping(rctx, driver.DHCPSnoopPlan{
			Ports: []driver.DHCPSnoopPortIntent{{Port: uplink, Trusted: false}},
		})
		if rerr != nil {
			t.Errorf("REVERT step 1 (untrust %s) FAILED: %v", uplink, rerr)
		}
		// The VLAN and the global switch come off with raw lines, because the
		// applier NEVER removes a VLAN by design: `snoopVLANsToAdd` treats a VLAN
		// found enabled as protection it did not install and must not take away.
		// That rule is right for production and is exactly why the test has to
		// clean up behind itself.
		var cerr = c.runConfigBlock(rctx, []string{
			"no ip dhcp snooping vlan 42",
			"no ip dhcp snooping",
		})
		if cerr != nil {
			t.Errorf("REVERT step 2 (remove VLAN + global) FAILED: %v", cerr)
			return
		}
		// AND SAVE. A revert left unsaved is the trap this range already sprang.
		var serr = c.save(rctx)
		if serr != nil {
			t.Errorf("REVERT step 3 (save) FAILED: %v — the running config is clean and the "+
				"STARTUP config still carries snooping; fix by hand before leaving", serr)
			return
		}
		var after, aerr = c.ReadDHCPSnooping(rctx)
		if aerr != nil {
			t.Errorf("revert sent but the re-read failed: %v", aerr)
			return
		}
		if after.Enabled || len(after.VLANs) > 0 {
			t.Errorf("REVERT INCOMPLETE: still enabled=%v VLANs=%v", after.Enabled, after.VLANs)
		}
		var startupAfter = snoopStartupLines(rctx, t, c)
		if len(startupAfter) != len(startupBefore) {
			t.Errorf("STARTUP CONFIG DID NOT COME BACK: %v → %v — a reboot would restore "+
				"snooping this exercise installed", startupBefore, startupAfter)
			return
		}
		t.Logf("revert: running clean, startup-config back to %d line(s)", len(startupAfter))
	}()

	// --- 1. The gesture: trust the uplink, snoop a VLAN with no local ports.
	var plan = driver.DHCPSnoopPlan{
		VLANs: []int{targetVLAN},
		Ports: []driver.DHCPSnoopPortIntent{{Port: uplink, Trusted: true}},
	}
	var res, err = c.ApplyDHCPSnooping(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyDHCPSnooping: %v", err)
	}
	t.Logf("apply: VLANsCreated=%v PortsWritten=%v Skipped=%v Notes=%v",
		res.VLANsCreated, res.PortsWritten, res.Skipped, res.Notes)
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != uplink {
		t.Errorf("PortsWritten=%v, want [%s]", res.PortsWritten, uplink)
	}

	var mid driver.DHCPSnoopState
	mid, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Fatalf("ReadDHCPSnooping after: %v", err)
	}
	t.Logf("re-read: enabled=%v VLANs=%v ports=%+v", mid.Enabled, mid.VLANs, mid.Ports)
	if !mid.Enabled {
		t.Error("the applier reported success and the switch reads snooping DISABLED")
	}
	// NOTE on the empty VLANsCreated above: that field belongs to the VLAN
	// PROVISIONING path (apply.go), not to snooping — this gesture enables
	// snooping ON an existing VLAN and creates nothing. It is initialised at the
	// top of ApplyDHCPSnooping and never written there, which is correct but reads
	// at first glance like the applier disowning its own work. The structured
	// report of which VLANs are now snooped is the RE-READ, asserted just below.
	var sawVLAN bool
	var v int
	for _, v = range mid.VLANs {
		if v == targetVLAN {
			sawVLAN = true
		}
	}
	if !sawVLAN {
		t.Errorf("VLAN %d absent from the re-read: %v", targetVLAN, mid.VLANs)
	}
	// The read answers in the STANDARD family too, so both halves speak one
	// vocabulary here — unlike mikrotik/swos, where the accept and emit families
	// differ and comparing names was a real defect (fixed 2026-08-08).
	var sawTrust bool
	var p driver.DHCPSnoopPortState
	for _, p = range mid.Ports {
		if p.Port == uplink && p.Trusted {
			sawTrust = true
		}
	}
	if !sawTrust {
		t.Errorf("%s does not read as trusted: %+v", uplink, mid.Ports)
	}

	// --- 2. CONVERGENCE. The second apply must write nothing.
	var again ApplyResult
	again, err = c.ApplyDHCPSnooping(ctx, plan)
	if err != nil {
		t.Fatalf("second ApplyDHCPSnooping: %v", err)
	}
	t.Logf("second apply: VLANsCreated=%v PortsWritten=%v", again.VLANsCreated, again.PortsWritten)
	if len(again.VLANsCreated) != 0 {
		t.Errorf("second apply re-added VLAN(s) %v — the connector does not converge",
			again.VLANsCreated)
	}
	if len(again.PortsWritten) != 0 {
		t.Errorf("second apply rewrote %v — a trust flag already correct was written again",
			again.PortsWritten)
	}

	// --- 3. TWO REFUSALS, neither of which the earlier capture exercised. They
	// are different branches and a test that only hits one proves half of it:
	// "Gi9/9/9" is MALFORMED for this connector's vocabulary (a device-family
	// name where a standard one belongs), while "GE99" is perfectly well formed
	// and simply not a port this 24-port switch has.
	var refusal struct {
		name string
		port string
		why  string
	}
	var refusals = []struct {
		name string
		port string
		why  string
	}{
		{"malformed name", "Gi9/9/9", "outside the standard-model mapping"},
		{"valid name, absent port", "GE99", "a 24-port switch has no GE99"},
	}
	for _, refusal = range refusals {
		var bogus ApplyResult
		bogus, err = c.ApplyDHCPSnooping(ctx, driver.DHCPSnoopPlan{
			Ports: []driver.DHCPSnoopPortIntent{{Port: refusal.port, Trusted: true}},
		})
		if err != nil {
			t.Errorf("%s: returned a hard error: %v", refusal.name, err)
			continue
		}
		if len(bogus.PortsWritten) != 0 {
			t.Errorf("%s (%s): reported as WRITTEN: %v — %s",
				refusal.name, refusal.port, bogus.PortsWritten, refusal.why)
		}
		if len(bogus.Skipped) == 0 {
			t.Errorf("%s (%s): no Skipped line, so the operator is told the trust flag "+
				"landed when nothing was sent", refusal.name, refusal.port)
		}
		t.Logf("refusal [%s] %s: Skipped=%v", refusal.name, refusal.port, bogus.Skipped)
	}
	_ = uplinkCLI
}

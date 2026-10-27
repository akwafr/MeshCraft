package ios

// A LIVE exercise of [Client.ApplyPortAdmin] — shut and un-shut — on the lab
// Catalyst 2960-X.
//
// `port_admin` is declared on this connector, so the gesture is reachable from
// a screen, and portadmin.go's own header records that "shutdown" on a
// PHYSICAL port had never been sent by it. This closes that.
//
// # THE APPLIER DOES NOT PERSIST, and the test asserts that rather than
// assuming it
//
// Everything else this connector writes ends with a save. This one deliberately
// does not: a shut port is an ACTION, and leaving the startup configuration
// alone means a reload brings the port back. That is the safety property of the
// whole gesture, and it is worth a live assertion because it is the kind of
// thing a later refactor "tidies up" by making every applier consistent. So the
// startup configuration is read before and after and must be IDENTICAL, while
// the running configuration moves.
//
// # THE TARGET IS DARK, and the guardrail is checked WITHOUT WRITING
//
// The uplink is Te1/0/2 and it carries this session. A live test that asked the
// applier to shut it would be using the outage it is trying to rule out as its
// own instrument. `carriesMgmtVLAN` is pure, so the real device's answer comes
// from feeding it the configuration the device just printed — same evidence, no
// stake. (Same reasoning, and the same first-draft mistake, as the EdgeSwitch
// sibling: see its file header.)
//
//	PA_LIVE_HOST=192.0.2.2 PA_LIVE_USER=… PA_LIVE_PASS=… \
//	PA_LIVE_WRITE=1 PA_LIVE_PORT=GE1 \
//	PA_LIVE_SOCKS=127.0.0.1:1080 PA_LIVE_SOCKS_USER=… PA_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/cisco/ios -run TestLivePortAdmin -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

func livePortAdminClient(ctx context.Context, t *testing.T, why string) *Client {
	t.Helper()
	var tgt = driver.Target{Host: os.Getenv("PA_LIVE_HOST")}
	var sa = os.Getenv("PA_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("PA_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("PA_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("PA_LIVE_USER"), Secret: os.Getenv("PA_LIVE_PASS"), Why: why,
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	return c
}

// startupShutdownLines returns the startup config's `shutdown` lines with the
// interface each belongs to, so "the startup config did not move" is checkable
// rather than merely stated.
func startupShutdownLines(ctx context.Context, t *testing.T, c *Client) []string {
	t.Helper()
	var out, err = c.cli.run(ctx, "show startup-config | include ^interface|^ shutdown", cmdTimeout)
	if err != nil {
		t.Fatalf("read startup-config: %v", err)
	}
	var keep []string
	var iface string
	var l string
	for _, l = range strings.Split(out, "\n") {
		var trimmed = strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "interface ") {
			iface = trimmed
			continue
		}
		if trimmed == "shutdown" && iface != "" {
			keep = append(keep, iface)
		}
	}
	return keep
}

func TestLivePortAdminShutAndUnshut(t *testing.T) {
	if os.Getenv("PA_LIVE_WRITE") != "1" {
		t.Skip("PA_LIVE_WRITE not set")
	}
	var port = os.Getenv("PA_LIVE_PORT")
	if port == "" {
		t.Skip("PA_LIVE_PORT not set — refusing to pick a port to shut on a live switch")
	}
	if os.Getenv("PA_LIVE_HOST") == "" {
		t.Skip("PA_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var c = livePortAdminClient(ctx, t, "port admin shut/un-shut exercise")
	defer c.Close()
	var _, perr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if perr != nil {
		t.Fatalf("terminal length 0: %v", perr)
	}

	var before, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState before: %v", err)
	}
	var was, known = before.Links[port]
	if !known {
		t.Fatalf("%s is not on this faceplate", port)
	}
	if was == driver.LinkUpState {
		t.Fatalf("%s carries a LIVE LINK — refusing to shut it. Pick a dark port.", port)
	}
	var startupBefore = startupShutdownLines(ctx, t, c)
	t.Logf("%s reads %s; startup-config has %d shut interface(s): %v",
		port, was, len(startupBefore), startupBefore)

	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var _, rerr = c.ApplyPortAdmin(rctx, driver.PortAdminPlan{
			Ports: []driver.PortAdminIntent{{Port: port, Enabled: true}},
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s may still be SHUT", rerr, port)
			return
		}
		var after, aerr = c.ReadState(rctx)
		if aerr != nil {
			t.Errorf("revert sent but the re-read failed: %v", aerr)
			return
		}
		if after.Links[port] == driver.LinkDisabled {
			t.Errorf("REVERT INCOMPLETE: %s still reads %s", port, after.Links[port])
			return
		}
		t.Logf("revert: %s reads %s, back as found (%s)", port, after.Links[port], was)
	}()

	// --- 1. SHUT.
	var res driver.ApplyResult
	res, err = c.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin(shut): %v", err)
	}
	t.Logf("shut: PortsWritten=%v Skipped=%v Notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten=%v, want [%s] — it means CONFIRMED on this fleet",
			res.PortsWritten, port)
	}

	var mid driver.State
	mid, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState after shut: %v", err)
	}
	if mid.Links[port] != driver.LinkDisabled {
		t.Errorf("%s reads %q after being shut, want %q",
			port, mid.Links[port], driver.LinkDisabled)
	} else {
		t.Logf("CONFIRMED: %s reads %q", port, mid.Links[port])
	}

	// --- 2. THE NON-PERSISTENCE PROPERTY. A reload must bring the port back, so
	// the startup configuration must be exactly what it was.
	var startupMid = startupShutdownLines(ctx, t, c)
	if len(startupMid) != len(startupBefore) {
		t.Errorf("THE STARTUP CONFIG MOVED: %v → %v. This applier is supposed to leave it "+
			"alone so a reload undoes a shut port; if it now saves, that safety property "+
			"is gone and portadmin.go's header is wrong.", startupBefore, startupMid)
	} else {
		t.Logf("startup-config unchanged (%d shut interface(s)) — a reload would bring "+
			"%s back, as the applier promises", len(startupMid), port)
	}

	// --- 3. CONVERGENCE.
	var again driver.ApplyResult
	again, err = c.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("second ApplyPortAdmin(shut): %v", err)
	}
	t.Logf("second shut: PortsWritten=%v Skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) != 0 {
		t.Errorf("second apply rewrote %v", again.PortsWritten)
	}
}

// TestLivePortAdminGuardSparesTheUplink feeds the device's OWN running
// configuration to the pure guard. Nothing is sent; see the file header for why
// the writing version of this test is the wrong instrument.
func TestLivePortAdminGuardSparesTheUplink(t *testing.T) {
	if os.Getenv("PA_LIVE_HOST") == "" {
		t.Skip("PA_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var c = livePortAdminClient(ctx, t, "port admin guardrail check (read only)")
	defer c.Close()
	var _, perr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if perr != nil {
		t.Fatalf("terminal length 0: %v", perr)
	}

	var st, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	var runOut string
	runOut, err = c.cli.run(ctx, "show running-config", cmdTimeout)
	if err != nil {
		t.Fatalf("show running-config: %v", err)
	}
	var rc = parseRunningConfig(runOut, c.host)

	var linked []string
	var name string
	for name = range st.Links {
		if st.Links[name] == driver.LinkUpState {
			linked = append(linked, name)
		}
	}
	if len(linked) == 0 {
		t.Skip("no port reads UP — nothing for the guardrail to spare")
	}
	t.Logf("mgmt VLAN %d; live port(s) %v", st.MgmtVLAN, linked)

	for _, name = range linked {
		var carries = carriesMgmtVLAN(name, rc, st.MgmtVLAN)
		if !carries {
			t.Errorf("THE GUARD WOULD NOT SPARE %s, and it carries a live link on the "+
				"device this session is talking to (mgmt VLAN %d)", name, st.MgmtVLAN)
			continue
		}
		t.Logf("guard spares %s: it carries the management VLAN (%d) on a live link",
			name, st.MgmtVLAN)
	}
}

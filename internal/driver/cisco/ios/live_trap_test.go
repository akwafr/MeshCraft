package ios

// A LIVE exercise of [Client.ApplyTrapReceiver] against a real Catalyst,
// through the controller's SOCKS route (the lab devices are not reachable
// from the build host directly).
//
// It exists for the same reason as internal/driver/live_mgmt_test.go: the
// project rule is that a capability is flipped only after the write has been
// exercised on hardware, and this is that proof, repeatable rather than a
// story in a commit message.
//
// IT LEAVES THE DEVICE AS IT FOUND IT. The sequence is pose → pose again
// (idempotence: the second call must write NOTHING) → revert to the exact
// prior lines → re-read that proves the revert → save. The worst case of an
// interrupted run is a device carrying one recipient toward the site's own
// collector, which the next run removes.
//
//	TRAP_LIVE_HOST=192.0.2.2 TRAP_LIVE_USER=mcadmin TRAP_LIVE_PASS=… \
//	TRAP_LIVE_ADDR=10.42.0.67 TRAP_LIVE_COMMUNITY=… \
//	TRAP_LIVE_SOCKS=127.0.0.1:1080 TRAP_LIVE_SOCKS_USER=… TRAP_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/cisco/ios -run TestLiveTrapReceiver -v

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

type liveSocksDialer struct{ d proxy.Dialer }

func (s liveSocksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var cd, ok = s.d.(proxy.ContextDialer)
	if ok {
		return cd.DialContext(ctx, network, addr)
	}
	return s.d.Dial(network, addr)
}

func TestLiveTrapReceiverIsIdempotentAndRevertible(t *testing.T) {
	var host = os.Getenv("TRAP_LIVE_HOST")
	if host == "" {
		t.Skip("TRAP_LIVE_HOST not set — live exercise skipped")
	}
	var want = driver.TrapDestination{
		Address:   os.Getenv("TRAP_LIVE_ADDR"),
		Community: os.Getenv("TRAP_LIVE_COMMUNITY"),
	}
	if want.Address == "" || want.Community == "" {
		t.Fatal("TRAP_LIVE_ADDR and TRAP_LIVE_COMMUNITY are both required")
	}

	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("TRAP_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("TRAP_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("TRAP_LIVE_SOCKS_PASS")}
		}
		var d, err = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if err != nil {
			t.Fatalf("socks: %v", err)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}

	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("TRAP_LIVE_USER"),
		Secret:   os.Getenv("TRAP_LIVE_PASS"),
		Why:      "live trap-receiver exercise",
	}})
	if err != nil {
		t.Fatalf("authenticate: %v — transcript %+v", err, adm.Transcript())
	}
	var c = adm.(*Client)
	defer c.Close()

	// --- Before: the exact lines the device carries ---
	var runOut string
	runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		t.Fatalf("pre-read: %v", err)
	}
	var before = parseRunningConfig(runOut, c.host)
	var hadEntry = false
	var r trapReceiver
	for _, r = range before.TrapReceivers {
		if trapEntryConforms(r, want) {
			hadEntry = true
		}
	}
	t.Logf("before: %d receiver(s) %+v, snmp trap events %v",
		len(before.TrapReceivers), before.TrapReceivers, before.SNMPTrapEvents)

	// --- Pose ---
	var res1 driver.TrapResult
	res1, err = c.ApplyTrapReceiver(ctx, want)
	if err != nil {
		t.Fatalf("first ApplyTrapReceiver: %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("first apply: set=%v replaced=%v known=%d notes=%v",
		res1.Set, res1.Replaced, res1.Known, res1.Notes)
	if !hadEntry && !res1.Set {
		t.Fatalf("the device did not carry this recipient and Set stayed false")
	}

	// --- Idempotence: the second call must write NOTHING ---
	var res2 driver.TrapResult
	res2, err = c.ApplyTrapReceiver(ctx, want)
	if err != nil {
		t.Fatalf("second ApplyTrapReceiver: %v", err)
	}
	if res2.Set || res2.Replaced {
		t.Fatalf("second apply must be a no-op: set=%v replaced=%v", res2.Set, res2.Replaced)
	}
	t.Logf("second apply: no-op confirmed (set=%v, notes=%v)", res2.Set, res2.Notes)

	// --- Revert to the exact prior state ---
	var undo []string
	if !hadEntry {
		undo = append(undo, "no snmp-server host "+want.Address+" version 2c "+want.Community)
	}
	if !trapLinkEventsCovered(before.SNMPTrapEvents) {
		undo = append(undo, "no snmp-server enable traps snmp")
		if len(before.SNMPTrapEvents) > 0 {
			undo = append(undo,
				"snmp-server enable traps snmp "+strings.Join(before.SNMPTrapEvents, " "))
		}
	}
	if len(undo) > 0 {
		err = c.runConfigBlock(ctx, undo)
		if err != nil {
			t.Fatalf("revert refused — the device still carries the recipient: %v", err)
		}
		err = c.save(ctx)
		if err != nil {
			t.Fatalf("revert applied but not persisted: %v", err)
		}
	}

	// --- The reread is what proves the revert, not the return codes ---
	runOut, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && runOut == "" {
		t.Fatalf("post-revert read: %v", err)
	}
	var after = parseRunningConfig(runOut, c.host)
	if !hadEntry {
		for _, r = range after.TrapReceivers {
			if trapEntryConforms(r, want) {
				t.Fatalf("revert NOT effective: the recipient is still in the running-config")
			}
		}
	}
	if trapLinkEventsCovered(after.SNMPTrapEvents) != trapLinkEventsCovered(before.SNMPTrapEvents) {
		t.Fatalf("revert left the trap events different from the prior state: %v -> %v",
			before.SNMPTrapEvents, after.SNMPTrapEvents)
	}
	t.Logf("after revert: %d receiver(s) %+v, events %v — device back to its prior lines",
		len(after.TrapReceivers), after.TrapReceivers, after.SNMPTrapEvents)
}

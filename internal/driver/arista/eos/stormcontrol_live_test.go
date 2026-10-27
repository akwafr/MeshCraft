package eos

// The FIRST live-session harness for this range. Every other test in the
// package asserts against a captured fixture; this one opens a real session,
// which is the only thing that can tell "the connector builds the right
// command" from "the command reference says so".
//
// Double-gated (EOS_LIVE_WRITE=1 on top of EOS_LIVE_HOST) because it configures
// a real switch:
//
//	EOS_LIVE_HOST=192.0.2.5 EOS_LIVE_USER=… EOS_LIVE_PASS=… EOS_LIVE_WRITE=1 \
//	EOS_LIVE_SOCKS=127.0.0.1:1080 EOS_LIVE_SOCKS_USER=… EOS_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/arista/eos -run TestLiveWriteStormControl -v

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

type socksDialer struct{ d proxy.Dialer }

func (s socksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var cd, ok = s.d.(proxy.ContextDialer)
	if ok {
		return cd.DialContext(ctx, network, addr)
	}
	return s.d.Dial(network, addr)
}

// TestLiveWriteStormControl exercises [Client.ApplyStormControl] against a real
// 7050SX and reverts.
//
// # Why this is safe on the lab unit
//
// The target defaults to a port with NO TRANSCEIVER FITTED. A storm limiter
// polices traffic crossing the port it is set on, and nothing can cross a cage
// with no optic in it — so the write is real, the device stores and reports it,
// and no frame anywhere is affected. That is a stronger guarantee than "the port
// is down", which a cable could change under us.
//
// # Why it goes through the applier and not the CLI
//
// The three command forms were already sent by hand on 2026-08-08 and the rate
// spelling settled (cf. stormcontrol.go's header). That proved the CLI. It said
// nothing about whether ApplyStormControl builds those forms, orders them
// deterministically, or believes its own verification re-read — which is the
// distinction that decided the EdgeSwitch case, where a hand-sent command was
// mistaken for a proven connector for most of a day.
func TestLiveWriteStormControl(t *testing.T) {
	if os.Getenv("EOS_LIVE_WRITE") != "1" {
		t.Skip("EOS_LIVE_WRITE not set — write exercise skipped")
	}
	var host = os.Getenv("EOS_LIVE_HOST")
	if host == "" {
		t.Skip("EOS_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("EOS_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("EOS_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("EOS_LIVE_SOCKS_PASS")}
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
		Username: os.Getenv("EOS_LIVE_USER"), Secret: os.Getenv("EOS_LIVE_PASS"),
		Why: "live storm-control exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v — transcript %+v", err, adm.Transcript())
	}
	defer c.Close()

	var port = os.Getenv("EOS_LIVE_STORM_PORT")
	if port == "" {
		port = "ET1" // no transceiver fitted on the lab core01.lab
	}

	// --- 0) Refuse to run on a device already policing something.
	var before driver.StormControlState
	before, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl before: %v", err)
	}
	var p driver.StormControlPortState
	for _, p = range before.Ports {
		var l driver.StormLimit
		for _, l = range p.Limits {
			if l.Enabled {
				t.Fatalf("%s already carries a %s limiter at %v — refusing to run, "+
					"because the revert would remove a limiter this test did not install",
					p.Port, l.Class, l.Rate)
			}
		}
	}
	t.Logf("before: %d port(s) reported, none limited", len(before.Ports))

	// --- The revert, armed BEFORE the first write.
	//
	// Raw config rather than the applier: [driver.StormControlApplier] expresses
	// removal as Enabled:false, which IS available here — but the revert must
	// also work when the apply failed halfway, and "no storm-control <class>"
	// is idempotent on a class that was never set.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var rerr = c.runConfigBlock(rctx, []string{
			"interface " + cliPortName(port),
			"no storm-control broadcast",
			"no storm-control multicast",
			"no storm-control unknown-unicast",
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s may still be policing traffic. "+
				"Clear it by hand.", rerr, port)
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
					t.Errorf("REVERT INCOMPLETE: %s still limits %s at %v",
						q.Port, l.Class, l.Rate)
				}
			}
		}
		t.Logf("revert: verified, nothing limited")
	}()

	// --- 1) All three classes, through the applier.
	//
	// 0.5 % is deliberate on the multicast class: it is the fraction the hardware
	// capture showed EOS preserves, and the one an integer-rounding bug would
	// silently turn into 0 or 1.
	var res ApplyResult
	res, err = c.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{
				{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
				{Class: driver.StormMulticast, Enabled: true, Rate: 0.5, Unit: driver.StormRatePercent},
				{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 10, Unit: driver.StormRatePercent},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ApplyStormControl: %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("apply: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.Skipped) > 0 {
		t.Errorf("this platform names all three classes as itself and something was "+
			"refused: %v", res.Skipped)
	}

	// --- 2) The device must agree, read back through the contract.
	var after driver.StormControlState
	after, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl after: %v", err)
	}
	var want = map[driver.StormClass]float64{
		driver.StormBroadcast:      5,
		driver.StormMulticast:      0.5,
		driver.StormUnknownUnicast: 10,
	}
	var found bool
	for _, p = range after.Ports {
		if p.Port != port {
			continue
		}
		found = true
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
			if got.Unit != driver.StormRatePercent {
				t.Errorf("%s %s unit = %q, want percent", port, class, got.Unit)
			}
		}
	}
	if !found {
		t.Errorf("%s does not appear in the read at all: %+v", port, after.Ports)
	}

	// --- 3) IDEMPOTENCE, the property the rate spelling exists to protect.
	//
	// Re-applying the same plan must write NOTHING: EOS prints "5" for a rate
	// sent as "5.00", so a connector comparing text would rewrite these three
	// lines on every pass, for ever.
	var again ApplyResult
	again, err = c.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{
				{Class: driver.StormBroadcast, Enabled: true, Rate: 5.00, Unit: driver.StormRatePercent},
				{Class: driver.StormMulticast, Enabled: true, Rate: 0.5, Unit: driver.StormRatePercent},
				{Class: driver.StormUnknownUnicast, Enabled: true, Rate: 10, Unit: driver.StormRatePercent},
			},
		}},
	})
	if err != nil {
		t.Fatalf("ApplyStormControl (second pass): %v", err)
	}
	t.Logf("second pass: written=%v skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) > 0 {
		t.Errorf("the same plan rewrote %v on a second pass — the apply does not "+
			"converge, and every automatic run would touch this device", again.PortsWritten)
	}

	// --- 4) REMOVAL through the applier, which is its own instruction.
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
		var b, read = p.Limit(driver.StormBroadcast)
		if read && b.Enabled {
			t.Errorf("broadcast was removed through the applier and still reads at %v", b.Rate)
		}
		// The other two must SURVIVE: a plan naming one class must not disturb
		// the classes it did not mention (driver/stormcontrol.go — a class absent
		// from Limits is NOT MANAGED).
		var m, mread = p.Limit(driver.StormMulticast)
		if !mread || !m.Enabled {
			t.Errorf("removing broadcast also removed the multicast limiter — a class " +
				"the plan never named")
		}
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

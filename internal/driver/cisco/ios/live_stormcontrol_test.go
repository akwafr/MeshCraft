package ios

// A LIVE exercise of [Client.ApplyStormControl] against a real Catalyst,
// through the controller's SOCKS route.
//
// Same discipline as live_trap_test.go next door, and the same reason: a
// capability is flipped only after the write has been exercised on hardware, and
// this is that proof, repeatable rather than a story in a commit message. It
// reuses that file's liveSocksDialer.
//
// IT LEAVES THE DEVICE AS IT FOUND IT: apply → apply again (the second must
// write NOTHING) → remove one class through the applier → revert → re-read that
// proves it.
//
//	STORM_LIVE_HOST=192.0.2.2 STORM_LIVE_USER=mcadmin STORM_LIVE_PASS=… \
//	STORM_LIVE_WRITE=1 \
//	STORM_LIVE_SOCKS=127.0.0.1:1080 STORM_LIVE_SOCKS_USER=… STORM_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/cisco/ios -run TestLiveStormControl -v

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

// TestLiveStormControlIsIdempotentAndRevertible exercises the applier on a port
// with nothing plugged into it.
//
// # Why the target must be an empty port, not merely a down one
//
// A storm limiter polices traffic crossing the port it sits on. On a port with
// no cable there is nothing to police, so the write is real and the consequence
// is nil — and unlike "the port is down", a cable cannot appear mid-run and
// change that.
//
// # What the second apply is for
//
// It is the step that catches the failure this platform is most exposed to. The
// 2960-X RE-SPELLS what it is given: "pps 1000" comes back "pps 1k", "bps
// 2000000" comes back "bps 2m", "pps 1500" comes back "pps 1.5k", while
// "5.00" is kept verbatim (captured 2026-08-08, cf. stormcontrol.go). A
// connector comparing the rate as TEXT would therefore see every limiter as
// different from what it asked for, and rewrite all of them on every automatic
// pass, for ever. One apply cannot show that; two can.
func TestLiveStormControlIsIdempotentAndRevertible(t *testing.T) {
	if os.Getenv("STORM_LIVE_WRITE") != "1" {
		t.Skip("STORM_LIVE_WRITE not set — write exercise skipped")
	}
	var host = os.Getenv("STORM_LIVE_HOST")
	if host == "" {
		t.Skip("STORM_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("STORM_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("STORM_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("STORM_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("STORM_LIVE_USER"),
		Secret:   os.Getenv("STORM_LIVE_PASS"),
		Why:      "live storm-control exercise",
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	defer c.Close()

	var port = os.Getenv("STORM_LIVE_PORT")
	if port == "" {
		port = "GE1" // Gi1/0/1 on the lab 2960-X: access port, nothing plugged in
	}

	// --- 0) Refuse to run against a device already policing something.
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
				t.Fatalf("%s already limits %s at %v %s — refusing to run, because the "+
					"revert would remove a limiter this test did not install",
					p.Port, l.Class, l.Rate, l.Unit)
			}
		}
	}
	t.Logf("before: %d port(s) reported, none limited", len(before.Ports))

	// --- The revert, armed before the first write.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var rerr = c.runConfigBlock(rctx, []string{
			"interface " + cliPortName(port),
			"no storm-control broadcast level",
			"no storm-control multicast level",
		})
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

		// THE REVERT MUST BE SAVED, and this is not hypothetical caution — the
		// first run of this test left the device in exactly the state it warns
		// about (2026-08-08).
		//
		// ApplyStormControl persists ("write memory", cf. the transcript it
		// emits), so by the time this runs the STARTUP configuration already
		// carries the limiters. The undo above is raw config and touches the
		// running configuration only. Without this line the running config reads
		// clean, every assertion passes, and the switch boots back into a limiter
		// nobody configured — which is what happened: startup-config was found
		// holding "storm-control multicast level pps 1.5k" on a port whose
		// running config was empty.
		var werr = c.save(rctx)
		if werr != nil {
			t.Errorf("REVERT NOT SAVED: %v — the running configuration is clean but "+
				"flash still carries the limiters. This switch will come back with "+
				"them after a reload; save it by hand.", werr)
			return
		}
		t.Logf("revert: verified and SAVED, nothing limited")
	}()

	// --- 1) Two classes, two DIFFERENT units, through the applier.
	//
	// pps 1500 is chosen deliberately: the device prints it back "1.5k", the
	// one form that needs a float parse before the multiplier. Unknown-unicast is
	// not in the plan — this connector refuses that class on purpose, because
	// "storm-control unicast" covers ALL unicast on this platform.
	var plan = driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{
				{Class: driver.StormBroadcast, Enabled: true, Rate: 5, Unit: driver.StormRatePercent},
				{Class: driver.StormMulticast, Enabled: true, Rate: 1500, Unit: driver.StormRatePPS},
			},
		}},
	}
	var res ApplyResult
	res, err = c.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyStormControl: %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("apply: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)

	// --- 2) The device must agree.
	var after driver.StormControlState
	after, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl after: %v", err)
	}
	var seen bool
	for _, p = range after.Ports {
		if p.Port != port {
			continue
		}
		seen = true
		var b, bok = p.Limit(driver.StormBroadcast)
		if !bok || !b.Enabled || b.Rate != 5 || b.Unit != driver.StormRatePercent {
			t.Errorf("broadcast reads %+v (ok=%v), want 5 percent", b, bok)
		}
		var m, mok = p.Limit(driver.StormMulticast)
		if !mok || !m.Enabled || m.Unit != driver.StormRatePPS {
			t.Errorf("multicast reads %+v (ok=%v), want pps", m, mok)
		}
		if mok && m.Rate != 1500 {
			t.Errorf("multicast rate = %v, want 1500 — the device prints \"1.5k\" and "+
				"reading that as 1.5 is a limiter a thousand times tighter", m.Rate)
		}
	}
	if !seen {
		t.Errorf("%s does not appear in the read: %+v", port, after.Ports)
	}

	// --- 3) IDEMPOTENCE. The same plan must write nothing.
	var again ApplyResult
	again, err = c.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyStormControl (second pass): %v", err)
	}
	t.Logf("second pass: written=%v skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) > 0 {
		t.Errorf("the same plan rewrote %v on a second pass — with \"1500\" printing "+
			"back as \"1.5k\", this is exactly where a text comparison would loop for "+
			"ever", again.PortsWritten)
	}

	// --- 4) Removal through the applier, and only of the class named.
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
		var b, bok = p.Limit(driver.StormBroadcast)
		if bok && b.Enabled {
			t.Errorf("broadcast was removed through the applier and still reads %v", b.Rate)
		}
		var m, mok = p.Limit(driver.StormMulticast)
		if !mok || !m.Enabled {
			t.Error("removing broadcast also removed multicast — a class the plan " +
				"never named (driver/stormcontrol.go: absent means NOT MANAGED)")
		}
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

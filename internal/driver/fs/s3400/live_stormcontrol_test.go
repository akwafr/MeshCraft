package s3400

// A LIVE exercise of [Client.ApplyStormControl], reusing liveTarget from
// live_probe_test.go.
//
// Double-gated (S3400_LIVE_WRITE=1 on top of S3400_LIVE_HOST) because it
// configures a real switch, and it puts the port back.
//
//	S3400_LIVE_HOST=192.0.2.3 S3400_LIVE_USER=… S3400_LIVE_PASS=… \
//	S3400_LIVE_WRITE=1 \
//	S3400_LIVE_SOCKS=127.0.0.1:1080 S3400_LIVE_SOCKS_USER=… S3400_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/fs/s3400 -run TestLiveStormControl -v

import (
	"context"
	"os"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// TestLiveStormControlIsIdempotentAndRevertible drives the applier and proves
// the three things a single apply cannot.
//
// # The forms this connector believes in were WRONG until 2026-08-08
//
// A hand capture that day established that the bare spelling
// ("storm-control broadcast 500") does not exist on this firmware — it answers
// "Unknown command" — and, more seriously, that the REMOVAL this connector
// emitted ("no storm-control <class>") is refused too: the accepted form is
// "no storm-control <class> threshold", with no rate. Both were fixed. This
// exercise is what turns that fix from a plausible reading of a transcript into
// something a device has agreed to.
//
// # Why an idempotence pass, and why a removal pass
//
// The first catches a connector that cannot converge — it re-writes a correct
// line on every automatic run. The second is the half a capture cannot reach at
// all: the removal is a DIFFERENT instruction from the set, it was the one that
// was broken here, and a test that only ever adds limiters would have passed
// against the broken build.
func TestLiveStormControlIsIdempotentAndRevertible(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" {
		t.Skip("S3400_LIVE_WRITE not set — write exercise skipped")
	}
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var adm, err = (&Driver{}).Authenticate(ctx, tgt, cands)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	defer c.Close()

	var port = os.Getenv("S3400_LIVE_SCRATCH_PORT")
	if port == "" {
		port = "GE1" // g0/1: link down on the lab unit
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
				t.Fatalf("%s already limits %s at %v — refusing to run, because the "+
					"revert would remove a limiter this test did not install",
					p.Port, l.Class, l.Rate)
			}
		}
	}
	t.Logf("before: %d port(s) reported, none limited", len(before.Ports))

	// --- The revert, armed before the first write.
	//
	// It uses the connector's OWN builders rather than hand-written lines, and
	// that is deliberate after what happened on 2026-08-08: the hand revert of
	// the capture session failed because the connector's removal form was wrong,
	// and reverting through the builders is what makes a broken removal fail the
	// test instead of quietly leaving the device configured.
	//
	// ApplyStormControl does NOT persist on this platform, so there is no
	// startup-config to put back — unlike cisco/ios, where the same omission left
	// a limiter in flash earlier the same day.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var rcmds, nerr = stormPortCmds(c.cages, stormFormThreshold, port,
			[]driver.StormLimit{
				{Class: driver.StormBroadcast, Enabled: false},
				{Class: driver.StormMulticast, Enabled: false},
			})
		if nerr != nil {
			t.Errorf("REVERT NOT EVEN SENT: %v — %s may still be policing", nerr, port)
			return
		}
		var rerr = c.configBlock(rctx, rcmds)
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s may still be policing. Clear it by hand "+
				"with \"no storm-control <class> threshold\".", rerr, port)
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
		t.Logf("revert: verified, nothing limited")
	}()

	// --- 1) Both expressible classes. Unknown-unicast is refused on this
	// lineage (the keyword is "unicast" and its breadth is unestablished), so
	// asking for it here would be testing the refusal, not the write.
	var plan = driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{
				{Class: driver.StormBroadcast, Enabled: true, Rate: 500, Unit: driver.StormRatePPS},
				{Class: driver.StormMulticast, Enabled: true, Rate: 400, Unit: driver.StormRatePPS},
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
	var want = map[driver.StormClass]float64{
		driver.StormBroadcast: 500,
		driver.StormMulticast: 400,
	}
	var seen bool
	for _, p = range after.Ports {
		if p.Port != port {
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
			if got.Unit != driver.StormRatePPS {
				t.Errorf("%s %s unit = %q, want pps — this lineage's threshold is a "+
					"packet rate and nothing converts", port, class, got.Unit)
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
			"converge and every automatic run would touch this device", again.PortsWritten)
	}

	// --- 4) REMOVAL, the instruction that was broken until today.
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
			t.Errorf("broadcast was removed through the applier and still reads %v — "+
				"the \"no storm-control <class> threshold\" form did not take", b.Rate)
		}
		var m, mok = p.Limit(driver.StormMulticast)
		if !mok || !m.Enabled {
			t.Error("removing broadcast also removed multicast, a class the plan " +
				"never named (driver/stormcontrol.go: absent means NOT MANAGED)")
		}
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

package s3400

// LIVE exercise of the TWO spanning-tree modes this connector refuses: "sstp"
// and "mstp".
//
// WHY IT EXISTS. The bridge priority command on this platform is MODE-PREFIXED
// — "spanning-tree rstp priority 36864", not "spanning-tree priority" —
// so a mode the connector has never run is a mode in which it would configure
// the wrong protocol's bridge, silently. [Client.ApplySpanningTree] therefore
// refuses everything but RSTP and [Capabilities] declares only "rstp". The
// only thing that can lift that refusal is running each mode on hardware and
// reading a written priority back, which is what this does.
//
// WHY IT RECONNECTS. Changing the STP mode re-converges the tree, and this
// unit's ONLY uplink (TGigaEthernet0/1 → core01) is also the path this session
// arrives on. A mode change can therefore cut the connection that issued it.
// Every stage is written to survive that: the session is re-opened when it dies,
// and the revert is attempted from a FRESH session if the original is gone. A
// harness that assumed one durable session would leave the lab switch in a mode
// nobody chose the first time the tree blinked.
//
// Triple-gated (S3400_LIVE_HOST, S3400_LIVE_WRITE=1, S3400_LIVE_STPMODE=1).
// Nothing is persisted — "write" is never sent.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// openLive opens a privileged session, or fails the test.
func openLive(t *testing.T, ctx context.Context) *Client {
	t.Helper()
	var tgt, cands = liveTarget(t)
	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	return c
}

// reopenIfDead replaces a session whose transport died, waiting for the tree to
// settle first. Returns the session to keep using.
func reopenIfDead(t *testing.T, ctx context.Context, c *Client, why string) *Client {
	t.Helper()
	var _, err = c.cli.runPaged(ctx, "show version", 15*time.Second)
	if err == nil {
		return c
	}
	t.Logf("session lost after %s (%v) — waiting for the tree, then reconnecting", why, err)
	c.Close()
	var attempt int
	for attempt = 1; attempt <= 6; attempt++ {
		time.Sleep(15 * time.Second)
		var tgt, cands = liveTarget(t)
		var drv = &Driver{}
		var adm, aerr = drv.Authenticate(ctx, tgt, cands)
		var nc, _ = adm.(*Client)
		if aerr == nil {
			t.Logf("reconnected after %d attempt(s)", attempt)
			return nc
		}
		t.Logf("  reconnect %d: %v", attempt, aerr)
	}
	t.Fatalf("could not reconnect after %s — the device may be left in a mode nobody chose", why)
	return nil
}

func TestLiveS3400STPModes(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" || os.Getenv("S3400_LIVE_STPMODE") != "1" {
		t.Skip("S3400_LIVE_WRITE=1 and S3400_LIVE_STPMODE=1 not both set — skipped")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	var c = openLive(t, ctx)
	defer func() { c.Close() }()

	// --- baseline. Everything is compared against these bytes at the end.
	var baseline, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil || strings.TrimSpace(baseline) == "" {
		t.Fatalf("baseline running-config unusable: %v", err)
	}
	liveCapture(t, "stpmodes_baseline_running_config.txt", baseline)
	var before driver0SpanningTree
	before = readSTP(t, ctx, c, "baseline")

	// The priority written in each mode. 36864 is a multiple of 4096 well away
	// from the 32768 default, so "it did not change" is unmistakable, and it
	// is WORSE than the root's 16384 — this switch cannot win the election by
	// accident and take the fleet core's place.
	var wantPrio = 36864

	var mode string
	for _, mode = range []string{modeSSTP, modeMSTP} {
		t.Logf("################ %s ################", mode)

		// 1) THE MODE ALONE. Measured 2026-08-11: "spanning-tree mode <m>" makes
		// this device CLOSE THE SSH SESSION before the CLI answers — the tree
		// reconverges and the only uplink is the session's own path. A second
		// command in the same block is therefore NEVER DELIVERED, which is how the
		// first run of this exercise read back an unchanged 32768 and would have
		// reported "the mode-prefixed priority does not work" about a command the
		// device never received. The mode goes out alone, and the priority waits
		// for a session that survives.
		var cerr = c.configBlock(ctx, []string{"spanning-tree mode " + mode})
		t.Logf("%s: \"spanning-tree mode %s\" → %v", mode, mode, cerr)
		c = reopenIfDead(t, ctx, c, "switching to "+mode)

		// 2) WAIT FOR AN INSTANCE. "No spanning tree instance exists." is what
		// this firmware answers for tens of seconds after a mode change (measured:
		// ~45 s). Writing a priority into that window, or judging one, reads a
		// bridge that is not there yet.
		c = waitForSTPInstance(t, ctx, c, mode)

		// 3) the priority, in the mode's own prefixed form.
		cerr = c.configBlock(ctx, []string{
			fmt.Sprintf("spanning-tree %s priority %d", mode, wantPrio)})
		if cerr != nil {
			t.Errorf("%s: \"spanning-tree %s priority %d\" REFUSED — %v",
				mode, mode, wantPrio, cerr)
		}
		c = reopenIfDead(t, ctx, c, "the "+mode+" priority write")

		// 4) read it back. THIS is the whole point: a priority written in a mode
		// this connector has not run must be visible in the mode's own bridge.
		var st = readSTP(t, ctx, c, mode)
		if st.mode == "" {
			t.Errorf("%s: \"show spanning-tree\" named no protocol", mode)
		}
		if st.bridgePrio != wantPrio {
			t.Errorf("%s: BRIDGE PRIORITY READS %d, wrote %d — the mode-prefixed "+
				"command did not configure this mode's bridge", mode, st.bridgePrio, wantPrio)
		} else {
			t.Logf("%s: priority %d written AND read back on the %s bridge (%s)",
				mode, st.bridgePrio, mode, st.bridgeID)
		}

		// 5) what the configuration stores for this mode.
		var cfg string
		cfg, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
		if err != nil {
			t.Errorf("%s: running-config read: %v", mode, err)
		}
		liveCapture(t, "stpmodes_"+mode+"_running_config.txt", cfg)
		var ln string
		for _, ln = range strings.Split(cfg, "\n") {
			if strings.Contains(ln, "spanning-tree") {
				t.Logf("%s cfg| %s", mode, ln)
			}
		}

		// 6) undo THIS mode's priority and CONFIRM the undo, before the mode
		// change takes the session away again.
		cerr = c.configBlock(ctx, []string{"no spanning-tree " + mode + " priority"})
		if cerr != nil {
			t.Errorf("%s: \"no spanning-tree %s priority\" refused — %v", mode, mode, cerr)
		}
		c = reopenIfDead(t, ctx, c, "the "+mode+" priority removal")
		var back = readSTP(t, ctx, c, mode+"-after-no-priority")
		if back.bridgePrio != before.bridgePrio {
			t.Errorf("%s: priority is %d after \"no … priority\", want the default %d",
				mode, back.bridgePrio, before.bridgePrio)
		} else {
			t.Logf("%s: \"no spanning-tree %s priority\" put the bridge back to %d",
				mode, mode, back.bridgePrio)
		}

		// 7) home to rstp, alone for the same reason as (1).
		cerr = c.configBlock(ctx, []string{"spanning-tree mode " + modeRSTP})
		t.Logf("%s: back to rstp → %v", mode, cerr)
		c = reopenIfDead(t, ctx, c, "returning from "+mode+" to rstp")
		c = waitForSTPInstance(t, ctx, c, modeRSTP)
	}

	// --- back where we started, proven twice: the tree AND the bytes.
	var after = readSTP(t, ctx, c, "final")
	if after.bridgePrio != before.bridgePrio || after.mode != before.mode {
		t.Errorf("the bridge did not come home: %+v, want %+v", after, before)
	} else {
		t.Logf("bridge back to %s priority %d", after.mode, after.bridgePrio)
	}

	var final string
	final, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Errorf("final running-config: %v", err)
	}
	liveCapture(t, "stpmodes_final_running_config.txt", final)
	if final != baseline {
		t.Error("THE RUNNING CONFIG DID NOT COME BACK TO ITS BASELINE")
		var seen = map[string]int{}
		var l string
		for _, l = range strings.Split(baseline, "\n") {
			seen[l]++
		}
		for _, l = range strings.Split(final, "\n") {
			seen[l]--
		}
		for l = range seen {
			if seen[l] != 0 {
				t.Errorf("  diff %+d %q", -seen[l], l)
			}
		}
	} else {
		t.Log("revert proven: final running-config BYTE-IDENTICAL to the baseline")
	}
}

// driver0SpanningTree is the handful of fields this exercise compares. The
// connector's own parser produces them, so a mode it cannot read shows up here
// as an empty mode rather than as a passing test.
type driver0SpanningTree struct {
	mode       string
	bridgeID   string
	bridgePrio int
	enabled    bool
}

func readSTP(t *testing.T, ctx context.Context, c *Client, label string) driver0SpanningTree {
	t.Helper()
	var out, err = c.cli.runPaged(ctx, "show spanning-tree", 60*time.Second)
	if err != nil {
		t.Errorf("%s: show spanning-tree: %v", label, err)
	}
	t.Logf("=== show spanning-tree (%s) ===\n%s", label, out)
	liveCapture(t, "stpmodes_"+label+"_show_spanning_tree.txt", out)
	var st = parseSpanningTree(out)
	return driver0SpanningTree{
		mode: st.Mode, bridgeID: st.BridgeID,
		bridgePrio: st.BridgePriority, enabled: st.Enabled,
	}
}

// TestLiveS3400STPBackToRSTP puts the bridge back into RSTP and PROVES it, from
// a fresh session, retrying across the reconvergence that cuts the link it
// arrives on.
//
// It exists because the exercise above measured exactly that hazard: on this
// unit "spanning-tree mode <m>" drops the SSH session before the CLI answers,
// so a revert issued in the same block as the read can be lost. This one does
// nothing but the revert, and reads it back.
func TestLiveS3400STPBackToRSTP(t *testing.T) {
	if os.Getenv("S3400_LIVE_STPREVERT") != "1" {
		t.Skip("S3400_LIVE_STPREVERT not set — skipped")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var c = openLive(t, ctx)
	defer func() { c.Close() }()

	var st = readSTP(t, ctx, c, "before-revert")
	t.Logf("before: mode=%q priority=%d", st.mode, st.bridgePrio)

	var attempt int
	for attempt = 1; attempt <= 5; attempt++ {
		var cerr = c.configBlock(ctx, []string{"spanning-tree mode " + modeRSTP})
		t.Logf("attempt %d: \"spanning-tree mode rstp\" → %v", attempt, cerr)
		c = reopenIfDead(t, ctx, c, "the mode change")
		st = readSTP(t, ctx, c, fmt.Sprintf("after-attempt-%d", attempt))
		if strings.EqualFold(st.mode, modeRSTP) {
			t.Logf("bridge is back on RSTP (priority %d)", st.bridgePrio)
			break
		}
	}
	if !strings.EqualFold(st.mode, modeRSTP) {
		t.Fatalf("STILL NOT RSTP after %d attempts: %+v", attempt, st)
	}

	var cfg, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Fatalf("running-config: %v", err)
	}
	if !strings.Contains(cfg, "spanning-tree mode rstp") {
		t.Errorf("the configuration does not carry \"spanning-tree mode rstp\"")
	}
	if strings.Contains(cfg, "spanning-tree mstp priority") ||
		strings.Contains(cfg, "spanning-tree sstp priority") {
		t.Errorf("a mode-prefixed priority survived the exercise")
	}
	liveCapture(t, "stpmodes_reverted_running_config.txt", cfg)
	var ln string
	for _, ln = range strings.Split(cfg, "\n") {
		if strings.Contains(ln, "spanning-tree") {
			t.Logf("cfg| %s", ln)
		}
	}
}

// waitForSTPInstance blocks until "show spanning-tree" stops answering
// "No spanning tree instance exists.".
//
// MEASURED 2026-08-11 on poefs3400lab: after "spanning-tree mode <m>" this
// firmware answers exactly that sentence for tens of seconds — three successive
// reads over ~45 s — before the bridge appears. It matters beyond this harness:
// [parseSpanningTree] extracts nothing from that sentence, so a read taken in
// that window yields Enabled=false with an empty BridgeID, and the connector
// would report a spanning tree that is OFF on a switch that is merely
// reconverging.
func waitForSTPInstance(t *testing.T, ctx context.Context, c *Client, mode string) *Client {
	t.Helper()
	var attempt int
	for attempt = 1; attempt <= 12; attempt++ {
		var out, err = c.cli.runPaged(ctx, "show spanning-tree", 60*time.Second)
		if err == nil && !strings.Contains(out, "No spanning tree instance exists") &&
			strings.Contains(out, "Bridge ID") {
			t.Logf("%s: an instance exists after %d read(s)", mode, attempt)
			return c
		}
		if err != nil {
			c = reopenIfDead(t, ctx, c, "waiting for the "+mode+" instance")
			continue
		}
		t.Logf("%s: \"No spanning tree instance exists.\" (read %d)", mode, attempt)
		time.Sleep(15 * time.Second)
	}
	t.Errorf("%s: no spanning tree instance appeared", mode)
	return c
}

// TestLiveS3400MSTPPriorityForm hunts the MSTP bridge-priority command.
//
// MEASURED 2026-08-11: "spanning-tree mstp priority 36864" — the form the
// whole "the priority is MODE-PREFIXED" note predicts, and the form that WORKS
// verbatim for rstp and sstp — is answered "Too many parameters" in MSTP. So
// the mode-prefix rule does not extend to MSTP, and the reason is visible in the
// mode's own read: "Bridge ID Priority 32768 (priority 32768 mst-id 0)". The
// priority belongs to an INSTANCE here, not to the bridge.
//
// Each candidate is a WHOLE command. Nothing is probed with "?": an
// interactive shell submits whatever the help leaves on the line, and that is
// how "spanning-tree root super" was once run on the fleet core.
//
// Gated on S3400_LIVE_MSTPFORM=1 on top of the write gate, because it flaps this
// unit's spanning tree twice and each flap cuts the session it arrives on.
func TestLiveS3400MSTPPriorityForm(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" || os.Getenv("S3400_LIVE_MSTPFORM") != "1" {
		t.Skip("S3400_LIVE_WRITE=1 and S3400_LIVE_MSTPFORM=1 not both set — skipped")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	var c = openLive(t, ctx)
	defer func() { c.Close() }()

	var baseline, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil || strings.TrimSpace(baseline) == "" {
		t.Fatalf("baseline unusable: %v", err)
	}

	var cerr = c.configBlock(ctx, []string{"spanning-tree mode " + modeMSTP})
	t.Logf("into mstp → %v", cerr)
	c = reopenIfDead(t, ctx, c, "switching to mstp")
	c = waitForSTPInstance(t, ctx, c, modeMSTP)

	// Reads first — free, and one of them may simply print the grammar's shape.
	var probe string
	for _, probe = range []string{
		"show spanning-tree mst", "show spanning-tree mst 0",
		"show spanning-tree mst configuration", "show spanning-tree detail",
	} {
		var out, rerr = c.cli.runPaged(ctx, probe, 60*time.Second)
		t.Logf("=== %s === (err=%v)\n%s", probe, rerr, out)
		liveCapture(t, "mstp_"+strings.ReplaceAll(probe, " ", "_")+".txt", out)
	}

	var wantPrio = 36864
	var candidates = []string{
		fmt.Sprintf("spanning-tree mstp instance 0 priority %d", wantPrio),
		fmt.Sprintf("spanning-tree mstp 0 priority %d", wantPrio),
		fmt.Sprintf("spanning-tree mst 0 priority %d", wantPrio),
		fmt.Sprintf("spanning-tree mstp instance 0 bridge priority %d", wantPrio),
		fmt.Sprintf("spanning-tree mstp priority 0 %d", wantPrio),
	}
	var accepted []string
	var cand string
	for _, cand = range candidates {
		var e = c.configBlock(ctx, []string{cand})
		if e != nil {
			t.Logf("REFUSED  \"%s\" → %v", cand, e)
			continue
		}
		t.Logf("ACCEPTED \"%s\"", cand)
		accepted = append(accepted, cand)
		c = reopenIfDead(t, ctx, c, "\""+cand+"\"")
		var st = readSTP(t, ctx, c, "mstp-after-"+strings.ReplaceAll(cand, " ", "_"))
		t.Logf("  → bridge priority now %d (want %d)", st.bridgePrio, wantPrio)
		// Undo immediately: "no" of the same form, so the next candidate is
		// judged against a default bridge and not against the previous one.
		var undo = "no " + strings.TrimSuffix(cand, fmt.Sprintf(" %d", wantPrio))
		var ue = c.configBlock(ctx, []string{undo})
		t.Logf("  undo \"%s\" → %v", undo, ue)
		c = reopenIfDead(t, ctx, c, "the undo")
	}
	if len(accepted) == 0 {
		t.Logf("NO CANDIDATE ACCEPTED — the MSTP priority form is still unknown on this firmware")
	}

	var cfg string
	cfg, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err == nil {
		var ln string
		for _, ln = range strings.Split(cfg, "\n") {
			if strings.Contains(ln, "spanning-tree") {
				t.Logf("mstp cfg| %s", ln)
			}
		}
	}

	// --- home, and proven.
	var attempt int
	for attempt = 1; attempt <= 5; attempt++ {
		cerr = c.configBlock(ctx, []string{"spanning-tree mode " + modeRSTP})
		t.Logf("back to rstp attempt %d → %v", attempt, cerr)
		c = reopenIfDead(t, ctx, c, "returning to rstp")
		var st = readSTP(t, ctx, c, fmt.Sprintf("mstpform-home-%d", attempt))
		if strings.EqualFold(st.mode, modeRSTP) {
			break
		}
		time.Sleep(15 * time.Second)
	}
	var final string
	final, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	liveCapture(t, "mstpform_final_running_config.txt", final)
	if final != baseline {
		t.Error("THE RUNNING CONFIG DID NOT COME BACK TO ITS BASELINE")
		var seen = map[string]int{}
		var l string
		for _, l = range strings.Split(baseline, "\n") {
			seen[l]++
		}
		for _, l = range strings.Split(final, "\n") {
			seen[l]--
		}
		for l = range seen {
			if seen[l] != 0 {
				t.Errorf("  diff %+d %q", -seen[l], l)
			}
		}
	} else {
		t.Log("revert proven: final running-config BYTE-IDENTICAL to the baseline")
	}
}

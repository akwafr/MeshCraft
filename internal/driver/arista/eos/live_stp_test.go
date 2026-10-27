package eos

// The PER-PORT half of ApplySpanningTree, driven against a real 7050SX — and the
// deliberate refusal of the other half on this particular switch.
//
// # What this harness does and what it will not do
//
// [Client.ApplySpanningTree] writes three things: an MST REGION, a global MODE,
// and PER-PORT intents. This harness drives the mode line and the per-port
// intents. It refuses to write a region, and the refusal is the point rather
// than a limitation of the code:
//
//   - the only Arista in this lab is core01.lab, the FLEET CORE. The measured
//     cost of one accidental global spanning-tree command there (2026-08-11,
//     "spanning-tree root super") was three devices logging unreachable and
//     ~45 s to full recovery;
//   - a REGION write is exactly the class of change that restarts the MST state
//     machine. core01 is in a region of ONE (name "", revision 0, instance 0
//     holding 1-4094 — re-read 2026-08-11), so every neighbour is already a
//     boundary and a new digest buys nothing measurable, while the downside is
//     four live trunks reconverging at once;
//   - the MODE line, by contrast, is an IDENTITY write here: the switch already
//     carries "spanning-tree mode mstp" (`show running-config | include
//     spanning-tree`, 2026-08-11, one line). Sending the value a config-diff
//     system already holds produces no change notification and no reconvergence,
//     which is what makes running the applier at all acceptable on this box.
//
// So `WritesExtra("stp")` STAYS FALSE on this connector. What this harness
// removes from the unknown is everything around the region: that the applier's
// config-block handling, its ordering and its per-port builder work against real
// EOS rather than against the fake CLI.
//
// # The port
//
// EOS_LIVE_STP_PORT, and the same reasoning as live_portadmin_test.go: a cage
// with `Type == Not Present` has no transceiver, so nothing has ever crossed it.
// Ethernet1 is such a cage on core01. A port with no link also has NO spanning
// tree instance at all on this platform — see the capture below — so a per-port
// intent written there cannot move a tree.
//
// # THE READ-BACK, which is why this file also settles a second entry
//
// `PortsWritten` means SENT for ApplySpanningTree on this connector: the applier
// verifies the BRIDGE level and never the per-port one. Two things were captured
// on 2026-08-11 while preparing this harness, and together they say what a
// per-port verification would have to be built on:
//
//	core01-lab#show spanning-tree interface Ethernet1 detail
//	no spanning tree info available for Ethernet1
//
// — so `show spanning-tree interface <x> detail` CANNOT verify a write on a dark
// port. It answers nothing at all. On a linked port it answers, but what it
// carries is the operational tree ("Port path cost 2000, Port priority 128 …
// Link type is point-to-point by default, Boundary") and it does NOT render
// portfast or bpduguard as such;
//
//	core01-lab#show running-config interfaces Ethernet1
//	interface Ethernet1
//	   switchport trunk allowed vlan 1,42,98-100
//	   switchport mode trunk
//
// — the running configuration renders exactly the lines the applier writes, and
// nothing when nothing was written. THAT is the read-back a per-port
// verification has to use on EOS, and this harness asserts it round-trips.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// stpLiveIntent is the per-port plan this harness writes: one of each kind the
// builder emits, so a form the CLI refuses shows up as a refusal rather than as
// a silence.
func stpLiveIntent(port string) driver.SpanningTreePortIntent {
	var guard = true
	var cost = 20000
	return driver.SpanningTreePortIntent{
		Port:      port,
		Edge:      driver.STPEdgeYes,
		BPDUGuard: &guard,
		PathCost:  &cost,
	}
}

// TestLiveWriteSpanningTreePorts drives ApplySpanningTree with per-port intents
// only, reads the result back out of the running configuration, and restores the
// port to the two lines it started with.
func TestLiveWriteSpanningTreePorts(t *testing.T) {
	var host = os.Getenv("EOS_LIVE_HOST")
	if host == "" {
		t.Skip("EOS_LIVE_HOST not set — live spanning-tree exercise skipped")
	}
	var tgt = paLiveTarget(t, host)
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("EOS_LIVE_USER"), Secret: os.Getenv("EOS_LIVE_PASS"),
		Why: "live spanning-tree exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v — transcript %+v", err, adm.Transcript())
	}
	defer c.Close()

	// --- 0) THE MODE MUST ALREADY BE THE ONE THE PLAN NAMES. Without this the
	// "identity write" claim in the header is an assumption, and the applier
	// would reconverge the fleet core's tree to prove a point about ordering.
	var runOut string
	runOut, err = c.cli.run(ctx, "show running-config | include spanning-tree", cmdTimeout)
	if err != nil && runOut == "" {
		t.Fatalf("show running-config | include spanning-tree: %v", err)
	}
	if !strings.Contains(runOut, "spanning-tree mode mstp") {
		t.Fatalf("this switch does not already carry \"spanning-tree mode mstp\" (%q): "+
			"refusing to run, because the mode line this applier always sends would then be "+
			"a real mode change on a production core rather than the no-op this harness "+
			"relies on", strings.TrimSpace(runOut))
	}

	var port = os.Getenv("EOS_LIVE_STP_PORT")
	if port == "" {
		port = "ET1"
	}
	var status = paReadStatus(ctx, t, c)
	var prior, known = status[port]
	if !known {
		t.Fatalf("%s is not reported by \"show interfaces status\"", port)
	}
	if prior.LinkUp {
		t.Fatalf("%s has a LIVE LINK — refusing: a path cost written on a forwarding port "+
			"can move a tree", port)
	}
	if !paEmptyCage(prior) {
		t.Fatalf("%s has a transceiver fitted (%q) — refusing: a link-down module comes "+
			"back up, and this harness will not gamble on when", port, prior.Type)
	}

	var cliName = cliPortName(port)
	var before string
	before, err = c.cli.run(ctx, "show running-config interfaces "+cliName, cmdTimeout)
	if err != nil && before == "" {
		t.Fatalf("show running-config interfaces %s: %v", cliName, err)
	}
	t.Logf("%s before:\n%s", port, strings.TrimSpace(before))
	if strings.Contains(before, "spanning-tree") {
		t.Fatalf("%s already carries a spanning-tree line: refusing, because the revert "+
			"below would not put this switch back where it was found", port)
	}

	if os.Getenv("EOS_LIVE_WRITE") != "1" {
		t.Skipf("EOS_LIVE_WRITE not set — reconnaissance done (%s is a dark cage with no "+
			"spanning-tree configuration), nothing written", port)
	}

	// --- 1) THE REVERT, armed before the first write and sent as raw
	// configuration: the applier is the thing under test.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var rerr = c.runConfigBlock(rctx, []string{
			"interface " + cliName,
			"no spanning-tree portfast",
			"no spanning-tree bpduguard",
			"no spanning-tree cost",
			"exit",
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s MAY STILL CARRY spanning-tree lines on %s",
				rerr, port, host)
			return
		}
		var back, berr = c.cli.run(rctx, "show running-config interfaces "+cliName, cmdTimeout)
		if berr != nil && back == "" {
			t.Errorf("REVERT NOT VERIFIED: %v", berr)
			return
		}
		if strings.Contains(back, "spanning-tree") {
			t.Errorf("REVERT INCOMPLETE: %s still carries a spanning-tree line:\n%s",
				port, strings.TrimSpace(back))
			return
		}
		t.Logf("revert: verified, %s back to\n%s", port, strings.TrimSpace(back))
	}()

	// --- 2) The apply. Region deliberately ZERO: Region.Configured() is false, so
	// no `spanning-tree mst configuration` block is built at all (stp.go § 1).
	var res ApplyResult
	res, err = c.ApplySpanningTree(ctx, driver.SpanningTreePlan{
		Enabled: true,
		Mode:    "mstp",
		Ports:   []driver.SpanningTreePortIntent{stpLiveIntent(port)},
	})
	if err != nil {
		t.Fatalf("ApplySpanningTree: %v — transcript %+v", err, c.Transcript())
	}
	t.Logf("apply: written=%v skipped=%v notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten = %v, want [%s]", res.PortsWritten, port)
	}

	// --- 3) THE READ-BACK. This is the assertion the file exists for: the three
	// per-port forms this connector emits are rendered VERBATIM by the running
	// configuration, which is the only per-port verification EOS offers.
	var after string
	after, err = c.cli.run(ctx, "show running-config interfaces "+cliName, cmdTimeout)
	if err != nil && after == "" {
		t.Fatalf("show running-config interfaces %s: %v", cliName, err)
	}
	t.Logf("%s after:\n%s", port, strings.TrimSpace(after))
	var want string
	for _, want = range []string{
		"spanning-tree portfast",
		"spanning-tree bpduguard enable",
		"spanning-tree cost 20000",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("\"%s\" is not rendered by \"show running-config interfaces %s\" — "+
				"either the CLI refused it silently or this platform spells the read-back "+
				"differently, and a per-port verification built on this text would report a "+
				"write it never made", want, cliName)
		}
	}

	// --- 4) A dark port has NO spanning-tree instance, whatever was configured on
	// it. Captured on 2026-08-11 and asserted here so the next person does not
	// build a verification on this command.
	var detail string
	detail, err = c.cli.run(ctx, "show spanning-tree interface "+cliName+" detail", cmdTimeout)
	if err != nil && detail == "" {
		t.Fatalf("show spanning-tree interface %s detail: %v", cliName, err)
	}
	t.Logf("%s spanning-tree detail:\n%s", port, strings.TrimSpace(detail))
	if !strings.Contains(strings.ToLower(detail), "no spanning tree info available") {
		t.Logf("NOTE: this train answers \"show spanning-tree interface %s detail\" on a "+
			"dark port. The 2026-08-11 capture on EOS 4.28.13.1M did not", cliName)
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

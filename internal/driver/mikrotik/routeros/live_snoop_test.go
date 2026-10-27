package routeros

// A LIVE exercise of [Client.ApplyDHCPSnooping] against the lab CRS326-24G-2S+.
//
// # WHY THIS RUNS FROM A HARNESS AND NOT FROM THE API
//
// `POST /api/devices/{id}/dhcp-snooping/apply` refuses this platform with 422
// ("this platform does not declare DHCP snooping as writable") because
// WritesExtra("dhcp_snooping") is false — cf. capabilities.go. That gate is
// RIGHT and this file does not touch it: the flag must follow the evidence, not
// precede it. So the only way to exercise the write is to call the applier
// directly, which is what cisco/ios did one range over
// (cisco/ios/live_snoop_test.go, preceded by its live_snoopprobe_test.go). This
// is the same shape.
//
// WHAT A GREEN RUN WOULD LICENSE. It would license flipping
// `WritableExtras["dhcp_snooping"]` to true, with the evidence written beside it
// in the same style as `storm_control`: the gesture was SENT to 192.0.2.54, the
// bridge flag and the port flag were confirmed FROM A SECOND SESSION, a second
// identical apply wrote nothing, and the two menus came back to the opening
// snapshot field for field. It would NOT license any claim about a bridge that
// is already snooping, about a device whose own address is a lease, about
// per-VLAN snooping (RouterOS has no such level — cf. dhcpsnoop.go), or about
// any RouterOS build other than the one this ran against. Nothing here flips
// anything; running it is the operator's call, and the flip is a separate commit.
//
// # THE PLAN, AND WHERE IT COMES FROM
//
// TestLiveProbeSnooping was run READ-ONLY against 192.0.2.54 on 2026-08-10 and
// reported: one bridge named `bridge` (vlan-filtering=true, dhcp-snooping=false,
// protocol-mode=mstp); 26 bridge ports, ALL trusted=false; exactly ONE cabled
// port, sfp-sfpplus2 (ether1‥24 and sfp-sfpplus1 dark); `/ip/dhcp-client` EMPTY,
// so this switch's own address is STATIC and a bad plan cannot cost it its
// management address; `/ip/dhcp-server` EMPTY, so DHCP arrives from upstream
// through sfp-sfpplus2.
//
// Hence the only safe plan: TRUST sfp-sfpplus2, THE REAL UPLINK, and only then
// enable `dhcp-snooping` on `bridge`. The twenty-four dark access ports stay
// untrusted — that is not an omission, that IS the protection. The applier
// already writes in that order (trust first, feature last, cf. planDHCPSnoop),
// and the order is load-bearing: snooping turned on before the uplink is trusted
// is a DHCP outage for as long as the window lasts.
//
// PORT NAMES ARE THE DEVICE'S HERE. Unlike cisco/ios, where the plan had to say
// `XG2` and the connector translated it to TenGigabitEthernet1/0/2, planDHCPSnoop
// matches `plan.Ports[].Port` against `/interface/bridge/port` `interface`
// verbatim. `sfp-sfpplus2` is what goes in the plan.
//
// # THE CONSTRAINT THAT SHAPES THE TEARDOWN
//
// RouterOS is [driver.CommitImmediate]: there is no running/startup split, no
// `write memory` to forget, and no reboot that undoes a `set`. And the applier
// CANNOT undo its own enable — dhcpsnoop_apply.go is explicit that it never
// turns the feature off, because "clearing dhcp-snooping on the bridge because
// the last trusted port went away would strip protection from every port on it,
// and no screen asked for that". That rule is right for production and it is
// exactly why the harness must clean up behind itself.
//
// So the teardown below goes through the REST LAYER, not through the applier,
// and it is armed in a defer BEFORE the first write. It restores from a
// SNAPSHOT taken at the top — the bridge row and all 26 port rows — and not from
// an assumption that everything was false, because a flag this exercise did not
// set is somebody's configuration.
//
//	ROS_LIVE_HOST=192.0.2.54 ROS_LIVE_USER=… ROS_LIVE_PASS=… \
//	ROS_LIVE_SNOOP_WRITE=1 \
//	ROS_LIVE_SOCKS=127.0.0.1:1080 ROS_LIVE_SOCKS_USER=… ROS_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/mikrotik/routeros -run TestLiveDHCPSnoopingRoundTrip -v
//
// DOUBLE-GATED, and the second gate is its own variable rather than the
// ROS_LIVE_WRITE the storm-control exercise uses. A storm limiter on a dark port
// polices nothing; this one moves a whole bridge. Somebody re-running the
// storm-control round trip must not enable DHCP snooping as a side effect.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// snoopUplink is the ONLY port this exercise trusts, and it is a CONSTANT rather
// than an environment variable on purpose.
//
// The storm-control sibling takes its target from ROS_LIVE_STORM_PORT because
// any dark port will do there. Here the target is the one thing the whole safety
// argument rests on: trusting the real uplink is what keeps upstream DHCP
// flowing once the bridge starts inspecting. A knob pointing this at another
// port would be a knob for turning a proven plan into an unreasoned one, so
// there is none — and the refusals below check that the device still agrees this
// is the uplink.
const snoopUplink = "sfp-sfpplus2"

// snoopPortFact is one bridge-port row reduced to the fields this exercise reads
// and writes.
type snoopPortFact struct {
	Iface   string
	RowID   string
	Trusted bool
}

// snoopFacts is the two menus the applier touches, reduced to what a before/after
// comparison needs: `dhcp-snooping` on the bridge, `trusted` on each of its ports.
//
// NARROW BY DESIGN, unlike stormSnapshot next door, which compares every field of
// every record. That one is whole-table because a whole-page POST can zero a
// field nobody named; here the writes are per-record `set`s carrying exactly one
// key, and the teardown must be able to RESTORE what it compares — which means
// comparing only fields it knows how to write back. The fields outside this
// struct are not watched, and saying so is better than implying they are.
type snoopFacts struct {
	BridgeName string
	BridgeID   string
	Snooping   bool
	// Ports in the device's own faceplate order, bridge members only.
	Ports []snoopPortFact
}

// port returns the fact for one interface name.
func (f snoopFacts) port(iface string) (snoopPortFact, bool) {
	var p snoopPortFact
	for _, p = range f.Ports {
		if p.Iface == iface {
			return p, true
		}
	}
	return snoopPortFact{}, false
}

// trustedNames lists the ports reading trusted=true, sorted.
func (f snoopFacts) trustedNames() []string {
	var out []string
	var p snoopPortFact
	for _, p = range f.Ports {
		if p.Trusted {
			out = append(out, p.Iface)
		}
	}
	sort.Strings(out)
	return out
}

// readSnoopFacts reads the bridge and its ports through the SAME two menus and
// the SAME bridge choice (pickBridge) the applier uses.
//
// Returning an error rather than taking *testing.T: the teardown calls this from
// inside a defer, where a t.Fatalf would abandon the rest of the restoration —
// and abandoning a restoration half-done on a platform that persists immediately
// is the one outcome this file exists to avoid.
func readSnoopFacts(ctx context.Context, c *Client) (snoopFacts, error) {
	var out snoopFacts
	var bridges, err = c.List(ctx, "interface/bridge")
	if err != nil {
		return out, fmt.Errorf("list interface/bridge: %w", err)
	}
	if len(bridges) == 0 {
		return out, fmt.Errorf("no bridge on this device")
	}
	var br = pickBridge(bridges)
	out.BridgeName = br.Get("name")
	out.BridgeID = br.ID()
	out.Snooping = br.Bool("dhcp-snooping")

	var rows []Row
	if rows, err = c.List(ctx, "interface/bridge/port"); err != nil {
		return out, fmt.Errorf("list interface/bridge/port: %w", err)
	}
	var r Row
	for _, r = range rows {
		// Same filter as foldDHCPSnoop and planDHCPSnoop: a row naming another
		// bridge belongs to another switching table.
		if r.Get("bridge") != "" && out.BridgeName != "" && r.Get("bridge") != out.BridgeName {
			continue
		}
		var name = r.Get("interface")
		if name == "" {
			continue
		}
		out.Ports = append(out.Ports, snoopPortFact{
			Iface:   name,
			RowID:   r.ID(),
			Trusted: r.Bool("trusted"),
		})
	}
	return out, nil
}

// diffSnoopFacts reports every field that moved, one line per subject.
//
// The bridge line is prefixed `[bridge]` and port lines with the interface name,
// so a caller can assert not merely THAT something moved but that the right two
// things did and nothing else.
func diffSnoopFacts(before, after snoopFacts) []string {
	var out []string
	if before.Snooping != after.Snooping {
		out = append(out, fmt.Sprintf("[bridge] %s dhcp-snooping: %s → %s",
			before.BridgeName, boolWord(before.Snooping), boolWord(after.Snooping)))
	}
	var names []string
	var seen = map[string]bool{}
	var p snoopPortFact
	for _, p = range before.Ports {
		names = append(names, p.Iface)
		seen[p.Iface] = true
	}
	for _, p = range after.Ports {
		if !seen[p.Iface] {
			names = append(names, p.Iface)
		}
	}
	var n string
	for _, n = range names {
		var b, hadB = before.port(n)
		var a, hadA = after.port(n)
		switch {
		case hadB && !hadA:
			out = append(out, n+": VANISHED from the bridge")
		case !hadB && hadA:
			out = append(out, n+": APPEARED on the bridge")
		case b.Trusted != a.Trusted:
			out = append(out, fmt.Sprintf("%s trusted: %s → %s",
				n, boolWord(b.Trusted), boolWord(a.Trusted)))
		}
	}
	return out
}

// snoopManualFix renders the console lines that put the device back by hand.
//
// Printed whenever the teardown cannot finish. A harness that persists
// immediately and then fails to clean up owes the operator the exact commands,
// not "check the bridge": the person reading the failure may be reaching this
// switch over SOCKS at the end of a run they did not start.
func snoopManualFix(want snoopFacts) []string {
	var out = []string{fmt.Sprintf(
		"/interface/bridge set [find name=%q] dhcp-snooping=%s",
		want.BridgeName, boolWord(want.Snooping))}
	var p snoopPortFact
	for _, p = range want.Ports {
		out = append(out, fmt.Sprintf(
			"/interface/bridge/port set [find interface=%q] trusted=%s",
			p.Iface, boolWord(p.Trusted)))
	}
	return out
}

// restoreSnoop puts the two menus back to `want`, at the REST layer.
//
// NOT THROUGH THE APPLIER, and that is forced rather than chosen:
// ApplyDHCPSnooping never writes `dhcp-snooping=false` (dhcpsnoop_apply.go says
// so and TestPlanNeverTurnsTheFeatureOff holds it to it), so the applier
// physically cannot undo its own enable. Calling it here would restore the trust
// flags and leave the bridge inspecting.
//
// THE ORDER IS THE APPLY'S, INVERTED, and for the same reason. The feature comes
// OFF FIRST; the trust flags go back after. Untrusting the uplink while the
// bridge is still inspecting would drop every DHCP reply crossing it until the
// next write lands — the very window planDHCPSnoop is written to avoid, opened
// on the way out instead of on the way in. Once the feature is off, every trust
// flag is inert (cf. foldDHCPSnoop's note) and the order stops mattering.
//
// Row identifiers come from a FRESH read, never from the snapshot: rest.go's Set
// is explicit that RouterOS renumbers its own records, and a stale `.id` would
// write to a different port — which here means untrusting something else.
func restoreSnoop(ctx context.Context, c *Client, want snoopFacts) error {
	var now, err = readSnoopFacts(ctx, c)
	if err != nil {
		return fmt.Errorf("re-read before restoring: %w", err)
	}
	if now.Snooping != want.Snooping {
		if now.BridgeID == "" {
			return fmt.Errorf("bridge %q has no .id on the re-read: cannot address the "+
				"dhcp-snooping field", now.BridgeName)
		}
		if _, err = c.Set(ctx, "interface/bridge", now.BridgeID,
			map[string]string{"dhcp-snooping": boolWord(want.Snooping)}); err != nil {
			return fmt.Errorf("set dhcp-snooping=%s on bridge %q: %w",
				boolWord(want.Snooping), now.BridgeName, err)
		}
	}
	var w snoopPortFact
	for _, w = range want.Ports {
		var cur, member = now.port(w.Iface)
		if !member {
			return fmt.Errorf("%s was a member of bridge %q at the start and is not one now: "+
				"its trust flag cannot be restored", w.Iface, want.BridgeName)
		}
		if cur.Trusted == w.Trusted {
			continue
		}
		if cur.RowID == "" {
			return fmt.Errorf("%s: bridge-port row has no .id on the re-read", w.Iface)
		}
		if _, err = c.Set(ctx, "interface/bridge/port", cur.RowID,
			map[string]string{"trusted": boolWord(w.Trusted)}); err != nil {
			return fmt.Errorf("set trusted=%s on %s: %w", boolWord(w.Trusted), w.Iface, err)
		}
	}
	return nil
}

// TestLiveDHCPSnoopingRoundTrip trusts the uplink, turns snooping on, confirms
// both from an INDEPENDENT session, proves the applier converges, and puts the
// device back.
func TestLiveDHCPSnoopingRoundTrip(t *testing.T) {
	if os.Getenv("ROS_LIVE_SNOOP_WRITE") != "1" {
		t.Skip("ROS_LIVE_SNOOP_WRITE not set — the DHCP-snooping WRITE exercise is skipped")
	}
	var c = liveClient(t) // skips unless ROS_LIVE_HOST is set
	var ctx, cancel = context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	// --- 0. THE REFUSALS -------------------------------------------------------
	//
	// Every one of these is t.Fatalf and none is t.Skip, deliberately. A skip
	// reads as "not applicable here" and gets ignored; each condition below means
	// THE DEVICE IN FRONT OF US IS NOT THE ONE THE PLAN WAS REASONED ABOUT, and
	// running a bridge-wide enable against an unreasoned device is precisely the
	// mistake the reconnaissance pass was written to prevent. A loud failure is
	// the correct outcome.

	// `/ip/dhcp-client` and `/ip/dhcp-server` must be READABLE and EMPTY.
	//
	// UNREADABLE COUNTS AS A REFUSAL, not as an observation. The probe next door
	// logs an absent menu and carries on, correctly — it only reports. Here the
	// two menus are the evidence that (a) this switch's own address is static, so
	// enabling snooping cannot cost it its management address at the next renewal,
	// and (b) DHCP genuinely arrives from upstream through the port about to be
	// trusted. A menu that cannot be read is evidence that was not gathered, and
	// the reconnaissance did read both.
	var menu string
	for _, menu = range []string{"ip/dhcp-client", "ip/dhcp-server"} {
		var rows, merr = c.List(ctx, menu)
		if merr != nil {
			t.Fatalf("/%s could not be read (%v) — REFUSING. The 2026-08-10 reconnaissance "+
				"read this menu and found it empty; a menu that does not answer today has "+
				"not been checked, and the plan below assumes it is empty.", menu, merr)
		}
		if len(rows) != 0 {
			t.Fatalf("/%s carries %d row(s) and the reconnaissance found it EMPTY — REFUSING. "+
				"%s", menu, len(rows), snoopWhyMenuMustBeEmpty(menu))
		}
	}

	// Exactly ONE cabled port, and it must be the uplink. `running` is carrier.
	var eth, eerr = c.List(ctx, "interface/ethernet")
	if eerr != nil {
		t.Fatalf("list interface/ethernet: %v — REFUSING: without carrier the blast radius "+
			"of the enable cannot be computed", eerr)
	}
	var cabled []string
	var r Row
	for _, r = range eth {
		if r.Bool("running") {
			cabled = append(cabled, r.Get("name"))
		}
	}
	sort.Strings(cabled)
	if len(cabled) != 1 || cabled[0] != snoopUplink {
		t.Fatalf("cabled port(s) = %v, and the plan is safe only for exactly [%s] — REFUSING. "+
			"Every cabled port that is not trusted stops seeing DHCP OFFER/ACK the instant "+
			"the bridge feature lands, and this platform persists immediately. Re-run "+
			"TestLiveProbeSnooping and reason about the new topology before writing.",
			cabled, snoopUplink)
	}
	t.Logf("cabled port(s): %v — the plan's uplink and nothing else", cabled)

	var before, berr = readSnoopFacts(ctx, c)
	if berr != nil {
		t.Fatalf("snapshot: %v", berr)
	}
	if before.BridgeID == "" {
		t.Fatalf("bridge %q has no .id — REFUSING: nothing can be addressed, and nothing "+
			"could be restored either", before.BridgeName)
	}
	if before.Snooping {
		t.Fatalf("bridge %q ALREADY reads dhcp-snooping=true — REFUSING. That is protection "+
			"this exercise did not install, and the teardown below would tear it down; the "+
			"reconnaissance found the feature OFF.", before.BridgeName)
	}
	var alreadyTrusted = before.trustedNames()
	if len(alreadyTrusted) != 0 {
		t.Fatalf("port(s) %v already read trusted=true and the reconnaissance found ALL 26 "+
			"bridge ports untrusted — REFUSING: somebody configured this since, and what "+
			"else they changed is unknown", alreadyTrusted)
	}
	var up, member = before.port(snoopUplink)
	if !member {
		t.Fatalf("%s is not a member of bridge %q — REFUSING: on RouterOS the trust flag "+
			"lives on the bridge PORT object, so there is nowhere to put it and the enable "+
			"would leave the uplink untrusted on an inspecting bridge",
			snoopUplink, before.BridgeName)
	}
	if up.RowID == "" {
		t.Fatalf("%s: bridge-port row has no .id — REFUSING, a `set` without an identifier "+
			"would apply to another port", snoopUplink)
	}
	t.Logf("snapshot: bridge %q dhcp-snooping=%s, %d bridge port(s), %d trusted",
		before.BridgeName, boolWord(before.Snooping), len(before.Ports), len(alreadyTrusted))

	// --- The teardown, ARMED BEFORE THE FIRST WRITE ---------------------------
	//
	// Before, and not after the apply: a run that dies between the enable and a
	// teardown registered later leaves a bridge inspecting with — depending on
	// where it died — possibly no trusted port at all. There is no reboot that
	// undoes that.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var rerr = restoreSnoop(rctx, c, before)
		if rerr != nil {
			t.Errorf("TEARDOWN FAILED: %v\n"+
				"  This platform persists immediately: what is on the device is on it in "+
				"flash, and no reboot will undo it. Bridge %q may still be INSPECTING.\n"+
				"  Put it back by hand, in this order:\n    %s",
				rerr, before.BridgeName, strings.Join(snoopManualFix(before), "\n    "))
			return
		}
		// Witnessed by a session that did not perform the restoration.
		var w = liveClient(t)
		var after, aerr = readSnoopFacts(rctx, w)
		if aerr != nil {
			t.Errorf("teardown sent, but the confirming re-read FAILED: %v\n"+
				"  Check by hand that this holds:\n    %s",
				aerr, strings.Join(snoopManualFix(before), "\n    "))
			return
		}
		var drift = diffSnoopFacts(before, after)
		if len(drift) > 0 {
			t.Errorf("THE DEVICE DID NOT COME BACK:\n  %s\n"+
				"  Fix by hand:\n    %s",
				strings.Join(drift, "\n  "), strings.Join(snoopManualFix(before), "\n    "))
			return
		}
		t.Logf("teardown: bridge %q dhcp-snooping=%s again, and all %d port trust flag(s) "+
			"compare EQUAL to the opening snapshot",
			before.BridgeName, boolWord(before.Snooping), len(before.Ports))
	}()

	// --- 1. THE GESTURE -------------------------------------------------------
	//
	// One trusted port — the real uplink — and the bridge feature. VLANs is left
	// nil: RouterOS has no per-VLAN enable (dhcpsnoop.go), and planDHCPSnoop says
	// so in a Note rather than pretending the level exists.
	var plan = driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: snoopUplink, Trusted: true}},
	}
	var res, err = c.ApplyDHCPSnooping(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyDHCPSnooping: %v", err)
	}
	t.Logf("apply: PortsWritten=%v Skipped=%v", res.PortsWritten, res.Skipped)
	var note string
	for _, note = range res.Notes {
		t.Logf("  note: %s", note)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != snoopUplink {
		t.Errorf("PortsWritten=%v, want [%s]", res.PortsWritten, snoopUplink)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped=%v — the plan named one port and it is a bridge member", res.Skipped)
	}

	// --- 2. AN INDEPENDENT READ IS THE PROOF, NOT THE APPLIER'S RETURN --------
	//
	// ApplyDHCPSnooping re-reads and verifies itself (verifySnoopWrites), which is
	// why PortsWritten can be trusted at all — but a harness that accepts the
	// applier's own account of its work cannot catch the applier agreeing with
	// itself about a write neither half sent. So this reads the raw menus back
	// through a SESSION THAT DID NOT WRITE.
	var w1 = liveClient(t)
	var mid, merr = readSnoopFacts(ctx, w1)
	if merr != nil {
		t.Fatalf("independent re-read: %v", merr)
	}
	if !mid.Snooping {
		t.Errorf("the apply reported success and bridge %q still reads dhcp-snooping=false — "+
			"nothing is being inspected and the trust flag is inert", mid.BridgeName)
	}
	var upMid, stillMember = mid.port(snoopUplink)
	if !stillMember {
		t.Errorf("%s absent from /interface/bridge/port on the re-read", snoopUplink)
	} else if !upMid.Trusted {
		t.Errorf("%s reads trusted=false after an apply that claimed to write it — the "+
			"uplink is untrusted on an inspecting bridge, which is a DHCP outage",
			snoopUplink)
	}

	// EXACTLY TWO THINGS MOVED, and the twenty-four dark access ports are not
	// among them. That the applier left them untrusted is not a detail of this
	// run: it IS the protection the gesture buys, and an apply that quietly
	// trusted more than it was asked would read as a success everywhere else.
	var moved = diffSnoopFacts(before, mid)
	sort.Strings(moved)
	t.Logf("moved: %v", moved)
	if len(moved) != 2 {
		t.Errorf("expected exactly 2 changes (the bridge flag and %s's trust), got %d: %v",
			snoopUplink, len(moved), moved)
	}
	var line string
	for _, line = range moved {
		if strings.HasPrefix(line, "[bridge] ") || strings.HasPrefix(line, snoopUplink+" ") {
			continue
		}
		t.Errorf("something OTHER than the bridge flag and %s moved: %s", snoopUplink, line)
	}
	var stillDark = len(mid.trustedNames())
	if stillDark != 1 {
		t.Errorf("%d port(s) read trusted after the apply, want exactly 1 (%s): %v — every "+
			"extra trusted port is a port snooping stops protecting",
			stillDark, snoopUplink, mid.trustedNames())
	}

	// --- 3. CONVERGENCE. The second identical apply must write NOTHING --------
	//
	// The step that has caught real defects on other ranges, and the one this
	// platform can least afford to fail: a `set` that changes nothing is still a
	// write to flash on a device with no rollback. PortsWritten going empty is
	// necessary but not sufficient — the menus are compared too, because a write
	// that landed and was then reported as "not written" would pass the first
	// check and fail this one.
	var again driver.ApplyResult
	again, err = c.ApplyDHCPSnooping(ctx, plan)
	if err != nil {
		t.Fatalf("second ApplyDHCPSnooping: %v", err)
	}
	t.Logf("second apply: PortsWritten=%v Skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) != 0 {
		t.Errorf("second apply wrote %v — a trust flag already correct was written again, "+
			"and on this platform that is a risk taken for nothing", again.PortsWritten)
	}
	var w2 = liveClient(t)
	var post, perr = readSnoopFacts(ctx, w2)
	if perr != nil {
		t.Fatalf("re-read after the second apply: %v", perr)
	}
	var settled = diffSnoopFacts(mid, post)
	if len(settled) > 0 {
		t.Errorf("the second apply MOVED something:\n  %s", strings.Join(settled, "\n  "))
	}

	// The teardown runs from the defer above. Its assertion — the device back to
	// `before`, field for field on everything this exercise can write — is the
	// last leg, and it fails loudly rather than quietly.
}

// snoopWhyMenuMustBeEmpty gives the refusal its reason, per menu. Two different
// dangers wear the same shape here, and telling an operator "this menu is not
// empty" without saying which one they are looking at is telling them nothing.
func snoopWhyMenuMustBeEmpty(menu string) string {
	if menu == "ip/dhcp-client" {
		return "THIS DEVICE WOULD BE A DHCP CLIENT: its own management address would be a " +
			"lease crossing the bridge about to start inspecting, and it would lose that " +
			"address at the next renewal — on a platform with no rollback, reached over " +
			"SOCKS. The reconnaissance found this menu empty, i.e. a STATIC address, and " +
			"that is the single fact that makes the enable recoverable."
	}
	return "THE DHCP SERVER WOULD LIVE HERE rather than upstream, so trusting the uplink " +
		"would be trusting the wrong direction entirely: the port to trust might be none " +
		"at all, and the plan below was reasoned for leases ARRIVING through " + snoopUplink + "."
}

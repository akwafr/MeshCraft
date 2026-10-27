package ios

// LIVE, READ ONLY — the MST region read, and the no-op decision that rides on it.
//
// WHY IT IS WORTH A HARNESS OF ITS OWN. Until 2026-08-11 this connector read the
// region with "show current", a command IOS has only INSIDE the
// `spanning-tree mst configuration` sub-mode. From privileged EXEC the Catalyst
// answers "% Invalid input detected at '^' marker", `ReadSpanningTree` swallowed
// the error by design (the region read is best-effort) and `Region` came back
// empty on every Cisco device meshcraft has ever talked to — silently, for a
// field whose entire job is to expose a silent 802.1s divergence.
//
// A fixture cannot prove that fix. The fixture is the bytes I already believe;
// the thing in doubt is whether the device answers the command at all. So this
// harness asks the device, through [Client.ReadSpanningTree] itself, and prints
// what came back.
//
// IT WRITES NOTHING. The no-op decision is exercised by calling
// [spanningTreeMatchesPlan] on the state just read — the pure half of
// [Client.ApplySpanningTree]'s new read-compare — which reports what an apply
// WOULD do without an apply happening.
//
//	STP_LIVE_HOST=192.0.2.2 STP_LIVE_USER=… STP_LIVE_PASS=… \
//	STP_LIVE_SOCKS=127.0.0.1:1080 STP_LIVE_SOCKS_USER=… STP_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/cisco/ios -run TestLiveReadMSTRegion -v

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

func liveSTPClient(ctx context.Context, t *testing.T, why string) *Client {
	t.Helper()
	var tgt = driver.Target{Host: os.Getenv("STP_LIVE_HOST")}
	var sa = os.Getenv("STP_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("STP_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("STP_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("STP_LIVE_USER"),
		Secret:   os.Getenv("STP_LIVE_PASS"),
		Why:      why,
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	return c
}

func TestLiveReadMSTRegion(t *testing.T) {
	if os.Getenv("STP_LIVE_HOST") == "" {
		t.Skip("STP_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var c = liveSTPClient(ctx, t, "MST region read (read only)")
	defer c.Close()

	var _, perr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if perr != nil {
		t.Fatalf("terminal length 0: %v", perr)
	}

	var st, raw, err = c.readSpanningTree(ctx)
	if err != nil {
		t.Fatalf("ReadSpanningTree: %v", err)
	}
	t.Logf("mode=%q enabled=%v bridge=%q headline priority=%d",
		st.Mode, st.Enabled, st.BridgeID, st.BridgePriority)
	var n string
	for _, n = range st.Notes {
		t.Logf("note: %s", n)
	}

	if st.Mode != "mstp" {
		t.Logf("this device is not in mst: the region read does not apply here")
		return
	}

	// THE MEASUREMENT. An empty region here is the OLD behaviour and means the
	// command still does not answer.
	if !st.Region.Configured() {
		t.Fatalf("REGION STILL EMPTY on an mst device — %q did not answer", mstRegionCmd)
	}
	t.Logf("MEASURED region: name=%q revision=%d instances=%d",
		st.Region.Name, st.Region.Revision, len(st.Region.InstanceVLANs))
	var inst int
	for _, inst = range sortedInstances(st.Region.InstanceVLANs) {
		var v = st.Region.InstanceVLANs[inst]
		t.Logf("  instance %d holds %d VLAN(s): %v", inst, len(v), v)
	}

	var prio, seen = iosConfiguredPriority(raw)
	t.Logf("configured bridge priority (from the parenthetical): %d (legible=%v)", prio, seen)

	// THE NO-OP DECISION, reported and not taken. A plan built from what the
	// device already says must no-op; the same plan with its revision moved must
	// NOT — which is the pair that shows the compare is looking at the region and
	// not merely at the mode.
	var plan = driver.SpanningTreePlan{
		Enabled: true, Mode: "mstp", Region: st.Region, BridgePriority: &prio,
	}
	var same, why = spanningTreeMatchesPlan(st, raw, plan, "mst")
	t.Logf("apply of the device's OWN state would no-op: %v — %s", same, why)
	if !same {
		t.Errorf("a plan built FROM this device did not compare equal to it: "+
			"the read-compare would rewrite a converged switch (region %+v)", st.Region)
	}

	plan.Region.Revision++
	var moved, _ = spanningTreeMatchesPlan(st, raw, plan, "mst")
	t.Logf("apply with the region revision moved would no-op: %v (must be false)", moved)
	if moved {
		t.Error("the no-op path ignored a region revision change — it is not comparing the region")
	}
}

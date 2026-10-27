package routeros

// A LIVE exercise of [Client.ApplyStormControl] on a real RouterOS switch.
//
// # THIS PLATFORM PERSISTS IMMEDIATELY TOO
//
// Like its SwOS sibling, RouterOS has no running/startup split: a `set` is the
// configuration, and no reboot forgets it. Unlike SwOS, though, the write is a
// per-record `set` on `/interface/ethernet/switch/port` rather than a whole-page
// POST — so the failure mode that dominates swos/live_stormcontrol_test.go, a
// field zeroed because it was omitted from the body, does not exist here. What
// remains is the ordinary one: leave the port as it was found.
//
// # WHAT THE RATE MEANS, because 100 is not "no opinion"
//
// `storm-rate` is a PERCENTAGE and 100 is the default meaning NO LIMITING
// (settled 2026-08-07 from the device's own UI; stormcontrol.go's header carries
// the capture). A plan asking for 100 % therefore writes the value that DISABLES
// the limiter. The revert below leans on that deliberately, and says so where it
// does, because it is the kind of coincidence that reads as a bug later.
//
//	ROS_LIVE_HOST=192.0.2.54 ROS_LIVE_USER=… ROS_LIVE_PASS=… \
//	ROS_LIVE_WRITE=1 ROS_LIVE_STORM_PORT=ether2 \
//	ROS_LIVE_SOCKS=127.0.0.1:1080 ROS_LIVE_SOCKS_USER=… ROS_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/mikrotik/routeros -run TestLive -v

import (
	"context"
	"net"
	"os"
	"sort"
	"strconv"
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

// liveClient opens a session. Each call is independent, so a second one can
// witness what the first wrote without any cache between them — the check that
// mattered on the SwOS sibling, where a page cache was one of the candidate
// explanations for two readings that could not both be true.
func liveClient(t *testing.T) *Client {
	t.Helper()
	var host = os.Getenv("ROS_LIVE_HOST")
	if host == "" {
		t.Skip("ROS_LIVE_HOST not set")
	}
	var d driver.Dialer
	var sa = os.Getenv("ROS_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ROS_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ROS_LIVE_SOCKS_PASS")}
		}
		var pd, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		d = liveSocksDialer{d: pd}
	}
	return Open(host, d, os.Getenv("ROS_LIVE_USER"), os.Getenv("ROS_LIVE_PASS"))
}

// stormSnapshot reduces the whole switch-port table to port → sorted field list,
// so a round trip can be compared across EVERY port and EVERY field rather than
// only the ones the plan named. The SwOS exercise is the reason this is
// whole-table: the assertion that catches a write touching something it should
// not have is the one that looks past the target.
func stormSnapshot(t *testing.T, c *Client, ctx context.Context) map[string]string {
	t.Helper()
	var rows, err = c.List(ctx, stormMenu)
	if err != nil {
		t.Fatalf("list /%s: %v", stormMenu, err)
	}
	var out = map[string]string{}
	var r map[string]string
	for _, r = range rows {
		var name = r["name"]
		if name == "" {
			continue
		}
		var keys []string
		var k string
		for k = range r {
			if volatileStormField(k) {
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts = make([]string, 0, len(keys))
		for _, k = range keys {
			parts = append(parts, k+"="+r[k])
		}
		out[name] = strings.Join(parts, " ")
	}
	return out
}

// volatileStormField says whether a field moves on its own, and so cannot appear
// in a before/after comparison without producing drift that is not drift.
//
// A DENYLIST RATHER THAN AN ALLOWLIST, deliberately. The point of comparing the
// whole table is to catch a write touching something nobody named; an allowlist
// of "the configuration fields we know about" would stop watching precisely the
// field a future firmware adds. So everything is compared except what is
// demonstrably not configuration:
//
//   - `.id` — RouterOS renumbers these on every reboot;
//   - the traffic counters. On this lab CRS326 only sfp-sfpplus2 has a link, and
//     its byte counters moved between two reads seconds apart, which is what
//     first reported the uplink as having "changed" during a write to ether2;
//   - `running` — carrier, which a cable or a neighbour can flap mid-exercise.
func volatileStormField(k string) bool {
	if k == ".id" || k == "running" {
		return true
	}
	return strings.HasPrefix(k, "driver-rx-") || strings.HasPrefix(k, "driver-tx-") ||
		strings.HasPrefix(k, "rx-") || strings.HasPrefix(k, "tx-")
}

// stormFieldsOnly keeps just the fields this exercise is about, for a log line
// that fits on a screen.
func stormFieldsOnly(rec string) string {
	var keep []string
	var f string
	for _, f = range strings.Fields(rec) {
		if strings.HasPrefix(f, "limit-") || strings.HasPrefix(f, "storm-rate") ||
			strings.HasPrefix(f, "name=") {
			keep = append(keep, f)
		}
	}
	return strings.Join(keep, " ")
}

func diffStormSnapshots(before, after map[string]string) []string {
	var keys []string
	var k string
	for k = range before {
		keys = append(keys, k)
	}
	for k = range after {
		var _, seen = before[k]
		if !seen {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []string
	for _, k = range keys {
		var b, hadB = before[k]
		var a, hadA = after[k]
		switch {
		case hadB && !hadA:
			out = append(out, k+": VANISHED")
		case !hadB && hadA:
			out = append(out, k+": APPEARED")
		case b != a:
			out = append(out, k+":\n    before "+b+"\n    after  "+a)
		}
	}
	return out
}

// restoreLimits reads a snapshot record back into the plan that reproduces it.
//
// Only the two classes this connector writes are returned: `limit-unknown-
// multicasts` is refused by the applier (its coverage turns on IGMP snooping, so
// the limiter's meaning would depend on a setting nothing on screen mentions), so
// asking for it here would only collect a refusal. The exercise never touches it,
// and the whole-table diff is what proves that claim rather than this comment.
func restoreLimits(rec string) []driver.StormLimit {
	var f = map[string]string{}
	var part string
	for _, part = range strings.Fields(rec) {
		var k, v, ok = strings.Cut(part, "=")
		if ok {
			f[k] = v
		}
	}
	var rate float64 = stormRateNoLimit
	var n, err = strconv.ParseFloat(f["storm-rate"], 64)
	if err == nil {
		rate = n
	}
	var mk = func(class driver.StormClass, field string) driver.StormLimit {
		var l = driver.StormLimit{Class: class, Enabled: f[field] == "true"}
		if l.Enabled {
			l.Rate, l.Unit = rate, driver.StormRatePercent
		}
		return l
	}
	return []driver.StormLimit{
		mk(driver.StormBroadcast, "limit-broadcasts"),
		mk(driver.StormUnknownUnicast, "limit-unknown-unicasts"),
	}
}

// TestLiveRestoreStormPort puts one port back to a state given verbatim, for
// cleaning up after an exercise that ended badly. The first run of the round trip
// left ether2 at `limit-broadcasts=false` where the switch had it `true` at 100.
//
//	ROS_LIVE_RESTORE_PORT=ether2 ROS_LIVE_RESTORE_REC='limit-broadcasts=true storm-rate=100'
func TestLiveRestoreStormPort(t *testing.T) {
	var port = os.Getenv("ROS_LIVE_RESTORE_PORT")
	if port == "" {
		t.Skip("ROS_LIVE_RESTORE_PORT not set")
	}
	var rec = os.Getenv("ROS_LIVE_RESTORE_REC")
	if rec == "" {
		t.Skip("ROS_LIVE_RESTORE_REC not set — refusing to guess the state to restore")
	}
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var before = stormSnapshot(t, c, ctx)
	t.Logf("before, %s: %s", port, stormFieldsOnly(before[port]))
	var res, err = c.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{Port: port, Limits: restoreLimits(rec)}},
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Logf("restore: PortsWritten=%v Skipped=%v", res.PortsWritten, res.Skipped)

	var w = liveClient(t)
	var after = stormSnapshot(t, w, ctx)
	t.Logf("after,  %s: %s", port, stormFieldsOnly(after[port]))
}

// TestLiveReadRouterOSStorm is READ-ONLY and gated on the host alone, so it can
// be run before any write to choose a target port.
func TestLiveReadRouterOSStorm(t *testing.T) {
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var st, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl: %v", err)
	}
	t.Logf("storm: %d port(s); notes=%v", len(st.Ports), st.Notes)
	var p driver.StormControlPortState
	for _, p = range st.Ports {
		var l driver.StormLimit
		for _, l = range p.Limits {
			if l.Enabled {
				t.Logf("  LIMITED %s %s at %v %s", p.Port, l.Class, l.Rate, l.Unit)
			}
		}
	}

	// Which ports are dark. `running` is the field RouterOS uses for carrier.
	var rows, lerr = c.List(ctx, "interface/ethernet")
	if lerr != nil {
		t.Fatalf("list interface/ethernet: %v", lerr)
	}
	var up, down []string
	var r map[string]string
	for _, r = range rows {
		if r["running"] == "true" {
			up = append(up, r["name"])
			continue
		}
		down = append(down, r["name"])
	}
	sort.Strings(up)
	sort.Strings(down)
	t.Logf("UP  (%d): %v", len(up), up)
	t.Logf("DOWN(%d): %v", len(down), down)

	var snap = stormSnapshot(t, c, ctx)
	var names []string
	var k string
	for k = range snap {
		names = append(names, k)
	}
	sort.Strings(names)
	t.Logf("/%s carries %d record(s): %v", stormMenu, len(snap), names)
	var probe string
	for _, probe = range []string{"ether1", "ether2", "sfp-sfpplus2"} {
		if snap[probe] != "" {
			t.Logf("  %s: %s", probe, stormFieldsOnly(snap[probe]))
		}
	}
}

// TestLiveStormControlRoundTrip writes one limiter, confirms it from an
// INDEPENDENT session, proves the applier converges, and puts the port back.
func TestLiveStormControlRoundTrip(t *testing.T) {
	if os.Getenv("ROS_LIVE_WRITE") != "1" {
		t.Skip("ROS_LIVE_WRITE not set — write exercise skipped")
	}
	var port = os.Getenv("ROS_LIVE_STORM_PORT")
	if port == "" {
		t.Skip("ROS_LIVE_STORM_PORT not set — refusing to pick a port on a live switch")
	}
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var before = stormSnapshot(t, c, ctx)
	var _, servesPort = before[port]
	if !servesPort {
		t.Fatalf("%s serves no /%s record — ports are named as RouterOS names them",
			port, stormMenu)
	}
	t.Logf("before, %s: %s", port, before[port])

	// Refuse to run against a TARGET that is already policing, so the revert
	// restores this exercise's own change and not somebody else's configuration.
	//
	// THE GUARD IS ABOUT THE RATE, NOT THE BOOLEAN, and that distinction is the
	// first thing this device taught. `limit-broadcasts=true` with
	// `storm-rate=100` is the FACTORY DEFAULT on every port of this CRS326: the
	// class is armed at a threshold equal to line rate, which polices nothing. A
	// guard written as "refuse if any class reads Enabled" would therefore refuse
	// on a pristine switch — and, worse, would have been read as evidence that
	// somebody had configured something.
	//
	// A rate BELOW 100 is a real limiter. ether1 carries one at the time of
	// writing (all three classes, storm-rate=1) — the operator's own 2026-08-07
	// experiment from the device's UI, never reverted — which is exactly the kind
	// of state this must not write over.
	var st0, serr = c.ReadStormControl(ctx)
	if serr != nil {
		t.Fatalf("ReadStormControl before: %v", serr)
	}
	var p driver.StormControlPortState
	for _, p = range st0.Ports {
		if p.Port != port {
			continue
		}
		var l driver.StormLimit
		for _, l = range p.Limits {
			if l.Enabled && l.Rate < stormRateNoLimit {
				t.Fatalf("%s already limits %s at %v %s — refusing, this platform persists "+
					"immediately and no reboot undoes it", p.Port, l.Class, l.Rate, l.Unit)
			}
		}
	}

	defer func() {
		var rctx = context.WithoutCancel(ctx)
		// RESTORE WHAT WAS OBSERVED, do not assume "disabled" is it.
		//
		// The obvious revert — disable the classes — leaves the port in a state the
		// switch was NOT in. This CRS326's factory state for a broadcast class is
		// `limit-broadcasts=true` beside `storm-rate=100`: armed at line rate,
		// policing nothing. Disabling writes `limit-broadcasts=false`, which
		// polices nothing either but is a different record, and the whole-table
		// diff rightly called it drift on the first run of this test.
		//
		// So the revert replays the ORIGINAL booleans, at the original rate. Same
		// lesson as the SwOS sibling, arriving from the other side: there the rate
		// survived a disable, here the boolean does not, and in both cases
		// "restore the limiter" is not the same instruction as "undo the write".
		var _, rerr = c.ApplyStormControl(rctx, driver.StormControlPlan{
			Ports: []driver.StormControlPortIntent{{
				Port:   port,
				Limits: restoreLimits(before[port]),
			}},
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s may still be policing and no reboot undoes it",
				rerr, port)
			return
		}
		var w = liveClient(t)
		var after = stormSnapshot(t, w, rctx)
		var drift = diffStormSnapshots(before, after)
		if len(drift) > 0 {
			t.Errorf("THE TABLE DID NOT COME BACK:\n  %s", strings.Join(drift, "\n  "))
			return
		}
		t.Logf("revert: /%s compares EQUAL to the opening snapshot across all %d record(s)",
			stormMenu, len(before))
	}()

	var plan = driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{{
				Class: driver.StormBroadcast, Enabled: true,
				Rate: 10, Unit: driver.StormRatePercent,
			}},
		}},
	}
	var res, err = c.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyStormControl: %v", err)
	}
	t.Logf("apply: PortsWritten=%v Skipped=%v", res.PortsWritten, res.Skipped)
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten=%v, want [%s]", res.PortsWritten, port)
	}

	// Witnessed by a session that did not perform the write.
	var w1 = liveClient(t)
	var mid = stormSnapshot(t, w1, ctx)
	t.Logf("after write, %s: %s", port, mid[port])
	var moved = diffStormSnapshots(before, mid)
	if len(moved) == 0 {
		t.Error("the apply reported a write and the whole table is unchanged")
	}
	var line string
	for _, line = range moved {
		if !strings.HasPrefix(line, port+":") {
			t.Errorf("a record OTHER than %s moved: %s", port, line)
		}
	}

	var st1 driver.StormControlState
	st1, err = w1.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl after: %v", err)
	}
	var seen bool
	for _, p = range st1.Ports {
		if p.Port != port {
			continue
		}
		seen = true
		var l, ok = p.Limit(driver.StormBroadcast)
		if !ok || !l.Enabled {
			t.Errorf("%s broadcast reads %+v (ok=%v), want an enabled limiter", port, l, ok)
		}
		if ok && l.Rate != 10 {
			t.Errorf("%s rate = %v, want 10 (percent — settled 2026-08-07)", port, l.Rate)
		}
	}
	if !seen {
		t.Errorf("%s absent from the re-read", port)
	}

	// Convergence: a second identical apply must write nothing.
	var again driver.ApplyResult
	again, err = c.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("second ApplyStormControl: %v", err)
	}
	t.Logf("second apply: PortsWritten=%v Skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) != 0 {
		t.Errorf("second apply wrote %v — on a platform that persists immediately a `set` "+
			"that changes nothing is a risk taken for nothing", again.PortsWritten)
	}
}

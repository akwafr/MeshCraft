package swos

// A LIVE exercise of [Admin.ApplyStormControl] on a real SwOS switch.
//
// # THIS IS THE ONE PLATFORM WITH NO UNDO
//
// SwOS applies and persists in one gesture. There is no startup configuration
// to leave alone, no reload that forgets, and no rollback except the
// configuration copy taken before the write ([driver.ApplyResult.Backup]). Every
// other range exercised in this fleet had a power cycle as a last resort; this
// one does not.
//
// Two consequences shape this test, and neither is optional.
//
// FIRST, /fwd.b IS POSTED WHOLE. A field omitted from the body is a field the
// device ZEROES — on this page that means losing port isolation, every port's
// VLAN mode, and the default VLAN, while setting one storm bit. The applier
// already refuses when the served field set does not match FwdWritableFields,
// which is the guard that makes this survivable. This test proves the guard held
// by comparing EVERY FIELD THE CONNECTOR POSTS before and after, rather than only
// the two it meant to touch.
//
// That set, and not the whole served page: fields the connector never sends are
// the device's own — uptime, fan RPM, PSU voltages — and they move on their own
// between two reads. Comparing them would make the assertion fail always rather
// than never. See restrictTo, which learned this from /sys.b.
//
// SECOND, the revert has to restore rather than undo. There is nothing to
// discard, so "back as found" means the page compares equal to the snapshot
// taken at the start — which is what the final assertion checks.
//
//	SWOS_LIVE_HOST=192.0.2.38 SWOS_LIVE_USER=… SWOS_LIVE_PASS=… \
//	SWOS_LIVE_WRITE=1 \
//	SWOS_LIVE_SOCKS=127.0.0.1:1080 SWOS_LIVE_SOCKS_USER=… SWOS_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/mikrotik/swos -run TestLiveStormControl -v

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

// renderValue prints a value WHATEVER its kind.
//
// [Value.Raw] is populated for KindStr alone: a number lives in Num and an array
// in List. The first version of this file snapshotted `v.Raw` and nothing else,
// which made the whole-page comparison compare empty strings to empty strings
// for every numeric field on /fwd.b — including "srt" and "suni", the two the
// write actually moves. The assertion passed because it was blind, not because
// the page came back. Anything that reduces a page to strings has to go through
// here.
func renderValue(v Value) string {
	switch v.Kind {
	case KindNum:
		return "num:" + strconv.FormatUint(uint64(v.Num), 10)
	case KindStr:
		return "str:" + v.Raw
	case KindList:
		var parts = make([]string, 0, len(v.List))
		var e Value
		for _, e = range v.List {
			parts = append(parts, renderValue(e))
		}
		return "list[" + strings.Join(parts, ",") + "]"
	case KindObj:
		var inner = pageSnapshot(v.Obj)
		var keys = make([]string, 0, len(inner))
		var k string
		for k = range inner {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts = make([]string, 0, len(keys))
		for _, k = range keys {
			parts = append(parts, k+"="+inner[k])
		}
		return "obj{" + strings.Join(parts, ",") + "}"
	}
	return "?:" + v.Raw
}

// pageSnapshot flattens a page to key → rendered value, so two reads can be
// compared without caring how any one field is typed.
func pageSnapshot(p *Page) map[string]string {
	var out = map[string]string{}
	if p == nil {
		return out
	}
	var k string
	for _, k = range p.Keys() {
		var v, ok = p.Value(k)
		if !ok {
			continue
		}
		out[k] = renderValue(v)
	}
	return out
}

// restrictTo keeps only the fields the connector actually POSTS.
//
// THIS IS THE SET THE ASSERTION BELONGS ON, and the reason is the write model
// rather than convenience. A SwOS page is posted whole from
// [WritableFields]/[FwdWritableFields], and the danger is a field in that list
// being dropped from the body — the device then zeroes it. Fields OUTSIDE the
// list are never sent at all: they are the device's own (uptime, fan RPM, PSU
// voltages, board temperatures, model and firmware strings), so comparing them
// says nothing about the write.
//
// Comparing them does actively harm, though, which is how this was found: the
// first `dtrp` exercise on the lab CSS326 reported /sys.b as "not come back"
// because `upt` — uptime — had advanced by eighty-five during the run. A
// whole-page comparison on this page can only ever fail, and a test that always
// fails is one nobody reads.
func restrictTo(snap map[string]string, fields []string) map[string]string {
	var out = map[string]string{}
	var f string
	for _, f = range fields {
		var v, ok = snap[f]
		if ok {
			out[f] = v
		}
	}
	return out
}

// diffSnapshots reports every key whose raw value moved, plus keys that appeared
// or vanished. Sorted, so a failure message reads the same twice.
func diffSnapshots(before, after map[string]string) []string {
	var seen = map[string]bool{}
	var keys []string
	var k string
	for k = range before {
		seen[k] = true
		keys = append(keys, k)
	}
	for k = range after {
		if !seen[k] {
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
			out = append(out, k+": VANISHED (was "+b+")")
		case !hadB && hadA:
			out = append(out, k+": APPEARED (now "+a+")")
		case b != a:
			out = append(out, k+": "+b+" → "+a)
		}
	}
	return out
}

func TestLiveStormControlWholePageRoundTrip(t *testing.T) {
	if os.Getenv("SWOS_LIVE_WRITE") != "1" {
		t.Skip("SWOS_LIVE_WRITE not set — write exercise skipped")
	}
	var host = os.Getenv("SWOS_LIVE_HOST")
	if host == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("SWOS_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("SWOS_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("SWOS_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("SWOS_LIVE_USER"), Secret: os.Getenv("SWOS_LIVE_PASS"),
		Why: "live storm-control exercise",
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var a, _ = adm.(*Admin)
	defer a.Close()

	var port = os.Getenv("SWOS_LIVE_STORM_PORT")
	if port == "" {
		port = "GE2"
	}

	// --- 0) THE SNAPSHOT. Everything after this is measured against it.
	var fwdBefore, gerr = a.Get(ctx, PageFwd)
	if gerr != nil {
		t.Fatalf("read %s before: %v", PageFwd, gerr)
	}
	var fwdFields = FwdWritableFields(a.facadeFor(ctx).Ports())
	var before = restrictTo(pageSnapshot(fwdBefore), fwdFields)
	t.Logf("before: %s posts %d fields", PageFwd, len(before))
	if len(before) == 0 {
		t.Fatalf("%s came back empty — refusing to write against a page that did not read",
			PageFwd)
	}

	// Refuse to touch a device already policing something: the revert below
	// restores THIS snapshot, and it must not be somebody else's configuration
	// that gets restored over.
	var stBefore driver.StormControlState
	stBefore, err = a.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl before: %v", err)
	}
	var p driver.StormControlPortState
	for _, p = range stBefore.Ports {
		var l driver.StormLimit
		for _, l = range p.Limits {
			if l.Enabled {
				t.Fatalf("%s already limits %s at %v — refusing to run on a device that "+
					"is already policing, because this exercise cannot be undone by a reboot",
					p.Port, l.Class, l.Rate)
			}
		}
	}

	// The rate this port carried BEFORE the exercise. Needed by the revert:
	// clearing the enable bit does not put "srt" back, because the bit does not
	// own it (cf. stormRateNoLimit). Restoring the bit is not restoring the page.
	var idxBefore, idxOK = a.facadeFor(ctx).Index(port)
	if !idxOK {
		t.Fatalf("%s is not a port on this faceplate", port)
	}
	var ratesBefore, ratesReadable = fwdBefore.Nums(FieldStormRate)
	if !ratesReadable || idxBefore >= len(ratesBefore) {
		t.Fatalf("%s does not read as a per-port array covering index %d",
			FieldStormRate, idxBefore)
	}
	var rateBefore = float64(ratesBefore[idxBefore])
	t.Logf("%s (index %d) carried %s = %v before the exercise",
		port, idxBefore, FieldStormRate, rateBefore)

	// --- The revert, armed before the first write.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		// Two applies, in this order. Enabling at the ORIGINAL rate is the only
		// path that reaches "srt"; disabling then clears the bit and leaves that
		// value standing, which is where the page started.
		var _, rerr = a.ApplyStormControl(rctx, driver.StormControlPlan{
			Ports: []driver.StormControlPortIntent{{
				Port: port,
				Limits: []driver.StormLimit{{
					Class: driver.StormUnknownUnicast, Enabled: true,
					Rate: rateBefore, Unit: driver.StormRatePercent,
				}},
			}},
		})
		if rerr != nil {
			t.Errorf("REVERT step 1 (restore rate %v) FAILED: %v", rateBefore, rerr)
			return
		}
		_, rerr = a.ApplyStormControl(rctx, driver.StormControlPlan{
			Ports: []driver.StormControlPortIntent{{
				Port:   port,
				Limits: []driver.StormLimit{{Class: driver.StormUnknownUnicast, Enabled: false}},
			}},
		})
		if rerr != nil {
			t.Errorf("REVERT step 2 FAILED: %v — %s may still be policing, and this platform "+
				"has no reboot that undoes it. Restore from the backup in the apply result.",
				rerr, port)
			return
		}
		// THE ASSERTION THIS WHOLE TEST EXISTS FOR: the page must be back to what
		// it was, in EVERY field, not only the two the plan named.
		var fwdAfter, aerr = a.Get(rctx, PageFwd)
		if aerr != nil {
			t.Errorf("revert sent but %s could not be re-read: %v", PageFwd, aerr)
			return
		}
		var drift = diffSnapshots(before, restrictTo(pageSnapshot(fwdAfter), fwdFields))
		if len(drift) > 0 {
			t.Errorf("THE PAGE DID NOT COME BACK: %v — on a platform that persists "+
				"immediately this is permanent; restore from the backup", drift)
			return
		}
		t.Logf("revert: %s compares EQUAL to the opening snapshot across all %d fields",
			PageFwd, len(before))
	}()

	// --- 1) The one class this platform expresses as itself.
	//
	// Broadcast and multicast are refused here on purpose — the fields are
	// `limit-broadcasts` (whole-class, but sharing one rate) and
	// `limit-unknown-multicasts` (narrower than the contract's multicast). Only
	// unknown-unicast maps cleanly, so that is what an exercise can prove.
	var plan = driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{{
				Class: driver.StormUnknownUnicast, Enabled: true,
				Rate: 10, Unit: driver.StormRatePercent,
			}},
		}},
	}
	var res driver.ApplyResult
	res, err = a.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyStormControl: %v — transcript %+v", err, a.Transcript())
	}
	t.Logf("apply: written=%v skipped=%v", res.PortsWritten, res.Skipped)
	if len(res.Backup) == 0 {
		t.Error("no backup was taken before writing to a platform with no other way back")
	} else {
		t.Logf("backup: %d bytes captured before the write", len(res.Backup))
	}

	// --- 2) ONLY the two storm fields may have moved.
	var fwdMid, merr = a.Get(ctx, PageFwd)
	if merr != nil {
		t.Fatalf("read %s after apply: %v", PageFwd, merr)
	}
	var moved = diffSnapshots(before, pageSnapshot(fwdMid))
	t.Logf("after apply, fields that moved: %v", moved)
	var d string
	for _, d = range moved {
		if !hasPrefixAny(d, FieldStormRate+":", FieldLimitUnknownUnicast+":") {
			t.Errorf("a field outside the storm pair moved: %q — /fwd.b is posted WHOLE, "+
				"so this is the shape that silently drops port isolation or a VLAN mode", d)
		}
	}
	if len(moved) == 0 {
		t.Error("the apply reported a write and nothing on the page changed")
	}

	// --- 3) The contract read must agree.
	var after driver.StormControlState
	after, err = a.ReadStormControl(ctx)
	if err != nil {
		t.Fatalf("ReadStormControl after: %v", err)
	}
	// Matched by faceplate INDEX, not by name: the plan may be written in
	// meshcraft's numbered family ("GE2") while the read always answers in the
	// device's own ("Port2"). Comparing the strings is what made the applier
	// itself disown this very write before 2026-08-08.
	var seen bool
	for _, p = range after.Ports {
		var pi, piOK = a.facadeFor(ctx).Index(p.Port)
		if !piOK || pi != idxBefore {
			continue
		}
		seen = true
		var l, ok = p.Limit(driver.StormUnknownUnicast)
		if !ok || !l.Enabled {
			t.Errorf("%s unknown-unicast reads %+v (ok=%v), want an enabled limiter", port, l, ok)
		}
		if ok && l.Rate != 10 {
			t.Errorf("%s rate = %v, want 10 (percent — settled 2026-08-07 on the "+
				"RouterOS sibling and shared by this page)", port, l.Rate)
		}
	}
	if !seen {
		t.Errorf("%s does not appear in the read: %+v", port, after.Ports)
	}

	// --- 4) IDEMPOTENCE. On a platform that persists immediately, a second write
	// that changes nothing is not merely wasteful — it is a risk taken for
	// nothing, which is why the applier short-circuits before posting.
	var again driver.ApplyResult
	again, err = a.ApplyStormControl(ctx, plan)
	if err != nil {
		t.Fatalf("ApplyStormControl (second pass): %v", err)
	}
	t.Logf("second pass: written=%v skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) > 0 {
		t.Errorf("the same plan wrote %v a second time — on SwOS every avoidable POST "+
			"is an avoidable risk", again.PortsWritten)
	}

	var s Step
	for _, s = range a.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

func hasPrefixAny(s string, prefixes ...string) bool {
	var p string
	for _, p = range prefixes {
		if len(s) >= len(p) && s[:len(p)] == p {
			return true
		}
	}
	return false
}

// TestLiveReadSwOSState is READ-ONLY and gated on the host alone, so it can be
// run before any write to choose a target port and to see what the page carries.
//
// This range had no live test of any kind before 2026-08-08, which is part of
// why its write was the last in the fleet to be exercised: there was no cheap
// way to look first.
func TestLiveReadSwOSState(t *testing.T) {
	var host = os.Getenv("SWOS_LIVE_HOST")
	if host == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("SWOS_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("SWOS_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("SWOS_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("SWOS_LIVE_USER"), Secret: os.Getenv("SWOS_LIVE_PASS"),
		Why: "live read",
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var a, _ = adm.(*Admin)
	defer a.Close()

	var st State
	st, err = a.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	t.Logf("state: %d VLANs, %d ports, %d links", len(st.VLANs), len(st.Ports), len(st.Links))
	var n string
	for n = range st.Links {
		t.Logf("  link %s = %s", n, st.Links[n])
	}

	var storm driver.StormControlState
	storm, err = a.ReadStormControl(ctx)
	if err != nil {
		t.Errorf("ReadStormControl: %v", err)
	} else {
		t.Logf("storm: %d port(s), notes=%v", len(storm.Ports), storm.Notes)
		var p driver.StormControlPortState
		for _, p = range storm.Ports {
			var l driver.StormLimit
			for _, l = range p.Limits {
				if l.Enabled {
					t.Logf("  LIMITED %s %s at %v %s", p.Port, l.Class, l.Rate, l.Unit)
				}
			}
		}
	}

	var fwd, gerr = a.Get(ctx, PageFwd)
	if gerr != nil {
		t.Fatalf("read %s: %v", PageFwd, gerr)
	}
	t.Logf("%s fields: %v", PageFwd, fwd.Keys())
}

// TestLiveFaceplateNaming is READ-ONLY. It answers the question the first live
// write raised: WHICH spellings does the faceplate accept, and which one does
// ReadStormControl emit? A mismatch between the two is invisible in unit tests —
// both halves use whatever name the fixture chose — but on a device it makes the
// applier disown a write it really did perform.
func TestLiveFaceplateNaming(t *testing.T) {
	var host = os.Getenv("SWOS_LIVE_HOST")
	if host == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("SWOS_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("SWOS_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("SWOS_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("SWOS_LIVE_USER"), Secret: os.Getenv("SWOS_LIVE_PASS"),
		Why: "live faceplate naming probe",
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var a, _ = adm.(*Admin)
	defer a.Close()

	var facade = a.facadeFor(ctx)
	t.Logf("faceplate: %d ports, Names()=%v", facade.Ports(), facade.Names())

	var probe string
	for _, probe = range []string{"GE2", "ge2", "Port2", "port2", "2", "SFP2", "Ether2"} {
		var idx, ok = facade.Index(probe)
		t.Logf("  Index(%-7q) = %d, ok=%v", probe, idx, ok)
	}

	var st, serr = a.ReadStormControl(ctx)
	if serr != nil {
		t.Fatalf("ReadStormControl: %v", serr)
	}
	var names []string
	var p driver.StormControlPortState
	for _, p = range st.Ports {
		names = append(names, p.Port)
	}
	t.Logf("ReadStormControl emits: %v", names)

	// The actual assertion: every name the reader emits must round-trip through
	// the faceplate, and the faceplate's own list must be what the reader uses.
	// If these two lists disagree, the verify step after a write cannot match.
	var fp = facade.Names()
	if len(fp) != len(names) {
		t.Errorf("faceplate has %d ports, ReadStormControl emits %d", len(fp), len(names))
	}
	var i int
	for i = range names {
		if i < len(fp) && fp[i] != names[i] {
			t.Errorf("index %d: faceplate says %q, storm read says %q", i, fp[i], names[i])
		}
	}

	var raw *Page
	raw, err = a.Get(ctx, PageFwd)
	if err != nil {
		t.Fatalf("get %s: %v", PageFwd, err)
	}
	var v, _ = raw.Value(FieldLimitUnknownUnicast)
	var r, _ = raw.Value(FieldStormRate)
	t.Logf("%s = %s", FieldLimitUnknownUnicast, renderValue(v))
	t.Logf("%s = %s", FieldStormRate, renderValue(r))
}

// liveAdmin opens a SECOND, independent session. The first live write produced
// two readings that could not both be true — the applier's own re-read saw the
// limiter, and a page fetched moments later did not — and one candidate
// explanation was a cached page. A witness that shares no state with the writer
// settles that: whatever it reads came from the device on this call.
func liveAdmin(ctx context.Context, t *testing.T, why string) *Admin {
	t.Helper()
	var tgt = driver.Target{Host: os.Getenv("SWOS_LIVE_HOST")}
	var sa = os.Getenv("SWOS_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("SWOS_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("SWOS_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("SWOS_LIVE_USER"), Secret: os.Getenv("SWOS_LIVE_PASS"), Why: why,
	}})
	if err != nil {
		t.Fatalf("authenticate (%s): %v", why, err)
	}
	var a, _ = adm.(*Admin)
	return a
}

// witnessStorm reports what a FRESH session sees: the raw fields plus how many
// ports read as limited. Raw, because the first exercise showed "suni" served
// as an empty string when nothing is policing — a shape no fixture had.
func witnessStorm(ctx context.Context, t *testing.T, label string) (rawSuni, rawSrt string, limited int) {
	t.Helper()
	var w = liveAdmin(ctx, t, "witness: "+label)
	defer w.Close()
	var pg, err = w.Get(ctx, PageFwd)
	if err != nil {
		t.Fatalf("witness %s: get %s: %v", label, PageFwd, err)
	}
	var suv, _ = pg.Value(FieldLimitUnknownUnicast)
	var srv, _ = pg.Value(FieldStormRate)
	rawSuni, rawSrt = renderValue(suv), renderValue(srv)
	var st, serr = w.ReadStormControl(ctx)
	if serr != nil {
		t.Fatalf("witness %s: ReadStormControl: %v", label, serr)
	}
	var p driver.StormControlPortState
	for _, p = range st.Ports {
		var l driver.StormLimit
		for _, l = range p.Limits {
			if l.Enabled {
				limited++
				t.Logf("  [%s] %s limited: %s at %v %s", label, p.Port, l.Class, l.Rate, l.Unit)
			}
		}
	}
	t.Logf("  [%s] suni=%s srt=%s — %d limited", label, rawSuni, rawSrt, limited)
	return rawSuni, rawSrt, limited
}

// TestLiveStormControlCanonicalName repeats the exercise using the name the
// device itself emits ("Port2"), and watches every phase through an
// independent session. This is the run that says whether ApplyStormControl works
// and only its verify step is wrong, or whether the write never landed at all.
func TestLiveStormControlCanonicalName(t *testing.T) {
	if os.Getenv("SWOS_LIVE_WRITE") != "1" {
		t.Skip("SWOS_LIVE_WRITE not set")
	}
	if os.Getenv("SWOS_LIVE_HOST") == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var port = "Port2"
	var a = liveAdmin(ctx, t, "canonical-name storm exercise")
	defer a.Close()

	var fwd0, gerr = a.Get(ctx, PageFwd)
	if gerr != nil {
		t.Fatalf("read %s: %v", PageFwd, gerr)
	}
	var canonFields = FwdWritableFields(a.facadeFor(ctx).Ports())
	var before = restrictTo(pageSnapshot(fwd0), canonFields)

	t.Log("phase 0 — before any write")
	var _, _, lim0 = witnessStorm(ctx, t, "before")
	if lim0 != 0 {
		t.Fatalf("device is already policing %d port(s) — refusing", lim0)
	}

	// The rate the port carried before, for the revert. Clearing the bit does not
	// put "srt" back — it is a standing value the bit does not own — so the two
	// applies below are enable-at-the-original-rate, then disable. This test
	// originally reverted with the disable alone and PASSED, because the page
	// comparison it ended with was blind to numeric fields; the version of it that
	// can see them fails on exactly that.
	var canonIdx, canonIdxOK = a.facadeFor(ctx).Index(port)
	if !canonIdxOK {
		t.Fatalf("%s is not a port on this faceplate", port)
	}
	var rates0, rates0OK = fwd0.Nums(FieldStormRate)
	if !rates0OK || canonIdx >= len(rates0) {
		t.Fatalf("%s does not read as a per-port array covering index %d",
			FieldStormRate, canonIdx)
	}
	var rate0 = float64(rates0[canonIdx])
	t.Logf("%s (index %d) carried %s = %v before the exercise",
		port, canonIdx, FieldStormRate, rate0)

	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var r = liveAdmin(rctx, t, "revert")
		defer r.Close()
		var _, rerr = r.ApplyStormControl(rctx, driver.StormControlPlan{
			Ports: []driver.StormControlPortIntent{{
				Port: port,
				Limits: []driver.StormLimit{{
					Class: driver.StormUnknownUnicast, Enabled: true,
					Rate: rate0, Unit: driver.StormRatePercent,
				}},
			}},
		})
		if rerr != nil {
			t.Errorf("REVERT step 1 (restore rate %v) FAILED: %v", rate0, rerr)
			return
		}
		_, rerr = r.ApplyStormControl(rctx, driver.StormControlPlan{
			Ports: []driver.StormControlPortIntent{{
				Port:   port,
				Limits: []driver.StormLimit{{Class: driver.StormUnknownUnicast, Enabled: false}},
			}},
		})
		if rerr != nil {
			t.Errorf("REVERT step 2 FAILED: %v — restore from the backup", rerr)
		}
		t.Log("phase 3 — after revert")
		var _, _, lim3 = witnessStorm(rctx, t, "after-revert")
		if lim3 != 0 {
			t.Errorf("REVERT DID NOT CLEAR: %d port(s) still policing, and this platform "+
				"has no reboot that undoes it", lim3)
		}
		var w = liveAdmin(rctx, t, "final page compare")
		defer w.Close()
		var fwd3, ferr = w.Get(rctx, PageFwd)
		if ferr != nil {
			t.Errorf("final read: %v", ferr)
			return
		}
		var drift = diffSnapshots(before, restrictTo(pageSnapshot(fwd3), canonFields))
		if len(drift) > 0 {
			t.Errorf("THE PAGE DID NOT COME BACK: %v", drift)
			return
		}
		t.Logf("revert: %s compares EQUAL to the opening snapshot across all %d fields",
			PageFwd, len(before))
	}()

	var res, err = a.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{{
				Class: driver.StormUnknownUnicast, Enabled: true,
				Rate: 10, Unit: driver.StormRatePercent,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("ApplyStormControl: %v", err)
	}
	t.Logf("apply: PortsWritten=%v Skipped=%v Notes=%v", res.PortsWritten, res.Skipped, res.Notes)

	t.Log("phase 1 — after the write, seen by a session that did not perform it")
	var suni1, srt1, lim1 = witnessStorm(ctx, t, "after-write")
	if lim1 != 1 {
		t.Errorf("expected exactly 1 limited port after the write, an independent session "+
			"sees %d (raw suni=%q srt=%q)", lim1, suni1, srt1)
	}
	if len(res.PortsWritten) != 1 || (len(res.PortsWritten) == 1 && res.PortsWritten[0] != port) {
		t.Errorf("the device took the write but the applier reports PortsWritten=%v — "+
			"a write it performed and then disowned", res.PortsWritten)
	}

	t.Log("phase 2 — second identical apply, which must converge")
	var res2, err2 = a.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{{
				Class: driver.StormUnknownUnicast, Enabled: true,
				Rate: 10, Unit: driver.StormRatePercent,
			}},
		}},
	})
	if err2 != nil {
		t.Fatalf("second ApplyStormControl: %v", err2)
	}
	t.Logf("second apply: PortsWritten=%v Skipped=%v", res2.PortsWritten, res2.Skipped)
	var _, _, lim2 = witnessStorm(ctx, t, "after-second-apply")
	if lim2 != 1 {
		t.Errorf("second apply changed the count to %d — not idempotent", lim2)
	}
}

// TestLiveRestoreStormRate puts "srt" back to the platform's no-limit value on
// one port, using only operations the exercise already proved.
//
// WHY THIS EXISTS. Disabling a limiter clears the `suni` bit and leaves `srt`
// where the last apply put it: ApplyStormControl assigns rates[idx] only on the
// enabled branch. That is invisible while the bit is off — SwOS ignores the rate
// of an unpoliced port — but it means the page does NOT come back to what it was,
// and the first exercise on this device left Port2 reading 10 while all 25 other
// ports read 100.
//
// The restore is two proven applies rather than a hand-built POST: enable at 100,
// which is the write path that sets a rate, then disable, which clears the bit and
// leaves that 100 in place.
//
//	SWOS_LIVE_RESTORE_PORT=Port2 go test -run TestLiveRestoreStormRate ...
func TestLiveRestoreStormRate(t *testing.T) {
	var port = os.Getenv("SWOS_LIVE_RESTORE_PORT")
	if port == "" {
		t.Skip("SWOS_LIVE_RESTORE_PORT not set")
	}
	if os.Getenv("SWOS_LIVE_HOST") == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var a = liveAdmin(ctx, t, "restore storm rate")
	defer a.Close()

	var facade = a.facadeFor(ctx)
	var idx, ok = facade.Index(port)
	if !ok {
		t.Fatalf("%s is not a port on this faceplate (%v)", port, facade.Names())
	}

	var pg, gerr = a.Get(ctx, PageFwd)
	if gerr != nil {
		t.Fatalf("read %s: %v", PageFwd, gerr)
	}
	var rates, rok = pg.Nums(FieldStormRate)
	if !rok || idx >= len(rates) {
		t.Fatalf("%s does not read as a per-port array of at least %d", FieldStormRate, idx+1)
	}
	t.Logf("before: %s[%d] = %d (other ports: %v)", FieldStormRate, idx, rates[idx], rates)
	if rates[idx] == stormRateNoLimit {
		t.Logf("already at %d — nothing to restore", stormRateNoLimit)
		return
	}

	var _, err = a.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port: port,
			Limits: []driver.StormLimit{{
				Class: driver.StormUnknownUnicast, Enabled: true,
				Rate: stormRateNoLimit, Unit: driver.StormRatePercent,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("enable at %d%%: %v", stormRateNoLimit, err)
	}
	_, err = a.ApplyStormControl(ctx, driver.StormControlPlan{
		Ports: []driver.StormControlPortIntent{{
			Port:   port,
			Limits: []driver.StormLimit{{Class: driver.StormUnknownUnicast, Enabled: false}},
		}},
	})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}

	var _, srt, limited = witnessStorm(ctx, t, "after-restore")
	if limited != 0 {
		t.Errorf("%d port(s) still policing after the restore", limited)
	}
	var w = liveAdmin(ctx, t, "restore check")
	defer w.Close()
	var pg2, g2 = w.Get(ctx, PageFwd)
	if g2 != nil {
		t.Fatalf("re-read: %v", g2)
	}
	var after, aok = pg2.Nums(FieldStormRate)
	if !aok || idx >= len(after) {
		t.Fatalf("%s unreadable after the restore", FieldStormRate)
	}
	if after[idx] != stormRateNoLimit {
		t.Errorf("%s[%d] = %d, wanted %d — %s", FieldStormRate, idx, after[idx],
			stormRateNoLimit, srt)
	}
	t.Logf("after: %s[%d] = %d", FieldStormRate, idx, after[idx])
}

// TestLiveDHCPSnoopTrustRoundTrip exercises [Admin.ApplyDHCPSnooping] — the
// `dtrp` trusted-port mask of /sys.b — on a real switch.
//
// # WHY THIS PAGE IS THE DANGEROUS ONE
//
// /fwd.b, which the storm exercise writes, can cost port isolation and VLAN
// modes. /sys.b can cost ACCESS: it carries `ip`, `avln` ("Allow From VLAN"),
// `allp` ("Allow From Ports") and `mgmt` alongside `dtrp`, it is posted WHOLE
// like every SwOS page, and this model has neither SSH, nor Telnet, nor a serial
// port. A field dropped from the body is a field the device zeroes, and the way
// back from a zeroed allow-list is the reset button, on site, which also loses
// the configuration.
//
// So the assertion that matters here is not "the trust bit moved". It is that
// EVERY OTHER FIELD ON /sys.b DID NOT — which is what the whole-page diff below
// checks, and why it is worth running even though the applier already refuses a
// body whose field set does not match WritableFields.
//
// The POST machinery itself is not new: management addressing already writes
// this page on hardware. What has never been sent is `dtrp`.
//
//	SWOS_LIVE_SNOOP_PORT=Port2 SWOS_LIVE_WRITE=1 …
func TestLiveDHCPSnoopTrustRoundTrip(t *testing.T) {
	if os.Getenv("SWOS_LIVE_WRITE") != "1" {
		t.Skip("SWOS_LIVE_WRITE not set")
	}
	var port = os.Getenv("SWOS_LIVE_SNOOP_PORT")
	if port == "" {
		t.Skip("SWOS_LIVE_SNOOP_PORT not set — refusing to pick a port on a live switch")
	}
	if os.Getenv("SWOS_LIVE_HOST") == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var a = liveAdmin(ctx, t, "dtrp trust exercise")
	defer a.Close()

	var facade = a.facadeFor(ctx)
	var idx, idxOK = facade.Index(port)
	if !idxOK {
		t.Fatalf("%s is not a port on this faceplate (%v)", port, facade.Names())
	}

	// THE SNAPSHOT of the whole page, taken before anything is sent.
	var sys0, gerr = a.Get(ctx, PageSys)
	if gerr != nil {
		t.Fatalf("read %s: %v", PageSys, gerr)
	}
	var sysFields, fieldsKnown = WritableFields(PageSys)
	if !fieldsKnown {
		t.Fatalf("no writable field set is declared for %s", PageSys)
	}
	var wholeBefore = pageSnapshot(sys0)
	var before = restrictTo(wholeBefore, sysFields)
	t.Logf("before: %s serves %d fields, %d of them posted back by this connector",
		PageSys, len(wholeBefore), len(before))
	if len(before) == 0 {
		t.Fatalf("%s came back empty — refusing to write against a page that did not read",
			PageSys)
	}
	// The four fields whose loss would end the session, named explicitly so a
	// failure says WHICH access field moved rather than burying it in a diff.
	var guard string
	for _, guard = range []string{"ip", "avln", "allp", "mgmt"} {
		if before[guard] == "" {
			t.Errorf("%s is not in the posted field set for %s — a write would ZERO it",
				guard, PageSys)
		}
		t.Logf("  guard %s = %s", guard, before[guard])
	}

	var wasTrusted, readOK = sys0.Bit(FieldDHCPTrusted, idx)
	if !readOK {
		t.Fatalf("%s bit %d unreadable — refusing to write a mask we cannot read",
			FieldDHCPTrusted, idx)
	}
	t.Logf("%s (index %d) is currently trusted=%v", port, idx, wasTrusted)

	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var _, rerr = a.ApplyDHCPSnooping(rctx, driver.DHCPSnoopPlan{
			Ports: []driver.DHCPSnoopPortIntent{{Port: port, Trusted: wasTrusted}},
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s trust may be wrong and no reboot undoes it",
				rerr, port)
			return
		}
		var w = liveAdmin(rctx, t, "final /sys.b compare")
		defer w.Close()
		var sys2, ferr = w.Get(rctx, PageSys)
		if ferr != nil {
			t.Errorf("final read: %v", ferr)
			return
		}
		var drift = diffSnapshots(before, restrictTo(pageSnapshot(sys2), sysFields))
		if len(drift) > 0 {
			t.Errorf("%s DID NOT COME BACK: %v — this page carries the access fields and "+
				"the platform persists immediately; restore from the backup", PageSys, drift)
			return
		}
		t.Logf("revert: %s compares EQUAL to the opening snapshot across all %d fields",
			PageSys, len(before))
	}()

	// Flip it, whichever way it was.
	var want = !wasTrusted
	var res, err = a.ApplyDHCPSnooping(ctx, driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: port, Trusted: want}},
	})
	if err != nil {
		t.Fatalf("ApplyDHCPSnooping: %v — transcript %+v", err, a.Transcript())
	}
	t.Logf("apply: PortsWritten=%v Skipped=%v Notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.Backup) == 0 {
		t.Error("no backup was taken before writing to the page that carries access")
	} else {
		t.Logf("backup: %d bytes captured before the write", len(res.Backup))
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten=%v, want [%s]", res.PortsWritten, port)
	}

	// Witnessed by a session that did not perform the write.
	var w1 = liveAdmin(ctx, t, "witness after write")
	defer w1.Close()
	var sys1, w1err = w1.Get(ctx, PageSys)
	if w1err != nil {
		t.Fatalf("witness read: %v", w1err)
	}
	var got, gotOK = sys1.Bit(FieldDHCPTrusted, idx)
	if !gotOK || got != want {
		t.Errorf("%s trusted reads %v (ok=%v), want %v", port, got, gotOK, want)
	}
	// EXACTLY ONE FIELD MAY HAVE MOVED, and it must be dtrp.
	var moved = diffSnapshots(before, restrictTo(pageSnapshot(sys1), sysFields))
	t.Logf("after write, posted fields that moved: %v", moved)
	var line string
	for _, line = range moved {
		if !strings.HasPrefix(line, FieldDHCPTrusted+":") {
			t.Errorf("a field OTHER than %s moved on the page that carries access: %s",
				FieldDHCPTrusted, line)
		}
	}
	if len(moved) == 0 {
		t.Error("the apply reported a write and no field on the page changed")
	}

	// Convergence.
	var again driver.ApplyResult
	again, err = a.ApplyDHCPSnooping(ctx, driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: port, Trusted: want}},
	})
	if err != nil {
		t.Fatalf("second ApplyDHCPSnooping: %v", err)
	}
	t.Logf("second apply: PortsWritten=%v Skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) != 0 {
		t.Errorf("second apply wrote %v — on a page that carries access, a POST that "+
			"changes nothing is a risk taken for nothing", again.PortsWritten)
	}
}

// TestLivePortAdminPlanOnTheRealDevice runs the port-admin GUARDRAIL against the
// pages this CSS326 actually serves, and writes NOTHING.
//
// # Why the plan is exercised before the gesture on this range in particular
//
// TODO.md calls this the highest-exposure declared path in the fleet and it is
// right: `port_admin` is declared here, SwOS persists immediately so NO REBOOT
// UNDOES A SHUT PORT, and this model has neither SSH, nor Telnet, nor a serial
// port. A shut uplink is recovered with the reset button, on site, which also
// loses the configuration.
//
// [PlanPortAdmin] is pure and takes the device's own /link.b, /sys.b, facade and
// state. So the question that actually matters — would this switch's uplink be
// spared? — is answerable from a read. Asking the applier instead would be using
// the outage as the instrument.
//
// Two cases, and the second is the one worth having:
//
//  1. every LINKED port must be spared without force, and the refusal must say
//     which of the guard's reasons applied;
//  2. a DARK port must be allowed through — a guardrail that refuses everything
//     protects nothing and would simply be discovered broken later, by an
//     operator whose shut did nothing.
func TestLivePortAdminPlanOnTheRealDevice(t *testing.T) {
	if os.Getenv("SWOS_LIVE_HOST") == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var a = liveAdmin(ctx, t, "port admin guardrail check (read only)")
	defer a.Close()

	var link, lerr = a.Get(ctx, PageLink)
	if lerr != nil {
		t.Fatalf("read %s: %v", PageLink, lerr)
	}
	var sys, serr = a.Get(ctx, PageSys)
	if serr != nil {
		t.Fatalf("read %s: %v", PageSys, serr)
	}
	var st, terr = a.ReadState(ctx)
	if terr != nil {
		t.Fatalf("ReadState: %v", terr)
	}
	var facade = a.facadeFor(ctx)

	// Which ports carry a link, from the device's own `lnk` mask.
	var linked, dark []string
	var i int
	for i = 0; i < facade.Ports(); i++ {
		var name = facade.Name(i)
		var up, ok = link.Bit(FieldLink, i)
		if !ok {
			t.Fatalf("%s bit %d unreadable — the guardrail would protect nothing",
				FieldLink, i)
		}
		if up {
			linked = append(linked, name)
			continue
		}
		dark = append(dark, name)
	}
	t.Logf("mgmt VLAN %d; linked %v; %d dark port(s)", st.MgmtVLAN, linked, len(dark))
	if len(linked) == 0 {
		t.Skip("no port carries a link — nothing for the guardrail to spare")
	}

	// --- 1. Every linked port must be refused.
	var intents []driver.PortAdminIntent
	var n string
	for _, n = range linked {
		intents = append(intents, driver.PortAdminIntent{Port: n, Enabled: false})
	}
	var dec, derr = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sys, Facade: facade, Current: st, Intents: intents,
	})
	if derr != nil {
		t.Fatalf("PlanPortAdmin: %v", derr)
	}
	if len(dec.Changes) != 0 {
		t.Errorf("THE GUARDRAIL WOULD SHUT %+v — these ports carry a link on the device "+
			"this session is talking to, and SwOS has no reboot that undoes it",
			dec.Changes)
	}
	if len(dec.Skipped) == 0 {
		t.Error("every linked port was refused and nothing said so — an operator would " +
			"be told the shut landed")
	}
	var s string
	for _, s = range dec.Skipped {
		t.Logf("  spared: %s", s)
	}
	for _, s = range dec.Notes {
		t.Logf("  note: %s", s)
	}

	// --- 2. A dark port must go through, or the guard protects nothing.
	var target = dark[len(dark)-1]
	dec, derr = PlanPortAdmin(PortAdminInput{
		Link: link, Sys: sys, Facade: facade, Current: st,
		Intents: []driver.PortAdminIntent{{Port: target, Enabled: false}},
	})
	if derr != nil {
		t.Fatalf("PlanPortAdmin (dark port): %v", derr)
	}
	if len(dec.Changes) != 1 {
		t.Errorf("a DARK port (%s) was not allowed through: changes=%+v skipped=%v — "+
			"a guardrail that refuses everything protects nothing",
			target, dec.Changes, dec.Skipped)
		return
	}
	t.Logf("dark port %s would be shut: %+v", target, dec.Changes[0])
}

// TestLivePortAdminShutAndUnshut is the gesture TODO.md calls the
// highest-exposure declared path in the fleet.
//
// `port_admin` is declared here, SwOS persists immediately so NO REBOOT UNDOES A
// SHUT PORT, and this model has no SSH, no Telnet and no serial port. The
// guardrail was proven on this device's own pages first, without writing
// ([TestLivePortAdminPlanOnTheRealDevice]) — that is the check that had to come
// before this one, not after.
//
// WHAT THIS ADDS beyond the plan: /link.b is posted WHOLE, and it carries `nm` —
// the twenty-six PORT NAMES, in one list. A body missing that field renames
// every jack on the switch to nothing while shutting one, permanently. The
// applier already refuses a served field set that does not match
// [WritableFields], and this compares every posted field before and after so the
// refusal is shown to have held rather than assumed.
//
//	SWOS_LIVE_WRITE=1 SWOS_LIVE_ADMIN_PORT=Port2 …
func TestLivePortAdminShutAndUnshut(t *testing.T) {
	if os.Getenv("SWOS_LIVE_WRITE") != "1" {
		t.Skip("SWOS_LIVE_WRITE not set")
	}
	var port = os.Getenv("SWOS_LIVE_ADMIN_PORT")
	if port == "" {
		t.Skip("SWOS_LIVE_ADMIN_PORT not set — refusing to pick a port to shut on a " +
			"platform where no reboot undoes it")
	}
	if os.Getenv("SWOS_LIVE_HOST") == "" {
		t.Skip("SWOS_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	var a = liveAdmin(ctx, t, "port admin shut/un-shut exercise")
	defer a.Close()

	var facade = a.facadeFor(ctx)
	var idx, idxOK = facade.Index(port)
	if !idxOK {
		t.Fatalf("%s is not on this faceplate (%v)", port, facade.Names())
	}
	var linkFields, fieldsKnown = WritableFields(PageLink)
	if !fieldsKnown {
		t.Fatalf("no writable field set declared for %s", PageLink)
	}

	var link0, lerr = a.Get(ctx, PageLink)
	if lerr != nil {
		t.Fatalf("read %s: %v", PageLink, lerr)
	}
	var before = restrictTo(pageSnapshot(link0), linkFields)
	t.Logf("before: %s posts %d field(s)", PageLink, len(before))

	// THE FIELD THAT MUST SURVIVE, asserted by name and not merely swept up.
	var namesBefore, namesOK = link0.Texts(FieldPortName)
	if !namesOK || len(namesBefore) == 0 {
		t.Fatalf("%s does not read as a list of port names — refusing to post a page "+
			"whose name field could not be read", FieldPortName)
	}
	t.Logf("%s carries %d port name(s)", FieldPortName, len(namesBefore))

	// The target must be dark, from the device's own mask.
	var up, upOK = link0.Bit(FieldLink, idx)
	if !upOK {
		t.Fatalf("%s bit %d unreadable", FieldLink, idx)
	}
	if up {
		t.Fatalf("%s CARRIES A LINK — refusing. No reboot undoes a shut port here.", port)
	}
	var wasEnabled, enOK = link0.Bit(FieldEnabled, idx)
	if !enOK {
		t.Fatalf("%s bit %d unreadable", FieldEnabled, idx)
	}
	t.Logf("%s (index %d) is dark and enabled=%v", port, idx, wasEnabled)

	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var _, rerr = a.ApplyPortAdmin(rctx, driver.PortAdminPlan{
			Ports: []driver.PortAdminIntent{{Port: port, Enabled: wasEnabled}},
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s may still be SHUT, and this platform has no "+
				"reboot that undoes it. Restore from the backup in the apply result.",
				rerr, port)
			return
		}
		var w = liveAdmin(rctx, t, "final /link.b compare")
		defer w.Close()
		var link2, ferr = w.Get(rctx, PageLink)
		if ferr != nil {
			t.Errorf("final read: %v", ferr)
			return
		}
		var drift = diffSnapshots(before, restrictTo(pageSnapshot(link2), linkFields))
		if len(drift) > 0 {
			t.Errorf("%s DID NOT COME BACK: %v", PageLink, drift)
			return
		}
		t.Logf("revert: %s compares EQUAL to the opening snapshot across all %d posted "+
			"field(s)", PageLink, len(before))
	}()

	// --- SHUT.
	var res, err = a.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin(shut): %v — transcript %+v", err, a.Transcript())
	}
	t.Logf("shut: PortsWritten=%v Skipped=%v", res.PortsWritten, res.Skipped)
	if len(res.Backup) == 0 {
		t.Error("no backup was taken before a write that no reboot undoes")
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten=%v, want [%s]", res.PortsWritten, port)
	}

	// --- THE PORT NAMES SURVIVED. This is the assertion this range needs.
	var w1 = liveAdmin(ctx, t, "witness after shut")
	defer w1.Close()
	var link1, w1err = w1.Get(ctx, PageLink)
	if w1err != nil {
		t.Fatalf("witness read: %v", w1err)
	}
	var namesAfter, naOK = link1.Texts(FieldPortName)
	if !naOK || len(namesAfter) != len(namesBefore) {
		t.Fatalf("%s went from %d name(s) to %d — the whole-page POST dropped the port "+
			"names", FieldPortName, len(namesBefore), len(namesAfter))
	}
	var i int
	for i = range namesBefore {
		if namesBefore[i] != namesAfter[i] {
			t.Errorf("port name %d changed: %q → %q", i, namesBefore[i], namesAfter[i])
		}
	}
	t.Logf("all %d port name(s) survived the write", len(namesAfter))

	// Exactly one posted field may have moved, and it must be the enable mask.
	var moved = diffSnapshots(before, restrictTo(pageSnapshot(link1), linkFields))
	t.Logf("posted fields that moved: %v", moved)
	var line string
	for _, line = range moved {
		if !strings.HasPrefix(line, FieldEnabled+":") {
			t.Errorf("a field OTHER than %s moved on the page that carries the port "+
				"names: %s", FieldEnabled, line)
		}
	}
	var nowEnabled, nowOK = link1.Bit(FieldEnabled, idx)
	if !nowOK || nowEnabled {
		t.Errorf("%s reads enabled=%v after being shut", port, nowEnabled)
	} else {
		t.Logf("CONFIRMED: %s reads administratively DOWN", port)
	}

	// --- CONVERGENCE.
	var again driver.ApplyResult
	again, err = a.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("second ApplyPortAdmin: %v", err)
	}
	t.Logf("second shut: PortsWritten=%v", again.PortsWritten)
	if len(again.PortsWritten) != 0 {
		t.Errorf("second apply rewrote %v — a POST that changes nothing is a risk taken "+
			"for nothing on a platform with no undo", again.PortsWritten)
	}
}

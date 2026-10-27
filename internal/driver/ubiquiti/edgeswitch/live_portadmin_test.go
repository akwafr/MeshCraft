package edgeswitch

// A LIVE exercise of [Client.ApplyPortAdmin] — shut and un-shut — on the lab
// EdgePoint S16, and the verification of a parser repair shipped the same day.
//
// # WHY THIS ONE MATTERS MORE THAN THE OTHER LIVE TESTS HERE
//
// `port_admin` is DECLARED on eight of the nine connectors, this one included,
// so the gesture is already reachable from a screen. It has been exercised on
// almost none of them. That is the widest gap in the fleet between "the operator
// can click it" and "we have watched it work".
//
// And on 2026-08-09 this range was found unable to REPORT the state it can set:
// `parseShowPortAll` decided admin state with `strings.Contains(row, "enable")`
// over the WHOLE row, and every row of `show port all` on this platform carries
// "Enable" in the LACP Mode and Link Trap columns. The test was therefore
// always true whatever the Admin Mode column said, and since [LinkStates] keys
// [driver.LinkDisabled] on that flag, an administratively shut port could only
// ever render as unplugged. The repair reads the Admin Mode column alone, with
// an `AdminSeen` companion so an unreadable column says nothing rather than
// inventing a shut port.
//
// That repair was made against the captured fixture. THIS is what proves it on
// a device: shut a dark port, and require the connector to come back and say
// "disabled" — the exact answer it was structurally incapable of giving
// before.
//
// # THE TARGET MUST BE DARK, and the guardrail is not a substitute
//
// [PlanPortAdmin] spares a port that carries the management VLAN or a live link,
// and that guardrail is real — but it is meshcraft's own reasoning about a
// device, not a property of the device. A test that leaned on it would be
// testing the guardrail with the uplink as the stake. The port is chosen dark
// from a read, and the guardrail is then checked SEPARATELY, on the uplink, by
// asking for something that must be refused.
//
//	ES_LIVE_HOST=192.0.2.4 ES_LIVE_USER=… ES_LIVE_PASS=… \
//	ES_LIVE_WRITE=1 ES_LIVE_ADMIN_PORT=GE12 \
//	ES_LIVE_SOCKS=127.0.0.1:1080 ES_LIVE_SOCKS_USER=… ES_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/ubiquiti/edgeswitch -run TestLivePortAdmin -v

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

func livePortAdminClient(ctx context.Context, t *testing.T, why string) *Client {
	t.Helper()
	var tgt = driver.Target{Host: os.Getenv("ES_LIVE_HOST")}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: os.Getenv("ES_LIVE_PASS"), Why: why,
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	return c
}

// TestLivePortAdminReadsTheFaceplate is READ ONLY. Run it first: it names the
// dark ports, so the write below is aimed from evidence rather than from a
// guess, and it prints what the repaired parser makes of the current state.
func TestLivePortAdminReadsTheFaceplate(t *testing.T) {
	if os.Getenv("ES_LIVE_HOST") == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var c = livePortAdminClient(ctx, t, "port admin reconnaissance (read only)")
	defer c.Close()

	var st, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	t.Logf("mgmt VLAN %d, %d port(s), %d link entries",
		st.MgmtVLAN, len(st.Ports), len(st.Links))
	var counts = map[driver.LinkState]int{}
	var name string
	for name = range st.Links {
		counts[st.Links[name]]++
		if st.Links[name] != driver.LinkUnplugged {
			t.Logf("  %s = %s", name, st.Links[name])
		}
	}
	t.Logf("link-state census: %v", counts)
	if counts[driver.LinkUpState] == 0 {
		t.Log("NOTE: no port reads UP — the uplink refusal check below has nothing to bite on")
	}
}

func TestLivePortAdminShutAndUnshut(t *testing.T) {
	if os.Getenv("ES_LIVE_WRITE") != "1" {
		t.Skip("ES_LIVE_WRITE not set")
	}
	var port = os.Getenv("ES_LIVE_ADMIN_PORT")
	if port == "" {
		t.Skip("ES_LIVE_ADMIN_PORT not set — refusing to pick a port to shut on a live switch")
	}
	if os.Getenv("ES_LIVE_HOST") == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	var c = livePortAdminClient(ctx, t, "port admin shut/un-shut exercise")
	defer c.Close()

	// --- 0. The target must be DARK, checked now rather than assumed.
	var before, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState before: %v", err)
	}
	var was, known = before.Links[port]
	if !known {
		t.Fatalf("%s is not on this faceplate (links: %v)", port, before.Links)
	}
	if was == driver.LinkUpState {
		t.Fatalf("%s carries a LIVE LINK (%s) — refusing to shut it. Pick a dark port.",
			port, was)
	}
	t.Logf("%s reads %s before the exercise; mgmt VLAN is %d", port, was, before.MgmtVLAN)

	// --- The revert, armed before the write.
	defer func() {
		var rctx = context.WithoutCancel(ctx)
		var _, rerr = c.ApplyPortAdmin(rctx, driver.PortAdminPlan{
			Ports: []driver.PortAdminIntent{{Port: port, Enabled: true}},
		})
		if rerr != nil {
			t.Errorf("REVERT FAILED: %v — %s may still be SHUT", rerr, port)
			return
		}
		var after, aerr = c.ReadState(rctx)
		if aerr != nil {
			t.Errorf("revert sent but the re-read failed: %v", aerr)
			return
		}
		if after.Links[port] == driver.LinkDisabled {
			t.Errorf("REVERT INCOMPLETE: %s still reads %s", port, after.Links[port])
			return
		}
		t.Logf("revert: %s reads %s, back as found (%s)", port, after.Links[port], was)
	}()

	// --- 1. SHUT IT.
	var res driver.ApplyResult
	res, err = c.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("ApplyPortAdmin(shut): %v", err)
	}
	t.Logf("shut: PortsWritten=%v Skipped=%v Notes=%v", res.PortsWritten, res.Skipped, res.Notes)
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten=%v, want [%s] — PortsWritten means CONFIRMED on this fleet",
			res.PortsWritten, port)
	}

	// --- 2. THE ASSERTION THIS FILE EXISTS FOR.
	//
	// Before the 2026-08-09 repair this could not pass: AdminUp was true on every
	// row, so LinkStates never produced LinkDisabled and a shut port reported as
	// unplugged. If this fails with "unplugged", the parser has regressed to
	// the whole-row substring test.
	var mid driver.State
	mid, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState after shut: %v", err)
	}
	if mid.Links[port] != driver.LinkDisabled {
		t.Errorf("%s reads %q after being shut, want %q. If this says %q, "+
			"parseShowPortAll is deciding admin state from the whole row again — "+
			"every row carries \"Enable\" in LACP Mode and Link Trap.",
			port, mid.Links[port], driver.LinkDisabled, driver.LinkUnplugged)
	} else {
		t.Logf("CONFIRMED: %s reads %q — the Admin Mode column is being read alone",
			port, mid.Links[port])
	}

	// --- 3. CONVERGENCE: shutting a shut port writes nothing.
	var again driver.ApplyResult
	again, err = c.ApplyPortAdmin(ctx, driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: port, Enabled: false}},
	})
	if err != nil {
		t.Fatalf("second ApplyPortAdmin(shut): %v", err)
	}
	t.Logf("second shut: PortsWritten=%v Skipped=%v", again.PortsWritten, again.Skipped)
	if len(again.PortsWritten) != 0 {
		t.Errorf("second apply rewrote %v — a port already in the wanted state was "+
			"written again", again.PortsWritten)
	}
}

// TestLivePortAdminGuardSparesTheUplink checks the guardrail on the port that
// would actually hurt — WITHOUT SENDING ANYTHING.
//
// # Why this does not call the applier, and why the first draft of it was wrong
//
// The obvious live test asks ApplyPortAdmin to shut every port that has a link
// and asserts a refusal. That test uses the uplink as its stake: if the
// guardrail has the bug the test is looking for, the test CAUSES the outage it
// was written to rule out. On this device the uplink is the only link, and
// ApplyPortAdmin deliberately does not persist — so recovery would be a power
// cycle, which is a drive to the lab.
//
// The guardrail is a PURE function of state the device just told us
// ([portAdminGuardBlocks]), so the real device's answer is obtainable without a
// write: read the state, feed it in, and assert it blocks. The evidence about
// THIS switch is identical; only the risk differs.
//
// What this cannot prove is that ApplyPortAdmin actually consults the guard.
// That is covered without hardware — the applier's own tests drive it through a
// fake CLI — and it is the half a device adds nothing to.
func TestLivePortAdminGuardSparesTheUplink(t *testing.T) {
	if os.Getenv("ES_LIVE_HOST") == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var c = livePortAdminClient(ctx, t, "port admin guardrail check (read only)")
	defer c.Close()

	var st, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	var linked []string
	var name string
	for name = range st.Links {
		if st.Links[name] == driver.LinkUpState {
			linked = append(linked, name)
		}
	}
	if len(linked) == 0 {
		t.Skip("no port reads UP on this device — nothing for the guardrail to spare")
	}
	t.Logf("mgmt VLAN %d; live port(s) %v", st.MgmtVLAN, linked)

	for _, name = range linked {
		var have = st.Ports[name]
		var block, reason = portAdminGuardBlocks(have, st.MgmtVLAN, true, false)
		if !block {
			t.Errorf("THE GUARDRAIL WOULD NOT SPARE %s, and it is carrying a live link on "+
				"the device this session is talking to. state=%+v mgmt=%d",
				name, have, st.MgmtVLAN)
			continue
		}
		t.Logf("guard spares %s: %s", name, reason)
		// And force must still be able to lift it — a guardrail with no override
		// is a guardrail an operator routes around outside meshcraft.
		var forced, _ = portAdminGuardBlocks(have, st.MgmtVLAN, true, true)
		if forced {
			t.Errorf("%s cannot be shut even with force — the override is not reachable", name)
		}
	}
}

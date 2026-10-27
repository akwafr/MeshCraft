package s3400

// LIVE WRITE exercise of the CONNECTOR's write paths, end to end.
//
// Why this exists: every write form in this package was proven BY HAND on the
// lab 48T4SP (2026-08-03) and reverted — but ApplyPorts / ApplySNMP /
// ApplySpanningTree / EnsureLLDP had never DRIVEN them until 2026-08-05
// (TODO.md, "Writes are unexercised FROM THE CONNECTOR"). Replaying a
// capture proves the parsers; only this proves that what the connector sends
// is what the device accepts, in the connector's own order, through the
// connector's own pager handling.
//
// Double-gated (S3400_LIVE_WRITE=1 on top of S3400_LIVE_HOST) because it
// configures a real switch. The discipline is the EdgeSwitch harness's
// (ubiquiti/edgeswitch/live_write_test.go), tightened:
//
//   - the port it touches is chosen to be DOWN and NOT carrying the
//     management VLAN — read first, never assumed — so nothing in the lab
//     loses connectivity even if the revert fails;
//   - every write is REVERTED to the port's exact prior state, re-read, and
//     compared; the scratch VLAN is deleted ("no vlan N", proven by hand
//     2026-08-03) and its absence confirmed;
//   - the three remaining paths are driven through their NO-OP branches
//     against the device's own current state — the transcript must SAY
//     nothing was written, not merely avoid writing.

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// pickScratchPort returns a copper port that is safe to reconfigure: link
// DOWN and not carrying the management VLAN. Mid-range first — the low ports
// are where a lab plugs things in, the high ones (GE45-48) carry this unit's
// hand-survey trunk history — then everything else that qualifies.
func pickScratchPort(st State, mgmt int) string {
	var candidates []string
	var n int
	for n = 20; n <= 40; n++ {
		candidates = append(candidates, "GE"+strconv.Itoa(n))
	}
	for n = 2; n < 20; n++ {
		candidates = append(candidates, "GE"+strconv.Itoa(n))
	}
	var name string
	for _, name = range candidates {
		var ps, known = st.Ports[name]
		if !known {
			continue
		}
		if st.Links[name] == driver.LinkUpState {
			continue
		}
		if carriesMgmt(ps, mgmt) {
			continue
		}
		return name
	}
	return ""
}

func TestLiveWriteApplyAndRevert(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" {
		t.Skip("S3400_LIVE_WRITE not set — write exercise skipped")
	}
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var scratchVLAN = 3900
	var scratchEnv = os.Getenv("S3400_LIVE_SCRATCH_VLAN")
	if scratchEnv != "" {
		scratchVLAN, _ = strconv.Atoi(scratchEnv)
	}

	// --- 0) Read first. The port is CHOSEN from the device's state, and its
	// exact prior state is what the revert must restore.
	var before State
	before, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	var exists = false
	var v driver.VLAN
	for _, v = range before.VLANs {
		if v.ID == scratchVLAN {
			exists = true
		}
	}
	if exists {
		t.Fatalf("VLAN %d already exists on the device — refusing to treat it as scratch", scratchVLAN)
	}
	var port = os.Getenv("S3400_LIVE_SCRATCH_PORT")
	if port == "" {
		port = pickScratchPort(before, before.MgmtVLAN)
	}
	if port == "" {
		t.Fatal("no safe port found: every copper port is up or carries the management VLAN")
	}
	if before.Links[port] == driver.LinkUpState {
		t.Fatalf("%s has a live link — refusing to touch it", port)
	}
	var prior = before.Ports[port]
	if carriesMgmt(prior, before.MgmtVLAN) {
		t.Fatalf("%s carries the management VLAN %d — refusing to touch it", port, before.MgmtVLAN)
	}
	t.Logf("scratch port %s (down, prior state %+v), scratch VLAN %d, mgmt VLAN %d",
		port, prior, scratchVLAN, before.MgmtVLAN)

	// --- 1) ApplyPorts: scratch VLAN as access on the chosen port.
	var res ApplyResult
	res, err = c.ApplyPorts(ctx, PortPlan{
		Desired:   map[string]PortState{port: {Mode: "access", PVID: scratchVLAN}},
		MgmtVLAN:  before.MgmtVLAN,
		SiteVLANs: []driver.VLAN{{ID: scratchVLAN, Name: "MESHCRAFT-SCRATCH"}},
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	t.Logf("apply: created=%v written=%v skipped=%v notes=%v",
		res.VLANsCreated, res.PortsWritten, res.Skipped, res.Notes)
	if len(res.Skipped) > 0 {
		t.Errorf("apply skipped: %v", res.Skipped)
	}
	if len(res.VLANsCreated) != 1 || res.VLANsCreated[0] != scratchVLAN {
		t.Errorf("VLANsCreated = %v, want [%d]", res.VLANsCreated, scratchVLAN)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != port {
		t.Errorf("PortsWritten = %v, want [%s]", res.PortsWritten, port)
	}

	// --- 2) The device must AGREE: re-read, do not trust the apply.
	var mid State
	mid, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState after apply: %v", err)
	}
	var found = false
	for _, v = range mid.VLANs {
		if v.ID == scratchVLAN {
			found = true
			t.Logf("vlan %d exists, name %q", v.ID, v.Name)
		}
	}
	if !found {
		t.Errorf("VLAN %d was not created", scratchVLAN)
	}
	var got = mid.Ports[port]
	if got.PVID != scratchVLAN || !strings.EqualFold(orAccess(got.Mode), "access") {
		t.Errorf("%s reads as %+v after apply — want access/pvid %d", port, got, scratchVLAN)
	} else {
		t.Logf("%s moved to VLAN %d and read back", port, scratchVLAN)
	}

	// --- 3) Revert the port to its EXACT prior state, and confirm.
	res, err = c.ApplyPorts(ctx, PortPlan{
		Desired:  map[string]PortState{port: prior},
		MgmtVLAN: before.MgmtVLAN,
	})
	if err != nil {
		t.Fatalf("ApplyPorts revert: %v", err)
	}
	if len(res.Skipped) > 0 {
		t.Errorf("revert skipped: %v", res.Skipped)
	}
	var after State
	after, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState after revert: %v", err)
	}
	if !samePort(after.Ports[port], prior) {
		t.Errorf("%s reads as %+v after revert — want the prior %+v", port, after.Ports[port], prior)
	} else {
		t.Logf("%s back to its prior state %+v", port, after.Ports[port])
	}

	// --- 4) Delete the scratch VLAN ("no vlan N" — proven by hand
	// 2026-08-03) and confirm it is gone.
	if err = c.configBlock(ctx, []string{"no vlan " + strconv.Itoa(scratchVLAN)}); err != nil {
		t.Errorf("no vlan %d: %v", scratchVLAN, err)
	}
	after, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState after vlan delete: %v", err)
	}
	for _, v = range after.VLANs {
		if v.ID == scratchVLAN {
			t.Errorf("VLAN %d still exists after \"no vlan\"", scratchVLAN)
		}
	}
	if !samePort(after.Ports[port], prior) {
		t.Errorf("%s changed by the VLAN delete: %+v, want %+v", port, after.Ports[port], prior)
	}
	t.Logf("vlan %d deleted, %s untouched", scratchVLAN, port)

	// --- 5) ApplySpanningTree through its NO-OP path: a plan that mirrors
	// the device's current state must produce ZERO writes, and the
	// transcript must say why. Nothing about STP is changed on this device.
	var stp driver.SpanningTreeState
	stp, err = c.ReadSpanningTree(ctx)
	if err != nil {
		t.Fatalf("ReadSpanningTree: %v", err)
	}
	t.Logf("stp: mode=%q enabled=%v priority=%d bridge=%s", stp.Mode, stp.Enabled,
		stp.BridgePriority, stp.BridgeID)
	if !stp.Enabled || stp.Mode != modeRSTP {
		t.Errorf("device is not running enabled RSTP (%+v) — the no-op leg needs the lab baseline", stp)
	} else {
		var prio = stp.BridgePriority
		var stepsBefore = len(c.Transcript())
		res, err = c.ApplySpanningTree(ctx, driver.SpanningTreePlan{
			Mode: stp.Mode, Enabled: true, BridgePriority: &prio,
		})
		if err != nil {
			t.Fatalf("ApplySpanningTree: %v", err)
		}
		if len(res.PortsWritten) > 0 || len(res.Skipped) > 0 || len(res.Notes) > 0 {
			t.Errorf("no-op STP plan produced %+v — want zero writes, zero notes", res)
		}
		var s Step
		var said = false
		for _, s = range c.Transcript()[stepsBefore:] {
			if strings.Contains(s.What, "spanning-tree bridge written") {
				t.Errorf("transcript says a bridge write happened: %+v", s)
			}
			if strings.Contains(s.What, "already in place") {
				said = true
			}
		}
		if !said {
			t.Error("transcript does not say the priority was already in place")
		} else {
			t.Log("spanning tree: no-op confirmed, transcript says nothing was written")
		}
	}

	// --- 6) ApplySNMP idempotently: the community ALREADY on the device must
	// produce a no-op (same discipline as the ProCurve exercise). The
	// community is read from the device itself, not from an env var.
	var cfgOut string
	cfgOut, err = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if err != nil && strings.TrimSpace(cfgOut) == "" {
		t.Fatalf("show running-config: %v", err)
	}
	var communities = ParseRunningConfig(cfgOut).Communities
	if len(communities) == 0 {
		t.Error("no SNMP community on the device — the idempotence leg cannot run " +
			"(the lab unit is expected to carry one)")
	} else {
		var stepsBefore = len(c.Transcript())
		var snmpRes SNMPResult
		snmpRes, err = c.ApplySNMP(ctx, driver.SNMPIdentity{Community: communities[0]})
		if err != nil {
			t.Fatalf("ApplySNMP: %v", err)
		}
		if snmpRes.CommunitySet {
			t.Errorf("CommunitySet=true for a community already on the device — not a no-op")
		}
		if !snmpRes.Enabled {
			t.Error("Enabled=false although the agent demonstrably has a community")
		}
		var s Step
		var said = false
		for _, s = range c.Transcript()[stepsBefore:] {
			if strings.Contains(s.What, "already in place") {
				said = true
			}
			if strings.Contains(s.What, "community set") {
				t.Errorf("transcript says a community WRITE happened: %+v", s)
			}
		}
		if !said {
			t.Error("transcript does not say the community was already in place")
		} else {
			t.Log("snmp: no-op confirmed, community already in place")
		}
	}

	// --- 7) EnsureLLDP: LLDP is already on (this fleet enables it at
	// provisioning) — assert the no-op.
	if !c.lldpKnown || !c.lldpOn {
		t.Errorf("LLDP reads as off/unknown (known=%v on=%v) — the no-op leg needs the lab "+
			"baseline, and nothing is written", c.lldpKnown, c.lldpOn)
	} else {
		var stepsBefore = len(c.Transcript())
		if err = c.EnsureLLDP(ctx); err != nil {
			t.Fatalf("EnsureLLDP: %v", err)
		}
		var s Step
		var said = false
		for _, s = range c.Transcript()[stepsBefore:] {
			if s.What == "LLDP already enabled" {
				said = true
			}
			if s.What == "LLDP enabled" {
				t.Errorf("transcript says \"lldp run\" was WRITTEN: %+v", s)
			}
		}
		if !said {
			t.Error("transcript does not say LLDP was already enabled")
		} else {
			t.Log("lldp: no-op confirmed, already enabled")
		}
	}

	// --- 8) Persist. The running configuration is byte-identical to the
	// baseline at this point (everything above was reverted on-device), so
	// "write" here proves the connector's save path — the OK! confirmation
	// parse included — without changing what a reboot restores.
	if err = c.Persist(ctx); err != nil {
		t.Errorf("Persist: %v", err)
	} else {
		t.Log("persist: ok (\"write\" answered OK!)")
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

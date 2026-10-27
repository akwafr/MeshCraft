package s3400

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// CAPTURING THE ENABLED SHAPE OF DHCP SNOOPING, through the connector's own
// paged reader and the connector's own builders.
//
// TODO.md asks for exactly this and spells out why the last clause matters:
// "`ip dhcp-relay snooping` on the lab unit, then `show ip dhcp-relay snooping`
// and `show running-config`, then revert THROUGH THE CONNECTOR'S OWN BUILDERS.
// That last clause is not ceremony — it is exactly how the storm-control removal
// defect on this range was found, and a hand revert in some other spelling would
// have hidden it."
//
// # WHY THIS IS A TEST AND NOT A TRANSCRIPT
//
// A hand capture was attempted first, on 2026-08-10, and it FAILED in the way
// this range always fails: with snooping OFF, `show ip dhcp-relay snooping`
// prints one line and a raw session reads it fine; with snooping ON the output
// is long enough to PAGE, the pager went unfed, and the read came back truncated
// mid-echo ("show ip dhc"). That is the trap `fs/s3400/ssh.go` § "The pager is
// FED, not disabled" describes, and it is why every capture on this range has to
// go through [cliConn.runPaged] — which is what [Client.readSnooping] already
// uses, on `show running-config`, with a 90 s budget.
//
// So this harness reads the way the CONNECTOR reads. The device was left off
// after that failed attempt (verified), and this test restores that state itself.
//
// # ALL THREE LEVELS, not just the global one — since 2026-08-10
//
// The first version of this harness enabled the feature switch-wide and stopped
// there, which settled ONE of the three forms this connector ships. The per-VLAN
// statement ([snoopVlanCmd]) and the per-port trust flag were still deduced from
// the BDCOM lineage, and a connector's second and third commands are exactly
// where a family's spelling stops matching.
//
// # THE THIRD LEG IS NOW A RE-PROBE OF AN ANSWER, and its expectation is inverted
//
// The run of 2026-08-10 settled the per-port leg: "ip dhcp-relay snooping trust"
// is answered "Unknown command", and the CLI's completion shows the level is
// missing rather than misspelled (dhcpsnoop.go's header holds the whole finding).
// The connector no longer builds that command — [snoopDeadTrustCmds] below is a
// TEST-LOCAL literal, kept so this exercise can go on asking the device rather
// than trusting a comment — and a REFUSAL is now the expected outcome. Acceptance
// is what fails the test, because it would mean the firmware grew a level the
// connector refuses on the strength of this very capture.
//
// So the exercise now runs the whole ladder — global, then one VLAN, then one
// port — and it does it entirely through the connector's own builders, for the
// reason TODO.md gives above. It picks its own targets from the DEVICE rather
// than from a constant:
//
//   - the VLAN is one the switch does NOT define ([snoopPickUnusedVLAN], read
//     from "show vlan" through the paged reader). Snooping a VLAN that exists
//     and carries clients is what makes this feature service-affecting; snooping
//     one that was never created inspects nothing, while still populating the
//     exact per-VLAN statement the parser has never seen;
//   - the port is a DARK one (GE1 on the lab unit, S3400_LIVE_SNOOP_PORT to
//     override). Trusting a port is not itself service-affecting — it is an
//     EXEMPTION from the drop — but a dark port keeps this exercise incapable of
//     touching traffic even in principle.
//
// DOUBLE-GATED, like live_write_test.go: S3400_LIVE_HOST for the session and
// S3400_LIVE_SNOOP_WRITE=1 for the write, because enabling DHCP snooping is
// service-affecting — every untrusted port stops forwarding DHCP OFFER/ACK. On
// the lab unit that costs nothing (every copper port is dark and management is
// static), and on anything else it would cost an outage. Do not set the second
// variable against a switch with clients behind it.
func TestLiveCaptureSnoopingEnabledShape(t *testing.T) {
	var tgt, cands = liveTarget(t)
	if os.Getenv("S3400_LIVE_SNOOP_WRITE") != "1" {
		t.Skip("S3400_LIVE_SNOOP_WRITE not set — snooping capture skipped")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	// --- BEFORE, through the connector's reader.
	var before, familyBefore, rerr = c.readSnooping(ctx)
	if rerr != nil {
		t.Fatalf("read before: %v", rerr)
	}
	t.Logf("BEFORE: enabled=%v family=%q vlans=%v ports=%d",
		before.Enabled, familyBefore, before.VLANs, len(before.Ports))

	// The family the DEVICE speaks, not this file's preference — the same
	// choice ApplyDHCPSnooping makes.
	var family = familyBefore
	if family == "" {
		family = snoopFamilyOf("-relay")
	}

	// --- The targets, chosen from the device (cf. the header).
	var vlanOut string
	vlanOut, err = c.cli.runPaged(ctx, "show vlan", 90*time.Second)
	if err != nil && strings.TrimSpace(vlanOut) == "" {
		t.Fatalf("show vlan: %v — no VLAN can be declared unused, so nothing is snooped", err)
	}
	var defined = parseShowVlan(vlanOut)
	if len(defined.VLANs) == 0 {
		t.Fatalf("\"show vlan\" listed NO vlan at all on %s: that is a truncated read, not a "+
			"device, and every id would look unused", tgt.Host)
	}
	var vlan = snoopPickUnusedVLAN(defined, os.Getenv("S3400_LIVE_SNOOP_VLAN"))
	if vlan == 0 {
		t.Fatal("no unused VLAN id could be found — refusing to snoop a VLAN this switch " +
			"actually carries")
	}
	var port = os.Getenv("S3400_LIVE_SNOOP_PORT")
	if port == "" {
		port = "GE1"
	}
	var wasTrusted, portKnown = snoopTrustOf(before, port)
	if portKnown && wasTrusted {
		t.Fatalf("%s already reads TRUSTED before the exercise: untrusting it at the end "+
			"would leave this switch in a state it did not start in", port)
	}
	t.Logf("targets: VLAN %d (undefined on this switch — %d VLAN(s) exist: %v), port %s "+
		"(trusted before: known=%v value=%v), family %q",
		vlan, len(defined.VLANs), snoopVLANIDs(defined), port, portKnown, wasTrusted, family)

	// --- REVERT IS ARMED BEFORE THE WRITE, and runs even if an assertion
	// fails. Leaving DHCP snooping enabled on a switch is the one outcome this
	// exercise must never produce.
	//
	// THREE LEVELS DOWN, in the reverse order they went up. The two that exist go
	// through the connector's OWN builder — "no" + [snoopVlanCmd], and
	// [snoopGlobalCmd] — because a hand revert in some other spelling is exactly
	// what hid the storm-control removal defect on this range. The trust leg has no
	// builder to use any more and undoes itself with [snoopDeadTrustCmds](false),
	// the same literal that went up.
	//
	// The global "no" goes last and is not relied upon to sweep the other two:
	// a firmware that keeps the per-VLAN and per-port statements after the feature
	// is switched off would leave them behind for the next run to trip over, and
	// this defer would never notice.
	//
	// The trust leg is undone ONLY IF IT LANDED. A "no" form of a command the
	// CLI does not have is refused for the same reason the positive form was, and
	// reporting that as REVERT FAILED would print an alarm about a flag that was
	// never set — which is exactly what this platform produced on 2026-08-10.
	var trustApplied = false
	defer func() {
		var rc = context.Background()
		var rctx, rcancel = context.WithTimeout(rc, 4*time.Minute)
		defer rcancel()
		var e error
		if trustApplied {
			e = c.configBlock(rctx, snoopDeadTrustCmds(c.cages, family, port, false))
			if e != nil {
				t.Errorf("REVERT (trust) FAILED — %s may still be TRUSTED: %v", port, e)
			}
		}
		e = c.configBlock(rctx, []string{"no " + snoopVlanCmd(family, vlan)})
		if e != nil {
			t.Errorf("REVERT (vlan) FAILED — VLAN %d may still be snooped: %v", vlan, e)
		}
		e = c.configBlock(rctx, []string{"no " + snoopGlobalCmd(family)})
		if e != nil {
			t.Errorf("REVERT FAILED — the device may still have snooping ON: %v", e)
			return
		}
		var after, _, e2 = c.readSnooping(rctx)
		if e2 != nil {
			t.Errorf("revert re-read failed: %v", e2)
			return
		}
		if after.Enabled {
			t.Errorf("REVERT DID NOT TAKE — snooping still reads enabled")
		}
		if len(after.VLANs) != 0 {
			t.Errorf("REVERT INCOMPLETE — snooping still names VLAN(s) %v", after.VLANs)
		}
		var stillTrusted, seen = snoopTrustOf(after, port)
		if seen && stillTrusted {
			t.Errorf("REVERT INCOMPLETE — %s still reads TRUSTED", port)
		}
		t.Logf("REVERTED: enabled=%v vlans=%v %s trusted=%v (seen=%v)",
			after.Enabled, after.VLANs, port, stillTrusted, seen)
	}()

	// --- 1) GLOBAL, using the connector's own builder.
	err = c.configBlock(ctx, []string{snoopGlobalCmd(family)})
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	t.Logf("sent: %s", snoopGlobalCmd(family))

	// --- 2) THE PER-VLAN STATEMENT. A refusal here is a RESULT, not a failure of
	// the exercise: it is the capture that stops a deduced form shipping, so it is
	// reported and the run carries on to the port.
	var vlanErr = c.configBlock(ctx, []string{snoopVlanCmd(family, vlan)})
	if vlanErr != nil {
		t.Errorf("PER-VLAN FORM REFUSED — \"%s\": %v. This is the deduced form this "+
			"connector ships; the refusal is the finding", snoopVlanCmd(family, vlan), vlanErr)
	} else {
		t.Logf("sent: %s", snoopVlanCmd(family, vlan))
	}

	// --- 3) THE PER-PORT TRUST FLAG — a re-probe, and the expectation is INVERTED.
	//
	// REFUSED ON 2026-08-10, and that refusal is what this connector is now built
	// on: "ip dhcp-relay snooping trust" answers "Unknown command" on OpCode
	// 2.0.2J, and the CLI's own completion says why it always will — inside an
	// "interface" block "ip ?" offers ACCESS-GROUP and IGMP-SNOOPING and
	// nothing else, at config level "ip dhcp-relay snooping ?" lists nine
	// sub-commands with no "trust" among them, and "… vlan <id> ?" offers only
	// "max-client". There is no per-port trust statement anywhere in this tree.
	//
	// So a refusal here is the EXPECTED result and is merely logged. ACCEPTANCE is
	// the failure: it would mean this firmware carries a level ApplyDHCPSnooping
	// refuses, and the refusal — not the acceptance — would be the thing to fix.
	var trustCmds = snoopDeadTrustCmds(c.cages, family, port, true)
	var trustErr = c.configBlock(ctx, trustCmds)
	if trustErr != nil {
		t.Logf("PER-PORT TRUST FORM REFUSED, as expected — \"%s\": %v. The level is ABSENT "+
			"from this platform; ApplyDHCPSnooping refuses every trust intent into Skipped "+
			"rather than emitting this", strings.Join(trustCmds, " ; "), trustErr)
	} else {
		trustApplied = true
		t.Errorf("PER-PORT TRUST FORM ACCEPTED — \"%s\". This contradicts the 2026-08-10 "+
			"capture this connector's refusal is built on (dhcpsnoop.go, snoopTrustRefusal): "+
			"if this firmware really carries the level, the refusal must be revisited and a "+
			"builder brought back — verify below whether the flag also READS back",
			strings.Join(trustCmds, " ; "))
	}

	// --- THE CAPTURE, through runPaged. This is the whole point.
	var raw string
	raw, err = c.cli.runPaged(ctx, "show running-config", 90*time.Second)
	if err != nil {
		t.Fatalf("paged running-config read: %v", err)
	}
	var lines []string
	var i int
	var all = strings.Split(raw, "\n")
	for i = range all {
		if strings.Contains(strings.ToLower(all[i]), "snoop") {
			lines = append(lines, strings.TrimRight(all[i], " \r"))
		}
	}
	t.Logf("ENABLED SHAPE — every snooping line in the running config (%d):", len(lines))
	for i = range lines {
		t.Logf("    %s", lines[i])
	}

	// --- And what the connector makes of it. Each level is asserted SEPARATELY:
	// the whole reason the harness was extended is that one landed form says
	// nothing about the next.
	var on, familyOn, oerr = c.readSnooping(ctx)
	if oerr != nil {
		t.Fatalf("read enabled: %v", oerr)
	}
	t.Logf("PARSED: enabled=%v family=%q vlans=%v ports=%d",
		on.Enabled, familyOn, on.VLANs, len(on.Ports))
	if !on.Enabled {
		t.Errorf("the connector read its OWN enable as disabled — parseDHCPSnooping "+
			"does not recognise the shape this firmware writes. Captured lines: %q", lines)
	}
	if vlanErr == nil && !snoopHasVLAN(on, vlan) {
		t.Errorf("\"%s\" was ACCEPTED and VLAN %d is absent from the re-read (%v) — an "+
			"accepted command that changes nothing is what an unproven form looks like from "+
			"here. Captured lines: %q", snoopVlanCmd(family, vlan), vlan, on.VLANs, lines)
	}
	var nowTrusted, nowKnown = snoopTrustOf(on, port)
	if trustErr == nil && !(nowKnown && nowTrusted) {
		t.Errorf("\"%s\" was ACCEPTED and %s does not read TRUSTED afterwards "+
			"(known=%v trusted=%v) — same reasoning. Captured lines: %q",
			strings.Join(trustCmds, " ; "), port, nowKnown, nowTrusted, lines)
	}
}

// snoopDeadTrustCmds builds the per-port form this platform DOES NOT HAVE.
//
// It lives here, in a test, and not in dhcpsnoop.go, which is the whole point: the
// connector must have no way to emit it (TestNoPlanCanStillEmitThePerPortTrustCommand
// asserts exactly that against the transcript). What this harness needs is the
// opposite — one place that can still type the dead command AT the device, so the
// finding stays a measurement rather than a memory, and so the revert leg can undo
// the leg that went up if some future firmware ever accepts it.
func snoopDeadTrustCmds(cages cageNumbering, family, standard string, trusted bool) []string {
	var line = family + " trust"
	if !trusted {
		line = "no " + line
	}
	// This harness types AT a live device, so the name must be the one that
	// device answers to (ADR-0018). An unobserved numbering falls back to the
	// standard name, which the CLI then refuses by name — visible, and this is
	// a capture harness, not a write path.
	var dev, err = cages.cliPortName(standard)
	if err != nil {
		dev = standard
	}
	return []string{"interface " + dev, line, "exit"}
}

// snoopPickUnusedVLAN returns a VLAN id this switch does NOT define, so that
// arming inspection on it cannot touch a frame.
//
// `want` is the operator's override (S3400_LIVE_SNOOP_VLAN) and is honoured only
// if it really is undefined: a harness that snooped whatever id it was handed
// would be one typo away from being the outage this file's gating exists to
// avoid. With no override it counts DOWN from 4094, because the ids a site
// actually uses cluster low.
func snoopPickUnusedVLAN(t VlanTable, want string) int {
	var defined = map[int]bool{}
	var v driver.VLAN
	for _, v = range t.VLANs {
		defined[v.ID] = true
	}
	var asked, err = strconv.Atoi(strings.TrimSpace(want))
	if err == nil && asked >= 1 && asked <= 4094 && !defined[asked] {
		return asked
	}
	var id int
	for id = 4094; id >= 2; id-- {
		if !defined[id] {
			return id
		}
	}
	return 0
}

func snoopVLANIDs(t VlanTable) []int {
	var out []int
	var v driver.VLAN
	for _, v = range t.VLANs {
		out = append(out, v.ID)
	}
	return out
}

// snoopHasVLAN: is this VLAN in the state the re-read produced?
func snoopHasVLAN(st driver.DHCPSnoopState, id int) bool {
	var v int
	for _, v = range st.VLANs {
		if v == id {
			return true
		}
	}
	return false
}

// snoopTrustOf returns the port's trust flag and whether it was OBSERVED at all.
// The second return is what keeps "this port has no interface block in the
// running-config" from collapsing into "untrusted" (cf.
// driver.DHCPSnoopPortState).
func snoopTrustOf(st driver.DHCPSnoopState, port string) (bool, bool) {
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if strings.EqualFold(p.Port, port) {
			return p.Trusted, true
		}
	}
	return false, false
}

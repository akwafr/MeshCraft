package instanton

// LIVE DHCP-SNOOPING VERB PROBE — the de-risking gesture, and nothing beyond it.
//
// WHY THIS EXISTS. The full [Admin.ApplyDHCPSnooping] run that would earn
// `WritesExtra("dhcp_snooping")` was WEIGHED AND DECLINED on 2026-08-11, on one
// specific asymmetry with the fs/s3900 run taken the same night: there, the
// TEARDOWN verbs had been typed on the hardware hours before the applier was let
// near it — the way back was proven before the way in. Here NEITHER the `set` NOR
// the `delete` had ever been sent to a 1930; both shapes were read out of the
// firmware's own JavaScript. A range with no SSH, no Telnet, no serial and no
// console cannot afford "the set took and the delete did not".
//
// So this harness sends both verbs, and only both verbs, on a VLAN that does not
// exist on the switch, with the switch-wide enable left ALONE. It is the cheapest
// thing that can refute "the delete does not work":
//
//   - the two writes are the exact bodies [snoopVLANInner] builds, through the
//     exact [Admin.writeSection] the applier uses. Nothing here is a lookalike;
//   - the global switch is NOT touched, and the harness REFUSES TO RUN if it
//     reads as already on — with snooping off switch-wide, a VLAN in the list
//     inspects nothing whatever the row says, so the blast radius is zero rather
//     than small;
//   - the VLAN is verified ABSENT from the device's own `VLANList` first. This
//     repo asserted a port role from prose for days and the device refuted it
//     (fs/s3900 GE28 was never the uplink); a VLAN nobody carries is a fact to
//     read, not to assume;
//   - nothing is saved to flash, as everywhere else on this range.
//
// WHAT IT PROVES, if it passes: the `set` verb reaches DHCPSnoopingVLANList and
// the row reads back; the `delete` verb removes it and the section returns to the
// bytes it served before. That is the teardown proven before the way in, which is
// the argument the S3900 run was armed on.
//
// Environment:
//
//	INSTANTON_LIVE_HOST        device management IP (required)
//	INSTANTON_LIVE_USER/PASS   admin credential
//	INSTANTON_LIVE_SOCKS       optional SOCKS5 address (e.g. 127.0.0.1:1080)
//	INSTANTON_LIVE_SOCKS_USER/PASS  SOCKS credential (operator email + token)
//	INSTANTON_LIVE_SNOOP_VERBS=1    ARMS THE TWO WRITES. Without it the harness
//	                                reads and reports what it would have done.
//	INSTANTON_LIVE_SNOOP_VLAN       VLAN id to pose and remove (default 777)

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

func TestLiveSnoopVLANVerbs(t *testing.T) {
	if os.Getenv("INSTANTON_LIVE_HOST") == "" {
		t.Skip("INSTANTON_LIVE_HOST not set")
	}
	var vlan = 777
	var raw = os.Getenv("INSTANTON_LIVE_SNOOP_VLAN")
	if raw != "" {
		var n, err = strconv.Atoi(raw)
		if err != nil || n < 1 || n > 4094 {
			t.Fatalf("INSTANTON_LIVE_SNOOP_VLAN=%q is not a VLAN id", raw)
		}
		vlan = n
	}
	var arm = os.Getenv("INSTANTON_LIVE_SNOOP_VERBS") == "1"

	// ONE session for the whole gesture: this range grants five in total and a
	// burst of probes exhausts it, coming back as a refusal on the NEXT gesture
	// rather than as a queue.
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var a = liveSnoopAdmin(ctx, t, "DHCP snooping VLAN verb probe (set then delete)")

	// ---- 1. What the device serves BEFORE anything ------------------------

	var loc = a.locateSnoopSections(ctx)
	if loc.vlan == "" {
		t.Fatalf("no per-VLAN snooping section answered (tried %s): "+
			"nothing to send a verb to", strings.Join(loc.tried, ", "))
	}
	t.Logf("VLAN section = %q, %d bytes", loc.vlan, len(loc.vlanBody))
	t.Logf("BEFORE body: %s", loc.vlanBody)

	// GUARDRAIL 1 — the switch-wide enable must be OFF. With it off, a row in
	// this list arms nothing: that is what makes the gesture free. With it on,
	// posing a VLAN starts inspecting real traffic on a switch whose rollback is
	// a power cycle needing a person on site.
	var enabled, seen = parseSnoopEnabled(loc.globalBody)
	t.Logf("switch-wide DHCP snooping: enabled=%v (field found=%v)", enabled, seen)
	if !seen {
		t.Fatalf("the global section %q did not carry an enable field (%s): "+
			"refusing to write a VLAN row without knowing whether inspection is armed",
			loc.global, strings.Join(snoopEnableFields, ", "))
	}
	if enabled {
		t.Fatalf("switch-wide DHCP snooping is ON: posing VLAN %d would arm real "+
			"inspection. This harness only runs against a switch where the feature "+
			"is globally off", vlan)
	}

	// GUARDRAIL 2 — the VLAN must not already be in the snooping list, or the
	// `delete` would remove somebody's configuration rather than our own row.
	var before = parseSnoopVLANs(loc.vlanSource())
	t.Logf("VLANs already inspected: %v", before)
	var v int
	for _, v = range before {
		if v == vlan {
			t.Fatalf("VLAN %d is ALREADY in %s: this harness must only remove the "+
				"row it posed itself", vlan, loc.vlan)
		}
	}

	// GUARDRAIL 3 — the VLAN must not EXIST on the switch. Read it, do not
	// assume it: a port role this repo asserted in prose for days was refuted by
	// the device (fs/s3900 GE28). If VLAN 777 turns out to carry ports, the
	// gesture is not the zero-blast-radius one it was authorised as.
	var vlBody, vlErr = a.RawSection(ctx, "VLANList")
	if vlErr != nil {
		t.Fatalf("could not read VLANList to confirm VLAN %d does not exist: %v", vlan, vlErr)
	}
	var existing = parseVLANRanges(xmlFieldAny(vlBody, "VLANList", "vlanList"))
	if len(existing) == 0 {
		// The section serves one row per VLAN on this firmware; fall back to the
		// row shape rather than declaring "no VLANs" on a switch that has some.
		existing = parseSnoopVLANs(vlBody)
	}
	t.Logf("VLANs configured on the switch: %v", existing)
	for _, v = range existing {
		if v == vlan {
			t.Fatalf("VLAN %d EXISTS on this switch: the gesture was authorised on "+
				"the premise that no port is on it. Refusing", vlan)
		}
	}

	if !arm {
		t.Logf("DRY RUN — set INSTANTON_LIVE_SNOOP_VERBS=1 to send the two verbs. "+
			"Would POST to %s?{%s}: set %s, then delete %s",
			a.wcdURL(), loc.vlan, snoopVLANInner(vlan), snoopVLANInner(vlan))
		return
	}

	// ---- 2. THE `set` VERB -------------------------------------------------

	var setErr = a.writeSection(ctx, loc.vlan, "set", snoopVLANInner(vlan),
		"DHCP snooping VLAN row posed (verb probe)")
	if setErr != nil {
		// A refused `set` is a perfectly good outcome for a probe: nothing was
		// written, and the shape read out of the JS is wrong. Report it and stop
		// — there is nothing to tear down.
		t.Fatalf("SET REFUSED (nothing written, no teardown owed): %v", setErr)
	}

	// INDEPENDENT re-read: a fresh GET of the section, not the write's own
	// answer. `statusCode 0` on this framework is exactly what a section returns
	// when it exists and the field written into it does not.
	var afterSet, rsErr = a.RawSection(ctx, loc.vlan)
	if rsErr != nil {
		t.Fatalf("SET sent but the re-read failed (%v) — VLAN %d may be posed; "+
			"the delete below is still attempted", rsErr, vlan)
	}
	t.Logf("AFTER SET body: %s", afterSet)
	var posed = parseSnoopVLANs(afterSet)
	t.Logf("VLANs inspected after set: %v", posed)
	var setLanded bool
	for _, v = range posed {
		if v == vlan {
			setLanded = true
		}
	}
	if setLanded {
		t.Logf("MEASURED: the `set` verb LANDED — VLAN %d reads back in %s", vlan, loc.vlan)
	} else {
		t.Errorf("MEASURED: the `set` verb answered code 0 and the row is NOT there. "+
			"This is the silent statusCode-0 no-op this connector is built around: "+
			"the captured row shape %s is not what this firmware wants",
			snoopVLANInner(vlan))
	}

	// ---- 3. THE `delete` VERB — the half that has never been sent ----------

	// ALWAYS attempted, even if the set did not read back: if the row landed in
	// some shape the read does not recognise, the delete is still the way out.
	var delErr = a.writeSection(ctx, loc.vlan, "delete", snoopVLANInner(vlan),
		"DHCP snooping VLAN row removed (verb probe)")
	if delErr != nil {
		t.Errorf("DELETE REFUSED: %v — VLAN %d may still be posed in %s. "+
			"Nothing was saved to flash, so a power cycle clears it",
			delErr, vlan, loc.vlan)
	}

	var afterDel, rdErr = a.RawSection(ctx, loc.vlan)
	if rdErr != nil {
		t.Fatalf("DELETE sent but the re-read failed: %v", rdErr)
	}
	t.Logf("AFTER DELETE body: %s", afterDel)
	var left = parseSnoopVLANs(afterDel)
	t.Logf("VLANs inspected after delete: %v", left)
	for _, v = range left {
		if v == vlan {
			t.Errorf("VLAN %d SURVIVED the delete. THE TEARDOWN VERB DOES NOT WORK "+
				"on this firmware — that is exactly what this probe existed to find "+
				"out, and it means the full snooping run must stay declined", vlan)
		}
	}

	// ---- 4. Byte-for-byte return to the opening snapshot -------------------

	if afterDel == loc.vlanBody {
		t.Logf("REVERT PROVED: %s serves byte-for-byte what it served before "+
			"(%d bytes)", loc.vlan, len(afterDel))
	} else {
		t.Errorf("section %s does NOT match its pre-run bytes.\nBEFORE (%d): %s\nAFTER (%d): %s",
			loc.vlan, len(loc.vlanBody), loc.vlanBody, len(afterDel), afterDel)
	}

	// The switch-wide enable must be exactly where it was: this harness never
	// wrote it, and saying so requires reading it rather than believing it.
	var gBody, gErr = a.RawSection(ctx, loc.global)
	if gErr == nil {
		var stillOn, stillSeen = parseSnoopEnabled(gBody)
		t.Logf("switch-wide DHCP snooping after the gesture: enabled=%v (found=%v)",
			stillOn, stillSeen)
		if stillOn {
			t.Errorf("the switch-wide enable is ON and this harness never wrote it")
		}
	}

	var s Step
	for _, s = range a.Steps {
		t.Logf("step ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
	var _ driver.DHCPSnoopApplier = a
}

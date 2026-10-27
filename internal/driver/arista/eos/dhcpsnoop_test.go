package eos

// DHCP snooping on EOS — the REFUSAL of the per-port trust level, and the two
// levels that survive it.
//
// # WHAT THESE TESTS REPLACED, because deleting them silently would lose the point
//
// Until 2026-08-10 this file tested `verifySnoopTrust`: five cases comparing a
// sent trust flag against a per-port table read back from the device. Both sides
// of that comparison were invented. The capture of that day (dhcpsnoop_live_test.go)
// established that "ip dhcp snooping trust" is answered "% Invalid input" on
// this hardware and that no per-port section is ever printed, so the function was
// verifying a command that cannot be sent against a grid that does not exist.
//
// What is tested here instead is the refusal that took its place: it fires, it
// names the PLATFORM rather than a failure, it lands in [driver.ApplyResult.Skipped],
// the global and per-VLAN levels still apply normally underneath it, and — the
// assertion that keeps the dead command from coming back — nothing this connector
// sends contains the string "ip dhcp snooping trust", asserted against the
// transcript of a fake CLI rather than against a comment.

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- the fake CLI ---------------------------------------------------------
//
// It is [portAdminFakeCLI] from portadmin_test.go, reused rather than copied:
// same package, same job (record every line, answer with a canned reply and a
// prompt), and a second fake would be a second thing to keep in step with
// [cliConn.run]. Only the reply table differs.

// snoopFakeClient returns a Client wired to the fake, whose "show ip dhcp
// snooping" answers `outputs` in order — the pre-write read first, then the
// verifying re-read. Reading past the end repeats the last one, which models a
// device that did not change.
func snoopFakeClient(outputs ...string) (*Client, *portAdminFakeCLI) {
	var n int
	var f = &portAdminFakeCLI{out: make(chan byte, 1<<20)}
	f.reply = func(cmd string) string {
		if cmd == SaveCmd {
			// The completion marker c.save waits for; without it the save falls
			// back and the transcript grows a second form for no reason.
			return "Copy completed successfully."
		}
		if cmd != showSnoopCmd {
			return ""
		}
		if len(outputs) == 0 {
			return snoopCapturedOff
		}
		if n < len(outputs) {
			n++
		}
		return outputs[n-1]
	}
	return &Client{
		cli:     &cliConn{stdin: f, out: f.out},
		host:    "192.0.2.5",
		User:    "mcadmin",
		dialect: dialectEOS,
	}, f
}

// snoopSent joins the transcript, so an assertion can look at the whole of it.
func snoopSent(f *portAdminFakeCLI) string { return strings.Join(f.sent, "\n") }

// snoopArmedVLAN is the VLAN [snoopCapturedOn] carries — the real enabled shape
// captured on core01 — so a plan naming it describes a switch already armed.
const snoopArmedVLAN = 777

// --- the absence of the dead command --------------------------------------

// THE ABSENCE TEST, and it names the exact string on purpose.
//
// "ip dhcp snooping trust" is what this connector emitted until 2026-08-10 and
// what core01 answers "% Invalid input" to. No plan, in either direction, may
// put it on the wire again. The assertion is on the TRANSCRIPT rather than on
// the source, so it holds however the command might be rebuilt — a helper, a
// format string, a fallback ladder.
func TestNoPlanCanStillEmitThePerPortTrustCommand(t *testing.T) {
	var plans = []driver.DHCPSnoopPlan{
		{Ports: []driver.DHCPSnoopPortIntent{{Port: "ET1", Trusted: true}}},
		{Ports: []driver.DHCPSnoopPortIntent{{Port: "ET1", Trusted: false}}},
		{VLANs: []int{snoopArmedVLAN}, Ports: []driver.DHCPSnoopPortIntent{
			{Port: "ET1", Trusted: true}, {Port: "ET2", Trusted: false}}},
		{VLANs: []int{42}, Ports: []driver.DHCPSnoopPortIntent{{Port: "ET49", Trusted: false}}},
	}
	var i int
	for i = range plans {
		var c, f = snoopFakeClient(snoopCapturedOn, snoopCapturedOn)
		var _, err = c.ApplyDHCPSnooping(t.Context(), plans[i])
		if err != nil {
			t.Fatalf("plan %d: %v", i, err)
		}
		var sent = strings.ToLower(snoopSent(f))
		if strings.Contains(sent, "ip dhcp snooping trust") {
			t.Fatalf("plan %d put the dead command on the wire — \"ip dhcp snooping trust\" "+
				"is answered \"%% Invalid input\" on this hardware:\n%s", i, snoopSent(f))
		}
		// The "no" form is the same absent command in the other direction.
		if strings.Contains(sent, "snooping trust") {
			t.Fatalf("plan %d sent a per-port trust command:\n%s", i, snoopSent(f))
		}
	}
}

// --- the refusal ----------------------------------------------------------

// The refusal fires, it is in Skipped, and it is NOT a Go error: a level the
// hardware does not carry is an ordinary outcome, and driver/dhcpsnoop.go says
// where an ordinary refusal goes.
func TestPerPortTrustIsRefusedIntoSkipped(t *testing.T) {
	var c, _ = snoopFakeClient(snoopCapturedOn, snoopCapturedOn)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{snoopArmedVLAN},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ET49", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("a platform that lacks a level is not a device that failed: %v", err)
	}
	if len(res.Skipped) == 0 {
		t.Fatal("the trust intent vanished: a plan asking for something impossible must SAY " +
			"so, and the one thing worse than emitting a bad command is accepting the intent")
	}
	if !strings.Contains(res.Skipped[0], "ET49") {
		t.Errorf("the refusal must name the port: %q", res.Skipped[0])
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten=%v — nothing per-port was sent, so nothing is confirmed",
			res.PortsWritten)
	}
}

// The refusal names the PLATFORM FACT rather than a failure. An operator reading
// it must be able to tell "this switch does not have this" from "meshcraft could
// not do it" — the first calls for a different plan, the second for a retry.
func TestTheRefusalNamesThePlatformAndNotAFailure(t *testing.T) {
	var skipped, _ = snoopTrustSkipped([]driver.DHCPSnoopPortIntent{{Port: "ET1", Trusted: true}})
	if len(skipped) != 1 {
		t.Fatalf("one intent, one refusal: %v", skipped)
	}
	var got = skipped[0]
	var must = []string{
		"ET1",                       // which port
		"NO PER-PORT DHCP SNOOPING", // what is missing
		"ABSENT from this hardware", // and that it is the hardware, not a bug
		"% Invalid input",           // the observation itself
		"DCS-7050SX-64-R",           // on which box
		"2026-08-10",                // and when
		"left OUT of the inspected", // what the operator can do instead
	}
	var want string
	for _, want = range must {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal does not carry %q:\n%s", want, got)
		}
	}
	// Words that would describe a device that misbehaved. None of them is true
	// here and each would send an operator looking for a fault that does not exist.
	var forbidden = []string{"refused the command", "timed out", "try again", "unsupported by meshcraft"}
	for _, want = range forbidden {
		if strings.Contains(strings.ToLower(got), want) {
			t.Errorf("the refusal reads as a failure (%q):\n%s", want, got)
		}
	}
}

// BOTH DIRECTIONS are refused, and that is deliberate. "Untrusted" is the
// permanent state of every port here, so granting the intent silently would be
// harmless in effect and wrong in principle: the plan asked for a level, and the
// contract forbids accepting an intent and reporting success.
func TestUntrustingIsRefusedToo(t *testing.T) {
	var skipped, wantsTrust = snoopTrustSkipped([]driver.DHCPSnoopPortIntent{
		{Port: "ET1", Trusted: false},
	})
	if len(skipped) != 1 || !strings.Contains(skipped[0], "NOT set untrusted") {
		t.Fatalf("an untrust intent must be refused and say which direction it asked for: %v",
			skipped)
	}
	if wantsTrust {
		t.Error("no intent asked for TRUST: the arming guard must not fire on this plan")
	}
}

// Deterministic and in faceplate order: two identical plans must produce two
// identical results, or a diff between two runs cannot be read.
func TestTheRefusalsAreOrderedByFaceplate(t *testing.T) {
	var skipped, wantsTrust = snoopTrustSkipped([]driver.DHCPSnoopPortIntent{
		{Port: "ET49", Trusted: false},
		{Port: "ET2", Trusted: true},
		{Port: "ET10", Trusted: false},
	})
	if len(skipped) != 3 {
		t.Fatalf("three intents, three refusals: %v", skipped)
	}
	if !strings.HasPrefix(skipped[0], "ET2:") || !strings.HasPrefix(skipped[1], "ET10:") ||
		!strings.HasPrefix(skipped[2], "ET49:") {
		t.Errorf("order=%q %q %q — want ET2, ET10, ET49",
			firstField(skipped[0]), firstField(skipped[1]), firstField(skipped[2]))
	}
	if !wantsTrust {
		t.Error("ET2 asked to be TRUSTED")
	}
}

func firstField(s string) string {
	var i = strings.Index(s, ":")
	if i < 0 {
		return s
	}
	return s[:i]
}

// --- the two levels that DO work ------------------------------------------

// A plan with no port intent is the ordinary global + per-VLAN gesture, and it
// must be untouched by any of the above: both forms were typed at core01's CLI
// on 2026-08-10 and the feature came up on VLAN 777.
func TestTheGlobalAndPerVLANLevelsStillApply(t *testing.T) {
	var c, f = snoopFakeClient(snoopCapturedOff, snoopCapturedOn)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{VLANs: []int{777}})
	if err != nil {
		t.Fatalf("apply: %v (skipped=%v)", err, res.Skipped)
	}
	var sent = snoopSent(f)
	if !strings.Contains(sent, "\nip dhcp snooping\n") {
		t.Errorf("the global enable was not sent:\n%s", sent)
	}
	if !strings.Contains(sent, "ip dhcp snooping vlan 777") {
		t.Errorf("the per-VLAN statement was not sent:\n%s", sent)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped=%v — nothing was refused on a plan with no port intent", res.Skipped)
	}
	// The device-wide consequence has to be readable afterwards, and on THIS
	// platform it is sharper than the contract's own wording: there is no port to
	// exempt.
	var notes = strings.Join(res.Notes, " | ")
	if !strings.Contains(notes, "turned it ON") ||
		!strings.Contains(notes, "NO PER-PORT EXEMPTION") {
		t.Errorf("notes must say the feature was armed AND that no port can be exempted: %q",
			notes)
	}
}

// A plan whose ports are all refused and whose two other levels are already in
// place writes NOTHING — not even a "copy running-config startup-config". A
// flash write for a gesture that changed nothing is a real cost on a switch, and
// the save is also what would make a caller believe something landed.
func TestARefusedOnlyPlanTouchesNothing(t *testing.T) {
	var c, f = snoopFakeClient(snoopCapturedOn, snoopCapturedOn)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{snoopArmedVLAN},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ET1", Trusted: false}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if snoopSent(f) != showSnoopCmd {
		t.Errorf("the only command allowed here is the read; transcript:\n%s", snoopSent(f))
	}
	if len(res.Skipped) != 1 {
		t.Errorf("Skipped=%v — the one intent must still be reported", res.Skipped)
	}
}

// --- the arming guard -----------------------------------------------------

// THE CASE THAT WOULD DO HARM, and the reason the per-port refusal is not the
// whole answer: a plan that wants an uplink TRUSTED and would also have to turn
// inspection on. The VLANs it arms are the ones crossing that uplink, and with
// no exemption available the switch would start dropping the DHCP OFFER/ACK the
// server answers with. Nothing is written.
func TestArmingIsRefusedWhenThePlanWantedATrustedPort(t *testing.T) {
	var c, f = snoopFakeClient(snoopCapturedOff, snoopCapturedOff)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{42},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ET49", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("this is a refusal, not a failure: %v", err)
	}
	if snoopSent(f) != showSnoopCmd {
		t.Fatalf("NOTHING may be written for this plan; transcript:\n%s", snoopSent(f))
	}
	var joined = strings.Join(res.Skipped, " | ")
	if !strings.Contains(joined, "NOT armed") {
		t.Errorf("Skipped must say the feature was not armed and why: %q", joined)
	}
	if !strings.Contains(joined, "outage in place of the protection") {
		t.Errorf("the refusal must name what it prevented: %q", joined)
	}
	if !strings.Contains(joined, "ET49") {
		t.Errorf("the port intent must still be reported on its own: %q", joined)
	}
}

// The guard is NARROW. A plan that only asks for untrusted ports is asking for
// exactly the two levels this platform has, and arming them is what the operator
// wants — the refusals ride along in Skipped and change nothing else.
func TestArmingProceedsForAnUntrustOnlyPlan(t *testing.T) {
	var c, f = snoopFakeClient(snoopCapturedOff, snoopCapturedOn)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{777},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ET1", Trusted: false}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(snoopSent(f), "\nip dhcp snooping\n") {
		t.Errorf("the global enable must still go out:\n%s", snoopSent(f))
	}
	if !strings.Contains(strings.Join(res.Skipped, " | "), "ET1") {
		t.Errorf("Skipped=%v — the intent is still refused", res.Skipped)
	}
	if strings.Contains(strings.Join(res.Skipped, " | "), "NOT armed") {
		t.Errorf("the arming guard fired on a plan that asked to TRUST nothing: %v", res.Skipped)
	}
}

// And it does not fire on a switch that is ALREADY snooping those VLANs: there
// is nothing to arm, and the state it would have refused to create is one this
// apply did not create. Refusing there would withhold nothing and report a
// decision nobody made.
func TestArmingGuardDoesNotFireOnAnAlreadyArmedSwitch(t *testing.T) {
	var c, f = snoopFakeClient(snoopCapturedOn, snoopCapturedOn)
	var res, err = c.ApplyDHCPSnooping(t.Context(), driver.DHCPSnoopPlan{
		VLANs: []int{snoopArmedVLAN},
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ET1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if strings.Contains(strings.Join(res.Skipped, " | "), "NOT armed") {
		t.Errorf("nothing had to be armed on this switch: %v", res.Skipped)
	}
	if snoopSent(f) != showSnoopCmd {
		t.Errorf("the device already carried the two levels: nothing to send:\n%s", snoopSent(f))
	}
}

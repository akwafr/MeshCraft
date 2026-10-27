package edgeswitch

import (
	"context"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Replayed against the REAL baseline capture (testdata/show_poe_port_all.txt,
// show_poe_status_all.txt — lab EdgePoint S16, 2026-08-05, every port left as
// found: administratively Shutdown, nothing plugged in). The regression this
// guards: ports 0/1-0/4 must read Capable=false (Temperature "N/A" on this
// hardware) while 0/5-0/16 must read Capable=true (a real reading) — the
// per-port SCOPE split this package exists to carry.
func TestReadPoEBaselineCapture(t *testing.T) {
	var portOut = mustRead(t, "testdata/show_poe_port_all.txt")
	var statusOut = mustRead(t, "testdata/show_poe_status_all.txt")
	var st = mergePoEState(parsePoEPortAll(portOut), parsePoEStatusAll(statusOut))

	if len(st.Ports) != 16 {
		t.Fatalf("expected 16 PoE-scoped ports (0/1-0/16), got %d: %+v", len(st.Ports), st.Ports)
	}
	var byName = map[string]driver.PoEPortState{}
	var p driver.PoEPortState
	for _, p = range st.Ports {
		byName[p.Port] = p
	}

	var noPSE = []string{"GE1", "GE2", "GE3", "GE4"}
	var n string
	for _, n = range noPSE {
		var ps = byName[n]
		if ps.Capable {
			t.Errorf("%s: no PSE hardware behind this jack (Temperature N/A) — must read Capable=false, got %+v", n, ps)
		}
		if ps.Class != driver.PoEClassNone {
			t.Errorf("%s: OP Mode is Shutdown — must read class %q, got %q", n, driver.PoEClassNone, ps.Class)
		}
	}

	var withPSE = []string{"GE5", "GE6", "GE9", "GE13", "GE16"}
	for _, n = range withPSE {
		var ps = byName[n]
		if !ps.Capable {
			t.Errorf("%s: a real Temperature reading was captured — must read Capable=true, got %+v", n, ps)
		}
		if ps.Role != driver.PoERoleOut {
			t.Errorf("%s: this platform has no observed PoE-in port — role must be %q, got %q", n, driver.PoERoleOut, ps.Role)
		}
	}

	// GE1 carries a residual 52.32V reading despite being Shutdown and having
	// no PSE hardware behind it (cf. the file header / poe.go) — captured
	// verbatim, not smoothed away, but it must not flip Delivering: nothing
	// is drawing power (Consumed(W) reads 0.00).
	if byName["GE1"].Delivering {
		t.Errorf("GE1: Consumed(W) is 0.00 — must not read as delivering, got %+v", byName["GE1"])
	}

	// SFP+ cages never appear in either table: they carry no PoE scope, and
	// mergePoEState must not invent an entry for them.
	var _, sawSFP = byName["GE17"]
	if sawSFP {
		t.Error("GE17 (SFP+) must not appear in the PoE state: this platform's PoE tables never list it")
	}
}

func TestClassOfOpMode(t *testing.T) {
	var cases = []struct {
		opMode string
		want   string
	}{
		{"Shutdown", driver.PoEClassNone},
		{"Auto", driver.PoEClass54V},
		{"Passive24V", driver.PoEClass24V},
	}
	var c struct {
		opMode string
		want   string
	}
	for _, c = range cases {
		var got, ok = classOfOpMode(c.opMode)
		if !ok || got != c.want {
			t.Errorf("classOfOpMode(%q) = %q, %v — want %q, true", c.opMode, got, ok, c.want)
		}
	}
	var _, unknownOk = classOfOpMode("Something Else")
	if unknownOk {
		t.Error("an unrecognized OP Mode must not be mapped to a guessed class")
	}
}

func TestOpModeOfClass(t *testing.T) {
	var mode, confirm, ok = opModeOfClass(driver.PoEClassNone)
	if !ok || mode != "shutdown" || confirm {
		t.Errorf("PoEClassNone: got %q confirm=%v ok=%v", mode, confirm, ok)
	}
	mode, confirm, ok = opModeOfClass(driver.PoEClass54V)
	if !ok || mode != "auto" || confirm {
		t.Errorf("PoEClass54V: got %q confirm=%v ok=%v", mode, confirm, ok)
	}
	mode, confirm, ok = opModeOfClass(driver.PoEClass24V)
	if !ok || mode != "passive24v" || !confirm {
		t.Errorf("PoEClass24V: got %q confirm=%v ok=%v — passive modes must ask confirmation", mode, confirm, ok)
	}
	if _, _, ok = opModeOfClass("77v"); ok {
		t.Error("an unknown class must be refused, not rounded to a known one")
	}
}

// TestNoFourPairModeCanEverBeREQUESTED is the write-side half of
// [TestClassOfOpModeRefusesTheFourPairVariantsRatherThanGuessing], and it pins an
// ABSENCE that is easy to close by accident.
//
// The shared vocabulary (driver.PoEClass*) has three values and none of them
// means "4-pair": driver/poe.go groups a passive 4-pair 54V mode under
// [driver.PoEClass54V] and a 4-pair 24V one under [driver.PoEClass24V]. So there
// is no way for a caller to ASK for 4-pair, and opModeOfClass must never emit one
// on its own initiative — the lab unit answers "Unsupported Interfaces" to both,
// and a connector that sent `passive24v-4pair` for a plain 24v intent would be
// energising four pairs nobody asked to energise.
//
// The day a unit ACCEPTS them, this test is what should fail: the fix is a RICHER
// INTENT in driver/poe.go, not a wider substring here. Widening the map without
// widening the vocabulary would make the class a caller sent and the class the
// port carries two different things, which is the exact confusion classOfOpMode
// was corrected for on the read side.
func TestNoFourPairModeCanEverBeREQUESTED(t *testing.T) {
	var class string
	for _, class = range []string{
		driver.PoEClassNone, driver.PoEClass24V, driver.PoEClass54V, "",
	} {
		var mode, _, ok = opModeOfClass(class)
		if !ok {
			continue
		}
		if strings.Contains(mode, "4pair") || strings.Contains(mode, "4-pair") {
			t.Errorf("class %q asked for CLI mode %q — the standard vocabulary cannot "+
				"express 4-pair, so nothing may send it", class, mode)
		}
		// And whatever it sends must read back AS THE SAME CLASS. CyclePortPower's
		// whole contract is that the class it read comes back identically.
		var back, rok = classOfOpMode(mode)
		if !rok || back != class {
			t.Errorf("class %q → %q → %q (ok=%v): the write and the read disagree, so a "+
				"power cycle would restore a different class than it cut", class, mode, back, rok)
		}
	}
}

// CAPTURED 2026-08-05 on port 0/1 (no PSE hardware): the CLI's own rejection
// forms, which the shared reCLIError (ssh.go) does not recognize since they
// are this command's own vocabulary.
func TestRePoERejected(t *testing.T) {
	var cases = []string{
		"Unsupported Interfaces: 0/1",
		"Error Interfaces: 0/1",
	}
	var c string
	for _, c = range cases {
		if !rePoERejected.MatchString(c) {
			t.Errorf("must recognize %q as a PoE rejection", c)
		}
	}
	if rePoERejected.MatchString("PoE class set on 1 port(s)") {
		t.Error("an ordinary success line must not be read as a rejection")
	}
}

// "Open Circuit" is the one documented Detection value carrying a space —
// a row using it must still parse, with Detection carried through untouched.
func TestParsePoEStatusOpenCircuit(t *testing.T) {
	var out = "0/3       Open Circuit   Unknown        0.00       0.00        0.00             41\n"
	var rows = parsePoEStatusAll(out)
	var row, ok = rows["0/3"]
	if !ok {
		t.Fatalf("row not parsed: %q", out)
	}
	if row.detection != "Open Circuit" {
		t.Errorf("detection = %q, want \"Open Circuit\"", row.detection)
	}
	if row.temperatureC == nil || *row.temperatureC != 41 {
		t.Errorf("temperature = %v, want 41", row.temperatureC)
	}
}

// The reference's six-column example (no "Consumed Meter(Whr)") must parse
// too — the column is a firmware ADDITION, not a fixed part of the shape.
func TestParsePoEStatusWithoutMeterColumn(t *testing.T) {
	var out = "0/1       Short          Unknown        0.00       0.00        0.00             37\n"
	var rows = parsePoEStatusAll(out)
	var row, ok = rows["0/1"]
	if !ok {
		t.Fatalf("row without the meter column must still parse: %q", out)
	}
	if row.temperatureC == nil || *row.temperatureC != 37 {
		t.Errorf("temperature = %v, want 37 (meter column absent must not shift the parse)", row.temperatureC)
	}
}

// verifyPoEClasses decides what was WRITTEN, as distinct from what was SENT, and
// this is a regression guard on a defect that was reachable by operators.
//
// ApplyPoE used to set `res.PortsWritten` from the list of ports it had sent
// commands to, BEFORE this verification ran, and never pruned it afterwards. The
// result contradicted itself — a port whose class read back wrong was reported
// both as written and as skipped — and a re-read that returned nothing at all
// reported every port as written. `poe` IS declared in WritesExtra, so this was
// live, on a gesture with physical consequences for whatever is plugged in.
func TestVerifyPoEClassesSeparatesConfirmedFromSent(t *testing.T) {
	var want = map[string]string{
		"0/5": driver.PoEClass54V,
		"0/6": driver.PoEClass24V,
		"0/7": driver.PoEClassNone,
	}
	var sent = []string{"0/5", "0/6", "0/7"}
	// The device agrees about 0/5, disagrees about 0/6, and does not mention 0/7.
	var after = driver.PoEState{Ports: []driver.PoEPortState{
		{Port: "0/5", Capable: true, Class: driver.PoEClass54V},
		{Port: "0/6", Capable: true, Class: driver.PoEClassNone},
	}}

	var confirmed, mismatched = verifyPoEClasses(after, sent, want)

	if len(confirmed) != 1 || confirmed[0] != "0/5" {
		t.Errorf("confirmed = %v, only 0/5 read back as asked", confirmed)
	}
	if len(mismatched) != 2 {
		t.Fatalf("mismatched = %v, expected one for 0/6 and one for 0/7", mismatched)
	}
	// The two failures are NOT the same fact and must not read alike: 0/6 was
	// contradicted, 0/7 was never mentioned. Collapsing them would hide that the
	// device said nothing about a jack we cut.
	var joined = strings.Join(mismatched, " | ")
	if !strings.Contains(joined, "0/6") || !strings.Contains(joined, "reads back") {
		t.Errorf("0/6 must be reported as contradicted: %s", joined)
	}
	if !strings.Contains(joined, "0/7") || !strings.Contains(joined, "does not appear") {
		t.Errorf("0/7 must be reported as absent from the re-read, not as an empty "+
			"class — \"the device says nothing\" is not \"the device says none\": %s", joined)
	}
}

// A re-read that produced NOTHING must confirm NOTHING. This is the shape that
// used to report the entire plan as written: an empty state made every
// comparison unequal, and PortsWritten had already been filled from the sent
// list, so the caller was told everything landed while Skipped listed
// everything.
func TestVerifyPoEClassesConfirmsNothingOnAnEmptyReRead(t *testing.T) {
	var want = map[string]string{"0/5": driver.PoEClass54V, "0/6": driver.PoEClass54V}
	var confirmed, mismatched = verifyPoEClasses(
		driver.PoEState{}, []string{"0/5", "0/6"}, want)
	if len(confirmed) != 0 {
		t.Errorf("confirmed = %v, a re-read with no ports proves nothing", confirmed)
	}
	if len(mismatched) != 2 {
		t.Errorf("mismatched = %v, both ports must be reported unconfirmed", mismatched)
	}
}

// --- a re-read that never LANDED is not a device that DISAGREED -----------
//
// The distinction [driver.ApplyResult.PortsWritten] draws between its three
// non-confirmations, on the one path in this tree that has physical consequences
// for whatever is plugged in.
//
// reReadPoE used to swallow the read error and hand verifyPoEClasses the zero
// [driver.PoEState]. Every comparison then came back unequal and every port the
// write had touched was reported in Skipped as "does not appear in "show poe
// port all"" — which says the DEVICE did not carry the class, when meshcraft
// never got to ask. Skipped is what meshcraft REFUSED to do, so that also put a
// "force" button under a lost session.

// poeSilentCLI accepts every line and never answers: the shape of a session that
// went away mid-apply. Nothing here needs an SSH client — cliConn.run wants a
// writer and a byte channel, and that is all.
type poeSilentCLI struct{ sent []string }

func (f *poeSilentCLI) Write(p []byte) (int, error) {
	var line = strings.TrimRight(string(p), " \t\r\n")
	if line != "" {
		f.sent = append(f.sent, line)
	}
	return len(p), nil
}

func (f *poeSilentCLI) Close() error { return nil }

// A dead context is how this test refuses to spend showTimeout waiting for a
// prompt that is never coming: cliConn.waitAny selects on ctx.Done first.
func TestReReadPoEReturnsTheFailureInsteadOfAnEmptyState(t *testing.T) {
	var f = &poeSilentCLI{}
	var c = &Client{cli: &cliConn{stdin: f, out: make(chan byte, 1024)},
		host: "192.0.2.4", known: true, privileged: true}

	var ctx, cancel = context.WithCancel(t.Context())
	cancel()

	var st, err = c.reReadPoE(ctx, poeVerifyAttempts)
	if err == nil {
		t.Fatal("a re-read that could not be taken returned a nil error: the callers cannot " +
			"then tell it apart from a device that lists no PoE port at all, which is the " +
			"confusion driver.ApplyResult.PortsWritten names as its third non-confirmation")
	}
	if len(st.Ports) != 0 {
		t.Errorf("a failed re-read must carry no ports, got %+v", st.Ports)
	}
}

// Zero attempts must not read as a clean empty answer either — the same trap by
// another route.
func TestReReadPoEWithNoAttemptsIsAFailureNotAnEmptyAnswer(t *testing.T) {
	var f = &poeSilentCLI{}
	var c = &Client{cli: &cliConn{stdin: f, out: make(chan byte, 1024)}}
	var _, err = c.reReadPoE(t.Context(), 0)
	if err == nil {
		t.Error("reReadPoE(0) returned no error: nothing was read, so nothing may be confirmed")
	}
	if len(f.sent) != 0 {
		t.Errorf("nothing should have been sent, got %q", f.sent)
	}
}

// poeApplyFakeCLI answers the pre-read and the write, then goes silent from a
// chosen command onwards: the session that survives long enough to send
// "poe opmode" and not long enough to prove it landed.
//
// Going silent means writing NO prompt, so cliConn.run has nothing to complete
// on; `die` then cancels the caller's context so the test costs microseconds
// instead of the full showTimeout. That is exactly the shape cliConn sees when
// the SSH channel goes away mid-apply.
// A command that goes silent must emit NO prompt AT ALL, which is why silence is
// decided BEFORE the reply rather than inside it: a fake that cancels the context
// and then writes a prompt anyway leaves cliConn.waitAny with two ready cases in
// one select, and Go picks between them at random. That test passes about half
// the time, which is worse than one that never does.
type poeApplyFakeCLI struct {
	sent []string
	out  chan byte
	// reply answers a command that is still being served.
	reply func(cmd string) string
	// goesSilent is consulted before each reply: the first command it accepts,
	// and every command after it, gets no answer.
	goesSilent func(cmd string) bool
	silent     bool
	die        func()
}

func (f *poeApplyFakeCLI) Write(p []byte) (int, error) {
	var line = strings.TrimRight(string(p), " \t\r\n")
	if line == "" {
		return len(p), nil
	}
	f.sent = append(f.sent, line)
	if !f.silent && f.goesSilent != nil && f.goesSilent(line) {
		f.silent = true
	}
	if f.silent {
		f.die()
		return len(p), nil
	}
	var b byte
	for _, b = range []byte(f.reply(line) + "\n(UBNT EdgeSwitch) #") {
		f.out <- b
	}
	return len(p), nil
}

func (f *poeApplyFakeCLI) Close() error { return nil }

// THE BRANCH: 0/5 is sent "poe opmode auto", the verifying re-read cannot be
// taken, and the result must say SENT-AND-NOT-CONFIRMED — in Notes, naming the
// port — while claiming nothing in PortsWritten and refusing nothing in Skipped.
func TestApplyPoEReportsAFailedReReadAsUnconfirmedAndNotAsARefusal(t *testing.T) {
	var portOut = mustRead(t, "testdata/show_poe_port_all.txt")
	var statusOut = mustRead(t, "testdata/show_poe_status_all.txt")

	var ctx, cancel = context.WithCancel(t.Context())
	defer cancel()

	// "show poe port all" is the first command of BOTH the pre-write read and
	// the verifying re-read. The SECOND occurrence is the re-read, and that is
	// where the session dies — after the write went out, before it can be checked.
	var reads int
	var f = &poeApplyFakeCLI{out: make(chan byte, 1<<20), die: cancel}
	f.goesSilent = func(cmd string) bool {
		if cmd != "show poe port all" {
			return false
		}
		reads++
		return reads == 2
	}
	f.reply = func(cmd string) string {
		switch cmd {
		case "show poe port all":
			return portOut
		case "show poe status all":
			return statusOut
		}
		return ""
	}
	var c = &Client{cli: &cliConn{stdin: f, out: f.out},
		host: "192.0.2.4", known: true, privileged: true}

	var res, err = c.ApplyPoE(ctx, driver.PoEPlan{Ports: []driver.PoEPortIntent{
		{Port: "0/5", Class: driver.PoEClass54V},
	}})
	if err != nil {
		t.Fatalf("a failed VERIFICATION is not a failed write and must not be an error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten = %v, want empty: nothing was observed, so nothing is confirmed",
			res.PortsWritten)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want empty: meshcraft refused nothing here, and putting a lost "+
			"re-read there offers the operator a \"force\" button for a network hiccup",
			res.Skipped)
	}
	if !notesMention(res.Notes, "0/5") || !notesMention(res.Notes, "NOT confirmed") {
		t.Errorf("the note must name the port and say the write was not confirmed: %v", res.Notes)
	}
	if reads < 1 {
		t.Error("the pre-write read never happened; the fixture is wrong, not the code")
	}
}

// The 4-PAIR modes must resolve to UNSET-with-a-reason, never to their 2-pair
// sibling. Pinned in both directions, because the failure is silent: the wrong
// answer here is a plausible one, and it feeds CyclePortPower's restore.
func TestClassOfOpModeRefusesTheFourPairVariantsRatherThanGuessing(t *testing.T) {
	// The 2-pair modes still resolve — the refusal must be narrow.
	var cl, ok = classOfOpMode("Passive24V")
	if !ok || cl != driver.PoEClass24V {
		t.Errorf("Passive24V = %q,%v — the 2-pair mode is captured and must still map", cl, ok)
	}
	if cl, ok = classOfOpMode("Auto"); !ok || cl != driver.PoEClass54V {
		t.Errorf("Auto = %q,%v", cl, ok)
	}

	var fourPair string
	for _, fourPair = range []string{
		"Passive24V-4pair", "Passive54V-4pair", "passive24v-4Pair", "Passive54V4pair"} {

		cl, ok = classOfOpMode(fourPair)
		if ok {
			t.Errorf("classOfOpMode(%q) = %q — a 4-pair mode mapped onto a class. Substring "+
				"matching made it read as its TWO-pair sibling, which CyclePortPower would "+
				"then restore: the jack comes back at half the pairs it was delivering on",
				fourPair, cl)
		}
	}
}

// End to end through the reader: an unrecognized OP Mode leaves the class empty
// AND says why. An empty class with no note would be indistinguishable from a
// port the device reports nothing about.
func TestReadPoENamesAnUnrecognizedOpModeInsteadOfGuessingAClass(t *testing.T) {
	var portOut = "" +
		"0/5       Passive54V-4pair Enable  802.3at Enable  Enable     Enable\n"
	var statusOut = "0/5       Good           Unknown        0.00       0.00        0.00             39\n"

	var st = mergePoEState(parsePoEPortAll(portOut), parsePoEStatusAll(statusOut))
	if len(st.Ports) != 1 {
		t.Fatalf("expected the one row to parse, got %+v", st.Ports)
	}
	if st.Ports[0].Class != "" {
		t.Errorf("class = %q, want unset: this mode is not one this connector can read back",
			st.Ports[0].Class)
	}
	if !notesMention(st.Notes, "not recognized") || !notesMention(st.Notes, "Passive54V-4pair") {
		t.Errorf("the note must name the mode the device printed: %v", st.Notes)
	}
}

// The safety consequence, stated as its own guard: a port whose class could not
// be read is NOT cyclable, because the restore half would have nothing truthful
// to put back. TestEveryReadableClassIsSendable pins the other half of the same
// invariant — everything the read CAN produce, the write can send.
func TestAnUnreadableClassIsNotSendableEither(t *testing.T) {
	var _, _, sendable = opModeOfClass("")
	if sendable {
		t.Error("an unset class must not be sendable: CyclePortPower gates its cut on this, " +
			"and a port whose class is unknown must be skipped rather than cut")
	}
}

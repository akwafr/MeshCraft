package api

import "testing"

// The rules a fleet-wide credential deployment turns on are the ones that spend the
// fleet's session budget and decide which switch has its only door changed. Every one
// of them is pure, and every one of them is pinned here without a database, a proxy or
// a device — because the failure they guard against is not a wrong HTTP code, it is a
// site visit with a paperclip to a CSS326 that has no serial port.

// reached builds a device the run has already been inside: visible, addressable, with
// a confirmed generation and a recorded account.
func reached(name string, gen int) credDeployTarget {
	return credDeployTarget{
		DeviceID: name, DeviceName: name, Status: "ok", MgmtIP: "10.0.0.1",
		InstalledGeneration: gen, InstalledUsername: "mcadmin",
	}
}

// The only shape the run pushes to: seen, addressable, behind, and previously
// confirmed under this very credential.
func TestEligibleWhenBehindAndAlreadyKnown(t *testing.T) {
	var ok, why = credDeployEligible(reached("sw01", 3), 4)
	if !ok {
		t.Fatalf("a visible device on generation 3 must be deployable to 4, refused: %s", why)
	}
}

// THE RULE THAT KEEPS AN UNATTENDED SWEEP OFF THE RANGES THAT CANNOT BE RECOVERED.
//
// A device meshcraft has never confirmed a secret on is a device the run would be
// guessing at. On the lab CSS326 — no SSH, no Telnet, no serial port — a wrong guess
// is repaired with the front-panel Reset button, on site. That device belongs to the
// per-device gesture, where an operator is present and can supply the current password.
func TestNeverConfirmedDeviceIsNeverPushedTo(t *testing.T) {
	var never = reached("css326", 0)
	never.InstalledGeneration = 0
	var ok, why = credDeployEligible(never, 4)
	if ok {
		t.Fatal("a device with no confirmed installation must not be pushed to by a fleet run")
	}
	if why == "" {
		t.Error("a refusal with no reason is a refusal somebody overrides")
	}
	// Same fact by the other door: a generation was observed but no account name ever
	// was, so nobody knows WHICH account the secret would land on.
	var nameless = reached("css326", 3)
	nameless.InstalledUsername = ""
	ok, _ = credDeployEligible(nameless, 4)
	if ok {
		t.Fatal("a device with no recorded account must not be pushed to: there is no " +
			"account to change the secret of")
	}
}

// A device meshcraft cannot currently see does NOT count as online. "unknown" is not
// optimism, it is the verdict that no proxy able to see this device is connected — and
// the read loops' tolerance for it does not transfer to a write.
func TestInvisibleDeviceIsNotPushedTo(t *testing.T) {
	var status string
	for _, status = range []string{"off", "unknown", ""} {
		var d = reached("sw01", 3)
		d.Status = status
		var ok, _ = credDeployEligible(d, 4)
		if ok {
			t.Errorf("status %q must not be treated as online for a secret push", status)
		}
	}
}

func TestDeviceWithoutAddressIsSkipped(t *testing.T) {
	var d = reached("sw01", 3)
	d.MgmtIP = ""
	var ok, _ = credDeployEligible(d, 4)
	if ok {
		t.Fatal("there is nothing to dial")
	}
}

// What makes a re-run a RESUME instead of a second push at everything. Devices that
// took the secret are recognised from observed state and left alone — and "everything"
// is precisely the set whose push is the risky part.
func TestDeviceAlreadyCarryingTargetIsSkipped(t *testing.T) {
	var ok, why = credDeployEligible(reached("sw01", 4), 4)
	if ok {
		t.Fatal("a device already on the target must not be pushed to again")
	}
	if why == "" {
		t.Error("want a reason")
	}
}

// A device ahead of the target is a run that happened out of order. Pushing the older
// secret would be a downgrade, and it would be recorded as progress.
func TestDeviceAheadIsNeverDowngraded(t *testing.T) {
	var ok, _ = credDeployEligible(reached("sw01", 5), 4)
	if ok {
		t.Fatal("a device on a newer generation must not be pushed backwards")
	}
}

func TestNoTargetDeploysNothing(t *testing.T) {
	var ok, _ = credDeployEligible(reached("sw01", 3), 0)
	if ok {
		t.Fatal("there is no generation to deploy")
	}
}

// Order is the only protection an interrupted run has: whatever it had time for should
// be the devices with the least margin left, i.e. the ones furthest behind. Ties break
// on name so a run is reproducible.
func TestQueueIsFurthestBehindFirst(t *testing.T) {
	var queue, skipped = credDeployQueue(5, []credDeployTarget{
		reached("bravo", 4), reached("alpha", 4), reached("delta", 2), reached("charlie", 3),
	})
	if len(skipped) != 0 {
		t.Fatalf("nothing should be skipped here, got %+v", skipped)
	}
	var want = []string{"delta", "charlie", "alpha", "bravo"}
	var i int
	for i = range want {
		if queue[i].DeviceName != want[i] {
			t.Fatalf("queue = %v, want %v",
				[]credDeployTarget{queue[0], queue[1], queue[2], queue[3]}, want)
		}
	}
}

// Every declined device is REPORTED with its reason. A fleet gesture that silently
// omitted devices would put the operator back in front of the surprise this whole
// mechanism exists to end.
func TestSkippedDevicesAreNamedWithAReason(t *testing.T) {
	var off = reached("sw-off", 3)
	off.Status = "off"
	var queue, skipped = credDeployQueue(4, []credDeployTarget{reached("sw01", 3), off})
	if len(queue) != 1 || queue[0].DeviceName != "sw01" {
		t.Fatalf("queue = %+v, want only sw01", queue)
	}
	if len(skipped) != 1 || skipped[0].DeviceName != "sw-off" || skipped[0].Reason == "" {
		t.Fatalf("the declined device must be named with a reason, got %+v", skipped)
	}
}

// THE NO-RENAME RULE, and the case that motivated it. On the lab CSS326 the credential
// says `mcadmin` while the installed account is `admin`, because SwOS has one nameless
// account it cannot rename. The run keys on the account ON RECORD, and the account it
// entered through — never on what the credential wishes were there.
func TestAccountIsTheOneOnRecordNotTheOneWished(t *testing.T) {
	var account, err = credDeployAccount("admin", "admin")
	if err != nil {
		t.Fatalf("entering as the recorded account must be allowed: %v", err)
	}
	if account != "admin" {
		t.Errorf("account = %q, want the account the device answered to", account)
	}
}

// A device the connector answers on a DIFFERENT account is a half-finished rename, a
// manual change, or not the device meshcraft thinks it is. All three need a human, and
// none of them is fixed by writing a secret.
func TestMismatchedAccountRefusesRatherThanRenames(t *testing.T) {
	var _, err = credDeployAccount("admin", "mcadmin")
	if err == nil {
		t.Fatal("entering as \"admin\" where \"mcadmin\" is on record must REFUSE: " +
			"a fleet run changes a secret, it never creates or renames an account")
	}
	if !contains(err.Error(), "admin") || !contains(err.Error(), "mcadmin") {
		t.Errorf("the refusal must name both accounts, got: %v", err)
	}
}

// Case is the device's business, not ours: a switch does not distinguish "Admin" from
// "admin", and refusing on case alone would strand a device over a spelling.
func TestAccountComparisonIgnoresCase(t *testing.T) {
	var account, err = credDeployAccount("Admin", "admin")
	if err != nil {
		t.Fatalf("case must not decide: %v", err)
	}
	if account != "Admin" {
		t.Errorf("account = %q, want the spelling the device answered to", account)
	}
}

func TestAccountRefusesWhenNothingIsKnown(t *testing.T) {
	var _, err = credDeployAccount("", "mcadmin")
	if err == nil {
		t.Error("a session that does not say who it is must not produce a write")
	}
	_, err = credDeployAccount("admin", "")
	if err == nil {
		t.Error("no account on record means nothing to change the secret of")
	}
}

// A rotation between opening the screen and pressing the button is two operators, or
// one operator and a script. The run must not push a generation nobody asked for.
func TestConfirmRefusesAStaleTarget(t *testing.T) {
	if credDeployConfirm(4, 4) != nil {
		t.Error("the agreed generation must start")
	}
	var err = credDeployConfirm(4, 5)
	if err == nil {
		t.Fatal("a credential that rotated since the screen was read must refuse")
	}
	if !contains(err.Error(), "4") || !contains(err.Error(), "5") {
		t.Errorf("the refusal must name both generations, got: %v", err)
	}
	if credDeployConfirm(0, 4) == nil {
		t.Error("a fleet-wide push does not start on an implicit target")
	}
	if credDeployConfirm(4, 0) == nil {
		t.Error("a credential with no active generation has nothing to deploy")
	}
}

// ONLY AN UNCONFIRMED WRITE HALTS THE RUN. Partial failure is the normal case — a
// switch that is off, a proxy that is down — and a run that stopped on those would be
// a run nobody dares start. A write that was accepted and did not read back is the
// other kind of event: the next unit of the same range will do the same thing, and
// walking the fleet into that state one device at a time is how a site becomes
// unreachable.
func TestOnlyAnUnconfirmedWriteHaltsTheRun(t *testing.T) {
	if !credDeployHalts(credDeployUnconfirmed) {
		t.Error("an unconfirmed write must stop the run")
	}
	var outcome string
	for _, outcome = range []string{
		credDeployInstalled, credDeployRehearsed, credDeploySkipped, credDeployFailed,
	} {
		if credDeployHalts(outcome) {
			t.Errorf("%q must not stop the run: partial failure is the normal case", outcome)
		}
	}
}

// Three refusals in a row are one cause on our side of the wire, not three
// coincidences — and continuing spends the fleet's authentication budget on it. On
// Instant On, whose firmware locks an account out, that budget is not free.
func TestConsecutiveFailuresTripTheRun(t *testing.T) {
	if credDeployTripped(credDeployMaxConsecutiveFailures - 1) {
		t.Error("scattered failures are switches that are off, not a systemic fault")
	}
	if !credDeployTripped(credDeployMaxConsecutiveFailures) {
		t.Error("failures in a row must trip the run")
	}
}

// ONE RUN AT A TIME ACROSS THE CONTROLLER. Every guardrail in the file is stated per
// run; two runs would double all of them while each believed itself well-behaved, and
// two credentials can land on the same switch — a device carries a site credential and
// a technical-tag one, and both go through the same five sessions.
func TestRegistryRefusesASecondConcurrentRun(t *testing.T) {
	var reg credDeployRegistry
	var first = &credDeployRun{CredentialID: "cred-a"}
	var ok, _ = reg.begin(first)
	if !ok {
		t.Fatal("the first run must start")
	}
	var busy string
	ok, busy = reg.begin(&credDeployRun{CredentialID: "cred-b"})
	if ok {
		t.Fatal("a second run must be refused while one is in flight")
	}
	if busy != "cred-a" {
		t.Errorf("the refusal must name the run in flight, got %q", busy)
	}
	reg.finish("cred-a")
	ok, _ = reg.begin(&credDeployRun{CredentialID: "cred-b"})
	if !ok {
		t.Fatal("once the first run is done the next must start")
	}
	// The finished run stays readable: its report is the only trace of what it did.
	if reg.get("cred-a") != first {
		t.Error("a finished run must remain readable for its report")
	}
}

// A run reports what it has done so far, and what is left. `Remaining` is what the
// operator watches; it must never read as finished while devices are still queued.
func TestSnapshotCountsWhatIsLeft(t *testing.T) {
	var run = &credDeployRun{
		CredentialID: "cred-a", Target: 4,
		queued:  []credDeployTarget{reached("a", 3), reached("b", 3)},
		skipped: []credDeploySkip{{DeviceID: "c", Reason: "off"}},
	}
	var st = run.snapshot()
	if st.Remaining != 2 || !st.Running {
		t.Fatalf("a fresh run = %d remaining, running %v; want 2 and true",
			st.Remaining, st.Running)
	}
	run.record(credDeployResult{DeviceID: "a", Outcome: credDeployInstalled})
	st = run.snapshot()
	if st.Remaining != 1 || len(st.Results) != 1 {
		t.Fatalf("after one device = %d remaining, %d results; want 1 and 1",
			st.Remaining, len(st.Results))
	}
	run.end("halted")
	st = run.snapshot()
	if st.Running || st.FinishedAt == nil || st.Stopped != "halted" {
		t.Fatalf("a finished run must say so and why, got %+v", st)
	}
	// The snapshot must be a COPY: a report handed to an HTTP writer while the run
	// keeps going must not be appended to underneath it.
	run.record(credDeployResult{DeviceID: "b", Outcome: credDeployInstalled})
	if len(st.Results) != 1 {
		t.Error("the snapshot must not alias the run's own slice")
	}
}

func contains(haystack, needle string) bool {
	var i int
	for i = 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

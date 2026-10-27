package store

import "testing"

// The promotion gate is the one decision in fleet-wide credential deployment that
// cannot be got backwards, and — like the try order next door in generations_test.go
// — a mistake in it is invisible until a device that stayed behind becomes
// unreachable. By then the vault holds a secret half the fleet does not know, and the
// recovery is a console visit per switch.
//
// So it is pure, and it is pinned here without a database.

// dev builds an observation: a device seen carrying `gen` of the credential.
func dev(id string, gen int) DeviceGeneration {
	return DeviceGeneration{DeviceID: id, DeviceName: id, Generation: gen}
}

// The only shape that promotes: every device in scope confirmed on the target.
func TestPromoteWhenEveryDeviceConfirmed(t *testing.T) {
	var p = PlanDeployment(4, []string{"a", "b", "c"},
		[]DeviceGeneration{dev("a", 4), dev("b", 4), dev("c", 4)})
	var ok, why = p.SafeToPromote()
	if !ok {
		t.Fatalf("a fully confirmed fleet must promote, refused with: %s", why)
	}
	if p.Outstanding() != 0 {
		t.Errorf("outstanding = %d, want 0", p.Outstanding())
	}
}

// The case the whole design exists for: one switch was off. It keeps the previous
// generation, it keeps working, and it BLOCKS the promotion — promoting here would
// move the vault head to a secret this device does not carry.
func TestOneDeviceBehindBlocksPromotion(t *testing.T) {
	var p = PlanDeployment(4, []string{"a", "b"},
		[]DeviceGeneration{dev("a", 4), dev("b", 3)})
	var ok, why = p.SafeToPromote()
	if ok {
		t.Fatal("a device still on generation 3 must block promotion to 4")
	}
	if why == "" {
		t.Error("a refusal with no reason is a refusal somebody overrides")
	}
	if len(p.Behind) != 1 || p.Behind[0].DeviceID != "b" {
		t.Errorf("the lagging device must be NAMED, got %+v", p.Behind)
	}
	if p.Outstanding() != 1 {
		t.Errorf("outstanding = %d, want 1", p.Outstanding())
	}
}

// A device in scope that was never reached is not a device that is up to date. It has
// no name, no error and no date to show for itself, which is exactly why it must be
// listed rather than skipped.
func TestNeverReachedDeviceBlocksPromotion(t *testing.T) {
	var p = PlanDeployment(4, []string{"a", "ghost"}, []DeviceGeneration{dev("a", 4)})
	var ok, _ = p.SafeToPromote()
	if ok {
		t.Fatal("a device never reached must block promotion")
	}
	if len(p.Unreached) != 1 || p.Unreached[0].DeviceID != "ghost" {
		t.Fatalf("the unreached device must appear, got %+v", p.Unreached)
	}
	if len(p.Carrying) != 1 {
		t.Errorf("carrying = %+v, want only \"a\"", p.Carrying)
	}
}

// A row with no observed generation is the same fact arriving by the other door:
// `device_credential_state` exists for the device (a push was attempted and failed)
// but `installed_generation` is null, which scans as 0.
func TestNullInstalledGenerationCountsAsUnreached(t *testing.T) {
	var p = PlanDeployment(4, nil,
		[]DeviceGeneration{dev("a", 4), {DeviceID: "b", LastError: "connection refused"}})
	var ok, _ = p.SafeToPromote()
	if ok {
		t.Fatal("generation 0 is \"never observed\", not \"up to date\"")
	}
	if len(p.Unreached) != 1 || p.Unreached[0].LastError != "connection refused" {
		t.Fatalf("the failure must survive into the outstanding list, got %+v", p.Unreached)
	}
}

// THE GUARD. A caller that narrows its scope to "the three devices I just pushed to"
// must not thereby promote a generation the other, KNOWN devices do not carry.
// Observation can only add to the caller's scope, never be trimmed by it — forgetting
// a device is how a fleet gets stranded, so forgetting one cannot look like success.
func TestObservedHolderOutsideScopeStillBlocks(t *testing.T) {
	var p = PlanDeployment(4, []string{"a"},
		[]DeviceGeneration{dev("a", 4), dev("forgotten", 2)})
	var ok, _ = p.SafeToPromote()
	if ok {
		t.Fatal("a known device on generation 2 must block promotion to 4 even though " +
			"the caller left it out of scope")
	}
	if len(p.Behind) != 1 || p.Behind[0].DeviceID != "forgotten" {
		t.Fatalf("the omitted device must be counted, got %+v", p.Behind)
	}
}

// "We have not looked" must not answer the same as "everyone is up to date". What
// reaches this case is a mistyped credential id, or a scope resolved before the
// devices were adopted.
func TestEmptyPlanNeverPromotes(t *testing.T) {
	var ok, why = PlanDeployment(4, nil, nil).SafeToPromote()
	if ok {
		t.Fatal("an empty plan must not promote: nothing observed is not everything installed")
	}
	if why == "" {
		t.Error("want a reason")
	}
}

// No target is no promotion — the degenerate call, refused before anything else is
// consulted.
func TestNoTargetNeverPromotes(t *testing.T) {
	var ok, _ = PlanDeployment(0, []string{"a"}, []DeviceGeneration{dev("a", 0)}).SafeToPromote()
	if ok {
		t.Fatal("target 0 must not promote")
	}
}

// A device on a NEWER generation is a run that happened out of order. It is not
// stranded by this promotion, so it does not block — but it is reported, because a
// bucket total that does not add up is how an operator stops trusting the screen.
func TestAheadIsReportedButDoesNotBlock(t *testing.T) {
	var p = PlanDeployment(4, []string{"a", "b"},
		[]DeviceGeneration{dev("a", 4), dev("b", 5)})
	var ok, why = p.SafeToPromote()
	if !ok {
		t.Fatalf("a device ahead of the target strands nobody, refused with: %s", why)
	}
	if len(p.Ahead) != 1 || p.Ahead[0].DeviceID != "b" {
		t.Fatalf("the device ahead must be reported, got %+v", p.Ahead)
	}
}

// Scope and observation overlap by construction — the caller lists what it means to
// cover, the store returns what it has seen. A device in both is one device.
func TestDeviceInBothScopeAndObservationCountsOnce(t *testing.T) {
	var p = PlanDeployment(4, []string{"a", "a", "b"},
		[]DeviceGeneration{dev("a", 4), dev("a", 4), dev("b", 4)})
	if len(p.Carrying) != 2 {
		t.Fatalf("carrying = %d devices, want 2 distinct", len(p.Carrying))
	}
	if p.Outstanding() != 0 {
		t.Errorf("outstanding = %d, want 0", p.Outstanding())
	}
}

// Partial failure is the NORMAL case: the plan must stay readable with every bucket
// occupied at once, which is what a real fleet looks like mid-rotation.
func TestPartialFleetIsReadable(t *testing.T) {
	var p = PlanDeployment(4, []string{"a", "b", "c", "d", "never"},
		[]DeviceGeneration{dev("a", 4), dev("b", 4), dev("c", 3), dev("d", 5)})
	if len(p.Carrying) != 2 || len(p.Behind) != 1 || len(p.Ahead) != 1 || len(p.Unreached) != 1 {
		t.Fatalf("buckets = carrying %d, behind %d, ahead %d, unreached %d; "+
			"want 2/1/1/1", len(p.Carrying), len(p.Behind), len(p.Ahead), len(p.Unreached))
	}
	if p.Outstanding() != 2 {
		t.Errorf("outstanding = %d, want 2 (one behind, one never reached)", p.Outstanding())
	}
	var ok, _ = p.SafeToPromote()
	if ok {
		t.Error("must not promote with two devices outstanding")
	}
}

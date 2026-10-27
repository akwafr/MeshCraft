package api

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// arenaRegion is the region ARENA wants, as read on 2026-10-25: name derived
// from the site UUID (ADR-0021), revision 1, every site VLAN but 1 in instance 1.
func arenaRegion() driver.MSTRegion {
	return driver.SiteMSTRegion("mst-7f0d238df180", 1,
		[]int{1, 10, 11, 42, 60, 98, 99, 100})
}

// TestTheThreeARENAStatesEachGetTheirOwnVerdict is the test that matters, and it
// is written from the survey rather than from the code: the site put three
// genuinely different states on one L2 the same night, and a banner that
// collapsed any two of them would say the wrong thing about a production switch.
func TestTheThreeARENAStatesEachGetTheirOwnVerdict(t *testing.T) {
	var want = arenaRegion()

	// 1. A conformant Aruba Instant On. Eight of them read like this.
	var aruba = driver.SpanningTreeState{
		Enabled: true, Mode: "mstp", BridgeID: "32768-5c5b35000001",
		Region: driver.MSTRegion{
			Name: "mst-7f0d238df180", Revision: 1,
			InstanceVLANs: map[int][]int{1: {10, 11, 42, 60, 98, 99, 100}},
		},
	}
	var v = stpRegionViewOf(want, true, &aruba, true)
	if v == nil || v.Verdict != "match" {
		t.Fatalf("conformant Instant On: want match, got %+v", v)
	}
	if len(v.Differs) != 0 {
		t.Fatalf("conformant Instant On must differ on nothing, got %v", v.Differs)
	}

	// 2. core01-arena, Arista EOS: mstp, "Name []", revision 0, no instance.
	// It is NOT "drift" — there is no region to have drifted from, and the
	// sentence an operator needs is "this switch is a region of one".
	var arista = driver.SpanningTreeState{
		Enabled: true, Mode: "mstp", BridgeID: "0-001c73aabbcc",
		Region: driver.MSTRegion{Name: "", Revision: 0},
	}
	v = stpRegionViewOf(want, true, &arista, true)
	if v == nil || v.Verdict != "unregioned" {
		t.Fatalf("core01-arena: want unregioned, got %+v", v)
	}
	if len(v.Differs) != 3 {
		t.Fatalf("core01-arena diverges on all three components, got %v", v.Differs)
	}

	// 3. sw01-arena, FS S3400: RSTP(2004), region name = its own bridge MAC.
	// Two reasons it is not in the region and only one of them is the name, so
	// the verdict has to be the one about the MODE: a bridge not running MSTP
	// advertises no digest at all, and telling the operator "the name differs"
	// would send him to rename a field that nothing reads.
	var fs = driver.SpanningTreeState{
		Enabled: true, Mode: "rstp", BridgeID: "32768-649d9929f706",
		Region: driver.MSTRegion{Name: "649D9929F706", Revision: 0},
	}
	v = stpRegionViewOf(want, true, &fs, false)
	if v == nil || v.Verdict != "not_mstp" {
		t.Fatalf("sw01-arena: want not_mstp, got %+v", v)
	}
	if v.CanJoin {
		t.Fatal("sw01-arena: fs-s3400 refuses FeatMSTRegion, so CanJoin must be false " +
			"— it is what hides \"Conform the spanning tree\", and an action offered where it cannot " +
			"land is worse than no action (ADR-0022)")
	}
}

// TestTheARENASplitIsDrift, not "unregioned": the seven switches on
// `site-exec` DO carry a region, and that is the whole difference. An
// operator reading "no region" about them would look for a missing
// configuration instead of a rename.
func TestTheARENASplitIsDrift(t *testing.T) {
	var want = arenaRegion()
	var evicted = driver.SpanningTreeState{
		Enabled: true, Mode: "mstp", BridgeID: "32768-5c5b35000002",
		Region: driver.MSTRegion{
			Name: "site-exec", Revision: 1,
			InstanceVLANs: map[int][]int{1: {42, 98, 99, 100}},
		},
	}
	var v = stpRegionViewOf(want, true, &evicted, true)
	if v == nil || v.Verdict != "drift" {
		t.Fatalf("want drift, got %+v", v)
	}
	// Name AND table, not one or the other: the site was renamed and VLANs 10,
	// 11 and 60 were added, and each of the two alone splits the region. A
	// banner naming only one would under-report the repair.
	var got = strings.Join(v.Differs, ",")
	if got != "name,instances" {
		t.Fatalf("differs = %q, want the name and the table (ADR-0021's two causes)", got)
	}
	if v.HaveName != "site-exec" || v.WantName != "mst-7f0d238df180" {
		t.Fatalf("both sides must be carried verbatim, got want=%q have=%q",
			v.WantName, v.HaveName)
	}
}

// TestInstanceZeroNeverDecidesAConformityVerdict guards the normalisation that
// would otherwise paint the entire fleet orange, permanently.
//
// EOS prints "Instance 0  1-4094" and so does the FS. mdpv1.MSTRegion
// documents the CIST as implicit and never listed, and arista-eos's parser drops
// it — but a reader that did not, or a future platform that reports it, must not
// be able to turn a conformant switch into a drifted one.
func TestInstanceZeroNeverDecidesAConformityVerdict(t *testing.T) {
	var want = arenaRegion()
	var withCIST = driver.SpanningTreeState{
		Enabled: true, Mode: "mstp", BridgeID: "32768-5c5b35000003",
		Region: driver.MSTRegion{
			Name: "mst-7f0d238df180", Revision: 1,
			InstanceVLANs: map[int][]int{
				0: {1, 2, 3, 4094},
				1: {100, 99, 98, 60, 42, 11, 10}, // and in a different ORDER
			},
		},
	}
	var v = stpRegionViewOf(want, true, &withCIST, true)
	if v == nil || v.Verdict != "match" {
		t.Fatalf("instance 0 and VLAN order must not make a region differ, got %+v", v)
	}
	var e stpMSTEntry
	for _, e = range v.HaveInstances {
		if e.Instance == 0 {
			t.Fatal("instance 0 must not reach the rendered table either: the one use " +
				"of these columns is a human comparing them line by line")
		}
	}
}

// TestNothingToSayIsNotConformant pins the three silences, because each of them
// is a case where a green verdict would be a lie and an orange one would be
// noise.
func TestNothingToSayIsNotConformant(t *testing.T) {
	var want = arenaRegion()
	var drifted = driver.SpanningTreeState{
		Enabled: true, Mode: "mstp", BridgeID: "32768-5c5b35000004",
		Region: driver.MSTRegion{Name: "site-exec", Revision: 1},
	}
	// The site does not prefer MSTP: a region is written nowhere and read by
	// nothing, so there is no divergence to report.
	if stpRegionViewOf(want, false, &drifted, true) != nil {
		t.Fatal("a site that does not prefer MSTP must produce no verdict")
	}
	// The site has no region to want.
	if stpRegionViewOf(driver.MSTRegion{}, true, &drifted, true) != nil {
		t.Fatal("a site with no configured region must produce no verdict")
	}
	// Never read. The absence of a banner here is NOT proof of conformity, and
	// the frontend type says so in as many words.
	if stpRegionViewOf(want, true, nil, true) != nil {
		t.Fatal("a device with no spanning-tree observation must produce no verdict")
	}
}

// TestAFactoryRegionNameNeedsTheBridgeAddressToBeCalledOne is the honesty
// guard on the second normalisation.
//
// `649D9929F706` is a perfectly legal 32-character region name. Calling it "no
// region" is right only because it is the BRIDGE'S OWN ADDRESS, which is the
// factory default on every range surveyed. Without a bridge address read, the
// name must be compared literally — telling an operator his region is unnamed
// because he named it in hexadecimal is worse than being less specific.
func TestAFactoryRegionNameNeedsTheBridgeAddressToBeCalledOne(t *testing.T) {
	if !stpRegionNameIsFactory("", "32768-649d9929f706") {
		t.Fatal("an empty name is no region: EOS then derives it from the bridge " +
			"address, so every unnamed switch is a region of its own")
	}
	if !stpRegionNameIsFactory("649D9929F706", "32768-649d9929f706") {
		t.Fatal("the bridge's own MAC as a region name is the factory default")
	}
	if !stpRegionNameIsFactory("649d.9929.f706", "649D:99:29:F7:06") {
		t.Fatal("separators must not decide: the same address is printed three ways " +
			"across these CLIs")
	}
	if stpRegionNameIsFactory("649D9929F706", "32768-aabbccddeeff") {
		t.Fatal("twelve hex characters that are NOT this bridge's address are a name")
	}
	if stpRegionNameIsFactory("649D9929F706", "") {
		t.Fatal("with no bridge address read, the name must be taken literally " +
			"rather than guessed to be a factory default")
	}
	if stpRegionNameIsFactory("mst-7f0d238df180", "32768-649d9929f706") {
		t.Fatal("a meshcraft-derived name is a name")
	}
}

// TestTheAutoPathOnlyConfirmsTheIdentical is ADR-0023 itself, and every branch
// here is a measured outage rather than a hypothesis. The automatic path is
// reached from runApply — "apply the ports of this device" — so anything it is
// allowed to write is something a VLAN change can trigger.
func TestTheAutoPathOnlyConfirmsTheIdentical(t *testing.T) {
	var want = arenaRegion()
	var conformant = driver.SpanningTreeState{
		Enabled: true, Mode: "mstp", BridgeID: "32768-5c5b35000001",
		Region: driver.MSTRegion{
			Name: "mst-7f0d238df180", Revision: 1,
			InstanceVLANs: map[int][]int{1: {10, 11, 42, 60, 98, 99, 100}},
		},
	}

	// ALLOWED, and this is the half of the decision that keeps per-port BPDU
	// guard arriving: the mode re-sent is the mode running, the region re-sent
	// is the region carried, the digest does not move, no MST boundary shifts.
	var refusal = stpAutoWriteRefusal("mstp", false, want, true, conformant, "")
	if refusal != "" {
		t.Fatalf("a device already carrying the site's identity must still take an "+
			"automatic apply — otherwise per-port STP intents stop reaching a healthy "+
			"fleet. Got: %s", refusal)
	}

	// REFUSED: the region differs. This is the ARENA rename, and the write that
	// used to ride a port apply.
	var evicted = conformant
	evicted.Region = driver.MSTRegion{Name: "site-exec", Revision: 1,
		InstanceVLANs: map[int][]int{1: {42, 98, 99, 100}}}
	refusal = stpAutoWriteRefusal("mstp", false, want, true, evicted, "")
	if refusal == "" {
		t.Fatal("a device in another MST region must NOT be written automatically: " +
			"re-emitting the site's triplet renames it and displaces an MST boundary")
	}
	if !strings.Contains(refusal, "Conform the spanning tree") {
		t.Fatalf("the refusal must name the gesture that replaces it, got: %s", refusal)
	}

	// REFUSED: the mode differs. Measured on fs-s3400 on 2026-08-11 —
	// "spanning-tree mode <m>" closes the SSH session before the CLI answers.
	var rstp = conformant
	rstp.Mode = "rstp"
	refusal = stpAutoWriteRefusal("mstp", false, want, true, rstp, "")
	if refusal == "" {
		t.Fatal("a mode change must never ride another gesture")
	}

	// REFUSED: spanning tree is off and the site wants it on. Turning it on
	// makes the bridge start advertising BPDUs and re-elects a root.
	var off = conformant
	off.Enabled = false
	refusal = stpAutoWriteRefusal("mstp", false, want, true, off, "")
	if refusal == "" {
		t.Fatal("enabling spanning tree is a topology event, not a setting")
	}

	// REFUSED: the read did not land, so nothing can be proven. The gate IS a
	// proof, and a proof needs a current reading.
	refusal = stpAutoWriteRefusal("mstp", false, want, true,
		driver.SpanningTreeState{}, "spanning tree not read back: timeout")
	if refusal == "" || !strings.Contains(refusal, "timeout") {
		t.Fatalf("an unreadable tree must refuse the write and carry the reader's own "+
			"sentence, got: %s", refusal)
	}

	// ALLOWED under RSTP even though the device carries no region: under a mode
	// that advertises no MST digest, a region comparison would freeze a device
	// over a field nothing reads. fs-s3400 resolves to rstp and lives here.
	var plainRSTP = driver.SpanningTreeState{
		Enabled: true, Mode: "rstp", BridgeID: "32768-649d9929f706",
		Region: driver.MSTRegion{Name: "649D9929F706"},
	}
	refusal = stpAutoWriteRefusal("rstp", false, want, true, plainRSTP, "")
	if refusal != "" {
		t.Fatalf("a device resolved to rstp must not be gated on an MST region, got: %s",
			refusal)
	}
}

// TestWantsMSTReadsTheWholeListAndNotItsHead: the gate on the verdict is "does
// this site mean MSTP at all", not "is mstp its first choice". A site that lists
// mstp second still means it for every platform whose caps rule the first choice
// out, and a gate reasoning about ORDER would have to re-run ResolveSTPMode per
// device — which is the apply path's job, not a display gate's.
func TestWantsMSTReadsTheWholeListAndNotItsHead(t *testing.T) {
	if !stpPreferenceWantsMST([]byte(`{"stp_mode_preference":["rstp","mstp"]}`)) {
		t.Fatal("mstp anywhere in the list is an intention of the site")
	}
	if stpPreferenceWantsMST([]byte(`{"stp_mode_preference":["rstp","stp"]}`)) {
		t.Fatal("a site that never lists mstp has no region to be out of")
	}
	// EXPLICITLY EMPTY is the per-site off switch and means no mstp either; an
	// ABSENT key falls back to the product default, which is rstp/stp/none.
	if stpPreferenceWantsMST([]byte(`{"stp_mode_preference":[]}`)) {
		t.Fatal("an emptied preference list wants nothing, mstp included")
	}
	if stpPreferenceWantsMST(nil) {
		t.Fatal("the product default (rstp, stp, none) does not contain mstp")
	}
}

// TestTurningSpanningTreeOffIsAlsoAGesture: "none" in the site preference is
// as coarse as any other mode, and it was the branch easiest to forget — the
// region comparison does not apply, so an early return could have let it
// through.
func TestTurningSpanningTreeOffIsAlsoAGesture(t *testing.T) {
	var want = arenaRegion()
	var running = driver.SpanningTreeState{Enabled: true, Mode: "rstp"}
	if stpAutoWriteRefusal("rstp", true, want, true, running, "") == "" {
		t.Fatal("disabling spanning tree on a bridge that runs it reconverges the " +
			"tree; it must wait for the gesture")
	}
	var already = driver.SpanningTreeState{Enabled: false}
	if stpAutoWriteRefusal("rstp", true, want, true, already, "") != "" {
		t.Fatal("a tree already off is the identical, and re-confirming it changes " +
			"nothing")
	}
}

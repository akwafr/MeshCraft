package unifi

// LIVE validation of the gesture code against a real UniFi OS controller,
// skipped unless the environment supplies one. No credential here, ever — the
// URL and token come from the environment like every other live test.
//
//	UOS_URL=https://host:port UOS_TOKEN=... go test ./internal/unifi -run LiveGesture -v
//
// # WHY NOTHING HERE CHANGES A REAL BOX, AND WHAT THAT COSTS
//
// Every assertion below exercises the complete Go path — the request building,
// the API key header, the JSON body, the reply decoding, the error mapping —
// against the real controller, while addressing a MAC that exists on no
// network (`02:…`, locally administered). The controller validates the command
// and answers `api.err.UnknownDevice`, so the round trip is real and its effect
// is nil.
//
// That is a deliberate limit and it leaves one thing unproven: the HAPPY path
// of `adopt` and `reprovision` against a box that does exist. It is covered
// against a double in gestures_test.go and NOT here, because the only
// controller available serves a venue with people on it. A reprovision of an
// adopted box is harmless in principle; the forget that is NOT harmless was
// already performed by accident on this very site while discovering that
// `DELETE` on the integration device resource has no inert form (ADR-0025,
// Context). Having cost that network one outage, spending a second write on
// it to confirm something a double already confirms is not a trade worth
// making. To close the gap deliberately, point this at a lab controller.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// envSiteRef lets a developer pin which site the live gestures aim at, so a
// controller holding several venues does not get picked at random.
func envSiteRef() string { return os.Getenv("UOS_SITE_REF") }

// noSuchMAC is locally administered and belongs to nothing. Every gesture below
// is aimed at it so the controller has nothing to act upon.
const noSuchMAC = "02:00:00:00:00:01"

// The token's rights must be READABLE, which is the whole of ADR-0025 §7: a
// read-only key has to be known before it is needed rather than at the worst
// moment.
func TestLiveGestureRightsAreReadable(t *testing.T) {
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var rights, err = c.Rights(ctx)
	if err != nil {
		t.Fatalf("Rights: %v", err)
	}
	if rights.Writes != WritesGranted && rights.Writes != WritesUnproven {
		t.Fatalf("Writes = %q, want %q or %q", rights.Writes, WritesGranted, WritesUnproven)
	}
	if rights.Admin == "" {
		t.Error("the controller named no administrator for this token")
	}
	t.Logf("token belongs to %s; writes: %s (scoped=%v)",
		rights.Admin, rights.Writes, rights.Scoped)
}

// The pending list must be reachable and must contain no adopted box. Pure
// read; it is what the adopt screen is built on.
func TestLiveGestureAdoptableList(t *testing.T) {
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sites, err = c.ListSites(ctx)
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(sites) == 0 {
		t.Skip("the controller has no site")
	}
	var ref string
	var s Site
	for _, s = range sites {
		if s.InternalReference != "" {
			ref = s.InternalReference
			break
		}
	}
	if ref == "" {
		t.Skip("no site carries a short reference")
	}
	var list []Candidate
	list, err = c.ListAdoptable(ctx, ref)
	if err != nil {
		t.Fatalf("ListAdoptable(%s): %v", ref, err)
	}
	var cand Candidate
	for _, cand = range list {
		if cand.MAC == "" {
			t.Error("a candidate with no MAC cannot be adopted")
		}
	}
	t.Logf("%d box(es) awaiting adoption", len(list))
}

// `adopt` must reach the controller and be REFUSED for a box it does not know.
// This proves the command name, the endpoint, the header and the body against
// the real thing — the parts a double cannot vouch for — with no effect.
func TestLiveGestureAdoptRefusesAnUnknownBox(t *testing.T) {
	var c, siteID, ref = liveSiteForGestures(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	var err = c.Do(ctx, Adopt, Target{SiteID: siteID, SiteRef: ref, MAC: noSuchMAC})
	if err == nil {
		t.Fatal("the controller claimed to adopt a box that does not exist")
	}
	if !errors.Is(err, ErrGestureRefused) {
		t.Fatalf("Do(Adopt) on an unknown box = %v, want ErrGestureRefused", err)
	}
	// The real controller's own vocabulary, which is what the operator sees.
	if !strings.Contains(err.Error(), "UnknownDevice") &&
		!strings.Contains(err.Error(), "CanNotAdopt") {
		t.Errorf("the controller's reason did not survive: %v", err)
	}
	t.Logf("refused as expected: %v", err)
}

// Same for `force-provision`: the command is recognised, and a box the
// controller has never heard of is a refusal rather than a silent `rc:ok`.
//
// This is the measurement that lets Reprovision have no confirming read: the
// gesture's own refusal is real, so an accepted call on a box we just read is
// as much as this API can be made to promise (see Reprovision's comment).
func TestLiveGestureReprovisionRefusesAnUnknownBox(t *testing.T) {
	var c, siteID, ref = liveSiteForGestures(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	var err = c.Do(ctx, Reprovision, Target{SiteID: siteID, SiteRef: ref, MAC: noSuchMAC})
	if err == nil {
		t.Fatal("force-provision answered ok for a box that does not exist")
	}
	if !errors.Is(err, ErrGestureRefused) {
		t.Fatalf("Do(Reprovision) on an unknown box = %v, want ErrGestureRefused", err)
	}
	t.Logf("refused as expected: %v", err)
}

// THE GATE, against the real controller. A forget whose confirmation name does
// not match must stop BEFORE the irreversible call — and the proof that it
// stopped is that the box is still there afterwards.
//
// This is the only live test that aims at a real, adopted box, and it is safe
// precisely because the name is deliberately wrong: if the gate were broken,
// this test would destroy something, so it asserts the box's survival rather
// than only the error.
func TestLiveGestureForgetStopsOnAWrongName(t *testing.T) {
	var c, siteID, ref = liveSiteForGestures(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	var boxes, err = c.listDevicesShallow(ctx, siteID)
	if err != nil {
		t.Fatalf("listing the site's boxes: %v", err)
	}
	if len(boxes) == 0 {
		t.Skip("the site has no adopted box to aim the gate at")
	}
	var victim = boxes[0]

	// Kept in its own variable: `err` is reused by the re-list below, and a log
	// line that printed the reassigned one would claim the gate returned nil.
	var refusal = c.Do(ctx, Forget, Target{
		SiteID: siteID, SiteRef: ref,
		DeviceID: victim.ID, MAC: victim.MACAddress,
		// A name no box can have. The gate must refuse on this alone.
		ConfirmName: "meshcraft-gate-test-" + victim.ID,
	})
	if !errors.Is(refusal, ErrConfirmMismatch) {
		t.Fatalf("Do(Forget) with a wrong name = %v, want ErrConfirmMismatch", refusal)
	}

	// The assertion that actually matters.
	var after []Device
	after, err = c.listDevicesShallow(ctx, siteID)
	if err != nil {
		t.Fatalf("re-listing the site's boxes: %v", err)
	}
	var found bool
	var d Device
	for _, d = range after {
		if d.ID == victim.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("THE GATE LET A FORGET THROUGH: %q (%s) is gone from the site",
			victim.Name, victim.MACAddress)
	}
	if len(after) != len(boxes) {
		t.Fatalf("the site had %d boxes and now has %d", len(boxes), len(after))
	}
	t.Logf("gate held: %q still on the site; refusal was %v", victim.Name, refusal)
}

// liveSiteForGestures picks a site that carries both identifiers a gesture
// needs. UOS_SITE_REF pins one; otherwise the first usable site wins.
func liveSiteForGestures(t *testing.T) (*Client, string, string) {
	t.Helper()
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var sites, err = c.ListSites(ctx)
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	var want = strings.TrimSpace(envSiteRef())
	var s Site
	for _, s = range sites {
		if s.ID == "" || s.InternalReference == "" {
			continue
		}
		if want != "" && s.InternalReference != want {
			continue
		}
		return c, s.ID, s.InternalReference
	}
	t.Skip("no site with both an id and a short reference")
	return nil, "", ""
}

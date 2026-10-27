package unifi

// A LIVE read of a real UniFi OS controller, skipped unless the environment
// supplies one. There is no credential in this file and there must never be:
// the controller's URL and token are a developer's own, they come from the
// environment, and the test skips silently when they are absent — which is the
// case in CI and in every container `make test-go` runs.
//
//	UOS_URL=https://host:port UOS_TOKEN=... go test ./internal/unifi -run Live -v
//
// What it asserts is the shape of the answers, not their contents: a controller
// has some sites, a paired site resolves by its stable id, a box carries a MAC
// and a class. Asserting on a particular access point's name would be a test
// about somebody's venue, not about this code.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func liveClient(t *testing.T) *Client {
	t.Helper()
	var base = os.Getenv("UOS_URL")
	var token = os.Getenv("UOS_TOKEN")
	if base == "" || token == "" {
		t.Skip("UOS_URL / UOS_TOKEN not set: skipping the live UniFi read")
	}
	// Self-signed certificates are the norm on-premise, and this test exists to
	// talk to a real controller rather than to audit its PKI.
	var c, err = New(base, token, WithInsecureTLS(true), WithTimeout(20*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestLiveVersionAndSites(t *testing.T) {
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var version, err = c.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version == "" {
		t.Fatal("Version returned an empty string: the integration API answered without a version")
	}
	t.Logf("UniFi Network application version %s", version)

	var sites []Site
	sites, err = c.ListSites(ctx)
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	if len(sites) == 0 {
		t.Fatal("ListSites returned nothing: a controller always has at least its default site")
	}
	var s Site
	for _, s = range sites {
		// Both identifiers must be present, because the pairing needs the
		// stable id and the legacy SSID endpoint needs the short reference.
		// A site offering only one of them would break one half of the import.
		if s.ID == "" {
			t.Errorf("site %q has no id", s.Name)
		}
		if s.InternalReference == "" {
			t.Errorf("site %q (%s) has no internalReference: the SSID read needs it", s.Name, s.ID)
		}
	}
	t.Logf("%d site(s) listed", len(sites))
}

// FindSite must distinguish "the controller answered and this site is not in
// its list" from any transport failure — only the former orphans a pairing
// (ADR-0015 §7), and conflating them would erase somebody's correlation work
// on a ten-minute outage.
func TestLiveFindSiteReportsGoneForAnUnknownID(t *testing.T) {
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var _, err = c.FindSite(ctx, "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, ErrRemoteSiteGone) {
		t.Fatalf("FindSite on an unknown id: got %v, want ErrRemoteSiteGone", err)
	}
}

// The full import path, against whichever site the environment names. Set
// UOS_SITE to a site id, or leave it unset and the first site with boxes wins.
func TestLiveImportShape(t *testing.T) {
	var c = liveClient(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var sites, err = c.ListSites(ctx)
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}

	var want = os.Getenv("UOS_SITE")
	var chosen Site
	var boxes []DeviceDetail
	var s Site
	for _, s = range sites {
		if want != "" && s.ID != want {
			continue
		}
		var got []DeviceDetail
		got, err = c.ListDevices(ctx, s.ID)
		if err != nil {
			t.Fatalf("ListDevices(%s): %v", s.Name, err)
		}
		if len(got) > 0 {
			chosen, boxes = s, got
			break
		}
	}
	if len(boxes) == 0 {
		t.Skip("no site with adopted boxes on this controller: nothing to assert about an import")
	}
	t.Logf("site %q (%s): %d box(es)", chosen.Name, chosen.ID, len(boxes))

	var sawRadio bool
	var b DeviceDetail
	for _, b = range boxes {
		if b.ID == "" {
			t.Error("a box came back with no id: the import key would be empty")
		}
		// The MAC is the only bridge to the switched world (ADR-0016 §3):
		// without it, "which port is this access point on" is unanswerable.
		if b.MACAddress == "" {
			t.Errorf("box %q has no MAC: it could never be correlated to a switch port", b.Name)
		}
		var class = ClassOf(b.Features)
		if class == "other" {
			t.Logf("box %q (%s) classed `other`: features = %s", b.Name, b.Model, string(b.Features))
		}
		if class == "access_point" && len(b.Decoded.Radios) == 0 {
			t.Errorf("access point %q came back with no radio: the detail call did not decode", b.Name)
		}
		var ra DeviceRadio
		for _, ra = range b.Decoded.Radios {
			sawRadio = true
			// A radio with no band is a radio nothing can be said about, and
			// the Wi-Fi screen groups by band.
			if ra.FrequencyGHz == 0 {
				t.Errorf("box %q: a radio reported no frequency", b.Name)
			}
		}
	}
	if !sawRadio {
		t.Log("no radio seen: this site has no access point")
	}

	// SSIDs come from the LEGACY endpoint, outside any published contract. Its
	// failure must degrade the import, never fail it — so this is a log, not an
	// assertion.
	var ssids []SSID
	ssids, err = c.ListSSIDs(ctx, chosen.InternalReference)
	if err != nil {
		t.Logf("ListSSIDs degraded (expected if UniFi moved wlanconf): %v", err)
		return
	}
	var names []string
	var wl SSID
	for _, wl = range ssids {
		if wl.Name == "" {
			t.Error("an SSID came back with no name")
		}
		names = append(names, wl.Name)
	}
	t.Logf("%d SSID(s): %s", len(ssids), strings.Join(names, ", "))
}

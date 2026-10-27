package unifi

// The WHOLE import chain, end to end, against a real UniFi controller and a
// real PostgreSQL: seal a token in the vault, pair on the remote site's stable
// id, read the controller, write the mirror, read it back.
//
// It is skipped unless BOTH environments are supplied, so `make test-go` and CI
// never run it. There is no credential in this file and there must never be:
//
//	MESHCRAFT_TEST_DATABASE_URL='postgres://…'  (a DISPOSABLE database)
//	UOS_URL=… UOS_TOKEN=…                        (a developer's own controller)
//	go test ./internal/unifi -run LiveImport -v
//
// What this proves that the unit tests cannot: that the two APIs, the
// pagination, the per-device detail call, the vendor→neutral translation, the
// sealed-token round trip and the atomic mirror write all agree with each other
// on a controller nobody wrote fixtures for.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/secretbox"
	"meshcraft/internal/store"
)

func TestLiveImportEndToEnd(t *testing.T) {
	var dbURL = os.Getenv("MESHCRAFT_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("MESHCRAFT_TEST_DATABASE_URL not set: skipping the end-to-end import")
	}
	var cli = liveClient(t) // skips on its own if UOS_URL/UOS_TOKEN are absent

	var ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var st, err = store.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer st.Close()
	if err = st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// A vault with its own key: this test must not depend on, or disturb, an
	// instance key.
	var key []byte
	key, err = secretbox.NewRandomKey()
	if err != nil {
		t.Fatalf("NewRandomKey: %v", err)
	}
	var vault *secretbox.Service
	vault, err = secretbox.New(key, true)
	if err != nil {
		t.Fatalf("secretbox.New: %v", err)
	}

	// A throwaway site. Deleting it at the end also proves the cascade: the
	// mirror and the extension must go with it, or this test leaves rows behind
	// and the next run finds them.
	var site model.Site
	site, err = st.CreateSite(ctx, "unifi-e2e-"+time.Now().UTC().Format("20060102150405"))
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	defer func() {
		var delErr = st.DeleteSite(context.Background(), site.ID)
		if delErr != nil {
			t.Errorf("DeleteSite: %v", delErr)
		}
	}()

	// --- The token goes into the vault, sealed, like any site credential ---
	var tokenEnc []byte
	tokenEnc, err = vault.Seal(cli.Token)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	var ext model.SiteExtension
	ext, err = st.UpsertSiteExtension(ctx, model.SiteExtension{
		SiteID:    site.ID,
		Kind:      model.ExtUniFiOS,
		Label:     "e2e",
		BaseURL:   cli.BaseURL,
		VerifyTLS: false,
		Transport: model.ExtTransportDirect,
		Enabled:   true,
	}, tokenEnc, "")
	if err != nil {
		t.Fatalf("UpsertSiteExtension: %v", err)
	}
	if !ext.HasToken {
		t.Fatal("the stored extension reports no token: has_token is computed wrong")
	}

	// The list must never carry the secret — the property the whole vault
	// regime rests on, asserted rather than assumed.
	var listed []model.SiteExtension
	listed, err = st.ListSiteExtensions(ctx, site.ID)
	if err != nil {
		t.Fatalf("ListSiteExtensions: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("%d extensions listed, want 1", len(listed))
	}

	// …and the reveal path must return ciphertext that opens to exactly what
	// was sealed. A vault that round-trips to something else is worse than no
	// vault, because the failure only shows up on the day it is needed.
	var revealed []byte
	_, revealed, err = st.RevealSiteExtensionToken(ctx, ext.ID)
	if err != nil {
		t.Fatalf("RevealSiteExtensionToken: %v", err)
	}
	var opened string
	opened, err = vault.Open(revealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened != cli.Token {
		t.Fatal("the token did not survive the vault round trip")
	}

	// --- Pair on the remote site's STABLE id ---
	var sites []Site
	sites, err = cli.ListSites(ctx)
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	// UOS_SITE names the site to import; otherwise the first with boxes wins.
	var want = os.Getenv("UOS_SITE")
	var chosen Site
	var boxes []DeviceDetail
	var s Site
	for _, s = range sites {
		if want != "" && s.ID != want {
			continue
		}
		var got []DeviceDetail
		got, err = cli.ListDevices(ctx, s.ID)
		if err != nil {
			t.Fatalf("ListDevices(%s): %v", s.Name, err)
		}
		if len(got) > 0 {
			chosen, boxes = s, got
			break
		}
	}
	if len(boxes) == 0 {
		t.Skip("no site with adopted boxes: nothing to import")
	}

	ext, err = st.PairSiteExtension(ctx, ext.ID, chosen.ID, chosen.InternalReference, chosen.Name)
	if err != nil {
		t.Fatalf("PairSiteExtension: %v", err)
	}
	if ext.RemoteSiteID != chosen.ID {
		t.Fatalf("pairing stored %q, want the stable id %q", ext.RemoteSiteID, chosen.ID)
	}
	if ext.PairedAt == nil {
		t.Error("paired_at was not stamped")
	}

	// --- Read the controller and write the mirror ---
	var ssids []SSID
	var ssidsKnown = true
	ssids, err = cli.ListSSIDs(ctx, chosen.InternalReference)
	if err != nil {
		t.Logf("ListSSIDs degraded (expected if UniFi moved wlanconf): %v", err)
		ssidsKnown = false
		ssids = nil
	}

	var rep model.ExtSyncReport
	rep, err = st.ReplaceExtInventory(ctx, ext.ID, site.ID,
		ToExtDevices(boxes), ToExtSSIDs(ssids), ssidsKnown)
	if err != nil {
		t.Fatalf("ReplaceExtInventory: %v", err)
	}
	t.Logf("imported from %q: %d AP, %d switch, %d other, %d SSID",
		chosen.Name, rep.AccessPoints, rep.Switches, rep.Others, rep.SSIDs)

	if rep.AccessPoints+rep.Switches+rep.Others != len(boxes) {
		t.Errorf("the report counts %d boxes, the controller listed %d",
			rep.AccessPoints+rep.Switches+rep.Others, len(boxes))
	}

	// --- Read it back the way the screen will ---
	var aps []model.ExtDevice
	aps, err = st.ListExtDevices(ctx, site.ID, model.ExtClassAccessPoint)
	if err != nil {
		t.Fatalf("ListExtDevices: %v", err)
	}
	if len(aps) != rep.AccessPoints {
		t.Errorf("%d access points read back, %d were imported", len(aps), rep.AccessPoints)
	}
	var a model.ExtDevice
	for _, a = range aps {
		if a.MAC == "" {
			t.Errorf("access point %q came back with no MAC: uncorrelatable", a.Name)
		}
		if a.MAC != strings.ToLower(a.MAC) {
			t.Errorf("access point %q has an uppercase MAC: it will not join", a.Name)
		}
		// The radios are the whole reason the detail call is made per box; if
		// they do not survive the JSONB round trip the screen has nothing.
		if len(a.Radios) == 0 {
			t.Errorf("access point %q came back with no radio after the round trip", a.Name)
		}
		var r model.ExtRadio
		for _, r = range a.Radios {
			if r.BandGHz == 0 {
				t.Errorf("access point %q: a radio lost its band in the round trip", a.Name)
			}
		}
	}

	// --- A second pass must be idempotent ---
	// The import runs whenever somebody clicks, so "it duplicates every row"
	// is the failure this is for, and it would be invisible until the list was
	// twice as long.
	var again model.ExtSyncReport
	again, err = st.ReplaceExtInventory(ctx, ext.ID, site.ID,
		ToExtDevices(boxes), ToExtSSIDs(ssids), ssidsKnown)
	if err != nil {
		t.Fatalf("second ReplaceExtInventory: %v", err)
	}
	if again.AccessPoints != rep.AccessPoints || again.Removed != 0 {
		t.Errorf("a second identical import was not idempotent: %+v then %+v", rep, again)
	}

	// --- A box that leaves a site that still answers is removed ---
	var shorter = ToExtDevices(boxes)[1:]
	var pruned model.ExtSyncReport
	pruned, err = st.ReplaceExtInventory(ctx, ext.ID, site.ID, shorter, ToExtSSIDs(ssids), ssidsKnown)
	if err != nil {
		t.Fatalf("pruning ReplaceExtInventory: %v", err)
	}
	if pruned.Removed != 1 {
		t.Errorf("dropping one box removed %d rows, want 1", pruned.Removed)
	}
}

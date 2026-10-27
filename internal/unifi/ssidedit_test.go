package unifi

// The SSID editor, against the same double that reproduces the real
// controller's awkward behaviour (ADR-0029).
//
// Four of those behaviours are measured, not imagined, and each test below
// exists because of one of them:
//
//  1. `GET /rest/wlanconf/{unknown-id}` answers `rc:ok` with `data: []` — the
//     fail-open reaches the READS, so a confirming read must require content.
//  2. a PUT that stores the body it is given will blank every key the body
//     omits, which is why the client sends the whole object.
//  3. `rc:ok` is not evidence, so an accepted-and-ineffective write has to come
//     back unconfirmed.
//  4. `PUT /rest/wlanconf/{id}` is UNVERIFIED on a real controller — probing it
//     is a write, and the day this was built a live venue was on the air. The
//     405 case is therefore only exercisable here, and it is.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func editCtx() context.Context { return context.Background() }

// The happy path, and the two things it must prove at once: the field lands,
// and the sixty keys the edit never mentioned come back unharmed.
func TestEditSSIDWritesTheWholeObject(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Hidden: boolp(false)},
		Want: SSIDSettings{Hidden: boolp(true)},
	}))
	if err != nil {
		t.Fatalf("Do(EditSSID) = %v, want nil", err)
	}
	if f.wlanPuts != 1 {
		t.Fatalf("%d PUTs, want exactly 1", f.wlanPuts)
	}
	if string(f.wlan["hide_ssid"]) != "true" {
		t.Errorf("hide_ssid is %s, want true", f.wlan["hide_ssid"])
	}
	// THE REASON THE WHOLE OBJECT IS SENT. A partial body on this handler
	// blanks what it omits, and the key most expensive to blank is the one
	// nobody was editing.
	var pass string
	if json.Unmarshal(f.wlan["x_passphrase"], &pass) != nil || pass != testSSIDPass {
		t.Error("the passphrase did not survive an edit that never mentioned it")
	}
	if string(f.wlan["x_iapp_key"]) == "" {
		t.Error("x_iapp_key was dropped: the PUT body was not the whole object")
	}
	if string(f.wlan["name"]) != `"`+testSSIDName+`"` {
		t.Errorf("the name moved to %s without being asked to", f.wlan["name"])
	}
}

// THE PRECONDITION. A field whose live value is not the one the operator was
// shown refuses the WHOLE edit, and refuses it before anything is sent
// (ADR-0029 §6).
func TestEditSSIDRefusesWhenTheValueMoved(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	// Somebody disabled the service set in the UniFi interface while the form
	// was open. The operator's form still believes it is enabled.
	f.wlan["enabled"] = json.RawMessage(`false`)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Broadcasting: boolp(true), Hidden: boolp(false)},
		Want: SSIDSettings{Broadcasting: boolp(true), Hidden: boolp(true)},
	}))
	if !errors.Is(err, ErrSSIDMoved) {
		t.Fatalf("Do = %v, want ErrSSIDMoved", err)
	}
	if f.wlanPuts != 0 {
		t.Fatalf("%d PUTs on a refused precondition, want 0: nothing may be sent", f.wlanPuts)
	}
	// The OTHER field of the same edit must not have landed either: the edit is
	// one decision, not twelve.
	if string(f.wlan["hide_ssid"]) != "false" {
		t.Error("a field of the refused edit was applied anyway")
	}
	// The sentence has to name the live value, because that is the operator's
	// next move.
	if !strings.Contains(err.Error(), "broadcasting") {
		t.Errorf("the refusal does not name the field that moved: %v", err)
	}
}

// An edit that only states `Want` without `Was` is a caller that has not read
// anything. Refused, because a precondition nobody supplied is not a
// precondition.
func TestEditSSIDRefusesAWantWithoutAWas(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Want: SSIDSettings{Name: strp("Renamed")},
	}))
	if !errors.Is(err, ErrSSIDMoved) {
		t.Fatalf("Do = %v, want ErrSSIDMoved", err)
	}
	if f.wlanPuts != 0 {
		t.Fatalf("%d PUTs, want 0", f.wlanPuts)
	}
}

// QUIRK 4 ON THE READ SIDE. An identifier the controller does not know answers
// `rc:ok` with an empty list, so the editor must refuse rather than treat the
// absence of an object as an object with no fields.
func TestEditSSIDRefusesAnUnknownServiceSet(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var tg = ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Hidden: boolp(false)},
		Want: SSIDSettings{Hidden: boolp(true)},
	})
	tg.SSIDID = "0000000000000000000000zz"

	var err = c.Do(editCtx(), EditSSID, tg)
	if !errors.Is(err, ErrGestureRefused) {
		t.Fatalf("Do on an unknown SSID = %v, want ErrGestureRefused", err)
	}
	if f.wlanPuts != 0 {
		t.Fatalf("%d PUTs, want 0", f.wlanPuts)
	}
}

// `rc:ok` IS NOT EVIDENCE (ADR-0025 §6). A PUT the controller accepts and does
// not act on must come back unconfirmed — neither a success nor a failure.
func TestEditSSIDReportsAnIneffectiveWriteAsUnconfirmed(t *testing.T) {
	var f = newFake()
	f.wlanPutIsInert = true
	var c = f.server(t)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Name: strp(testSSIDName)},
		Want: SSIDSettings{Name: strp("Renamed")},
	}))
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("Do = %v, want ErrUnconfirmed", err)
	}
	if f.wlanPuts != 1 {
		t.Fatalf("%d PUTs, want 1: the write was asked for", f.wlanPuts)
	}
}

// THE UNVERIFIED HALF. If this controller does not implement PUT on wlanconf
// at all, the edit must fail loudly as a refusal — not be reported as done, and
// not crash the screen.
func TestEditSSIDSurvivesAControllerThatRefusesThePut(t *testing.T) {
	var f = newFake()
	f.refuseWLANPut = "api.err.MethodNotAllowed"
	var c = f.server(t)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Hidden: boolp(false)},
		Want: SSIDSettings{Hidden: boolp(true)},
	}))
	if !errors.Is(err, ErrGestureRefused) {
		t.Fatalf("Do against a controller with no PUT = %v, want ErrGestureRefused", err)
	}
}

// THE PASSPHRASE. Writing it works, it is confirmed by a read, and NEITHER the
// value nor any part of it appears in what comes back (ADR-0029 §7).
func TestEditSSIDWritesThePassphraseAndNeverEchoesIt(t *testing.T) {
	var f = newFake()
	f.wlanPutIsInert = true // so the confirmation fails and produces the error
	var c = f.server(t)

	const secret = "a-new-fixture-passphrase"
	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{Passphrase: strp(secret)}))
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("Do = %v, want ErrUnconfirmed", err)
	}
	// The whole point: an operator reads this sentence, and it must not carry
	// the pre-shared key of a production network into a log or a screenshot.
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), testSSIDPass) {
		t.Fatal("the unconfirmed-passphrase error echoed a passphrase")
	}

	// And on a controller that does store it, the write lands.
	var g = newFake()
	var c2 = g.server(t)
	err = c2.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{Passphrase: strp(secret)}))
	if err != nil {
		t.Fatalf("Do(passphrase) = %v, want nil", err)
	}
	var stored string
	if json.Unmarshal(g.wlan["x_passphrase"], &stored) != nil || stored != secret {
		t.Error("the passphrase was not written")
	}
}

// An empty edit is not an error and must not write. Somebody who re-saves an
// unchanged form has not made a mistake, and a PUT that changes nothing is
// still a PUT at a live venue.
func TestEditSSIDWithNothingToChangeDoesNotWrite(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Hidden: boolp(false)},
		Want: SSIDSettings{Hidden: boolp(false)},
	}))
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	if f.wlanPuts != 0 {
		t.Fatalf("%d PUTs for an edit that changes nothing, want 0", f.wlanPuts)
	}
}

// Bands compare as a SET: the controller does not promise an order, and an
// edit must not fail — or worse, write — because of one.
func TestEditSSIDComparesBandsRegardlessOfOrder(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Bands: []string{"5", "2.4"}}, // the other order
		Want: SSIDSettings{Bands: []string{"5"}},
	}))
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	var got []string
	if json.Unmarshal(f.wlan["wlan_bands"], &got) != nil {
		t.Fatal("wlan_bands is no longer a list")
	}
	if len(got) != 1 || got[0] != "5g" {
		t.Errorf("wlan_bands = %v, want [5g]", got)
	}
	// `wlan_band`, the legacy companion key, is deliberately NOT written: its
	// token for a 5 GHz-only service set was never read off a controller, and
	// writing a guessed value is what ADR-0029 §3 refuses. It therefore comes
	// back exactly as it was.
	if string(f.wlan["wlan_band"]) != `"both"` {
		t.Errorf("wlan_band was rewritten to %s; it must be left alone", f.wlan["wlan_band"])
	}
}

// A band outside what this build offers must not be silently dropped: the
// canonicaliser refuses, so the edit refuses.
func TestEditSSIDRefusesABandItCannotTranslate(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(editCtx(), EditSSID, ssidTarget(SSIDEdit{
		Was:  SSIDSettings{Bands: []string{"2.4", "5"}},
		Want: SSIDSettings{Bands: []string{"2.4", "6"}},
	}))
	if err == nil {
		t.Fatal("an untranslatable band was accepted")
	}
	if f.wlanPuts != 0 {
		t.Fatalf("%d PUTs, want 0", f.wlanPuts)
	}
}

// GetSSID reports presence of a passphrase and NOT its value — the only thing
// the browser is ever told about it.
func TestGetSSIDHidesThePassphrase(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var d, err = c.GetSSID(editCtx(), testSiteRef, testSSIDID)
	if err != nil {
		t.Fatalf("GetSSID: %v", err)
	}
	if !d.HasPassphrase {
		t.Error("HasPassphrase is false on a service set that has one")
	}
	var enc, _ = json.Marshal(d)
	if strings.Contains(string(enc), testSSIDPass) {
		t.Fatal("the serialised SSIDDetail carries the passphrase")
	}
	if d.Settings.Name == nil || *d.Settings.Name != testSSIDName {
		t.Error("the name did not survive the projection")
	}
	if d.Security != "wpapsk" || d.WPAMode != "wpa2" {
		t.Errorf("security relayed as %q/%q", d.Security, d.WPAMode)
	}
	if len(d.Settings.Bands) != 2 {
		t.Errorf("bands = %v, want two", d.Settings.Bands)
	}
	if !d.BandsEditable {
		t.Error("bands should be editable on a 2.4+5 service set")
	}
}

// A service set carrying a band this build does not offer must say its bands
// are not editable, rather than offer an editor that would drop one.
func TestGetSSIDShutsTheBandEditorOnAnUnknownBand(t *testing.T) {
	var f = newFake()
	f.wlan["wlan_bands"] = json.RawMessage(`["2g","5g","6g"]`)
	var c = f.server(t)

	var d, err = c.GetSSID(editCtx(), testSiteRef, testSSIDID)
	if err != nil {
		t.Fatalf("GetSSID: %v", err)
	}
	if d.BandsEditable {
		t.Error("a 6 GHz service set must not offer a band editor that would drop 6 GHz")
	}
}

// A key the controller OMITS stays nil rather than becoming false. This API
// drops a field sitting at its default, and inventing `false` would make the
// form state a precondition nobody read.
func TestGetSSIDLeavesAnAbsentKeyUnknown(t *testing.T) {
	var f = newFake()
	delete(f.wlan, "proxy_arp")
	var c = f.server(t)

	var d, err = c.GetSSID(editCtx(), testSiteRef, testSSIDID)
	if err != nil {
		t.Fatalf("GetSSID: %v", err)
	}
	if d.Settings.ProxyARP != nil {
		t.Error("an absent key was defaulted instead of left unknown")
	}
}

func TestListNetworksReadsTheVLANs(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var nets, err = c.ListNetworks(editCtx(), testSiteRef)
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	if len(nets) != 2 {
		t.Fatalf("%d networks, want 2", len(nets))
	}
	// An untagged network reports VLAN 0 rather than omitting it, so a screen
	// can say "untagged" instead of saying nothing.
	if nets[0].VLAN != 0 || nets[0].Name != "Default" {
		t.Errorf("first network = %+v", nets[0])
	}
	if nets[1].VLAN != 42 {
		t.Errorf("IoT VLAN = %d, want 42", nets[1].VLAN)
	}
}

package unifi

// The gestures, tested against a double that reproduces the REAL controller's
// awkward behaviour rather than a polite version of it.
//
// Three of those behaviours were measured on a production controller
// (application 10.6.109) and each one is a trap this package exists to absorb:
//
//  1. `cmd/devmgr` answers `{"meta":{"rc":"ok"}}` to a command name it has
//     never heard of. So `rc:ok` is not evidence.
//  2. `DELETE` on the integration device resource works, is undocumented, and
//     has no inert form — the identifier IS the payload.
//  3. the integration device LIST only contains adopted boxes, so a forgotten
//     box disappears from it rather than turning up as pending.
//
// The double is therefore written to lie the way the real thing lies.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Synthetic identifiers. Deliberately not a real site's: a fixture that
// carries somebody's venue UUID invites a copy-paste into a live command.
const (
	testSiteID  = "00000000-0000-4000-8000-000000000001"
	testSiteRef = "exampleref"
	testDevID   = "00000000-0000-4000-8000-0000000000ap"
	testMAC     = "02:00:00:00:00:aa"
	testName    = "Server Room"
)

// fake is a UniFi controller double.
type fake struct {
	// present is the integration API's device list — adopted boxes only.
	present map[string]string // deviceID -> name
	// macOf maps a device id to its MAC, so the list answer is coherent.
	macOf map[string]string
	// devmgrCalls records every legacy command, in order.
	devmgrCalls []string
	// unknownCmdIsOK reproduces the fail-open behaviour: when true, a command
	// the double does not implement still answers rc:ok.
	unknownCmdIsOK bool
	// refuseDevmgr makes every legacy command answer with this msg.
	refuseDevmgr string
	// forgetDoesNothing makes DELETE answer 200 without removing the box —
	// which is how an accepted-but-ineffective gesture must look.
	forgetDoesNothing bool

	// --- the SSID resource (ADR-0029) ---

	// wlan is the stored service set, as the raw object the API round-trips.
	wlan map[string]json.RawMessage
	// lastWLANPut is the body of the last PUT, so a test can assert that the
	// WHOLE object came back and not a patch.
	lastWLANPut map[string]json.RawMessage
	// wlanPuts counts PUTs, so a refused precondition can be shown to have
	// sent nothing at all.
	wlanPuts int
	// wlanPutIsInert accepts the PUT and stores nothing — the accepted-but-
	// ineffective write, which must come back as unconfirmed and never as
	// success.
	wlanPutIsInert bool
	// refuseWLANPut answers 405 with this message: the controller that does
	// not implement PUT here at all. This is the UNVERIFIED half of ADR-0029,
	// and the test is the only place it can be exercised.
	refuseWLANPut string
}

// The service set the double starts with. The passphrase is a FIXTURE value
// and the point of having one is to prove it survives an edit that does not
// mention it.
const (
	testSSIDID   = "0000000000000000000000ss"
	testSSIDName = "ARENA"
	testSSIDPass = "fixture-not-a-real-passphrase"
)

func newFake() *fake {
	return &fake{
		present:        map[string]string{testDevID: testName},
		macOf:          map[string]string{testDevID: testMAC},
		unknownCmdIsOK: true,
		wlan:           newWLAN(),
	}
}

// newWLAN is the stored object, trimmed to the keys that matter here plus two
// that do NOT — `x_passphrase` and `x_iapp_key`, present precisely so a test
// can show that an edit which never mentions them puts them back untouched.
func newWLAN() map[string]json.RawMessage {
	var raw = `{
		"_id": "` + testSSIDID + `",
		"name": "` + testSSIDName + `",
		"enabled": true,
		"hide_ssid": false,
		"wlan_bands": ["2g","5g"],
		"wlan_band": "both",
		"networkconf_id": "net-lan",
		"is_guest": false,
		"l2_isolation": false,
		"fast_roaming_enabled": false,
		"bss_transition": true,
		"uapsd_enabled": false,
		"mcastenhance_enabled": false,
		"proxy_arp": false,
		"security": "wpapsk",
		"wpa_mode": "wpa2",
		"wpa_enc": "ccmp",
		"x_passphrase": "` + testSSIDPass + `",
		"x_iapp_key": "deadbeefdeadbeefdeadbeefdeadbeef"
	}`
	var out map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &out) != nil {
		panic("the SSID fixture is not valid JSON")
	}
	return out
}

// ssidTarget is a Target aimed at the double's service set.
func ssidTarget(edit SSIDEdit) Target {
	return Target{
		SiteID: testSiteID, SiteRef: testSiteRef,
		SSIDID: testSSIDID, SSIDEdit: edit,
	}
}

func boolp(v bool) *bool    { return &v }
func strp(v string) *string { return &v }

func (f *fake) server(t *testing.T) *Client {
	t.Helper()
	var srv = httptest.NewServer(http.HandlerFunc(f.route))
	t.Cleanup(srv.Close)
	var c, err = New(srv.URL, "token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func (f *fake) route(w http.ResponseWriter, r *http.Request) {
	var p = r.URL.Path
	w.Header().Set("Content-Type", "application/json")

	switch {
	// The integration device list. Adopted boxes only (quirk 3).
	case r.Method == http.MethodGet && p == "/proxy/network/integration/v1/sites/"+testSiteID+"/devices":
		type entry struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			MACAddress string `json:"macAddress"`
		}
		var data = []entry{}
		var id, name string
		for id, name = range f.present {
			data = append(data, entry{ID: id, Name: name, MACAddress: f.macOf[id]})
		}
		var raw, _ = json.Marshal(data)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"offset": 0, "limit": 200, "count": len(data),
			"totalCount": len(data), "data": json.RawMessage(raw),
		})

	// One device's detail — what the confirmation name is read from.
	case r.Method == http.MethodGet && strings.HasPrefix(p,
		"/proxy/network/integration/v1/sites/"+testSiteID+"/devices/"):
		var id = strings.TrimPrefix(p, "/proxy/network/integration/v1/sites/"+testSiteID+"/devices/")
		var name, ok = f.present[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"statusCode":404}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "name": name, "macAddress": f.macOf[id],
		})

	// Forget: undocumented DELETE, 200, no confirmation asked (quirk 2).
	case r.Method == http.MethodDelete && strings.HasPrefix(p,
		"/proxy/network/integration/v1/sites/"+testSiteID+"/devices/"):
		var id = strings.TrimPrefix(p, "/proxy/network/integration/v1/sites/"+testSiteID+"/devices/")
		if !f.forgetDoesNothing {
			delete(f.present, id)
		}
		w.WriteHeader(http.StatusOK)

	// The legacy command endpoint.
	case r.Method == http.MethodPost && p == "/proxy/network/api/s/"+testSiteRef+"/cmd/devmgr":
		var in struct {
			Cmd string `json:"cmd"`
			MAC string `json:"mac"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.devmgrCalls = append(f.devmgrCalls, in.Cmd+" "+in.MAC)
		if f.refuseDevmgr != "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"meta":{"rc":"error","msg":"` + f.refuseDevmgr + `"},"data":[]}`))
			return
		}
		switch in.Cmd {
		case "adopt":
			f.present[testDevID] = "AC Pro" // adoption loses the name, as observed
			f.macOf[testDevID] = testMAC
			_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
		case "force-provision":
			_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
		default:
			// QUIRK 1: an unknown command answers ok and does nothing.
			if f.unknownCmdIsOK {
				_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"meta":{"rc":"error","msg":"api.err.Invalid"},"data":[]}`))
		}

	case r.Method == http.MethodGet && p == "/proxy/network/api/s/"+testSiteRef+"/stat/device-basic":
		_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[
			{"mac":"02:00:00:00:00:09","model":"U7PG2","type":"uap","adopted":false},
			{"mac":"` + testMAC + `","model":"U7PG2","type":"uap","adopted":true}
		]}`))

	// One service set, as a REST resource: collection item GET and PUT.
	//
	// QUIRK 4, measured like the other three: an identifier this endpoint does
	// not know answers `{"meta":{"rc":"ok"},"data":[]}`. So the fail-open is
	// reproduced on the READ side too, which is what the editor has to survive.
	case strings.HasPrefix(p, "/proxy/network/api/s/"+testSiteRef+"/rest/wlanconf/"):
		var id = strings.TrimPrefix(p, "/proxy/network/api/s/"+testSiteRef+"/rest/wlanconf/")
		if id != testSSIDID || f.wlan == nil {
			// rc:ok with NOTHING in it. Not a 404.
			_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
			return
		}
		if r.Method == http.MethodPut {
			f.wlanPuts++
			if f.refuseWLANPut != "" {
				w.WriteHeader(http.StatusMethodNotAllowed)
				_, _ = w.Write([]byte(`{"meta":{"rc":"error","msg":"` + f.refuseWLANPut + `"},"data":[]}`))
				return
			}
			var in map[string]json.RawMessage
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			f.lastWLANPut = in
			if !f.wlanPutIsInert {
				// The real handler replaces the stored object with the body it
				// was given — which is precisely why a PARTIAL body is
				// dangerous and why the client sends the whole thing.
				f.wlan = in
			}
			_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
			return
		}
		var one, _ = json.Marshal([]map[string]json.RawMessage{f.wlan})
		_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":` + string(one) + `}`))

	case r.Method == http.MethodGet && p == "/proxy/network/api/s/"+testSiteRef+"/rest/networkconf":
		_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[
			{"_id":"net-lan","name":"Default","purpose":"corporate","vlan_enabled":false},
			{"_id":"net-iot","name":"IoT","purpose":"corporate","vlan_enabled":true,"vlan":42}
		]}`))

	case r.Method == http.MethodGet && p == "/proxy/network/api/self":
		_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[
			{"email":"admin@example.net","name":"UniFi User","is_owner":true,"is_super":true}
		]}`))

	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"statusCode":404,"message":"no endpoint ` + p + `"}`))
	}
}

func target() Target {
	return Target{
		SiteID: testSiteID, SiteRef: testSiteRef,
		DeviceID: testDevID, MAC: testMAC,
	}
}

// THE STRUCTURAL INVARIANT. A zero Gesture must refuse rather than guess.
//
// This is the half of ADR-0025 §4 a test can show: the other half — that no
// caller outside the package can BUILD a Gesture — is enforced by the compiler,
// because the type's only field is unexported and has no literal form.
func TestZeroGestureRefuses(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(context.Background(), Gesture{}, target())
	if !errors.Is(err, ErrNoGesture) {
		t.Fatalf("Do(zero gesture) = %v, want ErrNoGesture", err)
	}
	if len(f.devmgrCalls) != 0 {
		t.Fatalf("a zero gesture reached the controller: %v", f.devmgrCalls)
	}
	var _, still = f.present[testDevID]
	if !still {
		t.Fatal("a zero gesture removed the box")
	}
}

// The write surface is exactly four gestures, and they are named.
func TestGestureSetIsClosed(t *testing.T) {
	var got = []string{Adopt.Name(), Forget.Name(), Reprovision.Name(), EditSSID.Name()}
	var want = []string{"adopt", "forget", "reprovision", "edit-ssid"}
	var i int
	for i = range want {
		if got[i] != want[i] {
			t.Errorf("gesture %d is %q, want %q", i, got[i], want[i])
		}
	}
	// Only the irreversible one demands a name.
	if !Forget.Destructive() {
		t.Error("Forget must be destructive: it cannot be undone on UniFi's side")
	}
	if Adopt.Destructive() || Reprovision.Destructive() || EditSSID.Destructive() {
		t.Error("only Forget is destructive; marking another one dilutes the gate")
	}
	var zero = Gesture{}
	if zero.Name() != "" || zero.Destructive() {
		t.Error("the zero Gesture must describe itself as nothing")
	}
}

// A forget with the WRONG name must change nothing. This is the gate from
// ADR-0012, and the reason it is inside Do rather than in a handler.
func TestForgetRefusesAWrongName(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var tg = target()
	tg.ConfirmName = "Server Roon" // one letter out
	var err = c.Do(context.Background(), Forget, tg)
	if !errors.Is(err, ErrConfirmMismatch) {
		t.Fatalf("Do(Forget, wrong name) = %v, want ErrConfirmMismatch", err)
	}
	var _, still = f.present[testDevID]
	if !still {
		t.Fatal("THE BOX WAS FORGOTTEN on a mismatched name")
	}
	// The real name belongs in the message: it is the operator's next move.
	if !strings.Contains(err.Error(), testName) {
		t.Errorf("the refusal does not say what the box is really called: %v", err)
	}
}

// An EMPTY confirmation name is a refusal too — otherwise the gate would open
// for any caller that simply omitted the field.
func TestForgetRefusesAnEmptyName(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(context.Background(), Forget, target())
	if !errors.Is(err, ErrConfirmMismatch) {
		t.Fatalf("Do(Forget, no name) = %v, want ErrConfirmMismatch", err)
	}
	var _, still = f.present[testDevID]
	if !still {
		t.Fatal("THE BOX WAS FORGOTTEN with no confirmation at all")
	}
}

// A box that has been renamed under us must fail closed. This is the case the
// gate exists for: the id is still valid, so an id-only check would proceed on
// an object nobody read.
func TestForgetRefusesAfterARename(t *testing.T) {
	var f = newFake()
	var c = f.server(t)
	f.present[testDevID] = "Server Room (old)"

	var tg = target()
	tg.ConfirmName = testName
	var err = c.Do(context.Background(), Forget, tg)
	if !errors.Is(err, ErrConfirmMismatch) {
		t.Fatalf("Do(Forget) after a rename = %v, want ErrConfirmMismatch", err)
	}
	if len(f.present) != 1 {
		t.Fatal("the renamed box was forgotten")
	}
}

// The right name forgets the box, and the confirming read sees it gone.
func TestForgetWithTheRightName(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var tg = target()
	tg.ConfirmName = testName
	var err = c.Do(context.Background(), Forget, tg)
	if err != nil {
		t.Fatalf("Do(Forget) = %v", err)
	}
	var _, still = f.present[testDevID]
	if still {
		t.Fatal("the box is still on the site")
	}
}

// A forget the controller ACCEPTS but does not carry out comes back as
// unconfirmed — not as a success (ADR-0025 §6).
func TestForgetAcceptedButIneffectiveIsUnconfirmed(t *testing.T) {
	var f = newFake()
	f.forgetDoesNothing = true
	var c = f.server(t)

	var tg = target()
	tg.ConfirmName = testName
	var err = c.Do(context.Background(), Forget, tg)
	if !errors.Is(err, ErrUnconfirmed) {
		t.Fatalf("Do(Forget) on an ineffective controller = %v, want ErrUnconfirmed", err)
	}
}

// Adoption goes out as `adopt` on the legacy endpoint and is confirmed by the
// site knowing the box — not by the box being ONLINE, which takes minutes.
func TestAdoptAsksAndConfirms(t *testing.T) {
	var f = newFake()
	delete(f.present, testDevID) // not adopted yet
	var c = f.server(t)

	var err = c.Do(context.Background(), Adopt, target())
	if err != nil {
		t.Fatalf("Do(Adopt) = %v", err)
	}
	if len(f.devmgrCalls) != 1 || f.devmgrCalls[0] != "adopt "+testMAC {
		t.Fatalf("devmgr calls = %v, want one adopt", f.devmgrCalls)
	}
}

// An adoption the controller refuses must come back as a refusal even though
// the HTTP status was 200 — which is how this API reports a refusal.
func TestAdoptRefusalIsNotASuccess(t *testing.T) {
	var f = newFake()
	delete(f.present, testDevID)
	f.refuseDevmgr = "api.err.CanNotAdoptUnknownDevice"
	var c = f.server(t)

	var err = c.Do(context.Background(), Adopt, target())
	if !errors.Is(err, ErrGestureRefused) {
		t.Fatalf("Do(Adopt) on a refusal = %v, want ErrGestureRefused", err)
	}
	if !strings.Contains(err.Error(), "CanNotAdoptUnknownDevice") {
		t.Errorf("the controller's reason is lost: %v", err)
	}
}

// Reprovision sends `force-provision` and NOTHING ELSE. The assertion that
// matters is the absence of a payload: the moment this gesture carries settings,
// meshcraft holds an intention and ADR-0025 §2 is broken.
func TestReprovisionCarriesNoConfiguration(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(context.Background(), Reprovision, target())
	if err != nil {
		t.Fatalf("Do(Reprovision) = %v", err)
	}
	if len(f.devmgrCalls) != 1 || f.devmgrCalls[0] != "force-provision "+testMAC {
		t.Fatalf("devmgr calls = %v, want one force-provision", f.devmgrCalls)
	}
	// Still adopted, still named: reprovisioning is not destructive.
	if f.present[testDevID] != testName {
		t.Fatal("reprovisioning changed the box")
	}
}

// A gesture with no site id refuses before touching anything.
func TestGestureNeedsASite(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.Do(context.Background(), Reprovision, Target{MAC: testMAC})
	if err == nil {
		t.Fatal("Do with no site id was accepted")
	}
	if len(f.devmgrCalls) != 0 {
		t.Fatalf("it reached the controller anyway: %v", f.devmgrCalls)
	}
}

// Only unadopted boxes are offered for adoption, and the list is the LEGACY
// one — the integration list never shows a pending box.
func TestListAdoptableKeepsOnlyPendingBoxes(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var list, err = c.ListAdoptable(context.Background(), testSiteRef)
	if err != nil {
		t.Fatalf("ListAdoptable: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d candidates, want 1 (the adopted box must not be offered)", len(list))
	}
	if list[0].MAC != "02:00:00:00:00:09" {
		t.Errorf("candidate MAC = %q", list[0].MAC)
	}
}

func TestListAdoptableNeedsTheShortReference(t *testing.T) {
	var f = newFake()
	var c = f.server(t)
	var _, err = c.ListAdoptable(context.Background(), "  ")
	if err == nil {
		t.Fatal("ListAdoptable accepted an empty site reference")
	}
}

// An owner/super token reports `granted`. The screen may then offer gestures
// without a caveat.
func TestRightsReadsASuperToken(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var rights, err = c.Rights(context.Background())
	if err != nil {
		t.Fatalf("Rights: %v", err)
	}
	if rights.Writes != WritesGranted {
		t.Errorf("Writes = %q, want %q", rights.Writes, WritesGranted)
	}
	if rights.Scoped {
		t.Error("an owner must not be reported as scoped")
	}
	if rights.Admin != "admin@example.net" {
		t.Errorf("Admin = %q", rights.Admin)
	}
}

// A token that is NEITHER owner nor super comes back `unproven` and never
// `denied`: UniFi does not describe per-site rights here, and claiming the
// gesture will fail would be as wrong as claiming it will work.
func TestRightsOnALimitedTokenIsUnproven(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[
			{"email":"viewer@example.net","is_owner":false,"is_super":false}
		]}`))
	}))
	defer srv.Close()
	var c, err = New(srv.URL, "token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var rights Rights
	rights, err = c.Rights(context.Background())
	if err != nil {
		t.Fatalf("Rights: %v", err)
	}
	if rights.Writes != WritesUnproven {
		t.Errorf("Writes = %q, want %q", rights.Writes, WritesUnproven)
	}
	if !rights.Scoped {
		t.Error("a limited token must be reported as scoped")
	}
}

// THE FAIL-OPEN TRAP, stated as a test so nobody "simplifies" devmgr into
// trusting the HTTP status. A 200 whose rc is not `ok` is a refusal.
func TestDevmgrTreatsAnythingButOKAsARefusal(t *testing.T) {
	var f = newFake()
	f.refuseDevmgr = "api.err.UnknownDevice"
	var c = f.server(t)

	var err = c.devmgr(context.Background(), testSiteRef, "force-provision", testMAC)
	if !errors.Is(err, ErrGestureRefused) {
		t.Fatalf("devmgr on rc=error = %v, want ErrGestureRefused", err)
	}
}

// devmgr addresses a box by MAC; without one it must not send anything, or the
// controller would answer rc:ok to a command about nobody.
func TestDevmgrNeedsAMac(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.devmgr(context.Background(), testSiteRef, "adopt", "")
	if !errors.Is(err, ErrGestureRefused) {
		t.Fatalf("devmgr with no MAC = %v, want ErrGestureRefused", err)
	}
	if len(f.devmgrCalls) != 0 {
		t.Fatalf("it was sent anyway: %v", f.devmgrCalls)
	}
}

// The MAC is lowercased on the way out: devmgr compares it as a string, and
// the mirror stores it lowercased, so a mixed-case MAC would be an unknown
// device.
func TestDevmgrLowercasesTheMac(t *testing.T) {
	var f = newFake()
	var c = f.server(t)

	var err = c.devmgr(context.Background(), testSiteRef, "force-provision", strings.ToUpper(testMAC))
	if err != nil {
		t.Fatalf("devmgr: %v", err)
	}
	if f.devmgrCalls[0] != "force-provision "+testMAC {
		t.Fatalf("sent %q, want a lowercased MAC", f.devmgrCalls[0])
	}
}

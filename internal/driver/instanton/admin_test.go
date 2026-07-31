package instanton

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// What the device returns to an accepted write.
const okResponse = `<?xml version="1.0" encoding="UTF-8"?><ResponseData>` +
	`<ActionStatus><requestURL>wcd</requestURL><statusCode>0</statusCode>` +
	`<statusString>OK</statusString></ActionStatus></ResponseData>`

type captured struct {
	method      string
	path        string
	rawQuery    string
	contentType string
	body        string
}

// accountsResponse mimics a read of `wcd?{AdminUserSetting}`.
func accountsResponse(users ...string) string {
	var out = `<?xml version="1.0" encoding="UTF-8"?><ResponseData><DeviceConfiguration>` +
		`<AdminUserSetting type="section"><AdminUserList>`
	var u string
	for _, u = range users {
		out += "<AdminUserEntry><userName>" + u + "</userName>" +
			"<accessLevel>15</accessLevel></AdminUserEntry>"
	}
	return out + `</AdminUserList></AdminUserSetting></DeviceConfiguration></ResponseData>`
}

// newFakeSwitch serves a device that accepts everything and records what is sent.
// Reads return the provided account list, writes return success.
func newFakeSwitch(t *testing.T, seen *[]captured, users ...string) *httptest.Server {
	t.Helper()
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var raw, _ = io.ReadAll(r.Body)
			*seen = append(*seen, captured{
				method:      r.Method,
				path:        r.URL.Path,
				rawQuery:    r.URL.RawQuery,
				contentType: r.Header.Get("Content-Type"),
				body:        string(raw),
			})
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(accountsResponse(users...)))
				return
			}
			_, _ = w.Write([]byte(okResponse))
		}))
	t.Cleanup(srv.Close)
	return srv
}

// readAll reads the body of a request received by a test device.
func readAll(r *http.Request) string {
	var raw, _ = io.ReadAll(r.Body)
	return string(raw)
}

// writes keeps only writes: account tests read first.
func writes(seen []captured) []captured {
	var out []captured
	var c captured
	for _, c = range seen {
		if c.method == http.MethodPost {
			out = append(out, c)
		}
	}
	return out
}

// adminOn builds an already « logged in » session: only write is tested.
func adminOn(srv *httptest.Server) *Admin {
	var a = &Admin{web: driver.NewWebSession(srv.URL, nil, 0)}
	a.page = srv.URL + "/cs12345678/hpe/config/login.htm"
	return a
}

// fakeLogin serves the minimum to exercise login: home page, encryption setting
// (disabled — candidate storage does not depend on transport), and login request,
// which accepts ONLY ONE secret.
func fakeLogin(t *testing.T, bon string) *httptest.Server {
	t.Helper()
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.RawQuery, "EncryptionSetting"):
				_, _ = w.Write([]byte(`<ResponseData>` +
					`<passwEncryptEnable>2</passwEncryptEnable></ResponseData>`))
			case strings.Contains(r.URL.RawQuery, "action=login"):
				var code = "4" // invalid username or password
				if strings.Contains(r.URL.RawQuery, "password="+bon) {
					code = "0"
				}
				_, _ = w.Write([]byte(`<ResponseData><ActionStatus><statusCode>` + code +
					`</statusCode></ActionStatus></ResponseData>`))
			default:
				_, _ = w.Write([]byte(`<html>login page</html>`))
			}
		}))
	t.Cleanup(srv.Close)
	return srv
}

// TestLoginMemoriseLeCandidatAccepte: the secret that opened the device is RETURNED
// by the session, not reconstructed by the caller.
func TestLoginMemoriseLeCandidatAccepte(t *testing.T) {
	const bon = "MotDePasse2"
	var srv = fakeLogin(t, bon)

	var cands = []Candidate{
		{Username: "admin", Secret: "vieuxSecret1", Why: "generation 1, retired"},
		{Username: "mcadmin", Secret: bon, Why: "generation 2, observed"},
	}
	var a = &Admin{web: driver.NewWebSession(srv.URL, nil, 0)}
	var err = a.Login(context.Background(), cands)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if a.Accepted() != cands[1] {
		t.Errorf("Accepted() = %+v, expected candidate device accepted (%+v)",
			a.Accepted(), cands[1])
	}
	if a.Account() != cands[1].Username {
		t.Errorf("Account() = %q, expected %q", a.Account(), cands[1].Username)
	}
}

// TestLoginRefuseNeMemoriseRien: without session, there is no current secret.
func TestLoginRefuseNeMemoriseRien(t *testing.T) {
	var srv = fakeLogin(t, "MotDePasse2")

	var a = &Admin{web: driver.NewWebSession(srv.URL, nil, 0)}
	var err = a.Login(context.Background(), []Candidate{
		{Username: "admin", Secret: "mauvais", Why: "factory account"},
	})
	if err == nil {
		t.Fatal("refused secret must not open session")
	}
	if a.Accepted() != (Candidate{}) {
		t.Errorf("Accepted() = %+v, expected empty: no candidate served", a.Accepted())
	}
}

// TestSetPasswordRequestShape locks the three « wcd » framework conventions without
// which the device accepts the request WITHOUT DOING ANYTHING.
func TestSetPasswordRequestShape(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen)

	var err = adminOn(srv).SetPassword(context.Background(), "admin", "ancien", "S3cret!&<x")
	if err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 requests (account then save), got %d", len(seen))
	}

	var got = seen[0]
	if got.method != http.MethodPost {
		t.Errorf("method = %q, expected POST", got.method)
	}
	if got.path != "/cs12345678/hpe/wcd" {
		t.Errorf("path = %q, expected /cs12345678/hpe/wcd", got.path)
	}
	if got.rawQuery != "{AdminUserSetting}" {
		t.Errorf("query = %q, expected {AdminUserSetting}", got.rawQuery)
	}
	if !strings.HasPrefix(got.contentType, "application/x-www-form-urlencoded") {
		t.Errorf("content-type = %q", got.contentType)
	}
	if !strings.Contains(got.body, `<AdminUserSetting action="set">`) {
		t.Errorf("action must be a section attribute: %s", got.body)
	}
	if strings.Contains(got.body, "<set>") || strings.Contains(got.body, "<edit>") {
		t.Errorf("action must NOT be a child element: %s", got.body)
	}
	var want = []string{
		"<?xml version='1.0' encoding='utf-8'?>",
		"<DeviceConfiguration>",
		"<userName>admin</userName>",
		"<password>$S3cret!&amp;&lt;x</password>",
		"<accessLevel>15</accessLevel>",
	}
	var w string
	for _, w = range want {
		if !strings.Contains(got.body, w) {
			t.Errorf("body missing %q: %s", w, got.body)
		}
	}
	if strings.Contains(got.body, "ancien") {
		t.Errorf("old secret must not be transmitted: %s", got.body)
	}
}

// TestSetPasswordSavesToFlash: an unsaved apply disappears on first reboot.
func TestSetPasswordSavesToFlash(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen)

	var err = adminOn(srv).SetPassword(context.Background(), "admin", "", "MotDePasse1")
	if err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("save must follow apply: %d request(s)", len(seen))
	}
	var save = seen[1]
	if save.rawQuery != "{file=/hpe/home.htm}{FileCopy}" {
		t.Errorf("save query = %q", save.rawQuery)
	}
	var want = []string{
		`<FileCopy action="set">`,
		"<srcFileType>2</srcFileType>",
		"<destFileType>3</destFileType>",
	}
	var w string
	for _, w = range want {
		if !strings.Contains(save.body, w) {
			t.Errorf("save missing %q: %s", w, save.body)
		}
	}
}

// TestSetPasswordRejectsEmptyStatus: 200 without status code is this interface's
// characteristic silent failure.
func TestSetPasswordRejectsEmptyStatus(t *testing.T) {
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	var err = adminOn(srv).SetPassword(context.Background(), "admin", "", "MotDePasse1")
	if err == nil {
		t.Fatal("empty 200 body must be error, not success")
	}
	if !strings.Contains(err.Error(), "nothing was modified") {
		t.Errorf("message must say nothing changed: %v", err)
	}
}

// TestEnsureAccountCreatesThenUpdates: device distinguishes `add` from `set`.
func TestEnsureAccountCreatesThenUpdates(t *testing.T) {
	var cas = []struct {
		name     string
		presents []string
		action   string
	}{
		{"missing account", []string{"admin"}, `action="add"`},
		{"account already there", []string{"admin", "mcadmin"}, `action="set"`},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var seen []captured
			var srv = newFakeSwitch(t, &seen, c.presents...)

			var err = adminOn(srv).EnsureAccount(context.Background(), "mcadmin", "MotDePasse1")
			if err != nil {
				t.Fatalf("EnsureAccount: %v", err)
			}
			if len(seen) == 0 || seen[0].method != http.MethodGet {
				t.Fatalf("existing must be read before write: %+v", seen)
			}
			var w = writes(seen)
			if len(w) != 1 {
				t.Fatalf("expected 1 write, got %d", len(w))
			}
			if !strings.Contains(w[0].body, c.action) {
				t.Errorf("expected %s: %s", c.action, w[0].body)
			}
			if !strings.Contains(w[0].body, "<userName>mcadmin</userName>") {
				t.Errorf("body missing desired name: %s", w[0].body)
			}
		})
	}
}

// TestDeleteAccount verifies deletion shape.
func TestDeleteAccount(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen, "admin", "mcadmin")
	var a = adminOn(srv)
	a.User = "mcadmin"

	var err = a.DeleteAccount(context.Background(), "admin")
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	var w = writes(seen)
	if len(w) != 1 {
		t.Fatalf("expected 1 write, got %d", len(w))
	}
	if !strings.Contains(w[0].body, `<AdminUserSetting action="delete">`) {
		t.Errorf("expected delete action: %s", w[0].body)
	}
	if !strings.Contains(w[0].body, "<userName>admin</userName>") {
		t.Errorf("body missing account to remove: %s", w[0].body)
	}
	if strings.Contains(w[0].body, "<password>") {
		t.Errorf("deletion must not carry password: %s", w[0].body)
	}
}

// TestDeleteAccountRefusesSelf: deleting current session account is refused.
func TestDeleteAccountRefusesSelf(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen, "admin")
	var a = adminOn(srv)
	a.User = "admin"

	var err = a.DeleteAccount(context.Background(), "Admin")
	if err == nil {
		t.Fatal("deleting own account must be refused")
	}
	if len(writes(seen)) != 0 {
		t.Errorf("nothing must be sent to device: %+v", seen)
	}
}

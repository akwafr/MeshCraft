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

// adminOn builds an already "logged in" session: only write is tested.
//
// `host` is filled in because the deletion refusals name the device they
// refused on, and a message reading "refusing to delete … on" would be the
// one line an operator needs and cannot use.
func adminOn(srv *httptest.Server) *Admin {
	var a = &Admin{web: driver.NewWebSession(srv.URL, nil, 0), host: srv.Listener.Addr().String()}
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

// TestLoginRemembersTheAcceptedCandidate: the secret that opened the device is RETURNED
// by the session, not reconstructed by the caller.
func TestLoginRemembersTheAcceptedCandidate(t *testing.T) {
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

// TestRefusedLoginRemembersNothing: without session, there is no current secret.
func TestRefusedLoginRemembersNothing(t *testing.T) {
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

// TestSetPasswordRequestShape locks the three "wcd" framework conventions without
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

// serveBody answers every GET with one fixed body: the account section as this
// firmware has been seen rendering it, or a mangled version of it.
func serveBody(t *testing.T, body string) *httptest.Server {
	t.Helper()
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAccountsCarriesTheAccessLevel — what the widened contract gets out of
// THIS range's single GET.
//
// `<accessLevel>` sits beside `<userName>` in the section this read already
// fetches (15 is read/write here, cf. accessLevelRW), and the connector used to
// drop it: a level-1 account and the administrator came back as the same
// string. No extra request is made for it, and the test checks that too.
func TestAccountsCarriesTheAccessLevel(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen, "admin", "mcadmin")

	var got, err = adminOn(srv).Accounts(context.Background())
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(got) != 2 || got[0].Name != "admin" || got[1].Name != "mcadmin" {
		t.Fatalf("accounts = %+v", got)
	}
	var i int
	for i = range got {
		if got[i].Privilege != 15 || got[i].Authority != driver.AuthorityFull {
			t.Errorf("%+v: the section states accessLevel 15 and it must travel", got[i])
		}
		if got[i].Source != sectionAdminUsers {
			t.Errorf("%+v: the source names the section it was read from", got[i])
		}
	}
	if len(seen) != 1 {
		t.Errorf("%d requests — enumerating accounts must stay ONE GET; this range "+
			"grants five sessions and pays for every extra round trip", len(seen))
	}
}

// TestAccountsWithoutAccessLevelReadsUnknown.
//
// This firmware answers with a MIX of XML and fragments depending on the
// sections asked for, so an entry can arrive without its level. That must read
// as "not stated", never as level 0 — 0 would look like a deliberately
// unprivileged account and hide whatever the entry really is.
func TestAccountsWithoutAccessLevelReadsUnknown(t *testing.T) {
	var srv = serveBody(t, `<?xml version="1.0" encoding="UTF-8"?><ResponseData>`+
		`<DeviceConfiguration><AdminUserSetting type="section"><AdminUserList>`+
		`<AdminUserEntry><userName>admin</userName></AdminUserEntry>`+
		`<AdminUserEntry><userName>lecteur</userName><accessLevel>1</accessLevel></AdminUserEntry>`+
		`</AdminUserList></AdminUserSetting></DeviceConfiguration></ResponseData>`)

	var got, err = adminOn(srv).Accounts(context.Background())
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("an entry without its level is STILL an account: %+v", got)
	}
	if got[0].Privilege != driver.PrivilegeUnknown ||
		got[0].Authority != driver.AuthorityUnknown {
		t.Errorf("%+v: a missing accessLevel is \"not stated\", not level 0", got[0])
	}
	// And the level of the NEXT entry must not leak backwards into it.
	if got[1].Privilege != 1 || got[1].Authority != driver.AuthorityLimited {
		t.Errorf("%+v: accessLevel 1 is a real, limited level on this range", got[1])
	}
}

// TestEnsureAccountCreatesThenUpdates: device distinguishes `add` from `set`.
func TestEnsureAccountCreatesThenUpdates(t *testing.T) {
	var cases = []struct {
		name     string
		presents []string
		action   string
	}{
		{"missing account", []string{"admin"}, `action="add"`},
		{"account already there", []string{"admin", "mcadmin"}, `action="set"`},
	}
	var c = cases[0]
	for _, c = range cases {
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
//
// This refusal is FREE and must stay in front of the read: nothing at all is
// sent, not even the enumeration the last-account guard needs, because a session
// cannot outlive the account it was opened under whatever the rest of the device
// carries.
func TestDeleteAccountRefusesSelf(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen, "admin")
	var a = adminOn(srv)
	a.User = "admin"

	var err = a.DeleteAccount(context.Background(), "Admin")
	if err == nil {
		t.Fatal("deleting own account must be refused")
	}
	if !strings.Contains(err.Error(), "current session") {
		t.Errorf("the refusal must name its reason: %v", err)
	}
	if len(seen) != 0 {
		t.Errorf("this refusal costs nothing: not one request may be sent, got %+v", seen)
	}
}

// --- the last-account guard ------------------------------------------------
//
// A deletion on this range is the gesture with NO cheap repair anywhere in the
// tree: no SSH, no Telnet, no serial port. A 1930 left without an account is
// recovered with the factory-reset button, on site, at the cost of its
// configuration. Until 2026-08-12 the only refusal here compared the target
// against the CURRENT SESSION's name, and TODO.md wrongly listed `admin.go:605`
// — the `Accounts` call inside `EnsureAccount` — as a guard, so the gap hid
// behind a citation. These tests pin the three refusals, the limit of the
// second, and the platform difference that made the third necessary.

// TestDeleteRefusesTheLastAccount: whoever is asking, the last enumerated
// account does not go.
//
// The session runs as an account the section does not name — the only shape in
// which the last-account refusal can fire ahead of the current-session one.
func TestDeleteRefusesTheLastAccount(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen, "admin")
	var a = adminOn(srv)
	a.User = "mcadmin"

	var err = a.DeleteAccount(context.Background(), "ADMIN")
	if err == nil {
		t.Fatal("the last enumerated account must not be deletable: this range has no " +
			"SSH, no Telnet and no serial port, and the repair is a site visit")
	}
	if !strings.Contains(err.Error(), "only administration account") {
		t.Errorf("the refusal must say what it refused: %v", err)
	}
	if !strings.Contains(err.Error(), sectionAdminUsers) {
		t.Errorf("the refusal must name the source it counted from: %v", err)
	}
	if len(writes(seen)) != 0 {
		t.Fatalf("a refused deletion must cost the device NOTHING: %+v", writes(seen))
	}
}

// TestDeleteRefusesWhenTheAccountsCannotBeRead: a read that fails REFUSES here,
// where `arista/eos` and `cisco/ios` proceed. Their comments justify failing
// open in one word — the console still works — and this range has no console at
// all, so the justification does not transfer.
func TestDeleteRefusesWhenTheAccountsCannotBeRead(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen, "admin", "mcadmin")
	var a = adminOn(srv)
	a.User = "mcadmin"
	// The device stops answering between login and deletion — the transport
	// error [Admin.Accounts] does surface, as opposed to the silent one below.
	srv.Close()

	var err = a.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("an unreadable account list must not open the guard: recovery from an " +
			"account-less 1930 is a site visit")
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("the refusal must say the guard could not be evaluated: %v", err)
	}
	if len(writes(seen)) != 0 {
		t.Fatalf("nothing may be written on a refusal: %+v", writes(seen))
	}
}

// TestDeleteRefusesAnEmptyAccountRead — THE PLATFORM DIFFERENCE, and the reason
// this port is not a copy of the EdgeSwitch one.
//
// There the list comes from a CLI and a failed read fails LOUDLY. Here it is a
// `wcd` GET: `driver.WebSession.Get` does not treat a non-200 as an error, and
// this firmware answers a section it does not recognise with HTTP 200 and
// `statusCode = -1` — which parses to zero accounts and NO error. So "could not
// read" and "has no accounts" are INDISTINGUISHABLE at the parse, and the
// empty case has to refuse on its own.
//
// It is sound for a reason peculiar to a session-based read: this session is
// open, so the device holds at least the account it was opened under. Zero
// accounts contradicts that, and the contradiction is the evidence.
func TestDeleteRefusesAnEmptyAccountRead(t *testing.T) {
	var seen []captured
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var raw, _ = io.ReadAll(r.Body)
			seen = append(seen, captured{method: r.Method, path: r.URL.Path, body: string(raw)})
			// What this range answers for a section it does not recognise —
			// the `UpdatePassword` finding, HTTP 200 and no entry at all.
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ResponseData>` +
				`<ActionStatus><statusCode>-1</statusCode></ActionStatus></ResponseData>`))
		}))
	t.Cleanup(srv.Close)
	var a = adminOn(srv)
	a.User = "mcadmin"

	var err = a.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("a section naming no account did not answer with the account list: an " +
			"empty read here is an unreadable one, not an empty device")
	}
	if !strings.Contains(err.Error(), "named no account") {
		t.Errorf("the refusal must say the read told it nothing: %v", err)
	}
	if len(writes(seen)) != 0 {
		t.Fatalf("nothing may be written on a refusal: %+v", writes(seen))
	}
}

// TestDeleteAllowsAnAccountThatIsNotTheLast: the other half. A deletion that
// leaves something behind still goes through, and the success line says what the
// guard did NOT check.
//
// IT PINS WHAT IS NOT PROMISED. The connector counts what the device
// enumerates; whether meshcraft holds a secret that opens any survivor is a
// question about the vault, and a connector has no vault. If this test is ever
// changed to expect a refusal, the check it wants belongs in `internal/api`.
func TestDeleteAllowsAnAccountThatIsNotTheLast(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen, "admin", "mcadmin")
	var a = adminOn(srv)
	a.User = "mcadmin"

	var err = a.DeleteAccount(context.Background(), "admin")
	if err != nil {
		t.Fatalf("one account survives this deletion, it must proceed: %v", err)
	}
	if len(writes(seen)) != 1 {
		t.Fatalf("expected exactly one write: %+v", writes(seen))
	}
	// The guard costs ONE GET on the already-open session, the same read
	// EnsureAccount already pays for. This range grants five sessions.
	if len(seen) != 2 {
		t.Errorf("expected one read then one write, got %d requests: %+v", len(seen), seen)
	}
	var told = a.Steps[len(a.Steps)-1]
	if !strings.Contains(told.What, "NOT a promise") {
		t.Errorf("the success line must refuse to imply a way back in: %q", told.What)
	}
}

package discovery

import (
	"context"
	"crypto/md5" //nolint:gosec // imposed by Digest, see internal/driver/webfetch.go
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// These tests replay the THREE outcomes of trying credentials on a web
// interface, because they are three different gestures for the operator: fix a
// password, write a connector, or do nothing at all. A test that only checked
// "it worked / it did not work" would let through exactly the defect being
// fixed.
//
// The simulated devices reproduce the behaviour observed on the test fleet: the
// ROOT answers 200 to an anonymous visitor in all three cases. That is what
// makes the distinction hard, and that is why it is tested.

// webDialer sends connections to the test server's address: it takes the place
// of the proxy tunnel in the real probe.
//
// `open` filters the accepted ports, so that an end-to-end test's port scan
// sees a credible device — a fake device whose six ports all answer would make
// it try SSH on an HTTP server.
type webDialer struct {
	addr string
	open map[int]bool
}

func (d webDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if d.open != nil {
		var _, portStr, splitErr = net.SplitHostPort(addr)
		if splitErr != nil {
			return nil, splitErr
		}
		var port, convErr = strconv.Atoi(portStr)
		if convErr != nil {
			return nil, convErr
		}
		if !d.open[port] {
			return nil, errors.New("port closed (device de test)")
		}
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, network, d.addr)
}

// proberTo builds a probe whose connections go to the test server.
func proberTo(srv *httptest.Server) *Prober {
	return &Prober{Dialer: webDialer{addr: strings.TrimPrefix(srv.URL, "http://")}}
}

// authParams splits a Digest Authorization header into key/value pairs.
func authParams(header string) map[string]string {
	var out = map[string]string{}
	var space = strings.IndexByte(header, ' ')
	if space < 0 {
		return out
	}
	var field string
	for _, field = range strings.Split(header[space+1:], ",") {
		var eq = strings.IndexByte(field, '=')
		if eq < 0 {
			continue
		}
		out[strings.ToLower(strings.TrimSpace(field[:eq]))] =
			strings.Trim(strings.TrimSpace(field[eq+1:]), `"`)
	}
	return out
}

func md5sum(s string) string {
	var sum = md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// digestOK checks the Digest response the way the firmware would: by
// RECOMPUTING it. Without that computation, the test would validate the header's
// shape and not the fact that the device would accept it.
func digestOK(header, method, realm, user, password string) bool {
	if !strings.HasPrefix(strings.ToLower(header), "digest ") {
		return false
	}
	var p = authParams(header)
	if p["username"] != user || p["realm"] != realm {
		return false
	}
	var ha1 = md5sum(user + ":" + realm + ":" + password)
	var ha2 = md5sum(method + ":" + p["uri"])
	var want string
	if p["qop"] == "auth" {
		want = md5sum(ha1 + ":" + p["nonce"] + ":" + p["nc"] + ":" + p["cnonce"] + ":auth:" + ha2)
	} else {
		want = md5sum(ha1 + ":" + p["nonce"] + ":" + ha2)
	}
	return p["response"] == want
}

// --- Outcome 1a: Digest accepted (the test fleet's MikroTik SwOS case) ---

func TestTryWebDigestAccepted(t *testing.T) {
	const realm = "CSS326-24G-2S+"
	// EMPTY password: that is this switch's factory setting, and an attempt that
	// required a non-empty secret would never reach it.
	var mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><title>MikroTik SwOS</title></html>"))
	})
	mux.HandleFunc("/sys.b", func(w http.ResponseWriter, r *http.Request) {
		if !digestOK(r.Header.Get("Authorization"), r.Method, realm, "admin", "") {
			w.Header().Set("WWW-Authenticate",
				`Digest realm="`+realm+`", qop="auth", nonce="244ead9d", stale=FALSE`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Body observed on the real device: SHORT, hexadecimal fields. `brd` does
		// carry "CSS326-24G-2S+", but encoded.
		_, _ = w.Write([]byte("{upt:0x00e55b73,mac:'b869f45f14a8'," +
			"brd:'4353533332362d3234472d32532b',ver:'322e3137'}"))
	})
	var srv = httptest.NewServer(mux)
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.36", []int{80},
		[]Credential{{ID: "c1", Name: "MikroTik factory admin", Kind: "http",
			Username: "admin", Secret: ""}},
		Matcher{Driver: "mikrotik-swos"})

	if att.outcome != WebAuthOK {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthOK)
	}
	if att.cred.ID != "c1" || att.cred.Name != "MikroTik factory admin" {
		t.Errorf("credential kept = %q/%q", att.cred.ID, att.cred.Name)
	}
	if !strings.Contains(att.detail, "Digest") || !strings.Contains(att.detail, realm) {
		t.Errorf("the detail must name the scheme and the realm: %q", att.detail)
	}
	if !strings.Contains(att.path, "/sys.b") {
		t.Errorf("path kept = %q, expected the data page and not the root", att.path)
	}
	// What the generic probe must NOT do: invent facts. This page's fields are
	// hexadecimal, and decoding them is platform knowledge — the swos connector
	// does it, not the probe. A model cobbled together here would be worth less
	// than nothing: it would give identification a false confidence.
	var host, serial, model = ExtractFacts(att.body)
	if host != "" || serial != "" || model != "" {
		t.Errorf("facts invented from a page we cannot read: %q/%q/%q",
			host, serial, model)
	}
}

// --- Outcome 1b: Basic accepted (the RouterOS REST API case) ---

func TestTryWebBasicAccepted(t *testing.T) {
	var mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><title>RouterOS</title></html>"))
	})
	mux.HandleFunc("/rest/system/resource", func(w http.ResponseWriter, r *http.Request) {
		var want = "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:s3cret"))
		if r.Header.Get("Authorization") != want {
			// RouterOS announces plain "Basic", with no realm: the case is real and it
			// must not prevent presenting credentials.
			w.Header().Set("WWW-Authenticate", "Basic")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"platform":"MikroTik","board-name":"CRS310-1G-5S-4S+",` +
			`"version":"7.16.2 (stable)"}`))
	})
	var srv = httptest.NewServer(mux)
	defer srv.Close()

	// EMPTY kind: the one-off credential typed in the add-by-IP UI. It must be
	// tried on the web, otherwise a web-only product line stays out of reach even
	// when the operator types the right password.
	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.54", []int{80},
		[]Credential{{Name: "typed manually", Kind: "",
			Username: "admin", Secret: "s3cret"}},
		Matcher{Driver: "mikrotik-routeros"})

	if att.outcome != WebAuthOK {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthOK)
	}
	if !strings.Contains(att.detail, "Basic") {
		t.Errorf("the detail must name the announced scheme: %q", att.detail)
	}
	if identifyFromWeb(att.body).Driver != "mikrotik-routeros" {
		t.Errorf("the authenticated page must be enough to recognise the platform")
	}
}

// --- Outcome 2: credentials refused ---

func TestTryWebRefused(t *testing.T) {
	var attempts int
	var mux = http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><title>MikroTik SwOS</title></html>"))
	})
	mux.HandleFunc("/sys.b", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			attempts++
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="CSS326-24G-2S+", qop="auth", nonce="abc"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	var srv = httptest.NewServer(mux)
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.36", []int{80},
		[]Credential{
			{ID: "c1", Name: "one", Kind: "http", Username: "admin", Secret: "wrong1"},
			{ID: "c2", Name: "two", Kind: "http", Username: "admin", Secret: "wrong2"},
			{ID: "c3", Name: "three", Kind: "http", Username: "admin", Secret: "wrong3"},
		},
		Matcher{Driver: "mikrotik-swos"})

	if att.outcome != WebAuthRefused {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthRefused)
	}
	if att.cred.ID != "" || att.cred.Name != "" {
		t.Errorf("no credential must be credited with a refusal: %q/%q",
			att.cred.ID, att.cred.Name)
	}
	// The anti-lockout bound applies to the web TOO: the device's AAA policy counts
	// failures from every origin together. Without it, a sweep with ten recorded
	// credentials would lock out the network team's account on every device it
	// met.
	if attempts != maxAuthFailures {
		t.Errorf("%d authenticated attempts, expected %d (anti-lockout bound)",
			attempts, maxAuthFailures)
	}
}

// --- Outcome 3: this interface does not authenticate at the HTTP level ---

func TestTryWebNoHTTPAuth(t *testing.T) {
	// Form-based UI, in the manner of an Aruba Instant On: 200 to the anonymous
	// visitor, session set in a cookie after a POST whose shape only a connector knows.
	var sawAuthHeader bool
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				sawAuthHeader = true
			}
			_, _ = w.Write([]byte("<html><title>Instant On 1930</title>" +
				"<form id=login></form></html>"))
		}))
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.12", []int{80, 443},
		[]Credential{{ID: "c1", Name: "admin", Kind: "http",
			Username: "admin", Secret: "s3cret"}},
		Matcher{Driver: "aruba-instant-on-1930"})

	if att.outcome != WebAuthNone {
		t.Fatalf("issue = %q (detail : %s), expected %q", att.outcome, att.detail, WebAuthNone)
	}
	// The most important point of the test: the password was presented to NOBODY.
	// Mistaking this for a refusal would send someone looking for a wrong password
	// for hours.
	if sawAuthHeader {
		t.Error("no credential must go out to an interface that does not ask for one")
	}
	if !strings.Contains(att.detail, "form") {
		t.Errorf("the detail must say where the limit is (connector / form): %q",
			att.detail)
	}
	if att.cred.ID != "" {
		t.Errorf("no credential must be credited: %q", att.cred.ID)
	}
}

// --- Outcome 4: no credential usable on the web, and we SAY SO ---

func TestTryWebNoUsableCredential(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(_ http.ResponseWriter, _ *http.Request) {
			t.Error("no request must go out without a credential to try")
		}))
	defer srv.Close()

	var att = proberTo(srv).tryWeb(context.Background(), "10.0.0.36", []int{80},
		[]Credential{
			{ID: "c1", Name: "ssh", Kind: "ssh", Username: "admin", Secret: "x"},
			{ID: "c2", Name: "telnet", Kind: "telnet", Username: "admin", Secret: "x"},
		},
		Matcher{Driver: "mikrotik-swos"})

	if att.outcome != WebAuthNotTried {
		t.Fatalf("issue = %q, expected %q", att.outcome, WebAuthNotTried)
	}
	// The absence is DECLARED: that is exactly the defect being fixed, a recorded
	// credential nobody knows is never tried.
	if !strings.Contains(att.detail, "http") {
		t.Errorf("the detail must say which kind is missing: %q", att.detail)
	}
}

// --- End to end: what web authentication changes in the result ---

// A device that opens ONLY port 80, whose home page says nothing about it
// (neither a `Server` header nor a title) and that demands HTTP authentication
// from the root. This is the case where the web path brings everything:
// without it, the device is only an address with an open port.
func TestProbeWebAuthLiftsIdentification(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var want = "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:s3cret"))
			if r.Header.Get("Authorization") != want {
				w.Header().Set("WWW-Authenticate", `Basic realm="switch"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("HP ProCurve Switch 2530-24G\nSerial Number : ZA123456\n"))
		}))
	defer srv.Close()

	var prober = &Prober{Dialer: webDialer{
		addr: strings.TrimPrefix(srv.URL, "http://"),
		open: map[int]bool{80: true},
	}}
	var res = prober.Probe(context.Background(), "10.0.0.9",
		[]Credential{{ID: "c1", Name: "fleet admin", Kind: "any",
			Username: "admin", Secret: "s3cret"}})

	if !res.LoginOK || res.WebAuth != WebAuthOK {
		t.Fatalf("login_ok=%v web_auth=%q (%s)", res.LoginOK, res.WebAuth, res.WebAuthDetail)
	}
	// "which credential worked" is the question the operator asks in front of a
	// probe result: it must have an answer.
	if res.CredentialName != "fleet admin" || res.CredentialID != "c1" {
		t.Errorf("credential kept = %q/%q", res.CredentialID, res.CredentialName)
	}
	if res.Driver != "hpe-procurve" {
		t.Errorf("driver = %q, the authenticated page should have served to identify it", res.Driver)
	}
	if res.Serial != "ZA123456" {
		t.Errorf("serial = %q", res.Serial)
	}
	// Confidence rises because there is NEW FACT, not merely because a password
	// was accepted.
	if res.Confidence != ConfDeviceSelfReport {
		t.Errorf("confidence = %d, expected %d (authenticated page of the device)",
			res.Confidence, ConfDeviceSelfReport)
	}
	if !strings.Contains(res.Evidence, "authenticated") {
		t.Errorf("the retained clue must say where it comes from: %q", res.Evidence)
	}
}

// The "telnet" kind is not tried: that must SHOW on the device where the
// question arises, not only in a documentation page.
func TestProbeDeclaresTelnetNotTried(t *testing.T) {
	var srv = httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html><title>switch</title></html>"))
		}))
	defer srv.Close()

	var prober = &Prober{Dialer: webDialer{
		addr: strings.TrimPrefix(srv.URL, "http://"),
		// 23 open: that is where the telnet credential could have served.
		open: map[int]bool{23: true, 80: true},
	}}
	var res = prober.Probe(context.Background(), "10.0.0.10",
		[]Credential{{ID: "c1", Name: "factory telnet", Kind: "telnet",
			Username: "admin", Secret: "x"}})

	if !strings.Contains(res.Notes, "telnet") || !strings.Contains(res.Notes, "NOT attempted") {
		t.Errorf("the notes must declare that telnet was not attempted: %q", res.Notes)
	}
}

// --- A credential's eligibility per transport ---

func TestCredFor(t *testing.T) {
	type row struct {
		kind      string
		ssh, web  bool
		telnetTry bool
	}
	var cases = []row{
		{kind: "ssh", ssh: true},
		{kind: "http", web: true},
		{kind: "telnet", telnetTry: true},
		// "any" and the empty kind apply to every transport TRIED.
		{kind: "any", ssh: true, web: true, telnetTry: true},
		{kind: "", ssh: true, web: true, telnetTry: true},
	}
	var c row
	for _, c = range cases {
		var cred = Credential{Kind: c.kind}
		if credFor(cred, "ssh") != c.ssh {
			t.Errorf("kind %q on ssh = %v, expected %v", c.kind, !c.ssh, c.ssh)
		}
		if credFor(cred, "http") != c.web {
			t.Errorf("kind %q on http = %v, expected %v", c.kind, !c.web, c.web)
		}
		if credFor(cred, "telnet") != c.telnetTry {
			t.Errorf("kind %q on telnet = %v, expected %v", c.kind, !c.telnetTry, c.telnetTry)
		}
	}
	if countKind([]Credential{{Kind: "telnet"}, {Kind: "any"}, {Kind: "telnet"}}, "telnet") != 2 {
		t.Error("countKind must count the DECLARED kinds, without credFor's widening")
	}
}

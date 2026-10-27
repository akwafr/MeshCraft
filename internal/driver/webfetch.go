package driver

import (
	"context"
	"crypto/md5" //nolint:gosec // imposed by the Digest protocol, cf. md5hex
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	neturl "net/url"
	"strings"
	"time"
)

// Web transport for connectors: an HTTP client whose connections leave from the
// same place as everything else — directly, or through a proxy's tunnel.
//
// The connector does not know where the connection leaves from: it receives a
// Dialer. That is what lets the same code drive a device reachable locally and
// a device behind a [[Proxy]] (docs/en/01-Architecture/Overview.md).

// WebSession is an HTTP session to a device's web interface: cookies kept,
// self-signed certificate accepted, redirects under control.
type WebSession struct {
	BaseURL string
	// MaxBody caps what is fetched. Zero = MaxBody. Recent interfaces serve
	// JavaScript bundles of several megabytes: truncating them loses precisely
	// the useful part, which is rarely at the start.
	MaxBody int
	client  *http.Client
	auth    Auth
}

// AuthMode: how the device asks to be authenticated AT THE HTTP LEVEL — i.e.
// through an `Authorization` header, and not through a login form that sets a
// cookie.
//
// The distinction is not cosmetic: a form-based UI (the Instant On) logs in
// ONCE and the session lives in the cookie; a header-based UI (SwOS, the
// RouterOS REST API) has no session at all and must present its credentials
// again on EVERY request. A connector that confuses the two opens a session
// per read, or believes it is logged in when it no longer is.
type AuthMode string

const (
	// AuthNone: nothing to present.
	AuthNone AuthMode = ""
	// AuthBasic: `Authorization: Basic`. That is what the RouterOS REST API
	// serves.
	AuthBasic AuthMode = "basic"
	// AuthDigest: `Authorization: Digest` (RFC 7616, MD5/qop=auth). That is what
	// SwOS serves, and the reason this machinery exists here: without it, its
	// data pages answer 401 and the device passes for mute.
	AuthDigest AuthMode = "digest"
	// AuthAuto: let the device say which of the two it wants, in its
	// `WWW-Authenticate`. Preferred when exploring an unknown interface —
	// assuming the scheme means confusing "wrong password" with "wrong
	// method".
	AuthAuto AuthMode = "auto"
)

// Auth carries the credentials presented at the HTTP level.
type Auth struct {
	Mode     AuthMode
	Username string
	Password string
}

// SetAuth arms the session's HTTP authentication. The credentials are only
// used in response to a 401: an interface that does not ask for them never
// sees them go by.
func (s *WebSession) SetAuth(mode AuthMode, username, password string) {
	s.auth = Auth{Mode: mode, Username: username, Password: password}
}

// NewWebSession builds the session. `insecure` is true in practice for every
// network device: they present a self-signed certificate, which is the norm
// and not an anomaly — the link's confidentiality is ensured by the tunnel.
func NewWebSession(base string, d Dialer, timeout time.Duration) *WebSession {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	var jar, _ = cookiejar.New(nil)
	var transport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		// A device that accepts only 5 sessions in total must not see the
		// connector open dozens of them: a single connection, reused.
		MaxIdleConns:        2,
		MaxConnsPerHost:     2,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if d != nil {
		transport.DialContext = d.DialContext
	}
	return &WebSession{
		BaseURL: strings.TrimSuffix(base, "/"),
		client: &http.Client{
			Timeout:   timeout,
			Jar:       jar,
			Transport: transport,
			// Redirects are followed, but bounded: a device UI that loops
			// must not make us loop.
			CheckRedirect: func(r *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

// Response is what a connector sees of a request.
type Response struct {
	Status  int                 `json:"status"`
	URL     string              `json:"url"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
	// Truncated says the body was cut: a truncated response must never pass
	// for a complete one.
	Truncated bool `json:"truncated"`
	// Binary: the body is not usable text (image, archive).
	Binary bool `json:"binary"`
}

// MaxBody caps what is fetched from a device page.
//
// 2 MiB and not 512 KiB: an Instant On 1930's bundle exceeds half a megabyte,
// and truncation cut precisely the end — the part that carries the useful
// functions. A cap that silently amputates the part being looked for protects
// against nothing: it leads to concluding something is absent.
const MaxBody = 2 << 20

// Get reads a URL (absolute, or relative to BaseURL). Read-only: nothing is
// modified on the device, which makes exploring an unknown interface
// risk-free.
func (s *WebSession) Get(ctx context.Context, path string) (*Response, error) {
	return s.do(ctx, http.MethodGet, path, "", nil)
}

// PostForm sends a form. Reserved for connectors: it is a write.
func (s *WebSession) PostForm(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body,
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
}

// PostText sends a `text/plain` body. Reserved for connectors: it is a write.
//
// The content type is not a cosmetic detail on embedded interfaces: SwOS is
// what imposed this variant, its interface announcing `text/plain` for all of
// its writes. The body bytes are exactly those of `PostForm` — neither call
// re-encodes anything — but a firmware that has only ever seen one content
// type is entitled to refuse the other, and this range's refusals are SILENT:
// a 200 with no effect. Better, then, to send what the device expects than to
// discover in the field that a write declared itself successful without
// changing anything.
func (s *WebSession) PostText(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body,
		map[string]string{"Content-Type": "text/plain"})
}

// PostAJAX sends a body as-is with the headers a jQuery `$.ajax` produces:
// form content type, and `X-Requested-With`. Many embedded UIs accept their own
// writes only in this form — it is the only one they have ever seen.
func (s *WebSession) PostAJAX(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body, map[string]string{
		"Content-Type":     "application/x-www-form-urlencoded; charset=UTF-8",
		"X-Requested-With": "XMLHttpRequest",
		"Accept":           "application/xml, text/xml, */*; q=0.01",
	})
}

// Put sends a JSON body as a PUT. Reserved for connectors: it is a write.
func (s *WebSession) Put(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPut, path, body,
		map[string]string{"Content-Type": "application/json"})
}

// PostJSON sends a JSON body. Reserved for connectors: it is a write.
func (s *WebSession) PostJSON(ctx context.Context, path, body string) (*Response, error) {
	return s.do(ctx, http.MethodPost, path, body,
		map[string]string{"Content-Type": "application/json"})
}

func (s *WebSession) do(ctx context.Context, method, path, body string,
	headers map[string]string) (*Response, error) {
	var url = path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		url = s.BaseURL + path
	}
	var resp, doErr = s.send(ctx, method, url, body, headers, "")
	if doErr != nil {
		return nil, doErr
	}
	// 401: the device says HOW it wants to be authenticated. We do not guess
	// it, we read it — then replay the same request once, and only once. A
	// refused password must stay a refusal, not a loop of attempts that would
	// end up locking the account.
	if resp.StatusCode == http.StatusUnauthorized && s.auth.Mode != AuthNone {
		var challenge = resp.Header.Get("WWW-Authenticate")
		resp.Body.Close() //nolint:errcheck
		var credential, credErr = s.authorization(method, url, challenge)
		if credErr != nil {
			return nil, credErr
		}
		resp, doErr = s.send(ctx, method, url, body, headers, credential)
		if doErr != nil {
			return nil, doErr
		}
	}
	defer resp.Body.Close() //nolint:errcheck

	var err error

	var limit = s.MaxBody
	if limit <= 0 {
		limit = MaxBody
	}
	var raw []byte
	raw, err = io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	var out = &Response{
		Status: resp.StatusCode, URL: resp.Request.URL.String(),
		Headers: resp.Header,
	}
	if len(raw) > limit {
		raw = raw[:limit]
		out.Truncated = true
	}
	out.Binary = !looksTextual(raw)
	if !out.Binary {
		out.Body = string(raw)
	}
	return out, nil
}

// send issues a request and returns the raw response, body NOT read: do is what
// decides to read it or to replay the request authenticated.
func (s *WebSession) send(ctx context.Context, method, url, body string,
	headers map[string]string, credential string) (*http.Response, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	var req, err = http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	// An honest User-Agent: the administrator reading their device's logs
	// must be able to identify what is querying it.
	req.Header.Set("User-Agent", "meshcraft")
	req.Header.Set("Accept", "*/*")
	var k, v string
	for k, v = range headers {
		req.Header.Set(k, v)
	}
	if credential != "" {
		req.Header.Set("Authorization", credential)
	}
	return s.client.Do(req)
}

// authorization builds the `Authorization` header the challenge asks for.
//
// The scheme comes from the device, not from us: `auto` reads it in the
// challenge, and an explicit mode is kept only if it matches what is offered.
// Presenting Basic to something that asks for Digest yields a second 401,
// which would read as a wrong password — the costliest error to diagnose on a
// device whose console we do not have.
func (s *WebSession) authorization(method, url, challenge string) (string, error) {
	var scheme = strings.ToLower(strings.Fields(strings.TrimSpace(challenge) + " ")[0])
	switch {
	case scheme == "digest" && s.auth.Mode != AuthBasic:
		return s.digestCredential(method, url, challenge)
	case scheme == "basic" && s.auth.Mode != AuthDigest:
		return "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(s.auth.Username+":"+s.auth.Password)), nil
	case challenge == "" && s.auth.Mode == AuthBasic:
		// No readable challenge but Basic explicitly requested: it is the only
		// scheme that can be presented without knowing anything about the device.
		return "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(s.auth.Username+":"+s.auth.Password)), nil
	}
	return "", fmt.Errorf("HTTP authentication %q requested by the device, "+
		"not handled by this connector (challenge: %q)", scheme, challenge)
}

// digestCredential computes the MD5 Digest response with qop=auth (RFC 7616
// §3.4), the only variant the embedded interfaces met so far serve.
//
// `cnonce` is drawn at random on every call and `nc` stays at 1: a nonce is
// never reused from one request to the next. That is what makes every request
// independent — a connector that incremented a shared counter would be wrong
// as soon as it read two pages in parallel.
func (s *WebSession) digestCredential(method, url, challenge string) (string, error) {
	return DigestAuthorization(method, url, challenge, s.auth.Username, s.auth.Password)
}

// DigestAuthorization is [WebSession.digestCredential] with the credentials
// passed in rather than held in a session.
//
// Exported for the web-UI relay (cf. internal/api/console_webui.go), which
// presents Digest on a device's behalf without ever opening a WebSession — it
// forwards a browser's requests, and a browser is not a connector. Keeping ONE
// implementation matters more than the awkward signature: two copies of a digest
// computation diverge on the qop-less variant first, and that variant is the one
// that only old firmware asks for, i.e. the one nobody notices is broken.
func DigestAuthorization(method, url, challenge, username, password string) (string, error) {
	var p = parseChallenge(challenge)
	var realm, nonce = p["realm"], p["nonce"]
	if nonce == "" {
		return "", fmt.Errorf("Digest challenge without a nonce: %q", challenge)
	}
	var algorithm = strings.ToUpper(p["algorithm"])
	if algorithm != "" && algorithm != "MD5" {
		return "", fmt.Errorf("Digest %s not handled (only MD5 is)", algorithm)
	}
	// The digest URI is the path, not the absolute URL: that is what the
	// device saw go by on its request line.
	var uri = url
	var parsed, parseErr = neturl.Parse(url)
	if parseErr == nil {
		uri = parsed.RequestURI()
	}

	var ha1 = md5hex(username + ":" + realm + ":" + password)
	var ha2 = md5hex(method + ":" + uri)

	var raw = make([]byte, 8)
	var _, randErr = rand.Read(raw)
	if randErr != nil {
		return "", randErr
	}
	var cnonce = hex.EncodeToString(raw)

	var qop = ""
	var field string
	for _, field = range strings.Split(p["qop"], ",") {
		if strings.TrimSpace(field) == "auth" {
			qop = "auth"
		}
	}

	var response string
	var parts = []string{
		`username="` + username + `"`,
		`realm="` + realm + `"`,
		`nonce="` + nonce + `"`,
		`uri="` + uri + `"`,
	}
	if qop == "auth" {
		response = md5hex(ha1 + ":" + nonce + ":00000001:" + cnonce + ":auth:" + ha2)
		parts = append(parts, "qop=auth", "nc=00000001", `cnonce="`+cnonce+`"`)
	} else {
		// "Legacy" Digest, without qop: kept because old firmwares announce
		// nothing else, and a refusal here would send the operator back to an
		// interface that, for its part, works in their browser.
		response = md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	parts = append(parts, `response="`+response+`"`)
	if p["opaque"] != "" {
		parts = append(parts, `opaque="`+p["opaque"]+`"`)
	}
	return "Digest " + strings.Join(parts, ", "), nil
}

// parseChallenge splits a `WWW-Authenticate` into key/value pairs, quotes
// removed. Tolerant out of necessity: embedded firmwares each punctuate their
// challenge their own way.
func parseChallenge(challenge string) map[string]string {
	var out = map[string]string{}
	var body = strings.TrimSpace(challenge)
	var space = strings.IndexByte(body, ' ')
	if space < 0 {
		return out
	}
	body = body[space+1:]
	var field string
	for _, field = range strings.Split(body, ",") {
		var eq = strings.IndexByte(field, '=')
		if eq < 0 {
			continue
		}
		var key = strings.ToLower(strings.TrimSpace(field[:eq]))
		var value = strings.Trim(strings.TrimSpace(field[eq+1:]), `"`)
		out[key] = value
	}
	return out
}

// md5hex: MD5 imposed by the Digest protocol, and used nowhere else in
// meshcraft. It is not a security choice — it is the only digest these
// interfaces accept, and the link itself is protected by the tunnel.
func md5hex(s string) string {
	var sum = md5.Sum([]byte(s)) //nolint:gosec
	return hex.EncodeToString(sum[:])
}

// looksTextual: a NUL byte in the first kilobytes is enough to conclude.
func looksTextual(b []byte) bool {
	var limit = len(b)
	if limit > 2048 {
		limit = 2048
	}
	var i int
	for i = 0; i < limit; i++ {
		if b[i] == 0 {
			return false
		}
	}
	return true
}

package instanton

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Classic Instant On web UI administration session (port 443), as opposed to
// provisioning UI on port 4443.
//
// These switches have no SSH, Telnet, or serial port: replaying their web UI is the
// ONLY write path. We therefore behave like a browser — read the form the device
// serves and send it back complete — rather than posting guessed fields rejected
// without message.
//
// Everything the session does is recorded in a transcript. On a device that can be
// made unreachable by mistake, knowing AFTER THE FACT what was sent and what answered
// is not developer comfort: it is the only way to understand failure without returning
// on site.

// Step, transcript step type, is now the contract type (`driver.Step`), by ALIAS: cf.
// contract.go. The `Steps` field below did not change — that is the whole point of the alias.

// Admin is an authenticated (or authenticating) session.
type Admin struct {
	web  *driver.WebSession
	host string
	// Steps is the transcript, in order. Rendered by the `Transcript` method of
	// the contract, which bears this name precisely so that this FIELD can keep
	// its own name (cf. contract.go) — this package's tests and the API layer
	// read it.
	Steps []Step
	// page is the URL of the last page read: form actions there
	// are relative, under a session prefix `/csNNNNNNNN/`.
	page string
	// User is the account actually used to enter.
	User string
	// accepted is the candidate that the device ACCEPTED, as supplied.
	//
	// Memorized here because this is the only place that knows it firsthand:
	// `Login` sets it the moment the device says yes. Before, the caller
	// reconstructed it by searching the transcript for a step whose wording
	// starts with "logged in as" — thus depending on a sentence written for a
	// human, which someone would one day rephrase. That day, identity
	// application broke silently (cf. Admin.Accepted in contract.go).
	accepted Candidate
	// prefix is the port-naming prefix on THIS device's faceplate
	// ("GE"), captured at construction.
	//
	// On the session, not passed on each call: two calls on the same session
	// given two different prefixes would write to different ports while
	// believing they refer to the same ones. When empty, `portPrefix` falls
	// back to the range default rather than writing blindly.
	prefix string
	// notedEncryption avoids repeating the same transcript line each time.
	notedEncryption bool
	// State is the state of the open session: “ok”, “initial” (password
	// to be defined), “expired”… This is what decides what happens next.
	State string
}

// defaultPortPrefix is the faceplate prefix for the whole 1930 range: its
// ports are named "GE1" through "GE52", optical cages included (they continue
// the copper numbering).
//
// Serves as a default when the caller has said nothing — which happens for a
// device not yet in the inventory. This mirrors what the API layer does
// (`portPrefix`), and the value lives here so the connector never writes
// blindly: without a prefix, "GE12" would not translate to any port index,
// and a wrong index means writing to the wrong port — i.e., cutting off a
// customer.
const defaultPortPrefix = "GE"

// portPrefix returns the prefix to use when translating meshcraft port names
// into device interface numbers.
func (a *Admin) portPrefix() string {
	if a.prefix == "" {
		return defaultPortPrefix
	}
	return a.prefix
}

func (a *Admin) step(ok bool, what string, detail ...string) {
	a.Steps = append(a.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// OpenAdmin opens a session to the classic interface.
//
// WITHOUT A PORT PREFIX: a session built here falls back to the range default
// ("GE", cf. portPrefix), which is correct for all known models — their
// faceplates are numbered "GE1" to "GE52", and this same package describes
// them that way (see portGroups). A caller holding a prefix raised from the
// inventory must go through [Driver1930.Authenticate], which carries it in
// its [driver.Target]: that is the only path that guarantees reads and writes
// refer to the same ports as the inventory.
func OpenAdmin(host string, d driver.Dialer) *Admin {
	return &Admin{web: driver.NewWebSession("https://"+host, d, 0), host: host}
}

// Candidate, the type of a secret to try, is now the contract's
// (`driver.Candidate`), by ALIAS: cf. contract.go. Its fields are the same,
// and the order still comes from the caller (cf. store.SecretsForDevice) —
// the connector doesn't decide what a piece of gear probably carries, it
// applies it.

// Login tries the credentials IN ORDER and stops at the first one that
// succeeds.
//
// The order is the caller's. Trying the most recent password first would
// fail on any device that has fallen behind — and on an interface that
// accepts only 5 sessions, attempts are expensive.
//
// The mechanism is NOT a form POST, contrary to what the page's `<form>`
// suggests: it is never submitted. The button calls a `formSubmit()` that
// queries `/device/wcd?{EncryptionSetting}`, encrypts the credentials with
// the device's RSA key, and does a GET on
// `system.xml?action=login&cred=<hex>`. Posting the form as-is returns a
// 404 — this is the first trap in this range, and it costs an hour for
// anyone who does not read the page's JavaScript.
func (a *Admin) Login(ctx context.Context, cands []Candidate) error {
	var resp, err = a.web.Get(ctx, "/")
	if err != nil {
		a.step(false, "opening web UI", err.Error())
		return err
	}
	a.page = resp.URL
	a.step(true, "home page read", fmt.Sprintf("HTTP %d — %s", resp.Status, resp.URL))

	// The login URL is relative to the DIRECTORY of the login page
	// (`/csNNNNNNNN/hpe/config/`), not the root: the session prefix is part
	// of the expected path.
	var loginBase = dirOf(a.page) + "system.xml?action=login&"

	var c Candidate
	for _, c = range cands {
		// The login token is reread BEFORE EACH ATTEMPT: the device renews
		// it, and reusing an old one makes decryption fail (code 12) — which
		// looks like a key problem when it is actually replay protection.
		var enc, encErr = a.encryptionSetting(ctx)
		if encErr != nil {
			return encErr
		}
		var query string
		query, err = enc.loginQuery(c.Username, c.Secret)
		if err != nil {
			a.step(false, "encrypting credentials", err.Error())
			return err
		}
		var r *driver.Response
		r, err = a.web.Get(ctx, loginBase+query)
		if err != nil {
			a.step(false, fmt.Sprintf("attempt %q (%s)", c.Username, c.Why), err.Error())
			continue
		}
		var code = xmlField(r.Body, "statusCode")
		if authenticated(code) {
			a.page = r.URL
			a.User = c.Username
			// The accepted candidate is captured HERE, the only place that
			// knows it firsthand. This is what feeds `Accepted()`, and what
			// spares the caller from reconstructing it by re-reading the
			// transcript's wording.
			a.accepted = c
			a.State = loginState(code)
			a.step(true, fmt.Sprintf("logged in as %q", c.Username),
				c.Why+" — code "+code+" : "+loginError(code))
			return nil
		}
		a.step(false, fmt.Sprintf("refused for %q (%s)", c.Username, c.Why),
			fmt.Sprintf("HTTP %d — code %q : %s", r.Status, code, loginError(code)))
		// "Too many sessions" is not a bad password: retrying would only
		// worsen the saturation of an interface that accepts only five.
		if code == "5" {
			return fmt.Errorf("%s", loginError(code))
		}
	}
	return fmt.Errorf("no credential accepted (%d attempt(s))", len(cands))
}

// encryptionSetting is what the device says about its own credential
// encryption.
type encryptionSetting struct {
	Enabled bool
	Key     *rsa.PublicKey
	Token   string
}

// pathEncryption is served AT THE ROOT, outside of the session prefix — and without
// authentication, since it is used precisely to authenticate.
const pathEncryption = "/device/wcd?{EncryptionSetting}"

func (a *Admin) encryptionSetting(ctx context.Context) (encryptionSetting, error) {
	var out encryptionSetting
	var resp, err = a.web.Get(ctx, pathEncryption)
	if err != nil {
		a.step(false, "reading encryption setting", err.Error())
		return out, err
	}
	if xmlField(resp.Body, "passwEncryptEnable") != "1" {
		a.step(true, "credentials in clear", "device does not enable encryption")
		return out, nil
	}
	out.Enabled = true
	out.Token = xmlField(resp.Body, "loginToken")

	out.Key, err = parseRSAPublicKey(xmlField(resp.Body, "rsaPublicKey"))
	if err != nil {
		a.step(false, "device public key unreadable", err.Error())
		return out, err
	}
	// One transcript line only, even if token is re-read each attempt: repeating the
	// same information would drown lines that matter.
	if !a.notedEncryption {
		a.notedEncryption = true
		a.step(true, "encrypted credentials",
			fmt.Sprintf("RSA %d bits, token renewed each attempt", out.Key.N.BitLen()))
	}
	return out, nil
}

// loginQuery constructs the connection query, encrypted or not depending on what
// the equipment requires.
func (e encryptionSetting) loginQuery(user, secret string) (string, error) {
	var payload = "user=" + escapeURIComponent(user) +
		"&password=" + escapeURIComponent(secret) + "&ssd=true"
	if !e.Enabled {
		return payload + "&", nil
	}
	// OBSERVED on real hardware (1930, firmware 3.4.0.0): when encryption
	// is active, an EMPTY password produces a code 12 ("encryption
	// problem"), whether encrypted or sent in plain text. The factory
	// account with no password is therefore unusable through this door: a
	// device fresh out of the box is handled by the provisioning UI (port
	// 4443), which exists exactly for that.
	//
	// The field order and the trailing ampersand reproduce EXACTLY what the
	// interface encrypts: the decrypted message is compared as-is.
	var clear = payload + "&token=" + e.Token + "&"
	var ct, err = rsa.EncryptPKCS1v15(rand.Reader, e.Key, []byte(clear))
	if err != nil {
		return "", err
	}
	return "cred=" + hex.EncodeToString(ct), nil
}

// parseRSAPublicKey reads the key in PKCS#1 format used by the device.
func parseRSAPublicKey(pemText string) (*rsa.PublicKey, error) {
	var block, _ = pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, fmt.Errorf("public key missing or malformed")
	}
	var key, err = x509.ParsePKCS1PublicKey(block.Bytes)
	if err == nil {
		return key, nil
	}
	// Some revisions serve a PKIX key under the same PKCS#1 header.
	var generic, pkixErr = x509.ParsePKIXPublicKey(block.Bytes)
	if pkixErr != nil {
		return nil, err
	}
	var rsaKey, ok = generic.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("unexpected public key type")
	}
	return rsaKey, nil
}

// escapeURIComponent reproduces `encodeURIComponent`. `url.QueryEscape` is
// not suitable: it encodes the space as `+`, which the device would read
// back as a plus sign — a password would be silently altered.
func escapeURIComponent(s string) string {
	const safe = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()"
	var b strings.Builder
	var i int
	for i = 0; i < len(s); i++ {
		if strings.IndexByte(safe, s[i]) >= 0 {
			b.WriteByte(s[i])
			continue
		}
		fmt.Fprintf(&b, "%%%02X", s[i])
	}
	return b.String()
}

// authenticated reports whether a status code means an open session.
//
// Major pitfall of this range, discovered the hard way: the login page
// displays, for code 12, the message "Login failed due to Device password
// encryption problems", while its own `stateChange()` treats that same 12
// as a SUCCESSFUL CONNECTION, and the device returns in clear text
// `statusString = "Default user Login"`. Trusting the UI's error message
// would have pointed to an encryption failure exactly where the factory
// account had just opened a session.
//
// The codes equivalent to an open session are those after which
// `stateChange()` navigates to `home.htm`.
func authenticated(code string) bool {
	switch code {
	case "0", "9", "10", "12", "13", "14":
		return true
	}
	return false
}

// loginState names the session state with the words of the device.
func loginState(code string) string {
	switch code {
	case "0":
		return "ok"
	case "10":
		return "simple"
	case "12":
		return "initial"
	case "14":
		return "initComp"
	case "9":
		return "expired"
	case "13":
		return "dueExpire"
	}
	return code
}

// loginError translates the equipment codes. The labels of the success codes
// come from `stateChange()`, those from failures of `DisplayLoginError()` — the
// two tables of the login page, which do not say the same thing about the same
// code (cf. authenticated).
func loginError(code string) string {
	switch code {
	case "":
		return "response without status code — unexpected page"
	case "0":
		return "connection established"
	case "1":
		return "username missing"
	case "4":
		return "invalid username or password"
	case "5":
		return "too many open sessions on device (5 maximum)"
	case "6":
		return "failure attributed by device to client workstation configuration"
	case "7":
		return "no response from device within timeout"
	case "9":
		return "password expired: device requires a new one"
	case "10":
		return "connection established, password deemed too simple by device"
	case "11":
		return "password does not meet device complexity rules"
	case "12":
		return "first access with factory account: a password must be set"
	case "13":
		return "connection established, password near expiration"
	case "14":
		return "first access, complexity required"
	}
	return "unknown code"
}

// xmlField extracts a field from the device's XML responses. Strict decoding
// would be cleaner, but these answers mix XML and HTML fragments depending on
// the page: targeted extraction holds up where a full parser would give up
// on the entire document.
func xmlField(body, name string) string {
	var re = regexp.MustCompile(`(?is)<` + regexp.QuoteMeta(name) + `>(.*?)</` +
		regexp.QuoteMeta(name) + `>`)
	var m = re.FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// dirOf returns the directory of a URL, including the trailing slash.
func dirOf(pageURL string) string {
	var u, err = url.Parse(pageURL)
	if err != nil {
		return "/"
	}
	var i = strings.LastIndex(u.Path, "/")
	if i < 0 {
		return "/"
	}
	return u.Path[:i+1]
}

// sectionAdminUsers is the configuration section that carries the
// administration accounts. It is READ and WRITTEN under this name.
//
// An earlier version of this connector wrote to a section named
// `UpdatePassword`: it does not exist. When queried, this range responds
// `ActionStatus/statusCode = -1` — observed on a 1930 JL685A (firmware
// 3.4.0.0). This was the root cause of the provisioning failure.
const sectionAdminUsers = "AdminUserSetting"

// accessLevelRW is the "read/write" privilege level. The value is
// observed on the device itself: `ConnectedUserList` gives
// `<level>15</level>` for the administration account's session.
const accessLevelRW = "15"

// SetPassword sets the administration account: username AND password.
//
// This is exactly the gesture of the device's first-access form, as
// observed in its own bundle (`hpe/js/out/mainpage.js`):
//
//	post = {AdminUserSetting:{set:{AdminUserList:{AdminUserEntry:{
//	         userName: …, password: '$'+…, accessLevel: _MODE_RW}}}}}
//
// This range's web interface is Marvell/RADLAN's "wcd" framework, shared
// with the Cisco CBS350 — for which Cisco publishes an emulator, the
// source of the three conventions below. ALL of them must be respected,
// otherwise the device accepts the request without doing anything:
//
//   - the POST URL carries the list of sections being written
//     (`wcd?{AdminUserSetting}`). This is the ONLY way the dispatcher knows
//     what to process: without this list, it responds 200 with an EMPTY
//     body and no `ActionStatus`;
//   - the body is wrapped in `<?xml …?><DeviceConfiguration>`;
//   - the action is an ATTRIBUTE of the section element — the dispatcher
//     literally selects `//*[@action]`. Making it a child element
//     (`<AdminUserSetting><set>…`) selects nothing, so it does nothing.
//
// The password is prefixed with a `$` and sent IN CLEAR TEXT, exactly as
// this form does. RSA encryption with a `crypto_!` prefix exists on this
// range, but it is specific to the account management page; applying it
// here would fail silently. Confidentiality therefore relies on TLS and
// the proxy tunnel.
//
// `oldSecret` is not sent: the device does not require it, the open
// session is proof enough. The parameter is kept so the caller does not
// have to distinguish this range from the others.
//
// The device closes the session immediately afterward (its own interface
// follows up with a `gracefulLogout`): this is normal, and this is why
// verification is done via a NEW connection.
//
// WARNING: writing only affects the CURRENT configuration. Without an
// explicit save, a reboot returns the device to its factory state — the
// `admin` account with no password (cf. Capabilities.Commit).
func (a *Admin) SetPassword(ctx context.Context, user, oldSecret, newSecret string) error {
	var err = a.writeSection(ctx, sectionAdminUsers, "set",
		"<AdminUserList>"+adminUserEntry(user, newSecret)+"</AdminUserList>",
		fmt.Sprintf("account %q set on device", user))
	if err != nil {
		return err
	}

	// The device's own interface follows up with `saveConf()`: we do the
	// same, otherwise the password would not survive the first reboot. A
	// save failure does NOT undo the write — the password is indeed in
	// place, and claiming otherwise would be false. It is recorded in the
	// transcript, which is the proper channel for what the operator needs
	// to know.
	a.SaveConfig(ctx) //nolint:errcheck // logged in the transcript, non-blocking
	return nil
}

// adminUserEntry renders an account entry. The password is prefixed with a
// `$`: this is the marker by which the device recognizes a CLEAR-TEXT
// secret. It accepts two others, noted in its bundle and not to be
// confused with this one — `#` announces an already-computed hash (this is
// the form in which the device STORES the passwords it keeps), and
// `crypto_!` an RSA-encrypted secret. The marker sits OUTSIDE the payload:
// it is part of the protocol, not of the secret.
func adminUserEntry(user, secret string) string {
	return "<AdminUserEntry>" +
		"<userName>" + xmlEscape(user) + "</userName>" +
		"<password>$" + xmlEscape(secret) + "</password>" +
		"<accessLevel>" + accessLevelRW + "</accessLevel>" +
		"</AdminUserEntry>"
}

// writeSection sends ONE `wcd` write and checks what the device says about it.
// The three conventions described on SetPassword are applied here, in one
// place: this is the only way to avoid half-reintroducing them elsewhere.
func (a *Admin) writeSection(ctx context.Context, section, action, inner, done string) error {
	var body = "<?xml version='1.0' encoding='utf-8'?>" +
		"<DeviceConfiguration>" +
		"<" + section + ` action="` + action + `">` + inner +
		"</" + section + ">" +
		"</DeviceConfiguration>"

	// Content type `x-www-form-urlencoded`, not `application/xml`: this is
	// what the interface's `$.ajax` produces, since it does not specify a
	// contentType and therefore inherits jQuery's default.
	var url = a.wcdURL() + "?{" + section + "}"
	var resp, err = a.web.PostAJAX(ctx, url, body)
	if err != nil {
		a.step(false, "sending "+section+" ("+action+")", err.Error())
		return err
	}
	var code = xmlField(resp.Body, "statusCode")
	if code != "0" {
		a.step(false, section+" ("+action+") refused by device",
			fmt.Sprintf("POST %s → HTTP %d, code %q %s | response: %s",
				url, resp.Status, code, xmlField(resp.Body, "statusString"),
				excerpt(resp.Body, 300)))
		if code == "" {
			return fmt.Errorf("device responded without status code (%d bytes): "+
				"request not recognized, nothing was modified", len(resp.Body))
		}
		return fmt.Errorf("device refused change (code %s %s)",
			code, xmlField(resp.Body, "statusString"))
	}
	a.step(true, done, fmt.Sprintf("HTTP %d — code 0", resp.Status))
	return nil
}

// reEntrySplit cuts the section into one chunk per account. Splitting rather
// than matching a whole `<AdminUserEntry>…</AdminUserEntry>` block is
// deliberate: this range answers with a mix of XML and fragments depending on
// the sections asked for, and a parser that required the CLOSING tag would
// return an empty list on a response that names accounts perfectly well — which
// [Admin.EnsureAccount] would then read as "nothing exists yet" and try to
// `add` an account that is already there.
var reEntrySplit = regexp.MustCompile(`(?i)<AdminUserEntry>`)

// reUserName and reAccessLevel read the two fields of one such chunk. The level
// is in the SAME section as the name — the very field [adminUserEntry] writes —
// so carrying it costs nothing beyond this second FindStringSubmatch.
var (
	reUserName    = regexp.MustCompile(`(?is)<userName>(.*?)</userName>`)
	reAccessLevel = regexp.MustCompile(`(?is)<accessLevel>(.*?)</accessLevel>`)
)

// Accounts lists the administration accounts present on the device, with the
// access level each one carries.
//
// This is a READ, and it is what makes account operations safe: knowing
// what already exists before writing avoids adding an account that is
// already there (which the device would refuse) and mistakenly believing
// an account was deleted when it was not.
//
// THE LEVEL COMES FREE, and this connector used to throw it away: the section
// this GET already returns carries `<accessLevel>` beside every `<userName>`
// (15 is read/write on this range — cf. [accessLevelRW], observed as
// `<level>15</level>` on the administration session itself). One HTTP request,
// same as before.
//
// An entry whose `<accessLevel>` is absent or unreadable comes back
// [driver.PrivilegeUnknown]: this firmware has been seen answering with
// fragments, and "the section did not carry the field" must not be reported
// as level 0.
func (a *Admin) Accounts(ctx context.Context) ([]driver.Account, error) {
	var url = a.wcdURL() + "?{" + sectionAdminUsers + "}"
	var resp, err = a.web.Get(ctx, url)
	if err != nil {
		a.step(false, "reading admin accounts", err.Error())
		return nil, err
	}
	var out = []driver.Account{}
	var chunks = reEntrySplit.Split(resp.Body, -1)
	var i int
	// The first chunk is whatever came BEFORE the first entry: never an account.
	for i = 1; i < len(chunks); i++ {
		var name = reUserName.FindStringSubmatch(chunks[i])
		if name == nil {
			continue
		}
		out = append(out, driver.AccountAtLevel(strings.TrimSpace(name[1]),
			parseAccessLevel(chunks[i]), sectionAdminUsers))
	}
	var told = make([]string, 0, len(out))
	for i = range out {
		told = append(told, out[i].Describe())
	}
	a.step(true, "admin accounts read",
		fmt.Sprintf("HTTP %d — %d account(s): %s", resp.Status, len(out),
			strings.Join(told, ", ")))
	return out, nil
}

// parseAccessLevel reads `<accessLevel>` out of one entry chunk, and answers
// [driver.PrivilegeUnknown] when the field is missing or not a number.
func parseAccessLevel(chunk string) int {
	var m = reAccessLevel.FindStringSubmatch(chunk)
	if m == nil {
		return driver.PrivilegeUnknown
	}
	var n, err = strconv.Atoi(strings.TrimSpace(m[1]))
	if err != nil {
		return driver.PrivilegeUnknown
	}
	return n
}

// EnsureAccount ensures that an account named `user` exists and has `secret`.
//
// The device distinguishes `add` (create) from `set` (modify) and refuses
// one in place of the other: we therefore read the existing accounts to
// choose. This is also what makes the operation replayable — a write
// interrupted and then resumed does not run into a half-created account.
func (a *Admin) EnsureAccount(ctx context.Context, user, secret string) error {
	var present, err = a.Accounts(ctx)
	if err != nil {
		return err
	}
	var action = "add"
	if driver.HasAccount(present, user) {
		action = "set"
	}
	var what = fmt.Sprintf("account %q created on device", user)
	if action == "set" {
		what = fmt.Sprintf("account %q updated on device", user)
	}
	return a.writeSection(ctx, sectionAdminUsers, action,
		"<AdminUserList>"+adminUserEntry(user, secret)+"</AdminUserList>", what)
}

// DeleteAccount removes an administration account.
//
// DANGEROUS by nature: this range has no SSH, Telnet, or serial port. If
// the last usable account disappears, the only recourse is the
// factory-reset button, meaning a site visit. The caller MUST have proven
// — by opening a real session, not by reading a return code — that
// another account works before calling this.
//
// # THREE refusals, and the last two arrived on 2026-08-12
//
// Until then this method carried the CURRENT-SESSION refusal and nothing else,
// on the range where a lockout is least repairable in the whole tree. Worse, the
// gap was invisible: TODO.md listed `admin.go:605` among the connectors holding
// a last-account guard, and that line is the [Admin.Accounts] call inside
// [Admin.EnsureAccount] choosing `add` from `set`. The guard below is the one
// `ubiquiti/edgeswitch` landed the same day (`accounts.go`, `deleteAccountFrom`),
// ported — and it has to be at least as strict here, because that range at least
// keeps a front-panel reset that a technician reaches over Ethernet afterwards;
// this one answers on HTTPS only.
//
// THE PREDICATE IS "WHAT WOULD REMAIN", not "how many are there":
// [driver.AccountsExcept] filters out the account about to go and the deletion
// is refused when nothing survives it. That is `len(have) <= 1` made exact, and
// it also lets through the delete of a name the device does not enumerate,
// which removes nothing.
//
// # WHAT DIFFERS ON THIS PLATFORM: an empty read is NOT an empty device
//
// On the EdgeSwitch the account list comes from a CLI, and a read that fails
// fails loudly — `readAccountsFrom` returns an error only when both of its
// commands died, so "no accounts" and "could not read" are distinguishable
// facts. HERE THEY ARE NOT, and that is the one thing this port could not copy:
// [Admin.Accounts] is a `wcd` GET whose body is scanned for `<AdminUserEntry>`,
// [driver.WebSession.Get] does not treat a non-200 as an error, and this
// firmware answers a section it does not recognise with HTTP 200 and
// `statusCode = -1` (the `UpdatePassword` finding, see [SetPassword]) — an
// answer that parses to ZERO accounts and NO error. A bounced-to-login-page
// response does the same. So `len(have) == 0` had to become a refusal in its own
// right, and it is a sound one for a reason peculiar to a session-based read:
// THIS SESSION IS OPEN. The device therefore holds at least the account it was
// opened under, so a section naming none did not answer with the account list.
// An empty list here is an unreadable one.
//
// The same asymmetry covers a TRUNCATED answer, which this firmware also
// produces (it mixes XML with fragments — see [reEntrySplit]): a short list can
// only make the survivor count too LOW, so the guard errs into refusing a
// deletion that was fine. That is the direction a guard is allowed to be wrong
// in.
//
// # Why a failed read REFUSES, where eos and ios proceed
//
// `arista/eos` and `cisco/ios` run their guard as `rerr == nil && len(have) <= 1`
// and both justify failing open in one word: the console still works. There is no
// console on a 1930 — no SSH, no Telnet, no serial port — so that justification
// does not transfer. A deletion refused leaves a stale account, which is an open
// door but a visible and reversible one; a deletion allowed on a read nobody could
// make costs a site visit and the device's configuration with it.
//
// # WHAT THIS GUARD DOES NOT PROMISE, and the wording matters more than the code
//
// It does NOT promise a way back in. It counts accounts the DEVICE enumerates in
// `AdminUserSetting`; it cannot know which of their secrets meshcraft holds,
// which have ever been authenticated, or which is a factory password — a 1930 out
// of the box carries `admin` with no password at all, and this connector would
// count it as a survivor. So the honest guarantee is one sentence: THE DEVICE
// WILL STILL HAVE AN ACCOUNT. "A credential remains that works" is a question
// about the vault, which no connector can see, and it must not be implied here by
// a predicate that cannot check it — a guard that suggests a safety property it
// does not have is the one an operator stops thinking behind. The refusals and
// the success line both say so on purpose.
//
// COST: one GET on an already-open session, the same read [Admin.EnsureAccount]
// already pays for. This range grants five sessions and no new one is opened.
func (a *Admin) DeleteAccount(ctx context.Context, user string) error {
	if strings.EqualFold(user, a.User) {
		// Deleting yourself would leave the current session without an owner,
		// and equipment without guarantee that another account will respond.
		// FREE: nothing is sent, not even the read below.
		return fmt.Errorf("refusing to delete %q: it is the current session account", user)
	}
	var have, err = a.Accounts(ctx)
	if err != nil {
		return fmt.Errorf("refusing to delete %q on %s: the administration accounts could "+
			"not be read, so nothing here can tell whether it is the last one — and this "+
			"range has no SSH, no Telnet and no serial port, so a switch left without an "+
			"account is recovered with the factory-reset button on site, at the cost of "+
			"its configuration: %w", user, a.host, err)
	}
	if len(have) == 0 {
		return fmt.Errorf("refusing to delete %q on %s: \"%s\" named no account at all, "+
			"and this session is OPEN — the device holds at least the account it was "+
			"opened under, so this read did not answer with the account list. An empty "+
			"list here is an unreadable one, not an empty device, and this range has no "+
			"console to repair a lockout from", user, a.host, sectionAdminUsers)
	}
	var remaining = driver.AccountsExcept(have, user)
	if len(remaining) == 0 {
		return fmt.Errorf("refusing to delete %q on %s: \"%s\" enumerates it as the only "+
			"administration account, and this range has no SSH, no Telnet and no serial "+
			"port — a switch with no account is recovered with the factory-reset button "+
			"on site, at the cost of its configuration", user, a.host, sectionAdminUsers)
	}
	return a.writeSection(ctx, sectionAdminUsers, "delete",
		"<AdminUserList><AdminUserEntry><userName>"+xmlEscape(user)+
			"</userName></AdminUserEntry></AdminUserList>",
		fmt.Sprintf("account %q removed from device — %d account(s) remain, which is NOT a "+
			"promise that anyone holds a secret that opens one: this counts what the device "+
			"enumerates, not what the vault can prove", user, len(remaining)))
}

// SaveConfig copies the CURRENT configuration into the STARTUP
// configuration. Without it, everything just written disappears on the
// next reboot: the device would revert to the factory "admin with no
// password" state, which is exactly the trap provisioning needs to close.
//
// This is the gesture of the interface's "Save configuration" button,
// observed in `hpe/js/modules.js`:
//
//	POST wcd?{file=/hpe/home.htm}{FileCopy}
//	<FileCopy action="set"><srcLocation>1</srcLocation><srcFileType>2</srcFileType>
//	  <destLocation>1</destLocation><destFileType>3</destFileType></FileCopy>
//
// `srcFileType` 2 designates the current configuration and `destFileType`
// 3 the startup configuration; `Location` 1 is the device's flash.
//
// NOTE for anyone exploring this interface: the `FileCopy` section is not
// READABLE. Asked to read it, it responds `statusCode = -1` — exactly
// like a section that does not exist. A section silent on read is
// therefore not proof of absence.
func (a *Admin) SaveConfig(ctx context.Context) error {
	var body = `<?xml version="1.0" encoding="utf-8"?>` +
		"<DeviceConfiguration>" +
		`<FileCopy action="set">` +
		"<srcLocation>1</srcLocation><srcFileType>2</srcFileType>" +
		"<destLocation>1</destLocation><destFileType>3</destFileType>" +
		"</FileCopy>" +
		"</DeviceConfiguration>"

	var url = a.wcdURL() + "?{file=/hpe/home.htm}{FileCopy}"
	var resp, err = a.web.PostAJAX(ctx, url, body)
	if err != nil {
		a.step(false, "saving configuration", err.Error())
		return err
	}
	var code = xmlField(resp.Body, "statusCode")
	if code != "0" {
		a.step(false, "configuration NOT saved: change will be lost on reboot",
			fmt.Sprintf("POST %s → HTTP %d, code %q %s | response: %s",
				url, resp.Status, code, xmlField(resp.Body, "statusString"),
				excerpt(resp.Body, 300)))
		return fmt.Errorf("save refused (code %q)", code)
	}
	a.step(true, "configuration saved to flash",
		fmt.Sprintf("HTTP %d — code 0 (running → startup)", resp.Status))
	return nil
}

// wcdURL is the configuration endpoint, under the session prefix. READS
// take it as `?{Section}`; WRITES take it as POST with the XML body.
func (a *Admin) wcdURL() string {
	var dir = dirOf(a.page)
	// The login page lives under `…/hpe/config/`, the endpoint under `…/hpe/`.
	dir = strings.TrimSuffix(dir, "config/")
	return dir + "wcd"
}

// sessionPrefix extracts the `/csNNNNNNN` prefix that this UI places at
// the front of all its URLs after login. Omitting it systematically
// bounces back to the login page, which looks like an expired session
// even though it is actually a malformed URL.
func sessionPrefix(pageURL string) string {
	var i = strings.Index(pageURL, "/cs")
	if i < 0 {
		return ""
	}
	var rest = pageURL[i+1:]
	var j = strings.Index(rest, "/")
	if j < 0 {
		return ""
	}
	var seg = rest[:j]
	if len(seg) < 4 {
		return ""
	}
	return "/" + seg
}

// xmlEscape protects body values. Secrets generated by meshcraft are
// alphanumeric, but a secret entered by an operator is not necessarily —
// and an unescaped ampersand would break the document without the device
// saying why.
func xmlEscape(s string) string {
	var r = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
		`"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// Get reads a page WITHIN the authenticated session. The session prefix
// is added if the path does not already have one: omitting it sends you
// back to the login page, which looks like an expired session even though
// it is actually a malformed URL.
func (a *Admin) Get(ctx context.Context, path string) (*driver.Response, error) {
	if !strings.HasPrefix(path, "/cs") {
		path = sessionPrefix(a.page) + path
	}
	var resp, err = a.web.Get(ctx, path)
	if err != nil {
		a.step(false, "read of "+path, err.Error())
		return nil, err
	}
	a.step(true, "read of "+path, fmt.Sprintf("HTTP %d — %s", resp.Status, resp.URL))
	return resp, nil
}

// excerpt reduces an answer to what fits in one line of transcript.
func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// Note adds to the transcript what a path responded with. Used to focus
// a connector: recognize the correct URL among several candidates without
// opening a session per attempt.
func (a *Admin) Note(path string, status int, body string) {
	a.step(status == 200 && strings.Contains(body, "<"),
		"response from "+path,
		fmt.Sprintf("HTTP %d, %d bytes: %s", status, len(body), excerpt(body, 200)))
}

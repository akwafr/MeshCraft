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
	// contract, which bears this name precisely so that this FIELD can keep the
	// sien (cf. contract.go) — les tests de ce paquet et la couche API le lisent.
	Steps []Step
	// page is the URL of the last page read: form actions there
	// are relative, under a session prefix `/csNNNNNNNN/`.
	page string
	// User is the account actually used to enter.
	User string
	// accepted is the candidate that the equipment has ACCEPTED, as supplied.
	//
	// Memorized here because it's the only place that knows it firsthand:
	// `Login` holds it at the moment when the equipment responds yes. Before, the caller
	// reconstructed by searching the transcript in search of a step whose
	// wording starts with “connected in” — therefore depending on a written sentence
	// for a human, which one day someone would rephrase. That day, the pose
	// d'identifiant cassait sans un bruit (cf. Admin.Accepted dans contract.go).
	accepted Candidate
	// prefix is ​​the port naming prefix on the front of THIS equipment
	// (“GE”), kept during construction.
	//
	// On the session, and not passed on each reading: two calls from the same session
	// which would receive two different prefixes would write to different ports
	// believing we are talking about the same things. Empty, `portPrefix` applies the default
	// range rather than writing blindly.
	prefix string
	// notedEncryption avoids repeating the same transcript line each time.
	notedEncryption bool
	// State is the state of the open session: “ok”, “initial” (password
	// to be defined), “expired”… This is what decides what happens next.
	State string
}

// defaultPortPrefix is ​​the facade prefix of the entire 1930 range: its ports
// are called “GE1” to “GE52”, optical cages included (they continue the
// copper numbering).
//
// Serves as a default when the caller has not said anything — which happens on equipment
// whose facade is not yet in base. This is exactly what the
// API layer (`portPrefix`), and the value is here so that the connector does not write
// never blindly: without a prefix, “GE12” would not translate into any index of
// port, and a wrong index is a write to the wrong port, thus a broken client.
const defaultPortPrefix = "GE"

// portPrefix makes the prefix to use for translating meshcraft port names
// in equipment interface numbers.
func (a *Admin) portPrefix() string {
	if a.prefix == "" {
		return defaultPortPrefix
	}
	return a.prefix
}

func (a *Admin) step(ok bool, what string, detail ...string) {
	a.Steps = append(a.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// OpenAdmin ouvre une session vers l'interface classique.
//
// WITHOUT PORT PREFIX: a session constructed here falls back on the default of the
// range (“GE”, cf. portPrefix), which is correct for all known models —
// their facades are numbered “GE1” to “GE52”, and it is this package itself which
// describes them as follows (see portGroups). A caller who holds a prefix raised in base
// doit passer par [Driver1930.Authenticate], qui le porte dans son [driver.Target] :
// it is the only path that guarantees that reading and pose speak of the same
// ports que l'inventaire.
func OpenAdmin(host string, d driver.Dialer) *Admin {
	return &Admin{web: driver.NewWebSession("https://"+host, d, 0), host: host}
}

// Candidate, the type of a secret to try, is now that of the contract
// (`driver.Candidate`), by ALIAS: cf. contract.go. Its fields are the same, and
// l'ordre vient toujours de l'appelant (cf. store.SecretsForDevice) — le connecteur
// doesn't decide what a piece of gear probably carries, it applies it.

// Login tries the credentials IN ORDER and stops at the first one that enters.
//
// The order is that of the appellant. Try the most recent password
// first would fail on any equipment left behind — and on an interface
// which only accepts 5 sessions, trials are expensive.
//
// The mechanism is NOT a form POST, contrary to what leaves
// croire le `<form>` de la page : celui-ci n'est jamais soumis. Le bouton appelle
// un `formSubmit()` qui interroge `/device/wcd?{EncryptionSetting}`, chiffre les
// identifiers with the RSA key of the equipment, and does a GET on
// `system.xml?action=login&cred=<hex>`. Poster le formulaire tel quel renvoie un
// 404 — this is the first trap in this range, and it costs an hour for anyone who does not
// lit pas le JavaScript de la page.
func (a *Admin) Login(ctx context.Context, cands []Candidate) error {
	var resp, err = a.web.Get(ctx, "/")
	if err != nil {
		a.step(false, "opening web UI", err.Error())
		return err
	}
	a.page = resp.URL
	a.step(true, "home page read", fmt.Sprintf("HTTP %d — %s", resp.Status, resp.URL))

	// The login URL is relative to the DIRECTORY of the login page
	// (`/csNNNNNNNN/hpe/config/`), not at the root: the session prefix does
	// partie du chemin attendu.
	var loginBase = dirOf(a.page) + "system.xml?action=login&"

	var c Candidate
	for _, c = range cands {
		// The connection token is reread BEFORE EACH TEST: the equipment
		// renews, and playing it again causes the decryption to fail (code 12) — this
		// which looks like a key problem when it is a
		// protection contre le rejeu.
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
			// The accepted candidate is selected HERE, in the only place that knows them well.
			// first hand. This is what `Accepted()` makes, and what exempts
			// the appellant to reconstruct it by rereading the wording of the transcript.
			a.accepted = c
			a.State = loginState(code)
			a.step(true, fmt.Sprintf("logged in as %q", c.Username),
				c.Why+" — code "+code+" : "+loginError(code))
			return nil
		}
		a.step(false, fmt.Sprintf("refused for %q (%s)", c.Username, c.Why),
			fmt.Sprintf("HTTP %d — code %q : %s", r.Status, code, loginError(code)))
		// « Trop de sessions » n'est pas un mauvais mot de passe : insister ne
		// ferait qu'aggraver la saturation d'une interface qui n'en accepte que cinq.
		if code == "5" {
			return fmt.Errorf("%s", loginError(code))
		}
	}
	return fmt.Errorf("no credential accepted (%d attempt(s))", len(cands))
}

// encryptionSetting is what the device says about its own encryption
// identifiants.
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
	// OBSERVED on a real 1930 (firmware 3.4.0.0): when the encryption is
	// active, an EMPTY password results in a code 12 (“problem of
	// encryption"), encrypted as in plain text. Factory account without password
	// is therefore not usable through this door: equipment freshly delivered
	// to zero is handled by the provisioning UI (port 4443), which is
	// exactly there for that.
	// L'ordre des champs et l'esperluette finale reproduisent EXACTEMENT ce que
	// encrypts the interface: the decrypted message is compared as is.
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

// escapeURIComponent reproduit `encodeURIComponent`. `url.QueryEscape` ne
// not suitable: it encodes the space in `+`, which the equipment will reread as a
// more — a password would be silently altered.
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

// authenticated says if a status code is equal to open session.
//
// Major pitfall of this range, discovered the hard way: the login page
// affiche pour le code 12 le message « Login failed due to Device password
// encryption problems", while its own `stateChange()` deals with the same 12
// as a SUCCESSFUL CONNECTION, and that the equipment returns in clear
// `statusString = "Default user Login"`. Se fier au message d'erreur de l'UI
// pointed to an encryption breakdown where the factory account came from
// justement d'ouvrir une session.
//
// The codes which are equivalent to open session are those after which `stateChange()`
// navigue vers `home.htm`.
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
// would be cleaner, but these answers mix XML and HTML fragments according to the
// pages: targeted extraction resists where a complete parser would give up
// le document entier.
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

// sectionAdminUsers est la section de configuration qui porte les comptes
// of administration. It is READ and WRITTEN under this name.
//
// A first version of this connector wrote in a section
// `UpdatePassword`: it does not exist. When questioned, the range responds
// `ActionStatus/statusCode = -1` — observed on a 1930 JL685A (firmware
// 3.4.0.0). This was the root cause of the provisioning failure.
const sectionAdminUsers = "AdminUserSetting"

// accessLevelRW is the “read/write” privilege level. The value is
// noted on the equipment itself: `ConnectedUserList` gives `<level>15</level>`
// pour la session du compte d'administration.
const accessLevelRW = "15"

// SetPassword pose le compte d'administration : nom ET mot de passe.
//
// This is exactly the gesture of the equipment first access form,
// noted in its own bundle (`hpe/js/out/mainpage.js`):
//
//	post = {AdminUserSetting:{set:{AdminUserList:{AdminUserEntry:{
//	         userName: …, password: '$'+…, accessLevel: _MODE_RW}}}}}
//
// L'interface web de cette gamme est le framework « wcd » de Marvell/RADLAN,
// shared with the Cisco CBS350 — for which Cisco publishes the emulator, source of
// three conventions below. You must respect them ALL, otherwise the equipment
// accept the request without doing anything:
//
//   - the POST URL carries the list of written sections (`wcd?{AdminUserSetting}`).
//     The dispatcher ONLY knows from this what he must process: without this list,
//     it responds 200 with an EMPTY body and without `ActionStatus`;
//   - the body is wrapped in `<?xml …?><DeviceConfiguration>` ;
//   - the action is an ATTRIBUTE of the section element — the dispatcher
//     literally selects `//*[@action]`. Make it a child element
//     (`<AdminUserSetting><set>…`) selects nothing, so does nothing.
//
// The password is prefixed with a `$` and IN CLEAR, as this form does.
// RSA encryption with `crypto_!` prefix exists on this range, but it is
// specific to the account management page; applying it here would fail the
// laid. Confidentiality therefore relies on TLS and the proxy tunnel.
//
// `oldSecret` is not transmitted: the equipment does not request it, the session
// open is authentic. The setting is retained so that the caller does not have to
// distinguer cette gamme des autres.
//
// The device closes the session immediately afterwards (its interface continues with a
// `gracefulLogout`): this is normal, and this is why the check is done
// par une NOUVELLE connexion.
//
// WARNING: writing only affects the CURRENT configuration. Without one
// explicit backup, a reboot returns the equipment to its factory state —
// compte `admin` sans mot de passe (cf. Capabilities.Commit).
func (a *Admin) SetPassword(ctx context.Context, user, oldSecret, newSecret string) error {
	var err = a.writeSection(ctx, sectionAdminUsers, "set",
		"<AdminUserList>"+adminUserEntry(user, newSecret)+"</AdminUserList>",
		fmt.Sprintf("account %q set on device", user))
	if err != nil {
		return err
	}

	// The equipment interface continues with `saveConf()`: we do the same,
	// otherwise the password would not survive the first reboot. The failure of
	// la sauvegarde n'annule PAS la pose — le mot de passe est bel et bien en
	// place, and to claim it is absent would be false. It is recorded in the transcript,
	// which is the intended channel for what the operator needs to know.
	a.SaveConfig(ctx) //nolint:errcheck // logged in the transcript, non-blocking
	return nil
}

// adminUserEntry renders an account entry. The password is prefixed with a
// `$`: this is the marker by which the equipment recognizes a secret IN CLEAR.
// He accepts two others, noted in his bundle and not to be confused -
// `#` announces a footprint already calculated (it is in this form that
// the equipment MAKES the passwords it stores), and `crypto_!` a secret
// encrypted in RSA. The marker is placed OUTSIDE the exhaust: it is part
// du protocole, pas du secret.
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

	// Type de contenu `x-www-form-urlencoded`, et non `application/xml` : c'est
	// what the `$.ajax` of the interface produces, which does not specify
	// contentType and therefore inherits the jQuery default.
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

// reUserName rereads the accounts that the device declares. Targeted extraction
// is enough: the response mixes XML and fragments depending on the sections.
var reUserName = regexp.MustCompile(`(?is)<AdminUserEntry>.*?<userName>(.*?)</userName>`)

// Accounts lists the administration accounts present on the equipment.
//
// It’s a READING, and it’s what makes account operations safe:
// knowing what exists before writing avoids adding an account already there (that
// the equipment would refuse) and to believe that an account which is not deleted is deleted.
func (a *Admin) Accounts(ctx context.Context) ([]string, error) {
	var url = a.wcdURL() + "?{" + sectionAdminUsers + "}"
	var resp, err = a.web.Get(ctx, url)
	if err != nil {
		a.step(false, "reading admin accounts", err.Error())
		return nil, err
	}
	var out = []string{}
	var m []string
	for _, m = range reUserName.FindAllStringSubmatch(resp.Body, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	a.step(true, "admin accounts read",
		fmt.Sprintf("HTTP %d — %d account(s): %s", resp.Status, len(out),
			strings.Join(out, ", ")))
	return out, nil
}

// EnsureAccount ensures that an account named `user` exists and has `secret`.
//
// The equipment distinguishes `add` (create) from `set` (modify) and refuses one instead.
// place de l'autre : on lit donc l'existant pour choisir. C'est aussi ce qui
// makes the operation replayable — an pose interrupted then resumed does not come up against
// a half-created account.
func (a *Admin) EnsureAccount(ctx context.Context, user, secret string) error {
	var names, err = a.Accounts(ctx)
	if err != nil {
		return err
	}
	var action = "add"
	var n string
	for _, n = range names {
		if strings.EqualFold(n, user) {
			action = "set"
			break
		}
	}
	var what = fmt.Sprintf("account %q created on device", user)
	if action == "set" {
		what = fmt.Sprintf("account %q updated on device", user)
	}
	return a.writeSection(ctx, sectionAdminUsers, action,
		"<AdminUserList>"+adminUserEntry(user, secret)+"</AdminUserList>", what)
}

// DeleteAccount retire un compte d'administration.
//
// DANGEROUS by nature: this range has no SSH, Telnet, or serial port. If the
// last usable account disappears, the only recourse is the reset button
// zero, therefore a trip to site. The appellant MUST have proven — by opening
// une vraie session, pas en lisant un code de retour — qu'un autre compte
// fonctionne avant d'appeler ceci.
func (a *Admin) DeleteAccount(ctx context.Context, user string) error {
	if strings.EqualFold(user, a.User) {
		// Deleting yourself would leave the current session without an owner,
		// and equipment without guarantee that another account will respond.
		return fmt.Errorf("refusing to delete %q: it is the current session account", user)
	}
	return a.writeSection(ctx, sectionAdminUsers, "delete",
		"<AdminUserList><AdminUserEntry><userName>"+xmlEscape(user)+
			"</userName></AdminUserEntry></AdminUserList>",
		fmt.Sprintf("account %q removed from device", user))
}

// SaveConfig recopie la configuration COURANTE dans la configuration de
// STARTUP. Without it, everything that has just been written disappears in the next
// reboot: the equipment would revert to the “admin without password” exit
// factory, which is exactly the trap that provisioning needs to close.
//
// This is the gesture of the “Save configuration” button of the interface, noted in
// `hpe/js/modules.js` :
//
//	POST wcd?{file=/hpe/home.htm}{FileCopy}
//	<FileCopy action="set"><srcLocation>1</srcLocation><srcFileType>2</srcFileType>
//	  <destLocation>1</destLocation><destFileType>3</destFileType></FileCopy>
//
// `srcFileType` 2 designates the current configuration and `destFileType` 3 the
// startup configuration; `Location` 1 is the equipment flash.
//
// PLEASE NOTE for anyone exploring this interface: the `FileCopy` section is not
// LEGIBLE. Asked to read, it responds `statusCode = -1` — exactly
// comme une section qui n'existe pas. Une section muette en lecture n'est donc
// pas une preuve d'absence.
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

// wcdURL is the configuration endpoint, under the session prefix. THE
// READS take it in `?{Section}`; the SCRIPTURES take it POST with
// le corps XML.
func (a *Admin) wcdURL() string {
	var dir = dirOf(a.page)
	// La page de connexion vit dans `…/hpe/config/`, l'endpoint dans `…/hpe/`.
	dir = strings.TrimSuffix(dir, "config/")
	return dir + "wcd"
}

// sessionPrefix extracts the `/csNNNNNNN` prefix that this UI places at the top of
// all its URLs after connection. Forgetting it systematically returns to the page
// connection, which looks like an expired session even though it's a URL
// mal construite.
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

// xmlEscape protects body values. The secrets generated by meshcraft
// are alphanumeric, but a secret entered by an operator is not
// necessarily — and an unescaped ampersand would break the document without
// the equipment says why.
func xmlEscape(s string) string {
	var r = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;",
		`"`, "&quot;", "'", "&apos;")
	return r.Replace(s)
}

// Get reads a page IN the authenticated session. The session prefix is
// added if the path does not have one: forgetting it returns you to the page
// connection, which looks like an expired session even though it's a bad URL
// construite.
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

// Note adds to the transcript what a path responded to. Used for focusing
// of a connector: recognize the correct URL among several candidates without
// ouvrir une session par essai.
func (a *Admin) Note(path string, status int, body string) {
	a.step(status == 200 && strings.Contains(body, "<"),
		"response from "+path,
		fmt.Sprintf("HTTP %d, %d octets : %s", status, len(body), excerpt(body, 200)))
}

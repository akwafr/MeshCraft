package swos

import (
	"context"
	"crypto/rc4"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Admin is an administration session to a SwOS switch.
//
// There is NO session, and that's the first fact to internalize. SwOS
// authenticates via the HTTP `Authorization: Digest` header (MD5, qop=auth), not via
// a form that would set a cookie: every request re-presents the
// credentials, and nothing survives from one request to the next. A connector that
// "opened a session" and then believed it was still in one would be wrong about the
// nature of the platform.
//
// This mechanism is not reimplemented here: it lives in the shared transport
// (`driver.WebSession.SetAuth` with `driver.AuthDigest`), and that's where it belongs —
// computing a Digest digest has nothing MikroTik-specific about it.
//
// What IS specific to SwOS, and what lives here:
//
//   - the SCHEME is imposed, not negotiated. `AuthDigest`, not `AuthAuto`: SwOS only
//     serves Digest, and presenting it with Basic would produce a second 401 that
//     would read as a wrong password — the most expensive error to diagnose
//     on a device you don't have console access to;
//   - the shape of the write request body, which is neither a form nor JSON
//     (cf. postRaw);
//   - everything is logged to a transcript. On a platform whose
//     configuration is persistent by default and with no safety net (cf.
//     Capabilities.Commit), knowing AFTER THE FACT what was actually sent is not a
//     developer convenience: it's the only way to understand a failure without
//     going back on site — and this model has no serial port.

// `Step` is no longer defined here: it is now an ALIAS of the contract type
// (`type Step = driver.Step`, cf. contract.go). The three connectors used to each carry
// an identical copy, every one of them noting in its comment that it mirrored the
// shape of the others "so the API would have only one way to represent a step." Three
// identical-by-convention definitions hold up until the day one of them gains a
// field: on that day, the silent conversion in the API layer drops it, and a
// configuration drift becomes invisible. The `Steps` field below hasn't changed
// by a single line for all that — that's the whole point of an alias.

// Admin is a session to a SwOS switch.
type Admin struct {
	web *driver.WebSession
	// Steps is the transcript, in order.
	Steps []Step
	// User is the account used. On SwOS there's only one (cf.
	// Capabilities.Admin) — the field exists so the caller doesn't have to
	// treat this platform differently from the others.
	User string
	// accepted is the candidate that opened this access, exactly as supplied by
	// the caller (cf. Admin.Accepted). Set by Driver.Authenticate, EMPTY on an
	// Admin obtained via OpenAdmin — i.e. when no candidate was ever presented,
	// which is the case during discovery.
	//
	// It carries a SECRET: nothing from this field should ever land in a transcript,
	// which is meant to be shown and kept.
	accepted Candidate
	// prefix is the port-naming prefix of the standard model, as carried by the
	// target (driver.Target.PortPrefix). EMPTY until someone has said otherwise, and it's
	// portPrefix() that then applies the default rather than writing blindly: this
	// connector NUMBERS its ports, and translating "GE12" into index 12 is what
	// decides which port a write lands on.
	prefix string
	// ports is the model's geometry, read once from /link.b. Zero until it
	// has been read: no write happens without it, since the field set of
	// /fwd.b depends on it.
	ports int
	sfp   int
	// sfpFirst is the index of the first SFP cage.
	sfpFirst int
}

func (a *Admin) step(ok bool, what string, detail ...string) {
	a.Steps = append(a.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// OpenAdmin opens a session to a SwOS switch.
//
// PLAIN HTTP, and that's not an oversight: SwOS only listens on port 80.
// Confirmed on 10.99.0.36 (CSS326-24G-2S+, SwOS 2.17) — not 443, not 22, not 23. The
// confidentiality of the link therefore rests entirely on the proxy tunnel, and an
// HTTPS attempt would simply fail without revealing anything.
//
// `secret` can be EMPTY, which is the case for a device fresh out of the factory: SwOS
// accepts an empty password, the reference device had one, and the
// Digest computes just fine with it (cf. Capabilities.Admin.Notes).
func OpenAdmin(host, user, secret string, d driver.Dialer) *Admin {
	if user == "" {
		user = FixedAdminName
	}
	var web = driver.NewWebSession("http://"+host, d, 0)
	web.SetAuth(driver.AuthDigest, user, secret)
	return &Admin{web: web, User: user}
}

// DefaultPortPrefix is the prefix of meshcraft's NUMBERED naming family, the one this
// connector still accepts on read and no longer writes.
//
// "GE", the same convention as the Instant On connector, with CONTINUOUS numbering
// including cages: GE1 to GE24 for copper, GE25 / GE26 for the two cages of a
// CSS326-24G-2S+. This is what this connector USED TO WRITE; it now writes the
// device's own names ("Port1", "SFP1"), and this prefix now only serves to re-read
// intentions already recorded under the old convention. The full reasoning is
// in the doc comment block of naming.go.
const DefaultPortPrefix = "GE"

// portPrefix returns the numbered-family prefix to accept on this access.
//
// A DEFAULT and not a refusal, because this prefix isn't information about
// the device but a meshcraft naming convention: refusing to read a
// device whose target hasn't set the field would prevent surveying a
// brand-new switch. The contract puts it this way: "When empty, they apply their own default
// rather than writing blindly."
func (a *Admin) portPrefix() string {
	if a.prefix == "" {
		return DefaultPortPrefix
	}
	return a.prefix
}

// facade returns the NAMED front panel of this access: the geometry read from /link.b, plus the
// two naming families that [Facade.Index] knows how to resolve.
//
// It only makes sense AFTER a geometry read (readAll or Geometry): before that, the
// port count is zero, so no name translates to an index — which is the correct
// refusal, since writing without having read the front panel would be writing blindly.
func (a *Admin) facade() Facade {
	return NewFacade(a.ports, a.sfp, a.sfpFirst, a.portPrefix())
}

// Get reads a page and parses it as an OBJECT.
func (a *Admin) Get(ctx context.Context, page string) (*Page, error) {
	var body, err = a.raw(ctx, page)
	if err != nil {
		return nil, err
	}
	var obj, parseErr = ParseObject(body)
	if parseErr != nil {
		a.step(false, "page "+page+" unreadable", parseErr.Error())
		return nil, fmt.Errorf("%s: %w", page, parseErr)
	}
	a.step(true, "page "+page+" read",
		fmt.Sprintf("%d field(s): %s", len(obj.Keys()),
			strings.Join(obj.Keys(), ", ")))
	return obj, nil
}

// GetTable reads a page and parses it as a TABLE. An empty table is a
// result, not an error.
func (a *Admin) GetTable(ctx context.Context, page string) ([]*Page, error) {
	var body, err = a.raw(ctx, page)
	if err != nil {
		return nil, err
	}
	var rows, parseErr = ParseTable(body)
	if parseErr != nil {
		a.step(false, "table "+page+" unreadable", parseErr.Error())
		return nil, fmt.Errorf("%s: %w", page, parseErr)
	}
	a.step(true, "table "+page+" read", fmt.Sprintf("%d row(s)", len(rows)))
	return rows, nil
}

// errCredentialsRefused is the CREDENTIALS refusal, distinguished from any other failure.
//
// A SENTINEL and not just a message, because two callers need to tell the
// difference and neither can do it from text: Driver.Authenticate
// must move on to the NEXT candidate when the device refuses, and STOP outright when
// it doesn't respond — persisting with the other candidates would prove nothing and
// would muddy the diagnosis. Driver.Identify has exactly the same need.
//
// Comparing strings for this would make the number of attempts
// spent on a device depend on a label that's free to be reworded. The message
// rendered to the operator, though, doesn't change: "/sys.b: credentials refused by
// the device".
var errCredentialsRefused = errors.New("credentials refused by the device")

// raw reads a page and returns its body, refusing anything that's visibly not
// one.
//
// The length check is not paranoia: on this platform, an unknown
// path returns the ENTIRE web interface as HTTP 200 (44,807 bytes on the
// reference model). Without this guard, a typo in a page name
// would present itself as a valid response, and the parser would report an
// invalid syntax where the real error is the path.
func (a *Admin) raw(ctx context.Context, page string) (string, error) {
	var resp, err = a.web.Get(ctx, page)
	if err != nil {
		a.step(false, "reading "+page, err.Error())
		return "", err
	}
	if resp.Status == 401 {
		a.step(false, "authentication refused on "+page,
			fmt.Sprintf("HTTP 401 — account %q; challenge: %s", a.User,
				header(resp.Headers, hdrAuthenticate)))
		return "", fmt.Errorf("%s: %w", page, errCredentialsRefused)
	}
	if resp.Status != 200 {
		a.step(false, "reading "+page, fmt.Sprintf("HTTP %d", resp.Status))
		return "", fmt.Errorf("%s: HTTP %d", page, resp.Status)
	}
	if resp.Truncated {
		// A truncated page is an object whose closing brace is missing:
		// the parser would say so, but with a message that would blame the syntax.
		a.step(false, "truncated response on "+page,
			fmt.Sprintf("%d bytes, limit reached", len(resp.Body)))
		return "", fmt.Errorf("%s: truncated response", page)
	}
	if strings.Contains(resp.Body, "<!doctype") || strings.Contains(resp.Body, "<html") {
		a.step(false, "path unknown to the device: "+page,
			fmt.Sprintf("HTTP 200 but the complete web interface (%d bytes) was served "+
				"instead of the data — on SwOS, a path that doesn't exist doesn't return 404",
				len(resp.Body)))
		return "", fmt.Errorf("%s: page doesn't exist on this firmware "+
			"(the device served its web interface)", page)
	}
	return resp.Body, nil
}

// Backup retrieves the ENTIRE configuration in a single request.
//
// READ ONLY, and this platform's safety net. SwOS applies and
// persists changes in a single step (cf. Capabilities.Commit): there is no free
// rollback, and the only way to go back after an unfortunate write
// is to restore a backup file. Taking it BEFORE writing costs
// only one request and roughly three kilobytes.
//
// Restoring, however, is not implemented here: it's a multipart POST to the same
// URL, which no attempt managed to validate (cf. the delivery report). Offering it
// without having verified it would promise a safety net that might not exist.
func (a *Admin) Backup(ctx context.Context) (string, error) {
	var body, err = a.raw(ctx, PageBackup)
	if err != nil {
		return "", err
	}
	// The device returns an EMPTY body when the configuration is the factory one —
	// its interface then displays "Default configuration. Nothing to save.".
	// This is not a failure, and confusing it with a failed read would cause a refusal
	// to write to a freshly reset device, i.e. exactly
	// the one we want to adopt.
	if strings.TrimSpace(body) == "" {
		a.step(true, "no backup to take",
			"the device holds the factory configuration: there is nothing to restore")
		return "", nil
	}
	a.step(true, "configuration backup taken before write",
		fmt.Sprintf("%d bytes — this is this platform's ONLY way back", len(body)))
	return body, nil
}

// Geometry reads the model's geometry: port count, SFP cage count,
// index of the first cage.
//
// Necessary BEFORE any write to /fwd.b, whose writable field set
// depends on the port count (cf. FwdWritableFields). Reading it rather than deducing
// it from the model name is the safe choice: it's the device that knows how many
// ports it has.
func (a *Admin) Geometry(ctx context.Context) (int, int, int, error) {
	if a.ports > 0 {
		return a.ports, a.sfp, a.sfpFirst, nil
	}
	var link, err = a.Get(ctx, PageLink)
	if err != nil {
		return 0, 0, 0, err
	}
	var ports, sfp, first, geoErr = geometryOf(link)
	if geoErr != nil {
		a.step(false, "model geometry unreadable", geoErr.Error())
		return 0, 0, 0, geoErr
	}
	a.ports, a.sfp, a.sfpFirst = ports, sfp, first
	a.step(true, "model geometry read",
		fmt.Sprintf("%d ports including %d SFP cage(s), the first at index %d",
			ports, sfp, first))
	return ports, sfp, first, nil
}

// geometryOf extracts the geometry from /link.b. A pure function, so it's testable
// against a capture.
func geometryOf(link *Page) (int, int, int, error) {
	var prt, ok = link.Num(FieldPortCount)
	if !ok || prt == 0 || prt > 512 {
		return 0, 0, 0, fmt.Errorf("/link.b doesn't say how many ports this device has "+
			"(field %q missing or out of range)", FieldPortCount)
	}
	// The cage count is optional: an all-copper model doesn't have any.
	var sfp, _ = link.Num(FieldSFPCount)
	if sfp > prt {
		return 0, 0, 0, fmt.Errorf("/link.b reports %d SFP cage(s) for %d port(s)", sfp, prt)
	}
	// `sfpo` absent: the device's interface places the cages at the END.
	var first, hasOffset = link.Num(FieldSFPOffset)
	if !hasOffset {
		first = prt - sfp
	}
	if first > prt {
		return 0, 0, 0, fmt.Errorf("/link.b places the first SFP cage at index %d "+
			"for %d port(s)", first, prt)
	}
	return int(prt), int(sfp), int(first), nil
}

// postRaw sends a write to the device.
//
// The gesture matches its interface (`Q` in its JavaScript): a POST to
// THE PAGE'S URL, whose body is the full object in the pages' syntax —
// neither a form nor JSON. The bytes sent here are, byte for byte, the same
// ones the browser sends: the shared transport's `PostForm` transmits the body AS
// IS, without re-encoding it.
//
// The content type is that of the device's interface, `text/plain`, and
// not the `application/x-www-form-urlencoded` of a form: it's for this
// connector that `PostText` was added to the shared transport. Nothing indicates that
// SwOS reads this header — the body ends up in an evaluator — but nothing
// proves otherwise either, and on this platform a refusal is SILENT. Sending what
// the device has always seen costs less than discovering the hard way in the field.
//
// What the device answers when it accepts: 200 and an EMPTY body. Its interface
// reads nothing but the HTTP code — so there is no error message to
// translate, and the only possible verification is to RE-READ the page.
func (a *Admin) postRaw(ctx context.Context, page, body, what string) error {
	return a.post(ctx, page, body, what, true)
}

// postRawSecret is postRaw for a page whose body carries a SECRET.
//
// The transcript is meant to be read, shown, and kept: an SNMP community or a
// password digest that lands in it stays there. Only the body's size is
// logged — enough to verify that a write did go out, not enough to
// disclose what it carried.
func (a *Admin) postRawSecret(ctx context.Context, page, body, what string) error {
	return a.post(ctx, page, body, what, false)
}

func (a *Admin) post(ctx context.Context, page, body, what string, logBody bool) error {
	var sent = fmt.Sprintf("%d bytes (body not logged: it carries a secret)", len(body))
	if logBody {
		sent = "body sent: " + excerpt(body, 400)
	}
	var resp, err = a.web.PostText(ctx, page, body)
	if err != nil {
		a.step(false, what, "POST "+page+": "+err.Error())
		return err
	}
	if resp.Status != 200 {
		a.step(false, what+" — REFUSED by the device",
			fmt.Sprintf("POST %s → HTTP %d | %s", page, resp.Status, sent))
		return fmt.Errorf("%s: the device responded HTTP %d", page, resp.Status)
	}
	a.step(true, what, fmt.Sprintf("POST %s → HTTP 200 | %s", page, sent))
	return nil
}

// --- The administration password ---

// FixedAdminName is the ONLY account name on SwOS.
//
// This isn't a cautious assumption: the platform has no notion of an
// account. The password-change form carries only three fields
// (old, new, confirmation), the page that receives it contains only one key
// (`{pwd:”}`), and the ENTIRE configuration backup of the device
// mentions no username at all — observed on the CSS326-24G-2S+ running SwOS 2.17,
// testdata/backup.swb. So there is nothing to name.
//
// Consequence for meshcraft, and it matters: the fleet's unified account name
// (`mcadmin`) can NOT be applied here. It's AdminAccount.EffectiveUsername's
// job to resolve it to `admin`, and Capabilities.Admin.Custom=false's job to
// announce it before a run finds out the hard way.
const FixedAdminName = "admin"

// maxPasswordLen is the maximum length accepted, imposed by the device's
// interface: it refuses anything over 15 characters, for the old password
// ("Bad old password") as well as the new one ("New password is too long").
const maxPasswordLen = 15

// SetPassword changes the administration password.
//
// NOT VERIFIED ON HARDWARE. The gesture and the algorithm are observed to the letter
// in the device's JavaScript, but no write could ever be carried out from
// the development host — the tool's access through the tunnel is read
// only, by architectural decision. See the delivery report for the exact
// command to run.
//
// The gesture: `POST /!pwd.b`, body `{pwd:'<64 hex digits>'}`.
//
// The device's return codes, as its interface distinguishes them:
// 200 accepted, **405 wrong old password**. This 405 is the only useful error
// message on the whole platform, and translating it correctly avoids
// concluding there's an outage where the only thing missing is a secret.
//
// The device then RELOADS its page: the new secret takes effect
// immediately, and the following request must present it.
func (a *Admin) SetPassword(ctx context.Context, oldSecret, newSecret string) error {
	var blob, err = PasswordBlob(newSecret, oldSecret)
	if err != nil {
		a.step(false, "password refused before sending", err.Error())
		return err
	}
	// The value isn't text to hex-encode but a digest that's ALREADY in
	// hexadecimal: it's sent raw. Re-encoding it would double the encoding, and
	// the device would read a 64-character secret that isn't the right one.
	var page = NewPage()
	page.set("pwd", Value{Kind: KindStr, Raw: blob})

	var resp, postErr = a.web.PostText(ctx, PagePassword, page.Encode())
	if postErr != nil {
		a.step(false, "administration password change", postErr.Error())
		return postErr
	}
	switch resp.Status {
	case 200:
		// The device now holds the new secret: the session must
		// present it, otherwise every following read would answer 401 and would look
		// like an outage.
		a.web.SetAuth(driver.AuthDigest, a.User, newSecret)
		a.step(true, "administration password changed",
			"POST "+PagePassword+" → HTTP 200; the session now presents the new secret")
		return nil
	case 405:
		a.step(false, "wrong old password",
			"POST "+PagePassword+" → HTTP 405: the device rejected the OLD secret, "+
				"the new one was not applied")
		return fmt.Errorf("the device refuses the old password (HTTP 405)")
	}
	a.step(false, "password change REFUSED",
		fmt.Sprintf("POST %s → HTTP %d", PagePassword, resp.Status))
	return fmt.Errorf("password change refused (HTTP %d)", resp.Status)
}

// PasswordBlob computes what SwOS expects in `{pwd:…}`.
//
// The algorithm is observed to the letter in the device's JavaScript
// (functions `sa` and `ra` of its index.html, CSS326-24G-2S+ running SwOS 2.17):
//
//	buffer = bytes(old) + 0x00 + bytes(new), padded with 0x00 to 32 bytes
//	key    = old, or "*" if old is EMPTY, repeated then cut to 16 bytes
//	stream = RC4(key), with no initial drop
//	for i from 0 to 63: buffer[i mod 32] ^= stream[i]
//	result = hexadecimal of the 32 bytes
//
// Two details you would not guess and that decide success:
//
//   - the RC4 key is the OLD password. A device fresh out of the factory has an
//     empty one, hence the substitution with "*" — without it, RC4 would have no key,
//     and that is exactly the case of the reference device, whose password
//     is empty;
//   - the stream is applied SIXTY-FOUR times over thirty-two bytes, i.e. two
//     passes. Stopping at 32 produces a plausible-looking digest that the device rejects.
//
// The characters must be ASCII: the device's interface builds
// its bytes with `charCodeAt`, which returns UTF-16 code units. Beyond 127, what
// the browser would send and what Go would send would diverge, and the password
// applied would not be the one intended — on a platform with no serial port, that
// gets fixed with a paperclip. The check is therefore done HERE, before sending.
func PasswordBlob(newSecret, oldSecret string) (string, error) {
	var err = checkPassword("new password", newSecret)
	if err != nil {
		return "", err
	}
	if err = checkPassword("old password", oldSecret); err != nil {
		return "", err
	}

	var buf = make([]byte, 32)
	copy(buf, oldSecret)
	// The NUL byte separates the old password from the new one; it's already in
	// place since the buffer starts at zero. The new one begins right after.
	if len(oldSecret)+1 < len(buf) {
		copy(buf[len(oldSecret)+1:], newSecret)
	}

	var key = oldSecret
	if key == "" {
		key = "*"
	}
	for len(key) < 16 {
		key += key
	}
	key = key[:16]

	var stream, rcErr = rc4.NewCipher([]byte(key))
	if rcErr != nil {
		return "", rcErr
	}
	// XORKeyStream on zeros returns the stream itself: it's the most
	// direct way to obtain the 64 stream bytes the algorithm requires.
	var ks = make([]byte, 64)
	stream.XORKeyStream(ks, ks)
	var i int
	for i = 0; i < 64; i++ {
		buf[i&31] ^= ks[i]
	}
	return hex.EncodeToString(buf), nil
}

func checkPassword(label, s string) error {
	if len(s) > maxPasswordLen {
		return fmt.Errorf("%s: %d characters, the device accepts at most %d",
			label, len(s), maxPasswordLen)
	}
	var i int
	for i = 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return fmt.Errorf("%s: SwOS only accepts ASCII, and the character at "+
				"position %d is not one — sending it would make the device inaccessible", label, i+1)
		}
	}
	return nil
}

// Note adds to the transcript what a path answered. Used for recognizing
// an unknown firmware: telling an absent page apart from an empty one without opening
// a browser.
func (a *Admin) Note(page string, status int, body string) {
	a.step(status == 200 && !strings.Contains(body, "<!doctype"),
		"response from "+page,
		fmt.Sprintf("HTTP %d, %d bytes: %s", status, len(body), excerpt(body, 200)))
}

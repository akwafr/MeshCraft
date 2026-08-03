package routeros

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

// Transport for this connector: RouterOS's REST API.
//
// Why REST and not the CLI, even though RouterOS has SSH and Telnet open
// (observed: `/ip/service` serves `ssh` on 22 and `telnet` on 23, and the banner
// is `SSH-2.0-ROSSSH`): the project's generic SSH probe FAILS on this device —
// 'SSH session: timeout after 12s', seen on 10.99.0.54. RouterOS's shell is a
// full-screen interactive terminal that redraws, paginates and waits for keypresses;
// scraping it amounts to reimplementing a terminal. REST, on the other hand,
// returns structured JSON and re-authenticates on every request. So this is not an
// aesthetic preference: it is the only of the two paths that works without writing
// a terminal emulator.
//
// CAUTION, what is observed and what is not: everything this file does in READ
// has been exercised on a CRS326-24G-2S+ on 7.21beta3 through the proxy tunnel.
// Everything it does in WRITE has NOT been — the controller's read tool is
// restricted to GET/HEAD by architectural decision ('writing to a device goes
// through a connector, not through this tool'). Every write carries the mention
// NOT VERIFIED where it is coded.

// The transcript step is no longer defined here: it is `driver.Step`, aliased as
// [Step] by contract.go. This package used to carry its own, saying in its original
// comment that it had 'the same shape as instanton.Step, so the API has only one
// way to present an apply'. Three copies of an identical type by convention hold
// until the day one gains a field — and that day, the API's silent conversion loses
// that field without any test seeing it. The `Steps` field below has not changed
// for all that: an ALIAS imposes no conversion.

// Row is a RouterOS record: a menu 'item', with its `.id`.
//
// All values are STRINGS, including numbers and booleans. That is not a
// simplification on our part; it is what the device serves — observed on 7.21beta3:
// `{"cpu-load":"0","auto-mac":"false","pvid":"100"}`. Reading them as
// `map[string]string` avoids the reverse conversion on every comparison.
type Row map[string]string

// Client is a REST session toward a RouterOS.
//
// 'Session' is a deliberate abuse of language and it must be known: RouterOS REST
// has NO session. Authentication is HTTP Basic re-presented on EVERY request
// (observed: without credentials, `/rest/system/resource` returns 401 with
// `WWW-Authenticate: Basic`). There is therefore neither a cookie to keep, nor a
// connection to open, nor a logout to perform — and, a much heavier consequence, no
// possible 'safe mode', RouterOS's being attached to an interactive session (cf.
// the Commit comment in routeros.go).
type Client struct {
	web  *driver.WebSession
	host string
	// User is the account actually presented to the device.
	User string
	// accepted is the candidate that opened this access, as it was supplied.
	//
	// Set by [Driver.Authenticate] at the moment the device accepts it, therefore at
	// the only place that knows it first-hand, and returned by [Client.Accepted]
	// because the API layer needs it: the CURRENT secret of the device is the one
	// that just entered, and setting the next one requires knowing it without having
	// to reconstruct it from the transcript.
	//
	// UNEXPORTED deliberately: a public field would be mutable after the fact, and
	// 'the candidate that worked' must not be able to become anything other than
	// what the device actually accepted.
	accepted Candidate
	// Steps is the transcript, in order.
	Steps []Step
}

func (c *Client) step(ok bool, what string, detail ...string) {
	c.Steps = append(c.Steps, Step{What: what, Detail: strings.Join(detail, " "), OK: ok})
}

// restTimeout bounds a request.
//
// 20 s and not the default 15 s: measurement on hardware gives a median of 111 ms
// for `/rest/interface` (29 interfaces, 12.7 KiB) THROUGH the proxy tunnel — so far
// from the bound. But a RouterOS recalculating its bridge table, or a tunnel that
// just came back, take much longer, and a timeout on a WRITE leaves the caller
// without knowing whether it was taken. On this platform that persists immediately,
// 'I do not know if it is written' is the worst possible state: we prefer to wait.
const restTimeout = 20 * time.Second

// Open opens a REST client in CLEAR HTTP, and that is an observed choice, not
// negligence.
//
// Observed on 10.99.0.54: port 443 is NOT open, and `/ip/service` explains why —
// the `www-ssl` entry is `disabled: true` AND `invalid: true`, `certificate: none`.
// RouterOS does not serve HTTPS until a certificate has been installed, and it
// ships from the factory without one. Trying `https://` first would therefore not
// give a safer session, only one more timeout before falling back to port 80.
//
// Confidentiality of the Basic password rests ENTIRELY on the proxy tunnel. That
// is acceptable in meshcraft's architecture — the tunnel is the trust perimeter —
// but it must be said: on a segment where proxy traffic is not protected, these
// credentials travel in the clear.
//
// For a RouterOS whose operator has installed a certificate, OpenScheme lets one
// request https explicitly.
func Open(host string, d driver.Dialer, user, secret string) *Client {
	return OpenScheme("http", host, d, user, secret)
}

// OpenScheme opens a client on the requested scheme.
func OpenScheme(scheme, host string, d driver.Dialer, user, secret string) *Client {
	var web = driver.NewWebSession(scheme+"://"+host, d, restTimeout)
	// AuthBasic EXPLICIT, and not AuthAuto: the device does announce `Basic` in its
	// challenge, so `auto` would work. But naming the scheme documents what was
	// observed, and above all avoids a firmware that one day answered `Digest`
	// silently drifting the connector toward another calculation.
	web.SetAuth(driver.AuthBasic, user, secret)
	return &Client{web: web, host: host, User: user}
}

// restError is what RouterOS returns when it refuses.
//
// Observed: `{"detail":"no such command or directory (whatever)","error":400,
// "message":"Bad Request"}`. `detail` is the only field that says something useful
// — `message` is only the HTTP status label.
type restError struct {
	Error   int    `json:"error"`
	Message string `json:"message"`
	Detail  string `json:"detail"`
}

func (e restError) String() string {
	if e.Detail != "" {
		return fmt.Sprintf("%d %s: %s", e.Error, e.Message, e.Detail)
	}
	return fmt.Sprintf("%d %s", e.Error, e.Message)
}

// menuPath prefixes a RouterOS menu with `/rest`, refusing what has no place in a
// menu path.
//
// The sanitizing is not cosmetic: several menus are built from interface names that
// come from the device or an intent, and a `?` or `/../` slipped in would divert the
// request to another endpoint — or, on a WRITE, to a different menu from the one we
// think we are modifying.
//
// TWO filters, and both are needed:
//
//  1. characters that would open a query string or fragment (`?`, `#`, `&`, `=`,
//     space) are stripped;
//  2. `..` segments are stripped ENTIRELY. This is the least obvious and most
//     necessary filter: the dot is a legitimate character in a MikroTik interface
//     name, so it cannot be forbidden — but a `..` segment would survive the first
//     filter, and `net/http` NORMALIZES the path before emitting it.
//     'interface/../../etc' would therefore leave as '/etc', i.e. outside the
//     REST API, and on a write toward an endpoint we did not aim for.
func menuPath(menu string) string {
	menu = strings.TrimPrefix(strings.TrimSpace(menu), "/")
	var kept []string
	var segment string
	for _, segment = range strings.Split(menu, "/") {
		var out []rune
		var r rune
		for _, r = range segment {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
				out = append(out, r)
			}
		}
		var clean = string(out)
		// An empty or parent segment names no menu: keeping it could only move the
		// request.
		if clean == "" || clean == "." || clean == ".." {
			continue
		}
		kept = append(kept, clean)
	}
	return "/rest/" + strings.Join(kept, "/")
}

// decodeRows reads the device response, whether it is a list of items or a single
// object.
//
// Both shapes exist and the menu decides: `/rest/interface` returns an ARRAY,
// `/rest/system/resource` returns an OBJECT (both observed). A connector that
// accepted only one of the two shapes would fail on half the menus without the
// message saying why.
//
// Decoding goes through `json.RawMessage` then puts every value back as a string:
// the device today serves only strings, but a single value served as a number or
// array would make a DIRECT `map[string]string` fail and lose the whole record —
// whereas here it is simply kept as-is, under its JSON form.
func decodeRows(body string) ([]Row, error) {
	var trimmed = strings.TrimSpace(body)
	if trimmed == "" {
		return nil, fmt.Errorf("empty response")
	}
	if strings.HasPrefix(trimmed, "[") {
		var raw []map[string]json.RawMessage
		var err = json.Unmarshal([]byte(trimmed), &raw)
		if err != nil {
			return nil, err
		}
		var out = make([]Row, 0, len(raw))
		var item map[string]json.RawMessage
		for _, item = range raw {
			out = append(out, flatten(item))
		}
		return out, nil
	}
	var raw map[string]json.RawMessage
	var err = json.Unmarshal([]byte(trimmed), &raw)
	if err != nil {
		return nil, err
	}
	return []Row{flatten(raw)}, nil
}

// flatten brings every value back to a string. A JSON string is returned
// unquoted; everything else keeps its JSON form, which is always more usable than
// a lost value.
func flatten(item map[string]json.RawMessage) Row {
	var out = Row{}
	var key string
	var raw json.RawMessage
	for key, raw = range item {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			out[key] = s
			continue
		}
		out[key] = string(raw)
	}
	return out
}

// List reads a RouterOS menu. READ ONLY: replayable as often as wanted, including
// on a production fleet.
func (c *Client) List(ctx context.Context, menu string) ([]Row, error) {
	var path = menuPath(menu)
	var resp, err = c.web.Get(ctx, path)
	if err != nil {
		c.step(false, "read of "+path, err.Error())
		return nil, err
	}
	if resp.Status != 200 {
		var re restError
		json.Unmarshal([]byte(resp.Body), &re) //nolint:errcheck // fallback message below
		c.step(false, "read of "+path+" refused",
			fmt.Sprintf("HTTP %d — %s", resp.Status, re.String()))
		return nil, fmt.Errorf("%s: %s", path, restStatusMeaning(resp.Status, re))
	}
	var rows []Row
	rows, err = decodeRows(resp.Body)
	if err != nil {
		c.step(false, "unreadable response on "+path, err.Error())
		return nil, fmt.Errorf("%s: unreadable response (%w)", path, err)
	}
	c.step(true, "read of "+path,
		fmt.Sprintf("HTTP %d — %d record(s), %d bytes",
			resp.Status, len(rows), len(resp.Body)))
	return rows, nil
}

// Object reads a menu that returns a single setting (`/system/resource`,
// `/interface/bridge/settings`, `/snmp`…).
func (c *Client) Object(ctx context.Context, menu string) (Row, error) {
	var rows, err = c.List(ctx, menu)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return Row{}, nil
	}
	return rows[0], nil
}

// restStatusMeaning translates what a status code from this API means, and that
// is knowledge that cost observations.
//
// The main lesson: on this API, a 500 does NOT PROVE that a menu is absent.
// Observed side by side on 7.21beta3:
//
//	/rest/whatever                        → 400 'no such command or directory'
//	/rest/interface/doesnotexist          → 500 Internal Server Error
//	/rest/interface/dot1x                 → 500 Internal Server Error
//	/rest/interface/dot1x/server          → 200 []            ← the menu EXISTS nonetheless
//	/rest/interface/ethernet/poe          → 500 Internal Server Error
//	/rest/interface/ethernet/poe/settings → 400 'no such command prefix'
//
// In other words: a menu that contains only SUB-menus returns 500, just like a
// nonexistent path. Concluding 'absent' from a single 500 is a mistake — that is
// why this model's PoE absence is established on the 400 from `poe/settings`, which
// literally says the PREFIX does not exist, and not on the 500 from `poe`.
func restStatusMeaning(status int, re restError) string {
	switch status {
	case 401:
		return "credentials refused by the device (HTTP Basic)"
	case 403:
		return "account authenticated but without the required policy — check that its " +
			"group carries `rest-api`, and `write` to write"
	case 400:
		if strings.Contains(re.Detail, "no such command") {
			return "this menu does not exist on this model or firmware: " + re.Detail
		}
		return "request refused: " + re.String()
	case 404:
		return "record not found: " + re.String()
	case 500:
		// The trap documented above, stated where it is read.
		return "the device could not serve this path. On this API a 500 does NOT " +
			"distinguish an absent menu from a menu that contains only sub-menus: " +
			"try a sub-path before concluding absence (" + re.String() + ")"
	}
	return re.String()
}

// --- Writes ---
//
// NONE of the functions below has been exercised on hardware. They are coded from
// the observed shape of records and tested against fixtures; it is the
// orchestrator's write campaign that will validate them.

// Command executes a RouterOS menu command via POST.
//
// WHY POST and not PUT/PATCH/DELETE, which would be the idiomatic shape of a REST
// API: the project's shared transport (internal/driver/webfetch.go) offers `Get`,
// `Put` and `PostJSON`, but neither PATCH nor DELETE — and that file is outside
// this connector's scope. Yet RouterOS exposes EVERY console command as a POST on
// `<menu>/<command>`: `add`, `set`, `remove`, `print`, `monitor`. The POST form
// therefore covers everything this connector needs without touching the common
// transport.
//
// This is not a cheap workaround: it is the same dispatcher on the device side, the
// one that also receives CLI commands. If the write campaign showed that this form
// is refused by this firmware, the fallback is to add `Patch` and `Delete` to
// webfetch.go — that is flagged as such in the delivery report rather than done
// hastily here.
func (c *Client) Command(ctx context.Context, menu, command string,
	payload map[string]string) ([]Row, error) {
	// The command goes through the same sanitizing as the menu, and the result is
	// VERIFIED non-empty: a command reduced to nothing by sanitizing would produce a
	// POST on the menu itself, i.e. a write nobody knows what it does. On a platform
	// that persists immediately, that is not a risk one takes to save a test.
	var path = menuPath(menu + "/" + command)
	var verb = menuPath(command)
	if verb == "/rest/" || !strings.HasSuffix(path, verb[len("/rest/"):]) {
		return nil, fmt.Errorf("invalid command %q for menu %q", command, menu)
	}
	var body, marshalErr = json.Marshal(payload)
	if marshalErr != nil {
		return nil, marshalErr
	}
	var resp, err = c.web.PostJSON(ctx, path, string(body))
	if err != nil {
		c.step(false, "send of "+path, err.Error())
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		var re restError
		json.Unmarshal([]byte(resp.Body), &re) //nolint:errcheck // fallback message below
		// The body sent appears in the transcript: on a platform that persists
		// immediately, knowing exactly what was proposed is the only way to
		// distinguish 'refused' from 'half taken'.
		c.step(false, path+" refused by the device",
			fmt.Sprintf("POST %s %s → HTTP %d — %s", path, describe(payload),
				resp.Status, re.String()))
		return nil, fmt.Errorf("%s refused: %s", path, restStatusMeaning(resp.Status, re))
	}
	var rows []Row
	if strings.TrimSpace(resp.Body) != "" {
		rows, _ = decodeRows(resp.Body) //nolint:errcheck // empty body is a normal success
	}
	c.step(true, path+" accepted", fmt.Sprintf("POST %s → HTTP %d", describe(payload), resp.Status))
	return rows, nil
}

// Add creates a record in a menu.
func (c *Client) Add(ctx context.Context, menu string, fields map[string]string) ([]Row, error) {
	return c.Command(ctx, menu, "add", fields)
}

// Set modifies an existing record, designated by its `.id`.
//
// The `.id` is MANDATORY and always comes from a read done just before: RouterOS
// numbers its records itself (`*0`, `*1B`… observed) and these identifiers are not
// stable across reboots. Caching them would write into the wrong row — on a VLAN
// table, that means cutting the wrong port.
func (c *Client) Set(ctx context.Context, menu, id string,
	fields map[string]string) ([]Row, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("modification of %s refused: no .id supplied — "+
			"a `set` without an identifier would apply to something other than what we think", menu)
	}
	var payload = map[string]string{".id": id}
	var k, v string
	for k, v = range fields {
		payload[k] = v
	}
	return c.Command(ctx, menu, "set", payload)
}

// Remove deletes a record, designated by its `.id`.
func (c *Client) Remove(ctx context.Context, menu, id string) ([]Row, error) {
	if strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("deletion in %s refused: no .id supplied", menu)
	}
	return c.Command(ctx, menu, "remove", map[string]string{".id": id})
}

// describe renders a request body readable in a transcript line, keys SORTED —
// two identical runs must produce two identical lines, otherwise comparing two
// transcripts no longer means anything.
//
// Values of secret fields are MASKED: a transcript is meant to be shown to the
// operator, reread in a log, attached to an incident report. The password just set
// has no place there.
func describe(payload map[string]string) string {
	var keys = make([]string, 0, len(payload))
	var k string
	for k = range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts = make([]string, 0, len(keys))
	for _, k = range keys {
		var v = payload[k]
		if isSecretField(k) {
			v = "***(" + strconv.Itoa(len(v)) + " characters)"
		}
		parts = append(parts, k+"="+v)
	}
	return "{" + strings.Join(parts, " ") + "}"
}

// isSecretField names fields whose value must never reach a log.
//
// Matching is on a FRAGMENT and not on a list of exact names, because this
// platform has several and they do not look alike: `password` for an account, but
// also `authentication-password` and `encryption-password` for an SNMPv3 community
// (both observed in `/snmp/community`). An exact list would leak the day one more
// menu arrives; the fragment would not.
func isSecretField(name string) bool {
	var low = strings.ToLower(name)
	return strings.Contains(low, "password") || strings.Contains(low, "secret") ||
		strings.Contains(low, "passphrase")
}

// Get returns a field value from a record, or the empty string. Exists so one does
// not repeat the presence test on every read.
func (r Row) Get(field string) string { return r[field] }

// Bool reads a RouterOS boolean. The device serves them as `"true"` / `"false"`
// (observed), never as a JSON boolean.
func (r Row) Bool(field string) bool { return r[field] == "true" }

// Int reads an integer, and returns ok=false if the field is absent or unreadable
// rather than a zero that would confuse with a real null value. The difference
// matters: a PVID 0 does not exist, an absent PVID does.
func (r Row) Int(field string) (int, bool) {
	var v, err = strconv.Atoi(strings.TrimSpace(r[field]))
	if err != nil {
		return 0, false
	}
	return v, true
}

// ID returns the record's internal identifier.
func (r Row) ID() string { return r[".id"] }

// SplitList splits a RouterOS list, which is a comma-separated string — observed:
// `"tagged":"bridge,sfp-sfpplus2"`, `"protocol":"cdp,lldp,mndp"`.
//
// Returns an EMPTY and non-nil slice for an empty string: a list with no member and
// an unknown list are treated the same here, but the caller must not have to
// distinguish nil from empty to iterate.
func SplitList(s string) []string {
	var out = []string{}
	var part string
	for _, part = range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// JoinList recomposes a RouterOS list.
func JoinList(items []string) string { return strings.Join(items, ",") }

package unifi

// Editing one SSID: the fourth gesture (ADR-0029).
//
// # Why this is an edit and not a convergence
//
// ADR-0025 §3 refused a stored intention and that refusal stands. Nothing here
// is remembered: `SSIDEdit` lives for the duration of one call and dies with
// it. There is no desired-state column, no drift detection, no reconciling
// loop. meshcraft reads what the controller holds, asks for a change, reads it
// back — and then has no opinion about that SSID until somebody edits it
// again. So there is never a second DECIDER, which is the thing ADR-0015
// actually refused.
//
// # Why the SSID and not the access point
//
// Measured on application 10.6.109, read-only (ADR-0029, Context):
//
//   - `GET /rest/wlanconf` lists the service sets and `GET /rest/wlanconf/{id}`
//     re-reads ONE of them. That item route is the whole reason this file can
//     exist: a setting that was pushed can be read back and compared.
//   - `GET /rest/device` and `/rest/device/{id}` answer 404 api.err.NotFound.
//     The classic per-device REST resource IS GONE on this application, so
//     there is no reachable route to a per-box setting at all. What is left —
//     `stat/device`, `v2/.../device` — are statistics reads, and they OMIT a
//     field that sits at its default, so even a successful write could not be
//     told from a write that did nothing.
//
// Hence: SSIDs are editable, access points are not, and the frontier is a
// class of object rather than a list of fields (ADR-0029 §2).
//
// # The three traps this file absorbs
//
//  1. THE LEGACY API FAILS OPEN ON READS TOO. `GET /rest/wlanconf/{bogus-id}`
//     answers `{"meta":{"rc":"ok"},"data":[]}`. So a confirming read that only
//     checks `rc` would confirm the existence of an object that does not
//     exist. Every read here requires `data` to be non-empty.
//
//  2. A PARTIAL BODY MAY BLANK WHAT IT OMITS. UniFi's legacy REST handler has
//     historically wanted the whole object, and this project has already lost
//     per-box settings to a write that touched them by omission. So the write
//     is read-modify-write of the ENTIRE object: every key we did not touch
//     goes back bit-identical, the passphrase included (ADR-0029 §5).
//
//  3. `rc:ok` IS NOT A RESULT. Same rule as the other three gestures
//     (ADR-0025 §6): an edit is `confirmed` only when a re-read shows each
//     changed field holding what was asked. Otherwise it is ErrUnconfirmed,
//     which is neither a success nor a failure.
//
// # What is NOT verified, and could not be
//
// `PUT /rest/wlanconf/{id}` has never been issued at a controller from here.
// Verifying it is a write, and the day this was built four access points were
// serving a live venue. Its existence is inferred from the shape of the
// resource. BOTH ways of being wrong are handled rather than hidden: a verb
// the controller refuses becomes ErrGestureRefused (a 405 goes through
// asRefusal), and a verb it accepts without effect becomes ErrUnconfirmed. The
// one thing that cannot happen is an edit reported as landed when it did not.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// ErrSSIDMoved is the precondition failing: a field this edit means to replace
// no longer holds the value the operator was shown. NOTHING is sent in that
// case.
//
// This is ADR-0025 §5's idiom moved from "the box's exact name" to "the exact
// values being replaced", and it is strictly stronger for an edit: a typed name
// guards only against acting on the wrong object, whereas this also catches the
// concurrent editor — somebody changing the same SSID in the UniFi interface
// while the form was being filled in (ADR-0029 §6).
var ErrSSIDMoved = errors.New("unifi: the SSID changed on the controller since it was read")

// SSIDSettings is the set of SSID settings meshcraft is willing to edit.
//
// EVERY FIELD IS A POINTER (or a nil slice) and nil means "not part of this
// edit". That is what makes `Was`/`Want` able to describe a change to two
// fields out of twelve without claiming anything about the other ten — and what
// keeps "set this to false" distinguishable from "do not touch this".
//
// The names are 802.11's and meshcraft's, never UniFi's: ADR-0016's
// consequence is that this model is what a second Wi-Fi vendor would have to
// fill, so `client_isolation` rather than `l2_isolation`. The mapping to the
// vendor's keys is the one table below, and it is the only place a UniFi
// spelling appears.
type SSIDSettings struct {
	// Broadcasting is the service set being radiated at all.
	Broadcasting *bool `json:"broadcasting,omitempty"`
	// Name is the SSID itself.
	Name *string `json:"name,omitempty"`
	// Hidden suppresses the SSID from beacons.
	Hidden *bool `json:"hidden,omitempty"`
	// Bands are the radio bands the service set is offered on: "2.4" and "5",
	// in the screen's vocabulary and not the controller's `2g`/`5g`.
	Bands []string `json:"bands,omitempty"`
	// NetworkID is the controller's network object — which carries the VLAN.
	// Opaque on purpose: it is a vendor identifier, and the choices are read
	// from the controller rather than composed here.
	NetworkID *string `json:"network_id,omitempty"`
	// Guest marks the service set as a guest network on the controller.
	Guest *bool `json:"guest,omitempty"`
	// ClientIsolation stops associated clients from reaching each other.
	ClientIsolation *bool `json:"client_isolation,omitempty"`
	// FastRoaming is 802.11r.
	FastRoaming *bool `json:"fast_roaming,omitempty"`
	// BSSTransition is 802.11v.
	BSSTransition *bool `json:"bss_transition,omitempty"`
	// PowerSave is UAPSD.
	PowerSave *bool `json:"power_save,omitempty"`
	// MulticastEnhance is UniFi's multicast enhancement (IGMPv3 conversion).
	MulticastEnhance *bool `json:"multicast_enhance,omitempty"`
	// ProxyARP answers ARP on behalf of associated clients.
	ProxyARP *bool `json:"proxy_arp,omitempty"`
}

// SSIDEdit is one edit: where each field came from, and where it should go.
//
// `Was` is not decoration and it is not a cache. It is the precondition: the
// controller is re-read at the instant of the write and every field being
// changed must still hold its `Was` value, or the whole edit refuses
// (ADR-0029 §6).
type SSIDEdit struct {
	Was  SSIDSettings `json:"was"`
	Want SSIDSettings `json:"want"`
	// Passphrase, when non-nil, REPLACES the pre-shared key. Nil means "leave
	// it alone", and leaving it alone is safe because the write re-sends the
	// object the controller just gave us (ADR-0029 §5, §7).
	//
	// It has no `Was` counterpart on purpose: the browser is never told the
	// current passphrase, so it cannot state one. The rest of the object's
	// precondition is what guards this field from landing on the wrong SSID.
	Passphrase *string `json:"passphrase,omitempty"`
}

// --- The mapping to the vendor's keys: the only place UniFi spellings live ---

// ssidField ties one neutral setting to one wlanconf key.
//
// `read` pulls the value out of an SSIDSettings as a CANONICAL json.RawMessage.
// Canonical means a band list is sorted, so ["5g","2g"] and ["2g","5g"] compare
// equal — the controller does not promise an order and an edit must not fail on
// one.
//
// IT RETURNS THREE THINGS, and the third one is the lesson. With only
// (value, present), a value this package cannot REPRESENT — a band outside what
// this build offers — came back as "absent", and absent means "not part of this
// edit", so the field was silently skipped: an operator asking for 2.4+6 GHz
// would have got 2.4 and no word about the 6. "Cannot represent" and "not being
// edited" are different answers and the type now says so.
type ssidField struct {
	// name is the neutral name, for an error an operator reads.
	name string
	// key is the wlanconf key.
	key string
	// read canonicalises this field out of a settings struct.
	read func(s SSIDSettings) (json.RawMessage, bool, error)
}

// ssidFields is the CLOSED set of settings an edit can carry.
//
// A field is in this list only if `GET /rest/wlanconf/{id}` returns it, because
// that read is what confirms the write (ADR-0029 §3). What is deliberately
// absent, and why, is in ADR-0029's Consequences: the security mode and the
// WPA3 family (their coherent combination cannot be enumerated by reading one
// controller that uses exactly one of them), the MAC filter and the private
// pre-shared keys (structured lists, and the latter carry passphrases ADR-0015
// forbids importing), and every per-box setting (no reachable route).
var ssidFields = []ssidField{
	{"broadcasting", "enabled", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.Broadcasting) }},
	{"name", "name", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawString(s.Name) }},
	{"hidden", "hide_ssid", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.Hidden) }},
	{"bands", "wlan_bands", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBands(s.Bands) }},
	{"network", "networkconf_id", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawString(s.NetworkID) }},
	{"guest", "is_guest", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.Guest) }},
	{"client_isolation", "l2_isolation", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.ClientIsolation) }},
	{"fast_roaming", "fast_roaming_enabled", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.FastRoaming) }},
	{"bss_transition", "bss_transition", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.BSSTransition) }},
	{"power_save", "uapsd_enabled", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.PowerSave) }},
	{"multicast_enhance", "mcastenhance_enabled", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.MulticastEnhance) }},
	{"proxy_arp", "proxy_arp", func(s SSIDSettings) (json.RawMessage, bool, error) { return rawBool(s.ProxyARP) }},
}

// passphraseKey is wlanconf's pre-shared key. Named once, used twice, and never
// logged.
const passphraseKey = "x_passphrase"

// bandTags maps the screen's band vocabulary to the controller's tags.
//
// 6 GHz IS NOT HERE, and that is a decision rather than an oversight. wlanconf
// carries a SECOND, legacy band key (`wlan_band`, observed as "both" beside
// `wlan_bands: ["2g","5g"]`) whose token for a 6 GHz-only service set cannot be
// read off a controller that has no 6 GHz radio. Offering a band whose
// companion key we would have to guess is exactly what ADR-0029 §3 refuses. An
// SSID that already carries a band outside this map is reported as
// band-unknown and its band editor stays shut, so an edit cannot silently drop
// it.
var bandTags = map[string]string{"2.4": "2g", "5": "5g"}

func rawBool(v *bool) (json.RawMessage, bool, error) {
	if v == nil {
		return nil, false, nil
	}
	if *v {
		return json.RawMessage(`true`), true, nil
	}
	return json.RawMessage(`false`), true, nil
}

func rawString(v *string) (json.RawMessage, bool, error) {
	if v == nil {
		return nil, false, nil
	}
	var enc, err = json.Marshal(*v)
	if err != nil {
		return nil, false, err
	}
	return enc, true, nil
}

// rawBands canonicalises a band list: translated to the controller's tags and
// SORTED, so a difference in order is not mistaken for a difference in value.
//
// A band it cannot translate is an ERROR and not an absence. Returning
// "absent" here is the bug this signature was changed to make impossible: the
// edit would have skipped the field and narrowed the service set to whatever it
// could translate, without a word.
func rawBands(v []string) (json.RawMessage, bool, error) {
	if v == nil {
		return nil, false, nil
	}
	if len(v) == 0 {
		return nil, false, errors.New("unifi: a service set with no band would be unreachable")
	}
	var tags = make([]string, 0, len(v))
	var b string
	for _, b = range v {
		var tag, ok = bandTags[strings.TrimSpace(b)]
		if !ok {
			return nil, false, fmt.Errorf(
				"unifi: this build cannot set the %q band: its value for the controller's "+
					"legacy companion key was never read, and writing a guessed one is refused", b)
		}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	var enc, err = json.Marshal(tags)
	if err != nil {
		return nil, false, err
	}
	return enc, true, nil
}

// canonicalRaw puts a value read OUT of the controller's object into the same
// canonical form `read` produces, so the two can be compared as bytes.
//
// Only the band list needs the treatment; everything else is a scalar whose
// JSON encoding is already canonical. A value the controller sends in a shape
// we do not recognise is returned as-is and will simply fail to compare, which
// is the safe direction.
func canonicalRaw(key string, raw json.RawMessage) json.RawMessage {
	if key != "wlan_bands" {
		return raw
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return raw
	}
	sort.Strings(list)
	var enc, err = json.Marshal(list)
	if err != nil {
		return raw
	}
	return enc
}

// --- The gesture ---

// EditSSID writes settings to one service set of the paired site.
//
// THE FOURTH `var`, and ADR-0025 §4's guarantee is still the one that counts:
// `Gesture` cannot be built outside this package, so the set of writes
// meshcraft can ask for is closed at compile time and is counted by counting
// these declarations. It is now four. A fifth is a visible act in review, and
// TestClientSurfaceIsEnumerated fails until the list in it changes too.
//
// Not `destructive`: an edit is reversible, so it does not take the typed-name
// door. Its guard is the precondition in `ask`, which is stronger here — see
// ErrSSIDMoved.
var EditSSID = Gesture{&gesture{
	name: "edit-ssid",
	ask: func(ctx context.Context, c *Client, t Target) error {
		return c.writeWLANConf(ctx, t)
	},
	confirm: func(ctx context.Context, c *Client, t Target) error {
		return c.confirmWLANConf(ctx, t)
	},
}}

// writeWLANConf is read-modify-write of the WHOLE object (ADR-0029 §5).
//
// The order is the decision:
//
//  1. read the entire object from the controller — not from meshcraft's mirror,
//     which may be hours old;
//  2. check the precondition on every field being changed, and refuse the WHOLE
//     edit on the first mismatch, before anything is sent;
//  3. apply only the changed keys onto the object the controller just gave us;
//  4. PUT the whole thing back, so the sixty keys nobody touched — the
//     passphrase among them — return bit-identical.
func (c *Client) writeWLANConf(ctx context.Context, t Target) error {
	if strings.TrimSpace(t.SiteRef) == "" {
		return fmt.Errorf("%w: editing an SSID needs the site's short reference", ErrGestureRefused)
	}
	if strings.TrimSpace(t.SSIDID) == "" {
		return fmt.Errorf("%w: editing an SSID needs its identifier", ErrGestureRefused)
	}

	var current, err = c.wlanConfObject(ctx, t.SiteRef, t.SSIDID)
	if err != nil {
		return err
	}

	var changed = 0
	var f ssidField
	for _, f = range ssidFields {
		var want, wanted, rerr = f.read(t.SSIDEdit.Want)
		if rerr != nil {
			return fmt.Errorf("%w: %s: %v", ErrGestureRefused, f.name, rerr)
		}
		if !wanted {
			continue
		}
		var was, stated, werr = f.read(t.SSIDEdit.Was)
		if werr != nil {
			return fmt.Errorf("%w: %s (as it was): %v", ErrGestureRefused, f.name, werr)
		}
		if !stated {
			return fmt.Errorf("%w: %s is being changed without saying what it was; "+
				"an edit must carry the value it replaces", ErrSSIDMoved, f.name)
		}
		var live = canonicalRaw(f.key, current[f.key])
		if string(live) != string(was) {
			// Named, with both sides, because the operator's next move is to
			// look at what somebody else did. The LIVE value is the useful half.
			return fmt.Errorf("%w: %s is now %s on the controller, not %s. NOTHING WAS SENT",
				ErrSSIDMoved, f.name, blankIfEmpty(live), blankIfEmpty(was))
		}
		if string(live) == string(want) {
			// Already where it should be. Not an error and not a change.
			continue
		}
		current[f.key] = want
		changed++
	}

	if t.SSIDEdit.Passphrase != nil {
		var enc, perr = json.Marshal(*t.SSIDEdit.Passphrase)
		if perr != nil {
			return perr
		}
		current[passphraseKey] = enc
		changed++
	}

	if changed == 0 {
		// Nothing to do, and saying so beats a write that claims to have done
		// something. Not an error: an operator who re-saves an unchanged form
		// has not made a mistake.
		return nil
	}

	var body []byte
	body, err = json.Marshal(current)
	if err != nil {
		return err
	}
	var reply legacyReply
	err = c.do(ctx, http.MethodPut, wlanConfPath(t.SiteRef, t.SSIDID), body, &reply)
	if err != nil {
		// A controller that does not implement PUT here answers 405, which
		// asRefusal turns into a refusal rather than an outage. That is the
		// handled half of the one inference this file rests on.
		return asRefusal(err)
	}
	if reply.Meta.RC != "ok" {
		var why = reply.Meta.Msg
		if why == "" {
			why = "the controller answered " + strings.TrimSpace(reply.Meta.RC)
		}
		return fmt.Errorf("%w: edit-ssid: %s", ErrGestureRefused, why)
	}
	return nil
}

// confirmWLANConf re-reads the object and checks every field the edit asked for.
//
// This is the only judge of whether the edit happened: the legacy API answers
// `rc:ok` to commands it has never heard of, so the PUT's own reply proves
// nothing (ADR-0025 §6).
func (c *Client) confirmWLANConf(ctx context.Context, t Target) error {
	var current, err = c.wlanConfObject(ctx, t.SiteRef, t.SSIDID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnconfirmed, err)
	}
	var f ssidField
	for _, f = range ssidFields {
		var want, wanted, rerr = f.read(t.SSIDEdit.Want)
		if rerr != nil || !wanted {
			continue
		}
		var live = canonicalRaw(f.key, current[f.key])
		if string(live) != string(want) {
			return fmt.Errorf("%w: %s reads back as %s, not %s",
				ErrUnconfirmed, f.name, blankIfEmpty(live), blankIfEmpty(want))
		}
	}
	if t.SSIDEdit.Passphrase != nil {
		// COMPARED HERE AND NOWHERE ELSE, and neither side appears in the
		// error. The controller returns the pre-shared key in cleartext, which
		// is what makes this confirmable at all — and ADR-0015 forbids that
		// value from travelling any further than this comparison (ADR-0029 §7).
		var enc, perr = json.Marshal(*t.SSIDEdit.Passphrase)
		if perr != nil {
			return fmt.Errorf("%w: %v", ErrUnconfirmed, perr)
		}
		if string(current[passphraseKey]) != string(enc) {
			return fmt.Errorf("%w: the passphrase does not read back as the one that was sent",
				ErrUnconfirmed)
		}
	}
	return nil
}

// blankIfEmpty keeps an error sentence readable when the controller simply has
// no such key — which, on an API that omits a field sitting at its default, is
// a thing that happens and must not print as nothing at all.
func blankIfEmpty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	return string(raw)
}

func wlanConfPath(siteRef, ssidID string) string {
	return "/proxy/network/api/s/" + url.PathEscape(siteRef) +
		"/rest/wlanconf/" + url.PathEscape(ssidID)
}

// wlanConfObject reads ONE service set as the raw map the write has to put
// back.
//
// It requires `data` to carry exactly one entry. That is not pedantry: this
// endpoint answers `{"meta":{"rc":"ok"},"data":[]}` for an identifier that does
// not exist, so a reader that trusts `rc` confirms the existence of nothing
// (ADR-0029, Context).
func (c *Client) wlanConfObject(ctx context.Context, siteRef, ssidID string) (
	map[string]json.RawMessage, error) {

	var out struct {
		Meta struct {
			RC  string `json:"rc"`
			Msg string `json:"msg"`
		} `json:"meta"`
		Data []map[string]json.RawMessage `json:"data"`
	}
	var err = c.get(ctx, wlanConfPath(siteRef, ssidID), &out)
	if err != nil {
		return nil, asRefusal(err)
	}
	if out.Meta.RC != "" && out.Meta.RC != "ok" {
		var why = out.Meta.Msg
		if why == "" {
			why = out.Meta.RC
		}
		return nil, fmt.Errorf("%w: wlanconf refused: %s", ErrGestureRefused, why)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("%w: the controller has no service set %q on this site "+
			"(it answered `ok` with nothing in it, which this API does for an unknown id)",
			ErrGestureRefused, ssidID)
	}
	return out.Data[0], nil
}

// --- The reads an editor needs ---

// SSIDDetail is what the edit form is filled from — read LIVE from the
// controller, never from the mirror.
//
// Reading from the authority rather than from meshcraft's copy is what makes
// the precondition mean something: `Was` has to describe what the controller
// holds now, and a mirror that is an hour old would turn every edit into a
// refusal (or worse, into a precondition that passes against a stale truth).
type SSIDDetail struct {
	ID       string       `json:"id"`
	Settings SSIDSettings `json:"settings"`
	// Security is the mode, shown and NOT editable (ADR-0029 §9): the coherent
	// set of keys WPA3 needs cannot be enumerated from a controller that uses
	// one of them. Relayed verbatim, as the screen already does.
	Security string `json:"security"`
	WPAMode  string `json:"wpa_mode"`
	// HasPassphrase says whether a pre-shared key is set, which is all the
	// browser is ever told about it. The value itself never leaves the
	// controller-facing side (ADR-0029 §7).
	HasPassphrase bool `json:"has_passphrase"`
	// BandsEditable is false when the service set carries a band outside
	// `bandTags` — 6 GHz today. Editing then would silently drop that band, so
	// the editor stays shut and the screen says why.
	BandsEditable bool `json:"bands_editable"`
}

// GetSSID reads one service set's editable settings from the controller.
func (c *Client) GetSSID(ctx context.Context, siteRef, ssidID string) (SSIDDetail, error) {
	var obj, err = c.wlanConfObject(ctx, siteRef, ssidID)
	if err != nil {
		return SSIDDetail{}, err
	}
	return ssidDetailOf(ssidID, obj), nil
}

// ssidDetailOf projects the controller's object onto what the form needs.
//
// A key the controller OMITS is left nil rather than defaulted to false. That
// distinction is load-bearing: this API drops a field that sits at its default,
// so inventing `false` would make the form offer a precondition on a value
// nobody read — and the edit would refuse forever for a reason nobody could see.
func ssidDetailOf(id string, obj map[string]json.RawMessage) SSIDDetail {
	var d = SSIDDetail{ID: id, BandsEditable: true}

	d.Settings.Broadcasting = boolAt(obj, "enabled")
	d.Settings.Name = stringAt(obj, "name")
	d.Settings.Hidden = boolAt(obj, "hide_ssid")
	d.Settings.NetworkID = stringAt(obj, "networkconf_id")
	d.Settings.Guest = boolAt(obj, "is_guest")
	d.Settings.ClientIsolation = boolAt(obj, "l2_isolation")
	d.Settings.FastRoaming = boolAt(obj, "fast_roaming_enabled")
	d.Settings.BSSTransition = boolAt(obj, "bss_transition")
	d.Settings.PowerSave = boolAt(obj, "uapsd_enabled")
	d.Settings.MulticastEnhance = boolAt(obj, "mcastenhance_enabled")
	d.Settings.ProxyARP = boolAt(obj, "proxy_arp")

	var tags []string
	if json.Unmarshal(obj["wlan_bands"], &tags) == nil {
		var bands = make([]string, 0, len(tags))
		var tag string
		for _, tag = range tags {
			var band = bandOfTag(tag)
			if band == "" {
				d.BandsEditable = false
				continue
			}
			bands = append(bands, band)
		}
		sort.Strings(bands)
		d.Settings.Bands = bands
	}

	var sec = stringAt(obj, "security")
	if sec != nil {
		d.Security = strings.ToLower(*sec)
	}
	var mode = stringAt(obj, "wpa_mode")
	if mode != nil {
		d.WPAMode = strings.ToLower(*mode)
	}

	// Presence only. The value is read — the controller sends it in cleartext —
	// and goes no further than this boolean.
	var pass = stringAt(obj, passphraseKey)
	d.HasPassphrase = pass != nil && strings.TrimSpace(*pass) != ""

	return d
}

// bandOfTag is bandTags read backwards. "" for a tag this build does not offer.
func bandOfTag(tag string) string {
	var band, want string
	for band, want = range bandTags {
		if want == strings.TrimSpace(strings.ToLower(tag)) {
			return band
		}
	}
	return ""
}

func boolAt(obj map[string]json.RawMessage, key string) *bool {
	var raw, ok = obj[key]
	if !ok || len(raw) == 0 {
		return nil
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func stringAt(obj map[string]json.RawMessage, key string) *string {
	var raw, ok = obj[key]
	if !ok || len(raw) == 0 {
		return nil
	}
	var v string
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

// Network is one of the controller's network objects — what `networkconf_id`
// points at, and therefore the VLAN an SSID lands on.
type Network struct {
	ID string `json:"id"`
	// Name is the operator's label on the controller.
	Name string `json:"name"`
	// VLAN is 0 when the network is untagged, which is how UniFi expresses it:
	// `vlan_enabled` false and no `vlan`. Reported as 0 rather than omitted so
	// a screen can say "untagged" instead of saying nothing.
	VLAN int `json:"vlan"`
	// Purpose is the controller's own word ("corporate", "guest", …), relayed
	// and not interpreted.
	Purpose string `json:"purpose"`
}

// ListNetworks reads the networks an SSID can be attached to.
//
// From the LEGACY API like everything else in this file. It exists so the
// network field is a CHOICE made from what the controller holds, rather than an
// opaque identifier somebody has to paste — and so that choosing it cannot name
// an object that is not there.
func (c *Client) ListNetworks(ctx context.Context, siteRef string) ([]Network, error) {
	if strings.TrimSpace(siteRef) == "" {
		return nil, errors.New("unifi: no site reference; the networks are on the legacy API")
	}
	var out struct {
		Meta struct {
			RC string `json:"rc"`
		} `json:"meta"`
		Data []struct {
			ID          string `json:"_id"`
			Name        string `json:"name"`
			Purpose     string `json:"purpose"`
			VLANEnabled bool   `json:"vlan_enabled"`
			VLAN        any    `json:"vlan"`
		} `json:"data"`
	}
	var err = c.get(ctx, "/proxy/network/api/s/"+url.PathEscape(siteRef)+"/rest/networkconf", &out)
	if err != nil {
		return nil, err
	}
	if out.Meta.RC != "" && out.Meta.RC != "ok" {
		return nil, fmt.Errorf("unifi: networkconf refused: rc=%s", out.Meta.RC)
	}
	var list = make([]Network, 0, len(out.Data))
	var n struct {
		ID          string `json:"_id"`
		Name        string `json:"name"`
		Purpose     string `json:"purpose"`
		VLANEnabled bool   `json:"vlan_enabled"`
		VLAN        any    `json:"vlan"`
	}
	for _, n = range out.Data {
		var vlan int
		if n.VLANEnabled {
			// UniFi has sent this as a number AND as a string across versions,
			// so it is decoded loosely rather than failing the whole list on
			// one network.
			switch v := n.VLAN.(type) {
			case float64:
				vlan = int(v)
			case string:
				var parsed int
				var _, serr = fmt.Sscanf(v, "%d", &parsed)
				if serr == nil {
					vlan = parsed
				}
			}
		}
		list = append(list, Network{ID: n.ID, Name: n.Name, VLAN: vlan, Purpose: n.Purpose})
	}
	return list, nil
}

package unifi

import (
	"encoding/json"
	"reflect"
	"testing"
)

// The `features` field changes SHAPE between the two calls of the same import:
// a list of names in the device LIST, an object keyed by name in the device
// DETAIL. Both were observed on one controller (Network 10.6.109) in one pass,
// so a decoder that handles only one of them files every access point as
// `other` half the time — and `other` is the class that is never shown on the
// Wi-Fi screen.
func TestClassOfAcceptsBothFeatureShapes(t *testing.T) {
	var cases = []struct {
		name string
		raw  string
		want string
	}{
		{"list shape, access point", `["accessPoint"]`, "access_point"},
		{"object shape, access point", `{"accessPoint":{}}`, "access_point"},
		{"list shape, switch", `["switching"]`, "switch"},
		{"object shape, switch", `{"switching":{}}`, "switch"},
		{"gateway", `["gateway"]`, "gateway"},
		// Unrecognised is `other`, never guessed into a class: a box filed as
		// a switch when it is not would be the first step towards somebody
		// expecting to configure it.
		{"unknown feature", `["teleport"]`, "other"},
		{"empty list", `[]`, "other"},
		{"absent", ``, "other"},
		{"garbage", `"nonsense"`, "other"},
	}
	var c struct {
		name string
		raw  string
		want string
	}
	for _, c = range cases {
		var got = ClassOf(json.RawMessage(c.raw))
		if got != c.want {
			t.Errorf("%s: ClassOf(%s) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

// A box advertising several features is classed by the most specific thing it
// does, and an access point wins: a UniFi box that both switches and radiates
// is an access point with a couple of copper ports, and showing it as a switch
// would put it on a screen that promises configuration.
func TestClassOfPrefersAccessPointOverSwitching(t *testing.T) {
	var got = ClassOf(json.RawMessage(`{"switching":{},"accessPoint":{}}`))
	if got != "access_point" {
		t.Fatalf("ClassOf = %q, want access_point", got)
	}
}

// The trailing slash is not cosmetic. UniFi OS answers a bare `400 Bad Request`
// from nginx — naming neither the path nor the cause — to the `//proxy/...`
// that a naive join produces when the base URL ends in a slash. It costs an
// hour the first time, so New normalizes once and no call site has to remember.
func TestNewStripsTrailingSlash(t *testing.T) {
	var c, err = New("https://uos.example:11443/", "token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.BaseURL != "https://uos.example:11443" {
		t.Fatalf("BaseURL = %q, want no trailing slash", c.BaseURL)
	}
}

func TestNewRefusesUnusableInput(t *testing.T) {
	var cases = []struct {
		name  string
		base  string
		token string
	}{
		{"empty base", "", "t"},
		{"no scheme", "uos.example:11443", "t"},
		{"wrong scheme", "ftp://uos.example", "t"},
		{"no host", "https://", "t"},
		{"empty token", "https://uos.example", "  "},
	}
	var c struct {
		name  string
		base  string
		token string
	}
	for _, c = range cases {
		var _, err = New(c.base, c.token)
		if err == nil {
			t.Errorf("%s: New(%q, %q) accepted, want refusal", c.name, c.base, c.token)
		}
	}
}

// The client's exported surface is ENUMERATED here, so that adding to it is a
// visible act in review rather than a quiet one.
//
// This replaces the test that used to assert "there is no write method at
// all". That assertion was ADR-0015's guarantee and ADR-0025 spent it: the
// client can now ask for three gestures. A promise of the same kind had to take
// its place, and this is it — `Do` is the only writing method, and a fourth
// write cannot appear without this list changing in the same diff.
//
// Unlike the old version this is a proof and not a reminder: it reflects over
// the real method set, so a method added later fails the test instead of
// slipping past a comment.
func TestClientSurfaceIsEnumerated(t *testing.T) {
	// Reads.
	var want = map[string]bool{
		"Version":       true,
		"ListSites":     true,
		"FindSite":      true,
		"ListDevices":   true,
		"ListSSIDs":     true,
		"GetDevice":     true,
		"ListAdoptable": true,
		"Rights":        true,
		// The two reads an SSID editor needs (ADR-0029): the service set's
		// current settings, read from the CONTROLLER rather than from the
		// mirror because the edit's precondition has to compare against the
		// authority; and the networks a service set can be attached to, so the
		// network field is a choice rather than an identifier to paste.
		"GetSSID":      true,
		"ListNetworks": true,
		// The ONLY method that writes. Its argument is a Gesture, which cannot
		// be built outside this package, so the set of writes is closed at
		// compile time (ADR-0025 §4).
		"Do": true,
	}
	var typ = reflect.TypeOf(&Client{})
	var got = map[string]bool{}
	var i int
	for i = 0; i < typ.NumMethod(); i++ {
		got[typ.Method(i).Name] = true
	}
	var name string
	for name = range got {
		if !want[name] {
			t.Errorf("Client has an exported method %q that this test does not know about. "+
				"If it writes to the controller, it must be a Gesture instead: the point of "+
				"ADR-0025 §4 is that the write surface stays countable.", name)
		}
	}
	for name = range want {
		if !got[name] {
			t.Errorf("Client lost its %q method; update this list deliberately", name)
		}
	}
}

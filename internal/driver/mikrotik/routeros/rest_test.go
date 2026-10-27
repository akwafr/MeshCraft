package routeros

import (
	"reflect"
	"strings"
	"testing"
)

// TestDecodeOfBothResponseShapes: this API sometimes returns a list, sometimes an
// object, and the MENU decides.
//
// Observed side by side: `/rest/interface` returns an ARRAY, `/rest/system/resource`
// returns an OBJECT. A connector that accepted only one of the two shapes would fail
// on half the menus without the message saying why.
func TestDecodeOfBothResponseShapes(t *testing.T) {
	// Object shape, as served by /rest/system/identity.
	var rows, err = decodeRows(`{"name":"lan-r4"}`)
	if err != nil {
		t.Fatalf("object undecodable: %v", err)
	}
	if len(rows) != 1 || rows[0].Get("name") != "lan-r4" {
		t.Errorf("object decoded as %v", rows)
	}

	// List shape, as served by /rest/interface/bridge/vlan.
	rows, err = decodeRows(`[{".id":"*2","vlan-ids":"99"},{".id":"*6","vlan-ids":"100"}]`)
	if err != nil {
		t.Fatalf("list undecodable: %v", err)
	}
	if len(rows) != 2 || rows[1].ID() != "*6" {
		t.Errorf("list decoded as %v", rows)
	}

	// An empty response is an ERROR and not an empty list: distinguishing them avoids
	// taking a cut tunnel for a menu with no records.
	_, err = decodeRows("   ")
	if err == nil {
		t.Error("an empty response must be reported, not taken for an empty list")
	}
}

// TestEverythingIsAStringInThisAPI: the device serves only strings.
//
// Observed: `{"cpu-load":"0","auto-mac":"false","pvid":"100"}` — numbers and
// booleans are strings. The Bool and Int accessors exist for that.
//
// Decoding stays TOLERANT of a value that is not a string: a direct
// `map[string]string` would fail on the whole record, whereas here the value is
// kept under its JSON form. Losing a complete record because one field changed type
// would be a disproportionate failure mode.
func TestEverythingIsAStringInThisAPI(t *testing.T) {
	var rows, err = decodeRows(
		`[{"pvid":"100","hw-offload":"true","list":["a","b"],"count":7}]`)
	if err != nil {
		t.Fatalf("undecodable: %v", err)
	}
	var r = rows[0]
	var v, ok = r.Int("pvid")
	if !ok || v != 100 {
		t.Errorf("pvid = %d (ok=%t), expected 100", v, ok)
	}
	if !r.Bool("hw-offload") {
		t.Error("hw-offload must read true from the string 'true'")
	}
	// Unexpected forms are kept rather than lost.
	if r.Get("list") != `["a","b"]` {
		t.Errorf("non-string value = %q, expected kept as JSON", r.Get("list"))
	}
	if r.Get("count") != "7" {
		t.Errorf("count = %q, expected '7'", r.Get("count"))
	}

	// Int distinguishes 'absent' from 'zero', and the difference matters: a PVID 0
	// does not exist, an absent PVID does.
	_, ok = r.Int("nonexistent")
	if ok {
		t.Error("Int must return ok=false on an absent field, not a zero that would " +
			"confuse with a real null value")
	}
}

// TestMenuPathSanitized: several menus are built from names that come from the
// device or an intent.
//
// A `?` or `..` slipped in would divert the request to another endpoint — and, on a
// WRITE, to a different menu from the one we think we are modifying.
func TestMenuPathSanitized(t *testing.T) {
	var cases = []struct{ input, expected string }{
		{"interface/bridge/vlan", "/rest/interface/bridge/vlan"},
		{"/interface/bridge/vlan", "/rest/interface/bridge/vlan"},
		{"  system/resource  ", "/rest/system/resource"},
		// What has no place in a menu path is stripped: neither `?`, nor `=`,
		// nor `#`, nor space survives. The resulting path is harmless — it names
		// a menu that does not exist, which the device will report with a 400, instead
		// of naming a menu the caller did not aim for.
		{"user?name=admin", "/rest/usernameadmin"},
		{"snmp/community#x", "/rest/snmp/communityx"},
		// PATH TRAVERSAL, the least obvious case. The dot is a legitimate character
		// in a MikroTik interface name, so it cannot be forbidden — but a `..` segment
		// would survive, and `net/http` NORMALIZES the path before emitting it:
		// 'interface/../../etc' would leave as '/etc', outside the REST API.
		{"interface/../../etc", "/rest/interface/etc"},
		{"..", "/rest/"},
		// Empty segments are absorbed without moving the request.
		{"interface//bridge", "/rest/interface/bridge"},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.input, func(t *testing.T) {
			var got = menuPath(c.input)
			if got != c.expected {
				t.Errorf("menuPath(%q) = %q, expected %q", c.input, got, c.expected)
			}
			// The essential: no character can open a query string or a fragment, and
			// no segment can climb out of /rest — so no endpoint diversion, in read or
			// write.
			if strings.ContainsAny(got, "?#&= ") {
				t.Errorf("menuPath(%q) = %q: contains a character that could divert "+
					"the request", c.input, got)
			}
			if strings.Contains(got, "..") {
				t.Errorf("menuPath(%q) = %q: a path traversal survives, and "+
					"net/http would normalize it out of /rest", c.input, got)
			}
		})
	}
}

// TestSecretsDoNotReachTheTranscript
//
// A transcript is meant to be shown to the operator, reread in a log, attached to
// an incident report. The password just set has no place there.
//
// Matching is on a FRAGMENT and not on a list of exact names, because this
// platform has several and they do not look alike: `password` for an account,
// `authentication-password` and `encryption-password` for an SNMPv3 community
// (both observed in `/snmp/community`).
func TestSecretsDoNotReachTheTranscript(t *testing.T) {
	var line = describe(map[string]string{
		"name":                    "mcadmin",
		"password":                "SuperSecret42",
		"authentication-password": "AuthSecret42",
		"encryption-password":     "PrivSecret42",
		"group":                   "full",
	})

	var leak string
	for _, leak = range []string{"SuperSecret42", "AuthSecret42", "PrivSecret42"} {
		if strings.Contains(line, leak) {
			t.Errorf("secret %q appears in the transcript line: %s", leak, line)
		}
	}
	// What is not a secret stays readable, otherwise the transcript is useless.
	if !strings.Contains(line, "name=mcadmin") || !strings.Contains(line, "group=full") {
		t.Errorf("non-secret fields must stay readable: %s", line)
	}
	// LENGTH is preserved: that is what lets one diagnose a truncated secret
	// without revealing it.
	if !strings.Contains(line, "13 characters") {
		t.Errorf("secret length must stay visible for diagnosis: %s", line)
	}
	// Keys SORTED: two identical runs must produce two identical lines,
	// otherwise comparing two transcripts no longer means anything.
	if line != describe(map[string]string{
		"group": "full", "password": "SuperSecret42", "name": "mcadmin",
		"encryption-password": "PrivSecret42", "authentication-password": "AuthSecret42",
	}) {
		t.Error("key order must be stable regardless of map iteration order")
	}
}

// TestA500DoesNotProveAbsence — the costliest knowledge of this connector.
//
// Observed side by side on 7.21beta3:
//
//	/rest/whatever                        → 400 'no such command or directory'
//	/rest/interface/doesnotexist          → 500
//	/rest/interface/dot1x                 → 500   ← the menu EXISTS nonetheless
//	/rest/interface/dot1x/server          → 200 []
//	/rest/interface/ethernet/poe          → 500
//	/rest/interface/ethernet/poe/settings → 400 'no such command prefix'
//
// A menu that contains only SUB-menus returns 500, just like a nonexistent path.
// Concluding 'absent' from a single 500 is a mistake — that is why PoE absence
// is established on the 400 from `poe/settings`.
//
// The error message must CARRY this knowledge, because that is where it will be
// reread: the next person who sees a 500 on a MikroTik menu must learn from the
// message that they should try a sub-path before concluding.
func TestA500DoesNotProveAbsence(t *testing.T) {
	var msg = restStatusMeaning(500, restError{Error: 500, Message: "Internal Server Error"})
	var expected string
	for _, expected = range []string{"does NOT distinguish", "sub-menus", "sub-path"} {
		if !strings.Contains(msg, expected) {
			t.Errorf("the 500 message must contain %q to prevent concluding too "+
				"quickly: %q", expected, msg)
		}
	}

	// The 400 'no such command', on the other hand, does conclude.
	msg = restStatusMeaning(400, restError{
		Error: 400, Message: "Bad Request", Detail: "no such command prefix"})
	if !strings.Contains(msg, "does not exist") {
		t.Errorf("a 400 'no such command' must conclude absence: %q", msg)
	}

	// The 403 must name the real cause on this platform: the account's GROUP,
	// not the password. That is the diagnosis one will look for first.
	msg = restStatusMeaning(403, restError{Error: 403})
	if !strings.Contains(msg, "rest-api") {
		t.Errorf("a 403 must point to the group policy: %q", msg)
	}
}

func TestRouterOSLists(t *testing.T) {
	// The observed form: a comma-separated string.
	if !reflect.DeepEqual(SplitList("bridge,sfp-sfpplus2"),
		[]string{"bridge", "sfp-sfpplus2"}) {
		t.Error("splitting a RouterOS list")
	}
	// Empty and not nil: the caller must not have to distinguish the two to
	// iterate.
	var empty = SplitList("")
	if empty == nil || len(empty) != 0 {
		t.Errorf("an empty list must return a non-nil empty slice, got %#v", empty)
	}
	// Stray spaces from human input are absorbed.
	if !reflect.DeepEqual(SplitList(" a , b ,, c "), []string{"a", "b", "c"}) {
		t.Error("spaces and empty elements must be absorbed")
	}
	if JoinList([]string{"bridge", "ether1"}) != "bridge,ether1" {
		t.Error("rejoining a RouterOS list")
	}
}

// TestEmptyCommandIsRefused
//
// A command reduced to nothing by sanitizing would produce a POST on the MENU
// itself — a write nobody knows what it does. On a platform that persists
// immediately, that is not a risk one takes to save a test.
func TestEmptyCommandIsRefused(t *testing.T) {
	var c = &Client{}
	var cases string
	for _, cases = range []string{"", "   ", "..", "?"} {
		var _, err = c.Command(nil, "interface/bridge/vlan", cases, nil)
		if err == nil {
			t.Errorf("command %q must be refused before any send", cases)
		}
	}
}

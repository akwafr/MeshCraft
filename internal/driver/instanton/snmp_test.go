package instanton

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Test device for SNMP configuration.
//
// Returning « code 0 » is not enough: the connector RE-READS everything it
// writes, because on this product line a `statusCode 0` with no effect is a
// known case. The fake device must therefore carry STATE and evolve it; otherwise
// tests would only verify request shape — everything except what was costly.
type fakeSNMP struct {
	enabled bool
	// Empty engineID simulates a device that does not declare its SNMP engine.
	engineID string
	// communities: name → access code (1 = read-only).
	communities map[string]string
	// groups: name → write view (empty = read-only).
	groups map[string]string
	// users: name → {auth, priv, group} codes.
	users map[string][3]string
	// groupsNoNotify: groups WITHOUT a notify view. A v3 group
	// without `notifyView` allows declaring a trap receiver that will never emit
	// anything — and nothing in the device UI shows it.
	groupsNoNotify map[string]bool
	// deaf reproduces THIS interface's failure mode: it answers 0 and does
	// nothing.
	deaf bool

	// --- Return path: the two receiver tables ---

	// v2c / v3: declared receivers, in TWO separate tables as on the device.
	v2c []TrapReceiver
	v3  []TrapReceiver
	// trapsEnbl / v1v2Enabled: emission switches, raw codes. Empty
	// simulates firmware that does not render the field — not the same as « 2 ».
	trapsEnbl   string
	v1v2Enabled string
	// refuse: the `statusCode` the device returns for writes. That is how
	// a full table shows up: the firmware only enforces its limit on add.
	refuse string
	// duplicate reproduces the hypothesis OPPOSITE to the connector's: a table
	// where `set` stacks instead of replacing.
	duplicate bool
	// forceV1 simulates a device that falls back to its own form default — SNMPv1 —
	// no matter what is sent.
	forceV1 bool
}

func newFakeSNMP() *fakeSNMP {
	return &fakeSNMP{
		engineID:    "8000000b038825101761a7",
		communities: map[string]string{},
		groups:      map[string]string{},
		users:       map[string][3]string{},
		// The three switches observed on the lab device are set to 1.
		trapsEnbl:   snmpEnabledCode,
		v1v2Enabled: snmpEnabledCode,
	}
}

// refusedResponse is what the device returns when it refuses a write.
const refusedResponse = `<?xml version="1.0" encoding="UTF-8"?><ResponseData>` +
	`<ActionStatus><requestURL>wcd</requestURL><statusCode>4</statusCode>` +
	`<statusString>Table is full</statusString></ActionStatus></ResponseData>`

// xml renders state like a real 1930, sections in a single response.
func (f *fakeSNMP) xml() string {
	var enbl = snmpDisabledCode
	if f.enabled {
		enbl = snmpEnabledCode
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" ?><ResponseData><DeviceConfiguration>`)
	// `trapsEnbl` and `authTrapsEnbl` are RENDERED by the device although no UI
	// page writes them: the connector verifies them, it does not set them.
	b.WriteString(`<SNMPGlobalSetting type="section"><SNMPEnbl>` + enbl +
		`</SNMPEnbl><localEngineID>` + f.engineID + `</localEngineID>` +
		`<trapsEnbl>` + f.trapsEnbl + `</trapsEnbl>` +
		`<authTrapsEnbl>` + snmpEnabledCode + `</authTrapsEnbl></SNMPGlobalSetting>`)
	b.WriteString(`<CommunityList type="section">`)
	var name, mode string
	for name, mode = range f.communities {
		b.WriteString(`<CommunityEntry><communityString>` + name +
			`</communityString><managementStationIPAddress>0.0.0.0` +
			`</managementStationIPAddress><Basic><accessMode>` + mode +
			`</accessMode></Basic></CommunityEntry>`)
	}
	b.WriteString(`</CommunityList><SNMPGroupList type="section">`)
	// The three factory groups are ALWAYS present, two with a write view:
	// that is why a dedicated group is required.
	b.WriteString(`<SNMPGroup><SNMPGroupName>DefaultRead</SNMPGroupName>` +
		`<securityModel>0</securityModel><securityType>1</securityType>` +
		`<readView>Default</readView><writeView></writeView></SNMPGroup>` +
		`<SNMPGroup><SNMPGroupName>DefaultWrite</SNMPGroupName>` +
		`<securityModel>0</securityModel><securityType>1</securityType>` +
		`<readView>Default</readView><writeView>Default</writeView></SNMPGroup>`)
	var writeView string
	for name, writeView = range f.groups {
		// The NOTIFY view is what lets the device address a trap to users of the
		// group: `groupEntry` sets it; a group without it is simulated via `groupsNoNotify`.
		var notify = "Default"
		if f.groupsNoNotify[name] {
			notify = ""
		}
		b.WriteString(`<SNMPGroup><SNMPGroupName>` + name + `</SNMPGroupName>` +
			`<securityModel>3</securityModel><securityType>3</securityType>` +
			`<readView>Default</readView><writeView>` + writeView +
			`</writeView><notifyView>` + notify + `</notifyView></SNMPGroup>`)
	}
	b.WriteString(`</SNMPGroupList><SNMPUserISList type="section">`)
	var u [3]string
	for name, u = range f.users {
		b.WriteString(`<Entry><userName>` + name + `</userName><groupName>` + u[2] +
			`</groupName><authenticationMethod>` + u[0] +
			`</authenticationMethod><privacyMethod>` + u[1] +
			`</privacyMethod></Entry>`)
	}
	b.WriteString(`</SNMPUserISList>`)

	// The TWO receiver tables. `SNMPTrapReceiverList` is a CHILD of
	// `SNMPTrap` — flattening it would model a device that does not exist, and
	// tests would no longer catch the section-isolation trap.
	b.WriteString(`<SNMPTrap type="section"><enabled>` + f.v1v2Enabled +
		`</enabled><SNMPTrapReceiverList>`)
	var r TrapReceiver
	for _, r = range f.v2c {
		b.WriteString(`<SNMPTrapReceiverEntry><receiverIPAddress>` + r.Address +
			`</receiverIPAddress><UDPPortNo>` + r.Port +
			`</UDPPortNo><notificationType>` + r.NotifyType +
			`</notificationType><timeout></timeout><retries></retries>` +
			`<communityString>` + r.Community + `</communityString><SNMPVersion>` +
			r.SNMPVersion + `</SNMPVersion><filterEnabled>` + r.Filter +
			`</filterEnabled><filterName>` + r.FilterName +
			`</filterName><managementStationIPAddress>` + r.Station +
			`</managementStationIPAddress></SNMPTrapReceiverEntry>`)
	}
	b.WriteString(`</SNMPTrapReceiverList></SNMPTrap>`)
	b.WriteString(`<NotificationReceiverList type="section">`)
	for _, r = range f.v3 {
		b.WriteString(`<Receiver><receiverIPAddress>` + r.Address +
			`</receiverIPAddress><UDPPortNo>` + r.Port +
			`</UDPPortNo><notificationType>` + r.NotifyType +
			`</notificationType><timeout></timeout><retries></retries>` +
			`<securityType>` + r.SecurityType + `</securityType><userName>` + r.User +
			`</userName><filterEnabled>` + r.Filter + `</filterEnabled><filterName>` +
			r.FilterName + `</filterName></Receiver>`)
	}
	b.WriteString(`</NotificationReceiverList>`)

	b.WriteString(`</DeviceConfiguration></ResponseData>`)
	return b.String()
}

// apply mutates state according to the request body. Deliberately crude: the
// goal is to imitate a device, not to reimplement its XML parser.
func (f *fakeSNMP) apply(body string) {
	if f.deaf {
		return
	}
	if strings.Contains(body, "<SNMPEnbl>"+snmpEnabledCode+"</SNMPEnbl>") {
		f.enabled = true
	}
	if strings.Contains(body, `<CommunityList action="set">`) {
		f.communities[xmlField(body, "communityString")] = xmlField(body, "accessMode")
	}
	if strings.Contains(body, `<SNMPGroupList action="set">`) {
		f.groups[xmlField(body, "SNMPGroupName")] = xmlField(body, "writeView")
	}
	if strings.Contains(body, `<SNMPUserISList action="delete">`) {
		delete(f.users, xmlField(body, "userName"))
	}
	if strings.Contains(body, `<SNMPUserISList action="add">`) {
		f.users[xmlField(body, "userName")] = [3]string{
			xmlField(body, "authenticationMethod"),
			xmlField(body, "privacyMethod"),
			xmlField(body, "groupName"),
		}
	}
	if strings.Contains(body, `<SNMPTrap action="set">`) {
		var r = TrapReceiver{
			Address:     xmlField(body, "receiverIPAddress"),
			Port:        xmlField(body, "UDPPortNo"),
			NotifyType:  xmlField(body, "notificationType"),
			Community:   xmlField(body, "communityString"),
			SNMPVersion: xmlField(body, "SNMPVersion"),
			Filter:      xmlField(body, "filterEnabled"),
			FilterName:  xmlField(body, "filterName"),
			// The device RENDERS this field even though the add form does not carry
			// it: it is the deletion key for a v1/v2c receiver.
			Station: "0.0.0.0",
		}
		if f.forceV1 {
			r.SNMPVersion = snmpVersionV1
		}
		var addr = r.Address
		f.v2c = f.upsert(f.v2c, r, func(h TrapReceiver) bool { return h.Address == addr })
	}
	if strings.Contains(body, `<NotificationReceiverList action="set">`) {
		var r = TrapReceiver{
			Address:      xmlField(body, "receiverIPAddress"),
			Port:         xmlField(body, "UDPPortNo"),
			NotifyType:   xmlField(body, "notificationType"),
			User:         xmlField(body, "userName"),
			SecurityType: xmlField(body, "securityType"),
			Filter:       xmlField(body, "filterEnabled"),
			FilterName:   xmlField(body, "filterName"),
		}
		// A v3 entry key includes the USER NAME: two entries with the same
		// address and different users coexist, which is exactly why the
		// connector deletes before replacing.
		var addr, user = r.Address, r.User
		f.v3 = f.upsert(f.v3, r, func(h TrapReceiver) bool {
			return h.Address == addr && h.User == user
		})
	}
	if strings.Contains(body, `<NotificationReceiverList action="delete">`) {
		var addr, user = xmlField(body, "receiverIPAddress"), xmlField(body, "userName")
		var keep []TrapReceiver
		var h TrapReceiver
		for _, h = range f.v3 {
			if h.Address == addr && h.User == user {
				continue
			}
			keep = append(keep, h)
		}
		f.v3 = keep
	}
}

// upsert imitates a `set`: it REPLACES the same-key entry, or appends. With
// `duplicate`, it stacks — the hypothesis opposite to the connector's, which
// reread must detect rather than treat as success.
func (f *fakeSNMP) upsert(list []TrapReceiver, r TrapReceiver,
	sameKey func(TrapReceiver) bool) []TrapReceiver {
	if !f.duplicate {
		var i int
		var have TrapReceiver
		for i, have = range list {
			if sameKey(have) {
				list[i] = r
				return list
			}
		}
	}
	return append(list, r)
}

// newSNMPSwitch serves this state over HTTP, same pattern as newSwitchWithState.
func newSNMPSwitch(t *testing.T, seen *[]captured, f *fakeSNMP) *httptest.Server {
	t.Helper()
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var body = readAll(r)
			*seen = append(*seen, captured{
				method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery, body: body,
			})
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(f.xml()))
				return
			}
			// An explicit refusal is returned BEFORE applying: a full table
			// retains nothing of what is sent.
			if f.refuse != "" {
				_, _ = w.Write([]byte(refusedResponse))
				return
			}
			f.apply(body)
			_, _ = w.Write([]byte(okResponse))
		}))
	t.Cleanup(srv.Close)
	return srv
}

// wantIdentity is the reference identity for tests: a 1930 only supports
// SHA-1 and DES (see Capabilities.SNMPv3Auth / SNMPv3Priv).
func wantIdentity() SNMPIdentity {
	return SNMPIdentity{
		Community:  "mcread",
		User:       "mcpoll",
		AuthAlg:    "sha",
		PrivAlg:    "des",
		AuthSecret: "AuthentifSecret1",
		PrivSecret: "ChiffrementSecr2",
	}
}

// TestAuthAlgCode locks the mapping table taken from the device bundle. That
// is THE fact this file contributes: a wrong code yields a user that exists
// and never authenticates.
func TestAuthAlgCode(t *testing.T) {
	var cas = []struct {
		alg      string
		expected string
		refuse   bool
	}{
		{"md5", authCodeMD5, false},
		{"MD5", authCodeMD5, false},
		{"sha", authCodeSHA, false},
		{"SHA-1", authCodeSHA, false},
		// An algorithm the product line cannot do is REFUSED, never rounded
		// to the nearest: setting MD5 where the caller wanted SHA-256 would
		// yield working monitoring and false conformity.
		{"sha256", "", true},
		{"", "", true},
		{"kangaroo", "", true},
	}
	var c = cas[0]
	for _, c = range cas {
		var got, err = authAlgCode(c.alg)
		if c.refuse {
			if err == nil {
				t.Errorf("authAlgCode(%q) should be rejected, returned %q", c.alg, got)
			}
			continue
		}
		if err != nil || got != c.expected {
			t.Errorf("authAlgCode(%q) = %q, %v ; expected %q", c.alg, got, err, c.expected)
		}
	}
}

// TestPrivAlgCode: the enumeration trap is real — « none » is 0 for
// authentication but 1 for privacy.
func TestPrivAlgCode(t *testing.T) {
	var cas = []struct {
		alg      string
		expected string
		refuse   bool
	}{
		{"des", privCodeDES, false},
		{"DES", privCodeDES, false},
		{"", privCodeNone, false},
		{"none", privCodeNone, false},
		{"aes128", "", true},
	}
	var c = cas[0]
	for _, c = range cas {
		var got, err = privAlgCode(c.alg)
		if c.refuse {
			if err == nil {
				t.Errorf("privAlgCode(%q) should be rejected, returned %q", c.alg, got)
			}
			continue
		}
		if err != nil || got != c.expected {
			t.Errorf("privAlgCode(%q) = %q, %v ; expected %q", c.alg, got, err, c.expected)
		}
	}
	if privCodeNone == authCodeNone {
		t.Error("« none » is not the same code for authentication and privacy: " +
			"confusing them would send a value outside the enumeration")
	}
}

// TestApplySNMPRequestShape locks the « wcd » framework conventions applied to
// SNMP sections: without the section list in the URL, or with the action as an
// element rather than an attribute, the device answers 200 and does nothing.
func TestApplySNMPRequestShape(t *testing.T) {
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, newFakeSNMP())

	var res, err = adminOn(srv).ApplySNMP(context.Background(), wantIdentity())
	if err != nil {
		t.Fatalf("ApplySNMP: %v", err)
	}
	if !res.Enabled || !res.CommunitySet || !res.UserSet {
		t.Errorf("incomplete result: %+v", res)
	}

	var w = writes(seen)
	if len(w) < 4 {
		t.Fatalf("at least 4 writes expected (enable, community, group, "+
			"user), %d: %+v", len(w), w)
	}
	// Each write URL carries the written section, and the path keeps the
	// session prefix without `config/`. Flash save is separate: it does not
	// write a configuration section but copies a file, and its shape is already
	// locked by TestSetPasswordSavesToFlash.
	var c captured
	for _, c = range w {
		if strings.Contains(c.rawQuery, "FileCopy") {
			continue
		}
		if c.path != "/cs12345678/hpe/wcd" {
			t.Errorf("path = %q", c.path)
		}
		if !strings.HasPrefix(c.rawQuery, "{") {
			t.Errorf("URL must carry the section list: %q", c.rawQuery)
		}
		if !strings.HasPrefix(c.body, "<?xml version='1.0' encoding='utf-8'?>"+
			"<DeviceConfiguration>") {
			t.Errorf("missing envelope: %s", c.body)
		}
		if strings.Contains(c.body, "<set>") || strings.Contains(c.body, "<add>") {
			t.Errorf("action must be an ATTRIBUTE, not an element: %s", c.body)
		}
	}

	var joined = strings.Join(bodies(w), "\n")
	var expected = []string{
		// 1 = enabled (from `modules.js`: _TRUE = '1').
		`<SNMPGlobalSetting action="set">`,
		"<SNMPEnbl>1</SNMPEnbl>",
		// The v2c communities section is named CommunityList.
		`<CommunityList action="set">`,
		"<communityString>mcread</communityString>",
		`<SNMPGroupList action="set">`,
		`<SNMPUserISList action="add">`,
		"<userName>mcpoll</userName>",
		// SHA-1 = 2, DES = 2.
		"<authenticationMethod>2</authenticationMethod>",
		"<privacyMethod>2</privacyMethod>",
		"<engineIDType>1</engineIDType>",
		"<engineID>8000000b038825101761a7</engineID>",
	}
	var s string
	for _, s = range expected {
		if !strings.Contains(joined, s) {
			t.Errorf("no write contains %q", s)
		}
	}
	// The wrong section from the first attempt must never reappear.
	if strings.Contains(joined, "SNMPCommunityList") {
		t.Errorf("SNMPCommunityList does not exist on this product line: %s", joined)
	}
}

// TestApplySNMPNeverSetsWriteAccess is the most important test in the file.
//
// A write community on a fleet is a cleartext root password on the network
// (docs: SNMP identity §2). Neither the API nor any code path must
// be able to produce one — this test checks what goes on the wire, not intent.
func TestApplySNMPNeverSetsWriteAccess(t *testing.T) {
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, newFakeSNMP())

	var _, err = adminOn(srv).ApplySNMP(context.Background(), wantIdentity())
	if err != nil {
		t.Fatalf("ApplySNMP: %v", err)
	}
	var joined = strings.Join(bodies(writes(seen)), "\n")
	// accessMode 2 = DefaultWrite, 3 = DefaultSuper: neither must ever leave.
	if strings.Contains(joined, "<accessMode>2</accessMode>") ||
		strings.Contains(joined, "<accessMode>3</accessMode>") {
		t.Errorf("community set with write access: %s", joined)
	}
	if !strings.Contains(joined, "<accessMode>1</accessMode>") {
		t.Errorf("community must be set read-only: %s", joined)
	}
	// Same for the v3 group: the write view, not the password, decides that a
	// v3 user cannot write.
	if !strings.Contains(joined, "<writeView></writeView>") {
		t.Errorf("v3 group must have no write view: %s", joined)
	}
}

// TestApplySNMPDetectsWriteWithoutEffect: a `statusCode 0` with no effect is a
// CONFIRMED case on this product line. Without reread, meshcraft would announce
// monitoring in place where nothing is configured — and the operator would hunt
// the fault on the collector side.
func TestApplySNMPDetectsWriteWithoutEffect(t *testing.T) {
	var seen []captured
	var f = newFakeSNMP()
	f.deaf = true // answers 0, changes nothing
	var srv = newSNMPSwitch(t, &seen, f)

	var _, err = adminOn(srv).ApplySNMP(context.Background(), wantIdentity())
	if err == nil {
		t.Fatal("code 0 with no effect must be an error, not success")
	}
	if !strings.Contains(err.Error(), "without effect") {
		t.Errorf("message must say the write had no effect: %v", err)
	}
}

// TestApplySNMPRejectsImpossibleAlgorithm: nothing must leave until everything
// is namable. A half-state where SNMP is open with no identity on it is worse
// than no SNMP at all.
func TestApplySNMPRejectsImpossibleAlgorithm(t *testing.T) {
	var cas = []struct {
		name string
		want SNMPIdentity
	}{
		{"auth out of range", SNMPIdentity{User: "u", AuthAlg: "sha256", PrivAlg: "des",
			AuthSecret: "a", PrivSecret: "b"}},
		{"privacy out of range", SNMPIdentity{User: "u", AuthAlg: "sha", PrivAlg: "aes128",
			AuthSecret: "a", PrivSecret: "b"}},
		// noAuthNoPriv exists in the standard and protects nothing.
		{"no authentication", SNMPIdentity{User: "u", AuthAlg: "", PrivAlg: "des",
			PrivSecret: "b"}},
		{"no secret", SNMPIdentity{User: "u", AuthAlg: "sha", PrivAlg: "des",
			AuthSecret: "", PrivSecret: "b"}},
		{"community name too long", SNMPIdentity{
			Community: strings.Repeat("x", communityMaxLen+1)}},
		{"community with space", SNMPIdentity{Community: "with space"}},
		{"nothing to set", SNMPIdentity{}},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var seen []captured
			var srv = newSNMPSwitch(t, &seen, newFakeSNMP())

			var _, err = adminOn(srv).ApplySNMP(context.Background(), c.want)
			if err == nil {
				t.Fatal("this identity should be rejected")
			}
			if len(seen) != 0 {
				t.Errorf("nothing must be sent to the device: %+v", seen)
			}
		})
	}
}

// TestApplySNMPIdempotent: re-running on an already conformant device must
// rewrite nothing. That is what makes automatic provisioning replayable across
// a fleet without disturbing it — and for the v3 user, what avoids deleting
// then recreating it, thus interrupting collection for nothing.
func TestApplySNMPIdempotent(t *testing.T) {
	var f = newFakeSNMP()
	f.enabled = true
	f.communities["mcread"] = accessModeReadOnly
	f.groups[defaultV3Group] = ""
	f.users["mcpoll"] = [3]string{authCodeSHA, privCodeDES, defaultV3Group}

	var seen []captured
	var srv = newSNMPSwitch(t, &seen, f)

	var res, err = adminOn(srv).ApplySNMP(context.Background(), wantIdentity())
	if err != nil {
		t.Fatalf("ApplySNMP: %v", err)
	}
	if res.CommunitySet || res.UserSet || res.GroupCreated {
		t.Errorf("no write should be necessary: %+v", res)
	}
	// Only the flash save remains, and it is harmless.
	var c captured
	for _, c = range writes(seen) {
		if !strings.Contains(c.rawQuery, "FileCopy") {
			t.Errorf("unexpected write on an already conformant device: %s %s",
				c.rawQuery, c.body)
		}
	}
}

// TestApplySNMPReplacesDivergentUser: the device offers no `set` on
// SNMPUserISList — its interface only offers `add` and `delete`. Changing the
// algorithm pair therefore requires two gestures, in that order.
func TestApplySNMPReplacesDivergentUser(t *testing.T) {
	var f = newFakeSNMP()
	f.enabled = true
	// Same name, but set with MD5: it is not the same user; its keys are
	// derived differently.
	f.users["mcpoll"] = [3]string{authCodeMD5, privCodeDES, defaultV3Group}

	var seen []captured
	var srv = newSNMPSwitch(t, &seen, f)

	var want = wantIdentity()
	want.Community = "" // only testing v3 here
	var _, err = adminOn(srv).ApplySNMP(context.Background(), want)
	if err != nil {
		t.Fatalf("ApplySNMP: %v", err)
	}
	var delAt, addAt = -1, -1
	var i int
	var c captured
	for i, c = range writes(seen) {
		if strings.Contains(c.body, `<SNMPUserISList action="delete">`) {
			delAt = i
		}
		if strings.Contains(c.body, `<SNMPUserISList action="add">`) {
			addAt = i
		}
	}
	if delAt < 0 || addAt < 0 {
		t.Fatalf("delete then add expected: %+v", writes(seen))
	}
	if delAt > addAt {
		t.Error("delete must precede add: the device indexes users by name")
	}
	if f.users["mcpoll"][0] != authCodeSHA {
		t.Errorf("user should end up as SHA-1: %+v", f.users["mcpoll"])
	}
}

// TestApplySNMPRejectsWithoutEngineID: without an engine identifier, v3 key
// derivation has no salt. The user would be set and never authenticate — better
// to say so than to send an invented value.
func TestApplySNMPRejectsWithoutEngineID(t *testing.T) {
	var f = newFakeSNMP()
	f.engineID = ""
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, f)

	var want = wantIdentity()
	want.Community = ""
	var _, err = adminOn(srv).ApplySNMP(context.Background(), want)
	if err == nil {
		t.Fatal("setting a v3 user without localEngineID must be rejected")
	}
	var c captured
	for _, c = range writes(seen) {
		if strings.Contains(c.body, "SNMPUserISList") {
			t.Errorf("no user must be sent: %s", c.body)
		}
	}
}

// TestApplySNMPDoesNotLogSecrets: the transcript is shown to the operator and
// retained. A community name IS the secret that opens the device over SNMP;
// so are the v3 passwords.
func TestApplySNMPDoesNotLogSecrets(t *testing.T) {
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, newFakeSNMP())
	var a = adminOn(srv)

	var _, err = a.ApplySNMP(context.Background(), wantIdentity())
	if err != nil {
		t.Fatalf("ApplySNMP: %v", err)
	}
	var secrets = []string{"mcread", "AuthentifSecret1", "ChiffrementSecr2"}
	var st Step
	for _, st = range a.Steps {
		var s string
		for _, s = range secrets {
			if strings.Contains(st.What, s) || strings.Contains(st.Detail, s) {
				t.Errorf("secret in transcript: %q / %q", st.What, st.Detail)
			}
		}
	}
}

// TestApplySNMPSavesToFlash: without a save, SNMP becomes disabled again on the
// first reboot — and the device would still look monitored.
func TestApplySNMPSavesToFlash(t *testing.T) {
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, newFakeSNMP())

	var _, err = adminOn(srv).ApplySNMP(context.Background(), wantIdentity())
	if err != nil {
		t.Fatalf("ApplySNMP: %v", err)
	}
	var w = writes(seen)
	if len(w) == 0 || w[len(w)-1].rawQuery != "{file=/hpe/home.htm}{FileCopy}" {
		t.Errorf("save must close the operation: %+v", w)
	}
}

// TestReadSNMPReadsAllInOneRequest: this product line accepts only five web
// sessions. Reading one section at a time would kick the human administrator
// out of their own UI.
func TestReadSNMPReadsAllInOneRequest(t *testing.T) {
	var f = newFakeSNMP()
	f.enabled = true
	f.communities["public"] = "2" // a WRITE community, already present
	f.users["ancien"] = [3]string{authCodeMD5, privCodeNone, "DefaultRead"}

	var seen []captured
	var srv = newSNMPSwitch(t, &seen, f)

	var st, err = adminOn(srv).ReadSNMP(context.Background())
	if err != nil {
		t.Fatalf("ReadSNMP: %v", err)
	}
	if len(seen) != 1 {
		t.Errorf("1 request expected, got %d", len(seen))
	}
	if !st.Enabled || st.EngineID == "" {
		t.Errorf("global state misread: %+v", st)
	}
	var c, seenCommunity = st.Community("public")
	if !seenCommunity || c.ReadOnly() {
		t.Errorf("a write community must be seen as such: %+v", c)
	}
	var u, seenUser = st.User("ancien")
	if !seenUser || u.Auth != authCodeMD5 || u.Priv != privCodeNone {
		t.Errorf("v3 user misread: %+v", u)
	}
	// Factory groups carry a write view: that is what justifies a dedicated
	// group for meshcraft users.
	var g, seenGroup = st.Group("DefaultWrite")
	if !seenGroup || g.WriteView == "" {
		t.Errorf("factory group misread: %+v", g)
	}
}

// TestSectionOfDoesNotMixSections: v3 users live in `<Entry>` elements, a name
// so generic that searching the whole document would pick up entries from any
// other section.
func TestSectionOfDoesNotMixSections(t *testing.T) {
	var body = `<ResponseData><DeviceConfiguration>` +
		`<Standard802_3List type="section"><Entry><interfaceName>1</interfaceName>` +
		`</Entry></Standard802_3List>` +
		`<SNMPUserISList type="section"><Entry><userName>mcpoll</userName></Entry>` +
		`</SNMPUserISList></DeviceConfiguration></ResponseData>`

	var got = sectionOf(body, sectionSNMPUsers)
	if strings.Contains(got, "interfaceName") {
		t.Errorf("section spilled into another: %s", got)
	}
	if !strings.Contains(got, "mcpoll") {
		t.Errorf("section poorly isolated: %s", got)
	}
	if sectionOf(body, "MissingSection") != "" {
		t.Error("a missing section must return empty")
	}
}

// --- Trap receivers ---
//
// What these tests can prove, and what they cannot: writing receivers is a
// RECONSTRUCTION deduced from the device bundle and never executed on hardware
// (docs/01-Architecture/Connecteurs.md). A replayed device therefore cannot say
// the shape is correct — it only says what it was taught. What it does verify,
// and what is worth it, is that the connector produces EXACTLY the observed
// shape, never announces success without a reread, and that each known failure
// mode on this product line yields a message that says what to do.

// wantTrapV2c / wantTrapV3: the two reference receivers. Same collector address
// in both cases — that is the proxy.
func wantTrapV2c() TrapDestination {
	return TrapDestination{Address: "10.99.200.9", Community: "mctrap"}
}

func wantTrapV3() TrapDestination {
	return TrapDestination{Address: "10.99.200.9", User: "mcpoll"}
}

// fakeTrapReady makes a device ready to accept a receiver: SNMP enabled, and —
// for v3 — the user and its group already set by ApplySNMP.
func fakeTrapReady() *fakeSNMP {
	var f = newFakeSNMP()
	f.enabled = true
	f.groups[defaultV3Group] = ""
	f.users["mcpoll"] = [3]string{authCodeSHA, privCodeDES, defaultV3Group}
	return f
}

// trapPresent returns a conformant v2c receiver as the device would return
// after a successful set.
func trapPresent(addr string) TrapReceiver {
	return TrapReceiver{
		Address: addr, Port: "162", NotifyType: notifyTypeTrap, Community: "mctrap",
		SNMPVersion: snmpVersionV2, Filter: filterNone, Station: "0.0.0.0",
	}
}

// postTo finds the write addressed to a section, and fails if there is not
// exactly one: two writes for one receiver would mean probing hardware.
func postTo(t *testing.T, seen []captured, section string) captured {
	t.Helper()
	var out []captured
	var c captured
	for _, c = range writes(seen) {
		if c.rawQuery == "{"+section+"}" {
			out = append(out, c)
		}
	}
	if len(out) != 1 {
		t.Fatalf("%d write(s) on section %s, 1 expected: %+v",
			len(out), section, writes(seen))
	}
	return out[0]
}

// TestApplyTrapReceiverBodyV2c locks the body FIELD BY FIELD against the
// observation. Exact equality is intentional: it is the only shape we have, and
// the day hardware contradicts it, this is the test to correct — so the new
// shape will be readable there.
func TestApplyTrapReceiverBodyV2c(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	var srv = newSNMPSwitch(t, &seen, f)

	var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV2c())
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	if !res.Set || res.Version != "v2c" || res.Port != defaultTrapPort {
		t.Errorf("unexpected result: %+v", res)
	}

	var c = postTo(t, seen, sectionSNMPTrap)
	var expected = `<?xml version='1.0' encoding='utf-8'?><DeviceConfiguration>` +
		`<SNMPTrap action="set">` +
		`<SNMPTrapReceiverList><SNMPTrapReceiverEntry>` +
		`<receiverIPAddress>10.99.200.9</receiverIPAddress>` +
		`<UDPPortNo>162</UDPPortNo>` +
		`<notificationType>1</notificationType>` +
		`<timeout></timeout><retries></retries>` +
		`<communityString>mctrap</communityString>` +
		`<SNMPVersion>2</SNMPVersion>` +
		`<filterEnabled>2</filterEnabled><filterName></filterName>` +
		`</SNMPTrapReceiverEntry></SNMPTrapReceiverList>` +
		`</SNMPTrap></DeviceConfiguration>`
	if c.body != expected {
		t.Errorf("unexpected v2c body.\ngot: %s\nexpected: %s", c.body, expected)
	}
	// The section is `SNMPTrap`, and `SNMPTrapReceiverList` is only a CHILD:
	// treating it as a section answers `statusCode -1`, like a missing section.
	// The URL must therefore never carry it.
	if strings.Contains(c.rawQuery, "SNMPTrapReceiverList") {
		t.Errorf("SNMPTrapReceiverList is not a section: %q", c.rawQuery)
	}
	if c.path != "/cs12345678/hpe/wcd" {
		t.Errorf("path = %q", c.path)
	}
	// The form default on this product line is SNMPv1: copying it would declare
	// a v1 receiver that the v2c collector could not use.
	if strings.Contains(c.body, "<SNMPVersion>"+snmpVersionV1+"</SNMPVersion>") {
		t.Errorf("SNMPVersion 1 (form default) must never be sent: %s", c.body)
	}
	// The device must have retained version 2, not merely received it.
	if len(f.v2c) != 1 || f.v2c[0].SNMPVersion != snmpVersionV2 {
		t.Errorf("final device state: %+v", f.v2c)
	}
}

// TestApplyTrapReceiverBodyV3: the other table, the other shape. There is no
// « SNMPTrapV3 » section — it is `NotificationReceiverList`, without a prefix.
func TestApplyTrapReceiverBodyV3(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	var srv = newSNMPSwitch(t, &seen, f)

	var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV3())
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	if !res.Set || res.Version != "v3" {
		t.Errorf("unexpected result: %+v", res)
	}

	var c = postTo(t, seen, sectionTrapReceiversV3)
	// securityType 3 = authPriv, DEDUCED from the user (SHA-1 + DES) and not
	// chosen: a receiver declared at another level would point at an identity
	// that does not exist.
	var expected = `<?xml version='1.0' encoding='utf-8'?><DeviceConfiguration>` +
		`<NotificationReceiverList action="set">` +
		`<Receiver>` +
		`<receiverIPAddress>10.99.200.9</receiverIPAddress>` +
		`<UDPPortNo>162</UDPPortNo>` +
		`<notificationType>1</notificationType>` +
		`<timeout></timeout><retries></retries>` +
		`<securityType>3</securityType>` +
		`<userName>mcpoll</userName>` +
		`<filterEnabled>2</filterEnabled><filterName></filterName>` +
		`</Receiver>` +
		`</NotificationReceiverList></DeviceConfiguration>`
	if c.body != expected {
		t.Errorf("unexpected v3 body.\ngot: %s\nexpected: %s", c.body, expected)
	}
	// A community in a v3 receiver would make no sense, and would betray
	// confusion between the two tables.
	if strings.Contains(c.body, "communityString") {
		t.Errorf("a v3 receiver carries no community: %s", c.body)
	}
	if strings.Contains(strings.Join(bodies(writes(seen)), "\n"), "SNMPTrapV3") {
		t.Error("there is no « SNMPTrapV3 » section on this product line")
	}
	if len(f.v3) != 1 || f.v3[0].SecurityType != securityLevelAuthPriv {
		t.Errorf("final device state: %+v", f.v3)
	}
}

// TestApplyTrapReceiverSecurityDeducedFromUser: a user without privacy is
// authNoPriv, and the receiver must follow. The level is never a parameter —
// only the user's algorithm pair decides.
func TestApplyTrapReceiverSecurityDeducedFromUser(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.users["mcpoll"] = [3]string{authCodeSHA, privCodeNone, defaultV3Group}
	var srv = newSNMPSwitch(t, &seen, f)

	var _, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV3())
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	var c = postTo(t, seen, sectionTrapReceiversV3)
	if !strings.Contains(c.body,
		"<securityType>"+securityLevelAuthNoPriv+"</securityType>") {
		t.Errorf("securityType expected %s (authNoPriv): %s",
			securityLevelAuthNoPriv, c.body)
	}
}

// TestApplyTrapReceiverConfigurablePort: `UDPPortNo` is ordinary input,
// validated 1–65535. Directly useful: the proxy need not listen on a privileged
// port if that is not desired.
func TestApplyTrapReceiverConfigurablePort(t *testing.T) {
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, fakeTrapReady())

	var want = wantTrapV2c()
	want.Port = 10162
	var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), want)
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	if res.Port != 10162 {
		t.Errorf("result port = %d", res.Port)
	}
	if !strings.Contains(postTo(t, seen, sectionSNMPTrap).body,
		"<UDPPortNo>10162</UDPPortNo>") {
		t.Error("requested port must be sent as-is")
	}
}

// TestApplyTrapReceiverIdempotent: an already present and conformant receiver
// triggers NO write. That is what makes provisioning replayable across a fleet
// — and above all what avoids stacking one more receiver on every pass, in a
// table whose ceiling is unknown.
func TestApplyTrapReceiverIdempotent(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.v2c = []TrapReceiver{trapPresent("10.99.200.9")}
	var srv = newSNMPSwitch(t, &seen, f)

	var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV2c())
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	if res.Set || res.Replaced {
		t.Errorf("no write should be necessary: %+v", res)
	}
	if len(writes(seen)) != 0 {
		t.Errorf("unexpected write on an already conformant device: %+v",
			writes(seen))
	}
	if len(f.v2c) != 1 {
		t.Errorf("table must not grow: %+v", f.v2c)
	}
}

// TestApplyTrapReceiverRewritesDivergentReceiver: present but not conformant
// means a write. The three divergences tested each cut telemetry differently,
// and none show up on screen.
func TestApplyTrapReceiverRewritesDivergentReceiver(t *testing.T) {
	var cas = []struct {
		name    string
		present TrapReceiver
	}{
		// A v1 receiver: the form default, and the collector will get nothing
		// usable from it.
		{"declared as SNMPv1", func() TrapReceiver {
			var r = trapPresent("10.99.200.9")
			r.SNMPVersion = snmpVersionV1
			return r
		}()},
		// An Inform expects an acknowledgement the proxy does not send.
		{"declared as Inform", func() TrapReceiver {
			var r = trapPresent("10.99.200.9")
			r.NotifyType = notifyTypeInform
			return r
		}()},
		// An OID filter drops traps SILENTLY, including link traps.
		{"with an attached filter", func() TrapReceiver {
			var r = trapPresent("10.99.200.9")
			r.Filter, r.FilterName = filterAttached, "seulement-poe"
			return r
		}()},
		// Port: traps would leave for somewhere nobody listens.
		{"on another port", func() TrapReceiver {
			var r = trapPresent("10.99.200.9")
			r.Port = "1620"
			return r
		}()},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var seen []captured
			var f = fakeTrapReady()
			f.v2c = []TrapReceiver{c.present}
			var srv = newSNMPSwitch(t, &seen, f)

			var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(),
				wantTrapV2c())
			if err != nil {
				t.Fatalf("ApplyTrapReceiver: %v", err)
			}
			if !res.Set {
				t.Error("a non-conformant receiver must be rewritten")
			}
			// Rewritten IN PLACE: the table must not grow.
			if len(f.v2c) != 1 {
				t.Errorf("a duplicate was created: %+v", f.v2c)
			}
		})
	}
}

// TestApplyTrapReceiverDetectsWriteWithoutEffect is THE test of this batch.
//
// On this product line, `statusCode 0` does not mean « done »: it is a confirmed
// failure mode. And here the write is a reconstruction never executed — success
// announced without reread would therefore be monitoring announced on a mute
// device, discovered only at the first undetected outage.
func TestApplyTrapReceiverDetectsWriteWithoutEffect(t *testing.T) {
	var cas = []struct {
		name string
		want TrapDestination
	}{
		{"v2c", wantTrapV2c()},
		{"v3", wantTrapV3()},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var seen []captured
			var f = fakeTrapReady()
			f.deaf = true // answers 0, retains nothing
			var srv = newSNMPSwitch(t, &seen, f)

			var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), c.want)
			if err == nil {
				t.Fatal("code 0 with no effect must be an error, not success")
			}
			if res.Set {
				t.Error("nothing was set: the result must not say otherwise")
			}
			if !strings.Contains(err.Error(), "WITHOUT EFFECT") {
				t.Errorf("message must say the write had no effect: %v", err)
			}
			// The table was EMPTY: this refusal cannot be a ceiling. The message
			// must point at the write gesture, which is this batch's hypothesis —
			// otherwise the next batch will hunt a full table on an empty device.
			if !strings.Contains(err.Error(), "reconstruction") {
				t.Errorf("message must challenge the write gesture: %v", err)
			}
			var full *TrapTableFullError
			if errors.As(err, &full) {
				t.Errorf("an empty table is not a full table: %v", err)
			}
		})
	}
}

// TestApplyTrapReceiverFullTable: refusal is the ONLY way to discover how many
// receivers this product line accepts — firmware only enforces its ceiling on
// add, and nothing on the client declares it. The error must therefore carry
// the measurement, not merely fail.
func TestApplyTrapReceiverFullTable(t *testing.T) {
	var cas = []struct {
		name   string
		refuse string
		deaf   bool
		silent bool
	}{
		// Explicit refusal: the device says no.
		{"explicit refusal", "4", false, false},
		// SILENT refusal: code 0, and nothing moved. Same conclusion, and that
		// matters — otherwise the ceiling would stay invisible.
		{"silent refusal", "", true, true},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var seen []captured
			var f = fakeTrapReady()
			// Two receivers already present at other addresses: the table works,
			// so the write gesture is not at fault.
			f.v2c = []TrapReceiver{trapPresent("10.99.200.7"), trapPresent("10.99.200.8")}
			f.refuse, f.deaf = c.refuse, c.deaf
			var srv = newSNMPSwitch(t, &seen, f)

			var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(),
				wantTrapV2c())
			if err == nil {
				t.Fatal("a refused add must be an error")
			}
			var full *TrapTableFullError
			if !errors.As(err, &full) {
				t.Fatalf("refusal must be usable as a table ceiling: %v", err)
			}
			// The measurement: two accepted, the third refused. Exactly what the
			// observation lacks, and what will fill the capability.
			if full.Accepted != 2 || full.Family != "v1/v2c" || full.Silent != c.silent {
				t.Errorf("ceiling mismeasured: %+v", full)
			}
			// `Known` carries the target family's table — here v1/v2c. The other
			// table's count has not vanished: it goes in a note.
			if res.Known != 2 {
				t.Errorf("result must carry the pre-write count: %+v", res)
			}
			if !strings.Contains(err.Error(), "2 accepted, 3 refused") {
				t.Errorf("message must state the observed limit: %v", err)
			}
			if !strings.Contains(err.Error(), "capability") {
				t.Errorf("message must say what to do with this measurement: %v", err)
			}
		})
	}
}

// TestApplyTrapReceiverRejectsUnknownV3User: a v3 receiver's `userName` is FREE
// TEXT that firmware does not check. A misnamed receiver registers without
// error and emits nothing authenticable — so the connector must verify BEFORE
// writing.
func TestApplyTrapReceiverRejectsUnknownV3User(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	delete(f.users, "mcpoll") // user was never set
	var srv = newSNMPSwitch(t, &seen, f)

	var _, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV3())
	if err == nil {
		t.Fatal("a v3 receiver naming a nonexistent user must be rejected")
	}
	if len(writes(seen)) != 0 {
		t.Errorf("nothing must be written: %+v", writes(seen))
	}
	if len(f.v3) != 0 {
		t.Errorf("no receiver must have been set: %+v", f.v3)
	}
}

// TestApplyTrapReceiverRejectsGroupWithoutNotifyView: the NOTIFICATION view,
// not the read view, lets the device address a trap to users of a group.
// Without it, everything looks in place and nothing leaves.
func TestApplyTrapReceiverRejectsGroupWithoutNotifyView(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.groupsNoNotify = map[string]bool{defaultV3Group: true}
	var srv = newSNMPSwitch(t, &seen, f)

	var _, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV3())
	if err == nil {
		t.Fatal("a group without a notification view cannot receive traps")
	}
	if !strings.Contains(err.Error(), "notifyView") {
		t.Errorf("message must name the field at fault: %v", err)
	}
	if len(writes(seen)) != 0 {
		t.Errorf("nothing must be written: %+v", writes(seen))
	}
}

// TestApplyTrapReceiverReplacesDivergentV3User: in v3, the user name is part of
// the entry KEY. A `set` carrying another name would add an entry instead of
// replacing the one present — and the old, inert one would keep occupying a
// slot in a table whose ceiling is unknown.
func TestApplyTrapReceiverReplacesDivergentV3User(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.v3 = []TrapReceiver{{
		Address: "10.99.200.9", Port: "162", NotifyType: notifyTypeTrap,
		User: "ancien", SecurityType: securityLevelAuthPriv, Filter: filterNone,
	}}
	var srv = newSNMPSwitch(t, &seen, f)

	var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV3())
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	if !res.Replaced || !res.Set {
		t.Errorf("replacement expected: %+v", res)
	}
	var delAt, setAt = -1, -1
	var i int
	var c captured
	for i, c = range writes(seen) {
		if strings.Contains(c.body, `<NotificationReceiverList action="delete">`) {
			delAt = i
		}
		if strings.Contains(c.body, `<NotificationReceiverList action="set">`) {
			setAt = i
		}
	}
	if delAt < 0 || setAt < 0 {
		t.Fatalf("delete then set expected: %+v", writes(seen))
	}
	if delAt > setAt {
		t.Error("delete must precede set: otherwise both entries coexist, the " +
			"old one emitting nothing authenticable")
	}
	// The deletion key comes from the READ: rebuilt from memory, it would match
	// no entry and deletion would succeed without deleting anything.
	var del = writes(seen)[delAt]
	var expectedKey = []string{
		"<receiverIPAddress>10.99.200.9</receiverIPAddress>",
		"<notificationType>1</notificationType>",
		"<userName>ancien</userName>",
		"<securityType>3</securityType>",
	}
	var s string
	for _, s = range expectedKey {
		if !strings.Contains(del.body, s) {
			t.Errorf("deletion key must carry %q: %s", s, del.body)
		}
	}
	if len(f.v3) != 1 || f.v3[0].User != "mcpoll" {
		t.Errorf("exactly one receiver must remain, under the right name: %+v", f.v3)
	}
}

// TestApplyTrapReceiverDetectsDuplicate: the connector ASSUMES a `set` replaces
// the same-address entry. If the table is indexed otherwise, the device stacks
// — and two receivers for one collector means every event counted twice and a
// lost slot in a table whose ceiling is unknown. Reread must see it.
func TestApplyTrapReceiverDetectsDuplicate(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.duplicate = true
	var present = trapPresent("10.99.200.9")
	present.Community = "autre" // divergent, so a rewrite is needed
	f.v2c = []TrapReceiver{present}
	var srv = newSNMPSwitch(t, &seen, f)

	var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV2c())
	if err == nil {
		t.Fatal("two receivers for the same address must be an error")
	}
	if res.Set {
		t.Error("a non-conformant set must not be announced as done")
	}
	if !strings.Contains(err.Error(), "ADDED") {
		t.Errorf("message must say the device stacked entries: %v", err)
	}
}

// TestApplyTrapReceiverDetectsSNMPVersion1: the device falls back to its own
// form default. That is the documented observation trap, and it only shows on
// reread — a v1 receiver is declared, present, and unusable.
func TestApplyTrapReceiverDetectsSNMPVersion1(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.forceV1 = true
	var srv = newSNMPSwitch(t, &seen, f)

	var _, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV2c())
	if err == nil {
		t.Fatal("a receiver registered as SNMPv1 must be an error")
	}
	if !strings.Contains(err.Error(), "SNMPVersion") {
		t.Errorf("message must name the field at fault: %v", err)
	}
}

// TestApplyTrapReceiverRejectsBeforeWrite: nothing leaves until everything is
// namable. A half-valid receiver is worse than no receiver — it suggests a
// channel in service.
func TestApplyTrapReceiverRejectsBeforeWrite(t *testing.T) {
	var cas = []struct {
		name string
		want TrapDestination
	}{
		{"empty address", TrapDestination{Community: "mctrap"}},
		// The field is validated `unicastipv4` on this family.
		{"IPv6", TrapDestination{Address: "2001:db8::1", Community: "mctrap"}},
		{"loopback", TrapDestination{Address: "127.0.0.1", Community: "mctrap"}},
		{"multicast", TrapDestination{Address: "224.0.0.1", Community: "mctrap"}},
		{"port out of range", TrapDestination{
			Address: "10.99.200.9", Community: "mctrap", Port: 70000}},
		// Neither: there is nothing to declare.
		{"no identity", TrapDestination{Address: "10.99.200.9"}},
		// Both: tables are distinct, and the device would emit every event
		// twice toward the same collector.
		{"both identities", TrapDestination{
			Address: "10.99.200.9", Community: "mctrap", User: "mcpoll"}},
		{"community too long", TrapDestination{Address: "10.99.200.9",
			Community: strings.Repeat("x", communityMaxLen+1)}},
		{"user with space", TrapDestination{
			Address: "10.99.200.9", User: "with space"}},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var seen []captured
			var srv = newSNMPSwitch(t, &seen, fakeTrapReady())

			var _, err = adminOn(srv).ApplyTrapReceiver(context.Background(), c.want)
			if err == nil {
				t.Fatal("this receiver should be rejected")
			}
			// Not even a READ: validation must not cost a session among the five
			// this product line accepts.
			if len(seen) != 0 {
				t.Errorf("nothing must be sent to the device: %+v", seen)
			}
		})
	}
}

// TestApplyTrapReceiverRejectsMuteEquipment: three switches decide emission, and
// NO UI page writes them. The connector therefore checks them and refuses — it
// does not set them: a field no UI path writes is a field whose write acceptance
// is unknown.
func TestApplyTrapReceiverRejectsMuteEquipment(t *testing.T) {
	var cas = []struct {
		name    string
		prepare func(*fakeSNMP)
		want    TrapDestination
		motif   string
	}{
		{"SNMP disabled", func(f *fakeSNMP) { f.enabled = false },
			wantTrapV2c(), "ApplySNMP"},
		{"trap emission cut", func(f *fakeSNMP) { f.trapsEnbl = snmpDisabledCode },
			wantTrapV2c(), "trapsEnbl"},
		{"v1/v2c table disabled", func(f *fakeSNMP) { f.v1v2Enabled = snmpDisabledCode },
			wantTrapV2c(), "enabled"},
	}
	var c = cas[0]
	for _, c = range cas {
		t.Run(c.name, func(t *testing.T) {
			var seen []captured
			var f = fakeTrapReady()
			c.prepare(f)
			var srv = newSNMPSwitch(t, &seen, f)

			var _, err = adminOn(srv).ApplyTrapReceiver(context.Background(), c.want)
			if err == nil {
				t.Fatal("a device that will emit nothing must be rejected, not configured")
			}
			if !strings.Contains(err.Error(), c.motif) {
				t.Errorf("message must name the cause (%s): %v", c.motif, err)
			}
			if len(writes(seen)) != 0 {
				t.Errorf("nothing must be written, especially not those switches: %+v",
					writes(seen))
			}
		})
	}
}

// TestApplyTrapReceiverMissingSwitchIsNotRefusal: « field absent » and
// « field set to 2 » are not the same. Refusing on absence would cut the trap
// channel on any firmware that does not render the field, when nothing was
// observed — so the caveat goes in the result, not in a refusal.
func TestApplyTrapReceiverMissingSwitchIsNotRefusal(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.trapsEnbl = "" // firmware does not render the field
	var srv = newSNMPSwitch(t, &seen, f)

	var res, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV2c())
	if err != nil {
		t.Fatalf("a missing field must not fail the set: %v", err)
	}
	if !res.Set {
		t.Error("receiver should be set")
	}
	if len(res.Notes) == 0 {
		t.Error("inability to verify emission must be STATED: " +
			"a receiver set on a possibly mute device is not monitoring")
	}
}

// TestApplyTrapReceiverDoesNotLogCommunity: a trap community opens the device
// over SNMP exactly like the read community. The transcript is shown to the
// operator and retained.
func TestApplyTrapReceiverDoesNotLogCommunity(t *testing.T) {
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, fakeTrapReady())
	var a = adminOn(srv)

	var _, err = a.ApplyTrapReceiver(context.Background(), wantTrapV2c())
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	var st Step
	for _, st = range a.Steps {
		if strings.Contains(st.What, "mctrap") || strings.Contains(st.Detail, "mctrap") {
			t.Errorf("trap community in transcript: %q / %q",
				st.What, st.Detail)
		}
	}
}

// TestApplyTrapReceiverSavesToFlash: without a save, the receiver disappears on
// the first reboot — and the device would look monitored while emitting nothing.
func TestApplyTrapReceiverSavesToFlash(t *testing.T) {
	var seen []captured
	var srv = newSNMPSwitch(t, &seen, fakeTrapReady())

	var _, err = adminOn(srv).ApplyTrapReceiver(context.Background(), wantTrapV2c())
	if err != nil {
		t.Fatalf("ApplyTrapReceiver: %v", err)
	}
	var w = writes(seen)
	if len(w) == 0 || w[len(w)-1].rawQuery != "{file=/hpe/home.htm}{FileCopy}" {
		t.Errorf("save must close the operation: %+v", w)
	}
}

// TestReadTrapReceiversReadsBothTablesInOneRequest: two sections, one request.
// This product line accepts only five web sessions — reading one section at a
// time would kick the human administrator out of their own UI.
func TestReadTrapReceiversReadsBothTablesInOneRequest(t *testing.T) {
	var seen []captured
	var f = fakeTrapReady()
	f.v2c = []TrapReceiver{trapPresent("10.99.200.7")}
	f.v3 = []TrapReceiver{{
		Address: "10.99.200.8", Port: "1620", NotifyType: notifyTypeInform,
		User: "collecteur", SecurityType: securityLevelAuthNoPriv,
		Filter: filterAttached, FilterName: "seulement-poe",
	}}
	var srv = newSNMPSwitch(t, &seen, f)

	var st, err = adminOn(srv).ReadTrapReceivers(context.Background())
	if err != nil {
		t.Fatalf("ReadTrapReceivers: %v", err)
	}
	if len(seen) != 1 {
		t.Errorf("1 request expected, got %d", len(seen))
	}
	if !strings.Contains(seen[0].rawQuery, "{"+sectionSNMPTrap+"}") ||
		!strings.Contains(seen[0].rawQuery, "{"+sectionTrapReceiversV3+"}") {
		t.Errorf("both sections must be requested together: %q",
			seen[0].rawQuery)
	}
	if !st.SNMPEnabled || st.Emitting != snmpEnabledCode ||
		st.V1V2Enabled != snmpEnabledCode {
		t.Errorf("switches misread: %+v", st)
	}
	if len(st.V2c) != 1 || st.V2c[0].SNMPVersion != snmpVersionV2 ||
		st.V2c[0].Community != "mctrap" {
		t.Errorf("v1/v2c receiver misread: %+v", st.V2c)
	}
	// `managementStationIPAddress` is absent from the add form: the device
	// RENDERS it, and it is the deletion key. Not reading it would make every
	// v1/v2c deletion impossible to build.
	if st.V2c[0].Station != "0.0.0.0" {
		t.Errorf("deletion key must be read: %+v", st.V2c[0])
	}
	// v3 is read as-is, Inform and filter included: that is device state, not
	// what one might have wanted to see.
	if len(st.V3) != 1 || st.V3[0].User != "collecteur" ||
		st.V3[0].NotifyType != notifyTypeInform || st.V3[0].FilterName != "seulement-poe" {
		t.Errorf("v3 receiver misread: %+v", st.V3)
	}
	// The two tables do not mix: a v3 receiver read among v1/v2c would look like
	// a duplicate, or the reverse.
	if st.V2c[0].User != "" || st.V3[0].Community != "" {
		t.Errorf("the two tables mixed: %+v / %+v", st.V2c, st.V3)
	}
}

// TestSectionOfDoesNotConfuseSNMPTrapAndItsList: `SNMPTrapReceiverList` is not
// a section, it is a child of `SNMPTrap` — and its name STARTS with the
// section's. Approximate isolation would take one for the other.
func TestSectionOfDoesNotConfuseSNMPTrapAndItsList(t *testing.T) {
	var body = `<ResponseData><DeviceConfiguration>` +
		`<SNMPTrap type="section"><enabled>1</enabled><SNMPTrapReceiverList>` +
		`<SNMPTrapReceiverEntry><receiverIPAddress>10.0.0.1</receiverIPAddress>` +
		`</SNMPTrapReceiverEntry></SNMPTrapReceiverList></SNMPTrap>` +
		`<NotificationReceiverList type="section"><Receiver>` +
		`<userName>mcpoll</userName></Receiver></NotificationReceiverList>` +
		`</DeviceConfiguration></ResponseData>`

	var v1v2 = sectionOf(body, sectionSNMPTrap)
	if !strings.Contains(v1v2, "10.0.0.1") {
		t.Errorf("v1/v2c section must contain its child list: %s", v1v2)
	}
	// It must NOT spill into the v3 section right after: the end bound is
	// exactly `</SNMPTrap>`, and `</SNMPTrapReceiverList>` must not satisfy it.
	if strings.Contains(v1v2, "mcpoll") {
		t.Errorf("section spilled into the v3 table: %s", v1v2)
	}
	// And the reverse: the v3 table must not pick up v1/v2c.
	var v3 = sectionOf(body, sectionTrapReceiversV3)
	if strings.Contains(v3, "receiverIPAddress") || !strings.Contains(v3, "mcpoll") {
		t.Errorf("v3 table poorly isolated: %s", v3)
	}
}

// bodies extracts bodies from a sequence of captured requests.
func bodies(cs []captured) []string {
	var out = make([]string, 0, len(cs))
	var c captured
	for _, c = range cs {
		out = append(out, c.body)
	}
	return out
}

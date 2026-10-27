package swos

import (
	"reflect"
	"testing"

	"meshcraft/internal/driver"
)

// Identification, and this platform's pleasant surprise: the model is obtained
// WITHOUT the slightest credential.

// TestModelInTheRealm: the Digest challenge carries the exact reference.
//
// This is the REAL challenge from the equipment, captured by hitting /sys.b with no
// credentials:
//
//	HTTP/1.1 401 Unauthorized
//	WWW-Authenticate: Digest realm="CSS326-24G-2S+", qop="auth",
//	                  nonce="84aaad9d", stale=FALSE
//
// Where the Instant On connector must dig through the hidden fields of a login
// page in hopes of a descriptor, here it's enough to knock on the door. This is
// what lets discovery pick the right capabilities — and therefore the right
// facade geometry — before it even has a secret.
func TestModelInTheRealm(t *testing.T) {
	var challenge = `Digest realm="CSS326-24G-2S+", qop="auth", nonce="84aaad9d", stale=FALSE`
	var model, ok = ModelFromRealm(challenge)
	if !ok || model != "CSS326-24G-2S+" {
		t.Fatalf("ModelFromRealm = %q (ok=%v), expected CSS326-24G-2S+", model, ok)
	}
	if FamilyOf(model) != "CSS" {
		t.Errorf("FamilyOf(%q) = %q, expected CSS", model, FamilyOf(model))
	}
}

// TestUnexpectedRealmRefused: a realm that isn't a MikroTik reference means
// we aren't talking to a SwOS device.
//
// Saying so is better than forcing the equipment into this range anyway: the
// capabilities that follow from it decide what gets written.
func TestUnexpectedRealmRefused(t *testing.T) {
	var cases = []string{
		"",
		"Basic realm=\"index.html\"",
		`Digest realm="RouterOS", qop="auth", nonce="x"`,
		`Digest realm="TP-LINK", qop="auth", nonce="x"`,
		`Digest qop="auth", nonce="x"`, // no realm at all
	}
	var c string
	for _, c = range cases {
		var model, ok = ModelFromRealm(c)
		if ok {
			t.Errorf("ModelFromRealm(%q) latched onto %q", c, model)
		}
	}
	// And the two sub-ranges that SwOS runs are indeed recognized.
	var m, crsOK = ModelFromRealm(`Digest realm="CRS328-24P-4S+", qop="auth", nonce="x"`)
	if !crsOK || m != "CRS328-24P-4S+" || FamilyOf(m) != "CRS" {
		t.Errorf("ModelFromRealm on a CRS = %q (ok=%v)", m, crsOK)
	}
}

// TestHeaderIsCaseInsensitive: the header is found regardless of case.
//
// `driver.Response.Headers` is a plain map that carries keys in whatever case the
// SERVER sent them. A direct lookup by "WWW-Authenticate" would return an empty
// string the day a firmware capitalizes it differently — which would lead to
// concluding "this isn't a SwOS" about equipment that actually is one.
func TestHeaderIsCaseInsensitive(t *testing.T) {
	var cases = []string{"WWW-Authenticate", "Www-Authenticate", "www-authenticate"}
	var k string
	for _, k = range cases {
		var h = map[string][]string{k: {`Digest realm="CSS326-24G-2S+"`}}
		var got = header(h, hdrAuthenticate)
		if got == "" {
			t.Errorf("header %q not found", k)
		}
	}
	var absent = header(map[string][]string{}, hdrAuthenticate)
	if absent != "" {
		t.Errorf("missing header returns %q", absent)
	}
	// An empty value must not be mistaken for a value.
	var sansValeur = header(map[string][]string{"WWW-Authenticate": {}}, hdrAuthenticate)
	if sansValeur != "" {
		t.Errorf("header with no value returns %q", sansValeur)
	}
}

// TestIdentityFromRealCaptures: what /sys.b and /link.b add to the realm.
//
// The realm gives the model; everything else — serial, firmware, system name, MAC,
// facade, management addressing — requires authenticating. This test replays the two
// pages exactly as the equipment served them.
func TestIdentityFromRealCaptures(t *testing.T) {
	var sys, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b: %v", err)
	}
	var link *Page
	link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b: %v", err)
	}

	// Start from what the realm gave, exactly like Inspect does.
	var id = Identity{Model: "CSS326-24G-2S+", Family: "CSS", Source: "digest_realm"}
	fillFromSys(&id, sys)
	var fillErr = fillFromLink(&id, link)
	if fillErr != nil {
		t.Fatalf("facade unreadable: %v", fillErr)
	}

	var cases = []struct {
		field, got, want string
	}{
		{"model", id.Model, "CSS326-24G-2S+"},
		{"family", id.Family, "CSS"},
		{"serial", id.Serial, "945809C232D5"},
		{"firmware", id.Firmware, "2.17"},
		{"system name", id.Hostname, "lan-m6"},
		{"MAC", id.MAC, "b8:69:f4:5f:14:a8"},
		{"management address", id.MgmtIP, "192.0.2.36"},
		// The source must switch to `sys.b`: the model was CONFIRMED from the
		// inside, and the interface must not present at the same rank what comes from
		// the front door and what comes from the inside.
		{"source", id.Source, "sys.b"},
	}
	var c = cases[0]
	for _, c = range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, expected %q", c.field, c.got, c.want)
		}
	}
	if id.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d, expected 99", id.MgmtVLAN)
	}
	// The firmware also carries its build timestamp, which the equipment's
	// interface displays next to the version.
	if id.Build == "" {
		t.Error("the build timestamp (`bld`) was not read")
	}
	// This model has neither a revision nor a commercial name: the capture serves them
	// EMPTY, and nothing should be invented in their place.
	if id.Revision != "" || id.BoardName != "" {
		t.Errorf("revision and commercial name are empty in the capture, got %q / %q",
			id.Revision, id.BoardName)
	}
	// The facade.
	if id.CopperPorts != 24 || id.SFPPorts != 2 || id.SFPFirst != 24 || !id.SFPPlus {
		t.Errorf("facade = %d copper, %d SFP (first at %d, SFP+ = %v); expected 24/2/24/true",
			id.CopperPorts, id.SFPPorts, id.SFPFirst, id.SFPPlus)
	}
	if id.PoE {
		t.Error("the CSS326-24G-2S+ has no PoE")
	}
}

// TestFaceplateGroupsNamedLikeTheDevice: the recorded facade carries the names
// the switch itself uses.
//
// TWO FAMILIES, each restarting at 1: "Port1"… "Port24" for copper, "SFP1"
// and "SFP2" for the cages. This is what /link.b serves in `nm`, and it's what
// the equipment's interface displays — so it's what the operator reads on the two screens
// being compared.
//
// This test used to carry the OPPOSITE: "the SFP cages CONTINUE copper numbering (25 and
// 26 on a 24-port model)". That was the Instant On connector's convention, borrowed so
// a mixed fleet would compare — at the cost of a mental cross-check left to do in front of
// every MikroTik. The neighboring RouterOS connector never paid that cost: its groups are
// `ether` starting at 1 and `sfp-sfpplus` starting at 1. The old names remain ACCEPTED
// on input (cf. TestIndexApprendLesDeuxFamilles): it's the groups that change, not
// the ability to re-read what the database already carries.
//
// These groups aren't decorative: they're what the web interface expands to enumerate
// the ports (`expandPorts`), and what internal/portrules draws the media from — hence the
// "SFP+" label being kept, which is what causes the cage to be classified as 10 gigabit.
func TestFaceplateGroupsNamedLikeTheDevice(t *testing.T) {
	var id = Identity{
		Model: "CSS326-24G-2S+", CopperPorts: 24, SFPPorts: 2, SFPFirst: 24, SFPPlus: true,
	}
	var groups = id.PortGroups()
	if len(groups) != 2 {
		t.Fatalf("%d group(s), expected 2", len(groups))
	}
	if groups[0]["prefix"] != CopperPortPrefix || groups[0]["from"] != 1 ||
		groups[0]["count"] != 24 || groups[0]["speed"] != "1G" {
		t.Errorf("copper group = %+v; expected \"Port1\" … \"Port24\" at 1G", groups[0])
	}
	if groups[1]["prefix"] != SFPPortPrefix || groups[1]["from"] != 1 ||
		groups[1]["count"] != 2 || groups[1]["speed"] != "10G" ||
		groups[1]["label"] != "SFP+" {
		t.Errorf("SFP group = %+v; expected \"SFP1\" and \"SFP2\", 10G, label SFP+",
			groups[1])
	}

	// AND THE EXPANDED NAMES MUST BE THE SAME AS THE CONNECTOR'S FACADE. This is the
	// cross-check that matters: the state reader names its ports with [Facade], the screen
	// enumerates them with these groups, and the two lists must coincide — otherwise a
	// read configuration exists without any port on the screen carrying it.
	var deplie []string
	var g map[string]any
	for _, g = range groups {
		var from = g["from"].(int)
		var i int
		for i = 0; i < g["count"].(int); i++ {
			deplie = append(deplie, g["prefix"].(string)+itoa(from+i))
		}
	}
	if !reflect.DeepEqual(deplie, facadeCSS326().Names()) {
		t.Errorf("expanded facade = %v\nconnector names = %v", deplie,
			facadeCSS326().Names())
	}
}

// TestGeometryWithoutCageOffset: the fallback when the firmware doesn't say where the
// cages are.
//
// The `sfpo` field is OPTIONAL, and the equipment's interface then places the
// cages at the end (`t = void 0==d.sfpo ? da : d.sfpo`). Reproducing this fallback is
// necessary: an older firmware may omit it, and assuming 0 would place the
// cages at the front of the facade.
func TestGeometryWithoutCageOffset(t *testing.T) {
	var page, err = ParseObject("{prt:0x1a,sfp:0x02}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var ports, sfp, first, geoErr = geometryOf(page)
	if geoErr != nil {
		t.Fatalf("geometry refused: %v", geoErr)
	}
	if ports != 26 || sfp != 2 || first != 24 {
		t.Errorf("geometry = %d/%d/%d, expected 26/2/24 (cages at the end)", ports, sfp, first)
	}
}

// TestInconsistentGeometryRefused: a geometry that makes no sense must stop
// the read.
//
// The port count decides the field set to send back on /fwd.b: getting it wrong
// would send a truncated page, and the equipment would be free to reset to zero
// the port isolation of all its ports.
func TestInconsistentGeometryRefused(t *testing.T) {
	var cases = []struct{ name, src string }{
		{"no port count", "{sfp:0x02}"},
		{"zero ports", "{prt:0x00}"},
		{"more cages than ports", "{prt:0x02,sfp:0x04}"},
		{"cages outside the facade", "{prt:0x1a,sfp:0x02,sfpo:0x40}"},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var page, err = ParseObject(c.src)
			if err != nil {
				t.Fatalf("page refused by the parser: %v", err)
			}
			if _, _, _, geoErr = geometryOf(page); geoErr == nil {
				t.Error("inconsistent geometry accepted")
			}
		})
	}
}

// geoErr is declared at file scope so the test loop above
// doesn't need `:=`, which is forbidden in this repository.
var geoErr error

// TestAddressingRoundTrip: decoding and encoding an address agree.
//
// This is THE detail that makes you set a wrong address. Byte order is
// little-endian: the first octet in dotted notation is the low-order
// byte. Checking it in both directions is what proves the two functions weren't
// simply inverted in a consistently wrong way.
func TestAddressingRoundTrip(t *testing.T) {
	// The left-hand value is what the equipment actually served for
	// the address it answered on.
	var cases = []struct {
		brut   uint32
		pointe string
	}{
		{0x240200c0, "192.0.2.36"},
		{0x0100a8c0, "192.168.0.1"},
		{0x00000000, "0.0.0.0"},
		{0xffffffff, "255.255.255.255"},
	}
	var c = cases[0]
	for _, c = range cases {
		var got = DecodeIPv4(c.brut)
		if got != c.pointe {
			t.Errorf("DecodeIPv4(%#x) = %q, expected %q", c.brut, got, c.pointe)
		}
		var absent, ok = EncodeIPv4(c.pointe)
		if !ok || absent != c.brut {
			t.Errorf("EncodeIPv4(%q) = %#x (ok=%v), expected %#x", c.pointe, absent, ok, c.brut)
		}
	}
	// Something that isn't an address must be refused, not turned into zero: setting
	// 0.0.0.0 while believing an address was set would make the equipment unreachable.
	var mauvaises = []string{"", "192.0.2", "192.0.2.256", "192.0.2.36.1", "ten.99.0.36"}
	var s string
	for _, s = range mauvaises {
		var _, crsOK = EncodeIPv4(s)
		if crsOK {
			t.Errorf("EncodeIPv4(%q) was accepted", s)
		}
	}
}

// TestMACDecoding: no byte-order inversion, and refusal of anything that isn't a MAC.
func TestMACDecoding(t *testing.T) {
	var got, ok = DecodeMAC("b869f45f14a8")
	if !ok || got != "b8:69:f4:5f:14:a8" {
		t.Errorf("DecodeMAC = %q (ok=%v)", got, ok)
	}
	var mauvaises = []string{"", "b869f45f14", "b869f45f14a8ff", "zz69f45f14a8"}
	var s string
	for _, s = range mauvaises {
		var _, mauvaisOK = DecodeMAC(s)
		if mauvaisOK {
			t.Errorf("DecodeMAC(%q) accepted", s)
		}
	}
}

// TestFingerprintRecognition: SwOS is recognized by its name, and RouterOS is excluded.
//
// The RouterOS exclusion is the only case in this project where two connectors
// compete for the same chassis: a CRS boots under one system or the other, and their
// capabilities have nothing in common.
func TestFingerprintRecognition(t *testing.T) {
	var d = &Driver{}
	var cases = []struct {
		name     string
		ev       driver.Evidence
		want     int
		operator string
	}{
		{
			name: "title and model",
			ev: driver.Evidence{
				HTTPTitle: "MikroTik SwOS",
				HTTPBody:  `Digest realm="CSS326-24G-2S+"`,
			},
			want: 98,
		},
		{
			name: "title alone",
			ev:   driver.Evidence{HTTPTitle: "MikroTik SwOS"},
			want: 95,
		},
		{
			name: "model alone, without the system name",
			ev:   driver.Evidence{HTTPBody: `Digest realm="CSS610-8G-2S+"`},
			want: 55,
		},
		{
			// The chassis is a CRS, but it's RouterOS that booted: it's not
			// meant for this connector.
			name: "CRS running RouterOS",
			ev: driver.Evidence{
				HTTPTitle: "RouterOS",
				SysDescr:  "RouterOS CRS328-24P-4S+",
			},
			want: 0,
		},
		{
			name: "other platform",
			ev:   driver.Evidence{HTTPTitle: "Instant On - Hewlett Packard Enterprise"},
			want: 0,
		},
		{name: "nothing", ev: driver.Evidence{}, want: 0},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = d.Fingerprint(c.ev)
			if got != c.want {
				t.Errorf("Fingerprint = %d, expected %d", got, c.want)
			}
		})
	}
}

// TestFwdFieldSetFollowsTheSize: the port count decides the field set.
//
// /fwd.b is the only page whose writable set depends on the model's size:
// it carries a SOURCE-port isolation mask. Copying the field set observed
// on a 26-port model as-is onto a smaller model would send masks for ports that
// don't exist; onto a bigger one, it would omit some, and the equipment would be free
// to reset port isolation to zero.
//
// This is also why the geometry is READ from /link.b and never deduced from the
// model name: a naming convention can be wrong, equipment asked about its own
// geometry cannot.
func TestFwdFieldSetFollowsTheSize(t *testing.T) {
	var petits = FwdWritableFields(10)
	var grands = FwdWritableFields(26)
	if len(grands)-len(petits) != 16 {
		t.Errorf("a 26-port model must carry 16 more isolation masks than a "+
			"10-port one; got %d and %d fields", len(grands), len(petits))
	}
	if petits[0] != "fp1" || petits[9] != "fp10" {
		t.Errorf("isolation masks must be numbered starting at 1: %v", petits[:10])
	}
	// The common fields come AFTER, in the order of the equipment's
	// backup.
	if petits[10] != "lck" || grands[26] != "lck" {
		t.Errorf("the order of common fields doesn't follow that of the backup")
	}
	// And the native VLAN, which is what this connector writes, is indeed there.
	var trouve bool
	var f string
	for _, f = range grands {
		if f == FieldDefaultVLAN {
			trouve = true
		}
	}
	if !trouve {
		t.Errorf("the writable field set of /fwd.b must carry %q", FieldDefaultVLAN)
	}
}

// TestPortGroupsEmptyWithoutFaceplate: with no facade read, no ports are invented.
func TestPortGroupsEmptyWithoutFaceplate(t *testing.T) {
	var id = Identity{Model: "CSS326-24G-2S+"}
	if !reflect.DeepEqual(id.PortGroups(), []map[string]any{}) {
		t.Errorf("PortGroups with no facade = %v, expected empty", id.PortGroups())
	}
}

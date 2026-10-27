package swos

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The `*.b` page parser, verified on the device's REAL CAPTURES.
//
// The files in testdata/ are not written examples: they are the responses as
// the reference MikroTik CSS326-24G-2S+ (SwOS 2.17, 192.0.2.36) served them,
// brought back through the proxy's tunnel. That is what gives these tests
// their value — a parser validated on a syntax one wrote oneself proves
// nothing about what the device actually sends.
//
// The only edit made to these captures is the production device's SNMP
// COMMUNITY, replaced with a placeholder of the same shape in snmp.b and
// backup.swb: a production secret has no business in a repository.

// capture reads a real capture.
func capture(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("capture %s unreadable: %v", name, err)
	}
	return string(b)
}

// TestParseRealSys: the densest page of the platform, as served.
func TestParseRealSys(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}

	// Strings are hex-encoded UTF-8, and this decoding is what gives the
	// connector its identification.
	var cases = []struct {
		field, expected string
	}{
		{FieldBoard, "CSS326-24G-2S+"},
		{FieldVersion, "2.17"},
		{FieldIdentity, "lan-m6"},
		{FieldSerial, "945809C232D5"},
	}
	var c = cases[0]
	for _, c = range cases {
		var got, ok = page.Text(c.field)
		if !ok || got != c.expected {
			t.Errorf("%s = %q (ok=%v), expected %q", c.field, got, ok, c.expected)
		}
	}

	// THE ADDRESS, and the byte order. This decoding is what would display —
	// then write — a wrong address if it were reversed.
	var ip, ipOK = page.Num(FieldIP)
	if !ipOK {
		t.Fatalf("/sys.b does not carry %q", FieldIP)
	}
	if ip != 0x240200c0 {
		t.Fatalf("raw value of %s = %#x, the capture carries 0x240200c0", FieldIP, ip)
	}
	var dotted = DecodeIPv4(ip)
	if dotted != "192.0.2.36" {
		t.Errorf("DecodeIPv4(%#x) = %q, expected 192.0.2.36 — it is the address "+
			"the device answered this very capture on", ip, dotted)
	}

	// The MAC, without reversal.
	var raw, rawOK = page.Value(FieldMAC)
	if !rawOK || raw.Kind != KindStr {
		t.Fatalf("%s missing or of unexpected type", FieldMAC)
	}
	var mac, macOK = DecodeMAC(raw.Raw)
	if !macOK || mac != "b8:69:f4:5f:14:a8" {
		t.Errorf("DecodeMAC(%q) = %q (ok=%v), expected b8:69:f4:5f:14:a8", raw.Raw, mac, macOK)
	}

	// This device's management VLAN.
	var avln, has = page.Num(FieldAllowVLAN)
	if !has || avln != 99 {
		t.Errorf("%s = %d (ok=%v), expected 99 — it is the VLAN this "+
			"device is administered through", FieldAllowVLAN, avln, has)
	}
	// Static addressing.
	var iptp, iptpOK = page.Num(FieldAddrMode)
	if !iptpOK || iptp != AddrStatic {
		t.Errorf("%s = %d, expected %d (static)", FieldAddrMode, iptp, AddrStatic)
	}
}

// TestMissingFieldIsNotZero: the parser's central stance.
//
// The device's UI does the OPPOSITE — its accessor returns the default value
// declared in the field definition when the page does not carry the key. That
// is acceptable for drawing a form and unacceptable here: "this port is in
// VLAN 0" and "the page says nothing about this port" lead to opposite writes,
// and one of the two cuts an uplink.
func TestMissingFieldIsNotZero(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}

	// Every accessor must refuse, for two distinct reasons: the key does not
	// exist, or it exists but carries ANOTHER SHAPE. Reading an array as a
	// scalar would give a wrong port mask, hence a write on the wrong ports.
	var refusals = []struct {
		what string
		read func() bool // returns the accessor's second return value
	}{
		{"Num on a missing key — the field would be read as zero",
			func() bool { var _, ok = page.Num("doesnotexist"); return ok }},
		{"Text on a missing key",
			func() bool { var _, ok = page.Text("doesnotexist"); return ok }},
		{"Nums on a missing key",
			func() bool { var _, ok = page.Nums("doesnotexist"); return ok }},
		{"Bit on a missing key — the mask would be read as \"no port\"",
			func() bool { var _, ok = page.Bit("doesnotexist", 0); return ok }},
		{"NumAt on a missing key",
			func() bool { var _, ok = page.NumAt("doesnotexist", 0); return ok }},
		{"Num on a string",
			func() bool { var _, ok = page.Num(FieldBoard); return ok }},
		{"Text on a number",
			func() bool { var _, ok = page.Text(FieldIP); return ok }},
		{"Nums on a scalar",
			func() bool { var _, ok = page.Nums(FieldIP); return ok }},
		{"Obj on a number",
			func() bool { var _, ok = page.Obj(FieldIP); return ok }},
	}
	var r struct {
		what string
		read func() bool
	}
	for _, r = range refusals {
		if r.read() {
			t.Errorf("%s: the accessor returned ok=true", r.what)
		}
	}
	if page.Has("doesnotexist") {
		t.Error("Has on a missing key returns true")
	}
	// An out-of-bounds array index is not a zero either.
	var _, inBounds = page.NumAt(FieldAllowVLAN, 99)
	if inBounds {
		t.Error("out-of-bounds NumAt returns ok=true")
	}

	// And a zero ACTUALLY present is distinct from an absence: that is the
	// other half of the property, without which "cautious" would become
	// "blind".
	var alla, allaOK = page.Num(FieldAllowFrom)
	if !allaOK {
		t.Fatalf("%s should be present in the capture", FieldAllowFrom)
	}
	if alla != 0 {
		t.Errorf("%s = %#x, the capture carries 0 (no address restriction)",
			FieldAllowFrom, alla)
	}
}

// TestParseLinkGeometryAndMasks: the model's geometry and the bit order.
//
// Getting the bit order wrong gives a SHIFTED port mask, i.e. a configuration
// applied to the wrong ports. On the capture, a single port carries a link,
// and it can be verified three independent ways.
func TestParseLinkGeometryAndMasks(t *testing.T) {
	var page, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b refused: %v", err)
	}

	var ports, sfp, first, geoErr = geometryOf(page)
	if geoErr != nil {
		t.Fatalf("geometry unreadable: %v", geoErr)
	}
	if ports != 26 || sfp != 2 || first != 24 {
		t.Errorf("geometry = %d ports, %d SFP, first cage at %d; expected 26/2/24 "+
			"for a CSS326-24G-2S+", ports, sfp, first)
	}

	// The link mask: `lnk:0x02000000`, hence bit 25, hence port 26 — i.e. the
	// SECOND SFP cage. Bit 0 = port 1.
	var i int
	for i = 0; i < ports; i++ {
		var up, ok = page.Bit(FieldLink, i)
		if !ok {
			t.Fatalf("bit %d of the link mask unreadable", i)
		}
		if up != (i == 25) {
			t.Errorf("link of port %d (index %d) = %v; only index 25 (cage SFP2) "+
				"carries a link in this capture", i+1, i, up)
		}
	}

	// All ports are enabled (`en:0x03ffffff` = 26 bits at 1). That is what
	// proves the mask is read to the end, and does not stop at 24.
	for i = 0; i < ports; i++ {
		var on, onOK = page.Bit(FieldEnabled, i)
		if !onOK || !on {
			t.Errorf("port %d: enabled = %v (ok=%v), the capture enables them all", i+1, on, onOK)
		}
	}
	// And bit 26 (index 26), which does not exist on this model, is indeed
	// zero: the mask does not overflow.
	var offFacade, offFacadeOK = page.Bit(FieldEnabled, 26)
	if !offFacadeOK || offFacade {
		t.Errorf("bit 26 of the enable mask = %v: the 26-port mask would overflow",
			offFacade)
	}

	// The RATE only makes sense on a port that carries a link. The other 25
	// carry index 7, outside the table: returning "10M" for them would invent
	// a value.
	var spd, spdOK = page.Nums(FieldSpeed)
	if !spdOK || len(spd) != ports {
		t.Fatalf("%s = %v (ok=%v), expected %d values", FieldSpeed, spd, spdOK, ports)
	}
	var label, labelOK = SpeedLabel(spd[25])
	if !labelOK || label != "10G" {
		t.Errorf("rate of cage SFP2 = %q (ok=%v), expected 10G", label, labelOK)
	}
	var _, outOfTable = SpeedLabel(spd[0])
	if outOfTable {
		t.Errorf("port 1 has no link and carries index %d: SpeedLabel must refuse "+
			"rather than invent a rate", spd[0])
	}

	// The port names, hex-encoded: "Port1"… "SFP2".
	var names []string
	var namesOK bool
	names, namesOK = page.Texts(FieldPortName)
	if !namesOK || len(names) != ports {
		t.Fatalf("%s unreadable (ok=%v, %d names)", FieldPortName, namesOK, len(names))
	}
	if names[0] != "Port1" || names[23] != "Port24" || names[24] != "SFP1" || names[25] != "SFP2" {
		t.Errorf("port names = [%q … %q %q %q]", names[0], names[23], names[24], names[25])
	}
}

// TestParseRealVLANTable: the device's VLAN table.
func TestParseRealVLANTable(t *testing.T) {
	var rows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b refused: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("%d row(s), the capture carries 2 (VLANs 99 and 100)", len(rows))
	}
	var expected = []uint32{99, 100}
	var i int
	for i = 0; i < 2; i++ {
		var vid, ok = rows[i].Num(FieldVLANID)
		if !ok || vid != expected[i] {
			t.Errorf("row %d: vid = %d (ok=%v), expected %d", i+1, vid, ok, expected[i])
		}
		// All 26 ports are members of both VLANs (`mbr:0x03ffffff`).
		var p int
		for p = 0; p < 26; p++ {
			var member, bitOK = rows[i].Bit(FieldVLANMembers, p)
			if !bitOK || !member {
				t.Errorf("VLAN %d: port %d member = %v (ok=%v), the capture makes them all "+
					"members", expected[i], p+1, member, bitOK)
			}
		}
	}
}

// TestEmptyTableIsAResult: `[]` means "no rule", not "I could not read it".
// Confusing the two would flag a fault on a device whose ACL table is simply
// empty, which is the normal case.
func TestEmptyTableIsAResult(t *testing.T) {
	var name string
	for _, name = range []string{"acl.b", "host.b"} {
		var rows, err = ParseTable(capture(t, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if rows == nil {
			t.Errorf("%s returns a nil table: an empty table must be an empty "+
				"slice, so the caller does not have to tell the two apart", name)
		}
		if len(rows) != 0 {
			t.Errorf("%s: %d row(s), the capture is empty", name, len(rows))
		}
	}
}

// TestHTMLPageRefused: the most expensive trap of this platform.
//
// On SwOS, a path that does not exist does NOT return 404: it returns the
// complete web UI, as HTTP 200. A parser that accepted this body, or that
// returned an empty page, would conclude "this feature is not configured"
// where the real cause is a typo in the page name.
func TestHTMLPageRefused(t *testing.T) {
	var html = `<!doctype html><style>html{height:100%}</style><script>let l={};</script>`
	var _, _, err = Parse(html)
	if err == nil {
		t.Fatal("the device's web UI was accepted as a data page")
	}
	// The message must carry an excerpt: without it, "this is not a SwOS page"
	// helps nobody understand that the path is wrong.
	if !strings.Contains(err.Error(), "doctype") {
		t.Errorf("the error message must quote what was received, got: %v", err)
	}
}

// TestRefusedSyntaxes: what the parser must reject rather than interpret.
func TestRefusedSyntaxes(t *testing.T) {
	var cases = []struct {
		name, src string
	}{
		{"empty", ""},
		{"unclosed object", "{a:0x01"},
		{"unclosed array", "{a:[0x01,0x02"},
		{"unclosed quote", "{a:'6162"},
		{"missing colon", "{a 0x01}"},
		{"missing value", "{a:}"},
		{"repeated key", "{a:0x01,a:0x02}"},
		{"residue after the object", "{a:0x01} garbage"},
		{"number beyond 32 bits", "{a:0x1ffffffff}"},
		{"table of non-objects", "[0x01,0x02]"},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var _, _, err = Parse(c.src)
			if err == nil {
				t.Errorf("Parse(%q) accepted: a page we cannot read must report itself, "+
					"not be guessed", c.src)
			}
		})
	}
}

// TestEncodingIdenticalToTheUI: the body produced on write must be the one the
// device's UI would produce.
//
// The number encoding reproduces its `Pa` function: lower-case hexadecimal,
// padded to an EVEN number of digits. Nothing proves the device is sensitive
// to it; reproducing to the letter the only shape known to be accepted is the
// choice that costs nothing.
func TestEncodingIdenticalToTheUI(t *testing.T) {
	var cases = []struct {
		v    uint32
		want string
	}{
		{0, "0x00"},
		{1, "0x01"},
		{15, "0x0f"},
		{99, "0x63"},
		{0x100, "0x0100"},
		{0x03ffffff, "0x03ffffff"},
		{0xffffffff, "0xffffffff"},
	}
	var c = cases[0]
	for _, c = range cases {
		var got = EncodeNum(c.v)
		if got != c.want {
			t.Errorf("EncodeNum(%#x) = %q, expected %q", c.v, got, c.want)
		}
	}
}

// TestRoundTripOnRealCaptures: re-reading what was just written must return
// what had been read.
//
// That is the property that makes READ-MODIFY-REWRITE safe: if the encoder
// lost or altered a field, a VLAN membership write would silently take port
// isolation or storm limits with it.
func TestRoundTripOnRealCaptures(t *testing.T) {
	var name string
	for _, name = range []string{"sys.b", "link.b", "fwd.b", "snmp.b", "rstp.b", "lacp.b", "sfp.b", "stats.b"} {
		t.Run(name, func(t *testing.T) {
			var page, err = ParseObject(capture(t, name))
			if err != nil {
				t.Fatalf("%s refused: %v", name, err)
			}
			var reread *Page
			reread, err = ParseObject(page.Encode())
			if err != nil {
				t.Fatalf("the re-encoded body of %s cannot be read back: %v", name, err)
			}
			if !reflect.DeepEqual(page.Keys(), reread.Keys()) {
				t.Fatalf("the keys of %s changed in the round trip", name)
			}
			var k string
			for _, k = range page.Keys() {
				var a, _ = page.Value(k)
				var b, _ = reread.Value(k)
				if !reflect.DeepEqual(a, b) {
					t.Errorf("%s: field %q altered by the round trip (%+v → %+v)", name, k, a, b)
				}
			}
		})
	}
}

// TestRealBackup: /backup.swb, the entire configuration in one request.
//
// It is the source that made it possible to establish each page's WRITABLE
// field set — the information no documentation gives, and without which a
// partial write would let the device reset fields to zero.
func TestRealBackup(t *testing.T) {
	var page, err = ParseBackup(capture(t, "backup.swb"))
	if err != nil {
		t.Fatalf("/backup.swb refused: %v", err)
	}
	// The keys are PAGE NAMES, dot and exclamation mark included.
	var expected = []string{"vlan.b", "lacp.b", ".pwd.b", "snmp.b", "rstp.b",
		"link.b", "fwd.b", "sys.b", "acl.b", "host.b"}
	var k string
	for _, k = range expected {
		if !page.Has(k) {
			t.Errorf("the backup does not carry page %q", k)
		}
	}

	// THE property that matters: the field set declared for /fwd.b must be
	// exactly the one the device stores. A gap would mean a write would send a
	// truncated page — or fields it does not expect.
	var fwd, ok = page.Obj("fwd.b")
	if !ok {
		t.Fatal("the backup does not carry /fwd.b as an object")
	}
	var declared = FwdWritableFields(26)
	if !reflect.DeepEqual(fwd.Keys(), declared) {
		t.Errorf("the /fwd.b writable set declared by the connector differs from "+
			"the one the device stores.\n  backup   : %v\n  declared : %v",
			fwd.Keys(), declared)
	}

	var snmp *Page
	snmp, ok = page.Obj("snmp.b")
	if !ok {
		t.Fatal("the backup does not carry /snmp.b as an object")
	}
	var fields, known = WritableFields(PageSNMP)
	if !known {
		t.Fatal("writable set of /snmp.b not declared")
	}
	if !reflect.DeepEqual(snmp.Keys(), fields) {
		t.Errorf("writable set of /snmp.b: backup %v, declared %v",
			snmp.Keys(), fields)
	}

	// NO user name in the device's entire configuration: that is what
	// establishes there is no account to name on SwOS.
	var pwd *Page
	pwd, ok = page.Obj(".pwd.b")
	if !ok {
		t.Fatal("the backup does not carry the password page")
	}
	if !reflect.DeepEqual(pwd.Keys(), []string{"pwd"}) {
		t.Errorf("the password page carries %v: if there were an account name, "+
			"it would be there, and AdminAccount.Custom should become true", pwd.Keys())
	}
}

// TestTextDecodingLikeTheDevice: decoding hex-encoded strings.
func TestTextDecodingLikeTheDevice(t *testing.T) {
	var cases = []struct {
		name, raw, want string
		ok              bool
	}{
		{"empty", "", "", true},
		{"ASCII", "506f727431", "Port1", true},
		// The NUL byte TERMINATES: these fields are fixed-size buffers, and what
		// follows is residue the device's UI does not display either.
		{"NUL terminator", "506f7274310041424344", "Port1", true},
		// SPACE padding is kept: that is how a transceiver's SFF-8472 fields are
		// written, and it is up to the caller to trim it.
		{"spaces kept", "465320", "FS ", true},
		// UTF-8 multi-byte: switch identity may carry non-ASCII.
		{"UTF-8", "6d6574656f", "meteo", true},
		// An odd number of digits loses the last one, as in the device's
		// decoder.
		{"odd", "506f7274315", "Port1", true},
		{"non-hexadecimal", "zzzz", "", false},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got, ok = decodeHexText(c.raw)
			if ok != c.ok || got != c.want {
				t.Errorf("decodeHexText(%q) = %q,%v; expected %q,%v",
					c.raw, got, ok, c.want, c.ok)
			}
		})
	}
}

// TestMaskAsAnArrayOfWords: the word order of a mask of more than 32 ports.
//
// No known SwOS model exceeds 32 ports, and that is precisely why this test
// exists: the day one does, the word order will not be guessed but verified.
// It is observed in the device's JavaScript, which walks the array BACKWARDS —
// the LAST element carries bits 0 to 31.
func TestMaskAsAnArrayOfWords(t *testing.T) {
	// [high word, low word]: port 1 is bit 0 of the LAST word.
	var page, err = ParseObject("{mbr:[0x00000003,0x80000001]}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var cases = []struct {
		index int
		want  bool
	}{
		{0, true},   // bit 0 of the low word
		{1, false},  //
		{31, true},  // bit 31 of the low word
		{32, true},  // bit 0 of the high word
		{33, true},  // bit 1 of the high word
		{34, false}, //
	}
	var c = cases[0]
	for _, c = range cases {
		var got, ok = page.Bit("mbr", c.index)
		if !ok || got != c.want {
			t.Errorf("Bit(index %d) = %v (ok=%v), expected %v", c.index, got, ok, c.want)
		}
	}
	// And the write keeps the SHAPE: sending back a scalar where the device
	// expects an array means sending a mask it will not read as written. Port
	// 35 is bit 2 of the HIGH word, which already carried bits 0 and 1: it must
	// go from 0x03 to 0x07, and the LOW word must not be touched.
	if !page.SetBit("mbr", 34, true) {
		t.Fatal("SetBit refused on an array mask")
	}
	var encoded = page.Encode()
	if encoded != "{mbr:[0x07,0x80000001]}" {
		t.Errorf("after SetBit(34): %q, expected {mbr:[0x07,0x80000001]}", encoded)
	}
}

// TestSetBitRefusesAMissingKey: never create a mask from nothing.
//
// A mask made from zero means "no port". On `allp` ("Allow From Ports"), that
// is enough to make the device unreachable — and it would be done while
// believing one was simply allowing one more port.
func TestSetBitRefusesAMissingKey(t *testing.T) {
	var page = NewPage()
	if page.SetBit("allp", 3, true) {
		t.Error("SetBit created a mask that did not exist in the read page")
	}
	if page.Has("allp") {
		t.Error("a missing key was created")
	}
}

// TestOnlyFlagsMissingFields: the write filter never fills in.
//
// Sending the device a page missing a field it expects means letting it reset
// that field to zero. A field requested but missing from the read page must
// therefore STOP the write, not be invented.
func TestOnlyFlagsMissingFields(t *testing.T) {
	var page, err = ParseObject("{a:0x01,b:0x02}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var out, missing = page.Only([]string{"a", "b", "c"})
	if !reflect.DeepEqual(missing, []string{"c"}) {
		t.Errorf("missing fields = %v, expected [c]", missing)
	}
	if out.Has("c") {
		t.Error("Only invented the missing field")
	}
	// The requested order is respected: reproducing the order the device
	// serves makes two transcripts comparable line by line.
	out, missing = page.Only([]string{"b", "a"})
	if len(missing) != 0 || out.Encode() != "{b:0x02,a:0x01}" {
		t.Errorf("Only does not respect the requested order: %q", out.Encode())
	}
}

package swos

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// The parser for SwOS `*.b` pages — the foundation of the whole connector.
//
// SwOS does not serve JSON. It serves JavaScript literals, as its UI eats them
// in an `eval()`:
//
//	{upt:0x00e42cfe,mac:'b869f45f14a8',brd:'4353533332362d3234472d32532b',
//	 spd:[0x07,0x07,0x03],nm:['506f727431','53465031']}
//
// Three departures from JSON, and each one breaks a standard decoder:
//
//   - keys have NO quotes;
//   - numbers are hexadecimal, prefixed with `0x`, and of variable width — the
//     live page pads (`0x0063`), the backup does not (`0x63`);
//   - strings are in SINGLE quotes and their content is hex-encoded UTF-8
//     (`'506f727431'` = "Port1").
//
// Hence this parser. It is written by hand because there is no other choice,
// and it is verified on real captures from the device (testdata/) because
// that is the only proof worth anything.
//
// THE CENTRAL STANCE: a missing field is ABSENT, never zero.
//
// The device's UI does the opposite — its accessor (`Ca` in its JavaScript)
// returns the default value declared in the field definition when the page
// does not carry the key. That is acceptable for drawing a form, and
// unacceptable here: "this port is in VLAN 0" and "the page says nothing about
// this port" lead to opposite writes. All accessors therefore return a second
// boolean, and none makes up a value.

// Kind is the nature of a value read from a page.
type Kind int

const (
	// KindNum: a hexadecimal integer (`0x0063`).
	KindNum Kind = iota
	// KindStr: a hex-encoded string (`'506f727431'`). The content stays RAW
	// until its text is asked for: a malformed string must report itself when
	// it is read, not make the whole page fail.
	KindStr
	// KindList: an array, generally indexed by port number.
	KindList
	// KindObj: a nested object. Rare on pages, the normal shape of
	// /backup.swb.
	KindObj
)

// Value is a value read from a `*.b` page.
type Value struct {
	Kind Kind
	// Num carries integers. SwOS never exceeds 32 bits on a scalar value: its
	// port masks beyond 32 ports go through an ARRAY of words (cf. Bit).
	Num uint32
	// Raw carries strings as served — hence still in hexadecimal.
	Raw  string
	List []Value
	Obj  *Page
}

// Page is an object read from a `*.b` page, with key order preserved.
//
// The order is not cosmetic: writes send the page's complete field set back
// to the device, and reproducing the order in which it was served makes two
// transcripts comparable line by line. On a platform where what was sent
// cannot be read back, that is the only way to understand a failure without
// going back on site.
type Page struct {
	fields map[string]Value
	order  []string
}

// Keys returns the keys in the order the device served them.
func (p *Page) Keys() []string {
	if p == nil {
		return nil
	}
	return append([]string{}, p.order...)
}

// Has says whether the page carries the key. Prefer it to an accessor whose
// second return value would be ignored.
func (p *Page) Has(key string) bool {
	if p == nil {
		return false
	}
	var _, ok = p.fields[key]
	return ok
}

// Value returns the raw value.
func (p *Page) Value(key string) (Value, bool) {
	if p == nil {
		return Value{}, false
	}
	var v, ok = p.fields[key]
	return v, ok
}

// Num returns an integer. `ok` is false if the key is missing OR if it carries
// something other than a number — confusing the two would read an array as a
// scalar.
func (p *Page) Num(key string) (uint32, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindNum {
		return 0, false
	}
	return v.Num, true
}

// Text decodes a hex-encoded string.
//
// The decoding reproduces the device's (`Ga` in its JavaScript): the NUL byte
// TERMINATES the string. These fields are fixed-size buffers, and what
// follows the NUL is residue — returning it would show characters the
// operator sees nowhere in their switch's UI.
//
// SPACE padding, on the other hand, is kept: that is how a transceiver's
// SFF-8472 fields are written (`'46532020…'` = "FS" followed by blanks), and it
// is up to the caller to decide whether to trim it — not up to the parser to
// erase information it read.
func (p *Page) Text(key string) (string, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindStr {
		return "", false
	}
	return decodeHexText(v.Raw)
}

// Nums returns an array of integers, typically indexed by port.
func (p *Page) Nums(key string) ([]uint32, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindList {
		return nil, false
	}
	var out = make([]uint32, 0, len(v.List))
	var e Value
	for _, e = range v.List {
		if e.Kind != KindNum {
			return nil, false
		}
		out = append(out, e.Num)
	}
	return out, true
}

// NumAt returns the element of index `i` of an integer array. An
// out-of-bounds index is not a zero: it is an absence, and it says so.
func (p *Page) NumAt(key string, i int) (uint32, bool) {
	var list, ok = p.Nums(key)
	if !ok || i < 0 || i >= len(list) {
		return 0, false
	}
	return list[i], true
}

// Texts returns an array of decoded strings. A single unreadable string makes
// the whole set fail: silently returning the others would leave a hole at an
// index one would believe empty.
func (p *Page) Texts(key string) ([]string, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindList {
		return nil, false
	}
	var out = make([]string, 0, len(v.List))
	var e Value
	for _, e = range v.List {
		if e.Kind != KindStr {
			return nil, false
		}
		var s string
		s, ok = decodeHexText(e.Raw)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// TextAt returns the element of index `i` of a string array.
func (p *Page) TextAt(key string, i int) (string, bool) {
	var list, ok = p.Texts(key)
	if !ok || i < 0 || i >= len(list) {
		return "", false
	}
	return list[i], true
}

// Obj returns a nested object (the values of /backup.swb).
func (p *Page) Obj(key string) (*Page, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindObj || v.Obj == nil {
		return nil, false
	}
	return v.Obj, true
}

// List returns an array of objects — the shape of tables (/vlan.b, /host.b,
// /acl.b).
func (p *Page) List(key string) ([]*Page, bool) {
	var v, ok = p.Value(key)
	if !ok || v.Kind != KindList {
		return nil, false
	}
	var out = make([]*Page, 0, len(v.List))
	var e Value
	for _, e = range v.List {
		if e.Kind != KindObj || e.Obj == nil {
			return nil, false
		}
		out = append(out, e.Obj)
	}
	return out, true
}

// Bit reads the bit of rank `i` of a port mask.
//
// Two shapes coexist on this platform, and getting the shape wrong gives a
// shifted port mask — i.e. a configuration applied to the WRONG ports:
//
//   - up to 32 ports, the mask is a SCALAR and bit 0 is port 1
//     (`fp1:0x03fffffe` on a 26-port switch: every port but the first);
//   - beyond that, it is an ARRAY of 32-bit words whose LAST element carries
//     bits 0 to 31, the one before it bits 32 to 63.
//
// The word order is observed in the device's JavaScript, which walks the
// array backwards (`for(c=a.length-1; 0<=c; --c){ if(32>b) return a[c]>>b&1;
// b-=32 }`). No known SwOS model exceeds 32 ports, but coding the reverse
// order out of apparent symmetry would have been a silent trap the day one
// does.
func (p *Page) Bit(key string, i int) (bool, bool) {
	if i < 0 {
		return false, false
	}
	var v, ok = p.Value(key)
	if !ok {
		return false, false
	}
	switch v.Kind {
	case KindNum:
		if i >= 32 {
			return false, false
		}
		return v.Num>>uint(i)&1 == 1, true
	case KindList:
		var words, listOK = p.Nums(key)
		if !listOK {
			return false, false
		}
		var word = len(words) - 1 - i/32
		if word < 0 {
			return false, false
		}
		return words[word]>>uint(i%32)&1 == 1, true
	}
	return false, false
}

// SetNum sets an integer. Used by writes, which re-read the page, modify what
// they must, and send it back whole.
func (p *Page) SetNum(key string, v uint32) {
	p.set(key, Value{Kind: KindNum, Num: v})
}

// SetText sets a string, hex-encoded as the device expects.
func (p *Page) SetText(key string, s string) {
	p.set(key, Value{Kind: KindStr, Raw: hex.EncodeToString([]byte(s))})
}

// SetNums sets an integer array.
func (p *Page) SetNums(key string, vs []uint32) {
	var list = make([]Value, 0, len(vs))
	var v uint32
	for _, v = range vs {
		list = append(list, Value{Kind: KindNum, Num: v})
	}
	p.set(key, Value{Kind: KindList, List: list})
}

// SetBit sets the bit of rank `i` of a port mask, keeping the SHAPE (scalar
// or array of words) the device served.
//
// Keeping the shape is not an aesthetic detail: sending back a scalar where
// the device expects an array, or the reverse, is sending a mask it will not
// read the way it was written. A missing key is NOT created — a mask made
// from nothing would mean "no port", and on `allp` ("Allow From Ports") that
// is enough to make the device unreachable.
func (p *Page) SetBit(key string, i int, on bool) bool {
	if i < 0 {
		return false
	}
	var v, ok = p.Value(key)
	if !ok {
		return false
	}
	switch v.Kind {
	case KindNum:
		if i >= 32 {
			return false
		}
		var mask = uint32(1) << uint(i)
		if on {
			p.SetNum(key, v.Num|mask)
		} else {
			p.SetNum(key, v.Num&^mask)
		}
		return true
	case KindList:
		var words, listOK = p.Nums(key)
		if !listOK {
			return false
		}
		var word = len(words) - 1 - i/32
		if word < 0 {
			return false
		}
		var mask = uint32(1) << uint(i%32)
		if on {
			words[word] |= mask
		} else {
			words[word] &^= mask
		}
		p.SetNums(key, words)
		return true
	}
	return false
}

func (p *Page) set(key string, v Value) {
	if p.fields == nil {
		p.fields = map[string]Value{}
	}
	var _, seen = p.fields[key]
	if !seen {
		p.order = append(p.order, key)
	}
	p.fields[key] = v
}

// NewPage builds an empty page. It serves writes that have only one field to
// set (/!pwd.b), never those that modify an existing page — those RE-READ
// (cf. apply.go).
func NewPage() *Page { return &Page{fields: map[string]Value{}} }

// Only returns a COPY of the page reduced to the requested keys, in the
// requested order.
//
// It is the write-filtering tool, and it is central: a live page carries
// read-only fields (uptime, temperature, MAC) the device does not expect
// back. Only the page's WRITABLE field set is therefore sent back, as observed
// in its own configuration backup (cf. pages.go).
//
// A key that is requested but missing from the read page is flagged, not
// invented: the write must stop there. Sending a truncated page to a device
// that expects the complete set means letting it reset what we did not say.
func (p *Page) Only(keys []string) (*Page, []string) {
	var out = NewPage()
	var missing []string
	var k string
	for _, k = range keys {
		var v, ok = p.Value(k)
		if !ok {
			missing = append(missing, k)
			continue
		}
		out.set(k, v)
	}
	return out, missing
}

// Encode returns the page in the syntax the device expects on write.
//
// The number encoding reproduces EXACTLY that of the device's UI (`Pa` in its
// JavaScript): hexadecimal, lower case, padded to an EVEN number of digits —
// hence `0x00` for zero, `0x63` for 99, `0x03ffffff` for a 26-port mask.
// Nothing proves the device is sensitive to this padding; reproducing to the
// letter what its UI sends is simply the only shape known to be accepted.
func (p *Page) Encode() string {
	var b strings.Builder
	b.WriteByte('{')
	var i int
	var k string
	for i, k = range p.order {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(encodeValue(p.fields[k]))
	}
	b.WriteByte('}')
	return b.String()
}

func encodeValue(v Value) string {
	switch v.Kind {
	case KindNum:
		return EncodeNum(v.Num)
	case KindStr:
		return "'" + v.Raw + "'"
	case KindList:
		var parts = make([]string, 0, len(v.List))
		var e Value
		for _, e = range v.List {
			parts = append(parts, encodeValue(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case KindObj:
		if v.Obj == nil {
			return "{}"
		}
		return v.Obj.Encode()
	}
	return "x"
}

// EncodeTable returns a table (/vlan.b, /host.b, /acl.b) as the device
// expects it on write: `[{…},{…}]`.
//
// A table is written WHOLE. This platform has neither row add nor row delete:
// the UI rebuilds the complete list from its array and posts it (`Qa` in its
// JavaScript). Posting a table missing one row DELETES that VLAN — which is
// the intended behaviour when it was decided, and an outage when it was
// forgotten.
func EncodeTable(rows []*Page) string {
	var parts = make([]string, 0, len(rows))
	var r *Page
	for _, r = range rows {
		parts = append(parts, r.Encode())
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// EncodeNum returns an integer as the device's UI writes it.
func EncodeNum(v uint32) string {
	var s = strconv.FormatUint(uint64(v), 16)
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return "0x" + s
}

// --- The parser ---

// Parse reads a `*.b` page. The result is either an object or a list of
// objects (the tables: /vlan.b, /host.b, /acl.b, /!dhost.b).
//
// A page that is not SwOS does NOT return an empty page: it returns an error.
// That is what distinguishes "this switch configures no VLAN" from "the
// device served me its home page instead of its data" — and it does, every
// unknown path returning the complete UI as HTTP 200.
func Parse(body string) (*Page, []*Page, error) {
	var s = &scanner{src: body}
	s.ws()
	if s.eof() {
		return nil, nil, fmt.Errorf("empty page: the device served nothing")
	}
	switch s.peek() {
	case '{':
		var page, err = s.object()
		if err != nil {
			return nil, nil, err
		}
		s.ws()
		if !s.eof() {
			return nil, nil, fmt.Errorf("trailing data after the object, at offset %d: %s",
				s.i, excerpt(s.src[s.i:], 60))
		}
		return page, nil, nil
	case '[':
		var rows, err = s.rows()
		if err != nil {
			return nil, nil, err
		}
		s.ws()
		if !s.eof() {
			return nil, nil, fmt.Errorf("trailing data after the table, at offset %d: %s",
				s.i, excerpt(s.src[s.i:], 60))
		}
		return nil, rows, nil
	}
	return nil, nil, fmt.Errorf("this is not a SwOS page: it starts with %q, "+
		"expected \"{\" or \"[\" (%s)", s.peek(), excerpt(body, 80))
}

// ParseObject reads a page that is EXPECTED to be an object. Returns an error
// if the device served a table — getting the shape wrong here means reading a
// configuration where there is only an empty list.
func ParseObject(body string) (*Page, error) {
	var page, rows, err = Parse(body)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, fmt.Errorf("object expected, table of %d row(s) received", len(rows))
	}
	return page, nil
}

// ParseTable reads a page expected to be a table. An EMPTY table is a
// legitimate result, and is distinct from an error: on this device, `[]`
// means "no ACL rule", not "I could not read it".
func ParseTable(body string) ([]*Page, error) {
	var page, rows, err = Parse(body)
	if err != nil {
		return nil, err
	}
	if page != nil {
		return nil, fmt.Errorf("table expected, object with %d field(s) received", len(page.order))
	}
	if rows == nil {
		rows = []*Page{}
	}
	return rows, nil
}

// ParseBackup reads /backup.swb — the ENTIRE configuration in a single
// request.
//
// The shape is that of an object WITHOUT braces, whose keys are PAGE NAMES:
// `vlan.b:[…],snmp.b:{…},.pwd.b:{…}`. It is the most precious source on
// this platform, on two counts: it gives the complete state for the price of
// one read, and it ENUMERATES, page by page, the fields that are actually
// WRITABLE — which no documentation says (cf. pages.go).
func ParseBackup(body string) (*Page, error) {
	var s = &scanner{src: body}
	var page, err = s.fields('\x00')
	if err != nil {
		return nil, err
	}
	s.ws()
	if !s.eof() {
		return nil, fmt.Errorf("trailing data in the backup, at offset %d: %s",
			s.i, excerpt(s.src[s.i:], 60))
	}
	return page, nil
}

type scanner struct {
	src string
	i   int
}

func (s *scanner) eof() bool  { return s.i >= len(s.src) }
func (s *scanner) peek() byte { return s.src[s.i] }

func (s *scanner) ws() {
	for s.i < len(s.src) {
		switch s.src[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

// object reads `{ key:value, … }`.
func (s *scanner) object() (*Page, error) {
	s.i++ // '{'
	var page, err = s.fields('}')
	if err != nil {
		return nil, err
	}
	s.ws()
	if s.eof() || s.peek() != '}' {
		return nil, fmt.Errorf("missing closing brace at offset %d", s.i)
	}
	s.i++
	return page, nil
}

// fields reads a comma-separated sequence of `key:value`, up to `end`. With
// `end` set to NUL, the sequence runs to the end of input: that is the shape
// of /backup.swb.
func (s *scanner) fields(end byte) (*Page, error) {
	var page = NewPage()
	for {
		s.ws()
		if s.eof() {
			if end == '\x00' {
				return page, nil
			}
			return nil, fmt.Errorf("unexpected end of input at offset %d", s.i)
		}
		if s.peek() == end {
			return page, nil
		}
		var key, err = s.key()
		if err != nil {
			return nil, err
		}
		s.ws()
		if s.eof() || s.peek() != ':' {
			return nil, fmt.Errorf("\":\" expected after key %q, at offset %d", key, s.i)
		}
		s.i++
		var v Value
		v, err = s.value()
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", key, err)
		}
		// A repeated key is a page we cannot read: the last one would silently
		// win, and the write that followed would bear on a value nobody saw.
		if page.Has(key) {
			return nil, fmt.Errorf("key %q served twice at offset %d", key, s.i)
		}
		page.set(key, v)
		s.ws()
		if s.eof() {
			if end == '\x00' {
				return page, nil
			}
			return nil, fmt.Errorf("unexpected end of input after %q", key)
		}
		if s.peek() == ',' {
			s.i++
			continue
		}
		if s.peek() == end {
			return page, nil
		}
		return nil, fmt.Errorf("\",\" or end of object expected after %q, got %q at offset %d",
			key, s.peek(), s.i)
	}
}

// key reads a field name. The dot and the exclamation mark are allowed because
// /backup.swb uses PAGE NAMES as keys (`vlan.b`, `.pwd.b`); no data page
// carries any, so accepting them relaxes nothing useful.
func (s *scanner) key() (string, error) {
	var start = s.i
	for s.i < len(s.src) {
		var c = s.src[s.i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' || c == '.' || c == '!' {
			s.i++
			continue
		}
		break
	}
	if s.i == start {
		return "", fmt.Errorf("field name expected at offset %d, got %q", s.i, s.src[s.i])
	}
	return s.src[start:s.i], nil
}

func (s *scanner) value() (Value, error) {
	s.ws()
	if s.eof() {
		return Value{}, fmt.Errorf("value expected, end of input")
	}
	switch s.peek() {
	case '{':
		var obj, err = s.object()
		if err != nil {
			return Value{}, err
		}
		return Value{Kind: KindObj, Obj: obj}, nil
	case '[':
		return s.array()
	case '\'':
		return s.str()
	}
	return s.number()
}

// array reads an array, whose elements may be numbers, strings or objects.
func (s *scanner) array() (Value, error) {
	s.i++ // '['
	var out = Value{Kind: KindList}
	for {
		s.ws()
		if s.eof() {
			return Value{}, fmt.Errorf("missing closing bracket")
		}
		if s.peek() == ']' {
			s.i++
			return out, nil
		}
		var v, err = s.value()
		if err != nil {
			return Value{}, err
		}
		out.List = append(out.List, v)
		s.ws()
		if s.eof() {
			return Value{}, fmt.Errorf("missing closing bracket")
		}
		if s.peek() == ',' {
			s.i++
			continue
		}
		if s.peek() == ']' {
			s.i++
			return out, nil
		}
		return Value{}, fmt.Errorf("\",\" or \"]\" expected at offset %d, got %q",
			s.i, s.peek())
	}
}

// rows reads a table `[{…},{…}]`. SwOS tables carry only objects; finding
// anything else there is a page we cannot read.
func (s *scanner) rows() ([]*Page, error) {
	var v, err = s.array()
	if err != nil {
		return nil, err
	}
	var out = []*Page{}
	var e Value
	for _, e = range v.List {
		if e.Kind != KindObj || e.Obj == nil {
			return nil, fmt.Errorf("SwOS table: row that is not an object")
		}
		out = append(out, e.Obj)
	}
	return out, nil
}

// str reads `'…'`. The content is NOT decoded here: a badly encoded string
// must not lose the whole page, it must report itself when its text is read.
func (s *scanner) str() (Value, error) {
	s.i++ // '\''
	var start = s.i
	for s.i < len(s.src) && s.src[s.i] != '\'' {
		s.i++
	}
	if s.eof() {
		return Value{}, fmt.Errorf("missing closing quote from offset %d", start)
	}
	var raw = s.src[start:s.i]
	s.i++
	return Value{Kind: KindStr, Raw: raw}, nil
}

// number reads `0x…`. Bare decimal is allowed: the configuration backup and
// the pages serve only prefixed hexadecimal, but a firmware that omitted the
// prefix would give a page unreadable for a purely cosmetic reason.
func (s *scanner) number() (Value, error) {
	var start = s.i
	var base = 10
	if strings.HasPrefix(s.src[s.i:], "0x") || strings.HasPrefix(s.src[s.i:], "0X") {
		s.i += 2
		base = 16
		start = s.i
	}
	for s.i < len(s.src) && isHexDigit(s.src[s.i]) {
		s.i++
	}
	if s.i == start {
		return Value{}, fmt.Errorf("number expected at offset %d", start)
	}
	var n, err = strconv.ParseUint(s.src[start:s.i], base, 64)
	if err != nil {
		return Value{}, fmt.Errorf("unreadable number %q: %w", s.src[start:s.i], err)
	}
	// SwOS has no value beyond 32 bits: beyond that, it is a page we are
	// misreading, and truncating it would give a wrong port mask.
	if n > 0xFFFFFFFF {
		return Value{}, fmt.Errorf("number outside SwOS's 32 bits: %q", s.src[start:s.i])
	}
	return Value{Kind: KindNum, Num: uint32(n)}, nil
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// decodeHexText decodes a hex-encoded string the way the device does.
//
// An ODD number of digits has the last one dropped, as in the device's
// JavaScript (`for(d=0; d+1<a.length; d+=2)`): we read what the device reads,
// including when what it serves is lopsided.
func decodeHexText(raw string) (string, bool) {
	if len(raw)%2 == 1 {
		raw = raw[:len(raw)-1]
	}
	var b, err = hex.DecodeString(raw)
	if err != nil {
		return "", false
	}
	// The NUL byte terminates: these fields are fixed-size buffers.
	var i int
	for i = 0; i < len(b); i++ {
		if b[i] == 0 {
			return string(b[:i]), true
		}
	}
	return string(b), true
}

// excerpt reduces a response to what fits in an error message. Without an
// excerpt, "this is not a SwOS page" helps nobody: it is precisely the
// message one gets when the device serves its complete web UI instead of the
// data.
func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

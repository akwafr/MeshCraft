package swos

import (
	"encoding/hex"
	"strings"
	"testing"
)

// The identity write, proven on the REAL /sys.b capture (CSS326-24G-2S+, SwOS
// 2.17). Nothing here reaches a device — this package has no fake transport, and
// the write is testable where it matters: the body it builds.

func TestPaintSysIdentityHexEncodesLikeTheDevice(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	// The capture's own value, which is what makes the encoding verifiable rather
	// than merely self-consistent: `lan-m6` is served as 6c616e2d6d36.
	var got, ok = page.Text(FieldIdentity)
	if !ok || got != "lan-m6" {
		t.Fatalf("%s = %q (ok=%v), want lan-m6", FieldIdentity, got, ok)
	}
	paintSysIdentity(page, "sw-lab-01")
	got, ok = page.Text(FieldIdentity)
	if !ok || got != "sw-lab-01" {
		t.Errorf("%s = %q (ok=%v), want sw-lab-01", FieldIdentity, got, ok)
	}
	// And on the wire it must be the device's own form: single-quoted hex.
	var want = "'" + hex.EncodeToString([]byte("sw-lab-01")) + "'"
	if !strings.Contains(page.Encode(), FieldIdentity+":"+want) {
		t.Errorf("encoded body must carry %s:%s, got %s", FieldIdentity, want, page.Encode())
	}
}

func TestIdentityWriteBodyCarriesTheWholeWritableSet(t *testing.T) {
	// THE REGRESSION THIS PREVENTS is the paperclip-on-site one: a body missing
	// `avln`/`allp`/`alla`/`allm` lets the device zero the administration VLAN and
	// port mask, on a model with no serial port. The body must be the full
	// writable set of /sys.b, in the backup's order, with only `id` changed.
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	var avlnBefore, _ = page.Num(FieldAllowVLAN)
	var allpBefore, _ = page.Num(FieldAllowPorts)
	var ipBefore, _ = page.Num(FieldIP)
	var modeBefore, _ = page.Num(FieldAddrMode)

	paintSysIdentity(page, "sw-lab-01")
	var fields, ok = WritableFields(PageSys)
	if !ok {
		t.Fatal("PageSys must have a writable field set")
	}
	var body *Page
	var missing []string
	body, missing = page.Only(fields)
	if len(missing) > 0 {
		t.Fatalf("the real capture must serve every writable field, missing %v", missing)
	}
	var keys = body.Keys()
	if len(keys) != len(fields) {
		t.Fatalf("body carries %d fields, the writable set has %d", len(keys), len(fields))
	}
	var i int
	for i = range fields {
		if keys[i] != fields[i] {
			t.Errorf("[%d] body key %q, writable set %q — order comes from backup.swb",
				i, keys[i], fields[i])
		}
	}
	// The management fields must be echoed back UNCHANGED. This is what
	// read-modify-rewrite means here, and it is the whole safety of the gesture.
	var avlnAfter, _ = body.Num(FieldAllowVLAN)
	var allpAfter, _ = body.Num(FieldAllowPorts)
	var ipAfter, _ = body.Num(FieldIP)
	var modeAfter, _ = body.Num(FieldAddrMode)
	if avlnAfter != avlnBefore || allpAfter != allpBefore ||
		ipAfter != ipBefore || modeAfter != modeBefore {
		t.Errorf("management fields moved: avln %d→%d, allp %#x→%#x, ip %#x→%#x, iptp %d→%d",
			avlnBefore, avlnAfter, allpBefore, allpAfter, ipBefore, ipAfter,
			modeBefore, modeAfter)
	}
}

func TestIdentityWriteRefusesATruncatedPage(t *testing.T) {
	// A firmware that serves fewer fields than the connector expects must stop the
	// write, not post what it has. Constructed page — the point is the refusal,
	// and no real firmware has been seen serving a partial /sys.b.
	var page, err = ParseObject("{id:'6c616e2d6d36',wdt:0x01,ivl:0x00}")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	paintSysIdentity(page, "sw-lab-01")
	var fields, _ = WritableFields(PageSys)
	var missing []string
	_, missing = page.Only(fields)
	if len(missing) == 0 {
		t.Fatal("a page missing alla/allm/avln/allp must be reported as incomplete")
	}
	var found = false
	var m string
	for _, m = range missing {
		if m == FieldAllowPorts {
			found = true
		}
	}
	if !found {
		t.Errorf("missing=%v must name %s: it is the field whose loss mutes "+
			"administration", missing, FieldAllowPorts)
	}
}

func TestHostnameMismatchCarriesThePreviousName(t *testing.T) {
	// No save step and no rollback on this platform: the previous value is the
	// only way back, so every mismatch message must carry it.
	var empty = hostnameMismatch("sw-lab-01", "", "lan-m6")
	if !strings.Contains(empty, "lan-m6") || !strings.Contains(empty, "MUTE") {
		t.Errorf("an empty re-read must name the mute failure and the old name, got %q", empty)
	}
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very-", "lan-m6")
	if !strings.Contains(cut, "15 bytes") || !strings.Contains(cut, "lan-m6") {
		t.Errorf("a truncation must name the observed ceiling and the old name, got %q", cut)
	}
	var other = hostnameMismatch("sw-lab-01", "something-else", "lan-m6")
	if !strings.Contains(other, `"something-else"`) || !strings.Contains(other, `"lan-m6"`) {
		t.Errorf("a divergence must show what is there and what was there, got %q", other)
	}
}

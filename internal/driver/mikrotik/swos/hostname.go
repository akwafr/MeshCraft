package swos

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Setting the device's OWN name — the "Identity" field of `/sys.b`, `id`.
//
// # This is the case driver/hostname.go's header describes
//
// That header names this platform explicitly: "SwOS carries a system identity in
// `/sys.b` and it has been read but never written". It has been read for a long
// time — `fillFromSys` decodes `id` into Identity.Hostname, and the reference
// capture carries `id:'6c616e2d6d36'` = `lan-m6`. This file is the write, and it
// is the first one: NOTHING in this project has ever POSTed this field to a
// device, so the form below is DEDUCED — not from vendor documentation (there is
// none for these pages) but from the platform's OWN configuration backup, which
// is a better source and is still not a proof.
//
// What IS captured, and it is the load-bearing part: `id` is in `/sys.b`'s
// WRITABLE set. That set was not guessed — it was read out of `GET /backup.swb`,
// which enumerates exactly the fields the device stores (cf. pages.go, and
// testdata/backup.swb: `sys.b:{id:'6c616e2d6d36',wdt:0x01,…}` with `id` FIRST).
// So the field is known to be one the device accepts back; what remains unproven
// is the device's reaction to receiving it changed.
//
// # /sys.b is the most dangerous page on the platform, and this write touches it
//
// Writing on SwOS is necessarily READ-MODIFY-REWRITE: the device's own interface
// rebuilds the whole page object and POSTs it as-is, and a truncated body leaves
// the device free to zero what was not said. On `/sys.b` the fields that would be
// zeroed are `alla`, `allm`, `avln` and `allp` — "Allow From", its mask, the
// administration VLAN and the administration port mask. Zeroing those on a model
// with NO serial port is the paperclip-on-site failure, for the sake of renaming
// a switch.
//
// Hence the two locks, both of them borrowed rather than invented (cf.
// writeSysMgmt, which is the same shape for the addressing fields):
//
//  1. the body is `page.Only(WritableFields(PageSys))` — every writable field
//     echoed back with the value just read, nothing else;
//  2. a field the firmware did NOT serve aborts the write. "Refuse" and not
//     "send what we have": posting a page without `allp` is exactly how a
//     device goes mute.
//
// # Verification, and why the exact comparison matters more here than elsewhere
//
// A SwOS refusal is MUTE: HTTP 200, empty body, nothing changed — its own
// interface reads nothing but the status code. So the re-read is the only proof
// that exists. It also catches the second failure mode this field specifically
// invites: `id` is a fixed-size buffer (its sibling text fields on `/snmp.b` stop
// at 64 bytes, `ga:64` in the UI, and REFUSE past that without a message), so a
// name too long could come back SHORTENED. Comparing byte for byte turns that
// into a reported fact — including the previous name, so the operator can put it
// back.
//
// # What is NOT done
//
//   - No Persist: this platform is `immediate` (cf. capabilities.go). There is
//     nothing to save, and therefore no safety net either — the only way back is
//     the previous value, which the message carries when a write goes wrong.
//   - No backup taken first, unlike ApplyMgmtAddressing. Deliberate: a backup
//     exists there because the addressing write can make the device unreachable
//     and a restore is the only recourse. This write cannot: it changes one text
//     field and echoes the rest unchanged, and the fields whose loss would mute
//     the device are exactly the ones lock (2) refuses to write without. Paying
//     for a whole `GET /backup.swb` on every rename would buy nothing.
//   - No ceiling of our own beyond driver.ValidHostname's 63 bytes. `id`'s real
//     bound is documented NOWHERE — not in this package, not in the vault — and
//     pages.go's rule is explicit that a value without a source would be
//     invented. The device's own answer is the source, and the re-read is how it
//     is read.
var _ driver.HostnameSetter = (*Admin)(nil)

// paintSysIdentity sets the identity on a `/sys.b` page read from the device.
//
// Pure, and separate from the write for the reason every `paint…` function in
// this package is: what it does to the page can be checked against a REAL
// capture, field by field, including the neighbours it must not disturb.
//
// SetText is what hex-encodes the value, because that is how the device serves
// and expects its strings (`id:'6c616e2d6d36'`).
func paintSysIdentity(page *Page, name string) {
	page.SetText(FieldIdentity, name)
}

// hostnameMismatch explains a re-read that does not carry the requested name.
//
// `before` is in the message on purpose: on a platform with no save step and no
// rollback, the previous value is the only way back, and an operator reading this
// at three in the morning must not have to go looking for it.
func hostnameMismatch(want, got, before string) string {
	if got == "" {
		return "the device answered HTTP 200 but /sys.b re-reads with an empty " +
			"identity: on SwOS a refusal is MUTE — 200, empty body, nothing changed — " +
			"and this re-read is the only thing that catches it. The name was \"" +
			before + "\" before this attempt"
	}
	if len(got) < len(want) && strings.HasPrefix(want, got) {
		return fmt.Sprintf("the device stored %q — the requested name cut at %d "+
			"bytes. `id` is a fixed-size buffer and that is its real ceiling, learned "+
			"here rather than guessed; the switch now answers to a name meshcraft does "+
			"not carry. Send a name of at most %d bytes, or set %q back",
			got, len(got), len(got), before)
	}
	return fmt.Sprintf("/sys.b re-reads its identity as %q, not %q (it was %q before "+
		"this attempt): the write did not take the way it was sent", got, want, before)
}

// SetHostname sets the device's own system name — driver.HostnameSetter.
//
// Read, modify, rewrite, re-read. No save: nothing to save on this platform.
func (a *Admin) SetHostname(ctx context.Context, name string) error {
	// The shared floor first, and before any request: on this platform the name
	// is hex-encoded into a page body rather than a command line, so injection is
	// not the risk — but the device stores raw bytes and would happily carry a
	// name no CLI connector in the fleet could accept, which defeats the point of
	// having one name that works everywhere.
	if !driver.ValidHostname(name) {
		return fmt.Errorf("system name %q refused: it must carry only letters, "+
			"digits and hyphens, must not start or end with a hyphen, and must fit "+
			"in 63 bytes — this connector does not normalize it, because a device "+
			"answering to a name meshcraft did not choose breaks the LLDP "+
			"correlation this gesture exists to enable", name)
	}

	var page, err = a.Get(ctx, PageSys)
	if err != nil {
		return err
	}
	var before, _ = page.Text(FieldIdentity)
	if before == name {
		// A POST that changes nothing is a risk taken for nothing (the rule
		// ApplySNMP states on this platform), and on /sys.b the risk is the
		// management fields.
		a.step(true, "system name already \""+name+"\"", "no write sent")
		return nil
	}
	paintSysIdentity(page, name)

	var fields, ok = WritableFields(PageSys)
	if !ok {
		return fmt.Errorf("writable field set for %s unknown: write refused", PageSys)
	}
	var body *Page
	var missing []string
	body, missing = page.Only(fields)
	if len(missing) > 0 {
		var e = fmt.Errorf("%s: this firmware does not serve fields %v expected by "+
			"the connector — write refused; posting without them would let the device "+
			"zero them, and %s carries the administration VLAN and port mask",
			PageSys, missing, PageSys)
		a.step(false, "unexpected /sys.b field set", e.Error())
		return e
	}
	err = a.postRaw(ctx, PageSys, body.Encode(), "system identity written on "+PageSys)
	if err != nil {
		return err
	}

	// THE RE-READ IS THE ONLY PROOF: a SwOS refusal is HTTP 200 with an empty
	// body.
	var after *Page
	after, err = a.Get(ctx, PageSys)
	if err != nil {
		a.step(false, "system name not re-read", err.Error())
		return fmt.Errorf("identity %q sent to %s but NOT re-read (%v): on a platform "+
			"whose refusals are mute, that leaves the write unverified — it was %q "+
			"before this attempt", name, PageSys, err, before)
	}
	var got, _ = after.Text(FieldIdentity)
	if got != name {
		var why = hostnameMismatch(name, got, before)
		a.step(false, "system name not confirmed", why)
		return fmt.Errorf("identity on %s: %s", PageSys, why)
	}
	a.step(true, "system name confirmed by re-read", FieldIdentity+" = "+got)
	return nil
}

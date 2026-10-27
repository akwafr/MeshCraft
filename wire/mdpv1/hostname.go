package mdpv1

import (
	"context"
	"strings"
)

// The device's own name — "hostname", "system name", "identity" depending
// on the vendor — and the gesture that sets it.
//
// # What the name is FOR, and why meshcraft owns it
//
// `devices.name` is the name the fleet is administered by, and the device's own
// hostname is where that name becomes visible OUTSIDE meshcraft: on the console
// banner, in the LLDP system-name TLV, and in every syslog line the device
// emits. On a fleet adopted from the factory that name is `switch` forty-eight
// times over — or a serial number — and all three of those consumers are
// illegible until the inventory's name is actually on the box.
//
// So there is ONE name, not two. The device's hostname is meshcraft's name for
// it, normalized into what a hostname may carry, and nothing else.
//
// # IT IS RECONCILED, like VLAN membership
//
// This file previously argued the opposite — that a drifted hostname was someone's
// deliberate console-side decision and overwriting it would take the console away
// from the person standing at it. That reasoning is recorded here because it was
// wrong in an instructive way: it protects an edit nobody can find later, at the
// cost of the three consumers above going stale fleet-wide. A device answering to
// a name the inventory does not carry is not a preference, it is a device nobody
// can correlate at three in the morning.
//
// The consequence is the ordinary reconciliation shape, and the caller implements
// it (cf. api.hostnameheal): the intent is `devices.name`, the observation is
// what the device announces, a difference is DRIFT, and drift is repaired
// automatically with a per-device cooldown that backs off on failure. An operator
// who renames at the console gets overruled on the next pass — deliberately, and
// visibly, because the repair emits an event.
//
// THE OBSERVATION COSTS NOTHING, which is what makes periodic checking sane on
// ranges that grant five sessions. The device's own system name already arrives
// on every SNMP poll cycle as `lldpLocSysName` and is stored in `lldp_self`; the
// drift comparison therefore opens no session at all, and a session is spent only
// when a repair is actually due. [Identity.Hostname] remains the second source,
// for devices whose LLDP or SNMP is not answering.
//
// A device where NEITHER source has reported is "not observed", which is not
// "drifted": nothing is repaired on the strength of an absent reading, or every
// unreachable device would queue a write forever.
//
// # Every platform has one, so why is the interface optional
//
// Because the WRITE is not universal even where the field is. SwOS carries a
// system identity in `/sys.b` and it has been read but never written; a
// connector must not advertise a gesture it has not exercised, and the type
// assertion is how a screen learns that BEFORE offering the gesture (cf.
// connector.go, § on the boundary between mandatory and optional).
//
// Now that the name is reconciled, that assertion carries a SECOND meaning the
// caller must respect: a platform without it can never converge, so its hostname
// difference is not a repairable drift and must not be reported as one. "This
// range cannot be renamed by meshcraft" and "this device is waiting to be
// renamed" are different sentences, and a screen that showed the second for the
// first would promise a repair that will never come.

// HostnameSetter sets the device's own system name.
//
// OPTIONAL. Implement it only where the write form is proven on hardware — same
// staging rule as [PoEApplier], [DHCPSnoopApplier] and [PortAdminApplier].
//
// THE WRITE PERSISTS where the platform distinguishes applying from saving. Same
// reasoning as [Admin.SetSecret]: a hostname that a reboot undoes is worse than
// one that was never set, because it was OBSERVED as set and every screen will
// keep claiming it.
//
// The connector REFUSES a name its platform cannot carry rather than truncating
// it. A silently shortened hostname produces a device answering to a name
// meshcraft does not know, which breaks the LLDP correlation this gesture exists
// to make work — and it breaks it in the one direction nobody looks at.
type HostnameSetter interface {
	SetHostname(ctx context.Context, name string) error
}

// AsHostnameSetter returns the setter when Admin implements it.
func AsHostnameSetter(a Admin) (HostnameSetter, bool) {
	var s, ok = a.(HostnameSetter)
	return s, ok
}

// ValidHostname reports whether a name is safe for EVERY platform to carry, and
// safe to put in the command line of the five connectors that drive a CLI.
//
// DELIBERATELY THE INTERSECTION, not the union: RFC 1123's label rules
// (letters, digits, hyphens; no leading or trailing hyphen) are what every
// surveyed range accepts, and a caller validating once against the strictest
// rule beats eight connectors each discovering their own limit at write time.
// A platform with a tighter rule still refuses on its own — this is a floor,
// not a promise.
//
// THE POINT IS INJECTION AS MUCH AS CORRECTNESS. On the CLI connectors this
// string is concatenated into a command; a name carrying a newline would run
// whatever follows it as a second configuration command, with the privileges of
// the session. Rejecting everything outside the RFC 1123 alphabet closes that
// without a per-connector quoting rule nobody would keep in sync.
//
// The 63-octet ceiling is the DNS label limit. Several ranges stop well before
// it (32 characters is common on the CLI platforms); those refuse for
// themselves, with their own message, which is more useful than this function
// guessing on their behalf.
//
// # NO PER-PLATFORM CEILING IS ENFORCED ANYWHERE, and the register is here
//
// Not one of the nine ranges has a length limit CAPTURED or documented, so
// nothing invents one: this floor is the only bound applied, a CLI refusal is
// reported verbatim, and a device that quietly SHORTENS the name is caught by the
// byte-for-byte re-read every connector does, whose message publishes the ceiling
// it just observed. A guessed ceiling would refuse names the hardware accepts,
// which is the failure nobody would ever debug.
//
// So the ceilings are collected as the field reveals them, and this comment is
// where they go — one place, next to the bound they qualify, rather than one note
// per connector. NONE OBSERVED SO FAR. The two most likely to be shorter than 63
// are the SwOS `id` buffer and the ProCurve system name; neither has been pushed
// far enough to find out, and neither should be pushed on a production unit.
func ValidHostname(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return false
	}
	var i int
	var c byte
	for i = 0; i < len(name); i++ {
		c = name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// --- the ONE normalizer, and why it lives down here ---------------------------
//
// THIS USED TO LIVE IN internal/api, AND THAT IS WHY IT DRIFTED. The reconciled
// path (api.hostnameheal → [HostnameSetter]) normalized `devices.name` with its
// own private function; the five serial-provisioning connectors could not import
// `internal/api`, so each grew a `sanitizeHostname` of its own — and on
// 2026-08-09 there were FIVE spellings of the same idea in Go and four more in
// the browser scripts that mirror them. They disagreed on every axis that
// matters: four kept `_`, which [ValidHostname] forbids outright; four never
// trimmed, so a name ending in a separator produced the trailing hyphen RFC 1123
// rejects; all five SILENTLY DROPPED characters they did not recognise, turning
// "Switch Zürich 1" into `Switch-Zrich-1`; and all five fell back to the literal
// `switch` on an empty result — a fleet of identically-named devices, produced by
// the very gesture meant to give each switch an identity.
//
// So the normalizer moved DOWN, beside [ValidHostname], whose doc comment was
// already the argument for it. `api.hostnameFromDeviceName` now delegates here
// and the connectors call the same function, which is the only arrangement in
// which the two paths CANNOT disagree: a switch provisioned from a console and
// the same switch reconciled by the healer half an hour later derive their name
// from one implementation.
//
// The browser half — web/src/pages/add/hostname.ts — mirrors this file character
// for character, because the scripts are what actually reach hardware over Web
// Serial. Any change here is a change there.

// HostnameFromDeviceName derives a device's system name from its meshcraft
// name, or says why it cannot. The second return value is empty exactly when
// the first is not.
//
// THE TRANSFORMATION IS DELIBERATELY TINY, and the restraint is the design: it
// maps the separators an operator naturally types in a display name — space,
// underscore, dot, slash, tab — onto the hyphen, collapses runs, and trims the
// hyphens RFC 1123 forbids at the ends. That is all.
//
// It does NOT strip accents, punctuation, or anything else it does not
// understand. "Switch Zürich 1" could be turned into `switch-zrich-1` by
// deleting what does not fit, and that name is worse than a refusal: it looks
// deliberate, it goes on the console banner and into every syslog line, and
// nobody would ever connect it back to the letter that was dropped. So the
// second return value names what is wrong and the caller reports it.
//
// THE 63-OCTET DNS LABEL CEILING REFUSES, IT DOES NOT TRUNCATE. Cutting a
// too-long name is the anti-pattern this file's header names: the device would
// answer to a name meshcraft does not know, and the LLDP correlation the gesture
// exists to enable would break in the one direction nobody looks at. Several
// platforms stop well before 63 (32 is common on the CLI ranges) and refuse for
// themselves, with their own message, which is more useful than guessing on
// their behalf here.
func HostnameFromDeviceName(deviceName string) (string, string) {
	var trimmed = strings.TrimSpace(deviceName)
	if trimmed == "" {
		return "", "this device has no meshcraft name to derive a system name " +
			"from: send an explicit \"hostname\""
	}
	var b strings.Builder
	var i int
	var c byte
	for i = 0; i < len(trimmed); i++ {
		c = trimmed[i]
		switch {
		case c == ' ' || c == '_' || c == '.' || c == '/' || c == '\t':
			b.WriteByte('-')
		default:
			b.WriteByte(c)
		}
	}
	var out = CollapseHostnameHyphens(b.String())
	// NOT TRUNCATED, and this is the one place it was tempting to. Cutting a
	// too-long name to 63 characters is precisely the anti-pattern the header of
	// this file names: the device would answer to a name meshcraft does not
	// know, its LLDP system-name TLV would stop matching the inventory, and the
	// correlation this whole gesture exists to enable would break — in the one
	// direction nobody looks at. Refusing sends the operator to a decision they
	// can actually make.
	if len(out) > 63 {
		return "", "the meshcraft name \"" + deviceName + "\" is longer than the " +
			"63-character system-name limit once normalized. meshcraft does not " +
			"shorten it: a truncated hostname answers to a name the inventory does " +
			"not carry, and its LLDP system name would stop matching this device — " +
			"send an explicit \"hostname\""
	}
	if !ValidHostname(out) {
		return "", "the meshcraft name \"" + deviceName + "\" cannot be used as a " +
			"system name — " + HostnameRefusal(out) +
			". meshcraft does not guess a replacement: send an explicit \"hostname\""
	}
	return out, ""
}

// CollapseHostnameHyphens reduces runs of hyphens to one and trims them at both
// ends, which is where RFC 1123 forbids them.
//
// It runs BEFORE the validity test, on purpose: an operator who wrote
// "sw-lab-" wrote a name, not a violation, and refusing it for a character
// they probably did not notice typing would be pedantry. A name that is NOTHING
// but separators still comes out empty, and empty is a refusal.
func CollapseHostnameHyphens(in string) string {
	var b strings.Builder
	var prevHyphen = false
	var i int
	var c byte
	for i = 0; i < len(in); i++ {
		c = in[i]
		if c == '-' {
			if prevHyphen {
				continue
			}
			prevHyphen = true
		} else {
			prevHyphen = false
		}
		b.WriteByte(c)
	}
	return strings.Trim(b.String(), "-")
}

// HostnameRefusal says WHAT is wrong with a name, naming the offending
// characters.
//
// "Invalid hostname" sends the operator guessing; "contains "ü", ":""
// ends the search in one read. The refusal lists the actual bytes that fell
// outside the alphabet rather than restating the rule, because the rule is
// short and the violation is usually a single character pasted from a label.
func HostnameRefusal(name string) string {
	if name == "" {
		return "an empty system name is not a name"
	}
	if len(name) > 63 {
		return "it is longer than the 63-character DNS label limit"
	}
	var bad []string
	var seen = map[rune]bool{}
	var c rune
	for _, c = range name {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			if !seen[c] {
				seen[c] = true
				bad = append(bad, "\""+string(c)+"\"")
			}
		}
	}
	if len(bad) > 0 {
		return "it carries " + strings.Join(bad, ", ") +
			", and a system name may only carry letters, digits and hyphens " +
			"(every surveyed platform accepts that set, and only that set)"
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return "it starts or ends with a hyphen, which RFC 1123 forbids"
	}
	return "it is not a valid system name"
}

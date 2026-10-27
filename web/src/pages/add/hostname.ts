/**
 * ONE normalizer for the system name a provisioning run writes, mirroring the
 * reconciled path's semantics instead of inventing a sixth spelling.
 *
 * # Why this file exists
 *
 * Until 2026-08-09 the same device name could be normalized FOUR different ways
 * depending on which range was being provisioned, and a fifth way once the
 * reconciliation loop touched it. The Go connectors each carried a private
 * `sanitizeHostname` that SILENTLY DROPPED every character it did not
 * recognise; four of the five additionally kept `_`, which
 * `driver.ValidHostname` forbids outright, and never trimmed hyphens, so a name
 * ending in a separator produced a trailing `-` RFC 1123 rejects. The scripts in
 * this directory disagreed with those AND with each other: two substituted `-`,
 * two substituted `_` — the one character the reconciled path can never emit —
 * and one dropped, then substituted, then trimmed.
 *
 * The consequence was not cosmetic. "Switch étage 1" became `Switch-tage-1` on
 * a console banner, and nobody ever connects that back to the deleted letter.
 * The same device could then be provisioned under one spelling and reconciled
 * under another, so the healer would see drift it could never resolve.
 *
 * # The doctrine: REFUSE, do not mangle
 *
 * This is `hostnameFromDeviceName`'s rule (internal/api/hostname_apply.go),
 * mirrored here character for character, and the argument is written out there:
 * a mangled name looks DELIBERATE. It goes on the console banner, into every
 * syslog line and out over the LLDP system-name TLV, and no operator reading it
 * later can tell it was not what they typed. A refusal names the offending
 * character and sends them to a decision they can actually make.
 *
 * The 63-octet DNS label ceiling REFUSES rather than truncating, for the same
 * reason the Go side does: a truncated hostname answers to a name the inventory
 * does not carry, so the LLDP correlation this whole gesture exists to enable
 * breaks in the one direction nobody looks at.
 *
 * # It is an INJECTION boundary as much as a correctness one
 *
 * On every CLI range this string is concatenated into a configuration command.
 * A name carrying a newline would run whatever follows it as a SECOND command,
 * with the privileges of the console session. Restricting the output to the
 * RFC 1123 alphabet closes that without a per-script quoting rule nobody would
 * keep in sync — which is exactly why the refusal must happen BEFORE the string
 * reaches a command builder, never inside one.
 *
 * # There is no "switch" fallback here, deliberately
 *
 * All five Go spellings fell back to the literal `switch` on an empty result.
 * That is a fleet of identically-named devices — the correlation failure the
 * hostname reconciliation exists to end — produced by the very gesture meant to
 * give each switch an identity. An empty result is a refusal.
 */

/** The bytes a hostname occupies once encoded, which is what the Go side counts. */
function utf8Length(s: string): number {
  return new TextEncoder().encode(s).length;
}

/**
 * The characters Go's `strings.TrimSpace` strips — Unicode's White_Space property
 * — which is NOT the set `String.prototype.trim` uses.
 *
 * WHY THIS IS SPELLED OUT INSTEAD OF CALLING .trim(). The two sets differ by
 * exactly two characters, and the shared vector table (hostname_vectors.json)
 * caught both on the day it was written:
 *
 *   - U+0085 NEXT LINE is White_Space, so Go trims it; JS does not, so `.trim()`
 *     left it in the name and the browser side REFUSED a name the healer accepts.
 *   - U+FEFF BYTE ORDER MARK is not White_Space, so Go refuses it; JS `.trim()`
 *     strips it, so the browser side ACCEPTED `core01` where the healer refuses.
 *
 * The second is the one that bites. A device name pasted out of a UTF-8 export
 * carries a BOM, the console provisioning would write `core01` to the switch, and
 * the healer would then be unable to derive a name for that device at all — the
 * exact "provisioned under one spelling, reconciled under another" failure this
 * whole module exists to end.
 *
 * Go is the authority where the two differ, and it is also right on the merits:
 * U+FEFF is a zero-width FORMAT character, not a space. Swallowing an invisible
 * character silently is the mangling doctrine this file rejects in every other
 * paragraph — the refusal names it, which is the only way anyone finds it.
 */
const GO_WHITESPACE = "\\t-\\r \\u0085\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u3000";
const LEADING_SPACE = new RegExp(`^[${GO_WHITESPACE}]+`);
const TRAILING_SPACE = new RegExp(`[${GO_WHITESPACE}]+$`);

/** `strings.TrimSpace`, character for character. See [GO_WHITESPACE]. */
function goTrimSpace(s: string): string {
  return s.replace(LEADING_SPACE, "").replace(TRAILING_SPACE, "");
}

/**
 * Mirrors `driver.ValidHostname` (internal/driver/hostname.go): 1..63 octets,
 * letters, digits and hyphens only, and no hyphen at either end.
 *
 * Several ranges stop well before 63 (32 is common on the CLI platforms). Those
 * refuse for themselves, with their own message, which is more useful than
 * guessing on their behalf here.
 */
export function validHostname(name: string): boolean {
  if (name === "" || utf8Length(name) > 63) return false;
  if (name.startsWith("-") || name.endsWith("-")) return false;
  return /^[A-Za-z0-9-]+$/.test(name);
}

/** Runs of hyphens collapse to one, and both ends lose them — RFC 1123. */
function collapseHyphens(s: string): string {
  return s.replace(/-+/g, "-").replace(/^-+|-+$/g, "");
}

/**
 * Says what is wrong with a name, in the words the controller uses.
 *
 * Mirrors `hostnameRefusal`. Every offending character is named, once each and
 * in the order met, because "it carries an invalid character" sends an
 * operator hunting through a name they believed was fine.
 */
export function hostnameRefusal(name: string): string {
  if (name === "") return "an empty system name is not a name";
  if (utf8Length(name) > 63) {
    return "it is longer than the 63-character DNS label limit";
  }
  const bad: string[] = [];
  const seen = new Set<string>();
  for (const c of name) {
    if (/[A-Za-z0-9-]/.test(c)) continue;
    if (!seen.has(c)) {
      seen.add(c);
      bad.push(`"${c}"`);
    }
  }
  if (bad.length > 0) {
    return (
      `it carries ${bad.join(", ")}, and a system name may only carry letters, ` +
      "digits and hyphens (every surveyed platform accepts that set, and only that set)"
    );
  }
  if (name.startsWith("-") || name.endsWith("-")) {
    return "it starts or ends with a hyphen, which RFC 1123 forbids";
  }
  return "it is not a valid system name";
}

/** What the normalizer answers: a usable name, or the reason there is none. */
export interface HostnameVerdict {
  /** The name to send. Empty exactly when `reason` is set. */
  hostname: string;
  /** Why no name could be derived. Empty exactly when `hostname` is set. */
  reason: string;
}

/**
 * Derives the system name to write from a meshcraft device name.
 *
 * Mirrors `hostnameFromDeviceName` (internal/api/hostname_apply.go) so that a
 * switch provisioned from a console and the same switch reconciled by the healer
 * cannot end up being named two different things.
 *
 * The ONLY characters rewritten are the separators an operator plainly meant as
 * separators — space, underscore, dot, slash, tab — which become hyphens.
 * Accents, punctuation and everything else are NOT stripped: they refuse.
 */
export function hostnameFromDeviceName(deviceName: string): HostnameVerdict {
  // goTrimSpace, NOT .trim(): the two disagree on U+0085 and U+FEFF, and the
  // shared vector table caught both. See GO_WHITESPACE above.
  const trimmed = goTrimSpace(deviceName || "");
  if (trimmed === "") {
    return {
      hostname: "",
      reason:
        "this device has no meshcraft name to derive a system name from: " +
        "send an explicit \"hostname\"",
    };
  }

  const mapped = trimmed.replace(/[ _./\t]/g, "-");
  const out = collapseHyphens(mapped);

  // NOT TRUNCATED, and this is the one place it was tempting to be. Cutting a
  // too-long name to 63 characters is the anti-pattern internal/driver/hostname.go
  // names: the device would answer to a name meshcraft does not know, and its
  // LLDP system name would stop matching this device.
  if (utf8Length(out) > 63) {
    return {
      hostname: "",
      reason:
        `the meshcraft name "${deviceName}" is longer than the 63-character ` +
        "system-name limit once normalized. meshcraft does not shorten it: a " +
        "truncated hostname answers to a name the inventory does not carry, and " +
        "its LLDP system name would stop matching this device — send an " +
        "explicit \"hostname\"",
    };
  }

  if (!validHostname(out)) {
    return {
      hostname: "",
      reason:
        `the meshcraft name "${deviceName}" cannot be used as a system name — ` +
        `${hostnameRefusal(out)}. meshcraft does not guess a replacement: send ` +
        "an explicit \"hostname\"",
    };
  }

  return { hostname: out, reason: "" };
}

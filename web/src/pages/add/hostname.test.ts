/**
 * Lightweight checks for the shared provisioning hostname normalizer — run with:
 *   node --experimental-strip-types src/pages/add/hostname.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Written as the DIVERGENCES they prevent, because this module exists to end a
 * four-way disagreement rather than to add a feature. Each of the first four
 * cases below is a real spelling that shipped, named with the range it shipped
 * on, so that re-introducing one fails here instead of on a console banner.
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import {
  hostnameFromDeviceName,
  hostnameRefusal,
  validHostname,
} from "./hostname.ts";

// --- the four spellings this module replaced --------------------------------

// cisco/ios, fs/s3400, fs/s3900 and ubiquiti/edgeswitch (Go) DROPPED every
// character they did not recognise. "Switch étage 1" became `Switch-tage-1`:
// a name that looks deliberate, with no way back to the deleted letter.
{
  const v = hostnameFromDeviceName("Switch étage 1");
  assert.equal(v.hostname, "", "an accented name must refuse, never mangle");
  assert.match(v.reason, /"é"/, "the refusal must name the offending character");
  assert.notEqual(v.hostname, "Switch-tage-1");
}

// ciscoIosSoftReset.ts substituted `-` for everything it did not accept, so the
// same name came out a THIRD way — "Switch--tage-1" — on the one range where
// the Go builder and the browser script were supposed to agree line for line.
{
  const v = hostnameFromDeviceName("Switch étage 1");
  assert.notEqual(v.hostname, "Switch--tage-1");
}

// fsS3400SoftReset.ts and fsS3900SoftReset.ts substituted `_` — the ONE
// character driver.ValidHostname forbids outright, so those two scripts could
// only ever produce names the reconciled path can never emit.
{
  const v = hostnameFromDeviceName("server room");
  assert.equal(v.hostname, "server-room");
  assert.ok(!v.hostname.includes("_"), "the output must never carry an underscore");
}

// The Go spellings kept `_` in the INPUT as well, passing it through untouched.
// It is a separator an operator plainly meant as one: it maps to a hyphen.
assert.equal(hostnameFromDeviceName("core_01").hostname, "core-01");

// Four of the five Go spellings never trimmed, so a name ending in a separator
// produced a trailing hyphen RFC 1123 forbids. Here the hyphen is TRIMMED, not
// refused — the operator wrote a separator and meant one, and Go's
// collapseHyphens does exactly this before the validity test runs. Refusing
// would be stricter than the path this mirrors, which is its own kind of drift.
assert.equal(hostnameFromDeviceName("core01-").hostname, "core01", "a trailing hyphen is trimmed");
assert.equal(hostnameFromDeviceName("-core01").hostname, "core01", "a leading hyphen is trimmed");
// But a name that is NOTHING but separators has no name left in it.
assert.equal(hostnameFromDeviceName("-_ -").hostname, "", "separators alone refuse");
assert.equal(hostnameFromDeviceName("core01 ").hostname, "core01", "trailing space is trimmed");
assert.equal(hostnameFromDeviceName("a  b").hostname, "a-b", "hyphen runs collapse");
assert.equal(hostnameFromDeviceName("a_ /b").hostname, "a-b", "mixed separators collapse to one");

// None of the five fell back to anything but the literal `switch` on an empty
// result — a fleet of identically-named devices, produced by the gesture meant
// to give each switch an identity. Here an empty result is a refusal.
{
  const v = hostnameFromDeviceName("   ");
  assert.equal(v.hostname, "");
  assert.match(v.reason, /no meshcraft name/);
  assert.notEqual(v.hostname, "switch");
}

// --- the injection boundary -------------------------------------------------

// This string is concatenated into a configuration command on every CLI range.
// A newline would run what follows it as a SECOND command with the session's
// privileges. It must refuse — not strip the newline and send the rest.
{
  const v = hostnameFromDeviceName("core01\nusername attacker privilege 15");
  assert.equal(v.hostname, "", "a name carrying a newline must refuse");
  assert.ok(!v.hostname.includes("username"));
}
for (const nasty of ["a;b", "a b`whoami`", "a\rb", "a|b", "a$(id)"]) {
  assert.equal(
    hostnameFromDeviceName(nasty).hostname,
    "",
    `${JSON.stringify(nasty)} must never yield a sendable name`,
  );
}

// --- the ceiling refuses, it does not truncate ------------------------------

{
  const v = hostnameFromDeviceName("x".repeat(64));
  assert.equal(v.hostname, "", "64 characters refuses");
  assert.match(v.reason, /does not shorten it/);
}
assert.equal(hostnameFromDeviceName("x".repeat(63)).hostname, "x".repeat(63), "63 is accepted");

// The ceiling is counted in OCTETS, as the Go side counts it — so an accented
// name is measured the way the device will store it. (It refuses on the
// character set too; this pins that the length test agrees with Go's.)
assert.ok(!validHostname("é".repeat(32)), "64 octets of accents is over the limit");

// --- validHostname mirrors driver.ValidHostname -----------------------------

assert.ok(validHostname("core01"));
assert.ok(validHostname("a"));
assert.ok(validHostname("A-1"));
assert.ok(!validHostname(""), "empty is not a name");
assert.ok(!validHostname("-a"), "leading hyphen is forbidden");
assert.ok(!validHostname("a-"), "trailing hyphen is forbidden");
assert.ok(!validHostname("a_b"), "underscore is forbidden");
assert.ok(!validHostname("a.b"), "dot is forbidden");
assert.ok(!validHostname("a b"), "space is forbidden");
assert.ok(validHostname("x".repeat(63)));
assert.ok(!validHostname("x".repeat(64)));

// --- the refusal names every offender, once each ----------------------------

{
  const r = hostnameRefusal("a#b#c@d");
  assert.match(r, /"#"/);
  assert.match(r, /"@"/);
  assert.equal(r.match(/"#"/g)?.length, 1, "a repeated character is named once");
}
assert.match(hostnameRefusal(""), /empty system name/);
assert.match(hostnameRefusal("x".repeat(64)), /63-character DNS label limit/);
assert.match(hostnameRefusal("-a-"), /RFC 1123/);

// --- and nobody grows a private one again -----------------------------------
//
// The four scripts below build their `hostname` line inside a function that
// needs a live SerialSession, so there is no cheap way to assert on the emitted
// command for three of them (aristaEosSoftReset.ts's builder is pure and IS
// asserted, in its own suite). What can be pinned cheaply is the thing that
// actually went wrong: each script had a private normalizer, four of them
// disagreed, and none of the four disagreements was visible from any one file.
//
// So this reads the sources. It is a blunt instrument on purpose — it fails on
// the SHAPE of the mistake rather than on its consequences, which is the only
// way a re-introduction gets caught before it reaches a console banner.
{
  const scripts = [
    "ciscoIosSoftReset.ts",
    "fsS3400SoftReset.ts",
    "fsS3900SoftReset.ts",
    "aristaEosSoftReset.ts",
  ];
  for (const name of scripts) {
    const src = readFileSync(new URL("./" + name, import.meta.url), "utf8");
    const code = src
      // Comments are where these doctrines are DESCRIBED, at length, so that
      // nobody re-invents them; only executable code is being judged here.
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .replace(/^[ \t]*\/\/.*$/gm, "");
    assert.ok(
      code.includes('from "./hostname.ts"'),
      `${name} does not import the shared normalizer`,
    );
    assert.ok(
      !/\bsanitizeHostname\b/.test(code),
      `${name} carries a private sanitizeHostname again — that is the drift this module ended`,
    );
    assert.ok(
      !/hostname\s*\$\{[^}]*\breplace\b/.test(code),
      `${name} rewrites a hostname inline instead of refusing: a mangled name looks deliberate`,
    );
    assert.ok(
      /hostnameFromDeviceName\(/.test(code),
      `${name} never calls hostnameFromDeviceName`,
    );
  }
}

console.log("hostname.test.ts OK");

/**
 * THE TYPESCRIPT HALF OF THE CROSS-LANGUAGE PARITY TEST — run with:
 *   node --experimental-strip-types src/pages/add/hostname_vectors.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * # What was wrong with the arrangement this replaces
 *
 * The hostname rule has TWO implementations — `hostname.ts` here and
 * `internal/driver/hostname.go` in the controller — because these scripts are what
 * actually reach hardware over Web Serial while the healer runs in Go. Both files
 * say, in prose, that one mirrors the other. Until this test existed that was the
 * ONLY thing saying it: `hostname.test.ts` and `hostname_test.go` happened to
 * carry similar cases, written by hand, and NOTHING failed when one side was
 * edited and the other was not. Prose does not fail a build.
 *
 * That is the drift that produced the ten spellings collapsed on 2026-08-09/10,
 * so the unification would have come undone the first time somebody "fixed" one
 * side. The consequence is written out in both modules: a switch provisioned from
 * a console (this path) and the same switch reconciled by the healer (Go) would be
 * named two different things, and the healer would then chase a drift it can never
 * resolve.
 *
 * # The shared table
 *
 * `hostname_vectors.json`, beside this file, read by BOTH suites — here, and by
 * internal/driver/hostname_vectors_test.go. Neither side asserts against the
 * other's prose any more; both assert against the same table, so a change that
 * moves one implementation fails on the side that did NOT move.
 *
 * It lives HERE rather than at the repository root because of the container
 * mounts: `make test-web` mounts only ./web, so this suite can read nothing
 * outside it, while `make test-go` mounts the whole repo. web/ is the only
 * directory both can see, so the table lives here and Go reaches across.
 *
 * # Refusals are matched on MARKERS
 *
 * The operator-facing sentences are the Go ones and they are pinned word for word
 * in the Go suite; copying them into the table would make a third place to update
 * when a sentence is reworded. The markers pin the DIAGNOSIS instead — "does not
 * shorten it" is the ceiling refusal, "may only carry letters" is the alphabet
 * one — so a normalizer that rejects the right name for the wrong reason still
 * fails. `hostname.test.ts` keeps the French-facing wording checks; this file is
 * only about the two languages agreeing.
 */
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { hostnameFromDeviceName, validHostname } from "./hostname.ts";

interface HostnameVector {
  /** What this vector defends. Quoted back in every failure it produces. */
  why: string;
  /** The meshcraft device name handed to the normalizer. */
  in: string;
  /** The expected system name. Empty exactly when the name is refused. */
  hostname: string;
  /** Substrings the refusal must carry. Empty exactly when a name is expected. */
  refusal_contains: string[];
}

const VECTORS_FILE = "hostname_vectors.json";

// A parity test that stands down when it cannot find its table reports success
// while comparing nothing, which is worse than not existing: it occupies the slot
// where somebody would otherwise notice there is no guard. So every failure mode
// here throws.
let table: { vectors: HostnameVector[] };
try {
  const raw = readFileSync(new URL("./" + VECTORS_FILE, import.meta.url), "utf8");
  table = JSON.parse(raw);
} catch (err) {
  throw new Error(
    `${VECTORS_FILE} could not be read or parsed (${err}). It is the table the ` +
      "TypeScript and Go hostname normalizers are BOTH compared against; without " +
      "it the two implementations are unguarded, so this suite fails rather than " +
      "quietly passing.",
  );
}

assert.ok(
  Array.isArray(table.vectors) && table.vectors.length >= 40,
  `${VECTORS_FILE} carries ${table.vectors?.length} vectors. It covered 54 when ` +
    "it was written and it is the ONLY thing comparing the two normalizers: " +
    "shrinking it silently narrows the guard.",
);

// --- the table, against this language's implementation -----------------------

const seen = new Set<string>();
for (const v of table.vectors) {
  assert.ok(!seen.has(v.in), `the table repeats the input ${JSON.stringify(v.in)}`);
  seen.add(v.in);
  assert.ok(v.why, `a vector for ${JSON.stringify(v.in)} says nothing about what it defends`);
  assert.ok(
    !(v.hostname !== "" && v.refusal_contains.length > 0),
    `${JSON.stringify(v.in)} expects both a hostname and a refusal; exactly one exists`,
  );
  assert.ok(
    !(v.hostname === "" && v.refusal_contains.length === 0),
    `${JSON.stringify(v.in)} expects a refusal but pins nothing about it, so any ` +
      "refusal at all would satisfy it — including the wrong diagnosis",
  );

  const got = hostnameFromDeviceName(v.in);

  // The invariant the soft-reset scripts rely on now that none of them has a
  // sanitizer of its own: exactly one of the two is non-empty, and anything
  // non-empty is safe to concatenate into a configuration command.
  assert.ok(
    !(got.hostname !== "" && got.reason !== ""),
    `${JSON.stringify(v.in)} returned both the name ${JSON.stringify(got.hostname)} ` +
      `and the reason ${JSON.stringify(got.reason)}`,
  );
  assert.ok(
    got.hostname !== "" || got.reason !== "",
    `${JSON.stringify(v.in)} returned neither a name nor a reason`,
  );
  if (got.hostname !== "") {
    assert.ok(
      validHostname(got.hostname),
      `${JSON.stringify(v.in)} produced ${JSON.stringify(got.hostname)}, which ` +
        "validHostname itself rejects — a script would concatenate that straight " +
        "into a command line",
    );
  }

  assert.equal(
    got.hostname,
    v.hostname,
    `shared vector disagrees with the TypeScript normalizer.\n` +
      `  input:    ${JSON.stringify(v.in)}\n` +
      `  expected: ${JSON.stringify(v.hostname)}\n` +
      `  got:      ${JSON.stringify(got.hostname)}\n` +
      `  reason given: ${got.reason}\n` +
      `  the vector defends: ${v.why}\n` +
      "If Go agrees with the table and TypeScript does not, TypeScript moved.",
  );

  for (const marker of v.refusal_contains) {
    assert.ok(
      got.reason.includes(marker),
      `the refusal for ${JSON.stringify(v.in)} does not carry the marker ` +
        `${JSON.stringify(marker)}.\n  refusal: ${got.reason}\n` +
        `  the vector defends: ${v.why}`,
    );
  }
}

// --- the one vector that must never be tidied away ---------------------------
//
// A name UNDER 63 characters but OVER 63 octets is where Go's len() and a
// JavaScript .length silently part company: Go refuses at the ceiling while a
// .length mirror sails past and returns the alphabet refusal instead. The visible
// symptom is only WHICH refusal comes out, but the disagreement underneath is
// about how long a name may be — and on an all-ASCII name that same disagreement
// renames a device on one path and not the other.
{
  const octets = (s: string) => new TextEncoder().encode(s).length;
  const trap = table.vectors.filter((v) => octets(v.in) > 63 && [...v.in].length <= 63);
  assert.ok(
    trap.length > 0,
    "the shared table no longer carries a name that is under 63 CHARACTERS but " +
      "over 63 OCTETS. That is the single vector distinguishing an octet-counting " +
      "mirror from a .length one, and losing it is how the two normalizers stop " +
      "agreeing about the ceiling without any test noticing.",
  );
  for (const v of trap) {
    assert.equal(v.hostname, "", `over 63 octets must refuse: ${JSON.stringify(v.in)}`);
  }
}

console.log(`hostname_vectors.test.ts OK (${table.vectors.length} shared vectors)`);

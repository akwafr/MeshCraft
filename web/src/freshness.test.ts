/**
 * Lightweight checks for freshness.ts — run with:
 *   node --experimental-strip-types src/freshness.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Written as the CONFUSIONS they prevent rather than as coverage. The freshness
 * line has exactly two ways to be harmful, and both are on the reassuring side:
 *
 *   - painting an age `--text-dim` when the reading has actually stopped
 *     arriving, so a screen frozen since this morning looks the same as one
 *     refreshed a second ago;
 *   - claiming an age at all for a device nobody has ever read, which invents
 *     an observation and sends someone to check a device that is merely new.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { configFreshness, elapsedBucket } from "./freshness.ts";

// --- elapsedBucket ---------------------------------------------------------

// Under a minute is "just now" and carries no count: a line that said "read 0
// min ago" would be reporting its own rounding.
assert.deepEqual(elapsedBucket(0), { unit: "now", count: 0 });
assert.deepEqual(elapsedBucket(59_000), { unit: "now", count: 0 });

// Minutes, floored. 119 s is one minute, not two — the age must never round UP,
// or a screen would claim a reading newer than the one it has.
assert.deepEqual(elapsedBucket(60_000), { unit: "min", count: 1 });
assert.deepEqual(elapsedBucket(119_000), { unit: "min", count: 1 });
assert.deepEqual(elapsedBucket(59 * 60_000), { unit: "min", count: 59 });

// The two hinges, pinned because both `sinceLabel` and the freshness line now
// read them from here: an off-by-one at either edge shows up as the same
// instant being described two different ways in two corners of one screen.
assert.deepEqual(elapsedBucket(60 * 60_000), { unit: "h", count: 1 });
assert.deepEqual(elapsedBucket(47 * 3600_000), { unit: "h", count: 47 });
assert.deepEqual(elapsedBucket(48 * 3600_000), { unit: "d", count: 2 });

// A duration that cannot be believed yields NOTHING rather than a number.
// Negative means the browser clock and the controller clock disagree; "read in
// -3 min" tells the operator strictly less than an absent line does.
assert.equal(elapsedBucket(-1), null);
assert.equal(elapsedBucket(Number.NaN), null);

// --- configFreshness -------------------------------------------------------

// NEVER READ. Dim, not yellow: UI-GUIDE §2 separates "we have never looked"
// from "we have lost the vantage point", and yellow here would announce the
// loss of something meshcraft never had. `syncLabel` says "Never reread"
// alongside, which is why the caller prints no age for this verdict.
{
  const f = configFreshness({ status: "ok" });
  assert.equal(f.kind, "never");
  assert.equal(f.color, "var(--text-dim)");
}
// Still "never" even on a device nobody can see: the missing reading is the
// stronger fact, and yellow would imply a reading exists that has gone quiet.
{
  const f = configFreshness({ status: "unknown", status_at: "2026-08-08T10:00:00Z" });
  assert.equal(f.kind, "never");
}

// READ, WITH A VANTAGE POINT. Dim: the age is context, not a verdict. This is
// the healthy majority of the fleet and it must not be painted.
for (const status of ["ok", "warn", "off"]) {
  const f = configFreshness({ observed_at: "2026-08-08T10:00:00Z", status });
  assert.equal(f.kind, "read", status);
  assert.equal(f.color, "var(--text-dim)", status);
}

// A device that is DOWN is not indeterminate. We can see that it is down —
// that is knowledge, and its dot is already `--danger` (network.ts
// statusColor). A second colour for the same fault would double-count it.
assert.equal(
  configFreshness({ observed_at: "2026-08-08T10:00:00Z", status: "off", status_at: "x" }).kind,
  "read",
);

// BLIND. A reading landed and now no proxy able to watch this device is
// connected: the age is not merely old, it will not advance. This is the one
// case UI-GUIDE §2 names outright as `--indeterminate` — "a reading that has
// stopped arriving".
{
  const f = configFreshness({
    observed_at: "2026-08-08T10:00:00Z",
    status: "unknown",
    status_at: "2026-08-08T11:00:00Z",
  });
  assert.equal(f.kind, "blind");
  assert.equal(f.color, "var(--indeterminate)");
}

// THE PARTITION network.ts draws over `unknown`, reproduced here because this
// module may not import it. `unknown` WITHOUT a `status_at` is neverObserved —
// monitoring has never spoken, so there is no vantage point to have lost, and
// the config age stays dim even though a reading did land. Merging this with
// the case above is exactly what `neverObserved`/`blindNow` exist to prevent.
{
  const f = configFreshness({ observed_at: "2026-08-08T10:00:00Z", status: "unknown" });
  assert.equal(f.kind, "read");
  assert.equal(f.color, "var(--text-dim)");
}

console.log("freshness: OK");

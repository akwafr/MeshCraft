/**
 * Checks on the site's ONLINE count — run with:
 *   node --experimental-strip-types src/siteRates.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * What is being pinned here is a product decision, not an implementation
 * detail (2026-08-08, UI-GUIDE §7): ONLINE MEANS MESHCRAFT CAN VOUCH FOR IT.
 * The dashboard's headline counter is the most-read number in the product, and
 * before this it was reassuring about precisely the devices nothing is known
 * about.
 *
 * The ruling landed in two steps on the same day, and the second is why this
 * comment changed:
 *
 *   status "unknown", NO `status_at`  — never observed. Not online (step 1).
 *   status "unknown", WITH `status_at` — observed before, no proxy can watch
 *                                        it right now. NOT online either
 *                                        (step 2). The earlier reasoning for
 *                                        keeping it — "the device is probably
 *                                        fine, the proxy is what broke" — is
 *                                        true and still not enough: the tile
 *                                        would be asserting something nobody
 *                                        can currently verify. It is counted
 *                                        in `blind`, and it is NOT called
 *                                        offline either, because we did not
 *                                        observe it go down.
 *
 * Both carry the same `status`, so a test on the status string alone cannot
 * tell them apart and neither can `statusColor` — only `status_at` can.
 *
 * The two exclusions stay TWO figures rather than one "unverifiable" total,
 * because the palette gives them different inks and the tile has to choose one:
 * `blind` is `--indeterminate` yellow, `unobserved` is `--text-dim` grey.
 */
import assert from "node:assert/strict";
import { registerHooks } from "node:module";

// `network.ts` imports `./i18n`, a DIRECTORY import that vite resolves and node
// does not, and whose module body drives i18next off `document`. Neither the
// count nor the predicate below reads a translation, so the module is replaced
// by a stub that returns its own keys: this keeps the suite framework-free and
// browser-free, which is the property that lets `npm test` be a plain `node`.
const I18N_STUB =
  "export default { t: (k) => k, language: 'en', resolvedLanguage: 'en' };";
registerHooks({
  resolve(specifier, context, next) {
    if (specifier === "./i18n") {
      return {
        url: "data:text/javascript," + encodeURIComponent(I18N_STUB),
        shortCircuit: true,
      };
    }
    // `network.ts` imports its siblings the way the rest of the frontend does —
    // without a file extension, which vite resolves and node does not. Only
    // TYPE imports were extensionless when this hook was written, and those are
    // erased before node ever sees them; `./freshness` is the first VALUE
    // import to cross this boundary, and it failed as ERR_MODULE_NOT_FOUND.
    //
    // Restoring the extension here rather than writing `./freshness.ts` in
    // network.ts keeps the production import style uniform: one file spelling
    // its imports differently from its neighbours because of a test is the kind
    // of thing that gets "tidied" back and breaks the suite again.
    if (specifier.startsWith("./") && !/\.[a-z]+$/.test(specifier)) {
      return next(specifier + ".ts", context);
    }
    return next(specifier, context);
  },
});
// Hooks must be in place before the module under test is linked, hence the
// dynamic import. The .ts suffix is what --experimental-strip-types needs;
// tsc excludes this file.
const { neverObserved, blindNow, siteRates, reachability, deviceDot } =
  await import("./network.ts");

/**
 * A device reduced to the fields these counts read. `poe_budget_w` is summed
 * unconditionally, so it has to be there or the totals come out NaN.
 */
function dev(status: string, extra: Record<string, unknown> = {}) {
  return {
    id: status + JSON.stringify(extra),
    name: "sw",
    status,
    status_note: "",
    poe_budget_w: 0,
    port_groups: [],
    ports: {},
    ...extra,
  };
}

const OBSERVED = "2026-08-07T10:00:00Z";

// THE PREDICATE. "Never observed" is a status AND the absence of a date; each
// half alone is a different state, and conflating them is the bug this guards.
{
  assert.equal(neverObserved({ status: "unknown" }), true);
  assert.equal(neverObserved({ status: "unknown", status_at: OBSERVED }), false);
  // An observed status with no date is a row predating the column, not a
  // device nobody looked at: monitoring DID speak, we merely lost the duration.
  assert.equal(neverObserved({ status: "ok" }), false);
  assert.equal(neverObserved({ status: "off" }), false);
  assert.equal(neverObserved({ status: "warn" }), false);
}

// ITS SIBLING. `blindNow` is the other half of `unknown`, and the two must
// PARTITION it: never both true, never both false for an `unknown` device.
// Anything else and the buckets stop adding up to the fleet.
{
  assert.equal(blindNow({ status: "unknown", status_at: OBSERVED }), true);
  assert.equal(blindNow({ status: "unknown" }), false);
  assert.equal(blindNow({ status: "ok", status_at: OBSERVED }), false);
  assert.equal(blindNow({ status: "off", status_at: OBSERVED }), false);
  assert.equal(blindNow({ status: "warn", status_at: OBSERVED }), false);
  const cases = [
    { status: "unknown" },
    { status: "unknown", status_at: OBSERVED },
  ];
  cases.forEach((c) => {
    assert.equal(
      neverObserved(c) !== blindNow(c),
      true,
      "the two predicates must partition `unknown`, not overlap or leave a hole",
    );
  });
}

// It is the same condition `reachability` prints its sentence on. The two must
// not be allowed to drift: a device the counter calls unobserved and the
// detail line calls something else would be one screen arguing with itself.
{
  assert.equal(reachability({ status: "unknown" }).detail, "status.neverObserved");
  assert.notEqual(
    reachability({ status: "unknown", status_at: OBSERVED }).detail,
    "status.neverObserved",
  );
}

// THE RULING, PART 1. A device nobody has ever observed is not online.
{
  const r = siteRates([dev("unknown")] as never);
  assert.equal(r.online, 0);
  assert.equal(r.unobserved, 1);
  assert.equal(r.blind, 0);
}

// An UNREACHABLE device is not online either — unchanged, and stated so that a
// future refactor of the `off` branch cannot quietly take it with it.
{
  const r = siteRates([dev("off", { status_at: OBSERVED })] as never);
  assert.equal(r.online, 0);
  // Neither unobserved nor blind: we looked, and it is down. Three different
  // answers, three inks — red, grey and yellow. `off` is the only one the
  // dashboard's "N offline" sub-line is allowed to count.
  assert.equal(r.unobserved, 0);
  assert.equal(r.blind, 0);
}

// An ordinary healthy device counts, and so does one that answered with a
// problem: `warn` is "the device replied and something is wrong", which is an
// observation of a device that is up.
{
  const r = siteRates([
    dev("ok", { status_at: OBSERVED }),
    dev("warn", { status_at: OBSERVED }),
  ] as never);
  assert.equal(r.online, 2);
  assert.equal(r.unobserved, 0);
  assert.equal(r.blind, 0);
}

// THE RULING, PART 2 (2026-08-08). A device seen before, that no connected
// proxy can watch right now, does NOT count as online.
//
// This assertion used to say the opposite, on purpose, so that flipping it had
// to be a conscious act rather than a side effect of refactoring the `off`
// branch. This is that act, and the reasoning it replaces is kept because it
// was not wrong, only outweighed: the device is probably fine and the proxy is
// what broke, so dropping it reports an outage that is not one. What settled it
// is that the tile does not say "outage" — it says "N online", and meshcraft
// cannot currently vouch for this device at all. It is counted in `blind`, and
// deliberately not in `unobserved`: we DID look, and the two get different ink.
{
  const r = siteRates([dev("unknown", { status_at: OBSERVED })] as never);
  assert.equal(r.online, 0);
  assert.equal(r.blind, 1);
  assert.equal(r.unobserved, 0);
}

// THE DENOMINATOR DOES NOT SHRINK. The tile divides by the fleet, and the four
// buckets add up to it — so a device we cannot vouch for is visible as the gap
// between the numerator and the total rather than being written out of both.
// This is the arithmetic `DevicesOnlineTile` performs, reproduced here because
// the tile itself is a React component the plain-node suite cannot import.
{
  const devices = [
    dev("ok", { status_at: OBSERVED }),
    dev("ok", { status_at: OBSERVED, id: "b" }),
    dev("unknown", { status_at: OBSERVED }),
    dev("unknown"),
    dev("off", { status_at: OBSERVED }),
  ];
  const r = siteRates(devices as never);
  assert.equal(r.online, 2, "the blind device left the numerator");
  assert.equal(r.blind, 1);
  assert.equal(r.unobserved, 1);
  const offline = devices.length - r.online - r.blind - r.unobserved;
  // Still exactly ONE offline: the sub-line counts what we saw go down, and
  // neither the blind nor the never-observed device joined it on the way out.
  assert.equal(offline, 1);
  assert.equal(r.online + r.blind + r.unobserved + offline, devices.length);
}

// THE TILE'S DOT, as arithmetic. Which ink the shortfall earns is a product
// decision too, and its ORDER is the specification: a fault we know about
// outranks anything we merely cannot see, and "we cannot see" is never orange.
{
  const dotFor = (devices: unknown[]) => {
    const r = siteRates(devices as never);
    const missing = devices.length - r.online;
    const offline = missing - r.unobserved - r.blind;
    if (missing === 0) return "var(--ok)";
    if (offline > 0) return r.online === 0 ? "var(--danger)" : "var(--warn)";
    return r.blind > 0 ? "var(--indeterminate)" : "var(--text-dim)";
  };
  const OK = dev("ok", { status_at: OBSERVED });
  const BLIND = dev("unknown", { status_at: OBSERVED });
  const NEVER = dev("unknown");
  const OFF = dev("off", { status_at: OBSERVED });

  assert.equal(dotFor([OK]), "var(--ok)", "nothing missing is green");
  assert.equal(dotFor([OK, NEVER]), "var(--text-dim)", "never looked is grey");
  assert.equal(dotFor([OK, BLIND]), "var(--indeterminate)", "blind is yellow");
  // A MIX is yellow, not grey: grey would claim nothing was ever looked at,
  // which is false of the blind half, and yellow is the more actionable of the
  // two — it points at a proxy rather than at a wait.
  assert.equal(dotFor([OK, BLIND, NEVER]), "var(--indeterminate)");
  // A known outage still wins over both, and still is not yellow.
  assert.equal(dotFor([OK, BLIND, OFF]), "var(--warn)");
  assert.equal(dotFor([OFF, BLIND]), "var(--danger)", "nothing up and one down");
  // And a fleet nobody can see is NOT red: no device was observed to fail.
  assert.equal(dotFor([BLIND, NEVER]), "var(--indeterminate)");
}

// A DEVICE WE HAVE NEVER PINGED STILL HAS PORTS. Only the reachability claim is
// withheld; its counters keep feeding the site totals, because `status` is ping
// monitoring and an adopted switch can carry links before watch ever speaks.
// Zeroing them here would make the traffic tiles disagree with the chart.
{
  const r = siteRates([
    dev("unknown", {
      port_groups: [{ prefix: "GE", count: 2, from: 1 }],
      ports: { GE1: { mode: "access", pvid: 10, link: "1G", rx_mbps: 5, tx_mbps: 3 } },
    }),
  ] as never);
  assert.equal(r.online, 0);
  assert.equal(r.ports, 2);
  assert.equal(r.up, 1);
  assert.equal(r.rx, 5);
  assert.equal(r.tx, 3);
}

// Degenerate input: the dashboard renders before anything is fetched, and a
// site with no device at all is a normal state, not an error.
{
  assert.equal(siteRates(undefined).online, 0);
  assert.equal(siteRates(undefined).unobserved, 0);
  assert.equal(siteRates(undefined).blind, 0);
  assert.equal(siteRates([]).unobserved, 0);
  assert.equal(siteRates([]).blind, 0);
}

console.log("siteRates: OK");

// --- deviceDot: never observed is GREY, not the yellow of `unknown` ---
//
// deviceDot is the ONE place a device dot is decided (netui.tsx renders it and
// no page may reimplement it), so this is where the ruling has to hold.

{
  // The ruling: a device monitoring has never spoken about is grey.
  const fresh = deviceDot({ status: "unknown" });
  assert.equal(fresh.color, "var(--text-dim)", "never-observed dot must be grey");
  assert.ok(neverObserved({ status: "unknown" }), "predicate disagrees with the dot");

  // Observed, but nobody can see it right now: YELLOW, and this did not move
  // when the counting did. The device leaving the online tally (2026-08-08,
  // part 2) is a claim about what meshcraft can vouch for; the dot is a claim
  // about what it can SEE, and the answer to that was already correct. If this
  // ever goes grey, the two unseeable states have been conflated again.
  const blind = deviceDot({ status: "unknown", status_at: "2026-08-08T10:00:00Z" });
  assert.equal(
    blind.color,
    "var(--indeterminate)",
    "observed-unknown must stay indeterminate yellow",
  );
  assert.ok(blindNow({ status: "unknown", status_at: "2026-08-08T10:00:00Z" }));

  // A queued write outranks grey: "meshcraft owes this device a write" is true
  // whether or not we have ever seen it, and it is the more useful thing to say.
  const pending = deviceDot({ status: "unknown", sync: "pending" });
  assert.equal(pending.color, "var(--accent)", "a pending write must still show blue");
  assert.equal(pending.blink, "pending", "and must still blink pending");

  // Reachability we DO know wins outright.
  assert.equal(deviceDot({ status: "off" }).color, "var(--danger)", "off stays red");

  console.log("deviceDot never-observed: OK");
}

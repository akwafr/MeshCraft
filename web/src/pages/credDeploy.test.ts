/**
 * Lightweight checks for credDeploy — run with:
 *   node --experimental-strip-types src/pages/credDeploy.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * The assertions are written as the failures they prevent. This screen arms a
 * gesture that changes the administration password of every switch in a site,
 * so the two that matter most are the gate ([writeGate]) and the row that has no
 * name ([deviceLabel]) — one lets the write through too early, the other hides a
 * device the vault is about to make a claim about.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import {
  bucketInk,
  bucketRows,
  deployableKind,
  deviceLabel,
  haltedOnUnconfirmed,
  namesById,
  outcomeCounts,
  outcomeInk,
  runEndedBadly,
  runProgress,
  writeGate,
  type DeployResult,
  type DeploySkip,
  type DeployStatus,
} from "./credDeploy.ts";

/** A finished run, cleanly, over one device. Overridden field by field below. */
function run(over: Partial<DeployStatus> = {}): DeployStatus {
  return {
    credential_id: "c1",
    label: "admin@site",
    target: 4,
    write: false,
    actor: "op@example.com",
    started_at: "2026-08-09T10:00:00Z",
    finished_at: "2026-08-09T10:02:00Z",
    running: false,
    stopped: "",
    queued: [
      {
        device_id: "d1",
        device_name: "sw1",
        status: "ok",
        mgmt_ip: "192.0.2.1",
        installed_generation: 3,
        installed_username: "mcadmin",
      },
    ],
    skipped: [],
    results: [
      {
        device_id: "d1",
        device_name: "sw1",
        outcome: "rehearsed",
        at: "2026-08-09T10:01:00Z",
      },
    ],
    remaining: 0,
    ...over,
  };
}

// THE UNNAMED ROW. `PlanDeployment` puts every in-scope device that was never
// observed into `unreached` as a bare id — no name, no date, no error — and
// those are the rows that block promotion. An empty cell there would drop them
// out of the operator's attention.
{
  assert.equal(deviceLabel({ device_id: "9f2c-...", device_name: "" }), "9f2c-...");
  assert.equal(deviceLabel({ device_id: "9f2c-..." }), "9f2c-...");
  assert.equal(deviceLabel({ device_id: "9f2c-...", device_name: "  " }), "9f2c-...");
  assert.equal(deviceLabel({ device_id: "9f2c-...", device_name: "core01" }), "core01");

  // The middle rung: the site's own device list names the row the plan could
  // not. This is the whole reason the modal loads that list — an unreached
  // device is one an operator has to go and act on, and a UUID names nothing
  // in a rack.
  const names = namesById([{ id: "9f2c-...", name: "core01" }, { id: "other", name: "sw2" }]);
  assert.equal(deviceLabel({ device_id: "9f2c-..." }, names), "core01");
  // A device deleted between the two reads still has to appear.
  assert.equal(deviceLabel({ device_id: "gone" }, names), "gone");
  // The plan's own name WINS over the list: it is what was recorded at the
  // moment of the observation the row reports.
  assert.equal(deviceLabel({ device_id: "9f2c-...", device_name: "was-core01" }, names), "was-core01");
  assert.deepEqual(namesById(undefined), {});

  // THE SAME LADDER ON THE RUN REPORT, not only on the buckets. A run reports
  // its results and its declined devices with the name the QUEUE carried, and
  // that name comes from `devices.name` — which is empty for a device adopted
  // without one. Those rows were being printed as bare ids while the buckets
  // beside them showed the real name for the same device, which reads as two
  // different switches. The site's list settles both, so the screen resolves
  // results and skips through it as well.
  const fleet = namesById([{ id: "d1", name: "sw1" }]);
  const res: DeployResult = {
    device_id: "d1",
    device_name: "",
    outcome: "failed",
    at: "2026-08-09T10:01:00Z",
  };
  assert.equal(deviceLabel(res, fleet), "sw1");
  const skip: DeploySkip = { device_id: "d1", device_name: "", reason: "device is off" };
  assert.equal(deviceLabel(skip, fleet), "sw1");
}

// The two buckets that BLOCK a promotion are stated first, and an absent plan
// still yields four buckets rather than nothing: "0 unreached" is a fact on a
// screen whose subject is which devices were forgotten.
{
  const order = bucketRows(undefined).map((b) => b.bucket);
  assert.deepEqual(order, ["behind", "unreached", "carrying", "ahead"]);
  assert.deepEqual(
    bucketRows(undefined).map((b) => b.rows.length),
    [0, 0, 0, 0],
  );
}

// `unreached` is "we have never seen this device under this credential", which
// is the definition of --indeterminate, NOT of --warn. `behind` is observed and
// known to hold an older secret, so it is the warning.
{
  assert.equal(bucketInk("unreached"), "var(--indeterminate)");
  assert.equal(bucketInk("behind"), "var(--warn)");
  assert.equal(bucketInk("carrying"), "var(--ok)");
  assert.equal(bucketInk("ahead"), "var(--text-dim)");
}

// Only the accepted-but-unread write is --danger. A skipped device was not
// touched and its reason is printed beside it.
{
  assert.equal(outcomeInk("unconfirmed"), "var(--danger)");
  assert.equal(outcomeInk("skipped"), "var(--indeterminate)");
  assert.equal(outcomeInk("installed"), "var(--ok)");
  assert.equal(outcomeInk("failed"), "var(--warn)");
  // An outcome this build does not know must not paint itself green.
  assert.equal(outcomeInk("something-new"), "var(--indeterminate)");
}

// THE GATE, case by case.
{
  // Nothing has been run: the write is not on offer at all.
  assert.deepEqual(writeGate({ target: 4, last: undefined, perDevice: false }), {
    armed: false,
    why: "rehearseFirst",
  });

  // A clean rehearsal of the target generation is what arms it.
  assert.deepEqual(writeGate({ target: 4, last: run(), perDevice: false }), { armed: true });

  // A rehearsal of the PREVIOUS generation proves nothing about this one — the
  // credential rotated between the two gestures, which is exactly what
  // `credDeployConfirm` refuses server-side.
  assert.deepEqual(
    writeGate({ target: 5, last: run(), perDevice: false }),
    { armed: false, why: "rehearsalIsOfAnotherGeneration" },
  );

  // A run in flight: there is one run at a time controller-wide, so the button
  // would 409. Say so instead of collecting the click.
  assert.deepEqual(
    writeGate({ target: 4, last: run({ running: true, finished_at: undefined }), perDevice: false }),
    { armed: false, why: "runInFlight" },
  );

  // A rehearsal that was STOPPED reached only part of the queue. The devices it
  // never opened a session on are the ones a write would meet first.
  assert.deepEqual(
    writeGate({
      target: 4,
      last: run({ stopped: "stopped by the operator: 0 device(s) took generation 4" }),
      perDevice: false,
    }),
    { armed: false, why: "rehearsalDidNotFinish" },
  );

  // A WRITE run that finished cleanly re-arms: a re-run is a resume, and the
  // devices that already took the secret are skipped from observed state.
  assert.deepEqual(
    writeGate({ target: 4, last: run({ write: true }), perDevice: false }),
    { armed: true },
  );

  // A run HALTED on an unconfirmed write does NOT re-arm, and it is recognised
  // from the results rather than from the wording of `stopped`. The same
  // platform will do the same thing on the next device; the recovery is a human
  // reading the transcript.
  const halted = run({
    write: true,
    stopped: 'HALTED on "sw1": a write was accepted and did not read back',
    results: [
      { device_id: "d1", device_name: "sw1", outcome: "unconfirmed", at: "2026-08-09T10:01:00Z" },
    ],
  });
  assert.equal(haltedOnUnconfirmed(halted), true);
  assert.deepEqual(writeGate({ target: 4, last: halted, perDevice: false }), {
    armed: false,
    why: "haltedNeedsReview",
  });

  // Per-device credentials have no single generation to deploy: the server
  // refuses with 422 and the screen says it before the click.
  assert.deepEqual(writeGate({ target: 4, last: run(), perDevice: true }), {
    armed: false,
    why: "perDevice",
  });

  // No active generation at all — nothing to push, whatever was rehearsed.
  assert.deepEqual(writeGate({ target: 0, last: run(), perDevice: false }), {
    armed: false,
    why: "nothingToDeploy",
  });
}

// A run that walked its whole queue has an EMPTY `stopped`; one that ended early
// carries the sentence. A finished run with a sentence is not a success.
{
  assert.equal(runEndedBadly(run()), false);
  assert.equal(runEndedBadly(run({ stopped: "stopped after 3 devices refused in a row" })), true);
  assert.equal(runEndedBadly(run({ running: true, stopped: "" })), false);
  assert.equal(runEndedBadly(undefined), false);
}

// Progress counts the QUEUE, not the site: skipped devices were never in it, and
// counting them would show a run stuck at 40 % that has in fact finished.
{
  assert.deepEqual(runProgress(run()), { done: 1, total: 1 });
  assert.deepEqual(runProgress(undefined), { done: 0, total: 0 });
  // Defensive: more results than queue entries must not read as 150 %.
  const odd = run({ results: [...run().results, ...run().results] });
  assert.deepEqual(runProgress(odd), { done: 1, total: 1 });
}

// An outcome the Go side grows later shows up as a number that does not add up,
// rather than disappearing from the summary.
{
  const c = outcomeCounts(
    run({
      results: [
        { device_id: "a", device_name: "a", outcome: "installed", at: "" },
        { device_id: "b", device_name: "b", outcome: "skipped", at: "" },
        { device_id: "c", device_name: "c", outcome: "quarantined", at: "" },
      ],
    }),
  );
  assert.equal(c.installed, 1);
  assert.equal(c.skipped, 1);
  assert.equal(c.other, 1);
  assert.equal(c.unconfirmed, 0);
}

// The runner resolves each device's credential as kind `admin`; started on any
// other kind it would skip every device after explaining nothing useful.
{
  assert.equal(deployableKind("admin"), true);
  assert.equal(deployableKind("gnmi"), false);
  assert.equal(deployableKind("snmp_v2c"), false);
  assert.equal(deployableKind("snmp_v3"), false);
}

console.log("credDeploy.test.ts: ok");

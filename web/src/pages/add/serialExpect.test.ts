/**
 * Lightweight checks for serial expect rightmost-match.
 *   node --experimental-strip-types src/pages/add/serialExpect.test.ts
 */
import assert from "node:assert/strict";
import { createSerialSession, rightmostMatch } from "./serialExpect.ts";

var USERNAME = /user\s*name\s*:/i;
var PASSWORD = /password\s*:/i;

var stale = "Username: admin\r\nPassword: \r\n% Login invalid\r\nUsername: ";
var hit = rightmostMatch(stale, [PASSWORD, USERNAME]);
assert.ok(hit);
assert.equal(hit.index, 1, "Username must win over earlier Password");

var atPass = "Username: admin\r\nPassword: ";
hit = rightmostMatch(atPass, [PASSWORD, USERNAME]);
assert.ok(hit);
assert.equal(hit.index, 0, "Password wins when it is last");

var onlyUser = "User Access Verification\r\nUsername: ";
hit = rightmostMatch(onlyUser, [PASSWORD, USERNAME]);
assert.ok(hit);
assert.equal(hit.index, 1);

assert.equal(rightmostMatch("boot…", [PASSWORD, USERNAME]), null);

// --- the rolling capture must not strand the mark ---
//
// REGRESSION, 2026-08-06. `markAt` is an index into `capture`; once the buffer
// saturates, every append drops characters off the FRONT and every index into it
// must shift left by the same amount. It did not, so past `maxCapture` the mark
// stayed pinned at the end while the content slid out from under it:
// `getSinceMark()` returned "" for ever and every expect timed out on a console
// that was answering perfectly.
//
// This killed an Arista provisioning run at "write memory" — the whole push
// accepted, the save timed out, the configuration left in the running config
// only. Arista hit it first (a full Linux boot plus a ZTP retry line every
// thirty seconds is the most verbose console in the fleet); every platform was
// exposed.
async function markSurvivesABufferRollover() {
  var session = createSerialSession({ write: async () => {}, maxCapture: 100 });

  // Fill the buffer past its ceiling BEFORE marking, so the next appends are
  // guaranteed to be dropping from the front.
  session.append("x".repeat(400));
  session.mark();
  session.append("sw1#");

  assert.equal(
    session.getSinceMark(),
    "sw1#",
    "post-mark text was lost to the rollover: everything waiting on it times out",
  );

  // And the waiter path, which is what actually runs during a save.
  session.mark();
  var waiting = session.expect(/Copy completed successfully/, 2000);
  session.append("y".repeat(300));
  session.append("Copy completed successfully.\r\nsw1#");
  assert.match(await waiting, /Copy completed successfully/);
}

// A mark taken and then rolled entirely out of the buffer must degrade to "the
// whole buffer", never to a negative index or a stale one.
function markRolledCompletelyOutClampsToZero() {
  var session = createSerialSession({ write: async () => {}, maxCapture: 50 });
  session.append("a".repeat(50));
  session.mark();
  session.append("b".repeat(200));
  assert.equal(session.getSinceMark(), session.getCapture());
}

await markSurvivesABufferRollover();
markRolledCompletelyOutClampsToZero();

console.log("serialExpect.test.ts: ok");

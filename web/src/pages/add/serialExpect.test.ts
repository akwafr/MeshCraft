/**
 * Lightweight checks for serial expect rightmost-match.
 *   node --experimental-strip-types src/pages/add/serialExpect.test.ts
 */
import assert from "node:assert/strict";
import { rightmostMatch } from "./serialExpect.ts";

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

console.log("serialExpect.test.ts: ok");

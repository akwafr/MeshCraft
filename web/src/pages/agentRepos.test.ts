/**
 * Lightweight checks for agentRepos — run with:
 *   node --experimental-strip-types src/pages/agentRepos.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * The one that matters is [pullRef]: an agent image is pulled by a remote
 * proxy, and a ref that looks pullable but 404s (a name with no version) is the
 * exact failure the registry's tag resolution was hardened against — the UI
 * must not manufacture one.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { buildTone, canBuild, pullRef } from "./agentRepos.ts";

// buildTone: three tones, ok and failed the only coloured ones.
assert.equal(buildTone("ok"), "ok");
assert.equal(buildTone("failed"), "danger");
assert.equal(buildTone("running"), "");
assert.equal(buildTone("queued"), "");
assert.equal(buildTone(undefined), "");

// pullRef: a real ref only when there is a version to pull.
assert.equal(
  pullRef("ctrl.example.com", "meshcraft/agent-foo", "20260821.101500"),
  "ctrl.example.com/meshcraft/agent-foo:20260821.101500",
);
assert.equal(pullRef("ctrl.example.com", "meshcraft/agent-foo", ""), "");
assert.equal(pullRef("ctrl.example.com", "", "20260821.101500"), "");
// Trailing slashes on the host do not double up.
assert.equal(
  pullRef("ctrl.example.com/", "a/b", "v1"),
  "ctrl.example.com/a/b:v1",
);
// No host (unlikely, but must not produce a leading slash).
assert.equal(pullRef("", "a/b", "v1"), "a/b:v1");

// canBuild is the pipeline gate, plainly.
assert.equal(canBuild(true), true);
assert.equal(canBuild(false), false);

console.log("agentRepos: OK");

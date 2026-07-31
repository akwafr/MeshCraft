/**
 * Lightweight checks for modelShort — run with:
 *   node --experimental-strip-types src/modelLabel.test.ts
 * (from web/), or rely on tsc --noEmit covering types.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { modelShort } from "./modelLabel.ts";

assert.equal(
  modelShort("HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A"),
  "Aruba JL685A",
);
assert.equal(
  modelShort(
    "HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
    "Aruba (HPE)",
  ),
  "Aruba JL685A",
);
assert.equal(modelShort("CRS326-24G-2S+"), "CRS326-24G-2S+");
assert.equal(modelShort("CRS326-24G-2S+", "MikroTik"), "CRS326-24G-2S+");
assert.equal(modelShort("Generic 24p"), "Generic 24p");
assert.equal(modelShort(""), "");

console.log("modelLabel.test.ts: ok");

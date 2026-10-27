/**
 * Lightweight checks for modelShort — run with:
 *   node --experimental-strip-types src/modelLabel.test.ts
 * (from web/), or rely on tsc --noEmit covering types.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { modelOf, modelShort } from "./modelLabel.ts";

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

// --- modelOf: WHICH label, as opposed to modelShort's "does it fit" ---------

// The identity the controller derived wins over the raw declaration.
assert.equal(
  modelOf({
    model: "HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
    model_label: "Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
  }),
  "Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
);
// No identity derived (older payload, or no rule matched): the raw label is the
// name, and nothing is blanked out.
assert.equal(modelOf({ model: "CRS326-24G-2S+" }), "CRS326-24G-2S+");
assert.equal(
  modelOf({ model: "CRS326-24G-2S+", model_label: "" }),
  "CRS326-24G-2S+",
);
assert.equal(modelOf({}), "");
// The two compose, and the three grammars finally read as one model in a tile.
assert.equal(
  modelShort(
    modelOf({
      model: "HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
      model_label: "Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
    }),
    "Aruba (HPE)",
  ),
  modelShort(
    modelOf({
      model: "Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
      model_label: "Aruba Instant On 1930 48G 4SFP/SFP+ Switch",
    }),
    "Aruba (HPE)",
  ),
);

console.log("modelLabel.test.ts: ok");

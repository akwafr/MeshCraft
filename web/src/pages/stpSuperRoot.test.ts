/**
 * Lightweight checks for stpSuperRoot — run with:
 *   node --experimental-strip-types src/pages/stpSuperRoot.test.ts
 * (from web/), or `npm test` to run every file at once.
 *
 * Written as the CONFUSIONS they prevent rather than as coverage. Super Root is
 * a topology change with no recorded undo, so the expensive mistakes are all
 * about offering it where it cannot work:
 *
 *   - visible under rstp, where the field is meaningless and every connector
 *     answers it with a Skipped line nobody reads;
 *   - visible on a platform that cannot be put in mstp at all;
 *   - clickable next to an Apply button the endpoint answers with 422;
 *   - cleared without a priority, which leaves a switch at priority 0 with the
 *     screen claiming it is an ordinary bridge.
 */
import assert from "node:assert/strict";
// Node --experimental-strip-types needs the .ts suffix; tsc excludes this file.
import { superRootVerdict } from "./stpSuperRoot.ts";

const base = {
  mode: "mstp",
  modes: ["mstp", "rstp"],
  canEdit: true,
  writable: true,
  implementsSuperRoot: true,
  stored: false,
  checked: false,
};

// The ordinary case: MST, editable, writable, nothing surprising observed.
{
  const v = superRootVerdict(base);
  assert.equal(v.show, true);
  assert.equal(v.disabled, false);
  assert.equal(v.needsPriority, false);
  assert.equal(v.warnKey, "");
}

// HIDDEN UNDER RSTP. The contract scopes the field to mstp and every connector
// refuses it elsewhere — a visible box would promise a write that lands in a
// Skipped list, which reads on screen as success.
{
  const v = superRootVerdict({ ...base, mode: "rstp" });
  assert.equal(v.show, false);
}

// HIDDEN WHEN THE PLATFORM LISTS NO MSTP. A range that cannot be put in MST can
// never reach the state where this field means anything, so the row would be
// permanently decorative — and a decorative control is one somebody eventually
// ticks.
{
  const v = superRootVerdict({ ...base, modes: ["rstp", "stp"] });
  assert.equal(v.show, false);
}

// DISABLED, NOT HIDDEN, when the operator is read-only: the capability stays
// discoverable and the click does not.
{
  const v = superRootVerdict({ ...base, canEdit: false });
  assert.equal(v.show, true);
  assert.equal(v.disabled, true);
}

// DISABLED when the connector does not declare WritesExtra("stp"). This is the
// arista-eos case exactly — the only platform that implements Super Root at all
// has its STP write path unproven, so `modes` is populated and `write` is false.
// Gating on `modes` alone put the checkbox next to a button answering 422.
{
  const v = superRootVerdict({ ...base, writable: false });
  assert.equal(v.show, true);
  assert.equal(v.disabled, true);
}

// CLEARING NEEDS A PRIORITY. meshcraft never recorded what the bridge carried
// before it wrote 0 and no site plan holds one, so un-ticking alone would leave
// the switch unbeatable while the screen said otherwise. The controller answers
// 400 for the same request; this flag lets the panel ask first.
{
  const v = superRootVerdict({ ...base, stored: true, checked: false });
  assert.equal(v.needsPriority, true);
  // Re-ticking a stored intent asks for nothing: no priority is being restored.
  const back = superRootVerdict({ ...base, stored: true, checked: true });
  assert.equal(back.needsPriority, false);
}

// A WARNING MUST NOT HIDE THE ROW. Priority 0 observed with no meshcraft intent
// behind it means somebody set it by hand — worth saying, and NOT worth ticking
// the box, which would record a decision meshcraft never took. The operator
// opened this panel to resolve exactly this, so the control has to still be
// there.
{
  const v = superRootVerdict({
    ...base,
    observed: { enabled: true, mode: "mstp", bridge_priority: 0 },
  });
  assert.equal(v.show, true);
  assert.equal(v.disabled, false);
  assert.equal(v.warnKey, "stpSuperRootWarnManual");
}

// The intent says root and the switch says it is not: usually another bridge
// already at priority 0, where the tie-break is the MAC address and there is no
// knob left short of un-checking one of them.
{
  const v = superRootVerdict({
    ...base,
    stored: true,
    checked: true,
    observed: { enabled: true, mode: "mstp", bridge_priority: 0, is_root: false },
  });
  assert.equal(v.show, true);
  assert.equal(v.warnKey, "stpSuperRootWarn");
}

// A device that has never been read carries no observation, and absence is not
// a fault: no warning, and the control stays offered.
{
  const v = superRootVerdict({ ...base, stored: true, checked: true });
  assert.equal(v.warnKey, "");
}

console.log("stpSuperRoot: ok");

// THE HAZARD THE SPLIT GATE EXISTS FOR. `write` is WritesExtra("stp") — "this
// connector writes STP" — not "this connector implements Super Root". cisco/ios
// declares the first and deliberately refuses the second, so a gate on `write`
// alone rendered the checkbox ENABLED on a live Catalyst: ticking it did
// nothing, and un-ticking it went through the clear path, which demands a
// replacement priority and which cisco/ios DOES honour — writing a bridge
// priority to a production switch and costing it a root election it held on
// purpose.
function connectorThatCannotDoItNeverShowsTheRow() {
  var v = superRootVerdict({
    ...base,
    writable: true,          // cisco/ios: WritesExtra("stp") is true
    implementsSuperRoot: false, // …and it does not implement the field
  });
  assert.equal(v.show, false, "the row must be HIDDEN, not merely greyed: a greyed row " +
    "still leaves the stored intent reachable through the clear path");
  assert.equal(v.needsPriority, false, "a connector that never set anything must never " +
    "be asked for a priority to restore");

  // The only implementer, whose STP writes are not declared yet: visible so the
  // capability is discoverable, disabled so nothing is pushed.
  var eos = superRootVerdict({ ...base, writable: false, implementsSuperRoot: true });
  assert.equal(eos.show, true);
  assert.equal(eos.disabled, true);
}
connectorThatCannotDoItNeverShowsTheRow();

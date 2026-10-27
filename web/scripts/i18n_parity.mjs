#!/usr/bin/env node
// en and fr are the REFERENCE locales: every key in one must exist in the
// other, or a screen reads English on one instance and a raw key on the
// next. The nine other locales are allowed to LAG (they freeze between
// translation passes) but never to carry a key the references dropped —
// that is a rename that missed nine files.

import { readFileSync } from "node:fs";

const dir = new URL("../src/i18n/locales/", import.meta.url).pathname;
const load = (l) => JSON.parse(readFileSync(`${dir}${l}.json`, "utf8"));

function leaves(obj, prefix = "") {
  const out = [];
  for (const [k, v] of Object.entries(obj)) {
    const key = prefix ? `${prefix}.${k}` : k;
    if (v && typeof v === "object") out.push(...leaves(v, key));
    else out.push(key);
  }
  return out;
}

const en = new Set(leaves(load("en")));
const fr = new Set(leaves(load("fr")));
let failed = false;

for (const k of en) {
  if (!fr.has(k)) {
    console.error(`i18n: ${k} exists in en but not fr`);
    failed = true;
  }
}
for (const k of fr) {
  if (!en.has(k)) {
    console.error(`i18n: ${k} exists in fr but not en`);
    failed = true;
  }
}
for (const locale of ["nl", "es", "it", "pt", "de", "ru", "ar", "zh", "ja"]) {
  for (const k of leaves(load(locale))) {
    if (!en.has(k)) {
      console.error(`i18n: ${locale} carries ${k}, which en dropped — a rename missed it`);
      failed = true;
    }
  }
}
if (failed) process.exit(1);
console.log(`i18n parity: en/fr aligned (${en.size} keys), frozen locales carry no dropped keys`);

// The serial catalogue — ADR-0005.
//
// Plain node script, no framework: `npm test` runs these directly.

import assert from "node:assert/strict";
import {
  SERIAL_BRANDS,
  allSerialModels,
  brandOfKey,
  matchesBrand,
  matchesModel,
  modelOfDriver,
  modelOfKey,
  softResetDrivers,
} from "./serialCatalog.ts";

/** Stands in for i18next: returns the key's tail, which is enough to search. */
const LABELS: Record<string, string> = {
  "serialAdd.equipmentArista": "Arista EOS",
  "serialAdd.equipmentCisco2960x": "Catalyst 2960-X",
  "serialAdd.equipmentFs3900": "FS.com S3900",
  "serialAdd.equipmentFs3400": "FS.com S3400 PoE+",
  "juniperSerial.equipment": "Juniper EX (Junos)",
  "serialAdd.equipmentEdgeSwitch": "EdgeSwitch / UniFi",
};
const labelFor = (k: string) => LABELS[k] ?? k;

function everyModelIsReachableBothWays() {
  for (const m of allSerialModels()) {
    assert.ok(brandOfKey(m.key), `${m.key} has no brand`);
    assert.equal(modelOfKey(m.key)?.driver, m.driver);
    // Every model must carry a label this test knows, or the search
    // assertions below would silently be testing the raw i18n keys.
    assert.ok(LABELS[m.labelKey], `${m.labelKey} missing from the stub`);
  }
}

function keysAndDriversAreUnique() {
  const keys = allSerialModels().map((m) => m.key);
  const drivers = allSerialModels().map((m) => m.driver);
  assert.equal(new Set(keys).size, keys.length, `duplicate wizard key: ${keys}`);
  assert.equal(
    new Set(drivers).size,
    drivers.length,
    `duplicate driver key: ${drivers}`,
  );
}

function softResetListIsDerivedNotDuplicated() {
  // The point of ADR-0005's derivation: there is no second array to forget.
  assert.deepEqual(softResetDrivers(), allSerialModels().map((m) => m.driver));
  // And the six platforms that had a written procedure before the catalogue
  // existed are all still reachable — a regression here means a procedure
  // became unreachable from the UI without anyone noticing.
  for (const d of [
    "fs-s3900",
    "fs-s3400",
    "cisco-ios",
    "ubiquiti-edgeswitch",
    "arista-eos",
    "juniper-junos",
  ]) {
    assert.ok(softResetDrivers().includes(d), `${d} fell out of the catalogue`);
  }
}

function theListHoldsNothingWithoutAProcedure() {
  // The whole reason the screen changed: these were clickable and led to an
  // assistant with nothing to send. None of them may come back silently.
  for (const d of [
    "huawei-vrp",
    "extreme-exos",
    "aruba-cx",
    "cisco-nxos",
    "mikrotik-swos",
    "mikrotik-routeros",
    "hpe-procurve",
    "aruba-instant-on-1930",
  ]) {
    assert.ok(
      !softResetDrivers().includes(d),
      `${d} has no serial procedure and must not be offered`,
    );
  }
}

function brandSearchReachesTheModelNames() {
  const cisco = SERIAL_BRANDS.find((b) => b.id === "cisco")!;
  // Somebody holding the switch knows the number on the lid, not the vendor's
  // filing system — so the brand field searches model names too.
  assert.ok(matchesBrand(cisco, "2960", labelFor));
  assert.ok(matchesBrand(cisco, "catalyst", labelFor));
  assert.ok(matchesBrand(cisco, "CISCO", labelFor), "search is case-insensitive");
  assert.ok(!matchesBrand(cisco, "juniper", labelFor));
  // An empty query hides nothing.
  assert.ok(SERIAL_BRANDS.every((b) => matchesBrand(b, "   ", labelFor)));
}

function modelSearchFoldsCaseAndAccents() {
  const juniper = modelOfKey("juniper")!;
  assert.ok(matchesModel(juniper, "junos", labelFor));
  assert.ok(matchesModel(juniper, "EX", labelFor));
  // Accent folding, so a keyboard without them still finds a French name.
  assert.ok(
    matchesModel(
      { ...juniper, labelKey: "x" },
      "reinit",
      () => "Réinitialisation",
    ),
  );
}

function autoIsNotAModel() {
  // "Automatic discovery" is a brand with no model, not a model named auto:
  // the model field disappears rather than offering a list of one.
  assert.equal(brandOfKey("auto"), undefined);
  assert.equal(modelOfKey("auto"), undefined);
  assert.ok(!SERIAL_BRANDS.some((b) => b.id === "auto"));
}

function theDriverKeyIsTheJoinToTheResetScreen() {
  // ADR-0010: `/add/reset` lists what `internal/discovery/reset.go` carries
  // and enriches the rows this catalogue covers, joined on `driver`. The
  // lookup has to work in that direction, and it has to return NOTHING for a
  // driver the catalogue does not cover — a fallback to the first entry would
  // put "FS.com S3900" under a Huawei procedure.
  for (const m of allSerialModels()) {
    assert.equal(modelOfDriver(m.driver)?.key, m.key);
  }
  for (const d of ["huawei-vrp", "hpe-procurve", "zyxel", "", "generic"]) {
    assert.equal(
      modelOfDriver(d),
      undefined,
      `${d} is not in the catalogue and must not resolve to a model name`,
    );
  }
}

everyModelIsReachableBothWays();
theDriverKeyIsTheJoinToTheResetScreen();
keysAndDriversAreUnique();
softResetListIsDerivedNotDuplicated();
theListHoldsNothingWithoutAProcedure();
brandSearchReachesTheModelNames();
modelSearchFoldsCaseAndAccents();
autoIsNotAModel();

console.log("serialCatalog: OK");

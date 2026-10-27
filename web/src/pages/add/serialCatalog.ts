// The serial catalogue — brand, then model. Cf. ADR-0005.
//
// WHAT IS IN HERE IS A PROMISE THAT IS KEPT. The screen used to pour every
// driver `/api/drivers` returned into its tile grid, so `huawei-vrp` and
// `extreme-exos` were clickable and led to an assistant with nothing to send.
// An entry here means a procedure exists in `web/src/pages/add/*.ts` and runs
// end to end.
//
// It therefore DUPLICATES something the API already knows, and that is
// deliberate: the API lists what meshcraft can talk to, this lists what can be
// provisioned OVER A SERIAL CABLE, and those are different sets. The server
// has no idea which of its drivers has a console procedure, because the
// procedures live in the browser.
//
// Shared on purpose: `/add/serial` picks a procedure to RUN, `/add/reset`
// picks one to DISPLAY. Two screens, one list, or they drift.

export interface SerialModel {
  /** The key `Serial.tsx` already switches on for params and runners. */
  key: string;
  /** i18n key of the model's name. */
  labelKey: string;
  /** i18n key of the one-line description. */
  detailKey: string;
  /**
   * The platform key the controller uses — `internal/discovery/fingerprint.go`
   * and `/api/drivers` speak this one. Kept beside the UI key so a future
   * autodetect can map a banner onto a catalogue entry without a second table.
   */
  driver: string;
}

export interface SerialBrand {
  id: string;
  /** Brand names are proper nouns: not translated, not an i18n key. */
  label: string;
  models: SerialModel[];
}

/**
 * The sentinel brand. Picking it means "I do not know what this is, read the
 * banner and tell me" — so it carries no model, and the model field goes away
 * rather than showing a list of one.
 *
 * FIRST IN THE LIST, and that is the whole reason it is a brand rather than a
 * checkbox beside one: the person who needs it is holding an unknown switch,
 * and the first thing their eye lands on should be the way out.
 */
export const AUTO_BRAND_ID = "auto";

/** The `equipment` key the wizard uses for automatic detection. */
export const AUTO_KEY = "auto";

export const SERIAL_BRANDS: SerialBrand[] = [
  {
    id: "arista",
    label: "Arista",
    models: [
      {
        key: "arista",
        labelKey: "serialAdd.equipmentArista",
        detailKey: "serialAdd.equipmentAristaDetail",
        driver: "arista-eos",
      },
    ],
  },
  {
    id: "cisco",
    label: "Cisco",
    models: [
      {
        key: "cisco2960x",
        labelKey: "serialAdd.equipmentCisco2960x",
        detailKey: "serialAdd.equipmentCisco2960xDetail",
        driver: "cisco-ios",
      },
    ],
  },
  {
    id: "fs",
    label: "FS.com",
    models: [
      {
        key: "fs3900",
        labelKey: "serialAdd.equipmentFs3900",
        detailKey: "serialAdd.equipmentFs3900Detail",
        driver: "fs-s3900",
      },
      {
        key: "fs3400",
        labelKey: "serialAdd.equipmentFs3400",
        detailKey: "serialAdd.equipmentFs3400Detail",
        driver: "fs-s3400",
      },
    ],
  },
  {
    id: "juniper",
    label: "Juniper",
    models: [
      {
        key: "juniper",
        labelKey: "juniperSerial.equipment",
        detailKey: "juniperSerial.equipmentDetail",
        driver: "juniper-junos",
      },
    ],
  },
  {
    id: "ubiquiti",
    label: "Ubiquiti",
    models: [
      {
        key: "edgeswitch",
        labelKey: "serialAdd.equipmentEdgeSwitch",
        detailKey: "serialAdd.equipmentEdgeSwitchDetail",
        driver: "ubiquiti-edgeswitch",
      },
    ],
  },
];

/**
 * The drivers whose soft reset is scripted end to end.
 *
 * DERIVED, not listed. `Serial.tsx` used to carry this as a hand-written
 * array beside a second hand-written array of the same six keys
 * (`TILED_DRIVERS`), and a catalogue beside them would have made three
 * places to forget. A platform is soft-resettable because it is in the
 * catalogue; there is nowhere left for the two to disagree.
 */
export function softResetDrivers(): string[] {
  return allSerialModels().map((m) => m.driver);
}

/** Every model, flat — for lookups and for the coverage test. */
export function allSerialModels(): SerialModel[] {
  return SERIAL_BRANDS.flatMap((b) => b.models);
}

/** The brand owning a wizard key, or undefined for `auto` / an unknown key. */
export function brandOfKey(key: string): SerialBrand | undefined {
  return SERIAL_BRANDS.find((b) => b.models.some((m) => m.key === key));
}

export function modelOfKey(key: string): SerialModel | undefined {
  return allSerialModels().find((m) => m.key === key);
}

/**
 * The catalogue entry for a controller driver key, if there is one — ADR-0010.
 *
 * `/add/reset` lists what `internal/discovery/reset.go` carries, which is
 * three times this catalogue, and uses this to ENRICH the six rows the
 * catalogue covers with a written model name instead of the raw `Family`
 * string. `driver` was already the only key between the two tables; this is
 * the lookup in that direction, and it is why no third table was needed.
 *
 * The catalogue still decides alone what `/add/serial` may RUN (ADR-0005):
 * answering "is this driver in the catalogue" is not the same question as
 * "is this driver listed on the reset screen", and only the first one is a
 * promise.
 */
export function modelOfDriver(driver: string): SerialModel | undefined {
  return allSerialModels().find((m) => m.driver === driver);
}

/**
 * Case- and accent-insensitive substring match over a brand's name and every
 * model name it owns.
 *
 * Searching the MODELS from the brand field is the point: somebody holding a
 * 2960-X knows the number on the lid, not that Cisco files it under IOS. A
 * brand-only filter would make them guess the vendor first, which is the step
 * they came here to skip.
 *
 * `labelFor` resolves a model's i18n key — the caller owns `t`, this module
 * stays pure and testable without i18next.
 */
export function matchesBrand(
  brand: SerialBrand,
  query: string,
  labelFor: (key: string) => string,
): boolean {
  const q = fold(query);
  if (!q) return true;
  if (fold(brand.label).includes(q)) return true;
  return brand.models.some((m) => fold(labelFor(m.labelKey)).includes(q));
}

export function matchesModel(
  model: SerialModel,
  query: string,
  labelFor: (key: string) => string,
): boolean {
  const q = fold(query);
  if (!q) return true;
  return fold(labelFor(model.labelKey)).includes(q);
}

/**
 * Lowercase and strip diacritics, so "Réinitialiser" matches "reinit" and a
 * keyboard without accents still finds French model names.
 */
function fold(s: string): string {
  return s
    .normalize("NFD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .trim();
}

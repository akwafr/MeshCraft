import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { get, type ResetProcedure } from "../../api";
import { ErrorMsg } from "../../components";
import SearchSelect, { type SearchOption } from "../../ui/SearchSelect";
import { modelOfDriver } from "./serialCatalog";

/**
 * Reset procedures, carried by the drivers.
 *
 * ONE LIST, AND IT IS THE WHOLE LIST — ADR-0010. The screen used to offer the
 * five brands of `serialCatalog.ts` plus a sentinel, "another platform…",
 * which switched it into a free server-side search. That is where the other
 * fifteen procedures lived, SEVEN OF THEM `Verified: true` (ProCurve,
 * RouterOS, UniFi, EXOS, VRP, Comware, Cumulus/SONiC). Two grammars on one
 * screen, and the majority of the content behind a door labelled "the rest".
 *
 * The brand list is grouped on `procedure.brand`, a slug the controller
 * carries for exactly this — NOT on the `vendor` label, which is a sentence
 * and will be reworded. `serialCatalog.ts` no longer decides what is listed;
 * it ENRICHES the six rows it covers, joined by `driver`, with a written model
 * name in place of the raw `family` string.
 *
 * ADR-0005's rule is untouched where it belongs: `/add/serial` still lists
 * only what it can RUN, because there an entry promises an assistant with
 * something to send. Here there is nothing to send — the procedure exists and
 * is verified, and the screen only displays it.
 */

/** A brand of the list: one slug, one label, the procedures under it. */
type Brand = {
  id: string;
  label: string;
  procedures: ResetProcedure[];
};

/**
 * The model's name, written if the catalogue covers it, the driver's own
 * `family` string otherwise.
 *
 * `family` is perfectly readable ("VRP (S-series)", "JetStream / Easy
 * Smart") — it is what the driver author wrote to name the range. The
 * catalogue's label is better where it exists ("FS.com S3400 PoE+") because
 * it was written for somebody holding the box.
 */
function modelLabel(p: ResetProcedure, t: (k: string) => string): string {
  const m = modelOfDriver(p.driver);
  return m ? t(m.labelKey) : p.family;
}

/**
 * Hidden search text. The aliases are the numbers on the lid — `s5720`,
 * `2530`, `gs1900` — and the single most useful thing to match on; printing a
 * dozen of them per row would bury the name instead.
 */
function keywordsOf(ps: ResetProcedure[]): string {
  return ps
    .flatMap((p) => [p.driver, p.family, ...p.aliases])
    .join(" ");
}

export default function ResetProcedures() {
  const { t } = useTranslation();
  const [brandId, setBrandId] = useState("");
  const [driverKey, setDriverKey] = useState("");

  // The WHOLE set, once: twenty-one rows, and the filtering happens in the
  // browser from here on. The endpoint keeps its `q` for `/add/serial`, which
  // asks it for one driver.
  const { data, error, isFetching } = useQuery({
    queryKey: ["reset-procedures", ""],
    queryFn: () => get<ResetProcedure[]>("/api/discovery/reset-procedures?q="),
  });

  const all = data ?? [];

  // Grouped in the order the controller declares them, which is the order a
  // driver author put them in — not alphabetical. A list that re-sorted the
  // server's answer would make the two impossible to read side by side.
  const brands = useMemo<Brand[]>(() => {
    const out: Brand[] = [];
    for (const p of data ?? []) {
      const found = out.find((b) => b.id === p.brand);
      if (found) {
        found.procedures.push(p);
        continue;
      }
      out.push({ id: p.brand, label: p.vendor, procedures: [p] });
    }
    return out;
  }, [data]);

  const brandOptions = useMemo<SearchOption[]>(
    () =>
      brands.map((b) => ({
        id: b.id,
        label: b.label,
        // The families ride in the detail line so the BRAND field is
        // searchable by what is written on the box — the step somebody came
        // here to skip is guessing the vendor's filing system.
        detail: b.procedures.map((p) => modelLabel(p, t)).join(" · "),
        keywords: keywordsOf(b.procedures),
      })),
    [brands, t],
  );

  const brand = brands.find((b) => b.id === brandId);
  const modelOptions = useMemo<SearchOption[]>(
    () =>
      (brand?.procedures ?? []).map((p) => {
        const m = modelOfDriver(p.driver);
        return {
          id: p.driver,
          label: modelLabel(p, t),
          detail: m
            ? t(m.detailKey)
            : t("resetProcedures.driverLabel", { driver: p.driver }),
          keywords: keywordsOf([p]),
        };
      }),
    [brand, t],
  );

  const shown = all.filter((p) => p.driver === driverKey);

  // A brand with ONE procedure selects it outright, the way `/add/serial`
  // does: a list with a single answer is ceremony, not a choice. Most brands
  // here carry one, so the screen is two gestures for almost all of them.
  function pickBrand(id: string) {
    setBrandId(id);
    const b = brands.find((x) => x.id === id);
    setDriverKey(b && b.procedures.length === 1 ? b.procedures[0].driver : "");
  }

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.resetEquipment")}
          <span className="sub">{t("resetProcedures.subtitle")}</span>
        </h1>
      </div>

      <div className="card addpick-card">
        <div className="addpick">
          <SearchSelect
            id="reset-brand"
            label={t("resetProcedures.brandLabel")}
            placeholder={t("resetProcedures.brandPlaceholder")}
            value={brandId}
            options={brandOptions}
            emptyText={t("resetProcedures.brandEmpty") as string}
            onChange={pickBrand}
          />
          {brand && brand.procedures.length > 1 && (
            <SearchSelect
              id="reset-model"
              label={t("resetProcedures.modelLabel")}
              placeholder={t("resetProcedures.modelPlaceholder")}
              value={driverKey}
              options={modelOptions}
              onChange={setDriverKey}
            />
          )}
        </div>

        <div className="note addpick-note">{t("resetProcedures.warning")}</div>
        <div className="note addpick-note">{t("resetProcedures.listNote")}</div>
      </div>

      <ErrorMsg error={error} />
      {isFetching && <div className="note">{t("common.searching")}</div>}

      {/* Two empty states, because they ask two different things of the
          reader: choose a brand, or choose among this brand's families. One
          "nothing here" for both would leave the next gesture unsaid. */}
      {!isFetching && !brandId && (
        <div className="card">
          <div className="empty">{t("resetProcedures.pickBrand")}</div>
        </div>
      )}
      {!isFetching && brand && !driverKey && (
        <div className="card">
          <div className="empty">{t("resetProcedures.pickModel")}</div>
        </div>
      )}

      {shown.length > 0 && (
        <div className="reset-list">
          {shown.map((p) => (
            <Procedure key={p.driver} p={p} />
          ))}
        </div>
      )}
    </div>
  );
}

function Procedure({ p }: { p: ResetProcedure }) {
  const { t } = useTranslation();
  return (
    <div className="card">
      <div className="row reset-head">
        <span className="reset-family">{p.family}</span>
        <span className="chip">{p.vendor}</span>
        <span className="note">
          {t("resetProcedures.driverLabel", { driver: p.driver })}
        </span>
        <span className="reset-state">
          {p.verified ? (
            <span className="badge">{t("resetProcedures.stableProcedure")}</span>
          ) : (
            <span className="badge warn">{t("resetProcedures.toConfirm")}</span>
          )}
        </span>
      </div>

      <div className="reset-cols">
        {p.software.length > 0 && (
          <div>
            <div className="kicker">{t("resetProcedures.viaCli")}</div>
            <ol className="reset-steps">
              {p.software.map((s, i) => (
                <li key={i}>
                  <code>{s}</code>
                </li>
              ))}
            </ol>
          </div>
        )}
        {p.hardware.length > 0 && (
          <div>
            <div className="kicker">{t("resetProcedures.viaHardware")}</div>
            <ol className="reset-steps">
              {p.hardware.map((s, i) => (
                <li key={i}>{s}</li>
              ))}
            </ol>
          </div>
        )}
      </div>

      {/* Server text, shown verbatim: the caveats are the driver's knowledge
          of the platform, not a wording this screen may improve on. */}
      <div className="note reset-erases">
        <b>{t("resetProcedures.erasesLabel")}</b> {p.erases}
      </div>
      {p.caveats.length > 0 && (
        <ul className="warn-list">
          {p.caveats.map((c, i) => (
            <li key={i}>{c}</li>
          ))}
        </ul>
      )}
      {p.aliases.length > 0 && (
        <div className="chip-row reset-aliases">
          {p.aliases.map((a) => (
            <span key={a} className="chip">
              {a}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

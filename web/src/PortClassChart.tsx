import { useTranslation } from "react-i18next";
import { decimalSeparator } from "./network";
import type { PortClassBand, TrafficClassKey } from "./telemetry";
import { LineChart, SERIES } from "./viz";

/**
 * The traffic of ONE port decomposed by destination class — multicast,
 * broadcast and unknown unicast — as a STACKED packets-per-second chart, so
 * the top of the stack is the total and each band's thickness is its share.
 *
 * Shared by the port hover tip, the port panel's Info section and the port
 * action overlay, for the reason LevelChart is shared: the three must never
 * disagree about the same port. One component, one stack order, one sentence
 * for a class that is missing.
 *
 * # What this draws when a class is missing, which is most of the point
 *
 * The three classes here are exactly the ones a storm limiter polices, and
 * they are NOT equally collectable — no platform in the fleet counts forwarded
 * unknown unicast at all (ADR-0028). So a two-band stack is the normal case,
 * and a two-band stack that merely OMITTED the third would be indistinguishable
 * from a complete one: the reader would take the top edge for the total and
 * conclude this port floods nothing.
 *
 * Every class therefore gets a line on screen whatever happens, and the three
 * silences are kept apart (cf. PortClassState):
 *
 *   - `undeclared` — the platform cannot count it. A PERMANENT fact, stated in
 *     the info ink UI-GUIDE §2 reserves for a platform limit, because it calls
 *     for no action: nobody can fix it by looking at the switch;
 *   - `pending` — declared, nothing yet. Dim ink, the "we were never told"
 *     register, because it may fill in on the next poll;
 *   - `capsUnknown` — no connector resolved, so we cannot say which. Rendered
 *     as its own sentence and never as `undeclared`; "we cannot tell" is not
 *     "the platform says no", and UI-GUIDE §2 is explicit that collapsing the
 *     two is the error.
 *
 * Renders NOTHING AT ALL only when there is nothing to say — no bands and no
 * capabilities — leaving the caller's own empty-state copy in place.
 */

/** Stack colours: the product's three CVD-checked series inks, in stack order. */
const CLASS_COLOR: Record<TrafficClassKey, string> = {
  multicast: SERIES.in,
  broadcast: SERIES.out,
  unknown_unicast: SERIES.third,
};

/**
 * Packets per second, short enough for a 320 px tooltip.
 *
 * A rate under 10 keeps a decimal: the interesting broadcast rates on a quiet
 * port are fractions, and rounding 0.4 to "0" would read as silence on a port
 * that is in fact talking.
 */
function fmtPps(v: number, unit: string, dsep: string): string {
  const a = Math.abs(v);
  let n: string;
  if (a >= 10000) n = (v / 1000).toFixed(0) + " k";
  else if (a >= 1000) n = (v / 1000).toFixed(1).replace(".", dsep) + " k";
  else if (a >= 10) n = v.toFixed(0);
  else n = v.toFixed(1).replace(".", dsep);
  return n + " " + unit;
}

export function PortClassChart({
  bands,
  stepSeconds,
  height = 132,
}: {
  bands: PortClassBand[];
  stepSeconds: number;
  height?: number;
}) {
  const { t } = useTranslation();
  const dsep = decimalSeparator();
  const unit = t("switchDetailPage.portClassUnit");

  const drawn = bands.filter((b) => b.state === "ok");
  const missing = bands.filter((b) => b.state !== "ok");
  if (drawn.length === 0 && missing.length === 0) return null;

  const label = (k: TrafficClassKey) => t("switchDetailPage.portClass_" + k);
  const why = (s: PortClassBand["state"]) =>
    s === "undeclared"
      ? t("switchDetailPage.portClassUndeclared")
      : s === "capsUnknown"
        ? t("switchDetailPage.portClassCapsUnknown")
        : t("switchDetailPage.portClassPending");

  // NOTHING DRAWN AND ALL FOR THE SAME REASON IS ONE FACT, SAID ONCE.
  //
  // Printing it per class produced three identical sentences stacked under
  // each other, and three identical sentences do not read as one fact repeated
  // — they read as three separate measurements that all happen to be missing,
  // which is a bigger and more alarming claim than the truth.
  //
  // The class NAMES go with them, deliberately. Naming a class earns its line
  // only when it distinguishes that class from the others; when the answer is
  // the same for all three, the names carry nothing and the reason carries
  // everything.
  //
  // THE REASON STILL VARIES, and that is the half this must not flatten: "this
  // platform does not report the decomposition" and "nothing has been measured
  // yet" are different sentences, one permanent and one transient, and keeping
  // them apart is the entire purpose of the declared capability. So the
  // collapse is on a UNIFORM state, never on "nothing to draw".
  const uniform =
    drawn.length === 0 && missing.length > 1 &&
    missing.every((b) => b.state === missing[0].state)
      ? missing[0].state
      : null;
  if (uniform) {
    return (
      <div className="portclass">
        <div className="portclass-gaps">
          <div
            className={
              "portclass-gap" +
              (uniform === "undeclared" ? " portclass-gap-limit" : "")
            }
          >
            <span className="portclass-gap-why">
              {uniform === "undeclared"
                ? t("switchDetailPage.portClassAllUndeclared")
                : why(uniform)}
            </span>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div className="portclass">
      {drawn.length > 0 && (
        <LineChart
          stack
          height={height}
          totalLabel={t("switchDetailPage.portClassTotal")}
          series={drawn.map((b) => ({
            name: label(b.key),
            color: CLASS_COLOR[b.key],
            points: b.points,
          }))}
          format={(v) => fmtPps(v, unit, dsep)}
          xLabel={({ rawIndex, rawTotal, newest }) => {
            if (newest) return t("switchDetailPage.chartNow");
            const back = (rawTotal - 1 - rawIndex) * stepSeconds;
            if (back < 90) return t("switchDetailPage.chartSecondsAgo", { n: Math.round(back) });
            if (back < 5400) return t("switchDetailPage.chartMinutesAgo", { n: Math.round(back / 60) });
            if (back < 48 * 3600) return t("switchDetailPage.chartHoursAgo", { n: Math.round(back / 3600) });
            return t("switchDetailPage.chartDaysAgo", { n: Math.round(back / 86400) });
          }}
        />
      )}
      {missing.length > 0 && (
        <div className="portclass-gaps">
          {missing.map((b) => (
            <div
              key={b.key}
              className={
                "portclass-gap" +
                (b.state === "undeclared" ? " portclass-gap-limit" : "")
              }
            >
              {/* NAMED, here, and that is the opposite call from the collapse
                  above — on purpose. When only SOME classes are missing, the
                  stack that remains is not the port's whole traffic, and the
                  only thing standing between the reader and a wrong total is
                  knowing exactly WHICH class is absent. */}
              <span className="portclass-gap-name">{label(b.key)}</span>
              <span className="portclass-gap-why">{why(b.state)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

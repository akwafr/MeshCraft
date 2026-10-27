import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Device } from "../../api";
import { QuickTip } from "../../components";
import { configFreshness } from "../../freshness";
import { agoLabel } from "../../network";

/**
 * HOW OLD IS THIS SCREEN — one line, next to the sync verdict.
 *
 * # Why it exists
 *
 * The switch sheet updates itself (the site's change stream, `useSiteLive`),
 * so a port that flips from shut to no-shut arrives without a click. That is
 * an improvement with a sharp edge: a screen that CAN update by itself and a
 * screen that HAS updated look identical, and an operator who cannot tell them
 * apart is back to clicking refresh — the same complaint in a different form.
 * The date answers it: `observed_at` advances every time a connector reads the
 * device (store.SaveObserved), so "read 12 min ago" and "read 4 h ago" are two
 * genuinely different screens and now say so.
 *
 * It is deliberately the CONFIGURATION's age and not the sheet's. The two
 * differ, and the configuration's is the honest one: react-query may have
 * re-fetched the device list four seconds ago and received a row nobody has
 * re-read since this morning. Timestamping the fetch would certify freshness
 * the data does not have — precisely the reassurance this line exists to
 * refuse.
 *
 * # Why it keeps its own clock
 *
 * Time passes whether or not the query re-fetches. On a quiet site the stream
 * sends nothing for minutes, this page never re-renders, and a "read 1 min
 * ago" frozen at one minute for an hour would be worse than no line at all —
 * it is the original bug, wearing the label of its own fix.
 *
 * Hence the tick, and hence this being its own component: the interval
 * re-renders one `<span>` rather than the faceplate, the telemetry panel and
 * every port cell on the sheet. 30 s against a ladder whose finest step is the
 * minute — enough that the displayed value is never more than half a step
 * behind, cheap enough to leave running.
 *
 * `visibilitychange` is not an optimisation here, it is a correctness fix.
 * Browsers throttle timers in background tabs to a minute or worse, so a tab
 * left open all afternoon comes back showing whatever it last managed to
 * compute. Recomputing the moment it becomes visible means the first thing the
 * operator reads is current. The throttling itself is welcome and left alone —
 * a hidden tab nobody is reading has nothing to keep up to date.
 */
export function ConfigFreshness({ dev }: { dev: Device }) {
  const { t } = useTranslation();
  const [, tick] = useState(0);

  useEffect(() => {
    const bump = () => tick((n) => n + 1);
    const id = setInterval(bump, 30_000);
    const onVisible = () => {
      if (document.visibilityState === "visible") bump();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(id);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, []);

  const fresh = configFreshness(dev);
  // NEVER READ: nothing is printed. `syncLabel` already says "Never reread"
  // immediately to the left, and a second sentence for the same fact would read
  // as a second fact. The verdict is computed anyway so the reason lives in one
  // place (freshness.ts) rather than as an `!dev.observed_at` test here.
  if (fresh.kind === "never" || !dev.observed_at) return null;

  const age = agoLabel(dev.observed_at);
  // An empty age means the two clocks disagree — the browser believes the
  // reading landed in the future. Saying nothing beats printing a negative age.
  if (!age) return null;

  return (
    <QuickTip
      text={t(
        fresh.kind === "blind"
          ? "switchDetailPage.configReadBlindTitle"
          : "switchDetailPage.configReadTitle",
      )}
      className="quicktip-inline"
    >
      <span className="note" style={{ color: fresh.color }}>
        {t("switchDetailPage.configReadAgo", { age })}
      </span>
    </QuickTip>
  );
}

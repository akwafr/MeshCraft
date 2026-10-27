// "Your clock and the controller's disagree" — ADR-0008.
//
// A SYSTEM MESSAGE, and it lives in Configuration only. Under ADR-0006 this
// sat above every page, because a skew made every age on every screen wrong.
// It no longer does: ADR-0008 translates server timestamps into the viewer's
// frame, so nothing on a port facade is affected and there is no reason to
// interrupt somebody reading one.
//
// What remains worth saying — once, where instance settings live — is that
// the two clocks differ, because displayed times will then not match the
// server-side logs.
//
// It names a DISAGREEMENT, not a culprit. The viewer's workstation is the
// likely one — it is the clock nobody administers — but a controller whose own
// host has drifted produces the identical reading, and this component cannot
// tell which. Saying "your clock is wrong" would be the same mistake in the
// other direction.

import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { Banner } from "./Banner";
import { SKEW_WARN_MS, clockSkewIsSignificant, clockSkewMs } from "./clock";

export default function ClockSkewBanner() {
  const { t } = useTranslation();
  // The offset is a module value updated by the api layer, not React state:
  // nothing re-renders when it changes. A slow poll is enough — the banner
  // reports a standing condition, not an event, and a skew that matters does
  // not appear and vanish between two frames.
  const [, tick] = useState(0);
  useEffect(() => {
    const h = window.setInterval(() => tick((n) => n + 1), 20_000);
    return () => window.clearInterval(h);
  }, []);

  if (!clockSkewIsSignificant()) return null;

  const skew = clockSkewMs();
  // Rounded to the minute: the reading is good to a round trip and a one-second
  // `Date` header, so a figure to the second would claim a precision this does
  // not have.
  const minutes = Math.round(Math.abs(skew) / 60_000);
  const ahead = skew < 0; // our clock is ahead of the server's

  return (
    <Banner
      tone="act"
      className="clockskew-banner"
      title={
        ahead ? t("clockSkew.ahead", { minutes }) : t("clockSkew.behind", { minutes })
      }
      note={t("clockSkew.effect")}
    />
  );
}

/** Exported for the test: the threshold is the proxy list's own online cut. */
export { SKEW_WARN_MS };

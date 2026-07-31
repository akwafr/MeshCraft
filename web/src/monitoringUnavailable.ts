import type { TFunction } from "i18next";

import i18n from "./i18n";

/**
 * Operator-facing explanation for a metric the platform does not publish.
 * Drivers only declare the metric key; copy lives in the i18n catalog.
 */
export function monitoringUnavailableCopy(t: TFunction, metric: string): string {
  const key = `monitoring.unavailable.${metric}`;
  if (i18n.exists(key)) {
    return t(key);
  }
  return t("monitoring.unavailable.generic", { metric });
}

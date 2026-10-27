import { useTranslation } from "react-i18next";
import type { Device, ProxyInfo } from "../../api";
import { HelpTip, QuickTip } from "../../components";

/**
 * WHO POLLS THIS DEVICE — the collector row of the switch summary, just after
 * the address: both say the same thing from two angles, how we talk to this
 * device and who talks to it.
 *
 * RENDERS NOTHING unless there is something to DECIDE: at least two covering
 * proxies, or an attachment already declared. With one covering proxy and no
 * attachment, coverage already decides and the row would teach nothing. The
 * second condition is not a convenience: without it, an attachment that became
 * non-covering would be flagged by the banner and impossible to fix on this
 * page.
 *
 * `covering` is deliberately the proxies that COVER this site and not the full
 * inventory: attachment alone gives no coverage, so offering a proxy that does
 * not cover the site would not move collection elsewhere, it would stop it.
 */
export function CollectorRow({
  dev,
  covering,
  stranded,
  canEdit,
  busy,
  onChange,
}: {
  dev: Device;
  /** The proxies that cover this site — the only assignable ones. */
  covering: ProxyInfo[];
  /** Attached to a proxy that no longer covers the site. */
  stranded: boolean;
  canEdit: boolean;
  busy: boolean;
  /** Proxy id, or "" to detach. */
  onChange: (proxyId: string) => void;
}) {
  const { t } = useTranslation();
  if (!(covering.length > 1 || dev.proxy_id)) return null;
  return (
    <>
      <span className="k" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
        {t("switchDetailPage.collectorLabel")}
        <HelpTip label={t("switchDetailPage.collectorHelpLabel")}>
          <p>{t("switchDetailPage.collectorHelp1")}</p>
          <p>{t("switchDetailPage.collectorHelp2")}</p>
        </HelpTip>
      </span>
      {!canEdit ? (
        // In read-only mode, the collector regime is spelled
        // out in full. A "—" would suggest missing data
        // where this is in fact normal operation.
        <span>
          {dev.proxy_name || t("switchDetailPage.noCollectorCoverage")}
          {stranded ? t("switchDetailPage.noLongerCoversSite") : ""}
        </span>
      ) : (
        <QuickTip text={t("switchDetailPage.collectorSelectTitle")} className="quicktip-inline">
          <select
            value={dev.proxy_id ?? ""}
            disabled={busy}
            style={{ minHeight: 28, fontSize: 12 }}
            onChange={(e) => onChange(e.target.value)}
          >
          {/* Detachment listed first, and named: this is a fleet-wide regime,
              not a checkbox for "none". */}
          <option value="">{t("switchDetailPage.noCollectorOption")}</option>
          {covering.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
              {p.global ? t("switchDetailPage.instanceProxySuffix") : ""}
              {p.revoked ? t("switchDetailPage.revokedSuffix") : ""}
            </option>
          ))}
          {/* Non-covering attachment is NOT in the list: without this entry
              the select would show the first option and imply the device
              is detached — a screen that would lie about the very state
              it exists to fix. */}
          {stranded && (
            <option value={dev.proxy_id}>
              {t("switchDetailPage.unknownProxyNoCovers", { name: dev.proxy_name || t("switchDetailPage.unknownProxy") })}
            </option>
          )}
        </select>
        </QuickTip>
      )}
    </>
  );
}

import { useTranslation } from "react-i18next";
import type { AddressDomain } from "../../api";
import { HelpTip, QuickTip } from "../../components";

/**
 * WHICH NETWORK THIS EQUIPMENT LIVES IN, for declaration purposes — the
 * addressing-domain row of the switch summary. Especially useful for devices
 * entered by hand: those coming from discovery inherit the domain of their
 * address range.
 *
 * RENDERS NOTHING unless there is something to choose: on a single-network
 * instance the question does not arise and the row would only clutter. Nothing
 * for a read-only role either — this row is purely a control, and its value is
 * an internal declaration rather than a fact about the device.
 */
export function AddressDomainRow({
  domains,
  value,
  canEdit,
  busy,
  onChange,
}: {
  /** The instance's addressing domains; `undefined` while the list loads. */
  domains: AddressDomain[] | undefined;
  /** `dev.address_domain_id` — absent means undeclared. */
  value: string | undefined;
  canEdit: boolean;
  busy: boolean;
  /** Domain id, or "" to undeclare. */
  onChange: (domainId: string) => void;
}) {
  const { t } = useTranslation();
  if (!canEdit || (domains ?? []).length <= 1) return null;
  return (
    <>
      <span className="k" style={{ display: "inline-flex", alignItems: "center", gap: 4 }}>
        {t("switchDetailPage.networkLabel")}
        <HelpTip label={t("switchDetailPage.networkHelpLabel")}>
          <p>{t("switchDetailPage.networkHelp1")}</p>
          <p>{t("switchDetailPage.networkHelp2")}</p>
        </HelpTip>
      </span>
      <QuickTip text={t("switchDetailPage.networkTitle")} className="quicktip-inline">
        <select
          value={value ?? ""}
          disabled={busy}
          style={{ minHeight: 28, fontSize: 12 }}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">{t("switchDetailPage.undeclaredNetwork")}</option>
          {(domains ?? []).map((d) => (
            <option key={d.id} value={d.id}>
              {d.name}
            </option>
          ))}
        </select>
      </QuickTip>
    </>
  );
}

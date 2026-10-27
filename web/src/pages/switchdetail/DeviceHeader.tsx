import { useTranslation } from "react-i18next";
import type { Device } from "../../api";
import { QuickTip } from "../../components";
import { modelOf } from "../../modelLabel";
import { deviceRates, fmtMbps } from "../../network";
import { ProtocolPills, StatusDot } from "../../netui";

/**
 * THE DEVICE'S OWN LINE: what this box is called, what it is, and the three
 * numbers that describe the whole switch rather than any one socket.
 *
 * The numbers are on the RIGHT of the name and not in the side panel on
 * purpose — they belong to the device, and the side panel is given over to the
 * selected port as soon as one is clicked.
 */
export function DeviceHeader({
  dev,
  canEdit,
  off,
  rates,
  poePct,
  onRename,
}: {
  dev: Device;
  /** site_admin: only they get the editable title. */
  canEdit: boolean;
  off: boolean;
  /** `deviceRates(dev)` — throughput, ports up, PoE draw. */
  rates: ReturnType<typeof deviceRates>;
  /** PoE draw as a percentage of the budget, already rounded and clamped. */
  poePct: number;
  onRename: (name: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <div className="row" style={{ alignItems: "flex-end", marginTop: 8 }}>
      <div style={{ minWidth: 0 }}>
        <div className="row" style={{ gap: 10 }}>
          <StatusDot dev={dev} />
          {canEdit ? (
            /* THE ONLY NAME THERE IS. This input writes `devices.name`
               through the ordinary PATCH, and the controller writes that
               same name onto the EQUIPMENT as a consequence — there is no
               second field and no button for it any more.

               The tip says so, because the reach of this edit is no longer
               obvious: renaming here changes the console banner, the LLDP
               system-name TLV every neighbour sees, and every syslog line
               the device emits. An operator who thought they were editing an
               inventory label would be surprised at three in the morning —
               which is exactly when those three matter. The verdict on
               whether the equipment actually agrees is in the summary panel,
               next to the address. */
            <QuickTip text={t("switchDetailPage.hostnameTitle")} className="quicktip-inline">
              <input
                key={dev.id + ":name"}
                className="title-input"
                defaultValue={dev.name}
                onKeyDown={(e) => {
                  if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                }}
                onBlur={(e) => {
                  const v = e.target.value.trim();
                  if (v && v !== dev.name) onRename(v);
                  else e.target.value = dev.name;
                }}
              />
            </QuickTip>
          ) : (
            <h1 style={{ margin: 0 }}>{dev.name}</h1>
          )}
          <ProtocolPills dev={dev} />
        </div>
        {/* The model line shows the IDENTITY, not the declared string (ADR-0014):
            this fleet carries one switch under three vendor labels, and a sheet
            naming each device differently is what the operator reported. The
            catalogue reference rides along when the label carried one — it is
            the most precise thing available, and the most often absent. The raw
            declaration is a hover away, because it is the first thing anybody
            asks for when a faceplate looks wrong. */}
        <QuickTip
          text={modelOf(dev) !== dev.model ? dev.model : undefined}
          className="quicktip-block"
        >
          <div className="note" style={{ marginTop: 4 }}>
            {[modelOf(dev), dev.model_sku, dev.location, dev.mgmt_ip, dev.extra?.os, dev.extra?.uptime && t("switchDetailPage.inServicePrefix") + dev.extra.uptime]
              .filter(Boolean)
              .join(" · ")}
          </div>
        </QuickTip>
      </div>
      <div className="row" style={{ marginLeft: "auto", gap: 26, flex: "none" }}>
        <Metric k={t("switchDetailPage.switchThroughput")} v={off ? "—" : "↓ " + fmtMbps(rates.rx) + "  ↑ " + fmtMbps(rates.tx)} />
        <Metric k={t("switchDetailPage.activePorts")} v={rates.up + " / " + rates.total} />
        {dev.poe_budget_w > 0 && (
          <Metric
            k={t("switchDetailPage.poeBudget")}
            v={Math.round(rates.poe) + " / " + dev.poe_budget_w + " W"}
            color={poePct >= 85 ? "var(--warn)" : undefined}
          />
        )}
      </div>
    </div>
  );
}

function Metric({ k, v, color }: { k: string; v: string; color?: string }) {
  return (
    <div>
      <div className="kicker" style={{ marginBottom: 3 }}>{k}</div>
      <div style={{ fontSize: 14, fontWeight: 700, whiteSpace: "pre", color: color ?? "var(--text)" }}>
        {v}
      </div>
    </div>
  );
}

import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import { useSiteLive, get, type Device, type SiteLldp, type Vlan } from "../api";
import { useSiteRole } from "../auth";
import { sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import { ActionButton, ErrorMsg, QuickTip, useToast } from "../components";
import {
  deviceRates,
  expandPorts,
  fmtMbps,
  reachability,
  serves,
  statusColor,
  statusLabel,
  vlanMap,
} from "../network";
import { modelShort } from "../modelLabel";
import {
  lldpByPort,
  neighborTooltip,
  portKey,
} from "../lldp";
import { PortSquare, ProtocolPills, StatusDot, VlanLegend } from "../netui";
import i18n from "../i18n";

/**
 * The management addressing mode, in two words for a dense list.
 *
 * Renders an EMPTY string when the mode isn't known, and this is deliberate:
 * the location line then settles for the location. Writing "unknown mode" on
 * every card of a fleet not yet reread would fill the inventory with
 * information that says nothing about the device — only about what meshcraft
 * hasn't done yet. The device sheet, on the other hand, says so, along with
 * the action that fixes it.
 */
function addrModeShort(d: Device): string {
  const eff = d.mgmt_addressing?.effective;
  if (eff === "static") return i18n.t("equipements.fixedIp");
  if (eff === "dhcp") return "DHCP";
  return "";
}

/**
 * Fleet inventory: one card per device, port facade colored by VLAN. Alerts
 * live on the Dashboard, history on Monitoring.
 */
export default function Equipements() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const navigate = useNavigate();
  const role = useSiteRole(siteId);
  const [hoverVlan, setHoverVlan] = useState<number | null>(null);
  const [toast] = useToast();
  // Real-time: the controller announces site changes, react-query
  // invalidates. `false` as long as the stream holds — i.e. no more periodic
  // polling; the fallback cadence returns if the stream drops.
  const refetchInterval = useSiteLive(siteId);

  const { data: devices, error } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
    refetchInterval,
  });
  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });
  /**
   * The site's OBSERVED LLDP neighborhood, for port square HOVER.
   *
   * A separate query rather than a `devices` field: a neighbor is a dated
   * observation, not a device attribute, and loading it with the inventory
   * would make the facade display depend on a collection that can be
   * missing. Here, if the query fails, port squares render exactly as
   * before — without a neighbor tooltip. That's the degradation wanted: the
   * neighborhood is a BONUS, never a condition to see one's fleet.
   */
  const { data: lldp } = useQuery({
    queryKey: ["lldp", siteId],
    queryFn: () => get<SiteLldp>(`/api/sites/${siteId}/lldp`),
    enabled: !!siteId,
    refetchInterval,
  });
  const vm = vlanMap(vlans);
  const neighborsByPort = lldpByPort(lldp?.neighbors);
  const deviceById = new Map((devices ?? []).map((d) => [d.id, d]));

  const countOf = (vid: number) => {
    let n = 0;
    (devices ?? []).forEach((d) =>
      expandPorts(d).forEach((pr) => {
        if (pr.p.mode && pr.p.mode !== "libre" && serves(pr.p, vid)) n++;
      }),
    );
    return n;
  };

  let activeTotal = 0;
  (devices ?? []).forEach((d) => {
    if (d.status !== "off") activeTotal += deviceRates(d).up;
  });

  return (
    <div className="page2col">
      <main>
        <div className="toolbar">
          <h1>
            {t("nav.equipment")}
            <span className="sub">
              {t("equipements.subtitle", { devices: (devices ?? []).length, ports: activeTotal, vlans: (vlans ?? []).length })}
            </span>
          </h1>
          <div className="row">
            {/* Site import/export lives in Configuration → Import / Export;
                adding equipment has its own section with a side menu. */}
            {role !== "viewer" && (
              <ActionButton kind="add" onClick={() => navigate(sitePath(siteSlug, "add", "discover"))}>
                {t("nav.addEquipment")}
              </ActionButton>
            )}
          </div>
        </div>
        <ErrorMsg error={error} />

        {(devices ?? []).length === 0 ? (
          <div className="card">
            <div className="empty">
              {t("equipements.none")}
              {role === "ing" ? ` ${t("equipements.noneIngHint")}` : ""}
            </div>
          </div>
        ) : (
          <div className="device-grid">
            {(devices ?? []).map((d) => {
              const off = d.status === "off";
              const r = deviceRates(d);
              return (
                <div
                  key={d.id}
                  className="card device-card"
                  role="button"
                  tabIndex={0}
                  onClick={() => navigate(sitePath(siteSlug, "equipment", d.name))}
                >
                  <div className="row" style={{ gap: 8 }}>
                    <StatusDot dev={d} />
                    <span style={{ fontWeight: 700, fontSize: 15 }}>{d.name}</span>
                    <ProtocolPills dev={d} />
                    <QuickTip text={d.model || undefined} className="quicktip-inline">
                      <span className="note" style={{ marginLeft: "auto" }}>
                        {modelShort(d.model, d.vendor)}
                      </span>
                    </QuickTip>
                  </div>
                  {/* Management addressing mode on the location line, in a
                      couple of words. This list is DENSE — one more line per
                      card would overflow it — but the information can't stay
                      reserved to the device sheet: "which devices in this
                      fleet have a fixed address?" is exactly the kind of
                      question asked in front of the inventory, not in front
                      of one sheet at a time. The exclamation mark flags a
                      divergence between what meshcraft wants and what the
                      device carries. */}
                  <div className="kicker">
                    {[d.location || "—", addrModeShort(d)].filter(Boolean).join(" · ")}
                    {d.mgmt_addressing?.drift ? (
                      <QuickTip
                        text={
                          d.mgmt_addressing.drift === "addr_plan_mismatch"
                            ? t("mgmtAddressing.planMismatch")
                            : d.mgmt_addressing.drift
                        }
                        className="quicktip-inline"
                      >
                        <span style={{ color: "var(--text-dim)", fontWeight: 700 }}>
                          {" !"}
                        </span>
                      </QuickTip>
                    ) : null}
                  </div>
                  <div style={{ display: "flex", flexWrap: "wrap", gap: 3 }}>
                    {expandPorts(d).map((pr) => (
                      <PortSquare
                        key={pr.name}
                        name={pr.name}
                        displayName={pr.display}
                        port={pr.p}
                        vm={vm}
                        size={13}
                        off={off}
                        hoverVlan={hoverVlan}
                        // Neighbor on HOVER only when LLDP saw someone. Absence
                        // of a tooltip is not "no cable" — platforms that do not
                        // speak LLDP (SwOS) stay silent on purpose; we do not
                        // stamp "LLDP ?" on the tile for that.
                        neighbor={neighborTooltip(
                          neighborsByPort.get(portKey(d.id, pr.name)),
                          deviceById,
                        )}
                      />
                    ))}
                  </div>
                  <div className="device-card-foot">
                    <span>
                      {t("equipements.portsFraction", { up: r.up, total: r.total })}
                    </span>
                    <span>
                      {off
                        ? t("equipements.lastKnownState")
                        : "↓ " +
                          fmtMbps(r.rx) +
                          (d.poe_budget_w ? " · PoE " + Math.round(r.poe) + " W" : "")}
                    </span>
                    <QuickTip
                      // State duration as a tooltip rather than in full: this
                      // list is dense, and "unreachable for 3 d" would push
                      // the line. On hover, it answers the only question the
                      // label leaves open — since when.
                      text={reachability(d).detail || undefined}
                      className="quicktip-inline"
                    >
                      <span
                        style={{
                          marginLeft: "auto",
                          color: statusColor(d.status),
                          fontWeight: 700,
                        }}
                      >
                        {statusLabel(d.status)}
                      </span>
                    </QuickTip>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </main>

      <aside className="side-panel">
        <VlanLegend
          vlans={vlans ?? []}
          countOf={countOf}
          note={t("equipements.hoverVlanNote") as string}
          onHover={setHoverVlan}
        />
      </aside>

      {toast}
    </div>
  );
}

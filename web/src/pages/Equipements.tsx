import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import {
  useSiteLive,
  get,
  type Device,
  type Profile,
  type SiteLldp,
  type Vlan,
} from "../api";
import { useSiteRole } from "../auth";
import { deviceDetailPath, sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import { ActionButton, ErrorMsg, QuickTip, useToast } from "../components";
import { PortHoverTip, type PortHover } from "../PortHoverTip";
import { claimHoverTip, releaseHoverTip } from "../hoverExclusive";
import {
  deviceRates,
  expandPorts,
  fmtMbps,
  portShortNum,
  reachability,
  serves,
  statusColor,
  statusLabel,
  vlanMap,
} from "../network";
import { modelOf, modelShort } from "../modelLabel";
import {
  lldpByPort,
  neighborTooltip,
  portKey,
} from "../lldp";
import { PortSquare, ProtocolPills, StatusDot, VlanLegend } from "../netui";
import { portConfigLines, profileNameMap } from "../portConfig";
import { useBreakpoint } from "../ui/breakpoint";
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
 * The square size the NARROW card needs so its labels fit.
 *
 * Port numbering is not one shape. An Aruba says "GE10" and shortens to
 * "10"; a stacked Cisco says "Gi1/0/10" and shortens to "1/0/10", which is
 * six characters and does not fit the width two digits need. Sizing every
 * card the same meant one of the two was always wrong, and the failure is
 * silent: `portLabelFontSize` shrinks until its floor, then `overflow:
 * hidden` clips, so "1/0/10" came out as "1/0/1" — a real port name, for a
 * different port.
 *
 * Measuring the widest label the device actually carries costs one pass and
 * removes the guess. The slashed form is kept rather than reduced to its
 * last segment because a stack has both "1/0/1" and "2/0/1".
 */
function cardPortSize(d: Device): number {
  let longest = 0;
  for (const pr of expandPorts(d)) {
    const n = portShortNum(pr.display).length;
    if (n > longest) longest = n;
  }
  if (longest <= 2) return 30;
  if (longest <= 4) return 38;
  return 46;
}

/**
 * Fleet inventory: one card per device, port facade colored by VLAN. Alerts
 * live on the Dashboard, history on Monitoring.
 */
export default function Equipements() {
  const { t } = useTranslation();
  const narrow = useBreakpoint() === "narrow";
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const navigate = useNavigate();
  const role = useSiteRole(siteId);
  const [hoverVlan, setHoverVlan] = useState<number | null>(null);
  // THE FLOATING PORT CHART. One state for the whole page: the tip is exclusive
  // (hoverExclusive), so two squares can never own it at once, and keying it by
  // device+port is what lets a mouse MOVE reposition without remounting the
  // telemetry query underneath.
  const [portHover, setPortHover] = useState<PortHover | null>(null);
  const portTipId = "equipment-port-tip";
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
  /**
   * The site's PORT-PROFILE vocabulary, for port square HOVER — the same
   * bargain as the LLDP query above: one site-scoped request, and if it never
   * arrives the squares render exactly as before, minus one tip line. A
   * profile is a site-level object, so this is one request for the whole
   * inventory however many devices it draws.
   */
  const { data: profiles } = useQuery({
    queryKey: ["profiles", "port", siteId],
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/port`),
    enabled: !!siteId,
  });
  const vm = vlanMap(vlans);
  // `undefined` until the list is actually here: an empty map would report
  // every port that follows a group as following a DELETED one (cf. the same
  // guard on the device page).
  const profileNames = profiles ? profileNameMap(profiles) : undefined;
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
        {/* The site's counts moved to the top of the side panel, and the "add
            equipment" button to its foot — the heading that held them both is
            gone, and with it this page's toolbar. Site import/export lives in
            Configuration → Import / Export; adding equipment has its own
            section with a side menu. */}
        <h1>{t("nav.equipment")}</h1>
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
                  onClick={() => navigate(deviceDetailPath(siteSlug, d))}
                >
                  <div className="row" style={{ gap: 8 }}>
                    <StatusDot dev={d} />
                    <span style={{ fontWeight: 700, fontSize: 15 }}>{d.name}</span>
                    <ProtocolPills dev={d} />
                    <QuickTip text={d.model || undefined} className="quicktip-inline">
                      <span className="note" style={{ marginLeft: "auto" }}>
                        {modelShort(modelOf(d), d.vendor)}
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
                  {/* THE CARD FACADE, AND WHAT IT BECOMES ON A PHONE.
                      Wide: every port at 13px — a density glyph, too small
                      to carry a number (`showNum` in netui.tsx needs 24px),
                      and that is fine because the block is read as a SHAPE.
                      Narrow: the shape does not survive wrapping onto four
                      lines of a 393px card, so the squares grow to 30px,
                      stop wrapping, and scroll — the same gesture and the
                      same physical order as the device page's facade.

                      The label drops to the NUMBER ALONE (`portShortNum`).
                      "GE48" does not fit in 30px and came out clipped to
                      "GE4C", which is not a port name — a truncated
                      identifier is worse than none, because it reads as a
                      real one. The device is named three lines above, so
                      the prefix carries nothing here anyway. */}
                  <div className={narrow ? "card-ports card-ports--mobile scroll-x scroll-cue" : "card-ports"}>
                    {expandPorts(d).map((pr) => (
                      <PortSquare
                        key={pr.name}
                        name={pr.name}
                        displayName={narrow ? portShortNum(pr.display) : pr.display}
                        port={pr.p}
                        vm={vm}
                        profileNames={profileNames}
                        size={narrow ? cardPortSize(d) : 13}
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
                        onHover={(e) => {
                          claimHoverTip(portTipId);
                          setPortHover({
                            key: d.id + "/" + pr.name,
                            deviceId: d.id,
                            port: pr.name,
                            title: d.name + " · " + pr.name,
                            // Same lines as the square's own tip, and the only
                            // copy of them that survives on this page: the
                            // floating tip claims the hover slot and dismisses
                            // the bubble (hoverExclusive).
                            facts: portConfigLines(pr.p, vm, profileNames, t),
                            subtitle: t("planPage.linkTraffic24h") as string,
                            classCaps: d.traffic_class_caps,
                            clientX: e.clientX,
                            clientY: e.clientY,
                          });
                        }}
                        onHoverMove={(e) =>
                          setPortHover((cur) =>
                            cur && cur.key === d.id + "/" + pr.name
                              ? { ...cur, clientX: e.clientX, clientY: e.clientY }
                              : cur,
                          )
                        }
                        onHoverLeave={() =>
                          setPortHover((cur) => {
                            if (cur?.key !== d.id + "/" + pr.name) return cur;
                            releaseHoverTip(portTipId);
                            return null;
                          })
                        }
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

      <aside className="side-panel side-panel--footed">
        {/* The site in three numbers, above the VLAN plan. It used to be the
            page heading's subtitle; it belongs with the panel that breaks the
            third of those numbers down. */}
        <div className="note side-panel-summary">
          {t("equipements.subtitle", { devices: (devices ?? []).length, ports: activeTotal, vlans: (vlans ?? []).length })}
        </div>
        {/* `embedded`: no "VLAN plan" kicker and no hover hint — the list of
            coloured VLAN rows under a count of VLANs needs neither. */}
        <VlanLegend
          vlans={vlans ?? []}
          countOf={countOf}
          onHover={setHoverVlan}
          embedded
        />
        {/* The page's primary gesture, and the only one here that creates
            anything — so it gets the whole width of the band it sits in
            rather than the width of its own label (styles.css,
            `.side-panel-foot`). Sticky at the panel's bottom edge on a wide
            screen, where the panel is its own scroller and a long VLAN plan
            would otherwise push it out of sight; plain flow once the panel
            stacks under the device grid, because there the page is the
            scroller and sticking to the viewport would park it over the
            content and under the glass tab bar. */}
        {role !== "viewer" && (
          <div className="side-panel-foot">
            <ActionButton kind="add" onClick={() => navigate(sitePath(siteSlug, "add", "discover"))}>
              {t("nav.addEquipment")}
            </ActionButton>
          </div>
        )}
      </aside>

      <PortHoverTip hover={portHover} />
      {toast}
    </div>
  );
}

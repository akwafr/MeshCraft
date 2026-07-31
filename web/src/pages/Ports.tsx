import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import {
  useSiteLive,
  get,
  put,
  type Device,
  type DeviceLink,
  type PortIntent,
  type SiteLldp,
  type Vlan,
} from "../api";
import { useSiteRole } from "../auth";
import { ErrorMsg, HelpTip, QuickTip, Switch, useToast } from "../components";
import {
  decimalSeparator,
  expandPorts,
  fmtMbps,
  nativeVlan,
  statusColor,
  vlanMap,
  type VlanMap,
} from "../network";
import { modelShort } from "../modelLabel";
import {
  lldpByPort,
  neighborName,
  neighborPort,
  neighborTooltip,
} from "../lldp";
import { PortSquare } from "../netui";

/** Neighbor of a port, as displayed by the "Neighbor" column. */
interface Neighbor {
  device: string;
  /** Empty when the neighbor isn't a meshcraft device: nothing to link to. */
  deviceId: string;
  port: string;
  speed: string;
  status: string;
  note: string;
  /**
   * Where this neighbor comes from.
   *
   *   "lldp"     — an OBSERVATION: the device saw it, with its date;
   *   "declared" — an ENTRY: someone drew this link in meshcraft.
   *
   * The two look alike in a cell and aren't worth the same. An observation
   * proves a frame went through; an entry proves it was believed.
   */
  origin: "lldp" | "declared";
  /** The observation's tooltip: multiple neighbors, matching path, date. */
  tooltip?: string;
}

export default function Ports() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [onlyActive, setOnlyActive] = useState(false);
  const [toast, showToast] = useToast();
  // Real-time: this is where a link is watched going up or down. Waiting for
  // the next polling cycle to find out is precisely what this removes.
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
  const { data: links } = useQuery({
    queryKey: ["links", siteId],
    queryFn: () => get<DeviceLink[]>(`/api/sites/${siteId}/links`),
    enabled: !!siteId,
  });
  // The OBSERVED neighborhood. A query separate from the entered links,
  // because these are two natures of fact with different freshness: an LLDP
  // snapshot is reissued every 60 s, an entered link only changes if someone
  // draws it.
  const { data: lldp } = useQuery({
    queryKey: ["lldp", siteId],
    queryFn: () => get<SiteLldp>(`/api/sites/${siteId}/lldp`),
    enabled: !!siteId,
    refetchInterval,
  });
  const vm = vlanMap(vlans);

  const setPort = useMutation({
    mutationFn: (a: { deviceId: string; port: string; intent: PortIntent }) =>
      put<Device>(`/api/devices/${a.deviceId}/ports/${a.port}`, a.intent),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["devices", siteId] }),
  });

  // NEIGHBORHOOD: OBSERVATION FIRST, ENTRY AS FALLBACK.
  //
  // This column used to show only ENTERED links, even though LLDP reporting
  // exists: on a switch speaking 802.1AB, the most natural cell to look for
  // "what's on the other end?" showed a dash. The OBSERVED neighbor therefore
  // wins, and the entry remains the fallback — it covers what LLDP can't see:
  // a platform that doesn't speak the protocol (SwOS), a silent neighbor, a
  // port where LLDP is off.
  //
  // The two are NOT merged into a single appearance: the cell says which of
  // the two it shows. An observation proves a frame went through, an entry
  // proves it was believed — and it's precisely when the two contradict each
  // other that this needs to be visible.
  const byId = new Map((devices ?? []).map((d) => [d.id, d]));
  const neighbors = new Map<string, Neighbor>();
  (links ?? []).forEach((l) => {
    const a = byId.get(l.a_device);
    const b = byId.get(l.b_device);
    if (!a || !b) return;
    neighbors.set(`${a.id}:${l.a_port}`, {
      device: b.name, deviceId: b.id, port: l.b_port,
      speed: l.speed, status: l.status, note: l.note, origin: "declared",
    });
    neighbors.set(`${b.id}:${l.b_port}`, {
      device: a.name, deviceId: a.id, port: l.a_port,
      speed: l.speed, status: l.status, note: l.note, origin: "declared",
    });
  });
  // Observations overwrite entries, port by port. Several neighbors on the
  // same port — measured in the field, a transparent switch flooding frames
  // — are summarized by the first one and counted in the tooltip: a table
  // cell can't carry three, but it must not imply there's only one.
  lldpByPort(lldp?.neighbors).forEach((list, key) => {
    const n = list[0];
    const peer = n.peer_device ? byId.get(n.peer_device) : undefined;
    neighbors.set(key, {
      device: neighborName(n, byId) + (list.length > 1 ? ` +${list.length - 1}` : ""),
      deviceId: peer?.id ?? "",
      port: neighborPort(n),
      speed: "",
      status: "",
      note: "",
      origin: "lldp",
      tooltip: neighborTooltip(list, byId) ?? undefined,
    });
  });

  const needle = q.trim().toLowerCase();
  let shown = 0;
  let total = 0;

  const groups = (devices ?? []).map((d) => {
    const rows = expandPorts(d)
      .map((pr, pi) => ({ pr, pi }))
      .filter(({ pr }) => {
        total++;
        if (onlyActive && !pr.p.link) return false;
        if (!needle) return true;
        const nb = neighbors.get(`${d.id}:${pr.name}`);
        const vlanNames = [
          pr.p.pvid,
          ...(Array.isArray(pr.p.tagged) ? pr.p.tagged : []),
        ]
          .map((id) => (id != null ? vm.get(id)?.name : undefined))
          .filter(Boolean)
          .join(" ");
        return [pr.name, pr.p.label, d.name, vlanNames, nb?.device, nb?.port]
          .filter(Boolean)
          .join(" ")
          .toLowerCase()
          .includes(needle);
      });
    shown += rows.length;
    return { d, rows };
  });

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("nav.ports")}
          <HelpTip label={t("portsPage.helpLabel") as string}>
            <p>{t("portsPage.help1")}</p>
            <p>{t("portsPage.help2")}</p>
            <p>{t("portsPage.help3")}</p>
          </HelpTip>
          <span className="sub">{t("portsPage.shownOfTotal", { shown, total })}</span>
        </h1>
        <div className="row">
          <input
            style={{ width: 260 }}
            value={q}
            placeholder={t("portsPage.searchPlaceholder") as string}
            onChange={(e) => setQ(e.target.value)}
          />
          <div className={"plan-toggle-btn" + (onlyActive ? " on" : "")}>
            <Switch
              checked={onlyActive}
              onChange={setOnlyActive}
              label={t("portsPage.activeOnly")}
            />
          </div>
        </div>
      </div>
      <ErrorMsg error={error} />
      <ErrorMsg error={setPort.error} />

      {groups.map(({ d, rows }) => {
        if (rows.length === 0) return null;
        const off = d.status === "off";
        return (
          <div className="card" key={d.id}>
            <div className="card-head">
              <div className="row" style={{ gap: 8 }}>
                <span className="dot" style={{ background: statusColor(d.status) }} />
                <Link to={sitePath(siteSlug, "equipment", d.name)} style={{ fontWeight: 700, fontSize: 14 }}>
                  {d.name}
                </Link>
                <QuickTip text={d.model || undefined} className="quicktip-inline">
                  <span className="note">
                    {modelShort(d.model, d.vendor)}
                  </span>
                </QuickTip>
                {d.location && <span className="chip">{d.location}</span>}
              </div>
              <span className="note">{t("portsPage.portsCount", { count: rows.length })}</span>
            </div>
            <div style={{ overflowX: "auto" }}>
              <table className="ports-table">
                <thead>
                  <tr>
                    <th style={{ width: 40 }}></th>
                    <th style={{ width: 118 }}>{t("nav.ports")}</th>
                    <th>{t("common.description")}</th>
                    <th style={{ width: 190 }}>VLAN</th>
                    <th style={{ width: 96 }}>{t("portsPage.linkCol")}</th>
                    <th style={{ width: 150 }}>{t("portsPage.trafficCol")}</th>
                    <th style={{ width: 78 }}>PoE</th>
                    <th style={{ width: 210 }}>{t("portsPage.neighborCol")}</th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map(({ pr }) => {
                    const p = pr.p;
                    const nb = neighbors.get(`${d.id}:${pr.name}`);
                    const rx = off || !p.link ? 0 : (p.rx_mbps ?? 0);
                    const tx = off || !p.link ? 0 : (p.tx_mbps ?? 0);
                    return (
                      <tr key={pr.name}>
                        <td>
                          <PortSquare
                            name={pr.name} port={p} vm={vm} size={18} off={off}
                          />
                        </td>
                        <td>
                          <span style={{ fontWeight: 600, fontSize: 12 }}>{pr.name}</span>
                          <div className="note" style={{ fontSize: 10 }}>
                            {pr.speed}
                            {pr.poeCap ? " · PoE" : ""}
                          </div>
                        </td>
                        <td>
                          {role === "viewer" || off ? (
                            <span style={{ fontSize: 12 }}>{p.label || <span className="note">—</span>}</span>
                          ) : (
                            <input
                              key={d.id + pr.name + (p.label ?? "")}
                              className="desc-input"
                              defaultValue={p.label ?? ""}
                              placeholder={t("portsPage.describePlaceholder") as string}
                              onKeyDown={(e) => {
                                if (e.key === "Enter") (e.target as HTMLInputElement).blur();
                              }}
                              onBlur={(e) => {
                                const v = e.target.value;
                                if (v === (p.label ?? "")) return;
                                setPort.mutate(
                                  { deviceId: d.id, port: pr.name, intent: { ...p, label: v } },
                                  { onSuccess: () => showToast(t("portsPage.descriptionSaved", { device: d.name, port: pr.name })) },
                                );
                              }}
                            />
                          )}
                        </td>
                        <td>
                          <VlanCell port={p} vm={vm} />
                        </td>
                        <td>
                          {off ? (
                            <span className="note">{t("common.unknown")}</span>
                          ) : p.link ? (
                            <span style={{ fontSize: 11, color: "var(--ok)", fontWeight: 600 }}>
                              {p.link}
                            </span>
                          ) : (
                            <span className="note" style={{ fontSize: 11 }}>down</span>
                          )}
                        </td>
                        <td className="note" style={{ whiteSpace: "nowrap", fontSize: 11 }}>
                          {p.link && !off ? `${fmtMbps(rx)} / ${fmtMbps(tx)}` : "—"}
                        </td>
                        <td className="note" style={{ fontSize: 11 }}>
                          {!pr.poeCap
                            ? "—"
                            : (p.poe_w ?? 0) > 0
                              ? String(p.poe_w).replace(".", decimalSeparator()) + " W"
                              : p.poe
                                ? t("portsPage.poeEnabled")
                                : t("portsPage.poeOff")}
                        </td>
                        <td>
                          <QuickTip text={nb?.tooltip} className="quicktip-block">
                          {nb ? (
                            <>
                              {nb.deviceId ? (
                                <Link
                                  to={sitePath(
                                    siteSlug,
                                    "equipment",
                                    byId.get(nb.deviceId)?.name ?? nb.device,
                                  )}
                                  style={{ fontSize: 12 }}
                                >
                                  {nb.device}
                                </Link>
                              ) : (
                                // A neighbor not in meshcraft: no link to
                                // follow, but its name is worth reading —
                                // it's what the device sees facing it.
                                <span style={{ fontSize: 12 }}>{nb.device}</span>
                              )}
                              <div className="note" style={{ fontSize: 10 }}>
                                {nb.port}
                                {nb.speed ? " · " + nb.speed : ""}
                                {nb.status && nb.status !== "up" ? " · " + nb.status : ""}
                                {/* The ORIGIN, always stated. "LLDP" is a
                                    dated observation, "entered" a human
                                    declaration: the same cell carries both,
                                    and confusing them would make an intent
                                    pass for a measurement. */}
                                {nb.origin === "lldp" ? " · LLDP" : ` · ${t("portsPage.entered")}`}
                              </div>
                            </>
                          ) : (
                            <span className="note">—</span>
                          )}
                          </QuickTip>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        );
      })}

      {shown === 0 && (
        <div className="card">
          <div className="empty">
            {total === 0
              ? t("equipements.none")
              : t("portsPage.noPortMatch")}
          </div>
        </div>
      )}

      {toast}
    </>
  );
}

function VlanCell({ port, vm }: { port: PortIntent; vm: VlanMap }) {
  const { t } = useTranslation();
  if (!port.mode || port.mode === "libre")
    return <span className="note" style={{ fontSize: 11 }}>{t("portsPage.free")}</span>;
  if (port.mode === "access") {
    const v = port.pvid != null ? vm.get(port.pvid) : undefined;
    return (
      <span className="row" style={{ gap: 6, fontSize: 11 }}>
        <span className="vlan-chip" style={{ background: v?.color ?? "#888", margin: 0 }} />
        {v ? `${v.name} (${v.vlan_id})` : `VLAN ${port.pvid ?? "?"}`}
      </span>
    );
  }
  const tagged = port.tagged === "all" ? [] : ((port.tagged ?? []) as number[]);
  const natId = nativeVlan(port);
  const nat = natId != null ? vm.get(natId) : undefined;
  return (
    <div>
      <div className="row" style={{ gap: 4, fontSize: 10 }}>
        <span style={{ fontWeight: 700, letterSpacing: ".04em" }}>
          {port.tagged === "all" ? "TRUNK ▤" : "TRUNK"}
        </span>
        {tagged.slice(0, 6).map((id) => (
          <QuickTip
            key={id}
            text={vm.get(id)?.name ?? String(id)}
            className="quicktip-inline"
          >
            <span
              className="vlan-chip"
              style={{ background: vm.get(id)?.color ?? "#888", margin: 0, width: 10, height: 10 }}
            />
          </QuickTip>
        ))}
        {tagged.length > 6 && <span className="note">+{tagged.length - 6}</span>}
      </div>
      {nat && (
        <div className="note" style={{ fontSize: 10 }}>
          {t("portsPage.nativeVlan", { name: nat.name, id: nat.vlan_id })}
        </div>
      )}
    </div>
  );
}

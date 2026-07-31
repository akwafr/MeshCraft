// Shared network rendering components: port square, VLAN legend.
import { useTranslation } from "react-i18next";
import type { PortIntent, Vlan } from "./api";
import { QuickTip } from "./components";
import { deviceDot, portShortNum, portVisual, type VlanMap } from "./network";

export function PortSquare({
  name,
  port,
  vm,
  size,
  off,
  hoverVlan,
  selected,
  drift,
  neighbor,
  onClick,
}: {
  name: string;
  port: PortIntent;
  vm: VlanMap;
  size: number;
  off?: boolean;
  hoverVlan?: number | null;
  selected?: boolean;
  /**
   * Gap between what meshcraft knows and what the device carries.
   *
   *   "pending" — meshcraft has a configuration the device doesn't have yet
   *               (blue): this is normal, it will pass;
   *   "drift"   — the device was modified outside meshcraft (orange): it
   *               needs arbitration.
   *
   * Blinking applies ONLY to the ports actually concerned. Blinking the
   * whole facade for one differing port would make it a long search.
   */
  drift?: "pending" | "drift" | null;
  /**
   * OBSERVED LLDP neighbor on this port, already phrased by the caller
   * (`neighborTooltip`, web/src/lldp.ts). Added to the tooltip, never to the
   * visual.
   *
   * `undefined` means "nothing to add", and EMPHATICALLY NOT "no neighbor":
   * the absence of an observation has three ordinary causes — the device on
   * the other end doesn't speak LLDP, this one only runs it on certain
   * interfaces, or no snapshot has arrived yet. This component therefore
   * never says "no neighbor" on its own; it's up to the screen, which knows
   * which of the three applies, to do so (see lldpSilencePhrase).
   *
   * A STRING and not the raw data: this component has no reason to know the
   * shape of an LLDP observation, and passing it an array of neighbors would
   * have made it responsible for formatting that must stay testable outside
   * React.
   */
  neighbor?: string | null;
  /** Receives the event: ⇧ and Ctrl/⌘ distinguish range, add and replace. */
  onClick?: (e: React.MouseEvent) => void;
}) {
  const { t } = useTranslation();
  const v = portVisual(port, vm, { size, off, hoverVlan });
  const showNum = size >= 24;
  const style: React.CSSProperties = {
    position: "relative",
    width: size,
    height: Math.round(size * 0.92),
    flex: "none",
    background: v.background,
    border: `1px solid ${v.borderColor}`,
    borderRadius: 3,
    boxShadow: selected ? "0 0 0 2px var(--accent)" : "none",
    opacity: v.opacity,
    padding: 0,
    cursor: onClick ? "pointer" : "default",
    transition: "opacity .12s",
    display: "block",
    // Without this, a ⇧ click on the facade highlights the page's text
    // instead of selecting a range of ports.
    userSelect: "none",
  };
  // The title states first what the square SHOWS. "free" on a port carrying
  // a 10 G link was the same flaw as the gray background: an absent intent
  // taken for a dead jack.
  const state = port.link
    ? port.mode && port.mode !== "libre"
      ? " — " + port.link
      : " — " + port.link + ", " + t("netui.activeLinkUnmanaged")
    : " — " + t("netui.free");
  const tip =
    name +
    (port.label ? " — " + port.label : state) +
    (drift === "pending"
      ? "\n" + t("netui.driftPending")
      : drift === "drift"
        ? "\n" + t("netui.driftMismatch")
        : "") +
    // The neighbor LAST, and that's a deliberate order: the two lines above
    // describe what meshcraft WANTS on this port, this one describes what a
    // device SAW facing it. The separation mirrors the one between
    // `device_links` and `lldp_neighbors`, and the tooltip has no reason to
    // blur it.
    (neighbor ? "\n" + neighbor : "");
  const inner = (
    <>
      {showNum && (
        <span
          style={{
            position: "absolute", top: 1, left: 4,
            fontSize: 9, fontWeight: 700, color: v.ink, lineHeight: "12px",
          }}
        >
          {portShortNum(name)}
        </span>
      )}
      {v.poeDot && (
        <span
          style={{
            position: "absolute", right: 3, bottom: 3,
            width: 5, height: 5, borderRadius: 1, background: v.ink,
          }}
        />
      )}
    </>
  );
  // Custom tip (QuickTip) instead of native `title`: browser tooltips wait ~1 s
  // and feel broken on a dense port facade where LLDP lines matter immediately.
  const square = onClick ? (
    <button
      type="button"
      className={driftClass(drift)}
      style={style}
      onMouseDown={(e) => e.shiftKey && e.preventDefault()}
      onClick={onClick}
    >
      {inner}
    </button>
  ) : (
    <span className={driftClass(drift)} style={style}>
      {inner}
    </span>
  );
  return (
    <QuickTip text={tip} className="port-square-tip">
      {square}
    </QuickTip>
  );
}

/** Blink class, empty when the port is compliant. */
function driftClass(drift?: "pending" | "drift" | null): string | undefined {
  if (drift === "pending") return "port-pending";
  if (drift === "drift") return "port-drift";
  return undefined;
}

export function VlanLegend({
  vlans,
  countOf,
  note,
  onHover,
  onRowClick,
  embedded,
}: {
  vlans: Vlan[];
  countOf: (vid: number) => number;
  note?: string;
  onHover: (vid: number | null) => void;
  onRowClick?: (v: Vlan) => void;
  /** Title lives on the side-panel group when true. */
  embedded?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div>
      {!embedded && <div className="kicker">{t("netui.vlanPlan")}</div>}
      {note && <div className="note" style={{ margin: "2px 0 10px" }}>{note}</div>}
      <div>
        {vlans.map((v) => (
          <div
            key={v.id}
            onMouseEnter={() => onHover(v.vlan_id)}
            onMouseLeave={() => onHover(null)}
            onClick={onRowClick ? () => onRowClick(v) : undefined}
            style={{
              display: "flex", alignItems: "center", gap: 9,
              padding: "7px 2px", borderBottom: "1px solid var(--border)",
              cursor: onRowClick ? "pointer" : "default", fontSize: 12,
            }}
          >
            <span className="vlan-chip" style={{ background: v.color, margin: 0 }} />
            <span style={{ fontWeight: 600 }}>{v.name}</span>
            <span className="note">{v.vlan_id}</span>
            <span className="note" style={{ marginLeft: "auto" }}>
              {t("dashboard.portsCount", { count: countOf(v.vlan_id) })}
            </span>
          </div>
        ))}
        {vlans.length === 0 && <div className="empty">{t("netui.noVlanDefined")}</div>}
      </div>
    </div>
  );
}

/**
 * A device's status dot: reachability, and configuration drift.
 *
 * Shared by every screen that shows it. Copying the color rule into each
 * page would guarantee one screen eventually says green where another says
 * orange — and the operator would then believe the more reassuring screen.
 */
export function StatusDot({
  dev,
}: {
  dev: {
    status: string;
    sync?: string;
    apply_stalled?: boolean;
    last_apply?: { skipped?: string[]; error?: string };
  };
}) {
  const d = deviceDot(dev);
  return (
    <QuickTip text={d.title} className="quicktip-inline">
      <span
        className={"dot" + (d.blink ? " dot-" + d.blink : "")}
        style={{ background: d.color }}
      />
    </QuickTip>
  );
}

/**
 * Ping and SNMP reachability pills — two axes, never one light.
 *
 * ICMP (`status`) and SNMP collection (`snmp_status`) diverge in real fleets:
 * filtered ICMP with a live agent, or a dead agent behind a green ping. Green
 * = responding, red = down, muted = never observed yet.
 */
export function ProtocolPills({
  dev,
}: {
  dev: {
    status: string;
    status_note?: string;
    status_at?: string;
    snmp_status?: string;
    snmp_status_note?: string;
    snmp_status_at?: string;
    watch_excluded?: string;
  };
}) {
  const { t } = useTranslation();
  return (
    <span className="proto-pills">
      <ProtoPill
        label="ping"
        kind={protoKind(dev.status)}
        title={protoTitle("ping", dev.status, dev.status_note, t)}
      />
      <ProtoPill
        label="snmp"
        kind={dev.watch_excluded ? "unknown" : protoKind(dev.snmp_status ?? "unknown")}
        title={
          dev.watch_excluded
            ? dev.watch_excluded
            : protoTitle("snmp", dev.snmp_status ?? "unknown", dev.snmp_status_note, t)
        }
      />
    </span>
  );
}

function protoKind(status: string): "up" | "down" | "unknown" {
  if (status === "ok") return "up";
  if (status === "off") return "down";
  return "unknown";
}

function protoTitle(
  proto: string,
  status: string,
  note: string | undefined,
  t: (k: string, o?: Record<string, unknown>) => string,
): string {
  const state =
    status === "ok"
      ? t("status.protoUp")
      : status === "off"
        ? t("status.protoDown")
        : t("status.protoUnknown");
  const parts = [proto.toUpperCase(), state];
  if (note) parts.push(note);
  return parts.join(" — ");
}

function ProtoPill({
  label,
  kind,
  title,
}: {
  label: string;
  kind: "up" | "down" | "unknown";
  title: string;
}) {
  return (
    <QuickTip text={title} className="quicktip-inline">
      <span className={"badge proto-" + kind}>
        {label}
      </span>
    </QuickTip>
  );
}

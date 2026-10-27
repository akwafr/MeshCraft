// Shared network rendering components: port square, VLAN legend.
import { useTranslation } from "react-i18next";
import type { PortIntent, Vlan } from "./api";
import { QuickTip } from "./components";
import { deviceDot, portShortNum, portVisual, type VlanMap } from "./network";
import { portConfigLines, type ProfileNameMap } from "./portConfig";
import { portLabelFontSize } from "./portLabel";

export function PortSquare({
  name,
  displayName,
  port,
  vm,
  profileNames,
  size,
  off,
  sfp,
  hoverVlan,
  selected,
  emphasis,
  drift,
  neighbor,
  lag,
  onClick,
  onHover,
  onHoverMove,
  onHoverLeave,
}: {
  /** Standard-model name — what is sent to the API when this square is clicked. */
  name: string;
  /**
   * What the operator reads: the hardware's silkscreen number when the
   * platform prints one different from the standard name (FS S3900 "1/11" vs
   * the standard "GE11"). Falls back to a digit-preserving read of `name`
   * when the caller has none (older callers, or a platform whose standard
   * name already matches its faceplate).
   */
  displayName?: string;
  port: PortIntent;
  vm: VlanMap;
  /**
   * The site's PORT-PROFILE vocabulary, id → name (`profileNameMap`), so the
   * tip can name the group this jack follows instead of its UUID.
   *
   * OPTIONAL, and its absence is a third state rather than "no profile":
   * `undefined` means this screen never fetched the site's profiles, and the
   * tip then says nothing about profiles at all. A screen that DOES pass a map
   * gets the full answer, the empty one included — a port carrying no
   * `profile_id` is told it follows none. See `portConfigLines` for why that
   * distinction is kept and why "default" is never printed.
   */
  profileNames?: ProfileNameMap;
  size: number;
  off?: boolean;
  /**
   * SFP/SFP+ cage: drawn square-cornered, like the metal cage it is, where a
   * copper jack keeps its rounded corners. Shape and not color — every color
   * on a port cell already carries a meaning (VLAN, drift, fault).
   */
  sfp?: boolean;
  hoverVlan?: number | null;
  selected?: boolean;
  /**
   * SINGLED OUT WITHOUT BEING SELECTED: this port is a member of the
   * aggregate the operator just clicked (ADR-0007, decision 1).
   *
   * A third visual state, and it has to be a third: `selected` means "the
   * next port write lands here", and the members of a selected aggregate are
   * emphatically NOT that — clicking LAG2 must not arm a VLAN change on its
   * four members. Drawn in the AGGREGATE's colour rather than the accent, so
   * the emphasis and the overline that caused it are visibly the same fact.
   */
  emphasis?: boolean;
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
  drift?: "pending" | "drift" | "applying" | null;
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
  /**
   * LINK-AGGREGATION membership: an overline in the aggregate's colour across
   * the top of the square, the same colour on every member of the same LAG.
   * An OVERLINE and not a fill or border, deliberately: every fill already
   * means a VLAN, every border a fault or a cage, and the aggregate is an
   * orthogonal fact that must combine with all of them.
   */
  lag?: { name: string; color: string } | null;
  /** Receives the event: ⇧ and Ctrl/⌘ distinguish range, add and replace. */
  onClick?: (e: React.MouseEvent) => void;
  /**
   * THE FLOATING CHART, opt-in per screen.
   *
   * These three carry the pointer to whoever draws [PortHoverTip] — bandwidth
   * and, on a cage with a module, the optical levels. They are optional because
   * this component is drawn on screens that have no telemetry to show (an
   * inventory preview, the catalogue) and a hover that fetched a device's series
   * there would be a request per square for a chart nobody asked for.
   *
   * The text tip below stays either way: it answers "which port is this, and
   * what is on it" instantly, which a chart cannot.
   */
  onHover?: (e: React.MouseEvent) => void;
  onHoverMove?: (e: React.MouseEvent) => void;
  onHoverLeave?: () => void;
}) {
  const { t } = useTranslation();
  const v = portVisual(port, vm, { size, off, hoverVlan });
  // A port the SWITCH shut itself: red frame and a blinking fill, so it draws the
  // eye on a dense facade. Distinct from drift (orange), which is a configuration
  // gap and not a fault.
  const errDisabled = port.link_state === "err-disabled";
  const showNum = size >= 24;
  const shown = displayName ?? portShortNum(name);
  const style: React.CSSProperties = {
    position: "relative",
    width: size,
    height: Math.round(size * 0.92),
    flex: "none",
    background: v.background,
    border: errDisabled ? "2px solid var(--danger)" : `1px solid ${v.borderColor}`,
    borderRadius: sfp ? 0 : 3,
    // The ring says what the square is TO THE CURRENT SELECTION. Accent when
    // this port IS the selection; the aggregate's own colour when it is a
    // member of the selected aggregate — never both, and selection wins,
    // because a write is armed on the first and nothing is on the second.
    boxShadow: selected
      ? "0 0 0 2px var(--accent)"
      : emphasis && lag
        ? `0 0 0 2px ${lag.color}`
        : "none",
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
  // WHAT THE COLOUR MEANS, AND WHICH GROUP SET IT — right under the identity
  // line and ABOVE drift, which is the order that makes the two readable as
  // one thought: these lines state the configuration meshcraft records, and
  // the drift line below says when the device is not carrying it. Reversed,
  // "the device does not carry this" would arrive before "this".
  const config = portConfigLines(port, vm, profileNames, t);
  const tip =
    shown +
    (port.label ? " — " + port.label : state) +
    (config.length > 0 ? "\n" + config.join("\n") : "") +
    (drift === "applying"
      ? "\n" + t("netui.driftApplying")
      : drift === "pending"
        ? "\n" + t("netui.driftPending")
        : drift === "drift"
          ? "\n" + t("netui.driftMismatch")
          : "") +
    // The neighbor LAST, and that's a deliberate order: the two lines above
    // describe what meshcraft WANTS on this port, this one describes what a
    // device SAW facing it. The separation mirrors the one between
    // `device_links` and `lldp_neighbors`, and the tooltip has no reason to
    // blur it.
    (errDisabled
      ? "\n⚠ " + (port.err_reason || t("netui.errDisabled"))
      : "") +
    (lag ? "\n" + t("netui.lagMember", { name: lag.name }) : "") +
    (neighbor ? "\n" + neighbor : "");
  // The legend is bounded on BOTH sides and sized to what is left.
  //
  // It used to be `left: 4` at a fixed 9px with no right edge at all, which is
  // why a name carrying a unit ran past the border: absolutely positioned text
  // with only one horizontal anchor has no width to overflow, so nothing —
  // neither the box nor `overflow` — could hold it in. Anchoring `right` gives
  // the span a real width, and `portLabelFontSize` picks the size that fits in
  // it (see network.ts for why the name is shrunk rather than truncated).
  //
  // `overflow: hidden` is the backstop and not the mechanism: it only bites past
  // the shrink floor, on a name longer than any real faceplate carries, and
  // clipping there is still better than drawing on the next jack.
  const labelPad = 3;
  const labelSize = portLabelFontSize(shown, size - labelPad * 2);
  const inner = (
    <>
      {lag && (
        <span
          aria-hidden
          style={{
            position: "absolute", top: 0, left: 0, right: 0, height: 3,
            background: lag.color,
            // Above the fill, below the label: the overline must survive any
            // VLAN colour behind it and never cover the port number.
            zIndex: 1,
          }}
        />
      )}
      {showNum && (
        <span
          style={{
            position: "absolute", top: 1, left: labelPad, right: labelPad,
            fontSize: labelSize, fontWeight: 700, color: v.ink,
            // Tied to the font size rather than fixed at 12px: a shrunk label
            // kept on a 12px line sits low in its square and stops lining up
            // with the ones beside it.
            lineHeight: `${Math.round(labelSize * 1.35)}px`,
            whiteSpace: "nowrap", overflow: "hidden",
          }}
        >
          {shown}
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
      className={driftClass(drift) + (errDisabled ? " port-errdisabled" : "")}
      style={style}
      onMouseDown={(e) => e.shiftKey && e.preventDefault()}
      onClick={onClick}
      onMouseEnter={onHover}
      onMouseMove={onHoverMove}
      onMouseLeave={onHoverLeave}
    >
      {inner}
    </button>
  ) : (
    <span
      className={driftClass(drift) + (errDisabled ? " port-errdisabled" : "")}
      style={style}
      onMouseEnter={onHover}
      onMouseMove={onHoverMove}
      onMouseLeave={onHoverLeave}
    >
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
function driftClass(drift?: "pending" | "drift" | "applying" | null): string | undefined {
  if (drift === "applying") return "port-applying";
  if (drift === "pending") return "port-pending";
  if (drift === "drift") return "port-drift";
  return undefined;
}

export function VlanLegend({
  vlans,
  countOf,
  onHover,
  onRowClick,
  embedded,
}: {
  vlans: Vlan[];
  countOf: (vid: number) => number;
  onHover: (vid: number | null) => void;
  onRowClick?: (v: Vlan) => void;
  /** The caller titles the block itself — or deliberately does not. */
  embedded?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div>
      {!embedded && <div className="kicker">{t("netui.vlanPlan")}</div>}
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
    last_apply?: { skipped?: string[]; not_applied?: { kind: string }[]; error?: string };
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
 * = responding, red = down, YELLOW = nobody can see it right now, muted =
 * never observed yet.
 *
 * The yellow is the 2026-08-08 general rule for `--indeterminate` (UI-GUIDE
 * §2): "we cannot see" is a colour wherever it occurs, not only on the device
 * dot. Before it, this pill painted BOTH unseeable states muted — the very two
 * states the palette is built to keep apart. Both dates are already in the
 * props, so the pill can make the call `statusColor` cannot.
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
    /**
     * Which protocol collects this device — "snmp" or "gnmi", computed
     * server-side from the connector's declared channels.
     *
     * The STATUS below is protocol-agnostic on purpose (the proxy's gNMI
     * supervisor reuses the same up/down events, so "is this device collected"
     * has one answer), but the LABEL must name what was actually tried. A pill
     * reading "snmp" on an Arista is false: that platform declares no SNMP
     * channel and nothing in this product has ever polled it.
     */
    collect_kind?: string;
  };
}) {
  const { t } = useTranslation();
  // Falls back to "snmp", which is what every platform but one is collected by
  // and what the pill said before this field existed. An unknown connector
  // yields empty server-side, and labelling that "snmp" is the same claim the
  // rest of the product already makes about a device it cannot identify.
  const collect = dev.collect_kind || "snmp";
  return (
    <span className="proto-pills">
      <ProtoPill
        label="ping"
        kind={protoKind(dev.status, dev.status_at)}
        title={protoTitle("ping", dev.status, dev.status_note, t, dev.status_at)}
      />
      <ProtoPill
        label={collect}
        kind={
          // A watch exclusion is "NO proxy monitors this device, and here is
          // why" (`api.ts`), decided by the controller because no connected
          // proxy can reach it. That is a missing vantage point, not a missing
          // look, so it is yellow whatever the last collection said — and the
          // reason travels in the tooltip.
          dev.watch_excluded
            ? "blind"
            : protoKind(dev.snmp_status ?? "unknown", dev.snmp_status_at)
        }
        title={
          dev.watch_excluded
            ? dev.watch_excluded
            : protoTitle(
                collect,
                dev.snmp_status ?? "unknown",
                dev.snmp_status_note,
                t,
                dev.snmp_status_at,
              )
        }
      />
    </span>
  );
}

/**
 * Four states, because "we cannot see it" and "we never looked" are two facts.
 *
 * `at` is what separates them, exactly as it does for the device dot
 * ([neverObserved] / [blindNow] in `network.ts`): `unknown` WITH a date is an
 * observation that has stopped arriving; without one, nothing was ever claimed.
 *
 * The split is made on `unknown` ALONE and not on "anything that is not ok or
 * off". `status` also carries `warn`, which means the device ANSWERED and
 * reported a problem — the pill has no colour for that and falls through to
 * muted as it always has, but it must not fall through to yellow: "nobody can
 * see it" is exactly the wrong thing to say about a device that just replied.
 */
function protoKind(status: string, at?: string): "up" | "down" | "blind" | "unknown" {
  if (status === "ok") return "up";
  if (status === "off") return "down";
  if (status === "unknown") return at ? "blind" : "unknown";
  return "unknown";
}

function protoTitle(
  proto: string,
  status: string,
  note: string | undefined,
  t: (k: string, o?: Record<string, unknown>) => string,
  at?: string,
): string {
  // The sentence follows the pill's colour, and for the same reason: telling a
  // yellow pill "not observed yet" would put the two unseeable states back in
  // one word, which is the whole thing being avoided. Same `unknown`-only
  // guard as [protoKind], so the two cannot disagree.
  const state =
    status === "ok"
      ? t("status.protoUp")
      : status === "off"
        ? t("status.protoDown")
        : status === "unknown" && at
          ? t("status.protoBlind")
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
  kind: "up" | "down" | "blind" | "unknown";
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

import { useTranslation } from "react-i18next";
import { HelpTip } from "../../components";
import { UNMANAGED_UP, UNMANAGED_UP_BORDER, type VlanMap } from "../../network";
import type { ProfileNameMap } from "../../portConfig";
import { PortSquare } from "../../netui";
import { useEdgeFade } from "../../ui/useEdgeFade";
import type { FacadeBlock } from "./facade";
import { LagBlock } from "./LagBlock";

const PORT_SIZE = 38;

/**
 * One labelled block of the facade — "1–48", "SFP+" — and its sockets.
 *
 * THE ROWS ARE THE PHYSICAL ROWS OF THE BOX, AND THEY DO NOT WRAP. They used
 * to: a `flexWrap: "wrap"` on each row meant that on any viewport too narrow
 * for the whole block, port 17 moved under port 1 and the faceplate stopped
 * being a picture of the switch. On a phone a 48-port device rendered as six
 * rows of eight, interleaved odd and even — not a faceplate, a grid of
 * labels that happens to be the same colours as one. The operator counts
 * sockets from the left on the real box, and the drawing has to survive
 * that; range selection is measured on physical order too, so a wrapped row
 * made ⇧-click select a set that looked wrong on screen and was right.
 *
 * So the block scrolls sideways instead. It is its own component because
 * each block owns ONE scroller around BOTH its rows — one ref per block, and
 * `useEdgeFade` needs somewhere to put it. Scrolling the rows separately
 * would let the top and bottom row disagree about which port you are above.
 */
function FacadeBlockRows({
  block,
  vm,
  profileNames,
  off,
  hoverVlan,
  selected,
  emphasis,
  drift,
  lagOf,
  onPortClick,
  onPortHover,
  onPortHoverMove,
  onPortHoverLeave,
}: {
  block: FacadeBlock;
  vm: VlanMap;
  profileNames?: ProfileNameMap;
  off: boolean;
  hoverVlan: number | null;
  selected: Set<string>;
  emphasis?: Set<string>;
  drift: Map<string, "pending" | "drift" | "applying">;
  lagOf?: Map<string, { name: string; color: string }>;
  onPortClick: (
    name: string,
    e: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; detail?: number },
  ) => void;
  onPortHover?: (name: string, e: React.MouseEvent) => void;
  onPortHoverMove?: (name: string, e: React.MouseEvent) => void;
  onPortHoverLeave?: (name: string) => void;
}) {
  const fade = useEdgeFade<HTMLDivElement>();
  return (
    <div className="facade-block">
      <div className="kicker facade-label">{block.label}</div>
      <div className="facade-rows scroll-x scroll-x--faded" ref={fade.ref}>
        <div className="facade-rows-inner">
          {block.rows.map((row, ri) => (
            <div key={ri} className="facade-row">
              {row.map((pr) => (
                <PortSquare
                  key={pr.name}
                  name={pr.name}
                  displayName={pr.display}
                  port={pr.p}
                  vm={vm}
                  profileNames={profileNames}
                  size={PORT_SIZE}
                  off={off}
                  hoverVlan={hoverVlan}
                  selected={selected.has(pr.name)}
                  emphasis={emphasis?.has(pr.name)}
                  drift={drift.get(pr.name)}
                  lag={lagOf?.get(pr.name)}
                  onClick={(e) => onPortClick(pr.name, e)}
                  onHover={onPortHover && ((e) => onPortHover(pr.name, e))}
                  onHoverMove={onPortHoverMove && ((e) => onPortHoverMove(pr.name, e))}
                  onHoverLeave={onPortHoverLeave && (() => onPortHoverLeave(pr.name))}
                />
              ))}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

/**
 * THE PHYSICAL SOCKETS ALONE — every labelled block, no legend, no aggregates.
 *
 * Exported because the aggregate builder (ADR-0007, decision 2) darkens the
 * page and leaves exactly this: the physical ports, to pick members from. It
 * must be the SAME drawing as the page behind it — a second rendering of the
 * facade would eventually disagree with the first about which jack is a cage,
 * and the operator would be choosing members on a picture of a different
 * switch.
 */
export function FacadePorts(props: {
  blocks: FacadeBlock[];
  vm: VlanMap;
  /**
   * The site's port-profile vocabulary, so a square's tip can name the group
   * the jack follows. Optional, and its absence is NOT "no profile" — see
   * `PortSquare`.
   */
  profileNames?: ProfileNameMap;
  off: boolean;
  hoverVlan: number | null;
  selected: Set<string>;
  /** Ports to single out without selecting them — an aggregate's members. */
  emphasis?: Set<string>;
  drift: Map<string, "pending" | "drift" | "applying">;
  lagOf?: Map<string, { name: string; color: string }>;
  onPortClick: (
    name: string,
    e: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; detail?: number },
  ) => void;
  onPortHover?: (name: string, e: React.MouseEvent) => void;
  onPortHoverMove?: (name: string, e: React.MouseEvent) => void;
  onPortHoverLeave?: (name: string) => void;
}) {
  return (
    <>
      {props.blocks.map((g) => (
        <FacadeBlockRows
          key={g.label}
          block={g}
          vm={props.vm}
          profileNames={props.profileNames}
          off={props.off}
          hoverVlan={props.hoverVlan}
          selected={props.selected}
          emphasis={props.emphasis}
          drift={props.drift}
          lagOf={props.lagOf}
          onPortClick={props.onPortClick}
          onPortHover={props.onPortHover}
          onPortHoverMove={props.onPortHoverMove}
          onPortHoverLeave={props.onPortHoverLeave}
        />
      ))}
    </>
  );
}

/**
 * THE FACEPLATE — the switch's sockets as the operator sees them on the front
 * of the box, plus the legend that says what a colour means.
 *
 * The legend travels WITH the squares rather than living in the page: it names
 * the exact fills `PortSquare` paints, and a legend that drifted from the
 * squares it describes is worse than no legend at all. The selection vocabulary
 * (⇧, Ctrl/⌘, double-click) is stated here for the same reason — against the
 * faceplate it applies to, where it is read once.
 *
 * This component decides NOTHING. Which ports are selected, which blink, which
 * VLAN is hovered and what a click means are all the page's business; the
 * faceplate only draws them.
 */
export function Faceplate({
  blocks,
  vm,
  profileNames,
  off,
  hoverVlan,
  onPortHover,
  onPortHoverMove,
  onPortHoverLeave,
  selected,
  emphasis,
  drift,
  lagOf,
  onPortClick,
  lags,
  lagsObservedOnly,
  selectedLag,
  canAddLag,
  onLagSelect,
  onLagRangeRefused,
  onLagAdd,
}: {
  blocks: FacadeBlock[];
  vm: VlanMap;
  /** The site's port-profile vocabulary, for the port tips (see FacadePorts). */
  profileNames?: ProfileNameMap;
  off: boolean;
  hoverVlan: number | null;
  /** Port NAMES currently selected — the standard-model names, not silkscreen. */
  selected: Set<string>;
  /**
   * Ports SINGLED OUT without being the selection: the members of the
   * aggregate currently selected (ADR-0007, decision 1). A separate channel
   * from `selected` deliberately — these ports are not what the next port
   * write applies to, they are what the selected aggregate is made of.
   */
  emphasis?: Set<string>;
  /**
   * The aggregates, drawn as a labelled block AFTER the physical ones. Omit
   * (or pass an empty list with `canAddLag` false) on a screen that has no
   * aggregate story — the block then renders nothing at all.
   */
  lags?: import("../../lags").Lag[];
  lagsObservedOnly?: Set<string>;
  selectedLag?: string | null;
  canAddLag?: boolean;
  onLagSelect?: (name: string) => void;
  onLagRangeRefused?: () => void;
  onLagAdd?: () => void;
  /** Port name → the animation it carries, from `driftMap` plus any live action. */
  drift: Map<string, "pending" | "drift" | "applying">;
  /** Port name → its aggregate, for the LAG overline (lags.ts). */
  lagOf?: Map<string, { name: string; color: string }>;
  onPortClick: (
    name: string,
    e: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean; detail?: number },
  ) => void;
  /**
   * Pointer reporting for the floating port chart. Optional for the same reason
   * it is optional on PortSquare: a screen with no telemetry to draw passes
   * nothing and pays nothing.
   */
  onPortHover?: (name: string, e: React.MouseEvent) => void;
  onPortHoverMove?: (name: string, e: React.MouseEvent) => void;
  onPortHoverLeave?: (name: string) => void;
}) {
  const { t } = useTranslation();
  return (
    <>
      <FacadePorts
        blocks={blocks}
        vm={vm}
        profileNames={profileNames}
        off={off}
        hoverVlan={hoverVlan}
        selected={selected}
        emphasis={emphasis}
        drift={drift}
        lagOf={lagOf}
        onPortClick={onPortClick}
        onPortHover={onPortHover}
        onPortHoverMove={onPortHoverMove}
        onPortHoverLeave={onPortHoverLeave}
      />

      {/* AGGREGATES LAST, in their own labelled block. Last and labelled is
          the whole of what makes decision 1 payable: the operator reads the
          physical sockets in their numbering, then a second register that
          says what it is. */}
      {onLagSelect && onLagAdd && onLagRangeRefused && (
        <LagBlock
          lags={lags ?? []}
          observedOnly={lagsObservedOnly ?? new Set()}
          selected={selectedLag ?? null}
          canAdd={!!canAddLag}
          onSelect={onLagSelect}
          onRefuseRange={onLagRangeRefused}
          onAdd={onLagAdd}
        />
      )}

      <div className="port-legend">
        {/* Selection shortcuts live HERE, against the facade they control, with
            help behind: read once. */}
        <span>
          {t("switchDetailPage.selection")}
          <HelpTip label={t("switchDetailPage.selectionHelpLabel")}>
            <p>
              {t("switchDetailPage.selectionHelp1")}
            </p>
            <ul>
              <li>{t("switchDetailPage.selectionHelpShift")}</li>
              <li>{t("switchDetailPage.selectionHelpCtrl")}</li>
              <li>{t("switchDetailPage.selectionHelpShiftCtrl")}</li>
              {/* The two gestures that are not discoverable by looking at the
                  facade: how a selection is DROPPED, and what a double-click
                  opens. Both live here rather than as visible hints — the
                  faceplate is dense enough, and this bubble is where the
                  click vocabulary is already learned. */}
              <li>{t("switchDetailPage.selectionHelpClear")}</li>
              <li>{t("switchDetailPage.selectionHelpDouble")}</li>
            </ul>
            <p>
              {t("switchDetailPage.selectionHelpMulti")}
            </p>
          </HelpTip>
        </span>
        <span><i className="sw" style={{ background: "var(--accent)" }} /> {t("switchDetailPage.legendAccessActive")}</span>
        <span><i className="sw" style={{ background: "repeating-linear-gradient(135deg,var(--accent) 0 3px,var(--ok) 3px 6px)" }} /> {t("switchDetailPage.legendTrunk")}</span>
        <span><i className="sw" style={{ border: "1px solid var(--accent)", background: "transparent" }} /> {t("switchDetailPage.legendConfiguredDown")}</span>
        <span><i className="sw" style={{ background: UNMANAGED_UP, border: `1px solid ${UNMANAGED_UP_BORDER}` }} /> {t("switchDetailPage.legendActiveUnmanaged")}</span>
        <span><i className="sw" style={{ background: "var(--bg-hover)", border: "1px solid var(--border)" }} /> {t("switchDetailPage.legendFree")}</span>
        <span><i className="sw poe" /> {t("switchDetailPage.legendPoeActive")}</span>
      </div>
    </>
  );
}

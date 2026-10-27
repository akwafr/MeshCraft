// THE AGGREGATE BLOCK OF THE FACEPLATE — ADR-0007, decision 1.
//
// An aggregate is drawn as a cell, after the physical blocks, in its own
// labelled block exactly as "SFP+" is. That is the ADR's whole bet and its
// stated price: the faceplate stops being a strict picture of the box, because
// LAG1 exists nowhere on the capot. It is accepted because the block is
// LABELLED SEPARATELY and comes last — a second register added beside the
// sockets, never mixed into their numbering.
//
// The contract already said this. `wire/mdpv1/lag.go`: "in the bridge tables
// the bond appears AS an interface — indistinguishable from a physical port",
// which is why LAGState.VLAN reuses PortState verbatim. The old separate tile
// was the screen disagreeing with its own data model.
//
// RANGE SELECTION STOPS AT THIS BLOCK'S EDGE. A ⇧-click is measured on
// PHYSICAL order (see FacadeBlockRows' comment on the wrapping that broke it),
// and "from port 12 to LAG2" names no set at all: there is no physical order
// that contains both. So a modified click on an aggregate is REFUSED out loud
// by the page rather than reinterpreted as a plain selection — a silent
// reinterpretation would hand back a selection the operator did not ask for,
// on the one screen where the selection is what the next write applies to.
import { useTranslation } from "react-i18next";

import { ActionButton, QuickTip } from "../../components";
import { lagColor, type Lag } from "../../lags";

/**
 * One aggregate cell. A `<button>` for the same reason PortSquare is one: it
 * is a socket on the facade, not a verb — it carries no label an
 * ActionButton's icon could stand in for, and the thing it does is "become the
 * selection".
 */
function LagCell({
  lag,
  selected,
  observedOnly,
  onClick,
}: {
  lag: Lag;
  selected: boolean;
  observedOnly: boolean;
  onClick: (e: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => void;
}) {
  const { t } = useTranslation();
  const tip =
    lag.name +
    " — " +
    (lag.mode === "lacp" ? "LACP" : t("lag.modeStatic")) +
    "\n" +
    t("lag.memberCount", { count: lag.members.length }) +
    (lag.running === undefined
      ? ""
      : "\n" + (lag.running ? t("lag.converged") : t("lag.notConverged"))) +
    (lag.fallback ? "\n" + t("lag.fallbackOn", { vlan: lag.fallback }) : "") +
    (observedOnly ? "\n" + t("lag.importedTip") : "");
  return (
    <QuickTip text={tip}>
      <button
        type="button"
        className={
          "lag-cell" +
          (selected ? " on" : "") +
          (lag.running === false ? " lag-cell-down" : "") +
          (observedOnly ? " lag-cell-imported" : "")
        }
        onMouseDown={(e) => e.shiftKey && e.preventDefault()}
        onClick={(e) => onClick(e)}
      >
        {/* The overline, in the aggregate's own colour — the SAME colour this
            aggregate paints across the top of each of its member squares. It
            is what ties the cell to the ports it bundles without a line
            drawn across the drawing. */}
        <span className="lag-cell-bar" aria-hidden style={{ background: lagColor(lag.name) }} />
        <span className="lag-cell-name">{lag.name}</span>
        <span className="lag-cell-meta">
          {lag.members.length}
          {lag.mode === "static" ? " · S" : ""}
        </span>
      </button>
    </QuickTip>
  );
}

/**
 * The block: the existing aggregates, then the dashed "+" cell that opens
 * the builder (decision 2).
 *
 * The "+" appears only where `canAdd` — a connector with no proven
 * aggregate write would offer a gesture that answers 422, and the page has no
 * business holding a catalogue of which platforms those are (that is what
 * `lag_caps` is for).
 */
export function LagBlock({
  lags,
  observedOnly,
  selected,
  canAdd,
  onSelect,
  onRefuseRange,
  onAdd,
}: {
  lags: Lag[];
  /** Names read on the device and not yet adopted as an intent. */
  observedOnly: Set<string>;
  /** The aggregate currently selected, or null. */
  selected: string | null;
  canAdd: boolean;
  onSelect: (name: string) => void;
  /** A ⇧ / Ctrl-⌘ click landed on an aggregate: say no, visibly. */
  onRefuseRange: () => void;
  onAdd: () => void;
}) {
  const { t } = useTranslation();
  if (lags.length === 0 && !canAdd) return null;
  return (
    <div className="facade-block">
      <div className="kicker facade-label">{t("lag.blockLabel")}</div>
      <div className="facade-rows scroll-x">
        <div className="facade-rows-inner">
          <div className="facade-row lag-cells">
            {lags.map((l) => (
              <LagCell
                key={l.name}
                lag={l}
                selected={selected === l.name}
                observedOnly={observedOnly.has(l.name)}
                onClick={(e) => {
                  if (e.shiftKey || e.ctrlKey || e.metaKey) {
                    onRefuseRange();
                    return;
                  }
                  onSelect(l.name);
                }}
              />
            ))}
            {canAdd && (
              <ActionButton
                kind="add"
                className="lag-cell lag-cell-add"
                onClick={onAdd}
              >
                {t("lag.add")}
              </ActionButton>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

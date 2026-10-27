// THE AGGREGATE BUILDER — ADR-0007, decision 2: activating the dashed "+"
// darkens the whole page and leaves only the physical ports, to pick the
// members from, with the aggregate's options to the side.
//
// # Why a Modal and not an overlay of its own
//
// The ADR is explicit, and the reason is not taste: the darkening IS a modal,
// so it owes Escape, outside-click, and a focus trap. `Modal` (components.tsx)
// carries all three through the dismissal stack (ui/dismiss.ts), and refuses
// to close while `busy` — which matters here, because the write in flight is
// an apply that replaces EVERY aggregate on the device. A hand-rolled backdrop
// would reopen the seven defects UI-GUIDE §8.1 closed, one of which is exactly
// "Escape abandoned a write whose outcome nobody read".
//
// # Why the whole facade, rather than a list
//
// Choosing members is a question about the FRONT OF THE BOX ("the two SFP+
// cages on the right", "ports 47 and 48") and the answer is read off the
// drawing. A 300 px side panel cannot show a 48-port facade, which is what
// the alternative section of the ADR rejected. So the facade is drawn here,
// through the SAME component the page uses (FacadePorts), never a second
// rendering of it.
import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { LagCaps } from "../../api";
import { ActionButton, ErrorMsg, Field, Modal } from "../../components";
import { Form } from "../../ui/Form";
import { lagColor, nextLagName, type Lag } from "../../lags";
import type { VlanMap } from "../../network";
import type { FacadeBlock } from "./facade";
import { FacadePorts } from "./Faceplate";

const NO_DRIFT = new Map<string, "pending" | "drift" | "applying">();

export function LagBuilder({
  blocks,
  vm,
  caps,
  lags,
  editing,
  busy,
  error,
  onApply,
  onClose,
}: {
  blocks: FacadeBlock[];
  vm: VlanMap;
  caps: LagCaps | undefined;
  /** Every aggregate the device has — for the name, and for who owns what. */
  lags: Lag[];
  /** The aggregate being edited, or null to create a new one. */
  editing: Lag | null;
  busy: boolean;
  error: unknown;
  /** The COMPLETE desired list, already merged by the caller's rules. */
  onApply: (draft: Lag) => void;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const [draft, setDraft] = useState<Lag>(
    editing
      ? { ...editing, members: [...editing.members], vlan: { ...editing.vlan } }
      : { name: nextLagName(lags), mode: "lacp", members: [], vlan: {} },
  );
  // A port already claimed by ANOTHER aggregate is refused out loud, with the
  // name of the aggregate holding it. Not disabled and not silent: the
  // operator clicked a specific socket, and "nothing happened" would send them
  // looking for a rendering bug (ValidateLAGs refuses the same overlap
  // server-side, so a silent accept is not an option either).
  const [refusal, setRefusal] = useState<string | null>(null);

  const owner = new Map<string, string>();
  lags.forEach((l) => l.members.forEach((m) => owner.set(m, l.name)));

  const perLag = caps?.ports_per_lag ?? 0;
  const tooMany = perLag > 0 && draft.members.length > perLag;

  const toggle = (name: string) => {
    setRefusal(null);
    const held = owner.get(name);
    if (held && held !== draft.name) {
      setRefusal(t("lag.portTaken", { lag: held }));
      return;
    }
    setDraft((d) => ({
      ...d,
      members: d.members.includes(name)
        ? d.members.filter((m) => m !== name)
        : [...d.members, name].sort(),
    }));
  };

  // The fallback is offered ONLY where the connector declares the gesture
  // (ADR-0007, decision 3). Not greyed out: three states exist — set, off,
  // and impossible — and a disabled checkbox reads as "off", which would
  // promise a fallback that nothing will write.
  const canFallback = !!caps?.fallback;
  // And it is meaningful only on LACP: fallback is what happens while
  // negotiation has not converged, and a static aggregate never negotiates
  // (ValidateLAGs refuses the combination, so the field goes away with the
  // mode rather than waiting to be refused).
  const showFallback = canFallback && draft.mode === "lacp";

  return (
    <Modal
      title={editing ? t("lag.editTitle", { name: draft.name }) : t("lag.createTitle")}
      busy={busy}
      onClose={onClose}
      help={
        <>
          <p>{t("lag.builderHelp1")}</p>
          <p>{t("lag.builderHelp2")}</p>
        </>
      }
    >
      <Form onSubmit={() => onApply(draft)} busy={busy} className="lag-builder">
        <div className="lag-builder-face">
          <div className="kicker">{t("lag.pickMembers")}</div>
          {/* `selected` is the DRAFT's membership here: inside the builder the
              only selection that exists is "is this port a member", and
              reusing the accent ring keeps the gesture identical to the one
              the operator already knows from the page behind. */}
          <FacadePorts
            blocks={blocks}
            vm={vm}
            off={false}
            hoverVlan={null}
            selected={new Set(draft.members)}
            drift={NO_DRIFT}
            lagOf={
              new Map(
                [...owner.entries()]
                  .filter(([, l]) => l !== draft.name)
                  .map(([p, l]) => [p, { name: l, color: lagColor(l) }]),
              )
            }
            onPortClick={(name) => toggle(name)}
          />
          <p className="note">
            {t("lag.memberCount", { count: draft.members.length })}
            {draft.members.length > 0 ? " — " + draft.members.join(", ") : ""}
          </p>
          {refusal && <p className="note lag-refusal">{refusal}</p>}
          {tooMany && (
            <p className="note lag-refusal">
              {t("lag.tooManyMembers", { max: perLag })}
            </p>
          )}
        </div>

        <aside className="lag-builder-side">
          <div className="lag-builder-name">
            <span className="lag-swatch" style={{ background: lagColor(draft.name) }} />
            <strong>{draft.name}</strong>
          </div>

          <Field label={t("lag.mode")} help={<p>{t("lag.modeHelp")}</p>}>
            <select
              value={draft.mode}
              onChange={(e) =>
                setDraft((d) => ({
                  ...d,
                  mode: e.target.value as Lag["mode"],
                  // Leaving LACP drops the fallback with it: keeping a VLAN
                  // in a field the device can no longer be asked for would
                  // store an intent nothing reads back.
                  fallback: e.target.value === "lacp" ? d.fallback : undefined,
                }))
              }
            >
              <option value="lacp">LACP (802.3ad)</option>
              <option value="static">{t("lag.modeStatic")}</option>
            </select>
          </Field>

          <Field label="VLAN" help={<p>{t("lag.vlanHelp")}</p>}>
            <select
              value={draft.vlan?.mode ?? ""}
              onChange={(e) =>
                setDraft((d) => ({
                  ...d,
                  vlan: { ...d.vlan, mode: e.target.value || undefined },
                }))
              }
            >
              <option value="">{t("lag.vlanUnmanaged")}</option>
              <option value="access">access</option>
              <option value="trunk">trunk</option>
            </select>
          </Field>

          {draft.vlan?.mode && (
            <Field label="PVID">
              <input
                type="number"
                min={1}
                max={4094}
                value={draft.vlan.pvid ?? 1}
                onChange={(e) =>
                  setDraft((d) => ({
                    ...d,
                    vlan: { ...d.vlan, pvid: parseInt(e.target.value, 10) || 1 },
                  }))
                }
              />
            </Field>
          )}

          {draft.vlan?.mode === "trunk" && (
            <Field label={t("lag.tagged")}>
              <input
                type="text"
                placeholder="10, 42, 99"
                defaultValue={(draft.vlan.tagged ?? []).join(", ")}
                onBlur={(e) =>
                  setDraft((d) => ({
                    ...d,
                    vlan: { ...d.vlan, tagged: parseVlanList(e.target.value) },
                  }))
                }
              />
            </Field>
          )}

          {showFallback && (
            <Field label={t("lag.fallback")} help={<p>{t("lag.fallbackHelp")}</p>}>
              <input
                type="number"
                min={0}
                max={4094}
                value={draft.fallback ?? 0}
                onChange={(e) =>
                  setDraft((d) => ({
                    ...d,
                    // 0 IS the answer "no fallback" (mdpv1.LAGFallbackOff), not
                    // an empty field: the contract carries the absence as the
                    // zero VLAN, and the strip on the way out drops it.
                    fallback: Math.max(0, Math.min(4094, parseInt(e.target.value, 10) || 0)),
                  }))
                }
              />
            </Field>
          )}

          <ErrorMsg error={error} />

          <div className="actions">
            <ActionButton
              kind="apply"
              type="submit"
              disabled={draft.members.length === 0 || tooMany || busy}
              title={draft.members.length === 0 ? t("lag.needsMember") : undefined}
            >
              {busy ? t("lag.applying") : t("lag.apply")}
            </ActionButton>
            <ActionButton kind="cancel" onClick={onClose} disabled={busy}>
              {t("common.cancel")}
            </ActionButton>
          </div>
        </aside>
      </Form>
    </Modal>
  );
}

/** "10, 42; 99" → [10, 42, 99], out-of-range silently dropped. */
function parseVlanList(raw: string): number[] {
  return raw
    .split(/[\s,;]+/)
    .map((x) => parseInt(x, 10))
    .filter((n) => Number.isFinite(n) && n >= 1 && n <= 4094)
    .sort((a, b) => a - b);
}

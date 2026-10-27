import { useState } from "react";
import { useTranslation } from "react-i18next";
import type { Device, PortIntent, PortProfileSpec, Profile, Vlan } from "../../api";
import { ActionButton, HelpTip } from "../../components";
import { expandPorts, vlanMap, type PortRef } from "../../network";
import { poeOverrides } from "../poeOverride";
import { observedTrust, trustObsInk, trustToken, type TrustObs } from "../portAdmin";
import { SidePanelGroup } from "./SidePanelGroup";
import { common, rangeLabel, vlanMeta } from "./portFacts";

/**
 * Multiple selection panel.
 *
 * Two principles hold this screen:
 *
 * 1. what DIFFERS between ports is shown as such ("mixed"), never
 *    reduced to the value of the first — otherwise the screen would lie
 *    about what we're about to overwrite;
 * 2. nothing is sent until you click: on twelve ports, a toggle applied
 *    on hover would be a permanent accident.
 *
 * Sectioned exactly like the single-port panel, and for the same reason: an
 * operator moving between one port and twelve must not have to re-learn where
 * anything is. The five sections, their order, their default folds and their
 * `meta` lines all match — only the CONTENT differs, because a selection can
 * disagree with itself and every value here has a third answer ("mixed") that a
 * single port does not have.
 */
export function MultiPortPanel({
  dev,
  sel,
  all,
  role,
  off,
  vlans,
  profiles,
  selProf,
  onSelProf,
  onApply,
  onPatch,
  onPatchEach,
  onPoEWrite,
  onSnoopWrite,
  snoopWriting,
  onClear,
}: {
  dev: Device;
  sel: PortRef[];
  /** The SAME list that `sel` came from: `expandPorts` reconstructs
   * new objects on each call, and an `indexOf` on a copy returns -1. */
  all: PortRef[];
  role: "ing" | "tech" | "viewer";
  off: boolean;
  vlans: Vlan[];
  profiles: Profile[];
  selProf: string | null;
  onSelProf: (id: string | null) => void;
  onApply: () => void;
  onPatch: (patch: Partial<PortIntent>, msg?: string) => void;
  /** One patch PER PORT in a single batch — for repairs whose target value
   * differs port by port (each port re-aligned to its OWN group's PoE). */
  onPatchEach: (patches: Record<string, Partial<PortIntent>>, msg?: string) => void;
  /** Push explicit PoE classes to the device NOW (only acts when writable). */
  onPoEWrite: (ports: Record<string, string>) => void;
  /** Push explicit DHCP snooping TRUST flags to the device NOW. */
  onSnoopWrite: (ports: Record<string, boolean>) => void;
  /** A snooping write is in flight — the button must not be clickable twice. */
  snoopWriting: boolean;
  onClear: () => void;
}) {
  const { t } = useTranslation();

  const vm = vlanMap(vlans);
  const ing = role === "ing";
  const canApply = role !== "viewer" && !off;
  const [label, setLabel] = useState("");

  const mode = common(sel, (p) => p.p.mode ?? "libre");
  const pvid = common(sel, (p) => p.p.pvid ?? null);
  const poe = common(sel, (p) => !!p.p.poe);
  // Per-port capability, observed when a PoE read exists (mixed units make
  // the faceplate flag too coarse — cf. the single-port panel).
  const poeCapOf = (p: PortRef) =>
    dev.poe_state?.ports?.find((x) => x.port === p.name)?.capable ?? p.poeCap;
  const poeCapableSel = sel.filter(poeCapOf);
  const withPoe = poeCapableSel.length;
  const poeWritable = !!dev.poe_caps?.write;
  // The multi-port sibling of the single panel's setPoE: store the intent for
  // the whole selection (the parent's merge downgrades poe on non-capable
  // ports), and push explicit classes for the capable ones when the connector
  // writes PoE. "On" preserves a passive-24V port's class, same rule as the
  // single panel.
  const setPoEMulti = (on: boolean) => {
    onPatch(
      on ? { poe: true } : { poe: false, poe_w: 0 },
      on
        ? t("switchDetailPage.poeEnabledMultiToast", { count: withPoe })
        : t("switchDetailPage.poeDisabledMultiToast", { count: withPoe }),
    );
    if (poeWritable && withPoe > 0) {
      const body: Record<string, string> = {};
      poeCapableSel.forEach((p) => {
        const obs = dev.poe_state?.ports?.find((x) => x.port === p.name);
        body[p.name] = on ? (obs?.class === "24v" ? "24v" : "54v") : "none";
      });
      onPoEWrite(body);
    }
  };
  // Requirement-3, the multi-port way: which selected ports FORCE a PoE value
  // against the group each one follows. A selection may mix groups, so the
  // comparison is per port against ITS group (cf. poeOverride.ts) — never
  // against a group common to the selection, which may not exist. Only
  // PoE-capable ports enter: a cage holds poe:false by hardware, not by
  // choice, and flagging it would offer a repair no hardware can honor.
  const overridden = poeOverrides(
    poeCapableSel.map((p) => ({
      name: p.name, poe: p.p.poe, profile_id: p.p.profile_id,
    })),
    profiles.map((g) => ({
      id: g.id, name: g.name,
      poe: !!(g.spec as unknown as PortProfileSpec).poe,
    })),
  );
  // One repair re-aligns them ALL, each to its own group's value — hence
  // onPatchEach, not one shared patch. The device half mirrors the single
  // panel's setPoE: push explicit classes when the connector writes PoE, and
  // "on" keeps a passive-24V port passive.
  const repairPoE = () => {
    const patches: Record<string, Partial<PortIntent>> = {};
    const writes: Record<string, string> = {};
    overridden.forEach((o) => {
      patches[o.port] = o.groupPoE ? { poe: true } : { poe: false, poe_w: 0 };
      const obs = dev.poe_state?.ports?.find((x) => x.port === o.port);
      writes[o.port] = o.groupPoE ? (obs?.class === "24v" ? "24v" : "54v") : "none";
    });
    onPatchEach(
      patches,
      t("switchDetailPage.poeOverrideMultiRemovedToast", { count: overridden.length }),
    );
    if (poeWritable) onPoEWrite(writes);
  };
  // Manual VLAN edits on a selection detach every port from its group, same
  // rule as the single-port panel: a port leaves its group by changing lane.
  const patchManual = (patchIn: Partial<PortIntent>, msg?: string) =>
    onPatch({ ...patchIn, profile_id: null }, msg);
  const linked = sel.filter((p) => !!p.p.link).length;

  // --- DHCP snooping over a SELECTION ----------------------------------
  //
  // The observed value is folded to a TOKEN before `common` sees it, and that
  // is not a detail: observedTrust() answers `undefined` for "the device never
  // reported this port", while `common` answers `undefined` for "the ports
  // disagree". Two different facts on one value would have merged "we do not
  // know" into "they differ" — the first is a reason to re-read the device, the
  // second a reason to look at which port is the odd one out.
  const trustTokenOf = (p: PortRef): TrustObs =>
    trustToken(observedTrust(dev.dhcp_snoop_state, p.name));
  const trustObsToken = common(sel, trustTokenOf);
  const trustIntent = common(sel, (p) => !!p.p.dhcp_snooping);
  const trustedCount = sel.filter((p) => !!p.p.dhcp_snooping).length;
  const snoop = dev.dhcp_snoop_state;
  const setTrustMulti = (on: boolean) =>
    onPatch(
      { dhcp_snooping: on },
      on
        ? t("switchDetailPage.snoopTrustedMultiToast", { count: sel.length })
        : t("switchDetailPage.snoopUntrustedMultiToast", { count: sel.length }),
    );
  // The immediate write carries EACH port's own recorded flag, not one shared
  // value: a mixed selection is exactly where a single bool would silently
  // re-trust — or worse, untrust — ports the operator never named. The endpoint
  // takes a per-port map, so there is no reason to flatten it.
  const writeTrustMulti = () => {
    const body: Record<string, boolean> = {};
    sel.forEach((p) => { body[p.name] = !!p.p.dhcp_snooping; });
    onSnoopWrite(body);
  };

  // --- VLAN group vs manual, the multi-port way ------------------------
  //
  // Each port's recorded lane is the same fact as in the single-port panel
  // (`profile_id` set = the port lives by a group), but a SELECTION may mix
  // ports following different groups, or none. Chosen semantics:
  //   - every port follows the SAME group  -> group lane, followed note;
  //   - no port follows anything           -> manual lane opens first;
  //   - anything mixed (different groups, or grouped and manual ports
  //     together) -> the group lane opens with an explicit "mixed" note:
  //     applying a group is the one action that homogenizes, and it always
  //     attaches ALL selected ports. Each profile row also counts how many
  //     selected ports follow it (n/total), so partial follows stay visible
  //     instead of being reduced to the first port's state.
  // Flipping the selector writes nothing. The first manual VLAN edit
  // detaches the whole selection (patchManual); PoE and STP edits do NOT —
  // per-port overrides on top of the group, same rule as the single panel.
  const commonProfId = common(sel, (p) => p.p.profile_id ?? null);
  const followedAll = commonProfId
    ? profiles.find((x) => x.id === commonProfId) ?? null
    : null;
  const danglingAll = !!commonProfId && !followedAll;
  const attachedCount = sel.filter((p) => !!p.p.profile_id).length;
  const laneMixed = commonProfId === undefined;
  const [cfgMode, setCfgMode] = useState<"group" | "manual">(
    attachedCount > 0 ? "group" : "manual",
  );
  const manualLane = !off && ing && cfgMode === "manual";

  const stpAny =
    !!dev.stp_caps?.edge || !!dev.stp_caps?.port_enable || !!dev.stp_caps?.bpdu_guard;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {/* ============================ 1. INFO ============================ */}
      <SidePanelGroup
        title={t("switchDetailPage.sectionInfo")}
        // Folded, this still has to say WHAT is selected — the whole panel is
        // meaningless without it, and "12 ports" is the shortest true answer.
        meta={t("switchDetailPage.portsCountLabel", { count: sel.length })}
        defaultOpen
      >
        <div>
          <div className="kicker">{dev.name} · {t("switchDetailPage.selectionKicker")}</div>
          <div style={{ fontSize: 16, fontWeight: 700, marginTop: 8 }}>
            {sel.length} ports
          </div>
          <div className="note mono" style={{ marginTop: 2 }}>{rangeLabel(sel, all)}</div>
          <ActionButton kind="cancel" className="ghost" style={{ marginTop: 6 }} onClick={onClear}>
            {t("switchDetailPage.clearSelection")}
          </ActionButton>
        </div>

        {/* "MIXED" IS DIM, NOT ORANGE (fixed 2026-08-08, UI-GUIDE §6).
            It read `--warn` at all five sites on this panel, and that was the
            most-repeated token misuse in the frontend. A selection whose ports
            disagree is not a problem with the device — it is this panel saying
            honestly that there is no single value to print, which by §2's
            ladder is "nothing to say", which is `--text-dim`. Orange here is
            what teaches an operator to stop reading orange on the screen that
            also has to show err-disabled and drift. The italic stays: it is
            what distinguishes a state word from a value. */}
        <div className="kv" style={{ borderTop: "1px solid var(--border)", paddingTop: 12, marginTop: 12 }}>
          <span className="k">{t("switchDetailPage.modeLabel")}</span>
          <span style={{ fontWeight: 600 }}>
            {mode === undefined ? <i style={{ color: "var(--text-dim)" }}>{t("switchDetailPage.modeMixed")}</i>
              : mode === "access" ? t("switchDetailPage.modeAccessLabel") : mode === "trunk" ? t("switchDetailPage.modeTrunkLabel") : t("switchDetailPage.modeFreeLabel")}
          </span>
          <span className="k">{t("switchDetailPage.vlanLabel")}</span>
          <span>
            {pvid === undefined ? <i style={{ color: "var(--text-dim)" }}>{t("switchDetailPage.modeMixed")}</i>
              : pvid === null ? "—"
                : (vm.get(pvid)?.name ?? "VLAN " + pvid) + " (" + pvid + ")"}
          </span>
          <span className="k">{t("switchDetailPage.activeLinksLabel")}</span>
          <span>{off ? "—" : linked + " / " + sel.length}</span>
          <span className="k">PoE</span>
          <span>
            {withPoe === 0 ? t("switchDetailPage.noPoeInSelection")
              : (poe === undefined ? t("switchDetailPage.modeMixed") : poe ? t("switchDetailPage.powered") : t("switchDetailPage.cut")) +
                (withPoe < sel.length ? t("switchDetailPage.poeCapableNote", { capable: withPoe, total: sel.length }) : "")}
          </span>
        </div>

        {off && (
          <div className="note" style={{ border: "1px solid var(--border)", padding: "10px 12px", borderRadius: 8, marginTop: 12 }}>
            {t("switchDetailPage.unreachableMultiNote")}
          </div>
        )}

        {/* The shared label lives in Info and not in a sixth section: a label is
            what a port IS called, the same fact as the name and the link above
            it, and it is the one write on this panel that changes nothing about
            how traffic flows. */}
        {!off && ing && (
          <div style={{ borderTop: "1px solid var(--border)", paddingTop: 12, marginTop: 12 }}>
            <div className="kicker" style={{ marginBottom: 5 }}>{t("switchDetailPage.commonLabelTitle")}</div>
            <div className="row" style={{ gap: 6 }}>
              <input
                value={label}
                placeholder={t("switchDetailPage.commonLabelPlaceholder")}
                onChange={(e) => setLabel(e.target.value)}
                style={{ minHeight: 28, fontSize: 12 }}
              />
              <ActionButton
                kind="apply"
                className="secondary"
                disabled={!label.trim()}
                onClick={() => {
                  onPatch({ label: label.trim() }, t("switchDetailPage.portsTaggedToast", { count: sel.length }));
                  setLabel("");
                }}
              >
                {t("switchDetailPage.applyLabelBtn")}
              </ActionButton>
            </div>
          </div>
        )}
      </SidePanelGroup>

      {/* ========================== 2. PROFILE ===========================
          Same selector as the single-port panel; the lane the selection opens
          on and the mixed-state indicator are decided above (see the "VLAN
          group vs manual, the multi-port way" comment). */}
      {!off && canApply && (
        <SidePanelGroup
          title={t("switchDetailPage.sectionProfile")}
          meta={
            followedAll
              ? followedAll.name
              : danglingAll
                ? t("switchDetailPage.profileMetaDangling")
                : laneMixed
                  ? t("switchDetailPage.modeMixed")
                  : t("switchDetailPage.profileMetaManual")
          }
          defaultOpen={false}
        >
          <div className="kicker" style={{ marginBottom: 8, display: "inline-flex", alignItems: "center", gap: 4 }}>
            {t("switchDetailPage.vlanConfigTitle")}
            <HelpTip label={t("switchDetailPage.cfgModeHelpLabel")}>
              <p>{t("switchDetailPage.cfgModeHelp1")}</p>
              <p>{t("switchDetailPage.cfgModeHelp2")}</p>
              <p>{t("switchDetailPage.cfgModeHelp3")}</p>
            </HelpTip>
          </div>
          {/* The selector is an ENGINEER control: technicians apply groups
              and nothing else, so for them the group lane IS the section. */}
          {ing && (
            <div className="seg3">
              <button
                className={cfgMode === "group" ? "on" : ""}
                onClick={() => setCfgMode("group")}
              >
                {t("switchDetailPage.cfgModeGroup")}
              </button>
              <button
                className={cfgMode === "manual" ? "on" : ""}
                onClick={() => setCfgMode("manual")}
              >
                {t("switchDetailPage.cfgModeManual")}
              </button>
            </div>
          )}

          {(!ing || cfgMode === "group") ? (
            <>
              {/* What the selection lives by is the FIRST thing this lane
                  says — including the honest "mixed" answer. */}
              {followedAll && (
                <div className="note" style={{ margin: "2px 0 8px" }}>
                  {t("switchDetailPage.groupFollowedNoteMulti", { count: sel.length, name: followedAll.name })}
                </div>
              )}
              {danglingAll && (
                <div className="note" style={{ margin: "2px 0 8px", color: "var(--warn)" }}>
                  {t("switchDetailPage.groupDanglingNoteMulti")}
                </div>
              )}
              {/* --text-dim, not --warn: the sixth instance of the mixed-
                  selection misuse the 2026-08-08 sweep fixed five times (it
                  keyed on `modeMixed` and this one is named `laneMixed`). The
                  sentence says the selection follows different groups — or
                  none — and that applying one below will attach them all. That
                  is "there is no single value" (UI-GUIDE §2 step 5), not a
                  fault: nothing on the device is wrong and nothing needs
                  arbitrating. The `danglingAll` note above it stays orange
                  because a group that no longer exists IS a fault. */}
              {laneMixed && (
                <div className="note" style={{ margin: "2px 0 8px", color: "var(--text-dim)" }}>
                  {t("switchDetailPage.groupMixedNoteMulti", { count: sel.length })}
                </div>
              )}
              {!followedAll && !danglingAll && !laneMixed && (
                <div className="note" style={{ margin: "2px 0 8px" }}>
                  {t("switchDetailPage.groupNoneNoteMulti")}
                </div>
              )}
              <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
                {profiles.map((prof) => {
                  const spec = prof.spec as unknown as PortProfileSpec;
                  const locked = !!spec.eng_only && !ing;
                  const nFollow = sel.filter((p) => p.p.profile_id === prof.id).length;
                  const swatchIds =
                    spec.mode === "access"
                      ? [spec.pvid]
                      : spec.tagged === "all"
                        ? [spec.pvid]
                        : ((spec.tagged ?? []) as number[]).slice(0, 4);
                  return (
                    <button
                      key={prof.id}
                      disabled={locked}
                      className={"prof-row" + (selProf === prof.id ? " sel" : "")}
                      onClick={() => onSelProf(prof.id)}
                    >
                      <span style={{ display: "flex", gap: 2, flex: "none" }}>
                        {swatchIds.map((id, i) => (
                          <span
                            key={i}
                            style={{
                              width: 9, height: 9, borderRadius: 2, display: "block",
                              background: id != null && vm.get(id) ? vm.get(id)!.color : "#888",
                            }}
                          />
                        ))}
                      </span>
                      <span style={{ fontSize: 12, fontWeight: 700 }}>{prof.name}</span>
                      {/* n/total keeps a PARTIAL follow visible; the bare
                          badge means the whole selection follows this one. */}
                      {nFollow > 0 && (
                        <span style={{ color: "var(--ok)", fontWeight: 700, fontSize: 11 }}>
                          ✓ {t("switchDetailPage.groupFollowedBadge")}
                          {nFollow < sel.length ? ` ${nFollow}/${sel.length}` : ""}
                        </span>
                      )}
                      <span className="note" style={{ marginLeft: "auto", whiteSpace: "nowrap", fontSize: 9, letterSpacing: ".05em" }}>
                        {(spec.mode === "access"
                          ? t("switchDetailPage.accessPrefix", { pvid: spec.pvid ?? "" })
                          : spec.tagged === "all" ? "TRUNK ▤▤▤" : "TRUNK") + (spec.poe ? " · POE" : "")}
                      </span>
                    </button>
                  );
                })}
                {profiles.length === 0 && <div className="note">{t("switchDetailPage.noProfilesDefined")}</div>}
              </div>
              <ActionButton kind="apply" style={{ width: "100%", marginTop: 10 }} disabled={!selProf} onClick={onApply}>
                {t("switchDetailPage.applyToNPorts", { count: sel.length })}
              </ActionButton>
              {!ing && (
                <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
                  {t("switchDetailPage.engineerProfilesNote")}
                </div>
              )}
            </>
          ) : (
            // Manual lane selected: the profile list is gone rather than greyed
            // out, same reason as the single-port panel — its rows ATTACH, and
            // one click here would re-stamp the whole selection.
            <div className="note" style={{ marginTop: 8 }}>
              {t("switchDetailPage.profileManualLaneNote")}
            </div>
          )}
        </SidePanelGroup>
      )}

      {/* ============================ 3. VLAN ============================ */}
      <SidePanelGroup
        title={t("switchDetailPage.sectionVlan")}
        meta={
          mode === undefined || pvid === undefined
            ? t("switchDetailPage.modeMixed")
            : vlanMeta(mode, pvid, t)
        }
        defaultOpen={false}
      >
        {manualLane ? (
          <>
            {attachedCount > 0 && (
              <div className="note" style={{ margin: "0 0 8px" }}>
                {t("switchDetailPage.manualDetachNoteMulti", { count: attachedCount })}
              </div>
            )}
            <div className="seg3">
              {(["access", "trunk", "libre"] as const).map((m) => (
                <button
                  key={m}
                  className={mode === m ? "on" : ""}
                  onClick={() =>
                    patchManual(
                      { mode: m },
                      t("switchDetailPage.portsToModeToast", { count: sel.length, mode: m.toUpperCase() }),
                    )
                  }
                >
                  {m === "access" ? t("switchDetailPage.modeAccess") : m === "trunk" ? t("switchDetailPage.modeTrunk") : t("switchDetailPage.modeFree")}
                </button>
              ))}
            </div>

            {(mode === "access" || mode === undefined) && (
              <>
                <div className="kicker" style={{ margin: "10px 0 6px" }}>{t("switchDetailPage.accessVlanTitle")}</div>
                <div className="vlan-pick">
                  {vlans.map((v) => (
                    <button
                      key={v.id}
                      className={"vlan-row" + (pvid === v.vlan_id ? " sel" : "")}
                      onClick={() =>
                        patchManual(
                          { mode: "access", pvid: v.vlan_id, tagged: [] },
                          t("switchDetailPage.portsToAccessToast", { count: sel.length, name: v.name }),
                        )
                      }
                    >
                      <span className="vlan-chip" style={{ background: v.color, margin: 0 }} />
                      <span style={{ fontWeight: 600 }}>{v.name}</span>
                      <span className="note">{v.vlan_id}</span>
                    </button>
                  ))}
                </div>
              </>
            )}

            {mode === "trunk" && (
              <>
                <div className="kicker" style={{ margin: "10px 0 6px" }}>
                  {t("switchDetailPage.taggedMembersTitle")}
                  <HelpTip label={t("switchDetailPage.taggedMembersHelpLabel")}>
                    <p>
                      {t("switchDetailPage.taggedMembersHelp")}
                    </p>
                  </HelpTip>
                </div>
                <div className="vlan-pick">
                  <button
                    className="vlan-row"
                    onClick={() => patchManual({ tagged: "all" }, t("switchDetailPage.fullTrunkSelectionToast", { count: sel.length }))}
                  >
                    <span className="vlan-chip" style={{ background: "var(--accent)", margin: 0 }} />
                    <span style={{ fontWeight: 600 }}>{t("switchDetailPage.fullTrunkAllVlans")}</span>
                  </button>
                </div>
              </>
            )}
          </>
        ) : (
          // No manual controls on screen: the values, read-only, and where the
          // controls are. Same choice as the single-port panel — a section that
          // opens onto nothing would be a worse answer than a summary.
          <>
            <div className="kv">
              <span className="k">{t("switchDetailPage.modeLabel")}</span>
              <span style={{ fontWeight: 600 }}>
                {mode === undefined ? <i style={{ color: "var(--text-dim)" }}>{t("switchDetailPage.modeMixed")}</i>
                  : mode === "access" ? t("switchDetailPage.modeAccessLabel") : mode === "trunk" ? t("switchDetailPage.modeTrunkLabel") : t("switchDetailPage.modeFreeLabel")}
              </span>
              <span className="k">{t("switchDetailPage.vlanLabel")}</span>
              <span>
                {pvid === undefined ? <i style={{ color: "var(--text-dim)" }}>{t("switchDetailPage.modeMixed")}</i>
                  : pvid === null ? "—"
                    : (vm.get(pvid)?.name ?? "VLAN " + pvid) + " (" + pvid + ")"}
              </span>
            </div>
            <div className="note" style={{ marginTop: 8, fontSize: 10 }}>
              {off
                ? t("switchDetailPage.unreachableMultiNote")
                : ing
                  ? t("switchDetailPage.vlanManualElsewhereNote")
                  : t("switchDetailPage.vlanViewerNote")}
            </div>
          </>
        )}
      </SidePanelGroup>

      {/* =========================== 4. OPTIONS ========================== */}
      <SidePanelGroup
        title={t("switchDetailPage.sectionOptions")}
        meta={
          trustedCount > 0
            ? snoop && !snoop.enabled
              ? t("switchDetailPage.snoopMetaTrustedInert")
              : t("switchDetailPage.snoopMetaTrustedCount", { count: trustedCount })
            : undefined
        }
        defaultOpen={false}
      >
        {/* --- DHCP snooping trust, over the whole selection --------------- */}
        <div className="kicker" style={{ marginBottom: 6, display: "inline-flex", alignItems: "center", gap: 4 }}>
          {t("switchDetailPage.snoopTitle")}
          <HelpTip label={t("switchDetailPage.snoopHelpLabel")}>
            <p>{t("switchDetailPage.snoopHelp1")}</p>
            <p>{t("switchDetailPage.snoopHelp2")}</p>
            <p>{t("switchDetailPage.snoopHelp3")}</p>
            <p>{t("switchDetailPage.snoopHelp4")}</p>
          </HelpTip>
        </div>
        {/* The device-wide feature first, exactly as in the single-port panel:
            trusting twelve ports on a switch that inspects nothing installs
            nothing, and a selection is where that mistake scales. */}
        {/* Same observation, same bubble, same keys as the single-port panel:
            the two must not start disagreeing about a device neither of them
            has read. See the comment there for why the imperative went. */}
        {snoop === undefined ? (
          <div
            className="note"
            style={{ marginBottom: 8, display: "flex", alignItems: "center", gap: 4 }}
          >
            {t("switchDetailPage.snoopDeviceUnreadNote")}
            <HelpTip icon="i" label={t("switchDetailPage.snoopDeviceUnreadHelpLabel")}>
              <p>{t("switchDetailPage.snoopDeviceUnreadHelp")}</p>
            </HelpTip>
          </div>
        ) : !snoop.enabled ? (
          <div className="note" style={{ marginBottom: 8, color: "var(--warn)" }}>
            {t("switchDetailPage.snoopDeviceOffNote")}
          </div>
        ) : (
          <div className="note" style={{ marginBottom: 8 }}>
            {(snoop.vlans ?? []).length > 0
              ? t("switchDetailPage.snoopDeviceOnNote", { vlans: (snoop.vlans ?? []).join(", ") })
              : t("switchDetailPage.snoopDeviceOnNoVlanNote")}
          </div>
        )}
        <div className="kv">
          <span className="k">{t("switchDetailPage.snoopObservedLabel")}</span>
          {/* Same ink rule as the single-port panel, from the same function.
              `common` answers `undefined` for "the ports disagree", which is
              the fourth token — dim, like "unread", because neither is a claim
              about the device. */}
          <span style={{ fontWeight: 600, color: trustObsInk(trustObsToken ?? "mixed") }}>
            {trustObsToken === undefined
              ? <i>{t("switchDetailPage.modeMixed")}</i>
              : trustObsToken === "unread"
                ? t("switchDetailPage.snoopObservedUnknown")
                : trustObsToken === "trusted"
                  ? t("switchDetailPage.snoopObservedTrusted")
                  : t("switchDetailPage.snoopObservedUntrusted")}
          </span>
          <span className="k">{t("switchDetailPage.snoopIntentLabel")}</span>
          <span>
            {trustIntent === undefined
              ? t("switchDetailPage.snoopIntentMixed", { count: trustedCount, total: sel.length })
              : trustIntent
                ? t("switchDetailPage.snoopObservedTrusted")
                : t("switchDetailPage.snoopObservedUntrusted")}
          </span>
        </div>
        {ing && !off && (
          <>
            <div className="row" style={{ marginTop: 8, gap: 6, alignItems: "center" }}>
              <div className="seg3" style={{ marginBottom: 0 }}>
                <button className={trustIntent === true ? "on" : ""} onClick={() => setTrustMulti(true)}>
                  {t("switchDetailPage.snoopTrustedBtn")}
                </button>
                <button className={trustIntent === false ? "on" : ""} onClick={() => setTrustMulti(false)}>
                  {t("switchDetailPage.snoopUntrustedBtn")}
                </button>
              </div>
              <ActionButton
                kind="apply"
                className="secondary"
                disabled={snoopWriting}
                title={t("switchDetailPage.snoopWriteTitle")}
                onClick={writeTrustMulti}
              >
                {/* A mixed selection says so on the button: it pushes each
                    port's own flag, and a label reading "write trusted" would
                    be a promise to change ports nobody asked about. */}
                {trustIntent === undefined
                  ? t("switchDetailPage.snoopWriteEachBtn")
                  : trustIntent
                    ? t("switchDetailPage.snoopWriteTrustedBtn")
                    : t("switchDetailPage.snoopWriteUntrustedBtn")}
              </ActionButton>
            </div>
            <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
              {t("switchDetailPage.snoopSplitNote")}
            </div>
          </>
        )}

        {/* --- Spanning tree / BPDU guard over the selection -------------- */}
        {!off && ing && mode !== "libre" && stpAny && (
          <div style={{ borderTop: "1px solid var(--border)", paddingTop: 12, marginTop: 12 }}>
            <div className="kicker" style={{ marginBottom: 8 }}>{t("switchDetailPage.stpTitle")}</div>
            <div>
              {dev.stp_caps?.edge && (
                <div className="row" style={{ marginBottom: 8 }}>
                  <span className="kicker">{t("switchDetailPage.stpEdgeLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    {(dev.stp_caps.edge_auto
                      ? (["auto", "yes", "no"] as const)
                      : (["yes", "no"] as const)
                    ).map((e) => (
                      <button
                        key={e}
                        className={common(sel, (p) => String(p.p.stp?.edge ?? "")) === e ? "on" : ""}
                        onClick={() =>
                          onPatch(
                            { stp: { edge: e } },
                            t("switchDetailPage.stpEdgeToast", { name: `${sel.length} ports`, edge: e }),
                          )
                        }
                      >
                        {e === "auto"
                          ? t("switchDetailPage.stpEdgeAuto")
                          : e === "yes"
                            ? t("switchDetailPage.stpEdgeYes")
                            : t("switchDetailPage.stpEdgeNo")}
                      </button>
                    ))}
                  </div>
                </div>
              )}
              {dev.stp_caps?.port_enable && (
                <div className="row" style={{ marginBottom: 8 }}>
                  <span className="kicker">{t("switchDetailPage.stpPortEnableLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    <button
                      className={common(sel, (p) => p.p.stp?.enabled !== false) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: true } },
                          t("switchDetailPage.stpPortOnToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.stpPortOn")}
                    </button>
                    <button
                      className={common(sel, (p) => p.p.stp?.enabled === false) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: false } },
                          t("switchDetailPage.stpPortOffToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.stpPortOff")}
                    </button>
                  </div>
                </div>
              )}
              {dev.stp_caps?.bpdu_guard && (
                <div className="row">
                  <span className="kicker">{t("switchDetailPage.bpduGuardLabel")}</span>
                  <div className="seg3" style={{ marginBottom: 0 }}>
                    <button
                      className={common(sel, (p) => p.p.stp?.bpdu_guard === true) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: true } },
                          t("switchDetailPage.bpduGuardOnToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.bpduGuardOn")}
                    </button>
                    <button
                      className={common(sel, (p) => p.p.stp?.bpdu_guard === false) === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: false } },
                          t("switchDetailPage.bpduGuardOffToast", { name: `${sel.length} ports` }),
                        )
                      }
                    >
                      {t("switchDetailPage.bpduGuardOff")}
                    </button>
                  </div>
                </div>
              )}
            </div>
          </div>
        )}
      </SidePanelGroup>

      {/* ============================= 5. POE ============================
          PoE / the group-override indicator: visible whatever lane is open, and
          these are per-port overrides on top of the group, so touching them
          never detaches. */}
      {!off && ing && withPoe > 0 && (
        <SidePanelGroup
          title={t("switchDetailPage.sectionPoe")}
          meta={
            overridden.length > 0
              ? t("switchDetailPage.poeOverrideMultiCount", { count: overridden.length })
              : poe === undefined
                ? t("switchDetailPage.modeMixed")
                : poe
                  ? t("switchDetailPage.powered")
                  : t("switchDetailPage.cut")
          }
          defaultOpen={false}
        >
          {/* Requirement-3 indicator, the multi-port way: N selected ports
              force a PoE value against their OWN group — the bubble names,
              for each, both values and the group it was compared to, because
              the selection may mix groups. One repair re-aligns them all
              (and pushes to the device when the connector writes PoE, same
              as the single-port panel). */}
          {overridden.length > 0 && (
            <div
              className="row"
              style={{ marginBottom: 8, gap: 4, alignItems: "center", justifyContent: "flex-start" }}
            >
              <HelpTip icon="i" label={t("switchDetailPage.poeOverrideLabel")}>
                <p><b>{t("switchDetailPage.poeOverrideMultiTitle", { count: overridden.length })}</b></p>
                <p>{t("switchDetailPage.poeOverrideMultiBody")}</p>
                <p>
                  {overridden.map((o) => (
                    <span key={o.port}>
                      {t("switchDetailPage.poeOverrideMultiRow", {
                        port: o.port,
                        portValue: o.portPoE
                          ? t("switchDetailPage.poePoweredBtn")
                          : t("switchDetailPage.poeCutBtn"),
                        group: o.groupName,
                        groupValue: o.groupPoE
                          ? t("switchDetailPage.poePoweredBtn")
                          : t("switchDetailPage.poeCutBtn"),
                      })}
                      <br />
                    </span>
                  ))}
                </p>
                <ActionButton kind="apply" className="secondary" onClick={repairPoE}>
                  {t("switchDetailPage.poeOverrideMultiRemoveBtn", { count: overridden.length })}
                </ActionButton>
              </HelpTip>
              <span className="note" style={{ color: "var(--warn)", fontSize: 10 }}>
                {t("switchDetailPage.poeOverrideMultiCount", { count: overridden.length })}
              </span>
            </div>
          )}
          <div className="row">
            <span className="kicker">PoE</span>
            <div className="seg3" style={{ marginBottom: 0 }}>
              <button
                className={poe === true ? "on" : ""}
                onClick={() => setPoEMulti(true)}
              >
                {t("switchDetailPage.poePoweredBtn")}
              </button>
              <button
                className={poe === false ? "on" : ""}
                onClick={() => setPoEMulti(false)}
              >
                {t("switchDetailPage.poeCutBtn")}
              </button>
            </div>
          </div>
          {!poeWritable && (
            <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
              {t("switchDetailPage.poeIntentOnlyNote")}
            </div>
          )}
        </SidePanelGroup>
      )}
    </div>
  );
}

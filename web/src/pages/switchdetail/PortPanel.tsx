import { Fragment, useState } from "react";
import { useTranslation } from "react-i18next";
import type { Device, PortIntent, PortProfileSpec, Profile, Vlan } from "../../api";
import { ActionButton, HelpTip } from "../../components";
import { decimalSeparator, fmtMbps, isSFPRef, vlanMap, type PortRef } from "../../network";
import type { PortClassBand, PortLevelLine } from "../../telemetry";
import { LineChart, SERIES } from "../../viz";
import { observedTrust, trustObsInk, trustToken } from "../portAdmin";
import { LevelChart } from "../../LevelChart";
import { PortClassChart } from "../../PortClassChart";
import {
  opticsCage,
  opticsCageInk,
  opticsHasReading,
  opticsMeta,
  opticsReading,
  opticsReadingInk,
} from "./optics";
import { SidePanelGroup } from "./SidePanelGroup";
import { linkStateLabel, poeClassLabel, vlanMeta } from "./portFacts";

/**
 * THE SINGLE-PORT PANEL, in six collapsible sections.
 *
 * # Why sections, and why this order
 *
 * The panel used to be one flat column: identity, link, graph, VLAN lanes, PoE,
 * spanning tree, one after the other. On a 24-port switch that column is taller
 * than the viewport, which put the two controls an operator reaches for most
 * often — apply a profile, cut PoE — below the fold, behind a scroll they had to
 * discover. Worse, the reading order gave no clue about what belonged to what:
 * PoE and STP are per-port OVERRIDES on top of a group, and stacked flat they
 * looked like more VLAN settings.
 *
 * The sections are the device panel's own pattern (SidePanelGroup, cf. the
 * no-selection summary), applied to a port and ordered from "what is this jack
 * doing right now" down to "what am I about to change":
 *
 *   1. Info    — open. The only section that answers a question the operator
 *                did not have to ask: what this port IS, and what it carries.
 *   2. Profile — the group lane. Closed, because it is a decision, not a fact.
 *   3. Vlan    — the per-port values, and their manual controls.
 *   4. Options — DHCP snooping trust, spanning tree, BPDU guard.
 *   5. PoE     — the power controls and the group-override indicator.
 *   6. Optics  — the transceiver in the cage and its DDM readings. Read-only,
 *                and only on a device that carries an optics observation.
 *
 * Every folded section carries a `meta`: the port name and link on Info, the
 * followed group on Profile, the PVID on Vlan, the trust flag on Options, the
 * draw on PoE, the receive power on Optics. A collapsed panel is still a
 * readable summary — otherwise folding would just be hiding.
 *
 * NOTHING ABOUT THE SEMANTICS MOVED. `cfgMode`, `followed`, `dangling`,
 * `patchManual` and the "a manual VLAN edit detaches, a PoE or STP touch does
 * not" rule are exactly what they were; only their containers changed.
 */
export function PortPanel({
  dev,
  pr,
  role,
  off,
  vlans,
  profiles,
  traffic,
  classes,
  levels,
  stepSeconds,
  selProf,
  onSelProf,
  onApply,
  onPatch,
  onPoEWrite,
  onSnoopWrite,
  snoopWriting,
}: {
  dev: Device;
  pr: PortRef;
  role: "ing" | "tech" | "viewer";
  off: boolean;
  vlans: Vlan[];
  profiles: Profile[];
  /** Both directions of traffic from THIS port, measured (see ../telemetry). */
  traffic: { rx: (number | null)[]; tx: (number | null)[] };
  /**
   * Inbound traffic of THIS port decomposed by destination class, one band per
   * class — including the classes this platform cannot count, which carry
   * their reason rather than being absent (ADR-0028).
   */
  classes: PortClassBand[];
  /** Optical level history of THIS port, one line per direction and lane. */
  levels: PortLevelLine[];
  /** The pitch of the window served, to name the points of the axis. */
  stepSeconds: number;
  selProf: string | null;
  onSelProf: (id: string | null) => void;
  onApply: () => void;
  onPatch: (patch: Partial<PortIntent>, msg?: string) => void;
  /** Push explicit PoE classes to the device NOW (only wired when writable). */
  onPoEWrite: (ports: Record<string, string>) => void;
  /** Push explicit DHCP snooping TRUST flags to the device NOW. */
  onSnoopWrite: (ports: Record<string, boolean>) => void;
  /** A snooping write is in flight — the button must not be clickable twice. */
  snoopWriting: boolean;
}) {
  const { t } = useTranslation();

  const p = pr.p;
  const vm = vlanMap(vlans);
  const canApply = role !== "viewer" && !off;
  const ing = role === "ing";

  // --- VLAN group vs manual: which lane this port's configuration lives in ---
  //
  // `profile_id` on the intent is the recorded fact; `followed` resolves it
  // against the site's profiles. A reference whose profile was DELETED is a
  // third state worth naming (`dangling`): the stamped configuration is still
  // on the port and still applied, only the provenance is gone — the panel
  // says so instead of silently reclassifying the port as manual.
  const followed = p.profile_id
    ? profiles.find((x) => x.id === p.profile_id) ?? null
    : null;
  const dangling = !!p.profile_id && !followed;
  // The selector's own state: which lane's CONTROLS are on screen. Starts on
  // the recorded lane, and flipping it writes NOTHING — the stored config only
  // moves when the operator applies a group or edits a manual value. Per-port
  // by construction: the component is keyed on the port name.
  const [cfgMode, setCfgMode] = useState<"group" | "manual">(
    p.profile_id ? "group" : "manual",
  );
  // Manual edits DETACH: the port stops following its group by changing a
  // VLAN value, never by merely looking at the manual tab.
  const patchManual = (patchIn: Partial<PortIntent>, msg?: string) =>
    onPatch(p.profile_id ? { ...patchIn, profile_id: null } : patchIn, msg);
  // The manual VLAN CONTROLS are on screen only in that lane; when they are
  // not, the Vlan section shows the values read-only instead of an empty box —
  // a folded-open section that says nothing is worse than no section.
  const manualLane = !off && ing && cfgMode === "manual";

  // --- PoE: per-port capability, observation, and group override ---
  //
  // Observed capability (read from the device) beats the faceplate flag: a
  // mixed unit (EdgePoint S16, jacks 0/1-0/4 without PSE hardware) is exactly
  // the case where the group-level flag is too coarse. The faceplate answers
  // only while no observation exists yet.
  const poeObs = dev.poe_state?.ports?.find((x) => x.port === pr.name);
  const poeCapable = poeObs ? poeObs.capable : pr.poeCap;
  const poeWritable = !!dev.poe_caps?.write;
  // What the followed group PRESCRIBES for PoE, and whether this port forces
  // something else. Profiles write the intent on both sides (poe true AND
  // false), so a plain inequality is the honest comparison.
  const groupPoE = followed
    ? !!(followed.spec as unknown as PortProfileSpec).poe
    : null;
  const poeOverride = groupPoE !== null && !!p.poe !== groupPoE;
  const setPoE = (on: boolean, msg?: string) => {
    onPatch(
      on ? { poe: true } : { poe: false, poe_w: 0 },
      msg ?? (on
        ? t("switchDetailPage.poeEnabledToast", { name: pr.name })
        : t("switchDetailPage.poeDisabledToast", { name: pr.name })),
    );
    if (poeWritable) {
      // "On" keeps a passive-24V port passive: silently promoting it to the
      // 802.3af/at class would negotiate where the operator chose raw voltage.
      const onClass = poeObs?.class === "24v" ? "24v" : "54v";
      onPoEWrite({ [pr.name]: on ? onClass : "none" });
    }
  };

  // --- Optics: what is in this cage, and what it measures --------------------
  //
  // READ-ONLY, and the panel's only section with no control at all: optics has
  // no write path anywhere in the product, which is also why there is no
  // `optics_caps` to consult — the PRESENCE of `optics_state` is the whole
  // capability statement (cf. internal/api/optics.go).
  //
  // The section renders only when an observation exists. Absent means "never
  // read, OR this connector cannot read transceivers", the API does not
  // separate the two, and a permanently blank "optics: not read" row on every
  // SFP port of every device in the fleet would be a row nobody ever reads —
  // the `.info-banner` failure mode applied to text.
  //
  // The SFP test is a fallback, not the gate: `isSFPRef` reads the faceplate
  // group's label, and a platform that one day reports a module on a port that
  // heuristic does not recognise must still show it. An actual module, or a
  // cage the connector failed to read, opens the section on its own.
  const optics = dev.optics_state;
  const cage = opticsCage(optics, pr.name);
  // Level HISTORY exists independently of the observation: the streaming
  // collectors (gNMI) feed the series without any `optics_state` snapshot —
  // the Arista case — so a port with curves must open the section even on a
  // device the CLI reader never visited.
  const hasLevels = levels.length > 0;
  const showOptics =
    !off &&
    (hasLevels ||
      (!!optics && (isSFPRef(pr) || cage.kind === "module" || cage.kind === "blind")));
  const dsep = decimalSeparator();

  // --- DHCP snooping: the stored trust flag, what the device says, and the
  //     device-wide feature the flag depends on ------------------------------
  //
  // THE FLAG IS TRUST, and nothing else (cf. internal/driver/dhcpsnoop.go):
  // "trusted" means DHCP server replies arriving on this jack are accepted —
  // the uplink toward the real DHCP server, or toward another switch. Untrusted
  // is where the protection lives and is every platform's default, so an absent
  // flag reads as untrusted.
  //
  // The observed value keeps its THREE-WAY answer. observedTrust() returns
  // `undefined` for a port the device never reported, and that is not "false":
  // "untrusted" means protected, "not read" means we do not know, and an
  // operator acts differently on each. Collapsing them here would throw away
  // the exact distinction the connector took care to preserve on the wire.
  const snoop = dev.dhcp_snoop_state;
  const trustObs = observedTrust(snoop, pr.name);
  const trustIntent = !!p.dhcp_snooping;
  const setTrust = (on: boolean) =>
    onPatch(
      { dhcp_snooping: on },
      on
        ? t("switchDetailPage.snoopTrustedToast", { name: pr.name })
        : t("switchDetailPage.snoopUntrustedToast", { name: pr.name }),
    );

  // THE HISTORY OF BOTH DIRECTIONS, MEASURED.
  //
  // It used to be MANUFACTURED: thirty points generated by a deterministic
  // jitter around the current value, drawing a plausible-looking curve for
  // a past that had never actually been measured. It now comes from the
  // time-series store (GET /api/devices/{id}/telemetry), i.e. real
  // equipment counters.
  //
  // An unreachable switch keeps its curve: that's what happened BEFORE it
  // went down, and that's exactly what we come here to look at. Only the
  // "now" value is forced to zero, because no traffic flows through
  // silent equipment.
  const rxHist = traffic.rx;
  const txHist = traffic.tx;
  const lastNum = (xs: (number | null)[]) => {
    var i = xs.length - 1;
    for (; i >= 0; i--) {
      if (xs[i] != null) return xs[i] as number;
    }
    return null;
  };
  const rxNow = off ? 0 : (lastNum(rxHist) ?? p.rx_mbps ?? 0);
  const txNow = off ? 0 : (lastNum(txHist) ?? p.tx_mbps ?? 0);
  const hasHist =
    rxHist.some((v) => v != null) || txHist.some((v) => v != null);

  const tagged = p.tagged ?? [];
  const toggleTrunkMember = (vid: number) => {
    let next: number[] =
      tagged === "all" ? vlans.map((v) => v.vlan_id) : (tagged as number[]).slice();
    const i = next.indexOf(vid);
    if (i > -1) next.splice(i, 1);
    else next.push(vid);
    next.sort((a, b) => a - b);
    patchManual({ tagged: next });
  };

  const stpAny =
    !!dev.stp_caps?.edge || !!dev.stp_caps?.port_enable || !!dev.stp_caps?.bpdu_guard;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {/* ============================ 1. INFO ============================ */}
      <SidePanelGroup
        title={t("switchDetailPage.sectionInfo")}
        // The port NAME lives in the meta, not only in the body: this is the
        // one section whose fold must never leave the panel anonymous, and an
        // operator who collapsed everything still has to know which jack the
        // four sections below are about.
        meta={pr.name + " · " + (off
          ? t("switchDetailPage.switchUnreachableShort")
          : p.link || t("switchDetailPage.noLinkDetected"))}
        defaultOpen
      >
        <div>
          <div className="kicker">{dev.name} · {pr.name} · {pr.speed}</div>
          {ing && !off ? (
            <input
              className="port-label-input"
              value={p.label ?? ""}
              placeholder={t("switchDetailPage.portLabelPlaceholder")}
              onChange={(e) => onPatch({ label: e.target.value })}
            />
          ) : (
            <div style={{ fontSize: 16, fontWeight: 700, marginTop: 8 }}>
              {p.label || pr.name + t("switchDetailPage.portFreeSuffix")}
            </div>
          )}
        </div>

        <div className="kv" style={{ marginTop: 12 }}>
          <span className="k">{t("switchDetailPage.linkLabel")}</span>
          <span style={{ color: off ? "var(--text-dim)" : p.link ? "var(--ok)" : "var(--text-dim)", fontWeight: 600 }}>
            {off ? t("switchDetailPage.switchUnreachableShort") : p.link ? p.link + t("switchDetailPage.linkFullDuplex") : t("switchDetailPage.noLinkDetected")}
          </span>
          {/* The ADMIN state, from the connector's read, next to the live link
              speed and deliberately not merged with it: SNMP cannot tell an
              err-disabled port from a plain down one, and "the switch shut this
              itself" is the single most useful sentence on a dead jack. Shown
              only when it adds something the line above does not already say. */}
          {!off && p.link_state && p.link_state !== "up" && (
            <>
              <span className="k">{t("switchDetailPage.adminStateLabel")}</span>
              <span style={{ color: p.link_state === "err-disabled" ? "var(--danger)" : undefined }}>
                {linkStateLabel(p.link_state, t)}
                {p.err_reason ? " — " + p.err_reason : ""}
              </span>
            </>
          )}
          <span className="k">PoE</span>
          <span>
            {!poeCapable
              ? t("switchDetailPage.poeNotAvailable")
              : (poeObs?.power_w ?? p.poe_w ?? 0) > 0
                ? String(poeObs?.power_w ?? p.poe_w).replace(".", ",") + t("switchDetailPage.poeActiveWatts")
                : p.poe
                  ? t("switchDetailPage.poeEnabledWaiting")
                  : t("switchDetailPage.poeOff")}
          </span>
          <span className="k">{t("switchDetailPage.trafficLabel")}</span>
          <span style={{ whiteSpace: "pre" }}>{"↓ " + fmtMbps(rxNow) + "  ↑ " + fmtMbps(txNow)}</span>
        </div>

        <div style={{ marginTop: 12 }}>
          <div className="kicker" style={{ marginBottom: 4 }}>
            {t("switchDetailPage.portTrafficTitle")}
          </div>
          {hasHist ? (
            <LineChart
              height={132}
              series={[
                { name: t("switchDetailPage.chartIncoming"), color: SERIES.in, points: rxHist },
                { name: t("switchDetailPage.chartOutgoing"), color: SERIES.out, points: txHist },
              ]}
              format={fmtMbps}
              xLabel={({ rawIndex, rawTotal, newest }) => {
                if (newest) return t("switchDetailPage.chartNow");
                const back = (rawTotal - 1 - rawIndex) * stepSeconds;
                if (back < 90) return t("switchDetailPage.chartSecondsAgo", { n: Math.round(back) });
                if (back < 5400) return t("switchDetailPage.chartMinutesAgo", { n: Math.round(back / 60) });
                if (back < 48 * 3600) return t("switchDetailPage.chartHoursAgo", { n: Math.round(back / 3600) });
                return t("switchDetailPage.chartDaysAgo", { n: Math.round(back / 86400) });
              }}
            />
          ) : (
            <div className="note">
              {t("switchDetailPage.noTrafficNote")}
            </div>
          )}
        </div>

        {/* THE PER-CLASS STACK, directly under the traffic chart it decomposes
            (ADR-0028). Same window, same axis vocabulary, same port — so the
            two read as one story: the chart above says how much, this one says
            how much of it is flooding.

            It sits INSIDE the Info section rather than in a group of its own
            because it answers the same question the section already opens with
            and a collapsed group would hide the one number that explains a
            saturated port. */}
        <div className="portclass-block">
          <div className="kicker portclass-kicker">
            {t("switchDetailPage.portClassTitle")}
          </div>
          <PortClassChart bands={classes} stepSeconds={stepSeconds} />
        </div>

        {off && (
          <div className="note" style={{ border: "1px solid var(--border)", padding: "10px 12px", borderRadius: 8, marginTop: 12 }}>
            {t("switchDetailPage.unreachableConfigNote")}
          </div>
        )}
      </SidePanelGroup>

      {/* ========================== 2. PROFILE ===========================
          The group lane, and the group/manual selector that decides which of
          the two lanes carries CONTROLS. The selector stays here, with the
          group it selects away from: it is the question "does this port live by
          a group?", and the section that answers it is the right place to ask.
          Flipping it still writes nothing. */}
      {!off && canApply && (
        <SidePanelGroup
          title={t("switchDetailPage.sectionProfile")}
          meta={
            followed
              ? followed.name
              : dangling
                ? t("switchDetailPage.profileMetaDangling")
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
          {/* The selector is an ENGINEER control: technicians apply groups and
              nothing else, so for them the group lane simply IS the section. */}
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
              {/* Which group the port follows is the FIRST thing this lane
                  says — including the case where the answer is a tombstone. */}
              {followed && (
                <div className="note" style={{ margin: "2px 0 8px" }}>
                  {t("switchDetailPage.groupFollowedNote", { name: followed.name })}
                </div>
              )}
              {dangling && (
                <div className="note" style={{ margin: "2px 0 8px", color: "var(--warn)" }}>
                  {t("switchDetailPage.groupDanglingNote")}
                </div>
              )}
              {!followed && !dangling && (
                <div className="note" style={{ margin: "2px 0 8px" }}>
                  {t("switchDetailPage.groupNoneNote")}
                </div>
              )}
              <div style={{ display: "flex", flexDirection: "column", gap: 5 }}>
                {profiles.map((prof) => {
                  const spec = prof.spec as unknown as PortProfileSpec;
                  const locked = !!spec.eng_only && !ing;
                  const selp = selProf === prof.id;
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
                      className={"prof-row" + (selp ? " sel" : "")}
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
                      {followed?.id === prof.id && (
                        <span style={{ color: "var(--ok)", fontWeight: 700, fontSize: 11 }}>
                          ✓ {t("switchDetailPage.groupFollowedBadge")}
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
                {t("switchDetailPage.applyProfileBtn")}
              </ActionButton>
              {!ing && (
                <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
                  {t("switchDetailPage.engineerProfilesNote")}
                </div>
              )}
            </>
          ) : (
            // Manual lane selected: the profile list is deliberately gone rather
            // than greyed out. Its buttons ATTACH the port to a group, which is
            // the opposite of what the operator just asked for, and leaving them
            // clickable next to the manual controls is how a port gets
            // re-stamped by accident. The pointer to where the values are is
            // enough.
            <div className="note" style={{ marginTop: 8 }}>
              {t("switchDetailPage.profileManualLaneNote")}
            </div>
          )}
        </SidePanelGroup>
      )}

      {/* ============================ 3. VLAN ============================
          The per-port values. Controls when the manual lane is the live one,
          the same values READ-ONLY otherwise — a port that follows a group
          still has a PVID, and an operator opening this section to check it
          must not find an empty box. */}
      <SidePanelGroup
        title={t("switchDetailPage.sectionVlan")}
        meta={vlanMeta(p.mode, p.pvid, t)}
        defaultOpen={false}
      >
        {manualLane ? (
          <>
            {followed && (
              <div className="note" style={{ margin: "0 0 8px" }}>
                {t("switchDetailPage.manualDetachNote", { name: followed.name })}
              </div>
            )}
            <div className="seg3">
              {(["access", "trunk", "libre"] as const).map((m) => (
                <button
                  key={m}
                  className={p.mode === m ? "on" : ""}
                  onClick={() =>
                    patchManual(
                      m === "trunk"
                        ? { mode: m, tagged: tagged === "all" || (Array.isArray(tagged) && tagged.length) ? tagged : p.pvid != null ? [p.pvid] : [] }
                        : { mode: m },
                    )
                  }
                >
                  {m === "access" ? t("switchDetailPage.modeAccess") : m === "trunk" ? t("switchDetailPage.modeTrunk") : t("switchDetailPage.modeFree")}
                </button>
              ))}
            </div>

            {p.mode === "access" && (
              <div className="vlan-pick">
                {vlans.map((v) => (
                  <button
                    key={v.id}
                    className={"vlan-row" + (p.pvid === v.vlan_id ? " sel" : "")}
                    onClick={() => patchManual({ mode: "access", pvid: v.vlan_id, tagged: [] })}
                  >
                    <span className="vlan-chip" style={{ background: v.color, margin: 0 }} />
                    <span style={{ fontWeight: 600 }}>{v.name}</span>
                    <span className="note">{v.vlan_id}</span>
                  </button>
                ))}
              </div>
            )}

            {p.mode === "trunk" && (
              <>
                <div className="kicker" style={{ margin: "10px 0 6px" }}>{t("switchDetailPage.taggedMembersTitle")}</div>
                <div className="vlan-pick">
                  <button
                    className="vlan-row"
                    onClick={() =>
                      patchManual({ tagged: tagged === "all" ? (p.pvid != null ? [p.pvid] : []) : "all" })
                    }
                  >
                    <span
                      className="vlan-chip"
                      style={{ background: tagged === "all" ? "var(--accent)" : "transparent", border: "1px solid var(--border)", margin: 0 }}
                    />
                    <span style={{ fontWeight: 600 }}>{t("switchDetailPage.fullTrunkAllVlans")}</span>
                  </button>
                  {vlans.map((v) => {
                    const member = tagged === "all" || (Array.isArray(tagged) && tagged.includes(v.vlan_id));
                    return (
                      <button key={v.id} className="vlan-row" onClick={() => toggleTrunkMember(v.vlan_id)}>
                        <span
                          className="vlan-chip"
                          style={{ background: member ? v.color : "transparent", border: `1px solid ${v.color}`, margin: 0 }}
                        />
                        <span style={{ fontWeight: 600, color: member ? "var(--text)" : "var(--text-dim)" }}>
                          {v.name}
                        </span>
                        <span className="note">{v.vlan_id}</span>
                      </button>
                    );
                  })}
                </div>
                <div className="kicker" style={{ margin: "10px 0 5px" }}>{t("switchDetailPage.nativeVlanTitle")}</div>
                <select
                  value={p.pvid ?? ""}
                  onChange={(e) => patchManual({ pvid: e.target.value ? Number(e.target.value) : null })}
                >
                  <option value="">—</option>
                  {vlans.map((v) => (
                    <option key={v.id} value={v.vlan_id}>{v.vlan_id} — {v.name}</option>
                  ))}
                </select>
              </>
            )}
          </>
        ) : (
          <>
            <div className="kv">
              <span className="k">{t("switchDetailPage.modeLabel")}</span>
              <span style={{ fontWeight: 600 }}>
                {p.mode === "access"
                  ? t("switchDetailPage.modeAccessLabel")
                  : p.mode === "trunk"
                    ? t("switchDetailPage.modeTrunkLabel")
                    : t("switchDetailPage.modeFreeLabel")}
              </span>
              <span className="k">
                {p.mode === "trunk"
                  ? t("switchDetailPage.nativeVlanTitle")
                  : t("switchDetailPage.vlanLabel")}
              </span>
              <span>
                {p.pvid == null
                  ? "—"
                  : (vm.get(p.pvid)?.name ?? "VLAN " + p.pvid) + " (" + p.pvid + ")"}
              </span>
              {p.mode === "trunk" && (
                <>
                  <span className="k">{t("switchDetailPage.taggedMembersTitle")}</span>
                  <span>
                    {tagged === "all"
                      ? t("switchDetailPage.fullTrunkAllVlans")
                      : (tagged as number[]).length > 0
                        ? (tagged as number[]).join(", ")
                        : "—"}
                  </span>
                </>
              )}
            </div>
            <div className="note" style={{ marginTop: 8, fontSize: 10 }}>
              {off
                ? t("switchDetailPage.unreachableConfigNote")
                : ing
                  ? t("switchDetailPage.vlanManualElsewhereNote")
                  : t("switchDetailPage.vlanViewerNote")}
            </div>
          </>
        )}
      </SidePanelGroup>

      {/* =========================== 4. OPTIONS ==========================
          The per-port protections. All of them are OVERRIDES ON TOP of a group
          — touching one never detaches the port, unlike a VLAN edit — which is
          precisely why they had to stop being stacked under the VLAN lanes,
          where they read as more VLAN settings. */}
      <SidePanelGroup
        title={t("switchDetailPage.sectionOptions")}
        // The meta says something only when there IS something: a trusted port,
        // or a device whose snooping is off while a port claims trust. The
        // ordinary case (untrusted, protected) is silent — a badge on every port
        // would train the eye to ignore the one that matters.
        meta={
          trustIntent || trustObs
            ? snoop && !snoop.enabled
              ? t("switchDetailPage.snoopMetaTrustedInert")
              : t("switchDetailPage.snoopMetaTrusted")
            : undefined
        }
        defaultOpen={false}
      >
        {/* --- DHCP snooping trust ---------------------------------------- */}
        <div className="kicker" style={{ marginBottom: 6, display: "inline-flex", alignItems: "center", gap: 4 }}>
          {t("switchDetailPage.snoopTitle")}
          <HelpTip label={t("switchDetailPage.snoopHelpLabel")}>
            <p>{t("switchDetailPage.snoopHelp1")}</p>
            <p>{t("switchDetailPage.snoopHelp2")}</p>
            <p>{t("switchDetailPage.snoopHelp3")}</p>
            <p>{t("switchDetailPage.snoopHelp4")}</p>
          </HelpTip>
        </div>
        {/* THE DEVICE-WIDE FEATURE COMES FIRST, before the port's own flag.
            `enabled: false` means every trust flag on the box is INERT whatever
            it says — the switch inspects nothing — and a panel that showed the
            port flag without that sentence would be describing a protection
            that does not exist. That is the exact failure this control must not
            hide, so it is a warning and not a footnote. */}
        {/* THE UNREAD CASE IS AN OBSERVATION, NOT AN ERRAND (2026-10-25).
            It used to read "This device was never read for DHCP snooping:
            neither the switch-wide feature nor the port flags are known here.
            Re-read the device (⟳) to find out." — three clauses and an
            imperative, where the two branches below state their fact in one
            line. It now says the fact in the same grammar as its siblings
            ("Device-wide snooping: …"), reusing the one word the product
            already spends on an unseen value (`snoopObservedUnknown`, "not
            read"), and the explanation sits behind the `i` bubble: the glyph
            that announces a STATE rather than a lesson (components.tsx). The
            dim `.note` ink is the same `--text-dim` the device dot uses for
            "never observed" (UI-GUIDE §2) — the unknown mark we already own. */}
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
        {(snoop?.notes ?? []).map((n, i) => (
          // The connector's own words about what it understood, verbatim: these
          // sentences say things this type cannot carry ("no per-VLAN dimension
          // on this platform"), and a translation we invented would be a guess.
          <div key={i} className="note" style={{ marginBottom: 8 }}>{n}</div>
        ))}
        <div className="kv">
          <span className="k">{t("switchDetailPage.snoopObservedLabel")}</span>
          {/* One ink rule for this field, shared with the multi-port panel
              (trustObsInk, 2026-08-08). "trusted" used to be `--warn` here:
              a trusted uplink is a deliberate configuration, not an alert, and
              the sentence about the feature being off device-wide — above, in
              orange — is the one that does deserve the colour. */}
          <span style={{ fontWeight: 600, color: trustObsInk(trustToken(trustObs)) }}>
            {trustObs === undefined
              ? t("switchDetailPage.snoopObservedUnknown")
              : trustObs
                ? t("switchDetailPage.snoopObservedTrusted")
                : t("switchDetailPage.snoopObservedUntrusted")}
          </span>
          <span className="k">{t("switchDetailPage.snoopIntentLabel")}</span>
          <span>
            {trustIntent
              ? t("switchDetailPage.snoopObservedTrusted")
              : t("switchDetailPage.snoopObservedUntrusted")}
          </span>
        </div>
        {ing && !off && (
          <>
            <div className="row" style={{ marginTop: 8, gap: 6, alignItems: "center" }}>
              <div className="seg3" style={{ marginBottom: 0 }}>
                <button className={trustIntent ? "on" : ""} onClick={() => setTrust(true)}>
                  {t("switchDetailPage.snoopTrustedBtn")}
                </button>
                <button className={!trustIntent ? "on" : ""} onClick={() => setTrust(false)}>
                  {t("switchDetailPage.snoopUntrustedBtn")}
                </button>
              </div>
              {/* THE WRITE IS A SECOND, EXPLICIT CLICK — the same split as PoE
                  (stored intent through PUT /ports, wire write through its own
                  endpoint), but here the button is visible instead of implied.
                  Two reasons: no capability flag says in advance whether this
                  platform writes snooping, so a toggle that always pushed would
                  earn a 422 on every connector that only records the intent; and
                  the LABEL NAMES THE VALUE IT WILL SEND, so there is no window
                  in which the button carries a trust flag the screen no longer
                  shows. */}
              <ActionButton
                kind="apply"
                className="secondary"
                disabled={snoopWriting}
                title={t("switchDetailPage.snoopWriteTitle")}
                onClick={() => onSnoopWrite({ [pr.name]: trustIntent })}
              >
                {trustIntent
                  ? t("switchDetailPage.snoopWriteTrustedBtn")
                  : t("switchDetailPage.snoopWriteUntrustedBtn")}
              </ActionButton>
            </div>
            <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
              {t("switchDetailPage.snoopSplitNote")}
            </div>
          </>
        )}

        {/* --- Spanning tree and BPDU guard, same section: both are per-port
            protections against what is plugged in, and an operator arriving
            with "this jack must never influence the topology" needs them side
            by side. Unchanged behaviour — engineer only, not on a free port,
            and only the capabilities the connector declares. */}
        {!off && ing && p.mode !== "libre" && stpAny && (
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
                        className={String(p.stp?.edge ?? "") === e ? "on" : ""}
                        onClick={() =>
                          onPatch(
                            { stp: { edge: e } },
                            t("switchDetailPage.stpEdgeToast", { name: pr.name, edge: e }),
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
                      className={p.stp?.enabled !== false ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: true } },
                          t("switchDetailPage.stpPortOnToast", { name: pr.name }),
                        )
                      }
                    >
                      {t("switchDetailPage.stpPortOn")}
                    </button>
                    <button
                      className={p.stp?.enabled === false ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { enabled: false } },
                          t("switchDetailPage.stpPortOffToast", { name: pr.name }),
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
                      className={p.stp?.bpdu_guard === true ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: true } },
                          t("switchDetailPage.bpduGuardOnToast", { name: pr.name }),
                        )
                      }
                    >
                      {t("switchDetailPage.bpduGuardOn")}
                    </button>
                    <button
                      className={p.stp?.bpdu_guard === false ? "on" : ""}
                      onClick={() =>
                        onPatch(
                          { stp: { bpdu_guard: false } },
                          t("switchDetailPage.bpduGuardOffToast", { name: pr.name }),
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
          Shown only when this jack can source power at all — observed from the
          device when a read exists, faceplate flag otherwise. Telemetry rows
          render for every role; the on/off is an engineer control, and when the
          connector writes PoE the click reaches the hardware immediately
          (setPoE pushes the class alongside the stored intent). */}
      {!off && poeCapable && (
        <SidePanelGroup
          title={t("switchDetailPage.sectionPoe")}
          // Folded, the section still answers the two questions asked about a
          // powered jack: is it drawing, and does it disagree with its group.
          meta={
            poeOverride && followed
              ? t("switchDetailPage.poeMetaOverride")
              : poeObs?.delivering
                ? t("switchDetailPage.poeMetaWatts", {
                    w: String(poeObs.power_w ?? 0).replace(".", ","),
                  })
                : p.poe
                  ? t("switchDetailPage.poeEnabledWaiting")
                  : t("switchDetailPage.poeOff")
          }
          defaultOpen={false}
        >
          <div className="kicker" style={{ marginBottom: 8, display: "inline-flex", alignItems: "center", gap: 4 }}>
            PoE
            {/* Requirement-3 indicator: this port FORCES a PoE value that
                differs from what its group prescribes. The bubble shows both
                values and offers the one repair that makes sense — follow the
                group again. Rendered on the section title so it is visible
                even to roles that do not see the buttons. */}
            {poeOverride && followed && (
              <HelpTip
                icon="i"
                label={t("switchDetailPage.poeOverrideLabel")}
              >
                <p><b>{t("switchDetailPage.poeOverrideTitle")}</b></p>
                <p>{t("switchDetailPage.poeOverrideBody")}</p>
                <p>
                  {t("switchDetailPage.poeOverrideGroupValue", {
                    name: followed.name,
                    value: groupPoE ? t("switchDetailPage.poePoweredBtn") : t("switchDetailPage.poeCutBtn"),
                  })}
                  <br />
                  {t("switchDetailPage.poeOverridePortValue", {
                    value: p.poe ? t("switchDetailPage.poePoweredBtn") : t("switchDetailPage.poeCutBtn"),
                  })}
                </p>
                {ing ? (
                  <ActionButton
                    kind="apply"
                    className="secondary"
                    onClick={() =>
                      setPoE(!!groupPoE, t("switchDetailPage.poeOverrideRemovedToast", { name: pr.name, group: followed.name }))
                    }
                  >
                    {t("switchDetailPage.poeOverrideRemoveBtn")}
                  </ActionButton>
                ) : (
                  <p className="note">{t("switchDetailPage.poeOverrideEngNote")}</p>
                )}
              </HelpTip>
            )}
          </div>
          <div className="kv">
            <span className="k">{t("switchDetailPage.poeStatusLabel")}</span>
            <span>
              {poeObs?.delivering
                ? t("switchDetailPage.poeDelivering", {
                    w: String(poeObs.power_w ?? 0).replace(".", ","),
                  })
                : p.poe
                  ? t("switchDetailPage.poeEnabledWaiting")
                  : t("switchDetailPage.poeOff")}
            </span>
            <span className="k">{t("switchDetailPage.poeClassLabel")}</span>
            <span>{poeClassLabel(poeObs?.class, p.poe, t)}</span>
            {poeObs?.delivering && poeObs.negotiated_class ? (
              <>
                <span className="k">{t("switchDetailPage.poeNegotiatedLabel")}</span>
                <span>{poeObs.negotiated_class}</span>
              </>
            ) : null}
            {!poeObs?.delivering && poeObs?.detection ? (
              <>
                <span className="k">{t("switchDetailPage.poeDetectionLabel")}</span>
                {/* The platform's own wording, verbatim — "Open Circuit" from
                    the switch beats any translation we could guess. */}
                <span>{poeObs.detection}</span>
              </>
            ) : null}
          </div>
          {ing && p.mode !== "libre" && (
            <>
              <div className="seg3" style={{ marginTop: 10, marginBottom: 0 }}>
                <button
                  className={p.poe ? "on" : ""}
                  onClick={() => setPoE(true)}
                >
                  {t("switchDetailPage.poePoweredBtn")}
                </button>
                <button
                  className={!p.poe ? "on" : ""}
                  onClick={() => setPoE(false)}
                >
                  {t("switchDetailPage.poeCutBtn")}
                </button>
              </div>
              {!poeWritable && (
                <div className="note" style={{ fontSize: 10, marginTop: 6 }}>
                  {t("switchDetailPage.poeIntentOnlyNote")}
                </div>
              )}
            </>
          )}
        </SidePanelGroup>
      )}

      {/* ============================ 6. OPTICS ==========================
          The transceiver in this cage and what it measures. LAST, and after
          PoE rather than beside Info, because the ordering rule of this panel
          is "facts an operator did not have to ask for" at the top and
          "decisions they came to make" below — and this section is neither:
          it is a fact they came looking for, with no control to offer. Putting
          it second would push the profile lane, the one thing most sessions
          touch, down by a section.

          NOTHING HERE IS JUDGED. The module publishes alarm and warning bounds
          — the "DDM Thresholds" table — and the connector deliberately does
          not parse them (fs/s3400/optics.go), so the API cannot say whether a
          reading is in range. A green or amber number would be a verdict
          against a limit this product does not have; the raw value with its
          unit is the honest statement, and the help bubble says why there is
          no colour rather than letting the absence read as "all fine". */}
      {showOptics && (
        <SidePanelGroup
          title={t("switchDetailPage.sectionOptics")}
          // The folded line prefers the observation's answer; a device whose
          // levels arrive only as series (gNMI) has no observation, and the
          // freshest RX from the curve is the honest summary instead of
          // "never read".
          meta={
            cage.kind === "unread" && hasLevels
              ? (() => {
                  const rxLine = levels.find((l) => l.dir === "rx");
                  const last = rxLine?.points.reduce<number | null>(
                    (acc, v) => (v != null ? v : acc),
                    null,
                  );
                  return last != null
                    ? t("switchDetailPage.opticsMetaRx", {
                        rx: last.toFixed(1).replace(".", dsep) + " dBm",
                      })
                    : t("switchDetailPage.opticsMetaModule");
                })()
              : opticsMeta(cage, t, dsep)
          }
          defaultOpen={false}
        >
          <div className="kicker" style={{ marginBottom: 6, display: "inline-flex", alignItems: "center", gap: 4 }}>
            {t("switchDetailPage.opticsTitle")}
            <HelpTip label={t("switchDetailPage.opticsHelpLabel")}>
              <p>{t("switchDetailPage.opticsHelp1")}</p>
              <p>{t("switchDetailPage.opticsHelp2")}</p>
              <p>{t("switchDetailPage.opticsHelp3")}</p>
            </HelpTip>
          </div>
          <div className="kv">
            <span className="k">{t("switchDetailPage.opticsModuleLabel")}</span>
            {/* THE MODULE LINE IS THE CAGE'S STATE, and the answers are
                different things. An empty cage is an observation and takes no
                ink; a cage the read failed on is the one case
                `--indeterminate` exists for — it looks identical to an empty
                one in `ports`, and calling it empty would be inventing the
                emptiest reading there is. UNREAD is the fourth answer, newly
                reachable here since level curves open this section on devices
                the CLI reader never visits: a cage that streams levels
                obviously holds a module, and printing "empty" for it would be
                a false claim about hardware. */}
            <span style={{ fontWeight: 600, color: opticsCageInk(cage.kind) }}>
              {cage.kind === "module"
                ? [cage.module.vendor, cage.module.part_number]
                    .filter(Boolean)
                    .join(" ") || t("switchDetailPage.opticsModuleUnnamed")
                : cage.kind === "blind"
                  ? t("switchDetailPage.opticsBlind")
                  : cage.kind === "unread"
                    ? t("switchDetailPage.opticsMetaUnread")
                    : t("switchDetailPage.opticsEmpty")}
            </span>
            {cage.kind === "module" && cage.module.serial ? (
              <>
                <span className="k">{t("switchDetailPage.opticsSerialLabel")}</span>
                <span>{cage.module.serial}</span>
              </>
            ) : null}
            {/* The five readings, and the rule that governs all of them: an
                absent one is NEVER printed as a number. `0 dBm` is a legal
                transmit power and a legal receive power — the API keeps the
                two apart with a pointer, and `?? 0` here would put them back
                together on the one screen where the difference decides whether
                somebody drives to a rack. */}
            {cage.kind === "module" && (cage.module.ddm || opticsHasReading(cage.module))
              ? (
                  [
                    { key: "opticsTxLabel", value: cage.module.tx_power_dbm, unit: "dBm" },
                    { key: "opticsRxLabel", value: cage.module.rx_power_dbm, unit: "dBm" },
                    { key: "opticsTempLabel", value: cage.module.temperature_c, unit: "°C" },
                    { key: "opticsVoltLabel", value: cage.module.voltage_v, unit: "V" },
                    { key: "opticsBiasLabel", value: cage.module.bias_current_ma, unit: "mA" },
                  ] as const
                ).map((r) => (
                  <Fragment key={r.key}>
                    <span className="k">{t("switchDetailPage." + r.key)}</span>
                    <span style={{ color: opticsReadingInk(r.value, cage.module.ddm) }}>
                      {opticsReading(r.value, r.unit, dsep) ??
                        t("switchDetailPage.opticsNoReading")}
                    </span>
                  </Fragment>
                ))
              : null}
          </div>
          {/* A module that answers "DDM:NO" is not a fault and not a lost
              vantage point: it is a transceiver without the hardware to
              measure itself, permanent for as long as it sits in this cage,
              and calling for no action. Neutral text, per UI-GUIDE §2's
              "this platform cannot do it" row. */}
          {cage.kind === "module" && !cage.module.ddm && (
            <div className="note" style={{ marginTop: 8 }}>
              {t("switchDetailPage.opticsNoDdmNote")}
            </div>
          )}
          {cage.kind === "blind" && (
            <>
              <div className="note" style={{ marginTop: 8, color: "var(--indeterminate)" }}>
                {t("switchDetailPage.opticsBlindNote")}
              </div>
              {/* The connector's own words, verbatim — a timeout and a refused
                  session are different site visits, and a sentence we invented
                  would flatten them. */}
              {cage.reason ? (
                <div className="note" style={{ marginTop: 4 }}>{cage.reason}</div>
              ) : null}
            </>
          )}

          {/* THE LEVEL HISTORY — the reason this section stopped being a
              snapshot. The instantaneous readings above say where the fibre is
              NOW; the curve says whether it has moved, which is the question
              that decides between "long fibre doing fine" and "connector
              slowly dying". Same window and pitch as the traffic chart above:
              one time axis per panel, so the eye can correlate a level drop
              with a traffic drop. */}
          {hasLevels && (
            <div style={{ marginTop: 12 }}>
              <div className="kicker" style={{ marginBottom: 4 }}>
                {t("switchDetailPage.opticsLevelsTitle")}
              </div>
              <LevelChart lines={levels} stepSeconds={stepSeconds} />
            </div>
          )}
        </SidePanelGroup>
      )}
    </div>
  );
}

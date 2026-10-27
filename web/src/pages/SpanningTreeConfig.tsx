import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { get, put } from "../api";
import { useSiteId } from "../useSite";
import { useSiteRole } from "../auth";
import { ActionButton, ErrorMsg, Field, HelpTip, useToast } from "../components";
import { PillMultiSelect } from "../PillMultiSelect";
import type { Device } from "../api";
import { instanceLabel, rootVerdict, type RootBridgeReply } from "./stpRootBridge";

type STPPreferenceReply = {
  catalog: string[];
  preference: string[];
};

/**
 * The MST region identity of the site, as the controller would apply it.
 *
 * The three fields are ONE value: 802.1s compares the name, the revision and a
 * digest over the whole VLAN-to-instance table, and a bridge differing in any of
 * them is in a different region (cf. ADR-0020). They are therefore rendered
 * together and never separately — a screen showing only the name invites reading
 * the name AS the region, which is the misconception that splits a fabric.
 */
type STPMSTRegionReply = {
  name: string;
  revision: number;
  instances: { instance: number; vlans: number[] }[];
  /** The name comes from the site plan, not from the site's immutable id. */
  pinned: boolean;
};

const LABELS: Record<string, string> = {
  mstp: "MSTP",
  "rstp-pvst": "RSTP-PVST",
  rstp: "RSTP",
  stp: "STP",
  none: "none",
};

/** Configuration → Spanning-Tree: ordered mode preference for the site. */
export default function SpanningTreeConfig() {
  const { t } = useTranslation();
  const siteId = useSiteId();
  const role = useSiteRole(siteId);
  const canEdit = role === "ing";
  const qc = useQueryClient();
  const [toast, showToast] = useToast();
  const [selAvail, setSelAvail] = useState<string | null>(null);
  const [selPref, setSelPref] = useState<string | null>(null);
  // Local draft so Add/Drop update the lists immediately (PUT is async).
  const [draft, setDraft] = useState<string[] | null>(null);

  const { data, error, isLoading } = useQuery({
    queryKey: ["stp-preference", siteId],
    queryFn: () =>
      get<STPPreferenceReply>(`/api/sites/${siteId}/stp-preference`),
    enabled: !!siteId,
  });

  useEffect(() => {
    if (data) setDraft(data.preference);
  }, [data]);

  // Read-only, and computed from the database rather than read off the hardware:
  // no session is opened on any switch to render it.
  const region = useQuery({
    queryKey: ["stp-mst-region", siteId],
    queryFn: () =>
      get<STPMSTRegionReply>(`/api/sites/${siteId}/stp-mst-region`),
    enabled: !!siteId,
  });

  const catalog = data?.catalog ?? ["mstp", "rstp-pvst", "rstp", "stp", "none"];
  const preference = draft ?? data?.preference ?? [];
  const available = useMemo(
    () => catalog.filter((m) => !preference.includes(m)),
    [catalog, preference],
  );

  // Keep selection pointing at a real option. A controlled <select value=""> with
  // no empty <option> makes the browser highlight the first row without firing
  // onChange — so Add/Drop stay disabled until another row is clicked.
  useEffect(() => {
    if (selAvail && !available.includes(selAvail)) {
      setSelAvail(available[0] ?? null);
    }
  }, [available, selAvail]);
  useEffect(() => {
    if (selPref && !preference.includes(selPref)) {
      setSelPref(preference[0] ?? null);
    }
  }, [preference, selPref]);

  const save = useMutation({
    mutationFn: (preference: string[]) =>
      put<STPPreferenceReply>(`/api/sites/${siteId}/stp-preference`, {
        preference,
      }),
    onSuccess: (r) => {
      setDraft(r.preference);
      qc.setQueryData(["stp-preference", siteId], r);
      showToast(t("spanningTreePage.saved"));
    },
    onError: () => {
      // Roll back the optimistic draft to the last server value.
      if (data) setDraft(data.preference);
    },
  });

  // --- Root bridges -------------------------------------------------------
  //
  // A SECOND root is not a second independent choice: it changes what the FIRST
  // one means, and it makes meshcraft filter BPDUs on the path between them.
  // That is why the designation lives here, on the site screen, where both are
  // visible together — and not as a checkbox on each device page, where an
  // operator could make the change without ever seeing the pair.
  const devices = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
  });
  const rootsQ = useQuery({
    queryKey: ["stp-root-bridges", siteId],
    queryFn: () => get<RootBridgeReply>(`/api/sites/${siteId}/stp-root-bridges`),
    enabled: !!siteId,
  });
  const saveRoots = useMutation({
    mutationFn: (roots: RootBridgeReply["roots"]) =>
      put<RootBridgeReply>(`/api/sites/${siteId}/stp-root-bridges`, { roots }),
    onSuccess: (r) => {
      qc.setQueryData(["stp-root-bridges", siteId], r);
      showToast(t("spanningTreePage.rootsSaved"));
    },
  });
  const roots = rootsQ.data?.roots ?? [];
  const verdict = rootVerdict(rootsQ.data);

  const move = (next: string[]) => {
    if (!canEdit) return;
    setDraft(next);
    save.mutate(next);
  };

  const add = (mode?: string | null) => {
    const m = mode ?? selAvail;
    if (!m || preference.includes(m)) return;
    const nextAvail = available.filter((x) => x !== m);
    move([...preference, m]);
    setSelAvail(nextAvail[0] ?? null);
  };
  const drop = (mode?: string | null) => {
    const m = mode ?? selPref;
    if (!m) return;
    const nextPref = preference.filter((x) => x !== m);
    move(nextPref);
    setSelPref(nextPref[0] ?? null);
  };
  const up = () => {
    if (!selPref) return;
    const i = preference.indexOf(selPref);
    if (i <= 0) return;
    const next = [...preference];
    [next[i - 1], next[i]] = [next[i], next[i - 1]];
    move(next);
  };
  const down = () => {
    if (!selPref) return;
    const i = preference.indexOf(selPref);
    if (i < 0 || i >= preference.length - 1) return;
    const next = [...preference];
    [next[i], next[i + 1]] = [next[i + 1], next[i]];
    move(next);
  };

  return (
    <div className="page">
      <h1>{t("spanningTreePage.title")}</h1>
      <p className="note">{t("spanningTreePage.help")}</p>
      {!canEdit && (
        <p className="note">{t("spanningTreePage.readOnly")}</p>
      )}
      {error && <ErrorMsg error={error} />}
      {save.error && <ErrorMsg error={save.error} />}
      {isLoading && <div className="note">{t("spanningTreePage.loading")}</div>}
      {data && (
        <div className={"stp-dual" + (canEdit ? "" : " stp-dual--readonly")}>
          <div className="stp-col">
            <div className="stp-col-head">
              <span className="kicker">{t("spanningTreePage.available")}</span>
              <span className="stp-col-count">{available.length}</span>
            </div>
            <select
              className={"stp-list" + (available.length === 0 ? " stp-list--empty" : "")}
              size={8}
              value={selAvail && available.includes(selAvail) ? selAvail : ""}
              onChange={(e) => setSelAvail(e.target.value || null)}
              onDoubleClick={(e) => {
                const v = (e.target as HTMLOptionElement).value;
                if (v) add(v);
              }}
              disabled={!canEdit}
              aria-label={t("spanningTreePage.available")}
            >
              {available.length === 0 && (
                <option value="" disabled>
                  {t("spanningTreePage.emptyAvailable")}
                </option>
              )}
              {available.map((m) => (
                <option
                  key={m}
                  value={m}
                  onMouseDown={() => {
                    if (canEdit) setSelAvail(m);
                  }}
                >
                  {LABELS[m] ?? m}
                </option>
              ))}
            </select>
          </div>
          <div className="stp-actions" role="group" aria-label={t("spanningTreePage.title")}>
            <ActionButton
              kind="add"
              className="secondary stp-action-btn"
              disabled={!canEdit || !selAvail}
              onClick={() => add()}
            >
              {t("spanningTreePage.add")} →
            </ActionButton>
            <ActionButton
              kind="confirm"
              className="secondary stp-action-btn"
              disabled={!canEdit || !selPref}
              onClick={() => drop()}
            >
              ← {t("spanningTreePage.drop")}
            </ActionButton>
            <div className="stp-actions-sep" aria-hidden />
            <ActionButton
              kind="confirm"
              className="secondary stp-action-btn"
              disabled={!canEdit || !selPref}
              onClick={up}
            >
              ↑ {t("spanningTreePage.up")}
            </ActionButton>
            <ActionButton
              kind="confirm"
              className="secondary stp-action-btn"
              disabled={!canEdit || !selPref}
              onClick={down}
            >
              ↓ {t("spanningTreePage.down")}
            </ActionButton>
          </div>
          <div className="stp-col stp-col--preferred">
            <div className="stp-col-head">
              <span className="kicker">{t("spanningTreePage.preferred")}</span>
              <span className="stp-col-count">{preference.length}</span>
            </div>
            <select
              className={"stp-list stp-list--preferred" + (preference.length === 0 ? " stp-list--empty" : "")}
              size={8}
              value={selPref && preference.includes(selPref) ? selPref : ""}
              onChange={(e) => setSelPref(e.target.value || null)}
              onDoubleClick={(e) => {
                const v = (e.target as HTMLOptionElement).value;
                if (v) drop(v);
              }}
              disabled={!canEdit}
              aria-label={t("spanningTreePage.preferred")}
            >
              {preference.length === 0 && (
                <option value="" disabled>
                  {t("spanningTreePage.emptyPreferred")}
                </option>
              )}
              {preference.map((m, i) => (
                <option
                  key={m}
                  value={m}
                  onMouseDown={() => {
                    if (canEdit) setSelPref(m);
                  }}
                >
                  {i + 1}. {LABELS[m] ?? m}
                </option>
              ))}
            </select>
            {/*
              THE EMPTY LIST IS A SETTING, NOT A HOLE TO FILL. An empty Preferred
              is the per-site off switch (api/stp_apply.go, stpPreferenceEmptyNote):
              applies stop sending the mode, the MST region AND every per-port
              intent. Both facts are invisible on the equipment — a device evicted
              from its MST region keeps forwarding and keeps reading `ok` — so this
              screen is where they get said.
            */}
            {preference.length === 0 && (
              <p className="note">{t("spanningTreePage.emptyPreferredWarning")}</p>
            )}
          </div>
        </div>
      )}

      {/*
        THE MST REGION IDENTITY, and until ADR-0020 it appeared on NO screen.

        802.1s decides region membership on three values — name, revision, and an
        MD5 digest over the whole 4096-entry VLAN-to-instance table — and two
        bridges share a region only if all three match exactly. A device whose
        copy differs has not been relabelled: it has LEFT the region, and the
        failure is silent. It keeps forwarding, it keeps reading `ok`, and the
        only symptom is a port blocking somewhere nobody expected.

        That is exactly what happened on ARENA: a site rename left seven Instant
        On switches in region `site-exec` and one in `arena`, two regions where
        the design says one, unnoticed for weeks because no screen rendered the
        value. An identity the topology depends on that nobody can see is an
        identity nobody verifies — hence this block.

        Shown only when the site actually prefers MSTP. Under RSTP or STP a region
        is written nowhere and read by nothing, and displaying it then would be
        the same non-event noise this commit removes from the connector notes.
      */}
      {region.data && preference.includes("mstp") && (
        <>
          <h2>{t("spanningTreePage.mstTitle")}</h2>
          <p className="note">
            {t("spanningTreePage.mstHelp")}{" "}
            <HelpTip label={t("spanningTreePage.mstTitle") as string}>
              <p>{t("spanningTreePage.mstTipIdentity")}</p>
              <p>{t("spanningTreePage.mstTipRename")}</p>
              <p>{t("spanningTreePage.mstTipVlans")}</p>
            </HelpTip>
          </p>
          <table className="table">
            <tbody>
              <tr>
                <th>{t("spanningTreePage.mstName")}</th>
                <td>
                  <code>{region.data.name || "—"}</code>{" "}
                  {/* Pinned and derived names have different futures, and the
                      operator needs to know which one they are looking at: a pin
                      is a decision somebody made and can unmake, a derived name
                      cannot drift at all. */}
                  <span className="note">
                    {region.data.pinned
                      ? t("spanningTreePage.mstNamePinned")
                      : t("spanningTreePage.mstNameDerived")}
                  </span>
                </td>
              </tr>
              <tr>
                <th>{t("spanningTreePage.mstRevision")}</th>
                <td>{region.data.revision}</td>
              </tr>
              {region.data.instances.map((inst) => (
                <tr key={inst.instance}>
                  <th>
                    {t("spanningTreePage.mstInstance", { id: inst.instance })}
                  </th>
                  <td className="scroll-x">
                    <code>{inst.vlans.join(", ") || "—"}</code>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {/* NOT a survey. This is what the controller WOULD apply, computed from
              the site's VLAN list — a device that has not been re-pushed since the
              list last changed carries something else, and that difference is the
              whole hazard. Saying so is the difference between a reference value
              and a false confirmation. */}
          <p className="note">{t("spanningTreePage.mstNotObserved")}</p>
        </>
      )}

      <h2>{t("spanningTreePage.rootsTitle")}</h2>
      <p className="note">{t("spanningTreePage.rootsHelp")}</p>
      {rootsQ.error && <ErrorMsg error={rootsQ.error} />}
      {saveRoots.error && <ErrorMsg error={saveRoots.error} />}

      {/* THE RED FLAG. Shown whenever two devices contest ONE tree — not
          whenever there are two roots, which on different MST instances is
          ordinary, correct MST. The warning text comes from the server
          VERBATIM: it states the assumption about physical cabling the operator
          is accepting, and it must read identically here, in the API response
          and in the apply transcript. */}
      {verdict.kind === "contested" && (
        <div className="error" role="alert">
          <strong>{t("spanningTreePage.rootsDanger")}</strong>
          <p>{verdict.warning}</p>
          <p className="note">{verdict.applyOrder}</p>
          {verdict.noKnownPath ? (
            /* NOT "0 ports". meshcraft can see no path between these roots, so
               it will filter NOTHING and they may still be one tree through a
               cable it cannot see. Collapsed into a zero this reads as
               "nothing to do", which is the opposite of what it means. */
            <p>{t("spanningTreePage.rootsNoPath")}</p>
          ) : (
            <p>
              {t("spanningTreePage.rootsWillFilter", {
                ports: verdict.filteredPorts,
                devices: verdict.filteredDevices,
              })}
            </p>
          )}
        </div>
      )}
      {verdict.kind === "multi-instance" && (
        <p className="note">
          {t("spanningTreePage.rootsMultiInstance", { count: verdict.roots })}
        </p>
      )}

      {devices.data && (
        <>
          {/* THE SELECTION IS THE DISPLAY.
              This was a checkbox per device in a table of the whole fleet, which
              reads well when the answer is "most of them" and badly when it is
              "one, occasionally two" — which is what root bridges are. An
              operator had to scan forty rows to find the one ticked box. Here
              what was chosen is the only thing occupying space, and the fleet
              appears only while somebody is choosing. */}
          <Field label={t("spanningTreePage.rootsDevice") as string}>
            <PillMultiSelect
              options={devices.data.map((d) => ({
                id: d.id,
                label: d.name,
                hint: d.model || undefined,
              }))}
              selected={roots.map((r) => r.device_id)}
              onChange={(ids) =>
                saveRoots.mutate(
                  // Instances are PRESERVED for a device that already had them:
                  // rebuilding every entry from the id alone would silently
                  // demote a per-instance root to the CIST, which is the one
                  // thing this control must not do quietly.
                  ids.map(
                    (id) =>
                      roots.find((r) => r.device_id === id) ?? { device_id: id },
                  ),
                )
              }
              placeholder={t("spanningTreePage.rootsPlaceholder") as string}
              disabled={!canEdit || saveRoots.isPending}
              ariaLabel={t("spanningTreePage.rootsDevice") as string}
            />
          </Field>

          {/* What each designated root is root OF, and what it will cost in
              filtered ports. Only the CHOSEN devices appear: a row per fleet
              member said nothing for the ones nobody picked. */}
          {roots.length > 0 && (
            <table className="table">
              <thead>
                <tr>
                  <th>{t("spanningTreePage.rootsDevice")}</th>
                  <th>{t("spanningTreePage.rootsInstances")}</th>
                  <th>{t("spanningTreePage.rootsFiltered")}</th>
                </tr>
              </thead>
              <tbody>
                {roots.map((r) => {
                  const dev = devices.data?.find((d) => d.id === r.device_id);
                  const filtered = rootsQ.data?.filter_ports?.[r.device_id] ?? [];
                  return (
                    <tr key={r.device_id}>
                      <td>{dev?.name ?? r.device_id}</td>
                      <td>{instanceLabel(r.instances)}</td>
                      <td>
                        {filtered.length > 0 ? (
                          /* Named BEFORE anything is written, which is the
                             point: these interfaces stop taking part in spanning
                             tree, and discovering that in an apply transcript
                             would be too late to object. */
                          <span className="warn">{filtered.join(", ")}</span>
                        ) : (
                          <span className="note">—</span>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          )}
        </>
      )}
      {toast}
    </div>
  );
}

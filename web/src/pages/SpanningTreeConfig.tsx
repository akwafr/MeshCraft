import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { get, put } from "../api";
import { useSiteId } from "../useSite";
import { useSiteRole } from "../auth";
import { ActionButton, ErrorMsg, useToast } from "../components";

type STPPreferenceReply = {
  catalog: string[];
  preference: string[];
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
          </div>
        </div>
      )}
      {toast}
    </div>
  );
}

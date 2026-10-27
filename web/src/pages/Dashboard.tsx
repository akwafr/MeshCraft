/**
 * Dashboards: the site's arrangements of tiles, and the screen that edits them.
 *
 * This page replaces two fixed ones — the old Dashboard and the old Monitoring,
 * which said nearly the same thing in a different order and could not be
 * changed by the person looking at them. Both survive as the two dashboards
 * every site is seeded with, and both are ordinary data from the first second:
 * renamable, rearrangeable, deletable.
 *
 * # Three responsibilities, and only three
 *
 * Choosing which dashboard is shown, editing the arrangement, and managing the
 * public link. Everything else lives elsewhere on purpose: the tiles are in
 * dashboardTiles.tsx, the grid and its gestures in dashboardGrid.tsx, the
 * arithmetic in dashboardLayout.ts (where a test can reach it), and the fetching
 * in dashboardData.ts.
 */
import { useEffect, useMemo, useState } from "react";
import { viewerDate } from "../ui/clock";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import { ActionButton } from "../ActionButton";
import { useSiteRole } from "../auth";
import { ErrorMsg, Field, Modal } from "../components";
import {
  createDashboard,
  deleteDashboard,
  listDashboards,
  moveDashboard,
  publicDashboardPath,
  renameDashboard,
  saveDashboardTiles,
  shareDashboard,
  unshareDashboard,
  type Dashboard as DashboardRec,
} from "../dashboardApi";
import { useDashboardData } from "../dashboardData";
import DashboardGrid from "../dashboardGrid";
import { addTile, sameLayout, type Tile } from "../dashboardLayout";
import { TILES, TILE_TYPES } from "../dashboardTiles";
import { useBreakpoint } from "../ui/breakpoint";
import { useSite, useSiteId } from "../useSite";

/** Which dashboard was last open, per site. A tab bar that always reopened on
 *  the first one would make the second dashboard cost a click, forever. */
const LAST_DASHBOARD_KEY = "meshcraft.lastDashboard";

function rememberDashboard(siteId: string, id: string) {
  try {
    localStorage.setItem(LAST_DASHBOARD_KEY + "." + siteId, id);
  } catch {
    /* private mode, quota — losing a preference is not worth an error */
  }
}

function recallDashboard(siteId: string): string {
  try {
    return localStorage.getItem(LAST_DASHBOARD_KEY + "." + siteId) ?? "";
  } catch {
    return "";
  }
}

export default function Dashboard() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const role = useSiteRole(siteId);
  const canWrite = role === "ing" || role === "tech";
  // Publishing a site's state without a login is not the same decision as
  // moving a tile: the share panel is site_admin only, and the server checks it
  // again (handleShareDashboard).
  const canShare = role === "ing";
  const qc = useQueryClient();

  const { data: dashboards, error } = useQuery({
    queryKey: ["dashboards", siteId],
    queryFn: () => listDashboards(siteId),
    enabled: !!siteId,
  });

  const [selectedId, setSelectedId] = useState("");
  const selected = useMemo<DashboardRec | undefined>(() => {
    if (!dashboards || dashboards.length === 0) return undefined;
    return (
      dashboards.find((d) => d.id === selectedId) ??
      dashboards.find((d) => d.id === recallDashboard(siteId)) ??
      dashboards[0]
    );
  }, [dashboards, selectedId, siteId]);

  useEffect(() => {
    if (selected && selected.id !== selectedId) setSelectedId(selected.id);
    if (selected && siteId) rememberDashboard(siteId, selected.id);
  }, [selected, selectedId, siteId]);

  // --- Local arrangement ---
  //
  // `local` is what the screen shows while it differs from the server. It is
  // NOT only an edit-mode thing: picking a traffic window or a log severity is
  // a tile option, so it changes the layout even outside edit mode. Whether
  // that change is ever written is a separate question — see the save button,
  // which appears only when the caller may write.
  const [local, setLocal] = useState<Tile[] | null>(null);
  const [editing, setEditing] = useState(false);
  // Under bp:narrow the grid is a stack and half of what the hint describes —
  // dragging, the resize corner, the arrows — does not exist. See the header
  // of dashboardGrid.tsx for why the spatial gestures are withdrawn there
  // rather than approximated.
  const narrow = useBreakpoint() === "narrow";
  const serverTiles = useMemo<Tile[]>(() => selected?.tiles ?? [], [selected]);
  const tiles = local ?? serverTiles;
  const dirty = !sameLayout(tiles, serverTiles);

  // Switching dashboards drops the local arrangement: carrying it across would
  // apply one dashboard's edits to another's tiles.
  useEffect(() => {
    setLocal(null);
    setEditing(false);
  }, [selected?.id]);

  const saveLayout = useMutation({
    mutationFn: (next: Tile[]) => saveDashboardTiles(selected?.id ?? "", next),
    onSuccess: () => {
      setLocal(null);
      qc.invalidateQueries({ queryKey: ["dashboards", siteId] });
    },
  });

  const create = useMutation({
    mutationFn: (name: string) => createDashboard(siteId, name, []),
    onSuccess: (d) => {
      setSelectedId(d.id);
      qc.invalidateQueries({ queryKey: ["dashboards", siteId] });
    },
  });

  const rename = useMutation({
    mutationFn: (v: { id: string; name: string }) => renameDashboard(v.id, v.name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dashboards", siteId] }),
  });

  const reorder = useMutation({
    mutationFn: (v: { id: string; position: number }) => moveDashboard(v.id, v.position),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["dashboards", siteId] }),
  });

  const remove = useMutation({
    mutationFn: (id: string) => deleteDashboard(id),
    onSuccess: () => {
      setSelectedId("");
      qc.invalidateQueries({ queryKey: ["dashboards", siteId] });
    },
  });

  const data = useDashboardData(siteId, tiles);

  const [renaming, setRenaming] = useState(false);
  const [nameDraft, setNameDraft] = useState("");
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [sharing, setSharing] = useState(false);

  if (!siteId) return <div className="empty">{t("common.loading")}</div>;

  return (
    <>
      {/* No page heading. The tab bar already says "Dashboard", and the
          dashboard bar below already highlights WHICH dashboard is open — the
          title was the third place saying it. */}
      <div className="toolbar toolbar--actions-end">
        <div className="row" style={{ gap: 8, flex: "none" }}>
          {canWrite && !editing && (
            <ActionButton kind="edit" className="secondary" onClick={() => setEditing(true)}>
              {t("dashboards.arrange")}
            </ActionButton>
          )}
          {canWrite && editing && (
            <ActionButton kind="finish" onClick={() => setEditing(false)}>
              {t("dashboards.doneArranging")}
            </ActionButton>
          )}
          {canWrite && dirty && (
            <>
              <ActionButton
                kind="save"
                disabled={saveLayout.isPending}
                onClick={() => saveLayout.mutate(tiles)}
              >
                {saveLayout.isPending ? t("dashboards.saving") : t("dashboards.saveLayout")}
              </ActionButton>
              <ActionButton kind="cancel" className="secondary" onClick={() => setLocal(null)}>
                {t("common.cancel")}
              </ActionButton>
            </>
          )}
          {canShare && selected && (
            <ActionButton kind="link" className="secondary" onClick={() => setSharing(true)}>
              {selected.shared ? t("dashboards.shareOn") : t("dashboards.share")}
            </ActionButton>
          )}
        </div>
      </div>

      <ErrorMsg error={error} />
      <ErrorMsg error={saveLayout.error} />
      <ErrorMsg error={create.error} />
      <ErrorMsg error={rename.error} />
      <ErrorMsg error={remove.error} />

      {/* The dashboard bar. Tabs, because a select would hide from a new user
          that there is more than one — and the whole point of this feature is
          that there can be several. */}
      <div className="row" style={{ gap: 6, flexWrap: "wrap", marginBottom: 12 }}>
        {(dashboards ?? []).map((d, i) => (
          <span key={d.id} className="row" style={{ gap: 2, flex: "none" }}>
            <button
              className={"pill" + (d.id === selected?.id ? " on" : "")}
              onClick={() => setSelectedId(d.id)}
            >
              {d.name}
              {d.shared && (
                <span
                  className="note"
                  title={t("dashboards.publicBadgeTip") as string}
                  style={{ marginLeft: 6 }}
                >
                  ●
                </span>
              )}
            </button>
            {editing && canWrite && (
              <>
                <button
                  className="pill pill--icon"
                  disabled={i === 0}
                  title={t("dashboards.moveEarlier") as string}
                  onClick={() => reorder.mutate({ id: d.id, position: Math.max(0, i - 1) })}
                >
                  ◀
                </button>
                <button
                  className="pill pill--icon"
                  disabled={i === (dashboards?.length ?? 1) - 1}
                  title={t("dashboards.moveLater") as string}
                  onClick={() => reorder.mutate({ id: d.id, position: i + 1 })}
                >
                  ▶
                </button>
              </>
            )}
          </span>
        ))}
        {canWrite && (
          <ActionButton
            kind="add"
            className="pill pill--add"
            onClick={() => {
              setNewName("");
              setCreating(true);
            }}
          >
            {t("dashboards.newDashboard")}
          </ActionButton>
        )}
        {canWrite && selected && editing && (
          <>
            <ActionButton
              kind="rename"
              className="secondary"
              onClick={() => {
                setNameDraft(selected.name);
                setRenaming(true);
              }}
            >
              {t("dashboards.rename")}
            </ActionButton>
            <ActionButton
              kind="delete"
              className="secondary"
              onClick={() => setConfirmDelete(true)}
            >
              {t("dashboards.deleteDashboard")}
            </ActionButton>
          </>
        )}
      </div>

      {editing && canWrite && (
        <div className="card" style={{ padding: 12 }}>
          <div className="row" style={{ gap: 10, flexWrap: "wrap" }}>
            {/* `1 1 220px`, not `1`: with a zero basis the hint never asks for
                room, so the row never wrapped and at 393px the sentence was
                set one word per line beside a 260px select. The basis is the
                width below which the two stop sharing a line. */}
            <span className="note" style={{ flex: "1 1 220px" }}>
              {t(narrow ? "dashboards.editHintNarrow" : "dashboards.editHint")}
            </span>
            <select
              value=""
              style={{ width: 260 }}
              onChange={(e) => {
                const type = e.target.value;
                const def = TILES[type];
                if (!def) return;
                setLocal(addTile(tiles, type, def.w, def.h));
              }}
            >
              <option value="">{t("dashboards.addTile")}</option>
              {TILE_TYPES.map((type) => (
                <option key={type} value={type}>
                  {t(TILES[type].labelKey)}
                </option>
              ))}
            </select>
          </div>
        </div>
      )}

      {!dashboards ? (
        <div className="empty">{t("common.loading")}</div>
      ) : dashboards.length === 0 ? (
        <div className="empty">{t("dashboards.noDashboards")}</div>
      ) : (
        <DashboardGrid
          tiles={tiles}
          data={data}
          siteSlug={siteSlug}
          publicView={false}
          editing={editing && canWrite}
          onChange={setLocal}
        />
      )}

      {creating && (
        <Modal title={t("dashboards.newDashboard")} onClose={() => setCreating(false)}>
          <Field label={t("dashboards.name")}>
            <input
              autoFocus
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && newName.trim()) {
                  create.mutate(newName.trim());
                  setCreating(false);
                }
              }}
            />
          </Field>
          <div className="row" style={{ gap: 8, justifyContent: "flex-end" }}>
            <ActionButton kind="cancel" className="secondary" onClick={() => setCreating(false)}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton
              kind="create"
              disabled={!newName.trim()}
              onClick={() => {
                create.mutate(newName.trim());
                setCreating(false);
              }}
            >
              {t("dashboards.create")}
            </ActionButton>
          </div>
        </Modal>
      )}

      {renaming && selected && (
        <Modal title={t("dashboards.rename")} onClose={() => setRenaming(false)}>
          <Field label={t("dashboards.name")}>
            <input
              autoFocus
              value={nameDraft}
              onChange={(e) => setNameDraft(e.target.value)}
            />
          </Field>
          <div className="row" style={{ gap: 8, justifyContent: "flex-end" }}>
            <ActionButton kind="cancel" className="secondary" onClick={() => setRenaming(false)}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton
              kind="save"
              disabled={!nameDraft.trim()}
              onClick={() => {
                rename.mutate({ id: selected.id, name: nameDraft.trim() });
                setRenaming(false);
              }}
            >
              {t("common.save")}
            </ActionButton>
          </div>
        </Modal>
      )}

      {confirmDelete && selected && (
        <Modal title={t("dashboards.deleteDashboard")} onClose={() => setConfirmDelete(false)}>
          <p>{t("dashboards.confirmDelete", { name: selected.name })}</p>
          {selected.shared && <p className="error">{t("dashboards.deleteSharedWarning")}</p>}
          <div className="row" style={{ gap: 8, justifyContent: "flex-end" }}>
            <ActionButton
              kind="cancel"
              className="secondary"
              onClick={() => setConfirmDelete(false)}
            >
              {t("common.cancel")}
            </ActionButton>
            <ActionButton
              kind="delete"
              onClick={() => {
                remove.mutate(selected.id);
                setConfirmDelete(false);
              }}
            >
              {t("common.delete")}
            </ActionButton>
          </div>
        </Modal>
      )}

      {sharing && selected && (
        <SharePanel
          dashboard={selected}
          onClose={() => setSharing(false)}
          onChanged={() => qc.invalidateQueries({ queryKey: ["dashboards", siteId] })}
        />
      )}
    </>
  );
}

/**
 * The share panel.
 *
 * Everything about this dialog is shaped by one fact: the token is shown ONCE.
 * The server keeps a sha256 of it (migration 0041), so there is nothing to
 * re-display later — the panel therefore says so before minting, shows the URL
 * in full afterwards, and offers rotation rather than recovery.
 */
function SharePanel({
  dashboard,
  onClose,
  onChanged,
}: {
  dashboard: DashboardRec;
  onClose: () => void;
  onChanged: () => void;
}) {
  const { t, i18n } = useTranslation();
  const [token, setToken] = useState("");
  const [copied, setCopied] = useState(false);

  const mint = useMutation({
    mutationFn: () => shareDashboard(dashboard.id),
    onSuccess: (r) => {
      setToken(r.token);
      setCopied(false);
      onChanged();
    },
  });
  const revoke = useMutation({
    mutationFn: () => unshareDashboard(dashboard.id),
    onSuccess: () => {
      setToken("");
      onChanged();
    },
  });

  // The ORIGIN comes from the browser, never from the server: the controller is
  // reached through whatever hostname this operator's browser used, and a
  // server-side guess at its own public URL is wrong exactly when it matters
  // (reverse proxy, split DNS).
  const url = token ? window.location.origin + publicDashboardPath(token) : "";

  const when = (iso?: string) =>
    iso ? (viewerDate(iso) ?? new Date(iso)).toLocaleString(i18n.language) : t("dashboards.shareNever");

  return (
    <Modal
      title={t("dashboards.shareTitle", { name: dashboard.name })}
      onClose={onClose}
      help={
        <>
          <p>{t("dashboards.shareHelp1")}</p>
          <p>{t("dashboards.shareHelp2")}</p>
        </>
      }
    >
      <ErrorMsg error={mint.error} />
      <ErrorMsg error={revoke.error} />

      {!dashboard.shared && !token && (
        <>
          <p className="note">{t("dashboards.shareOffExplain")}</p>
          <div className="row" style={{ gap: 8, justifyContent: "flex-end" }}>
            <ActionButton kind="cancel" className="secondary" onClick={onClose}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton kind="link" disabled={mint.isPending} onClick={() => mint.mutate()}>
              {t("dashboards.shareCreate")}
            </ActionButton>
          </div>
        </>
      )}

      {token && (
        <>
          <p className="note">{t("dashboards.shareOnceWarning")}</p>
          <Field label={t("dashboards.shareUrl")}>
            <input readOnly value={url} onFocus={(e) => e.currentTarget.select()} />
          </Field>
          <div className="row" style={{ gap: 8, justifyContent: "flex-end" }}>
            <ActionButton
              kind="copy"
              className="secondary"
              onClick={() => {
                navigator.clipboard?.writeText(url).then(
                  () => setCopied(true),
                  // Clipboard access can be refused (insecure origin, policy).
                  // The field above is selectable, so the fallback is "copy it
                  // by hand" and not an error nobody can act on.
                  () => setCopied(false),
                );
              }}
            >
              {copied ? t("dashboards.shareCopied") : t("dashboards.shareCopy")}
            </ActionButton>
            <ActionButton kind="close" onClick={onClose}>
              {t("common.closeAction")}
            </ActionButton>
          </div>
        </>
      )}

      {dashboard.shared && !token && (
        <>
          <p className="note">
            {t("dashboards.shareCreatedAt", { when: when(dashboard.share_created_at) })}
            {" · "}
            {t("dashboards.shareLastUsed", { when: when(dashboard.share_last_used_at) })}
          </p>
          <p className="note">{t("dashboards.shareLostExplain")}</p>
          <div className="row" style={{ gap: 8, justifyContent: "flex-end" }}>
            <ActionButton kind="cancel" className="secondary" onClick={onClose}>
              {t("common.closeAction")}
            </ActionButton>
            <ActionButton
              kind="ban"
              className="secondary"
              disabled={revoke.isPending}
              onClick={() => revoke.mutate()}
            >
              {t("dashboards.shareRevoke")}
            </ActionButton>
            <ActionButton kind="migrate" disabled={mint.isPending} onClick={() => mint.mutate()}>
              {t("dashboards.shareRotate")}
            </ActionButton>
          </div>
        </>
      )}
    </Modal>
  );
}

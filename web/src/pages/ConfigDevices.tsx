import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { deviceDetailPath, sitePath } from "../paths";
import { useSite, useSiteId } from "../useSite";
import { del, get, post, useSiteLive, type Device, type Relocation, type Site } from "../api";
import { useSiteRole } from "../auth";
import { ActionButton, ErrorMsg, Field, Modal, useToast } from "../components";
import { IconAction, IconDeleteButton } from "../ui/IconAction";
import { ConformMgmtButton } from "./ConformMgmt";
import { effectiveStatus, expandPorts } from "../network";

/** Site administrative inventory: device removal and migration to another site. */
export default function ConfigDevices() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const [moving, setMoving] = useState<Device | null>(null);
  const [toast, showToast] = useToast();
  /**
   * This table carries a REACHABILITY dot per row (`effectiveStatus` below), and
   * reachability moves without anyone on this page asking it to. Until this
   * subscription existed the screen had no refresh of any kind — not the site
   * stream, not a fallback interval — because `useSiteLive` is what OPENS the
   * stream, and no component mounted on this route was calling it. A device
   * could go down and this inventory would keep it green until the operator
   * navigated away and back.
   *
   * Safe against what is being edited here: the two modals hold the `Device`
   * they were opened with in their own state, so a list arriving underneath
   * cannot move the row out from under a delete or a site change in progress.
   */
  const refetchInterval = useSiteLive(siteId);

  const { data: devices, error } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
    refetchInterval,
  });

  const [relocating, setRelocating] = useState<Device | null>(null);
  const remove = useMutation({
    mutationFn: (d: Device) => del(`/api/devices/${d.id}`),
    onSuccess: (_out, d) => {
      qc.invalidateQueries({ queryKey: ["devices", siteId] });
      qc.invalidateQueries({ queryKey: ["links", siteId] });
      showToast(t("configDevicesPage.removedToast", { name: d.name }));
    },
  });

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("configDevicesPage.title")}
          <span className="sub">
            {t("configDevicesPage.subtitle", { count: (devices ?? []).length })}
          </span>
        </h1>
      </div>
      {role !== "ing" && (
        <div className="note" style={{ margin: "0 0 12px" }}>
          {t("configDevicesPage.readOnly")}
        </div>
      )}
      <ErrorMsg error={error} />
      <ErrorMsg error={remove.error} />

      <div className="card">
        <div className="scroll-x scroll-cue">
          <table>
            <thead>
              <tr>
                <th>{t("configDevicesPage.deviceCol")}</th>
                <th>{t("configDevicesPage.modelCol")}</th>
                <th>{t("configDevicesPage.locationCol")}</th>
                <th>{t("configDevicesPage.addressCol")}</th>
                <th>{t("configDevicesPage.portsCol")}</th>
                <th>{t("common.stateCol")}</th>
                <th className="col-actions"></th>
              </tr>
            </thead>
            <tbody>
              {(devices ?? []).map((d) => (
                <tr key={d.id}>
                  <td>
                    <Link to={deviceDetailPath(siteSlug, d)}>{d.name}</Link>
                  </td>
                  <td className="note">{d.model || "—"}</td>
                  <td className="note">{d.location || "—"}</td>
                  <td className="note mono">{d.mgmt_ip || "—"}</td>
                  <td className="note">{expandPorts(d).length}</td>
                  <td>
                    <span className="row" style={{ gap: 6 }}>
                      <span className="dot" style={{ background: effectiveStatus(d.status, d.mgmt).color }} />
                      <span style={{ fontSize: 11, fontWeight: 700 }}>
                        {effectiveStatus(d.status, d.mgmt).label}
                      </span>
                    </span>
                    {effectiveStatus(d.status, d.mgmt).hint && (
                      <div className="note" style={{ color: "var(--warn)" }}>
                        {effectiveStatus(d.status, d.mgmt).hint}
                      </div>
                    )}
                  </td>
                  <td className="col-actions">
                    {role === "ing" && (
                      <div className="icon-actions">
                        {/* STAYS A WORD, and on purpose: it appears on the few
                            rows that are off the addressing plan, it is orange,
                            and it is the row's exceptional gesture rather than
                            one of its routine three. Spelled out, it also gives
                            the glyphs beside it the context an icon needs. */}
                        {d.mgmt?.checked && !d.mgmt.conforms && (
                          <ConformMgmtButton
                            device={d}
                            className="ghost warn-text"
                            onToast={showToast}
                          />
                        )}
                        <IconAction
                          kind="scan"
                          label={t("configDevicesPage.relocate") as string}
                          onClick={() => setRelocating(d)}
                        />
                        <IconAction
                          kind="sites"
                          label={t("configDevicesPage.changeSite") as string}
                          onClick={() => setMoving(d)}
                        />
                        <IconDeleteButton onConfirm={() => remove.mutate(d)} />
                      </div>
                    )}
                  </td>
                </tr>
              ))}
              {(devices ?? []).length === 0 && (
                <tr>
                  <td colSpan={7} className="empty">
                    {t("configDevicesPage.noDevices")}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <p className="note" style={{ marginTop: 12 }}>
          {t("configDevicesPage.footerNote")}
        </p>
      </div>

      {relocating && (
        <RelocateModal
          device={relocating}
          onClose={() => setRelocating(null)}
          onDone={(ip) => {
            setRelocating(null);
            qc.invalidateQueries({ queryKey: ["devices", siteId] });
            showToast(t("configDevicesPage.relocatedToast", { ip }));
          }}
        />
      )}

      {moving && (
        <MoveModal
          device={moving}
          fromSite={siteId!}
          onClose={() => setMoving(null)}
          onDone={(siteName) => {
            setMoving(null);
            qc.invalidateQueries();
            showToast(t("configDevicesPage.movedToast", { name: moving.name, site: siteName }));
          }}
        />
      )}
      {toast}
    </>
  );
}

function MoveModal({
  device,
  fromSite,
  onClose,
  onDone,
}: {
  device: Device;
  fromSite: string;
  onClose: () => void;
  onDone: (siteName: string) => void;
}) {
  const { t } = useTranslation();
  const [target, setTarget] = useState("");
  const { data: sites } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });
  const others = (sites ?? []).filter((s) => s.id !== fromSite);

  const move = useMutation({
    mutationFn: () => post(`/api/devices/${device.id}/move`, { site_id: target }),
    onSuccess: () => onDone(others.find((s) => s.id === target)?.name ?? ""),
  });

  return (
    <Modal title={t("configDevicesPage.moveTitle", { name: device.name })} onClose={onClose}>
      <Field label={t("configDevicesPage.targetSite")}>
        <select value={target} onChange={(e) => setTarget(e.target.value)} autoFocus>
          <option value="">{t("configDevicesPage.chooseSite")}</option>
          {others.map((s) => (
            <option key={s.id} value={s.id}>
              {s.name}
            </option>
          ))}
        </select>
      </Field>
      <p className="note">{t("configDevicesPage.moveNote")}</p>
      <ErrorMsg error={move.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton kind="migrate" disabled={!target || move.isPending} onClick={() => move.mutate()}>
          {t("configDevicesPage.moveButton")}
        </ActionButton>
      </div>
    </Modal>
  );
}

/** Find a device that changed address — serial-verified scan or manual IP entry. */
function RelocateModal({
  device,
  onClose,
  onDone,
}: {
  device: Device;
  onClose: () => void;
  onDone: (ip: string) => void;
}) {
  const { t } = useTranslation();
  const [prefix, setPrefix] = useState("");
  const [manual, setManual] = useState("");
  const [started, setStarted] = useState(false);

  const start = useMutation({
    mutationFn: () => post<Relocation>(`/api/devices/${device.id}/relocate`,
      prefix.trim() ? { prefix: prefix.trim() } : {}),
    onSuccess: () => setStarted(true),
  });
  const submit = useMutation({
    mutationFn: () => post<Relocation>(`/api/devices/${device.id}/relocate/address`,
      { ip: manual.trim() }),
    onSuccess: (r) => {
      if (r.found_ip) onDone(r.found_ip);
    },
  });
  const cancel = useMutation({
    mutationFn: () => del(`/api/devices/${device.id}/relocate`),
    onSuccess: onClose,
  });

  const { data: status } = useQuery({
    queryKey: ["relocate", device.id],
    queryFn: () => get<Relocation>(`/api/devices/${device.id}/relocate`),
    enabled: started,
    refetchInterval: (q) =>
      (q.state.data as Relocation | undefined)?.state === "searching" ? 2000 : false,
  });

  const found = status?.state === "found";
  const pct = status && status.total > 0
    ? Math.round((status.scanned / status.total) * 100)
    : 0;

  return (
    <Modal
      title={t("configDevicesPage.relocateTitle", { name: device.name })}
      onClose={onClose}
      help={<p>{t("configDevicesPage.relocateHelp")}</p>}
    >
      <p className="note" style={{ marginTop: 0 }}>
        {t("configDevicesPage.lastKnownAddress")}{" "}
        <span className="mono">{device.mgmt_ip || t("configDevicesPage.none")}</span>.
      </p>

      {!started ? (
        <>
          <Field label={t("configDevicesPage.scanPrefix")}>
            <input
              value={prefix}
              onChange={(e) => setPrefix(e.target.value)}
              placeholder="192.0.2.0/24"
              className="mono"
            />
          </Field>
          <ErrorMsg error={start.error} />
          <div className="actions">
            <ActionButton kind="cancel" className="secondary" onClick={onClose}>
              {t("common.cancel")}
            </ActionButton>
            <ActionButton kind="scan" disabled={start.isPending} onClick={() => start.mutate()}>
              {t("configDevicesPage.startSearch")}
            </ActionButton>
          </div>
        </>
      ) : (
        <>
          <div className="card" style={{ marginBottom: 12 }}>
            <div className="kicker">{t("configDevicesPage.autoScan")}</div>
            {status?.state === "searching" && (
              <>
                <p className="note" style={{ margin: "6px 0" }}>
                  {t("configDevicesPage.scanProgress", {
                    scanned: status.scanned,
                    total: status.total,
                    prefix: status.prefix,
                    serial: status.serial,
                  })}
                </p>
                <div className="progress"><div style={{ width: pct + "%" }} /></div>
              </>
            )}
            {found && (
              <p style={{ color: "var(--ok)", margin: "6px 0" }}>
                {t("configDevicesPage.foundAt", {
                  ip: status?.found_ip,
                  scanned: status?.scanned,
                  by: status?.found_by === "operator"
                    ? t("configDevicesPage.foundByOperator")
                    : t("configDevicesPage.foundByScan"),
                })}
              </p>
            )}
            {status?.state === "not_found" && (
              <p className="note" style={{ color: "var(--warn)", margin: "6px 0" }}>
                {t("configDevicesPage.notFound", { total: status.total })}
              </p>
            )}
          </div>

          {!found && (
            <>
              <Field label={t("configDevicesPage.knownAddress")} help={<p>{t("configDevicesPage.knownAddressHelp")}</p>}>
                <input
                  value={manual}
                  onChange={(e) => setManual(e.target.value)}
                  placeholder="192.0.2.42"
                  className="mono"
                />
              </Field>
              <ErrorMsg error={submit.error} />
            </>
          )}

          <div className="actions">
            {found ? (
              <ActionButton kind="finish" onClick={() => onDone(status?.found_ip ?? "")}>
                {t("configDevicesPage.finish")}
              </ActionButton>
            ) : (
              <>
                <ActionButton kind="cancel" className="secondary" disabled={cancel.isPending}
                  onClick={() => cancel.mutate()}>
                  {t("configDevicesPage.stopSearch")}
                </ActionButton>
                <ActionButton kind="confirm" disabled={!manual.trim() || submit.isPending}
                  onClick={() => submit.mutate()}>
                  {t("configDevicesPage.useAddress")}
                </ActionButton>
              </>
            )}
          </div>
        </>
      )}
    </Modal>
  );
}

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../useSite";
import {
  get,
  post,
  type Device,
  type DeviceLink,
  type Profile,
  type Vlan,
} from "../api";
import { useSiteRole } from "../auth";
import { ActionButton, ErrorMsg, HelpTip, useToast } from "../components";

interface ImportResult {
  counts: Record<string, number>;
}

/** Import / export site network configuration in pivot `meshcraft_site_config` format. */
export default function SiteConfigIO() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const role = useSiteRole(siteId);
  const qc = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState<{ name: string; text: string } | null>(null);
  const [result, setResult] = useState<ImportResult | null>(null);
  const [toast, showToast] = useToast();
  const samplePortName = t("siteConfigIOPage.samplePortProfileName");

  const { data: vlans } = useQuery({
    queryKey: ["vlans", siteId],
    queryFn: () => get<Vlan[]>(`/api/sites/${siteId}/vlans`),
    enabled: !!siteId,
  });
  const { data: devices } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId,
  });
  const { data: links } = useQuery({
    queryKey: ["links", siteId],
    queryFn: () => get<DeviceLink[]>(`/api/sites/${siteId}/links`),
    enabled: !!siteId,
  });
  const { data: portProfiles } = useQuery({
    queryKey: ["profiles", "port", siteId],
    queryFn: () => get<Profile[]>(`/api/sites/${siteId}/profiles/port`),
    enabled: !!siteId,
  });

  const doExport = async () => {
    const res = await fetch(`/api/sites/${siteId}/export`, { credentials: "same-origin" });
    if (!res.ok) {
      showToast(t("siteConfigIOPage.exportDenied"));
      return;
    }
    const blob = await res.blob();
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "meshcraft-site-config.json";
    a.click();
    URL.revokeObjectURL(a.href);
    showToast(t("siteConfigIOPage.exported"));
  };

  const runImport = useMutation({
    mutationFn: () => post<ImportResult>(`/api/sites/${siteId}/import`, JSON.parse(pending!.text)),
    onSuccess: (out) => {
      setResult(out);
      setPending(null);
      qc.invalidateQueries();
      showToast(t("siteConfigIOPage.importApplied", { count: out.counts.devices }));
    },
  });

  const pick = async (file: File) => {
    const text = await file.text();
    try {
      const parsed = JSON.parse(text) as { meshcraft_site_config?: number };
      if (parsed.meshcraft_site_config !== 1) {
        showToast(t("siteConfigIOPage.unrecognizedFile"));
        return;
      }
    } catch {
      showToast(t("siteConfigIOPage.invalidJson"));
      return;
    }
    setResult(null);
    runImport.reset();
    setPending({ name: file.name, text });
  };

  return (
    <>
      <div className="toolbar">
        <h1>
          {t("siteConfigIOPage.title")}
          <span className="sub">{t("siteConfigIOPage.subtitle")}</span>
        </h1>
      </div>

      <div className="dash-grid">
        <div className="card">
          <div className="card-head">
            <div className="kicker">
              {t("siteConfigIOPage.export")}
              <HelpTip label={t("siteConfigIOPage.exportHelpTitle")}>
                <p>{t("siteConfigIOPage.exportHelp1")}</p>
                <p>{t("siteConfigIOPage.exportHelp2")}</p>
              </HelpTip>
            </div>
            <span className="note">{t("siteConfigIOPage.formatNote")}</span>
          </div>
          <div className="kv" style={{ marginBottom: 12 }}>
            <span className="k">{t("siteConfigIOPage.devices")}</span>
            <span>{(devices ?? []).length}</span>
            <span className="k">{t("siteConfigIOPage.vlans")}</span>
            <span>{(vlans ?? []).length}</span>
            <span className="k">{t("siteConfigIOPage.portProfiles")}</span>
            <span>{(portProfiles ?? []).length}</span>
            <span className="k">{t("siteConfigIOPage.links")}</span>
            <span>{(links ?? []).length}</span>
          </div>
          <ActionButton kind="export" onClick={doExport} disabled={role === "viewer"}>
            {t("siteConfigIOPage.exportButton")}
          </ActionButton>
          {role === "viewer" && (
            <div className="note" style={{ marginTop: 6 }}>
              {t("siteConfigIOPage.exportRoleNote")}
            </div>
          )}
        </div>

        <div className="card">
          <div className="card-head">
            <div className="kicker">
              {t("siteConfigIOPage.import")}
              <HelpTip label={t("siteConfigIOPage.importHelpTitle")}>
                <p>{t("siteConfigIOPage.importHelp")}</p>
              </HelpTip>
            </div>
            <span className="note">{t("siteConfigIOPage.engineerOnly")}</span>
          </div>
          <p className="note" style={{ marginTop: 0 }}>
            {t("siteConfigIOPage.importWarning")}
          </p>
          {role === "ing" ? (
            <>
              <input
                ref={fileRef}
                type="file"
                accept="application/json"
                style={{ display: "none" }}
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) pick(f);
                  e.target.value = "";
                }}
              />
              <ActionButton kind="import" className="secondary" onClick={() => fileRef.current?.click()}>
                {t("siteConfigIOPage.chooseFile")}
              </ActionButton>

              {pending && (
                <div className="confirm-box">
                  <div style={{ fontWeight: 600, fontSize: 13 }}>
                    {t("siteConfigIOPage.replaceConfirm", { name: pending.name })}
                  </div>
                  <div className="note" style={{ margin: "4px 0 10px" }}>
                    {t("siteConfigIOPage.replaceDetail", {
                      devices: (devices ?? []).length,
                      vlans: (vlans ?? []).length,
                    })}
                  </div>
                  <div className="row">
                    <ActionButton
                      kind="import"
                      className="danger"
                      disabled={runImport.isPending}
                      onClick={() => runImport.mutate()}
                    >
                      {runImport.isPending
                        ? t("siteConfigIOPage.importing")
                        : t("siteConfigIOPage.replaceButton")}
                    </ActionButton>
                    <ActionButton kind="cancel" className="secondary" onClick={() => setPending(null)}>
                      {t("common.cancel")}
                    </ActionButton>
                  </div>
                </div>
              )}
              <ErrorMsg error={runImport.error} />

              {result && (
                <div className="confirm-box" style={{ borderColor: "var(--ok)" }}>
                  <div style={{ fontWeight: 600, fontSize: 13 }}>
                    {t("siteConfigIOPage.importDone")}
                  </div>
                  <div className="note" style={{ marginTop: 4 }}>
                    {Object.entries(result.counts)
                      .map(([k, v]) => `${v} ${k.replace(/_/g, " ")}`)
                      .join(" · ")}
                  </div>
                </div>
              )}
            </>
          ) : (
            <div className="note">{t("siteConfigIOPage.importRoleNote")}</div>
          )}
        </div>
      </div>

      <div className="card">
        <div className="card-head">
          <div className="kicker">
            {t("siteConfigIOPage.sampleFile")}
            <HelpTip label={t("siteConfigIOPage.sampleHelpTitle")}>
              <p>{t("siteConfigIOPage.sampleHelp")}</p>
            </HelpTip>
          </div>
        </div>
        <pre className="code">{`{
  "meshcraft_site_config": 1,
  "site":  { "name": "…", "plan": { "zones": [ … ] } },
  "vlans": [ { "vlan_id": 20, "name": "DANTE PRI", "color": "#199e70" } ],
  "port_profiles": [
    { "name": "${samplePortName}",
      "spec": { "mode": "trunk", "pvid": 40, "tagged": [40, 41], "poe": true } }
  ],
  "device_profiles": [ … ], "device_groups": [ … ],
  "devices": [
    { "name": "FOH", "model": "Generic-24P-4X",
      "ports": { "ether5": { "mode": "trunk", "pvid": 10, "tagged": [10, 70, 71] } } }
  ],
  "links": [ { "a": "CORE", "b": "FOH", "speed": "10G", "status": "up" } ]
}`}</pre>
      </div>
      {toast}
    </>
  );
}

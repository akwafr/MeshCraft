import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { sitePath } from "../../paths";
import { useSite, useSiteId } from "../../useSite";
import { del, get, post, type Discovered, type ScanPrefix } from "../../api";
import { ActionButton, DeleteButton, ErrorMsg, HelpTip, QuickTip, useToast } from "../../components";
import AdoptModal from "./AdoptModal";

/**
 * Automatic discovery queue: candidates found by scanning the ranges
 * declared on the proxies, identified by SSH banner / HTTP header and, when
 * a standard credential works, by what the device says about itself.
 *
 * These candidates have no site: adoption is what picks one.
 */
export default function Discover() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const qc = useQueryClient();
  const [q, setQ] = useState("");
  const [adopting, setAdopting] = useState<Discovered | null>(null);
  const [showAdopted, setShowAdopted] = useState(false);
  const [toast, showToast] = useToast();

  const { data: candidates, error, isFetching } = useQuery({
    queryKey: ["discovered", showAdopted],
    queryFn: () =>
      get<Discovered[]>(`/api/discovery/candidates${showAdopted ? "?adopted=1" : ""}`),
    refetchInterval: 15000,
  });
  const { data: prefixes } = useQuery({
    queryKey: ["scan-prefixes"],
    queryFn: () => get<ScanPrefix[]>("/api/discovery/prefixes"),
  });

  const scan = useMutation({
    mutationFn: (id: string) =>
      post<{ addresses: number; found: number; stored: number; duration: string }>(
        `/api/discovery/prefixes/${id}/scan`,
        {},
      ),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["discovered"] });
      qc.invalidateQueries({ queryKey: ["scan-prefixes"] });
      showToast(t("discover.scanResult", { addresses: r.addresses, duration: r.duration, stored: r.stored }));
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/discovery/candidates/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["discovered"] }),
  });

  const needle = q.trim().toLowerCase();
  const shown = (candidates ?? []).filter((c) => {
    if (!needle) return true;
    return [c.ip, c.hostname, c.vendor, c.model, c.driver, c.serial, c.proxy_name]
      .join(" ")
      .toLowerCase()
      .includes(needle);
  });

  const enabledPrefixes = (prefixes ?? []).filter((p) => p.enabled);

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.autoDiscovery")}
          <span className="sub">{t("discover.subtitle", { count: (candidates ?? []).length })}</span>
        </h1>
        <div className="row" style={{ gap: 6 }}>
          <label className="note row" style={{ gap: 6 }}>
            <input
              type="checkbox"
              checked={showAdopted}
              onChange={(e) => setShowAdopted(e.target.checked)}
            />
            {t("discover.includeAdopted")}
          </label>
        </div>
      </div>
      <ErrorMsg error={error} />

      <div className="card" style={{ marginBottom: 12 }}>
        <div className="row" style={{ justifyContent: "space-between", flexWrap: "wrap", gap: 8 }}>
          <div>
            <div className="kicker">
              {t("discover.launchScan")}
              <HelpTip label={t("discover.scanHelpLabel") as string}>
                <p>{t("discover.scanHelp1")}</p>
                <p>{t("discover.scanHelp2")}</p>
                <p>{t("discover.scanHelp3")}</p>
              </HelpTip>
            </div>
            {/* The only case where the screen still needs to speak: without
                an active range, no scan will happen, and nothing else would
                say so. */}
            {enabledPrefixes.length === 0 && (
              <div className="note" style={{ marginTop: 4 }}>
                {t("discover.noActiveRangePrefix")}{" "}
                <Link to={sitePath(siteSlug, "add", "prefixes")}>{t("nav.scanRanges")}</Link>
                {t("discover.noActiveRangeMid")}{" "}
                <Link to="/config/identifiants">{t("nav.discoveryCredentials")}</Link>
                {t("discover.noActiveRangeSuffix")}
              </div>
            )}
          </div>
          <div className="chip-row">
            {enabledPrefixes.map((p) => (
              <ActionButton
                key={p.id}
                kind="scan"
                className="secondary"
                disabled={scan.isPending}
                onClick={() => scan.mutate(p.id)}
                title={
                  p.last_result
                    ? (t("discover.lastScanTitle", { result: p.last_result }) as string)
                    : (t("discover.neverScanned") as string)
                }
              >
                {scan.isPending ? t("scanPrefixes.scanning") : t("discover.scanPrefixButton", { prefix: p.prefix })}
                <span className="note" style={{ marginLeft: 6 }}>
                  {p.proxy_name}
                </span>
              </ActionButton>
            ))}
          </div>
        </div>
        <ErrorMsg error={scan.error} />
      </div>

      <div className="toolbar" style={{ marginBottom: 8 }}>
        <input
          className="search"
          placeholder={t("discover.searchPlaceholder") as string}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        {isFetching && <span className="note">{t("common.refreshing")}</span>}
      </div>

      {shown.length === 0 ? (
        <div className="card">
          <div className="empty">{t("discover.noCandidates")}</div>
        </div>
      ) : (
        <div className="card" style={{ padding: 0, overflowX: "auto" }}>
          <table className="ports-table">
            <thead>
              <tr>
                <th>{t("common.device")}</th>
                <th>{t("common.address")}</th>
                <th>{t("discover.identificationCol")}</th>
                <th>{t("discover.transportsCol")}</th>
                <th>{t("discover.seenByCol")}</th>
                <th>{t("discover.originCol")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {shown.map((c) => (
                <tr key={c.id}>
                  <td>
                    <div style={{ fontWeight: 700 }}>{c.hostname || "—"}</div>
                    <div className="note">
                      {[c.vendor, c.model].filter(Boolean).join(" ") || t("discover.unknownVendor")}
                      {c.serial ? ` · S/N ${c.serial}` : ""}
                    </div>
                  </td>
                  <td style={{ fontVariantNumeric: "tabular-nums" }}>{c.ip || "—"}</td>
                  <td>
                    <span className="chip">{c.driver}</span>
                    <div className="note">
                      {t("discover.confidence", { value: c.confidence })}
                      {typeof c.fingerprint?.evidence === "string"
                        ? ` · ${c.fingerprint.evidence}`
                        : ""}
                    </div>
                    {c.note && <div className="note warn-text">{c.note}</div>}
                  </td>
                  <td>
                    <div className="chip-row">
                      {c.transports
                        .split(",")
                        .filter(Boolean)
                        .map((tr) => (
                          <span key={tr} className="chip">
                            {tr}
                          </span>
                        ))}
                    </div>
                    {c.has_config && <div className="note">{t("discover.configRead")}</div>}
                  </td>
                  <td>
                    {c.sightings.length === 0 ? (
                      <span className="note">{c.proxy_name || "—"}</span>
                    ) : (
                      <div className="chip-row">
                        {c.sightings.map((sg) => (
                          <QuickTip key={sg.proxy_id + sg.ip} text={sg.ip} className="quicktip-inline">
                            <span className="chip">
                              {sg.proxy_name}
                            </span>
                          </QuickTip>
                        ))}
                      </div>
                    )}
                  </td>
                  <td className="note">{sourceLabel(c.source, t)}</td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    {c.adopted_device_id ? (
                      <span className="badge">{t("discover.adopted")}</span>
                    ) : (
                      <>
                        <ActionButton kind="adopt" onClick={() => setAdopting(c)}>
                          {t("discover.adoptEllipsis")}
                        </ActionButton>
                        <span style={{ marginLeft: 6 }}>
                          <DeleteButton onConfirm={() => remove.mutate(c.id)} />
                        </span>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {adopting && (
        <AdoptModal
          candidate={adopting}
          onClose={() => setAdopting(null)}
          onDone={(deviceName) => {
            setAdopting(null);
            qc.invalidateQueries({ queryKey: ["discovered"] });
            qc.invalidateQueries({ queryKey: ["devices"] });
            qc.invalidateQueries({ queryKey: ["vlans"] });
            showToast(t("discover.adoptedToast", { name: deviceName }));
          }}
        />
      )}
      {toast}
    </div>
  );
}

function sourceLabel(s: string, t: (key: string) => string) {
  if (s === "scan") return t("discover.sourceScan");
  if (s === "manual") return t("discover.sourceManual");
  if (s === "serial") return t("discover.sourceSerial");
  return s;
}

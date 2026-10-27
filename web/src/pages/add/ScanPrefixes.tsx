import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { viewerDate } from "../../ui/clock";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useSite, useSiteId } from "../../useSite";
import {
  del,
  get,
  patch,
  post,
  type AddressDomain,
  type ProxyInfo,
  type ScanPrefix,
} from "../../api";
import { ActionButton, DeleteButton, ErrorMsg, Field, QuickTip, useToast } from "../../components";

/**
 * Scan ranges: IPv4/IPv6 prefixes declared ON A PROXY.
 *
 * A range belongs to the proxy that can reach it, never to a site: at scan
 * time, the site the device will end up in isn't known yet. The "Local"
 * proxy is the meshcraft instance itself.
 *
 * Two different questions come up here, and confusing them is what makes
 * `192.168.1.0/24` ambiguous as soon as it appears twice:
 *
 *   - the PROXY says how the range is reached;
 *   - the ADDRESS DOMAIN says which network it is.
 *
 * RFC 1918 space overlaps: the same range behind two proxies can be two
 * networks foreign to each other — or a single one, reached via two paths.
 * Nothing meshcraft observes lets it guess, and picking the wrong side would
 * merge two distinct devices into a single sheet. So the operator declares
 * it, and the default is always "distinct networks".
 */
export default function ScanPrefixes() {
  const { t, i18n } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const qc = useQueryClient();
  const [proxyID, setProxyID] = useState("");
  const [prefix, setPrefix] = useState("");
  const [note, setNote] = useState("");
  const [domainID, setDomainID] = useState("");
  const [toast, showToast] = useToast();

  // Proxies usable for this site: those covering it, plus instance proxies
  // (see docs/en/01-Architecture/Proxy).
  const { data: proxies } = useQuery({
    queryKey: ["discovery-proxies", siteId],
    queryFn: () => get<ProxyInfo[]>(`/api/sites/${siteId}/discovery-proxies`),
    enabled: !!siteId,
  });
  const { data: prefixes, error } = useQuery({
    queryKey: ["scan-prefixes"],
    queryFn: () => get<ScanPrefix[]>("/api/discovery/prefixes"),
  });
  const { data: domains } = useQuery({
    queryKey: ["address-domains"],
    queryFn: () => get<AddressDomain[]>("/api/discovery/domains"),
  });

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["scan-prefixes"] });
    qc.invalidateQueries({ queryKey: ["address-domains"] });
  };

  const create = useMutation({
    mutationFn: () =>
      post<ScanPrefix>("/api/discovery/prefixes", {
        proxy_id: proxyID,
        prefix: prefix.trim(),
        note,
        // Empty = the proxy's own domain. The safe default: assuming
        // separation only costs a duplicate, assuming sharing merges two
        // devices into one and can't be undone.
        address_domain_id: domainID,
        enabled: true,
      }),
    onSuccess: (sp) => {
      setPrefix("");
      setNote("");
      invalidate();
      showToast(t("scanPrefixes.added", { prefix: sp.prefix, proxy: sp.proxy_name, domain: sp.domain_name }));
    },
  });

  const toggle = useMutation({
    mutationFn: (sp: ScanPrefix) =>
      patch<ScanPrefix>(`/api/discovery/prefixes/${sp.id}`, {
        prefix: sp.prefix,
        note: sp.note,
        enabled: !sp.enabled,
      }),
    onSuccess: invalidate,
  });

  // Reassigning a range to another network means deciding two proxies see
  // the same LAN — or stopping saying so. It's the only action that merges
  // or splits discoveries.
  const reassign = useMutation({
    mutationFn: (v: { sp: ScanPrefix; domain: string }) =>
      patch<ScanPrefix>(`/api/discovery/prefixes/${v.sp.id}`, {
        prefix: v.sp.prefix,
        note: v.sp.note,
        enabled: v.sp.enabled,
        address_domain_id: v.domain,
      }),
    onSuccess: (sp) => {
      invalidate();
      showToast(t("scanPrefixes.reassigned", { prefix: sp.prefix, domain: sp.domain_name }));
    },
  });

  const remove = useMutation({
    mutationFn: (id: string) => del(`/api/discovery/prefixes/${id}`),
    onSuccess: invalidate,
  });

  const createDomain = useMutation({
    mutationFn: (name: string) =>
      post<AddressDomain>("/api/discovery/domains", { name }),
    onSuccess: (d) => {
      qc.invalidateQueries({ queryKey: ["address-domains"] });
      setDomainID(d.id);
      showToast(t("scanPrefixes.domainCreated", { name: d.name }));
    },
  });

  const scan = useMutation({
    mutationFn: (id: string) =>
      post<{ addresses: number; stored: number; duration: string }>(
        `/api/discovery/prefixes/${id}/scan`,
        {},
      ),
    onSuccess: (r) => {
      invalidate();
      qc.invalidateQueries({ queryKey: ["discovered"] });
      showToast(t("scanPrefixes.scanResult", { addresses: r.addresses, duration: r.duration, stored: r.stored }));
    },
  });

  // Ranges declared ELSEWHERE under the same prefix. This is exactly the
  // moment ambiguity is born: it's shown rather than left to produce silent
  // duplicates six months later.
  const collisions = useMemo(() => {
    const byPrefix = new Map<string, ScanPrefix[]>();
    for (const sp of prefixes ?? []) {
      byPrefix.set(sp.prefix, [...(byPrefix.get(sp.prefix) ?? []), sp]);
    }
    return byPrefix;
  }, [prefixes]);

  const askNewDomain = () => {
    const name = window.prompt(t("scanPrefixes.newDomainPrompt") as string);
    if (name?.trim()) createDomain.mutate(name.trim());
  };

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.scanRanges")}
          <span className="sub">{t("scanPrefixes.subtitle")}</span>
        </h1>
      </div>

      <div className="card">
        <div className="row" style={{ alignItems: "flex-end" }}>
          <div style={{ width: 200 }}>
            <Field label={t("common.proxy") as string}>
              <select value={proxyID} onChange={(e) => setProxyID(e.target.value)}>
                <option value="">{t("common.localInstance")}</option>
                {(proxies ?? [])
                  .filter((p) => !p.internal)
                  .map((p) => (
                    <option key={p.id} value={p.id}>
                      {p.name || p.id.slice(0, 8)}
                    </option>
                  ))}
              </select>
            </Field>
          </div>
          <div style={{ width: 220 }}>
            <Field
              label={t("scanPrefixes.prefixToScan") as string}
              help={<p>{t("scanPrefixes.prefixHelp")}</p>}
            >
              <input
                value={prefix}
                onChange={(e) => setPrefix(e.target.value)}
                placeholder="10.0.10.0/24 or 2001:db8::/120"
              />
            </Field>
          </div>
          <div style={{ width: 230 }}>
            <Field
              label={t("scanPrefixes.addressDomain") as string}
              help={<p>{t("scanPrefixes.domainHelp")}</p>}
            >
              <select
                value={domainID}
                onChange={(e) => {
                  if (e.target.value === "__new__") askNewDomain();
                  else setDomainID(e.target.value);
                }}
              >
                <option value="">{t("scanPrefixes.ownDomain")}</option>
                {(domains ?? []).map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name}
                  </option>
                ))}
                <option value="__new__">{t("scanPrefixes.newDomain")}</option>
              </select>
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label={t("common.note") as string}>
              <input
                value={note}
                onChange={(e) => setNote(e.target.value)}
                placeholder={t("scanPrefixes.notePlaceholder") as string}
              />
            </Field>
          </div>
          <ActionButton kind="add" disabled={!prefix.trim() || create.isPending} onClick={() => create.mutate()}>
            {t("common.add")}
          </ActionButton>
        </div>
        <ErrorMsg error={create.error} />
        <ErrorMsg error={createDomain.error} />

        {prefix.trim() !== "" && (collisions.get(prefix.trim())?.length ?? 0) > 0 && (
          <div className="note" style={{ marginTop: 8, fontWeight: 600 }}>
            {t("scanPrefixes.collisionWarning", {
              prefix: prefix.trim(),
              list: collisions
                .get(prefix.trim())!
                .map((sp) => t("scanPrefixes.collisionEntry", { proxy: sp.proxy_name, domain: sp.domain_name }))
                .join(", "),
            })}
          </div>
        )}

      </div>

      <ErrorMsg error={error} />

      {(prefixes ?? []).length === 0 ? (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="empty">{t("scanPrefixes.noneDeclared")}</div>
        </div>
      ) : (
        <div className="card card-flush scroll-x scroll-cue">
          <table className="ports-table">
            <thead>
              <tr>
                <th>{t("scanPrefixes.prefixCol")}</th>
                <th>{t("common.proxy")}</th>
                <th>{t("scanPrefixes.networkCol")}</th>
                <th>{t("common.note")}</th>
                <th>{t("scanPrefixes.lastScanCol")}</th>
                <th>{t("common.stateCol")}</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {(prefixes ?? []).map((sp) => (
                <tr key={sp.id}>
                  <td style={{ fontVariantNumeric: "tabular-nums", fontWeight: 700 }}>
                    {sp.prefix}
                    {(collisions.get(sp.prefix)?.length ?? 0) > 1 && (
                      <QuickTip text={t("scanPrefixes.sharedTitle") as string} className="quicktip-inline">
                        <span className="badge" style={{ marginLeft: 6 }}>
                          {t("scanPrefixes.shared")}
                        </span>
                      </QuickTip>
                    )}
                  </td>
                  <td>
                    {sp.proxy_name}
                    {sp.proxy_id === LOCAL_PROXY_ID && (
                      <span className="badge" style={{ marginLeft: 6 }}>
                        {t("common.instance")}
                      </span>
                    )}
                  </td>
                  <td>
                    <select
                      value={sp.address_domain_id}
                      disabled={reassign.isPending}
                      onChange={(e) =>
                        reassign.mutate({ sp, domain: e.target.value })
                      }
                    >
                      {(domains ?? []).map((d) => (
                        <option key={d.id} value={d.id}>
                          {d.name}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td className="note">{sp.note || "—"}</td>
                  <td className="note">
                    {sp.last_run_at
                      ? `${(viewerDate(sp.last_run_at) ?? new Date(sp.last_run_at)).toLocaleString(i18n.language)} — ${sp.last_result}`
                      : t("common.never")}
                  </td>
                  <td>
                    <ActionButton kind="apply" className="secondary" onClick={() => toggle.mutate(sp)}>
                      {sp.enabled ? t("common.active") : t("common.disabled")}
                    </ActionButton>
                  </td>
                  <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                    <ActionButton
                      kind="scan"
                      className="secondary"
                      disabled={scan.isPending}
                      onClick={() => scan.mutate(sp.id)}
                    >
                      {scan.isPending ? t("scanPrefixes.scanning") : t("scanPrefixes.scan")}
                    </ActionButton>
                    <span style={{ marginLeft: 6 }}>
                      <DeleteButton onConfirm={() => remove.mutate(sp.id)} />
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <ErrorMsg error={scan.error} />
      <ErrorMsg error={reassign.error} />
      {toast}
    </div>
  );
}

/** Fixed identifier of the "Local" proxy, created by migration 0005. */
const LOCAL_PROXY_ID = "00000000-0000-0000-0000-000000000001";

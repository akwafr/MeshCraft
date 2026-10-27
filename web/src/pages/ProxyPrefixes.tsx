import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { viewerDate } from "../ui/clock";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  del,
  get,
  patch,
  post,
  type AddressDomain,
  type ScanPrefix,
} from "../api";
import { ActionButton, ErrorMsg, Field, Modal } from "../components";
import { IconAction, IconDeleteButton } from "../ui/IconAction";

/**
 * The ranges scanned BY A PROXY, managed from the proxies page.
 *
 * The screen already existed, but tucked into the "Add equipment" flow: it
 * could only be reached while already in the middle of adding a device. Yet
 * a range belongs to the PROXY that can reach it, never to a site
 * (see docs/en/01-Architecture/Discovery) — so it's while looking at a proxy
 * that one wonders what it scans, and that's where it should be edited.
 *
 * The add-flow form remains: it suits someone discovering the product who
 * wants to do everything from one place. Both hit the same API.
 */
export function ProxyPrefixesModal({
  proxyID,
  proxyName,
  onClose,
}: {
  proxyID: string;
  proxyName: string;
  onClose: () => void;
}) {
  const { t, i18n } = useTranslation();
  const qc = useQueryClient();
  const [prefix, setPrefix] = useState("");
  const [note, setNote] = useState("");
  const [domainID, setDomainID] = useState("");

  const { data: prefixes, error } = useQuery({
    queryKey: ["scan-prefixes", proxyID],
    queryFn: () => get<ScanPrefix[]>(`/api/discovery/prefixes?proxy=${proxyID}`),
  });
  const { data: domains } = useQuery({
    queryKey: ["address-domains"],
    queryFn: () => get<AddressDomain[]>("/api/discovery/domains"),
  });
  const refresh = () => qc.invalidateQueries({ queryKey: ["scan-prefixes", proxyID] });

  const add = useMutation({
    mutationFn: () =>
      post<ScanPrefix>("/api/discovery/prefixes", {
        proxy_id: proxyID,
        prefix: prefix.trim(),
        note: note.trim(),
        enabled: true,
        // Empty = the network specific to this proxy. This is the SAFE
        // default: assuming two proxies look at the same network would
        // merge two devices into one sheet, which can't be undone — a
        // duplicate can.
        address_domain_id: domainID,
      }),
    onSuccess: () => {
      setPrefix("");
      setNote("");
      refresh();
    },
  });
  const toggle = useMutation({
    mutationFn: (sp: ScanPrefix) =>
      patch<ScanPrefix>(`/api/discovery/prefixes/${sp.id}`, {
        proxy_id: sp.proxy_id,
        prefix: sp.prefix,
        note: sp.note,
        enabled: !sp.enabled,
        address_domain_id: sp.address_domain_id,
      }),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: (sp: ScanPrefix) => del(`/api/discovery/prefixes/${sp.id}`),
    onSuccess: refresh,
  });
  const scan = useMutation({
    mutationFn: (sp: ScanPrefix) => post(`/api/discovery/prefixes/${sp.id}/scan`),
    onSuccess: refresh,
  });

  return (
    <Modal title={t("proxyPrefixes.title", { name: proxyName }) as string} onClose={onClose}>
      <ErrorMsg error={error} />
      {(prefixes ?? []).length === 0 ? (
        <div className="empty">{t("proxyPrefixes.noneScanned")}</div>
      ) : (
        <div className="scroll-x scroll-cue">
          <table>
            <thead>
              <tr>
                <th>{t("scanPrefixes.prefixCol")}</th>
                <th>{t("scanPrefixes.networkCol")}</th>
                <th>{t("scanPrefixes.lastScanCol")}</th>
                <th className="col-actions" />
              </tr>
            </thead>
            <tbody>
              {(prefixes ?? []).map((sp) => (
                <tr key={sp.id} style={{ opacity: sp.enabled ? 1 : 0.55 }}>
                  <td>
                    <div className="mono">{sp.prefix}</div>
                    {sp.note && <div className="note">{sp.note}</div>}
                  </td>
                  <td>
                    <span className="chip">{sp.domain_name || t("scanPrefixes.ownDomain")}</span>
                  </td>
                  <td className="note">
                    {sp.last_run_at
                      ? (viewerDate(sp.last_run_at) ?? new Date(sp.last_run_at)).toLocaleString(i18n.language)
                      : t("common.never")}
                    {sp.last_result && <div className="note">{sp.last_result}</div>}
                  </td>
                  <td className="col-actions">
                    <div className="icon-actions">
                      <IconAction
                        kind="scan"
                        label={t("proxyPrefixes.scanAction") as string}
                        onClick={() => scan.mutate(sp)}
                      />
                      {/* A VERB, not a state — the row already says which it is
                          by dimming. So it can be a glyph, as long as the glyph
                          changes with the verb: a check to turn the range back
                          on, a slashed circle to stop scanning it. The single
                          `apply` tick it used to wear said neither. */}
                      <IconAction
                        kind={sp.enabled ? "ban" : "check"}
                        label={
                          (sp.enabled
                            ? t("proxyPrefixes.deactivate")
                            : t("proxyPrefixes.activate")) as string
                        }
                        onClick={() => toggle.mutate(sp)}
                      />
                      <IconDeleteButton onConfirm={() => remove.mutate(sp)} />
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="kicker">{t("proxyPrefixes.addRange")}</div>
        <div className="row">
          <div style={{ width: 200 }}>
            <Field label={t("scanPrefixes.prefixCol") as string}>
              <input
                className="mono"
                value={prefix}
                placeholder="192.0.2.0/24"
                onChange={(e) => setPrefix(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && prefix.trim()) add.mutate();
                }}
              />
            </Field>
          </div>
          <div style={{ width: 200 }}>
            <Field label={t("common.note") as string}>
              <input
                value={note}
                placeholder={t("proxyPrefixes.notePlaceholder") as string}
                onChange={(e) => setNote(e.target.value)}
              />
            </Field>
          </div>
          <div style={{ width: 220 }}>
            <Field
              label={t("scanPrefixes.networkCol") as string}
              help={<p>{t("proxyPrefixes.networkHelp")}</p>}
            >
              <select value={domainID} onChange={(e) => setDomainID(e.target.value)}>
                <option value="">{t("scanPrefixes.ownDomain")}</option>
                {(domains ?? []).map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <ActionButton kind="add" disabled={!prefix.trim() || add.isPending} onClick={() => add.mutate()}>
            {t("common.add")}
          </ActionButton>
        </div>
        <ErrorMsg error={add.error} />
      </div>

      <div className="actions">
        <ActionButton kind="close" className="secondary" onClick={onClose}>
          {t("common.closeAction")}
        </ActionButton>
      </div>
    </Modal>
  );
}

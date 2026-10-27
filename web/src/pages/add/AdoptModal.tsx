import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import {
  get,
  post,
  type Discovered,
  type ParsedConfig,
  type Site,
} from "../../api";
import { useAuth } from "../../auth";
import { ActionButton, ErrorMsg, Field, Modal } from "../../components";

/**
 * Adopting a candidate: this is WHERE the site is chosen.
 *
 * A discovered candidate belongs to no site (it may be seen by any proxy);
 * adoption is the gesture that makes it enter a site, and the controller
 * checks permissions on THAT site.
 */
export default function AdoptModal({
  candidate,
  onClose,
  onDone,
}: {
  candidate: Discovered;
  onClose: () => void;
  onDone: (deviceName: string, siteId: string) => void;
}) {
  const { t } = useTranslation();
  const { user } = useAuth();
  const [siteId, setSiteId] = useState("");
  const [name, setName] = useState(candidate.hostname || candidate.ip || "");
  const [location, setLocation] = useState("");
  const [mode, setMode] = useState<"push" | "import">(
    candidate.has_config ? "import" : "push",
  );
  const [pasted, setPasted] = useState("");

  const { data: sites } = useQuery({
    queryKey: ["sites"],
    queryFn: () => get<Site[]>("/api/sites"),
  });

  // Only sites where the user can adopt are offered: showing others would
  // just lead to a 403.
  const adoptable = (sites ?? []).filter((s) => {
    if (user?.is_global_admin) return true;
    const role = user?.roles?.find((r) => r.site_id === s.id)?.role;
    return role === "operator" || role === "site_admin";
  });

  // Configuration already read from the device, with its analysis.
  const { data: conf } = useQuery({
    enabled: candidate.has_config,
    queryKey: ["candidate-config", candidate.id],
    queryFn: () =>
      get<{ raw_config: string; parsed: ParsedConfig | null; parse_error: string }>(
        `/api/discovery/candidates/${candidate.id}/config`,
      ),
  });

  // Analysis of a manually pasted configuration (serial console case).
  const parsePasted = useMutation({
    mutationFn: (raw: string) =>
      post<ParsedConfig>("/api/discovery/parse-config", { raw_config: raw }),
  });

  const parsed = pasted.trim() ? parsePasted.data ?? null : conf?.parsed ?? null;
  const parseError = pasted.trim()
    ? (parsePasted.error as Error | null)?.message ?? ""
    : conf?.parse_error ?? "";

  const adopt = useMutation({
    mutationFn: () =>
      post<{
        device: { name: string };
        vlans_created: number;
        vlans_kept: number;
        warnings: string[];
      }>(
        `/api/discovery/candidates/${candidate.id}/adopt`,
        {
          site_id: siteId,
          name,
          location,
          config_mode: mode,
          raw_config: mode === "import" ? pasted : "",
        },
      ),
    onSuccess: (res) => onDone(res.device.name, siteId),
  });

  // WHAT MAKES THE IMPORT POSSIBLE, in the order the server will actually try.
  //
  // `has_config` alone used to gate this choice, and that made the option
  // permanently grey on every range discovery cannot SCRAPE a config from — the
  // web-only ones, and RouterOS, whose shell answers the probe with terminal
  // escape sequences until it times out. Those are exactly the devices meshcraft
  // identifies confidently and can log into, so "we know what you are and we
  // have your password, but you may not import your configuration" was the
  // wrong answer.
  //
  // A resolved connector is now enough: the server adopts, then READS the device
  // through it and absorbs what it finds (importFromDevice). If that read fails
  // the device is still adopted and the reason comes back as a warning, which is
  // why offering the choice here costs nothing it cannot explain afterwards.
  const canReadLive = !!candidate.driver;
  const canImport =
    candidate.has_config || pasted.trim().length > 0 || canReadLive;

  return (
    <Modal title={t("adoptModal.title", { name: candidate.hostname || candidate.ip }) as string} onClose={onClose}>
      <div className="row">
        <div style={{ flex: 1 }}>
          <Field label={t("adoptModal.destinationSite") as string}>
            <select value={siteId} onChange={(e) => setSiteId(e.target.value)} autoFocus>
              <option value="">{t("adoptModal.chooseASite")}</option>
              {adoptable.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
          </Field>
        </div>
        <div style={{ flex: 1 }}>
          <Field label={t("adoptModal.nameInMeshcraft") as string}>
            <input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
        </div>
      </div>
      <Field label={t("adoptModal.locationOptional") as string}>
        <input
          value={location}
          onChange={(e) => setLocation(e.target.value)}
          placeholder={t("adoptModal.locationPlaceholder") as string}
        />
      </Field>

      <div className="kicker" style={{ marginTop: 14 }}>{t("nav.configuration")}</div>
      <div className="choice-list">
        <label className={mode === "push" ? "choice active" : "choice"}>
          <input
            type="radio"
            checked={mode === "push"}
            onChange={() => setMode("push")}
          />
          <span>
            <b>{t("adoptModal.pushedByPlatform")}</b>
            <span className="note">{t("adoptModal.pushedByPlatformNote")}</span>
          </span>
        </label>
        <label className={mode === "import" ? "choice active" : "choice"}>
          <input
            type="radio"
            checked={mode === "import"}
            disabled={!canImport}
            onChange={() => setMode("import")}
          />
          <span>
            <b>{t("adoptModal.importLocalConfig")}</b>
            <span className="note">
              {candidate.has_config
                ? t("adoptModal.importLocalConfigNoteReady")
                : canReadLive
                  ? t("adoptModal.importLocalConfigNoteLive")
                  : t("adoptModal.importLocalConfigNotePaste")}
            </span>
          </span>
        </label>
      </div>

      {mode === "import" && (
        <>
          {!candidate.has_config && !canReadLive && (
            <Field label={t("adoptModal.configToImport") as string}>
              <textarea
                rows={6}
                value={pasted}
                onChange={(e) => setPasted(e.target.value)}
                onBlur={() => pasted.trim() && parsePasted.mutate(pasted)}
                placeholder="hostname sw-01&#10;vlan 10&#10; name Bureaux&#10;interface GigabitEthernet1/0/1&#10; switchport mode access&#10; switchport access vlan 10"
              />
            </Field>
          )}
          {parseError && (
            <div className="alert-row" style={{ cursor: "default" }}>
              <span className="dot" style={{ background: "var(--warn)" }} />
              <span style={{ fontSize: 12 }}>{t("adoptModal.parseImpossible", { error: parseError })}</span>
            </div>
          )}
          {parsed && (
            <div className="card" style={{ padding: 10, marginTop: 6 }}>
              <div className="note" style={{ marginBottom: 6 }}>
                {t("adoptModal.parsedSummary", {
                  dialect: parsed.dialect,
                  vlans: parsed.vlans.length,
                  ports: Object.keys(parsed.ports).length,
                  groups: parsed.port_groups.length,
                })}
                {parsed.hostname ? ` · ${t("adoptModal.hostnameSuffix", { hostname: parsed.hostname })}` : ""}
              </div>
              <div className="chip-row">
                {parsed.vlans.slice(0, 24).map((v) => (
                  <span key={v.vlan_id} className="chip">
                    {v.vlan_id}
                    {v.name ? " · " + v.name : ""}
                  </span>
                ))}
                {parsed.vlans.length > 24 && (
                  <span className="note">+{parsed.vlans.length - 24}</span>
                )}
              </div>
              {parsed.warnings.length > 0 && (
                <ul className="warn-list">
                  {parsed.warnings.map((wn, i) => (
                    <li key={i}>{wn}</li>
                  ))}
                </ul>
              )}
            </div>
          )}
        </>
      )}

      <ErrorMsg error={adopt.error} />
      <div className="actions">
        <ActionButton kind="cancel" className="secondary" onClick={onClose}>
          {t("common.cancel")}
        </ActionButton>
        <ActionButton
          kind="adopt"
          disabled={!siteId || !name.trim() || adopt.isPending}
          onClick={() => adopt.mutate()}
        >
          {adopt.isPending ? t("adoptModal.adopting") : t("adoptModal.adopt")}
        </ActionButton>
      </div>
    </Modal>
  );
}

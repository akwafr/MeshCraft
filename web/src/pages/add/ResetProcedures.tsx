import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { get, type ResetProcedure } from "../../api";
import { ErrorMsg } from "../../components";

/**
 * Reset procedures, carried by the drivers.
 *
 * The user searches for their device (vendor, range, reference) and gets the
 * two paths: via the CLI when access is still available, via the hardware
 * when it's been lost.
 */
export default function ResetProcedures() {
  const { t } = useTranslation();
  const [q, setQ] = useState("");

  const { data, error, isFetching } = useQuery({
    queryKey: ["reset-procedures", q],
    queryFn: () =>
      get<ResetProcedure[]>(
        `/api/discovery/reset-procedures?q=${encodeURIComponent(q)}`,
      ),
  });

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.resetEquipment")}
          <span className="sub">{t("resetProcedures.subtitle")}</span>
        </h1>
      </div>

      <div className="card" style={{ marginBottom: 12 }}>
        <input
          className="search"
          style={{ width: "100%" }}
          placeholder={t("resetProcedures.searchPlaceholder") as string}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          autoFocus
        />
        <div className="note" style={{ marginTop: 8 }}>
          {t("resetProcedures.warning")}
        </div>
      </div>

      <ErrorMsg error={error} />
      {isFetching && <div className="note">{t("common.searching")}</div>}

      {(data ?? []).length === 0 && !isFetching ? (
        <div className="card">
          <div className="empty">{t("resetProcedures.noMatch", { query: q })}</div>
        </div>
      ) : (
        <div className="reset-list">
          {(data ?? []).map((p) => (
            <div key={p.driver} className="card">
              <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                <span style={{ fontWeight: 700, fontSize: 15 }}>{p.family}</span>
                <span className="chip">{p.vendor}</span>
                <span className="note">{t("resetProcedures.driverLabel", { driver: p.driver })}</span>
                <span style={{ marginLeft: "auto" }}>
                  {p.verified ? (
                    <span className="badge">{t("resetProcedures.stableProcedure")}</span>
                  ) : (
                    <span className="badge warn">{t("resetProcedures.toConfirm")}</span>
                  )}
                </span>
              </div>

              <div className="reset-cols">
                {p.software.length > 0 && (
                  <div>
                    <div className="kicker">{t("resetProcedures.viaCli")}</div>
                    <ol className="reset-steps">
                      {p.software.map((s, i) => (
                        <li key={i}>
                          <code>{s}</code>
                        </li>
                      ))}
                    </ol>
                  </div>
                )}
                {p.hardware.length > 0 && (
                  <div>
                    <div className="kicker">{t("resetProcedures.viaHardware")}</div>
                    <ol className="reset-steps">
                      {p.hardware.map((s, i) => (
                        <li key={i}>{s}</li>
                      ))}
                    </ol>
                  </div>
                )}
              </div>

              <div className="note" style={{ marginTop: 8 }}>
                <b>{t("resetProcedures.erasesLabel")}</b> {p.erases}
              </div>
              {p.caveats.length > 0 && (
                <ul className="warn-list">
                  {p.caveats.map((c, i) => (
                    <li key={i}>{c}</li>
                  ))}
                </ul>
              )}
              {p.aliases.length > 0 && (
                <div className="chip-row" style={{ marginTop: 8 }}>
                  {p.aliases.map((a) => (
                    <span key={a} className="chip">
                      {a}
                    </span>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

import { useEffect, useRef, useState } from "react";
import { viewerDate } from "../ui/clock";
import { useTranslation } from "react-i18next";
import { get } from "../api";
import { ErrorMsg, HelpTip } from "../components";
import SerialServerPanel from "./SerialServer";

/**
 * Debug zone: the live log of the APPLICATION.
 * See docs/en/01-Architecture/Unified monitoring and pipelines §6.
 *
 * Not to be confused with the event log, which speaks to the operator about
 * the fleet. Here meshcraft speaks to whoever is diagnosing it. Reserved to
 * the global admin: internal errors describe the product's structure.
 */

interface Line {
  seq: number;
  ts: string;
  level: string;
  msg: string;
  attrs?: Record<string, string>;
}

const COLOR: Record<string, string> = {
  DEBUG: "var(--text-dim)",
  INFO: "var(--text-dim)",
  WARN: "var(--warn)",
  ERROR: "var(--danger)",
};

/** How many lines are kept on screen. Beyond that, the browser struggles. */
const MAX_SHOWN = 1000;

export default function DebugPage() {
  const { t, i18n } = useTranslation();
  const LEVELS = ["", "INFO", "WARN", "ERROR"];
  const [level, setLevel] = useState("");
  const [lines, setLines] = useState<Line[]>([]);
  const [live, setLive] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const boxRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  // History first: opening on an empty screen while waiting for something to
  // happen tells you nothing.
  useEffect(() => {
    setLines([]);
    get<{ lines: Line[] }>(`/api/debug/log?level=${level}`)
      .then((d) => setLines(d.lines.slice(-MAX_SHOWN)))
      .catch(setError);
  }, [level]);

  // Then live.
  useEffect(() => {
    if (!live) return;
    const es = new EventSource(`/api/debug/stream?level=${level}`);
    es.onmessage = (e) => {
      const l = JSON.parse(e.data) as Line;
      setLines((prev) => [...prev, l].slice(-MAX_SHOWN));
    };
    // The browser reconnects on its own; no need to show an error at every
    // network hiccup, that would be noise on a diagnostic screen.
    es.onerror = () => {};
    return () => es.close();
  }, [live, level]);

  // Follow the bottom of the stream, UNLESS the user has scrolled up to read:
  // yanking their position away at every line would make the screen unusable.
  useEffect(() => {
    const el = boxRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [lines]);

  return (
    <>
      {/* Shared serial lines live here rather than on the Add wizard: this is
          the diagnostics screen, and attaching to somebody else's console is a
          diagnostic act — not a step in adding a device. */}
      <SerialServerPanel />
      <div className="toolbar">
        <h1>
          Debug
          <HelpTip label={t("debugPage.helpLabel") as string}>
            <p>{t("debugPage.help1")}</p>
            <p>{t("debugPage.help2")}</p>
          </HelpTip>
          <span className="sub">{t("debugPage.subtitle")}</span>
        </h1>
        <div className="row" style={{ gap: 8 }}>
          <select value={level} onChange={(e) => setLevel(e.target.value)} style={{ width: 130 }}>
            {LEVELS.map((n) => (
              <option key={n} value={n}>
                {n === "" ? t("debugPage.allLevels") : t("debugPage.andAbove", { level: n })}
              </option>
            ))}
          </select>
          <button className={live ? "" : "secondary"} onClick={() => setLive(!live)}>
            {live ? t("debugPage.live") : t("debugPage.paused")}
          </button>
          <button className="ghost" onClick={() => setLines([])}>
            {t("common.clear")}
          </button>
        </div>
      </div>

      <ErrorMsg error={error} />

      <div className="card">
        <div className="debug-log" ref={boxRef} onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
        }}>
          {lines.length === 0 && <div className="empty">{t("debugPage.noLines")}</div>}
          {lines.map((l) => (
            <div key={l.seq} className="debug-line">
              <span className="debug-ts">
                {(viewerDate(l.ts) ?? new Date(l.ts)).toLocaleTimeString(i18n.language)}
              </span>
              <span style={{ color: COLOR[l.level] ?? "var(--text-dim)" }}>
                {l.level}
              </span>
              <span>
                {l.msg}
                {l.attrs && (
                  <span className="debug-attrs">
                    {Object.entries(l.attrs).map(([k, v]) => ` ${k}=${v}`)}
                  </span>
                )}
              </span>
            </div>
          ))}
        </div>
      </div>
    </>
  );
}

/**
 * The device CONSOLE overlay: a real shell, or the vendor's own web interface,
 * on top of the switch sheet.
 *
 * # Two panels, one button, and why the distinction is drawn server-side
 *
 * What an operator gets depends on what the platform actually is. A Catalyst or
 * an FS S3900 has SSH, so this opens a terminal. An Instant On 1930 has no SSH at
 * all — its web interface is the only way to configure one — so this opens that
 * interface, relayed through the controller because the management VLAN is not
 * routable from the operator's laptop.
 *
 * The choice is NOT made here. `GET /api/devices/:id/console` reports what the
 * connector declares, and this component draws what it is told. Deciding in the
 * frontend would mean a second place that knows which vendors speak SSH, and the
 * two would disagree the first time a connector gained a transport.
 *
 * # Teardown is the part that matters
 *
 * A vty is scarce — a 2960 has sixteen — so an abandoned console is not a
 * cosmetic leak, it is a switch nobody can log into. The socket is closed on
 * unmount, on Escape, on the close button, and by the controller's own idle
 * timer. `useEffect`'s cleanup is what covers the case nothing else does: the
 * operator navigating away with the overlay open.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

import { get, post } from "../api";
import { ActionButton } from "../ActionButton";

/** What the controller says this device offers (cf. console_handlers.go). */
type ConsoleInfo = {
  device: string;
  connector?: string;
  transports?: string[];
  ssh: boolean;
  web: boolean;
  /** The device's OWN administration URL, e.g. "https://10.99.0.38". */
  web_url?: string;
  web_auth?: "none" | "basic" | "digest" | "form";
  proxy_id?: string;
  username?: string;
  credential_id?: string;
  credential_label?: string;
  reason?: string;
};

/** Control protocol, mirror of internal/console/relay.go. */
type ConsoleStatus = {
  op: "ready" | "closed" | "error";
  username?: string;
  why?: string;
  reason?: string;
};

export default function DeviceConsole({
  deviceId,
  deviceName,
  siteId,
  onClose,
}: {
  deviceId: string;
  deviceName: string;
  siteId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const { data: info, error, isLoading } = useQuery({
    queryKey: ["console-info", deviceId],
    queryFn: () => get<ConsoleInfo>(`/api/devices/${deviceId}/console`),
    // Capabilities do not change while an overlay is open, and re-asking on
    // window focus would fire a request every time the operator alt-tabs back
    // to the terminal they are typing in.
    refetchOnWindowFocus: false,
    staleTime: Infinity,
  });

  // Escape closes, like every other overlay in the product. Registered on the
  // window rather than the panel so it works while the terminal has focus —
  // xterm swallows keystrokes on its own textarea.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div className="modal-backdrop console-backdrop" onMouseDown={(e) => {
      // Backdrop click closes, but only when it IS the backdrop: a drag that
      // starts on a text selection inside the terminal and releases outside
      // must not tear the session down.
      if (e.target === e.currentTarget) onClose();
    }}>
      <div className="console-panel" onMouseDown={(e) => e.stopPropagation()}>
        <div className="console-head">
          <div>
            <div className="kicker">{t("console.title")}</div>
            <div className="console-device">{deviceName}</div>
          </div>
          <ActionButton kind="close" className="ghost" onClick={onClose}>
            {t("console.close")}
          </ActionButton>
        </div>

        {isLoading && <div className="console-note">{t("console.loading")}</div>}
        {error && <div className="console-note error">{(error as Error).message}</div>}

        {info && info.ssh && (
          <SSHTerminal deviceId={deviceId} onClose={onClose} />
        )}
        {info && !info.ssh && info.web && info.web_url && (
          <WebUIFrame info={info} siteId={siteId} />
        )}
        {info && !info.ssh && !info.web && (
          <div className="console-note error">
            {info.reason ?? t("console.noAccess")}
          </div>
        )}
      </div>
    </div>
  );
}

/**
 * The terminal half.
 *
 * xterm.js is mounted once, imperatively, and deliberately NOT re-created on
 * every render: it owns a canvas and a DOM subtree, and rebuilding it would
 * clear the scrollback an operator is reading.
 */
function SSHTerminal({ deviceId, onClose }: { deviceId: string; onClose: () => void }) {
  const { t } = useTranslation();
  const holder = useRef<HTMLDivElement | null>(null);
  const [state, setState] = useState<"connecting" | "open" | "closed">("connecting");
  const [note, setNote] = useState<string>("");

  useEffect(() => {
    const el = holder.current;
    if (!el) return;

    const term = new Terminal({
      convertEol: false,
      cursorBlink: true,
      fontSize: 13,
      fontFamily:
        'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      // Scrollback is what makes a `show running-config` usable at all. 5000
      // lines is roughly a full config on a 48-port switch.
      scrollback: 5000,
      theme: { background: "#0b0e14", foreground: "#d7dce5" },
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(el);
    fit.fit();

    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const ws = new WebSocket(
      `${proto}//${window.location.host}/api/devices/${deviceId}/console/ws`,
    );
    ws.binaryType = "arraybuffer";

    const decoder = new TextDecoder();
    const encoder = new TextEncoder();

    const sendResize = () => {
      if (ws.readyState !== WebSocket.OPEN) return;
      ws.send(JSON.stringify({ op: "resize", cols: term.cols, rows: term.rows }));
    };

    ws.onopen = () => {
      setState("open");
      // The pty was sized from this component's defaults; the real geometry is
      // only known once the panel has laid out, so it is sent immediately.
      sendResize();
      term.focus();
    };

    ws.onmessage = (ev) => {
      if (typeof ev.data === "string") {
        // Text frames are the control protocol and NEVER terminal output —
        // writing them to the screen would print JSON at the operator.
        let st: ConsoleStatus | null = null;
        try {
          st = JSON.parse(ev.data) as ConsoleStatus;
        } catch {
          return;
        }
        if (st.op === "ready" && st.username) {
          setNote(t("console.connectedAs", { user: st.username, why: st.why ?? "" }));
        }
        if (st.op === "closed") {
          setState("closed");
          setNote(st.reason ?? "");
          term.writeln("");
          term.writeln(`\x1b[33m── ${st.reason ?? t("console.sessionEnded")} ──\x1b[0m`);
        }
        return;
      }
      term.write(decoder.decode(new Uint8Array(ev.data as ArrayBuffer)));
    };

    ws.onerror = () => {
      setState("closed");
      setNote(t("console.transportError"));
    };
    ws.onclose = () => setState((s) => (s === "closed" ? s : "closed"));

    const offData = term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(encoder.encode(data));
    });

    // Resize follows the panel, not just the window: the overlay is sized in
    // viewport units, so a browser zoom changes the geometry without ever
    // firing a window resize.
    const ro = new ResizeObserver(() => {
      try {
        fit.fit();
      } catch {
        /* the panel can be measured as zero mid-layout */
      }
      sendResize();
    });
    ro.observe(el);

    return () => {
      // TEARDOWN, in the order that matters: stop feeding the socket, close it,
      // then dispose the terminal. Disposing first would let a late frame write
      // into a destroyed renderer.
      offData.dispose();
      ro.disconnect();
      if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
        ws.close(1000, "console closed");
      }
      term.dispose();
    };
  }, [deviceId, t]);

  return (
    <>
      <div className="console-term" ref={holder} />
      <div className="console-foot">
        <span className={"console-state " + state}>
          {state === "connecting"
            ? t("console.connecting")
            : state === "open"
              ? t("console.open")
              : t("console.closed")}
        </span>
        {note && <span className="note">{note}</span>}
        {state === "closed" && (
          <ActionButton kind="close" className="ghost" onClick={onClose}>
            {t("console.close")}
          </ActionButton>
        )}
      </div>
    </>
  );
}

/**
 * The web-interface half, for platforms with no SSH.
 *
 * # Why this frames the DEVICE and not a relay
 *
 * The first version of this proxied the vendor UI through the controller,
 * rewriting every URL back under our origin. It was removed. A vendor interface
 * is an application, not a document — it computes URLs, scopes cookies to paths,
 * ships bundles a regex must never touch — and a rewriter is a standing bet that
 * none of that changes at the next firmware. Losing that bet looks like a broken
 * switch.
 *
 * The route now comes from a SOCKS proxy (internal/socks) that dials through the
 * meshcraft proxy which can actually reach the device. The browser then talks to
 * the device at its OWN address, with its own TLS, cookies and paths, and
 * nothing in the middle edits anything.
 *
 * # Two consequences this panel has to be honest about
 *
 * Nothing strips `X-Frame-Options` any more, so a device that sends SAMEORIGIN —
 * most of them do — renders as a blank frame, and a cross-origin iframe cannot
 * reliably report that it was refused. Hence the frame is offered AND a plain
 * "open in a new tab" sits next to it, rather than a frame that silently fails.
 *
 * And the login is the operator's to perform. Pre-authenticating a form-based
 * vendor UI would mean reproducing its login here, on a range that grants five
 * sessions total. The credential is offered for copying instead, which is honest
 * about who does what.
 */
function WebUIFrame({ info, siteId }: { info: ConsoleInfo; siteId: string }) {
  const { t } = useTranslation();
  const [secret, setSecret] = useState<string | null>(null);
  const [copied, setCopied] = useState<"" | "user" | "secret">("");
  const [revealing, setRevealing] = useState(false);
  const [revealErr, setRevealErr] = useState("");
  // The frame is opt-in. Loading it unprompted on a device that refuses framing
  // shows a blank rectangle and teaches the operator to distrust the panel.
  const [framed, setFramed] = useState(false);

  const copy = useCallback(async (what: "user" | "secret", value: string) => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(what);
      setTimeout(() => setCopied(""), 1500);
    } catch {
      setRevealErr(t("console.clipboardDenied"));
    }
  }, [t]);

  // REVEALING IS A TRACED ACTION and reuses the existing audited endpoint rather
  // than a quieter one of its own: it emits `credential.revealed` at warning
  // severity, exactly as the Credentials page does.
  const reveal = useCallback(async () => {
    if (!info.credential_id) return;
    setRevealing(true);
    setRevealErr("");
    try {
      const r = await post<{ secret: string }>(
        `/api/sites/${siteId}/credentials/${info.credential_id}/reveal`,
      );
      setSecret(r.secret);
    } catch (e) {
      setRevealErr((e as Error).message);
    } finally {
      setRevealing(false);
    }
  }, [info.credential_id, siteId]);

  return (
    <>
      <div className="console-webnote">
        <span className="note">
          {t("console.webOnly", { connector: info.connector ?? "" })}
        </span>
        <span className="row" style={{ gap: 8, flexWrap: "wrap" }}>
          {info.username && (
            <button
              className="ghost"
              onClick={() => copy("user", info.username!)}
              title={t("console.copyUserTitle")}
            >
              {copied === "user" ? t("console.copied") : `${t("console.user")}: ${info.username}`}
            </button>
          )}
          {info.credential_id &&
            (secret === null ? (
              <ActionButton kind="role" className="ghost" disabled={revealing} onClick={reveal}>
                {revealing ? t("console.revealing") : t("console.revealPassword")}
              </ActionButton>
            ) : (
              <ActionButton kind="copy" className="ghost" onClick={() => copy("secret", secret)}>
                {copied === "secret" ? t("console.copied") : t("console.copyPassword")}
              </ActionButton>
            ))}
          {info.web_url && (
            <ActionButton
              kind="link"
              onClick={() => window.open(info.web_url, "_blank", "noopener,noreferrer")}
            >
              {t("console.openInTab")}
            </ActionButton>
          )}
        </span>
      </div>
      {revealErr && <div className="console-note error">{revealErr}</div>}

      <SocksSetup url={info.web_url ?? ""} />

      {framed && info.web_url ? (
        <iframe
          className="console-frame"
          src={info.web_url}
          title={t("console.frameTitle", { device: info.device })}
          // A third-party origin: sandboxed, with same-origin granted because the
          // vendor's own scripts and session cookie need it, and top-navigation
          // withheld so a redirect inside the device UI cannot navigate the whole
          // product away.
          sandbox="allow-forms allow-scripts allow-same-origin allow-popups allow-modals"
        />
      ) : (
        <div className="console-frame console-frame-empty">
          <div className="note" style={{ maxWidth: 520, textAlign: "center" }}>
            {t("console.frameHint")}
          </div>
          <ActionButton kind="play" className="ghost" onClick={() => setFramed(true)}>
            {t("console.tryFrame")}
          </ActionButton>
        </div>
      )}
    </>
  );
}

/**
 * How to reach the device at all: the SOCKS route, and the credential for it.
 *
 * Placed inside the web panel rather than on a settings page because this is
 * where the need appears — an operator looking at a switch they cannot open. The
 * token is shown ONCE, on generation, like every other secret meshcraft mints;
 * regenerating replaces the previous one, which is also how it is revoked.
 */
function SocksSetup({ url }: { url: string }) {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [token, setToken] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const { data } = useQuery({
    queryKey: ["socks-token"],
    queryFn: () => get<{
      username: string; has_token: boolean; created_at?: string; listen?: string;
    }>("/api/auth/socks-token"),
    refetchOnWindowFocus: false,
  });

  const generate = useCallback(async () => {
    setBusy(true);
    setErr("");
    try {
      const r = await post<{ token: string }>("/api/auth/socks-token");
      setToken(r.token);
      qc.invalidateQueries({ queryKey: ["socks-token"] });
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  }, [qc]);

  return (
    <div className="console-note">
      <div>{t("console.socksNeeded", { url })}</div>
      {data && (
        <div className="row" style={{ gap: 10, marginTop: 8, flexWrap: "wrap" }}>
          <span className="note">
            {t("console.socksUser")}: <b className="mono">{data.username}</b>
          </span>
          {data.listen && (
            <span className="note">
              {t("console.socksAddr")}: <b className="mono">{data.listen}</b>
            </span>
          )}
          <ActionButton kind="role" className="ghost" disabled={busy} onClick={generate}>
            {data.has_token ? t("console.socksRegen") : t("console.socksGen")}
          </ActionButton>
        </div>
      )}
      {/* Shown once and never again — the controller stores only a hash. */}
      {token && (
        <div style={{ marginTop: 8 }}>
          <div className="note">{t("console.socksOnce")}</div>
          <pre className="code" style={{ fontSize: 14 }}>{token}</pre>
          <ActionButton kind="copy" className="ghost"
            onClick={() => navigator.clipboard.writeText(token)}>
            {t("console.copyPassword")}
          </ActionButton>
        </div>
      )}
      {err && <div className="error" style={{ marginTop: 6 }}>{err}</div>}
    </div>
  );
}

import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { get, type Site } from "../api";
import { ErrorMsg } from "../components";
import { autodetectSerialParams } from "./add/serialAutodetect";
import { DEFAULT_SERIAL_PARAMS, type SerialParams } from "./add/serialTypes";

/**
 * SHARING A SERIAL LINE, and attaching to somebody else's.
 *
 * Web Serial only works in the browser physically cabled to the switch, which
 * is the right security model and also a hard operational limit: the person
 * holding the console cable is not always the person who knows what to type,
 * and a switch being provisioned over serial is usually one that cannot be
 * reached any other way.
 *
 * So this component does two unrelated jobs that belong on one screen:
 *
 *   - HOST — take the serial port this browser already has, and pump it through
 *     the controller so another site administrator can use it;
 *   - ATTACH — list what other people are offering, and open one.
 *
 * Everything about the risk is stated in internal/api/serialserver.go. The one
 * thing worth repeating where the button lives: publishing a console is handing
 * over the most privileged access a switch has, it is audited under both
 * operators' names, and it ends when this tab closes.
 */

interface Offer {
  id: string;
  label: string;
  host_user: string;
  since: string;
  attached_by?: string;
}

/** Minimal Web Serial surface — the DOM lib does not ship these types. */
interface SerialPortLike {
  open(o: { baudRate: number }): Promise<void>;
  close(): Promise<void>;
  readable: ReadableStream<Uint8Array> | null;
  writable: WritableStream<Uint8Array> | null;
}

function wsURL(path: string): string {
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  return `${proto}//${location.host}${path}`;
}

export default function SerialServerPanel() {
  const { t } = useTranslation();
  // THE SITE IS CHOSEN HERE, not taken from the route.
  //
  // This panel lives on /config/debug, which has NO site in its path — so
  // `useSiteId()` returned "" and every request went to `/api/sites//…`. chi
  // matched an empty {siteID}, `CanSite` answers true for a global admin
  // whatever site it is asked about, and the offer was registered against site
  // "" where no list could ever find it. The feature did nothing and said
  // nothing. A serial line belongs to a site — that is the authorization
  // boundary for attaching to it — so the site is an explicit choice.
  const [siteId, setSiteId] = useState("");
  const sites = useQuery({ queryKey: ["sites"], queryFn: () => get<Site[]>("/api/sites") });
  useEffect(() => {
    if (!siteId && sites.data && sites.data.length > 0) setSiteId(sites.data[0].id);
  }, [sites.data, siteId]);
  const [detected, setDetected] = useState<SerialParams | null>(null);
  const [offers, setOffers] = useState<Offer[]>([]);
  const [error, setError] = useState<unknown>(null);
  const [hosting, setHosting] = useState(false);
  const [label, setLabel] = useState("");
  const [attached, setAttached] = useState<string | null>(null);
  const [screen, setScreen] = useState("");
  const hostWS = useRef<WebSocket | null>(null);
  const portRef = useRef<SerialPortLike | null>(null);
  const attachWS = useRef<WebSocket | null>(null);
  const screenRef = useRef<HTMLPreElement>(null);

  const refresh = useCallback(async () => {
    if (!siteId) return;
    try {
      const r = await get<{ servers: Offer[] }>(
        `/api/sites/${siteId}/serial-servers`,
      );
      setOffers(r.servers ?? []);
    } catch (e) {
      setError(e);
    }
  }, [siteId]);

  useEffect(() => {
    refresh();
    // Polled rather than pushed: an offer appears when somebody plugs in a
    // cable, which is a human-timescale event. A second WebSocket to watch a
    // list that changes twice an hour would be more moving parts than the
    // feature it serves.
    const h = setInterval(refresh, 5000);
    return () => clearInterval(h);
  }, [refresh]);

  /** Publish this browser's serial port. */
  const startHosting = async () => {
    setError(null);
    try {
      if (!siteId) throw new Error(t("serialServer.errNoSite"));
      // The port is requested HERE and not reused from the wizard: Web Serial
      // grants a port to a user gesture, and borrowing a handle another screen
      // opened would publish a line the operator did not choose to publish.
      const nav = navigator as unknown as {
        serial?: { requestPort(): Promise<SerialPortLike> };
      };
      if (!nav.serial) throw new Error(t("serialServer.noWebSerial"));
      const port = await nav.serial.requestPort();
      // BAUD IS DETECTED, NOT ASSUMED. The wizard already carries the trial
      // matrix and the scoring — reused rather than reimplemented, because a
      // second copy would drift and this one is the one that has been run
      // against the fleet. 9600 was a guess that happens to suit Cisco and
      // Arista consoles and is wrong on the FS ranges (115200), where the
      // shared line would have produced nothing but line noise.
      // AUTODETECT LEAVES THE PORT OPEN ON THE WINNING PARAMETERS.
      //
      // Stated here because the first version of this function assumed the
      // opposite and called `port.open()` afterwards — which throws
      // InvalidStateError, was swallowed by the catch below, and meant the
      // WebSocket was never opened at all. The symptom was a line that never
      // appeared to anybody, with nothing on the server to look at: no request
      // ever reached it.
      //
      // `autodetectSerialParams` opens each candidate, and either returns early
      // with the port still open on the one that scored (its own comment says
      // "Port already open with these params") or re-opens the best at the end.
      // Either way it hands back an OPEN port, so there is nothing to do but
      // keep it.
      let params: SerialParams;
      try {
        const found = await autodetectSerialParams({
          port: port as never,
          equipmentHint: label,
          log: (m) => setScreen((s2) => (s2 + m + "\n").slice(-20000)),
        });
        params = found.params;
      } catch {
        // It THROWS when nothing scores — a console that is simply silent,
        // which is the normal state of a switch sitting at a prompt with
        // nobody typing. That must not stop the line being shared: fall back
        // to the documented default, open it here (autodetect closed the port
        // on its way out of the loop), and let the operator see the rate that
        // was assumed rather than detected.
        params = DEFAULT_SERIAL_PARAMS;
        await port.open({ baudRate: params.baudRate });
        setScreen((s2) => (s2 + t("serialServer.detectFailed") + "\n").slice(-20000));
      }
      setDetected(params);
      portRef.current = port;

      // PROBE FIRST, over ordinary HTTP, and report what it says.
      //
      // The browser's WebSocket API deliberately hides the HTTP status from
      // `onerror` — a failed upgrade, a 404, a 403 and a rejected Origin all
      // arrive as one contentless event. So a message written from `onerror`
      // alone can only ever say "it did not work", which is exactly the
      // unhelpful thing this codebase refuses to ship: the first time this ran
      // against a controller whose route was mounted under the wrong prefix, the
      // operator was told "the controller refused the serial line" and had
      // nothing to act on.
      //
      // One GET on the list endpoint distinguishes the cases that matter — route
      // absent (controller too old, or not deployed yet), insufficient rights,
      // no site selected — and it costs one request before a gesture that opens
      // a console.
      const probe = await fetch(`/api/sites/${siteId}/serial-servers`, {
        credentials: "same-origin",
      });
      if (!probe.ok) {
        throw new Error(
          probe.status === 404
            ? t("serialServer.errNoRoute")
            : probe.status === 403
              ? t("serialServer.errForbidden")
              : `${t("serialServer.hostFailed")} (HTTP ${probe.status})`,
        );
      }

      const ws = new WebSocket(
        wsURL(
          `/api/sites/${siteId}/serial-servers/host?label=${encodeURIComponent(
            label || t("serialServer.defaultLabel"),
          )}&baud=${params.baudRate}`,
        ),
      );
      ws.binaryType = "arraybuffer";
      hostWS.current = ws;
      ws.onopen = () => {
        setHosting(true);
        // Only now is readyState OPEN, which is what the pump requires.
        void pump();
      };
      ws.onclose = () => stopHosting();
      // Reached only AFTER the probe succeeded, so the route exists and the
      // rights are right: what is left is the socket itself. Said as such
      // rather than repeating the generic refusal.
      ws.onerror = () => setError(new Error(t("serialServer.errSocket")));

      // Controller → device. What the remote operator types.
      // Controller → device. SERIALISED through one promise chain.
      //
      // `getWriter()` locks the stream, so two frames arriving close together —
      // which is exactly what a burst of keystrokes is — would have the second
      // call throw while the first still held the lock, and that keystroke would
      // be silently lost. Chaining keeps one writer in flight at a time without
      // needing a queue of our own.
      let writeChain: Promise<void> = Promise.resolve();
      ws.onmessage = (ev) => {
        const data =
          ev.data instanceof ArrayBuffer
            ? new Uint8Array(ev.data)
            : new TextEncoder().encode(String(ev.data));
        writeChain = writeChain
          .then(async () => {
            const w = port.writable?.getWriter();
            if (!w) return;
            try {
              await w.write(data);
            } finally {
              w.releaseLock();
            }
          })
          .catch(() => {
            // Adapter unplugged mid-write. The read loop's guard ends hosting;
            // dropping this keystroke is the correct outcome.
          });
      };

      // Device → controller. STARTED FROM onopen, and that is the whole point.
      //
      // The first version started it inline, right after `new WebSocket(...)`.
      // At that instant `readyState` is CONNECTING (0), not OPEN (1) — so the
      // loop's own guard was false on its first evaluation, it exited before the
      // socket had finished connecting, and it never ran again. The device half
      // of the relay was dead from the start: the offer published, both sockets
      // upgraded, an attacher could connect, and not one byte ever left the
      // switch. Measured as exactly zero bytes across a real attach.
      //
      // A socket that is CONNECTING is not a socket that is closed, and a guard
      // written as `=== OPEN` cannot tell the difference. Starting from onopen
      // removes the question instead of restating it.
      const pump = async () => {
        while (portRef.current && ws.readyState === WebSocket.OPEN) {
          if (!port.readable) {
            await new Promise((r) => setTimeout(r, 200));
            continue;
          }
          const reader = port.readable.getReader();
          try {
            for (;;) {
              const { value, done } = await reader.read();
              if (done) break;
              if (value && ws.readyState === WebSocket.OPEN) ws.send(value);
            }
          } catch {
            // Adapter went away mid-read; the outer loop re-attaches.
          } finally {
            reader.releaseLock();
          }
        }
      };
    } catch (e) {
      setError(e);
      stopHosting();
    }
  };

  const stopHosting = () => {
    setHosting(false);
    hostWS.current?.close();
    hostWS.current = null;
    portRef.current?.close().catch(() => {});
    portRef.current = null;
    refresh();
  };

  /** Attach to somebody else's line. */
  const attach = (id: string) => {
    setError(null);
    setScreen("");
    const ws = new WebSocket(
      wsURL(`/api/sites/${siteId}/serial-servers/${id}/attach`),
    );
    ws.binaryType = "arraybuffer";
    attachWS.current = ws;
    ws.onopen = () => setAttached(id);
    ws.onclose = () => {
      setAttached(null);
      attachWS.current = null;
      refresh();
    };
    ws.onmessage = (ev) => {
      const text =
        ev.data instanceof ArrayBuffer
          ? new TextDecoder().decode(new Uint8Array(ev.data))
          : String(ev.data);
      setScreen((s) => (s + text).slice(-20000));
    };
  };

  const detach = () => {
    attachWS.current?.close();
    attachWS.current = null;
    setAttached(null);
  };

  // Keystrokes go out one at a time, unbuffered and unechoed locally: this is a
  // console, and the device is what decides whether a character appears.
  const onKey = (e: React.KeyboardEvent) => {
    const ws = attachWS.current;
    if (!ws || ws.readyState !== WebSocket.OPEN) return;
    let out = "";
    if (e.key === "Enter") out = "\r";
    else if (e.key === "Backspace") out = "\x7f";
    else if (e.key === "Tab") out = "\t";
    else if (e.key.length === 1) out = e.ctrlKey
      ? String.fromCharCode(e.key.toUpperCase().charCodeAt(0) - 64)
      : e.key;
    if (!out) return;
    e.preventDefault();
    ws.send(new TextEncoder().encode(out));
  };

  useEffect(() => {
    if (screenRef.current) screenRef.current.scrollTop = screenRef.current.scrollHeight;
  }, [screen]);

  // Closing the tab must end the offer. The socket dying is what withdraws it
  // server-side, so this only makes the port release deterministic.
  useEffect(() => () => stopHosting(), []); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <section className="card">
      <div className="card-head">
        <h2>{t("serialServer.title") as string}</h2>
      </div>
      <p className="note">{t("serialServer.help") as string}</p>
      {error ? <ErrorMsg error={error} /> : null}

      <div className="row">
        <select
          value={siteId}
          onChange={(e) => setSiteId(e.target.value)}
          disabled={hosting}
          aria-label={t("serialServer.siteLabel") as string}
        >
          {(sites.data ?? []).map((s2) => (
            <option key={s2.id} value={s2.id}>
              {s2.name}
            </option>
          ))}
        </select>
        <input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder={t("serialServer.labelPlaceholder") as string}
          disabled={hosting}
          aria-label={t("serialServer.labelPlaceholder") as string}
        />
        {hosting ? (
          <button className="danger" onClick={stopHosting}>
            {t("serialServer.stop") as string}
          </button>
        ) : (
          <button onClick={startHosting}>{t("serialServer.enable") as string}</button>
        )}
        <button onClick={refresh}>{t("serialServer.list") as string}</button>
      </div>
      {hosting && (
        <>
          <p className="warn">{t("serialServer.hostingWarning") as string}</p>
          {detected && (
            <p className="note">
              {t("serialServer.detected", { baud: detected.baudRate })}
            </p>
          )}
        </>
      )}

      <table className="table">
        <thead>
          <tr>
            <th>{t("serialServer.colLabel") as string}</th>
            <th>{t("serialServer.colHost") as string}</th>
            <th>{t("serialServer.colInUse") as string}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {offers.length === 0 && (
            <tr>
              <td colSpan={4} className="note">
                {t("serialServer.empty") as string}
              </td>
            </tr>
          )}
          {offers.map((o) => (
            <tr key={o.id}>
              <td>{o.label}</td>
              <td>{o.host_user}</td>
              <td>
                {o.attached_by ? (
                  <span className="warn">{o.attached_by}</span>
                ) : (
                  <span className="note">—</span>
                )}
              </td>
              <td>
                {attached === o.id ? (
                  <button className="danger" onClick={detach}>
                    {t("serialServer.detach") as string}
                  </button>
                ) : (
                  <button
                    onClick={() => attach(o.id)}
                    /* One attacher at a time — two people typing into one
                       console interleave keystrokes into a single command
                       line. The server refuses the second; the button is
                       disabled so nobody has to discover that from an error. */
                    disabled={!!o.attached_by || !!attached}
                  >
                    {t("serialServer.attach") as string}
                  </button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      {attached && (
        <>
          <p className="note">{t("serialServer.terminalHelp") as string}</p>
          <pre
            ref={screenRef}
            className="console-screen"
            tabIndex={0}
            onKeyDown={onKey}
            aria-label={t("serialServer.title") as string}
          >
            {screen}
          </pre>
        </>
      )}
    </section>
  );
}

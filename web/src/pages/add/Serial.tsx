import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { get, post, type Discovered, type DriverRef } from "../../api";
import { ActionButton, ErrorMsg, Field, useToast } from "../../components";
import AdoptModal from "./AdoptModal";

/**
 * Add via serial console.
 *
 * The browser talks directly to the serial port (Web Serial API): it holds
 * the cable, the controller only sees the captured text. This is exactly the
 * targeted use case — a new or reset device, with no reachable IP yet,
 * plugged in via serial to identify it and read its configuration.
 *
 * Real API constraints: a Chromium browser (Chrome, Edge, Opera) and an
 * HTTPS page. Firefox and Safari don't implement it; we say so instead of
 * leaving a dead button.
 */

type SerialPortLike = {
  open(options: { baudRate: number; dataBits?: number; stopBits?: number }): Promise<void>;
  close(): Promise<void>;
  readable: ReadableStream<Uint8Array> | null;
  writable: WritableStream<Uint8Array> | null;
};
type SerialLike = { requestPort(): Promise<SerialPortLike> };

function serialApi(): SerialLike | undefined {
  return (navigator as Navigator & { serial?: SerialLike }).serial;
}

const BAUDS = [9600, 19200, 38400, 57600, 115200];

export default function Serial() {
  const { t } = useTranslation();
  const qc = useQueryClient();
  const [baud, setBaud] = useState(9600);
  const [connected, setConnected] = useState(false);
  const [capture, setCapture] = useState("");
  const [config, setConfig] = useState("");
  const [cmd, setCmd] = useState("");
  const [driver, setDriver] = useState("");
  const [note, setNote] = useState("");
  const [ip, setIp] = useState("");
  const [err, setErr] = useState("");
  const [candidate, setCandidate] = useState<Discovered | null>(null);
  const [adopting, setAdopting] = useState<Discovered | null>(null);
  const [toast, showToast] = useToast();

  const portRef = useRef<SerialPortLike | null>(null);
  const readerRef = useRef<ReadableStreamDefaultReader<Uint8Array> | null>(null);
  const captureRef = useRef<HTMLTextAreaElement | null>(null);

  const { data: drivers } = useQuery({
    queryKey: ["drivers"],
    queryFn: () => get<DriverRef[]>("/api/discovery/drivers"),
  });

  const available = !!serialApi();

  // The port is closed when leaving the screen: a serial port left open
  // prevents any other software from using it.
  useEffect(() => {
    return () => {
      void disconnect();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const append = (text: string) => {
    setCapture((prev) => {
      const next = (prev + text).slice(-200000);
      return next;
    });
    const el = captureRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  };

  const connect = async () => {
    setErr("");
    const api = serialApi();
    if (!api) return;
    try {
      const port = await api.requestPort();
      await port.open({ baudRate: baud });
      portRef.current = port;
      setConnected(true);
      readLoop(port);
    } catch (e) {
      setErr((e as Error).message || t("serialAdd.openPortFailed"));
    }
  };

  const readLoop = async (port: SerialPortLike) => {
    if (!port.readable) return;
    const reader = port.readable.getReader();
    readerRef.current = reader;
    const decoder = new TextDecoder();
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) break;
        if (value) append(decoder.decode(value, { stream: true }));
      }
    } catch (e) {
      setErr((e as Error).message || t("serialAdd.readInterrupted"));
    } finally {
      reader.releaseLock();
      readerRef.current = null;
    }
  };

  const disconnect = async () => {
    try {
      await readerRef.current?.cancel();
    } catch {
      /* the port may have been unplugged */
    }
    try {
      await portRef.current?.close();
    } catch {
      /* same */
    }
    portRef.current = null;
    setConnected(false);
  };

  const send = async (text: string) => {
    const port = portRef.current;
    if (!port?.writable) return;
    const writer = port.writable.getWriter();
    try {
      await writer.write(new TextEncoder().encode(text + "\r\n"));
    } finally {
      writer.releaseLock();
    }
  };

  const create = useMutation({
    mutationFn: () =>
      post<Discovered>("/api/discovery/serial", {
        banner: capture,
        raw_config: config,
        ip: ip.trim(),
        driver,
        note,
      }),
    onSuccess: (c) => {
      setCandidate(c);
      qc.invalidateQueries({ queryKey: ["discovered"] });
      showToast(t("serialAdd.candidateCreatedToast"));
    },
  });

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.serialAdd")}
          <span className="sub">{t("serialAdd.subtitle")}</span>
        </h1>
      </div>

      {!available ? (
        <div className="card">
          <div className="empty" style={{ textAlign: "left" }}>
            <b>{t("serialAdd.notSupported")}</b>
            <div className="note" style={{ marginTop: 8 }}>
              {t("serialAdd.notSupportedDetail1")}
              <br />
              {t("serialAdd.notSupportedDetail2")}
            </div>
          </div>
        </div>
      ) : (
        <div className="card">
          <div className="row" style={{ gap: 8, alignItems: "flex-end" }}>
            <div style={{ width: 160 }}>
              <Field label={t("serialAdd.baudRate") as string}>
                <select
                  value={baud}
                  disabled={connected}
                  onChange={(e) => setBaud(Number(e.target.value))}
                >
                  {BAUDS.map((b) => (
                    <option key={b} value={b}>
                      {b}
                    </option>
                  ))}
                </select>
              </Field>
            </div>
            {connected ? (
              <ActionButton kind="close" className="secondary" onClick={() => void disconnect()}>
                {t("serialAdd.closePort")}
              </ActionButton>
            ) : (
              <ActionButton kind="connect" onClick={() => void connect()}>
                {t("serialAdd.connectPort")}
              </ActionButton>
            )}
            <span className="note">
              {connected ? t("serialAdd.portOpen") : t("serialAdd.baudHint")}
            </span>
          </div>
          {err && (
            <div className="alert-row" style={{ cursor: "default" }}>
              <span className="dot" style={{ background: "var(--danger)" }} />
              <span style={{ fontSize: 12 }}>{err}</span>
            </div>
          )}
          {connected && (
            <div className="row" style={{ gap: 6, marginTop: 8 }}>
              <input
                style={{ flex: 1 }}
                value={cmd}
                onChange={(e) => setCmd(e.target.value)}
                placeholder={t("serialAdd.commandPlaceholder") as string}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    void send(cmd);
                    setCmd("");
                  }
                }}
              />
              <ActionButton kind="play" className="secondary" onClick={() => void send("")}>
                {t("serialAdd.enterKey")}
              </ActionButton>
              <ActionButton kind="play" className="secondary" onClick={() => void send("show version")}>
                show version
              </ActionButton>
              <ActionButton
                kind="play"
                className="secondary"
                onClick={() => void send("show running-config")}
              >
                show running-config
              </ActionButton>
            </div>
          )}
        </div>
      )}

      <div className="card" style={{ marginTop: 12 }}>
        <div className="kicker">{t("serialAdd.consoleCapture")}</div>
        <textarea
          ref={captureRef}
          className="raw-input"
          rows={12}
          value={capture}
          onChange={(e) => setCapture(e.target.value)}
          placeholder={t("serialAdd.capturePlaceholder") as string}
        />
        <div className="row" style={{ gap: 6 }}>
          <ActionButton kind="cancel" className="secondary" onClick={() => setCapture("")}>
            {t("common.clear")}
          </ActionButton>
          <ActionButton
            kind="import"
            className="secondary"
            disabled={!capture.trim()}
            onClick={() => setConfig(capture)}
          >
            {t("serialAdd.useCaptureAsConfig")}
          </ActionButton>
        </div>
      </div>

      <div className="card" style={{ marginTop: 12 }}>
        <div className="kicker">{t("serialAdd.configToImportOptional")}</div>
        <textarea
          className="raw-input"
          rows={8}
          value={config}
          onChange={(e) => setConfig(e.target.value)}
          placeholder={t("serialAdd.configPlaceholder") as string}
        />
        <div className="row">
          <div style={{ width: 220 }}>
            <Field label={t("manualIp.deviceType") as string}>
              <select value={driver} onChange={(e) => setDriver(e.target.value)}>
                <option value="">{t("manualIp.autoDetect")}</option>
                {(drivers ?? []).map((d) => (
                  <option key={d.driver} value={d.driver}>
                    {d.driver} {d.vendor ? `(${d.vendor})` : ""}
                  </option>
                ))}
              </select>
            </Field>
          </div>
          <div style={{ width: 200 }}>
            <Field label={t("serialAdd.mgmtIpIfKnown") as string}>
              <input value={ip} onChange={(e) => setIp(e.target.value)} />
            </Field>
          </div>
          <div style={{ flex: 1 }}>
            <Field label={t("serialAdd.remark") as string}>
              <input
                value={note}
                onChange={(e) => setNote(e.target.value)}
                placeholder={t("serialAdd.remarkPlaceholder") as string}
              />
            </Field>
          </div>
        </div>
        <ErrorMsg error={create.error} />
        <div className="actions">
          <ActionButton
            kind="create"
            disabled={(!capture.trim() && !config.trim()) || create.isPending}
            onClick={() => create.mutate()}
          >
            {create.isPending ? t("serialAdd.creating") : t("serialAdd.createCandidate")}
          </ActionButton>
        </div>
      </div>

      {candidate && (
        <div className="card" style={{ marginTop: 12 }}>
          <div className="kicker">{t("serialAdd.candidateCreated")}</div>
          <div className="note" style={{ marginTop: 6 }}>
            {candidate.hostname || t("serialAdd.unknownHostname")} ·{" "}
            {[candidate.vendor, candidate.model].filter(Boolean).join(" ") ||
              t("serialAdd.unidentifiedVendor")}{" "}
            · {t("resetProcedures.driverLabel", { driver: candidate.driver })} ({t("discover.confidence", { value: candidate.confidence })})
            {candidate.has_config ? ` · ${t("serialAdd.configReadyToImport")}` : ""}
          </div>
          <div className="actions">
            <ActionButton kind="adopt" onClick={() => setAdopting(candidate)}>
              {t("discover.adoptEllipsis")}
            </ActionButton>
          </div>
        </div>
      )}

      {adopting && (
        <AdoptModal
          candidate={adopting}
          onClose={() => setAdopting(null)}
          onDone={(deviceName) => {
            setAdopting(null);
            setCandidate(null);
            qc.invalidateQueries({ queryKey: ["discovered"] });
            qc.invalidateQueries({ queryKey: ["devices"] });
            showToast(t("discover.adoptedToast", { name: deviceName }));
          }}
        />
      )}
      {toast}
    </div>
  );
}

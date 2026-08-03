import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import {
  get,
  post,
  type Device,
  type Discovered,
  type DriverRef,
  type KnownCredential,
  type MgmtPlan,
  type MgmtPoolUsage,
  type ProxyInfo,
  type ResetProcedure,
  type ScanPrefix,
} from "../../api";
import { ActionButton, ErrorMsg, Field, useToast } from "../../components";
import { useAuth } from "../../auth";
import { sitePath } from "../../paths";
import { useSite, useSiteId } from "../../useSite";
import AdoptModal from "./AdoptModal";
import { resolveAutoProxy } from "./autoProxy";
import { runCiscoIosSoftReset } from "./ciscoIosSoftReset";
import { runFsS3900SoftReset } from "./fsS3900SoftReset";
import { probeUntilReachable } from "./probeUntil";
import { autodetectSerialParams } from "./serialAutodetect";
import {
  createPausableSession,
  createSerialSession,
  type SerialSession,
} from "./serialExpect";
import {
  formatSerialParams,
  serialApi,
  type SerialParams,
  type SerialPortLike,
} from "./serialTypes";
import { proposeSiteMgmt } from "./siteMgmtPropose";
import { createSerialLogReporter } from "./serialLogReport";

/**
 * Add via serial console — wizard:
 * 1) equipment kind (or auto)
 * 2) action: hardware reset / soft reset / adopt via serial
 *
 * Soft reset for FS.com S3900 / S3900-R runs over Web Serial until the
 * management IP answers a discovery probe.
 *
 * CLI: https://img-en.fs.com/file/user_manual/s3900-r-series-switches-cli-reference-guide.pdf
 */

type WizardStep = "equipment" | "action" | "run";
type SerialAction = "hardware" | "soft" | "adopt";

const BAUDS = [9600, 19200, 38400, 57600, 115200];

/** Prefer documented S3900 console settings before autodetection. */
const FS_S3900_SERIAL: SerialParams = {
  baudRate: 115200,
  dataBits: 8,
  stopBits: 1,
  parity: "none",
  flowControl: "none",
};

/**
 * A Catalyst console is 9600 8N1, NOT the 115200 the FS S3900 uses. Opening at
 * the wrong speed produces a screenful of garbage that reads like a dead
 * console, so the default follows the selected platform rather than the last
 * one used.
 */
const CISCO_IOS_SERIAL: SerialParams = {
  baudRate: 9600,
  dataBits: 8,
  stopBits: 1,
  parity: "none",
  flowControl: "none",
};

function equipmentDriver(equipment: string): string {
  if (
    equipment === "fs3900" ||
    equipment === "fs-s3900" ||
    equipment === "s3900"
  ) {
    return "fs-s3900";
  }
  if (
    equipment === "cisco2960x" ||
    equipment === "cisco-ios" ||
    equipment === "c2960x" ||
    equipment === "2960x"
  ) {
    return "cisco-ios";
  }
  return equipment;
}

/** Documented console settings for the platform, before autodetection runs. */
function serialDefaultsFor(equipment: string): SerialParams {
  var driver = equipmentDriver(equipment);
  if (driver === "cisco-ios") return CISCO_IOS_SERIAL;
  return FS_S3900_SERIAL;
}

/** Platforms whose soft reset is scripted end to end. */
const SOFT_RESET_DRIVERS = ["fs-s3900", "cisco-ios"];

export default function Serial() {
  const { t } = useTranslation();
  const { site } = useSite();
  const siteId = useSiteId();
  const siteSlug = site?.slug ?? "";
  const { user } = useAuth();
  const qc = useQueryClient();
  const [toast, showToast] = useToast();

  const [step, setStep] = useState<WizardStep>("equipment");
  const [equipment, setEquipment] = useState("fs3900");
  const [action, setAction] = useState<SerialAction>("soft");

  const [params, setParams] = useState<SerialParams>(FS_S3900_SERIAL);
  const [paramsLocked, setParamsLocked] = useState(false);
  const [manualOverride, setManualOverride] = useState(false);
  const [connected, setConnected] = useState(false);
  const [detecting, setDetecting] = useState(false);
  const [running, setRunning] = useState(false);
  const [log, setLog] = useState<string[]>([]);
  const [capture, setCapture] = useState("");
  const [config, setConfig] = useState("");
  const [cmd, setCmd] = useState("");
  const [note, setNote] = useState("");
  const [ipKnown, setIpKnown] = useState("");
  const [err, setErr] = useState("");

  // Soft-reset: factory serial defaults to reach the device; post-reset account
  // comes from an instance discovery credential (materialized for Web Serial).
  const [currentUser, setCurrentUser] = useState("admin");
  const [currentPassword, setCurrentPassword] = useState("admin");
  const [enablePassword, setEnablePassword] = useState("super");
  const [credChoice, setCredChoice] = useState("");
  const [mgmtIp, setMgmtIp] = useState("");
  const [mgmtMask, setMgmtMask] = useState("");
  const [mgmtGateway, setMgmtGateway] = useState("");
  const [mgmtIface, setMgmtIface] = useState("vlan 1");
  const [mgmtManual, setMgmtManual] = useState(false);

  const [candidate, setCandidate] = useState<Discovered | null>(null);
  const [adopting, setAdopting] = useState<Discovered | null>(null);

  // Pause / interactive TTY. `pausedRef` feeds the gate closures (state alone
  // would be stale inside them); waiters are the script steps frozen on pause.
  const [paused, setPaused] = useState(false);
  const [ttyOpen, setTtyOpen] = useState(false);
  const pausedRef = useRef(false);
  const resumeWaitersRef = useRef<(() => void)[]>([]);

  const portRef = useRef<SerialPortLike | null>(null);
  const readerRef = useRef<ReadableStreamDefaultReader<Uint8Array> | null>(null);
  const sessionRef = useRef<SerialSession | null>(null);
  const captureRef = useRef<HTMLTextAreaElement | null>(null);
  const logRef = useRef<HTMLPreElement | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const serialLogRef = useRef(createSerialLogReporter());
  const [logPhase, setLogPhase] = useState("");

  useEffect(() => {
    serialLogRef.current.setMeta({
      siteId: siteId || "",
      equipment,
      action,
      phase: logPhase,
    });
  }, [siteId, equipment, action, logPhase]);

  const { data: drivers } = useQuery({
    queryKey: ["drivers"],
    queryFn: () => get<DriverRef[]>("/api/discovery/drivers"),
  });

  var driverKey = equipmentDriver(equipment);

  // Follow the selected platform's documented console speed while the port is
  // still closed. Once params are locked (autodetected or opened by hand) the
  // observed value wins — it is the one that actually produced readable text.
  useEffect(() => {
    if (paramsLocked || connected) return;
    setParams(serialDefaultsFor(equipment));
  }, [equipment, paramsLocked, connected]);
  const { data: procedures } = useQuery({
    queryKey: ["reset-procedures", driverKey],
    queryFn: () =>
      get<ResetProcedure[]>(
        `/api/discovery/reset-procedures?q=${encodeURIComponent(driverKey)}`,
      ),
    enabled: step === "run" && action === "hardware",
  });
  var procedure =
    (procedures ?? []).find((p) => p.driver === driverKey) ??
    (procedures ?? [])[0] ??
    null;

  const { data: mgmtData } = useQuery({
    queryKey: ["mgmt-plan", siteId],
    queryFn: () =>
      get<{ plan: MgmtPlan; pool: MgmtPoolUsage }>(`/api/sites/${siteId}/mgmt-plan`),
    enabled: !!siteId && action === "soft",
  });
  const { data: siteDevices } = useQuery({
    queryKey: ["devices", siteId],
    queryFn: () => get<Device[]>(`/api/sites/${siteId}/devices`),
    enabled: !!siteId && action === "soft",
  });

  // The reachability probe originates from a PROXY, and the choice decides the
  // outcome: the mgmt VLAN configured over serial is usually NOT reachable from
  // the controller container (2026-08-01: everything pushed and saved, then
  // « no administration port open » — the probe was leaving from « Local »).
  //
  // Default is AUTO: the proxy whose DECLARED range contains the management
  // address (longest prefix wins — a /24 subnet is covered by a declared /16).
  const AUTO_PROXY = "auto";
  const [probeProxyId, setProbeProxyId] = useState(AUTO_PROXY);
  const { data: discoveryProxies } = useQuery({
    queryKey: ["discovery-proxies", siteId],
    queryFn: () => get<ProxyInfo[]>(`/api/sites/${siteId}/discovery-proxies`),
    enabled: !!siteId && action === "soft",
  });
  const { data: scanPrefixes } = useQuery({
    queryKey: ["scan-prefixes"],
    queryFn: () => get<ScanPrefix[]>("/api/discovery/prefixes"),
    enabled: action === "soft",
  });
  var autoProxyChoice = useMemo(() => {
    if (!mgmtIp.trim()) return null;
    var allowed = new Set((discoveryProxies ?? []).map((p) => p.id));
    return resolveAutoProxy(mgmtIp.trim(), scanPrefixes ?? [], allowed);
  }, [mgmtIp, scanPrefixes, discoveryProxies]);
  // What the probe actually uses: the auto resolution, or — when nothing
  // declared contains the address — the first non-local proxy, then Local.
  var effectiveProxyId =
    probeProxyId === AUTO_PROXY
      ? autoProxyChoice?.proxyId ??
        (discoveryProxies ?? []).find((p) => !p.internal)?.id ??
        ""
      : probeProxyId;
  var effectiveProxyName =
    (discoveryProxies ?? []).find((p) => p.id === effectiveProxyId)?.name ||
    (t("common.localInstance") as string);

  var canListCreds = !!user?.is_global_admin;
  const { data: creds } = useQuery({
    queryKey: ["known-credentials"],
    enabled: canListCreds && action === "soft",
    queryFn: () =>
      get<{ credentials: KnownCredential[]; unreadable_secrets: string[] }>(
        "/api/discovery/credentials",
      ),
  });
  var usableCreds = (creds?.credentials ?? []).filter(
    (c) => c.enabled && c.has_secret && !(creds?.unreadable_secrets ?? []).includes(c.id),
  );

  var siteProposal = useMemo(() => {
    var used = (siteDevices ?? []).map((d) => d.mgmt_ip).filter(Boolean);
    return proposeSiteMgmt(mgmtData?.plan, used);
  }, [mgmtData?.plan, siteDevices]);

  useEffect(() => {
    if (mgmtManual || !siteProposal) return;
    setMgmtIp(siteProposal.ip);
    setMgmtMask(siteProposal.mask);
    setMgmtGateway(siteProposal.gateway);
    setMgmtIface(siteProposal.iface);
  }, [siteProposal, mgmtManual]);

  const available = !!serialApi();

  var appendLog = (msg: string, phase?: string) => {
    if (phase) setLogPhase(phase);
    setLog((prev) => [...prev, msg].slice(-500));
    serialLogRef.current.line(msg, phase || logPhase || undefined);
    requestAnimationFrame(() => {
      var el = logRef.current;
      if (el) el.scrollTop = el.scrollHeight;
    });
  };

  useEffect(() => {
    return () => {
      abortRef.current?.abort();
      void disconnect();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  var stopReader = async () => {
    try {
      await readerRef.current?.cancel();
    } catch {
      /* unplugged */
    }
    readerRef.current = null;
  };

  var startReadLoop = (port: SerialPortLike, session: SerialSession) => {
    var decoder = new TextDecoder();
    var generation = (startReadLoop as unknown as { _gen?: number })._gen ?? 0;
    generation++;
    (startReadLoop as unknown as { _gen?: number })._gen = generation;
    var myGen = generation;
    void (async () => {
      // USB-serial often drops readable during switch reboot; keep re-attaching
      // until disconnect() bumps the generation or the port is cleared.
      while (portRef.current === port && myGen === (startReadLoop as unknown as { _gen?: number })._gen) {
        if (!port.readable) {
          await new Promise((r) => setTimeout(r, 400));
          continue;
        }
        var reader = port.readable.getReader();
        readerRef.current = reader;
        try {
          for (;;) {
            var { value, done } = await reader.read();
            if (done) break;
            if (value) session.append(decoder.decode(value, { stream: true }));
          }
        } catch {
          // Device reboot / adapter blip — retry below.
        } finally {
          try {
            reader.releaseLock();
          } catch {
            /* ignore */
          }
          if (readerRef.current === reader) readerRef.current = null;
        }
        await new Promise((r) => setTimeout(r, 300));
      }
    })();
  };

  var ensureSession = (port: SerialPortLike): SerialSession => {
    if (sessionRef.current) return sessionRef.current;
    var session = createSerialSession({
      write: async (bytes) => {
        if (!port.writable) throw new Error("serial port not writable");
        var writer = port.writable.getWriter();
        try {
          await writer.write(bytes);
        } finally {
          writer.releaseLock();
        }
      },
      onCapture: (full) => {
        setCapture(full);
        var el = captureRef.current;
        if (el) el.scrollTop = el.scrollHeight;
      },
    });
    sessionRef.current = session;
    return session;
  };

  var disconnect = async () => {
    await stopReader();
    try {
      await portRef.current?.close();
    } catch {
      /* same */
    }
    portRef.current = null;
    sessionRef.current = null;
    setConnected(false);
    setParamsLocked(false);
    setTtyOpen(false);
  };

  var openManual = async () => {
    setErr("");
    var api = serialApi();
    if (!api) return;
    try {
      await disconnect();
      var port = await api.requestPort();
      await port.open({
        baudRate: params.baudRate,
        dataBits: params.dataBits,
        stopBits: params.stopBits,
        parity: params.parity,
        flowControl: params.flowControl,
      });
      portRef.current = port;
      var session = ensureSession(port);
      startReadLoop(port, session);
      setConnected(true);
      setParamsLocked(true);
      appendLog(`opened ${formatSerialParams(params)} (manual)`);
    } catch (e) {
      setErr((e as Error).message || t("serialAdd.openPortFailed"));
    }
  };

  var connectAndDetect = async () => {
    setErr("");
    setDetecting(true);
    setLog([]);
    setLogPhase("autodetect");
    serialLogRef.current.newSession();
    serialLogRef.current.setMeta({
      siteId: siteId || "",
      equipment,
      action,
      phase: "autodetect",
    });
    var api = serialApi();
    if (!api) return;
    abortRef.current?.abort();
    var ac = new AbortController();
    abortRef.current = ac;
    try {
      await disconnect();
      var port = await api.requestPort();
      portRef.current = port;
      appendLog(t("serialAdd.autodetectStart"), "autodetect");
      var result = await autodetectSerialParams({
        port,
        equipmentHint: equipment,
        log: (m) => appendLog(m, "autodetect"),
        signal: ac.signal,
      });
      setParams(result.params);
      setParamsLocked(true);
      sessionRef.current = null;
      var session = ensureSession(port);
      startReadLoop(port, session);
      setConnected(true);
      appendLog(
        t("serialAdd.autodetectLocked", {
          params: formatSerialParams(result.params),
        }),
        "autodetect",
      );
      await serialLogRef.current.flush();
    } catch (e) {
      setErr((e as Error).message || t("serialAdd.autodetectFailed"));
      appendLog(`error: ${(e as Error).message || e}`, "autodetect");
      await serialLogRef.current.flush();
      await disconnect();
    } finally {
      setDetecting(false);
    }
  };

  var send = async (text: string) => {
    var session = sessionRef.current;
    if (!session) return;
    await session.send(text);
  };

  /** Gate awaited by the provisioning script before each step (and by the
   * probe loop between attempts). Resolved instantly when not paused. */
  var pauseGate = () => {
    if (!pausedRef.current) return Promise.resolve();
    return new Promise<void>((resolve) => {
      resumeWaitersRef.current.push(resolve);
    });
  };

  var releasePausedSteps = () => {
    var waiters = resumeWaitersRef.current;
    resumeWaitersRef.current = [];
    var wi = 0;
    for (wi = 0; wi < waiters.length; wi++) waiters[wi]();
  };

  var setPausedState = (p: boolean) => {
    if (pausedRef.current === p) return;
    pausedRef.current = p;
    setPaused(p);
    if (p) {
      appendLog(t("serialAdd.provisioningPaused"));
    } else {
      appendLog(t("serialAdd.provisioningResumed"));
      releasePausedSteps();
    }
  };

  /** Interactive TTY: keystrokes go to the serial console as-is. */
  var ttyKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (!ttyOpen) return;
    var session = sessionRef.current;
    if (!session) return;
    var seq = "";
    if (e.key === "Enter") seq = "\r";
    // \x08 (BS / Ctrl-H), NOT \x7f (DEL): network-device serial consoles — Cisco
    // IOS, HP ProCurve, FS — edit their line with Ctrl-H and echo « ^? » for DEL
    // instead of erasing, which is the "backspace does nothing" symptom on a raw
    // TTY. Web terminals default to DEL; this line is the correction.
    else if (e.key === "Backspace") seq = "\x08";
    else if (e.key === "Tab") seq = "\t";
    else if (e.key === "Escape") seq = "\x1b";
    else if (e.key === "ArrowUp") seq = "\x1b[A";
    else if (e.key === "ArrowDown") seq = "\x1b[B";
    else if (e.key === "ArrowRight") seq = "\x1b[C";
    else if (e.key === "ArrowLeft") seq = "\x1b[D";
    else if (e.ctrlKey && !e.metaKey && !e.altKey && e.key.length === 1 && /[a-z]/i.test(e.key)) {
      // Ctrl+C → ETX, Ctrl+Z → SUB, Ctrl+P (S3900 monitor mode)…
      seq = String.fromCharCode(e.key.toUpperCase().charCodeAt(0) - 64);
    } else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
      seq = e.key;
    } else {
      return;
    }
    e.preventDefault();
    void session.sendRaw(seq);
  };

  var ttyPaste = (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    if (!ttyOpen) return;
    var session = sessionRef.current;
    if (!session) return;
    e.preventDefault();
    var text = e.clipboardData.getData("text").replace(/\r?\n/g, "\r");
    if (text) void session.sendRaw(text);
  };

  var create = useMutation({
    mutationFn: () =>
      post<Discovered>("/api/discovery/serial", {
        banner: capture,
        raw_config: config,
        ip: (action === "adopt" || !available ? ipKnown : mgmtIp).trim(),
        driver: driverKey === "auto" ? "" : driverKey,
        note,
      }),
    onSuccess: (c) => {
      setCandidate(c);
      qc.invalidateQueries({ queryKey: ["discovered"] });
      showToast(t("serialAdd.candidateCreatedToast"));
    },
  });

  var runSoftReset = async () => {
    setErr("");
    if (!connected || !sessionRef.current || !paramsLocked) {
      setErr(t("serialAdd.connectFirst"));
      return;
    }
    if (!mgmtIp.trim() || !mgmtMask.trim()) {
      setErr(t("serialAdd.siteMgmtIncomplete"));
      return;
    }
    setRunning(true);
    setLogPhase("soft-reset");
    abortRef.current?.abort();
    var ac = new AbortController();
    abortRef.current = ac;
    // A leftover pause must not freeze a fresh run; and Cancel must unblock
    // steps frozen on the gate so the abort signal can reach them.
    pausedRef.current = false;
    setPaused(false);
    ac.signal.addEventListener("abort", releasePausedSteps);
    var script = createPausableSession(sessionRef.current, pauseGate);
    var material:
      | { id: string; name: string; username: string; secret: string }
      | undefined;
    try {
      appendLog(t("serialAdd.materializingCred"), "soft-reset");
      material = await post<{
        id: string;
        name: string;
        username: string;
        secret: string;
      }>("/api/discovery/serial-credential", {
        credential_id: credChoice,
      });
      if (!material.username || material.secret === undefined) {
        throw new Error(t("serialAdd.noInstanceCred"));
      }
      appendLog(
        t("serialAdd.usingCred", {
          name: material.name,
          user: material.username,
        }),
        "soft-reset",
      );
      appendLog(
        t("serialAdd.siteMgmtPushLog", {
          ip: mgmtIp,
          mask: mgmtMask,
          iface: mgmtIface,
          gateway: mgmtGateway || "—",
        }),
        "soft-reset",
      );
      // One script per platform, chosen by connector. The two CLIs disagree on
      // almost every prompt — a single "console script" with conditionals would
      // be unreadable exactly when a run fails and someone has to say why.
      if (driverKey === "cisco-ios") {
        await runCiscoIosSoftReset({
          session: script,
          log: (m) => appendLog(m, "soft-reset"),
          currentUser,
          currentPassword,
          enablePassword,
          targetUser: material.username,
          targetPassword: material.secret,
          mgmtIp,
          mgmtMask,
          gateway: mgmtGateway,
          mgmtIface,
          signal: ac.signal,
        });
      } else {
        await runFsS3900SoftReset({
          session: script,
          log: (m) => appendLog(m, "soft-reset"),
          currentUser,
          currentPassword,
          enablePassword,
          targetUser: material.username,
          targetPassword: material.secret,
          mgmtIp,
          mgmtMask,
          gateway: mgmtGateway,
          mgmtIface,
          signal: ac.signal,
        });
      }
      // Soft-reset replaces factory admin/admin with the instance credential.
      setCurrentUser(material.username);
      setCurrentPassword(material.secret);
      setEnablePassword(material.secret);
      appendLog(
        t("serialAdd.passwordNowInstance", {
          user: material.username,
          name: material.name,
        }),
        "soft-reset",
      );
      appendLog(t("serialAdd.probingNetwork"), "probe");
      appendLog(
        t("serialAdd.probingVia", {
          proxy:
            probeProxyId === AUTO_PROXY
              ? `${effectiveProxyName} (auto)`
              : effectiveProxyName,
        }),
        "probe",
      );
      var probed = await probeUntilReachable({
        ip: mgmtIp,
        credentialId: material.id,
        driver: driverKey,
        proxyId: effectiveProxyId,
        log: (m) => appendLog(m, "probe"),
        signal: ac.signal,
        gate: pauseGate,
      });
      setCandidate(probed.candidate);
      qc.invalidateQueries({ queryKey: ["discovered"] });
      showToast(
        t("manualIp.reachableToast", {
          ip: probed.probe.ip,
          driver: probed.probe.driver,
        }),
      );
      appendLog(t("serialAdd.softResetDone"), "probe");
      await serialLogRef.current.flush();
      setAdopting(probed.candidate);
    } catch (e) {
      setErr((e as Error).message || t("serialAdd.softResetFailed"));
      appendLog(`error: ${(e as Error).message || e}`, "soft-reset");
      // Ship the raw console tail (secrets masked) with the failure: the
      // 2026-08-01 runs could not be diagnosed from wizard messages alone.
      var cap = sessionRef.current?.getCapture() ?? "";
      if (cap) {
        var secrets = [material?.secret, currentPassword, enablePassword];
        var tail = cap.slice(-1800);
        var si = 0;
        for (si = 0; si < secrets.length; si++) {
          var s = secrets[si];
          if (s && s.length >= 4) tail = tail.split(s).join("•••");
        }
        appendLog(`serial capture (tail, redacted):\n${tail}`, "soft-reset");
      }
      await serialLogRef.current.flush();
    } finally {
      ac.signal.removeEventListener("abort", releasePausedSteps);
      pausedRef.current = false;
      setPaused(false);
      releasePausedSteps();
      setRunning(false);
    }
  };

  var softSupported = SOFT_RESET_DRIVERS.includes(driverKey);

  return (
    <div>
      <div className="toolbar">
        <h1>
          {t("nav.serialAdd")}
          <span className="sub">{t("serialAdd.subtitle")}</span>
        </h1>
      </div>

      {!available && (
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
      )}

      {available && step === "equipment" && (
        <div className="card">
          <div className="kicker">{t("serialAdd.stepEquipment")}</div>
          <p className="note">{t("serialAdd.stepEquipmentHint")}</p>
          <div className="row" style={{ gap: 8, flexWrap: "wrap", marginTop: 12, alignItems: "stretch" }}>
            <Choice
              selected={equipment === "auto"}
              onClick={() => setEquipment("auto")}
              title={t("serialAdd.equipmentAuto")}
              detail={t("serialAdd.equipmentAutoDetail")}
            />
            <Choice
              selected={equipment === "fs3900"}
              onClick={() => setEquipment("fs3900")}
              title={t("serialAdd.equipmentFs3900")}
              detail={t("serialAdd.equipmentFs3900Detail")}
            />
            <Choice
              selected={equipment === "cisco2960x"}
              onClick={() => setEquipment("cisco2960x")}
              title={t("serialAdd.equipmentCisco2960x")}
              detail={t("serialAdd.equipmentCisco2960xDetail")}
            />
            {(drivers ?? []).map((d) => (
                <Choice
                  key={d.driver}
                  selected={equipment === d.driver}
                  onClick={() => setEquipment(d.driver)}
                  title={d.driver}
                  detail={d.vendor || ""}
                />
              ))}
          </div>
          <div className="actions">
            <ActionButton kind="confirm" onClick={() => setStep("action")}>
              {t("serialAdd.continue")}
            </ActionButton>
          </div>
        </div>
      )}

      {available && step === "action" && (
        <div className="card">
          <div className="kicker">{t("serialAdd.stepAction")}</div>
          <p className="note">{t("serialAdd.stepActionHint")}</p>
          <div className="row" style={{ gap: 8, flexWrap: "wrap", marginTop: 12, alignItems: "stretch" }}>
            <Choice
              selected={action === "hardware"}
              onClick={() => setAction("hardware")}
              title={t("serialAdd.actionHardware")}
              detail={t("serialAdd.actionHardwareDetail")}
            />
            <Choice
              selected={action === "soft"}
              onClick={() => setAction("soft")}
              title={t("serialAdd.actionSoft")}
              detail={t("serialAdd.actionSoftDetail")}
            />
            <Choice
              selected={action === "adopt"}
              onClick={() => setAction("adopt")}
              title={t("serialAdd.actionAdopt")}
              detail={t("serialAdd.actionAdoptDetail")}
            />
          </div>
          {action === "soft" && !softSupported && (
            <div className="alert-row" style={{ cursor: "default", marginTop: 12 }}>
              <span className="dot" style={{ background: "var(--warn)" }} />
              <span style={{ fontSize: 12 }}>{t("serialAdd.softNotAutomated")}</span>
            </div>
          )}
          <div className="actions">
            <ActionButton kind="cancel" className="secondary" onClick={() => setStep("equipment")}>
              {t("common.goBack")}
            </ActionButton>
            <ActionButton
              kind="confirm"
              disabled={action === "soft" && !softSupported}
              onClick={() => setStep("run")}
            >
              {t("serialAdd.continue")}
            </ActionButton>
          </div>
        </div>
      )}

      {available && step === "run" && (
        <>
          <div className="card">
            <div className="row" style={{ gap: 8, alignItems: "center", flexWrap: "wrap" }}>
              <span className="chip">
                {equipment === "fs3900"
                  ? t("serialAdd.equipmentFs3900")
                  : equipment === "cisco2960x"
                    ? t("serialAdd.equipmentCisco2960x")
                    : equipment === "auto"
                      ? t("serialAdd.equipmentAuto")
                      : equipment}
              </span>
              <span className="chip">
                {action === "hardware"
                  ? t("serialAdd.actionHardware")
                  : action === "soft"
                    ? t("serialAdd.actionSoft")
                    : t("serialAdd.actionAdopt")}
              </span>
              {paramsLocked && (
                <span className="badge">{formatSerialParams(params)}</span>
              )}
              <span style={{ marginLeft: "auto" }} />
              <ActionButton
                kind="cancel"
                className="ghost"
                disabled={running || detecting}
                onClick={() => {
                  void disconnect();
                  setStep("action");
                }}
              >
                {t("common.goBack")}
              </ActionButton>
            </div>

            <div className="row" style={{ gap: 8, alignItems: "flex-end", marginTop: 12 }}>
              {connected ? (
                <ActionButton
                  kind="close"
                  className="secondary"
                  disabled={running || detecting}
                  onClick={() => void disconnect()}
                >
                  {t("serialAdd.closePort")}
                </ActionButton>
              ) : (
                <ActionButton
                  kind="connect"
                  disabled={detecting}
                  onClick={() =>
                    void (manualOverride ? openManual() : connectAndDetect())
                  }
                >
                  {detecting
                    ? t("serialAdd.autodetecting")
                    : manualOverride
                      ? t("serialAdd.connectPort")
                      : t("serialAdd.connectAndDetect")}
                </ActionButton>
              )}
              {connected && (
                <ActionButton
                  kind="refresh"
                  className="secondary"
                  disabled={running || detecting}
                  onClick={() => void connectAndDetect()}
                >
                  {t("serialAdd.redetect")}
                </ActionButton>
              )}
              <label className="note" style={{ display: "flex", gap: 6, alignItems: "center" }}>
                <input
                  type="checkbox"
                  checked={manualOverride}
                  disabled={connected || detecting}
                  onChange={(e) => setManualOverride(e.target.checked)}
                />
                {t("serialAdd.manualOverride")}
              </label>
            </div>

            {manualOverride && !connected && (
              <div className="row" style={{ gap: 8, marginTop: 8, flexWrap: "wrap" }}>
                <div style={{ width: 140 }}>
                  <Field label={t("serialAdd.baudRate") as string}>
                    <select
                      value={params.baudRate}
                      onChange={(e) =>
                        setParams({ ...params, baudRate: Number(e.target.value) })
                      }
                    >
                      {BAUDS.map((b) => (
                        <option key={b} value={b}>
                          {b}
                        </option>
                      ))}
                    </select>
                  </Field>
                </div>
                <div style={{ width: 120 }}>
                  <Field label={t("serialAdd.dataBits") as string}>
                    <select
                      value={params.dataBits}
                      onChange={(e) =>
                        setParams({
                          ...params,
                          dataBits: Number(e.target.value) as 7 | 8,
                        })
                      }
                    >
                      <option value={8}>8</option>
                      <option value={7}>7</option>
                    </select>
                  </Field>
                </div>
                <div style={{ width: 120 }}>
                  <Field label={t("serialAdd.parity") as string}>
                    <select
                      value={params.parity}
                      onChange={(e) =>
                        setParams({
                          ...params,
                          parity: e.target.value as SerialParams["parity"],
                        })
                      }
                    >
                      <option value="none">none</option>
                      <option value="even">even</option>
                      <option value="odd">odd</option>
                    </select>
                  </Field>
                </div>
                <div style={{ width: 120 }}>
                  <Field label={t("serialAdd.stopBits") as string}>
                    <select
                      value={params.stopBits}
                      onChange={(e) =>
                        setParams({
                          ...params,
                          stopBits: Number(e.target.value) as 1 | 2,
                        })
                      }
                    >
                      <option value={1}>1</option>
                      <option value={2}>2</option>
                    </select>
                  </Field>
                </div>
              </div>
            )}

            {err && (
              <div className="alert-row" style={{ cursor: "default", marginTop: 8 }}>
                <span className="dot" style={{ background: "var(--danger)" }} />
                <span style={{ fontSize: 12 }}>{err}</span>
              </div>
            )}

            {connected && (
              <div className="row" style={{ gap: 6, marginTop: 8 }}>
                <input
                  style={{ flex: 1 }}
                  value={cmd}
                  disabled={running}
                  onChange={(e) => setCmd(e.target.value)}
                  placeholder={t("serialAdd.commandPlaceholder") as string}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") {
                      void send(cmd);
                      setCmd("");
                    }
                  }}
                />
                <ActionButton
                  kind="play"
                  className="secondary"
                  disabled={running}
                  onClick={() => void send("")}
                >
                  {t("serialAdd.enterKey")}
                </ActionButton>
              </div>
            )}
          </div>

          {(log.length > 0 || action === "soft") && (
            <div className="card" style={{ marginTop: 12 }}>
              <div className="kicker">{t("serialAdd.runLog")}</div>
              <pre
                ref={logRef}
                className="raw-output"
                style={{ maxHeight: 200, overflow: "auto", margin: 0 }}
              >
                {log.join("\n") || t("serialAdd.runLogEmpty")}
              </pre>
            </div>
          )}

          {action === "hardware" && (
            <div className="card" style={{ marginTop: 12 }}>
              <div className="kicker">{t("serialAdd.hardwareGuide")}</div>
              {!procedure ? (
                <div className="note">{t("serialAdd.noProcedure")}</div>
              ) : (
                <>
                  <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                    <span style={{ fontWeight: 700 }}>{procedure.family}</span>
                    <span className="chip">{procedure.vendor}</span>
                  </div>
                  {procedure.hardware.length > 0 && (
                    <>
                      <div className="kicker" style={{ marginTop: 12 }}>
                        {t("resetProcedures.viaHardware")}
                      </div>
                      <ol className="reset-steps">
                        {procedure.hardware.map((s, i) => (
                          <li key={i}>{s}</li>
                        ))}
                      </ol>
                    </>
                  )}
                  {procedure.software.length > 0 && (
                    <>
                      <div className="kicker" style={{ marginTop: 12 }}>
                        {t("resetProcedures.viaCli")}
                      </div>
                      <ol className="reset-steps">
                        {procedure.software.map((s, i) => (
                          <li key={i}>
                            <code>{s}</code>
                          </li>
                        ))}
                      </ol>
                    </>
                  )}
                  <div className="note" style={{ marginTop: 8 }}>
                    <b>{t("resetProcedures.erasesLabel")}</b> {procedure.erases}
                  </div>
                </>
              )}
            </div>
          )}

          {action === "soft" && (
            <div className="card" style={{ marginTop: 12 }}>
              <div className="kicker">{t("serialAdd.softResetForm")}</div>
              <p className="note">{t("serialAdd.softResetFormHint")}</p>
              <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
                <div style={{ width: 160 }}>
                  <Field label={t("serialAdd.currentUser") as string}>
                    <input
                      value={currentUser}
                      disabled={running}
                      onChange={(e) => setCurrentUser(e.target.value)}
                    />
                  </Field>
                </div>
                <div style={{ width: 180 }}>
                  <Field label={t("serialAdd.currentPassword") as string}>
                    <input
                      type="password"
                      value={currentPassword}
                      disabled={running}
                      onChange={(e) => setCurrentPassword(e.target.value)}
                      autoComplete="off"
                    />
                  </Field>
                </div>
                <div style={{ width: 180 }}>
                  <Field label={t("serialAdd.enablePassword") as string}>
                    <input
                      type="password"
                      value={enablePassword}
                      disabled={running}
                      onChange={(e) => setEnablePassword(e.target.value)}
                      autoComplete="off"
                    />
                  </Field>
                </div>
              </div>
              <div className="row" style={{ gap: 8, flexWrap: "wrap", marginTop: 8 }}>
                <div style={{ flex: 1, minWidth: 220 }}>
                  <Field
                    label={t("serialAdd.instanceCred") as string}
                    help={<p>{t("serialAdd.instanceCredHelp")}</p>}
                  >
                    <select
                      value={credChoice}
                      disabled={running}
                      onChange={(e) => setCredChoice(e.target.value)}
                    >
                      <option value="">{t("serialAdd.instanceCredDefault")}</option>
                      {usableCreds.map((c) => (
                        <option key={c.id} value={c.id}>
                          {c.name} — {c.username}
                        </option>
                      ))}
                    </select>
                  </Field>
                </div>
                <div style={{ width: 240 }}>
                  <Field
                    label={t("serialAdd.probeProxy") as string}
                    help={<p>{t("serialAdd.probeProxyHelp")}</p>}
                  >
                    <select
                      value={probeProxyId}
                      disabled={running}
                      onChange={(e) => setProbeProxyId(e.target.value)}
                    >
                      <option value={AUTO_PROXY}>{t("serialAdd.probeProxyAuto")}</option>
                      <option value="">{t("common.localInstance")}</option>
                      {(discoveryProxies ?? [])
                        .filter((p) => !p.internal)
                        .map((p) => (
                          <option key={p.id} value={p.id}>
                            {p.name || p.id.slice(0, 8)}
                          </option>
                        ))}
                    </select>
                  </Field>
                </div>
              </div>
              {probeProxyId === AUTO_PROXY && autoProxyChoice && (
                <p className="note" style={{ marginTop: 6 }}>
                  {t("serialAdd.probeProxyAutoResolved", {
                    proxy: autoProxyChoice.proxyName,
                    prefix: autoProxyChoice.prefix,
                  })}
                </p>
              )}
              {probeProxyId === AUTO_PROXY && !autoProxyChoice && mgmtIp.trim() !== "" && (
                <p className="note" style={{ marginTop: 6 }}>
                  {t("serialAdd.probeProxyAutoNone", {
                    ip: mgmtIp.trim(),
                    fallback: effectiveProxyName,
                  })}
                </p>
              )}
              {probeProxyId === "" && (discoveryProxies ?? []).some((p) => !p.internal) && (
                <p className="note" style={{ marginTop: 6 }}>
                  {t("serialAdd.probeProxyLocalWarning")}
                </p>
              )}
              {!canListCreds && (
                <p className="note" style={{ marginTop: 6 }}>
                  {t("serialAdd.instanceCredOperatorNote")}
                </p>
              )}

              <div className="kicker" style={{ marginTop: 16 }}>
                {t("serialAdd.siteMgmtTitle")}
              </div>
              <p className="note">{t("serialAdd.siteMgmtHint")}</p>
              {!siteId ? (
                <div className="alert-row" style={{ cursor: "default" }}>
                  <span className="dot" style={{ background: "var(--warn)" }} />
                  <span style={{ fontSize: 12 }}>{t("serialAdd.siteMgmtNoSite")}</span>
                </div>
              ) : !siteProposal?.configured ? (
                <div className="alert-row" style={{ cursor: "default" }}>
                  <span className="dot" style={{ background: "var(--warn)" }} />
                  <span style={{ fontSize: 12 }}>
                    {t("serialAdd.siteMgmtNotConfigured")}{" "}
                    {siteSlug && (
                      <Link to={sitePath(siteSlug, "config", "management")}>
                        {t("serialAdd.siteMgmtOpenPlan")}
                      </Link>
                    )}
                  </span>
                </div>
              ) : !siteProposal.ip || !siteProposal.mask ? (
                <div className="alert-row" style={{ cursor: "default" }}>
                  <span className="dot" style={{ background: "var(--danger)" }} />
                  <span style={{ fontSize: 12 }}>{t("serialAdd.siteMgmtIncomplete")}</span>
                </div>
              ) : (
                <div className="note" style={{ marginTop: 6, lineHeight: 1.5 }}>
                  <div>
                    <b>{t("serialAdd.siteMgmtVlan")}</b>{" "}
                    {siteProposal.untagged
                      ? t("serialAdd.siteMgmtUntagged", { vlan: siteProposal.vlanId })
                      : siteProposal.vlanId}
                    {" · "}
                    <b>{t("serialAdd.siteMgmtSubnet")}</b> {siteProposal.subnet}
                    {siteProposal.gateway
                      ? ` · ${t("serialAdd.siteMgmtGateway")}: ${siteProposal.gateway}`
                      : ""}
                  </div>
                  <div>
                    <b>{t("serialAdd.siteMgmtProposedIp")}</b> {mgmtIp}/{siteProposal.prefixLen}{" "}
                    <span className="chip">{mgmtIface}</span>
                    {siteProposal.source === "meshcraft" ? (
                      <span className="badge" style={{ marginLeft: 6 }}>
                        {t("serialAdd.siteMgmtFromPool")}
                      </span>
                    ) : (
                      <span className="badge warn" style={{ marginLeft: 6 }}>
                        {t("serialAdd.siteMgmtFromSubnet")}
                      </span>
                    )}
                  </div>
                  {siteSlug && (
                    <div style={{ marginTop: 4 }}>
                      <Link to={sitePath(siteSlug, "config", "management")}>
                        {t("serialAdd.siteMgmtOpenPlan")}
                      </Link>
                    </div>
                  )}
                </div>
              )}

              <label
                className="note"
                style={{ display: "flex", gap: 6, alignItems: "center", marginTop: 10 }}
              >
                <input
                  type="checkbox"
                  checked={mgmtManual}
                  disabled={running}
                  onChange={(e) => setMgmtManual(e.target.checked)}
                />
                {t("serialAdd.siteMgmtOverride")}
              </label>
              {mgmtManual && (
                <div className="row" style={{ gap: 8, flexWrap: "wrap", marginTop: 8 }}>
                  <div style={{ width: 160 }}>
                    <Field label={t("serialAdd.mgmtIp") as string}>
                      <input
                        value={mgmtIp}
                        disabled={running}
                        onChange={(e) => setMgmtIp(e.target.value)}
                      />
                    </Field>
                  </div>
                  <div style={{ width: 160 }}>
                    <Field label={t("serialAdd.mgmtMask") as string}>
                      <input
                        value={mgmtMask}
                        disabled={running}
                        onChange={(e) => setMgmtMask(e.target.value)}
                      />
                    </Field>
                  </div>
                  <div style={{ width: 160 }}>
                    <Field label={t("serialAdd.mgmtGateway") as string}>
                      <input
                        value={mgmtGateway}
                        disabled={running}
                        onChange={(e) => setMgmtGateway(e.target.value)}
                      />
                    </Field>
                  </div>
                  <div style={{ width: 160 }}>
                    <Field label={t("serialAdd.mgmtIface") as string}>
                      <input
                        value={mgmtIface}
                        disabled={running}
                        onChange={(e) => setMgmtIface(e.target.value)}
                      />
                    </Field>
                  </div>
                </div>
              )}

              <div className="actions">
                <ActionButton
                  kind="play"
                  disabled={
                    !connected ||
                    !paramsLocked ||
                    running ||
                    detecting ||
                    !mgmtIp.trim() ||
                    !mgmtMask.trim()
                  }
                  onClick={() => void runSoftReset()}
                >
                  {running ? t("serialAdd.softResetRunning") : t("serialAdd.softResetStart")}
                </ActionButton>
                {running && (
                  <ActionButton
                    kind="pause"
                    className="secondary"
                    onClick={() => setPausedState(!paused)}
                  >
                    {paused
                      ? t("serialAdd.resumeProvisioning")
                      : t("serialAdd.pauseProvisioning")}
                  </ActionButton>
                )}
                {running && (
                  <ActionButton
                    kind="cancel"
                    className="secondary"
                    onClick={() => abortRef.current?.abort()}
                  >
                    {t("common.cancel")}
                  </ActionButton>
                )}
              </div>
              {paused && (
                <div className="note" style={{ marginTop: 6 }}>
                  {t("serialAdd.pausedNote")}
                </div>
              )}
            </div>
          )}

          <div className="card" style={{ marginTop: 12 }}>
            <div className="row" style={{ alignItems: "center", gap: 8 }}>
              <div className="kicker" style={{ marginBottom: 0 }}>
                {ttyOpen ? t("serialAdd.ttyTitle") : t("serialAdd.consoleCapture")}
              </div>
              <span style={{ marginLeft: "auto" }} />
              <ActionButton
                kind="terminal"
                className={ttyOpen ? "" : "secondary"}
                disabled={!connected}
                onClick={() => {
                  var opening = !ttyOpen;
                  setTtyOpen(opening);
                  if (opening && running && !pausedRef.current) {
                    // Two writers on one UART is chaos: taking the keyboard
                    // pauses the script at its next step.
                    setPausedState(true);
                  }
                  if (opening) {
                    requestAnimationFrame(() => captureRef.current?.focus());
                  }
                }}
              >
                {ttyOpen ? t("serialAdd.closeTTY") : t("serialAdd.openTTY")}
              </ActionButton>
            </div>
            {ttyOpen && (
              <div className="note" style={{ marginTop: 6 }}>
                {t("serialAdd.ttyHint")}
              </div>
            )}
            <textarea
              ref={captureRef}
              className="raw-input"
              rows={ttyOpen ? 16 : 10}
              value={capture}
              readOnly={ttyOpen}
              onChange={(e) => {
                if (!ttyOpen) setCapture(e.target.value);
              }}
              onKeyDown={ttyKeyDown}
              onPaste={ttyPaste}
              style={ttyOpen ? { caretColor: "transparent" } : undefined}
              placeholder={
                (ttyOpen
                  ? t("serialAdd.ttyPlaceholder")
                  : t("serialAdd.capturePlaceholder")) as string
              }
            />
            <div className="row" style={{ gap: 6 }}>
              <ActionButton
                kind="cancel"
                className="secondary"
                onClick={() => {
                  setCapture("");
                  sessionRef.current?.clearCapture();
                }}
              >
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

          {action === "adopt" && (
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
                <div style={{ width: 200 }}>
                  <Field label={t("serialAdd.mgmtIpIfKnown") as string}>
                    <input value={ipKnown} onChange={(e) => setIpKnown(e.target.value)} />
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
          )}

          {candidate && (
            <div className="card" style={{ marginTop: 12 }}>
              <div className="kicker">{t("serialAdd.candidateCreated")}</div>
              <div className="note" style={{ marginTop: 6 }}>
                {candidate.hostname || t("serialAdd.unknownHostname")} ·{" "}
                {[candidate.vendor, candidate.model].filter(Boolean).join(" ") ||
                  t("serialAdd.unidentifiedVendor")}{" "}
                ·{" "}
                {t("resetProcedures.driverLabel", { driver: candidate.driver })} (
                {t("discover.confidence", { value: candidate.confidence })})
                {candidate.has_config ? ` · ${t("serialAdd.configReadyToImport")}` : ""}
              </div>
              <div className="actions">
                <ActionButton kind="adopt" onClick={() => setAdopting(candidate)}>
                  {t("discover.adoptEllipsis")}
                </ActionButton>
              </div>
            </div>
          )}
        </>
      )}

      {/* Paste-only fallback when Web Serial is missing: keep adopt path usable */}
      {!available && (
        <>
          <div className="card" style={{ marginTop: 12 }}>
            <div className="kicker">{t("serialAdd.consoleCapture")}</div>
            <textarea
              className="raw-input"
              rows={12}
              value={capture}
              onChange={(e) => setCapture(e.target.value)}
              placeholder={t("serialAdd.capturePlaceholder") as string}
            />
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
        </>
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

function Choice(props: {
  selected: boolean;
  onClick: () => void;
  title: string;
  detail: string;
}) {
  return (
    <button
      type="button"
      className="card"
      onClick={props.onClick}
      style={{
        textAlign: "left",
        // A FIXED basis so every choice is the same size, whatever its label —
        // the row wraps into a tidy grid instead of ragged content-width boxes.
        flex: "0 0 240px",
        width: 240,
        boxSizing: "border-box",
        cursor: "pointer",
        // The border is ALWAYS 2px (transparent when unselected) so selecting a
        // box does not nudge the layout by a pixel.
        border: props.selected
          ? "2px solid var(--accent)"
          : "2px solid var(--border)",
        background: props.selected ? "var(--bg-hover)" : undefined,
      }}
    >
      <div style={{ fontWeight: 600 }}>{props.title}</div>
      <div className="note" style={{ marginTop: 4 }}>
        {props.detail}
      </div>
    </button>
  );
}

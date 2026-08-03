/**
 * Ship Web Serial wizard run-log lines to the controller so autodetection /
 * soft-reset can be inspected via docker logs (`serial.session`) and
 * GET /api/discovery/serial-log.
 */

import { post } from "../../api";

export type SerialLogMeta = {
  sessionId: string;
  siteId?: string;
  equipment?: string;
  action?: string;
  phase?: string;
};

export function newSerialSessionId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `serial-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}

export type SerialLogReporter = {
  setMeta: (partial: Partial<SerialLogMeta>) => void;
  /** Reset session id (new connect / re-detect). */
  newSession: () => string;
  sessionId: () => string;
  line: (msg: string, phase?: string) => void;
  flush: () => Promise<void>;
};

export function createSerialLogReporter(initial?: Partial<SerialLogMeta>): SerialLogReporter {
  var meta: SerialLogMeta = {
    sessionId: initial?.sessionId || newSerialSessionId(),
    siteId: initial?.siteId || "",
    equipment: initial?.equipment || "",
    action: initial?.action || "",
    phase: initial?.phase || "",
  };
  var pending: { phase: string; msg: string }[] = [];
  var timer: ReturnType<typeof setTimeout> | null = null;
  var flushing: Promise<void> | null = null;

  var schedule = () => {
    if (timer) return;
    timer = setTimeout(() => {
      timer = null;
      void flush();
    }, 400);
  };

  var flush = async () => {
    if (flushing) {
      await flushing;
      return;
    }
    if (pending.length === 0) return;
    var batch = pending;
    pending = [];
    // Group by phase to keep batches coherent.
    var byPhase = new Map<string, string[]>();
    var i = 0;
    for (i = 0; i < batch.length; i++) {
      var p = batch[i].phase || meta.phase || "";
      var list = byPhase.get(p);
      if (!list) {
        list = [];
        byPhase.set(p, list);
      }
      list.push(batch[i].msg);
    }
    flushing = (async () => {
      var entries = Array.from(byPhase.entries());
      var e = 0;
      for (e = 0; e < entries.length; e++) {
        var [phase, lines] = entries[e];
        try {
          await post("/api/discovery/serial-log", {
            session_id: meta.sessionId,
            site_id: meta.siteId || "",
            equipment: meta.equipment || "",
            action: meta.action || "",
            phase,
            lines,
          });
        } catch {
          /* diagnosis must not break the serial run */
        }
      }
    })();
    try {
      await flushing;
    } finally {
      flushing = null;
    }
  };

  return {
    setMeta(partial) {
      if (partial.sessionId !== undefined) meta.sessionId = partial.sessionId;
      if (partial.siteId !== undefined) meta.siteId = partial.siteId;
      if (partial.equipment !== undefined) meta.equipment = partial.equipment;
      if (partial.action !== undefined) meta.action = partial.action;
      if (partial.phase !== undefined) meta.phase = partial.phase;
    },
    newSession() {
      meta.sessionId = newSerialSessionId();
      pending = [];
      return meta.sessionId;
    },
    sessionId() {
      return meta.sessionId;
    },
    line(msg, phase) {
      pending.push({ msg, phase: phase || meta.phase || "" });
      if (pending.length >= 40) {
        void flush();
      } else {
        schedule();
      }
    },
    flush,
  };
}

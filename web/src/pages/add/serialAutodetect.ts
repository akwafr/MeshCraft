/**
 * Software baud/framing autodetection for Web Serial.
 * The API requires an explicit baudRate on open(); we trial a matrix and score
 * printable console text + known login/banner markers.
 */

import {
  formatSerialParams,
  type SerialParams,
  type SerialPortLike,
} from "./serialTypes";

export type AutodetectLog = (msg: string) => void;

export type AutodetectResult = {
  params: SerialParams;
  score: number;
  sample: string;
};

/** FS.com S3900 / S3900-R console is documented as 115200 8N1. */
const BAUDS_FS_S3900 = [115200, 9600, 19200, 38400, 57600];
/** Cisco Catalyst consoles are 9600 8N1 — the opposite end from the FS range. */
const BAUDS_CISCO = [9600, 115200, 19200, 38400, 57600];
const BAUDS_DEFAULT = [9600, 115200, 19200, 38400, 57600];

const FRAMINGS: Omit<SerialParams, "baudRate">[] = [
  { dataBits: 8, stopBits: 1, parity: "none", flowControl: "none" },
  { dataBits: 7, stopBits: 1, parity: "even", flowControl: "none" },
  { dataBits: 8, stopBits: 1, parity: "even", flowControl: "none" },
  { dataBits: 8, stopBits: 1, parity: "odd", flowControl: "none" },
  { dataBits: 8, stopBits: 2, parity: "none", flowControl: "none" },
  { dataBits: 8, stopBits: 1, parity: "none", flowControl: "hardware" },
];

const MARKERS =
  /login\s*:|user\s*name\s*:|password\s*:|fortiswitch|fortios|s3900|fsos|mikrotik|routeros|cisco|catalyst|ios software|press\s+return\s+to\s+get\s+started|initial\s+configuration\s+dialog|arub|procurve|switch>|switch#|#\s*$|>\s*$/im;

function buildMatrix(equipmentHint: string): SerialParams[] {
  var bauds = BAUDS_DEFAULT;
  if (
    equipmentHint === "fs3900" ||
    equipmentHint === "fs-s3900" ||
    equipmentHint === "s3900"
  ) {
    bauds = BAUDS_FS_S3900;
  } else if (
    equipmentHint === "cisco2960x" ||
    equipmentHint === "cisco-ios" ||
    equipmentHint === "c2960x" ||
    equipmentHint === "2960x"
  ) {
    bauds = BAUDS_CISCO;
  }
  var out: SerialParams[] = [];
  var b = 0;
  var f = 0;
  for (b = 0; b < bauds.length; b++) {
    for (f = 0; f < FRAMINGS.length; f++) {
      // Skip hardware flow-control until plain 8N1 family has been tried at this baud.
      if (FRAMINGS[f].flowControl === "hardware" && f > 0) {
        // only append hwfc after exhausting none-parity variants at this baud — already ordered
      }
      out.push({ baudRate: bauds[b], ...FRAMINGS[f] });
    }
  }
  return out;
}

function scoreSample(text: string): number {
  if (!text) return 0;
  var i = 0;
  var printable = 0;
  var bad = 0;
  for (i = 0; i < text.length; i++) {
    var c = text.charCodeAt(i);
    if (c === 0 || c === 0xfffd) {
      bad++;
      continue;
    }
    if (c === 9 || c === 10 || c === 13 || (c >= 32 && c < 127)) {
      printable++;
    } else if (c < 32) {
      bad++;
    } else {
      // high bytes: mild penalty
      bad += 0.5;
    }
  }
  var len = text.length;
  var ratio = printable / len;
  var score = ratio * 40;
  if (MARKERS.test(text)) score += 50;
  if (/\b(login|password|username)\b/i.test(text)) score += 15;
  if (/[#>]\s*$/m.test(text)) score += 10;
  score -= Math.min(30, bad * 2);
  return score;
}

async function safeClose(port: SerialPortLike): Promise<void> {
  try {
    await port.close();
  } catch {
    /* already closed or unplugged */
  }
}

async function openWith(
  port: SerialPortLike,
  params: SerialParams,
): Promise<void> {
  await port.open({
    baudRate: params.baudRate,
    dataBits: params.dataBits,
    stopBits: params.stopBits,
    parity: params.parity,
    flowControl: params.flowControl,
    bufferSize: 4096,
  });
}

/**
 * Try candidates until one scores above threshold, or return the best attempt.
 * Leaves the port open with the winning (or best) parameters and a live reader
 * is NOT started here — the caller owns the read loop after return.
 */
export async function autodetectSerialParams(opts: {
  port: SerialPortLike;
  equipmentHint: string;
  log?: AutodetectLog;
  listenMs?: number;
  /** Stop early when score >= this (default 55). */
  threshold?: number;
  /** Cap how many framing/baud combos to try (default 12 — covers common cases fast). */
  maxAttempts?: number;
  signal?: AbortSignal;
}): Promise<AutodetectResult> {
  var listenMs = opts.listenMs ?? 1200;
  var threshold = opts.threshold ?? 55;
  var maxAttempts = opts.maxAttempts ?? 12;
  var matrix = buildMatrix(opts.equipmentHint).slice(0, maxAttempts);
  var best: AutodetectResult | null = null;
  var i = 0;

  for (i = 0; i < matrix.length; i++) {
    if (opts.signal?.aborted) {
      throw new Error("serial autodetection aborted");
    }
    var params = matrix[i];
    opts.log?.(`trying ${formatSerialParams(params)}…`);
    await safeClose(opts.port);
    // Brief settle so the OS releases the handle.
    await new Promise((r) => setTimeout(r, 80));
    try {
      await openWith(opts.port, params);
    } catch (e) {
      opts.log?.(`  open failed: ${(e as Error).message || e}`);
      continue;
    }

    var sample = await listenAndWake(opts.port, listenMs);
    var score = scoreSample(sample);
    opts.log?.(
      `  score=${score.toFixed(0)} sample=${JSON.stringify(sample.slice(-120))}`,
    );

    if (!best || score > best.score) {
      best = { params, score, sample };
    }
    if (score >= threshold) {
      opts.log?.(`locked ${formatSerialParams(params)} (score ${score.toFixed(0)})`);
      // Port already open with these params.
      return best;
    }
    await safeClose(opts.port);
  }

  if (!best || best.score < 15) {
    throw new Error(
      "could not autodetect serial parameters — try manual override",
    );
  }

  // Re-open with best params.
  await safeClose(opts.port);
  await new Promise((r) => setTimeout(r, 80));
  await openWith(opts.port, best.params);
  opts.log?.(
    `locked ${formatSerialParams(best.params)} (best score ${best.score.toFixed(0)})`,
  );
  return best;
}

async function listenAndWake(port: SerialPortLike, listenMs: number): Promise<string> {
  if (!port.readable || !port.writable) return "";
  var reader = port.readable.getReader();
  var decoder = new TextDecoder();
  var buf = "";
  var done = false;

  var collect = (async () => {
    try {
      while (!done) {
        var result = await reader.read();
        if (result.done) break;
        if (result.value) buf += decoder.decode(result.value, { stream: true });
      }
    } catch {
      /* cancelled */
    }
  })();

  try {
    var writer = port.writable.getWriter();
    try {
      await writer.write(new TextEncoder().encode("\r"));
      await new Promise((r) => setTimeout(r, Math.min(400, listenMs / 2)));
      await writer.write(new TextEncoder().encode("\r"));
    } finally {
      writer.releaseLock();
    }
  } catch {
    /* write may fail on some adapters during probe */
  }

  await new Promise((r) => setTimeout(r, listenMs));
  done = true;
  try {
    await reader.cancel();
  } catch {
    /* ignore */
  }
  try {
    reader.releaseLock();
  } catch {
    /* ignore */
  }
  await collect;
  return buf;
}

export { buildMatrix, scoreSample };

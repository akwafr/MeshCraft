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

/** FS.com S3900 / S3900-R console is documented as 115200 8N1 — and so is the
 * S3400 PoE+ range (PoE+ series reset guide), unrelated CLI notwithstanding. */
const BAUDS_FS_S3900 = [115200, 9600, 19200, 38400, 57600];
/** Cisco Catalyst consoles are 9600 8N1 — the opposite end from the FS range.
 * Arista consoles are documented at the same 9600 8N1, so they share the order. */
const BAUDS_CISCO = [9600, 115200, 19200, 38400, 57600];
/**
 * Juniper EX consoles are 9600 8N1 with NO flow control, across the whole
 * range — Juniper's quick starts for the EX2300, EX2300-C, EX3400, EX4300 and
 * EX4600 all print the same block ("Baud rate—9600, Flow control—None,
 * Data—8, Parity—None, Stop bits—1, DCD state—Disregard"), and the Junos
 * overview adds "The console port is enabled by default, and its speed is 9600
 * baud". So the EX4300 and EX4600 are 9600 like the rest, NOT 115200.
 *
 * Shares the Cisco order rather than aliasing it, because the two ranges agree
 * only by coincidence and a change to one must not move the other. The higher
 * rates are tried after 9600 only because a previous operator may have changed
 * the speed from the loader (`set baudrate=…`), which persists across reboots
 * and is the one way an EX console ends up somewhere else.
 */
const BAUDS_JUNIPER = [9600, 115200, 19200, 38400, 57600];
const BAUDS_DEFAULT = [9600, 115200, 19200, 38400, 57600];

const FRAMINGS: Omit<SerialParams, "baudRate">[] = [
  { dataBits: 8, stopBits: 1, parity: "none", flowControl: "none" },
  { dataBits: 7, stopBits: 1, parity: "even", flowControl: "none" },
  { dataBits: 8, stopBits: 1, parity: "even", flowControl: "none" },
  { dataBits: 8, stopBits: 1, parity: "odd", flowControl: "none" },
  { dataBits: 8, stopBits: 2, parity: "none", flowControl: "none" },
  { dataBits: 8, stopBits: 1, parity: "none", flowControl: "hardware" },
];

/**
 * Markers worth 50 points — the difference between locking a working speed and
 * trying eleven more.
 *
 * "user\s*:" is here because of a real failure: an EdgeSwitch console
 * greeted us with "…agree to be bound by its terms.\r\n\r\nUser:", perfectly
 * readable at 115200 8N1, and scored 40 — printable ratio alone, no marker.
 * The threshold is 55, so autodetection rejected the right answer and kept
 * cycling the port until the adapter refused to open at all. "Username:"
 * was matched; "User:" was not.
 *
 * "arista" and "zerotouch" are the pair that matter on the Arista range, and
 * the second earns its place on its own: a switch fresh out of the factory is in
 * ZERO TOUCH PROVISIONING, and what it prints on the console while probing is
 * mostly ZTP progress lines — sometimes for a long stretch before any prompt
 * appears at all. A detection pass that only knew about prompts and vendor names
 * would score that stream on its printable ratio alone, the way the EdgeSwitch
 * banner was scored, and reject a perfectly readable console.
 */
/*
 * The JUNIPER additions below are here for the same reason `zerotouch` is, and
 * they matter more on this range than on any other, because the states an EX
 * console is most likely to be in when an operator opens the wizard all say
 * something other than a prompt:
 *
 *   "Amnesiac"            — the hostname placeholder a switch with NO
 *                             configuration logs in under. A console caught
 *                             just before its `login:` shows only this;
 *   "loader>"             — the boot loader, where the assisted hardware path
 *                             deliberately leaves the switch, and which carries
 *                             no vendor name at all;
 *   "pathname of shell"   — single-user mode, likewise;
 *   "Auto Image Upgrade:" — a zeroized EX2300 / EX3400 prints these for
 *                             minutes while it hunts for a ZTP server, and like
 *                             the Arista's `%ZTP-` chatter it is often the ONLY
 *                             thing on the console. Scored on printable ratio
 *                             alone that comes to 40 against a threshold of 55,
 *                             and the right speed would be rejected;
 *   "Booting [kernel]"    — the countdown, which proves the speed is right at
 *                             the exact moment the hardware path needs to know.
 */
const MARKERS =
  /login\s*:|user\s*name\s*:|user\s*:|password\s*:|fortiswitch|fortios|s3900|fsos|s3400|bdcom|edgeswitch|ubnt|ubiquiti|unifi|mikrotik|routeros|cisco|catalyst|ios software|arista|zerotouch|%ZTP-|aboot|press\s+return\s+to\s+get\s+started|initial\s+configuration\s+dialog|arub|procurve|juniper|junos|amnesiac|auto image upgrade|loader>|pathname of (?:the )?shell|booting \[kernel\]|switch>|switch#|#\s*$|>\s*$/im;

function buildMatrix(equipmentHint: string): SerialParams[] {
  var bauds = BAUDS_DEFAULT;
  if (
    equipmentHint === "fs3900" ||
    equipmentHint === "fs-s3900" ||
    equipmentHint === "s3900" ||
    equipmentHint === "fs3400" ||
    equipmentHint === "fs-s3400" ||
    equipmentHint === "s3400" ||
    equipmentHint === "edgeswitch" ||
    equipmentHint === "ubiquiti-edgeswitch" ||
    equipmentHint === "unifis16" ||
    equipmentHint === "us16"
  ) {
    bauds = BAUDS_FS_S3900;
  } else if (
    equipmentHint === "cisco2960x" ||
    equipmentHint === "cisco-ios" ||
    equipmentHint === "c2960x" ||
    equipmentHint === "2960x" ||
    equipmentHint === "arista" ||
    equipmentHint === "arista-eos" ||
    equipmentHint === "eos" ||
    equipmentHint === "dcs7050"
  ) {
    bauds = BAUDS_CISCO;
  } else if (
    equipmentHint === "juniper" ||
    equipmentHint === "juniper-junos" ||
    equipmentHint === "junos" ||
    equipmentHint === "ex2200" ||
    equipmentHint === "ex2300" ||
    equipmentHint === "ex3400" ||
    equipmentHint === "ex4200" ||
    equipmentHint === "ex4300"
  ) {
    bauds = BAUDS_JUNIPER;
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
 * Open, RETRYING with a growing pause.
 *
 * A USB serial adapter cycled a dozen times in a few seconds starts answering
 * "Failed to open port" to everything — observed on an EdgeSwitch run where
 * the last three candidates failed to open and then the final re-open of the
 * BEST parameters failed too, turning a successful detection into a dead
 * wizard. The port is not broken, it is busy: give it time rather than give up.
 */
async function openWithRetry(
  port: SerialPortLike,
  params: SerialParams,
  attempts: number,
  log?: AutodetectLog,
): Promise<void> {
  var lastErr: unknown;
  var i = 0;
  for (i = 0; i < attempts; i++) {
    if (i > 0) {
      var pause = 250 * i;
      log?.(`  port busy, retrying in ${pause}ms…`);
      await new Promise((r) => setTimeout(r, pause));
    }
    try {
      await openWith(port, params);
      return;
    } catch (e) {
      lastErr = e;
      await safeClose(port);
    }
  }
  throw lastErr instanceof Error ? lastErr : new Error(String(lastErr));
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
    // Settle so the OS releases the handle. 80ms proved too short once the
    // adapter had been cycled a dozen times.
    await new Promise((r) => setTimeout(r, 200));
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
      // NOT "port already open with these params" — that comment shipped a
      // deaf wizard. The probe (listenAndWake) ends with reader.cancel(),
      // and on Web Serial CANCELLING THE READER CLOSES THE READABLE STREAM:
      // a caller that starts its read loop on this port hears NOTHING from
      // then on, while its writes still leave — the exact field shape of the
      // 2960-X hardware reset stuck at "test the line" (the Cisco locks at
      // the FIRST candidate, 9600, so only this early path was deaf; the FS
      // path fell through to the re-open below and never showed it). Hand
      // back a FRESH open, same as the fallback path always did.
      await safeClose(opts.port);
      await new Promise((r) => setTimeout(r, 300));
      await openWithRetry(opts.port, params, 4, opts.log);
      return best;
    }
    await safeClose(opts.port);
  }

  if (!best || best.score < 15) {
    throw new Error(
      "could not autodetect serial parameters — try manual override",
    );
  }

  // Re-open with the best parameters. This is the step that must not fail
  // after the whole matrix has been tried: the answer is already known, and
  // losing it to a busy adapter is the worst possible moment to give up.
  await safeClose(opts.port);
  await new Promise((r) => setTimeout(r, 300));
  await openWithRetry(opts.port, best.params, 4, opts.log);
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

// NOTE: this module cannot be exercised by the repo's hand-run node harness
// (`node --experimental-strip-types`): unlike the soft-reset scripts, which
// import only TYPES across files, it imports VALUES from ./serialTypes, and
// bare node will not resolve an extensionless specifier. The sample that
// caused the EdgeSwitch failure is recorded verbatim in the MARKERS comment
// above so the reason for each marker survives.
export { buildMatrix, scoreSample, openWithRetry };

/**
 * Cisco Catalyst (IOS) soft reset + management push over Web Serial.
 *
 * Calibrated on the WS-C2960X range, IOS 15.2(7)E. Sibling of
 * fsS3900SoftReset.ts and deliberately NOT a generalization of it: the two
 * CLIs disagree on almost every prompt, and a shared "console script" would be
 * a pile of conditionals nobody could read a failure out of.
 *
 * # Status of what is written here
 *
 * DOCUMENTED, not captured. Every string comes from the vendor's command
 * reference and password-recovery procedure — see internal/driver/cisco/ios,
 * which carries the same caveat and the same command set for the SSH path.
 *
 * # The console flow, and the three prompts that decide whether this works
 *
 *   write erase              → "Continue? [confirm]"                   → CR
 *   delete flash:vlan.dat    → "Delete filename [vlan.dat]?"           → CR
 *                            → "Delete flash:/vlan.dat? [confirm]"     → CR
 *   reload                   → "Save? [yes/no]:"                       → no  ← NOT CR
 *                            → "Proceed with reload? [confirm]"        → CR
 *   (reboot)                 → "…initial configuration dialog? [yes/no]:" → no
 *                            → "Press RETURN to get started!"          → CR
 *
 * The « no » to Save? is the one that matters: its default writes the running
 * configuration back over the NVRAM that was just erased, and the reset then
 * silently does nothing.
 *
 * # Console speed
 *
 * 9600 8N1 on this range — NOT the 115200 the FS S3900 uses. Getting this
 * wrong produces a screen of garbage that the autodetector scores as noise.
 */

import type { SerialSession } from "./serialExpect";

export type SoftResetLog = (msg: string) => void;

export type CiscoIosSoftResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  /** Credentials that work BEFORE the reset. A factory Catalyst has none. */
  currentUser: string;
  currentPassword: string;
  /** Enable (privileged) password before the reset — empty on a factory unit. */
  enablePassword: string;
  /** Admin account to create after the reset (from instance credentials). */
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  mgmtMask: string;
  gateway?: string;
  /** « vlan 99 » (default « vlan 1 ») — the SVI that carries management. */
  mgmtIface?: string;
  /**
   * Every VLAN the SITE declares, created on the device during provisioning.
   *
   * A switch coming out of a factory reset has VLAN 1 and nothing else, so any
   * port profile applied later — access on VLAN 30, trunk carrying 40 — fails on
   * a VLAN the device has never heard of. Creating them here means the switch
   * leaves provisioning already able to carry the site, and the first profile
   * apply has nothing to discover.
   */
  siteVlans?: { id: number; name?: string }[];
  /** Needed before « crypto key generate rsa », or SSH stays impossible. */
  domainName?: string;
  hostname?: string;
  signal?: AbortSignal;
};

const USERNAME = /user\s*name\s*:/i;
const PASSWORD = /password\s*:/i;
const PROMPT_PRIV = /#\s*$/m;
const PROMPT_USER = />\s*$/m;
/** "Continue? [confirm]", "Proceed with reload? [confirm]" — a bare CR answers. */
const CONFIRM = /\[confirm\]/i;
/** "Delete filename [vlan.dat]?" — a bare CR keeps the proposed name. */
const FILENAME_PROMPT = /delete\s+filename\s*\[[^\]]*\]\s*\?/i;
/** "System configuration has been modified. Save? [yes/no]:" — answer « no ». */
const SAVE_YESNO = /save\s*\?\s*\[yes\/no\]\s*:/i;
/** The setup dialog after a reset. It swallows every command typed at it. */
const SETUP_DIALOG = /initial\s+configuration\s+dialog\s*\?\s*\[yes\/no\]\s*:/i;
const TERMINATE_AUTOINSTALL = /terminate\s+autoinstall\s*\?\s*\[yes\]\s*:/i;
const PRESS_RETURN = /press\s+return\s+to\s+get\s+started/i;
const RSA_REPLACE = /replace\s+them\s*\?\s*\[yes\/no\]/i;
const BOOT_MARK =
  /Boot\s+loader|Loading\s|Initializing\s|POST:|Base\s+ethernet\s+MAC|cisco\s+WS-C|System\s+Bootstrap/i;

/**
 * IOS REFUSALS — and deliberately NOT the syslog lines that share their shape.
 *
 * "%LINK-3-UPDOWN: Interface GigabitEthernet1/0/5, changed state to up" lands
 * in the middle of command output and starts with a percent sign. Treating it
 * as an error turns every port that comes up during the push into a failure.
 * "% Generating 2048 bit RSA keys" is likewise progress, not a problem.
 */
const CLI_ERROR =
  /%\s*(?:invalid input|incomplete command|ambiguous command|unknown command|unrecognized command|bad mask|bad ip address|please define|access denied|error[: ]|not enough|insufficient)[^\n]*|command rejected:[^\n]*/i;

/** Output pager. Web Serial cannot announce a tall terminal. */
const PAGER = /-+\s*more\s*-+\s*$/i;
/** Any sign the console fell back to login mid-script. */
const LOGIN_FALLBACK = /user\s*name\s*:\s*$|password\s*:\s*$|press\s+return\s+to\s+get\s+started/i;
/** "Building configuration... / [OK]" — the write reached NVRAM. */
const SAVE_OK = /\[OK\]|bytes\s+copied/i;

type ReplyKind = "confirm" | "replaceKeys" | "error" | "prompt" | "timeout";
export type LoginCandidate = { user: string; pass: string; enable: string; label: string };

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("soft reset aborted");
}

function clipTail(text: string, n: number): string {
  return text.slice(-n).replace(/\s+/g, " ").trim();
}

/** Expand the short form « Gi1/0/28 » into the full name IOS prints back. */
export function fullIfName(short: string): string {
  var m = /^([A-Za-z]+)([\d/]+)$/.exec(short.trim());
  if (!m) return short.trim();
  var media = m[1].toLowerCase();
  var rest = m[2];
  if (media === "fa" || media === "fastethernet") return "FastEthernet" + rest;
  if (media === "gi" || media === "gigabitethernet") return "GigabitEthernet" + rest;
  if (media === "tw" || media === "twogigabitethernet") return "TwoGigabitEthernet" + rest;
  if (media === "te" || media === "tengigabitethernet") return "TenGigabitEthernet" + rest;
  return short.trim();
}

/**
 * Ports whose link is up, from « show interfaces status ».
 *
 * The Status word is the pivot and not the column position: the Name column is
 * free text that may contain spaces, so splitting on whitespace from the left
 * shifts every field on any port someone has described.
 */
export function parseUpPorts(status: string): string[] {
  var re =
    /^(\S+)\s+.*?\b(connected|notconnect|disabled|err-disabled|errdisable|inactive|suspended|monitoring|faulty)\b/gim;
  var out: string[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(status)) !== null) {
    if (m[2].toLowerCase() !== "connected") continue;
    // Only faceplate ports of stack member 1 — same coverage as the connector.
    if (!/^(fa|gi|tw|te)\d+\/\d+(\/\d+)?$/i.test(m[1])) continue;
    var full = fullIfName(m[1]);
    if (seen[full]) continue;
    seen[full] = true;
    out.push(full);
  }
  return out;
}

/**
 * EVERY faceplate port, linked or not, from « show interfaces status ».
 *
 * Distinct from [parseUpPorts], which keeps only « connected »: that one answers
 * "where is the uplink", this one answers "what does this switch have". A blanket
 * port policy has to cover the ports nothing is plugged into yet — those are
 * precisely the ones someone will patch tomorrow, when nobody is watching a
 * console.
 */
export function parseAllPorts(status: string): string[] {
  var re =
    /^(\S+)\s+.*?\b(connected|notconnect|disabled|err-disabled|errdisable|inactive|suspended|monitoring|faulty)\b/gim;
  var out: string[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(status)) !== null) {
    if (!/^(fa|gi|tw|te)\d+\/\d+(\/\d+)?$/i.test(m[1])) continue;
    var full = fullIfName(m[1]);
    if (seen[full]) continue;
    seen[full] = true;
    out.push(full);
  }
  return out;
}

/**
 * Group ports into « interface range » expressions, one per media family.
 *
 * NOT a micro-optimisation. This console runs at 9600 8N1, and every command in
 * [runConfigCmds] waits for its prompt before the next is sent. A 48-port switch
 * configured port by port is 48 blocks of four commands — around two hundred
 * round trips, minutes of console time during which a reset switch sits
 * unconfigured. As ranges it is two commands per media family.
 *
 * One command per family rather than one comma-separated command for all: IOS
 * caps « interface range » at five ranges, and a stack with mixed media plus
 * non-contiguous numbering can exceed that. Splitting by family keeps each
 * command a single contiguous run in the overwhelming majority of cases, and
 * degrades into several runs rather than a refusal in the rest.
 */
export function portRanges(ports: string[]): string[] {
  // Family = media name + the stack/module part ("GigabitEthernet1/0/"), so
  // members of a stack do not get merged into a range that spans two switches.
  var families: string[] = [];
  var byFamily: Record<string, number[]> = {};
  var p: string;
  for (p of ports) {
    var m = /^([A-Za-z]+)((?:\d+\/)+)(\d+)$/.exec(p);
    if (!m) continue;
    var key = m[1] + m[2];
    if (!byFamily[key]) {
      byFamily[key] = [];
      families.push(key);
    }
    byFamily[key].push(Number(m[3]));
  }
  var out: string[] = [];
  var key: string;
  for (key of families) {
    var nums = byFamily[key].slice().sort((a, b) => a - b);
    var runs: string[] = [];
    var i = 0;
    while (i < nums.length) {
      var start = nums[i];
      var end = start;
      while (i + 1 < nums.length && nums[i + 1] === end + 1) {
        i++;
        end = nums[i];
      }
      runs.push(start === end ? key + start : key + start + "-" + end);
      i++;
    }
    out.push(runs.join(", "));
  }
  return out;
}

/**
 * Global commands that make IOS accept optics it does not read as Cisco-coded.
 *
 * Mirrors internal/driver/cisco/ios UnsupportedTransceiverCmds() — the two
 * paths must never drift into two different remediations.
 *
 * APPLIED AT EVERY PROVISIONING RUN. Cisco prints a warranty disclaimer on the
 * first command, and that is a real consideration the fleet owner has weighed:
 * without these lines a third-party SFP leaves its cage looking EMPTY, so
 * uplink discovery finds nothing, the management VLAN is carried on no port,
 * and the run fails reporting an unreachable address — every symptom pointing
 * at the network and none at the optic.
 *
 * They live in the running configuration, so the factory reset that precedes
 * this push is exactly what removes them. That is why they are re-applied on
 * every run rather than set once.
 */
export const UNSUPPORTED_TRANSCEIVER_CMDS = [
  "service unsupported-transceiver",
  "no errdisable detect cause gbic-invalid",
];

/** The syslog IOS emits when it refuses an optic. */
const UNSUPPORTED_TRANSCEIVER_LOG =
  /%PHY-4-UNSUPPORTED_TRANSCEIVER|%GBIC_SECURITY[^\n]*|unsupported\s+transceiver/i;

/**
 * Ports the SWITCH shut by itself, with the reason, from
 * « show interfaces status err-disabled ».
 *
 *   Port      Name               Status       Reason               Err-disabled Vlans
 *   Gi1/0/25                     err-disabled gbic-invalid
 *
 * Same pivot as parseUpPorts — the Name column is free text — with the reason
 * taken as the first word after the status.
 */
export function parseErrDisabled(status: string): { port: string; reason: string }[] {
  var re = /^(\S+)\s+(?:.*?\s+)?err-?disabled?\s+(\S+)/gim;
  var out: { port: string; reason: string }[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(status)) !== null) {
    if (!/^(fa|gi|tw|te)\d+\/\d+(\/\d+)?$/i.test(m[1])) continue;
    var full = fullIfName(m[1]);
    if (seen[full]) continue;
    seen[full] = true;
    out.push({ port: full, reason: m[2].toLowerCase() });
  }
  return out;
}

/** Turn a dotted mask (or « /nn ») into the dotted mask IOS requires. */
export function toDottedMask(mask: string): string {
  var trimmed = mask.trim();
  if (/^\d+\.\d+\.\d+\.\d+$/.test(trimmed)) return trimmed;
  var bits = Number(trimmed.replace(/^\//, ""));
  if (!Number.isFinite(bits) || bits < 0 || bits > 32) bits = 24;
  var octets: number[] = [];
  var i = 0;
  for (i = 0; i < 4; i++) {
    var b = bits - i * 8;
    if (b >= 8) octets.push(255);
    else if (b <= 0) octets.push(0);
    else octets.push(256 - Math.pow(2, 8 - b));
  }
  return octets.join(".");
}

/**
 * Collect a show command's output, feeding the pager when « --More-- » shows
 * up: Web Serial cannot claim a tall terminal, so output that fits a PuTTY
 * window may still paginate here and swallow every later command.
 */
export async function collectShow(
  session: SerialSession,
  cmd: string,
  totalMs: number,
): Promise<string> {
  session.mark();
  await session.send(cmd);
  var deadline = Date.now() + totalMs;
  var lastLen = 0;
  var quiet = 0;
  while (Date.now() < deadline && quiet < 3) {
    await session.sleep(500);
    var out = session.getSinceMark();
    if (PAGER.test(out.slice(-120).trimEnd())) {
      // Space = next page; the CR that send() appends would only add one line.
      await session.sendRaw(" ");
      quiet = 0;
      continue;
    }
    if (out.length > lastLen) {
      lastLen = out.length;
      quiet = 0;
    } else {
      quiet++;
    }
  }
  return session.getSinceMark();
}

/** After a command: a prompt to answer, a refusal, or back at #/> . */
async function expectReply(
  session: SerialSession,
  timeoutMs: number,
): Promise<{ kind: ReplyKind; match: string }> {
  try {
    var hit = await session.expectAny(
      [CONFIRM, CLI_ERROR, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "error", match: hit.match };
    // A prompt often arrives after an error line in the same burst — prefer the error.
    var err = session.getCapture().slice(-500).match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
  }
}

async function expectReplyOrLogin(
  session: SerialSession,
  timeoutMs: number,
): Promise<{ kind: ReplyKind | "login"; match: string }> {
  try {
    var hit = await session.expectAny(
      [CONFIRM, CLI_ERROR, LOGIN_FALLBACK, RSA_REPLACE, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "error", match: hit.match };
    if (hit.index === 2) return { kind: "login", match: hit.match };
    if (hit.index === 3) return { kind: "replaceKeys", match: hit.match };
    var err = session.getCapture().slice(-500).match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
  }
}

/**
 * Reach the privileged « # » prompt.
 *
 * A factory Catalyst asks for NOTHING: the console lands on « Switch> » and
 * « enable » gives « Switch# » with no password. A configured one may ask for
 * a username, a password, or both. All four shapes are handled, and the
 * decision is made on the LAST marker seen — a stale « Password: » earlier in
 * the buffer must not make us type the password into a fresh « Username: ».
 */
async function ensurePrivileged(
  session: SerialSession,
  user: string,
  password: string,
  enablePassword: string,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  session.mark();
  await session.send("");
  await session.sleep(600);

  var deadline = Date.now() + timeoutMs;
  var nudges = 0;
  while (Date.now() < deadline) {
    aborted(signal);
    var tail = session.getSinceMark().slice(-800) || session.getCapture().slice(-400);

    if (SETUP_DIALOG.test(tail)) {
      log("initial configuration dialog offered — declining (no)");
      session.mark();
      await session.send("no");
      await session.sleep(1500);
      continue;
    }
    if (TERMINATE_AUTOINSTALL.test(tail)) {
      log("autoinstall — terminating (yes)");
      session.mark();
      await session.send("yes");
      await session.sleep(1500);
      continue;
    }

    var lastPriv = tail.lastIndexOf("#");
    var lastUser = tail.search(/user\s*name\s*:/i);
    var lastPass = tail.toLowerCase().lastIndexOf("password:");
    var lastExec = tail.lastIndexOf(">");

    if (lastPriv >= 0 && lastPriv > lastUser && lastPriv > lastPass && /#\s*$/.test(tail)) {
      log("already at privileged prompt");
      return;
    }
    if (lastUser >= 0 && lastUser > lastPass) {
      log(`login as ${user || "(empty user)"}`);
      session.mark();
      await session.send(user);
      var afterUser = await session.expectAny(
        [PASSWORD, PROMPT_PRIV, PROMPT_USER, USERNAME],
        20000,
      );
      if (afterUser.index === 1) {
        log("login ok — privileged");
        return;
      }
      if (afterUser.index === 2) {
        await enterEnable(session, enablePassword, log);
        return;
      }
      if (afterUser.index === 3) {
        log("back at Username: — credential refused");
        continue;
      }
      session.mark();
      await session.send(password);
      var afterPass = await session.expectAny(
        [PROMPT_PRIV, PROMPT_USER, USERNAME, PASSWORD],
        20000,
      );
      if (afterPass.index === 0) {
        log("login ok — privileged");
        return;
      }
      if (afterPass.index === 1) {
        await enterEnable(session, enablePassword, log);
        return;
      }
      log("login refused — re-syncing");
      session.mark();
      await session.send("");
      await session.sleep(800);
      continue;
    }
    if (lastPass >= 0 && lastPass > lastUser) {
      // Line password only (no username prompt at all).
      log("line password prompt");
      session.mark();
      await session.send(password);
      var afterLine = await session.expectAny([PROMPT_PRIV, PROMPT_USER, PASSWORD], 20000);
      if (afterLine.index === 0) return;
      if (afterLine.index === 1) {
        await enterEnable(session, enablePassword, log);
        return;
      }
      log("line password refused");
      continue;
    }
    if (lastExec >= 0 && />\s*$/.test(tail)) {
      await enterEnable(session, enablePassword, log);
      return;
    }
    if (PRESS_RETURN.test(tail)) {
      session.mark();
      await session.send("");
      await session.sleep(800);
      continue;
    }

    nudges++;
    if (nudges > 10) {
      throw new Error("no Username / Password / prompt seen on the console");
    }
    session.mark();
    await session.send("");
    await session.sleep(900);
  }
  throw new Error("could not reach the Catalyst privileged prompt");
}

async function enterEnable(
  session: SerialSession,
  enablePassword: string,
  log: SoftResetLog,
): Promise<void> {
  session.mark();
  await session.send("enable");
  try {
    var hit = await session.expectAny([PASSWORD, PROMPT_PRIV], 8000);
    if (hit.index === 0) {
      session.mark();
      await session.send(enablePassword);
      await session.expect(PROMPT_PRIV, 15000);
    }
    log("privileged EXEC (#)");
  } catch {
    session.mark();
    await session.send("");
    await session.expect(PROMPT_PRIV, 5000);
    log("privileged EXEC (#)");
  }
}

/** Re-open a privileged session after an unexpected fallback to login. */
async function reloginAny(
  session: SerialSession,
  logins: LoginCandidate[],
  log: SoftResetLog,
  signal?: AbortSignal,
): Promise<void> {
  var i = 0;
  for (i = 0; i < logins.length; i++) {
    aborted(signal);
    try {
      log(`re-login as ${logins[i].label}…`);
      await ensurePrivileged(
        session,
        logins[i].user,
        logins[i].pass,
        logins[i].enable,
        log,
        30000,
        signal,
      );
      return;
    } catch (e) {
      log(`re-login ${logins[i].label} failed (${(e as Error).message || e})`);
    }
  }
  throw new Error("could not re-open a privileged session after console logout");
}

/**
 * Soft factory reset.
 *
 * « write erase » alone leaves flash:vlan.dat behind, so every VLAN survives
 * the reset — which is exactly the surprise that makes an operator think the
 * reset failed. Both are done, and the reload is answered « no » so the
 * running configuration is not written back over the NVRAM we just cleared.
 */
async function factoryReset(session: SerialSession, log: SoftResetLog, signal?: AbortSignal) {
  aborted(signal);

  log("write erase (nvram)…");
  session.mark();
  await session.send("write erase");
  var r1 = await expectReply(session, 15000);
  if (r1.kind === "confirm") {
    session.mark();
    await session.sendRaw("\r");
    log("write erase: confirmed");
  } else if (r1.kind === "error") {
    log(`write erase: CLI error « ${r1.match} » — continuing`);
  }
  await session.sleep(1500);

  log("delete flash:vlan.dat (the VLAN database does NOT go with write erase)…");
  session.mark();
  await session.send("delete flash:vlan.dat");
  // Two questions in a row: the file name, then the confirmation. Both take
  // their default, so a bare CR answers each.
  var round = 0;
  for (round = 0; round < 3; round++) {
    var hit: { index: number; match: string };
    try {
      hit = await session.expectAny([FILENAME_PROMPT, CONFIRM, CLI_ERROR, PROMPT_PRIV], 10000);
    } catch {
      break;
    }
    if (hit.index === 2) {
      // "%Error deleting flash:vlan.dat (No such file or directory)" is the
      // normal answer on a switch that never had VLANs. Not a failure.
      log(`delete vlan.dat: ${hit.match.trim()} — continuing`);
      break;
    }
    if (hit.index === 3) break;
    session.mark();
    await session.sendRaw("\r");
  }
  await session.sleep(1000);

  await reloadAndDeclineSave(session, log);
}

/**
 * « reload », answering « no » to the save question.
 *
 * THE prompt of this whole procedure. Its default is yes, and yes writes the
 * running configuration — which still holds everything — back into the NVRAM
 * that was just erased. The reset then appears to have done nothing at all.
 */
async function reloadAndDeclineSave(session: SerialSession, log: SoftResetLog) {
  log("reload…");
  session.mark();
  await session.send("reload");
  var round = 0;
  for (round = 0; round < 4; round++) {
    var hit: { index: number; match: string };
    try {
      hit = await session.expectAny([SAVE_YESNO, CONFIRM, CLI_ERROR], 20000);
    } catch {
      log("reload: no confirmation seen — continuing (a manual power cycle also works)");
      return;
    }
    if (hit.index === 0) {
      log("reload: « Save? » → no (yes would re-save the configuration just erased)");
      session.mark();
      await session.send("no");
      continue;
    }
    if (hit.index === 1) {
      log("reload: confirmed");
      session.mark();
      await session.sendRaw("\r");
      return;
    }
    log(`reload: CLI error « ${hit.match} » — continuing`);
    return;
  }
}

async function waitLoginAfterReboot(
  session: SerialSession,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  // Drop ALL pre-reboot text: a stale « # » would make us declare the switch
  // back and push configuration while the boot loader is still running.
  session.mark();
  log("reload confirmed — waiting for the switch to come back (a Catalyst takes ~2 min)…");
  var minBootMs = 60000;
  var deadline = Date.now() + timeoutMs;
  var bootWaitUntil = Date.now() + minBootMs;
  var lastProgress = Date.now();

  while (Date.now() < bootWaitUntil && Date.now() < deadline) {
    aborted(signal);
    try {
      await session.expect(BOOT_MARK, Math.min(5000, bootWaitUntil - Date.now()));
      log("boot output seen — still waiting for the console to settle…");
      break;
    } catch {
      /* quiet while rebooting */
    }
    if (Date.now() - lastProgress > 10000) {
      log(`rebooting (${Math.round((bootWaitUntil - Date.now()) / 1000)}s before looking for a prompt)…`);
      lastProgress = Date.now();
    }
  }

  session.mark();
  log("looking for the post-reset console (new data only)…");
  lastProgress = Date.now();
  var lastNudge = 0;

  while (Date.now() < deadline) {
    aborted(signal);
    try {
      var hit = await session.expectAny(
        [SETUP_DIALOG, TERMINATE_AUTOINSTALL, PRESS_RETURN, USERNAME, PASSWORD, PROMPT_USER, PROMPT_PRIV],
        Math.min(10000, deadline - Date.now()),
      );
      if (hit.index === 0) {
        log("initial configuration dialog offered — declining (no)");
        session.mark();
        await session.send("no");
        await session.sleep(2000);
        continue;
      }
      if (hit.index === 1) {
        log("autoinstall — terminating (yes)");
        session.mark();
        await session.send("yes");
        await session.sleep(2000);
        continue;
      }
      log("post-reset console reached");
      return;
    } catch {
      /* still booting */
    }
    var now = Date.now();
    if (now - lastProgress > 15000) {
      log(
        `still waiting for the console (${Math.round((deadline - now) / 1000)}s left) — ${
          clipTail(session.getSinceMark(), 80) || "no new serial output"
        }`,
      );
      lastProgress = now;
    }
    if (now - lastNudge > 30000) {
      session.mark();
      await session.send("");
      lastNudge = now;
    }
    await session.sleep(2000);
  }
  throw new Error("the switch did not present a console prompt after the reload (timeout)");
}

/**
 * Run configuration commands one at a time, VERIFYING the console after each.
 * Confirmations are answered, refusals are logged with the tail that says
 * which word the caret rejected, and a fallback to login triggers re-login +
 * « configure terminal » + a retry of the same command.
 */
export async function runConfigCmds(
  session: SerialSession,
  log: SoftResetLog,
  logins: LoginCandidate[],
  cmds: { cmd: string; label?: string; timeoutMs?: number }[],
  signal?: AbortSignal,
): Promise<void> {
  var enterConfig = async () => {
    session.mark();
    await session.send("configure terminal");
    await expectReply(session, 8000);
    await session.sleep(300);
  };
  await enterConfig();
  var i = 0;
  var relogins = 0;
  while (i < cmds.length) {
    aborted(signal);
    var c = cmds[i];
    var label = c.label || c.cmd;
    session.mark();
    await session.send(c.cmd);
    var reply = await expectReplyOrLogin(session, c.timeoutMs ?? 10000);
    if (reply.kind === "login") {
      relogins++;
      if (relogins > 3) {
        throw new Error(`console session lost repeatedly (last at « ${label} »)`);
      }
      log(`console fell back to login at « ${label} » — re-login and retry…`);
      await reloginAny(session, logins, log, signal);
      await enterConfig();
      continue;
    }
    if (reply.kind === "confirm") {
      session.mark();
      await session.sendRaw("\r");
      log(`${label}: confirmed`);
    } else if (reply.kind === "replaceKeys") {
      // "% You already have RSA keys defined … Do you really want to replace
      // them? [yes/no]:" — a re-run over a switch that got halfway. Answering
      // no leaves a key whose pair nobody here holds; yes is the only answer
      // that ends with a device meshcraft can open.
      log(`${label}: RSA keys already present — replacing (yes)`);
      session.mark();
      await session.send("yes");
      // Generation itself takes tens of seconds and answers nothing meanwhile.
      await expectReply(session, c.timeoutMs ?? 180000);
    } else if (reply.kind === "error") {
      log(
        `${label}: CLI error « ${reply.match} » — continuing (${clipTail(session.getSinceMark(), 160)})`,
      );
    } else if (reply.kind === "prompt") {
      log(`${label}: ok`);
    } else {
      log(`${label}: no prompt reply — continuing`);
    }
    // Let the device finish printing its prompt: matching fires on the FIRST
    // characters, and sending the next command while the rest is still
    // streaming shifts every reply by one.
    await session.sleep(250);
    i++;
  }
  session.mark();
  await session.send("end");
  await session.sleep(500);
}

/**
 * « write memory », answering whatever it asks.
 *
 * A failed save throws: a management configuration that does not survive a
 * reboot is worse than a refused one, because the operator believes it is in
 * place.
 */
export async function saveConfig(
  session: SerialSession,
  log: SoftResetLog,
  logins?: LoginCandidate[],
): Promise<void> {
  log("write memory…");
  session.mark();
  await session.send("write memory");
  var deadline = Date.now() + 90000;
  var relogins = 0;
  var sawOK = false;
  while (Date.now() < deadline) {
    try {
      await session.expectAny(
        [SAVE_OK, CONFIRM, FILENAME_PROMPT, CLI_ERROR, LOGIN_FALLBACK, PROMPT_PRIV],
        20000,
      );
    } catch {
      throw new Error("save: no [OK] and no prompt after « write memory »");
    }
    // expectAny reports the RIGHTMOST pattern, and the prompt is always last
    // on the line — "[OK]\r\nswitch#" would be read as "prompt, no [OK]". So
    // the burst is examined as a whole rather than through the winning match.
    var seen = session.getSinceMark();
    var refusal = seen.match(CLI_ERROR);
    if (refusal) {
      throw new Error(`save failed: « ${refusal[0].trim()} »`);
    }
    if (SAVE_OK.test(seen)) {
      sawOK = true;
    }
    if (LOGIN_FALLBACK.test(seen) && !sawOK) {
      relogins++;
      if (!logins || relogins > 2) {
        throw new Error("save: console session lost before the NVRAM write");
      }
      log("save: console fell back to login — re-login and retry…");
      await reloginAny(session, logins, log);
      session.mark();
      await session.send("write memory");
      continue;
    }
    if (sawOK) {
      log("save: written to NVRAM");
      return;
    }
    if (CONFIRM.test(seen) || FILENAME_PROMPT.test(seen)) {
      // "Overwrite the previous NVRAM configuration?[confirm]" after an IOS
      // upgrade, or the destination file name. Both take their default.
      session.mark();
      await session.sendRaw("\r");
      continue;
    }
    throw new Error("save: returned to the prompt without an [OK] marker");
  }
  throw new Error("save: timed out waiting for the NVRAM write result");
}

function parseVlanId(iface: string): number | null {
  var m = iface.match(/^vlan\s*(\d+)$/i);
  if (!m) return null;
  var n = Number(m[1]);
  if (!Number.isFinite(n) || n < 1 || n > 4094) return null;
  return n;
}

/**
 * Make the switch accept third-party optics, then bring back any cage it had
 * already shut for holding one.
 *
 * TWO STEPS AND BOTH ARE NEEDED. Disabling the detection does not clear a port
 * that is ALREADY err-disabled — it only stops the next one from being shut —
 * so a cage refused at boot stays dark until it is bounced. Running only the
 * global commands would look like it worked and change nothing.
 *
 * Runs BEFORE uplink discovery, because a refused cage is indistinguishable
 * from an empty one and would otherwise make the whole run fail as "no uplink".
 */
export async function acceptThirdPartyOptics(
  session: SerialSession,
  log: SoftResetLog,
  logins: LoginCandidate[],
  signal?: AbortSignal,
): Promise<void> {
  log("accepting third-party optics (service unsupported-transceiver)…");
  await runConfigCmds(
    session,
    log,
    logins,
    UNSUPPORTED_TRANSCEIVER_CMDS.map((cmd) => ({ cmd })),
    signal,
  );

  var status = await collectShow(session, "show interfaces status err-disabled", 10000);
  var shut = parseErrDisabled(status);
  if (shut.length === 0) return;

  var gbic = shut.filter((p) => p.reason.includes("gbic"));
  var other = shut.filter((p) => !p.reason.includes("gbic"));
  if (other.length) {
    // Not ours to clear: bpduguard is a loop someone just created,
    // port-security is a device that moved. Bouncing those hides a real event.
    log(
      `ports shut by the switch for another reason (left alone): ${other
        .map((p) => `${p.port} (${p.reason})`)
        .join(", ")}`,
    );
  }
  if (gbic.length === 0) return;

  log(
    `${gbic.length} cage(s) were shut for holding a non-Cisco optic — bouncing them: ${gbic
      .map((p) => p.port)
      .join(", ")}`,
  );
  var cmds: { cmd: string; label?: string }[] = [];
  var i = 0;
  for (i = 0; i < gbic.length; i++) {
    cmds.push({ cmd: `interface ${gbic[i].port}` });
    cmds.push({ cmd: "shutdown", label: `${gbic[i].port}: shutdown` });
    cmds.push({ cmd: "no shutdown", label: `${gbic[i].port}: no shutdown` });
    cmds.push({ cmd: "exit", label: `exit interface ${gbic[i].port}` });
  }
  await runConfigCmds(session, log, logins, cmds, signal);
  // The optic has to train after the bounce; discovery retries anyway, this
  // just spends the first retry usefully.
  await session.sleep(4000);
}

/**
 * Find ports with a link, RETRYING while the switch finishes bringing them up.
 * Without an uplink carrying the management VLAN, the probe that follows this
 * push cannot reach the new address and MUST fail rather than report success.
 *
 * When nothing comes up, the LAST thing seen decides what the operator is
 * told: a cage the switch refused reads exactly like an empty one, and
 * "no port connected" would send someone to check a cable that is fine.
 */
async function discoverUpPorts(
  session: SerialSession,
  log: SoftResetLog,
  budgetMs: number,
  signal?: AbortSignal,
): Promise<string[]> {
  var deadline = Date.now() + budgetMs;
  var attempt = 0;
  for (;;) {
    aborted(signal);
    attempt++;
    var status = await collectShow(session, "show interfaces status", 10000);
    var up = parseUpPorts(status);
    if (up.length > 0) return up;
    if (Date.now() >= deadline) {
      await explainNoUplink(session, log);
      return [];
    }
    log(`no port connected yet (check ${attempt}) — links may still be coming up, retrying…`);
    await session.sleep(5000);
  }
}

/** Say WHY no port came up, rather than only that none did. */
async function explainNoUplink(session: SerialSession, log: SoftResetLog): Promise<void> {
  if (UNSUPPORTED_TRANSCEIVER_LOG.test(session.getCapture())) {
    log(
      "the console reported an UNSUPPORTED TRANSCEIVER: this switch refused an optic it " +
        "does not read as Cisco-coded. « service unsupported-transceiver » was applied and " +
        "the cage bounced — if the port is still down, the optic may also be failing to " +
        "train, or the far end is down",
    );
  }
  var status = await collectShow(session, "show interfaces status err-disabled", 10000);
  var shut = parseErrDisabled(status);
  if (shut.length) {
    log(
      `ports shut BY THE SWITCH (not empty ports): ${shut
        .map((p) => `${p.port} (${p.reason})`)
        .join(", ")}`,
    );
  }
}

async function pushManagement(
  session: SerialSession,
  opts: {
    iface: string;
    ip: string;
    mask: string;
    gateway?: string;
    domainName: string;
    targetUser: string;
    newPassword: string;
    hostname?: string;
    log: SoftResetLog;
    logins: LoginCandidate[];
    signal?: AbortSignal;
  },
): Promise<void> {
  var log = opts.log;
  var iface = opts.iface.trim() || "vlan 1";
  var vlanId = parseVlanId(iface) ?? 1;
  var targetUser = (opts.targetUser || "admin").trim() || "admin";
  var mask = toDottedMask(opts.mask);
  var vlanName = `LAN${vlanId}_Management`.replace(/[^A-Za-z0-9_]/g, "_").slice(0, 32);

  // Kill the pager for this session; universal on IOS.
  session.mark();
  await session.send("terminal length 0");
  await expectReply(session, 5000);

  // Before ANY link is looked for: a refused optic leaves its cage looking
  // empty, so discovery would find nothing and the run would fail blaming the
  // network. The factory reset just removed these lines, so they are set again.
  await acceptThirdPartyOptics(session, log, opts.logins, opts.signal);

  var upPorts: string[] = [];
  if (vlanId !== 1) {
    log("show interfaces status (finding the uplink; links may still be coming up)…");
    upPorts = await discoverUpPorts(session, log, 75000, opts.signal);
    if (upPorts.length) {
      log(`port(s) with a link: ${upPorts.join(", ")}`);
    } else {
      log(
        "no port connected after waiting — the management VLAN will be carried on nothing and the probe will fail unless a trunk already exists",
      );
    }
  }

  var cmds: { cmd: string; label?: string; timeoutMs?: number }[] = [];

  if (opts.hostname?.trim()) {
    cmds.push({ cmd: `hostname ${opts.hostname.trim().replace(/[^A-Za-z0-9_-]/g, "-")}` });
  }

  // 1. The VLAN before the SVI, or the SVI never comes up.
  if (vlanId !== 1) {
    cmds.push({ cmd: `vlan ${vlanId}` });
    cmds.push({ cmd: `name ${vlanName}` });
    cmds.push({ cmd: "exit", label: `exit vlan ${vlanId}` });
  }
  cmds.push({ cmd: `interface vlan ${vlanId}` });
  cmds.push({ cmd: `ip address ${opts.ip} ${mask}` });
  // An SVI created in configuration mode comes up administratively down.
  cmds.push({ cmd: "no shutdown", label: `no shutdown (interface vlan ${vlanId})` });
  cmds.push({ cmd: "exit", label: `exit interface vlan ${vlanId}` });

  if (opts.gateway?.trim()) {
    // An L2 Catalyst has no routing table: « ip route » would be refused.
    cmds.push({ cmd: `ip default-gateway ${opts.gateway.trim()}` });
  }

  // 2. The domain name before the key, the key before SSH is worth enabling.
  cmds.push({ cmd: `ip domain-name ${opts.domainName}` });
  cmds.push({
    cmd: "crypto key generate rsa modulus 2048",
    label: "crypto key generate rsa modulus 2048 (tens of seconds)",
    timeoutMs: 180000,
  });
  cmds.push({ cmd: "ip ssh version 2" });
  cmds.push({ cmd: "line vty 0 15" });
  cmds.push({ cmd: "login local", label: "login local (make the device consult local accounts)" });
  cmds.push({ cmd: "transport input ssh" });
  cmds.push({ cmd: "exit", label: "exit line vty" });
  // LLDP globally: IOS ships with it OFF and speaks only CDP, so a Catalyst is
  // invisible to every neighbour in the fleet and serves nothing under LLDP-MIB.
  // The cost is a topology hole exactly where the switch is — on 2026-08-02 a
  // freshly provisioned uplink could not be located at all, and the inference
  // made in its absence was wrong. Mirrors MgmtPushCmds in internal/driver/cisco/ios.
  cmds.push({ cmd: "lldp run", label: "lldp run (make this switch visible to LLDP)" });

  // 3. The management VLAN on the live uplink(s).
  if (vlanId !== 1) {
    var ti = 0;
    for (ti = 0; ti < upPorts.length; ti++) {
      var tp = upPorts[ti];
      cmds.push({ cmd: `interface ${tp}` });
      cmds.push({ cmd: "switchport mode trunk", label: `${tp}: switchport mode trunk` });
      cmds.push({
        cmd: `switchport trunk allowed vlan add ${vlanId}`,
        label: `${tp}: trunk allowed vlan add ${vlanId}`,
      });
      cmds.push({ cmd: "exit", label: `exit interface ${tp}` });
    }
  }

  // 4. CREDENTIALS LAST. If changing the logged-in account's secret drops the
  //    console, everything else is already in place and runConfigCmds re-logins
  //    with the NEW secret to finish.
  if (opts.newPassword) {
    cmds.push({
      cmd: `username ${targetUser} privilege 15 secret 0 ${opts.newPassword}`,
      label: `username ${targetUser} privilege 15 secret 0 •••`,
    });
    cmds.push({
      cmd: `enable secret 0 ${opts.newPassword}`,
      label: "enable secret 0 •••",
    });
  }

  log("pushing management configuration (each command verified)…");
  await runConfigCmds(session, log, opts.logins, cmds, opts.signal);

  await saveConfig(session, log, opts.logins);
  log("management configuration pushed and saved");
}

/** Run show commands on the console and log a short snippet (sanity before the IP probe). */
async function verifyOnSerial(
  session: SerialSession,
  opts: {
    ip: string;
    iface: string;
    gateway?: string;
    log: SoftResetLog;
    logins: LoginCandidate[];
    signal?: AbortSignal;
  },
): Promise<void> {
  var log = opts.log;

  session.mark();
  await session.send("");
  var state = await expectReplyOrLogin(session, 5000);
  if (state.kind === "login") {
    log("serial verify: console at login — re-opening the session…");
    await reloginAny(session, opts.logins, log, opts.signal);
  }

  log("serial verify: show ip interface brief…");
  var brief = await collectShow(session, "show ip interface brief", 12000);
  if (opts.ip && brief.includes(opts.ip)) {
    log(`serial verify: management IP ${opts.ip} present and assigned`);
  } else {
    log(`serial verify: show ip interface brief (tail):\n${brief.trim().slice(-600)}`);
  }

  log("serial verify: show ip ssh…");
  var ssh = await collectShow(session, "show ip ssh", 8000);
  if (/SSH Enabled/i.test(ssh)) {
    log("serial verify: SSH enabled");
  } else {
    log(
      `serial verify: SSH does NOT report as enabled — the switch may be running a non-crypto image:\n${ssh
        .trim()
        .slice(-300)}`,
    );
  }

  if (opts.gateway?.trim()) {
    log(`serial verify: ping ${opts.gateway.trim()}…`);
    var ping = await collectShow(session, `ping ${opts.gateway.trim()}`, 15000);
    if (/Success rate is \s*(?!0)/i.test(ping)) {
      log("serial verify: gateway ping ok");
    } else {
      log(`serial verify: ping output:\n${ping.trim().slice(-400)}`);
    }
  }
}

/**
 * Full soft reset, until the management CLI is configured and verified on the
 * console. Network reachability is probed by the caller afterwards.
 */
export async function runCiscoIosSoftReset(input: CiscoIosSoftResetInput): Promise<void> {
  var session = input.session;
  var log = input.log;
  var iface = (input.mgmtIface || "vlan 1").trim() || "vlan 1";
  var domainName = (input.domainName || "meshcraft.local").trim() || "meshcraft.local";

  if (!input.mgmtIp.trim() || !input.mgmtMask.trim()) {
    throw new Error("management IP and mask are required");
  }
  if (!input.targetPassword) {
    throw new Error("instance credential secret is required");
  }
  // The management interface field is free text and its FS-shaped values
  // ("Craft", "management") mean nothing here. Falling back to VLAN 1 would
  // configure a different interface from the one the operator named, silently,
  // and the probe would then fail for a reason nothing on screen explains.
  if (parseVlanId(iface) === null) {
    throw new Error(
      `management interface « ${iface} » is not a VLAN: on a Catalyst the address ` +
        `goes on an SVI, so this must read « vlan N » (there is no Craft port and no ` +
        `« management ip address » command on this platform)`,
    );
  }

  var user = input.currentUser || "";
  var pass = input.currentPassword ?? "";
  var enablePass = input.enablePassword ?? "";
  var targetUser = (input.targetUser || "admin").trim() || "admin";

  // The device may hold ANY of these: what the operator typed, the instance
  // credential (an earlier run pushed it without saving), or nothing at all —
  // which is what a factory Catalyst holds.
  var initialLogins: LoginCandidate[] = [
    { user, pass, enable: enablePass, label: `current serial credentials (${user || "none"})` },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "", pass: "", enable: "", label: "factory console (no credential)" },
    { user: "cisco", pass: "cisco", enable: "cisco", label: "cisco / cisco" },
  ];
  var seen: Record<string, boolean> = {};
  initialLogins = initialLogins.filter((l) => {
    var key = `${l.user} ${l.pass}`;
    if (seen[key]) return false;
    seen[key] = true;
    return true;
  });

  log("logging in on the console…");
  await reloginAny(session, initialLogins, log, input.signal);

  session.mark();
  await session.send("terminal length 0");
  await expectReply(session, 5000);

  await factoryReset(session, log, input.signal);

  await waitLoginAfterReboot(session, log, 360000, input.signal);

  // Fresh buffer for the factory console — never inherit a pre-reboot « # ».
  session.clearCapture();

  log("factory console login (a reset Catalyst asks for nothing)…");
  var factoryLogins: LoginCandidate[] = [
    { user: "", pass: "", enable: "", label: "factory console (no credential)" },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user, pass, enable: enablePass, label: `credentials entered in the wizard (${user || "none"})` },
  ];
  var fi = 0;
  var ok = false;
  for (fi = 0; fi < factoryLogins.length; fi++) {
    var fl = factoryLogins[fi];
    try {
      log(`trying ${fl.label}…`);
      await ensurePrivileged(session, fl.user, fl.pass, fl.enable, log, 60000, input.signal);
      ok = true;
      break;
    } catch (e) {
      log(`${fl.label} failed (${(e as Error).message || e})`);
    }
  }
  if (!ok) {
    throw new Error("could not reach the privileged prompt after the factory reset");
  }

  // If the console drops mid-push, the instance credential is the one most
  // likely in force (the drop happens right at the secret change).
  var pushLogins: LoginCandidate[] = [
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "", pass: "", enable: "", label: "factory console (no credential)" },
  ];

  await pushManagement(session, {
    iface,
    ip: input.mgmtIp.trim(),
    mask: input.mgmtMask.trim(),
    gateway: input.gateway,
    domainName,
    targetUser,
    newPassword: input.targetPassword,
    hostname: input.hostname,
    log,
    logins: pushLogins,
    signal: input.signal,
  });

  await verifyOnSerial(session, {
    ip: input.mgmtIp.trim(),
    iface,
    gateway: input.gateway,
    log,
    logins: pushLogins,
    signal: input.signal,
  });

  log(
    `serial side done — login is now ${targetUser} / <instance credential secret> (enable the same); caller will probe the management IP`,
  );
}

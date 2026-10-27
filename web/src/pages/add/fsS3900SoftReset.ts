/**
 * FS.com S3900 / S3900-R soft-reset + management push over Web Serial.
 *
 * Soft reset (S3900 Series Reset and Recovery Guide):
 * https://img-en.fs.com/file/user_manual/s3900-series-switches-reset-and-recovery-system-configuration-guide.pdf
 *
 *   conf t
 *   boot system config:Factory_Default_Config.cfg
 *   exit
 *   delete file unit 1 name startup1.cfg
 *   reboot | restart (OpCode ~1.7) | reload → y
 *
 * Then factory login admin with empty password (OpCode 1.7) or admin/admin,
 * push mgmt + user, verify on serial, caller probes IP.
 * Console: 115200 8N1; enable may be empty or "super".
 *
 * Push CLI (same over SSH) — lab OpCode 1.7.0:
 *   vlan database → vlan N name X media ethernet
 *   interface vlan N → ip add A.B.C.D/nn
 *   ip route 0.0.0.0 0.0.0.0 <gw>
 *   ip ssh server enable ; username U privilege 15 ; username U password 0 S
 *   trunk uplink: switchport trunk allowed vlan add N
 *   copy running-config startup-config
 *
 * After push, factory admin is REPLACED by the instance discovery credential.
 */

import type { SerialSession } from "./serialExpect";
import { hostnameFromDeviceName } from "./hostname.ts";

export type SoftResetLog = (msg: string) => void;

export type FsS3900SoftResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  /** Credentials that work BEFORE reset (factory default admin/admin). */
  currentUser: string;
  currentPassword: string;
  /** Enable (privileged) password before reset — factory default "super". */
  enablePassword: string;
  /** Admin account to create/update after reset (from instance credentials). */
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  mgmtMask: string;
  /** Default gateway — pushed as "ip route 0.0.0.0 0.0.0.0 <gw>" when set. */
  gateway?: string;
  /**
   * Where to place the management address:
   * - "vlan 1" (default) — in-band VLAN interface
   * - "Craft" — dedicated MGT port (interface Craft)
   * - "management" — global "management ip address …"
   */
  mgmtIface?: string;
  signal?: AbortSignal;
};

const USERNAME = /user\s*name\s*:/i;
const PASSWORD = /password\s*:/i;
// Lab OpCode 1.7.0: console shows "---- Press ENTER to start CLI session ----"
// after logout and after boot — a CR is needed before Username: appears.
const LOGIN_BANNER =
  /user\s+access\s+verification|press\s+(any\s+)?key|press\s+enter|login\s*:/i;
const BOOT_MARK = /U-Boot|Hit\s+CTRL|autoboot|Booting|Starting\s+system/i;
const PROMPT_USER = />\s*$/m;
const PROMPT_PRIV = /#\s*$/m;
const CONFIRM_Y =
  /continue\s*<\s*y\s*\/\s*n\s*>\s*\?|continue\s*\?|\(y\s*\/\s*n\)|y\s*\/\s*n|yes\s*\/\s*no/i;
/** CLI rejection / soft failure — log and continue, never hang on these. */
const CLI_ERROR =
  /unknown\s+command|invalid\s+(input|command)|unrecognized\s+command|ambiguous\s+command|incomplete\s+command|no\s+such\s+file|not\s+found|does\s+not\s+exist|%Error|% Invalid|command\s+failed|failed\s+to/i;

/**
 * Startup file name answered to the save prompt. ALWAYS explicit: lab OpCode
 * 1.7.0 prompts "Startup configuration file name:" after
 * "copy running-config startup-config", and accepting a default could write
 * over Factory_Default_Config.cfg — the file the soft reset boots from.
 */
export const STARTUP_CONFIG_FILE = "startup1.cfg";

const SAVE_FILENAME_PROMPT = /startup\s+configuration\s+file\s+name\s*:?/i;
/** Lab: "Flash programming started. / Flash programming completed. / Success." */
const SAVE_OK = /flash\s+programming\s+completed|\bsuccess\b|\[OK\]/i;
/** Output pager. Web Serial cannot announce a tall terminal, so long show
 * output may stop at "--More--" even when a PuTTY session shows none. */
const PAGER = /-+\s*more\s*-+\s*$/i;
/** Any sign the console fell back to login mid-script (deploy log 2026-08-01:
 * the session was lost during the push and "show run" was typed into
 * Username:). */
const LOGIN_FALLBACK =
  /user\s+access\s+verification|press\s+enter|user\s*name\s*:\s*$|password\s*:\s*$/i;

type AuthPrompt = "username" | "password" | "priv" | "exec" | "none";
type ReplyKind = "confirm" | "error" | "prompt" | "timeout";

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("soft reset aborted");
}

/**
 * Decide which prompt the console is waiting on by the LAST marker in the
 * recent capture. First-match expect is unsafe: a stale "Password:" earlier
 * in the buffer makes us type the password into a fresh "Username:".
 */
function lastAuthPrompt(text: string): AuthPrompt {
  var tail = text.slice(-800);
  var bestKind: AuthPrompt = "none";
  var bestPos = -1;
  var consider = (re: RegExp, kind: AuthPrompt) => {
    var r = new RegExp(re.source, "gi");
    var m: RegExpExecArray | null;
    while ((m = r.exec(tail)) !== null) {
      if (m.index >= bestPos) {
        bestPos = m.index;
        bestKind = kind;
      }
    }
  };
  consider(/user\s*name\s*:/, "username");
  consider(/password\s*:/, "password");
  consider(/[^\r\n]#\s*$/m, "priv");
  consider(/[^\r\n]>\s*$/m, "exec");
  if (/#\s*$/.test(tail) && tail.lastIndexOf("#") >= bestPos) {
    bestKind = "priv";
  } else if (/>\s*$/.test(tail) && tail.lastIndexOf(">") >= bestPos) {
    bestKind = "exec";
  }
  return bestKind;
}

async function settle(session: SerialSession, ms: number) {
  await session.sleep(ms);
}

/**
 * After a command: confirm prompt, CLI error, or back at #/> .
 * Never waits blindly for y/n alone — errors must unblock the script.
 */
async function expectReply(
  session: SerialSession,
  timeoutMs: number,
): Promise<{ kind: ReplyKind; match: string }> {
  try {
    var hit = await session.expectAny(
      [CONFIRM_Y, CLI_ERROR, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "error", match: hit.match };
    // Prompt often arrives after an error line in the same burst — prefer the error.
    var err = session.getCapture().slice(-500).match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
  }
}

function clipTail(text: string, n: number): string {
  return text.slice(-n).replace(/\s+/g, " ").trim();
}

/** Send a line, then if a CLI error appears quickly, log it and continue. */
async function sendContinueOnError(
  session: SerialSession,
  log: SoftResetLog,
  cmd: string,
  label: string,
  waitMs: number,
): Promise<ReplyKind> {
  session.mark();
  await session.send(cmd);
  var reply = await expectReply(session, waitMs);
  if (reply.kind === "error") {
    log(`${label}: CLI error "${reply.match}" — continuing`);
  } else if (reply.kind === "confirm") {
    session.mark();
    await session.send("y");
    log(`${label}: confirmed y`);
  } else if (reply.kind === "prompt") {
    log(`${label}: ok`);
  } else {
    log(`${label}: sent`);
  }
  return reply.kind;
}

type LoginCandidate = { user: string; pass: string; enable: string; label: string };

/** Reply classification that ALSO recognizes a fallback to login: the
 * 2026-08-01 deploy log shows the console session dropping mid-push (verify
 * typed "show run" into Username:). Blind sleeps cannot see that. */
async function expectReplyOrLogin(
  session: SerialSession,
  timeoutMs: number,
): Promise<{ kind: ReplyKind | "login"; match: string }> {
  try {
    var hit = await session.expectAny(
      [CONFIRM_Y, CLI_ERROR, LOGIN_FALLBACK, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "error", match: hit.match };
    if (hit.index === 2) return { kind: "login", match: hit.match };
    var err = session.getCapture().slice(-500).match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
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
 * Collect the output of a show command, feeding the pager when "--More--"
 * appears: Web Serial cannot claim a tall terminal, so output that fits a
 * PuTTY window may still paginate here and swallow every later command.
 */
export async function collectShowOutput(
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
      // Space = next page (the CR appended by send() only adds one line).
      await session.send(" ");
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

/**
 * Find Up/Up Ethernet ports, RETRYING while the switch finishes bringing
 * links up: the 2026-08-01 deploy saw "no Ethernet link Up" ~25 s after
 * boot on a switch whose uplink was cabled — SFP+ links need longer. Without
 * an uplink trunk the management VLAN is unreachable and the probe MUST fail.
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
    var brief = await collectShowOutput(session, "show interfaces brief", 8000);
    var upPorts = parseUpEthPorts(brief);
    if (upPorts.length > 0) return upPorts;
    if (Date.now() >= deadline) return [];
    log(
      `no Ethernet link Up yet (check ${attempt}) — ports may still be coming up after boot, retrying…`,
    );
    await session.sleep(5000);
  }
}

/**
 * Run configure-mode commands one by one, VERIFYING the console after each:
 * y/n confirms are answered, CLI errors logged, and a fallback to login
 * triggers re-login + "conf t" + retry of the same command. Credentials are
 * pushed LAST by the caller so a session dropped by the password change finds
 * everything else already in place.
 */
export async function runConfigCmds(
  session: SerialSession,
  log: SoftResetLog,
  logins: LoginCandidate[],
  cmds: { cmd: string; label?: string }[],
  signal?: AbortSignal,
): Promise<void> {
  var enterConfig = async () => {
    await sendContinueOnError(session, log, "conf t", "conf t", 5000);
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
    var reply = await expectReplyOrLogin(session, 10000);
    if (reply.kind === "login") {
      relogins++;
      if (relogins > 3) {
        throw new Error(`console session lost repeatedly (last at "${label}")`);
      }
      log(`console fell back to login at "${label}" — re-login and retry…`);
      await reloginAny(session, logins, log, signal);
      await enterConfig();
      continue;
    }
    if (reply.kind === "confirm") {
      session.mark();
      await session.send("y");
      log(`${label}: confirmed y`);
    } else if (reply.kind === "error") {
      // The tail says WHICH word the caret rejected — without it the
      // 2026-08-01 "% Invalid" cascade was undiagnosable from the log.
      log(
        `${label}: CLI error "${reply.match}" — continuing (${clipTail(session.getSinceMark(), 160)})`,
      );
    } else if (reply.kind === "prompt") {
      log(`${label}: ok`);
    } else {
      log(`${label}: no prompt reply — continuing`);
    }
    // Let the device finish printing its prompt: matching fires on the FIRST
    // error/prompt characters, and sending the next command while the rest is
    // still streaming shifted replies by one command (2026-08-01 run).
    await session.sleep(250);
    i++;
  }
  session.mark();
  await session.send("end");
  await session.sleep(500);
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

/**
 * Reach privileged # prompt (login + enable).
 *
 * Never send a password "cold" from a stale Password: marker.
 * After Username:, wait only for a fresh Password: — do not treat a
 * "Username: admin" echo as a re-prompt (that skipped the password entirely).
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
  await settle(session, 600);

  var deadline = Date.now() + timeoutMs;
  var idleNudges = 0;
  var attempts = 0;
  while (Date.now() < deadline) {
    aborted(signal);
    // Use post-mark text only — a stale "#" from before reboot must not
    // short-circuit factory login into "already privileged" + push config.
    var prompt = lastAuthPrompt(session.getSinceMark());
    if (prompt === "none") {
      // Fall back to a short recent tail if the mark is empty mid-stream.
      prompt = lastAuthPrompt(session.getCapture().slice(-400));
    }
    log(`console prompt: ${prompt}`);

    if (prompt === "priv") {
      log("already at privileged prompt");
      return;
    }
    if (prompt === "exec") {
      await enterEnable(session, enablePassword, log);
      return;
    }

    if (prompt === "password") {
      log("stale Password: prompt — sending CR to re-sync on Username:");
      session.mark();
      await session.send("");
      await settle(session, 800);
      idleNudges++;
      if (idleNudges > 12) {
        throw new Error("stuck at Password: prompt; could not reach Username:");
      }
      continue;
    }

    if (prompt === "username") {
      attempts++;
      log(
        `login as ${user || "(empty user)"}${password === "" ? " (empty password)" : ""} (attempt ${attempts})`,
      );
      session.mark();
      await session.send(user);
      // Factory builds may skip Password: entirely when there is no password.
      var afterUser = await session.expectAny(
        [PASSWORD, PROMPT_PRIV, PROMPT_USER, LOGIN_BANNER],
        20000,
      );
      if (afterUser.index === 1) {
        log("login ok — privileged (no password prompt)");
        return;
      }
      if (afterUser.index === 2) {
        await enterEnable(session, enablePassword, log);
        return;
      }
      if (afterUser.index === 3) {
        // "---- Press ENTER to start CLI session ----" — wake it and retry.
        log("login banner — sending CR");
        session.mark();
        await session.send("");
        await settle(session, 600);
        idleNudges = 0;
        continue;
      }
      session.mark();
      await session.send(password);
      var afterPass = await session.expectAny(
        [PROMPT_PRIV, PROMPT_USER, USERNAME, PASSWORD, LOGIN_BANNER],
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
      if (afterPass.index === 2) {
        log("login failed — back at Username:");
        idleNudges = 0;
        continue;
      }
      if (afterPass.index === 4) {
        // Failed login on OpCode 1.7.0 re-shows the Press ENTER banner instead
        // of Username: (deploy log 2026-08-01: two 20 s timeouts here).
        log("login refused — banner again, sending CR");
        session.mark();
        await session.send("");
        await settle(session, 600);
        idleNudges = 0;
        continue;
      }
      log("Password: again after login password — re-syncing");
      session.mark();
      await session.send("");
      await settle(session, 500);
      idleNudges = 0;
      continue;
    }

    idleNudges++;
    if (idleNudges > 8) {
      throw new Error("no Username/Password/# prompt seen on serial console");
    }
    session.mark();
    await session.send("");
    await settle(session, 800);
  }
  throw new Error("could not reach FS S3900 privileged prompt");
}

/**
 * Prefer "reboot" (FS reset guide); older OpCode 1.7.x often uses "restart";
 * some builds use "reload".
 *
 * After the verb, wait ONLY for confirm or CLI error — never treat a fleeting
 * "#" as failure and type the next verb into "Continue <y/n>?" (that typed
 * "r…" from reload into the confirm line).
 */
async function rebootOrReload(session: SerialSession, log: SoftResetLog) {
  var verbs = ["reboot", "restart", "reload"];
  var vi = 0;
  for (vi = 0; vi < verbs.length; vi++) {
    var verb = verbs[vi];
    var isLast = vi === verbs.length - 1;
    log(`${verb}…`);
    session.mark();
    await session.send(verb);
    // Let the device print "System will be restarted. Continue <y/n>?".
    await session.sleep(1000);
    try {
      var hit = await session.expectAny([CONFIRM_Y, CLI_ERROR], 20000);
      if (hit.index === 0) {
        await session.sleep(500);
        session.mark();
        await session.send("y");
        log(`${verb}: confirmed y`);
        return;
      }
      // Consume the error so the next verb does not re-match a stale line.
      session.mark();
      log(
        `${verb}: CLI error "${hit.match}"${isLast ? " — continuing (manual reboot OK)" : ` — trying ${verbs[vi + 1]}…`}`,
      );
      if (!isLast) continue;
      return;
    } catch {
      if (CONFIRM_Y.test(session.getCapture().slice(-400))) {
        session.mark();
        await session.send("y");
        log(`${verb}: confirmed y (prompt already on screen)`);
        return;
      }
      session.mark();
      log(
        `${verb}: no confirm/error${isLast ? " — continuing (waiting for Username / manual reboot)" : ` — trying ${verbs[vi + 1]}…`}`,
      );
      if (!isLast) continue;
      return;
    }
  }
}

/**
 * Soft factory reset per FS S3900 Reset and Recovery guide §3.2.5
 * (not Cisco "delete startup-config").
 * CLI errors on individual steps are logged; the script continues.
 */
async function factoryReset(session: SerialSession, log: SoftResetLog, signal?: AbortSignal) {
  aborted(signal);
  log("FS reset: boot Factory_Default_Config.cfg + delete startup1.cfg…");

  await sendContinueOnError(session, log, "conf t", "conf t", 3000);
  await session.sleep(300);
  await sendContinueOnError(
    session,
    log,
    "boot system config:Factory_Default_Config.cfg",
    "boot Factory_Default_Config.cfg",
    5000,
  );
  await session.sleep(300);
  await sendContinueOnError(session, log, "exit", "exit", 3000);
  await session.sleep(300);

  await sendContinueOnError(
    session,
    log,
    "delete file unit 1 name startup1.cfg",
    "delete startup1.cfg",
    8000,
  );
  // Advance mark past delete output ("No such file", etc.) before reboot verbs.
  session.mark();
  await session.sleep(400);

  await rebootOrReload(session, log);
}

async function waitLoginAfterReboot(
  session: SerialSession,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  // Drop ALL pre-reboot text. A stale "Username:" from autodetect made us
  // declare "post-reset login prompt seen" in the same millisecond as
  // "restart: confirmed y" and push config while U-Boot was still running.
  session.mark();
  log("reboot confirmed — waiting for device reboot (minimum 50s)…");
  var minBootMs = 50000;
  var deadline = Date.now() + timeoutMs;
  var bootWaitUntil = Date.now() + minBootMs;
  var lastProgress = Date.now();
  var sawBoot = false;

  while (Date.now() < bootWaitUntil && Date.now() < deadline) {
    aborted(signal);
    try {
      await session.expect(BOOT_MARK, Math.min(5000, bootWaitUntil - Date.now()));
      sawBoot = true;
      log("boot loader output seen — still waiting for login…");
      break;
    } catch {
      /* quiet while power-cycling */
    }
    if (Date.now() - lastProgress > 10000) {
      log(
        `reboot in progress (${Math.round((bootWaitUntil - Date.now()) / 1000)}s before looking for Username)…`,
      );
      lastProgress = Date.now();
    }
  }

  // Anything received during the dying pre-reboot session is useless.
  session.mark();
  log("looking for post-reset Username: (new data only)…");
  var lastNudge = 0;
  lastProgress = Date.now();

  while (Date.now() < deadline) {
    aborted(signal);
    var fresh = session.getSinceMark();
    // Only trust prompts that arrived AFTER the post-boot mark.
    if (USERNAME.test(fresh) || PASSWORD.test(fresh) || LOGIN_BANNER.test(fresh)) {
      // Ignore if we only saw boot noise without a real login line yet.
      if (USERNAME.test(fresh) || PASSWORD.test(fresh) || /user\s+access\s+verification/i.test(fresh)) {
        log("post-reset login prompt seen (after reboot)");
        return;
      }
    }

    try {
      var hit = await session.expectAny(
        [USERNAME, PASSWORD, LOGIN_BANNER, BOOT_MARK],
        Math.min(10000, deadline - Date.now()),
      );
      if (hit.index === 3) {
        sawBoot = true;
        log("boot output — continuing to wait for Username:");
        session.mark();
        continue;
      }
      log("post-reset login prompt seen (after reboot)");
      return;
    } catch {
      /* still booting */
    }

    var now = Date.now();
    if (now - lastProgress > 15000) {
      log(
        `still waiting for Username: (${Math.round((deadline - now) / 1000)}s left)${sawBoot ? " [boot seen]" : ""} — ${clipTail(session.getSinceMark(), 80) || "no new serial output"}`,
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
  throw new Error("no Username prompt after reboot (timeout)");
}

function parseVlanId(iface: string): number | null {
  var m = iface.match(/^vlan\s*(\d+)$/i);
  if (!m) return null;
  var n = Number(m[1]);
  if (!Number.isFinite(n) || n < 1 || n > 4094) return null;
  return n;
}

async function pushManagement(
  session: SerialSession,
  opts: {
    iface: string;
    ip: string;
    mask: string;
    gateway?: string;
    newPassword: string;
    enablePassword: string;
    targetUser: string;
    hostname?: string;
    log: SoftResetLog;
    /** Credentials usable to re-open the console if the session drops mid-push. */
    logins: LoginCandidate[];
    signal?: AbortSignal;
  },
): Promise<void> {
  var log = opts.log;
  var iface = opts.iface.trim() || "vlan 1";
  var targetUser = (opts.targetUser || "admin").trim() || "admin";
  var vlanId = parseVlanId(iface);
  var prefix = Number(maskToCidr(opts.mask));
  if (!Number.isFinite(prefix) || prefix < 1 || prefix > 32) prefix = 24;
  var vlanName =
    vlanId !== null ? `LAN${vlanId}_Management` : "Management";

  // Best effort: kill the pager for this session. Unverified on OpCode 1.7.0 —
  // a rejected command only costs an error echo, an active pager costs the
  // whole push (every later keystroke feeds the pager, not the CLI).
  await sendContinueOnError(session, log, "terminal length 0", "terminal length 0", 4000);

  // Discover uplink(s) with link so the mgmt VLAN is reachable for the probe.
  // Wait for links: right after boot SFP+ ports can take tens of seconds.
  var upPorts: string[] = [];
  var trunk = "";
  if (vlanId !== null && vlanId !== 1) {
    log("show interfaces brief (find uplinks; links may still be coming up)…");
    upPorts = await discoverUpPorts(session, log, 75000, opts.signal);
    trunk = compactEthRange(upPorts);
    if (trunk) {
      log(`uplink(s) with link: ${trunk}`);
    } else if (upPorts.length) {
      log(`uplink ports (non-contiguous): ${upPorts.join(", ")}`);
    } else {
      log(
        "no Ethernet link Up after waiting — the mgmt VLAN will not be carried on any port and the probe will fail unless a trunk exists",
      );
    }
  }

  var cmds: { cmd: string; label?: string }[] = [];

  // The system name, through the ONE shared normalizer (./hostname.ts), which
  // mirrors internal/driver/hostname.go — the same function the reconciliation
  // loop uses. What stood here was `.replace(/[^A-Za-z0-9_-]/g, "_")`, and the
  // substitute is the point: `_` is the ONE character driver.ValidHostname
  // forbids outright, so every name this line touched came out in a form the
  // reconciled path can never emit — a permanent disagreement with the healer,
  // on a name that goes out over the LLDP system-name TLV.
  //
  // A REFUSAL IS REPORTED AND THE REST OF THE PUSH CONTINUES. Aborting the run
  // over a system name would leave a factory-reset switch with no management
  // address at all; the honest outcome is a reachable switch still carrying its
  // factory name, with the reason on screen. This mirrors MgmtPushCmds, which
  // returns that reason as its error alongside the commands.
  if (opts.hostname?.trim()) {
    var verdict = hostnameFromDeviceName(opts.hostname);
    if (verdict.hostname) {
      cmds.push({ cmd: `hostname ${verdict.hostname}` });
    } else {
      log(
        `the system name was NOT written — ${verdict.reason}. The switch keeps the ` +
          `name it has; nothing else in this push is affected`,
      );
    }
  }

  if (vlanId !== null && vlanId !== 1) {
    cmds.push({ cmd: "vlan database" });
    cmds.push({ cmd: `vlan ${vlanId} name ${vlanName} media ethernet` });
    cmds.push({ cmd: "exit", label: "exit vlan database" });
  }

  if (/^management$/i.test(iface)) {
    cmds.push({ cmd: `management ip address ${opts.ip}/${prefix}` });
  } else if (/^craft$/i.test(iface)) {
    cmds.push({ cmd: "interface craft" });
    cmds.push({ cmd: `ip add ${opts.ip}/${prefix}` });
    cmds.push({ cmd: "exit", label: "exit interface craft" });
  } else {
    var vid = vlanId ?? 1;
    cmds.push({ cmd: `interface vlan ${vid}` });
    cmds.push({ cmd: `ip add ${opts.ip}/${prefix}` });
    cmds.push({ cmd: "exit", label: `exit interface vlan ${vid}` });
  }

  if (opts.gateway?.trim()) {
    cmds.push({ cmd: `ip route 0.0.0.0 0.0.0.0 ${opts.gateway.trim()}` });
  }

  cmds.push({ cmd: "ip http server" });
  cmds.push({ cmd: "ip ssh server enable" });

  // Carry mgmt VLAN on live uplink(s) so the probe can reach the SVI.
  if (vlanId !== null && vlanId !== 1) {
    var trunkTargets = trunk ? [trunk] : upPorts;
    var ti = 0;
    for (ti = 0; ti < trunkTargets.length; ti++) {
      var tp = trunkTargets[ti];
      cmds.push({ cmd: `interface ${tp}` });
      cmds.push({ cmd: "switchport mode trunk", label: `${tp}: switchport mode trunk` });
      cmds.push({
        cmd: `switchport trunk allowed vlan add ${vlanId}`,
        label: `${tp}: trunk allowed vlan add ${vlanId}`,
      });
      cmds.push({ cmd: "exit", label: `exit interface ${tp}` });
    }
    cmds.push({ cmd: "no interface vlan 1", label: "no interface vlan 1 (drop factory DHCP SVI)" });
  }

  // CREDENTIALS LAST. The 2026-08-01 deploy lost the console session between
  // the credential commands and the save — if changing the logged-in
  // account's password drops the session, everything else is already in
  // place and runConfigCmds re-logins with the NEW secret to finish.
  if (opts.newPassword) {
    cmds.push({
      cmd: `username ${targetUser} privilege 15`,
      label: `username ${targetUser} privilege 15 (replaces factory admin after save)`,
    });
    cmds.push({
      cmd: `username ${targetUser} password 0 ${opts.newPassword}`,
      label: `username ${targetUser} password 0 •••`,
    });
    cmds.push({
      cmd: `enable password 0 ${opts.enablePassword || opts.newPassword}`,
      label: "enable password 0 •••",
    });
  }

  log("pushing management configuration (each command verified)…");
  await runConfigCmds(session, log, opts.logins, cmds, opts.signal);

  await saveStartupConfig(session, log, opts.logins);
  log("management config pushed");
}

/**
 * "copy running-config startup-config" — lab OpCode 1.7.0 prompts for the
 * startup FILE NAME (no y/n): answer it, then wait for "Success.". Waiting
 * for y/n here timed out AND left the CLI at the filename prompt, so the next
 * command ("show run") was typed as a file name and nothing was persisted.
 *
 * A failed save throws: a management config that does not survive a reboot is
 * worse than a refused one (the operator believes it is in place).
 */
export async function saveStartupConfig(
  session: SerialSession,
  log: SoftResetLog,
  logins?: LoginCandidate[],
): Promise<void> {
  log("copy running-config startup-config…");
  session.mark();
  await session.send("copy running-config startup-config");
  var deadline = Date.now() + 90000;
  var filenameSent = false;
  var relogins = 0;
  while (Date.now() < deadline) {
    var hit: { index: number; match: string };
    try {
      hit = await session.expectAny(
        [SAVE_FILENAME_PROMPT, CONFIRM_Y, SAVE_OK, CLI_ERROR, LOGIN_FALLBACK],
        20000,
      );
    } catch {
      throw new Error(
        "save: no filename prompt / confirmation / Success after copy running-config startup-config",
      );
    }
    if (hit.index === 0) {
      if (filenameSent) {
        // Re-prompt after our answer: the device refused the name — do not loop.
        throw new Error(
          `save: file name ${STARTUP_CONFIG_FILE} not accepted (prompted again)`,
        );
      }
      filenameSent = true;
      log(`save: startup file name → ${STARTUP_CONFIG_FILE}`);
      session.mark();
      await session.send(STARTUP_CONFIG_FILE);
      continue;
    }
    if (hit.index === 1) {
      session.mark();
      await session.send("y");
      log("save: confirmed y");
      continue;
    }
    if (hit.index === 2) {
      log("save: flash write ok");
      return;
    }
    if (hit.index === 4) {
      // Console fell back to login right at save time — re-open and retry.
      relogins++;
      if (!logins || relogins > 2) {
        throw new Error("save: console session lost before the flash write");
      }
      log("save: console fell back to login — re-login and retry copy…");
      await reloginAny(session, logins, log);
      filenameSent = false;
      session.mark();
      await session.send("copy running-config startup-config");
      continue;
    }
    throw new Error(`save failed: "${hit.match}"`);
  }
  throw new Error("save: timed out waiting for flash write result");
}

function parseUpEthPorts(brief: string): string[] {
  // Same-line only: Admin Up + Link Up (\\s crosses newlines and false-matched 1/27→1/28).
  var re = /^Eth\s+(\d+)\s*\/\s*(\d+)\s+[^\n]*?\bUp\s+Up\b/gim;
  var out: string[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(brief)) !== null) {
    var key = `Eth ${m[1]}/${m[2]}`;
    if (seen[key]) continue;
    seen[key] = true;
    out.push(key);
  }
  return out;
}

function compactEthRange(ports: string[]): string {
  if (ports.length === 0) return "";
  var unit = -1;
  var nums: number[] = [];
  var i = 0;
  for (i = 0; i < ports.length; i++) {
    var m = /^Eth\s+(\d+)\/(\d+)$/i.exec(ports[i]);
    if (!m) return "";
    var u = Number(m[1]);
    var n = Number(m[2]);
    if (unit < 0) unit = u;
    else if (u !== unit) return "";
    nums.push(n);
  }
  for (i = 1; i < nums.length; i++) {
    if (nums[i] !== nums[i - 1] + 1) return "";
  }
  if (nums.length === 1) return `Eth ${unit}/${nums[0]}`;
  return `Eth ${unit}/${nums[0]}-${nums[nums.length - 1]}`;
}

/** Run show commands on serial and log a short snippet (sanity before IP probe). */
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
  var vlanId = parseVlanId(opts.iface.trim() || "vlan 1");

  // Re-assert the prompt first: verify used to type "show run" into a login
  // prompt when the session had dropped (2026-08-01 deploy log).
  session.mark();
  await session.send("");
  var state = await expectReplyOrLogin(session, 5000);
  if (state.kind === "login") {
    log("serial verify: console at login — re-opening session…");
    await reloginAny(session, opts.logins, log, opts.signal);
  }

  log("serial verify: show run (checking management IP)…");
  var snippet = await collectShowOutput(session, "show run", 12000);
  if (opts.ip && snippet.includes(opts.ip)) {
    log(`serial verify: management IP ${opts.ip} present in running-config`);
  } else {
    log(`serial verify: show run (tail):\n${snippet.trim().slice(-600)}`);
  }

  if (opts.gateway?.trim()) {
    log(`serial verify: ping ip ${opts.gateway.trim()}…`);
    var pingOut = await collectShowOutput(session, `ping ip ${opts.gateway.trim()}`, 12000);
    if (/packets received|Average\s*=/i.test(pingOut)) {
      log("serial verify: gateway ping ok");
    } else {
      log(`serial verify: ping output:\n${pingOut.trim().slice(-400)}`);
    }
  }

  if (vlanId !== null) {
    var vlans = await collectShowOutput(session, "show vlan all", 8000);
    log(`serial verify: show vlan all (tail):\n${vlans.trim().slice(-500)}`);
  }
}

function maskToCidr(mask: string): string {
  if (mask.startsWith("/")) return mask.slice(1);
  var parts = mask.split(".").map((x) => Number(x));
  if (parts.length !== 4 || parts.some((n) => Number.isNaN(n))) return "24";
  var bits = 0;
  var i = 0;
  for (i = 0; i < 4; i++) {
    var b = parts[i];
    var k = 0;
    for (k = 7; k >= 0; k--) {
      if (b & (1 << k)) bits++;
      else return String(bits);
    }
  }
  return String(bits);
}

/**
 * Full soft-reset until management CLI is configured and verified on serial.
 * Network reachability is probed by the caller afterward.
 */
export async function runFsS3900SoftReset(input: FsS3900SoftResetInput): Promise<void> {
  var session = input.session;
  var log = input.log;
  var iface = (input.mgmtIface || "vlan 1").trim() || "vlan 1";

  if (!input.mgmtIp.trim() || !input.mgmtMask.trim()) {
    throw new Error("management IP and mask are required");
  }
  if (!input.targetPassword) {
    throw new Error("instance credential secret is required");
  }

  var user = input.currentUser || "admin";
  var pass = input.currentPassword !== undefined ? input.currentPassword : "admin";
  var enablePass = input.enablePassword !== undefined ? input.enablePassword : "super";
  var targetUser = (input.targetUser || "admin").trim() || "admin";

  // The device may hold ANY of these: the UI-entered credentials, the
  // instance credential (an earlier run pushed it without saving — running
  // config until power-cycle), or the factory account. Try them all instead
  // of failing on the first (2026-08-01: a re-run after a half-finished push
  // would have had to guess).
  var initialLogins: LoginCandidate[] = [
    { user, pass, enable: enablePass, label: `current serial credentials (${user})` },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "admin", pass: "admin", enable: "super", label: "admin / admin (enable super)" },
    { user: "admin", pass: "", enable: "", label: "admin / (no password)" },
  ];
  var seenLogin: Record<string, boolean> = {};
  initialLogins = initialLogins.filter((l) => {
    var key = `${l.user}\0${l.pass}`;
    if (seenLogin[key]) return false;
    seenLogin[key] = true;
    return true;
  });

  log("logging in with current serial credentials…");
  await reloginAny(session, initialLogins, log, input.signal);

  await factoryReset(session, log, input.signal);

  await waitLoginAfterReboot(session, log, 300000, input.signal);

  // Fresh buffer for factory login — never inherit pre-reboot "#" / Username:.
  session.clearCapture();

  log("factory login admin/admin (then empty password if needed)…");
  // admin/admin FIRST: observed working on OpCode 1.7.0 after factory reset
  // (2026-08-01 deploy — the empty password was refused twice, 40 s lost).
  var factoryLogins: LoginCandidate[] = [
    { user: "admin", pass: "admin", enable: "super", label: "admin / admin (enable super)" },
    { user: "admin", pass: "", enable: "", label: "admin / (no password)" },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
  ];
  var fi = 0;
  var factoryOk = false;
  for (fi = 0; fi < factoryLogins.length; fi++) {
    var fl = factoryLogins[fi];
    try {
      log(`trying ${fl.label}…`);
      await ensurePrivileged(
        session,
        fl.user,
        fl.pass,
        fl.enable,
        log,
        45000,
        input.signal,
      );
      factoryOk = true;
      break;
    } catch (e) {
      log(`${fl.label} failed (${(e as Error).message || e})`);
    }
  }
  if (!factoryOk) {
    throw new Error("could not login after factory reset (tried empty password, admin/admin, instance cred)");
  }

  // Re-login order if the console drops mid-push: the instance credential
  // FIRST (the drop was observed right after the password change, so the new
  // secret is the one most likely in force), factory admin/admin second.
  var pushLogins: LoginCandidate[] = [
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "admin", pass: "admin", enable: "super", label: "admin / admin (enable super)" },
  ];

  await pushManagement(session, {
    iface,
    ip: input.mgmtIp.trim(),
    mask: input.mgmtMask.trim(),
    gateway: input.gateway,
    targetUser,
    newPassword: input.targetPassword,
    enablePassword: input.targetPassword,
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
    `serial side done — login is now ${targetUser} / <instance credential secret> (enable same); caller will probe management IP`,
  );
}

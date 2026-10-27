/**
 * FS.com S3400 PoE+ series (S3400-48T4SP) soft-reset + management push over
 * Web Serial.
 *
 * STATUS: base forms from the official FS PDFs (S3400-48T4SP CLI reference,
 * configuration preparation, PoE+ series reset & recovery guide), corrected
 * by the first hardware runs (2026-08-03, S3400-48T4SP). CAPTURED since:
 *   - boot after "reboot" takes ~2 MINUTES to a real "Username:"; boot
 *     noise contains lines ending in '#' that read exactly like a privileged
 *     prompt — the first run "logged in" and pushed into a switch that was
 *     still booting, which is why every post-reboot prompt is now validated
 *     with a quiet window + CR round-trip;
 *   - default route: "ip route default GW" ACCEPTED ("ip route 0.0.0.0
 *     0.0.0.0 GW" also accepted; "ip default-gateway" → Unknown command);
 *   - "ip http server" and "aaa authentication login default local" ARE
 *     ACCEPTED. An earlier version of this file said the opposite, on the
 *     strength of this script's own log; an SSH session into the provisioned
 *     switch then found both in its running configuration and "show ip http"
 *     answering "Http server is running". The errors came from the bug fixed
 *     in expectReply/expectReplyOrLogin below, which blamed one command for
 *     the previous one's error;
 *   - "terminal length 0" suppresses NOTHING here — the pager stops every
 *     screen on "--More--" and EATS THE FIRST CHARACTER of the next command
 *     (that is how "show ..." became "how ..." → Unknown command). Reads
 *     feed it a space;
 *   - "| include" takes ONE word: "show interface | include line protocol"
 *     matches nothing ("protocol" is read as a separator), which is why the
 *     first run found no uplink at all and provisioned no trunk;
 *   - "write" answers "OK!";
 *   - "ip sshd enable" can stay silent well past 10 s (key generation) —
 *     it gets a longer per-command budget so replies don't shift by one.
 *
 * This is NOT the S3900 CLI. The S3400 runs a BDCOM-style IOS-like CLI:
 *   - console 115200 8N1; modes: Switch> --enable--> Switch# --config-->
 *     Switch_config# ("config", NOT "conf t");
 *   - soft reset (PoE+ series reset guide):
 *       delete startup-config   → "this file will be erased, are you sure? (y/n)" → y
 *       reboot                  → confirm — but NO to any prompt that offers
 *                                 to SAVE the running config (saving would
 *                                 rewrite the startup-config just deleted);
 *   - factory login after reset is admin / admin (NOT admin / blank);
 *   - interfaces are g0/1 … ("GigaEthernet0/1"); the 4 SFP+ cages are
 *     "TGigaEthernet0/1-4", short form tg0/1-4 — CAPTURED on hardware
 *     (2026-08-03 session), NOT g0/49-52;
 *   - port ranges need the "range" keyword: "int range tg0/1-4" gives
 *     Switch_config_if_range#, while "int tg0/1-4" answers
 *     "Parameter invalid" (captured 2026-08-03); "switchport mode trunk"
 *     accepted in that mode;
 *   - push CLI (reference: internal/driver/fs/s3400 MgmtPushCmds):
 *       vlan N → name X ; interface vlan N → ip address A.B.C.D M.M.M.M
 *       ip route default GW   (captured 2026-08-03)
 *       ip sshd enable ; ip http server ; lldp run
 *       trunk uplink: switchport mode trunk ; switchport trunk vlan-allowed 1-4094
 *       username U password S ; enable password 0 S
 *       write → "OK!"
 *
 * After push, the factory (open) console is guarded by the instance
 * discovery credential.
 */

import type { SerialSession } from "./serialExpect";
import { hostnameFromDeviceName } from "./hostname.ts";

export type SoftResetLog = (msg: string) => void;

export type FsS3400SoftResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  /** Credentials that work BEFORE reset (factory default admin / admin). */
  currentUser: string;
  currentPassword: string;
  /** Enable (privileged) password before reset — none documented at factory. */
  enablePassword: string;
  /** Admin account to create after reset (from instance credentials). */
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  mgmtMask: string;
  /** Default gateway — pushed as ordered route candidates when set. */
  gateway?: string;
  /** Management interface: only "vlan N" exists on this platform. */
  mgmtIface?: string;
  signal?: AbortSignal;
};

const USERNAME = /user\s*name\s*:|login\s*:/i;
const PASSWORD = /password\s*:/i;
const LOGIN_BANNER =
  /user\s+access\s+verification|press\s+(any\s+)?key|press\s+enter|press\s+return/i;
const BOOT_MARK = /U-Boot|BootROM|Hit\s+CTRL|autoboot|Booting|Loading|Starting\s+system|monitor#/i;
const PROMPT_USER = />\s*$/m;
const PROMPT_PRIV = /#\s*$/m;
/** Documented confirm: "this file will be erased, are you sure? (y/n)". */
const CONFIRM_YN =
  /are\s+you\s+sure[^\n]*|\(\s*y\s*\/\s*n\s*\)|y\s*\/\s*n|yes\s*\/\s*no/i;
/** CLI rejection / soft failure — log and continue, never hang on these. */
const CLI_ERROR =
  /unknown\s+command|invalid\s+(input|command|parameter)|unrecognized\s+command|ambiguous\s+command|incomplete\s+command|too\s+many\s+parameters|no\s+such\s+file|not\s+found|does\s+not\s+exist|%\s*error|% Invalid|command\s+failed|failed\s+to/i;
/** "write" result — the exact wording is uncaptured, so several shapes. */
const SAVE_OK =
  /\[OK\]|OK!|\bsaved\b|\bsuccess\b|saving\s+current\s+config[^\n]*(ok|done|complete)|complete[d]?\b/i;
/** Output pager: Web Serial cannot announce a tall terminal — long show
 * output may stop at "--More--" even when a PuTTY session shows none. */
const PAGER = /-+\s*more\s*-+\s*$/i;
/** Any sign the console fell back to login mid-script. */
const LOGIN_FALLBACK =
  /user\s+access\s+verification|press\s+enter|user\s*name\s*:\s*$|login\s*:\s*$|password\s*:\s*$/i;

type AuthPrompt = "username" | "password" | "priv" | "exec" | "none";
type ReplyKind = "confirm" | "error" | "prompt" | "timeout";

/** The four SFP+ cages of the 48T4SP ("TGigaEthernet0/1-4", captured
 * 2026-08-03) — the trunk fallback when uplink discovery sees no link. */
const FALLBACK_UPLINKS = "range tg0/1-4";

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("soft reset aborted");
}

/**
 * Decide which prompt the console is waiting on by the LAST marker in the
 * recent capture — a stale "Password:" earlier in the buffer must not make
 * us type the password into a fresh "Username:".
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
  consider(/user\s*name\s*:|login\s*:/, "username");
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
      [CONFIRM_YN, CLI_ERROR, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "error", match: hit.match };
    // Prefer an error that landed in the same burst as the prompt — but only
    // within the text received SINCE THE MARK, i.e. since THIS command was
    // sent. A 500-char window over the whole capture reaches back into the
    // PREVIOUS command's output and blames this one for its error: that bug
    // made the 2026-08-03 run report "ip http server" and "aaa
    // authentication login default local" as refused when both were accepted
    // and sit in the device's running configuration.
    var err = session.getSinceMark().match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
  }
}

function clipTail(text: string, n: number): string {
  return text.slice(-n).replace(/\s+/g, " ").trim();
}

/**
 * Answer to give a y/n confirm. After "delete startup-config", any prompt
 * that offers to SAVE must be answered NO — saying yes writes the running
 * config right back into the startup-config the reset just deleted, and the
 * "factory reset" silently becomes a plain reboot.
 */
export function confirmAnswer(confirmText: string): "y" | "n" {
  if (/sav/i.test(confirmText)) return "n";
  return "y";
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
    var ans = confirmAnswer(session.getSinceMark().slice(-200));
    session.mark();
    await session.send(ans);
    log(`${label}: confirmed ${ans}`);
  } else if (reply.kind === "prompt") {
    log(`${label}: ok`);
  } else {
    log(`${label}: sent`);
  }
  return reply.kind;
}

type LoginCandidate = { user: string; pass: string; enable: string; label: string };

/** Reply classification that ALSO recognizes a fallback to login (observed on
 * the S3900 mid-push: the password change dropped the console session). */
async function expectReplyOrLogin(
  session: SerialSession,
  timeoutMs: number,
): Promise<{ kind: ReplyKind | "login"; match: string }> {
  try {
    var hit = await session.expectAny(
      [CONFIRM_YN, CLI_ERROR, LOGIN_FALLBACK, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "error", match: hit.match };
    if (hit.index === 2) return { kind: "login", match: hit.match };
    // Prefer an error that landed in the same burst as the prompt — but only
    // within the text received SINCE THE MARK, i.e. since THIS command was
    // sent. A 500-char window over the whole capture reaches back into the
    // PREVIOUS command's output and blames this one for its error: that bug
    // made the 2026-08-03 run report "ip http server" and "aaa
    // authentication login default local" as refused when both were accepted
    // and sit in the device's running configuration.
    var err = session.getSinceMark().match(CLI_ERROR);
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
 * appears (default pause is 24 lines until "terminal length 0" is accepted).
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
 * Find up/up Ethernet ports, RETRYING while the switch finishes bringing
 * links up (on the S3900 the SFP+ links took tens of seconds after boot —
 * assume the same here). Without an uplink carrying the mgmt VLAN, the probe
 * that follows the push MUST fail.
 *
 * There is no "show interface brief" on this platform. "show interface |
 * include protocol" keeps only the header lines ("TGigaEthernet0/3 is up,
 * line protocol is up") — ONE word after "include", because this CLI reads
 * anything past the first as a separator. "include line protocol" matched
 * nothing, which is how the 2026-08-03 run concluded the switch had no link
 * and provisioned no trunk at all.
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
    var show = await collectShowOutput(
      session,
      "show interface | include protocol",
      15000,
    );
    var upPorts = parseUpPorts(show);
    if (upPorts.length > 0) return upPorts;
    if (Date.now() >= deadline) return [];
    log(
      `no Ethernet link up yet (check ${attempt}) — ports may still be coming up after boot, retrying…`,
    );
    await session.sleep(5000);
  }
}

/**
 * Run configure-mode commands one by one, VERIFYING the console after each:
 * y/n confirms are answered (NO whenever the prompt offers to save), CLI
 * errors logged, and a fallback to login triggers re-login + "config" +
 * retry of the same command. Credentials are pushed LAST by the caller so a
 * session dropped by the password change finds everything else in place.
 */
export async function runConfigCmds(
  session: SerialSession,
  log: SoftResetLog,
  logins: LoginCandidate[],
  cmds: { cmd: string; label?: string; waitMs?: number }[],
  signal?: AbortSignal,
): Promise<void> {
  var enterConfig = async () => {
    // "config" (prompt Switch_config#) — this platform has no "conf t".
    await sendContinueOnError(session, log, "config", "config", 5000);
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
    // 2026-08-03 run: a command that outlives its budget shifts every later
    // reply by one — slow commands (sshd key generation) declare their own.
    var reply = await expectReplyOrLogin(session, c.waitMs ?? 12000);
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
      var ans = confirmAnswer(session.getSinceMark().slice(-200));
      session.mark();
      await session.send(ans);
      log(`${label}: confirmed ${ans}`);
    } else if (reply.kind === "error") {
      // The tail says WHICH word the caret rejected — indispensable the day a
      // documented-only form proves wrong on real firmware.
      log(
        `${label}: CLI error "${reply.match}" — continuing (${clipTail(session.getSinceMark(), 160)})`,
      );
    } else if (reply.kind === "prompt") {
      log(`${label}: ok`);
    } else {
      log(`${label}: no prompt reply — continuing`);
    }
    // Let the device finish printing its prompt before the next command.
    await session.sleep(250);
    i++;
  }
  // Leave global config for EXEC. "exit" from Switch_config# returns to
  // Switch# (only an exit from EXEC itself would log out).
  session.mark();
  await session.send("exit");
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
 * Reach the privileged # prompt. The factory login is admin / admin; a
 * console line without authentication lands straight at "Switch>", in which
 * case the username/password of the candidate are simply never typed and
 * "enable" gives # directly.
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
      prompt = lastAuthPrompt(session.getCapture().slice(-400));
    }
    log(`console prompt: ${prompt}`);

    if (prompt === "priv" || prompt === "exec") {
      // CONFIRM with a CR round-trip before trusting it: boot noise ends in
      // '#' too, and the 2026-08-03 run declared "already at privileged
      // prompt" on a switch still booting, then pushed into the void.
      session.mark();
      await session.send("");
      try {
        var confirmHit = await session.expectAny(
          [PROMPT_PRIV, PROMPT_USER, USERNAME, PASSWORD],
          5000,
        );
        if (confirmHit.index === 0) {
          log("privileged prompt confirmed (CR round-trip)");
          return;
        }
        if (confirmHit.index === 1) {
          await enterEnable(session, enablePassword, log);
          return;
        }
        log("prompt was stale — a login prompt answered the CR");
        continue;
      } catch {
        idleNudges++;
        if (idleNudges > 8) {
          throw new Error("prompt-looking text never answered a CR (still booting?)");
        }
        log("prompt did not answer CR — still booting? retrying…");
        continue;
      }
    }

    if (prompt === "password") {
      log("stale Password: prompt — sending CR to re-sync");
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
      // A password refused 3 times will not succeed on attempt 44: the
      // 2026-08-03 run burned ~2 minutes re-trying one wrong candidate.
      if (attempts > 3) {
        throw new Error("credentials refused 3 times — giving up on this candidate");
      }
      log(
        `login as ${user || "(empty user)"}${password === "" ? " (empty password)" : ""} (attempt ${attempts})`,
      );
      session.mark();
      await session.send(user);
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
      throw new Error("no Username/Password/#/> prompt seen on serial console");
    }
    session.mark();
    await session.send("");
    await settle(session, 800);
  }
  throw new Error("could not reach FS S3400 privileged prompt");
}

/**
 * "reboot" with SAVE-AWARE confirms: the sequence follows "delete
 * startup-config", so a prompt offering to save the running config must be
 * answered NO (yes would rewrite the file just deleted) while a plain
 * "are you sure" gets a YES. Up to three prompt rounds are answered — the
 * exact prompt chain of this firmware is uncaptured.
 */
export async function rebootAnswering(
  session: SerialSession,
  log: SoftResetLog,
): Promise<void> {
  log("reboot…");
  session.mark();
  await session.send("reboot");
  await session.sleep(800);
  var rounds = 0;
  while (rounds < 3) {
    var hit: { index: number; match: string };
    try {
      // First prompt gets the full budget; follow-up prompts of the same
      // chain arrive within a breath or not at all.
      hit = await session.expectAny([CONFIRM_YN, CLI_ERROR], rounds === 0 ? 20000 : 4000);
    } catch {
      if (rounds === 0) {
        log("reboot: no confirm prompt — assuming the device is going down");
      }
      return;
    }
    if (hit.index === 1) {
      // Consume the error so a stale line does not re-match.
      session.mark();
      log(`reboot: CLI error "${hit.match}" — continuing (manual reboot OK)`);
      return;
    }
    var ans = confirmAnswer(session.getSinceMark().slice(-200));
    session.mark();
    await session.send(ans);
    log(`reboot: confirmed ${ans}`);
    rounds++;
    await session.sleep(800);
  }
}

/**
 * Soft factory reset per the FS PoE+ series reset guide:
 * "delete startup-config" → y → "reboot". A "No such file" on the
 * delete is fine (already factory) — the script continues to the reboot.
 */
async function factoryReset(session: SerialSession, log: SoftResetLog, signal?: AbortSignal) {
  aborted(signal);
  log("FS PoE+ reset: delete startup-config + reboot…");

  await sendContinueOnError(
    session,
    log,
    "delete startup-config",
    "delete startup-config",
    10000,
  );
  // Advance mark past the delete output before the reboot confirms.
  session.mark();
  await session.sleep(400);

  await rebootAnswering(session, log);
}

/**
 * Wait through the reboot for a fresh console. The post-boot console shows a
 * login (factory admin / admin) or, on a line without authentication, a bare
 * "Switch>" — so bare prompts on POST-BOOT data are accepted too.
 */
async function waitConsoleAfterReboot(
  session: SerialSession,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  // Drop ALL pre-reboot text: a stale prompt must not declare the boot done
  // while the bootloader is still running (S3900 run, 2026-08-01).
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
      log("boot output seen — still waiting for a console prompt…");
      break;
    } catch {
      /* quiet while power-cycling */
    }
    if (Date.now() - lastProgress > 10000) {
      log(
        `reboot in progress (${Math.round((bootWaitUntil - Date.now()) / 1000)}s before looking for a prompt)…`,
      );
      lastProgress = Date.now();
    }
  }

  // Anything received during the dying pre-reboot session is useless.
  session.mark();
  log("looking for a post-reset console prompt (new data only)…");
  var lastNudge = 0;
  lastProgress = Date.now();

  while (Date.now() < deadline) {
    aborted(signal);
    try {
      var hit = await session.expectAny(
        [USERNAME, PASSWORD, LOGIN_BANNER, PROMPT_USER, PROMPT_PRIV, BOOT_MARK],
        Math.min(10000, deadline - Date.now()),
      );
      if (hit.index === 5) {
        sawBoot = true;
        log("boot output — continuing to wait for a console prompt");
        session.mark();
        continue;
      }
      // VALIDATE before believing it. The 2026-08-03 run accepted boot noise
      // as a prompt ~50 s after reboot and pushed the whole config into a
      // switch that needed ~2 minutes to really show "Username:". A settled
      // console prints NOTHING unprompted: require a quiet window, then a CR
      // that answers with a fresh prompt.
      session.mark();
      await session.sleep(2500);
      if (session.getSinceMark().trim() !== "") {
        log("output still flowing after the prompt — still booting, waiting…");
        continue;
      }
      session.mark();
      await session.send("");
      try {
        await session.expectAny(
          [USERNAME, PASSWORD, LOGIN_BANNER, PROMPT_USER, PROMPT_PRIV],
          8000,
        );
        log("post-reset console prompt validated (quiet window + CR round-trip)");
        return;
      } catch {
        log("prompt-looking text did not answer a CR — still booting, waiting…");
        session.mark();
        continue;
      }
    } catch {
      /* still booting */
    }

    var now = Date.now();
    if (now - lastProgress > 15000) {
      log(
        `still waiting for a console prompt (${Math.round((deadline - now) / 1000)}s left)${sawBoot ? " [boot seen]" : ""} — ${clipTail(session.getSinceMark(), 80) || "no new serial output"}`,
      );
      lastProgress = now;
    }

    if (now - lastNudge > 20000) {
      // A CR makes a silent "Switch>" print itself again.
      session.mark();
      await session.send("");
      lastNudge = now;
    }
    await session.sleep(2000);
  }
  throw new Error("no console prompt after reboot (timeout)");
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
    vlanId: number;
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
  var vlanId = opts.vlanId;
  var targetUser = (opts.targetUser || "admin").trim() || "admin";
  var mask = toDottedMask(opts.mask);
  var vlanName = `LAN${vlanId}_Management`;

  // Best effort: kill the pager for this session. The guide files it under
  // global config mode but its own example runs it from EXEC — try EXEC
  // first (a rejected command only costs an error echo; an active pager
  // costs the whole push).
  await sendContinueOnError(session, log, "terminal length 0", "terminal length 0", 4000);

  // Discover uplink(s) with link so the mgmt VLAN is reachable for the probe.
  var upPorts: string[] = [];
  var trunk = "";
  if (vlanId !== 1) {
    log("show interface | include protocol (find uplinks; links may still be coming up)…");
    upPorts = await discoverUpPorts(session, log, 75000, opts.signal);
    trunk = compactPortRange(upPorts);
    if (trunk) {
      log(`uplink(s) with link: ${trunk}`);
    } else if (upPorts.length) {
      log(`uplink ports (non-contiguous): ${upPorts.join(", ")}`);
    } else {
      // NEVER skip the trunk. The 2026-08-03 run saw no link (discovery ran
      // while the switch was still booting) and provisioned no trunk at all —
      // the mgmt VLAN was carried nowhere and the probe failed. Trunking the
      // four SFP+ cages (captured naming) is harmless when they are empty and
      // exactly right when the uplink fibre is in one of them.
      trunk = FALLBACK_UPLINKS;
      log(
        `no Ethernet link seen — trunking all SFP+ cages (${FALLBACK_UPLINKS}) so the mgmt VLAN is carried regardless`,
      );
    }
  }

  var cmds: { cmd: string; label?: string; waitMs?: number }[] = [];

  // "terminal length 0" again from config mode — the documented home of the
  // command; whichever of the two forms the firmware accepts wins.
  cmds.push({ cmd: "terminal length 0", label: "terminal length 0 (config mode)" });

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

  if (vlanId !== 1) {
    cmds.push({ cmd: `vlan ${vlanId}` });
    cmds.push({ cmd: `name ${vlanName}` });
    cmds.push({ cmd: "exit", label: `exit vlan ${vlanId}` });
  }

  cmds.push({ cmd: `interface vlan ${vlanId}` });
  cmds.push({ cmd: `ip address ${opts.ip} ${mask}` });
  cmds.push({ cmd: "exit", label: `exit interface vlan ${vlanId}` });

  if (opts.gateway?.trim()) {
    // Captured 2026-08-03: "ip route default GW" accepted in config mode
    // ("ip default-gateway" answers Unknown command on this firmware).
    cmds.push({ cmd: `ip route default ${opts.gateway.trim()}` });
  }

  // SSH key generation can keep the console silent well past 10 s: its own
  // budget, or every later reply shifts by one command.
  cmds.push({ cmd: "ip sshd enable", waitMs: 45000 });
  cmds.push({ cmd: "ip http server" });
  // LLDP ships OFF on this platform ("show lldp neighbor" answers "LLDP is
  // not enabled" on a factory unit). Without it the switch is invisible to
  // every neighbour and sees none, so the topology view stays empty on a
  // healthy device — provisioning turns it on by default.
  cmds.push({ cmd: "lldp run" });

  // Carry the mgmt VLAN on live uplink(s) so the probe can reach the SVI.
  if (vlanId !== 1) {
    var trunkTargets = trunk ? [trunk] : upPorts;
    var ti = 0;
    for (ti = 0; ti < trunkTargets.length; ti++) {
      var tp = trunkTargets[ti];
      cmds.push({ cmd: `interface ${tp}` });
      cmds.push({ cmd: "switchport mode trunk", label: `${tp}: switchport mode trunk` });
      cmds.push({
        cmd: "switchport trunk vlan-allowed 1-4094",
        label: `${tp}: trunk vlan-allowed 1-4094`,
      });
      cmds.push({ cmd: "exit", label: `exit interface ${tp}` });
    }
    cmds.push({
      cmd: "no interface vlan 1",
      label: "no interface vlan 1 (drop factory SVI)",
    });
  }

  // CREDENTIALS LAST — if this platform drops the console on the password
  // change (the S3900 did), everything else is already in place and
  // runConfigCmds re-logins with the NEW secret to finish.
  if (opts.newPassword) {
    cmds.push({
      cmd: `username ${targetUser} password ${opts.newPassword}`,
      label: `username ${targetUser} password •••`,
    });
    cmds.push({
      // Captured present in the provisioned unit's configuration: this is what
      // makes the local account guard anything.
      cmd: "aaa authentication login default local",
    });
    cmds.push({
      cmd: `enable password 0 ${opts.enablePassword || opts.newPassword}`,
      label: "enable password 0 •••",
    });
  }

  log("pushing management configuration (each command verified)…");
  await runConfigCmds(session, log, opts.logins, cmds, opts.signal);

  await saveWrite(session, log, opts.logins);
  log("management config pushed");
}

/**
 * "write" — the documented save verb of this platform (configuration
 * preparation guide §1.6; not "copy running-config startup-config", and no
 * startup file-name prompt is documented). The exact success wording is
 * uncaptured: an explicit OK/success line is accepted, a clean return to the
 * prompt is accepted WITH a warning, a CLI error throws — a management config
 * that does not survive a reboot is worse than a refused one.
 */
export async function saveWrite(
  session: SerialSession,
  log: SoftResetLog,
  logins?: LoginCandidate[],
): Promise<void> {
  log("write (save configuration)…");
  session.mark();
  await session.send("write");
  var deadline = Date.now() + 90000;
  var relogins = 0;
  while (Date.now() < deadline) {
    var hit: { index: number; match: string };
    try {
      hit = await session.expectAny(
        [CONFIRM_YN, SAVE_OK, CLI_ERROR, LOGIN_FALLBACK, PROMPT_PRIV],
        20000,
      );
    } catch {
      throw new Error("save: no reaction to \"write\" (no confirm / OK / prompt)");
    }
    if (hit.index === 0) {
      // This confirm asks whether to SAVE — here the answer is yes.
      session.mark();
      await session.send("y");
      log("save: confirmed y");
      continue;
    }
    if (hit.index === 1) {
      log(`save: ok ("${hit.match.trim()}")`);
      return;
    }
    if (hit.index === 2) {
      throw new Error(`save failed: "${hit.match}"`);
    }
    if (hit.index === 3) {
      relogins++;
      if (!logins || relogins > 2) {
        throw new Error("save: console session lost before the flash write");
      }
      log("save: console fell back to login — re-login and retry write…");
      await reloginAny(session, logins, log);
      session.mark();
      await session.send("write");
      continue;
    }
    // Prompt returned: an OK/error may have arrived in the same burst.
    var tail = session.getSinceMark();
    if (SAVE_OK.test(tail)) {
      log("save: ok");
      return;
    }
    if (CLI_ERROR.test(tail)) {
      throw new Error(`save failed: "${clipTail(tail, 160)}"`);
    }
    log(
      "save: prompt returned without an explicit confirmation — accepted, but verify the config survives a reboot (uncaptured firmware wording)",
    );
    return;
  }
  throw new Error("save: timed out waiting for the flash write result");
}

/** "TGigaEthernet0/3 is up, line protocol is up" → "tg0/3";
 * "GigaEthernet0/1 …" → "g0/1". Short forms captured on hardware
 * (2026-08-03: "show run int tg0/3" accepted). SVIs ("VLAN1 is up")
 * never match. */
export function parseUpPorts(show: string): string[] {
  var re = /^((?:T?Giga|Fast)?Ethernet)(\d+\/\d+)\s+is\s+up,\s*line\s+protocol\s+is\s+up/gim;
  var out: string[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(show)) !== null) {
    var kind = m[1].toLowerCase();
    var key = "";
    if (kind.startsWith("tgiga")) key = `tg${m[2]}`;
    else if (kind.startsWith("giga")) key = `g${m[2]}`;
    else if (kind.startsWith("fast")) key = `f${m[2]}`;
    else if (kind === "ethernet") key = `e${m[2]}`;
    if (!key || seen[key]) continue;
    seen[key] = true;
    out.push(key);
  }
  return out;
}

/** ["tg0/1","tg0/2","tg0/3"] → "range tg0/1-3" when contiguous with one
 * prefix and slot; otherwise "". Captured 2026-08-03: "int range tg0/1-4"
 * gives Switch_config_if_range# — the "range" keyword is MANDATORY
 * ("int tg0/1-4" answers "Parameter invalid"). */
export function compactPortRange(ports: string[]): string {
  if (ports.length === 0) return "";
  var prefix = "";
  var slot = -1;
  var nums: number[] = [];
  var i = 0;
  for (i = 0; i < ports.length; i++) {
    var m = /^(tg|g|f|e)(\d+)\/(\d+)$/i.exec(ports[i]);
    if (!m) return "";
    var p = m[1].toLowerCase();
    var s = Number(m[2]);
    var n = Number(m[3]);
    if (!prefix) prefix = p;
    else if (p !== prefix) return "";
    if (slot < 0) slot = s;
    else if (s !== slot) return "";
    nums.push(n);
  }
  for (i = 1; i < nums.length; i++) {
    if (nums[i] !== nums[i - 1] + 1) return "";
  }
  if (nums.length === 1) return `${prefix}${slot}/${nums[0]}`;
  return `range ${prefix}${slot}/${nums[0]}-${nums[nums.length - 1]}`;
}

/** Run show commands on serial and log a short snippet (sanity before IP probe). */
async function verifyOnSerial(
  session: SerialSession,
  opts: {
    ip: string;
    gateway?: string;
    log: SoftResetLog;
    logins: LoginCandidate[];
    signal?: AbortSignal;
  },
): Promise<void> {
  var log = opts.log;

  // Re-assert the prompt first — never type "show running-config" into a
  // login prompt if the session dropped.
  session.mark();
  await session.send("");
  var state = await expectReplyOrLogin(session, 5000);
  if (state.kind === "login") {
    log("serial verify: console at login — re-opening session…");
    await reloginAny(session, opts.logins, log, opts.signal);
  }

  // "include address", not "include ip address": one word only.
  log("serial verify: show running-config | include address…");
  var snippet = await collectShowOutput(
    session,
    "show running-config | include address",
    15000,
  );
  if (opts.ip && snippet.includes(opts.ip)) {
    log(`serial verify: management IP ${opts.ip} present in running-config`);
  } else {
    // The filter itself may be refused on this firmware — fall back to the
    // full output before concluding.
    var full = await collectShowOutput(session, "show running-config", 15000);
    if (opts.ip && full.includes(opts.ip)) {
      log(`serial verify: management IP ${opts.ip} present in running-config`);
    } else {
      log(`serial verify: show running-config (tail):\n${full.trim().slice(-600)}`);
    }
  }

  if (opts.gateway?.trim()) {
    log(`serial verify: ping ${opts.gateway.trim()}…`);
    var pingOut = await collectShowOutput(session, `ping ${opts.gateway.trim()}`, 15000);
    if (/!{2,}|success rate is [1-9]|\breceived\b/i.test(pingOut)) {
      log("serial verify: gateway ping ok");
    } else {
      log(`serial verify: ping output:\n${pingOut.trim().slice(-400)}`);
    }
  }

  var vlans = await collectShowOutput(session, "show vlan", 8000);
  log(`serial verify: show vlan (tail):\n${vlans.trim().slice(-500)}`);
}

/** Accept a dotted mask as-is, convert a /nn (with or without slash) to dotted
 * — this CLI has no CIDR form. */
export function toDottedMask(mask: string): string {
  var m = mask.trim();
  if (/^\d+\.\d+\.\d+\.\d+$/.test(m)) return m;
  var bits = Number(m.replace(/^\//, ""));
  if (!Number.isFinite(bits) || bits < 1 || bits > 32) return "255.255.255.0";
  var out: number[] = [];
  var i = 0;
  for (i = 0; i < 4; i++) {
    var take = Math.max(0, Math.min(8, bits - i * 8));
    out.push(256 - Math.pow(2, 8 - take));
  }
  return out.join(".");
}

/**
 * Full soft-reset until the management CLI is configured and verified on
 * serial. Network reachability is probed by the caller afterward.
 */
export async function runFsS3400SoftReset(input: FsS3400SoftResetInput): Promise<void> {
  var session = input.session;
  var log = input.log;
  var iface = (input.mgmtIface || "vlan 1").trim() || "vlan 1";
  var vlanId = parseVlanId(iface);
  if (vlanId === null) {
    throw new Error(
      `management interface "${iface}" does not exist on an S3400 — this platform only has VLAN interfaces (use "vlan N")`,
    );
  }

  if (!input.mgmtIp.trim() || !input.mgmtMask.trim()) {
    throw new Error("management IP and mask are required");
  }
  if (!input.targetPassword) {
    throw new Error("instance credential secret is required");
  }

  var user = input.currentUser || "admin";
  var pass = input.currentPassword !== undefined ? input.currentPassword : "admin";
  var enablePass = input.enablePassword !== undefined ? input.enablePassword : "";
  var targetUser = (input.targetUser || "admin").trim() || "admin";

  // The device may hold ANY of these: the UI-entered credentials, the
  // instance credential (an earlier half-finished push), or the factory
  // default admin / admin. On a console line without authentication no login
  // prompt appears and the first candidate reaches # without typing anything.
  var initialLogins: LoginCandidate[] = [
    { user, pass, enable: enablePass, label: `current serial credentials (${user})` },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "admin", pass: "admin", enable: "admin", label: "admin / admin (factory)" },
  ];
  var seenLogin: Record<string, boolean> = {};
  initialLogins = initialLogins.filter((l) => {
    var key = `${l.user} ${l.pass}`;
    if (seenLogin[key]) return false;
    seenLogin[key] = true;
    return true;
  });

  log("opening a privileged console session…");
  await reloginAny(session, initialLogins, log, input.signal);

  await factoryReset(session, log, input.signal);

  await waitConsoleAfterReboot(session, log, 300000, input.signal);

  // Fresh buffer for the factory console — never inherit a pre-reboot prompt.
  session.clearCapture();

  log("factory login admin / admin…");
  var factoryLogins: LoginCandidate[] = [
    // Factory default is admin / admin — NOT admin / blank (a refused blank
    // login cost the S3900 runs 40 s each; don't repeat that here). On a
    // console line without authentication the credentials are never typed.
    { user: "admin", pass: "admin", enable: "admin", label: "factory admin / admin" },
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
      await ensurePrivileged(session, fl.user, fl.pass, fl.enable, log, 45000, input.signal);
      factoryOk = true;
      break;
    } catch (e) {
      log(`${fl.label} failed (${(e as Error).message || e})`);
    }
  }
  if (!factoryOk) {
    throw new Error(
      "could not open a console session after factory reset (tried factory admin/admin, instance cred)",
    );
  }

  // Re-login order if the console drops mid-push: the instance credential
  // FIRST (after "aaa authentication login default local" + username it is
  // the secret in force), factory shapes second.
  var pushLogins: LoginCandidate[] = [
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "admin", pass: "admin", enable: "admin", label: "factory admin / admin" },
  ];

  await pushManagement(session, {
    vlanId,
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
    gateway: input.gateway,
    log,
    logins: pushLogins,
    signal: input.signal,
  });

  log(
    `serial side done — login is now ${targetUser} / <instance credential secret> (enable same); caller will probe the management IP`,
  );
}

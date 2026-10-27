/**
 * Ubiquiti EdgeSwitch / UniFi Switch — soft reset + management push over Web
 * Serial. Calibrated on the UniFi Switch 16.
 *
 * STATUS: every form here is DOCUMENTED (EdgeSwitch CLI Command Reference) and
 * NONE has been seen on hardware. The FS S3400 script was written the same way
 * and the first real run contradicted it on three separate points, so treat
 * the strings below as a starting hypothesis: the run log is what corrects
 * them, here AND in internal/driver/ubiquiti/edgeswitch.
 *
 * This is Broadcom FASTPATH, not a Cisco-alike, and four differences shape the
 * whole script:
 *
 *   - THE MODE OF A COMMAND IS NOT WHERE A CISCO HABIT PUTS IT. "network
 *     parms", "network protocol", "network mgmt_vlan", "vlan database",
 *     "write memory" and "clear config" are PRIVILEGED EXEC commands.
 *     Sending them from "(Config)#" fails;
 *   - management is NOT an SVI: one network interface, "network parms <ip>
 *     <mask> [gw]", and its VLAN chosen separately with "network mgmt_vlan";
 *   - a port needs pvid + participation + tagging to carry a VLAN; a pvid
 *     alone is not membership. The "all" variants of participation and
 *     tagging are Global Config commands that hit every interface at once,
 *     which is what provisioning uses;
 *   - "clear config" does NOT reboot. It resets the running configuration in
 *     place, which is why this script does not wait for a boot: it re-syncs on
 *     the prompt and pushes. Nothing is persistent until "write memory".
 *
 * "write memory confirm" saves WITHOUT prompting — the plain form asks a
 * question a script would hang on.
 */

import type { SerialSession } from "./serialExpect";

export type SoftResetLog = (msg: string) => void;

export type EdgeSwitchSoftResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  /** Credentials that work BEFORE reset (factory default ubnt/ubnt). */
  currentUser: string;
  currentPassword: string;
  /**
   * Enable password. Empty falls back to the login password: "enable" asks
   * for one on this platform (captured 2026-08-03) and an empty answer is
   * refused.
   */
  enablePassword: string;
  /** Admin account to create after reset (from instance credentials). */
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  mgmtMask: string;
  gateway?: string;
  /** Management VLAN, as "vlan N". VLAN 1 is the platform default. */
  mgmtIface?: string;
  /**
   * Uplink port(s), "0/16" or "0/1-0/4". NO LONGER used to decide where
   * the management VLAN goes — every port carries it after provisioning (see
   * pushCmds). Kept for callers that want to record which port they cabled.
   */
  uplink?: string;
  signal?: AbortSignal;
};

const USERNAME = /user\s*(name)?\s*:|login\s*:/i;
const PASSWORD = /password\s*:/i;
const LOGIN_BANNER = /press\s+(any\s+)?key|press\s+enter|user\s+access\s+verification/i;
const PROMPT_USER = />\s*$/m;
const PROMPT_PRIV = /#\s*$/m;
/** "Are you sure you want to clear the configuration? (y/n)" and friends. */
const CONFIRM_YN = /\(y\/n\)|are\s+you\s+sure[^\n]*|continue[^\n]*\?|\(yes\/no\)/i;
/**
 * FASTPATH rejections. It does NOT use the IOS "% Invalid input" form
 * consistently — "Command not found", "Incorrect input!" and "An invalid
 * interface" are the shapes the reference prints.
 */
const CLI_ERROR =
  /command not found|incorrect input|invalid input|an invalid interface|unrecognized command|ambiguous command|incomplete command|not a valid|error:|% ?invalid/i;
/** The pager: "--More-- or (q)uit". */
const PAGER = /--\s*more\s*--[^\n]*$/i;
/** Any sign the console dropped back to login mid-script. */
const LOGIN_FALLBACK = /user\s*(name)?\s*:\s*$|login\s*:\s*$|password\s*:\s*$/i;

/** The default uplink when the caller names none: the top port of an S16. */
const DEFAULT_UPLINK = "0/16";

type ReplyKind = "confirm" | "error" | "prompt" | "timeout";

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("soft reset aborted");
}

function clipTail(text: string, n: number): string {
  return text.slice(-n).replace(/\s+/g, " ").trim();
}

function parseVlanId(iface: string): number {
  var m = (iface || "").match(/(\d+)/);
  if (!m) return 1;
  var n = Number(m[1]);
  if (!Number.isFinite(n) || n < 1 || n > 4093) return 1;
  return n;
}

/**
 * Classify what came back after a command, WITHOUT reaching outside this
 * command's own output: a window over the whole capture blames a command for
 * the previous one's error, which is the bug that made the S3400 script report
 * two working commands as refused.
 */
async function expectReply(
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
    var err = session.getSinceMark().match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
  }
}

/**
 * Collect a show command's output, feeding "--More--".
 *
 * The pager is fed a SPACE rather than relying on "no pagination": the S3400
 * taught that a pager-disabling command can be accepted and do nothing, and an
 * unfed pager does not merely truncate — it eats the first character of the
 * next command.
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
    await session.sleep(400);
    var out = session.getSinceMark();
    if (PAGER.test(out.slice(-160).trimEnd())) {
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

type LoginCandidate = { user: string; pass: string; enable: string; label: string };

/**
 * Reach the privileged "#" prompt.
 *
 * A factory EdgeSwitch asks for a login (ubnt/ubnt), and its "enable" ALSO
 * asks for a password — captured on hardware, contradicting the assumption
 * this script shipped with. The login password is offered. A prompt-looking tail
 * is CONFIRMED with a CR round-trip before being trusted: on the S3400, boot
 * noise ending in '#' was taken for a live prompt and the whole push went into
 * a switch that was still starting.
 */
async function ensurePrivileged(
  session: SerialSession,
  cand: LoginCandidate,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  session.mark();
  await session.send("");
  await session.sleep(600);

  var deadline = Date.now() + timeoutMs;
  var attempts = 0;
  var enableTries = 0;
  var nudges = 0;
  while (Date.now() < deadline) {
    aborted(signal);
    var tail = session.getSinceMark().slice(-600) || session.getCapture().slice(-400);

    if (PROMPT_PRIV.test(tail)) {
      session.mark();
      await session.send("");
      var confirmed = await expectReply(session, 5000);
      if (confirmed.kind === "prompt" && PROMPT_PRIV.test(session.getSinceMark())) {
        log("privileged prompt confirmed (#)");
        return;
      }
      if (confirmed.kind === "timeout") {
        log("prompt did not answer a CR — retrying");
        nudges++;
        if (nudges > 8) throw new Error("prompt-looking text never answered a CR");
        continue;
      }
      // Fall through: whatever answered is handled below.
    }

    if (PROMPT_USER.test(tail) && !PROMPT_PRIV.test(tail)) {
      enableTries++;
      // CAPPED. The first hardware run repeated this branch 47 times: enable
      // asked for a password, the empty answer was refused, the switch stayed
      // in user mode, and nothing broke the loop until the outer deadline.
      if (enableTries > 3) {
        throw new Error(
          "\"enable\" did not reach privileged mode after 3 tries — the enable " +
            "password is not what this candidate carries",
        );
      }
      log(`user mode — enable (try ${enableTries})…`);
      session.mark();
      await session.send("enable");
      var afterEnable = await session.expectAny([PASSWORD, PROMPT_PRIV], 8000).catch(() => null);
      if (afterEnable && afterEnable.index === 0) {
        // CAPTURED on hardware 2026-08-03: "enable" DOES ask for a password
        // on this platform — "(UBNT EdgeSwitch) >enable / Password:" — and an
        // empty answer is refused. The switch's enable secret is the login
        // password on a factory unit, so that is what is offered; an explicit
        // enablePassword from the caller still wins.
        session.mark();
        await session.send(cand.enable || cand.pass);
      }
      await session.sleep(800);
      continue;
    }

    if (USERNAME.test(tail)) {
      attempts++;
      // A credential refused three times will not be accepted on the fortieth.
      if (attempts > 3) {
        throw new Error("credentials refused 3 times — giving up on this candidate");
      }
      log(`login as ${cand.user} (attempt ${attempts})`);
      session.mark();
      await session.send(cand.user);
      var afterUser = await session
        .expectAny([PASSWORD, PROMPT_PRIV, PROMPT_USER], 15000)
        .catch(() => null);
      if (afterUser && afterUser.index === 0) {
        session.mark();
        await session.send(cand.pass);
        await session.sleep(800);
      }
      continue;
    }

    if (PASSWORD.test(tail)) {
      log("stale Password: — sending CR to re-sync");
      session.mark();
      await session.send("");
      await session.sleep(700);
      nudges++;
      if (nudges > 10) throw new Error("stuck at a Password: prompt");
      continue;
    }

    if (LOGIN_BANNER.test(tail)) {
      session.mark();
      await session.send("");
      await session.sleep(600);
      continue;
    }

    nudges++;
    if (nudges > 8) {
      throw new Error("no login or prompt seen on the serial console");
    }
    session.mark();
    await session.send("");
    await session.sleep(800);
  }
  throw new Error("could not reach the EdgeSwitch privileged prompt");
}

async function loginAny(
  session: SerialSession,
  logins: LoginCandidate[],
  log: SoftResetLog,
  signal?: AbortSignal,
): Promise<void> {
  var i = 0;
  for (i = 0; i < logins.length; i++) {
    try {
      log(`trying ${logins[i].label}…`);
      await ensurePrivileged(session, logins[i], log, 40000, signal);
      return;
    } catch (e) {
      log(`${logins[i].label} failed (${(e as Error).message || e})`);
    }
  }
  throw new Error("no credential opened a privileged console session");
}

/**
 * Run commands one at a time, answering confirmations and surviving a console
 * that drops back to login.
 *
 * Each command waits for its OWN reply before the next is sent: FASTPATH
 * prints an error as several lines, and pipelining would attribute one
 * command's refusal to its neighbour.
 */
export async function runCmds(
  session: SerialSession,
  log: SoftResetLog,
  logins: LoginCandidate[],
  cmds: { cmd: string; label?: string; waitMs?: number }[],
  signal?: AbortSignal,
): Promise<void> {
  var i = 0;
  var relogins = 0;
  while (i < cmds.length) {
    aborted(signal);
    var c = cmds[i];
    var label = c.label || c.cmd;
    session.mark();
    await session.send(c.cmd);
    var reply = await expectReply(session, c.waitMs ?? 12000);

    if (reply.kind === "login") {
      relogins++;
      if (relogins > 3) {
        throw new Error(`console session lost repeatedly (last at "${label}")`);
      }
      log(`console fell back to login at "${label}" — re-login and retry…`);
      await loginAny(session, logins, log, signal);
      continue;
    }
    if (reply.kind === "confirm") {
      session.mark();
      await session.send("y");
      log(`${label}: confirmed y`);
    } else if (reply.kind === "error") {
      log(`${label}: CLI error "${reply.match}" — continuing (${clipTail(session.getSinceMark(), 160)})`);
    } else if (reply.kind === "prompt") {
      log(`${label}: ok`);
    } else {
      log(`${label}: no reply — continuing`);
    }
    await session.sleep(250);
    i++;
  }
}

/**
 * "clear config" — the soft reset on this platform.
 *
 * It resets the running configuration to factory defaults and does NOT reboot,
 * so there is no boot to wait for. The trade is that the switch's management
 * address disappears the moment it lands, and nothing is persistent until
 * "write memory" — which is exactly why the push follows immediately.
 */
async function clearConfig(
  session: SerialSession,
  log: SoftResetLog,
  logins: LoginCandidate[],
  signal?: AbortSignal,
): Promise<void> {
  log("clear config (factory defaults, no reboot on this platform)…");
  await runCmds(session, log, logins, [{ cmd: "clear config", waitMs: 20000 }], signal);
  // The reset can drop the session back to a fresh login with factory
  // credentials: re-establish before pushing anything.
  await session.sleep(3000);
  session.mark();
  await session.send("");
  var state = await expectReply(session, 6000);
  if (state.kind === "login" || state.kind === "timeout") {
    log("console re-syncing after clear config…");
    await loginAny(session, logins, log, signal);
  }
}

/**
 * Build the push. Mirrors internal/driver/ubiquiti/edgeswitch.MgmtPushCmds —
 * the two must not drift, because that Go function is the reference the driver
 * documents and this is what actually reaches a switch.
 */
export function pushCmds(opts: {
  vlanId: number;
  ip: string;
  mask: string;
  gateway?: string;
  // NO hostname option, and that is deliberate — see the "3)" block below.
  uplink: string;
  targetUser: string;
  targetPassword: string;
}): { cmd: string; label?: string; waitMs?: number }[] {
  var out: { cmd: string; label?: string; waitMs?: number }[] = [];

  // 1) VLAN database — PRIVILEGED EXEC, not Global Config.
  if (opts.vlanId !== 1) {
    out.push({ cmd: "vlan database" });
    out.push({ cmd: `vlan ${opts.vlanId}` });
    out.push({ cmd: `vlan name ${opts.vlanId} LAN${opts.vlanId}_Management` });
    out.push({ cmd: "exit", label: "exit vlan database" });
  }

  // 2) The switch's own address: one network interface, no SVI.
  out.push({ cmd: "network protocol none" });
  var parms = `network parms ${opts.ip} ${opts.mask}`;
  if (opts.gateway?.trim()) parms += ` ${opts.gateway.trim()}`;
  out.push({ cmd: parms, label: "network parms (address, mask, gateway)" });
  if (opts.vlanId !== 1) {
    out.push({ cmd: `network mgmt_vlan ${opts.vlanId}` });
  }

  // 3) Global Config: VLAN membership, then the account.
  //
  // NO "hostname" LINE, AND THE PLATFORM IS THE REASON. This FASTPATH CLI has
  // no `hostname` command: the form was sent to the lab EdgePoint S16 on
  // 2026-08-05 and came back "% Invalid input detected at '^' marker." The
  // whole account — what was sent, what answered, and why `set prompt` /
  // `snmp-server sysname` are the suspects for the real form — is in
  // internal/driver/ubiquiti/edgeswitch/hostname.go. The refusal is non-fatal
  // here (runCmds logs a CLI error and continues), which is exactly what made it
  // worth removing: a command sent in order to be refused names no switch and
  // puts a red herring in the console transcript, where it hides a real refusal.
  // Do not re-add it. Whatever verb turns out to be right, it gets captured on
  // hardware first.
  out.push({ cmd: "configure" });
  if (opts.vlanId !== 1) {
    // PROVISIONING RULE: every port becomes a trunk carrying every VLAN.
    //
    // The first run of this script configured the management VLAN on ONE
    // guessed uplink (0/16) and nothing else, so the tagged VLAN never reached
    // the port the fibre was actually in and the probe could not see the
    // switch. At first provisioning meshcraft does not know the cabling, and
    // guessing it is the one thing that cannot be made reliable.
    //
    // "all" is a real Global Config form on this platform, not a loop over
    // interfaces: "vlan participation all include N" and "vlan port tagging
    // all N" configure every interface at once, which is both shorter and
    // atomic. VLAN 1 stays the untagged native everywhere (the default pvid),
    // so an access device on any port keeps working while the management VLAN
    // rides tagged alongside it.
    out.push({
      cmd: `vlan participation all include ${opts.vlanId}`,
      label: `all ports: member of VLAN ${opts.vlanId}`,
    });
    out.push({
      cmd: `vlan port tagging all ${opts.vlanId}`,
      label: `all ports: VLAN ${opts.vlanId} tagged`,
    });
  }
  // Credentials LAST: whatever the platform does when the logged-in account's
  // password changes, everything else is already in place when it happens.
  if (opts.targetPassword) {
    out.push({
      cmd: `username ${opts.targetUser} password ${opts.targetPassword} level 15`,
      label: `username ${opts.targetUser} password ••• level 15`,
    });
  }
  out.push({ cmd: "exit", label: "exit config mode" });

  // 4) Save, from privileged EXEC. "confirm" or it asks.
  out.push({ cmd: "write memory confirm", label: "write memory confirm", waitMs: 45000 });
  return out;
}

/** Verify on the console before the caller probes the network. */
async function verifyOnSerial(
  session: SerialSession,
  opts: { ip: string; log: SoftResetLog },
): Promise<void> {
  var log = opts.log;
  log("serial verify: show network…");
  var out = await collectShowOutput(session, "show network", 15000);
  if (opts.ip && out.includes(opts.ip)) {
    log(`serial verify: management address ${opts.ip} present`);
  } else {
    log(`serial verify: show network (tail):\n${out.trim().slice(-500)}`);
  }
}

/**
 * Full soft reset until the management CLI is configured and verified on
 * serial. Network reachability is probed by the caller afterwards.
 */
export async function runEdgeSwitchSoftReset(input: EdgeSwitchSoftResetInput): Promise<void> {
  var session = input.session;
  var log = input.log;

  if (!input.mgmtIp.trim() || !input.mgmtMask.trim()) {
    throw new Error("management IP and mask are required");
  }
  if (!input.targetPassword) {
    throw new Error("instance credential secret is required");
  }
  // "network parms" takes a DOTTED mask and no CIDR form.
  if (input.mgmtMask.trim().startsWith("/")) {
    throw new Error(
      `management mask ${input.mgmtMask} is a prefix — "network parms" takes a dotted mask`,
    );
  }

  var vlanId = parseVlanId(input.mgmtIface || "vlan 1");
  var uplink = (input.uplink || DEFAULT_UPLINK).trim() || DEFAULT_UPLINK;
  var targetUser = (input.targetUser || "ubnt").trim() || "ubnt";

  // The device may hold the UI-entered credentials, the instance credential
  // (an earlier half-finished run), or the factory ubnt/ubnt.
  var logins: LoginCandidate[] = [
    {
      user: input.currentUser || "ubnt",
      pass: input.currentPassword !== undefined ? input.currentPassword : "ubnt",
      enable: input.enablePassword ?? "",
      label: `current serial credentials (${input.currentUser || "ubnt"})`,
    },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "ubnt", pass: "ubnt", enable: "", label: "factory ubnt / ubnt" },
  ];
  var seen: Record<string, boolean> = {};
  logins = logins.filter((l) => {
    var k = `${l.user} ${l.pass}`;
    if (seen[k]) return false;
    seen[k] = true;
    return true;
  });

  log("opening a privileged console session…");
  await loginAny(session, logins, log, input.signal);

  // Best effort: kill the pager for the session. NOT relied upon — every read
  // feeds "--More--" regardless.
  // "terminal length 0", NOT "no pagination": the reference names the
  // latter and the hardware answers "% Invalid input detected at '^' marker"
  // (captured 2026-08-03 on EdgePoint S16 1.9.2).
  await runCmds(session, log, logins, [{ cmd: "terminal length 0", waitMs: 5000 }], input.signal);

  await clearConfig(session, log, logins, input.signal);

  log(
    `pushing management: vlan ${vlanId}, ${input.mgmtIp} ${input.mgmtMask} — ` +
      "every port becomes a trunk carrying the management VLAN tagged, so the " +
      "cabling does not have to be known",
  );
  await runCmds(
    session,
    log,
    logins,
    pushCmds({
      vlanId,
      ip: input.mgmtIp.trim(),
      mask: input.mgmtMask.trim(),
      gateway: input.gateway,
      uplink,
      targetUser,
      targetPassword: input.targetPassword,
    }),
    input.signal,
  );

  await verifyOnSerial(session, { ip: input.mgmtIp.trim(), log });

  log(
    `serial side done — login is now ${targetUser} / <instance credential secret>; ` +
      "caller will probe the management address",
  );
}

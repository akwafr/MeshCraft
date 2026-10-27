/**
 * Arista EOS provisioning over Web Serial: factory reset, ZTP exit, and the
 * management push, until the switch answers on the LAN.
 *
 * Sibling of ciscoIosSoftReset.ts and deliberately NOT a generalization of it.
 * EOS is IOS-SHAPED on purpose — `configure`, `interface`, `switchport mode
 * trunk`, `write memory` all behave as an IOS hand expects — and that surface
 * similarity is exactly what makes a shared console script dangerous. The places
 * the two disagree are the places where the IOS form is silently WRONG here.
 *
 * # Status of what is written here
 *
 * DOCUMENTED, not captured. Every string comes from the vendor's command
 * reference and the ZTP chapter of the EOS User Manual — see
 * internal/driver/arista/eos, which carries the same caveat and the same command
 * set for the SSH path.
 *
 * # THE ONE THING THAT MAKES THIS PROCEDURE DIFFERENT FROM EVERY OTHER IN THIS
 * # DIRECTORY: a factory Arista is not sitting at a prompt, it is in ZTP
 *
 * An EOS switch with NO startup-config boots into ZERO TOUCH PROVISIONING: it
 * brings every port up, sends DHCP on all of them looking for a boot file, fails,
 * waits, and retries — for ever, logging each round to the console. `admin` logs
 * in with no password, the CLI answers, and EVERYTHING WRITTEN TO IT IS THROWN
 * AWAY at the next ZTP cycle.
 *
 * The way out is `zerotouch cancel`, which writes an EMPTY startup-config (that
 * file's mere existence is what disables ZTP) and REBOOTS. So a full reset is TWO
 * reboots, not one:
 *
 *   delete flash:startup-config → reload now   → the switch comes back IN ZTP
 *   zerotouch cancel                           → the switch comes back BLANK
 *
 * A script that assumed one reboot would sit typing a management configuration
 * into a console that is about to discard it, report a flawless run, and leave a
 * switch that comes back factory-fresh minutes later. So this one DETECTS which
 * of the three states it is looking at before it sends anything (cf.
 * [detectConsoleState]).
 *
 * # The four IOS reflexes that produce a rejected line here
 *
 *   ip address 192.0.2.5/24        ← PREFIX form; the dotted mask is refused
 *   ip route 0.0.0.0/0 <gw>        ← there is NO "ip default-gateway"
 *   (no trunk encapsulation)       ← EOS speaks 802.1Q only; the line does not exist
 *   (no crypto key generate)       ← SSH is on and keyed from first boot
 *
 * The last one is the good news: the longest and most failure-prone step of the
 * IOS provisioning path simply does not exist here, and there is no non-crypto
 * image that can strand a switch on the console.
 *
 * # Console speed
 *
 * 9600 8N1 on this range — the same as the Cisco range, NOT the 115200 the FS and
 * Ubiquiti ranges use.
 */

import type { SerialSession } from "./serialExpect";
import { hostnameFromDeviceName } from "./hostname.ts";

export type SoftResetLog = (msg: string) => void;

export type AristaEosSoftResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  /** Credentials that work BEFORE the reset. A factory Arista has none. */
  currentUser: string;
  currentPassword: string;
  /** Enable (privileged) password before the reset — empty on a factory unit. */
  enablePassword: string;
  /** Admin account to create after the reset (from instance credentials). */
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  /** Dotted mask or prefix length; converted to the PREFIX form EOS wants. */
  mgmtMask: string;
  gateway?: string;
  /** "vlan 99" (default "vlan 1") — the SVI that carries management. */
  mgmtIface?: string;
  /**
   * Every VLAN the SITE declares. Created on the device during provisioning AND
   * used as the allowed list every faceplate trunk is given.
   *
   * A switch coming out of a factory reset has VLAN 1 and nothing else, so any
   * port profile applied later — access on VLAN 30, trunk carrying 40 — lands on
   * a VLAN the device has never heard of.
   */
  siteVlans?: { id: number; name?: string }[];
  hostname?: string;
  signal?: AbortSignal;
};

/**
 * EOS presents a UNIX-style login prompt ("localhost login:"), not the
 * "Username:" a Catalyst puts up. Both are matched: a switch someone has put
 * behind AAA can show either, and a script that knew one of them would sit
 * typing into a prompt it did not recognise.
 */
const LOGIN_PROMPT = /(?:^|\n)\s*[\w.\-]*\s*login\s*:\s*$|user\s*name\s*:\s*$/i;
const PASSWORD = /password\s*:\s*$/i;

/**
 * The console REFUSING a credential, said in its own words.
 *
 * OBSERVED 2026-08-06 on the lab switch, and it cost the first provisioning run.
 * EOS logs in in TWO stages — "login:" then "Password:" — and a wrong answer
 * to the second prints "Login incorrect" and goes back to the first. Without
 * this pattern the script had no way to tell a refusal from a slow device: it sat
 * out the full 20 s expect timeout, twice, and gave up on a switch that was
 * answering perfectly.
 *
 * Recognising it turns a 40-second dead end into an immediate move to the next
 * candidate, which is the whole point of having a candidate list.
 */
const LOGIN_INCORRECT = /login incorrect|authentication fail(?:ed|ure)|access denied/i;
const PROMPT_PRIV = /#\s*$/m;
const PROMPT_USER = />\s*$/m;
/** A bare CR answers a "[confirm]" / "[yes/no]" whose default is right. */
const CONFIRM = /\[confirm\]/i;
const YESNO = /\[yes\/no\]\s*:?\s*$|\(y\/n\)\s*\??\s*$/i;
/** "System configuration has been modified. Save? [yes/no]" */
const SAVE_YESNO = /save\s*\??\s*\[yes\/no\]/i;

/**
 * ZERO TOUCH PROVISIONING, as the console announces it.
 *
 * These lines are the whole reason this script has a state machine. A switch
 * printing them has a working CLI, a login prompt and an "admin" account that
 * needs no password — it looks, from the console, exactly like a switch that is
 * ready to be configured. It is not: the next ZTP cycle discards whatever was
 * written.
 *
 * Matched broadly on purpose. The exact wording of the ZTP progress lines has
 * changed across trains; what has not changed is that they all carry the word.
 * A false positive costs one extra `zerotouch cancel` (harmless on a switch
 * already out of ZTP — it writes an empty startup-config and reboots, which is
 * the state we were heading for anyway). A false negative costs the entire
 * provisioning run, silently.
 */
const ZTP_MARK =
  /%ZTP-|\bZeroTouch\b|\bzerotouch\b|Zero\s?Touch\s?Provisioning|ZTP:\s/i;

/**
 * EOS refusals — and deliberately NOT the syslog lines that share their shape.
 *
 * "Aug  6 12:00:00 sw1 Ebra: %LINEPROTO-5-UPDOWN: Line protocol on Interface
 * Ethernet1, changed state to up" lands in the middle of command output and
 * starts with a percent sign. Treating it as an error turns every port that comes
 * up during the push into a failure — and on this platform a provisioning run
 * brings a whole faceplate to life at once, so that is not a rare case, it is
 * every run.
 *
 * The bang forms are matched only with their refusal phrasings: "!" alone is
 * the running configuration's section separator, and matching the character would
 * make every configuration read look like an error.
 */
const CLI_ERROR =
  /%\s*(?:invalid input|incomplete command|ambiguous command|unknown command|unrecognized command|unavailable command|invalid command|error[: ]|access denied|authorization denied|not supported)[^\n]*|!\s*(?:Interface does not exist|Invalid|Error)[^\n]*|command rejected:[^\n]*/i;

/** Output pager. Web Serial cannot announce a tall terminal. */
const PAGER = /-+\s*more\s*-+\s*$/i;
/** Any sign the console fell back to login mid-script. */
const LOGIN_FALLBACK = /(?:^|\n)\s*[\w.\-]*\s*login\s*:\s*$|user\s*name\s*:\s*$|password\s*:\s*$/i;
/** "Copy completed successfully" — the write reached the startup configuration. */
const SAVE_OK = /Copy completed successfully|\[OK\]|bytes copied/i;

/**
 * How long "write memory" is allowed to say NOTHING before the script reacts.
 *
 * MEASURED 2026-08-06 on the lab switch: 74 seconds from the command to
 * "Copy completed successfully.", with an entirely silent console in between.
 * The device was not stuck and the session was not lost — this platform simply
 * does not narrate a save.
 *
 * 45 s is therefore not padding, it is the floor below which a NORMAL save looks
 * like a failure. The previous 20 s made every save on this range report three
 * consecutive failures before succeeding, which is worse than a slow save: it
 * trains whoever reads the run log to ignore the word "failure".
 */
const SAVE_QUIET_WINDOW_MS = 45000;
/** Total budget for the save, across every quiet window and nudge. */
const SAVE_TOTAL_BUDGET_MS = 300000;
/** How many silent windows to sit through before giving up. */
const SAVE_MAX_SILENCES = 4;

/**
 * Boot output, for the wait between a reload and a usable console.
 *
 * Arista boots through Aboot and then a full Linux userspace, so there is a lot
 * of it, and the phases are worth recognising rather than sleeping through: the
 * gap between "Aboot" and a login prompt is well over a minute on this range.
 */
const BOOT_MARK =
  /Aboot|Booting|Loading linux|Welcome to Arista Networks EOS|Starting ProcMgr|switching to startup-config|\[\s*OK\s*\]/i;

type ReplyKind = "confirm" | "yesno" | "error" | "prompt" | "timeout";
export type LoginCandidate = { user: string; pass: string; enable: string; label: string };

/** What the console is showing us, which decides the whole procedure. */
export type ConsoleState = "ztp" | "cli" | "unknown";

/**
 * WHICH of [readConsoleState]'s branches produced that verdict: `show zerotouch`
 * answering for itself, the console-text fallback, or nothing at all.
 */
export type ConsoleStateSource = "zerotouch" | "console" | "neither";

/** The verdict, and the evidence for it — cf. [readConsoleState]. */
export type ConsoleStateRead = { state: ConsoleState; from: ConsoleStateSource };

/**
 * How much of the raw "show zerotouch" answer reaches the run log.
 *
 * The answer is a line or two on every train anyone has documented, so this is
 * not expected to bite — it is here because the whole point of printing the
 * output verbatim is to be able to lift it into a fixture, and a log line that
 * silently lost its tail would be worse than no log line at all. When it does
 * bite, the log says so and gives the full length.
 */
const ZTP_RAW_LOG_MAX = 600;

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("provisioning aborted");
}

function clipTail(text: string, n: number): string {
  return text.slice(-n).replace(/\s+/g, " ").trim();
}

/**
 * The index of the LAST match, or -1.
 *
 * `String.search` returns the FIRST, and using it to locate a prompt is a bug
 * this file paid for: the login state machine compares "where is the login
 * prompt" against "where is the password prompt" to decide which stage of EOS's
 * two-step login the console is on, and a buffer that has been through one failed
 * attempt contains BOTH, twice. Taking the first "login:" made a fresh one look
 * older than a stale "Password:", so the script answered the second stage of a
 * login it had never started.
 *
 * Same principle as `rightmostMatch` in serialExpect.ts, and for the same reason:
 * on a console, the most recent marker is the only one that describes the present.
 */
export function lastIndexOfRe(text: string, re: RegExp): number {
  var flags = re.flags.includes("g") ? re.flags : re.flags + "g";
  var scan = new RegExp(re.source, flags);
  var last = -1;
  var m: RegExpExecArray | null;
  while ((m = scan.exec(text)) !== null) {
    last = m.index;
    if (m[0].length === 0) scan.lastIndex++;
  }
  return last;
}

/**
 * Turn a dotted mask (or "/nn", or "nn") into the PREFIX LENGTH EOS wants.
 *
 * The mirror image of the Cisco script's toDottedMask, and the asymmetry is the
 * single most consequential difference between the two platforms' pushes: EOS
 * takes "ip address 192.0.2.5/24" and REFUSES the two-argument dotted form,
 * where IOS does the opposite. A shared helper would have had to guess which
 * device it was talking to.
 *
 * An unreadable input falls back to /24 rather than producing a command the CLI
 * rejects — the same choice the Cisco side makes, for the same reason.
 */
export function toPrefixLen(mask: string): number {
  var trimmed = (mask || "").trim().replace(/^\//, "");
  if (/^\d+$/.test(trimmed)) {
    var n = Number(trimmed);
    if (n >= 0 && n <= 32) return n;
    return 24;
  }
  var m = /^(\d+)\.(\d+)\.(\d+)\.(\d+)$/.exec(trimmed);
  if (!m) return 24;
  var bits = 0;
  var i = 0;
  for (i = 1; i <= 4; i++) {
    var octet = Number(m[i]);
    if (!Number.isFinite(octet) || octet < 0 || octet > 255) return 24;
    while (octet > 0) {
      bits += octet & 1;
      octet >>= 1;
    }
  }
  return bits;
}

/** "vlan 99", "Vlan99", "99" → 99. Null when the field names no VLAN. */
export function parseVlanId(iface: string): number | null {
  var m = /^\s*(?:vlan\s*)?(\d{1,4})\s*$/i.exec(iface || "");
  if (!m) return null;
  var id = Number(m[1]);
  if (!Number.isFinite(id) || id < 1 || id > 4094) return null;
  return id;
}

/**
 * EVERY faceplate port, linked or not, from "show interfaces status".
 *
 * The Status word is the pivot and not the column position: the Name column is
 * free text that may contain spaces, and so may the Type column ("Not Present"),
 * so neither end can be split on whitespace.
 *
 * WHAT IS EXCLUDED, and each exclusion is a port this script must not write to:
 *
 *   - Ma1 / Management1 — the OUT-OF-BAND port. It sits outside the switching
 *     fabric, carries no VLAN, and "switchport mode trunk" on it is a refused
 *     command in the middle of the push;
 *   - Po1 / Port-Channel1 — a logical interface, not a jack;
 *   - Et49/1 — a BREAKOUT LANE. Four of them share a cage, and this script has
 *     no way to name them apart from the cage itself. Skipping them means a
 *     switch with breakouts in service gets those ports left alone, which is a
 *     real gap and the safe one.
 */
export function parseAllPorts(status: string): string[] {
  var re =
    /^(\S+)\s+.*?\b(connected|notconnect|disabled|errdisabled|err-disabled|inactive|suspended|monitoring|unknown)\b/gim;
  var out: string[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(status)) !== null) {
    var n = portNumberOf(m[1]);
    if (n === null) continue;
    var full = "Ethernet" + n;
    if (seen[full]) continue;
    seen[full] = true;
    out.push(full);
  }
  return out;
}

/** Ports whose link is up — "where is the uplink", as opposed to what the
 * switch has. Used only to report what the operator has already patched. */
export function parseUpPorts(status: string): string[] {
  var re =
    /^(\S+)\s+.*?\b(connected|notconnect|disabled|errdisabled|err-disabled|inactive|suspended|monitoring|unknown)\b/gim;
  var out: string[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(status)) !== null) {
    if (m[2].toLowerCase() !== "connected") continue;
    var n = portNumberOf(m[1]);
    if (n === null) continue;
    var full = "Ethernet" + n;
    if (seen[full]) continue;
    seen[full] = true;
    out.push(full);
  }
  return out;
}

/**
 * The port NUMBER of a faceplate interface, or null for anything this script
 * refuses to touch (cf. [parseAllPorts]).
 */
export function portNumberOf(name: string): number | null {
  var m = /^(?:ethernet|eth|et)\s*(\d+)(\/\d+)?$/i.exec((name || "").trim());
  if (!m) return null;
  // A breakout lane. Recognised so that it is declined for the right reason,
  // rather than falling through as "not an interface at all".
  if (m[2]) return null;
  var n = Number(m[1]);
  if (!Number.isFinite(n) || n < 1) return null;
  return n;
}

/**
 * Ports the SWITCH shut by itself, with the cause, from
 * "show interfaces status errdisabled".
 *
 *   Port      Name          Status       Reason
 *   Et50                    errdisabled  xcvr-unsupported
 */
export function parseErrDisabled(status: string): { port: string; reason: string }[] {
  var re = /^(\S+)\s+(?:.*?\s+)?err-?disabled\s+(\S+)/gim;
  var out: { port: string; reason: string }[] = [];
  var seen: Record<string, boolean> = {};
  var m: RegExpExecArray | null;
  while ((m = re.exec(status)) !== null) {
    var n = portNumberOf(m[1]);
    if (n === null) continue;
    var full = "Ethernet" + n;
    if (seen[full]) continue;
    seen[full] = true;
    out.push({ port: full, reason: m[2].toLowerCase() });
  }
  return out;
}

/**
 * Group port numbers into "interface" RANGE expressions.
 *
 * NOT a micro-optimisation. This console runs at 9600 8N1, and every command in
 * [runConfigCmds] waits for its prompt before the next is sent. Trunking a
 * 48-port switch one port at a time is 48 blocks of five commands — around two
 * hundred and fifty round trips, many minutes of console time during which a
 * reset switch sits unconfigured with every port up in VLAN 1. As ranges it is
 * one block.
 *
 * Contiguous runs are merged and the rest are listed, which is what EOS's
 * "interface Ethernet1-24,49-52" syntax takes. The chunking exists because a
 * switch whose numbering has holes — every switch with a breakout cage in
 * service — would otherwise produce one enormous line, and a console line has a
 * length beyond which the CLI stops reading.
 */
export function portRanges(ports: string[], maxRunsPerCmd = 8): string[] {
  var nums: number[] = [];
  var p: string;
  for (p of ports) {
    var n = portNumberOf(p);
    if (n !== null) nums.push(n);
  }
  nums = nums.slice().sort((a, b) => a - b);
  var runs: string[] = [];
  var i = 0;
  while (i < nums.length) {
    var start = nums[i];
    var end = start;
    while (i + 1 < nums.length && nums[i + 1] === end + 1) {
      i++;
      end = nums[i];
    }
    runs.push(start === end ? String(start) : start + "-" + end);
    i++;
  }
  var out: string[] = [];
  var j = 0;
  for (j = 0; j < runs.length; j += maxRunsPerCmd) {
    out.push("Ethernet" + runs.slice(j, j + maxRunsPerCmd).join(","));
  }
  return out;
}

/**
 * Global commands that make EOS accept the fleet's third-party optics.
 *
 * Mirrors internal/driver/arista/eos UnsupportedTransceiverCmds() — the two
 * paths must never drift into two different remediations.
 *
 * TWO commands doing DIFFERENT things, and applying one without the other is
 * half a fix:
 *
 *   - `service unsupported-transceiver` stops EOS refusing the optic at all. It
 *     is what matters for a cage brought up from now on;
 *   - `errdisable recovery cause xcvr-unsupported` arms the automatic recovery
 *     for ports ALREADY sitting err-disabled. Without it, a cage refused before
 *     the first command landed stays dark until somebody bounces it by hand —
 *     which, on a switch being provisioned over its own uplink, means the uplink
 *     never comes back and the probe blames the network.
 *
 * The second token of the first line is a CHECKSUM of the first, not a secret: it
 * is identical on every switch, it authenticates nobody, and it unlocks nothing
 * but the acceptance of optics the fleet already owns.
 *
 * Both live in the running configuration, so the factory reset that precedes this
 * push is exactly what removes them. That is why they are re-applied on every run
 * rather than set once.
 */
export const UNSUPPORTED_TRANSCEIVER_CMDS = [
  "service unsupported-transceiver wiprolabs f5047577",
  "errdisable recovery cause xcvr-unsupported",
];

/**
 * Cap the fan speed, by fleet decision.
 *
 * A data-centre Arista runs its fans for a data-centre inlet temperature, and in
 * a lab or a small comms room that is simply loud. `environment fan-speed
 * override 30` pins them at 30 %.
 *
 * # This one is NOT like the transceiver pair, and the difference is thermal
 *
 * `service unsupported-transceiver` changes what the switch will ACCEPT. This
 * changes what the switch does to keep itself alive, and the vendor's own banner
 * (below) says so plainly: too low a fan speed risks damaging hardware and doing
 * this without their direction can be grounds for voiding the warranty. That is
 * the fleet owner's call and it has been made deliberately — it is recorded here
 * rather than buried so that whoever reads a thermal incident later finds it in
 * one search.
 *
 * The undo is "environment fan-speed auto", and it is worth knowing before it
 * is needed: the override survives a reload (it is in the running configuration
 * and gets saved), so a switch moved from a cool lab into a warm rack keeps the
 * cap it was provisioned with.
 *
 * MEASURED WITH THE CAP IN FORCE (core01, EOS 4.28.13.1M, 2026-08-11, idle):
 * status Ok, ambient 33 °C, hottest sensor 58.5 °C against a 100 °C alert limit,
 * all four system fans steady at 30 %; the PSU fans sit at 34 % and report "FW
 * Override", so the cap does not reach them. Unknown on a LOADED box — that
 * needs traffic. To check it on site the verb is "show system environment
 * temperature" / "… cooling": plain "show environment temperature" is
 * DEPRECATED on this train.
 */
export const FAN_SPEED_OVERRIDE_CMD = "environment fan-speed override 30";
export const FAN_SPEED_AUTO_CMD = "environment fan-speed auto";

/**
 * NO ENABLE PASSWORD — the fleet's universal rule, on every switch.
 *
 * Mirrors internal/driver/arista/eos NoEnableCmds(); the reasoning lives at the
 * call site in [buildPushCmds], with the rest of the provisioning template.
 */
export const NO_ENABLE_CMDS = ["no enable secret"];

/**
 * The banner EOS prints when the override is accepted — CAPTURED 2026-08-06.
 *
 * Worth a pattern for two reasons. First, it must NOT read as a failure: it is
 * four lines of `=` rules around a warranty warning, it contains the word
 * "unsupported", and the command SUCCEEDED. [CLI_ERROR] does not match it (every
 * refusal form there is anchored on a leading "%" or "!"), and this pattern
 * is what proves that on purpose rather than by luck.
 *
 * Second, seeing it is the only confirmation that the override actually took —
 * EOS prints nothing else, and the prompt comes straight back either way.
 */
const FAN_SPEED_WARNING =
  /Overriding the system fan speed is unsupported|voiding your warranty|fan-speed auto/i;

/** The syslog EOS emits when it refuses an optic. */
const UNSUPPORTED_TRANSCEIVER_LOG =
  /%ETH-4-UNSUPPORTED|unsupported\s+transceiver|xcvr-unsupported/i;

/**
 * Collect a show command's output, feeding the pager when "--More--" shows up:
 * Web Serial cannot claim a tall terminal, so output that fits a PuTTY window may
 * still paginate here and swallow every later command.
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

/** After a command: a question to answer, a refusal, or back at #/> . */
async function expectReply(
  session: SerialSession,
  timeoutMs: number,
): Promise<{ kind: ReplyKind; match: string }> {
  try {
    var hit = await session.expectAny(
      [CONFIRM, YESNO, CLI_ERROR, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "yesno", match: hit.match };
    if (hit.index === 2) return { kind: "error", match: hit.match };
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
      [CONFIRM, YESNO, CLI_ERROR, LOGIN_FALLBACK, PROMPT_PRIV, PROMPT_USER],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "confirm", match: hit.match };
    if (hit.index === 1) return { kind: "yesno", match: hit.match };
    if (hit.index === 2) return { kind: "error", match: hit.match };
    if (hit.index === 3) return { kind: "login", match: hit.match };
    var err = session.getCapture().slice(-500).match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
  }
}

/**
 * Which of the three states the console is in.
 *
 * THE FIRST DECISION OF THE WHOLE PROCEDURE, and the one nothing else can
 * recover from. `show zerotouch` is asked FIRST because it is the authoritative
 * answer; the console text is the fallback for a train that does not carry the
 * command, and it is a good fallback precisely because a switch in ZTP will not
 * stop talking about it — the retry loop logs a line every thirty seconds or so.
 *
 * Returns "unknown" rather than guessing when neither says anything: the caller
 * then treats it as a configured CLI, which is the conservative order (a full
 * reset runs, and it lands in ZTP, which the next phase handles). Guessing "ztp"
 * on a configured switch would send `zerotouch cancel` to a device that would
 * reboot into a configuration nobody meant to keep.
 *
 * WHY `from` IS REPORTED ALONGSIDE THE VERDICT. The first lab run detected ZTP
 * correctly and the log said only that — so it proved the verdict right and left
 * the interesting question open: was it `show zerotouch` that answered, or the
 * console-text fallback? The regex above accepts on/enabled/active/armed because
 * the wording has MOVED ACROSS RELEASES and nobody here has a capture from this
 * train; one real answer, printed verbatim in the run log, is what would let it
 * be tightened to what the hardware actually says. Reporting the branch turns the
 * NEXT console run into that capture, instead of a run that once again only
 * confirms the verdict and sends someone back for a second maintenance window.
 * `from` decides nothing — it is evidence, and [detectConsoleState] logs it.
 */
export function readConsoleState(zeroTouchOut: string, consoleTail: string): ConsoleStateRead {
  if (/zerotouch[^\n]*\b(?:on|enabled|active|armed|in progress)\b/i.test(zeroTouchOut)) {
    return { state: "ztp", from: "zerotouch" };
  }
  if (/zerotouch[^\n]*\b(?:off|disabled|inactive|cancelled|canceled)\b/i.test(zeroTouchOut)) {
    return { state: "cli", from: "zerotouch" };
  }
  if (ZTP_MARK.test(consoleTail)) return { state: "ztp", from: "console" };
  return { state: "unknown", from: "neither" };
}

/**
 * Does the console text ALREADY say this switch is in ZTP, before anyone has
 * logged in?
 *
 * ADDED AFTER THE FIRST LAB RUN, which failed for want of exactly this. The
 * console was printing
 *
 *   %ZTP-5-RETRY: Retrying Zero Touch Provisioning from the beginning (attempt 129)
 *
 * every thirty seconds while the script tried the operator's credentials against
 * a switch whose "admin" account has no password, failed twice, and reported
 * "could not re-open a privileged session" — a message that says nothing about
 * the one fact that was on screen the whole time.
 *
 * A switch in ZTP will not stop talking about it, which is what makes this
 * reliable: the retry loop is the platform's own heartbeat. It costs no command
 * and it runs BEFORE the first login, which is the only moment it can change what
 * the login does.
 */
export function consoleLooksLikeZTP(consoleText: string): boolean {
  return ZTP_MARK.test(consoleText);
}

/**
 * Put the factory account first when the console is visibly in ZTP.
 *
 * On such a switch "admin" has NO password, so a candidate carrying one is not
 * merely useless — it is answered with "Login incorrect", which costs a round
 * trip and leaves the console on the second prompt of a two-stage login for the
 * NEXT candidate to trip over. Ordering is the cheap fix; the refusal handling in
 * [ensurePrivileged] is the robust one, and both are wanted.
 *
 * Nothing is REMOVED — a switch that answers ZTP markers while still holding a
 * password (a half-finished earlier run) must still be reachable.
 */
export function orderLoginsForZTP(
  logins: LoginCandidate[],
  looksZTP: boolean,
): LoginCandidate[] {
  if (!looksZTP) return logins;
  var factory = logins.filter((l) => l.pass === "");
  var rest = logins.filter((l) => l.pass !== "");
  return [...factory, ...rest];
}

export async function detectConsoleState(
  session: SerialSession,
  log: SoftResetLog,
): Promise<ConsoleState> {
  var out = await collectShow(session, "show zerotouch", 8000);
  var read = readConsoleState(out, session.getCapture().slice(-4000));
  var state = read.state;
  if (state === "ztp") {
    log(
      "the switch is in ZERO TOUCH PROVISIONING: it answers, it has a prompt, and it " +
        "discards everything written to it at its next cycle. \"zerotouch cancel\" first.",
    );
  } else if (state === "cli") {
    log("ZTP is off — this is an ordinary configured CLI");
  } else {
    log(
      "could not establish the ZTP state (this train may not carry \"show zerotouch\") — " +
        "treating the console as a configured CLI, which is the conservative order",
    );
  }

  // WHICH half of the read fired, and the raw answer it read. Instrumentation
  // only — cf. [readConsoleState]: the verdict above has already been decided and
  // these two lines change nothing about it. They exist so that ONE console run
  // yields the fixture: the branch says whether the wording matched at all, and
  // the verbatim output is the wording itself, ready to be lifted into
  // aristaEosSoftReset.test.ts and used to tighten a regex that today accepts
  // on/enabled/active/armed because no one has a capture from this train.
  log(
    `ZTP state settled by: ${
      read.from === "zerotouch"
        ? "\"show zerotouch\" itself — its answer matched"
        : read.from === "console"
          ? "the CONSOLE-TEXT fallback — \"show zerotouch\" said nothing this script " +
            "recognises, the %ZTP- chatter did"
          : "NEITHER — \"show zerotouch\" said nothing recognisable and the console " +
            "text carries no ZTP marker"
    }`,
  );
  var raw = out.trim();
  log(
    `"show zerotouch" answered, verbatim: ${
      raw ? JSON.stringify(raw.slice(0, ZTP_RAW_LOG_MAX)) : "nothing at all"
    }${
      raw.length > ZTP_RAW_LOG_MAX
        ? ` — TRUNCATED to the first ${ZTP_RAW_LOG_MAX} of ${raw.length} characters`
        : ""
    }`,
  );
  return state;
}

/**
 * Reach the privileged "#" prompt.
 *
 * A switch fresh out of ZTP asks for NOTHING beyond a user name: "admin" with
 * an EMPTY password logs in, and "enable" gives "#" with no password either.
 * A configured one may ask for a password too. All shapes are handled, and the
 * decision is made on the LAST marker seen — a stale "Password:" earlier in the
 * buffer must not make us type the password into a fresh "login:".
 */
export async function ensurePrivileged(
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
  var refusals = 0;
  while (Date.now() < deadline) {
    aborted(signal);
    // FRESH text and the fallback are two different things, and conflating them
    // is what this whole function keeps getting wrong.
    //
    // `fresh` is what arrived since the last mark; `tail` falls back to the
    // rolling capture so a console that has gone quiet can still be read for its
    // CURRENT prompt. That fallback is right for a prompt — a prompt is a state,
    // and it stays true until something changes it — and WRONG for an event.
    // "Login incorrect" is an event: it happened once, and reading it out of
    // the fallback on every quiet pass would count one refusal for ever, which
    // is the same class of stale-buffer mistake that made the first lab run
    // answer a password prompt nobody had caused.
    var fresh = session.getSinceMark();
    var tail = fresh.slice(-800) || session.getCapture().slice(-400);

    // THE REFUSAL, FIRST — before any prompt is looked at, and only when it is NEW.
    //
    // OBSERVED 2026-08-06: the console answered "Login incorrect" and went back
    // to "login:". Without this branch the loop had no notion of a refused
    // credential at all; it kept feeding the same pair into the two-stage prompt
    // and burned the whole expect budget on a switch that had already said no.
    // Give up on THIS candidate quickly so the caller reaches the next one —
    // which, on a switch fresh out of ZTP, is the one with no password at all.
    if (LOGIN_INCORRECT.test(fresh)) {
      refusals++;
      session.mark();
      if (refusals >= 2) {
        throw new Error(
          `the console refused this credential ("Login incorrect") — moving on ` +
            `rather than sitting out the expect timeout`,
        );
      }
      log("console answered \"Login incorrect\" — re-syncing to the login prompt");
      await session.send("");
      await session.sleep(1200);
      continue;
    }

    // LAST occurrence of each, never the first (cf. lastIndexOfRe): a buffer
    // that has been through one failed attempt carries both prompts twice, and
    // only the most recent one describes the present.
    var lastPriv = tail.lastIndexOf("#");
    var lastLogin = lastIndexOfRe(tail, /[\w.\-]*\s*login\s*:|user\s*name\s*:/i);
    var lastPass = tail.toLowerCase().lastIndexOf("password:");
    var lastExec = tail.lastIndexOf(">");

    if (lastPriv >= 0 && lastPriv > lastLogin && lastPriv > lastPass && /#\s*$/.test(tail)) {
      log("already at privileged prompt");
      return;
    }
    if (lastLogin >= 0 && lastLogin > lastPass) {
      log(`login as ${user || "admin (factory, no password)"}`);
      session.mark();
      await session.send(user || "admin");
      var afterUser = await session.expectAny(
        [PASSWORD, PROMPT_PRIV, PROMPT_USER, LOGIN_PROMPT],
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
        log("back at login: — credential refused");
        continue;
      }
      session.mark();
      await session.send(password);
      var afterPass = await session.expectAny(
        [PROMPT_PRIV, PROMPT_USER, LOGIN_PROMPT, PASSWORD],
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
    if (lastPass >= 0 && lastPass > lastLogin) {
      // A "Password:" THIS ATTEMPT DID NOT CAUSE.
      //
      // This is the exact state the first lab run died in. EOS logs in in two
      // stages, so a console left mid-login — by a previous attempt, by the
      // autodetection pass pressing Enter, by an operator — sits on the SECOND
      // prompt with no username behind it. Answering it means posting the
      // password against whatever name is already half-entered, which on a
      // switch fresh out of ZTP is doubly wrong: "admin" there has NO
      // password, so the correct answer is an empty one.
      //
      // So the prompt is CLEARED rather than answered: a bare CR ends the stale
      // attempt, the console returns to "login:", and the loop takes the
      // branch that sends a user name and a password IN ORDER — which is the
      // only sequence whose meaning is unambiguous.
      log("stale \"Password:\" with no login of ours behind it — clearing it to re-sync");
      session.mark();
      await session.send("");
      var afterClear = await session.expectAny(
        [LOGIN_PROMPT, PROMPT_PRIV, PROMPT_USER, PASSWORD],
        15000,
      );
      if (afterClear.index === 1) return;
      if (afterClear.index === 2) {
        await enterEnable(session, enablePassword, log);
        return;
      }
      // Back at "login:" (or still at a password prompt, which the next pass
      // will clear again up to the nudge budget).
      await session.sleep(400);
      continue;
    }
    if (lastExec >= 0 && />\s*$/.test(tail)) {
      await enterEnable(session, enablePassword, log);
      return;
    }

    nudges++;
    if (nudges > 10) {
      throw new Error("no login / password / prompt seen on the console");
    }
    session.mark();
    await session.send("");
    await session.sleep(900);
  }
  throw new Error("could not reach the EOS privileged prompt");
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
 * Erase the startup configuration and reboot.
 *
 * Two facts about this sequence, each of which has bitten somebody:
 *
 *   1. `delete flash:startup-config` asks NOTHING here — no "[confirm]", no
 *      "Delete filename?". The IOS reflex of answering a prompt that never
 *      comes leaves a stray Enter, which EOS reads as an empty command and
 *      ignores. Harmless, and worth knowing when reading a transcript;
 *   2. `reload now` is used rather than `reload`. Plain `reload` asks
 *      "Save? [yes/no]" whenever running and startup differ — which, one
 *      command after deleting the startup file, they ALWAYS do — and the default
 *      answer writes the configuration straight back over the file just removed.
 *      The reset then appears to have done nothing at all.
 *
 * THIS DOES NOT FINISH THE RESET: the switch comes back in ZTP.
 */
async function eraseAndReload(session: SerialSession, log: SoftResetLog, signal?: AbortSignal) {
  aborted(signal);

  log("delete flash:startup-config…");
  session.mark();
  await session.send("delete flash:startup-config");
  var r1 = await expectReply(session, 15000);
  if (r1.kind === "confirm" || r1.kind === "yesno") {
    // Not documented to happen, and answered rather than waited out: a question
    // left standing would take the NEXT command as its answer.
    session.mark();
    await session.sendRaw("\r");
    log("delete: confirmed");
  } else if (r1.kind === "error") {
    // "No such file or directory" on a switch that never had one. Not a failure.
    log(`delete: ${r1.match.trim()} — continuing`);
  }
  await session.sleep(1200);

  log("reload now (\"now\" skips the \"Save?\" question whose default undoes the reset)…");
  session.mark();
  await session.send("reload now");
  var round = 0;
  for (round = 0; round < 4; round++) {
    var hit: { index: number; match: string };
    try {
      hit = await session.expectAny([SAVE_YESNO, CONFIRM, YESNO, CLI_ERROR], 15000);
    } catch {
      log("reload: no confirmation seen — the switch is rebooting (or a power cycle works)");
      return;
    }
    if (hit.index === 0) {
      // Should not appear after "reload now". If it does, the answer is the
      // one that matters most in this whole procedure.
      log("reload: \"Save?\" → no (yes would re-save the configuration just erased)");
      session.mark();
      await session.send("no");
      continue;
    }
    if (hit.index === 1 || hit.index === 2) {
      log("reload: confirmed");
      session.mark();
      await session.sendRaw("\r");
      return;
    }
    log(`reload: CLI error "${hit.match}" — continuing`);
    return;
  }
}

/**
 * End Zero Touch Provisioning.
 *
 * ONE command, and it reboots the switch. `zerotouch cancel` writes an EMPTY
 * startup-config — the file's existence, not its content, is what tells EOS not
 * to run ZTP again — and reloads. A switch that reaches a prompt after this has a
 * blank configuration and no ZTP, which is the state every other range in this
 * product is in the moment it finishes a factory reset.
 */
async function zeroTouchCancel(session: SerialSession, log: SoftResetLog, signal?: AbortSignal) {
  aborted(signal);
  log("zerotouch cancel (writes an empty startup-config and reboots)…");
  session.mark();
  await session.send("zerotouch cancel");
  var round = 0;
  for (round = 0; round < 3; round++) {
    var hit: { index: number; match: string };
    try {
      hit = await session.expectAny([CONFIRM, YESNO, CLI_ERROR], 15000);
    } catch {
      log("zerotouch cancel: no question asked — the switch is rebooting");
      return;
    }
    if (hit.index === 2) {
      throw new Error(
        `"zerotouch cancel" was refused (${hit.match.trim()}) — the switch cannot be ` +
          `taken out of ZTP from here, and anything written to it would be discarded`,
      );
    }
    session.mark();
    await session.sendRaw("\r");
  }
}

/**
 * Wait for the console to come back after a reload.
 *
 * The minimum boot wait is deliberately long on this platform: an Arista goes
 * through Aboot and then a full Linux userspace, and the console is CHATTY the
 * whole way — agent startup lines, "[ OK ]" banners, interface events. A script
 * that started looking for a prompt immediately would find a "#" inside that
 * noise and start configuring a switch that is still bringing its agents up.
 */
async function waitConsoleAfterReboot(
  session: SerialSession,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  // Drop ALL pre-reboot text: a stale "#" would make us declare the switch back
  // and push configuration while Aboot is still running.
  session.mark();
  log("waiting for the switch to come back (an Arista takes 2–4 min)…");
  var minBootMs = 90000;
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
      log(
        `rebooting (${Math.round((bootWaitUntil - Date.now()) / 1000)}s before looking for a prompt)…`,
      );
      lastProgress = Date.now();
    }
  }

  session.mark();
  log("looking for the post-reboot console (new data only)…");
  lastProgress = Date.now();
  var lastNudge = 0;

  while (Date.now() < deadline) {
    aborted(signal);
    try {
      await session.expectAny([LOGIN_PROMPT, PASSWORD, PROMPT_USER, PROMPT_PRIV],
        Math.min(10000, deadline - Date.now()));
      log("post-reboot console reached");
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
 * Questions are answered, refusals are logged with the tail that says which word
 * the caret rejected, and a fallback to login triggers re-login + "configure"
 * + a retry of the same command.
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
    // "configure" and not "configure terminal": EOS accepts both, and the
    // short form is the one this platform's own prompts and documentation use.
    // The distinction matters only in a transcript, which is exactly where it
    // matters — an operator pasting these lines back should be pasting EOS.
    await session.send("configure");
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
        throw new Error(`console session lost repeatedly (last at "${label}")`);
      }
      log(`console fell back to login at "${label}" — re-login and retry…`);
      await reloginAny(session, logins, log, signal);
      await enterConfig();
      continue;
    }
    if (reply.kind === "confirm" || reply.kind === "yesno") {
      session.mark();
      await session.sendRaw("\r");
      log(`${label}: confirmed`);
    } else if (reply.kind === "error") {
      log(
        `${label}: CLI error "${reply.match}" — continuing (${clipTail(session.getSinceMark(), 160)})`,
      );
    } else if (reply.kind === "prompt") {
      log(`${label}: ok`);
    } else {
      log(`${label}: no prompt reply — continuing`);
    }
    // A banner is not a refusal, and this one has to be REPEATED rather than
    // swallowed: "environment fan-speed override" answers with four lines of
    // warranty warning and nothing else, so the banner is both the only proof
    // the override took AND a fact the fleet owner chose to accept. Surfacing it
    // in the run log is what puts it where a thermal incident would be
    // investigated later. It is deliberately NOT matched by CLI_ERROR, whose
    // every refusal form is anchored on a leading "%" or "!".
    if (FAN_SPEED_WARNING.test(session.getSinceMark())) {
      log(
        `${label}: the switch printed its fan-speed warning — the override is in ` +
          `force. Vendor's words: overriding the fan speed is unsupported, too low ` +
          `a setting risks damaging hardware, and doing it without their direction ` +
          `can be grounds for voiding the warranty. Undo with "${FAN_SPEED_AUTO_CMD}"`,
      );
    }
    // Let the device finish printing its prompt: matching fires on the FIRST
    // characters, and sending the next command while the rest is still streaming
    // shifts every reply by one.
    await session.sleep(250);
    i++;
  }
  session.mark();
  await session.send("end");
  await session.sleep(500);
}

/**
 * "write memory", answering whatever it asks.
 *
 * A failed save throws: a management configuration that does not survive a reboot
 * is worse than a refused one, because the operator believes it is in place — and
 * on this platform an empty startup-config is also what keeps ZTP from coming
 * back, so a switch left unsaved is a switch that reboots into the retry loop.
 */
export async function saveConfig(
  session: SerialSession,
  log: SoftResetLog,
  logins?: LoginCandidate[],
  // Overridable so the tests can exercise the SILENCE path without sitting out
  // a real 45-second window on every `make test`. Production never passes it.
  opts?: { quietWindowMs?: number; totalBudgetMs?: number },
): Promise<void> {
  var quietWindowMs = opts?.quietWindowMs ?? SAVE_QUIET_WINDOW_MS;
  var totalBudgetMs = opts?.totalBudgetMs ?? SAVE_TOTAL_BUDGET_MS;
  log(
    `write memory… (this platform can take a while: ${Math.round(quietWindowMs / 1000)}s ` +
      `of silence is normal here, and the command is NOT re-sent while it runs)`,
  );
  var startedAt = Date.now();
  session.mark();
  await session.send("write memory");
  var deadline = startedAt + totalBudgetMs;
  var relogins = 0;
  var silences = 0;
  var sawOK = false;
  while (Date.now() < deadline) {
    try {
      await session.expectAny(
        [SAVE_OK, CONFIRM, YESNO, CLI_ERROR, LOGIN_FALLBACK, PROMPT_PRIV],
        quietWindowMs,
      );
    } catch {
      // "write memory" IS SLOW HERE, and that is the whole story.
      //
      // MEASURED 2026-08-06 on the lab switch: 74 SECONDS between the command
      // and "Copy completed successfully.", with the console emitting nothing
      // whatsoever in between. Not a lost session, not an unanswered prompt —
      // the device was busy writing, and this platform says nothing while it is.
      //
      // The first version of this loop got that exactly wrong in two ways, and
      // the second was the dangerous one:
      //
      //   - it gave the save a 20 s window, so a normal save "failed" three
      //     times before succeeding;
      //   - on each of those it RE-SENT "write memory" while the first was
      //     still running, queueing duplicate saves onto a device that was
      //     already busy. That is how you turn a slow operation into a slower
      //     one, and on a flash-backed configuration it is not a harmless thing
      //     to do.
      //
      // So: one long window, and a nudge that is a BARE CR and nothing else. A
      // CR costs nothing if the device is merely busy, and it answers a prompt
      // this script does not recognise if there turns out to be one — which is
      // the only other hypothesis the capture cannot yet rule out. The command
      // itself is re-sent ONLY when the session is genuinely gone (a login
      // prompt), which is a different situation entirely.
      silences++;
      var waitedS = Math.round((Date.now() - startedAt) / 1000);
      if (silences > SAVE_MAX_SILENCES) {
        throw new Error(
          `save: no completion marker after ${waitedS}s — THE CONFIGURATION IS IN ` +
            `THE RUNNING CONFIG ONLY and a reload would lose it. Log in on the ` +
            `console and run "write memory" by hand before power-cycling this switch`,
        );
      }
      // What DID arrive during the silence, if anything. This is the line that
      // will settle "busy" versus "waiting on something we do not recognise" on
      // the next run, because today's capture cannot.
      var quiet = session.getSinceMark();
      log(
        `save: still nothing after ${waitedS}s (${silences}/${SAVE_MAX_SILENCES}) — ` +
          `sending a bare CR, NOT another "write memory". Received meanwhile: ` +
          `${quiet.trim() ? JSON.stringify(clipTail(quiet, 200)) : "nothing at all"}`,
      );
      await session.sendRaw("\r");
      await session.sleep(1500);
      if (LOGIN_FALLBACK.test(session.getSinceMark()) && logins) {
        log("save: the console is back at login — re-opening the session and re-sending…");
        await reloginAny(session, logins, log);
        session.mark();
        await session.send("write memory");
      }
      continue;
    }
    // expectAny reports the RIGHTMOST pattern, and the prompt is always last on
    // the line — "Copy completed successfully\r\nsw1#" would be read as
    // "prompt, no marker". So the burst is examined as a whole rather than
    // through the winning match.
    var seen = session.getSinceMark();
    var refusal = seen.match(CLI_ERROR);
    if (refusal) {
      throw new Error(`save failed: "${refusal[0].trim()}"`);
    }
    if (SAVE_OK.test(seen)) {
      sawOK = true;
    }
    if (LOGIN_FALLBACK.test(seen) && !sawOK) {
      relogins++;
      if (!logins || relogins > 2) {
        throw new Error("save: console session lost before the configuration was written");
      }
      log("save: console fell back to login — re-login and retry…");
      await reloginAny(session, logins, log);
      session.mark();
      await session.send("write memory");
      startedAt = Date.now();
      continue;
    }
    if (sawOK) {
      // The duration is logged deliberately: it is the number that tells the
      // next person whether 74 s was this box on that day, or what this platform
      // simply costs. Nothing else in the run measures it.
      log(
        `save: written to the startup configuration (took ${Math.round(
          (Date.now() - startedAt) / 1000,
        )}s)`,
      );
      return;
    }
    if (CONFIRM.test(seen.slice(-200)) || YESNO.test(seen.slice(-200))) {
      session.mark();
      await session.sendRaw("\r");
      continue;
    }
    if (PROMPT_PRIV.test(seen.slice(-200))) {
      throw new Error("save returned to the prompt without a completion marker");
    }
  }
  throw new Error("save: timed out");
}

/**
 * The allowed VLAN list every provisioned trunk carries.
 *
 * VLAN 1 IS IN THE LIST DELIBERATELY, and leaving it out is the mistake this
 * function exists to avoid: EOS drops native-VLAN traffic on a trunk whose
 * allowed list does not contain the native VLAN, so on a site whose management
 * VLAN is 1 — the common small deployment — a list built from the site VLANs
 * alone produces a trunk that looks perfectly configured and forwards nothing at
 * all, including the management traffic this run is about to depend on.
 */
// VLAN 1 is here because it is the NATIVE VLAN — the platform default, which the
// push deliberately leaves alone (cf. buildPushCmds) — and EOS drops native
// traffic on a trunk whose allowed list does not contain the native VLAN. The
// management VLAN is in the list as an ordinary TAGGED member, exactly like every
// site VLAN beside it.
export function allowedVlanList(
  siteVlans: { id: number }[] | undefined,
  mgmtVlan: number,
): number[] {
  var seen: Record<number, boolean> = { 1: true };
  var out: number[] = [1];
  var add = (id: number) => {
    if (!Number.isFinite(id) || id < 1 || id > 4094 || seen[id]) return;
    seen[id] = true;
    out.push(id);
  };
  var v: { id: number };
  for (v of siteVlans || []) add(v.id);
  add(mgmtVlan);
  return out.sort((a, b) => a - b);
}

/**
 * The whole management push, as an ordered command list.
 *
 * # THE FLEET TEMPLATE FOR THIS PLATFORM
 *
 * What every Arista provisioned by meshcraft comes out looking like. This list
 * and internal/driver/arista/eos MgmtPushCmds() build from the same rules, so a
 * switch adopted at the console and one adopted over the network are the same
 * switch:
 *
 *   no enable password    every account is already privileged; see NO_ENABLE_CMDS
 *   admin account         username U privilege 15 role network-admin secret 0 S
 *   management            SVI on the site's VLAN, "ip address A.B.C.D/NN"
 *   management VLAN       TAGGED on every trunk — never native
 *   native VLAN           left at the platform default (1)
 *   trunks                every faceplate port, allowed = site VLANs + 1 + mgmt
 *   default route         ip route 0.0.0.0/0 <gw>  (there is no ip default-gateway)
 *   third-party optics    accepted, with errdisable recovery armed
 *   fan speed             capped at 30 %           (fleet decision)
 *   LLDP                  on (already is by default; written anyway)
 *   SSH                   on and not shut (already is; written anyway)
 *
 * The two lines that are FLEET POLICY rather than platform fact — the fan cap and
 * the absent enable password — carry their reasoning where they are built,
 * because those are the two a future operator is most likely to revisit.
 *
 * # The ORDER is load-bearing and is not a style choice:
 *
 *   1. THE OPTICS FIRST, before anything that depends on a link existing. EOS
 *      shuts a cage holding an optic it does not accept, and such a port is
 *      indistinguishable from an empty one — so the uplink carries nothing and
 *      the probe blames the network;
 *   2. every VLAN exists before any port is told to carry it, and before the SVI
 *      that serves the management one — an SVI on a VLAN that does not exist
 *      never comes up;
 *   3. the trunks come before the SVI's address, so the moment management has an
 *      address there is already a path for it to answer on;
 *   4. CREDENTIALS LAST — the console session can drop right at the password
 *      change of the account currently logged in, and everything else is then
 *      already in place.
 */
export function buildPushCmds(opts: {
  mgmtVlan: number;
  mgmtVlanName?: string;
  ip: string;
  prefixLen: number;
  gateway?: string;
  hostname?: string;
  siteVlans?: { id: number; name?: string }[];
  trunkPorts: string[];
  user: string;
  password: string;
  /**
   * Where a REFUSED system name is reported. This is the whole reason a pure
   * command builder takes a logger: `hostnameFromDeviceName` refuses a name it
   * cannot normalize, and a builder returning only an array of commands could
   * honour that refusal only by dropping the line in silence — which is the
   * mangling doctrine wearing a different hat. The Go mirror (MgmtPushCmds)
   * carries the same reason out through its error return.
   *
   * Optional so the pure-ordering tests can call this without one.
   */
  log?: SoftResetLog;
}): { cmd: string; label?: string; timeoutMs?: number }[] {
  var out: { cmd: string; label?: string; timeoutMs?: number }[] = [];
  var push = (cmd: string, label?: string, timeoutMs?: number) =>
    out.push({ cmd, label, timeoutMs });

  // 1. Optics.
  var t: string;
  for (t of UNSUPPORTED_TRANSCEIVER_CMDS) push(t);

  // Fan speed, by fleet decision (cf. FAN_SPEED_OVERRIDE_CMD). It sits here
  // because it belongs with the other "make this hardware usable in OUR racks"
  // line and because it answers with a four-line warranty banner: getting it out
  // of the way before the VLAN work keeps that banner from landing in the middle
  // of the block an operator will actually read.
  push(FAN_SPEED_OVERRIDE_CMD, FAN_SPEED_OVERRIDE_CMD, 20000);

  // The system name, through the ONE shared normalizer (./hostname.ts), which
  // mirrors internal/driver/hostname.go — the same function the reconciliation
  // loop uses. The local `sanitizeHostname` that stood here was the least wrong
  // of the four scripts (it mapped `_` to `-` and it trimmed) and it was still
  // wrong where it counts: it DROPPED silently, so "Switch étage 1" reached the
  // console banner as `Switch-tage-1`, and "!!!" reached it as nothing at all
  // while the caller believed a name had been set.
  //
  // A REFUSAL IS REPORTED AND THE REST OF THE PUSH CONTINUES. Aborting the run
  // over a system name would leave a factory-reset switch with no management
  // address at all; the honest outcome is a reachable switch still carrying its
  // factory name, with the reason on screen.
  if (opts.hostname?.trim()) {
    var verdict = hostnameFromDeviceName(opts.hostname);
    if (verdict.hostname) {
      push("hostname " + verdict.hostname);
    } else {
      opts.log?.(
        `the system name was NOT written — ${verdict.reason}. The switch keeps the ` +
          `name it has; nothing else in this push is affected`,
      );
    }
  }
  // LLDP is ON by default on EOS — unlike IOS, where its absence leaves a hole in
  // the topology exactly where the switch is. The line costs one command and
  // brings back a switch somebody had turned it off on; provisioning is the only
  // pass that visits every device.
  push("lldp run");

  // 2. VLANs, before anything references them. VLAN 1 always exists and must not
  //    be re-declared — creating it would be the one line in this push that
  //    modifies a VLAN the operator never declared.
  var wanted = mergeSiteVlans(opts.siteVlans, opts.mgmtVlan, opts.mgmtVlanName);
  var v: { id: number; name?: string };
  for (v of wanted) {
    push(`vlan ${v.id}`);
    if (v.name) push("name " + sanitizeVlanName(v.name));
    push("exit");
  }

  // 3. Trunks: every faceplate port, carrying every VLAN the site declares, with
  //    the management VLAN as native.
  var allowed = allowedVlanList(opts.siteVlans, opts.mgmtVlan).join(",");
  var range: string;
  for (range of portRanges(opts.trunkPorts)) {
    push("interface " + range, `interface ${range}`, 20000);
    push("switchport mode trunk");
    // The SET form, not "add": it is the only one that RESTRICTS, and on EOS a
    // trunk with no allowed line already carries every VLAN there is — "add" on
    // such a trunk changes nothing and reports success.
    push(`switchport trunk allowed vlan ${allowed}`, `switchport trunk allowed vlan ${allowed}`, 20000);
    // NO "switchport trunk native vlan". THE MANAGEMENT VLAN IS TAGGED LIKE
    // ANY OTHER, and this is a design decision, not an omission.
    //
    // An earlier version of this push set the management VLAN as the trunk's
    // native one, reasoning that it made the switch reachable from an upstream
    // port that had not been told about tagging yet. That is a convenience for
    // the ten minutes of provisioning, paid for permanently by the design:
    //
    //   - native means UNTAGGED. An upstream expecting VLAN 99 tagged — which is
    //     what a correctly configured neighbour does — sees untagged frames and
    //     drops them, so the setting breaks exactly the sites that got their own
    //     configuration right;
    //   - it puts the administration VLAN on the one part of a trunk that
    //     carries no tag, which is the surface every VLAN-hopping technique aims
    //     at. The management VLAN is the last one to volunteer for it.
    //
    // The native VLAN is therefore left at the platform default (1), which is
    // what a switch coming out of the factory reset above already carries. VLAN 1
    // stays in the allowed list for that reason and only that reason — EOS drops
    // native traffic on a trunk whose allowed list omits the native VLAN (cf.
    // allowedVlanList).
    push("exit");
  }

  // 4. The management SVI. PREFIX form — the dotted mask IOS wants is refused
  //    here, and this is the line where that difference actually bites.
  push(`interface Vlan${opts.mgmtVlan}`);
  push(`ip address ${opts.ip}/${opts.prefixLen}`);
  // An SVI created in configuration mode comes up administratively down.
  push("no shutdown");
  push("exit");

  var gw = (opts.gateway || "").trim();
  if (gw) {
    // NOT "ip default-gateway": that command does not exist on EOS. The default
    // route is a real route, in the default VRF.
    push(`ip route 0.0.0.0/0 ${gw}`);
  }

  // SSH: already running, already keyed — there is no "crypto key generate"
  // step on this platform and no non-crypto image. All that is left is to be sure
  // the service is not administratively down, which it is not on a factory switch
  // and which one operator in the past will have shut.
  push("management ssh");
  push("no shutdown");
  push("exit");

  // NO gNMI BLOCK, and its absence is deliberate rather than an omission.
  //
  // This script mirrors MgmtPushCmds line for line, and that builder writes
  // "management api gnmi" only when a caller sets MgmtOpts.GNMI — which
  // provisioning never does. The reason is in
  // internal/driver/arista/eos/gnmi.go: the transport it would open is PLAINTEXT
  // gRPC carrying the very network-admin password written a few lines below.
  //
  // When that changes, the block goes EXACTLY HERE — with the other
  // management-plane service, before the credentials, for the same reason
  // everything else in this push is ordered that way: a console session can drop
  // at the password change, and whatever comes after it is what a half-finished
  // switch will be missing.

  // 5. Credentials last.
  if (opts.user && opts.password) {
    // "secret" and not "password": the second stores the value reversibly in
    // the configuration. `role network-admin` is what EOS authorisation actually
    // consults — `privilege 15` alone leaves the account able to log in and
    // unable to configure anything.
    push(
      `username ${opts.user} privilege 15 role network-admin secret 0 ${opts.password}`,
      `username ${opts.user} privilege 15 role network-admin secret 0 •••`,
    );
  }

  // NO ENABLE PASSWORD — the fleet's universal rule, on every switch and every
  // platform, and APPLIED rather than merely omitted: not writing it would leave
  // the rule true on a factory switch and false on every switch that already had
  // one, which is exactly the fleet where it matters. "no enable secret" is
  // idempotent on a device carrying none.
  //
  // It is not a downgrade. The account written just above carries `privilege 15`
  // and `role network-admin`, so it lands on "#" at login and never traverses
  // an enable prompt; a second secret would guard a door the administrator is
  // already through, while adding one more thing to rotate and to get out of step.
  // The login password is untouched and is what actually protects the device.
  //
  // The script still ANSWERS an enable prompt on a switch meshcraft did not
  // provision (cf. enterEnable) — enforcing the rule on write while refusing to
  // cope with it on read would lock this script out of the very devices it exists
  // to take over.
  //
  // LAST, after the account: at no point in this block is the device without a
  // privileged way in.
  var noEnable: string;
  for (noEnable of NO_ENABLE_CMDS) push(noEnable);
  return out;
}

/** VLAN 1 dropped, management VLAN folded in without duplicating a site entry. */
export function mergeSiteVlans(
  siteVlans: { id: number; name?: string }[] | undefined,
  mgmtVlan: number,
  mgmtName?: string,
): { id: number; name?: string }[] {
  var out: { id: number; name?: string }[] = [];
  var seen: Record<number, boolean> = {};
  var v: { id: number; name?: string };
  for (v of siteVlans || []) {
    if (!Number.isFinite(v.id) || v.id < 1 || v.id > 4094 || v.id === 1 || seen[v.id]) continue;
    seen[v.id] = true;
    out.push({ id: v.id, name: v.name });
  }
  if (mgmtVlan > 1 && !seen[mgmtVlan]) {
    out.push({ id: mgmtVlan, name: (mgmtName || "").trim() || `LAN${mgmtVlan}_Management` });
  }
  return out.sort((a, b) => a.id - b.id);
}

/** EOS refuses a space in a VLAN name outright, so spaces become underscores. */
export function sanitizeVlanName(s: string): string {
  var out = (s || "").replace(/[^A-Za-z0-9_ -]/g, "").replace(/[ -]/g, "_");
  if (!out) return "Management";
  return out.slice(0, 32);
}

// There is no sanitizeHostname here any more, and its removal is not tidying.
// It was exported, and nothing outside this file's own test ever imported it.
// See `hostnameFromDeviceName` in ./hostname.ts, which mirrors the Go authority
// and REFUSES a name it cannot normalize instead of quietly deleting characters
// out of it. Do not re-introduce a local one: the four-way disagreement that
// module exists to end started as exactly one such function, per script, each
// reasonable on its own.

/**
 * Bring the third-party optics setting in BEFORE anything reads the faceplate.
 *
 * Order matters more than it looks: a cage EOS has refused reads exactly like an
 * empty one, so a port list taken before these two lines land is a port list with
 * the uplinks missing. Then the ports already sitting err-disabled are bounced,
 * because enabling the recovery does not clear the ports already in that state —
 * it only arms the timer for the future.
 */
export async function acceptThirdPartyOptics(
  session: SerialSession,
  log: SoftResetLog,
  logins: LoginCandidate[],
  signal?: AbortSignal,
): Promise<void> {
  log("accepting third-party optics (a refused cage reads exactly like an empty one)…");
  await runConfigCmds(
    session,
    log,
    logins,
    UNSUPPORTED_TRANSCEIVER_CMDS.map((cmd) => ({ cmd })),
    signal,
  );

  var status = await collectShow(session, "show interfaces status errdisabled", 12000);
  var shut = parseErrDisabled(status).filter((p) => p.reason === "xcvr-unsupported");
  if (shut.length === 0) {
    if (UNSUPPORTED_TRANSCEIVER_LOG.test(session.getCapture().slice(-4000))) {
      log(
        "the console mentioned a refused transceiver but no port reports it now — the " +
          "setting above is what makes that stick",
      );
    }
    return;
  }
  log(
    `bouncing ${shut.length} port(s) the switch had shut for an unsupported transceiver ` +
      `(${shut.map((p) => p.port).join(", ")}) — arming the recovery does not clear a port ` +
      `already in that state`,
  );
  var p: { port: string; reason: string };
  var cmds: { cmd: string; label?: string }[] = [];
  for (p of shut) {
    cmds.push({ cmd: "interface " + p.port });
    cmds.push({ cmd: "shutdown" });
    cmds.push({ cmd: "no shutdown" });
    cmds.push({ cmd: "exit" });
  }
  await runConfigCmds(session, log, logins, cmds, signal);
}

/** Run show commands on the console and log a short snippet (sanity before the IP probe). */
async function verifyOnSerial(
  session: SerialSession,
  opts: {
    ip: string;
    mgmtVlan: number;
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

  log("serial verify: show management ssh…");
  var ssh = await collectShow(session, "show management ssh", 8000);
  if (/enabled|running|Administratively\s+up/i.test(ssh) && !/shutdown|disabled/i.test(ssh)) {
    log("serial verify: SSH enabled");
  } else {
    log(
      `serial verify: SSH does not clearly report as enabled — meshcraft adopts over SSH, ` +
        `so this is the door that matters:\n${ssh.trim().slice(-300)}`,
    );
  }

  // ZTP, one last time. If it is still armed, everything written above is about
  // to be discarded — and that is worth finding out on the console rather than
  // from a switch that comes back factory-fresh in ten minutes.
  var ztp = await collectShow(session, "show zerotouch", 8000);
  if (readConsoleState(ztp, "").state === "ztp") {
    throw new Error(
      "the switch STILL reports Zero Touch Provisioning as armed after the push: " +
        "everything just written will be discarded at its next cycle. Run \"zerotouch " +
        "cancel\" on the console and re-run provisioning",
    );
  }

  if (opts.gateway?.trim()) {
    log(`serial verify: ping ${opts.gateway.trim()}…`);
    var ping = await collectShow(session, `ping ${opts.gateway.trim()}`, 15000);
    if (/(\d+)\s+received/i.test(ping) && !/\b0\s+received/i.test(ping)) {
      log("serial verify: gateway ping ok");
    } else {
      log(`serial verify: ping output:\n${ping.trim().slice(-400)}`);
    }
  }
}

/**
 * Full provisioning, until the management CLI is configured and verified on the
 * console. Network reachability is probed by the caller afterwards.
 */
export async function runAristaEosSoftReset(input: AristaEosSoftResetInput): Promise<void> {
  var session = input.session;
  var log = input.log;
  var iface = (input.mgmtIface || "vlan 1").trim() || "vlan 1";

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
  var mgmtVlan = parseVlanId(iface);
  if (mgmtVlan === null) {
    throw new Error(
      `management interface "${iface}" is not a VLAN: on an Arista the address goes on ` +
        `an SVI, so this must read "vlan N". Management1 exists and is an OUT-OF-BAND ` +
        `port on its own cable — provisioning does not put the fabric's management ` +
        `address there`,
    );
  }
  var prefixLen = toPrefixLen(input.mgmtMask);
  var targetUser = (input.targetUser || "admin").trim() || "admin";

  // The device may hold ANY of these: what the operator typed, the instance
  // credential (an earlier run pushed it without saving), or nothing at all —
  // which is what a switch fresh out of ZTP holds.
  var initialLogins: LoginCandidate[] = [
    {
      user: input.currentUser || "",
      pass: input.currentPassword ?? "",
      enable: input.enablePassword ?? "",
      label: `current serial credentials (${input.currentUser || "none"})`,
    },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "admin", pass: "", enable: "", label: "factory console (admin, no password)" },
  ];
  var seen: Record<string, boolean> = {};
  initialLogins = initialLogins.filter((l) => {
    var key = `${l.user} ${l.pass}`;
    if (seen[key]) return false;
    seen[key] = true;
    return true;
  });

  // LOOK AT THE CONSOLE BEFORE TYPING AT IT. A switch in ZTP announces itself
  // every thirty seconds, and knowing it here decides which credential goes
  // first — on such a switch "admin" has no password at all.
  session.mark();
  await session.send("");
  await session.sleep(1500);
  var preLoginText = session.getCapture().slice(-6000);
  var looksZTP = consoleLooksLikeZTP(preLoginText);
  if (looksZTP) {
    log(
      "the console is printing Zero Touch Provisioning lines: this switch has no " +
        "startup-config, so \"admin\" logs in with NO password — trying that first",
    );
    initialLogins = orderLoginsForZTP(initialLogins, true);
  }

  log("logging in on the console…");
  try {
    await reloginAny(session, initialLogins, log, input.signal);
  } catch (e) {
    // Say what was on screen. A bare "could not open a session" sends an
    // operator to the cable; naming ZTP sends them to the right command.
    if (consoleLooksLikeZTP(session.getCapture().slice(-6000))) {
      throw new Error(
        `no credential opened the console, and the console is printing Zero Touch ` +
          `Provisioning lines — on a switch in that state "admin" has NO password. ` +
          `Clear the credential fields in the wizard and re-run, or log in by hand ` +
          `(admin, empty password) and run "zerotouch cancel". Underlying failure: ` +
          `${(e as Error).message || e}`,
      );
    }
    throw e;
  }

  session.mark();
  await session.send("terminal length 0");
  await expectReply(session, 5000);

  // THE DECISION THIS WHOLE PROCEDURE TURNS ON.
  var state = await detectConsoleState(session, log);

  if (state !== "ztp") {
    // A switch that still holds a configuration. Erasing it lands the switch in
    // ZTP, which is the state the next block handles — the reset is not over when
    // the switch comes back.
    await eraseAndReload(session, log, input.signal);
    await waitConsoleAfterReboot(session, log, 420000, input.signal);
    session.clearCapture();

    log("re-login after the erase (the switch should now be in ZTP: admin, no password)…");
    await reloginAny(
      session,
      [
        { user: "admin", pass: "", enable: "", label: "factory console (admin, no password)" },
        ...initialLogins,
      ],
      log,
      input.signal,
    );
    session.mark();
    await session.send("terminal length 0");
    await expectReply(session, 5000);
  }

  // Out of ZTP, whichever way we got here. On a switch that was already in it
  // this is the only reboot; on one that held a configuration it is the second.
  await zeroTouchCancel(session, log, input.signal);
  await waitConsoleAfterReboot(session, log, 420000, input.signal);

  // Fresh buffer for the blank console — never inherit a pre-reboot "#".
  session.clearCapture();

  log("blank console login (a switch out of ZTP asks for nothing but a name)…");
  var factoryLogins: LoginCandidate[] = [
    { user: "admin", pass: "", enable: "", label: "factory console (admin, no password)" },
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    {
      user: input.currentUser || "",
      pass: input.currentPassword ?? "",
      enable: input.enablePassword ?? "",
      label: `credentials entered in the wizard (${input.currentUser || "none"})`,
    },
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
    throw new Error("could not reach the privileged prompt after leaving ZTP");
  }

  session.mark();
  await session.send("terminal length 0");
  await expectReply(session, 5000);

  // If the console drops mid-push, the instance credential is the one most likely
  // in force (the drop happens right at the secret change).
  var pushLogins: LoginCandidate[] = [
    {
      user: targetUser,
      pass: input.targetPassword,
      enable: input.targetPassword,
      label: `instance credential ${targetUser}`,
    },
    { user: "admin", pass: "", enable: "", label: "factory console (admin, no password)" },
  ];

  // The optics BEFORE the faceplate is read: a refused cage reads exactly like an
  // empty one, so a port list taken now would be missing the uplinks.
  await acceptThirdPartyOptics(session, log, pushLogins, input.signal);

  log("reading the faceplate…");
  var status = await collectShow(session, "show interfaces status", 20000);
  var allPorts = parseAllPorts(status);
  var upPorts = parseUpPorts(status);
  if (allPorts.length === 0) {
    throw new Error(
      "no faceplate port could be read from \"show interfaces status\": the trunks " +
        "cannot be configured without knowing which ports exist, and inventing a port " +
        "list would send refused commands into the middle of the push",
    );
  }
  log(
    `faceplate: ${allPorts.length} port(s), ${upPorts.length} with a link` +
      (upPorts.length ? ` (${upPorts.join(", ")})` : ""),
  );
  log(
    `every faceplate port will be configured as a trunk carrying the site's VLANs, with ` +
      `VLAN ${mgmtVlan} TAGGED like any other and the native VLAN left at the platform ` +
      `default. A port profile applied later narrows any of them to access in one apply; ` +
      `a port left at the factory default could not carry anything until somebody noticed`,
  );

  var cmds = buildPushCmds({
    mgmtVlan,
    ip: input.mgmtIp.trim(),
    prefixLen,
    gateway: input.gateway,
    hostname: input.hostname,
    siteVlans: input.siteVlans,
    trunkPorts: allPorts,
    user: targetUser,
    password: input.targetPassword,
    // A system name that cannot be normalized is refused, and the refusal has to
    // reach the operator: this builder is pure, so the run log is passed in.
    log,
  });

  log("pushing management configuration (each command verified)…");
  await runConfigCmds(session, log, pushLogins, cmds, input.signal);
  await saveConfig(session, log, pushLogins);
  log("management configuration pushed and saved");

  await verifyOnSerial(session, {
    ip: input.mgmtIp.trim(),
    mgmtVlan,
    gateway: input.gateway,
    log,
    logins: pushLogins,
    signal: input.signal,
  });

  log(
    `serial side done — login is now ${targetUser} / <instance credential secret> (enable ` +
      `the same); caller will probe the management IP`,
  );
}

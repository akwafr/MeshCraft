/**
 * Juniper EX (Junos) provisioning over Web Serial: factory zeroize, the exit
 * from Zero Touch Provisioning, and the initial management push, until the
 * switch answers on the LAN.
 *
 * Sibling of aristaEosSoftReset.ts / ciscoIosSoftReset.ts and, like them,
 * DELIBERATELY NOT a generalization of either. Junos disagrees with the
 * IOS-shaped world at the level of the prompt characters themselves, which is
 * the one kind of difference a shared console script cannot survive.
 *
 * # Status of what is written here
 *
 * DOCUMENTED, NOT CAPTURED. Nobody on this project has run this against a real
 * EX. Every verbatim string below carries its source in a comment; the expect
 * timeouts are deliberately generous so that a first live run produces a
 * readable transcript rather than a timeout, and every regex is loose where the
 * wording is known to have moved across Junos trains.
 *
 * # THE THREE PROMPT CHARACTERS, AND WHY AN IOS REFLEX IS DANGEROUS HERE
 *
 *   `%`  the UNIX shell. A factory EX logs `root` straight into it. Typing
 *        Junos commands here does nothing useful — `cli` is the way in.
 *   `>`  OPERATIONAL mode (Cisco's "enable" level). `request`, `show`, `ping`.
 *   `#`  CONFIGURATION mode. On IOS `#` means PRIVILEGED; here it means
 *        "you are inside a candidate configuration". A script that read `#`
 *        as "we are privileged, send the next show command" would be sending
 *        operational verbs into a configuration database.
 *
 * There is no `enable`, no `configure terminal` (`configure` alone), no
 * `write memory` — `commit` is the save, and it is ATOMIC.
 *
 * # ATOMICITY IS THIS PLATFORM'S GIFT, AND THIS SCRIPT IS BUILT AROUND IT
 *
 * Every other console script in this directory pushes a management
 * configuration line by line into a RUNNING configuration, so a run that dies
 * two thirds of the way through leaves a switch two thirds configured — the
 * failure mode this product has been burned by (cf. the Instant On
 * mgmt-addressing note: a half-applied address change that cut the device).
 *
 * Junos does not work that way. `set` lines accumulate in a CANDIDATE
 * configuration that changes nothing, `commit check` validates it without
 * applying it, and `commit` applies the whole thing or none of it. So this
 * script:
 *
 *   1. types every `set` of a transaction into the candidate;
 *   2. runs `commit check` — a dry run. A refused line is found HERE, with the
 *      device still untouched;
 *   3. only then commits;
 *   4. on a check failure, `rollback 0` throws the candidate away and the
 *      device is exactly as it was.
 *
 * It also means THE DIALECT CAN BE PROBED SAFELY, which matters more on this
 * platform than on any other in the fleet — see [ELS] below.
 *
 * # TWO TRANSACTIONS, AND THE SPLIT IS LOAD-BEARING
 *
 *   T1 — REACHABILITY. The root password, the meshcraft account, SSH, the
 *        management VLAN and its L3 interface, the trunks that carry it, the
 *        default route, and the end of ZTP. Everything meshcraft needs to take
 *        over, and nothing else. If this fails, the run fails.
 *   T2 — THE REST OF THE SITE's VLANs. Nice to have, and explicitly allowed to
 *        fail: a switch that is reachable but missing VLAN 40 is a switch an
 *        operator can fix from the UI. A switch that is not reachable is a
 *        second trip to the site.
 *
 * # WHAT IS IRREVERSIBLE, AND IT IS ANNOUNCED BEFORE IT RUNS
 *
 * `request system zeroize` is the only irreversible step. It erases the
 * configuration, the log files, and — the part people forget — "all plain-text
 * passwords, secrets, and private keys for SSH, local encryption, local
 * authentication, IPsec, RADIUS, TACACS+, and SNMP"
 * (juniper.net CLI reference, request system zeroize). Any certificate or SSH
 * host key a neighbour had pinned is gone. Nothing else here cannot be undone
 * from the console.
 *
 * # Console speed
 *
 * 9600 8N1, no flow control — the EX3400 hardware guide states exactly that,
 * and it is the documented setting across the EX range. The same end of the
 * range as the Catalyst and the Arista, NOT the 115200 the FS and Ubiquiti
 * ranges use.
 *
 * # SOURCES (each fact's own citation is at its use site)
 *
 *   - request system zeroize, incl. the post-reboot state:
 *     juniper.net/documentation/us/en/software/junos/cli-reference/topics/ref/command/request-system-zeroize.html
 *   - the loader window and `boot -s` / `recovery`:
 *     juniper.net/documentation/us/en/software/junos/junos-install-upgrade/topics/topic-map/recovery-of-junos-os.html
 *     juniper.net/documentation/us/en/software/junos/user-access/topics/topic-map/recovering-root-password.html
 *   - ZTP / phone-home on a factory-default or zeroized EX, and the way out:
 *     juniper.net/documentation/us/en/software/junos/junos-install-upgrade/topics/topic-map/zero-touch-provision.html
 *   - console 9600 8N1:
 *     juniper.net/documentation/us/en/hardware/ex3400/topics/topic-map/ex3400-configuring-junos-os.html
 *   - ELS vs pre-ELS layer-2 syntax:
 *     juniper.net/documentation/en_US/junos/topics/task/configuration/bridging-vlans-ex-series-cli-els.html
 *   - root SSH login denied by default from Junos 17.4:
 *     juniper.net/documentation/en_US/junos12.3/topics/task/configuration/ssh-services-configuring.html
 *     plus the release-note change recorded in the community thread cited at
 *     [MESHCRAFT_CLASS].
 *   - this repository's own `juniper-junos` entry in internal/discovery/reset.go
 *     (marked Verified) for the "Junos refuses to commit without a root
 *     password" caveat.
 */

import type { SerialSession } from "./serialExpect";
import { hostnameFromDeviceName } from "./hostname.ts";

export type SoftResetLog = (msg: string) => void;

/** The staged moments a caller may render to the operator. */
export type JuniperExStage =
  | "line-check"
  /** Operator must power-cycle; the wizard watches for the loader window. */
  | "await-power-cycle"
  /** Loader chatter seen — the countdown is on screen and SPACE has been sent. */
  | "loader"
  /** `boot -s` → the single-user shell/recovery question. */
  | "single-user"
  | "zeroize"
  | "booting"
  /** Factory console reached ("Amnesiac"), root logged in, `cli` entered. */
  | "amnesiac"
  | "ztp-off"
  | "provisioning"
  | "verify";

export type JuniperExSoftResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  onStage?: (stage: JuniperExStage) => void;
  /** Credentials that work BEFORE the reset. A factory EX has `root`, no password. */
  currentUser: string;
  currentPassword: string;
  /** Account to create after the reset (from the instance credential). */
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  /** Dotted mask or prefix length; converted to the PREFIX form Junos wants. */
  mgmtMask: string;
  gateway?: string;
  /**
   * "vlan 99" for IN-BAND management (the default and the fleet's shape), or
   * "me0" / "vme" for the out-of-band port. See [resolveMgmtTarget] — the
   * two are genuinely different cables and the wrong one is a silent failure.
   */
  mgmtIface?: string;
  /** Every VLAN the SITE declares, created on the device in transaction T2. */
  siteVlans?: { id: number; name?: string }[];
  hostname?: string;
  signal?: AbortSignal;
};

/* ──────────────────────────── the wire vocabulary ─────────────────────────── */

/**
 * The loader countdown — the moment the whole hardware path aims at.
 *
 * VERBATIM, and the punctuation is part of it:
 *   "Hit [Enter] to boot immediately, or space bar for command prompt."
 *   "Booting [kernel] in 1 second..."
 *
 * The second line is the countdown TICKING: it prints "in 9 seconds..." down
 * to "in 1 second...". The window is therefore a handful of seconds at most,
 * and on some trains is as short as one — far too short for a human reading a
 * wizard, which is exactly why the script watches the line and sends the space
 * itself. Both halves are matched because a console opened mid-countdown may
 * only ever show the second.
 *
 * NOT PRESENT ON EVERY PLATFORM. Juniper states the loader prompt does not
 * appear on QFX5100, QFX5200, EX4600 and QFX10000 — [LOADER_ABSENT_MODELS]
 * carries that list and the runner says so rather than timing out silently.
 */
const LOADER_WINDOW =
  /Hit \[Enter\] to boot immediately|space ?bar for command prompt|Booting \[kernel\] in \d+ second/i;

/** The loader itself. One keystroke of space got us here. */
const LOADER_PROMPT = /(?:^|\n)\s*loader>\s*$/m;

/**
 * The single-user question, whose WORDING HAS MOVED and is matched loosely.
 *
 * Seen in the wild as both
 *   "Enter full pathname of shell or 'recovery' for root password recovery:"
 * and
 *   "Enter the full pathname of the shell or recovery for root password
 *     recovery or RETURN for /bin/sh:"
 *
 * so the pattern anchors on the two words that are in every form — pathname and
 * shell — rather than on a sentence no train quite agrees on. Answering it with
 * `recovery` (rather than RETURN, which drops to /bin/sh) is what brings the
 * Junos CLI up with root authority.
 */
const SINGLE_USER_QUESTION = /pathname of (?:the )?shell/i;

/** `FreeBSD/amd64 (Amnesiac) (ttyu0)` then `login:` — a switch with no config. */
const AMNESIAC = /\bAmnesiac\b/i;
const LOGIN_PROMPT = /(?:^|\n)[^\n]*\blogin:\s*$/i;
const PASSWORD_PROMPT = /(?:^|\n)\s*Password:\s*$/i;
/** FreeBSD's refusal. Junos does not say "Login incorrect" any other way. */
const LOGIN_INCORRECT = /Login incorrect|Authentication failed/i;

/** The UNIX shell a factory `root` lands in. `cli` is the way out of it. */
const SHELL_PROMPT = /%\s*$/m;
/** OPERATIONAL mode. */
const CLI_PROMPT = />\s*$/m;
/** CONFIGURATION mode — and `#` here does NOT mean privileged (see header). */
const CONFIG_PROMPT = /#\s*$/m;
/** Junos prints the edit context after each configuration command. */
const EDIT_BANNER = /\[edit[^\]]*\]/;

/**
 * Junos refusals. Every form is anchored so that ordinary output cannot match.
 *
 * `error:` is the workhorse; `syntax error` and `unknown command` are what a
 * wrong-dialect `set` produces (cf. [ELS]); the caret line is how Junos points
 * at the rejected token, and it is matched because the token itself is the most
 * useful thing to put in a run log.
 */
const CLI_ERROR =
  /(?:^|\n)\s*error:[^\n]*|syntax error[^\n]*|unknown command[^\n]*|missing argument[^\n]*|statement not found[^\n]*|is not a valid [^\n]*/i;

/**
 * The commit's own words.
 *
 * `commit complete` is the success line; `configuration check succeeds` is what
 * `commit check` answers. Both are matched as completion so that one helper can
 * drive either verb.
 */
const COMMIT_OK = /commit complete|configuration check succeeds/i;
/**
 * The ONE commit refusal worth naming, because it is the one this platform
 * produces on a freshly zeroized switch and the message does not explain
 * itself: Junos will not commit a configuration with no root password.
 * internal/discovery/reset.go records the same caveat (marked Verified).
 */
const MISSING_ROOT_AUTH = /Missing mandatory statement:\s*'?root-authentication/i;

/** The interactive password dialogue. The value is never echoed. */
const NEW_PASSWORD = /New password:\s*$/i;
const RETYPE_PASSWORD = /Retype new password:\s*$/i;
/** A password Junos refuses on its own policy — length, character classes. */
const PASSWORD_REFUSED =
  /error:[^\n]*(?:password|minimum length|character)[^\n]*|require(?:s|d)? (?:change|at least)[^\n]*/i;

/** The operational-mode pager. Web Serial cannot claim a tall terminal. */
const PAGER = /---\(more[^)]*\)---/i;

/**
 * TWO QUESTION GRAMMARS LIVE IN THE SAME PROCEDURE, and they take DIFFERENT
 * answers. Getting this wrong is silent.
 *
 *   "Erase all data…? [yes,no] (no)"   needs the WORD `yes`. A bare `y` is
 *                                        rejected, and the default is no — so a
 *                                        `y`, or a bare CR, DECLINES THE RESET
 *                                        while the run carries on believing it
 *                                        wiped the switch.
 *   "Reboot the system? [y/n]"         needs `y`. The word `yes` is not what
 *                                        this one reads.
 *
 * So the two are matched apart and answered apart — cf. [answerYesNo].
 */
const YESNO_WORD = /\[yes,no\]\s*(?:\((?:yes|no)\)\s*)?$/i;
const YESNO_LETTER = /\[y\/n\]\s*\??\s*$/i;
const YESNO = /\[yes,no\]\s*(?:\((?:yes|no)\)\s*)?$|\[y\/n\]\s*\??\s*$/i;

/** Answer whichever grammar is on screen. Cf. [YESNO_WORD] / [YESNO_LETTER]. */
export function yesAnswerFor(prompt: string): "yes" | "y" {
  if (YESNO_LETTER.test(prompt.trimEnd())) return "y";
  return "yes";
}

/**
 * The zeroize confirmation, VERBATIM:
 *   warning: System will be rebooted and may not boot without configuration
 *   Erase all data, including configuration and log files? [yes,no] (no)
 *
 * The DEFAULT IS NO, which is why this is answered with the word `yes` and never
 * with a bare CR — the reflex that works on a Cisco "[confirm]" would quietly
 * decline the reset here and the run would carry on against an un-wiped switch.
 */
const ZEROIZE_QUESTION = /Erase all data, including configuration and log files\?/i;

/**
 * Boot chatter, for the wait between a reboot and a usable console.
 *
 * A Junos boot is long and loud: U-Boot or the FreeBSD loader, a kernel, a
 * filesystem check, then the Junos daemons. "Automatic reboot in progress"
 * and the loader banner are the early markers; `Amnesiac` and `login:` are the
 * arrival.
 */
const BOOT_MARK =
  /U-Boot|FreeBSD\/|Loading \/boot|Booting \[kernel\]|Automatic reboot|Junos|mountroot|Starting (?:local )?daemons|chassisd/i;

/**
 * Zero Touch Provisioning, as the console announces it.
 *
 * A zeroized or factory-default EX2300 / EX3400 / EX4300 does NOT sit quietly:
 * Juniper's own ZTP chapter says the switch must be in a factory-default state
 * ("or you can issue the request system zeroize command") for DHCP-options
 * ZTP or the phone-home client to run, and that is precisely the state this
 * script creates. So every run of this script arms ZTP, by design, and the
 * first thing the push does is disarm it.
 *
 * # It is NOT the Arista trap, and the difference is worth stating exactly
 *
 * On EOS, a ZTP switch DISCARDS whatever is written to it at the next cycle —
 * the reason aristaEosSoftReset.ts has a state machine. Junos does not discard a
 * committed configuration; committing `delete chassis auto-image-upgrade` is
 * itself what stops ZTP. The danger here is different and narrower: Juniper
 * warns "do not commit a user configuration while the device is performing ZTP
 * activity — for example, updating the software image or applying a
 * configuration file". A site whose DHCP server hands out ZTP options can
 * therefore have the switch downloading an IMAGE while this script types into
 * it, and the reboot that follows an image install lands in the middle of the
 * push.
 *
 * Which is why [stopZTP] runs first, in a transaction of its own, and why its
 * log line tells the operator what was seen.
 */
const ZTP_MARK =
  /auto-image-upgrade|zero ?touch|phone-?home|\bZTP\b|dhcp-?client[^\n]*ztp/i;

/* ───────────────────────────── pure helpers ───────────────────────────── */

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("provisioning aborted");
}

function clipTail(text: string, n: number): string {
  return text.slice(-n).replace(/\s+/g, " ").trim();
}

/**
 * Turn a dotted mask (or "/nn", or "nn") into the PREFIX LENGTH Junos wants.
 *
 * Junos takes `address 10.0.0.5/24` and has no two-argument dotted form at all,
 * so this conversion is not a convenience — it is the only shape the platform
 * accepts. An unreadable input falls back to /24 rather than producing a line
 * the CLI rejects, the same choice every other script in this directory makes.
 *
 * It is a LOCAL copy of arithmetic that also exists in aristaEosSoftReset.ts,
 * and that is deliberate: the lesson this directory learned from four diverging
 * `sanitizeHostname` functions was about POLICY (what name is acceptable), which
 * must have one home. This is IP arithmetic with no platform opinion in it, and
 * a Juniper console script importing from an Arista console script would be a
 * worse dependency than a duplicated bit count.
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
  var m = /^\s*(?:vlan\s*\.?\s*)?(\d{1,4})\s*$/i.exec(iface || "");
  if (!m) return null;
  var id = Number(m[1]);
  if (!Number.isFinite(id) || id < 1 || id > 4094) return null;
  return id;
}

/** What the console is showing, which decides everything the runner does next. */
export type JunosConsoleState =
  | "loader-window"
  | "loader"
  | "single-user"
  | "login"
  | "password"
  | "shell"
  | "cli"
  | "config"
  | "unknown";

/**
 * Classify the console from its tail, by the LAST marker — never the first.
 *
 * Same principle as `rightmostMatch` in serialExpect.ts and `lastIndexOfRe` in
 * the Arista script, and here it is not an optimisation but the whole
 * correctness of the thing: a Junos console that has been through one login
 * attempt and one `cli` carries `login:`, `%` and `>` all in the same buffer,
 * and only the most recent of them describes the present. Reading the first
 * `%` would have the script typing shell commands at an operational prompt.
 *
 * PURE and exported — the fixtures in the test file are the real reason this is
 * a function rather than an if-chain inside the runner.
 */
export function classifyJunosConsole(tail: string): JunosConsoleState {
  var probes: { state: JunosConsoleState; re: RegExp }[] = [
    { state: "loader-window", re: LOADER_WINDOW },
    { state: "loader", re: LOADER_PROMPT },
    { state: "single-user", re: SINGLE_USER_QUESTION },
    { state: "password", re: PASSWORD_PROMPT },
    { state: "login", re: LOGIN_PROMPT },
    { state: "shell", re: SHELL_PROMPT },
    { state: "config", re: CONFIG_PROMPT },
    { state: "cli", re: CLI_PROMPT },
  ];
  var best: JunosConsoleState = "unknown";
  var bestAt = -1;
  var p: { state: JunosConsoleState; re: RegExp };
  for (p of probes) {
    // WHERE THE MATCH ENDS, not where it starts, and that distinction is a bug
    // this function was written with. "loader>" contains the `>` the
    // operational-mode test is looking for, and the loader pattern STARTS
    // earlier (it anchors on the newline before the word) — so comparing start
    // positions made a boot loader look like a Junos prompt, which is the one
    // misreading with no recovery: the script would have typed `set cli
    // screen-length 0` at a loader.
    //
    // Comparing END positions makes the two tie, and the STRICT `>` below then
    // lets the FIRST probe in the list win a tie. That is why the order above
    // runs most-specific-first.
    var at = lastMatchEnd(tail, p.re);
    if (at > bestAt) {
      bestAt = at;
      best = p.state;
    }
  }
  return best;
}

/** Where the LAST match of `re` ENDS, or -1. Cf. [classifyJunosConsole]. */
export function lastMatchEnd(text: string, re: RegExp): number {
  var flags = re.flags.includes("g") ? re.flags : re.flags + "g";
  var scan = new RegExp(re.source, flags);
  var last = -1;
  var m: RegExpExecArray | null;
  while ((m = scan.exec(text)) !== null) {
    last = m.index + m[0].length;
    if (m[0].length === 0) scan.lastIndex++;
  }
  return last;
}

/**
 * Platforms whose loader window this script cannot catch.
 *
 * Juniper states plainly that "the loader prompt does not appear on QFX5100,
 * QFX5200, EX4600, and QFX10000 switches". The assisted path is therefore
 * impossible on them, and saying so up front is worth more than a five-minute
 * watch that ends in a timeout nothing explains.
 */
export const LOADER_ABSENT_MODELS = /QFX5100|QFX5200|EX4600|QFX10000/i;

/**
 * ELS — Enhanced Layer 2 Software — and this is THE fork between EX models.
 *
 * The layer-2 configuration syntax changed name for name, and a line from the
 * wrong side is simply refused:
 *
 *                      ELS                        pre-ELS
 *   L3 interface       irb unit N / irb.N         vlan unit N / vlan.N
 *   VLAN ↔ L3          set vlans V l3-interface   same statement, vlan.N
 *   port mode          interface-mode trunk       port-mode trunk
 *   VLAN membership    vlan members V             vlan members V   (same)
 *
 * It is decided by MODEL, not by Junos version, for the models in the table
 * below — but the table is not the safety net. [commitCheck] is: the candidate
 * configuration is validated before anything is applied, so a wrong guess costs
 * one `rollback 0` and a retry in the other dialect, with the device untouched
 * throughout. That is why this function is allowed to return a BEST GUESS for
 * an unknown model instead of refusing to act.
 *
 * ELS landed in Junos 12.3R2 for the models that support it; an EX4300 is ELS
 * from its first shipping release, so model is a sound discriminator in practice.
 */
export function isELSModel(versionOutput: string): boolean | null {
  var text = versionOutput || "";
  if (/\bEX2200|\bEX3200|\bEX3300|\bEX4200|\bEX45[05]0|\bEX6200|\bEX8200/i.test(text)) {
    return false;
  }
  if (
    /\bEX2300|\bEX3400|\bEX4300|\bEX4400|\bEX4100|\bEX4600|\bEX4650|\bEX9200|\bQFX/i.test(text)
  ) {
    return true;
  }
  return null;
}

/** Where the management address goes, and on which cable. */
export type MgmtTarget =
  | { kind: "oob"; iface: "me0" | "vme" }
  | { kind: "inband"; vlanId: number; vlanName: string };

/**
 * Decide between the OUT-OF-BAND port and an IN-BAND L3 interface.
 *
 * # THIS IS THE DECISION THAT DECIDES WHETHER THE SWITCH ANSWERS AT ALL
 *
 * `me0` is a PHYSICAL RJ-45 on the Routing Engine, outside the switching
 * fabric. `vme` — Virtual Management Ethernet — is the logical form of the same
 * thing: it exists so that a Virtual Chassis is managed through whichever member
 * is master, and Juniper notes it can be configured on a standalone switch too
 * "for consistency". Both reach the switch ONLY over that dedicated port.
 *
 * So the `me0` / `vme` question an operator asks is not "which name does my
 * model use" but "is the management cable in the small port on its own".
 * meshcraft reaches a switch through a PROXY on the site LAN, over the fabric,
 * so the fleet's shape is IN-BAND and that is the default here — exactly as
 * aristaEosSoftReset.ts refuses to put the fabric's management address on
 * Management1.
 *
 * The out-of-band path is still offered, because a site that has an OOB network
 * has it for good reasons; it is chosen by naming the interface explicitly.
 *
 * WHICH NAME FOR WHICH MODEL: `me0` exists on the models that have a dedicated
 * management RJ-45; `vme` is available on the Virtual-Chassis-capable range
 * (EX3300, EX4200, EX4300, EX4500 and the EX2300/EX3400 generation). Rather
 * than guess, this takes the operator's word for it and the runner CONFIRMS the
 * name exists on the device before committing to it (cf. [readMgmtIfaces]) —
 * the only check that cannot be wrong.
 */
export function resolveMgmtTarget(
  mgmtIface: string | undefined,
  mgmtVlanName?: string,
): MgmtTarget {
  var raw = (mgmtIface || "").trim().toLowerCase();
  if (/^me0(?:\.0)?$/.test(raw)) return { kind: "oob", iface: "me0" };
  if (/^vme(?:\.0)?$/.test(raw)) return { kind: "oob", iface: "vme" };
  var vlanId = parseVlanId(mgmtIface || "");
  if (vlanId === null) {
    throw new Error(
      `management interface "${mgmtIface || ""}" is not usable on a Junos EX: write ` +
        `"vlan N" for in-band management (the fleet's shape — meshcraft reaches the ` +
        `switch over the site LAN), or "me0" / "vme" to put the address on the ` +
        `dedicated out-of-band port instead. Those are different cables, and the wrong ` +
        `one produces a switch that is configured perfectly and answers nothing`,
    );
  }
  return {
    kind: "inband",
    vlanId,
    vlanName: sanitizeVlanName(mgmtVlanName || `LAN${vlanId}_Management`),
  };
}

/**
 * A VLAN name Junos accepts. Letters, digits, hyphen and underscore; a leading
 * digit is refused by the platform, so one is prefixed away.
 */
export function sanitizeVlanName(s: string): string {
  var out = (s || "").replace(/[^A-Za-z0-9_-]/g, "_").replace(/_+/g, "_");
  out = out.replace(/^_+|_+$/g, "");
  if (!out) return "Management";
  if (/^\d/.test(out)) out = "v" + out;
  return out.slice(0, 63);
}

/**
 * The L3 interface UNIT for an in-band management VLAN.
 *
 * Junos ties a VLAN to a layer-3 interface by unit number, and the fleet
 * convention is unit = VLAN id so that `irb.99` reads as "VLAN 99" to anyone
 * looking at the device.
 *
 * VLAN 1 IS THE EXCEPTION and it is not arbitrary: a factory EX already carries
 * a VLAN named `default`, the untagged one every port is in, and the universal
 * convention for its L3 interface is unit 0 — `irb.0` / `vlan.0`. Creating a
 * second VLAN with vlan-id 1 beside `default` is refused by the platform, so for
 * VLAN 1 the push ATTACHES TO `default` rather than declaring anything.
 */
export function mgmtUnit(vlanId: number): number {
  return vlanId === 1 ? 0 : vlanId;
}

/** The faceplate interfaces, from "show interfaces terse". */
export type JunosIfaces = {
  /** ge-0/0/1, xe-0/1/0, et-0/0/48 … — revenue ports, in faceplate order. */
  ports: string[];
  /** Whether the device shows a `me0` / `vme` of its own. */
  hasMe0: boolean;
  hasVme: boolean;
  hasIrb: boolean;
  hasVlanIface: boolean;
};

/**
 * Parse "show interfaces terse".
 *
 *   Interface               Admin Link Proto    Local                 Remote
 *   ge-0/0/0                up    up
 *   ge-0/0/0.0              up    up   eth-switch
 *   me0                     up    up
 *   me0.0                   up    up   inet     10.0.0.5/24
 *   irb                     up    up
 *
 * ONLY the PHYSICAL line is taken (no dot, no unit): the `.0` logical units are
 * the same jack counted twice, and `vcp-` / `vme` / `me0` / `irb` / `lo0` are
 * not jacks at all. A port list with `me0` in it would have the push telling the
 * out-of-band port to become a trunk — a refused command in the middle of the
 * transaction, which on this platform fails the WHOLE commit rather than just
 * that line.
 *
 * The prefixes accepted are the revenue-port families on this range: `ge-`
 * (gigabit copper/SFP), `xe-` (10G), `et-` (25/40/100G), `mge-` (multigigabit
 * on the EX4100/EX4400 generation). Anything else is deliberately left alone.
 */
export function parseInterfacesTerse(out: string): JunosIfaces {
  var ports: string[] = [];
  var seen: Record<string, boolean> = {};
  var hasMe0 = false;
  var hasVme = false;
  var hasIrb = false;
  var hasVlanIface = false;
  var line: string;
  for (line of (out || "").split(/\r?\n/)) {
    var m = /^(\S+)\s+(?:up|down)\b/i.exec(line);
    if (!m) continue;
    var name = m[1];
    // The management / L3 interfaces are detected on the name with any LOGICAL
    // UNIT stripped. Junos normally prints the physical line and its units, but
    // "vme is often listed without a vme.0 until it is configured" cuts both
    // ways — a device that showed only `vme.0` would otherwise be read as having
    // no out-of-band port at all, and the push would refuse a name the switch
    // plainly has.
    var base = name.split(".")[0];
    if (base === "me0") hasMe0 = true;
    if (base === "vme") hasVme = true;
    if (base === "irb") hasIrb = true;
    if (base === "vlan") hasVlanIface = true;
    if (name.indexOf(".") >= 0) continue;
    if (!/^(?:ge|xe|et|mge)-\d+\/\d+\/\d+$/.test(name)) continue;
    if (seen[name]) continue;
    seen[name] = true;
    ports.push(name);
  }
  return { ports, hasMe0, hasVme, hasIrb, hasVlanIface };
}

/** VLAN 1 dropped, the management VLAN folded in without duplicating a site entry. */
export function mergeSiteVlans(
  siteVlans: { id: number; name?: string }[] | undefined,
  mgmtVlan: number,
  mgmtName?: string,
): { id: number; name: string }[] {
  var out: { id: number; name: string }[] = [];
  var seen: Record<number, boolean> = {};
  var v: { id: number; name?: string };
  for (v of siteVlans || []) {
    if (!Number.isFinite(v.id) || v.id < 1 || v.id > 4094 || v.id === 1 || seen[v.id]) continue;
    seen[v.id] = true;
    out.push({ id: v.id, name: sanitizeVlanName(v.name || `LAN${v.id}`) });
  }
  if (mgmtVlan > 1 && !seen[mgmtVlan]) {
    out.push({ id: mgmtVlan, name: sanitizeVlanName(mgmtName || `LAN${mgmtVlan}_Management`) });
    seen[mgmtVlan] = true;
  }
  return out.sort((a, b) => a.id - b.id);
}

/**
 * The class the meshcraft account is created in.
 *
 * `super-user` is the only built-in class that can both read everything and
 * commit. It is chosen over logging meshcraft in as `root` for a reason that is
 * not style: FROM JUNOS 17.4 ROOT SSH LOGIN IS DENIED BY DEFAULT. A provisioning
 * run that set only the root password would leave a switch whose SSH door is
 * open, whose credential is correct, and which refuses the login anyway — and
 * the fix (`set system services ssh root-login allow`) is a security regression
 * nobody asked for.
 *
 * The root password is still set, because Junos refuses to commit without one
 * and because the console must stay usable; it is simply not the account
 * meshcraft uses.
 */
export const MESHCRAFT_CLASS = "super-user";

/** One command in a transaction: what to send, and what to call it in the log. */
export type JunosCmd = { cmd: string; label?: string; timeoutMs?: number };

/**
 * TRANSACTION T1 — REACHABILITY. Everything meshcraft needs to take the switch
 * over, and deliberately nothing else.
 *
 * # THE FLEET TEMPLATE FOR THIS PLATFORM
 *
 *   ZTP off              delete chassis auto-image-upgrade
 *   host-name            through the ONE shared normalizer (./hostname.ts)
 *   SSH                  set system services ssh   (+ explicit root-login deny)
 *   management address   irb.N / vlan.N on the site's VLAN, or me0 / vme
 *   VLAN ↔ L3            set vlans V l3-interface irb.N
 *   trunks               every faceplate port, carrying the management VLAN
 *   default route        set routing-options static route 0.0.0.0/0 next-hop GW
 *   meshcraft account    super-user class, password typed interactively
 *   root password        set, because Junos will not commit without one
 *
 * # The ORDER inside a Junos transaction does NOT matter for application —
 * # it is one atomic change — but it matters for the OPERATOR READING THE LOG,
 * # and for `commit check`, which reports the first problem it meets.
 *
 * So the lines are grouped the way a human would check them: stop the switch
 * doing anything on its own, name it, open the door, give it an address, give
 * the address a path, then the accounts.
 *
 * THE TWO PASSWORDS ARE NOT IN THIS LIST. `set …plain-text-password` is an
 * INTERACTIVE dialogue ("New password:" / "Retype new password:") and is
 * driven by [setPlainTextPassword]. That is a feature, not a wart: the secret
 * never appears in a `set` line, so it never reaches the console transcript the
 * wizard ships with a failed run.
 */
export function buildReachabilityCmds(opts: {
  els: boolean;
  target: MgmtTarget;
  ip: string;
  prefixLen: number;
  gateway?: string;
  hostname?: string;
  /** Faceplate ports to trunk. Empty leaves the faceplate alone (see below). */
  trunkPorts: string[];
  /** Where a REFUSED system name is reported — cf. aristaEosSoftReset. */
  log?: SoftResetLog;
}): JunosCmd[] {
  var out: JunosCmd[] = [];
  var push = (cmd: string, label?: string, timeoutMs?: number) =>
    out.push({ cmd, label, timeoutMs });

  // 1. STOP THE SWITCH PROVISIONING ITSELF. First line of the transaction, for
  //    the reason in [ZTP_MARK]: a switch that has just been zeroized is armed
  //    for ZTP / phone-home, and Juniper's own guidance is not to commit a user
  //    configuration while ZTP activity is in flight. This is the statement
  //    whose deletion ends it.
  //
  //    `delete` of a statement that is absent is an ERROR on Junos
  //    ("statement not found") rather than a no-op, so the runner sends this
  //    one through [stopZTP], which tolerates exactly that refusal.
  push("delete chassis auto-image-upgrade", "delete chassis auto-image-upgrade");

  // 2. The system name, through the ONE shared normalizer (./hostname.ts),
  //    which mirrors internal/driver/hostname.go. A refusal is REPORTED and the
  //    rest of the transaction continues: aborting a run over a system name
  //    would leave a zeroized switch with no management address at all.
  if (opts.hostname?.trim()) {
    var verdict = hostnameFromDeviceName(opts.hostname);
    if (verdict.hostname) {
      push(`set system host-name ${verdict.hostname}`);
    } else {
      opts.log?.(
        `the system name was NOT written — ${verdict.reason}. The switch keeps the ` +
          `name it has; nothing else in this transaction is affected`,
      );
    }
  }

  // 3. THE DOOR meshcraft comes in through, and the explicit refusal of the one
  //    door nobody asked for. `root-login deny` is written rather than relied
  //    upon: it is the default from Junos 17.4 and it is NOT the default before,
  //    so the line is what makes the rule true across the range rather than true
  //    only on recent trains. Cf. [MESHCRAFT_CLASS].
  push("set system services ssh");
  push("set system services ssh root-login deny");
  // LLDP on every revenue port, by the same reasoning as the other scripts:
  // provisioning is the one pass that visits every device, and a switch missing
  // from the topology is a hole exactly where the switch is.
  push("set protocols lldp interface all");

  // 4. THE MANAGEMENT ADDRESS. Two genuinely different cables — cf.
  //    [resolveMgmtTarget] — and the in-band form carries the ELS fork.
  if (opts.target.kind === "oob") {
    // THE DHCP CLIENT COMES OFF FIRST, and leaving it on is a real failure
    // rather than untidiness: a factory-default EX of the ZTP generation ships
    // with "interfaces vme unit 0 family inet dhcp", so a static address added
    // beside it leaves the interface with two sources of truth — the one this
    // run recorded in meshcraft, and whatever the site's DHCP server hands out.
    //
    // It is sent through [tolerantDeletes] rather than listed here, because
    // `delete` of an absent statement is an ERROR on Junos, not a no-op, and on
    // a pre-ZTP model (EX2200, EX4200, EX4550) the statement is simply absent.
    push(
      `set interfaces ${opts.target.iface} unit 0 family inet address ${opts.ip}/${opts.prefixLen}`,
    );
  } else {
    // THE YELLOW ALARM NOBODY ASKED FOR.
    //
    // Junos raises a chassis alarm when the management Ethernet port has no
    // carrier, and in-band management is exactly the case where that port is
    // deliberately left unplugged. Without this the switch finishes
    // provisioning correctly and then sits there with its alarm LED lit and a
    // permanent entry in `show chassis alarms` — a standing fault condition
    // that is not a fault, on every device meshcraft provisions this way.
    //
    // The cost of a false alarm is not the LED: it is that an operator who
    // sees one on every switch stops reading them, and the real one arrives
    // into a habit of ignoring the colour. That is the same argument §2 of
    // the UI guide makes about painting a platform limit orange.
    //
    // ONLY ON THE IN-BAND PATH. In the `oob` branch above, the management
    // port IS the cable this run depends on, and silencing its link-down
    // alarm would hide the one failure that matters most there.
    push("set chassis alarm management-ethernet link-down ignore");

    var l3 = opts.els ? "irb" : "vlan";
    var unit = mgmtUnit(opts.target.vlanId);
    // VLAN 1 attaches to the FACTORY `default` VLAN rather than declaring a
    // second VLAN with vlan-id 1, which the platform refuses. Cf. [mgmtUnit].
    var vlanName = opts.target.vlanId === 1 ? "default" : opts.target.vlanName;
    if (opts.target.vlanId !== 1) {
      push(`set vlans ${vlanName} vlan-id ${opts.target.vlanId}`);
    }
    push(`set interfaces ${l3} unit ${unit} family inet address ${opts.ip}/${opts.prefixLen}`);
    push(`set vlans ${vlanName} l3-interface ${l3}.${unit}`);

    // The trunks. WITHOUT THESE THE ADDRESS IS UNREACHABLE whenever the
    // management VLAN is tagged: a factory EX has every port untagged in
    // `default`, so an irb on VLAN 99 has no port that carries VLAN 99 and the
    // switch answers on nothing. This is the same reasoning as the Arista
    // script's faceplate trunking, and the same trade: a port profile applied
    // later narrows any of them to access in one apply, while a port left at
    // the factory default could not carry anything until somebody noticed.
    //
    // VLAN 1 is the one case where the trunks are NOT needed for reachability
    // (the factory faceplate already carries it untagged) — they are still
    // written, so that a switch provisioned on VLAN 1 and one provisioned on
    // VLAN 99 come out of this script the same shape.
    var port: string;
    for (port of opts.trunkPorts) {
      // `interface-mode` (ELS) vs `port-mode` (pre-ELS) — the single most
      // common wrong-dialect line on this platform.
      push(
        `set interfaces ${port} unit 0 family ethernet-switching ${
          opts.els ? "interface-mode" : "port-mode"
        } trunk`,
      );
      push(
        `set interfaces ${port} unit 0 family ethernet-switching vlan members ${vlanName}`,
      );
    }
  }

  // 5. The path out. NOT a "default-gateway" — Junos has no such statement;
  //    the default route is an ordinary static route in the main instance.
  var gw = (opts.gateway || "").trim();
  if (gw) {
    push(`set routing-options static route 0.0.0.0/0 next-hop ${gw}`);
  }
  return out;
}

/**
 * TRANSACTION T2 — the rest of the site's VLANs, and their membership on the
 * trunks T1 built.
 *
 * ALLOWED TO FAIL, and that permission is the point of splitting it off. A
 * switch that is reachable but missing VLAN 40 is something an operator fixes
 * from the UI in a minute; a switch that is not reachable is a second trip to
 * the site. So anything that is not strictly needed for meshcraft to take over
 * lives here, behind its own `commit check`, and a refusal is rolled back
 * without touching what T1 established.
 */
export function buildSiteVlanCmds(opts: {
  els: boolean;
  target: MgmtTarget;
  siteVlans: { id: number; name: string }[];
  trunkPorts: string[];
}): JunosCmd[] {
  var out: JunosCmd[] = [];
  if (opts.target.kind !== "inband") {
    // No faceplate work was done in T1 for the out-of-band shape, and inventing
    // some here would be this script reconfiguring a switching fabric nobody
    // asked it to touch.
    return out;
  }
  // Bound to a local because the narrowing above does not survive into a
  // closure — and because the management VLAN's id is the one value this whole
  // function is defined by exclusion from.
  var mgmtVlanId = opts.target.vlanId;
  var v: { id: number; name: string };
  for (v of opts.siteVlans) {
    if (v.id === mgmtVlanId) continue; // T1 already declared it
    out.push({ cmd: `set vlans ${v.name} vlan-id ${v.id}` });
  }
  var names = opts.siteVlans.filter((x) => x.id !== mgmtVlanId).map((x) => x.name);
  if (names.length === 0) return out;
  var port: string;
  for (port of opts.trunkPorts) {
    // ONE statement per VLAN rather than a bracketed list: Junos accepts
    // "vlan members [ a b c ]", but a single refused name inside the brackets
    // fails the whole line, and on a console a long bracketed line is also the
    // shape most likely to hit a line-length limit.
    var name: string;
    for (name of names) {
      out.push({
        cmd: `set interfaces ${port} unit 0 family ethernet-switching vlan members ${name}`,
      });
    }
  }
  return out;
}

/* ───────────────────────────── the dialogue ───────────────────────────── */

type ReplyKind = "error" | "prompt" | "yesno" | "pager" | "timeout";

/** After a command: a question, a refusal, or back at a prompt. */
async function expectReply(
  session: SerialSession,
  timeoutMs: number,
): Promise<{ kind: ReplyKind; match: string }> {
  try {
    var hit = await session.expectAny(
      [CLI_ERROR, YESNO, PAGER, CONFIG_PROMPT, CLI_PROMPT, SHELL_PROMPT],
      timeoutMs,
    );
    if (hit.index === 0) return { kind: "error", match: hit.match };
    if (hit.index === 1) return { kind: "yesno", match: hit.match };
    if (hit.index === 2) return { kind: "pager", match: hit.match };
    // A prompt usually arrives in the SAME burst as the error line that
    // preceded it, and expectAny reports the rightmost match — so the burst is
    // re-read as a whole and the error preferred. Without this, every refused
    // `set` would be logged as "ok".
    var err = session.getSinceMark().match(CLI_ERROR);
    if (err) return { kind: "error", match: err[0] };
    return { kind: "prompt", match: hit.match };
  } catch {
    return { kind: "timeout", match: "" };
  }
}

/**
 * Collect a show command's output, feeding the pager when it appears.
 * `set cli screen-length 0` is sent once per session, but a session that
 * dropped and came back has lost it, so the pager is still handled here.
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
    if (PAGER.test(out.slice(-120))) {
      // Space = next page. The CR that send() appends would advance one line.
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

/**
 * Reach Junos OPERATIONAL mode (`>`), from wherever the console happens to be.
 *
 * The four states this has to cope with, and all four are real:
 *
 *   `login:`  the ordinary case. `root` with no password on a zeroized switch;
 *             whatever the operator typed on one that still has a config.
 *   `%`       the UNIX shell — where a factory `root` LANDS. `cli` is the way
 *             out, and a script that did not know this would type `show
 *             version` at a shell and read "show: not found".
 *   `#`       configuration mode, left behind by an earlier run or an operator.
 *             `top` then `exit configuration-mode` — not `end`, which is IOS.
 *   `>`       already there.
 */
export async function ensureOperational(
  session: SerialSession,
  user: string,
  password: string,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  session.mark();
  await session.send("");
  await session.sleep(900);

  var deadline = Date.now() + timeoutMs;
  var nudges = 0;
  var refusals = 0;
  while (Date.now() < deadline) {
    aborted(signal);
    // FRESH text and the rolling fallback are two different things. A PROMPT is
    // a state and stays true while the console is quiet, so the fallback is
    // right for it; "Login incorrect" is an EVENT and must only ever be read
    // out of fresh text, or one refusal would be counted for ever. Same
    // distinction, same reason, as aristaEosSoftReset.ensurePrivileged.
    var fresh = session.getSinceMark();
    var tail = fresh.slice(-1200) || session.getCapture().slice(-600);

    if (LOGIN_INCORRECT.test(fresh)) {
      refusals++;
      session.mark();
      if (refusals >= 2) {
        throw new Error(
          "the console refused this credential (\"Login incorrect\") — moving on " +
            "rather than sitting out the expect timeout",
        );
      }
      log("console answered \"Login incorrect\" — re-syncing to the login prompt");
      await session.send("");
      await session.sleep(1200);
      continue;
    }

    var state = classifyJunosConsole(tail);
    if (state === "cli") {
      log("at the Junos operational prompt (>)");
      return;
    }
    if (state === "shell") {
      log("console is at the UNIX shell (%) — entering the CLI with \"cli\"");
      session.mark();
      await session.send("cli");
      await session.sleep(1500);
      continue;
    }
    if (state === "config") {
      // NOT "end". That is IOS; here it is an unknown command, and the
      // console would stay in configuration mode while the script believed it
      // had left.
      log("console is in CONFIGURATION mode (#) — leaving it");
      session.mark();
      await session.send("top");
      await session.sleep(400);
      await session.send("exit configuration-mode");
      await session.sleep(1200);
      continue;
    }
    if (state === "login") {
      var who = user || "root";
      log(`login as ${who}${password ? "" : " (no password — factory state)"}`);
      session.mark();
      await session.send(who);
      var afterUser = await session.expectAny(
        [PASSWORD_PROMPT, SHELL_PROMPT, CLI_PROMPT, LOGIN_PROMPT],
        20000,
      );
      if (afterUser.index === 1 || afterUser.index === 2) {
        await session.sleep(300);
        continue; // the shell / cli branches above finish the job
      }
      if (afterUser.index === 3) {
        log("back at login: — credential refused");
        continue;
      }
      session.mark();
      await session.send(password);
      await session.sleep(2000);
      continue;
    }
    if (state === "password") {
      // A "Password:" THIS ATTEMPT DID NOT CAUSE — a console left mid-login.
      // Cleared rather than answered: posting a password against a half-entered
      // name is the one sequence whose meaning is unknowable, and on a zeroized
      // switch `root` has no password at all so the right answer would be empty.
      log("stale \"Password:\" with no login of ours behind it — clearing it to re-sync");
      session.mark();
      await session.send("");
      await session.sleep(1200);
      continue;
    }
    if (state === "loader" || state === "loader-window" || state === "single-user") {
      throw new Error(
        `the console is in the boot loader, not in Junos ("${clipTail(tail, 80)}") — ` +
          `this is the assisted hardware path's territory, not the soft reset's`,
      );
    }

    nudges++;
    if (nudges > 10) {
      throw new Error(
        `no Junos prompt seen on the console — last text: ${
          clipTail(session.getCapture(), 160) || "nothing at all"
        }`,
      );
    }
    session.mark();
    await session.send("");
    await session.sleep(1000);
  }
  throw new Error("could not reach the Junos operational prompt");
}

/** Enter configuration mode. Junos spells it `configure`, never `configure terminal`. */
async function enterConfig(session: SerialSession, log: SoftResetLog): Promise<void> {
  session.mark();
  await session.send("configure");
  var r = await session.expectAny([CONFIG_PROMPT, CLI_ERROR, EDIT_BANNER], 15000);
  if (r.index === 1) {
    throw new Error(`"configure" was refused: ${r.match.trim()}`);
  }
  log("in configuration mode (#)");
  await session.sleep(300);
}

/**
 * Send one transaction's `set` lines into the CANDIDATE configuration.
 *
 * Nothing here changes the device. Refusals are collected and returned rather
 * than thrown: on this platform the authoritative verdict is `commit check`,
 * and a line this function could not place is a line `commit check` will refuse
 * for the same reason — reporting both is what tells an operator whether the
 * problem was the dialect or the device.
 */
export async function typeCandidate(
  session: SerialSession,
  log: SoftResetLog,
  cmds: JunosCmd[],
  signal?: AbortSignal,
): Promise<string[]> {
  var refused: string[] = [];
  var i = 0;
  for (i = 0; i < cmds.length; i++) {
    aborted(signal);
    var c = cmds[i];
    var label = c.label || c.cmd;
    session.mark();
    await session.send(c.cmd);
    var reply = await expectReply(session, c.timeoutMs ?? 12000);
    if (reply.kind === "error") {
      refused.push(`${label}: ${reply.match.trim()}`);
      log(`${label}: REFUSED — ${reply.match.trim()}`);
    } else if (reply.kind === "timeout") {
      log(`${label}: no prompt reply within the window — continuing`);
    }
    // Let the device finish printing "[edit]" and its prompt: matching fires
    // on the first characters, and sending the next line while the rest streams
    // shifts every reply by one.
    await session.sleep(150);
  }
  return refused;
}

/**
 * `delete` lines whose ABSENCE is the good news.
 *
 * Junos answers `delete` of a statement that is not there with
 * "warning: statement not found" (an `error:` on some trains), and that is NOT
 * a failure here: it means the factory configuration this run is cleaning up
 * never had the statement. A pre-ZTP EX (EX2200, EX4200, EX4550) has no
 * `chassis auto-image-upgrade` and no DHCP client on its management interface,
 * so every one of these deletes is expected to be a no-op on half the range.
 *
 * Returns which ones actually removed something — the run log says so, because
 * "this switch WAS armed for zero-touch provisioning" and "it never was" are
 * different facts about the device in front of the operator.
 */
export async function tolerantDeletes(
  session: SerialSession,
  log: SoftResetLog,
  lines: string[],
  signal?: AbortSignal,
): Promise<string[]> {
  var removed: string[] = [];
  var line: string;
  for (line of lines) {
    aborted(signal);
    session.mark();
    await session.send(line);
    var reply = await expectReply(session, 15000);
    if (reply.kind === "error") {
      if (/statement not found|not found/i.test(reply.match)) {
        log(`"${line}": statement not found — nothing to remove, which is fine`);
        continue;
      }
      log(`"${line}": REFUSED — ${reply.match.trim()}`);
      continue;
    }
    removed.push(line);
    log(`"${line}": removed from the candidate configuration`);
  }
  return removed;
}

/**
 * The statements a FACTORY-DEFAULT Junos puts there on its own, and which a
 * static management address must not be layered on top of.
 *
 * `request system zeroize` — unlike `load factory-default` — adds
 * `chassis auto-image-upgrade`, and the factory configuration of the ZTP
 * generation also runs a DHCP client on its management interfaces: the ZTP
 * console chatter names them explicitly ("DHCP Client Unbound interfaces:
 * irb.0 vme.0"). Setting a static address beside a live DHCP client leaves the
 * interface with two sources of truth, one of which meshcraft does not know
 * about.
 */
export function factoryAutoConfigDeletes(target: MgmtTarget, els: boolean): string[] {
  if (target.kind === "oob") {
    return [`delete interfaces ${target.iface} unit 0 family inet dhcp`];
  }
  var l3 = els ? "irb" : "vlan";
  return [
    `delete interfaces ${l3} unit ${mgmtUnit(target.vlanId)} family inet dhcp`,
    // The out-of-band interface's DHCP client too, even though the address is
    // going in-band: the factory configuration arms both, and a `vme.0` left
    // DHCPing is a second management address on a switch meshcraft believes has
    // one. Both names are tried — [tolerantDeletes] shrugs at the absent one.
    "delete interfaces vme unit 0 family inet dhcp",
    "delete interfaces me0 unit 0 family inet dhcp",
  ];
}

/**
 * `commit check` — THE DRY RUN, and the reason this script can be bold.
 *
 * Validates the candidate configuration and applies NOTHING. Answers
 * "configuration check succeeds" or an `error:` naming the problem. Returns
 * the verdict rather than throwing, because a failed check is often a question
 * ("which dialect is this box?") and not yet a failure.
 */
export async function commitCheck(
  session: SerialSession,
  log: SoftResetLog,
): Promise<{ ok: boolean; detail: string }> {
  log("commit check (a DRY RUN — the device is not touched by this)…");
  session.mark();
  await session.send("commit check");
  var deadline = Date.now() + 120000;
  while (Date.now() < deadline) {
    try {
      await session.expectAny([COMMIT_OK, CLI_ERROR, CONFIG_PROMPT], 20000);
    } catch {
      log("commit check: still quiet — a large candidate takes a while here");
      continue;
    }
    var seen = session.getSinceMark();
    if (MISSING_ROOT_AUTH.test(seen)) {
      return {
        ok: false,
        detail:
          "Junos refuses to commit a configuration with no root password " +
          "(\"Missing mandatory statement: 'root-authentication'\"). The root " +
          "password step must have been skipped or refused",
      };
    }
    var err = seen.match(CLI_ERROR);
    if (err) return { ok: false, detail: err[0].trim() };
    if (COMMIT_OK.test(seen)) return { ok: true, detail: clipTail(seen, 200) };
  }
  return { ok: false, detail: "commit check timed out" };
}

/**
 * `commit` — the atomic apply.
 *
 * # WHY NOT `commit confirmed`, WHICH WOULD SEEM SAFER
 *
 * `commit confirmed 5` applies the configuration and rolls it back in five
 * minutes unless a second `commit` confirms it. It is the right tool when the
 * session you are typing through is the session the change might cut.
 *
 * It is the WRONG tool here, and using it would make this script less safe
 * rather than more. This run is on the CONSOLE: the change cannot cut the
 * session it travels over. What a rollback timer would add is a failure mode
 * that does not otherwise exist — a run that dies after the apply and before
 * the confirmation (browser tab closed, USB adapter unplugged, operator hits
 * Cancel) leaves a switch that was provisioned, was reachable, was probed
 * successfully, and then QUIETLY REVERTS TO FACTORY five minutes later. Every
 * record meshcraft holds would say the device is adopted.
 *
 * So: a plain atomic commit, with `commit check` in front of it doing the job
 * the timer was reaching for. Recorded here because "why is there no commit
 * confirmed on the one platform that has it" is a fair question to ask of this
 * file, and the answer is a decision rather than an oversight.
 */
export async function commitConfig(
  session: SerialSession,
  log: SoftResetLog,
  what: string,
): Promise<void> {
  log(`commit (${what}) — Junos applies the whole transaction or none of it…`);
  session.mark();
  await session.send("commit");
  var deadline = Date.now() + 180000;
  var silences = 0;
  while (Date.now() < deadline) {
    try {
      await session.expectAny([COMMIT_OK, CLI_ERROR, CONFIG_PROMPT], 30000);
    } catch {
      silences++;
      // A commit is SLOW on this range — the whole configuration is re-read and
      // every daemon is signalled — and it says nothing while it works. The
      // nudge is a bare CR and NEVER a second `commit`: a duplicate commit on a
      // device already committing is how a slow operation becomes a wedged one.
      log(
        `commit: quiet after ${silences * 30}s — sending a bare CR, NOT another ` +
          `"commit". Received meanwhile: ${
            clipTail(session.getSinceMark(), 200) || "nothing at all"
          }`,
      );
      if (silences > 4) {
        throw new Error(
          `commit (${what}) produced no verdict after ${silences * 30}s. Junos is ` +
            `atomic, so the configuration is EITHER fully applied OR not at all — ` +
            `check "show configuration" on the console before power-cycling`,
        );
      }
      await session.sendRaw("\r");
      continue;
    }
    var seen = session.getSinceMark();
    if (MISSING_ROOT_AUTH.test(seen)) {
      throw new Error(
        `commit (${what}) refused: Junos will not commit without a root password ` +
          `("Missing mandatory statement: 'root-authentication'")`,
      );
    }
    var err = seen.match(CLI_ERROR);
    if (err) throw new Error(`commit (${what}) refused: ${err[0].trim()}`);
    if (COMMIT_OK.test(seen)) {
      log(`commit (${what}): complete`);
      return;
    }
  }
  throw new Error(`commit (${what}) timed out`);
}

/** Throw the candidate away. The device is left exactly as it was. */
async function rollback(session: SerialSession, log: SoftResetLog): Promise<void> {
  log("rollback 0 — discarding the candidate configuration; the device is untouched");
  session.mark();
  await session.send("rollback 0");
  await expectReply(session, 20000);
}

/**
 * The interactive password dialogue, for `root-authentication` and for the
 * meshcraft account.
 *
 * "New password:" then "Retype new password:", neither echoed. This is the
 * only way to set a password from a console without a pre-computed crypt hash,
 * and it has a property worth keeping on purpose: the secret never appears in a
 * `set` line, so it never lands in the console transcript the wizard attaches
 * to a failed run.
 *
 * A password Junos refuses on its own policy (too short, too few character
 * classes) is thrown rather than swallowed — the account would not exist, and a
 * run that reported success would hand meshcraft a credential that cannot log in.
 */
export async function setPlainTextPassword(
  session: SerialSession,
  log: SoftResetLog,
  setLine: string,
  label: string,
  password: string,
): Promise<void> {
  // THE REFUSAL IS READ OUT OF THE WHOLE BURST, never out of the winning match.
  //
  // `expectAny` reports the RIGHTMOST pattern, and Junos prints its refusal and
  // then its prompt in one go:
  //
  //   error: minimum length is 6 characters
  //
  //   [edit]
  //   root@sw#
  //
  // so the prompt is always further right than the error. Reading the index
  // alone made a REFUSED password report "the dialogue did not complete" — a
  // message that sends an operator looking at the console wiring instead of at
  // their password. Same shape as the bug aristaEosSoftReset.expectReply carries
  // a comment about, and the same fix: examine the burst.
  var refusalIn = (burst: string): string | null => {
    var m = burst.match(PASSWORD_REFUSED) || burst.match(CLI_ERROR);
    return m ? m[0].trim() : null;
  };

  log(`${label} (the secret is typed into the prompt, never into a "set" line)…`);
  session.mark();
  await session.send(setLine);
  await session.expectAny([NEW_PASSWORD, CLI_ERROR, CONFIG_PROMPT], 20000);
  var burst1 = session.getSinceMark();
  var err1 = refusalIn(burst1);
  if (err1) throw new Error(`${label} refused: ${err1}`);
  if (!NEW_PASSWORD.test(burst1.trimEnd())) {
    throw new Error(
      `${label}: the console came back to the prompt without asking for a password — ` +
        `the statement was not understood on this train`,
    );
  }

  session.mark();
  await session.send(password);
  await session.expectAny(
    [RETYPE_PASSWORD, PASSWORD_REFUSED, CLI_ERROR, CONFIG_PROMPT],
    20000,
  );
  var burst2 = session.getSinceMark();
  var err2 = refusalIn(burst2);
  if (err2) throw new Error(`${label}: the password was refused — ${err2}`);
  if (!RETYPE_PASSWORD.test(burst2.trimEnd())) {
    throw new Error(`${label}: no "Retype new password:" — the dialogue did not complete`);
  }

  session.mark();
  await session.send(password);
  await session.expectAny(
    [PASSWORD_REFUSED, CLI_ERROR, CONFIG_PROMPT, EDIT_BANNER],
    20000,
  );
  var err3 = refusalIn(session.getSinceMark());
  if (err3) throw new Error(`${label}: the password was refused — ${err3}`);
  log(`${label}: accepted into the candidate configuration`);
}

/**
 * `request system zeroize` — THE ONE IRREVERSIBLE STEP.
 *
 * Announced loudly before it is sent, because it cannot be undone from here and
 * because what it erases is wider than most people expect: Juniper's own words
 * are "all plain-text passwords, secrets, and private keys for SSH, local
 * encryption, local authentication, IPsec, RADIUS, TACACS+, and SNMP", plus the
 * log files. Any SSH host key a neighbour had pinned is gone with it.
 *
 * It asks, with NO as the default:
 *   warning: System will be rebooted and may not boot without configuration
 *   Erase all data, including configuration and log files? [yes,no] (no)
 *
 * which is answered with the WORD `yes`. A bare CR — the reflex a Cisco
 * "[confirm]" teaches — would decline it, and the run would carry on happily
 * against a switch that still holds its old configuration.
 */
export async function zeroize(
  session: SerialSession,
  log: SoftResetLog,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  log(
    "IRREVERSIBLE STEP — \"request system zeroize\" erases the configuration, the log " +
      "files, and every stored secret and private key on this switch (SSH host keys, " +
      "local authentication, RADIUS, TACACS+, SNMP). It cannot be undone from the " +
      "console or anywhere else. The switch then reboots into the factory default " +
      "state, where it is reachable ONLY over this serial console",
  );
  session.mark();
  await session.send("request system zeroize");
  var hit: { index: number; match: string };
  try {
    hit = await session.expectAny([ZEROIZE_QUESTION, YESNO, CLI_ERROR], 20000);
  } catch {
    log("zeroize: no question asked — treating the switch as already rebooting");
    return;
  }
  if (hit.index === 2) {
    throw new Error(
      `"request system zeroize" was refused (${hit.match.trim()}) — the switch cannot be ` +
        `reset from here`,
    );
  }
  // The WORD, never the letter and never a bare CR. Cf. [YESNO_WORD]: this
  // prompt's default is NO, so the Cisco "[confirm]" reflex would decline the
  // reset and the run would carry on against an un-wiped switch.
  var answer = yesAnswerFor(hit.match);
  log(
    `zeroize: answering "${answer}" (the platform default is NO — a bare CR, or a ` +
      `bare "y" at a [yes,no] prompt, would decline it)`,
  );
  session.mark();
  await session.send(answer);
}

/**
 * THE THREE STATEMENTS THIS SCRIPT MUST NEVER EMIT, recorded here so that
 * nobody adds them later in good faith.
 *
 * Each one is reachable from "make the console nicer" and each one ends the
 * only way into the switch this procedure has:
 *
 *   set system ports console disable
 *       Console login is enabled by default; this turns it OFF. On a switch
 *       being provisioned from its console, committing this is the end of the
 *       run and the end of every future run — the next door is the LCD panel or
 *       the factory-reset button, in person.
 *   set system ports console insecure
 *       "Disables root logins and UID 0 connections in multiuser mode." On a
 *       freshly zeroized switch `root` is the ONLY account, so this locks the
 *       console against the only credential that exists.
 *   set system ports console log-out-on-disconnect
 *       Logs the session out whenever carrier drops. Juniper's own console
 *       settings for the EX range say "DCD state — Disregard", which is the
 *       terminal side of the same problem: a USB serial adapter that blips its
 *       DCD line mid-transaction would silently log the script out.
 *
 * And one from the LOADER, which is worse because it survives a reboot and the
 * script never sees it coming:
 *
 *   loader> set baudrate=115200
 *       The EX2200 hardware guide's own procedure for attaching a modem. It
 *       persists in the loader environment, so the next boot talks at 115200 to
 *       a terminal still opened at 9600 — a console that is alive and
 *       unreadable, which reads exactly like dead hardware.
 *
 * This list is not defensive programming; it is the answer to "why does this
 * script not set the console type / disable the pager at the system level",
 * which is the shape of the first change someone will want to make here.
 */
export const NEVER_EMIT = [
  "set system ports console disable",
  "set system ports console insecure",
  "set system ports console log-out-on-disconnect",
  "set baudrate=115200",
];

/**
 * Wait for the console to come back after a reboot.
 *
 * A Junos boot is long. The minimum wait is deliberately generous and the
 * pre-reboot buffer is DROPPED first: a stale `>` from before the zeroize would
 * make the script declare the switch back and start configuring a device that is
 * still running a filesystem check.
 */
export async function waitConsoleAfterReboot(
  session: SerialSession,
  log: SoftResetLog,
  timeoutMs: number,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  session.mark();
  log("waiting for the switch to come back (an EX takes 3–6 minutes after a zeroize)…");
  var minBootMs = 90000;
  var deadline = Date.now() + timeoutMs;
  var bootWaitUntil = Date.now() + minBootMs;
  var lastProgress = Date.now();

  while (Date.now() < bootWaitUntil && Date.now() < deadline) {
    aborted(signal);
    try {
      await session.expect(BOOT_MARK, Math.min(5000, Math.max(100, bootWaitUntil - Date.now())));
      log("boot output seen — still waiting for the console to settle…");
      break;
    } catch {
      /* quiet while rebooting */
    }
    if (Date.now() - lastProgress > 10000) {
      log(
        `rebooting (${Math.round(
          (bootWaitUntil - Date.now()) / 1000,
        )}s before looking for a prompt)…`,
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
      await session.expectAny(
        [AMNESIAC, LOGIN_PROMPT, SHELL_PROMPT, CLI_PROMPT],
        Math.min(10000, Math.max(100, deadline - Date.now())),
      );
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
  throw new Error("the switch did not present a console prompt after the reboot (timeout)");
}

/**
 * End Zero Touch Provisioning, in a transaction OF ITS OWN and BEFORE anything
 * else is typed.
 *
 * Cf. [ZTP_MARK]. Juniper's guidance is not to commit a user configuration while
 * ZTP activity is in flight, so the very first commit this script makes is the
 * one that stops it, and it carries nothing else: if the management push had to
 * be re-attempted for any reason, ZTP would already be off rather than racing it.
 *
 * `delete` of an absent statement is an ERROR on Junos, not a no-op, and
 * "statement not found" here means ZTP was not armed — which is good news, not
 * a failure. That one refusal is tolerated; anything else is reported.
 */
export async function stopZTP(
  session: SerialSession,
  log: SoftResetLog,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  var sawZtp = ZTP_MARK.test(session.getCapture().slice(-8000));
  if (sawZtp) {
    log(
      "the console has been talking about zero-touch provisioning / phone-home: a " +
        "zeroized EX is armed for it by design, and Juniper's own guidance is not to " +
        "commit a configuration while it is in flight. Disarming it first",
    );
  } else {
    log("disarming zero-touch provisioning before anything else is written…");
  }
  session.mark();
  await session.send("delete chassis auto-image-upgrade");
  var reply = await expectReply(session, 15000);
  if (reply.kind === "error") {
    if (/statement not found/i.test(reply.match)) {
      log(
        "\"delete chassis auto-image-upgrade\": statement not found — ZTP was not armed " +
          "on this switch. Nothing to disarm, and nothing to commit for it",
      );
      return;
    }
    throw new Error(
      `could not disarm zero-touch provisioning: ${reply.match.trim()}. Committing a ` +
        `configuration while ZTP is running risks the switch installing an image and ` +
        `rebooting in the middle of the push`,
    );
  }
  // THE COMMIT THAT MAY LEGITIMATELY FAIL, AND MUST THEN BE RETRIED.
  //
  // Juniper's ZTP chapter states it plainly: "You cannot commit a
  // configuration while the device is performing the software update process."
  // So on a switch that found a ZTP image server, this first commit is refused
  // for a reason that is neither our mistake nor permanent — and giving up here
  // would strand a switch that is about to finish downloading and reboot into a
  // perfectly ordinary state.
  //
  // Three attempts, a minute apart. The same page says the ZTP retry loop
  // "continues until there is either a successful software upgrade, or an
  // operator manually commits a user configuration and deletes the ZTP
  // configuration" — this is that operator.
  var attempt = 0;
  for (attempt = 1; attempt <= 3; attempt++) {
    aborted(signal);
    try {
      await commitConfig(session, log, "ZTP off");
      return;
    } catch (e) {
      if (attempt === 3) {
        throw new Error(
          `could not commit the end of zero-touch provisioning after ${attempt} attempts: ` +
            `${(e as Error).message || e}. Juniper states a commit is refused while the ` +
            `device is installing a ZTP software image — if that is what is happening, ` +
            `let it finish and re-run, because a switch that reboots mid-push comes back ` +
            `with none of this applied`,
        );
      }
      log(
        `the ZTP-off commit was refused (${
          (e as Error).message || e
        }) — this is expected while a ZTP image download is in flight. Waiting 60s and ` +
          `retrying (attempt ${attempt + 1} of 3)`,
      );
      await session.sleep(60000);
    }
  }
}

/* ───────────────────────────── the provisioning ───────────────────────────── */

export type ProvisionFactoryJunosInput = {
  session: SerialSession;
  log: SoftResetLog;
  onStage?: (stage: JuniperExStage) => void;
  mgmtIface?: string;
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  mgmtMask: string;
  gateway?: string;
  siteVlans?: { id: number; name?: string }[];
  hostname?: string;
  /** Root password to set. Junos refuses to commit without one. */
  rootPassword: string;
  signal?: AbortSignal;
};

/**
 * Everything from "the switch is at a factory console" to "meshcraft can log
 * in over SSH".
 *
 * SHARED by the soft reset (which got here through `request system zeroize`) and
 * the assisted hardware reset (which got here through the loader and recovery
 * mode): from the first factory prompt the two paths are the same switch, so
 * they are the same code.
 */
export async function provisionFactoryJunos(
  input: ProvisionFactoryJunosInput,
): Promise<void> {
  var session = input.session;
  var log = input.log;
  var stage = input.onStage ?? (() => {});

  stage("amnesiac");
  log("factory console: logging in as root (a zeroized EX has no root password)…");
  await ensureOperational(session, "root", "", log, 180000, input.signal);

  // The pager, once. Junos spells it `set cli screen-length 0` — the same verb
  // internal/discovery/fingerprint.go already records for this platform.
  session.mark();
  await session.send("set cli screen-length 0");
  await expectReply(session, 8000);

  // WHICH BOX IS THIS. The model decides the layer-2 dialect (cf. [isELSModel])
  // and the answer goes in the run log either way: on a platform where nobody
  // here has hardware, the model string is the single most useful line a first
  // live run can leave behind.
  var version = await collectShow(session, "show version", 25000);
  var els = isELSModel(version);
  var modelLine = (/(?:^|\n)\s*Model:\s*(\S+)/i.exec(version) || [])[1] || "";
  var junosLine = (/Junos:\s*(\S+)/i.exec(version) || [])[1] || "";
  log(
    `switch reports Model "${modelLine || "unreadable"}", Junos "${
      junosLine || "unreadable"
    }" — layer-2 dialect: ${
      els === true
        ? "ELS (irb, interface-mode)"
        : els === false
          ? "pre-ELS (vlan, port-mode)"
          : "UNKNOWN from the model name; trying ELS first and falling back on the " +
            "commit check, which costs nothing because it applies nothing"
    }`,
  );
  if (LOADER_ABSENT_MODELS.test(version)) {
    log(
      "note: this platform is one of those where Juniper states the loader prompt does " +
        "NOT appear (QFX5100, QFX5200, EX4600, QFX10000) — the assisted hardware path " +
        "cannot be used on it, only this soft path with a working login",
    );
  }

  // THE FACEPLATE, and the interfaces that are not jacks. Read BEFORE anything
  // is written: the trunk list is built from it, and inventing a port name would
  // put a refused line inside an ATOMIC transaction — which on this platform
  // fails the whole commit, not just that line.
  var terse = await collectShow(session, "show interfaces terse", 40000);
  var ifaces = parseInterfacesTerse(terse);
  log(
    `faceplate: ${ifaces.ports.length} revenue port(s)` +
      (ifaces.ports.length
        ? ` (${ifaces.ports[0]} … ${ifaces.ports[ifaces.ports.length - 1]})`
        : "") +
      `; me0 ${ifaces.hasMe0 ? "present" : "absent"}, vme ${
        ifaces.hasVme ? "present" : "absent"
      }`,
  );

  var mgmtVlanName: string | undefined;
  var v0: { id: number; name?: string };
  for (v0 of input.siteVlans || []) {
    var wantId = parseVlanId(input.mgmtIface || "");
    if (wantId !== null && v0.id === wantId && v0.name) mgmtVlanName = v0.name;
  }
  var target = resolveMgmtTarget(input.mgmtIface, mgmtVlanName);

  // The operator named a port the device does not have. Said plainly rather than
  // committed: an address on an interface that does not exist is either refused
  // by the commit check (best case) or accepted into a configuration nothing can
  // reach (worse, and it looks like a success).
  if (target.kind === "oob") {
    // THE CHECK THAT EARNS ITS KEEP, and the reason is an EX2300.
    //
    // On that model `me0` is reported absent — Juniper's own EX2300 / EX2300-C /
    // EX3400 / EX4300 quick starts all name the management port `vme`, and a
    // Juniper community thread ("Management port 'me0' not available on
    // EX2300") has an operator finding a configured `me0` address with no
    // device behind it. That is the worst possible failure shape: the `set`
    // line is accepted, the COMMIT SUCCEEDS, the configuration reads back
    // perfectly, and the switch answers on nothing.
    //
    // `commit check` cannot catch it, because the statement is legal. Only the
    // device's own interface list can, which is why this reads the faceplate
    // before it commits to a name.
    var present = target.iface === "me0" ? ifaces.hasMe0 : ifaces.hasVme;
    if (!present && (ifaces.hasMe0 || ifaces.hasVme)) {
      var other = ifaces.hasVme ? "vme" : "me0";
      throw new Error(
        `the management interface "${target.iface}" does not exist on this switch, but ` +
          `"${other}" does. These are the two names for the out-of-band port — ` +
          `"me0" is the physical RJ-45, "vme" its Virtual-Chassis form — and the ` +
          `wizard will not guess between them. Set the management interface to ` +
          `"${other}", or to "vlan N" for in-band management`,
      );
    }
    if (!present) {
      throw new Error(
        `this switch shows neither "me0" nor "vme": it has no usable out-of-band ` +
          `management port, so the management address must go in-band. Set the ` +
          `management interface to "vlan N"`,
      );
    }
    log(
      `management address will go on the OUT-OF-BAND port "${target.iface}". That is a ` +
        `SEPARATE CABLE from the faceplate: meshcraft will only reach this switch if a ` +
        `proxy can route to ${input.mgmtIp} through that port`,
    );
  } else if (ifaces.ports.length === 0) {
    throw new Error(
      "no revenue port could be read from \"show interfaces terse\", so the trunks that " +
        "carry the management VLAN cannot be built — and without them an in-band address " +
        "on a tagged VLAN answers on nothing. Inventing a port list would put a refused " +
        "line inside an atomic transaction and fail the whole commit",
    );
  } else {
    log(
      `management address will go IN-BAND on VLAN ${target.vlanId} ("${target.vlanName}") ` +
        `via ${els === false ? "vlan" : "irb"}.${mgmtUnit(target.vlanId)}, and every ` +
        `faceplate port will be made a trunk carrying it. A port profile applied later ` +
        `narrows any of them to access in one apply; a port left at the factory default ` +
        `could not carry the management VLAN at all`,
    );
  }

  stage("ztp-off");
  await enterConfig(session, log);
  await stopZTP(session, log, input.signal);

  stage("provisioning");
  var prefixLen = toPrefixLen(input.mgmtMask);
  var dialects: boolean[] = els === null ? [true, false] : [els];
  var di = 0;
  var committed = false;
  var lastDetail = "";
  for (di = 0; di < dialects.length; di++) {
    aborted(input.signal);
    var useEls = dialects[di];
    if (di > 0) {
      log(
        `the ${dialects[di - 1] ? "ELS" : "pre-ELS"} dialect did not pass the commit ` +
          `check (${lastDetail}) — retrying in the ${useEls ? "ELS" : "pre-ELS"} form. ` +
          `Nothing was applied, so this costs only time`,
      );
    }
    // The factory's own automatic configuration comes OFF before the static one
    // goes on — cf. [factoryAutoConfigDeletes]. Tolerant, because half this
    // range never had any of it.
    await tolerantDeletes(
      session,
      log,
      factoryAutoConfigDeletes(target, useEls),
      input.signal,
    );

    var cmds = buildReachabilityCmds({
      els: useEls,
      target,
      ip: input.mgmtIp.trim(),
      prefixLen,
      gateway: input.gateway,
      hostname: input.hostname,
      trunkPorts: target.kind === "inband" ? ifaces.ports : [],
      log,
    });
    log(
      `typing the reachability transaction: ${cmds.length} statement(s) into the ` +
        `CANDIDATE configuration — nothing is applied until the commit`,
    );
    var refused = await typeCandidate(session, log, cmds, input.signal);

    // The passwords, inside the SAME transaction. The root one is not optional:
    // Junos refuses to commit a configuration without it, so a run that skipped
    // it would fail the commit with a message that explains nothing.
    await setPlainTextPassword(
      session,
      log,
      "set system root-authentication plain-text-password",
      "root password",
      input.rootPassword,
    );
    var who = (input.targetUser || "meshcraft").trim() || "meshcraft";
    await typeCandidate(
      session,
      log,
      [{ cmd: `set system login user ${who} class ${MESHCRAFT_CLASS}` }],
      input.signal,
    );
    await setPlainTextPassword(
      session,
      log,
      `set system login user ${who} authentication plain-text-password`,
      `meshcraft account "${who}" (class ${MESHCRAFT_CLASS})`,
      input.targetPassword,
    );

    var check = await commitCheck(session, log);
    lastDetail = check.detail;
    if (!check.ok) {
      if (refused.length) {
        log(`statements the console had already refused: ${refused.join(" ; ")}`);
      }
      await rollback(session, log);
      continue;
    }
    await commitConfig(session, log, "reachability");
    committed = true;
    els = useEls;
    break;
  }
  if (!committed) {
    throw new Error(
      `the reachability configuration did not pass "commit check" in either layer-2 ` +
        `dialect — last verdict: ${lastDetail}. NOTHING WAS APPLIED: this switch is ` +
        `still at its factory default, reachable on the console, and no half-written ` +
        `configuration was left behind`,
    );
  }

  // T2 — the rest of the site's VLANs. Allowed to fail; see [buildSiteVlanCmds].
  var merged = mergeSiteVlans(
    input.siteVlans,
    target.kind === "inband" ? target.vlanId : 0,
    target.kind === "inband" ? target.vlanName : undefined,
  );
  var extra = buildSiteVlanCmds({
    els: els === true,
    target,
    siteVlans: merged,
    trunkPorts: ifaces.ports,
  });
  if (extra.length > 0) {
    log(
      `second transaction: the site's other VLANs (${merged
        .filter((m) => target.kind !== "inband" || m.id !== target.vlanId)
        .map((m) => m.id)
        .join(", ")}) and their membership on the trunks. This one is allowed to fail — a ` +
        `reachable switch missing a VLAN is a minute in the UI; an unreachable switch is ` +
        `another trip to the site`,
    );
    try {
      await typeCandidate(session, log, extra, input.signal);
      var check2 = await commitCheck(session, log);
      if (check2.ok) {
        await commitConfig(session, log, "site VLANs");
      } else {
        log(`site VLANs: commit check refused them (${check2.detail}) — rolling back`);
        await rollback(session, log);
      }
    } catch (e) {
      log(`site VLANs: ${(e as Error).message || e} — rolling back, reachability stands`);
      try {
        await rollback(session, log);
      } catch {
        /* the reachability commit is already on the device */
      }
    }
  }

  // Out of configuration mode before the verify: `show interfaces terse` typed
  // in configuration mode is read as a configuration statement, so every check
  // below would be nonsense.
  //
  // AND THE EXIT CAN ASK A QUESTION. If anything is left uncommitted — a T2
  // that was rolled back on a train where the rollback did not take, an
  // operator who typed something during a pause — Junos answers
  // "exit configuration-mode" with
  //
  //   The configuration has been changed but not committed
  //   Exit with uncommitted changes? [yes,no]
  //
  // and a question left standing takes the NEXT command as its answer: the
  // first verify would be swallowed and every reading after it shifted by one.
  // `yes` is the right answer because the reachability transaction is already
  // committed and anything still in the candidate is something this run decided
  // not to apply.
  session.mark();
  await session.send("top");
  await session.sleep(300);
  await session.send("exit configuration-mode");
  var leaving = await session.expectAny([YESNO, CLI_PROMPT, CLI_ERROR], 15000);
  if (YESNO.test(leaving.match)) {
    log(
      "leaving configuration mode: Junos asked about uncommitted changes — answering " +
        `"${yesAnswerFor(leaving.match)}". The reachability transaction is already ` +
        "committed; anything still in the candidate is what this run chose not to apply",
    );
    session.mark();
    await session.send(yesAnswerFor(leaving.match));
  }
  await session.sleep(1200);

  stage("verify");
  await verifyOnSerial(session, {
    log,
    ip: input.mgmtIp.trim(),
    gateway: input.gateway,
    user: (input.targetUser || "meshcraft").trim() || "meshcraft",
  });
}

/** Read the device back on the console — the sanity pass before the IP probe. */
async function verifyOnSerial(
  session: SerialSession,
  opts: { log: SoftResetLog; ip: string; gateway?: string; user: string },
): Promise<void> {
  var log = opts.log;

  var terse = await collectShow(session, "show interfaces terse | match inet", 20000);
  if (opts.ip && terse.includes(opts.ip)) {
    log(`serial verify: management address ${opts.ip} present and assigned`);
  } else {
    log(
      `serial verify: ${opts.ip} was NOT found in "show interfaces terse | match inet":\n` +
        `${terse.trim().slice(-600)}`,
    );
  }

  var ssh = await collectShow(session, "show configuration system services", 15000);
  if (/\bssh\b/.test(ssh)) {
    log("serial verify: SSH is configured — that is the door meshcraft adopts through");
  } else {
    log(
      `serial verify: SSH does not appear under "system services". meshcraft adopts over ` +
        `SSH, so this is the one that matters:\n${ssh.trim().slice(-300)}`,
    );
  }

  var users = await collectShow(session, "show configuration system login", 15000);
  if (users.includes(opts.user)) {
    log(`serial verify: the account "${opts.user}" exists`);
  } else {
    log(
      `serial verify: "${opts.user}" does not appear under "system login" — meshcraft ` +
        `will not be able to log in:\n${users.trim().slice(-300)}`,
    );
  }

  var ztp = await collectShow(session, "show configuration chassis", 12000);
  if (/auto-image-upgrade/i.test(ztp)) {
    throw new Error(
      "\"chassis auto-image-upgrade\" is STILL in the configuration: this switch is armed " +
        "for zero-touch provisioning and may install an image and reboot on its own. Run " +
        "\"delete chassis auto-image-upgrade\" and commit on the console",
    );
  }

  if (opts.gateway?.trim()) {
    log(`serial verify: ping ${opts.gateway.trim()}…`);
    // `count 3` and `rapid`: the Junos ping is otherwise unbounded and would
    // hold the console until something interrupted it.
    var ping = await collectShow(
      session,
      `ping ${opts.gateway.trim()} count 3 rapid`,
      25000,
    );
    if (/\b[1-9]\d* packets received/i.test(ping)) {
      log("serial verify: gateway ping ok");
    } else {
      log(`serial verify: ping output:\n${ping.trim().slice(-400)}`);
    }
  }
}

/* ───────────────────────────── the runner ───────────────────────────── */

/**
 * The SOFT path: a switch somebody can still log into.
 *
 * Log in, confirm what we are looking at, zeroize, wait out the reboot, then the
 * shared factory provisioning. The loader is never involved — that is
 * juniperExHardReset.ts, for the switch whose root password nobody has.
 */
export async function runJuniperExSoftReset(
  input: JuniperExSoftResetInput,
): Promise<void> {
  var session = input.session;
  var log = input.log;
  var stage = input.onStage ?? (() => {});

  if (!input.mgmtIp.trim() || !input.mgmtMask.trim()) {
    throw new Error("management IP and mask are required");
  }
  if (!input.targetPassword) {
    throw new Error("instance credential secret is required");
  }
  // Fails here rather than after the irreversible step: an unusable management
  // interface is the one input whose consequence is a zeroized switch that
  // cannot be finished.
  resolveMgmtTarget(input.mgmtIface);

  log("Juniper EX (Junos) serial provisioning engine v1 — documented, not yet captured");

  stage("line-check");
  session.mark();
  await session.send("");
  await session.sleep(1800);
  var preText = session.getCapture().slice(-6000);
  var preState = classifyJunosConsole(preText.slice(-1200));
  log(
    `console says: ${
      preState === "unknown"
        ? `nothing this script recognises (${
            clipTail(preText, 120) || "no output at all"
          })`
        : preState
    }`,
  );
  if (preState === "loader" || preState === "loader-window" || preState === "single-user") {
    throw new Error(
      "the switch is sitting in its BOOT LOADER, not in Junos. That is the assisted " +
        "hardware reset's path (it is the door for a switch nobody can log into) — go " +
        "back and choose \"hardware reset\"",
    );
  }

  log("logging in on the console…");
  var logins: { user: string; pass: string; label: string }[] = [
    {
      user: input.currentUser || "root",
      pass: input.currentPassword ?? "",
      label: `credentials entered in the wizard (${input.currentUser || "root"})`,
    },
    { user: "root", pass: "", label: "factory console (root, no password)" },
    {
      user: (input.targetUser || "").trim(),
      pass: input.targetPassword,
      label: `instance credential ${input.targetUser || ""}`,
    },
  ].filter((l, i, arr) => {
    if (!l.user) return false;
    var j = 0;
    for (j = 0; j < i; j++) {
      if (arr[j].user === l.user && arr[j].pass === l.pass) return false;
    }
    return true;
  });

  var opened = false;
  var li = 0;
  for (li = 0; li < logins.length; li++) {
    aborted(input.signal);
    try {
      log(`trying ${logins[li].label}…`);
      await ensureOperational(
        session,
        logins[li].user,
        logins[li].pass,
        log,
        60000,
        input.signal,
      );
      opened = true;
      break;
    } catch (e) {
      log(`${logins[li].label} failed (${(e as Error).message || e})`);
    }
  }
  if (!opened) {
    throw new Error(
      "no credential opened the console. On a Junos EX the way in without a password is " +
        "the BOOT LOADER (interrupt the boot, \"boot -s\", then \"recovery\") — go back " +
        "and choose \"hardware reset\", which drives exactly that",
    );
  }

  session.mark();
  await session.send("set cli screen-length 0");
  await expectReply(session, 8000);

  // What we are about to wipe, for the record. On a platform nobody here has
  // hardware for, the model and version read from the device BEFORE the reset is
  // the only evidence that survives if the run goes wrong.
  var version = await collectShow(session, "show version", 25000);
  log(
    `about to zeroize: Model "${
      (/(?:^|\n)\s*Model:\s*(\S+)/i.exec(version) || [])[1] || "unreadable"
    }", Junos "${(/Junos:\s*(\S+)/i.exec(version) || [])[1] || "unreadable"}"`,
  );

  stage("zeroize");
  await zeroize(session, log, input.signal);

  stage("booting");
  await waitConsoleAfterReboot(session, log, 600000, input.signal);
  // Never inherit a pre-zeroize prompt into the factory console's state machine.
  session.clearCapture();

  await provisionFactoryJunos({
    session,
    log,
    onStage: input.onStage,
    mgmtIface: input.mgmtIface,
    targetUser: input.targetUser,
    targetPassword: input.targetPassword,
    mgmtIp: input.mgmtIp,
    mgmtMask: input.mgmtMask,
    gateway: input.gateway,
    siteVlans: input.siteVlans,
    hostname: input.hostname,
    // THE ROOT PASSWORD IS THE INSTANCE SECRET, and that is a deliberate
    // choice rather than a convenience. Junos will not commit without a root
    // password, so one must be set; inventing a random one would leave the
    // console door behind a secret nothing records, and leaving it empty is not
    // an option the platform offers. meshcraft never logs in as root (cf.
    // [MESHCRAFT_CLASS] — root SSH is denied, and this push writes the denial
    // explicitly), so the value's only use is the console in front of the rack
    // — where whoever is holding the cable is the person the instance
    // credential belongs to anyway.
    rootPassword: input.targetPassword,
    signal: input.signal,
  });

  log(
    `serial side done — SSH login is now ${
      (input.targetUser || "meshcraft").trim() || "meshcraft"
    } / <instance credential secret> (class ${MESHCRAFT_CLASS}); root has the same secret ` +
      `for the console only. Caller will probe the management IP`,
  );
}

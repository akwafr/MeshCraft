/**
 * Juniper EX ASSISTED HARDWARE RESET over Web Serial — the door for a switch
 * nobody can log into.
 *
 * The soft reset (juniperExSoftReset.ts) needs a working login; this flow exists
 * for the switch whose root password is lost, which on this platform is the
 * ordinary second-hand switch. It is Juniper's own root-password-recovery
 * procedure, driven by the wizard, with the operator holding the one thing
 * software cannot: the power cable.
 *
 * # WHAT THE OPERATOR SEES, AND WHY THE WIZARD HAS TO CATCH THE WINDOW
 *
 * A booting EX prints
 *
 *   Hit [Enter] to boot immediately, or space bar for command prompt.
 *   Booting [/kernel] in 1 second...
 *
 * and counts down. The window is a handful of seconds at best — Juniper's own
 * samples show "in 9 seconds..." and the captured EX2200 below shows "in 1
 * second...", and the delay is a loader variable, so the honest answer is
 * "somewhere between one and ten, and assume one". That is far too short for
 * a human reading an instruction on a screen, which is the entire reason this
 * file exists: the operator power-cycles, and the SCRIPT catches the window.
 *
 * # THERE ARE TWO DOORS, NOT ONE, AND THEY TAKE DIFFERENT KEYS
 *
 * This is the single most important fact in this file, and the one a procedure
 * written from a single blog post gets wrong.
 *
 *   PATH A — the classic FreeBSD loader.
 *     Models: EX2200, EX2200-C, EX3200, EX3300, EX4200, EX4500, EX4550 — and
 *     EX4300 on older Junos.
 *     Key: the SPACE BAR during the countdown.
 *     Lands on:  loader>   (or  ok  on the M/T-series loader Juniper's generic
 *                sample transcript shows; both are accepted here)
 *     Then:      boot -s
 *                → "Enter full pathname of shell or 'recovery' for root
 *                   password recovery or RETURN for /bin/sh:"
 *                → recovery
 *                → the Junos CLI, as root.
 *
 *   PATH B — "Junos OS with Upgraded FreeBSD", which is menu-driven.
 *     Models: EX2300, EX3400 (Juniper has a page named exactly "Understanding
 *     Junos OS with Upgraded FreeBSD for EX2300 and EX3400"), plus EX4600 and
 *     QFX5100.
 *     Key: CTRL-C, not the space bar, in a window of roughly three seconds.
 *     Lands on:  a numbered MENU ending in "Choice:".
 *     Then:      "5. [M]ore options" → "2. Recovery mode - [C]LI"
 *                → the Junos CLI, as root. No boot -s, no pathname question.
 *
 *     AND `boot -s` IS THE WRONG DOOR ON THESE MODELS. Juniper's own note for
 *     this path says single-user mode there gives only a shell and "there is
 *     limited support and password recovery is not possible using this option".
 *     A procedure that sent `boot -s` to an EX2300 would reach a prompt, do
 *     something, and not recover the password.
 *
 * The models cannot be told apart BEFORE the boot — there is no configuration to
 * read and no banner yet. So this script hammers the window with whichever keys
 * are SAFE at that instant and branches on what answers: "Choice:" means
 * Path B; "loader>" or "ok" means Path A.
 *
 * "Whichever are safe" and not simply "both" — that distinction is the fix
 * of 2026-10-25 and it is argued out under "WHICH KEY, WHEN" below. A space at
 * a Path-B menu is ignored and a CTRL-C at the Path-A *countdown* is harmless,
 * but a CTRL-C at the Path-A *U-Boot*, which runs minutes earlier on the same
 * boot, is U-Boot's own break-in key and lands somewhere this script cannot
 * drive.
 *
 * # THE MENU ITEM NUMBERS ARE NOT STABLE — THE BRACKETED LETTERS ARE
 *
 * Juniper's pages quote individual menu items but never a whole block, and at
 * least two variants of item 1 exist across releases ("Boot [J]unos volume"
 * vs "Boot [D]irectly from Junos volume"). The numbering of the Options menu
 * moves too. So this script answers the menu with the BRACKETED HOTKEY LETTER
 * (`m` then `c`), never with a number, and matches on the letters rather than on
 * the positions.
 *
 * # Platforms where this path is IMPOSSIBLE
 *
 * Juniper states the loader prompt does not appear at all on QFX5100, QFX5200,
 * EX4600 and QFX10000 — cf. LOADER_ABSENT_MODELS in juniperExSoftReset.ts.
 * (EX4600 appears on both that list and the Path-B list, because it has the menu
 * rather than a loader prompt; the menu is what this script drives.)
 *
 * # A KNOWN DEFECT WORTH RECOGNISING
 *
 * On EX2200 / EX3200 / EX3300 / EX4200 / EX4500 / EX4550 running 15.1R1 through
 * 15.1R6, `boot -s` at the loader boots the ALTERNATE SLICE instead of entering
 * single-user mode. The documented way round it is U-Boot (CTRL-C during the USB
 * scan, prompt "=>") and clearing `boot_unattended`. This script does not
 * drive U-Boot — it RECOGNISES it and says so, because "I am at a => prompt"
 * is a diagnosis an operator can act on and a timeout is not.
 *
 * # WHAT A REAL EX2200-48P-4G ACTUALLY PRINTS, AND THE TWO DEFECTS IT EXPOSED
 *
 * 2026-10-25, first run against hardware (EX2200-48P-4G 4.18, U-Boot 1.1.6).
 * It failed, and the operator's full boot trace says why. The trace, trimmed:
 *
 *   U-Boot 1.1.6 (Jul 26 2011 - 03:19:50)
 *   Board: EX2200-48P-4G 4.18
 *   USB:   scanning bus for devices... 3 USB Device(s) found
 *   ELF file is 32 bit
 *   Consoles: U-Boot console
 *   FreeBSD/arm U-Boot loader, Revision 1.1
 *   bootsequencing is enabled
 *   Loading /boot/defaults/loader.conf
 *   /kernel data=0x9a9df0+0xfe3bc syms=[...]
 *   Hit [Enter] to boot immediately, or space bar for command prompt.
 *   Booting [/kernel] in 1 second...
 *   loader>
 *
 * DEFECT 1 — THE RUN DIED ON THE FIRST LINE OF THE BOOT. `classifyBootDoor`
 * used to return "uboot" on the BANNER (`/U-Boot \d/`), not on the prompt,
 * and `catchBootWindow` treats "uboot" as a fatal diagnosis. Every pre-ELS EX
 * opens its boot with exactly that banner, so the wizard aborted roughly half a
 * second after the operator plugged the cord back in, reporting "the console is
 * at a U-BOOT prompt" about a console that was nowhere near one. U-Boot is a
 * destination only when its PROMPT ("=>") is sitting at the end of the
 * buffer; the banner is ordinary chatter and is now read as such.
 *
 * DEFECT 2 — THE COUNTDOWN LINE DID NOT MATCH. The regex wanted
 * `Booting \[kernel\]`; this loader prints `Booting [/kernel]`, with a slash.
 * The header above claims a console attached mid-countdown is supported on the
 * strength of that line alone, and it was not. Matched on the shape now
 * ("Booting [anything] in N second"), not on the payload's spelling.
 *
 * DEFECT 3 — CTRL-C WAS BEING FIRED INTO U-BOOT'S OWN AUTOBOOT. See the
 * section below; it is a decision, not just a fix.
 *
 * The window here is "in 1 second", the short end of the documented range.
 * That is the budget the hammer below is sized against.
 *
 * # WHICH KEY, WHEN — AND WHY NOT BOTH FROM THE START
 *
 * There are two interrupt keys and three consoles that read them, and the
 * earlier code sent both keys from the moment the watch opened. On this
 * hardware that is wrong, because U-Boot runs FIRST and CTRL-C is precisely the
 * documented way to break INTO it ("CTRL-C during the USB scan", prompt
 * "=>"). A hammer that starts at power-on therefore races U-Boot's autoboot
 * with U-Boot's own break-in key, and when it wins it lands at "=>" — a
 * prompt this wizard does not drive, and which it then reports as a failure.
 * Both halves of that were firing on the same run.
 *
 * So the keys are now EARNED rather than assumed, by `interruptKeys` below:
 *
 *   - SPACE is sent always. It is the key the classic loader's own line names
 *     ("space bar for command prompt"), it is not U-Boot's break-in key, and
 *     it is inert at a Path-B menu and at a `loader>` prompt alike.
 *   - CTRL-C is sent only on positive evidence that U-Boot is not the thing
 *     listening: either the FreeBSD loader has announced itself (past U-Boot —
 *     CTRL-C there drops into the same loader the space bar would have), or
 *     boot chatter has been running with no U-Boot banner in it at all, which
 *     is the Path-B generation whose ONLY key is CTRL-C.
 *
 * Nothing is lost on Path B: those boxes print no U-Boot banner, so CTRL-C is
 * armed from their first line of output.
 *
 * # THE HAMMER IS CONTINUOUS, SO THERE IS NO REACTION TIME TO BE TOO SLOW
 *
 * Worth stating plainly, because "the wizard reacted too late" is the obvious
 * suspicion and it is not what happened. The keys are not sent in answer to the
 * countdown line; they are sent on a fixed tick for the whole watch, so the
 * window cannot be missed by arriving late — only by sending the wrong key,
 * which is defect 3, or by aborting first, which is defect 1. The tick is 120 ms
 * rather than the old 200 ms anyway: a "in 1 second" window now takes eight
 * spaces instead of five, and a space costs about a millisecond on the wire at
 * 9600 baud.
 *
 * # Status of what is written here
 *
 * The boot window and the loader prompt are now CAPTURED (the trace above).
 * Everything past `boot -s` — the single-user question, the Path-B menu, the
 * zeroize — remains DOCUMENTED, NOT CAPTURED. Sources are named at each use
 * site; the per-string confidence is recorded in the handover notes.
 */

import type { SerialSession } from "./serialExpect";
import {
  LOADER_ABSENT_MODELS,
  provisionFactoryJunos,
  resolveMgmtTarget,
  zeroize,
  waitConsoleAfterReboot,
  type JuniperExStage,
  type SoftResetLog,
} from "./juniperExSoftReset.ts";

export type JuniperExHardResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  onStage?: (stage: JuniperExStage) => void;
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  mgmtMask: string;
  gateway?: string;
  mgmtIface?: string;
  siteVlans?: { id: number; name?: string }[];
  hostname?: string;
  signal?: AbortSignal;
};

/**
 * The boot window, matched on the STABLE FRAGMENT of each of its two lines and
 * on no model's spelling of either.
 *
 * The lines are, on the captured EX2200:
 *
 *   Hit [Enter] to boot immediately, or space bar for command prompt.
 *   Booting [/kernel] in 1 second...
 *
 * "space bar for command prompt" is the half that says what to press, and is
 * the one fragment every documented variant of the first line carries — the
 * "Hit [Enter]" half is matched too, but separately, because an EX that
 * reworded it must not take the whole line down with it.
 *
 * The second line is matched on its SHAPE — "Booting [something] in N
 * second" — and that is the fix for a real defect: the regex used to require
 * `Booting \[kernel\]` and this loader prints `Booting [/kernel]`, so a console
 * attached mid-countdown (which the header claims to support) matched nothing
 * at all. What is inside the brackets is a path, and paths differ across
 * releases and across the two slices; the bracket-N-second shape does not.
 * Singular and plural both, because the countdown ends at "1 second".
 */
const LOADER_WINDOW =
  /space ?bar for command prompt|Hit \[Enter\] to boot immediately|Booting \[[^\]\n]*\] in \d+ seconds?/i;

/** An earlier anchor: the line the loader prints just before the countdown. */
const LOADER_CONF = /Loading \/boot\/defaults\/loader\.conf/i;

/**
 * THE FREEBSD LOADER HAS THE CONSOLE — i.e. U-Boot is done and no longer
 * listening for its break-in key. This is what arms CTRL-C on Path A; cf. the
 * header's "which key, when".
 *
 * `FreeBSD/arm U-Boot loader, Revision 1.1` is the loader naming itself (and
 * note that it contains the string "U-Boot" without being U-Boot — which is
 * why UBOOT_BANNER below requires a version number after it). `Loading /boot/`
 * is the loader reading its own configuration. Either one, or the countdown
 * itself, means the window being watched for is this loader's.
 */
const LOADER_PHASE = /FreeBSD\/\w+ U-Boot loader|Loading \/boot\//i;

/**
 * The Path-A loader, in both of its documented spellings.
 *
 * `loader>` is what the EX2200 hardware guide's own console-speed procedure
 * shows; `ok` is what Juniper's generic Junos recovery transcript shows (the
 * M/T-series loader). Accepting both costs nothing and a procedure that knew
 * only one of them would hang on half the fleet.
 *
 * `ok` is anchored hard — at the start of a line and at the end of the buffer —
 * because the word appears inside ordinary boot chatter constantly, and a loose
 * match would have the script typing `boot -s` into a kernel message.
 */
const LOADER_PROMPT = /(?:^|\n)\s*(?:loader>|ok)\s*$/m;

/**
 * U-Boot, which is a diagnosis rather than a destination — but ONLY at its
 * prompt. Cf. defect 1 in the header: the banner used to be treated as the
 * destination, and since every pre-ELS EX opens its boot with it, the wizard
 * aborted on the first line of a perfectly normal boot.
 *
 * `UBOOT_PROMPT` is the destination test. `UBOOT_BANNER` is now only evidence
 * of WHICH GENERATION is booting, used to decide whether CTRL-C is safe to
 * send; it requires a dotted version so that the FreeBSD loader's own
 * "FreeBSD/arm U-Boot loader" line cannot be mistaken for U-Boot itself.
 */
const UBOOT_PROMPT = /(?:^|\n)\s*=>\s*$/m;
const UBOOT_BANNER = /U-Boot \d+\.\d+/i;

/** The Path-B menu. The prompt is the stable part; the numbering is not. */
const MENU_CHOICE = /(?:^|\n)\s*Choice:\s*$/m;
const MENU_MAIN = /\[J\]unos volume|\[D\]irectly from Junos volume|Boot Junos volume in \[S\]afe mode/i;
const MENU_MORE = /\[M\]ore options/i;
const MENU_RECOVERY_CLI = /Recovery mode\s*-\s*\[C\]LI/i;

/**
 * The Path-A single-user question, matched loosely on purpose.
 *
 * The full documented form is
 *   "Enter full pathname of shell or 'recovery' for root password recovery or
 *     RETURN for /bin/sh:"
 * and shorter variants are in circulation. The two words every form carries are
 * `pathname` and `shell`, so those are what is matched. The answer is `recovery`
 * and NOT a bare RETURN: RETURN gives a raw /bin/sh with no CLI and no commit,
 * from which the whole procedure would have to be done by hand with `mount -uw`.
 */
const SINGLE_USER_QUESTION = /pathname of (?:the )?shell/i;

/** Junos is up as root: the shell (`%`), operational mode (`>`), or a login. */
const JUNOS_REACHED = /(?:^|\n)[^\n]*\blogin:\s*$|%\s*$|>\s*$/m;

/** Boot chatter, to tell "power is back" from "the operator is still looking
 * for the socket". */
const BOOT_CHATTER =
  /U-Boot|FreeBSD|Loading \/boot|Booting \[|BIOS|POST|Consoles:|CPU:|DRAM:|Flash:|Board:|Copyright \(c\) 199|Juniper/i;

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("cancelled");
}

/** Which door the boot window opened. */
export type BootDoor = "loader" | "menu" | "uboot";

/**
 * Classify what answered the boot interrupt. PURE, and the fixtures in the test
 * file are why it is a function.
 *
 * Deliberately NOT "whichever matched first": a Path-B boot prints plenty of
 * FreeBSD text before its menu, and a Path-A boot may show the countdown line
 * and the loader prompt in the same burst. The MENU is checked first because
 * "Choice:" is unambiguous and because sending `boot -s` to a Path-B box is
 * the one wrong move with no recovery inside this run (cf. the header).
 *
 * A DOOR IS A PROMPT, NEVER A BANNER. The U-Boot test is `UBOOT_PROMPT` alone;
 * the banner that used to be accepted here is the whole of defect 1 in the
 * header, and it made every real EX2200 boot look like a dead end.
 */
export function classifyBootDoor(tail: string): BootDoor | null {
  if (MENU_CHOICE.test(tail) || MENU_MORE.test(tail) || MENU_MAIN.test(tail)) {
    return "menu";
  }
  if (UBOOT_PROMPT.test(tail)) return "uboot";
  if (LOADER_PROMPT.test(tail)) return "loader";
  return null;
}

/**
 * WHICH INTERRUPT KEYS IT IS SAFE TO BE HOLDING DOWN RIGHT NOW, given what the
 * console has said so far. PURE, exported, and pinned by the fixture test —
 * this is the decision the header's "which key, when" argues for.
 *
 * SPACE is always in the list. CTRL-C joins it only on evidence that U-Boot is
 * not the thing listening, because CTRL-C is U-Boot's own documented break-in
 * key and winning that race lands at "=>", which this wizard does not drive:
 *
 *   - the FreeBSD loader has named itself or is reading its config → past
 *     U-Boot, CTRL-C is harmless and drops into the same loader;
 *   - the countdown is on screen → likewise, and it is the last moment to act;
 *   - there is boot chatter but no U-Boot banner anywhere in it → the Path-B
 *     generation (EX2300 / EX3400 / EX4600), whose ONLY key is CTRL-C;
 *   - nothing yet, or U-Boot and nothing after it → SPACE ALONE.
 *
 * The last clause is the conservative one and it is deliberate: before any
 * output at all the operator may be seconds from plugging the cord back in, and
 * a CTRL-C waiting there would meet U-Boot first.
 */
export function interruptKeys(tail: string): string[] {
  const SPACE = " ";
  const CTRL_C = "\x03";
  if (LOADER_PHASE.test(tail) || LOADER_WINDOW.test(tail)) return [SPACE, CTRL_C];
  if (UBOOT_BANNER.test(tail)) return [SPACE];
  if (BOOT_CHATTER.test(tail)) return [SPACE, CTRL_C];
  return [SPACE];
}

/** How often the interrupt keys go out. Cf. the header: a "in 1 second"
 * window takes eight ticks at this rate, and a keystroke costs about a
 * millisecond on the wire at 9600 baud. */
const HAMMER_TICK_MS = 120;

/**
 * Hammer the boot window until a door answers, with whatever keys are safe at
 * this instant — `keys()` is re-asked every tick and is `interruptKeys` above.
 *
 * Every key is sent RAW: a CR behind either would be "boot immediately",
 * which is precisely the outcome being avoided. ENTER IS NEVER SENT HERE, and
 * that is not an oversight to be tidied up later — the loader's own line says
 * "Hit [Enter] to boot immediately", so a stray CR during this watch does the
 * exact opposite of what the whole file is for.
 *
 * Sent on a fixed tick for the whole watch rather than once when the countdown
 * line is seen. Three reasons, and the first two are the ones that bite:
 *
 *   - the countdown can be as short as one second — the captured EX2200 prints
 *     "in 1 second" — and the line announcing it may arrive in the same read
 *     as the boot that follows it;
 *   - the operator's power cycle happens at an unknown moment, so there is no
 *     "now" to aim at;
 *   - the keys that ARE sent are harmless where they land. At the loader a
 *     space is whitespace; at a Path-B menu it does nothing; before the window
 *     it is swallowed by a device that is not listening yet. The one key that
 *     was NOT harmless is CTRL-C in front of U-Boot, and `keys()` is what
 *     keeps it out of there.
 */
async function hammerInterrupts(
  session: SerialSession,
  keys: () => string[],
  stopAt: () => boolean,
): Promise<void> {
  while (!stopAt()) {
    var now = keys();
    var i = 0;
    for (i = 0; i < now.length; i++) {
      if (stopAt()) return;
      try {
        await session.sendRaw(now[i]);
      } catch {
        // The port can be momentarily unwritable while the adapter re-asserts
        // after a power cycle. Not a failure — the next tick tries again.
      }
    }
    await new Promise<void>((resolve) => setTimeout(resolve, HAMMER_TICK_MS));
  }
}

/** Path A: `boot -s`, then answer the pathname question with `recovery`. */
async function enterRecoveryViaLoader(
  session: SerialSession,
  log: SoftResetLog,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  log("loader reached — \"boot -s\" for single-user mode");
  session.mark();
  await session.send("boot -s");

  // The single-user question, or — on a 15.1R1…R6 box hitting the known defect —
  // a normal boot into the alternate slice. Both are recognised, because
  // "the switch booted normally" is a diagnosis and a timeout is not.
  var hit: { index: number; match: string };
  try {
    hit = await session.expectAny([SINGLE_USER_QUESTION, JUNOS_REACHED], 300000);
  } catch {
    throw new Error(
      "\"boot -s\" produced neither the single-user question nor a login prompt within " +
        "five minutes. On EX2200 / EX3200 / EX3300 / EX4200 / EX4500 / EX4550 running " +
        "Junos 15.1R1 to 15.1R6 this command boots the ALTERNATE SLICE instead of " +
        "entering single-user mode — the way round it is U-Boot (CTRL-C during the USB " +
        "scan, prompt \"=>\") and clearing \"boot_unattended\", which this wizard does " +
        "not drive. Do that by hand, or use the LCD panel / factory-reset button",
    );
  }
  if (hit.index === 1) {
    throw new Error(
      "\"boot -s\" led straight to a normal Junos boot rather than to single-user mode. " +
        "That is the documented behaviour of Junos 15.1R1 to 15.1R6 on the pre-ELS EX " +
        "range: it boots the alternate slice. Recover through U-Boot by hand (CTRL-C " +
        "during the USB scan gives \"=>\", then clear \"boot_unattended\"), or use the " +
        "LCD panel's FACTORY DEFAULT entry",
    );
  }

  log(
    "single-user prompt — answering \"recovery\" (a bare RETURN would give a raw " +
      "/bin/sh with no CLI and no commit)",
  );
  session.mark();
  await session.send("recovery");
}

/**
 * Path B: the numbered menu, answered by its BRACKETED LETTERS.
 *
 *   Main menu     → `m`   ("5. [M]ore options")
 *   Options menu  → `c`   ("2. Recovery mode - [C]LI")
 *
 * The letters and not the numbers, because the numbering moves across releases
 * while the mnemonics do not — cf. the header. Each is sent as a line (the menu
 * reads a line, not a keystroke) and the arrival is confirmed before the next.
 */
async function enterRecoveryViaMenu(
  session: SerialSession,
  log: SoftResetLog,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  log(
    "this is an upgraded-FreeBSD EX (the EX2300 / EX3400 / EX4600 generation): it has a " +
      "boot MENU rather than a loader prompt, and \"boot -s\" would not recover the " +
      "password here — Juniper says so explicitly. Driving the menu instead",
  );

  // Make sure a menu is actually on screen before answering it.
  session.mark();
  await session.sendRaw("\r");
  try {
    await session.expectAny([MENU_CHOICE, MENU_MAIN, MENU_MORE], 60000);
  } catch {
    log("no \"Choice:\" seen yet — answering anyway; the menu re-prints on a bad entry");
  }

  log("menu: \"[M]ore options\"");
  session.mark();
  await session.send("m");
  try {
    await session.expectAny([MENU_RECOVERY_CLI, MENU_CHOICE], 60000);
  } catch {
    log("the options menu did not announce itself — continuing to \"[C]LI\" regardless");
  }

  log("menu: \"Recovery mode - [C]LI\"");
  session.mark();
  await session.send("c");
}

/**
 * Watch for the boot window and take whichever door opens.
 *
 * Up to eight minutes: finding a power socket behind a rack takes as long as it
 * takes, and an EX's own pre-loader POST is not instant either.
 */
async function catchBootWindow(
  session: SerialSession,
  log: SoftResetLog,
  stage: (s: JuniperExStage) => void,
  signal?: AbortSignal,
): Promise<BootDoor> {
  session.mark();
  var done = false;
  // Fire-and-forget: the hammer must never be something the watch awaits, for
  // the same reason the Catalyst line-check poke is fire-and-forget — a busy
  // Web Serial writer would otherwise freeze the whole watch, which is how two
  // field reports of "stuck at test the line" happened.
  //
  // The key set is recomputed from the live tail on every tick (cf.
  // `interruptKeys`), so CTRL-C arms itself the moment the FreeBSD loader takes
  // the console and never before.
  void hammerInterrupts(
    session,
    () => interruptKeys(session.getSinceMark().slice(-3000)),
    () => done || !!signal?.aborted,
  );

  var deadline = Date.now() + 480000;
  var sawPower = false;
  var armedCtrlC = false;
  var lastProgress = Date.now();
  try {
    for (;;) {
      if (signal?.aborted) throw new Error("cancelled");
      if (Date.now() > deadline) {
        throw new Error(
          "no boot loader and no boot menu appeared within eight minutes. Either the " +
            "switch was never power-cycled, or this is one of the platforms Juniper says " +
            "has no loader prompt at all (QFX5100, QFX5200, QFX10000) — on those the " +
            "console cannot interrupt the boot and the way in is the factory-reset " +
            "button or the LCD panel",
        );
      }
      var tail = session.getSinceMark();
      var door = classifyBootDoor(tail.slice(-3000));
      if (door === "uboot") {
        throw new Error(
          "the console is at a U-BOOT prompt (\"=>\"), not at the Junos loader. The wizard " +
            "does not send CTRL-C before the FreeBSD loader has the console, so something " +
            "else broke the boot there — a keystroke on the console, or a unit configured " +
            "to break on any key. U-Boot is the workaround path for the Junos 15.1 \"boot " +
            "-s boots the alternate slice\" defect and this wizard does not drive it: type " +
            "\"boot\" at the \"=>\" prompt to let the loader run and start again, clear " +
            "\"boot_unattended\" by hand, or reset the switch from its LCD panel",
        );
      }
      if (door) {
        done = true;
        stage("loader");
        return door;
      }
      if (!sawPower && (BOOT_CHATTER.test(tail) || LOADER_CONF.test(tail) || LOADER_WINDOW.test(tail))) {
        sawPower = true;
        log(
          "boot output detected — power is back, and the wizard is holding the SPACE bar " +
            "down through the boot. CTRL-C is deliberately withheld for now: it is U-Boot's " +
            "own break-in key, and reaching U-Boot is not the way in",
        );
      }
      // Said once, when it changes, because "which key is it pressing" is the
      // one question a failed run turns on.
      if (!armedCtrlC && interruptKeys(tail.slice(-3000)).length > 1) {
        armedCtrlC = true;
        log(
          "the FreeBSD loader has the console — CTRL-C is now sent alongside the space bar " +
            "(past U-Boot it is harmless, and on the EX2300 / EX3400 generation it is the " +
            "only key that opens the boot menu)",
        );
      }
      if (Date.now() - lastProgress > 15000) {
        log(
          `watching for the boot window (${Math.round(
            (deadline - Date.now()) / 1000,
          )}s left) — ${
            tail.trim().slice(-120).replace(/\s+/g, " ") || "no serial output yet"
          }`,
        );
        lastProgress = Date.now();
      }
      // 150 ms, not the old 500: the poll is also what arms CTRL-C from the
      // tail, and a third of a second spent still sending the wrong key set is
      // a third of a second of a one-second window.
      await new Promise<void>((resolve) => setTimeout(resolve, 150));
    }
  } finally {
    done = true;
  }
}

/**
 * The whole assisted reset.
 *
 * The operator's ONE gesture is the power cycle; everything else is the script's.
 * That is a deliberate difference from the Catalyst flow, where the operator
 * also has to hold a button for fifteen seconds and release it at the right
 * moment: on this platform there is no button to hold, only a window too short
 * to aim at by hand, so the division of labour is cleaner.
 */
export async function runJuniperExHardReset(
  input: JuniperExHardResetInput,
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
  // Validated BEFORE the power cycle, let alone before the zeroize: an unusable
  // management interface is the one input whose consequence would be a wiped
  // switch that cannot be finished.
  resolveMgmtTarget(input.mgmtIface);

  log("Juniper EX assisted hardware-reset engine v1 — documented, not yet captured");

  // 1 — LINE CHECK. A poke, and an honest report either way. Silence is what a
  // powered-off switch says, and the next step powers it off anyway. Nothing
  // here may block (cf. ciscoIosHardReset: this exact step, awaited, is how two
  // "stuck at test the line" reports happened).
  stage("line-check");
  session.mark();
  var poke = { state: "pending" as "sent" | "failed" | "pending" };
  void session.send("").then(
    () => {
      poke.state = "sent";
    },
    () => {
      poke.state = "failed";
    },
  );
  await new Promise<void>((resolve) => setTimeout(resolve, 2500));
  var heard = session.getSinceMark().trim();
  if (heard.length > 0) {
    log("console line alive — the switch answered the poke");
    if (LOADER_ABSENT_MODELS.test(heard)) {
      log(
        "warning: the console text names a platform Juniper says has NO loader prompt " +
          "(QFX5100, QFX5200, EX4600, QFX10000). The boot window may not exist here",
      );
    }
  } else if (poke.state === "sent") {
    log(
      "console line silent — normal for a switch that is powered off; the wiring is " +
        "proven the moment boot output appears",
    );
  } else {
    log(
      "line poke did not go through (port busy?) — continuing anyway: the next stage " +
        "only LISTENS until the boot window answers",
    );
  }

  // 2 — THE OPERATOR'S ONE GESTURE. Announced with what comes after it, because
  // the step after the next one cannot be undone.
  stage("await-power-cycle");
  log(
    "ACTION REQUIRED — power-cycle the switch now (pull the power cord, or both cords " +
      "on a dual-supply unit, wait five seconds, plug back in). PRESS NOTHING, and ABOVE " +
      "ALL NOT ENTER: the loader's own line is \"Hit [Enter] to boot immediately\", so an " +
      "Enter during the countdown is the one keystroke that loses the window. It is one " +
      "second long on an EX2200 and the wizard holds the space bar down through it for you",
  );
  log(
    "WHAT HAPPENS NEXT, so it is read before it happens: once the wizard is in recovery " +
      "mode it runs \"request system zeroize\", which ERASES this switch's configuration, " +
      "its log files, and every stored secret and private key on it (SSH host keys, local " +
      "authentication, RADIUS, TACACS+, SNMP). That step cannot be undone, from the " +
      "console or anywhere else. Cancel now if this switch holds anything worth keeping",
  );

  var door = await catchBootWindow(session, log, stage, input.signal);

  stage("single-user");
  if (door === "menu") {
    await enterRecoveryViaMenu(session, log, input.signal);
  } else {
    await enterRecoveryViaLoader(session, log, input.signal);
  }

  // 3 — RECOVERY MODE. Both doors land in the Junos CLI as root; the boot that
  // precedes it is a full one (consistency checks, filesystems, management
  // services), so the wait is generous.
  log("waiting for recovery mode to bring the Junos CLI up (this is a full boot)…");
  await waitConsoleAfterReboot(session, log, 600000, input.signal);

  // 4 — WIPE. From here the two paths and the soft reset are the same switch,
  // so they are the same code.
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
    // Same reasoning as the soft path: Junos refuses to commit without a root
    // password, meshcraft never logs in as root, and whoever is holding the
    // console cable is the person the instance credential belongs to.
    rootPassword: input.targetPassword,
    signal: input.signal,
  });
}

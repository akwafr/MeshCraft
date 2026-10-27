/**
 * Boot-window checks for the Junos EX assisted hardware reset:
 *   node --experimental-strip-types src/pages/add/juniperExHardReset.test.ts
 *
 * Fixtures are the DOCUMENTED Junos boot output (the module header names each
 * source and states that none of it has been captured on hardware).
 *
 * WHAT IS PINNED HERE IS THE ONE WRONG MOVE WITH NO RECOVERY INSIDE A RUN:
 * sending "boot -s" to an EX2300 / EX3400 / EX4600. Juniper's own note for
 * that generation says single-user mode there gives only a shell and "password
 * recovery is not possible using this option", so the menu — driven by its
 * BRACKETED HOTKEY LETTERS, never by its item numbers, which move across
 * releases — is the only door. The two paths are therefore asserted to be
 * mutually exclusive, not merely each correct.
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";
import {
  classifyBootDoor,
  interruptKeys,
  runJuniperExHardReset,
} from "./juniperExHardReset.ts";

/* ─────────────────────────── which door answered ─────────────────────────── */

function bootDoorIsClassified() {
  // PATH A, both documented spellings of the loader prompt. `loader>` is what
  // the EX2200 hardware guide shows; `ok` is Juniper's generic Junos sample.
  assert.equal(classifyBootDoor("Loading /boot/defaults/loader.conf\r\nloader> "), "loader");
  assert.equal(classifyBootDoor("\r\nok "), "loader");

  // PATH B. "Choice:" is the stable part; the item NUMBERING is not, and both
  // documented variants of item 1 must be recognised.
  assert.equal(
    classifyBootDoor(
      "Main Menu\r\n\r\n1. Boot [J]unos volume\r\n" +
        "2. Boot Junos volume in [S]afe mode\r\n3. [R]eboot\r\n" +
        "4. [B]oot menu\r\n5. [M]ore options\r\n\r\nChoice: ",
    ),
    "menu",
  );
  assert.equal(
    classifyBootDoor("1. Boot [D]irectly from Junos volume\r\n\r\nChoice: "),
    "menu",
  );

  // THE MENU MUST WIN WHEN BOTH COULD MATCH. A Path-B boot prints plenty of
  // FreeBSD text, and a buffer that carried both markers must not be answered
  // with "boot -s".
  assert.equal(
    classifyBootDoor("ok\r\nMain Menu\r\n5. [M]ore options\r\n\r\nChoice: "),
    "menu",
  );

  // U-Boot is a DIAGNOSIS, not a destination: the wizard does not drive it.
  // Only at its PROMPT, though — see the regression below.
  assert.equal(classifyBootDoor("U-Boot 1.1.6 (Feb  6 2008 - 11:27:42)\r\n=> "), "uboot");

  // Ordinary boot chatter is not a door.
  assert.equal(classifyBootDoor("Consoles: serial port\r\nBooting [kernel] in 9 seconds..."), null);
  assert.equal(classifyBootDoor(""), null);

  // REGRESSION, defect 1 of 2026-10-25 — THE ONE THAT MADE THE WHOLE
  // PROCEDURE FAIL ON HARDWARE. The U-Boot BANNER used to classify as the
  // U-Boot door, and `catchBootWindow` treats that door as a fatal diagnosis.
  // Every pre-ELS EX opens its boot with exactly these lines, so the run died
  // about half a second after the operator plugged the power back in, naming a
  // prompt the console had never reached. A banner is not a prompt.
  assert.equal(
    classifyBootDoor(
      "U-Boot 1.1.6 (Jul 26 2011 - 03:19:50)\r\n\r\nBoard: EX2200-48P-4G 4.18\r\n" +
        "EPLD:  Version 04.100311 (0x10)\r\nDRAM:  Initializing (512MB)\r\nFlash: 8 MB\r\n",
    ),
    null,
    "the U-Boot banner is ordinary boot chatter, not the U-Boot door",
  );
  // Nor is the loader's own self-introduction, which contains the words
  // "U-Boot loader" without being U-Boot.
  assert.equal(
    classifyBootDoor("Consoles: U-Boot console\r\nFreeBSD/arm U-Boot loader, Revision 1.1\r\n"),
    null,
  );
}

/* ──────────────── which key it is safe to be holding, and when ───────────── */

/**
 * The decision argued for in the module header: SPACE always, CTRL-C only once
 * something proves U-Boot is not the thing listening.
 *
 * CTRL-C is U-Boot's own documented break-in key ("CTRL-C during the USB
 * scan" → "=>"). Holding it down from power-on races U-Boot's autoboot with
 * U-Boot's own key, and winning that race lands at a prompt this wizard does
 * not drive. That is defect 3 of 2026-10-25, and it was firing on the same run
 * as defect 1.
 */
function interruptKeysAreEarnedNotAssumed() {
  var SPACE = " ";
  var CTRL_C = "\x03";

  // Nothing heard yet — the operator may be seconds from plugging the cord
  // back in, and U-Boot would be the first thing to read a waiting CTRL-C.
  assert.deepEqual(interruptKeys(""), [SPACE]);

  // U-Boot is talking, and nothing after it. SPACE ALONE.
  assert.deepEqual(
    interruptKeys("U-Boot 1.1.6 (Jul 26 2011 - 03:19:50)\r\nBoard: EX2200-48P-4G 4.18\r\n"),
    [SPACE],
    "CTRL-C must never go out while U-Boot is the thing listening",
  );
  assert.deepEqual(
    interruptKeys(
      "U-Boot 1.1.6 (Jul 26 2011 - 03:19:50)\r\n" +
        "USB:   scanning bus for devices... 3 USB Device(s) found\r\n" +
        "Consoles: U-Boot console\r\n",
    ),
    [SPACE],
    "the USB scan is exactly the moment CTRL-C would break into U-Boot",
  );

  // The FreeBSD loader has the console: past U-Boot, CTRL-C is harmless and
  // drops into the same loader the space bar would have.
  assert.deepEqual(
    interruptKeys("Consoles: U-Boot console\r\nFreeBSD/arm U-Boot loader, Revision 1.1\r\n"),
    [SPACE, CTRL_C],
  );
  assert.deepEqual(
    interruptKeys("U-Boot 1.1.6\r\nLoading /boot/defaults/loader.conf\r\n"),
    [SPACE, CTRL_C],
  );

  // REGRESSION, defect 2 of 2026-10-25: the countdown line is matched on its
  // SHAPE, not on the spelling of what is in the brackets. The old regex
  // demanded `Booting [kernel]`; the captured EX2200 prints `Booting [/kernel]`
  // with a slash, so a console attached mid-countdown — which the header claims
  // to support — matched nothing at all and stayed on space alone.
  assert.deepEqual(
    interruptKeys("Booting [/kernel] in 1 second...\r\n"),
    [SPACE, CTRL_C],
    "\"Booting [/kernel] in 1 second\" must register as the boot window",
  );
  assert.deepEqual(interruptKeys("Booting [kernel] in 9 seconds...\r\n"), [SPACE, CTRL_C]);

  // Path B (EX2300 / EX3400 / EX4600): FreeBSD chatter, no U-Boot banner
  // anywhere. CTRL-C is armed from its very first line, because it is the only
  // key that opens that generation's boot menu.
  assert.deepEqual(interruptKeys("FreeBSD/arm64 ...\r\nConsoles: serial port\r\n"), [
    SPACE,
    CTRL_C,
  ]);
}

/* ───────────────────────────── the two dialogues ─────────────────────────── */

type Scripted = {
  session: SerialSession;
  sent: string[];
  raw: string[];
  append: (s: string) => void;
};

function scriptedDevice(
  onLine: (line: string, raw: string, dev: Scripted) => string | undefined,
): Scripted {
  var sent: string[] = [];
  var raw: string[] = [];
  var session: SerialSession;
  var dev: Scripted;
  session = createSerialSession({
    write: async (bytes) => {
      var text = new TextDecoder().decode(bytes);
      raw.push(text);
      var line = text.replace(/\r+$/, "");
      // `sent` is the COMMANDS. The interrupt hammer's raw keystrokes (and the
      // bare CRs that nudge a prompt) stay in `raw`, where the test that cares
      // about them looks.
      if (line !== "" && line !== "\x03" && line !== " ") sent.push(line);
      var out = onLine(line, text, dev);
      if (out !== undefined) setTimeout(() => session.append(out), 5);
    },
  });
  dev = {
    session: session!,
    sent,
    raw,
    append: (s: string) => session!.append(s),
  };
  return dev;
}

/**
 * PATH A — the classic loader. The run is aborted the moment `recovery` is sent,
 * because what follows (a full boot, then the zeroize, then the shared factory
 * provisioning) belongs to juniperExSoftReset.ts and is tested there.
 */
async function classicLoaderPath() {
  var ac = new AbortController();
  var stages: string[] = [];
  var dev = scriptedDevice((line) => {
    if (line === "boot -s") {
      return (
        "\r\nboot -s\r\nKernel entry at 0x801000f0 ...\r\n" +
        "Enter full pathname of shell or 'recovery' for root password recovery " +
        "or RETURN for /bin/sh: "
      );
    }
    if (line === "recovery") {
      // Stop here: the next phase is the soft reset's, tested with it.
      setTimeout(() => ac.abort(), 5);
      return "\r\nrecovery\r\n";
    }
    return undefined;
  });

  var run = runJuniperExHardReset({
    session: dev.session,
    log: () => {},
    onStage: (s) => stages.push(s),
    targetUser: "meshadmin",
    targetPassword: "s3cr3t-pass",
    mgmtIp: "192.0.2.21",
    mgmtMask: "255.255.255.0",
    mgmtIface: "vlan 99",
    signal: ac.signal,
  });

  // The operator power-cycles. The line check takes 2.5 s and marks the buffer
  // when the watch starts, so boot output sent earlier would be (correctly)
  // ignored as stale.
  setTimeout(() => {
    dev.append(
      "\r\nConsoles: serial port\r\n" +
        "Loading /boot/defaults/loader.conf\r\n" +
        "Hit [Enter] to boot immediately, or space bar for command prompt.\r\n" +
        "Booting [kernel] in 9 seconds...\r\n",
    );
  }, 3200);
  setTimeout(() => dev.append("\r\nloader> "), 4200);

  await assert.rejects(run, /cancelled|aborted/);

  assert.deepEqual(
    stages.slice(0, 4),
    ["line-check", "await-power-cycle", "loader", "single-user"],
    `stage order: ${stages.join(" → ")}`,
  );

  // BOTH interrupt keys were held down through the window. This fixture has no
  // U-Boot banner in it and announces the loader ("Loading /boot/…"), so
  // CTRL-C is armed — the models cannot be told apart beyond that, and on the
  // EX2300 / EX3400 generation CTRL-C is the only key that works. WHEN it is
  // armed is pinned by interruptKeysAreEarnedNotAssumed() and by the captured
  // EX2200 replay; what is pinned here is that it is still sent at all.
  assert.ok(
    dev.raw.includes("\x03"),
    "CTRL-C must be sent once the loader has the console (the only key on EX2300 / EX3400)",
  );
  assert.ok(
    dev.raw.includes(" "),
    "the space bar must be sent (it is the key the classic loader reads)",
  );
  // And RAW: a CR behind either would be "boot immediately", the exact
  // outcome being avoided.
  assert.ok(
    !dev.raw.some((r) => r === "\x03\r" || r === " \r"),
    "the interrupt keys must be raw keystrokes, never lines",
  );

  assert.deepEqual(
    dev.sent,
    ["boot -s", "recovery"],
    `the classic path is boot -s then recovery, and nothing else: ${dev.sent.join(" | ")}`,
  );
  // A bare RETURN would give /bin/sh with no CLI and no commit.
  assert.ok(dev.sent.includes("recovery"));
}

/**
 * PATH B — the upgraded-FreeBSD menu. The assertion that matters most is the
 * NEGATIVE one: "boot -s" must never be sent here.
 */
async function upgradedFreebsdMenuPath() {
  var ac = new AbortController();
  var stages: string[] = [];
  var dev = scriptedDevice((line) => {
    if (line === "m") {
      return (
        "\r\nOptions Menu\r\n\r\n1. Recover [J]unos volume\r\n" +
        "2. Recovery mode - [C]LI\r\n3. Check [F]ile system\r\n" +
        "4. Enable [V]erbose boot\r\n5. [B]oot prompt\r\n6. [M]ain menu\r\n\r\nChoice: "
      );
    }
    if (line === "c") {
      setTimeout(() => ac.abort(), 5);
      return "\r\nStarting CLI ...\r\n";
    }
    return undefined;
  });

  var run = runJuniperExHardReset({
    session: dev.session,
    log: () => {},
    onStage: (s) => stages.push(s),
    targetUser: "meshadmin",
    targetPassword: "s3cr3t-pass",
    mgmtIp: "192.0.2.21",
    mgmtMask: "255.255.255.0",
    mgmtIface: "vme",
    signal: ac.signal,
  });

  setTimeout(() => {
    dev.append(
      "\r\nFreeBSD/arm ...\r\n" +
        "Main Menu\r\n\r\n1. Boot [J]unos volume\r\n" +
        "2. Boot Junos volume in [S]afe mode\r\n3. [R]eboot\r\n" +
        "4. [B]oot menu\r\n5. [M]ore options\r\n\r\nChoice: ",
    );
  }, 3200);

  await assert.rejects(run, /cancelled|aborted/);

  assert.deepEqual(stages.slice(0, 4), [
    "line-check",
    "await-power-cycle",
    "loader",
    "single-user",
  ]);

  // THE BRACKETED LETTERS, never the item numbers: Juniper's pages show at
  // least two numberings and the mnemonics are what stays put.
  assert.deepEqual(
    dev.sent,
    ["m", "c"],
    `the menu path answers [M]ore options then [C]LI: ${dev.sent.join(" | ")}`,
  );
  assert.ok(
    !dev.sent.includes("boot -s"),
    "\"boot -s\" on this generation gives a shell that cannot recover the password — " +
      "Juniper says so explicitly",
  );
  assert.ok(!dev.sent.includes("5") && !dev.sent.includes("2"), "never answer by number");
}

/** U-Boot is reported as a diagnosis an operator can act on, not as a timeout. */
async function ubootIsNamedRatherThanWaitedOut() {
  var ac = new AbortController();
  var dev = scriptedDevice(() => undefined);
  var run = runJuniperExHardReset({
    session: dev.session,
    log: () => {},
    targetUser: "meshadmin",
    targetPassword: "s3cr3t-pass",
    mgmtIp: "192.0.2.21",
    mgmtMask: "255.255.255.0",
    mgmtIface: "vlan 99",
    signal: ac.signal,
  });
  setTimeout(() => {
    dev.append("\r\nU-Boot 1.1.6 (Feb  6 2008 - 11:27:42)\r\n=> ");
  }, 3200);
  await assert.rejects(run, /U-BOOT prompt/);
}

/** An unusable management interface must fail BEFORE the power cycle, let alone
 * before the irreversible wipe. */
async function badMgmtInterfaceFailsBeforeAnythingIrreversible() {
  var dev = scriptedDevice(() => undefined);
  await assert.rejects(
    () =>
      runJuniperExHardReset({
        session: dev.session,
        log: () => {},
        targetUser: "meshadmin",
        targetPassword: "s3cr3t-pass",
        mgmtIp: "192.0.2.21",
        mgmtMask: "255.255.255.0",
        mgmtIface: "Craft",
      }),
    /not usable on a Junos EX/,
  );
  assert.deepEqual(dev.sent, [], "nothing may be typed at the switch before this check");
}

/* ───────── the captured EX2200-48P-4G boot, replayed at console speed ──────── */

/**
 * THE REAL TRACE, 2026-10-25, EX2200-48P-4G 4.18 / U-Boot 1.1.6, split at the
 * two moments that matter: where U-Boot hands over, and where the window opens.
 */
const EX2200_UBOOT =
  "\r\nU-Boot 1.1.6 (Jul 26 2011 - 03:19:50)\r\n\r\n" +
  "Board: EX2200-48P-4G 4.18\r\n" +
  "EPLD:  Version 04.100311 (0x10)\r\n" +
  "DRAM:  Initializing (512MB)\r\n" +
  "Flash: 8 MB\r\n\r\n" +
  "Firmware Version:01.00.00\r\n" +
  "USB:   scanning bus for devices... 3 USB Device(s) found\r\n" +
  "       scanning bus for storage devices... 1 Storage Device(s) found\r\n\r\n" +
  "ELF file is 32 bit\r\n" +
  "Consoles: U-Boot console\r\n";

const EX2200_LOADER =
  "\r\nFreeBSD/arm U-Boot loader, Revision 1.1\r\n" +
  "(builder@dagmath.juniper.net, Tue Jul 26 01:25:53 UTC 2011)\r\n" +
  "Memory: 512MB\r\n" +
  "bootsequencing is enabled\r\n" +
  "bootsuccess is set\r\n" +
  "new boot device = disk0s1:\r\n" +
  "Loading /boot/defaults/loader.conf\r\n" +
  "/kernel data=0x9a9df0+0xfe3bc syms=[0x4+0x106310+0x4+0xe15d2]\r\n\r\n";

const EX2200_WINDOW =
  "Hit [Enter] to boot immediately, or space bar for command prompt.\r\n" +
  "Booting [/kernel] in 1 second...\r\n";

const EX2200_LOADER_PROMPT =
  "\r\nType '?' for a list of commands, 'help' for more detailed help.\r\n\r\nloader> ";

/** The countdown the trace itself declares: "in 1 second...". */
const WINDOW_MS = 1000;
/** 9600 8N1 ≈ 960 characters a second — the speed this console actually runs. */
const CONSOLE_CHARS_PER_TICK = 24;
const CONSOLE_TICK_MS = 25;

type Phase = "dark" | "uboot" | "loader-boot" | "countdown" | "booted" | "loader" | "uboot-prompt";
type Keystroke = { phase: Phase; text: string; at: number };

/**
 * An EX2200 that BEHAVES like one, rather than a transcript that is handed over
 * regardless. This is the whole point of the fixture: the switch is only
 * reachable if the right key arrives at the right moment, so a procedure that
 * is too slow, sends Enter, or sends CTRL-C too early does not get a `loader>`
 * out of it — it gets a switch that booted, or a U-Boot prompt.
 *
 *   dark        — powered off / nothing yet. Keystrokes vanish.
 *   uboot       — U-Boot is reading the console. A CTRL-C HERE BREAKS IN and
 *                 lands at "=>", which is the dead end defect 3 produced.
 *   loader-boot — the FreeBSD loader is printing; nothing is read yet.
 *   countdown   — the one-second window. A space (or any non-CR key) opens
 *                 `loader>`. A CR is "boot immediately" and loses it. Silence
 *                 for WINDOW_MS boots the kernel, and the run is over.
 *   loader      — `boot -s` / `recovery` answered as Juniper documents.
 */
function ex2200Device() {
  var phase: Phase = "dark";
  var keys: Keystroke[] = [];
  var sent: string[] = [];
  var windowOpenedAt = 0;
  var windowTimer: ReturnType<typeof setTimeout> | null = null;
  var session: SerialSession;
  var onRecovery = () => {};

  var emit = (s: string) => session.append(s);

  var openWindow = () => {
    phase = "countdown";
    windowOpenedAt = Date.now();
    windowTimer = setTimeout(() => {
      if (phase !== "countdown") return;
      // Nobody pressed anything in time: the switch boots, and there is no
      // second chance inside this run.
      phase = "booted";
      emit("\r\nKernel entry at 0x801000f0 ...\r\n");
    }, WINDOW_MS);
  };

  session = createSerialSession({
    write: async (bytes) => {
      var text = new TextDecoder().decode(bytes);
      keys.push({ phase, text, at: Date.now() });
      if (phase === "uboot" && text === "\x03") {
        // U-Boot's own documented break-in key. The wizard must never get here.
        phase = "uboot-prompt";
        emit("\r\n=> ");
        return;
      }
      if (phase === "countdown") {
        if (text.includes("\r")) {
          // "Hit [Enter] to boot immediately" — exactly what it says.
          phase = "booted";
          if (windowTimer) clearTimeout(windowTimer);
          emit("\r\nKernel entry at 0x801000f0 ...\r\n");
          return;
        }
        phase = "loader";
        if (windowTimer) clearTimeout(windowTimer);
        emit(EX2200_LOADER_PROMPT);
        return;
      }
      if (phase === "loader") {
        var line = text.replace(/\r+$/, "");
        if (line === "boot -s") {
          sent.push(line);
          emit(
            "\r\nboot -s\r\nKernel entry at 0x801000f0 ...\r\n" +
              "Enter full pathname of shell or 'recovery' for root password recovery " +
              "or RETURN for /bin/sh: ",
          );
          return;
        }
        if (line === "recovery") {
          sent.push(line);
          emit("\r\nrecovery\r\n");
          onRecovery();
          return;
        }
      }
    },
  });

  /** Stream a block at 9600 baud, the way the console really delivers it. */
  var stream = (text: string) =>
    new Promise<void>((resolve) => {
      var i = 0;
      var tick = () => {
        if (i >= text.length) {
          resolve();
          return;
        }
        emit(text.slice(i, i + CONSOLE_CHARS_PER_TICK));
        i += CONSOLE_CHARS_PER_TICK;
        setTimeout(tick, CONSOLE_TICK_MS);
      };
      tick();
    });

  return {
    session,
    keys,
    sent,
    get phase() {
      return phase;
    },
    get windowOpenedAt() {
      return windowOpenedAt;
    },
    set onRecovery(fn: () => void) {
      onRecovery = fn;
    },
    /** The operator's one gesture: the power cycle. */
    async powerCycle() {
      phase = "uboot";
      await stream(EX2200_UBOOT);
      phase = "loader-boot";
      await stream(EX2200_LOADER);
      await stream(EX2200_WINDOW);
      openWindow();
    },
  };
}

/**
 * THE REGRESSION TEST FOR THE FIELD FAILURE. Replays the captured boot at
 * console speed into a switch that only opens its loader for the right key at
 * the right moment, and pins the three facts the hardware run turned on.
 */
async function realEx2200BootIsCaught() {
  var ac = new AbortController();
  var stages: string[] = [];
  var dev = ex2200Device();
  dev.onRecovery = () => setTimeout(() => ac.abort(), 5);

  var run = runJuniperExHardReset({
    session: dev.session,
    log: () => {},
    onStage: (s) => stages.push(s),
    targetUser: "meshadmin",
    targetPassword: "s3cr3t-pass",
    mgmtIp: "192.0.2.21",
    mgmtMask: "255.255.255.0",
    mgmtIface: "vlan 99",
    signal: ac.signal,
  });

  // The line check takes 2.5 s and marks the buffer when the watch opens, so
  // the power cycle has to land after it — as it does in the field.
  setTimeout(() => void dev.powerCycle(), 3000);

  await assert.rejects(run, /cancelled|aborted/);

  // 1 — IT GOT IN. Not a timeout, not the U-Boot dead end: the loader answered
  // and the documented Path-A dialogue ran.
  assert.equal(dev.phase, "loader", `the switch must end at its loader, not ${dev.phase}`);
  assert.deepEqual(
    stages.slice(0, 4),
    ["line-check", "await-power-cycle", "loader", "single-user"],
    `stage order: ${stages.join(" → ")}`,
  );
  assert.deepEqual(dev.sent, ["boot -s", "recovery"], dev.sent.join(" | "));

  // 2 — NOTHING WENT INTO U-BOOT. Defect 3: CTRL-C is U-Boot's break-in key,
  // and the old code held it down from the moment the watch opened.
  var early = dev.keys.filter((k) => k.phase === "dark" || k.phase === "uboot");
  assert.ok(
    !early.some((k) => k.text.includes("\x03")),
    "CTRL-C reached the console while U-Boot was reading it — that is the \"=>\" dead end",
  );
  assert.ok(
    early.every((k) => k.text === " " || k.text === "\r" || k.text === ""),
    `only the space bar (and the line-check poke) may go out before the loader: ${JSON.stringify(
      early.map((k) => k.text),
    )}`,
  );

  // 3 — THE SPACE LANDED IN THE WINDOW, AND EARLY IN IT. This is the timing
  // claim, as a number: the hammer runs on a fixed 120 ms tick, so the first
  // key after the window opens cannot be more than one tick late. The window
  // itself is the one second the trace declares.
  var inWindow = dev.keys.filter((k) => k.phase === "countdown");
  assert.ok(inWindow.length > 0, "no key at all arrived during the boot window");
  var firstSpace = inWindow.find((k) => k.text === " ");
  assert.ok(firstSpace, "the key that arrived in the window was not the space bar");
  var latency = firstSpace!.at - dev.windowOpenedAt;
  assert.ok(
    latency >= 0 && latency < 300,
    `the space bar must land within 300 ms of the window opening, took ${latency} ms ` +
      `(window is ${WINDOW_MS} ms)`,
  );

  // 4 — AND IT WAS NEVER AN ENTER. "Hit [Enter] to boot immediately" means a
  // CR in this window does the exact opposite of what the file is for.
  assert.ok(
    !inWindow.some((k) => k.text.includes("\r")),
    "a CR during the boot window is \"boot immediately\" — it must never be sent",
  );
}

async function main() {
  bootDoorIsClassified();
  interruptKeysAreEarnedNotAssumed();
  await badMgmtInterfaceFailsBeforeAnythingIrreversible();
  await classicLoaderPath();
  await upgradedFreebsdMenuPath();
  await ubootIsNamedRatherThanWaitedOut();
  await realEx2200BootIsCaught();
  console.log("juniperExHardReset.test.ts: ok");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});

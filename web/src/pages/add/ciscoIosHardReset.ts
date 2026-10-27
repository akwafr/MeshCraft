/**
 * Cisco Catalyst 2960-X ASSISTED HARDWARE RESET over Web Serial.
 *
 * The soft reset (ciscoIosSoftReset.ts) needs a working login; this flow
 * exists for the switch nobody can log into — unknown enable password, dead
 * local accounts — where the only door is the boot loader. It is the
 * vendor's password-recovery procedure with the wizard holding the operator's
 * hand at the two moments that need one, and the console at every other:
 *
 *  1. LINE CHECK — the console is poked to prove the wiring and speed
 *     (2960-X console runs 9600 8N1; the wizard's autodetect has usually
 *     already established this). Silence is tolerated: a switch that is off
 *     says nothing, and that is exactly the expected state before step 2;
 *  2. OPERATOR: POWER-CYCLE WITH MODE HELD — the 2960-X gives no console
 *     break into its boot loader; the MODE button during power-on is the one
 *     documented door. The wizard says exactly what to do and then WATCHES;
 *  3. CATCH THE BOOT-LOADER WINDOW — the `switch:` prompt is the target.
 *     The wizard tells the operator to release MODE the moment it appears
 *     (the LED sequence is easy to misread; the prompt is not);
 *  4. WIPE — `flash_init`, then delete the three files that hold identity:
 *     config.text, vlan.dat, private-config.text. Absences are tolerated
 *     (a half-reset switch may lack any of them); the deletion prompt
 *     "Are you sure you want to delete ... (y/n)?" is answered `y`;
 *  5. `boot` — and from the first boot banner onward this is EXACTLY the
 *     soft reset's world: provisionFactoryCatalyst owns the setup dialog,
 *     the virgin login, the management push and the serial-side verify.
 *
 * # Status of what is written here
 *
 * DOCUMENTED, not captured — the same status the soft reset carries, from
 * the same source (the vendor's password-recovery procedure for the 2960-X).
 * The prompts asserted below are the documented ones; the first live run on
 * the lab unit is the capture, and the expect timeouts are generous so an
 * operator can read what the switch actually printed if a prompt diverges.
 */

import type { SerialSession } from "./serialExpect.ts";
import {
  parseVlanId,
  provisionFactoryCatalyst,
  resolveDomainName,
  type SoftResetLog,
} from "./ciscoIosSoftReset.ts";

/** The staged moments the wizard renders to the operator. */
export type HardResetStage =
  | "line-check"
  | "await-power-cycle"
  /** First boot-loader chatter: power is back and MODE is being held right. */
  | "power-restored"
  | "bootloader"
  | "wiping"
  | "booting"
  | "provisioning";

export type CiscoIosHardResetInput = {
  session: SerialSession;
  log: SoftResetLog;
  /** Stage transitions, for the wizard's operator instructions. */
  onStage?: (stage: HardResetStage) => void;
  targetUser: string;
  targetPassword: string;
  mgmtIp: string;
  mgmtMask: string;
  gateway?: string;
  mgmtIface?: string;
  domainName?: string;
  hostname?: string;
  signal?: AbortSignal;
};

/** The boot-loader prompt — the whole procedure aims at this line. */
const BOOTLOADER = /switch:\s*$/m;
/** Boot-loader chatter that proves MODE was held correctly. */
const BOOTLOADER_BANNER =
  /(boot loader|BOOTLDR|base ethernet mac|xmodem|password-recovery mechanism)/i;
/** The deletion confirmation: "Are you sure you want to delete ... (y/n)?" */
const DELETE_CONFIRM = /\(y\/n\)\?/i;
/** File absent — an ordinary state on a half-reset switch. */
const NOT_FOUND = /(no such file|not found|unable to (stat|open))/i;

function aborted(signal?: AbortSignal) {
  if (signal?.aborted) throw new Error("cancelled");
}

/**
 * One boot-loader deletion, tolerant of absence. The `y` is sent RAW: the
 * loader reads a single keystroke, and a CR behind it would answer the NEXT
 * question too.
 */
async function delFile(
  session: SerialSession,
  file: string,
  log: SoftResetLog,
  signal?: AbortSignal,
): Promise<void> {
  aborted(signal);
  session.mark();
  await session.send("del flash:" + file);
  var first = await session.expectAny([DELETE_CONFIRM, NOT_FOUND, BOOTLOADER], 15000);
  if (first.index === 0) {
    await session.sendRaw("y");
    await session.expect(BOOTLOADER, 20000);
    log(`deleted flash:${file}`);
    return;
  }
  if (first.index === 1) {
    log(`flash:${file} not present — nothing to delete`);
    try {
      await session.expect(BOOTLOADER, 10000);
    } catch {
      /* the prompt may already be the match we consumed */
    }
    return;
  }
  log(`flash:${file}: the loader answered with its prompt — treated as absent`);
}

export async function runCiscoIosHardReset(input: CiscoIosHardResetInput): Promise<void> {
  var session = input.session;
  var log = input.log;
  var stage = input.onStage ?? (() => {});
  var iface = (input.mgmtIface || "vlan 1").trim() || "vlan 1";
  var domainName = resolveDomainName(input.domainName);

  if (!input.mgmtIp.trim() || !input.mgmtMask.trim()) {
    throw new Error("management IP and mask are required");
  }
  if (!input.targetPassword) {
    throw new Error("instance credential secret is required");
  }
  // Same guard as the soft reset, same reason: on a Catalyst the address goes
  // on an SVI, and a free-text interface that is not "vlan N" would be
  // configured silently wrong.
  if (parseVlanId(iface) === null) {
    throw new Error(
      `management interface "${iface}" is not a VLAN: this must read "vlan N"`,
    );
  }

  // 0 — ENGINE STAMP, first line of every run: the one journal line that
  // settles "is my browser running the fixed code?" without DevTools. Bump
  // it whenever this file's behaviour changes.
  log("assisted hard-reset engine v3");

  // 1 — LINE CHECK. A poke, and an honest report either way: text proves the
  // wiring and the speed; silence is what a powered-off switch says, and the
  // next stage begins with a power cycle anyway.
  //
  // NOTHING here may block. Two field reports of "stuck at test the line"
  // taught it twice: first the poke's write AWAITED a busy Web Serial writer,
  // then the timeout guarding it went through the pausable session — whose
  // gate is one more thing that can wait. The poke is now FIRE-AND-FORGET
  // (its outcome only decides a log sentence) and the pacing is a raw
  // setTimeout that answers to nobody.
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
  } else if (poke.state === "sent") {
    log(
      "console line silent — normal for a switch that is powered off; " +
        "the wiring is proven the moment boot output appears",
    );
  } else {
    log(
      "line poke did not go through (port busy?) — continuing anyway: the " +
        "next stages only LISTEN until the boot loader answers",
    );
  }

  // 2 — THE OPERATOR'S MOMENT. The wizard can only watch: the MODE button
  // is a physical door. The stage drives the on-screen instruction; the log
  // repeats it for the record.
  stage("await-power-cycle");
  log(
    "ACTION REQUIRED — unplug the switch's power, PRESS AND HOLD the MODE " +
      "button (front-left), plug power back in while holding, keep holding " +
      "about 15 seconds (the SYST LED blinks), then RELEASE. Many 2960-X " +
      "print NOTHING while the button is held — the wizard watches either way",
  );
  session.mark();
  log("watching the console for the boot loader (up to 5 minutes)…");

  // 3 — CATCH THE WINDOW. Up to five minutes: finding a power plug behind a
  // rack takes as long as it takes. Any boot-loader chatter confirms the
  // hold worked; the `switch:` prompt is the actual door.
  var sawBanner = false;
  var deadline = Date.now() + 300000;
  for (;;) {
    aborted(input.signal);
    if (Date.now() > deadline) {
      throw new Error(
        "boot loader never appeared — if the switch booted normally, MODE was " +
          "released too early (or not held): power-cycle and try again",
      );
    }
    try {
      var hit = await session.expectAny([BOOTLOADER, BOOTLOADER_BANNER], 5000);
      if (hit.index === 1 && !sawBanner) {
        sawBanner = true;
        stage("power-restored");
        log("boot-loader output detected — power is back, keep holding MODE…");
        continue;
      }
      if (hit.index === 0) {
        break;
      }
    } catch {
      /* quiet console while the operator finds the plug */
    }
  }
  stage("bootloader");
  log("boot loader reached (\"switch:\") — RELEASE the MODE button now");

  // 4 — WIPE. flash_init first (the loader cannot touch files before it),
  // then the three identity files. 60 s for flash_init: it walks the whole
  // filesystem on first use.
  stage("wiping");
  session.mark();
  await session.send("flash_init");
  await session.expect(BOOTLOADER, 60000);
  log("flash_init done");
  await delFile(session, "config.text", log, input.signal);
  await delFile(session, "vlan.dat", log, input.signal);
  await delFile(session, "private-config.text", log, input.signal);

  // 5 — BOOT, then the shared factory provisioning. From here the switch is
  // indistinguishable from one the soft reset just reloaded.
  stage("booting");
  session.mark();
  await session.send("boot");
  log("booting IOS — the switch takes ~2 minutes to come back");

  stage("provisioning");
  await provisionFactoryCatalyst({
    session,
    log,
    iface,
    domainName,
    targetUser: (input.targetUser || "admin").trim() || "admin",
    targetPassword: input.targetPassword,
    mgmtIp: input.mgmtIp.trim(),
    mgmtMask: input.mgmtMask.trim(),
    gateway: input.gateway,
    hostname: input.hostname,
    signal: input.signal,
  });
}

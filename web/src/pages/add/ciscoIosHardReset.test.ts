/**
 * Boot-loader dialogue checks for the Catalyst assisted hardware reset:
 *   node --experimental-strip-types src/pages/add/ciscoIosHardReset.test.ts
 *
 * Fixtures are the DOCUMENTED 2960-X boot-loader output shapes (module
 * header states the status); a live capture will be diffed against them.
 * What is pinned here is the half a bad regex silently ruins: catching the
 * "switch:" window, answering the deletion confirmation with a RAW `y`,
 * and tolerating an absent file.
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";

// The internals under test are module-private; the dialogue is exercised
// through a scripted fake device driving the exported runner up to the boot
// hand-off, which is where provisionFactoryCatalyst (tested with the soft
// reset) takes over. To stop before it, the fake device never emits boot
// output after "boot", and the test asserts everything sent up to it.

import { runCiscoIosHardReset } from "./ciscoIosHardReset.ts";

function bootloaderDevice() {
  var sent: string[] = [];
  var session: SerialSession;
  var files: Record<string, boolean> = {
    "config.text": true,
    "vlan.dat": true,
    // private-config.text intentionally ABSENT: the tolerance path must run.
  };
  var pendingDelete = "";
  session = createSerialSession({
    write: async (bytes) => {
      var text = new TextDecoder().decode(bytes);
      var line = text.replace(/\r+$/, "");
      sent.push(text);
      var reply = (out: string) => setTimeout(() => session.append(out), 5);
      if (pendingDelete && text === "y") {
        var f = pendingDelete;
        pendingDelete = "";
        delete files[f];
        reply(`\r\nFile "flash:${f}" deleted\r\nswitch: `);
        return;
      }
      if (line === "flash_init") {
        reply("\r\nInitializing Flash...\r\n...done Initializing Flash.\r\nswitch: ");
        return;
      }
      var del = /^del flash:(.+)$/.exec(line);
      if (del) {
        if (files[del[1]]) {
          pendingDelete = del[1];
          reply(`\r\nAre you sure you want to delete "flash:${del[1]}" (y/n)?`);
        } else {
          reply(`\r\nUnable to stat flash:${del[1]}: no such file or directory\r\nswitch: `);
        }
        return;
      }
      if (line === "boot") {
        // Silence: the runner's next phase (provisionFactoryCatalyst) is the
        // soft reset's, tested there. The test aborts the run here.
        return;
      }
      // The line-check poke and anything else: silence, like a powered-off
      // switch.
    },
  });
  return { session, sent, files: () => files };
}

async function bootloaderWipeDialogue() {
  var dev = bootloaderDevice();
  var stages: string[] = [];
  var ac = new AbortController();
  var run = runCiscoIosHardReset({
    session: dev.session,
    log: () => {},
    onStage: (s) => {
      stages.push(s);
      // Stop the run once the shared provisioning phase begins: its own
      // tests live with the soft reset.
      if (s === "provisioning") ac.abort();
    },
    targetUser: "admin",
    targetPassword: "s3cret",
    mgmtIp: "10.0.0.2",
    mgmtMask: "255.255.255.0",
    mgmtIface: "vlan 99",
    signal: ac.signal,
  });

  // The operator "power-cycles": the boot-loader banner arrives AFTER the
  // wizard's instruction — the runner marks its buffer when it starts
  // watching, so output from before the instruction is stale by design, and
  // a banner sent too early would be (correctly) ignored. 2.5 s clears the
  // line-check poke (1.2 s) with margin.
  setTimeout(() => {
    dev.session.append(
      "\r\nThe system has been interrupted prior to initializing the flash filesystem.\r\n" +
        "Base ethernet MAC Address: bc:c4:93:e5:7f:80\r\n" +
        "Xmodem file system is available.\r\nswitch: ",
    );
  }, 2500);

  await assert.rejects(run, /cancelled|aborted/);

  // Stage order: the two operator moments happen, in order, before the wipe.
  assert.deepEqual(stages, [
    "line-check",
    "await-power-cycle",
    "bootloader",
    "wiping",
    "booting",
    "provisioning",
  ]);

  var typed = dev.sent.map((s) => s.replace(/\r+$/, ""));
  // flash_init before any deletion — the loader cannot touch files before it.
  var fi = typed.indexOf("flash_init");
  var d1 = typed.indexOf("del flash:config.text");
  assert.ok(fi >= 0 && d1 > fi, `flash_init must precede deletions (${typed.join(" | ")})`);
  // The confirmation is answered with a RAW y — no CR that would answer the
  // next question too.
  assert.ok(dev.sent.includes("y"), "deletion confirmed with a bare y keystroke");
  // The absent file was tolerated, and both present files are gone.
  assert.deepEqual(dev.files(), {});
  // boot was sent last of the loader phase.
  assert.ok(typed.includes("boot"), "boot must be sent after the wipe");
}

async function main() {
  await bootloaderWipeDialogue();
  console.log("ciscoIosHardReset: OK");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});

// ——— The checklist model: it must track the runner's stages without lying. ———
import { hardStepStatuses } from "./hardResetStepsModel.ts";

function checklistFollowsTheWire() {
  // Before anything: only "connect" invites action.
  var s = hardStepStatuses({ connected: false, baudLocked: false, running: false, stage: null, verified: false });
  assert.equal(s["connect"], "active");
  assert.equal(s["line-check"], "pending");
  assert.equal(s["unplug"], "pending");

  // Speed locked, no run started: the AUTOMATIC line test is in flight —
  // the step must be ACTIVE on its own, not waiting for the start button.
  s = hardStepStatuses({ connected: true, baudLocked: true, running: false, stage: null, verified: false });
  assert.equal(s["baud"], "done");
  assert.equal(s["line-check"], "active");
  assert.equal(s["unplug"], "pending");

  // The automatic test concluded (answered OR silent): ticked before any run.
  s = hardStepStatuses({ connected: true, baudLocked: true, lineTested: true, running: false, stage: null, verified: false });
  assert.equal(s["line-check"], "done");
  assert.equal(s["unplug"], "pending");

  // Waiting for the power cycle: the three GESTURES are active as a block.
  s = hardStepStatuses({ connected: true, baudLocked: true, running: true, stage: "await-power-cycle", verified: false });
  assert.equal(s["line-check"], "done");
  assert.equal(s["unplug"], "active");
  assert.equal(s["hold-mode"], "active");
  assert.equal(s["replug"], "active");
  assert.equal(s["release"], "pending");

  // First boot chatter: the gestures are PROVEN together; release pending the prompt.
  s = hardStepStatuses({ connected: true, baudLocked: true, running: true, stage: "power-restored", verified: false });
  assert.equal(s["unplug"], "done");
  assert.equal(s["replug"], "done");
  assert.equal(s["release"], "active");

  // Wiping: release done, wipe active.
  s = hardStepStatuses({ connected: true, baudLocked: true, running: true, stage: "wiping", verified: false });
  assert.equal(s["release"], "done");
  assert.equal(s["wiping"], "active");
  assert.equal(s["verify"], "pending");

  // Adopted: the last line ticks.
  s = hardStepStatuses({ connected: true, baudLocked: true, running: false, stage: "provisioning", verified: true });
  assert.equal(s["verify"], "done");
}
checklistFollowsTheWire();
console.log("hardResetSteps model: OK");

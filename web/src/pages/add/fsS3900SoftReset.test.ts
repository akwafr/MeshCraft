/**
 * Save-flow checks for the FS S3900 soft reset (lab OpCode 1.7.0):
 *   node --experimental-strip-types src/pages/add/fsS3900SoftReset.test.ts
 *
 * "copy running-config startup-config" prompts for a startup FILE NAME —
 * not y/n. The script must answer it, then wait for the flash Success line.
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";
import {
  collectShowOutput,
  runConfigCmds,
  saveStartupConfig,
  STARTUP_CONFIG_FILE,
} from "./fsS3900SoftReset.ts";

function fakeDevice(replies: (line: string) => string | undefined): SerialSession {
  var session: SerialSession;
  session = createSerialSession({
    write: async (bytes) => {
      var line = new TextDecoder().decode(bytes).replace(/\r+$/, "");
      var out = replies(line);
      if (out !== undefined) setTimeout(() => session.append(out), 5);
    },
  });
  return session;
}

async function labFilenamePrompt() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "copy running-config startup-config") {
      return "\r\nStartup configuration file name: ";
    }
    if (line === STARTUP_CONFIG_FILE) {
      return (
        "\r\nFlash programming started.\r\n" +
        "Flash programming completed.\r\nSuccess.\r\ntest-fs-s3900#"
      );
    }
    return undefined;
  });
  await saveStartupConfig(session, (m) => logs.push(m));
  assert.ok(
    logs.some((l) => l.includes(STARTUP_CONFIG_FILE)),
    "must answer the filename prompt",
  );
  assert.ok(
    logs.some((l) => l.includes("flash write ok")),
    "must see the flash Success line",
  );
}

async function confirmYesBuild() {
  var session = fakeDevice((line) => {
    if (line === "copy running-config startup-config") {
      return "\r\nContinue <y/n>? ";
    }
    if (line === "y") {
      return "\r\n[OK]\r\nSwitch#";
    }
    return undefined;
  });
  await saveStartupConfig(session, () => {});
}

async function cliErrorRejects() {
  var session = fakeDevice((line) => {
    if (line.startsWith("copy")) {
      return "\r\n% Invalid input detected at '^' marker.\r\nSwitch#";
    }
    return undefined;
  });
  await assert.rejects(
    () => saveStartupConfig(session, () => {}),
    /save failed/,
    "a CLI error must reject, not hang",
  );
}

async function refusedFilenameRejects() {
  var session = fakeDevice((line) => {
    if (line === "copy running-config startup-config" || line === STARTUP_CONFIG_FILE) {
      // Device keeps re-prompting: the name was refused.
      return "\r\nStartup configuration file name: ";
    }
    return undefined;
  });
  await assert.rejects(
    () => saveStartupConfig(session, () => {}),
    /not accepted/,
    "a re-prompt after our answer must reject, not loop",
  );
}

async function pagerIsFed() {
  var page2Sent = false;
  var session = fakeDevice((line) => {
    if (line === "show interfaces brief") {
      return "\r\nEth 1/ 1  1000BASE-T   Up      Down          Auto  None\r\n--More--";
    }
    if (line === " " && !page2Sent) {
      page2Sent = true;
      return "\r\nEth 1/28  10GBASE SFP+ Up      Up            Disable  None\r\nSwitch#";
    }
    return undefined;
  });
  var out = await collectShowOutput(session, "show interfaces brief", 8000);
  assert.ok(page2Sent, "must feed the pager with a space");
  assert.ok(out.includes("Eth 1/28"), "must collect the page behind --More--");
}

/** Mid-push logout (2026-08-01 deploy): the password change drops the console;
 * the runner must re-login and retry the SAME command. */
async function midPushLogoutRecovers() {
  var executed: string[] = [];
  var kicked = false;
  var session = fakeDevice((line) => {
    if (line === "") return "\r\nUsername: ";
    if (line === "admin" ) return "\r\nPassword: ";
    if (line === "SECRET9") return "\r\nSwitch# ";
    if (line === "conf t") {
      executed.push(line);
      return "\r\nSwitch(config)# ";
    }
    if (line === "username admin password 0 SECRET9" && !kicked) {
      kicked = true;
      return "\r\n% CLI exit session\r\n\r\nUser Access Verification\r\n\r\nUsername: ";
    }
    if (line === "end") return "\r\nSwitch# ";
    executed.push(line);
    return "\r\nSwitch(config)# ";
  });
  var logs: string[] = [];
  await runConfigCmds(
    session,
    (m) => logs.push(m),
    [{ user: "admin", pass: "SECRET9", enable: "SECRET9", label: "instance admin" }],
    [
      { cmd: "ip http server" },
      { cmd: "username admin password 0 SECRET9", label: "username admin password 0 •••" },
      { cmd: "enable password 0 SECRET9", label: "enable password 0 •••" },
    ],
  );
  assert.ok(kicked, "scenario must have dropped the session once");
  assert.ok(
    logs.some((l) => l.includes("fell back to login")),
    "runner must report the logout",
  );
  var pwdRuns = executed.filter((c) => c === "username admin password 0 SECRET9");
  assert.equal(pwdRuns.length, 1, "the kicked command must be retried once after re-login");
  assert.ok(
    executed.includes("enable password 0 SECRET9"),
    "commands after the logout must still run",
  );
}

await labFilenamePrompt();
await confirmYesBuild();
await cliErrorRejects();
await refusedFilenameRejects();
await pagerIsFed();
await midPushLogoutRecovers();
console.log("fsS3900SoftReset.test.ts: ok");

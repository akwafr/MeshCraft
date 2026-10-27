/**
 * FS S3400 soft-reset checks against a fake BDCOM-style device:
 *   node --experimental-strip-types src/pages/add/fsS3400SoftReset.test.ts
 *
 * The load-bearing behavior: after "delete startup-config", a reboot prompt
 * that offers to SAVE the running config must be answered NO — a yes writes
 * the just-deleted startup-config right back and the "factory reset" becomes
 * a plain reboot.
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";
import {
  collectShowOutput,
  compactPortRange,
  confirmAnswer,
  parseUpPorts,
  rebootAnswering,
  runConfigCmds,
  saveWrite,
  toDottedMask,
} from "./fsS3400SoftReset.ts";

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

function confirmAnswers() {
  assert.equal(confirmAnswer("this file will be erased, are you sure? (y/n)"), "y");
  assert.equal(confirmAnswer("Do you want to reboot the switch? (y/n)"), "y");
  assert.equal(
    confirmAnswer("Save current configuration to startup-config? (y/n)"),
    "n",
    "a save offer after delete startup-config must be declined",
  );
  assert.equal(confirmAnswer("saving config, continue? (y/n)"), "n");
}

async function rebootDeclinesSaveThenConfirms() {
  var answers: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "reboot") {
      return "\r\nSave current configuration? (y/n)";
    }
    if (line === "n" && answers.length === 0) {
      answers.push(line);
      return "\r\nDo you want to reboot the switch? (y/n)";
    }
    if (line === "y" && answers.length === 1) {
      answers.push(line);
      return "\r\nSwitch is rebooting...";
    }
    return undefined;
  });
  await rebootAnswering(session, () => {});
  assert.deepEqual(answers, ["n", "y"], "save declined first, reboot confirmed second");
}

async function rebootPlainConfirm() {
  var confirmed = false;
  var session = fakeDevice((line) => {
    if (line === "reboot") return "\r\nthis will restart the system, are you sure? (y/n)";
    if (line === "y") {
      confirmed = true;
      return "\r\nRebooting...";
    }
    return undefined;
  });
  await rebootAnswering(session, () => {});
  assert.ok(confirmed, "a plain reboot confirm gets a y");
}

async function writeSaveExplicitOk() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "write") {
      return "\r\nSaving current configuration...\r\nOK!\r\nSwitch#";
    }
    return undefined;
  });
  await saveWrite(session, (m) => logs.push(m));
  assert.ok(
    logs.some((l) => l.includes("save: ok")),
    "must recognize the OK line",
  );
}

async function writeSaveConfirmThenOk() {
  var session = fakeDevice((line) => {
    if (line === "write") return "\r\nAre you sure to save configuration? (y/n)";
    if (line === "y") return "\r\n[OK]\r\nSwitch#";
    return undefined;
  });
  await saveWrite(session, () => {});
}

async function writeSaveErrorRejects() {
  var session = fakeDevice((line) => {
    if (line === "write") {
      return "\r\nUnknown command!\r\nSwitch#";
    }
    return undefined;
  });
  await assert.rejects(
    () => saveWrite(session, () => {}),
    /save failed/,
    "a CLI error on write must reject, not hang",
  );
}

async function writeSaveBarePromptAcceptedWithWarning() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "write") return "\r\nSwitch#";
    return undefined;
  });
  await saveWrite(session, (m) => logs.push(m));
  assert.ok(
    logs.some((l) => l.includes("without an explicit confirmation")),
    "a bare prompt return must be flagged, not silently trusted",
  );
}

function upPortsParsing() {
  // SFP+ cages captured 2026-08-03: TGigaEthernet0/1-4 (short tg0/N) — these
  // are the uplinks of a 48T4SP, not g0/49-52.
  var show = [
    "GigaEthernet0/1 is down, line protocol is down",
    "GigaEthernet0/2 is up, line protocol is down",
    "TGigaEthernet0/3 is up, line protocol is up",
    "TGigaEthernet0/4 is up, line protocol is up",
    "VLAN1 is up, line protocol is down",
  ].join("\r\n");
  var ports = parseUpPorts(show);
  assert.deepEqual(ports, ["tg0/3", "tg0/4"]);
  assert.equal(compactPortRange(ports), "range tg0/3-4");
  assert.equal(compactPortRange(["tg0/1"]), "tg0/1");
  assert.equal(
    compactPortRange(["g0/48", "tg0/1"]),
    "",
    "mixed port types must not compact into one range",
  );
  assert.equal(compactPortRange(["g0/1", "g0/3"]), "", "non-contiguous must not compact");
}

function dottedMask() {
  assert.equal(toDottedMask("255.255.0.0"), "255.255.0.0");
  assert.equal(toDottedMask("/24"), "255.255.255.0");
  assert.equal(toDottedMask("16"), "255.255.0.0");
  assert.equal(toDottedMask("garbage"), "255.255.255.0");
}

async function pagerIsFed() {
  var page2Sent = false;
  var session = fakeDevice((line) => {
    if (line === "show interface | include line protocol") {
      return "\r\nGigaEthernet0/1 is up, line protocol is up\r\n--More--";
    }
    if (line === " " && !page2Sent) {
      page2Sent = true;
      return "\r\nGigaEthernet0/49 is up, line protocol is up\r\nSwitch#";
    }
    return undefined;
  });
  var out = await collectShowOutput(session, "show interface | include line protocol", 8000);
  assert.ok(page2Sent, "must feed the pager with a space");
  assert.ok(out.includes("GigaEthernet0/49"), "must collect the page behind --More--");
}

/** Mid-push logout (observed on the S3900): the password change drops the
 * console; the runner must re-login and retry the SAME command — and re-enter
 * config with "config", not "conf t". */
async function midPushLogoutRecovers() {
  var executed: string[] = [];
  var kicked = false;
  var session = fakeDevice((line) => {
    if (line === "") return "\r\nUsername: ";
    if (line === "admin") return "\r\nPassword: ";
    if (line === "SECRET9") return "\r\nSwitch# ";
    if (line === "config") {
      executed.push(line);
      return "\r\nSwitch_config# ";
    }
    if (line === "username admin password SECRET9" && !kicked) {
      kicked = true;
      return "\r\n% CLI exit session\r\n\r\nUsername: ";
    }
    if (line === "exit") return "\r\nSwitch# ";
    executed.push(line);
    return "\r\nSwitch_config# ";
  });
  var logs: string[] = [];
  await runConfigCmds(
    session,
    (m) => logs.push(m),
    [{ user: "admin", pass: "SECRET9", enable: "SECRET9", label: "instance admin" }],
    [
      { cmd: "ip http server" },
      { cmd: "username admin password SECRET9", label: "username admin password •••" },
      { cmd: "aaa authentication login default local" },
      { cmd: "enable password 0 SECRET9", label: "enable password 0 •••" },
    ],
  );
  assert.ok(kicked, "scenario must have dropped the session once");
  assert.ok(
    logs.some((l) => l.includes("fell back to login")),
    "runner must report the logout",
  );
  assert.ok(
    executed.filter((c) => c === "config").length >= 2,
    "config mode must be re-entered with \"config\" after the re-login",
  );
  var pwdRuns = executed.filter((c) => c === "username admin password SECRET9");
  assert.equal(pwdRuns.length, 1, "the kicked command must be retried once after re-login");
  assert.ok(
    executed.includes("enable password 0 SECRET9"),
    "commands after the logout must still run",
  );
}

confirmAnswers();
upPortsParsing();
dottedMask();
await rebootDeclinesSaveThenConfirms();
await rebootPlainConfirm();
await writeSaveExplicitOk();
await writeSaveConfirmThenOk();
await writeSaveErrorRejects();
await writeSaveBarePromptAcceptedWithWarning();
await pagerIsFed();
await midPushLogoutRecovers();
console.log("fsS3400SoftReset.test.ts: ok");

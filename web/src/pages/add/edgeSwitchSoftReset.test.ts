/**
 * EdgeSwitch soft-reset checks against a fake FASTPATH device:
 *   node --experimental-strip-types src/pages/add/edgeSwitchSoftReset.test.ts
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";
import { collectShowOutput, pushCmds, runCmds } from "./edgeSwitchSoftReset.ts";

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

// The mode structure is the thing to get right: the network/vlan-database
// gestures are PRIVILEGED EXEC, the interface work is Global Config, and the
// save happens back in privileged EXEC. Sending any of the first group from
// "(Config)#" fails on this platform.
function pushOrderIsTheModeStructure() {
  var cmds = pushCmds({
    vlanId: 99,
    ip: "192.0.2.20",
    mask: "255.255.0.0",
    gateway: "192.0.2.1",
    uplink: "0/16",
    targetUser: "mcadmin",
    targetPassword: "Sekret1234",
  }).map((c) => c.cmd);
  assert.deepEqual(cmds, [
    "vlan database",
    "vlan 99",
    "vlan name 99 LAN99_Management",
    "exit",
    "network protocol none",
    "network parms 192.0.2.20 255.255.0.0 192.0.2.1",
    "network mgmt_vlan 99",
    "configure",
    // No "hostname" between "configure" and the VLAN work: see
    // hostnameIsNeverSentOnThisCli below.
    // Every port becomes a trunk carrying the management VLAN: the cabling is
    // unknown at first provisioning, and the first real run failed exactly
    // because one guessed uplink was configured and the fibre was elsewhere.
    "vlan participation all include 99",
    "vlan port tagging all 99",
    "username mcadmin password Sekret1234 level 15",
    "exit",
    "write memory confirm",
  ]);
}

function vlan1NeedsNoDatabaseWork() {
  var cmds = pushCmds({
    vlanId: 1,
    ip: "192.168.1.2",
    mask: "255.255.255.0",
    uplink: "0/16",
    targetUser: "ubnt",
    targetPassword: "x",
  }).map((c) => c.cmd);
  assert.ok(!cmds.includes("vlan database"), "VLAN 1 is reserved and always exists");
  assert.ok(!cmds.some((c) => c.startsWith("network mgmt_vlan")), "VLAN 1 is the default");
  assert.ok(cmds.includes("network parms 192.168.1.2 255.255.255.0"));
}

// THIS CLI HAS NO "hostname" COMMAND, and this test pins the absence.
//
// Sent to the lab EdgePoint S16 on 2026-08-05, "hostname unifimoule1" came back
// "% Invalid input detected at '^' marker." That is why the Go connector's
// setter is private (internal/driver/ubiquiti/edgeswitch/hostname.go) and why
// `ubiquiti-edgeswitch` reads `unsupported` in the hostname capability table.
// This script sent it anyway; the refusal is non-fatal, so the run continued and
// the only trace was a spurious CLI error in the operator's transcript — noise
// that makes a real refusal harder to spot. Nothing here may utter the verb until
// a working form is captured on hardware.
function hostnameIsNeverSentOnThisCli() {
  var cmds = pushCmds({
    vlanId: 99,
    ip: "192.0.2.20",
    mask: "255.255.0.0",
    uplink: "0/16",
    targetUser: "mcadmin",
    targetPassword: "Sekret1234",
  }).map((c) => c.cmd);
  assert.ok(
    !cmds.some((c) => c.includes("hostname")),
    "this FASTPATH CLI refused \"hostname <name>\" on 2026-08-05 — the push must not send it",
  );
}

// "write memory confirm" must never lose its confirm keyword: the plain form
// asks a question, and a script that does not answer hangs on it.
function saveNeverPrompts() {
  var cmds = pushCmds({
    vlanId: 1, ip: "10.0.0.2", mask: "255.255.255.0",
    uplink: "0/16", targetUser: "u", targetPassword: "p",
  }).map((c) => c.cmd);
  assert.equal(cmds[cmds.length - 1], "write memory confirm");
}

// A confirmation must be answered, not waited out: "clear config" asks.
async function confirmsAreAnswered() {
  var answered = false;
  var session = fakeDevice((line) => {
    if (line === "clear config") {
      return "\r\nAre you sure you want to clear the configuration? (y/n) ";
    }
    if (line === "y") {
      answered = true;
      return "\r\n(UBNT EdgeSwitch) #";
    }
    return "\r\n(UBNT EdgeSwitch) #";
  });
  var logs: string[] = [];
  await runCmds(session, (m) => logs.push(m), [], [{ cmd: "clear config" }]);
  assert.ok(answered, "the confirmation must be answered with y");
}

// FASTPATH does not print the IOS "% Invalid input" form — an error must
// still be recognized and must not stall the run.
async function cliErrorsAreLoggedNotFatal() {
  var ran: string[] = [];
  var session = fakeDevice((line) => {
    ran.push(line);
    if (line === "bogus") return "\r\nCommand not found\r\n(UBNT EdgeSwitch) #";
    return "\r\n(UBNT EdgeSwitch) #";
  });
  var logs: string[] = [];
  await runCmds(session, (m) => logs.push(m), [], [{ cmd: "bogus" }, { cmd: "network protocol none" }]);
  assert.ok(logs.some((l) => l.includes("CLI error")), "the refusal must be reported");
  assert.ok(ran.includes("network protocol none"), "the run must continue past an error");
}

// The pager must be FED: an unfed "--More--" eats the first character of the
// next command, which is how "show ..." becomes "how ...".
async function pagerIsFed() {
  var page2 = false;
  var session = fakeDevice((line) => {
    if (line === "show network") {
      return "\r\nIP Address..... 192.0.2.20\r\n--More-- or (q)uit";
    }
    if (line === " " && !page2) {
      page2 = true;
      return "\r\nSubnet Mask.... 255.255.0.0\r\n(UBNT EdgeSwitch) #";
    }
    return undefined;
  });
  var out = await collectShowOutput(session, "show network", 8000);
  assert.ok(page2, "must feed the pager");
  assert.ok(out.includes("255.255.0.0"), "must collect the page behind --More--");
}

pushOrderIsTheModeStructure();
vlan1NeedsNoDatabaseWork();
hostnameIsNeverSentOnThisCli();
saveNeverPrompts();
await confirmsAreAnswered();
await cliErrorsAreLoggedNotFatal();
await pagerIsFed();
console.log("edgeSwitchSoftReset.test.ts: ok");

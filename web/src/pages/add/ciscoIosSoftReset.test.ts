/**
 * Console-flow checks for the Cisco Catalyst soft reset:
 *   node --experimental-strip-types src/pages/add/ciscoIosSoftReset.test.ts
 *
 * These cover the prompts that decide whether the procedure works at all, and
 * the parsing the push depends on. Nothing here has been captured on hardware
 * (cf. the module header): these fixtures are the documented output shapes, and
 * they are what a real capture will be diffed against.
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";
import {
  acceptThirdPartyOptics,
  collectShow,
  fullIfName,
  parseErrDisabled,
  parseUpPorts,
  runConfigCmds,
  saveConfig,
  toDottedMask,
  UNSUPPORTED_TRANSCEIVER_CMDS,
} from "./ciscoIosSoftReset.ts";

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

function maskConversions() {
  assert.equal(toDottedMask("255.255.0.0"), "255.255.0.0");
  assert.equal(toDottedMask("16"), "255.255.0.0");
  assert.equal(toDottedMask("/24"), "255.255.255.0");
  assert.equal(toDottedMask("22"), "255.255.252.0");
  // IOS takes a dotted mask and nothing else: an unreadable input must still
  // produce a usable mask rather than a command the CLI rejects.
  assert.equal(toDottedMask("nonsense"), "255.255.255.0");
}

function interfaceNames() {
  assert.equal(fullIfName("Gi1/0/28"), "GigabitEthernet1/0/28");
  assert.equal(fullIfName("Te1/0/1"), "TenGigabitEthernet1/0/1");
  assert.equal(fullIfName("Fa0/1"), "FastEthernet0/1");
  assert.equal(fullIfName("GigabitEthernet1/0/2"), "GigabitEthernet1/0/2");
}

function upPorts() {
  // The Name column is free text and may contain spaces: splitting on
  // whitespace from the left shifts every field on any port someone described.
  var status =
    "Port      Name               Status       Vlan       Duplex  Speed Type\r\n" +
    "Gi1/0/1                      notconnect   1            auto   auto 10/100/1000BaseTX\r\n" +
    "Gi1/0/2   salle de reunion   connected    10           a-full a-1000 10/100/1000BaseTX\r\n" +
    "Gi1/0/3                      disabled     1            auto   auto 10/100/1000BaseTX\r\n" +
    "Gi1/0/28  uplink core        connected    trunk        full   1000 1000BaseSX SFP\r\n" +
    "Te1/0/1                      notconnect   1            full    10G unknown\r\n";
  var up = parseUpPorts(status);
  assert.deepEqual(up, ["GigabitEthernet1/0/2", "GigabitEthernet1/0/28"]);
  // "disabled" is not "connected": an administratively down port carries nothing.
  assert.ok(!up.includes("GigabitEthernet1/0/3"));
}

async function saveAnswersTheOverwritePrompt() {
  // After an IOS upgrade, « write memory » asks before overwriting NVRAM that
  // an older image wrote. Waiting that prompt out leaves the CLI sitting at the
  // question, and the NEXT command is typed in as its answer.
  var logs: string[] = [];
  var asked = false;
  var session = fakeDevice((line) => {
    if (line === "write memory") {
      asked = true;
      return (
        "\r\nWarning: Attempting to overwrite an NVRAM configuration previously written\r\n" +
        "by a different version of the system image.\r\n" +
        "Overwrite the previous NVRAM configuration?[confirm]"
      );
    }
    if (line === "" && asked) {
      return "\r\nBuilding configuration...\r\n[OK]\r\nlab-cat2960x#";
    }
    return undefined;
  });
  await saveConfig(session, (m) => logs.push(m));
  assert.ok(
    logs.some((l) => l.includes("written to NVRAM")),
    "the overwrite prompt must be answered and the [OK] seen",
  );
}

async function saveFailsLoudly() {
  // A management configuration that does not survive a reboot is worse than a
  // refused one: the operator believes it is in place.
  var session = fakeDevice((line) => {
    if (line === "write memory") {
      return "\r\n% Invalid input detected at '^' marker.\r\nlab-cat2960x#";
    }
    return undefined;
  });
  await assert.rejects(() => saveConfig(session, () => {}), /save failed/);
}

async function syslogIsNotAFailure() {
  // IOS prints syslog onto the console, mid-command, starting with a percent
  // sign. Mistaking one for a refusal turns every port that comes up during the
  // push into a failed command.
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "configure terminal") return "\r\nlab-cat2960x(config)#";
    if (line === "interface vlan 99") return "\r\nlab-cat2960x(config-if)#";
    if (line === "no shutdown") {
      return (
        "\r\n%LINK-3-UPDOWN: Interface Vlan99, changed state to up\r\n" +
        "%LINEPROTO-5-UPDOWN: Line protocol on Interface Vlan99, changed state to up\r\n" +
        "lab-cat2960x(config-if)#"
      );
    }
    if (line === "end") return "\r\nlab-cat2960x#";
    return "\r\nlab-cat2960x(config-if)#";
  });
  await runConfigCmds(
    session,
    (m) => logs.push(m),
    [],
    [{ cmd: "interface vlan 99" }, { cmd: "no shutdown" }],
  );
  assert.ok(
    !logs.some((l) => l.includes("CLI error")),
    "syslog must not be reported as a CLI refusal: " + logs.join(" | "),
  );
}

async function realRefusalIsReported() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "configure terminal") return "\r\nlab-cat2960x(config)#";
    if (line === "switchport mode trunk") {
      return (
        "\r\nCommand rejected: An interface whose trunk encapsulation is \"Auto\" " +
        "can not be configured to \"trunk\" mode.\r\nlab-cat2960x(config-if)#"
      );
    }
    if (line === "end") return "\r\nlab-cat2960x#";
    return "\r\nlab-cat2960x(config-if)#";
  });
  await runConfigCmds(
    session,
    (m) => logs.push(m),
    [],
    [{ cmd: "switchport mode trunk" }],
  );
  assert.ok(
    logs.some((l) => l.includes("CLI error") && l.includes("Command rejected")),
    "a real refusal must be reported: " + logs.join(" | "),
  );
}

async function pagerIsFed() {
  // Web Serial cannot claim a tall terminal, so output that fits a PuTTY window
  // may still paginate here — and every later keystroke then feeds the pager
  // instead of the CLI.
  var page = 0;
  var session = fakeDevice((line) => {
    if (line === "show interfaces status") {
      page = 1;
      return "\r\nPort      Name    Status\r\nGi1/0/1           connected\r\n --More-- ";
    }
    if (line === " " && page === 1) {
      page = 2;
      return "\r\nGi1/0/28          connected\r\nlab-cat2960x#";
    }
    return undefined;
  });
  var out = await collectShow(session, "show interfaces status", 6000);
  assert.ok(out.includes("Gi1/0/28"), "the second page must be collected: " + out);
  assert.deepEqual(parseUpPorts(out), [
    "GigabitEthernet1/0/1",
    "GigabitEthernet1/0/28",
  ]);
}

function errDisabledParsing() {
  var out =
    "Port      Name               Status       Reason               Err-disabled Vlans\r\n" +
    "Gi1/0/25                     err-disabled gbic-invalid\r\n" +
    "Gi1/0/26  lien secours       err-disabled gbic-invalid\r\n" +
    "Gi1/0/10  poste 4            err-disabled bpduguard\r\n";
  var shut = parseErrDisabled(out);
  assert.deepEqual(shut, [
    { port: "GigabitEthernet1/0/25", reason: "gbic-invalid" },
    { port: "GigabitEthernet1/0/26", reason: "gbic-invalid" },
    { port: "GigabitEthernet1/0/10", reason: "bpduguard" },
  ]);
}

async function opticsAreAcceptedAndOnlyGbicPortsBounced() {
  // Disabling the detection does NOT clear a port that is already err-disabled:
  // a cage refused at boot stays dark until it is bounced. And bouncing a
  // bpduguard port would hide a loop somebody just created, so only the
  // transceiver ones are touched.
  var sent: string[] = [];
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    sent.push(line);
    if (line === "show interfaces status err-disabled") {
      return (
        "\r\nPort      Name               Status       Reason\r\n" +
        "Gi1/0/25                     err-disabled gbic-invalid\r\n" +
        "Gi1/0/10                     err-disabled bpduguard\r\n" +
        "lab-cat2960x#"
      );
    }
    if (line === "service unsupported-transceiver") {
      // IOS prints its warranty disclaimer here and returns to the prompt.
      return (
        "\r\nWarning: When Cisco determines that a fault or defect can be traced to\r\n" +
        "the use of third-party transceivers installed by a customer or reseller,\r\n" +
        "then, at Cisco's discretion, Cisco may withhold support under warranty.\r\n" +
        "lab-cat2960x(config)#"
      );
    }
    if (line === "end") return "\r\nlab-cat2960x#";
    if (line === "configure terminal") return "\r\nlab-cat2960x(config)#";
    return "\r\nlab-cat2960x(config)#";
  });

  await acceptThirdPartyOptics(session, (m) => logs.push(m), []);

  var i = 0;
  for (i = 0; i < UNSUPPORTED_TRANSCEIVER_CMDS.length; i++) {
    assert.ok(
      sent.includes(UNSUPPORTED_TRANSCEIVER_CMDS[i]),
      `must send ${UNSUPPORTED_TRANSCEIVER_CMDS[i]}: ${sent.join(" | ")}`,
    );
  }
  // The warranty warning starts with no percent sign and is not a refusal;
  // treating it as one would abandon the run right here.
  assert.ok(
    !logs.some((l) => l.includes("CLI error")),
    "Cisco's warranty warning must not read as a CLI refusal: " + logs.join(" | "),
  );
  assert.ok(
    sent.includes("interface GigabitEthernet1/0/25"),
    "the refused cage must be bounced: " + sent.join(" | "),
  );
  assert.ok(
    !sent.includes("interface GigabitEthernet1/0/10"),
    "a bpduguard port must be LEFT ALONE — bouncing it hides a loop: " + sent.join(" | "),
  );
  assert.ok(
    logs.some((l) => l.includes("bpduguard")),
    "the port left alone must still be reported: " + logs.join(" | "),
  );
  assert.equal(sent.filter((l) => l === "shutdown").length, 1);
  assert.equal(sent.filter((l) => l === "no shutdown").length, 1);
}

async function opticsStepIsQuietWhenNothingIsShut() {
  var sent: string[] = [];
  var session = fakeDevice((line) => {
    sent.push(line);
    if (line === "show interfaces status err-disabled") {
      return "\r\nPort      Name               Status       Reason\r\nlab-cat2960x#";
    }
    if (line === "end") return "\r\nlab-cat2960x#";
    return "\r\nlab-cat2960x(config)#";
  });
  await acceptThirdPartyOptics(session, () => {}, []);
  assert.ok(
    !sent.includes("shutdown"),
    "nothing to recover: no port may be bounced — " + sent.join(" | "),
  );
}

async function main() {
  maskConversions();
  interfaceNames();
  upPorts();
  errDisabledParsing();
  await opticsAreAcceptedAndOnlyGbicPortsBounced();
  await opticsStepIsQuietWhenNothingIsShut();
  await saveAnswersTheOverwritePrompt();
  await saveFailsLoudly();
  await syslogIsNotAFailure();
  await realRefusalIsReported();
  await pagerIsFed();
  console.log("ciscoIosSoftReset.test.ts: ok");
}

void main();

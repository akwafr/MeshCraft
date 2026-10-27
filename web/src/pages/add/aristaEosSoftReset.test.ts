/**
 * Console-flow checks for the Arista EOS provisioning script:
 *   node --experimental-strip-types src/pages/add/aristaEosSoftReset.test.ts
 *
 * These cover the decisions that make the procedure work at all — the ZTP state
 * detection above everything else — and the parsing the push depends on. Nothing
 * here has been captured on hardware (cf. the module header): these fixtures are
 * the documented output shapes, and they are what a real capture will be diffed
 * against.
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";
import {
  acceptThirdPartyOptics,
  allowedVlanList,
  buildPushCmds,
  collectShow,
  consoleLooksLikeZTP,
  detectConsoleState,
  ensurePrivileged,
  FAN_SPEED_AUTO_CMD,
  FAN_SPEED_OVERRIDE_CMD,
  mergeSiteVlans,
  orderLoginsForZTP,
  parseAllPorts,
  parseErrDisabled,
  parseUpPorts,
  portNumberOf,
  portRanges,
  readConsoleState,
  runConfigCmds,
  sanitizeVlanName,
  saveConfig,
  toPrefixLen,
  UNSUPPORTED_TRANSCEIVER_CMDS,
} from "./aristaEosSoftReset.ts";

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

const STATUS_FIXTURE =
  "Port       Name              Status       Vlan     Duplex Speed  Type            Flags\r\n" +
  "Et1                          connected    1        full   1G     1000BASE-T\r\n" +
  "Et2                          notconnect   1        auto   auto   Not Present\r\n" +
  "Et3        meeting room      connected    30       full   1G     1000BASE-T\r\n" +
  "Et4                          disabled     1        auto   auto   Not Present\r\n" +
  "Et49       core uplink       connected    trunk    full   10G    10GBASE-SR\r\n" +
  "Et50                         errdisabled  trunk    full   10G    Not Present\r\n" +
  "Ma1                          connected    routed   full   1G     10/100/1000\r\n" +
  "Po1                          connected    trunk    full   10G    N/A\r\n" +
  "Et51/1                       connected    trunk    full   25G    25GBASE-CR\r\n";

// --- the mask, which is the mirror image of the Cisco script's ---

function maskConversions() {
  // EOS takes "ip address 192.0.2.5/24" and REFUSES the two-argument dotted
  // form IOS demands. This is the single most consequential difference between
  // the two platforms' pushes, and a shared helper would have had to guess which
  // device it was talking to.
  assert.equal(toPrefixLen("255.255.0.0"), 16);
  assert.equal(toPrefixLen("255.255.255.0"), 24);
  assert.equal(toPrefixLen("255.255.252.0"), 22);
  assert.equal(toPrefixLen("16"), 16);
  assert.equal(toPrefixLen("/24"), 24);
  // An unreadable input must still produce a usable prefix rather than a command
  // the CLI rejects.
  assert.equal(toPrefixLen("nonsense"), 24);
  assert.equal(toPrefixLen("255.0.255.0"), 16); // non-contiguous: bits counted, not guessed at
}

// --- the state machine, which is what this platform needs and no other does ---

function ztpDetection() {
  // The authoritative answer.
  assert.equal(readConsoleState("ZeroTouch: on", "").state, "ztp");
  assert.equal(readConsoleState("ZeroTouch provisioning mode is enabled", "").state, "ztp");
  assert.equal(readConsoleState("ZeroTouch: off", "").state, "cli");
  assert.equal(readConsoleState("ZeroTouch provisioning mode is disabled", "").state, "cli");

  // The fallback, for a train that does not carry "show zerotouch". A switch in
  // ZTP will not stop talking about it — the retry loop logs a line every thirty
  // seconds or so — which is exactly what makes this a good fallback.
  assert.equal(
    readConsoleState(
      "% Invalid input",
      "Aug  6 12:00:30 localhost ZeroTouch: %ZTP-5-DHCP_QUERY_FAIL: Failed to get a " +
        "valid DHCP response\r\nlocalhost login:",
    ).state,
    "ztp",
  );

  // Neither says anything: "unknown", NOT a guess. The caller then treats it as a
  // configured CLI, which is the conservative order — a full reset runs and lands
  // in ZTP, which the next phase handles. Guessing "ztp" on a configured switch
  // would send "zerotouch cancel" to a device that would reboot into a
  // configuration nobody meant to keep.
  assert.equal(readConsoleState("", "sw-lab-1#").state, "unknown");
}

// Each branch must NAME ITSELF, because the verdict alone has already been
// recorded once by a lab run and left the open question open: nobody could tell
// afterwards whether "show zerotouch" had answered or whether the console-text
// fallback had carried the run. The verdicts asserted here are the same ones
// asserted above — this is about the evidence beside them.
function ztpDetectionSaysWhichBranchAnsweredIt() {
  // "show zerotouch" answering for itself, in both directions. Same source for
  // "on" and for "off": what matters is that the COMMAND spoke.
  assert.deepEqual(readConsoleState("ZeroTouch: on", ""), { state: "ztp", from: "zerotouch" });
  assert.deepEqual(readConsoleState("ZeroTouch provisioning mode is armed", ""), {
    state: "ztp",
    from: "zerotouch",
  });
  assert.deepEqual(readConsoleState("ZeroTouch: off", ""), { state: "cli", from: "zerotouch" });

  // The command spoke AND the console is full of ZTP chatter: the command wins,
  // and must be reported as the one that won — otherwise a train whose wording
  // this script does not know would look, in the log, exactly like one it does.
  assert.deepEqual(
    readConsoleState(
      "ZeroTouch: on",
      "Aug  6 12:00:30 localhost ZeroTouch: %ZTP-5-RETRY: Retrying Zero Touch Provisioning",
    ),
    { state: "ztp", from: "zerotouch" },
  );

  // The fallback carrying the run alone — the case the next console run has to be
  // able to distinguish, since it is the one that says the wording moved.
  assert.deepEqual(
    readConsoleState(
      "% Invalid input",
      "Aug  6 12:00:30 localhost ZeroTouch: %ZTP-5-DHCP_QUERY_FAIL: Failed to get a " +
        "valid DHCP response\r\nlocalhost login:",
    ),
    { state: "ztp", from: "console" },
  );

  // Nothing matched anywhere.
  assert.deepEqual(readConsoleState("", "sw-lab-1#"), { state: "unknown", from: "neither" });
}

// The run log has to carry both the branch and the RAW answer, because the open
// question — what wording does this train actually print? — is answerable only
// from a console, and one run should settle it rather than prove the verdict was
// right and send someone back for a second maintenance window.
async function detectionLogsItsBranchAndTheRawAnswer() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "show zerotouch") return "\r\nZeroTouch: on\r\nsw-lab-1#";
    return undefined;
  });
  var state = await detectConsoleState(session, (m) => logs.push(m));
  assert.equal(state, "ztp");
  assert.ok(
    logs.some((l) => l.includes("ZTP state settled by:") && l.includes("\"show zerotouch\" itself")),
    "the branch that fired is not in the log: " + logs.join(" | "),
  );
  // Verbatim and delimited: JSON quoting is what makes the wording liftable —
  // trailing spaces and CRs and all — straight into a fixture.
  assert.ok(
    logs.some((l) => l.includes("verbatim") && l.includes('"ZeroTouch: on\\r\\nsw-lab-1#"')),
    "the raw \"show zerotouch\" answer is not in the log: " + logs.join(" | "),
  );
  assert.ok(
    !logs.some((l) => l.includes("TRUNCATED")),
    "a two-line answer must not be reported as truncated: " + logs.join(" | "),
  );
}

// The same run, on a train that does not carry the command: the fallback decides,
// the log says so, and the useless answer is still printed — "% Invalid input"
// IS the finding in that case.
async function detectionLogsTheFallbackWhenTheCommandIsUseless() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "show zerotouch") {
      return (
        "\r\n% Invalid input\r\n" +
        "Aug  6 15:44:44 localhost ZeroTouch: %ZTP-5-RETRY: Retrying Zero Touch " +
        "Provisioning from the beginning (attempt 129)\r\nlocalhost login:"
      );
    }
    return undefined;
  });
  var state = await detectConsoleState(session, (m) => logs.push(m));
  assert.equal(state, "ztp");
  assert.ok(
    logs.some((l) => l.includes("ZTP state settled by:") && l.includes("CONSOLE-TEXT fallback")),
    "the fallback did not name itself: " + logs.join(" | "),
  );
  assert.ok(
    logs.some((l) => l.includes("verbatim") && l.includes("% Invalid input")),
    "the raw answer is not in the log: " + logs.join(" | "),
  );
}

// Nothing matched: the verdict is "unknown", and the log must say that BOTH
// halves came up empty rather than leaving it to be inferred from a missing line.
async function detectionSaysWhenNeitherHalfMatched() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "show zerotouch") return "\r\n% Invalid input (at marker)\r\nsw-lab-1#";
    return undefined;
  });
  var state = await detectConsoleState(session, (m) => logs.push(m));
  assert.equal(state, "unknown");
  assert.ok(
    logs.some((l) => l.includes("ZTP state settled by:") && l.includes("NEITHER")),
    "the empty case did not name itself: " + logs.join(" | "),
  );
}

// A long answer must be clipped AND must admit it — a log line that silently lost
// its tail is worse than none, since the whole point is to copy it into a fixture.
async function detectionAdmitsWhenItTruncates() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "show zerotouch") {
      return "\r\nZeroTouch: on\r\n" + "filler line of zerotouch debug\r\n".repeat(40) + "sw-lab-1#";
    }
    return undefined;
  });
  await detectConsoleState(session, (m) => logs.push(m));
  var verbatim = logs.filter((l) => l.includes("verbatim"));
  assert.equal(verbatim.length, 1, "expected exactly one verbatim line: " + logs.join(" | "));
  assert.ok(
    verbatim[0].includes("TRUNCATED to the first 600 of"),
    "a long answer was clipped without saying so: " + verbatim[0],
  );
}

// --- the faceplate, and the three kinds of port this script must not write to ---

function portNames() {
  assert.equal(portNumberOf("Ethernet49"), 49);
  assert.equal(portNumberOf("Et49"), 49);
  assert.equal(portNumberOf("et 3"), 3);
  // A breakout lane: four of them share a cage, and this script has no way to
  // name them apart from the cage itself.
  assert.equal(portNumberOf("Et49/1"), null);
  // The out-of-band port and a logical interface.
  assert.equal(portNumberOf("Ma1"), null);
  assert.equal(portNumberOf("Management1"), null);
  assert.equal(portNumberOf("Po1"), null);
  assert.equal(portNumberOf("Vlan99"), null);
}

function faceplateParsing() {
  var all = parseAllPorts(STATUS_FIXTURE);
  // Ma1, Po1 and the breakout lane are all excluded. A "switchport mode trunk"
  // on the out-of-band port is a refused command in the middle of the push.
  assert.deepEqual(all, [
    "Ethernet1",
    "Ethernet2",
    "Ethernet3",
    "Ethernet4",
    "Ethernet49",
    "Ethernet50",
  ]);

  // The Name column is free text and may contain spaces: splitting on whitespace
  // from the left shifts every field on any port somebody has described.
  var up = parseUpPorts(STATUS_FIXTURE);
  assert.deepEqual(up, ["Ethernet1", "Ethernet3", "Ethernet49"]);
  assert.ok(!up.includes("Ethernet4"), "\"disabled\" is not \"connected\"");
  assert.ok(!up.includes("Ethernet50"), "\"errdisabled\" is not \"connected\"");
}

function errDisabledParsing() {
  var out =
    "Port      Name          Status       Reason\r\n" +
    "Et50                    errdisabled  xcvr-unsupported\r\n" +
    "Et51      uplink b      err-disabled bpduguard\r\n";
  var got = parseErrDisabled(out);
  assert.deepEqual(got, [
    { port: "Ethernet50", reason: "xcvr-unsupported" },
    { port: "Ethernet51", reason: "bpduguard" },
  ]);
}

// This console runs at 9600 8N1 and every command waits for its prompt. Trunking
// a 48-port switch one port at a time is around two hundred and fifty round
// trips, many minutes during which a reset switch sits with every port up in
// VLAN 1.
function rangeGrouping() {
  assert.deepEqual(portRanges(["Ethernet1", "Ethernet2", "Ethernet3"]), ["Ethernet1-3"]);
  // A hole in the numbering — every switch with a breakout cage in service —
  // must produce two runs, not one range that covers a port that is not there.
  assert.deepEqual(
    portRanges(["Ethernet1", "Ethernet2", "Ethernet49", "Ethernet50"]),
    ["Ethernet1-2,49-50"],
  );
  assert.deepEqual(portRanges(["Ethernet7"]), ["Ethernet7"]);
  // Chunked, because a console line has a length beyond which the CLI stops
  // reading.
  var scattered = portRanges(
    [1, 3, 5, 7, 9, 11, 13, 15, 17, 19].map((n) => "Ethernet" + n),
    4,
  );
  assert.equal(scattered.length, 3);
  assert.equal(scattered[0], "Ethernet1,3,5,7");
  assert.deepEqual(portRanges([]), []);
}

// --- the allowed list, and the VLAN-1 trap ---

function allowedList() {
  assert.deepEqual(allowedVlanList([{ id: 30 }, { id: 40 }], 99), [1, 30, 40, 99]);
  // EOS drops native-VLAN traffic on a trunk whose allowed list does not contain
  // the native VLAN. On a site whose management VLAN is 1 — the common small
  // deployment — a list built from the site VLANs alone would produce a trunk
  // that looks perfectly configured and forwards nothing at all, including the
  // management traffic this run is about to depend on.
  assert.deepEqual(allowedVlanList([{ id: 30 }], 1), [1, 30]);
  assert.deepEqual(allowedVlanList(undefined, 99), [1, 99]);
}

function vlanMerging() {
  // VLAN 1 always exists: creating it would be the one line in the push that
  // modifies a VLAN the operator never declared.
  assert.deepEqual(mergeSiteVlans([{ id: 1, name: "default" }, { id: 30, name: "Voice" }], 99), [
    { id: 30, name: "Voice" },
    { id: 99, name: "LAN99_Management" },
  ]);
  // A site entry for the management VLAN keeps ITS name and is not duplicated.
  assert.deepEqual(mergeSiteVlans([{ id: 99, name: "Admin" }], 99, "ignored"), [
    { id: 99, name: "Admin" },
  ]);
  assert.equal(sanitizeVlanName("LAN99 Management"), "LAN99_Management");
  assert.equal(sanitizeVlanName("!!!"), "Management");
}

// --- the system name: ONE normalizer, and refusals that are said out loud ---

// This range used to carry its own exported `sanitizeHostname`. It was the least
// wrong of the four scripts — it mapped `_` to `-` and it trimmed — and it still
// DROPPED silently, so "Switch étage 1" went onto the console banner as
// `Switch-tage-1`. These assertions pin the absence of that doctrine: they fail
// if a local sanitizer comes back, rather than merely looking odd in a diff.
function hostnameGoesThroughTheSharedNormalizer() {
  var logs: string[] = [];
  var build = (hostname: string) =>
    buildPushCmds({
      mgmtVlan: 99,
      ip: "192.0.2.5",
      prefixLen: 16,
      hostname,
      trunkPorts: ["Ethernet1"],
      user: "mcadmin",
      password: "s3cret",
      log: (m) => logs.push(m),
    }).map((c) => c.cmd);

  // The separators an operator meant as separators still become hyphens.
  assert.ok(build("sw lab 1").includes("hostname sw-lab-1"));
  assert.equal(logs.length, 0, "a name that normalizes must log nothing");

  // arista/eos (Go and TS): dropped what it did not recognise.
  var accented = build("Switch étage 1");
  assert.ok(
    !accented.some((l) => l.startsWith("hostname ")),
    "an accented name must produce NO hostname line: " + accented.join(" | "),
  );
  assert.ok(
    !accented.some((l) => l.includes("Switch-tage-1")),
    "the dropped-character spelling is back",
  );
  assert.equal(logs.length, 1, "the refusal must be reported exactly once");
  assert.match(logs[0], /"é"/, "the refusal must name the offending character");
  assert.match(logs[0], /NOT written/);
  // The rest of the push is unaffected — a refused system name must not cost the
  // switch its management address.
  assert.ok(accented.includes("ip address 192.0.2.5/16"));
  assert.ok(accented.some((l) => l.startsWith("username mcadmin")));

  // All four scripts, and all five Go spellings, fell back to the literal
  // `switch` on an empty result: a fleet of identically-named devices, produced
  // by the gesture meant to give each switch an identity.
  var nothing = build("!!!");
  assert.ok(
    !nothing.some((l) => l.startsWith("hostname ")),
    "a name with nothing usable in it must not fall back to a literal",
  );
  assert.ok(!nothing.includes("hostname switch"));

  // The 63-octet ceiling REFUSES; it never truncates.
  var long = build("x".repeat(64));
  assert.ok(!long.some((l) => l.startsWith("hostname ")));
  assert.ok(!long.some((l) => l.includes("x".repeat(63))), "the name was truncated");

  // The injection boundary: this string is concatenated into a config command.
  var nasty = build("core01\nusername attacker privilege 15");
  assert.ok(!nasty.some((l) => l.startsWith("hostname ")));
  assert.ok(!nasty.some((l) => l.includes("attacker")));
}

// --- the push, whose ORDER is load-bearing ---

function pushOrder() {
  var cmds = buildPushCmds({
    mgmtVlan: 99,
    ip: "192.0.2.5",
    prefixLen: 16,
    gateway: "192.0.2.1",
    hostname: "sw lab 1",
    siteVlans: [{ id: 30, name: "Voice" }, { id: 40, name: "Data" }],
    trunkPorts: ["Ethernet1", "Ethernet2", "Ethernet49"],
    user: "mcadmin",
    password: "s3cret",
  });
  var lines = cmds.map((c) => c.cmd);
  var at = (needle: string) => lines.findIndex((l) => l.startsWith(needle));

  // 1. THE OPTICS FIRST, before anything that depends on a link existing: EOS
  //    shuts a cage holding an optic it does not accept, and such a port is
  //    indistinguishable from an empty one.
  assert.equal(at("service unsupported-transceiver"), 0);
  assert.ok(lines.includes(UNSUPPORTED_TRANSCEIVER_CMDS[1]));

  // 2. VLANs before any port is told to carry them.
  assert.ok(at("vlan 30") < at("switchport trunk allowed vlan"));
  // 3. Trunks before the SVI gets an address, so management has a path the
  //    moment it has an address.
  assert.ok(at("switchport trunk allowed vlan") < at("interface Vlan99"));
  // 4. Credentials LAST: the console can drop right at the password change of
  //    the account currently logged in.
  assert.ok(at("username mcadmin") > at("interface Vlan99"));
  // NO ENABLE PASSWORD, by fleet rule — and APPLIED, not merely omitted: not
  // writing it would leave the rule true on a factory switch and false on every
  // switch that already had one, which is exactly the fleet where it matters.
  assert.ok(
    !lines.some((l) => /^enable (secret|password)\b/.test(l)),
    "an enable password was written: " + lines.join(" | "),
  );
  assert.equal(
    lines[lines.length - 1],
    "no enable secret",
    "the rule must be applied LAST, after the privileged account exists — at no " +
      "point may the device be left without a way in: " + lines.join(" | "),
  );

  // The PREFIX form, and no "ip default-gateway" — the two IOS reflexes that
  // produce a rejected line here.
  assert.ok(lines.includes("ip address 192.0.2.5/16"));
  assert.ok(!lines.some((l) => l.startsWith("ip default-gateway")));
  assert.ok(lines.includes("ip route 0.0.0.0/0 192.0.2.1"));
  // There is no trunk encapsulation on this platform.
  assert.ok(!lines.some((l) => l.includes("encapsulation")));
  // There is no key ceremony either: SSH is on and keyed from first boot.
  assert.ok(!lines.some((l) => l.includes("crypto key")));
  assert.ok(!lines.some((l) => l.startsWith("ip domain-name")));

  // The SVI comes up shut and must be brought up.
  var sviAt = lines.indexOf("interface Vlan99");
  assert.equal(lines[sviAt + 2], "no shutdown");

  // The allowed list is the SET form, never "add": on EOS a trunk with no
  // allowed line already carries everything, so "add" changes nothing and
  // reports success.
  assert.ok(lines.includes("switchport trunk allowed vlan 1,30,40,99"));
  assert.ok(!lines.some((l) => l.includes("allowed vlan add")));
  // THE MANAGEMENT VLAN IS TAGGED, NOT NATIVE. Making it native sends
  // administration traffic UNTAGGED, which a correctly configured upstream — one
  // expecting VLAN 99 tagged — drops; and it parks the administration VLAN on the
  // one part of a trunk that carries no tag. The native VLAN is left at the
  // platform default, which is why VLAN 1 must stay in the allowed list.
  assert.ok(
    !lines.some((l) => l.startsWith("switchport trunk native vlan")),
    "a native VLAN was written on the provisioned trunks: " + lines.join(" | "),
  );
  assert.ok(lines.includes("switchport trunk allowed vlan 1,30,40,99"));
  // Every faceplate port, as one range block rather than three.
  assert.ok(lines.includes("interface Ethernet1-2,49"));

  // EOS authorisation reads the ROLE; "privilege 15" alone leaves an account
  // able to log in and unable to configure anything.
  var user = lines.find((l) => l.startsWith("username mcadmin")) ?? "";
  assert.ok(user.includes("role network-admin"));
  assert.ok(user.includes("secret 0 s3cret"));
  // …and the secret must not reach the transcript.
  var label = cmds.find((c) => c.cmd.startsWith("username mcadmin"))?.label ?? "";
  assert.ok(!label.includes("s3cret"), "the secret leaked into the log label: " + label);
}

// --- the console behaviours ---

async function syslogIsNotAFailure() {
  // EOS prints syslog onto the console mid-command, starting with a percent
  // sign. Mistaking one for a refusal turns every port that comes up during the
  // push into a failed command — and on this platform a provisioning run brings
  // a whole faceplate to life at once, so that is every run.
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "configure") return "\r\nsw-lab-1(config)#";
    if (line === "interface Ethernet1-48") return "\r\nsw-lab-1(config-if-Et1-48)#";
    if (line === "no shutdown") {
      return (
        "\r\nAug  6 12:00:00 sw-lab-1 Ebra: %LINEPROTO-5-UPDOWN: Line protocol on " +
        "Interface Ethernet1, changed state to up\r\n" +
        "Aug  6 12:00:00 sw-lab-1 Ebra: %LINEPROTO-5-UPDOWN: Line protocol on " +
        "Interface Ethernet2, changed state to up\r\n" +
        "sw-lab-1(config-if-Et1-48)#"
      );
    }
    if (line === "end") return "\r\nsw-lab-1#";
    return "\r\nsw-lab-1(config-if-Et1-48)#";
  });
  await runConfigCmds(
    session,
    (m) => logs.push(m),
    [],
    [{ cmd: "interface Ethernet1-48" }, { cmd: "no shutdown" }],
  );
  assert.ok(
    !logs.some((l) => l.includes("CLI error")),
    "syslog must not be reported as a CLI refusal: " + logs.join(" | "),
  );
}

async function configSeparatorIsNotAFailure() {
  // "!" is the running configuration's section separator. Matching the
  // character rather than the refusal phrasings would make every configuration
  // read look like an error.
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "configure") return "\r\nsw-lab-1(config)#";
    if (line === "vlan 99") return "\r\n!\r\nsw-lab-1(config-vlan-99)#";
    if (line === "end") return "\r\nsw-lab-1#";
    return "\r\nsw-lab-1(config)#";
  });
  await runConfigCmds(session, (m) => logs.push(m), [], [{ cmd: "vlan 99" }]);
  assert.ok(
    !logs.some((l) => l.includes("CLI error")),
    "a bare \"!\" must not read as a refusal: " + logs.join(" | "),
  );
}

async function realRefusalIsReported() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "configure") return "\r\nsw-lab-1(config)#";
    if (line === "ip default-gateway 192.0.2.1") {
      return "\r\n% Invalid input (at token 1: 'default-gateway')\r\nsw-lab-1(config)#";
    }
    if (line === "end") return "\r\nsw-lab-1#";
    return "\r\nsw-lab-1(config)#";
  });
  await runConfigCmds(
    session,
    (m) => logs.push(m),
    [],
    [{ cmd: "ip default-gateway 192.0.2.1" }],
  );
  assert.ok(
    logs.some((l) => l.includes("CLI error")),
    "a real refusal must be reported: " + logs.join(" | "),
  );
}

// The interface-RANGE prompt is what a whole-faceplate trunk block runs under. A
// prompt pattern that stopped at word characters would miss it, and every
// command in the block would time out having succeeded.
async function rangeSubmodePromptIsRecognised() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "configure") return "\r\nsw-lab-1(config)#";
    if (line === "interface Ethernet1-24,49-52") {
      return "\r\nsw-lab-1(config-if-Et1-24,49-52)#";
    }
    if (line === "end") return "\r\nsw-lab-1#";
    return "\r\nsw-lab-1(config-if-Et1-24,49-52)#";
  });
  await runConfigCmds(
    session,
    (m) => logs.push(m),
    [],
    [{ cmd: "interface Ethernet1-24,49-52" }, { cmd: "switchport mode trunk" }],
  );
  assert.ok(
    !logs.some((l) => l.includes("no prompt reply")),
    "the interface-range prompt was not recognised: " + logs.join(" | "),
  );
}

async function saveSeesTheCompletionMarker() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "write memory") {
      return "\r\nCopy completed successfully.\r\nsw-lab-1#";
    }
    return undefined;
  });
  await saveConfig(session, (m) => logs.push(m));
  assert.ok(
    logs.some((l) => l.includes("written to the startup configuration")),
    "the completion marker must be seen even with the prompt on its heels: " + logs.join(" | "),
  );
}

async function saveFailsLoudly() {
  // A management configuration that does not survive a reboot is worse than a
  // refused one — and on this platform an empty startup-config is also what
  // keeps ZTP from coming back, so a switch left unsaved reboots into the retry
  // loop.
  var session = fakeDevice((line) => {
    if (line === "write memory") {
      return "\r\n% Invalid input (at token 0: 'write')\r\nsw-lab-1#";
    }
    return undefined;
  });
  await assert.rejects(() => saveConfig(session, () => {}), /save failed/);
}

async function opticsAreAcceptedAndOnlyXcvrPortsBounced() {
  var sent: string[] = [];
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    sent.push(line);
    if (line === "show interfaces status errdisabled") {
      return (
        "\r\nPort      Name          Status       Reason\r\n" +
        "Et50                    errdisabled  xcvr-unsupported\r\n" +
        "Et51                    errdisabled  bpduguard\r\n" +
        "sw-lab-1#"
      );
    }
    if (line === "end") return "\r\nsw-lab-1#";
    return "\r\nsw-lab-1(config)#";
  });
  await acceptThirdPartyOptics(session, (m) => logs.push(m), []);

  assert.ok(sent.includes(UNSUPPORTED_TRANSCEIVER_CMDS[0]));
  assert.ok(sent.includes(UNSUPPORTED_TRANSCEIVER_CMDS[1]));
  // Only the transceiver port is bounced: a bpduguard trip is a loop somebody
  // just created, and bouncing it would hide it.
  assert.ok(sent.includes("interface Ethernet50"));
  assert.ok(
    !sent.includes("interface Ethernet51"),
    "a bpduguard port must be left alone: " + sent.join(" | "),
  );
  assert.equal(sent.filter((l) => l === "shutdown").length, 1);
}

async function opticsStepIsQuietWhenNothingIsShut() {
  var sent: string[] = [];
  var session = fakeDevice((line) => {
    sent.push(line);
    if (line === "show interfaces status errdisabled") {
      return "\r\nPort      Name          Status       Reason\r\nsw-lab-1#";
    }
    if (line === "end") return "\r\nsw-lab-1#";
    return "\r\nsw-lab-1(config)#";
  });
  await acceptThirdPartyOptics(session, () => {}, []);
  assert.ok(
    !sent.includes("shutdown"),
    "nothing to recover: no port may be bounced — " + sent.join(" | "),
  );
}

async function pagerIsFed() {
  // Web Serial cannot claim a tall terminal, so output that fits a PuTTY window
  // may still paginate here and swallow every later command.
  var fed = 0;
  var session = fakeDevice((line) => {
    if (line === "show interfaces status") {
      return "Port  Name  Status\r\nEt1  connected\r\n --More-- ";
    }
    if (line === " ") {
      fed++;
      return "Et2  notconnect\r\nsw-lab-1#";
    }
    return undefined;
  });
  var out = await collectShow(session, "show interfaces status", 6000);
  assert.ok(fed > 0, "the pager was never fed");
  assert.ok(out.includes("Et2"), "the second page was never collected");
}

// ---------------------------------------------------------------------------
// Regressions from the FIRST LAB RUN, 2026-08-06. Every fixture below is the
// console text that switch actually produced.
// ---------------------------------------------------------------------------

// The console announces ZTP every thirty seconds, and the first run never used
// it: detection only ran AFTER a privileged login, so a failed login reported
// "could not re-open a privileged session" while the answer scrolled past.
function ztpVisibleBeforeAnyLogin() {
  var observed =
    "Aug  6 15:44:44 localhost ZeroTouch: %ZTP-5-DHCP_QUERY_FAIL: Failed to get a " +
    "valid DHCP response\r\n" +
    "Aug  6 15:44:44 localhost ZeroTouch: %ZTP-5-RETRY: Retrying Zero Touch " +
    "Provisioning from the beginning (attempt 129)\r\n" +
    "Login incorrect\r\n\r\nlogin: \r\nPassword:";
  assert.ok(consoleLooksLikeZTP(observed));
  assert.ok(!consoleLooksLikeZTP("sw-lab-1 login: \r\nPassword:"));

  // On such a switch "admin" has NO password, so the credential-free candidate
  // must be tried first — a candidate carrying a password is answered with
  // "Login incorrect" and leaves the console on the second prompt of a
  // two-stage login for the next candidate to trip over.
  var logins = [
    { user: "admin", pass: "wizard-pw", enable: "wizard-pw", label: "wizard" },
    { user: "admin", pass: "instance-pw", enable: "instance-pw", label: "instance" },
    { user: "admin", pass: "", enable: "", label: "factory" },
  ];
  assert.equal(orderLoginsForZTP(logins, true)[0].label, "factory");
  // Nothing is REMOVED: a switch printing ZTP lines while still holding a
  // password (a half-finished earlier run) must stay reachable.
  assert.equal(orderLoginsForZTP(logins, true).length, 3);
  assert.equal(orderLoginsForZTP(logins, false)[0].label, "wizard");
}

// THE BUG THAT COST THE FIRST RUN. EOS logs in in two stages; the console was
// sitting on "Password:" with no username of ours behind it. The old code
// answered it, got "Login incorrect", then waited on a pattern list that did
// NOT include "login:" — twenty seconds of timeout, twice, on a switch that
// was answering perfectly.
async function staleSecondStagePromptIsClearedNotAnswered() {
  var sent: string[] = [];
  var logs: string[] = [];
  var stage = "password";
  var session = fakeDevice((line) => {
    sent.push(line);
    if (stage === "password") {
      // Whatever is posted here is submitted against a username nobody entered.
      // A CR submits an EMPTY password, which is the only harmless answer — and
      // on a switch out of ZTP it is also the correct one.
      stage = "login";
      return "\r\nLogin incorrect\r\n\r\nlocalhost login: ";
    }
    if (stage === "login" && line === "admin") {
      stage = "done";
      return "\r\nlocalhost>";
    }
    if (line === "enable") return "\r\nlocalhost#";
    return undefined;
  });
  // Seed the console in the state the lab switch was actually in: a two-stage
  // login left on its SECOND prompt, with ZTP retry lines above it.
  session.append("%ZTP-5-RETRY: Retrying Zero Touch Provisioning\r\nlocalhost login: \r\nPassword:");

  await ensurePrivileged(session, "admin", "wizard-pw", "wizard-pw", (m) => logs.push(m), 25000);

  // THE REGRESSION: the old code posted the operator's password into a prompt it
  // had never caused, got "Login incorrect", and then waited on a pattern list
  // that did not include "login:" — twenty seconds of nothing, twice.
  assert.ok(
    !sent.includes("wizard-pw"),
    "the wizard password was posted into a prompt this attempt did not cause: " +
      sent.join(" | "),
  );
  assert.ok(sent.includes("admin"), "the user name was never sent: " + sent.join(" | "));
  assert.ok(
    logs.some((l) => l.includes("privileged")),
    "the session must still reach \"#\" from that state: " + logs.join(" | "),
  );
}

// "Login incorrect" must end THIS candidate immediately so the caller reaches
// the next one, instead of sitting out the expect budget on a switch that has
// already said no.
async function loginIncorrectFailsFastInsteadOfTimingOut() {
  var session = fakeDevice(() => "\r\nLogin incorrect\r\n\r\nlocalhost login: ");
  session.append("\r\nLogin incorrect\r\n\r\nlocalhost login: ");
  var started = Date.now();
  await assert.rejects(
    () => ensurePrivileged(session, "admin", "wrong", "wrong", () => {}, 60000),
    /Login incorrect/,
  );
  // The old code burned a 20 s expect per attempt. The point of the fix is that
  // this returns in a fraction of that.
  assert.ok(
    Date.now() - started < 12000,
    "a refused credential must not cost the whole expect budget",
  );
}

// OBSERVED: the whole push was accepted, then "write memory" produced nothing
// for 20 s and the run threw — leaving the configuration in the RUNNING config
// only, one power cut away from being lost.
// MEASURED 2026-08-06: 74 s from "write memory" to "Copy completed
// successfully.", with a completely silent console in between. The save is
// SLOW on this platform, not stuck — so the command must not be re-sent while it
// runs, or duplicate saves get queued onto a device that is already busy.
async function saveWaitsOutASlowWriteWithoutResendingIt() {
  var logs: string[] = [];
  var writes = 0;
  var session = fakeDevice((line) => {
    if (line === "write memory") {
      writes++;
      // Answer late, the way the device does — after the caller has had time to
      // decide the console is silent.
      setTimeout(() => session.append("\r\nCopy completed successfully.\r\nlocalhost#"), 60);
      return undefined;
    }
    return undefined;
  });
  await saveConfig(session, (m) => logs.push(m));

  assert.equal(
    writes,
    1,
    "\"write memory\" was sent " + writes + " times: re-sending it while the first " +
      "is still running queues duplicate saves onto a busy device",
  );
  assert.ok(
    logs.some((l) => l.includes("written to the startup configuration") && l.includes("took")),
    "the completion must be reported WITH its duration — that number is what tells " +
      "the next person whether this box was slow or the platform is: " + logs.join(" | "),
  );
}

// When it does go quiet past the window, the nudge is a bare CR — never another
// save — and the run log must say what arrived meanwhile, because that is the
// only evidence that will separate "busy" from "waiting on a prompt we do not
// recognise" on the next run.
async function saveNudgesWithACarriageReturnOnly() {
  var logs: string[] = [];
  var lines: string[] = [];
  var session: SerialSession;
  session = createSerialSession({
    write: async (bytes) => {
      var raw = new TextDecoder().decode(bytes);
      lines.push(raw);
      if (raw === "write memory\r") {
        // Silent well past the (injected, short) quiet window, then finish.
        setTimeout(() => session.append("\r\nCopy completed successfully.\r\nlocalhost#"), 900);
      }
    },
  });
  await saveConfig(session, (m) => logs.push(m), [], { quietWindowMs: 300 });

  assert.equal(
    lines.filter((l) => l === "write memory\r").length,
    1,
    "the save command was re-sent: " + JSON.stringify(lines),
  );
  assert.ok(lines.includes("\r"), "no bare CR nudge was sent: " + JSON.stringify(lines));
  assert.ok(
    logs.some((l) => l.includes("NOT another") && l.includes("Received meanwhile")),
    "the silence must be reported with what arrived during it: " + logs.join(" | "),
  );
}

async function saveSaysWhatIsAtStakeWhenItGivesUp() {
  var session = fakeDevice(() => undefined);
  await assert.rejects(
    () => saveConfig(session, () => {}),
    // The message must name the consequence, not just the symptom: an operator
    // reading it has to know this switch must not be power-cycled yet.
    /RUNNING CONFIG ONLY/,
  );
}

// --- the fan-speed override, captured 2026-08-06 ---

const FAN_BANNER =
  "\r\n====================================================================\r\n" +
  "WARNING: Overriding the system fan speed is unsupported and should only \r\n" +
  "be done under the direction of an Arista Networks engineer.\r\n" +
  "You can risk damaging hardware by setting the fan speed too low\r\n" +
  "and doing so without direction from Arista Networks can be grounds\r\n" +
  "for voiding your warranty.\r\n" +
  "To set the fan speed back to automatic mode, use the\r\n" +
  "'environment fan-speed auto' command\r\n" +
  "====================================================================\r\n" +
  "localhost(config)#";

// The banner contains the word "unsupported" and the command SUCCEEDED. Every
// refusal form in CLI_ERROR is anchored on a leading "%" or "!", which is
// what keeps this from reading as a failure — this test is what proves it on
// purpose rather than by luck.
async function fanSpeedBannerIsNotAFailure() {
  var logs: string[] = [];
  var session = fakeDevice((line) => {
    if (line === "configure") return "\r\nlocalhost(config)#";
    if (line === FAN_SPEED_OVERRIDE_CMD) return FAN_BANNER;
    if (line === "end") return "\r\nlocalhost#";
    return "\r\nlocalhost(config)#";
  });
  await runConfigCmds(session, (m) => logs.push(m), [], [{ cmd: FAN_SPEED_OVERRIDE_CMD }]);
  assert.ok(
    !logs.some((l) => l.includes("CLI error")),
    "the warranty banner must not read as a refusal: " + logs.join(" | "),
  );
  // Seeing the banner is the ONLY confirmation the override took — EOS prints
  // nothing else — so it has to reach the run log.
  assert.ok(
    logs.some((l) => l.includes("fan-speed warning") && l.includes(FAN_SPEED_AUTO_CMD)),
    "the banner must be surfaced with its undo: " + logs.join(" | "),
  );
}

function fanSpeedIsInThePush() {
  var lines = buildPushCmds({
    mgmtVlan: 99,
    ip: "192.0.2.5",
    prefixLen: 16,
    trunkPorts: ["Ethernet1"],
    user: "admin",
    password: "pw",
  }).map((c) => c.cmd);
  var fan = lines.indexOf(FAN_SPEED_OVERRIDE_CMD);
  assert.ok(fan >= 0, "the fan-speed override is missing from the push");
  // With the other "make this hardware usable in our racks" line, and BEFORE the
  // VLAN work — its four-line banner has no business landing in the middle of
  // the block an operator will actually read.
  assert.ok(fan < lines.indexOf("lldp run"));
  assert.ok(fan > lines.indexOf(UNSUPPORTED_TRANSCEIVER_CMDS[0]));
}

async function main() {
  maskConversions();
  ztpDetection();
  ztpDetectionSaysWhichBranchAnsweredIt();
  ztpVisibleBeforeAnyLogin();
  fanSpeedIsInThePush();
  portNames();
  faceplateParsing();
  errDisabledParsing();
  rangeGrouping();
  allowedList();
  vlanMerging();
  hostnameGoesThroughTheSharedNormalizer();
  pushOrder();
  await syslogIsNotAFailure();
  await configSeparatorIsNotAFailure();
  await realRefusalIsReported();
  await rangeSubmodePromptIsRecognised();
  await saveSeesTheCompletionMarker();
  await saveFailsLoudly();
  await opticsAreAcceptedAndOnlyXcvrPortsBounced();
  await opticsStepIsQuietWhenNothingIsShut();
  await pagerIsFed();
  await staleSecondStagePromptIsClearedNotAnswered();
  await loginIncorrectFailsFastInsteadOfTimingOut();
  await saveWaitsOutASlowWriteWithoutResendingIt();
  await saveNudgesWithACarriageReturnOnly();
  await saveSaysWhatIsAtStakeWhenItGivesUp();
  await fanSpeedBannerIsNotAFailure();
  await detectionLogsItsBranchAndTheRawAnswer();
  await detectionLogsTheFallbackWhenTheCommandIsUseless();
  await detectionSaysWhenNeitherHalfMatched();
  await detectionAdmitsWhenItTruncates();
  console.log("aristaEosSoftReset.test.ts: ok");
}

void main();

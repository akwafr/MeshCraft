/**
 * Junos EX provisioning checks:
 *   node --experimental-strip-types src/pages/add/juniperExSoftReset.test.ts
 *
 * Fixtures are the DOCUMENTED Junos console strings — the module header names
 * the source of each and states that none of them has been captured on
 * hardware. What is pinned here is the half a wrong assumption silently ruins:
 *
 *   - `#` is CONFIGURATION mode on this platform, not privileged EXEC;
 *   - "[yes,no]" takes the WORD `yes` while "[y/n]" takes the LETTER, and
 *     the zeroize prompt's default is NO — so the Cisco "[confirm]" reflex
 *     declines the factory reset while the run reports success;
 *   - ELS (`irb`, `interface-mode`) and pre-ELS (`vlan`, `port-mode`) are not
 *     interchangeable, and VLAN 1 attaches to the factory `default` VLAN rather
 *     than declaring a second vlan-id 1;
 *   - the passwords are an interactive dialogue and must never appear in a
 *     `set` line.
 */
import assert from "node:assert/strict";
import { createSerialSession, type SerialSession } from "./serialExpect.ts";
import {
  buildReachabilityCmds,
  buildSiteVlanCmds,
  classifyJunosConsole,
  commitCheck,
  factoryAutoConfigDeletes,
  isELSModel,
  mergeSiteVlans,
  mgmtUnit,
  NEVER_EMIT,
  parseInterfacesTerse,
  parseVlanId,
  resolveMgmtTarget,
  sanitizeVlanName,
  setPlainTextPassword,
  stopZTP,
  toPrefixLen,
  tolerantDeletes,
  yesAnswerFor,
  zeroize,
} from "./juniperExSoftReset.ts";

function fakeDevice(replies: (line: string, raw: string) => string | undefined): {
  session: SerialSession;
  sent: string[];
} {
  var sent: string[] = [];
  var session: SerialSession;
  session = createSerialSession({
    write: async (bytes) => {
      var raw = new TextDecoder().decode(bytes);
      sent.push(raw);
      var line = raw.replace(/\r+$/, "");
      var out = replies(line, raw);
      if (out !== undefined) setTimeout(() => session.append(out), 5);
    },
  });
  return { session: session!, sent };
}

/* ───────────────────────── the three prompt characters ───────────────────── */

function promptsAreClassifiedByTheLastMarker() {
  // A zeroized switch, exactly as the console presents it.
  assert.equal(
    classifyJunosConsole("FreeBSD/arm (Amnesiac) (ttyu0)\r\n\r\nlogin: "),
    "login",
  );
  // The shell a factory `root` LANDS in — and the EX4300 form of the prompt,
  // which a naive /root%/ would miss.
  assert.equal(
    classifyJunosConsole("--- JUNOS 14.1X53-D12.9 built 2015-06-25 03:06:46 UTC\r\nroot@:RE:0% "),
    "shell",
  );
  assert.equal(classifyJunosConsole("root% "), "shell");
  // Operational mode.
  assert.equal(classifyJunosConsole("root> "), "cli");
  // CONFIGURATION mode. On IOS this character means privileged; here it means
  // "inside a candidate configuration", and reading it the IOS way would send
  // operational verbs into the configuration database.
  assert.equal(classifyJunosConsole("\r\n[edit]\r\nroot@sw-labo-1# "), "config");

  // THE RIGHTMOST RULE. A console that has been through a login and a `cli`
  // carries all three markers at once, and only the last one is the present.
  var whole =
    "Amnesiac (ttyu0)\r\n\r\nlogin: root\r\n" +
    "--- JUNOS 18.4R3 built 2020-01-01\r\n" +
    "root@:RE:0% cli\r\n" +
    "root> ";
  assert.equal(classifyJunosConsole(whole), "cli");

  // The loader, which the SOFT path must refuse rather than type into.
  assert.equal(
    classifyJunosConsole(
      "Loading /boot/defaults/loader.conf\r\n" +
        "Hit [Enter] to boot immediately, or space bar for command prompt.\r\n" +
        "Booting [kernel] in 9 seconds... ",
    ),
    "loader-window",
  );
  assert.equal(classifyJunosConsole("\r\nloader> "), "loader");
  assert.equal(
    classifyJunosConsole(
      "Enter full pathname of shell or 'recovery' for root password recovery or RETURN for /bin/sh: ",
    ),
    "single-user",
  );
  assert.equal(classifyJunosConsole("nothing recognisable here"), "unknown");
}

/* ─────────────────────────── the two yes/no grammars ─────────────────────── */

function theTwoQuestionGrammarsTakeDifferentAnswers() {
  // The zeroize question. Default NO, and `y` is not an answer it reads.
  assert.equal(
    yesAnswerFor("Erase all data, including configuration and log files? [yes,no] (no) "),
    "yes",
  );
  // The reboot question, in the SAME procedure, takes the letter.
  assert.equal(yesAnswerFor("Reboot the system? [y/n] "), "y");
}

/* ───────────────────────────── the ELS fork ──────────────────────────────── */

function elsIsDecidedByModelAndAdmitsIgnorance() {
  assert.equal(isELSModel("Model: ex2200-24t-4g\nJunos: 12.3R12.4"), false);
  assert.equal(isELSModel("Model: ex4200-48t\nJunos: 15.1R7"), false);
  assert.equal(isELSModel("Model: ex4550-32f"), false);
  assert.equal(isELSModel("Model: ex2300-c-12p\nJunos: 18.4R3-S9"), true);
  assert.equal(isELSModel("Model: ex3400-48p"), true);
  assert.equal(isELSModel("Model: ex4300-48t"), true);
  // An unknown model must NOT be guessed silently: null is what makes the
  // runner try both dialects behind a commit check that applies nothing.
  assert.equal(isELSModel("Model: ex9999-weird"), null);
  assert.equal(isELSModel(""), null);
}

function reachabilityPushIsTheRightDialect() {
  var elsCmds = buildReachabilityCmds({
    els: true,
    target: { kind: "inband", vlanId: 99, vlanName: "LAN99_Management" },
    ip: "192.0.2.21",
    prefixLen: 24,
    gateway: "192.0.2.1",
    hostname: "sw-labo-ex",
    trunkPorts: ["ge-0/0/0", "ge-0/0/1"],
  }).map((c) => c.cmd);

  // ZTP OFF FIRST — Juniper's own step 1, and the statement whose deletion ends
  // the auto-image upgrade.
  assert.equal(elsCmds[0], "delete chassis auto-image-upgrade");
  assert.ok(elsCmds.includes("set vlans LAN99_Management vlan-id 99"));
  assert.ok(
    elsCmds.includes("set interfaces irb unit 99 family inet address 192.0.2.21/24"),
    `ELS must use irb: ${elsCmds.join(" | ")}`,
  );
  assert.ok(elsCmds.includes("set vlans LAN99_Management l3-interface irb.99"));
  assert.ok(
    elsCmds.some((c) => c.includes("interface-mode trunk")),
    "ELS trunks are \"interface-mode\", not \"port-mode\"",
  );
  assert.ok(
    !elsCmds.some((c) => c.includes("port-mode")),
    "an ELS push must not contain the pre-ELS port-mode form",
  );
  // There is NO "default-gateway" statement on Junos; the default route is a
  // real static route.
  assert.ok(elsCmds.includes("set routing-options static route 0.0.0.0/0 next-hop 192.0.2.1"));
  assert.ok(!elsCmds.some((c) => c.includes("default-gateway")));
  // SSH on, and root SSH explicitly denied — the default is deny-password, so
  // writing the denial is what makes the rule true across the whole range.
  assert.ok(elsCmds.includes("set system services ssh"));
  assert.ok(elsCmds.includes("set system services ssh root-login deny"));
  assert.ok(elsCmds.includes("set system host-name sw-labo-ex"));
  // THE PASSWORDS ARE NOT HERE. They are an interactive dialogue, which is what
  // keeps the secret out of the console transcript a failed run ships.
  assert.ok(
    !elsCmds.some((c) => /plain-text-password|encrypted-password/.test(c)),
    "no password may appear in a \"set\" line",
  );

  var preCmds = buildReachabilityCmds({
    els: false,
    target: { kind: "inband", vlanId: 99, vlanName: "LAN99_Management" },
    ip: "192.0.2.21",
    prefixLen: 24,
    trunkPorts: ["ge-0/0/0"],
  }).map((c) => c.cmd);
  assert.ok(preCmds.includes("set interfaces vlan unit 99 family inet address 192.0.2.21/24"));
  assert.ok(preCmds.includes("set vlans LAN99_Management l3-interface vlan.99"));
  assert.ok(preCmds.some((c) => c.includes("port-mode trunk")));
  assert.ok(!preCmds.some((c) => c.includes("interface-mode")));
}

function vlanOneAttachesToTheFactoryDefaultVlan() {
  var cmds = buildReachabilityCmds({
    els: true,
    target: { kind: "inband", vlanId: 1, vlanName: "LAN1_Management" },
    ip: "192.168.1.10",
    prefixLen: 24,
    trunkPorts: ["ge-0/0/0"],
  }).map((c) => c.cmd);
  // Unit 0, by universal convention, and NO second VLAN with vlan-id 1 — the
  // platform refuses that, and a factory switch already carries `default`.
  assert.equal(mgmtUnit(1), 0);
  assert.ok(cmds.includes("set interfaces irb unit 0 family inet address 192.168.1.10/24"));
  assert.ok(cmds.includes("set vlans default l3-interface irb.0"));
  assert.ok(
    !cmds.some((c) => /vlan-id 1\b/.test(c)),
    `VLAN 1 must not be declared beside the factory "default": ${cmds.join(" | ")}`,
  );
  // IN-BAND LEAVES THE MANAGEMENT PORT UNPLUGGED ON PURPOSE, so the alarm it
  // raises for having no carrier is a standing false positive on every device
  // provisioned this way. Silencing it is part of the shape, not a tweak.
  assert.ok(
    cmds.includes("set chassis alarm management-ethernet link-down ignore"),
    `in-band must silence the empty management port's alarm: ${cmds.join(" | ")}`,
  );
}

function outOfBandPushTouchesNoFaceplatePort() {
  var cmds = buildReachabilityCmds({
    els: true,
    target: { kind: "oob", iface: "vme" },
    ip: "10.0.9.5",
    prefixLen: 24,
    trunkPorts: ["ge-0/0/0", "ge-0/0/1"],
  }).map((c) => c.cmd);
  assert.ok(cmds.includes("set interfaces vme unit 0 family inet address 10.0.9.5/24"));
  // The out-of-band shape must not reconfigure a switching fabric nobody asked
  // it to touch — and `vme`/`me0` are not VLAN interfaces, so there is nothing
  // for a trunk to carry.
  assert.ok(
    !cmds.some((c) => /ethernet-switching|vlan members|l3-interface/.test(c)),
    `the out-of-band push must leave the faceplate alone: ${cmds.join(" | ")}`,
  );
  // AND IT MUST NOT SILENCE THE MANAGEMENT PORT'S ALARM. Here that port is
  // the cable the whole run depends on: ignoring its link-down would hide the
  // one failure that matters most in this shape. The statement belongs to
  // in-band only, and this is the assertion that keeps it there.
  assert.ok(
    !cmds.some((c) => /alarm management-ethernet/.test(c)),
    `out-of-band must keep its management port alarm: ${cmds.join(" | ")}`,
  );
  // And T2 adds nothing in that shape either.
  assert.deepEqual(
    buildSiteVlanCmds({
      els: true,
      target: { kind: "oob", iface: "vme" },
      siteVlans: [{ id: 40, name: "LAN40" }],
      trunkPorts: ["ge-0/0/0"],
    }),
    [],
  );
}

function siteVlanTransactionSkipsTheManagementVlan() {
  var cmds = buildSiteVlanCmds({
    els: true,
    target: { kind: "inband", vlanId: 99, vlanName: "LAN99_Management" },
    siteVlans: [
      { id: 30, name: "LAN30" },
      { id: 40, name: "LAN40" },
      { id: 99, name: "LAN99_Management" },
    ],
    trunkPorts: ["ge-0/0/0"],
  }).map((c) => c.cmd);
  // T1 already declared the management VLAN and put it on the trunks.
  assert.ok(!cmds.some((c) => c.includes("LAN99_Management")));
  assert.ok(cmds.includes("set vlans LAN30 vlan-id 30"));
  assert.ok(cmds.includes("set vlans LAN40 vlan-id 40"));
  // ONE statement per VLAN, never a bracketed list: a single refused name inside
  // brackets fails the whole line.
  assert.ok(!cmds.some((c) => c.includes("[")));
  assert.ok(
    cmds.includes(
      "set interfaces ge-0/0/0 unit 0 family ethernet-switching vlan members LAN30",
    ),
  );
}

/* ────────────────────── the management-interface question ────────────────── */

function mgmtTargetDistinguishesTheTwoCables() {
  assert.deepEqual(resolveMgmtTarget("me0"), { kind: "oob", iface: "me0" });
  assert.deepEqual(resolveMgmtTarget("vme"), { kind: "oob", iface: "vme" });
  assert.deepEqual(resolveMgmtTarget("VME.0"), { kind: "oob", iface: "vme" });
  assert.deepEqual(resolveMgmtTarget("vlan 99"), {
    kind: "inband",
    vlanId: 99,
    vlanName: "LAN99_Management",
  });
  assert.deepEqual(resolveMgmtTarget("vlan 99", "Management étage 1"), {
    kind: "inband",
    vlanId: 99,
    vlanName: "Management_tage_1",
  });
  // The FS-shaped free-text values other platforms accept mean nothing here,
  // and falling back to a VLAN the operator never named would configure the
  // wrong interface silently.
  assert.throws(() => resolveMgmtTarget("Craft"), /not usable on a Junos EX/);
  assert.throws(() => resolveMgmtTarget(""), /not usable on a Junos EX/);
  assert.equal(parseVlanId("vlan 4095"), null);
  assert.equal(parseVlanId("vlan.99"), 99);
}

function vlanNamesAreMadeAcceptable() {
  assert.equal(sanitizeVlanName("LAN30"), "LAN30");
  assert.equal(sanitizeVlanName("30_data"), "v30_data");
  assert.equal(sanitizeVlanName("Management étage 1"), "Management_tage_1");
  assert.equal(sanitizeVlanName("!!!"), "Management");
}

function masksBecomePrefixes() {
  assert.equal(toPrefixLen("255.255.255.0"), 24);
  assert.equal(toPrefixLen("255.255.0.0"), 16);
  assert.equal(toPrefixLen("/25"), 25);
  assert.equal(toPrefixLen("29"), 29);
  assert.equal(toPrefixLen("nonsense"), 24);
}

function faceplateReadingExcludesWhatIsNotAJack() {
  var terse =
    "Interface               Admin Link Proto    Local                 Remote\r\n" +
    "ge-0/0/0                up    up\r\n" +
    "ge-0/0/0.0              up    up   eth-switch\r\n" +
    "ge-0/0/1                up    down\r\n" +
    "xe-0/1/0                up    up\r\n" +
    "xe-0/1/0.0              up    up   eth-switch\r\n" +
    "me0                     up    up\r\n" +
    "me0.0                   up    up   inet     10.0.0.5/24\r\n" +
    "vme                     up    down\r\n" +
    "irb                     up    up\r\n" +
    "irb.99                  up    up   inet     192.0.2.21/24\r\n" +
    "lo0                     up    up\r\n" +
    "lo0.16385               up    up   inet\r\n" +
    "vcp-255/0/48            up    down\r\n" +
    "bme0                    up    up\r\n";
  var ifaces = parseInterfacesTerse(terse);
  assert.deepEqual(ifaces.ports, ["ge-0/0/0", "ge-0/0/1", "xe-0/1/0"]);
  assert.equal(ifaces.hasMe0, true);
  assert.equal(ifaces.hasVme, true);
  assert.equal(ifaces.hasIrb, true);
  assert.equal(ifaces.hasVlanIface, false);

  // A device that only ever printed the LOGICAL unit must still be read as
  // having the port — "vme is often listed without a vme.0 until configured"
  // cuts both ways.
  var onlyUnits = "vme.0                   up    up   inet     10.0.0.9/24\r\n";
  assert.equal(parseInterfacesTerse(onlyUnits).hasVme, true);
  assert.deepEqual(parseInterfacesTerse(onlyUnits).ports, []);
}

function factoryAutoConfigIsRemovedBeforeAStaticAddress() {
  assert.deepEqual(factoryAutoConfigDeletes({ kind: "oob", iface: "vme" }, true), [
    "delete interfaces vme unit 0 family inet dhcp",
  ]);
  var inband = factoryAutoConfigDeletes(
    { kind: "inband", vlanId: 99, vlanName: "LAN99_Management" },
    true,
  );
  assert.ok(inband.includes("delete interfaces irb unit 99 family inet dhcp"));
  // Both out-of-band names too: the factory configuration arms them, and a
  // `vme.0` left DHCPing is a second management address meshcraft knows nothing
  // about.
  assert.ok(inband.includes("delete interfaces vme unit 0 family inet dhcp"));
  assert.ok(inband.includes("delete interfaces me0 unit 0 family inet dhcp"));
  // pre-ELS names the L3 interface differently.
  assert.ok(
    factoryAutoConfigDeletes(
      { kind: "inband", vlanId: 99, vlanName: "LAN99_Management" },
      false,
    ).includes("delete interfaces vlan unit 99 family inet dhcp"),
  );
}

function siteVlansAreMergedWithoutVlanOne() {
  assert.deepEqual(
    mergeSiteVlans([{ id: 1, name: "default" }, { id: 30 }, { id: 30 }], 99, "Mgmt"),
    [
      { id: 30, name: "LAN30" },
      { id: 99, name: "Mgmt" },
    ],
  );
  // Management VLAN 1 is never added as a declaration — see
  // vlanOneAttachesToTheFactoryDefaultVlan.
  assert.deepEqual(mergeSiteVlans([{ id: 40, name: "LAN40" }], 1), [
    { id: 40, name: "LAN40" },
  ]);
}

function theConsoleKillersAreRecorded() {
  // Not a behaviour test — a tripwire. These four statements end the only way
  // into a switch being provisioned from its console, and each is reachable
  // from a well-meant "make the console nicer" change.
  assert.ok(NEVER_EMIT.includes("set system ports console disable"));
  assert.ok(NEVER_EMIT.includes("set system ports console insecure"));
  assert.ok(NEVER_EMIT.includes("set system ports console log-out-on-disconnect"));
  assert.ok(NEVER_EMIT.includes("set baudrate=115200"));
}

/* ──────────────────────────── the dialogues ──────────────────────────────── */

async function zeroizeAnswersTheWordYes() {
  var dev = fakeDevice((line) => {
    if (line === "request system zeroize") {
      return (
        "\r\nwarning: System will be rebooted and may not boot without configuration\r\n" +
        "Erase all data, including configuration and log files? [yes,no] (no) "
      );
    }
    return undefined;
  });
  var logs: string[] = [];
  await zeroize(dev.session, (m) => logs.push(m));
  var typed = dev.sent.map((s) => s.replace(/\r+$/, ""));
  assert.ok(typed.includes("yes"), `must answer with the WORD yes: ${typed.join(" | ")}`);
  assert.ok(
    !typed.includes("y"),
    "a bare \"y\" at a [yes,no] prompt is rejected by the platform",
  );
  assert.ok(
    !typed.some((t, i) => i > 0 && t === ""),
    "a bare CR would DECLINE the reset (the prompt's default is no)",
  );
  assert.ok(
    logs.some((l) => /IRREVERSIBLE/.test(l)),
    "the irreversible step must be announced before it is sent",
  );
}

async function zeroizeRefusalRejects() {
  var dev = fakeDevice((line) => {
    if (line === "request system zeroize") {
      return "\r\nerror: permission denied\r\nroot> ";
    }
    return undefined;
  });
  await assert.rejects(
    () => zeroize(dev.session, () => {}),
    /was refused/,
    "a refused zeroize must reject rather than carry on against an un-wiped switch",
  );
}

async function passwordDialogueTypesItTwiceAndNeverInASetLine() {
  var stage = 0;
  var dev = fakeDevice((line) => {
    if (line === "set system root-authentication plain-text-password") {
      stage = 1;
      return "\r\nNew password:";
    }
    if (stage === 1 && line === "s3cr3t-pass") {
      stage = 2;
      return "\r\nRetype new password:";
    }
    if (stage === 2 && line === "s3cr3t-pass") {
      stage = 3;
      return "\r\n\r\n[edit]\r\nroot@sw# ";
    }
    return undefined;
  });
  await setPlainTextPassword(
    dev.session,
    () => {},
    "set system root-authentication plain-text-password",
    "root password",
    "s3cr3t-pass",
  );
  var typed = dev.sent.map((s) => s.replace(/\r+$/, ""));
  assert.equal(
    typed.filter((t) => t === "s3cr3t-pass").length,
    2,
    `the secret must be typed twice, at the two prompts: ${typed.join(" | ")}`,
  );
  assert.ok(
    !typed.some((t) => t.startsWith("set ") && t.includes("s3cr3t-pass")),
    "the secret must never appear in a \"set\" line",
  );
}

async function refusedPasswordRejects() {
  var dev = fakeDevice((line) => {
    if (line.startsWith("set system login user")) return "\r\nNew password:";
    if (line === "short") {
      return "\r\nerror: minimum length is 6 characters\r\n\r\n[edit]\r\nroot@sw# ";
    }
    return undefined;
  });
  await assert.rejects(
    () =>
      setPlainTextPassword(
        dev.session,
        () => {},
        "set system login user meshadmin authentication plain-text-password",
        "meshcraft account",
        "short",
      ),
    /was refused/,
    "a password the platform refuses must reject — the account would not exist",
  );
}

async function commitCheckReadsItsVerdict() {
  var ok = fakeDevice((line) => {
    if (line === "commit check") {
      return "\r\nconfiguration check succeeds\r\n\r\n[edit]\r\nroot@sw# ";
    }
    return undefined;
  });
  assert.equal((await commitCheck(ok.session, () => {})).ok, true);

  var bad = fakeDevice((line) => {
    if (line === "commit check") {
      return (
        "\r\n[edit interfaces]\r\n  'irb'\r\n" +
        "    interface-mode is not a valid statement\r\n" +
        "error: configuration check-out failed\r\n\r\n[edit]\r\nroot@sw# "
      );
    }
    return undefined;
  });
  var verdict = await commitCheck(bad.session, () => {});
  assert.equal(verdict.ok, false, "a refused dialect must be caught by the DRY RUN");

  var noRoot = fakeDevice((line) => {
    if (line === "commit check") {
      return (
        "\r\n[edit system]\r\n  Missing mandatory statement: 'root-authentication'\r\n" +
        "error: commit failed: (missing statements)\r\n\r\n[edit]\r\nroot@sw# "
      );
    }
    return undefined;
  });
  var rootVerdict = await commitCheck(noRoot.session, () => {});
  assert.equal(rootVerdict.ok, false);
  assert.match(
    rootVerdict.detail,
    /root password/,
    "the missing-root-authentication refusal must be explained, not quoted raw",
  );
}

async function absentStatementsAreToleratedByDeletes() {
  var dev = fakeDevice((line) => {
    if (line === "delete chassis auto-image-upgrade") {
      return "\r\nwarning: statement not found\r\n\r\n[edit]\r\nroot@sw# ";
    }
    if (line === "delete interfaces vme unit 0 family inet dhcp") {
      return "\r\n\r\n[edit]\r\nroot@sw# ";
    }
    return undefined;
  });
  var logs: string[] = [];
  var removed = await tolerantDeletes(
    dev.session,
    (m) => logs.push(m),
    ["delete chassis auto-image-upgrade", "delete interfaces vme unit 0 family inet dhcp"],
  );
  assert.deepEqual(
    removed,
    ["delete interfaces vme unit 0 family inet dhcp"],
    "only the delete that actually removed something counts",
  );
  assert.ok(logs.some((l) => /statement not found/.test(l)));
}

/** A pre-ZTP EX (EX2200, EX4200, EX4550) has no auto-image-upgrade: stopZTP
 * must notice and commit NOTHING, rather than committing an empty transaction. */
async function stopZtpOnAPreZtpSwitchCommitsNothing() {
  var dev = fakeDevice((line) => {
    if (line === "delete chassis auto-image-upgrade") {
      return "\r\nerror: statement not found\r\n\r\n[edit]\r\nroot@sw# ";
    }
    if (line === "commit") return "\r\ncommit complete\r\n\r\n[edit]\r\nroot@sw# ";
    return undefined;
  });
  var logs: string[] = [];
  await stopZTP(dev.session, (m) => logs.push(m));
  var typed = dev.sent.map((s) => s.replace(/\r+$/, ""));
  assert.ok(
    !typed.includes("commit"),
    `nothing to disarm means nothing to commit: ${typed.join(" | ")}`,
  );
  assert.ok(logs.some((l) => /was not armed/.test(l)));
}

async function main() {
  promptsAreClassifiedByTheLastMarker();
  theTwoQuestionGrammarsTakeDifferentAnswers();
  elsIsDecidedByModelAndAdmitsIgnorance();
  reachabilityPushIsTheRightDialect();
  vlanOneAttachesToTheFactoryDefaultVlan();
  outOfBandPushTouchesNoFaceplatePort();
  siteVlanTransactionSkipsTheManagementVlan();
  mgmtTargetDistinguishesTheTwoCables();
  vlanNamesAreMadeAcceptable();
  masksBecomePrefixes();
  faceplateReadingExcludesWhatIsNotAJack();
  factoryAutoConfigIsRemovedBeforeAStaticAddress();
  siteVlansAreMergedWithoutVlanOne();
  theConsoleKillersAreRecorded();

  await zeroizeAnswersTheWordYes();
  await zeroizeRefusalRejects();
  await passwordDialogueTypesItTwiceAndNeverInASetLine();
  await refusedPasswordRejects();
  await commitCheckReadsItsVerdict();
  await absentStatementsAreToleratedByDeletes();
  await stopZtpOnAPreZtpSwitchCommitsNothing();

  console.log("juniperExSoftReset.test.ts: ok");
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});

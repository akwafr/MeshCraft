package eos

// The PERSISTENCE leg of [Client.ApplySNMP], exercised on real hardware — the
// one thing live_snmpv3_test.go deliberately did not touch.
//
// That harness drove [Client.applySNMPv3] directly, precisely so that
// startup-config stayed untouched and a reload remained a clean escape hatch.
// The consequence is that the LAST statement of the whole path — "the identity
// was persisted" — was the only one nobody had watched a switch make. This file
// watches it: it calls the exported [Client.ApplySNMP], which ends in
// [Client.save], and then reads "show startup-config" to see whether the v3
// user is actually in there.
//
// # There is no escape hatch here, so the safety is in the ORDER
//
// A probe that writes startup-config cannot be undone by a reload. So:
//
//  1. the FULL startup configuration is read and kept before anything is sent,
//     and the run REFUSES to start unless startup and running already agree —
//     otherwise `write memory` would persist somebody else's pending change as a
//     side effect of this probe, which is the worst thing this file could do;
//  2. the probe refuses to start at all if the switch already carries any
//     "snmp-server user/group" line: this must never edit an identity that
//     belongs to somebody;
//  3. the removal AND the second save are registered as a cleanup BEFORE the
//     first write, so any failure between the two still ends with the device
//     back where it was found;
//  4. the last act compares the full startup configuration, line for line,
//     against the baseline — the two header comments excepted, see
//     [startupBody].
//
// The v2c half is left strictly alone: [SNMPIdentity.Community] is EMPTY in the
// intent below, so [Client.applySNMPCommunity] is never reached. core01 carries a
// real community and a real trap host for live monitoring, and this probe must
// not so much as re-issue them.
//
// # Running it
//
//	EOS_LIVE_HOST=192.0.2.5 EOS_LIVE_USER=… EOS_LIVE_PASS=… EOS_LIVE_SNMPV3_SAVE=1 \
//	EOS_LIVE_SOCKS=127.0.0.1:1080 EOS_LIVE_SOCKS_USER=… EOS_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/arista/eos -run TestLiveSNMPv3Save -v
//
// A DIFFERENT variable from live_snmpv3_test.go's on purpose: that probe is
// re-runnable at will, this one writes flash, and one variable for both would
// make the dangerous one a side effect of asking for the safe one.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

// The probe's identity — the "c" generation, so a leftover from the previous
// harness (mcprobev3b/meshprobeb) is never mistaken for this one's.
const (
	saveProbeUser  = "mcprobev3c"
	saveProbeGroup = "meshprobec"
)

// Throwaway passphrases, distinct within the pair because [checkV3Secrets]
// refuses a pair that is not.
const (
	saveProbeAuth = "MeshProbeAuthCharlie3"
	saveProbePriv = "MeshProbePrivCharlie3"
)

// showStartupCmd is the read this whole file is about: the only one that says
// what survives a reboot.
const showStartupCmd = "show startup-config"

// startupBody strips what a configuration read says ABOUT ITSELF, so that two
// captures can be compared as configurations.
//
// EOS prefixes both reads with "! Command: …" and prefixes the startup one with
// "! Startup-config last modified at <date> by <user>". That timestamp is
// rewritten by every save INCLUDING one that changes nothing, so a probe that
// saves twice can never leave the raw text byte-identical, and demanding it would
// report a false failure on a device that is in fact exactly as it was found.
// Both lines are comments; neither is configuration.
//
// The echoed command and the prompt that frames it go the same way: they are the
// pty's, not the device's configuration.
func startupBody(out string) []string {
	var keep []string
	var line string
	for _, line = range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t\r")
		var t = strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case strings.HasPrefix(t, "! Command:"):
			continue
		case strings.HasPrefix(t, "! Startup-config last modified at"):
			continue
		case t == showStartupCmd || t == "show running-config":
			continue
		case strings.HasSuffix(t, "#") && !strings.Contains(t, " "):
			continue // the prompt
		}
		keep = append(keep, line)
	}
	return keep
}

// liveSaveSession opens the session, or skips. Same shape as
// [liveSNMPSession] — the Cleanup-not-defer rule included, and for the same
// reason: a deferred Close would shut the shell before the removal runs.
func liveSaveSession(t *testing.T) (*Client, context.Context) {
	t.Helper()
	if os.Getenv("EOS_LIVE_SNMPV3_SAVE") != "1" {
		t.Skip("EOS_LIVE_SNMPV3_SAVE not set — live SNMPv3 persistence probe skipped")
	}
	var host = os.Getenv("EOS_LIVE_HOST")
	if host == "" {
		t.Skip("EOS_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("EOS_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("EOS_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("EOS_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 10*time.Minute)
	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("EOS_LIVE_USER"), Secret: os.Getenv("EOS_LIVE_PASS"),
		Why: "live SNMPv3 persistence probe",
	}})
	if err != nil {
		cancel()
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	t.Cleanup(func() { c.Close(); cancel() })
	return c, ctx
}

// sayLong is [say] with the long budget a whole configuration needs.
func sayLong(t *testing.T, c *Client, ctx context.Context, cmd string) string {
	t.Helper()
	var out, err = c.cli.run(ctx, cmd, showRunTimeout)
	t.Logf("===== %s =====\n%s", cmd, out)
	if err != nil {
		t.Logf("(\"%s\" returned: %v)", cmd, err)
	}
	return out
}

// TestLiveSNMPv3Save is the persistence leg, in five acts.
func TestLiveSNMPv3Save(t *testing.T) {
	var c, ctx = liveSaveSession(t)

	// --- 0) BEFORE. Nothing is sent until every one of these reads agrees.
	t.Log("--- 0) BEFORE ---")
	var beforeStartRaw = sayLong(t, c, ctx, showStartupCmd)
	var beforeRunRaw = sayLong(t, c, ctx, "show running-config")
	var beforeSNMP = say(t, c, ctx, "show running-config | section snmp")
	var beforeStartSNMP = say(t, c, ctx, showStartupCmd+" | section snmp")
	var beforeUsers = say(t, c, ctx, showSNMPUserCmd)
	say(t, c, ctx, showSNMPGroupCmd)

	var baseline = startupBody(beforeStartRaw)
	if len(baseline) == 0 {
		t.Fatalf("\"%s\" came back empty — refusing to write flash with no baseline",
			showStartupCmd)
	}

	// GUARD 1: startup and running must already agree. `write memory` persists
	// the WHOLE running configuration, so a probe that saves on a switch carrying
	// an unsaved change of somebody else's would commit it — a side effect far
	// larger than anything this probe intends.
	var runBody = startupBody(beforeRunRaw)
	if strings.Join(baseline, "\n") != strings.Join(runBody, "\n") {
		t.Fatalf("the running configuration DIFFERS from the startup configuration on this "+
			"device: a save would persist somebody else's pending change. Refusing to run.\n"+
			"startup:\n%s\nrunning:\n%s",
			strings.Join(baseline, "\n"), strings.Join(runBody, "\n"))
	}
	t.Log("GUARD: startup-config and running-config agree — a save persists nothing but " +
		"what this probe writes")

	// GUARD 2: no v3 identity of anybody else's, in either configuration.
	var l string
	for _, l = range append(snmpLines(beforeSNMP), snmpLines(beforeStartSNMP)...) {
		if strings.HasPrefix(l, "snmp-server user ") || strings.HasPrefix(l, "snmp-server group ") {
			t.Fatalf("this switch already carries \"%s\" — refusing to run: this probe must "+
				"not touch an existing v3 configuration", l)
		}
	}
	var u snmpUser
	for _, u = range parseSNMPUsers(beforeUsers) {
		t.Fatalf("\"%s\" already lists user %q — refusing to run", showSNMPUserCmd, u.Name)
	}
	t.Logf("BASELINE startup-config: %d configuration lines", len(baseline))
	t.Logf("BASELINE \"%s | section snmp\":\n%s", showStartupCmd,
		strings.Join(snmpLines(beforeStartSNMP), "\n"))

	// The removal AND THE SECOND SAVE are registered before the first write. The
	// save is the half live_snmpv3_test.go never needed: there, leaving the
	// running configuration clean was enough, because startup-config was never
	// touched. Here it is, so putting the device back means saving again.
	t.Cleanup(func() {
		var ictx, icancel = context.WithTimeout(context.Background(), 5*time.Minute)
		defer icancel()
		t.Log("--- CLEANUP ---")
		t.Log(rawConfig(t, c, ictx, []string{
			"no snmp-server user " + saveProbeUser + " " + saveProbeGroup + " v3",
			"no snmp-server group " + saveProbeGroup + " v3 priv",
		}))
		var leftRun = say(t, c, ictx, "show running-config | section snmp")

		// Any "snmp-server engineID" the switch materialised for the probe and
		// did NOT take away with the user is removed here — AFTER the user, when
		// no key derived from that engine is left to invalidate.
		var stray []string
		var line string
		for _, line = range snmpLines(leftRun) {
			if strings.HasPrefix(line, "snmp-server engineID") &&
				!strings.Contains(strings.Join(baseline, "\n"), line) {
				stray = append(stray, "no "+line)
			}
		}
		if len(stray) > 0 {
			t.Logf("the probe left %d line(s) the switch did not have; removing", len(stray))
			t.Log(rawConfig(t, c, ictx, stray))
			leftRun = say(t, c, ictx, "show running-config | section snmp")
		}

		// THE SECOND SAVE, through the same shipped code the probe is testing.
		var serr = c.save(ictx)
		t.Logf("cleanup save -> %v", serr)
		if serr != nil {
			t.Errorf("THE CLEANUP SAVE FAILED (%v): the probe's user may still be in "+
				"startup-config. Look before leaving this device", serr)
		}

		say(t, c, ictx, showSNMPUserCmd)
		say(t, c, ictx, showSNMPGroupCmd)
		var afterStartRaw = sayLong(t, c, ictx, showStartupCmd)
		var after = startupBody(afterStartRaw)
		if strings.Join(after, "\n") != strings.Join(baseline, "\n") {
			t.Errorf("THE STARTUP CONFIGURATION WAS NOT LEFT AS FOUND.\n"+
				"baseline (%d lines):\n%s\nafter (%d lines):\n%s",
				len(baseline), strings.Join(baseline, "\n"), len(after), strings.Join(after, "\n"))
			return
		}
		t.Logf("STARTUP-CONFIG RESTORED: %d lines, identical to the baseline "+
			"(the two \"! Command\" / \"! Startup-config last modified\" comments excepted)",
			len(after))
	})

	// --- 1) THE WHOLE PATH, save included.
	t.Log("--- 1) ApplySNMP (v3 only — Community deliberately empty) ---")
	var want = SNMPIdentity{
		User: saveProbeUser, Group: saveProbeGroup,
		AuthAlg: "sha", PrivAlg: "aes128",
		AuthSecret: saveProbeAuth, PrivSecret: saveProbePriv,
	}
	var res, err = c.ApplySNMP(ctx, want)
	t.Logf("ApplySNMP -> err=%v result=%+v", err, res)
	if err != nil {
		t.Errorf("ApplySNMP failed: %v", err)
	}
	var st driver.Step
	for _, st = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", st.OK, st.What, st.Detail)
	}
	// The persistence leg's OWN verdict, in the connector's own words: ApplySNMP
	// does not return an error when the save fails — it puts the reason in
	// Skipped. So a green error and a failed save look alike from the return
	// value, and this is where that is checked.
	var sk string
	for _, sk = range res.Skipped {
		if strings.Contains(sk, "NOT persisted") {
			t.Errorf("THE SAVE LEG FAILED and ApplySNMP reported it in Skipped: %s", sk)
		}
	}

	// --- 2) Did it survive into startup-config? THE question of this file.
	t.Log("--- 2) what startup-config now carries ---")
	say(t, c, ctx, showSNMPUserCmd)
	say(t, c, ctx, showSNMPGroupCmd)
	var afterRunSNMP = say(t, c, ctx, "show running-config | section snmp")
	var afterStartSNMP = say(t, c, ctx, showStartupCmd+" | section snmp")

	var runLines = snmpLines(afterRunSNMP)
	var startLines = snmpLines(afterStartSNMP)
	t.Logf("running-config snmp AFTER apply:\n%s", strings.Join(runLines, "\n"))
	t.Logf("startup-config snmp AFTER apply:\n%s", strings.Join(startLines, "\n"))

	var joined = strings.Join(startLines, "\n")
	if !strings.Contains(joined, "snmp-server user "+saveProbeUser) {
		t.Errorf("THE V3 USER DID NOT SURVIVE INTO STARTUP-CONFIG: \"%s | section snmp\" "+
			"does not carry \"snmp-server user %s\". A reboot would silence the identity "+
			"ApplySNMP reported as posed", showStartupCmd, saveProbeUser)
	}
	if !strings.Contains(joined, "snmp-server group "+saveProbeGroup) {
		t.Errorf("the v3 GROUP did not survive into startup-config: a user whose group is " +
			"gone after a reboot authenticates and is authorised for nothing")
	}

	// The engine ID is the switch's own line, not the connector's. What it does
	// ACROSS a save is new ground: the previous run only ever saw it in the
	// running configuration.
	var runEngine = strings.Contains(strings.Join(runLines, "\n"), "snmp-server engineID")
	var startEngine = strings.Contains(joined, "snmp-server engineID")
	t.Logf("ENGINE ID — running-config carries \"snmp-server engineID\": %v ; "+
		"startup-config after the save: %v", runEngine, startEngine)

	// The two configurations must now differ from the baseline by the probe's
	// lines and nothing else — the strongest statement this probe can make about
	// what a save on this platform actually persisted.
	var afterStartFull = startupBody(sayLong(t, c, ctx, showStartupCmd))
	var addedByProbe []string
	var line string
	for _, line = range afterStartFull {
		if !strings.Contains(strings.Join(baseline, "\n"), strings.TrimSpace(line)) {
			addedByProbe = append(addedByProbe, strings.TrimSpace(line))
		}
	}
	t.Logf("STARTUP-CONFIG GAINED %d line(s) over the baseline:\n%s",
		len(addedByProbe), strings.Join(addedByProbe, "\n"))
}

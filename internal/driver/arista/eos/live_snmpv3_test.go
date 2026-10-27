package eos

// The SNMPv3 write forms, exercised THROUGH [Client.applySNMPv3] on real
// hardware — the thing snmp.go's header says is still owed.
//
// snmp.go ships two DEDUCED command forms and one deduced BEHAVIOUR:
//
//	snmp-server group <n> v3 priv
//	snmp-server user <n> <g> v3 auth sha <phrase> priv aes <phrase>
//	…and that re-issuing the user line REPLACES rather than being refused.
//
// This harness settles all three against core01.lab, and it drives the
// SHIPPED applier rather than typing at a CLI: what is proven is that
// [Client.applySNMPv3] builds forms this platform accepts and that its own
// verification gate — the "(not configured)" marker — agrees.
//
// # Why applySNMPv3 and not ApplySNMP
//
// [Client.ApplySNMP] finishes with [Client.save], i.e. "copy running-config
// startup-config". A probe on the fleet's CORE SWITCH must leave startup-config
// untouched so that a reload is a clean escape hatch, so the unexported half is
// driven directly and the persistence leg stays unexercised. Everything the
// deduction is about — the two command forms, their order, the re-read and the
// group gate — is inside applySNMPv3.
//
// # Running it
//
//	EOS_LIVE_HOST=192.0.2.5 EOS_LIVE_USER=… EOS_LIVE_PASS=… EOS_LIVE_SNMPV3=1 \
//	EOS_LIVE_SOCKS=127.0.0.1:1080 EOS_LIVE_SOCKS_USER=… EOS_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/arista/eos -run TestLiveSNMPv3 -v
//
// With EOS_LIVE_SNMPV3 unset the whole thing skips, which is how CI sees it.

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

// The probe's identity. A THROWAWAY name on purpose: nothing on this switch may
// be touched, so the user and the group both carry a name no operator would
// choose, and both are removed by the cleanup below.
const (
	probeV3User  = "mcprobev3b"
	probeV3Group = "meshprobeb"
)

// The two passphrase pairs. Throwaway, and DISTINCT within each pair because
// [checkV3Secrets] refuses a pair that is not (the encryption key would only be a
// prefix of the authentication key). The B pair is what leg 3 re-issues with: the
// whole question is whether the device takes it.
const (
	probeAuthA = "MeshProbeAuthAlpha1"
	probePrivA = "MeshProbePrivAlpha1"
	probeAuthB = "MeshProbeAuthBravo22"
	probePrivB = "MeshProbePrivBravo22"
)

// liveSNMPSession opens the session, or skips.
//
// The session is closed from [testing.T.Cleanup] REGISTERED FIRST, never from a
// `defer` in the test: cleanups run after the test function's defers, so a
// deferred Close would shut the shell before the removal block gets to run — and
// the probe user would be left on the core switch. Registering it here means it
// runs LAST, cleanups being LIFO.
func liveSNMPSession(t *testing.T) (*Client, context.Context) {
	t.Helper()
	if os.Getenv("EOS_LIVE_SNMPV3") != "1" {
		t.Skip("EOS_LIVE_SNMPV3 not set — live SNMPv3 probe skipped")
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
	var ctx, cancel = context.WithTimeout(context.Background(), 6*time.Minute)
	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("EOS_LIVE_USER"), Secret: os.Getenv("EOS_LIVE_PASS"),
		Why: "live SNMPv3 write-form probe",
	}})
	if err != nil {
		cancel()
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	t.Cleanup(func() { c.Close(); cancel() })
	return c, ctx
}

// say runs one command and prints it verbatim. Every capture in this file goes
// through it, so what the report quotes is what the switch sent.
func say(t *testing.T, c *Client, ctx context.Context, cmd string) string {
	t.Helper()
	var out, err = c.cli.run(ctx, cmd, cmdTimeout)
	t.Logf("===== %s =====\n%s", cmd, out)
	if err != nil {
		t.Logf("(\"%s\" returned: %v)", cmd, err)
	}
	return out
}

// rawConfig sends a configuration block and RETURNS the device's own text for
// each line. [Client.runConfigBlock] discards that text — it only keeps whatever
// [reCLIError] matched — and for a deduced form the exact wording is the
// evidence, so the cleanup and the by-hand legs use this instead.
func rawConfig(t *testing.T, c *Client, ctx context.Context, cmds []string) string {
	t.Helper()
	var b strings.Builder
	var all = append([]string{"configure"}, cmds...)
	all = append(all, "end")
	var cmd string
	for _, cmd = range all {
		var out, _ = c.cli.run(ctx, cmd, cmdTimeout)
		b.WriteString("===== " + cmd + " =====\n" + out + "\n")
	}
	return b.String()
}

// snmpLines keeps only the "snmp-server …" lines of a running-config section,
// so a before/after comparison is not defeated by the echoed command or the
// prompt that frames it.
func snmpLines(out string) []string {
	var keep []string
	var line string
	for _, line = range strings.Split(out, "\n") {
		line = strings.TrimRight(strings.TrimSpace(line), "\r")
		if strings.HasPrefix(line, "snmp-server ") {
			keep = append(keep, line)
		}
	}
	return keep
}

// TestLiveSNMPv3WriteForms is the whole probe: four questions, in order.
func TestLiveSNMPv3WriteForms(t *testing.T) {
	var c, ctx = liveSNMPSession(t)

	// --- 0) BEFORE. Nothing is written until this reads the way it must.
	t.Log("--- 0) BEFORE ---")
	var beforeRun = say(t, c, ctx, "show running-config | section snmp")
	var beforeUsers = say(t, c, ctx, showSNMPUserCmd)
	say(t, c, ctx, "show snmp group")

	// "as found" means WITHOUT the probe's own leavings: a previous run that
	// died before its removal block must be cleanable by the next one, and its
	// leftovers must not become the baseline the cleanup restores to. Anything
	// ELSE naming a v3 user or group belongs to somebody, and stops the probe.
	var before []string
	var leftovers bool
	var l string
	for _, l = range snmpLines(beforeRun) {
		if strings.Contains(l, probeV3User) || strings.Contains(l, probeV3Group) {
			leftovers = true
		}
	}
	for _, l = range snmpLines(beforeRun) {
		if strings.Contains(l, probeV3User) || strings.Contains(l, probeV3Group) {
			t.Logf("LEFTOVER from an earlier run, not part of the baseline: %s", l)
			continue
		}
		// MEASURED, 2026-08-10: posing the first v3 user materialises
		// "snmp-server engineID local …" in the running configuration, and
		// removing that user takes the line away again — the device manages it,
		// not the connector. So on a switch still carrying the probe's leavings
		// that line is one of them, and counting it as baseline would make the
		// cleanup demand a line the device itself deletes.
		if leftovers && strings.HasPrefix(l, "snmp-server engineID local ") {
			t.Logf("LEFTOVER from an earlier run, not part of the baseline: %s", l)
			continue
		}
		if strings.HasPrefix(l, "snmp-server user ") || strings.HasPrefix(l, "snmp-server group ") {
			t.Fatalf("this switch already carries \"%s\" — refusing to run: this probe "+
				"must not touch an existing v3 configuration", l)
		}
		before = append(before, l)
	}
	var u snmpUser
	for _, u = range parseSNMPUsers(beforeUsers) {
		if u.Name != probeV3User {
			t.Fatalf("\"%s\" already lists user %q — refusing to run",
				showSNMPUserCmd, u.Name)
		}
	}

	// The removal is registered BEFORE the first write, so a failure at any leg
	// still leaves the switch as it was found.
	t.Cleanup(func() {
		var ictx, icancel = context.WithTimeout(context.Background(), 90*time.Second)
		defer icancel()
		t.Log("--- CLEANUP ---")
		t.Log(rawConfig(t, c, ictx, []string{
			"no snmp-server user " + probeV3User + " " + probeV3Group + " v3",
			"no snmp-server group " + probeV3Group + " v3 priv",
		}))
		// Creating a v3 user MATERIALISES "snmp-server engineID local …" in the
		// running configuration — a line this switch did not carry before — and
		// MEASURED on 2026-08-10, removing the user takes it away again by itself.
		// This block is therefore a safety net that did not have to fire, kept
		// because "left as found" has to mean the text too: if a future release
		// leaves the line behind, the probe removes it, and it does so AFTER the
		// user, when no key derived from that engine is left to invalidate.
		var leftRun = say(t, c, ictx, "show running-config | section snmp")
		var stray []string
		var line string
		for _, line = range snmpLines(leftRun) {
			if strings.HasPrefix(line, "snmp-server engineID") &&
				!strings.Contains(strings.Join(before, "\n"), line) {
				stray = append(stray, "no "+line)
			}
		}
		if len(stray) > 0 {
			t.Logf("the probe left %d line(s) the switch did not have; removing", len(stray))
			t.Log(rawConfig(t, c, ictx, stray))
			leftRun = say(t, c, ictx, "show running-config | section snmp")
		}

		var leftUsers = say(t, c, ictx, showSNMPUserCmd)
		say(t, c, ictx, "show snmp group")
		var got = snmpLines(leftRun)
		if strings.Join(got, "\n") != strings.Join(before, "\n") {
			t.Errorf("THE SWITCH WAS NOT LEFT AS FOUND.\nbefore:\n%s\nafter:\n%s",
				strings.Join(before, "\n"), strings.Join(got, "\n"))
		}
		if len(parseSNMPUsers(leftUsers)) != 0 {
			t.Errorf("\"%s\" still lists a user after cleanup", showSNMPUserCmd)
		}
	})

	// --- 1 & 2) The group and the user, through the shipped applier.
	t.Log("--- 1+2) applySNMPv3, passphrase pair A ---")
	var wantA = SNMPIdentity{
		User: probeV3User, Group: probeV3Group,
		AuthAlg: "sha", PrivAlg: "aes128",
		AuthSecret: probeAuthA, PrivSecret: probePrivA,
	}
	var outA = SNMPResult{}
	var wroteA, errA = c.applySNMPv3(ctx, wantA, "sha", "aes", &outA)
	t.Logf("applySNMPv3(A) -> wrote=%v err=%v result=%+v", wroteA, errA, outA)
	if errA != nil {
		// Not a t.Fatal in disguise: this IS the answer if a deduced form is wrong,
		// and the captures below are what diagnose it.
		t.Errorf("the shipped applier could not pose the v3 identity: %v", errA)
	}
	var st driver.Step
	for _, st = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", st.OK, st.What, st.Detail)
	}

	t.Log("--- what the device says about it ---")
	say(t, c, ctx, "show snmp group")
	say(t, c, ctx, showSNMPUserCmd)
	var runA = say(t, c, ctx, "show running-config | section snmp")

	// --- 3) THE HEADLINE: re-issue the SAME user with a DIFFERENT passphrase.
	t.Log("--- 3) re-issue with a DIFFERENT passphrase pair (B) ---")
	var wantB = wantA
	wantB.AuthSecret = probeAuthB
	wantB.PrivSecret = probePrivB
	var outB = SNMPResult{}
	var wroteB, errB = c.applySNMPv3(ctx, wantB, "sha", "aes", &outB)
	t.Logf("applySNMPv3(B) -> wrote=%v err=%v result=%+v", wroteB, errB, outB)
	if errB != nil {
		t.Logf("RE-ISSUE REFUSED — the device's own words: %v", errB)
	}
	say(t, c, ctx, showSNMPUserCmd)
	var runB = say(t, c, ctx, "show running-config | section snmp")

	// --- 3b) The SAME gesture BY HAND, for the device's LITERAL answer.
	//
	// [Client.runConfigBlock] keeps only what [reCLIError] matched, so a silent
	// acceptance and a refusal it failed to recognise look alike from inside the
	// applier. This leg sends the identical line through [rawConfig] and prints
	// what came back, so "accepted" is a quotation rather than an inference. The
	// passphrases are the throwaway constants at the top of this file, and the
	// device echoes the line it is given — nothing here reaches a stored
	// transcript, which is what [snmpV3UserCmd]'s masked twin is for.
	t.Log("--- 3b) the same re-issue BY HAND, verbatim ---")
	var handCmd, handShown = snmpV3UserCmd(probeV3User, probeV3Group, "sha", "aes",
		probeAuthA, probePrivA)
	t.Logf("sending (masked): %s", handShown)
	t.Log(rawConfig(t, c, ctx, []string{handCmd}))
	say(t, c, ctx, "show running-config | section snmp")

	// The running configuration is the only place a CHANGE of key could show: if
	// this platform stores the localised keys there, the two lines differ and the
	// re-issue provably replaced the user rather than being ignored.
	t.Logf("running-config snmp BEFORE re-issue:\n%s", strings.Join(snmpLines(runA), "\n"))
	t.Logf("running-config snmp AFTER  re-issue:\n%s", strings.Join(snmpLines(runB), "\n"))
	if strings.Join(snmpLines(runA), "\n") == strings.Join(snmpLines(runB), "\n") {
		t.Log("VERDICT INPUT: the running configuration is IDENTICAL across the two " +
			"passphrases — either the keys are not stored there at all, or the re-issue " +
			"changed nothing")
	} else {
		t.Log("VERDICT INPUT: the running configuration CHANGED across the two " +
			"passphrases — the re-issue replaced the user's keys")
	}
}

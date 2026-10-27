package s3400

// LIVE CAPTURE of the SNMPv3 reads, and of the "snmp-server host" rendering,
// in ONE session.
//
// Why one session and not four commands typed by hand: this range's pager eats
// the first character of whatever follows an unfed "--More--" (cf. ssh.go
// § "The pager is FED, not disabled"), so a raw ssh transcript with
// "terminal length 0" is not evidence here. Everything below goes through
// [cliConn.runPaged], which is the connector's own read path.
//
// WHAT IT DOES, in order, and it is deliberately the smallest gesture that
// answers the four open captures at once:
//
//	1. "show version" and the FULL "show running-config" — the BASELINE the
//	   revert is diffed against, and the recon on a device that rebooted itself.
//	2. "show snmp user" and "show snmp group" with NOTHING configured, so
//	   the empty rendering is known too.
//	3. poses a v3 group, a v3 user and a trap host, all named with a
//	   "meshcraft-live" prefix so nothing collides with site configuration.
//	4. captures "show snmp user", "show snmp group" and "show
//	   running-config" WITH them in place.
//	5. removes all three, re-reads, and FAILS if the final running-config is not
//	   byte-identical to the baseline.
//
// Triple-gated: S3400_LIVE_HOST, S3400_LIVE_WRITE=1 and S3400_LIVE_SNMPV3=1 —
// it writes SNMP identity to a real switch, which no other live test here does.
// Nothing is persisted: "write" is never sent, so a power cycle undoes even a
// failed revert.
//
//	S3400_LIVE_HOST=192.0.2.3 S3400_LIVE_USER=… S3400_LIVE_PASS=… \
//	S3400_LIVE_WRITE=1 S3400_LIVE_SNMPV3=1 \
//	S3400_LIVE_SOCKS=127.0.0.1:1080 S3400_LIVE_SOCKS_USER=… S3400_LIVE_SOCKS_PASS=… \
//	S3400_LIVE_CAPTURE_DIR=/tmp/caps \
//	go test ./internal/driver/fs/s3400 -run TestLiveCaptureSNMPv3 -v

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// liveCapture writes one raw capture beside the others and logs it whole.
func liveCapture(t *testing.T, name, body string) {
	t.Helper()
	t.Logf("=== %s ===\n%s\n=== end %s ===", name, body, name)
	var dir = os.Getenv("S3400_LIVE_CAPTURE_DIR")
	if dir == "" {
		return
	}
	var err = os.MkdirAll(dir, 0o755)
	if err != nil {
		t.Fatalf("capture dir: %v", err)
	}
	err = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	if err != nil {
		t.Errorf("writing %s: %v", name, err)
	}
}

// TestLiveS3400Recon is READ-ONLY and exists for one question: this unit went
// silent for hours on 2026-08-11 and came back on its own. Uptime and whatever
// log the platform keeps say whether it rebooted or merely lost its path.
func TestLiveS3400Recon(t *testing.T) {
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var cmd string
	for _, cmd = range []string{"show version", "show logging", "show memory", "show clock"} {
		var out, rerr = c.cli.runPaged(ctx, cmd, 60*time.Second)
		t.Logf("=== %s === (err=%v)\n%s", cmd, rerr, out)
	}
}

func TestLiveCaptureSNMPv3(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" || os.Getenv("S3400_LIVE_SNMPV3") != "1" {
		t.Skip("S3400_LIVE_WRITE=1 and S3400_LIVE_SNMPV3=1 not both set — skipped")
	}
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	// --- names and secrets. The passphrase comes from the environment: a
	// literal here would be a credential in the repository, and this one is
	// keyed into the device's own SHA digest for the length of the test.
	var user = "mcliveu"
	var group = "mcliveg"
	var pass = os.Getenv("S3400_LIVE_V3PASS")
	if pass == "" {
		t.Fatal("S3400_LIVE_V3PASS not set — refusing to invent a passphrase")
	}
	var trapHost = os.Getenv("S3400_LIVE_TRAPHOST")
	var trapComm = os.Getenv("S3400_LIVE_TRAPCOMM")
	if trapHost == "" || trapComm == "" {
		t.Fatal("S3400_LIVE_TRAPHOST / S3400_LIVE_TRAPCOMM not set")
	}

	// --- 1) recon + baseline.
	var out string
	out, err = c.cli.runPaged(ctx, "show version", cmdTimeout)
	if err != nil {
		t.Errorf("show version: %v", err)
	}
	liveCapture(t, "recon_show_version.txt", out)

	var baseline string
	baseline, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil || strings.TrimSpace(baseline) == "" {
		t.Fatalf("baseline show running-config unusable (%d bytes): %v", len(baseline), err)
	}
	liveCapture(t, "baseline_show_running_config.txt", baseline)

	// --- 2) the EMPTY renderings.
	out, err = c.cli.runPaged(ctx, "show snmp user", cmdTimeout)
	t.Logf("show snmp user (nothing configured): err=%v", err)
	liveCapture(t, "empty_show_snmp_user.txt", out)
	out, err = c.cli.runPaged(ctx, "show snmp group", cmdTimeout)
	t.Logf("show snmp group (nothing configured): err=%v", err)
	liveCapture(t, "empty_show_snmp_group.txt", out)

	// --- 3) pose. The group form is the one open question — the tail of
	// "snmp-server group <n> v3 priv …" was never written down — so it is
	// sent MINIMAL first and the running-config below says what the device
	// stored. A refusal here is a finding, not a failure of the harness.
	var groupLine = "snmp-server group " + group + " v3 priv"
	var groupEnv = os.Getenv("S3400_LIVE_V3GROUPLINE")
	if groupEnv != "" {
		groupLine = groupEnv
	}
	var userLine = "snmp-server user " + user + " " + group + " v3 encrypted auth sha " + pass
	var hostLine = "snmp-server host " + trapHost + " version v2c " + trapComm

	t.Logf("posing: %s", groupLine)
	if err = c.configBlock(ctx, []string{groupLine}); err != nil {
		t.Fatalf("GROUP REFUSED — %v", err)
	}
	t.Logf("posing: snmp-server user %s %s v3 encrypted auth sha <pass>", user, group)
	if err = c.configBlock(ctx, []string{userLine}); err != nil {
		t.Errorf("USER REFUSED — %v", err)
	}
	t.Logf("posing: %s", hostLine)
	if err = c.configBlock(ctx, []string{hostLine}); err != nil {
		t.Errorf("TRAP HOST REFUSED — %v", err)
	}

	// --- 4) capture with everything in place.
	out, err = c.cli.runPaged(ctx, "show snmp user", cmdTimeout)
	t.Logf("show snmp user (configured): err=%v", err)
	liveCapture(t, "show_snmp_user.txt", out)
	out, err = c.cli.runPaged(ctx, "show snmp group", cmdTimeout)
	t.Logf("show snmp group (configured): err=%v", err)
	liveCapture(t, "show_snmp_group.txt", out)

	var withV3 string
	withV3, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Errorf("show running-config with v3: %v", err)
	}
	liveCapture(t, "show_running_config_with_snmpv3.txt", withV3)
	var ln string
	for _, ln = range strings.Split(withV3, "\n") {
		if strings.Contains(strings.ToLower(ln), "snmp-server") ||
			strings.HasPrefix(strings.TrimSpace(strings.ToLower(ln)), "hostname") {
			t.Logf("cfg| %s", ln)
		}
	}

	// --- 5) remove, in the reverse order, each in its FULL form.
	var removals = []string{
		"no " + hostLine,
		"no snmp-server user " + user + " " + group + " v3",
		"no snmp-server group " + group,
	}
	var rm string
	for _, rm = range removals {
		if err = c.configBlock(ctx, []string{rm}); err != nil {
			t.Errorf("REMOVAL \"%s\" refused: %v", rm, err)
		}
	}

	var final string
	final, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Errorf("final show running-config: %v", err)
	}
	liveCapture(t, "final_show_running_config.txt", final)
	if final != baseline {
		t.Errorf("THE DEVICE DID NOT COME BACK TO ITS BASELINE — see the two captures")
		var a = strings.Split(baseline, "\n")
		var b = strings.Split(final, "\n")
		var seen = map[string]int{}
		var l string
		for _, l = range a {
			seen[l]++
		}
		for _, l = range b {
			seen[l]--
		}
		for l = range seen {
			if seen[l] != 0 {
				t.Errorf("  diff %+d %q", -seen[l], l)
			}
		}
	} else {
		t.Log("revert proven: the final running-config is BYTE-IDENTICAL to the baseline")
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

// TestLiveS3400V3PrivFormLadder answers the ONE question that decides whether a
// v3 applier can exist on this range at all: is there a user form taking a
// SEPARATE privacy passphrase?
//
// [driver.SNMPIdentity] requires two DISTINCT phrases and says "without a second
// phrase, we DO NOT set v3" — reusing one feeds the same value into both
// RFC 3414 derivations, and the privacy key becomes a prefix of the
// authentication key. The 2026-08-10 survey recorded that "encrypted" keys
// CBC-DES on the auth passphrase and that no priv-password argument exists. That
// is a premise, not a capture, and everything downstream rests on it.
//
// So each candidate form is SENT, its verdict recorded, and anything that was
// accepted is removed. The running-config must come back byte-identical.
// Refusals cost nothing: a command this CLI rejects configures nothing.
//
// NEVER probed with "?" — the trailing newline of an interactive shell submits
// whatever the help left on the line (tools/labssh ran "spanning-tree root
// super" on the fleet core that way). Whole commands only.
func TestLiveS3400V3PrivFormLadder(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" || os.Getenv("S3400_LIVE_SNMPV3") != "1" {
		t.Skip("S3400_LIVE_WRITE=1 and S3400_LIVE_SNMPV3=1 not both set — skipped")
	}
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var p1 = os.Getenv("S3400_LIVE_V3PASS")
	var p2 = os.Getenv("S3400_LIVE_V3PASS2")
	if p1 == "" || p2 == "" {
		t.Fatal("S3400_LIVE_V3PASS / S3400_LIVE_V3PASS2 not set")
	}
	var group = "mcladg"

	var baseline string
	baseline, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil || strings.TrimSpace(baseline) == "" {
		t.Fatalf("baseline unusable: %v", err)
	}

	if err = c.configBlock(ctx, []string{"snmp-server group " + group + " v3 priv"}); err != nil {
		t.Fatalf("group refused: %v", err)
	}

	// Each candidate: a user name of its own, so one acceptance never masks the
	// next form's verdict.
	type form struct{ name, line string }
	var forms = []form{
		{"mclad1", "snmp-server user mclad1 " + group + " v3 auth sha " + p1 + " priv des " + p2},
		{"mclad2", "snmp-server user mclad2 " + group + " v3 auth sha " + p1 + " priv aes " + p2},
		{"mclad3", "snmp-server user mclad3 " + group + " v3 encrypted auth sha " + p1 + " priv des " + p2},
		{"mclad4", "snmp-server user mclad4 " + group + " v3 auth sha " + p1 + " priv " + p2},
		{"mclad5", "snmp-server user mclad5 " + group + " v3 auth md5 " + p1},
	}
	var accepted []string
	var f form
	for _, f = range forms {
		var cerr = c.configBlock(ctx, []string{f.line})
		var shown = strings.ReplaceAll(strings.ReplaceAll(f.line, p1, "<P1>"), p2, "<P2>")
		if cerr != nil {
			t.Logf("REFUSED  %s → %v", shown, cerr)
			continue
		}
		t.Logf("ACCEPTED %s", shown)
		accepted = append(accepted, f.name)
	}

	var out string
	out, err = c.cli.runPaged(ctx, "show snmp user", 60*time.Second)
	t.Logf("=== show snmp user after the ladder === (err=%v)\n%s", err, out)
	liveCapture(t, "ladder_show_snmp_user.txt", out)

	var withAll string
	withAll, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	t.Logf("running-config read after the ladder: err=%v", err)
	var ln string
	for _, ln = range strings.Split(withAll, "\n") {
		if strings.Contains(ln, "snmp-server") {
			t.Logf("cfg| %s", strings.ReplaceAll(strings.ReplaceAll(ln, p1, "<P1>"), p2, "<P2>"))
		}
	}
	liveCapture(t, "ladder_show_running_config.txt",
		strings.ReplaceAll(strings.ReplaceAll(withAll, p1, "<P1>"), p2, "<P2>"))

	// --- remove everything that took.
	var n string
	for _, n = range accepted {
		if err = c.configBlock(ctx, []string{"no snmp-server user " + n + " " + group + " v3"}); err != nil {
			t.Errorf("removing %s: %v", n, err)
		}
	}
	if err = c.configBlock(ctx, []string{"no snmp-server group " + group}); err != nil {
		t.Errorf("removing group: %v", err)
	}

	var final string
	final, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Errorf("final read: %v", err)
	}
	liveCapture(t, "ladder_final_show_running_config.txt", final)
	if final != baseline {
		t.Error("THE DEVICE DID NOT COME BACK TO ITS BASELINE")
		var seen = map[string]int{}
		var l string
		for _, l = range strings.Split(baseline, "\n") {
			seen[l]++
		}
		for _, l = range strings.Split(final, "\n") {
			seen[l]--
		}
		for l = range seen {
			if seen[l] != 0 {
				t.Errorf("  diff %+d %q", -seen[l], l)
			}
		}
	} else {
		t.Log("revert proven: final running-config BYTE-IDENTICAL to the baseline")
	}
}

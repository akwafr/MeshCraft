package s3400

// LIVE LADDER for the authNoPriv decision of 2026-08-12.
//
// The 2026-08-11 ladder ([TestLiveS3400V3PrivFormLadder]) settled the NEGATIVE
// half: no user form on this firmware takes a second passphrase, so authPriv is
// not expressible. It left THREE questions open, and every one of them decides a
// line of the applier rather than a comment:
//
//  1. IS "snmp-server group <g> v3 auth" ACCEPTED? Every group written on this
//     range so far was "v3 priv". The trap recorded on 2026-08-10 is that an
//     authNoPriv user against a priv group answers "authorizationError" — the
//     walk authenticates and returns nothing — so an authNoPriv applier MUST
//     create the group at "auth". If that form is refused, the decision is dead
//     and the connector says so instead of shipping a group it cannot match;
//  2. WHICH AUTH ALGORITHMS does the user line take? "auth md5 <P>" was
//     ACCEPTED by the ladder and "encrypted auth sha <P>" by the capture run.
//     Neither says whether PLAIN "auth sha <P>" works, nor whether anything
//     above SHA-1 exists. The capability declaration is exactly this answer, and
//     guessing it either way is what [Capabilities.SNMPv3Auth] exists to prevent;
//  3. IS A USER NAMING A MISSING GROUP ACCEPTED? That is the EOS failure class —
//     a user that authenticates and is authorised for nothing. If this firmware
//     REFUSES it, group creation is verified by the user line itself; if it
//     accepts it, the applier must gate on "show snmp group" like EOS does.
//
// Every candidate is a WHOLE COMMAND. Nothing here is probed with "?": the
// trailing newline of an interactive shell submits whatever the help left on the
// line, which is how tools/labssh once ran "spanning-tree root super" on the
// fleet core. A refused command configures nothing, so refusals are free; what
// is accepted is removed, and the running-config must come back BYTE-IDENTICAL.
//
//	S3400_LIVE_HOST=192.0.2.3 S3400_LIVE_USER=… S3400_LIVE_PASS=… \
//	S3400_LIVE_WRITE=1 S3400_LIVE_SNMPV3=1 S3400_LIVE_V3PASS=… \
//	S3400_LIVE_SOCKS=127.0.0.1:1080 S3400_LIVE_SOCKS_USER=… S3400_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/fs/s3400 -run TestLiveS3400AuthNoPrivLadder -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveS3400AuthNoPrivLadder(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" || os.Getenv("S3400_LIVE_SNMPV3") != "1" {
		t.Skip("S3400_LIVE_WRITE=1 and S3400_LIVE_SNMPV3=1 not both set — skipped")
	}
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var p1 = os.Getenv("S3400_LIVE_V3PASS")
	if p1 == "" {
		t.Fatal("S3400_LIVE_V3PASS not set — refusing to invent a passphrase")
	}
	var hide = func(s string) string { return strings.ReplaceAll(s, p1, "<P1>") }

	var baseline string
	baseline, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil || strings.TrimSpace(baseline) == "" {
		t.Fatalf("baseline unusable (%d bytes): %v", len(baseline), err)
	}

	// --- Q3 FIRST, because it must be asked while NO group exists. A user
	// naming a group nobody created is exactly the EOS failure class, and the
	// answer decides whether the applier needs a "show snmp group" gate.
	var orphan = "snmp-server user mcorph mcnogroup v3 auth sha " + p1
	var oerr = c.configBlock(ctx, []string{orphan})
	t.Logf("Q3 orphan-group user → %v (%s)", oerr, hide(orphan))
	var orphanTook = oerr == nil
	if orphanTook {
		var out, _ = c.cli.runPaged(ctx, "show snmp user", 60*time.Second)
		t.Logf("=== Q3 show snmp user (group mcnogroup never created) ===\n%s", out)
		liveCapture(t, "ladder_orphan_show_snmp_user.txt", out)
	}

	// --- Q1: the group at "auth" rather than "priv".
	var groups = []struct{ name, line string }{
		{"mcagA", "snmp-server group mcagA v3 auth"},
		{"mcagN", "snmp-server group mcagN v3 noauth"},
	}
	var madeGroups []string
	var g struct{ name, line string }
	for _, g = range groups {
		var gerr = c.configBlock(ctx, []string{g.line})
		if gerr != nil {
			t.Logf("Q1 REFUSED  %s → %v", g.line, gerr)
			continue
		}
		t.Logf("Q1 ACCEPTED %s", g.line)
		madeGroups = append(madeGroups, g.name)
	}
	var gout string
	gout, err = c.cli.runPaged(ctx, "show snmp group", 60*time.Second)
	t.Logf("=== Q1 show snmp group === (err=%v)\n%s", err, gout)
	liveCapture(t, "ladder_auth_show_snmp_group.txt", gout)

	// --- Q2: the authentication algorithms, against whichever group took. The
	// group is a prerequisite, so fall back to the known-good "v3 priv" form
	// rather than skip the whole question.
	var useGroup = "mcagA"
	if len(madeGroups) == 0 {
		useGroup = "mcagP"
		var gerr = c.configBlock(ctx, []string{"snmp-server group mcagP v3 priv"})
		if gerr != nil {
			t.Fatalf("even the known-good \"v3 priv\" group was refused: %v", gerr)
		}
		madeGroups = append(madeGroups, useGroup)
	}
	type form struct{ name, line string }
	var forms = []form{
		{"mcau1", "snmp-server user mcau1 " + useGroup + " v3 auth sha " + p1},
		{"mcau2", "snmp-server user mcau2 " + useGroup + " v3 auth md5 " + p1},
		{"mcau3", "snmp-server user mcau3 " + useGroup + " v3 encrypted auth sha " + p1},
		{"mcau4", "snmp-server user mcau4 " + useGroup + " v3 auth sha256 " + p1},
		{"mcau5", "snmp-server user mcau5 " + useGroup + " v3 auth sha-256 " + p1},
		{"mcau6", "snmp-server user mcau6 " + useGroup + " v3 noauth"},
	}
	var accepted []string
	var f form
	for _, f = range forms {
		var cerr = c.configBlock(ctx, []string{f.line})
		if cerr != nil {
			t.Logf("Q2 REFUSED  %s → %v", hide(f.line), cerr)
			continue
		}
		t.Logf("Q2 ACCEPTED %s", hide(f.line))
		accepted = append(accepted, f.name)
	}

	// --- Q4, free once the ladder is standing: is RE-ISSUING a user line a
	// replacement or a refusal? Every rotation on this range depends on it, and
	// its failure mode on EOS was a silent no-op.
	if len(accepted) > 0 {
		var again = "snmp-server user " + accepted[0] + " " + useGroup + " v3 auth sha " + p1
		var aerr = c.configBlock(ctx, []string{again})
		t.Logf("Q4 re-issue of an existing user → %v", aerr)
	}

	var uout string
	uout, err = c.cli.runPaged(ctx, "show snmp user", 60*time.Second)
	t.Logf("=== Q2 show snmp user === (err=%v)\n%s", err, uout)
	liveCapture(t, "ladder_authnopriv_show_snmp_user.txt", uout)
	gout, err = c.cli.runPaged(ctx, "show snmp group", 60*time.Second)
	t.Logf("=== Q2 show snmp group (with users) === (err=%v)\n%s", err, gout)

	var withAll string
	withAll, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	t.Logf("running-config after the ladder: err=%v", err)
	var ln string
	for _, ln = range strings.Split(withAll, "\n") {
		if strings.Contains(ln, "snmp-server") {
			t.Logf("cfg| %s", hide(ln))
		}
	}
	liveCapture(t, "ladder_authnopriv_running_config.txt", hide(withAll))

	// --- remove everything that took, users before groups.
	var n string
	for _, n = range accepted {
		if err = c.configBlock(ctx, []string{"no snmp-server user " + n + " " + useGroup + " v3"}); err != nil {
			t.Errorf("removing user %s: %v", n, err)
		}
	}
	if orphanTook {
		if err = c.configBlock(ctx, []string{"no snmp-server user mcorph mcnogroup v3"}); err != nil {
			t.Errorf("removing the orphan user: %v", err)
		}
	}
	for _, n = range madeGroups {
		if err = c.configBlock(ctx, []string{"no snmp-server group " + n}); err != nil {
			t.Errorf("removing group %s: %v", n, err)
		}
	}

	var final string
	final, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Errorf("final read: %v", err)
	}
	liveCapture(t, "ladder_authnopriv_final_running_config.txt", final)
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
				t.Errorf("  diff %+d %q", -seen[l], hide(l))
			}
		}
	} else {
		t.Log("revert proven: final running-config BYTE-IDENTICAL to the baseline")
	}
}

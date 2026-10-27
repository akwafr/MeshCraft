package s3400

// LIVE EXERCISE of the authNoPriv applier itself — not of a hand-typed form, but
// of [Client.ApplySNMP] as the controller calls it.
//
// A ladder proves what the CLI accepts. This proves what the CONNECTOR does with
// that, which is a different claim and the one the fleet depends on:
//
//	1. a PRIVACY request is REFUSED and writes NOTHING — the substance of the
//	   2026-08-12 decision. Checked FIRST, and the running-config is compared
//	   immediately, so a refusal that had quietly written the authNoPriv line
//	   anyway would be caught before anything else muddies the configuration;
//	2. an algorithm above SHA-1 is REFUSED and writes nothing;
//	3. an authNoPriv identity is WRITTEN and CONFIRMED by both reads plus the
//	   passphrase comparison;
//	4. re-applying the SAME identity writes NOTHING and reports it — the
//	   idempotence this connector can afford because it can see the phrase;
//	5. a ROTATION (same user, new passphrase) IS detected and written — the
//	   failure mode of idempotence done wrong, and the reason EOS rewrites blindly;
//	6. everything is removed and the running-config must come back BYTE-IDENTICAL.
//
// Triple-gated and never persisted: "write" is never sent, so a power cycle
// undoes even a failed revert.
//
//	S3400_LIVE_HOST=192.0.2.3 S3400_LIVE_USER=… S3400_LIVE_PASS=… \
//	S3400_LIVE_WRITE=1 S3400_LIVE_SNMPV3=1 S3400_LIVE_V3PASS=… S3400_LIVE_V3PASS2=… \
//	go test ./internal/driver/fs/s3400 -run TestLiveS3400ApplySNMPv3 -v

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

func TestLiveS3400ApplySNMPv3(t *testing.T) {
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
	var p2 = os.Getenv("S3400_LIVE_V3PASS2")
	if p1 == "" || p2 == "" {
		t.Fatal("S3400_LIVE_V3PASS / S3400_LIVE_V3PASS2 not set")
	}
	var user = "mcapplyu"
	var group = "mcapplyg"

	var baseline string
	baseline, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil || strings.TrimSpace(baseline) == "" {
		t.Fatalf("baseline unusable (%d bytes): %v", len(baseline), err)
	}

	// --- 1) PRIVACY IS REFUSED, AND NOTHING IS WRITTEN.
	var res, perr = c.ApplySNMP(ctx, SNMPIdentity{
		User: user, Group: group, AuthAlg: "sha", AuthSecret: p1,
		PrivAlg: "des", PrivSecret: p2,
	})
	if perr == nil {
		t.Errorf("A PRIVACY REQUEST WAS ACCEPTED — result %+v. The whole decision of "+
			"2026-08-12 is that this must refuse rather than downgrade", res)
	} else {
		t.Logf("privacy refused: %v", perr)
		if !errors.Is(perr, driver.ErrNotImplemented) {
			t.Errorf("the refusal does not attach to driver.ErrNotImplemented: %v", perr)
		}
	}
	var afterRefusal string
	afterRefusal, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err == nil && afterRefusal != baseline {
		t.Error("THE PRIVACY REFUSAL WROTE SOMETHING — the configuration moved while the " +
			"connector said it had refused, which is worse than a silent downgrade")
	}

	// The passphrase-only shape, which a connector reading only PrivAlg would miss.
	_, perr = c.ApplySNMP(ctx, SNMPIdentity{
		User: user, Group: group, AuthAlg: "sha", AuthSecret: p1, PrivSecret: p2,
	})
	t.Logf("privacy passphrase with no algorithm → %v", perr)
	if perr == nil {
		t.Error("an encryption passphrase was accepted with no algorithm named: it would " +
			"have been dropped on the floor")
	}

	// --- 2) AN ALGORITHM ABOVE SHA-1 IS REFUSED.
	_, perr = c.ApplySNMP(ctx, SNMPIdentity{
		User: user, Group: group, AuthAlg: "sha256", AuthSecret: p1,
	})
	t.Logf("sha256 → %v", perr)
	if perr == nil {
		t.Error("SHA-256 was accepted: this firmware answers \"Unknown command\" to it, " +
			"so something was written that is not what was asked for")
	}

	// --- 3) THE authNoPriv IDENTITY IS WRITTEN AND CONFIRMED.
	res, perr = c.ApplySNMP(ctx, SNMPIdentity{
		User: user, Group: group, AuthAlg: "sha", AuthSecret: p1,
	})
	if perr != nil {
		t.Fatalf("authNoPriv apply FAILED: %v (result %+v)", perr, res)
	}
	t.Logf("apply #1: %+v", res)
	if !res.UserSet || !res.GroupCreated || !res.Enabled {
		t.Errorf("apply #1 result %+v — a first write must report the user, the group "+
			"and the agent", res)
	}

	var withV3 string
	withV3, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	var ln string
	for _, ln = range strings.Split(withV3, "\n") {
		if strings.Contains(ln, "snmp-server") {
			t.Logf("cfg| %s", strings.ReplaceAll(strings.ReplaceAll(ln, p1, "<P1>"), p2, "<P2>"))
		}
	}

	// --- 4) RE-APPLYING THE SAME IDENTITY MUST WRITE NOTHING.
	res, perr = c.ApplySNMP(ctx, SNMPIdentity{
		User: user, Group: group, AuthAlg: "sha", AuthSecret: p1,
	})
	if perr != nil {
		t.Fatalf("apply #2 (identical) FAILED: %v", perr)
	}
	t.Logf("apply #2 (identical): %+v", res)
	if res.UserSet || res.GroupCreated {
		t.Errorf("apply #2 rewrote an identity that already matched (%+v): the passphrase "+
			"is comparable on this range, so an unchanged identity must cost nothing", res)
	}
	var afterIdem string
	afterIdem, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err == nil && afterIdem != withV3 {
		t.Error("the identical re-apply CHANGED the configuration")
	}

	// --- 5) A ROTATION MUST BE DETECTED AND WRITTEN.
	res, perr = c.ApplySNMP(ctx, SNMPIdentity{
		User: user, Group: group, AuthAlg: "sha", AuthSecret: p2,
	})
	if perr != nil {
		t.Fatalf("rotation apply FAILED: %v", perr)
	}
	t.Logf("apply #3 (rotated passphrase): %+v", res)
	if !res.UserSet {
		t.Error("A ROTATION WAS A SILENT NO-OP — the collector would keep failing to " +
			"authenticate against an identity every screen calls conformant. This is the " +
			"exact failure the passphrase comparison exists to prevent")
	}
	var rotated string
	rotated, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	var _, _, storedPhrase, storedOK = findSNMPv3UserInConfig(rotated, user)
	if !storedOK {
		t.Error("the rotated user line could not be read back")
	} else if storedPhrase != p2 {
		t.Error("the device stored a phrase that is not the rotated one")
	} else {
		t.Log("rotation confirmed: the running configuration carries the NEW passphrase")
	}

	// --- 6) REMOVE, and prove the revert.
	var rm string
	for _, rm = range []string{
		"no snmp-server user " + user + " " + group + " v3",
		"no snmp-server group " + group,
	} {
		if err = c.configBlock(ctx, []string{rm}); err != nil {
			t.Errorf("REMOVAL \"%s\" refused: %v", rm, err)
		}
	}
	var final string
	final, err = c.cli.runPaged(ctx, "show running-config", 120*time.Second)
	if err != nil {
		t.Errorf("final read: %v", err)
	}
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
				t.Errorf("  diff %+d %q", -seen[l],
					strings.ReplaceAll(strings.ReplaceAll(l, p1, "<P1>"), p2, "<P2>"))
			}
		}
	} else {
		t.Log("revert proven: final running-config BYTE-IDENTICAL to the baseline")
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

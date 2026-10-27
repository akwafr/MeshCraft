package socks

import (
	"context"
	"testing"
	"time"
)

// The Minter is the security boundary of the external-driver transport: a
// driver holds nothing but these credentials, so what they allow IS what a
// driver can do. Every test below is one clause of that boundary.

func TestMintedCredentialReachesItsHostAndNothingElse(t *testing.T) {
	var m = NewMinter()
	var cred, err = m.Mint("sess-1", "192.0.2.4", "proxy-lab", "sw-lab-01", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var ctx = context.Background()

	var subject, label, ok = m.Authenticate(ctx, cred.Username, cred.Password)
	if !ok || subject != cred.Username {
		t.Fatalf("the minted pair must authenticate: ok=%v subject=%q", ok, subject)
	}
	if label == "" {
		t.Fatal("the audit label must name the session and device")
	}

	var proxyID, deviceName, allowed = m.ProxyFor(ctx, subject, "192.0.2.4")
	if !allowed || proxyID != "proxy-lab" || deviceName != "sw-lab-01" {
		t.Fatalf("the credential's own host must resolve to the frozen route, got %q %q %v",
			proxyID, deviceName, allowed)
	}

	// THE CLAUSE THAT MATTERS: any other destination is refused. A driver that
	// can reach one switch must not be able to reach the one next to it.
	var _, _, other = m.ProxyFor(ctx, subject, "192.0.2.5")
	if other {
		t.Fatal("a minted credential resolved a host it was not minted for")
	}
}

func TestWrongPasswordAndUnknownUserAreRefused(t *testing.T) {
	var m = NewMinter()
	var cred, _ = m.Mint("sess-1", "192.0.2.4", "", "sw", time.Minute)
	var ctx = context.Background()
	var _, _, ok = m.Authenticate(ctx, cred.Username, "not-the-password")
	if ok {
		t.Fatal("wrong password accepted")
	}
	_, _, ok = m.Authenticate(ctx, "drv-nobody", cred.Password)
	if ok {
		t.Fatal("unknown username accepted")
	}
}

func TestExpiryRevocationAndRenewal(t *testing.T) {
	var m = NewMinter()
	var ctx = context.Background()

	// Expired: both doors close, and Renew refuses to resurrect.
	var cred, _ = m.Mint("sess-old", "10.0.0.1", "", "sw", -time.Second)
	var _, _, ok = m.Authenticate(ctx, cred.Username, cred.Password)
	if ok {
		t.Fatal("expired credential authenticated")
	}
	if m.Renew(cred.Username, time.Minute) {
		t.Fatal("an expired credential must not be renewable: the session it served is gone")
	}

	// Live: renewal extends, revocation ends, twice over is the ordinary path.
	cred, _ = m.Mint("sess-live", "10.0.0.2", "", "sw", time.Minute)
	if !m.Renew(cred.Username, time.Hour) {
		t.Fatal("a live credential must renew")
	}
	m.Revoke(cred.Username)
	m.Revoke(cred.Username)
	_, _, ok = m.Authenticate(ctx, cred.Username, cred.Password)
	if ok {
		t.Fatal("revoked credential authenticated")
	}

	// Sweep bounds the map without changing observable behaviour.
	m.Sweep()
}

func TestTwoSessionsDoNotShareCredentials(t *testing.T) {
	var m = NewMinter()
	var ctx = context.Background()
	var a, _ = m.Mint("sess-a", "10.0.0.1", "p1", "sw-a", time.Minute)
	var b, _ = m.Mint("sess-b", "10.0.0.2", "p2", "sw-b", time.Minute)
	if a.Username == b.Username || a.Password == b.Password {
		t.Fatal("two sessions minted overlapping credentials")
	}
	// A's identity must not open B's route.
	var _, _, crossed = m.ProxyFor(ctx, a.Username, "10.0.0.2")
	if crossed {
		t.Fatal("session A resolved session B's device")
	}
}

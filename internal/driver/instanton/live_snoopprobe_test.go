package instanton

// LIVE DHCP-SNOOPING SECTION PROBE — READ ONLY, and the point of it is that
// nothing here is a hypothesis except the names being tried.
//
// This connector's snooping section AND field names have never been captured on
// a 1930: `dhcpSnoopGlobalSections`, `dhcpSnoopVLANSections` and
// `dhcpSnoopPortSections` are candidate lists, and `ReadDHCPSnooping` returns an
// ERROR rather than a zero state when none of them answers — deliberately, since
// "snooping is off" about a device we could not question is the sentence an
// operator would act on.
//
// So this test asks the device and PRINTS WHAT CAME BACK. It asserts almost
// nothing: a probe that fails the build when the guess is wrong would tell us
// only what we already know. What it must do is leave a transcript good enough
// to paste into dhcpsnoop.go, which means the section bodies verbatim.
//
//	INSTANTON_LIVE_HOST=198.51.100.101 INSTANTON_LIVE_USER=… INSTANTON_LIVE_PASS=… \
//	INSTANTON_LIVE_SOCKS=127.0.0.1:1080 INSTANTON_LIVE_SOCKS_USER=… INSTANTON_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/instanton -run TestLiveProbeSnoopSections -v

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

// liveSnoopAdmin opens a session using the same environment as the MST harness.
func liveSnoopAdmin(ctx context.Context, t *testing.T, why string) *Admin {
	t.Helper()
	var tgt = driver.Target{Host: os.Getenv("INSTANTON_LIVE_HOST")}
	var sa = os.Getenv("INSTANTON_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("INSTANTON_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("INSTANTON_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var adm, err = (&Driver1930{}).Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("INSTANTON_LIVE_USER"),
		Secret:   os.Getenv("INSTANTON_LIVE_PASS"),
		Why:      why,
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var a, _ = adm.(*Admin)
	return a
}

func TestLiveProbeSnoopSections(t *testing.T) {
	if os.Getenv("INSTANTON_LIVE_HOST") == "" {
		t.Skip("INSTANTON_LIVE_HOST not set")
	}
	// ONE session for the whole probe. This range allows five concurrent
	// sessions and no more, so a probe that opened one per candidate name would
	// lock the operator out of their own switch to answer a question about
	// section spelling.
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var a = liveSnoopAdmin(ctx, t, "DHCP snooping section probe (read only)")

	var loc = a.locateSnoopSections(ctx)
	t.Logf("tried: %s", strings.Join(loc.tried, ", "))
	t.Logf("global section: %q", loc.global)
	t.Logf("VLAN   section: %q", loc.vlan)
	t.Logf("port   section: %q", loc.port)

	var dump = func(label, name, body string) {
		if name == "" {
			t.Logf("--- %s: NO CANDIDATE ANSWERED", label)
			return
		}
		t.Logf("--- %s = %q, %d bytes:\n%s", label, name, len(body), body)
	}
	dump("global", loc.global, loc.globalBody)
	dump("vlan", loc.vlan, loc.vlanBody)
	dump("port", loc.port, loc.portBody)

	if !loc.found() {
		// NOT a failure of this test. The candidate names are guesses, and
		// "none of them" is a real answer that has to be recorded so the next
		// person tries different names rather than the same ones.
		t.Logf("NO SECTION ANSWERED — the candidate lists in dhcpsnoop.go are all wrong "+
			"for this firmware. Record the %d names tried so they are not retried blind.",
			len(loc.tried))
		return
	}

	// Something answered: show what the connector makes of it, so the field
	// aliases get exercised on real content and not only the section names.
	var st, err = a.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Errorf("a section answered but ReadDHCPSnooping still failed: %v", err)
		return
	}
	t.Logf("parsed: enabled=%v VLANs=%v ports=%d", st.Enabled, st.VLANs, len(st.Ports))
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		t.Logf("  %s trusted=%v", p.Port, p.Trusted)
	}

	var s Step
	for _, s = range a.Steps {
		t.Logf("step ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

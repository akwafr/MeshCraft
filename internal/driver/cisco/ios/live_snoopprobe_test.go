package ios

// READ-ONLY reconnaissance before the DHCP-snooping write exercise.
//
// The snooping write is not like the storm-control one next door. A storm
// limiter on an empty port polices nothing and risks nothing; `ip dhcp snooping
// vlan <n>` is VLAN-WIDE, and on a VLAN whose clients get their addresses
// through this switch it DROPS DHCP offers arriving on untrusted ports. There is
// no "empty port" version of that.
//
// So the target VLAN has to be chosen from evidence, and this is what gathers
// it: which VLANs exist, which ports are actually up and in which VLAN, and
// whether anything is already snooping. Nothing here writes.
//
//	SNOOP_LIVE_HOST=192.0.2.2 SNOOP_LIVE_USER=… SNOOP_LIVE_PASS=… \
//	SNOOP_LIVE_SOCKS=127.0.0.1:1080 SNOOP_LIVE_SOCKS_USER=… SNOOP_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/cisco/ios -run TestLiveProbeSnooping -v

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

func liveSnoopClient(ctx context.Context, t *testing.T, why string) *Client {
	t.Helper()
	var tgt = driver.Target{Host: os.Getenv("SNOOP_LIVE_HOST")}
	var sa = os.Getenv("SNOOP_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("SNOOP_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("SNOOP_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = liveSocksDialer{d: d}
	}
	var adm, err = (&Driver{}).Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("SNOOP_LIVE_USER"),
		Secret:   os.Getenv("SNOOP_LIVE_PASS"),
		Why:      why,
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	return c
}

func TestLiveProbeSnooping(t *testing.T) {
	if os.Getenv("SNOOP_LIVE_HOST") == "" {
		t.Skip("SNOOP_LIVE_HOST not set")
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var c = liveSnoopClient(ctx, t, "DHCP snooping reconnaissance (read only)")
	defer c.Close()

	// `terminal length 0` FIRST, every time. A --More-- pager truncated a
	// running-config read on the FS S3400 on 2026-08-08 at 34 lines and produced
	// a confident, wrong conclusion; the same trap is live here.
	var _, perr = c.cli.run(ctx, "terminal length 0", cmdTimeout)
	if perr != nil {
		t.Fatalf("terminal length 0: %v", perr)
	}

	var cmd string
	for _, cmd = range []string{
		"show ip dhcp snooping",
		"show vlan brief",
		"show interfaces status",
		"show ip dhcp snooping binding",
	} {
		var out, err = c.cli.run(ctx, cmd, cmdTimeout)
		if err != nil {
			t.Errorf("%s: %v", cmd, err)
			continue
		}
		t.Logf("===== %s =====\n%s", cmd, out)
	}

	var st, serr = c.ReadDHCPSnooping(ctx)
	if serr != nil {
		t.Fatalf("ReadDHCPSnooping: %v", serr)
	}
	t.Logf("parsed: enabled=%v VLANs=%v ports=%d", st.Enabled, st.VLANs, len(st.Ports))
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		t.Logf("  %s trusted=%v", p.Port, p.Trusted)
	}
}

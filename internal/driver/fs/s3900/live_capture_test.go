package s3900

// LIVE CAPTURE harness: dumps the raw output of show commands so real
// captures can be placed under testdata/ instead of shipping parsers written
// from another platform's convention (the gap that let the EdgeSwitch
// GE17/GE18 false drift ship — cf. that package's live_capture_test.go).
// Read-only. Skipped unless S3900_LIVE_HOST is set; writes each capture to
// S3900_LIVE_CAPTURE_DIR when that is set, and t.Logs it either way.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

type socksDialer struct{ d proxy.Dialer }

func (s socksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var cd, ok = s.d.(proxy.ContextDialer)
	if ok {
		return cd.DialContext(ctx, network, addr)
	}
	return s.d.Dial(network, addr)
}

func TestLiveCaptureShowCommands(t *testing.T) {
	var host = os.Getenv("S3900_LIVE_HOST")
	if host == "" {
		t.Skip("S3900_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("S3900_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("S3900_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("S3900_LIVE_SOCKS_PASS")}
		}
		var d, derr = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if derr != nil {
			t.Fatalf("socks: %v", derr)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("S3900_LIVE_USER"), Secret: os.Getenv("S3900_LIVE_PASS"),
		Why: "live capture",
	}})
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	defer c.Close()

	var dir = os.Getenv("S3900_LIVE_CAPTURE_DIR")
	// "show spanning-tree" is the ONE working form on FSOS 1.7.7 (captured
	// 2026-08-05): "… summary" is refused ("% Invalid input"), "… brief"
	// truncates after its banner line, and "… mst configuration" answers
	// nothing at all — the region is not readable on this CLI.
	//
	// DHCP snooping: the command is "show dhcp snooping config", NOT the
	// "show ip dhcp snooping" this connector's header assumed. Established on
	// the lab S3900 (192.0.2.71, FSOS 1.7.7) on 2026-08-08, by exhausting the
	// candidates rather than guessing:
	//
	//	show ip dhcp snooping          → % Invalid input
	//	show ip dhcp-relay snooping    → % Invalid input
	//	show ip dhcp snooping binding  → % Invalid input
	//	show dhcp snooping             → % Incomplete command   ← the prefix EXISTS
	//	show dhcp snooping binding     → an empty binding table
	//	show dhcp snooping config      → the status, the VLAN list and the
	//	                                 per-interface Trusted table
	//
	// "% Incomplete command" on the bare prefix is what turned this from a
	// guess into a search: it says the words are recognised and something is
	// missing, where "% Invalid input" says the family's documented spelling
	// is simply not this firmware's. Both refusals matter, which is why they are
	// written down rather than summarised.
	var cmds = []struct{ cmd, file string }{
		{"show spanning-tree", "show_spanning_tree.txt"},
		{"show dhcp snooping config", "show_dhcp_snooping_config.txt"},
	}
	var i int
	for i = range cmds {
		var out, rerr = c.cli.run(ctx, cmds[i].cmd, cmdTimeout)
		if rerr != nil {
			t.Logf("%q: %v (output %d bytes)", cmds[i].cmd, rerr, len(out))
		}
		t.Logf("=== %s ===\n%s", cmds[i].cmd, out)
		if dir != "" && out != "" {
			var werr = os.WriteFile(filepath.Join(dir, cmds[i].file), []byte(out), 0o644)
			if werr != nil {
				t.Errorf("writing %s: %v", cmds[i].file, werr)
			}
		}
	}
}

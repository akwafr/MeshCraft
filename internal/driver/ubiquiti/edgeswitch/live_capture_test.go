package edgeswitch

// LIVE CAPTURE harness: dumps the raw output of the commands the parsers
// read, so real captures can be placed under testdata/ instead of shipping
// parsers written from the command-echo convention alone (the gap that let
// the GE17/GE18 false drift ship — cf. TODO.md and parse.go's running-config
// section header). Read-only. Skipped unless ES_LIVE_HOST is set; writes each
// capture to ES_LIVE_CAPTURE_DIR when that is set, and t.Logs it either way.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

func TestLiveCaptureShowCommands(t *testing.T) {
	var host = os.Getenv("ES_LIVE_HOST")
	if host == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
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
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: os.Getenv("ES_LIVE_PASS"),
		Why: "live capture",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var dir = os.Getenv("ES_LIVE_CAPTURE_DIR")
	var cmds = map[string]string{
		"show running-config": "show_running_config.txt",
		"show vlan port all":  "show_vlan_port_all.txt",
		// NOT "show port description all": tried 2026-08-05 on firmware
		// 1.9.3, refused with "An invalid interface has been used".

		// Both spanning-tree reads: the summary alone carries NO bridge or
		// root identifier on this platform (captured 2026-08-05, firmware
		// 1.9.3) — those live in the plain "show spanning-tree", which is
		// why ReadSpanningTree runs the pair.
		"show spanning-tree summary": "show_spanning_tree_summary.txt",
		"show spanning-tree":         "show_spanning_tree.txt",
	}
	var cmd, file string
	for cmd, file = range cmds {
		var out, rerr = c.cli.run(ctx, cmd, showTimeout)
		if rerr != nil {
			t.Errorf("%q: %v (output %d bytes)", cmd, rerr, len(out))
		}
		t.Logf("=== %s ===\n%s", cmd, out)
		if dir != "" && out != "" {
			var werr = os.WriteFile(filepath.Join(dir, file), []byte(out), 0o644)
			if werr != nil {
				t.Errorf("writing %s: %v", file, werr)
			}
		}
	}

	// And what the state reader makes of the LIVE device, port by port — the
	// view the controller's read-state would report with this build.
	var st, serr = c.ReadState(ctx)
	if serr != nil {
		t.Fatalf("ReadState: %v", serr)
	}
	var name string
	for name = range st.Ports {
		t.Logf("ReadState %s: %+v", name, st.Ports[name])
	}
}

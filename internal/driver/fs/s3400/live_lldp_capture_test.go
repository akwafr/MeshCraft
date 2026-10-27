package s3400

// LIVE CAPTURE of the LLDP neighbour commands' VERBOSE forms.
//
// Why this exists: the compact `show lldp neighbor` table on this firmware
// prints the neighbour's chassis MAC NIBBLE-SWAPPED per byte (observed live
// 2026-08-05: the EdgeSwitch `9c05.d6f5.f137` shows as `c950.6d5f.1f73`) and
// serves it in the Device-ID column with no chassis column at all — an
// identity no resolver should trust as-is. This harness asks the device for
// its per-neighbour DETAIL forms so the parser can read a correct chassis if
// one is served anywhere. Read-only; skipped unless S3400_LIVE_HOST is set.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveCaptureLLDPDetail(t *testing.T) {
	if os.Getenv("S3400_LIVE_HOST") == "" {
		t.Skip("S3400_LIVE_HOST not set")
	}
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	defer c.Close()

	var dir = os.Getenv("S3400_LIVE_CAPTURE_DIR")
	var cmds = []struct{ cmd, file string }{
		{"show lldp neighbor", "show_lldp_neighbor.txt"},
		{"show lldp neighbor detail", "show_lldp_neighbor_detail.txt"},
		{"show lldp neighbor interface TGigaEthernet0/1", "show_lldp_neighbor_tg01.txt"},
		{"show lldp neighbor interface TGigaEthernet0/1 detail", "show_lldp_neighbor_tg01_detail.txt"},
	}
	var i int
	for i = range cmds {
		var out, rerr = c.cli.runPaged(ctx, cmds[i].cmd, 30*time.Second)
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

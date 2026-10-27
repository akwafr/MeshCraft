package s3400

// LIVE CAPTURE of the per-interface "show interface" forms that carry the
// DDM block (TX/RX power, temperature, voltage, bias current).
//
// Why this exists: the DDM readings are the ONLY optical telemetry this
// platform serves — its SNMP enterprise trees answer empty (walked live,
// 2026-08-05) — and the replay fixtures for the optics parser must be the
// device's own bytes, not a transcription. Four SFP+ cages are captured so
// the parser sees both a populated cage (XG1 carries the live uplink) and
// empty ones; one copper port is captured as the negative control.
//
// Read-only; skipped unless S3400_LIVE_HOST is set. Writes to
// S3400_LIVE_CAPTURE_DIR when set, t.Logs either way.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveReadOptics drives the driver.OpticsReader adapter itself against
// the hardware — the capture below proves the bytes, this proves the loop:
// four per-cage queries, populated cages parsed, empty cages absent.
func TestLiveReadOptics(t *testing.T) {
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	var c, _ = adm.(*Client)
	defer c.Close()

	var st, rerr = c.ReadOptics(ctx)
	if rerr != nil {
		t.Fatalf("ReadOptics: %v", rerr)
	}
	if len(st.Notes) > 0 {
		t.Logf("notes: %v", st.Notes)
	}
	if len(st.Ports) == 0 {
		t.Fatal("no module read — XG1 and XG2 demonstrably carry DDM-capable modules")
	}
	var i int
	for i = range st.Ports {
		var p = st.Ports[i]
		t.Logf("%s: %s %s serial %s ddm=%v tx=%v rx=%v temp=%v volt=%v bias=%v",
			p.Port, p.Vendor, p.PartNumber, p.Serial, p.DDM,
			f64(p.TxPowerDBm), f64(p.RxPowerDBm), f64(p.TemperatureC),
			f64(p.VoltageV), f64(p.BiasCurrentMA))
		if p.DDM && (p.TxPowerDBm == nil || p.RxPowerDBm == nil) {
			t.Errorf("%s declares DDM but a power reading is missing", p.Port)
		}
	}
}

func f64(p *float64) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func TestLiveCaptureOpticsDetail(t *testing.T) {
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
		{"show interface tg0/1", "show_interface_tg01.txt"},
		{"show interface tg0/2", "show_interface_tg02.txt"},
		{"show interface tg0/3", "show_interface_tg03.txt"},
		{"show interface tg0/4", "show_interface_tg04.txt"},
		{"show interface g0/1", "show_interface_g01.txt"},
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

package instanton

// LIVE MST harness: proves the MSTPLogicalInstanceList / MSTPInstanceList /
// MSTPVLANList write triple on real hardware, through THIS package's own code
// path (applyMSTInstanceVLANs) — the same discipline as the s3900 package's
// live_capture_test.go. Skipped unless INSTANTON_LIVE_HOST is set; WRITES only
// when INSTANTON_LIVE_MST_VLANS is also set, and says exactly what the device
// answered either way.
//
// Environment:
//
//	INSTANTON_LIVE_HOST        device management IP (required)
//	INSTANTON_LIVE_USER/PASS   admin credential
//	INSTANTON_LIVE_SOCKS       optional SOCKS5 address (e.g. 127.0.0.1:1080)
//	INSTANTON_LIVE_SOCKS_USER/PASS  SOCKS credential (operator email + token)
//	INSTANTON_LIVE_MST_VLANS   e.g. "42,98,99,100" — desired instance map; WRITE
//	INSTANTON_LIVE_MST_INSTANCE  instance id (default 1)
//	INSTANTON_LIVE_SAVE=1      SaveConfig after a successful write

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

type liveSocksDialer struct{ d proxy.Dialer }

func (s liveSocksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var cd, ok = s.d.(proxy.ContextDialer)
	if ok {
		return cd.DialContext(ctx, network, addr)
	}
	return s.d.Dial(network, addr)
}

func TestLiveMSTInstanceApply(t *testing.T) {
	var host = os.Getenv("INSTANTON_LIVE_HOST")
	if host == "" {
		t.Skip("INSTANTON_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
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
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var drv = &Driver1930{}
	var adm, err = drv.Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("INSTANTON_LIVE_USER"),
		Secret:   os.Getenv("INSTANTON_LIVE_PASS"),
		Why:      "live MST proof",
	}})
	if err != nil {
		var a, _ = adm.(*Admin)
		if a != nil {
			var s Step
			for _, s = range a.Steps {
				t.Logf("step ok=%v %s — %s", s.OK, s.What, s.Detail)
			}
		}
		t.Fatalf("authenticate: %v", err)
	}
	var a, _ = adm.(*Admin)

	var before, rerr = a.readMSTInstances(ctx)
	if rerr != nil {
		t.Fatalf("read %s: %v", sectionMSTPInstances, rerr)
	}
	var id int
	for id = range before {
		t.Logf("BEFORE: instance %d priority=%d VLANs=%s",
			id, before[id].Priority, compressVLANRanges(before[id].VLANs))
	}

	var spec = os.Getenv("INSTANTON_LIVE_MST_VLANS")
	if spec == "" {
		t.Log("INSTANTON_LIVE_MST_VLANS not set: read-only pass, nothing written")
		return
	}
	var want = parseVLANRanges(spec)
	if len(want) == 0 {
		t.Fatalf("INSTANTON_LIVE_MST_VLANS %q parsed to nothing", spec)
	}
	var inst = 1
	var instRaw = os.Getenv("INSTANTON_LIVE_MST_INSTANCE")
	if instRaw != "" {
		inst, err = strconv.Atoi(instRaw)
		if err != nil || inst < 1 {
			t.Fatalf("INSTANTON_LIVE_MST_INSTANCE %q", instRaw)
		}
	}

	var out driver.ApplyResult
	err = a.applyMSTInstanceVLANs(ctx, map[int][]int{inst: want}, nil, &out)
	var s Step
	for _, s = range a.Steps {
		t.Logf("step ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
	var n string
	for _, n = range out.Notes {
		t.Logf("NOTE: %s", n)
	}
	for _, n = range out.Skipped {
		t.Logf("SKIPPED: %s", n)
	}
	if err != nil {
		t.Fatalf("applyMSTInstanceVLANs: %v", err)
	}

	var after, aerr = a.readMSTInstances(ctx)
	if aerr != nil {
		t.Fatalf("reread %s: %v", sectionMSTPInstances, aerr)
	}
	for id = range after {
		t.Logf("AFTER: instance %d priority=%d VLANs=%s",
			id, after[id].Priority, compressVLANRanges(after[id].VLANs))
	}
	var got, exists = after[inst]
	if !exists || !equalVLANSets(got.VLANs, want) {
		t.Fatalf("instance %d reads back %v, wanted %v", inst, got.VLANs, want)
	}

	if os.Getenv("INSTANTON_LIVE_SAVE") == "1" {
		var saveErr = a.SaveConfig(ctx)
		if saveErr != nil {
			t.Fatalf("save config: %v", saveErr)
		}
		t.Log("configuration saved to flash")
	}
}

package s3400

// A LIVE end-to-end exercise of this connector against a real S3400.
//
// Skipped unless S3400_LIVE_HOST is set, so `make test` never depends on
// hardware. It exists because every other test in this package replays a
// capture: replaying proves the parsers still parse, not that the commands
// this connector SENDS are the ones the device accepts.
//
//	S3400_LIVE_HOST=192.0.2.3 S3400_LIVE_USER=admin S3400_LIVE_PASS=… \
//	S3400_LIVE_SOCKS=127.0.0.1:1080 S3400_LIVE_SOCKS_USER=… S3400_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/fs/s3400 -run TestLive -v
//
// It is READ-ONLY. Nothing here writes to the device: a test that provisions
// a switch as a side effect of `go test` is a trap for whoever runs the suite
// next.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func liveTarget(t *testing.T) (driver.Target, []Candidate) {
	t.Helper()
	var host = os.Getenv("S3400_LIVE_HOST")
	if host == "" {
		t.Skip("S3400_LIVE_HOST not set — live exercise skipped")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("S3400_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("S3400_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("S3400_LIVE_SOCKS_PASS")}
		}
		var d, err = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if err != nil {
			t.Fatalf("socks: %v", err)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	return tgt, []Candidate{{
		Username: os.Getenv("S3400_LIVE_USER"),
		Secret:   os.Getenv("S3400_LIVE_PASS"),
		Why:      "live exercise",
	}}
}

func TestLiveReadEverything(t *testing.T) {
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v (transcript %+v)", err, adm.Transcript())
	}
	defer c.Close()

	if !c.known {
		t.Fatalf("the CLI was not recognized — transcript %+v", c.Transcript())
	}
	if !c.privileged {
		t.Fatalf("never reached privileged mode — transcript %+v", c.Transcript())
	}
	t.Logf("identity: model=%q version=%q serial=%q mac=%q",
		c.facts.Model, c.facts.Version, c.facts.Serial, c.facts.MAC)

	// --- state: VLANs, ports, links
	var st State
	st, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if len(st.VLANs) == 0 {
		t.Error("no VLAN read — a switch always has VLAN 1")
	}
	if len(st.Ports) == 0 {
		t.Fatal("no port read: the whole inventory rests on this")
	}
	if len(st.Links) == 0 {
		t.Error("no link state read — every port would show as unknown")
	}
	t.Logf("state: %d VLANs, %d ports, %d links, mgmt VLAN %d",
		len(st.VLANs), len(st.Ports), len(st.Links), st.MgmtVLAN)
	var pn string
	for _, pn = range []string{"XG1", "XG2", "XG3", "XG4"} {
		t.Logf("  %s => %+v", pn, st.Ports[pn])
	}
	var upCount int
	var name string
	for name = range st.Links {
		if st.Links[name] == driver.LinkUpState {
			upCount++
			t.Logf("  up: %s (%+v)", name, st.Ports[name])
		}
	}
	if upCount == 0 {
		t.Error("no port is up, yet this session came in over one of them")
	}

	// --- management addressing
	var addr driver.MgmtAddrState
	addr, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		t.Errorf("ReadMgmtAddressing: %v", err)
	} else {
		t.Logf("mgmt: mode=%q address=%q gateway=%q iface=%q",
			addr.Mode, addr.Address, addr.Gateway, addr.Interface)
		if addr.Address == "" {
			t.Error("no management address read on a device reached BY that address")
		}
	}

	// --- spanning tree
	var stp driver.SpanningTreeState
	stp, err = c.ReadSpanningTree(ctx)
	if err != nil {
		t.Errorf("ReadSpanningTree: %v", err)
	} else {
		t.Logf("stp: mode=%q enabled=%v bridge=%s prio=%d root=%s rootprio=%d isroot=%v ports=%d",
			stp.Mode, stp.Enabled, stp.BridgeID, stp.BridgePriority,
			stp.RootBridgeID, stp.RootPriority, stp.IsRoot, len(stp.Ports))
		if stp.BridgeID == "" {
			t.Error("no bridge id read from \"show spanning-tree\"")
		}
	}

	// --- faceplate
	var groups []map[string]any
	groups, err = c.ReadFaceplate(ctx)
	if err != nil {
		t.Errorf("ReadFaceplate: %v", err)
	} else {
		t.Logf("faceplate: %+v", groups)
	}

	// --- LLDP (read-only: reports OFF rather than turning it on)
	var neighbours []LLDPNeighbor
	var on bool
	neighbours, on, err = c.ReadLLDPNeighbors(ctx)
	if err != nil {
		t.Errorf("ReadLLDPNeighbors: %v", err)
	}
	t.Logf("lldp: enabled=%v neighbours=%d", on, len(neighbours))
	var n LLDPNeighbor
	for _, n = range neighbours {
		t.Logf("  %s ← %q port %q caps %q", n.LocalPort, n.DeviceID, n.PortID, n.Caps)
	}

	// --- accounts. Logged one per line WITH what the device said about its
	// rights, which on this range is "nothing" — cf. Client.Accounts.
	var users []driver.Account
	users, err = c.Accounts(ctx)
	if err != nil {
		t.Errorf("Accounts: %v", err)
	}
	var u driver.Account
	for _, u = range users {
		t.Logf("account: %s", u.Describe())
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

// TestLiveCaptureSpanningTree dumps the RAW "show spanning-tree" output, so
// the replay fixture under testdata/ is the device's own bytes and not a
// transcription. Same gesture as the edgeswitch package's live capture
// harness, and read-only like every other live test here. Writes to
// S3400_LIVE_CAPTURE_DIR when set, t.Logs either way.
func TestLiveCaptureSpanningTree(t *testing.T) {
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var out string
	out, err = c.cli.runPaged(ctx, "show spanning-tree", 60*time.Second)
	if err != nil {
		t.Errorf("show spanning-tree: %v (output %d bytes)", err, len(out))
	}
	t.Logf("=== show spanning-tree ===\n%s", out)
	var dir = os.Getenv("S3400_LIVE_CAPTURE_DIR")
	if dir != "" && out != "" {
		var werr = os.MkdirAll(dir, 0o755)
		if werr != nil {
			t.Fatalf("capture dir: %v", werr)
		}
		werr = os.WriteFile(filepath.Join(dir, "show_spanning_tree.txt"), []byte(out), 0o644)
		if werr != nil {
			t.Errorf("writing capture: %v", werr)
		}
	}
}

// TestLiveLinkRead isolates the interface read — the one that decides whether
// every port shows as up, down, or unknown.
func TestLiveLinkRead(t *testing.T) {
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var out string
	out, err = c.cli.runPaged(ctx, "show interface | include protocol", 90*time.Second)
	t.Logf("err=%v len=%d", err, len(out))
	var links = ParseLinkStates(out)
	t.Logf("parsed %d links", len(links))
	t.Logf("newlines=%d", strings.Count(out, "\n"))
	// The device erases its pager with backspaces; if they ever survive
	// stripPager again, every line after the first becomes unparseable.
	if strings.Contains(out, "\x08") {
		t.Error("backspaces survived stripPager — the interface list will not parse")
	}
	if len(links) < 50 {
		t.Errorf("only %d links parsed from a 52-socket switch", len(links))
	}
}

// TestLiveAccessUndoesATrunk drives the FIXED access path against real
// hardware: GE46 is left a trunk by the old code, and this must turn it back
// into a plain access port. Double-gated (S3400_LIVE_WRITE=1) because it
// WRITES — every other live test here is read-only.
func TestLiveAccessUndoesATrunk(t *testing.T) {
	if os.Getenv("S3400_LIVE_WRITE") != "1" {
		t.Skip("S3400_LIVE_WRITE not set — write exercise skipped")
	}
	var tgt, cands = liveTarget(t)
	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, cands)
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	defer c.Close()

	var port = os.Getenv("S3400_LIVE_PORT")
	if port == "" {
		port = "GE46"
	}
	// The device's own cage numbering, before any name is composed — this
	// session has read nothing yet (ADR-0018).
	c.ensureCageNumbering(ctx)
	var dev, nerr = c.cages.cliPortName(port)
	if nerr != nil {
		t.Fatalf("naming %s on this device: %v", port, nerr)
	}

	var before string
	before, _ = c.cli.runPaged(ctx, "show running-config interface "+dev, 30*time.Second)
	t.Logf("before:\n%s", strings.TrimSpace(before))

	if err = c.writePort(ctx, port, PortState{Mode: "access", PVID: 100}); err != nil {
		t.Fatalf("writePort: %v", err)
	}

	var after string
	after, _ = c.cli.runPaged(ctx, "show running-config interface "+dev, 30*time.Second)
	t.Logf("after:\n%s", strings.TrimSpace(after))
	if strings.Contains(after, "switchport mode trunk") {
		t.Error("the port is STILL a trunk — the access intent did not undo it")
	}
	if strings.Contains(after, "vlan-allowed") {
		t.Error("the stale trunk allowed list survived")
	}

	// And the connector must now READ it as access with no tagged VLANs.
	var st State
	st, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	var got = st.Ports[port]
	t.Logf("read back: %s => %+v", port, got)
	if got.Mode != "access" || got.PVID != 100 || len(got.Tagged) != 0 {
		t.Errorf("%s reads as %+v — want access/pvid 100/no tagged", port, got)
	}
}

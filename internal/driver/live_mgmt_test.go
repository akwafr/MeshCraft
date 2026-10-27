package driver_test

// A LIVE exercise of [driver.MgmtAddrWriter] against real switches.
//
// It exists because of a rule this project states in the interface's own
// doc-comment: "Declaring this interface without having exercised the writes on
// hardware is forbidden: flip MgmtCapabilities.ApplySameVLAN only after proof."
// This is that proof, and it is repeatable rather than a story in a commit
// message.
//
// It is IDEMPOTENT ON PURPOSE. It reads the address the device already carries
// and writes back exactly that — same address, same mask, same gateway. That
// exercises the whole command path (interface selection, CIDR→dotted-mask
// conversion, the config block, the re-read) with nothing to lose if a form is
// wrong: the worst case is the device keeps the address it already had. A test
// that moved a switch to a new subnet would be a better proof and a far worse
// idea, since the session it travels on is the one that would disappear.
//
//	MGMT_LIVE_HOST=192.0.2.3 MGMT_LIVE_DRIVER=fs-s3400 \
//	MGMT_LIVE_USER=admin MGMT_LIVE_PASS=… \
//	MGMT_LIVE_SOCKS=127.0.0.1:1080 MGMT_LIVE_SOCKS_USER=… MGMT_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver -run TestLiveMgmt -v

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"

	_ "meshcraft/internal/driver/cisco/ios"
	_ "meshcraft/internal/driver/fs/s3400"
	_ "meshcraft/internal/driver/fs/s3900"
)

type socksCtxDialer struct{ d proxy.Dialer }

func (s socksCtxDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var cd, ok = s.d.(proxy.ContextDialer)
	if ok {
		return cd.DialContext(ctx, network, addr)
	}
	return s.d.Dial(network, addr)
}

func TestLiveMgmtAddressingIsIdempotent(t *testing.T) {
	var host = os.Getenv("MGMT_LIVE_HOST")
	if host == "" {
		t.Skip("MGMT_LIVE_HOST not set — live exercise skipped")
	}
	var name = os.Getenv("MGMT_LIVE_DRIVER")
	var drv, known = driver.Get(name)
	if !known {
		t.Fatalf("driver %q is not registered", name)
	}
	var conn, isConn = drv.(driver.Connector)
	if !isConn {
		t.Fatalf("driver %q is not a connector", name)
	}

	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("MGMT_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("MGMT_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("MGMT_LIVE_SOCKS_PASS")}
		}
		var d, err = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if err != nil {
			t.Fatalf("socks: %v", err)
		}
		tgt.Dialer = socksCtxDialer{d: d}
	}

	var ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var adm, err = conn.Authenticate(ctx, tgt, []driver.Candidate{{
		Username: os.Getenv("MGMT_LIVE_USER"),
		Secret:   os.Getenv("MGMT_LIVE_PASS"),
		Why:      "live management-addressing exercise",
	}})
	if err != nil {
		t.Fatalf("authenticate: %v — transcript %+v", err, adm.Transcript())
	}
	var closer, canClose = adm.(interface{ Close() })
	if canClose {
		defer closer.Close()
	}

	var reader, canRead = driver.AsMgmtAddrReader(adm)
	if !canRead {
		t.Fatalf("%s declares no MgmtAddrReader", name)
	}
	var writer, canWrite = driver.AsMgmtAddrWriter(adm)
	if !canWrite {
		t.Fatalf("%s declares no MgmtAddrWriter", name)
	}

	var before driver.MgmtAddrState
	before, err = reader.ReadMgmtAddressing(ctx)
	if err != nil {
		t.Fatalf("ReadMgmtAddressing: %v", err)
	}
	t.Logf("before: mode=%q address=%q gateway=%q interface=%q",
		before.Mode, before.Address, before.Gateway, before.Interface)
	if before.Address == "" || before.Interface == "" {
		t.Fatalf("nothing to re-apply: the device reports no management address")
	}

	// Write back EXACTLY what is already there.
	var want = driver.MgmtAddrIntent{
		Mode:    before.Mode,
		Address: before.Address,
		Gateway: before.Gateway,
	}
	if want.Mode == "" {
		want.Mode = driver.MgmtAddrModeStatic
	}

	var res driver.MgmtAddrResult
	res, err = writer.ApplyMgmtAddressing(ctx, want)
	if err != nil {
		t.Fatalf("ApplyMgmtAddressing: %v", err)
	}
	var n string
	for _, n = range res.Notes {
		t.Logf("note: %s", n)
	}

	// The session must have survived (the address did not actually move), and
	// the device must still report the same addressing.
	var after driver.MgmtAddrState
	after, err = reader.ReadMgmtAddressing(ctx)
	if err != nil {
		t.Fatalf("re-read after write: %v — the session did not survive an "+
			"idempotent re-apply, which means the write disturbed the address", err)
	}
	t.Logf("after:  mode=%q address=%q gateway=%q interface=%q",
		after.Mode, after.Address, after.Gateway, after.Interface)

	if after.Address != before.Address {
		t.Errorf("address changed by an idempotent re-apply: %q -> %q",
			before.Address, after.Address)
	}
	if after.Interface != before.Interface {
		t.Errorf("management interface moved: %q -> %q", before.Interface, after.Interface)
	}
	if before.Gateway != "" && after.Gateway != before.Gateway {
		t.Errorf("gateway changed: %q -> %q", before.Gateway, after.Gateway)
	}

	var s driver.Step
	for _, s = range adm.Transcript() {
		t.Logf("transcript: ok=%v %s", s.OK, s.What)
	}
}

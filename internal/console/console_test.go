package console

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// A REAL SSH SERVER, in process.
//
// The alternative was a mock, and it would have proven nothing that matters
// here: what can go wrong in this package is the handshake, the pty request, the
// shell channel, and — the one that costs an account lockout — advancing through
// the credential list at the wrong moment. None of those are visible against a
// fake. This server is thirty lines and exercises all of them.

type testServer struct {
	ln       net.Listener
	hostKey  ssh.Signer
	accept   map[string]string // username → password that works
	mu       sync.Mutex
	attempts []string // "user:pass" in the order presented
	// refusePty makes the server reject a pty request, which is how an account
	// without exec rights behaves.
	refusePty bool
	wg        sync.WaitGroup
}

func newTestServer(t *testing.T, accept map[string]string) *testServer {
	t.Helper()
	var _, priv, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var signer ssh.Signer
	signer, err = ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var ln net.Listener
	ln, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var s = &testServer{ln: ln, hostKey: signer, accept: accept}
	go s.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		s.wg.Wait()
	})
	return s
}

func (s *testServer) addr() (string, int) {
	var a = s.ln.Addr().(*net.TCPAddr)
	return a.IP.String(), a.Port
}

func (s *testServer) tried() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.attempts...)
}

func (s *testServer) serve() {
	for {
		var conn, err = s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

func (s *testServer) handle(nc net.Conn) {
	defer nc.Close() //nolint:errcheck
	var cfg = &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			s.mu.Lock()
			s.attempts = append(s.attempts, c.User()+":"+string(pass))
			s.mu.Unlock()
			var want, known = s.accept[c.User()]
			if known && want == string(pass) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(s.hostKey)

	var _, chans, reqs, err = ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	var newChan ssh.NewChannel
	for newChan = range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		var ch, chReqs, aerr = newChan.Accept()
		if aerr != nil {
			return
		}
		go s.session(ch, chReqs)
	}
}

// session answers the pty and shell requests, then echoes what it is given —
// enough to prove bytes travel in both directions.
func (s *testServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close() //nolint:errcheck
	var req *ssh.Request
	for req = range reqs {
		switch req.Type {
		case "pty-req":
			_ = req.Reply(!s.refusePty, nil)
		case "shell":
			_ = req.Reply(true, nil)
			_, _ = io.WriteString(ch, "Switch#")
			go func() {
				var buf = make([]byte, 256)
				for {
					var n, rerr = ch.Read(buf)
					if n > 0 {
						_, _ = ch.Write(buf[:n])
					}
					if rerr != nil {
						return
					}
				}
			}()
		case "window-change":
			_ = req.Reply(true, nil)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

func optsFor(s *testServer) Options {
	var host, port = s.addr()
	return Options{Host: host, Port: port, Cols: 100, Rows: 30}
}

func TestOpenAcceptsFirstWorkingCredential(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var sess, err = Open(context.Background(), optsFor(srv), []Credential{
		{Username: "admin", Secret: "good", Why: "generation 2"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close() //nolint:errcheck

	if sess.Accepted.Why != "generation 2" {
		t.Errorf("Accepted.Why = %q, want the caller's reason back", sess.Accepted.Why)
	}
	// The prompt proves pty + shell + a readable stream.
	var buf = make([]byte, 16)
	_ = deadlineRead(t, sess, buf)
	if !strings.Contains(string(buf), "Switch#") {
		t.Errorf("first read = %q, want the device prompt", string(buf))
	}
}

// THE ORDER IS THE CALLER'S, and refusals advance one at a time. A regression
// here does not fail loudly — it locks accounts in the field.
func TestOpenWalksCredentialsInOrder(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "third"})
	var sess, err = Open(context.Background(), optsFor(srv), []Credential{
		{Username: "admin", Secret: "first"},
		{Username: "admin", Secret: "second"},
		{Username: "admin", Secret: "third"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer sess.Close() //nolint:errcheck

	var want = []string{"admin:first", "admin:second", "admin:third"}
	var got = srv.tried()
	if len(got) != len(want) {
		t.Fatalf("presented %v, want exactly %v", got, want)
	}
	var i int
	for i = range want {
		if got[i] != want[i] {
			t.Errorf("attempt %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOpenReportsNoCredentialAccepted(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var _, err = Open(context.Background(), optsFor(srv), []Credential{
		{Username: "admin", Secret: "wrong"},
	})
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential so the UI can point at the credentials page", err)
	}
}

// A DEVICE THAT DOES NOT ANSWER MUST NOT COST THE WHOLE BUDGET. This is the
// distinction that keeps a WAN outage from locking every account meshcraft
// holds: only an authentication refusal advances the list.
func TestOpenDoesNotWalkCredentialsOnTransportFailure(t *testing.T) {
	// A port nobody listens on: the TCP open fails, which is not an auth failure.
	var opts = Options{Host: "127.0.0.1", Port: 1, Cols: 80, Rows: 24}
	var _, err = Open(context.Background(), opts, []Credential{
		{Username: "admin", Secret: "a"},
		{Username: "admin", Secret: "b"},
		{Username: "admin", Secret: "c"},
	})
	if err == nil {
		t.Fatal("expected a transport failure")
	}
	if errors.Is(err, ErrNoCredential) {
		t.Fatalf("a device that never answered was reported as refusing credentials: %v", err)
	}
	if !strings.Contains(err.Error(), "TCP") {
		t.Errorf("err = %v, want the transport named so nobody hunts a password", err)
	}
}

func TestOpenRefusesWithoutCredentials(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var _, err = Open(context.Background(), optsFor(srv), nil)
	if err == nil {
		t.Fatal("expected a refusal when meshcraft holds no secret")
	}
	if len(srv.tried()) != 0 {
		t.Error("the device was contacted with nothing to present")
	}
}

// A pty refusal is NOT an authentication problem and must not advance the list:
// the login already worked, and trying the next password would be nonsense.
func TestOpenDoesNotRetryWhenTheShellIsRefused(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	srv.refusePty = true
	var _, err = Open(context.Background(), optsFor(srv), []Credential{
		{Username: "admin", Secret: "good"},
		{Username: "admin", Secret: "other"},
	})
	if err == nil {
		t.Fatal("expected the pty refusal to surface")
	}
	if errors.Is(err, ErrNoCredential) {
		t.Errorf("a refused pty was reported as a credential problem: %v", err)
	}
	var tried = len(srv.tried())
	if tried != 1 {
		t.Errorf("presented %d credentials, want 1: the login had already worked", tried)
	}
}

func TestResizeIsAcceptedOnAnOpenSession(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var sess, err = Open(context.Background(), optsFor(srv),
		[]Credential{{Username: "admin", Secret: "good"}})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close() //nolint:errcheck
	if err = sess.Resize(200, 50); err != nil {
		t.Errorf("Resize: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	var srv = newTestServer(t, map[string]string{"admin": "good"})
	var sess, err = Open(context.Background(), optsFor(srv),
		[]Credential{{Username: "admin", Secret: "good"}})
	if err != nil {
		t.Fatal(err)
	}
	// The relay closes on the client going away, on the idle timer and on the
	// device hanging up, and at least two of those routinely land together.
	_ = sess.Close()
	_ = sess.Close()
	_ = sess.Close()
}

func TestClampWindow(t *testing.T) {
	var cases = []struct{ inC, inR, wantC, wantR int }{
		// xterm.js reports zero while the overlay is still laying out, and a
		// zero-column pty makes some IOS images wrap every line onto itself.
		{0, 0, DefaultCols, DefaultRows},
		{-5, -5, DefaultCols, DefaultRows},
		{1, 1, MinCols, MinRows},
		{100000, 100000, MaxCols, MaxRows},
		{100, 30, 100, 30},
	}
	var c struct{ inC, inR, wantC, wantR int }
	for _, c = range cases {
		var gotC, gotR = ClampWindow(c.inC, c.inR)
		if gotC != c.wantC || gotR != c.wantR {
			t.Errorf("ClampWindow(%d,%d) = %d,%d want %d,%d",
				c.inC, c.inR, gotC, gotR, c.wantC, c.wantR)
		}
	}
}

func TestIsAuthFailureFailsClosed(t *testing.T) {
	// Recognized refusals advance the credential list.
	if !IsAuthFailure(errors.New("ssh: unable to authenticate, attempted methods [none password]")) {
		t.Error("a plain authentication refusal was not recognized")
	}
	if !IsAuthFailure(errors.New("Permission denied (publickey,password)")) {
		t.Error("permission denied was not recognized")
	}
	// EVERYTHING ELSE STOPS THE LOOP. Failing closed is the safe direction: an
	// unrecognized error must not walk the list against an unreachable switch.
	if IsAuthFailure(errors.New("dial tcp 10.0.0.1:22: i/o timeout")) {
		t.Error("a timeout was treated as a wrong password")
	}
	if IsAuthFailure(errors.New("ssh: handshake failed: no common algorithm")) {
		t.Error("an algorithm mismatch was treated as a wrong password")
	}
	if IsAuthFailure(nil) {
		t.Error("nil is not a refusal")
	}
}

// The field algorithms are the reason a console can open on the range this fleet
// is full of. A current OpenSSH default list refuses every one of them.
func TestClientConfigCarriesTheFieldAlgorithms(t *testing.T) {
	var cfg = ClientConfig(Credential{Username: "u", Secret: "p"})
	var needKex = []string{"diffie-hellman-group14-sha1", "diffie-hellman-group1-sha1"}
	var needCipher = []string{"aes128-cbc", "3des-cbc"}
	var needHostKey = []string{"ssh-rsa", "ssh-dss"}
	var want string
	for _, want = range needKex {
		if !contains(cfg.KeyExchanges, want) {
			t.Errorf("key exchange %q missing: IOS 15.2 offers nothing newer", want)
		}
	}
	for _, want = range needCipher {
		if !contains(cfg.Ciphers, want) {
			t.Errorf("cipher %q missing", want)
		}
	}
	for _, want = range needHostKey {
		if !contains(cfg.HostKeyAlgorithms, want) {
			t.Errorf("host key algorithm %q missing", want)
		}
	}
}

func contains(list []string, want string) bool {
	var v string
	for _, v = range list {
		if v == want {
			return true
		}
	}
	return false
}

func deadlineRead(t *testing.T, sess *Session, buf []byte) int {
	t.Helper()
	var done = make(chan int, 1)
	go func() {
		var n, _ = sess.Read(buf)
		done <- n
	}()
	var n int
	select {
	case n = <-done:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("no output from the device within 5s")
		return 0
	}
}

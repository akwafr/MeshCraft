// Package console opens an INTERACTIVE shell on a device and hands it to a
// caller as a byte stream.
//
// # Why this is not part of internal/driver
//
// The driver contract is deliberately about RESULTS — read the state, set VLAN
// membership, change a secret — and never about the gesture by which a platform
// obtains them (cf. driver/connector.go). An interactive console is the exact
// opposite: it is a gesture with no result, a raw pipe where meshcraft has no
// opinion about what the operator types. Modelling it as a connector capability
// would have put "give me a shell" next to "apply this port plan", and the two
// have nothing in common except the device they land on.
//
// It also has no business inside internal/api: the relay is testable on its own,
// and a package that imports neither chi nor the store can be exercised against a
// real in-process SSH server (cf. console_test.go) instead of a mocked one.
//
// # What this package guarantees, and what it deliberately does not
//
// It guarantees the transport: TCP through the caller's [Dialer] — the proxy
// tunnel, therefore the confidentiality boundary and site coverage — the SSH
// handshake with the algorithms of THE FIELD, a pty, and a shell. It guarantees
// that closing the session closes everything under it.
//
// It does NOT guarantee anything about the CONTENT. Nothing here parses prompts,
// recognizes refusals, or strips syslog. That is the connectors' job and they do
// it well (cf. cisco/ios/ssh.go); doing it here would mean a console that
// silently edits what the switch said, which is the one thing an operator opening
// a console must be able to trust it does not do.
package console

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Dialer is the already-established transport. Same shape as driver.Dialer and
// discovery.Dialer, redeclared rather than imported: this package must not pull
// the driver registry in just to name one method.
//
// Nil means "from this machine", which only the Local proxy justifies.
type Dialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

// Credential is one secret to try, with the reason for its rank.
//
// `Why` is not decoration: it is what the audit line and the UI show once the
// session is open ("generation 2, observed" versus "the factory account"), and on
// a console — where whatever happens next is attributed to that account — it is
// the only record of WHICH identity the operator was actually given.
type Credential struct {
	Username string
	Secret   string
	Why      string
}

const (
	// DefaultPort: no platform in the fleet moves SSH, and a caller that needs to
	// is expected to say so rather than have this package guess.
	DefaultPort = 22
	// DialTimeout bounds the TCP open and the SSH handshake separately.
	//
	// Generous on purpose: these devices sit behind a WAN and a yamux stream, and
	// a Catalyst negotiating a group14 key exchange on a 2960's CPU is not fast.
	DialTimeout = 12 * time.Second

	// Window bounds. A pty is sized by the browser, i.e. by a number this package
	// did not choose and must not trust: xterm.js reports 0 during layout, and a
	// zero-column pty makes some IOS images wrap every line onto itself.
	MinCols, MinRows = 20, 4
	MaxCols, MaxRows = 500, 200
	DefaultCols      = 120
	DefaultRows      = 32
)

// ErrNoCredential: every candidate was refused by the device.
//
// A distinct sentinel because the repair is distinct: this is not "the switch is
// down" and not "the proxy is gone" — it is a password meshcraft holds that the
// device no longer accepts, and the screen must send the operator to the
// credentials page rather than to the equipment rack.
var ErrNoCredential = errors.New("no credential was accepted by the device")

// Options describes the device to reach. Credentials come separately: they are
// an ORDERED BUDGET, not a setting (cf. [Open]).
type Options struct {
	Host string
	Port int
	// Dialer is the transport. Nil = from the controller itself.
	Dialer Dialer
	// Term is the terminal type announced to the device. "vt100" is what every
	// platform in the fleet understands; "xterm" makes some IOS images emit
	// escape sequences their own pager then fails to clean up.
	Term string
	Cols int
	Rows int
}

// Session is an open interactive shell.
//
// Read and Write are the device's stdout and stdin. They are safe to use from
// two goroutines — one pumping each way — which is exactly what a relay does; they
// are NOT safe to use from two goroutines in the SAME direction, and no caller
// needs that.
type Session struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	stdout  io.Reader

	// Accepted is the credential that actually opened the device.
	Accepted Credential

	// closeOnce guards teardown: the relay closes on the client going away, on
	// the idle timer, and on the device hanging up, and at least two of those
	// routinely happen within the same millisecond.
	closeOnce sync.Once
	closeErr  error
}

func (s *Session) Read(p []byte) (int, error)  { return s.stdout.Read(p) }
func (s *Session) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Resize retells the device how big the terminal is.
//
// Best-effort BY CONTRACT, and the caller must treat a failure as cosmetic: a
// platform that refuses the window-change request still has a working shell, and
// tearing down a console the operator is typing into because a resize was
// refused would be a far worse outcome than a misaligned `show` table.
func (s *Session) Resize(cols, rows int) error {
	if s.session == nil {
		return errors.New("session closed")
	}
	var c, r = ClampWindow(cols, rows)
	return s.session.WindowChange(r, c)
}

// Close tears the whole stack down: shell, SSH client, and — because the client
// owns it — the tunnel stream underneath.
//
// The `exit` written first is not politeness. On a Catalyst, dropping the TCP
// connection leaves the vty allocated until its exec-timeout fires, and a 2960
// has sixteen of them: an operator who opens and abandons a dozen consoles in an
// afternoon locks themselves out of the switch. Writing `exit` lets the device
// reap the line immediately. It is best-effort — a device that already hung up
// will refuse the write, and that is fine.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		if s.stdin != nil {
			_, _ = io.WriteString(s.stdin, "exit\r")
			_ = s.stdin.Close()
		}
		if s.session != nil {
			_ = s.session.Close()
		}
		if s.client != nil {
			s.closeErr = s.client.Close()
		}
	})
	return s.closeErr
}

// ClampWindow forces a browser-reported window into something a pty accepts.
//
// Exported because the relay clamps again on every resize message, and a bound
// applied in only one of the two places is not a bound.
func ClampWindow(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = DefaultCols
	}
	if rows <= 0 {
		rows = DefaultRows
	}
	if cols < MinCols {
		cols = MinCols
	}
	if cols > MaxCols {
		cols = MaxCols
	}
	if rows < MinRows {
		rows = MinRows
	}
	if rows > MaxRows {
		rows = MaxRows
	}
	return cols, rows
}

// Open dials the device and returns an open shell.
//
// CREDENTIALS ARE TRIED IN ORDER AND THE ORDER IS THE CALLER'S. It is computed by
// the database (observed generation first, older generations next, the factory
// account last) and this package never reorders it, never retries one, and never
// invents one. That restraint is the same one the driver contract imposes on
// connectors, and for the same reason: these ranges LOCK ACCOUNTS OUT, and on a
// device with neither a serial port nor a second path, exhausting the attempts
// closes the only door.
//
// THE LOOP ADVANCES ONLY ON AN AUTHENTICATION REFUSAL. A TCP failure, a
// handshake failure or a dead tunnel abort immediately with that error: retrying
// the next credential against a switch that is simply unreachable would burn the
// whole budget on a fault that has nothing to do with passwords, and would report
// "no credential accepted" about a device that never answered.
func Open(ctx context.Context, opts Options, creds []Credential) (*Session, error) {
	if strings.TrimSpace(opts.Host) == "" {
		return nil, errors.New("no management address for this device")
	}
	if len(creds) == 0 {
		return nil, errors.New("no credential to present: meshcraft holds no secret for this device")
	}
	if opts.Port <= 0 {
		opts.Port = DefaultPort
	}
	if opts.Term == "" {
		opts.Term = "vt100"
	}

	var lastAuthErr error
	var cred Credential
	for _, cred = range creds {
		var client, err = dial(ctx, opts, cred)
		if err != nil {
			if IsAuthFailure(err) {
				lastAuthErr = err
				continue
			}
			return nil, err
		}
		var sess *Session
		sess, err = openShell(client, opts)
		if err != nil {
			_ = client.Close()
			// A shell refused on a proven login is NOT an authentication problem:
			// it is an account without exec rights, or an image that only grants
			// `exec` channels. Advancing to the next password would be nonsense.
			return nil, err
		}
		sess.Accepted = cred
		return sess, nil
	}
	if lastAuthErr != nil {
		return nil, fmt.Errorf("%w (%d tried, last refusal: %v)",
			ErrNoCredential, len(creds), firstLine(lastAuthErr.Error()))
	}
	return nil, ErrNoCredential
}

// dial opens the TCP connection, honoring the Dialer, then the SSH handshake.
func dial(ctx context.Context, opts Options, cred Credential) (*ssh.Client, error) {
	var addr = net.JoinHostPort(opts.Host, fmt.Sprint(opts.Port))
	var conn net.Conn
	var err error
	var dctx, cancel = context.WithTimeout(ctx, DialTimeout)
	defer cancel()
	if opts.Dialer != nil {
		conn, err = opts.Dialer.DialContext(dctx, "tcp", addr)
	} else {
		var d = net.Dialer{Timeout: DialTimeout}
		conn, err = d.DialContext(dctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("TCP %s: %v", addr, firstLine(err.Error()))
	}

	// The handshake gets its own deadline ON THE CONNECTION: ssh.NewClientConn
	// takes no context, and a device that completes the TCP open and then stops
	// talking would otherwise hold this goroutine — and the operator's browser —
	// until something upstream gives up.
	_ = conn.SetDeadline(time.Now().Add(DialTimeout))
	var sc ssh.Conn
	var chans <-chan ssh.NewChannel
	var reqs <-chan *ssh.Request
	sc, chans, reqs, err = ssh.NewClientConn(conn, addr, ClientConfig(cred))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Cleared: from here the caller sets its own idle policy, and a console
	// legitimately sits silent while someone reads a `show` output.
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), nil
}

// ClientConfig is the SSH configuration, with THE ALGORITHMS OF THE FIELD.
//
// Exported so a test can assert the list rather than re-derive it. The list is
// the one the connectors and the discovery probe already carry (cf.
// cisco/ios/ssh.go, discovery/probe.go) and it must stay identical across all
// three: a Catalyst on IOS 15.2 offers `diffie-hellman-group14-sha1`,
// `ssh-rsa` host keys and aes-cbc ciphers, every one of which a current OpenSSH
// client refuses by default. A console that could not open on the very range this
// fleet is full of would be a console for nothing.
//
// Host keys are NOT verified, exactly as everywhere else in this product, and it
// is worth being explicit about why rather than leaving a bare nolint: these
// devices generate a self-signed key on first boot and meshcraft has no channel
// to learn it from beforehand. The confidentiality of the link is carried by the
// tunnel, not by the SSH host key.
func ClientConfig(cred Credential) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User: cred.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(cred.Secret),
			// Keyboard-interactive as well: several images ask for the password
			// through it and offer `password` only as a fallback, and a console
			// that failed on those would look like a wrong secret.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				var answers = make([]string, len(questions))
				var i int
				for i = range questions {
					answers[i] = cred.Secret
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         DialTimeout,
		Config: ssh.Config{
			KeyExchanges: []string{
				"curve25519-sha256", "curve25519-sha256@libssh.org",
				"ecdh-sha2-nistp256", "ecdh-sha2-nistp384", "ecdh-sha2-nistp521",
				"diffie-hellman-group14-sha256", "diffie-hellman-group14-sha1",
				"diffie-hellman-group1-sha1", "diffie-hellman-group-exchange-sha1",
			},
			Ciphers: []string{
				"aes128-gcm@openssh.com", "aes256-gcm@openssh.com",
				"aes128-ctr", "aes192-ctr", "aes256-ctr",
				"aes128-cbc", "aes256-cbc", "3des-cbc",
			},
		},
		HostKeyAlgorithms: []string{
			"ssh-ed25519", "rsa-sha2-256", "rsa-sha2-512", "ssh-rsa",
			"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ssh-dss",
		},
	}
}

// openShell requests the pty and starts the shell.
//
// ECHO STAYS ON, and this is the one place where the console deliberately differs
// from every connector in this codebase. They turn it off because they parse the
// transcript and the device echoes the command back anyway. Here a HUMAN is
// typing: with ECHO off, the characters they type never appear, and the terminal
// looks frozen.
func openShell(client *ssh.Client, opts Options) (*Session, error) {
	var session, err = client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("opening the SSH session: %v", firstLine(err.Error()))
	}
	var stdin io.WriteCloser
	stdin, err = session.StdinPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	var stdout io.Reader
	stdout, err = session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	// STDERR IS MERGED INTO THE SAME STREAM, and not discarded.
	//
	// Requesting a pty is supposed to make the server fold both onto the data
	// channel, and every terminal emulator relies on that. "Supposed to" is not a
	// guarantee across the ranges in this fleet, and the failure mode of being
	// wrong is the worst kind: a device that writes its refusal to the extended
	// channel would have it dropped by x/crypto/ssh, so the operator sees a
	// command produce nothing at all and concludes the console is broken.
	//
	// A terminal has ONE screen. Two goroutines copy into one pipe; interleaving
	// is exactly what a real tty does.
	var stderr io.Reader
	stderr, err = session.StderrPipe()
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	var pr, pw = io.Pipe()
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() { defer pumps.Done(); _, _ = io.Copy(pw, stdout) }()
	go func() { defer pumps.Done(); _, _ = io.Copy(pw, stderr) }()
	go func() {
		pumps.Wait()
		// Closing the write end is what turns "the device hung up" into an EOF
		// the relay can see, instead of a read that blocks forever.
		_ = pw.Close()
	}()

	var cols, rows = ClampWindow(opts.Cols, opts.Rows)
	err = session.RequestPty(opts.Term, rows, cols, ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 38400,
		ssh.TTY_OP_OSPEED: 38400,
	})
	if err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("the device refused a pty: %v", firstLine(err.Error()))
	}
	if err = session.Shell(); err != nil {
		_ = session.Close()
		return nil, fmt.Errorf("the device refused an interactive shell: %v",
			firstLine(err.Error()))
	}
	return &Session{client: client, session: session, stdin: stdin, stdout: pr}, nil
}

// IsAuthFailure recognizes "the device refused this password" and nothing else.
//
// Sorting on message text is fragile and this function does it anyway, because
// x/crypto/ssh gives nothing better: an authentication refusal arrives as a plain
// error whose string starts with "ssh: unable to authenticate". The alternative —
// treating every handshake failure as an auth failure — would walk the whole
// credential list against a switch that is merely unreachable, and lock the
// account for a WAN outage. Matching narrowly and failing closed is the safer
// direction: an unrecognized error stops the loop instead of continuing it.
func IsAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	var s = strings.ToLower(err.Error())
	return strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "no supported methods remain") ||
		strings.Contains(s, "permission denied")
}

func firstLine(s string) string {
	var i = strings.IndexAny(s, "\r\n")
	if i >= 0 {
		return s[:i]
	}
	return s
}

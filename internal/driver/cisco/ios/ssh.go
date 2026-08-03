package ios

// The connector's SSH transport.
//
// Two non-negotiable constraints, inherited from the contract
// (driver/connector.go), identical to the FS connector's:
//
//   - TCP goes out through [driver.Target.Dialer] when supplied: this is the
//     proxy's tunnel, and therefore the confidentiality boundary and site
//     coverage. `ssh.Dial` would open its own connection and bypass all of
//     that — so the client is assembled by hand on the Dialer's net.Conn;
//   - the algorithms are those of THE FIELD. A Catalyst running IOS 15.2
//     offers `diffie-hellman-group14-sha1` and `diffie-hellman-group1-sha1`,
//     ssh-rsa host keys, and aes-cbc ciphers — every one of which a current
//     OpenSSH client refuses by default. Same list as the discovery probe
//     (internal/discovery/probe.go): both talk to the same fleet.
//
// # The IOS-specific hazard this file exists to contain
//
// IOS prints SYSLOG onto the vty. `%LINK-3-UPDOWN: Interface
// GigabitEthernet1/0/5, changed state to up` arrives in the MIDDLE of a
// command's output, starts with a percent sign, and looks exactly like the
// CLI's own refusals. A naive "does the output contain a %" error test turns
// every port that happens to come up during an apply into a failed command.
// Two defenses, both needed: `terminal no monitor` at login (which the device
// may refuse), and [reCLIError], which matches the finite set of REFUSALS and
// deliberately not the syslog shape.

import (
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"meshcraft/internal/driver"
)

const (
	sshPort     = "22"
	dialTimeout = 8 * time.Second
	cmdTimeout  = 15 * time.Second
	// saveTimeout: "Building configuration..." on a Catalyst takes seconds, and
	// longer on a switch with a large VLAN database.
	saveTimeout = 60 * time.Second
	// cryptoTimeout: "crypto key generate rsa modulus 2048" runs tens of
	// seconds on a 2960-X and answers nothing while it does.
	cryptoTimeout = 180 * time.Second
)

// cliConn is an interactive shell session opened on the device.
//
// IOS accepts `exec` channels on some images and refuses them on others;
// everything therefore goes through ONE shell with a pty, commands written
// line by line, and a read up to the prompt. Same model as the discovery
// probe, hardened here for WRITES: every command waits for its prompt and any
// refusal before the next one.
type cliConn struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	out     chan byte
	buf     strings.Builder
	// priv records whether the FIRST prompt was privileged ("#"). A login that
	// lands on ">" needs `enable` before any command that matters, and the
	// distinction has to be captured at the moment it is visible: the read
	// buffer is consumed by whoever waited on it.
	priv bool
}

// rePromptPriv matches the privileged prompt, including configuration
// sub-modes ("sw1(config-if)#").
var rePromptPriv = regexp.MustCompile(`(?m)^[\w.\-]+(?:\([\w\- ]+\))?#\s*$`)

// rePromptAny also matches user EXEC ("Switch>"), which is where a login lands
// when the account does not carry privilege 15.
var rePromptAny = regexp.MustCompile(`(?m)^[\w.\-]+(?:\([\w\- ]+\))?[#>]\s*$`)

// reCLIError: the finite set of IOS REFUSALS.
//
// Deliberately NOT `% ?\w+`: IOS syslog lines ("%LINK-3-UPDOWN", "%SYS-5-CONFIG_I")
// share that shape and are not errors. Deliberately NOT matching
// "% Generating ... RSA keys" either — that is crypto key generation reporting
// progress, and treating it as a failure would abandon a switch halfway to
// having SSH.
var reCLIError = regexp.MustCompile(
	`(?i)(?:%\s*(?:invalid input|incomplete command|ambiguous command|unknown command|` +
		`unrecognized command|bad mask|bad ip address|please define|access denied|` +
		`error[: ]|not enough|insufficient)|command rejected:)[^\n]*`)

// reEnablePassword: the prompt "Password:" that `enable` puts up.
var reEnablePassword = regexp.MustCompile(`(?i)password\s*:\s*$`)

// dialSSH opens the connection, honoring the Target's Dialer.
func dialSSH(ctx context.Context, tgt driver.Target, user, secret string) (*ssh.Client, error) {
	var addr = net.JoinHostPort(tgt.Host, sshPort)
	var conn net.Conn
	var err error
	if tgt.Dialer != nil {
		var dctx, cancel = context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		conn, err = tgt.Dialer.DialContext(dctx, "tcp", addr)
	} else {
		var d = net.Dialer{Timeout: dialTimeout}
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("TCP %s: %v", addr, err)
	}

	var cfg = &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(secret),
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				var answers = make([]string, len(questions))
				var i int
				for i = range questions {
					answers[i] = secret
				}
				return answers, nil
			}),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         dialTimeout,
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

	var sc ssh.Conn
	var chans <-chan ssh.NewChannel
	var reqs <-chan *ssh.Request
	sc, chans, reqs, err = ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return ssh.NewClient(sc, chans, reqs), nil
}

// openCLI opens the interactive shell and waits for the first prompt — which
// may be user EXEC (">") when the account does not carry privilege 15.
func openCLI(ctx context.Context, client *ssh.Client) (*cliConn, error) {
	var session, err = client.NewSession()
	if err != nil {
		return nil, err
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
	// Tall and wide pty: discourages pagination even before "terminal length 0"
	// lands. ECHO off on the pty side — IOS echoes the command back anyway,
	// which is what makes the transcript diagnosable.
	err = session.RequestPty("vt100", 500, 250, ssh.TerminalModes{
		ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400,
	})
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if err = session.Shell(); err != nil {
		_ = session.Close()
		return nil, err
	}

	var c = &cliConn{client: client, session: session, stdin: stdin,
		out: make(chan byte, 64*1024)}
	go func() {
		var rbuf = make([]byte, 4096)
		for {
			var n, rerr = stdout.Read(rbuf)
			var i int
			for i = 0; i < n; i++ {
				c.out <- rbuf[i]
			}
			if rerr != nil {
				close(c.out)
				return
			}
		}
	}()

	// First prompt: the device may print a banner (and IOS often does) before it.
	var first, perr = c.waitFor(ctx, cmdTimeout, rePromptAny)
	if perr != nil {
		c.close()
		return nil, fmt.Errorf("no CLI prompt after opening the shell: %v", perr)
	}
	c.priv = rePromptPriv.MatchString(tail(first, 200))
	return c, nil
}

func (c *cliConn) close() {
	if c.session != nil {
		_, _ = io.WriteString(c.stdin, "exit\n")
		_ = c.session.Close()
	}
	if c.client != nil {
		_ = c.client.Close()
	}
}

func (c *cliConn) waitPrompt(ctx context.Context, timeout time.Duration) (string, error) {
	return c.waitFor(ctx, timeout, rePromptPriv)
}

// waitFor reads the stream until the pattern matches the end of the buffer.
func (c *cliConn) waitFor(ctx context.Context, timeout time.Duration, re *regexp.Regexp) (string, error) {
	var deadline = time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case b, ok := <-c.out:
			if !ok {
				return c.take(), fmt.Errorf("SSH session closed by the device")
			}
			c.buf.WriteByte(b)
			// Only test when the line progresses: the prompt arrives at the end
			// of a burst, and testing every byte against the whole buffer would
			// be quadratic on a "show running-config".
			if b == '\n' || b == '#' || b == '>' || b == ':' || b == '?' {
				if re.MatchString(tail(c.buf.String(), 400)) {
					return c.take(), nil
				}
			}
		case <-ctx.Done():
			return c.take(), ctx.Err()
		case <-deadline.C:
			return c.take(), fmt.Errorf("no response after %s", timeout)
		}
	}
}

// take empties the read buffer and returns its content.
func (c *cliConn) take() string {
	var s = c.buf.String()
	c.buf.Reset()
	return s
}

// sendLine writes a line terminated by CR — same convention as PuTTY and the
// Web Serial soft reset: CRLF would send one extra Enter.
func (c *cliConn) sendLine(line string) error {
	var _, err = io.WriteString(c.stdin, line+"\r")
	return err
}

// run sends a command and waits for the next privileged prompt. The returned
// output is the command's (echo included); a detected CLI refusal is returned
// second — the caller decides whether it is fatal.
func (c *cliConn) run(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	var werr = c.sendLine(cmd)
	if werr != nil {
		return "", werr
	}
	var out, err = c.waitPrompt(ctx, timeout)
	if err != nil {
		return out, err
	}
	var m = reCLIError.FindString(out)
	if m != "" {
		return out, fmt.Errorf("%s", strings.TrimSpace(m))
	}
	return out, nil
}

// enable raises a user-EXEC session to privileged EXEC.
//
// Called only when the login landed on ">" — an account created by this
// product carries `privilege 15` and lands straight on "#". The secret used is
// the ACCOUNT's: meshcraft stores one secret per account, and EnsureAccount
// aligns `enable secret` on it for exactly this reason.
func (c *cliConn) enable(ctx context.Context, secret string) error {
	var err = c.sendLine("enable")
	if err != nil {
		return err
	}
	var out string
	out, err = c.waitFor(ctx, cmdTimeout, regexp.MustCompile(
		rePromptPriv.String()+`|`+reEnablePassword.String()))
	if err != nil {
		return fmt.Errorf("no reaction to « enable »: %v", err)
	}
	if rePromptPriv.MatchString(tail(out, 200)) {
		return nil // no enable secret set: already privileged
	}
	err = c.sendLine(secret)
	if err != nil {
		return err
	}
	_, err = c.waitPrompt(ctx, cmdTimeout)
	if err != nil {
		return fmt.Errorf("« enable » refused the account secret: %v", err)
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

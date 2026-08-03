package s3900

// The connector's SSH transport — the product's FIRST CLI connector.
//
// Two non-negotiable constraints, inherited from the contract (driver/connector.go):
//
//   - TCP goes out through [driver.Target.Dialer] when supplied: this is the
//     proxy's tunnel, and therefore the confidentiality boundary and site
//     coverage. `ssh.Dial` would open its own connection and bypass all of
//     that — so the client is assembled by hand on the Dialer's net.Conn,
//     the same way driver.CheckReach does for its probes;
//   - the algorithms are those of THE FIELD, not those of the present: the
//     KEX offer captured on the S3900 OpCode 1.7.0 is
//     "diffie-hellman-group1-sha1, diffie-hellman-group14-sha1" (captured
//     2026-08-02, modern OpenSSH client refused it), the host key can be
//     ssh-rsa or even ssh-dss, and the ciphers include CBC. Same list as the
//     discovery probe (internal/discovery/probe.go) — both talk to the same
//     fleet.

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
	// saveTimeout: the 1.7.0 flash write takes several seconds
	// ("Flash programming started." -> "Success." per the serial capture).
	saveTimeout = 60 * time.Second
)

// cliConn is an interactive shell session opened on the device.
//
// The FSOS CLI refuses `exec` (like many access switches): everything goes
// through ONE shell with a pty, commands written line by line, and a read up
// to the prompt. This is the model proven by the discovery probe, hardened
// here for WRITES: every command waits for its prompt and any error before
// the next one.
type cliConn struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	out     chan byte
	// buf accumulates what has been read and not yet consumed by waitPrompt.
	buf strings.Builder
	// banner is the login banner printed before the first prompt; it carries
	// the product name on this platform (« S3900-24T4S »).
	banner string
}

// rePrompt recognizes the FSOS 1.7 prompt ("Switch#", "sw(config-if)#") and,
// as a precaution, the post-upgrade S3900-R CLI's ("Switch_config_g0/1#") —
// dialect detection reads `show version`, but the prompt must match BEFORE we
// know which one we're talking to.
var rePrompt = regexp.MustCompile(`(?m)^[\w.\-]*(?:\([\w\- /]+\))?#\s*$`)

// reCLIError: the refusals observed in the 1.7.0 lab ("% Invalid input
// detected", "% Incomplete command.") plus neighboring forms from the same CLI family.
var reCLIError = regexp.MustCompile(`(?i)% ?(invalid|incomplete|unknown|unrecognized|ambiguous)[^\n]*`)

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
		return nil, fmt.Errorf("TCP %s : %v", addr, err)
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

// openCLI ouvre le shell interactif et attend le premier prompt.
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
	// tall and wide pty: discourages pagination even if "terminal length 0"
	// doesn't exist on this firmware. ECHO cut on the pty side — the 1.7 CLI
	// echoes the command back anyway, which is enough for transcript diagnosis.
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

	// First prompt: the device may print a banner before it. That banner is the
	// ONLY place the product name appears on the CLI path — « CLI session with
	// the S3900-24T4S is opened » — so it is kept, not discarded (cf. bannerModel).
	var banner, perr = c.waitPrompt(ctx, cmdTimeout)
	if perr != nil {
		c.close()
		return nil, fmt.Errorf("no CLI prompt after opening the shell: %v", perr)
	}
	c.banner = banner
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

// waitPrompt reads up to the next prompt (or a y/n / filename prompt handled
// by the caller) and returns what was read.
func (c *cliConn) waitPrompt(ctx context.Context, timeout time.Duration) (string, error) {
	return c.waitFor(ctx, timeout, rePrompt)
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
			// be quadratic on a "show run".
			if b == '\n' || b == '#' || b == ':' || b == '?' {
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

// run sends a command and waits for the next prompt. The returned output is
// the command's (echo included); a detected CLI error is returned second —
// the caller decides whether it is fatal.
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

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

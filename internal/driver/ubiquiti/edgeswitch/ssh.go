package edgeswitch

// SSH transport for the EdgeSwitch / EdgePoint range.
//
// Two constraints inherited from the contract (driver/connector.go): TCP goes
// out through [driver.Target.Dialer] when supplied — that is the proxy tunnel,
// hence the confidentiality boundary — and the algorithm offer is the field's,
// not the present's.
//
// # What the lab session settled (EdgePoint S16, firmware 1.9.2, 2026-08-03)
//
//   - SSH lands in USER mode: "(UBNT EdgeSwitch) >". Every session runs
//     "enable" first, and enable ASKS FOR A PASSWORD — the login password
//     answers it. An empty answer is refused;
//   - "terminal length 0" IS accepted and kills the pager. "no pagination",
//     which the CLI reference names, answers "% Invalid input detected at '^'
//     marker". The pager is still FED as a fallback: the FS S3400 taught that
//     a pager-disabling command can be accepted and do nothing.

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
	cmdTimeout  = 20 * time.Second
	showTimeout = 90 * time.Second
	saveTimeout = 60 * time.Second
	maxPages    = 400
)

// cliConn is an interactive shell session on the device.
type cliConn struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	out     chan byte
	buf     strings.Builder
	banner  string
}

// rePrompt matches every prompt this CLI presents.
//
// ONE OR MORE parenthesised groups, and that repetition is the whole point:
// the sub-modes append a SECOND group rather than changing the first —
// "(UBNT EdgeSwitch) #", "(UBNT EdgeSwitch) (Config)#",
// "(UBNT EdgeSwitch) (Vlan)#", "(UBNT EdgeSwitch) (Interface 0/2)#".
//
// A pattern that allowed only one group matched the privileged prompt and
// nothing else, so every configuration command timed out waiting for a prompt
// it could not see — while the device executed it perfectly well. The 2026-08-03
// write exercise created its VLAN and moved its port, then reported both as
// refused and ran out of budget before it could revert.
var rePrompt = regexp.MustCompile(`(?m)^(?:\([^)\n]*\)\s*)+[#>]\s*$|^[\w.\-]*\s*[#>]\s*$`)

// rePriv is the privileged prompt specifically.
var rePriv = regexp.MustCompile(`[#]\s*$`)

// rePager: "--More-- or (q)uit".
var rePager = regexp.MustCompile(`--\s*More\s*--[^\n]*$`)

// rePassword is what "enable" asks.
var rePassword = regexp.MustCompile(`(?i)password\s*:\s*$`)

// rePoEConfirm: the interactive confirmation this CLI asks before applying a
// PASSIVE PoE power mode. CAPTURED 2026-08-05 on the lab EdgePoint S16:
// "Setting Passive power mode might damage unsupported device, are you sure
// you want to change opmode? (y/n)" — "auto" and "shutdown" apply
// immediately, only the passive modes (2- or 4-pair, 24V or 54V) ask. A
// caller that sends the plain command and waits for a normal prompt hangs
// until cmdTimeout: this CLI does not end the question with a prompt
// character, only the question mark and the parenthesised choice.
var rePoEConfirm = regexp.MustCompile(`\(y/n\)\s*$`)

// reCLIError: this CLI prints the IOS-style form (captured), plus the shapes
// the reference documents.
var reCLIError = regexp.MustCompile(`(?im)^\s*(%\s*invalid[^\n]*|command not found|incorrect input[^\n]*|an invalid interface[^\n]*|not a valid[^\n]*|error:[^\n]*)\s*$`)

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
			ssh.KeyboardInteractive(func(_, _ string, q []string, _ []bool) ([]string, error) {
				var answers = make([]string, len(q))
				var i int
				for i = range q {
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
	var c = &cliConn{client: client, session: session, stdin: stdin, out: make(chan byte, 64*1024)}
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
	// The login banner is long on this platform (the Ubiquiti licence notice)
	// and ends at the user-mode prompt.
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
		_, _ = io.WriteString(c.stdin, "exit\r")
		_ = c.session.Close()
	}
	if c.client != nil {
		_ = c.client.Close()
	}
}

func (c *cliConn) waitPrompt(ctx context.Context, timeout time.Duration) (string, error) {
	var out, _, err = c.waitAny(ctx, timeout)
	return out, err
}

// drainStalePrompt sweeps up a leftover already-flushed byte sequence before
// the next command is sent — see poe.go's ApplyPoE for the exact case this
// exists for (a stale prompt copy left behind by the PoE confirmation
// exchange). BEST EFFORT: a timeout here — nothing arrived — is the outcome
// that means there was nothing to drain, not a failure.
func (c *cliConn) drainStalePrompt(ctx context.Context) {
	var _, _, _ = c.waitAny(ctx, 400*time.Millisecond)
}

// waitAny reads until a prompt, a pager, or a password question ends the
// buffer, and says which.
func (c *cliConn) waitAny(ctx context.Context, timeout time.Duration) (string, string, error) {
	var deadline = time.NewTimer(timeout)
	defer deadline.Stop()
	// Hoisted out of the case: they now persist across iterations, and both
	// are read inside the case body before the next receive overwrites them.
	var b byte
	var ok bool
	for {
		select {
		case b, ok = <-c.out:
			if !ok {
				return c.take(), "", fmt.Errorf("SSH session closed by the device")
			}
			c.buf.WriteByte(b)
			if b == '\n' || b == '#' || b == '>' || b == '-' || b == ':' || b == ')' {
				var t = tail(c.buf.String(), 400)
				var trimmed = strings.TrimRight(t, " \t")
				if rePager.MatchString(trimmed) {
					return c.take(), "pager", nil
				}
				if rePassword.MatchString(trimmed) {
					return c.take(), "password", nil
				}
				if rePoEConfirm.MatchString(trimmed) {
					return c.take(), "confirm", nil
				}
				if rePrompt.MatchString(t) {
					return c.take(), "prompt", nil
				}
			}
		case <-ctx.Done():
			return c.take(), "", ctx.Err()
		case <-deadline.C:
			return c.take(), "", fmt.Errorf("no response after %s", timeout)
		}
	}
}

func (c *cliConn) take() string {
	var s = c.buf.String()
	c.buf.Reset()
	return s
}

func (c *cliConn) sendLine(line string) error {
	var _, err = io.WriteString(c.stdin, line+"\r")
	return err
}

// run sends a command and reads its whole output, feeding "--More--" if the
// pager is alive despite "terminal length 0".
func (c *cliConn) run(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	var serr = c.sendLine(cmd)
	if serr != nil {
		return "", serr
	}
	var all strings.Builder
	var pages int
	for {
		var out, kind, err = c.waitAny(ctx, timeout)
		all.WriteString(out)
		if err != nil {
			return all.String(), err
		}
		if kind != "pager" {
			break
		}
		pages++
		if pages > maxPages {
			return all.String(), fmt.Errorf("pager still asking after %d pages on \"%s\"", maxPages, cmd)
		}
		if _, err = io.WriteString(c.stdin, " "); err != nil {
			return all.String(), err
		}
	}
	var text = stripPager(all.String())
	var m = reCLIError.FindString(text)
	if m != "" {
		return text, fmt.Errorf("%s", strings.TrimSpace(m))
	}
	return text, nil
}

// stripPager removes the pagination artefacts, substituting a NEWLINE so the
// record after the marker still starts its own line (the FS S3400 lesson: a
// stripper that eats the surrounding whitespace glues two rows into one).
func stripPager(s string) string {
	var out = strings.NewReplacer("\r", "", "\x08", "").Replace(s)
	return rePagerAnywhere.ReplaceAllString(out, "\n")
}

var rePagerAnywhere = regexp.MustCompile(`\s*--\s*More\s*--[^\n]*`)

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

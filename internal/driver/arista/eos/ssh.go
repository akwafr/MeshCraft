package eos

// The connector's SSH transport.
//
// Two non-negotiable constraints, inherited from the contract
// (driver/connector.go), identical to every other CLI connector here:
//
//   - TCP goes out through [driver.Target.Dialer] when supplied: this is the
//     proxy's tunnel, and therefore the confidentiality boundary and site
//     coverage. `ssh.Dial` would open its own connection and bypass all of that,
//     so the client is assembled by hand on the Dialer's net.Conn;
//   - the algorithms are those of THE FIELD. EOS is a Linux underneath and its
//     OpenSSH is modern — a current client negotiates with it without help — but
//     the list stays the fleet's wide one anyway: an EOS pinned to an old train
//     for a hardware reason still offers the older kex, and a connector that
//     refused it would report an unreachable switch that ssh(1) reaches fine.
//
// # The EOS-specific hazard this file exists to contain
//
// EOS prints SYSLOG onto the vty, the way IOS does, and the lines have the same
// shape as CLI refusals:
//
//	Aug  6 12:00:00 sw1 Ebra: %LINEPROTO-5-UPDOWN: Line protocol on Interface
//	Ethernet1, changed state to up
//
// A naive "does the output contain a %" error test turns every port that comes
// up during an apply — and on this platform that means every port, because a
// provisioning run brings a whole faceplate to life at once — into a failed
// command. Two defenses, both needed: `no terminal monitor` at login (which the
// device may refuse), and [reCLIError], which matches the finite set of REFUSALS
// and deliberately not the syslog shape.
//
// # And one hazard IOS does not have
//
// EOS refuses commands with a leading "%" AND with a leading "!" on some
// releases ("! Interface does not exist"). The bang is also the running
// configuration's section separator, so the pattern below anchors it to the
// finite set of refusal phrasings rather than to the character — matching a bare
// "!" would make every `show running-config` read as an error.

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
	// saveTimeout: MEASURED, not guessed, and much larger than it looks like it
	// should be.
	//
	// The original comment here said "a second or two", which was documentation
	// talking. On 2026-08-06 a save on the lab switch took 74 SECONDS from the
	// command to "Copy completed successfully.", with the console emitting
	// nothing at all in between — the platform does not narrate a save. The
	// serial path had a 20 s window and reported three consecutive failures
	// before succeeding; this one had 60 s and would have failed outright on the
	// same device.
	//
	// 180 s leaves room for a switch slower or busier than that one. The cost of
	// too generous a value is a slow failure; the cost of too tight a one is
	// declaring a configuration lost that is in fact being written.
	saveTimeout = 180 * time.Second
	// showRunTimeout: EOS assembles the running configuration quickly, but a
	// 48-port switch with every interface written out is a lot of text through a
	// pty.
	showRunTimeout = 30 * time.Second
)

// cliConn is an interactive shell session opened on the device.
//
// One shell with a pty, commands written line by line, and a read up to the
// prompt — the same model as every other CLI connector here. EOS does accept
// `exec` channels, and they are deliberately not used: a single shell is what
// makes configuration mode a state this connector can reason about across
// commands, and mixing the two would make `configure` apply to a channel that
// has already closed.
type cliConn struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	out     chan byte
	buf     strings.Builder
	// priv records whether the FIRST prompt was privileged ("#"). A login that
	// lands on ">" needs `enable` before any command that matters, and the
	// distinction has to be captured at the moment it is visible: the read buffer
	// is consumed by whoever waited on it.
	priv bool
}

// rePromptPriv matches the privileged prompt, including configuration sub-modes.
//
// EOS sub-mode labels carry the interface name and its punctuation —
// `sw1(config-if-Et1)#`, `sw1(config-if-Et1-24,49-52)#`, `sw1(config-vlan-99)#`,
// `sw1(config-mgmt-ssh)#` — so the parenthesised part accepts commas and slashes
// as well as the word characters IOS needs. A pattern that stopped at `\w-`
// would miss the prompt of an interface-RANGE block, which is exactly the block
// this connector uses to trunk a whole faceplate, and every command in it would
// time out having succeeded.
var rePromptPriv = regexp.MustCompile(`(?m)^[\w.\-]+(?:\([\w\-,/ ]+\))?#\s*$`)

// rePromptAny also matches user EXEC ("sw1>"), which is where a login lands when
// the account does not carry a privileged role.
var rePromptAny = regexp.MustCompile(`(?m)^[\w.\-]+(?:\([\w\-,/ ]+\))?[#>]\s*$`)

// reCLIError: the finite set of EOS REFUSALS.
//
// Deliberately NOT `% ?\w+`: EOS syslog lines share that shape and are not
// errors (cf. the file header). Deliberately NOT a bare `!` either — that is the
// running configuration's separator, and matching it would make every
// configuration read fail.
var reCLIError = regexp.MustCompile(
	`(?i)(?:%\s*(?:invalid input|incomplete command|ambiguous command|unknown command|` +
		`unrecognized command|unavailable command|invalid command|error[: ]|access denied|` +
		`bad secret|bad password|` +
		`authorization denied|not supported|cannot |could not |failed to )|` +
		`!\s*(?:interface does not exist|invalid|error)|` +
		`command rejected:)[^\n]*`)

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
				"diffie-hellman-group14-sha256", "diffie-hellman-group16-sha512",
				"diffie-hellman-group-exchange-sha256",
				"diffie-hellman-group14-sha1", "diffie-hellman-group1-sha1",
			},
			Ciphers: []string{
				"aes128-gcm@openssh.com", "aes256-gcm@openssh.com",
				"chacha20-poly1305@openssh.com",
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

// openCLI opens the interactive shell and waits for the first prompt — which may
// be user EXEC (">") when the account does not carry a privileged role.
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
	// lands. ECHO off on the pty side — EOS echoes the command back anyway, which
	// is what makes the transcript diagnosable.
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

	// First prompt: the device may print a banner before it.
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
	var b byte
	var ok bool
	for {
		select {
		case b, ok = <-c.out:
			if !ok {
				return c.take(), fmt.Errorf("SSH session closed by the device")
			}
			c.buf.WriteByte(b)
			// Only test when the line progresses: the prompt arrives at the end of
			// a burst, and testing every byte against the whole buffer would be
			// quadratic on a "show running-config".
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
// serial provisioning script: CRLF would send one extra Enter.
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
// Called only when the login landed on ">" — an account created by this product
// carries `role network-admin` and lands straight on "#". The secret used is the
// ACCOUNT's: meshcraft stores one secret per account, and EnsureAccount aligns
// `enable password` on it for exactly this reason.
func (c *cliConn) enable(ctx context.Context, secret string) error {
	var err = c.sendLine("enable")
	if err != nil {
		return err
	}
	var out string
	out, err = c.waitFor(ctx, cmdTimeout, regexp.MustCompile(
		rePromptPriv.String()+`|`+reEnablePassword.String()))
	if err != nil {
		return fmt.Errorf("no reaction to \"enable\": %v", err)
	}
	if rePromptPriv.MatchString(tail(out, 200)) {
		return nil // no enable password set: already privileged
	}
	err = c.sendLine(secret)
	if err != nil {
		return err
	}
	out, err = c.waitPrompt(ctx, cmdTimeout)
	if err != nil {
		return fmt.Errorf("\"enable\" refused the account secret: %v", err)
	}
	// A REJECTED secret does not come back as an error. EOS prints "% Bad secret"
	// and re-prints the USER EXEC prompt, and waitPrompt matches ">" as readily
	// as "#" — so without this check a refused enable reports success, and every
	// command that follows is typed at ">", where even a valid one answers
	// "% Invalid input".
	//
	// Found while investigating a different failure, not because it had fired: on
	// this fleet the account secret IS the enable password, so the check has been
	// passing by luck. It stops being luck the moment one device is provisioned
	// out of step — and the symptom would then be a wrong-command error on a
	// perfectly good command, which is a bug hunt in the wrong file entirely.
	if !rePromptPriv.MatchString(tail(out, 200)) {
		return fmt.Errorf("\"enable\" refused the account secret: still at user " +
			"EXEC (\">\"). This account carries no privileged role and the device's " +
			"enable password is not the account secret — meshcraft cannot configure " +
			"it until one of the two is corrected")
	}
	return nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

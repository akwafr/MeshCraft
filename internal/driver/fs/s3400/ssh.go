package s3400

// The connector's SSH transport for the FS S3400 PoE+ range (BDCOM lineage).
//
// Same two non-negotiable constraints as the S3900 connector next door:
//
//   - TCP goes out through [driver.Target.Dialer] when supplied — that is the
//     proxy's tunnel, hence the confidentiality boundary and the site
//     coverage. `ssh.Dial` would open its own connection and bypass both;
//   - the algorithms are those of THE FIELD. This platform answers
//     "SSH-1.99-SSH_2.0.0" (captured 2026-08-03 on Version 2.0.2J): a
//     modern OpenSSH client refuses it outright without group1-sha1 and CBC.
//
// # The pager is FED, not disabled
//
// This is the difference that shapes the whole file. "terminal length 0" is
// ACCEPTED SILENTLY on this firmware — from EXEC mode and from config mode —
// and changes NOTHING: `show interface` still stops every screenful on
// "--More--" (captured 2026-08-03, both modes tried, tall pty requested).
// Worse, the pager consumes the FIRST CHARACTER of whatever is typed next, so
// an unfed pager does not merely truncate a read: it eats the head of the next
// command and turns "show ..." into "how ..." → "Unknown command".
//
// Every read therefore goes through [cliConn.runPaged], which answers each
// "--More--" with a space until the prompt comes back.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"meshcraft/internal/driver"
)

// errTruncated marks a read THAT NEVER REACHED A PROMPT, and it is the whole
// point of this sentinel that it is not the same thing as a failed command.
//
// [cliConn.runPaged] already separates the two internally and threw the
// distinction away at the boundary:
//
//   - the session dying, the deadline expiring, the context being cancelled or
//     the pager still asking after maxPages all return EARLY, holding whatever
//     bytes had arrived. The output is a PREFIX of the answer, and nothing in it
//     says so — a "show running-config" cut inside an interface stanza is
//     valid-looking configuration all the way to its last byte;
//   - reCLIError matching returns at the END, after the prompt came back. The
//     device answered, and the answer was a refusal. The output is COMPLETE.
//
// Callers used to collapse both into "err != nil && output is empty", which
// accepts every partial read that got at least one byte through. On 2026-08-11
// that is how poefs3400lab persisted 23 of its 52 ports as an observation, with
// GE23 left at the default PVID by ParseRunningConfig's trailing flushPort, and
// produced 30 phantom drift differences on a device nobody had misconfigured.
//
// Wrapped, never returned bare, so the message still names the command and the
// underlying cause; test with [isTruncated].
var errTruncated = errors.New("the read did not end at a CLI prompt (truncated)")

// isTruncated reports whether this error means "the output is a prefix".
//
// A caller that is about to draw a conclusion about EVERY port from one command
// must consult this before parsing: absence of a CLI error is not evidence that
// the device finished speaking.
func isTruncated(err error) bool { return errors.Is(err, errTruncated) }

// truncatedErr wraps a premature exit from a read, naming the command.
func truncatedErr(cmd string, err error) error {
	return fmt.Errorf("\"%s\" %w: %w", cmd, errTruncated, err)
}

const (
	sshPort     = "22"
	dialTimeout = 8 * time.Second
	cmdTimeout  = 20 * time.Second
	// saveTimeout: "write" answers "OK!" quickly on the captured firmware,
	// but a flash write is a flash write — leave it room.
	saveTimeout = 60 * time.Second
	// maxPages bounds the pager feeding. "show interface" on a 48T4SP is the
	// worst case seen (52 interfaces, one screen each): 400 is far above it and
	// still terminates on a firmware that would never stop asking.
	maxPages = 400
)

// cliConn is an interactive shell session opened on the device.
//
// The CLI has no usable `exec` channel: everything goes through ONE shell,
// commands written line by line, each read up to the prompt.
type cliConn struct {
	client  *ssh.Client
	session *ssh.Session
	stdin   io.WriteCloser
	out     chan byte
	buf     strings.Builder
	// banner is what the device prints before the first prompt. On this
	// platform it carries the product name: "Welcome to FS S3400-48T4SP
	// Ethernet Switch" (captured 2026-08-03).
	banner string
}

// rePrompt recognizes every prompt this CLI presents (captured 2026-08-03):
// "Switch>", "Switch#", "Switch_config#", "Switch_config_g0/1#",
// "Switch_config_vlan200#", "Switch_config_if_range#". A renamed switch
// changes the prefix, so the prefix is not anchored to "Switch".
var rePrompt = regexp.MustCompile(`(?m)^[\w.\-]*(?:_[\w./\-]+)?[#>]\s*$`)

// rePager is the pagination prompt. It arrives WITHOUT a trailing newline,
// which is why waitFor also tests on the trailing '-'.
var rePager = regexp.MustCompile(`--More--\s*$`)

// reCLIError: the refusals captured on Version 2.0.2J. This CLI does NOT
// prefix its errors with '%' the way IOS does — it prints the offending word,
// a caret line, then a bare sentence:
//
//	show interface status
//	               ^
//	Unknown command
//
// "Incomplete command", "Parameter invalid" and "Too many parameters"
// were all captured the same way (2026-08-03).
var reCLIError = regexp.MustCompile(`(?im)^\s*(unknown command|incomplete command|parameter invalid|too many parameters|invalid input[^\n]*|% ?(?:invalid|incomplete|unknown|unrecognized|ambiguous)[^\n]*)\s*$`)

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

// openCLI opens the interactive shell and waits for the first prompt.
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
	// A tall pty is requested even though it does NOT stop the pager here
	// (captured 2026-08-03): it costs nothing, and the day a firmware honours
	// it the reads get cheaper on their own. runPaged is what actually copes.
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

	// First prompt. The banner before it is the ONLY place the product name
	// appears on the CLI path, so it is kept (cf. bannerModel).
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
	var out, _, err = c.waitPromptOrPager(ctx, timeout)
	return out, err
}

// waitPromptOrPager reads until either the prompt or "--More--" ends the
// buffer, and says which one it was.
func (c *cliConn) waitPromptOrPager(ctx context.Context, timeout time.Duration) (string, bool, error) {
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
				return c.take(), false, fmt.Errorf("SSH session closed by the device")
			}
			c.buf.WriteByte(b)
			// Test only when something could plausibly end a prompt: the
			// pager ends on '-', the prompts on '#'/'>' , and everything else
			// after a newline. Testing every byte against the tail would be
			// quadratic on a "show running-config".
			if b == '\n' || b == '#' || b == '>' || b == '-' || b == ':' || b == '?' {
				var t = tail(c.buf.String(), 400)
				if rePager.MatchString(strings.TrimRight(t, " \t")) {
					return c.take(), true, nil
				}
				if rePrompt.MatchString(t) {
					return c.take(), false, nil
				}
			}
		case <-ctx.Done():
			return c.take(), false, ctx.Err()
		case <-deadline.C:
			return c.take(), false, fmt.Errorf("no response after %s", timeout)
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
// Web Serial soft reset; CRLF would send one extra Enter.
func (c *cliConn) sendLine(line string) error {
	var _, err = io.WriteString(c.stdin, line+"\r")
	return err
}

// runPaged sends a command and reads its WHOLE output, answering every
// "--More--" with a space.
//
// A space, not a newline: newline advances one LINE on this pager, so a long
// read would take one round trip per line. The space is also why the caller
// must never send its next command while a pager is pending — the pager eats
// the first character (captured 2026-08-03: "show ..." arrived as "how ...").
//
// EVERY EARLY RETURN BELOW IS WRAPPED IN [errTruncated] and the one at the
// bottom is not: that is the whole error contract of this function. Leaving the
// loop before a prompt means the returned string is a PREFIX; leaving it after
// one means the device finished and may have refused. The existing callers are
// unaffected — they test `err != nil` and the output's emptiness, and both still
// answer exactly as before — but a caller that cares can now tell the two apart
// (cf. errTruncated, and ReadState, which does).
func (c *cliConn) runPaged(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	var werr = c.sendLine(cmd)
	if werr != nil {
		return "", truncatedErr(cmd, werr)
	}
	var all strings.Builder
	var pages int
	for {
		var out, paged, err = c.waitPromptOrPager(ctx, timeout)
		all.WriteString(out)
		if err != nil {
			return all.String(), truncatedErr(cmd, err)
		}
		if !paged {
			break
		}
		pages++
		if pages > maxPages {
			return all.String(), truncatedErr(cmd,
				fmt.Errorf("pager still asking after %d pages", maxPages))
		}
		// Space = next page. No CR: that would submit an empty command line.
		if _, err = io.WriteString(c.stdin, " "); err != nil {
			return all.String(), truncatedErr(cmd, err)
		}
	}
	var text = stripPager(all.String())
	var m = reCLIError.FindString(text)
	if m != "" {
		return text, fmt.Errorf("%s", strings.TrimSpace(m))
	}
	return text, nil
}

// run is runPaged for commands whose output is short by construction
// (configuration lines). It still feeds the pager: "show running-config"
// paginates and so does a long error.
func (c *cliConn) run(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	return c.runPaged(ctx, cmd, timeout)
}

// stripPager removes the pagination artefacts so parsers never see them: the
// "--More--" marker and the run of spaces the device writes to erase it.
//
// It substitutes a NEWLINE, not nothing. The device writes the marker at the
// start of a line and the next record straight after the erasing spaces:
//
//	GigaEthernet0/1 is down, line protocol is down
//	 --More--         GigaEthernet0/2 is down, line protocol is down
//
// so a stripper that ate the surrounding whitespace glued both records into
// one line, and a line-anchored parser then saw ONE interface instead of 52.
// That is exactly what the first live run reported: "1 link" on a 48-port
// switch, and a faceplate claiming a single copper socket.
// The BACKSPACES are the reason this is not a one-liner. The device erases
// its own "--More--" with 0x08 runs:
//
//	"…is down\n\b\b\b\b\b\b\b\b\b        \b\b\b\b\b\b\b\b\bGigaEthernet0/2 is down…"
//
// Go's \s does NOT include 0x08, so a line-anchored parser reading "^\s*(\S+)"
// took the backspace run itself for the interface name and matched nothing
// after the first record. Live run of 2026-08-03: 52 ports read, ONE parsed —
// every socket would have shown as unknown on a healthy switch.
func stripPager(s string) string {
	var out = strings.NewReplacer("\r", "", "\x08", "").Replace(s)
	out = rePagerAnywhere.ReplaceAllString(out, "\n")
	return out
}

var rePagerAnywhere = regexp.MustCompile(`\s*--More--\s*`)

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

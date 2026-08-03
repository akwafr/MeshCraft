package procurve

// ONE shell for the whole session, and why there is no choice about it.
//
// The first implementation opened a fresh SSH session per command batch. On this
// range that fails, and it fails on the SECOND batch — captured 2026-08-02:
//
//	VLAN membership: the device refused a pty: ssh: pty-req failed
//
// A read had just succeeded, so nothing was wrong with the credential or the
// transport: this switch grants ONE session channel per SSH connection, and the
// second `pty-req` is refused. An apply — which re-reads the state before writing
// — needs at least two, so every write failed while every read worked.
//
// The shape here is therefore the one the Cisco and FS connectors already use: a
// pty and a shell opened once at login, commands written into it line by line,
// output read up to the prompt. It is more code than "run a command" and it is
// the only thing this platform accepts.

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// cliConn is the interactive shell opened on the device.
type cliConn struct {
	session *ssh.Session
	stdin   io.WriteCloser
	out     chan byte
	buf     strings.Builder
}

// rePrompt matches this CLI's prompts, including the sub-modes a configuration
// block passes through.
//
// CAPTURED: « SW-PLAYER-79# » at manager level, « SW-PLAYER-79> » after `exit`
// drops to operator, and the parenthesised sub-modes the config editor uses
// (« SW-PLAYER-79(config)# », « SW-PLAYER-79(vlan-99)# »).
//
// Matching BOTH « # » and « > » matters: a session that dropped to operator
// level would otherwise hang waiting for a manager prompt that never comes,
// turning a privilege problem into a timeout.
var rePrompt = regexp.MustCompile(`(?m)^[\w.\-]+(?:\([\w\-]+\))?[#>][ \t]*$`)

// reANSI matches the terminal control sequences this UI interleaves with its
// output.
//
// CAPTURED, and it is what defeated the first prompt matcher: the device writes
// cursor-visibility and mode sequences immediately before the prompt, on the SAME
// line —
//
//	"\x1b[?25hSW-PLAYER-79# "
//
// so an anchor expecting the hostname at the start of the line never fires, and
// a login that had actually succeeded timed out waiting for a prompt that was on
// screen. Stripping them at the read boundary keeps every later regex — prompt
// and configuration alike — working on the text a human sees.
var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b[()][A-Za-z0-9]|\x1b[A-DF-Za-z]`)

// stripControl turns what the device sends into the text a human sees.
//
// ESC-E IS A LINE BREAK, NOT NOISE, and getting that wrong cost a full
// debugging cycle. This CLI terminates its prompt/echo lines with ESC E — the
// ANSI NEL (next line) — where its configuration output uses LF+CR. Deleting it
// with the rest of the escape sequences collapsed an entire session onto ONE
// line, so a multi-line anchor matched only at the very start of the buffer and
// « configure terminal » timed out waiting for a prompt that had already been
// printed:
//
//	"SW-PLAYER-79# no page\x1bESW-PLAYER-79# configure terminal\x1bESW-PLAYER-79(config)# "
//
// So it is translated FIRST, before anything else is removed.
func stripControl(s string) string {
	s = strings.ReplaceAll(s, "\x1bE", "\n")
	s = reANSI.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "\r", "")
}

// openCLI opens the shell and waits for the first prompt.
//
// The banner on this unit ends with a gate on some configurations, and a bare CR
// clears it harmlessly when it is absent. « no page » follows immediately:
// without it this CLI stops long output at a "-- MORE --" prompt and waits,
// which reads downstream as a device that went silent halfway through its
// configuration.
func openCLI(ctx context.Context, client *ssh.Client) (*cliConn, error) {
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
	// ECHO off: this CLI echoes what it is sent anyway, and leaving the pty echo
	// on doubles every command in the transcript.
	err = session.RequestPty("vt100", 200, 200, ssh.TerminalModes{
		ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 38400, ssh.TTY_OP_OSPEED: 38400,
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

	var c = &cliConn{session: session, stdin: stdin, out: make(chan byte, 128*1024)}
	go func() {
		var buf = make([]byte, 4096)
		for {
			var n, rerr = stdout.Read(buf)
			var i int
			for i = 0; i < n; i++ {
				c.out <- buf[i]
			}
			if rerr != nil {
				close(c.out)
				return
			}
		}
	}()

	// Clear the banner gate, then reach the first prompt.
	_ = c.sendLine("")
	if _, err = c.waitPrompt(ctx, cmdTimeout); err != nil {
		c.close()
		return nil, fmt.Errorf("no CLI prompt after opening the shell: %v", err)
	}
	if _, err = c.run(ctx, "no page", cmdTimeout); err != nil {
		// Not fatal: a refusal costs an error echo, where active pagination would
		// cost every long read.
		_ = err
	}
	return c, nil
}

func (c *cliConn) close() {
	if c.session != nil {
		_, _ = io.WriteString(c.stdin, "exit\n")
		_ = c.session.Close()
	}
}

func (c *cliConn) sendLine(line string) error {
	var _, err = io.WriteString(c.stdin, line+"\n")
	return err
}

// run sends one command and returns everything printed up to the next prompt.
func (c *cliConn) run(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	if err := c.sendLine(cmd); err != nil {
		return "", err
	}
	return c.waitPrompt(ctx, timeout)
}

// waitPrompt reads until a prompt closes the output.
//
// The prompt is only tested when the line could plausibly have ended — this
// device streams kilobytes of configuration, and matching the whole buffer on
// every byte would be quadratic on a « show running-config ».
func (c *cliConn) waitPrompt(ctx context.Context, timeout time.Duration) (string, error) {
	var deadline = time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case b, ok := <-c.out:
			if !ok {
				return c.take(), fmt.Errorf("SSH session closed by the device")
			}
			c.buf.WriteByte(b)
			if b == '#' || b == '>' || b == ' ' {
				if rePrompt.MatchString(stripControl(tail(c.buf.String(), 300))) {
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

// take empties the read buffer and returns it CLEANED: callers parse what a
// human would see, not the cursor traffic that produced it.
func (c *cliConn) take() string {
	var s = c.buf.String()
	c.buf.Reset()
	return stripControl(s)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

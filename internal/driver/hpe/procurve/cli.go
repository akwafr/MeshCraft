package procurve

// ONE shell for the whole session, and why there is no choice about it.
//
// The first implementation opened a fresh SSH session per command batch. On this
// range that fails, and it fails on the SECOND batch — captured 2026-08-02:
//
//	VLAN membership: the device refused a pty: ssh: pty-req failed
//
// That was read at the time as "this switch grants ONE session channel per SSH
// connection". IT DOES NOT, and the correction matters because the real cause
// is one an operator will meet again: this range allows FOUR management sessions
// IN TOTAL — one console plus three network, as "show ip ssh" lists them — and
// an abandoned session holds its slot for 120 seconds. Opening a fresh session
// per batch simply ran the switch out of them, and a saturated ProCurve
// authenticates normally and THEN refuses the session channel (CAPTURED
// 2026-08-03, over both SSH and telnet: "Sorry, the maximum number of sessions
// are active").
//
// The consequence for anything driving this range is the same either way, which
// is why the shape below is right regardless: hold ONE shell for the whole
// session — a pty and a shell opened at login, commands written into it line by
// line, output read up to the prompt — and close it. The consequence that is
// NEW is that a caller must pace its gestures: four reads in quick succession
// will lock a healthy switch out of its own administration for two minutes.

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
// CAPTURED: "SW-PLAYER-79#" at manager level, "SW-PLAYER-79>" after `exit`
// drops to operator, and the parenthesised sub-modes the config editor uses
// ("SW-PLAYER-79(config)#", "SW-PLAYER-79(vlan-99)#").
//
// Matching BOTH "#" and ">" matters: a session that dropped to operator
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
// "configure terminal" timed out waiting for a prompt that had already been
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
// clears it harmlessly when it is absent. "no page" follows immediately:
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
		// NAME THE LIKELY CAUSE, because the bare message points the wrong way.
		//
		// A pty refused straight after a SUCCESSFUL authentication is, on this
		// range, almost always session saturation: it allows FOUR management
		// sessions in total — one console plus three network — and an abandoned
		// one holds its slot until its 120-second inactivity timer expires. The
		// switch answers by accepting the credential and then refusing the
		// session channel.
		//
		// Read literally, "the device refused a pty" suggests a transport or
		// firmware problem, and it cost one investigation a wrong conclusion
		// (recorded in this package as "this switch grants ONE session channel per
		// SSH connection" — it does not; it had simply run out of sessions).
		return nil, fmt.Errorf("the device refused a session channel after accepting "+
			"the credential (%v) — this range allows only 4 management sessions at "+
			"once and holds an abandoned one for 120s: check \"show ip ssh\"",
			firstLine(err.Error()))
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
//
// # The prompt must not be believed until the command has been echoed back
//
// This CLI REPAINTS ITS PROMPT before echoing what is typed at it, and that one
// fact is enough to make every long read return nothing. CAPTURED on the wire,
// 2026-08-03, sending "show running-config":
//
//	\x1b[24;1HSW-PLAYER-79# \x1b[24;1H\x1b[24;15H…\x1b[24;15Hshow runni…
//
// The prompt on the left is not the answer to the command — it is the SAME
// prompt, redrawn at the bottom of a 24-line screen the device manages for a
// terminal it assumes is watching. A reader that returns at the first prompt it
// sees therefore returns 15 characters and stops, and the whole configuration
// arrives afterwards to be discarded as the next command's output.
//
// It presents as data loss with no error anywhere: the read "succeeded", the
// device is fine, and the state comes back with zero VLANs. Only the
// desynchronisation guard in ReadState caught it, which is exactly what that
// guard is for.
//
// So the echo is the synchronisation point. Once the command has been seen
// coming back, any prompt after it is genuinely the end of ITS output.
func (c *cliConn) run(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	var err = c.sendLine(cmd)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cmd) == "" {
		// Nothing to echo: a bare CR clears the banner gate and the next prompt IS
		// the answer.
		return c.waitPrompt(ctx, timeout)
	}
	return c.waitEchoThenPrompt(ctx, timeout, cmd)
}

// waitEchoThenPrompt reads until the command comes back, then until a prompt.
//
// The echo arrives in FRAGMENTS, each preceded by its own cursor move
// ("…show runni…" then "…ng-config"), so it is only contiguous after
// [stripControl] — which is why the test runs on the cleaned buffer and not on
// the bytes as they land.
func (c *cliConn) waitEchoThenPrompt(ctx context.Context, timeout time.Duration,
	cmd string) (string, error) {
	var deadline = time.NewTimer(timeout)
	defer deadline.Stop()
	var last = cmd[len(cmd)-1]
	var echoAt = -1
	var b byte
	var ok bool
	for {
		select {
		case b, ok = <-c.out:
			if !ok {
				return c.take(), fmt.Errorf("SSH session closed by the device")
			}
			c.buf.WriteByte(b)
			if echoAt < 0 {
				// Only test when the byte could complete the echo: this device
				// streams kilobytes and scanning the buffer on every byte would be
				// quadratic on a "show running-config".
				if b == last && strings.Contains(stripControl(c.buf.String()), cmd) {
					// Remember WHERE the echo ended. Searching the whole tail from
					// here on would keep finding the prompt this device repaints
					// BEFORE the echo — a complete "SW-PLAYER-79#" line sitting a
					// few characters back, which matches and ends the read on the
					// first space of the output. That is the same bug the echo gate
					// exists to fix, one step further along.
					echoAt = c.buf.Len()
				}
				continue
			}
			if b == '#' || b == '>' || b == ' ' {
				var s = c.buf.String()
				if rePrompt.MatchString(stripControl(tail(s[echoAt:], 300))) {
					return c.take(), nil
				}
			}
		case <-ctx.Done():
			return c.take(), ctx.Err()
		case <-deadline.C:
			if echoAt < 0 {
				return c.take(), fmt.Errorf(
					"the device never echoed %q within %s: the session is not at a "+
						"command prompt", cmd, timeout)
			}
			return c.take(), fmt.Errorf("no prompt after %s", timeout)
		}
	}
}

// waitPrompt reads until a prompt closes the output.
//
// The prompt is only tested when the line could plausibly have ended — this
// device streams kilobytes of configuration, and matching the whole buffer on
// every byte would be quadratic on a "show running-config".
func (c *cliConn) waitPrompt(ctx context.Context, timeout time.Duration) (string, error) {
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

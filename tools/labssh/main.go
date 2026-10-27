// Command labssh runs commands on a lab switch through the controller's SOCKS
// route, and prints the transcript.
//
// EXPLORATION ONLY. It exists because the lab sits behind the proxy tunnel and
// the host running the build has no route to it: `ssh` alone cannot get there,
// and the machine has no netcat to bridge through. Everything here — the SOCKS
// dial, the SSH handshake, the interactive shell drain — is what
// internal/driver/*/ssh.go already does inside the controller; this is the same
// path with a command line in front of it, so a capture taken with this tool is
// taken over the transport the connector will use.
//
// It is NOT part of the product and nothing imports it. `go build ./...` covers
// it, which is the point: it must keep compiling against the same x/crypto/ssh
// the connectors use, or it stops being evidence of anything.
//
// Credentials come from the environment, never from flags: an argument is
// visible in `ps` to every user on the box for as long as the command runs.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

func main() {
	var host = flag.String("host", "", "device address, e.g. 192.0.2.5")
	var socksAddr = flag.String("socks", "127.0.0.1:1080", "controller SOCKS route")
	var timeout = flag.Duration("timeout", 25*time.Second, "per-command budget")
	var shell = flag.Bool("shell", true, "use an interactive shell (EOS refuses exec on some trains)")
	flag.Parse()
	if *host == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: labssh -host IP [flags] 'cmd' ['cmd'...]")
		os.Exit(2)
	}

	var user = os.Getenv("LAB_USER")
	var pass = os.Getenv("LAB_PASS")
	var sUser = os.Getenv("SOCKS_USER")
	var sPass = os.Getenv("SOCKS_PASS")
	if user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "LAB_USER and LAB_PASS must be set")
		os.Exit(2)
	}

	var auth *proxy.Auth
	if sUser != "" {
		auth = &proxy.Auth{User: sUser, Password: sPass}
	}
	var dialer, err = proxy.SOCKS5("tcp", *socksAddr, auth, proxy.Direct)
	if err != nil {
		fail("socks: %v", err)
	}

	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var raw net.Conn
	raw, err = dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", net.JoinHostPort(*host, "22"))
	if err != nil {
		fail("dial %s through socks: %v", *host, err)
	}

	var cfg = &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.Password(pass),
			// Some EOS trains offer keyboard-interactive rather than password.
			ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
				var out = make([]string, len(qs))
				var i int
				for i = range qs {
					out[i] = pass
				}
				return out, nil
			}),
		},
		// Lab exploration against a device whose host key nobody has recorded.
		// The connectors make the same choice for the same reason, and it is
		// acceptable here only because the whole path is already inside the
		// operator's own tunnel.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
		Timeout:         20 * time.Second,
	}
	var conn ssh.Conn
	var chans <-chan ssh.NewChannel
	var reqs <-chan *ssh.Request
	conn, chans, reqs, err = ssh.NewClientConn(raw, *host+":22", cfg)
	if err != nil {
		fail("ssh handshake: %v", err)
	}
	var client = ssh.NewClient(conn, chans, reqs)
	defer client.Close() //nolint:errcheck

	if !*shell {
		var i int
		for i = 0; i < flag.NArg(); i++ {
			runExec(client, flag.Arg(i), *timeout)
		}
		return
	}
	runShell(client, flag.Args(), *timeout)
}

// runExec runs one command in its own channel. Simple, but several switch CLIs
// refuse an exec channel entirely — hence the shell path being the default.
func runExec(client *ssh.Client, cmd string, budget time.Duration) {
	var sess, err = client.NewSession()
	if err != nil {
		fail("session: %v", err)
	}
	defer sess.Close() //nolint:errcheck
	var out []byte
	out, err = sess.CombinedOutput(cmd)
	fmt.Printf("===== %s =====\n%s\n", cmd, out)
	if err != nil {
		fmt.Printf("(exit: %v)\n", err)
	}
	_ = budget
}

// runShell opens one interactive shell and feeds it every command, which is how
// the EOS connector works: `terminal length 0` has to survive across commands,
// and privileged EXEC is a property of the SESSION, not of a channel.
func runShell(client *ssh.Client, cmds []string, budget time.Duration) {
	var sess, err = client.NewSession()
	if err != nil {
		fail("session: %v", err)
	}
	defer sess.Close() //nolint:errcheck
	// A TALL terminal, and it matters: with a short one EOS paginates even after
	// `terminal length 0` on some trains, and the capture comes back peppered
	// with --More-- markers that look like output.
	var modes = ssh.TerminalModes{ssh.ECHO: 0}
	if err = sess.RequestPty("xterm", 200, 200, modes); err != nil {
		fail("pty: %v", err)
	}
	var stdin, _ = sess.StdinPipe()
	var stdout, _ = sess.StdoutPipe()
	if err = sess.Shell(); err != nil {
		fail("shell: %v", err)
	}

	var buf = make(chan []byte, 256)
	go func() {
		var b = make([]byte, 4096)
		for {
			var n, rerr = stdout.Read(b)
			if n > 0 {
				var c = make([]byte, n)
				copy(c, b[:n])
				buf <- c
			}
			if rerr != nil {
				close(buf)
				return
			}
		}
	}()

	// Drain the banner, then quiet the terminal.
	drain(buf, 2*time.Second)
	send(stdin, "terminal length 0")
	drain(buf, 1500*time.Millisecond)
	send(stdin, "no terminal monitor")
	drain(buf, 1500*time.Millisecond)

	var cmd string
	for _, cmd = range cmds {
		// "__ENABLE__" raises the session to privileged EXEC, answering the
		// password prompt from the environment.
		//
		// A pseudo-command rather than a flag, and the secret comes from the
		// environment rather than the argument list, for the reason the file
		// header gives: an argument is visible in `ps` to every user on the box.
		// The connector does the same gesture with the same secret
		// (Client.Authenticate -> cli.enable), so what is exercised here is the
		// path the product will take.
		if cmd == "__ENABLE__" {
			send(stdin, "enable")
			var challenge = drain(buf, 3*time.Second)
			// THE SECRET GOES OUT ONLY IF SOMETHING ASKED FOR IT. This used to
			// send it unconditionally, which is a real leak rather than a
			// cosmetic one: on a device where the account reaches privileged EXEC
			// with NO challenge — core01.lab with `mcadmin`, measured
			// 2026-08-10 — there is no prompt to answer, so the secret was
			// submitted as a CLI COMMAND, echoed back by the device, and written
			// into the capture beside "% Invalid input". Captures end up in
			// working notes and committed as fixtures, so that is a secret at
			// rest in git.
			//
			// Matching the prompt FIRST is what the connector already does
			// (cliConn.enable, internal/driver/.../ssh.go) — this is that rule
			// brought over, not a new invention. "assword" rather than the full
			// word because the platforms differ on case and on the trailing
			// colon, and the substring is the part they agree on.
			if strings.Contains(strings.ToLower(challenge), "assword") {
				send(stdin, os.Getenv("LAB_ENABLE"))
				challenge += drain(buf, budget)
			}
			// The secret is never printed even when it WAS sent: a device that
			// echoes it back would otherwise put it in this transcript, which is
			// the same leak one step later.
			fmt.Printf("===== enable =====\n%s\n", redact(strings.TrimRight(challenge, " \r\n")))
			continue
		}
		send(stdin, cmd)
		var out = drain(buf, budget)
		fmt.Printf("===== %s =====\n%s\n", cmd, strings.TrimRight(out, " \r\n"))
	}
	send(stdin, "exit")
	drain(buf, time.Second)
}

// redact removes the enable secret from text about to be printed. It is the last
// line of defence, not the first: the caller above avoids SENDING the secret
// when nothing asked for it, and this covers the remaining case where a device
// echoes back what it was legitimately given.
func redact(s string) string {
	var secret = os.Getenv("LAB_ENABLE")
	if strings.TrimSpace(secret) == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "<redacted>")
}

func send(w interface{ Write([]byte) (int, error) }, s string) {
	_, _ = w.Write([]byte(s + "\n"))
}

// drain collects output until it goes quiet.
//
// QUIET, not "until the prompt": a prompt regexp is exactly the thing this tool
// exists to DISCOVER, so keying the drain on one would make the capture depend
// on the guess it is meant to check. The cost is a fixed idle wait per command.
func drain(ch chan []byte, budget time.Duration) string {
	var out bytes.Buffer
	var deadline = time.After(budget)
	var idle = 700 * time.Millisecond
	var b []byte
	var open bool
	for {
		var quiet = time.After(idle)
		select {
		case b, open = <-ch:
			if !open {
				return out.String()
			}
			out.Write(b)
		case <-quiet:
			if out.Len() > 0 {
				return out.String()
			}
		case <-deadline:
			return out.String()
		}
	}
}

func fail(f string, a ...any) {
	fmt.Fprintf(os.Stderr, f+"\n", a...)
	os.Exit(1)
}

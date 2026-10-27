// Command labattach connects to a shared serial line as an ordinary attacher
// and prints what the device says.
//
// EXPLORATION ONLY. It exists because the attach half of the relay had never
// been exercised by anything: the host half was proven by a real browser
// publishing, and the WebSocket upgrade was proven by curl, but "the socket
// opens" and "bytes reach the other end" are different claims and only the
// second one is the feature.
//
// It speaks the same protocol the browser does — binary frames, no framing of
// its own — so what it proves is what an operator would get.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// runScript drives an expect-style exchange and prints the whole transcript.
func runScript(ctx context.Context, conn *websocket.Conn, script string, budget time.Duration) {
	var steps = strings.Split(script, "~~")
	var seen strings.Builder
	var i int
	for i = 0; i+1 < len(steps); i += 2 {
		var expect = steps[i]
		var toSend = strings.ReplaceAll(steps[i+1], "\\r", "\r")
		if expect != "" {
			// Read until the marker shows up or the budget runs out. The marker is
			// matched against everything seen SO FAR, not just the latest frame:
			// a prompt arrives split across frames more often than not.
			var deadline = time.Now().Add(budget)
			for !strings.Contains(seen.String(), expect) && time.Now().Before(deadline) {
				var rc, cancel = context.WithTimeout(ctx, time.Until(deadline))
				var _, data, err = conn.Read(rc)
				cancel()
				if err != nil {
					break
				}
				seen.Write(data)
			}
			if !strings.Contains(seen.String(), expect) {
				fmt.Printf("=== TIMED OUT waiting for %q ===\n%s\n", expect, seen.String())
				return
			}
		}
		if toSend != "" {
			var werr = conn.Write(ctx, websocket.MessageBinary, []byte(toSend))
			if werr != nil {
				fmt.Fprintf(os.Stderr, "write: %v\n", werr)
				return
			}
		}
	}
	// Drain whatever follows the last send.
	var deadline = time.Now().Add(budget)
	for time.Now().Before(deadline) {
		var rc, cancel = context.WithTimeout(ctx, time.Until(deadline))
		var _, data, err = conn.Read(rc)
		cancel()
		if err != nil {
			break
		}
		seen.Write(data)
	}
	fmt.Println(seen.String())
}

func main() {
	var base = flag.String("base", "http://127.0.0.1:8080", "controller")
	var site = flag.String("site", "", "site id")
	var offer = flag.String("offer", "", "serial offer id")
	var cookie = flag.String("cookie", "", "session cookie, name=value")
	var send = flag.String("send", "\r", "bytes to send once connected")
	var wait = flag.Duration("wait", 8*time.Second, "how long to listen")
	// -script runs an EXPECT-style exchange: alternating "wait for this
	// substring" / "then send this", separated by "|". Empty expect means "do
	// not wait, just send".
	//
	// Needed because a console is a conversation: a login prompt, a password
	// prompt, a pager. Sending everything at once and reading the tail loses the
	// ordering that tells you which step failed — and on a device that asks for
	// a password, blind sending puts the secret into whatever is echoing.
	// Separated by "~~" and NOT by "|": half the commands worth scripting on a
	// switch contain a pipe (`show running-config | section …`), and using it as
	// the separator silently split those into nonsense steps. Observed exactly
	// once, immediately.
	var script = flag.String("script", "", "expect~~send~~expect~~send… ('\\r' allowed)")
	flag.Parse()
	if *site == "" || *offer == "" || *cookie == "" {
		fmt.Fprintln(os.Stderr, "usage: labattach -site ID -offer ID -cookie 'n=v'")
		os.Exit(2)
	}

	var url = fmt.Sprintf("%s/api/sites/%s/serial-servers/%s/attach", *base, *site, *offer)
	var hdr = http.Header{}
	hdr.Set("Cookie", *cookie)
	hdr.Set("Origin", *base)

	var ctx, cancel = context.WithTimeout(context.Background(), *wait+10*time.Second)
	defer cancel()
	var conn, resp, err = websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:      hdr,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		var code int
		if resp != nil {
			code = resp.StatusCode
		}
		fmt.Fprintf(os.Stderr, "attach failed (http %d): %v\n", code, err)
		os.Exit(1)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done") //nolint:errcheck
	fmt.Println("ATTACHED")

	if *script != "" {
		runScript(ctx, conn, *script, *wait)
		return
	}
	if *send != "" {
		// A bare carriage return is what wakes a console that is sitting at a
		// prompt saying nothing — the same trick the autodetect probe uses.
		var werr = conn.Write(ctx, websocket.MessageBinary, []byte(*send))
		if werr != nil {
			fmt.Fprintf(os.Stderr, "write failed: %v\n", werr)
			os.Exit(1)
		}
		fmt.Printf("SENT %q\n", *send)
	}

	var deadline = time.Now().Add(*wait)
	var got int
	var b strings.Builder
	for time.Now().Before(deadline) {
		var readCtx, rc = context.WithTimeout(ctx, time.Until(deadline))
		var _, data, rerr = conn.Read(readCtx)
		rc()
		if rerr != nil {
			break
		}
		got += len(data)
		b.Write(data)
	}
	fmt.Printf("RECEIVED %d bytes\n", got)
	if got > 0 {
		fmt.Printf("---\n%s\n---\n", b.String())
	} else {
		fmt.Println("NOTHING CAME BACK: the socket is open and the relay moved no bytes.")
	}
}

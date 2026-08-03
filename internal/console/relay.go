package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// The relay: a device shell on one side, a browser on the other, and the
// LIFECYCLE rules that keep an abandoned tab from holding a switch hostage.
//
// # Why the lifecycle is the hard part, not the plumbing
//
// Copying bytes both ways is ten lines. What earns this file its length is
// everything that closes it. A vty is a SCARCE RESOURCE — a Catalyst 2960 has
// sixteen and hands them out until it runs out, at which point nobody can log in
// at all, including the person trying to fix it. The browser is the least
// reliable participant in the whole chain: a closed laptop lid, a WAN drop, or a
// tab left open over a weekend all produce a socket that is still "open" as far
// as TCP is concerned and will never send another byte.
//
// So the session is closed by FOUR independent conditions, and each one is here
// because the other three do not cover it:
//
//   - the client closes the socket. The ordinary case, and the only one that
//     needs no timer;
//   - the device hangs up (exec-timeout, `exit`, a reload). Detected as EOF on
//     the read side, which is exactly what the merged stderr pipe in
//     [openShell] exists to make observable;
//   - IDLE: nothing typed for [RelayOptions.IdleTimeout]. Measured on the
//     CLIENT's input only, deliberately. Counting device output would keep a
//     forgotten console alive forever on any switch that logs to the vty, which
//     is every IOS switch — the precise case this rule exists to catch;
//   - MAX LIFETIME: a hard ceiling regardless of activity. Someone typing once an
//     hour is not "using" a console, and a session that outlives the operator's
//     shift is one nobody is watching.
//
// A keepalive ping runs underneath all of it: without traffic, a NAT or a load
// balancer drops the connection silently, and both ends would sit waiting on a
// socket that no longer goes anywhere.

// Frame is one WebSocket message. Binary carries terminal bytes; text carries
// the control protocol.
//
// The split is not cosmetic. Terminal traffic is arbitrary bytes — a `show run`
// on a device with a non-UTF-8 banner is not valid text — and a transport that
// tried to send it as text would either corrupt it or refuse it. Control messages
// are JSON and belong in text frames, which also means the two can never be
// confused for one another by either end.
type Frame struct {
	Text bool
	Data []byte
}

// Peer is the browser side of the relay.
//
// An interface and not a *websocket.Conn so this file can be tested without a
// network: the fake peer in relay_test.go is thirty lines, and it can do the
// things a real browser does that are otherwise impossible to arrange on demand —
// go silent forever, close mid-write, send a malformed control frame.
type Peer interface {
	// Read blocks until the next message. It must return an error when ctx is
	// done, and io.EOF (or a wrapped one) when the client went away.
	Read(ctx context.Context) (Frame, error)
	Write(ctx context.Context, f Frame) error
	// Ping is the keepalive. A Peer with nothing to send may implement it as a
	// no-op returning nil.
	Ping(ctx context.Context) error
	// Close ends the connection, telling the client WHY in a way it can show.
	Close(reason string) error
}

// RelayOptions carries the lifecycle policy. Zero values take the defaults
// below, so a caller that has no opinion still gets every safeguard.
type RelayOptions struct {
	IdleTimeout  time.Duration
	MaxLifetime  time.Duration
	PingInterval time.Duration
	// WriteTimeout bounds one write to the client. A browser that stops reading
	// applies back-pressure all the way down to the device, and without this the
	// relay would block on it holding the vty open.
	WriteTimeout time.Duration
	// OnClose reports how the session ended, for the audit line. Optional.
	OnClose func(reason string, sent, received int64, d time.Duration)
}

const (
	DefaultIdleTimeout  = 15 * time.Minute
	DefaultMaxLifetime  = 4 * time.Hour
	DefaultPingInterval = 30 * time.Second
	DefaultWriteTimeout = 20 * time.Second
	// readChunk: one read from the device, one frame to the browser. 8 KiB keeps
	// a `show tech-support` moving without producing frames so large that a slow
	// client stalls the pipe.
	readChunk = 8 << 10
	// maxControlFrame bounds a control message. A resize is under a hundred
	// bytes; anything larger is not the protocol.
	maxControlFrame = 4 << 10
	// maxInputFrame bounds ONE keystroke burst from the browser. Paste is the
	// real case — an operator pasting a configuration block — and 64 KiB carries
	// any of those while refusing a client trying to use the console as an
	// upload channel.
	maxInputFrame = 64 << 10
)

func (o *RelayOptions) withDefaults() {
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = DefaultIdleTimeout
	}
	if o.MaxLifetime <= 0 {
		o.MaxLifetime = DefaultMaxLifetime
	}
	if o.PingInterval <= 0 {
		o.PingInterval = DefaultPingInterval
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = DefaultWriteTimeout
	}
}

// control is the client → controller message set. One operation today; the
// field is present so an older client sending something unknown is IGNORED
// rather than treated as terminal input, which would type JSON at the switch.
type control struct {
	Op   string `json:"op"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

// status is the controller → client message set.
type status struct {
	Op       string `json:"op"`
	Username string `json:"username,omitempty"`
	Why      string `json:"why,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// Relay pumps bytes between an open [Session] and a [Peer] until one of the four
// closing conditions fires. It always closes the session before returning.
//
// The returned error describes how it ENDED, and an ordinary ending is not an
// error: a client that closed its tab, an idle timeout and a device that hung up
// all return nil. Only a fault the operator could act on comes back as an error.
func Relay(ctx context.Context, sess *Session, peer Peer, opts RelayOptions) error {
	opts.withDefaults()
	defer sess.Close() //nolint:errcheck

	var started = time.Now()
	var ctx2, cancel = context.WithTimeout(ctx, opts.MaxLifetime)
	defer cancel()

	// idle is reset by client input only (cf. the header). Buffered so a burst of
	// keystrokes never blocks the reader on a timer nobody is draining yet.
	var idle = make(chan struct{}, 1)
	var sent, received int64
	var counts sync.Mutex

	// The reason is written by whichever arm finishes first and read once at the
	// end; the mutex covers that hand-off, not the byte counters' precision.
	var reason = "closed"
	var setReason = func(r string) {
		counts.Lock()
		if reason == "closed" {
			reason = r
		}
		counts.Unlock()
	}

	_ = writeStatus(ctx2, peer, opts.WriteTimeout, status{
		Op: "ready", Username: sess.Accepted.Username, Why: sess.Accepted.Why,
	})

	var wg sync.WaitGroup
	wg.Add(2)

	// Device → browser.
	go func() {
		defer wg.Done()
		defer cancel()
		var buf = make([]byte, readChunk)
		for {
			var n, err = sess.Read(buf)
			if n > 0 {
				counts.Lock()
				sent += int64(n)
				counts.Unlock()
				var wctx, wcancel = context.WithTimeout(ctx2, opts.WriteTimeout)
				var werr = peer.Write(wctx, Frame{Data: append([]byte(nil), buf[:n]...)})
				wcancel()
				if werr != nil {
					setReason("client stopped reading")
					return
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					setReason("the device closed the session")
				} else {
					setReason("device link lost: " + firstLine(err.Error()))
				}
				return
			}
		}
	}()

	// Browser → device.
	go func() {
		defer wg.Done()
		defer cancel()
		for {
			var f, err = peer.Read(ctx2)
			if err != nil {
				if ctx2.Err() != nil {
					return // another arm already decided why
				}
				setReason("the console was closed")
				return
			}
			select {
			case idle <- struct{}{}:
			default:
			}
			if f.Text {
				// A control frame NEVER reaches the device. That is the whole
				// point of the two-frame protocol: were it merged, a client
				// bug would type `{"op":"resize"}` into a privileged shell.
				if len(f.Data) > maxControlFrame {
					continue
				}
				var msg control
				if json.Unmarshal(f.Data, &msg) != nil {
					continue
				}
				if msg.Op == "resize" {
					// Failure is cosmetic by contract (cf. Session.Resize).
					_ = sess.Resize(msg.Cols, msg.Rows)
				}
				continue
			}
			if len(f.Data) > maxInputFrame {
				setReason("input frame too large")
				return
			}
			counts.Lock()
			received += int64(len(f.Data))
			counts.Unlock()
			if _, err = sess.Write(f.Data); err != nil {
				setReason("the device stopped accepting input")
				return
			}
		}
	}()

	// Timers. Kept on this goroutine rather than in the pumps: an idle deadline
	// measured inside the read loop would only fire on the next read, which on an
	// abandoned console is never.
	var idleTimer = time.NewTimer(opts.IdleTimeout)
	defer idleTimer.Stop()
	var pinger = time.NewTicker(opts.PingInterval)
	defer pinger.Stop()

	var loop = true
	for loop {
		select {
		case <-ctx2.Done():
			if errors.Is(ctx2.Err(), context.DeadlineExceeded) {
				setReason(fmt.Sprintf("session closed after the %s maximum",
					opts.MaxLifetime.Round(time.Minute)))
			}
			loop = false
		case <-idle:
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(opts.IdleTimeout)
		case <-idleTimer.C:
			setReason(fmt.Sprintf("closed after %s without activity",
				opts.IdleTimeout.Round(time.Minute)))
			loop = false
		case <-pinger.C:
			var pctx, pcancel = context.WithTimeout(ctx2, opts.WriteTimeout)
			var perr = peer.Ping(pctx)
			pcancel()
			if perr != nil {
				setReason("the console stopped responding")
				loop = false
			}
		}
	}

	counts.Lock()
	var finalReason = reason
	var s, rcv = sent, received
	counts.Unlock()

	// Tell the client WHY before hanging up. Without it the terminal just stops,
	// and "did it crash or did it time out?" is not a question an operator should
	// have to answer by reading controller logs.
	_ = writeStatus(ctx, peer, opts.WriteTimeout, status{Op: "closed", Reason: finalReason})
	_ = peer.Close(finalReason)

	// Closing the session unblocks the device-side read; cancelling ctx2 unblocks
	// the peer read. Both arms are therefore guaranteed to return.
	_ = sess.Close()
	cancel()
	wg.Wait()

	if opts.OnClose != nil {
		opts.OnClose(finalReason, s, rcv, time.Since(started))
	}
	return nil
}

func writeStatus(ctx context.Context, peer Peer, timeout time.Duration, st status) error {
	var data, err = json.Marshal(st)
	if err != nil {
		return err
	}
	var wctx, cancel = context.WithTimeout(ctx, timeout)
	defer cancel()
	return peer.Write(wctx, Frame{Text: true, Data: data})
}

package tunnel

import (
	"bufio"
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// What this file exercises: the ORDER in which SendEvents delivers the backlog,
// the resync snapshot, and the live stream.
//
// This is the only property of the function that can break silently, and the one
// whose violation costs the most: a snapshot delivered BEFORE the backlog would be
// overwritten by transitions accumulated during the outage, therefore by stale state,
// and the product would show a responding device as unreachable. Nothing in the
// function type enforces this -- only this test does.
//
// On a real yamux pair (net.Pipe, no reserved port), in production direction: the
// PROXY is the yamux server and opens the stream, the controller is the client and
// accepts it (cf. Hub.AcceptEvents).

// pairYamuxBoth sets up both ends of a tunnel. Returns (proxy, controller).
func pairYamuxBoth(t *testing.T) (*yamux.Session, *yamux.Session) {
	t.Helper()
	var a, b = net.Pipe()
	var cfg = yamux.DefaultConfig()
	cfg.EnableKeepAlive = false
	cfg.LogOutput = io.Discard

	var controller, err = yamux.Client(a, cfg)
	if err != nil {
		t.Fatalf("yamux session failed: %v", err)
	}
	var proxy *yamux.Session
	proxy, err = yamux.Server(b, cfg)
	if err != nil {
		t.Fatalf("yamux session failed: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = proxy.Close()
	})
	return proxy, controller
}

// readEvents accepts the upstream stream and returns the first `expected` events,
// in arrival order.
func readEvents(t *testing.T, controller *yamux.Session, expected int) []Event {
	t.Helper()
	var stream, err = controller.Accept()
	if err != nil {
		t.Fatalf("upstream stream not accepted: %v", err)
	}
	defer stream.Close() //nolint:errcheck

	var br = bufio.NewReader(stream)
	var req DialRequest
	if err = readLine(br, &req); err != nil {
		t.Fatalf("unreadable preamble: %v", err)
	}
	if req.Op != OpEvents {
		t.Fatalf("preamble = %q, expected %q", req.Op, OpEvents)
	}

	var out []Event
	var i int
	for i = 0; i < expected; i++ {
		var ev Event
		if err = readLine(br, &ev); err != nil {
			t.Fatalf("event %d unreadable: %v", i+1, err)
		}
		out = append(out, ev)
	}
	return out
}

// TestSendEventsDeliversBacklogBeforeSnapshot: backlog, then snapshot, then live.
//
// The scenario is a real reconnection. During the outage, the monitor kept pinging:
// its queue holds two transitions nobody saw (A down, A up). On reconnect, its
// snapshot says the CURRENT state (A online). Then monitoring resumes and produces
// live events (B down).
//
// The expected order is chronological: what was observed earliest first, and the
// snapshot -- which describes the instant of reconnect -- after everything that
// precedes it.
func TestSendEventsDeliversBacklogBeforeSnapshot(t *testing.T) {
	var proxy, controller = pairYamuxBoth(t)

	// The queue survives the outage: it is already filled when the stream opens.
	var events = make(chan Event, 16)
	events <- Event{Type: EventDeviceDown, DeviceID: "A", Misses: 3}
	events <- Event{Type: EventDeviceUp, DeviceID: "A"}

	var snapshot = func() []Event {
		return []Event{{Type: EventDeviceUp, DeviceID: "A", Resync: true}}
	}

	var done = make(chan error, 1)
	go func() {
		done <- SendEvents(proxy, events, snapshot)
	}()

	// Live events are posted only after a delay so they cannot be confused with the
	// backlog: this test must fail if order comes from scheduling luck rather than code.
	go func() {
		time.Sleep(50 * time.Millisecond)
		events <- Event{Type: EventDeviceDown, DeviceID: "B", Misses: 3}
		close(events)
	}()

	var got = readEvents(t, controller, 4)
	// Stream close after the last event is not a failure: what is exercised here is
	// order, not how the session ends.
	var sendErr = <-done
	if sendErr != nil {
		t.Logf("SendEvents returned: %v", sendErr)
	}

	var expected = []struct {
		typ      string
		device   string
		resync   bool
		position string
	}{
		{EventDeviceDown, "A", false, "backlog 1"},
		{EventDeviceUp, "A", false, "backlog 2"},
		{EventDeviceUp, "A", true, "snapshot"},
		{EventDeviceDown, "B", false, "live"},
	}
	var i int
	for i = range expected {
		if got[i].Type != expected[i].typ || got[i].DeviceID != expected[i].device ||
			got[i].Resync != expected[i].resync {
			t.Errorf("rank %d (%s): got {%s %s resync=%v}, expected {%s %s resync=%v}",
				i+1, expected[i].position, got[i].Type, got[i].DeviceID, got[i].Resync,
				expected[i].typ, expected[i].device, expected[i].resync)
		}
	}
}

// TestSendEventsWithoutSnapshot: nil snapshot remains valid and does not change the flow.
//
// Not every caller has state to re-announce, and nil must not panic on the product's
// most sensitive path.
func TestSendEventsWithoutSnapshot(t *testing.T) {
	var proxy, controller = pairYamuxBoth(t)

	var events = make(chan Event, 4)
	events <- Event{Type: EventDeviceUp, DeviceID: "A"}

	go func() {
		_ = SendEvents(proxy, events, nil)
	}()

	var got = readEvents(t, controller, 1)
	if got[0].Type != EventDeviceUp || got[0].DeviceID != "A" {
		t.Errorf("got {%s %s}, expected {%s A}", got[0].Type, got[0].DeviceID,
			EventDeviceUp)
	}
	if got[0].Resync {
		t.Error("no snapshot requested: nothing should be marked as resync")
	}
}

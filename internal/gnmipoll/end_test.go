package gnmipoll

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"meshcraft/internal/metricbuf"
)

// What this file tests: that "the device refused the login" and "the device did
// not answer" stay TOLD APART all the way out of the transport, and that
// everything else is honestly called unknown rather than rounded to whichever of
// the two is nearest.
//
// The distinction is the operational content of the whole repair. Both failures
// produce the same symptom — an empty graph and a subscription that will not
// stay up — and they are fixed by different people doing different things: one
// re-enters a credential in meshcraft, the other goes and looks at a switch.
// Getting it wrong is worse than not saying: an operator sent to rotate a
// credential that was never the problem finds nothing wrong with it, and learns
// to distrust the report.

// TestClassifyEndSeparatesAuthFromUnreachable locks the code → class table.
//
// Read in full, including the cases that must NOT be actionable, because that is
// where an omission is silent: a status this build has never seen would fall to
// whatever the switch's default happens to be, and a default of "auth" would
// accuse a credential on every firmware quirk.
func TestClassifyEndSeparatesAuthFromUnreachable(t *testing.T) {
	var cas = []struct {
		name string
		err  error
		want EndKind
	}{
		// Nothing went wrong.
		{"nil", nil, EndClean},
		// A gRPC server ending a stream with an OK status. An ordinary
		// configuration save bounces the gNMI agent and does this several times a
		// day on a fleet; treating it as a fault would alarm on every `write
		// memory`.
		{"eof", io.EOF, EndClean},
		{"eof wrapped", fmt.Errorf("stream over: %w", io.EOF), EndClean},
		// Our own gesture: a watch re-push, a device leaving supervision, the
		// proxy shutting down. Never a fact about the device.
		{"cancelled", context.Canceled, EndClean},
		{"status ok", status.Error(codes.OK, ""), EndClean},

		// THE INCIDENT. EOS authenticates a Subscribe against AAA and refuses a
		// stale password with this code.
		{"unauthenticated", status.Error(codes.Unauthenticated,
			"failed to authenticate"), EndAuth},
		// The account exists and may not subscribe — a different repair from a
		// wrong password, but the same person and the same screen.
		{"permission denied", status.Error(codes.PermissionDenied,
			"not authorized for gnmi"), EndAuth},
		// The status survives %w wrapping, which is why the dial path wraps that
		// way. Cf. transport.go.
		{"unauthenticated wrapped", fmt.Errorf("gnmi dial dev-1: %w",
			status.Error(codes.Unauthenticated, "no")), EndAuth},

		// Off, firewalled, or not serving gNMI at all.
		{"unavailable", status.Error(codes.Unavailable,
			"connection refused"), EndUnreachable},
		{"deadline", status.Error(codes.DeadlineExceeded, "too slow"), EndUnreachable},

		// A box that answers gRPC and does not implement gNMI. Not a credential
		// problem and not reachability, and saying so is more useful than
		// rounding it to either.
		{"unimplemented", status.Error(codes.Unimplemented,
			"unknown service gnmi.gNMI"), EndUnknown},
		{"internal", status.Error(codes.Internal, "firmware"), EndUnknown},
		// A status FLATTENED into text by a %v wrap — one of gnmic's two error
		// paths does exactly this. The class degrades to unknown, which is the
		// acceptable direction: the alternative is matching substrings of somebody
		// else's error message, and that starts lying the day it is reworded.
		{"flattened", errors.New("failed to create a subscribe client, " +
			"target='dev-1'. err=rpc error: code = Unauthenticated"), EndUnknown},
		{"plain", errors.New("something"), EndUnknown},
	}
	var i int
	for i = range cas {
		var got = ClassifyEnd(cas[i].err)
		if got != cas[i].want {
			t.Errorf("%s: classified %q, want %q", cas[i].name, got, cas[i].want)
		}
	}
}

// TestRunReturnsWhyTheStreamEnded: the reason reaches the caller.
//
// THE defect, stated as a test. `Run` used to return a flat nil whenever the
// channel closed, so a subscription refused by AAA and a device closing a
// healthy stream were the same value to everything above — and everything above
// read that value as "the target is healthy", reset its backoff, and went round
// again. There was no fact anywhere for a supervisor to act on, which is why
// nothing reported an hour of dark telemetry.
func TestRunReturnsWhyTheStreamEnded(t *testing.T) {
	var refused = status.Error(codes.Unauthenticated, "authentication failed")
	var sub = &fakeSubscriber{closeAfter: true, endErr: refused}
	var c = Collector{
		Target: Target{DeviceID: "dev-core01", IP: "192.0.2.5"},
		Metric: MetricIfCounters,
	}
	var err = c.Run(context.Background(), sub, func([]metricbuf.Sample) {})
	if err == nil {
		t.Fatal("a stream refused by the device ended as if it had finished " +
			"normally: this is the silence the batch exists to remove")
	}
	if ClassifyEnd(err) != EndAuth {
		t.Errorf("the reason arrived but not the class: %v", err)
	}
}

// TestRunEndsCleanWhenTheStreamSaysNothing: a subscriber that reports no reason
// is an ordinary end, not a fault.
//
// The property that keeps [Stream.Reason]'s nil tolerance from becoming a
// second silence — and the case every test fake in the tree relies on.
func TestRunEndsCleanWhenTheStreamSaysNothing(t *testing.T) {
	var sub = &fakeSubscriber{closeAfter: true}
	var c = Collector{Target: Target{DeviceID: "dev-1"}, Metric: MetricIfState}
	var err = c.Run(context.Background(), sub, func([]metricbuf.Sample) {})
	if err != nil {
		t.Fatalf("a stream that simply ended reported %v", err)
	}
}

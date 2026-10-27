package gnmipoll

// Why a subscription ENDED, as a class an operator can act on.
//
// # The silence this file exists to break
//
// core01.lab, 2026-08-10. The site admin credential was rotated on the
// device; the `gnmi` credential is a separate `site_credentials` row holding its
// own copy of that password, so it went stale at the same instant. The proxy
// resubscribed for over an hour with a password the switch refused, and nothing
// upstream said so: the controller kept logging `gnmi_targets:1`, which is true
// and useless — it says a credential RESOLVED, not that it WORKS.
//
// [Stream] is what carries the reason out of the transport at all. This file is
// what turns it into a fact an operator can act on, because "authentication
// refused" and "the box did not answer" are repaired by different people doing
// different things: one is a credential to re-enter in meshcraft, the other is a
// cable, a VLAN, or a switch that is off.
//
// # What the wire actually supports, established rather than assumed
//
// gNMI is gRPC, so a subscription that fails ends with a gRPC STATUS, and the
// status carries a numeric code from a closed enumeration. That is a real
// distinction on the wire and not one invented here:
//
//   - EOS authenticates a Subscribe against AAA and rejects a bad login with
//     `codes.Unauthenticated`; an account that exists but may not subscribe gets
//     `codes.PermissionDenied`. Both mean THE LOGIN IS THE PROBLEM;
//   - a device that is off, firewalled, or not serving gNMI fails the connection
//     and gRPC reports `codes.Unavailable`.
//
// It arrives on the RECV path, which is the one that matters:
// `github.com/openconfig/gnmic/pkg/api` hands the raw `SubscribeClient.Recv()`
// error to its error channel without wrapping it, so the status survives intact
// and [ClassifyEnd] can read it. That is where an authentication refusal lands —
// for a streaming RPC the server's status is delivered on the first Recv, not
// from the call that opens the stream.
//
// # Where the distinction is LOST, said rather than papered over
//
// Two paths flatten a status into a string and the class degrades to
// [EndUnknown] there:
//
//   - gnmic's other error path wraps with `fmt.Errorf(... %v)` ("failed to
//     create a subscribe client"). For a streaming RPC that call fails only when
//     the connection itself cannot carry a stream, so an auth refusal does not
//     normally land there;
//   - a TLS handshake that fails — a self-signed certificate with no
//     [Target.SkipVerify], a [Target.CAPEM] that does not match — is reported by
//     gRPC as `codes.Unavailable` with the reason in the message text. It is
//     therefore classified [EndUnreachable]. That is imprecise (the device
//     answered; it is the TRUST that failed) and it is left imprecise on
//     purpose: recovering it would mean matching substrings of a gRPC message,
//     which is a distinction the code enum does not make and which would start
//     lying the day the message is reworded.
//
// Degrading to "I could not tell" is the acceptable direction. Guessing "auth"
// from a message that merely mentions authentication is not: it would send an
// operator to rotate a credential that was never the problem.

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// EndKind is why a subscription stopped.
//
// A string and not an int, so that it can be logged, put on the wire, and read
// in a journal without a lookup table — the same reason `verdictLabel` exists in
// internal/api. The zero value is [EndClean] because a nil error is the ordinary
// case and must not need a constructor.
type EndKind string

const (
	// EndClean: the stream ended without a fault. A device that closed it, a
	// context that was cancelled, an agent restarted by a config save. Nothing to
	// report: the supervisor resubscribes and that is the whole story.
	EndClean EndKind = "clean"
	// EndAuth: the device REFUSED THE LOGIN. Never recovers on its own — no
	// amount of backoff fixes a wrong password — and the gesture that repairs it
	// is on the controller, not on the switch.
	EndAuth EndKind = "auth"
	// EndUnreachable: the device did not answer. Includes the TLS-trust case, cf.
	// the file header.
	EndUnreachable EndKind = "unreachable"
	// EndUnknown: it failed and the transport did not say in a form this build
	// can read. An HONEST value, and the one that must exist: without it every
	// unclassifiable failure would be filed as one of the two above, and the
	// class would stop meaning anything.
	EndUnknown EndKind = "unknown"
)

// ClassifyEnd names why a subscription ended.
//
// Nil is [EndClean], so a caller can classify unconditionally and branch once
// instead of testing the error and then classifying it — which is what
// cmd/proxy's supervisor does.
//
// `io.EOF` is clean and that is worth stating: a gRPC server ending a stream
// with an OK status surfaces exactly like that, and a device whose gNMI agent is
// restarted by an ordinary configuration save does it several times a day.
// Treating it as a fault would raise an alarm on every `write memory` on the
// fleet.
func ClassifyEnd(err error) EndKind {
	if err == nil {
		return EndClean
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		// Context cancellation is the collector STOPPING this subscription — a
		// watch re-push, a device leaving supervision, the proxy shutting down.
		// It is our own gesture and never a fact about the device.
		return EndClean
	}
	// FromError unwraps: since grpc-go 1.51 it looks for a GRPCStatus() anywhere
	// in the error chain, so an error wrapped with %w keeps its code. It is the
	// %v wraps that lose it, and those land on `ok == false` below.
	var st, ok = status.FromError(err)
	if !ok {
		return EndUnknown
	}
	switch st.Code() {
	case codes.OK:
		return EndClean
	case codes.Unauthenticated, codes.PermissionDenied:
		return EndAuth
	case codes.Unavailable, codes.DeadlineExceeded:
		return EndUnreachable
	}
	// Everything else — Unimplemented from a box that does not serve gNMI,
	// InvalidArgument from a path this build asks for and the target does not
	// model, Internal from firmware. None of them is an authentication problem
	// and none of them is reachability, and saying so is more useful than
	// rounding them to the nearest neighbour.
	return EndUnknown
}

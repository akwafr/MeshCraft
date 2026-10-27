package mdpv1

import (
	"errors"
	"testing"
)

// The error envelope's one obligation: a caller on the controller side must
// see EXACTLY what an in-tree driver would have handed it. errors.Is keeps
// working, Unsupported keeps its operator-facing sentence — across a wire
// that only carries JSON.
func TestErrorEnvelopeRoundTripsTheInProcessErrors(t *testing.T) {
	var refusal = NotSupported("mikrotik-swos", "declare a trap receiver",
		"this platform has no trap client at all")
	var body = ErrorBodyOf(refusal)
	if body.Code != ErrCodeUnsupported {
		t.Fatalf("an Unsupported must classify as %q, got %q", ErrCodeUnsupported, body.Code)
	}
	var back = body.ToError("mikrotik-swos")
	var unsup *Unsupported
	if !errors.As(back, &unsup) {
		t.Fatal("the rebuilt error lost its Unsupported identity")
	}
	if unsup.Because != "this platform has no trap client at all" {
		t.Fatalf("the operator's sentence did not survive the wire: %q", unsup.Because)
	}
	if !errors.Is(back, ErrNotImplemented) {
		t.Fatal("Unsupported must keep unwrapping to ErrNotImplemented, wire or not")
	}

	var body2 = ErrorBodyOf(ErrNotImplemented)
	if body2.Code != ErrCodeNotImplemented {
		t.Fatalf("ErrNotImplemented must classify as %q, got %q", ErrCodeNotImplemented, body2.Code)
	}
	if !errors.Is(body2.ToError("x"), ErrNotImplemented) {
		t.Fatal("errors.Is(…, ErrNotImplemented) must answer true after the round trip")
	}

	var plain = ErrorBodyOf(errors.New("ssh: handshake failed"))
	if plain.Code != ErrCodeDeviceError || plain.Detail == "" {
		t.Fatalf("an ordinary error is a device_error with its message, got %+v", plain)
	}
}

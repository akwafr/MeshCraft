package edgeswitch

import (
	"errors"
	"testing"
)

// A refusal must CONTINUE the candidate loop; anything else aborts it. The
// distinction decides whether the vault's older generations and the fallback
// account are ever tried, so each shape is pinned — including the FASTPATH
// keyboard-interactive wording ("unexpected message type 51 (expected 60)"),
// which read as "unreachable" until 2026-08-14 and stopped the loop at the
// first secret.
func TestIsSSHAuthFailureShapes(t *testing.T) {
	var refusals = []string{
		"ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password]",
		"ssh: handshake failed: ssh: unexpected message type 51 (expected 60)",
		"permission denied (password)",
	}
	var s string
	for _, s = range refusals {
		if !isSSHAuthFailure(errors.New(s)) {
			t.Errorf("refusal not recognized (would abort the candidate loop): %q", s)
		}
	}
	var fatal = []string{
		"TCP 192.0.2.4:22 : dial tcp 192.0.2.4:22: connect: no route to host",
		"ssh: handshake failed: read tcp: connection reset by peer",
	}
	for _, s = range fatal {
		if isSSHAuthFailure(errors.New(s)) {
			t.Errorf("transport failure classed as refusal (would burn the try budget): %q", s)
		}
	}
	if isSSHAuthFailure(nil) {
		t.Error("nil error classed as refusal")
	}
}

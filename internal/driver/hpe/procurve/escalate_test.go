package procurve

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

type bufCloser struct{ bytes.Buffer }

func (b *bufCloser) Close() error { return nil }

func feed(s string) chan byte {
	var ch = make(chan byte, len(s))
	var i int
	for i = 0; i < len(s); i++ {
		ch <- s[i]
	}
	return ch
}

// CAPTURED on a 2810-24G N.11.15 with no manager password: "enable" goes
// straight from ">" to "#".
func TestEscalateFromOperatorWithoutManagerPassword(t *testing.T) {
	var in = &bufCloser{}
	var c = &cliConn{stdin: in, out: feed("enable\x1bE\x1b[200;1HSW-PLAYER-90# ")}
	var err = c.escalate(context.Background(), "SW-PLAYER-90> ")
	if err != nil {
		t.Fatalf("escalate: %v", err)
	}
	if !strings.Contains(in.String(), "enable\n") {
		t.Errorf("enable was not sent: %q", in.String())
	}
}

// A manager password is set: back out rather than type the login secret.
func TestEscalateRefusesManagerPasswordPrompt(t *testing.T) {
	var in = &bufCloser{}
	var c = &cliConn{stdin: in, out: feed("enable\x1bEPassword: ")}
	var err = c.escalate(context.Background(), "SW-PLAYER-90> ")
	if err == nil || !strings.Contains(err.Error(), "manager") {
		t.Fatalf("expected a manager-password refusal, got %v", err)
	}
}

// Already at manager level: nothing is sent.
func TestEscalateNoopAtManagerLevel(t *testing.T) {
	var in = &bufCloser{}
	var c = &cliConn{stdin: in, out: make(chan byte)}
	var err = c.escalate(context.Background(), "SW-PLAYER-79# ")
	if err != nil || in.Len() != 0 {
		t.Fatalf("got err=%v sent=%q", err, in.String())
	}
}

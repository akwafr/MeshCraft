package api

import (
	"strings"
	"testing"
)

func TestRedactSerialLogLine(t *testing.T) {
	var cases = []struct {
		in, want string
	}{
		{"setting enable password…", "setting enable password…"},
		{"password hunter2", "password [redacted]"},
		{"Password: hunter2", "Password:[redacted]"},
		{"trying 115200 8N1…", "trying 115200 8N1…"},
		{"login wait: serial expect timeout", "login wait: serial expect timeout"},
	}
	var c struct{ in, want string }
	for _, c = range cases {
		var got = redactSerialLogLine(c.in)
		if got != c.want {
			t.Fatalf("redact(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// A MULTI-LINE payload is the serial capture tail, and redacting it must not
// truncate the transcript.
//
// REGRESSION, 2026-08-06: it did, and the loss was read as evidence. A failing
// Arista run appeared to show the console going SILENT right after the credential
// change — in fact the echoes of the next three commands had been redacted away
// by this function, and a real defect was diagnosed out of the artifact.
func TestRedactSerialLogKeepsTheRestOfACapture(t *testing.T) {
	var capture = "localhost(config)#username admin privilege 15 secret hunter2\r\n" +
		"localhost(config)#enable secret hunter2\r\n" +
		"localhost(config)#end\r\n" +
		"localhost#write memory\r\n" +
		"Copy completed successfully.\r\n"
	var got = redactSerialLogLine(capture)

	if strings.Contains(got, "hunter2") {
		t.Fatalf("the secret survived redaction:\n%s", got)
	}
	// Everything AFTER the first secret must still be there — that is the half
	// an operator reads to find out what the device actually did.
	var must []string
	for _, must = range [][]string{{"end"}, {"write memory"}, {"Copy completed successfully."}} {
		if !strings.Contains(got, must[0]) {
			t.Errorf("the transcript lost %q after redaction:\n%s", must[0], got)
		}
	}
	// The line structure survives: a transcript is read as it arrived.
	if strings.Count(got, "\n") != strings.Count(capture, "\n") {
		t.Errorf("line count changed: %d -> %d", strings.Count(capture, "\n"), strings.Count(got, "\n"))
	}
	if !strings.Contains(got, "secret [redacted]\r") {
		t.Errorf("the CRLF of the redacted line was lost:\n%q", got)
	}
}

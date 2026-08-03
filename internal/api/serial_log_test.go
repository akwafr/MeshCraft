package api

import "testing"

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

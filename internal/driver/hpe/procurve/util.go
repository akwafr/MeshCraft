package procurve

import (
	"strings"
	"sync"
)

// lockedBuilder collects shell output from the two goroutines x/crypto/ssh uses
// for stdout and stderr. A plain strings.Builder is not safe across them, and the
// race shows up as truncated configuration rather than as a crash.
type lockedBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuilder) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func firstLine(s string) string {
	var i = strings.IndexAny(s, "\r\n")
	if i >= 0 {
		return s[:i]
	}
	return s
}

// isSSHAuthFailure recognizes "the device refused this password" and nothing
// else, so a caller can advance its credential list without walking it against a
// switch that is merely unreachable (cf. driver/connector.go on try budgets:
// these ranges lock accounts).
func isSSHAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	var s = strings.ToLower(err.Error())
	return strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "no supported methods remain") ||
		strings.Contains(s, "permission denied")
}

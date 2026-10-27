package api

import (
	"errors"
	"testing"
)

// The single most dangerous decision in credential placement: whether the rename
// sequence deletes the account it entered through. Three conditions, all load-
// bearing, locked as a truth table:
//
//   - only a RENAME leaves an old account behind (SetSecret changes in place);
//   - only a VERIFIED new door justifies closing the old one — deleting on an
//     unverified write is the sequence that costs a site visit;
//   - an operator's explicit KEEP wins over both: first placement on a takeover
//     or lab device must be able to leave the only historically proven door in
//     place, deliberately and traced.
func TestShouldRemovePreviousAccount(t *testing.T) {
	var failed = errors.New("verification failed")
	var cases = []struct {
		name      string
		renaming  bool
		verifyErr error
		keep      bool
		want      bool
	}{
		{"nominal rotation: rename verified, no keep → remove", true, nil, false, true},
		{"not a rename: nothing to remove", false, nil, false, false},
		{"verify failed: old door is the ONLY door, never remove", true, failed, false, false},
		{"operator keeps the previous account: never remove", true, nil, true, false},
		{"keep wins even when everything else says remove", true, nil, true, false},
		{"verify failed and keep: still nothing removed", true, failed, true, false},
	}
	var c struct {
		name      string
		renaming  bool
		verifyErr error
		keep      bool
		want      bool
	}
	for _, c = range cases {
		var got = shouldRemovePreviousAccount(c.renaming, c.verifyErr, c.keep)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

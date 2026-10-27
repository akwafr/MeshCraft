package store

import "testing"

// SetDeviceIdentity's protection does not live in Go: it lives in the SQL —
// `coalesce(nullif($2, ''), model)`. Same situation as devices_test.go for
// DevicesWatchedBy: the package has no database, so the test replays the
// DECISION TABLE of the expression, transcribed word for word. Both must move
// together; the expression itself was exercised on the deployment's PostgreSQL
// (SELECT replay: empty-model keep, model replace, NULL-extra fill) before
// being written.

// modelAfterSet transcribes `coalesce(nullif(new, ”), old)`.
func modelAfterSet(old, new string) string {
	if new == "" {
		return old
	}
	return new
}

// TestEmptyModelDoesNotOverwrite: the invariant the API layer also applies
// (identityHealDecision), repeated at the layer that survives the next caller —
// an empty observation NEVER blanks a stored model.
func TestEmptyModelDoesNotOverwrite(t *testing.T) {
	if modelAfterSet("S3900-24T4S", "") != "S3900-24T4S" {
		t.Fatal("an empty model must keep the stored one")
	}
}

func TestObservedModelReplaces(t *testing.T) {
	if modelAfterSet("", "S3900-24T4S") != "S3900-24T4S" {
		t.Fatal("an empty stored model must take the observation")
	}
	if modelAfterSet("WS-C2960X-24PS-L", "WS-C2960X-48FPD-L") != "WS-C2960X-48FPD-L" {
		t.Fatal("a differing observation must replace the stored model")
	}
}

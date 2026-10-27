package api

import (
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/store"
)

// What an operator SEES when a second credential moved with a rotation.
//
// WHAT THIS REACHES: only the journal line, which is a pure function
// (credResyncMessage), and the shape of what the store hands back. `Server.Store` is a
// concrete `*store.Store`, so `handleRotateCredential` cannot be entered without
// PostgreSQL — the whole package deliberately avoids one (cf. dhcpsnoop_apply_test.go's
// header). That the handler emits this line at all, and only when something moved, is
// visible by reading it and is NOT asserted here.
//
// The rule that decides WHICH rows move, and the transaction they move in, live in
// internal/store/credresync.go and are pinned there.

func oneMove() []store.SiblingResync {
	return []store.SiblingResync{{
		CredentialID: "cred-gnmi", Kind: "gnmi", Username: "mcadmin", Generation: 3,
	}}
}

// The line has to answer the question its reader actually has — "why did telemetry stop
// when I rotated?" — so it names the row, the account, the new generation, and the fact
// that these rows are copies of one login.
func TestResyncMessageNamesWhatMoved(t *testing.T) {
	var msg = credResyncMessage("mcadmin@site", 5, oneMove())
	var want string
	for _, want = range []string{"mcadmin@site", "generation 5", "gnmi", "mcadmin",
		"generation 3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the journal line must mention %q: %s", want, msg)
		}
	}
}

// THE SECRET NEVER APPEARS, and the way to prove that about a string builder is to prove
// it is never handed one. The rotation's cleartext password is in scope at the call site
// (`in.Secret`), one line above the emit; the only thing standing between it and a
// retained audit trail is that this function has nowhere to receive it. So the SIGNATURE
// is what is pinned — a label, a number, and rows of identity. The field set of those
// rows is pinned by the test below, and between the two there is no input that could
// carry secret material.
func TestResyncMessageIsNeverGivenASecret(t *testing.T) {
	var typ = reflect.TypeOf(credResyncMessage)
	var want = []reflect.Type{
		reflect.TypeOf(""), reflect.TypeOf(0), reflect.TypeOf([]store.SiblingResync{}),
	}
	if typ.NumIn() != len(want) {
		t.Fatalf("credResyncMessage takes %d arguments, expected %d: a new one is how a "+
			"password reaches the site journal", typ.NumIn(), len(want))
	}
	var i int
	for i = range want {
		if typ.In(i) != want[i] {
			t.Errorf("argument %d is %s, expected %s", i, typ.In(i), want[i])
		}
	}
}

// THE REPORT'S FIELDS ARE FROZEN, and that is the real guard on the line above.
//
// `SiblingResync` travels into the site journal, which is read by anyone with `operator`
// on the site and is retained. A future field named `Secret`, `Password` or `SecretEnc`
// added "so the caller can verify what was written" would put a fleet administration
// password into that trail with no screen saying so — and the message builder, which
// iterates nothing, would not even have to change for a JSON payload to carry it. So the
// field set is pinned: adding one is a decision that has to be made here, in front of
// this comment.
func TestSiblingResyncCarriesIdentityOnly(t *testing.T) {
	var want = map[string]bool{
		"CredentialID": true, "Kind": true, "Username": true, "Generation": true,
	}
	var typ = reflect.TypeOf(store.SiblingResync{})
	var i int
	for i = 0; i < typ.NumField(); i++ {
		var name = typ.Field(i).Name
		if !want[name] {
			t.Errorf("SiblingResync grew field %q: it is journalled, so anything that "+
				"could hold secret material must not be added here", name)
		}
		delete(want, name)
	}
	var missing string
	for missing = range want {
		t.Errorf("SiblingResync lost field %q, which the journal line reads", missing)
	}
}

// Plural and singular both have to read as a sentence: this line is written at WARNING
// and somebody is reading it during an incident.
func TestResyncMessageCountsCorrectly(t *testing.T) {
	var one = credResyncMessage("mcadmin@site", 5, oneMove())
	if !strings.Contains(one, "1 other credential naming") {
		t.Errorf("singular reads wrong: %s", one)
	}
	var two = credResyncMessage("mcadmin@site", 5, append(oneMove(),
		store.SiblingResync{CredentialID: "c2", Kind: "netconf", Username: "mcadmin",
			Generation: 2}))
	if !strings.Contains(two, "2 other credentials naming") {
		t.Errorf("plural reads wrong: %s", two)
	}
	if !strings.Contains(two, "netconf") {
		t.Errorf("every row that moved must be named: %s", two)
	}
}

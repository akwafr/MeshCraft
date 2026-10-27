package eos

import (
	"strings"
	"testing"
)

// A REFUSED "enable" MUST NOT READ AS SUCCESS.
//
// EOS answers a wrong enable secret with "% Bad secret" and then re-prints the
// USER EXEC prompt. Because the connector's prompt matcher accepts ">" and
// "#" alike, the refusal is invisible unless the privileged prompt is asserted
// explicitly — and a session that silently stays at ">" types every subsequent
// configuration command into a prompt that cannot run it.
//
// On the lab fleet the account secret and the enable password are the same
// string, so this path has never actually been exercised in anger — which is
// exactly why it is pinned here rather than left to a live capture. A device
// provisioned out of step is enough to fire it, and the symptom would be a
// "% Invalid input" on a command that is not wrong at all.
func TestUserExecPromptIsNotAcceptedAsPrivileged(t *testing.T) {
	// What the device leaves on the wire when the secret is wrong.
	var refused = "% Bad secret\ncore01-lab>"
	if rePromptPriv.MatchString(tail(refused, 200)) {
		t.Fatal("the user EXEC prompt matched rePromptPriv: a refused \"enable\" " +
			"will be reported as success and the write will be typed at \">\"")
	}
	if !rePromptAny.MatchString(tail(refused, 200)) {
		t.Fatal("rePromptAny does not match \">\" — the connector would hang " +
			"rather than report the refusal")
	}
	// And the refusal itself must be recognised as a refusal, not mistaken for
	// ordinary output.
	if !reCLIError.MatchString(refused) {
		t.Error("\"% Bad secret\" is not recognised as a CLI refusal")
	}
}

// The privileged prompt, and the config sub-modes, must still match — a fix that
// tightened this into rejecting "#" would break every write instead.
func TestPrivilegedPromptsStillMatch(t *testing.T) {
	var ok = []string{
		"core01-lab#",
		"core01-lab(config)#",
		"core01-lab(config-if-Et1-24,49-52)#",
	}
	var p string
	for _, p = range ok {
		if !rePromptPriv.MatchString(p) {
			t.Errorf("privileged prompt not matched: %q", p)
		}
	}
	if strings.Contains(ok[0], ">") {
		t.Fatal("fixture is wrong")
	}
}

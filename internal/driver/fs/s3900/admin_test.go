package s3900

// THE LAST-ACCOUNT GUARD, ported here on 2026-08-12 with no device touched.
//
// Until then [Client.DeleteAccount] compared the target against the CURRENT
// SESSION's name and nothing else — so "delete admin while logged in as
// mcadmin" went straight through, the shape that halted a real deletion on
// `ubiquiti/edgeswitch` and that `instanton` carried until the same day.
//
// Every refusal below asserts TWICE: that the method said no, and that NOTHING
// REACHED THE DEVICE. A guard that regrets a command after sending it is not a
// guard, and on this connector the write is a "no username" that no re-read
// undoes.

import (
	"context"
	"strings"
	"testing"
)

// runWithAccounts builds a "show run" around the account lines given, in the
// two-lines-per-account shape this platform actually prints (cf. reRunUsername).
func runWithAccounts(usernameLines ...string) string {
	var b strings.Builder
	b.WriteString("!\nhostname \"labs3900\"\n!\n")
	var l string
	for _, l = range usernameLines {
		b.WriteString(l + "\n")
	}
	b.WriteString("!\ninterface vlan 99\n ip address 192.0.2.7 255.255.0.0\n!\n")
	return b.String()
}

func adminLines(name string) []string {
	return []string{
		"username " + name + " privilege 15",
		"username " + name + " password 7 aabbccddeeff00112233445566778899",
	}
}

// assertNothingWritten: no configuration mode, no account command. The read
// itself ("show run") is allowed — it is the whole point of the guard.
func assertNothingWritten(t *testing.T, dev *scriptedCLI) {
	t.Helper()
	var cmd string
	for _, cmd = range dev.sent {
		var low = strings.ToLower(strings.TrimSpace(cmd))
		if low == "conf t" || low == "configure" || strings.Contains(low, "username") {
			t.Fatalf("a refused deletion still sent %q — the whole command list was %v",
				cmd, dev.sent)
		}
	}
}

// THE GAP THAT WAS OPEN: the target is the only account the device enumerates,
// and it is not the session's own name, so the pre-2026-08-12 code deleted it.
//
// The session account being ABSENT from the enumeration is not an artifice of
// the fixture: it is the only shape this refusal can ever fire in, because a
// target equal to the session name is stopped one refusal earlier.
func TestDeleteAccountRefusesTheOnlyAccountTheDeviceEnumerates(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return runWithAccounts(adminLines("admin")...), true
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("the last account the device enumerates was deleted")
	}
	if !strings.Contains(err.Error(), "ONLY administration account") {
		t.Errorf("the refusal must name its reason: %v", err)
	}
	assertNothingWritten(t, dev)
}

// THE OTHER HALF, without which the guard would just be a broken DeleteAccount:
// a non-last account still goes, and the exact captured-shape command is what
// gets sent.
func TestDeleteAccountStillDeletesANonLastAccount(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return runWithAccounts(append(adminLines("admin"),
				adminLines("mcadmin")...)...), true
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "admin")
	if err != nil {
		t.Fatalf("two accounts are enumerated: deleting one of them must work: %v", err)
	}
	var joined = strings.Join(dev.sent, "|")
	if !strings.Contains(joined, "no username admin") {
		t.Errorf("the deletion command never reached the device: %v", dev.sent)
	}
	if !strings.Contains(joined, "conf t") || !strings.Contains(joined, "end") {
		t.Errorf("the write must ride the usual configuration block: %v", dev.sent)
	}
	// The success line must not claim more than the guard can check.
	var last = c.Steps[len(c.Steps)-1]
	if !last.OK || !strings.Contains(last.Detail, "NOT a promise") {
		t.Errorf("the success transcript must say what the count is worth: %+v", last)
	}
}

// THE OLDEST REFUSAL, unchanged and still FREE: deleting the account the session
// rides costs not even the read.
func TestDeleteAccountStillRefusesTheCurrentSessionAccount(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return runWithAccounts(append(adminLines("admin"),
				adminLines("mcadmin")...)...), true
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "MCADMIN") // the case must not matter
	if err == nil {
		t.Fatal("the current session's own account was deleted")
	}
	if !strings.Contains(err.Error(), "current session") {
		t.Errorf("the refusal must name its reason: %v", err)
	}
	if len(dev.sent) != 0 {
		t.Errorf("this refusal is free — not even the account read is owed: %v", dev.sent)
	}
}

// THE DECISION THIS PORT HAD TO TAKE, PINNED: a read that could not be made
// REFUSES the deletion, where `eos` and `ios` proceed.
//
// This range HAS a console (RJ45, 115200 8N1) — the one word those two justify
// failing open with — and it still refuses, for the reason spelled out on
// [Client.DeleteAccount]: on this connector a failed read is a failed SESSION,
// so proceeding would not buy a deletion, it would buy an unverified one.
func TestDeleteAccountRefusesWhenTheAccountsCouldNotBeRead(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			// The session goes away mid-answer: a PREFIX, and one that still
			// enumerates an account, so nothing in the text says it is short.
			return runWithAccounts(append(adminLines("admin"),
				adminLines("mcadmin")...)...)[:60], false
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("a deletion was allowed on an account list nobody could read")
	}
	if !strings.Contains(err.Error(), "could not be read") {
		t.Errorf("the refusal must name its reason: %v", err)
	}
	assertNothingWritten(t, dev)
}

// AN EMPTY ENUMERATION IS AN UNREADABLE ONE, not an account-less switch: this
// session is open, so the device holds at least the account it was opened under.
func TestDeleteAccountRefusesAnEnumerationWithNoAccountAtAll(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return runWithAccounts(), true // a complete config, no "username" line
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("a deletion was allowed on a read that named no account at all")
	}
	if !strings.Contains(err.Error(), "unreadable one") {
		t.Errorf("the refusal must name its reason: %v", err)
	}
	assertNothingWritten(t, dev)
}

// THE CASE THAT MADE THE EMPTY REFUSAL A REFUSAL OF ITS OWN: an in-band CLI
// error is a COMPLETE answer — [Client.Accounts] is entitled to parse around it
// and comes back with zero accounts and no error, exactly the shape that forced
// `instanton` to refuse empties. Distinguishable from a truncation, and refused
// all the same.
func TestDeleteAccountRefusesWhenTheCLIRefusedTheRead(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return "% Invalid input detected at '^' marker.", true
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("a deletion was allowed after the CLI refused the account read")
	}
	assertNothingWritten(t, dev)
}

// [Client.Accounts] ITSELF: a prefix must not leave here as a short list.
//
// parseRunAccounts reports the "username" lines it reached and is silent about
// the rest, so the enumeration a lockout is decided on used to come back
// complete-looking and merely SHORT — the phantom-ports defect class, applied to
// accounts.
func TestAccountsRefusesATruncatedShowRun(t *testing.T) {
	var full = runWithAccounts(append(adminLines("admin"), adminLines("mcadmin")...)...)

	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return full[:strings.Index(full, "username mcadmin")], false
		}
		return "", true
	})
	var got, err = c.Accounts(context.Background())
	if err == nil {
		t.Fatalf("a truncated \"show run\" enumerated %d account(s) as if complete", len(got))
	}
	if len(got) != 0 {
		t.Errorf("a refused read must carry no enumeration: %+v", got)
	}

	// A COMPLETE READ IS UNCHANGED — the half that keeps the fix from being
	// worse than the defect.
	var c2, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return full, true
		}
		return "", true
	})
	got, err = c2.Accounts(context.Background())
	if err != nil {
		t.Fatalf("a complete read must enumerate: %v", err)
	}
	if len(got) != 2 || got[0].Name != "admin" || got[1].Name != "mcadmin" {
		t.Errorf("accounts = %+v, want admin and mcadmin", got)
	}
}

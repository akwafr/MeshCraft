package s3400

// THE LAST-ACCOUNT GUARD, ported here on 2026-08-12 with no device touched —
// and this was the LAST connector in the tree without one.
//
// Until then [Client.DeleteAccount] compared the target against the CURRENT
// SESSION's name and nothing else, so "delete admin while logged in as
// mcadmin" went straight through: the shape `instanton` carried until the same
// day, and the shape that halted a real deletion on `ubiquiti/edgeswitch`.
//
// Every refusal below asserts TWICE: that the method said no, and that NOTHING
// REACHED THE DEVICE. A guard that regrets a command after sending it is not a
// guard, and on this connector the write is a "no username" that no re-read
// undoes.
//
// The scripted device is [newScriptedCLI] from truncation_test.go — the only way
// to build a TRUNCATED read, because the text alone can never carry the fact.

import (
	"context"
	"strings"
	"testing"
)

// cfgWithAccounts builds a "show running-config" around the account lines
// given, in this platform's one-line-per-account shape: "username U password 0
// S", with no privilege keyword anywhere (cf. parse_test.go's labRunningConfig
// and [Client.Accounts]). The "enable password … level 15" line rides along
// because it is on every real capture and must never be mistaken for an account.
func cfgWithAccounts(names ...string) string {
	var b strings.Builder
	b.WriteString("Building configuration...\n\nCurrent configuration:\n!\n" +
		"hostname poefs3400lab\n!\n" +
		"aaa authentication login default local\n" +
		"aaa authentication enable default none\n!\n")
	var n string
	for _, n = range names {
		b.WriteString("username " + n + " password 0 SECRET\n")
	}
	b.WriteString("!\nenable password 0 SECRET level 15\n!\n" +
		"interface GigaEthernet0/1\n switchport pvid 200\n!\n")
	return b.String()
}

// assertNothingWritten: no configuration mode, no account command. The read
// itself ("show running-config") is allowed — it is the whole point of the
// guard. The mode word is compared WHOLE: "config" is this platform's config
// mode (not "conf t", cf. Driver.ResetProcedure's pitfalls) and it is also a
// substring of "show running-config".
func assertNothingWritten(t *testing.T, dev *scriptedCLI) {
	t.Helper()
	var cmd string
	for _, cmd = range dev.sent {
		var low = strings.ToLower(strings.TrimSpace(cmd))
		if low == "config" || strings.Contains(low, "username") {
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
		if strings.HasPrefix(cmd, "show running-config") {
			return cfgWithAccounts("admin"), true
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("the last account the device enumerates was deleted")
	}
	if !strings.Contains(err.Error(), "ONLY local account") {
		t.Errorf("the refusal must name its reason: %v", err)
	}
	// It must not overclaim either: this counts what the device lists, not what
	// the vault can open.
	if !strings.Contains(err.Error(), "DEVICE enumerates") {
		t.Errorf("the refusal must say what it counted: %v", err)
	}
	assertNothingWritten(t, dev)
}

// THE OTHER HALF, without which the guard would just be a broken DeleteAccount:
// a non-last account still goes, and the captured-shape command is what is sent.
func TestDeleteAccountStillDeletesANonLastAccount(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show running-config") {
			return cfgWithAccounts("admin", "mcadmin"), true
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
	// "config", not "conf t" — the S3900's word is refused on this firmware.
	if !strings.Contains(joined, "|config|") {
		t.Errorf("the write must ride this platform's configuration block: %v", dev.sent)
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
		if strings.HasPrefix(cmd, "show running-config") {
			return cfgWithAccounts("admin", "mcadmin"), true
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "MCADMIN") // the case must not matter
	if err == nil {
		t.Fatal("the current session's own account was deleted")
	}
	if !strings.Contains(err.Error(), "logged in with") {
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
// and on THIS connector a session left mid-pager also eats the first character
// of whatever is typed next, so proceeding would not buy a deletion, it would
// buy an unverified and probably mistyped one.
func TestDeleteAccountRefusesWhenTheAccountsCouldNotBeRead(t *testing.T) {
	var full = cfgWithAccounts("admin", "mcadmin")
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show running-config") {
			// The session goes away mid-answer: a PREFIX, and one that still
			// enumerates an account, so nothing in the text says it is short.
			return full[:strings.Index(full, "username mcadmin")], false
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
		if strings.HasPrefix(cmd, "show running-config") {
			return cfgWithAccounts(), true // complete, and no "username" line
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
// `instanton` to refuse empties.
//
// The refusal shape is THIS platform's, not the S3900's: no '%' prefix, but the
// echoed word, a caret line and a bare sentence (captured on Version 2.0.2J).
func TestDeleteAccountRefusesWhenTheCLIRefusedTheRead(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show running-config") {
			return "show running-config\n              ^\nUnknown command", true
		}
		return "", true
	})
	c.User = "mcadmin"

	var err = c.DeleteAccount(context.Background(), "admin")
	if err == nil {
		t.Fatal("a deletion was allowed after the CLI refused the account read")
	}
	if !strings.Contains(err.Error(), "unreadable one") {
		t.Errorf("a refused read must land on the empty-enumeration refusal: %v", err)
	}
	assertNothingWritten(t, dev)
}

// [Client.Accounts] ITSELF: a prefix must not leave here as a short list.
//
// parseCfgAccounts reports the "username" lines it reached and is silent about
// the rest, so the enumeration a lockout is decided on used to come back
// complete-looking and merely SHORT — the 2026-08-11 phantom-ports defect class
// (truncation_test.go), applied to accounts.
func TestAccountsRefusesATruncatedRunningConfig(t *testing.T) {
	var full = cfgWithAccounts("admin", "mcadmin")

	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show running-config") {
			return full[:strings.Index(full, "username mcadmin")], false
		}
		return "", true
	})
	var got, err = c.Accounts(context.Background())
	if err == nil {
		t.Fatalf("a truncated \"show running-config\" enumerated %d account(s) as if "+
			"complete", len(got))
	}
	if len(got) != 0 {
		t.Errorf("a refused read must carry no enumeration: %+v", got)
	}
	if !strings.Contains(err.Error(), "show running-config") {
		t.Errorf("the error must name the command that was cut: %v", err)
	}

	// A COMPLETE READ IS UNCHANGED — the half that keeps the fix from being
	// worse than the defect.
	var c2, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show running-config") {
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

	// AND AN IN-BAND REFUSAL IS STILL NOT A TRUNCATION: the device answered, so
	// Accounts parses around it and reports the zero accounts it found. That is
	// what makes the empty-enumeration refusal in DeleteAccount a refusal of its
	// own rather than dead code.
	var c3, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show running-config") {
			return "show running-config\n              ^\nUnknown command", true
		}
		return "", true
	})
	got, err = c3.Accounts(context.Background())
	if err != nil {
		t.Fatalf("a COMPLETE read carrying a refusal line is not a truncation: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a refused read enumerates nothing: %+v", got)
	}
}

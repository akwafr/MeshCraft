package s3400

// THE ADMIN-DOWN CAPTURE — poefs3400lab (S3400-48T4SP, Version 2.0.2J),
// 2026-08-10, over the controller's SOCKS route as `mcadmin`. CAPTURED, not
// constructed: these are the device's own words, from a shut → read → revert
// exercise on a DARK port (g0/1), reverted and confirmed. Running configuration
// only — nothing was saved to startup.
//
// This is the capture portadmin.go's header and contract.go's `port_admin` note
// said did not exist. Until today the ONLY evidence that "administratively
// down" is what this hardware prints for a shut port was the CLI family it
// belongs to; every "show interface" in testdata/ (2026-08-03/05) read "is
// up" or "is down". Both files replayed here now carry the real thing:
//
//	testdata/show_interface_g01_admin_down.txt        (shut)
//	testdata/show_interface_g01_after_no_shutdown.txt (reverted)
//
// Both are EXCERPTS of the transcript — the five lines that were transcribed
// verbatim for the shut state, the first line for the reverted one — rather than
// a whole command dump like the 2026-08-03 fixtures. Every line in them is the
// device's, no line was reflowed.
//
// # WHAT THIS EXERCISE DOES *NOT* PROVE
//
// The commands were TYPED AT THE CLI by hand ("config" → "interface g0/1" →
// "shutdown" → "exit", prompt walking Switch_config# → Switch_config_g0/1#),
// NOT driven through [Client.ApplyPortAdmin]. So the write form and the read-back
// word are now proven on this hardware, and the APPLIER is not: its guardrail,
// its "already where the plan wants it" skip and its post-write verification
// remain exercised by constructed tests only (portadmin_test.go). That is also
// why no [Driver.WritesExtra] flag moved today — the declaration was already
// there, taken on other grounds, and this capture strengthens its evidence
// without changing what has been driven end to end.

import (
	"os"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// captured reads one of today's fixture files.
func capturedPortAdmin(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(b)
}

// The shut capture must read as [driver.LinkDisabled], and the reverted one as
// [driver.LinkDown].
//
// This is the pair the applier hangs on twice over: the "already where the plan
// wants it" test skips a port whose state already matches, and the post-write
// re-read is what promotes a port into [driver.ApplyResult.PortsWritten]. Read
// admin-down as plain down and the applier would send "shutdown" to a port it
// had just shut, then report the write as unconfirmed; read plain down as
// admin-down and it would report an unplugged port as successfully shut without
// ever sending anything.
func TestParseLinkStatesOnCapturedAdminDown(t *testing.T) {
	var shut = ParseLinkStates(capturedPortAdmin(t, "show_interface_g01_admin_down.txt"))
	if shut["GE1"] != driver.LinkDisabled {
		t.Errorf("the SHUT capture reads GE1 = %q, want %q — an operator who shut a port must "+
			"not be told the cable is out", shut["GE1"], driver.LinkDisabled)
	}
	var back = ParseLinkStates(capturedPortAdmin(t, "show_interface_g01_after_no_shutdown.txt"))
	if back["GE1"] != driver.LinkDown {
		t.Errorf("the REVERTED capture reads GE1 = %q, want %q — g0/1 is a dark port, so \"no "+
			"shutdown\" leaves it down with nothing plugged in, NOT disabled", back["GE1"],
			driver.LinkDown)
	}
	if shut["GE1"] == back["GE1"] {
		t.Fatal("the two captures are the same state: this connector cannot tell a shut port " +
			"from an unplugged one, which is the whole read side of ApplyPortAdmin")
	}
}

// The long interface name on this range is "GigaEthernet0/1" — NOT
// "GigabitEthernet0/1", the spelling the rest of the industry uses and the one
// a parser borrowed from another vendor would expect.
//
// Pinned against the capture because it is a silent failure mode: a name that
// does not resolve is dropped by [Driver.StandardPortName], so a wrong prefix
// yields an EMPTY link map rather than a wrong one — and an empty map is exactly
// what [Client.ApplyPortAdmin] refuses to act on ("nothing is shut"). The gesture
// would simply never work, with no parse error anywhere.
func TestCapturedAdminDownUsesGigaEthernetNotGigabitEthernet(t *testing.T) {
	var raw = capturedPortAdmin(t, "show_interface_g01_admin_down.txt")
	if !strings.Contains(raw, "GigaEthernet0/1 ") {
		t.Fatal("the capture no longer carries the \"GigaEthernet0/N\" spelling this range prints")
	}
	if strings.Contains(raw, "GigabitEthernet") {
		t.Error("\"GigabitEthernet\" is another vendor's spelling and this hardware does not use it")
	}
	var name, ok = (&Driver{}).StandardPortName("GigaEthernet0/1")
	if !ok || name != "GE1" {
		t.Errorf("StandardPortName(GigaEthernet0/1) = %q, %v — the captured spelling must resolve",
			name, ok)
	}
}

// THE DISTINCTION IS ONE WORD, and the protocol half is useless for it.
//
// Both captures say "line protocol is down". The only difference between a port
// an operator shut and a port with no cable is "administratively" in the first
// half — so anything that keys on the protocol clause, or on a bare "down"
// anywhere in the line, reports the two as one state. reIfLine keeps the
// administrative word in its own group precisely so it does not have to.
func TestCapturedAdminDownAndPlainDownDifferByOneWord(t *testing.T) {
	var shut = firstLine(capturedPortAdmin(t, "show_interface_g01_admin_down.txt"))
	var back = firstLine(capturedPortAdmin(t, "show_interface_g01_after_no_shutdown.txt"))

	if !strings.HasSuffix(shut, "line protocol is down") ||
		!strings.HasSuffix(back, "line protocol is down") {
		t.Fatalf("both captured states end \"line protocol is down\":\n shut: %q\n back: %q",
			shut, back)
	}
	if !strings.Contains(shut, "administratively down") {
		t.Fatalf("the shut capture must carry the word the parser keys on: %q", shut)
	}
	if strings.Contains(back, "administratively") {
		t.Fatalf("the reverted capture must NOT carry it: %q", back)
	}
	if strings.Replace(shut, "administratively ", "", 1) != back {
		t.Errorf("the two lines differ by more than the word \"administratively\":\n shut: %q\n"+
			" back: %q", shut, back)
	}

	// The negative half, stated as code: a reader that trusted the protocol
	// clause alone would see one state where the device reports two.
	var protoOf = func(line string) string {
		var _, after, found = strings.Cut(line, "line protocol is ")
		if !found {
			t.Fatalf("no protocol clause in %q", line)
		}
		return after
	}
	if protoOf(shut) != protoOf(back) {
		t.Fatal("the protocol halves now differ, so this range CAN be read from that clause " +
			"alone — the comments calling it insufficient are the ones to fix")
	}
}

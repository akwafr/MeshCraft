package edgeswitch

// Tests for dhcpsnoop.go — [driver.DHCPSnoopReader] / [driver.DHCPSnoopApplier].
//
// # EVERY DHCP-SNOOPING FIXTURE IN THIS FILE IS CONSTRUCTED, NOT CAPTURED
//
// Said first because it is the fact that decides what these tests are worth.
// testdata/ holds no "show ip dhcp snooping" capture, and the 2026-08-05
// running-config dump (testdata/show_running_config.txt) carries no
// "dhcp-snooping" line of any kind — checked, not assumed. Every string
// literal below was BUILT BY HAND from the command forms and the output shape
// dhcpsnoop.go's own doc comment names, which that file states are deduced from
// the CLI reference and have never been sent to the lab EdgePoint S16.
//
// So these tests prove the connector is SELF-CONSISTENT — that it parses the
// shape it says it expects, orders its commands deterministically, never emits a
// disable form, and refuses to report a write it could not read back. They prove
// NOTHING about whether the real firmware prints that shape. When a capture is
// finally taken, the fixtures here must be replaced by it and every expectation
// re-checked; a fixture that agrees with the code because both were written from
// the same guess is not evidence.
//
// The one exception is [TestCliPortNameMatchesTheCapturedSpelling], which leans
// on testdata/show_port_all.txt — a real capture.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The optional contracts this file implements. contract.go's assertion block
// does not list them (it predates this file), so they are pinned here: a
// signature drift in driver/dhcpsnoop.go must fail a test, not go unnoticed
// until a caller's type assertion silently stops matching.
var (
	_ driver.DHCPSnoopReader  = (*Client)(nil)
	_ driver.DHCPSnoopApplier = (*Client)(nil)
)

// --- CONSTRUCTED fixtures (cf. the file header) --------------------------

// snoopRefShape is the shape dhcpsnoop.go's doc comment says the CLI reference
// prints. CONSTRUCTED FROM THAT DOCUMENTATION — never captured from hardware.
// The 3/1 row is a LAG member, which this platform answers with a non-zero
// slot and which is not a faceplate socket.
const snoopRefShape = `DHCP snooping is Disabled
DHCP snooping source MAC verification is enabled
DHCP snooping is enabled on the following VLANs:
 1, 10 - 12

Interface   Trusted    Log Invalid Pkts
----------  ---------  ----------------
0/1         Yes        No
0/2         No         Yes
3/1         Yes        No
`

// snoopOffNoVLANs: the feature never touched. CONSTRUCTED, not captured.
const snoopOffNoVLANs = `DHCP snooping is Disabled
DHCP snooping source MAC verification is enabled
`

// snoopCLIRefusal is what a firmware without the feature answers. This form IS
// real — reCLIError was built on it (ssh.go, captured 2026-08-03) — but its
// pairing with "show ip dhcp snooping" is constructed.
const snoopCLIRefusal = "% Invalid input detected at '^' marker"

// snoopShow renders a "show ip dhcp snooping" answer. CONSTRUCTED shape.
// ports is a list of "0/N Yes|No Yes|No" rows already spelled out.
func snoopShow(enabled bool, vlanLine string, ports ...string) string {
	var b strings.Builder
	if enabled {
		b.WriteString("DHCP snooping is Enabled\n")
	} else {
		b.WriteString("DHCP snooping is Disabled\n")
	}
	b.WriteString("DHCP snooping source MAC verification is enabled\n")
	if vlanLine != "" {
		b.WriteString("DHCP snooping is enabled on the following VLANs:\n")
		b.WriteString(" " + vlanLine + "\n")
		b.WriteString("\n")
	}
	if len(ports) > 0 {
		b.WriteString("Interface   Trusted    Log Invalid Pkts\n")
		b.WriteString("----------  ---------  ----------------\n")
		var row string
		for _, row = range ports {
			b.WriteString(row + "\n")
		}
	}
	return b.String()
}

// --- parseDHCPSnooping --------------------------------------------------

func TestParseDHCPSnoopingReferenceShape(t *testing.T) {
	var st, ok = parseDHCPSnooping(snoopRefShape)
	if !ok {
		t.Fatal("the global state line is present: the parse must be reported as understood")
	}
	if st.Enabled {
		t.Error("\"DHCP snooping is Disabled\" must read as off")
	}
	var wantVLANs = []int{1, 10, 11, 12}
	if !snoopSameVLANs(st.VLANs, wantVLANs) {
		t.Errorf("VLANs = %v, want %v", st.VLANs, wantVLANs)
	}
	if len(st.Ports) != 2 {
		t.Fatalf("two faceplate rows were printed (0/1, 0/2) — the 3/1 LAG row is not a "+
			"socket: got %d entries %+v", len(st.Ports), st.Ports)
	}
	if st.Ports[0].Port != "GE1" || !st.Ports[0].Trusted {
		t.Errorf("0/1 Yes must read as GE1 trusted, got %+v", st.Ports[0])
	}
	if st.Ports[1].Port != "GE2" || st.Ports[1].Trusted {
		t.Errorf("0/2 No must read as GE2 untrusted, got %+v", st.Ports[1])
	}
	// The per-VLAN dimension note is not decoration: an empty VLANs slice means
	// two different things without it (cf. driver.DHCPSnoopState.VLANs).
	if !notesMention(st.Notes, "per VLAN") {
		t.Errorf("Notes must say this platform has a per-VLAN dimension: %v", st.Notes)
	}
	// "Log Invalid Pkts" is Yes on 0/2 only: reported, never modelled.
	if !notesMention(st.Notes, "GE2") || notesMention(st.Notes, "GE1 GE2") {
		t.Errorf("the invalid-packet logging note must name GE2 and only GE2: %v", st.Notes)
	}
}

// The regression the `$` anchor on reSnoopGlobal exists for. "DHCP snooping is
// enabled on the following VLANs:" opens with the same six words as the global
// state line, so a device with the feature OFF and a leftover VLAN list would
// report as protected if the anchor were dropped.
func TestParseDHCPSnoopingVLANHeaderIsNotTheGlobalLine(t *testing.T) {
	var st, ok = parseDHCPSnooping(snoopRefShape)
	if !ok || st.Enabled {
		t.Fatalf("the VLAN header must not be read as the global state: Enabled=%v ok=%v", st.Enabled, ok)
	}

	// And on its own, the VLAN header is not an answer at all: no global line
	// means the read was not understood.
	var only = `DHCP snooping is enabled on the following VLANs:
 10, 20
`
	var _, headerOnly = parseDHCPSnooping(only)
	if headerOnly {
		t.Error("a VLAN header with no global state line must not be reported as understood — " +
			"a caller would display trust flags on a device it knows nothing about")
	}

	// Nor is the source-MAC line, which also ends in "is enabled".
	var _, macOnly = parseDHCPSnooping("DHCP snooping source MAC verification is enabled\n")
	if macOnly {
		t.Error("\"DHCP snooping source MAC verification is enabled\" is a different sentence")
	}
}

// A refused command still returns TEXT. Reporting the zero value on it would
// put "snooping: off" on screen for a device nobody has information about —
// and off is exactly the reading an operator would act on.
func TestParseDHCPSnoopingRefusalIsNotAnAnswer(t *testing.T) {
	var _, ok = parseDHCPSnooping(snoopCLIRefusal)
	if ok {
		t.Error("\"% Invalid input\" is not a snooping state")
	}
	if _, ok = parseDHCPSnooping(""); ok {
		t.Error("empty output is not a snooping state")
	}
}

// THE CONTRACT POINT THIS PACKAGE IS MOST LIKELY TO BREAK LATER (cf.
// driver.DHCPSnoopPortState.Trusted): a port whose flag was not READ is ABSENT
// from Ports. It is NOT listed with Trusted=false, because "untrusted" and "not
// observed" call for different things on screen — the first is the protection
// working, the second is a gap in what we know.
func TestParseDHCPSnoopingUnreadPortIsAbsentNotUntrusted(t *testing.T) {
	// An eighteen-socket device whose table printed two rows.
	var st, ok = parseDHCPSnooping(snoopShow(true, "10", "0/1         Yes        No", "0/2         No         No"))
	if !ok {
		t.Fatal("parse must be understood")
	}
	if len(st.Ports) != 2 {
		t.Fatalf("only two rows were printed: Ports must carry two entries, got %d: %+v",
			len(st.Ports), st.Ports)
	}
	var byName = map[string]bool{}
	var ps driver.DHCPSnoopPortState
	for _, ps = range st.Ports {
		byName[ps.Port] = true
	}
	var n string
	for _, n = range []string{"GE3", "GE9", "GE16", "GE17", "GE18"} {
		if byName[n] {
			t.Errorf("%s printed no row: it must not appear in Ports at all — an entry with "+
				"Trusted=false would claim a reading nobody took", n)
		}
	}

	// And with no interface table at all — a device with the feature off often
	// prints none — Ports must be empty, not eighteen untrusted ports.
	st, ok = parseDHCPSnooping(snoopOffNoVLANs)
	if !ok {
		t.Fatal("the global line alone is a valid answer")
	}
	if len(st.Ports) != 0 {
		t.Errorf("no interface table was printed: Ports must be empty, got %+v", st.Ports)
	}
	if len(st.VLANs) != 0 {
		t.Errorf("no VLAN header was printed: VLANs must be empty, got %v", st.VLANs)
	}
}

// Ports must come out in FACEPLATE order whatever order the firmware printed
// them in — GE2 before GE10, numerically. A caller rendering a faceplate from
// this list must not have to sort it, and drift comparing two reads must not see
// a difference that is only an ordering.
func TestParseDHCPSnoopingPortsAreSortedNumerically(t *testing.T) {
	var st, ok = parseDHCPSnooping(snoopShow(true, "10",
		"0/10        Yes        No",
		"0/2         No         No",
		"0/18        Yes        No",
		"0/1         No         No",
		"0/9         No         No",
	))
	if !ok {
		t.Fatal("parse must be understood")
	}
	var got []string
	var ps driver.DHCPSnoopPortState
	for _, ps = range st.Ports {
		got = append(got, ps.Port)
	}
	var want = "GE1 GE2 GE9 GE10 GE18"
	if strings.Join(got, " ") != want {
		t.Errorf("port order = %q, want %q (numeric, not lexical)", strings.Join(got, " "), want)
	}
}

// Only slot 0 is a faceplate socket on this platform: LAG members answer as
// slot 3 and the CPU interface as slot 5 (cf. StandardPortName).
func TestParseDHCPSnoopingDropsNonFaceplateRows(t *testing.T) {
	var st, _ = parseDHCPSnooping(snoopShow(true, "10",
		"3/1         Yes        No",
		"5/1         Yes        No",
		"0/4         Yes        No",
	))
	if len(st.Ports) != 1 || st.Ports[0].Port != "GE4" {
		t.Errorf("only 0/4 is a socket: got %+v", st.Ports)
	}
}

// --- parseSnoopVLANList -------------------------------------------------

func TestParseSnoopVLANList(t *testing.T) {
	var cases = []struct {
		name string
		out  string
		want []int
	}{
		{
			// The reference's own spelling, spaces around the dash.
			name: "range with spaces",
			out:  "DHCP snooping is enabled on the following VLANs:\n 1, 10 - 20\n\n",
			want: []int{1, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		},
		{
			// The running config's coalescing, no spaces (cf. expandVlanList).
			name: "range without spaces",
			out:  "DHCP snooping is enabled on the following VLANs:\n 10,20-22\n\n",
			want: []int{10, 20, 21, 22},
		},
		{
			name: "single id",
			out:  "DHCP snooping is enabled on the following VLANs:\n 99\n\n",
			want: []int{99},
		},
		{
			// SORTED and DEDUPLICATED: the same set printed in any order must
			// produce the same slice, or two identical gestures produce two
			// different requests.
			name: "unsorted and duplicated",
			out:  "DHCP snooping is enabled on the following VLANs:\n 30, 10, 10, 20-22, 21\n\n",
			want: []int{10, 20, 21, 22, 30},
		},
		{
			name: "continued over several lines",
			out:  "DHCP snooping is enabled on the following VLANs:\n 1, 2, 3\n 100, 200\n\n",
			want: []int{1, 2, 3, 100, 200},
		},
		{
			// A blank line BEFORE any id is not the end of the list on every
			// firmware; a blank line after ids is.
			name: "blank line before the ids",
			out:  "DHCP snooping is enabled on the following VLANs:\n\n 42\n\n",
			want: []int{42},
		},
		{
			name: "no header at all",
			out:  snoopOffNoVLANs,
			want: nil,
		},
		{
			// THE ONE THAT MATTERS: the table's dashed separator is made of
			// nothing but dashes and spaces, so it MATCHES reSnoopVLANList. On a
			// device whose VLAN list is empty, the run must stop at the
			// "Interface" header and never reach it.
			name: "empty list followed by the interface table",
			out: "DHCP snooping is enabled on the following VLANs:\n\n" +
				"Interface   Trusted    Log Invalid Pkts\n" +
				"----------  ---------  ----------------\n" +
				"0/1         Yes        No\n",
			want: nil,
		},
		{
			// A malformed piece is skipped rather than poisoning its
			// neighbours: better to under-read one VLAN than to invent
			// membership no line states (cf. expandVlanList).
			name: "malformed pieces are skipped, not fatal",
			out:  "DHCP snooping is enabled on the following VLANs:\n 10,,30\n\n",
			want: []int{10, 30},
		},
		{
			// A range written backwards names nothing.
			name: "backwards range",
			out:  "DHCP snooping is enabled on the following VLANs:\n 20-18, 40\n\n",
			want: []int{40},
		},
		{
			// 4094 is outside this platform's VLAN space (1-4093).
			name: "id outside the platform space",
			out:  "DHCP snooping is enabled on the following VLANs:\n 4094\n\n",
			want: nil,
		},
	}
	var c struct {
		name string
		out  string
		want []int
	}
	for _, c = range cases {
		var got = parseSnoopVLANList(c.out)
		if !snoopSameVLANs(got, c.want) {
			t.Errorf("%s: parseSnoopVLANList = %v, want %v", c.name, got, c.want)
		}
	}
}

// Two reads of the same device, printed in a different order by the firmware,
// must produce byte-identical VLAN slices — that is what makes drift comparison
// and request building deterministic.
func TestParseSnoopVLANListIsOrderIndependent(t *testing.T) {
	var a = parseSnoopVLANList("DHCP snooping is enabled on the following VLANs:\n 10, 20-22, 99\n\n")
	var b = parseSnoopVLANList("DHCP snooping is enabled on the following VLANs:\n 99, 22, 20, 21, 10\n\n")
	if !snoopSameVLANs(a, b) {
		t.Errorf("the same VLAN set printed in two orders must parse identically: %v vs %v", a, b)
	}
}

// --- the faceplate order (driver.SortPortNames + this platform's split) ---

// NUMERICALLY, not lexically: lexical order puts GE10 before GE2, which is how
// a faceplate ends up drawn with its sockets shuffled and how a command list
// stops being reproducible.
func TestSortStandardNamesIsNumericNotLexical(t *testing.T) {
	var names = []string{"GE10", "GE2", "GE1", "GE18", "GE9", "GE11"}
	driver.SortPortNames(names, splitStandard)
	var want = "GE1 GE2 GE9 GE10 GE11 GE18"
	if strings.Join(names, " ") != want {
		t.Errorf("SortPortNames = %q, want %q", strings.Join(names, " "), want)
	}

	// Lexical order would have produced this, and must not.
	if strings.Join(names, " ") == "GE1 GE10 GE11 GE18 GE2 GE9" {
		t.Error("names were sorted as strings")
	}
}

func TestSortStandardNamesHandlesUnparseableNames(t *testing.T) {
	// Must not panic, and must leave a total order behind it.
	var names = []string{"ether3", "GE2", "GE1"}
	driver.SortPortNames(names, splitStandard)
	if len(names) != 3 {
		t.Fatalf("names lost: %v", names)
	}
	var seen = map[string]bool{}
	var n string
	for _, n = range names {
		seen[n] = true
	}
	if !seen["GE1"] || !seen["GE2"] || !seen["ether3"] {
		t.Errorf("sorting must not drop a name: %v", names)
	}
}

// --- verifyDHCPSnoopTrust -----------------------------------------------

func TestVerifyDHCPSnoopTrustConfirmsWhatTheDeviceAgreesWith(t *testing.T) {
	var after = driver.DHCPSnoopState{
		Enabled: true,
		Ports: []driver.DHCPSnoopPortState{
			{Port: "GE1", Trusted: true},
			{Port: "GE2", Trusted: false},
		},
	}
	var want = map[string]bool{"GE1": true, "GE2": false}
	var confirmed, mismatched = verifyDHCPSnoopTrust(after, []string{"GE1", "GE2"}, want)
	if len(mismatched) != 0 {
		t.Errorf("the re-read agrees: nothing to report, got %v", mismatched)
	}
	if strings.Join(confirmed, " ") != "GE1 GE2" {
		t.Errorf("confirmed = %v, want [GE1 GE2]", confirmed)
	}
}

func TestVerifyDHCPSnoopTrustReportsADisagreeingReRead(t *testing.T) {
	var after = driver.DHCPSnoopState{
		Enabled: true,
		Ports:   []driver.DHCPSnoopPortState{{Port: "GE1", Trusted: false}},
	}
	var confirmed, mismatched = verifyDHCPSnoopTrust(after, []string{"GE1"}, map[string]bool{"GE1": true})
	if len(confirmed) != 0 {
		t.Errorf("the device says untrusted: the port must not count as confirmed, got %v", confirmed)
	}
	if len(mismatched) != 1 || !strings.Contains(mismatched[0], "GE1") {
		t.Fatalf("the mismatch must be phrased and name the port: %v", mismatched)
	}
}

// "Not observed" is not "trusted as asked". A port that vanished from the
// re-read is a mismatch, not a success: the whole point of
// driver.DHCPSnoopState.Ports carrying only READ ports is lost if a verifier
// treats absence as agreement.
func TestVerifyDHCPSnoopTrustTreatsAVanishedPortAsUnconfirmed(t *testing.T) {
	var after = driver.DHCPSnoopState{
		Enabled: true,
		Ports:   []driver.DHCPSnoopPortState{{Port: "GE2", Trusted: false}},
	}
	var confirmed, mismatched = verifyDHCPSnoopTrust(after, []string{"GE1"}, map[string]bool{"GE1": true})
	if len(confirmed) != 0 {
		t.Errorf("GE1 is absent from the re-read: it must not count as confirmed, got %v", confirmed)
	}
	if len(mismatched) != 1 || !strings.Contains(mismatched[0], "does not appear") {
		t.Fatalf("an absent port must be phrased as not observed: %v", mismatched)
	}

	// The untrusted direction is verified with the same rigour: a port asked to
	// go UNTRUSTED and reading back trusted is a protection that did not land.
	confirmed, mismatched = verifyDHCPSnoopTrust(
		driver.DHCPSnoopState{Ports: []driver.DHCPSnoopPortState{{Port: "GE2", Trusted: true}}},
		[]string{"GE2"}, map[string]bool{"GE2": false})
	if len(confirmed) != 0 || len(mismatched) != 1 {
		t.Errorf("untrusting must be verified too: confirmed=%v mismatched=%v", confirmed, mismatched)
	}
}

// --- the fake CLI (cf. the file header: CONSTRUCTED) --------------------

// snoopFakeCLI stands in for the SSH shell. It records every line the connector
// sends and answers each one with a canned reply terminated by a prompt, which
// is all cliConn.run needs to complete. No SSH client, no session: those fields
// stay nil and nothing here closes the connection.
type snoopFakeCLI struct {
	sent  []string
	out   chan byte
	reply func(cmd string) string
}

// snoopFakePrompt is the privileged prompt. rePrompt (ssh.go) matches the
// sub-mode prompts with the same pattern, so the fake does not bother modelling
// "(Config)#" or "(Interface 0/1)#" separately.
const snoopFakePrompt = "\n(UBNT EdgeSwitch) #"

func (f *snoopFakeCLI) Write(p []byte) (int, error) {
	var line = strings.TrimRight(string(p), " \t\r\n")
	if line == "" {
		return len(p), nil // the pager feed; these fixtures never trigger it
	}
	f.sent = append(f.sent, line)
	var b byte
	for _, b = range []byte(f.reply(line) + snoopFakePrompt) {
		f.out <- b
	}
	return len(p), nil
}

func (f *snoopFakeCLI) Close() error { return nil }

// newSnoopFakeClient returns a Client whose session is the fake, already
// privileged and recognized so requireWritable lets writes through.
func newSnoopFakeClient(reply func(cmd string) string) (*Client, *snoopFakeCLI) {
	var f = &snoopFakeCLI{out: make(chan byte, 1<<20), reply: reply}
	return &Client{
		cli:        &cliConn{stdin: f, out: f.out},
		host:       "192.0.2.4",
		User:       "mcadmin",
		known:      true,
		privileged: true,
	}, f
}

// snoopReplayReads answers "show ip dhcp snooping" with the next canned read
// and everything else with a bare prompt. A read past the end of the list is a
// test bug, and says so rather than hanging.
func snoopReplayReads(t *testing.T, reads ...string) func(string) string {
	var n int
	return func(cmd string) string {
		if cmd != dhcpSnoopShow {
			return ""
		}
		if n >= len(reads) {
			t.Errorf("the connector read \"%s\" %d time(s); only %d answers were staged",
				dhcpSnoopShow, n+1, len(reads))
			return snoopCLIRefusal
		}
		n++
		return reads[n-1]
	}
}

func assertSnoopSent(t *testing.T, f *snoopFakeCLI, want ...string) {
	t.Helper()
	if strings.Join(f.sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands sent:\n%s\n\nwant:\n%s",
			strings.Join(f.sent, "\n"), strings.Join(want, "\n"))
	}
}

func notesMention(notes []string, sub string) bool {
	var n string
	for _, n = range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// --- ApplyDHCPSnooping --------------------------------------------------

// THE EXACT COMMAND SEQUENCE, in order. Two identical gestures must produce two
// identical requests, so the VLAN list is sorted and deduplicated before it
// becomes commands — a plan carrying "20, 10, 10" must not send VLAN 10 twice
// nor send 20 before 10.
func TestApplyDHCPSnoopingSendsADeterministicCommandSequence(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopOffNoVLANs,
		snoopShow(true, "10, 20", "0/2         Yes        No"),
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE2", Trusted: true}},
		VLANs: []int{20, 10, 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertSnoopSent(t, f,
		dhcpSnoopShow, // the pre-read: a write that could not read first does not proceed
		"configure",
		"ip dhcp snooping",         // the global switch, because the device had it off
		"ip dhcp snooping vlan 10", // sorted, deduplicated
		"ip dhcp snooping vlan 20",
		"interface 0/2",
		"ip dhcp snooping trust",
		"exit",                 // out of Interface Config
		"exit",                 // out of Global Config
		dhcpSnoopShow,          // the verifying re-read
		"write memory confirm", // and LAST, the save — never before the re-read
	)
	if strings.Join(res.PortsWritten, " ") != "GE2" {
		t.Errorf("PortsWritten = %v, want [GE2]", res.PortsWritten)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("nothing was refused: Skipped must be empty, got %v", res.Skipped)
	}
}

// The global and per-VLAN enable change the device for EVERY port, not only the
// ones that were clicked — so they are reported, in words, in Notes. An
// operator who did not ask for that must be able to read it afterwards.
func TestApplyDHCPSnoopingReportsTheGlobalAndVLANEnableInNotes(t *testing.T) {
	var c, _ = newSnoopFakeClient(snoopReplayReads(t,
		snoopOffNoVLANs,
		snoopShow(true, "10", "0/1         Yes        No"),
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
		VLANs: []int{10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !notesMention(res.Notes, "turned ON globally") {
		t.Errorf("the global enable must be reported in Notes: %v", res.Notes)
	}
	if !notesMention(res.Notes, "EVERY port") {
		t.Errorf("Notes must say the global enable affects every port: %v", res.Notes)
	}
	if !notesMention(res.Notes, "VLAN(s) 10") {
		t.Errorf("the per-VLAN enable must be reported in Notes: %v", res.Notes)
	}
	// What makes the change survive a reboot is no longer advice to the operator:
	// this apply sends it itself, and the transcript is where that is recorded.
	if !snoopStepDone(c, "configuration saved") {
		t.Errorf("the apply must persist: transcript %+v", c.Steps)
	}
}

// snoopStepDone reports whether the transcript carries a SUCCESSFUL step whose
// wording contains sub.
func snoopStepDone(c *Client, sub string) bool {
	var s Step
	for _, s = range c.Steps {
		if s.OK && strings.Contains(s.What, sub) {
			return true
		}
	}
	return false
}

// snoopStepFailed is its mirror: a step the transcript records as NOT ok.
func snoopStepFailed(c *Client, sub string) bool {
	var s Step
	for _, s = range c.Steps {
		if !s.OK && strings.Contains(s.What, sub) {
			return true
		}
	}
	return false
}

// A SAVE THAT FAILS DOES NOT FAIL THE APPLY. The trust flags really are in the
// running configuration — the re-read above confirmed them — and returning an
// error here would tell an operator the opposite of what the device holds. What
// they lose is the reboot, and the Note has to say exactly that rather than
// merely reporting that a command was refused.
func TestApplyDHCPSnoopingFailedSaveIsANoteAndNotAFailure(t *testing.T) {
	var reads = snoopReplayReads(t,
		snoopShow(true, "10", "0/2         No         No"),
		snoopShow(true, "10", "0/2         Yes        No"),
	)
	var c, f = newSnoopFakeClient(func(cmd string) string {
		if cmd == "write memory confirm" {
			// A device whose flash is busy, or a session that lost its privilege
			// between the write and the save.
			return snoopCLIRefusal
		}
		return reads(cmd)
	})
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE2", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("a refused save must not fail the apply: the write landed and this claims "+
			"it did not — %v", err)
	}
	// The write is still reported as done, because it IS done.
	if strings.Join(res.PortsWritten, " ") != "GE2" {
		t.Errorf("PortsWritten = %v, want [GE2]: the trust flag is in the running "+
			"configuration whatever the save did", res.PortsWritten)
	}
	if !notesMention(res.Notes, "NOT persisted") {
		t.Fatalf("the failed save must be reported in Notes: %v", res.Notes)
	}
	if !notesMention(res.Notes, "a reboot would take this snooping configuration with it") {
		t.Errorf("the Note must say what the operator LOSES — the reboot — and not merely "+
			"that a command failed: %v", res.Notes)
	}
	if !snoopStepFailed(c, "write memory refused") {
		t.Errorf("the failed save must be a failed transcript step: %+v", c.Steps)
	}
	// And it was attempted LAST: the save may never precede the verifying re-read.
	if f.sent[len(f.sent)-1] != "write memory confirm" ||
		f.sent[len(f.sent)-2] != dhcpSnoopShow {
		t.Errorf("the save must be the last thing sent, right after the re-read: %v", f.sent)
	}
}

// A device that already has the feature on must not be told again — and above
// all, a VLAN it already covers must be left exactly as it is: protection
// somebody else installed is not ours to restate or to remove.
func TestApplyDHCPSnoopingUnionsVLANsAndNeverDisablesAnything(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(true, "10, 20", "0/1         Yes        No"),
		snoopShow(true, "10, 20, 30", "0/1         No         No"),
	))
	// The plan names 30 only; the device carries 10 and 20. And GE1 goes from
	// trusted to untrusted, which IS a legitimate per-port write.
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: false}},
		VLANs: []int{30},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertSnoopSent(t, f,
		dhcpSnoopShow,
		"configure",
		// no "ip dhcp snooping": already on
		"ip dhcp snooping vlan 30", // 10 and 20 are not restated
		"interface 0/1",
		"no ip dhcp snooping trust",
		"exit",
		"exit",
		dhcpSnoopShow,
		"write memory confirm",
	)
	// THE FORMS THIS CONNECTOR MUST NEVER SEND, whatever the plan says. The
	// per-port "no ip dhcp snooping trust" above is the one exception the
	// contract allows, and it is checked for by exact match so a global or
	// per-VLAN disable cannot hide behind it.
	var sent string
	for _, sent = range f.sent {
		if sent == "no ip dhcp snooping" {
			t.Error("the global disable strips protection from the whole device: never sent")
		}
		if strings.HasPrefix(sent, "no ip dhcp snooping vlan") {
			t.Errorf("a per-VLAN disable removes protection nobody asked to remove: %q", sent)
		}
	}
	if strings.Join(res.PortsWritten, " ") != "GE1" {
		t.Errorf("PortsWritten = %v, want [GE1]", res.PortsWritten)
	}
}

// A port already carrying the intent is not written: sending nothing is better
// than a round trip that changes nothing (cf. driver.ApplyResult.PortsWritten).
func TestApplyDHCPSnoopingSkipsPortsThatAlreadyCarryTheIntent(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(true, "10", "0/1         Yes        No", "0/2         No         No"),
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE1", Trusted: true},  // already trusted
			{Port: "GE2", Trusted: false}, // already untrusted
		},
		VLANs: []int{10}, // already covered
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertSnoopSent(t, f, dhcpSnoopShow, "configure", "exit")
	if len(res.PortsWritten) != 0 {
		t.Errorf("nothing needed writing: PortsWritten = %v", res.PortsWritten)
	}
}

// The same port named twice in one plan must produce ONE interface block. A
// plan is a set as far as the device is concerned, and a second identical write
// is a second chance to fail for no gain.
func TestApplyDHCPSnoopingWritesADuplicatePortIntentOnce(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(true, "10", "0/3         No         No"),
		snoopShow(true, "10", "0/3         Yes        No"),
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE3", Trusted: true},
			{Port: "GE3", Trusted: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertSnoopSent(t, f,
		dhcpSnoopShow,
		"configure",
		"interface 0/3",
		"ip dhcp snooping trust",
		"exit",
		"exit",
		dhcpSnoopShow,
		"write memory confirm",
	)
	if strings.Join(res.PortsWritten, " ") != "GE3" {
		t.Errorf("PortsWritten = %v, want [GE3] once", res.PortsWritten)
	}
}

// A port the platform refuses is an ORDINARY REFUSAL: [ApplyResult.Skipped],
// never a Go error and never silently dropped. The other ports in the plan are
// still written.
func TestApplyDHCPSnoopingRefusedPortLandsInSkipped(t *testing.T) {
	var reads = snoopReplayReads(t,
		snoopShow(true, "10", "0/1         No         No", "0/2         No         No"),
		snoopShow(true, "10", "0/1         No         No", "0/2         Yes        No"),
	)
	var c, f = newSnoopFakeClient(func(cmd string) string {
		if cmd == "interface 0/1" {
			// A stacking or uplink port some firmware excludes.
			return snoopCLIRefusal
		}
		return reads(cmd)
	})
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE1", Trusted: true},
			{Port: "GE2", Trusted: true},
		},
	})
	if err != nil {
		t.Fatalf("a platform refusal must not be a Go error: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "GE1") {
		t.Fatalf("the refusal must be reported in Skipped, naming the port: %v", res.Skipped)
	}
	if strings.Join(res.PortsWritten, " ") != "GE2" {
		t.Errorf("GE2 was accepted and must still be written: PortsWritten = %v", res.PortsWritten)
	}
	// ONE trust command, GE2's. The refused interface block must not be followed
	// by a trust command that would land in whatever mode the CLI was left in.
	assertSnoopSent(t, f,
		dhcpSnoopShow,
		"configure",
		"interface 0/1", // refused
		"interface 0/2",
		"ip dhcp snooping trust",
		"exit",
		"exit",
		dhcpSnoopShow,
		"write memory confirm",
	)
}

// A name this connector does not recognize on this platform is a refusal, not a
// command built on a guess.
func TestApplyDHCPSnoopingUnknownPortNameLandsInSkipped(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t, snoopShow(true, "10", "0/1         No         No")))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ether3", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("an unrecognized name is not a Go error: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "ether3") {
		t.Fatalf("Skipped must name the port it did not recognize: %v", res.Skipped)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("nothing was written: %v", res.PortsWritten)
	}
	var sent string
	for _, sent = range f.sent {
		if strings.HasPrefix(sent, "interface ") {
			t.Errorf("no interface block may be opened for an unrecognized name: %q", sent)
		}
	}
}

// THE TAIL CASE, and the reason splitStandard was tightened (cf. its comment in
// state.go). "GE1/0/2" is a stacked faceplate's spelling, from a range that is
// not this one. It used to pass the gate as port 1 — the digits the old
// fmt.Sscanf stopped at — and cliPortName then addressed "0/1", a real jack on
// this box, which would have carried a trust flag nobody asked for. It must be a
// refusal, and no interface block may be opened for it.
func TestApplyDHCPSnoopingNameWithATailNeverAddressesAJack(t *testing.T) {
	var names = []string{"GE1/0/2", "GE1x", "GE1.5"}
	var name string
	for _, name = range names {
		var c, f = newSnoopFakeClient(snoopReplayReads(t, snoopShow(true, "10", "0/1         No         No")))
		var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
			Ports: []driver.DHCPSnoopPortIntent{{Port: name, Trusted: true}},
		})
		if err != nil {
			t.Fatalf("%s: a name with a tail is not a Go error: %v", name, err)
		}
		if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], name) {
			t.Fatalf("%s: Skipped must name the port it refused: %v", name, res.Skipped)
		}
		if len(res.PortsWritten) != 0 {
			t.Errorf("%s: nothing was written: %v", name, res.PortsWritten)
		}
		var sent string
		for _, sent = range f.sent {
			if strings.HasPrefix(sent, "interface ") {
				t.Errorf("%s: the digits in the tail must not resolve to a socket, "+
					"and this opened %q", name, sent)
			}
		}
	}
}

// This platform's VLAN space is 1-4093, not the 1-4094 most of the fleet uses
// (cf. Capabilities.VlanRange). An id outside it is refused in Skipped, and no
// command is built from it.
func TestApplyDHCPSnoopingVLANOutsideThePlatformSpaceLandsInSkipped(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t, snoopShow(true, "", "0/1         No         No")))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{4094, 0},
	})
	if err != nil {
		t.Fatalf("an out-of-range VLAN is not a Go error: %v", err)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("both ids are outside 1-4093 and both must be reported: %v", res.Skipped)
	}
	var sent string
	for _, sent = range f.sent {
		if strings.HasPrefix(sent, "ip dhcp snooping vlan") {
			t.Errorf("no VLAN command may be built from an out-of-range id: %q", sent)
		}
	}
}

// THE POINT OF THE RE-READ. A trust flag is a protection: a command that did not
// complain is not evidence it landed. A port whose re-read disagrees must NOT be
// reported in PortsWritten — "the ports actually reconfigured" (cf.
// driver.ApplyResult.PortsWritten) — and the disagreement must be phrased in
// Skipped, which is the field an operator is entitled to read.
func TestApplyDHCPSnoopingDoesNotReportAnUnverifiedWriteAsWritten(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(true, "10", "0/1         No         No"),
		// The device took the command without complaint and still reads
		// untrusted afterwards.
		snoopShow(true, "10", "0/1         No         No"),
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("the re-read says GE1 is untrusted: it must not be reported as written, got %v",
			res.PortsWritten)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "GE1") {
		t.Fatalf("the mismatch must be reported in Skipped: %v", res.Skipped)
	}
	// And the verifying re-read must actually have been sent, BEFORE the save:
	// nothing goes into flash that the device has not been asked to confirm.
	if f.sent[len(f.sent)-2] != dhcpSnoopShow || f.sent[len(f.sent)-1] != "write memory confirm" {
		t.Errorf("the gesture must end with the re-read then the save, got %v",
			f.sent[len(f.sent)-2:])
	}
}

// A port that vanished from the re-read is not confirmed either: "not observed"
// is not "trusted as asked" (cf. driver.DHCPSnoopPortState.Trusted).
func TestApplyDHCPSnoopingDoesNotReportAVanishedPortAsWritten(t *testing.T) {
	var c, _ = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(true, "10", "0/1         No         No"),
		snoopShow(true, "10"), // no interface table at all in the re-read
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("GE1 does not appear in the re-read: it must not be reported as written, got %v",
			res.PortsWritten)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("the unverified write must be reported: %v", res.Skipped)
	}
}

// A re-read that FAILS is not a verification either: the write is reported as
// unverified, and no port is claimed.
func TestApplyDHCPSnoopingUnreadableReReadClaimsNothing(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(true, "10", "0/1         No         No"),
		snoopCLIRefusal, // the re-read is not understood
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("a failed verification is not a Go error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("nothing was verified: PortsWritten must be empty, got %v", res.PortsWritten)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "unverified") {
		t.Fatalf("Skipped must say the write could not be read back: %v", res.Skipped)
	}
	// AND IT IS NOT SAVED. A configuration nobody confirmed must not be baked
	// into flash, where a power cycle can no longer undo it.
	var sent string
	for _, sent = range f.sent {
		if sent == "write memory confirm" {
			t.Fatalf("an unverified write must not be persisted: %v", f.sent)
		}
	}
	if !notesMention(res.Notes, "NOT saved") {
		t.Errorf("Notes must say the write was left out of flash, and why: %v", res.Notes)
	}
}

// A gesture that only turns the feature ON — no port in the plan — must be
// verified too. "A device left with the feature off after a successful-looking
// apply would be the one failure this contract exists to prevent"
// (driver/dhcpsnoop.go). The return code of "ip dhcp snooping" is not evidence.
func TestApplyDHCPSnoopingVerifiesTheGlobalAndVLANEnable(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopOffNoVLANs,
		// The device took both commands without complaint and still reports the
		// feature off, with the VLAN nowhere in sight.
		snoopOffNoVLANs,
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		VLANs: []int{42},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.sent[len(f.sent)-2] != dhcpSnoopShow {
		t.Fatalf("a global/VLAN-only gesture must still be verified by a re-read; commands sent: %v", f.sent)
	}
	if !skippedMentions(res.Skipped, "still reports the feature Disabled") {
		t.Errorf("a global enable that did not land must be reported in Skipped: %v", res.Skipped)
	}
	if !skippedMentions(res.Skipped, "VLAN 42") {
		t.Errorf("a per-VLAN enable that did not land must be reported in Skipped: %v", res.Skipped)
	}
}

// The site declaring no VLAN is worth saying — but only what is TRUE. On a
// device that already covers VLANs, "the feature inspects NOTHING" would be a
// false statement in the one field an operator reads to decide whether the
// protection is real.
func TestApplyDHCPSnoopingNoteOnASiteWithNoVLANTellsTheTruth(t *testing.T) {
	// First: a device that really covers nothing.
	var c, _ = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(false, "", "0/1         No         No"),
		snoopShow(true, "", "0/1         Yes        No"),
	))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !notesMention(res.Notes, "inspects NOTHING") {
		t.Errorf("with no VLAN anywhere, Notes must say the feature covers nothing: %v", res.Notes)
	}

	// Then: the same empty plan on a device that already covers VLAN 10.
	c, _ = newSnoopFakeClient(snoopReplayReads(t,
		snoopShow(true, "10", "0/1         No         No"),
		snoopShow(true, "10", "0/1         Yes        No"),
	))
	res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notesMention(res.Notes, "inspects NOTHING") {
		t.Errorf("this device covers VLAN 10: saying the feature inspects nothing is false — %v", res.Notes)
	}
	if !notesMention(res.Notes, "10") {
		t.Errorf("Notes must name the coverage the device already had: %v", res.Notes)
	}
}

// A write that could not read first does not proceed — same rule as ApplyPoE and
// ApplyPorts. Nothing is sent past the pre-read.
func TestApplyDHCPSnoopingPreReadFailureAbortsBeforeAnyWrite(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t, snoopCLIRefusal))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
		VLANs: []int{10},
	})
	if err == nil {
		t.Fatal("a pre-read that was not understood must abort the write")
	}
	assertSnoopSent(t, f, dhcpSnoopShow)
	if len(res.PortsWritten) != 0 || len(res.Notes) != 0 {
		t.Errorf("nothing happened: %+v", res)
	}
}

// requireWritable is the refusal common to every write on this connector: a
// session that never reached "#" cannot configure anything, and must not try.
func TestApplyDHCPSnoopingRefusesAnUnprivilegedSession(t *testing.T) {
	var c = &Client{host: "192.0.2.4", known: true} // recognized, never privileged
	var _, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
	})
	if err == nil {
		t.Fatal("an unprivileged session must be refused before any command is built")
	}
	var unrecognized = &Client{host: "192.0.2.4", privileged: true} // CLI never recognized
	if _, err = unrecognized.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
	}); err == nil {
		t.Fatal("an unrecognized CLI must be refused")
	}
}

// An empty plan sends nothing at all — not even the pre-read. There is no
// gesture to make and no session budget to spend making it (this range answers
// four concurrent sessions and no more).
func TestApplyDHCPSnoopingEmptyPlanSendsNothing(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t))
	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{})
	if err != nil {
		t.Fatalf("an empty plan is not an error: %v", err)
	}
	if len(f.sent) != 0 {
		t.Errorf("an empty plan must send nothing, sent: %v", f.sent)
	}
	if len(res.PortsWritten) != 0 || len(res.Skipped) != 0 || len(res.Notes) != 0 {
		t.Errorf("an empty plan reports nothing: %+v", res)
	}
}

// The whole gesture runs on the ONE session it was handed: this range refuses a
// fifth concurrent SSH session and authenticates-then-hangs at the fourth, so a
// connector that opened a session per read would break the device it is
// configuring. Counted here rather than trusted: the fake is the only session.
func TestApplyDHCPSnoopingOpensNoExtraSession(t *testing.T) {
	var c, f = newSnoopFakeClient(snoopReplayReads(t,
		snoopOffNoVLANs,
		snoopShow(true, "10", "0/1         Yes        No"),
	))
	var _, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE1", Trusted: true}},
		VLANs: []int{10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Exactly TWO reads: the pre-read and the verifying re-read. Neither the
	// port loop nor the VLAN loop may read again per item.
	var reads int
	var sent string
	for _, sent = range f.sent {
		if sent == dhcpSnoopShow {
			reads++
		}
	}
	if reads != 2 {
		t.Errorf("the gesture must cost exactly two \"%s\" — one before, one after: got %d",
			dhcpSnoopShow, reads)
	}
}

// --- ReadDHCPSnooping ---------------------------------------------------

// FAILS rather than reporting a device as unprotected. "% Invalid input" is
// text, and a reader that returned the zero value on it would put
// "snooping: off" on screen for a device nobody has information about.
func TestReadDHCPSnoopingRefusesToGuessOnAnUnparseableAnswer(t *testing.T) {
	var c, _ = newSnoopFakeClient(snoopReplayReads(t, snoopCLIRefusal))
	var st, err = c.ReadDHCPSnooping(context.Background())
	if err == nil {
		t.Fatal("an answer with no global state line must be an error, not \"off\"")
	}
	if st.Enabled || len(st.Ports) != 0 || len(st.VLANs) != 0 {
		t.Errorf("nothing may be reported from an answer that was not understood: %+v", st)
	}
	if !strings.Contains(err.Error(), dhcpSnoopShow) {
		t.Errorf("the error must name the command that failed: %v", err)
	}
}

func TestReadDHCPSnoopingReportsTheThreeLevels(t *testing.T) {
	var c, _ = newSnoopFakeClient(snoopReplayReads(t, snoopRefShape))
	var st, err = c.ReadDHCPSnooping(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st.Enabled {
		t.Error("the fixture says Disabled")
	}
	if !snoopSameVLANs(st.VLANs, []int{1, 10, 11, 12}) {
		t.Errorf("VLANs = %v", st.VLANs)
	}
	if len(st.Ports) != 2 {
		t.Errorf("Ports = %+v", st.Ports)
	}
	if len(st.Notes) == 0 {
		t.Error("the per-VLAN dimension must be stated in Notes")
	}
}

// --- the staging discipline --------------------------------------------

// WritesExtra must NOT claim "dhcp_snooping" while the command forms have
// only been read in a manual: the code ships, the capability does not, and a
// screen must not offer the control until a live pass has proven the forms
// (cf. driver.DHCPSnoopApplier's own comment, and how PoE landed here).
//
// This test is the one that must be DELETED — not edited around — on the day
// the live pass is done.
// TestWritesExtraClaimsDHCPSnoopingOnlyOnLiveProof replaces the test that pinned
// this capability as UNDECLARED. It was renamed rather than deleted so the git
// history shows the day the claim changed and on what evidence.
//
// "dhcp_snooping" was declared 2026-08-08, after all four gestures were driven
// through ApplyDHCPSnooping on the lab EP-S16 — enable, per-VLAN, trust, untrust
// — each confirmed by the contract's own re-read, persisted with "write memory
// confirm", and reverted. That is the bar driver/dhcpsnoop.go sets: implemented
// AND proven on hardware.
//
// If this ever fails because someone flipped it back, the question to answer
// first is whether the live exercise still passes (live_write_test.go,
// ES_LIVE_WRITE=1) — not whether the assertion is inconvenient.
func TestWritesExtraClaimsDHCPSnoopingOnlyOnLiveProof(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("dhcp_snooping") {
		t.Error("\"dhcp_snooping\" was proven live on 2026-08-08 — four gestures " +
			"through the applier, verified by re-read and persisted — and must stay " +
			"advertised unless that evidence is withdrawn")
	}
	if !d.WritesExtra("poe") {
		t.Error("\"poe\" was exercised live on 2026-08-05 and must stay advertised")
	}
	// The declaration is per capability, never a blanket yes: a name nothing has
	// implemented must still come back false, or the UI would offer every gesture
	// this connector has never heard of.
	// "storm_control" was declared here later the same day, on its own live
	// exercise, so this guard moved to a name that is still genuinely unproven
	// on this range. The point is unchanged: the declaration is per capability
	// and never a blanket yes.
	if d.WritesExtra("hostname") {
		t.Error("\"hostname\" is REFUTED BY THE DEVICE on this range (cf. TODO.md) " +
			"and must not be advertised")
	}
}

// The CLI spells its sockets "0/N" and the facade "GEN"; both directions
// must agree, or a trust flag lands on the wrong jack. Checked against the REAL
// capture (testdata/show_port_all.txt), which is where the platform's own
// spelling is on record — the off-by-one this guards is "GE1" becoming
// "0/0" or "0/2".
func TestCliPortNameMatchesTheCapturedSpelling(t *testing.T) {
	var d = &Driver{}
	var rows = parseShowPortAll(mustRead(t, "testdata/show_port_all.txt"))
	var seen int
	var p PortLine
	for _, p = range rows {
		if p.Name == "" {
			continue
		}
		seen++
		// Round trip: whatever the capture called this socket must survive
		// facade -> CLI -> facade unchanged.
		var back, ok = d.StandardPortName(cliPortName(p.Name))
		if !ok || back != p.Name {
			t.Errorf("%s -> %q -> %q (ok=%v): the round trip must be exact",
				p.Name, cliPortName(p.Name), back, ok)
		}
	}
	if seen != 18 {
		t.Fatalf("the capture carries 18 sockets, walked %d", seen)
	}
	if cliPortName("GE1") != "0/1" {
		t.Errorf("GE1 must be 0/1, got %q — the numbering starts at 1 on both sides",
			cliPortName("GE1"))
	}
	if cliPortName("GE18") != "0/18" {
		t.Errorf("GE18 must be 0/18, got %q", cliPortName("GE18"))
	}
}

func skippedMentions(skipped []string, sub string) bool {
	var s string
	for _, s = range skipped {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// snoopSameVLANs compares IN ORDER, unlike the package's sameInts which sorts
// both sides before looking. The order is exactly what the VLAN tests are
// about: driver.DHCPSnoopState.VLANs is documented SORTED, and a helper that
// sorted before comparing would pass on an unsorted result.
func snoopSameVLANs(got []int, want []int) bool {
	if len(got) != len(want) {
		return false
	}
	var i int
	for i = range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestParseDHCPSnoopingOnCapturedHardware holds the parser to what the lab
// EdgePoint S16 actually printed, rather than to the CLI reference it was
// written from.
//
// Captured 2026-08-08 (EP-S16, firmware 1.9.3, 192.0.2.4) through meshcraft's
// own SOCKS route, by the connector's own read path — so this fixture is the
// bytes ReadDHCPSnooping really receives, not a transcription of them.
//
// The feature was OFF, and that is what makes the assertions below the useful
// ones: the VLAN header is present with NOTHING under it, and the port table
// prints its header and its rule with no rows. Both are the shapes most likely
// to be misread as data — an empty list as "no per-VLAN dimension", a header row
// as a port — and neither may produce an entry.
func TestParseDHCPSnoopingOnCapturedHardware(t *testing.T) {
	var raw, err = os.ReadFile(filepath.Join("testdata", "show_ip_dhcp_snooping.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var st, found = parseDHCPSnooping(string(raw))

	// The bool is "the GLOBAL STATE LINE was found", and on a real capture it is
	// the assertion that matters most: without it every other field is
	// meaningless, and a refused command would also have returned text.
	if !found {
		t.Fatal("the global state line was not recognised in the captured output — " +
			"\"DHCP snooping is Disabled\" is what this firmware prints, and if that " +
			"is missed the whole read is untrustworthy rather than merely empty")
	}

	// "DHCP snooping is Disabled" — capital D, which is this platform's
	// spelling and not the reference's.
	if st.Enabled {
		t.Error("the captured OFF state reads as enabled")
	}
	if len(st.VLANs) != 0 {
		t.Errorf("VLANs = %v, want none: the header is present with nothing under it",
			st.VLANs)
	}
	// The table printed "Interface / Trusted / Log Invalid Pkts" and a rule of
	// dashes, and no rows. A port here would be invented.
	if len(st.Ports) != 0 {
		t.Errorf("%d port(s) reported from an empty table: %+v", len(st.Ports), st.Ports)
	}
	// The note is the whole reason an empty VLAN list is safe to return: it says
	// the dimension exists and is covering nothing, which is not the same claim
	// as "this platform has no per-VLAN dimension".
	if len(st.Notes) == 0 {
		t.Error("no note explaining that an empty VLAN list means the feature " +
			"inspects nothing — a caller cannot tell that from the absence alone")
	}
	// The second global line must not be mistaken for the first.
	if !strings.Contains(string(raw), "source MAC verification") {
		t.Fatal("fixture no longer carries the second global line this guards against")
	}
}

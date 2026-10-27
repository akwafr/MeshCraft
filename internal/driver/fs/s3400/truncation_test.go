package s3400

// THE 2026-08-11 INCIDENT, REPRODUCED — and the contract that now stops it.
//
// poefs3400lab persisted an observation of 23 of its 52 ports. "show
// running-config" died some eight seconds in, inside GE23's stanza;
// ParseRunningConfig's trailing flushPort() emitted the half-read GE23 with the
// default PVID and stopped; ReadState returned a nil error because it only
// treated a read as failed when the output was EMPTY; and the config poll stored
// the result with `observed_at = now()`, producing 30 phantom drift differences
// — 29 sockets "unknown to the device" plus GE23's PVID — on a switch nobody
// had misconfigured.
//
// Nothing in the truncated text says it is truncated: configuration is
// valid-looking to its last byte. The ONLY place the difference exists is in how
// runPaged left its loop, which is what errTruncated now carries out.
//
// These tests hold three things together, and the third is the one that keeps
// the fix from becoming a worse bug than the defect: a truncated read is
// REFUSED, a complete read is UNCHANGED, and an in-band CLI refusal — which
// arrives on a COMPLETE read — is still not a truncation.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// scriptedCLI is a device that answers a canned reply per command and can be
// told to stop mid-answer, which is the only way to build a truncated read: the
// text alone can never carry the fact.
type scriptedCLI struct {
	out  chan byte
	sent []string
	line strings.Builder
	dead bool

	// answer returns the body for a command and whether the prompt follows it.
	// endsAtPrompt=false closes the stream instead — the device going away
	// mid-sentence, which is what waitPromptOrPager reports as "SSH session
	// closed by the device".
	answer func(cmd string) (body string, endsAtPrompt bool)
}

func newScriptedCLI(answer func(string) (string, bool)) (*Client, *scriptedCLI) {
	var d = &scriptedCLI{out: make(chan byte, 1<<20), answer: answer}
	var c = &Client{cli: &cliConn{out: d.out, stdin: d}, host: "192.0.2.3",
		known: true, privileged: true}
	return c, d
}

func (d *scriptedCLI) Write(p []byte) (int, error) {
	var i int
	for i = 0; i < len(p); i++ {
		if p[i] == '\r' || p[i] == '\n' {
			var l = d.line.String()
			d.line.Reset()
			d.handle(strings.TrimSpace(l))
			continue
		}
		d.line.WriteByte(p[i])
	}
	return len(p), nil
}

func (d *scriptedCLI) Close() error { return nil }

func (d *scriptedCLI) handle(cmd string) {
	if d.dead {
		return
	}
	d.sent = append(d.sent, cmd)
	var body, endsAtPrompt = d.answer(cmd)
	d.emit(body)
	if !endsAtPrompt {
		// The session goes away without a prompt. Everything already emitted is a
		// PREFIX of the answer, and nothing in it says so.
		d.dead = true
		close(d.out)
		return
	}
	d.emit("\nSwitch#")
}

func (d *scriptedCLI) emit(s string) {
	var i int
	for i = 0; i < len(s); i++ {
		d.out <- s[i]
	}
}

// s3400RunningConfig builds the configuration of a 48T4SP: 48 copper blocks,
// four SFP+ cages, an SVI. Every copper port carries an explicit PVID so a
// port reported at the default 1 can only have come from flushPort() guessing.
func s3400RunningConfig() string {
	var b strings.Builder
	b.WriteString("!\nhostname poefs3400lab\n!\nlldp run\n!\n")
	var i int
	for i = 1; i <= 48; i++ {
		fmt.Fprintf(&b, "interface GigaEthernet0/%d\n switchport pvid %d\n!\n", i, 100+i)
	}
	for i = 1; i <= 4; i++ {
		fmt.Fprintf(&b, "interface TGigaEthernet0/%d\n switchport mode trunk\n!\n", i)
	}
	b.WriteString("interface VLAN99\n ip address 192.0.2.3 255.255.0.0\n!\n")
	return b.String()
}

// s3400TruncatedConfig cuts the configuration INSIDE GE23's stanza — the exact
// shape of the 2026-08-11 read: GE1..GE22 complete, GE23 opened and never
// closed, nothing after it.
func s3400TruncatedConfig() string {
	var full = s3400RunningConfig()
	var cut = strings.Index(full, " switchport pvid 123\n")
	if cut < 0 {
		panic("fixture: GE23's stanza is not where this test thinks it is")
	}
	return full[:cut]
}

// The fixture is the incident, and this pins it BEFORE any connector logic runs:
// the parser cannot tell, and never could. If this test ever stops reporting 23
// and a defaulted GE23, the reproduction has drifted and the tests below are
// asserting something else.
func TestTruncatedRunningConfigParsesAsAConfidentWrongSwitch(t *testing.T) {
	var cfg = ParseRunningConfig(s3400TruncatedConfig())
	if len(cfg.Ports) != 23 {
		t.Fatalf("the truncated capture parses to %d ports, want 23 — the fixture no longer "+
			"reproduces the 2026-08-11 read", len(cfg.Ports))
	}
	if cfg.Ports["GE23"].PVID != 1 {
		t.Errorf("GE23 PVID = %d, want 1: the half-read stanza must come out of flushPort() "+
			"carrying the platform default, which is what produced the pvid difference",
			cfg.Ports["GE23"].PVID)
	}
	if cfg.Ports["GE22"].PVID != 122 {
		t.Errorf("GE22 PVID = %d, want 122: everything before the cut is genuine",
			cfg.Ports["GE22"].PVID)
	}
	// And the whole config is 52 ports, so 23 is the shortfall the guardrails care
	// about — not a small switch.
	if len(ParseRunningConfig(s3400RunningConfig()).Ports) != 52 {
		t.Fatal("the complete capture must parse to 52 ports")
	}
}

// THE FIX. A running-config that did not reach a prompt is refused, and the
// refusal is what the caller gets instead of a switch with 29 missing sockets.
func TestReadStateRefusesATruncatedRunningConfig(t *testing.T) {
	var c, dev = newScriptedCLI(func(cmd string) (string, bool) {
		if cmd == "show running-config" {
			return s3400TruncatedConfig(), false
		}
		return "", true
	})
	var st, err = c.ReadState(context.Background())
	if err == nil {
		t.Fatalf("ReadState accepted a truncated running-config and reported %d ports — this "+
			"is the 2026-08-11 defect", len(st.Ports))
	}
	if len(st.Ports) != 0 {
		t.Errorf("the refusal still carried %d ports; a refused read must report no inventory "+
			"at all, or a caller may still persist it", len(st.Ports))
	}
	if !strings.Contains(err.Error(), "show running-config") {
		t.Errorf("the error must name the command that was cut: %v", err)
	}
	// The transcript is the evidence the config poll now logs (configpoll.go).
	var steps = c.Transcript()
	var found bool
	var i int
	for i = range steps {
		if !steps[i].OK && strings.Contains(steps[i].What, "running-config") {
			found = true
		}
	}
	if !found {
		t.Errorf("no failed step names the running-config: %+v", steps)
	}
	if len(dev.sent) == 0 {
		t.Error("nothing was sent to the device")
	}
}

// A COMPLETE READ IS UNCHANGED. This is the half that matters most: the sentinel
// must not make a healthy device unreadable.
func TestReadStateAcceptsACompleteRead(t *testing.T) {
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		switch {
		case cmd == "show running-config":
			return s3400RunningConfig(), true
		case cmd == "show vlan":
			return "VLAN Name        Status     Ports\n" +
				"1    default     active     g0/1\n", true
		case strings.HasPrefix(cmd, "show interface"):
			return "GigaEthernet0/1 is up, line protocol is up\n", true
		}
		return "", true
	})
	var st, err = c.ReadState(context.Background())
	if err != nil {
		t.Fatalf("a complete read must succeed: %v", err)
	}
	if len(st.Ports) != 52 {
		t.Fatalf("complete read reported %d ports, want 52", len(st.Ports))
	}
	if st.Ports["GE23"].PVID != 123 {
		t.Errorf("GE23 PVID = %d, want 123: the whole stanza was there", st.Ports["GE23"].PVID)
	}
	if st.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99", st.MgmtVLAN)
	}
}

// AN IN-BAND CLI ERROR IS NOT A TRUNCATION, at the transport level and at the
// ReadState level, and keeping the two apart is what makes this fix safe to put
// under an apply path: a device that ANSWERS — even to refuse — has finished
// speaking, and this connector has always been entitled to parse around that.
func TestInBandCLIErrorIsNotTruncation(t *testing.T) {
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		switch cmd {
		case "show bogus":
			// The captured refusal shape on Version 2.0.2J: the offending word, a
			// caret line, a bare sentence — and then the prompt comes back.
			return "show bogus\n          ^\nUnknown command", true
		case "show dies":
			return "partial output that never ends", false
		}
		return "", true
	})
	var ctx = context.Background()

	var out, err = c.cli.runPaged(ctx, "show bogus", 5*time.Second)
	if err == nil {
		t.Fatal("a refused command must still be an error")
	}
	if isTruncated(err) {
		t.Errorf("\"Unknown command\" was reported as a truncation: %v — a caller refusing "+
			"truncated reads would then refuse every device that answers a refusal", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("the refusal text must still reach the caller")
	}

	out, err = c.cli.runPaged(ctx, "show dies", 5*time.Second)
	if err == nil {
		t.Fatal("a session that went away must be an error")
	}
	if !isTruncated(err) {
		t.Errorf("a dead session was not reported as a truncation: %v", err)
	}
	if !strings.Contains(out, "partial output") {
		t.Errorf("the prefix must still reach the caller for diagnosis: %q", out)
	}
}

// The same distinction, one level up: a running-config that came back COMPLETE
// but happens to carry a line reCLIError matches is still parsed. Before the
// sentinel this was the behaviour the empty-output test protected, and it must
// survive the tightening — an apply path that started refusing here would fail
// closed on devices that work today.
func TestReadStateStillParsesAConfigThatCarriesACLIErrorLine(t *testing.T) {
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if cmd == "show running-config" {
			return s3400RunningConfig() + "Unknown command", true
		}
		return "", true
	})
	var st, err = c.ReadState(context.Background())
	if err != nil {
		t.Fatalf("a COMPLETE read carrying a refusal line must still be parsed: %v", err)
	}
	if len(st.Ports) != 52 {
		t.Errorf("reported %d ports, want 52", len(st.Ports))
	}
}

// The port-admin guardrail's yardstick. 0 is "I do not know", and a caller
// that concluded anything from a guessed faceplate would fire or hold on a
// device it has never identified.
func TestFaceplatePortsDeclinesToGuess(t *testing.T) {
	if faceplatePorts("S3400-48T4SP") != 52 {
		t.Errorf("48T4SP = %d, want 52", faceplatePorts("S3400-48T4SP"))
	}
	if faceplatePorts("S3400-24T4FP") != 28 {
		t.Errorf("24T4FP = %d, want 28", faceplatePorts("S3400-24T4FP"))
	}
	if faceplatePorts("") != 0 || faceplatePorts("some unknown box") != 0 {
		t.Error("an unreadable model must total 0, so the guardrail declines to conclude")
	}
}

// The widened guardrail, as a rule rather than as a write. 23 of 52 is ignorance
// exactly as firmly as 0 of 52; 52 of 52 is knowledge; MORE than the faceplate is
// left alone (a connector reporting extra sockets is being generous, not short);
// and an unknown faceplate falls back to the emptiness test it always had.
func TestMembershipCoverageTreatsAShortReadAsIgnorance(t *testing.T) {
	if membershipCoverage(23, 52) != 0 {
		t.Error("23 of 52 ports must collapse to \"nothing usable was read\": that is the " +
			"2026-08-11 read, and a test for emptiness walks straight past it")
	}
	if membershipCoverage(52, 52) != 52 {
		t.Error("a full read must be taken at face value")
	}
	if membershipCoverage(54, 52) != 54 {
		t.Error("more ports than the faceplate declares is not a short read")
	}
	if membershipCoverage(23, 0) != 23 {
		t.Error("an unknown faceplate must not make every read look short")
	}
	if membershipCoverage(0, 0) != 0 {
		t.Error("an empty membership is ignorance whatever the faceplate says")
	}
	// And the fleet-wide rule it feeds: zero coverage reports the management VLAN
	// as UNKNOWN, which routes the guard to the branch that spares linked ports.
	if driver.MgmtVLANWhenMembershipRead(99, membershipCoverage(23, 52)) != 0 {
		t.Error("a short membership must report the management VLAN as unknown")
	}
	if driver.MgmtVLANWhenMembershipRead(99, membershipCoverage(52, 52)) != 99 {
		t.Error("a complete membership must keep the management VLAN it read")
	}
}

package s3900

// THE SAME DEFECT CLASS AS THE S3400's, on the connector next door.
//
// "show run" here is read by [cliConn.run], which returns EARLY — holding
// whatever bytes arrived — when the session dies, the deadline expires or the
// context is cancelled, and returns at the END, after the prompt, when the CLI
// refused the command. Both used to arrive at ReadState as "an error", and
// ReadState only refused when the output was EMPTY.
//
// parseRunningConfig cannot notice: it reports the interface blocks it reached
// and is silent about the rest. So a cut "show run" left here as a complete
// answer describing a switch with fewer ports than the device has, and the
// caller stored it as an observation with `observed_at = now()` — which is
// exactly how the S3400 produced 29 phantom "this port no longer exists"
// differences on 2026-08-11.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// scriptedCLI answers a canned reply per command and can stop mid-answer, which
// is the only way to build a truncated read: the text alone never carries it.
type scriptedCLI struct {
	out  chan byte
	sent []string
	line strings.Builder
	dead bool

	// answer returns the body and whether the prompt follows. false closes the
	// stream instead — the device going away mid-sentence.
	answer func(cmd string) (body string, endsAtPrompt bool)
}

func newScriptedCLI(answer func(string) (string, bool)) (*Client, *scriptedCLI) {
	var d = &scriptedCLI{out: make(chan byte, 1<<20), answer: answer}
	var c = &Client{cli: &cliConn{out: d.out, stdin: d}, host: "192.0.2.7",
		dialect: dialectFSOS17}
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
	// RECORDED BEFORE the dead check: what the connector SENT is the question a
	// "nothing was written" assertion asks, and a session that already went
	// away must not make a write invisible to the test.
	d.sent = append(d.sent, cmd)
	if d.dead {
		return
	}
	var body, endsAtPrompt = d.answer(cmd)
	d.emit(body)
	if !endsAtPrompt {
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

// s3900ShowRun builds a 24-port configuration. Every port carries an explicit
// access VLAN, so a port reported on VLAN 1 can only be the parser's default.
func s3900ShowRun() string {
	var b strings.Builder
	b.WriteString("!\nhostname \"labs3900\"\n!\n")
	var i int
	for i = 1; i <= 24; i++ {
		fmt.Fprintf(&b, "interface ethernet 1/%d\n switchport access vlan %d\n!\n", i, 100+i)
	}
	b.WriteString("interface vlan 99\n ip address 192.0.2.7 255.255.0.0\n!\n")
	return b.String()
}

// s3900TruncatedShowRun cuts INSIDE port 11's block: ports 1..11 present, the
// eleventh half-read and defaulted, nothing after it.
func s3900TruncatedShowRun() string {
	var full = s3900ShowRun()
	var cut = strings.Index(full, " switchport access vlan 111\n")
	if cut < 0 {
		panic("fixture: GE11's block is not where this test thinks it is")
	}
	return full[:cut]
}

// The fixture is the failure: the parser is confident and wrong, and no test on
// the TEXT could ever catch it.
func TestTruncatedShowRunParsesAsAConfidentWrongSwitch(t *testing.T) {
	var rc = parseRunningConfig(s3900TruncatedShowRun(), "")
	if len(rc.Ports) != 11 {
		t.Fatalf("the truncated capture parses to %d ports, want 11", len(rc.Ports))
	}
	if rc.Ports["GE11"].PVID != 1 {
		t.Errorf("GE11 PVID = %d, want 1: the half-read block must come out carrying the "+
			"factory default, which is what reads as drift", rc.Ports["GE11"].PVID)
	}
	if len(parseRunningConfig(s3900ShowRun(), "").Ports) != 24 {
		t.Fatal("the complete capture must parse to 24 ports")
	}
}

// THE FIX.
func TestReadStateRefusesATruncatedShowRun(t *testing.T) {
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		if strings.HasPrefix(cmd, "show run") {
			return s3900TruncatedShowRun(), false
		}
		return "", true
	})
	var st, err = c.ReadState(context.Background())
	if err == nil {
		t.Fatalf("ReadState accepted a truncated \"show run\" and reported %d ports",
			len(st.Ports))
	}
	if len(st.Ports) != 0 {
		t.Errorf("the refusal still carried %d ports; a refused read must report no inventory",
			len(st.Ports))
	}
}

// A COMPLETE READ IS UNCHANGED — the half that keeps the fix from being worse
// than the defect.
func TestReadStateAcceptsACompleteShowRun(t *testing.T) {
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		switch {
		case strings.HasPrefix(cmd, "show run"):
			return s3900ShowRun(), true
		case strings.HasPrefix(cmd, "show interfaces brief"):
			return "Interface  Name  Status  Vlan  Duplex  Speed  Type\n" +
				"Eth 1/1          Up      1     Full    1000   1000BASE-T\n", true
		}
		return "", true
	})
	var st, err = c.ReadState(context.Background())
	if err != nil {
		t.Fatalf("a complete read must succeed: %v", err)
	}
	if len(st.Ports) != 24 {
		t.Fatalf("complete read reported %d ports, want 24", len(st.Ports))
	}
	if st.Ports["GE11"].PVID != 111 {
		t.Errorf("GE11 PVID = %d, want 111", st.Ports["GE11"].PVID)
	}
}

// AN IN-BAND CLI ERROR IS NOT A TRUNCATION. The device answered — the answer was
// a refusal — and the output is COMPLETE. Collapsing the two would make every
// caller that refuses truncated reads also refuse working devices.
func TestInBandCLIErrorIsNotTruncation(t *testing.T) {
	var c, _ = newScriptedCLI(func(cmd string) (string, bool) {
		switch cmd {
		case "show bogus":
			return "% Invalid input detected at '^' marker.", true
		case "show dies":
			return "partial output that never ends", false
		}
		return "", true
	})
	var ctx = context.Background()

	var out, err = c.cli.run(ctx, "show bogus", 5*time.Second)
	if err == nil {
		t.Fatal("a refused command must still be an error")
	}
	if isTruncated(err) {
		t.Errorf("\"%% Invalid input\" was reported as a truncation: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("the refusal text must still reach the caller")
	}

	out, err = c.cli.run(ctx, "show dies", 5*time.Second)
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

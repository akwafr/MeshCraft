package procurve

import (
	"strings"
	"testing"
)

// THE REGRESSION THIS EXISTS FOR. The 2610 terminates lines with LF then CR —
// reversed from the usual pairing — so every line arrives with a leading stray
// carriage return. Captured from the wire on 2026-08-02.
//
// The original fixture came through a capture pipeline that stripped control
// characters, so the parser passed its tests and returned ZERO VLANs against the
// real device. This test uses the byte sequence the device actually sends.
const rawLineEndings = "hostname \"SW-PLAYER-79\" \n\r" +
	"max-vlans 30 \n\r" +
	"vlan 1 \n\r   name \"DEFAULT_VLAN\" \n\r   ip address dhcp-bootp \n\r   no untagged 1-52 \n\r   exit \n\r" +
	"vlan 99 \n\r   name \"admin\" \n\r   ip address 203.0.113.79 255.255.255.0 \n\r   tagged 45-52 \n\r   exit \n\r" +
	"vlan 200 \n\r   name \"joueurs\" \n\r   untagged 1-52 \n\r   exit \n\r" +
	"interface 45\n\r   dhcp-snooping trust\n\r   exit\n\r"

func TestParseSurvivesTheDevicesLineEndings(t *testing.T) {
	var rc = parseRunningConfig(rawLineEndings)

	if rc.Hostname != "SW-PLAYER-79" {
		t.Errorf("hostname = %q — the anchor did not survive the leading CR", rc.Hostname)
	}
	if rc.MaxVLANs != 30 {
		t.Errorf("max-vlans = %d", rc.MaxVLANs)
	}
	if len(rc.VLANs) != 3 {
		t.Fatalf("parsed %d VLANs, want 3: this is the live-read failure", len(rc.VLANs))
	}
	if rc.VLANs[200] != "joueurs" {
		t.Errorf("VLAN 200 name = %q", rc.VLANs[200])
	}
	// "no untagged" is still a removal, CR or not.
	if len(rc.Untagged[1]) != 0 {
		t.Errorf("VLAN 1 untagged = %v, want none", rc.Untagged[1])
	}
	if len(rc.Untagged[200]) != 52 {
		t.Errorf("VLAN 200 untagged = %d ports, want 52", len(rc.Untagged[200]))
	}
	if len(rc.Tagged[99]) != 8 {
		t.Errorf("VLAN 99 tagged = %v, want 45-52", rc.Tagged[99])
	}
	if rc.MgmtVLAN != 99 {
		t.Errorf("management VLAN = %d, want 99", rc.MgmtVLAN)
	}
	// The interface stanza that FOLLOWS the VLANs must not be swallowed into the
	// last one: the stanza has to end at its own "exit" line.
	if len(rc.Untagged[200]) > 52 {
		t.Error("the trailing interface block leaked into VLAN 200")
	}
}

// ESC-E is this CLI's line break on the prompt path. Deleting it as an escape
// sequence collapses a whole session onto one line, and every multi-line anchor
// then matches only at the start of the buffer — which is how a login that had
// succeeded timed out waiting for a prompt already on screen.
func TestStripControlTreatsNELAsALineBreak(t *testing.T) {
	var raw = "SW-PLAYER-79# no page\x1bESW-PLAYER-79# configure terminal\x1bESW-PLAYER-79(config)# "
	var got = stripControl(raw)
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("got %d line breaks from two NELs: %q", strings.Count(got, "\n"), got)
	}
	if !rePrompt.MatchString(got) {
		t.Errorf("the config-mode prompt was not recognised in %q", got)
	}
	// Cursor sequences still go, and the CR of the LF+CR pairing with them.
	if stripControl("\x1b[?25hSW# \r") != "SW# " {
		t.Errorf("cursor sequences or CR survived: %q", stripControl("\x1b[?25hSW# \r"))
	}
}

// CAPTURED on a 2810-24G N.11.15: the banner gate is followed by no line break
// at all, the prompt being placed by cursor addressing. Without treating a move
// to column 1 as a new line, the prompt is glued to "continue" and never matched.
func TestStripControlTreatsColumnOneAsALineBreak(t *testing.T) {
	var raw = "\x1b[24;1HPress any key to continue\x1b[1;1H\x1b[?25h\x1b[24;27H\x1b[2J" +
		"\x1b[?7l\x1b[1;200r\x1b[200;1H\x1b[2K\x1b[200;1H\x1b[?25h\x1b[200;1HSW-PLAYER-90> "
	var got = stripControl(raw)
	if !rePrompt.MatchString(got) {
		t.Fatalf("the operator prompt after the banner gate was not recognised in %q", got)
	}
	// A move to another column is the echo repaint, not a new line.
	var echo = stripControl("show versi\x1b[200;15H\x1b[?25h\x1b[200;25Hon")
	if echo != "show version" {
		t.Errorf("echo repaint split the command: %q", echo)
	}
}

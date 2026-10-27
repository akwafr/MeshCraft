package procurve

import (
	"strings"
	"testing"
)

func TestHostnameCmdsQuoteTheNameLikeTheDeviceDoes(t *testing.T) {
	// The quotes are not decoration: this range renders its own configuration
	// line quoted (`hostname "SW-PLAYER-79"`, captured 2026-08-03), and writing
	// the form the device itself uses is the only form this project has evidence
	// about.
	var cmds = hostnameCmds("SW-PLAYER-79")
	if len(cmds) != 1 {
		t.Fatalf("cmds=%v, want exactly one configuration line", cmds)
	}
	if cmds[0] != `hostname "SW-PLAYER-79"` {
		t.Errorf("cmds[0]=%q, want %q", cmds[0], `hostname "SW-PLAYER-79"`)
	}
	// runConfigBlock supplies "configure terminal" and the trailing "exit". A
	// builder repeating them would leave the session a level deeper than the
	// next gesture expects.
	var c string
	for _, c = range cmds {
		if strings.HasPrefix(c, "config") || c == "exit" {
			t.Errorf("builder must not carry mode changes, got %q", c)
		}
	}
}

func TestHostnameWrittenFormIsReadBackByTheConfigParser(t *testing.T) {
	// The quoted line this connector sends must be the line its own parser
	// reads — quotes stripped. If these two ever disagree, verification would
	// fail on a device that did exactly what it was told.
	var rc = parseRunningConfig("hostname \"SW-PLAYER-79\" \n\r   exit \n\r" +
		strings.Repeat("; padding to clear minRunningConfig ", 8))
	if rc.Hostname != "SW-PLAYER-79" {
		t.Errorf("Hostname=%q, want the unquoted name", rc.Hostname)
	}
}

func TestHostnameVerificationReadsTheCapturedSystemInformation(t *testing.T) {
	// Verification goes through "show system-information", so the row it keys
	// on must be the one the REAL capture carries. Fixture: verbatim
	// SW-PLAYER-79 output, 2026-08-03.
	var got = parseSystemInfo(fixture(t, "show_system_information.txt")).Name
	if got != "SW-PLAYER-79" {
		t.Errorf("System Name = %q, want SW-PLAYER-79 — the row this write "+
			"verifies against moved", got)
	}
}

func TestHostnameMismatchNamesTheThreeFaults(t *testing.T) {
	var absent = hostnameMismatch("sw-lab-01", "")
	if !strings.Contains(absent, "no system name") {
		t.Errorf("an absent system name must be reported as such, got %q", absent)
	}
	// A truncation must publish the observed ceiling: that number is not
	// documented anywhere in this project, so the field is where it gets learned.
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very-")
	if !strings.Contains(cut, "15 bytes") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	var other = hostnameMismatch("sw-lab-01", "HP-2610")
	if !strings.Contains(other, `"HP-2610"`) || !strings.Contains(other, `"sw-lab-01"`) {
		t.Errorf("a divergence must show both names, got %q", other)
	}
}

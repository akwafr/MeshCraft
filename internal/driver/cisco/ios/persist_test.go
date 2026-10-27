package ios

// A provisioning push persists itself.
//
// Regression guard for a whole run lost at the next reload. This builder used to
// end at the account, so the domain, the RSA key, SSH, the VLANs, the SVI, the
// address, the gateway, the trunks and the credentials all lived in the running
// configuration and vanished on power-cycle — with nothing to report it, because
// every command had been accepted and the switch was reachable.

import (
	"strings"
	"testing"
)

func TestMgmtPushPersistsItself(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.2", PrefixLen: 24, Gateway: "192.0.2.1",
		Username: "mcadmin", Password: "secret1234567890",
	})
	if len(cmds) < 2 {
		t.Fatalf("push too short to be a provisioning run: %v", cmds)
	}
	if cmds[len(cmds)-1] != SaveCmd {
		t.Errorf("the push ends with %q, not %q: everything it wrote is lost on reload",
			cmds[len(cmds)-1], SaveCmd)
	}
	// EXEC, so the block must leave configuration mode first — otherwise the save
	// is refused and the run silently persists nothing, which is the same bug
	// arriving by another door.
	if cmds[len(cmds)-2] != "end" {
		t.Errorf("the save is issued from configuration mode (%q precedes it)",
			cmds[len(cmds)-2])
	}
	// NOT the fallback form. `copy running-config startup-config` always asks
	// "Destination filename [startup-config]?", and a blind command list cannot
	// answer a prompt — the next line would be typed in as the answer. That is
	// the failure that cost the FS connector a deployment (cf. admin.go).
	if strings.Contains(strings.Join(cmds, "\n"), SaveCmdFallback) {
		t.Errorf("the push uses the prompting save form: a command list cannot answer "+
			"\"Destination filename?\" and the next line becomes the answer: %v", cmds)
	}
}

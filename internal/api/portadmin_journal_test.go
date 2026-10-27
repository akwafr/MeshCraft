package api

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// The fleet log must describe the DEVICE, not the request.
//
// Found on hardware, 2026-08-11, on core01.lab: a plan naming ET34, ET37 and
// ET48 — the fleet core's three live uplinks — was refused in full by the EOS
// connector's management-VLAN guardrail. `ports_written` came back empty, three
// refusals came back in `Skipped`, no configuration command was ever sent, and
// the journal recorded, at WARNING:
//
//	Port administrative state written on "core01.lab": ET34 → shut,
//	ET37 → shut, ET48 → shut
//
// The gesture was safe; the history it left was not. This is the assertion that
// keeps the two apart.
func TestPortAdminJournalLineDescribesWhatMoved(t *testing.T) {
	var requested = map[string]bool{"ET34": false, "ET37": false, "ET48": false}

	// --- The refusal, verbatim in shape from the 2026-08-11 run.
	var refused = driver.ApplyResult{
		Skipped: []string{
			"ET34 NOT shut: it carries an ACTIVE link and the management VLAN (99) …",
			"ET37 NOT shut: it carries an ACTIVE link and the management VLAN (99) …",
			"ET48 NOT shut: it carries an ACTIVE link and the management VLAN (99) …",
		},
	}
	var msg, sev = portAdminJournalLine("core01.lab", requested, refused)
	if strings.Contains(msg, "state written on") {
		t.Errorf("a plan the connector refused in full is journaled as WRITTEN: %q", msg)
	}
	if !strings.Contains(msg, "NO port administrative state changed") {
		t.Errorf("the refusal line does not say nothing changed: %q", msg)
	}
	if !strings.Contains(msg, "3 refused by the connector") {
		t.Errorf("the refusal line does not say the ports were refused: %q", msg)
	}
	if sev != model.SevInfo {
		t.Errorf("severity %d for a shutdown that did NOT happen, want SevInfo (%d): a "+
			"warning here trains an operator to ignore the ones that mean something",
			sev, model.SevInfo)
	}

	// --- One port moved, two were refused: the line names the one that moved and
	// still says the others were not.
	var partial = driver.ApplyResult{
		PortsWritten: []string{"ET37"},
		Skipped:      []string{"ET34 NOT shut: …", "ET48 NOT shut: …"},
	}
	msg, sev = portAdminJournalLine("core01.lab", requested, partial)
	if !strings.Contains(msg, "ET37 → shut") {
		t.Errorf("the port that moved is missing from %q", msg)
	}
	if strings.Contains(msg, "ET34") || strings.Contains(msg, "ET48") {
		t.Errorf("a port that did NOT move is named as written in %q", msg)
	}
	if !strings.Contains(msg, "2 refused by the connector") {
		t.Errorf("the refusals are not counted in %q", msg)
	}
	if sev != model.SevWarning {
		t.Errorf("severity %d for a real shutdown, want SevWarning (%d)", sev, model.SevWarning)
	}

	// --- Bringing ports back up is ordinary, whatever the count.
	var up = driver.ApplyResult{PortsWritten: []string{"ET1"}}
	msg, sev = portAdminJournalLine("core01.lab", map[string]bool{"ET1": true}, up)
	if !strings.Contains(msg, "ET1 → enabled") {
		t.Errorf("the enable is not rendered: %q", msg)
	}
	if strings.Contains(msg, "refused") {
		t.Errorf("nothing was refused and the line says otherwise: %q", msg)
	}
	if sev != model.SevInfo {
		t.Errorf("severity %d for an enable, want SevInfo (%d)", sev, model.SevInfo)
	}

	// --- The port was ALREADY where the plan wanted it. The connector sends
	// nothing and reports nothing skipped — an ordinary convergent apply, and it
	// must not read as a shutdown either.
	msg, sev = portAdminJournalLine("core01.lab", map[string]bool{"ET1": false},
		driver.ApplyResult{})
	if strings.Contains(msg, "state written on") {
		t.Errorf("a no-op apply is journaled as written: %q", msg)
	}
	if sev != model.SevInfo {
		t.Errorf("severity %d for a no-op apply, want SevInfo (%d)", sev, model.SevInfo)
	}
}

package driver

import (
	"errors"
	"fmt"
)

// --- Resolving a connector, and refusing legibly ---

// ConnectorFor returns a device's connector, or the explanation for its absence.
//
// THE MANDATORY GATEWAY for the API layer, and the reason this file exists: as
// long as every handler called `instanton.OpenAdmin` directly, none of them could
// get the platform wrong — they ALL got it wrong, all the time, and it only showed
// on a fleet with a single vendor. One single place that resolves, and three
// distinct refusals that can be read at a glance:
//
//   - no connector associated: discovery failed to file this device, or it was
//     entered by hand. There is nothing to fix on meshcraft's side;
//   - connector unknown to this binary: the database carries the name of a
//     connector this version doesn't contain. A symptom of a version rollback, not
//     of a device problem;
//   - connector present but unable to REACH the platform: meshcraft can describe
//     this range (fingerprint, capabilities) but not talk to it. This device can
//     be inventoried and monitored, it cannot be configured.
//
// The three are fixed by gestures that have nothing to do with each other, hence
// three distinct messages and not one.
func ConnectorFor(name string) (Connector, error) {
	if name == "" {
		return nil, errors.New("no connector is associated with this device: " +
			"meshcraft does not know which platform it would talk to, and does not guess — " +
			"re-run discovery on its address so it can be identified")
	}
	var d, known = Get(name)
	if !known {
		return nil, fmt.Errorf("connector %q is unknown to this meshcraft version: "+
			"the device was identified by a version that contained it. "+
			"Nothing to do on the device — it is the controller binary that is not "+
			"the expected one", name)
	}
	var c, reachable = d.(Connector)
	if !reachable {
		return nil, fmt.Errorf("connector %q can DESCRIBE this platform but "+
			"not yet reach it: meshcraft can inventory and monitor it, it cannot "+
			"write anything on it. This is not a failure, it is a capability that "+
			"is not shipped", name)
	}
	return c, nil
}

package procurve

// Turning the parsed configuration into meshcraft's port-centric State.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// defaultPortPrefix: the faceplate prefix used when the inventory carries none.
//
// "GE" like the rest of the fleet, and on this range that is a deliberate
// simplification worth stating: ports 1-48 of a 2610-48 are 10/100, not gigabit.
// The prefix is a NAMING scheme meshcraft applies uniformly so a port profile
// written once fits every platform — it is not a claim about link speed, which
// the faceplate groups carry.
const defaultPortPrefix = "GE"

// minRunningConfig: below this, a read is a desynchronisation and not a device.
// The smallest real configuration on this range still carries its header, a
// hostname and VLAN 1 — several hundred characters.
const minRunningConfig = 200

func (c *Client) portPrefix() string {
	if c.tgt.PortPrefix != "" {
		return c.tgt.PortPrefix
	}
	return defaultPortPrefix
}

// StandardPortName translates a name OBSERVED on the platform — a bare number,
// which is all this CLI uses — into the standard-model name.
//
// On the Driver rather than the Admin: a pure function, useful before any
// session exists (cf. driver.PortNamer).
func (d *Driver) StandardPortName(deviceName string) (string, bool) {
	var n, err = strconv.Atoi(strings.TrimSpace(deviceName))
	if err != nil || n < 1 {
		return "", false
	}
	return defaultPortPrefix + strconv.Itoa(n), true
}

// portNumber is the inverse: standard-model name → the number this CLI wants.
func portNumber(name string) (int, bool) {
	var digits = strings.TrimLeftFunc(strings.TrimSpace(name), func(r rune) bool {
		return r < '0' || r > '9'
	})
	var n, err = strconv.Atoi(digits)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// ReadState re-reads the device. READ ONLY, replayable.
//
// The running-config is the authoritative source here, as it is on the FS
// connector and for the same reason: it states INTENT exactly, where the status
// views summarise. What it does NOT carry is link state — that needs « show
// interfaces brief », whose column layout has not been captured on this range, so
// LinkUp is left empty rather than guessed. An invented link state is worse than
// an absent one: it decides which ports an apply is allowed to touch.
func (c *Client) ReadState(ctx context.Context) (State, error) {
	var out = State{Ports: map[string]driver.PortState{}, Links: map[string]driver.LinkState{}}
	if c.cli == nil {
		return out, errors.New("no open session on this access")
	}
	var text, err = c.cli.run(ctx, "show running-config", showRunTimeout)
	if err != nil && text == "" {
		c.step(false, "running-config read failed", firstLine(err.Error()))
		out.Notes = []string{"INCOMPLETE READ: " + firstLine(err.Error())}
		return out, err
	}
	// A CONFIGURATION THIS SHORT IS NOT A CONFIGURATION. Every unit of this range
	// prints kilobytes, starting with its « Running configuration: » header — so a
	// handful of characters means the read desynchronised, not that the switch is
	// empty. Returning it as state would let an apply "create" every VLAN the
	// device already has and rewrite membership against a fiction, which is
	// exactly what happened before the drain in cli.go was added.
	if len(strings.TrimSpace(text)) < minRunningConfig {
		c.step(false, "running-config read too short to be real",
			fmt.Sprintf("%d characters — refusing to report this as the device state",
				len(text)))
		return out, fmt.Errorf("running-config read returned %d characters: "+
			"the CLI read desynchronised, and an empty state here would be acted on "+
			"as if the switch had no VLANs", len(text))
	}
	var rc = parseRunningConfig(text)
	c.step(true, "running-config read",
		fmt.Sprintf("%d characters, %d VLANs", len(text), len(rc.VLANs)))

	out.MgmtVLAN = rc.MgmtVLAN
	var id int
	var name string
	for id, name = range rc.VLANs {
		out.VLANs = append(out.VLANs, driver.VLAN{ID: id, Name: name})
	}
	sortVLANs(out.VLANs)

	// INVERT the VLAN-centric tables into per-port state.
	var prefix = c.portPrefix()
	var ports = map[int]*driver.PortState{}
	var get = func(p int) *driver.PortState {
		var st, ok = ports[p]
		if !ok {
			st = &driver.PortState{Mode: "access"}
			ports[p] = st
		}
		return st
	}
	var list []int
	var p int
	for id, list = range rc.Untagged {
		for _, p = range list {
			get(p).PVID = id
		}
	}
	for id, list = range rc.Tagged {
		for _, p = range list {
			var st = get(p)
			st.Tagged = append(st.Tagged, id)
			// A port carrying tagged VLANs is a trunk in meshcraft's vocabulary,
			// whatever the platform calls it — this CLI has no mode keyword at all,
			// membership IS the mode.
			st.Mode = "trunk"
		}
	}
	for p = range ports {
		var st = ports[p]
		sortInts(st.Tagged)
		out.Ports[prefix+strconv.Itoa(p)] = *st
	}

	if rc.MaxVLANs > 0 {
		// The runtime ceiling, surfaced where a plan that will not fit can be seen
		// BEFORE the write that would fail on it.
		out.Notes = append(out.Notes, fmt.Sprintf(
			"this switch is configured for at most %d VLANs (« max-vlans »); raising it "+
				"requires a reboot", rc.MaxVLANs))
	}
	out.Notes = append(out.Notes,
		"link state is not read on this range: the « show interfaces brief » column "+
			"layout has not been captured, and a guessed link state decides which ports "+
			"an apply may touch")
	return out, nil
}

func sortVLANs(v []driver.VLAN) {
	var i, j int
	for i = 1; i < len(v); i++ {
		var x = v[i]
		for j = i; j > 0 && v[j-1].ID > x.ID; j-- {
			v[j] = v[j-1]
		}
		v[j] = x
	}
}

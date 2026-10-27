package swos

import (
	"fmt"
	"strconv"
	"strings"
)

// PORT NAMES: the one the DEVICE uses, and the old one that stays accepted.
//
// # WHAT CHANGED, AND WHY
//
// The first delivery named the ports "GE1"… "GE26", borrowing from the Instant
// On range whose connector served as the model. The CSS326, for its part,
// names its sockets "Port1"… "Port24", "SFP1", "SFP2" — that is what /link.b
// serves in its `nm` field, it is what its web UI displays, and it is
// therefore the name the operator reads on the two screens they compare. A
// port called "GE25" by meshcraft and "SFP1" by the switch is a mapping left
// to be done in one's head, on the one piece of data that decides where a
// write lands.
//
// The neighbouring RouterOS connector had already settled this without having
// to decide it: meshcraft's names there ARE the device's (`ether3`,
// `sfp-sfpplus1`), because that platform publishes them. SwOS publishes them
// too.
//
// # TWO FAMILIES, AND THAT IS THIS FILE'S POINT OF CAUTION
//
// [Facade.Index] recognises BOTH conventions:
//
//	"Port12", "SFP1"  the DEVICE's family, the one now written. Two separate
//	                  counters: copper on one side, cages on the other, each
//	                  starting at 1;
//	"GE12", "GE25"    meshcraft's NUMBERED family, cages included and in one
//	                  run. It is the one of intents already recorded in the
//	                  database, and of any UI that still displays them.
//
// Refusing the old family would have SILENTLY ignored the intents of an
// already-configured fleet — `PlanPorts` files an unknown name under its
// `Skipped`, so a port configuration would have stopped being applied
// without any error saying so. On the two reference devices, `devices.ports`
// is empty and nothing would have been lost; that is no reason to build a
// translation that accepts only one direction, since the database of a fleet
// in service is not empty.
//
// THE DEVICE COMES FIRST, and the order has a consequence to be aware of: when
// the faceplate has been re-read from the device, its group prefix is "Port",
// and the numbered family therefore becomes "Port1"… "Port26". "Port25" then
// designates the first cage by the old convention, while the device calls it
// "SFP1". Both readings land on the SAME index — 24 — because the continuous
// numbering and the faceplate describe the same faceplate; it is a happy
// coincidence, not a guarantee, and that is why the device's family is tried
// first.
//
// # WHAT `nm` DOES NOT BECOME: A KEY
//
// The `nm` field is FREE TEXT. The device's UI lets it be renamed port by
// port, it has no uniqueness constraint, and nothing follows a rename. Making
// it the standard model's key would bring three defects at once:
//
//   - two ports with the same name would overwrite each other in the [State]
//     maps — a VLAN membership displayed for one port would be another's;
//   - `port_groups` cannot express a list of arbitrary names: the faceplate is
//     written there as prefix + count, and it is what the web UI expands to
//     enumerate the sockets. A port named "Uplink" would become invisible to
//     the UI while existing in the read state;
//   - a recorded intent would stop designating a port at the first rename
//     made from the switch's UI.
//
// This file therefore keeps the device's CONVENTION — two families, `Port`
// for copper and `SFP` for cages, as /link.b serves them by default — and not
// the strings themselves. The strings remain served where they decide
// nothing: [PortTelemetry.DeviceName] for diagnosis, and [renamedPorts], which
// FLAGS the ports whose observed name differs from the faceplate name —
// otherwise the gap between the two screens would come back, the other way
// round and unexplained.

const (
	// CopperPortPrefix names the copper sockets. OBSERVED in `nm` on the
	// reference CSS326-24G-2S+: "Port1"… "Port24".
	CopperPortPrefix = "Port"
	// SFPPortPrefix names the optical cages, NUMBERED SEPARATELY and starting
	// again at 1. OBSERVED: "SFP1", "SFP2" for the ports of index 24 and 25.
	//
	// The prefix does not distinguish SFP from SFP+: the device does not
	// either (its two 10-gigabit cages are called "SFP1" and "SFP2"), and the
	// faceplate speed already says what the cage accepts.
	SFPPortPrefix = "SFP"
)

// Facade is the NAMED faceplate of a SwOS device: its geometry, plus the two
// naming conventions that translate a name into an index.
//
// ONE TYPE AND NOT TWO PARAMETERS, and that is the lesson of the comment on
// [PortsInput]: "a positional parameter added in the middle of a list is
// exactly the mistake that passes `ports` where the code expects `prefix`".
// Geometry and naming are now inseparable — knowing where the cages start is
// what decides whether index 24 is called "SFP1" or "Port25" — and separating
// them would leave two values one of which can lie about the other.
type Facade struct {
	ports    int
	sfp      int
	sfpFirst int
	// legacy is the prefix of the numbered family, as the TARGET carries it
	// (cf. Admin.portPrefix). It no longer names anything: it serves only for
	// READING.
	legacy string
}

// NewFacade builds the named faceplate of a device.
//
// `legacyPrefix` is the prefix of the numbered family to accept on read.
// Empty, [DefaultPortPrefix] applies — the project's historical family.
func NewFacade(ports, sfp, sfpFirst int, legacyPrefix string) Facade {
	if legacyPrefix == "" {
		legacyPrefix = DefaultPortPrefix
	}
	return Facade{ports: ports, sfp: sfp, sfpFirst: sfpFirst, legacy: legacyPrefix}
}

// Ports returns the number of ports on the faceplate.
func (f Facade) Ports() int { return f.ports }

// SFPFirst returns the index of the first optical cage.
func (f Facade) SFPFirst() int { return f.sfpFirst }

// isCage says whether the index falls within the RANGE of cages declared by
// /link.b.
//
// A range and not "the last ports": `sfpo` puts the first cage wherever it
// likes, and the reference device puts it at the end (`sfpo:0x18` for 26
// ports) without anything forcing it to. A model that placed its cages at the
// start would therefore still see its copper sockets numbered from 1, which is
// the expected behaviour of two independent counters.
func (f Facade) isCage(i int) bool {
	return f.sfp > 0 && i >= f.sfpFirst && i < f.sfpFirst+f.sfp
}

// Name returns the faceplate name of the port of index `i` (0-based).
//
// The two counters are independent: copper counts the ports that are NOT
// cages, in faceplate order, cages count cages. That is what the reference
// device serves in `nm`, and [renamedPorts] verifies it on the real capture
// rather than on this sentence.
func (f Facade) Name(i int) string {
	if i < 0 || i >= f.ports {
		// An index outside the faceplate is not named: naming it anyway would
		// invent a port the device does not have, and the silence of an empty
		// name would send someone looking for the error in the page rather
		// than in the call.
		return fmt.Sprintf("port#%d(outside a faceplate of %d ports)", i, f.ports)
	}
	if f.isCage(i) {
		return SFPPortPrefix + strconv.Itoa(i-f.sfpFirst+1)
	}
	// The port's rank among the sockets that are not cages.
	var rank = i + 1
	if f.sfp > 0 && i >= f.sfpFirst+f.sfp {
		rank = i + 1 - f.sfp
	}
	return CopperPortPrefix + strconv.Itoa(rank)
}

// Names returns the faceplate names, in index order.
func (f Facade) Names() []string {
	var out = make([]string, 0, f.ports)
	var i int
	for i = 0; i < f.ports; i++ {
		out = append(out, f.Name(i))
	}
	return out
}

// Index translates a port name into a SwOS index (0-based), in either family.
//
// A name that follows neither convention, or that designates a port the
// device does not have, is REFUSED rather than guessed: sending a wrong port
// number is a write in the wrong place, and on this platform, the one that
// cuts the uplink.
func (f Facade) Index(name string) (int, bool) {
	var i, ok = f.deviceIndex(name)
	if ok {
		return i, true
	}
	return f.legacyIndex(name)
}

// deviceIndex reads the DEVICE's family: "Port12", "SFP1".
func (f Facade) deviceIndex(name string) (int, bool) {
	var n, ok = suffixNum(name, SFPPortPrefix)
	if ok {
		if n > f.sfp {
			return 0, false
		}
		return f.sfpFirst + n - 1, true
	}
	if n, ok = suffixNum(name, CopperPortPrefix); !ok {
		return 0, false
	}
	// The n-th port that is not a cage. Counted rather than computed: it is
	// the same rule as Name, taken the other way, and a duplicated formula is
	// a formula that will diverge.
	var seen int
	var i int
	for i = 0; i < f.ports; i++ {
		if f.isCage(i) {
			continue
		}
		seen++
		if seen == n {
			return i, true
		}
	}
	return 0, false
}

// legacyIndex reads meshcraft's NUMBERED family: "GE12", cages included.
func (f Facade) legacyIndex(name string) (int, bool) {
	var n, ok = suffixNum(name, f.legacy)
	if !ok || n > f.ports {
		return 0, false
	}
	return n - 1, true
}

// suffixNum reads the number following `prefix` in `name`. False as soon as
// the shape is not exactly "prefix + positive integer".
//
// The comparison is CASE-SENSITIVE, as it was before this batch: both
// families are generated by code — one by the device, the other by
// `port_groups` — and accepting "port3" would just as well accept "pOrT3",
// i.e. tolerate hand typing where the only legitimate source is a machine.
func suffixNum(name, prefix string) (int, bool) {
	if prefix == "" || !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	var num = name[len(prefix):]
	if num == "" {
		return 0, false
	}
	// DIGITS ONLY, and the check is explicit because `strconv.Atoi` is more
	// tolerant than it looks: it accepts a sign, so "Port+1" and "Port-1"
	// would return 1 and −1. The first would designate port 1 under a name
	// nothing generates; what matters is not that case but the rule, which is
	// to translate only what we recognise.
	var i int
	for i = 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return 0, false
		}
	}
	var n, err = strconv.Atoi(num)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// renamedPorts returns, as sentences for an operator, the ports whose name AS
// CARRIED BY THE DEVICE differs from meshcraft's faceplate name.
//
// Why say so: the faceplate name is a CONVENTION (cf. the box at the top of
// the file), so a port renamed "Uplink" in the switch's UI keeps being
// displayed as "Port5" in meshcraft. It is the right choice for the key, and
// it is exactly the gap between two screens this file exists to remove:
// keeping quiet about it would send someone looking for "Uplink" in a list
// that does not contain it.
//
// Empty when all names agree — which is the case of the reference device, and
// what the test on the real capture verifies.
//
// A PURE function, verifiable on testdata/link.b.
func renamedPorts(link *Page, f Facade) []string {
	var names, ok = link.Texts(FieldPortName)
	if !ok {
		// Field missing: nothing to compare, and above all nothing to flag. It
		// is not a fault — a firmware that does not serve `nm` simply leaves
		// the faceplate alone to do the naming, which it can.
		return nil
	}
	var out []string
	var i int
	for i = 0; i < f.ports && i < len(names); i++ {
		var got = strings.TrimSpace(names[i])
		if got == "" || got == f.Name(i) {
			continue
		}
		out = append(out, f.Name(i)+" is named \""+got+"\" on the device")
	}
	return out
}

// renamedNote assembles the state note, or returns the empty string if there
// is nothing to say.
func renamedNote(link *Page, f Facade) string {
	var diffs = renamedPorts(link, f)
	if len(diffs) == 0 {
		return ""
	}
	return "PORTS RENAMED ON THE DEVICE: " + joinComma(diffs) + ". meshcraft " +
		"keeps the faceplate names (\"" + CopperPortPrefix + "1\", \"" + SFPPortPrefix +
		"1\"), which are the ones SwOS uses by default: the \"" + FieldPortName +
		"\" field of /link.b is free text, without uniqueness, and making it a port's key " +
		"would lose an intent at the first rename. What is displayed therefore does " +
		"designate the physical socket; it is its local label that differs."
}

package gnmipoll

// LLDP over gNMI: a streamed neighbour table, folded back into the snapshot the
// controller already consumes.
//
// # WHY THIS IS NOT A SAMPLE PATH
//
// Everything else in this package produces [metricbuf.Sample] values. A neighbour
// is a table of STRINGS, and cmd/proxy/lldp.go's header already argued out why
// that cannot ride the metric buffer: encoding a system name into a series label
// creates one time series per cable, which VictoriaMetrics keeps for ever — long
// after the cable is unplugged. That argument does not change because the
// transport did. So this produces an [snmppoll.LLDPReport], travels by
// `POST /api/proxy/lldp`, and shares nothing with the sample path but the target.
//
// # WHY IT IMPORTS snmppoll, WHICH LOOKS BACKWARDS
//
// It borrows the report TYPE, not the poller. That type is already the neutral
// snapshot shape rather than an SNMP one — internal/api/lldp_cli.go builds one
// from a CLI read, with no SNMP involved — so a second, gNMI-shaped report would
// mean two wire formats for one controller endpoint, and a controller that had to
// know which collector a snapshot came from. The alternative, moving the type to a
// neutral package, is a rename across the API, the store and both collectors for
// no behavioural gain.
//
// # THE STATE PROBLEM, WHICH IS THE WHOLE DIFFICULTY
//
// An SNMP poll reads the entire table every cycle: the snapshot IS the read, and
// a neighbour that disappeared is simply absent from the next one. A subscription
// is the opposite — the device sends the initial state once, then only what
// changes — so the snapshot has to be ACCUMULATED here and re-sent on a timer.
//
// That inverts where a stale row comes from, and it is worth naming because the
// project already has a bug of exactly this shape in its topology handling:
//
//   - with a poll, a row goes stale when the poll stops. Nothing is re-sent, and
//     the controller's own ageing is what removes it;
//   - with a stream, a row goes stale when a DELETE is missed. The table here
//     would keep it for ever and keep re-pushing it, and the controller would keep
//     seeing a fresh snapshot asserting a cable that no longer exists.
//
// So deletes are honoured ([Table.Delete]) and the table is rebuilt from scratch
// whenever the subscription restarts ([Table.Reset]). A resubscribe replays the
// full initial state, which is the only moment the two views are known to agree,
// and throwing away what came before is what stops a reconnect from resurrecting
// a neighbour that went away while the stream was down.

import (
	"sort"
	"strings"
	"sync"

	"meshcraft/internal/snmppoll"
)

// Table accumulates streamed LLDP state for ONE device.
//
// Safe for concurrent use: the subscription goroutine writes it and the push
// timer reads it, and those are different goroutines by construction.
type Table struct {
	mu       sync.Mutex
	deviceID string
	// neighbors is keyed by local interface, then by the neighbour's `id` — the
	// two list keys of the OpenConfig path.
	//
	// TWO LEVELS AND NOT ONE, and the reason is a real topology: a port facing an
	// IP phone with a PC behind it, or any hub, sees several neighbours at once.
	// Keyed on the interface alone, the last update to arrive would overwrite the
	// rest and the link would read as whichever neighbour spoke most recently.
	neighbors map[string]map[string]*snmppoll.Neighbor
	// gone holds what a DELETED neighbour last carried, under the same two keys.
	//
	// See [Table.applyNeighbor]: it is read only when an update RE-CREATES a list
	// entry the device had deleted, which is what an ageout-then-relearn is, and
	// it is what stops a partial re-creation from producing a nameless neighbour.
	// It never puts a row back by itself.
	gone map[string]map[string]snmppoll.Neighbor
	// self: what the device says about ITSELF, from /lldp/state.
	chassisID string
	sysName   string
	// seen: at least one LLDP notification has arrived. See [Table.Report] — this
	// is what separates "LLDP runs and nobody is opposite" from "we know nothing",
	// which is the costliest lie this snapshot can tell.
	seen bool
}

// NewTable returns an empty table for a device.
func NewTable(deviceID string) *Table {
	return &Table{
		deviceID:  deviceID,
		neighbors: map[string]map[string]*snmppoll.Neighbor{},
		gone:      map[string]map[string]snmppoll.Neighbor{},
	}
}

// maxRemembered bounds [Table.gone].
//
// A BOUND AND NOT A POLICY. What is remembered is one struct per list entry the
// device has deleted, and on a switch whose neighbour `id` is stable that is a
// handful of rows for the life of the process. It is not stable everywhere: a
// target free to mint a fresh `id` on every relearn would grow this map for as
// long as the proxy runs, and this collector is meant to run for months. Past
// the bound the whole map is dropped rather than trimmed — there is no useful
// order to evict in, and losing it costs exactly what the bug below costs on one
// re-creation, whereas a proxy growing without limit costs the fleet.
const maxRemembered = 256

// Reset empties the table.
//
// Called when a subscription is (re)established, and that is not housekeeping.
// A resubscribe replays the device's full initial state, so it is the one moment
// the two views are known to agree; keeping the previous contents would let a
// neighbour that vanished while the stream was down survive the reconnect and go
// on being re-pushed as a live cable.
func (t *Table) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.neighbors = map[string]map[string]*snmppoll.Neighbor{}
	// The memory of departed neighbours goes with them. It exists to survive one
	// device's delete-then-recreate inside a LIVE subscription; carrying it across
	// a resubscribe would let it refill a row from a state the replayed initial
	// dump has just contradicted, which is the resurrection this Reset exists to
	// prevent.
	t.gone = map[string]map[string]snmppoll.Neighbor{}
	t.chassisID = ""
	t.sysName = ""
	t.seen = false
}

// Fold folds ONE notification into the table, deletes first and updates second.
// Returns true when something changed, so a caller can push on change rather
// than only on a timer.
//
// THE ORDER IS THE WHOLE POINT, and getting it backwards costs a live cable.
// gNMI lets a single Notification carry both a delete and an update for the SAME
// path, and says the update wins — deletes are processed first. That shape is
// not exotic: it is what a device sends when a list entry is re-created, which
// for `/lldp/interfaces/interface/neighbors/neighbor[id=…]` happens on every
// ageout-then-relearn.
//
// Applying updates first and deletes second inverts the rule: the neighbour is
// re-added by the update, removed again by the delete of the same message, and
// the device — having already published it — sends nothing further until
// something about it CHANGES. The residue is an interface key with an empty
// neighbour set, so [Table.Report] keeps listing the port in `PortIDs` while
// serving no row for it.
//
// A DRIFT OF THIS SHAPE WAS OBSERVED, 2026-08-11, on core01.lab (EOS
// 4.28.13.1M), and it is what sent somebody looking at this ordering — but read
// what it does and does not establish. For roughly thirty-five minutes the
// controller served three of that switch's four neighbours: Ethernet37 had no row
// in `lldp_neighbors` while `lldp_self.port_ids` still named it, which is exactly
// the residue above, and a FRESH subscription to the same switch listed the
// neighbour throughout. It outlasted some seventy push cycles and survived the
// device's own ageout and re-learn of that very neighbour by about ten minutes —
// then cleared on its own, by a means NOT identified (a resubscribe would do it:
// [Table.Reset] plus a replayed initial state). So the field evidence is
// corroboration, not a proof of causation, and the reason this order is wrong is
// the protocol's, not that one switch's.
//
// One method rather than two calls at the call site: the ordering is a property
// of the notification, not a choice a caller should be able to get wrong.
func (t *Table) Fold(n Notification) bool {
	var changed bool
	if len(n.Delete) > 0 {
		changed = t.Delete(n.Prefix, n.Delete)
	}
	if t.Apply(n) {
		changed = true
	}
	return changed
}

// Apply folds one notification's UPDATES into the table. Returns true when
// something changed, so a caller can push on change rather than only on a timer.
//
// Callers holding a whole notification want [Table.Fold], which also applies its
// deletes and does so in the order gNMI mandates.
func (t *Table) Apply(n Notification) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	var changed bool
	var u Update
	for _, u = range Expand(n.Update) {
		var full = Join(n.Prefix, u.Path)
		var names = full.Names()
		if len(names) == 0 || names[0] != "lldp" {
			continue
		}
		t.seen = true
		var iface = full.Key("interface", "name")
		var id = full.Key("neighbor", "id")
		if iface == "" || id == "" {
			// No list keys: this is the device's own /lldp/state.
			if t.applySelf(suffixAfter(names, "state"), u.Value) {
				changed = true
			}
			continue
		}
		if t.applyNeighbor(iface, id, suffixAfter(names, "state"), u.Value) {
			changed = true
		}
	}
	return changed
}

// applySelf records what the device says about itself. Called under t.mu.
//
// An EMPTY string is refused for the same reason it is refused on a neighbour
// (see [Table.applyNeighbor]): this identity is what `buildLLDPIdentity` indexes
// the whole fleet's chassis ids and system names on, and overwriting a known one
// with "" would unname the reporting device itself.
func (t *Table) applySelf(leaf string, v Value) bool {
	if v.Kind != ValueString || v.Str == "" {
		return false
	}
	switch leaf {
	case "chassis-id":
		if t.chassisID == v.Str {
			return false
		}
		t.chassisID = v.Str
		return true
	case "system-name":
		if t.sysName == v.Str {
			return false
		}
		t.sysName = v.Str
		return true
	}
	return false
}

// neighborLeaf returns the field of a neighbour that an OpenConfig leaf name
// names, and nil when this table does not record that leaf.
//
// The leaf names are OpenConfig's and they are NOT the MIB's, which is the one
// place a careless mapping would quietly mislabel a cable: `port-id` here is the
// remote port's identifier and `port-description` its human label, matching
// lldpRemPortId and lldpRemPortDesc — both are kept, because neither is served
// everywhere (measured on this fleet: a CRS326 serves no port description at all).
//
// It is a FUNCTION rather than a switch inlined in the caller so that the caller
// can ask whether a leaf is recorded BEFORE it touches the table — see
// [Table.applyNeighbor], where creating an entry for a leaf that turns out to be
// unrecorded is one of the ways a nameless neighbour was born.
func neighborLeaf(nb *snmppoll.Neighbor, leaf string) *string {
	switch leaf {
	case "chassis-id":
		return &nb.ChassisID
	case "port-id":
		return &nb.PortID
	case "port-description":
		return &nb.PortDesc
	case "system-name":
		return &nb.SysName
	case "system-description":
		return &nb.SysDesc
	}
	return nil
}

// applyNeighbor records one leaf of one neighbour. Called under t.mu.
//
// # THREE REFUSALS BEFORE THE ENTRY IS TOUCHED, and each one closed a way of
// producing a neighbour with NO IDENTITY
//
// A neighbour row whose `chassis-id` and `port-id` are both empty is not a
// smaller truth: chassis id and port id are LLDP's two MANDATORY TLVs, so no
// device ever observed one, and `internal/api`'s `identifyPeer` falls through
// chassis → port-id → system-name and comes out with nothing. The row then
// OCCUPIES the local port — `markRefutedObservations` bails on an occupant with
// no peer device and `refuteByFreshSilence` skips an occupied port — so one
// cable is drawn as two one-sided links and that endpoint can never be
// arbitrated again. It is worth three guards.
//
//  1. An EMPTY string is not an observation. A device says a neighbour is gone
//     with a delete, not by publishing "" over a mandatory TLV, and writing that
//     "" into a row that already carried a chassis id destroys the only thing
//     that names the peer.
//  2. A leaf this table does not record — `age`, `ttl`, `last-update`,
//     `chassis-id-type` — must not CREATE the entry. Those tick on their own,
//     which means that after a delete the very next housekeeping update used to
//     resurrect the neighbour as an empty row and hold the port for ever.
//  3. An entry the device DELETED and is now re-creating resumes from what it
//     last carried. See below.
//
// # WHY A RE-CREATION MUST RESUME AND NOT START FROM ZERO
//
// This is the ageout-then-relearn defect, reproduced 3/3 on core01.lab by
// toggling the far end's LLDP transmit. The device deletes the list entry when
// the neighbour ages out — correctly, and the table honours it — and re-creates
// it on the relearn. But the stream is ON_CHANGE, so the re-creation carries
// only the leaves the device considers to have CHANGED, and the identity of a
// neighbour that came back on the same port has not changed. What arrived was
// `port-description`, `system-name` and `system-description`; `chassis-id` and
// `port-id` never came, because the device had already published them and, from
// its side, nothing about them was new.
//
// Rebuilt from zero, that produced exactly the residue seen on the map: a row
// with a description and no identity, for the whole life of the subscription. A
// proxy restart cleared it — the replayed initial dump carries every leaf — and
// the next ageout brought it back, which is what makes a restart a workaround.
//
// Resuming cannot resurrect anything, and that is the property to check against
// [Table.Delete]'s reason for existing: nothing here ever puts a row back. The
// remembered state is consulted only when the DEVICE re-creates that same list
// entry, i.e. only when the device is itself asserting the neighbour exists; and
// every leaf the device does send overwrites what was remembered, so a cable
// that moved to a different neighbour re-states its own identity — both mandatory
// TLVs having genuinely changed — and overwrites it entirely.
func (t *Table) applyNeighbor(iface, id, leaf string, v Value) bool {
	if v.Kind != ValueString || v.Str == "" {
		return false
	}
	var probe snmppoll.Neighbor
	if neighborLeaf(&probe, leaf) == nil {
		return false
	}
	var byID = t.neighbors[iface]
	if byID == nil {
		byID = map[string]*snmppoll.Neighbor{}
		t.neighbors[iface] = byID
	}
	var created bool
	var nb = byID[id]
	if nb == nil {
		// LocalPort is the device's OWN name for the port, exactly as the SNMP
		// path reports it: mapping it onto the meshcraft faceplate name is the
		// controller's job (`facadeOf`), and doing it here would hide what the
		// match was made on.
		nb = &snmppoll.Neighbor{}
		var prev, known = t.recall(iface, id)
		if known {
			*nb = prev
		}
		nb.LocalPort = iface
		byID[id] = nb
		created = true
	}
	var dst = neighborLeaf(nb, leaf)
	if *dst == v.Str {
		// `created` and not false: a re-creation whose first leaf happens to match
		// what was remembered still put a row back into the report, and a caller
		// that pushes on change would otherwise not hear about it.
		return created
	}
	*dst = v.Str
	return true
}

// recall takes back what a deleted neighbour last carried, and says whether
// anything was remembered. Called under t.mu.
//
// It CONSUMES the entry: a re-creation is the one moment the memory is for, and
// keeping it afterwards would leave a value that could resurface much later,
// long after the row it came from stopped being evidence of anything.
func (t *Table) recall(iface, id string) (snmppoll.Neighbor, bool) {
	var byID = t.gone[iface]
	if byID == nil {
		return snmppoll.Neighbor{}, false
	}
	var prev, known = byID[id]
	if !known {
		return snmppoll.Neighbor{}, false
	}
	delete(byID, id)
	if len(byID) == 0 {
		delete(t.gone, iface)
	}
	return prev, true
}

// remember stores what a departing neighbour carried, for the re-creation that
// an ageout-then-relearn will send. Called under t.mu.
func (t *Table) remember(iface, id string, nb *snmppoll.Neighbor) {
	if nb == nil || (nb.ChassisID == "" && nb.PortID == "") {
		// Only a row that carried an IDENTITY is worth keeping: the whole purpose
		// is to stop a re-creation from losing the chassis id and the port id, and
		// remembering a row that had neither would cost memory to restore nothing.
		return
	}
	var count int
	var one string
	for one = range t.gone {
		count += len(t.gone[one])
	}
	if count >= maxRemembered {
		t.gone = map[string]map[string]snmppoll.Neighbor{}
	}
	var byID = t.gone[iface]
	if byID == nil {
		byID = map[string]snmppoll.Neighbor{}
		t.gone[iface] = byID
	}
	byID[id] = *nb
}

// Delete removes what a notification's delete paths named.
//
// THE HALF A STREAM HAS AND A POLL DOES NOT. An SNMP snapshot drops a vanished
// neighbour by simply not containing it; a subscription has to be told, and a
// collector that ignored deletes would re-push an unplugged cable for ever, with
// a fresh timestamp on it every cycle.
//
// A delete on the INTERFACE level removes every neighbour on it — that is what
// arrives when a port goes down — and a delete on a leaf removes the whole
// neighbour rather than blanking one field: a neighbour with its chassis-id
// deleted is not a neighbour with an empty chassis-id, it is a row on its way out.
func (t *Table) Delete(prefix Path, paths []Path) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	var changed bool
	var p Path
	for _, p = range paths {
		var full = Join(prefix, p)
		var iface = full.Key("interface", "name")
		if iface == "" {
			continue
		}
		var id = full.Key("neighbor", "id")
		if id == "" {
			if len(t.neighbors[iface]) > 0 {
				changed = true
			}
			var one string
			for one = range t.neighbors[iface] {
				t.remember(iface, one, t.neighbors[iface][one])
			}
			delete(t.neighbors, iface)
			continue
		}
		if t.neighbors[iface] != nil {
			var nb, ok = t.neighbors[iface][id]
			if ok {
				changed = true
				// What it carried is kept aside — see [Table.applyNeighbor]. The row
				// still leaves the report here and now; this is only so that the
				// device re-creating this same list entry, which is what a relearn
				// does, does not have to re-state an identity it considers unchanged.
				t.remember(iface, id, nb)
			}
			delete(t.neighbors[iface], id)
		}
	}
	return changed
}

// Report renders the current table as the snapshot the controller consumes.
//
// `Served` is the field to get right and the reason [Table.seen] exists. False
// and "zero neighbours" are NOT the same statement, and confusing them is the
// costliest lie this structure can tell: zero neighbours is an OBSERVATION —
// LLDP runs, nobody is opposite — whereas not served means we know nothing about
// this device's neighbours and no amount of waiting will change it. The screen
// must be able to say "unknown" rather than "none".
//
// Here, `seen` is true as soon as any /lldp notification has arrived, including
// one carrying an empty neighbour list. A subscription that produced nothing at
// all leaves it false, which is the honest answer for a switch whose gNMI serves
// no LLDP tree.
func (t *Table) Report() snmppoll.LLDPReport {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out = snmppoll.LLDPReport{
		DeviceID:  t.deviceID,
		ChassisID: t.chassisID,
		SysName:   t.sysName,
		Served:    t.seen,
		Neighbors: []snmppoll.Neighbor{},
	}
	var iface string
	for iface = range t.neighbors {
		var id string
		for id = range t.neighbors[iface] {
			out.Neighbors = append(out.Neighbors, *t.neighbors[iface][id])
		}
	}
	// SORTED, and for the controller's sake rather than for tidiness: it diffs
	// snapshots run against run, and Go randomises map iteration — unsorted, two
	// identical tables would compare as different, the neighbour rows would be
	// rewritten every cycle and the screens would blink. Same rule as the SNMP
	// report's, stated in its own comment.
	sort.Slice(out.Neighbors, func(i, j int) bool {
		if out.Neighbors[i].LocalPort != out.Neighbors[j].LocalPort {
			return out.Neighbors[i].LocalPort < out.Neighbors[j].LocalPort
		}
		if out.Neighbors[i].ChassisID != out.Neighbors[j].ChassisID {
			return out.Neighbors[i].ChassisID < out.Neighbors[j].ChassisID
		}
		return out.Neighbors[i].PortID < out.Neighbors[j].PortID
	})
	// PortIDs: the local port identifiers this device announces.
	//
	// It saves the fleet's only bilateral link (see the field's own comment in
	// snmppoll): a switch may announce one chassis-id globally while its frames
	// carry a per-port MAC, and without this list the controller cannot recognise
	// the neighbour that named it — a link seen from both ends reads as
	// unilateral. Over gNMI the per-interface `id` key is the nearest equivalent,
	// so it is what gets reported, deduplicated and sorted.
	var ids = map[string]bool{}
	for iface = range t.neighbors {
		if iface != "" {
			ids[iface] = true
		}
	}
	var id string
	for id = range ids {
		out.PortIDs = append(out.PortIDs, id)
	}
	sort.Strings(out.PortIDs)
	return out
}

// IsLLDP says whether a notification belongs to the LLDP tree.
//
// Cheap, and it exists so the proxy can route one subscription's notifications to
// the table instead of to the sample translator without either of them having to
// know about the other.
func IsLLDP(n Notification) bool {
	if len(n.Prefix) > 0 {
		return n.Prefix[0].Name == "lldp"
	}
	var u Update
	for _, u = range n.Update {
		if len(u.Path) > 0 && strings.EqualFold(u.Path[0].Name, "lldp") {
			return true
		}
	}
	return false
}

package gnmipoll

import (
	"strings"
	"testing"
)

// THE SUBSCRIBED PATH, PINNED — because the wire cannot be tested here and the
// constant is the only thing that can.
//
// On 2026-08-13, core01.lab (EOS 4.28.13.1M) served two of its four LLDP
// neighbours to a subscription on
// `/lldp/interfaces/interface[name=*]/neighbors/neighbor[id=*]/state` and all
// four to one on `/lldp/interfaces/interface[name=*]/neighbors`. Same device,
// same instant, fresh subscription each time, both sub-modes: the second
// wildcard is what loses rows. Two of the lab's inter-switch links were
// undrawable for days because of it — the far end saw us, we did not see it, and
// a topology cannot draw a cable from one end.
//
// A unit test cannot reproduce that: it needs the switch. What it CAN do is stop
// the path from drifting back, which is a live risk precisely because the longer
// spelling looks more correct — it names the leaf container the code actually
// reads, and every other path in this table goes all the way down to `/state`.
// Anyone tidying for consistency would restore the bug and nothing would fail.
func TestLLDPNeighbourPathStopsAtTheContainer(t *testing.T) {
	var got = pathLLDPNeighbor.String()
	const want = "/lldp/interfaces/interface[name=Ethernet34]/neighbors"

	// Compared with the wildcard filled in, because Path.String() renders keys and
	// a literal "*" comparison would be a test of the renderer, not of the path.
	var normalized = strings.ReplaceAll(got, "[name=*]", "[name=Ethernet34]")
	if normalized != want {
		t.Errorf("pathLLDPNeighbor = %q, want the neighbours container %q", got, want)
	}
	if strings.Contains(got, "neighbor[") {
		t.Error("the path descends into neighbor[id=*] again: EOS 4.28.13.1M drops " +
			"rows when a second wildcard is asked for, and the neighbours it drops " +
			"are links the topology then cannot draw")
	}
}

// The container path carries leaves the old one did not, and one of them —
// `neighbor[id=N]/id`, outside `/state` — must not create a neighbour.
//
// It is the guard [Table.applyNeighbor] already documents (an unrecorded leaf
// must never CREATE an entry, or housekeeping updates resurrect deleted rows as
// nameless ones holding the port for ever), reached here by a NEW route: before
// this path change no leaf outside `/state` was ever subscribed, so this branch
// was only ever exercised by leaves like `age` and `ttl` that live inside it.
func TestTheBareIDLeafDoesNotCreateANeighbour(t *testing.T) {
	var tbl = NewTable("dev")
	const base = "/lldp/interfaces/interface[name=Ethernet48]/neighbors/neighbor[id=3]"

	var changed = tbl.Apply(Notification{Update: []Update{
		{Path: mustPath(base + "/id"), Value: StringValue("3")},
	}})
	if changed {
		t.Error("the bare /id leaf reported a change; it identifies the row, it is " +
			"not a fact about the neighbour")
	}
	var n = len(tbl.Report().Neighbors)
	if n != 0 {
		t.Errorf("table holds %d neighbours after only /id arrived, want 0 — a row "+
			"with no chassis id and no port id names nobody and occupies the port", n)
	}

	// And the real leaves that follow it in the same subtree do build the row, so
	// the refusal above costs nothing.
	tbl.Apply(Notification{Update: []Update{
		{Path: mustPath(base + "/state/chassis-id"), Value: StringValue("64:9d:99:16:34:10")},
		{Path: mustPath(base + "/state/port-id"), Value: StringValue("Eth 1/26")},
	}})
	var got = tbl.Report().Neighbors
	if len(got) != 1 {
		t.Fatalf("neighbours = %d, want 1", len(got))
	}
	if got[0].LocalPort != "Ethernet48" || got[0].ChassisID != "64:9d:99:16:34:10" {
		t.Errorf("got %+v, want the s3900lab end of the ET48 cable on Ethernet48", got[0])
	}
	// An EMPTY system name is what s3900lab actually publishes, which is why the
	// chassis id above is the only thing that can identify it: a match on system
	// name would leave this cable one-sided for ever.
	if got[0].SysName != "" {
		t.Errorf("SysName = %q, want empty as the device serves it", got[0].SysName)
	}
}

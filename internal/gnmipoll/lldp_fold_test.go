package gnmipoll

import "testing"

// neighborNotification builds the message a device sends for one neighbour,
// optionally preceded by the delete of that same entry — the shape a list
// re-creation takes on the wire.
func neighborNotification(iface, id, chassis, port string, withDelete bool) Notification {
	var base = "/lldp/interfaces/interface[name=" + iface + "]/neighbors/neighbor[id=" + id + "]"
	var n = Notification{
		Update: []Update{
			{Path: mustPath(base + "/state/chassis-id"), Value: StringValue(chassis)},
			{Path: mustPath(base + "/state/port-id"), Value: StringValue(port)},
		},
	}
	if withDelete {
		n.Delete = []Path{mustPath(base)}
	}
	return n
}

// THE ORDER, and it is worth a test of its own because getting it backwards does
// not fail loudly — it deletes a cable that is plugged in and never puts it back.
//
// gNMI allows one Notification to carry a delete and an update for the SAME path,
// and the update wins: deletes are processed first. A device sends exactly that
// when it re-creates a list entry, which for an LLDP neighbour happens on every
// ageout-then-relearn. Applied in the wrong order the neighbour disappears, and
// it stays gone until something about it changes — the device has already
// published it.
//
// The protocol is the reason this test exists. A drift of the matching shape was
// seen on core01.lab (EOS 4.28.13.1M) on 2026-08-11 — three of four
// neighbours served for some thirty-five minutes, the fourth listed by a fresh
// subscription throughout — but it eventually cleared by itself, so it
// corroborates the ordering rule rather than proving it caused that outage. See
// [Table.Fold].
func TestFoldAppliesDeletesBeforeUpdates(t *testing.T) {
	var tbl = NewTable("dev")
	if !tbl.Fold(neighborNotification("Ethernet37", "1", "9c:05:d6:f5:f1:37", "0/18", false)) {
		t.Fatal("the first notification changed nothing")
	}

	// The re-creation: the same entry deleted and re-published in one message.
	tbl.Fold(neighborNotification("Ethernet37", "1", "9c:05:d6:f5:f1:37", "0/18", true))

	var report = tbl.Report()
	if len(report.Neighbors) != 1 {
		t.Fatalf("%d neighbours after a delete+update of the same entry, want 1: the "+
			"delete was applied AFTER the update and removed a neighbour the device "+
			"had just re-published", len(report.Neighbors))
	}
	if report.Neighbors[0].ChassisID != "9c:05:d6:f5:f1:37" {
		t.Errorf("chassis = %q, want the re-published one", report.Neighbors[0].ChassisID)
	}
}

// A delete that arrives ALONE still removes the neighbour: the ordering fix must
// not turn a departure into a permanent row, which is the opposite mistake and
// the one the delete handling exists to prevent.
func TestFoldStillHonoursALoneDelete(t *testing.T) {
	var tbl = NewTable("dev")
	tbl.Fold(neighborNotification("Ethernet37", "1", "9c:05:d6:f5:f1:37", "0/18", false))

	var gone = Notification{Delete: []Path{
		mustPath("/lldp/interfaces/interface[name=Ethernet37]/neighbors/neighbor[id=1]"),
	}}
	if !tbl.Fold(gone) {
		t.Fatal("a delete of a known neighbour reported no change")
	}
	if len(tbl.Report().Neighbors) != 0 {
		t.Error("the neighbour survived its own delete: an unplugged cable would be " +
			"re-pushed for ever")
	}
}

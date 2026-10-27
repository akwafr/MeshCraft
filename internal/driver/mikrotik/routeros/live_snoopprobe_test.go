package routeros

// READ-ONLY reconnaissance before any DHCP-snooping write on RouterOS.
//
// # Why this exists BEFORE a write harness, and not beside one
//
// dhcpsnoop_apply.go is written, unit-tested against the reference CRS326
// captures, and has never sent a `set`. The blocker is NOT the channel —
// ApplyStormControl issued real writes to 192.0.2.54 over this same REST client
// and this same SOCKS route on 2026-08-09 (live_stormcontrol_test.go). The
// blocker is the BLAST RADIUS of the enable.
//
// `dhcp-snooping` is a BRIDGE property. A storm limiter could be exercised on
// ether2 because a limiter on a dark port polices nothing; there is no dark-port
// version of this one. The instant `dhcp-snooping=yes` lands, every untrusted
// port on that bridge starts dropping DHCP OFFER and ACK — the lab unit's other
// twenty-five included — and this platform is driver.CommitImmediate, so no
// reboot undoes it.
//
// cisco/ios met exactly this and solved it by gathering evidence first
// (cisco/ios/live_snoopprobe_test.go), then writing a plan whose trusted port
// was the real uplink. This is that pass for RouterOS. NOTHING HERE WRITES: it
// is `List` calls only, on menus this connector already reads elsewhere, so it
// is replayable on a production fleet.
//
// # The four questions a write plan cannot be built without
//
//  1. WHICH BRIDGE. Snooping is per bridge, and pickBridge chooses the filtering
//     one. A device with two bridges can have the ports that matter on the other.
//  2. WHICH PORTS ARE CABLED. `running` is carrier. Every cabled untrusted port
//     is a port whose clients stop getting leases the moment the feature is on.
//  3. WHERE DHCP COMES FROM. Three cases, and they need three different plans:
//     the device is a DHCP CLIENT (its own management address is a lease — it
//     can lose its own address at the next renewal); the device RUNS a DHCP
//     server (the source is local, and the port to trust may be none at all);
//     or the leases arrive through an uplink, which is the port to trust and
//     the thing `/ip/neighbor` helps identify.
//  4. WHAT IS ALREADY ON. A bridge already snooping, or a port already trusted,
//     is somebody's configuration, and a revert must restore it rather than
//     "undo" it — the lesson the storm-control exercise next door paid for.
//
//	ROS_LIVE_HOST=192.0.2.54 ROS_LIVE_USER=… ROS_LIVE_PASS=… \
//	ROS_LIVE_SOCKS=127.0.0.1:1080 ROS_LIVE_SOCKS_USER=… ROS_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/mikrotik/routeros -run TestLiveProbeSnooping -v
//
// Gated on ROS_LIVE_HOST alone (via liveClient), deliberately: a reconnaissance
// pass that needs a second opt-in flag is one nobody runs before planning the
// write, which is the whole failure this file is here to prevent.

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
)

// snoopProbeMenus are the read-only menus the pass walks, each with the reason
// it is on the list. A menu that errors is REPORTED AND SURVIVED rather than
// fatal: `/ip/dhcp-server` is absent on a switch whose RouterOS build has no
// dhcp package, and that absence is itself an answer to question 3.
var snoopProbeMenus = []struct {
	menu, why string
	fields    []string
}{
	{"interface/bridge", "which bridge filters, and is snooping already on",
		[]string{"name", "vlan-filtering", "dhcp-snooping", "protocol-mode"}},
	{"interface/bridge/port", "who is a member, and who is already trusted",
		[]string{"interface", "bridge", "trusted", "pvid", "frame-types"}},
	{"interface/ethernet", "which jacks are cabled (`running` is carrier)",
		[]string{"name", "running", "disabled", "default-name"}},
	{"ip/dhcp-client", "IS THIS DEVICE'S OWN ADDRESS A LEASE — the one that " +
		"turns a bad plan into a lost switch",
		[]string{"interface", "status", "address", "gateway"}},
	{"ip/dhcp-server", "does the DHCP server live HERE rather than upstream",
		[]string{"name", "interface", "disabled"}},
	{"ip/address", "the static addressing, and which interface carries management",
		[]string{"address", "interface", "dynamic"}},
	{"ip/route", "the default route: which way is upstream",
		[]string{"dst-address", "gateway", "active", "dynamic"}},
	{"interface/bridge/vlan", "the bridge VLAN table, for what the trusted port carries",
		[]string{"vlan-ids", "tagged", "untagged", "bridge"}},
	{"ip/neighbor", "LLDP/MNDP: what is on the far end of each cabled port",
		[]string{"interface", "identity", "address", "board", "platform"}},
}

// rowLine renders the named fields of a row, skipping the ones the device did
// not serve. Named fields rather than the whole row: `/ip/neighbor` alone
// carries two dozen keys and a full dump buries the four that matter.
func rowLine(r Row, fields []string) string {
	var parts []string
	var f string
	for _, f = range fields {
		var v = r.Get(f)
		if v == "" {
			continue
		}
		parts = append(parts, f+"="+v)
	}
	if len(parts) == 0 {
		return "(none of the expected fields)"
	}
	return strings.Join(parts, " ")
}

// TestLiveProbeSnooping gathers the evidence, and JUDGES none of it into a
// write. Reading the log is the deliverable.
func TestLiveProbeSnooping(t *testing.T) {
	var c = liveClient(t) // skips unless ROS_LIVE_HOST is set
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var m struct {
		menu, why string
		fields    []string
	}
	for _, m = range snoopProbeMenus {
		var rows, err = c.List(ctx, m.menu)
		if err != nil {
			// NOT a t.Errorf. An absent menu is an observation, and failing the
			// whole pass on it would throw away the eight that answered.
			t.Logf("===== /%s — %s =====\n  UNAVAILABLE: %v", m.menu, m.why, err)
			continue
		}
		t.Logf("===== /%s — %s ===== (%d row(s))", m.menu, m.why, len(rows))
		var r Row
		for _, r = range rows {
			t.Logf("  %s", rowLine(r, m.fields))
		}
	}

	// --- The connector's own reading of the same device ---
	//
	// Logged beside the raw menus on purpose: a plan is built from what
	// ReadDHCPSnooping reports, so a disagreement between the rows above and the
	// folded state is a connector bug that must surface HERE, before a write, and
	// not be discovered from a device that has already stopped forwarding DHCP.
	var st, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Fatalf("ReadDHCPSnooping: %v", err)
	}
	t.Logf("===== ReadDHCPSnooping =====")
	t.Logf("  enabled=%v  VLANs=%v  ports=%d", st.Enabled, st.VLANs, len(st.Ports))
	var trusted, untrusted []string
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if p.Trusted {
			trusted = append(trusted, p.Port)
			continue
		}
		untrusted = append(untrusted, p.Port)
	}
	sort.Strings(trusted)
	t.Logf("  TRUSTED (%d): %v", len(trusted), trusted)
	t.Logf("  untrusted (%d): %v", len(untrusted), untrusted)
	var n string
	for _, n = range st.Notes {
		t.Logf("  note: %s", n)
	}

	// --- The blast radius, computed rather than left to be eyeballed ---
	//
	// The number that decides whether the write is safe to plan is "how many
	// CABLED ports would start dropping DHCP replies". Ports and carrier live in
	// two different menus, so nobody reads it off the log by accident; it is
	// worth one join.
	var running = map[string]bool{}
	var eth, eerr = c.List(ctx, "interface/ethernet")
	if eerr == nil {
		var r Row
		for _, r = range eth {
			running[r.Get("name")] = r.Bool("running")
		}
	}
	var exposed []string
	for _, p = range st.Ports {
		if !p.Trusted && running[p.Port] {
			exposed = append(exposed, p.Port)
		}
	}
	sort.Strings(exposed)
	t.Logf("===== BLAST RADIUS OF `dhcp-snooping=yes` ON THIS BRIDGE =====")
	t.Logf("  %d CABLED and UNTRUSTED port(s): %v", len(exposed), exposed)
	t.Logf("  every one of them would drop DHCP OFFER/ACK the instant the feature "+
		"lands, and this platform persists immediately: %s", strings.Join(exposed, " "))

	// The device's own lease is called out separately because it is the failure
	// with no way back — losing management on a switch reached over SOCKS.
	var clients, cerr = c.List(ctx, "ip/dhcp-client")
	if cerr == nil && len(clients) > 0 {
		var r Row
		for _, r = range clients {
			t.Logf("  THIS DEVICE IS A DHCP CLIENT on %q (status=%q address=%q): if its "+
				"lease crosses the snooped bridge and no port toward the server is "+
				"trusted, it loses its own management address at the next renewal",
				r.Get("interface"), r.Get("status"), r.Get("address"))
		}
	}
}

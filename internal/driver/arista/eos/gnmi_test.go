package eos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The exact block. These strings are what a switch would be asked to accept in
// the middle of a provisioning run, so they are pinned rather than pattern
// matched: a submode line that drifts by one word does not fail here, it fails
// on a console with the credentials still unwritten.
func TestGNMIEnableCmdsForm(t *testing.T) {
	var got = GNMIEnableCmds(GNMIOpts{})
	var want = []string{
		"management api gnmi",
		"transport grpc default",
		"port 6030",
		"exit",
		"exit",
	}
	if len(got) != len(want) {
		t.Fatalf("GNMIEnableCmds() = %v, want %v", got, want)
	}
	var i int
	for i = range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// One exit per submode, and the count is the whole of the test. `management api
// gnmi` opens a submode and `transport grpc default` opens a second one inside
// it; a block that exits once leaves the session in the outer submode, and the
// next line of the enclosing push — the username line — is then read as a
// transport keyword and refused.
func TestGNMIEnableCmdsClosesBothSubmodes(t *testing.T) {
	var cmds = GNMIEnableCmds(GNMIOpts{Port: 57400, VRF: "MGMT"})
	var exits int
	var c string
	for _, c = range cmds {
		if c == "exit" {
			exits++
		}
	}
	if exits != 2 {
		t.Errorf("%d exit lines in %v, want 2 — one per submode opened", exits, cmds)
	}
	if idx(cmds, "port 57400") < 0 {
		t.Errorf("the requested port is not in the block: %v", cmds)
	}
	if idx(cmds, "vrf MGMT") < 0 {
		t.Errorf("the requested VRF is not in the block: %v", cmds)
	}
	// The VRF is a command of the INNER submode: written after the transport
	// line, before the exits. Placed anywhere else it is refused, or worse,
	// accepted by a different submode.
	if idx(cmds, "vrf MGMT") < idx(cmds, "transport grpc default") {
		t.Error("the vrf line comes before the transport it belongs to")
	}
	if idx(cmds, "vrf MGMT") > idx(cmds, "exit") {
		t.Error("the vrf line is written after the submode was closed")
	}
}

// An out-of-range port falls back to the documented default rather than writing
// "port 0" — a line the device refuses, in the middle of the push.
func TestGNMIEnableCmdsDefaultsThePort(t *testing.T) {
	var c GNMIOpts
	for _, c = range []GNMIOpts{{}, {Port: 0}, {Port: -1}, {Port: 70000}} {
		var cmds = GNMIEnableCmds(c)
		if idx(cmds, "port 6030") < 0 {
			t.Errorf("GNMIEnableCmds(%+v) did not fall back to the default port: %v", c, cmds)
		}
	}
}

// THE PROVISIONING PUSH WRITES NOTHING gNMI. This is the test that keeps the
// plaintext transport off every switch this product provisions: pushFixture is
// the fixture every other ordering test uses, and it leaves MgmtOpts.GNMI unset,
// exactly as the provisioning path does.
func TestMgmtPushWritesNoGNMIByDefault(t *testing.T) {
	var cmds = pushFixture()
	if idxPrefix(cmds, "management api gnmi") >= 0 {
		t.Fatalf("the default push enables gNMI: the transport is PLAINTEXT and would carry "+
			"the network-admin password this same block writes: %v", cmds)
	}
	if idxPrefix(cmds, "transport grpc") >= 0 {
		t.Errorf("a gRPC transport was configured without the caller asking: %v", cmds)
	}
}

// When a caller does ask, the block lands with the other management-plane
// service and before the credentials — the same ordering rule as everything else
// in this push, for the same reason: a console session that drops at the
// password change must leave the rest already in place.
func TestMgmtPushPlacesGNMIAfterSSHAndBeforeCredentials(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.5", PrefixLen: 16, Gateway: "192.0.2.1",
		Username: "mcadmin", Password: "s3cret", GNMI: true,
	})
	var ssh = idx(cmds, "management ssh")
	var gnmi = idx(cmds, "management api gnmi")
	var user = idxPrefix(cmds, "username mcadmin")
	if ssh < 0 || gnmi < 0 || user < 0 {
		t.Fatalf("one of the three blocks is missing: ssh=%d gnmi=%d user=%d in %v",
			ssh, gnmi, user, cmds)
	}
	if gnmi < ssh {
		t.Error("the gNMI block comes before \"management ssh\": the two management " +
			"services belong together, in that order")
	}
	if gnmi > user {
		t.Error("the gNMI block comes after the credentials: a session dropping at the " +
			"password change would leave it unwritten")
	}
}

// The honesty rule, applied to the READ side — and now to the OTHER direction.
//
// It used to assert that this connector declared NO gnmi channel, because nothing
// in the build subscribed. The transport exists now (internal/gnmipoll, gnmic
// over gRPC), the proxy subscribes, and this platform is carried on gNMI
// entirely. So what needs guarding has inverted, and there are two halves to it:
//
//   - `Streaming` and the channel list are ONE FACT. A summary that disagreed
//     with the list would put "streaming" in front of an operator while
//     BestOfKind(KindGNMI, …) found nothing to collect;
//   - NO SNMP CHANNEL MAY COME BACK. This is the half worth a test rather than a
//     comment: the two collectors are exclusive per device, and the controller
//     decides which one carries a platform by asking whether it declares a gNMI
//     channel (`carriedOnGNMI`). A connector declaring both would be read as
//     streaming, its SNMP channels silently ignored — an operator would see a
//     declared SNMP cadence that nothing anywhere honours.
func TestMonitoringIsCarriedByStreamingTelemetry(t *testing.T) {
	var mon = monitoring()
	if !mon.Streaming {
		t.Error("Streaming is false while gnmi channels are declared — the summary and " +
			"the channel list are one fact and move together, cf. " +
			"driver.MonitoringCapabilities.Streaming")
	}
	var seen = map[string]bool{}
	var ch driver.MonitorChannel
	for _, ch = range mon.Channels {
		if ch.Kind == driver.KindSNMP {
			t.Errorf("an %q channel is declared for %v: this platform is carried on gNMI, "+
				"and a connector declaring both is read as streaming — the SNMP cadence "+
				"would be shown and never honoured", driver.KindSNMP, ch.Metrics)
		}
		if ch.Kind != driver.KindGNMI {
			continue
		}
		var m string
		for _, m = range ch.Metrics {
			seen[m] = true
		}
	}
	// The four sets the migration promised. `if_state` in particular: the original
	// design refused to move it, because port transitions were derived from the
	// SNMP ifindex — that objection was removed by teaching the proxy's detector
	// to key on the port NAME, and if this declaration ever drops back to
	// `if_counters` alone the fleet loses on-change port state without a word.
	var want string
	for _, want = range []string{"if_state", "if_counters", "lldp", "health"} {
		if !seen[want] {
			t.Errorf("no gnmi channel declares %q", want)
		}
	}
}

// TRAPS ARE NOT DECLARED, and that must not drift back.
//
// A trap's only job in this product is to ADVANCE a poll that would have happened
// anyway (tunnel.EventPortUp) — its content is never a state source, because a
// v2c trap is unauthenticated UDP anyone on the path can forge. There is no
// `if_state` poll on this platform any more, so a trap has nothing to advance,
// and declaring Traps would make the proxy hold 162/udp open on the management
// network to receive datagrams that can no longer have any effect.
func TestTrapsAreNotDeclaredOnAStreamingPlatform(t *testing.T) {
	if monitoring().Traps {
		t.Error("Traps is declared: the proxy would open 162/udp for a platform whose " +
			"port state already arrives on-change over an authenticated subscription")
	}
}

// The device-side block is written down BECAUSE it is unverified, and the file
// must say so where an operator reading a diff would see it. This test guards
// the three deductions named in gnmi.go's header: they are the ones a capture
// would settle, and a future edit that quietly drops them would leave a form
// that reads as established fact.
func TestGNMIFileDocumentsWhatIsDeduced(t *testing.T) {
	// The pre-4.23 enable form is NOT written, deliberately.
	var cmds = GNMIEnableCmds(GNMIOpts{})
	var joined = strings.Join(cmds, "\n")
	if strings.Contains(joined, "no shutdown") {
		t.Error("\"no shutdown\" is written under the gnmi submode: that is the pre-4.23 " +
			"enable form, and an unexpected submode line fails a provisioning run mid-block")
	}
	// Neither is the AAA authorization line: it changes how EVERY administrative
	// session on the box is authorized, including the SSH session meshcraft
	// adopts the device with.
	if strings.Contains(joined, "aaa authorization") {
		t.Error("the gNMI block rewrites the switch's authorization model; a telemetry " +
			"feature must not do that as a side effect")
	}
}

package instanton

// LIVE LLDP TRANSMIT SWITCH — the one lever this framework offers on LLDP, and
// the reason it exists is not the 1930 at all.
//
// `staleWitnessMargin`'s holdtime term was measured on every fleet platform but
// one: RouterOS, whose ONLY LLDP neighbour is this 1930. Measuring a RouterOS
// ageout therefore means silencing the 1930's transmitter on the port facing it
// — there is no other speaker to switch off. That is what this harness does, and
// nothing else: `portState` selects tx / rx / both / off per interface and
// touches NO forwarding state, so a link carrying four VLANs keeps carrying them
// while its LLDP advertisement stops.
//
// THE SHAPE IS THE DEVICE'S OWN. The entry is READ first and every field is
// echoed back exactly as it was served, with `portState` alone replaced — the
// discipline dhcpsnoop.go arrived at the hard way on this firmware, where a POST
// naming a field the UI invented answers `statusCode 0` and changes nothing.
//
// DELIBERATELY NOT SAVED. This range is `apply_then_save` and a power cycle is
// its only rollback, so leaving the change in the running configuration means an
// interrupted run is repaired by the switch itself. Set it back when done.
//
//	INSTANTON_LIVE_HOST=198.51.100.101 INSTANTON_LIVE_USER=… INSTANTON_LIVE_PASS=… \
//	INSTANTON_LIVE_SOCKS=127.0.0.1:1080 INSTANTON_LIVE_SOCKS_USER=… INSTANTON_LIVE_SOCKS_PASS=… \
//	INSTANTON_LLDP_IFACE=50 INSTANTON_LLDP_STATE=2 \
//	go test ./internal/driver/instanton -run TestLiveSetLLDPPortState -v
//
// portState: 1 = tx only, 2 = rx only, 3 = both, 4 = off.

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// reLLDPEntry splits the served list into its per-interface entries.
var reLLDPEntry = regexp.MustCompile(`(?s)<InterfaceEntry>.*?</InterfaceEntry>`)

// lldpEntryFor returns the entry block whose interfaceName is exactly `iface`.
func lldpEntryFor(body, iface string) string {
	var blocks = reLLDPEntry.FindAllString(body, -1)
	var b string
	for _, b = range blocks {
		if xmlField(b, "interfaceName") == iface {
			return b
		}
	}
	return ""
}

func TestLiveSetLLDPPortState(t *testing.T) {
	if os.Getenv("INSTANTON_LIVE_HOST") == "" {
		t.Skip("INSTANTON_LIVE_HOST not set")
	}
	var iface = os.Getenv("INSTANTON_LLDP_IFACE")
	var want = os.Getenv("INSTANTON_LLDP_STATE")
	if iface == "" || want == "" {
		t.Skip("INSTANTON_LLDP_IFACE / INSTANTON_LLDP_STATE not set")
	}

	var ctx, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var a = liveSnoopAdmin(ctx, t, "LLDP transmit switch (RouterOS ageout measurement)")

	var body, err = a.RawSection(ctx, "LLDPInterfaceList")
	if err != nil {
		t.Fatalf("read LLDPInterfaceList: %v", err)
	}
	var entry = lldpEntryFor(body, iface)
	if entry == "" {
		t.Fatalf("no InterfaceEntry carries interfaceName %q", iface)
	}
	var before = xmlField(entry, "portState")
	t.Logf("BEFORE %s: portState=%s (entry: %s)", iface, before,
		strings.Join(strings.Fields(entry), ""))

	// Echo the device's own values back, portState alone replaced.
	var inner = "<InterfaceEntry>" +
		"<interfaceName>" + iface + "</interfaceName>" +
		"<interfaceType>" + xmlField(entry, "interfaceType") + "</interfaceType>" +
		"<interfaceID>" + xmlField(entry, "interfaceID") + "</interfaceID>" +
		"<portState>" + want + "</portState>" +
		"<notifEnbl>" + xmlField(entry, "notifEnbl") + "</notifEnbl>" +
		"<optionalTLVs>" + xmlField(entry, "optionalTLVs") + "</optionalTLVs>" +
		"<mngAddrState>" + xmlField(entry, "mngAddrState") + "</mngAddrState>" +
		"</InterfaceEntry>"

	var sentAt = time.Now().UTC()
	err = a.writeSection(ctx, "LLDPInterfaceList", "set", inner,
		"LLDP portState "+want+" on interface "+iface)
	if err != nil {
		t.Fatalf("write LLDPInterfaceList: %v", err)
	}
	t.Logf("SENT at %s", sentAt.Format(time.RFC3339Nano))

	// THE RE-READ IS THE ONLY PROOF: `statusCode 0` means understood, never
	// changed (cf. writeSection and ApplyPorts).
	body, err = a.RawSection(ctx, "LLDPInterfaceList")
	if err != nil {
		t.Fatalf("re-read LLDPInterfaceList: %v", err)
	}
	entry = lldpEntryFor(body, iface)
	var got = xmlField(entry, "portState")
	t.Logf("AFTER  %s: portState=%s", iface, got)
	if got != want {
		t.Fatalf("portState re-reads %q, wanted %q — the write did not take", got, want)
	}

	var s Step
	for _, s = range a.Steps {
		t.Logf("step ok=%v %s — %s", s.OK, s.What, s.Detail)
	}
}

package edgeswitch

// A LIVE read-only exercise against a real EdgeSwitch. Skipped unless
// ES_LIVE_HOST is set, so `make test` never depends on hardware.
//
//	ES_LIVE_HOST=192.0.2.4 ES_LIVE_USER=admin ES_LIVE_PASS=… \
//	ES_LIVE_SOCKS=127.0.0.1:1080 ES_LIVE_SOCKS_USER=… ES_LIVE_SOCKS_PASS=… \
//	go test ./internal/driver/ubiquiti/edgeswitch -run TestLive -v

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"meshcraft/internal/driver"
)

type socksDialer struct{ d proxy.Dialer }

func (s socksDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var cd, ok = s.d.(proxy.ContextDialer)
	if ok {
		return cd.DialContext(ctx, network, addr)
	}
	return s.d.Dial(network, addr)
}

func TestLiveReadEverything(t *testing.T) {
	var host = os.Getenv("ES_LIVE_HOST")
	if host == "" {
		t.Skip("ES_LIVE_HOST not set")
	}
	var tgt = driver.Target{Host: host}
	var sa = os.Getenv("ES_LIVE_SOCKS")
	if sa != "" {
		var auth *proxy.Auth
		var u = os.Getenv("ES_LIVE_SOCKS_USER")
		if u != "" {
			auth = &proxy.Auth{User: u, Password: os.Getenv("ES_LIVE_SOCKS_PASS")}
		}
		var d, err = proxy.SOCKS5("tcp", sa, auth, proxy.Direct)
		if err != nil {
			t.Fatalf("socks: %v", err)
		}
		tgt.Dialer = socksDialer{d: d}
	}
	var ctx, cancel = context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var drv = &Driver{}
	var adm, err = drv.Authenticate(ctx, tgt, []Candidate{{
		Username: os.Getenv("ES_LIVE_USER"), Secret: os.Getenv("ES_LIVE_PASS"),
		Why: "live exercise",
	}})
	var c, _ = adm.(*Client)
	if err != nil {
		t.Fatalf("authenticate: %v — transcript %+v", err, adm.Transcript())
	}
	defer c.Close()
	if !c.privileged {
		t.Fatalf("never reached privileged mode — transcript %+v", c.Transcript())
	}
	t.Logf("identity: model=%q type=%q fw=%q serial=%q mac=%q",
		c.facts.Model, c.facts.Type, c.facts.Firmware, c.facts.Serial, c.facts.MAC)

	var st State
	st, err = c.ReadState(ctx)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	t.Logf("state: %d VLANs, %d ports, %d links, mgmt vlan %d",
		len(st.VLANs), len(st.Ports), len(st.Links), st.MgmtVLAN)
	var v driver.VLAN
	for _, v = range st.VLANs {
		t.Logf("  vlan %d %q", v.ID, v.Name)
	}
	if len(st.Links) == 0 {
		t.Error("no link state read — every port would show as unknown")
	}
	var up int
	var n string
	for n = range st.Links {
		if st.Links[n] == driver.LinkUpState {
			up++
			t.Logf("  up: %s", n)
		}
	}
	if up == 0 {
		t.Error("no port up, yet this session came in over one")
	}
	// LAG interfaces (slot 3) must never appear as faceplate ports.
	var _, bad = st.Links["GE0"]
	if bad {
		t.Error("a slot-3 LAG row leaked into the faceplate ports")
	}

	var addr driver.MgmtAddrState
	addr, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		t.Errorf("ReadMgmtAddressing: %v", err)
	} else {
		t.Logf("mgmt: mode=%q address=%q gw=%q iface=%q",
			addr.Mode, addr.Address, addr.Gateway, addr.Interface)
		if addr.Address == "" {
			t.Error("no management address read on a device reached BY that address")
		}
	}

	var stp driver.SpanningTreeState
	stp, err = c.ReadSpanningTree(ctx)
	if err != nil {
		t.Errorf("ReadSpanningTree: %v", err)
	} else {
		t.Logf("stp: enabled=%v mode=%q bridge=%s prio=%d root=%s rootprio=%d cost=%d isroot=%v region=%+v ports=%+v notes=%v",
			stp.Enabled, stp.Mode, stp.BridgeID, stp.BridgePriority,
			stp.RootBridgeID, stp.RootPriority, stp.RootPathCost, stp.IsRoot,
			stp.Region, stp.Ports, stp.Notes)
		if stp.Enabled && stp.BridgeID == "" {
			t.Error("spanning tree is on yet no bridge identifier was read — " +
				"the \"show spanning-tree\" half of the read regressed")
		}
	}

	var ns []LLDPNeighbor
	ns, err = c.ReadLLDPNeighbors(ctx)
	if err != nil {
		t.Errorf("ReadLLDPNeighbors: %v", err)
	}
	t.Logf("lldp: %d neighbour(s)", len(ns))
	var nb LLDPNeighbor
	for _, nb = range ns {
		t.Logf("  %s ← %q chassis %s port %q", nb.LocalPort, nb.SysName, nb.ChassisID, nb.PortID)
	}

	var groups []map[string]any
	groups, err = c.ReadFaceplate(ctx)
	if err != nil {
		t.Errorf("ReadFaceplate: %v", err)
	} else {
		t.Logf("faceplate: %+v", groups)
	}

	// PoE: per-port scope, configured class, and measured draw (cf. poe.go).
	var poe driver.PoEState
	poe, err = c.ReadPoE(ctx)
	if err != nil {
		t.Logf("ReadPoE: %v (fine on a unit with no PoE hardware at all)", err)
	} else {
		var capable, notCapable int
		var p driver.PoEPortState
		for _, p = range poe.Ports {
			if p.Capable {
				capable++
			} else {
				notCapable++
			}
		}
		t.Logf("poe: model=%q %d port(s) recognized, %d capable / %d not — notes=%v",
			c.facts.Model, len(poe.Ports), capable, notCapable, poe.Notes)
		for _, p = range poe.Ports {
			var temp = "N/A"
			if p.TemperatureC != nil {
				temp = fmt.Sprintf("%.0fC", *p.TemperatureC)
			}
			t.Logf("  %s capable=%v role=%q class=%q delivering=%v %.2fW %.2fV temp=%s",
				p.Port, p.Capable, p.Role, p.Class, p.Delivering, p.PowerW, p.VoltageV, temp)
		}
	}

	// DHCP snooping: the RAW command output first, because that is the artifact
	// this connector has never had (dhcpsnoop.go's header says so), and then the
	// contract read on top of it. Both, in that order, so a parse that quietly
	// returns nothing can be told apart from a firmware that prints nothing.
	var rawSnoop, rawErr = c.cli.run(ctx, dhcpSnoopShow, showTimeout)
	if rawErr != nil {
		t.Errorf("%q: %v", dhcpSnoopShow, rawErr)
	}
	t.Logf("=== %s (raw, %d bytes) ===\n%s", dhcpSnoopShow, len(rawSnoop), rawSnoop)

	var snoop driver.DHCPSnoopState
	snoop, err = c.ReadDHCPSnooping(ctx)
	if err != nil {
		t.Errorf("ReadDHCPSnooping: %v", err)
	} else {
		t.Logf("snooping: enabled=%v vlans=%v ports=%d notes=%v",
			snoop.Enabled, snoop.VLANs, len(snoop.Ports), snoop.Notes)
		var sp driver.DHCPSnoopPortState
		for _, sp = range snoop.Ports {
			t.Logf("  %s trusted=%v", sp.Port, sp.Trusted)
		}
		// A read that finds the feature off must not also claim to know every
		// port's trust: on this platform "untrusted" and "not printed" are the
		// same silence (cf. dhcpsnoop.go).
		if !snoop.Enabled && len(snoop.Ports) > 0 && len(rawSnoop) > 0 {
			t.Logf("NOTE: feature reads off yet %d port(s) reported — check that "+
				"absence is not being rendered as untrusted", len(snoop.Ports))
		}
	}

	// Storm control: the RAW row shape first, because "show storm-control all"
	// has never been seen on this family (stormcontrol.go's header says so), then
	// the contract read on top of it. Both, in that order, so a parse that
	// silently returns nothing can be told from a firmware that prints nothing.
	var rawStorm, stormErr = c.cli.run(ctx, showStormCmd, showTimeout)
	if stormErr != nil {
		t.Errorf("%q: %v", showStormCmd, stormErr)
	}
	t.Logf("=== %s (raw, %d bytes) ===\n%s", showStormCmd, len(rawStorm), rawStorm)

	var storm driver.StormControlState
	storm, err = c.ReadStormControl(ctx)
	if err != nil {
		t.Errorf("ReadStormControl: %v", err)
	} else {
		t.Logf("storm: %d port(s) read, notes=%v", len(storm.Ports), storm.Notes)
		var sp driver.StormControlPortState
		for _, sp = range storm.Ports {
			if len(sp.Limits) > 0 {
				t.Logf("  %s %+v", sp.Port, sp.Limits)
			}
		}
	}

	var s Step
	for _, s = range c.Transcript() {
		t.Logf("transcript: ok=%v %s", s.OK, s.What)
	}
}

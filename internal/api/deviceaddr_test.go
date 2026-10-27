package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/driver"
	_ "meshcraft/internal/driver/instanton"
	"meshcraft/internal/model"
)

// Per-device management addressing mode, tested WITHOUT A DATABASE.
//
// The three functions covered here are pure by design: they decide whether a
// static device found in place stays put, and therefore whether meshcraft leaves
// it alone or flags drift. Testing through HTTP and a database would tie the
// guarantee to a site fixture.

// devWith builds a device for these tests. Both jsonb fields are written as the
// database returns them, so tests use the same decoding as production.
func devWith(mode string, observedMode, observedAddr string, readBack bool) model.Device {
	var d model.Device
	d.MgmtAddr.Mode = mode
	if readBack {
		var raw, _ = json.Marshal(driver.MgmtAddrState{
			Mode: observedMode, Address: observedAddr,
		})
		var at = time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
		d.MgmtAddrObserved, d.MgmtAddrObservedAt = raw, &at
	}
	return d
}

func planDHCP() model.MgmtPlan {
	return model.MgmtPlan{
		Configured: true, VlanID: 99, SubnetV4: "192.0.2.0/24",
		GatewayV4: "192.0.2.1", Source: model.MgmtSourceDHCPExternal,
	}
}

// TestEffectiveAddrModeOrder: three resolution stages, in order.
//
// THE CASE THAT CARRIES THE WHOLE FEATURE is "observed static, nothing decided,
// DHCP plan": it must yield "static", origin "observed". That is the exact
// situation of the two CSS326 and CRS326 in the reference deployment; yielding
// "dhcp" there — by applying site plan origin — would treat three healthy devices
// as drift to fix.
func TestEffectiveAddrModeOrder(t *testing.T) {
	var cas = []struct {
		name         string
		dev          model.Device
		plan         model.MgmtPlan
		mode, origin string
	}{
		{
			name: "decided static, observed cannot override",
			dev:  devWith(model.MgmtAddrStatic, driver.MgmtAddrModeDHCP, "192.0.2.36", true),
			plan: planDHCP(), mode: model.MgmtAddrStatic, origin: "device",
		},
		{
			name: "decided DHCP on device still static",
			dev:  devWith(model.MgmtAddrDHCP, driver.MgmtAddrModeStatic, "192.0.2.36", true),
			plan: planDHCP(), mode: model.MgmtAddrDHCP, origin: "device",
		},
		{
			name: "OBSERVED STATIC KEPT, site plan is DHCP",
			dev:  devWith(model.MgmtAddrInherit, driver.MgmtAddrModeStatic, "192.0.2.36", true),
			plan: planDHCP(), mode: model.MgmtAddrStatic, origin: "observed",
		},
		{
			name: "observed lease kept",
			dev:  devWith(model.MgmtAddrInherit, driver.MgmtAddrModeDHCP, "192.0.2.42", true),
			plan: planDHCP(), mode: model.MgmtAddrDHCP, origin: "observed",
		},
		{
			name: "never read back: site plan decides",
			dev:  devWith(model.MgmtAddrInherit, "", "", false),
			plan: planDHCP(), mode: model.MgmtAddrDHCP, origin: "site_plan",
		},
		{
			name: "never read back, allocating plan: static expected",
			dev:  devWith(model.MgmtAddrInherit, "", "", false),
			plan: model.MgmtPlan{
				Configured: true, SubnetV4: "192.0.2.0/24",
				Source: model.MgmtSourceMeshcraft,
			},
			mode: model.MgmtAddrStatic, origin: "site_plan",
		},
		{
			name: "read back but unreadable mode, no plan: do NOT conclude",
			dev:  devWith(model.MgmtAddrInherit, "", "192.0.2.36", true),
			plan: model.MgmtPlan{},
			mode: "", origin: "unknown",
		},
	}

	var c = cas[0]
	for _, c = range cas {
		var mode, origin, why = effectiveAddrMode(c.dev, c.plan)
		if mode != c.mode || origin != c.origin {
			t.Errorf("%s: (%q, %q), want (%q, %q)",
				c.name, mode, origin, c.mode, c.origin)
		}
		if why == "" {
			t.Errorf("%s: no explanation to display — a screen that does not explain "+
				"addressing mode leaves the operator guessing", c.name)
		}
	}
}

// TestAddrDriftOf: divergence between what meshcraft wants and what the device carries.
//
// Both NEGATIVE cases matter as much as positives. inherit cannot drift — it
// demands nothing — and a never-read device cannot be flagged: that would turn
// ignorance into blame on a fleet largely not yet read back. Same trade-off as
// VLAN path, and it must hold in both places.
func TestAddrDriftOf(t *testing.T) {
	var cas = []struct {
		name  string
		dev   model.Device
		drift bool
	}{
		{
			name:  "wants DHCP, carries static",
			dev:   devWith(model.MgmtAddrDHCP, driver.MgmtAddrModeStatic, "192.0.2.36", true),
			drift: true,
		},
		{
			name:  "wants static, carries lease",
			dev:   devWith(model.MgmtAddrStatic, driver.MgmtAddrModeDHCP, "192.0.2.42", true),
			drift: true,
		},
		{
			name:  "inherited: no requirement, therefore no drift",
			dev:   devWith(model.MgmtAddrInherit, driver.MgmtAddrModeStatic, "192.0.2.36", true),
			drift: false,
		},
		{
			name:  "wants DHCP, never read: ignorance is not blame",
			dev:   devWith(model.MgmtAddrDHCP, "", "", false),
			drift: false,
		},
		{
			name:  "wants static and carries it: nothing to report",
			dev:   devWith(model.MgmtAddrStatic, driver.MgmtAddrModeStatic, "192.0.2.36", true),
			drift: false,
		},
	}

	var c = cas[0]
	for _, c = range cas {
		if (addrDriftOf(c.dev) != "") != c.drift {
			t.Errorf("%s: drift = %q, want %v", c.name, addrDriftOf(c.dev), c.drift)
		}
	}
}

// TestAddrDriftAddressPrefix: ADDRESS drift is not flagged on notation difference.
//
// Concrete trap across a whole product line: SwOS does NOT carry a prefix
// (/sys.b has ip, no mask) where RouterOS renders "192.0.2.54/24". Comparing raw
// strings would flag permanent drift on every CSS326 for a notation difference.
func TestAddrDriftAddressPrefix(t *testing.T) {
	var d = devWith(model.MgmtAddrStatic, driver.MgmtAddrModeStatic, "192.0.2.36", true)
	d.MgmtAddr.StaticIP = "192.0.2.36/24"
	var got = addrDriftOf(d)
	if got != "" {
		t.Errorf("drift wrongly flagged on prefix difference: %q", got)
	}

	d.MgmtAddr.StaticIP = "192.0.2.37/24"
	if addrDriftOf(d) == "" {
		t.Error("real address mismatch NOT flagged: intent and device carry " +
			"two addresses")
	}

	// Intent already matches how meshcraft reaches the device: an older observation
	// that still shows the previous address is stale, not a drift to push.
	d.MgmtAddr.StaticIP = "192.0.2.38/16"
	d.MgmtIP = "192.0.2.38"
	if addrDriftOf(d) != "" {
		t.Errorf("stale observed address raised drift while mgmt_ip matches intent: %q",
			addrDriftOf(d))
	}
}

// TestValidateDeviceAddressing: what is rejected AT INPUT TIME.
//
// Each rejection in this table matches a failure you would hunt elsewhere: a
// gateway outside the subnet yields a device reachable only on its own link,
// a guessed mask silences a device as surely as a wrong address, and a gateway
// entered on SwOS goes nowhere.
func TestValidateDeviceAddressing(t *testing.T) {
	var full = MgmtAddrCaps{Known: true, Static: true, DHCP: true, Gateway: true, DNS: true}
	var swos = MgmtAddrCaps{Known: true, Static: true, DHCP: true}
	var unknown = MgmtAddrCaps{}

	var cas = []struct {
		name     string
		in       deviceAddrInput
		plan     model.MgmtPlan
		caps     MgmtAddrCaps
		rejected bool
		wantIP   string
	}{
		{
			name: "prefix borrowed from site subnet",
			in:   deviceAddrInput{Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36"},
			plan: planDHCP(), caps: full, wantIP: "192.0.2.36/24",
		},
		{
			name: "explicit prefix kept as-is",
			in:   deviceAddrInput{Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/25"},
			plan: planDHCP(), caps: full, wantIP: "192.0.2.36/25",
		},
		{
			name: "bare address without site plan: REJECTED, do not guess mask",
			in:   deviceAddrInput{Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36"},
			plan: model.MgmtPlan{}, caps: full, rejected: true,
		},
		{
			name: "bare address outside subnet: REJECTED, prefix not borrowable",
			in:   deviceAddrInput{Mode: model.MgmtAddrStatic, StaticIP: "10.100.0.36"},
			plan: planDHCP(), caps: full, rejected: true,
		},
		{
			name: "out-of-plan address WITH prefix: accepted, off-plan device is legitimate",
			in:   deviceAddrInput{Mode: model.MgmtAddrStatic, StaticIP: "10.100.0.36/24"},
			plan: planDHCP(), caps: full, wantIP: "10.100.0.36/24",
		},
		{
			name: "network address: REJECTED",
			in:   deviceAddrInput{Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.0/24"},
			plan: planDHCP(), caps: full, rejected: true,
		},
		{
			name: "gateway outside device subnet: REJECTED",
			in: deviceAddrInput{
				Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/24", StaticGW: "10.50.0.1",
			},
			plan: planDHCP(), caps: full, rejected: true,
		},
		{
			name: "gateway equals device itself: REJECTED",
			in: deviceAddrInput{
				Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/24", StaticGW: "192.0.2.36",
			},
			plan: planDHCP(), caps: full, rejected: true,
		},
		{
			name: "valid gateway: accepted",
			in: deviceAddrInput{
				Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/24", StaticGW: "192.0.2.1",
			},
			plan: planDHCP(), caps: full, wantIP: "192.0.2.36/24",
		},
		{
			name: "gateway on platform that does not support it: REJECTED",
			in: deviceAddrInput{
				Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/24", StaticGW: "192.0.2.1",
			},
			plan: planDHCP(), caps: swos, rejected: true,
		},
		{
			name: "DNS on platform that does not support it: REJECTED",
			in: deviceAddrInput{
				Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/24",
				StaticDNS: []string{"192.0.2.1"},
			},
			plan: planDHCP(), caps: swos, rejected: true,
		},
		{
			name: "DNS that is not an address: REJECTED",
			in: deviceAddrInput{
				Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/24",
				StaticDNS: []string{"resolver.local"},
			},
			plan: planDHCP(), caps: full, rejected: true,
		},
		{
			name: "static without address: REJECTED",
			in:   deviceAddrInput{Mode: model.MgmtAddrStatic},
			plan: planDHCP(), caps: full, rejected: true,
		},
		{
			name: "unknown mode: REJECTED",
			in:   deviceAddrInput{Mode: "auto"},
			plan: planDHCP(), caps: full, rejected: true,
		},
		{
			name: "empty mode = inherit, legitimate rollback",
			in:   deviceAddrInput{},
			plan: planDHCP(), caps: full,
		},
		{
			name: "UNKNOWN connector: annotate, do not forbid",
			in: deviceAddrInput{
				Mode: model.MgmtAddrStatic, StaticIP: "192.0.2.36/24", StaticGW: "192.0.2.1",
				StaticDNS: []string{"192.0.2.1"},
			},
			plan: planDHCP(), caps: unknown, wantIP: "192.0.2.36/24",
		},
	}

	var c = cas[0]
	for _, c = range cas {
		var got, msg = validateDeviceAddressing(c.in, c.plan, c.caps)
		if c.rejected {
			if msg == "" {
				t.Errorf("%s: accepted when it should be rejected", c.name)
			}
			continue
		}
		if msg != "" {
			t.Errorf("%s: rejected — %s", c.name, msg)
			continue
		}
		if c.wantIP != "" && got.StaticIP != c.wantIP {
			t.Errorf("%s: address = %q, want %q", c.name, got.StaticIP, c.wantIP)
		}
	}
}

// TestValidateKeepsTheStaticInDHCP: switching to DHCP must NOT destroy stored static config.
//
// That makes the trial reversible: operator switches to DHCP, finds it unsuitable,
// and gets prior addressing back without retyping. Corollary in the same test: stale
// input must not REJECT a DHCP switch, or the gesture we want easy becomes hardest.
func TestValidateKeepsTheStaticInDHCP(t *testing.T) {
	var in = deviceAddrInput{
		Mode: model.MgmtAddrDHCP, StaticIP: "192.0.2.36/24", StaticGW: "192.0.2.1",
	}
	var got, msg = validateDeviceAddressing(in, planDHCP(),
		MgmtAddrCaps{Known: true, Static: true, DHCP: true, Gateway: true, DNS: true})
	if msg != "" {
		t.Fatalf("switch to DHCP rejected: %s", msg)
	}
	if got.StaticIP != "192.0.2.36/24" || got.StaticGateway != "192.0.2.1" {
		t.Errorf("static config lost on DHCP switch: %+v", got)
	}
}

// TestAddrCapsSurviveSTPObserved: Extra.stp_observed is a nested object. Decoding
// Extra as map[string]string used to fail and hide the connector (ANNOTATION only).
func TestAddrCapsSurviveSTPObserved(t *testing.T) {
	var d = model.Device{
		Model: "HPE Networking Instant On Switch 48p Gigabit 4p SFP+ 1930 JL685A",
		Extra: json.RawMessage(`{
			"driver":"aruba-instant-on-1930",
			"config_mode":"push",
			"stp_observed":{"enabled":false,"is_root":false}
		}`),
	}
	var caps = addrCapsOf(d)
	if !caps.Known {
		t.Fatal("connector must stay known when Extra carries nested stp_observed")
	}
	if !caps.ApplySameVLAN {
		t.Fatalf("Instant On ApplySameVLAN must stay true, got %+v", caps)
	}
	if pushBlockedReason(caps) != "" {
		t.Fatalf("push must not be blocked as ANNOTATION, got %q", pushBlockedReason(caps))
	}
	if !readableAddrOf(d) {
		t.Fatal("readableAddrOf must survive nested Extra")
	}
}

// TestPushBlockedAlwaysSays: same-VLAN push refusal is always explained until
// ApplySameVLAN is proven. Instant On's MigrateVLAN path is separate.
func TestPushBlockedAlwaysSays(t *testing.T) {
	var caps = []MgmtAddrCaps{
		{},
		{Known: true, Static: true, DHCP: true},
		{Known: true, Static: true, DHCP: true, Gateway: true, DNS: true},
		{Known: true, Static: true, DHCP: true, MigrateVLAN: true},
	}
	var c MgmtAddrCaps
	for _, c = range caps {
		if pushBlockedReason(c) == "" {
			t.Errorf("caps %+v: no push refusal reason", c)
		}
	}
	var migrated = MgmtAddrCaps{Known: true, Static: true, DHCP: true, MigrateVLAN: true}
	var msg = pushBlockedReason(migrated)
	if !strings.Contains(msg, "Conform Management") {
		t.Errorf("migrate-capable platform should point to Conform Management, got %q", msg)
	}
	var proven = MgmtAddrCaps{Known: true, Static: true, DHCP: true, ApplySameVLAN: true}
	if pushBlockedReason(proven) != "" {
		t.Errorf("ApplySameVLAN proven must leave push_blocked empty, got %q", pushBlockedReason(proven))
	}
	var d = model.Device{MgmtAddr: model.DeviceAddressing{Mode: model.MgmtAddrStatic}}
	// Caps come from the connector; force proven via addressingViewOf after patching
	// would need a registered driver. Check Pushable logic directly:
	var v = addressingViewOf(d, planDHCP())
	if v.Pushable {
		t.Errorf("unknown/manual device must not be pushable")
	}
}

// TestApplyConfirmReason: Apply is never a silent lock — plan override and
// acquisition-mode cutover ask for confirmed=true; same-mode refresh does not.
func TestApplyConfirmReason(t *testing.T) {
	var dhcpPlan = planDHCP()
	dhcpPlan.Configured = true
	dhcpPlan.Source = model.MgmtSourceDHCPExternal
	var staticPlan = planDHCP()
	staticPlan.Configured = true
	staticPlan.Source = model.MgmtSourceMeshcraft

	var cas = []struct {
		name    string
		dev     model.Device
		plan    model.MgmtPlan
		confirm bool
	}{
		{
			name:    "static intent on DHCP site: plan override",
			dev:     devWith(model.MgmtAddrStatic, driver.MgmtAddrModeDHCP, "198.51.100.101", true),
			plan:    dhcpPlan,
			confirm: true,
		},
		{
			name:    "DHCP intent on meshcraft site: plan override",
			dev:     devWith(model.MgmtAddrDHCP, driver.MgmtAddrModeStatic, "192.0.2.36", true),
			plan:    staticPlan,
			confirm: true,
		},
		{
			name: "static refresh same mode: no confirm",
			dev: func() model.Device {
				var d = devWith(model.MgmtAddrStatic, driver.MgmtAddrModeStatic, "192.0.2.36", true)
				d.MgmtAddr.StaticIP = "192.0.2.37/24"
				return d
			}(),
			plan:    staticPlan,
			confirm: false,
		},
		{
			name:    "inherit: nothing to apply",
			dev:     devWith(model.MgmtAddrInherit, driver.MgmtAddrModeDHCP, "198.51.100.101", true),
			plan:    dhcpPlan,
			confirm: false,
		},
	}
	var c = cas[0]
	for _, c = range cas {
		var got = applyConfirmReason(c.dev, c.plan)
		if (got != "") != c.confirm {
			t.Errorf("%s: confirm = %q, want confirm=%v", c.name, got, c.confirm)
		}
	}
}

func TestMgmtHostFromAddr(t *testing.T) {
	var cas = []struct{ in, want string }{
		{"192.0.2.36/24", "192.0.2.36"},
		{"192.0.2.36", "192.0.2.36"},
		{" 192.0.2.36/24 ", "192.0.2.36"},
		{"", ""},
	}
	var c struct{ in, want string }
	for _, c = range cas {
		var got = mgmtHostFromAddr(c.in)
		if got != c.want {
			t.Errorf("mgmtHostFromAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

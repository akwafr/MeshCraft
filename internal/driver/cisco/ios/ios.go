// Package ios is the Cisco Catalyst / IOS connector knowledge base, calibrated
// on the WS-C2960X range (IOS 15.2(7)E, C2960X-UNIVERSALK9-M).
//
// # The status of what is written here, said before anything else
//
// Every CLI form below is DOCUMENTED, not captured. That is the difference
// with the FS S3900 connector next door, whose every string was typed on lab
// hardware and whose comments say so line by line. IOS is a far more stable and
// far better documented CLI than FSOS — which is why this connector exists at
// all before a capture — but "well known" is not "observed", and the project
// rule does not bend for a popular vendor: what has not been seen is announced
// as such, in the transcript, at every login (cf. [Client.postLogin]), and in
// [Driver.ResetProcedure] via `Confirmed: false`.
//
// The practical consequence is in apply.go: forms that differ across the
// Catalyst family are run as ORDERED candidates rather than assumed. The one
// that gets accepted is recorded in the transcript — which is how this
// connector produces, on its own, the capture it was missing.
//
// # The two forms that actually differ across the family
//
//   - trunk encapsulation. A 2960/2960X speaks 802.1Q and nothing else, so
//     `switchport mode trunk` is enough. A 3560/3750 refuses it outright
//     ("An interface whose trunk encapsulation is Auto can not be configured to
//     trunk mode") until `switchport trunk encapsulation dot1q` is set. Sending
//     the encapsulation line unconditionally would fail on the 2960X, and
//     omitting it fails on the 3750: it is a fallback, not a default;
//   - saving. `write memory` answers nothing on IOS; `copy running-config
//     startup-config` asks "Destination filename [startup-config]?". The first
//     is used, the second is the fallback, and the prompt is answered rather
//     than waited out.
//
// # What is NOT here on purpose
//
// SNMPv3. IOS carries it (`snmp-server group` / `snmp-server user`), and this
// connector still declares v1/v2c only — because v3 users do NOT appear in
// `show running-config` on IOS, so the "nothing is taken as written on the
// strength of a return code" rule would need `show snmp user`, a read nobody
// has performed here. Declaring v3 without that read would advertise monitoring
// this connector cannot verify it turned on.
package ios

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver identifies Cisco Catalyst / IOS and reaches it over SSH (contract.go).
// Soft-reset pushes management over Web Serial using [MgmtPushCmds]; both paths
// speak the same IOS command set, and a device whose `show version` does not
// announce IOS gets reads only.
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:      "cisco-ios",
		Vendor:    "Cisco",
		Family:    "Catalyst 2960 / 2960-X / 2960-XR (IOS 12.2–15.2)",
		Transport: []string{"ssh", "serial", "telnet"},
		Doc: "Catalyst 2960-X Series Switch Command Reference, IOS 15.2(7)E " +
			"(https://www.cisco.com/c/en/us/td/docs/switches/lan/catalyst2960x/software/15-2_7_e/command_reference.html)",
	}
}

var (
	reIOS   = regexp.MustCompile(`(?i)Cisco IOS|IOS-XE|IOS Software|Catalyst|WS-C\d`)
	re2960X = regexp.MustCompile(`(?i)C2960X|WS-C2960X|2960-X`)
	reCisco = regexp.MustCompile(`(?i)\bcisco\b`)
)

// monitoring: through which channels this platform is observed.
//
// The product's periodic collector speaks only SNMP, so a channel declared
// under any other kind would be collected by nobody. Everything listed here is
// a STANDARD MIB that the poller (internal/snmppoll) already queries — nothing
// Cisco-proprietary is claimed, and this is the reason `health` carries the
// note it does.
//
// Unlike the FS S3900 — where the walks were performed and the absences proven
// — NOTHING here has been walked on a 2960X. The channels are declared because
// the MIBs they rest on are the ones IOS is documented to serve on every
// Catalyst since 12.2, and the note on each says what remains to be confirmed.
// The floors are deliberately conservative: an access switch's SNMP engine is
// modest, and a v2c agent polled too hard answers less and less.
func monitoring() driver.MonitoringCapabilities {
	return driver.MonitoringCapabilities{
		Channels: []driver.MonitorChannel{
			{
				Kind:        "snmp",
				Metrics:     []string{"if_state", "if_counters"},
				MinInterval: 15 * time.Second,
				Note: "standard IF-MIB; served as soon as a v2c community is set (ApplySNMP). " +
					"ifDescr on this platform is the full interface name (\"GigabitEthernet1/0/11\"), " +
					"which the connector translates via driver.PortNamer",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				Note: "standard LLDP-MIB (1.0.8802). IOS ships with LLDP OFF and speaks " +
					"only CDP, so this branch is empty until « lldp run » is configured " +
					"globally — which provisioning NOW writes (cf. MgmtPushCmds). A switch " +
					"adopted before that change, or configured by hand, still needs the " +
					"command: the LLDP screen reports not_served rather than a fault, and " +
					"the device is invisible to every neighbour in the fleet until it is set",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"health"},
				MinInterval: 5 * time.Minute,
				Note: "sysUpTime (1.3.6.1.2.1.1.3) is served by every IOS. CPU and memory are " +
					"NOT claimed here: IOS carries them under CISCO-PROCESS-MIB / " +
					"CISCO-MEMORY-POOL-MIB (enterprise 9), which the poller does not query — " +
					"cf. the Unavailable declarations below",
			},
		},
		// « snmp-server host » exists on IOS and is not written by this connector:
		// declaring traps would open 162/udp on the proxy for a destination nobody
		// posed.
		Traps:  false,
		Syslog: false,
		// What this platform does not deliver TO THIS PRODUCT, with the reason.
		// Said out loud so the screen answers "why is there no CPU curve?" instead
		// of leaving an operator waiting for a release.
		//
		// Note the wording: these absences are NOT observed on hardware (no walk
		// has been run on a 2960X here), they follow from what the POLLER asks
		// for. Observed stays false — an assumed absence must remain revisable.
		Unavailable: []driver.MetricUnavailable{
			{
				Metric:   "optical_power",
				Observed: false,
				Source: "the poller's optical OIDs are MikroTik-proprietary (mtxrOptical, " +
					"1.3.6.1.4.1.14988.1.1.19). IOS exposes transceiver diagnostics under " +
					"CISCO-ENTITY-SENSOR-MIB on models whose SFPs support DOM — never queried here",
			},
		},
	}
}

// Fingerprint. "Cisco" alone is deliberately weak: the word shows up in banners
// and in third-party gear that mimics the CLI, and a false positive here routes
// a write to the wrong command set.
func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var hay = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if re2960X.MatchString(hay) {
		return 92
	}
	if reIOS.MatchString(hay) {
		return 85
	}
	if reCisco.MatchString(hay) {
		return 40
	}
	return 0
}

func (d *Driver) Capabilities(model string) driver.Capabilities {
	return driver.Capabilities{
		VlanRange: [2]int{1, 4094},
		// A 2960-X carries 255 active VLANs (LAN Base), not 4094 — the ID range
		// and the SIMULTANEOUS count are two different limits, and it is the
		// second one a write hits.
		MaxVlans:     255,
		Features:     map[string]bool{"ssh": true, "telnet": true, "serial": true},
		STPModes:     []string{"stp", "rstp", "mstp"},
		SNMPVersions: []string{"v1", "v2c"},
		// vty 0 15 = sixteen lines; the console is one more. Kept at 5 for the
		// same reason as everywhere else: this is a budget, not a target.
		MaxSessions: 5,
		// IOS distinguishes running from startup: apply, verify, THEN save.
		Commit:     driver.CommitApplyThenSave,
		Monitoring: monitoring(),
		Mgmt: driver.MgmtCapabilities{
			VlanIDs:   driver.VlanAny,
			VlanRange: [2]int{1, 4094},
			TaggedOK:  true,
			Static:    true,
			DHCP:      true,
			Gateway:   true,
			DNS:       true,
			// A 2960-X is L2: it has no routing table, only « ip default-gateway ».
			// Off-subnet outbound therefore works, through that gateway alone.
			Routed:    true,
			IPARP:     true,
			OutOfBand: true, // RJ45 console, 9600 8N1
			// ApplySameVLAN stays FALSE: the write forms are documented, none has
			// been run on hardware from this connector. It flips the day a
			// same-VLAN re-addressing is observed to work end to end — not before,
			// because this is the capability that decides whether a screen offers
			// a button that can make a device mute.
			ApplySameVLAN: false,
			Notes: []string{
				"« ip address A.B.C.D M.M.M.M » (dotted mask) — IOS does not accept the " +
					"CIDR form the FS connector uses",
				"an SVI created by « interface vlan N » comes up SHUTDOWN: « no shutdown » " +
					"is required, and the VLAN must exist in the VLAN database first",
				"changing the address of the SVI the session came in through cuts the " +
					"session: verify afterward through the NEW address",
			},
		},
		Admin: driver.AdminAccount{
			Custom: true,
			Notes: []string{
				"username U privilege 15 secret 0 SECRET",
				"enable secret 0 SECRET",
				"SSH needs « ip domain-name » + « crypto key generate rsa » before " +
					"« transport input ssh » is of any use",
			},
		},
	}
}

func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Software: "enable; write erase → [confirm]; delete flash:vlan.dat → [confirm] ×2; " +
			"reload → answer « no » to « Save? », then [confirm]",
		Hardware: "Power off. Hold MODE and power on; release when the SYST LED blinks " +
			"amber then turns green (10–15 s) → « switch: » prompt. " +
			"flash_init ; del flash:config.text ; del flash:vlan.dat ; boot",
		Erases: []string{
			"startup configuration (nvram)",
			"VLAN database — ONLY if flash:vlan.dat is deleted explicitly: « write erase » leaves it",
		},
		AfterReset: []string{
			"9600 8N1 console (NOT 115200 — that is the FS default)",
			"« Would you like to enter the initial configuration dialog? [yes/no]: » → answer no",
			"« Press RETURN to get started! » → Switch> , « enable » gives # with NO password",
			"push management via MgmtPushCmds / soft-reset",
		},
		Pitfalls: []string{
			"« reload » asks « System configuration has been modified. Save? [yes/no]: » — " +
				"answering yes RE-SAVES the configuration that was just erased. Answer no",
			"« write erase » does NOT remove the VLAN database: delete flash:vlan.dat too, " +
				"or VLANs survive the reset",
			"the setup dialog after reboot swallows every command typed at it and answers " +
				"« % Please answer 'yes' or 'no'. » — it must be declined before anything else",
			"« crypto key generate rsa » fails with « % Please define a domain-name first » " +
				"until « ip domain-name » is set; it then takes tens of seconds on a 2960-X",
			"« switchport mode trunk » is refused on 3560/3750 until « switchport trunk " +
				"encapsulation dot1q » is set; a 2960/2960-X refuses that same encapsulation " +
				"line — the connector tries both forms in order rather than assuming one",
			"a non-k9 image has no « crypto » command at all: SSH is then impossible and " +
				"the device stays serial/telnet only",
			"the uplink must carry the management VLAN (switchport trunk allowed vlan add N) " +
				"or the probe that follows the push cannot reach the new address",
			"THIRD-PARTY SFP: IOS refuses any optic it does not read as Cisco-coded, shuts " +
				"the port (err-disabled, reason gbic-invalid) and logs " +
				"« %PHY-4-UNSUPPORTED_TRANSCEIVER ». The cage then reads exactly like an " +
				"EMPTY one, so the uplink looks absent while the fibre is plugged in — this " +
				"is the most common way a provisioning run dies looking like a cabling " +
				"fault. Provisioning applies « service unsupported-transceiver » + « no " +
				"errdisable detect cause gbic-invalid » and bounces the affected ports; a " +
				"factory reset removes both, which is why they are re-applied every run",
		},
		// NOTHING here has been run on a 2960-X by this connector. The gestures are
		// the vendor's documented ones and are stable across the family, which is
		// not the same as verified — and the UI must be able to say which.
		Confirmed: false,
		Source: "Catalyst 2960-X command reference (IOS 15.2(7)E) + Cisco password-recovery " +
			"procedure — DOCUMENTED, not captured on lab hardware",
	}
}

// --- CLI builders (the serial soft-reset and the SSH connector share these) ---

// MgmtOpts is what soft-reset / onboarding needs on the wire.
type MgmtOpts struct {
	VlanID    int
	VlanName  string // default LAN<id>_Management
	IP        string
	PrefixLen int // e.g. 16 for /16 — converted to the dotted mask IOS wants
	Gateway   string
	// DomainName is required BEFORE « crypto key generate rsa »; defaults to
	// [DefaultDomainName] rather than being skipped, because skipping it makes
	// SSH silently impossible.
	DomainName string
	Username   string
	Password   string
	// EnablePassword defaults to Password: meshcraft stores one secret per
	// account, and « enable » is the only door from user EXEC to privileged mode.
	EnablePassword string
	Hostname       string
	// TrunkPorts are FULL IOS interface names ("GigabitEthernet1/0/25"), the
	// ones discovered to have a link. Empty skips trunk membership.
	TrunkPorts []string
	// RSAModulus for the SSH host key. 2048 is the floor that IOS 15.2 accepts
	// for SSHv2 in practice; 4096 takes minutes on a 2960-X and is not worth it.
	RSAModulus int
}

// DefaultDomainName is used when the caller supplies none: « crypto key generate
// rsa » refuses to run without a domain name, and a switch left without SSH
// after a provisioning run is a switch nobody can adopt.
const DefaultDomainName = "meshcraft.local"

// FactoryResetCmds is the soft factory-reset sequence (privileged EXEC).
//
// Each of these commands PROMPTS, and the prompts are not interchangeable —
// the caller answers them by name (cf. web/src/pages/add/ciscoIosSoftReset.ts):
//
//	write erase           → "Continue? [confirm]"           → Enter
//	delete flash:vlan.dat → "Delete filename [vlan.dat]?"   → Enter
//	                      → "Delete flash:/vlan.dat? [confirm]" → Enter
//	reload                → "Save? [yes/no]:"               → no   ← NOT Enter
//	                      → "Proceed with reload? [confirm]" → Enter
//
// The « no » is the one that matters: the default on that prompt saves the
// running configuration back over the nvram that was just erased.
func FactoryResetCmds() []string {
	return []string{
		"write erase",
		"delete flash:vlan.dat",
		"reload",
	}
}

// MgmtPushCmds returns configure-mode lines that bring a factory Catalyst to a
// reachable management state (same commands over serial or SSH).
//
// The ORDER is load-bearing and is not a style choice:
//
//  1. the VLAN exists before the SVI that serves it, or the SVI never comes up;
//  2. the domain name exists before the RSA key, or key generation is refused;
//  3. the uplink carries the management VLAN before anything else can be probed;
//  4. CREDENTIALS LAST — same reason as the FS connector: the console session
//     can drop right at the password change of the account currently logged in,
//     and everything else is then already in place.
func MgmtPushCmds(opts MgmtOpts) []string {
	var vlanID = opts.VlanID
	if vlanID < 1 {
		vlanID = 1
	}
	var prefix = opts.PrefixLen
	if prefix < 1 || prefix > 32 {
		prefix = 24
	}
	var name = strings.TrimSpace(opts.VlanName)
	if name == "" {
		name = fmt.Sprintf("LAN%d_Management", vlanID)
	}
	name = sanitizeVlanName(name)

	var out []string

	// FIRST, before anything that depends on a link existing. IOS shuts a cage
	// holding an optic it does not read as Cisco-coded, and such a port is
	// indistinguishable from an empty one — so uplink discovery finds nothing,
	// the management VLAN is carried nowhere, and the probe blames the network.
	// Both lines live in the running configuration, so the factory reset that
	// precedes this push is precisely what removed them.
	out = append(out, UnsupportedTransceiverCmds()...)

	if opts.Hostname != "" {
		out = append(out, "hostname "+sanitizeHostname(opts.Hostname))
	}

	// 1. VLAN before SVI. VLAN 1 always exists and must not be re-declared.
	if vlanID != 1 {
		out = append(out,
			fmt.Sprintf("vlan %d", vlanID),
			"name "+name,
			"exit",
		)
	}

	out = append(out,
		fmt.Sprintf("interface vlan %d", vlanID),
		fmt.Sprintf("ip address %s %s", opts.IP, MaskFromPrefix(prefix)),
		// An SVI created in configuration mode comes up administratively down.
		"no shutdown",
		"exit",
	)

	var gw = strings.TrimSpace(opts.Gateway)
	if gw != "" {
		// A 2960-X has no routing table: « ip route » would be refused, and
		// « ip default-gateway » is the only form that works on an L2 switch.
		out = append(out, "ip default-gateway "+gw)
	}

	// 2. Domain name before the key, key before SSH is worth enabling.
	var domain = strings.TrimSpace(opts.DomainName)
	if domain == "" {
		domain = DefaultDomainName
	}
	var modulus = opts.RSAModulus
	if modulus < 512 {
		modulus = 2048
	}
	out = append(out,
		"ip domain-name "+domain,
		fmt.Sprintf("crypto key generate rsa modulus %d", modulus),
		"ip ssh version 2",
		"line vty 0 15",
		"login local",
		"transport input ssh",
		"exit",
		// LLDP, GLOBALLY, and it is not cosmetic.
		//
		// IOS ships with LLDP OFF and speaks only CDP, so a Catalyst is INVISIBLE
		// to every neighbour in the fleet and serves nothing under LLDP-MIB — the
		// channel this connector declares for `lldp` stays empty, and meshcraft's
		// topology has a hole exactly where the switch is.
		//
		// The cost of that is not theoretical. On 2026-08-02 an uplink on a
		// freshly provisioned 2960-X could not be located at all: no LLDP row
		// existed for it anywhere, so which switch it was patched into had to be
		// inferred from the ports OTHER devices reported, and the inference was
		// wrong — the fibre was on the FS S3900, not the Instant On. Hours of
		// diagnosis on a blocked port started from that missing line.
		//
		// One command, at provisioning, closes it for every future unit.
		"lldp run",
	)

	// 3. Management VLAN on the live uplink(s), or nothing can probe the address.
	if vlanID != 1 {
		var port string
		for _, port = range opts.TrunkPorts {
			port = strings.TrimSpace(port)
			if port == "" {
				continue
			}
			out = append(out,
				"interface "+port,
				"switchport mode trunk",
				fmt.Sprintf("switchport trunk allowed vlan add %d", vlanID),
				"exit",
			)
		}
	}

	// 4. Credentials last.
	var user = strings.TrimSpace(opts.Username)
	var pass = opts.Password
	if user != "" && pass != "" {
		// « secret » and not « password »: the second stores the value in clear
		// (or reversibly encoded as type 7) in the configuration.
		out = append(out, fmt.Sprintf("username %s privilege 15 secret 0 %s", user, pass))
	}
	var en = opts.EnablePassword
	if en == "" {
		en = pass
	}
	if en != "" {
		out = append(out, fmt.Sprintf("enable secret 0 %s", en))
	}

	return out
}

// SaveCmd is the save this connector emits first; [SaveCmdFallback] is the one
// tried if IOS refuses it. « write memory » prompts for nothing, which is why
// it comes first.
const (
	SaveCmd         = "write memory"
	SaveCmdFallback = "copy running-config startup-config"
)

// MaskFromPrefix turns a prefix length into the dotted mask IOS requires
// ("16" -> "255.255.0.0"). Out-of-range input falls back to /24 rather than
// producing a mask that would silently misconfigure the SVI.
func MaskFromPrefix(prefix int) string {
	if prefix < 0 || prefix > 32 {
		prefix = 24
	}
	var octets [4]int
	var i int
	for i = 0; i < 4; i++ {
		var bits = prefix - i*8
		if bits >= 8 {
			octets[i] = 255
			continue
		}
		if bits <= 0 {
			octets[i] = 0
			continue
		}
		octets[i] = 256 - (1 << uint(8-bits))
	}
	return fmt.Sprintf("%d.%d.%d.%d", octets[0], octets[1], octets[2], octets[3])
}

// PrefixFromMask is the reverse, for reading a running-config back. Returns 0
// when the mask is unreadable — the caller then says so instead of assuming /24.
func PrefixFromMask(mask string) int {
	var parts = strings.Split(strings.TrimSpace(mask), ".")
	if len(parts) != 4 {
		return 0
	}
	var bits int
	var p string
	for _, p = range parts {
		var b, err = strconv.Atoi(p)
		if err != nil || b < 0 || b > 255 {
			return 0
		}
		for b > 0 {
			bits += b & 1
			b >>= 1
		}
	}
	return bits
}

// ParseUpPorts extracts the FULL interface names of the ports whose link is up,
// from « show interfaces status » output. This is what the soft-reset uses to
// decide which uplink must carry the management VLAN.
func ParseUpPorts(status string) []string {
	var rows = parseInterfacesStatus(status)
	var out []string
	var seen = map[string]bool{}
	var r statusPort
	for _, r = range rows {
		if !r.LinkUp || seen[r.CLIName] {
			continue
		}
		seen[r.CLIName] = true
		out = append(out, r.CLIName)
	}
	return out
}

func sanitizeVlanName(s string) string {
	var b strings.Builder
	var r rune
	for _, r = range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' {
			b.WriteByte('_')
		}
	}
	var out = b.String()
	if out == "" {
		return "Management"
	}
	// IOS caps a VLAN name at 32 characters and refuses the command beyond it.
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func sanitizeHostname(s string) string {
	var b strings.Builder
	var r rune
	for _, r = range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' {
			b.WriteByte('-')
		}
	}
	var out = b.String()
	if out == "" {
		return "switch"
	}
	return out
}

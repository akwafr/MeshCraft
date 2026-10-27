// Package ios is the Cisco Catalyst / IOS connector knowledge base, calibrated
// on the WS-C2960X range (IOS 15.2(7)E, C2960X-UNIVERSALK9-M).
//
// # The status of what is written here, said before anything else
//
// This header opened with "Every CLI form below is DOCUMENTED, not captured"
// from the day the connector was written until 2026-08-08, by which point the
// lab 2960-X (CiscoLabFirst1) had disproved it several times over. It is now
// SPLIT, because a blanket claim in either direction is the wrong shape:
//
//   - EXERCISED THROUGH THIS CONNECTOR, on that hardware: management addressing
//     (2026-08-03), the system name (2026-08-05, a converging rename),
//     [Client.ApplyTrapReceiver] and [Client.ApplyStormControl] (each apply →
//     idempotent re-apply → revert, proven by re-read — live_trap_test.go and
//     live_stormcontrol_test.go), plus the spanning-tree read (2026-08-02) and
//     the DHCP-snooping read off AND on (2026-08-08, both in testdata/);
//   - STILL DOCUMENTED ONLY: the port VLAN write path — "switchport mode
//     access / trunk", the allowed-VLAN forms and the encapsulation fallback —
//     and "shutdown" on a physical port. These are the forms whose refusals
//     name [DocumentedRelease] and ask for a survey.
//
// The rule that produced the original sentence has not changed and is why the
// split is worth maintaining: IOS is a far more stable and far better
// documented CLI than FSOS — which is why this connector existed at all before
// a capture — but "well known" is not "observed". What has not been seen is
// announced as such, in the transcript, at every login (cf.
// [Client.postLogin]), and in [Driver.ResetProcedure] via `Confirmed: false`
// (that one is still accurate: no factory reset has been performed here).
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
	"errors"
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
		// The CLI sits behind SSH authentication: nothing is identifiable
		// without a credential.
		IdentifyNeedsCredentials: true,
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
					"only CDP, so this branch is empty until \"lldp run\" is configured " +
					"globally — which provisioning NOW writes (cf. MgmtPushCmds). A switch " +
					"adopted before that change, or configured by hand, still needs the " +
					"command: the LLDP screen reports not_served rather than a fault, and " +
					"the device is invisible to every neighbour in the fleet until it is set",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"fdb"},
				MinInterval: 30 * time.Second,
				Note: "MEASURED (walks 2026-08-13 on the C2960X): dot1qTpFdbPort is EMPTY " +
					"and the flat dot1dTpFdbPort serves only the default VLAN's instance " +
					"(6 rows, all VLAN 1). IOS reads its table per VLAN through community " +
					"indexing (\"community@N\"), VLANs enumerated from vtpVlanState (9 rows " +
					"here, of which 1002-1005 are FDDI/TR defaults) — hence FDBPerVLAN below.",
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
			{
				Kind:    "snmp",
				Metrics: []string{"optical_power"},
				// A minute: a fibre's level moves on the minute scale, and the walk
				// is six branches joined per cage rather than one column.
				MinInterval: time.Minute,
				Note: "CISCO-ENTITY-SENSOR-MIB (enterprise 9.9.91), JOINED to ENTITY-MIB to " +
					"learn which port each sensor describes. CAPTURED 2026-08-12 on a " +
					"WS-C2960X-24TD-L: entSensorType 14 is dBm, and entPhysicalName names both " +
					"the direction (\"Te1/0/2 Transmit Power Sensor\") and — through " +
					"entPhysicalContainedIn — the port (\"TenGigabitEthernet1/0/2\", an " +
					"SFP-10GBase-LR). TX -3.9 dBm and RX -2.7 dBm read back on that cage, with " +
					"module temperature, supply voltage and laser bias on the same join. The " +
					"STANDARD ENTITY-SENSOR-MIB (1.3.6.1.2.1.99) answers NOTHING on this " +
					"platform, which is why the enterprise tree is the one walked",
			},
		},
		// "snmp-server host" IS written by this connector (trap.go,
		// [Client.ApplyTrapReceiver]): declaring it here is what makes the proxy
		// open 162/udp for this platform (watchConfigFor) — the two halves of the
		// same capability, per the [driver.TrapPusher] contract.
		Traps:  true,
		Syslog: false,
		// The community@vlan regime the fdb channel's note establishes: the
		// controller copies this onto the watch, the proxy executes (PollFDB).
		FDBPerVLAN: true,
		// NOTHING IS DECLARED UNAVAILABLE ON THIS PLATFORM ANY MORE, and the
		// emptiness is the record of a walk rather than an oversight.
		//
		// The list held one entry — `optical_power` — whose reason was that the
		// poller's optical OIDs are MikroTik-proprietary and that IOS carries DOM
		// "under CISCO-ENTITY-SENSOR-MIB on models whose SFPs support DOM, never
		// queried here". On 2026-08-12 that tree WAS queried, on a live
		// WS-C2960X-24TD-L, and it answers: the channel above quotes the capture.
		// The entry went with it, because an Unavailable that has stopped being
		// true is worse than none — a screen repeats it to an operator as the
		// reason a curve is missing, and they wait for a release that already
		// shipped.
		//
		// The old preamble also said no walk had ever been run on a 2960X here.
		// Several have now (health scales 2026-08-02, DOM 2026-08-12), so an
		// absence added below from now on should say which walk found it.
		Unavailable: nil,
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
		MaxVlans: 255,
		Features: map[string]bool{"ssh": true, "telnet": true, "serial": true,
			// LACP: read AND written live on the lab C2960X (2026-08-13) —
			// Po5 built with "channel-group 5 mode active", read back in
			// "show etherchannel summary", torn down clean.
			"LACP": true,
			// MSTRegion: this connector emits the whole triplet —
			// "spanning-tree mst configuration" then name / revision /
			// "instance N vlan …" — and emits it FIRST in the block, because a
			// mode written without its region puts the switch in a region of one
			// (stp.go's header argues it at length).
			//
			// NOT FeatSTPSuperRoot, which this connector still refuses: the two
			// keys exist precisely so that "writes a region" and "expresses Super
			// Root" can disagree on the same platform, and here they do. Declaring
			// the region was an omission ADR-0023 closes, not a new promise — the
			// code has emitted it since the file was written.
			driver.FeatMSTRegion: true},
		// 2960-X EtherChannel bounds (LAN Base): 24 groups of 8.
		MaxLAGs:      24,
		PortsPerLAG:  8,
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
			// A 2960-X is L2: it has no routing table, only "ip default-gateway".
			// Off-subnet outbound therefore works, through that gateway alone.
			Routed:    true,
			IPARP:     true,
			OutOfBand: true, // RJ45 console, 9600 8N1
			// ApplySameVLAN: EXERCISED ON HARDWARE 2026-08-03, on the lab
			// WS-C2960X at 192.0.2.2. driver.MgmtAddrWriter re-applied the
			// address the switch already carried (192.0.2.2/16 on vlan99, with
			// its gateway) through the real command path — interface selection
			// from the read, CIDR converted to the dotted mask IOS wants,
			// "no shutdown", "ip default-gateway", then a re-read that came
			// back identical on a session that survived.
			//
			// What that proves and what it does not: the FORMS are accepted and
			// the block applies cleanly. A re-address to a DIFFERENT subnet was
			// deliberately not run — the session travels on the address being
			// moved — so the honest statement is that this connector can apply
			// addressing, and that the caller must verify through the new
			// address (which ApplyMgmtAddressing says in its own notes).
			ApplySameVLAN: true,
			Notes: []string{
				"\"ip address A.B.C.D M.M.M.M\" (dotted mask) — IOS does not accept the " +
					"CIDR form the FS connector uses",
				"an SVI created by \"interface vlan N\" comes up SHUTDOWN: \"no shutdown\" " +
					"is required, and the VLAN must exist in the VLAN database first",
				"changing the address of the SVI the session came in through cuts the " +
					"session: verify afterward through the NEW address",
			},
		},
		Admin: driver.AdminAccount{
			Custom: true,
			Notes: []string{
				"username U privilege 15 secret 0 SECRET",
				"NO ENABLE PASSWORD is set, by fleet rule, and \"no enable secret\" / " +
					"\"no enable password\" are written to remove one a device already " +
					"carried. On THIS platform that makes \"privilege 15\" above " +
					"load-bearing: IOS refuses \"enable\" from a vty when no enable " +
					"password exists (\"% No password set\"), so an account without it " +
					"cannot reach \"#\" over the network — only from the console",
				"SSH needs \"ip domain-name\" + \"crypto key generate rsa\" before " +
					"\"transport input ssh\" is of any use",
			},
		},
	}
}

func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Software: "enable; write erase → [confirm]; delete flash:vlan.dat → [confirm] ×2; " +
			"reload → answer \"no\" to \"Save?\", then [confirm]",
		Hardware: "ASSISTED by the serial wizard (Add → serial → hardware reset): it " +
			"watches the console, tells you when to power-cycle holding MODE and when " +
			"to release, then wipes and provisions by itself. By hand: power off, hold " +
			"MODE and power on, release at the \"switch:\" prompt, then " +
			"flash_init ; del flash:config.text ; del flash:vlan.dat ; boot",
		Erases: []string{
			"startup configuration (nvram)",
			"VLAN database — ONLY if flash:vlan.dat is deleted explicitly: \"write erase\" leaves it",
		},
		AfterReset: []string{
			"9600 8N1 console (NOT 115200 — that is the FS default)",
			"\"Would you like to enter the initial configuration dialog? [yes/no]:\" → answer no",
			"\"Press RETURN to get started!\" → Switch> , \"enable\" gives # with NO password",
			"push management via MgmtPushCmds / soft-reset",
		},
		Pitfalls: []string{
			"\"reload\" asks \"System configuration has been modified. Save? [yes/no]:\" — " +
				"answering yes RE-SAVES the configuration that was just erased. Answer no",
			"\"write erase\" does NOT remove the VLAN database: delete flash:vlan.dat too, " +
				"or VLANs survive the reset",
			"the setup dialog after reboot swallows every command typed at it and answers " +
				"\"% Please answer 'yes' or 'no'.\" — it must be declined before anything else",
			"\"crypto key generate rsa\" fails with \"% Please define a domain-name first\" " +
				"until \"ip domain-name\" is set; it then takes tens of seconds on a 2960-X",
			"\"switchport mode trunk\" is refused on 3560/3750 until \"switchport trunk " +
				"encapsulation dot1q\" is set; a 2960/2960-X refuses that same encapsulation " +
				"line — the connector tries both forms in order rather than assuming one",
			"a non-k9 image has no \"crypto\" command at all: SSH is then impossible and " +
				"the device stays serial/telnet only",
			"the uplink must carry the management VLAN (switchport trunk allowed vlan add N) " +
				"or the probe that follows the push cannot reach the new address",
			"THIRD-PARTY SFP: IOS refuses any optic it does not read as Cisco-coded, shuts " +
				"the port (err-disabled, reason gbic-invalid) and logs " +
				"\"%PHY-4-UNSUPPORTED_TRANSCEIVER\". The cage then reads exactly like an " +
				"EMPTY one, so the uplink looks absent while the fibre is plugged in — this " +
				"is the most common way a provisioning run dies looking like a cabling " +
				"fault. Provisioning applies \"service unsupported-transceiver\" + \"no " +
				"errdisable detect cause gbic-invalid\" and bounces the affected ports; a " +
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

// MgmtPushCmds HAS NO GO CALLER, AND THAT IS THE CONVENTION RATHER THAN A HOLE.
//
// Not one of the six MgmtPushCmds in this tree has one — cisco/ios,
// ubiquiti/edgeswitch, fs/s3400, fs/s3900 and arista/eos are all reached only
// from their own tests. The argument is written out once, at
// arista/eos/eos.go's equivalent: the block exists to GIVE a switch a management
// address, so a switch that has one is already provisioned and a switch that has
// not cannot be reached over SSH — and a switch still in ZTP has every write
// refused by requireWritableDialect anyway. There is no device state in which an
// SSH caller can both reach the switch and needs this.
//
// The builder is the written SPECIFICATION of a gesture that runs in the
// BROWSER, over Web Serial: web/src/pages/add/ciscoIosSoftReset.ts. That script
// is the path that reaches hardware, and internal/discovery/reset.go points an
// operator at this function by name as the reference for what it must send.
//
// SO THE UNFILLED FIELDS BELOW ARE A SPECIFICATION, NOT DEAD WIRING.
// DomainName in particular: the runnable path resolves the domain from the
// site's own field (site.local_domain → resolveDomainName), and falls back to
// [DefaultDomainName] only when a site declares none. Before 2026-08-09 the
// script passed nothing at all, so that fallback was not a fallback — it was the
// only domain this product had ever written, on every console-provisioned
// Catalyst. Do not "fix" the absence of a Go caller here by adding one.

// MgmtOpts is what soft-reset / onboarding needs on the wire.
type MgmtOpts struct {
	VlanID    int
	VlanName  string // default LAN<id>_Management
	IP        string
	PrefixLen int // e.g. 16 for /16 — converted to the dotted mask IOS wants
	Gateway   string
	// DomainName is required BEFORE "crypto key generate rsa"; defaults to
	// [DefaultDomainName] rather than being skipped, because skipping it makes
	// SSH silently impossible.
	DomainName string
	Username   string
	Password   string
	// NOTE: there is deliberately NO EnablePassword field any more. The fleet
	// rule is that no switch carries an enable password (cf. NoEnableCmds), and
	// the field existed only to be defaulted to Password and written — removing
	// it is what makes the rule impossible to bypass by filling in a struct.
	//
	// Hostname is the device's MESHCRAFT NAME, not an already-normalized system
	// name: MgmtPushCmds runs it through [driver.HostnameFromDeviceName] — the
	// same function api.hostnameheal uses — so a switch provisioned from a
	// console and the same switch reconciled half an hour later cannot end up
	// carrying two different names. A name that cannot be normalized is REFUSED,
	// never mangled: MgmtPushCmds returns the reason and omits the line.
	Hostname string
	// TrunkPorts are FULL IOS interface names ("GigabitEthernet1/0/25"), the
	// ones discovered to have a link. Empty skips trunk membership.
	TrunkPorts []string
	// RSAModulus for the SSH host key. 2048 is the floor that IOS 15.2 accepts
	// for SSHv2 in practice; 4096 takes minutes on a 2960-X and is not worth it.
	RSAModulus int
}

// DefaultDomainName is used when the caller supplies none: "crypto key generate
// rsa" refuses to run without a domain name, and a switch left without SSH
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
// The "no" is the one that matters: the default on that prompt saves the
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
//
// # THE ERROR IS THE SYSTEM NAME'S REFUSAL, AND NOTHING ELSE (2026-08-09)
//
// This returned a bare []string until the four `sanitizeHostname` spellings were
// collapsed into [driver.HostnameFromDeviceName]. That function REFUSES a name it
// cannot normalize instead of mangling it, and a builder with no error channel
// has exactly two ways to honour a refusal: drop the line, or say so. Dropping it
// silently is the mangle-doctrine in another costume — the operator asked for a
// name, the switch keeps `Switch`, and nothing anywhere says why. So the
// signature carries the reason out.
//
// THE REST OF THE PUSH IS STILL RETURNED ALONGSIDE THE ERROR, deliberately, and
// it is the same convention `adminFor` and the identity readers already follow in
// this tree: return what you COULD build next to the reason the rest failed. A
// switch left factory-reset and unreachable because its meshcraft name carries an
// accent is a far worse answer than a switch that is reachable, adopted and
// merely still called `Switch` — with the reason on the operator's screen. The
// caller sends the commands AND reports the error; it does not choose one.
//
// The mirror of this contract is web/src/pages/add/ciscoIosSoftReset.ts, which
// logs the refusal into the run log and pushes everything else. The builder is
// the written specification of that script (see the note above), so the shape of
// the refusal is part of what is being specified.
//
// ubiquiti/edgeswitch's MgmtPushCmds keeps the bare []string: that CLI has no
// `hostname` command at all, so its MgmtOpts carries no name that could refuse.
func MgmtPushCmds(opts MgmtOpts) ([]string, error) {
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

	// The system name, through the ONE normalizer. No local sanitizer any more:
	// the one that stood here dropped every character it did not recognise, kept
	// the `_` driver.ValidHostname forbids, never trimmed, and fell back to the
	// literal `switch` — four disagreements with the reconciled path, on a name
	// that ends up on the console banner and in the LLDP system-name TLV.
	var hostErr error
	if strings.TrimSpace(opts.Hostname) != "" {
		var host, refusal = driver.HostnameFromDeviceName(opts.Hostname)
		if refusal != "" {
			hostErr = errors.New(refusal)
		} else {
			out = append(out, "hostname "+host)
		}
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
		// A 2960-X has no routing table: "ip route" would be refused, and
		// "ip default-gateway" is the only form that works on an L2 switch.
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
		// "secret" and not "password": the second stores the value in clear
		// (or reversibly encoded as type 7) in the configuration.
		out = append(out, fmt.Sprintf("username %s privilege 15 secret 0 %s", user, pass))
	}
	// NO ENABLE PASSWORD — the fleet rule (cf. NoEnableCmds), applied AFTER the
	// privilege-15 account exists so the device is never left without a way in.
	out = append(out, NoEnableCmds()...)

	// PERSIST. Last, unconditionally, and it used to be absent entirely.
	//
	// The same defect the Arista builder carried: this produced a complete,
	// correct provisioning run — domain, RSA key, SSH, VLANs, SVI, address,
	// gateway, trunks, account — that lived ENTIRELY in the running
	// configuration and was gone at the next reload. Nothing reported it. Every
	// command was accepted, the switch was reachable, and it stayed that way
	// until somebody power-cycled it. Of the connectors that build a push, only
	// EdgeSwitch and the two FS ranges were saving; this one and EOS were not.
	//
	// # Why `write memory` and not the fallback
	//
	// [SaveCmdFallback] (`copy running-config startup-config`) ALWAYS asks
	// "Destination filename [startup-config]?", and a blind command list cannot
	// answer a prompt — the next line of the list would be typed in as the
	// answer. That is not hypothetical: it is the exact failure that cost the FS
	// connector a deployment, recorded in admin.go's header, where a `show run`
	// became a startup file name.
	//
	// `write memory` normally answers without asking anything. It is deprecated
	// on some IOS-XE builds, where it returns "% Invalid input" — one refused
	// line at the very end of a console transcript, visible, and leaving the
	// operator exactly where they were before this change. The interactive path
	// ([Client.save]) is what tries both and reads the answers; a command list
	// gets the form that does not prompt.
	//
	// `end` first: `write memory` is an EXEC command and is refused in
	// configuration mode.
	out = append(out, "end", SaveCmd)

	return out, hostErr
}

// NoEnableCmds enforces the fleet's universal rule: NO ENABLE PASSWORD.
//
// # Applied, not merely omitted
//
// Not writing one would leave the rule true on a factory Catalyst and false on
// every switch that already carried one — which is exactly the fleet where it
// matters. Both forms are removed because IOS has two and a device can carry
// either: `enable secret` (hashed) and the older `enable password` (type 7,
// trivially reversible). Removing only the first would leave the second in force
// and the rule quietly false.
//
// # THE IOS-SPECIFIC HAZARD, which the Arista connector does not have
//
// This is where the two platforms genuinely differ, and it is worth stating
// before anyone copies this across another range. On IOS, `enable` from a VTY
// (SSH / telnet) session with NO enable password configured is REFUSED:
//
//	% No password set
//
// From the CONSOLE the same command succeeds. So on a Catalyst, removing the
// enable secret makes `privilege 15` on the account not a convenience but the
// ONLY path to a privileged SSH session — an account that lands on ">" can
// never leave it over the network, and the recovery is the console port.
//
// That is survivable here, and only because of the ORDER: this connector always
// writes `username U privilege 15 secret 0 S` first, in the same block, and
// [Client.EnsureAccount] does the same. The account that remains is privileged at
// login and never traverses an enable prompt. [cliConn.enable] recognises the
// refusal by name so the failure, if it ever happens, says what it is instead of
// timing out.
func NoEnableCmds() []string {
	return []string{"no enable secret", "no enable password"}
}

// SaveCmd is the save this connector emits first; [SaveCmdFallback] is the one
// tried if IOS refuses it. "write memory" prompts for nothing, which is why
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
// from "show interfaces status" output. This is what the soft-reset uses to
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

// There is no sanitizeHostname here any more, and its removal is not tidying.
// It kept the `_` [driver.ValidHostname] forbids, dropped silently every other
// character it did not recognise, never trimmed the hyphens RFC 1123 rejects at
// the ends, and fell back to the literal `switch` on an empty result — four ways
// of disagreeing with the name the reconciliation loop would push to the same
// device an hour later. [MgmtPushCmds] now calls
// [driver.HostnameFromDeviceName], which is that loop's own normalizer, and
// refuses rather than mangling. Do not re-introduce a local one: the drift this
// ended started as exactly one such function, per connector, each reasonable on
// its own.

// Package edgeswitch is the Ubiquiti EdgeSwitch / UniFi Switch knowledge base,
// calibrated on the UniFi Switch 16 (EdgeSwitch firmware).
//
// # The status of what is written here, said before anything else
//
// The forms below come from the EdgeSwitch CLI Command Reference (431 pages).
// A first SSH session on a lab EdgePoint S16 (firmware 1.9.2, 2026-08-03)
// confirmed the shape of the reads and corrected two things the reference had
// led this package to assume — see testdata/, which holds those captures
// verbatim, and the pitfalls below. The WRITE path is still unexercised.
// That is the same situation the Cisco IOS connector was born in, and the same
// rule applies: what has not been seen is announced as such — ResetProcedure
// carries `Confirmed: false`, and every capability that would let a screen
// offer a dangerous button stays at its zero value.
//
// # This is FASTPATH, and that is the whole difficulty
//
// EdgeSwitch is a Broadcom FASTPATH derivative. It looks Cisco-ish and is not,
// and the differences are exactly where a driver written from muscle memory
// breaks:
//
//   - THE MODE OF A COMMAND IS NOT WHERE YOU EXPECT. "network parms",
//     "network protocol", "network mgmt_vlan", "vlan database",
//     "write memory" and "clear config" are all PRIVILEGED EXEC commands,
//     not Global Config ones. Sending them from "(Config)#" fails;
//   - management is NOT an SVI. There is no "interface vlan N" + "ip
//     address" for the switch's own address: FASTPATH has ONE network
//     interface, addressed with "network parms <ip> <mask> [gw]", and its
//     VLAN is chosen separately with "network mgmt_vlan N";
//   - port VLAN membership takes THREE commands, not one. "vlan pvid N" sets
//     the untagged VLAN, "vlan participation include N" makes the port a
//     member at all, and "vlan tagging N" decides whether it leaves tagged.
//     A port given a pvid without participation carries nothing;
//   - interfaces are "0/1", slot/port, with ranges as "0/1-0/4".
//
// # Serial adoption
//
// "write memory confirm" saves WITHOUT prompting, which is what makes this
// platform pleasant to script over a console — cf. [MgmtPushCmds], whose last
// line is exactly that.
package edgeswitch

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver identifies the Ubiquiti EdgeSwitch / UniFi Switch range.
//
// There is no read/write connector yet: this package feeds discovery
// fingerprinting, the reset-procedure screen, and the Web Serial adoption
// script, which must emit the exact forms of [MgmtPushCmds] and
// [FactoryResetCmds].
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:   "ubiquiti-edgeswitch",
		Vendor: "Ubiquiti",
		Family: "EdgeSwitch / UniFi Switch (FASTPATH)",
		// The CLI sits behind SSH authentication: nothing is identifiable
		// without a credential.
		IdentifyNeedsCredentials: true,
		// "http", not "web": this range HAS a web UI, but declaring the "web"
		// transport is a promise to implement driver.WebUIAddresser (enforced by
		// api.TestWebTransportConnectorsDeclareTheirWebUI), and nothing here can
		// address that UI yet.
		Transport: []string{"ssh", "serial", "http"},
		Doc: "EdgeSwitch CLI Command Reference " +
			"(https://dl.ubnt.com/guides/edgemax/EdgeSwitch_CLI_Command_Reference_UG.pdf)",
	}
}

var (
	// "(UBNT EdgeSwitch) #" is the prompt the reference prints throughout,
	// and the product names carry the range.
	// "EdgePoint" and "EP-S16" are here because of the lab unit: its login
	// banner says "Welcome to EdgeSwitch" but "show version" reports
	// "EdgePoint S16" / "EP-S16" — the same firmware under another product
	// name, and a fingerprint keyed only on the banner would miss it whenever
	// identification comes from SNMP sysDescr instead.
	reEdgeSwitch = regexp.MustCompile(`(?i)EdgeSwitch|UBNT EdgeSwitch|EdgePoint|EP-S\d+|US-\d+-\d+W|UniFi Switch`)
	reUbiquiti   = regexp.MustCompile(`(?i)Ubiquiti|UBNT`)
)

func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var hay = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reEdgeSwitch.MatchString(hay) {
		return 88
	}
	// A bare "Ubiquiti" covers EdgeRouter, UniFi APs and this range alike —
	// enough to stop a device falling into generic-ssh, not enough to claim it.
	if reUbiquiti.MatchString(hay) {
		return 45
	}
	return 0
}

// monitoring: through which channels this platform is observed.
//
// The product's periodic collector speaks only SNMP, so a channel declared
// under any other kind would be collected by nobody. NOTHING here has been
// walked on an EdgeSwitch: the channels rest on the standard MIBs the poller
// (internal/snmppoll) already queries and on the reference documenting an SNMP
// agent, which is not the same as verified. Each note says what is missing.
func monitoring() driver.MonitoringCapabilities {
	return driver.MonitoringCapabilities{
		Channels: []driver.MonitorChannel{
			{
				Kind:        "snmp",
				Metrics:     []string{"if_state", "if_counters"},
				MinInterval: 15 * time.Second,
				Note: "standard IF-MIB, documented but NOT walked on this hardware. The " +
					"ifDescr shape is unknown — verify it against the \"0/1\" slot/port " +
					"naming before trusting a sample to find its socket",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				Note: "standard LLDP-MIB (1.0.8802). EdgeSwitch documents \"lldp transmit\" / " +
					"\"lldp receive\" per interface; whether they are on by default is NOT " +
					"established here, so an empty branch may mean LLDP is off rather than absent",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"fdb"},
				MinInterval: 30 * time.Second,
				Note: "MEASURED (walk 2026-08-13 on the lab EP-S16): Q-BRIDGE-MIB " +
					"dot1qTpFdbPort serves the learned table with its VLAN arc (17 rows); " +
					"the flat dot1dTpFdbPort serves NOTHING on this agent — Q-BRIDGE is " +
					"not a preference here, it is the only door.",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"health"},
				MinInterval: 5 * time.Minute,
				Note: "sysUpTime is served by any SNMP agent. CPU and memory are NOT claimed: " +
					"the poller's OIDs for them are vendor-private or HOST-RESOURCES, and " +
					"neither has been walked on this platform",
			},
		},
		Traps:  false, // "snmptrap" exists in the reference, never exercised
		Syslog: false,
	}
}

func (d *Driver) Capabilities(model string) driver.Capabilities {
	return driver.Capabilities{
		// VLAN 1 is reserved for the default VLAN and the creatable range is
		// 2-4093 (reference: "vlan 2-4093"), which is NOT the 1-4094 most of
		// the fleet uses — a plan carrying VLAN 4094 would be refused here.
		VlanRange: [2]int{1, 4093},
		MaxVlans:  4093,
		Features: map[string]bool{"ssh": true, "http": true, "serial": true,
			// LACP: membership read AND written live on the lab EP-S16
			// (2026-08-13) — "addport 3/1" from interface 0/5, read back in
			// "show port-channel all", removed with "deleteport". The
			// platform pre-provisions slots 3/1-3/6; VLAN mapping on the slot
			// is not captured yet and the writer says so.
			"LACP": true},
		// The six pre-provisioned slots ARE the bound on this model.
		MaxLAGs:      6,
		PortsPerLAG:  8,
		STPModes:     []string{"rstp", "mstp"},
		SNMPVersions: []string{"v1", "v2c"},
		MaxSessions:  5,
		// FASTPATH separates running from startup: apply, verify, THEN save
		// with "write memory".
		Commit:     driver.CommitApplyThenSave,
		Monitoring: monitoring(),
		Mgmt: driver.MgmtCapabilities{
			// The management VLAN is chosen with "network mgmt_vlan", whose
			// documented range is 1-4093.
			VlanIDs:   driver.VlanAny,
			VlanRange: [2]int{1, 4093},
			TaggedOK:  true,
			Static:    true, // network parms <ip> <mask> [gw]
			DHCP:      true, // network protocol dhcp
			Gateway:   true, // third argument of network parms
			OutOfBand: true, // RJ45 console
			// ApplySameVLAN stays FALSE: nothing here has been run on hardware.
			// This is the capability that decides whether a screen offers a
			// button that can make a device mute.
			ApplySameVLAN: false,
			Notes: []string{
				"management is NOT an SVI on this platform: there is one network " +
					"interface, addressed by \"network parms <ip> <mask> [gw]\" in " +
					"PRIVILEGED EXEC — not \"interface vlan N\" + \"ip address\"",
				"the management VLAN is set separately, \"network mgmt_vlan N\", also " +
					"in privileged EXEC",
				"\"network parms\" requires the address and the gateway to be on the " +
					"SAME subnet — the device refuses the pair otherwise",
			},
		},
		Admin: driver.AdminAccount{
			Custom: true,
			Notes: []string{
				"username <name> password <secret> level 15",
				"factory credentials on this range are ubnt/ubnt — DOCUMENTED by " +
					"Ubiquiti, not captured here",
			},
		},
	}
}

func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Software: "enable; clear config → confirm \"y\" (resets the configuration to " +
			"factory defaults WITHOUT rebooting); then write memory confirm, or reload " +
			"if a clean boot is wanted",
		Hardware: "Reset button held ~10 s with the switch powered on, per the model's " +
			"quick-start guide — verify the duration on the product sheet",
		Erases: []string{
			"running configuration (returned to factory defaults)",
			"the management address: the switch falls back to its factory address or DHCP",
		},
		AfterReset: []string{
			"console 115200 8N1 on this range — \"serial baudrate\" can change it, so a " +
				"switch that was re-baud-rated needs its configured speed",
			"factory credentials ubnt/ubnt (documented by Ubiquiti, not captured here)",
			"push management via MgmtPushCmds / serial adoption",
		},
		Pitfalls: []string{
			"\"clear config\" does NOT reboot the switch — it resets the running " +
				"configuration in place, and the change is not persistent until " +
				"\"write memory\"",
			"\"enable\" ASKS FOR A PASSWORD (captured 2026-08-03 on EdgePoint S16 " +
				"1.9.2): \"(UBNT EdgeSwitch) >enable\" is answered by \"Password:\", and " +
				"an empty answer is refused. The login password works",
			"THERE IS NO \"hostname\" COMMAND on this CLI: \"hostname <name>\" in Global " +
				"Config answers \"% Invalid input detected at '^' marker\" (captured " +
				"2026-08-05 on EdgePoint S16). The system name is \"snmp-server sysname " +
				"<name>\" in Global Config, and THAT is what drives the LLDP System Name " +
				"TLV (captured 2026-08-10: \"System Name: UBNT EdgeSwitch\" became " +
				"\"System Name: mctest1\"). It does NOT change the CLI prompt, which stays " +
				"\"(UBNT EdgeSwitch) #\" — \"set prompt\" is a separate, uncaptured " +
				"gesture and is deliberately not sent (cf. hostname.go)",
			"the pager is killed by \"terminal length 0\", NOT \"no pagination\" — the " +
				"latter answers \"% Invalid input detected at '^' marker\" despite the " +
				"reference. This CLI does print the IOS-style \"% Invalid input\" form",
			"\"show port all\" lists LAG interfaces in slot 3 (3/1..3/6) alongside the " +
				"physical sockets in slot 0 — a parser that takes every row as a faceplate " +
				"port invents six jacks that do not exist",
			"the lab unit ships with spanning tree in IEEE 802.1s (MSTP) mode, not RSTP",
			"MODE TRAPS: \"network parms\", \"network protocol\", \"network mgmt_vlan\", " +
				"\"vlan database\", \"write memory\" and \"clear config\" are PRIVILEGED " +
				"EXEC commands, NOT Global Config ones — sending them from \"(Config)#\" fails",
			"management is not an SVI: \"interface vlan N\" + \"ip address\" is the wrong " +
				"platform's gesture and does not address this switch",
			"a port needs THREE commands to carry a VLAN: \"vlan pvid N\" alone is not " +
				"membership — \"vlan participation include N\" is, and \"vlan tagging N\" " +
				"decides tagged versus untagged",
			"VLAN 1 is reserved and the creatable range is 2-4093, not 1-4094",
			"\"write memory confirm\" saves without prompting; plain \"write memory\" " +
				"asks, and a script that does not answer hangs on the prompt",
			"the uplink must carry the management VLAN or the probe that follows the " +
				"push cannot reach the new address",
		},
		// NOTHING here has been run on an EdgeSwitch by this package.
		Confirmed: false,
		Source:    "EdgeSwitch CLI Command Reference — DOCUMENTED, not captured on hardware",
	}
}

// --- CLI builders (serial adoption and any future SSH path share these) ---

// MgmtOpts is what adoption needs on the wire.
type MgmtOpts struct {
	VlanID   int
	VlanName string // default LAN<id>_Management
	IP       string
	Mask     string // dotted decimal — "network parms" takes no CIDR form
	Gateway  string
	Username string
	Password string
	// NOTE: there is deliberately NO Hostname field, and the 2026-08-10 capture
	// does not change that. This FASTPATH CLI has no `hostname` command — the
	// deduced form was sent to the lab EdgePoint S16 on 2026-08-05 and refused
	// with "% Invalid input detected at '^' marker." The system name IS
	// settable, by "snmp-server sysname" (cf. hostname.go, Client.SetHostname),
	// but that is the reconciliation loop's gesture on an adopted device, not
	// first-contact provisioning: this builder is the written specification of the
	// Web Serial script, and a field here would ask that script to carry a second
	// naming path meshcraft would then have to keep in step with `devices.name`.
	// TrunkPorts is NO LONGER what decides where the management VLAN goes:
	// provisioning trunks every port (see MgmtPushCmds). Kept so a caller can
	// record which socket was cabled.
	TrunkPorts string
}

// FactoryResetCmds is the soft factory-reset sequence (privileged EXEC).
//
// The caller answers the confirmation with "y". Note that this does NOT
// reboot: the switch keeps running with a default configuration, which is why
// the address it answered on is gone the moment the command lands.
func FactoryResetCmds() []string {
	return []string{
		"enable",
		"clear config",
	}
}

// MgmtPushCmds returns the lines that bring a factory EdgeSwitch to a
// reachable management state.
//
// The ORDER encodes the platform's mode structure, which is the thing to get
// right: privileged EXEC for the network/VLAN-database gestures, Global Config
// for the interface work, and back out to privileged EXEC to save.
func MgmtPushCmds(opts MgmtOpts) []string {
	var vlanID = opts.VlanID
	if vlanID < 1 {
		vlanID = 1
	}
	var mask = strings.TrimSpace(opts.Mask)
	if mask == "" {
		mask = "255.255.255.0"
	}
	var name = strings.TrimSpace(opts.VlanName)
	if name == "" {
		name = fmt.Sprintf("LAN%d_Management", vlanID)
	}
	name = sanitizeVlanName(name)

	var out []string
	out = append(out, "enable")

	// 1) VLAN database — privileged EXEC, NOT Global Config. VLAN 1 always
	// exists and is never created.
	if vlanID != 1 {
		out = append(out,
			"vlan database",
			fmt.Sprintf("vlan %d", vlanID),
			fmt.Sprintf("vlan name %d %s", vlanID, name),
			"exit",
		)
	}

	// 2) The switch's own address. One network interface, no SVI.
	out = append(out, "network protocol none")
	var parms = fmt.Sprintf("network parms %s %s", opts.IP, mask)
	var gw = strings.TrimSpace(opts.Gateway)
	if gw != "" {
		parms += " " + gw
	}
	out = append(out, parms)
	if vlanID != 1 {
		out = append(out, fmt.Sprintf("network mgmt_vlan %d", vlanID))
	}

	// 3) Global Config for the uplink and the account. NO name of any kind: this
	// CLI has no `hostname` command — the lab EdgePoint S16 answered "% Invalid
	// input detected at '^' marker." on 2026-08-05 — and the verb that DOES work,
	// "snmp-server sysname" (captured 2026-08-10), is deliberately not emitted
	// here either: naming is Client.SetHostname's job, driven by `devices.name`
	// through the reconciliation loop. Do not re-add either line here; the whole
	// account is in hostname.go.
	out = append(out, "configure")

	if vlanID != 1 {
		// PROVISIONING RULE: every port becomes a trunk carrying every VLAN.
		//
		// At first provisioning meshcraft does not know which socket the uplink
		// is in, and a guess is the one thing that cannot be made reliable — the
		// first real run of the serial script configured one guessed port and
		// the management VLAN never reached the fibre. "all" is a genuine
		// Global Config form here, not a loop: it configures every interface at
		// once. VLAN 1 remains the untagged native everywhere (default pvid), so
		// an access device on any port keeps working while the management VLAN
		// rides tagged alongside it.
		out = append(out,
			fmt.Sprintf("vlan participation all include %d", vlanID),
			fmt.Sprintf("vlan port tagging all %d", vlanID),
		)
	}

	// Credentials LAST: whatever the platform does when the logged-in
	// account's password changes, everything else is already in place when it
	// happens (the discipline the FS S3900 deploy of 2026-08-01 taught).
	var user = strings.TrimSpace(opts.Username)
	var pass = opts.Password
	if user != "" && pass != "" {
		out = append(out, fmt.Sprintf("username %s password %s level 15", user, pass))
	}
	out = append(out, "exit")

	// 4) Save, from privileged EXEC. "confirm" is what stops the device
	// asking a question a script would hang on.
	out = append(out, "write memory confirm")
	return out
}

// PortCmds returns the Interface Config lines that put one port on a VLAN.
//
// Access and trunk differ only by "vlan tagging": FASTPATH has no
// "switchport mode", so the shape of the port is the sum of these three
// commands rather than a keyword.
func PortCmds(port string, pvid int, tagged []int) []string {
	var out = []string{"interface " + port}
	if pvid > 0 {
		out = append(out,
			fmt.Sprintf("vlan pvid %d", pvid),
			fmt.Sprintf("vlan participation include %d", pvid),
		)
	}
	var id int
	for _, id = range tagged {
		if id == pvid {
			// The native VLAN is carried untagged; tagging it too would make
			// the port emit both forms for one VLAN.
			continue
		}
		out = append(out,
			fmt.Sprintf("vlan participation include %d", id),
			fmt.Sprintf("vlan tagging %d", id),
		)
	}
	out = append(out, "exit")
	return out
}

// PortName renders a slot/port pair the way this CLI takes it.
func PortName(slot, port int) string { return fmt.Sprintf("%d/%d", slot, port) }

// rePortName matches the platform's own spelling, "0/12" — the CLI form,
// which is also what SNMP ifName (1.3.6.1.2.1.31.1.1.1.1) serves.
var rePortName = regexp.MustCompile(`^\s*(\d+)/(\d+)\s*$`)

// reIfDescrName matches the OTHER spelling the same hardware serves over
// SNMP: ifDescr (1.3.6.1.2.1.2.2.1.2) says "Slot: 0 Port: 18 10G - Level",
// and ifDescr — not ifName — is the label the poller attaches to every sample
// (snmppoll.Samples). CAPTURED on the lab EdgePoint S16 (firmware 1.9.2,
// 2026-08-05, testdata/snmp_iftable.txt): before this form was accepted,
// every SNMP sample failed facade resolution and the whole faceplate read as
// free — including the uplink whose LLDP neighbor proved the link alive.
//
// Anchored at the start on purpose: the CPU interface's ifDescr is
// "CPU Interface for Slot: 5 Port: 1", which CONTAINS the pattern and
// names no front-panel socket.
var reIfDescrName = regexp.MustCompile(`^Slot: (\d+) Port: (\d+)\b`)

// StandardPortName maps a name observed on the platform to the standard model.
//
// A UniFi Switch 16 carries sixteen RJ45 sockets plus two SFP cages, all in
// slot 0 and numbered continuously (0/1..0/18), so ONE prefix is enough —
// unlike the Catalyst or the FS 48T4SP, which carry the same number twice.
//
// TWO spellings are accepted, because the platform itself uses two: the CLI
// and SNMP ifName say "0/18", SNMP ifDescr says "Slot: 0 Port: 18 10G -
// Level". Telemetry resolution (facade.resolve, step 4) hands this function
// the ifDescr form — refusing it silently unplugged every port of the SNMP
// monitoring path while the CLI path read the same links fine.
//
// Refuses anything that is not a physical socket — LAG members answer as
// slot 3 ("3/1") and the CPU interface as slot 5, both non-zero — and
// refuses names that are already standard: "GE12" is not a spelling this
// platform utters.
func (d *Driver) StandardPortName(deviceName string) (string, bool) {
	var m = rePortName.FindStringSubmatch(deviceName)
	if m == nil {
		m = reIfDescrName.FindStringSubmatch(deviceName)
	}
	if m == nil {
		return "", false
	}
	var slot, serr = strconv.Atoi(m[1])
	var port, perr = strconv.Atoi(m[2])
	if serr != nil || perr != nil || port < 1 || slot != 0 {
		return "", false
	}
	return fmt.Sprintf("GE%d", port), true
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
	// "vlan name" takes at most 32 characters on this platform.
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

// There is no sanitizeHostname here any more, and its removal is not tidying.
// It existed only to feed the `hostname` line MgmtPushCmds no longer builds, and
// it also contradicted this package's stated policy: hostname.go refuses an
// invalid name outright (driver.ValidHostname) rather than mangling it, precisely
// because a switch answering to a name meshcraft did not choose breaks the LLDP
// correlation the gesture exists for. When a real form for this range is captured
// (`set prompt` / `snmp-server sysname` are the suspects), it inherits that
// refusal, not a silent rewrite.

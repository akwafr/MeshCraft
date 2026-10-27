// Package s3900 is the FS.com S3900 / S3900-R connector knowledge base.
//
// CLI strings below were OBSERVED on lab OpCode 1.7.0 (serial + SSH share the
// same command set). Soft-reset in the browser and any future SSH apply path
// must emit these exact forms — "ip sshd", "ip address A B", "write" and
// "vlan N" without "media ethernet" fail on this platform.
package s3900

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

// Driver identifies FS S3900 / S3900-R and reaches it over SSH (contract.go —
// the product's first CLI connector). Soft-reset pushes mgmt over Web Serial
// using [MgmtPushCmds]; both paths speak the same verified FSOS 1.7 command
// set, and a firmware whose CLI is not recognized gets reads only.
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:      "fs-s3900",
		Vendor:    "FS.com",
		Family:    "S3900 / S3900-R",
		Transport: []string{"ssh", "serial", "http"},
		// The CLI sits behind SSH authentication: nothing is identifiable
		// without a credential.
		IdentifyNeedsCredentials: true,
		Doc: "FS S3900 Series CLI — reset guide + lab OpCode 1.7.0 " +
			"(https://img-en.fs.com/file/user_manual/s3900-series-switches-reset-and-recovery-system-configuration-guide.pdf)",
	}
}

var (
	reS3900 = regexp.MustCompile(`(?i)S3900|FSOS|Factory_Default_Config|Operation Code Version`)
	reFS    = regexp.MustCompile(`(?i)\bFS\.?com\b|Fiberstore`)
)

// monitoring: through which channels this platform is observed.
//
// The product's periodic collector speaks only SNMP: a channel declared under
// any other kind would be collected by nobody. The metrics are those of the
// STANDARD MIBs the poller queries (internal/snmppoll): if_state/if_counters
// = IF-MIB, lldp = LLDP-MIB 1.0.8802 — nothing proprietary. `health` and
// `optical_power` are NOT declared: their OIDs on the poller side are
// MikroTik-specific, and nothing equivalent has been captured on FSOS.
//
// The floors are conservative: the supervision CPU of an access switch in
// this line is modest, and a v2c agent polled too hard answers less and less.
// Tighten on a capture, not on a whim.
func monitoring() driver.MonitoringCapabilities {
	return driver.MonitoringCapabilities{
		Channels: []driver.MonitorChannel{
			{
				Kind:        "snmp",
				Metrics:     []string{"if_state", "if_counters"},
				MinInterval: 15 * time.Second,
				Note:        "standard IF-MIB; accessible as soon as a v2c community is set (ApplySNMP)",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"lldp"},
				MinInterval: 30 * time.Second,
				Note: "standard LLDP-MIB (1.0.8802) — walk not yet captured on OpCode 1.7: " +
					"if the agent serves nothing under this branch, the LLDP screen will say so (not_served)",
			},
			{
				Kind:        "snmp",
				Metrics:     []string{"fdb"},
				MinInterval: 30 * time.Second,
				Note: "MEASURED (walk 2026-08-13 on the lab S3900-24T4S, OpCode 1.7.0): " +
					"Q-BRIDGE-MIB dot1qTpFdbPort serves the learned table with its VLAN arc " +
					"(20 rows), dot1dBasePortIfIndex maps the 28 bridge ports.",
			},
			{
				Kind:    "snmp",
				Metrics: []string{"health"},
				// Uptime moves slowly and is the only thing this metric actually
				// yields here; five minutes is plenty and costs the agent nothing.
				MinInterval: 5 * time.Minute,
				Note: "UPTIME ONLY on OpCode 1.7.0. sysUpTime (1.3.6.1.2.1.1.3) answers, " +
					"and it is the whole of what this channel collects on this platform: " +
					"HOST-RESOURCES-MIB is NOT served (walks of hrProcessorLoad " +
					"1.3.6.1.2.1.25.3.3.1.2 and hrStorage 1.3.6.1.2.1.25.2.3.1 both returned " +
					"nothing, 2026-08-02), so no CPU and no memory — cf. the Unavailable " +
					"declarations below",
			},
		},
		Traps:  false, // "snmp-server host" never captured on 1.7 — verify after the upgrade
		Syslog: false,
		// What this platform does NOT expose, with the reading that establishes
		// it. Said out loud so the screen answers "why is there no CPU curve?"
		// instead of leaving an operator waiting for a release that would never
		// change anything (cf. driver.MetricUnavailable).
		//
		// NOTE the deliberate asymmetry with the `health` channel above: that
		// channel IS declared, because sysUpTime answers. These entries name the
		// metrics the poller asks for under it that this firmware leaves empty.
		Unavailable: []driver.MetricUnavailable{
			{
				Metric:   "optical_power",
				Observed: true,
				Source: "SNMP walk through the proxy on OpCode 1.7.0 (2026-08-02): the " +
					"poller's optical OIDs are MikroTik-proprietary (mtxrOptical, " +
					"1.3.6.1.4.1.14988.1.1.19) and this platform is enterprise 52642 " +
					"(sysObjectID 1.3.6.1.4.1.52642.2.1.45.101). Nothing equivalent was " +
					"found under its private tree",
			},
		},
	}
}

func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var hay = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reS3900.MatchString(hay) {
		return 88
	}
	if reFS.MatchString(hay) {
		return 50
	}
	return 0
}

func (d *Driver) Capabilities(model string) driver.Capabilities {
	return driver.Capabilities{
		VlanRange: [2]int{1, 4094},
		MaxVlans:  4094,
		Features: map[string]bool{"ssh": true, "http": true, "serial": true,
			// LACP: STATIC groups read AND written live on s3900lab (2026-08-13)
			// — group-first, member joined with the bare "channel-group 1",
			// "Group Type: Static" read back. Pinned LACP groups are NOT
			// expressible on this train (mode active is a syntax error; per-port
			// "lacp" forms dynamic groups) and the writer refuses that mode
			// with its sentence.
			"LACP": true},
		// ECS bounds on this range: 13 groups of up to 8 (26 ports / 2).
		MaxLAGs:      13,
		PortsPerLAG:  8,
		STPModes:     []string{"stp", "rstp"},
		SNMPVersions: []string{"v1", "v2c"},
		MaxSessions:  5,
		// 1.7 distinguishes running from startup: apply, verify, THEN save
		// (copy running-config startup-config + filename prompt).
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
			Routed:    true,
			IPARP:     true,
			OutOfBand: true, // console serial
			// ApplySameVLAN: the write forms ("ip add A.B.C.D/nn" on the SVI,
			// "ip route 0.0.0.0 0.0.0.0 GW", "dns name-server") were RUN
			// successfully on hardware (serial soft reset on 2026-08-02,
			// OpCode 1.7.0); SSH shares this same command set.
			ApplySameVLAN: true,
			Notes: []string{
				"changing the address of the SVI the session came in through cuts the session: " +
					"verify afterward through the NEW address",
			},
		},
		Admin: driver.AdminAccount{
			Custom: true,
			Notes: []string{
				"username U privilege 15 ; username U password 0 SECRET",
				"enable password 0 SECRET",
			},
		},
	}
}

func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Software: "conf t; boot system config:Factory_Default_Config.cfg; exit; " +
			"delete file unit 1 name startup1.cfg; restart (or reboot/reload) → y",
		Hardware: "Reset button per model sheet; monitor/Ctrl+P recovery on some builds",
		Erases:   []string{"startup configuration", "boot points at Factory_Default_Config.cfg"},
		AfterReset: []string{
			"115200 8N1 console",
			"login admin/admin (observed on OpCode 1.7.0 after reset; empty password refused)",
			"push mgmt via MgmtPushCmds / soft-reset",
		},
		Pitfalls: []string{
			"Need \"vlan N name X media ethernet\", not bare \"vlan N\"",
			"\"interface vlan N\" (space), not VlanN",
			"\"ip add A.B.C.D/nn\", not \"ip address A B\"",
			"\"ip ssh server enable\", not ip sshd",
			"\"username U privilege 15\" then \"username U password 0 S\"",
			"save with \"copy running-config startup-config\", not write",
			"save prompts \"Startup configuration file name:\" — answer a name " +
				"(startup1.cfg), NEVER Factory_Default_Config.cfg",
			"uplink must carry mgmt VLAN (trunk allowed vlan add N) or probe fails",
			"SFP+ links can take tens of seconds after boot — retry \"show " +
				"interfaces brief\" before concluding no uplink (deploy 2026-08-01)",
			"console session can drop around the logged-in account's password " +
				"change — push credentials LAST and be ready to re-login (deploy 2026-08-01)",
			"failed login shows \"---- Press ENTER to start CLI session ----\", " +
				"not Username: — send CR first",
		},
		Confirmed: true,
		Source:    "lab OpCode 1.7.0 serial session + FS reset guide",
	}
}

// --- CLI builders (serial soft-reset and SSH share these) ---

// MgmtOpts is what soft-reset / onboarding needs on the wire.
type MgmtOpts struct {
	VlanID         int
	VlanName       string // default LAN<id>_Management
	IP             string
	PrefixLen      int // e.g. 16 for /16
	Gateway        string
	Username       string
	Password       string
	EnablePassword string
	// Hostname is the device's MESHCRAFT NAME, not an already-normalized system
	// name: [MgmtPushCmds] runs it through [driver.HostnameFromDeviceName] — the
	// same function api.hostnameheal uses — so a switch provisioned from a
	// console and the same switch reconciled half an hour later cannot end up
	// carrying two different names. A name that cannot be normalized is REFUSED,
	// never mangled: MgmtPushCmds returns the reason and omits the line.
	Hostname string
	// RemoveVlan1 drops the factory DHCP SVI (lab: "no interface vlan 1").
	RemoveVlan1 bool
	// TrunkPorts e.g. "Eth 1/25-28" — empty skips trunk membership.
	TrunkPorts string
}

// FactoryResetCmds is the soft factory-reset sequence (privileged EXEC).
func FactoryResetCmds() []string {
	return []string{
		"conf t",
		"boot system config:Factory_Default_Config.cfg",
		"exit",
		"delete file unit 1 name startup1.cfg",
		// Caller tries reboot → restart → reload and answers y.
		"restart",
	}
}

// MgmtPushCmds returns configure-mode lines that bring a factory S3900 to a
// reachable management state (same commands over serial or SSH).
//
// THE ERROR IS THE SYSTEM NAME'S REFUSAL AND NOTHING ELSE, and the rest of the
// push is returned alongside it: [driver.HostnameFromDeviceName] refuses a name
// it cannot normalize, a builder returning a bare []string could only honour
// that refusal by dropping the line in silence, and a silently dropped hostname
// is the same lie as a mangled one. The argument is written out once, over
// cisco/ios's MgmtPushCmds. The mirror is web/src/pages/add/fsS3900SoftReset.ts,
// which logs the reason and pushes everything else.
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
	// CLI rejects some punctuation in names; keep alnum + underscore.
	name = sanitizeVlanName(name)

	var out []string
	out = append(out, "conf t")

	// The system name, through the ONE normalizer — see the note where this
	// package's sanitizeHostname used to be.
	var hostErr error
	if strings.TrimSpace(opts.Hostname) != "" {
		var host, refusal = driver.HostnameFromDeviceName(opts.Hostname)
		if refusal != "" {
			hostErr = errors.New(refusal)
		} else {
			out = append(out, "hostname "+host)
		}
	}

	if vlanID != 1 {
		out = append(out,
			"vlan database",
			fmt.Sprintf("vlan %d name %s media ethernet", vlanID, name),
			"exit",
		)
	}

	out = append(out,
		fmt.Sprintf("interface vlan %d", vlanID),
		fmt.Sprintf("ip add %s/%d", opts.IP, prefix),
		"exit",
	)

	var gw = strings.TrimSpace(opts.Gateway)
	if gw != "" {
		// Lab running-config uses ip route, not ip default-gateway.
		out = append(out, fmt.Sprintf("ip route 0.0.0.0 0.0.0.0 %s", gw))
	}

	out = append(out,
		"ip http server",
		"ip ssh server enable",
	)

	var ports = strings.TrimSpace(opts.TrunkPorts)
	if ports != "" && vlanID > 0 {
		out = append(out,
			"interface "+ports,
			"switchport mode trunk",
			fmt.Sprintf("switchport trunk allowed vlan add %d", vlanID),
			"exit",
		)
	}

	if opts.RemoveVlan1 && vlanID != 1 {
		out = append(out, "no interface vlan 1")
	}

	// Credentials LAST: the 2026-08-01 deploy lost the console session right
	// around these commands (target account = logged-in factory "admin").
	// Whatever the platform does on the logged-in account's password change,
	// everything else is already in place when it happens, and the caller can
	// re-login with the NEW secret to finish the save.
	var user = strings.TrimSpace(opts.Username)
	var pass = opts.Password
	if user != "" && pass != "" {
		out = append(out,
			fmt.Sprintf("username %s privilege 15", user),
			fmt.Sprintf("username %s password 0 %s", user, pass),
		)
	}
	var en = opts.EnablePassword
	if en == "" {
		en = pass
	}
	if en != "" {
		out = append(out, fmt.Sprintf("enable password 0 %s", en))
	}

	out = append(out,
		"end",
		"copy running-config startup-config",
		// Lab OpCode 1.7.0: the device prompts "Startup configuration file
		// name:" (no y/n). The answer is a LINE, always explicit — accepting a
		// default could overwrite Factory_Default_Config.cfg, the file the soft
		// reset boots from. Success reads "Flash programming completed. /
		// Success.".
		StartupConfigFile,
	)
	return out, hostErr
}

// StartupConfigFile is the name answered to the save prompt of
// "copy running-config startup-config" (see MgmtPushCmds).
const StartupConfigFile = "startup1.cfg"

// ParseUpEthernetPorts extracts "Eth U/P" rows whose Link-Status is Up from
// "show interfaces brief" output (lab OpCode 1.7).
func ParseUpEthernetPorts(brief string) []string {
	var re = regexp.MustCompile(`(?im)^Eth\s+(\d+)\s*/\s*(\d+)\s+[^\n]*?\bUp\s+Up\b`)
	var seen = map[string]bool{}
	var out []string
	var matches = re.FindAllStringSubmatch(brief, -1)
	var i int
	for i = range matches {
		var m = matches[i]
		var key = fmt.Sprintf("Eth %s/%s", m[1], m[2])
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// CompactEthRange turns ["Eth 1/25","Eth 1/26","Eth 1/27","Eth 1/28"] into
// "Eth 1/25-28" when contiguous on the same unit; otherwise returns "".
func CompactEthRange(ports []string) string {
	if len(ports) == 0 {
		return ""
	}
	var unit = -1
	var nums []int
	var p string
	for _, p = range ports {
		var m = regexp.MustCompile(`(?i)^Eth\s+(\d+)/(\d+)$`).FindStringSubmatch(p)
		if m == nil {
			return ""
		}
		var u, _ = strconv.Atoi(m[1])
		var n, _ = strconv.Atoi(m[2])
		if unit < 0 {
			unit = u
		} else if u != unit {
			return ""
		}
		nums = append(nums, n)
	}
	var i int
	for i = 1; i < len(nums); i++ {
		if nums[i] != nums[i-1]+1 {
			return ""
		}
	}
	if len(nums) == 1 {
		return fmt.Sprintf("Eth %d/%d", unit, nums[0])
	}
	return fmt.Sprintf("Eth %d/%d-%d", unit, nums[0], nums[len(nums)-1])
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
	return out
}

// There is no sanitizeHostname here any more, and its removal is not tidying.
// It kept the `_` [driver.ValidHostname] forbids, silently dropped every other
// character it did not recognise, never trimmed the hyphens RFC 1123 rejects at
// the ends, and fell back to the literal `switch` on an empty result — four ways
// of disagreeing with the name the reconciliation loop pushes to the same device
// through hostname.go. [MgmtPushCmds] now calls
// [driver.HostnameFromDeviceName], the loop's own normalizer, which refuses
// rather than mangling. Do not re-introduce a local one: this drift began as
// exactly one such function per connector, each reasonable on its own.

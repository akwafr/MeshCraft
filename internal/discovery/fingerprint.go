package discovery

import (
	"regexp"
	"strings"
)

// Matcher links a textual clue to a driver. The order of the slice matters:
// the first pattern that matches wins, so the most specific entries come
// first.
type Matcher struct {
	Driver string
	Vendor string
	Re     *regexp.Regexp
	// PageCmd disables interactive pagination, Ident identifies, Config
	// exports the current configuration. Empty for the generic drivers.
	PageCmd    string
	IdentCmd   string
	ConfigCmd  string
	ConfigNote string
}

func re(p string) *regexp.Regexp { return regexp.MustCompile("(?i)" + p) }

// matchers: recognition of common platforms.
//
// These entries are used to IDENTIFY the device and to read its
// configuration. They are not yet write connectors: driving the
// configuration of a Cisco or an Aruba requires the phase-2 work (see
// docs/01-Architecture/Connecteurs).
var matchers = []Matcher{
	// Aruba Instant On: BEFORE the generic HPE/Aruba entries, otherwise
	// "hpe-procurve" would catch the range. These switches are WEB-ONLY
	// (neither SSH nor Telnet): no CLI command to send them, hence the empty
	// fields.
	// Connector: internal/driver/instanton.
	{Driver: "aruba-instant-on-1930", Vendor: "Aruba (HPE)",
		Re:         re(`Instant\s?On`),
		ConfigNote: "Web-only range: the configuration is read through the web interface, not the CLI."},
	{Driver: "cisco-nxos", Vendor: "Cisco", Re: re(`NX-OS|Nexus`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "cisco-ios", Vendor: "Cisco", Re: re(`Cisco IOS|IOS-XE|IOS Software|Catalyst|WS-C\d|C9[23]00`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "aruba-cx", Vendor: "Aruba", Re: re(`ArubaOS-CX|AOS-CX|Aruba JL\d`),
		PageCmd: "no page", IdentCmd: "show system", ConfigCmd: "show running-config"},
	{Driver: "hpe-procurve", Vendor: "HPE", Re: re(`ProCurve|Aruba|HP J\d|HPE OfficeConnect`),
		PageCmd: "no page", IdentCmd: "show system-information", ConfigCmd: "show running-config"},
	{Driver: "juniper-junos", Vendor: "Juniper", Re: re(`JUNOS|Juniper`),
		PageCmd: "set cli screen-length 0", IdentCmd: "show version",
		ConfigCmd: "show configuration | display set"},
	// SwOS BEFORE RouterOS, and this is not a mere detail of ordering: its
	// page title is "MikroTik SwOS", which the RouterOS pattern used to catch
	// through its `MikroTik` alternative. A CSS unit therefore ended up
	// classified as `mikrotik-routeros`, and the connector talked REST to it —
	// an API that SwOS does not have. These two systems share a vendor and
	// almost nothing else.
	// Connector: internal/driver/mikrotik/swos.
	{Driver: "mikrotik-swos", Vendor: "MikroTik", Re: re(`SwOS`),
		ConfigNote: "Web-only range: the configuration is read through the web interface's " +
			"`*.b` pages (HTTP Digest authentication), not the CLI."},
	// Connector: internal/driver/mikrotik/routeros.
	{Driver: "mikrotik-routeros", Vendor: "MikroTik", Re: re(`RouterOS|ROSSSH|MikroTik`),
		IdentCmd: "/system resource print", ConfigCmd: "/export terse"},
	{Driver: "ubiquiti", Vendor: "Ubiquiti", Re: re(`UniFi|EdgeSwitch|EdgeOS|Ubiquiti`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "extreme-exos", Vendor: "Extreme", Re: re(`ExtremeXOS|EXOS|Extreme Networks`),
		PageCmd: "disable clipaging", IdentCmd: "show version", ConfigCmd: "show configuration"},
	{Driver: "huawei-vrp", Vendor: "Huawei", Re: re(`Huawei|VRP \(|Versatile Routing`),
		PageCmd: "screen-length 0 temporary", IdentCmd: "display version",
		ConfigCmd: "display current-configuration"},
	{Driver: "h3c-comware", Vendor: "H3C", Re: re(`H3C|Comware`),
		PageCmd: "screen-length disable", IdentCmd: "display version",
		ConfigCmd: "display current-configuration"},
	{Driver: "dell-os10", Vendor: "Dell", Re: re(`Dell EMC|OS10|PowerConnect|Force10`),
		PageCmd: "terminal length 0", IdentCmd: "show version", ConfigCmd: "show running-configuration"},
	{Driver: "fortiswitch", Vendor: "Fortinet", Re: re(`FortiSwitch|FortiOS`),
		IdentCmd: "get system status", ConfigCmd: "show"},
	// FS.com S3900 — BEFORE the overly broad matchers. Ident/config observed
	// in lab testing, OpCode 1.7.
	// Connector: internal/driver/fs/s3900 (CLI builders; Web Serial soft-reset).
	// PageCmd: lab testing (console) shows "show run" un-paginated, but a
	// "--More--" over SSH would block the shell until the budget runs out and
	// make login_ok false — "terminal length 0" only costs an error echo if
	// the firmware doesn't support it.
	{Driver: "fs-s3900", Vendor: "FS.com",
		Re:        re(`S3900|Factory_Default_Config|Operation Code Version`),
		PageCmd:   "terminal length 0",
		IdentCmd:  "show version",
		ConfigCmd: "show run"},
	{Driver: "zyxel", Vendor: "Zyxel", Re: re(`Zyxel|ZyNOS`),
		IdentCmd: "show system-information", ConfigCmd: "show running-config"},
	{Driver: "netgear", Vendor: "Netgear", Re: re(`Netgear|NETGEAR|GS[13]\d{2}|M4[13]00`),
		IdentCmd: "show version", ConfigCmd: "show running-config"},
	{Driver: "tplink", Vendor: "TP-Link", Re: re(`TP-?Link|T[12][56]00G`),
		IdentCmd: "show system-info", ConfigCmd: "show running-config"},
	{Driver: "dlink", Vendor: "D-Link", Re: re(`D-Link|DGS-|DES-`),
		IdentCmd: "show switch", ConfigCmd: "show config current_config"},
	{Driver: "cumulus-sonic", Vendor: "Linux", Re: re(`Cumulus|SONiC`),
		IdentCmd: "cat /etc/os-release", ConfigCmd: "cat /etc/network/interfaces"},
}

// genericSSH and genericHTTP are used when nothing matches: the device still
// shows up in the adoption queue, marked as unidentified.
var genericSSH = Matcher{Driver: "generic-ssh", Vendor: "",
	IdentCmd: "show version", ConfigCmd: "show running-config"}
var genericHTTP = Matcher{Driver: "generic-http", Vendor: ""}

// Confidence assigned according to the source of the clue: what the device
// says about itself after login is worth more than an HTTP header.
const (
	// ConfDeviceSelfReport: the device identified itself through its API or
	// its page fields — better than an interpreted command output.
	ConfDeviceSelfReport = 96
	ConfShellOutput      = 92
	ConfSSHBanner        = 72
	ConfHTTPHeader       = 55
	ConfPortsOnly        = 20
)

// Identify picks a driver from the collected clues. Also returns the
// confidence and the retained source, which the UI displays as-is.
func Identify(shellOut, sshBanner, httpServer, httpTitle string) (Matcher, int, string) {
	type source struct {
		text  string
		conf  int
		label string
	}
	var sources = []source{
		{shellOut, ConfShellOutput, "command output"},
		{sshBanner, ConfSSHBanner, "SSH banner"},
		{httpServer + " " + httpTitle, ConfHTTPHeader, "HTTP header/title"},
	}
	var src source
	var m Matcher
	for _, src = range sources {
		if strings.TrimSpace(src.text) == "" {
			continue
		}
		for _, m = range matchers {
			if m.Re.MatchString(src.text) {
				return m, src.conf, src.label
			}
		}
	}
	// Nothing identified: fall back to a generic driver based on transport.
	if strings.TrimSpace(sshBanner) != "" || strings.TrimSpace(shellOut) != "" {
		return genericSSH, ConfPortsOnly, "not identified (SSH reachable)"
	}
	if strings.TrimSpace(httpServer+httpTitle) != "" {
		return genericHTTP, ConfPortsOnly, "not identified (web reachable)"
	}
	return genericHTTP, ConfPortsOnly, "not identified"
}

// MatcherFor looks up a matcher by driver name (manual type choice in the
// UI, or a candidate reloaded from the database).
func MatcherFor(driver string) Matcher {
	var m Matcher
	for _, m = range matchers {
		if m.Driver == driver {
			return m
		}
	}
	if driver == genericSSH.Driver {
		return genericSSH
	}
	return genericHTTP
}

// Drivers lists the drivers that can be proposed in the UI (explicit type
// choice).
func Drivers() []map[string]string {
	var out = []map[string]string{}
	var m Matcher
	for _, m = range matchers {
		out = append(out, map[string]string{"driver": m.Driver, "vendor": m.Vendor})
	}
	out = append(out,
		map[string]string{"driver": genericSSH.Driver, "vendor": "generic"},
		map[string]string{"driver": genericHTTP.Driver, "vendor": "generic"})
	return out
}

var hostnameRe = regexp.MustCompile(`(?m)^\s*(?:hostname|sysname|system name)\s+"?([A-Za-z0-9_.\-]+)"?`)

// The ":" or "=" separator is MANDATORY in these two patterns.
//
// Without it, "System Serial Number : FOC1841X0AB" captures the word
// "Number": the "system serial" alternative matches before "serial number",
// and since (?i) also makes the [A-Z0-9] class case-insensitive, the
// following word makes a perfectly credible-looking serial. Requiring the
// separator removes that entire class of bug, at the cost of the rare
// outputs that are aligned without punctuation — where we'd rather have an
// empty field than an invented value.
var serialRe = regexp.MustCompile(`(?i)serial(?:[ -]?(?:number|no\.?))?\s*[:=]\s*"?([A-Za-z0-9\-]{5,24})`)
var modelRe = regexp.MustCompile(`(?i)(?:model(?:[ -]?(?:number|name))?|hardware(?:[ -]?model)?)\s*[:=]\s*"?([A-Za-z0-9\-/+]{3,32})`)

var hasDigitRe = regexp.MustCompile(`[0-9]`)

// ExtractFacts fishes the hostname, serial, and model out of a command
// output. Deliberately conservative: an empty field is better than a wrong
// value that would give the user the illusion of a reliable identification.
func ExtractFacts(text string) (hostname, serial, model string) {
	var m = hostnameRe.FindStringSubmatch(text)
	if len(m) > 1 {
		hostname = m[1]
	}
	m = serialRe.FindStringSubmatch(text)
	// A serial number contains at least one digit: this safeguard rules out
	// ordinary words that an overly permissive pattern would otherwise pick up.
	if len(m) > 1 && hasDigitRe.MatchString(m[1]) {
		serial = strings.ToUpper(m[1])
	}
	m = modelRe.FindStringSubmatch(text)
	if len(m) > 1 {
		model = m[1]
	}
	return hostname, serial, model
}

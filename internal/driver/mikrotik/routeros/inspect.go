package routeros

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Identification of a RouterOS device.
//
// Unlike the Instant On range, there is NOTHING to scrape: three REST menus
// return the complete identity, in JSON, without ambiguity. OBSERVED on
// 10.99.0.54:
//
//	/rest/system/resource    → {"board-name":"CRS326-24G-2S+",
//	                            "version":"7.21beta3 (testing)",
//	                            "architecture-name":"arm","platform":"MikroTik",
//	                            "build-time":"2025-10-14 12:50:07", ...}
//	/rest/system/routerboard → {"model":"CRS326-24G-2S+",
//	                            "serial-number":"945509EF00B0",
//	                            "current-firmware":"6.42.5","routerboard":"true"}
//	/rest/system/identity    → {"name":"lan-r4"}
//
// A major difference from the Instant On connector, and it must be known: this
// identification requires CREDENTIALS. Without them, `/rest/...` responds 401
// with `WWW-Authenticate: Basic` — OBSERVED. There is no equivalent of a 1930
// login page that serves sysDescr to whoever asks. Discovery must therefore
// either have credentials, or settle for the HTTP title ('RouterOS', served
// without authentication, cf. Fingerprint) and the SNMP serial number.

// DeviceIdentity is what is known about the device after an authenticated read.
//
// The contract view is [Identity] (`driver.Identity`), obtained via
// [DeviceIdentity.Standard]. Four things stay HERE because the contract does not
// carry them, and it matters which:
//
//	RouterBoot    the BOOTLOADER version. OBSERVED: RouterOS 7.21beta3 runs on
//	              RouterBOOT 6.42.5. Merging them into the contract's
//	              `Firmware` field would suggest a device four major versions
//	              behind, and push toward an update that is not warranted.
//	Architecture  and Platform, as the device declares them.
//	MaxL2MTU      the frame ceiling the device PUBLISHES itself (10218
//	              OBSERVED). Used to validate a jumbo-frame intent against the
//	              real bound rather than a catalog value.
//	Ports         the port-by-port front panel, of which the contract keeps only
//	              counts and groups.
//
// This is the circulation sense the contract describes: it carries what the
// CALLER consumes; the connector keeps its richer type.
type DeviceIdentity struct {
	// Model is the product reference as the device names itself.
	Model string `json:"model"`
	// BoardName is the board name from `/system/resource`. Identical to Model on
	// the reference device, but the two menus are distinct and nothing guarantees
	// they agree across the range — we keep both rather than electing one.
	BoardName string `json:"board_name,omitempty"`
	Serial    string `json:"serial"`
	MAC       string `json:"mac,omitempty"`
	// Firmware is the RouterOS version — the SYSTEM.
	Firmware string `json:"firmware,omitempty"`
	// Build is the compile timestamp. RouterOS publishes no separate build
	// number: `build-time` is what distinguishes two images of the same version,
	// which matters on a `testing` branch where '7.21beta3' can denote
	// several images.
	Build string `json:"build,omitempty"`
	// RouterBoot is the BOOTLOADER version, not to be confused with Firmware.
	// OBSERVED: RouterOS 7.21beta3 runs on RouterBOOT 6.42.5. Presenting them as
	// one version would suggest a device four major versions behind.
	RouterBoot string `json:"routerboot,omitempty"`
	// Architecture and Platform, as the device declares them.
	Architecture string `json:"architecture,omitempty"`
	Platform     string `json:"platform,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	// Front panel, counted on interfaces ACTUALLY present.
	CopperPorts int  `json:"copper_ports"`
	SFPPorts    int  `json:"sfp_ports"`
	SFPPlus     bool `json:"sfp_plus"`
	PoE         bool `json:"poe"`
	// MaxL2MTU is the frame ceiling the device PUBLISHES itself.
	//
	// OBSERVED: 10218 bytes on all 26 ports of the reference CRS326, served by
	// `/interface` (not `/interface/ethernet`, which does not carry it). This
	// validates a jumbo-frame intent against the device's real bound instead of a
	// catalog value — a ceiling assumed too high ends in refusal at apply, too low
	// in refusal by meshcraft.
	MaxL2MTU int `json:"max_l2mtu,omitempty"`
	// Ports is the list of switching interfaces, in device order. It is the
	// source of PortGroups, and why this connector needs no model catalog.
	Ports []Port `json:"ports,omitempty"`
	// Source says where the information came from: the UI must not present a
	// deduction as a read.
	Source string `json:"source"` // rest_api
}

// Port is a switching interface as the device declares it.
type Port struct {
	// Name is the CURRENT name, the one to use when writing.
	Name string `json:"name"`
	// DefaultName is the factory name. The two differ once an operator has
	// renamed a port; `DefaultName` carries the range convention (`ether3`,
	// `sfp-sfpplus1`) and therefore groups the front panel.
	DefaultName string `json:"default_name,omitempty"`
	MAC         string `json:"mac,omitempty"`
	// SpeedMbps is the maximum rate the port ANNOUNCES it can do, DERIVED from
	// its `advertise` field.
	SpeedMbps int `json:"speed_mbps,omitempty"`
	// SFP says whether the port is an optical cage rather than a copper jack.
	SFP bool `json:"sfp,omitempty"`
	// Running: does the port carry a link. Telemetry, not configuration — present
	// here because identification reads it anyway, and because apply needs it as
	// a guardrail.
	Running bool `json:"running,omitempty"`
}

// Inspect identifies the device. READ-ONLY.
//
// VALIDATED LIVE against 10.99.0.54 (CRS326-24G-2S+, RouterOS 7.21beta3): returns
// model `CRS326-24G-2S+`, serial `945509EF00B0`, firmware `7.21beta3 (testing)`,
// RouterBOOT `6.42.5`, system name `lan-r4`, MAC `B8:69:F4:5B:A0:D2`, front panel
// 24 copper ports + 2 SFP+.
//
// The four reads are tolerant SEPARATELY, and that is deliberate: firmware that
// would not serve `/system/routerboard` (a CHR virtual machine, for example, which
// is not a RouterBOARD) must return a partial identity rather than fail all of
// discovery. Only `/system/resource` is mandatory — without it, we are not talking
// to a RouterOS device.
func Inspect(ctx context.Context, host string, d driver.Dialer,
	user, secret string) (DeviceIdentity, error) {
	var c = Open(host, d, user, secret)
	return c.Identify(ctx)
}

// Identify does Inspect's work on an already-open client, so a caller with other
// reads to make does not open a second client.
//
// NOT TO BE CONFUSED with [Driver.Identify], which uses the same verb for the
// same job one level up: this one reads on access ALREADY proven and returns the
// rich view; that one starts from an address and a candidate list and returns the
// contract view. Both exist because both needs exist — discovery has no session,
// the API layer has one.
func (c *Client) Identify(ctx context.Context) (DeviceIdentity, error) {
	var id = DeviceIdentity{Source: "rest_api"}

	var resource, err = c.Object(ctx, "system/resource")
	if err != nil {
		return id, fmt.Errorf("identification failed: %w", err)
	}
	id.BoardName = resource.Get("board-name")
	id.Model = id.BoardName
	id.Firmware = resource.Get("version")
	id.Build = resource.Get("build-time")
	id.Architecture = resource.Get("architecture-name")
	id.Platform = resource.Get("platform")

	// `/system/routerboard`: serial number and bootloader. Absent on RouterOS
	// instances not running on a MikroTik board.
	var board Row
	board, err = c.Object(ctx, "system/routerboard")
	if err == nil {
		if board.Get("model") != "" {
			id.Model = board.Get("model")
		}
		id.Serial = board.Get("serial-number")
		id.RouterBoot = board.Get("current-firmware")
	}

	var identity Row
	identity, err = c.Object(ctx, "system/identity")
	if err == nil {
		id.Hostname = identity.Get("name")
	}

	// Front panel, counted on REAL interfaces.
	//
	// Here is where this connector differs from a catalog: it does not DERIVE the
	// front panel from a product reference, it READS it. A disabled port, an
	// extension card, a model unknown to our regexps: all counted correctly,
	// because the device answers.
	var ifaces []Row
	ifaces, err = c.List(ctx, "interface")
	if err == nil {
		// TWO menus, and both are needed: `/interface` carries type, link state,
		// MAC, and frame ceiling (`max-l2mtu`); `/interface/ethernet` carries
		// ADVERTISED SPEEDS (`advertise`), which do not exist in the first. Having
		// believed everything could come from `/interface` yielded ports at 0 Mbit/s
		// — a device drawn with no speed at all. The second request costs about
		// 110 ms (measured), once, at identification: not on the supervision path.
		var eth []Row
		eth, _ = c.List(ctx, "interface/ethernet") //nolint:errcheck // speeds absent if unreadable
		id.Ports = switchPorts(ifaces, eth)
		id.MaxL2MTU = maxL2MTU(ifaces)
		var p Port
		for _, p = range id.Ports {
			if p.SFP {
				id.SFPPorts++
				if p.SpeedMbps >= 10000 {
					id.SFPPlus = true
				}
				continue
			}
			id.CopperPorts++
		}
		// Device MAC is that of its FIRST interface, which the bridge also reuses
		// — OBSERVED: ether1, the bridge, and vlan99 all carry `B8:69:F4:5B:A0:D2`.
		// This is the board's L2 identity, not an arbitrary port's.
		if len(id.Ports) > 0 {
			id.MAC = id.Ports[0].MAC
		}
	}

	// PoE is NOT inferred from the name: it is read, exactly like the front panel.
	//
	// `/interface/ethernet/poe` responds 500 on a model without PoE, but a 500
	// proves nothing on this API (cf. restStatusMeaning). `poe/settings` decides,
	// responding 400 'no such command prefix' when the menu prefix does not
	// exist. That is the one we query.
	id.PoE = c.hasPoE(ctx)

	c.step(true, "device identified",
		fmt.Sprintf("%s, serial %s, RouterOS %s (RouterBOOT %s), name '%s', "+
			"%d copper ports + %d SFP%s", id.Model, id.Serial, id.Firmware,
			id.RouterBoot, id.Hostname, id.CopperPorts, id.SFPPorts,
			map[bool]string{true: "+"}[id.SFPPlus]))
	return id, nil
}

// hasPoE reports whether the board supports Power over Ethernet.
//
// Returns FALSE when the question could not be asked: an unknown capability is
// assumed absent, and erring that way only deprives the operator of a setting —
// the opposite would make them believe an access point will power up.
func (c *Client) hasPoE(ctx context.Context) bool {
	var _, err = c.List(ctx, "interface/ethernet/poe/settings")
	return err == nil
}

// reTrailingDigits splits an interface name prefix from its number:
// 'ether12' → 'ether' + 12, 'sfp-sfpplus2' → 'sfp-sfpplus' + 2.
var reTrailingDigits = regexp.MustCompile(`^(.*?)(\d+)$`)

// reAdvertiseSpeed reads speeds advertised by a port. OBSERVED on
// `sfp-sfpplus2`: '10M-baseT-half,...,1G-baseX,2.5G-baseT,5G-baseT,10G-baseT,
// 10G-baseSR-LR,10G-baseCR'. Port speed is the LARGEST in the list.
var reAdvertiseSpeed = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)(M|G)-base`)

// switchPorts keeps SWITCHING interfaces, in natural order.
//
// What is excluded, and why: `/rest/interface` returns 29 entries on the
// reference device, including the bridge, loopback, and management `vlan99`
// interface. Showing them as ports would invent three slots on the front panel —
// and, far worse, expose `bridge` and `vlan99` to VLAN apply, when those are
// precisely the objects administration passes through.
//
// The criterion is `type: "ether"`, the one the device itself uses (OBSERVED:
// all 26 switching ports carry `"type":"ether"`, the bridge carries
// `"type":"bridge"`, loopback `"type":"loopback"`, and the VLAN `"type":"vlan"`).
func switchPorts(rows, ethernet []Row) []Port {
	// Advertised speeds, indexed by name: they come from `/interface/ethernet`,
	// the only menu that carries `advertise`.
	var advertised = map[string]string{}
	var e Row
	for _, e = range ethernet {
		advertised[e.Get("name")] = e.Get("advertise")
	}

	var out []Port
	var r Row
	for _, r = range rows {
		if r.Get("type") != "ether" {
			continue
		}
		var p = Port{
			Name:        r.Get("name"),
			DefaultName: r.Get("default-name"),
			MAC:         r.Get("mac-address"),
			SpeedMbps:   maxAdvertised(advertised[r.Get("name")]),
			Running:     r.Bool("running"),
		}
		// An optical cage is recognized by its factory name, which follows the
		// range convention: `sfp1`, `sfp-sfpplus2`, `qsfpplus1-1`. The CURRENT name
		// does not serve for that — an operator who renamed `sfp-sfpplus1` to
		// 'uplink-core' did not turn the port into a copper jack.
		var basis = p.DefaultName
		if basis == "" {
			basis = p.Name
		}
		p.SFP = strings.Contains(strings.ToLower(basis), "sfp")
		out = append(out, p)
	}
	sortPorts(out)
	return out
}

// maxAdvertised returns the highest speed a port advertises, in Mbit/s.
//
// Reading speed on the device rather than in a model table is what draws an
// unseen chassis correctly: OBSERVED, `ether1` caps at `1G-baseT-full` (1000) and
// `sfp-sfpplus2` reaches `10G-baseSR-LR` (10000).
func maxAdvertised(advertise string) int {
	var best int
	var m []string
	for _, m = range reAdvertiseSpeed.FindAllStringSubmatch(advertise, -1) {
		var value, err = strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		var mbps = int(value)
		if strings.EqualFold(m[2], "G") {
			mbps = int(value * 1000)
		}
		if mbps > best {
			best = mbps
		}
	}
	return best
}

// maxL2MTU returns the frame ceiling the device publishes, keeping the LOWEST
// value among its ports.
//
// The lowest and not the highest: a frame crosses the switch only if ALL ports
// it uses accept it. Keeping the maximum would accept an intent a single port
// could fail — failure visible at apply at best, silently dropped frames in
// production at worst.
//
// OBSERVED: 10218 on all 26 ports of the reference CRS326, therefore uniform.
// That may not hold on a chassis with extension cards.
func maxL2MTU(rows []Row) int {
	var best int
	var r Row
	for _, r = range rows {
		if r.Get("type") != "ether" {
			continue
		}
		var v, ok = r.Int("max-l2mtu")
		if !ok || v <= 0 {
			continue
		}
		if best == 0 || v < best {
			best = v
		}
	}
	return best
}

// sortPorts orders ports as a human reads them on the front panel: by prefix,
// then by NUMBER and not alphabetically.
//
// Without this, `ether10` would sit before `ether2`, and the drawn front panel
// would no longer match the device in front of you.
func sortPorts(ports []Port) {
	sort.SliceStable(ports, func(i, j int) bool {
		var pi, ni = splitPortName(ports[i].DefaultName, ports[i].Name)
		var pj, nj = splitPortName(ports[j].DefaultName, ports[j].Name)
		if pi != pj {
			// Copper first, optical cages second: front-panel order across the
			// range, SFP ports on the right.
			var si = strings.Contains(strings.ToLower(pi), "sfp")
			var sj = strings.Contains(strings.ToLower(pj), "sfp")
			if si != sj {
				return sj
			}
			return pi < pj
		}
		return ni < nj
	})
}

// splitPortName returns a port's prefix and number. Factory name wins when present.
func splitPortName(defaultName, name string) (string, int) {
	var basis = defaultName
	if basis == "" {
		basis = name
	}
	var m = reTrailingDigits.FindStringSubmatch(basis)
	if m == nil {
		return basis, 0
	}
	var n, err = strconv.Atoi(m[2])
	if err != nil {
		return basis, 0
	}
	return m[1], n
}

// PortNames returns switching port names, in front-panel order.
//
// These names are EXACTLY the device's (`ether1`, `sfp-sfpplus2`), and that is a
// design decision, not a shortcut.
//
// The Instant On connector must translate: its device numbers ports 1 through 52
// and meshcraft names them 'GE1'…, hence a prefix to add and remove on every
// read and write — and ParsePortRange/portNumbers to handle it. Here RouterOS
// NAMES its ports, and those names are already stable, readable identifiers.
// Reusing them removes all translation, hence an entire class of bug: writing to
// port 3 while believing it is port 4. On a VLAN table, that kind of error cuts
// off a client.
func (id DeviceIdentity) PortNames() []string {
	var out = make([]string, 0, len(id.Ports))
	var p Port
	for _, p = range id.Ports {
		out = append(out, p.Name)
	}
	return out
}

// PortGroups translates the front panel into standard-model port groups.
//
// Record shape follows what the Instant On connector produces and the frontend
// consumes (`prefix`, `count`, `from`, `speed`, `poe`, `label`): the UI
// reconstructs names by concatenating `prefix` and a number from `from`. Because
// meshcraft names here are RouterOS names, groups carry device prefixes — `ether`
// and `sfp-sfpplus` — not a conventional 'GE'.
//
// Groups are built on FACTORY names and verified CONTIGUOUS: a prefix whose
// numbering skips is split into several groups rather than declared in one block.
// Announcing 'ether, 24 ports starting at 1' when one is missing in the middle
// would draw a jack that does not exist, and meshcraft would eventually write to
// it.
func (id DeviceIdentity) PortGroups() []map[string]any {
	var out = []map[string]any{}
	var group []Port
	var flush = func() {
		if len(group) == 0 {
			return
		}
		var prefix, from = splitPortName(group[0].DefaultName, group[0].Name)
		var speed = group[0].SpeedMbps
		var label = prefix
		if group[0].SFP {
			label = "SFP"
			if speed >= 10000 {
				label = "SFP+"
			}
		} else if id.PoE {
			label = fmt.Sprintf("%s · PoE", prefix)
		}
		out = append(out, map[string]any{
			"prefix": prefix,
			"count":  len(group),
			"from":   from,
			"speed":  speedLabel(speed),
			// PoE never applies to an optical cage.
			"poe":   id.PoE && !group[0].SFP,
			"label": label,
		})
		group = nil
	}

	var p Port
	for _, p = range id.Ports {
		if len(group) == 0 {
			group = append(group, p)
			continue
		}
		var prevPrefix, prevNum = splitPortName(
			group[len(group)-1].DefaultName, group[len(group)-1].Name)
		var prefix, num = splitPortName(p.DefaultName, p.Name)
		// Same prefix, next number, same speed: same front-panel group.
		if prefix == prevPrefix && num == prevNum+1 &&
			p.SpeedMbps == group[0].SpeedMbps {
			group = append(group, p)
			continue
		}
		flush()
		group = append(group, p)
	}
	flush()
	return out
}

// speedLabel names a speed as the standard model expects ('1G', '10G').
func speedLabel(mbps int) string {
	if mbps >= 1000 && mbps%1000 == 0 {
		return strconv.Itoa(mbps/1000) + "G"
	}
	if mbps > 0 {
		return strconv.Itoa(mbps) + "M"
	}
	return ""
}

// --- What the contract expects from an identification ---

// Identify reports which device responds at an address.
//
// REQUIRES CANDIDATES, and that is a difference in nature from the other two
// shipped platforms — not a lag of this connector, a RouterOS fact:
//
//   - Instant On carries `sysDescr` and its serial in hidden login-page fields,
//     served to whoever asks;
//   - SwOS announces its model in the Digest challenge realm
//     (`realm="CSS326-24G-2S+"`), therefore before any authentication;
//   - RouterOS says NOTHING. The REST API responds 401 on ALL its resources,
//     including the one that carries the model (OBSERVED: `/rest/system/resource` →
//     401 with `WWW-Authenticate: Basic`). There is no page that speaks before
//     login.
//
// Without a candidate, discovery therefore stops at the web page title — '
// RouterOS', served without authentication, enough to place the device in the
// right connector but not to name its model — and the SNMP serial number. That is
// what the message below says, because the operator looking at a less informative
// discovery line than the others must understand WHY, and know that completing it
// means supplying a credential.
//
// RETURNS A PARTIAL IDENTITY WITH THE REASON FOR STOPPING, never a bare error.
// That is the contract rule, and it has a concrete consequence here: the four
// reads of [Client.Identify] are tolerant SEPARATELY — firmware that would not
// serve `/system/routerboard` (a CHR virtual machine, which is not a RouterBOARD)
// returns model and version without serial number, rather than failing all of
// discovery. 'It is a CRS326-24G-2S+ whose serial we did not read' is a useful
// result; 'failure' is not.
func (d *Driver) Identify(ctx context.Context, tgt driver.Target,
	cands []Candidate) (Identity, error) {
	if len(cands) == 0 {
		return Identity{}, fmt.Errorf("cannot identify %s without a credential: "+
			"RouterOS REST responds 401 on all its resources, including the one that "+
			"carries the model. Unlike Instant On and SwOS ranges, this device says "+
			"nothing before authentication — supply a credential, or settle for its "+
			"web page title ('RouterOS', served without authentication) and "+
			"its SNMP serial number", tgt.Host)
	}

	// Same candidate loop as apply, with the same guardrails: caller order
	// respected, no candidate replayed, stop on dead transport or on 403.
	// Reusing [Driver.Authenticate] rather than redoing a loop is what guarantees
	// those guardrails will not diverge between the two paths — and the attempt
	// budget is the same budget, whichever gesture spends it.
	var admin, err = d.Authenticate(ctx, tgt, cands)
	if err != nil {
		return Identity{}, err
	}
	var c, ok = admin.(*Client)
	if !ok {
		return Identity{}, fmt.Errorf("access of unexpected type for %s", tgt.Host)
	}

	// Error is returned WITH whatever could be read: `Standard` on a partial
	// identity carries what little is known, and that is better than nothing.
	var dev DeviceIdentity
	dev, err = c.Identify(ctx)
	return dev.Standard(), err
}

// Standard maps the rich identity to what the contract carries.
//
// What stays behind is listed on [DeviceIdentity]: RouterBOOT, architecture,
// platform, and frame ceiling. What is CALCULATED here rather than copied deserves
// a word, because both cases are decisions:
//
//   - `Family` is DERIVED from the reference — 'CRS326-24G-2S+' yields 'CRS'.
//     The contract wants it for capability differences at sub-range level, and
//     MikroTik names families at the head of the reference. Derivation cannot
//     invent: without leading letters, it returns empty;
//   - `ModelCode` stays EMPTY, and that is not an oversight. This field is the
//     CATALOG reference when it differs from the label — 'JL685A' at Aruba.
//     MikroTik has none: `/system/routerboard` and `/system/resource` both return
//     the same string, which IS both label and commercial reference. Copying the
//     model there would display the same thing twice as if they were two
//     pieces of information.
//
// `PortGroups` is the field the contract carries that this package used to expose
// as a METHOD: the front panel is computed by [DeviceIdentity.PortGroups] and
// FILLED here, which provides the same service. Groups carry device prefixes
// (`ether`, `sfp-sfpplus`) and are verified CONTIGUOUS — announcing '24 ports
// starting at 1' when one is missing in the middle would draw a jack that does
// not exist, and meshcraft would eventually write to it.
func (id DeviceIdentity) Standard() Identity {
	return Identity{
		Model:  id.Model,
		Family: familyOf(id.Model),
		Serial: id.Serial,
		MAC:    id.MAC,
		// `Firmware` is the RouterOS version, THE SYSTEM. See [DeviceIdentity] for
		// why RouterBOOT does not come here.
		Firmware: id.Firmware,
		// RouterOS publishes no separate build number: `build-time` is what
		// distinguishes two images of the same version, which matters on a `testing`
		// branch where '7.21beta3' can denote several images.
		Build:       id.Build,
		Hostname:    id.Hostname,
		CopperPorts: id.CopperPorts,
		SFPPorts:    id.SFPPorts,
		SFPPlus:     id.SFPPlus,
		PoE:         id.PoE,
		PortGroups:  id.PortGroups(),
		Source:      id.Source,
	}
}

// reFamily isolates the leading family of a MikroTik reference: 'CRS326-24G-2S+' →
// 'CRS', 'CCR2004-1G-12S+2XS' → 'CCR'.
var reFamily = regexp.MustCompile(`^([A-Za-z]+)`)

// familyOf returns a product reference's sub-range, or empty.
//
// EMPTY RATHER THAN APPROXIMATE when the reference does not start with letters:
// the project rule applied to a display field — an invented family would place a
// device in a range whose capabilities it lacks, and nobody would see the
// difference on screen.
func familyOf(model string) string {
	var m = reFamily.FindStringSubmatch(strings.TrimSpace(model))
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

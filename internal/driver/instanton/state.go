package instanton

import (
	"context"
	"meshcraft/internal/driver"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Reading state ACTUALLY applied on an Instant On.
//
// This is the « Read » step of the reconciliation pipeline
// (docs/01-Architecture/Reconciliation §2): without it, meshcraft cannot tell
// whether what it believes matches what is, and « configuration drift » remains
// just a word.
//
// Two sections suffice, observed on a real 1930:
//
//	GET wcd?{VLANList}                     → declared VLANs
//	GET wcd?{VLANInterfaceMembershipTable} → per VLAN, tagged and untagged ports
//
// There is NO « PVID per port » section: the device UI computes it itself
// (`assets.getPvid`) from untagged membership.
// We do the same — a port's PVID is the VLAN where it is an untagged member.
// Looking for a section that does not exist would have been costly; reading it
// in the device JavaScript cost one request.

// `VLAN`, `PortState` and `State` are now contract types
// (`driver.VLAN`, `driver.PortState`, `driver.State`), by ALIAS: cf. contract.go.
// Their fields and JSON tags are what this package carried — `State` is
// serialized in `devices.observed`, and a renamed tag would read « zero VLANs,
// no ports » on every already-collected device.
//
// `State` gains a `Notes` field for what a connector understood that the structure
// cannot express — VLAN configuration present but INACTIVE, for example. This
// product line has NOTHING to declare there today: it has no state where configured
// VLAN membership would remain without effect on traffic, and fabricating a note to
// fill the field would show the operator a meaningless warning. An empty field that
// stays empty is the right answer here.

var (
	reVLANBlock  = regexp.MustCompile(`(?is)<VLAN>(.*?)</VLAN>`)
	reEntryBlock = regexp.MustCompile(`(?is)<Entry>(.*?)</Entry>`)
)

// ReadState re-reads the device. READ ONLY: nothing is modified, so it can be
// replayed as often as needed, including on a production fleet.
//
// The port prefix is NO LONGER A PARAMETER: it lives on the session, set by the
// caller at construction (cf. Admin.prefix). Two reads on the same session with
// different prefixes would yield two port name sets for the same device, and drift
// calculation would compare ports that do not exist.
func (a *Admin) ReadState(ctx context.Context) (State, error) {
	var prefix = a.portPrefix()
	var out = State{VLANs: []VLAN{}, Ports: map[string]PortState{}}

	var resp, err = a.web.Get(ctx, a.wcdURL()+"?{VLANList}")
	if err != nil {
		a.step(false, "reading VLANs", err.Error())
		return out, err
	}
	var block string
	for _, block = range reVLANBlock.FindAllString(resp.Body, -1) {
		var id, convErr = strconv.Atoi(xmlField(block, "VLANID"))
		if convErr != nil {
			continue
		}
		out.VLANs = append(out.VLANs, VLAN{ID: id, Name: xmlField(block, "VLANName")})
	}
	a.step(true, "VLANs read from device",
		strconv.Itoa(len(out.VLANs))+" VLAN(s)")

	resp, err = a.web.Get(ctx, a.wcdURL()+"?{VLANInterfaceMembershipTable}")
	if err != nil {
		a.step(false, "reading VLAN membership", err.Error())
		return out, err
	}
	out.Ports = portsFromMembership(resp.Body, prefix)
	a.step(true, "port membership read",
		strconv.Itoa(len(out.Ports))+" port(s)")

	// Management VLAN, FROM the device. The only source that says where the
	// current session goes.
	resp, err = a.web.Get(ctx, a.wcdURL()+"?{ManagementInterfaceGlobalSetting}")
	if err != nil {
		a.step(false, "reading management VLAN", err.Error())
		return out, err
	}
	out.MgmtVLAN, _ = strconv.Atoi(xmlField(resp.Body, "managementVLAN"))
	a.step(true, "device management VLAN",
		"VLAN "+strconv.Itoa(out.MgmtVLAN))

	// Link state. Observed on a 1930: `linkState` is 1 when the port has a link,
	// 2 when down (and 6 for aggregates, which are not physical ports).
	resp, err = a.web.Get(ctx, a.wcdURL()+"?{Standard802_3List}")
	if err != nil {
		a.step(false, "reading link state", err.Error())
		return out, err
	}
	out.Links = driver.LinksFromUp(linksFromPortList(resp.Body, prefix))
	a.step(true, "link state read",
		strconv.Itoa(len(out.LinkUp()))+" port(s) with link")
	return out, nil
}

// linkStateUp is the value the device assigns to a port that has a link.
const linkStateUp = "1"

// linksFromPortList lists active ports. Only ports WITH a link are returned:
// absence in the map means « no link », which is also the safe default when
// the read said nothing.
func linksFromPortList(body, prefix string) map[string]bool {
	var out = map[string]bool{}
	var block string
	for _, block = range reEntryBlock.FindAllString(body, -1) {
		if xmlField(block, "linkState") != linkStateUp {
			continue
		}
		var name = xmlField(block, "interfaceName")
		var _, err = strconv.Atoi(name)
		if err != nil {
			continue // aggregate: not a physical port
		}
		out[prefix+name] = true
	}
	return out
}

// portsFromMembership rebuilds per-PORT configuration from the per-VLAN table.
// The device reasons by VLAN (« who is a member? »), meshcraft by port (« what does
// this port carry? »): the pivot happens here.
func portsFromMembership(body, prefix string) map[string]PortState {
	var out = map[string]PortState{}
	var tagged = map[string][]int{}
	var pvid = map[string]int{}

	var block string
	for _, block = range reEntryBlock.FindAllString(body, -1) {
		var id, err = strconv.Atoi(xmlField(block, "VLANID"))
		if err != nil {
			continue
		}
		var p string
		for _, p = range ParsePortRange(xmlField(block, "untaggedPorts"), prefix) {
			// A port is untagged in only ONE VLAN: that is its PVID.
			pvid[p] = id
		}
		for _, p = range ParsePortRange(xmlField(block, "taggedPorts"), prefix) {
			tagged[p] = append(tagged[p], id)
		}
	}

	var name string
	var vid int
	for name, vid = range pvid {
		out[name] = PortState{Mode: "access", PVID: vid}
	}
	var vids []int
	for name, vids = range tagged {
		sort.Ints(vids)
		var st = out[name]
		// A port carrying tagged VLANs is a trunk, regardless of native VLAN.
		st.Mode, st.Tagged = "trunk", vids
		out[name] = st
	}
	return out
}

// rePortToken splits a range element: « 12 », « 1-48 », « TRK1-16 ».
var rePortToken = regexp.MustCompile(`^([A-Za-z]*)(\d+)(?:-([A-Za-z]*)(\d+))?$`)

// ParsePortRange expands the device's compact notation
// (« 1-48,49-52,TRK1-16 ») into meshcraft port names (« GE1 », « GE2 »…).
//
// Aggregates (TRK…) are IGNORED: meshcraft does not model LAGs yet,
// and showing them as ports would invent faceplate ports that do not exist.
// Better an incomplete honest state than a complete false one.
func ParsePortRange(spec, prefix string) []string {
	var out []string
	var token string
	for _, token = range strings.Split(spec, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		var m = rePortToken.FindStringSubmatch(token)
		if m == nil {
			continue
		}
		// An alphabetic prefix denotes an aggregate, not a physical port.
		if m[1] != "" || m[3] != "" {
			continue
		}
		var from, err = strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		var to = from
		if m[4] != "" {
			if to, err = strconv.Atoi(m[4]); err != nil {
				continue
			}
		}
		// A reversed range is a response we cannot parse: drop it rather than
		// spawning thousands of ports.
		if to < from || to-from > 1024 {
			continue
		}
		var i int
		for i = from; i <= to; i++ {
			out = append(out, prefix+strconv.Itoa(i))
		}
	}
	return out
}

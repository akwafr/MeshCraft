package procurve

// The "show" views: everything the running configuration does NOT carry.
//
// The configuration states INTENT and is authoritative for membership. It says
// nothing about link state, about who is on the other end of a cable, or about
// where the spanning tree put each port — and those are read here.
//
// EVERY FORM BELOW IS CAPTURED on SW-PLAYER-79 (ProCurve 2610-48 / J9088A,
// R.11.121, 2026-08-03) and the verbatim outputs are in testdata/. They are the
// text as this connector sees it — that is, after [stripControl], because this
// CLI paints its output with cursor positioning and terminates lines with ESC E
// (cf. cli.go).

import (
	"regexp"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// portStatus is one row of "show interfaces brief".
type portStatus struct {
	// Kind is the Type column: "10/100TX", "100/1000T", or EMPTY on an SFP cage
	// with no transceiver in it. The blank is meaningful — it is how this range
	// says "socket present, nothing plugged" — and it is why these tables are
	// read by ruler rather than split on whitespace (cf. table.go).
	Kind    string
	Enabled bool
	Up      bool
}

// parseInterfacesBrief reads the faceplate's operational state.
//
// The DISABLED case is what makes this worth reading rather than inferring:
// an administratively disabled port and an unplugged one both print "Down",
// and only the Enabled column tells them apart. meshcraft has distinct
// vocabulary for the two ([driver.LinkDisabled] vs [driver.LinkDown]) and
// collapsing them would report a port someone deliberately shut as a fault.
func parseInterfacesBrief(text string) map[int]portStatus {
	var out = map[int]portStatus{}
	var row []string
	for _, row = range tableRows(text, "Port", "Enabled", "Status") {
		if len(row) < 5 {
			continue
		}
		var n, err = strconv.Atoi(row[0])
		if err != nil || n < 1 {
			continue
		}
		out[n] = portStatus{
			Kind:    row[1],
			Enabled: !strings.EqualFold(row[3], "No"),
			Up:      strings.EqualFold(row[4], "Up"),
		}
	}
	return out
}

// linkStates translates the faceplate into meshcraft's vocabulary.
func linkStates(ports map[int]portStatus, prefix string) map[string]driver.LinkState {
	var out = map[string]driver.LinkState{}
	var n int
	var st portStatus
	for n, st = range ports {
		var name = prefix + strconv.Itoa(n)
		switch {
		case st.Up:
			out[name] = driver.LinkUpState
		case !st.Enabled:
			out[name] = driver.LinkDisabled
		default:
			out[name] = driver.LinkDown
		}
	}
	return out
}

// vlanNames maps VLAN id → name from "show vlans".
//
// Read even though the running configuration carries the same pairs, because
// [parseIPTable] keys its rows by NAME: this range prints the addressing table
// with the VLAN's name and never its id, so the two views only join through
// this map.
func vlanNames(text string) map[int]string {
	var out = map[int]string{}
	var row []string
	for _, row = range tableRows(text, "VLAN ID", "Name", "Status") {
		if len(row) < 2 {
			continue
		}
		var id, err = strconv.Atoi(row[0])
		if err != nil {
			continue
		}
		out[id] = row[1]
	}
	return out
}

// ipRow is one line of the "show ip" VLAN table.
type ipRow struct {
	// Config is the IP Config column verbatim: "Manual", "DHCP/Bootp" or
	// "Disabled".
	Config  string
	Address string
	Mask    string
}

// ipService is what "show ip" says about addressing.
type ipService struct {
	Gateway string
	// ByVLANName because that is the only key this view offers.
	ByVLANName map[string]ipRow
}

func parseIPService(text string) ipService {
	var out = ipService{ByVLANName: map[string]ipRow{}}
	out.Gateway = labelValues(text)["Default Gateway"]
	var row []string
	for _, row = range tableRows(text, "IP Config", "IP Address", "Subnet Mask") {
		if len(row) < 2 {
			continue
		}
		var r = ipRow{Config: row[1]}
		if len(row) > 2 {
			r.Address = row[2]
		}
		if len(row) > 3 {
			r.Mask = row[3]
		}
		out.ByVLANName[row[0]] = r
	}
	return out
}

// systemInfo is the identity block of "show system-information".
type systemInfo struct {
	Name     string
	Contact  string
	Location string
	Serial   string
	MAC      string
	Revision string
}

func parseSystemInfo(text string) systemInfo {
	var kv = labelValues(text)
	return systemInfo{
		Name:     kv["System Name"],
		Contact:  kv["System Contact"],
		Location: kv["System Location"],
		Serial:   kv["Serial Number"],
		MAC:      kv["Base MAC Addr"],
		Revision: kv["Software revision"],
	}
}

// reModelFromLLDP pulls the product and its part number out of the LLDP system
// description, which is the ONE place a session sees them together:
//
//	System Description : ProCurve J9088A Switch 2610-48, revision R.11.121, R...
//
// "show version" on this range prints only a build stamp — no model, no part
// number — so an adopted device would otherwise keep an empty model forever.
var reModelFromLLDP = regexp.MustCompile(`(ProCurve|HP|HPE)\s+(J\d{4}[A-Z])\s+Switch\s+([\w\-]+)`)

// parseLocalDevice reads "show lldp info local-device" for the model.
func parseLocalDevice(text string) (model string, partNumber string) {
	var descr = labelValues(text)["System Description"]
	var m = reModelFromLLDP.FindStringSubmatch(descr)
	if m == nil {
		return "", ""
	}
	return "ProCurve " + m[3], m[2]
}

// LLDPNeighbor is one entry of the neighbour table.
//
// TRUNCATION IS THE PLATFORM'S, and it is why this is not the fleet's topology
// source. CAPTURED:
//
//	LocalPort | ChassisId                 PortId PortDescr SysName
//	--------- + ------------------------- ------ --------- ----------------------
//	47        | 44 f4 77 b5 a7 80         521    ge-0/0... salon-sw01
//
// "ge-0/0..." is what the switch prints: the summary view cuts the port
// description to the column width and marks it with an ellipsis. meshcraft
// builds its topology from SNMP (internal/snmppoll), where the values arrive
// whole; this reader exists so an operator asking "who is on port 47" gets an
// answer from the device in front of them, and the truncation is reported
// rather than passed off as the neighbour's real port name.
type LLDPNeighbor struct {
	LocalPort string
	ChassisID string
	PortID    string
	PortDescr string
	SysName   string
	Truncated bool
}

func parseLLDPRemote(text string) []LLDPNeighbor {
	var out []LLDPNeighbor
	var row []string
	for _, row = range tableRows(text, "LocalPort", "ChassisId", "SysName") {
		if len(row) < 2 {
			continue
		}
		var n = LLDPNeighbor{LocalPort: row[0]}
		// The chassis id is printed as space-separated octets; joined here so it
		// compares with what every other source in meshcraft carries.
		n.ChassisID = strings.ToLower(strings.ReplaceAll(row[1], " ", ""))
		if len(row) > 2 {
			n.PortID = row[2]
		}
		if len(row) > 3 {
			n.PortDescr = row[3]
		}
		if len(row) > 4 {
			n.SysName = row[4]
		}
		n.Truncated = strings.HasSuffix(n.PortDescr, "...") ||
			strings.HasSuffix(n.SysName, "...")
		out = append(out, n)
	}
	return out
}

// community is one row of "show snmp-server".
type community struct {
	Name string
	// View is the MIB View column ("Operator" is read-only, "Manager" is not).
	View  string
	Write string
}

func parseSNMPServer(text string) []community {
	var out []community
	var row []string
	for _, row = range tableRows(text, "Community Name", "MIB View") {
		if len(row) < 2 || row[0] == "" {
			continue
		}
		var c = community{Name: row[0], View: row[1]}
		if len(row) > 2 {
			c.Write = row[2]
		}
		out = append(out, c)
	}
	return out
}

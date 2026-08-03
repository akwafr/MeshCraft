package procurve

// Parsing the ProCurve configuration.
//
// # The model is VLAN-CENTRIC, and that inverts everything
//
// Every other connector in meshcraft reads a port and asks which VLANs it
// carries. This platform states the opposite: membership is declared UNDER THE
// VLAN, as compact port ranges. Captured on the reference unit (2610-48,
// R.11.121, 2026-08-02):
//
//	vlan 1
//	   name "DEFAULT_VLAN"
//	   ip address dhcp-bootp
//	   no untagged 1-52
//	   exit
//	vlan 99
//	   name "admin"
//	   ip address 172.16.99.79 255.255.255.0
//	   tagged 45-52
//	   exit
//	vlan 200
//	   name "joueurs"
//	   untagged 1-52
//	   exit
//
// So a port's PVID is "the VLAN that lists it as untagged" and its tagged set is
// "every VLAN that lists it as tagged" — both discovered by inverting the whole
// table, not by reading one stanza. That inversion is this file's job, and it is
// what lets the rest of meshcraft keep its port-centric vocabulary.
//
// A CONSEQUENCE THAT MATTERS FOR WRITES: a port is untagged in AT MOST ONE VLAN.
// The model cannot express anything else, so moving a PVID is a removal plus an
// addition, never a single assignment (cf. apply.go).

import (
	"regexp"
	"strconv"
	"strings"
)

// runningConfig is what a read extracts from « show running-config ».
type runningConfig struct {
	Hostname string
	// VLANs by id, with the name the device carries.
	VLANs map[int]string
	// Untagged: vlan id → ports declared untagged in it.
	Untagged map[int][]int
	// Tagged: vlan id → ports declared tagged in it.
	Tagged map[int][]int
	// MgmtVLAN: the VLAN carrying an IP address. Zero when none does.
	MgmtVLAN int
	// MgmtAddr: the address as written, for the record ("172.16.99.79 255.255.255.0"
	// or "dhcp-bootp").
	MgmtAddr string
	// MaxVLANs: « max-vlans N ». This is a RUNTIME limit on this platform, not a
	// product constant — the reference unit carries 30, where the family supports
	// up to 256. A site plan with more VLANs than this will not fit until the
	// limit is raised and the switch rebooted.
	MaxVLANs int
}

// THE LINE ANCHORS TOLERATE A LEADING CARRIAGE RETURN, and that is not
// defensive coding — it is what this device actually sends.
//
// Captured on the wire, 2026-08-02: the 2610 terminates its lines with LF then
// CR, REVERSED from the usual pairing:
//
//	b'\n\r   tagged 45-52 \n\r   exit \n\rvlan 99 \n\r   name "admin" \n\r'
//
// With multi-line mode, `^` matches straight after the `\n`, so every line
// begins with the stray `\r` from the previous terminator. An anchor written
// `^vlan` therefore matches NOTHING against a real device — which is exactly
// what happened: a live read returned 4647 characters and zero VLANs, while the
// same parser passed against a fixture whose control characters a capture
// pipeline had already stripped.
//
// `[ \t\r]*` and not `\s*`: in Go `\s` includes `\n`, so a leading `\s*`
// could span line boundaries and report a match starting on an earlier line —
// which would corrupt the stanza splitting below, since that works from match
// offsets.
var (
	reHostname = regexp.MustCompile(`(?m)^[ \t\r]*hostname\s+"?([^"\n]+?)"?\s*$`)
	reMaxVlans = regexp.MustCompile(`(?m)^[ \t\r]*max-vlans\s+(\d+)`)
	reVlanOpen = regexp.MustCompile(`(?m)^[ \t\r]*vlan\s+(\d+)[ \t\r]*$`)
	reExitLine = regexp.MustCompile(`(?m)^[ \t\r]*exit[ \t\r]*$`)
	reVlanName = regexp.MustCompile(`(?m)^\s*name\s+"?([^"\n]+?)"?\s*$`)
	reUntagged = regexp.MustCompile(`(?m)^\s*(no\s+)?untagged\s+([0-9,\-]+)`)
	reTagged   = regexp.MustCompile(`(?m)^\s*(no\s+)?tagged\s+([0-9,\-]+)`)
	reIPAddr   = regexp.MustCompile(`(?m)^\s*ip address\s+(.+?)\s*$`)
)

// parseRunningConfig inverts the VLAN-centric configuration into tables the rest
// of the connector can use.
//
// « no untagged 1-52 » is READ AND DISCARDED for membership: it is the device
// spelling out a removal, and treating it as an assignment would put every port
// of the switch into VLAN 1. That line is exactly why the regexes capture the
// optional « no » rather than matching the keyword alone.
func parseRunningConfig(out string) runningConfig {
	var rc = runningConfig{
		VLANs:    map[int]string{},
		Untagged: map[int][]int{},
		Tagged:   map[int][]int{},
	}
	if m := reHostname.FindStringSubmatch(out); m != nil {
		rc.Hostname = strings.TrimSpace(m[1])
	}
	if m := reMaxVlans.FindStringSubmatch(out); m != nil {
		rc.MaxVLANs, _ = strconv.Atoi(m[1])
	}

	// Each « vlan N » opens a stanza that runs to the next one. Splitting on the
	// opener rather than on « exit » is deliberate: the configuration uses « exit »
	// for interface blocks too, and a stanza scanner keyed on it would swallow them.
	var locs = reVlanOpen.FindAllStringSubmatchIndex(out, -1)
	var i int
	for i = 0; i < len(locs); i++ {
		var id, err = strconv.Atoi(out[locs[i][2]:locs[i][3]])
		if err != nil {
			continue
		}
		var end = len(out)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		var body = out[locs[i][1]:end]
		// Stop the stanza at its own « exit »: anything after it belongs to the
		// next block, and on this platform interface stanzas follow the VLANs.
		// Matched as a LINE, for the same carriage-return reason as above — a
		// plain search for "\nexit" never fires against this device.
		var cut = reExitLine.FindStringIndex(body)
		if cut != nil {
			body = body[:cut[0]]
		}

		rc.VLANs[id] = ""
		if m := reVlanName.FindStringSubmatch(body); m != nil {
			rc.VLANs[id] = strings.TrimSpace(m[1])
		}
		if m := reIPAddr.FindStringSubmatch(body); m != nil {
			// The FIRST VLAN carrying a real address wins as management. A
			// dhcp-bootp line on VLAN 1 is not one: it is the factory default and
			// naming it management would point provisioning at the wrong VLAN.
			var addr = strings.TrimSpace(m[1])
			if rc.MgmtVLAN == 0 && !strings.HasPrefix(addr, "dhcp") {
				rc.MgmtVLAN, rc.MgmtAddr = id, addr
			}
		}
		var mm []string
		for _, mm = range reUntagged.FindAllStringSubmatch(body, -1) {
			if strings.TrimSpace(mm[1]) != "" {
				continue // « no untagged … » is a removal, not a membership
			}
			rc.Untagged[id] = append(rc.Untagged[id], parsePortRange(mm[2])...)
		}
		for _, mm = range reTagged.FindAllStringSubmatch(body, -1) {
			if strings.TrimSpace(mm[1]) != "" {
				continue
			}
			rc.Tagged[id] = append(rc.Tagged[id], parsePortRange(mm[2])...)
		}
	}
	return rc
}

// parsePortRange expands this platform's compact port notation: "45-52",
// "1,3,5", "1-4,9". Returned sorted and deduplicated.
//
// The compact form is not cosmetic here — it is how the device WRITES its
// configuration and how it ACCEPTS commands, so the same grammar is needed in
// both directions (cf. formatPortRange).
func parsePortRange(s string) []int {
	var out []int
	var seen = map[int]bool{}
	var part string
	for _, part = range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var lo, hi int
		var err error
		if dash := strings.Index(part, "-"); dash > 0 {
			lo, err = strconv.Atoi(strings.TrimSpace(part[:dash]))
			if err != nil {
				continue
			}
			hi, err = strconv.Atoi(strings.TrimSpace(part[dash+1:]))
			if err != nil {
				continue
			}
		} else {
			lo, err = strconv.Atoi(part)
			if err != nil {
				continue
			}
			hi = lo
		}
		if lo > hi || lo < 1 || hi > 4096 {
			continue
		}
		var n int
		for n = lo; n <= hi; n++ {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sortInts(out)
	return out
}

// formatPortRange collapses a port list back into the compact notation the CLI
// expects. The inverse of parsePortRange, and tested against it.
func formatPortRange(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	var s = append([]int(nil), ports...)
	sortInts(s)
	var parts []string
	var i int
	for i = 0; i < len(s); i++ {
		var start = s[i]
		for i+1 < len(s) && s[i+1] == s[i]+1 {
			i++
		}
		if s[i] == start {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, strconv.Itoa(start)+"-"+strconv.Itoa(s[i]))
		}
	}
	return strings.Join(parts, ",")
}

func sortInts(v []int) {
	var i, j int
	for i = 1; i < len(v); i++ {
		var x = v[i]
		for j = i; j > 0 && v[j-1] > x; j-- {
			v[j] = v[j-1]
		}
		v[j] = x
	}
}

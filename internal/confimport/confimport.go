// Package confimport traduit la configuration lue sur un device vers le
// model de data standard de meshcraft (VLANs + intent par port).
//
// C'est le chemin « import de la configuration locale » propose a l'adoption :
// au lieu d'ecraser the device avec ce que la plateforme croit juste, on lit
// ce qui existe et on le fait entrer dans le model. L'utilisateur voit donc
// from l'adoption son plan de VLAN reel.
//
// Deux dialectes sont couverts, ceux qui cadrent la majorite des CLI de switchs :
//
//   - « ios » : Cisco IOS/IOS-XE/NX-OS, et les nombreuses CLI qui l'imitent
//     (Aruba/HPE, Dell, Netgear, TP-Link JetStream, Zyxel…) ;
//   - « comware » : Huawei VRP et H3C/Comware.
//
// Ce qui n'est pas reconnu n'est pas invented : chaque ligne de configuration
// ignored qui portait pourtant du sens VLAN produit un avertissement, et
// l'utilisateur voit exactement ce qui a been importe avant de valider.
package confimport

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Vlan struct {
	VlanID int    `json:"vlan_id"`
	Name   string `json:"name"`
}

// Port suit le model standard : mode, PVID, VLANs tagges.
type Port struct {
	Label  string `json:"label,omitempty"`
	Mode   string `json:"mode"` // libre | access | trunk
	PVID   *int   `json:"pvid,omitempty"`
	Tagged any    `json:"tagged,omitempty"` // []int ou "all"
}

// PortGroup describes la front panel physique deduced des noms d'interfaces.
type PortGroup struct {
	Prefix    string `json:"prefix"`
	From      int    `json:"from"`
	To        int    `json:"to"`
	SpeedMbps int    `json:"speed_mbps"`
	PoE       bool   `json:"poe"`
	Label     string `json:"label,omitempty"`
}

type Result struct {
	Dialect    string          `json:"dialect"`
	Hostname   string          `json:"hostname,omitempty"`
	Vlans      []Vlan          `json:"vlans"`
	Ports      map[string]Port `json:"ports"`
	PortGroups []PortGroup     `json:"port_groups"`
	Warnings   []string        `json:"warnings"`
}

// DetectDialect devine le dialecte a partir de marqueurs de syntaxe.
func DetectDialect(text string) string {
	var lower = strings.ToLower(text)
	if strings.Contains(lower, "port link-type") || strings.Contains(lower, "allow-pass") ||
		strings.Contains(lower, "port default vlan") {
		return "comware"
	}
	if strings.Contains(lower, "switchport") || strings.Contains(lower, "interface ") {
		return "ios"
	}
	return ""
}

var (
	reHostname     = regexp.MustCompile(`(?i)^\s*(?:hostname|sysname)\s+"?([^\s"]+)"?`)
	reVlanDecl     = regexp.MustCompile(`(?i)^\s*vlan\s+([0-9,\s\-]+?)\s*$`)
	reVlanName     = regexp.MustCompile(`(?i)^\s*name\s+"?(.+?)"?\s*$`)
	reInterface    = regexp.MustCompile(`(?i)^\s*interface\s+(\S+)\s*$`)
	reDescription  = regexp.MustCompile(`(?i)^\s*description\s+"?(.+?)"?\s*$`)
	reSwMode       = regexp.MustCompile(`(?i)^\s*switchport\s+mode\s+(access|trunk)`)
	reSwAccess     = regexp.MustCompile(`(?i)^\s*switchport\s+access\s+vlan\s+(\d+)`)
	reSwTrunkNat   = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+native\s+vlan\s+(\d+)`)
	reSwTrunkAllow = regexp.MustCompile(`(?i)^\s*switchport\s+trunk\s+allowed\s+vlan\s+(?:add\s+)?(\S+)`)
	reSwVoice      = regexp.MustCompile(`(?i)^\s*switchport\s+voice\s+vlan\s+(\d+)`)
	reNoSwitchport = regexp.MustCompile(`(?i)^\s*no\s+switchport\s*$`)
	reCwLinkType   = regexp.MustCompile(`(?i)^\s*port\s+link-type\s+(access|trunk|hybrid)`)
	reCwDefVlan    = regexp.MustCompile(`(?i)^\s*port\s+default\s+vlan\s+(\d+)`)
	reCwTrunkPvid  = regexp.MustCompile(`(?i)^\s*port\s+trunk\s+pvid\s+vlan\s+(\d+)`)
	reCwTrunkPerm  = regexp.MustCompile(`(?i)^\s*port\s+trunk\s+(?:permit|allow-pass)\s+vlan\s+(.+?)\s*$`)
	reCwHybridVlan = regexp.MustCompile(`(?i)^\s*port\s+hybrid\s+(?:tagged|untagged)\s+vlan\s+(.+?)\s*$`)
	rePoE          = regexp.MustCompile(`(?i)^\s*(?:power\s+inline|poe\s+enable|poe-power)`)
	reIfaceIndex   = regexp.MustCompile(`^(.*?)(\d+)$`)
)

// Parse lit une configuration et la traduit. dialect vide = detection auto.
func Parse(text, dialect string) (Result, error) {
	if strings.TrimSpace(text) == "" {
		return Result{}, fmt.Errorf("configuration vide")
	}
	if dialect == "" {
		dialect = DetectDialect(text)
	}
	if dialect == "" {
		return Result{}, fmt.Errorf(
			"dialecte non reconnu : aucune syntaxe VLAN connue trouvee. " +
				"Collez la sortie de « show running-config » (Cisco/Aruba/Dell…) " +
				"ou « display current-configuration » (Huawei/H3C)")
	}

	var res = Result{
		Dialect: dialect,
		Vlans:   []Vlan{},
		Ports:   map[string]Port{},
		// PortGroups reste non nil : l'UI itere dessus sans garde.
		PortGroups: []PortGroup{},
		Warnings:   []string{},
	}

	// Contexte courant : on est soit dans un bloc vlan, soit dans une interface.
	var curVlans []int
	var curIface string
	var poeSeen = map[string]bool{}

	var lines = strings.Split(strings.ReplaceAll(text, "\r", ""), "\n")
	var raw string
	for _, raw = range lines {
		var line = strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		// Un « ! » ou « return » seul ferme le bloc courant.
		var trimmed = strings.TrimSpace(line)
		if trimmed == "!" || trimmed == "#" || strings.EqualFold(trimmed, "return") ||
			strings.EqualFold(trimmed, "exit") || strings.EqualFold(trimmed, "quit") {
			curVlans = nil
			curIface = ""
			continue
		}

		var m []string

		if m = reHostname.FindStringSubmatch(line); m != nil {
			res.Hostname = m[1]
			continue
		}

		if m = reInterface.FindStringSubmatch(line); m != nil {
			curVlans = nil
			curIface = m[1]
			// Les interfaces logiques (Vlan10, Loopback0, Null0…) ne sont pas
			// des ports physiques : on ne les fait pas entrer dans la front panel.
			if isLogicalIface(curIface) {
				curIface = ""
				continue
			}
			var _, exists = res.Ports[curIface]
			if !exists {
				res.Ports[curIface] = Port{Mode: "libre"}
			}
			continue
		}

		if m = reVlanDecl.FindStringSubmatch(line); m != nil && curIface == "" {
			var ids, err = expandVlanList(m[1])
			if err != nil {
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("liste de VLAN illisible ignored : %q", trimmed))
				continue
			}
			curVlans = ids
			var id int
			for _, id = range ids {
				if findVlan(res.Vlans, id) < 0 {
					res.Vlans = append(res.Vlans, Vlan{VlanID: id})
				}
			}
			continue
		}

		// Dans un bloc vlan : « name » nomme le VLAN.
		if curVlans != nil && curIface == "" {
			if m = reVlanName.FindStringSubmatch(line); m != nil {
				if len(curVlans) == 1 {
					var idx = findVlan(res.Vlans, curVlans[0])
					if idx >= 0 {
						res.Vlans[idx].Name = m[1]
					}
				} else {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("« name %s » porte sur %d VLANs a la fois : nom non applique",
							m[1], len(curVlans)))
				}
				continue
			}
		}

		if curIface == "" {
			continue
		}

		// --- Dans une interface ---
		var p = res.Ports[curIface]

		switch {
		case reNoSwitchport.MatchString(line):
			// Port route : hors du model L2, on le sort de la front panel pour ne
			// pas afficher un port « libre » qui ne l'est pas.
			delete(res.Ports, curIface)
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s est un port route (no switchport) : non importe", curIface))
			curIface = ""
			continue

		case reDescription.MatchString(line):
			p.Label = reDescription.FindStringSubmatch(line)[1]

		case rePoE.MatchString(line):
			poeSeen[curIface] = true

		// --- dialecte ios ---
		case reSwMode.MatchString(line):
			p.Mode = strings.ToLower(reSwMode.FindStringSubmatch(line)[1])

		case reSwAccess.MatchString(line):
			var vid, _ = strconv.Atoi(reSwAccess.FindStringSubmatch(line)[1])
			p.PVID = &vid
			if p.Mode == "" || p.Mode == "libre" {
				p.Mode = "access"
			}
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reSwVoice.MatchString(line):
			var vid, _ = strconv.Atoi(reSwVoice.FindStringSubmatch(line)[1])
			p.Tagged = addTagged(p.Tagged, []int{vid})
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reSwTrunkNat.MatchString(line):
			var vid, _ = strconv.Atoi(reSwTrunkNat.FindStringSubmatch(line)[1])
			p.PVID = &vid
			p.Mode = "trunk"
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reSwTrunkAllow.MatchString(line):
			p.Mode = "trunk"
			var spec = reSwTrunkAllow.FindStringSubmatch(line)[1]
			if strings.EqualFold(spec, "all") || strings.EqualFold(spec, "1-4094") {
				p.Tagged = "all"
			} else {
				var ids, err = expandVlanList(spec)
				if err != nil {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("%s : liste de VLAN trunk illisible (%q)", curIface, spec))
				} else {
					p.Tagged = addTagged(p.Tagged, ids)
					res.Vlans = ensureVlans(res.Vlans, ids)
				}
			}

		// --- dialecte comware / VRP ---
		case reCwLinkType.MatchString(line):
			var t = strings.ToLower(reCwLinkType.FindStringSubmatch(line)[1])
			if t == "hybrid" {
				p.Mode = "trunk"
				res.Warnings = append(res.Warnings,
					fmt.Sprintf("%s est en mode hybrid : importe en trunk, a verify", curIface))
			} else {
				p.Mode = t
			}

		case reCwDefVlan.MatchString(line):
			var vid, _ = strconv.Atoi(reCwDefVlan.FindStringSubmatch(line)[1])
			p.PVID = &vid
			if p.Mode == "" || p.Mode == "libre" {
				p.Mode = "access"
			}
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reCwTrunkPvid.MatchString(line):
			var vid, _ = strconv.Atoi(reCwTrunkPvid.FindStringSubmatch(line)[1])
			p.PVID = &vid
			p.Mode = "trunk"
			res.Vlans = ensureVlan(res.Vlans, vid)

		case reCwTrunkPerm.MatchString(line):
			p.Mode = "trunk"
			var spec = reCwTrunkPerm.FindStringSubmatch(line)[1]
			if strings.EqualFold(strings.TrimSpace(spec), "all") {
				p.Tagged = "all"
			} else {
				var ids, err = expandVlanList(spec)
				if err != nil {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("%s : liste de VLAN trunk illisible (%q)", curIface, spec))
				} else {
					p.Tagged = addTagged(p.Tagged, ids)
					res.Vlans = ensureVlans(res.Vlans, ids)
				}
			}

		case reCwHybridVlan.MatchString(line):
			var ids, err = expandVlanList(reCwHybridVlan.FindStringSubmatch(line)[1])
			if err == nil {
				p.Tagged = addTagged(p.Tagged, ids)
				res.Vlans = ensureVlans(res.Vlans, ids)
			}
		}

		res.Ports[curIface] = p
	}

	// Un trunk sans liste explicite laisse passer tous les VLANs sur la
	// plupart des plateformes : le dire rather que d'afficher un trunk vide.
	var name string
	var p Port
	for name, p = range res.Ports {
		if p.Mode == "trunk" && p.Tagged == nil {
			p.Tagged = "all"
			res.Ports[name] = p
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s : trunk sans liste de VLAN, interprete comme « tous »", name))
		}
	}

	res.PortGroups = deriveGroups(res.Ports, poeSeen)
	sort.Slice(res.Vlans, func(i, j int) bool { return res.Vlans[i].VlanID < res.Vlans[j].VlanID })

	if len(res.Ports) == 0 {
		return res, fmt.Errorf("aucun port trouve dans la configuration fournie")
	}
	return res, nil
}

func isLogicalIface(name string) bool {
	var lower = strings.ToLower(name)
	var prefix string
	for _, prefix = range []string{
		"vlan", "loopback", "lo", "null", "tunnel", "port-channel", "po",
		"bridge-aggregation", "eth-trunk", "management", "mgmt", "bvi", "nve",
	} {
		if strings.HasPrefix(lower, prefix) {
			// « ethernet » commence par « eth » mais n'est pas « eth-trunk ».
			if prefix == "lo" && strings.HasPrefix(lower, "log") {
				continue
			}
			return true
		}
	}
	return false
}

// expandVlanList accepte « 10,20,30-35 » (ios) et « 10 20 30 to 35 » (comware).
func expandVlanList(spec string) ([]int, error) {
	var norm = strings.ToLower(strings.TrimSpace(spec))
	norm = strings.ReplaceAll(norm, " to ", "-")
	norm = strings.ReplaceAll(norm, ",", " ")
	var out = []int{}
	var seen = map[int]bool{}
	var tok string
	for _, tok = range strings.Fields(norm) {
		if strings.Contains(tok, "-") {
			var parts = strings.SplitN(tok, "-", 2)
			var lo, errLo = strconv.Atoi(strings.TrimSpace(parts[0]))
			var hi, errHi = strconv.Atoi(strings.TrimSpace(parts[1]))
			if errLo != nil || errHi != nil || lo < 1 || hi > 4094 || hi < lo {
				return nil, fmt.Errorf("intervalle invalide %q", tok)
			}
			var v int
			for v = lo; v <= hi; v++ {
				if !seen[v] {
					seen[v] = true
					out = append(out, v)
				}
			}
			continue
		}
		var v, err = strconv.Atoi(tok)
		if err != nil || v < 1 || v > 4094 {
			return nil, fmt.Errorf("identifiant de VLAN invalide %q", tok)
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("liste vide")
	}
	sort.Ints(out)
	return out, nil
}

func findVlan(list []Vlan, id int) int {
	var i int
	for i = range list {
		if list[i].VlanID == id {
			return i
		}
	}
	return -1
}

func ensureVlan(list []Vlan, id int) []Vlan {
	if findVlan(list, id) < 0 {
		return append(list, Vlan{VlanID: id})
	}
	return list
}

func ensureVlans(list []Vlan, ids []int) []Vlan {
	var id int
	for _, id = range ids {
		list = ensureVlan(list, id)
	}
	return list
}

// addTagged fusionne des VLANs tagges. « all » absorbe tout : une fois qu'un
// port laisse tout passer, ajouter des identifiants ne veut plus rien dire.
func addTagged(cur any, ids []int) any {
	var asAll, isAll = cur.(string)
	if isAll && asAll == "all" {
		return "all"
	}
	var out = []int{}
	var prev, isList = cur.([]int)
	if isList {
		out = append(out, prev...)
	}
	var seen = map[int]bool{}
	var v int
	for _, v = range out {
		seen[v] = true
	}
	for _, v = range ids {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Ints(out)
	return out
}

// deriveGroups reconstitue la front panel physique a partir des noms d'interfaces :
// « GigabitEthernet1/0/1 … 1/0/24 » devient un groupe de 24 ports. La vitesse
// est deduced du prefix, faute d'information dans la configuration.
func deriveGroups(ports map[string]Port, poe map[string]bool) []PortGroup {
	type acc struct {
		min, max int
		poe      bool
	}
	var byPrefix = map[string]*acc{}
	var name string
	for name = range ports {
		var m = reIfaceIndex.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		var prefix = m[1]
		var idx, err = strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		var a = byPrefix[prefix]
		if a == nil {
			byPrefix[prefix] = &acc{min: idx, max: idx, poe: poe[name]}
			continue
		}
		if idx < a.min {
			a.min = idx
		}
		if idx > a.max {
			a.max = idx
		}
		if poe[name] {
			a.poe = true
		}
	}
	var out = []PortGroup{}
	var prefix string
	var a *acc
	for prefix, a = range byPrefix {
		out = append(out, PortGroup{
			Prefix: prefix, From: a.min, To: a.max,
			SpeedMbps: speedOf(prefix), PoE: a.poe,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// speedOf deduit un rate du nom d'interface. C'est une convention de nommage
// constructeur, pas une mesure : la configuration ne porte pas la vitesse.
func speedOf(prefix string) int {
	var lower = strings.ToLower(prefix)
	switch {
	case strings.Contains(lower, "hundredgig"):
		return 100000
	case strings.Contains(lower, "fortygig"):
		return 40000
	case strings.Contains(lower, "twentyfivegig"):
		return 25000
	case strings.Contains(lower, "tengig") || strings.Contains(lower, "xge"):
		return 10000
	case strings.Contains(lower, "fastethernet"):
		return 100
	case strings.Contains(lower, "gigabit") || strings.HasPrefix(lower, "ge") ||
		strings.HasPrefix(lower, "gi"):
		return 1000
	default:
		return 1000
	}
}

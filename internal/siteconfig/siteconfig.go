// Package siteconfig defines the "meshcraft_site_config" exchange format: ALL
// network configuration for a site in a single JSON, exportable and importable.
// This is the pivot data model on the IT side — vendor-independent: future
// connectors will translate this model to each platform (cf.
// docs/01-Architecture/Modele de donnees standard).
//
// Format rules:
//   - version carried by the meshcraft_site_config field (integer);
//   - all internal references are BY NAME (profile, group, device), never by id:
//     the file is portable between instances;
//   - import fully replaces site configuration, in one transaction (all or
//     nothing).
package siteconfig

import "encoding/json"

const Version = 1

type File struct {
	Version int  `json:"meshcraft_site_config"`
	Site    Site `json:"site"`

	Vlans          []Vlan    `json:"vlans"`
	PortProfiles   []Profile `json:"port_profiles"`
	DeviceProfiles []Profile `json:"device_profiles"`
	// SwitchProfiles : rules that produce port configuration
	// (cf. docs/01-Architecture/Profils de switchs). Their rules may reference a
	// port profile; the reference is translated to NAME on export by
	// RemapSwitchRules, like every other reference in the format.
	//
	// Absent from an older file: the field is simply empty, and import creates
	// none. No format version bump for an addition the old reader ignores and the
	// new one knows not to find.
	SwitchProfiles []Profile `json:"switch_profiles,omitempty"`
	DeviceGroups   []Group   `json:"device_groups"`
	Devices        []Device  `json:"devices"`
	Links          []Link    `json:"links"`
}

// RemapSwitchRules translates port profile references in a switch profile spec:
// `port_profile_id` (a uuid, instance-specific) becomes `port_profile` (a name,
// portable) on export, and the reverse on import.
//
// Translation works on RAW JSON structure, without going through `portrules`
// types. That is deliberate: a round trip through a struct would drop every
// field the current version does not know, and a file exported by a newer version
// would return stripped of what it knows how to do — silently, which is the worst
// way to lose configuration.
//
// A reference `lookup` does not know is LEFT IN PLACE rather than erased: it will
// be flagged on screen as a faulty rule (and block applying that profile), which
// is fixable. Erasing the reference would produce a rule with no configuration,
// therefore a port that silently receives what the next rule gives it.
func RemapSwitchRules(spec json.RawMessage, from, to string,
	lookup map[string]string) json.RawMessage {
	if len(spec) == 0 {
		return spec
	}
	var root map[string]any
	if json.Unmarshal(spec, &root) != nil {
		return spec
	}
	var rules, ok = root["rules"].([]any)
	if !ok {
		return spec
	}
	var changed bool
	var item any
	for _, item = range rules {
		var rule, isObject = item.(map[string]any)
		if !isObject {
			continue
		}
		var ref, isString = rule[from].(string)
		if !isString || ref == "" {
			continue
		}
		var mapped, known = lookup[ref]
		if !known {
			continue
		}
		delete(rule, from)
		rule[to] = mapped
		changed = true
	}
	if !changed {
		return spec
	}
	var out, err = json.Marshal(root)
	if err != nil {
		return spec
	}
	return out
}

// Reference keys for a port profile in a switch profile rule.
const (
	// RefByID is what the database stores.
	RefByID = "port_profile_id"
	// RefByName is what the exchange file carries.
	RefByName = "port_profile"
)

type Site struct {
	Name string `json:"name"`
	// Synoptic plan: {width, height, zones:[{x,y,w,h,label}]}
	Plan json.RawMessage `json:"plan,omitempty"`
}

type Vlan struct {
	VlanID  int    `json:"vlan_id"`
	Name    string `json:"name"`
	Color   string `json:"color"`
	Purpose string `json:"purpose,omitempty"`
}

// Profile : spec in standard model. For a port profile:
// {description, mode: access|trunk, pvid, tagged: [ids]|"all", poe, eng_only}
type Profile struct {
	Name string          `json:"name"`
	Spec json.RawMessage `json:"spec"`
}

type Group struct {
	Name          string `json:"name"`
	DeviceProfile string `json:"device_profile,omitempty"` // reference by name
}

type Device struct {
	Name       string          `json:"name"`
	Model      string          `json:"model,omitempty"`
	Vendor     string          `json:"vendor,omitempty"`
	Location   string          `json:"location,omitempty"`
	MgmtIP     string          `json:"mgmt_ip,omitempty"`
	Status     string          `json:"status,omitempty"`
	StatusNote string          `json:"status_note,omitempty"`
	Group      string          `json:"group,omitempty"` // reference by name
	PoEBudgetW int             `json:"poe_budget_w,omitempty"`
	Position   Position        `json:"position"`
	PortGroups json.RawMessage `json:"port_groups,omitempty"`
	// Ports by canonical name. Each port:
	// {label, mode: access|trunk|libre, pvid, tagged: [ids]|"all", poe,
	//  link, poe_w, rx_mbps, tx_mbps} — last 4 = last observed state.
	Ports json.RawMessage `json:"ports,omitempty"`
	Extra json.RawMessage `json:"extra,omitempty"`
}

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type Link struct {
	A      string `json:"a"` // device name
	APort  string `json:"a_port,omitempty"`
	B      string `json:"b"`
	BPort  string `json:"b_port,omitempty"`
	Speed  string `json:"speed,omitempty"`
	Status string `json:"status,omitempty"` // up | warn | down
	Note   string `json:"note,omitempty"`
}

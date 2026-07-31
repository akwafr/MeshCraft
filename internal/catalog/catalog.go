// Package catalog exposess device models known to the controller.
//
// Current phase: Generic switches only (vendor: generic),
// templates for mocking a site and validating the data model — NOT for
// driving real equipment. When connectors arrive, this catalog will be
// fed by the driver registry (a model catalog per platform, cf.
// docs/01-Architecture/Connectors).
package catalog

import "encoding/json"

// PortGroup describes a physically homogeneous block of ports.
type PortGroup struct {
	Prefix string `json:"prefix"` // canonical name: prefix + number (ether1…)
	Count  int    `json:"count"`
	PoE    bool   `json:"poe,omitempty"`
	Speed  string `json:"speed"` // 1G | 10G…
	Label  string `json:"label"` // block label in the UI
}

type Model struct {
	Model      string      `json:"model"`
	Vendor     string      `json:"vendor"`
	Desc       string      `json:"desc"`
	PoEBudgetW int         `json:"poe_budget_w"`
	PortGroups []PortGroup `json:"port_groups"`
}

// Generic: neutral templates covering common form factors.
var Models = []Model{
	{
		Model: "Generic-8P-2S", Vendor: "generic",
		Desc: "8x 1G PoE ports + 2x 1G SFP — edge switch", PoEBudgetW: 120,
		PortGroups: []PortGroup{
			{Prefix: "ether", Count: 8, PoE: true, Speed: "1G", Label: "ETHER 1–8 · PoE"},
			{Prefix: "sfp", Count: 2, Speed: "1G", Label: "SFP"},
		},
	},
	{
		Model: "Generic-16P-2X", Vendor: "generic",
		Desc: "16x 1G PoE ports + 2x 10G SFP+ — floor / zone", PoEBudgetW: 240,
		PortGroups: []PortGroup{
			{Prefix: "ether", Count: 16, PoE: true, Speed: "1G", Label: "ETHER 1–16 · PoE"},
			{Prefix: "sfp-sfpplus", Count: 2, Speed: "10G", Label: "SFP+"},
		},
	},
	{
		Model: "Generic-24P-4X", Vendor: "generic",
		Desc: "24x 1G PoE+ ports + 4x 10G SFP+ — distribution", PoEBudgetW: 450,
		PortGroups: []PortGroup{
			{Prefix: "ether", Count: 24, PoE: true, Speed: "1G", Label: "ETHER 1–24 · PoE+"},
			{Prefix: "sfp-sfpplus", Count: 4, Speed: "10G", Label: "SFP+"},
		},
	},
	{
		Model: "Generic-24G-2X", Vendor: "generic",
		Desc: "24x 1G ports + 2x 10G SFP+ — distribution without PoE", PoEBudgetW: 0,
		PortGroups: []PortGroup{
			{Prefix: "ether", Count: 24, Speed: "1G", Label: "ETHER 1–24"},
			{Prefix: "sfp-sfpplus", Count: 2, Speed: "10G", Label: "SFP+"},
		},
	},
	{
		Model: "Generic-48P-4X", Vendor: "generic",
		Desc: "48x 1G PoE+ ports + 4x 10G SFP+ — large building", PoEBudgetW: 740,
		PortGroups: []PortGroup{
			{Prefix: "ether", Count: 48, PoE: true, Speed: "1G", Label: "ETHER 1–48 · PoE+"},
			{Prefix: "sfp-sfpplus", Count: 4, Speed: "10G", Label: "SFP+"},
		},
	},
	{
		Model: "Generic-AGG-16X", Vendor: "generic",
		Desc: "16x 10G SFP+ + 1 management port — aggregation / core", PoEBudgetW: 0,
		PortGroups: []PortGroup{
			{Prefix: "ether", Count: 1, Speed: "1G", Label: "MGMT"},
			{Prefix: "sfp-sfpplus", Count: 16, Speed: "10G", Label: "SFP+ 1–16"},
		},
	},
}

// PortGroupsJSON returns a catalog model's port_groups ("" if unknown).
func PortGroupsJSON(modelName string) json.RawMessage {
	var m Model
	for _, m = range Models {
		if m.Model == modelName {
			var raw, err = json.Marshal(m.PortGroups)
			if err == nil {
				return raw
			}
		}
	}
	return nil
}

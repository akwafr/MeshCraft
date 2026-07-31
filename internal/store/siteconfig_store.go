package store

import (
	"context"
	"fmt"
	"sort"

	"meshcraft/internal/model"
	"meshcraft/internal/siteconfig"
)

// ExportSiteConfig serializes all site configuration in meshcraft_site_config v1 format
// (name references, portable).
func (s *Store) ExportSiteConfig(ctx context.Context, siteID string) (siteconfig.File, error) {
	var f = siteconfig.File{Version: siteconfig.Version}

	var site, err = s.GetSite(ctx, siteID)
	if err != nil {
		return f, err
	}
	var plan, planErr = s.GetSitePlan(ctx, siteID)
	if planErr != nil {
		return f, planErr
	}
	f.Site = siteconfig.Site{Name: site.Name, Plan: plan}

	var vlans, vErr = s.ListVlans(ctx, siteID)
	if vErr != nil {
		return f, vErr
	}
	f.Vlans = []siteconfig.Vlan{}
	var v model.Vlan
	for _, v = range vlans {
		f.Vlans = append(f.Vlans, siteconfig.Vlan{VlanID: v.VlanID, Name: v.Name, Color: v.Color, Purpose: v.Purpose})
	}

	// Port profiles are exported first and their names kept: switch profile rules
	// reference them by id, and the exchange format carries only names.
	var portProfileName = map[string]string{}
	var kind string
	for _, kind = range []string{"port", "device", "switch"} {
		var profs, pErr = s.ListProfiles(ctx, kind, siteID)
		if pErr != nil {
			return f, pErr
		}
		var list = []siteconfig.Profile{}
		var p model.Profile
		for _, p = range profs {
			var spec = p.Spec
			if kind == "port" {
				portProfileName[p.ID] = p.Name
			}
			if kind == "switch" {
				spec = siteconfig.RemapSwitchRules(spec,
					siteconfig.RefByID, siteconfig.RefByName, portProfileName)
			}
			list = append(list, siteconfig.Profile{Name: p.Name, Spec: spec})
		}
		switch kind {
		case "port":
			f.PortProfiles = list
		case "device":
			f.DeviceProfiles = list
		default:
			f.SwitchProfiles = list
		}
	}

	var groups, gErr = s.ListGroups(ctx, siteID)
	if gErr != nil {
		return f, gErr
	}
	f.DeviceGroups = []siteconfig.Group{}
	var g model.DeviceGroup
	for _, g = range groups {
		var ref = ""
		if g.DeviceProfileName != nil {
			ref = *g.DeviceProfileName
		}
		f.DeviceGroups = append(f.DeviceGroups, siteconfig.Group{Name: g.Name, DeviceProfile: ref})
	}

	var devices, dErr = s.ListDevices(ctx, siteID)
	if dErr != nil {
		return f, dErr
	}
	var groupName = map[string]string{}
	for _, g = range groups {
		groupName[g.ID] = g.Name
	}
	var deviceName = map[string]string{}
	f.Devices = []siteconfig.Device{}
	var d model.Device
	for _, d = range devices {
		deviceName[d.ID] = d.Name
		var grp = ""
		if d.GroupID != nil {
			grp = groupName[*d.GroupID]
		}
		f.Devices = append(f.Devices, siteconfig.Device{
			Name: d.Name, Model: d.Model, Vendor: d.Vendor, Location: d.Location,
			MgmtIP: d.MgmtIP, Status: d.Status, StatusNote: d.StatusNote, Group: grp,
			PoEBudgetW: d.PoEBudgetW,
			Position:   siteconfig.Position{X: d.PosX, Y: d.PosY},
			PortGroups: d.PortGroups, Ports: d.Ports, Extra: d.Extra,
		})
	}

	var links, lErr = s.ListLinks(ctx, siteID)
	if lErr != nil {
		return f, lErr
	}
	f.Links = []siteconfig.Link{}
	var l model.DeviceLink
	for _, l = range links {
		f.Links = append(f.Links, siteconfig.Link{
			A: deviceName[l.ADevice], APort: l.APort,
			B: deviceName[l.BDevice], BPort: l.BPort,
			Speed: l.Speed, Status: l.Status, Note: l.Note,
		})
	}
	// Deterministic order: export -> import -> export must be stable.
	sort.Slice(f.Links, func(i, j int) bool {
		var a, b = f.Links[i], f.Links[j]
		if a.A != b.A {
			return a.A < b.A
		}
		if a.APort != b.APort {
			return a.APort < b.APort
		}
		return a.B < b.B
	})
	return f, nil
}

// ImportSiteConfig REPLACES all site configuration with the file contents in a single
// transaction. The site name is not changed (the file may come from a differently named site).
func (s *Store) ImportSiteConfig(ctx context.Context, siteID string, f siteconfig.File) error {
	if f.Version != siteconfig.Version {
		return fmt.Errorf("unsupported file version: %d (expected %d)", f.Version, siteconfig.Version)
	}
	var tx, err = s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Purge existing (devices -> links cascade).
	var table string
	for _, table = range []string{"devices", "device_groups", "device_profiles",
		"switch_profiles", "port_profiles", "vlans"} {
		if _, err = tx.Exec(ctx, `delete from `+table+` where site_id = $1::uuid`, siteID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `update sites set plan = $2 where id = $1::uuid`,
		siteID, jsonOr(f.Site.Plan, `{}`)); err != nil {
		return err
	}

	var v siteconfig.Vlan
	for _, v = range f.Vlans {
		if v.Color == "" {
			v.Color = "#7c8cf8"
		}
		if _, err = tx.Exec(ctx,
			`insert into vlans (site_id, vlan_id, name, color, purpose) values ($1::uuid, $2, $3, $4, $5)`,
			siteID, v.VlanID, v.Name, v.Color, v.Purpose); err != nil {
			return fmt.Errorf("vlan %d: %w", v.VlanID, err)
		}
	}

	var portProfileID = map[string]string{}
	var p siteconfig.Profile
	for _, p = range f.PortProfiles {
		var id string
		if err = tx.QueryRow(ctx,
			`insert into port_profiles (site_id, name, spec) values ($1::uuid, $2, $3) returning id::text`,
			siteID, p.Name, jsonOr(p.Spec, `{}`)).Scan(&id); err != nil {
			return fmt.Errorf("port profile %q: %w", p.Name, err)
		}
		portProfileID[p.Name] = id
	}
	// Switch profiles after port profiles: their rules may point at a port profile, and
	// the name reference must find an id just assigned by THIS instance.
	for _, p = range f.SwitchProfiles {
		var spec = siteconfig.RemapSwitchRules(jsonOr(p.Spec, `{}`),
			siteconfig.RefByName, siteconfig.RefByID, portProfileID)
		if _, err = tx.Exec(ctx,
			`insert into switch_profiles (site_id, name, spec) values ($1::uuid, $2, $3)`,
			siteID, p.Name, spec); err != nil {
			return fmt.Errorf("switch profile %q: %w", p.Name, err)
		}
	}
	var deviceProfileID = map[string]string{}
	for _, p = range f.DeviceProfiles {
		var id string
		if err = tx.QueryRow(ctx,
			`insert into device_profiles (site_id, name, spec) values ($1::uuid, $2, $3) returning id::text`,
			siteID, p.Name, jsonOr(p.Spec, `{}`)).Scan(&id); err != nil {
			return fmt.Errorf("device profile %q: %w", p.Name, err)
		}
		deviceProfileID[p.Name] = id
	}

	var groupID = map[string]string{}
	var g siteconfig.Group
	for _, g = range f.DeviceGroups {
		var profRef *string
		if g.DeviceProfile != "" {
			var id, ok = deviceProfileID[g.DeviceProfile]
			if !ok {
				return fmt.Errorf("group %q: unknown device profile %q", g.Name, g.DeviceProfile)
			}
			profRef = &id
		}
		var id string
		if err = tx.QueryRow(ctx,
			`insert into device_groups (site_id, name, device_profile_id) values ($1::uuid, $2, $3::uuid) returning id::text`,
			siteID, g.Name, profRef).Scan(&id); err != nil {
			return fmt.Errorf("group %q: %w", g.Name, err)
		}
		groupID[g.Name] = id
	}

	var deviceID = map[string]string{}
	var d siteconfig.Device
	for _, d = range f.Devices {
		var grpRef *string
		if d.Group != "" {
			var id, ok = groupID[d.Group]
			if !ok {
				return fmt.Errorf("device %q: unknown group %q", d.Name, d.Group)
			}
			grpRef = &id
		}
		// Imported file `status` is IGNORED, deliberately: a configuration file describes
		// intent, not observation. Accepting it would import an "ok" no ping established,
		// exactly what removing this field from the API forbids (cf. store.UpdateDevice).
		// Imported devices therefore start "unknown" — the column default — until the
		// first monitoring observation. The field remains in the format for EXPORT, where
		// it faithfully describes what was observed at export time.
		var id string
		if err = tx.QueryRow(ctx,
			`insert into devices (site_id, group_id, name, model, vendor, location, mgmt_ip,
			    poe_budget_w, pos_x, pos_y, port_groups, ports, extra)
			 values ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			 returning id::text`,
			siteID, grpRef, d.Name, d.Model, d.Vendor, d.Location, d.MgmtIP,
			d.PoEBudgetW, d.Position.X, d.Position.Y,
			jsonOr(d.PortGroups, `[]`), jsonOr(d.Ports, `{}`), jsonOr(d.Extra, `{}`)).Scan(&id); err != nil {
			return fmt.Errorf("device %q: %w", d.Name, err)
		}
		deviceID[d.Name] = id
	}

	var l siteconfig.Link
	for _, l = range f.Links {
		var aID, aOK = deviceID[l.A]
		var bID, bOK = deviceID[l.B]
		if !aOK || !bOK {
			return fmt.Errorf("link %s<->%s: unknown device", l.A, l.B)
		}
		if l.Status == "" {
			l.Status = "up"
		}
		if _, err = tx.Exec(ctx,
			`insert into device_links (site_id, a_device, a_port, b_device, b_port, speed, status, note)
			 values ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, $7, $8)`,
			siteID, aID, l.APort, bID, l.BPort, l.Speed, l.Status, l.Note); err != nil {
			return fmt.Errorf("link %s<->%s: %w", l.A, l.B, err)
		}
	}
	return tx.Commit(ctx)
}

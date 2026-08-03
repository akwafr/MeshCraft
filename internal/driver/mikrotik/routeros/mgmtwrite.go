package routeros

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Management addressing WRITE paths on RouterOS: same-VLAN apply and dual-path
// VLAN migrate.
//
// REST Add/Set/Remove are already exercised by port apply (bridge vlan membership,
// users, SNMP). Addressing writes follow the same channel and the order locked in
// Capabilities().Mgmt.Notes — add the new path, verify, only then cut the old one.
//
// Caps ApplySameVLAN / MigrateVLAN are set on the driver: OutOfBand recovery
// (serial / MAC-winbox) exists on this platform, unlike web-only lines.

var (
	_ driver.MgmtAddrWriter = (*Client)(nil)
	_ driver.MgmtMover      = (*Client)(nil)
)

// ApplyMgmtAddressing applies DHCP or static addressing on the CURRENT management
// interface — it does not change management VLAN ([MgmtMover] does).
func (c *Client) ApplyMgmtAddressing(ctx context.Context, want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {
	var out driver.MgmtAddrResult
	var st, err = c.ReadDeviceState(ctx)
	if err != nil {
		return out, err
	}
	if st.MgmtInterface == "" {
		return out, fmt.Errorf("no management interface found on this device: cannot apply addressing")
	}
	var iface = st.MgmtInterface

	switch want.Mode {
	case driver.MgmtAddrModeStatic:
		if want.Address == "" {
			return out, fmt.Errorf("static addressing requested without an address")
		}
		if err = c.ensureStaticOnIface(ctx, iface, want.Address); err != nil {
			return out, err
		}
		if err = c.removeDHCPClientsOn(ctx, iface); err != nil {
			return out, err
		}
		if err = c.applyGatewayDNS(ctx, want); err != nil {
			out.Notes = append(out.Notes, err.Error())
		}
	case driver.MgmtAddrModeDHCP:
		if err = c.removeStaticOnIface(ctx, iface, ""); err != nil {
			return out, err
		}
		if err = c.ensureDHCPClientOn(ctx, iface); err != nil {
			return out, err
		}
	default:
		return out, fmt.Errorf("unsupported addressing mode %q (expected static or dhcp)", want.Mode)
	}

	out.Observed, err = c.ReadMgmtAddressing(ctx)
	if err != nil {
		out.Notes = append(out.Notes, "re-read after apply failed: "+err.Error())
		err = nil
	}
	c.step(true, "management addressing applied",
		"mode '"+want.Mode+"' on "+iface)
	return out, nil
}

// PrepareMgmt adds administration on vlanID without removing the current path.
func (c *Client) PrepareMgmt(ctx context.Context, vlanID int) error {
	if vlanID <= 0 {
		return fmt.Errorf("management VLAN %d is not usable", vlanID)
	}
	var st, err = c.ReadDeviceState(ctx)
	if err != nil {
		return err
	}
	if st.BridgeName == "" {
		return fmt.Errorf("no bridge on this device: cannot prepare a management VLAN")
	}
	var uplinkTagged = uplinksFromMgmt(st)
	if err = c.ensureBridgeVLAN(ctx, st.BridgeName, vlanID, uplinkTagged); err != nil {
		return err
	}
	var ifName string
	if ifName, err = c.ensureVLANInterface(ctx, st.BridgeName, vlanID); err != nil {
		return err
	}
	if err = c.ensureDHCPClientOn(ctx, ifName); err != nil {
		return err
	}
	c.step(true, "management path prepared",
		"VLAN "+strconv.Itoa(vlanID)+" on "+ifName+" (DHCP client); current path kept")
	return nil
}

// MgmtAddressed reports whether the prepared VLAN interface has an address.
func (c *Client) MgmtAddressed(ctx context.Context, vlanID int) (bool, string, error) {
	var ifName, err = c.vlanIfaceName(ctx, vlanID)
	if err != nil {
		return false, "", err
	}
	if ifName == "" {
		return false, "", nil
	}
	var addrs []Row
	if addrs, err = c.List(ctx, "ip/address"); err != nil {
		return false, "", err
	}
	var r Row
	for _, r = range addrs {
		if r.Bool("disabled") || r.Bool("invalid") {
			continue
		}
		if r.Get("interface") != ifName && r.Get("actual-interface") != ifName {
			continue
		}
		var addr = r.Get("address")
		if addr != "" {
			return true, addr, nil
		}
	}
	return false, "", nil
}

// CommitMgmt removes the OLD management address path after the new one has an address.
//
// Caller must have seen MgmtAddressed=true. Does not persist separately — RouterOS is
// immediate; OutOfBand is the safety net.
func (c *Client) CommitMgmt(ctx context.Context, vlanID int) error {
	var addressed, addr, err = c.MgmtAddressed(ctx, vlanID)
	if err != nil {
		return err
	}
	if !addressed {
		return fmt.Errorf("refusing to commit: VLAN %d has no address yet — "+
			"switching would cut access with no replacement", vlanID)
	}
	var st DeviceState
	if st, err = c.ReadDeviceState(ctx); err != nil {
		return err
	}
	var newIface, _ = c.vlanIfaceName(ctx, vlanID)
	if st.MgmtInterface == "" || st.MgmtInterface == newIface {
		c.step(true, "management commit",
			"new path "+newIface+" ("+addr+") already carries administration; nothing to remove")
		return nil
	}
	var old = st.MgmtInterface
	if err = c.removeDHCPClientsOn(ctx, old); err != nil {
		return err
	}
	if err = c.removeStaticOnIface(ctx, old, ""); err != nil {
		return err
	}
	c.step(true, "management committed",
		"removed addressing on "+old+"; administration on "+newIface+" ("+addr+")")
	return nil
}

// --- helpers ---

func (c *Client) ensureStaticOnIface(ctx context.Context, iface, address string) error {
	var addrs, err = c.List(ctx, "ip/address")
	if err != nil {
		return err
	}
	var haveWant bool
	var r Row
	for _, r = range addrs {
		if r.Bool("disabled") {
			continue
		}
		if r.Get("interface") != iface && r.Get("actual-interface") != iface {
			continue
		}
		if r.Get("address") == address {
			haveWant = true
			break
		}
	}
	if !haveWant {
		if _, err = c.Add(ctx, "ip/address", map[string]string{
			"address":   address,
			"interface": iface,
		}); err != nil {
			return fmt.Errorf("add address %s on %s: %w", address, iface, err)
		}
	}
	// Drop other static entries on this iface after the wanted address exists
	// (session may drop if the IP changed — OutOfBand recovers on this platform).
	return c.removeStaticOnIface(ctx, iface, address)
}

func (c *Client) removeStaticOnIface(ctx context.Context, iface, keepAddress string) error {
	var addrs, err = c.List(ctx, "ip/address")
	if err != nil {
		return err
	}
	var r Row
	for _, r = range addrs {
		if r.Bool("disabled") || r.Bool("dynamic") {
			continue
		}
		if r.Get("interface") != iface && r.Get("actual-interface") != iface {
			continue
		}
		if keepAddress != "" && r.Get("address") == keepAddress {
			continue
		}
		if _, err = c.Remove(ctx, "ip/address", r.Get(".id")); err != nil {
			return fmt.Errorf("remove address %s: %w", r.Get("address"), err)
		}
	}
	return nil
}

func (c *Client) ensureDHCPClientOn(ctx context.Context, iface string) error {
	var clients, err = c.List(ctx, "ip/dhcp-client")
	if err != nil {
		return fmt.Errorf("list dhcp-client: %w", err)
	}
	var r Row
	for _, r = range clients {
		if r.Get("interface") == iface && !r.Bool("disabled") {
			return nil
		}
	}
	if _, err = c.Add(ctx, "ip/dhcp-client", map[string]string{
		"interface": iface,
		"disabled":  "no",
	}); err != nil {
		return fmt.Errorf("add dhcp-client on %s: %w", iface, err)
	}
	return nil
}

func (c *Client) removeDHCPClientsOn(ctx context.Context, iface string) error {
	var clients, err = c.List(ctx, "ip/dhcp-client")
	if err != nil {
		// Menu may be empty or restricted — state as note via caller if needed.
		return fmt.Errorf("list dhcp-client: %w", err)
	}
	var r Row
	for _, r = range clients {
		if r.Get("interface") != iface {
			continue
		}
		var id = r.Get(".id")
		if id == "" {
			continue
		}
		if _, err = c.Remove(ctx, "ip/dhcp-client", id); err != nil {
			return fmt.Errorf("remove dhcp-client on %s: %w", iface, err)
		}
	}
	return nil
}

func (c *Client) applyGatewayDNS(ctx context.Context, want driver.MgmtAddrIntent) error {
	if want.Gateway != "" {
		var routes, err = c.List(ctx, "ip/route")
		if err != nil {
			return fmt.Errorf("gateway not written: /ip/route list failed (%w)", err)
		}
		var existing string
		var r Row
		for _, r = range routes {
			if r.Get("dst-address") != "0.0.0.0/0" || r.Bool("disabled") {
				continue
			}
			if r.Get("gateway") == want.Gateway {
				existing = r.Get(".id")
				break
			}
			// Replace a different static default if present.
			if !r.Bool("dynamic") && r.Get(".id") != "" {
				if _, err = c.Remove(ctx, "ip/route", r.Get(".id")); err != nil {
					return fmt.Errorf("remove old default route: %w", err)
				}
			}
		}
		if existing == "" {
			if _, err = c.Add(ctx, "ip/route", map[string]string{
				"dst-address": "0.0.0.0/0",
				"gateway":     want.Gateway,
			}); err != nil {
				return fmt.Errorf("add default gateway %s: %w", want.Gateway, err)
			}
		}
	}
	if len(want.DNS) > 0 {
		var _, dnsErr = c.Command(ctx, "ip/dns", "set", map[string]string{
			"servers": strings.Join(want.DNS, ","),
		})
		if dnsErr != nil {
			return fmt.Errorf("set DNS servers: %w", dnsErr)
		}
	}
	return nil
}

func (c *Client) ensureBridgeVLAN(ctx context.Context, bridge string, vlanID int, tagged []string) error {
	tagged = insertOnce(tagged, bridge)
	var rows, err = c.List(ctx, "interface/bridge/vlan")
	if err != nil {
		return err
	}
	var idStr = strconv.Itoa(vlanID)
	var r Row
	for _, r = range rows {
		if !vlanIDsContain(r.Get("vlan-ids"), vlanID) {
			continue
		}
		var have = SplitList(r.Get("tagged"))
		var need []string
		var m string
		for _, m = range tagged {
			if !contains(have, m) {
				need = append(need, m)
			}
		}
		if len(need) == 0 {
			return nil
		}
		var merged = append(append([]string{}, have...), need...)
		if _, err = c.Set(ctx, "interface/bridge/vlan", r.Get(".id"), map[string]string{
			"tagged": JoinList(merged),
		}); err != nil {
			return fmt.Errorf("update bridge vlan %d tagged: %w", vlanID, err)
		}
		return nil
	}
	if _, err = c.Add(ctx, "interface/bridge/vlan", map[string]string{
		"bridge":   bridge,
		"vlan-ids": idStr,
		"tagged":   JoinList(tagged),
	}); err != nil {
		return fmt.Errorf("add bridge vlan %d: %w", vlanID, err)
	}
	return nil
}

func (c *Client) ensureVLANInterface(ctx context.Context, bridge string, vlanID int) (string, error) {
	var name, err = c.vlanIfaceName(ctx, vlanID)
	if err != nil {
		return "", err
	}
	if name != "" {
		return name, nil
	}
	name = "vlan" + strconv.Itoa(vlanID)
	if _, err = c.Add(ctx, "interface/vlan", map[string]string{
		"name":      name,
		"vlan-id":   strconv.Itoa(vlanID),
		"interface": bridge,
	}); err != nil {
		return "", fmt.Errorf("add interface/vlan %s: %w", name, err)
	}
	return name, nil
}

func (c *Client) vlanIfaceName(ctx context.Context, vlanID int) (string, error) {
	var rows, err = c.List(ctx, "interface/vlan")
	if err != nil {
		return "", err
	}
	var idStr = strconv.Itoa(vlanID)
	var r Row
	for _, r = range rows {
		if r.Get("vlan-id") == idStr && !r.Bool("disabled") {
			return r.Get("name"), nil
		}
	}
	return "", nil
}

// uplinksFromMgmt copies tagged members of the current management VLAN entry,
// excluding the bridge CPU name — Prepare re-adds bridge explicitly.
func uplinksFromMgmt(st DeviceState) []string {
	var out []string
	var v BridgeVLAN
	for _, v = range st.VLANs {
		if v.ID != st.MgmtVLAN {
			continue
		}
		var m string
		for _, m = range v.TaggedMembers {
			if m == st.BridgeName || m == "" {
				continue
			}
			out = insertOnce(out, m)
		}
		for _, m = range v.UntaggedMembers {
			if m == st.BridgeName || m == "" {
				continue
			}
			out = insertOnce(out, m)
		}
	}
	// Fallback: any port with link that is not the bridge.
	if len(out) == 0 {
		var name string
		var up bool
		for name, up = range st.LinkUp {
			if up && name != st.BridgeName {
				out = insertOnce(out, name)
			}
		}
	}
	return out
}

func vlanIDsContain(csv string, vlanID int) bool {
	var idStr = strconv.Itoa(vlanID)
	var part string
	for _, part = range strings.Split(csv, ",") {
		if strings.TrimSpace(part) == idStr {
			return true
		}
	}
	return false
}

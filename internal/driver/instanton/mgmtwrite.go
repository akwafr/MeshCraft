package instanton

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Management addressing READ/WRITE on Instant On (adopted device / wcd).
//
// Static `owner` was unknown until lab probe; DHCP is owner [ownerDHCP] (= 2).
// Factory `/connectivity` remains a separate channel (factory-fresh only).

var (
	_ driver.MgmtAddrReader = (*Admin)(nil)
	_ driver.MgmtAddrWriter = (*Admin)(nil)
)

// ownerStatic is the `owner` value of a manually configured IPv4 interface.
//
// OBSERVED on lab sw-labo-1930 (JL685A, InstantOn_1930_3.4.0.0): temporary
// static ifEntry on VLAN100 with 192.168.100.1/24 accepted with owner=1;
// owner 0/4 refused « Invalid owner value », owner 5 « BootP is not supported ».
// Same /16 as the management DHCP interface is refused (« Duplicate IP subnet »).
const ownerStatic = 1

// ReadMgmtAddressing reports how the management interface obtained its address.
func (a *Admin) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	var out driver.MgmtAddrState
	var body, err = a.RawSection(ctx, "IPv4InterfaceList")
	if err != nil {
		return out, err
	}
	var mgmtName string
	var glob string
	if glob, err = a.RawSection(ctx, "IPv4GlobalSetting"); err == nil {
		mgmtName = xmlField(glob, "managementInterfaceName")
	}
	var block string
	var found bool
	for _, block = range reIfEntry.FindAllString(body, -1) {
		var name = xmlField(block, "interfaceName")
		if mgmtName != "" && name != mgmtName {
			continue
		}
		if mgmtName == "" && !strings.HasPrefix(strings.ToUpper(name), "VLAN") {
			continue
		}
		found = true
		out.Interface = name
		out.Address = formatIOAddr(xmlField(block, "IPAddr"), xmlField(block, "subnetMask"))
		var owner = atoiOr(xmlField(block, "owner"), -1)
		switch {
		case owner == ownerDHCP:
			out.Mode = driver.MgmtAddrModeDHCP
		case owner == ownerStatic:
			out.Mode = driver.MgmtAddrModeStatic
		case owner == 3:
			out.Notes = append(out.Notes,
				"management interface carries owner 3 (factory link-local style); "+
					"mode left empty rather than guessing static")
		default:
			out.Notes = append(out.Notes, fmt.Sprintf(
				"IPv4InterfaceList owner=%d is not a known acquisition mode on this "+
					"firmware (DHCP=%d, static=%d). Mode left empty.",
				owner, ownerDHCP, ownerStatic))
		}
		break
	}
	if !found {
		out.Notes = append(out.Notes, "no management IPv4 interface found in IPv4InterfaceList")
	}

	// Gateway: default route in IPv4RouteList (observed on lab).
	var routes string
	var rErr error
	if routes, rErr = a.RawSection(ctx, "IPv4RouteList"); rErr != nil {
		out.Notes = append(out.Notes, "default route not readable: "+rErr.Error())
	} else {
		out.Gateway = defaultRouteOf(routes)
	}

	a.step(true, "management addressing read",
		fmt.Sprintf("mode « %s », address %s on %s",
			emptyDash(out.Mode), emptyDash(out.Address), emptyDash(out.Interface)))
	return out, nil
}

// ApplyMgmtAddressing applies DHCP or static on the CURRENT management interface.
//
// Does not change management VLAN ([MgmtMover] does). Never calls SaveConfig —
// power loss remains the safety net, as for migrate prepare.
func (a *Admin) ApplyMgmtAddressing(ctx context.Context, want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {
	var out driver.MgmtAddrResult
	var st, err = a.ReadState(ctx)
	if err != nil {
		return out, err
	}
	if st.MgmtVLAN <= 0 {
		return out, fmt.Errorf("no management VLAN declared: cannot apply addressing")
	}
	var vlanID = st.MgmtVLAN

	switch want.Mode {
	case driver.MgmtAddrModeDHCP:
		// Dual-path on same VLAN is not available: remove then add DHCP.
		// Session may drop; Instant On has no OutOfBand — caller must know.
		_ = a.removeIPv4Interface(ctx, vlanIfName(vlanID), ownerDHCP)
		_ = a.removeIPv4Interface(ctx, vlanIfName(vlanID), ownerStatic)
		if err = a.AddManagementInterface(ctx, vlanID); err != nil {
			return out, err
		}
	case driver.MgmtAddrModeStatic:
		var host, mask = splitCIDR(want.Address)
		if host == "" {
			return out, fmt.Errorf("static addressing requested without an address")
		}
		if mask == "" {
			mask = "255.255.255.0"
			out.Notes = append(out.Notes,
				"no prefix in address: used 255.255.255.0 — set an explicit CIDR to avoid guessing")
		}
		// Remove DHCP first, then place static (same cutover risk as SwOS jump).
		_ = a.removeIPv4Interface(ctx, vlanIfName(vlanID), ownerDHCP)
		if err = a.addStaticInterface(ctx, vlanID, host, mask); err != nil {
			return out, err
		}
		if want.Gateway != "" {
			var gErr = a.ensureDefaultRoute(ctx, want.Gateway, vlanIfName(vlanID), vlanID)
			if gErr != nil {
				out.Notes = append(out.Notes, gErr.Error())
			}
		}
		if len(want.DNS) > 0 {
			out.Notes = append(out.Notes,
				"DNS not written: NameServerInterfaceList layout not proven on this firmware")
		}
	default:
		return out, fmt.Errorf("unsupported addressing mode %q", want.Mode)
	}

	out.Observed, err = a.ReadMgmtAddressing(ctx)
	if err != nil {
		out.Notes = append(out.Notes, "re-read after apply failed: "+err.Error())
		err = nil
	}
	return out, nil
}

// ProbeStaticOwner creates a temporary STATIC IPv4 interface on vlanID, reads the
// owner the device assigned/accepted, then deletes it. Never SaveConfig.
//
// vlanID must NOT be the current management VLAN — that would cut the session.
// Used once on lab hardware to learn ownerStatic.
func (a *Admin) ProbeStaticOwner(ctx context.Context, vlanID int, addr, mask string) (int, string, error) {
	var st, err = a.ReadState(ctx)
	if err != nil {
		return 0, "", err
	}
	if vlanID == st.MgmtVLAN {
		return 0, "", fmt.Errorf("refusing to probe static owner on current management VLAN %d", vlanID)
	}
	if addr == "" || mask == "" {
		return 0, "", fmt.Errorf("probe requires address and mask")
	}

	var attempts = []struct {
		owner int
		body  string
	}{
		{1, staticIfXML(vlanID, addr, mask, 1)},
		{0, staticIfXML(vlanID, addr, mask, 0)},
		{4, staticIfXML(vlanID, addr, mask, 4)},
		{5, staticIfXML(vlanID, addr, mask, 5)},
		{-1, staticIfXMLNoOwner(vlanID, addr, mask)},
	}
	var lastErr error
	var attempt struct {
		owner int
		body  string
	}
	for _, attempt = range attempts {
		lastErr = a.writeSection(ctx, "IPv4InterfaceList", "set", attempt.body,
			fmt.Sprintf("static probe on VLAN %d (owner guess %d)", vlanID, attempt.owner))
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		return 0, "", fmt.Errorf("all static field-set attempts refused: %w", lastErr)
	}

	var raw string
	if raw, err = a.RawSection(ctx, "IPv4InterfaceList"); err != nil {
		_ = a.removeIPv4Interface(ctx, vlanIfName(vlanID), 1)
		_ = a.removeIPv4Interface(ctx, vlanIfName(vlanID), 0)
		_ = a.removeIPv4Interface(ctx, vlanIfName(vlanID), 4)
		_ = a.removeIPv4Interface(ctx, vlanIfName(vlanID), 5)
		return 0, "", err
	}
	var owner = -1
	var block string
	for _, block = range reIfEntry.FindAllString(raw, -1) {
		if xmlField(block, "interfaceName") != vlanIfName(vlanID) {
			continue
		}
		owner = atoiOr(xmlField(block, "owner"), -1)
		break
	}
	if owner >= 0 {
		var addr = ""
		for _, block = range reIfEntry.FindAllString(raw, -1) {
			if xmlField(block, "interfaceName") == vlanIfName(vlanID) {
				addr = xmlField(block, "IPAddr")
				break
			}
		}
		if err = a.removeIPv4InterfaceAddr(ctx, vlanIfName(vlanID), owner, addr); err != nil {
			return owner, raw, fmt.Errorf("static owner=%d observed but cleanup failed: %w "+
				"(interface may still be present until removed or power-cycled)", owner, err)
		}
	}
	if owner < 0 {
		return 0, raw, fmt.Errorf("static interface written but owner not found on re-read")
	}
	a.step(true, "static owner observed",
		fmt.Sprintf("owner=%d on %s (temporary; deleted; not saved to flash)",
			owner, vlanIfName(vlanID)))
	return owner, raw, nil
}

func (a *Admin) addStaticInterface(ctx context.Context, vlanID int, addr, mask string) error {
	return a.writeSection(ctx, "IPv4InterfaceList", "set",
		staticIfXML(vlanID, addr, mask, ownerStatic),
		fmt.Sprintf("interface %s set static %s/%s", vlanIfName(vlanID), addr, mask))
}

func (a *Admin) removeIPv4Interface(ctx context.Context, name string, owner int) error {
	return a.removeIPv4InterfaceAddr(ctx, name, owner, "")
}

func (a *Admin) removeIPv4InterfaceAddr(ctx context.Context, name string, owner int, addr string) error {
	var inner = "<ifEntry><interfaceName>" + xmlEscape(name) + "</interfaceName>" +
		"<owner>" + strconv.Itoa(owner) + "</owner>"
	if addr != "" {
		inner += "<IPAddr>" + xmlEscape(addr) + "</IPAddr>"
	}
	inner += "</ifEntry>"
	return a.writeSection(ctx, "IPv4InterfaceList", "delete", inner,
		fmt.Sprintf("interface %s removed (owner %d)", name, owner))
}

func (a *Admin) ensureDefaultRoute(ctx context.Context, gateway, ifName string, vlanID int) error {
	return a.writeSection(ctx, "IPv4RouteList", "set",
		"<Entry>"+
			"<destinationIPv4Address>0.0.0.0</destinationIPv4Address>"+
			"<destinationPrefixLength>0</destinationPrefixLength>"+
			"<nextHopIPv4Address>"+xmlEscape(gateway)+"</nextHopIPv4Address>"+
			"<interfaceName>"+xmlEscape(ifName)+"</interfaceName>"+
			"<interfaceType>3</interfaceType>"+
			"<interfaceID>"+strconv.Itoa(vlanID)+"</interfaceID>"+
			"</Entry>",
		"default route via "+gateway)
}

func staticIfXML(vlanID int, addr, mask string, owner int) string {
	return "<ifEntry>" +
		"<interfaceName>" + vlanIfName(vlanID) + "</interfaceName>" +
		"<IPAddr>" + xmlEscape(addr) + "</IPAddr>" +
		"<subnetMask>" + xmlEscape(mask) + "</subnetMask>" +
		"<owner>" + strconv.Itoa(owner) + "</owner>" +
		"<directedBroadcast>2</directedBroadcast>" +
		"<precedence>2</precedence>" +
		"<redirect>2</redirect>" +
		"<adminStatus>1</adminStatus>" +
		"</ifEntry>"
}

func staticIfXMLNoOwner(vlanID int, addr, mask string) string {
	return "<ifEntry>" +
		"<interfaceName>" + vlanIfName(vlanID) + "</interfaceName>" +
		"<IPAddr>" + xmlEscape(addr) + "</IPAddr>" +
		"<subnetMask>" + xmlEscape(mask) + "</subnetMask>" +
		"<directedBroadcast>2</directedBroadcast>" +
		"<precedence>2</precedence>" +
		"<redirect>2</redirect>" +
		"<adminStatus>1</adminStatus>" +
		"</ifEntry>"
}

func formatIOAddr(ip, mask string) string {
	if ip == "" {
		return ""
	}
	if mask == "" {
		return ip
	}
	var bits = maskToPrefix(mask)
	if bits < 0 {
		return ip
	}
	return ip + "/" + strconv.Itoa(bits)
}

func maskToPrefix(mask string) int {
	var parts = strings.Split(mask, ".")
	if len(parts) != 4 {
		return -1
	}
	var out uint32
	var i int
	for i = 0; i < 4; i++ {
		var n, err = strconv.Atoi(parts[i])
		if err != nil || n < 0 || n > 255 {
			return -1
		}
		out = out<<8 | uint32(n)
	}
	var bits int
	var b int
	for b = 31; b >= 0; b-- {
		if out&(1<<uint(b)) == 0 {
			break
		}
		bits++
	}
	// Require contiguous mask: remaining bits must be zero.
	for ; b >= 0; b-- {
		if out&(1<<uint(b)) != 0 {
			return -1
		}
	}
	return bits
}

func splitCIDR(addr string) (host, mask string) {
	var s = strings.TrimSpace(addr)
	var slash = strings.IndexByte(s, '/')
	if slash < 0 {
		return s, ""
	}
	host = strings.TrimSpace(s[:slash])
	var p, err = strconv.Atoi(strings.TrimSpace(s[slash+1:]))
	if err != nil || p < 0 || p > 32 {
		return host, ""
	}
	return host, prefixToMask(p)
}

func prefixToMask(p int) string {
	var m uint32
	if p > 0 {
		m = ^uint32(0) << uint(32-p)
	}
	return fmt.Sprintf("%d.%d.%d.%d", m>>24&0xff, m>>16&0xff, m>>8&0xff, m&0xff)
}

func defaultRouteOf(routesXML string) string {
	// Cheap scan: first Entry with destination 0.0.0.0 / prefix 0.
	var parts = strings.Split(routesXML, "<Entry>")
	var part string
	for _, part = range parts[1:] {
		if xmlField(part, "destinationIPv4Address") != "0.0.0.0" {
			continue
		}
		if xmlField(part, "destinationPrefixLength") != "0" {
			continue
		}
		return xmlField(part, "nextHopIPv4Address")
	}
	return ""
}

func atoiOr(s string, def int) int {
	var n, err = strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

func emptyDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

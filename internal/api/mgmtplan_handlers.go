package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"

	"meshcraft/internal/auth"
	"meshcraft/internal/model"
	"meshcraft/internal/store"
)

// Site management addressing plan
// (cf. docs/en/01-Architecture/Management addressing.md).
//
// Read open to site roles; write reserved for `site_admin` — a decision that
// affects reachability of the entire site fleet ([[RBAC]]).

func (s *Server) handleGetMgmtPlan(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "viewer")
	if !ok {
		return
	}
	var plan, err = s.Store.GetMgmtPlan(r.Context(), siteID)
	if err != nil {
		fail(w, err)
		return
	}
	var usage, usageErr = s.Store.MgmtPoolUsage(r.Context(), siteID)
	if usageErr != nil {
		fail(w, usageErr)
		return
	}
	usage.Size = store.PoolSize(plan.PoolStart, plan.PoolEnd)
	usage.Free = usage.Size - usage.Assigned - usage.Reserved
	if usage.Free < 0 {
		usage.Free = 0
	}
	respond(w, http.StatusOK, map[string]any{"plan": plan, "pool": usage})
}

func (s *Server) handleUpdateMgmtPlan(w http.ResponseWriter, r *http.Request) {
	var siteID, ok = s.requireSite(w, r, "site_admin")
	if !ok {
		return
	}
	var in model.MgmtPlan
	if !decode(w, r, &in) {
		return
	}
	in.SiteID = siteID
	var problem = validateMgmtPlan(&in)
	if problem != "" {
		respondErr(w, http.StatusBadRequest, problem)
		return
	}
	var me = auth.UserFrom(r.Context())
	var plan, err = s.Store.SaveMgmtPlan(r.Context(), in, me.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.emitSite(r, siteID, "mgmt.plan_changed", model.SevNotice,
		"Site management plan updated: "+planLabel(plan), nil)
	respond(w, http.StatusOK, plan)
}

func planLabel(p model.MgmtPlan) string {
	var tag = "tagged"
	if p.Untagged {
		tag = "native"
	}
	var src = "external DHCP"
	if p.Source == model.MgmtSourceMeshcraft {
		src = "addresses allocated by meshcraft (" + p.PoolStart + " -> " + p.PoolEnd + ")"
	}
	return "VLAN " + itoa(p.VlanID) + " " + tag + ", " + src
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var neg = n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// validateMgmtPlan rejects at INPUT what could not work, rather than letting
// the error surface on first deployment — when the device is already half
// reconfigured.
func validateMgmtPlan(p *model.MgmtPlan) string {
	p.SubnetV4 = strings.TrimSpace(p.SubnetV4)
	p.GatewayV4 = strings.TrimSpace(p.GatewayV4)
	p.SubnetV6 = strings.TrimSpace(p.SubnetV6)
	p.GatewayV6 = strings.TrimSpace(p.GatewayV6)
	p.PoolStart = strings.TrimSpace(p.PoolStart)
	p.PoolEnd = strings.TrimSpace(p.PoolEnd)

	if p.VlanID < 1 || p.VlanID > 4094 {
		return "management VLAN out of range (1-4094)"
	}
	if p.Source != model.MgmtSourceDHCPExternal && p.Source != model.MgmtSourceMeshcraft {
		return "unknown address origin: expected \"dhcp_external\" or \"meshcraft\""
	}

	var v4, err = parsePrefix(p.SubnetV4)
	if p.SubnetV4 != "" && err != nil {
		return "invalid IPv4 subnet (expected 10.10.0.0/24)"
	}
	if p.SubnetV6 != "" {
		var _, err6 = parsePrefix(p.SubnetV6)
		if err6 != nil {
			return "invalid IPv6 subnet (expected 2001:db8:1::/64)"
		}
	}
	if p.GatewayV4 != "" {
		var gw, gwErr = netip.ParseAddr(p.GatewayV4)
		if gwErr != nil || !gw.Is4() {
			return "invalid IPv4 gateway"
		}
		// A gateway outside the subnet is almost always a typo: reject it
		// rather than pushing it across the whole fleet.
		if p.SubnetV4 != "" && !v4.Contains(gw) {
			return "gateway " + p.GatewayV4 + " is not in subnet " + p.SubnetV4
		}
	}

	if p.Source == model.MgmtSourceMeshcraft {
		if p.SubnetV4 == "" {
			return "meshcraft allocation requires an IPv4 subnet"
		}
		if p.PoolStart == "" || p.PoolEnd == "" {
			return "meshcraft allocation requires a range (start and end)"
		}
		var a, errA = netip.ParseAddr(p.PoolStart)
		var b, errB = netip.ParseAddr(p.PoolEnd)
		if errA != nil || errB != nil {
			return "invalid allocation range"
		}
		if b.Less(a) {
			return "range end precedes its start"
		}
		if !v4.Contains(a) || !v4.Contains(b) {
			return "allocation range must be included in " + p.SubnetV4
		}
		// A gateway inside the range would eventually be allocated to a device.
		if p.GatewayV4 != "" {
			var gw, _ = netip.ParseAddr(p.GatewayV4)
			if !gw.Less(a) && !b.Less(gw) {
				return "gateway " + p.GatewayV4 + " is in the allocation range: " +
					"it would end up assigned to a device"
			}
		}
	}

	var host string
	var cleaned = []string{}
	for _, host = range p.DNS {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if net.ParseIP(host) == nil {
			return "invalid DNS server: " + host
		}
		cleaned = append(cleaned, host)
	}
	p.DNS = cleaned
	cleaned = []string{}
	for _, host = range p.NTP {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		if net.ParseIP(host) == nil {
			return "invalid NTP server: " + host
		}
		cleaned = append(cleaned, host)
	}
	p.NTP = cleaned
	return ""
}

func parsePrefix(s string) (netip.Prefix, error) {
	return netip.ParsePrefix(s)
}

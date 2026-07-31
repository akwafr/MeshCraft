package api

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"meshcraft/internal/model"
)

// pushWatchForDevice refreshes ICMP/SNMP watch on the proxies that cover this
// device after mgmt_ip changes. Without it, the proxy keeps pinging the OLD
// address until an unrelated PushWatchTo (tunnel reconnect, collector edit…).
func (s *Server) pushWatchForDevice(d model.Device) {
	var ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var seen = map[string]bool{}
	var pid = d.ProxyID
	if pid == "" {
		pid = s.proxyOf(ctx, d)
	}
	if pid != "" {
		seen[pid] = true
		go s.PushWatchTo(context.Background(), pid)
	}
	// Discovery proxy may differ from the declared collector: refresh both.
	var disc = s.proxyOf(ctx, d)
	if disc != "" && !seen[disc] {
		go s.PushWatchTo(context.Background(), disc)
	}
}

// rememberRenumberCandidate asks the proxy to ping a possible new management IP
// alongside the current mgmt_ip. Promotion waits for device.up + serial match
// (cf. confirmRenumberCandidate).
func (s *Server) rememberRenumberCandidate(d model.Device, ips ...string) {
	if s.Renumber == nil {
		return
	}
	var filtered []string
	var ip string
	for _, ip = range ips {
		ip = mgmtHostFromAddr(ip)
		if ip == "" || ip == d.MgmtIP {
			continue
		}
		filtered = append(filtered, ip)
	}
	if len(filtered) == 0 {
		return
	}
	s.Renumber.Remember(d.ID, d.SiteID, d.Name, filtered...)
	s.pushWatchForDevice(d)
}

// confirmRenumberCandidate runs after the proxy reports device.up on a pending
// renumber IP. SERIAL must match before mgmt_ip is updated.
func (s *Server) confirmRenumberCandidate(deviceID, ip string) {
	var ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if s.Renumber == nil || !s.Renumber.Has(deviceID, ip) {
		return
	}
	var d, err = s.Store.GetDevice(ctx, deviceID)
	if err != nil {
		return
	}
	if ip == d.MgmtIP {
		s.Renumber.ForgetIP(deviceID, ip)
		return
	}
	var serial, driverName = s.deviceIdentity(ctx, d)
	if serial == "" {
		slog.Warn("renumber: candidate answered but device has no known serial",
			"device", d.Name, "ip", ip)
		return
	}
	var proxyID = d.ProxyID
	if proxyID == "" {
		proxyID = s.proxyOf(ctx, d)
	}
	var dialer, dialErr = s.dialerFor(proxyID)
	if dialErr != nil {
		slog.Warn("renumber: no dialer for serial check",
			"err", dialErr, "device", d.Name, "ip", ip)
		return
	}
	var cand, refusal = s.relocateCandidate(ctx, d)
	var probe = s.serialOf(driverName, dialer, cand, refusal)
	var got string
	got, err = probe(ctx, ip)
	if err != nil {
		slog.Warn("renumber: serial probe failed",
			"err", err, "device", d.Name, "ip", ip)
		return
	}
	if got == "" || !strings.EqualFold(got, serial) {
		slog.Info("renumber: candidate IP answered but serial mismatch — ignored",
			"device", d.Name, "ip", ip, "want", serial, "got", got)
		return
	}
	if err = s.Store.SetDeviceMgmtIP(ctx, deviceID, ip); err != nil {
		slog.Error("renumber: mgmt_ip not updated",
			"err", err, "device", d.Name, "ip", ip)
		return
	}
	s.Renumber.Forget(deviceID)
	s.pushWatchForDevice(d)
	var siteID = d.SiteID
	_ = s.Store.InsertEvent(ctx, model.Event{
		SiteID: &siteID, DeviceID: &deviceID, Type: "device.relocated",
		Severity: model.SevNotice,
		Message: "Device \"" + d.Name + "\" confirmed at " + ip +
			" (renumber candidate answered; serial matched)",
		Actor: "meshcraft",
	})
	s.NotifySiteChanged(siteID, ChangeDevice)
	slog.Info("renumber: mgmt_ip promoted after ICMP+serial",
		"device", d.Name, "ip", ip)
}

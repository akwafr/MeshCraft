package unifi

// Translation of UniFi's vocabulary into meshcraft's neutral shapes.
//
// It lives beside the client rather than in the handler because it is part of
// knowing how to read a UniFi controller, not part of serving HTTP: the next
// Wi-Fi vendor gets its own package with its own file of this name, and both
// produce the SAME model types. A handler that mapped fields itself would have
// to grow a branch per vendor.
//
// The names produced here are the 802.11 standard's, not UniFi's — that is the
// rule ADR-0016 sets, and the reason `wlanStandard`/`frequencyGHz` become
// `Standard`/`BandGHz` while `configurationId` is simply not carried: it does
// not translate, so it must not pollute the neutral columns.

import (
	"strings"

	"meshcraft/internal/model"
)

// ToExtDevices maps the controller's boxes onto read-only mirror rows.
func ToExtDevices(boxes []DeviceDetail) []model.ExtDevice {
	var out = make([]model.ExtDevice, 0, len(boxes))
	var b DeviceDetail
	for _, b = range boxes {
		var d = model.ExtDevice{
			ExternalID: b.ID,
			Class:      ClassOf(b.Features),
			Name:       b.Name,
			Model:      b.Model,
			Firmware:   b.FirmwareVersion,
			// Lowercased so "ONLINE" and "online" are one value. Deliberately
			// NOT mapped onto Device.Status (ok|warn|off|unknown): meshcraft
			// does not poll this box, so turning the vendor's word into its own
			// judgement would be inventing a verdict it cannot support.
			State:  strings.ToLower(b.State),
			MgmtIP: b.IPAddress,
			// Lowercased because this is the join key to LLDP and the FDB
			// (ADR-0016 §3), and a MAC that differs only in case would simply
			// fail to correlate — silently, which is the worst way to fail.
			MAC:    strings.ToLower(b.MACAddress),
			Radios: []model.ExtRadio{},
			Ports:  []model.ExtPort{},
		}
		var ra DeviceRadio
		for _, ra = range b.Decoded.Radios {
			d.Radios = append(d.Radios, model.ExtRadio{
				Standard: ra.WLANStandard,
				BandGHz:  ra.FrequencyGHz,
				Channel:  ra.Channel,
				WidthMHz: ra.ChannelWidthMHz,
			})
		}
		var p DevicePort
		for _, p = range b.Decoded.Ports {
			d.Ports = append(d.Ports, model.ExtPort{
				Index:        p.Idx,
				State:        strings.ToLower(p.State),
				Connector:    p.Connector,
				SpeedMbps:    p.SpeedMbps,
				MaxSpeedMbps: p.MaxSpeedMbps,
			})
		}
		out = append(out, d)
	}
	return out
}

// ToExtSSIDs maps service sets.
//
// No passphrase is carried, and model.ExtSSID has no field that could hold
// one: reading somebody's controller does not mean copying the PSKs of a
// production network into a second database.
func ToExtSSIDs(in []SSID) []model.ExtSSID {
	var out = make([]model.ExtSSID, 0, len(in))
	var w SSID
	for _, w = range in {
		var bands = w.Bands
		if bands == nil {
			bands = []string{}
		}
		out = append(out, model.ExtSSID{
			ExternalID: w.ID,
			Name:       w.Name,
			Enabled:    w.Enabled,
			Security:   strings.ToLower(w.Security),
			WPAMode:    strings.ToLower(w.WPAMode),
			Bands:      bands,
		})
	}
	return out
}

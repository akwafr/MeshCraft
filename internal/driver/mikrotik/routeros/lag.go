package routeros

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"meshcraft/internal/driver"
)

// Link aggregation on RouterOS: `/interface/bonding`, plus the bridge tables
// the bond appears in AS an interface.
//
// # What the reference device establishes (CRS305, captured 2026-08-13)
//
//	/interface/bonding → {"name":"bonding1","mode":"802.3ad",
//	                      "lacp-mode":"active","slaves":"sfp-sfpplus4",
//	                      "running":"true", ...}
//	/interface/bridge/port  → a row {"interface":"bonding1","pvid":"1"}
//	/interface/bridge/vlan  → "bonding1" listed in eleven VLANs' tagged lists
//
// Three facts drive the code:
//
//   - the VLAN half of an aggregate is ALREADY the port machinery: the bond
//     has a bridge-port row (PVID) and appears in vlan rows (tagged), so the
//     read folds it exactly as foldVLANs folds a port, and the writer edits
//     the same rows the port applier edits;
//   - the slaves list is a comma-joined field on the bonding row, member
//     ports keep their own (deactivated) bridge rows — enslaving does not
//     delete them, and neither does this connector: the device's own bonding
//     left sfp-sfpplus4's row in place, deactivated, and writes must not be
//     more invasive than the platform's own hand;
//   - `mode` is the platform's rich vocabulary ("802.3ad", "balance-xor",
//     "active-backup"…). The standard model keeps two words: 802.3ad is
//     "lacp", everything else is "static" — the finer mode rides Raw/Notes,
//     and a WRITE of "static" emits "balance-xor" (the only non-negotiating
//     mode whose hashing matches what a switch peer expects).

// Bonding is one `/interface/bonding` row, as read.
type Bonding struct {
	RowID   string   `json:"-"`
	Name    string   `json:"name"`
	ModeRaw string   `json:"mode"`
	Slaves  []string `json:"slaves"`
	Running bool     `json:"running"`
}

// foldBondings decodes the menu rows. PURE — tests replay the capture.
func foldBondings(rows []Row) []Bonding {
	var out []Bonding
	var r Row
	for _, r = range rows {
		var name = r.Get("name")
		if name == "" {
			continue
		}
		var b = Bonding{
			RowID: r.ID(), Name: name, ModeRaw: r.Get("mode"),
			Running: r.Bool("running"),
		}
		var s string
		for _, s = range strings.Split(r.Get("slaves"), ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				b.Slaves = append(b.Slaves, s)
			}
		}
		sort.Strings(b.Slaves)
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// lagStdName translates the device spelling to the standard one: bondingN →
// LAGN. A bond named outside that shape keeps its own name — the read must
// not invent an identity — and the intent-side validation refuses to WRITE
// such a name, which is the right asymmetry.
func lagStdName(deviceName string) string {
	var rest, has = strings.CutPrefix(deviceName, "bonding")
	if !has {
		return deviceName
	}
	var n, err = strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return deviceName
	}
	return driver.StandardLAGName(n)
}

// lagDeviceName renders the device spelling for a standard intent name.
func lagDeviceName(std string) (string, bool) {
	var n, ok = driver.LAGNumber(std)
	if !ok {
		return "", false
	}
	return "bonding" + strconv.Itoa(n), true
}

// standardLAGs builds the contract view from the rich read: bonding rows for
// identity and members, the bond's own bridge-port entry for the VLAN half.
// PURE.
func standardLAGs(s DeviceState) []driver.LAGState {
	var out []driver.LAGState
	var i int
	for i = range s.Bondings {
		var b = s.Bondings[i]
		var mode = driver.LAGModeStatic
		if b.ModeRaw == "802.3ad" {
			mode = driver.LAGModeLACP
		}
		var lag = driver.LAGState{
			Name: lagStdName(b.Name), Raw: b.Name, Mode: mode,
			Members: append([]string{}, b.Slaves...), Running: b.Running,
		}
		var port, inBridge = s.Ports[b.Name]
		if inBridge {
			lag.VLAN = PortState{Mode: port.Mode, PVID: port.PVID, Tagged: port.Tagged}
		}
		out = append(out, lag)
	}
	driver.SortLAGs(out)
	return out
}

// ApplyLAGs applies the DESIRED aggregate list — the [driver.LAGWriter]
// contract: complete list, replace semantics.
func (c *Client) ApplyLAGs(ctx context.Context, desired []driver.LAGState) (ApplyResult, error) {
	var out ApplyResult
	var err = driver.ValidateLAGs(desired)
	if err != nil {
		return out, err
	}
	// LACP fallback is REFUSED, not trimmed: this connector does not declare
	// GestureLAGFallback, and RouterOS bonding has no fallback concept —
	// `/interface/bonding` negotiates or it does not.
	var fb, wantsFB = driver.LAGFallbackWanted(desired)
	if wantsFB {
		return out, driver.RefuseLAGFallback("mikrotik-routeros", fb.Name)
	}
	// Fresh rich read, never cached: row `.id`s do not survive a reboot
	// (the port applier's rule, same reason).
	var s DeviceState
	if s, err = c.ReadDeviceState(ctx); err != nil {
		return out, fmt.Errorf("state unreadable before writing aggregates: %v", err)
	}
	if s.BridgeName == "" {
		return out, fmt.Errorf("no bridge on this device: an aggregate would " +
			"switch nothing — this RouterOS routes without a switching table")
	}

	var have = map[string]Bonding{}
	var i int
	for i = range s.Bondings {
		have[lagStdName(s.Bondings[i].Name)] = s.Bondings[i]
	}

	var wantNames = map[string]bool{}
	for i = range desired {
		var want = desired[i]
		wantNames[want.Name] = true
		var devName, ok = lagDeviceName(want.Name)
		if !ok {
			return out, fmt.Errorf("aggregate %q: not a writable standard name", want.Name)
		}
		var mode = "balance-xor"
		if want.Mode == driver.LAGModeLACP {
			mode = "802.3ad"
		}
		var slaves = strings.Join(want.Members, ",")

		var b, exists = have[want.Name]
		var changing = !exists || b.ModeRaw != mode || strings.Join(b.Slaves, ",") != slaves
		if changing {
			// MEMBERS LEAVE THE BRIDGE FIRST — a platform rule, met live on the
			// reference device: `/interface/bonding/set` answers "400: failure:
			// ether1 already in bridge" for a member whose own bridge-port row
			// is still active. The bond's row is what carries the traffic; the
			// member's row is removed, and a member later FREED gets a fresh row
			// back (below) so the jack does not fall off the switching table.
			var m string
			for _, m = range want.Members {
				var row, inBridge = s.Ports[m]
				if inBridge && row.RowID != "" {
					if _, err = c.Remove(ctx, "interface/bridge/port", row.RowID); err != nil {
						return out, fmt.Errorf("aggregate %s: member %s could not "+
							"leave the bridge: %v", want.Name, m, err)
					}
					delete(s.Ports, m)
					c.step(true, "member left the bridge", m+" (enslaved to "+devName+")")
				}
			}
		}
		if !exists {
			if _, err = c.Add(ctx, "interface/bonding", map[string]string{
				"name": devName, "mode": mode, "slaves": slaves,
			}); err != nil {
				return out, fmt.Errorf("aggregate %s not created: %v", want.Name, err)
			}
			c.step(true, "aggregate created",
				devName+" mode "+mode+" slaves "+slaves)
			out.PortsWritten = append(out.PortsWritten, want.Name)
		} else if changing {
			if _, err = c.Set(ctx, "interface/bonding", b.RowID, map[string]string{
				"mode": mode, "slaves": slaves,
			}); err != nil {
				return out, fmt.Errorf("aggregate %s not updated: %v", want.Name, err)
			}
			c.step(true, "aggregate updated",
				devName+" mode "+mode+" slaves "+slaves)
			out.PortsWritten = append(out.PortsWritten, want.Name)
			// Members this aggregate no longer claims return to the bridge as
			// plain ports (PVID 1, the platform default) — without this they
			// would belong to nothing and switch nothing, invisibly.
			var m string
			for _, m = range b.Slaves {
				if contains(want.Members, m) {
					continue
				}
				if _, err = c.Add(ctx, "interface/bridge/port", map[string]string{
					"bridge": s.BridgeName, "interface": m, "pvid": "1",
				}); err != nil {
					return out, fmt.Errorf("freed member %s not returned to the "+
						"bridge: %v", m, err)
				}
				out.Notes = append(out.Notes, "freed member "+m+" returned to the "+
					"bridge untagged in VLAN 1 — reassign it from the faceplate")
			}
		}

		// The VLAN half, only when the intent MANAGES it (mode set) — the same
		// "empty means leave alone" rule as ports.
		if want.VLAN.Mode != "" {
			var wrote bool
			if wrote, err = c.applyLAGVLAN(ctx, &s, devName, want.VLAN, &out); err != nil {
				return out, err
			}
			if wrote && !contains(out.PortsWritten, want.Name) {
				out.PortsWritten = append(out.PortsWritten, want.Name)
			}
		}
	}

	// Aggregates the desired list no longer carries are DISSOLVED — replace
	// semantics, stated on the contract. The bond's bridge row goes first
	// (a bridge port whose interface vanished is an invalid row), then the
	// bonding row; member ports keep their own rows and reactivate on their
	// own, which is the platform's behaviour, not ours.
	for i = range s.Bondings {
		var b = s.Bondings[i]
		var std = lagStdName(b.Name)
		if wantNames[std] {
			continue
		}
		var port, inBridge = s.Ports[b.Name]
		if inBridge && port.RowID != "" {
			if _, err = c.Remove(ctx, "interface/bridge/port", port.RowID); err != nil {
				return out, fmt.Errorf("aggregate %s: bridge row not removed: %v", std, err)
			}
		}
		if _, err = c.Remove(ctx, "interface/bonding", b.RowID); err != nil {
			return out, fmt.Errorf("aggregate %s not removed: %v", std, err)
		}
		c.step(true, "aggregate dissolved", b.Name)
		out.PortsWritten = append(out.PortsWritten, std)
		// Its members return to the bridge as plain ports — the enslaving
		// removed their rows (platform rule: a member cannot stay in the
		// bridge), so dissolution must give them back or they switch nothing.
		var m string
		for _, m = range b.Slaves {
			var _, alreadyBack = s.Ports[m]
			if alreadyBack {
				continue
			}
			if _, err = c.Add(ctx, "interface/bridge/port", map[string]string{
				"bridge": s.BridgeName, "interface": m, "pvid": "1",
			}); err != nil {
				return out, fmt.Errorf("aggregate %s dissolved but member %s not "+
					"returned to the bridge: %v", std, m, err)
			}
		}
		out.Notes = append(out.Notes, "aggregate "+std+" dissolved: its members "+
			"returned to the bridge untagged in VLAN 1 — reassign them from the "+
			"faceplate")
	}
	return out, nil
}

// applyLAGVLAN writes the bond's PVID and tagged membership by editing the
// same rows the port applier edits. Returns whether anything was written.
func (c *Client) applyLAGVLAN(ctx context.Context, s *DeviceState, devName string,
	want PortState, out *ApplyResult) (bool, error) {
	var wrote bool
	// PVID rides the bond's bridge-port row; create it if the bond has none.
	var port, inBridge = s.Ports[devName]
	var pvid = want.PVID
	if pvid == 0 {
		pvid = 1
	}
	if !inBridge {
		var _, err = c.Add(ctx, "interface/bridge/port", map[string]string{
			"bridge": s.BridgeName, "interface": devName,
			"pvid": strconv.Itoa(pvid),
		})
		if err != nil {
			return wrote, fmt.Errorf("%s: bridge row not created: %v", devName, err)
		}
		wrote = true
	} else if port.PVID != pvid {
		var _, err = c.Set(ctx, "interface/bridge/port", port.RowID, map[string]string{
			"pvid": strconv.Itoa(pvid),
		})
		if err != nil {
			return wrote, fmt.Errorf("%s: PVID not written: %v", devName, err)
		}
		wrote = true
	}

	// Tagged membership: the bond joins each wanted VLAN's tagged list and
	// leaves every other. Rows are edited in place (insertOnce keeps a member
	// listed once); a wanted VLAN with no row is created, and counted.
	var wantTagged = map[int]bool{}
	var t int
	for _, t = range want.Tagged {
		wantTagged[t] = true
	}
	if want.Mode != "trunk" {
		wantTagged = map[int]bool{}
	}
	var i int
	for i = range s.VLANs {
		var row = s.VLANs[i]
		if row.Dynamic || row.RowID == "" {
			continue
		}
		var isMember = contains(row.TaggedMembers, devName)
		var wanted = wantTagged[row.ID]
		delete(wantTagged, row.ID)
		if wanted == isMember {
			continue
		}
		var members []string
		if wanted {
			members = insertOnce(row.TaggedMembers, devName)
		} else {
			members = without(row.TaggedMembers, devName)
		}
		var _, err = c.Set(ctx, "interface/bridge/vlan", row.RowID, map[string]string{
			"tagged": strings.Join(members, ","),
		})
		if err != nil {
			return wrote, fmt.Errorf("%s: VLAN %d membership not written: %v",
				devName, row.ID, err)
		}
		wrote = true
	}
	for t = range wantTagged {
		var _, err = c.Add(ctx, "interface/bridge/vlan", map[string]string{
			"bridge": s.BridgeName, "vlan-ids": strconv.Itoa(t), "tagged": devName,
		})
		if err != nil {
			return wrote, fmt.Errorf("%s: VLAN %d not created: %v", devName, t, err)
		}
		out.VLANsCreated = append(out.VLANsCreated, t)
		wrote = true
	}
	sort.Ints(out.VLANsCreated)
	return wrote, nil
}

// without: the removal half; `contains` already lives in mgmtaddr.go.
func without(list []string, item string) []string {
	var out = make([]string, 0, len(list))
	var v string
	for _, v = range list {
		if v != item {
			out = append(out, v)
		}
	}
	return out
}

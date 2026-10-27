package swos

import (
	"context"
	"fmt"
	"strings"

	"meshcraft/internal/driver"
)

// Management addressing WRITE paths on SwOS: same-VLAN apply and single-address
// jump. Never [driver.MgmtMover] — this platform carries one address and one
// `avln` at a time (cf. contract.go).
//
// Both paths take a configuration backup first (GET /backup.swb) and rewrite
// /sys.b via read-modify-rewrite of the writable field set from backup.swb.
// Changing `ip` / `iptp` / `avln` cuts the commanding session; OutOfBand is
// false — the backup is the only soft net.

var (
	_ driver.MgmtAddrWriter = (*Admin)(nil)
	_ driver.MgmtJumper     = (*Admin)(nil)
)

// ApplyMgmtAddressing sets DHCP or static addressing WITHOUT changing `avln`.
func (a *Admin) ApplyMgmtAddressing(ctx context.Context, want driver.MgmtAddrIntent) (driver.MgmtAddrResult, error) {
	var out driver.MgmtAddrResult
	var backup, err = a.Backup(ctx)
	if err != nil {
		return out, fmt.Errorf("backup before addressing apply: %w", err)
	}
	out.Backup = []byte(backup)

	if err = a.writeSysMgmt(ctx, want, 0, false); err != nil {
		return out, err
	}
	out.Notes = append(out.Notes,
		"single management address: the session may drop if the IP or mode changed; "+
			"relocate the device if it no longer answers")
	if want.Gateway != "" || len(want.DNS) > 0 {
		out.Notes = append(out.Notes,
			"gateway and DNS were ignored: this platform has neither field")
	}

	var observed driver.MgmtAddrState
	if observed, err = a.ReadMgmtAddressing(ctx); err != nil {
		out.Notes = append(out.Notes, "re-read after apply failed (session may have moved): "+err.Error())
		err = nil
	} else {
		out.Observed = observed
	}
	return out, nil
}

// JumpMgmt sets management VLAN (`avln`) and/or addressing in one shot.
//
// vlanID 0 keeps the current Allow-From VLAN. vlanID > 0 requires that VLAN to
// already exist in /vlan.b — membership is the caller's duty (create VLAN +
// member ports BEFORE this jump).
func (a *Admin) JumpMgmt(ctx context.Context, vlanID int, want driver.MgmtAddrIntent) ([]byte, error) {
	if vlanID < 0 {
		return nil, fmt.Errorf("management VLAN %d is not usable", vlanID)
	}
	if vlanID > 0 {
		var vlanErr = a.requireVLANInTable(ctx, vlanID)
		if vlanErr != nil {
			return nil, vlanErr
		}
	}
	var backup, err = a.Backup(ctx)
	if err != nil {
		return nil, fmt.Errorf("backup before management jump: %w", err)
	}
	if err = a.writeSysMgmt(ctx, want, vlanID, vlanID > 0); err != nil {
		return []byte(backup), err
	}
	a.step(true, "management jump applied",
		fmt.Sprintf("avln change=%v vlan=%d mode=%s — relocate if unreachable; restore backup if silent",
			vlanID > 0, vlanID, want.Mode))
	return []byte(backup), nil
}

func (a *Admin) requireVLANInTable(ctx context.Context, vlanID int) error {
	var rows, err = a.GetTable(ctx, PageVLAN)
	if err != nil {
		return fmt.Errorf("read %s before jump: %w", PageVLAN, err)
	}
	var row *Page
	for _, row = range rows {
		var vid, ok = row.Num(FieldVLANID)
		if ok && int(vid) == vlanID {
			return nil
		}
	}
	return fmt.Errorf("VLAN %d is not in %s: create it and make the administration "+
		"port a member BEFORE jumping Allow-From VLAN — otherwise the CPU never sees "+
		"admin frames", vlanID, PageVLAN)
}

// writeSysMgmt read-modify-rewrites /sys.b addressing fields.
//
// When changeAvln is true, avln is set to vlanID. Empty want.Mode leaves mode
// and address untouched (VLAN-only jump).
func (a *Admin) writeSysMgmt(ctx context.Context, want driver.MgmtAddrIntent, vlanID int, changeAvln bool) error {
	var page, err = a.Get(ctx, PageSys)
	if err != nil {
		return err
	}
	if err = paintSysMgmt(page, want, vlanID, changeAvln); err != nil {
		return err
	}
	var fields, ok = WritableFields(PageSys)
	if !ok {
		return fmt.Errorf("writable field set for %s unknown: write refused", PageSys)
	}
	var body *Page
	var missing []string
	body, missing = page.Only(fields)
	if len(missing) > 0 {
		var e = fmt.Errorf("%s: firmware does not serve fields %v expected by the "+
			"connector — write refused; posting without them would let the device "+
			"zero them", PageSys, missing)
		a.step(false, "unexpected /sys.b field set", e.Error())
		return e
	}
	return a.postRaw(ctx, PageSys, body.Encode(),
		"management addressing written on "+PageSys)
}

// paintSysMgmt applies intent onto a live /sys.b page (pure aside from validation).
func paintSysMgmt(page *Page, want driver.MgmtAddrIntent, vlanID int, changeAvln bool) error {
	if changeAvln {
		if vlanID < 0 || vlanID > 4094 {
			return fmt.Errorf("Allow-From VLAN %d out of range 0–4094", vlanID)
		}
		page.SetNum(FieldAllowVLAN, uint32(vlanID))
	}
	switch want.Mode {
	case "":
		// VLAN-only jump: leave iptp/ip alone.
	case driver.MgmtAddrModeStatic:
		var host = hostOfCIDR(want.Address)
		if host == "" {
			return fmt.Errorf("static addressing requested without an address")
		}
		var enc, ok = EncodeIPv4(host)
		if !ok {
			return fmt.Errorf("static address %q is not a usable IPv4 host", want.Address)
		}
		page.SetNum(FieldAddrMode, AddrStatic)
		page.SetNum(FieldIP, enc)
	case driver.MgmtAddrModeDHCP:
		// DHCP only — not "with fallback": the intent is DHCP from the site plan.
		page.SetNum(FieldAddrMode, AddrDHCPOnly)
	default:
		return fmt.Errorf("unsupported addressing mode %q (expected static or dhcp)", want.Mode)
	}
	return nil
}

// hostOfCIDR strips an optional /prefix; SwOS has no mask field.
func hostOfCIDR(addr string) string {
	var s = strings.TrimSpace(addr)
	if s == "" {
		return ""
	}
	var slash = strings.IndexByte(s, '/')
	if slash >= 0 {
		s = s[:slash]
	}
	return strings.TrimSpace(s)
}

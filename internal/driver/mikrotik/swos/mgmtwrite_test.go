package swos

import (
	"testing"

	"meshcraft/internal/driver"
)

func TestHostOfCIDRStripsPrefix(t *testing.T) {
	if hostOfCIDR("10.99.0.36/24") != "10.99.0.36" {
		t.Fatal(hostOfCIDR("10.99.0.36/24"))
	}
	if hostOfCIDR("10.99.0.36") != "10.99.0.36" {
		t.Fatal(hostOfCIDR("10.99.0.36"))
	}
	if hostOfCIDR("") != "" {
		t.Fatal("empty")
	}
}

func TestPaintSysMgmtStatic(t *testing.T) {
	var page, err = ParseObject("{avln:0x63,ip:0x2400630a,iptp:0x01,alla:0x00,allm:0x00,allp:0x03ffffff}")
	if err != nil {
		t.Fatal(err)
	}
	if err = paintSysMgmt(page, driver.MgmtAddrIntent{
		Mode:    driver.MgmtAddrModeStatic,
		Address: "10.99.0.40/24",
	}, 0, false); err != nil {
		t.Fatal(err)
	}
	var ip, _ = page.Num(FieldIP)
	var mode, _ = page.Num(FieldAddrMode)
	var avln, _ = page.Num(FieldAllowVLAN)
	if DecodeIPv4(ip) != "10.99.0.40" {
		t.Errorf("ip = %s", DecodeIPv4(ip))
	}
	if mode != AddrStatic {
		t.Errorf("iptp = %d", mode)
	}
	if avln != 99 {
		t.Errorf("avln must stay 99, got %d", avln)
	}
}

func TestPaintSysMgmtDHCPAndVLANJump(t *testing.T) {
	var page, err = ParseObject("{avln:0x63,ip:0x2400630a,iptp:0x01}")
	if err != nil {
		t.Fatal(err)
	}
	if err = paintSysMgmt(page, driver.MgmtAddrIntent{Mode: driver.MgmtAddrModeDHCP}, 100, true); err != nil {
		t.Fatal(err)
	}
	var mode, _ = page.Num(FieldAddrMode)
	var avln, _ = page.Num(FieldAllowVLAN)
	if mode != AddrDHCPOnly {
		t.Errorf("iptp = %d, want DHCP only", mode)
	}
	if avln != 100 {
		t.Errorf("avln = %d, want 100", avln)
	}
}

func TestSysWritableFieldsMatchBackup(t *testing.T) {
	var fields, ok = WritableFields(PageSys)
	if !ok {
		t.Fatal("PageSys must have a writable field set")
	}
	var want = []string{
		"id", "wdt", "dsc", "pdsc", "ivl", "alla", "allm", "avln", "allp",
		"mgmt", "prio", "cost", "frmc", "poe", "igmp", "igmq", "igfl", "igve",
		"ip", "dtrp", "ainf", "iptp",
	}
	if len(fields) != len(want) {
		t.Fatalf("len=%d want %d: %v", len(fields), len(want), fields)
	}
	var i int
	for i = range want {
		if fields[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, fields[i], want[i])
		}
	}
}

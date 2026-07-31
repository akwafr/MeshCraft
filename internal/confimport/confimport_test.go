package confimport

import (
	"testing"
)

const iosConfig = `
!
hostname sw-atelier-01
!
vlan 10
 name Bureaux
!
vlan 20
 name Voix
!
vlan 30,31
 name Ignored
!
interface GigabitEthernet1/0/1
 description Accounting workstation
 switchport mode access
 switchport access vlan 10
 switchport voice vlan 20
 power inline auto
!
interface GigabitEthernet1/0/2
 switchport mode access
 switchport access vlan 10
!
interface GigabitEthernet1/0/24
 description Uplink coeur
 switchport mode trunk
 switchport trunk native vlan 1
 switchport trunk allowed vlan 10,20,30-31
!
interface TenGigabitEthernet1/1/1
 switchport mode trunk
!
interface Vlan10
 ip address 10.0.10.2 255.255.255.0
!
interface GigabitEthernet1/0/48
 no switchport
!
end
`

func TestParseIOS(t *testing.T) {
	var res, err = Parse(iosConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Dialect != "ios" {
		t.Fatalf("dialecte %q", res.Dialect)
	}
	if res.Hostname != "sw-atelier-01" {
		t.Fatalf("hostname %q", res.Hostname)
	}

	// VLANs : 1 (native), 10, 20, 30, 31.
	var want = []int{1, 10, 20, 30, 31}
	if len(res.Vlans) != len(want) {
		t.Fatalf("%d VLANs, expected %d : %+v", len(res.Vlans), len(want), res.Vlans)
	}
	var i int
	for i = range want {
		if res.Vlans[i].VlanID != want[i] {
			t.Fatalf("VLAN[%d] = %d, expected %d", i, res.Vlans[i].VlanID, want[i])
		}
	}
	if res.Vlans[1].Name != "Bureaux" {
		t.Fatalf("nom du VLAN 10 : %q", res.Vlans[1].Name)
	}

	// Un « name » qui porte sur plusieurs VLANs ne doit pas be applique.
	if res.Vlans[3].Name != "" || res.Vlans[4].Name != "" {
		t.Fatalf("nom applique a une plage de VLANs : %+v", res.Vlans[3:])
	}

	// Port access avec VLAN voix tagge.
	var p1 = res.Ports["GigabitEthernet1/0/1"]
	if p1.Mode != "access" || p1.PVID == nil || *p1.PVID != 10 {
		t.Fatalf("Gi1/0/1 : %+v", p1)
	}
	if p1.Label != "Accounting workstation" {
		t.Fatalf("description : %q", p1.Label)
	}
	var tagged, ok = p1.Tagged.([]int)
	if !ok || len(tagged) != 1 || tagged[0] != 20 {
		t.Fatalf("VLAN voix non tagge : %+v", p1.Tagged)
	}

	// Trunk avec liste explicite.
	var up = res.Ports["GigabitEthernet1/0/24"]
	if up.Mode != "trunk" || up.PVID == nil || *up.PVID != 1 {
		t.Fatalf("uplink : %+v", up)
	}
	tagged, ok = up.Tagged.([]int)
	if !ok || len(tagged) != 4 {
		t.Fatalf("VLANs du trunk : %+v", up.Tagged)
	}

	// Trunk sans liste : tous les VLANs, avec avertissement.
	var te = res.Ports["TenGigabitEthernet1/1/1"]
	if te.Tagged != "all" {
		t.Fatalf("trunk implicite : %+v", te.Tagged)
	}

	// Interface logique exclue, port route exclu.
	var _, hasVlanIface = res.Ports["Vlan10"]
	if hasVlanIface {
		t.Fatal("interface Vlan10 importee comme port physique")
	}
	var _, hasRouted = res.Ports["GigabitEthernet1/0/48"]
	if hasRouted {
		t.Fatal("port route importe")
	}

	// Facade : deux groupes, PoE vu sur le groupe gigabit.
	if len(res.PortGroups) != 2 {
		t.Fatalf("groupes : %+v", res.PortGroups)
	}
	var g = res.PortGroups[0]
	if g.Prefix != "GigabitEthernet1/0/" || g.From != 1 || g.To != 24 {
		t.Fatalf("groupe gigabit : %+v", g)
	}
	if !g.PoE {
		t.Fatal("PoE non detecte")
	}
	if g.SpeedMbps != 1000 {
		t.Fatalf("vitesse deduced : %d", g.SpeedMbps)
	}
	if res.PortGroups[1].SpeedMbps != 10000 {
		t.Fatalf("vitesse 10G deduced : %d", res.PortGroups[1].SpeedMbps)
	}
}

const comwareConfig = `
#
 sysname sw-regie
#
vlan 100
 name Video
#
vlan 200
 name Audio
#
interface GigabitEthernet0/0/1
 port link-type access
 port default vlan 100
#
interface GigabitEthernet0/0/2
 description Camera 2
 port link-type trunk
 port trunk pvid vlan 100
 port trunk allow-pass vlan 100 200 to 202
#
interface GigabitEthernet0/0/3
 port link-type trunk
 port trunk allow-pass vlan all
#
return
`

func TestParseComware(t *testing.T) {
	var res, err = Parse(comwareConfig, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Dialect != "comware" {
		t.Fatalf("dialecte %q", res.Dialect)
	}
	if res.Hostname != "sw-regie" {
		t.Fatalf("hostname %q", res.Hostname)
	}
	var acc = res.Ports["GigabitEthernet0/0/1"]
	if acc.Mode != "access" || acc.PVID == nil || *acc.PVID != 100 {
		t.Fatalf("access port : %+v", acc)
	}
	var tr = res.Ports["GigabitEthernet0/0/2"]
	if tr.Mode != "trunk" || tr.PVID == nil || *tr.PVID != 100 {
		t.Fatalf("trunk : %+v", tr)
	}
	var tagged, ok = tr.Tagged.([]int)
	if !ok || len(tagged) != 4 {
		// 100, 200, 201, 202
		t.Fatalf("« 200 to 202 » mal etendu : %+v", tr.Tagged)
	}
	if res.Ports["GigabitEthernet0/0/3"].Tagged != "all" {
		t.Fatalf("allow-pass vlan all : %+v", res.Ports["GigabitEthernet0/0/3"].Tagged)
	}
}

func TestExpandVlanList(t *testing.T) {
	var cases = []struct {
		in   string
		want int
		ok   bool
	}{
		{"10", 1, true},
		{"10,20,30", 3, true},
		{"10-12", 3, true},
		{"10 20 30 to 32", 5, true},
		{"1-4094", 4094, true},
		{"", 0, false},
		{"0", 0, false},
		{"4095", 0, false},
		{"30-10", 0, false},
		{"abc", 0, false},
	}
	var c struct {
		in   string
		want int
		ok   bool
	}
	for _, c = range cases {
		var got, err = expandVlanList(c.in)
		if c.ok != (err == nil) {
			t.Fatalf("%q : err=%v, expected ok=%v", c.in, err, c.ok)
		}
		if c.ok && len(got) != c.want {
			t.Fatalf("%q : %d valeurs, expected %d", c.in, len(got), c.want)
		}
	}
}

func TestParseRejectsUnknownDialect(t *testing.T) {
	var _, err = Parse("Ceci n'est pas une configuration de switch.", "")
	if err == nil {
		t.Fatal("dialecte inconnu accepted")
	}
}

func TestParseEmpty(t *testing.T) {
	var _, err = Parse("   \n\t\n", "")
	if err == nil {
		t.Fatal("configuration vide accepted")
	}
}

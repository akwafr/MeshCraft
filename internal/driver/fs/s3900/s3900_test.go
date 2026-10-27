package s3900

import "testing"

func TestMgmtPushCmdsLabSession(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID:         99,
		VlanName:       "LAN99_Management",
		IP:             "192.0.2.240",
		PrefixLen:      16,
		Gateway:        "192.0.2.1",
		Username:       "mcadmin",
		Password:       "Mc8e03481e067aZz9",
		EnablePassword: "Mc8e03481e067aZz9",
		Hostname:       "test-fs-s3900",
		RemoveVlan1:    true,
		TrunkPorts:     "Eth 1/25-28",
	})
	var want = []string{
		"conf t",
		"hostname test-fs-s3900",
		"vlan database",
		"vlan 99 name LAN99_Management media ethernet",
		"exit",
		"interface vlan 99",
		"ip add 192.0.2.240/16",
		"exit",
		"ip route 0.0.0.0 0.0.0.0 192.0.2.1",
		"ip http server",
		"ip ssh server enable",
		"interface Eth 1/25-28",
		"switchport mode trunk",
		"switchport trunk allowed vlan add 99",
		"exit",
		"no interface vlan 1",
		// Credentials LAST — a session dropped by the password change must find
		// everything else already pushed (deploy log 2026-08-01).
		"username mcadmin privilege 15",
		"username mcadmin password 0 Mc8e03481e067aZz9",
		"enable password 0 Mc8e03481e067aZz9",
		"end",
		"copy running-config startup-config",
		// Answer to "Startup configuration file name:" (lab OpCode 1.7.0) —
		// explicit so a default never points the save at Factory_Default_Config.cfg.
		"startup1.cfg",
	}
	if len(cmds) != len(want) {
		t.Fatalf("len=%d want %d\ncmds=%v", len(cmds), len(want), cmds)
	}
	var i int
	for i = 0; i < len(want); i++ {
		if cmds[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, cmds[i], want[i])
		}
	}
}

func TestParseUpEthernetPorts(t *testing.T) {
	var brief = `
Eth 1/27  10GBASE SFP+ Up      Down          Disable       10Gfull      None
Eth 1/28  10GBASE SFP+ Up      Up            Disable       10Gfull      None
`
	var ports = ParseUpEthernetPorts(brief)
	if len(ports) != 1 || ports[0] != "Eth 1/28" {
		t.Fatalf("ports=%v", ports)
	}
	if CompactEthRange(ports) != "Eth 1/28" {
		t.Fatalf("compact=%q", CompactEthRange(ports))
	}
	if CompactEthRange([]string{"Eth 1/25", "Eth 1/26", "Eth 1/27", "Eth 1/28"}) != "Eth 1/25-28" {
		t.Fatalf("range compact failed")
	}
}

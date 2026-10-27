package discovery

import (
	"strings"
)

// ResetProcedure describes how to bring a device back to its factory
// configuration. The procedure belongs to the driver: it is platform
// knowledge, in the same way as the command that reads the configuration.
//
// Two paths are given when they exist, because they do not serve in the same
// situation:
//
//   - Software: administrator access is still available. Clean, reversible if
//     a backup was taken beforehand.
//   - Hardware: access is lost (unknown password). Assumes physical access,
//     and interrupts service.
type ResetProcedure struct {
	Driver string `json:"driver"`
	// Brand is the grouping identity of the /add/reset screen — ADR-0010.
	//
	// Vendor is a LABEL: it is shown in a chip, and it will be rewritten the
	// day the wording improves. Grouping on it would let a copy edit split a
	// brand in the interface, silently. Brand is a stable slug; two procedures
	// that share it must carry the SAME Vendor, and reset_brand_test.go refuses
	// anything else.
	Brand  string `json:"brand"`
	Vendor string `json:"vendor"`
	// Family is the readable label, Aliases feed the search (models, product
	// lines, OS names).
	Family  string   `json:"family"`
	Aliases []string `json:"aliases"`

	Software []string `json:"software"`
	Hardware []string `json:"hardware"`

	// Erases says what the procedure actually erases — that is often where
	// the bad surprises hide (VLAN database, licences, certificates).
	Erases string `json:"erases"`
	// Caveats: known traps, to read before acting.
	Caveats []string `json:"caveats"`
	// Verified separates what is stable across a family from what varies
	// from one model to another and must be confirmed on the product sheet.
	Verified bool `json:"verified"`
}

// resetProcedures: families covered by the recognised drivers.
//
// These procedures are per family, not per reference. Where the hardware
// gesture varies with the model (button location, how long to hold it),
// Verified is false and the UI asks for confirmation against the model's
// documentation — getting a reset procedure wrong costs time at best, and
// access to the device at worst.
var resetProcedures = []ResetProcedure{
	{
		Driver: "cisco-ios", Brand: "cisco", Vendor: "Cisco", Family: "Catalyst / IOS, IOS-XE",
		Aliases: []string{"catalyst", "ios", "ios-xe", "ws-c2960", "c9200", "c9300", "c3750", "c3850"},
		Software: []string{
			"enable",
			"write erase",
			"delete flash:vlan.dat      (the VLAN database does NOT go with write erase)",
			"reload   — answer \"no\" when asked to save the configuration",
		},
		Hardware: []string{
			"Cut power to the switch.",
			"Hold the MODE button down and power it back on.",
			"Release when the SYST LED blinks amber then turns green (10–15 s): you reach the \"switch:\" prompt.",
			"switch: flash_init",
			"switch: del flash:config.text   (and del flash:vlan.dat if needed)",
			"switch: boot",
		},
		Erases: "startup configuration; the VLAN database (vlan.dat) and the IOS images remain unless explicitly deleted",
		Caveats: []string{
			"On recent IOS-XE, prefer \"erase startup-config\" then \"reload\"; the MODE button does not open a bootloader prompt on every model.",
			"Connector: internal/driver/cisco/ios (MgmtPushCmds); the Web Serial soft reset " +
				"replays the same commands. Console is 9600 8N1 on this range, NOT 115200.",
			"After the reboot the switch offers its initial configuration dialog: it must be " +
				"declined (\"no\") before any command is accepted.",
		},
		Verified: true,
	},
	{
		Driver: "cisco-nxos", Brand: "cisco", Vendor: "Cisco", Family: "Nexus / NX-OS",
		Aliases: []string{"nexus", "nx-os", "n3k", "n5k", "n9k"},
		Software: []string{
			"write erase          (erases the startup configuration)",
			"write erase boot     (also erases the boot variables — careful, the image must be specified afterwards)",
			"reload",
		},
		Hardware: []string{
			"Interrupt the boot to enter the loader (Ctrl-] then power on, depending on the platform).",
			"loader> boot <nx-os image>  then apply the password recovery documented for the model.",
		},
		Erases:   "startup configuration; \"write erase boot\" also touches the boot variables",
		Caveats:  []string{"\"write erase boot\" without an identified fallback image can leave the device unable to boot."},
		Verified: false,
	},
	{
		Driver: "arista-eos", Brand: "arista", Vendor: "Arista", Family: "EOS (7000 / 720 / 750)",
		Aliases: []string{"arista", "eos", "dcs-7050", "dcs-7020", "720xp", "750", "ztp", "zerotouch"},
		Software: []string{
			"enable",
			"delete flash:startup-config      (ASKS NOTHING: no Cisco-style \"[confirm]\")",
			"reload now                       — \"now\" avoids the \"Save?\" question whose default " +
				"answer would rewrite the configuration just erased",
			"— the switch comes back in ZTP, which is NOT the end of the reset —",
			"log in as \"admin\" (no password)",
			"zerotouch cancel                 (writes an EMPTY startup-config and reboots a 2nd time)",
		},
		Hardware: []string{
			"No reset button on this range: the console port IS the recovery path.",
			"Interrupt the boot with Ctrl-C at \"Press Ctrl-C now to enter Aboot shell\".",
			"Aboot# cd /mnt/flash",
			"Aboot# rm startup-config",
			"Aboot# exit",
		},
		Erases: "startup configuration (/mnt/flash/startup-config). There is NO separate VLAN " +
			"database like vlan.dat: VLANs live in the startup-config and go with it",
		Caveats: []string{
			"THE RESET LEAVES THE SWITCH IN ZTP, AND A SWITCH IN ZTP LOOKS LIKE A WORKING SWITCH. " +
				"Every port comes up, DHCP goes out on each, the console shows a login " +
				"prompt. A configuration written there is thrown away at the next ZTP cycle: " +
				"\"zerotouch cancel\" first, everything else after.",
			"ZTP BRINGS EVERY PORT UP AT ONCE, unconfigured and in VLAN 1. On a switch " +
				"already cabled into a production infrastructure, that is a broadcast " +
				"domain nobody planned for: resetting a cabled Arista is done with the " +
				"uplinks unplugged, or in a maintenance window.",
			"Console at 9600 8N1 — same speed as the Cisco range, NOT the 115200 of the FS " +
				"and Ubiquiti ranges.",
			"Connector: internal/driver/arista/eos (MgmtPushCmds); the Web Serial provisioning " +
				"script replays the same commands and detects the ZTP state before sending any.",
			"THIRD-PARTY SFP: EOS refuses any optic it does not read as Arista-coded and " +
				"err-disables the port (cause \"xcvr-unsupported\"), after which the cage reads " +
				"exactly like an EMPTY one. Provisioning applies \"service " +
				"unsupported-transceiver\" + \"errdisable recovery cause xcvr-unsupported\"; a " +
				"factory reset removes both.",
			"There is NO \"ip default-gateway\" on this platform (it is \"ip route " +
				"0.0.0.0/0 <gw>\"), and the address is written as a PREFIX (\"ip address 192.0.2.5/24\"): " +
				"both IOS reflexes produce a refused line.",
		},
		// Nothing has been executed on an Arista by this connector: the gestures
		// are the ones the vendor documents, which is not the same thing as
		// verified — and the UI must be able to say which of the two it is.
		Verified: false,
	},
	{
		Driver: "aruba-cx", Brand: "hpe", Vendor: "HPE / Aruba", Family: "ArubaOS-CX (6000/6100/6300/8320…)",
		Aliases: []string{"aos-cx", "arubaos-cx", "6100", "6300", "8320", "jl679", "jl667"},
		Software: []string{
			"erase startup-config           (configuration only)",
			"erase all zeroize              (complete factory reset, including certificates and keys)",
			"boot system                    (reboot)",
		},
		Hardware: []string{
			"Reset button on the front panel (often recessed, to be pressed with a paperclip).",
			"Short press = reboot; long press = factory reset — the duration depends on the model, check it first.",
		},
		Erases:   "configuration; \"zeroize\" also destroys the certificates, SSH keys and stored secrets",
		Caveats:  []string{"\"erase all zeroize\" is irreversible and invalidates certificates already deployed.", "How long to hold the button varies with the reference."},
		Verified: false,
	},
	{
		Driver: "hpe-procurve", Brand: "hpe", Vendor: "HPE / Aruba", Family: "ProCurve / Aruba 2530, 2930",
		Aliases: []string{"procurve", "2530", "2920", "2930", "j9773", "j9776"},
		Software: []string{
			"erase startup-config",
			"reload",
		},
		Hardware: []string{
			"Hold the Reset and Clear buttons down at the same time.",
			"Release Reset while keeping Clear pressed.",
			"Release Clear when the \"Self Test\" LED blinks (about 10 s): the factory configuration is restored.",
		},
		Erases:   "startup configuration and local passwords",
		Caveats:  []string{"Clear alone, pressed briefly, only erases the passwords — useful to regain control without losing the configuration."},
		Verified: true,
	},
	{
		Driver: "juniper-junos", Brand: "juniper", Vendor: "Juniper", Family: "JunOS (EX, QFX)",
		Aliases: []string{"junos", "ex2200", "ex2300", "ex3400", "qfx"},
		Software: []string{
			"configure",
			"load factory-default",
			"set system root-authentication plain-text-password    (mandatory: JunOS refuses to commit without a root password)",
			"commit and-quit",
			"— or, to erase everything including the logs: request system zeroize",
		},
		Hardware: []string{
			"Interrupt the boot (Ctrl-C in the loader) through the serial console.",
			"Boot in single-user mode, then \"recovery\" to redefine the root password.",
		},
		Erases:   "configuration; \"zeroize\" also erases the keys, logs and user data",
		Caveats:  []string{"\"load factory-default\" does not commit without a root password defined in the same transaction."},
		Verified: true,
	},
	{
		Driver: "mikrotik-routeros", Brand: "mikrotik", Vendor: "MikroTik", Family: "RouterOS",
		Aliases: []string{"routeros", "crs", "css", "hex", "rb"},
		Software: []string{
			"/system reset-configuration",
			"/system reset-configuration no-defaults=yes    (start from a truly empty configuration)",
			"/system reset-configuration keep-users=yes      (keep the accounts)",
		},
		Hardware: []string{
			"Powered off, hold the Reset button (or bridge the RES jumper, depending on the model).",
			"Power on while holding it: the ACT LED starts blinking (≈ 5 s) → configuration reset.",
			"Keep holding until the LED goes out (≈ 10 s) → Netinstall mode (full reinstallation over the network).",
		},
		Erases:   "configuration; Netinstall mode reinstalls RouterOS entirely",
		Caveats:  []string{"Without \"no-defaults=yes\", RouterOS re-applies its default configuration, which includes a DHCP server and a firewall — a frequent surprise in production."},
		Verified: true,
	},
	{
		// Family names the PATH, not the product line — ADR-0010. The two
		// Ubiquiti procedures are now listed side by side under one brand, and
		// "UniFi / EdgeSwitch" next to "EdgeSwitch / UniFi Switch (FASTPATH)"
		// was a choice between two anagrams. What really separates them:
		// adoption by the controller on one side, the FASTPATH CLI on the
		// other.
		Driver: "ubiquiti", Brand: "ubiquiti", Vendor: "Ubiquiti", Family: "UniFi (adopted by the controller)",
		Aliases: []string{"unifi", "usw", "edgeswitch", "edgemax", "edgeos"},
		Software: []string{
			"UniFi: from the controller, \"Forget this device\" — the device goes back to waiting for adoption.",
			"EdgeSwitch: erase startup-config then reload.",
		},
		Hardware: []string{
			"Hold the Reset button for about 10 s, device powered on.",
			"Release when the LEDs blink: factory reset and waiting for adoption.",
		},
		Erases:   "configuration and association with the controller",
		Caveats:  []string{"A reset UniFi waits for a new adoption: make sure you have access to the controller before resetting remotely."},
		Verified: true,
	},
	{
		Driver: "extreme-exos", Brand: "extreme", Vendor: "Extreme", Family: "ExtremeXOS",
		Aliases:  []string{"exos", "summit", "x440", "x460"},
		Software: []string{"unconfigure switch all    (reboots on a blank configuration)"},
		Hardware: []string{"Through the serial console: interrupt the boot and select the factory configuration in the boot menu."},
		Erases:   "the whole configuration, including VLANs and accounts",
		Caveats:  []string{"\"unconfigure switch\" without \"all\" keeps part of the settings."},
		Verified: true,
	},
	{
		Driver: "huawei-vrp", Brand: "huawei", Vendor: "Huawei", Family: "VRP (S-series)",
		Aliases: []string{"vrp", "s5700", "s5720", "s6720", "huawei"},
		Software: []string{
			"reset saved-configuration",
			"reboot        (answer \"n\" when asked to save)",
		},
		Hardware: []string{"Through the serial console: interrupt the boot (Ctrl-B), default BootROM password, then \"Clear password\" / \"Reset configuration\" in the menu."},
		Erases:   "saved configuration file",
		Caveats:  []string{"The default BootROM password is often changed by the integrator; without it, the menu is out of reach."},
		Verified: true,
	},
	{
		Driver: "h3c-comware", Brand: "h3c", Vendor: "H3C / HPE Comware", Family: "Comware 5/7",
		Aliases:  []string{"comware", "h3c", "5130", "5820", "hpe comware"},
		Software: []string{"reset saved-configuration", "reboot"},
		Hardware: []string{"Serial console: interrupt the boot (Ctrl-B) and choose \"Skip current system configuration\" in the BootWare menu."},
		Erases:   "saved configuration",
		Caveats:  []string{"\"Skip configuration\" only ignores the configuration at the next boot: it is still present in flash."},
		Verified: true,
	},
	{
		Driver: "dell-os10", Brand: "dell", Vendor: "Dell", Family: "OS10 / PowerConnect",
		Aliases: []string{"os10", "powerconnect", "n1500", "n2000", "s4048"},
		Software: []string{
			"OS10: delete startup-configuration then reload",
			"PowerConnect: clear config then copy running-config startup-config",
		},
		Hardware: []string{"Serial console: interrupt the boot and use the platform's recovery menu."},
		Erases:   "startup configuration",
		Caveats:  []string{"OS10 and PowerConnect are two different CLIs: check which one is running before applying a command."},
		Verified: false,
	},
	{
		Driver: "fortiswitch", Brand: "fortinet", Vendor: "Fortinet", Family: "FortiSwitch",
		Aliases:  []string{"fortiswitch", "fortilink", "fs-108", "fs-224"},
		Software: []string{"execute factoryreset          (or \"execute factoryreset2\" to keep the image)"},
		Hardware: []string{"Serial console: interrupt the boot and choose the factory configuration in the FortiSwitch BIOS menu."},
		Erases:   "configuration; the device may go back to waiting for FortiLink authorisation",
		Caveats:  []string{"A FortiSwitch managed over FortiLink must be re-authorised on the FortiGate after a reset."},
		Verified: false,
	},
	{
		Driver: "ubiquiti-edgeswitch", Brand: "ubiquiti", Vendor: "Ubiquiti", Family: "EdgeSwitch / UniFi Switch (FASTPATH)",
		Aliases: []string{
			"edgeswitch", "ubnt", "unifi switch", "us-16-150w", "us-8", "us-24", "us-48",
			"es-16", "es-24", "es-48", "unifi s16",
		},
		Software: []string{
			"Console: 115200 8N1 — factory credentials ubnt/ubnt (documented by Ubiquiti, not captured here)",
			"enable   (prompt \"(UBNT EdgeSwitch) #\")",
			"clear config   — confirm \"y\"; resets the configuration to defaults WITHOUT rebooting",
			"MIND THE MODES: \"network parms\", \"network protocol\", \"network mgmt_vlan\", " +
				"\"vlan database\", \"write memory\" and \"clear config\" are in PRIVILEGED EXEC, " +
				"NOT in Global Config",
			"vlan database → vlan N → vlan name N <name> → exit",
			"network protocol none",
			"network parms <ip> <mask> <gateway>   — management is NOT an SVI: " +
				"a single network interface, no \"interface vlan N\" + \"ip address\"",
			"network mgmt_vlan N",
			"configure → username <U> password <secret> level 15   — NO \"hostname\": " +
				"this CLI does not have that command (captured 2026-08-05 on the lab's EdgePoint S16: " +
				"\"% Invalid input detected at '^' marker.\")",
			"uplink: interface 0/N → vlan participation include N → vlan tagging N → exit   " +
				"(a port needs THREE commands: pvid, participation, tagging)",
			"write memory confirm   — \"confirm\" avoids the question a script would block on",
		},
		Hardware: []string{
			"Reset button held ~10 s, switch powered on — check the duration on the model's sheet.",
		},
		Erases: "running configuration (back to defaults); the management address is lost",
		Caveats: []string{
			"CLI reference: https://dl.ubnt.com/guides/edgemax/EdgeSwitch_CLI_Command_Reference_UG.pdf",
			"DOCUMENTED CLI, not yet captured on hardware — this is Broadcom FASTPATH, not Cisco.",
			"VLAN 1 is reserved and the creatable range is 2-4093 (not 1-4094).",
			"\"clear config\" does NOT reboot and is not persistent until \"write memory\" has been done.",
			"Driver: internal/driver/ubiquiti/edgeswitch (MgmtPushCmds).",
		},
		Verified: false,
	},
	{
		Driver: "fs-s3400", Brand: "fs", Vendor: "FS.com", Family: "S3400 PoE+ (BDCOM CLI)",
		Aliases: []string{
			"s3400", "fs3400", "fs-3400", "s3400-48t4sp", "s3400-24t4fp",
			"s3150-8t2fp", "s3260-8t2fp", "s3260-16t4fp",
		},
		Software: []string{
			"Console: 115200 8N1 — after a reset: login admin / admin (NOT admin / empty); boot takes ~2 MINUTES before the real \"Username:\" (captured 2026-08-03) and the boot traces contain lines ending in \"#\" that look like a prompt",
			"delete startup-config   — confirm \"y\" at \"this file will be erased, are you sure? (y/n)\"",
			"reboot   — if the prompt offers to SAVE the running configuration, answer NO (otherwise the erased startup-config is rewritten)",
			"config (prompt Switch_config#, NOT \"conf t\") → terminal length 0",
			"vlan N → name X → exit  (management VLAN)",
			"interface vlan N → ip address A.B.C.D M.M.M.M  (dotted mask, no /nn)",
			"default route: ip route default GW  (captured 2026-08-03; \"ip default-gateway\" → Unknown command)",
			"ip sshd enable   (NOT \"ip ssh server enable\"; key generation may leave the console silent > 10 s) — \"ip http server\" answers \"Unknown command\" on the captured firmware",
			"SFP+ uplinks: interface range tg0/1-4 (prompt Switch_config_if_range#) → switchport mode trunk; switchport trunk vlan-allowed 1-4094 — the \"range\" keyword is MANDATORY (\"int tg0/1-4\" → Parameter invalid); with no link detected, trunk all 4 cages rather than none",
			"username U password <secret> ; enable password 0 <secret>   (\"aaa authentication login default local\" → Unknown command; the factory login is already enforced)",
			"write   — the save answers \"OK!\" (captured 2026-08-03), not \"copy running-config startup-config\"",
		},
		Hardware: []string{
			"Monitor path (Ctrl+P at boot) → \"monitor#\": firmware recovery over TFTP (ip address / copy tftp flash / reboot).",
		},
		Erases: "startup configuration (startup-config file)",
		Caveats: []string{
			"PoE+ reset guide: https://img-en.fs.com/file/user_manual/poe+-series-switches-reset-and-recovery-system-configuration-guide.pdf",
			"Reset + push sequence captured on hardware on 2026-08-03 (48T4SP) — nothing in common with the S3900's FSOS CLI.",
			"Interfaces \"g0/1\" / \"GigaEthernet0/1\"; the 4 SFP+ cages are \"TGigaEthernet0/1-4\", short form tg0/1-4 (CAPTURED 2026-08-03, missing from the CLI guide).",
			"A port's access VLAN: \"switchport pvid N\", not \"switchport access vlan N\".",
			"The management VLAN must be carried on the uplink, otherwise the IP probe fails.",
			"Driver: internal/driver/fs/s3400 (MgmtPushCmds); Web Serial soft reset: web/src/pages/add/fsS3400SoftReset.ts.",
		},
		Verified: true,
	},
	{
		Driver: "fs-s3900", Brand: "fs", Vendor: "FS.com", Family: "S3900 / S3900-R",
		Aliases: []string{
			"fs", "fs.com", "s3900", "s3900-r", "fs3900", "fs-3900",
			"s3900-24t4s", "s3900-48t4s", "s3900-24f4s",
		},
		Software: []string{
			"Console: 115200 8N1 — after a reset: admin (empty password, OpCode ~1.7) or admin/admin",
			"conf t",
			"boot system config:Factory_Default_Config.cfg",
			"exit",
			"delete file unit 1 name startup1.cfg   — confirm \"y\" if asked",
			"reboot | restart (OpCode ~1.7) | reload  — confirm \"y\" if asked",
			"vlan database → vlan N name X media ethernet",
			"interface vlan N → ip add A.B.C.D/nn",
			"ip route 0.0.0.0 0.0.0.0 <gw>",
			"ip http server ; ip ssh server enable",
			"username U privilege 15 ; username U password 0 <secret> ; enable password 0 <secret>",
			"interface Eth U/P(-Q) → switchport mode trunk ; switchport trunk allowed vlan add N",
			"no interface vlan 1 (optional)",
			"copy running-config startup-config — answer the \"Startup configuration " +
				"file name:\" prompt with an explicit name (startup1.cfg), NEVER Factory_Default_Config.cfg",
		},
		Hardware: []string{
			"Reset button: short press (< 10 s) = reboot; factory procedure per the model's sheet (often a long press).",
			"Monitor path (Ctrl+P at boot) documented for recovery / TFTP on the S3900-R.",
		},
		Erases: "active configuration (startup1.cfg); boots on Factory_Default_Config.cfg; flash images kept",
		Caveats: []string{
			"S3900 reset guide: https://img-en.fs.com/file/user_manual/s3900-series-switches-reset-and-recovery-system-configuration-guide.pdf",
			"CLI verified in the lab on OpCode 1.7 — not Cisco \"delete startup-config\" / \"write\" / \"ip sshd\".",
			"The management VLAN must be carried on the uplink (trunk allowed vlan add), otherwise the IP probe fails.",
			"Connector: internal/driver/fs/s3900 (MgmtPushCmds); the Web Serial soft reset reuses the same commands.",
		},
		Verified: true,
	},
	{
		Driver: "zyxel", Brand: "zyxel", Vendor: "Zyxel", Family: "GS / XGS (ZyNOS)",
		Aliases:  []string{"zyxel", "zynos", "gs1900", "gs2210", "xgs"},
		Software: []string{"erase running-config then write memory, or \"Restore Default\" in the web interface."},
		Hardware: []string{"Hold the RESET button for about 5 s, device powered on."},
		Erases:   "configuration",
		Caveats:  []string{"Zyxel ranges differ markedly from one another (GS1900 web-only, GS2210 CLI): check the command on the model's sheet."},
		Verified: false,
	},
	{
		Driver: "netgear", Brand: "netgear", Vendor: "Netgear", Family: "GS / M4300",
		Aliases:  []string{"netgear", "gs308", "gs724", "m4300", "m4250"},
		Software: []string{"clear config then save, or \"Factory Default\" in the web interface."},
		Hardware: []string{"Hold the Factory Defaults button (often recessed) for about 10 s until the reboot."},
		Erases:   "configuration and administrator password",
		Caveats:  []string{"On \"Plus/Smart\" models without a CLI, only the web path or the button exist."},
		Verified: false,
	},
	{
		Driver: "tplink", Brand: "tplink", Vendor: "TP-Link", Family: "JetStream / Easy Smart",
		Aliases:  []string{"tp-link", "tplink", "jetstream", "t1600g", "t2600g", "tl-sg"},
		Software: []string{"reset (privileged mode) then reboot, or \"System Tools → Reset\" in the web interface."},
		Hardware: []string{"Hold the Reset button for 5 to 10 s, device powered on, until the LEDs blink."},
		Erases:   "configuration, including the management IP address",
		Caveats:  []string{"After a reset, the device takes back its factory IP: make sure you can reach the right subnet."},
		Verified: false,
	},
	{
		Driver: "dlink", Brand: "dlink", Vendor: "D-Link", Family: "DGS / DES",
		Aliases:  []string{"d-link", "dlink", "dgs-1210", "dgs-3120", "des-"},
		Software: []string{"reset system  (erases everything and reboots), or \"Tools → Reset\" in the web interface."},
		Hardware: []string{"Hold the Reset button for about 10 s, device powered on."},
		Erases:   "configuration and administrator account",
		Caveats:  []string{"\"reset config\" and \"reset system\" do not have the same scope depending on the series."},
		Verified: false,
	},
	{
		Driver: "cumulus-sonic", Brand: "whitebox", Vendor: "Linux network", Family: "Cumulus Linux / SONiC",
		Aliases: []string{"cumulus", "sonic", "onie", "white box"},
		Software: []string{
			"Cumulus: net del all then net commit (cancels the network configuration).",
			"SONiC: sudo config erase then reboot, or restore a blank config_db.json.",
		},
		Hardware: []string{"A true factory reset: reboot into ONIE (\"ONIE: Install OS\" in GRUB) and reinstall the image."},
		Erases:   "network configuration; an ONIE reinstallation erases the whole system",
		Caveats:  []string{"These are Linux systems: the \"factory configuration\" depends on the image the integrator installed."},
		Verified: true,
	},
}

// genericReset is returned for an unidentified device: we do not pretend to
// know a procedure we do not know.
var genericReset = ResetProcedure{
	Driver: "generic", Vendor: "", Family: "Unidentified device",
	Software: []string{
		"Look in the administration interface for a \"Restore defaults\" / \"Factory reset\" entry.",
		"On a CLI, the most common forms are \"erase startup-config\", \"write erase\", \"reset saved-configuration\".",
	},
	Hardware: []string{
		"A recessed Reset button, held 5 to 15 s with the device powered on, is the most common path.",
		"Failing that, the serial console gives access to the boot menu, which almost always offers the factory configuration.",
	},
	Erases:   "to be checked in the model's documentation",
	Caveats:  []string{"No certified procedure for this model: identify the device (or look up its vendor sheet) before acting."},
	Verified: false,
}

// SearchResetProcedures searches by vendor, family, driver or model. An empty
// query returns the whole catalogue, sorted as it is declared.
func SearchResetProcedures(q string) []ResetProcedure {
	var needle = strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return resetProcedures
	}
	var out = []ResetProcedure{}
	var p ResetProcedure
	for _, p = range resetProcedures {
		if resetMatches(p, needle) {
			out = append(out, p)
		}
	}
	return out
}

func resetMatches(p ResetProcedure, needle string) bool {
	var hay = strings.ToLower(strings.Join(append([]string{
		p.Driver, p.Vendor, p.Family,
	}, p.Aliases...), " "))
	// Every word of the query must appear: "cisco 2960" must not bring up
	// every Cisco.
	var word string
	for _, word = range strings.Fields(needle) {
		if !strings.Contains(hay, word) {
			return false
		}
	}
	return true
}

// ResetProcedureFor returns the procedure of a given driver.
func ResetProcedureFor(driver string) ResetProcedure {
	var p ResetProcedure
	for _, p = range resetProcedures {
		if p.Driver == driver {
			return p
		}
	}
	return genericReset
}

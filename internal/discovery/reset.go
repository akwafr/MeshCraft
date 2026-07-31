package discovery

import (
	"strings"
)

// ResetProcedure describes comment ramener un device a sa configuration
// d'usine. La procedure appartient au driver : c'est une connaissance de
// plateforme, au same titre que la commande qui lit la configuration.
//
// Deux chemins sont donnes quand ils existent, parce qu'ils ne servent pas dans
// la same situation :
//
//   - Software : on a encore un access administrateur. Propre, reversible si on
//     a sauvegarde avant.
//   - Hardware : on a perdu access (mot de passe inconnu). Suppose un access
//     physique, et coupe le service.
type ResetProcedure struct {
	Driver string `json:"driver"`
	Vendor string `json:"vendor"`
	// Family est le label lisible, Aliases alimente la recherche (modeles,
	// gammes, noms d'OS).
	Family  string   `json:"family"`
	Aliases []string `json:"aliases"`

	Software []string `json:"software"`
	Hardware []string `json:"hardware"`

	// Erases dit ce que la procedure efface actually — c'est souvent there que
	// se cachent les mauvaises surprises (VLAN database, licences, certificats).
	Erases string `json:"erases"`
	// Caveats : pieges connus, a lire avant d'agir.
	Caveats []string `json:"caveats"`
	// Verified distingue ce qui est stable dans une famille de ce qui varie
	// d'un model a l'autre et doit be confirmed sur la fiche produit.
	Verified bool `json:"verified"`
}

// resetProcedures : familles couvertes par les drivers reconnus.
//
// Ces procedures sont celles des familles, pas de chaque reference. There where le
// geste hardware varie selon le model (emplacement du bouton, duree d'appui),
// Verified est faux et l'UI invite a confirmer sur la documentation du model —
// se tromper de procedure de reset, c'est au mieux perdre du temps, au pire
// perdre access a the device.
var resetProcedures = []ResetProcedure{
	{
		Driver: "cisco-ios", Vendor: "Cisco", Family: "Catalyst / IOS, IOS-XE",
		Aliases: []string{"catalyst", "ios", "ios-xe", "ws-c2960", "c9200", "c9300", "c3750", "c3850"},
		Software: []string{
			"enable",
			"write erase",
			"delete flash:vlan.dat      (la base VLAN ne part PAS avec write erase)",
			"reload   — repondre « no » a la sauvegarde de la configuration",
		},
		Hardware: []string{
			"Couper l'alimentation du switch.",
			"Maintenir le bouton MODE enfonce et remettre sous tension.",
			"Relacher quand la LED SYST clignote en orange puis passe au vert (10–15 s) : on arrive a l'invite « switch: ».",
			"switch: flash_init",
			"switch: del flash:config.text   (et del flash:vlan.dat si besoin)",
			"switch: boot",
		},
		Erases:   "configuration de demarrage ; la base VLAN (vlan.dat) et les images IOS restent, sauf suppression explicite",
		Caveats:  []string{"Sur IOS-XE recent, preferer « erase startup-config » puis « reload » ; le bouton MODE n'ouvre pas d'invite bootloader sur tous les modeles."},
		Verified: true,
	},
	{
		Driver: "cisco-nxos", Vendor: "Cisco", Family: "Nexus / NX-OS",
		Aliases: []string{"nexus", "nx-os", "n3k", "n5k", "n9k"},
		Software: []string{
			"write erase          (efface la configuration de demarrage)",
			"write erase boot     (efface aussi les variables de boot — attention, l'image doit be preciseee ensuite)",
			"reload",
		},
		Hardware: []string{
			"Interrompre le demarrage pour entrer dans le loader (Ctrl-] puis mise sous tension, selon la plateforme).",
			"loader> boot <image nx-os>  puis appliquer la recuperation de mot de passe documentee pour le model.",
		},
		Erases:   "configuration de demarrage ; « write erase boot » touche aussi les variables de demarrage",
		Caveats:  []string{"« write erase boot » sans image de secours identifiee peut rendre the device non demarrable."},
		Verified: false,
	},
	{
		Driver: "aruba-cx", Vendor: "Aruba (HPE)", Family: "ArubaOS-CX (6000/6100/6300/8320…)",
		Aliases: []string{"aos-cx", "arubaos-cx", "6100", "6300", "8320", "jl679", "jl667"},
		Software: []string{
			"erase startup-config           (configuration seule)",
			"erase all zeroize              (factory reset complete, y compris certificats et keys)",
			"boot system                    (reboot)",
		},
		Hardware: []string{
			"Bouton de reinitialisation en front panel (souvent encastre, a actionner avec un trombone).",
			"Appui court = reboot ; appui long = retour usine — la duree depend du model, la verify avant.",
		},
		Erases:   "configuration ; « zeroize » detruit en plus les certificats, keys SSH et secrets stockes",
		Caveats:  []string{"« erase all zeroize » est irreversible et invalide les certificats already deployes.", "La duree d'appui du bouton varie selon la reference."},
		Verified: false,
	},
	{
		Driver: "hpe-procurve", Vendor: "HPE", Family: "ProCurve / Aruba 2530, 2930",
		Aliases: []string{"procurve", "2530", "2920", "2930", "j9773", "j9776"},
		Software: []string{
			"erase startup-config",
			"reload",
		},
		Hardware: []string{
			"Maintenir simultanement les boutons Reset et Clear.",
			"Relacher Reset en gardant Clear enfonce.",
			"Relacher Clear quand la LED « Self Test » clignote (environ 10 s) : la configuration d'usine est restauree.",
		},
		Erases:   "configuration de demarrage et mots de passe locaux",
		Caveats:  []string{"Clear seul, appuye brievement, n'efface que les mots de passe — utile pour reprendre la main sans perdre la configuration."},
		Verified: true,
	},
	{
		Driver: "juniper-junos", Vendor: "Juniper", Family: "JunOS (EX, QFX)",
		Aliases: []string{"junos", "ex2200", "ex2300", "ex3400", "qfx"},
		Software: []string{
			"configure",
			"load factory-default",
			"set system root-authentication plain-text-password    (obligatoire : JunOS refuse de valider sans mot de passe root)",
			"commit and-quit",
			"— ou, pour tout effacer y compris les journaux : request system zeroize",
		},
		Hardware: []string{
			"Interrompre le demarrage (Ctrl-C dans le loader) via la console serial.",
			"Demarrer en mode mono-utilisateur, puis « recovery » pour redefinir le mot de passe root.",
		},
		Erases:   "configuration ; « zeroize » efface aussi les keys, journaux et data utilisateur",
		Caveats:  []string{"« load factory-default » ne se valide pas sans mot de passe root defini dans la same transaction."},
		Verified: true,
	},
	{
		Driver: "mikrotik-routeros", Vendor: "MikroTik", Family: "RouterOS",
		Aliases: []string{"routeros", "crs", "css", "hex", "rb"},
		Software: []string{
			"/system reset-configuration",
			"/system reset-configuration no-defaults=yes    (repartir d'une configuration vraiment vide)",
			"/system reset-configuration keep-users=yes      (conserver les comptes)",
		},
		Hardware: []string{
			"Hors tension, maintenir le bouton Reset (ou pontage du jumper RES selon le model).",
			"Mettre sous tension en gardant l'appui : la LED ACT se met a clignoter (≈ 5 s) → reinitialisation de la configuration.",
			"Continuer l'appui jusqu'a extinction de la LED (≈ 10 s) → mode Netinstall (reinstallation complete par le network).",
		},
		Erases:   "configuration ; le mode Netinstall reinstalle RouterOS entirely",
		Caveats:  []string{"Sans « no-defaults=yes », RouterOS reapplique sa configuration par default, qui inclut un DHCP et un pare-feu — surprise frequente en production."},
		Verified: true,
	},
	{
		Driver: "ubiquiti", Vendor: "Ubiquiti", Family: "UniFi / EdgeSwitch",
		Aliases: []string{"unifi", "usw", "edgeswitch", "edgemax", "edgeos"},
		Software: []string{
			"UniFi : depuis le controleur, « Forget this device » — the device repart en attente d'adoption.",
			"EdgeSwitch : erase startup-config puis reload.",
		},
		Hardware: []string{
			"Maintenir le bouton Reset environ 10 s, device sous tension.",
			"Relacher quand les LED clignotent : retour usine et attente d'adoption.",
		},
		Erases:   "configuration et association au controleur",
		Caveats:  []string{"Un UniFi reinitialise attend une nouvelle adoption : prevoir access au controleur avant de reinitialiser a distance."},
		Verified: true,
	},
	{
		Driver: "extreme-exos", Vendor: "Extreme", Family: "ExtremeXOS",
		Aliases:  []string{"exos", "summit", "x440", "x460"},
		Software: []string{"unconfigure switch all    (redemarre sur une configuration vierge)"},
		Hardware: []string{"Par la console serial : interrompre le demarrage et selectionner la configuration d'usine dans le menu de boot."},
		Erases:   "toute la configuration, y compris les VLAN et les comptes",
		Caveats:  []string{"« unconfigure switch » sans « all » conserve une partie des reglages."},
		Verified: true,
	},
	{
		Driver: "huawei-vrp", Vendor: "Huawei", Family: "VRP (S-series)",
		Aliases: []string{"vrp", "s5700", "s5720", "s6720", "huawei"},
		Software: []string{
			"reset saved-configuration",
			"reboot        (repondre « n » a la sauvegarde)",
		},
		Hardware: []string{"Par la console serial : interrompre le demarrage (Ctrl-B), mot de passe BootROM par default, puis « Clear password » / « Reset configuration » dans le menu."},
		Erases:   "fichier de configuration sauvegarde",
		Caveats:  []string{"Le mot de passe BootROM par default est souvent changed par l'integrateur ; sans lui, le menu est inaccessible."},
		Verified: true,
	},
	{
		Driver: "h3c-comware", Vendor: "H3C / HPE Comware", Family: "Comware 5/7",
		Aliases:  []string{"comware", "h3c", "5130", "5820", "hpe comware"},
		Software: []string{"reset saved-configuration", "reboot"},
		Hardware: []string{"Console serial : interrompre le demarrage (Ctrl-B) et choisir « Skip current system configuration » dans le menu BootWare."},
		Erases:   "configuration sauvegardee",
		Caveats:  []string{"« Skip configuration » ne fait qu'ignorer la configuration au demarrage suivant : elle est toujours present en flash."},
		Verified: true,
	},
	{
		Driver: "dell-os10", Vendor: "Dell", Family: "OS10 / PowerConnect",
		Aliases: []string{"os10", "powerconnect", "n1500", "n2000", "s4048"},
		Software: []string{
			"OS10 : delete startup-configuration puis reload",
			"PowerConnect : clear config puis copy running-config startup-config",
		},
		Hardware: []string{"Console serial : interrompre le demarrage et utiliser le menu de recuperation de la plateforme."},
		Erases:   "configuration de demarrage",
		Caveats:  []string{"OS10 et PowerConnect sont deux CLI differentes : verify laquelle tourne avant d'appliquer une commande."},
		Verified: false,
	},
	{
		Driver: "fortiswitch", Vendor: "Fortinet", Family: "FortiSwitch",
		Aliases:  []string{"fortiswitch", "fortilink", "fs-108", "fs-224"},
		Software: []string{"execute factoryreset          (ou « execute factoryreset2 » pour conserver l'image)"},
		Hardware: []string{"Console serial : interrompre le demarrage et choisir la configuration d'usine dans le menu du BIOS FortiSwitch."},
		Erases:   "configuration ; the device peut repasser en attente d'autorisation FortiLink",
		Caveats:  []string{"Un FortiSwitch managed en FortiLink doit be re-autorise sur le FortiGate after reinitialisation."},
		Verified: false,
	},
	{
		Driver: "zyxel", Vendor: "Zyxel", Family: "GS / XGS (ZyNOS)",
		Aliases:  []string{"zyxel", "zynos", "gs1900", "gs2210", "xgs"},
		Software: []string{"erase running-config puis write memory, ou « Restore Default » dans l'interface web."},
		Hardware: []string{"Maintenir le bouton RESET environ 5 s, device sous tension."},
		Erases:   "configuration",
		Caveats:  []string{"Les gammes Zyxel different nettement entre elles (GS1900 web-only, GS2210 CLI) : verify la commande sur la fiche du model."},
		Verified: false,
	},
	{
		Driver: "netgear", Vendor: "Netgear", Family: "GS / M4300",
		Aliases:  []string{"netgear", "gs308", "gs724", "m4300", "m4250"},
		Software: []string{"clear config puis save, ou « Factory Default » dans l'interface web."},
		Hardware: []string{"Maintenir le bouton Factory Defaults (souvent encastre) environ 10 s jusqu'au reboot."},
		Erases:   "configuration et mot de passe administrateur",
		Caveats:  []string{"Sur les modeles « Plus/Smart » sans CLI, seule la voie web ou le bouton existent."},
		Verified: false,
	},
	{
		Driver: "tplink", Vendor: "TP-Link", Family: "JetStream / Easy Smart",
		Aliases:  []string{"tp-link", "tplink", "jetstream", "t1600g", "t2600g", "tl-sg"},
		Software: []string{"reset (mode privilegie) puis reboot, ou « System Tools → Reset » dans l'interface web."},
		Hardware: []string{"Maintenir le bouton Reset 5 a 10 s, device sous tension, jusqu'au clignotement des LED."},
		Erases:   "configuration, y compris l'adresse IP de management",
		Caveats:  []string{"Apres reinitialisation, the device reprend son IP d'usine : prevoir access au bon subnet."},
		Verified: false,
	},
	{
		Driver: "dlink", Vendor: "D-Link", Family: "DGS / DES",
		Aliases:  []string{"d-link", "dlink", "dgs-1210", "dgs-3120", "des-"},
		Software: []string{"reset system  (efface tout et redemarre), ou « Tools → Reset » dans l'interface web."},
		Hardware: []string{"Maintenir le bouton Reset 10 s environ, device sous tension."},
		Erases:   "configuration et compte administrateur",
		Caveats:  []string{"« reset config » et « reset system » n'ont pas la same portee selon la serial."},
		Verified: false,
	},
	{
		Driver: "cumulus-sonic", Vendor: "Linux network", Family: "Cumulus Linux / SONiC",
		Aliases: []string{"cumulus", "sonic", "onie", "white box"},
		Software: []string{
			"Cumulus : net del all puis net commit (annule la configuration network).",
			"SONiC : sudo config erase puis reboot, ou restauration d'un config_db.json vierge.",
		},
		Hardware: []string{"Retour vraiment usine : reboot sur ONIE (« ONIE: Install OS » dans GRUB) et reinstaller l'image."},
		Erases:   "configuration network ; la reinstallation ONIE efface le systeme entier",
		Caveats:  []string{"Ce sont des systemes Linux : la « configuration d'usine » depend de l'image installee par l'integrateur."},
		Verified: true,
	},
}

// genericReset est renvoye pour un device non identifie : on ne pretend pas
// connaitre une procedure qu'on ignore.
var genericReset = ResetProcedure{
	Driver: "generic", Vendor: "", Family: "Equipement non identifie",
	Software: []string{
		"Chercher dans l'interface d'administration une ingress « Restore defaults » / « Factory reset ».",
		"En CLI, les formes les plus repandues sont « erase startup-config », « write erase », « reset saved-configuration ».",
	},
	Hardware: []string{
		"Un bouton Reset encastre, maintenu 5 a 15 s device sous tension, est la voie la plus courante.",
		"A default, la console serial donne access au menu de demarrage, qui propose presque toujours la configuration d'usine.",
	},
	Erases:   "a verify sur la documentation du model",
	Caveats:  []string{"Aucune procedure certifiee pour ce model : identifier the device (ou consulter sa fiche constructeur) avant d'agir."},
	Verified: false,
}

// SearchResetProcedures cherche par constructeur, famille, driver ou model.
// Une request vide renvoie tout le catalogue, trie comme il est declared.
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
	// Chaque mot de la request doit apparaitre : « cisco 2960 » ne doit pas
	// remonter tous les Cisco.
	var word string
	for _, word = range strings.Fields(needle) {
		if !strings.Contains(hay, word) {
			return false
		}
	}
	return true
}

// ResetProcedureFor renvoie la procedure d'un driver donne.
func ResetProcedureFor(driver string) ResetProcedure {
	var p ResetProcedure
	for _, p = range resetProcedures {
		if p.Driver == driver {
			return p
		}
	}
	return genericReset
}

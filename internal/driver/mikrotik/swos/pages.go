package swos

import (
	"fmt"
	"strconv"
	"strings"
)

// La carte des pages de SwOS, et la SIGNIFICATION de leurs champs.
//
// Il n'existe aucune documentation de ces pages. Tout ce qui suit est observed dans
// le JavaScript que the device sert lui-same (une page `index.html` de 44 807
// octets sur un CSS326-24G-2S+ en SwOS 2.17, script integralement en ligne), qui
// est la SEULE reference de la plateforme : il nomme chaque champ, dit son type,
// son encodage, ses bornes, et montre par quelle request l'interface l'written.
// C'est la same method que celle du connecteur Instant On sur sa propre gamme.
//
// Chaque constante ci-dessous porte l'endroit hence elle vient. Une valeur sans
// source dans ce fichier serait une valeur invented, et une capability invented
// finit par abimer un device.

// Les pages de data. Toutes servies sous authentification HTTP Digest, en
// `application/x-javascript`.
//
// TRAP MAJEUR de cette plateforme : un chemin INCONNU ne rend pas 404. Il rend
// l'interface web complete, en HTTP 200, `text/html`. Constate sur
// 10.99.0.36 (CSS326-24G-2S+, SwOS 2.17) : /vlans.b, /poe.b, /igmp.b, /dhcp.b,
// /cfg.b, /mact.b, /leds.b, /fan.b repondent tous les huit par les 44 807 octets
// de l'interface. Conclure a l'existence d'une page sur un HTTP 200 est donc faux ;
// c'est l'analyseur qui tranche, en refusant ce qui n'est pas une page SwOS.
const (
	// PageSys : identity, adressage de management, RSTP global, IGMP, snooping
	// DHCP, sante. La page la plus dense de la plateforme.
	PageSys = "/sys.b"
	// PageLink : etat et configuration des ports (activation, nom, lien, rate,
	// duplex, control de flux), et la GEOMETRY du model (`prt`, `sfp`, `sfpo`).
	PageLink = "/link.b"
	// PageFwd : commutation — isolation de ports, verrouillage, mirroring, limite
	// de storm, rate ingress, et le VLAN PAR PORT (`vlan`, `vlni`, `dvid`).
	PageFwd = "/fwd.b"
	// PageVLAN : la TABLE des VLAN, une ligne par VLAN.
	PageVLAN = "/vlan.b"
	// PageHost : table des hotes STATIQUES (configuree).
	PageHost = "/host.b"
	// PageDynHost : table des hotes APPRIS. Lecture seule (le « ! » du nom marque
	// les pages que l'interface does not write jamais).
	PageDynHost = "/!dhost.b"
	// PageSFP : diagnostic optique des cages SFP (SFF-8472).
	PageSFP = "/sfp.b"
	// PageSNMP : agent SNMP — activation, community, contact, localisation.
	PageSNMP = "/snmp.b"
	// PageACL : la table des rules ACL.
	PageACL = "/acl.b"
	// PageStats : compteurs et rates par port.
	PageStats = "/stats.b"
	// PageRSTP : etat RSTP par port.
	PageRSTP = "/rstp.b"
	// PageLACP : aggregation par port.
	PageLACP = "/lacp.b"
	// PageIGMPGroups : groupes IGMP appris. Lecture seule.
	PageIGMPGroups = "/!igmp.b"
	// PagePassword : le mot de passe d'administration. Lue, elle rend
	// `{pwd:''}` — jamais le secret. Ecrite, elle le change (cf. admin.go).
	PagePassword = "/!pwd.b"
	// PageBackup : la configuration ENTIRE, en texte, en une seule request.
	// `application/octet-stream`, mais c'est bien du texte.
	PageBackup = "/backup.swb"
)

// Les points d'ACTION de SwOS. Aucun n'a de corps utile : l'interface y poste le
// caractere `*`.
//
// OBSERVED, et a savoir avant d'explorer cette plateforme : ces chemins IGNORENT
// la method GET. Un `GET /reset` et un `GET /reboot` rendent tous deux HTTP 200
// avec un corps VIDE et ne font RIEN — verified sur 10.99.0.36, dont l'uptime a
// continue de croitre (149 281 s puis 149 801 s) et dont la table de VLAN was
// inchangee juste after. C'est ce qui rend l'exploration en lecture seule
// actually sans risque sur cette plateforme, et ce n'est pas une chose qu'on
// devrait avoir a decouvrir en la tentant.
const (
	// ActionReboot redemarre. La configuration de SwOS etant persistante d'office
	// (cf. Capabilities.Commit), un reboot n'annule RIEN.
	ActionReboot = "/reboot"
	// ActionReset remet la configuration d'usine.
	ActionReset = "/reset"
	// ActionLogout clot la session de l'interface.
	ActionLogout = "/logout"
)

// Le jeu de champs INSCRIPTIBLE de chaque page, et pourquoi cette liste existe.
//
// L'interface de the device n'envoie pas de delta : son bouton « Apply All »
// recompose l'objet ENTIER de la page a partir de tous ses champs modifiables
// (`Sa` dans son JavaScript, qui parcourt les definitions et saute celles marquees
// en lecture seule), puis le poste tel quel. Ecrire sur SwOS est donc forcement un
// LIRE-MODIFIER-REWRITE, et une page renvoyee truncated laisse the device libre
// de remettre a zero ce qu'on n'a pas dit. Sur /fwd.b, cela voudrait dire perdre
// l'isolation de ports, les limites de storm et les rates ingress en posant un
// VLAN.
//
// Hence vient la liste : de la SAUVEGARDE DE CONFIGURATION de the device,
// `GET /backup.swb`, qui enumerates page par page exactement les champs que
// the device stocke — i.e. les champs inscriptibles, et rien d'autre.
// Capture reelle jointe au paquet (testdata/backup.swb, CSS326-24G-2S+, SwOS 2.17).
// C'est une source bien meilleure qu'une lecture de page vivante, qui melange
// l'inscriptible et la telemetry.
//
// Seules les pages que ce connecteur WRITES sont declarees ici. Declare les autres
// inviterait a s'en servir sans avoir verified leur effet ; leur contenu se lit dans
// testdata/backup.swb, qui est le observed.
var writableFields = map[string][]string{
	// /snmp.b
	PageSNMP: {FieldSNMPEnabled, FieldSNMPCommunity, FieldSNMPContact, FieldSNMPLocation},
	// /sys.b — order and keys from testdata/backup.swb (CSS326-24G-2S+, SwOS 2.17).
	// Omitting any of these on POST would let the device zero them; `allp`/`alla`/
	// `allm`/`avln` are the fields that mute administration when mishandled.
	PageSys: {
		FieldIdentity, "wdt", "dsc", "pdsc", "ivl",
		FieldAllowFrom, FieldAllowMask, FieldAllowVLAN, FieldAllowPorts,
		"mgmt", "prio", "cost", "frmc", "poe", "igmp", "igmq", "igfl", "igve",
		FieldIP, "dtrp", "ainf", FieldAddrMode,
	},
}

// WritableFields rend le jeu de champs inscriptible d'une page, et dit s'il est
// connu. Une page dont on ne connait pas le jeu ne s'written pas : c'est la rule
// « une capability inconnue est supposee absente » appliquee a writing.
func WritableFields(page string) ([]string, bool) {
	var f, ok = writableFields[page]
	if !ok {
		return nil, false
	}
	return append([]string{}, f...), true
}

// FwdWritableFields rend le jeu inscriptible de /fwd.b pour un model a `ports`
// ports.
//
// Cette page est la seule dont le jeu de champs DEPEND de la taille du model :
// elle porte un masque d'isolation par port SOURCE, `fp1` a `fp<ports>` — 26 sur un
// CSS326-24G-2S+. Recopier tel quel le jeu observed sur ce model vers un model plus
// petit enverrait des masques pour des ports qui n'existent pas ; vers un plus
// grand, il en oublierait, et the device serait libre d'y remettre l'isolation a
// zero. La liste se recompose donc a la bonne taille, l'ordre restant celui de la
// sauvegarde.
func FwdWritableFields(ports int) []string {
	var out []string
	var i int
	for i = 1; i <= ports; i++ {
		out = append(out, "fp"+strconv.Itoa(i))
	}
	return append(out, "lck", "lckf", "imr", "omr", "mrto",
		FieldVLANMode, FieldVLANReceive, FieldDefaultVLAN, FieldForceVLAN,
		"srt", "suni", "fmc", "ir")
}

// vlanRowFields est le jeu de champs d'UNE ligne de /vlan.b, dans l'ordre de la
// sauvegarde. La table s'written ENTIRE : il n'y a pas d'ajout ni de suppression
// ligne a ligne sur cette plateforme (l'interface reconstruit la liste et la poste).
var vlanRowFields = []string{
	FieldVLANName, FieldVLANMembers, FieldVLANID, "piso", "lrn", "mrr", "igmp",
}

// --- Champs de /sys.b ---

// Identite et firmware. Tous en lecture seule.
const (
	// FieldBoard : la reference du model, string hex-encoded
	// (`'4353533332362d3234472d32532b'` = « CSS326-24G-2S+ »).
	FieldBoard = "brd"
	// FieldSerial : le serial number.
	FieldSerial = "sid"
	// FieldVersion : la version de SwOS (`'322e3137'` = « 2.17 »).
	FieldVersion = "ver"
	// FieldBuild : l'horodatage de compilation du firmware, en secondes Unix.
	FieldBuild = "bld"
	// FieldRevision : la revision hardware, souvent vide.
	FieldRevision = "rev"
	// FieldMarket : le nom commercial de la carte, souvent vide.
	FieldMarket = "mrkt"
	// FieldIdentity : le nom systeme, MODIFIABLE (« Identity » dans l'interface).
	FieldIdentity = "id"
	// FieldMAC : l'adresse MAC de the device, 12 chiffres hex SANS separateur et
	// SANS inversion (`'b869f45f14a8'` = b8:69:f4:5f:14:a8).
	FieldMAC = "mac"
	// FieldUptime : le temps de fonctionnement, en HUNDREDTHS de seconde
	// (`scale:100` dans l'interface).
	FieldUptime = "upt"
	// FieldTemp : la temperature, en degrees Celsius entiers, sans facteur
	// d'echelle. Relevee a 0x44 puis 0x45 (68 puis 69 °C) a quelques minutes
	// d'intervalle sur 10.99.0.36 : c'est bien un capteur vivant.
	FieldTemp = "temp"
)

// Adressage de management. C'est le groupe le plus dangereux de la plateforme : se
// tromper sur l'un de ces cinq champs rend the device injoignable, et ce model
// n'a PAS de serial port.
const (
	// FieldIP : l'adresse statique. Voir DecodeIPv4 pour l'ordre des octets.
	FieldIP = "ip"
	// FieldCurrentIP : l'adresse EN COURS d'utilisation, en lecture seule. Differe
	// de `ip` quand the device est en DHCP.
	FieldCurrentIP = "cip"
	// FieldAddrMode : le mode d'adressage — 0 « DHCP with fallback », 1 « static »,
	// 2 « DHCP only » (les trois options du selecteur de l'interface, dans cet
	// ordre).
	FieldAddrMode = "iptp"
	// FieldAllowVLAN : « Allow From VLAN » — le VLAN depuis lequel l'administration
	// est accepted. 0 = tous. C'est le VLAN de management de the device.
	FieldAllowVLAN = "avln"
	// FieldAllowFrom / FieldAllowMask : « Allow From » — le sous-network autorise a
	// administrer. 0/0 = tous.
	FieldAllowFrom = "alla"
	FieldAllowMask = "allm"
	// FieldAllowPorts : « Allow From Ports » — masque des ports par lesquels
	// l'administration est accepted.
	FieldAllowPorts = "allp"
)

// Les modes d'adressage de FieldAddrMode.
const (
	AddrDHCPFallback uint32 = 0
	AddrStatic       uint32 = 1
	AddrDHCPOnly     uint32 = 2
)

// --- Champs de /link.b ---

// Geometrie du model et etat des ports.
const (
	// FieldPortCount : le nombre TOTAL de ports, cages SFP comprises (0x1a = 26 sur
	// un CSS326-24G-2S+).
	FieldPortCount = "prt"
	// FieldSFPCount : le nombre de cages SFP (0x02).
	FieldSFPCount = "sfp"
	// FieldSFPOffset : l'index de la FIRST cage SFP (0x18 = 24, donc les ports
	// d'index 24 et 25 — numerotes 25 et 26 — sont les cages).
	//
	// Champ FACULTATIF : quand il manque, l'interface place les cages a la fin
	// (`t = void 0==d.sfpo ? da : d.sfpo`, avec da = prt - sfp). Reproduire ce
	// repli est necessary, un model plus ancien pouvant l'omettre.
	FieldSFPOffset = "sfpo"
	// FieldEnabled : masque des ports administrativement enabled.
	FieldEnabled = "en"
	// FieldLink : masque des ports qui portent un LIEN.
	FieldLink = "lnk"
	// FieldDuplex : masque des ports en duplex integral.
	FieldDuplex = "dpx"
	// FieldSpeed : le rate NEGOCIE, un index par port dans SpeedLabels.
	//
	// N'a de sens que sur un port qui porte un lien. Sur 10.99.0.36, les 25 ports
	// sans lien portent l'index 7 — hors des bornes de la table — et le seul port
	// avec lien (index 25, la cage SFP2) porte 3, i.e. 10G, ce que confirme
	// le transceiver insere (`SFP-10G-BX`). Rendre un rate pour un port off
	// serait donc inventer une valeur.
	FieldSpeed = "spd"
	// FieldPortName : le nom de chaque port, string hex-encoded. Par default
	// « Port1 »… « Port24 », « SFP1 », « SFP2 ».
	FieldPortName = "nm"
)

// SpeedLabels traduit l'index de FieldSpeed. L'ordre est celui de la table de
// l'interface : `"10M 100M 1G 10G 5G 2.5G 40G".split(" ")`. Il n'est PAS croissant,
// et le supposer trie donnerait un rate faux.
var SpeedLabels = []string{"10M", "100M", "1G", "10G", "5G", "2.5G", "40G"}

// SpeedLabel traduit un index de rate. Un index hors table n'est pas « 10M » :
// c'est une absence, et elle se dit.
func SpeedLabel(i uint32) (string, bool) {
	if i >= uint32(len(SpeedLabels)) {
		return "", false
	}
	return SpeedLabels[i], true
}

// --- Champs de /fwd.b : le VLAN par port ---

const (
	// FieldVLANMode : le mode VLAN de chaque port — 0 « disabled », 1 « optional »,
	// 2 « enabled », 3 « strict ».
	FieldVLANMode = "vlan"
	// FieldVLANReceive : ce que le port accepte a ingress — 0 « any »,
	// 1 « only tagged », 2 « only untagged ».
	FieldVLANReceive = "vlni"
	// FieldDefaultVLAN : le « Default VLAN ID » du port, i.e. son PVID.
	FieldDefaultVLAN = "dvid"
	// FieldForceVLAN : masque des ports en « Force VLAN ID ».
	FieldForceVLAN = "fvid"
)

// Les modes VLAN de FieldVLANMode.
const (
	VLANModeDisabled uint32 = 0
	VLANModeOptional uint32 = 1
	VLANModeEnabled  uint32 = 2
	VLANModeStrict   uint32 = 3
)

// VLANModeLabel nomme un mode VLAN avec les mots de l'interface de the device.
func VLANModeLabel(m uint32) (string, bool) {
	switch m {
	case VLANModeDisabled:
		return "disabled", true
	case VLANModeOptional:
		return "optional", true
	case VLANModeEnabled:
		return "enabled", true
	case VLANModeStrict:
		return "strict", true
	}
	return "", false
}

// --- Champs de /vlan.b : la table des VLAN ---

const (
	// FieldVLANID : l'identifiant du VLAN.
	FieldVLANID = "vid"
	// FieldVLANName : son nom, string hex-encoded, 16 octets au plus.
	FieldVLANName = "nm"
	// FieldVLANMembers : masque des ports MEMBRES du VLAN.
	FieldVLANMembers = "mbr"
)

// --- Champs de /snmp.b ---

const (
	// FieldSNMPEnabled : l'agent est-il actif.
	FieldSNMPEnabled = "en"
	// FieldSNMPCommunity : la community, string hex-encoded, 64 octets au plus
	// (`ga:64` dans l'interface). UNE seule, et il n'existe aucun champ de version
	// ni d'utilisateur v3 sur toute la plateforme.
	FieldSNMPCommunity = "com"
	// FieldSNMPContact / FieldSNMPLocation : sysContact et sysLocation, 64 octets.
	FieldSNMPContact  = "ci"
	FieldSNMPLocation = "loc"
)

// --- Champs de /sfp.b : diagnostic optique ---

const (
	// FieldSFPVendor, FieldSFPPart, FieldSFPSerial : identity du transceiver,
	// strings hex-encodees rembourrees par des ESPACES (champs SFF-8472 de taille
	// fixe). A rogner a l'affichage, jamais a la lecture.
	FieldSFPVendor = "vnd"
	FieldSFPPart   = "pnr"
	FieldSFPSerial = "ser"
	// FieldSFPTemp : temperature du transceiver, en °C. La valeur 0xffffff80
	// (-128 en complement a deux) est le « pas de module » de l'interface
	// (`u:-128`), pas une temperature.
	FieldSFPTemp = "tmp"
	// FieldSFPVcc : tension d'alimentation, a diviser par 1 000 pour des volts
	// (`scale:1E3`).
	FieldSFPVcc = "vcc"
	// FieldSFPTxBias : courant de polarisation transmit, en mA.
	FieldSFPTxBias = "tbs"
	// FieldSFPTxPower / FieldSFPRxPower : puissances optiques. L'interface les
	// affiche en dBm par `10·log10(v/10000)` (`scale:1E4, ma:1`) — la valeur brute
	// est donc en dixiemes de microwatt.
	FieldSFPTxPower = "tpw"
	FieldSFPRxPower = "rpw"
)

// sfpNoModule est la valeur de FieldSFPTemp quand la cage est vide.
const sfpNoModule uint32 = 0xffffff80

// --- Champs de /stats.b ---

const (
	// FieldRxBytes / FieldTxBytes : octets recus et emis. Les compagnons `rbh` et
	// `tbh` en portent les 32 bits HAUTS.
	FieldRxBytes     = "rb"
	FieldRxBytesHigh = "rbh"
	FieldTxBytes     = "tb"
	FieldTxBytesHigh = "tbh"
	// FieldRxPackets / FieldTxPackets : paquets totaux.
	FieldRxPackets = "rtp"
	FieldTxPackets = "ttp"
	// FieldRxRate / FieldTxRate : les RATES, computed par the device lui-same.
	// A diviser par 100 pour des bits par seconde (`scale:.01`).
	//
	// Ce champ est ce qui permet a ce connecteur de tenir un plancher de collecte
	// large sans perdre la precision du rate : le rate snapshot n'a pas besoin
	// d'be derive de deux lectures de compteur, the device le sert already.
	FieldRxRate = "rrb"
	FieldTxRate = "trb"
	// FieldRxErrors : erreurs de MAC en receive, et FieldRxFCS les erreurs de
	// sequence de control.
	FieldRxErrors = "rte"
	FieldRxFCS    = "rfcs"
)

// Counter64 recompose un compteur 64 bits a partir de ses deux halves.
//
// Les compteurs d'octets de SwOS sont scindes : `rb` porte les 32 bits bas, `rbh`
// les 32 bits hauts. Lire `rb` seul fait repasser le compteur a zero tous les
// 4 Go — sur un lien 10G, toutes les trois secondes et demie, ce qui produirait
// une courbe de trafic entirely fausse.
func Counter64(low, high uint32) uint64 {
	return uint64(high)<<32 | uint64(low)
}

// --- Encodages transverses ---

// DecodeIPv4 traduit une adresse de SwOS en notation pointee.
//
// L'ordre des octets est celui du LITTLE-ENDIAN : le PREMIER octet de la notation
// pointee est l'octet de POIDS FAIBLE. `0x2400630a` se lit donc 10.99.0.36 et non
// 36.0.99.10.
//
// Ce n'est pas une supposition tiree de la valeur observee, c'est le code de
// the device. Son decoder (`L.C`) fait `for(d=0;4>d;++d){ b += a&255; a>>=8 }`,
// et son encodeur (`L.B`) fait l'inverse, `e = e>>8&0xFFFFFF | f<<24` sur les
// octets pris dans l'ordre de lecture — rejouer cet encodeur sur 10.99.0.36 rend
// bien 0x2400630A. Les deux sens concordent, et l'adresse ainsi decodee est celle
// par laquelle the device repond actually.
//
// C'est exactement le genre de detail qui fait afficher une adresse fausse, puis
// write une adresse fausse : l'inverser ferait poser 36.0.99.10 a un device
// qu'on croit envoyer en 10.99.0.36, et il faudrait alors se deplacer.
func DecodeIPv4(v uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", v&0xff, v>>8&0xff, v>>16&0xff, v>>24&0xff)
}

// EncodeIPv4 est l'inverse exact de DecodeIPv4.
func EncodeIPv4(s string) (uint32, bool) {
	var parts = strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 4 {
		return 0, false
	}
	var out uint32
	var i int
	for i = 0; i < 4; i++ {
		var n, err = strconv.Atoi(parts[i])
		if err != nil || n < 0 || n > 255 {
			return 0, false
		}
		out |= uint32(n) << uint(8*i)
	}
	return out, true
}

// DecodeMAC met les deux-points dans une MAC de SwOS. Aucune inversion : ces
// champs sont servis dans l'ordre de lecture.
func DecodeMAC(raw string) (string, bool) {
	if len(raw) != 12 {
		return "", false
	}
	var parts = make([]string, 0, 6)
	var i int
	for i = 0; i < 12; i += 2 {
		var b = raw[i : i+2]
		var _, err = strconv.ParseUint(b, 16, 8)
		if err != nil {
			return "", false
		}
		parts = append(parts, strings.ToLower(b))
	}
	return strings.Join(parts, ":"), true
}

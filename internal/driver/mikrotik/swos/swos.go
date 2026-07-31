// Package swos pilote les switchs MikroTik sous SwOS — le systeme des CSS, a ne
// pas confondre avec RouterOS.
//
// SwOS est WEB-ONLY, et plus etroitement que ne l'est l'Instant On : ni SSH, ni
// Telnet, ni HTTPS, ni serial port. Sur l'reference device, un seul port
// TCP repond, le 80. Toute l'administration passe par des pages `*.b` servies sous
// authentification HTTP Digest, qui rendent des objets a la syntaxe JavaScript aux
// valeurs hexadecimales (cf. parse.go).
//
// D'WHERE VIENNENT LES CAPABILITIES DE CE FICHIER
//
// Il n'existe aucun guide constructeur qui les donne. MikroTik documente RouterOS
// abondamment et SwOS a peine ; la reference de la plateforme est le JAVASCRIPT
// QUE THE DEVICE SERT LUI-SAME — une page `index.html` de 44 807 octets, script
// integralement en ligne, qui definit chaque onglet, chaque champ, son type, son
// encodage, ses bornes, et la request par laquelle l'interface l'written. C'est la
// method qu'a employed le connecteur Instant On sur sa propre gamme, et c'est ici
// la seule source qui existe.
//
// Trois observed independants la corroborent, tous joints au paquet (testdata/) :
//
//   - les pages de data vivantes de the device — /sys.b, /link.b, /fwd.b,
//     /vlan.b, /rstp.b, /lacp.b, /sfp.b, /snmp.b, /acl.b, /host.b, /!dhost.b,
//     /stats.b ;
//   - sa SAUVEGARDE DE CONFIGURATION, /backup.swb, qui enumerates page par page les
//     champs qu'il stocke actually — donc l'inventaire exact de ce qui est
//     inscriptible ;
//   - le challenge de son authentification, lisible SANS identifiant, qui porte la
//     reference du model.
//
// Materiel de reference : **MikroTik CSS326-24G-2S+, SwOS 2.17** (serial
// 945809C232D5, MAC b8:69:f4:5f:14:a8), atteint a travers le tunnel d'un proxy.
// Chaque valeur chiffree ci-dessous porte celui de ces observed qui l'establishes, ou
// dit explicitement qu'elle est deduced.
//
// # CE QUE SwOS NE SAIT PAS FAIRE EST UN LIVRABLE
//
// SwOS est volontairement pauvre face a RouterOS, et la half de la valeur de ce
// connecteur est de le DIRE : pas de routage, pas d'horloge, pas de syslog, pas de
// trap, pas de LLDP, pas de compte named, pas de rollback. Ces `false` et ces
// motifs d'indisponibilite ne sont pas des trous a combler dans une version
// suivante : ce sont des faits de la plateforme, et les afficher est ce qui
// empechera meshcraft de promettre a un exploitant ce qu'un CSS ne tiendra pas.
package swos

import (
	"regexp"
	"strings"
	"time"

	"meshcraft/internal/driver"
)

func init() { driver.Register(&Driver{}) }

// Driver couvre la gamme CSS sous SwOS.
type Driver struct{}

func (d *Driver) Info() driver.Info {
	return driver.Info{
		Name:   "mikrotik-swos",
		Vendor: "MikroTik",
		Family: "SwOS",
		// WEB pour l'ADMINISTRATION, SNMP pour la COLLECTE — et les deux ne sont pas
		// interchangeables : tout ce que ce connecteur WRITES passe par les pages `*.b`,
		// et l'agent SNMP est en lecture seule.
		//
		// « snmp » a been ajoute sur un OBSERVED, pas sur une deduction. Le commentaire
		// previous disait « rien dans ce que ce connecteur a pu establish ne dit a quelle
		// version du protocole il repond, et l'outil d'exploration disponible ne parle
		// pas UDP » : le walk du proxy, lui, parle UDP, et l'agent a repondu en v2c
		// (cf. SNMPVersions pour les valeurs).
		Transport: []string{"web", "snmp"},
		Doc: "Releve sur hardware (MikroTik CSS326-24G-2S+, SwOS 2.17) : pages *.b, " +
			"sauvegarde de configuration /backup.swb, et le JavaScript de l'interface " +
			"embarquee, qui est la seule documentation de la plateforme",
	}
}

// Motifs de reconnaissance.
//
// SwOS se nomme trois fois, et c'est une plateforme confortable a reconnaitre :
//
//   - le TITRE de sa page web est « MikroTik SwOS » (observed sur the device de
//     reference) ;
//   - le REALM de son challenge Digest porte la reference exacte du model
//     (`realm="CSS326-24G-2S+"`), et il est lisible SANS identifiant ;
//   - un sysDescr SNMP, quand l'agent repond, porte « SwOS ».
var (
	reSwOS = regexp.MustCompile(`(?i)SwOS`)
	// reRealmModel accroche une reference MikroTik commutee there where elle apparait
	// dans un corps de page ou un en-tete recopie dans l'Evidence.
	reRealmModel = regexp.MustCompile(`(?i)\b(CSS[0-9]{3}|CRS[0-9]{3})[0-9A-Za-z+\-]*`)
	// reRouterOS exclut RouterOS. Les CRS existent dans les deux mondes — le same
	// chassis demarre sous SwOS ou sous RouterOS — et leurs capabilities n'ont rien
	// a voir. Prendre un CRS sous RouterOS pour un SwOS ferait refuser la half de
	// ce qu'il sait faire, et write par le mauvais chemin.
	reRouterOS = regexp.MustCompile(`(?i)RouterOS`)
)

// Fingerprint : « cet device est-il pour moi ? »
//
// Le nom de la plateforme est un signe SUR : « SwOS » n'apparait que there. Le realm
// qui porte une reference CSS/CRS en est un autre, independant, et c'est celui
// qu'on obtient sans le moindre identifiant.
//
// L'exclusion de RouterOS passe AVANT tout le reste : un CRS peut demarrer sous
// l'un ou l'autre systeme, et c'est le seul cas de ce projet where deux connecteurs se
// disputent le same chassis. Le titre de la page tranche — c'est le seul indice
// que le systeme EN COURS D'EXECUTION produit lui-same, there where une reference de
// model ne dit rien de ce qui a demarre.
func (d *Driver) Fingerprint(ev driver.Evidence) int {
	var haystack = strings.Join([]string{
		ev.HTTPTitle, ev.HTTPServer, ev.HTTPBody, ev.SysDescr, ev.SSHBanner, ev.ShellOut,
	}, " ")
	if reRouterOS.MatchString(haystack) && !reSwOS.MatchString(ev.HTTPTitle) {
		return 0
	}
	switch {
	case reSwOS.MatchString(haystack) && reRealmModel.MatchString(haystack):
		// La plateforme ET le model : on ne peut pas faire mieux sans se connecter.
		return 98
	case reSwOS.MatchString(haystack):
		return 95
	case reRealmModel.MatchString(haystack):
		// Une reference CSS/CRS sans que « SwOS » n'apparaisse : bon candidat, a
		// confirmer. Un CRS peut be sous RouterOS, et la discovery n'a peut-be
		// pas lu le realm.
		return 55
	}
	return 0
}

// Capabilities de la gamme SwOS, observed sur hardware.
func (d *Driver) Capabilities(model string) driver.Capabilities {
	// Le PoE se deduit de la reference, faute de mieux quand on n'a pas
	// the device sous la main : la convention de nommage de MikroTik marque le
	// PoE par un `P` (« CRS328-24P-4S+ ») there where les modeles sans PoE portent un `G`
	// (« CSS326-24G-2S+ »). Le CSS326 de reference n'en a pas, et son propre
	// firmware le confirme trois fois : `npoe:0x00` et `poe:0x00` dans /sys.b, et
	// AUCUNE page /poe.b — le chemin rend l'interface web, i.e. n'existe pas.
	var poe = ModelHasPoE(model)

	return driver.Capabilities{
		// Bornes du VLAN, observed dans la definition de la table de the device :
		// `{n:"VLAN ID", t:I, id:"vid", min:1, max:4094}`. A ne pas confondre avec
		// le « Default VLAN ID » par port, que la same interface borne a 4095 — c'est
		// la TABLE qui contraint, puisqu'un port ne peut appartenir only un VLAN
		// declared.
		VlanRange: [2]int{1, 4094},
		// 250 entries : la borne que l'interface de the device s'impose a
		// elle-same (`Z:250` sur la table des VLAN, avec le refus « Max entries
		// (250) »). C'est la contrainte qui vaut pour un connecteur qui rejoue cette
		// interface, et c'est la seule que la plateforme chiffre.
		MaxVlans: 250,
		// Ce que la plateforme sait faire, et surtout ce qu'elle ne sait pas.
		//
		// Les keys sont celles du connecteur Instant On, a la lettre : c'est ce qui
		// permet de comparer un CSS et un 1930 dans le same tableau. Chaque `true`
		// nomme le champ de the device qui l'establishes ; chaque `false` est un
		// constat d'ABSENCE dans l'inventaire complet de son interface et de sa
		// sauvegarde de configuration, pas une supposition de prudence.
		Features: map[string]bool{
			// /vlan.b (table VLAN + masque de membres) et /fwd.b (`vlan`, `vlni`).
			"Trunk8021Q": true,
			// /fwd.b `dvid` — « Default VLAN ID », un par port.
			"PVID": true,
			// Aucune notion de VLAN voix : rien dans l'interface, rien dans la
			// sauvegarde. Un intent qui en demanderait un est refused, et c'est mieux
			// que de le poser dans le vide.
			"VoiceVLAN": false,
			// /poe.b et le groupe « PoE Out » de /sys.b, sur les modeles qui en ont :
			// l'interface conditionne son onglet PoE a la presence du hardware.
			"PoE": poe,
			// PAS DE LLDP. C'est l'absence la plus lourde de consequences de toute la
			// plateforme, et elle est ferme : ni 802.1AB, ni LLDP-MED, aucun champ,
			// aucune table de voisinage, dans aucun onglet ni dans la sauvegarde. SwOS
			// n'annonce et n'ecoute que le protocole proprietaire de MikroTik, MNDP
			// (/sys.b `pdsc`, « Mikrotik Discovery Protocol », un masque par port).
			// Les modeles PoE portent bien un champ `lldp`, mais c'est la negociation
			// de puissance LLDP-MED d'un port alimente : elle ne construit aucune
			// table de voisins.
			"LLDP": false,
			// /sys.b `igmp`, `igmq` (querier), `igfl` (fast leave), `igve` (v2/v3),
			// plus la table des groupes appris /!igmp.b.
			"IGMPSnooping": true,
			// /lacp.b — `mode` par port en « passive », « active » ou « static ».
			"LACP": true,
			// /sys.b, groupe « DHCP & PPPoE Snooping » : `dtrp` (ports de confiance)
			// et `ainf` (insertion de l'option d'information, dite option 82).
			"DHCPSnooping": true,
			// Aucun relais DHCP : SwOS ne route pas, donc ne relaie pas. L'insertion
			// d'option 82 ci-dessus n'est pas un relais et ne doit pas se lire ainsi.
			"DHCPRelay": false,
			// Ni inspection ARP, ni protection contre le deni de service : rien de tel
			// dans l'interface. Ce qui en approche le plus est l'ACL, qui filtre mais
			// ne surveille pas.
			"ARPProtection": false,
			"DoSProtection": false,
			// /fwd.b `lck` (« Port Lock ») et `lckf` (« Lock On First »), avec la
			// table des hotes statiques /host.b et son drapeau `drp`.
			"PortSecurity": true,
			// /fwd.b `fp1`…`fp26` — une matrice d'isolation complete, un masque de
			// destinations autorisees par port SOURCE. Bien plus fin que des « ports
			// proteges » ordinaires.
			"ProtectedPorts": true,
			// Ni 802.1X, ni RADIUS : aucun champ, aucune notion d'authentification
			// access au network sur cette plateforme.
			"Dot1X":  false,
			"RADIUS": false,
			// /fwd.b `imr` (miroir en ingress), `omr` (en sortie), `mrto` (port
			// destination), plus `mrr` par VLAN et `mirr` par rule ACL.
			"PortMirroring": true,
			// /fwd.b `ir` — un rate ingress par port.
			"IngressRate": true,
			// Aucun reglage de trame geante nulle part. Le hardware en accepte
			// peut-be par default, mais une capability describes ce qu'on peut
			// CONFIGURER et donc garantir : un intent « activer les trames geantes »
			// n'a ici aucune write a faire, et le declare disponible ferait croire
			// qu'il a been applied.
			"JumboFrames": false,
			// SwOS NE ROUTE PAS. Pas de table de routage, pas de passerelle, pas
			// d'interface IP autre que celle du management. C'est la difference de
			// fond avec RouterOS sur le same chassis, et elle doit se voir avant qu'un
			// exploitant ne batisse un plan dessus.
			"StaticRouting": false,
			// AUCUNE HORLOGE. Pas de NTP, pas de SNTP, pas de fuseau, pas de date
			// nulle part dans l'interface. Consequence en cascade : aucun horodatage
			// n'est disponible sur cet device, ce qui est aussi la raison pour
			// laquelle il n'a pas de syslog (cf. Monitoring.Syslog).
			"SNTP": false,
			// Pas de protection de boucle dediee. RSTP existe, et c'est tout — ce
			// n'est pas la same chose : RSTP suppose un voisin qui parle RSTP.
			"LoopProtection": false,
			// Ni economie d'energie 802.3az, ni RMON.
			"EEE":  false,
			"RMON": false,
			// /fwd.b `srt` (« Storm Rate », en pourcentage), `suni` (« Limit Unknown
			// Unicast ») et `fmc` (« Flood Unknown Multicast »).
			"StormControl": true,
			// PAR PORT, et c'est une force de cette plateforme there where l'Instant On ne
			// sait le faire que globalement : `srt` est un TABLEAU, un pourcentage par
			// port (26 valeurs a 0x64 sur l'reference device).
			"StormControlPort": true,
			// AUCUNE ligne de commande, d'aucune sorte, et aucun serial port sur ce
			// model. Sur l'reference device, un seul port TCP repond : le 80.
			// Ni 22, ni 23, ni 443.
			//
			// Consequence a retenir avant toute write : l'adoption par console
			// serial, l'une des trois portes ingress de la discovery, est IMPOSSIBLE
			// ici. Un device dont on a perdu access se recupere par le bouton de
			// factory reset, donc par un deplacement.
			"SSHMgmt":    false,
			"TelnetMgmt": false,
			"SerialCLI":  false,
			"SerialPort": false,
			// Le seul chemin. En CLAIR : SwOS n'ecoute pas en HTTPS, et la
			// confidentialite repose entirely sur le tunnel du proxy.
			"HTTPMgmt": true,
		},
		// RSTP, et RSTP seulement.
		//
		// L'interface n'offre AUCUN selecteur de protocole : elle expose un
		// interrupteur RSTP par port (/rstp.b `ena`), la priorite et le mode de cost
		// du pont (/sys.b `prio`, `cost`). Le champ `rstp` de /rstp.b, qui affiche
		// « STP » ou « RSTP », est en LECTURE SEULE : c'est le repli automatique
		// lorsqu'un voisin ne parle que 802.1D, pas un mode qu'on choisit. Declare
		// « stp » ferait accepter un intent qu'aucune write ne pourrait honorer.
		STPModes: []string{"rstp"},
		// Pas de MSTP, donc pas d'instances.
		MSTInstances: 0,
		// L'ACL de SwOS est une LISTE PLATE : il n'y a pas d'ACL named qu'on
		// attacherait a des interfaces, il y a une seule table de rules, chacune
		// portant ses propres ports d'application (`frm`). Le contrat de meshcraft
		// distingue les deux nombres, et la traduction honnete est donc « une ACL,
		// 32 rules ».
		//
		// 32 est la borne que l'interface s'impose (`Z:32`, refus « Max entries (32) »).
		// Les criteres sont riches pour la gamme — MAC source et destination avec
		// masques, ethertype, VLAN et priorite, IP source et destination avec
		// prefixes et ports, protocole, DSCP — et les actions sont « drop »,
		// « mirror », « redirect », limitation de rate et rewrite de VLAN.
		MaxACLs: 1,
		MaxACEs: 32,
		// Aucun routage, donc aucune route.
		MaxStaticRoutes: 0,
		// 15 agregats : /lacp.b borne `sgrp` a 15 (`{n:"Group", t:I, id:"sgrp",
		// max:15}`), et l'interface nomme « Trunk1 » a « Trunk15 » les agregats
		// qu'elle sait afficher.
		MaxLAGs: 15,
		// INCONNU, et dit comme tel rather que suppose.
		//
		// L'interface de the device n'impose AUCUNE borne au nombre de ports d'un
		// same agregat, et aucun observed ne permet de l'establish en lecture seule — il
		// faudrait constituer un agregat et regarder where the device refuse. 0 se lit
		// « inconnu » et fait donc refuser, conformement a la rule du projet : c'est
		// sans consequence aujourd'hui, meshcraft ne posant pas d'agregat, et c'est
		// la valeur a relever le jour where il en posera.
		PortsPerLAG: 0,
		// v2c ET RIEN D'AUTRE, et chacun des deux termes est une decision.
		//
		// Ce que /snmp.b PROUVE : un agent existe, il est actif (`en:0x01`), il porte
		// UNE community (`com`, 64 octets au plus), un contact et une localisation.
		// Ce qu'il prouve tout autant, par son inventaire complet : il n'y a AUCUN
		// SNMPv3 sur cette plateforme — pas d'utilisateur, pas d'algorithme
		// d'authentification, pas de chiffrement, aucun champ, dans aucun onglet ni
		// dans la sauvegarde de configuration. Et pas de destinataire de trap.
		//
		// Ce que cette page n'a jamais pu prouver : a quelle version du protocole cet
		// agent repond. L'interface n'a pas de selecteur, et une community ne dit pas
		// si l'agent sert du v1, du v2c, ou les deux. Cette liste est donc restee VIDE
		// tant que la response manquait — l'outil disponible pour atteindre the device
		// a travers le tunnel ne parlant que HTTP en lecture, aucune request SNMP ne
		// pouvait be emise. Elle ne l'est plus, et c'est le network qui a tranche, pas
		// la page.
		//
		// ┌── v2c : OBSERVED, l'agent a repondu ────────────────────────────────────┐
		//
		// Cette liste was VIDE, et le commentaire qu'elle portait disait : « la liste
		// se levera d'une ligne le jour where un `snmpget` aura repondu depuis un proxy ».
		// C'est fait, le 29 juillet 2026, sur l'reference device 10.99.0.36
		// (CSS326-24G-2S+, SwOS 2.17), par le walk de diagnostic du proxy — lequel parle
		// **v2c en dur** (`gosnmp.Version2c`, internal/snmppoll), ce qui fait de chaque
		// response obtenue une preuve de v2c et non d'autre chose :
		//
		//	sysDescr.0     CSS326-24G-2S+ SwOS v2.17
		//	sysObjectID.0  1.3.6.1.4.1.14988.2
		//	sysName.0      lan-m6      ← le champ `id` de /sys.b, decode
		//	ifNumber.0     26          ← les 26 ports de /link.b (`prt:0x1a`)
		//
		// Les deux dernieres lignes ne sont pas decoratives : elles CROISENT le SNMP
		// avec ce que le connecteur lit en web sur le same device. Un agent qui
		// repondrait pour un autre hardware — cas reel quand une adresse a been recyclee
		// — ne rendrait pas ce nom-there et ce compte de ports-there.
		//
		// v1 N'EST PAS ANNONCE, et l'omission est volontaire : l'agent le sert
		// probablement (SwOS n'a aucun selecteur de version), mais rien ne l'a montre —
		// le seul canal d'interrogation du projet est v2c. Une capability se observes, elle
		// ne se deduit pas d'une vraisemblance, et annoncer v1 n'apporterait rien : c'est
		// v2c que le collecteur emploie.
		//
		// v3 reste ABSENT, et c'est un fait de plateforme established par enumeration
		// exhaustive : /snmp.b tient en quatre champs (activation, community, contact,
		// localisation), il n'y a ni utilisateur, ni algorithme, ni chiffrement, dans
		// aucun onglet ni dans la sauvegarde de configuration. `snmpprov.For` conclut
		// donc « v2c, NON conforme au plancher de la politique », ce qui est exactement
		// la verite a afficher : supervise, et en clair.
		//
		// └───────────────────────────────────────────────────────────────────────┘
		SNMPVersions: []string{"v2c"},
		// Observabilite de la plateforme.
		//
		// TOUT est du polling web, et il faut commencer par there : SwOS ne pousse
		// RIEN. Aucun destinataire de trap, aucun serveur syslog, aucune telemetry —
		// et il ne pourrait pas horodater un message same s'il en envoyait un,
		// n'ayant pas d'horloge. There where le canal de traps d'un Instant On permet de
		// RALENTIR le polling state a 30 s en gardant la reactivity, ici le polling
		// EST la reactivity : un lien qui tombe se voit au prochain cycle, jamais
		// avant.
		//
		// Ce que costs une lecture sur cette plateforme, et c'est ce qui fixe les
		// planchers ci-dessous :
		//
		//   - DEUX requests HTTP par page, toujours. SwOS n'a pas de session : le
		//     Digest se renegocie a chaque fois, donc un 401 puis la request
		//     authentifiee. Une collecte « une lecture toutes les 10 s » est en
		//     realite douze requests par minute ;
		//   - le corps ENTIER de la page, sans exception. SwOS n'offre aucun moyen de
		//     demander un sous-ensemble : ni un port, ni une colonne. There where un walk
		//     SNMP peut se limiter a quatre colonnes, ici /stats.b serialise ses
		//     26 ports et sa quarantaine de compteurs a chaque appel ;
		//   - le CPU qui serialise est celui d'un commutateur ingress de gamme, et
		//     c'est le same qui sert l'interface a l'administrateur humain.
		//
		// Mesures faites sur l'reference device, a travers le tunnel du proxy
		// (moyenne de cinq lectures, aller-retour du controleur inclus, donc majoree) :
		// /sys.b 780 octets en 49 ms, /link.b 2 026 octets en 46 ms, /stats.b
		// 12 694 octets en 50 ms, /backup.swb 2 996 octets en 51 ms.
		//
		// # LE GENRE DES CANAUX, ET POURQUOI IL Y EN A NOW DEUX
		//
		// Quatre canaux `web`, puis UN canal `snmp` en dernier de la liste. Cette
		// place disait longtemps le contraire — « aucun canal snmp ici, et ce n'est
		// pas un oubli » — et elle avait raison tant que `SNMPVersions` was vide :
		// un connecteur ne declares pas une collecte dont il n'a jamais vu l'agent
		// repondre. Le walk du 29 juillet 2026 sur 10.99.0.36 a leve le verrou ; la
		// declaration `v2c` est trente lignes plus haut, et le canal qu'elle autorise
		// porte, OID par OID, ce que ce walk a effectivement obtenu.
		//
		// Le genre n'est pas decoratif : la planification du controleur interroge le
		// canal PAR GENRE (`MonitoringCapabilities.BestOfKind("snmp", …)`), parce que
		// la collecte periodique s'execute sur le proxy et ne parle que ce protocole.
		// Tant que cette plateforme n'annoncait que du `web`, elle ne recevait aucune
		// cadence SNMP — consistent, et sans consequence puisqu'elle n'apparaissait dans
		// aucune cible. C'est exactement ce qui change ici.
		//
		// Les deux genres ne se recouvrent pas, et aucun ne subsume l'autre. Ce que le
		// scraping web rend est dans telemetry.go : compteurs 64 bits, rates computed
		// par the device, temperature de puce, temps de fonctionnement, diagnostic
		// optique COMPLET des cages — le web seul porte les puissances, la table
		// optique de l'agent s'arretant au courant de polarisation. SNMP, lui, costs
		// assez peu pour tenir un plancher a 10 s there where /stats.b serialise
		// 12 694 octets a chaque appel.
		//
		// Le manque, en revanche, est de PLATEFORME et non de protocole : ni la charge
		// processeur ni la memory ne sont published d'un cote ni de l'autre, et c'est
		// `Unavailable` qui le dit a la fiche device — pas une note de canal que
		// personne ne lit.
		Monitoring: driver.MonitoringCapabilities{
			Channels: []driver.MonitorChannel{
				{
					// Etat des ports, depuis /link.b : masques de lien, d'activation et de
					// duplex, index de rate et nom par port. 2 026 octets.
					//
					// PLANCHER A 10 s, et il n'est pas recopie.
					//
					// L'interface de the device rafraichit cette page toutes les 3 s
					// (`refresh:3E3`) : la plateforme encaisse donc demontrablement ce
					// rythme pour un client. Ce n'est pourtant pas le plancher retenu, et la
					// raison est un changement de REGIME, pas de client — une page qu'un
					// administrateur regarde une minute et une collecte qui ne stops
					// jamais, sur un parc entier, pendant des mois, ne sollicitent pas la
					// same chose. 10 s est ce qu'on accepte de faire en PERMANENCE.
					//
					// Descendre plus bas serait de surcroit sans objet : la reactivity qu'on
					// chercherait there ne s'obtient pas par le polling sur cette plateforme,
					// elle s'obtiendrait par un trap, et SwOS n'en emet aucun. Qui voudra la
					// descendre devra l'avoir mesuree sur du hardware, comme celle-ci l'a
					// been.
					Kind:        "web",
					Metrics:     []string{"if_state"},
					MinInterval: 10 * time.Second,
					Note: "SwOS emits no trap: this polling is the ONLY source for " +
						"detecting a link that goes down, and a change is seen at latest " +
						"on the next cycle.",
				},
				{
					// Compteurs et rates, depuis /stats.b. 12 694 octets — six fois la
					// page suivante par la taille, et de loin la plus couteuse.
					//
					// PLANCHER A 15 s, plus HAUT que celui de state des ports, a l'inverse
					// de ce que fait le connecteur Instant On. La raison est le cost, et lui
					// seul : cette page serialise a chaque appel les quarante compteurs des
					// vingt-six ports, sans qu'aucun moyen n'existe d'en demander moins.
					// A 15 s, cela reste sous le kilo-octet par seconde et par device ;
					// a 5 s, un parc de deux cents CSS representerait un demi-megaoctet par
					// seconde de serialisation permanente sur des CPU de commutateurs
					// access.
					//
					// Et ce plancher ne costs PAS la finesse de la courbe, ce qui est le
					// point decisif : SwOS calcule lui-same les rates snapshots (`rrb` et
					// `trb`, en hundredths de bit par seconde). meshcraft n'a donc pas besoin
					// de deriver deux lectures de compteur pour tracer un rate, et un
					// echantillonnage plus large ne le lisse pas — contrairement a une
					// plateforme where le rate ne s'obtient qu'en differenciant
					// `ifHCInOctets`. C'est ce qui rend 15 s acceptable there where il ne le
					// serait pas ailleurs.
					Kind:        "web",
					Metrics:     []string{"if_counters"},
					MinInterval: 15 * time.Second,
					Note: "The entire page (26 ports × ~40 counters) is serialized on " +
						"every read: SwOS cannot serve a subset. Snapshot rates are " +
						"computed by the device, so a wider sample interval does not " +
						"smooth them.",
				},
				{
					// Sante, depuis /sys.b. 780 octets, la page la moins chere.
					//
					// PLANCHER A 30 s. Une temperature ne bouge pas en dessous de la
					// minute, et la conception du monitoring place la sante a cinq minutes :
					// 30 s laisse already dix fois la marge necessary, et il n'y a aucune
					// raison de descendre. Il y en a une de ne pas le faire — c'est aussi la
					// page que l'interface de the device rafraichit a la seconde quand un
					// administrateur la regarde, et le connecteur n'a pas a lui disputer son
					// propre switch.
					Kind:        "web",
					Metrics:     []string{"health"},
					MinInterval: 30 * time.Second,
					Note: "On this model, health reduces to ONE chip temperature " +
						"(/sys.b `temp`, in °C): no fan, no redundant PSU, no board " +
						"sensor — the page serves them as zero and the UI does not " +
						"display them.",
				},
				{
					// Diagnostic optique, depuis /sfp.b.
					//
					// SwOS SERT la puissance optique, et c'est une vraie difference avec
					// l'Instant On, qui ne l'expose nulle part : /sfp.b rend par cage la
					// temperature, la tension, le courant de polarisation, la puissance
					// emise et la puissance recue. Releve sur l'reference device,
					// cage 2 occupee par un SFP-10G-BX : `tpw:0x12ba`, `rpw:0x0ad2`,
					// valeurs vivantes.
					//
					// PLANCHER A 30 s. Les registres de diagnostic d'un transceiver ne sont
					// pas dans la memory du commutateur : ils se lisent sur le bus I²C du
					// module, le plus lent de la carte. Et un budget optique derive sur des
					// jours, pas sur des secondes — une lecture par demi-minute est already
					// bien plus fine que ce que le phenomene demande.
					Kind:        "web",
					Metrics:     []string{"optical_power"},
					MinInterval: 30 * time.Second,
					Note: "Only covers SFP cages (2 of 26 ports on a CSS326-24G-2S+). " +
						"An empty cage is recognized by its temperature, which the " +
						"device returns as -128 °C. ONLY channel that serves optical " +
						"POWERS: the SNMP agent serves the optical table WITHOUT its " +
						"power columns (observed; see the snmp channel).",
				},
				{
					// ┌── LE CANAL SNMP : OBSERVED SUR HARDWARE, ET C'EST NOUVEAU ────────┐
					//
					// Ce canal n'existait pas, parce que `SNMPVersions` was vide : le
					// connecteur ne pouvait pas declare une collecte dont il n'avait pas
					// vu l'agent repondre. Le walk du 29 juillet 2026 sur 10.99.0.36 a
					// tranche, et ce canal ne porte QUE ce que ce walk a effectivement
					// obtenu — pas la liste de ce qu'une MIB permettrait d'esperer.
					//
					// CE QUE L'AGENT SERT, verified OID par OID :
					//
					//	ifDescr        « Port1 »… « SFP1 », « SFP2 » — les noms de /link.b, et
					//	               donc CEUX DE CE CONNECTEUR depuis qu'il nomme ses ports
					//	               comme the device (cf. naming.go). Une serial collectee
					//	               en SNMP par le proxy et une configuration lue en web par
					//	               le controleur designent la same prise par le same nom,
					//	               sans table de correspondance nulle part.
					//	ifOperStatus   servi (1 sur la cage SFP2, seule a porter un lien)
					//	ifHCInOctets   189 323 935 sur cette cage : compteurs 64 bits VIVANTS
					//	ifHCOutOctets  1 666 156
					//	ifHighSpeed    10000 (Mbit/s) — et c'est LUI qu'il faut lire (voir plus bas)
					//	sysUpTime      servi
					//	mtxrHealth.11  690, soit 69,0 °C — la SEULE grandeur de sante published
					//
					// CE QUE L'AGENT NE SERT PAS, et qui decide de ce canal autant que le
					// reste : `hrProcessorLoad` et `hrStorage` rendent AUCUNE valeur — pas
					// de CPU, pas de memory par SNMP sur cette plateforme. Et la table
					// optique stops a la colonne 8 (`txBias`) : ni `txPower` (colonne
					// 9), ni `rxPower` (colonne 10). C'est pourquoi `optical_power`
					// N'EST PAS dans la liste ci-dessous alors que le canal web, lui, le
					// sert — les puissances sont dans /sfp.b (`tpw`, `rpw`) et nulle part
					// ailleurs. Declare ici une metrique que l'agent ne rend pas ferait
					// promettre une courbe qu'aucune collecte ne remplirait.
					//
					// LE TRAP A NE PAS REPRODUIRE, observed sur la cage active :
					// `ifSpeed` rend **0** sur un lien a 10 Gbit/s, there where `ifHighSpeed`
					// rend 10000. Un collecteur qui lirait `ifSpeed` afficherait donc un
					// rate nul sur les liaisons les plus rapides du parc. Le poller lit
					// bien `ifHighSpeed` (internal/snmppoll) ; c'est note ici parce que
					// c'est le genre de chose qu'on « corrige » un jour dans le mauvais
					// sens.
					//
					// PLANCHER A 10 s, contre 15 s pour les compteurs en web : un GETBULK
					// sur six colonnes costs a cet device infiniment moins que la
					// serialisation des 12 694 octets de /stats.b, et c'est tout l'interet
					// d'avoir ce canal. Il ne REMPLACE pas le web pour autant — /stats.b
					// porte les rates snapshots computed par the device, que SNMP
					// n'expose pas.
					//
					// └────────────────────────────────────────────────────────────────┘
					Kind:        "snmp",
					Metrics:     []string{"if_state", "if_counters", "health"},
					MinInterval: 10 * time.Second,
					Note: "Read-only v2c agent, no trap destination: reactivity stays " +
						"that of polling. Health here reduces to ONE temperature and " +
						"uptime — neither CPU nor memory; the agent does not serve " +
						"HOST-RESOURCES-MIB (observed). Optical POWERS do not go through " +
						"this channel: this agent's optical table stops at bias current.",
				},
			},
			// SwOS NE POUSSE RIEN.
			//
			// Pas de destinataire de trap : /snmp.b n'a que quatre champs — activation,
			// community, contact, localisation. Pas de serveur syslog : aucun champ
			// dans aucun onglet, et de toute way aucune horloge pour horodater un
			// message. Pas de telemetry continue, evidemment.
			//
			// Ce n'est pas un detail de tableau : c'est ce qui fait que la supervision
			// de cet device est ENTIRELY du polling, et qu'un exploitant qui
			// compte sur des alertes poussees doit le savoir avant de batir dessus.
			Traps:     false,
			Syslog:    false,
			Streaming: false,
			// Ce que la plateforme n'expose PAS, avec son motif.
			//
			// Deux cas seulement, et volontairement : ce n'est pas la liste de tout ce
			// qui manque a the UI. Les metriques servies par un canal ci-dessus n'ont
			// rien a faire ici, et une metrique que meshcraft ne lit pas encore n'y a
			// rien a faire non plus — l'y mettre la declarerait impossible et ferait
			// renoncer un exploitant a ce qui n'attend qu'un lot de collecte.
			Unavailable: unavailableFor(poe),
		},
		// UNE session a la fois, et c'est un plancher de prudence assume.
		//
		// SwOS n'a pas de session du tout : authentication is a header HTTP
		// rejoue a chaque request, et rien ne compte les clients. Il n'existe donc
		// aucune limite documentee a annoncer, et aucun message d'erreur du type
		// « trop de sessions » dans son interface.
		//
		// Ce qui A been established : des dizaines de lectures SEQUENTIELLES passent sans
		// incident. Ce qui n'a PAS been established : ce que fait the device sous lectures
		// concurrentes — cela n'a pas been tente, un CPU de commutateur access n'etant
		// pas un endroit where l'on decouvre ce genre de chose sur un device en
		// service. Declare 1 fait serialiser les access du connecteur, ce qui est
		// exactement le comportement sur, et c'est la rule « une capability inconnue
		// est supposee absente » appliquee au parallelisme.
		MaxSessions: 1,
		// IMMEDIAT, et c'est la contrainte la plus lourde de ce connecteur : il n'y a
		// AUCUN filet gratuit a l'apply.
		//
		// L'inventaire de l'interface est sans ambiguite. Ses seuls boutons sont
		// « Apply All », qui poste la page et rien d'autre, et « Discard Changes »,
		// qui se contente de RELIRE la page depuis the device — un annulateur
		// purement local, qui n'annule rien de ce qui est parti. Il n'existe nulle part
		// de « Save Configuration », de configuration de demarrage distincte, ni de
		// commit a confirmer : ce qui est poste est written, et un reboot ne
		// l'annulera pas. C'est l'inverse exact de l'Instant On, dont le « appliquer
		// puis sauver » offre un retour arriere par coupure d'alimentation.
		//
		// La seule way de revenir en arriere est donc de RESTAURER un fichier de
		// sauvegarde. C'est pourquoi Admin.Backup existe et pourquoi ApplyPorts la
		// prend avant to write : sur cette plateforme, la prudence n'est pas dans la
		// strategie de commit, elle est dans ce qu'on a pris la peine de copier avant.
		Commit: driver.CommitImmediate,
		Mgmt: driver.MgmtCapabilities{
			// Le VLAN de management est LIBRE : /sys.b `avln` (« Allow From VLAN »),
			// borne a 4095 par l'interface, 0 valant « depuis n'importe quel VLAN ».
			VlanIDs: driver.VlanAny,
			// 1 a 4094 et non 1 a 4095 : la borne de l'interface sur `avln` est bien
			// 4095, mais un VLAN de management doit exister dans la TABLE des VLAN pour
			// que le port qui le porte en soit membre, et la table stops a 4094.
			// Retenir la borne la plus BASSE des deux est le seul choix sur.
			VlanRange: [2]int{1, 4094},
			// TAGGED : OBSERVED, pas deduit.
			//
			// L'reference device est joignable en 10.99.0.36 avec `avln:0x0063`
			// (VLAN 99), alors que le SEUL port qui porte un lien est la cage SFP2
			// (/link.b `lnk:0x02000000`, bit 25) et que le VLAN par default de ce port
			// est 1 (/fwd.b `dvid[25]=0x01`). Le trafic d'administration arrive donc
			// necessarily TAGGED en 99 sur ce port. La table des adresses apprises le
			// confirme, qui montre des hotes en VLAN 99 sur ce same port (/!dhost.b,
			// `vid:0x0063, prt:0x19`).
			TaggedOK: true,
			// Les trois modes du selecteur « Address Acquisition » de l'interface, dans
			// son ordre : « DHCP with fallback », « static », « DHCP only ».
			// L'reference device est en statique (`iptp:0x01`), ce qui est
			// observed ; les deux autres modes existent dans le code de son interface,
			// ce qui est une source forte mais n'a pas been exerce.
			Static: true,
			DHCP:   true,
			// NI PASSERELLE, NI RESOLVEUR — et c'est une ABSENCE ESTABLISHED, pas un
			// « pas trouve ». Le jeu de champs inscriptible de /sys.b est enumerated par
			// la sauvegarde de configuration de the device lui-same
			// (testdata/backup.swb) : `ip` et `iptp` y sont, aucun champ de passerelle
			// ni de DNS n'y figure, et l'interface web complete de la plateforme tient
			// en un seul fichier qui definit tous ses champs. L'enumeration exhausts la
			// question, comme elle l'a exhausted pour le destinataire de traps.
			//
			// Consequence d'exploitation : sans passerelle, les sorties hors
			// sous-network (traps, DNS) n'ont pas de route ici. L'administration
			// entrante depuis un autre sous-network reste possible tant que le
			// chemin L2 de retour existe — ce n'est pas « injoignable hors subnet ».
			// Le collecteur beneficie quand meme d'etre dans le bon network.
			Gateway: false,
			DNS:     false,
			// No L3 route off-subnet (same fact as Gateway=false). Inbound still
			// works over L2/ARP when a return path exists — place the collector
			// in the right network rather than relying on a gateway that is not there.
			Routed: false,
			IPARP:  true,
			// DEDUIT de la documentation MikroTik, pas observed : l'adresse de repli
			// d'usine d'un device MikroTik est 192.168.88.1. Le mode « DHCP with
			// fallback » de l'interface implique bien une adresse de repli, mais
			// l'reference device etant en statique, aucune lecture ne la donne.
			// A verify avant de s'en servir pour rattraper un device perdu.
			DHCPFallback: "192.168.88.1",
			// AUCUNE voie de secours hors bande, et c'est ESTABLISHED par enumeration, pas
			// suppose : sur l'reference device, un seul port TCP repond — le 80.
			// Ni 22, ni 23, et ce model n'a pas de connecteur serial.
			//
			// La difference avec RouterOS est instructive et vaut d'be written ici,
			// parce que les deux plateformes sortent du same constructeur : RouterOS
			// garde MAC-winbox, i.e. une administration en couche 2 qui survit a
			// n'importe quelle erreur d'adressage. SwOS n'a rien de tel — son interface
			// web est son unique porte, et cette porte depend du VLAN declared dans
			// `avln` (/sys.b). Se tromper there ferme the device pour de bon.
			//
			// Consequence, a lire avec [driver.CommitImmediate] juste au-dessus : ni
			// filet de commit, ni voie de secours. Le SEUL retour arriere de cette
			// plateforme est la copie de configuration prise avant writing
			// ([driver.ApplyResult.Backup]).
			OutOfBand: false,
			// Same Digest POST channel as port apply / SNMP. Same-VLAN apply and
			// jump both rewrite /sys.b after GET /backup.swb; JumpVLAN is the
			// honest single-address leap (never MgmtMover).
			ApplySameVLAN: true,
			JumpVLAN:      true,
			// L'ORDRE DES OPERATIONS. Ce model n'a PAS de serial port : chacune des
			// lignes ci-dessous est une way documentee de se couper la main, et le
			// seul recours serait un deplacement avec un trombone.
			Notes: []string{
				"NO out-of-band console: no SSH, no Telnet, no serial port. On the " +
					"reference device, a single TCP port answers — 80. A management " +
					"addressing mistake is fixed on site, at the factory-reset button.",
				"Create the VLAN in the table (/vlan.b) AND make the port that carries " +
					"administration a member of it BEFORE selecting that VLAN in « Allow " +
					"From VLAN » (/sys.b `avln`). The reverse order points administration " +
					"at a VLAN that never reaches the CPU.",
				"« Allow From VLAN » accepts ONLY one VLAN, and there is no second " +
					"management address: unlike Instant On, you cannot add the new path, " +
					"verify it, then remove the old one. The cutover is a jump, which is " +
					"why a configuration backup must be taken beforehand.",
				"Three other /sys.b fields cut access just as surely: `allp` " +
					"(« Allow From Ports », mask of ports allowed to administer), `alla` " +
					"and `allm` (allowed subnet). They mean « allow everything » when zero " +
					"on the reference device; writing them without understanding them is " +
					"the shortest path to a silent device.",
				"The management port VLAN mode (/fwd.b `vlan`) must stay compatible " +
					"with the management VLAN. Setting it to « strict » on a port that " +
					"is not a member of the management VLAN drops frames before they " +
					"reach the CPU.",
				"SwOS configuration is persistent IMMEDIATELY (see Commit): a power " +
					"cut does not undo a failed cutover.",
				"The address is little-endian in the page (`0x2400630a` = 10.99.0.36). " +
					"Reversing it writes 36.0.99.10 while believing you wrote 10.99.0.36.",
			},
		},
		// Le compte d'administration : il n'y en a qu'un, et il ne se nomme pas.
		Admin: driver.AdminAccount{
			// PAS de compte named, et c'est un FAIT, pas une prudence.
			//
			// SwOS n'a aucune notion de compte. Le formulaire de changement de mot de
			// passe de son interface ne porte que trois champs — ancien, nouveau,
			// confirmation ; la page qui le receives ne contient qu'une key, `{pwd:''}` ;
			// et la sauvegarde de configuration ENTIRE de the device ne mentionne
			// aucun nom d'utilisateur. Il n'y a litteralement rien a nommer.
			//
			// Consequence pour meshcraft : le nom unifie du parc ne peut PAS be applied
			// ici. C'est important et non anecdotique — cela veut dire que access
			// direct a un CSS ne sera jamais predictable de la same way que sur le
			// reste du parc, et que la seule chose que le provisionnement peut y
			// unifier est le SECRET.
			Custom:    false,
			FixedName: FixedAdminName,
			// 0 se lit « inconnu » dans ce contrat, et c'est ici la valeur exacte :
			// SwOS n'impose aucune longueur minimale, et accepte same un mot de passe
			// VIDE — l'reference device en porte un, et c'est avec un secret
			// vide que toutes les lectures de ce connecteur ont been validees. Le dire
			// dans les notes rather que de laisser un 1 mentir.
			PasswordMin: 0,
			// 15 caracteres : la borne que l'interface de the device s'impose, pour
			// l'ancien comme pour le nouveau secret (« Bad old password » au-dela de 15
			// sur l'ancien, « New password is too long » sur le nouveau).
			PasswordMax: maxPasswordLen,
			// La ponctuation passe : la seule contrainte est l'ASCII (cf. Notes).
			AlphanumericOnly: false,
			Notes: []string{
				"An EMPTY password is accepted by SwOS, and that is the factory state: " +
					"the reference device carries one, and the Digest computes normally " +
					"with it. A freshly factory-reset CSS is therefore taken over with no " +
					"initial secret.",
				"ASCII ONLY. The device UI builds secret bytes with `charCodeAt`, which " +
					"returns UTF-16 code units: beyond character 127, what the browser " +
					"sends and what another client would send diverge, and the applied " +
					"secret is not the one you think. The connector therefore refuses " +
					"before sending.",
				"The secret is not sent in cleartext but in a homemade digest " +
					"(RC4 keyed by the OLD password, see PasswordBlob). Practical " +
					"consequence: changing the password REQUIRES knowing the previous " +
					"one, and the device answers HTTP 405 when it is wrong — the only " +
					"usable error message on the whole platform.",
				"The secret is NOT recoverable: losing it forces an on-site factory " +
					"reset; this model has no serial port.",
				"The change takes effect IMMEDIATELY and without a save: the next " +
					"request must present the new secret, otherwise it gets a 401 that " +
					"looks like an outage.",
			},
		},
	}
}

// unavailableFor rend ce que la plateforme n'expose pas, selon que le model porte
// du PoE ou non.
//
// Chaque ingress repond a « pourquoi n'ai-je pas cette courbe ? » en disant s'il y a
// quelque chose a attendre — la seule chose que ces motifs apportent a un exploitant.
//
// LA CHARGE PROCESSEUR ET LA MEMORY Y SONT ENTREES avec the UI qui les affiche.
// Elles were dites dans deux endroits que personne ne lit : une `Note` de canal de
// supervision, et les phrases que `ReadTelemetry` joint a chaque snapshot. Or ce qui
// atteint la fiche device est CETTE liste, et elle seule — le panneau de telemetry
// affichait donc « le connecteur ne dit pas si la plateforme la publie » sur une absence
// que le connecteur documente depuis sa first livraison. Un savoir qui n'arrive pas a
// the UI where la question se pose n'existe pas.
//
// La granularite est celle de la QUESTION, pas celle du canal : `health` reste un canal
// declared — cette plateforme sert bien une temperature — et ce sont les deux grandeurs
// manquantes qui se nomment. Declare `health` indisponible ferait disparaitre la
// temperature, qui est la seule mesure de fatigue disponible ici.
func unavailableFor(poe bool) []driver.MetricUnavailable {
	var out = []driver.MetricUnavailable{
		{
			Metric:   "cpu",
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (10.99.0.36): " +
				"none of the served pages (/sys.b, /link.b, /fwd.b, /vlan.b, /stats.b, " +
				"/sfp.b, /rstp.b, /lacp.b, /snmp.b, /acl.b, /host.b, /dhost.b) carries a " +
				"load field, and neither does the full configuration backup (/backup.swb). " +
				"The device SNMP agent serves nothing on hrProcessorLoad " +
				"(1.3.6.1.2.1.25.3.3.1.2) — walk of 29 July 2026 through the tunnel.",
		},
		{
			Metric:   "memory",
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (10.99.0.36): " +
				"no memory field in the served pages or in /backup.swb, and the SNMP " +
				"agent returns no value on hrStorage (1.3.6.1.2.1.25.2.3) — walk of " +
				"29 July 2026.",
		},
		{
			Metric: "lldp",
			// Established by ENUMERATION, not probing: this is the strongest form of
			// proof. A SwOS web UI fits in a single file that defines ALL its tabs and
			// ALL its fields — there is no hidden page to discover, unlike Instant On
			// where a section could exist without being shown. Not finding LLDP there,
			// and not finding it in the configuration backup either, exhausts the
			// question.
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (10.99.0.36): " +
				"the entire web UI (index.html, 44,807 bytes, fully inline script) " +
				"defines its tabs and every field — no LLDP occurrence outside PoE " +
				"power negotiation on a port, no neighbor table, no discovery tab " +
				"besides « Mikrotik Discovery Protocol » (/sys.b `pdsc`). The full " +
				"configuration backup (/backup.swb, 2,996 bytes, attached to the " +
				"package) carries no LLDP field. Pages /lldp.b and /neighbor.b do not " +
				"exist: they render the web UI, as every unknown path does on this " +
				"platform.",
		},
	}
	if !poe {
		out = append(out, driver.MetricUnavailable{
			Metric:   "poe",
			Observed: true,
			Source: "Observed on MikroTik CSS326-24G-2S+ running SwOS 2.17 (10.99.0.36): " +
				"/sys.b returns `npoe:0x00`, `poe:0x00` and `poes:0x00`, and page /poe.b " +
				"does not exist — it renders the web UI. The UI also gates its PoE tab " +
				"on the presence of that hardware.",
		})
	}
	return out
}

// ResetProcedure de la gamme.
//
// `Confirmed` est FAUX, et il faut lire pourquoi : les deux halves de cette
// procedure ne sont pas connues avec la same certitude, et le contrat n'a qu'un
// booleen pour les deux. La voie logicielle est solidement established — elle est
// observed dans le code de l'interface de the device lui-same. Le geste hardware,
// lui, vient de la documentation MikroTik et n'a pas been exerce sur ce model. Comme
// c'est precisely le geste qu'un operator tentera quand il aura perdu access,
// i.e. au pire moment, le present  comme verified serait la faute que ce
// champ existe pour empecher.
func (d *Driver) ResetProcedure(model string) driver.ResetProcedure {
	return driver.ResetProcedure{
		Hardware: "Front-panel Reset button. Cut power, hold the button, restore power " +
			"while still holding until the status LED starts blinking (about 5 seconds), " +
			"then release: SwOS configuration returns to factory state. Releasing BEFORE " +
			"the blink is only a normal boot and erases nothing. PROCEDURE UNVERIFIED on " +
			"this model: it comes from vendor documentation, not a trial.",
		Software: "Web UI, System tab, Backup section, « Reset Configuration » button " +
			"(confirmation required). The gesture is a POST to /reset, observed in the " +
			"device UI code. Administrator access required, so unusable when access is " +
			"exactly what was lost.",
		Erases: []string{
			"the VLAN table and port membership",
			"management addressing (address, mode, VLAN and access restrictions)",
			"the administration password",
			"system name, port names, aggregation, ACLs, static hosts",
			"the SNMP community",
		},
		AfterReset: []string{
			"user « admin », EMPTY password — observed on the reference device, which " +
				"carries that state",
			"fallback address 192.168.88.1 (MikroTik documentation, NOT verified)",
			"web UI on port 80, cleartext, no HTTPS",
		},
		Pitfalls: []string{
			"NO serial port on this model, and neither SSH nor Telnet: the Reset button " +
				"is the ONLY recourse after loss of access, and it requires a site visit.",
			"The hardware gesture requires cutting then restoring power while holding " +
				"the button: it is not a simple long press on a powered-on device.",
			"SwOS configuration is persistent immediately: unlike a platform that " +
				"« apply then save », a power cut undoes no configuration mistake. Only " +
				"restoring a backup (/backup.swb) does.",
			"On CRS chassis that can boot under RouterOS or SwOS, a factory reset is " +
				"not an OS switch: verify which of the two answers before concluding the " +
				"reset failed.",
		},
		// False: see the comment on this method.
		Confirmed: false,
		Source: "Software path: observed in the embedded UI JavaScript of a " +
			"CSS326-24G-2S+ on SwOS 2.17 (« Reset Configuration » button → POST /reset). " +
			"Hardware gesture and fallback address: MikroTik documentation, not " +
			"exercised on this model.",
	}
}

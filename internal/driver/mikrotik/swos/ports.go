package swos

import (
	"fmt"
	"sort"
)

// Le model VLAN de SwOS pris au serieux : l'APPARTENANCE, le VLAN NATIF, et les
// TROIS champs par port qui decident si tout cela agit ou decore.
//
// # POURQUOI CE FICHIER EXISTE
//
// La first livraison du connecteur posait deux choses — le masque de membres de
// /vlan.b et le tableau `dvid` de /fwd.b — et s'arretait there. C'est la half du
// model. /fwd.b porte trois AUTRES champs par port, tous inscriptibles, tous already
// renvoyes a the device a chaque pose (cf. FwdWritableFields), et chacun capable
// d'annuler ce que les deux premiers viennent to write :
//
//	`vlan` (FieldVLANMode)     — 0 disabled, 1 optional, 2 enabled, 3 strict.
//	                            A `disabled`, le port IGNORE la table des VLAN :
//	                            l'appartenance et le VLAN natif sont configures et
//	                            SANS EFFET.
//	`vlni` (FieldVLANReceive)  — 0 any, 1 only tagged, 2 only untagged. Un port
//	                            access en « only tagged » jette tout son trafic.
//	`fvid` (FieldForceVLAN)    — masque. Un port en « Force VLAN ID » range TOUT son
//	                            trafic entrant dans son VLAN natif, tags
//	                            comprises : il n'est plus un trunk, quoi que la
//	                            table dise de lui.
//
// La lecture state les taisait aussi, sauf `vlan`. Un UI pouvait donc present
// un trunk de trois VLAN sur un port qui force tout dans le VLAN 1.
//
// # CE QUE CE FICHIER WRITES, ET CE QU'IL REFUSE WRITE
//
// Il written UN seul de ces trois champs, `vlan`, et seulement pour le FAIRE MONTER de
// `disabled` a `enabled`. Les raisons de ne pas toucher aux autres sont dans
// [ModeRaise] et [receivePolicy] — elles tiennent toutes a la same chose : sur cette
// plateforme, resserrer un filtre est un geste qui JETTE du trafic, et il n'y a ni
// console de secours, ni retour arriere.
//
// Source de la semantique des valeurs : la documentation SwOS de MikroTik (manuel de
// la gamme CSS610, section VLAN), corroborated par les valeurs observed sur le
// CSS326-24G-2S+ de reference — `vlan:[0x01 × 26]` (« optional », le default),
// `vlni:[0x00 × 26]` (« any »), `fvid:0x00000000` (personne).

// --- L'appartenance voulue d'un port ---

// askedVLANs rend les VLAN dont un port DOIT be membre selon l'intention.
//
// Le VLAN natif compte comme une appartenance, et ce n'est pas une subtilite : sur
// SwOS, une trame ne sort d'un port que si ce port est membre de son VLAN — le VLAN
// natif n'echappe pas a la rule, il decide seulement de l'ETIQUETTE. Un PVID applied
// sans appartenance donne un port qui range son trafic entrant dans un VLAN dont rien
// ne ressort (cf. [vlanAnomalies], qui le signale quand the device le porte already).
func askedVLANs(want PortState) []int {
	var seen = map[int]bool{}
	var out []int
	var add = func(id int) {
		if id <= 0 || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	add(want.PVID)
	var id int
	for _, id = range want.Tagged {
		add(id)
	}
	sort.Ints(out)
	return out
}

// heldVLANs rend les VLAN dont un port est membre DANS LA TABLE data.
//
// Lu sur la table rather que sur state standard, et la difference compte : state
// standard rend `Tagged`, i.e. l'appartenance MOINS le VLAN natif. Calculer
// un retrait there-dessus laisserait croire qu'un port n'est pas membre de son propre
// VLAN natif, donc ferait retirer ce qu'il faut garder.
//
// `ok` est faux from qu'un masque n'est pas lisible : un retrait calcule sur une
// appartenance devinee est un retrait au hasard, et sur cette plateforme un retrait
// coupe.
func heldVLANs(table []*Page, index int) ([]int, error) {
	var out []int
	var row *Page
	for _, row = range table {
		var vid, ok = row.Num(FieldVLANID)
		if !ok {
			continue
		}
		var member bool
		member, ok = row.Bit(FieldVLANMembers, index)
		if !ok {
			return nil, fmt.Errorf("/vlan.b : le masque de membres du VLAN %d n'est pas "+
				"lisible au rang %d (champ « %s » absent ou de forme inattendue)",
				vid, index, FieldVLANMembers)
		}
		if member {
			out = append(out, int(vid))
		}
	}
	sort.Ints(out)
	return out, nil
}

// --- Le retrait, et les trois refus qui l'encadrent ---

// LeaveDecision est ce qu'une pose fait — ou refuse de faire — d'une appartenance
// que le plan ne demande plus.
type LeaveDecision struct {
	// Leave : les VLAN dont le port sort actually.
	Leave []int
	// Refused : les retraits que la pose s'interdit, avec leur motif, en clair.
	Refused []string
}

// planLeaves decide quelles appartenances un port PERD.
//
// LE RETRAIT EST LA HALF MANQUANTE DE LA CONVERGENCE, et son absence n'was pas
// neutre : un device pouvait be annonce « conforme » tout en transportant des
// VLAN que le plan avait retires depuis des mois. La limite was dite a the UI, ce
// qui valait mieux que le silence, mais elle laissait le parc deriver dans le sens le
// plus couteux — un VLAN qui fuit entre deux zones ne se voit pas, et quand il se
// voit, c'est en cherchant autre chose.
//
// TROIS REFUS l'encadrent, et chacun couvre une way documentee de se couper la
// main sur une plateforme qui n'a ni SSH, ni Telnet, ni serial port :
//
//  1. UN VLAN DE MANAGEMENT NE SE RETIRE PAS D'UN PORT QUI PORTE UN LIEN, sauf
//     `force`. Ni celui du plan du site, ni celui que the device declares : rien ne
//     dit lequel des ports actifs porte la session — SwOS ne le publie pas, et `avln`
//     ne nomme qu'un VLAN, pas un port.
//
//     CE REFUS PORTAIT SUR TOUS LES PORTS, y compris ceux sans lien, au motif qu'« un
//     port sans lien aujourd'hui est peut-be l'uplink de demain, et le benefit d'un
//     tel retrait est nul ». Les deux halves de cette phrase were fausses, et le
//     reference fleet l'a montre le 29 juillet 2026 :
//
//     LE BENEFIT N'EST PAS NUL, IL EST DE SECURITY. Sur SwOS, appartenance et
//     tagging sont la SAME data : laisser le VLAN de management membre d'un port
//     access, c'est laisser n'importe quel poste branche there atteindre le plan
//     d'administration en etiquetant lui-same son trafic. C'est tres exactement le
//     trou que l'intention « access VLAN 100 » existe pour fermer.
//
//     ET LE COST WAS  LA CONVERGENCE, ENTIRELY. Le VLAN 99 de the device de
//     reference est membre des VINGT-SIX ports (observed /vlan.b). Une pose demandant
//     « les 24 ports cuivre en access VLAN 100 » ecrivait donc ZERO port et refusait
//     vingt-quatre fois, a chaque passage, pour toujours : `ports_written` vide,
//     quarante-huit ecarts que rien ne pouvait fermer, et un exploitant qui lit
//     « ca ne fonctionne pas » — ce qui was le bon diagnostic.
//
//     Le risque residuel, lui, exige un LIEN : une session ne traverse pas une prise
//     vide. C'est already le critere des deux autres garde-fous de ce fichier et de
//     [planModeRaise] ; l'appliquer ici rend le fichier consistent au lieu d'ajouter une
//     hypothese. Un port actif qui n'est PAS celui de la session garde donc son
//     appartenance : c'est le residu conservateur, et il est assume ;
//
//  2. AUCUN RETRAIT SUR UN PORT ACTIF quand aucun VLAN de management n'est connu
//     (`avln:0x00`, l'factory state : « administration accepted depuis n'importe quel
//     VLAN »). Rien ne dit alors quel port porte la session, et retirer une
//     appartenance de celui qui la porte la coupe ;
//
//  3. LE VLAN NATIF EFFECTIF n'est jamais retire. Il est dans `asked` par
//     construction quand le plan en designe un ; ce garde-fou couvre le cas where le
//     plan n'en designe PAS et where le port garde donc celui de the device.
//
// Le VLAN 1 n'est jamais concerne : il n'a pas de ligne dans la table (cf.
// implicitVLAN), donc rien a en retirer.
//
// Fonction PURE : c'est elle qui decide de couper un VLAN sur un port, et elle doit
// be verifiable sur les captures reelles de the device rather que sur du hardware
// en service.
func planLeaves(port string, index int, want PortState, effectivePVID int,
	table []*Page, linked bool, protected map[int]string, force bool) (LeaveDecision, error) {
	var out LeaveDecision

	// `TaggedAtLeast` means unresolved « all » (empty site plan): do not strip
	// tagged membership. A resolved « all » has TaggedAtLeast false and an exact
	// site VLAN list — removals are legitimate.
	if want.TaggedAtLeast {
		return out, nil
	}

	var held, err = heldVLANs(table, index)
	if err != nil {
		return out, err
	}
	var asked = map[int]bool{}
	var v int
	for _, v = range askedVLANs(want) {
		asked[v] = true
	}
	// Le natif effectif, qu'il vienne du plan ou de the device.
	if effectivePVID > 0 {
		asked[effectivePVID] = true
	}

	var mgmtUnknown = len(protected) == 0
	for _, v = range held {
		if v == implicitVLAN || asked[v] {
			continue
		}
		var why, isProtected = protected[v]
		if isProtected && linked && !force {
			out.Refused = append(out.Refused, fmt.Sprintf(
				"%s reste membre du VLAN %d (%s) : c'est un VLAN de MANAGEMENT et ce port "+
					"porte un lien ACTIF. SwOS ne dit pas lequel de ses ports porte la session "+
					"d'administration — « %s » ne nomme qu'un VLAN — donc retirer ce VLAN d'un "+
					"port actif peut be le geste qui coupe access, sur un model qui n'a ni "+
					"SSH, ni Telnet, ni serial port. Les ports SANS lien, eux, perdent bien cette "+
					"appartenance : c'est ce qui permet a un access port de devenir vraiment un "+
					"access port. Pour l'y retirer aussi, il faut forcer.",
				port, v, why, FieldAllowVLAN))
			continue
		}
		if mgmtUnknown && linked && !force {
			out.Refused = append(out.Refused, fmt.Sprintf(
				"%s reste membre du VLAN %d : il porte un lien ACTIF et the device "+
					"accepte l'administration depuis N'IMPORTE QUEL VLAN (/sys.b « %s » = 0), "+
					"donc rien ne dit que ce n'est pas lui qui porte la session. Sur cette "+
					"plateforme, appartenance et tagging sont la SAME data : retirer un "+
					"port de la liste des membres coupe ce VLAN sur ce port, sans demi-mesure.",
				port, v, FieldAllowVLAN))
			continue
		}
		out.Leave = append(out.Leave, v)
	}
	sort.Ints(out.Leave)
	return out, nil
}

// --- Le mode VLAN : faire agir ce qu'on vient to write ---

// ModeRaise est la decision de mode VLAN pour UN port.
type ModeRaise struct {
	// Mode est la valeur a write dans /fwd.b `vlan`.
	Mode uint32
	// Change dit s'il y a quelque chose a write. Faux ne veut PAS dire « mode 0 » :
	// VLANModeDisabled VAUT zero, et confondre les deux ferait ETEINDRE le filtrage
	// des ports qu'on ne voulait pas toucher — l'exact inverse du but de ce code.
	Change bool
	// Refused est le motif du refus, quand il y en a un.
	Refused string
}

// planModeRaise decide s'il faut REVEILLER le filtrage VLAN d'un port.
//
// # LE PROBLEME : UNE POSE REUSSIE ET SANS EFFET
//
// Un port dont /fwd.b `vlan` vaut `disabled` IGNORE la table des VLAN. meshcraft
// pouvait donc y write une appartenance et un VLAN natif, relire, tout comparer,
// afficher « conforme » — et le port continuait de laisser tout passer. La lecture
// state le DISAIT dans une note, ce qui was mieux que rien, mais aucune pose ne le
// corrigeait : la seule action possible pour l'exploitant was d'aller cliquer dans
// l'interface de the device, ce qui est exactement ce que meshcraft existe pour
// eviter.
//
// # CE QUI EST WRITES : `disabled` → `enabled`, ET RIEN D'AUTRE
//
// Les quatre modes ne sont pas quatre crans d'un same reglage :
//
//	disabled — la table n'est pas consultee. Ce qu'on written est decoratif.
//	optional — le DEFAULT d'usine, et state des 26 ports de the device de
//	           reference. La table est consultee, mais un VLAN qu'elle ne declares pas
//	           passe librement.
//	enabled  — filtrage a la SORTIE : une trame ne sort d'un port que s'il est membre
//	           du VLAN.
//	strict   — `enabled` PLUS un control a l'INGRESS : le port doit be membre du
//	           VLAN de la trame recue.
//
// Seul le passage depuis `disabled` est fait, et seulement jusqu'a `enabled` :
//
//   - `optional` → `enabled` RESSERRE un filtre sur un port qui fonctionne
//     aujourd'hui. Toute trame d'un VLAN que la table ne declares pas cesse de passer
//     a la seconde where c'est written. Sur l'reference device, dont les 26 ports
//     sont en `optional`, personne n'a requested ca — et meshcraft ne sait pas quels
//     VLAN non declares transitent par un port ;
//   - `enabled` → `strict` ajoute un filtre d'INGRESS, i.e. jette des trames
//     que le port acceptait. La documentation de MikroTik le recommande en fin de
//     configuration ; elle s'adresse a un humain qui regarde son switch, pas a une
//     reconciliation qui repasse toutes les nuits sur deux cents devices ;
//   - RIEN N'EST JAMAIS LOWERED. Un exploitant qui a mis `strict` l'a voulu.
//
// # LE GARDE-FOU : NE PAS FILTRER CE QU'ON N'A PAS DECLARED
//
// `enabled` filtre a la sortie. Le poser sur un port qui n'est PAS membre d'un VLAN
// de management, ou pas membre de son propre VLAN natif, ferait tomber ce trafic-there.
// Le control porte donc sur l'appartenance TELLE QU'ELLE SERA AFTER la pose —
// `members` est calcule sur la table already completee — et non sur celle d'avant :
// c'est le same geste qui declares et qui filtre, dans cet ordre.
//
// Un port SANS LIEN ne porte aucune session : le garde-fou ne s'y applique pas, sans
// quoi un device neuf, dont tous les ports sont off, resterait a jamais en
// filtrage disabled.
func planModeRaise(port string, current uint32, members map[int]bool, effectivePVID int,
	linked bool, protected map[int]string, force bool) ModeRaise {
	var out ModeRaise
	if current != VLANModeDisabled {
		return out // rien a wake, et rien a resserrer : voir ci-dessus
	}
	if force {
		out.Mode, out.Change = VLANModeEnabled, true
		return out
	}
	if !linked {
		out.Mode, out.Change = VLANModeEnabled, true
		return out
	}

	// Le port porte un lien : il peut porter la session. On ne resserre que si tout ce
	// qui doit continuer de sortir de ce port en sortira.
	if effectivePVID > 0 && effectivePVID != implicitVLAN && !members[effectivePVID] {
		out.Refused = fmt.Sprintf(
			"%s : filtrage VLAN laisse DISABLED (/fwd.b « %s » = disabled). Son VLAN "+
				"natif est %d et il n'est pas membre de ce VLAN dans la table : activer le "+
				"filtrage a la sortie ferait tomber ce trafic, sur un port qui porte un lien "+
				"ACTIF. Consequence a connaitre : l'appartenance et le VLAN natif de ce port "+
				"sont written mais SANS EFFET tant que ce mode reste a disabled.",
			port, FieldVLANMode, effectivePVID)
		return out
	}
	var vlans = sortedKeysInt(protected)
	var v int
	for _, v = range vlans {
		if v == implicitVLAN || members[v] {
			continue
		}
		out.Refused = fmt.Sprintf(
			"%s : filtrage VLAN laisse DISABLED (/fwd.b « %s » = disabled). Il porte un "+
				"lien ACTIF et n'est pas membre du VLAN de management %d (%s) : activer le "+
				"filtrage a la sortie couperait l'administration si la session passe par ce "+
				"port, et ce model n'a ni SSH, ni Telnet, ni serial port. L'appartenance "+
				"written sur ce port reste donc SANS EFFET.",
			port, FieldVLANMode, v, protected[v])
		return out
	}
	out.Mode, out.Change = VLANModeEnabled, true
	return out
}

// receivePolicy dit pourquoi ce connecteur does not write NI `vlni` NI `fvid`.
//
// Ce n'est pas une capability absente de la plateforme — les deux champs existent,
// sont inscriptibles, et sont already renvoyes a the device a chaque pose avec leur
// valeur lue. C'est un refus to write, et il vaut d'be enonce rather que laisse au
// silence.
//
//	`vlni` (« VLAN Receive ») — le resserrer est ce qui rend un access port
//	  « propre » : « only untagged » lui fait jeter les trames tagged. Mais le model
//	  standard de meshcraft ne dit PAS qu'un access port refuse le tagged, il dit quel
//	  VLAN sort non etiquete ; et sur cette plateforme l'administration peut arriver
//	  TAGGED sur n'importe quel port, y compris un access port (c'est le cas de
//	  l'reference device, dont la session arrive tagged en 99 sur une cage au
//	  VLAN natif 1). Poser « only untagged » sur un access port est donc une way
//	  d'shut off l'administration que rien dans l'intention ne laisserait prevoir.
//	`fvid` (« Force VLAN ID ») — il n'a aucun equivalent dans le model standard : il
//	  range TOUT le trafic entrant dans le VLAN natif, tags comprises. Aucune
//	  intention de meshcraft ne demande cela, donc aucune write ne doit le poser —
//	  ni, symetriquement, le retirer a un exploitant qui l'a voulu.
//
// Les deux sont en revanche LUS et flagged quand ils contredisent le role du port
// (cf. [vlanAnomalies]) : ce qu'on refuse to write, on le dit au moins a voix haute.
func receivePolicy() string {
	return "ce connecteur does not write ni « VLAN Receive » (/fwd.b " + FieldVLANReceive +
		") ni « Force VLAN ID » (/fwd.b " + FieldForceVLAN + ") : les resserrer jette du " +
		"trafic, et le model standard n'exprime pas ce qu'il faudrait pour le decider"
}

// --- La lecture : ce que trois champs par port disent et que state taisait ---

// vlanAnomalies rend, en phrases pour un exploitant, les configurations VLAN
// PRESENT ET CONTRADICTOIRES que la vue standard ne sait pas exprimer.
//
// Toutes sont des etats qu'un CSS accepte sans rien signaler, et chacune fait qu'un
// UI montrant le VLAN natif et l'appartenance d'un port DESCRIBES AUTRE CHOSE que ce
// que le port fait du trafic. C'est le same role que la note « filtrage disabled »
// already rendue par la lecture state, etendu aux trois champs qu'elle ne regardait
// pas.
//
// Fonction PURE, verifiable sur les captures de testdata/.
//
// L'ORDRE VA DU PLUS GRAVE AU PLUS ANECDOTIQUE, parce qu'une liste de notes se lit du
// haut : un port qui force tout son trafic dans un VLAN ne fait rien de ce qui est
// affiche pour lui, un PVID sans appartenance ne casse qu'un sens de circulation.
func vlanAnomalies(vlanRows []*Page, fwd *Page, f Facade) []string {
	var out []string
	var ports = f.Ports()

	var dvid, hasDvid = fwd.Nums(FieldDefaultVLAN)
	if !hasDvid {
		return out
	}
	var modes, hasModes = fwd.Nums(FieldVLANMode)
	var recv, hasRecv = fwd.Nums(FieldVLANReceive)

	// Les listes sont construites dans l'ordre de la FACADE : un tri alphabetique
	// placerait Port10 avant Port2, et une note qui enumerates vingt-quatre ports dans cet
	// ordre est illisible, donc equivalente a ne rien dire.
	var forced, orphans, unreachable, accessTaggedOnly, trunkUntaggedOnly, strictOrphan []string

	var i int
	for i = 0; i < ports && i < len(dvid); i++ {
		var name = f.Name(i)
		var pvid = int(dvid[i])

		var isForced, forcedOK = fwd.Bit(FieldForceVLAN, i)
		if forcedOK && isForced {
			forced = append(forced, name)
		}

		// L'appartenance du port a son propre VLAN natif. Le VLAN 1 n'a pas de ligne
		// dans la table et n'en a pas besoin (cf. implicitVLAN) : il ne compte donc ni
		// comme absent, ni comme orphelin.
		var row = rowFor(vlanRows, pvid)
		var member bool
		if pvid > 0 && pvid != implicitVLAN {
			if row == nil {
				unreachable = append(unreachable, name)
			} else {
				var ok bool
				member, ok = row.Bit(FieldVLANMembers, i)
				if ok && !member {
					orphans = append(orphans, name)
				}
			}
		}

		// Le role du port, tel que la lecture state le deduira : un port qui
		// transporte des VLAN tagged est un trunk, les autres sont des access.
		var trunk = carriesTagged(vlanRows, i, pvid)
		if hasRecv && i < len(recv) {
			switch recv[i] {
			case 1: // only tagged
				if !trunk {
					accessTaggedOnly = append(accessTaggedOnly, name)
				}
			case 2: // only untagged
				if trunk {
					trunkUntaggedOnly = append(trunkUntaggedOnly, name)
				}
			}
		}

		if hasModes && i < len(modes) && modes[i] == VLANModeStrict &&
			pvid > 0 && pvid != implicitVLAN && (row == nil || !member) {
			strictOrphan = append(strictOrphan, name)
		}
	}

	if len(forced) > 0 {
		out = append(out, "FORCE VLAN ID actif sur "+joinComma(forced)+" (/fwd.b « "+
			FieldForceVLAN+" ») : ces ports rangent TOUT leur trafic entrant dans leur "+
			"VLAN natif, tags comprises — une trame tagged en 30 qui entre there "+
			"devient du VLAN natif. Ce ne sont donc PAS des trunks, quoi que la table des "+
			"VLAN dise de leur appartenance, et la liste des VLAN tagged affichee pour eux "+
			"ne describes rien de ce qui circule. meshcraft does not write jamais ce champ : il a been "+
			"applied sur the device.")
	}
	if len(strictOrphan) > 0 {
		out = append(out, "FILTRAGE STRICT SANS APPARTENANCE sur "+joinComma(strictOrphan)+
			" (/fwd.b « "+FieldVLANMode+" » = strict) : le mode strict control "+
			"l'appartenance A L'INGRESS, et ces ports ne sont pas membres de leur propre "+
			"VLAN natif. Leur trafic untagged est donc jete from la receive — le port est "+
			"configure, actif, et muet.")
	}
	if len(unreachable) > 0 {
		out = append(out, "VLAN NATIF ABSENT DE LA TABLE sur "+joinComma(unreachable)+
			" (/fwd.b « "+FieldDefaultVLAN+" ») : ces ports rangent leur trafic untagged "+
			"dans un VLAN qui n'a aucune ligne dans /vlan.b, donc aucun membre. Rien ne "+
			"peut en sortir vers un autre port. A ne pas confondre avec le VLAN 1, qui est "+
			"le default implicite du hardware et n'a legitimately pas de ligne.")
	}
	if len(orphans) > 0 {
		out = append(out, "PVID SANS APPARTENANCE sur "+joinComma(orphans)+" : ces ports "+
			"rangent leur trafic untagged dans leur VLAN natif mais ne figurent PAS dans "+
			"la liste des membres de ce VLAN. Sur SwOS, une trame ne sort d'un port que "+
			"s'il est membre du VLAN : ce trafic entre et ne ressort nulle part. SwOS "+
			"accepte cette configuration sans rien signaler ; une pose meshcraft la "+
			"complete en posant les deux.")
	}
	if len(accessTaggedOnly) > 0 {
		out = append(out, "PORT ACCESS QUI N'ACCEPTE QUE LE TAGGED sur "+
			joinComma(accessTaggedOnly)+" (/fwd.b « "+FieldVLANReceive+" » = only tagged) : "+
			"ces ports ne transportent aucun VLAN tagged et jettent pourtant toute trame "+
			"non tagged a ingress. Un poste branche there n'a aucun trafic qui passe. "+
			"meshcraft does not write jamais ce champ.")
	}
	if len(trunkUntaggedOnly) > 0 {
		out = append(out, "TRUNK QUI N'ACCEPTE QUE LE UNTAGGED sur "+
			joinComma(trunkUntaggedOnly)+" (/fwd.b « "+FieldVLANReceive+" » = only "+
			"untagged) : ces ports sont membres de VLAN tagged et jettent pourtant toute "+
			"trame tagged a ingress. Le trunk fonctionne dans un seul sens. meshcraft "+
			"does not write jamais ce champ.")
	}
	return out
}

// carriesTagged dit si le port d'index `i` est membre d'au moins un VLAN autre que
// son VLAN natif — i.e. s'il transporte du tagged, donc si la lecture state
// l'appellera un trunk.
func carriesTagged(vlanRows []*Page, i, pvid int) bool {
	var row *Page
	for _, row = range vlanRows {
		var vid, ok = row.Num(FieldVLANID)
		if !ok || int(vid) == pvid {
			continue
		}
		var member bool
		member, ok = row.Bit(FieldVLANMembers, i)
		if ok && member {
			return true
		}
	}
	return false
}

// sortedKeysInt rend les keys entire d'une carte, triees.
//
// Trie parce que ces keys servent a composer des messages destines a un humain et
// compares d'une execution a l'autre : l'ordre de parcours d'une carte Go varie a
// chaque execution, et ferait changer un refus qui ne change pas.
func sortedKeysInt(m map[int]string) []int {
	var out = make([]int, 0, len(m))
	var k int
	for k = range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

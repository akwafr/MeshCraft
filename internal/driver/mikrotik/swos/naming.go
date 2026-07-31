package swos

import (
	"fmt"
	"strconv"
	"strings"
)

// LE NOM DES PORTS : celui que l'DEVICE emploie, et l'ancien qui reste accepted.
//
// # CE QUI A CHANGED, ET POURQUOI
//
// La first livraison nommait les ports « GE1 »… « GE26 », par emprunt a la gamme
// Instant On dont le connecteur a servi de model. Le CSS326, lui, nomme ses prises
// « Port1 »… « Port24 », « SFP1 », « SFP2 » — c'est ce que /link.b sert dans son champ
// `nm`, c'est ce que son interface web affiche, et c'est donc le nom que l'exploitant
// lit sur les deux ecrans qu'il compare. Un port designe « GE25 » par meshcraft et
// « SFP1 » par le switch est un rapprochement laisse a faire de tete, sur la seule
// data qui decide where une write atterrit.
//
// Le connecteur RouterOS voisin avait already tranche dans ce sens sans avoir a le
// decider : les noms de meshcraft y SONT ceux de the device (`ether3`,
// `sfp-sfpplus1`), parce que cette plateforme les publie. SwOS les publie aussi.
//
// # DEUX FAMILLES, ET C'EST LE POINT DE PRUDENCE DE CE FICHIER
//
// [Facade.Index] reconnait les DEUX conventions :
//
//	« Port12 », « SFP1 »  la famille de l'DEVICE, celle qui est now written.
//	                      Deux compteurs distincts : le cuivre d'un cote, les cages de
//	                      l'autre, chacun a partir de 1 ;
//	« GE12 », « GE25 »    la famille NUMEROTEE de meshcraft, cages comprises et d'un
//	                      seul tenant. C'est celle des intentions already enregistrees en
//	                      base, et de tout UI qui les affiche encore.
//
// Refuser l'ancienne famille aurait fait ignorer EN SILENCE les intentions d'un parc
// already configure — `PlanPorts` range un nom inconnu dans ses `Skipped`, donc une
// configuration de port aurait cesse d'be applied sans qu'aucune erreur ne le dise.
// Sur les deux devices de reference, `devices.ports` est vide et rien n'aurait
// been perdu ; ce n'est pas une raison de construire une traduction qui n'accepte
// qu'un seul sens, puisque la base d'un parc en service, elle, ne l'est pas.
//
// THE DEVICE PASSE EN PREMIER, et l'ordre a une consequence a connaitre : quand la
// front panel a been relue depuis the device, son prefix de groupe vaut « Port », et la
// famille numerotee devient donc « Port1 »… « Port26 ». « Port25 » designe alors la
// first cage par l'ancienne convention, alors que the device l'appelle « SFP1 ».
// Les deux lectures tombent sur le SAME index — 24 — parce que la numerotation
// continue et la front panel decrivent la same front panel ; c'est un hasard heureux, pas une
// garantie, et c'est pourquoi la famille de the device est essayee d'abord.
//
// # CE QUE `nm` NE DEVIENT PAS : UNE CLEF
//
// Le champ `nm` est du TEXTE LIBRE. L'interface de the device le laisse renommer
// port par port, il n'a aucune contrainte d'unicite, et rien ne suit un renommage.
// En faire la clef du model standard donnerait trois defauts d'un coup :
//
//   - deux ports named pareil s'ecraseraient dans les cartes de [State] — une
//     appartenance VLAN affichee pour un port serait celle d'un autre ;
//   - `port_groups` ne sait pas exprimer une liste de noms quelconques : la front panel
//     s'y written en prefix + compte, et c'est elle que l'interface web deplie pour
//     enumerate les prises. Un port named « Uplink » deviendrait invisible a the UI
//     tout en existant dans state lu ;
//   - une intention enregistree cesserait de designer un port au premier renommage
//     fait depuis l'interface du switch.
//
// Ce fichier retient donc la CONVENTION de the device — deux familles, `Port` pour
// le cuivre et `SFP` pour les cages, telles que /link.b les sert par default — et non
// les strings elles-memes. Les strings, elles, restent servies there where elles ne
// decident de rien : [PortTelemetry.DeviceName] pour le diagnostic, et
// [renamedPorts] qui SIGNALE les ports dont le nom observed differe du nom de front panel —
// sans quoi l'ecart entre les deux ecrans reviendrait, a l'envers et sans explication.

const (
	// CopperPortPrefix nomme les prises cuivre. OBSERVED dans `nm` sur le
	// CSS326-24G-2S+ de reference : « Port1 »… « Port24 ».
	CopperPortPrefix = "Port"
	// SFPPortPrefix nomme les cages optiques, NUMEROTEES A PART et repartant de 1.
	// OBSERVED : « SFP1 », « SFP2 » pour les ports d'index 24 et 25.
	//
	// Le prefix ne distingue pas SFP de SFP+ : the device ne le fait pas non plus
	// (ses deux cages 10 gigabits s'appellent « SFP1 » et « SFP2 »), et le rate de la
	// front panel dit already ce que la cage accepte.
	SFPPortPrefix = "SFP"
)

// Facade est la front panel NOMMEE d'un device SwOS : sa geometry, plus les deux
// conventions de nommage qui traduisent un nom en index.
//
// UN TYPE ET NON DEUX PARAMETRES, et c'est la lecon du commentaire de [PortsInput] :
// « un parameter positionnel ajoute au milieu d'une liste est exactement la faute qui
// fait passer `ports` there where le code attend `prefix` ». La geometry et le nommage sont
// now indissociables — savoir where commencent les cages est ce qui decide si
// l'index 24 s'appelle « SFP1 » ou « Port25 » — et les separate laisserait deux valeurs
// dont l'une peut mentir sur l'autre.
type Facade struct {
	ports    int
	sfp      int
	sfpFirst int
	// legacy est le prefix de la famille numerotee, tel que la CIBLE le porte
	// (cf. Admin.portPrefix). Il ne nomme plus rien : il ne sert only RELIRE.
	legacy string
}

// NewFacade construit la front panel named d'un device.
//
// `legacyPrefix` est le prefix de la famille numerotee a accepter en lecture. Vide,
// c'est [DefaultPortPrefix] qui s'applique — la famille historique du projet.
func NewFacade(ports, sfp, sfpFirst int, legacyPrefix string) Facade {
	if legacyPrefix == "" {
		legacyPrefix = DefaultPortPrefix
	}
	return Facade{ports: ports, sfp: sfp, sfpFirst: sfpFirst, legacy: legacyPrefix}
}

// Ports rend le nombre de ports de la front panel.
func (f Facade) Ports() int { return f.ports }

// SFPFirst rend l'index de la first cage optique.
func (f Facade) SFPFirst() int { return f.sfpFirst }

// isCage dit si l'index tombe dans la PLAGE de cages declared par /link.b.
//
// Une plage et non « les derniers ports » : `sfpo` place la first cage where il veut,
// et l'reference device la met a la fin (`sfpo:0x18` pour 26 ports) sans que
// rien ne l'y oblige. Un model qui placerait ses cages at the top verrait donc ses
// prises cuivre numerotees a partir de 1 quand same, ce qui est le comportement
// expected de deux compteurs independants.
func (f Facade) isCage(i int) bool {
	return f.sfp > 0 && i >= f.sfpFirst && i < f.sfpFirst+f.sfp
}

// Name rend le nom de front panel du port d'index `i` (base 0).
//
// Les deux compteurs sont independants : le cuivre compte les ports qui NE SONT PAS
// des cages, dans l'ordre de la front panel, les cages comptent les cages. C'est ce que
// l'reference device sert dans `nm`, et [renamedPorts] le verifies sur la
// real capture rather que sur cette phrase.
func (f Facade) Name(i int) string {
	if i < 0 || i >= f.ports {
		// Un index hors front panel ne se nomme pas : le nommer quand same fabriquerait un
		// port que the device n'a pas, et le silence d'un nom vide ferait chercher
		// l'erreur dans la page rather que dans l'appel.
		return fmt.Sprintf("port#%d(hors front panel de %d ports)", i, f.ports)
	}
	if f.isCage(i) {
		return SFPPortPrefix + strconv.Itoa(i-f.sfpFirst+1)
	}
	// Le rang du port parmi les prises qui ne sont pas des cages.
	var rank = i + 1
	if f.sfp > 0 && i >= f.sfpFirst+f.sfp {
		rank = i + 1 - f.sfp
	}
	return CopperPortPrefix + strconv.Itoa(rank)
}

// Names rend les noms de front panel, dans l'ordre des index.
func (f Facade) Names() []string {
	var out = make([]string, 0, f.ports)
	var i int
	for i = 0; i < f.ports; i++ {
		out = append(out, f.Name(i))
	}
	return out
}

// Index traduit un nom de port en index SwOS (base 0), dans l'une ou l'autre famille.
//
// Un nom qui ne suit aucune des deux conventions, ou qui designe un port que
// the device n'a pas, est REFUSED rather que devine : envoyer un number de port faux
// est une write au mauvais endroit, et sur cette plateforme, celle qui coupe
// l'uplink.
func (f Facade) Index(name string) (int, bool) {
	var i, ok = f.deviceIndex(name)
	if ok {
		return i, true
	}
	return f.legacyIndex(name)
}

// deviceIndex lit la famille de l'DEVICE : « Port12 », « SFP1 ».
func (f Facade) deviceIndex(name string) (int, bool) {
	var n, ok = suffixNum(name, SFPPortPrefix)
	if ok {
		if n > f.sfp {
			return 0, false
		}
		return f.sfpFirst + n - 1, true
	}
	if n, ok = suffixNum(name, CopperPortPrefix); !ok {
		return 0, false
	}
	// Le n-ieme port qui n'est pas une cage. Compte rather que calcule : c'est la same
	// rule que Name, prise dans l'autre sens, et une formule dupliquee est une formule
	// qui divergera.
	var seen int
	var i int
	for i = 0; i < f.ports; i++ {
		if f.isCage(i) {
			continue
		}
		seen++
		if seen == n {
			return i, true
		}
	}
	return 0, false
}

// legacyIndex lit la famille NUMEROTEE de meshcraft : « GE12 », cages comprises.
func (f Facade) legacyIndex(name string) (int, bool) {
	var n, ok = suffixNum(name, f.legacy)
	if !ok || n > f.ports {
		return 0, false
	}
	return n - 1, true
}

// suffixNum lit le number qui suit `prefix` dans `name`. Faux from que la forme n'est
// pas exactement « prefix + entier positif ».
//
// La comparaison est SENSIBLE A LA CASSE, comme elle l'was avant ce lot : les deux
// familles sont engendrees par du code — l'une par the device, l'autre par
// `port_groups` — et accepter « port3 » ferait accepter aussi bien « pOrT3 », c'est-a-
// dire tolerer une saisie a la main there where la seule source legitime est une machine.
func suffixNum(name, prefix string) (int, bool) {
	if prefix == "" || !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	var num = name[len(prefix):]
	if num == "" {
		return 0, false
	}
	// CHIFFRES SEULEMENT, et le control est explicite parce que `strconv.Atoi` est plus
	// tolerant qu'il n'y parait : il accepte le signe, donc « Port+1 » et « Port-1 »
	// rendraient 1 et −1. Le premier designerait le port 1 sous un nom que rien
	// n'engendre ; ce qui compte n'est pas ce cas-there mais la rule, qui est de ne
	// traduire que ce qu'on reconnait.
	var i int
	for i = 0; i < len(num); i++ {
		if num[i] < '0' || num[i] > '9' {
			return 0, false
		}
	}
	var n, err = strconv.Atoi(num)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// renamedPorts rend, en phrases pour un exploitant, les ports dont le nom PORTE PAR
// THE DEVICE differe du nom de front panel de meshcraft.
//
// Pourquoi le dire : le nom de front panel est une CONVENTION (cf. l'encadre at the top de
// fichier), donc un port renomme « Uplink » dans l'interface du switch continue de
// s'afficher « Port5 » dans meshcraft. C'est le bon choix pour la clef, et c'est
// exactement l'ecart entre deux ecrans que ce fichier existe pour supprimer : le
// taire ferait chercher « Uplink » dans une liste qui ne le contient pas.
//
// Vide quand tous les noms concordent — ce qui est le cas de the device de
// reference, et ce que le test sur la real capture verifies.
//
// Fonction PURE, verifiable sur testdata/link.b.
func renamedPorts(link *Page, f Facade) []string {
	var names, ok = link.Texts(FieldPortName)
	if !ok {
		// Champ absent : rien a comparer, et surtout rien a signaler. Ce n'est pas une
		// panne — un firmware qui ne sert pas `nm` laisse simplement la front panel seule a
		// nommer, ce qu'elle sait faire.
		return nil
	}
	var out []string
	var i int
	for i = 0; i < f.ports && i < len(names); i++ {
		var got = strings.TrimSpace(names[i])
		if got == "" || got == f.Name(i) {
			continue
		}
		out = append(out, f.Name(i)+" est named « "+got+" » sur the device")
	}
	return out
}

// renamedNote assemble la note state, ou rend la string vide s'il n'y a rien a dire.
func renamedNote(link *Page, f Facade) string {
	var diffs = renamedPorts(link, f)
	if len(diffs) == 0 {
		return ""
	}
	return "NOMS DE PORTS RENOMMES SUR THE DEVICE : " + joinComma(diffs) + ". meshcraft " +
		"garde les noms de front panel (« " + CopperPortPrefix + "1 », « " + SFPPortPrefix +
		"1 »), qui sont ceux que SwOS emploie par default : le champ « " + FieldPortName +
		" » de /link.b est du texte libre, sans unicite, et en faire la clef d'un port " +
		"ferait perdre une intention au premier renommage. Ce qui est affiche designe " +
		"donc bien la prise physique ; c'est son tag locale qui differe."
}

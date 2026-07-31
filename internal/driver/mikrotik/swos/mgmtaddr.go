package swos

import (
	"context"
	"fmt"

	"meshcraft/internal/driver"
)

// Lecture de l'adressage de management d'un SwOS : par quel moyen cet device a
// obtenu l'adresse a laquelle meshcraft lui parle.
//
// Writes live in mgmtwrite.go ([driver.MgmtAddrWriter], [driver.MgmtJumper]).
// This file only reads /sys.b.
//
// # Ce que le observed establishes, et qui change l'affichage du produit
//
// Le jeu de champs INSCRIPTIBLE de /sys.b est enumerated par la sauvegarde de
// configuration de the device (testdata/backup.swb, CSS326-24G-2S+, SwOS 2.17), donc
// exhaustif — c'est la same source que celle de pages.go, et c'est ce qui autorise a en
// tirer une ABSENCE rather qu'un « pas trouve » :
//
//	sys.b:{id, wdt, dsc, pdsc, ivl, alla, allm, avln, allp, mgmt, prio, cost,
//	       frmc, poe, igmp, igmq, igfl, igve, ip, dtrp, ainf, iptp}
//
// Il y a `ip` et `iptp`. Il n'y a NI PASSERELLE, NI RESOLVEUR DNS, dans aucun onglet,
// et l'interface web complete de la plateforme tient en un seul fichier qui definit
// tous ses champs — enumeration exhausts la question, exactement comme elle l'a exhausted
// pour le destinataire de traps.
//
// La consequence est une contrainte d'exploitation, et elle merite d'be dite
// there where elle se lit : sans passerelle, les sorties hors sous-network
// (traps, DNS) n'ont pas de route ici ; l'administration entrante peut encore
// repondre en L2. C'est ce que `Capabilities().Mgmt.Gateway = false` publie, et
// c'est ce qui doit empecher un UI d'offrir un champ « passerelle » sur cette
// gamme : faire saisir une valeur qui ne serait pas written fait conclure a
// l'exploitant que meshcraft n'a pas written ce qu'il avait requested.

// Ce qui fera echouer la COMPILATION le jour where le contrat bouge — same raison que les
// assertions de contract.go : une assertion de type a l'execution echouerait, elle, en
// silence et sur le terrain.
var _ driver.MgmtAddrReader = (*Admin)(nil)

// ReadMgmtAddressing dit comment ce switch a obtenu son adresse d'administration.
//
// UNE SEULE PAGE est lue, /sys.b, et c'est la moins chere de la plateforme (780 octets)
// — celle que l'authentification lit already pour eprouver les identifiants. Sur une
// plateforme dont chaque page costs deux requests HTTP (Digest) et dont les access sont
// serialises, un UI qui affiche trois champs ne doit pas en couter quatre.
func (a *Admin) ReadMgmtAddressing(ctx context.Context) (driver.MgmtAddrState, error) {
	var sys, err = a.Get(ctx, PageSys)
	if err != nil {
		return driver.MgmtAddrState{}, fmt.Errorf("%s illisible : %w", PageSys, err)
	}
	var out = MgmtAddrOf(sys)
	a.step(true, "adressage de management relu",
		fmt.Sprintf("%s : mode « %s », adresse %s", PageSys,
			modeLabel(out.Mode), addrLabel(out.Address)))
	return out, nil
}

// MgmtAddrOf traduit /sys.b en adressage de management observe.
//
// PURE, comme PlanPorts et pour la same raison : elle est verifiable sur la capture
// reelle de the device (testdata/sys.b) rather que sur du hardware, et c'est elle qui
// decide ce que the UI affirmera sur la configuration d'un switch en service.
//
// TROIS DECISIONS, chacune contre un mensonge precise.
//
// 1. L'ADRESSE RENDUE EST `cip`, CELLE EN COURS, ET NON `ip`.
//
// Les deux champs coexistent et ne veulent pas dire la same chose : `ip` est l'adresse
// CONFIGUREE, `cip` celle actually portee. En statique elles sont egales ; en DHCP,
// `ip` garde l'adresse de repli et `cip` porte le bail. Rendre `ip` ferait afficher, sur
// un device en DHCP, une adresse a laquelle il ne repond pas — et c'est le genre de
// valeur qu'un exploitant recopie dans un navigateur avant de conclure a une panne.
// Le repli sur `ip` n'a lieu que si `cip` manque, et il est DIT dans les notes.
//
// 2. UN `iptp` INCONNU NE SE TRADUIT PAS EN « STATIQUE ».
//
// Trois valeurs sont observed dans le selecteur « Address Acquisition » de l'interface
// (0 « DHCP with fallback », 1 « static », 2 « DHCP only »). Une quatrieme, sur un
// firmware plus recent, rendrait un mode VIDE et une note — et non le mode le plus
// probable. « On ne sait pas » est une response exacte ; deviner « statique » sur un
// device en bail ferait conserver comme intentionnelle une adresse qui peut changer
// a tout moment.
//
// 3. « DHCP WITH FALLBACK » EST RENDU COMME DU DHCP, et le repli est dit en note.
//
// C'est bien du DHCP du point de vue de la question applied — l'adresse n'est pas decidee
// par l'exploitant — mais la consequence pratique est particular et vaut d'be
// affichee : cet device A une adresse same quand aucun serveur ne repond, ce qui
// fait chercher un device muet there where il repond, a une autre adresse
// (cf. MgmtCapabilities.DHCPFallback, same trap observed sur l'Instant On).
func MgmtAddrOf(sys *Page) driver.MgmtAddrState {
	var out driver.MgmtAddrState

	var mode, hasMode = sys.Num(FieldAddrMode)
	if hasMode {
		switch mode {
		case AddrStatic:
			out.Mode = driver.MgmtAddrModeStatic
		case AddrDHCPOnly:
			out.Mode = driver.MgmtAddrModeDHCP
		case AddrDHCPFallback:
			out.Mode = driver.MgmtAddrModeDHCP
			out.Notes = append(out.Notes, "mode « DHCP with fallback » : cet device "+
				"porte une adresse de repli same quand aucun serveur DHCP ne repond. Un "+
				"CSS326 qu'on croit muet peut donc repondre ailleurs, sur son adresse de "+
				"repli, et non a l'adresse attendue.")
		default:
			out.Notes = append(out.Notes, fmt.Sprintf(
				"mode d'acquisition non reconnu (%s = 0x%02x) : ce firmware propose une "+
					"valeur que le connecteur n'a pas observed. meshcraft ne conclut donc RIEN "+
					"sur l'origine de l'adresse rather que de supposer la plus probable.",
				FieldAddrMode, mode))
		}
	} else {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"%s ne sert pas le champ « %s » : l'origine de l'adresse n'est pas lisible sur "+
				"ce firmware.", PageSys, FieldAddrMode))
	}

	var cur, hasCur = sys.Num(FieldCurrentIP)
	var cfg, hasCfg = sys.Num(FieldIP)
	switch {
	case hasCur && cur != 0:
		out.Address = DecodeIPv4(cur)
		// L'ecart entre configuree et en cours n'est une anomalie que si the device
		// se dit statique — sinon c'est le fonctionnement normal du DHCP.
		if hasCfg && cfg != cur && out.Mode == driver.MgmtAddrModeStatic {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"the device se declares en adresse statique %s mais repond en %s. L'un des "+
					"deux champs a been changed sans que l'autre suive : a verify avant toute "+
					"write sur cette page.", DecodeIPv4(cfg), DecodeIPv4(cur)))
		}
	case hasCfg && cfg != 0:
		out.Address = DecodeIPv4(cfg)
		out.Notes = append(out.Notes, fmt.Sprintf(
			"adresse EN COURS (« %s ») absente de la page : c'est l'adresse CONFIGUREE "+
				"(« %s ») qui est affichee. Sur un device en DHCP, les deux peuvent "+
				"differer.", FieldCurrentIP, FieldIP))
	}

	// Absence of gateway/DNS is a PLATFORM CAPABILITY (Gateway/DNS/Routed/IPARP
	// on MgmtCapabilities), not a per-read note: notes are localized in the UI
	// from caps, never stored as French/English prose in the observation.
	// Aucune interface named : SwOS n'attache pas son adresse a une interface. Ce qui
	// tient lieu de portee est « Allow From VLAN » (`avln`), qui est le VLAN de
	// management et se lit already dans State.MgmtVLAN — le remettre ici sous le nom
	// d'« interface » inviterait a le comparer a un nom d'interface RouterOS, qui est
	// une tout autre chose.
	return out
}

// modeLabel et addrLabel n'existent que pour le transcript, et pour qu'une ligne de
// transcript ne dise jamais « mode «  », adresse «  » » — un journal qui affiche du vide
// se lit comme une panne du connecteur, there where c'est the device qui n'a rien dit.
func modeLabel(mode string) string {
	switch mode {
	case driver.MgmtAddrModeStatic:
		return "statique"
	case driver.MgmtAddrModeDHCP:
		return "DHCP"
	}
	return "non observed"
}

func addrLabel(addr string) string {
	if addr == "" {
		return "inconnue"
	}
	return addr
}

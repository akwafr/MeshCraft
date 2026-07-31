package swos

import (
	"context"
	"fmt"
	"sort"
	"strconv"
)

// Lecture de state ACTUALLY applique sur un switch SwOS.
//
// C'est l'etape « Read » du pipeline de reconciliation : sans elle, meshcraft ne
// peut pas savoir si ce qu'il croit est ce qui est, et « derive de configuration »
// reste un mot.
//
// DEUX PAGES suffisent, et elles disent des choses complementaires :
//
//	GET /vlan.b → la TABLE des VLAN : un identifiant, un nom, et un masque des
//	              ports MEMBRES.
//	GET /fwd.b  → par PORT : son mode VLAN (`vlan`), ce qu'il accepte a ingress
//	              (`vlni`), et son « Default VLAN ID » (`dvid`).
//
// LE MODEL VLAN DE SwOS, ET LA TRADUCTION QU'IL IMPOSE
//
// SwOS ne dit nulle part « ce port est tagged dans ce VLAN ». Il n'y a AUCUN champ
// pour cela — ni dans la table des VLAN, ni par port, ni dans la sauvegarde de
// configuration complete. Ce que la plateforme porte est plus econome :
//
//   - la table des VLAN dit QUI est membre de quoi ;
//   - chaque port a UN « Default VLAN ID », qui est son VLAN natif.
//
// La rule d'tagging s'en deduit, et c'est la seule qui rende ces deux data
// suffisantes : une trame sort d'un port SANS tag si son VLAN est le
// « Default VLAN ID » de ce port, et AVEC tag sinon. C'est ce que dit la
// documentation de MikroTik pour ce champ, et c'est ce que confirme l'absence de
// tout autre reglage — l'interface de the device, qui expose absolument tous ses
// champs, n'offre rien d'autre.
//
// Donc, pour un port d'index i :
//
//	PVID(i)   = dvid[i]
//	Tague(i)  = { vid de la table | i est membre de vid } \ { dvid[i] }
//	Mode(i)   = « trunk » si Tague(i) n'est pas vide, « access » sinon
//
// CE QUI CORROBORE CETTE TRADUCTION SUR THE DEVICE DE REFERENCE, et ce n'est pas
// une petite chose : appliquee telle quelle au CSS326-24G-2S+ de reference, elle
// dit que la cage SFP2 — le seul port qui porte un lien — est un trunk de VLAN natif
// 1 transportant les VLAN 99 et 100 tagged. Or cet device est administre en
// VLAN 99 (`avln:0x0063`) et il REPOND. Sa table d'adresses apprises montre des
// hotes en VLAN 99 sur ce same port. La traduction predit donc exactement l'unique
// fait de commutation qu'on puisse observer de l'exterieur : que le VLAN 99 arrive
// tagged par ce port. Une rule d'tagging inversee predirait le contraire, et
// the device serait muet.
//
// Cela ne remplace pas une injection de trafic — c'est written ici pour qu'on sache
// ce qui est established et ce qui ne l'est pas.

// `VLAN`, `PortState` et `State` ne sont plus definis ici : ce sont des ALIAS des
// types du contrat (cf. contract.go). Ce paquet en portait des copies, et son
// commentaire disait already pourquoi — « same forme que celle du connecteur Instant On,
// pour que l'API n'ait qu'une way de present  une pose », « c'est ce qui permet a
// la couche de reconciliation de comparer deux plateformes sans les distinguer ».
// L'alias tient cette promesse au lieu de la repeter : ce que ce fichier construit EST
// ce que la couche API relit, sans conversion, donc sans champ perdu le jour where le
// contrat en gagne un.
//
// Ce que les commentaires de ces copies portaient de propre a SwOS a been REPRIS dans
// le contrat, a l'endroit qui en avait besoin — voir [driver.State.MgmtVLAN], dont la
// documentation dit explicitement que zero n'est pas « inconnu » ici mais
// « administration accepted depuis n'importe quel VLAN », et que la protection se
// reporte alors sur le seul etat des liens (cf. ApplyPorts).
//
// UN SEUL champ n'a pas pu suivre, et il vit maintenant ci-dessous : le mode VLAN par
// port. Le contrat ne le porte pas — il n'a rien d'universel — et c'est
// [driver.State.Notes] qui en dit a l'exploitant ce qu'il doit savoir.

// VLANFiltering rend, par port, le mode VLAN que the device APPLIQUE
// (« disabled », « optional », « enabled », « strict », cf. VLANModeLabel).
//
// Propre a cette plateforme, et c'est pour cela que ce n'est pas un champ de state
// standard : « disabled » veut dire que le port ne filtre RIEN, et que le PVID comme
// l'appartenance lus par ailleurs ne decrivent alors pas ce que le port fait
// actually du trafic. Taire cette distinction ferait present  une configuration
// VLAN comme active alors qu'elle est INERTE — et c'est le genre de silence qui fait
// chercher une panne ailleurs pendant une heure.
//
// Ce que la lecture state en fait : une phrase dans [driver.State.Notes], parce que
// l'appelant doit l'AFFICHER et n'a aucune decision a en tirer (cf. stateOf).
type VLANFiltering map[string]string

// Disabled rend, tries, les ports dont le filtrage VLAN est off.
//
// Trie parce que cette liste part dans une note lue par un humain et comparee d'une
// relecture a l'autre : un ordre de parcours de carte Go varie a chaque execution, et
// ferait clignoter une note qui ne change pas.
func (f VLANFiltering) Disabled() []string {
	var out []string
	var name, mode string
	for name, mode = range f {
		if mode == "disabled" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// snapshot est l'ensemble des pages necessary a state, lues en UNE passe.
//
// Il existe pour une raison de COST, et elle est propre a cette plateforme. SwOS
// n'a pas de session : chaque page se paie de DEUX requests HTTP, le challenge
// Digest puis la request authentifiee. Et ce connecteur declares `MaxSessions: 1`,
// donc ses access sont serialises. Une first version relisait /link.b deux fois
// par lecture state, et /vlan.b et /fwd.b une seconde fois avant to write :
// huit requests de trop sur le CPU d'un commutateur access, a chaque passage de
// reconciliation, sur tout le parc. Passer les pages rather que les redemander
// n'est pas une optimisation de style ici — c'est la coherence avec le cost qu'on
// a soi-same documente.
type snapshot struct {
	ports    int
	sfp      int
	sfpFirst int
	// facade est la geometry ET le nommage, indissociables : c'est la position des
	// cages qui decide si l'index 24 s'appelle « SFP1 » ou « Port25 » (cf. Facade).
	facade   Facade
	link     *Page
	fwd      *Page
	sys      *Page
	vlanRows []*Page
}

// readAll rapatrie les quatre pages de state : /link.b, /vlan.b, /fwd.b, /sys.b.
func (a *Admin) readAll(ctx context.Context) (*snapshot, error) {
	var snap = &snapshot{}
	var err error
	snap.link, err = a.Get(ctx, PageLink)
	if err != nil {
		return nil, err
	}
	snap.ports, snap.sfp, snap.sfpFirst, err = geometryOf(snap.link)
	if err != nil {
		a.step(false, "geometry du model illisible", err.Error())
		return nil, err
	}
	// La geometry est mise en cache : les ecritures en ont besoin pour composer le
	// jeu de champs de /fwd.b, et elles ne doivent pas relire /link.b pour l'obtenir.
	a.ports, a.sfp, a.sfpFirst = snap.ports, snap.sfp, snap.sfpFirst
	snap.facade = a.facade()
	a.step(true, "geometry du model lue",
		fmt.Sprintf("%d ports dont %d cage(s) SFP, la first a l'index %d — "+
			"named « %s » … « %s »", snap.ports, snap.sfp, snap.sfpFirst,
			snap.facade.Name(0), snap.facade.Name(snap.ports-1)))

	snap.vlanRows, err = a.GetTable(ctx, PageVLAN)
	if err != nil {
		return nil, err
	}
	snap.fwd, err = a.Get(ctx, PageFwd)
	if err != nil {
		return nil, err
	}
	snap.sys, err = a.Get(ctx, PageSys)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// ReadState relit the device. LECTURE SEULE : rien n'est modifie, ce qui permet
// de la rejouer aussi souvent qu'on veut, y compris sur un parc en production.
//
// C'est l'etape « Read » du pipeline de reconciliation, et la signature est celle du
// contrat : aucun parameter. LES NOMS DE PORTS SONT CEUX DE THE DEVICE — « Port1 »…
// « Port24 », « SFP1 », « SFP2 » — et ils ne viennent donc pas de l'appel : ils
// viennent de la geometry que /link.b sert, par [Facade]. Deux appels sur le same
// access doivent nommer les memes prises, sans quoi la pose would write sur un autre port
// que celui que the UI a montre.
//
// La famille numerotee de meshcraft (« GE25 ») reste ACCEPTED en ingress, et le prefix
// qui la porte vient toujours de la CIBLE (`driver.Target.PortPrefix`, retenue par
// [Driver.Authenticate]). Elle n'est plus jamais written — voir l'encadre de naming.go.
//
// QUATRE PAGES, DEUX REQUETES HTTP CHACUNE : /link.b, /vlan.b, /fwd.b, /sys.b, soit
// huit allers-retours. SwOS n'a pas de session, le Digest se renegocie a chaque fois
// (un 401 qui porte le challenge, puis la request authentifiee) et rien de tout cela
// ne se mutualise. C'est le cost reel d'une lecture state sur cette plateforme, et
// c'est ce qui justifie que readAll les lise en UNE passe pour les partager avec la
// pose (cf. snapshot).
func (a *Admin) ReadState(ctx context.Context) (State, error) {
	var snap, err = a.readAll(ctx)
	if err != nil {
		return State{VLANs: []VLAN{}, Ports: map[string]PortState{}}, err
	}
	return a.stateOf(snap)
}

// stateOf reconstruit state depuis un snapshot et consigne ce qui a been compris.
func (a *Admin) stateOf(snap *snapshot) (State, error) {
	var out = State{VLANs: []VLAN{}, Ports: map[string]PortState{}}

	var err error
	out.VLANs, err = vlansFrom(snap.vlanRows)
	if err != nil {
		a.step(false, "table des VLAN illisible", err.Error())
		return out, err
	}
	a.step(true, "VLAN lus sur the device",
		fmt.Sprintf("%d VLAN(s) : %s", len(out.VLANs), vlanSummary(out.VLANs)))

	var filtering VLANFiltering
	out.Ports, filtering, err = portsFrom(snap.vlanRows, snap.fwd, snap.facade)
	if err != nil {
		a.step(false, "appartenance des ports illisible", err.Error())
		return out, err
	}
	a.step(true, "appartenance VLAN des ports reconstruite",
		fmt.Sprintf("%d port(s)", len(out.Ports)))

	// LA NOTE OBLIGATOIRE DE CETTE PLATEFORME : un filtrage VLAN off rend la
	// configuration lue INERTE.
	//
	// Elle va dans State.Notes et pas seulement au transcript, et la difference
	// compte : le transcript raconte ce qu'une operation a fait, les notes accompagnent
	// l'ETAT lui-same — donc the UI qui affiche le VLAN natif et l'appartenance de ces
	// ports. Sans cette phrase, cet UI presenterait une segmentation en place there where
	// the device ne filtre rien : le PVID et la liste des membres sont bien
	// configures, et le port les ignore. C'est un fait qu'aucun champ du model standard
	// ne sait dire, et le taire ferait chercher la panne ailleurs pendant une heure.
	//
	// Une PHRASE et pas un code : l'appelant l'affiche telle quelle et n'en tire aucune
	// decision (le contrat est explicite there-dessus). Si une decision devait be prise,
	// ce serait a ce connecteur de la prendre — et il refuserait.
	var inertes = filtering.Disabled()
	if len(inertes) > 0 {
		var note = "Filtrage VLAN DISABLED sur " + joinComma(inertes) + " (/fwd.b " +
			"« " + FieldVLANMode + " » = disabled) : sur ces ports, le VLAN natif et " +
			"l'appartenance ci-dessus sont bien configures mais SANS EFFET sur le trafic — " +
			"le port ne filtre rien et laisse tout passer. Ce qui est affiche pour eux est " +
			"une intention, pas un comportement. Une pose meshcraft REVEILLE ce filtrage " +
			"quand elle peut le faire sans risquer access (cf. planModeRaise)."
		out.Notes = append(out.Notes, note)
		a.step(true, "filtrage VLAN disabled sur certains ports", note)
	}

	// LES TROIS AUTRES CHAMPS PAR PORT, et ce qu'ils contredisent.
	//
	// Le mode VLAN n'est pas le seul reglage capable de faire mentir l'appartenance
	// affichee : « Force VLAN ID » range tout le trafic entrant dans le VLAN natif,
	// « VLAN Receive » peut jeter le tagged ou le untagged a ingress, et un PVID sans
	// appartenance ne mene nulle part. Ces etats sont accepted par the device sans
	// aucun signalement, et chacun fait qu'un UI montrant le VLAN natif et la liste
	// des VLAN tagged DESCRIBES AUTRE CHOSE que ce que le port fait du trafic.
	//
	// Meme nature que la note ci-dessus, same destination : [driver.State.Notes], des
	// phrases que l'appelant AFFICHE sans en tirer de decision.
	var anomalies = vlanAnomalies(snap.vlanRows, snap.fwd, snap.facade)
	if len(anomalies) > 0 {
		out.Notes = append(out.Notes, anomalies...)
		var note string
		for _, note = range anomalies {
			a.step(true, "configuration VLAN contradictoire observed", note)
		}
	}

	// Le VLAN de management, DEPUIS the device. La seule source qui dise par where
	// passe la session en cours.
	var avln, hasAvln = snap.sys.Num(FieldAllowVLAN)
	if !hasAvln {
		// Champ absent : on ne suppose pas. Sans VLAN de management connu, writing
		// se rabattra sur la protection par le lien.
		var note = "Le VLAN de management de the device n'a pas pu be lu : ce " +
			"firmware ne sert pas le champ « " + FieldAllowVLAN + " » de /sys.b. Une pose " +
			"restera possible, mais elle se protegera sur le seul etat des liens — donc " +
			"elle epargnera TOUT port actif."
		out.Notes = append(out.Notes, note)
		a.step(false, "VLAN de management inconnu", note)
	} else {
		out.MgmtVLAN = int(avln)
		if avln == 0 {
			// Zero est un etat LEGITIME ici, pas une ignorance — et c'est celui d'un
			// device sorti d'usine. La note existe parce que le contrat interdit a
			// l'appelant d'interpreter ce zero lui-same : sur l'Instant On il veut dire
			// « inconnu » et fait refuser writing, ici il veut dire « depuis n'importe
			// quel VLAN » et writing reste possible.
			var note = "The device accepte l'administration depuis N'IMPORTE QUEL VLAN " +
				"(/sys.b « " + FieldAllowVLAN + " » = 0), ce qui est son factory state : il n'y " +
				"a aucun VLAN de management a epargner, et une pose se protegera donc sur le " +
				"seul etat des liens — tout port actif sera epargne."
			out.Notes = append(out.Notes, note)
			a.step(true, "administration accepted depuis n'importe quel VLAN", note)
		} else {
			a.step(true, "VLAN de management de the device",
				"VLAN "+strconv.Itoa(out.MgmtVLAN))
		}
	}

	out.LinkUp, err = linksFrom(snap.link, snap.facade)
	if err != nil {
		a.step(false, "etat des liens illisible", err.Error())
		return out, err
	}
	a.step(true, "etat des liens lu",
		fmt.Sprintf("%d port(s) avec lien", len(out.LinkUp)))

	// L'ETIQUETTE LOCALE D'UN PORT, quand elle differe du nom de front panel. Meme nature
	// que les notes ci-dessus — une phrase a afficher, aucune decision a en tirer — et
	// elle existe pour la raison qui a fait adopter les noms de the device : que la
	// prise dont parle meshcraft et celle que l'exploitant voit dans SwOS soient la
	// same. Un port renomme sur the device est le seul cas where les deux ecrans
	// divergent encore, et cette phrase est ce qui l'explique (cf. naming.go).
	var renamed = renamedNote(snap.link, snap.facade)
	if renamed != "" {
		out.Notes = append(out.Notes, renamed)
		a.step(true, "ports renommes sur the device", renamed)
	}
	return out, nil
}

// vlansFrom lit la table des VLAN. Fonction pure, donc verifiable sur une capture.
//
// Une ligne sans identifiant n'est pas ignored en silence : c'est une table qu'on ne
// sait pas lire, et writing qui suivrait reconstruirait la table ENTIRE a partir
// de ce qu'on en a compris — donc en perdant cette ligne. Sur cette plateforme, where
// write une table c'est la remplacer, mieux vaut refuser de lire que de perdre un
// VLAN.
func vlansFrom(rows []*Page) ([]VLAN, error) {
	var out = []VLAN{}
	var i int
	var row *Page
	for i, row = range rows {
		var vid, ok = row.Num(FieldVLANID)
		if !ok {
			return nil, fmt.Errorf("/vlan.b : la ligne %d ne porte pas de champ « %s » — "+
				"table refused rather que lue a half, la rewrite perdrait cette ligne",
				i+1, FieldVLANID)
		}
		// Le nom est facultatif et souvent vide : l'reference device n'en
		// nomme aucun.
		var name, _ = row.Text(FieldVLANName)
		out = append(out, VLAN{ID: int(vid), Name: name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func vlanSummary(vlans []VLAN) string {
	var parts = make([]string, 0, len(vlans))
	var v VLAN
	for _, v = range vlans {
		var s = strconv.Itoa(v.ID)
		if v.Name != "" {
			s += " « " + v.Name + " »"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return "aucun"
	}
	return joinComma(parts)
}

// portsFrom reconstruit la configuration PAR PORT depuis la table des VLAN et la
// page de commutation.
//
// C'est ici que se fait la bascule : the device raisonne par VLAN (« qui est
// membre ? ») et par port pour le seul VLAN natif, meshcraft raisonne par port
// (« que porte ce port ? »). La rule d'tagging appliquee est celle documentee en
// tete de fichier.
func portsFrom(vlanRows []*Page, fwd *Page, f Facade) (
	map[string]PortState, VLANFiltering, error) {
	var ports = f.Ports()
	var dvid, hasDvid = fwd.Nums(FieldDefaultVLAN)
	if !hasDvid {
		return nil, nil, fmt.Errorf("/fwd.b ne porte pas le tableau « %s » : "+
			"sans les VLAN natifs, l'appartenance des ports ne peut pas be reconstruite",
			FieldDefaultVLAN)
	}
	if len(dvid) < ports {
		return nil, nil, fmt.Errorf("/fwd.b : « %s » ne porte que %d valeur(s) pour "+
			"%d port(s) — page tronquee ou geometry incoherente",
			FieldDefaultVLAN, len(dvid), ports)
	}
	// Le mode VLAN est facultatif a la lecture : son absence n'empeche pas de lire
	// l'appartenance, elle empeche seulement de dire si le filtrage est actif. On
	// distingue les deux rather que de refuser la lecture entire.
	var modes, hasModes = fwd.Nums(FieldVLANMode)

	var out = map[string]PortState{}
	var filtering = VLANFiltering{}

	var i int
	for i = 0; i < ports; i++ {
		var name = f.Name(i)
		var st = PortState{Mode: "access", PVID: int(dvid[i])}

		var row *Page
		for _, row = range vlanRows {
			var vid, ok = row.Num(FieldVLANID)
			if !ok {
				// vlansFrom a already refused ce cas ; ici on ne fabrique rien.
				continue
			}
			var member bool
			member, ok = row.Bit(FieldVLANMembers, i)
			if !ok {
				return nil, nil, fmt.Errorf("/vlan.b : le VLAN %d ne porte pas de masque "+
					"de membres lisible (« %s ») — l'appartenance du port %s serait devinee",
					vid, FieldVLANMembers, name)
			}
			if !member {
				continue
			}
			// Le VLAN natif du port n'est pas tagged sur ce port : c'est toute la rule.
			if int(vid) == st.PVID {
				continue
			}
			st.Tagged = append(st.Tagged, int(vid))
		}
		if len(st.Tagged) > 0 {
			// Un port qui transporte des VLAN tagged est un trunk, quel que soit son
			// VLAN natif.
			sort.Ints(st.Tagged)
			st.Mode = "trunk"
		}
		out[name] = st

		if hasModes && i < len(modes) {
			var label, ok = VLANModeLabel(modes[i])
			if ok {
				filtering[name] = label
			}
		}
	}
	return out, filtering, nil
}

// linksFrom observes les ports qui portent un lien.
//
// Seuls les ports AVEC lien sont rendus : l'absence dans la carte vaut « pas de
// lien », ce qui est aussi le default prudent quand la lecture n'a rien dit. Le
// masque manquant, en revanche, est une ERREUR et non un « aucun lien » — croire
// qu'aucun port n'a de lien leverait le garde-fou de writing sur tous, ce qui est
// exactement l'inverse de la prudence.
func linksFrom(link *Page, f Facade) (map[string]bool, error) {
	var ports = f.Ports()
	if !link.Has(FieldLink) {
		return nil, fmt.Errorf("/link.b ne porte pas le masque « %s » : state des "+
			"liens est inconnu, et le supposer vide ferait croire qu'aucun port ne "+
			"porte la session en cours", FieldLink)
	}
	var out = map[string]bool{}
	var i int
	for i = 0; i < ports; i++ {
		var up, ok = link.Bit(FieldLink, i)
		if !ok {
			return nil, fmt.Errorf("/link.b : masque « %s » trop court pour %d port(s)",
				FieldLink, ports)
		}
		if up {
			out[f.Name(i)] = true
		}
	}
	return out, nil
}

func joinComma(parts []string) string {
	var out string
	var i int
	for i = 0; i < len(parts); i++ {
		if i > 0 {
			out += ", "
		}
		out += parts[i]
	}
	return out
}
